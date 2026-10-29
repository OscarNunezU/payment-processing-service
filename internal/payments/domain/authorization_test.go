package domain

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC)

func half() float64 { return 0.5 }

// RFC 0001, section 5.6: a call, even at the longest timeout allowed, ends
// before its lease, with room for the writes around it.
func TestTheTimeBudgetChains(t *testing.T) {
	if AcquirerTimeout > MaxAcquirerTimeout || MaxAcquirerTimeout+2*2*time.Second > Lease {
		t.Errorf("timeout %s, at most %s, lease %s: the lease does not cover the call and two 2 s writes",
			AcquirerTimeout, MaxAcquirerTimeout, Lease)
	}
}

func TestANewPaymentIsRecordedBeforeItsFirstCall(t *testing.T) {
	p := NewPayment("p1", activeMerchant(), validRequest(), now)
	if p.Status != PaymentProcessing || p.Attempts != 1 || p.Epoch != 1 || !p.LeaseUntil.Equal(now.Add(Lease)) {
		t.Errorf("got %+v, want PROCESSING with attempt 1, epoch 1 and a %s lease", p, Lease)
	}
}

func TestADefiniteAnswerEndsThePayment(t *testing.T) {
	tests := []struct {
		answer Answer
		status PaymentStatus
		reason FailureReason
	}{
		{Answer{Outcome: OutcomeApproved, AuthorizationCode: "7T4N4V", CardBrand: "visa", CardLast4: "1111"}, PaymentApproved, ""},
		{Answer{Outcome: OutcomeDeclined, DeclineCode: "card_declined", CardBrand: "visa", CardLast4: "0002"}, PaymentDeclined, ""},
		{Answer{Outcome: OutcomeRejected, ResponseCode: "token_unknown"}, PaymentFailed, ReasonAcquirerRejected},
		{Answer{Outcome: OutcomeReversed}, PaymentFailed, ReasonAcquirerUnavailable},
	}
	for _, tt := range tests {
		p := NewPayment("p1", activeMerchant(), validRequest(), now).WithAnswer(tt.answer, now.Add(time.Second), half)
		if p.Status != tt.status || p.Reason != tt.reason || !p.LeaseUntil.IsZero() || !p.UpdatedAt.Equal(now.Add(time.Second)) {
			t.Errorf("%s: got %+v, want %s %s with no lease", tt.answer.Outcome, p, tt.status, tt.reason)
		}
	}

	p := NewPayment("p1", activeMerchant(), validRequest(), now).WithAnswer(
		Answer{Outcome: OutcomeApproved, AuthorizationCode: "7T4N4V", CardBrand: "visa", CardLast4: "1111"}, now, half)
	if p.AuthorizationCode != "7T4N4V" || p.CardBrand != "visa" || p.CardLast4 != "1111" {
		t.Errorf("got %+v, want the code and the card the acquirer reported", p)
	}
}

func TestAnUnclearAnswerWaitsForTheNextAttempt(t *testing.T) {
	for _, o := range []Outcome{OutcomeUncertain, OutcomeNotSent} {
		p := NewPayment("p1", activeMerchant(), validRequest(), now).WithAnswer(Answer{Outcome: o}, now, half)
		if p.Status != PaymentProcessing || !p.LeaseUntil.Equal(now.Add(1500*time.Millisecond)) {
			t.Errorf("%s: got %s with lease %s, want PROCESSING until the wait ends", o, p.Status, p.LeaseUntil)
		}
	}
}

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
