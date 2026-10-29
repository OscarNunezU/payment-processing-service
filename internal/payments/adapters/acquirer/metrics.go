package acquirer

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

var meter = otel.Meter("github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/acquirer")

// answers counts the acquirer's answers. A rise of the uncertain ones is the
// first sign of an acquirer in trouble, before the breaker opens.
var answers, _ = meter.Int64Counter("payments.acquirer.answers",
	metric.WithDescription("Answers of the acquirer, by call (authorize or reverse) and outcome."))

func countAnswer(ctx context.Context, call string, o domain.Outcome) {
	answers.Add(ctx, 1, metric.WithAttributes(attribute.String("call", call), attribute.String("outcome", string(o))))
}

// ObserveBreaker reports whether b refuses calls, as 1 or 0.
func ObserveBreaker(b *Breaker) error {
	_, err := meter.Int64ObservableGauge("payments.breaker.open",
		metric.WithDescription("1 while the circuit breaker refuses calls to the acquirer."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			open := int64(0)
			if b.Open() {
				open = 1
			}
			o.Observe(open)
			return nil
		}))
	return err
}
