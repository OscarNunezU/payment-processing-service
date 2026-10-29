package outbox

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var meter = otel.Meter("github.com/OscarNunezU/payment-processing-service/internal/platform/outbox")

// published counts the events Kafka acknowledged.
var published, _ = meter.Int64Counter("outbox.published", metric.WithDescription("Events the relay published to Kafka."))

// ObservePending reports the events waiting in the outbox: it grows while
// Kafka is down, and drains once Kafka is back.
func ObservePending(db *pgxpool.Pool) error {
	_, err := meter.Int64ObservableGauge("outbox.pending",
		metric.WithDescription("Events in the outbox, waiting for the relay."),
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			var n int64
			if err := db.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
				return err
			}
			o.Observe(n)
			return nil
		}))
	return err
}
