// Command acquirer-mock simulates the external acquirer. It decides by card
// number, so every scenario can be reproduced, including its failures, and it
// remembers each answer by Idempotency-Key, so resending a payment never
// authorizes it twice.
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

// Test cards for the failures of a real acquirer. Failures are not remembered
// the way answers are, so a retry can still succeed.
const (
	cardUnavailable     = "4000000000000119" // answers 503 every time
	cardUnavailableOnce = "4000000000000127" // answers 503 the first time, then approves
	cardSlowOnce        = "4000000000000135" // approves, but the first answer comes after slowDelay
	cardRefused         = "4000000000000143" // answers 422: the request itself is refused
)

type acquirer struct {
	mu         sync.Mutex
	answers    map[string]authorizeResponse // by Idempotency-Key
	failedOnce map[string]bool              // keys of cardUnavailableOnce already failed
	slowDelay  time.Duration
}

func newAcquirer(slowDelay time.Duration) *acquirer {
	return &acquirer{
		answers:    map[string]authorizeResponse{},
		failedOnce: map[string]bool{},
		slowDelay:  slowDelay,
	}
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

	status, resp, replayed := a.answer(key, req)
	if status == http.StatusOK && !replayed && req.Card.Number == cardSlowOnce {
		// The answer is already stored: the retry that follows the caller's
		// timeout gets it at once, so the payment is approved only once.
		select {
		case <-r.Context().Done():
		case <-time.After(a.slowDelay):
		}
	}

	slog.Info("authorization",
		"transaction_id", req.TransactionID,
		"card_last4", last4(req.Card.Number),
		"status", status,
		"approved", resp.Approved,
		"replayed", replayed,
	)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// answer decides how to respond. An answer is remembered by key, so a payment
// sent again gets the same one; a failure is not.
func (a *acquirer) answer(key string, req authorizeRequest) (status int, resp authorizeResponse, replayed bool) {
	switch req.Card.Number {
	case cardUnavailable:
		return http.StatusServiceUnavailable, authorizeResponse{}, false
	case cardRefused:
		return http.StatusUnprocessableEntity, authorizeResponse{}, false
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if resp, ok := a.answers[key]; ok {
		return http.StatusOK, resp, true
	}
	if req.Card.Number == cardUnavailableOnce && !a.failedOnce[key] {
		a.failedOnce[key] = true
		return http.StatusServiceUnavailable, authorizeResponse{}, false
	}
	resp = decide(req)
	a.answers[key] = resp
	return http.StatusOK, resp, false
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

	// Longer than the service's ACQUIRER_TIMEOUT (5s): the first answer for
	// cardSlowOnce arrives after the service gave up waiting.
	a := newAcquirer(6 * time.Second)
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
