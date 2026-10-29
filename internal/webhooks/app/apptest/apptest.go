// Package apptest provides in-memory ports for the tests of the webhooks use
// cases. The store keeps the rules the database enforces: one delivery per
// event, and attempts recorded only by the last dispatcher.
package apptest

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

// Store implements app.Store in memory.
type Store struct {
	mu         sync.Mutex
	endpoints  map[string]domain.Endpoint
	deliveries []domain.Delivery
	// Err, when set, is what every call returns, as a database that is down.
	Err error
}

// NewStore returns a store with the endpoints.
func NewStore(endpoints ...domain.Endpoint) *Store {
	s := &Store{endpoints: map[string]domain.Endpoint{}}
	for _, e := range endpoints {
		s.endpoints[e.MerchantID] = e
	}
	return s
}

// SetErr sets the error every call returns, or clears it with nil.
func (s *Store) SetErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Err = err
}

// Deliveries returns the deliveries, oldest first.
func (s *Store) Deliveries() []domain.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.deliveries)
}

// Claim counts a new attempt at the delivery of eventID, as another
// dispatcher that claims it does.
func (s *Store) Claim(eventID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.deliveries {
		if s.deliveries[i].EventID == eventID {
			s.deliveries[i].Attempts++
		}
	}
}

func (s *Store) Endpoint(_ context.Context, merchantID string) (domain.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return domain.Endpoint{}, s.Err
	}
	e, ok := s.endpoints[merchantID]
	if !ok {
		return domain.Endpoint{}, domain.ErrNotFound
	}
	return e, nil
}

func (s *Store) AddDelivery(_ context.Context, d domain.Delivery) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return false, s.Err
	}
	for _, q := range s.deliveries {
		if q.EventID == d.EventID {
			return false, nil
		}
	}
	s.deliveries = append(s.deliveries, d)
	return true, nil
}

func (s *Store) ClaimDue(_ context.Context, now time.Time, limit int) ([]app.Due, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return nil, s.Err
	}
	var due []int
	for i, d := range s.deliveries {
		if d.Status == domain.Pending && !d.NextAttemptAt.After(now) {
			due = append(due, i)
		}
	}
	slices.SortFunc(due, func(a, b int) int {
		return cmp.Compare(s.deliveries[a].NextAttemptAt.UnixNano(), s.deliveries[b].NextAttemptAt.UnixNano())
	})
	var out []app.Due
	for _, i := range due[:min(limit, len(due))] {
		d := &s.deliveries[i]
		d.Attempts++
		d.NextAttemptAt = now.Add(domain.Lease)
		out = append(out, app.Due{Delivery: *d, Endpoint: s.endpoints[d.MerchantID]})
	}
	return out, nil
}

func (s *Store) RecordAttempt(_ context.Context, d domain.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	for i, q := range s.deliveries {
		if q.ID != d.ID {
			continue
		}
		if q.Status != domain.Pending || q.Attempts != d.Attempts {
			return app.ErrStale
		}
		s.deliveries[i] = d
		return nil
	}
	return domain.ErrNotFound
}

// Sender answers each POST with the next attempt of Script, and every POST
// after them with Attempt. It remembers the requests.
type Sender struct {
	mu       sync.Mutex
	Script   []domain.Attempt
	Attempt  domain.Attempt
	requests []app.Request
	// During, when set, runs during each POST.
	During func(app.Request)
}

func (s *Sender) Send(_ context.Context, r app.Request) domain.Attempt {
	s.mu.Lock()
	s.requests = append(s.requests, r)
	n := len(s.requests)
	during := s.During
	s.mu.Unlock()
	if during != nil {
		during(r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= len(s.Script) {
		return s.Script[n-1]
	}
	return s.Attempt
}

// Requests returns the POSTs sent so far.
func (s *Sender) Requests() []app.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}
