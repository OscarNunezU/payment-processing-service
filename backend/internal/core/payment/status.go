package payment

import "fmt"

type Status string

const (
	StatusPending    Status = "PENDING"
	StatusProcessing Status = "PROCESSING"
	StatusApproved   Status = "APPROVED"
	StatusDeclined   Status = "DECLINED"
	StatusFailed     Status = "FAILED"
)

// IsFinal reports whether the status can no longer change.
func (s Status) IsFinal() bool {
	switch s {
	case StatusApproved, StatusDeclined, StatusFailed:
		return true
	}
	return false
}

// CanTransitionTo enforces the lifecycle:
// PENDING → PROCESSING | FAILED (expired), PROCESSING → APPROVED | DECLINED | FAILED.
func (s Status) CanTransitionTo(next Status) bool {
	switch s {
	case StatusPending:
		return next == StatusProcessing || next == StatusFailed
	case StatusProcessing:
		return next == StatusApproved || next == StatusDeclined || next == StatusFailed
	default:
		return false
	}
}

func ParseStatus(s string) (Status, error) {
	st := Status(s)
	switch st {
	case StatusPending, StatusProcessing, StatusApproved, StatusDeclined, StatusFailed:
		return st, nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidStatus, s)
}
