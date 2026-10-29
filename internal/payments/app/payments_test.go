package app_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
	"uuid"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app/apptest"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

var merchant = domain.Merchant{
	ID: "00000000-0000-4000-8000-000000000001", Status: domain.MerchantActive,
	MaxAmount: map[domain.Currency]int64{domain.CLP: 5_000_000},
}

type harness struct {
	svc      *app.Service
	store    *apptest.Store
	acquirer *apptest.Acquirer
	gate     *apptest.Gate
}

// newHarness returns the use cases on an in-memory store and an approving
// acquirer, or on the acquirer that with builds, when it is not nil.
func newHarness(t *testing.T, with func(*apptest.Store) app.Acquirer) *harness {
	t.Helper()
	h := &harness{store: apptest.NewStore(), acquirer: apptest.Approving(), gate: &apptest.Gate{}}
	var acquirer app.Acquirer = h.acquirer
	if with != nil {
		acquirer = with(h.store)
	}
	h.svc = app.NewService(app.Deps{
		Merchants: h.store, Payments: h.store, Acquirer: acquirer, Gate: h.gate,
		NewID:  func() string { return uuid.NewV7().String() },
		Now:    time.Now,
		Random: func() float64 { return 0.5 },
		Log:    slog.New(slog.DiscardHandler),
	})
	return h
}

// answering builds an acquirer that gives every call the answer a.
func answering(a domain.Answer) func(*apptest.Store) app.Acquirer {
	return func(*apptest.Store) app.Acquirer { return &apptest.Acquirer{Answer: a} }
}

func request(key, order string) domain.PaymentRequest {
	return domain.PaymentRequest{IdempotencyKey: key, OrderID: order, Amount: 15000, Currency: "CLP", CardToken: "tok_1"}
}

func TestAPaymentIsAuthorizedInTheRequest(t *testing.T) {
	h := newHarness(t, nil)
	p, replayed, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || replayed {
		t.Fatalf("got %v, replayed=%v", err, replayed)
	}
	if p.Status != domain.PaymentApproved || p.AuthorizationCode != "7T4N4V" || p.CardLast4 != "1111" || !p.LeaseUntil.IsZero() {
		t.Errorf("got %+v, want APPROVED with its code and card", p)
	}
	calls := h.acquirer.Calls()
	if len(calls) != 1 || calls[0].Key != p.ID || calls[0].Token != "tok_1" || calls[0].Amount != 15000 || calls[0].Reference != "o1" {
		t.Errorf("got calls %+v, want one with the payment id as its key", calls)
	}
	outbox := h.store.Outbox()
	if len(outbox) != 1 || outbox[0].Type != events.PaymentApproved || outbox[0].PaymentID != p.ID || outbox[0].Data.Status != "APPROVED" {
		t.Errorf("got %+v, want one payment.approved", outbox)
	}
	if h.gate.InFlight() != 0 {
		t.Errorf("%d payments still hold the gate after the call", h.gate.InFlight())
	}
}

func TestEveryDefiniteAnswerEndsThePaymentWithItsEvent(t *testing.T) {
	tests := []struct {
		answer domain.Answer
		status domain.PaymentStatus
		reason domain.FailureReason
		event  events.Type
	}{
		{domain.Answer{Outcome: domain.OutcomeDeclined, DeclineCode: "card_declined"}, domain.PaymentDeclined, "", events.PaymentDeclined},
		{domain.Answer{Outcome: domain.OutcomeRejected, ResponseCode: "token_unknown"}, domain.PaymentFailed, domain.ReasonAcquirerRejected, events.PaymentFailed},
	}
	for _, tt := range tests {
		h := newHarness(t, answering(tt.answer))
		p, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
		if err != nil || p.Status != tt.status || p.Reason != tt.reason {
			t.Errorf("%s: got %+v, %v", tt.answer.Outcome, p, err)
		}
		if outbox := h.store.Outbox(); len(outbox) != 1 || outbox[0].Type != tt.event {
			t.Errorf("%s: got %+v, want one %s", tt.answer.Outcome, outbox, tt.event)
		}
	}
}

func TestAnUnclearAnswerLeavesThePaymentForTheRecovery(t *testing.T) {
	for _, o := range []domain.Outcome{domain.OutcomeUncertain, domain.OutcomeNotSent} {
		h := newHarness(t, answering(domain.Answer{Outcome: o, ResponseCode: "timeout"}))
		before := time.Now()
		p, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
		if err != nil || p.Status != domain.PaymentProcessing {
			t.Fatalf("%s: got %+v, %v, want PROCESSING", o, p, err)
		}
		// The lease moves to the end of the wait before the next attempt: 1.5 s with this jitter.
		if wait := p.LeaseUntil.Sub(before); wait < time.Second || wait > 2*time.Second {
			t.Errorf("%s: the lease ends in %s, want the 1–2 s wait", o, wait)
		}
		if outbox := h.store.Outbox(); len(outbox) != 0 {
			t.Errorf("%s: got %+v, want no event until the payment is final", o, outbox)
		}
	}
}

func TestARetryGetsThePaymentBackWithoutCallingAgain(t *testing.T) {
	h := newHarness(t, nil)
	first, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	again, replayed, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || !replayed || again.ID != first.ID || again.Status != domain.PaymentApproved {
		t.Errorf("got %+v, replayed=%v, %v, want the first payment replayed", again, replayed, err)
	}
	if n := len(h.acquirer.Calls()); n != 1 {
		t.Errorf("the acquirer got %d calls, want 1", n)
	}

	changed := request("k1", "o1")
	changed.Amount++
	if _, _, err := h.svc.Accept(t.Context(), merchant, changed); !errors.Is(err, domain.ErrRuleIdempotencyKeyReused) {
		t.Errorf("another request with the key: got %v, want ErrRuleIdempotencyKeyReused", err)
	}
}

func TestANewPaymentIsRefusedWhenTheGateIsClosed(t *testing.T) {
	h := newHarness(t, nil)
	h.gate.Closed = true
	if _, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1")); !errors.Is(err, app.ErrAcquirerUnavailable) {
		t.Fatalf("got %v, want ErrAcquirerUnavailable", err)
	}
	if h.store.Count() != 0 || len(h.acquirer.Calls()) != 0 {
		t.Errorf("got %d payments and %d calls, want nothing created", h.store.Count(), len(h.acquirer.Calls()))
	}
	h.gate.Closed = false
	if p, replayed, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1")); err != nil || replayed || p.Status != domain.PaymentApproved {
		t.Errorf("the same key once the gate opens: got %+v, replayed=%v, %v, want a new payment", p, replayed, err)
	}
}

func TestARetryIsNeverGated(t *testing.T) {
	h := newHarness(t, nil)
	first, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	h.gate.Closed = true
	again, replayed, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || !replayed || again.ID != first.ID {
		t.Errorf("got %+v, replayed=%v, %v, want the payment, even with the gate closed", again, replayed, err)
	}
}

// hangUp is an acquirer that checks the call outlives the merchant's request.
type hangUp struct {
	*apptest.Acquirer
	t *testing.T
}

func (a hangUp) Authorize(ctx context.Context, _ app.AuthorizationRequest) domain.Answer {
	if ctx.Err() != nil {
		a.t.Errorf("the call got a canceled context: %v", ctx.Err())
	}
	return domain.Answer{Outcome: domain.OutcomeApproved, AuthorizationCode: "7T4N4V"}
}

func TestTheCallFinishesWhenTheMerchantHangsUp(t *testing.T) {
	h := newHarness(t, func(*apptest.Store) app.Acquirer { return hangUp{apptest.Approving(), t} })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, _, err := h.svc.Accept(ctx, merchant, request("k1", "o1"))
	if err != nil || p.Status != domain.PaymentApproved {
		t.Errorf("got %+v, %v, want the approval recorded", p, err)
	}
}

func TestWhenTheAnswerCannotBeRecordedThePaymentStaysProcessing(t *testing.T) {
	h := newHarness(t, nil)
	h.store.RecordErr = errors.New("connection refused")
	p, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || p.Status != domain.PaymentProcessing {
		t.Errorf("got %+v, %v, want PROCESSING, for the recovery to repeat the call", p, err)
	}
}

// takeOver is an acquirer during whose calls another worker claims the
// payment.
type takeOver struct {
	*apptest.Acquirer
	store *apptest.Store
}

func (a takeOver) Authorize(ctx context.Context, r app.AuthorizationRequest) domain.Answer {
	a.store.TakeOver(r.Key)
	return a.Acquirer.Authorize(ctx, r)
}

func TestAPaymentTakenOverIsAnsweredAsItIs(t *testing.T) {
	h := newHarness(t, func(s *apptest.Store) app.Acquirer { return takeOver{apptest.Approving(), s} })
	p, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || p.Status != domain.PaymentProcessing || p.Epoch != 2 {
		t.Errorf("got %+v, %v, want the payment as the recovery holds it", p, err)
	}
	if outbox := h.store.Outbox(); len(outbox) != 0 {
		t.Errorf("a stale writer wrote %+v", outbox)
	}
}

func TestARejectedRequestDoesNotUseItsKey(t *testing.T) {
	h := newHarness(t, nil)
	invalid := request("k1", "o1")
	invalid.Amount = 0
	if _, _, err := h.svc.Accept(t.Context(), merchant, invalid); err == nil {
		t.Fatal("an invalid request was accepted")
	}
	tooLarge := request("k1", "o1")
	tooLarge.Amount = 5_000_001
	if _, _, err := h.svc.Accept(t.Context(), merchant, tooLarge); !errors.Is(err, domain.ErrRuleAmountExceedsMax) {
		t.Fatalf("got %v, want ErrRuleAmountExceedsMax", err)
	}
	if _, replayed, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1")); err != nil || replayed {
		t.Errorf("the corrected request: got %v, replayed=%v, want it created", err, replayed)
	}
}

func TestARetryAfterARuleChangedStillGetsItsPayment(t *testing.T) {
	h := newHarness(t, nil)
	first, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	suspended := merchant
	suspended.Status = domain.MerchantSuspended
	again, replayed, err := h.svc.Accept(t.Context(), suspended, request("k1", "o1"))
	if err != nil || !replayed || again.ID != first.ID {
		t.Errorf("got %+v, replayed=%v, %v, want the payment accepted before the suspension", again, replayed, err)
	}
	if _, _, err := h.svc.Accept(t.Context(), suspended, request("k2", "o2")); !errors.Is(err, domain.ErrRuleMerchantSuspended) {
		t.Errorf("a new payment: got %v, want ErrRuleMerchantSuspended", err)
	}
}

func TestWhenTheKeyCannotBeLookedUpTheRuleIsNotTheAnswer(t *testing.T) {
	h := newHarness(t, nil)
	h.store.Err = errors.New("connection refused")
	suspended := merchant
	suspended.Status = domain.MerchantSuspended
	_, _, err := h.svc.Accept(t.Context(), suspended, request("k1", "o1"))
	var rule *domain.RuleError
	if err == nil || errors.As(err, &rule) {
		t.Errorf("got %v, want the store's error: the payment might exist", err)
	}
}

func TestAnOrderHasOneActivePayment(t *testing.T) {
	h := newHarness(t, nil)
	first, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Accept(t.Context(), merchant, request("k2", "o1")); !errors.Is(err, domain.ErrOrderHasActivePayment) {
		t.Fatalf("got %v, want ErrOrderHasActivePayment", err)
	}
	h.store.SetStatus(first.ID, domain.PaymentDeclined)
	if _, _, err := h.svc.Accept(t.Context(), merchant, request("k2", "o1")); err != nil {
		t.Errorf("after a decline: got %v, want a new payment", err)
	}
}

func TestAPaymentIsOnlyVisibleToItsMerchant(t *testing.T) {
	h := newHarness(t, nil)
	p, _, err := h.svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	got, history, err := h.svc.Payment(t.Context(), merchant.ID, p.ID)
	if err != nil || got.ID != p.ID || len(history) != 3 || history[0].Status != domain.PaymentProcessing ||
		history[1].Kind != domain.HistoryAcquirerCall || history[1].Outcome != "approved" || history[2].Status != domain.PaymentApproved {
		t.Errorf("its merchant: got %+v %+v %v", got, history, err)
	}
	if _, _, err := h.svc.Payment(t.Context(), "00000000-0000-4000-8000-000000000002", p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("another merchant: got %v, want ErrNotFound", err)
	}
	if _, _, err := h.svc.Payment(t.Context(), merchant.ID, "not-an-id"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("a malformed id: got %v, want ErrNotFound", err)
	}
}

func TestListPagesNewestFirst(t *testing.T) {
	h := newHarness(t, nil)
	var ids []string
	for i := range 5 {
		p, _, err := h.svc.Accept(t.Context(), merchant, request("k"+string(rune('a'+i)), "o"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}

	page, err := h.svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, Limit: 2})
	if err != nil || len(page.Payments) != 2 || !page.HasMore || page.Payments[0].ID != ids[4] || page.Payments[1].ID != ids[3] {
		t.Fatalf("first page: got %+v, %v", page, err)
	}
	page, err = h.svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, Limit: 2, StartingAfter: ids[1]})
	if err != nil || len(page.Payments) != 1 || page.HasMore || page.Payments[0].ID != ids[0] {
		t.Errorf("last page: got %+v, %v", page, err)
	}
	if _, err := h.svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, StartingAfter: "x"}); !errors.Is(err, app.ErrInvalidCursor) {
		t.Errorf("a bad cursor: got %v, want ErrInvalidCursor", err)
	}
	page, err = h.svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, Limit: 1000})
	if err != nil || len(page.Payments) != 5 {
		t.Errorf("a limit above the maximum: got %d payments, %v", len(page.Payments), err)
	}
}
