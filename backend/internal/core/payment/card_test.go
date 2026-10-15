package payment

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLuhnValid(t *testing.T) {
	tests := []struct {
		number string
		want   bool
	}{
		{"4111111111111111", true}, // Visa test card
		{"5555555555554444", true}, // Mastercard test card
		{"378282246310005", true},  // Amex test card
		{"4111111111111112", false},
		{"1234567812345678", false},
	}
	for _, tt := range tests {
		if got := (Card{Number: tt.number}).luhnValid(); got != tt.want {
			t.Errorf("luhnValid(%s) = %v, want %v", tt.number, got, tt.want)
		}
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		month, year int
		want        bool
	}{
		{10, 2026, false}, // valid until the end of its month
		{11, 2026, false},
		{1, 2027, false},
		{9, 2026, true},
		{12, 2025, true},
	}
	for _, tt := range tests {
		c := Card{ExpiryMonth: tt.month, ExpiryYear: tt.year}
		if got := c.expired(now); got != tt.want {
			t.Errorf("expired(%02d/%d) = %v, want %v", tt.month, tt.year, got, tt.want)
		}
	}
}

func TestCardNeverExposesTheNumber(t *testing.T) {
	c := Card{Number: "4111111111111111", ExpiryMonth: 12, ExpiryYear: 2030, CVV: "123"}

	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("payment received", "card", c)
	fmt.Fprintf(&buf, "%v %+v %s", c, c, c)

	out := buf.String()
	if strings.Contains(out, c.Number) {
		t.Fatalf("output leaks the card number: %s", out)
	}
	if !strings.Contains(out, "1111") {
		t.Fatalf("output should keep the last four digits: %s", out)
	}
}

func TestLast4OfAShortNumber(t *testing.T) {
	if got := (Card{Number: "123"}).Last4(); got != "" {
		t.Errorf("Last4() = %q, want empty", got)
	}
}
