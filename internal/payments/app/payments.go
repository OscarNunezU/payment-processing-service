package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Paging of the list (RFC 0001, section 11).
const (
	DefaultListLimit = 50
	MaxListLimit     = 100
)

var (
	// ErrInvalidCursor means starting_after is not a payment id.
	ErrInvalidCursor = errors.New("starting_after must be a payment id")
	// ErrAcquirerUnavailable means a new payment was refused before being
	// created: the circuit breaker is open, or the instance has as many
	// payments in flight as it takes (RFC 0001, section 5.4).
	ErrAcquirerUnavailable = errors.New("the acquirer cannot take a payment now")
	// ErrStaleEpoch means someone else took the payment over, so this writer
	// may no longer change it.
	ErrStaleEpoch = errors.New("the payment was taken over")
)

// Payments is the part of the store that keeps payments.
type Payments interface {
	// InsertPayment is T1 (RFC 0001, section 5.1). It inserts p, unless its
	// key already holds a payment: then nothing changes and that payment
	// comes back with created false. For a new payment, it calls admit before
	// committing; if admit fails, it rolls back and returns admit's error, so
	// nothing is created. Otherwise it records the first attempt and commits.
	// When the order already has an active payment, the error is
	// domain.ErrOrderHasActivePayment.
	InsertPayment(ctx context.Context, p domain.Payment, admit func() error) (stored domain.Payment, created bool, err error)
	// RecordAnswer is T2 (RFC 0001, section 5.2). It records the answer to
	// p's current attempt and p's new state, and, when final is not nil, the
	// final event in the outbox, all in one transaction. It applies only if
	// the stored payment is still PROCESSING with p's epoch; otherwise it
	// returns ErrStaleEpoch.
	RecordAnswer(ctx context.Context, p domain.Payment, a domain.Answer, final *events.PaymentEvent) error
	// PaymentByKey returns the merchant's payment that holds key, or ErrNotFound.
	PaymentByKey(ctx context.Context, merchantID, key string) (domain.Payment, error)
	// Payment returns the merchant's payment with id, or ErrNotFound, also
	// when the payment belongs to another merchant.
	Payment(ctx context.Context, merchantID, id string) (domain.Payment, error)
	// History returns a payment's history, oldest first.
	History(ctx context.Context, paymentID string) ([]domain.HistoryEntry, error)
	// ListPayments returns up to f.Limit payments that match f, newest first.
	ListPayments(ctx context.Context, f ListFilter) ([]domain.Payment, error)

	// The recovery (RFC 0001, section 5.5). Every write but the claim applies
	// only if the stored payment is still PROCESSING with p's epoch, and
	// returns ErrStaleEpoch otherwise.

	// ClaimDue takes over up to limit PROCESSING payments whose lease expired
	// at now: it increments their epoch and moves their lease domain.Lease
	// forward. Payments that another worker is claiming are skipped.
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]Claim, error)
	// StartAttempt records attempt p.Attempts at at, before its call.
	StartAttempt(ctx context.Context, p domain.Payment, at time.Time) error
	// StartReversal records a reversal at at, before it is sent, and when the
	// reversal started: p.ReversalStartedAt.
	StartReversal(ctx context.Context, p domain.Payment, at time.Time) error
	// RecordReversal records the answer to the last reversal and p's new
	// state, and, when final is not nil, the final event in the outbox.
	RecordReversal(ctx context.Context, p domain.Payment, a domain.Answer, final *events.PaymentEvent) error
	// Finish records p's final state, reached without a call, and its event.
	Finish(ctx context.Context, p domain.Payment, final events.PaymentEvent) error
	// Unresolved summarizes the payments still PROCESSING that were created
	// before createdBefore.
	Unresolved(ctx context.Context, createdBefore time.Time) (Unresolved, error)
}

// Claim is a payment the recovery took over.
type Claim struct {
	Payment domain.Payment
	// Reached is whether any of its attempts may have reached the acquirer:
	// false only when every one was refused before it was sent.
	Reached bool
}

// Unresolved is the alert of the recovery (INV04): how many payments are
// still PROCESSING too long after they were created, and the oldest of them.
type Unresolved struct {
	Count           int
	OldestID        string
	OldestCreatedAt time.Time
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

// Deps are what the use cases run on.
type Deps struct {
	Merchants Merchants
	Payments  Payments
	Acquirer  Acquirer
	Gate      Gate
	// NewID returns time-ordered ids, so that the newest payments sort first.
	NewID func() string
	Now   func() time.Time
	// Random returns a number in [0, 1), for the jitter of the backoff.
	Random func() float64
	Log    *slog.Logger
}

// Service runs the use cases of payments-api.
type Service struct {
	d Deps
}

// NewService returns the use cases on top of d.
func NewService(d Deps) *Service { return &Service{d: d} }

// Authenticate returns the merchant that owns the secret API key.
func (s *Service) Authenticate(ctx context.Context, secret string) (domain.Merchant, error) {
	return Authenticate(ctx, s.d.Merchants, secret)
}

// Accept creates the payment r asks for, once per idempotency key, and
// authorizes it within the request (RFC 0001, sections 5.1, 5.2 and 6). It
// returns the payment, final or still PROCESSING, and whether it is a replay:
// a retry of a request that already created it.
//
// The insert comes first, and the key is looked up only on a conflict, so a
// new payment costs one transaction. A retry never goes through the gate, so
// a merchant that lost a response always gets its payment back. A request
// that breaks a business rule also looks its key up: a retry of a payment
// already accepted gets it back, even if a limit changed since.
func (s *Service) Accept(ctx context.Context, m domain.Merchant, r domain.PaymentRequest) (domain.Payment, bool, error) {
	if err := r.Validate(); err != nil {
		return domain.Payment{}, false, err
	}
	if ruleErr := domain.CheckRules(m, r); ruleErr != nil {
		existing, err := s.d.Payments.PaymentByKey(ctx, m.ID, r.IdempotencyKey)
		switch {
		case errors.Is(err, ErrNotFound):
			return domain.Payment{}, false, ruleErr
		case err != nil:
			// The payment might exist, so the rule cannot be the answer.
			return domain.Payment{}, false, fmt.Errorf("look up the key of a request that broke a rule: %w", err)
		}
		return replay(existing, r)
	}

	p := domain.NewPayment(s.d.NewID(), m, r, s.now())
	var release func()
	admit := func() error {
		rel, ok := s.d.Gate.Acquire()
		if !ok {
			return ErrAcquirerUnavailable
		}
		release = rel
		return nil
	}
	stored, created, err := s.d.Payments.InsertPayment(ctx, p, admit)
	if release != nil {
		defer release()
	}
	if err != nil {
		return domain.Payment{}, false, err
	}
	if !created {
		return replay(stored, r)
	}

	// The payment exists now. If the merchant hangs up, the call and its
	// answer still finish, so the outcome is recorded instead of left for the
	// recovery to find out.
	ctx = context.WithoutCancel(ctx)
	a := s.d.Acquirer.Authorize(ctx, AuthorizationRequest{
		Key: p.ID, Token: p.CardToken, Amount: p.Amount, Currency: p.Currency, Reference: p.OrderID,
	})
	return s.record(ctx, p, a), false, nil
}

// record applies the answer to p and stores it (T2). It returns the payment as
// the merchant should see it. When T2 cannot be stored, p stays PROCESSING
// and the recovery repeats the call with the same key once the lease expires,
// so the answer is never lost, only delayed.
func (s *Service) record(ctx context.Context, p domain.Payment, a domain.Answer) domain.Payment {
	now := s.now()
	next := p.WithAnswer(a, now, s.d.Random)
	var final *events.PaymentEvent
	if next.Status.Final() {
		e := paymentEvent(eventType(next.Status), s.d.NewID(), next, now)
		final = &e
	}
	err := s.d.Payments.RecordAnswer(ctx, next, a, final)
	switch {
	case err == nil:
		return next
	case errors.Is(err, ErrStaleEpoch):
		current, err := s.d.Payments.Payment(ctx, p.MerchantID, p.ID)
		if err == nil {
			return current
		}
		s.d.Log.WarnContext(ctx, "could not read a payment taken over by the recovery", "payment_id", p.ID, "error", err)
	default:
		s.d.Log.ErrorContext(ctx, "could not record the acquirer's answer; the recovery will repeat the call",
			"payment_id", p.ID, "outcome", a.Outcome, "error", err)
	}
	return p
}

// now is the time in UTC, to the microsecond, as Postgres keeps it: the
// payment that comes back from the store must be the one that went in.
func (s *Service) now() time.Time { return s.d.Now().UTC().Truncate(time.Microsecond) }

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
	p, err := s.d.Payments.Payment(ctx, merchantID, id)
	if err != nil {
		return domain.Payment{}, nil, err
	}
	history, err := s.d.Payments.History(ctx, p.ID)
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
	payments, err := s.d.Payments.ListPayments(ctx, f)
	if err != nil {
		return Page{}, err
	}
	if len(payments) > limit {
		return Page{Payments: payments[:limit], HasMore: true}, nil
	}
	return Page{Payments: payments}, nil
}

func eventType(s domain.PaymentStatus) events.Type {
	switch s {
	case domain.PaymentApproved:
		return events.PaymentApproved
	case domain.PaymentDeclined:
		return events.PaymentDeclined
	default:
		return events.PaymentFailed
	}
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
