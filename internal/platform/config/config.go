// Package config reads a service's settings from environment variables. It
// collects every problem instead of stopping at the first one, so a
// misconfigured deployment reports all of them at once.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Env reads settings and remembers which ones were missing or invalid.
type Env struct {
	lookup func(string) (string, bool)
	errs   []error
}

// FromOS reads from the process environment.
func FromOS() *Env { return New(os.LookupEnv) }

// New reads from lookup, which lets tests pass a map.
func New(lookup func(string) (string, bool)) *Env { return &Env{lookup: lookup} }

// get treats an empty value as unset.
func (e *Env) get(key string) (string, bool) {
	v, ok := e.lookup(key)
	return v, ok && v != ""
}

// String returns the value of key, or def when it is unset.
func (e *Env) String(key, def string) string {
	if v, ok := e.get(key); ok {
		return v
	}
	return def
}

// Required returns the value of key, and records an error when it is unset.
func (e *Env) Required(key string) string {
	v, ok := e.get(key)
	if !ok {
		e.errs = append(e.errs, fmt.Errorf("%s is required", key))
	}
	return v
}

// Duration returns a positive duration such as "5s", or def when key is unset.
func (e *Env) Duration(key string, def time.Duration) time.Duration {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.errs = append(e.errs, fmt.Errorf("%s must be a positive duration such as 5s, got %q", key, v))
		return def
	}
	return d
}

// Int returns an integer between 1 and limit, or def when key is unset.
func (e *Env) Int(key string, def, limit int) int {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > limit {
		e.errs = append(e.errs, fmt.Errorf("%s must be an integer between 1 and %d, got %q", key, limit, v))
		return def
	}
	return n
}

// LogLevel returns a level such as "debug" or "warn", or def when key is unset.
func (e *Env) LogLevel(key string, def slog.Level) slog.Level {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s must be debug, info, warn or error, got %q", key, v))
		return def
	}
	return l
}

// Err reports every problem found so far, or nil when there was none.
func (e *Env) Err() error { return errors.Join(e.errs...) }
