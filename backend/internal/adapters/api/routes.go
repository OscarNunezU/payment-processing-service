package api

import "net/http"

func NewRouter(h *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /payments", h.createPayment)
	mux.HandleFunc("GET /payments/{id}", h.getPayment)
	mux.HandleFunc("GET /payments", h.listPayments)
	return requestID(accessLog(recoverPanic(mux)))
}
