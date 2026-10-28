package migrate_test

import (
	"database/sql"
	"slices"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUpAppliesEveryMigrationOnce(t *testing.T) {
	db := open(t)
	fsys, err := migrations.For("payments")
	if err != nil {
		t.Fatal(err)
	}

	applied, err := migrate.Up(t.Context(), db, fsys)
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}
	if len(applied) == 0 || applied[0] != 1 {
		t.Fatalf("applied = %v, want to start at version 1", applied)
	}

	again, err := migrate.Up(t.Context(), db, fsys)
	if err != nil || len(again) != 0 {
		t.Fatalf("second Up applied %v (err %v), want nothing", again, err)
	}

	statuses, err := migrate.Status(t.Context(), db, fsys)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if s.State != goose.StateApplied {
			t.Errorf("migration %d is %s, want applied", s.Source.Version, s.State)
		}
	}
}

func TestConcurrentMigrationsApplyEachVersionOnce(t *testing.T) {
	db := open(t)
	fsys, err := migrations.For("payments")
	if err != nil {
		t.Fatal(err)
	}

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all []int64
	)
	for range 3 {
		wg.Go(func() {
			applied, err := migrate.Up(t.Context(), db, fsys)
			if err != nil {
				t.Errorf("Up: %v", err)
				return
			}
			mu.Lock()
			all = append(all, applied...)
			mu.Unlock()
		})
	}
	wg.Wait()

	slices.Sort(all)
	if len(slices.Compact(slices.Clone(all))) != len(all) {
		t.Errorf("versions applied by three migrators at once: %v, want each one once", all)
	}
}

func TestUnknownDatabase(t *testing.T) {
	if _, err := migrations.For("ledger"); err == nil {
		t.Error("For(ledger) = nil error, want no migrations")
	}
}
