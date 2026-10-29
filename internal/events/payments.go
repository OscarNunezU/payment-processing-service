// Package events holds the contracts of the events on Kafka (RFC 0001, section
// 8.1). Producers and consumers share these types; a change that breaks them
// fails the golden-file test, and needs a new topic version instead.
package events

import "time"

// PaymentsTopic carries every event of every payment, keyed by payment id, so
// the events of one payment keep their order.
const PaymentsTopic = "payments.events.v1"

// Headers on every record, so a consumer can skip the events it does not want
// without decoding them.
const (
	HeaderEventID   = "event-id"
	HeaderEventType = "event-type"
)

// Type names what happened to the payment. Each payment produces one event,
// when it reaches its final state.
type Type string

const (
	PaymentApproved Type = "payment.approved"
	PaymentDeclined Type = "payment.declined"
	PaymentFailed   Type = "payment.failed"
)

// PaymentEvent is the envelope of every payment event. ID is unique per event,
// and it is also the id of the webhook that reports it, so a consumer that
// sees an event twice can tell.
type PaymentEvent struct {
	ID         string    `json:"id"`
	Type       Type      `json:"type"`
	PaymentID  string    `json:"payment_id"`
	MerchantID string    `json:"merchant_id"`
	OccurredAt time.Time `json:"occurred_at"`
	Data       Payment   `json:"data"`
}

// Payment is the payment as the API shows it, at the moment of the event.
type Payment struct {
	ID                string    `json:"id"`
	OrderID           string    `json:"order_id"`
	Status            string    `json:"status"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Card              *Card     `json:"card,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	AuthorizationCode string    `json:"authorization_code,omitempty"`
	DeclineCode       string    `json:"decline_code,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Card is what the acquirer reports about the card: never its number.
type Card struct {
	Brand string `json:"brand"`
	Last4 string `json:"last4"`
}
