// Command acquirer-sim runs the simulated acquirer (RFC 0001, section 10). The
// public port serves tokens, authorizations and reversals; the admin port
// serves the book, the faults and the health probe, and keeps working while
// the public port is down.
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
	"syscall"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/acquirersim"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/logging"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/telemetry"
)

const service = "acquirer-sim"

type settings struct {
	publicAddr      string
	adminAddr       string
	sim             acquirersim.Config
	tracesEndpoint  string
	metricsEndpoint string
	version         string
	logLevel        slog.Level
	shutdownGrace   time.Duration
}

func loadSettings(env *config.Env) (settings, error) {
	s := settings{
		publicAddr: env.String("PUBLIC_ADDR", ":8090"),
		adminAddr:  env.String("ADMIN_ADDR", ":8091"),
		sim: acquirersim.Config{
			SecretKey:      env.Required("ACQUIRER_SECRET_KEY"),
			PublishableKey: env.Required("ACQUIRER_PUBLISHABLE_KEY"),
			AllowedOrigins: strings.Split(env.String("ALLOWED_ORIGINS", "http://localhost:3000"), ","),
			// Longer than the 5 s payments-worker waits (RFC 0001, section 5.6).
			LateAnswer: env.Duration("LATE_ANSWER", 8*time.Second),
			TokenTTL:   env.Duration("TOKEN_TTL", time.Hour),
		},
		tracesEndpoint:  env.String("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", ""),
		metricsEndpoint: env.String("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", ""),
		version:         env.String("SERVICE_VERSION", "dev"),
		logLevel:        env.LogLevel("LOG_LEVEL", slog.LevelInfo),
		shutdownGrace:   env.Duration("SHUTDOWN_GRACE", 10*time.Second),
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

	wrap := func(h http.Handler) http.Handler {
		return httpx.Trace(service, httpx.RequestID(httpx.AccessLog(log)(httpx.Recover(log)(h))))
	}
	sim := acquirersim.New(s.sim, log)
	public := acquirersim.NewSwitch(s.publicAddr, wrap(sim.PublicHandler()), log)
	sim.Attach(public)
	if err := public.Up(ctx); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownGrace)
		defer cancel()
		if err := public.Shutdown(shutdownCtx); err != nil {
			log.Warn("could not shut the public port down cleanly", "error", err)
		}
	}()

	log.Info("listening", "public_addr", public.Addr(), "admin_addr", s.adminAddr, "version", s.version)
	admin := httpx.NewServer(s.adminAddr, wrap(sim.AdminHandler()))
	if err := httpx.Serve(ctx, admin, s.shutdownGrace); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped cleanly")
	return nil
}
