// Command payments-api serves the public payments API. For now it only answers
// its health endpoints; the API arrives in step 4 of the plan (RFC 0001,
// section 17).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/health"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/logging"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/telemetry"
)

const service = "payments-api"

type settings struct {
	httpAddr      string
	databaseURL   string
	maxConns      int
	otlpEndpoint  string
	version       string
	logLevel      slog.Level
	shutdownGrace time.Duration
}

func loadSettings(env *config.Env) (settings, error) {
	s := settings{
		httpAddr:     env.String("HTTP_ADDR", ":8080"),
		databaseURL:  env.Required("DATABASE_URL"),
		maxConns:     env.Int("DATABASE_MAX_CONNS", 20, 500),
		otlpEndpoint: env.String("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		version:      env.String("SERVICE_VERSION", "dev"),
		logLevel:     env.LogLevel("LOG_LEVEL", slog.LevelInfo),
		// In-flight requests get up to 20 s to finish (RFC 0001, section 5.6).
		shutdownGrace: env.Duration("SHUTDOWN_GRACE", 20*time.Second),
	}
	return s, env.Err()
}

func main() {
	if err := run(); err != nil {
		slog.Error(service+" stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := loadSettings(config.FromOS())
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log := logging.New(os.Stdout, service, s.logLevel)
	slog.SetDefault(log)

	shutdownTracing, err := telemetry.Setup(ctx, service, s.version, s.otlpEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(flushCtx); err != nil {
			log.Warn("could not flush the last spans", "error", err)
		}
	}()

	db, err := postgres.Open(ctx, s.databaseURL, service, int32(s.maxConns)) //nolint:gosec // loadSettings caps it at 500
	if err != nil {
		return err
	}
	defer db.Close()

	mux := http.NewServeMux()
	httpx.Handle(mux, "GET /healthz", http.HandlerFunc(health.Live))
	httpx.Handle(mux, "GET /readyz", health.Ready(log, time.Second,
		health.Check{Name: "postgres", Critical: true, Run: db.Ping},
	))
	handler := httpx.Trace(service, httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux))))

	log.Info("listening", "addr", s.httpAddr, "version", s.version)
	if err := httpx.Serve(ctx, httpx.NewServer(s.httpAddr, handler), s.shutdownGrace); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped cleanly")
	return nil
}
