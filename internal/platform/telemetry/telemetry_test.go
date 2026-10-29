package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
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

func TestAResumedSpanJoinsTheTraceItWasKeptFrom(t *testing.T) {
	shutdown, err := Setup(context.Background(), "payments-worker", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	ctx, request := otel.Tracer("test").Start(context.Background(), "POST /v1/payments")
	kept := TraceParent(ctx)
	request.End()
	if !strings.Contains(kept, request.SpanContext().TraceID().String()) {
		t.Fatalf("kept %q, want the request's trace %s", kept, request.SpanContext().TraceID())
	}

	resumed, end := Resume(context.Background(), kept, "recover payment")
	defer end()
	got := trace.SpanContextFromContext(resumed)
	if got.TraceID() != request.SpanContext().TraceID() || got.SpanID() == request.SpanContext().SpanID() {
		t.Errorf("the resumed span is %s/%s, want a new span in trace %s", got.TraceID(), got.SpanID(), request.SpanContext().TraceID())
	}

	if TraceParent(context.Background()) != "" {
		t.Error("a context without a trace has a traceparent")
	}
	fresh, end := Resume(context.Background(), "", "recover payment")
	defer end()
	if sc := trace.SpanContextFromContext(fresh); !sc.IsValid() || sc.TraceID() == request.SpanContext().TraceID() {
		t.Errorf("without a traceparent, got %s, want a trace of its own", sc.TraceID())
	}
}
