package payment

import (
	"context"
	"errors"
	"testing"
)

// waitingPayment stores one PENDING payment with its card sealed, exactly as
// POST /payments leaves it.
func waitingPayment(t *testing.T) *fakeRepository {
	t.Helper()
	repo := &fakeRepository{}
	if _, _, err := newTestService(repo, fakeVault{}).Create(context.Background(), validPayment()); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	return repo
}

func TestProcessNextApprovesAndWipesTheCard(t *testing.T) {
	repo := waitingPayment(t)
	acquirer := &fakeAcquirer{result: AuthorizationResult{Approved: true, AuthorizationCode: "A1B2C3"}}

	txn, processed, err := NewProcessor(repo, fakeVault{}, acquirer).ProcessNext(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v; want a processed payment", processed, err)
	}

	stored := repo.stored[0]
	if stored.Status != StatusApproved || stored.AuthorizationCode != "A1B2C3" || stored.CardData != nil {
		t.Fatalf("stored = %+v, want APPROVED with its code and the card wiped", stored)
	}
	if txn.Status != StatusApproved || txn.CardData != nil {
		t.Fatalf("returned = %+v, want the final state and no card data", txn)
	}

	if len(acquirer.calls) != 1 {
		t.Fatalf("acquirer called %d times, want 1", len(acquirer.calls))
	}
	req := acquirer.calls[0]
	if req.TransactionID != stored.ID || req.Amount != 15000 || req.Card.Number != "4111111111111111" || req.Card.CVV != "123" {
		t.Fatalf("acquirer got %+v, want the opened card and the transaction id as idempotency key", req)
	}
}

func TestProcessNextOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		acquirer   *fakeAcquirer
		vault      fakeVault
		wantStatus Status
		wantReason string
		wantCalls  int
	}{
		{"acquirer declines", &fakeAcquirer{result: AuthorizationResult{DeclineReason: "insufficient_funds"}},
			fakeVault{}, StatusDeclined, "insufficient_funds", 1},
		{"acquirer gives no answer", &fakeAcquirer{err: errors.New("connection refused")},
			fakeVault{}, StatusFailed, ReasonAcquirerError, 1},
		{"card cannot be opened", &fakeAcquirer{},
			fakeVault{openErr: errors.New("wrong key")}, StatusFailed, ReasonCardUnreadable, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := waitingPayment(t)

			_, processed, err := NewProcessor(repo, tt.vault, tt.acquirer).ProcessNext(context.Background())
			if err != nil || !processed {
				t.Fatalf("ProcessNext() = %v, %v; want a processed payment", processed, err)
			}
			stored := repo.stored[0]
			if stored.Status != tt.wantStatus || stored.Reason != tt.wantReason || stored.CardData != nil {
				t.Fatalf("stored = %s %q, want %s %q with the card wiped", stored.Status, stored.Reason, tt.wantStatus, tt.wantReason)
			}
			if len(tt.acquirer.calls) != tt.wantCalls {
				t.Fatalf("acquirer called %d times, want %d", len(tt.acquirer.calls), tt.wantCalls)
			}
		})
	}
}

func TestProcessNextWithNothingWaiting(t *testing.T) {
	acquirer := &fakeAcquirer{}

	_, processed, err := NewProcessor(&fakeRepository{}, fakeVault{}, acquirer).ProcessNext(context.Background())
	if processed || err != nil || len(acquirer.calls) != 0 {
		t.Fatalf("ProcessNext() = %v, %v with %d acquirer calls; want nothing done", processed, err, len(acquirer.calls))
	}
}

// If the acquirer approves but the outcome cannot be stored, the payment stays
// PROCESSING: once the lease expires it is taken again, and the acquirer, being
// idempotent, gives the same answer.
func TestProcessNextReportsAnOutcomeItCouldNotStore(t *testing.T) {
	repo := waitingPayment(t)
	repo.finishErr = errors.New("database down")
	acquirer := &fakeAcquirer{result: AuthorizationResult{Approved: true, AuthorizationCode: "A1B2C3"}}

	_, processed, err := NewProcessor(repo, fakeVault{}, acquirer).ProcessNext(context.Background())
	if !processed || err == nil {
		t.Fatalf("ProcessNext() = %v, %v; want processed with an error", processed, err)
	}
	if repo.stored[0].Status != StatusProcessing || repo.stored[0].CardData == nil {
		t.Fatalf("stored = %+v, want it still PROCESSING with its card, ready to be taken again", repo.stored[0])
	}
}

// When the worker is stopping, its context ends while the acquirer may already
// have approved. Recording FAILED would contradict the acquirer, so nothing is
// recorded: the payment stays PROCESSING and is taken again later.
func TestProcessNextRecordsNothingWhenInterrupted(t *testing.T) {
	repo := waitingPayment(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	acquirer := &fakeAcquirer{err: context.Canceled}

	txn, processed, err := NewProcessor(repo, fakeVault{}, acquirer).ProcessNext(ctx)
	if !processed || !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessNext() = %v, %v; want processed with context.Canceled", processed, err)
	}
	if repo.stored[0].Status != StatusProcessing || repo.stored[0].CardData == nil {
		t.Fatalf("stored = %+v, want it still PROCESSING with its card", repo.stored[0])
	}
	if txn.CardData != nil {
		t.Fatal("the returned transaction carries card data")
	}
}
