package apptest

import (
	"context"
	"sync"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Acquirer answers the first calls with Script, in order, and every call
// after them with Answer. It answers every reversal with Reversal, which
// confirms it unless it is set. It remembers the calls and the reversals.
type Acquirer struct {
	mu        sync.Mutex
	Script    []domain.Answer
	Answer    domain.Answer
	Reversal  domain.Answer
	calls     []app.AuthorizationRequest
	reversals []string
}

// Approving returns an acquirer that approves every call.
func Approving() *Acquirer {
	return &Acquirer{Answer: domain.Answer{Outcome: domain.OutcomeApproved, AuthorizationCode: "7T4N4V", CardBrand: "visa", CardLast4: "1111"}}
}

func (a *Acquirer) Authorize(_ context.Context, r app.AuthorizationRequest) domain.Answer {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, r)
	if len(a.calls) <= len(a.Script) {
		return a.Script[len(a.calls)-1]
	}
	return a.Answer
}

func (a *Acquirer) Reverse(_ context.Context, key string) domain.Answer {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reversals = append(a.reversals, key)
	if a.Reversal.Outcome == "" {
		return domain.Answer{Outcome: domain.OutcomeReversed, ResponseCode: "200"}
	}
	return a.Reversal
}

// SetReversal changes the answer to the reversals that follow.
func (a *Acquirer) SetReversal(answer domain.Answer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Reversal = answer
}

// Reversals returns the keys reversed so far, one per reversal.
func (a *Acquirer) Reversals() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.reversals...)
}

// Calls returns the calls received so far.
func (a *Acquirer) Calls() []app.AuthorizationRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]app.AuthorizationRequest(nil), a.calls...)
}

// Gate admits payments unless Closed is true, and counts the ones in flight.
type Gate struct {
	mu       sync.Mutex
	Closed   bool
	inFlight int
}

func (g *Gate) Acquire() (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Closed {
		return nil, false
	}
	g.inFlight++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.inFlight--
	}, true
}

// InFlight returns how many admitted payments have not been released.
func (g *Gate) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight
}

// Breaker allows every attempt until it is opened.
type Breaker struct {
	mu   sync.Mutex
	open bool
}

func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.open
}

// SetOpen opens or closes the breaker.
func (b *Breaker) SetOpen(open bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = open
}
