// Command api serves the Fulcrum HTTP API.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/xrodriguezb/fulcrum/internal/api"
	idempotencyinfra "github.com/xrodriguezb/fulcrum/internal/idempotency/infra"
	inventoryinfra "github.com/xrodriguezb/fulcrum/internal/inventory/infra"
	opsinfra "github.com/xrodriguezb/fulcrum/internal/ops/infra"
	orderapp "github.com/xrodriguezb/fulcrum/internal/order/app"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
	outboxinfra "github.com/xrodriguezb/fulcrum/internal/outbox/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
	"github.com/xrodriguezb/fulcrum/internal/platform/idgen"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
	"github.com/xrodriguezb/fulcrum/internal/platform/messaging"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
	"github.com/xrodriguezb/fulcrum/internal/platform/telemetry"
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

	shutdownTracing, err := telemetry.Setup(ctx, cfg)
	if err != nil {
		return fmt.Errorf("set up tracing: %w", err)
	}
	defer func() {
		// Flushing runs on a context cancellation cannot reach: the spans
		// describing the shutdown are the ones worth keeping.
		if flushErr := shutdownTracing(context.WithoutCancel(ctx)); flushErr != nil {
			logger.WarnContext(context.WithoutCancel(ctx), "cannot flush traces", "error", flushErr)
		}
	}()

	pool, poolErr := postgres.NewPool(ctx, cfg.Postgres)
	if poolErr != nil {
		return fmt.Errorf("connect to postgres: %w", poolErr)
	}
	defer pool.Close()

	if migrateErr := postgres.MigrateWithDSN(ctx, cfg.Postgres, cfg.MigrationDSN()); migrateErr != nil {
		return fmt.Errorf("apply migrations: %w", migrateErr)
	}
	logger.InfoContext(ctx, "schema is up to date")

	broker, err := messaging.Connect(ctx, cfg.NATS)
	if err != nil {
		return fmt.Errorf("connect to the broker: %w", err)
	}
	defer broker.Close()

	txManager := postgres.NewTxManager(pool)
	reserver := inventoryinfra.NewReserver(txManager)
	orders := orderinfra.NewRepository(txManager)
	outbox := outboxinfra.NewWriter(txManager)

	creator, err := orderapp.NewCreateOrderHandler(orderapp.CreateOrderDeps{
		Tx:           txManager,
		Orders:       orders,
		Inventory:    reserver,
		Outbox:       outbox,
		Idempotency:  idempotencyinfra.NewStore(txManager),
		Clock:        time.Now,
		IDs:          idgen.UUID{},
		KeyTTL:       cfg.Idempotency.TTL,
		MaxKeyLength: cfg.Idempotency.MaxKeyLength,
	})
	if err != nil {
		return fmt.Errorf("wire the create order use case: %w", err)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	chain, err := httpx.Chain(httpx.MiddlewareConfig{
		Logger:         logger,
		Metrics:        httpx.NewMetrics(registry),
		MaxBodyBytes:   cfg.HTTP.MaxBodyBytes,
		AllowedOrigins: cfg.HTTP.CORSAllowedOrigins,
	})
	if err != nil {
		return fmt.Errorf("build the middleware chain: %w", err)
	}

	router := api.New(api.Deps{
		Orders: orderinfra.NewHandlers(creator, orders, logger),
		Ops:    opsinfra.NewHandlers(opsinfra.NewReader(txManager), reserver, logger),
		Checks: []httpx.Check{
			{Name: "postgres", Probe: func(probeCtx context.Context) error {
				return postgres.HealthCheck(probeCtx, pool, time.Second)
			}},
			{Name: "nats", Probe: broker.Health},
		},
		Registry: registry,
		Logger:   logger,
		Chain:    chain,
	})

	return httpx.Serve(ctx, httpx.ServerConfig{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.HTTP.Port)),
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ShutdownTimeout:   cfg.HTTP.ShutdownTimeout,
		Logger:            logger,
	})
}
