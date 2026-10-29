package acquirersim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
)

// Switch serves a handler on an address that can be turned off and on again.
// While it is off nothing listens, so a client gets "connection refused", as
// it would from an acquirer whose service is down: the one failure where the
// client knows for sure that its request never arrived.
type Switch struct {
	mu   sync.Mutex
	addr string
	h    http.Handler
	log  *slog.Logger
	srv  *http.Server
}

// NewSwitch returns a switch for h on addr, turned off.
func NewSwitch(addr string, h http.Handler, log *slog.Logger) *Switch {
	return &Switch{addr: addr, h: h, log: log}
}

// Up starts listening, unless it already is. The first time, a port 0 in the
// address picks a free port; every later Up reuses that port.
func (s *Switch) Up(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return nil
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.addr, err)
	}
	s.addr = ln.Addr().String()
	srv := httpx.NewServer(s.addr, s.h)
	s.srv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("the public port stopped serving", "addr", s.addr, "error", err)
		}
	}()
	return nil
}

// Down stops listening and cuts every open connection, requests in flight
// included, as when the acquirer's machine dies. It does nothing when the
// switch is already off.
func (s *Switch) Down() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Close()
	s.srv = nil
	if err != nil {
		return fmt.Errorf("close %s: %w", s.addr, err)
	}
	return nil
}

// Shutdown stops listening and lets the requests in flight finish until ctx
// ends; then it cuts the ones left, such as calls that never answer. It is for
// the end of the process.
func (s *Switch) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Shutdown(ctx)
	if err != nil {
		_ = s.srv.Close()
	}
	s.srv = nil
	if err != nil {
		return fmt.Errorf("shut down %s: %w", s.addr, err)
	}
	return nil
}

// Addr returns the address the switch listens on, with the port it picked.
func (s *Switch) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}
