package infra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/app"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// claimStatement takes a batch of due events and marks them claimed.
//
// FOR UPDATE SKIP LOCKED is what makes several publisher instances safe against
// one database: a row another transaction is already holding is skipped rather
// than waited on, so a second instance adds throughput instead of latency.
//
// The claim also increments attempts. A crash after the claim therefore leaves
// the attempt counted, which is the conservative direction: an event is retried
// slightly later rather than forever.
const claimStatement = `
WITH claimed AS (
  SELECT id
  FROM   outbox_events
  WHERE  published_at IS NULL
    AND  next_attempt_at <= now()
  ORDER  BY occurred_at
  FOR UPDATE SKIP LOCKED
  LIMIT  $1
)
UPDATE outbox_events o
SET    claimed_at = now(),
       attempts   = o.attempts + 1
FROM   claimed
WHERE  o.id = claimed.id
RETURNING o.id, o.aggregate_id, o.aggregate_type, o.event_type, o.event_version,
          o.payload, o.correlation_id, coalesce(o.trace_id, ''), o.occurred_at, o.attempts`

// Claimer implements the publisher's view of the outbox.
type Claimer struct {
	tx *postgres.TxManager
}

// NewClaimer builds a claimer over the transaction manager.
func NewClaimer(tx *postgres.TxManager) *Claimer {
	return &Claimer{tx: tx}
}

// Claim takes up to batchSize due events.
func (c *Claimer) Claim(ctx context.Context, batchSize int) ([]app.Claimed, error) {
	rows, err := c.tx.Executor(ctx).Query(ctx, claimStatement, batchSize)
	if err != nil {
		return nil, errs.Unavailable("cannot claim outbox events", err)
	}
	defer rows.Close()

	claimed := make([]app.Claimed, 0, batchSize)
	for rows.Next() {
		var (
			entry   app.Claimed
			payload []byte
		)
		if scanErr := rows.Scan(
			&entry.Envelope.ID, &entry.Envelope.AggregateID, &entry.Envelope.AggregateType,
			&entry.Envelope.EventType, &entry.Envelope.EventVersion, &payload,
			&entry.Envelope.CorrelationID, &entry.Envelope.TraceID, &entry.Envelope.OccurredAt,
			&entry.Attempts,
		); scanErr != nil {
			return nil, errs.Internal("scan claimed outbox event", scanErr)
		}
		entry.Envelope.Payload = json.RawMessage(payload)
		claimed = append(claimed, entry)
	}
	if rows.Err() != nil {
		return nil, errs.Unavailable("cannot read claimed outbox events", rows.Err())
	}
	return claimed, nil
}

// MarkPublished records a successful publication.
//
// The guard on published_at makes the update idempotent: two publishers that
// somehow both handled the same row cannot produce two different published
// timestamps, and a retry of the mark itself is harmless.
func (c *Claimer) MarkPublished(ctx context.Context, id string) error {
	const statement = `
UPDATE outbox_events
SET    published_at = now(),
       last_error   = NULL
WHERE  id = $1
  AND  published_at IS NULL`

	if _, err := c.tx.Executor(ctx).Exec(ctx, statement, id); err != nil {
		return errs.Unavailable("cannot mark an outbox event published", err)
	}
	return nil
}

// MarkFailed records a failure and schedules the next attempt. The claim is
// released so the row is visible to whichever instance claims it next.
func (c *Claimer) MarkFailed(ctx context.Context, id, reason string, nextAttemptAt time.Time) error {
	const statement = `
UPDATE outbox_events
SET    next_attempt_at = $2,
       last_error      = $3,
       claimed_at      = NULL
WHERE  id = $1
  AND  published_at IS NULL`

	if _, err := c.tx.Executor(ctx).Exec(ctx, statement, id, nextAttemptAt, reason); err != nil {
		return errs.Unavailable("cannot record an outbox publication failure", err)
	}
	return nil
}

// compile time check that the claimer is what the publisher expects.
var _ app.Store = (*Claimer)(nil)
