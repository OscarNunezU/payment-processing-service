package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
)

func TestOpenSetsTheSessionLimits(t *testing.T) {
	pool, err := postgres.Open(t.Context(), pgtest.Start(t), "test-app", 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	for setting, want := range map[string]string{
		"statement_timeout":                   "2s",
		"idle_in_transaction_session_timeout": "5s",
		"application_name":                    "test-app",
	} {
		var got string
		if err := pool.QueryRow(t.Context(), "SELECT current_setting($1)", setting).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", setting, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", setting, got, want)
		}
	}
	if got := pool.Config().MaxConns; got != 4 {
		t.Errorf("MaxConns = %d, want 4", got)
	}
}

func TestAStatementOverTheLimitIsCanceled(t *testing.T) {
	pool, err := postgres.Open(t.Context(), pgtest.Start(t), "test-app", 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(t.Context(), "SELECT pg_sleep(3)")
	if err == nil {
		t.Fatal("a 3 s statement finished, want it canceled after 2 s")
	}
}

func TestBeginFuncRollsBackOnError(t *testing.T) {
	ctx := t.Context()
	pool, err := postgres.Open(ctx, pgtest.Start(t), "test-app", 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "CREATE TABLE t (n int)"); err != nil {
		t.Fatal(err)
	}

	errBoom := errors.New("boom")
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("BeginFunc returned %v, want the function's error", err)
	}
	if n := count(ctx, t, pool); n != 0 {
		t.Errorf("rows = %d after a rollback, want 0", n)
	}
}

func count(ctx context.Context, t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) int {
	t.Helper()
	var n int
	if err := q.QueryRow(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
