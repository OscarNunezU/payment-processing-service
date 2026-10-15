package payment

import (
	"errors"
	"testing"
	"time"
)

var (
	testLimits = Limits{"CLP": 5_000_000, "USD": 500_000}
	testNow    = time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
)

func TestCheckRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*NewPayment)
		want   error
	}{
		{"valid payment", func(*NewPayment) {}, nil},
		{"amount exactly at the limit", func(p *NewPayment) { p.Amount = 5_000_000 }, nil},
		{"usd within its limit", func(p *NewPayment) { p.Currency, p.Amount = "USD", 12_000 }, nil},
		{"currency not accepted", func(p *NewPayment) { p.Currency = "EUR" }, ErrCurrencyNotSupported},
		{"amount over the limit", func(p *NewPayment) { p.Amount = 5_000_001 }, ErrAmountExceedsMax},
		{"card fails the luhn check", func(p *NewPayment) { p.Card.Number = "4111111111111112" }, ErrInvalidCardNumber},
		{"card expired", func(p *NewPayment) { p.Card.ExpiryMonth, p.Card.ExpiryYear = 9, 2026 }, ErrCardExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPayment()
			tt.mutate(&p)

			if err := CheckRules(p, testLimits, testNow); !errors.Is(err, tt.want) {
				t.Errorf("CheckRules() = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRuleErrorCarriesItsCode(t *testing.T) {
	p := validPayment()
	p.Amount = 9_000_000

	var re *RuleError
	if err := CheckRules(p, testLimits, testNow); !errors.As(err, &re) || re.Code != "amount_exceeds_max" {
		t.Fatalf("CheckRules() = %v, want a RuleError with code amount_exceeds_max", err)
	}
}
