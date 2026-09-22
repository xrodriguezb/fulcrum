package httpx

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the HTTP instrumentation.
//
// Every label is a closed set: method, route pattern and status. None of them
// carries an order id, a sku or an idempotency key, because an unbounded label
// turns a metric into a memory leak that only shows up under real traffic.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

// NewMetrics registers the HTTP metrics on the given registry.
func NewMetrics(registry prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests by route, method and status.",
		}, []string{"route", "method", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Request duration by route and method.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"route", "method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of requests currently being served.",
		}),
	}

	registry.MustRegister(metrics.requests, metrics.duration, metrics.inFlight)
	return metrics
}

func (m *Metrics) observe(route, method string, status int, seconds float64) {
	m.requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(route, method).Observe(seconds)
}
