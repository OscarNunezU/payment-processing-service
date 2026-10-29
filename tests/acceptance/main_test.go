package acceptance

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/telemetry"
)

// TestMain installs tracing as the services do, without exporting, so the
// scenarios can follow a payment's trace.
func TestMain(m *testing.M) {
	shutdown, err := telemetry.Setup(context.Background(), "scenarios", "test", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = shutdown(context.Background())
	os.Exit(code)
}
