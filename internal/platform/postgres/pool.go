// Package postgres owns the connection pool, the transaction manager and the
// migration runner. Nothing above this package is allowed to know which driver
// is in use.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
)

// NewPool builds a connection pool and proves it works before returning. A
// service that starts with an unusable database is a service that reports
// healthy and then fails every request, which is worse than not starting.
func NewPool(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// A lifetime jitter keeps replicas from recycling their whole pool in the
	// same second after a synchronized start.
	poolCfg.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 10

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if pingErr := pool.Ping(pingCtx); pingErr != nil {
		pool.Close()
		return nil, fmt.Errorf("database is not reachable: %w", pingErr)
	}
	return pool, nil
}

// MigrateWithDSN applies the migration set on a connection of its own, opened
// with the given connection string.
//
// Migrations run as a role that may change the schema, while the request path
// runs as a role that may not. Sharing the application pool would mean granting
// those rights to every request.
func MigrateWithDSN(ctx context.Context, cfg config.PostgresConfig, dsn string) error {
	migrationCfg := cfg
	migrationCfg.URL = dsn
	// One connection is enough: the runner holds an advisory lock for the whole
	// run, so a pool would only add idle connections.
	migrationCfg.MaxConns = 2
	migrationCfg.MinConns = 1

	pool, err := NewPool(ctx, migrationCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	return Migrate(ctx, pool)
}

// Migrate applies the embedded migration set using a dedicated connection from
// the pool, so the advisory lock is held by one session for the whole run.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migrations: %w", err)
	}
	defer conn.Release()

	migrator, err := NewMigrator(conn)
	if err != nil {
		return err
	}
	return migrator.Up(ctx)
}

// HealthCheck reports whether the database answers within the given budget. It
// is the readiness probe's database dependency.
func HealthCheck(ctx context.Context, pool *pgxpool.Pool, budget time.Duration) error {
	checkCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := pool.Ping(checkCtx); err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}
	return nil
}
