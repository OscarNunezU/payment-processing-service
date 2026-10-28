package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		ok     bool
	}{
		"ready":   {status: http.StatusOK, ok: true},
		"unready": {status: http.StatusServiceUnavailable, ok: false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			if err := check(t.Context(), srv.URL); (err == nil) != tc.ok {
				t.Errorf("check = %v, want ok = %v", err, tc.ok)
			}
		})
	}
}

func TestCheckRefusesOtherHosts(t *testing.T) {
	for _, target := range []string{"http://example.com/readyz", "https://localhost/readyz", "file:///etc/passwd", "::"} {
		if err := check(t.Context(), target); err == nil {
			t.Errorf("check(%q) = nil, want it refused", target)
		}
	}
}

func TestCheckFailsWhenNothingAnswers(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if err := check(t.Context(), url); err == nil {
		t.Error("check succeeded against a closed server")
	}
}
