// Command payments-api serves the public payments API (RFC 0001, section 11):
// it authorizes each payment with the acquirer inside its request, and answers
// queries. It talks to Postgres and the acquirer only, never to Kafka.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"uuid"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/acquirer"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/httpapi"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/health"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/logging"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/telemetry"
)

const service = "payments-api"

type settings struct {
	httpAddr        string
	databaseURL     string
	maxConns        int
	acquirerURL     string
	acquirerKey     string
	acquirerTimeout time.Duration
	maxInFlight     int
	tracesEndpoint  string
	metricsEndpoint string
	version         string
	logLevel        slog.Level
	shutdownGrace   time.Duration
}

func loadSettings(env *config.Env) (settings, error) {
	s := settings{
		httpAddr:    env.String("HTTP_ADDR", ":8080"),
		databaseURL: env.Required("DATABASE_URL"),
		// Each payment holds a connection for T1 and for T2; under load, 20
		// were not enough and requests waited for one (step 9, the README).
		maxConns:    env.Int("DATABASE_MAX_CONNS", 50, 500),
		acquirerURL: env.Required("ACQUIRER_URL"),
		acquirerKey: env.Required("ACQUIRER_SECRET_KEY"),
		// The time budget and the load shedding of RFC 0001, sections 5.4 and
		// 5.6. A call must end before the payment's lease does.
		acquirerTimeout: env.DurationUpTo("ACQUIRER_TIMEOUT", domain.AcquirerTimeout, domain.MaxAcquirerTimeout),
		maxInFlight:     env.Int("MAX_PAYMENTS_IN_FLIGHT", 512, 100_000),
		tracesEndpoint:  env.String("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", ""),
		metricsEndpoint: env.String("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", ""),
		version:         env.String("SERVICE_VERSION", "dev"),
		logLevel:        env.LogLevel("LOG_LEVEL", slog.LevelInfo),
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

	shutdownTelemetry, err := telemetry.Setup(ctx, telemetry.Config{
		Service: service, Version: s.version, TracesEndpoint: s.tracesEndpoint, MetricsEndpoint: s.metricsEndpoint,
	})
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			log.Warn("could not flush the last spans and metrics", "error", err)
		}
	}()

	db, err := platformpg.Open(ctx, s.databaseURL, service, int32(s.maxConns)) //nolint:gosec // loadSettings caps it at 500
	if err != nil {
		return err
	}
	defer db.Close()

	breaker := acquirer.NewBreaker(5, 30*time.Second, time.Now)
	if err := errors.Join(acquirer.ObserveBreaker(breaker), platformpg.ObservePool(db)); err != nil {
		return fmt.Errorf("observe the circuit breaker and the pool: %w", err)
	}
	store := postgres.New(db)
	svc := app.NewService(app.Deps{
		Merchants: store,
		Payments:  store,
		Acquirer:  acquirer.NewClient(s.acquirerURL, s.acquirerKey, s.acquirerTimeout, breaker),
		Gate:      acquirer.NewGate(breaker, s.maxInFlight),
		NewID:     func() string { return uuid.NewV7().String() },
		Now:       time.Now,
		Random:    rand.Float64, //nolint:gosec // jitter, not a secret
		Log:       log,
	})

	mux := http.NewServeMux()
	httpx.Handle(mux, "GET /healthz", http.HandlerFunc(health.Live))
	httpx.Handle(mux, "GET /readyz", health.Ready(log, time.Second,
		health.Check{Name: "postgres", Critical: true, Run: db.Ping},
		// An open breaker is reported, but the instance stays in rotation: it
		// still answers queries and retries of existing payments.
		health.Check{Name: "acquirer", Run: func(context.Context) error {
			if breaker.Open() {
				return errors.New("the circuit breaker is open")
			}
			return nil
		}},
	))
	httpapi.New(svc, log).Register(mux)
	handler := httpx.Trace(service, httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux))))

	log.Info("listening", "addr", s.httpAddr, "version", s.version)
	if err := httpx.Serve(ctx, httpx.NewServer(s.httpAddr, handler), s.shutdownGrace); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped cleanly")
	return nil
}
