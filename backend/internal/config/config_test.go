package config

import (
	"testing"
	"time"
)

// validKey is "dev-only-key-not-for-production!" (32 bytes) in base64.
const validKey = "ZGV2LW9ubHkta2V5LW5vdC1mb3ItcHJvZHVjdGlvbiE="

// setRequired sets the required variables and clears the optional ones, so a
// value exported in the developer's shell cannot change the result.
func setRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/payments")
	t.Setenv("CARD_ENCRYPTION_KEY", validKey)
	for _, key := range []string{
		"HTTP_ADDR", "MAX_AMOUNT_CLP", "MAX_AMOUNT_USD",
		"ACQUIRER_URL", "ACQUIRER_TIMEOUT", "WORKER_CONCURRENCY",
	} {
		t.Setenv(key, "")
	}
}

func TestLoadUsesTheDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.HTTPAddr != ":8080" || len(cfg.CardEncryptionKey) != 32 ||
		cfg.MaxAmounts["CLP"] != 5_000_000 || cfg.MaxAmounts["USD"] != 500_000 ||
		cfg.AcquirerURL != "http://localhost:8081" || cfg.AcquirerTimeout != 5*time.Second ||
		cfg.WorkerConcurrency != 4 {
		t.Fatalf("Load() = %+v, want the defaults", cfg)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"missing database url", "DATABASE_URL", ""},
		{"missing key", "CARD_ENCRYPTION_KEY", ""},
		{"key not in base64", "CARD_ENCRYPTION_KEY", "not base64!"},
		{"key of 16 bytes", "CARD_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZg=="},
		{"limit that is not a number", "MAX_AMOUNT_CLP", "mucho"},
		{"limit that is not positive", "MAX_AMOUNT_USD", "0"},
		{"timeout without a unit", "ACQUIRER_TIMEOUT", "5"},
		{"negative timeout", "ACQUIRER_TIMEOUT", "-1s"},
		{"no workers", "WORKER_CONCURRENCY", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tt.key, tt.value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %s=%q", tt.key, tt.value)
			}
		})
	}
}
