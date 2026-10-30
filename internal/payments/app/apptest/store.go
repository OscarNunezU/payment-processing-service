// Package apptest provides in-memory ports for the tests of the use cases
// and of the inbound adapters. The store keeps the rules the database
// enforces (one payment per key, one active payment per order, writes
// conditional on the epoch), so the tests that use it see the same answers
// as with Postgres.
package apptest

import (
	"cmp"
	"context"
	"crypto/sha256"
	"slices"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// Store implements app.Merchants and app.Payments in memory.
type Store struct {
	mu        sync.Mutex
	merchants map[[sha256.Size]byte]domain.Merchant
	payments  []domain.Payment
	history   map[string][]domain.HistoryEntry
	outbox    []events.PaymentEvent
	traces    map[string]string
	// Err, when set, is what every call returns, as a database that is down.
	Err error
	// RecordErr, when set, is what RecordAnswer returns, as a database that
	// fails right after the acquirer answered.
	RecordErr error
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{merchants: map[[sha256.Size]byte]domain.Merchant{}, history: map[string][]domain.HistoryEntry{}, traces: map[string]string{}}
}

// AddMerchant stores m and returns a new API key for it.
func (s *Store) AddMerchant(m domain.Merchant) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	secret := domain.NewAPIKey()
	hash, _ := domain.HashAPIKey(secret)
	s.merchants[hash] = m
	return secret
}

// SetStatus changes a payment's status, as the processing will.
func (s *Store) SetStatus(id string, status domain.PaymentStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.payments {
		if s.payments[i].ID == id {
			s.payments[i].Status = status
		}
	}
}

// SetTrace keeps trace as the trace of the request that created payment id,
// as T1 does with the trace of its context.
func (s *Store) SetTrace(id, trace string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces[id] = trace
}

// TakeOver increments a payment's epoch, as a recovery that claims it does.
func (s *Store) TakeOver(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.payments {
		if s.payments[i].ID == id {
			s.payments[i].Epoch++
		}
	}
}

// Outbox returns the events written so far, oldest first.
func (s *Store) Outbox() []events.PaymentEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.outbox)
}

// Count returns how many payments the store holds.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.payments)
}

func (s *Store) MerchantByAPIKey(_ context.Context, hash [sha256.Size]byte) (domain.Merchant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return domain.Merchant{}, s.Err
	}
	m, ok := s.merchants[hash]
	if !ok {
		return domain.Merchant{}, app.ErrNotFound
	}
	return m, nil
}

func (s *Store) InsertPayment(_ context.Context, p domain.Payment, admit func() error) (domain.Payment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return domain.Payment{}, false, s.Err
	}
	for _, q := range s.payments {
		if q.MerchantID == p.MerchantID && q.IdempotencyKey == p.IdempotencyKey {
			return q, false, nil
		}
	}
	for _, q := range s.payments {
		if q.MerchantID == p.MerchantID && q.OrderID == p.OrderID && active(q.Status) {
			return domain.Payment{}, false, domain.ErrOrderHasActivePayment
		}
	}
	if err := admit(); err != nil {
		return domain.Payment{}, false, err
	}
	s.payments = append(s.payments, p)
	s.history[p.ID] = []domain.HistoryEntry{
		{Kind: domain.HistoryStatus, Status: p.Status, At: p.CreatedAt},
		{Kind: domain.HistoryAcquirerCall, Attempt: p.Attempts, At: p.CreatedAt},
	}
	return p, true, nil
}

func (s *Store) RecordAnswer(_ context.Context, p domain.Payment, a domain.Answer, final *events.PaymentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RecordErr != nil {
		return s.RecordErr
	}
	return s.record(p, final, func(h []domain.HistoryEntry) {
		for j := range h {
			if h[j].Kind == domain.HistoryAcquirerCall && h[j].Attempt == p.Attempts {
				answer(&h[j], a)
			}
		}
	})
}

func (s *Store) ClaimDue(_ context.Context, now time.Time, limit int) ([]app.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	var due []int
	for i, p := range s.payments {
		if p.Status == domain.PaymentProcessing && !p.LeaseUntil.After(now) {
			due = append(due, i)
		}
	}
	slices.SortFunc(due, func(a, b int) int { return s.payments[a].LeaseUntil.Compare(s.payments[b].LeaseUntil) })
	var claims []app.Claim
	for _, i := range due[:min(limit, len(due))] {
		p := &s.payments[i]
		p.Epoch++
		p.LeaseUntil = now.Add(domain.Lease)
		reached := false
		for _, h := range s.history[p.ID] {
			if h.Kind == domain.HistoryAcquirerCall && h.Outcome != string(domain.OutcomeNotSent) {
				reached = true
			}
		}
		claims = append(claims, app.Claim{Payment: *p, Reached: reached, Trace: s.traces[p.ID]})
	}
	return claims, nil
}

func (s *Store) StartAttempt(_ context.Context, p domain.Payment, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.fenced(p)
	if err != nil {
		return err
	}
	stored.Attempts = p.Attempts
	s.history[p.ID] = append(s.history[p.ID], domain.HistoryEntry{Kind: domain.HistoryAcquirerCall, Attempt: p.Attempts, At: at})
	return nil
}

func (s *Store) StartReversal(_ context.Context, p domain.Payment, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.fenced(p)
	if err != nil {
		return err
	}
	stored.ReversalStartedAt = p.ReversalStartedAt
	s.history[p.ID] = append(s.history[p.ID], domain.HistoryEntry{Kind: domain.HistoryReversal, At: at})
	return nil
}

func (s *Store) RecordReversal(_ context.Context, p domain.Payment, a domain.Answer, final *events.PaymentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record(p, final, func(h []domain.HistoryEntry) {
		for j := len(h) - 1; j >= 0; j-- {
			if h[j].Kind == domain.HistoryReversal {
				answer(&h[j], a)
				return
			}
		}
	})
}

func (s *Store) Finish(_ context.Context, p domain.Payment, final events.PaymentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record(p, &final, nil)
}

func (s *Store) Unresolved(_ context.Context, createdBefore time.Time) (app.Unresolved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return app.Unresolved{}, s.Err
	}
	var u app.Unresolved
	for _, p := range s.payments {
		if p.Status != domain.PaymentProcessing || !p.CreatedAt.Before(createdBefore) {
			continue
		}
		if u.Count == 0 || p.CreatedAt.Before(u.OldestCreatedAt) {
			u.OldestID, u.OldestCreatedAt = p.ID, p.CreatedAt
		}
		u.Count++
	}
	return u, nil
}

// record stores p, as the transaction that ends a call does: it applies only
// to a payment still PROCESSING with p's epoch, gives the call its answer
// through call, and with final, adds the final status and the event.
func (s *Store) record(p domain.Payment, final *events.PaymentEvent, call func([]domain.HistoryEntry)) error {
	stored, err := s.fenced(p)
	if err != nil {
		return err
	}
	*stored = p
	h := s.history[p.ID]
	if call != nil {
		call(h)
	}
	if final != nil {
		h = append(h, domain.HistoryEntry{Kind: domain.HistoryStatus, Status: p.Status, Reason: string(p.Reason), At: p.UpdatedAt})
		s.outbox = append(s.outbox, *final)
	}
	s.history[p.ID] = h
	return nil
}

// fenced returns the stored payment p, if it is still PROCESSING with p's
// epoch.
func (s *Store) fenced(p domain.Payment) (*domain.Payment, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	for i := range s.payments {
		if s.payments[i].ID != p.ID {
			continue
		}
		if s.payments[i].Status != domain.PaymentProcessing || s.payments[i].Epoch != p.Epoch {
			return nil, app.ErrStaleEpoch
		}
		return &s.payments[i], nil
	}
	return nil, app.ErrNotFound
}

func answer(h *domain.HistoryEntry, a domain.Answer) {
	h.Outcome, h.ResponseCode, h.LatencyMS = string(a.Outcome), a.ResponseCode, int(a.Latency.Milliseconds())
}

func active(s domain.PaymentStatus) bool {
	return s == domain.PaymentProcessing || s == domain.PaymentApproved
}

func (s *Store) PaymentByKey(_ context.Context, merchantID, key string) (domain.Payment, error) {
	return s.find(func(p domain.Payment) bool { return p.MerchantID == merchantID && p.IdempotencyKey == key })
}

func (s *Store) Payment(_ context.Context, merchantID, id string) (domain.Payment, error) {
	return s.find(func(p domain.Payment) bool { return p.MerchantID == merchantID && p.ID == id })
}

func (s *Store) find(match func(domain.Payment) bool) (domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return domain.Payment{}, s.Err
	}
	for _, p := range s.payments {
		if match(p) {
			return p, nil
		}
	}
	return domain.Payment{}, app.ErrNotFound
}

func (s *Store) PaymentWithHistory(_ context.Context, merchantID, id string) (domain.Payment, []domain.HistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return domain.Payment{}, nil, s.Err
	}
	for _, p := range s.payments {
		if p.MerchantID == merchantID && p.ID == id {
			return p, slices.Clone(s.history[id]), nil
		}
	}
	return domain.Payment{}, nil, app.ErrNotFound
}

func (s *Store) History(_ context.Context, paymentID string) ([]domain.HistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	return slices.Clone(s.history[paymentID]), nil
}

func (s *Store) ListPayments(_ context.Context, f app.ListFilter) ([]domain.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	var out []domain.Payment
	for _, p := range s.payments {
		if p.MerchantID != f.MerchantID ||
			(f.Status != "" && p.Status != f.Status) ||
			(f.OrderID != "" && p.OrderID != f.OrderID) ||
			(f.StartingAfter != "" && p.ID >= f.StartingAfter) {
			continue
		}
		out = append(out, p)
	}
	// Newest first: the ids are time-ordered.
	slices.SortFunc(out, func(a, b domain.Payment) int { return cmp.Compare(b.ID, a.ID) })
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}
