package payment

import (
	"bytes"
	"context"
	"fmt"
	"slices"
)

// fakeRepository keeps transactions in memory, so the core can be tested
// without a database.
type fakeRepository struct {
	stored []Transaction
	err    error
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
	var list []Transaction
	for _, txn := range f.stored {
		if (filter.MerchantID == "" || txn.MerchantID == filter.MerchantID) &&
			(filter.Status == "" || txn.Status == filter.Status) {
			list = append(list, txn)
		}
	}
	return list, nil
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
