package apptest

import (
	"context"
	"sync"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Acquirer answers every call with Answer, and remembers the calls.
type Acquirer struct {
	mu     sync.Mutex
	Answer domain.Answer
	calls  []app.AuthorizationRequest
}

// Approving returns an acquirer that approves every call.
func Approving() *Acquirer {
	return &Acquirer{Answer: domain.Answer{Outcome: domain.OutcomeApproved, AuthorizationCode: "7T4N4V", CardBrand: "visa", CardLast4: "1111"}}
}

func (a *Acquirer) Authorize(_ context.Context, r app.AuthorizationRequest) domain.Answer {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, r)
	return a.Answer
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
