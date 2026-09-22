// Package api assembles the HTTP surface: it mounts every handler on one router
// and wraps it in the middleware chain.
//
// Assembly lives here rather than in cmd/api so that an integration test can
// exercise the real routing table, the real middleware order and the real
// probes, instead of a second router built to look like it.
package api

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

// Deps is the wiring of the HTTP surface.
type Deps struct {
	Orders   *orderinfra.Handlers
	Ops      *opsinfra.Handlers
	Checks   []httpx.Check
	Registry *prometheus.Registry
	Logger   *slog.Logger
	Chain    httpx.Middleware
}

// New mounts every endpoint.
//
// Routing uses the standard library's method and wildcard patterns. A router
// dependency would add a third party to the one part of the system that the
// standard library has covered since 1.22.
func New(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/orders", deps.Orders.Create)
	mux.HandleFunc("GET /api/v1/orders", deps.Orders.List)
	mux.HandleFunc("GET /api/v1/orders/{id}", deps.Orders.Get)
	mux.HandleFunc("GET /api/v1/inventory", deps.Ops.Inventory)
	mux.HandleFunc("GET /api/v1/ops/outbox", deps.Ops.Outbox)
	mux.HandleFunc("GET /api/v1/ops/dead-letters", deps.Ops.DeadLetters)
	mux.HandleFunc("GET /api/v1/ops/stream", deps.Ops.Stream)

	// The probes and the metrics endpoint are outside the versioned API: they
	// are an operational contract with the platform, not with a client.
	mux.HandleFunc("GET /healthz", httpx.Liveness(deps.Logger))
	mux.HandleFunc("GET /readyz", httpx.Readiness(deps.Checks, readinessBudget, deps.Logger))
	mux.Handle("GET /metrics", promhttp.HandlerFor(deps.Registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))

	// The routing wrapper is innermost, so it sees the statuses the mux itself
	// produces and nothing that a handler wrote.
	return deps.Chain(httpx.RoutingProblems(deps.Logger)(mux))
}
