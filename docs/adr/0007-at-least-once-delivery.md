# 0007. At-least-once delivery with an idempotent consumer

Status: accepted
Date: 2026-09-21

## Context

The outbox publisher reads a row, publishes it and marks it published. Those are
two systems and two steps, so there is a window between them. A crash inside that
window leaves an event that was published and not marked, and the next run
publishes it again.

The window cannot be closed. It can only be moved: marking before publishing
turns "published twice" into "never published", which is worse.

## Decision

Delivery is at-least-once, stated plainly in the README and in this document.
Nothing in the system claims exactly-once.

What makes that sufficient is that the consumer is idempotent. It inserts a row
into `processed_events` keyed by `(consumer_name, event_id)` in the same
transaction as the side effect. A redelivered event finds the key present and
does nothing. One effect per event, achieved by the consumer rather than promised
by the transport.

Three mechanisms support this, in order of how much weight they carry:

1. **The deduplication table.** The correctness mechanism. Unbounded in time,
   backed by a primary key, and committed with the effect it describes.
2. **The claim lease.** The claim pushes `next_attempt_at` forward, so a row
   being published by one instance is not visible to another. This turns a
   routine duplicate into an exceptional one.
3. **The broker's message id.** JetStream deduplicates identical message ids
   inside a short window. It costs one header and removes the most common
   duplicate, a publisher retrying after a timeout.

Mechanism 1 would be enough on its own. The other two exist so that it is rarely
exercised, not because it needs help.

## Alternatives considered

**Claiming exactly-once.** Rejected because it would be false. Exactly-once
delivery over a network requires the broker and the consumer to share a
transaction, which they do not. Systems that advertise it are describing
exactly-once processing built on deduplication, which is what this document
describes without the marketing.

**Two-phase commit between PostgreSQL and NATS.** Rejected. It would give real
atomicity across the two systems, and it costs a coordinator, a recovery
protocol, and a failure mode where a prepared transaction blocks the database
until an operator intervenes. For an event that the consumer can safely see twice,
that is a large price for a property already obtained more cheaply.

**Marking published before the publish.** Rejected. It converts duplicate
delivery into silent loss. A duplicate is visible and handled; a lost event is
neither.

**Deduplicating only in the broker.** Rejected. The JetStream duplicate window is
finite by design. An event redelivered after the window, which is exactly what
happens when a consumer is down for a while, would be processed twice. The window
is an optimisation, not a guarantee.

**A time-bounded deduplication table, pruned aggressively.** Deferred rather than
rejected. Unbounded growth is real and the README lists it as a known limitation.
Pruning has to be argued against the maximum redelivery interval, which depends
on the retry policy and on how long a consumer can be down, and choosing that
number without operational data would be a guess presented as a decision.

## Consequences

- The consumer must be idempotent for every side effect it ever gains. That is a
  standing constraint on future work, stated here so it is not rediscovered.
- `processed_events` grows with event volume. Documented as a limitation with a
  concrete next step: partition by month and drop old partitions.
- Duplicate delivery is a normal event, not an incident. The order aggregate
  reflects that: confirming an already confirmed order returns a distinct
  sentinel rather than an invalid transition error, so the consumer can treat it
  as success.
- The demo shows the property rather than asserting it: it kills the broker,
  keeps creating orders, restores the broker and shows the backlog draining with
  no duplicate side effect.
