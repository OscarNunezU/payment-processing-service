package payment

import (
	"fmt"
	"time"
)

// Limits maps each accepted currency to its maximum amount, in minor units.
// A currency that is not in the map is not accepted.
type Limits map[string]int64

// CheckRules applies the business rules to a payment that already passed
// Validate, and returns the first rule it breaks.
func CheckRules(p NewPayment, limits Limits, now time.Time) error {
	limit, ok := limits[p.Currency]
	if !ok {
		return fmt.Errorf("%w: %s", ErrCurrencyNotSupported, p.Currency)
	}
	if p.Amount > limit {
		return fmt.Errorf("%w: %d > %d %s", ErrAmountExceedsMax, p.Amount, limit, p.Currency)
	}
	if !p.Card.luhnValid() {
		return ErrInvalidCardNumber
	}
	if p.Card.expired(now) {
		return ErrCardExpired
	}
	return nil
}
