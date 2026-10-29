// Package sender posts webhooks to the merchants' endpoints (RFC 0001,
// section 9).
package sender

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

// Sender implements app.Sender over HTTP.
type Sender struct {
	http    *http.Client
	timeout time.Duration
	now     func() time.Time
}

// New returns a sender whose POSTs end after timeout.
func New(timeout time.Duration) *Sender {
	return &Sender{
		http: &http.Client{
			// The tracer provider is the global one, set explicitly: by
			// default otelhttp takes the provider of the span in the
			// request's context, and the span resumed from a delivery's
			// trace is a remote one, whose provider records nothing.
			Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(otel.GetTracerProvider())),
			// A redirect is not an answer: it counts as a failed attempt, and
			// the webhook never goes to an address the merchant did not set.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		timeout: timeout,
		now:     time.Now,
	}
}

// Send posts r. The POST joins the trace of the payment, so the trace runs
// from the merchant's request to its webhook.
func (s *Sender) Send(ctx context.Context, r app.Request) domain.Attempt {
	if r.Traceparent != "" {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": r.Traceparent})
	}
	start := s.now()
	a := s.send(ctx, r)
	a.Latency = s.now().Sub(start)
	return a
}

func (s *Sender) send(ctx context.Context, r app.Request) domain.Attempt {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return domain.Attempt{Error: "invalid_url"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "payments-webhooks/1")
	req.Header.Set("Webhook-Id", r.EventID)
	req.Header.Set("Webhook-Signature", r.Signature)

	resp, err := s.http.Do(req)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return domain.Attempt{Error: "timeout"}
	case errors.Is(err, syscall.ECONNREFUSED):
		return domain.Attempt{Error: "connection_refused"}
	case err != nil:
		return domain.Attempt{Error: "connection_failed"}
	}
	defer resp.Body.Close()
	// The answer's body is not used, but reading it lets the connection be
	// reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return domain.Attempt{StatusCode: resp.StatusCode}
}
