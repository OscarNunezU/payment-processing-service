package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// ClaimDue is the claim of the recovery (RFC 0001, section 5.5). It takes the
// payments whose lease expired, oldest lease first. Incrementing the epoch
// fences out whoever held each payment before, and the new lease keeps other
// workers away while its call is in flight. With SKIP LOCKED, workers that
// claim at the same time neither wait for each other nor take the same
// payment.
//
// A payment reached the acquirer unless every one of its attempts was refused
// before it was sent: an attempt without an outcome, cut by a crash, may have
// reached it.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int) ([]app.Claim, error) {
	rows, err := s.db.Query(ctx, `
		WITH due (due_id) AS MATERIALIZED (
			SELECT id FROM payments
			WHERE status = 'PROCESSING' AND lease_until <= $1
			ORDER BY lease_until
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE payments SET epoch = epoch + 1, lease_until = $3
		FROM due WHERE id = due_id
		RETURNING `+paymentColumns+`, EXISTS (
			SELECT 1 FROM payment_history h
			WHERE h.payment_id = payments.id AND h.kind = 'acquirer_call'
			  AND h.outcome IS DISTINCT FROM 'not_sent'
		)`,
		now, limit, now.Add(domain.Lease),
	)
	if err != nil {
		return nil, fmt.Errorf("claim the payments due: %w", err)
	}
	claims, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.Claim, error) {
		var c app.Claim
		var err error
		c.Payment, err = scanPayment(row, &c.Reached)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("claim the payments due: %w", err)
	}
	return claims, nil
}

// StartAttempt records the attempt before its call, so that no call is ever
// sent without a trace.
func (s *Store) StartAttempt(ctx context.Context, p domain.Payment, at time.Time) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := fenced(ctx, tx, p, `attempts = $3`, p.Attempts); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_history (payment_id, kind, attempt, created_at)
			VALUES ($1, 'acquirer_call', $2, $3)`,
			p.ID, p.Attempts, at,
		); err != nil {
			return fmt.Errorf("record attempt %d of payment %s: %w", p.Attempts, p.ID, err)
		}
		return nil
	})
}

// StartReversal is the start of a reversal (RFC 0001, section 5.3): the
// reversal is recorded before it is sent, so the payment is never authorized
// again.
func (s *Store) StartReversal(ctx context.Context, p domain.Payment, at time.Time) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := fenced(ctx, tx, p, `reversal_started_at = $3`, p.ReversalStartedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_history (payment_id, kind, created_at) VALUES ($1, 'reversal', $2)`,
			p.ID, at,
		); err != nil {
			return fmt.Errorf("record a reversal of payment %s: %w", p.ID, err)
		}
		return nil
	})
}

// RecordReversal records the answer to the payment's last reversal, the one
// its epoch sent.
func (s *Store) RecordReversal(ctx context.Context, p domain.Payment, a domain.Answer, final *events.PaymentEvent) error {
	return s.record(ctx, p, final, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE payment_history SET outcome = $2, latency_ms = $3, response_code = nullif($4, '')
			WHERE id = (SELECT max(id) FROM payment_history WHERE payment_id = $1 AND kind = 'reversal')`,
			p.ID, a.Outcome, a.Latency.Milliseconds(), a.ResponseCode,
		); err != nil {
			return fmt.Errorf("record a reversal of payment %s: %w", p.ID, err)
		}
		return nil
	})
}

func (s *Store) Finish(ctx context.Context, p domain.Payment, final events.PaymentEvent) error {
	return s.record(ctx, p, &final, nil)
}

func (s *Store) Unresolved(ctx context.Context, createdBefore time.Time) (app.Unresolved, error) {
	var u app.Unresolved
	err := s.db.QueryRow(ctx, `
		SELECT id, created_at, count(*) OVER ()
		FROM payments WHERE status = 'PROCESSING' AND created_at < $1
		ORDER BY created_at LIMIT 1`,
		createdBefore,
	).Scan(&u.OldestID, &u.OldestCreatedAt, &u.Count)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return app.Unresolved{}, nil
	case err != nil:
		return app.Unresolved{}, fmt.Errorf("count the unresolved payments: %w", err)
	}
	u.OldestCreatedAt = u.OldestCreatedAt.UTC()
	return u, nil
}

// fenced applies set to p's row, with arg as $3, if the row is still
// PROCESSING with p's epoch.
func fenced(ctx context.Context, tx pgx.Tx, p domain.Payment, set string, arg any) error {
	// set is one of the fixed assignments above; every value is an argument.
	tag, err := tx.Exec(ctx, `UPDATE payments SET `+set+` WHERE id = $1 AND epoch = $2 AND status = 'PROCESSING'`,
		p.ID, p.Epoch, arg)
	if err != nil {
		return fmt.Errorf("update payment %s: %w", p.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrStaleEpoch
	}
	return nil
}
