package acquirer

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestEveryAnswerIsCountedByCallAndOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	b := newBreaker()
	if err := ObserveBreaker(b); err != nil {
		t.Fatal(err)
	}

	c := NewClient(answering(t, 503, ``).URL, "sk_sim", time.Second, b)
	for range 5 {
		c.Authorize(t.Context(), request)
	}
	c.Reverse(t.Context(), "pay-1")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	breaker := int64(-1)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range data.DataPoints {
					call, _ := p.Attributes.Value(attribute.Key("call"))
					outcome, _ := p.Attributes.Value(attribute.Key("outcome"))
					counts[call.AsString()+" "+outcome.AsString()] += p.Value
				}
			case metricdata.Gauge[int64]:
				breaker = data.DataPoints[0].Value
			}
		}
	}
	if counts["authorize uncertain"] != 5 || counts["reverse uncertain"] != 1 {
		t.Errorf("got the answers %v, want 5 uncertain authorizations and 1 uncertain reversal", counts)
	}
	if breaker != 1 {
		t.Errorf("the breaker gauge is %d after 5 failures, want 1", breaker)
	}
}
