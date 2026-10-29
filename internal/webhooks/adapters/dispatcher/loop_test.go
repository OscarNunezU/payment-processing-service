package dispatcher

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// dispatcher sends the batches in due, one per call, and then nothing.
type dispatcher struct {
	mu     sync.Mutex
	due    []int
	err    error
	limits []int
}

func (d *dispatcher) Dispatch(_ context.Context, limit int) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.limits = append(d.limits, limit)
	if d.err != nil {
		return 0, d.err
	}
	if len(d.due) == 0 {
		return 0, nil
	}
	n := d.due[0]
	d.due = d.due[1:]
	return n, nil
}

func (d *dispatcher) calls() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]int(nil), d.limits...)
}

func newLoop(uc Dispatcher) *Loop {
	l := New(uc, slog.New(slog.DiscardHandler))
	l.Batch, l.Interval = 2, 10*time.Millisecond
	return l
}

func TestAScanDrainsTheBacklogBatchAfterBatch(t *testing.T) {
	uc := &dispatcher{due: []int{2, 2, 1}}
	newLoop(uc).scan(t.Context())
	if limits := uc.calls(); len(limits) != 3 {
		t.Errorf("one scan made the claims %v, want 3: two full batches, then a short one", limits)
	}
}

func TestAFailedClaimWaitsForTheNextScan(t *testing.T) {
	uc := &dispatcher{err: errors.New("connection refused")}
	newLoop(uc).scan(t.Context())
	if limits := uc.calls(); len(limits) != 1 {
		t.Errorf("a failed claim was repeated %d times in the same scan", len(limits))
	}
}

func TestTheLoopScansUntilItIsStopped(t *testing.T) {
	uc := &dispatcher{}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { newLoop(uc).Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop with its context")
	}
	if n := len(uc.calls()); n < 2 {
		t.Errorf("got %d scans in 100 ms, want one every 10 ms", n)
	}
}
