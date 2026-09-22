package infra

import "github.com/prometheus/client_golang/prometheus"

// Metrics records consumer outcomes. Labels are event type and a bounded reason
// class, never the error text, which would be unbounded.
type Metrics struct {
	processed    *prometheus.CounterVec
	failed       *prometheus.CounterVec
	deadLettered *prometheus.CounterVec
}

// NewMetrics registers the consumer metrics.
func NewMetrics(registry prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "consumer_processed_total",
			Help: "Events processed successfully, including duplicates that were skipped.",
		}, []string{"event_type"}),
		failed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "consumer_failed_total",
			Help: "Processing failures by event type and error class.",
		}, []string{"event_type", "reason_class"}),
		deadLettered: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dead_letter_total",
			Help: "Events routed to the dead letter queue.",
		}, []string{"event_type"}),
	}
	registry.MustRegister(metrics.processed, metrics.failed, metrics.deadLettered)
	return metrics
}

// Processed records a successful delivery.
func (m *Metrics) Processed(eventType string) { m.processed.WithLabelValues(eventType).Inc() }

// Failed records a failed delivery.
func (m *Metrics) Failed(eventType, reasonClass string) {
	m.failed.WithLabelValues(eventType, reasonClass).Inc()
}

// DeadLettered records an event that will not be retried.
func (m *Metrics) DeadLettered(eventType string) { m.deadLettered.WithLabelValues(eventType).Inc() }
