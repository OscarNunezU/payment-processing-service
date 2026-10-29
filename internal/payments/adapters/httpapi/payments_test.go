package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
)

const paymentBody = `{"order_id":"1001","amount":15000,"currency":"CLP","card_token":"tok_1"}`

func (a *testAPI) pay(t *testing.T, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	return a.do(t, http.MethodPost, "/v1/payments", "Bearer "+a.key, body, "Idempotency-Key", key)
}

func TestCreateAPayment(t *testing.T) {
	api := newTestAPI(t)
	rec := api.pay(t, "k1", paymentBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s, want 201", rec.Code, rec.Body)
	}
	p := decodeInto[paymentResponse](t, rec)
	if p.Status != domain.PaymentApproved || p.AuthorizationCode != "7T4N4V" || p.OrderID != "1001" || p.Amount != 15000 ||
		p.Currency != domain.CLP || p.Card == nil || p.Card.Last4 != "1111" {
		t.Errorf("got %+v, want an APPROVED payment with its code and card", p)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/payments/"+p.ID {
		t.Errorf("Location = %q", loc)
	}
	if rec.Header().Get("Idempotent-Replayed") != "" {
		t.Error("a new payment is marked as replayed")
	}
	if strings.Contains(rec.Body.String(), "tok_1") {
		t.Error("the response shows the card token")
	}

	again := api.pay(t, "k1", paymentBody)
	if again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" || decodeInto[paymentResponse](t, again).ID != p.ID {
		t.Errorf("the retry: got %d %s, want 201 replayed with the same payment", again.Code, again.Body)
	}
}

func TestAPaymentStillProcessingAnswers202(t *testing.T) {
	api := newTestAPI(t)
	api.acquirer.Answer = domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: "timeout"}
	rec := api.pay(t, "k1", paymentBody)
	if rec.Code != http.StatusAccepted || decodeInto[paymentResponse](t, rec).Status != domain.PaymentProcessing {
		t.Errorf("got %d %s, want 202 PROCESSING", rec.Code, rec.Body)
	}
	if again := api.pay(t, "k1", paymentBody); again.Code != http.StatusAccepted || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("the retry: got %d, want 202 replayed", again.Code)
	}
}

func TestAClosedGateAnswers503ButNeverToARetry(t *testing.T) {
	api := newTestAPI(t)
	if rec := api.pay(t, "k1", paymentBody); rec.Code != http.StatusCreated {
		t.Fatalf("got %d", rec.Code)
	}
	api.gate.Closed = true

	rec := api.pay(t, "k2", strings.Replace(paymentBody, "1001", "1002", 1))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" ||
		decodeInto[httpx.ErrorBody](t, rec).Error.Code != "acquirer_unavailable" {
		t.Errorf("a new payment: got %d, want 503 acquirer_unavailable with Retry-After", rec.Code)
	}
	if again := api.pay(t, "k1", paymentBody); again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("a retry: got %d, want its payment", again.Code)
	}
}

func TestCreateErrors(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		body   string
		status int
		code   string
	}{
		{"no idempotency key", "", paymentBody, http.StatusBadRequest, "invalid_request"},
		{"a zero amount", "k1", `{"order_id":"1","amount":0,"currency":"CLP","card_token":"tok_1"}`, http.StatusBadRequest, "invalid_request"},
		{"an amount with decimals", "k1", `{"order_id":"1","amount":150.5,"currency":"CLP","card_token":"tok_1"}`, http.StatusBadRequest, "invalid_json"},
		{"an unknown field", "k1", `{"order_id":"1","amount":1,"currency":"CLP","card_token":"tok_1","merchant_id":"x"}`, http.StatusBadRequest, "invalid_json"},
		{"two objects", "k1", paymentBody + paymentBody, http.StatusBadRequest, "invalid_json"},
		{"a huge body", "k1", `{"order_id":"` + strings.Repeat("x", 70_000) + `"}`, http.StatusRequestEntityTooLarge, "body_too_large"},
		{"above the maximum", "k1", `{"order_id":"1","amount":5000001,"currency":"CLP","card_token":"tok_1"}`, http.StatusUnprocessableEntity, "amount_exceeds_max"},
		{"a currency the merchant does not accept", "k1", `{"order_id":"1","amount":100,"currency":"USD","card_token":"tok_1"}`, http.StatusUnprocessableEntity, "currency_not_accepted"},
	}
	for _, tt := range tests {
		api := newTestAPI(t)
		rec := api.pay(t, tt.key, tt.body)
		body := decodeInto[httpx.ErrorBody](t, rec)
		if rec.Code != tt.status || body.Error.Code != tt.code {
			t.Errorf("%s: got %d %s, want %d %s", tt.name, rec.Code, body.Error.Code, tt.status, tt.code)
		}
	}
}

func TestEveryInvalidFieldIsReported(t *testing.T) {
	api := newTestAPI(t)
	rec := api.pay(t, "", `{"order_id":"","amount":-5,"currency":"pesos","card_token":""}`)
	body := decodeInto[httpx.ErrorBody](t, rec)
	got := map[string]bool{}
	for _, f := range body.Error.Fields {
		got[f.Field] = true
	}
	for _, field := range []string{"Idempotency-Key", "order_id", "amount", "currency", "card_token"} {
		if !got[field] {
			t.Errorf("%s is not reported: %+v", field, body.Error.Fields)
		}
	}
}

func TestReuseConflictAndSuspension(t *testing.T) {
	api := newTestAPI(t)
	if rec := api.pay(t, "k1", paymentBody); rec.Code != http.StatusCreated {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := api.pay(t, "k1", strings.Replace(paymentBody, "15000", "12000", 1)); rec.Code != http.StatusUnprocessableEntity ||
		decodeInto[httpx.ErrorBody](t, rec).Error.Code != "idempotency_key_reused" {
		t.Errorf("the key with another amount: got %d", rec.Code)
	}
	if rec := api.pay(t, "k2", paymentBody); rec.Code != http.StatusConflict ||
		decodeInto[httpx.ErrorBody](t, rec).Error.Code != "order_has_active_payment" {
		t.Errorf("the order again: got %d", rec.Code)
	}

	suspended := tiendaUno
	suspended.ID, suspended.Status = "00000000-0000-4000-8000-000000000003", domain.MerchantSuspended
	key := api.store.AddMerchant(suspended)
	rec := api.do(t, http.MethodPost, "/v1/payments", "Bearer "+key, paymentBody, "Idempotency-Key", "k1")
	if rec.Code != http.StatusUnprocessableEntity || decodeInto[httpx.ErrorBody](t, rec).Error.Code != "merchant_suspended" {
		t.Errorf("a suspended merchant: got %d", rec.Code)
	}
}

func TestGetAPaymentWithItsHistory(t *testing.T) {
	api := newTestAPI(t)
	created := decodeInto[paymentResponse](t, api.pay(t, "k1", paymentBody))

	rec := api.do(t, http.MethodGet, "/v1/payments/"+created.ID, "Bearer "+api.key, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	got := decodeInto[paymentResponse](t, rec)
	if got.ID != created.ID || len(got.History) != 3 || got.History[0].Status != domain.PaymentProcessing ||
		got.History[1].Kind != domain.HistoryAcquirerCall || got.History[1].Outcome != "approved" || got.History[2].Status != domain.PaymentApproved {
		t.Errorf("got %+v", got)
	}

	other := tiendaUno
	other.ID = "00000000-0000-4000-8000-000000000002"
	otherKey := api.store.AddMerchant(other)
	for _, tt := range []struct{ name, path, key string }{
		{"another merchant's payment", "/v1/payments/" + created.ID, otherKey},
		{"an id that is not one", "/v1/payments/pay_123", api.key},
		{"an id in a form Postgres does not read", "/v1/payments/urn:uuid:" + created.ID, api.key},
		{"an id that does not exist", "/v1/payments/0199b5a2-7c1e-7d3a-9f00-0000000000a1", api.key},
	} {
		if rec := api.do(t, http.MethodGet, tt.path, "Bearer "+tt.key, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", tt.name, rec.Code)
		}
	}
}

func TestListPayments(t *testing.T) {
	api := newTestAPI(t)
	for i := range 3 {
		body := strings.Replace(paymentBody, "1001", fmt.Sprintf("o%d", i), 1)
		if rec := api.pay(t, fmt.Sprintf("k%d", i), body); rec.Code != http.StatusCreated {
			t.Fatal(rec.Body)
		}
	}

	rec := api.do(t, http.MethodGet, "/v1/payments?limit=2", "Bearer "+api.key, "")
	page := decodeInto[listResponse](t, rec)
	if rec.Code != http.StatusOK || len(page.Data) != 2 || !page.HasMore || page.Data[0].OrderID != "o2" {
		t.Fatalf("first page: got %d %+v", rec.Code, page)
	}
	rec = api.do(t, http.MethodGet, "/v1/payments?limit=2&starting_after="+page.Data[1].ID, "Bearer "+api.key, "")
	page = decodeInto[listResponse](t, rec)
	if len(page.Data) != 1 || page.HasMore || page.Data[0].OrderID != "o0" {
		t.Errorf("second page: got %+v", page)
	}

	rec = api.do(t, http.MethodGet, "/v1/payments?status=DECLINED", "Bearer "+api.key, "")
	if page := decodeInto[listResponse](t, rec); page.Data == nil || len(page.Data) != 0 {
		t.Errorf("an empty page: got %s, want an empty list, not null", rec.Body)
	}

	for query, code := range map[string]string{
		"status=approved":  "invalid_status",
		"limit=0":          "invalid_limit",
		"limit=ten":        "invalid_limit",
		"starting_after=x": "invalid_cursor",
	} {
		rec := api.do(t, http.MethodGet, "/v1/payments?"+query, "Bearer "+api.key, "")
		if rec.Code != http.StatusBadRequest || decodeInto[httpx.ErrorBody](t, rec).Error.Code != code {
			t.Errorf("%s: got %d, want 400 %s", query, rec.Code, code)
		}
	}
}

func TestAStoreFailureWhileCreatingIs503(t *testing.T) {
	api := newTestAPI(t)
	key := api.key
	api.store.Err = errors.New("connection refused")
	rec := api.do(t, http.MethodPost, "/v1/payments", "Bearer "+key, paymentBody, "Idempotency-Key", "k1")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rec.Code)
	}
}
