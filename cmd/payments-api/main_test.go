package main

import (
	"strings"
	"testing"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
)

func TestATimeoutTheLeaseCannotCoverIsRefused(t *testing.T) {
	env := config.New(func(key string) (string, bool) {
		v, ok := map[string]string{
			"DATABASE_URL":        "postgres://localhost/payments",
			"ACQUIRER_URL":        "http://localhost:8090",
			"ACQUIRER_SECRET_KEY": "sk_sim_test",
			"ACQUIRER_TIMEOUT":    "15s",
		}[key]
		return v, ok
	})
	if _, err := loadSettings(env); err == nil || !strings.Contains(err.Error(), "ACQUIRER_TIMEOUT must be at most 10s") {
		t.Errorf("got %v, want a 15 s timeout refused: the call would outlive the payment's lease", err)
	}
}
