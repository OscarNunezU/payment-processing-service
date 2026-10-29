package acquirersim

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/health"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
)

// Config holds the simulator's settings.
type Config struct {
	// SecretKey authenticates payments-worker, and PublishableKey the
	// browser, which may only create tokens.
	SecretKey      string
	PublishableKey string
	// AllowedOrigins are the web pages allowed to create tokens from a browser.
	AllowedOrigins []string
	// LateAnswer is how long a late answer takes. It must be longer than the
	// client's timeout.
	LateAnswer time.Duration
	// TokenTTL is how long a token can be used.
	TokenTTL time.Duration
}

// neverAnswerLimit bounds how long a call that never answers keeps its
// goroutine, in case its client never gives up either.
const neverAnswerLimit = 10 * time.Minute

// Simulator is the simulated acquirer. Its public handler serves the API that
// browsers and payments-worker call; its admin handler serves the book, the
// faults and the health probe, on a port of its own that keeps working while
// the public one is down.
type Simulator struct {
	cfg    Config
	book   *Book
	tokens *tokens
	faults faults
	// switching serializes the changes of faults, so the public port always
	// matches the Down fault that was stored.
	switching sync.Mutex
	log       *slog.Logger
	// public is set by Attach. Without it, as in the handler tests, Down is
	// recorded but no port is closed.
	public *Switch
}

// New returns a simulator with an empty book.
func New(cfg Config, log *slog.Logger) *Simulator {
	t := newTokens(cfg.TokenTTL, time.Now)
	return &Simulator{cfg: cfg, book: newBook(t, time.Now), tokens: t, log: log}
}

// Attach gives the simulator the switch that serves its public handler, so
// that the Down fault turns the public port off and on. It must be called
// before the admin handler serves.
func (s *Simulator) Attach(public *Switch) { s.public = public }

// PublicHandler serves tokens, authorizations and reversals.
func (s *Simulator) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	httpx.Handle(mux, "OPTIONS /v1/tokens", http.HandlerFunc(s.preflight))
	httpx.Handle(mux, "POST /v1/tokens", s.withKey(s.cfg.PublishableKey, http.HandlerFunc(s.createToken)))
	httpx.Handle(mux, "POST /v1/authorizations", s.withKey(s.cfg.SecretKey, http.HandlerFunc(s.authorize)))
	httpx.Handle(mux, "POST /v1/authorizations/{key}/reversal", s.withKey(s.cfg.SecretKey, http.HandlerFunc(s.reverse)))
	return mux
}

// AdminHandler serves the book, the faults and the health probe.
func (s *Simulator) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	httpx.Handle(mux, "GET /healthz", http.HandlerFunc(health.Live))
	httpx.Handle(mux, "GET /admin/book", http.HandlerFunc(s.listBook))
	httpx.Handle(mux, "GET /admin/faults", http.HandlerFunc(s.getFaults))
	httpx.Handle(mux, "PUT /admin/faults", http.HandlerFunc(s.putFaults))
	return mux
}

// withKey lets a request through only when it carries key as its bearer token.
func (s *Simulator) withKey(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.cors(w, r)
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "Send a valid key as a bearer token.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cors lets the allowed pages read the answer of a token request.
func (s *Simulator) cors(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/tokens" {
		return
	}
	if origin := r.Header.Get("Origin"); slices.Contains(s.cfg.AllowedOrigins, origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
}

// preflight answers the browser's CORS check before a token request.
func (s *Simulator) preflight(w http.ResponseWriter, r *http.Request) {
	if !slices.Contains(s.cfg.AllowedOrigins, r.Header.Get("Origin")) {
		httpx.WriteError(w, http.StatusForbidden, "origin_not_allowed", "This page may not create tokens.")
		return
	}
	s.cors(w, r)
	w.Header().Set("Access-Control-Allow-Methods", "POST")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

type tokenRequest struct {
	Number   string `json:"number"`
	ExpMonth int    `json:"exp_month"`
	ExpYear  int    `json:"exp_year"`
	CVC      string `json:"cvc"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	Brand     string    `json:"brand"`
	Last4     string    `json:"last4"`
	ExpMonth  int       `json:"exp_month"`
	ExpYear   int       `json:"exp_year"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Simulator) createToken(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decode(w, r, &req) {
		return
	}
	switch {
	case !validNumber(req.Number):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_number", "The card number is not valid.")
		return
	case req.ExpMonth < 1 || req.ExpMonth > 12 || req.ExpYear < 2000 || req.ExpYear > 2100:
		httpx.WriteError(w, http.StatusBadRequest, "invalid_expiry", "The expiry date is not valid.")
		return
	case expired(req.ExpMonth, req.ExpYear, time.Now()):
		httpx.WriteError(w, http.StatusBadRequest, "expired_card", "The card has expired.")
		return
	case !digits(req.CVC, 3, 4):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_cvc", "The security code must have 3 or 4 digits.")
		return
	}
	c := card{number: req.Number, brand: brandOf(req.Number), expMonth: req.ExpMonth, expYear: req.ExpYear}
	id, expires := s.tokens.create(c)
	httpx.WriteJSON(w, http.StatusCreated, tokenResponse{
		Token: id, Brand: c.brand, Last4: c.last4(), ExpMonth: c.expMonth, ExpYear: c.expYear, ExpiresAt: expires,
	})
}

type authorizeRequest struct {
	Token     string `json:"token"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Reference string `json:"reference"`
}

type authorizationResponse struct {
	Key               string      `json:"key"`
	Status            Outcome     `json:"status"`
	AuthorizationCode string      `json:"authorization_code,omitempty"`
	DeclineCode       string      `json:"decline_code,omitempty"`
	Amount            int64       `json:"amount"`
	Currency          string      `json:"currency"`
	Reference         string      `json:"reference"`
	Card              cardSummary `json:"card"`
}

type cardSummary struct {
	Brand string `json:"brand"`
	Last4 string `json:"last4"`
}

func (s *Simulator) authorize(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if !validKey(key) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_idempotency_key", "Send an Idempotency-Key of 1 to 255 printable characters.")
		return
	}
	var req authorizeRequest
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Token == "":
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "token is required.")
		return
	case req.Amount < 1:
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "amount must be a positive integer.")
		return
	case req.Currency != "CLP" && req.Currency != "USD":
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "currency must be CLP or USD.")
		return
	case len(req.Reference) > 64:
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "reference must have at most 64 characters.")
		return
	}

	f := s.faults.get()
	if hit(f.ErrorRate) {
		s.answerAfter(w, r, f.latency(), func() { writeUnavailable(w) })
		return
	}

	auth, err := s.book.Authorize(AuthorizeRequest{
		Key: key, Token: req.Token, Amount: req.Amount, Currency: req.Currency, Reference: req.Reference,
	})
	if err != nil {
		s.answerAfter(w, r, f.latency(), func() { writeAuthorizeError(w, err) })
		return
	}
	e := auth.entry
	s.log.DebugContext(r.Context(), "authorization", "key", key, "outcome", e.Outcome, "replayed", auth.replayed)

	delay := f.latency()
	switch {
	case e.behavior == neverAnswer:
		delay = neverAnswerLimit
	case e.behavior == answerLate && !auth.replayed, hit(f.TimeoutRate):
		delay += s.cfg.LateAnswer
	}
	s.answerAfter(w, r, delay, func() {
		if auth.replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		httpx.WriteJSON(w, http.StatusOK, authorizationResponse{
			Key:               e.Key,
			Status:            e.Outcome,
			AuthorizationCode: e.AuthorizationCode,
			DeclineCode:       e.DeclineCode,
			Amount:            e.Amount,
			Currency:          e.Currency,
			Reference:         e.Reference,
			Card:              cardSummary{Brand: e.Brand, Last4: e.Last4},
		})
	})
}

func writeAuthorizeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errReversed):
		httpx.WriteError(w, http.StatusConflict, "reversed", "This key was reversed and can never be authorized.")
	case errors.Is(err, errKeyReused):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "idempotency_key_reused", "This key was already used for a different request.")
	case errors.Is(err, errTokenUnknown):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "token_unknown", "The token does not exist.")
	case errors.Is(err, errTokenExpired):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "token_expired", "The token has expired.")
	case errors.Is(err, errTokenUsed):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "token_used", "The token already paid for another authorization.")
	case errors.Is(err, errRejected):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "request_rejected", "The acquirer rejected the request.")
	default:
		writeUnavailable(w)
	}
}

func writeUnavailable(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusServiceUnavailable, "unavailable", "The acquirer is unavailable. Try again later.")
}

type reversalResponse struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	// Outcome is what the key held before the reversal: approved, declined, or
	// none when no authorization had reached it.
	Outcome Outcome `json:"outcome"`
}

func (s *Simulator) reverse(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validKey(key) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_key", "The key must have 1 to 255 printable characters.")
		return
	}
	f := s.faults.get()
	if hit(f.ErrorRate) {
		s.answerAfter(w, r, f.latency(), func() { writeUnavailable(w) })
		return
	}
	e := s.book.Reverse(key)
	s.log.DebugContext(r.Context(), "reversal", "key", key, "outcome", e.Outcome)

	delay := f.latency()
	if hit(f.TimeoutRate) {
		delay += s.cfg.LateAnswer
	}
	s.answerAfter(w, r, delay, func() {
		httpx.WriteJSON(w, http.StatusOK, reversalResponse{Key: e.Key, Status: "reversed", Outcome: e.Outcome})
	})
}

// answerAfter waits delay and then answers, unless the client gave up first:
// then there is no one left to answer.
func (s *Simulator) answerAfter(w http.ResponseWriter, r *http.Request, delay time.Duration, answer func()) {
	if !sleep(r.Context(), delay) {
		return
	}
	answer()
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// bookEntry is an Entry as the admin port shows it.
type bookEntry struct {
	Entry
	Live bool `json:"live"`
}

func (s *Simulator) listBook(w http.ResponseWriter, _ *http.Request) {
	entries := s.book.Entries()
	out := make([]bookEntry, len(entries))
	for i, e := range entries {
		out[i] = bookEntry{Entry: e, Live: e.Live()}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Simulator) getFaults(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.faults.get())
}

func (s *Simulator) putFaults(w http.ResponseWriter, r *http.Request) {
	var f Faults
	if !decode(w, r, &f) {
		return
	}
	if err := f.validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_faults", err.Error())
		return
	}
	s.switching.Lock()
	defer s.switching.Unlock()
	if s.public != nil {
		var err error
		if f.Down {
			err = s.public.Down()
		} else {
			err = s.public.Up(r.Context())
		}
		if err != nil {
			s.log.ErrorContext(r.Context(), "could not switch the public port", "down", f.Down, "error", err)
			httpx.WriteError(w, http.StatusInternalServerError, "switch_failed", "Could not switch the public port.")
			return
		}
	}
	s.faults.set(f)
	s.log.InfoContext(r.Context(), "faults changed", "latency_ms", f.LatencyMS, "error_rate", f.ErrorRate,
		"timeout_rate", f.TimeoutRate, "down", f.Down)
	httpx.WriteJSON(w, http.StatusOK, f)
}

// decode reads a JSON body of at most 64 KiB into v, and answers 400 when it
// cannot.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", "The body must be a JSON object of at most 64 KiB.")
		return false
	}
	return true
}

// validKey accepts 1 to 255 printable ASCII characters.
func validKey(k string) bool {
	if k == "" || len(k) > 255 {
		return false
	}
	for i := range len(k) {
		if k[i] < ' ' || k[i] > '~' {
			return false
		}
	}
	return true
}

func digits(s string, minLen, maxLen int) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
