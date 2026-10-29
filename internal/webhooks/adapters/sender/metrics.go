package sender

import (
	"context"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

var meter = otel.Meter("github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/sender")

var attempts, _ = meter.Int64Counter("webhooks.attempts",
	metric.WithDescription("POSTs of webhooks, by result: the status class (2xx to 5xx), or why no answer came."))

func countAttempt(ctx context.Context, a domain.Attempt) {
	result := a.Error
	if a.StatusCode != 0 {
		result = strconv.Itoa(a.StatusCode/100) + "xx"
	}
	attempts.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
