package acquirersim

import (
	"testing"
	"time"
)

func TestTheTestCardsAreValidNumbers(t *testing.T) {
	for number := range testCards {
		if !validNumber(number) {
			t.Errorf("%s fails the Luhn check", number)
		}
	}
}

func TestValidNumber(t *testing.T) {
	tests := []struct {
		number string
		want   bool
	}{
		{"4111111111111111", true},
		{"5555555555554444", true},
		{"378282246310005", true},
		{"4111111111111112", false}, // wrong check digit
		{"41111111111", false},      // 11 digits
		{"41111111111111111111", false},
		{"4111 1111 1111 1111", false},
		{"411111111111111a", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := validNumber(tt.number); got != tt.want {
			t.Errorf("validNumber(%q) = %v, want %v", tt.number, got, tt.want)
		}
	}
}

func TestBrandOf(t *testing.T) {
	tests := map[string]string{
		"4111111111111111": "visa",
		"5555555555554444": "mastercard",
		"2221000000000009": "mastercard",
		"2720999999999996": "mastercard",
		"378282246310005":  "amex",
		"6011111111111117": "unknown",
	}
	for number, want := range tests {
		if got := brandOf(number); got != want {
			t.Errorf("brandOf(%s) = %s, want %s", number, got, want)
		}
	}
}

func TestACardIsValidThroughTheLastDayOfItsMonthEverywhere(t *testing.T) {
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"last day of the month in Chile, late at night", time.Date(2030, 12, 31, 23, 30, 0, 0, santiago), false},
		{"already January in UTC, still December at UTC−12", time.Date(2031, 1, 1, 11, 59, 0, 0, time.UTC), false},
		{"January everywhere", time.Date(2031, 1, 1, 12, 0, 0, 0, time.UTC), true},
		{"a month earlier", time.Date(2030, 11, 15, 12, 0, 0, 0, time.UTC), false},
	}
	for _, tt := range tests {
		if got := expired(12, 2030, tt.now); got != tt.want {
			t.Errorf("%s: expired = %v, want %v", tt.name, got, tt.want)
		}
	}
}
