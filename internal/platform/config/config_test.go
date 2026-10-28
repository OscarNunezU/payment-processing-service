package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func from(m map[string]string) *Env {
	return New(func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	})
}

func TestUnsetAndEmptyValuesUseTheDefault(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"unset": nil,
		"empty": {"ADDR": "", "TIMEOUT": "", "WORKERS": "", "LOG_LEVEL": ""},
	} {
		t.Run(name, func(t *testing.T) {
			env := from(m)
			if got := env.String("ADDR", ":8080"); got != ":8080" {
				t.Errorf("String = %q, want :8080", got)
			}
			if got := env.Duration("TIMEOUT", 5*time.Second); got != 5*time.Second {
				t.Errorf("Duration = %s, want 5s", got)
			}
			if got := env.Int("WORKERS", 4, 100); got != 4 {
				t.Errorf("Int = %d, want 4", got)
			}
			if got := env.LogLevel("LOG_LEVEL", slog.LevelInfo); got != slog.LevelInfo {
				t.Errorf("LogLevel = %s, want INFO", got)
			}
			if err := env.Err(); err != nil {
				t.Errorf("Err = %v, want nil", err)
			}
		})
	}
}

func TestValidValuesAreParsed(t *testing.T) {
	env := from(map[string]string{
		"ADDR": ":9090", "TIMEOUT": "250ms", "WORKERS": "12", "LOG_LEVEL": "debug", "DATABASE_URL": "postgres://x",
	})
	if got := env.String("ADDR", ":8080"); got != ":9090" {
		t.Errorf("String = %q, want :9090", got)
	}
	if got := env.Duration("TIMEOUT", time.Second); got != 250*time.Millisecond {
		t.Errorf("Duration = %s, want 250ms", got)
	}
	if got := env.Int("WORKERS", 4, 100); got != 12 {
		t.Errorf("Int = %d, want 12", got)
	}
	if got := env.LogLevel("LOG_LEVEL", slog.LevelInfo); got != slog.LevelDebug {
		t.Errorf("LogLevel = %s, want DEBUG", got)
	}
	if got := env.Required("DATABASE_URL"); got != "postgres://x" {
		t.Errorf("Required = %q, want postgres://x", got)
	}
	if err := env.Err(); err != nil {
		t.Errorf("Err = %v, want nil", err)
	}
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	env := from(map[string]string{"TIMEOUT": "soon", "WORKERS": "1000", "LOG_LEVEL": "loud"})
	env.Required("DATABASE_URL")
	if got := env.Duration("TIMEOUT", time.Second); got != time.Second {
		t.Errorf("an invalid duration returned %s, want the default", got)
	}
	if got := env.Int("WORKERS", 4, 100); got != 4 {
		t.Errorf("an integer over the limit returned %d, want the default", got)
	}
	env.LogLevel("LOG_LEVEL", slog.LevelInfo)

	err := env.Err()
	if err == nil {
		t.Fatal("Err = nil, want every problem")
	}
	for _, key := range []string{"DATABASE_URL", "TIMEOUT", "WORKERS", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Err does not mention %s: %v", key, err)
		}
	}
}

func TestDurationAndIntRejectZeroAndNegatives(t *testing.T) {
	env := from(map[string]string{"TIMEOUT": "0s", "WORKERS": "-1"})
	env.Duration("TIMEOUT", time.Second)
	env.Int("WORKERS", 4, 100)
	if err := env.Err(); err == nil || !strings.Contains(err.Error(), "TIMEOUT") || !strings.Contains(err.Error(), "WORKERS") {
		t.Errorf("Err = %v, want both TIMEOUT and WORKERS rejected", err)
	}
}
