package acquirer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

var request = app.AuthorizationRequest{Key: "pay-1", Token: "tok_1", Amount: 15000, Currency: domain.CLP, Reference: "1001"}

func newBreaker() *Breaker { return NewBreaker(5, 30*time.Second, time.Now) }

// answering serves every authorization with status and body.
func answering(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheRequestCarriesTheKeyAndThePayment(t *testing.T) {
	var got struct {
		auth, key string
		body      authorizeBody
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth, got.key = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		_, _ = w.Write([]byte(`{"status":"approved","authorization_code":"7T4N4V","card":{"brand":"visa","last4":"1111"}}`))
	}))
	defer srv.Close()

	a := NewClient(srv.URL, "sk_sim", time.Second, newBreaker()).Authorize(t.Context(), request)
	if a.Outcome != domain.OutcomeApproved || a.AuthorizationCode != "7T4N4V" || a.CardBrand != "visa" || a.CardLast4 != "1111" || a.Latency <= 0 {
		t.Errorf("got %+v, want an approval with its code, card and latency", a)
	}
	if got.auth != "Bearer sk_sim" || got.key != "pay-1" || got.body != (authorizeBody{Token: "tok_1", Amount: 15000, Currency: "CLP", Reference: "1001"}) {
		t.Errorf("the acquirer got %+v", got)
	}
}

func TestAnswersAreClassified(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		outcome domain.Outcome
		code    string
	}{
		{"declined", 200, `{"status":"declined","decline_code":"card_declined"}`, domain.OutcomeDeclined, "200"},
		{"an approval without its code", 200, `{"status":"approved"}`, domain.OutcomeUncertain, "200 incomplete"},
		{"a decline without its code", 200, `{"status":"declined"}`, domain.OutcomeUncertain, "200 incomplete"},
		{"a body that is not JSON", 200, `<html>`, domain.OutcomeUncertain, "200 unreadable"},
		{"a reversed key", 409, `{"error":{"code":"reversed"}}`, domain.OutcomeReversed, "409 reversed"},
		{"a rejected token", 422, `{"error":{"code":"token_unknown"}}`, domain.OutcomeRejected, "422 token_unknown"},
		{"a rejected request without a body", 400, ``, domain.OutcomeRejected, "400"},
		{"unavailable", 503, `{"error":{"code":"unavailable"}}`, domain.OutcomeUncertain, "503"},
		{"too many requests", 429, ``, domain.OutcomeUncertain, "429"},
		{"request timeout", 408, ``, domain.OutcomeUncertain, "408"},
		{"a redirect", 302, ``, domain.OutcomeUncertain, "302 unexpected"},
	}
	for _, tt := range tests {
		srv := answering(t, tt.status, tt.body)
		a := NewClient(srv.URL, "sk_sim", time.Second, newBreaker()).Authorize(t.Context(), request)
		if a.Outcome != tt.outcome || a.ResponseCode != tt.code {
			t.Errorf("%s: got %s %q, want %s %q", tt.name, a.Outcome, a.ResponseCode, tt.outcome, tt.code)
		}
	}
}

func TestATimeoutIsUncertain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	a := NewClient(srv.URL, "sk_sim", 50*time.Millisecond, newBreaker()).Authorize(t.Context(), request)
	if a.Outcome != domain.OutcomeUncertain || a.ResponseCode != "timeout" {
		t.Errorf("got %s %q, want uncertain timeout", a.Outcome, a.ResponseCode)
	}
}

func TestARefusedConnectionWasNotSent(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	a := NewClient("http://"+addr, "sk_sim", time.Second, newBreaker()).Authorize(t.Context(), request)
	if a.Outcome != domain.OutcomeNotSent || a.ResponseCode != "connection_refused" {
		t.Errorf("got %s %q, want not_sent connection_refused", a.Outcome, a.ResponseCode)
	}
}

func TestACutConnectionIsUncertain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			conn.Close() // the request arrived, and the answer never leaves
		}
	}))
	defer srv.Close()
	a := NewClient(srv.URL, "sk_sim", time.Second, newBreaker()).Authorize(context.Background(), request)
	if a.Outcome != domain.OutcomeUncertain || a.ResponseCode != "connection_failed" {
		t.Errorf("got %s %q, want uncertain connection_failed", a.Outcome, a.ResponseCode)
	}
}

func TestEveryAnswerReachesTheBreaker(t *testing.T) {
	srv := answering(t, 503, ``)
	b := newBreaker()
	c := NewClient(srv.URL, "sk_sim", time.Second, b)
	for range 5 {
		c.Authorize(t.Context(), request)
	}
	if !b.Open() {
		t.Error("5 uncertain answers in a row did not open the breaker")
	}
}
