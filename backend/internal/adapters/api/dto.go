package api

import (
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

type createPaymentRequest struct {
	MerchantID string      `json:"merchant_id"`
	Amount     int64       `json:"amount"`
	Currency   string      `json:"currency"`
	Card       cardRequest `json:"card"`
}

type cardRequest struct {
	Number      string `json:"number"`
	ExpiryMonth int    `json:"expiry_month"`
	ExpiryYear  int    `json:"expiry_year"`
	CVV         string `json:"cvv"`
}

func (r createPaymentRequest) toNewPayment() payment.NewPayment {
	return payment.NewPayment{
		MerchantID: r.MerchantID,
		Amount:     r.Amount,
		Currency:   r.Currency,
		Card: payment.Card{
			Number:      r.Card.Number,
			ExpiryMonth: r.Card.ExpiryMonth,
			ExpiryYear:  r.Card.ExpiryYear,
			CVV:         r.Card.CVV,
		},
	}
}

// paymentResponse is what a merchant sees of a transaction: never the card
// number, the CVV or the fields the worker uses internally.
type paymentResponse struct {
	TransactionID     string    `json:"transaction_id"`
	MerchantID        string    `json:"merchant_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Status            string    `json:"status"`
	CardLast4         string    `json:"card_last4"`
	Reason            string    `json:"reason,omitempty"`
	AuthorizationCode string    `json:"authorization_code,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// listResponse wraps the list in an object, so pagination fields can be added
// later without breaking clients.
type listResponse struct {
	Payments []paymentResponse `json:"payments"`
}

func toListResponse(txns []payment.Transaction) listResponse {
	resp := listResponse{Payments: make([]paymentResponse, len(txns))}
	for i, txn := range txns {
		resp.Payments[i] = toPaymentResponse(txn)
	}
	return resp
}

func toPaymentResponse(txn payment.Transaction) paymentResponse {
	return paymentResponse{
		TransactionID:     txn.ID,
		MerchantID:        txn.MerchantID,
		Amount:            txn.Amount,
		Currency:          txn.Currency,
		Status:            string(txn.Status),
		CardLast4:         txn.CardLast4,
		Reason:            txn.Reason,
		AuthorizationCode: txn.AuthorizationCode,
		CreatedAt:         txn.CreatedAt,
		UpdatedAt:         txn.UpdatedAt,
	}
}
