// Package migrate applies a database's schema with goose. Only one process
// migrates a database at a time: the others wait on a Postgres advisory lock
// (ADR 0003).
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Up applies every pending migration in fsys and returns the versions it
// applied, oldest first.
func Up(ctx context.Context, db *sql.DB, fsys fs.FS) ([]int64, error) {
	p, err := provider(db, fsys)
	if err != nil {
		return nil, err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Version)
	}
	return applied, nil
}

// Status returns every migration in fsys with whether it was applied.
func Status(ctx context.Context, db *sql.DB, fsys fs.FS) ([]*goose.MigrationStatus, error) {
	p, err := provider(db, fsys)
	if err != nil {
		return nil, err
	}
	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the migration status: %w", err)
	}
	return statuses, nil
}

func provider(db *sql.DB, fsys fs.FS) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create the migration lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("load the migrations: %w", err)
	}
	return p, nil
}
