package postgres_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
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

func newPayment(merchantID, key, order string) (domain.Payment, events.PaymentEvent) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := domain.PaymentRequest{IdempotencyKey: key, OrderID: order, Amount: 15000, Currency: "CLP", CardToken: "tok_1"}
	p := domain.NewPayment(uuid.NewV7().String(), domain.Merchant{ID: merchantID}, r, now)
	return p, events.PaymentEvent{ID: uuid.NewV7().String(), Type: events.PaymentAccepted, PaymentID: p.ID, MerchantID: merchantID, OccurredAt: now}
}

func TestInsertStoresThePaymentItsHistoryAndItsEvent(t *testing.T) {
	store, pool := paymentsSetup(t)
	p, ev := newPayment(merchant().ID, "k1", "o1")

	stored, created, err := store.InsertPayment(t.Context(), p, ev)
	if err != nil || !created || stored.ID != p.ID {
		t.Fatalf("got %+v, created=%v, %v", stored, created, err)
	}
	got, err := store.Payment(t.Context(), p.MerchantID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Errorf("read back %+v, want %+v", got, p)
	}

	history, err := store.History(t.Context(), p.ID)
	if err != nil || len(history) != 1 || history[0].Kind != domain.HistoryStatus || history[0].Status != domain.PaymentPending || !history[0].At.Equal(p.CreatedAt) {
		t.Errorf("history: got %+v, %v", history, err)
	}

	var (
		topic, key string
		payload    []byte
		headers    map[string]string
	)
	if err := pool.QueryRow(t.Context(), `SELECT topic, key, payload, headers FROM outbox`).Scan(&topic, &key, &payload, &headers); err != nil {
		t.Fatal(err)
	}
	var decoded events.PaymentEvent
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if topic != events.PaymentsTopic || key != p.ID || decoded.ID != ev.ID || headers[events.HeaderEventType] != "payment.accepted" || headers[events.HeaderEventID] != ev.ID {
		t.Errorf("outbox: topic %s key %s event %+v headers %v", topic, key, decoded, headers)
	}
}

func TestATakenKeyReturnsItsPaymentAndWritesNothing(t *testing.T) {
	store, pool := paymentsSetup(t)
	first, ev := newPayment(merchant().ID, "k1", "o1")
	if _, _, err := store.InsertPayment(t.Context(), first, ev); err != nil {
		t.Fatal(err)
	}
	second, ev2 := newPayment(merchant().ID, "k1", "o1")
	got, created, err := store.InsertPayment(t.Context(), second, ev2)
	if err != nil || created || got.ID != first.ID {
		t.Errorf("got %+v, created=%v, %v, want the first payment", got, created, err)
	}
	var payments, history, outbox int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM payments), (SELECT count(*) FROM payment_history), (SELECT count(*) FROM outbox)`).
		Scan(&payments, &history, &outbox); err != nil {
		t.Fatal(err)
	}
	if payments != 1 || history != 1 || outbox != 1 {
		t.Errorf("got %d payments, %d history rows and %d events, want one of each", payments, history, outbox)
	}
}

func TestTwentyRequestsWithOneKeyCreateOnePayment(t *testing.T) {
	store, pool := paymentsSetup(t)
	const requests = 20
	ids := make(chan string, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			p, ev := newPayment(merchant().ID, "k1", "o1")
			got, _, err := store.InsertPayment(t.Context(), p, ev)
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
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM payments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if len(distinct) != 1 || count != 1 {
		t.Errorf("got %d distinct ids and %d payments, want 1 of each", len(distinct), count)
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
			p, ev := newPayment(merchant().ID, fmt.Sprintf("k%d", i), "o1")
			_, ok, err := store.InsertPayment(t.Context(), p, ev)
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
	if _, err := pool.Exec(t.Context(), `UPDATE payments SET status = 'DECLINED'`); err != nil {
		t.Fatal(err)
	}
	p, ev := newPayment(merchant().ID, "k-new", "o1")
	if _, ok, err := store.InsertPayment(t.Context(), p, ev); err != nil || !ok {
		t.Errorf("after a decline: got created=%v, %v", ok, err)
	}
	// Another merchant's order with the same id is another order.
	p, ev = newPayment(otherMerchantID, "k1", "o1")
	if _, ok, err := store.InsertPayment(t.Context(), p, ev); err != nil || !ok {
		t.Errorf("another merchant's order o1: got created=%v, %v", ok, err)
	}
}

func TestAPaymentIsOnlyFoundByItsMerchant(t *testing.T) {
	store, _ := paymentsSetup(t)
	p, ev := newPayment(merchant().ID, "k1", "o1")
	if _, _, err := store.InsertPayment(t.Context(), p, ev); err != nil {
		t.Fatal(err)
	}
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
		p, ev := newPayment(merchant().ID, fmt.Sprintf("k%d", i), fmt.Sprintf("o%d", i%2))
		if i >= 2 {
			p.OrderID = fmt.Sprintf("o%d", i) // only one active payment per order
		}
		if _, _, err := store.InsertPayment(t.Context(), p, ev); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE payments SET status = 'APPROVED' WHERE id = $1`, ids[1]); err != nil {
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

func TestTheDatabaseRejectsAFailedPaymentWithoutAReason(t *testing.T) {
	store, pool := paymentsSetup(t)
	p, ev := newPayment(merchant().ID, "k1", "o1")
	if _, _, err := store.InsertPayment(t.Context(), p, ev); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE payments SET status = 'FAILED'`); err == nil {
		t.Error("a FAILED payment without a reason was stored")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE payments SET reason = 'expired'`); err == nil {
		t.Error("a PENDING payment with a reason was stored")
	}
}
