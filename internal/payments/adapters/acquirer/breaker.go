package acquirer

import (
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Breaker stops calls to an acquirer that keeps failing (RFC 0001, section
// 5.4). After threshold failures in a row, it opens for cooldown. Then a
// single call probes the acquirer: any other outcome closes the breaker, and a
// failure opens it again.
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	// fails tells the outcomes that count as failures; any other resets the
	// count.
	fails func(domain.Outcome) bool

	failures  int
	openUntil time.Time
	// probeSince is when the probe was let through; zero when there is none.
	probeSince time.Time
}

// NewBreaker returns a closed breaker for the API, which counts as failures
// the calls that were not sent or are uncertain. Its calls are every new
// payment, so failures in a row are about the acquirer.
func NewBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: now,
		fails: func(o domain.Outcome) bool { return !o.Definite() }}
}

// NewRecoveryBreaker returns a closed breaker for the recovery, which counts
// as failures only the calls that were not sent. The recovery calls only for
// payments whose last attempt had no definite answer, so its timeouts and
// errors are often about the payment: under load, a card the acquirer never
// answers kept a breaker that counted them open, and held the attempts of
// every other payment until their authorization windows ended (the README,
// "Proof"). A refused connection is about the acquirer, and any other outcome
// shows that it accepts connections.
func NewRecoveryBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: now,
		fails: func(o domain.Outcome) bool { return o == domain.OutcomeNotSent }}
}

// Allow reports whether a new call may go to the acquirer.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	switch {
	case b.failures < b.threshold:
		return true
	case now.Before(b.openUntil):
		return false
	// A probe whose answer never came, because its payment was never sent,
	// gives way to another after a cool-down of its own.
	case !b.probeSince.IsZero() && now.Sub(b.probeSince) < b.cooldown:
		return false
	}
	b.probeSince = now
	return true
}

// Record counts the outcome of a call.
func (b *Breaker) Record(o domain.Outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeSince = time.Time{}
	if !b.fails(o) {
		b.failures = 0
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = b.now().Add(b.cooldown)
	}
}

// Open reports whether the breaker is refusing calls now.
func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures >= b.threshold && b.now().Before(b.openUntil)
}
