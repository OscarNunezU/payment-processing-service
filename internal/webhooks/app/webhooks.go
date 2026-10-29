// Package app holds the use cases of the webhooks service (RFC 0001, sections
// 8.3 and 9): turning payment events into deliveries, and sending them.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

var (
	// ErrUndecodable means an event cannot be read: the consumer moves it to
	// the dead-letter topic instead of retrying it forever.
	ErrUndecodable = errors.New("the event cannot be decoded")
	// ErrStale means another dispatcher made a later attempt at the
	// delivery, so this one may not record its own.
	ErrStale = errors.New("the delivery was claimed again")
)

// Store keeps the endpoints and the deliveries.
type Store interface {
	// Endpoint returns the merchant's endpoint, or domain.ErrNotFound.
	Endpoint(ctx context.Context, merchantID string) (domain.Endpoint, error)
	// AddDelivery stores d, unless its event already has a delivery: then it
	// changes nothing and returns false.
	AddDelivery(ctx context.Context, d domain.Delivery) (bool, error)
	// ClaimDue takes up to limit pending deliveries that are due at now: it
	// counts their next attempt and moves them domain.Lease forward.
	// Deliveries another dispatcher is claiming are skipped.
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]Due, error)
	// RecordAttempt stores d after its attempt, if d's attempt is still the
	// last one; otherwise it returns ErrStale.
	RecordAttempt(ctx context.Context, d domain.Delivery) error
}

// Due is a delivery to send, with its endpoint.
type Due struct {
	Delivery domain.Delivery
	Endpoint domain.Endpoint
}

// Sender posts a webhook. A failed POST is an attempt with an error, not an
// error.
type Sender interface {
	Send(ctx context.Context, r Request) domain.Attempt
}

// Request is one POST of a webhook.
type Request struct {
	URL       string
	Body      []byte
	Signature string
	EventID   string
	// Traceparent is the trace of the payment, which the POST joins.
	Traceparent string
}

// Message is an event as it arrives from Kafka.
type Message struct {
	Payload     []byte
	Traceparent string
}

// Deps are what the use cases run on.
type Deps struct {
	Store  Store
	Sender Sender
	// NewID returns time-ordered ids.
	NewID func() string
	Now   func() time.Time
	// Random returns a number in [0, 1), for the jitter of the backoff.
	Random func() float64
	Log    *slog.Logger
}

// Service runs the use cases of the webhooks service.
type Service struct {
	d Deps
}

// NewService returns the use cases on top of d.
func NewService(d Deps) *Service { return &Service{d: d} }

// Ingest turns a final payment event into a delivery to its merchant's
// endpoint (RFC 0001, section 9). The event id is unique among deliveries, so
// an event that arrives twice is delivered once. An event of a merchant
// without an endpoint, or of a type that is not a webhook, is skipped.
func (s *Service) Ingest(ctx context.Context, m Message) error {
	var e events.PaymentEvent
	if err := json.Unmarshal(m.Payload, &e); err != nil {
		return fmt.Errorf("%w: %w", ErrUndecodable, err)
	}
	if e.ID == "" || e.MerchantID == "" {
		return fmt.Errorf("%w: the event has no id or no merchant", ErrUndecodable)
	}
	switch e.Type {
	case events.PaymentApproved, events.PaymentDeclined, events.PaymentFailed:
	default:
		s.d.Log.DebugContext(ctx, "an event that is not a webhook", "event_id", e.ID, "type", e.Type)
		return nil
	}

	switch _, err := s.d.Store.Endpoint(ctx, e.MerchantID); {
	case errors.Is(err, domain.ErrNotFound):
		s.d.Log.DebugContext(ctx, "the merchant has no endpoint", "event_id", e.ID, "merchant_id", e.MerchantID)
		return nil
	case err != nil:
		return err
	}
	d := domain.NewDelivery(s.d.NewID(), e.ID, string(e.Type), e.MerchantID, m.Payload, m.Traceparent, s.now())
	created, err := s.d.Store.AddDelivery(ctx, d)
	if err != nil {
		return err
	}
	if !created {
		s.d.Log.DebugContext(ctx, "the event already has a delivery", "event_id", e.ID)
	}
	return nil
}

// Dispatch sends up to limit deliveries that are due, and records each
// attempt. It returns how many it sent, once they are all done.
//
// A claimed delivery finishes its attempt even if ctx is canceled: the POST
// is bounded by domain.Timeout, which is shorter than the shutdown grace.
func (s *Service) Dispatch(ctx context.Context, limit int) (int, error) {
	due, err := s.d.Store.ClaimDue(ctx, s.now(), limit)
	if err != nil {
		return 0, err
	}
	ctx = context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	for _, d := range due {
		wg.Go(func() { s.deliver(ctx, d) })
	}
	wg.Wait()
	return len(due), nil
}

// deliver signs the webhook when it is sent, so its timestamp is fresh on
// every attempt, and the body is the same event each time.
func (s *Service) deliver(ctx context.Context, due Due) {
	d := due.Delivery
	a := s.d.Sender.Send(ctx, Request{
		URL:         due.Endpoint.URL,
		Body:        d.Payload,
		Signature:   domain.Sign(due.Endpoint.Secret, d.Payload, s.now()),
		EventID:     d.EventID,
		Traceparent: d.Traceparent,
	})
	next := d.After(a, s.now(), s.d.Random)
	log := s.d.Log.With("delivery_id", d.ID, "event_id", d.EventID, "attempt", d.Attempts)
	switch err := s.d.Store.RecordAttempt(ctx, next); {
	case errors.Is(err, ErrStale):
		log.InfoContext(ctx, "another dispatcher made a later attempt")
	case err != nil:
		log.ErrorContext(ctx, "could not record a webhook attempt; it is sent again when its lease ends", "error", err)
	case next.Status == domain.Failed:
		log.WarnContext(ctx, "a webhook was given up after "+domain.RetryFor.String(),
			"status", a.StatusCode, "error", a.Error)
	case next.Status == domain.Pending:
		log.InfoContext(ctx, "a webhook was not accepted; it will be sent again",
			"status", a.StatusCode, "error", a.Error, "next_attempt_at", next.NextAttemptAt)
	}
}

// now is the time in UTC, to the microsecond, as Postgres keeps it.
func (s *Service) now() time.Time { return s.d.Now().UTC().Truncate(time.Microsecond) }
