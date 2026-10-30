package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("github.com/OscarNunezU/payment-processing-service/internal/platform/postgres")

// ObservePool reports the pool's connections, and how long requests waited
// for one: a pool too small for the load shows up as waits long before
// Postgres is busy.
func ObservePool(pool *pgxpool.Pool) error {
	conns, err := meter.Int64ObservableGauge("db.pool.connections",
		metric.WithDescription("Connections of the pool, by state: acquired, idle, or max."))
	if err != nil {
		return err
	}
	waits, err := meter.Int64ObservableCounter("db.pool.waits",
		metric.WithDescription("Acquires that found no idle connection and had to wait."))
	if err != nil {
		return err
	}
	waited, err := meter.Float64ObservableCounter("db.pool.wait.duration", metric.WithUnit("s"),
		metric.WithDescription("Time spent waiting for a connection."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := pool.Stat()
		state := func(v string) metric.ObserveOption { return metric.WithAttributes(attribute.String("state", v)) }
		o.ObserveInt64(conns, int64(s.AcquiredConns()), state("acquired"))
		o.ObserveInt64(conns, int64(s.IdleConns()), state("idle"))
		o.ObserveInt64(conns, int64(s.MaxConns()), state("max"))
		o.ObserveInt64(waits, s.EmptyAcquireCount())
		o.ObserveFloat64(waited, s.EmptyAcquireWaitTime().Seconds())
		return nil
	}, conns, waits, waited)
	return err
}
