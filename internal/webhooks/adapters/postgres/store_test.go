package postgres_test

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

var endpoint = domain.Endpoint{
	MerchantID: "00000000-0000-4000-8000-000000000001", URL: "http://web:3000/api/webhooks", Secret: "whsec_test_tienda_uno_0123456789",
}

// setup starts Postgres with the webhooks schema and one endpoint.
func setup(t *testing.T) (*postgres.Store, *pgxpool.Pool) {
	t.Helper()
	url := pgtest.Start(t)
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fsys, err := migrations.For("webhooks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(t.Context(), db, fsys); err != nil {
		t.Fatal(err)
	}
	pool, err := platformpg.Open(t.Context(), url, "test", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.New(pool)
	if err := store.CreateEndpoint(t.Context(), endpoint, time.Now()); err != nil {
		t.Fatal(err)
	}
	return store, pool
}

func newDelivery(eventID string, at time.Time) domain.Delivery {
	return domain.NewDelivery(uuid.NewV7().String(), eventID, "payment.approved", endpoint.MerchantID,
		fmt.Appendf(nil, `{"id":%q}`, eventID), "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		at.UTC().Truncate(time.Microsecond))
}

func add(t *testing.T, store *postgres.Store, d domain.Delivery) {
	t.Helper()
	if created, err := store.AddDelivery(t.Context(), d); err != nil || !created {
		t.Fatalf("add %s: created=%v, %v", d.EventID, created, err)
	}
}

func TestAnEndpointIsFoundByItsMerchant(t *testing.T) {
	store, _ := setup(t)
	if got, err := store.Endpoint(t.Context(), endpoint.MerchantID); err != nil || got != endpoint {
		t.Errorf("got %+v, %v, want %+v", got, err, endpoint)
	}
	if _, err := store.Endpoint(t.Context(), "00000000-0000-4000-8000-000000000009"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another merchant: got %v, want ErrNotFound", err)
	}
	// The seed runs again, with another URL: the endpoint stays.
	again := endpoint
	again.URL = "http://elsewhere/webhooks"
	if err := store.CreateEndpoint(t.Context(), again, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Endpoint(t.Context(), endpoint.MerchantID); got.URL != endpoint.URL {
		t.Errorf("the endpoint changed to %s", got.URL)
	}
}

func TestAnEventGetsOneDelivery(t *testing.T) {
	store, pool := setup(t)
	eventID := uuid.NewV7().String()
	add(t, store, newDelivery(eventID, time.Now()))
	if created, err := store.AddDelivery(t.Context(), newDelivery(eventID, time.Now())); err != nil || created {
		t.Errorf("the event again: created=%v, %v, want nothing created", created, err)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM deliveries`).Scan(&n); err != nil || n != 1 {
		t.Errorf("got %d deliveries, %v, want 1", n, err)
	}
}

func TestTheClaimTakesTheDueDeliveriesWithTheirEndpoint(t *testing.T) {
	store, _ := setup(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	due := newDelivery(uuid.NewV7().String(), now)
	add(t, store, due)
	later := newDelivery(uuid.NewV7().String(), now.Add(time.Minute))
	add(t, store, later)

	claimed, err := store.ClaimDue(t.Context(), now, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("got %+v, %v, want only the delivery due", claimed, err)
	}
	got := claimed[0]
	if got.Delivery.EventID != due.EventID || got.Delivery.Attempts != 1 || !got.Delivery.NextAttemptAt.Equal(now.Add(domain.Lease)) ||
		string(got.Delivery.Payload) != string(due.Payload) || got.Delivery.Traceparent != due.Traceparent || got.Endpoint != endpoint {
		t.Errorf("got %+v, want attempt 1 leased, with its body, trace and endpoint", got)
	}
	if again, _ := store.ClaimDue(t.Context(), now, 10); len(again) != 0 {
		t.Errorf("a second claim got %+v, want nothing: the delivery is leased", again)
	}
}

func TestConcurrentClaimsNeverTakeTheSameDelivery(t *testing.T) {
	store, _ := setup(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const deliveries = 20
	for range deliveries {
		add(t, store, newDelivery(uuid.NewV7().String(), now))
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed = map[string]int{}
	)
	for range 4 {
		wg.Go(func() {
			due, err := store.ClaimDue(t.Context(), now, 10)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range due {
				claimed[d.Delivery.ID]++
			}
		})
	}
	wg.Wait()
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("delivery %s was claimed %d times", id, n)
		}
	}
	if len(claimed) != deliveries {
		t.Errorf("%d deliveries were claimed, want %d", len(claimed), deliveries)
	}
}

func TestAnAttemptIsRecordedOnlyByTheLastClaim(t *testing.T) {
	store, _ := setup(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	add(t, store, newDelivery(uuid.NewV7().String(), now))
	first, _ := store.ClaimDue(t.Context(), now, 10)
	// The first dispatcher is slow: its lease ends, and another claims it.
	if second, _ := store.ClaimDue(t.Context(), now.Add(domain.Lease), 10); len(second) != 1 {
		t.Fatalf("the second claim got %d deliveries, want 1", len(second))
	}
	delivered := first[0].Delivery.After(domain.Attempt{StatusCode: 200}, now.Add(domain.Lease), func() float64 { return 0 })
	if err := store.RecordAttempt(t.Context(), delivered); !errors.Is(err, app.ErrStale) {
		t.Errorf("got %v, want ErrStale", err)
	}
}

func TestAnAttemptMovesTheDeliveryOn(t *testing.T) {
	store, pool := setup(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	add(t, store, newDelivery(uuid.NewV7().String(), now))
	claimed, _ := store.ClaimDue(t.Context(), now, 10)

	failed := claimed[0].Delivery.After(domain.Attempt{StatusCode: 500}, now.Add(time.Second), func() float64 { return 0 })
	if err := store.RecordAttempt(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.ClaimDue(t.Context(), failed.NextAttemptAt.Add(-time.Millisecond), 10); len(again) != 0 {
		t.Fatalf("a failed delivery was due before its wait ended")
	}
	claimed, _ = store.ClaimDue(t.Context(), failed.NextAttemptAt, 10)
	if len(claimed) != 1 || claimed[0].Delivery.Attempts != 2 || claimed[0].Delivery.LastStatus != 500 {
		t.Fatalf("got %+v, want attempt 2 after the 500", claimed)
	}
	delivered := claimed[0].Delivery.After(domain.Attempt{StatusCode: 204}, failed.NextAttemptAt.Add(time.Second), func() float64 { return 0 })
	if err := store.RecordAttempt(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	var (
		status string
		at     *time.Time
	)
	if err := pool.QueryRow(t.Context(), `SELECT status, delivered_at FROM deliveries`).Scan(&status, &at); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || at == nil || !at.Equal(delivered.DeliveredAt) {
		t.Errorf("got %s at %v, want delivered at %s", status, at, delivered.DeliveredAt)
	}
}

func TestTheDatabaseKeepsADeliveryConsistent(t *testing.T) {
	store, pool := setup(t)
	add(t, store, newDelivery(uuid.NewV7().String(), time.Now()))
	for name, query := range map[string]string{
		"a pending delivery that is never due": `UPDATE deliveries SET next_attempt_at = NULL`,
		"a delivered one without its time":     `UPDATE deliveries SET status = 'delivered', next_attempt_at = NULL`,
		"an unknown status":                    `UPDATE deliveries SET status = 'sent'`,
		"a merchant without an endpoint": `UPDATE deliveries
			SET merchant_id = '00000000-0000-4000-8000-000000000009'`,
		"an endpoint that is not HTTP": `UPDATE endpoints SET url = 'ftp://x'`,
		"a short secret":               `UPDATE endpoints SET secret = 'short'`,
	} {
		if _, err := pool.Exec(t.Context(), query); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
}
