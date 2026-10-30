// Command webhooks runs the webhooks service (RFC 0001, section 9): it
// consumes the payment events from Kafka, turns each final one into a
// delivery, and posts the deliveries to the merchants' endpoints, signed and
// with retries. It has its own database, and serves its probes on its own
// port.
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

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/health"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/logging"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/telemetry"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/dispatcher"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/kafka"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/sender"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

const service = "webhooks"

type settings struct {
	httpAddr        string
	databaseURL     string
	maxConns        int
	kafkaBrokers    []string
	tracesEndpoint  string
	metricsEndpoint string
	version         string
	logLevel        slog.Level
	shutdownGrace   time.Duration
}

func loadSettings(env *config.Env) (settings, error) {
	s := settings{
		httpAddr:        env.String("HTTP_ADDR", ":8082"),
		databaseURL:     env.Required("DATABASE_URL"),
		maxConns:        env.Int("DATABASE_MAX_CONNS", 10, 500),
		kafkaBrokers:    strings.Split(env.Required("KAFKA_BROKERS"), ","),
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

	// Kafka is not a startup dependency: while it is down, the consumer
	// waits, and the deliveries already stored are still sent.
	kafkaClient, err := kgo.NewClient(append(kafka.Options(events.PaymentsTopic),
		kgo.SeedBrokers(s.kafkaBrokers...),
		kgo.RecordDeliveryTimeout(30*time.Second),
	)...)
	if err != nil {
		return fmt.Errorf("create the Kafka client: %w", err)
	}
	defer kafkaClient.Close()

	store := postgres.New(db)
	if err := errors.Join(postgres.ObserveDeliveries(store), platformpg.ObservePool(db)); err != nil {
		return fmt.Errorf("observe the deliveries and the pool: %w", err)
	}
	svc := app.NewService(app.Deps{
		Store:  store,
		Sender: sender.New(domain.Timeout),
		NewID:  func() string { return uuid.NewV7().String() },
		Now:    time.Now,
		Random: rand.Float64, //nolint:gosec // jitter, not a secret
		Log:    log,
	})

	var wg sync.WaitGroup
	wg.Go(func() { kafka.NewConsumer(kafkaClient, svc, log).Run(ctx) })
	wg.Go(func() { dispatcher.New(svc, log).Run(ctx) })

	mux := http.NewServeMux()
	httpx.Handle(mux, "GET /healthz", http.HandlerFunc(health.Live))
	httpx.Handle(mux, "GET /readyz", health.Ready(log, time.Second,
		health.Check{Name: "postgres", Critical: true, Run: db.Ping},
		health.Check{Name: "kafka", Run: kafkaClient.Ping},
	))
	handler := httpx.Trace(service, httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux))))

	log.Info("running", "probes", s.httpAddr, "version", s.version)
	err = httpx.Serve(ctx, httpx.NewServer(s.httpAddr, handler), s.shutdownGrace)
	// The consumer and the dispatcher stop with the probes, also when they
	// could not start: a service that cannot be probed must exit, so it gets
	// restarted. The dispatcher finishes the POSTs it started first.
	cancel()
	wg.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped cleanly")
	return nil
}
