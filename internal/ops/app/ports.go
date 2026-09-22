// Package app holds the read model behind the operations console.
//
// It is a context of its own because it answers a question none of the other
// contexts own: what is the state of this system right now. Putting these
// queries inside the order or outbox context would make those contexts serve two
// masters, the business flow and the console.
package app

import (
	"context"
	"time"
)

// OutboxHealth is the state of the publication pipeline.
type OutboxHealth struct {
	Pending              int           `json:"pending"`
	Failing              int           `json:"failing"`
	Published            int           `json:"published"`
	OldestUnpublishedAge time.Duration `json:"-"`
	OldestUnpublishedSec float64       `json:"oldest_unpublished_seconds"`
}

// DeadLetter is one event that will not be retried again.
type DeadLetter struct {
	ID            string    `json:"id"`
	EventID       string    `json:"event_id"`
	ConsumerName  string    `json:"consumer_name"`
	EventType     string    `json:"event_type"`
	Attempts      int       `json:"attempts"`
	FirstFailedAt time.Time `json:"first_failed_at"`
	LastFailedAt  time.Time `json:"last_failed_at"`
	FailureReason string    `json:"failure_reason"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	TraceID       string    `json:"trace_id,omitempty"`
}

// Snapshot is everything the console shows at a glance.
type Snapshot struct {
	Outbox OutboxHealth `json:"outbox"`
	// DeadLetters is a count, not a list: the list has its own endpoint because
	// it is paginated and the snapshot is polled.
	DeadLetters int `json:"dead_letters"`
	// StuckIdempotencyKeys are claims that never completed, which is the visible
	// trace of a crash between the claim and the business transaction.
	StuckIdempotencyKeys int       `json:"stuck_idempotency_keys"`
	ObservedAt           time.Time `json:"observed_at"`
}

// Reader answers operational questions.
type Reader interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	DeadLetters(ctx context.Context, limit, offset int) ([]DeadLetter, int, error)
}
