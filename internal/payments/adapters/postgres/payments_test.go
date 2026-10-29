package postgres_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

const otherMerchantID = "00000000-0000-4000-8000-000000000002"

// paymentsSetup returns a store with two merchants and the pool under it.
func paymentsSetup(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	pool := setup(t)
	store := postgres.New(pool)
	other := merchant()
	other.ID, other.RUT = otherMerchantID, "77123456-9"
	for _, m := range []domain.Merchant{merchant(), other} {
		if err := store.CreateMerchant(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	return store, pool
}

func newPayment(merchantID, key, order string) domain.Payment {
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := domain.PaymentRequest{IdempotencyKey: key, OrderID: order, Amount: 15000, Currency: "CLP", CardToken: "tok_1"}
	return domain.NewPayment(uuid.NewV7().String(), domain.Merchant{ID: merchantID}, r, now)
}

func admitAll() error { return nil }

// insert stores p as a new payment, or fails the test.
func insert(t *testing.T, store *postgres.Store, p domain.Payment) {
	t.Helper()
	if _, created, err := store.InsertPayment(t.Context(), p, admitAll); err != nil || !created {
		t.Fatalf("insert %s: created=%v, %v", p.ID, created, err)
	}
}

func approve(p domain.Payment) (domain.Payment, domain.Answer, *events.PaymentEvent) {
	a := domain.Answer{Outcome: domain.OutcomeApproved, AuthorizationCode: "7T4N4V", CardBrand: "visa", CardLast4: "1111",
		ResponseCode: "200", Latency: 312 * time.Millisecond}
	next := p.WithAnswer(a, p.CreatedAt.Add(312*time.Millisecond), func() float64 { return 0.5 })
	ev := &events.PaymentEvent{ID: uuid.NewV7().String(), Type: events.PaymentApproved, PaymentID: p.ID, MerchantID: p.MerchantID, OccurredAt: next.UpdatedAt}
	return next, a, ev
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestT1RecordsThePaymentAndItsFirstAttempt(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)

	got, err := store.Payment(t.Context(), p.MerchantID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Errorf("read back %+v, want %+v", got, p)
	}
	history, err := store.History(t.Context(), p.ID)
	if err != nil || len(history) != 2 || history[0].Status != domain.PaymentProcessing ||
		history[1].Kind != domain.HistoryAcquirerCall || history[1].Attempt != 1 || history[1].Outcome != "" {
		t.Errorf("history: got %+v, %v, want PROCESSING and attempt 1 in flight", history, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox`); n != 0 {
		t.Errorf("T1 wrote %d events, want none until the payment is final", n)
	}
}

func TestARefusedPaymentIsRolledBack(t *testing.T) {
	store, pool := paymentsSetup(t)
	refuse := errors.New("no capacity")
	p := newPayment(merchant().ID, "k1", "o1")
	if _, created, err := store.InsertPayment(t.Context(), p, func() error { return refuse }); !errors.Is(err, refuse) || created {
		t.Fatalf("got created=%v, %v, want the gate's error", created, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM payments`); n != 0 {
		t.Errorf("%d payments exist, want none", n)
	}
	insert(t, store, newPayment(merchant().ID, "k1", "o1")) // the key is still free
}

func TestATakenKeyReturnsItsPaymentWithoutAdmitting(t *testing.T) {
	store, pool := paymentsSetup(t)
	first := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, first)

	admitted := false
	got, created, err := store.InsertPayment(t.Context(), newPayment(merchant().ID, "k1", "o1"), func() error { admitted = true; return nil })
	if err != nil || created || got.ID != first.ID || admitted {
		t.Errorf("got %+v, created=%v, admitted=%v, %v, want the first payment and no admission", got, created, admitted, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM payment_history`); n != 2 {
		t.Errorf("got %d history rows, want the 2 of the first payment", n)
	}
}

func TestT2RecordsTheFinalStateItsAttemptAndItsEvent(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)

	next, a, ev := approve(p)
	if err := store.RecordAnswer(t.Context(), next, a, ev); err != nil {
		t.Fatal(err)
	}
	got, err := store.Payment(t.Context(), p.MerchantID, p.ID)
	if err != nil || got != next {
		t.Errorf("read back %+v, %v, want %+v", got, err, next)
	}
	history, _ := store.History(t.Context(), p.ID)
	if len(history) != 3 || history[1].Outcome != "approved" || history[1].LatencyMS != 312 || history[1].ResponseCode != "200" ||
		history[2].Status != domain.PaymentApproved {
		t.Errorf("history: got %+v, want the call's outcome and the final state", history)
	}

	var (
		key     string
		payload []byte
		headers map[string]string
	)
	if err := pool.QueryRow(t.Context(), `SELECT key, payload, headers FROM outbox`).Scan(&key, &payload, &headers); err != nil {
		t.Fatal(err)
	}
	var decoded events.PaymentEvent
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if key != p.ID || decoded.ID != ev.ID || headers[events.HeaderEventType] != "payment.approved" || headers[events.HeaderEventID] != ev.ID {
		t.Errorf("outbox: key %s event %+v headers %v", key, decoded, headers)
	}
}

func TestT2WithAnUnclearAnswerMovesTheLease(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)

	a := domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: "timeout", Latency: 5 * time.Second}
	next := p.WithAnswer(a, p.CreatedAt.Add(5*time.Second), func() float64 { return 0.5 })
	if err := store.RecordAnswer(t.Context(), next, a, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Payment(t.Context(), p.MerchantID, p.ID)
	if got.Status != domain.PaymentProcessing || !got.LeaseUntil.Equal(next.LeaseUntil) {
		t.Errorf("got %s with lease %s, want PROCESSING until %s", got.Status, got.LeaseUntil, next.LeaseUntil)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox`); n != 0 {
		t.Errorf("got %d events, want none", n)
	}
}

func TestT2WithAnOldEpochChangesNothing(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)
	// The recovery takes the payment over.
	if _, err := pool.Exec(t.Context(), `UPDATE payments SET epoch = epoch + 1 WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}

	next, a, ev := approve(p)
	if err := store.RecordAnswer(t.Context(), next, a, ev); !errors.Is(err, app.ErrStaleEpoch) {
		t.Fatalf("got %v, want ErrStaleEpoch", err)
	}
	got, _ := store.Payment(t.Context(), p.MerchantID, p.ID)
	if got.Status != domain.PaymentProcessing || count(t, pool, `SELECT count(*) FROM outbox`) != 0 {
		t.Errorf("a stale writer changed the payment: %+v", got)
	}
}

func TestTwentyRequestsWithOneKeyCreateOnePayment(t *testing.T) {
	store, pool := paymentsSetup(t)
	const requests = 20
	ids := make(chan string, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			got, _, err := store.InsertPayment(t.Context(), newPayment(merchant().ID, "k1", "o1"), admitAll)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- got.ID
		})
	}
	wg.Wait()
	close(ids)

	distinct := map[string]bool{}
	for id := range ids {
		distinct[id] = true
	}
	if n := count(t, pool, `SELECT count(*) FROM payments`); len(distinct) != 1 || n != 1 {
		t.Errorf("got %d distinct ids and %d payments, want 1 of each", len(distinct), n)
	}
}

func TestOneOrderGetsOneActivePaymentAtATime(t *testing.T) {
	store, pool := paymentsSetup(t)
	const requests = 10
	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		created, active int
	)
	for i := range requests {
		wg.Go(func() {
			_, ok, err := store.InsertPayment(t.Context(), newPayment(merchant().ID, fmt.Sprintf("k%d", i), "o1"), admitAll)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, domain.ErrOrderHasActivePayment):
				active++
			case err != nil:
				t.Error(err)
			case ok:
				created++
			}
		})
	}
	wg.Wait()
	if created != 1 || active != requests-1 {
		t.Fatalf("got %d created and %d refused, want 1 and %d", created, active, requests-1)
	}

	// After a decline, the order takes a new payment.
	if _, err := pool.Exec(t.Context(), `UPDATE payments SET status = 'DECLINED', lease_until = NULL`); err != nil {
		t.Fatal(err)
	}
	insert(t, store, newPayment(merchant().ID, "k-new", "o1"))
	// Another merchant's order with the same id is another order.
	insert(t, store, newPayment(otherMerchantID, "k1", "o1"))
}

func TestAPaymentIsOnlyFoundByItsMerchant(t *testing.T) {
	store, _ := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)
	if _, err := store.Payment(t.Context(), otherMerchantID, p.ID); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("by id: got %v, want ErrNotFound", err)
	}
	if _, err := store.PaymentByKey(t.Context(), otherMerchantID, "k1"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("by key: got %v, want ErrNotFound", err)
	}
	list, err := store.ListPayments(t.Context(), app.ListFilter{MerchantID: otherMerchantID, Limit: 10})
	if err != nil || len(list) != 0 {
		t.Errorf("list: got %d payments, %v, want none", len(list), err)
	}
}

func TestListFiltersAndPages(t *testing.T) {
	store, pool := paymentsSetup(t)
	var ids []string
	for i := range 5 {
		p := newPayment(merchant().ID, fmt.Sprintf("k%d", i), fmt.Sprintf("o%d", i))
		insert(t, store, p)
		ids = append(ids, p.ID)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE payments SET status = 'APPROVED', lease_until = NULL WHERE id = $1`, ids[1]); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListPayments(t.Context(), app.ListFilter{MerchantID: merchant().ID, Limit: 3})
	if err != nil || len(page) != 3 || page[0].ID != ids[4] || page[2].ID != ids[2] {
		t.Fatalf("first page: got %v, %v", paymentIDs(page), err)
	}
	page, err = store.ListPayments(t.Context(), app.ListFilter{MerchantID: merchant().ID, Limit: 3, StartingAfter: ids[2]})
	if err != nil || len(page) != 2 || page[0].ID != ids[1] || page[1].ID != ids[0] {
		t.Errorf("second page: got %v, %v", paymentIDs(page), err)
	}
	page, err = store.ListPayments(t.Context(), app.ListFilter{MerchantID: merchant().ID, Limit: 10, Status: domain.PaymentApproved})
	if err != nil || len(page) != 1 || page[0].ID != ids[1] {
		t.Errorf("by status: got %v, %v", paymentIDs(page), err)
	}
	page, err = store.ListPayments(t.Context(), app.ListFilter{MerchantID: merchant().ID, Limit: 10, OrderID: "o1"})
	if err != nil || len(page) != 1 || page[0].ID != ids[1] {
		t.Errorf("by order: got %v, %v", paymentIDs(page), err)
	}
}

func paymentIDs(ps []domain.Payment) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}

func TestTheDatabaseKeepsAPaymentConsistent(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)
	for name, query := range map[string]string{
		"a FAILED payment without a reason":   `UPDATE payments SET status = 'FAILED', lease_until = NULL`,
		"a PROCESSING payment with a reason":  `UPDATE payments SET reason = 'acquirer_unavailable'`,
		"a PROCESSING payment without lease":  `UPDATE payments SET lease_until = NULL`,
		"an APPROVED payment with a lease":    `UPDATE payments SET status = 'APPROVED'`,
		"the PENDING status of the old model": `UPDATE payments SET status = 'PENDING'`,
	} {
		if _, err := pool.Exec(t.Context(), query); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
}

// Migration 00004 moves the payments of the background design to this one: a
// PENDING payment was never sent, so it ends FAILED with nothing authorized.
func TestMigration00004FailsThePaymentsThatWereNeverSent(t *testing.T) {
	url := pgtest.Start(t)
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fsys, err := migrations.For("payments")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	pool, err := platformpg.Open(t.Context(), url, "test", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := postgres.New(pool).CreateMerchant(t.Context(), merchant()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO payments (id, merchant_id, order_id, idempotency_key, request_hash, amount, currency, card_token,
		                      status, created_at, updated_at)
		VALUES ($1, $2, 'o1', 'k1', $3, 15000, 'CLP', 'tok_1', 'PENDING', now(), now())`,
		uuid.NewV7().String(), merchant().ID, make([]byte, 32),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, reason string
	if err := pool.QueryRow(t.Context(), `SELECT status, reason FROM payments`).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" || reason != "acquirer_unavailable" {
		t.Errorf("got %s %s, want FAILED acquirer_unavailable", status, reason)
	}
}
