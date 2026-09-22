package infra

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	opsapp "github.com/xrodriguezb/fulcrum/internal/ops/app"
)

// Metrics records publisher activity and exposes outbox depth.
type Metrics struct {
	published *prometheus.CounterVec
	failures  *prometheus.CounterVec
	batchSize prometheus.Histogram
}

// NewMetrics registers the publisher metrics.
func NewMetrics(registry prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_published_total",
			Help: "Events successfully published and marked.",
		}, []string{"result"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_publish_failures_total",
			Help: "Publication failures, split by whether the attempt budget is exhausted.",
		}, []string{"exhausted"}),
		batchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "outbox_claim_batch_size",
			Help:    "Rows returned by one claim. A batch that is always full means the publisher is behind.",
			Buckets: []float64{0, 1, 5, 10, 25, 50, 100, 250},
		}),
	}
	registry.MustRegister(metrics.published, metrics.failures, metrics.batchSize)
	return metrics
}

// PublishSucceeded records a published event.
func (m *Metrics) PublishSucceeded() { m.published.WithLabelValues("ok").Inc() }

// PublishFailed records a failure. The label is a bounded two value set, never
// the error text, which would be unbounded.
func (m *Metrics) PublishFailed(exhausted bool) {
	label := "false"
	if exhausted {
		label = "true"
	}
	m.failures.WithLabelValues(label).Inc()
}

// ObserveBatch records how many rows a claim returned.
func (m *Metrics) ObserveBatch(size int) { m.batchSize.Observe(float64(size)) }

// SnapshotReader is the operational read model, as this collector needs it. The
// operations console reads the same snapshot, so there is one query behind both
// rather than two that can disagree.
type SnapshotReader interface {
	Snapshot(ctx context.Context) (opsapp.Snapshot, error)
}

// depthCollector reports outbox depth at scrape time rather than on a ticker.
//
// A ticker would keep querying a database nobody is asking about, and it would
// report a number that is up to one interval stale. Collecting on scrape costs
// one query per scrape and is always current.
type depthCollector struct {
	snapshots SnapshotReader
	timeout   time.Duration

	pending   *prometheus.Desc
	failing   *prometheus.Desc
	oldestAge *prometheus.Desc
}

// NewDepthCollector registers a collector that reports outbox depth on scrape.
func NewDepthCollector(registry prometheus.Registerer, snapshots SnapshotReader, timeout time.Duration) {
	collector := &depthCollector{
		snapshots: snapshots,
		timeout:   timeout,
		pending: prometheus.NewDesc("outbox_pending_total",
			"Events written and not yet published.", nil, nil),
		failing: prometheus.NewDesc("outbox_failing_total",
			"Unpublished events that have already failed at least once.", nil, nil),
		oldestAge: prometheus.NewDesc("outbox_oldest_unpublished_seconds",
			"Age of the oldest unpublished event.", nil, nil),
	}
	registry.MustRegister(collector)
}

// Describe sends the descriptors of the collected metrics.
func (c *depthCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pending
	ch <- c.failing
	ch <- c.oldestAge
}

// Collect queries the outbox once per scrape.
func (c *depthCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	snapshot, err := c.snapshots.Snapshot(ctx)
	if err != nil {
		// A scrape that cannot read the database reports nothing rather than
		// zero: a zero here would look like an empty outbox, which is the
		// opposite of what a failing database means.
		return
	}

	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(snapshot.Outbox.Pending))
	ch <- prometheus.MustNewConstMetric(c.failing, prometheus.GaugeValue, float64(snapshot.Outbox.Failing))
	ch <- prometheus.MustNewConstMetric(c.oldestAge, prometheus.GaugeValue,
		snapshot.Outbox.OldestUnpublishedAge.Seconds())
}
