package app_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app/apptest"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

const merchantID = "00000000-0000-4000-8000-000000000001"

var endpoint = domain.Endpoint{MerchantID: merchantID, URL: "https://tienda-uno.test/webhooks", Secret: "whsec_test_tienda_uno_0123456789"}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	svc    *app.Service
	store  *apptest.Store
	sender *apptest.Sender
	clock  *clock
}

// newHarness has the endpoint of one merchant. The jitter is 0.5, so the
// waits after attempts 1 and 2 are 7.5 and 15 s.
func newHarness() *harness {
	h := &harness{store: apptest.NewStore(endpoint), sender: &apptest.Sender{Attempt: domain.Attempt{StatusCode: 200}},
		clock: &clock{t: time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC)}}
	h.svc = app.NewService(app.Deps{
		Store: h.store, Sender: h.sender,
		NewID:  func() string { return uuid.NewV7().String() },
		Now:    h.clock.Now,
		Random: func() float64 { return 0.5 },
		Log:    slog.New(slog.DiscardHandler),
	})
	return h
}

func event(id, merchant, typ string) app.Message {
	return app.Message{
		Payload:     fmt.Appendf(nil, `{"id":%q,"type":%q,"payment_id":"p1","merchant_id":%q,"data":{"status":"APPROVED"}}`, id, typ, merchant),
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}
}

func (h *harness) dispatchAfter(t *testing.T, d time.Duration) int {
	t.Helper()
	h.clock.advance(d)
	n, err := h.svc.Dispatch(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAFinalEventBecomesOneDelivery(t *testing.T) {
	h := newHarness()
	e := event("e1", merchantID, "payment.approved")
	for range 2 {
		if err := h.svc.Ingest(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	ds := h.store.Deliveries()
	if len(ds) != 1 {
		t.Fatalf("got %d deliveries, want 1: the event arrived twice", len(ds))
	}
	d := ds[0]
	if d.EventID != "e1" || d.EventType != "payment.approved" || d.Status != domain.Pending ||
		!d.NextAttemptAt.Equal(h.clock.Now()) || string(d.Payload) != string(e.Payload) || d.Traceparent != e.Traceparent {
		t.Errorf("got %+v, want the event pending, due now, with its body and trace", d)
	}
}

func TestEventsThatAreNotWebhooksAreSkipped(t *testing.T) {
	h := newHarness()
	for name, e := range map[string]app.Message{
		"a merchant without an endpoint": event("e1", "00000000-0000-4000-8000-000000000009", "payment.approved"),
		"a type that is not a webhook":   event("e2", merchantID, "payment.created"),
	} {
		if err := h.svc.Ingest(t.Context(), e); err != nil {
			t.Errorf("%s: got %v, want it skipped", name, err)
		}
	}
	if ds := h.store.Deliveries(); len(ds) != 0 {
		t.Errorf("got %d deliveries, want none", len(ds))
	}
}

func TestAnEventThatCannotBeReadIsReported(t *testing.T) {
	h := newHarness()
	for name, payload := range map[string]string{
		"not JSON":           `<html>`,
		"without an id":      `{"type":"payment.approved","merchant_id":"m1"}`,
		"without a merchant": `{"id":"e1","type":"payment.approved"}`,
	} {
		if err := h.svc.Ingest(t.Context(), app.Message{Payload: []byte(payload)}); !errors.Is(err, app.ErrUndecodable) {
			t.Errorf("%s: got %v, want ErrUndecodable", name, err)
		}
	}
}

func TestADatabaseErrorIsReturnedSoTheEventIsRetried(t *testing.T) {
	h := newHarness()
	h.store.SetErr(errors.New("connection refused"))
	if err := h.svc.Ingest(t.Context(), event("e1", merchantID, "payment.approved")); err == nil || errors.Is(err, app.ErrUndecodable) {
		t.Errorf("got %v, want the database's error, to be retried", err)
	}
}

// Feature: Webhooks. Scenario: The merchant's endpoint is down.
func TestAWebhookIsSignedAndSentUntilTheEndpointAcceptsIt(t *testing.T) {
	h := newHarness()
	h.sender.Script = []domain.Attempt{{StatusCode: 500}, {Error: "timeout"}}
	e := event("e1", merchantID, "payment.approved")
	if err := h.svc.Ingest(t.Context(), e); err != nil {
		t.Fatal(err)
	}

	var sentAt []time.Time
	for _, wait := range []time.Duration{0, 8 * time.Second, 16 * time.Second} {
		if n := h.dispatchAfter(t, wait); n != 1 {
			t.Fatalf("after %s: sent %d webhooks, want 1", wait, n)
		}
		sentAt = append(sentAt, h.clock.Now())
		if n := h.dispatchAfter(t, wait/2); n != 0 {
			t.Fatalf("a webhook was sent again before its wait ended")
		}
		h.clock.advance(-wait / 2)
	}

	d := h.store.Deliveries()[0]
	if d.Status != domain.Delivered || d.Attempts != 3 || d.LastStatus != 200 {
		t.Errorf("got %+v, want delivered on attempt 3", d)
	}
	for i, r := range h.sender.Requests() {
		if r.URL != endpoint.URL || r.EventID != "e1" || string(r.Body) != string(e.Payload) || r.Traceparent != e.Traceparent {
			t.Errorf("attempt %d: got %+v, want the same event to the endpoint", i+1, r)
		}
		if err := domain.Verify(endpoint.Secret, r.Signature, r.Body, sentAt[i]); err != nil {
			t.Errorf("attempt %d: the signature does not verify: %v", i+1, err)
		}
	}
}

func TestAWebhookIsGivenUpAfter24Hours(t *testing.T) {
	h := newHarness()
	h.sender.Attempt = domain.Attempt{StatusCode: 503}
	if err := h.svc.Ingest(t.Context(), event("e1", merchantID, "payment.approved")); err != nil {
		t.Fatal(err)
	}
	h.dispatchAfter(t, domain.RetryFor-10*time.Second)
	if d := h.store.Deliveries()[0]; d.Status != domain.Pending {
		t.Fatalf("got %s, want pending: the next attempt still fits in 24 h", d.Status)
	}
	h.dispatchAfter(t, 8*time.Second)
	if d := h.store.Deliveries()[0]; d.Status != domain.Failed || d.LastStatus != 503 {
		t.Errorf("got %+v, want failed after 24 h", d)
	}
}

func TestADispatcherThatWasOvertakenRecordsNothing(t *testing.T) {
	h := newHarness()
	// During the POST, another dispatcher claims the delivery again.
	h.sender.During = func(r app.Request) { h.store.Claim(r.EventID) }
	if err := h.svc.Ingest(t.Context(), event("e1", merchantID, "payment.approved")); err != nil {
		t.Fatal(err)
	}
	h.dispatchAfter(t, 0)
	if d := h.store.Deliveries()[0]; d.Status != domain.Pending || d.Attempts != 2 || d.LastStatus != 0 {
		t.Errorf("got %+v, want the delivery as the later dispatcher holds it", d)
	}
}

func TestADispatchFinishesItsPostsWhenItIsStopped(t *testing.T) {
	h := newHarness()
	ctx, cancel := context.WithCancel(t.Context())
	h.sender.During = func(app.Request) { cancel() }
	if err := h.svc.Ingest(t.Context(), event("e1", merchantID, "payment.approved")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Dispatch(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if d := h.store.Deliveries()[0]; d.Status != domain.Delivered {
		t.Errorf("got %s, want the attempt recorded although the dispatcher was stopping", d.Status)
	}
}
