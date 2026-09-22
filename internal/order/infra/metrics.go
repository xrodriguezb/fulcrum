package infra

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/xrodriguezb/fulcrum/internal/order/app"
)

// Metrics records the business outcomes of order creation.
type Metrics struct {
	created   prometheus.Counter
	conflicts prometheus.Counter
	idemHits  *prometheus.CounterVec
}

// NewMetrics registers the order metrics.
func NewMetrics(registry prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		created: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "orders_created_total",
			Help: "Orders that were persisted with their inventory reserved.",
		}),
		conflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "inventory_conflicts_total",
			Help: "Reservations refused because the requested quantity was no longer available.",
		}),
		idemHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "idempotency_hits_total",
			Help: "Requests carrying an idempotency key, by how they were answered.",
		}, []string{"outcome"}),
	}
	registry.MustRegister(metrics.created, metrics.conflicts, metrics.idemHits)
	return metrics
}

// OrderCreated records a created order.
func (m *Metrics) OrderCreated() { m.created.Inc() }

// InventoryConflict records a refusal caused by stock.
func (m *Metrics) InventoryConflict() { m.conflicts.Inc() }

// IdempotencyOutcome records how a keyed request was answered. The outcome comes
// from the closed set in the application layer, never from data.
func (m *Metrics) IdempotencyOutcome(outcome string) { m.idemHits.WithLabelValues(outcome).Inc() }

// compile time proof that this satisfies the port the use case depends on.
var _ app.Metrics = (*Metrics)(nil)
