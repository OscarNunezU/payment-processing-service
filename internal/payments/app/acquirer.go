package app

import (
	"context"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Acquirer authorizes a payment. Its answer is always classified, so a failed
// call is an answer with an outcome such as OutcomeUncertain, not an error.
type Acquirer interface {
	Authorize(ctx context.Context, r AuthorizationRequest) domain.Answer
}

// AuthorizationRequest is one call to the acquirer. Key is the payment id, on
// every call for the payment, so repeating a call is safe.
type AuthorizationRequest struct {
	Key       string
	Token     string
	Amount    int64
	Currency  domain.Currency
	Reference string
}

// Gate admits a new payment when the acquirer can take one: the circuit
// breaker is closed and there is room in flight. release frees the room once
// the call is over.
type Gate interface {
	Acquire() (release func(), ok bool)
}
