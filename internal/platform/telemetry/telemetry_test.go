package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func TestSetupPropagatesTheTraceContext(t *testing.T) {
	shutdown, err := Setup(context.Background(), "payments-api", "test", "")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	ctx, span := otel.Tracer("test").Start(context.Background(), "op")
	defer span.End()

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	traceparent := carrier.Get("traceparent")
	if !strings.Contains(traceparent, span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent = %q, want it to carry trace %s", traceparent, span.SpanContext().TraceID())
	}
}
