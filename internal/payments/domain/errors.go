package domain

import (
	"errors"
	"strings"
)

// FieldError is one malformed field of a request.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError means the request is malformed, and nothing was created.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + " " + f.Message
	}
	return "invalid request: " + strings.Join(parts, "; ")
}

// RuleError means a business rule rejected a well-formed request. Code is
// stable, for programs; Message is for people.
type RuleError struct {
	Code    string
	Message string
}

func (e *RuleError) Error() string { return e.Message }

// The business rules a payment request can break (FR03, FR06, FR07).
var (
	ErrRuleMerchantSuspended = &RuleError{
		Code: "merchant_suspended", Message: "The merchant is suspended and cannot create payments.",
	}
	ErrRuleCurrencyNotSupported = &RuleError{
		Code: "currency_not_supported", Message: "The service takes payments in CLP and USD only.",
	}
	ErrRuleCurrencyNotAccepted = &RuleError{
		Code: "currency_not_accepted", Message: "The merchant does not accept payments in this currency.",
	}
	ErrRuleAmountExceedsMax = &RuleError{
		Code: "amount_exceeds_max", Message: "The amount is above the merchant's maximum for a payment.",
	}
	ErrRuleIdempotencyKeyReused = &RuleError{
		Code: "idempotency_key_reused", Message: "This idempotency key was already used for a different request.",
	}
)

// ErrOrderHasActivePayment means the order already has a payment that is
// pending, processing or approved (FR08).
var ErrOrderHasActivePayment = errors.New("the order already has an active payment")
