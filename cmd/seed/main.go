// Command seed loads the merchants of the local environment into the payments
// database, with API keys that are published in the README. It is for local
// development only: a real merchant is onboarded, never seeded. Running it
// again changes nothing.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/config"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/logging"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
)

// localMerchant is a merchant of the local environment and its API key.
type localMerchant struct {
	merchant domain.Merchant
	key      string
}

// limits are the PRD's example limits: 5,000,000 CLP and 5,000 USD, in cents.
var limits = map[domain.Currency]int64{domain.CLP: 5_000_000, domain.USD: 500_000}

var localMerchants = []localMerchant{
	{
		merchant: domain.Merchant{ID: "00000000-0000-4000-8000-000000000001", LegalName: "Tienda Uno SpA",
			RUT: "76543210-3", Status: domain.MerchantActive, MaxAmount: limits},
		key: "sk_test_local_tienda_uno",
	},
	{
		merchant: domain.Merchant{ID: "00000000-0000-4000-8000-000000000002", LegalName: "Tienda Dos Limitada",
			RUT: "77123456-9", Status: domain.MerchantActive, MaxAmount: limits},
		key: "sk_test_local_tienda_dos",
	},
	{
		merchant: domain.Merchant{ID: "00000000-0000-4000-8000-000000000003", LegalName: "Comercio Suspendido SpA",
			RUT: "76987654-5", Status: domain.MerchantSuspended, MaxAmount: limits},
		key: "sk_test_local_suspendida",
	},
}

func main() {
	log := logging.New(os.Stdout, "seed", slog.LevelInfo)
	if err := run(context.Background(), config.FromOS(), log); err != nil {
		log.Error("seeding failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, env *config.Env, log *slog.Logger) error {
	url := env.Required("DATABASE_URL")
	if err := env.Err(); err != nil {
		return err
	}
	db, err := platformpg.Open(ctx, url, "seed", 2)
	if err != nil {
		return err
	}
	defer db.Close()

	store := postgres.New(db)
	for _, lm := range localMerchants {
		if err := store.CreateMerchant(ctx, lm.merchant, lm.key); err != nil {
			return fmt.Errorf("seed %s: %w", lm.merchant.LegalName, err)
		}
		log.Info("merchant ready", "id", lm.merchant.ID, "legal_name", lm.merchant.LegalName, "status", lm.merchant.Status)
	}
	return nil
}
