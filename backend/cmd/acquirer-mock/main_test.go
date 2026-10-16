package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func post(a *acquirer, key, card string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"transaction_id":%q,"amount":15000,"currency":"CLP",
		"card":{"number":%q,"expiry_month":12,"expiry_year":2030,"cvv":"123"}}`, key, card)
	req := httptest.NewRequest(http.MethodPost, "/authorizations", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	a.authorize(rec, req)
	return rec
}

func authorize(t *testing.T, a *acquirer, key, card string) authorizeResponse {
	t.Helper()
	rec := post(a, key, card)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var resp authorizeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestDecidesByCardNumber(t *testing.T) {
	tests := []struct {
		card       string
		approved   bool
		wantReason string
	}{
		{"4111111111111111", true, ""},
		{"4000000000000002", false, "card_declined"},
		{"4000000000009995", false, "insufficient_funds"},
	}
	for _, tt := range tests {
		resp := authorize(t, newAcquirer(0), "txn-"+tt.card, tt.card)

		if resp.Approved != tt.approved || resp.DeclineReason != tt.wantReason {
			t.Errorf("card %s = %+v, want approved=%v reason=%q", tt.card, resp, tt.approved, tt.wantReason)
		}
		if tt.approved && len(resp.AuthorizationCode) != 6 {
			t.Errorf("card %s: authorization code %q, want 6 characters", tt.card, resp.AuthorizationCode)
		}
	}
}

func TestResendingAPaymentReturnsTheSameAnswer(t *testing.T) {
	a := newAcquirer(0)

	first := authorize(t, a, "txn-1", "4111111111111111")
	second := authorize(t, a, "txn-1", "4111111111111111")

	if first.AuthorizationCode != second.AuthorizationCode {
		t.Fatalf("authorization codes %q and %q differ: the payment was authorized twice",
			first.AuthorizationCode, second.AuthorizationCode)
	}
}

func TestRejectsARequestWithoutIdempotencyKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/authorizations", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	newAcquirer(0).authorize(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestFailureCards(t *testing.T) {
	tests := []struct {
		card string
		want []int // the status of two calls with the same key
	}{
		{cardUnavailable, []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable}},
		{cardUnavailableOnce, []int{http.StatusServiceUnavailable, http.StatusOK}},
		{cardRefused, []int{http.StatusUnprocessableEntity, http.StatusUnprocessableEntity}},
	}
	for _, tt := range tests {
		a := newAcquirer(0)
		for i, want := range tt.want {
			if rec := post(a, "txn-1", tt.card); rec.Code != want {
				t.Errorf("card %s, call %d: status %d, want %d", tt.card, i+1, rec.Code, want)
			}
		}
	}
}

func TestTheSlowCardAnswersLateOnceAndTheRetryGetsTheSameApproval(t *testing.T) {
	a := newAcquirer(50 * time.Millisecond)

	start := time.Now()
	first := authorize(t, a, "txn-1", cardSlowOnce)
	slow := time.Since(start)
	start = time.Now()
	second := authorize(t, a, "txn-1", cardSlowOnce)
	fast := time.Since(start)

	if slow < 50*time.Millisecond || fast >= 50*time.Millisecond {
		t.Fatalf("the first call took %v and the retry %v, want the first slow and the retry immediate", slow, fast)
	}
	if !first.Approved || first.AuthorizationCode != second.AuthorizationCode {
		t.Fatalf("answers %+v and %+v, want one approval given twice", first, second)
	}
}
