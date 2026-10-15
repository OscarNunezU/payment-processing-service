package payment

import "context"

// Repository is what the core needs from storage.
type Repository interface {
	Create(ctx context.Context, txn Transaction) (Transaction, error)
	Get(ctx context.Context, id string) (Transaction, error)
	List(ctx context.Context, filter ListFilter) ([]Transaction, error)
}

type ListFilter struct {
	MerchantID string // empty means any merchant
	Status     Status // empty means any status
	Limit      int
}
