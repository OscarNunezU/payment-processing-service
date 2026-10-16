package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/adapters/acquirer"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/adapters/api"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/adapters/vault"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/adapters/worker"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/config"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/core/payment"
	"github.com/OscarNunezU/payment-processing-service/backend/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := logging.New()
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// A payment must be able to wait for the acquirer and still finish within
	// its process timeout; otherwise every payment would be cut and retried.
	if cfg.AcquirerTimeout >= worker.ProcessTimeout {
		return fmt.Errorf("ACQUIRER_TIMEOUT (%s) must be shorter than the worker's process timeout (%s)",
			cfg.AcquirerTimeout, worker.ProcessTimeout)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer db.Close()

	if err := postgres.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	logger.Info("database migrated")

	cardVault, err := vault.New(cfg.CardEncryptionKey)
	if err != nil {
		return fmt.Errorf("create card vault: %w", err)
	}
	repo := postgres.NewRepository(db)
	payments := payment.NewService(repo, cardVault, payment.Limits(cfg.MaxAmounts))
	// The breaker wraps the acquirer for the processor and, as the worker's
	// gate, stops the worker from taking payments while it is open.
	breaker := acquirer.NewBreaker(acquirer.NewClient(cfg.AcquirerURL, cfg.AcquirerTimeout),
		acquirer.DefaultFailureThreshold, acquirer.DefaultCooldown)
	processor := payment.NewProcessor(repo, cardVault, breaker)

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.New(processor, breaker, cfg.WorkerConcurrency).Run(ctx)
	}()
	logger.Info("worker started", "concurrency", cfg.WorkerConcurrency, "acquirer", cfg.AcquirerURL)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewRouter(api.NewHandler(db, payments)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", cfg.HTTPAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		stop() // the server could not start: stop the worker too
		<-workerDone
		return err
	case <-ctx.Done():
	}

	// Stop taking requests, then let the worker finish the payments in flight;
	// only after both is the database closed (deferred above).
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	<-workerDone
	return err
}
