package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
)

func TestTheWorkerExitsWhenItsProbesCannotStart(t *testing.T) {
	url := pgtest.Start(t)
	busy, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	env := config.New(func(key string) (string, bool) {
		v, ok := map[string]string{
			"DATABASE_URL":        url,
			"KAFKA_BROKERS":       "127.0.0.1:1", // never reached: the probes fail first
			"ACQUIRER_URL":        "http://127.0.0.1:1",
			"ACQUIRER_SECRET_KEY": "sk_sim_test",
			"HTTP_ADDR":           busy.Addr().String(),
			"LOG_LEVEL":           "error",
		}[key]
		return v, ok
	})

	done := make(chan error, 1)
	go func() { done <- run(context.Background(), env) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("run returned no error, want the failure to listen")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return: the relay kept the worker alive without its probes")
	}
}

func TestATimeoutTheLeaseCannotCoverIsRefused(t *testing.T) {
	env := config.New(func(key string) (string, bool) {
		v, ok := map[string]string{
			"DATABASE_URL":        "postgres://localhost/payments",
			"KAFKA_BROKERS":       "localhost:9092",
			"ACQUIRER_URL":        "http://localhost:8090",
			"ACQUIRER_SECRET_KEY": "sk_sim_test",
			"ACQUIRER_TIMEOUT":    "15s",
		}[key]
		return v, ok
	})
	if _, err := loadSettings(env); err == nil || !strings.Contains(err.Error(), "ACQUIRER_TIMEOUT must be at most 10s") {
		t.Errorf("got %v, want a 15 s timeout refused: the recovery's call would outlive its claim", err)
	}
}
