// Package logging carries a request-scoped logger through the context,
// so every log line of a request shares the same ids.
package logging

import (
	"context"
	"log/slog"
	"os"
)

type ctxKey struct{}

func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext falls back to slog.Default, so it never returns nil.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
