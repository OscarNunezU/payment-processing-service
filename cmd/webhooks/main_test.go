package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
)

func TestTheServiceExitsWhenItsProbesCannotStart(t *testing.T) {
	url := pgtest.Start(t)
	busy, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	env := config.New(func(key string) (string, bool) {
		v, ok := map[string]string{
			"DATABASE_URL":  url,
			"KAFKA_BROKERS": "127.0.0.1:1", // never reached: the probes fail first
			"HTTP_ADDR":     busy.Addr().String(),
			"LOG_LEVEL":     "error",
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
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return: the consumer or the dispatcher kept the service alive without its probes")
	}
}
