// Package health serves the liveness and readiness endpoints that
// orchestrators probe.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/httpx"
)

// Check is a dependency the readiness endpoint reports on. When a critical
// check fails, the instance is unready and stops receiving traffic. A
// non-critical check is only reported: it is for dependencies the service can
// work without, such as Valkey in degraded mode (RFC 0001, section 10).
type Check struct {
	Name     string
	Critical bool
	Run      func(context.Context) error
}

// Report is the body of the readiness endpoint.
type Report struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Live answers 200 while the process can serve HTTP at all.
func Live(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

// Ready runs every check at once, each bounded by timeout. It answers 200 when
// every critical check passes and 503 otherwise, and logs each failure. The
// body only says which checks fail; the reasons stay in the logs.
func Ready(log *slog.Logger, timeout time.Duration, checks ...Check) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errs := make([]error, len(checks))
		var wg sync.WaitGroup
		for i, c := range checks {
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(r.Context(), timeout)
				defer cancel()
				errs[i] = c.Run(ctx)
			})
		}
		wg.Wait()

		report := Report{Status: "ready", Checks: make(map[string]string, len(checks))}
		status := http.StatusOK
		for i, c := range checks {
			if errs[i] == nil {
				report.Checks[c.Name] = "ok"
				continue
			}
			report.Checks[c.Name] = "failing"
			log.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "critical", c.Critical, "error", errs[i])
			if c.Critical {
				report.Status = "unready"
				status = http.StatusServiceUnavailable
			}
		}
		httpx.WriteJSON(w, status, report)
	})
}
