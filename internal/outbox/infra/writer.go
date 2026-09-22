// Package infra implements the outbox ports against PostgreSQL.
package infra

import (
	"context"

	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// Writer appends envelopes to outbox_events.
type Writer struct {
	tx *postgres.TxManager
}

// NewWriter builds a writer over the transaction manager.
func NewWriter(tx *postgres.TxManager) *Writer {
	return &Writer{tx: tx}
}

// Append inserts every envelope. It runs on the caller's executor, so when the
// caller is inside a transaction the events commit with the caller's work and
// disappear with its rollback.
func (w *Writer) Append(ctx context.Context, envelopes ...domain.Envelope) error {
	const statement = `
INSERT INTO outbox_events
  (id, aggregate_id, aggregate_type, event_type, event_version, payload,
   correlation_id, trace_id, occurred_at, next_attempt_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())`

	executor := w.tx.Executor(ctx)
	for _, envelope := range envelopes {
		if err := envelope.Validate(); err != nil {
			return postgres.Fault("append outbox event", err)
		}
		_, err := executor.Exec(ctx, statement,
			envelope.ID, envelope.AggregateID, envelope.AggregateType, envelope.EventType,
			envelope.EventVersion, []byte(envelope.Payload), envelope.CorrelationID,
			nullableText(envelope.TraceID), envelope.OccurredAt)
		if err != nil {
			return postgres.Fault("append outbox event", err)
		}
	}
	return nil
}

// nullableText keeps an empty optional string out of the column as NULL, so that
// "absent" and "empty" are not the same value in the database.
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
