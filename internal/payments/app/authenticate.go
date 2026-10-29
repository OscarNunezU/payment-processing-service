// Package app holds the use cases of the payment service and the ports they
// need. It knows the domain, and nothing about HTTP, SQL or Kafka (ADR 0004).
package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

var (
	// ErrUnauthenticated means the request carries no valid API key.
	ErrUnauthenticated = errors.New("no valid API key")
	// ErrNotFound means the store has no such record.
	ErrNotFound = errors.New("not found")
)

// Merchants is the part of the store that knows merchants.
type Merchants interface {
	// MerchantByAPIKey returns the merchant whose active key has hash, or
	// ErrNotFound.
	MerchantByAPIKey(ctx context.Context, hash [sha256.Size]byte) (domain.Merchant, error)
}

// Authenticate returns the merchant that owns the secret API key. A missing,
// malformed, unknown or revoked key is ErrUnauthenticated; any other error
// means the store could not answer.
func Authenticate(ctx context.Context, merchants Merchants, secret string) (domain.Merchant, error) {
	hash, ok := domain.HashAPIKey(secret)
	if !ok {
		return domain.Merchant{}, ErrUnauthenticated
	}
	m, err := merchants.MerchantByAPIKey(ctx, hash)
	if errors.Is(err, ErrNotFound) {
		return domain.Merchant{}, ErrUnauthenticated
	}
	if err != nil {
		return domain.Merchant{}, fmt.Errorf("look up the API key: %w", err)
	}
	return m, nil
}
