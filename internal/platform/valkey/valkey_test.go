package valkey

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcvalkey "github.com/testcontainers/testcontainers-go/modules/valkey"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestNotConnectedWhileValkeyIsDown(t *testing.T) {
	// A port nothing listens on: grab one and release it.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	l := Connect(addr, 50*time.Millisecond, discard)
	defer l.Close()

	time.Sleep(150 * time.Millisecond)
	if _, ok := l.Client(); ok {
		t.Fatal("Client reports a connection to a closed port")
	}
	if err := l.Check(t.Context()); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Check = %v, want ErrNotConnected", err)
	}
}

func TestConnectsAndPings(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: it needs Docker")
	}
	c, err := tcvalkey.Run(t.Context(), "valkey/valkey:8.1.4-alpine")
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("start valkey: %v", err)
	}
	endpoint, err := c.Endpoint(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}

	l := Connect(endpoint, 50*time.Millisecond, discard)
	defer l.Close()

	deadline := time.Now().Add(5 * time.Second)
	for l.Check(t.Context()) != nil {
		if time.Now().After(deadline) {
			t.Fatalf("not connected after 5 s: %v", l.Check(t.Context()))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
