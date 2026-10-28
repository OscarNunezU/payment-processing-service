// Package valkey connects to Valkey without making it a startup dependency.
package valkey

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// ErrNotConnected means the first connection has not succeeded yet.
var ErrNotConnected = errors.New("not connected to valkey")

// Lazy connects to Valkey in the background and keeps trying until it
// succeeds, so a service can start and keep serving while Valkey is down.
// Callers must handle the not-connected case: it is the degraded mode of
// RFC 0001, section 10. Once connected, the client reconnects on its own.
type Lazy struct {
	mu     sync.RWMutex
	client valkey.Client
	stop   context.CancelFunc
	done   chan struct{}
}

// Connect starts connecting to addr ("host:port"), retrying every retry.
func Connect(addr string, retry time.Duration, log *slog.Logger) *Lazy {
	ctx, stop := context.WithCancel(context.Background())
	l := &Lazy{stop: stop, done: make(chan struct{})}
	go l.connect(ctx, addr, retry, log)
	return l
}

func (l *Lazy) connect(ctx context.Context, addr string, retry time.Duration, log *slog.Logger) {
	defer close(l.done)
	for {
		c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
		if err == nil {
			l.mu.Lock()
			l.client = c
			l.mu.Unlock()
			log.Info("connected to valkey", "addr", addr)
			return
		}
		log.Warn("valkey is unavailable, retrying", "addr", addr, "error", err, "retry_in", retry.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

// Client returns the client once the first connection succeeded.
func (l *Lazy) Client() (valkey.Client, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.client, l.client != nil
}

// Check pings Valkey. It fails with ErrNotConnected before the first
// connection.
func (l *Lazy) Check(ctx context.Context) error {
	c, ok := l.Client()
	if !ok {
		return ErrNotConnected
	}
	return c.Do(ctx, c.B().Ping().Build()).Error()
}

// Close stops connecting and closes the client.
func (l *Lazy) Close() {
	l.stop()
	<-l.done
	if c, ok := l.Client(); ok {
		c.Close()
	}
}
