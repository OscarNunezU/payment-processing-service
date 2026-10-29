package recovery

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// useCases claims the batches in due, one per call, and then nothing.
type useCases struct {
	mu     sync.Mutex
	due    []int
	err    error
	limits []int
	checks int
}

func (u *useCases) Recover(_ context.Context, limit int) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.limits = append(u.limits, limit)
	if u.err != nil {
		return 0, u.err
	}
	if len(u.due) == 0 {
		return 0, nil
	}
	n := u.due[0]
	u.due = u.due[1:]
	return n, nil
}

func (u *useCases) CheckUnresolved(context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checks++
	return nil
}

func (u *useCases) calls() ([]int, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int(nil), u.limits...), u.checks
}

func newLoop(uc UseCases) *Loop {
	l := New(uc, slog.New(slog.DiscardHandler))
	l.Batch, l.Interval, l.AlertInterval = 2, 10*time.Millisecond, 10*time.Millisecond
	return l
}

// run runs l for d and waits until it returns.
func run(l *Loop, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	l.Run(ctx)
}

func TestAScanDrainsTheBacklogBatchAfterBatch(t *testing.T) {
	uc := &useCases{due: []int{2, 2, 1}}
	newLoop(uc).scan(t.Context())
	if limits, _ := uc.calls(); len(limits) != 3 || limits[0] != 2 {
		t.Errorf("one scan made the claims %v, want 3 of up to 2: two full batches, then a short one", limits)
	}
}

func TestTheLoopScansAndChecksUntilItIsStopped(t *testing.T) {
	uc := &useCases{}
	done := make(chan struct{})
	go func() {
		run(newLoop(uc), 100*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop with its context")
	}
	if limits, checks := uc.calls(); len(limits) < 2 || checks < 2 {
		t.Errorf("got %d scans and %d checks in 100 ms, want them every 10 ms", len(limits), checks)
	}
}

func TestAFailedClaimWaitsForTheNextScan(t *testing.T) {
	uc := &useCases{err: errors.New("connection refused")}
	newLoop(uc).scan(t.Context())
	if limits, _ := uc.calls(); len(limits) != 1 {
		t.Errorf("a failed claim was repeated %d times in the same scan", len(limits))
	}
}
