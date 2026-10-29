package domain

import "time"

// The time budget of an authorization (RFC 0001, section 5.6).
const (
	// AcquirerTimeout bounds each call to the acquirer.
	AcquirerTimeout = 5 * time.Second
	// MaxAcquirerTimeout is the longest the timeout may be configured. The
	// lease must cover the call and the writes before and after it, each
	// bounded by the database's statement timeout, so the call gets at most
	// two thirds of the lease.
	MaxAcquirerTimeout = 10 * time.Second
	// Lease is how long a payment belongs to whoever is calling the acquirer
	// for it. It is longer than a call, so a call always ends before its
	// lease does, and no one else calls for the payment meanwhile.
	Lease = 15 * time.Second
	// MaxAttempts is how many calls a payment gets without a definite answer.
	MaxAttempts = 3
	// AuthorizationWindow is how long after its creation a payment may still
	// be authorized, so that a customer who left the checkout is never
	// charged late.
	AuthorizationWindow = 2 * time.Minute
)

// Outcome classifies an answer from the acquirer (RFC 0001, section 5.3).
type Outcome string

const (
	// OutcomeApproved and OutcomeDeclined are the issuer's decision.
	OutcomeApproved Outcome = "approved"
	OutcomeDeclined Outcome = "declined"
	// OutcomeRejected: the acquirer refused the request itself, so sending it
	// again would get the same answer.
	OutcomeRejected Outcome = "rejected"
	// OutcomeReversed: the key was reversed, so it can never be authorized.
	OutcomeReversed Outcome = "reversed"
	// OutcomeNotSent: the request certainly never reached the acquirer.
	OutcomeNotSent Outcome = "not_sent"
	// OutcomeUncertain: the request may have reached the acquirer, and its
	// answer is unknown.
	OutcomeUncertain Outcome = "uncertain"
)

// Definite reports whether the outcome ends the payment.
func (o Outcome) Definite() bool {
	switch o {
	case OutcomeApproved, OutcomeDeclined, OutcomeRejected, OutcomeReversed:
		return true
	}
	return false
}

// Answer is what one call to the acquirer returned.
type Answer struct {
	Outcome           Outcome
	AuthorizationCode string
	DeclineCode       string
	CardBrand         string
	CardLast4         string
	// ResponseCode is what the acquirer or the network said, for the
	// history: an HTTP status, an error code, or a description such as
	// "timeout".
	ResponseCode string
	Latency      time.Duration
}

// WithAnswer returns the payment after the answer to its current attempt. A
// definite answer makes it final. Any other keeps it PROCESSING and moves its
// lease to the end of the wait before the next attempt, so the recovery picks
// it up then. random returns a number in [0, 1), for the jitter.
func (p Payment) WithAnswer(a Answer, now time.Time, random func() float64) Payment {
	p.UpdatedAt = now
	switch a.Outcome {
	case OutcomeApproved:
		p.Status, p.AuthorizationCode = PaymentApproved, a.AuthorizationCode
	case OutcomeDeclined:
		p.Status, p.DeclineCode = PaymentDeclined, a.DeclineCode
	case OutcomeRejected:
		p.Status, p.Reason = PaymentFailed, ReasonAcquirerRejected
	case OutcomeReversed:
		// Only a reversal leaves a key reversed, and a reversal releases
		// whatever the key held.
		p.Status, p.Reason = PaymentFailed, ReasonAcquirerUnavailable
	default:
		p.LeaseUntil = now.Add(Backoff(p.Attempts, random))
		return p
	}
	if a.CardLast4 != "" {
		p.CardBrand, p.CardLast4 = a.CardBrand, a.CardLast4
	}
	p.LeaseUntil = time.Time{}
	return p
}

// Backoff is the wait after attempt n before the next one: it doubles from a
// 2 s base, with half of it random, so payments that failed together do not
// retry together. Attempt 1 waits 1–2 s, attempt 2 waits 2–4 s.
func Backoff(attempt int, random func() float64) time.Duration {
	base := 2 * time.Second << max(attempt-1, 0)
	half := base / 2
	return half + time.Duration(random()*float64(half))
}
