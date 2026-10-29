package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// merchantsByKey is an in-memory Merchants.
type merchantsByKey struct {
	byHash map[[sha256.Size]byte]domain.Merchant
	err    error
}

func (s merchantsByKey) MerchantByAPIKey(_ context.Context, hash [sha256.Size]byte) (domain.Merchant, error) {
	if s.err != nil {
		return domain.Merchant{}, s.err
	}
	m, ok := s.byHash[hash]
	if !ok {
		return domain.Merchant{}, ErrNotFound
	}
	return m, nil
}

func TestAuthenticate(t *testing.T) {
	secret := domain.NewAPIKey()
	hash, _ := domain.HashAPIKey(secret)
	store := merchantsByKey{byHash: map[[sha256.Size]byte]domain.Merchant{hash: {ID: "m1"}}}

	m, err := Authenticate(t.Context(), store, secret)
	if err != nil || m.ID != "m1" {
		t.Fatalf("got %+v, %v, want merchant m1", m, err)
	}

	for _, bad := range []string{"", "sk_test_short", domain.NewAPIKey()} {
		if _, err := Authenticate(t.Context(), store, bad); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("key %q: got %v, want ErrUnauthenticated", bad, err)
		}
	}
}

func TestAuthenticateWhenTheStoreFails(t *testing.T) {
	down := errors.New("connection refused")
	_, err := Authenticate(t.Context(), merchantsByKey{err: down}, domain.NewAPIKey())
	if !errors.Is(err, down) || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("got %v, want the store's error, not ErrUnauthenticated", err)
	}
}
