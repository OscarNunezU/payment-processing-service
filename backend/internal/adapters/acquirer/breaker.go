package acquirer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/logging"
)

// Five temporary failures in a row mean the acquirer is down rather than
// having a bad moment; thirty seconds give it room to recover without leaving
// the queue stopped for long.
const (
	DefaultFailureThreshold = 5
	DefaultCooldown         = 30 * time.Second
)

var errCircuitOpen = errors.New("acquirer circuit open")

// Breaker stops calling an acquirer that keeps failing. After threshold
// temporary failures in a row it opens for cooldown: calls fail at once, and
// Wait tells the worker to stop taking payments, so they wait in the queue
// instead of spending their attempts. After the cooldown calls go through
// again, and one more failure opens it again.
type Breaker struct {
	next      payment.Acquirer
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

func NewBreaker(next payment.Acquirer, threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{next: next, threshold: threshold, cooldown: cooldown, now: time.Now}
}

func (b *Breaker) Authorize(ctx context.Context, req payment.AuthorizationRequest) (payment.AuthorizationResult, error) {
	if wait := b.Wait(); wait > 0 {
		return payment.AuthorizationResult{}, fmt.Errorf("%w for %s more", errCircuitOpen, wait.Round(time.Second))
	}
	result, err := b.next.Authorize(ctx, req)
	b.record(ctx, err)
	return result, err
}

// Wait is how long the circuit stays open: zero when calls may go through.
func (b *Breaker) Wait() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return max(0, b.openUntil.Sub(b.now()))
}

// record counts temporary failures in a row. An answer, or a refused request,
// shows the acquirer is up: it starts the count over and closes the circuit,
// even when the call had started before the circuit opened.
func (b *Breaker) record(ctx context.Context, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil || errors.Is(err, payment.ErrAcquirerRejected) {
		b.failures = 0
		b.openUntil = time.Time{}
		return
	}
	b.failures++
	if b.failures < b.threshold {
		return
	}
	b.openUntil = b.now().Add(b.cooldown)
	b.failures = b.threshold - 1 // after the cooldown, one more failure opens it again
	logging.FromContext(ctx).Warn("acquirer circuit opened: the worker stops taking payments",
		"cooldown", b.cooldown.String(), "error", err)
}
