package payment

// NewPayment is what a merchant sends to create a payment.
type NewPayment struct {
	MerchantID string
	Amount     int64
	Currency   string
	Card       Card
}

// Validate checks the shape of the request and reports every malformed field
// at once, so the merchant can fix them in a single round trip.
func (p NewPayment) Validate() error {
	var fields []FieldError
	add := func(field, message string) {
		fields = append(fields, FieldError{Field: field, Message: message})
	}

	if p.MerchantID == "" {
		add("merchant_id", "is required")
	} else if len(p.MerchantID) > 64 {
		add("merchant_id", "must be at most 64 characters")
	}
	if p.Amount <= 0 {
		add("amount", "must be a positive integer in minor units")
	}
	if !isUpperLetters(p.Currency, 3) {
		add("currency", "must be a 3-letter ISO 4217 code")
	}
	if n := len(p.Card.Number); n < 13 || n > 19 || !isDigits(p.Card.Number) {
		add("card.number", "must have 13 to 19 digits")
	}
	if p.Card.ExpiryMonth < 1 || p.Card.ExpiryMonth > 12 {
		add("card.expiry_month", "must be between 1 and 12")
	}
	if p.Card.ExpiryYear < 2000 || p.Card.ExpiryYear > 9999 {
		add("card.expiry_year", "must be a 4-digit year")
	}
	if n := len(p.Card.CVV); n < 3 || n > 4 || !isDigits(p.Card.CVV) {
		add("card.cvv", "must have 3 or 4 digits")
	}

	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isUpperLetters(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
