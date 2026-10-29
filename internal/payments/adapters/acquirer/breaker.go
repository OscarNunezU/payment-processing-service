package acquirer

import (
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Breaker stops calls to an acquirer that keeps failing (RFC 0001, section
// 5.4). After threshold answers in a row that are not sent or uncertain, it
// opens for cooldown. Then a single call probes the acquirer: a definite
// answer closes the breaker, and a failure opens it again.
type Breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	failures  int
	openUntil time.Time
	// probeSince is when the probe was let through; zero when there is none.
	probeSince time.Time
}

// NewBreaker returns a closed breaker.
func NewBreaker(threshold int, cooldown time.Duration, now func() time.Time) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: now}
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
	if o.Definite() {
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
