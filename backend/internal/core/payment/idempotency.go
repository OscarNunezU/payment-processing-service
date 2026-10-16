package payment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// fingerprint identifies what a request asks for, to tell a retry of the same
// payment from a different payment that reuses its idempotency key. The full
// card number and the CVV stay out: even hashed, they would still be card data.
func fingerprint(p NewPayment) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%s|%d|%d",
		p.Amount, p.Currency, p.Card.Last4(), p.Card.ExpiryMonth, p.Card.ExpiryYear))
	return hex.EncodeToString(sum[:])
}

// replay answers a request whose idempotency key is already taken: the same
// payment comes back, in its current state, marked as replayed; a different
// payment is rejected.
func replay(existing Transaction, fp string) (Transaction, bool, error) {
	if existing.RequestFingerprint != fp {
		return Transaction{}, false, ErrIdempotencyKeyReused
	}
	existing.CardData = nil
	return existing, true, nil
}
