// Package acceptance runs the PRD's acceptance scenarios (PRD, section 12)
// through the real HTTP API, the recovery, the use cases, Postgres, and the
// acquirer simulator over HTTP, all in the test process.
package acceptance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/OscarNunezU/payment-processing-service/internal/acquirersim"
	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/acquirer"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/httpapi"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/recovery"
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
	tiendaUno     = "00000000-0000-4000-8000-000000000001"

	simSecret      = "sk_sim_test"
	simPublishable = "pk_sim_test"

	// The test cards of RFC 0001, section 10.
	approves          = "4111111111111111"
	declines          = "4000000000000002"
	unavailableOnce   = "4000000000000127"
	answersLate       = "4000000000000135"
	rejectsTheRequest = "4000000000000143"
	neverAnswers      = "4000000000000150"
)

type service struct {
	url    string
	db     *pgxpool.Pool
	store  *postgres.Store
	public *acquirersim.Switch
	admin  string
}

// The clients give up on the acquirer after 500 ms, the simulator's late
// answers take 1.5 s, and a breaker that opens waits 2 s before its probe.
// The jitter is 0, so the waits after attempts 1, 2 and 3 are 1, 2 and 4 s.
const (
	clientTimeout   = 500 * time.Millisecond
	breakerCooldown = 2 * time.Second
)

func noJitter() float64 { return 0 }

var quiet = slog.New(slog.DiscardHandler)

// start runs the API and the recovery on a fresh database with three
// merchants (two active and one suspended), against an acquirer simulator.
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
		{domain.Merchant{ID: tiendaUno, LegalName: "Tienda Uno SpA", RUT: "76543210-3", Status: domain.MerchantActive, MaxAmount: limits}, keyUno},
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000002", LegalName: "Tienda Dos Limitada", RUT: "77123456-9", Status: domain.MerchantActive, MaxAmount: limits}, keyDos},
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000003", LegalName: "Comercio Suspendido SpA", RUT: "76987654-5", Status: domain.MerchantSuspended, MaxAmount: limits}, keySuspendida},
	} {
		if err := store.CreateMerchant(t.Context(), m.merchant, m.key); err != nil {
			t.Fatal(err)
		}
	}

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

	s := &service{db: db, store: store, public: public, admin: admin.URL}
	s.url = s.serveAPI(t, store)
	s.runRecovery(t)
	return s
}

// serveAPI serves the API, on payments as its store, and returns its URL.
func (s *service) serveAPI(t *testing.T, payments app.Payments) string {
	t.Helper()
	breaker := acquirer.NewBreaker(5, breakerCooldown, time.Now)
	svc := app.NewService(app.Deps{
		Merchants: s.store,
		Payments:  payments,
		Acquirer:  acquirer.NewClient("http://"+s.public.Addr(), simSecret, clientTimeout, breaker),
		Gate:      acquirer.NewGate(breaker, 512),
		NewID:     func() string { return uuid.NewV7().String() },
		Now:       time.Now,
		Random:    noJitter,
		Log:       quiet,
	})
	mux := http.NewServeMux()
	httpapi.New(svc, quiet).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// runRecovery runs the recovery of payments-worker, with a breaker of its
// own, scanning every 20 ms, until the test ends.
func (s *service) runRecovery(t *testing.T) {
	t.Helper()
	breaker := acquirer.NewBreaker(5, breakerCooldown, time.Now)
	svc := app.NewService(app.Deps{
		Payments: s.store,
		Acquirer: acquirer.NewClient("http://"+s.public.Addr(), simSecret, clientTimeout, breaker),
		Breaker:  breaker,
		NewID:    func() string { return uuid.NewV7().String() },
		Now:      time.Now,
		Random:   noJitter,
		Log:      quiet,
	})
	loop := recovery.New(svc, quiet)
	loop.Interval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loop.Run(ctx)
		close(done)
	}()
	// Registered after the database's cleanup, so it runs before it.
	t.Cleanup(func() {
		cancel()
		<-done
	})
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
	History []struct {
		Kind    string `json:"kind"`
		Attempt int    `json:"attempt"`
		Outcome string `json:"outcome"`
	} `json:"history"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// calls returns the outcomes of the payment's calls of kind, in order.
func (a answer) calls(kind string) []string {
	var outcomes []string
	for _, h := range a.History {
		if h.Kind == kind {
			outcomes = append(outcomes, h.Outcome)
		}
	}
	return outcomes
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

// settle waits until the payment reaches a final state, and returns it.
func (s *service) settle(t *testing.T, apiKey, id string) answer {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		a := s.get(t, apiKey, id)
		if a.Status.Final() {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("payment %s is still %s after 20 s: %+v", id, a.Status, a)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// bookEntry is what the acquirer's book holds for a key.
type bookEntry struct {
	Key      string `json:"key"`
	Outcome  string `json:"outcome"`
	Reversed bool   `json:"reversed"`
	Live     bool   `json:"live"`
}

// book returns the entries of the acquirer's book for key.
func (s *service) book(t *testing.T, key string) []bookEntry {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.admin+"/admin/book", nil)
	if err != nil {
		t.Fatal(err)
	}
	var book struct {
		Data []bookEntry `json:"data"`
	}
	s.send(t, req, &book)
	var entries []bookEntry
	for _, e := range book.Data {
		if e.Key == key {
			entries = append(entries, e)
		}
	}
	return entries
}

// expire ends the lease of a payment now, so the test does not wait the 15 s
// after which the recovery takes over a payment whose request died.
func (s *service) expire(t *testing.T, id string) {
	t.Helper()
	if _, err := s.db.Exec(t.Context(), `UPDATE payments SET lease_until = $2 WHERE id = $1`, id, time.Now()); err != nil {
		t.Fatal(err)
	}
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

// Feature: Acquirer failures. Scenario: A temporary error.
func TestATemporaryError(t *testing.T) {
	s := start(t)
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, unavailableOnce), 15000)
	if a.httpStatus != http.StatusAccepted || a.Status != domain.PaymentProcessing {
		t.Fatalf("got %+v, want 202 PROCESSING", a)
	}
	end := s.settle(t, keyUno, a.ID)
	if end.Status != domain.PaymentApproved || strings.Join(end.calls("acquirer_call"), " ") != "uncertain approved" {
		t.Errorf("got %+v, want APPROVED on the second attempt, in the background", end)
	}
}

// Feature: Acquirer failures. Scenario: Timeout with a late approval. The
// acquirer approves after the client gave up: the payment answers 202, and
// the retry with the same key gets the approval the acquirer stored.
func TestATimeoutWithALateApproval(t *testing.T) {
	s := start(t)
	start := time.Now()
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, answersLate), 15000)
	if a.httpStatus != http.StatusAccepted || a.Status != domain.PaymentProcessing {
		t.Fatalf("got %+v, want 202 PROCESSING", a)
	}
	if took := time.Since(start); took > clientTimeout+700*time.Millisecond {
		t.Errorf("the request took %s, want it bounded by the client's timeout", took)
	}
	if end := s.settle(t, keyUno, a.ID); end.Status != domain.PaymentApproved {
		t.Errorf("got %+v, want APPROVED", end)
	}
	if book := s.book(t, a.ID); len(book) != 1 || !book[0].Live {
		t.Errorf("the acquirer's book has %+v, want exactly one live authorization", book)
	}
}

// Feature: Acquirer failures. Scenario: No answer at all.
func TestNoAnswerAtAll(t *testing.T) {
	s := start(t)
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, neverAnswers), 15000)
	if a.httpStatus != http.StatusAccepted {
		t.Fatalf("got %+v, want 202", a)
	}
	end := s.settle(t, keyUno, a.ID)
	if end.Status != domain.PaymentFailed || end.Reason != "acquirer_unavailable" ||
		len(end.calls("acquirer_call")) != 3 || strings.Join(end.calls("reversal"), " ") != "reversed" {
		t.Errorf("got %+v, want FAILED acquirer_unavailable after 3 attempts and a reversal", end)
	}
	if book := s.book(t, a.ID); len(book) != 1 || !book[0].Reversed || book[0].Live {
		t.Errorf("the acquirer's book has %+v, want the key reversed, with nothing live", book)
	}

	// A request delayed in the network arrives after the reversal.
	var late answer
	status, _ := s.post(t, "http://"+s.public.Addr()+"/v1/authorizations", simSecret, map[string]string{"Idempotency-Key": a.ID},
		map[string]any{"token": s.tokenize(t, approves), "amount": 15000, "currency": "CLP"}, &late)
	if status != http.StatusConflict || late.Error.Code != "reversed" {
		t.Errorf("a late authorization got %d %s, want 409 reversed", status, late.Error.Code)
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
	tokens := make([]string, 5)
	for i := range tokens {
		tokens[i] = s.tokenize(t, approves)
	}
	s.setFaults(t, map[string]any{"down": true})

	// The first payments discover the outage: they are created and answer 202.
	var inDoubt []string
	for i, token := range tokens {
		a := s.pay(t, keyUno, fmt.Sprintf("k%d", i), fmt.Sprintf("o%d", i), token, 15000)
		if a.httpStatus != http.StatusAccepted {
			t.Fatalf("payment %d before the breaker opens: got %+v, want 202", i, a)
		}
		inDoubt = append(inDoubt, a.ID)
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

	// Once the acquirer is back, the breaker's probe lets payments through
	// again, and the payments in doubt are finished in the background.
	s.setFaults(t, map[string]any{"down": false})
	after := s.tokenize(t, approves)
	deadline := time.Now().Add(10 * time.Second)
	for {
		a := s.pay(t, keyUno, "k-after", "o-after", after, 15000)
		if a.httpStatus == http.StatusCreated && a.Status == domain.PaymentApproved {
			break
		}
		if a.httpStatus != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("a payment after the outage: got %+v, want 201 APPROVED once the breaker probes", a)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, id := range inDoubt {
		if end := s.settle(t, keyUno, id); end.Status != domain.PaymentApproved || end.calls("acquirer_call")[0] != "not_sent" {
			t.Errorf("a payment in doubt: got %+v, want APPROVED after its refused first attempt", end)
		}
	}
}

// Feature: Service failures. Scenario: The API dies after sending. The test
// does what the API did up to its death: T1, and the call to the acquirer,
// which approves. The answer is never recorded.
func TestTheAPIDiesAfterSending(t *testing.T) {
	s := start(t)
	r := domain.PaymentRequest{IdempotencyKey: "k1", OrderID: "o1", Amount: 15000, Currency: "CLP", CardToken: s.tokenize(t, approves)}
	p := domain.NewPayment(uuid.NewV7().String(), domain.Merchant{ID: tiendaUno}, r, time.Now().UTC().Truncate(time.Microsecond))
	if _, _, err := s.store.InsertPayment(t.Context(), p, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	client := acquirer.NewClient("http://"+s.public.Addr(), simSecret, clientTimeout, acquirer.NewBreaker(5, breakerCooldown, time.Now))
	sent := client.Authorize(t.Context(), app.AuthorizationRequest{Key: p.ID, Token: p.CardToken, Amount: p.Amount, Currency: p.Currency, Reference: p.OrderID})
	if sent.Outcome != domain.OutcomeApproved {
		t.Fatalf("the call before the crash: got %+v", sent)
	}

	// Meanwhile, the merchant retries and gets the payment in progress.
	if again := s.pay(t, keyUno, "k1", "o1", r.CardToken, 15000); again.httpStatus != http.StatusAccepted || again.ID != p.ID {
		t.Errorf("a retry before the recovery: got %+v, want 202 with the payment", again)
	}
	s.expire(t, p.ID)
	end := s.settle(t, keyUno, p.ID)
	if end.Status != domain.PaymentApproved || end.AuthCode != sent.AuthorizationCode {
		t.Errorf("got %+v, want APPROVED with the code of the first call %s", end, sent.AuthorizationCode)
	}
	if book := s.book(t, p.ID); len(book) != 1 || !book[0].Live {
		t.Errorf("the acquirer's book has %+v, want exactly one authorization", book)
	}
}

// failingT2 is a database that fails right after the acquirer answered.
type failingT2 struct{ *postgres.Store }

func (failingT2) RecordAnswer(context.Context, domain.Payment, domain.Answer, *events.PaymentEvent) error {
	return errors.New("the database went away")
}

// Feature: Service failures. Scenario: The database fails after the approval.
func TestTheDatabaseFailsAfterTheApproval(t *testing.T) {
	s := start(t)
	s.url = s.serveAPI(t, failingT2{s.store})
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, approves), 15000)
	if a.httpStatus != http.StatusAccepted || a.Status != domain.PaymentProcessing {
		t.Fatalf("got %+v, want 202 PROCESSING", a)
	}
	s.expire(t, a.ID)
	end := s.settle(t, keyUno, a.ID)
	if end.Status != domain.PaymentApproved || len(end.calls("acquirer_call")) != 2 {
		t.Errorf("got %+v, want APPROVED by the recovery's repeated call", end)
	}
	if book := s.book(t, a.ID); len(book) != 1 || !book[0].Live {
		t.Errorf("the acquirer's book has %+v, want exactly one authorization", book)
	}
	if n := s.count(t, `SELECT count(*) FROM outbox WHERE headers ->> 'event-type' = 'payment.approved'`); n != 1 {
		t.Errorf("got %d payment.approved events, want 1", n)
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
