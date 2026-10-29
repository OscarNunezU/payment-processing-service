package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app/apptest"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

var (
	uncertain = domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: "timeout"}
	refused   = domain.Answer{Outcome: domain.OutcomeNotSent, ResponseCode: "connection_refused"}
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recovery is the API and the recovery on one in-memory store, with a clock
// that moves only when the test says.
type recovery struct {
	svc      *app.Service
	store    *apptest.Store
	acquirer *apptest.Acquirer
	breaker  *apptest.Breaker
	clock    *clock
	logs     *bytes.Buffer
}

// newRecovery answers the calls with acquirer, or with the acquirer that with
// builds around it, when with is not nil. The jitter is 0.5, so the waits
// after attempts 1, 2 and 3 are 1.5, 3 and 6 s.
func newRecovery(t *testing.T, acquirer *apptest.Acquirer, with func(*apptest.Store, *apptest.Acquirer) app.Acquirer) *recovery {
	t.Helper()
	r := &recovery{
		store: apptest.NewStore(), acquirer: acquirer, breaker: &apptest.Breaker{},
		clock: &clock{t: time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC)}, logs: &bytes.Buffer{},
	}
	var a app.Acquirer = acquirer
	if with != nil {
		a = with(r.store, acquirer)
	}
	r.svc = app.NewService(app.Deps{
		Merchants: r.store, Payments: r.store, Acquirer: a, Gate: &apptest.Gate{}, Breaker: r.breaker,
		NewID:  func() string { return uuid.NewV7().String() },
		Now:    r.clock.Now,
		Random: func() float64 { return 0.5 },
		Log:    slog.New(slog.NewJSONHandler(r.logs, nil)),
	})
	return r
}

// accept makes a payment through the API, and checks that it is left to the
// recovery.
func (r *recovery) accept(t *testing.T) domain.Payment {
	t.Helper()
	p, _, err := r.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || p.Status != domain.PaymentProcessing {
		t.Fatalf("got %+v, %v, want a payment left PROCESSING", p, err)
	}
	return p
}

// recoverAfter moves the clock d forward and runs the recovery once. It
// returns how many payments it took over.
func (r *recovery) recoverAfter(t *testing.T, d time.Duration) int {
	t.Helper()
	r.clock.advance(d)
	n, err := r.svc.Recover(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *recovery) payment(t *testing.T, id string) domain.Payment {
	t.Helper()
	p, err := r.store.Payment(t.Context(), merchant.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Feature: Acquirer failures. Scenario: A temporary error.
func TestARetryWithTheSameKeyFinishesThePayment(t *testing.T) {
	approving := apptest.Approving()
	approving.Script = []domain.Answer{{Outcome: domain.OutcomeUncertain, ResponseCode: "503"}}
	r := newRecovery(t, approving, nil)
	p := r.accept(t)

	if n := r.recoverAfter(t, time.Second); n != 0 {
		t.Fatalf("the recovery took %d payments before the wait ended", n)
	}
	if n := r.recoverAfter(t, time.Second); n != 1 {
		t.Fatalf("the recovery took %d payments after the wait, want 1", n)
	}
	got := r.payment(t, p.ID)
	if got.Status != domain.PaymentApproved || got.Attempts != 2 || got.AuthorizationCode != "7T4N4V" {
		t.Errorf("got %+v, want APPROVED on attempt 2", got)
	}
	for i, c := range approving.Calls() {
		if c.Key != p.ID {
			t.Errorf("call %d had key %s, want the payment id %s", i+1, c.Key, p.ID)
		}
	}
	if outbox := r.store.Outbox(); len(outbox) != 1 || outbox[0].Type != events.PaymentApproved {
		t.Errorf("got %+v, want one payment.approved", outbox)
	}
	history, _ := r.store.History(t.Context(), p.ID)
	if last := history[len(history)-2]; last.Kind != domain.HistoryAcquirerCall || last.Attempt != 2 || last.Outcome != "approved" {
		t.Errorf("got history %+v, want attempt 2 approved", history)
	}
}

// watching checks, at each reversal, that the store already records it.
type watching struct {
	*apptest.Acquirer
	t     *testing.T
	store *apptest.Store
}

func (a watching) Reverse(ctx context.Context, key string) domain.Answer {
	if p, err := a.store.Payment(ctx, merchant.ID, key); err != nil || p.ReversalStartedAt.IsZero() {
		a.t.Errorf("a reversal was sent before it was recorded: %+v, %v", p, err)
	}
	return a.Acquirer.Reverse(ctx, key)
}

// Feature: Acquirer failures. Scenario: No answer at all.
func TestWhenTheAttemptsRunOutThePaymentIsReversed(t *testing.T) {
	never := &apptest.Acquirer{Answer: uncertain}
	r := newRecovery(t, never, func(s *apptest.Store, a *apptest.Acquirer) app.Acquirer { return watching{a, t, s} })
	p := r.accept(t)

	r.recoverAfter(t, 2*time.Second) // attempt 2
	r.recoverAfter(t, 4*time.Second) // attempt 3
	if got := r.payment(t, p.ID); got.Status != domain.PaymentProcessing || got.Attempts != 3 || len(never.Reversals()) != 0 {
		t.Fatalf("got %+v, want 3 attempts and no reversal yet", got)
	}
	r.recoverAfter(t, 7*time.Second) // the reversal

	got := r.payment(t, p.ID)
	if got.Status != domain.PaymentFailed || got.Reason != domain.ReasonAcquirerUnavailable {
		t.Errorf("got %+v, want FAILED acquirer_unavailable", got)
	}
	if calls, reversals := len(never.Calls()), never.Reversals(); calls != 3 || len(reversals) != 1 || reversals[0] != p.ID {
		t.Errorf("got %d calls and reversals %v, want 3 calls and the payment's key reversed", calls, reversals)
	}
	if outbox := r.store.Outbox(); len(outbox) != 1 || outbox[0].Type != events.PaymentFailed || outbox[0].Data.Reason != "acquirer_unavailable" {
		t.Errorf("got %+v, want one payment.failed", outbox)
	}
}

func TestAPaymentThatNeverReachedTheAcquirerFailsWithoutAReversal(t *testing.T) {
	down := &apptest.Acquirer{Answer: refused}
	r := newRecovery(t, down, nil)
	p := r.accept(t)
	r.recoverAfter(t, 2*time.Second)
	r.recoverAfter(t, 4*time.Second)
	r.recoverAfter(t, 7*time.Second)

	got := r.payment(t, p.ID)
	if got.Status != domain.PaymentFailed || got.Reason != domain.ReasonAcquirerUnavailable || !got.ReversalStartedAt.IsZero() {
		t.Errorf("got %+v, want FAILED acquirer_unavailable, never reversed", got)
	}
	if calls, reversals := len(down.Calls()), len(down.Reversals()); calls != 3 || reversals != 0 {
		t.Errorf("got %d calls and %d reversals, want 3 calls and nothing to reverse", calls, reversals)
	}
	if outbox := r.store.Outbox(); len(outbox) != 1 || outbox[0].Type != events.PaymentFailed {
		t.Errorf("got %+v, want one payment.failed", outbox)
	}
}

func TestAReversalIsSentUntilTheAcquirerConfirmsIt(t *testing.T) {
	never := &apptest.Acquirer{Answer: uncertain, Reversal: domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: "503"}}
	r := newRecovery(t, never, nil)
	p := r.accept(t)
	r.recoverAfter(t, 2*time.Second)
	r.recoverAfter(t, 4*time.Second)
	r.recoverAfter(t, 7*time.Second) // the first reversal, refused

	if got := r.payment(t, p.ID); got.Status != domain.PaymentProcessing || !got.LeaseUntil.Equal(r.clock.Now().Add(domain.FirstReversalWait)) {
		t.Fatalf("got %+v, want PROCESSING for 5 s before the next reversal", got)
	}
	if n := r.recoverAfter(t, 4*time.Second); n != 0 {
		t.Fatalf("the reversal was sent again after 4 s")
	}
	r.recoverAfter(t, time.Second) // the second, refused
	never.SetReversal(domain.Answer{Outcome: domain.OutcomeReversed, ResponseCode: "200"})
	r.recoverAfter(t, 5*time.Second) // the third, confirmed

	if got := r.payment(t, p.ID); got.Status != domain.PaymentFailed || got.Reason != domain.ReasonAcquirerUnavailable {
		t.Errorf("got %+v, want FAILED once the reversal was confirmed", got)
	}
	if calls, reversals := len(never.Calls()), len(never.Reversals()); calls != 3 || reversals != 3 {
		t.Errorf("got %d calls and %d reversals, want 3 of each: a reversal is never followed by an authorization", calls, reversals)
	}
	history, _ := r.store.History(t.Context(), p.ID)
	var outcomes []string
	for _, h := range history {
		if h.Kind == domain.HistoryReversal {
			outcomes = append(outcomes, h.Outcome)
		}
	}
	if strings.Join(outcomes, " ") != "uncertain uncertain reversed" {
		t.Errorf("the history has reversals %v, want two refused and one confirmed", outcomes)
	}
}

// Feature: Acquirer failures. The authorization window (FR12).
func TestNoAuthorizationIsSentAfterTheWindow(t *testing.T) {
	never := &apptest.Acquirer{Answer: uncertain}
	r := newRecovery(t, never, nil)
	p := r.accept(t)
	r.recoverAfter(t, domain.AuthorizationWindow)

	if got := r.payment(t, p.ID); got.Status != domain.PaymentFailed || got.Attempts != 1 {
		t.Errorf("got %+v, want FAILED after its single attempt", got)
	}
	if calls, reversals := len(never.Calls()), len(never.Reversals()); calls != 1 || reversals != 1 {
		t.Errorf("got %d calls and %d reversals, want the reversal instead of a late attempt", calls, reversals)
	}
}

func TestTheBreakerHoldsTheAttemptsWithoutSpendingThem(t *testing.T) {
	approving := apptest.Approving()
	approving.Script = []domain.Answer{refused}
	r := newRecovery(t, approving, nil)
	p := r.accept(t)

	r.breaker.SetOpen(true)
	if n := r.recoverAfter(t, 2*time.Second); n != 1 {
		t.Fatalf("the recovery took %d payments, want 1", n)
	}
	if got := r.payment(t, p.ID); got.Attempts != 1 || len(approving.Calls()) != 1 {
		t.Fatalf("got %+v after %d calls, want the attempt held", got, len(approving.Calls()))
	}
	// The payment waits for the lease of its claim.
	if n := r.recoverAfter(t, domain.Lease-time.Second); n != 0 {
		t.Fatalf("a held payment was claimed again before its lease ended")
	}
	r.breaker.SetOpen(false)
	r.recoverAfter(t, time.Second)
	if got := r.payment(t, p.ID); got.Status != domain.PaymentApproved || got.Attempts != 2 {
		t.Errorf("got %+v, want APPROVED on attempt 2", got)
	}
}

func TestAWorkerThatWasTakenOverWritesNothing(t *testing.T) {
	approving := apptest.Approving()
	approving.Script = []domain.Answer{uncertain}
	r := newRecovery(t, approving, nil)
	p := r.accept(t)
	// From the next call on, another worker claims the payment during it.
	r.svc = app.NewService(app.Deps{
		Payments: r.store, Acquirer: takeOver{approving, r.store}, Breaker: r.breaker,
		NewID: func() string { return uuid.NewV7().String() }, Now: r.clock.Now, Random: func() float64 { return 0.5 },
		Log: slog.New(slog.DiscardHandler),
	})
	r.recoverAfter(t, 2*time.Second)

	got := r.payment(t, p.ID)
	if got.Status != domain.PaymentProcessing || got.Epoch != 3 {
		t.Errorf("got %+v, want PROCESSING, held by the worker that took it over", got)
	}
	if outbox := r.store.Outbox(); len(outbox) != 0 {
		t.Errorf("the worker that was taken over wrote %+v", outbox)
	}
}

// INV04: every accepted payment reaches a final state, or raises an alert.
func TestAPaymentUnresolvedForFiveMinutesRaisesAnAlert(t *testing.T) {
	r := newRecovery(t, &apptest.Acquirer{Answer: uncertain}, nil)
	p := r.accept(t)

	r.clock.advance(domain.AlertAfter - time.Second)
	if err := r.svc.CheckUnresolved(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.logs.String(), "payments_unresolved") {
		t.Fatalf("an alert was raised before 5 minutes: %s", r.logs)
	}
	r.clock.advance(2 * time.Second)
	if err := r.svc.CheckUnresolved(t.Context()); err != nil {
		t.Fatal(err)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, `"alert":"payments_unresolved"`) ||
		!strings.Contains(logs, `"count":1`) || !strings.Contains(logs, p.ID) || !strings.Contains(logs, `"oldest_age":"5m1s"`) {
		t.Errorf("got logs %s, want an error with the alert, the count and the oldest payment", logs)
	}
}
