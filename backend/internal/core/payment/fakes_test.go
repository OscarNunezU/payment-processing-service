package payment

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"
)

// fakeRepository keeps transactions in memory, so the core can be tested
// without a database.
type fakeRepository struct {
	stored     []Transaction
	err        error
	lastFilter ListFilter
}

func (f *fakeRepository) Create(_ context.Context, txn Transaction) (Transaction, error) {
	if f.err != nil {
		return Transaction{}, f.err
	}
	txn.ID = fmt.Sprintf("txn-%d", len(f.stored)+1)
	f.stored = append(f.stored, txn)
	return txn, nil
}

func (f *fakeRepository) Get(_ context.Context, id string) (Transaction, error) {
	for _, txn := range f.stored {
		if txn.ID == id {
			return txn, nil
		}
	}
	return Transaction{}, ErrNotFound
}

func (f *fakeRepository) List(_ context.Context, filter ListFilter) ([]Transaction, error) {
	f.lastFilter = filter
	var list []Transaction
	for _, txn := range f.stored {
		if (filter.MerchantID == "" || txn.MerchantID == filter.MerchantID) &&
			(filter.Status == "" || txn.Status == filter.Status) {
			list = append(list, txn)
		}
	}
	return list, nil
}

// ClaimNext takes the first PENDING payment; the lease is not simulated.
func (f *fakeRepository) ClaimNext(context.Context, time.Duration) (Transaction, bool, error) {
	for i := range f.stored {
		if f.stored[i].Status == StatusPending {
			f.stored[i].Status = StatusProcessing
			f.stored[i].Attempts++
			return f.stored[i], true, nil
		}
	}
	return Transaction{}, false, nil
}

func (f *fakeRepository) Finish(_ context.Context, id string, outcome Outcome) error {
	if !outcome.Status.IsFinal() {
		return fmt.Errorf("finish %s: %s is not a final status", id, outcome.Status)
	}
	for i := range f.stored {
		if f.stored[i].ID == id && f.stored[i].Status == StatusProcessing {
			f.stored[i].Status = outcome.Status
			f.stored[i].Reason = outcome.Reason
			f.stored[i].AuthorizationCode = outcome.AuthorizationCode
			f.stored[i].CardData = nil
			return nil
		}
	}
	return ErrNotProcessing
}

// fakeVault "seals" by reversing the bytes: enough to tell sealed data from
// plaintext, and easy to undo in a test.
type fakeVault struct {
	err error
}

func (f fakeVault) Seal(plaintext []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	sealed := bytes.Clone(plaintext)
	slices.Reverse(sealed)
	return sealed, nil
}
