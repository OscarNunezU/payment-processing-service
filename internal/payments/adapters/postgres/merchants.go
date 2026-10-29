// Package postgres is the store of the payment service. Each method is one of
// the transactions of RFC 0001, section 5, not one per table, and every change
// is conditional on the state it expects (ADR 0004).
package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Store reads and writes the payments database.
type Store struct {
	db *pgxpool.Pool
}

// New returns a store on db.
func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// MerchantByAPIKey returns the merchant whose key has hash, if the key is not
// revoked. It is one round trip: the key, the merchant and its limits.
func (s *Store) MerchantByAPIKey(ctx context.Context, hash [sha256.Size]byte) (domain.Merchant, error) {
	var (
		m      domain.Merchant
		limits map[string]int64
	)
	err := s.db.QueryRow(ctx, `
		SELECT m.id, m.legal_name, m.rut, m.status,
		       coalesce(jsonb_object_agg(l.currency, l.max_amount) FILTER (WHERE l.currency IS NOT NULL), '{}')
		FROM api_keys k
		JOIN merchants m ON m.id = k.merchant_id
		LEFT JOIN merchant_limits l ON l.merchant_id = m.id
		WHERE k.hash = $1 AND (k.revoked_at IS NULL OR k.revoked_at > now())
		GROUP BY m.id`,
		hash[:],
	).Scan(&m.ID, &m.LegalName, &m.RUT, &m.Status, &limits)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Merchant{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Merchant{}, fmt.Errorf("read the merchant of an API key: %w", err)
	}
	m.MaxAmount = make(map[domain.Currency]int64, len(limits))
	for c, amount := range limits {
		m.MaxAmount[domain.Currency(c)] = amount
	}
	return m, nil
}

// CreateMerchant inserts m, its limits and its API keys, skipping whatever
// already exists. Running it twice changes nothing, which is what the seed
// data needs.
func (s *Store) CreateMerchant(ctx context.Context, m domain.Merchant, secrets ...string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO merchants (id, legal_name, rut, status) VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO NOTHING`,
			m.ID, m.LegalName, m.RUT, m.Status,
		); err != nil {
			return fmt.Errorf("insert merchant %s: %w", m.ID, err)
		}
		for c, amount := range m.MaxAmount {
			if _, err := tx.Exec(ctx, `
				INSERT INTO merchant_limits (merchant_id, currency, max_amount) VALUES ($1, $2, $3)
				ON CONFLICT (merchant_id, currency) DO NOTHING`,
				m.ID, c, amount,
			); err != nil {
				return fmt.Errorf("insert the %s limit of merchant %s: %w", c, m.ID, err)
			}
		}
		for _, secret := range secrets {
			hash, ok := domain.HashAPIKey(secret)
			if !ok {
				return fmt.Errorf("an API key of merchant %s is not shaped like a key", m.ID)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO api_keys (hash, hint, merchant_id) VALUES ($1, $2, $3)
				ON CONFLICT (hash) DO NOTHING`,
				hash[:], domain.APIKeyHint(secret), m.ID,
			); err != nil {
				return fmt.Errorf("insert an API key of merchant %s: %w", m.ID, err)
			}
		}
		return nil
	})
}
