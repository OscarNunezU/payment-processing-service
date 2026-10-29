package domain

import "time"

// The limits of the recovery (RFC 0001, sections 5.3, 5.5 and 5.6).
const (
	// FirstReversalWait and LastReversalWait bound the wait after a reversal
	// that was not confirmed.
	FirstReversalWait = 5 * time.Second
	LastReversalWait  = time.Minute
	// AlertAfter is how long a payment may stay PROCESSING before the
	// recovery raises an alert (INV04).
	AlertAfter = 5 * time.Minute
)

// Step is what the recovery does with a payment it took over.
type Step string

const (
	// StepRetry repeats the authorization, with the same key.
	StepRetry Step = "retry"
	// StepReverse sends the reversal, or sends it again.
	StepReverse Step = "reverse"
	// StepFail ends the payment FAILED: no attempt reached the acquirer, so
	// there is nothing to reverse.
	StepFail Step = "fail"
)

// NextStep decides what the recovery does with p, a payment whose lease
// expired (RFC 0001, sections 5.3 and 5.5). reached says whether any of its
// attempts may have reached the acquirer: it is false only when every one was
// refused before it was sent.
//
// Once a reversal started, the payment is only reversed: an authorization
// sent after it would be rejected anyway. Otherwise, a payment with attempts
// left, inside its authorization window, gets another one. A payment with
// neither is reversed if anything may have reached the acquirer, and fails at
// once if nothing did.
func (p Payment) NextStep(now time.Time, reached bool) Step {
	switch {
	case !p.ReversalStartedAt.IsZero():
		return StepReverse
	case p.Attempts < MaxAttempts && now.Before(p.CreatedAt.Add(AuthorizationWindow)):
		return StepRetry
	case reached:
		return StepReverse
	default:
		return StepFail
	}
}

// NextAttempt returns p with one more attempt, as it is recorded before the
// call.
func (p Payment) NextAttempt() Payment {
	p.Attempts++
	return p
}

// WithReversalStarted returns p as it is recorded before a reversal is sent.
// The first one marks when the reversal started.
func (p Payment) WithReversalStarted(now time.Time) Payment {
	if p.ReversalStartedAt.IsZero() {
		p.ReversalStartedAt = now
	}
	return p
}

// WithReversal returns p after the answer to its reversal. A confirmation
// makes it FAILED, since nothing is left authorized. Any other answer keeps it
// PROCESSING, and moves its lease to the end of the wait before the reversal
// is sent again.
func (p Payment) WithReversal(a Answer, now time.Time) Payment {
	if a.Outcome == OutcomeReversed {
		return p.Unavailable(now)
	}
	p.UpdatedAt = now
	p.LeaseUntil = now.Add(ReversalWait(p.ReversalStartedAt, now))
	return p
}

// Unavailable returns p FAILED with reason acquirer_unavailable: no attempt
// got a definite answer, and nothing is left authorized at the acquirer.
func (p Payment) Unavailable(now time.Time) Payment {
	p.Status, p.Reason = PaymentFailed, ReasonAcquirerUnavailable
	p.LeaseUntil = time.Time{}
	p.UpdatedAt = now
	return p
}

// ReversalWait is the wait after a reversal that was not confirmed: the time
// since the reversal started, between 5 s and 1 minute. Each wait is then
// about as long as all the ones before it together, so the waits double from
// 5 s until they reach a minute, without a counter.
func ReversalWait(started, now time.Time) time.Duration {
	return min(max(now.Sub(started), FirstReversalWait), LastReversalWait)
}
