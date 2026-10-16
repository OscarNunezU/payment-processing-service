package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/logging"
)

// Pinger lets the health check be tested without a real database.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Payments is what the HTTP layer needs from the core.
type Payments interface {
	Create(ctx context.Context, p payment.NewPayment) (txn payment.Transaction, replayed bool, err error)
	Get(ctx context.Context, id string) (payment.Transaction, error)
	List(ctx context.Context, filter payment.ListFilter) ([]payment.Transaction, error)
}

type Handler struct {
	db       Pinger
	payments Payments
}

func NewHandler(db Pinger, payments Payments) *Handler {
	return &Handler{db: db, payments: payments}
}

// maxBodyBytes caps the request body: a payment request is a few hundred bytes.
const maxBodyBytes = 1 << 20

const idempotencyKeyHeader = "Idempotency-Key"

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		logging.FromContext(ctx).Error("health check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database is not reachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

var errTrailingData = errors.New("unexpected data after the JSON body")

// decodeStrict reads exactly one JSON value from the body. Unknown fields,
// anything after the value and a body over maxBodyBytes are all rejected.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	switch err := dec.Decode(&struct{}{}); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return err
	default:
		return errTrailingData
	}
}

func (h *Handler) createPayment(w http.ResponseWriter, r *http.Request) {
	var req createPaymentRequest
	if err := decodeStrict(w, r, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "the body must be at most 1 MB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "the body must be a single JSON payment request")
		return
	}

	newPayment := req.toNewPayment()
	newPayment.IdempotencyKey = r.Header.Get(idempotencyKeyHeader)

	txn, replayed, err := h.payments.Create(r.Context(), newPayment)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	msg := "payment accepted"
	if replayed {
		msg = "payment replayed"
		w.Header().Set("Idempotent-Replayed", "true")
	}
	logging.FromContext(r.Context()).Info(msg,
		"transaction_id", txn.ID,
		"merchant_id", txn.MerchantID,
		"status", txn.Status,
	)
	w.Header().Set("Location", "/payments/"+txn.ID)
	writeJSON(w, http.StatusAccepted, toPaymentResponse(txn))
}

func (h *Handler) getPayment(w http.ResponseWriter, r *http.Request) {
	txn, err := h.payments.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPaymentResponse(txn))
}

func (h *Handler) listPayments(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := payment.ListFilter{MerchantID: query.Get("merchant_id")}

	if s := query.Get("status"); s != "" {
		status, err := payment.ParseStatus(s)
		if err != nil {
			writeDomainError(w, r, err)
			return
		}
		filter.Status = status
	}
	if s := query.Get("limit"); s != "" {
		limit, err := strconv.Atoi(s)
		if err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		filter.Limit = limit
	}

	txns, err := h.payments.List(r.Context(), filter)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toListResponse(txns))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
