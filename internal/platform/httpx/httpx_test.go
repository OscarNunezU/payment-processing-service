package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestIDKeepsAValidIDAndReplacesTheRest(t *testing.T) {
	for name, tc := range map[string]struct {
		header string
		keep   bool
	}{
		"valid":          {header: "req-123", keep: true},
		"missing":        {header: "", keep: false},
		"with a newline": {header: "a\nb", keep: false},
		"with a space":   {header: "a b", keep: false},
		"too long":       {header: strings.Repeat("a", 129), keep: false},
	} {
		t.Run(name, func(t *testing.T) {
			var seen string
			h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.Header.Set(RequestIDHeader, tc.header)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if seen == "" || rec.Header().Get(RequestIDHeader) != seen {
				t.Fatalf("context id %q and response id %q must be set and equal", seen, rec.Header().Get(RequestIDHeader))
			}
			if tc.keep != (seen == tc.header) {
				t.Errorf("id = %q for header %q, keep = %v", seen, tc.header, tc.keep)
			}
		})
	}
}

func TestRecoverAnswers500WithTheErrorBody(t *testing.T) {
	var logs bytes.Buffer
	h := Recover(slog.New(slog.NewJSONHandler(&logs, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "internal_error" {
		t.Errorf("body = %s, want the internal_error body", rec.Body)
	}
	if !strings.Contains(logs.String(), "boom") {
		t.Errorf("the panic was not logged: %s", logs.String())
	}
}

func TestAccessLogRecordsTheRouteAndStatus(t *testing.T) {
	var logs bytes.Buffer
	mux := http.NewServeMux()
	Handle(mux, "GET /v1/payments/{id}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	h := AccessLog(slog.New(slog.NewJSONHandler(&logs, nil)))(mux)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/payments/pay_1", nil))

	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("the access log is not JSON: %v\n%s", err, logs.String())
	}
	if line["route"] != "GET /v1/payments/{id}" {
		t.Errorf("route = %v, want the pattern, not the raw path", line["route"])
	}
	if line["status"] != float64(http.StatusNotFound) {
		t.Errorf("status = %v, want 404", line["status"])
	}
}

func TestProbesAreNotLogged(t *testing.T) {
	var logs bytes.Buffer
	h := AccessLog(slog.New(slog.NewJSONHandler(&logs, nil)))(http.NotFoundHandler())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
	if logs.Len() != 0 {
		t.Errorf("a probe was logged: %s", logs.String())
	}
}

func TestServeLetsRequestsInFlightFinish(t *testing.T) {
	started := make(chan struct{})
	srv := NewServer("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
	}))
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, srv, ln, 5*time.Second) }()

	got := make(chan string, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String(), nil)
		if err != nil {
			got <- err.Error()
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- string(b)
	}()

	<-started
	cancel()
	if body := <-got; body != "done" {
		t.Errorf("the request in flight got %q, want done", body)
	}
	if err := <-served; err != nil {
		t.Errorf("serve returned %v, want nil after a clean shutdown", err)
	}
}
