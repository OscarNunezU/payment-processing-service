package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/outbox"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/telemetry"
)

// paymentColumns is the read order that scanPayment expects.
const paymentColumns = `id, merchant_id, order_id, idempotency_key, request_hash, amount, currency, card_token,
	coalesce(card_brand, ''), coalesce(card_last4, ''), status, coalesce(reason, ''),
	coalesce(authorization_code, ''), coalesce(decline_code, ''), attempts, epoch, lease_until,
	reversal_started_at, created_at, updated_at`

// scanPayment reads a payment in the order of paymentColumns, and then the
// columns that follow into extra.
func scanPayment(row pgx.Row, extra ...any) (domain.Payment, error) {
	var (
		p                      domain.Payment
		hash                   []byte
		lease, reversalStarted *time.Time
	)
	err := row.Scan(append([]any{&p.ID, &p.MerchantID, &p.OrderID, &p.IdempotencyKey, &hash, &p.Amount, &p.Currency,
		&p.CardToken, &p.CardBrand, &p.CardLast4, &p.Status, &p.Reason, &p.AuthorizationCode, &p.DeclineCode,
		&p.Attempts, &p.Epoch, &lease, &reversalStarted, &p.CreatedAt, &p.UpdatedAt}, extra...)...)
	if err != nil {
		return domain.Payment{}, err
	}
	copy(p.RequestHash[:], hash)
	// pgx reads timestamps in the local time zone; the service speaks UTC.
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	if lease != nil {
		p.LeaseUntil = lease.UTC()
	}
	if reversalStarted != nil {
		p.ReversalStartedAt = reversalStarted.UTC()
	}
	return p, nil
}

// InsertPayment is T1 (RFC 0001, section 5.1): the payment and its first
// attempt, recorded before the call to the acquirer.
//
// The unique index on the key is the arbiter: when the key is taken, nothing
// is inserted, and the payment that holds it is read afterwards. Two requests
// with one key at the same time are serialized by that index: the second
// waits until the first commits, and then finds its payment. Only a new
// payment goes through admit, and a refusal rolls the insert back. The trace of
// ctx is kept with the payment, for the recovery to resume.
func (s *Store) InsertPayment(ctx context.Context, p domain.Payment, admit func() error) (domain.Payment, bool, error) {
	created := false
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO payments (id, merchant_id, order_id, idempotency_key, request_hash, amount, currency,
			                      card_token, status, attempts, epoch, lease_until, created_at, updated_at, traceparent)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13, nullif($14, ''))
			ON CONFLICT (merchant_id, idempotency_key) DO NOTHING`,
			p.ID, p.MerchantID, p.OrderID, p.IdempotencyKey, p.RequestHash[:], p.Amount, p.Currency,
			p.CardToken, p.Status, p.Attempts, p.Epoch, p.LeaseUntil, p.CreatedAt, telemetry.TraceParent(ctx),
		)
		if err != nil {
			if violates(err, "payments_one_live_per_order") {
				return domain.ErrOrderHasActivePayment
			}
			return fmt.Errorf("insert payment %s: %w", p.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return nil // the key holds another payment
		}
		if err := admit(); err != nil {
			return err // rolls the insert back: the payment never existed
		}
		created = true
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_history (payment_id, kind, status, attempt, created_at)
			VALUES ($1, 'status', $2, NULL, $4), ($1, 'acquirer_call', NULL, $3, $4)`,
			p.ID, p.Status, p.Attempts, p.CreatedAt,
		); err != nil {
			return fmt.Errorf("record the first attempt of payment %s: %w", p.ID, err)
		}
		return nil
	})
	if err != nil {
		return domain.Payment{}, false, err
	}
	if created {
		countCreated(ctx)
		return p, true, nil
	}

	existing, err := s.PaymentByKey(ctx, p.MerchantID, p.IdempotencyKey)
	if errors.Is(err, app.ErrNotFound) {
		return domain.Payment{}, false, fmt.Errorf("the key of payment %s conflicted, but no payment holds it", p.ID)
	}
	if err != nil {
		return domain.Payment{}, false, err
	}
	return existing, false, nil
}

// RecordAnswer is T2 (RFC 0001, section 5.2). The update applies only to a
// payment still PROCESSING with p's epoch, so a writer that was taken over
// changes nothing.
func (s *Store) RecordAnswer(ctx context.Context, p domain.Payment, a domain.Answer, final *events.PaymentEvent) error {
	return s.record(ctx, p, final, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE payment_history SET outcome = $3, latency_ms = $4, response_code = nullif($5, '')
			WHERE payment_id = $1 AND kind = 'acquirer_call' AND attempt = $2`,
			p.ID, p.Attempts, a.Outcome, a.Latency.Milliseconds(), a.ResponseCode,
		); err != nil {
			return fmt.Errorf("record attempt %d of payment %s: %w", p.Attempts, p.ID, err)
		}
		return nil
	})
}

// record stores p's new state, conditional on the stored payment being still
// PROCESSING with p's epoch, in one transaction with the answer of its call,
// when call is not nil, and with the final status and its event in the
// outbox, when final is not nil.
func (s *Store) record(ctx context.Context, p domain.Payment, final *events.PaymentEvent, call func(pgx.Tx) error) error {
	var payload []byte
	if final != nil {
		var err error
		if payload, err = json.Marshal(final); err != nil {
			return fmt.Errorf("encode the event of payment %s: %w", p.ID, err)
		}
	}
	var lease *time.Time
	if !p.Status.Final() {
		lease = &p.LeaseUntil
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE payments
			SET status = $3, reason = nullif($4, ''), authorization_code = nullif($5, ''),
			    decline_code = nullif($6, ''), card_brand = nullif($7, ''), card_last4 = nullif($8, ''),
			    lease_until = $9, updated_at = $10
			WHERE id = $1 AND epoch = $2 AND status = 'PROCESSING'`,
			p.ID, p.Epoch, p.Status, p.Reason, p.AuthorizationCode, p.DeclineCode, p.CardBrand, p.CardLast4,
			lease, p.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("record the state of payment %s: %w", p.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return app.ErrStaleEpoch
		}
		if call != nil {
			if err := call(tx); err != nil {
				return err
			}
		}
		if final == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_history (payment_id, kind, status, reason, created_at)
			VALUES ($1, 'status', $2, nullif($3, ''), $4)`,
			p.ID, p.Status, p.Reason, p.UpdatedAt,
		); err != nil {
			return fmt.Errorf("record the final state of payment %s: %w", p.ID, err)
		}
		return outbox.Write(ctx, tx, outbox.Message{
			Topic:   events.PaymentsTopic,
			Key:     p.ID,
			Payload: payload,
			Headers: map[string]string{events.HeaderEventID: final.ID, events.HeaderEventType: string(final.Type)},
		})
	})
	if err == nil && final != nil {
		countFinished(ctx, p)
	}
	return err
}

// violates reports whether err is a unique violation of constraint.
func violates(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func (s *Store) PaymentByKey(ctx context.Context, merchantID, key string) (domain.Payment, error) {
	return s.onePayment(ctx, `SELECT `+paymentColumns+` FROM payments WHERE merchant_id = $1 AND idempotency_key = $2`, merchantID, key)
}

// Payment filters by merchant as well as by id: another merchant's payment is
// not found, as if it did not exist (FR04).
func (s *Store) Payment(ctx context.Context, merchantID, id string) (domain.Payment, error) {
	return s.onePayment(ctx, `SELECT `+paymentColumns+` FROM payments WHERE merchant_id = $1 AND id = $2`, merchantID, id)
}

func (s *Store) onePayment(ctx context.Context, query string, args ...any) (domain.Payment, error) {
	p, err := scanPayment(s.db.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Payment{}, fmt.Errorf("read a payment: %w", err)
	}
	return p, nil
}

func (s *Store) History(ctx context.Context, paymentID string) ([]domain.HistoryEntry, error) {
	rows, err := s.db.Query(ctx, `
		SELECT kind, coalesce(status, ''), coalesce(attempt, 0), coalesce(outcome, ''), coalesce(latency_ms, 0),
		       coalesce(response_code, ''), coalesce(reason, ''), created_at
		FROM payment_history WHERE payment_id = $1 ORDER BY id`,
		paymentID,
	)
	if err != nil {
		return nil, fmt.Errorf("read the history of payment %s: %w", paymentID, err)
	}
	history, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.HistoryEntry, error) {
		var h domain.HistoryEntry
		err := row.Scan(&h.Kind, &h.Status, &h.Attempt, &h.Outcome, &h.LatencyMS, &h.ResponseCode, &h.Reason, &h.At)
		h.At = h.At.UTC()
		return h, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the history of payment %s: %w", paymentID, err)
	}
	return history, nil
}

// ListPayments pages by id: each page starts below the last id of the one
// before, which an index serves however deep the page is.
func (s *Store) ListPayments(ctx context.Context, f app.ListFilter) ([]domain.Payment, error) {
	where := []string{"merchant_id = $1"}
	args := []any{f.MerchantID}
	add := func(condition string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.OrderID != "" {
		add("order_id = $%d", f.OrderID)
	}
	if f.StartingAfter != "" {
		add("id < $%d", f.StartingAfter)
	}
	args = append(args, f.Limit)
	// Only fixed conditions and placeholders are joined; every value is an argument.
	query := `SELECT ` + paymentColumns + ` FROM payments WHERE ` + strings.Join(where, " AND ") +
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args))

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	payments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Payment, error) { return scanPayment(row) })
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	return payments, nil
}
