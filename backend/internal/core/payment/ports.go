package payment

import (
	"context"
	"time"
)

// Repository is what the core needs from storage.
type Repository interface {
	Create(ctx context.Context, txn Transaction) (Transaction, error)
	Get(ctx context.Context, id string) (Transaction, error)
	List(ctx context.Context, filter ListFilter) ([]Transaction, error)

	// ClaimNext takes the payment that has waited the longest, marks it
	// PROCESSING and keeps it for lease. ok is false when nothing is waiting.
	ClaimNext(ctx context.Context, lease time.Duration) (txn Transaction, ok bool, err error)

	// Finish records the final status of a claimed payment and wipes its card
	// data. It rejects a status that is not final, and returns
	// ErrNotProcessing if the payment is not PROCESSING.
	Finish(ctx context.Context, id string, outcome Outcome) error
}

// CardVault encrypts card details before they are stored, and decrypts them
// when the worker needs them for the acquirer.
type CardVault interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

type ListFilter struct {
	MerchantID string // empty means any merchant
	Status     Status // empty means any status
	Limit      int
}

// Acquirer authorizes a payment. The transaction id travels as the
// idempotency key, so sending the same payment again never authorizes it twice.
type Acquirer interface {
	Authorize(ctx context.Context, req AuthorizationRequest) (AuthorizationResult, error)
}

type AuthorizationRequest struct {
	TransactionID string
	Amount        int64
	Currency      string
	Card          Card
}

// AuthorizationResult is the acquirer's answer. A decline is an answer, not an
// error: errors are kept for when no answer could be obtained.
type AuthorizationResult struct {
	Approved          bool
	AuthorizationCode string
	DeclineReason     string
}
