package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var (
	pass = func(context.Context) error { return nil }
	fail = func(context.Context) error { return errors.New("down") }
	hang = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
)

func ready(t *testing.T, checks ...Check) (int, Report) {
	t.Helper()
	rec := httptest.NewRecorder()
	Ready(slog.New(slog.NewTextHandler(io.Discard, nil)), 50*time.Millisecond, checks...).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
	var report Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("the body is not a report: %v\n%s", err, rec.Body)
	}
	return rec.Code, report
}

func TestReadyWhenEveryCheckPasses(t *testing.T) {
	code, report := ready(t, Check{Name: "postgres", Critical: true, Run: pass})
	if code != http.StatusOK || report.Status != "ready" || report.Checks["postgres"] != "ok" {
		t.Errorf("got %d %+v, want 200 ready", code, report)
	}
}

func TestAFailingCriticalCheckMakesTheInstanceUnready(t *testing.T) {
	code, report := ready(t,
		Check{Name: "postgres", Critical: true, Run: fail},
		Check{Name: "valkey", Run: pass},
	)
	if code != http.StatusServiceUnavailable || report.Status != "unready" || report.Checks["postgres"] != "failing" {
		t.Errorf("got %d %+v, want 503 unready", code, report)
	}
}

func TestAFailingNonCriticalCheckIsOnlyReported(t *testing.T) {
	code, report := ready(t,
		Check{Name: "postgres", Critical: true, Run: pass},
		Check{Name: "valkey", Run: fail},
	)
	if code != http.StatusOK || report.Status != "ready" || report.Checks["valkey"] != "failing" {
		t.Errorf("got %d %+v, want 200 ready with valkey failing", code, report)
	}
}

func TestAHangingCheckTimesOut(t *testing.T) {
	start := time.Now()
	code, report := ready(t, Check{Name: "postgres", Critical: true, Run: hang})
	if code != http.StatusServiceUnavailable || report.Checks["postgres"] != "failing" {
		t.Errorf("got %d %+v, want the hanging check to fail", code, report)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("readiness took %s, want it bounded by the check timeout", elapsed)
	}
}

func TestLive(t *testing.T) {
	rec := httptest.NewRecorder()
	Live(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
