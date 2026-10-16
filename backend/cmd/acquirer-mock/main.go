// Command acquirer-mock simulates the external acquirer. It decides by card
// number, so every scenario can be reproduced, and it remembers each answer by
// Idempotency-Key, so resending a payment never authorizes it twice.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type authorizeRequest struct {
	TransactionID string `json:"transaction_id"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Card          struct {
		Number      string `json:"number"`
		ExpiryMonth int    `json:"expiry_month"`
		ExpiryYear  int    `json:"expiry_year"`
		CVV         string `json:"cvv"`
	} `json:"card"`
}

type authorizeResponse struct {
	TransactionID     string `json:"transaction_id"`
	Approved          bool   `json:"approved"`
	AuthorizationCode string `json:"authorization_code,omitempty"`
	DeclineReason     string `json:"decline_reason,omitempty"`
}

// declines are the test cards the issuer rejects, with its reason.
var declines = map[string]string{
	"4000000000000002": "card_declined",
	"4000000000009995": "insufficient_funds",
}

type acquirer struct {
	mu      sync.Mutex
	answers map[string]authorizeResponse // by Idempotency-Key
}

func newAcquirer() *acquirer {
	return &acquirer{answers: map[string]authorizeResponse{}}
}

func (a *acquirer) authorize(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header is required"})
		return
	}
	var req authorizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid authorization request"})
		return
	}

	a.mu.Lock()
	resp, replayed := a.answers[key]
	if !replayed {
		resp = decide(req)
		a.answers[key] = resp
	}
	a.mu.Unlock()

	slog.Info("authorization",
		"transaction_id", req.TransactionID,
		"card_last4", last4(req.Card.Number),
		"approved", resp.Approved,
		"replayed", replayed,
	)
	writeJSON(w, http.StatusOK, resp)
}

func decide(req authorizeRequest) authorizeResponse {
	if reason, ok := declines[req.Card.Number]; ok {
		return authorizeResponse{TransactionID: req.TransactionID, DeclineReason: reason}
	}
	return authorizeResponse{
		TransactionID:     req.TransactionID,
		Approved:          true,
		AuthorizationCode: rand.Text()[:6],
	}
}

func last4(number string) string {
	if len(number) < 4 {
		return ""
	}
	return number[len(number)-4:]
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8081"
	}

	a := newAcquirer()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /authorizations", a.authorize)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("acquirer mock listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("acquirer mock stopped", "error", err)
		os.Exit(1)
	}
}
