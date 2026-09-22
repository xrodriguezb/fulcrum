// Package infra implements the consumer ports against PostgreSQL and NATS.
package infra

import (
	"context"

	"github.com/xrodriguezb/fulcrum/internal/consumer/app"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// Deduplicator records which events a consumer has already processed.
type Deduplicator struct {
	tx *postgres.TxManager
}

// NewDeduplicator builds the deduplicator over the transaction manager.
func NewDeduplicator(tx *postgres.TxManager) *Deduplicator {
	return &Deduplicator{tx: tx}
}

// Claim inserts the deduplication row and reports whether this delivery is the
// first one.
//
// It runs on the caller's executor, so the row commits with the side effect. A
// rollback takes the row with it, which is what lets a failed attempt be retried
// rather than mistaken for a duplicate.
func (d *Deduplicator) Claim(ctx context.Context, consumerName, eventID string) (bool, error) {
	const statement = `
INSERT INTO processed_events (consumer_name, event_id)
VALUES ($1, $2)
ON CONFLICT (consumer_name, event_id) DO NOTHING`

	tag, err := d.tx.Executor(ctx).Exec(ctx, statement, consumerName, eventID)
	if err != nil {
		return false, errs.Unavailable("cannot claim an event for processing", err)
	}
	return tag.RowsAffected() == 1, nil
}

// DeadLetterStore persists events that will not be retried again.
type DeadLetterStore struct {
	tx *postgres.TxManager
}

// NewDeadLetterStore builds the store over the transaction manager.
func NewDeadLetterStore(tx *postgres.TxManager) *DeadLetterStore {
	return &DeadLetterStore{tx: tx}
}

// Record writes one dead letter entry.
func (s *DeadLetterStore) Record(ctx context.Context, entry app.DeadLetterEntry) error {
	const statement = `
INSERT INTO dead_letter_events
  (id, event_id, consumer_name, event_type, payload, attempts,
   first_failed_at, last_failed_at, failure_reason, correlation_id, trace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (id) DO NOTHING`

	payload := entry.Payload
	if len(payload) == 0 || !isJSON(payload) {
		// The column is jsonb and the payload may be anything, including the
		// bytes that could not be decoded in the first place. Wrapping keeps the
		// evidence without letting a malformed body reject the insert.
		payload = wrapAsJSON(entry.Payload)
	}

	_, err := s.tx.Executor(ctx).Exec(ctx, statement,
		entry.ID, entry.EventID, entry.ConsumerName, entry.EventType, payload, entry.Attempts,
		entry.FirstFailedAt, entry.LastFailedAt, entry.FailureReason,
		nullable(entry.CorrelationID), nullable(entry.TraceID))
	if err != nil {
		return errs.Unavailable("cannot record a dead letter", err)
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
