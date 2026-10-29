package app

import (
	"context"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Acquirer authorizes and reverses payments. Its answers are always
// classified, so a failed call is an answer with an outcome such as
// OutcomeUncertain, not an error.
type Acquirer interface {
	Authorize(ctx context.Context, r AuthorizationRequest) domain.Answer
	// Reverse releases whatever key holds at the acquirer, and keeps the key
	// from ever being authorized. Only OutcomeReversed confirms it.
	Reverse(ctx context.Context, key string) domain.Answer
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

// Breaker tells the recovery whether an attempt may go to the acquirer now.
// The API asks the Gate instead, which adds the limit of payments in flight.
type Breaker interface {
	Allow() bool
}
