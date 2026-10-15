package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
	}{
		{"database reachable", nil, http.StatusOK},
		{"database down", errors.New("connection refused"), http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter(NewHandler(fakePinger{err: tt.pingErr}))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	router := NewRouter(NewHandler(fakePinger{}))

	t.Run("generates one when missing", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		if rec.Header().Get(requestIDHeader) == "" {
			t.Error("expected a generated request id")
		}
	})

	t.Run("reuses the caller's id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set(requestIDHeader, "merchant-abc-123")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if got := rec.Header().Get(requestIDHeader); got != "merchant-abc-123" {
			t.Errorf("request id = %q, want %q", got, "merchant-abc-123")
		}
	})
}

func TestRecoverPanic(t *testing.T) {
	h := recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
