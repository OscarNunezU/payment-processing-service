package app_test

import (
	"errors"
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

func newService(t *testing.T) (*app.Service, *apptest.Store) {
	t.Helper()
	store := apptest.NewStore()
	return app.NewService(store, store, func() string { return uuid.NewV7().String() }, time.Now), store
}

func request(key, order string) domain.PaymentRequest {
	return domain.PaymentRequest{IdempotencyKey: key, OrderID: order, Amount: 15000, Currency: "CLP", CardToken: "tok_1"}
}

func TestAcceptCreatesAPendingPaymentAndItsEvent(t *testing.T) {
	svc, store := newService(t)
	p, replayed, err := svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || replayed {
		t.Fatalf("got %v, replayed=%v", err, replayed)
	}
	if p.Status != domain.PaymentPending || !domain.IsPaymentID(p.ID) || p.MerchantID != merchant.ID {
		t.Errorf("got %+v, want a PENDING payment of the merchant", p)
	}
	outbox := store.Outbox()
	if len(outbox) != 1 || outbox[0].Type != events.PaymentAccepted || outbox[0].PaymentID != p.ID || outbox[0].ID == p.ID {
		t.Errorf("got %+v, want one payment.accepted with its own id", outbox)
	}
}

func TestARetryGetsThePaymentBack(t *testing.T) {
	svc, store := newService(t)
	first, _, err := svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	again, replayed, err := svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil || !replayed || again.ID != first.ID {
		t.Errorf("got %+v, replayed=%v, %v, want the first payment replayed", again, replayed, err)
	}
	if n := len(store.Outbox()); n != 1 {
		t.Errorf("the retry wrote another event: %d in the outbox", n)
	}

	changed := request("k1", "o1")
	changed.Amount++
	if _, _, err := svc.Accept(t.Context(), merchant, changed); !errors.Is(err, domain.ErrRuleIdempotencyKeyReused) {
		t.Errorf("another request with the key: got %v, want ErrRuleIdempotencyKeyReused", err)
	}
}

func TestARejectedRequestDoesNotUseItsKey(t *testing.T) {
	svc, _ := newService(t)
	invalid := request("k1", "o1")
	invalid.Amount = 0
	if _, _, err := svc.Accept(t.Context(), merchant, invalid); err == nil {
		t.Fatal("an invalid request was accepted")
	}
	tooLarge := request("k1", "o1")
	tooLarge.Amount = 5_000_001
	if _, _, err := svc.Accept(t.Context(), merchant, tooLarge); !errors.Is(err, domain.ErrRuleAmountExceedsMax) {
		t.Fatalf("got %v, want ErrRuleAmountExceedsMax", err)
	}
	if _, replayed, err := svc.Accept(t.Context(), merchant, request("k1", "o1")); err != nil || replayed {
		t.Errorf("the corrected request: got %v, replayed=%v, want it created", err, replayed)
	}
}

func TestARetryAfterARuleChangedStillGetsItsPayment(t *testing.T) {
	svc, _ := newService(t)
	first, _, err := svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	suspended := merchant
	suspended.Status = domain.MerchantSuspended
	again, replayed, err := svc.Accept(t.Context(), suspended, request("k1", "o1"))
	if err != nil || !replayed || again.ID != first.ID {
		t.Errorf("got %+v, replayed=%v, %v, want the payment accepted before the suspension", again, replayed, err)
	}
	if _, _, err := svc.Accept(t.Context(), suspended, request("k2", "o2")); !errors.Is(err, domain.ErrRuleMerchantSuspended) {
		t.Errorf("a new payment: got %v, want ErrRuleMerchantSuspended", err)
	}
}

func TestWhenTheKeyCannotBeLookedUpTheRuleIsNotTheAnswer(t *testing.T) {
	svc, store := newService(t)
	store.Err = errors.New("connection refused")
	suspended := merchant
	suspended.Status = domain.MerchantSuspended
	_, _, err := svc.Accept(t.Context(), suspended, request("k1", "o1"))
	var rule *domain.RuleError
	if err == nil || errors.As(err, &rule) {
		t.Errorf("got %v, want the store's error: the payment might exist", err)
	}
}

func TestAnOrderHasOneActivePayment(t *testing.T) {
	svc, store := newService(t)
	first, _, err := svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Accept(t.Context(), merchant, request("k2", "o1")); !errors.Is(err, domain.ErrOrderHasActivePayment) {
		t.Fatalf("got %v, want ErrOrderHasActivePayment", err)
	}
	store.SetStatus(first.ID, domain.PaymentDeclined)
	if _, _, err := svc.Accept(t.Context(), merchant, request("k2", "o1")); err != nil {
		t.Errorf("after a decline: got %v, want a new payment", err)
	}
}

func TestAPaymentIsOnlyVisibleToItsMerchant(t *testing.T) {
	svc, _ := newService(t)
	p, _, err := svc.Accept(t.Context(), merchant, request("k1", "o1"))
	if err != nil {
		t.Fatal(err)
	}
	got, history, err := svc.Payment(t.Context(), merchant.ID, p.ID)
	if err != nil || got.ID != p.ID || len(history) != 1 || history[0].Status != domain.PaymentPending {
		t.Errorf("its merchant: got %+v %+v %v", got, history, err)
	}
	if _, _, err := svc.Payment(t.Context(), "00000000-0000-4000-8000-000000000002", p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("another merchant: got %v, want ErrNotFound", err)
	}
	if _, _, err := svc.Payment(t.Context(), merchant.ID, "not-an-id"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("a malformed id: got %v, want ErrNotFound", err)
	}
}

func TestListPagesNewestFirst(t *testing.T) {
	svc, _ := newService(t)
	var ids []string
	for i := range 5 {
		p, _, err := svc.Accept(t.Context(), merchant, request("k"+string(rune('a'+i)), "o"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}

	page, err := svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, Limit: 2})
	if err != nil || len(page.Payments) != 2 || !page.HasMore || page.Payments[0].ID != ids[4] || page.Payments[1].ID != ids[3] {
		t.Fatalf("first page: got %+v, %v", page, err)
	}
	page, err = svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, Limit: 2, StartingAfter: ids[1]})
	if err != nil || len(page.Payments) != 1 || page.HasMore || page.Payments[0].ID != ids[0] {
		t.Errorf("last page: got %+v, %v", page, err)
	}
	if _, err := svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, StartingAfter: "x"}); !errors.Is(err, app.ErrInvalidCursor) {
		t.Errorf("a bad cursor: got %v, want ErrInvalidCursor", err)
	}
	page, err = svc.List(t.Context(), app.ListFilter{MerchantID: merchant.ID, Limit: 1000})
	if err != nil || len(page.Payments) != 5 {
		t.Errorf("a limit above the maximum: got %d payments, %v", len(page.Payments), err)
	}
}
