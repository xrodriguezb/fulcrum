package infra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/outbox/app"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// claimStatement takes a batch of due events and leases them.
//
// FOR UPDATE SKIP LOCKED is what makes several publisher instances safe against
// one database: a row another transaction is already holding is skipped rather
// than waited on, so a second instance adds throughput instead of latency.
//
// The row lock only lasts for the claim transaction, so it cannot be what keeps
// a second instance away while the first one publishes. The claim therefore also
// pushes next_attempt_at forward by a lease: until the lease expires the row is
// not due, and no other instance considers it. A publisher that dies mid-publish
// loses the lease and the event is picked up again when it expires, which is the
// behaviour at-least-once delivery is built on.
//
// The claim also increments attempts, so a crash after the claim counts the
// attempt. That is the conservative direction: the event is retried a little
// later rather than immediately and forever.
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
SET    claimed_at      = now(),
       attempts        = o.attempts + 1,
       next_attempt_at = now() + $2::interval
FROM   claimed
WHERE  o.id = claimed.id
RETURNING o.id, o.aggregate_id, o.aggregate_type, o.event_type, o.event_version,
          o.payload, o.correlation_id, coalesce(o.trace_id, ''), o.occurred_at, o.attempts`

// Claimer implements the publisher's view of the outbox.
type Claimer struct {
	tx    *postgres.TxManager
	lease time.Duration
}

// defaultLease is used when a caller does not choose one. It has to be longer
// than a publish can reasonably take and short enough that a crashed publisher
// does not strand an event for long.
const defaultLease = 30 * time.Second

// NewClaimer builds a claimer over the transaction manager.
func NewClaimer(tx *postgres.TxManager, lease time.Duration) *Claimer {
	if lease <= 0 {
		lease = defaultLease
	}
	return &Claimer{tx: tx, lease: lease}
}

// Claim takes up to batchSize due events.
func (c *Claimer) Claim(ctx context.Context, batchSize int) ([]app.Claimed, error) {
	rows, err := c.tx.Executor(ctx).Query(ctx, claimStatement, batchSize, c.lease.String())
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
