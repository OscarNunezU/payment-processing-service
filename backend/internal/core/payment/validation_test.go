package payment

import (
	"errors"
	"strings"
	"testing"
)

func validPayment() NewPayment {
	return NewPayment{
		MerchantID:     "merchant-001",
		IdempotencyKey: "order-1001",
		Amount:         15000,
		Currency:       "CLP",
		Card:           Card{Number: "4111111111111111", ExpiryMonth: 12, ExpiryYear: 2030, CVV: "123"},
	}
}

func TestValidateAcceptsAWellFormedPayment(t *testing.T) {
	if err := validPayment().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsMalformedFields(t *testing.T) {
	tests := []struct {
		field  string
		mutate func(*NewPayment)
	}{
		{"merchant_id", func(p *NewPayment) { p.MerchantID = "" }},
		{"merchant_id", func(p *NewPayment) { p.MerchantID = strings.Repeat("x", 65) }},
		{"idempotency_key", func(p *NewPayment) { p.IdempotencyKey = "" }},
		{"idempotency_key", func(p *NewPayment) { p.IdempotencyKey = strings.Repeat("k", 256) }},
		{"idempotency_key", func(p *NewPayment) { p.IdempotencyKey = "order\n1001" }},
		{"amount", func(p *NewPayment) { p.Amount = 0 }},
		{"amount", func(p *NewPayment) { p.Amount = -100 }},
		{"currency", func(p *NewPayment) { p.Currency = "clp" }},
		{"currency", func(p *NewPayment) { p.Currency = "PESO" }},
		{"card.number", func(p *NewPayment) { p.Card.Number = "4111 1111 1111 1111" }},
		{"card.number", func(p *NewPayment) { p.Card.Number = "411111" }},
		{"card.expiry_month", func(p *NewPayment) { p.Card.ExpiryMonth = 13 }},
		{"card.expiry_year", func(p *NewPayment) { p.Card.ExpiryYear = 30 }},
		{"card.cvv", func(p *NewPayment) { p.Card.CVV = "12" }},
		{"card.cvv", func(p *NewPayment) { p.Card.CVV = "12a" }},
	}
	for _, tt := range tests {
		p := validPayment()
		tt.mutate(&p)

		var ve *ValidationError
		if err := p.Validate(); !errors.As(err, &ve) || !hasField(ve, tt.field) {
			t.Errorf("Validate() with a bad %s = %v, want a ValidationError for %s", tt.field, err, tt.field)
		}
	}
}

func TestValidateReportsEveryBadFieldAtOnce(t *testing.T) {
	var ve *ValidationError
	if err := (NewPayment{}).Validate(); !errors.As(err, &ve) || len(ve.Fields) != 8 {
		t.Fatalf("Validate() = %v, want the 8 fields reported together", err)
	}
}

func hasField(ve *ValidationError, field string) bool {
	for _, f := range ve.Fields {
		if f.Field == field {
			return true
		}
	}
	return false
}
