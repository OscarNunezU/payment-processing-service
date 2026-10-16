package payment

import "time"

type Transaction struct {
	ID                string
	MerchantID        string
	Amount            int64
	Currency          string
	Status            Status
	CardLast4         string
	CardData          []byte // sealed card details; nil once the payment is final
	Reason            string // why the payment ended DECLINED or FAILED
	AuthorizationCode string // given by the acquirer when it approves
	Attempts          int
	AvailableAt       time.Time // when a worker may take it: a lease or a retry delay
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Outcome is how a processed payment ends.
type Outcome struct {
	Status            Status
	Reason            string
	AuthorizationCode string
}
