// Package telemetry sets up OpenTelemetry tracing. The W3C trace context lets a
// payment be followed across HTTP calls and Kafka messages as a single trace
// (RFC 0001, section 12).
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
// function that flushes pending spans on shutdown. Spans go to the OTLP
// collector at endpoint ("host:port"). With an empty endpoint, spans are
// created but never exported, which is what tests want.
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
		// Plain text is enough for the collector next to the service; mutual
		// TLS between services arrives in milestone 4.
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
