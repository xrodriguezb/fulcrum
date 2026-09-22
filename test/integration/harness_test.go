//go:build integration

// Package integration exercises the system against a real PostgreSQL instance.
// Nothing here uses a fake: the claims this repository makes are claims about
// what the database does under concurrency, and a fake cannot falsify them.
package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// defaultPostgresImage is overridden in CI so the same suite runs against every
// supported major version. The reservation guarantee depends on database
// semantics, so proving it on one version only would be proving less than it
// looks like.
const defaultPostgresImage = "postgres:16-alpine"

var containerDSN string

// TestMain owns the container for the whole package.
//
// An earlier version started it lazily and registered cleanup against whichever
// test happened to be first. That test's cleanup then terminated the container
// while later tests were still using the connection string, which surfaced as
// "connection refused" in an unrelated test. Ownership belongs to the package,
// so it lives here.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	image := os.Getenv("FULCRUM_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = defaultPostgresImage
	}

	container, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("fulcrum"),
		tcpostgres.WithUsername("fulcrum"),
		tcpostgres.WithPassword("fulcrum"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: cannot start postgres: %v\n", err)
		os.Exit(1)
	}

	containerDSN, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: cannot resolve connection string: %v\n", err)
		_ = testcontainers.TerminateContainer(container)
		os.Exit(1)
	}

	code := m.Run()

	if termErr := testcontainers.TerminateContainer(container); termErr != nil {
		fmt.Fprintf(os.Stderr, "integration: cannot terminate postgres: %v\n", termErr)
	}
	os.Exit(code)
}

// newPool returns a pool against the shared container, with the schema applied.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx := t.Context()

	pool, err := postgres.NewPool(ctx, config.PostgresConfig{
		URL:             containerDSN,
		MaxConns:        30,
		MinConns:        2,
		MaxConnLifetime: 10 * time.Minute,
		MaxConnIdleTime: time.Minute,
		ConnectTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	truncateAll(t, pool)
	return pool
}

// truncateAll resets the data between tests while keeping the schema, which is
// much faster than migrating down and up again.
func truncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	const truncate = `
TRUNCATE TABLE outbox_events, idempotency_keys, order_lines, orders, inventory_items RESTART IDENTITY CASCADE`
	if _, err := pool.Exec(t.Context(), truncate); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// seedInventory inserts one stock position and returns nothing: the tests read
// it back through the repository under test rather than trusting the fixture.
func seedInventory(t *testing.T, pool *pgxpool.Pool, sku string, available int, unitPriceCents int64) {
	t.Helper()

	const insert = `
INSERT INTO inventory_items (sku, available, reserved, unit_price_cents, currency, version)
VALUES ($1, $2, 0, $3, 'EUR', 1)
ON CONFLICT (sku) DO UPDATE SET available = EXCLUDED.available, reserved = 0, version = 1`
	if _, err := pool.Exec(t.Context(), insert, sku, available, unitPriceCents); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
}
