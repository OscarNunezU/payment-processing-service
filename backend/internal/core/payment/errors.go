package payment

import (
	"errors"
	"strings"
)

var (
	ErrInvalidStatus = errors.New("invalid status")
	ErrNotFound      = errors.New("payment not found")
	// ErrNotProcessing means the payment is not PROCESSING: it was never
	// claimed, or another worker already finished it after this one's lease
	// expired.
	ErrNotProcessing = errors.New("payment is not being processed")
)

type FieldError struct {
	Field   string
	Message string
}

// ValidationError means the request is malformed: the caller must fix the input.
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

// RuleError means a business rule rejected the payment; Code says which one.
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
	ErrIdempotencyKeyReused = &RuleError{
		Code:    "idempotency_key_reused",
		Message: "the idempotency key was already used for a different payment",
	}
)
