package payment

import (
	"errors"
	"testing"
)

func TestFingerprintChangesWithWhatThePaymentAsksFor(t *testing.T) {
	base := fingerprint(validPayment())
	if base != fingerprint(validPayment()) {
		t.Fatal("the same payment gave two different fingerprints")
	}

	changes := map[string]func(*NewPayment){
		"amount":       func(p *NewPayment) { p.Amount++ },
		"currency":     func(p *NewPayment) { p.Currency = "USD" },
		"card last4":   func(p *NewPayment) { p.Card.Number = "5555555555554444" },
		"expiry month": func(p *NewPayment) { p.Card.ExpiryMonth = 11 },
		"expiry year":  func(p *NewPayment) { p.Card.ExpiryYear = 2031 },
	}
	for name, change := range changes {
		p := validPayment()
		change(&p)
		if fingerprint(p) == base {
			t.Errorf("changing the %s kept the same fingerprint", name)
		}
	}
}

// Two cards that share the last 4 digits and the expiry give the same
// fingerprint: the price of never hashing the full number or the CVV.
func TestFingerprintLeavesOutTheFullNumberAndTheCVV(t *testing.T) {
	p := validPayment()
	p.Card.Number = "4000056655661111"
	p.Card.CVV = "999"
	if fingerprint(p) != fingerprint(validPayment()) {
		t.Fatal("the fingerprint depends on the full card number or the CVV")
	}
}

func TestReplay(t *testing.T) {
	p := validPayment()
	existing := Transaction{ID: "txn-1", RequestFingerprint: fingerprint(p), CardData: []byte("sealed")}

	got, replayed, err := replay(existing, fingerprint(p))
	if err != nil || !replayed || got.ID != "txn-1" || got.CardData != nil {
		t.Fatalf("replay(same payment) = %+v, %v, %v; want the payment without its card, replayed", got, replayed, err)
	}

	p.Amount++
	if _, _, err := replay(existing, fingerprint(p)); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("replay(other payment) = %v, want ErrIdempotencyKeyReused", err)
	}
}
