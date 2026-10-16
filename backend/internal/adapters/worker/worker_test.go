package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

// noExpiry is embedded by the processors below that have nothing to expire.
type noExpiry struct{}

func (noExpiry) ExpirePending(context.Context) (int, error) { return 0, nil }

// gateAt is a gate that always asks to wait d; gateAt(0) never holds the worker.
type gateAt time.Duration

func (g gateAt) Wait() time.Duration { return time.Duration(g) }

// queueProcessor pretends there are `waiting` payments and processes them one
// per call; it is safe for several goroutines.
type queueProcessor struct {
	noExpiry
	waiting   atomic.Int32
	processed atomic.Int32
	drained   chan struct{}
	once      sync.Once
}

func (q *queueProcessor) ProcessNext(context.Context) (payment.Transaction, bool, error) {
	if q.waiting.Add(-1) < 0 {
		q.once.Do(func() { close(q.drained) })
		return payment.Transaction{}, false, nil
	}
	q.processed.Add(1)
	return payment.Transaction{ID: "txn", Status: payment.StatusApproved}, true, nil
}

func runInBackground(w *Worker, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return done
}

func waitOrFail(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestWorkerProcessesEverythingWaitingAndStops(t *testing.T) {
	q := &queueProcessor{drained: make(chan struct{})}
	q.waiting.Store(20)
	w := New(q, gateAt(0), 3)
	w.idle = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())

	done := runInBackground(w, ctx)
	waitOrFail(t, q.drained, "the queue to drain")
	cancel()
	waitOrFail(t, done, "Run to return after cancel")

	if got := q.processed.Load(); got != 20 {
		t.Fatalf("processed %d payments, want 20", got)
	}
}

// blockingProcessor holds one payment until released, to look at the context
// it was given while the worker is being stopped.
type blockingProcessor struct {
	noExpiry
	started  chan struct{}
	release  chan struct{}
	ctxErr   error
	finished atomic.Bool
}

func (b *blockingProcessor) ProcessNext(ctx context.Context) (payment.Transaction, bool, error) {
	if b.finished.Load() {
		return payment.Transaction{}, false, nil
	}
	close(b.started)
	<-b.release
	b.ctxErr = ctx.Err()
	b.finished.Store(true)
	return payment.Transaction{ID: "txn", Status: payment.StatusApproved}, true, nil
}

func TestStoppingTheWorkerDoesNotCutAPaymentInFlight(t *testing.T) {
	b := &blockingProcessor{started: make(chan struct{}), release: make(chan struct{})}
	w := New(b, gateAt(0), 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := runInBackground(w, ctx)
	waitOrFail(t, b.started, "the payment to start")
	cancel() // stop the worker while the payment is in flight
	close(b.release)
	waitOrFail(t, done, "Run to return")

	if b.ctxErr != nil {
		t.Fatalf("the payment's context was %v when it finished; stopping the worker must not cancel it", b.ctxErr)
	}
}

// failingProcessor always fails to claim, as when the database is down.
type failingProcessor struct {
	noExpiry
	calls atomic.Int32
}

func (f *failingProcessor) ProcessNext(context.Context) (payment.Transaction, bool, error) {
	f.calls.Add(1)
	return payment.Transaction{}, false, errors.New("database down")
}

// With a 20ms base pause that doubles on each failure (40, 80, 160ms...), 200ms
// fit about 3 attempts; without the back-off there would be about 10.
func TestAFailingClaimBacksOff(t *testing.T) {
	f := &failingProcessor{}
	w := New(f, gateAt(0), 1)
	w.idle = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w.Run(ctx)

	if calls := f.calls.Load(); calls < 2 || calls > 4 {
		t.Fatalf("%d claim attempts in 200ms, want 2 to 4: the pause must double while claims fail", calls)
	}
}

// While the acquirer's circuit is open the worker claims nothing: the payments
// wait in the queue instead of spending their attempts.
func TestTheWorkerTakesNoPaymentsWhileTheGateHoldsIt(t *testing.T) {
	f := &failingProcessor{}
	w := New(f, gateAt(time.Hour), 2)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	w.Run(ctx)

	if calls := f.calls.Load(); calls != 0 {
		t.Fatalf("%d claims while the gate held the worker, want 0", calls)
	}
}

// expiringProcessor has nothing to process and counts the expiry runs.
type expiringProcessor struct {
	runs atomic.Int32
}

func (e *expiringProcessor) ProcessNext(context.Context) (payment.Transaction, bool, error) {
	return payment.Transaction{}, false, nil
}

func (e *expiringProcessor) ExpirePending(context.Context) (int, error) {
	e.runs.Add(1)
	return 0, nil
}

func TestTheWorkerExpiresWaitingPaymentsPeriodically(t *testing.T) {
	e := &expiringProcessor{}
	w := New(e, gateAt(0), 1)
	w.expireEvery = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	w.Run(ctx)

	if runs := e.runs.Load(); runs < 3 {
		t.Fatalf("expiry ran %d times in 100ms every 10ms, want at least 3", runs)
	}
}
