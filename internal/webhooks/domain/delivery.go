// Package domain holds the rules of the webhooks service (RFC 0001, section
// 9): one delivery per event, its retries, and the signature the merchant
// checks.
package domain

import (
	"errors"
	"time"
	"uuid"
)

// The delivery policy (RFC 0001, section 9).
const (
	// Timeout bounds each POST to a merchant's endpoint.
	Timeout = 5 * time.Second
	// Lease is how long a claimed delivery belongs to its dispatcher. It is
	// longer than a POST, so a delivery is sent by one dispatcher at a time.
	Lease = 30 * time.Second
	// FirstWait and LastWait bound the wait after a failed attempt, which
	// doubles from the first to the last.
	FirstWait = 10 * time.Second
	LastWait  = time.Hour
	// RetryFor is how long a delivery is retried. After that, it is marked
	// failed, and the merchant can still query the payment.
	RetryFor = 24 * time.Hour
)

// ErrNotFound means the record does not exist.
var ErrNotFound = errors.New("not found")

// IsID reports whether s is an id as the services write them: a UUID in its
// canonical form.
func IsID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}

// Endpoint is where a merchant receives its webhooks, and the secret that
// signs them.
type Endpoint struct {
	MerchantID string
	URL        string
	Secret     string
}

// Status is where a delivery is.
type Status string

const (
	// Pending: not delivered yet, and still retried.
	Pending   Status = "pending"
	Delivered Status = "delivered"
	// Failed: retried for RetryFor without a 2xx.
	Failed Status = "failed"
)

// Delivery is one event on its way to a merchant's endpoint.
type Delivery struct {
	ID string
	// EventID is the id of the event, and of the webhook: the merchant
	// discards a webhook whose id it already processed.
	EventID    string
	EventType  string
	MerchantID string
	// Payload is the event, sent as the body of every attempt.
	Payload []byte
	// Traceparent is the trace of the payment, so the webhook joins it.
	Traceparent string
	Status      Status
	// Attempts counts the POSTs, the one in flight included. It also fences
	// the writes: only the dispatcher that made the last attempt records it.
	Attempts int
	// NextAttemptAt is when the delivery is due, while it is pending: the end
	// of the wait after a failure, or of the lease of its dispatcher.
	NextAttemptAt time.Time
	// LastStatus and LastError describe the last attempt: the HTTP status, or
	// why there was none.
	LastStatus  int
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeliveredAt time.Time
}

// NewDelivery returns the delivery of an event, due at once.
func NewDelivery(id, eventID, eventType, merchantID string, payload []byte, traceparent string, now time.Time) Delivery {
	return Delivery{
		ID: id, EventID: eventID, EventType: eventType, MerchantID: merchantID, Payload: payload,
		Traceparent: traceparent, Status: Pending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

// Attempt is what one POST to the endpoint returned.
type Attempt struct {
	// StatusCode is the HTTP status, or 0 when no answer came.
	StatusCode int
	// Error says why no answer came, such as "timeout".
	Error   string
	Latency time.Duration
}

// Succeeded reports whether the merchant accepted the webhook.
func (a Attempt) Succeeded() bool { return a.StatusCode >= 200 && a.StatusCode < 300 }

// After returns the delivery after its current attempt. A 2xx delivers it.
// Anything else waits before the next attempt, unless that attempt would come
// more than RetryFor after the event: then the delivery has failed. random
// returns a number in [0, 1), for the jitter.
func (d Delivery) After(a Attempt, now time.Time, random func() float64) Delivery {
	d.LastStatus, d.LastError, d.UpdatedAt = a.StatusCode, a.Error, now
	if a.Succeeded() {
		d.Status, d.DeliveredAt, d.NextAttemptAt = Delivered, now, time.Time{}
		return d
	}
	next := now.Add(Backoff(d.Attempts, random))
	if next.Sub(d.CreatedAt) > RetryFor {
		d.Status, d.NextAttemptAt = Failed, time.Time{}
		return d
	}
	d.NextAttemptAt = next
	return d
}

// Backoff is the wait after attempt n: it doubles from FirstWait up to
// LastWait, with half of it random, so the deliveries to an endpoint that was
// down do not all come back at once.
func Backoff(attempt int, random func() float64) time.Duration {
	base := LastWait
	// From the 9th doubling on, the wait is past the hour anyway, and a large
	// shift would overflow.
	if n := max(attempt-1, 0); n < 9 {
		base = min(FirstWait<<n, LastWait)
	}
	half := base / 2
	return half + time.Duration(random()*float64(half))
}
