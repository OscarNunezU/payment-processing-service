// Package acquirer is the client of the acquirer: it authorizes and reverses
// payments over HTTP and classifies every answer (RFC 0001, section 5.3), and
// it guards the acquirer with a circuit breaker and a limit of payments in
// flight (section 5.4).
package acquirer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Client calls the acquirer's authorization and reversal API.
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
		http:    &http.Client{Transport: otelhttp.NewTransport(keepAlive(512))},
		breaker: breaker,
		now:     time.Now,
	}
}

// keepAlive returns a transport that keeps up to idle connections to each
// host. Go's default keeps 2, so with many payments in flight most calls would
// open a connection of their own. 512 is the API's limit of payments in
// flight (RFC 0001, section 5.4).
func keepAlive(idle int) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = idle
	t.MaxIdleConnsPerHost = idle
	return t
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
	countAnswer(ctx, "authorize", a.Outcome)
	return a
}

func (c *Client) authorize(ctx context.Context, r app.AuthorizationRequest) domain.Answer {
	body, err := json.Marshal(authorizeBody{Token: r.Token, Amount: r.Amount, Currency: string(r.Currency), Reference: r.Reference})
	if err != nil {
		return domain.Answer{Outcome: domain.OutcomeNotSent, ResponseCode: "unencodable_request"}
	}
	status, raw, failure := c.post(ctx, "/v1/authorizations", r.Key, body)
	if failure != nil {
		return *failure
	}
	return classify(status, raw)
}

// Reverse releases the authorization of key, if there is one, and leaves the
// key unusable at the acquirer (RFC 0001, section 5.3). Only an answer that
// confirms the reversal is OutcomeReversed; any other, a rejection included,
// leaves it unconfirmed, and the recovery sends it again.
//
// Reversals do not count for the breaker: the recovery sends them whatever
// its state, since they are what keeps a failed payment from being charged.
func (c *Client) Reverse(ctx context.Context, key string) domain.Answer {
	start := c.now()
	a := c.reverse(ctx, key)
	a.Latency = c.now().Sub(start)
	countAnswer(ctx, "reverse", a.Outcome)
	return a
}

func (c *Client) reverse(ctx context.Context, key string) domain.Answer {
	status, raw, failure := c.post(ctx, "/v1/authorizations/"+url.PathEscape(key)+"/reversal", "", nil)
	if failure != nil {
		return *failure
	}
	code := strconv.Itoa(status)
	var answer struct {
		Status string `json:"status"`
	}
	if status == http.StatusOK && json.Unmarshal(raw, &answer) == nil && answer.Status == "reversed" {
		return domain.Answer{Outcome: domain.OutcomeReversed, ResponseCode: code}
	}
	if c := errorCode(raw); c != "" {
		code += " " + c
	}
	return domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: code}
}

// post sends body to path, with the idempotency key when there is one, and
// returns the answer once it arrived whole. A call that got no whole answer
// comes back as failure instead, already classified.
func (c *Client) post(ctx context.Context, path, idempotencyKey string, body []byte) (int, []byte, *domain.Answer) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// Go's transport replays a request that carries an Idempotency-Key on a
	// new connection when a kept-alive one fails, and returns the error of
	// the replay. A refused replay says nothing about the first try, which
	// may have reached the acquirer: only a request that never had a
	// connection was certainly not sent.
	var connected atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, &domain.Answer{Outcome: domain.OutcomeNotSent, ResponseCode: "invalid_request"}
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		failure := failedCall(err, connected.Load())
		return 0, nil, &failure
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		// The acquirer answered, but the answer did not arrive whole.
		return 0, nil, &domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: strconv.Itoa(resp.StatusCode) + " unreadable"}
	}
	return resp.StatusCode, raw, nil
}

// failedCall classifies a call that got no answer. Only a refused connection,
// for a request that never had one, proves the request never arrived;
// anything else may have reached the acquirer.
func failedCall(err error, connected bool) domain.Answer {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED) && !connected:
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
