// Package telemetry sets up OpenTelemetry tracing. The W3C trace context lets a
// payment be followed across HTTP calls and Kafka messages as a single trace
// (RFC 0001, section 13).
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup installs the global tracer provider and propagator, and returns a
// function that flushes pending spans on shutdown. Spans go over OTLP to
// endpoint ("host:port"). With an empty endpoint, spans are created but never
// exported, which is what tests want.
func Setup(ctx context.Context, service, version, endpoint string) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", service),
			attribute.String("service.version", version),
		)),
	}
	if endpoint != "" {
		// Plain text is enough for a local trace backend; production would
		// use TLS.
		exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
		if err != nil {
			return nil, fmt.Errorf("create the trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	provider := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// TraceParent returns the trace context of ctx as a W3C traceparent, or ""
// when ctx carries no trace. It is how a trace is kept with a record, so
// work done later for the record can join the trace again.
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// Resume starts a span named name as a child of traceparent, so that work
// done later for a request, such as the recovery of a payment, joins the
// request's trace. Without a valid traceparent, the span starts a trace of
// its own. end ends the span.
func Resume(ctx context.Context, traceparent, name string) (_ context.Context, end func()) {
	if traceparent != "" {
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
	}
	ctx, span := otel.Tracer("github.com/OscarNunezU/payment-processing-service").Start(ctx, name)
	return ctx, func() { span.End() }
}
