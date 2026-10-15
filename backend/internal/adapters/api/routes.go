package api

import "net/http"

func NewRouter(h *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.health)
	return requestID(accessLog(recoverPanic(mux)))
}
