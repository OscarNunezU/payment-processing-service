package acquirersim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	secretKey      = "sk_test"
	publishableKey = "pk_test"
	checkoutOrigin = "http://localhost:3000"
)

// harness runs the public and admin handlers of a fresh simulator.
type harness struct {
	sim    *Simulator
	public *httptest.Server
	admin  *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sim := New(Config{
		SecretKey:      secretKey,
		PublishableKey: publishableKey,
		AllowedOrigins: []string{checkoutOrigin},
		LateAnswer:     300 * time.Millisecond,
		TokenTTL:       time.Hour,
	}, slog.New(slog.DiscardHandler))
	h := &harness{sim: sim, public: httptest.NewServer(sim.PublicHandler()), admin: httptest.NewServer(sim.AdminHandler())}
	t.Cleanup(h.public.Close)
	t.Cleanup(h.admin.Close)
	return h
}

// answer is what a test needs from a response, once its body is read.
type answer struct {
	status int
	header http.Header
}

// call sends a JSON request and decodes the JSON answer into out, when out is
// not nil.
func call(t *testing.T, ctx context.Context, method, url, key string, headers map[string]string, body, out any) (answer, error) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode the answer to %s %s (%d): %v", method, url, resp.StatusCode, err)
		}
	}
	return answer{status: resp.StatusCode, header: resp.Header}, nil
}

type errorBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (h *harness) tokenize(t *testing.T, number string) string {
	t.Helper()
	var tok tokenResponse
	resp, err := call(t, t.Context(), http.MethodPost, h.public.URL+"/v1/tokens", publishableKey, nil,
		tokenRequest{Number: number, ExpMonth: 12, ExpYear: 2030, CVC: "123"}, &tok)
	if err != nil || resp.status != http.StatusCreated {
		t.Fatalf("tokenize %s: %v %v", number, resp, err)
	}
	return tok.Token
}

func (h *harness) authorize(t *testing.T, ctx context.Context, key, token string, out any) (answer, error) {
	t.Helper()
	return call(t, ctx, http.MethodPost, h.public.URL+"/v1/authorizations", secretKey,
		map[string]string{"Idempotency-Key": key},
		authorizeRequest{Token: token, Amount: 15000, Currency: "CLP", Reference: "order-1"}, out)
}

func (h *harness) reverse(t *testing.T, key string) reversalResponse {
	t.Helper()
	var out reversalResponse
	resp, err := call(t, t.Context(), http.MethodPost, h.public.URL+"/v1/authorizations/"+key+"/reversal", secretKey, nil, nil, &out)
	if err != nil || resp.status != http.StatusOK {
		t.Fatalf("reverse %s: %v %v", key, resp, err)
	}
	return out
}

func (h *harness) book(t *testing.T) []bookEntry {
	t.Helper()
	var out struct {
		Data []bookEntry `json:"data"`
	}
	if _, err := call(t, t.Context(), http.MethodGet, h.admin.URL+"/admin/book", "", nil, nil, &out); err != nil {
		t.Fatal(err)
	}
	return out.Data
}

func (h *harness) setFaults(t *testing.T, f Faults) {
	t.Helper()
	resp, err := call(t, t.Context(), http.MethodPut, h.admin.URL+"/admin/faults", "", nil, f, nil)
	if err != nil || resp.status != http.StatusOK {
		t.Fatalf("set the faults: %v %v", resp, err)
	}
}

func TestTokenize(t *testing.T) {
	h := newHarness(t)
	var tok tokenResponse
	resp, err := call(t, t.Context(), http.MethodPost, h.public.URL+"/v1/tokens", publishableKey,
		map[string]string{"Origin": checkoutOrigin},
		tokenRequest{Number: "4111111111111111", ExpMonth: 12, ExpYear: 2030, CVC: "123"}, &tok)
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusCreated || !strings.HasPrefix(tok.Token, "tok_") || tok.Brand != "visa" || tok.Last4 != "1111" {
		t.Errorf("got %d %+v, want 201 with a visa token", resp.status, tok)
	}
	if got := resp.header.Get("Access-Control-Allow-Origin"); got != checkoutOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, checkoutOrigin)
	}
}

func TestTokenizeRejectsBadCards(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name string
		req  tokenRequest
		code string
	}{
		{"wrong check digit", tokenRequest{Number: "4111111111111112", ExpMonth: 12, ExpYear: 2030, CVC: "123"}, "invalid_number"},
		{"month 13", tokenRequest{Number: "4111111111111111", ExpMonth: 13, ExpYear: 2030, CVC: "123"}, "invalid_expiry"},
		{"expired", tokenRequest{Number: "4111111111111111", ExpMonth: 1, ExpYear: 2020, CVC: "123"}, "expired_card"},
		{"short CVC", tokenRequest{Number: "4111111111111111", ExpMonth: 12, ExpYear: 2030, CVC: "12"}, "invalid_cvc"},
	}
	for _, tt := range tests {
		var body errorBody
		resp, err := call(t, t.Context(), http.MethodPost, h.public.URL+"/v1/tokens", publishableKey, nil, tt.req, &body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.status != http.StatusBadRequest || body.Error.Code != tt.code {
			t.Errorf("%s: got %d %s, want 400 %s", tt.name, resp.status, body.Error.Code, tt.code)
		}
	}
}

func TestKeysAreChecked(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name, path, key string
	}{
		{"a token without a key", "/v1/tokens", ""},
		{"a token with a wrong key", "/v1/tokens", "pk_wrong"},
		{"an authorization with the publishable key", "/v1/authorizations", publishableKey},
		{"a reversal without a key", "/v1/authorizations/k1/reversal", ""},
	}
	for _, tt := range tests {
		resp, err := call(t, t.Context(), http.MethodPost, h.public.URL+tt.path, tt.key, nil, map[string]any{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.status != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", tt.name, resp.status)
		}
	}
}

func TestOnlyAllowedPagesPassThePreflight(t *testing.T) {
	h := newHarness(t)
	for origin, want := range map[string]int{checkoutOrigin: http.StatusNoContent, "https://evil.example": http.StatusForbidden} {
		resp, err := call(t, t.Context(), http.MethodOptions, h.public.URL+"/v1/tokens", "", map[string]string{"Origin": origin}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.status != want {
			t.Errorf("%s: got %d, want %d", origin, resp.status, want)
		}
	}
}

func TestAuthorizeAndReplay(t *testing.T) {
	h := newHarness(t)
	token := h.tokenize(t, "4111111111111111")

	var first authorizationResponse
	resp, err := h.authorize(t, t.Context(), "pay-1", token, &first)
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusOK || first.Status != Approved || first.AuthorizationCode == "" || first.Card.Last4 != "1111" {
		t.Fatalf("got %d %+v, want 200 approved", resp.status, first)
	}

	var again authorizationResponse
	resp, err = h.authorize(t, t.Context(), "pay-1", token, &again)
	if err != nil {
		t.Fatal(err)
	}
	if again.AuthorizationCode != first.AuthorizationCode || resp.header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("got %+v replayed=%q, want the first answer replayed", again, resp.header.Get("Idempotent-Replayed"))
	}
}

func TestAuthorizeErrors(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name   string
		number string
		status int
		code   string
	}{
		{"unavailable", "4000000000000119", http.StatusServiceUnavailable, "unavailable"},
		{"rejected", "4000000000000143", http.StatusUnprocessableEntity, "request_rejected"},
	}
	for _, tt := range tests {
		var body errorBody
		resp, err := h.authorize(t, t.Context(), "pay-"+tt.name, h.tokenize(t, tt.number), &body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.status != tt.status || body.Error.Code != tt.code {
			t.Errorf("%s: got %d %s, want %d %s", tt.name, resp.status, body.Error.Code, tt.status, tt.code)
		}
	}

	var body errorBody
	resp, err := call(t, t.Context(), http.MethodPost, h.public.URL+"/v1/authorizations", secretKey, nil,
		authorizeRequest{Token: "tok_x", Amount: 1, Currency: "CLP"}, &body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusBadRequest || body.Error.Code != "invalid_idempotency_key" {
		t.Errorf("without a key: got %d %s, want 400 invalid_idempotency_key", resp.status, body.Error.Code)
	}
}

func TestALateAnswerIsDecidedBeforeTheClientGivesUp(t *testing.T) {
	h := newHarness(t)
	token := h.tokenize(t, "4000000000000135")

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := h.authorize(t, ctx, "pay-1", token, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the client to time out", err)
	}

	// The retry gets the approval at once, without waiting again.
	start := time.Now()
	var again authorizationResponse
	if _, err := h.authorize(t, t.Context(), "pay-1", token, &again); err != nil {
		t.Fatal(err)
	}
	if again.Status != Approved || time.Since(start) > 200*time.Millisecond {
		t.Errorf("got %s after %s, want approved at once", again.Status, time.Since(start))
	}
}

func TestNoAnswerThenReversal(t *testing.T) {
	h := newHarness(t)
	token := h.tokenize(t, "4000000000000150")

	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		_, err := h.authorize(t, ctx, "pay-1", token, nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: got %v, want no answer", attempt, err)
		}
	}
	if book := h.book(t); len(book) != 1 || !book[0].Live {
		t.Fatalf("got %+v, want one live authorization the client never heard of", book)
	}

	if rev := h.reverse(t, "pay-1"); rev.Status != "reversed" || rev.Outcome != Approved {
		t.Errorf("got %+v, want an approval reversed", rev)
	}
	if book := h.book(t); book[0].Live || !book[0].Reversed {
		t.Errorf("got %+v, want it reversed and no longer live", book[0])
	}

	// An attempt that was delayed in the network arrives after the reversal.
	var body errorBody
	resp, err := h.authorize(t, t.Context(), "pay-1", token, &body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusConflict || body.Error.Code != "reversed" {
		t.Errorf("got %d %s, want 409 reversed", resp.status, body.Error.Code)
	}
}

func TestErrorRateAnswers503WithoutProcessing(t *testing.T) {
	h := newHarness(t)
	h.setFaults(t, Faults{ErrorRate: 1})

	resp, err := h.authorize(t, t.Context(), "pay-1", h.tokenize(t, "4111111111111111"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.status != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", resp.status)
	}
	if book := h.book(t); len(book) != 0 {
		t.Errorf("got %+v, want nothing recorded", book)
	}
}

func TestTimeoutRateProcessesButAnswersLate(t *testing.T) {
	h := newHarness(t)
	h.setFaults(t, Faults{TimeoutRate: 1})

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := h.authorize(t, ctx, "pay-1", h.tokenize(t, "4111111111111111"), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the client to time out", err)
	}
	if book := h.book(t); len(book) != 1 || !book[0].Live {
		t.Errorf("got %+v, want the authorization recorded", book)
	}
}

func TestLatencyIsAdded(t *testing.T) {
	h := newHarness(t)
	h.setFaults(t, Faults{LatencyMS: 150})
	token := h.tokenize(t, "4111111111111111")

	start := time.Now()
	if _, err := h.authorize(t, t.Context(), "pay-1", token, nil); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("answered in %s, want at least 150ms", elapsed)
	}
}

func TestInvalidFaultsAreRejected(t *testing.T) {
	h := newHarness(t)
	for _, f := range []Faults{{LatencyMS: -1}, {ErrorRate: 1.5}, {TimeoutRate: -0.1}} {
		resp, err := call(t, t.Context(), http.MethodPut, h.admin.URL+"/admin/faults", "", nil, f, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.status != http.StatusBadRequest {
			t.Errorf("%+v: got %d, want 400", f, resp.status)
		}
	}
	var current Faults
	if _, err := call(t, t.Context(), http.MethodGet, h.admin.URL+"/admin/faults", "", nil, nil, &current); err != nil {
		t.Fatal(err)
	}
	if current != (Faults{}) {
		t.Errorf("got %+v, want the faults unchanged", current)
	}
}
