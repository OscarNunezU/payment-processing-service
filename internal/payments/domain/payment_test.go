package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validRequest() PaymentRequest {
	return PaymentRequest{IdempotencyKey: "order-1001-1", OrderID: "1001", Amount: 15000, Currency: "CLP", CardToken: "tok_abc"}
}

func activeMerchant() Merchant {
	return Merchant{ID: "m1", Status: MerchantActive, MaxAmount: map[Currency]int64{CLP: 5_000_000, USD: 500_000}}
}

func TestValidateReportsEveryMalformedField(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("a valid request: %v", err)
	}

	err := PaymentRequest{OrderID: strings.Repeat("x", 65), Amount: 0, Currency: "clp", CardToken: "tok\n"}.Validate()
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("got %v, want a ValidationError", err)
	}
	got := map[string]bool{}
	for _, f := range invalid.Fields {
		got[f.Field] = true
	}
	for _, field := range []string{"idempotency_key", "order_id", "amount", "currency", "card_token"} {
		if !got[field] {
			t.Errorf("%s is not reported", field)
		}
	}
}

func TestValidateBounds(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*PaymentRequest)
		valid bool
	}{
		{"the smallest amount", func(r *PaymentRequest) { r.Amount = 1 }, true},
		{"a negative amount", func(r *PaymentRequest) { r.Amount = -1 }, false},
		{"a 255-character key", func(r *PaymentRequest) { r.IdempotencyKey = strings.Repeat("k", 255) }, true},
		{"a 256-character key", func(r *PaymentRequest) { r.IdempotencyKey = strings.Repeat("k", 256) }, false},
		{"a 64-character order", func(r *PaymentRequest) { r.OrderID = strings.Repeat("o", 64) }, true},
		{"a key with a control character", func(r *PaymentRequest) { r.IdempotencyKey = "k\x01" }, false},
		{"an unknown but well-formed currency", func(r *PaymentRequest) { r.Currency = "EUR" }, true},
		{"a 4-letter currency", func(r *PaymentRequest) { r.Currency = "CLPX" }, false},
	}
	for _, tt := range tests {
		r := validRequest()
		tt.edit(&r)
		if err := r.Validate(); (err == nil) != tt.valid {
			t.Errorf("%s: got %v, want valid=%v", tt.name, err, tt.valid)
		}
	}
}

func TestTheHashCoversWhatTheRequestAsks(t *testing.T) {
	base := validRequest()
	otherKey := base
	otherKey.IdempotencyKey = "another"
	if base.Hash() != otherKey.Hash() {
		t.Error("the key changes the hash, but it is what two retries share")
	}
	for name, edit := range map[string]func(*PaymentRequest){
		"order":    func(r *PaymentRequest) { r.OrderID = "1002" },
		"amount":   func(r *PaymentRequest) { r.Amount++ },
		"currency": func(r *PaymentRequest) { r.Currency = "USD" },
		"token":    func(r *PaymentRequest) { r.CardToken = "tok_other" },
		// The separator keeps "10"+"01" apart from "100"+"1".
		"fields that run together": func(r *PaymentRequest) { r.OrderID, r.Amount = "10011", 5000 },
	} {
		r := base
		edit(&r)
		if r.Hash() == base.Hash() {
			t.Errorf("changing the %s keeps the hash", name)
		}
	}
}

func TestCheckRules(t *testing.T) {
	suspended := activeMerchant()
	suspended.Status = MerchantSuspended
	onlyCLP := activeMerchant()
	delete(onlyCLP.MaxAmount, USD)

	tests := []struct {
		name string
		m    Merchant
		edit func(*PaymentRequest)
		want error
	}{
		{"a valid payment", activeMerchant(), func(*PaymentRequest) {}, nil},
		{"exactly the maximum", activeMerchant(), func(r *PaymentRequest) { r.Amount = 5_000_000 }, nil},
		{"one above the maximum", activeMerchant(), func(r *PaymentRequest) { r.Amount = 5_000_001 }, ErrRuleAmountExceedsMax},
		{"USD in cents", activeMerchant(), func(r *PaymentRequest) { r.Currency, r.Amount = "USD", 500_000 }, nil},
		{"the CLP maximum in USD", activeMerchant(), func(r *PaymentRequest) { r.Currency, r.Amount = "USD", 5_000_000 }, ErrRuleAmountExceedsMax},
		{"a suspended merchant", suspended, func(*PaymentRequest) {}, ErrRuleMerchantSuspended},
		{"a currency the service does not take", activeMerchant(), func(r *PaymentRequest) { r.Currency = "EUR" }, ErrRuleCurrencyNotSupported},
		{"a currency the merchant does not accept", onlyCLP, func(r *PaymentRequest) { r.Currency = "USD" }, ErrRuleCurrencyNotAccepted},
	}
	for _, tt := range tests {
		r := validRequest()
		tt.edit(&r)
		if err := CheckRules(tt.m, r); !errors.Is(err, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestReplay(t *testing.T) {
	r := validRequest()
	existing := NewPayment("p1", activeMerchant(), r, time.Now())

	if got, err := Replay(existing, r); err != nil || got.ID != "p1" {
		t.Errorf("the same request: got %+v, %v, want payment p1", got, err)
	}
	r.Amount++
	if _, err := Replay(existing, r); !errors.Is(err, ErrRuleIdempotencyKeyReused) {
		t.Errorf("a different request: got %v, want ErrRuleIdempotencyKeyReused", err)
	}
}

func TestStatuses(t *testing.T) {
	for _, s := range []PaymentStatus{PaymentPending, PaymentProcessing, PaymentApproved, PaymentDeclined, PaymentFailed} {
		if got, err := ParsePaymentStatus(string(s)); err != nil || got != s {
			t.Errorf("ParsePaymentStatus(%s) = %s, %v", s, got, err)
		}
	}
	if _, err := ParsePaymentStatus("approved"); !errors.Is(err, ErrInvalidStatus) {
		t.Errorf("lower case: got %v, want ErrInvalidStatus", err)
	}
	if PaymentPending.Final() || PaymentProcessing.Final() || !PaymentApproved.Final() || !PaymentDeclined.Final() || !PaymentFailed.Final() {
		t.Error("only APPROVED, DECLINED and FAILED are final")
	}
}

func TestIsPaymentID(t *testing.T) {
	if !IsPaymentID("0199b5a2-7c1e-7d3a-9f00-0000000000a1") {
		t.Error("a UUID is not accepted")
	}
	for _, s := range []string{
		"", "1", "pay_123", "0199b5a2-7c1e-7d3a-9f00-0000000000a",
		// Other forms of the same UUID: only the canonical one is an id.
		"0199B5A2-7C1E-7D3A-9F00-0000000000A1", "urn:uuid:0199b5a2-7c1e-7d3a-9f00-0000000000a1",
		"{0199b5a2-7c1e-7d3a-9f00-0000000000a1}", "0199b5a27c1e7d3a9f000000000000a1",
	} {
		if IsPaymentID(s) {
			t.Errorf("%q is accepted", s)
		}
	}
}
