package api

import (
	"crypto/rand"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/OscarNunezU/payment-processing-service/backend/internal/logging"
)

const requestIDHeader = "X-Request-ID"

// requestID reuses the caller's id when it sends one, so a merchant can
// correlate its own logs with ours.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" || len(id) > 64 {
			id = rand.Text()
		}
		w.Header().Set(requestIDHeader, id)

		logger := logging.FromContext(r.Context()).With("request_id", id)
		ctx := logging.WithLogger(r.Context(), logger)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		logging.FromContext(r.Context()).Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logging.FromContext(r.Context()).Error("panic recovered",
					"panic", v,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
