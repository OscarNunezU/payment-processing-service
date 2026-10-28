// Package pgtest starts a throwaway Postgres for integration tests.
package pgtest

import (
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Image is the Postgres the tests run against: the same major version as the
// local environment.
const Image = "postgres:18-alpine"

// Start runs Postgres in a container for the duration of the test and returns
// its connection string. In -short mode it skips the test, which is how
// `make test` runs without Docker.
func Start(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: it needs Docker")
	}
	ctx := t.Context()
	c, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	return url
}
