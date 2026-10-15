package payment

import (
	"encoding/json"
	"log/slog"
	"time"
)

type Card struct {
	Number      string
	ExpiryMonth int
	ExpiryYear  int
	CVV         string
}

func (c Card) Last4() string {
	if len(c.Number) < 4 {
		return ""
	}
	return c.Number[len(c.Number)-4:]
}

// String masks the number, so a card printed by mistake never leaks it.
func (c Card) String() string {
	return "card ****" + c.Last4()
}

// GoString covers %#v, which ignores String and would print every field.
func (c Card) GoString() string {
	return c.String()
}

// MarshalJSON keeps the card masked in JSON too. The vault and the acquirer
// serialize the full details with their own types, never with Card.
func (c Card) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"last4": c.Last4()})
}

// LogValue does the same for slog: only the last four digits reach the logs.
func (c Card) LogValue() slog.Value {
	return slog.GroupValue(slog.String("last4", c.Last4()))
}

// luhnValid runs the Luhn checksum; it expects a number made only of digits.
func (c Card) luhnValid() bool {
	sum := 0
	double := false
	for i := len(c.Number) - 1; i >= 0; i-- {
		d := int(c.Number[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// expired reports whether the card is past its expiry month; a card is valid
// until the last day of that month.
func (c Card) expired(now time.Time) bool {
	year, month := now.Year(), int(now.Month())
	return c.ExpiryYear < year || (c.ExpiryYear == year && c.ExpiryMonth < month)
}
