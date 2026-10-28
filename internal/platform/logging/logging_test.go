package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func decode(t *testing.T, b *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	if err := json.Unmarshal(b.Bytes(), &line); err != nil {
		t.Fatalf("the log line is not JSON: %v\n%s", err, b)
	}
	return line
}

func TestLinesCarryTheServiceAndTheTrace(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "payments-api", slog.LevelInfo)

	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "op")
	defer span.End()
	log.InfoContext(ctx, "hello", "payment_id", "pay_1")

	line := decode(t, &buf)
	if line["service"] != "payments-api" {
		t.Errorf("service = %v, want payments-api", line["service"])
	}
	if line["trace_id"] != span.SpanContext().TraceID().String() {
		t.Errorf("trace_id = %v, want %s", line["trace_id"], span.SpanContext().TraceID())
	}
	if line["span_id"] != span.SpanContext().SpanID().String() {
		t.Errorf("span_id = %v, want %s", line["span_id"], span.SpanContext().SpanID())
	}
	if line["payment_id"] != "pay_1" {
		t.Errorf("payment_id = %v, want pay_1", line["payment_id"])
	}
}

func TestLinesWithoutASpanHaveNoTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "payments-api", slog.LevelInfo).With("k", "v").InfoContext(context.Background(), "hello")

	line := decode(t, &buf)
	if _, ok := line["trace_id"]; ok {
		t.Errorf("trace_id = %v, want none", line["trace_id"])
	}
	if line["k"] != "v" {
		t.Errorf("an attribute added with With was lost: %v", line)
	}
}

func TestLevelFiltersLines(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "payments-api", slog.LevelWarn).Info("not shown")
	if buf.Len() != 0 {
		t.Errorf("an info line was written at warn level: %s", buf.String())
	}
}
