package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	opsinfra "github.com/xrodriguezb/fulcrum/internal/ops/infra"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
)

// readinessBudget bounds how long the readiness probe waits for its
// dependencies. A probe that hangs is worse than a probe that fails, because an
// orchestrator reads no answer as no problem for as long as it waits.
const readinessBudget = 2 * time.Second

// routerDeps is the wiring of the HTTP surface.
type routerDeps struct {
	orders   *orderinfra.Handlers
	ops      *opsinfra.Handlers
	checks   []httpx.Check
	registry *prometheus.Registry
	logger   *slog.Logger
	chain    httpx.Middleware
}

// newRouter mounts every endpoint.
//
// Routing uses the standard library's method and wildcard patterns. A router
// dependency would add a third party to the one part of the system that the
// standard library has covered since 1.22.
func newRouter(deps routerDeps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/orders", deps.orders.Create)
	mux.HandleFunc("GET /api/v1/orders", deps.orders.List)
	mux.HandleFunc("GET /api/v1/orders/{id}", deps.orders.Get)
	mux.HandleFunc("GET /api/v1/inventory", deps.ops.Inventory)
	mux.HandleFunc("GET /api/v1/ops/outbox", deps.ops.Outbox)
	mux.HandleFunc("GET /api/v1/ops/dead-letters", deps.ops.DeadLetters)
	mux.HandleFunc("GET /api/v1/ops/stream", deps.ops.Stream)

	// The probes and the metrics endpoint are outside the versioned API: they
	// are an operational contract with the platform, not with a client.
	mux.HandleFunc("GET /healthz", httpx.Liveness(deps.logger))
	mux.HandleFunc("GET /readyz", httpx.Readiness(deps.checks, readinessBudget, deps.logger))
	mux.Handle("GET /metrics", promhttp.HandlerFor(deps.registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))

	return deps.chain(mux)
}
