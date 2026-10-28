// Package httpx runs HTTP servers with the timeouts, middleware and error
// format every service shares.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// NewServer returns a server whose timeouts bound slow clients. The write
// timeout leaves room for the 8 s deadline of the payment path (RFC 0001,
// section 5.2).
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// Serve listens on srv.Addr and serves until ctx ends. Then it stops accepting
// connections and gives the requests in flight up to grace to finish.
func Serve(ctx context.Context, srv *http.Server, grace time.Duration) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}
	return serve(ctx, srv, ln, grace)
}

func serve(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve on %s: %w", ln.Addr(), err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down %s: %w", ln.Addr(), err)
	}
	return nil
}
