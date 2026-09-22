// Command worker publishes outbox events and consumes them back.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	consumerapp "github.com/xrodriguezb/fulcrum/internal/consumer/app"
	consumerinfra "github.com/xrodriguezb/fulcrum/internal/consumer/infra"
	idempotencyinfra "github.com/xrodriguezb/fulcrum/internal/idempotency/infra"
	orderapp "github.com/xrodriguezb/fulcrum/internal/order/app"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
	outboxapp "github.com/xrodriguezb/fulcrum/internal/outbox/app"
	outboxinfra "github.com/xrodriguezb/fulcrum/internal/outbox/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
	"github.com/xrodriguezb/fulcrum/internal/platform/idgen"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
	"github.com/xrodriguezb/fulcrum/internal/platform/messaging"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
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

	// The worker never migrates. Two services racing to change the schema is a
	// failure mode with no upside, so the API owns it.
	pool, err := postgres.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()

	broker, err := messaging.Connect(ctx, cfg.NATS)
	if err != nil {
		return fmt.Errorf("connect to the broker: %w", err)
	}
	defer broker.Close()

	publisherTarget, err := outboxinfra.EnsureStream(ctx, broker.Conn(), cfg.NATS)
	if err != nil {
		return fmt.Errorf("ensure the event stream: %w", err)
	}

	txManager := postgres.NewTxManager(pool)
	claimer := outboxinfra.NewClaimer(txManager, cfg.Outbox.ClaimLease)

	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metrics := outboxinfra.NewMetrics(registry)
	outboxinfra.NewDepthCollector(registry, outboxinfra.NewWriter(txManager), 2*time.Second)

	publisher, err := outboxapp.NewPublisher(claimer, publisherTarget, outboxapp.PublisherConfig{
		Workers:        cfg.Outbox.Workers,
		BatchSize:      cfg.Outbox.BatchSize,
		PollInterval:   cfg.Outbox.PollInterval,
		PublishTimeout: cfg.NATS.PublishTimeout,
		DrainTimeout:   cfg.Outbox.DrainTimeout,
		MaxAttempts:    cfg.Outbox.MaxAttempts,
		Backoff:        outboxapp.BackoffPolicy{Base: cfg.Outbox.BackoffBase, Cap: cfg.Outbox.BackoffCap},
	}, logger, metrics)
	if err != nil {
		return fmt.Errorf("wire the publisher: %w", err)
	}

	consumer, err := buildConsumer(ctx, cfg, txManager, broker, logger, registry)
	if err != nil {
		return err
	}

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		logger.InfoContext(groupCtx, "outbox publisher started",
			slog.Int("workers", cfg.Outbox.Workers),
			slog.Int("batch_size", cfg.Outbox.BatchSize))
		return publisher.Run(groupCtx)
	})

	group.Go(func() error {
		logger.InfoContext(groupCtx, "event consumer started", slog.String("consumer", cfg.Consumer.Name))
		return consumer.Run(groupCtx)
	})

	group.Go(func() error {
		return sweepIdempotencyKeys(groupCtx, idempotencyinfra.NewStore(txManager), cfg, logger)
	})

	group.Go(func() error {
		return serveOperationalEndpoints(groupCtx, cfg, registry, pool, broker, logger)
	})

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("worker component failed: %w", err)
	}
	logger.InfoContext(context.WithoutCancel(ctx), "worker stopped")
	return nil
}

// buildConsumer wires the event consumer and the side effect it performs.
func buildConsumer(
	ctx context.Context,
	cfg config.Config,
	txManager *postgres.TxManager,
	broker *messaging.Client,
	logger *slog.Logger,
	registry *prometheus.Registry,
) (*consumerapp.Consumer, error) {
	source, err := consumerinfra.NewSource(ctx, broker.Conn(), cfg.NATS, cfg.Consumer)
	if err != nil {
		return nil, fmt.Errorf("create the durable consumer: %w", err)
	}

	handler, err := orderapp.NewConfirmOrderHandler(orderinfra.NewRepository(txManager), time.Now)
	if err != nil {
		return nil, fmt.Errorf("wire the confirmation handler: %w", err)
	}

	consumer, err := consumerapp.New(consumerapp.Deps{
		Source:      source,
		Handler:     handler,
		Dedup:       consumerinfra.NewDeduplicator(txManager),
		DeadLetters: consumerinfra.NewDeadLetterStore(txManager),
		Tx:          txManager,
		IDs:         idgen.UUID{},
		Clock:       time.Now,
		Logger:      logger,
		Metrics:     consumerinfra.NewMetrics(registry),
	}, consumerapp.Config{
		Name:         cfg.Consumer.Name,
		MaxAttempts:  cfg.Consumer.MaxAttempts,
		FetchBatch:   cfg.Consumer.FetchBatch,
		Backoff:      outboxapp.BackoffPolicy{Base: cfg.Consumer.RetryBase, Cap: cfg.Consumer.RetryCap},
		DrainTimeout: cfg.Consumer.DrainTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("wire the consumer: %w", err)
	}
	return consumer, nil
}

// sweeper is the part of the idempotency store the worker needs.
type sweeper interface {
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// sweepIdempotencyKeys removes expired keys on a slow interval. It lives in the
// worker because the API should not spend request capacity on housekeeping.
func sweepIdempotencyKeys(ctx context.Context, store sweeper, cfg config.Config, logger *slog.Logger) error {
	ticker := time.NewTicker(cfg.Idempotency.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			removed, err := store.DeleteExpired(ctx, time.Now())
			if err != nil {
				// Housekeeping that fails is not a reason to stop the worker:
				// the keys expire logically whether or not the row is gone.
				logger.WarnContext(ctx, "idempotency sweep failed", slog.String("error", err.Error()))
				continue
			}
			if removed > 0 {
				logger.InfoContext(ctx, "expired idempotency keys removed", slog.Int64("count", removed))
			}
		}
	}
}

// serveOperationalEndpoints exposes the worker's own metrics and probes.
//
// The worker owns the outbox and consumer numbers, so it has to publish them
// itself. Routing them through the API would mean the API reporting on work it
// does not do.
func serveOperationalEndpoints(
	ctx context.Context,
	cfg config.Config,
	registry *prometheus.Registry,
	pool interface {
		Ping(ctx context.Context) error
	},
	broker *messaging.Client,
	logger *slog.Logger,
) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	mux.HandleFunc("GET /healthz", httpx.Liveness(logger))
	mux.HandleFunc("GET /readyz", httpx.Readiness([]httpx.Check{
		{Name: "postgres", Probe: pool.Ping},
		{Name: "nats", Probe: broker.Health},
	}, 2*time.Second, logger))

	return httpx.Serve(ctx, httpx.ServerConfig{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Worker.MetricsPort)),
		Handler:           mux,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ShutdownTimeout:   cfg.Worker.ShutdownTimeout,
		Logger:            logger,
	})
}
