// Package postgres is the store of the webhooks service, on its own database
// (RFC 0001, section 7).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

// Store implements app.Store.
type Store struct {
	db *pgxpool.Pool
}

// New returns a store on db.
func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

// CreateEndpoint stores e for its merchant. A merchant that already has an
// endpoint keeps it, so running the seed again changes nothing.
func (s *Store) CreateEndpoint(ctx context.Context, e domain.Endpoint, now time.Time) error {
	if _, err := s.db.Exec(ctx, `
		INSERT INTO endpoints (merchant_id, url, secret, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (merchant_id) DO NOTHING`,
		e.MerchantID, e.URL, e.Secret, now,
	); err != nil {
		return fmt.Errorf("create the endpoint of merchant %s: %w", e.MerchantID, err)
	}
	return nil
}

func (s *Store) Endpoint(ctx context.Context, merchantID string) (domain.Endpoint, error) {
	e := domain.Endpoint{MerchantID: merchantID}
	err := s.db.QueryRow(ctx, `SELECT url, secret FROM endpoints WHERE merchant_id = $1`, merchantID).Scan(&e.URL, &e.Secret)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Endpoint{}, domain.ErrNotFound
	case err != nil:
		return domain.Endpoint{}, fmt.Errorf("read the endpoint of merchant %s: %w", merchantID, err)
	}
	return e, nil
}

// AddDelivery inserts d. The unique event id is the arbiter, so a redelivered
// event inserts nothing.
func (s *Store) AddDelivery(ctx context.Context, d domain.Delivery) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO deliveries (id, event_id, event_type, merchant_id, payload, traceparent, status, attempts,
		                        next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, $10, $10)
		ON CONFLICT (event_id) DO NOTHING`,
		d.ID, d.EventID, d.EventType, d.MerchantID, d.Payload, d.Traceparent, d.Status, d.Attempts,
		d.NextAttemptAt, d.CreatedAt,
	)
	if err != nil {
		return false, fmt.Errorf("add the delivery of event %s: %w", d.EventID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimDue takes the deliveries due, the longest waiting first. Counting the
// attempt in the claim fences out a dispatcher that held the delivery before,
// and the lease keeps other dispatchers away while the POST is in flight.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) ([]app.Due, error) {
	rows, err := s.db.Query(ctx, `
		WITH due (due_id) AS MATERIALIZED (
			SELECT id FROM deliveries
			WHERE status = 'pending' AND next_attempt_at <= $1
			ORDER BY next_attempt_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE deliveries d SET attempts = d.attempts + 1, next_attempt_at = $3
		FROM due, endpoints e
		WHERE d.id = due_id AND e.merchant_id = d.merchant_id
		RETURNING d.id, d.event_id, d.event_type, d.merchant_id, d.payload, coalesce(d.traceparent, ''), d.status,
		          d.attempts, d.next_attempt_at, coalesce(d.last_status, 0), coalesce(d.last_error, ''),
		          d.created_at, d.updated_at, e.url, e.secret`,
		now, limit, now.Add(domain.Lease),
	)
	if err != nil {
		return nil, fmt.Errorf("claim the deliveries due: %w", err)
	}
	due, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.Due, error) {
		var (
			x    app.Due
			d    = &x.Delivery
			next time.Time
		)
		err := row.Scan(&d.ID, &d.EventID, &d.EventType, &d.MerchantID, &d.Payload, &d.Traceparent, &d.Status,
			&d.Attempts, &next, &d.LastStatus, &d.LastError, &d.CreatedAt, &d.UpdatedAt, &x.Endpoint.URL, &x.Endpoint.Secret)
		// pgx reads timestamps in the local time zone; the service speaks UTC.
		d.NextAttemptAt, d.CreatedAt, d.UpdatedAt = next.UTC(), d.CreatedAt.UTC(), d.UpdatedAt.UTC()
		x.Endpoint.MerchantID = d.MerchantID
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("claim the deliveries due: %w", err)
	}
	return due, nil
}

// RecordAttempt applies only while the delivery is pending at d's attempt: a
// dispatcher that was overtaken by a later claim changes nothing.
func (s *Store) RecordAttempt(ctx context.Context, d domain.Delivery) error {
	var next, delivered *time.Time
	if d.Status == domain.Pending {
		next = &d.NextAttemptAt
	}
	if d.Status == domain.Delivered {
		delivered = &d.DeliveredAt
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE deliveries
		SET status = $3, next_attempt_at = $4, last_status = nullif($5, 0), last_error = nullif($6, ''),
		    updated_at = $7, delivered_at = $8
		WHERE id = $1 AND attempts = $2 AND status = 'pending'`,
		d.ID, d.Attempts, d.Status, next, d.LastStatus, d.LastError, d.UpdatedAt, delivered,
	)
	if err != nil {
		return fmt.Errorf("record attempt %d of delivery %s: %w", d.Attempts, d.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrStale
	}
	return nil
}
