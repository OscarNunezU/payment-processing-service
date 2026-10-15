package api

import "net/http"

func NewRouter(h *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /payments", h.createPayment)
	return requestID(accessLog(recoverPanic(mux)))
}
