package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func newTestService(repo *fakeRepository, vault CardVault) *Service {
	s := NewService(repo, vault, testLimits)
	s.now = func() time.Time { return testNow }
	return s
}

func mustCreate(t *testing.T, s *Service, p NewPayment) Transaction {
	t.Helper()
	txn, replayed, err := s.Create(context.Background(), p)
	if err != nil || replayed {
		t.Fatalf("Create() = %v, replayed %v; want a new payment", err, replayed)
	}
	return txn
}

func TestCreateStoresAPendingPaymentWithTheCardSealed(t *testing.T) {
	repo := &fakeRepository{}
	txn := mustCreate(t, newTestService(repo, fakeVault{}), validPayment())

	if txn.ID == "" || txn.Status != StatusPending || txn.CardLast4 != "1111" || len(repo.stored) != 1 {
		t.Fatalf("Create() = %+v, want one PENDING transaction stored", txn)
	}
	if txn.CardData != nil {
		t.Fatal("Create() returned the sealed card: it must stay in storage")
	}

	sealed := repo.stored[0].CardData
	if bytes.Contains(sealed, []byte("4111111111111111")) {
		t.Fatal("the card number was stored in clear")
	}
	opened := bytes.Clone(sealed)
	slices.Reverse(opened) // undo fakeVault
	var secret cardSecret
	if err := json.Unmarshal(opened, &secret); err != nil || secret.Number != "4111111111111111" || secret.CVV != "123" {
		t.Fatalf("sealed card = %s, want the full card details", opened)
	}
}

func TestCreateStoresNothingWhenThePaymentIsRejected(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewPayment)
		check  func(error) bool
	}{
		{"malformed request", func(p *NewPayment) { p.Card.CVV = "" }, func(err error) bool {
			var ve *ValidationError
			return errors.As(err, &ve)
		}},
		{"business rule broken", func(p *NewPayment) { p.Amount = 9_000_000 }, func(err error) bool {
			return errors.Is(err, ErrAmountExceedsMax)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepository{}
			p := validPayment()
			tt.mutate(&p)

			_, _, err := newTestService(repo, fakeVault{}).Create(context.Background(), p)
			if !tt.check(err) {
				t.Fatalf("Create() = %v, want the matching rejection", err)
			}
			if len(repo.stored) != 0 {
				t.Fatalf("a rejected payment was stored: %+v", repo.stored)
			}
		})
	}
}

func TestCreateStoresNothingIfTheCardCannotBeSealed(t *testing.T) {
	repo := &fakeRepository{}
	s := newTestService(repo, fakeVault{err: errors.New("vault down")})

	if _, _, err := s.Create(context.Background(), validPayment()); err == nil {
		t.Fatal("Create() succeeded without sealing the card")
	}
	if len(repo.stored) != 0 {
		t.Fatal("a payment was stored without its card sealed")
	}
}

func TestARetryWithTheSameKeyGetsTheSamePayment(t *testing.T) {
	repo := &fakeRepository{}
	s := newTestService(repo, fakeVault{})
	first := mustCreate(t, s, validPayment())
	repo.stored[0].Status = StatusApproved // the worker finished it in the meantime

	again, replayed, err := s.Create(context.Background(), validPayment())
	if err != nil || !replayed || again.ID != first.ID || len(repo.stored) != 1 {
		t.Fatalf("retry = %s, replayed %v, %v (%d stored); want the same payment, replayed, 1 stored",
			again.ID, replayed, err, len(repo.stored))
	}
	if again.Status != StatusApproved {
		t.Fatalf("retry status = %s, want APPROVED: a retry sees the payment as it is now", again.Status)
	}
	if again.CardData != nil {
		t.Fatal("a replay returned the sealed card")
	}
}

func TestADifferentPaymentWithAUsedKeyIsRejected(t *testing.T) {
	repo := &fakeRepository{}
	s := newTestService(repo, fakeVault{})
	mustCreate(t, s, validPayment())

	other := validPayment()
	other.Amount = 20000
	if _, _, err := s.Create(context.Background(), other); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("Create() = %v, want ErrIdempotencyKeyReused", err)
	}
	if len(repo.stored) != 1 {
		t.Fatalf("%d payments stored, want 1", len(repo.stored))
	}
}

func TestARetryGetsItsPaymentEvenIfTheRulesNowRejectIt(t *testing.T) {
	repo := &fakeRepository{}
	first := mustCreate(t, newTestService(repo, fakeVault{}), validPayment())

	// The limit drops below the payment's amount after it was accepted.
	stricter := NewService(repo, fakeVault{}, Limits{"CLP": 1000})
	stricter.now = func() time.Time { return testNow }

	again, replayed, err := stricter.Create(context.Background(), validPayment())
	if err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("retry after a limit change = %v, replayed %v; want the original payment back", err, replayed)
	}

	other := validPayment()
	other.Amount = 20000
	if _, _, err := stricter.Create(context.Background(), other); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("a different payment with the key = %v, want ErrIdempotencyKeyReused", err)
	}
}

// If the key cannot be checked, the rule is not reported: the payment might
// already exist, and a 422 would tell the merchant it was rejected.
func TestARuleRejectionIsNotReportedIfTheKeyCannotBeChecked(t *testing.T) {
	storageDown := errors.New("database down")
	s := newTestService(&fakeRepository{findErr: storageDown}, fakeVault{})

	tooBig := validPayment()
	tooBig.Amount = 9_000_000
	if _, _, err := s.Create(context.Background(), tooBig); !errors.Is(err, storageDown) {
		t.Fatalf("Create() = %v, want the storage error instead of the rule", err)
	}
}

func TestTheSameKeyFromAnotherMerchantIsAnotherPayment(t *testing.T) {
	repo := &fakeRepository{}
	s := newTestService(repo, fakeVault{})
	first := mustCreate(t, s, validPayment())

	other := validPayment()
	other.MerchantID = "merchant-002"
	second := mustCreate(t, s, other)

	if second.ID == first.ID || len(repo.stored) != 2 {
		t.Fatalf("got ids %s and %s with %d stored, want two different payments", first.ID, second.ID, len(repo.stored))
	}
}

func TestARejectedPaymentDoesNotUseUpItsKey(t *testing.T) {
	repo := &fakeRepository{}
	s := newTestService(repo, fakeVault{})

	tooBig := validPayment()
	tooBig.Amount = 9_000_000
	if _, _, err := s.Create(context.Background(), tooBig); !errors.Is(err, ErrAmountExceedsMax) {
		t.Fatalf("Create() = %v, want ErrAmountExceedsMax", err)
	}

	mustCreate(t, s, validPayment()) // same key, corrected amount
}

func TestGetReturnsTheStoredPaymentOrNotFound(t *testing.T) {
	repo := &fakeRepository{}
	s := newTestService(repo, fakeVault{})
	created := mustCreate(t, s, validPayment())

	if got, err := s.Get(context.Background(), created.ID); err != nil || got.ID != created.ID {
		t.Fatalf("Get(%s) = %+v, %v; want the stored payment", created.ID, got, err)
	}
	if _, err := s.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}
}

func TestEventsReturnsTheHistoryOrNotFound(t *testing.T) {
	repo := &fakeRepository{events: []Event{{Status: StatusPending}, {Status: StatusProcessing, Attempt: 1}}}
	s := newTestService(repo, fakeVault{})
	created := mustCreate(t, s, validPayment())

	if got, err := s.Events(context.Background(), created.ID); err != nil || len(got) != 2 {
		t.Fatalf("Events(%s) = %+v, %v; want its 2 events", created.ID, got, err)
	}
	if _, err := s.Events(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Events(missing) = %v, want ErrNotFound", err)
	}
}

func TestListAppliesTheDefaultAndMaximumLimit(t *testing.T) {
	tests := []struct {
		asked, want int
	}{
		{0, DefaultListLimit},
		{10, 10},
		{500, MaxListLimit},
	}
	for _, tt := range tests {
		repo := &fakeRepository{}
		newTestService(repo, fakeVault{}).List(context.Background(), ListFilter{Limit: tt.asked})

		if repo.lastFilter.Limit != tt.want {
			t.Errorf("List(limit %d) asked the repository for %d, want %d", tt.asked, repo.lastFilter.Limit, tt.want)
		}
	}
}
