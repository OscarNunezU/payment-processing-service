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
}

// ProcessTimeout is the most a single payment may take. It sits between the
// acquirer's timeout, which is shorter, and payment.DefaultLease, which is
// longer: a payment cut by this timeout is taken again when its lease expires.
const ProcessTimeout = 15 * time.Second

// maxPause caps the back-off while claims keep failing, so the worker notices
// within seconds when the database is back.
const maxPause = 10 * time.Second

// step is what a worker goroutine does after one turn.
type step int

const (
	stepProcessed   step = iota // a payment was handled: look for the next one right away
	stepIdle                    // nothing was waiting: pause
	stepClaimFailed             // the claim failed, likely the database: back off
)

type Worker struct {
	processor      Processor
	concurrency    int
	idle           time.Duration // pause when the queue is empty or the claim failed
	processTimeout time.Duration
}

func New(processor Processor, concurrency int) *Worker {
	return &Worker{
		processor:      processor,
		concurrency:    concurrency,
		idle:           500 * time.Millisecond,
		processTimeout: ProcessTimeout,
	}
}

// Run blocks until ctx ends and every payment in flight has been handled.
func (w *Worker) Run(ctx context.Context) {
	logger := logging.FromContext(ctx).With("component", "worker")
	var wg sync.WaitGroup
	for range w.concurrency {
		wg.Go(func() { w.loop(ctx, logger) })
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, logger *slog.Logger) {
	pause := w.idle
	for ctx.Err() == nil {
		switch w.processOne(ctx, logger) {
		case stepProcessed:
			pause = w.idle
			continue
		case stepIdle:
			pause = w.idle
		case stepClaimFailed:
			pause = min(pause*2, maxPause)
		}
		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
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
