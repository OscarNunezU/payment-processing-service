// Package recovery is the inbound adapter of the recovery (ADR 0004): it runs
// the recovery's use cases on a timer inside payments-worker. Every second it
// takes over, in batches, the payments whose lease expired, and every minute
// it checks for payments unresolved for too long (RFC 0001, section 5.5).
package recovery

import (
	"context"
	"log/slog"
	"time"
)

// UseCases are the use cases the loop runs.
type UseCases interface {
	Recover(ctx context.Context, limit int) (int, error)
	CheckUnresolved(ctx context.Context) error
}

// Loop runs the recovery until its context ends.
type Loop struct {
	uc  UseCases
	log *slog.Logger
	// Batch is how many payments a claim takes. Interval is the wait between
	// scans, and AlertInterval between checks for unresolved payments.
	Batch         int
	Interval      time.Duration
	AlertInterval time.Duration
}

// New returns a loop that runs uc.
func New(uc UseCases, log *slog.Logger) *Loop {
	return &Loop{uc: uc, log: log, Batch: 100, Interval: time.Second, AlertInterval: time.Minute}
}

// Run scans until ctx ends. A scan in progress finishes first, so the
// payments it claimed do not wait for their lease to end.
func (l *Loop) Run(ctx context.Context) {
	scan := time.NewTicker(l.Interval)
	defer scan.Stop()
	alert := time.NewTicker(l.AlertInterval)
	defer alert.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-scan.C:
			l.scan(ctx)
		case <-alert.C:
			if err := l.uc.CheckUnresolved(ctx); err != nil && ctx.Err() == nil {
				l.log.WarnContext(ctx, "could not check for unresolved payments", "error", err)
			}
		}
	}
}

// scan claims batches until one comes back short, so that a backlog drains
// without a wait between its batches.
func (l *Loop) scan(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := l.uc.Recover(ctx, l.Batch)
		if err != nil {
			if ctx.Err() == nil {
				l.log.WarnContext(ctx, "the recovery could not claim payments; it will try again", "error", err)
			}
			return
		}
		if n < l.Batch {
			return
		}
	}
}
