package acquirer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

// stubAcquirer answers with err, or approves when err is nil, and counts calls.
type stubAcquirer struct {
	err   error
	calls int
}

func (s *stubAcquirer) Authorize(context.Context, payment.AuthorizationRequest) (payment.AuthorizationResult, error) {
	s.calls++
	if s.err != nil {
		return payment.AuthorizationResult{}, s.err
	}
	return payment.AuthorizationResult{Approved: true}, nil
}

// newTestBreaker returns a breaker with a clock the test moves by hand.
func newTestBreaker(next payment.Acquirer) (*Breaker, *time.Time) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	b := NewBreaker(next, 3, 30*time.Second)
	b.now = func() time.Time { return now }
	return b, &now
}

func callTimes(b *Breaker, n int) {
	for range n {
		b.Authorize(context.Background(), request)
	}
}

func TestTheBreakerOpensAfterFailuresInARow(t *testing.T) {
	stub := &stubAcquirer{err: errors.New("acquirer answered 503")}
	b, _ := newTestBreaker(stub)

	callTimes(b, 3)
	if b.Wait() != 30*time.Second {
		t.Fatalf("Wait() = %v after 3 failures, want the 30s cooldown", b.Wait())
	}

	_, err := b.Authorize(context.Background(), request)
	if !errors.Is(err, errCircuitOpen) || stub.calls != 3 {
		t.Fatalf("call while open = %v with %d acquirer calls, want errCircuitOpen without calling it", err, stub.calls)
	}
	if errors.Is(err, payment.ErrAcquirerRejected) {
		t.Fatal("an open circuit must be a temporary error, so the payment is retried")
	}

	// A call that started before the circuit opened gets an answer: the
	// acquirer is up after all.
	b.record(context.Background(), nil)
	if b.Wait() != 0 {
		t.Fatalf("Wait() = %v after an answer, want 0: an answer closes the circuit", b.Wait())
	}
}

func TestAnAnswerStartsTheCountOver(t *testing.T) {
	answers := []error{nil, fmt.Errorf("%w (status 422)", payment.ErrAcquirerRejected)}
	for _, answer := range answers {
		stub := &stubAcquirer{err: errors.New("acquirer answered 503")}
		b, _ := newTestBreaker(stub)

		callTimes(b, 2)
		stub.err = answer
		callTimes(b, 1)
		stub.err = errors.New("acquirer answered 503")
		callTimes(b, 2)

		if b.Wait() != 0 {
			t.Errorf("after 2 failures, an answer (%v) and 2 failures the circuit is open; want it closed", answer)
		}
	}
}

func TestAfterTheCooldownOneMoreFailureOpensItAgain(t *testing.T) {
	stub := &stubAcquirer{err: errors.New("acquirer answered 503")}
	b, now := newTestBreaker(stub)
	callTimes(b, 3)

	*now = now.Add(30 * time.Second)
	if b.Wait() != 0 {
		t.Fatalf("Wait() = %v once the cooldown passed, want 0", b.Wait())
	}
	callTimes(b, 1)
	if stub.calls != 4 || b.Wait() != 30*time.Second {
		t.Fatalf("%d acquirer calls and Wait() = %v; want the call made and the circuit open again", stub.calls, b.Wait())
	}

	stub.err = nil
	*now = now.Add(30 * time.Second)
	callTimes(b, 1)
	stub.err = errors.New("acquirer answered 503")
	callTimes(b, 1)
	if b.Wait() != 0 {
		t.Fatalf("Wait() = %v after an answer and one failure, want 0: the answer closed it", b.Wait())
	}
}
