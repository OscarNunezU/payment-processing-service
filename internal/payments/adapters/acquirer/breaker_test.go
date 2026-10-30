package acquirer

import (
	"testing"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestBreaker() (*Breaker, *clock) {
	c := &clock{now: time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC)}
	return NewBreaker(5, 30*time.Second, c.Now), c
}

func fail(b *Breaker, n int) {
	for range n {
		b.Record(domain.OutcomeUncertain)
	}
}

func TestTheBreakerOpensAfterFiveFailuresInARow(t *testing.T) {
	b, _ := newTestBreaker()
	fail(b, 4)
	if !b.Allow() || b.Open() {
		t.Fatal("4 failures opened the breaker")
	}
	b.Record(domain.OutcomeNotSent)
	if b.Allow() || !b.Open() {
		t.Error("5 failures did not open the breaker")
	}
}

func TestADefiniteAnswerResetsTheCount(t *testing.T) {
	b, _ := newTestBreaker()
	fail(b, 4)
	b.Record(domain.OutcomeDeclined)
	fail(b, 4)
	if !b.Allow() {
		t.Error("the count was not reset by a decline")
	}
}

func TestAfterTheCooldownOneCallProbes(t *testing.T) {
	b, c := newTestBreaker()
	fail(b, 5)
	c.advance(30 * time.Second)
	if !b.Allow() {
		t.Fatal("no probe after the cool-down")
	}
	if b.Allow() {
		t.Error("a second call went through while the probe was out")
	}

	b.Record(domain.OutcomeApproved)
	first, second := b.Allow(), b.Allow()
	if !first || !second {
		t.Error("a successful probe did not close the breaker")
	}
}

func TestAFailedProbeOpensTheBreakerAgain(t *testing.T) {
	b, c := newTestBreaker()
	fail(b, 5)
	c.advance(30 * time.Second)
	b.Allow()
	b.Record(domain.OutcomeUncertain)
	if b.Allow() {
		t.Error("the breaker let calls through after a failed probe")
	}
	c.advance(30 * time.Second)
	if !b.Allow() {
		t.Error("no new probe after the second cool-down")
	}
}

func TestAProbeThatNeverAnsweredGivesWay(t *testing.T) {
	b, c := newTestBreaker()
	fail(b, 5)
	c.advance(30 * time.Second)
	b.Allow() // its payment is never sent, so no outcome is recorded
	c.advance(29 * time.Second)
	if b.Allow() {
		t.Fatal("a second probe went out too soon")
	}
	c.advance(time.Second)
	if !b.Allow() {
		t.Error("the stuck probe never gave way")
	}
}

func newTestRecoveryBreaker() *Breaker {
	c := &clock{now: time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC)}
	return NewRecoveryBreaker(5, 30*time.Second, c.Now)
}

func refuse(b *Breaker, n int) {
	for range n {
		b.Record(domain.OutcomeNotSent)
	}
}

// The recovery only calls for payments that had no definite answer, so a card
// the acquirer never answers can time out again and again while the acquirer
// answers every other payment.
func TestTheRecoveryBreakerDoesNotOpenOnUncertainAnswers(t *testing.T) {
	b := newTestRecoveryBreaker()
	fail(b, 20)
	if !b.Allow() || b.Open() {
		t.Error("uncertain answers opened the recovery's breaker")
	}
	refuse(b, 5)
	if b.Allow() || !b.Open() {
		t.Error("5 refused connections did not open the recovery's breaker")
	}
}

func TestAnUncertainAnswerResetsTheRecoveryBreaker(t *testing.T) {
	b := newTestRecoveryBreaker()
	refuse(b, 4)
	b.Record(domain.OutcomeUncertain)
	refuse(b, 4)
	if !b.Allow() {
		t.Error("a call that reached the acquirer did not reset the count")
	}
}
