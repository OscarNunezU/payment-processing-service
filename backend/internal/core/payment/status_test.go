package payment

import (
	"errors"
	"testing"
)

func TestIsFinal(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusPending, false},
		{StatusProcessing, false},
		{StatusApproved, true},
		{StatusDeclined, true},
		{StatusFailed, true},
	}
	for _, tt := range tests {
		if got := tt.status.IsFinal(); got != tt.want {
			t.Errorf("%s.IsFinal() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestCanTransitionTo(t *testing.T) {
	tests := []struct {
		from, to Status
		want     bool
	}{
		{StatusPending, StatusProcessing, true},
		{StatusPending, StatusFailed, true},
		{StatusProcessing, StatusApproved, true},
		{StatusProcessing, StatusDeclined, true},
		{StatusProcessing, StatusFailed, true},

		{StatusPending, StatusApproved, false},
		{StatusProcessing, StatusPending, false},
		{StatusApproved, StatusFailed, false},
		{StatusDeclined, StatusApproved, false},
		{StatusFailed, StatusProcessing, false},
	}
	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Errorf("%s → %s = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	got, err := ParseStatus("PENDING")
	if err != nil || got != StatusPending {
		t.Fatalf("ParseStatus(%q) = %q, %v; want %q, nil", "PENDING", got, err, StatusPending)
	}

	for _, in := range []string{"PAID", "approved", ""} {
		if _, err := ParseStatus(in); !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("ParseStatus(%q) error = %v, want ErrInvalidStatus", in, err)
		}
	}
}
