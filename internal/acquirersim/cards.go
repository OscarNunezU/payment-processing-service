package acquirersim

import "time"

// behavior is what the simulator does with an authorization for a card.
type behavior int

const (
	approve         behavior = iota
	declineCard              // declined with card_declined
	declineFunds             // declined with insufficient_funds
	unavailable              // 503 on every authorization, nothing processed
	unavailableOnce          // 503 on the first authorization of a key, then approved
	answerLate               // approved, but the first answer comes after the client's timeout
	rejectRequest            // 422, nothing processed
	neverAnswer              // approved, and no answer ever comes
)

// testCards are the cards documented in RFC 0001, section 10. Any other card
// that passes the checks is approved.
var testCards = map[string]behavior{
	"4111111111111111": approve,
	"4000000000000002": declineCard,
	"4000000000009995": declineFunds,
	"4000000000000119": unavailable,
	"4000000000000127": unavailableOnce,
	"4000000000000135": answerLate,
	"4000000000000143": rejectRequest,
	"4000000000000150": neverAnswer,
}

// card is what the simulator keeps behind a token. It stands for the
// acquirer's vault: the payment service never sees the number.
type card struct {
	number   string
	brand    string
	expMonth int
	expYear  int
}

func (c card) last4() string { return c.number[len(c.number)-4:] }

func (c card) behavior() behavior { return testCards[c.number] }

// validNumber reports whether number has 12 to 19 digits and passes the Luhn
// check.
func validNumber(number string) bool {
	if len(number) < 12 || len(number) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(number) - 1; i >= 0; i-- {
		d := int(number[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
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

// brandOf names the card network from the first digits of number.
func brandOf(number string) string {
	switch {
	case number[0] == '4':
		return "visa"
	case number[:2] >= "51" && number[:2] <= "55", number[:4] >= "2221" && number[:4] <= "2720":
		return "mastercard"
	case number[:2] == "34", number[:2] == "37":
		return "amex"
	default:
		return "unknown"
	}
}

// lastTimeZone is the last place on Earth where a day ends.
var lastTimeZone = time.FixedZone("UTC-12", -12*60*60)

// expired reports whether a card that expires in month/year can no longer be
// used at now. A card is valid through the last day of its month wherever the
// cardholder is, so the month is over only once it has ended at UTC−12.
func expired(month, year int, now time.Time) bool {
	firstDayAfter := time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, lastTimeZone)
	return !now.Before(firstDayAfter)
}
