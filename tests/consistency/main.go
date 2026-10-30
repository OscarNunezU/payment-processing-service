// Command consistency checks the invariants of the PRD (section 11) after a
// load test with faults: it compares every payment in the payments database
// with the acquirer simulator's book and with the webhooks the webhooks
// service delivered (PRD, section 13).
//
// It first waits for the system to settle: no payment PROCESSING, no event in
// the outbox or in Kafka, and no webhook pending. Then it checks, prints a
// report, and exits with 1 if any invariant was broken.
//
//	go run ./tests/consistency
//
// The defaults reach the local environment from the host.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	payments := flag.String("payments", "postgres://payments:payments@localhost:5432/payments?sslmode=disable", "the payments database")
	webhooks := flag.String("webhooks", "postgres://webhooks:webhooks@localhost:5432/webhooks?sslmode=disable", "the webhooks database")
	acquirer := flag.String("acquirer", "http://localhost:8091", "the acquirer simulator's admin port")
	settle := flag.Duration("settle", 5*time.Minute, "how long to wait for the system to settle")
	flag.Parse()

	ctx := context.Background()
	src, err := open(ctx, *payments, *webhooks, *acquirer)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer src.close()

	s, settled, err := src.settled(ctx, *settle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	results := Check(s)
	report(os.Stdout, s, settled, results)
	if Violations(results) > 0 {
		os.Exit(1)
	}
}

type sources struct {
	payments, webhooks *pgxpool.Pool
	acquirer           string
}

func open(ctx context.Context, paymentsURL, webhooksURL, acquirer string) (*sources, error) {
	p, err := pgxpool.New(ctx, paymentsURL)
	if err != nil {
		return nil, fmt.Errorf("connect to the payments database: %w", err)
	}
	w, err := pgxpool.New(ctx, webhooksURL)
	if err != nil {
		p.Close()
		return nil, fmt.Errorf("connect to the webhooks database: %w", err)
	}
	return &sources{payments: p, webhooks: w, acquirer: acquirer}, nil
}

func (s *sources) close() {
	s.payments.Close()
	s.webhooks.Close()
}

// settled waits until nothing is left in flight, or until within passes, and
// returns what it read last, and how long it took to settle.
func (s *sources) settled(ctx context.Context, within time.Duration) (Snapshot, time.Duration, error) {
	start := time.Now()
	for {
		snap, err := s.read(ctx)
		if err != nil {
			return Snapshot{}, 0, err
		}
		if quiet(snap) || time.Since(start) > within {
			return snap, time.Since(start), nil
		}
		time.Sleep(2 * time.Second)
	}
}

// quiet reports whether nothing is left to finish: the recovery, the relay,
// the webhooks consumer and the dispatcher are all done. An event the relay
// published that the consumer has not stored yet, still in Kafka, shows as a
// final payment without a delivery. One that never arrives is waited for until
// the time runs out, and INV06 reports it.
func quiet(s Snapshot) bool {
	delivered := make(map[string]bool, len(s.Deliveries))
	for _, d := range s.Deliveries {
		if d.Status == "pending" {
			return false
		}
		delivered[d.PaymentID] = true
	}
	for _, p := range s.Payments {
		if p.Status == "PROCESSING" || !delivered[p.ID] {
			return false
		}
	}
	return s.OutboxPending == 0
}

func (s *sources) read(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	var err error
	if snap.Payments, err = collect(ctx, s.payments, `
		SELECT id::text, merchant_id::text, order_id, idempotency_key, status, coalesce(reason, '') FROM payments`,
		func(row pgx.CollectableRow) (Payment, error) {
			var p Payment
			return p, row.Scan(&p.ID, &p.MerchantID, &p.OrderID, &p.Key, &p.Status, &p.Reason)
		}); err != nil {
		return Snapshot{}, fmt.Errorf("read the payments: %w", err)
	}
	if err := s.payments.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&snap.OutboxPending); err != nil {
		return Snapshot{}, fmt.Errorf("read the outbox: %w", err)
	}
	if snap.Deliveries, err = collect(ctx, s.webhooks, `
		SELECT event_id::text, event_type, status,
		       convert_from(payload, 'UTF8')::jsonb ->> 'payment_id',
		       convert_from(payload, 'UTF8')::jsonb -> 'data' ->> 'status'
		FROM deliveries`,
		func(row pgx.CollectableRow) (Delivery, error) {
			var d Delivery
			return d, row.Scan(&d.EventID, &d.EventType, &d.Status, &d.PaymentID, &d.PaymentStatus)
		}); err != nil {
		return Snapshot{}, fmt.Errorf("read the deliveries: %w", err)
	}
	if snap.Book, err = book(ctx, s.acquirer); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

func collect[T any](ctx context.Context, db *pgxpool.Pool, query string, scan func(pgx.CollectableRow) (T, error)) ([]T, error) {
	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan)
}

func book(ctx context.Context, admin string) ([]BookEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, admin+"/admin/book", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read the acquirer's book: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("read the acquirer's book: " + resp.Status)
	}
	var body struct {
		Data []BookEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("read the acquirer's book: %w", err)
	}
	return body.Data, nil
}
