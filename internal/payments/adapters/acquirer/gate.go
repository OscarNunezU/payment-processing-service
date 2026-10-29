package acquirer

// Gate admits a new payment when the breaker allows a call and there is room
// for one more payment in flight (RFC 0001, section 5.4). It implements
// app.Gate.
type Gate struct {
	breaker *Breaker
	slots   chan struct{}
}

// NewGate returns a gate with room for limit payments in flight.
func NewGate(breaker *Breaker, limit int) *Gate {
	return &Gate{breaker: breaker, slots: make(chan struct{}, limit)}
}

// Acquire takes a slot and asks the breaker, without waiting for either.
func (g *Gate) Acquire() (func(), bool) {
	select {
	case g.slots <- struct{}{}:
	default:
		return nil, false
	}
	if !g.breaker.Allow() {
		<-g.slots
		return nil, false
	}
	return func() { <-g.slots }, true
}
