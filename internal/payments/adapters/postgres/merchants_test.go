package postgres_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

// setup starts Postgres, applies the payments migrations and returns a pool.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	if _, err := migrate.Up(t.Context(), db, fsys); err != nil {
		t.Fatal(err)
	}
	pool, err := platformpg.Open(t.Context(), url, "test", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func merchant() domain.Merchant {
	return domain.Merchant{
		ID:        "00000000-0000-4000-8000-000000000001",
		LegalName: "Tienda Uno SpA",
		RUT:       "76543210-3",
		Status:    domain.MerchantActive,
		MaxAmount: map[domain.Currency]int64{domain.CLP: 5_000_000, domain.USD: 500_000},
	}
}

func TestAMerchantIsFoundByItsKey(t *testing.T) {
	store := postgres.New(setup(t))
	secret := domain.NewAPIKey()
	if err := store.CreateMerchant(t.Context(), merchant(), secret); err != nil {
		t.Fatal(err)
	}

	hash, _ := domain.HashAPIKey(secret)
	got, err := store.MerchantByAPIKey(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	want := merchant()
	if got.ID != want.ID || got.LegalName != want.LegalName || got.RUT != want.RUT || got.Status != want.Status ||
		got.MaxAmount[domain.CLP] != 5_000_000 || got.MaxAmount[domain.USD] != 500_000 || len(got.MaxAmount) != 2 {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestUnknownAndRevokedKeysFindNothing(t *testing.T) {
	pool := setup(t)
	store := postgres.New(pool)
	secret := domain.NewAPIKey()
	if err := store.CreateMerchant(t.Context(), merchant(), secret); err != nil {
		t.Fatal(err)
	}

	unknown, _ := domain.HashAPIKey(domain.NewAPIKey())
	if _, err := store.MerchantByAPIKey(t.Context(), unknown); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("unknown key: got %v, want ErrNotFound", err)
	}

	hash, _ := domain.HashAPIKey(secret)
	if _, err := pool.Exec(t.Context(), `UPDATE api_keys SET revoked_at = now() + interval '1 hour' WHERE hash = $1`, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MerchantByAPIKey(t.Context(), hash); err != nil {
		t.Errorf("a key revoked in an hour still works now: got %v", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE api_keys SET revoked_at = now() WHERE hash = $1`, hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MerchantByAPIKey(t.Context(), hash); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("revoked key: got %v, want ErrNotFound", err)
	}
}

func TestCreatingAMerchantTwiceChangesNothing(t *testing.T) {
	pool := setup(t)
	store := postgres.New(pool)
	secret := domain.NewAPIKey()
	for range 2 {
		if err := store.CreateMerchant(t.Context(), merchant(), secret); err != nil {
			t.Fatal(err)
		}
	}
	var merchants, limits, keys int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM merchants), (SELECT count(*) FROM merchant_limits), (SELECT count(*) FROM api_keys)`).
		Scan(&merchants, &limits, &keys); err != nil {
		t.Fatal(err)
	}
	if merchants != 1 || limits != 2 || keys != 1 {
		t.Errorf("got %d merchants, %d limits and %d keys, want 1, 2 and 1", merchants, limits, keys)
	}
}

func TestTheDatabaseRejectsAMalformedMerchant(t *testing.T) {
	store := postgres.New(setup(t))
	bad := merchant()
	bad.RUT = "76.543.210-3" // not canonical
	if err := store.CreateMerchant(t.Context(), bad); err == nil {
		t.Error("a RUT with dots was stored")
	}
	bad = merchant()
	bad.Status = "PAUSED"
	if err := store.CreateMerchant(t.Context(), bad); err == nil {
		t.Error("an unknown status was stored")
	}
}
