// Package postgres stores transactions in PostgreSQL with hand-written SQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
const columns = `id::text, merchant_id, amount, currency, status, card_last4, card_data,
	COALESCE(reason, ''), attempts, available_at, created_at, updated_at`

func (r *Repository) Create(ctx context.Context, txn payment.Transaction) (payment.Transaction, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO transactions (merchant_id, amount, currency, status, card_last4, card_data)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+columns,
		txn.MerchantID, txn.Amount, txn.Currency, string(txn.Status), txn.CardLast4, txn.CardData,
	)
	return scanTransaction(row)
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

func scanTransaction(row pgx.Row) (payment.Transaction, error) {
	var (
		txn    payment.Transaction
		status string
	)
	err := row.Scan(
		&txn.ID, &txn.MerchantID, &txn.Amount, &txn.Currency, &status, &txn.CardLast4, &txn.CardData,
		&txn.Reason, &txn.Attempts, &txn.AvailableAt, &txn.CreatedAt, &txn.UpdatedAt,
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
