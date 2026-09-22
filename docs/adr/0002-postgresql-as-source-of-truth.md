# 0002. PostgreSQL is the single source of truth

Status: accepted
Date: 2026-09-21

## Context

Inventory reservation is a contended write. Several orders can arrive at the same
instant for the last unit of the same SKU, from several API replicas, and exactly
one of them may succeed. Something has to arbitrate. The choice of arbiter
determines how the rest of the system can be written, so it is made once, here.

Separately, the system publishes an `order.created` event. If the event store and
the order store are different systems, then "the order exists but the event was
never published" and "the event was published but the order was rolled back" both
become reachable states.

## Decision

PostgreSQL is the only system of record. It arbitrates inventory, stores orders,
holds idempotency keys, and holds the outbox and the dead letter queue. The
broker carries messages; it never holds state that cannot be reconstructed from
the database.

Concretely:

- Reservation is a single conditional UPDATE guarded by `available >= $2`, with
  `CHECK (available >= 0)` as a schema-level backstop. See ADR 0003.
- The `order.created` event is written to `outbox_events` inside the same
  transaction that persists the order. See ADR 0004.
- The consumer's deduplication row and its side effect share one transaction.
- The broker is JetStream, which is durable, but the outbox remains authoritative:
  an event that is in the database and not in the broker will be published again,
  while an event in the broker that never committed in the database cannot exist.

## Alternatives considered

**Redis for inventory counters, PostgreSQL for orders.** Rejected. A decrement in
Redis is fast and atomic, and it is the standard answer to this problem. It fails
here for one reason: the decrement and the order insert cannot commit together.
A crash between them leaves stock reserved with no order, or an order with no
reservation, and reconciling that requires a background job that is harder to get
right than the thing it is fixing. Redis would also become a second system whose
durability configuration determines whether inventory survives a restart.

**Kafka as the source of truth with a projection for reads.** Rejected. It makes
the ordering and replay story excellent and the "can I reserve one unit right now"
story poor: the answer has to come from a projection that is, by construction,
behind the log. Overselling then becomes a function of projection lag, which is
the failure mode the whole project is meant to eliminate.

**A dedicated inventory service with its own store.** Rejected for the reasons in
ADR 0001: it converts an atomic decision into a distributed one.

**Dual writes to the database and the broker, without an outbox.** Rejected.
There is no ordering of two writes to two systems that is safe under a crash
between them. This is the failure the transactional outbox pattern exists to
remove, and skipping it would undermine the reliability claims of the project.

**Serializable isolation for everything.** Rejected as a blanket policy. It is a
correct answer to the reservation problem and a poor default for the rest of the
system, because it makes every read path a candidate for serialization failure
and retry. The conditional UPDATE gives the same guarantee for the contended row
at read committed. See ADR 0003.

## Consequences

- The database is the scaling bottleneck by design. The README states this
  explicitly, and the production evolution section describes what would change
  first: partitioning the outbox, moving hot SKUs to their own rows, and adding
  read replicas for the operational queries that do not need the primary.
- Every correctness claim in this repository is testable against a real
  PostgreSQL instance, which is why the integration suite uses Testcontainers
  rather than a fake.
- The broker can be lost entirely without losing data. The demo demonstrates
  exactly that: NATS is killed, orders keep being accepted, the outbox grows, and
  the backlog drains when the broker returns.
- The application role is not the schema owner. Migrations run with elevated
  rights at startup; the request path runs with a role that cannot alter the
  schema.
