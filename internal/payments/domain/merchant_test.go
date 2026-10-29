package domain

import (
	"errors"
	"math/rand/v2"
	"strconv"
	"testing"
)

func TestParseRUT(t *testing.T) {
	valid := map[string]RUT{
		"11.111.111-1": "11111111-1",
		"12345678-5":   "12345678-5",
		"76.543.210-3": "76543210-3",
		"10000013-K":   "10000013-K",
		"10000013-k":   "10000013-K",
		"14-0":         "14-0",
		"123456785":    "12345678-5",
	}
	for in, want := range valid {
		got, err := ParseRUT(in)
		if err != nil || got != want {
			t.Errorf("ParseRUT(%q) = %q, %v, want %q", in, got, err, want)
		}
	}

	invalid := []string{
		"12345678-4", // wrong check digit
		"10000013-0",
		"100000013-K", // 9 digits
		"01234567-8",  // leading zero
		"+2345678-5",
		"1234a678-5",
		"-5",
		"5",
		"",
	}
	for _, in := range invalid {
		if got, err := ParseRUT(in); !errors.Is(err, ErrInvalidRUT) {
			t.Errorf("ParseRUT(%q) = %q, %v, want ErrInvalidRUT", in, got, err)
		}
	}
}

// Every number has exactly one check digit: the right one passes, and the
// other ten are rejected.
func TestEveryRUTHasOneCheckDigit(t *testing.T) {
	digits := "0123456789K"
	for range 1000 {
		body := strconv.Itoa(1 + rand.IntN(99_999_999)) //nolint:gosec // test input
		accepted := 0
		for i := range len(digits) {
			if _, err := ParseRUT(body + "-" + digits[i:i+1]); err == nil {
				accepted++
			}
		}
		if accepted != 1 {
			t.Fatalf("%s accepts %d check digits, want 1", body, accepted)
		}
	}
}

func TestParseCurrency(t *testing.T) {
	for _, s := range []string{"CLP", "USD"} {
		if c, err := ParseCurrency(s); err != nil || string(c) != s {
			t.Errorf("ParseCurrency(%q) = %q, %v", s, c, err)
		}
	}
	for _, s := range []string{"clp", "EUR", ""} {
		if _, err := ParseCurrency(s); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Errorf("ParseCurrency(%q): got %v, want ErrUnsupportedCurrency", s, err)
		}
	}
}

func TestOnlyActiveMerchantsCreatePayments(t *testing.T) {
	if err := (Merchant{Status: MerchantActive}).CanCreatePayments(); err != nil {
		t.Errorf("active: %v", err)
	}
	if err := (Merchant{Status: MerchantSuspended}).CanCreatePayments(); !errors.Is(err, ErrMerchantSuspended) {
		t.Errorf("suspended: got %v, want ErrMerchantSuspended", err)
	}
}

func TestAPIKeys(t *testing.T) {
	a, b := NewAPIKey(), NewAPIKey()
	if a == b {
		t.Fatal("two new keys are equal")
	}
	ha, ok := HashAPIKey(a)
	if !ok {
		t.Fatalf("a new key %q is not accepted", a)
	}
	if hb, _ := HashAPIKey(b); ha == hb {
		t.Error("two keys have the same hash")
	}
	if hint := APIKeyHint(a); hint != a[:12] {
		t.Errorf("hint %q, want the first 12 characters of the key", hint)
	}
	for _, bad := range []string{"", "sk_test_", "sk_test_short", "pk_test_0123456789abcdef", "Bearer sk_test_0123456789abcdef"} {
		if _, ok := HashAPIKey(bad); ok {
			t.Errorf("HashAPIKey(%q) accepted it", bad)
		}
	}
}
