// The PRD's acceptance scenarios for accepting payments (PRD, section 12),
// through the real HTTP API, the use cases and Postgres.
package payments_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/httpapi"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

const (
	keyUno        = "sk_test_scenario_tienda_uno"
	keyDos        = "sk_test_scenario_tienda_dos"
	keySuspendida = "sk_test_scenario_suspendida"
)

type service struct {
	url string
	db  *pgxpool.Pool
}

// start runs the API on a fresh database with three merchants: two active
// and one suspended.
func start(t *testing.T) *service {
	t.Helper()
	url := pgtest.Start(t)
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	fsys, err := migrations.For("payments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(t.Context(), sqlDB, fsys); err != nil {
		t.Fatal(err)
	}
	db, err := platformpg.Open(t.Context(), url, "scenarios", 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	store := postgres.New(db)
	limits := map[domain.Currency]int64{domain.CLP: 5_000_000}
	for _, m := range []struct {
		merchant domain.Merchant
		key      string
	}{
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000001", LegalName: "Tienda Uno SpA", RUT: "76543210-3", Status: domain.MerchantActive, MaxAmount: limits}, keyUno},
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000002", LegalName: "Tienda Dos Limitada", RUT: "77123456-9", Status: domain.MerchantActive, MaxAmount: limits}, keyDos},
		{domain.Merchant{ID: "00000000-0000-4000-8000-000000000003", LegalName: "Comercio Suspendido SpA", RUT: "76987654-5", Status: domain.MerchantSuspended, MaxAmount: limits}, keySuspendida},
	} {
		if err := store.CreateMerchant(t.Context(), m.merchant, m.key); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	svc := app.NewService(store, store, func() string { return uuid.NewV7().String() }, time.Now)
	httpapi.New(svc, slog.New(slog.DiscardHandler)).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &service{url: srv.URL, db: db}
}

type answer struct {
	status int
	id     string
	code   string
}

func (s *service) pay(t *testing.T, apiKey, idempotencyKey, order string, amount int64) answer {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"order_id": order, "amount": amount, "currency": "CLP", "card_token": "tok_" + order})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+"/v1/payments", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	return s.send(t, req)
}

func (s *service) get(t *testing.T, apiKey, id string) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+"/v1/payments/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return s.send(t, req)
}

func (s *service) send(t *testing.T, req *http.Request) answer {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return answer{}
	}
	defer resp.Body.Close()
	var body struct {
		ID    string `json:"id"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	return answer{status: resp.StatusCode, id: body.ID, code: body.Error.Code}
}

func (s *service) setStatus(t *testing.T, id string, status domain.PaymentStatus) {
	t.Helper()
	if _, err := s.db.Exec(t.Context(), `UPDATE payments SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatal(err)
	}
}

// Scenario: Double click with the same key.
func TestDoubleClickWithTheSameKey(t *testing.T) {
	s := start(t)
	const clicks = 20
	answers := make([]answer, clicks)
	var wg sync.WaitGroup
	for i := range clicks {
		wg.Go(func() { answers[i] = s.pay(t, keyUno, "k1", "o1", 15000) })
	}
	wg.Wait()

	for i, a := range answers {
		if a.status != http.StatusAccepted || a.id != answers[0].id {
			t.Errorf("click %d: got %+v, want 202 with the payment of the first", i, a)
		}
	}
	var count int
	if err := s.db.QueryRow(t.Context(), `SELECT count(*) FROM payments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d payments exist, want exactly 1", count)
	}
}

// Scenario: The same key with a different amount.
func TestTheSameKeyWithADifferentAmount(t *testing.T) {
	s := start(t)
	if a := s.pay(t, keyUno, "k1", "o1", 10000); a.status != http.StatusAccepted {
		t.Fatalf("got %+v", a)
	}
	if a := s.pay(t, keyUno, "k1", "o1", 12000); a.status != http.StatusUnprocessableEntity || a.code != "idempotency_key_reused" {
		t.Errorf("got %+v, want 422 idempotency_key_reused", a)
	}
}

// Scenario: The same order while a payment is live.
func TestTheSameOrderWhileAPaymentIsLive(t *testing.T) {
	s := start(t)
	first := s.pay(t, keyUno, "k1", "o1", 15000)
	s.setStatus(t, first.id, domain.PaymentApproved)
	if a := s.pay(t, keyUno, "k2", "o1", 15000); a.status != http.StatusConflict || a.code != "order_has_active_payment" {
		t.Errorf("got %+v, want 409 order_has_active_payment", a)
	}
}

// Scenario: A new attempt after a decline.
func TestANewAttemptAfterADecline(t *testing.T) {
	s := start(t)
	first := s.pay(t, keyUno, "k1", "o2", 15000)
	s.setStatus(t, first.id, domain.PaymentDeclined)
	if a := s.pay(t, keyUno, "k2", "o2", 15000); a.status != http.StatusAccepted || a.id == first.id {
		t.Errorf("got %+v, want a new payment for the order", a)
	}
}

// Scenario: A suspended merchant.
func TestASuspendedMerchant(t *testing.T) {
	s := start(t)
	if a := s.pay(t, keySuspendida, "k1", "o1", 15000); a.status != http.StatusUnprocessableEntity || a.code != "merchant_suspended" {
		t.Errorf("got %+v, want 422 merchant_suspended", a)
	}
}

// Scenario: Another merchant's payment.
func TestAnotherMerchantsPayment(t *testing.T) {
	s := start(t)
	p := s.pay(t, keyUno, "k1", "o1", 15000)
	if a := s.get(t, keyUno, p.id); a.status != http.StatusOK {
		t.Fatalf("its merchant: got %+v", a)
	}
	if a := s.get(t, keyDos, p.id); a.status != http.StatusNotFound {
		t.Errorf("another merchant: got %+v, want 404", a)
	}
}
