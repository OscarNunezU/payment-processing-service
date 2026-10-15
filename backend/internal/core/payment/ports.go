package payment

import "context"

// Repository is what the core needs from storage.
type Repository interface {
	Create(ctx context.Context, txn Transaction) (Transaction, error)
	Get(ctx context.Context, id string) (Transaction, error)
	List(ctx context.Context, filter ListFilter) ([]Transaction, error)
}

// CardVault encrypts card details before they are stored.
type CardVault interface {
	Seal(plaintext []byte) ([]byte, error)
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
