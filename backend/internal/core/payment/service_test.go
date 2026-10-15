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

func TestCreateStoresAPendingPaymentWithTheCardSealed(t *testing.T) {
	repo := &fakeRepository{}
	s := newTestService(repo, fakeVault{})

	txn, err := s.Create(context.Background(), validPayment())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if txn.ID == "" || txn.Status != StatusPending || txn.CardLast4 != "1111" || len(repo.stored) != 1 {
		t.Fatalf("Create() = %+v, want one PENDING transaction stored", txn)
	}

	if bytes.Contains(txn.CardData, []byte("4111111111111111")) {
		t.Fatal("the card number was stored in clear")
	}
	opened := bytes.Clone(txn.CardData)
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

			_, err := newTestService(repo, fakeVault{}).Create(context.Background(), p)
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

	if _, err := s.Create(context.Background(), validPayment()); err == nil {
		t.Fatal("Create() succeeded without sealing the card")
	}
	if len(repo.stored) != 0 {
		t.Fatal("a payment was stored without its card sealed")
	}
}
