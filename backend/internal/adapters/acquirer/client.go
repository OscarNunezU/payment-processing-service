// Package acquirer calls the external acquirer over HTTP.
package acquirer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

// maxResponseBytes caps what we read from the acquirer: its answer is tiny.
const maxResponseBytes = 1 << 20

type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a client that cuts every call after timeout, so a hung
// acquirer can never hold a worker forever.
func NewClient(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

type authorizeRequest struct {
	TransactionID string      `json:"transaction_id"`
	Amount        int64       `json:"amount"`
	Currency      string      `json:"currency"`
	Card          cardPayload `json:"card"`
}

type cardPayload struct {
	Number      string `json:"number"`
	ExpiryMonth int    `json:"expiry_month"`
	ExpiryYear  int    `json:"expiry_year"`
	CVV         string `json:"cvv"`
}

type authorizeResponse struct {
	Approved          bool   `json:"approved"`
	AuthorizationCode string `json:"authorization_code"`
	DeclineReason     string `json:"decline_reason"`
}

func (c *Client) Authorize(ctx context.Context, req payment.AuthorizationRequest) (payment.AuthorizationResult, error) {
	body, err := json.Marshal(authorizeRequest{
		TransactionID: req.TransactionID,
		Amount:        req.Amount,
		Currency:      req.Currency,
		Card: cardPayload{
			Number:      req.Card.Number,
			ExpiryMonth: req.Card.ExpiryMonth,
			ExpiryYear:  req.Card.ExpiryYear,
			CVV:         req.Card.CVV,
		},
	})
	if err != nil {
		return payment.AuthorizationResult{}, fmt.Errorf("encode authorization: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/authorizations", bytes.NewReader(body))
	if err != nil {
		return payment.AuthorizationResult{}, fmt.Errorf("build authorization request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", req.TransactionID)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return payment.AuthorizationResult{}, fmt.Errorf("call acquirer: %w", err)
	}
	defer func() {
		io.Copy(io.Discard, resp.Body) // read to the end so the connection can be reused
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return payment.AuthorizationResult{}, fmt.Errorf("acquirer answered %d", resp.StatusCode)
	}

	var out authorizeResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return payment.AuthorizationResult{}, fmt.Errorf("decode acquirer response: %w", err)
	}
	return payment.AuthorizationResult{
		Approved:          out.Approved,
		AuthorizationCode: out.AuthorizationCode,
		DeclineReason:     out.DeclineReason,
	}, nil
}
