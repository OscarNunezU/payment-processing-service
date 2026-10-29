// Package dispatcher runs the dispatch of webhooks on a timer: every second
// it sends, in batches, the deliveries that are due (RFC 0001, section 9).
package dispatcher

import (
	"context"
	"log/slog"
	"time"
)

// Dispatcher is the use case the loop runs.
type Dispatcher interface {
	Dispatch(ctx context.Context, limit int) (int, error)
}

// Loop runs the dispatch until its context ends.
type Loop struct {
	uc  Dispatcher
	log *slog.Logger
	// Batch is how many deliveries a claim takes, and Interval the wait
	// between scans.
	Batch    int
	Interval time.Duration
}

// New returns a loop that runs uc.
func New(uc Dispatcher, log *slog.Logger) *Loop {
	return &Loop{uc: uc, log: log, Batch: 100, Interval: time.Second}
}

// Run scans until ctx ends. A scan in progress finishes first, so the
// deliveries it claimed do not wait for their lease to end.
func (l *Loop) Run(ctx context.Context) {
	t := time.NewTicker(l.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.scan(ctx)
		}
	}
}

// scan claims batches until one comes back short, so that a backlog drains
// without a wait between its batches.
func (l *Loop) scan(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := l.uc.Dispatch(ctx, l.Batch)
		if err != nil {
			if ctx.Err() == nil {
				l.log.WarnContext(ctx, "could not claim the webhooks due; it will try again", "error", err)
			}
			return
		}
		if n < l.Batch {
			return
		}
	}
}
