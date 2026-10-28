package httpx

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// RequestIDHeader carries the id that correlates a request across services and
// log lines.
const RequestIDHeader = "X-Request-ID"

type ctxKey int

const (
	requestIDKey ctxKey = iota
	routeKey
)

// Trace starts a span for every request, continuing the caller's trace when it
// sends one. The span is named after the route once Handle routes the request.
// Probes are not traced: orchestrators call them every few seconds.
func Trace(service string, next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, service,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r) }),
	)
}

// RequestID takes the request id from X-Request-ID, or generates one when it is
// missing or unsafe to log, and puts it in the context and in the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = rand.Text()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// RequestIDFrom returns the request id in ctx, or "" when there is none.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// validRequestID accepts up to 128 visible ASCII characters, so a caller cannot
// inject line breaks or control characters into the logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}

// AccessLog logs one line per request, with the route that served it, the
// status and the duration. Probes are not logged.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r) {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			served := &route{}
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), routeKey, served)))
			log.InfoContext(r.Context(), "request",
				"method", r.Method,
				"route", served.pattern,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()),
			)
		})
	}
}

// Recover answers 500 with the standard error body when a handler panics, and
// logs the panic with its stack.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler { //nolint:errorlint // the sentinel is compared as net/http itself does
					panic(p)
				}
				log.ErrorContext(r.Context(), "handler panicked",
					"panic", p,
					"stack", string(debug.Stack()),
					"request_id", RequestIDFrom(r.Context()),
				)
				WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong on our side.")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Handle registers h for pattern. Once the request is routed, it names the span
// after the route and tells the access log which route served it, so traces
// and logs group requests by route instead of by raw path.
func Handle(mux *http.ServeMux, pattern string, h http.Handler) {
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.Pattern
		if !strings.Contains(name, " ") {
			name = r.Method + " " + name
		}
		span := trace.SpanFromContext(r.Context())
		span.SetName(name)
		span.SetAttributes(attribute.String("http.route", name[strings.Index(name, " ")+1:]))
		if served, ok := r.Context().Value(routeKey).(*route); ok {
			served.pattern = name
		}
		h.ServeHTTP(w, r)
	}))
}

func isProbe(r *http.Request) bool {
	return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
}

// route is filled in by Handle and read by AccessLog after the request.
type route struct{ pattern string }

// statusRecorder remembers the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = status, true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer, for
// flushing server-sent events among others.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
