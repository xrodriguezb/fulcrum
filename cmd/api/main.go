// Command api serves the Fulcrum HTTP API.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

func main() {
	if err := run(); err != nil {
		// Startup failures go to stderr in plain text: a service that cannot
		// build its logger still has to say why it refused to start.
		fmt.Fprintf(os.Stderr, "api: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := logging.New(cfg)

	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	if migrateErr := postgres.Migrate(ctx, pool); migrateErr != nil {
		return fmt.Errorf("apply migrations: %w", migrateErr)
	}
	logger.InfoContext(ctx, "schema is up to date")
	logger.InfoContext(ctx, "api started", "port", cfg.HTTP.Port)

	<-ctx.Done()
	logger.InfoContext(context.WithoutCancel(ctx), "api stopped")
	return nil
}
