// Command migrate applies the schema of one database:
//
//	migrate <database> up
//	migrate <database> status
//
// It reads the connection string from DATABASE_URL. It runs as its own step
// before the services start, never inside them (ADR 0003).
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/logging"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

func main() {
	log := logging.New(os.Stdout, "migrate", slog.LevelInfo)
	if err := run(context.Background(), os.Args[1:], config.FromOS(), log); err != nil {
		log.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, env *config.Env, log *slog.Logger) error {
	if len(args) != 2 || (args[1] != "up" && args[1] != "status") {
		return errors.New("usage: migrate <database> up|status")
	}
	database, action := args[0], args[1]
	url := env.Required("DATABASE_URL")
	if err := env.Err(); err != nil {
		return err
	}
	fsys, err := migrations.For(database)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open the %s database: %w", database, err)
	}
	defer db.Close()

	if action == "status" {
		statuses, err := migrate.Status(ctx, db, fsys)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			log.Info("migration", "database", database, "version", s.Source.Version, "state", string(s.State))
		}
		return nil
	}
	applied, err := migrate.Up(ctx, db, fsys)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "database", database, "versions", applied)
	return nil
}
