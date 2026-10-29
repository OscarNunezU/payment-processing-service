// Package httpapi is the public API of the payment service (RFC 0001, section
// 11). It turns requests into use cases and their errors into status codes.
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
)

// API serves the merchant-facing endpoints.
type API struct {
	merchants app.Merchants
	log       *slog.Logger
}

// New returns the API on top of the store's merchants.
func New(merchants app.Merchants, log *slog.Logger) *API {
	return &API{merchants: merchants, log: log}
}

// Register adds the API's routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	httpx.Handle(mux, "GET /v1/merchant", a.authenticated(a.getMerchant))
}

// authenticated resolves the merchant of the request's secret key before
// next runs. Without a valid key the answer is 401, and when the store cannot
// answer, 503: the key may well be valid.
func (a *API) authenticated(next func(http.ResponseWriter, *http.Request, domain.Merchant)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := app.Authenticate(r.Context(), a.merchants, bearerToken(r))
		switch {
		case errors.Is(err, app.ErrUnauthenticated):
			w.Header().Set("WWW-Authenticate", `Bearer realm="payments"`)
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "Send your secret API key as a bearer token.")
			return
		case err != nil:
			a.log.ErrorContext(r.Context(), "could not authenticate a request", "error", err)
			writeUnavailable(w)
			return
		}
		trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("merchant.id", m.ID))
		next(w, r, m)
	})
}

// bearerToken returns the token of an "Authorization: Bearer <token>" header,
// or "" when there is none. The scheme is case-insensitive (RFC 9110, section
// 11.1).
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}

// writeUnavailable answers 503 when a dependency the request needs, such as
// Postgres, does not answer.
func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	httpx.WriteError(w, http.StatusServiceUnavailable, "unavailable", "The service is temporarily unavailable. Try again shortly.")
}

type merchantResponse struct {
	ID        string                    `json:"id"`
	LegalName string                    `json:"legal_name"`
	RUT       domain.RUT                `json:"rut"`
	Status    domain.MerchantStatus     `json:"status"`
	MaxAmount map[domain.Currency]int64 `json:"max_amount"`
}

// getMerchant returns the merchant that owns the key, so an integration can
// check which merchant a key belongs to.
func (a *API) getMerchant(w http.ResponseWriter, _ *http.Request, m domain.Merchant) {
	httpx.WriteJSON(w, http.StatusOK, merchantResponse{
		ID: m.ID, LegalName: m.LegalName, RUT: m.RUT, Status: m.Status, MaxAmount: m.MaxAmount,
	})
}
