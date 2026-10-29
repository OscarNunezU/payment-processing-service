package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"
)

func TestSetupPropagatesTheTraceContext(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{Service: "payments-api", Version: "test"})
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
	shutdown, err := Setup(context.Background(), Config{Service: "payments-worker", Version: "test"})
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

func TestMetricsArePushedOverOTLP(t *testing.T) {
	type push struct{ path, contentType string }
	pushes := make(chan push, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushes <- push{r.URL.Path, r.Header.Get("Content-Type")}
	}))
	defer srv.Close()

	shutdown, err := Setup(context.Background(), Config{Service: "payments-api", Version: "test", MetricsEndpoint: srv.URL + "/api/v1/otlp/v1/metrics"})
	if err != nil {
		t.Fatal(err)
	}
	counter, err := otel.Meter("test").Int64Counter("test.pushed")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 1)
	// Shutting down pushes what is pending, as the services do when they stop.
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-pushes:
		if p.path != "/api/v1/otlp/v1/metrics" || p.contentType != "application/x-protobuf" {
			t.Errorf("got a push to %s as %s, want OTLP protobuf to the configured path", p.path, p.contentType)
		}
	default:
		t.Fatal("no metrics were pushed")
	}
}

func TestHTTPDurationsHaveFineBucketsAroundTheTimeout(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithView(durations...))
	histogram, err := provider.Meter("test").Float64Histogram("http.server.request.duration")
	if err != nil {
		t.Fatal(err)
	}
	histogram.Record(context.Background(), 5.03) // a call cut at the 5 s timeout, plus its writes

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	point := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64]).DataPoints[0]
	for i, bound := range point.Bounds {
		if bound == 5.05 && point.BucketCounts[i] == 1 && point.Bounds[i-1] == 5.025 {
			return
		}
	}
	t.Errorf("5.03 s fell in the buckets %v, %v, want it between 5.025 and 5.05 s", point.Bounds, point.BucketCounts)
}
