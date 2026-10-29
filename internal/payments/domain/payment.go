package domain

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"uuid"
)

// PaymentStatus is where a payment is in its lifecycle (PRD, section 10).
type PaymentStatus string

const (
	// PaymentPending: accepted, and not taken by a worker yet.
	PaymentPending PaymentStatus = "PENDING"
	// PaymentProcessing: a worker has it, and it may have reached the acquirer.
	PaymentProcessing PaymentStatus = "PROCESSING"
	PaymentApproved   PaymentStatus = "APPROVED"
	PaymentDeclined   PaymentStatus = "DECLINED"
	PaymentFailed     PaymentStatus = "FAILED"
)

// ErrInvalidStatus means a status filter names no status.
var ErrInvalidStatus = errors.New("the status does not exist")

// ParsePaymentStatus accepts the five statuses, in upper case.
func ParsePaymentStatus(s string) (PaymentStatus, error) {
	switch st := PaymentStatus(s); st {
	case PaymentPending, PaymentProcessing, PaymentApproved, PaymentDeclined, PaymentFailed:
		return st, nil
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidStatus, s)
}

// Final reports whether the status can no longer change.
func (s PaymentStatus) Final() bool {
	return s == PaymentApproved || s == PaymentDeclined || s == PaymentFailed
}

// FailureReason says why the service failed a payment.
type FailureReason string

const (
	ReasonAcquirerRejected    FailureReason = "acquirer_rejected"
	ReasonAcquirerUnavailable FailureReason = "acquirer_unavailable"
	ReasonExpired             FailureReason = "expired"
)

// Payment is one attempt to charge a card for an order.
type Payment struct {
	ID             string
	MerchantID     string
	OrderID        string
	IdempotencyKey string
	// RequestHash identifies what was asked, to tell a retry from a different
	// request that reuses the key.
	RequestHash       [sha256.Size]byte
	Amount            int64
	Currency          Currency
	CardToken         string
	CardBrand         string
	CardLast4         string
	Status            PaymentStatus
	Reason            FailureReason
	AuthorizationCode string
	DeclineCode       string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// IsPaymentID reports whether s is a payment id in its canonical form, the
// only one the service writes, so a malformed id is answered as not found
// without asking the database.
func IsPaymentID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}

// PaymentRequest is what a merchant sends to create a payment.
type PaymentRequest struct {
	IdempotencyKey string
	OrderID        string
	Amount         int64
	Currency       string
	CardToken      string
}

// Validate checks the shape of the request, and reports every malformed field
// at once so the merchant can fix them in one round trip.
func (r PaymentRequest) Validate() error {
	var fields []FieldError
	add := func(field, message string) { fields = append(fields, FieldError{Field: field, Message: message}) }

	if !printable(r.IdempotencyKey, 255) {
		add("idempotency_key", "must have 1 to 255 printable ASCII characters")
	}
	if !printable(r.OrderID, 64) {
		add("order_id", "must have 1 to 64 printable ASCII characters")
	}
	if r.Amount < 1 {
		add("amount", "must be a positive integer, in the currency's minor unit")
	}
	if len(r.Currency) != 3 || strings.Trim(r.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		add("currency", "must be a 3-letter ISO 4217 code, such as CLP")
	}
	if !printable(r.CardToken, 255) {
		add("card_token", "must be the token the acquirer returned for the card")
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// Hash identifies what the request asks for. The idempotency key is not part
// of it: the hash is what two requests with the same key must share.
func (r PaymentRequest) Hash() [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		r.OrderID, strconv.FormatInt(r.Amount, 10), r.Currency, r.CardToken,
	}, "\x00")))
}

// CheckRules applies the merchant's rules to a valid request: the merchant
// must be active, accept the currency, and allow the amount.
func CheckRules(m Merchant, r PaymentRequest) error {
	if err := m.CanCreatePayments(); err != nil {
		return ErrRuleMerchantSuspended
	}
	currency, err := ParseCurrency(r.Currency)
	if err != nil {
		return ErrRuleCurrencyNotSupported
	}
	limit, ok := m.MaxAmount[currency]
	if !ok {
		return ErrRuleCurrencyNotAccepted
	}
	if r.Amount > limit {
		return ErrRuleAmountExceedsMax
	}
	return nil
}

// NewPayment builds the PENDING payment for a request that passed its rules.
func NewPayment(id string, m Merchant, r PaymentRequest, now time.Time) Payment {
	return Payment{
		ID:             id,
		MerchantID:     m.ID,
		OrderID:        r.OrderID,
		IdempotencyKey: r.IdempotencyKey,
		RequestHash:    r.Hash(),
		Amount:         r.Amount,
		Currency:       Currency(r.Currency),
		CardToken:      r.CardToken,
		Status:         PaymentPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// Replay answers a request whose key already holds a payment: the same
// request gets that payment back, and a different one is rejected.
func Replay(existing Payment, r PaymentRequest) (Payment, error) {
	if existing.RequestHash != r.Hash() {
		return Payment{}, ErrRuleIdempotencyKeyReused
	}
	return existing, nil
}

// HistoryKind says what a history entry records.
type HistoryKind string

const (
	// HistoryStatus is a change of status.
	HistoryStatus HistoryKind = "status"
	// HistoryAcquirerCall is a call to the acquirer, with its outcome.
	HistoryAcquirerCall HistoryKind = "acquirer_call"
)

// HistoryEntry is one line of a payment's history (FR15).
type HistoryEntry struct {
	Kind         HistoryKind
	Status       PaymentStatus
	Attempt      int
	Outcome      string
	LatencyMS    int
	ResponseCode string
	Reason       string
	At           time.Time
}

func printable(s string, maxLen int) bool {
	if s == "" || len(s) > maxLen {
		return false
	}
	for i := range len(s) {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}
