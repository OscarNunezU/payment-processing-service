package outbox_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/outbox"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := pgtest.Start(t)
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fsys, err := migrations.For("payments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(t.Context(), db, fsys); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.Open(t.Context(), url, "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestWriteKeepsTheMessageAndTheTraceContext(t *testing.T) {
	pool := setup(t)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(t.Context(), "op")
	defer span.End()

	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Write(ctx, tx, outbox.Message{
			Topic: "payments.events.v1", Key: "pay_1", Payload: []byte("event"),
			Headers: map[string]string{"type": "payment.authorized"},
		})
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	var topic, key string
	var payload, headers []byte
	if err := pool.QueryRow(ctx, "SELECT topic, key, payload, headers FROM outbox").Scan(&topic, &key, &payload, &headers); err != nil {
		t.Fatal(err)
	}
	if topic != "payments.events.v1" || key != "pay_1" || string(payload) != "event" {
		t.Errorf("row = (%s, %s, %s), want the message as written", topic, key, payload)
	}
	var h map[string]string
	if err := json.Unmarshal(headers, &h); err != nil {
		t.Fatal(err)
	}
	if h["type"] != "payment.authorized" {
		t.Errorf("headers = %v, want the caller's headers kept", h)
	}
	if !strings.Contains(h["traceparent"], span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent = %q, want the trace of the change", h["traceparent"])
	}
}

func TestAMessageRollsBackWithItsChange(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	errBoom := errors.New("the change failed")

	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := outbox.Write(ctx, tx, outbox.Message{Topic: "t", Key: "k", Payload: []byte("x")}); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("BeginFunc returned %v, want the change's error", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM outbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("outbox rows = %d after a rollback, want 0", n)
	}
}

func TestAMessageNeedsATopicAndAKey(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Write(ctx, tx, outbox.Message{Payload: []byte("x")})
	})
	if err == nil {
		t.Error("Write accepted a message without topic or key")
	}
}
