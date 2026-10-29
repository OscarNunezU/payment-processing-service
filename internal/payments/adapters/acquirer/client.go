// Package acquirer is the client of the acquirer: it authorizes payments over
// HTTP and classifies every answer (RFC 0001, section 5.3), and it guards the
// acquirer with a circuit breaker and a limit of payments in flight (section
// 5.4).
package acquirer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Client calls the acquirer's authorization API.
type Client struct {
	baseURL string
	key     string
	timeout time.Duration
	http    *http.Client
	breaker *Breaker
	now     func() time.Time
}

// NewClient returns a client of the acquirer at baseURL, authenticated with
// its secret key. Each call ends after timeout. Every answer is reported to
// breaker.
func NewClient(baseURL, secretKey string, timeout time.Duration, breaker *Breaker) *Client {
	return &Client{
		baseURL: baseURL,
		key:     secretKey,
		timeout: timeout,
		http:    &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)},
		breaker: breaker,
		now:     time.Now,
	}
}

type authorizeBody struct {
	Token     string `json:"token"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Reference string `json:"reference"`
}

type authorizationAnswer struct {
	Status            string `json:"status"`
	AuthorizationCode string `json:"authorization_code"`
	DeclineCode       string `json:"decline_code"`
	Card              struct {
		Brand string `json:"brand"`
		Last4 string `json:"last4"`
	} `json:"card"`
}

type errorAnswer struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// Authorize sends the authorization with r.Key as its idempotency key, and
// returns the classified answer. It never returns an error: a failed call is
// an answer that is not sent or uncertain.
func (c *Client) Authorize(ctx context.Context, r app.AuthorizationRequest) domain.Answer {
	start := c.now()
	a := c.authorize(ctx, r)
	a.Latency = c.now().Sub(start)
	c.breaker.Record(a.Outcome)
	return a
}

func (c *Client) authorize(ctx context.Context, r app.AuthorizationRequest) domain.Answer {
	body, err := json.Marshal(authorizeBody{Token: r.Token, Amount: r.Amount, Currency: string(r.Currency), Reference: r.Reference})
	if err != nil {
		return domain.Answer{Outcome: domain.OutcomeNotSent, ResponseCode: "unencodable_request"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/authorizations", bytes.NewReader(body))
	if err != nil {
		return domain.Answer{Outcome: domain.OutcomeNotSent, ResponseCode: "invalid_request"}
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Idempotency-Key", r.Key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return failedCall(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		// The acquirer answered, but the answer did not arrive whole.
		return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: strconv.Itoa(resp.StatusCode) + " unreadable"}
	}
	return classify(resp.StatusCode, raw)
}

// failedCall classifies a call that got no answer. Only a refused connection
// proves the request never arrived; anything else may have reached the
// acquirer.
func failedCall(err error) domain.Answer {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return domain.Answer{Outcome: domain.OutcomeNotSent, ResponseCode: "connection_refused"}
	case errors.Is(err, context.DeadlineExceeded):
		return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: "timeout"}
	default:
		return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: "connection_failed"}
	}
}

// classify turns an HTTP answer into an outcome. An answer without its
// decision, or without the code that comes with it, counts as unreadable: it
// is never taken for a decline.
func classify(status int, raw []byte) domain.Answer {
	code := strconv.Itoa(status)
	switch {
	case status == http.StatusOK:
		var a authorizationAnswer
		if err := json.Unmarshal(raw, &a); err != nil {
			return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: code + " unreadable"}
		}
		answer := domain.Answer{ResponseCode: code, CardBrand: a.Card.Brand, CardLast4: a.Card.Last4}
		switch {
		case a.Status == "approved" && a.AuthorizationCode != "":
			answer.Outcome, answer.AuthorizationCode = domain.OutcomeApproved, a.AuthorizationCode
		case a.Status == "declined" && a.DeclineCode != "":
			answer.Outcome, answer.DeclineCode = domain.OutcomeDeclined, a.DeclineCode
		default:
			return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: code + " incomplete"}
		}
		return answer
	case status == http.StatusConflict && errorCode(raw) == "reversed":
		return domain.Answer{Outcome: domain.OutcomeReversed, ResponseCode: "409 reversed"}
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests, status >= 500:
		return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: code}
	case status >= 400:
		if c := errorCode(raw); c != "" {
			code += " " + c
		}
		return domain.Answer{Outcome: domain.OutcomeRejected, ResponseCode: code}
	default:
		return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: code + " unexpected"}
	}
}

func errorCode(raw []byte) string {
	var e errorAnswer
	_ = json.Unmarshal(raw, &e)
	return e.Error.Code
}
