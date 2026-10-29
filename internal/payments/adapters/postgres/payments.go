package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/outbox"
)

// paymentColumns is the read order that scanPayment expects.
const paymentColumns = `id, merchant_id, order_id, idempotency_key, request_hash, amount, currency, card_token,
	coalesce(card_brand, ''), coalesce(card_last4, ''), status, coalesce(reason, ''),
	coalesce(authorization_code, ''), coalesce(decline_code, ''), created_at, updated_at`

func scanPayment(row pgx.Row) (domain.Payment, error) {
	var (
		p    domain.Payment
		hash []byte
	)
	err := row.Scan(&p.ID, &p.MerchantID, &p.OrderID, &p.IdempotencyKey, &hash, &p.Amount, &p.Currency, &p.CardToken,
		&p.CardBrand, &p.CardLast4, &p.Status, &p.Reason, &p.AuthorizationCode, &p.DeclineCode, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return domain.Payment{}, err
	}
	copy(p.RequestHash[:], hash)
	// pgx reads timestamps in the local time zone; the service speaks UTC.
	p.CreatedAt, p.UpdatedAt = p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, nil
}

// InsertPayment stores p, its first history entry and the outbox row of
// accepted in one transaction (RFC 0001, section 5.1).
//
// The unique index on the key is the arbiter: when the key is taken, nothing
// is inserted, and the payment that holds it is read afterwards. Two requests
// with one key at the same time are serialized by that index: the second
// waits until the first commits, and then finds its payment.
func (s *Store) InsertPayment(ctx context.Context, p domain.Payment, accepted events.PaymentEvent) (domain.Payment, bool, error) {
	payload, err := json.Marshal(accepted)
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("encode the event of payment %s: %w", p.ID, err)
	}

	created := false
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			WITH inserted AS (
				INSERT INTO payments (id, merchant_id, order_id, idempotency_key, request_hash, amount, currency,
				                      card_token, status, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
				ON CONFLICT (merchant_id, idempotency_key) DO NOTHING
				RETURNING id, status, created_at
			)
			INSERT INTO payment_history (payment_id, kind, status, created_at)
			SELECT id, 'status', status, created_at FROM inserted`,
			p.ID, p.MerchantID, p.OrderID, p.IdempotencyKey, p.RequestHash[:], p.Amount, p.Currency,
			p.CardToken, p.Status, p.CreatedAt,
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
		created = true
		return outbox.Write(ctx, tx, outbox.Message{
			Topic:   events.PaymentsTopic,
			Key:     p.ID,
			Payload: payload,
			Headers: map[string]string{events.HeaderEventID: accepted.ID, events.HeaderEventType: string(accepted.Type)},
		})
	})
	if err != nil {
		return domain.Payment{}, false, err
	}
	if created {
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
