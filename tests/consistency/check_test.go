package main

import (
	"strings"
	"testing"
)

const (
	merchant = "00000000-0000-4000-8000-000000000001"
	p1       = "01a18855-249b-7d8a-9aec-f70f853b0001"
	p2       = "01a18855-249b-7d8a-9aec-f70f853b0002"
	p3       = "01a18855-249b-7d8a-9aec-f70f853b0003"
	p4       = "01a18855-249b-7d8a-9aec-f70f853b0004"
)

// settled is a run where everything agrees: an approval, a decline, a
// rejected request and a payment reversed after its attempts ran out.
func settled() Snapshot {
	return Snapshot{
		Payments: []Payment{
			{ID: p1, MerchantID: merchant, OrderID: "o1", Key: "k1", Status: "APPROVED"},
			{ID: p2, MerchantID: merchant, OrderID: "o2", Key: "k2", Status: "DECLINED"},
			{ID: p3, MerchantID: merchant, OrderID: "o3", Key: "k3", Status: "FAILED", Reason: "acquirer_rejected"},
			{ID: p4, MerchantID: merchant, OrderID: "o4", Key: "k4", Status: "FAILED", Reason: "acquirer_unavailable"},
		},
		Book: []BookEntry{
			{Key: p1, Outcome: "approved", Live: true},
			{Key: p2, Outcome: "declined"},
			{Key: p4, Outcome: "approved", Reversed: true},
			// A test that calls the simulator directly, with a key of its own.
			{Key: "ci-1", Outcome: "approved", Live: true},
		},
		Deliveries: []Delivery{
			{EventID: "e1", EventType: "payment.approved", Status: "delivered", PaymentID: p1, PaymentStatus: "APPROVED"},
			{EventID: "e2", EventType: "payment.declined", Status: "delivered", PaymentID: p2, PaymentStatus: "DECLINED"},
			{EventID: "e3", EventType: "payment.failed", Status: "delivered", PaymentID: p3, PaymentStatus: "FAILED"},
			{EventID: "e4", EventType: "payment.failed", Status: "delivered", PaymentID: p4, PaymentStatus: "FAILED"},
		},
	}
}

func violated(results []Result) map[string][]string {
	out := map[string][]string{}
	for _, r := range results {
		if len(r.Violations) > 0 {
			out[r.Invariant] = r.Violations
		}
	}
	return out
}

func TestASettledRunHoldsEveryInvariant(t *testing.T) {
	if v := violated(Check(settled())); len(v) != 0 {
		t.Errorf("got violations %v, want none", v)
	}
}

func TestEachBrokenInvariantIsReported(t *testing.T) {
	tests := []struct {
		name      string
		damage    func(*Snapshot)
		invariant string
		mentions  string
	}{
		{"a key with two payments", func(s *Snapshot) {
			s.Payments = append(s.Payments, Payment{ID: "p5", MerchantID: merchant, OrderID: "o5", Key: "k1", Status: "DECLINED"})
		}, "INV01", `key "k1"`},
		{"an order with two live payments", func(s *Snapshot) {
			s.Payments[1].OrderID, s.Payments[1].Status = "o1", "PROCESSING"
		}, "INV02", `order "o1"`},
		{"a FAILED payment whose authorization is live", func(s *Snapshot) {
			s.Book[2].Reversed, s.Book[2].Live = false, true
		}, "INV03", p4},
		{"an APPROVED payment the acquirer never approved", func(s *Snapshot) {
			s.Book[0].Live, s.Book[0].Reversed = false, true
		}, "INV03", p1},
		{"money held for a key without an approved payment", func(s *Snapshot) {
			s.Book = append(s.Book, BookEntry{Key: "01a18855-249b-7d8a-9aec-f70f853b0099", Outcome: "approved", Live: true})
		}, "INV03", "0099"},
		{"a payment left PROCESSING", func(s *Snapshot) {
			s.Payments[2].Status = "PROCESSING"
		}, "INV04", p3},
		{"a webhook that says another status", func(s *Snapshot) {
			s.Deliveries[1].PaymentStatus, s.Deliveries[1].EventType = "APPROVED", "payment.approved"
		}, "INV05", "e2"},
		{"a final state without a webhook", func(s *Snapshot) {
			s.Deliveries = s.Deliveries[:3]
		}, "INV06", p4},
		{"a webhook never delivered", func(s *Snapshot) {
			s.Deliveries[0].Status = "failed"
		}, "INV06", p1},
		{"events left in the outbox", func(s *Snapshot) {
			s.OutboxPending = 3
		}, "INV06", "outbox"},
	}
	for _, tt := range tests {
		s := settled()
		tt.damage(&s)
		v := violated(Check(s))
		got := strings.Join(v[tt.invariant], "; ")
		if !strings.Contains(got, tt.mentions) {
			t.Errorf("%s: got violations %v, want %s to mention %q", tt.name, v, tt.invariant, tt.mentions)
		}
	}
}

func TestTheSystemIsQuietOnlyWhenNothingIsInFlight(t *testing.T) {
	s := settled()
	if !quiet(s) {
		t.Fatal("a settled run is not quiet")
	}
	for name, change := range map[string]func(*Snapshot){
		"a payment PROCESSING":    func(s *Snapshot) { s.Payments[0].Status = "PROCESSING" },
		"a webhook pending":       func(s *Snapshot) { s.Deliveries[0].Status = "pending" },
		"events in the outbox":    func(s *Snapshot) { s.OutboxPending = 1 },
		"an event still in Kafka": func(s *Snapshot) { s.Deliveries = s.Deliveries[1:] },
	} {
		s := settled()
		change(&s)
		if quiet(s) {
			t.Errorf("with %s, the system counts as quiet", name)
		}
	}
}
