# 0004. Transactional outbox

Status: accepted
Date: 2026-09-21

## Context

When an order is created, something outside the request has to learn about it.
The order and the notification must agree: an order that exists with no event
published is a silent data loss, and an event published for an order that rolled
back is a lie that downstream systems will act on.

Two writes to two systems cannot be made atomic by ordering them carefully. Every
ordering has a crash window.

## Decision

The event is written to an `outbox_events` row inside the same transaction that
reserves inventory, persists the order and completes the idempotency key. A
separate worker claims unpublished rows and publishes them.

Claiming uses `FOR UPDATE SKIP LOCKED` so that several worker instances can run
against one database without coordinating:

```sql
WITH claimed AS (
  SELECT id FROM outbox_events
  WHERE published_at IS NULL AND next_attempt_at <= now()
  ORDER BY occurred_at
  FOR UPDATE SKIP LOCKED
  LIMIT $1
)
UPDATE outbox_events o
SET claimed_at = now(), attempts = attempts + 1
FROM claimed WHERE o.id = claimed.id
RETURNING o.*;
```

The partial index is on `(next_attempt_at, occurred_at) WHERE published_at IS
NULL`. The table grows monotonically while the claim only ever reads unpublished
rows, so indexing the whole table would cost space and write throughput for
nothing. The second column is in the index because the claim orders by
`occurred_at`; with `next_attempt_at` alone, every due row would be sorted on
every claim.

## Alternatives considered

**Publish directly from the request handler.** Rejected. It is the dual write
this pattern exists to remove. Publishing before the commit produces events for
orders that never existed; publishing after produces orders nobody hears about;
publishing inside the transaction blocks the commit on the broker being
available, which makes the broker a hard dependency of accepting an order. The
demo makes that last point visible by killing the broker and showing orders still
being accepted.

**Change data capture from the write-ahead log, with Debezium or similar.**
Genuinely good, and the right answer at a larger scale. Rejected here because it
publishes rows rather than events: the payload becomes the shape of a table, so a
column rename turns into a consumer outage, and the event carries no intent.
It also adds an operational component with its own failure modes to a system whose
whole point is to be readable in thirty minutes.

**A queue table polled without `SKIP LOCKED`.** Rejected. Without it, two workers
claiming at once serialize on the same rows: the second waits for the first
rather than moving on to different work, so adding a worker adds latency rather
than throughput.

**Listen/notify instead of polling.** Rejected as the primary mechanism, and
worth revisiting as an optimisation. `LISTEN` reduces the idle latency between a
commit and a publish, but notifications are not durable: a worker that is
restarting during a notification never learns about the row, so a poll is still
required for correctness. Adding notification on top of polling is a latency
improvement, not a design change, and it is listed in the production evolution
section rather than built now.

## Consequences

- An event can be published more than once: the worker can publish and then fail
  before marking the row. That is why delivery is at-least-once and the consumer
  is idempotent. See ADR 0007.
- The outbox table needs a retention policy. Published rows are kept for
  operational visibility and would be partitioned or pruned at volume, which the
  README states as a known limitation rather than implementing now.
- Publication latency is bounded by the poll interval, which is configurable and
  small by default.
- The broker can be down for as long as the outbox can hold rows, and the system
  stays correct. The demo kills NATS, creates twenty orders, shows the backlog
  growing while the API keeps returning 201, then restores the broker and shows
  the backlog drain.
