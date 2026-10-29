package domain

import (
	"testing"
	"time"
)

func TestTheRecoveryRetriesReversesOrFails(t *testing.T) {
	created := NewPayment("p1", activeMerchant(), validRequest(), now)
	withAttempts := func(n int) Payment {
		p := created
		p.Attempts = n
		return p
	}
	reversing := created.WithReversalStarted(now.Add(30 * time.Second))
	inWindow := now.Add(30 * time.Second)
	pastWindow := now.Add(AuthorizationWindow)

	tests := []struct {
		name    string
		p       Payment
		at      time.Time
		reached bool
		want    Step
	}{
		{"attempts left, inside the window", withAttempts(1), inWindow, true, StepRetry},
		{"attempts left, nothing sent yet", withAttempts(2), inWindow, false, StepRetry},
		{"attempts run out after reaching the acquirer", withAttempts(MaxAttempts), inWindow, true, StepReverse},
		{"attempts run out, all refused before being sent", withAttempts(MaxAttempts), inWindow, false, StepFail},
		{"the window closed with attempts left", withAttempts(1), pastWindow, true, StepReverse},
		{"the window closed, nothing sent", withAttempts(1), pastWindow, false, StepFail},
		{"a reversal started, with attempts left", reversing, inWindow, true, StepReverse},
	}
	for _, tt := range tests {
		if got := tt.p.NextStep(tt.at, tt.reached); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestAReversalStartsOnce(t *testing.T) {
	first := now.Add(time.Minute)
	p := NewPayment("p1", activeMerchant(), validRequest(), now).WithReversalStarted(first)
	if p = p.WithReversalStarted(first.Add(10 * time.Second)); !p.ReversalStartedAt.Equal(first) {
		t.Errorf("the reversal started at %s, want %s: a reversal sent again does not restart it", p.ReversalStartedAt, first)
	}
}

func TestAConfirmedReversalFailsThePayment(t *testing.T) {
	p := NewPayment("p1", activeMerchant(), validRequest(), now).WithReversalStarted(now)
	got := p.WithReversal(Answer{Outcome: OutcomeReversed}, now.Add(time.Second))
	if got.Status != PaymentFailed || got.Reason != ReasonAcquirerUnavailable || !got.LeaseUntil.IsZero() {
		t.Errorf("got %+v, want FAILED acquirer_unavailable with no lease", got)
	}
}

func TestAReversalThatFailsIsSentAgainLater(t *testing.T) {
	p := NewPayment("p1", activeMerchant(), validRequest(), now).WithReversalStarted(now)
	at := now.Add(20 * time.Second)
	got := p.WithReversal(Answer{Outcome: OutcomeUncertain}, at)
	if got.Status != PaymentProcessing || !got.LeaseUntil.Equal(at.Add(20*time.Second)) {
		t.Errorf("got %s with lease %s, want PROCESSING until %s", got.Status, got.LeaseUntil, at.Add(20*time.Second))
	}
}

func TestReversalWaitsDoubleUpToAMinute(t *testing.T) {
	// Each reversal is sent when the wait before it ends, so the waits go 5,
	// 5, 10, 20 and 40 s, and then stay at a minute.
	started, at := now, now
	var waits []time.Duration
	for range 7 {
		wait := ReversalWait(started, at)
		waits = append(waits, wait)
		at = at.Add(wait)
	}
	want := []time.Duration{5, 5, 10, 20, 40, 60, 60}
	for i := range want {
		if waits[i] != want[i]*time.Second {
			t.Fatalf("got waits %v, want %v seconds", waits, want)
		}
	}
}
