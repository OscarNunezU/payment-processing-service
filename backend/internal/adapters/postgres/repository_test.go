package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
)

// testRepository connects to DATABASE_URL and applies the migrations. Without
// DATABASE_URL the test is skipped: run make test-integration.
func testRepository(t *testing.T) *Repository {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set: run make test-integration")
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() = %v", err)
	}
	return NewRepository(db)
}

// uniqueMerchant keeps each test's rows apart, so tests never see each
// other's data even when packages run in parallel.
func uniqueMerchant() string {
	return "test-" + rand.Text()
}

func newTransaction(merchant string, status payment.Status) payment.Transaction {
	return payment.Transaction{
		MerchantID: merchant,
		Amount:     15000,
		Currency:   "CLP",
		Status:     status,
		CardLast4:  "1111",
		CardData:   []byte("sealed card data"),
	}
}

func TestCreateThenGet(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, newTransaction(uniqueMerchant(), payment.StatusPending))
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if created.ID == "" || created.CreatedAt.IsZero() || created.AvailableAt.IsZero() {
		t.Fatalf("Create() did not fill id and timestamps: %+v", created)
	}

	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if got.MerchantID != created.MerchantID || got.Amount != 15000 || got.Status != payment.StatusPending ||
		string(got.CardData) != "sealed card data" || got.Attempts != 0 || got.Reason != "" {
		t.Fatalf("Get() = %+v, want what was created", got)
	}
}

func TestGetReturnsNotFound(t *testing.T) {
	repo := testRepository(t)

	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "not-a-uuid"} {
		if _, err := repo.Get(context.Background(), id); !errors.Is(err, payment.ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestListFiltersAndOrdersNewestFirst(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	merchant, other := uniqueMerchant(), uniqueMerchant()

	first, _ := repo.Create(ctx, newTransaction(merchant, payment.StatusPending))
	second, _ := repo.Create(ctx, newTransaction(merchant, payment.StatusApproved))
	repo.Create(ctx, newTransaction(other, payment.StatusPending))

	all, err := repo.List(ctx, payment.ListFilter{MerchantID: merchant, Limit: 50})
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(all) != 2 || all[0].ID != second.ID || all[1].ID != first.ID {
		t.Fatalf("List(merchant) = %d rows, want the 2 of the merchant, newest first", len(all))
	}

	pending, _ := repo.List(ctx, payment.ListFilter{MerchantID: merchant, Status: payment.StatusPending, Limit: 50})
	if len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("List(merchant, PENDING) = %d rows, want 1", len(pending))
	}

	limited, _ := repo.List(ctx, payment.ListFilter{MerchantID: merchant, Limit: 1})
	if len(limited) != 1 {
		t.Fatalf("List(limit 1) = %d rows, want 1", len(limited))
	}
}

func TestDatabaseRejectsAnUnknownStatus(t *testing.T) {
	repo := testRepository(t)

	if _, err := repo.Create(context.Background(), newTransaction(uniqueMerchant(), "PAID")); err == nil {
		t.Fatal("Create() accepted status PAID; the CHECK constraint should reject it")
	}
}
