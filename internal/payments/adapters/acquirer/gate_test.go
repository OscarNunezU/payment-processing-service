package acquirer

import "testing"

func TestTheGateLimitsThePaymentsInFlight(t *testing.T) {
	g := NewGate(newBreaker(), 2)
	r1, ok1 := g.Acquire()
	_, ok2 := g.Acquire()
	if !ok1 || !ok2 {
		t.Fatal("the first two payments were refused")
	}
	if _, ok := g.Acquire(); ok {
		t.Fatal("a third payment got in")
	}
	r1()
	if _, ok := g.Acquire(); !ok {
		t.Error("a released slot was not reused")
	}
}

func TestAnOpenBreakerClosesTheGateWithoutLosingSlots(t *testing.T) {
	b, _ := newTestBreaker()
	g := NewGate(b, 1)
	fail(b, 5)
	for range 3 {
		if _, ok := g.Acquire(); ok {
			t.Fatal("the gate admitted a payment with the breaker open")
		}
	}
	if len(g.slots) != 0 {
		t.Errorf("%d slots leaked while the breaker refused", len(g.slots))
	}
}
