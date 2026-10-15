package payment

import "time"

type Transaction struct {
	ID          string
	MerchantID  string
	Amount      int64
	Currency    string
	Status      Status
	CardLast4   string
	CardData    []byte // sealed card details; nil once the payment is final
	Reason      string // why the payment ended DECLINED or FAILED
	Attempts    int
	AvailableAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
