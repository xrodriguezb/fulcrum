// Package infra implements the operations read model against PostgreSQL.
package infra

import (
	"context"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/ops/app"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// stuckKeyAge is how long a claim may stay in progress before an operator should
// be told about it. A business transaction takes milliseconds, so a claim older
// than a minute is evidence of a crash rather than of a slow request.
const stuckKeyAge = time.Minute

// Reader queries the operational state.
type Reader struct {
	tx *postgres.TxManager
}

// NewReader builds the reader over the transaction manager.
func NewReader(tx *postgres.TxManager) *Reader {
	return &Reader{tx: tx}
}

// Snapshot returns the state of the pipeline in one round trip. It is one query
// because the console polls it and three round trips per poll would be three
// times the load for the same answer.
func (r *Reader) Snapshot(ctx context.Context) (app.Snapshot, error) {
	const query = `
SELECT
  (SELECT count(*) FROM outbox_events WHERE published_at IS NULL)                     AS pending,
  (SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND attempts > 0)    AS failing,
  (SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL)                 AS published,
  (SELECT coalesce(extract(epoch FROM now() - min(occurred_at)), 0)
     FROM outbox_events WHERE published_at IS NULL)                                   AS oldest_seconds,
  (SELECT count(*) FROM dead_letter_events)                                           AS dead_letters,
  (SELECT count(*) FROM idempotency_keys
     WHERE status = 'in_progress' AND created_at < now() - $1::interval)              AS stuck_keys`

	var (
		snapshot   app.Snapshot
		oldestSecs float64
	)
	err := r.tx.Executor(ctx).QueryRow(ctx, query, stuckKeyAge.String()).Scan(
		&snapshot.Outbox.Pending,
		&snapshot.Outbox.Failing,
		&snapshot.Outbox.Published,
		&oldestSecs,
		&snapshot.DeadLetters,
		&snapshot.StuckIdempotencyKeys,
	)
	if err != nil {
		return app.Snapshot{}, postgres.Fault("read operational snapshot", err)
	}

	snapshot.Outbox.OldestUnpublishedSec = oldestSecs
	snapshot.Outbox.OldestUnpublishedAge = time.Duration(oldestSecs * float64(time.Second))
	snapshot.ObservedAt = time.Now().UTC()
	return snapshot, nil
}

// DeadLetters lists failures newest first, with everything an operator needs to
// act: what failed, when, how many attempts, and the identifiers that join it to
// the logs.
func (r *Reader) DeadLetters(ctx context.Context, limit, offset int) ([]app.DeadLetter, int, error) {
	const countQuery = `SELECT count(*) FROM dead_letter_events`
	const query = `
SELECT id::text, event_id::text, consumer_name, event_type, attempts,
       first_failed_at, last_failed_at, failure_reason,
       coalesce(correlation_id, ''), coalesce(trace_id, '')
FROM   dead_letter_events
ORDER  BY last_failed_at DESC
LIMIT  $1 OFFSET $2`

	executor := r.tx.Executor(ctx)

	// Counted separately for the same reason the order listing is: a page past
	// the last one carries no rows, and a total travelling on the rows is
	// therefore reported as zero.
	var total int
	if err := executor.QueryRow(ctx, countQuery).Scan(&total); err != nil {
		return nil, 0, postgres.Fault("count dead letters", err)
	}

	rows, err := executor.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, postgres.Fault("list dead letters", err)
	}
	defer rows.Close()

	entries := make([]app.DeadLetter, 0, limit)
	for rows.Next() {
		var entry app.DeadLetter
		if scanErr := rows.Scan(&entry.ID, &entry.EventID, &entry.ConsumerName, &entry.EventType,
			&entry.Attempts, &entry.FirstFailedAt, &entry.LastFailedAt, &entry.FailureReason,
			&entry.CorrelationID, &entry.TraceID); scanErr != nil {
			return nil, 0, postgres.Fault("scan dead letter", scanErr)
		}
		entries = append(entries, entry)
	}
	if rows.Err() != nil {
		return nil, 0, postgres.Fault("iterate dead letters", rows.Err())
	}
	return entries, total, nil
}
