package events

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files with the current output")

// The golden files are the published shape of each event. A failure here
// means a consumer would see something else: either the change is a mistake,
// or it is compatible and the file is updated on purpose with -update.
func TestEventsMatchTheirGoldenFiles(t *testing.T) {
	at := time.Date(2026, 10, 28, 19, 2, 11, 314_000_000, time.UTC)
	tests := map[string]PaymentEvent{
		"payment_approved.json": {
			ID: "0199b5a2-7c1e-7d3a-9f00-0000000000e2", Type: PaymentApproved,
			PaymentID: "0199b5a2-7c1e-7d3a-9f00-0000000000a1", MerchantID: "00000000-0000-4000-8000-000000000001",
			OccurredAt: at.Add(300 * time.Millisecond),
			Data: Payment{
				ID: "0199b5a2-7c1e-7d3a-9f00-0000000000a1", OrderID: "1001", Status: "APPROVED",
				Amount: 15000, Currency: "CLP", Card: &Card{Brand: "visa", Last4: "1111"},
				AuthorizationCode: "7T4N4V", CreatedAt: at, UpdatedAt: at.Add(300 * time.Millisecond),
			},
		},
		"payment_failed.json": {
			ID: "0199b5a2-7c1e-7d3a-9f00-0000000000e3", Type: PaymentFailed,
			PaymentID: "0199b5a2-7c1e-7d3a-9f00-0000000000a2", MerchantID: "00000000-0000-4000-8000-000000000001",
			OccurredAt: at.Add(5 * time.Second),
			Data: Payment{
				ID: "0199b5a2-7c1e-7d3a-9f00-0000000000a2", OrderID: "1002", Status: "FAILED",
				Amount: 15000, Currency: "CLP", Reason: "acquirer_unavailable",
				CreatedAt: at, UpdatedAt: at.Add(5 * time.Second),
			},
		},
	}
	for name, event := range tests {
		got, err := json.MarshalIndent(event, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		path := filepath.Join("testdata", name)
		if *update {
			if err := os.WriteFile(path, got, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s changed:\n%s\nwant:\n%s", name, got, want)
		}
	}
}
