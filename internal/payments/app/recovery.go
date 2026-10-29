package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Recover takes over up to limit payments whose lease expired, and moves each
// one a step forward (RFC 0001, section 5.5): another attempt, a reversal, or
// the end. It returns how many it took over, once they are all done.
//
// A claimed payment finishes its step even if ctx is canceled. The step is
// bounded by the acquirer's timeout, which is shorter than the shutdown grace,
// and finishing it records an answer the next worker would otherwise have to
// ask for again.
func (s *Service) Recover(ctx context.Context, limit int) (int, error) {
	claims, err := s.d.Payments.ClaimDue(ctx, s.now(), limit)
	if err != nil {
		return 0, err
	}
	ctx = context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	for _, c := range claims {
		wg.Go(func() {
			ctx, end := s.resumeTrace(ctx, c.Trace)
			defer end()
			s.recover(ctx, c)
		})
	}
	wg.Wait()
	return len(claims), nil
}

func (s *Service) resumeTrace(ctx context.Context, trace string) (context.Context, func()) {
	if s.d.ResumeTrace == nil {
		return ctx, func() {}
	}
	return s.d.ResumeTrace(ctx, trace, "recover payment")
}

func (s *Service) recover(ctx context.Context, c Claim) {
	p, now := c.Payment, s.now()
	switch p.NextStep(now, c.Reached) {
	case domain.StepRetry:
		s.retry(ctx, p, now)
	case domain.StepReverse:
		s.reverse(ctx, p, now)
	case domain.StepFail:
		failed := p.Unavailable(now)
		s.logWrite(ctx, p, "fail the payment", s.d.Payments.Finish(ctx, failed, *s.finalEvent(failed)))
	}
}

// retry repeats the authorization with the same key, which also asks the
// acquirer what happened to the attempts before. While the breaker is open,
// the attempt waits without being spent: the payment keeps the lease of its
// claim, so it is claimed again when that ends, and its authorization window
// keeps running.
func (s *Service) retry(ctx context.Context, p domain.Payment, now time.Time) {
	if !s.d.Breaker.Allow() {
		s.d.Log.DebugContext(ctx, "the circuit breaker holds an attempt", "payment_id", p.ID)
		return
	}
	p = p.NextAttempt()
	if err := s.d.Payments.StartAttempt(ctx, p, now); err != nil {
		s.logWrite(ctx, p, "record an attempt", err)
		return
	}
	s.record(ctx, p, s.d.Acquirer.Authorize(ctx, authorization(p)))
}

// reverse records the reversal and only then sends it, so a payment that may
// have been reversed is never authorized again. A reversal that is not
// confirmed is sent again later, with longer waits, and never given up on:
// the alert reports a payment that stays unresolved.
func (s *Service) reverse(ctx context.Context, p domain.Payment, now time.Time) {
	p = p.WithReversalStarted(now)
	if err := s.d.Payments.StartReversal(ctx, p, now); err != nil {
		s.logWrite(ctx, p, "record a reversal", err)
		return
	}
	a := s.d.Acquirer.Reverse(ctx, p.ID)
	next := p.WithReversal(a, s.now())
	s.logWrite(ctx, p, "record the answer to a reversal", s.d.Payments.RecordReversal(ctx, next, a, s.finalEvent(next)))
}

// logWrite logs a write of the recovery that failed. A stale epoch means that
// another worker took the payment over, as happens when a step outlives its
// lease. Any other error leaves the payment to the claim that follows the end
// of its lease.
func (s *Service) logWrite(ctx context.Context, p domain.Payment, what string, err error) {
	switch {
	case err == nil:
	case errors.Is(err, ErrStaleEpoch):
		s.d.Log.InfoContext(ctx, "another worker took the payment over", "payment_id", p.ID, "step", what)
	default:
		s.d.Log.ErrorContext(ctx, "could not "+what+"; the payment is retried when its lease ends",
			"payment_id", p.ID, "error", err)
	}
}

// CheckUnresolved raises the alert of INV04: it logs an error while any
// payment has been PROCESSING for longer than domain.AlertAfter. That is the
// sign of a payment the recovery cannot finish, such as one whose reversal the
// acquirer has refused for minutes, or of a recovery that is not running.
func (s *Service) CheckUnresolved(ctx context.Context) error {
	now := s.now()
	u, err := s.d.Payments.Unresolved(ctx, now.Add(-domain.AlertAfter))
	if err != nil {
		return err
	}
	if u.Count > 0 {
		s.d.Log.ErrorContext(ctx, "payments are unresolved for too long",
			"alert", "payments_unresolved", "after", domain.AlertAfter.String(), "count", u.Count,
			"oldest_payment_id", u.OldestID, "oldest_age", now.Sub(u.OldestCreatedAt).Round(time.Second).String())
	}
	return nil
}
