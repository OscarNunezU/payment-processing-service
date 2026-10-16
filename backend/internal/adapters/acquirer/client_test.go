package acquirer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

var request = payment.AuthorizationRequest{
	TransactionID: "txn-1",
	Amount:        15000,
	Currency:      "CLP",
	Card:          payment.Card{Number: "4111111111111111", ExpiryMonth: 12, ExpiryYear: 2030, CVV: "123"},
}

func TestAuthorizeSendsThePaymentAndReadsTheAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got authorizeRequest
		json.NewDecoder(r.Body).Decode(&got)

		if r.Method != http.MethodPost || r.URL.Path != "/authorizations" ||
			r.Header.Get("Idempotency-Key") != "txn-1" || got.Amount != 15000 || got.Card.Number != "4111111111111111" {
			t.Errorf("unexpected request: %s %s key=%q body=%+v", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"), got)
		}
		w.Write([]byte(`{"transaction_id":"txn-1","approved":true,"authorization_code":"A1B2C3"}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, time.Second).Authorize(context.Background(), request)
	if err != nil || !result.Approved || result.AuthorizationCode != "A1B2C3" {
		t.Fatalf("Authorize() = %+v, %v; want approved with code A1B2C3", result, err)
	}
}

func TestADeclineIsAnAnswerNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"transaction_id":"txn-1","approved":false,"decline_reason":"insufficient_funds"}`))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, time.Second).Authorize(context.Background(), request)
	if err != nil || result.Approved || result.DeclineReason != "insufficient_funds" {
		t.Fatalf("Authorize() = %+v, %v; want a decline with its reason and no error", result, err)
	}
}

func TestAuthorizeFailsWhenNoAnswerCanBeObtained(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}},
		{"rate limited", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}},
		{"response is not json", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`not json`))
		}},
		{"acquirer too slow", func(w http.ResponseWriter, r *http.Request) {
			// Reading the body lets the server notice when the client hangs up.
			io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			start := time.Now()
			_, err := NewClient(server.URL, 100*time.Millisecond).Authorize(context.Background(), request)
			if err == nil || errors.Is(err, payment.ErrAcquirerRejected) {
				t.Fatalf("Authorize() = %v, want an error that can be retried", err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("Authorize() took %v: the timeout did not cut the call", elapsed)
			}
		})
	}
}

func TestARefusedRequestCannotBeRetried(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))

		_, err := NewClient(server.URL, time.Second).Authorize(context.Background(), request)
		server.Close()
		if !errors.Is(err, payment.ErrAcquirerRejected) {
			t.Errorf("status %d: Authorize() = %v, want ErrAcquirerRejected", status, err)
		}
	}
}
