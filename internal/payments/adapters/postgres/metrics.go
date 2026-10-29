package postgres

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// The store counts what it committed, so the numbers are the database's, not
// what a request hoped to write.

var meter = otel.Meter("github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres")

var (
	created, _  = meter.Int64Counter("payments.created", metric.WithDescription("Payments created."))
	finished, _ = meter.Int64Counter("payments.finished",
		metric.WithDescription("Payments that reached a final state, by status and reason."))
	recoveryAttempts, _ = meter.Int64Counter("payments.recovery.attempts",
		metric.WithDescription("Attempts the recovery sent to the acquirer."))
	recoveryReversals, _ = meter.Int64Counter("payments.recovery.reversals",
		metric.WithDescription("Reversals the recovery sent to the acquirer."))
)

func countCreated(ctx context.Context) { created.Add(ctx, 1) }

func countFinished(ctx context.Context, p domain.Payment) {
	finished.Add(ctx, 1, metric.WithAttributes(attribute.String("status", string(p.Status)), attribute.String("reason", string(p.Reason))))
}

// ObserveRecovery reports, from the database, the payments in progress and
// those still PROCESSING after domain.AlertAfter: the gauge behind the alert
// of INV04.
func ObserveRecovery(s *Store, now func() time.Time) error {
	processing, err := meter.Int64ObservableGauge("payments.processing",
		metric.WithDescription("Payments in PROCESSING."))
	if err != nil {
		return err
	}
	unresolved, err := meter.Int64ObservableGauge("payments.unresolved",
		metric.WithDescription("Payments still PROCESSING more than 5 minutes after they were created (INV04)."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		t := now()
		all, err := s.Unresolved(ctx, t)
		if err != nil {
			return err
		}
		late, err := s.Unresolved(ctx, t.Add(-domain.AlertAfter))
		if err != nil {
			return err
		}
		o.ObserveInt64(processing, int64(all.Count))
		o.ObserveInt64(unresolved, int64(late.Count))
		return nil
	}, processing, unresolved)
	return err
}
