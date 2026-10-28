// Command healthcheck asks a service's readiness endpoint and exits 0 when it
// answers 200. The container images are distroless, without curl, so the
// Compose health checks run this instead:
//
//	healthcheck http://localhost:8080/readyz
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	target := "http://localhost:8080/readyz"
	if len(os.Args) > 1 {
		target = os.Args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := check(ctx, target); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// check only probes the container it runs in, so it refuses any URL that is
// not plain HTTP on localhost.
func check(ctx context.Context, target string) error {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		return fmt.Errorf("%q must be an http URL on localhost", target)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil) //nolint:gosec // G704: restricted to localhost above
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: restricted to localhost above
	if err != nil {
		return fmt.Errorf("ask %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", target, resp.StatusCode)
	}
	return nil
}
