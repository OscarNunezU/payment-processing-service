// Package acceptance runs the PRD's acceptance scenarios (PRD, section 12)
// through the real HTTP API, the use cases, Postgres, and the acquirer
// simulator over HTTP, all in the test process.
package acceptance

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/OscarNunezU/payment-processing-service/internal/acquirersim"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/acquirer"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/httpapi"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

const (
	keyUno        = "sk_test_scenario_tienda_uno"
	keyDos        = "sk_test_scenario_tienda_dos"
	keySuspendida = "sk_test_scenario_suspendida"

	simSecret      = "sk_sim_test"
	simPublishable = "pk_sim_test"

	// The test cards of RFC 0001, section 10.
	approves          = "4111111111111111"
	declines          = "4000000000000002"
	unavailableOnce   = "4000000000000127"
	answersLate       = "4000000000000135"
	rejectsTheRequest = "4000000000000143"
)

type service struct {
	url    string
	db     *pgxpool.Pool
	public *acquirersim.Switch
	admin  string
}

// start runs the API on a fresh database with three merchants (two active
// and one suspended), against an acquirer simulator. The client gives up on
// the acquirer after 500 ms, and the simulator's late answers take 1.5 s.
func start(t *testing.T) *service {
	t.Helper()
	url := pgtest.Start(t)
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	fsys, err := migrations.For("payments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(t.Context(), sqlDB, fsys); err != nil {
		t.Fatal(err)
	}
	db, err := platformpg.Open(t.Context(), url, "scenarios", 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	store := postgres.New(db)
	limits := map[domain.Currency]int64{domain.CLP: 5_000_000}
	for _, m := range []struct {
		merchant domain.Merchant
		key      string
	}{
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000001", LegalName: "Tienda Uno SpA", RUT: "76543210-3", Status: domain.MerchantActive, MaxAmount: limits}, keyUno},
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000002", LegalName: "Tienda Dos Limitada", RUT: "77123456-9", Status: domain.MerchantActive, MaxAmount: limits}, keyDos},
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000003", LegalName: "Comercio Suspendido SpA", RUT: "76987654-5", Status: domain.MerchantSuspended, MaxAmount: limits}, keySuspendida},
	} {
		if err := store.CreateMerchant(t.Context(), m.merchant, m.key); err != nil {
			t.Fatal(err)
		}
	}

	quiet := slog.New(slog.DiscardHandler)
	sim := acquirersim.New(acquirersim.Config{
		SecretKey: simSecret, PublishableKey: simPublishable, LateAnswer: 1500 * time.Millisecond, TokenTTL: time.Hour,
	}, quiet)
	public := acquirersim.NewSwitch("127.0.0.1:0", sim.PublicHandler(), quiet)
	sim.Attach(public)
	if err := public.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = public.Down() })
	admin := httptest.NewServer(sim.AdminHandler())
	t.Cleanup(admin.Close)

	breaker := acquirer.NewBreaker(5, 30*time.Second, time.Now)
	svc := app.NewService(app.Deps{
		Merchants: store,
		Payments:  store,
		Acquirer:  acquirer.NewClient("http://"+public.Addr(), simSecret, 500*time.Millisecond, breaker),
		Gate:      acquirer.NewGate(breaker, 512),
		NewID:     func() string { return uuid.NewV7().String() },
		Now:       time.Now,
		Random:    rand.Float64,
		Log:       quiet,
	})
	mux := http.NewServeMux()
	httpapi.New(svc, quiet).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &service{url: srv.URL, db: db, public: public, admin: admin.URL}
}

// answer is what a test needs from a response.
type answer struct {
	httpStatus int
	replayed   bool
	ID         string               `json:"id"`
	Status     domain.PaymentStatus `json:"status"`
	Reason     string               `json:"reason"`
	AuthCode   string               `json:"authorization_code"`
	Decline    string               `json:"decline_code"`
	Card       *struct {
		Last4 string `json:"last4"`
	} `json:"card"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// send does req and decodes its JSON answer into out, when out is not nil. It
// returns the status and the headers of the answer, whose body is closed.
func (s *service) send(t *testing.T, req *http.Request, out any) (int, http.Header) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode the answer to %s %s: %v", req.Method, req.URL, err)
		}
	}
	return resp.StatusCode, resp.Header
}

func (s *service) post(t *testing.T, url, auth string, headers map[string]string, body any, out any) (int, http.Header) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return s.send(t, req, out)
}

// tokenize does what the checkout does: it sends the card to the acquirer
// and gets a token back.
func (s *service) tokenize(t *testing.T, number string) string {
	t.Helper()
	var tok struct {
		Token string `json:"token"`
	}
	s.post(t, "http://"+s.public.Addr()+"/v1/tokens", simPublishable, nil,
		map[string]any{"number": number, "exp_month": 12, "exp_year": 2035, "cvc": "123"}, &tok)
	if tok.Token == "" {
		t.Fatalf("no token for %s", number)
	}
	return tok.Token
}

func (s *service) pay(t *testing.T, apiKey, idempotencyKey, order, token string, amount int64) answer {
	t.Helper()
	var a answer
	status, header := s.post(t, s.url+"/v1/payments", apiKey, map[string]string{"Idempotency-Key": idempotencyKey},
		map[string]any{"order_id": order, "amount": amount, "currency": "CLP", "card_token": token}, &a)
	a.httpStatus, a.replayed = status, header.Get("Idempotent-Replayed") == "true"
	return a
}

func (s *service) get(t *testing.T, apiKey, id string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+"/v1/payments/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	var a answer
	a.httpStatus, _ = s.send(t, req, &a)
	return a
}

func (s *service) setFaults(t *testing.T, faults map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(faults)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, s.admin+"/admin/faults", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := s.send(t, req, nil); status != http.StatusOK {
		t.Fatalf("set the faults: %d", status)
	}
}

func (s *service) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Feature: The result in the response. Scenario: An approval.
func TestAnApproval(t *testing.T) {
	s := start(t)
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, approves), 15000)
	if a.httpStatus != http.StatusCreated || a.Status != domain.PaymentApproved || a.AuthCode == "" || a.Card == nil || a.Card.Last4 != "1111" {
		t.Errorf("got %+v, want 201 APPROVED with its code and card", a)
	}
	if n := s.count(t, `SELECT count(*) FROM outbox WHERE headers ->> 'event-type' = 'payment.approved'`); n != 1 {
		t.Errorf("got %d payment.approved events, want 1", n)
	}
}

// Feature: The result in the response. Scenario: A decline.
func TestADecline(t *testing.T) {
	s := start(t)
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, declines), 15000)
	if a.httpStatus != http.StatusCreated || a.Status != domain.PaymentDeclined || a.Decline != "card_declined" {
		t.Errorf("got %+v, want 201 DECLINED card_declined", a)
	}
}

// Feature: Acquirer failures. Scenario: A rejected request.
func TestARejectedRequest(t *testing.T) {
	s := start(t)
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, rejectsTheRequest), 15000)
	if a.httpStatus != http.StatusCreated || a.Status != domain.PaymentFailed || a.Reason != "acquirer_rejected" {
		t.Errorf("got %+v, want 201 FAILED acquirer_rejected", a)
	}
}

// Feature: Acquirer failures. Scenario: A temporary error. The recovery that
// finishes the payment arrives in step 6 of the plan.
func TestATemporaryErrorAnswersProcessing(t *testing.T) {
	s := start(t)
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, unavailableOnce), 15000)
	if a.httpStatus != http.StatusAccepted || a.Status != domain.PaymentProcessing {
		t.Errorf("got %+v, want 202 PROCESSING", a)
	}
}

// Feature: Acquirer failures. Scenario: Timeout with a late approval. The
// acquirer approves after the client gave up: the payment answers 202, and
// the approval waits at the acquirer for the recovery's retry.
func TestATimeoutAnswersProcessing(t *testing.T) {
	s := start(t)
	start := time.Now()
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, answersLate), 15000)
	if a.httpStatus != http.StatusAccepted || a.Status != domain.PaymentProcessing {
		t.Errorf("got %+v, want 202 PROCESSING", a)
	}
	if took := time.Since(start); took > 1200*time.Millisecond {
		t.Errorf("the request took %s, want it bounded by the client's timeout", took)
	}
}

// Feature: Acquirer failures. Scenario: The acquirer is down.
func TestTheAcquirerIsDown(t *testing.T) {
	s := start(t)
	token := s.tokenize(t, approves)
	before := s.pay(t, keyUno, "k-before", "o-before", token, 15000)
	if before.httpStatus != http.StatusCreated {
		t.Fatalf("before the outage: got %+v", before)
	}
	s.setFaults(t, map[string]any{"down": true})

	// The first payments discover the outage: they are created and answer 202.
	for i := range 5 {
		a := s.pay(t, keyUno, "k"+string(rune('a'+i)), "o"+string(rune('a'+i)), "tok_any", 15000)
		if a.httpStatus != http.StatusAccepted {
			t.Fatalf("payment %d before the breaker opens: got %+v, want 202", i, a)
		}
	}
	created := s.count(t, `SELECT count(*) FROM payments`)

	// From then on, new payments are refused at once, and nothing is created.
	a := s.pay(t, keyUno, "k-new", "o-new", "tok_any", 15000)
	if a.httpStatus != http.StatusServiceUnavailable || a.Error.Code != "acquirer_unavailable" {
		t.Errorf("a new payment with the breaker open: got %+v, want 503 acquirer_unavailable", a)
	}
	if n := s.count(t, `SELECT count(*) FROM payments`); n != created {
		t.Errorf("the refused payment was created: %d payments, want %d", n, created)
	}
	// A retry of a payment created before still gets it.
	if again := s.pay(t, keyUno, "k-before", "o-before", token, 15000); again.httpStatus != http.StatusCreated || !again.replayed || again.ID != before.ID {
		t.Errorf("a retry with the breaker open: got %+v, want its payment, replayed", again)
	}
}

// Feature: No duplicate payments. Scenario: Double click with the same key.
func TestDoubleClickWithTheSameKey(t *testing.T) {
	s := start(t)
	token := s.tokenize(t, approves)
	const clicks = 20
	answers := make([]answer, clicks)
	var wg sync.WaitGroup
	for i := range clicks {
		wg.Go(func() { answers[i] = s.pay(t, keyUno, "k1", "o1", token, 15000) })
	}
	wg.Wait()

	for i, a := range answers {
		if (a.httpStatus != http.StatusCreated && a.httpStatus != http.StatusAccepted) || a.ID != answers[0].ID {
			t.Errorf("click %d: got %+v, want the payment of the first", i, a)
		}
	}
	if n := s.count(t, `SELECT count(*) FROM payments`); n != 1 {
		t.Errorf("%d payments exist, want exactly 1", n)
	}
}

// Feature: No duplicate payments. Scenario: The same key with a different amount.
func TestTheSameKeyWithADifferentAmount(t *testing.T) {
	s := start(t)
	token := s.tokenize(t, approves)
	if a := s.pay(t, keyUno, "k1", "o1", token, 10000); a.httpStatus != http.StatusCreated {
		t.Fatalf("got %+v", a)
	}
	if a := s.pay(t, keyUno, "k1", "o1", token, 12000); a.httpStatus != http.StatusUnprocessableEntity || a.Error.Code != "idempotency_key_reused" {
		t.Errorf("got %+v, want 422 idempotency_key_reused", a)
	}
}

// Feature: No duplicate payments. Scenario: The same order while a payment is live.
func TestTheSameOrderWhileAPaymentIsLive(t *testing.T) {
	s := start(t)
	if a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, approves), 15000); a.Status != domain.PaymentApproved {
		t.Fatalf("got %+v", a)
	}
	if a := s.pay(t, keyUno, "k2", "o1", s.tokenize(t, approves), 15000); a.httpStatus != http.StatusConflict || a.Error.Code != "order_has_active_payment" {
		t.Errorf("got %+v, want 409 order_has_active_payment", a)
	}
}

// Feature: No duplicate payments. Scenario: A new attempt after a decline.
func TestANewAttemptAfterADecline(t *testing.T) {
	s := start(t)
	first := s.pay(t, keyUno, "k1", "o2", s.tokenize(t, declines), 15000)
	if first.Status != domain.PaymentDeclined {
		t.Fatalf("got %+v", first)
	}
	if a := s.pay(t, keyUno, "k2", "o2", s.tokenize(t, approves), 15000); a.httpStatus != http.StatusCreated || a.Status != domain.PaymentApproved || a.ID == first.ID {
		t.Errorf("got %+v, want a new payment for the order, approved", a)
	}
}

// Feature: Merchants. Scenario: A suspended merchant.
func TestASuspendedMerchant(t *testing.T) {
	s := start(t)
	if a := s.pay(t, keySuspendida, "k1", "o1", s.tokenize(t, approves), 15000); a.httpStatus != http.StatusUnprocessableEntity || a.Error.Code != "merchant_suspended" {
		t.Errorf("got %+v, want 422 merchant_suspended", a)
	}
}

// Feature: Merchants. Scenario: Another merchant's payment.
func TestAnotherMerchantsPayment(t *testing.T) {
	s := start(t)
	p := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, approves), 15000)
	if a := s.get(t, keyUno, p.ID); a.httpStatus != http.StatusOK {
		t.Fatalf("its merchant: got %+v", a)
	}
	if a := s.get(t, keyDos, p.ID); a.httpStatus != http.StatusNotFound {
		t.Errorf("another merchant: got %+v, want 404", a)
	}
}
