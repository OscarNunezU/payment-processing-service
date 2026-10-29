package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Paging of the list (RFC 0001, section 11).
const (
	DefaultListLimit = 50
	MaxListLimit     = 100
)

// ErrInvalidCursor means starting_after is not a payment id.
var ErrInvalidCursor = errors.New("starting_after must be a payment id")

// Payments is the part of the store that keeps payments.
type Payments interface {
	// InsertPayment stores p as PENDING, with its first history entry and the
	// outbox row of accepted, all in one transaction. When the key already
	// holds a payment, nothing is stored and that payment comes back with
	// created false. When the order already has an active payment, the error
	// is domain.ErrOrderHasActivePayment.
	InsertPayment(ctx context.Context, p domain.Payment, accepted events.PaymentEvent) (stored domain.Payment, created bool, err error)
	// PaymentByKey returns the merchant's payment that holds key, or ErrNotFound.
	PaymentByKey(ctx context.Context, merchantID, key string) (domain.Payment, error)
	// Payment returns the merchant's payment with id, or ErrNotFound, also
	// when the payment belongs to another merchant.
	Payment(ctx context.Context, merchantID, id string) (domain.Payment, error)
	// History returns a payment's history, oldest first.
	History(ctx context.Context, paymentID string) ([]domain.HistoryEntry, error)
	// ListPayments returns up to f.Limit payments that match f, newest first.
	ListPayments(ctx context.Context, f ListFilter) ([]domain.Payment, error)
}

// ListFilter selects a page of a merchant's payments.
type ListFilter struct {
	MerchantID string
	Status     domain.PaymentStatus
	OrderID    string
	// StartingAfter is the id of the last payment of the previous page.
	StartingAfter string
	Limit         int
}

// Page is one page of payments, and whether more follow.
type Page struct {
	Payments []domain.Payment
	HasMore  bool
}

// Service runs the use cases of payments-api.
type Service struct {
	merchants Merchants
	payments  Payments
	newID     func() string
	now       func() time.Time
}

// NewService returns the use cases on top of the store. newID returns
// time-ordered ids, so that the newest payments sort first.
func NewService(merchants Merchants, payments Payments, newID func() string, now func() time.Time) *Service {
	return &Service{merchants: merchants, payments: payments, newID: newID, now: now}
}

// Authenticate returns the merchant that owns the secret API key.
func (s *Service) Authenticate(ctx context.Context, secret string) (domain.Merchant, error) {
	return Authenticate(ctx, s.merchants, secret)
}

// Accept creates the payment r asks for, once per idempotency key (RFC 0001,
// sections 5.1 and 6). It returns the payment and whether it is a replay: a
// retry of a request that already created it.
//
// The insert comes first, and the key is looked up only on a conflict, so a
// new payment costs one transaction. A request that breaks a business rule
// also looks its key up: a retry of a payment already accepted gets it back,
// even if a limit changed since.
func (s *Service) Accept(ctx context.Context, m domain.Merchant, r domain.PaymentRequest) (domain.Payment, bool, error) {
	if err := r.Validate(); err != nil {
		return domain.Payment{}, false, err
	}
	if ruleErr := domain.CheckRules(m, r); ruleErr != nil {
		existing, err := s.payments.PaymentByKey(ctx, m.ID, r.IdempotencyKey)
		switch {
		case errors.Is(err, ErrNotFound):
			return domain.Payment{}, false, ruleErr
		case err != nil:
			// The payment might exist, so the rule cannot be the answer.
			return domain.Payment{}, false, fmt.Errorf("look up the key of a request that broke a rule: %w", err)
		}
		return replay(existing, r)
	}

	// Postgres keeps microseconds: the payment that comes back from the store
	// must be the same one that went in.
	now := s.now().UTC().Truncate(time.Microsecond)
	p := domain.NewPayment(s.newID(), m, r, now)
	stored, created, err := s.payments.InsertPayment(ctx, p, paymentEvent(events.PaymentAccepted, s.newID(), p, now))
	if err != nil {
		return domain.Payment{}, false, err
	}
	if !created {
		return replay(stored, r)
	}
	return stored, false, nil
}

func replay(existing domain.Payment, r domain.PaymentRequest) (domain.Payment, bool, error) {
	p, err := domain.Replay(existing, r)
	if err != nil {
		return domain.Payment{}, false, err
	}
	return p, true, nil
}

// Payment returns the merchant's payment with id and its history. Another
// merchant's payment, and an id that is not one, are ErrNotFound (FR04).
func (s *Service) Payment(ctx context.Context, merchantID, id string) (domain.Payment, []domain.HistoryEntry, error) {
	if !domain.IsPaymentID(id) {
		return domain.Payment{}, nil, ErrNotFound
	}
	p, err := s.payments.Payment(ctx, merchantID, id)
	if err != nil {
		return domain.Payment{}, nil, err
	}
	history, err := s.payments.History(ctx, p.ID)
	if err != nil {
		return domain.Payment{}, nil, err
	}
	return p, history, nil
}

// List returns a page of the merchant's payments, newest first. Without a
// limit it returns DefaultListLimit, and never more than MaxListLimit.
func (s *Service) List(ctx context.Context, f ListFilter) (Page, error) {
	if f.StartingAfter != "" && !domain.IsPaymentID(f.StartingAfter) {
		return Page{}, ErrInvalidCursor
	}
	if f.Limit <= 0 {
		f.Limit = DefaultListLimit
	}
	f.Limit = min(f.Limit, MaxListLimit)
	limit := f.Limit
	f.Limit++ // one more tells whether another page follows
	payments, err := s.payments.ListPayments(ctx, f)
	if err != nil {
		return Page{}, err
	}
	if len(payments) > limit {
		return Page{Payments: payments[:limit], HasMore: true}, nil
	}
	return Page{Payments: payments}, nil
}

// paymentEvent is the event of type t about p, as it was at at.
func paymentEvent(t events.Type, id string, p domain.Payment, at time.Time) events.PaymentEvent {
	data := events.Payment{
		ID:                p.ID,
		OrderID:           p.OrderID,
		Status:            string(p.Status),
		Amount:            p.Amount,
		Currency:          string(p.Currency),
		Reason:            string(p.Reason),
		AuthorizationCode: p.AuthorizationCode,
		DeclineCode:       p.DeclineCode,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
	}
	if p.CardLast4 != "" {
		data.Card = &events.Card{Brand: p.CardBrand, Last4: p.CardLast4}
	}
	return events.PaymentEvent{ID: id, Type: t, PaymentID: p.ID, MerchantID: p.MerchantID, OccurredAt: at, Data: data}
}
