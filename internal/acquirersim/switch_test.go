package acquirersim

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
)

// get asks addr for /v1/tokens and returns the status, or the connection
// error.
func get(t *testing.T, addr string) (int, error) {
	t.Helper()
	// A fresh client each time, so no kept-alive connection hides that the
	// port is closed.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/v1/tokens", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func TestTheDownFaultRefusesConnectionsUntilItIsLifted(t *testing.T) {
	sim := New(Config{SecretKey: secretKey, PublishableKey: publishableKey}, slog.New(slog.DiscardHandler))
	public := NewSwitch("127.0.0.1:0", sim.PublicHandler(), slog.New(slog.DiscardHandler))
	sim.Attach(public)
	if err := public.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = public.Down() })
	admin := httptest.NewServer(sim.AdminHandler())
	t.Cleanup(admin.Close)
	h := &harness{sim: sim, admin: admin}
	addr := public.Addr()

	if status, err := get(t, addr); err != nil || status != http.StatusUnauthorized {
		t.Fatalf("while up: got %d %v, want 401 from the simulator", status, err)
	}

	h.setFaults(t, Faults{Down: true})
	if _, err := get(t, addr); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("while down: got %v, want connection refused", err)
	}

	h.setFaults(t, Faults{})
	if status, err := get(t, addr); err != nil || status != http.StatusUnauthorized {
		t.Errorf("after coming back: got %d %v, want 401 on the same port", status, err)
	}
}

func TestUpAndDownAreIdempotent(t *testing.T) {
	s := NewSwitch("127.0.0.1:0", http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	for range 2 {
		if err := s.Up(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := s.Down(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Errorf("shutdown while down: %v", err)
	}
}
