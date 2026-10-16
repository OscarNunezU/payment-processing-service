// Package postgres stores transactions in PostgreSQL with hand-written SQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// columns is the read order that scanTransaction expects.
const columns = `id::text, merchant_id, COALESCE(idempotency_key, ''), COALESCE(request_fingerprint, ''),
	amount, currency, status, card_last4, card_data, COALESCE(reason, ''), COALESCE(authorization_code, ''),
	attempts, available_at, created_at, updated_at`

// recordEvent completes a WITH clause whose first statement, changed, returns
// the rows it modified: each one gets an event in the same statement as the
// change, so neither can be stored without the other.
const recordEvent = `event AS (
		INSERT INTO transaction_events (transaction_id, status, attempt, reason)
		SELECT id, status, attempts, reason FROM changed
	)`

// Create relies on the unique index over (merchant_id, idempotency_key): if the
// key is taken, nothing is inserted and the payment that holds it is returned.
// When two requests with the same key race, the second waits for the first to
// commit and ends the same way.
func (r *Repository) Create(ctx context.Context, txn payment.Transaction) (payment.Transaction, bool, error) {
	stored, err := scanTransaction(r.db.QueryRow(ctx, `
		WITH changed AS (
			INSERT INTO transactions
			    (merchant_id, idempotency_key, request_fingerprint, amount, currency, status, card_last4, card_data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (merchant_id, idempotency_key) DO NOTHING
			RETURNING *
		), `+recordEvent+`
		SELECT `+columns+` FROM changed`,
		txn.MerchantID, txn.IdempotencyKey, txn.RequestFingerprint, txn.Amount, txn.Currency,
		string(txn.Status), txn.CardLast4, txn.CardData,
	))
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return payment.Transaction{}, false, err
	}

	existing, found, err := r.FindByIdempotencyKey(ctx, txn.MerchantID, txn.IdempotencyKey)
	if err != nil {
		return payment.Transaction{}, false, err
	}
	if !found {
		return payment.Transaction{}, false, fmt.Errorf("idempotency key %q conflicted but its payment was not found", txn.IdempotencyKey)
	}
	return existing, false, nil
}

func (r *Repository) FindByIdempotencyKey(ctx context.Context, merchantID, key string) (payment.Transaction, bool, error) {
	txn, err := scanTransaction(r.db.QueryRow(ctx,
		`SELECT `+columns+` FROM transactions WHERE merchant_id = $1 AND idempotency_key = $2`,
		merchantID, key,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Transaction{}, false, nil
	}
	if err != nil {
		return payment.Transaction{}, false, err
	}
	return txn, true, nil
}

func (r *Repository) Get(ctx context.Context, id string) (payment.Transaction, error) {
	txn, err := scanTransaction(r.db.QueryRow(ctx, `SELECT `+columns+` FROM transactions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return payment.Transaction{}, payment.ErrNotFound
	}
	return txn, err
}

func (r *Repository) List(ctx context.Context, filter payment.ListFilter) ([]payment.Transaction, error) {
	var (
		where []string
		args  []any
	)
	if filter.MerchantID != "" {
		args = append(args, filter.MerchantID)
		where = append(where, fmt.Sprintf("merchant_id = $%d", len(args)))
	}
	if filter.Status != "" {
		args = append(args, string(filter.Status))
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}

	query := `SELECT ` + columns + ` FROM transactions`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, len(args))

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []payment.Transaction{}
	for rows.Next() {
		txn, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, txn)
	}
	return list, rows.Err()
}

// ClaimNext is the queue: FOR UPDATE SKIP LOCKED lets every worker, in every
// replica, take a different payment without waiting for the others. The lease
// is computed with the database clock, the same one that decides when it expires.
func (r *Repository) ClaimNext(ctx context.Context, lease time.Duration) (payment.Transaction, bool, error) {
	txn, err := scanTransaction(r.db.QueryRow(ctx, `
		WITH changed AS (
			UPDATE transactions
			SET status = 'PROCESSING',
			    attempts = attempts + 1,
			    available_at = now() + make_interval(secs => $1),
			    updated_at = now()
			WHERE id = (
				SELECT id FROM transactions
				WHERE status IN ('PENDING', 'PROCESSING') AND available_at <= now()
				ORDER BY available_at
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			)
			RETURNING *
		), `+recordEvent+`
		SELECT `+columns+` FROM changed`,
		lease.Seconds(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.Transaction{}, false, nil
	}
	if err != nil {
		return payment.Transaction{}, false, err
	}
	return txn, true, nil
}

// Finish only touches a payment that is still PROCESSING, so a final state can
// never be overwritten, not even by a worker whose lease expired.
func (r *Repository) Finish(ctx context.Context, id string, outcome payment.Outcome) error {
	if !outcome.Status.IsFinal() {
		return fmt.Errorf("finish %s: %s is not a final status", id, outcome.Status)
	}
	var finished int
	err := r.db.QueryRow(ctx, `
		WITH changed AS (
			UPDATE transactions
			SET status = $2,
			    reason = NULLIF($3, ''),
			    authorization_code = NULLIF($4, ''),
			    card_data = NULL,
			    updated_at = now()
			WHERE id = $1 AND status = 'PROCESSING'
			RETURNING *
		), `+recordEvent+`
		SELECT count(*) FROM changed`,
		id, string(outcome.Status), outcome.Reason, outcome.AuthorizationCode,
	).Scan(&finished)
	if err != nil {
		return err
	}
	if finished == 0 {
		return payment.ErrNotProcessing
	}
	return nil
}

func (r *Repository) Events(ctx context.Context, transactionID string) ([]payment.Event, error) {
	rows, err := r.db.Query(ctx, `
		SELECT status, attempt, COALESCE(reason, ''), created_at
		FROM transaction_events
		WHERE transaction_id = $1
		ORDER BY id`,
		transactionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []payment.Event{}
	for rows.Next() {
		var (
			e      payment.Event
			status string
		)
		if err := rows.Scan(&status, &e.Attempt, &e.Reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Status = payment.Status(status)
		events = append(events, e)
	}
	return events, rows.Err()
}

func scanTransaction(row pgx.Row) (payment.Transaction, error) {
	var (
		txn    payment.Transaction
		status string
	)
	err := row.Scan(
		&txn.ID, &txn.MerchantID, &txn.IdempotencyKey, &txn.RequestFingerprint,
		&txn.Amount, &txn.Currency, &status, &txn.CardLast4, &txn.CardData, &txn.Reason, &txn.AuthorizationCode,
		&txn.Attempts, &txn.AvailableAt, &txn.CreatedAt, &txn.UpdatedAt,
	)
	txn.Status = payment.Status(status)
	return txn, err
}

// isInvalidUUID reports whether Postgres rejected the id because it is not a
// UUID. An id that cannot exist is simply not found.
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02" // invalid_text_representation
}
