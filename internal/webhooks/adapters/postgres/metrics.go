package postgres

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

var meter = otel.Meter("github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/postgres")

// delay is how long a webhook took to be accepted, from its event: its
// retries show up here.
var delay, _ = meter.Float64Histogram("webhooks.delivery.delay",
	metric.WithDescription("Time from an event to its webhook being accepted."), metric.WithUnit("s"),
	metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 1800, 3600, 21600, 86400))

// ObserveDeliveries reports the deliveries by status, from the database.
func ObserveDeliveries(s *Store) error {
	_, err := meter.Int64ObservableGauge("webhooks.deliveries",
		metric.WithDescription("Deliveries of webhooks, by status."),
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			counts := map[domain.Status]int64{domain.Pending: 0, domain.Delivered: 0, domain.Failed: 0}
			rows, err := s.db.Query(ctx, `SELECT status, count(*) FROM deliveries GROUP BY status`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var status domain.Status
				var n int64
				if err := rows.Scan(&status, &n); err != nil {
					return err
				}
				counts[status] = n
			}
			if err := rows.Err(); err != nil {
				return err
			}
			for status, n := range counts {
				o.Observe(n, metric.WithAttributes(attribute.String("status", string(status))))
			}
			return nil
		}))
	return err
}
