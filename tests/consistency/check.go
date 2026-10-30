package main

import (
	"fmt"
	"slices"
	"uuid"
)

// What the checker reads: the three places that must agree once the system
// has settled (PRD, section 13).

// Payment is a payment as the payments database holds it.
type Payment struct {
	ID, MerchantID, OrderID, Key string
	Status, Reason               string
}

// BookEntry is what the acquirer holds for a key.
type BookEntry struct {
	Key      string
	Outcome  string // approved, declined, or none: no authorization reached it
	Reversed bool
	Live     bool // an authorization that holds money on the card
}

// Delivery is a webhook as the webhooks database holds it, with what its
// event told the merchant.
type Delivery struct {
	EventID, EventType, Status string
	PaymentID, PaymentStatus   string
}

// Snapshot is everything the checks look at, once settled.
type Snapshot struct {
	Payments      []Payment
	Book          []BookEntry
	Deliveries    []Delivery
	OutboxPending int
}

// Result is one invariant, how many records it looked at, and what broke it.
type Result struct {
	Invariant  string
	Rule       string
	Checked    int
	Violations []string
}

// Check runs the invariants of the PRD (section 11) over s.
func Check(s Snapshot) []Result {
	return []Result{oneKeyOnePayment(s), oneLivePaymentPerOrder(s), noMoneyHeldWithoutApproval(s),
		everyPaymentIsFinal(s), webhooksTellTheTruth(s), everyFinalStateIsDelivered(s)}
}

// INV01: one idempotency key creates at most one payment per merchant.
func oneKeyOnePayment(s Snapshot) Result {
	r := Result{Invariant: "INV01", Rule: "one key, one payment", Checked: len(s.Payments)}
	seen := map[[2]string]string{}
	for _, p := range s.Payments {
		k := [2]string{p.MerchantID, p.Key}
		if other, ok := seen[k]; ok {
			r.Violations = append(r.Violations, fmt.Sprintf("key %q holds payments %s and %s", p.Key, other, p.ID))
		}
		seen[k] = p.ID
	}
	return r
}

// INV02: an order never has two payments that are processing or approved.
func oneLivePaymentPerOrder(s Snapshot) Result {
	r := Result{Invariant: "INV02", Rule: "one live or approved payment per order", Checked: len(s.Payments)}
	live := map[[2]string][]string{}
	for _, p := range s.Payments {
		if p.Status == "PROCESSING" || p.Status == "APPROVED" {
			k := [2]string{p.MerchantID, p.OrderID}
			live[k] = append(live[k], p.ID)
		}
	}
	for k, ids := range live {
		if len(ids) > 1 {
			r.Violations = append(r.Violations, fmt.Sprintf("order %q has %d live payments: %v", k[1], len(ids), ids))
		}
	}
	return r
}

// INV03: a payment is authorized at most once, and one that ends FAILED or
// DECLINED leaves no live authorization. Every authorization the acquirer
// holds belongs to an approved payment. Keys that are not payment ids belong
// to tests that call the simulator directly.
func noMoneyHeldWithoutApproval(s Snapshot) Result {
	r := Result{Invariant: "INV03", Rule: "money is held only for approved payments", Checked: len(s.Payments)}
	book := map[string]BookEntry{}
	for _, e := range s.Book {
		book[e.Key] = e
	}
	payments := map[string]Payment{}
	for _, p := range s.Payments {
		payments[p.ID] = p
		e, ok := book[p.ID]
		switch {
		case p.Status == "APPROVED" && (!ok || e.Outcome != "approved" || !e.Live):
			r.Violations = append(r.Violations, fmt.Sprintf("payment %s is APPROVED, but the acquirer holds %+v", p.ID, e))
		case p.Status == "DECLINED" && (!ok || e.Outcome != "declined"):
			r.Violations = append(r.Violations, fmt.Sprintf("payment %s is DECLINED, but the acquirer holds %+v", p.ID, e))
		case p.Status == "FAILED" && ok && e.Live:
			r.Violations = append(r.Violations, fmt.Sprintf("payment %s is FAILED %s, but its authorization is live", p.ID, p.Reason))
		}
	}
	for _, e := range s.Book {
		if !e.Live || !isID(e.Key) {
			continue
		}
		r.Checked++
		if p, ok := payments[e.Key]; !ok || p.Status != "APPROVED" {
			r.Violations = append(r.Violations, fmt.Sprintf("the acquirer holds money for key %s, whose payment is %q", e.Key, p.Status))
		}
	}
	return r
}

// INV04: every accepted payment reaches a final state.
func everyPaymentIsFinal(s Snapshot) Result {
	r := Result{Invariant: "INV04", Rule: "every payment reaches a final state", Checked: len(s.Payments)}
	for _, p := range s.Payments {
		if p.Status == "PROCESSING" {
			r.Violations = append(r.Violations, fmt.Sprintf("payment %s is still PROCESSING", p.ID))
		}
	}
	return r
}

// INV05: what the merchant is told by webhook matches the database, which
// INV03 holds against the acquirer's book.
func webhooksTellTheTruth(s Snapshot) Result {
	r := Result{Invariant: "INV05", Rule: "webhooks match the database and the book", Checked: len(s.Deliveries)}
	status := map[string]string{}
	for _, p := range s.Payments {
		status[p.ID] = p.Status
	}
	types := map[string]string{"APPROVED": "payment.approved", "DECLINED": "payment.declined", "FAILED": "payment.failed"}
	for _, d := range s.Deliveries {
		got, ok := status[d.PaymentID]
		switch {
		case !ok:
			r.Violations = append(r.Violations, fmt.Sprintf("event %s is about payment %s, which does not exist", d.EventID, d.PaymentID))
		case d.PaymentStatus != got || d.EventType != types[got]:
			r.Violations = append(r.Violations, fmt.Sprintf("event %s says %s %s, but payment %s is %s", d.EventID, d.EventType, d.PaymentStatus, d.PaymentID, got))
		}
	}
	return r
}

// INV06: every final state produces one webhook event, delivered. The event
// id is unique among deliveries, so one event is one delivery.
func everyFinalStateIsDelivered(s Snapshot) Result {
	r := Result{Invariant: "INV06", Rule: "every final state is delivered once", Checked: len(s.Payments)}
	deliveries := map[string][]Delivery{}
	for _, d := range s.Deliveries {
		deliveries[d.PaymentID] = append(deliveries[d.PaymentID], d)
	}
	for _, p := range s.Payments {
		if p.Status == "PROCESSING" {
			continue // INV04 reports it
		}
		ds := deliveries[p.ID]
		switch {
		case len(ds) != 1:
			r.Violations = append(r.Violations, fmt.Sprintf("payment %s is %s and has %d webhook events", p.ID, p.Status, len(ds)))
		case ds[0].Status != "delivered":
			r.Violations = append(r.Violations, fmt.Sprintf("the webhook of payment %s is %s", p.ID, ds[0].Status))
		}
	}
	if s.OutboxPending > 0 {
		r.Violations = append(r.Violations, fmt.Sprintf("%d events are still waiting in the outbox", s.OutboxPending))
	}
	return r
}

func isID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s
}

// Violations counts what broke, over every result.
func Violations(results []Result) int {
	n := 0
	for _, r := range results {
		n += len(r.Violations)
	}
	return n
}

// sorted returns the violations in a stable order, for a report that can be
// compared between runs.
func sorted(v []string) []string {
	out := slices.Clone(v)
	slices.Sort(out)
	return out
}
