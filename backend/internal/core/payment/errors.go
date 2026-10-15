package payment

import (
	"errors"
	"strings"
)

var ErrInvalidStatus = errors.New("invalid status")

type FieldError struct {
	Field   string
	Message string
}

// ValidationError means the request is malformed; the API answers 400.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + " " + f.Message
	}
	return "invalid payment: " + strings.Join(parts, "; ")
}

// RuleError is a business rule violation; the API answers 422 with its code.
type RuleError struct {
	Code    string
	Message string
}

func (e *RuleError) Error() string {
	return e.Message
}

var (
	ErrCurrencyNotSupported = &RuleError{Code: "currency_not_supported", Message: "currency is not supported"}
	ErrAmountExceedsMax     = &RuleError{Code: "amount_exceeds_max", Message: "amount exceeds the maximum allowed"}
	ErrInvalidCardNumber    = &RuleError{Code: "invalid_card_number", Message: "card number is not valid"}
	ErrCardExpired          = &RuleError{Code: "card_expired", Message: "card is expired"}
)
