// Command payments-worker runs the work of the payments service that happens
// outside a request: the relay that publishes the outbox to Kafka (RFC 0001,
// section 8.2), and, from step 6 of the plan, the recovery of the payments in
// doubt (section 5.5). It serves its probes on its own port.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

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
	httpAddr      string
	databaseURL   string
	maxConns      int
	kafkaBrokers  []string
	otlpEndpoint  string
	version       string
	logLevel      slog.Level
	shutdownGrace time.Duration
}

func loadSettings(env *config.Env) (settings, error) {
	s := settings{
		httpAddr:      env.String("HTTP_ADDR", ":8081"),
		databaseURL:   env.Required("DATABASE_URL"),
		maxConns:      env.Int("DATABASE_MAX_CONNS", 10, 500),
		kafkaBrokers:  strings.Split(env.Required("KAFKA_BROKERS"), ","),
		otlpEndpoint:  env.String("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		version:       env.String("SERVICE_VERSION", "dev"),
		logLevel:      env.LogLevel("LOG_LEVEL", slog.LevelInfo),
		shutdownGrace: env.Duration("SHUTDOWN_GRACE", 20*time.Second),
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

	var wg sync.WaitGroup
	wg.Go(func() { outbox.NewRelay(db, kafka, log).Run(ctx) })

	mux := http.NewServeMux()
	httpx.Handle(mux, "GET /healthz", http.HandlerFunc(health.Live))
	httpx.Handle(mux, "GET /readyz", health.Ready(log, time.Second,
		health.Check{Name: "postgres", Critical: true, Run: db.Ping},
		health.Check{Name: "kafka", Run: kafka.Ping},
	))
	handler := httpx.Trace(service, httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(mux))))

	log.Info("running", "probes", s.httpAddr, "version", s.version)
	err = httpx.Serve(ctx, httpx.NewServer(s.httpAddr, handler), s.shutdownGrace)
	// The relay stops with the probes, also when they could not start: a
	// worker that cannot be probed must exit, so it gets restarted.
	cancel()
	wg.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped cleanly")
	return nil
}
