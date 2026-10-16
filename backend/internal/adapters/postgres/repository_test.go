package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// newTransaction gets a fresh idempotency key, so every call is a new payment.
func newTransaction(merchant string, status payment.Status) payment.Transaction {
	return payment.Transaction{
		MerchantID:         merchant,
		IdempotencyKey:     "key-" + rand.Text(),
		RequestFingerprint: "fingerprint",
		Amount:             15000,
		Currency:           "CLP",
		Status:             status,
		CardLast4:          "1111",
		CardData:           []byte("sealed card data"),
	}
}

func mustCreate(t *testing.T, repo *Repository, txn payment.Transaction) payment.Transaction {
	t.Helper()
	stored, created, err := repo.Create(context.Background(), txn)
	if err != nil || !created {
		t.Fatalf("Create() = %v, created %v; want a new payment", err, created)
	}
	return stored
}

func mustClaim(t *testing.T, repo *Repository, lease time.Duration) payment.Transaction {
	t.Helper()
	claimed, ok, err := repo.ClaimNext(context.Background(), lease)
	if err != nil || !ok {
		t.Fatalf("ClaimNext() = %v, %v; want a payment", ok, err)
	}
	return claimed
}

// emptyQueue removes every transaction: the queue is shared by all merchants,
// so the claim tests need it to themselves.
func emptyQueue(t *testing.T, repo *Repository) {
	t.Helper()
	if _, err := repo.db.Exec(context.Background(), `TRUNCATE transactions`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func TestCreateThenGet(t *testing.T) {
	repo := testRepository(t)

	created := mustCreate(t, repo, newTransaction(uniqueMerchant(), payment.StatusPending))
	if created.ID == "" || created.CreatedAt.IsZero() || created.AvailableAt.IsZero() {
		t.Fatalf("Create() did not fill id and timestamps: %+v", created)
	}

	got, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if got.MerchantID != created.MerchantID || got.Amount != 15000 || got.Status != payment.StatusPending ||
		string(got.CardData) != "sealed card data" || got.Attempts != 0 || got.Reason != "" || got.AuthorizationCode != "" {
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

	first := mustCreate(t, repo, newTransaction(merchant, payment.StatusPending))
	second := mustCreate(t, repo, newTransaction(merchant, payment.StatusApproved))
	mustCreate(t, repo, newTransaction(other, payment.StatusPending))

	all, err := repo.List(ctx, payment.ListFilter{MerchantID: merchant, Limit: 50})
	if err != nil || len(all) != 2 || all[0].ID != second.ID || all[1].ID != first.ID {
		t.Fatalf("List(merchant) = %d rows, %v; want the 2 of the merchant, newest first", len(all), err)
	}

	pending, err := repo.List(ctx, payment.ListFilter{MerchantID: merchant, Status: payment.StatusPending, Limit: 50})
	if err != nil || len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("List(merchant, PENDING) = %d rows, %v; want 1", len(pending), err)
	}

	limited, err := repo.List(ctx, payment.ListFilter{MerchantID: merchant, Limit: 1})
	if err != nil || len(limited) != 1 {
		t.Fatalf("List(limit 1) = %d rows, %v; want 1", len(limited), err)
	}
}

func TestDatabaseRejectsAnUnknownStatus(t *testing.T) {
	repo := testRepository(t)

	if _, _, err := repo.Create(context.Background(), newTransaction(uniqueMerchant(), "PAID")); err == nil {
		t.Fatal("Create() accepted status PAID; the CHECK constraint should reject it")
	}
}

func TestClaimNextTakesTheOldestWaitingPayment(t *testing.T) {
	repo := testRepository(t)
	emptyQueue(t, repo)
	merchant := uniqueMerchant()
	first := mustCreate(t, repo, newTransaction(merchant, payment.StatusPending))
	second := mustCreate(t, repo, newTransaction(merchant, payment.StatusPending))

	claimed := mustClaim(t, repo, time.Minute)
	if claimed.ID != first.ID || claimed.Status != payment.StatusProcessing || claimed.Attempts != 1 ||
		claimed.AvailableAt.Sub(claimed.UpdatedAt) != time.Minute {
		t.Fatalf("claimed = %+v, want the oldest payment, PROCESSING, 1 attempt and a one-minute lease", claimed)
	}

	if next := mustClaim(t, repo, time.Minute); next.ID != second.ID {
		t.Fatalf("second claim = %s, want %s: a claimed payment must not be taken again", next.ID, second.ID)
	}
	if _, ok, err := repo.ClaimNext(context.Background(), time.Minute); ok || err != nil {
		t.Fatalf("ClaimNext() on an empty queue = %v, %v; want nothing", ok, err)
	}
}

func TestClaimNextNeverGivesTheSamePaymentToTwoWorkers(t *testing.T) {
	repo := testRepository(t)
	emptyQueue(t, repo)
	mustCreate(t, repo, newTransaction(uniqueMerchant(), payment.StatusPending))

	var (
		wg      sync.WaitGroup
		claimed atomic.Int32
	)
	for range 10 {
		wg.Go(func() {
			if _, ok, err := repo.ClaimNext(context.Background(), time.Minute); err == nil && ok {
				claimed.Add(1)
			}
		})
	}
	wg.Wait()

	if claimed.Load() != 1 {
		t.Fatalf("%d workers claimed the only payment, want exactly 1", claimed.Load())
	}
}

func TestAnExpiredLeaseLetsAnotherWorkerTakeThePayment(t *testing.T) {
	repo := testRepository(t)
	emptyQueue(t, repo)
	created := mustCreate(t, repo, newTransaction(uniqueMerchant(), payment.StatusPending))

	mustClaim(t, repo, 0) // a worker takes it and dies: its lease ends at once

	again := mustClaim(t, repo, time.Minute)
	if again.ID != created.ID || again.Attempts != 2 {
		t.Fatalf("second claim = %+v, want the same payment on its second attempt", again)
	}
}

func TestFinishRecordsTheOutcomeAndWipesTheCard(t *testing.T) {
	repo := testRepository(t)
	emptyQueue(t, repo)
	ctx := context.Background()
	created := mustCreate(t, repo, newTransaction(uniqueMerchant(), payment.StatusPending))
	mustClaim(t, repo, time.Minute)

	approved := payment.Outcome{Status: payment.StatusApproved, AuthorizationCode: "A1B2C3"}
	if err := repo.Finish(ctx, created.ID, approved); err != nil {
		t.Fatalf("Finish() = %v", err)
	}
	got, err := repo.Get(ctx, created.ID)
	if err != nil || got.Status != payment.StatusApproved || got.AuthorizationCode != "A1B2C3" || got.CardData != nil {
		t.Fatalf("after Finish = %+v, %v; want APPROVED with its code and no card data", got, err)
	}

	late := payment.Outcome{Status: payment.StatusDeclined, Reason: "card_declined"}
	if err := repo.Finish(ctx, created.ID, late); !errors.Is(err, payment.ErrNotProcessing) {
		t.Fatalf("second Finish() = %v, want ErrNotProcessing: a final state must not change", err)
	}
}

func TestFinishRequiresAClaimedPayment(t *testing.T) {
	repo := testRepository(t)
	created := mustCreate(t, repo, newTransaction(uniqueMerchant(), payment.StatusPending))

	err := repo.Finish(context.Background(), created.ID, payment.Outcome{Status: payment.StatusApproved})
	if !errors.Is(err, payment.ErrNotProcessing) {
		t.Fatalf("Finish() on a PENDING payment = %v, want ErrNotProcessing", err)
	}
}

func TestFinishRejectsAStatusThatIsNotFinal(t *testing.T) {
	repo := testRepository(t)
	emptyQueue(t, repo)
	ctx := context.Background()
	created := mustCreate(t, repo, newTransaction(uniqueMerchant(), payment.StatusPending))
	mustClaim(t, repo, time.Minute)

	if err := repo.Finish(ctx, created.ID, payment.Outcome{Status: payment.StatusPending}); err == nil {
		t.Fatal("Finish() accepted PENDING as the end of a payment")
	}
	got, err := repo.Get(ctx, created.ID)
	if err != nil || got.Status != payment.StatusProcessing || got.CardData == nil {
		t.Fatalf("after a rejected Finish = %+v, %v; want it still PROCESSING with its card", got, err)
	}
}

func TestCreateIsIdempotentPerMerchant(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	txn := newTransaction(uniqueMerchant(), payment.StatusPending)
	first := mustCreate(t, repo, txn)

	again, created, err := repo.Create(ctx, txn)
	if err != nil || created || again.ID != first.ID || again.RequestFingerprint != "fingerprint" {
		t.Fatalf("second Create() = %s, created %v, %v; want the first payment and created false", again.ID, created, err)
	}

	otherMerchant := txn
	otherMerchant.MerchantID = uniqueMerchant()
	if other := mustCreate(t, repo, otherMerchant); other.ID == first.ID {
		t.Fatal("the same key from another merchant returned the first merchant's payment")
	}
}

func TestFindByIdempotencyKey(t *testing.T) {
	repo := testRepository(t)
	ctx := context.Background()
	created := mustCreate(t, repo, newTransaction(uniqueMerchant(), payment.StatusPending))

	got, found, err := repo.FindByIdempotencyKey(ctx, created.MerchantID, created.IdempotencyKey)
	if err != nil || !found || got.ID != created.ID {
		t.Fatalf("FindByIdempotencyKey() = %s, %v, %v; want the payment", got.ID, found, err)
	}
	if _, found, err := repo.FindByIdempotencyKey(ctx, created.MerchantID, "unknown-key"); found || err != nil {
		t.Fatalf("FindByIdempotencyKey(unknown) = %v, %v; want not found", found, err)
	}
}

func TestConcurrentCreatesWithTheSameKeyStoreOnePayment(t *testing.T) {
	repo := testRepository(t)
	txn := newTransaction(uniqueMerchant(), payment.StatusPending)

	var (
		wg      sync.WaitGroup
		created atomic.Int32
		ids     sync.Map
	)
	for range 10 {
		wg.Go(func() {
			stored, isNew, err := repo.Create(context.Background(), txn)
			if err != nil {
				t.Errorf("Create() = %v", err)
				return
			}
			if isNew {
				created.Add(1)
			}
			ids.Store(stored.ID, true)
		})
	}
	wg.Wait()

	distinct := 0
	ids.Range(func(any, any) bool { distinct++; return true })
	if created.Load() != 1 || distinct != 1 {
		t.Fatalf("%d creates reported new and %d distinct ids, want 1 and 1", created.Load(), distinct)
	}
	list, err := repo.List(context.Background(), payment.ListFilter{MerchantID: txn.MerchantID, Limit: 50})
	if err != nil || len(list) != 1 {
		t.Fatalf("%d payments stored for the merchant, %v; want exactly 1", len(list), err)
	}
}
