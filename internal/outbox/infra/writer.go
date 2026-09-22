// Package infra implements the outbox ports against PostgreSQL.
package infra

import (
	"context"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/app"
	"github.com/xrodriguezb/fulcrum/internal/outbox/domain"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
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
			return errs.Internal("append outbox event", err)
		}
		_, err := executor.Exec(ctx, statement,
			envelope.ID, envelope.AggregateID, envelope.AggregateType, envelope.EventType,
			envelope.EventVersion, []byte(envelope.Payload), envelope.CorrelationID,
			nullableText(envelope.TraceID), envelope.OccurredAt)
		if err != nil {
			return errs.Internal("append outbox event", err)
		}
	}
	return nil
}

// Stats reports outbox health for the operations console and the metrics
// endpoint.
func (w *Writer) Stats(ctx context.Context) (app.Stats, error) {
	const query = `
SELECT
  count(*) FILTER (WHERE published_at IS NULL)                        AS pending,
  count(*) FILTER (WHERE published_at IS NULL AND attempts > 0)       AS failing,
  count(*) FILTER (WHERE published_at IS NOT NULL)                    AS published,
  coalesce(extract(epoch FROM now() - min(occurred_at)
    FILTER (WHERE published_at IS NULL)), 0)                          AS oldest_age_seconds,
  coalesce((
    SELECT id::text FROM outbox_events
    WHERE published_at IS NULL
    ORDER BY occurred_at
    LIMIT 1), '')                                                     AS oldest_id
FROM outbox_events`

	var (
		stats      app.Stats
		ageSeconds float64
	)
	err := w.tx.Executor(ctx).QueryRow(ctx, query).Scan(
		&stats.Pending, &stats.Failing, &stats.Published, &ageSeconds, &stats.OldestUnpublishedEvent)
	if err != nil {
		return app.Stats{}, errs.Internal("read outbox stats", err)
	}
	stats.OldestUnpublishedAge = time.Duration(ageSeconds * float64(time.Second))
	return stats, nil
}

// nullableText keeps an empty optional string out of the column as NULL, so that
// "absent" and "empty" are not the same value in the database.
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
