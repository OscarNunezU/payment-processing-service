package acquirer

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestAReversalGoesToTheKeyOfThePayment(t *testing.T) {
	var method, path, auth, key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth, key = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		_, _ = w.Write([]byte(`{"key":"pay-1","status":"reversed","outcome":"approved"}`))
	}))
	defer srv.Close()

	a := NewClient(srv.URL, "sk_sim", time.Second, newBreaker()).Reverse(t.Context(), "pay-1")
	if a.Outcome != domain.OutcomeReversed || a.ResponseCode != "200" || a.Latency <= 0 {
		t.Errorf("got %+v, want a confirmed reversal with its latency", a)
	}
	if method != http.MethodPost || path != "/v1/authorizations/pay-1/reversal" || auth != "Bearer sk_sim" || key != "" {
		t.Errorf("the acquirer got %s %s, auth %q, key %q", method, path, auth, key)
	}
}

func TestOnlyAConfirmationReversesThePayment(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"unavailable", 503, `{"error":{"code":"unavailable"}}`, "503 unavailable"},
		{"rejected", 400, `{"error":{"code":"invalid_key"}}`, "400 invalid_key"},
		{"a 200 that does not confirm it", 200, `{"status":"pending"}`, "200"},
		{"a body that is not JSON", 200, `<html>`, "200"},
	}
	for _, tt := range tests {
		srv := answering(t, tt.status, tt.body)
		a := NewClient(srv.URL, "sk_sim", time.Second, newBreaker()).Reverse(t.Context(), "pay-1")
		if a.Outcome != domain.OutcomeUncertain || a.ResponseCode != tt.code {
			t.Errorf("%s: got %s %q, want uncertain %q", tt.name, a.Outcome, a.ResponseCode, tt.code)
		}
	}
}

func TestReversalsDoNotCountForTheBreaker(t *testing.T) {
	srv := answering(t, 503, ``)
	b := newBreaker()
	c := NewClient(srv.URL, "sk_sim", time.Second, b)
	for range 5 {
		c.Reverse(t.Context(), "pay-1")
	}
	if b.Open() {
		t.Error("5 failed reversals opened the breaker")
	}
}

// The bug the consistency checker found under faults: the acquirer approves a
// call on a kept-alive connection, and then goes down before answering. Go's
// transport replays a request that carries an Idempotency-Key on a new
// connection, which is refused. The refusal is the error the client sees,
// but the request was sent, and approved: it must be uncertain, never not
// sent, or the payment would fail without its reversal.
func TestARequestSentBeforeARefusedRetryIsUncertain(t *testing.T) {
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"status":"approved","authorization_code":"7T4N4V","card":{"brand":"visa","last4":"1111"}}`))
			return
		}
		// The second call reaches the acquirer, which goes down before it
		// answers: the port closes, and so does the connection.
		ln.Close()
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			conn.Close()
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	c := NewClient("http://"+ln.Addr().String(), "sk_sim", 2*time.Second, newBreaker())
	if a := c.Authorize(t.Context(), request); a.Outcome != domain.OutcomeApproved {
		t.Fatalf("the first call: got %+v", a)
	}
	a := c.Authorize(t.Context(), request)
	if n := calls.Load(); n != 2 {
		t.Fatalf("the acquirer got %d calls, want 2", n)
	}
	if a.Outcome != domain.OutcomeUncertain {
		t.Errorf("got %s %q, want uncertain: the request reached the acquirer before the refusal", a.Outcome, a.ResponseCode)
	}
}
