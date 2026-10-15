package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

type fakePayments struct {
	txn       payment.Transaction
	list      []payment.Transaction
	err       error
	gotFilter *payment.ListFilter // when set, List records the filter it received
}

func (f fakePayments) Create(context.Context, payment.NewPayment) (payment.Transaction, error) {
	return f.txn, f.err
}

func (f fakePayments) Get(context.Context, string) (payment.Transaction, error) {
	return f.txn, f.err
}

func (f fakePayments) List(_ context.Context, filter payment.ListFilter) ([]payment.Transaction, error) {
	if f.gotFilter != nil {
		*f.gotFilter = filter
	}
	return f.list, f.err
}

func get(payments fakePayments, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router := NewRouter(NewHandler(fakePinger{}, payments))
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestGetPayment(t *testing.T) {
	txn := payment.Transaction{ID: "txn-1", MerchantID: "merchant-001", Status: payment.StatusApproved, CardLast4: "1111"}

	rec := get(fakePayments{txn: txn}, "/payments/txn-1")
	var got paymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || got.TransactionID != "txn-1" {
		t.Fatalf("GET /payments/txn-1 = %d %s, want 200 with the transaction", rec.Code, rec.Body)
	}

	rec = get(fakePayments{err: payment.ErrNotFound}, "/payments/missing")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /payments/missing = %d, want 404", rec.Code)
	}
}

func TestListPaymentsPassesTheFilters(t *testing.T) {
	var filter payment.ListFilter
	payments := fakePayments{
		list:      []payment.Transaction{{ID: "txn-2"}, {ID: "txn-1"}},
		gotFilter: &filter,
	}

	rec := get(payments, "/payments?merchant_id=merchant-001&status=APPROVED&limit=10")

	var got listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusOK || err != nil || len(got.Payments) != 2 {
		t.Fatalf("GET /payments = %d %s, want 200 with 2 payments", rec.Code, rec.Body)
	}
	if filter.MerchantID != "merchant-001" || filter.Status != payment.StatusApproved || filter.Limit != 10 {
		t.Fatalf("filter = %+v, want merchant-001, APPROVED, 10", filter)
	}
}

func TestListPaymentsAnswersAnEmptyListAsAnArray(t *testing.T) {
	rec := get(fakePayments{}, "/payments")

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"payments":[]`) {
		t.Fatalf("GET /payments = %d %s, want 200 with \"payments\":[]", rec.Code, rec.Body)
	}
}

func TestListPaymentsRejectsBadQueryParameters(t *testing.T) {
	tests := []struct {
		target, wantCode string
	}{
		{"/payments?status=PAID", "invalid_status"},
		{"/payments?status=approved", "invalid_status"},
		{"/payments?limit=abc", "invalid_limit"},
		{"/payments?limit=0", "invalid_limit"},
	}
	for _, tt := range tests {
		rec := get(fakePayments{}, tt.target)

		var got errorResponse
		json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusBadRequest || got.Code != tt.wantCode {
			t.Errorf("GET %s = %d %q, want 400 %q", tt.target, rec.Code, got.Code, tt.wantCode)
		}
	}
}

const validBody = `{"merchant_id":"merchant-001","amount":15000,"currency":"CLP",
	"card":{"number":"4111111111111111","expiry_month":12,"expiry_year":2030,"cvv":"123"}}`

func postPayment(payments fakePayments, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router := NewRouter(NewHandler(fakePinger{}, payments))
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/payments", strings.NewReader(body)))
	return rec
}

func TestCreatePaymentAnswers202WithTheTransaction(t *testing.T) {
	txn := payment.Transaction{
		ID: "txn-1", MerchantID: "merchant-001", Amount: 15000, Currency: "CLP",
		Status: payment.StatusPending, CardLast4: "1111",
	}

	rec := postPayment(fakePayments{txn: txn}, validBody)

	if rec.Code != http.StatusAccepted || rec.Header().Get("Location") != "/payments/txn-1" {
		t.Fatalf("status = %d, Location = %q; want 202 and /payments/txn-1", rec.Code, rec.Header().Get("Location"))
	}
	var got paymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil ||
		got.TransactionID != "txn-1" || got.Status != "PENDING" || got.CardLast4 != "1111" {
		t.Fatalf("body = %s, want the pending transaction", rec.Body)
	}
}

func TestCreatePaymentErrors(t *testing.T) {
	invalid := &payment.ValidationError{Fields: []payment.FieldError{{Field: "card.cvv", Message: "must have 3 or 4 digits"}}}

	tests := []struct {
		name       string
		body       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"body is not json", `{"amount":`, nil, http.StatusBadRequest, "invalid_json"},
		{"unknown field", `{"ammount":15000}`, nil, http.StatusBadRequest, "invalid_json"},
		{"garbage after the json", validBody + ` xyz`, nil, http.StatusBadRequest, "invalid_json"},
		{"two json values", validBody + `{}`, nil, http.StatusBadRequest, "invalid_json"},
		{"body over 1 MB", `{"merchant_id":"` + strings.Repeat("a", 2<<20) + `"}`, nil,
			http.StatusRequestEntityTooLarge, "body_too_large"},
		{"malformed fields", validBody, invalid, http.StatusBadRequest, "invalid_request"},
		{"business rule broken", validBody, fmt.Errorf("%w: 9000000 > 5000000 CLP", payment.ErrAmountExceedsMax),
			http.StatusUnprocessableEntity, "amount_exceeds_max"},
		{"unexpected error", validBody, errors.New("connection reset by peer"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postPayment(fakePayments{err: tt.err}, tt.body)

			var got errorResponse
			json.Unmarshal(rec.Body.Bytes(), &got)
			if rec.Code != tt.wantStatus || got.Code != tt.wantCode {
				t.Fatalf("got %d %q, want %d %q (body %s)", rec.Code, got.Code, tt.wantStatus, tt.wantCode, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "connection reset") {
				t.Fatal("the response leaks an internal error")
			}
		})
	}
}

func TestCreatePaymentListsTheInvalidFields(t *testing.T) {
	invalid := &payment.ValidationError{Fields: []payment.FieldError{{Field: "card.cvv", Message: "must have 3 or 4 digits"}}}

	rec := postPayment(fakePayments{err: invalid}, validBody)

	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Fields) != 1 || got.Fields[0].Field != "card.cvv" {
		t.Fatalf("body = %s, want the invalid field listed", rec.Body)
	}
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
	}{
		{"database reachable", nil, http.StatusOK},
		{"database down", errors.New("connection refused"), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter(NewHandler(fakePinger{err: tt.pingErr}, fakePayments{}))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	router := NewRouter(NewHandler(fakePinger{}, fakePayments{}))

	t.Run("generates one when missing", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		if rec.Header().Get(requestIDHeader) == "" {
			t.Error("expected a generated request id")
		}
	})

	t.Run("reuses the caller's id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set(requestIDHeader, "merchant-abc-123")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if got := rec.Header().Get(requestIDHeader); got != "merchant-abc-123" {
			t.Errorf("request id = %q, want %q", got, "merchant-abc-123")
		}
	})
}

func TestRecoverPanic(t *testing.T) {
	h := recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
