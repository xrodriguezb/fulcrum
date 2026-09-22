package app

// Metrics records what the business did, as opposed to what the transport did.
//
// The distinction matters: http_requests_total counts a 409 the same way whether
// the stock ran out or the client sent nonsense, and an operator watching for
// contention needs the first number without the second.
type Metrics interface {
	// OrderCreated records an order that was persisted and reserved.
	OrderCreated()
	// InventoryConflict records a reservation refused because the stock was gone.
	InventoryConflict()
	// IdempotencyOutcome records how a request with a key was answered. The
	// outcome is a closed set: claimed, replayed, in_progress, reused.
	IdempotencyOutcome(outcome string)
}

// Idempotency outcomes. They are constants because they are metric label values,
// and a label value built from data is a metric that grows with traffic.
const (
	// OutcomeClaimed means this request owned the work.
	OutcomeClaimed = "claimed"
	// OutcomeReplayed means a stored response was returned.
	OutcomeReplayed = "replayed"
	// OutcomeInProgress means another request with the same key was still running.
	OutcomeInProgress = "in_progress"
	// OutcomeReused means the key arrived with a different request body.
	OutcomeReused = "reused"
)

// NoopMetrics satisfies Metrics without recording anything, so a caller that
// does not care about metrics does not have to build one.
type NoopMetrics struct{}

// OrderCreated does nothing.
func (NoopMetrics) OrderCreated() {}

// InventoryConflict does nothing.
func (NoopMetrics) InventoryConflict() {}

// IdempotencyOutcome does nothing.
func (NoopMetrics) IdempotencyOutcome(string) {}
