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
	err        error // returned by Create
	finishErr  error // returned by Finish
	findErr    error // returned by FindByIdempotencyKey
	lastFilter ListFilter
}

func (f *fakeRepository) Create(_ context.Context, txn Transaction) (Transaction, bool, error) {
	if f.err != nil {
		return Transaction{}, false, f.err
	}
	for _, existing := range f.stored {
		if existing.MerchantID == txn.MerchantID && existing.IdempotencyKey == txn.IdempotencyKey {
			return existing, false, nil
		}
	}
	txn.ID = fmt.Sprintf("txn-%d", len(f.stored)+1)
	f.stored = append(f.stored, txn)
	return txn, true, nil
}

func (f *fakeRepository) FindByIdempotencyKey(_ context.Context, merchantID, key string) (Transaction, bool, error) {
	if f.findErr != nil {
		return Transaction{}, false, f.findErr
	}
	for _, txn := range f.stored {
		if txn.MerchantID == merchantID && txn.IdempotencyKey == key {
			return txn, true, nil
		}
	}
	return Transaction{}, false, nil
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
	if f.finishErr != nil {
		return f.finishErr
	}
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
// plaintext, and easy to undo.
type fakeVault struct {
	err     error // returned by Seal
	openErr error // returned by Open
}

func (f fakeVault) Seal(plaintext []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	sealed := bytes.Clone(plaintext)
	slices.Reverse(sealed)
	return sealed, nil
}

func (f fakeVault) Open(sealed []byte) ([]byte, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	plain := bytes.Clone(sealed)
	slices.Reverse(plain)
	return plain, nil
}

// fakeAcquirer answers with a fixed result and records what it was asked.
type fakeAcquirer struct {
	result AuthorizationResult
	err    error
	calls  []AuthorizationRequest
}

func (f *fakeAcquirer) Authorize(_ context.Context, req AuthorizationRequest) (AuthorizationResult, error) {
	f.calls = append(f.calls, req)
	return f.result, f.err
}
