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
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// defaultPostgresImage is overridden in CI so the same suite runs against every
// supported major version. The reservation guarantee depends on database
// semantics, so proving it on one version only would be proving less than it
// looks like.
const (
	defaultPostgresImage = "postgres:16-alpine"
	defaultNATSImage     = "nats:2.10-alpine"
)

var (
	containerDSN string
	natsURL      string
)

// TestMain owns the container for the whole package.
//
// An earlier version started it lazily and registered cleanup against whichever
// test happened to be first. That test's cleanup then terminated the container
// while later tests were still using the connection string, which surfaced as
// "connection refused" in an unrelated test. Ownership belongs to the package,
// so it lives here.
func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

// runSuite exists so that every deferred cleanup runs before os.Exit, which
// would otherwise skip them.
func runSuite(m *testing.M) int {
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
		return 1
	}
	defer func() {
		if termErr := testcontainers.TerminateContainer(container); termErr != nil {
			fmt.Fprintf(os.Stderr, "integration: cannot terminate postgres: %v\n", termErr)
		}
	}()

	containerDSN, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: cannot resolve connection string: %v\n", err)
		return 1
	}

	// JetStream is enabled explicitly: without it the broker accepts publishes
	// and stores nothing, which is exactly the failure this project argues
	// against in ADR 0008.
	broker, err := tcnats.Run(ctx, natsImage(), testcontainers.WithCmdArgs("--jetstream"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: cannot start nats: %v\n", err)
		return 1
	}
	defer func() {
		if termErr := testcontainers.TerminateContainer(broker); termErr != nil {
			fmt.Fprintf(os.Stderr, "integration: cannot terminate nats: %v\n", termErr)
		}
	}()

	natsURL, err = broker.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: cannot resolve the broker url: %v\n", err)
		return 1
	}

	return m.Run()
}

// natsImage allows CI to pin the broker version the same way it pins postgres.
func natsImage() string {
	if image := os.Getenv("FULCRUM_TEST_NATS_IMAGE"); image != "" {
		return image
	}
	return defaultNATSImage
}

// natsConfig points a client at the container started for this package.
func natsConfig(t *testing.T) config.NATSConfig {
	t.Helper()
	return config.NATSConfig{
		URL:            natsURL,
		StreamName:     "FULCRUM_TEST",
		SubjectPrefix:  "fulcrum.test.events",
		ConnectTimeout: 10 * time.Second,
		ReconnectWait:  200 * time.Millisecond,
		MaxReconnects:  -1,
		PublishTimeout: 5 * time.Second,
	}
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
TRUNCATE TABLE outbox_events, idempotency_keys, order_lines, orders, inventory_items,
               processed_events, dead_letter_events RESTART IDENTITY CASCADE`
	if _, err := pool.Exec(t.Context(), truncate); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
