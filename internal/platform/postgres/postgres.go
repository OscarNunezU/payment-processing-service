// Package postgres opens connection pools with the session limits every service
// shares. Transactions use pgx.BeginFunc, which commits when the function
// returns nil and rolls back on an error or a panic.
package postgres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// No statement may run longer than 2 s and no transaction may sit idle for more
// than 5 s, so a slow acquirer can never hold locks (RFC 0001, section 5.2).
const (
	StatementTimeout         = 2 * time.Second
	IdleInTransactionTimeout = 5 * time.Second
)

// Open creates a pool of at most maxConns connections, identified in Postgres
// as application, and checks that the database answers.
func Open(ctx context.Context, url, application string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse the database url: %w", err)
	}
	cfg.MaxConns = maxConns
	params := cfg.ConnConfig.RuntimeParams
	params["application_name"] = application
	params["statement_timeout"] = strconv.FormatInt(StatementTimeout.Milliseconds(), 10)
	params["idle_in_transaction_session_timeout"] = strconv.FormatInt(IdleInTransactionTimeout.Milliseconds(), 10)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create the pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach the database: %w", err)
	}
	return pool, nil
}
