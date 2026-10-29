package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
)

// maxBodyBytes caps a request body: a payment request is a few hundred bytes.
const maxBodyBytes = 64 << 10

type createPaymentRequest struct {
	OrderID   string `json:"order_id"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	CardToken string `json:"card_token"`
}

// paymentResponse is what a merchant sees of a payment: never its card token
// or the fields only the service uses.
type paymentResponse struct {
	ID                string               `json:"id"`
	OrderID           string               `json:"order_id"`
	Status            domain.PaymentStatus `json:"status"`
	Amount            int64                `json:"amount"`
	Currency          domain.Currency      `json:"currency"`
	Card              *cardResponse        `json:"card,omitempty"`
	Reason            domain.FailureReason `json:"reason,omitempty"`
	AuthorizationCode string               `json:"authorization_code,omitempty"`
	DeclineCode       string               `json:"decline_code,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
	History           []historyResponse    `json:"history,omitempty"`
}

type cardResponse struct {
	Brand string `json:"brand"`
	Last4 string `json:"last4"`
}

type historyResponse struct {
	Kind         domain.HistoryKind   `json:"kind"`
	Status       domain.PaymentStatus `json:"status,omitempty"`
	Attempt      int                  `json:"attempt,omitempty"`
	Outcome      string               `json:"outcome,omitempty"`
	LatencyMS    int                  `json:"latency_ms,omitempty"`
	ResponseCode string               `json:"response_code,omitempty"`
	Reason       string               `json:"reason,omitempty"`
	At           time.Time            `json:"at"`
}

// listResponse wraps the page in an object, with has_more for the next one.
type listResponse struct {
	Data    []paymentResponse `json:"data"`
	HasMore bool              `json:"has_more"`
}

func toPaymentResponse(p domain.Payment) paymentResponse {
	resp := paymentResponse{
		ID:                p.ID,
		OrderID:           p.OrderID,
		Status:            p.Status,
		Amount:            p.Amount,
		Currency:          p.Currency,
		Reason:            p.Reason,
		AuthorizationCode: p.AuthorizationCode,
		DeclineCode:       p.DeclineCode,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
	if p.CardLast4 != "" {
		resp.Card = &cardResponse{Brand: p.CardBrand, Last4: p.CardLast4}
	}
	return resp
}

// createPayment accepts a payment and answers 202: the acquirer is called
// afterwards, in the background (RFC 0001, section 5.1). A replay answers the
// same way, with the payment in its current state.
func (a *API) createPayment(w http.ResponseWriter, r *http.Request, m domain.Merchant) {
	var body createPaymentRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	p, replayed, err := a.svc.Accept(r.Context(), m, domain.PaymentRequest{
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		OrderID:        body.OrderID,
		Amount:         body.Amount,
		Currency:       body.Currency,
		CardToken:      body.CardToken,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}

	trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("payment.id", p.ID))
	msg := "payment accepted"
	if replayed {
		msg = "payment replayed"
		w.Header().Set("Idempotent-Replayed", "true")
	}
	a.log.InfoContext(r.Context(), msg, "payment_id", p.ID, "merchant_id", m.ID, "order_id", p.OrderID, "status", p.Status)
	w.Header().Set("Location", "/v1/payments/"+p.ID)
	httpx.WriteJSON(w, http.StatusAccepted, toPaymentResponse(p))
}

func (a *API) getPayment(w http.ResponseWriter, r *http.Request, m domain.Merchant) {
	p, history, err := a.svc.Payment(r.Context(), m.ID, r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	resp := toPaymentResponse(p)
	resp.History = make([]historyResponse, len(history))
	for i, h := range history {
		resp.History[i] = historyResponse{
			Kind: h.Kind, Status: h.Status, Attempt: h.Attempt, Outcome: h.Outcome,
			LatencyMS: h.LatencyMS, ResponseCode: h.ResponseCode, Reason: h.Reason, At: h.At,
		}
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (a *API) listPayments(w http.ResponseWriter, r *http.Request, m domain.Merchant) {
	q := r.URL.Query()
	f := app.ListFilter{MerchantID: m.ID, OrderID: q.Get("order_id"), StartingAfter: q.Get("starting_after")}
	if s := q.Get("status"); s != "" {
		status, err := domain.ParsePaymentStatus(s)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		f.Status = status
	}
	if s := q.Get("limit"); s != "" {
		limit, err := strconv.Atoi(s)
		if err != nil || limit < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer; the maximum is 100.")
			return
		}
		f.Limit = limit
	}

	page, err := a.svc.List(r.Context(), f)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	resp := listResponse{Data: make([]paymentResponse, len(page.Payments)), HasMore: page.HasMore}
	for i, p := range page.Payments {
		resp.Data[i] = toPaymentResponse(p)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// writeError turns the errors of the use cases into status codes. An error it
// does not know means the store could not answer: the answer is 503, which
// tells the merchant to retry, and a retry with the same key is safe.
func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid *domain.ValidationError
		rule    *domain.RuleError
	)
	switch {
	case errors.As(err, &invalid):
		fields := make([]httpx.FieldError, len(invalid.Fields))
		for i, f := range invalid.Fields {
			fields[i] = httpx.FieldError{Field: wireName(f.Field), Message: f.Message}
		}
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.ErrorBody{Error: httpx.ErrorDetail{
			Code: "invalid_request", Message: "The request has invalid fields.", Fields: fields,
		}})
	case errors.As(err, &rule):
		httpx.WriteError(w, http.StatusUnprocessableEntity, rule.Code, rule.Message)
	case errors.Is(err, domain.ErrOrderHasActivePayment):
		httpx.WriteError(w, http.StatusConflict, "order_has_active_payment",
			"The order already has a payment that is pending, processing or approved.")
	case errors.Is(err, app.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "There is no such payment.")
	case errors.Is(err, domain.ErrInvalidStatus):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_status",
			"status must be PENDING, PROCESSING, APPROVED, DECLINED or FAILED.")
	case errors.Is(err, app.ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_cursor", "starting_after must be the id of a payment.")
	default:
		a.log.ErrorContext(r.Context(), "a request failed", "error", err)
		writeUnavailable(w)
	}
}

// wireName names a field the way the merchant sent it: the idempotency key
// arrives as a header, not in the body.
func wireName(field string) string {
	if field == "idempotency_key" {
		return "Idempotency-Key"
	}
	return field
}

// decodeStrict reads exactly one JSON object from the body. Unknown fields,
// anything after the object and a body over maxBodyBytes are rejected, so a
// typo in a field name never goes unnoticed.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if extra := dec.Decode(&struct{}{}); !errors.Is(extra, io.EOF) {
			err = errors.New("unexpected data after the JSON object")
		}
	}
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "body_too_large", "The body must be at most 64 KiB.")
	default:
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", "The body must be one JSON object with the documented fields.")
	}
	return false
}
