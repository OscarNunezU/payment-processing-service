// Package telemetry sets up OpenTelemetry: traces, so a payment can be
// followed across HTTP calls and Kafka messages as a single trace, and
// metrics (RFC 0001, section 13; ADR 0006). Both leave the process over OTLP.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// MetricsInterval is how often metrics are pushed: often enough for a
// dashboard to follow a load test.
const MetricsInterval = 10 * time.Second

// durations gives the HTTP durations fine buckets around the acquirer's 5 s
// timeout, so a percentile tells a call cut at the timeout from one that took
// longer (NFR01). The default buckets jump from 5 s to 7.5 s.
var durations = func() []sdkmetric.View {
	bounds := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 5.025, 5.05, 5.1, 5.25, 5.5, 7.5, 10}
	var views []sdkmetric.View
	for _, name := range []string{"http.server.request.duration", "http.client.request.duration"} {
		views = append(views, sdkmetric.NewView(sdkmetric.Instrument{Name: name},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: bounds}}))
	}
	return views
}()

// Config says what the process is and where its signals go. An empty
// endpoint keeps that signal in the process, created but never exported,
// which is what tests want.
type Config struct {
	Service string
	Version string
	// TracesEndpoint is the URL of an OTLP/gRPC traces receiver, such as
	// Tempo. An http URL is sent in plain text, which is enough for a local
	// backend; production would use https.
	TracesEndpoint string
	// MetricsEndpoint is the URL of an OTLP/HTTP metrics receiver, such as
	// Prometheus's.
	MetricsEndpoint string
}

// Setup installs the global tracer and meter providers and the propagator,
// and returns a function that flushes what is pending on shutdown.
func Setup(ctx context.Context, c Config) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	// The instance tells apart the replicas of a service in the metrics.
	instance, _ := os.Hostname()
	res := resource.NewSchemaless(
		attribute.String("service.name", c.Service),
		attribute.String("service.version", c.Version),
		attribute.String("service.instance.id", instance),
	)

	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if c.TracesEndpoint != "" {
		exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(c.TracesEndpoint))
		if err != nil {
			return nil, fmt.Errorf("create the trace exporter: %w", err)
		}
		traceOpts = append(traceOpts, sdktrace.WithBatcher(exporter))
	}
	tracer := sdktrace.NewTracerProvider(traceOpts...)
	otel.SetTracerProvider(tracer)

	meterOpts := []sdkmetric.Option{sdkmetric.WithResource(res), sdkmetric.WithView(durations...)}
	if c.MetricsEndpoint != "" {
		exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(c.MetricsEndpoint))
		if err != nil {
			return nil, fmt.Errorf("create the metric exporter: %w", err)
		}
		meterOpts = append(meterOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(MetricsInterval))))
	}
	meter := sdkmetric.NewMeterProvider(meterOpts...)
	otel.SetMeterProvider(meter)

	return func(ctx context.Context) error {
		return errors.Join(tracer.Shutdown(ctx), meter.Shutdown(ctx))
	}, nil
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
