package sender

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
)

const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func request(url string) app.Request {
	return app.Request{URL: url, Body: []byte(`{"id":"e1"}`), Signature: "t=1,v1=abc", EventID: "e1", Traceparent: traceparent}
}

func TestAWebhookCarriesItsIdSignatureAndTrace(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	var got struct {
		method, contentType, id, signature, trace, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.method, got.contentType, got.body = r.Method, r.Header.Get("Content-Type"), string(body)
		got.id, got.signature, got.trace = r.Header.Get("Webhook-Id"), r.Header.Get("Webhook-Signature"), r.Header.Get("traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	a := New(time.Second).Send(t.Context(), request(srv.URL))
	if a.StatusCode != http.StatusNoContent || a.Error != "" || a.Latency <= 0 {
		t.Errorf("got %+v, want 204 with its latency", a)
	}
	if got.method != http.MethodPost || got.contentType != "application/json" || got.body != `{"id":"e1"}` ||
		got.id != "e1" || got.signature != "t=1,v1=abc" {
		t.Errorf("the endpoint got %+v", got)
	}
	if !strings.Contains(got.trace, "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Errorf("the webhook has trace %q, want the payment's trace", got.trace)
	}
	// The POST is also recorded, in the payment's trace, not only passed on.
	exported := spans.GetSpans()
	if len(exported) != 1 || exported[0].SpanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("exported %d spans, want the POST's, in the payment's trace", len(exported))
	}
}

func TestFailuresAreAttemptsWithoutAStatus(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	if a := New(50*time.Millisecond).Send(t.Context(), request(slow.URL)); a.StatusCode != 0 || a.Error != "timeout" {
		t.Errorf("a slow endpoint: got %+v, want a timeout", a)
	}

	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	if a := New(time.Second).Send(t.Context(), request("http://"+addr)); a.StatusCode != 0 || a.Error != "connection_refused" {
		t.Errorf("an endpoint that is down: got %+v, want connection_refused", a)
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer redirect.Close()

	if a := New(time.Second).Send(t.Context(), request(redirect.URL)); a.StatusCode != http.StatusFound || followed {
		t.Errorf("got %+v, followed=%v, want the 302 as the answer", a, followed)
	}
}
