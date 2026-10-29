package domain

import (
	"testing"
	"time"
)

func TestBackoffDoublesWithHalfOfItRandom(t *testing.T) {
	tests := []struct {
		attempt  int
		min, max time.Duration
	}{
		{1, time.Second, 2 * time.Second},
		{2, 2 * time.Second, 4 * time.Second},
		{3, 4 * time.Second, 8 * time.Second},
	}
	for _, tt := range tests {
		if got := Backoff(tt.attempt, func() float64 { return 0 }); got != tt.min {
			t.Errorf("attempt %d, no jitter: got %s, want %s", tt.attempt, got, tt.min)
		}
		if got := Backoff(tt.attempt, func() float64 { return 0.999999 }); got < tt.min || got >= tt.max {
			t.Errorf("attempt %d, most jitter: got %s, want under %s", tt.attempt, got, tt.max)
		}
	}
}

func TestOnlyDefiniteOutcomesEndAPayment(t *testing.T) {
	for o, want := range map[Outcome]bool{
		OutcomeApproved: true, OutcomeDeclined: true, OutcomeRejected: true, OutcomeReversed: true,
		OutcomeNotSent: false, OutcomeUncertain: false,
	} {
		if o.Definite() != want {
			t.Errorf("%s.Definite() = %v, want %v", o, o.Definite(), want)
		}
	}
}
