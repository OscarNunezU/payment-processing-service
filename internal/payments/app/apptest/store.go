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
	// Err, when set, is what every call returns, as a database that is down.
	Err error
	// RecordErr, when set, is what RecordAnswer returns, as a database that
	// fails right after the acquirer answered.
	RecordErr error
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{merchants: map[[sha256.Size]byte]domain.Merchant{}, history: map[string][]domain.HistoryEntry{}}
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

// TakeOver increments a payment's epoch, as the recovery does when it claims
// the payment.
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
	if s.Err != nil {
		return s.Err
	}
	if s.RecordErr != nil {
		return s.RecordErr
	}
	for i, q := range s.payments {
		if q.ID != p.ID {
			continue
		}
		if q.Status != domain.PaymentProcessing || q.Epoch != p.Epoch {
			return app.ErrStaleEpoch
		}
		s.payments[i] = p
		h := s.history[p.ID]
		for j := range h {
			if h[j].Kind == domain.HistoryAcquirerCall && h[j].Attempt == p.Attempts {
				h[j].Outcome, h[j].ResponseCode, h[j].LatencyMS = string(a.Outcome), a.ResponseCode, int(a.Latency.Milliseconds())
			}
		}
		if p.Status.Final() {
			h = append(h, domain.HistoryEntry{Kind: domain.HistoryStatus, Status: p.Status, Reason: string(p.Reason), At: p.UpdatedAt})
		}
		s.history[p.ID] = h
		if final != nil {
			s.outbox = append(s.outbox, *final)
		}
		return nil
	}
	return app.ErrNotFound
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
