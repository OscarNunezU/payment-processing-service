// Package worker runs the payment processor in the background: a fixed number
// of goroutines take waiting payments from the queue until told to stop.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/logging"
)

// Processor is what the worker needs from the core.
type Processor interface {
	ProcessNext(ctx context.Context) (payment.Transaction, bool, error)
	ExpirePending(ctx context.Context) (int, error)
}

// Gate tells the worker to hold off taking payments, as the acquirer's circuit
// breaker does while the acquirer keeps failing.
type Gate interface {
	Wait() time.Duration
}

// ProcessTimeout is the most a single payment may take. It sits between the
// acquirer's timeout, which is shorter, and payment.DefaultLease, which is
// longer: a payment cut by this timeout is taken again when its lease expires.
const ProcessTimeout = 15 * time.Second

// maxPause caps the back-off while claims keep failing, so the worker notices
// within seconds when the database is back.
const maxPause = 10 * time.Second

// expireEvery is how often the worker fails the payments that waited too long.
const expireEvery = 30 * time.Second

// step is what a worker goroutine does after one turn.
type step int

const (
	stepProcessed   step = iota // a payment was handled: look for the next one right away
	stepIdle                    // nothing was waiting: pause
	stepClaimFailed             // the claim failed, likely the database: back off
)

type Worker struct {
	processor      Processor
	gate           Gate
	concurrency    int
	idle           time.Duration // pause when the queue is empty or the claim failed
	processTimeout time.Duration
	expireEvery    time.Duration
}

func New(processor Processor, gate Gate, concurrency int) *Worker {
	return &Worker{
		processor:      processor,
		gate:           gate,
		concurrency:    concurrency,
		idle:           500 * time.Millisecond,
		processTimeout: ProcessTimeout,
		expireEvery:    expireEvery,
	}
}

// Run blocks until ctx ends and every payment in flight has been handled.
func (w *Worker) Run(ctx context.Context) {
	logger := logging.FromContext(ctx).With("component", "worker")
	var wg sync.WaitGroup
	for range w.concurrency {
		wg.Go(func() { w.loop(ctx, logger) })
	}
	wg.Go(func() { w.expire(ctx, logger) })
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, logger *slog.Logger) {
	pause := w.idle
	for ctx.Err() == nil {
		if wait := w.gate.Wait(); wait > 0 {
			// The acquirer keeps failing: payments wait in the queue, without
			// spending attempts, until the circuit closes.
			sleep(ctx, wait)
			continue
		}
		switch w.processOne(ctx, logger) {
		case stepProcessed:
			pause = w.idle
			continue
		case stepIdle:
			pause = w.idle
		case stepClaimFailed:
			pause = min(pause*2, maxPause)
		}
		sleep(ctx, pause)
	}
}

// expire fails, every expireEvery, the payments that waited too long without
// being sent. Every replica runs it, which is safe: a payment expires once.
func (w *Worker) expire(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(w.expireEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		n, err := w.processor.ExpirePending(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("could not expire waiting payments", "error", err)
		case n > 0:
			logger.Warn("payments expired without being sent to the acquirer", "count", n)
		}
	}
}

// sleep waits for d, or less if ctx ends first.
func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// processOne handles at most one payment. The payment runs on a context of its
// own, so stopping the worker never cuts a payment halfway: it gets up to
// processTimeout to finish.
func (w *Worker) processOne(ctx context.Context, logger *slog.Logger) step {
	start := time.Now()
	paymentCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.processTimeout)
	defer cancel()

	txn, processed, err := w.processor.ProcessNext(paymentCtx)
	switch {
	case err != nil && !processed:
		logger.Error("could not claim a payment", "error", err)
		return stepClaimFailed
	case !processed:
		return stepIdle
	case errors.Is(err, payment.ErrNotProcessing):
		logger.Warn("payment already finished by another worker", "transaction_id", txn.ID)
	case errors.Is(err, payment.ErrRetryScheduled):
		logger.Warn("payment attempt got no answer; it will be retried",
			"transaction_id", txn.ID, "attempt", txn.Attempts, "error", err)
	case err != nil:
		logger.Error("payment not finished; it will be taken again when its lease expires",
			"transaction_id", txn.ID, "error", err)
	default:
		logger.Info("payment processed",
			"transaction_id", txn.ID,
			"merchant_id", txn.MerchantID,
			"status", txn.Status,
			"reason", txn.Reason,
			"attempt", txn.Attempts,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
	return stepProcessed
}
