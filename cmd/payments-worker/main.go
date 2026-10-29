// Command payments-worker runs the work of the payments service that happens
// outside a request: the relay that publishes the outbox to Kafka (RFC 0001,
// section 8.2), and the recovery of the payments whose outcome is still
// unknown (section 5.5). It serves its probes on its own port.
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
	"strings"
	"sync"
	"syscall"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/acquirer"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/recovery"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/health"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/logging"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/outbox"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/telemetry"
)

const service = "payments-worker"

type settings struct {
	httpAddr        string
	databaseURL     string
	maxConns        int
	kafkaBrokers    []string
	acquirerURL     string
	acquirerKey     string
	acquirerTimeout time.Duration
	tracesEndpoint  string
	metricsEndpoint string
	version         string
	logLevel        slog.Level
	shutdownGrace   time.Duration
}

func loadSettings(env *config.Env) (settings, error) {
	s := settings{
		httpAddr:     env.String("HTTP_ADDR", ":8081"),
		databaseURL:  env.Required("DATABASE_URL"),
		maxConns:     env.Int("DATABASE_MAX_CONNS", 10, 500),
		kafkaBrokers: strings.Split(env.Required("KAFKA_BROKERS"), ","),
		acquirerURL:  env.Required("ACQUIRER_URL"),
		acquirerKey:  env.Required("ACQUIRER_SECRET_KEY"),
		// The same timeout as the API's, and the same limit: the recovery's
		// calls also end before the lease of their claim (RFC 0001, section
		// 5.6).
		acquirerTimeout: env.DurationUpTo("ACQUIRER_TIMEOUT", domain.AcquirerTimeout, domain.MaxAcquirerTimeout),
		tracesEndpoint:  env.String("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", ""),
		metricsEndpoint: env.String("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", ""),
		version:         env.String("SERVICE_VERSION", "dev"),
		logLevel:        env.LogLevel("LOG_LEVEL", slog.LevelInfo),
		shutdownGrace:   env.Duration("SHUTDOWN_GRACE", 20*time.Second),
	}
	return s, env.Err()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, config.FromOS())
	stop()
	if err != nil {
		slog.Error(service+" stopped", "error", err)
		os.Exit(1)
	}
}

// run works until ctx ends, or until the probes stop serving.
func run(ctx context.Context, env *config.Env) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s, err := loadSettings(env)
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

	// Kafka is not a startup dependency: while it is down, the outbox grows
	// and the relay keeps trying.
	kafka, err := kgo.NewClient(
		kgo.SeedBrokers(s.kafkaBrokers...),
		kgo.RecordDeliveryTimeout(30*time.Second),
	)
	if err != nil {
		return fmt.Errorf("create the Kafka client: %w", err)
	}
	defer kafka.Close()

	// The recovery has a breaker of its own, which opens on the failures of
	// its attempts (RFC 0001, section 5.4).
	breaker := acquirer.NewBreaker(5, 30*time.Second, time.Now)
	store := postgres.New(db)
	// What the worker watches over: its breaker, the payments it may have to
	// recover, and the events waiting for Kafka.
	if err := errors.Join(
		acquirer.ObserveBreaker(breaker),
		postgres.ObserveRecovery(store, time.Now),
		outbox.ObservePending(db),
	); err != nil {
		return fmt.Errorf("observe the worker: %w", err)
	}
	svc := app.NewService(app.Deps{
		Payments: store,
		Acquirer: acquirer.NewClient(s.acquirerURL, s.acquirerKey, s.acquirerTimeout, breaker),
		Breaker:  breaker,
		NewID:    func() string { return uuid.NewV7().String() },
		Now:      time.Now,
		Random:   rand.Float64, //nolint:gosec // jitter, not a secret
		// A payment finished here joins the trace of its request (NFR07).
		ResumeTrace: telemetry.Resume,
		Log:         log,
	})

	var wg sync.WaitGroup
	wg.Go(func() { outbox.NewRelay(db, kafka, log).Run(ctx) })
	wg.Go(func() { recovery.New(svc, log).Run(ctx) })

	mux := http.NewServeMux()
	httpx.Handle(mux, "GET /healthz", http.HandlerFunc(health.Live))
	httpx.Handle(mux, "GET /readyz", health.Ready(log, time.Second,
		health.Check{Name: "postgres", Critical: true, Run: db.Ping},
		health.Check{Name: "kafka", Run: kafka.Ping},
		health.Check{Name: "acquirer", Run: func(context.Context) error {
			if breaker.Open() {
				return errors.New("the circuit breaker is open")
			}
			return nil
		}},
	))
	handler := httpx.Trace(service, httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux))))

	log.Info("running", "probes", s.httpAddr, "version", s.version)
	err = httpx.Serve(ctx, httpx.NewServer(s.httpAddr, handler), s.shutdownGrace)
	// The relay and the recovery stop with the probes, also when they could
	// not start: a worker that cannot be probed must exit, so it gets
	// restarted. The recovery finishes the payments it claimed first.
	cancel()
	wg.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped cleanly")
	return nil
}
