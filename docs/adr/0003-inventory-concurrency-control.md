# 0003. Inventory concurrency control by conditional UPDATE

Status: accepted
Date: 2026-09-21

## Context

Several orders can arrive at the same instant, from several API replicas, for the
last unit of the same SKU. Exactly one may succeed. This is the only decision in
the repository that the whole project is built to justify, so the alternatives
are argued rather than listed.

The workload has a specific shape, and the shape decides the answer:

- Contention is concentrated on a small number of hot rows, not spread evenly.
- A reservation is a short write, not a long computation.
- A refusal is an ordinary business outcome, not an error. Under the demo load,
  195 of 200 requests are refused and all 200 must stay fast.
- The write must compose with other writes: the order, the outbox event and the
  idempotency completion commit in the same transaction.

## Decision

A single conditional UPDATE, executed once per line, inside the business
transaction:

```sql
UPDATE inventory_items
SET    available  = available - $2,
       reserved   = reserved + $2,
       version    = version + 1,
       updated_at = now()
WHERE  sku = $1
  AND  available >= $2
RETURNING available, reserved, version, unit_price_cents, currency;
```

Zero rows affected means the guard refused, which the reserver translates into a
domain conflict after one further read that distinguishes "not enough stock" from
"no such sku". Two CHECK constraints, `available >= 0` and `reserved >= 0`, sit
underneath as a backstop that must never fire in correct operation.

Lines are sorted by SKU before execution. Two orders touching the same pair of
items in opposite sequence would otherwise take row locks in opposite order, and
PostgreSQL would resolve the cycle by aborting one of them at random.

## Alternatives considered

**`SELECT ... FOR UPDATE` then a separate UPDATE.** Correct, and the most common
answer. Rejected because it costs two round trips per line and holds the row lock
across both, which widens the window during which every other buyer of that SKU
is blocked. The conditional UPDATE does the same work in one statement and holds
the lock for the duration of one statement. Under 200 concurrent buyers of one
row, that difference is the difference between a queue that drains and a queue
that grows.

**SERIALIZABLE isolation with retry.** Correct, and the most principled answer.
Rejected because of what it costs everywhere else. Serialization failures are not
confined to the contended statement: the whole transaction aborts, including the
order insert, the outbox write and the idempotency completion, and the retry has
to be able to redo all of it. Under the demo load, the large majority of
transactions would abort and retry, so throughput would be governed by the retry
loop rather than by the database. The conditional UPDATE gives the same guarantee
for the contended row at READ COMMITTED, because the guard and the write are one
atomic statement.

**Optimistic concurrency with a version compare and retry.** This is the same
mechanism with the comparison moved from the quantity to a version column:
`WHERE sku = $1 AND version = $2`. Rejected because it is strictly weaker for
this workload. Comparing the version fails whenever the row changed at all, even
when 400 units are available and two buyers each want one, so it manufactures
conflicts that the business does not have. Comparing availability fails only when
the business answer is actually no. The version column is still maintained,
because it is useful for the operations console and for detecting lost updates,
but it is not the arbiter.

**An application-level mutex.** Rejected outright. It holds only within one
process, so it stops working the moment a second API replica starts, which is the
exact condition the claim is about. It would also make a correctness property
depend on deployment topology, which is how systems pass their tests and oversell
in production.

**Redis with `DECRBY` and a Lua guard.** Fast, atomic, and genuinely attractive
for read-heavy inventory. Rejected because the decrement cannot commit with the
order. A crash between the two leaves stock reserved with no order, or an order
with no reservation, and reconciling that requires a compensating job that is
harder to make correct than the thing it repairs. It also introduces a second
system whose durability settings silently determine whether inventory survives a
restart.

**Reserving through an event stream and a projection.** Rejected for the reason
in ADR 0002: the answer to "can I take one unit right now" would come from a
projection that is behind the log by construction, so overselling would be a
function of projection lag.

## Consequences

- The hot row is a serialization point. Throughput on one SKU is bounded by how
  fast PostgreSQL can apply short updates to one row, which is thousands per
  second and far beyond what this system is built for. The README states where
  that ceiling is and what would be done about it at 100 times the traffic.
- A refusal costs one round trip and one extra read for classification, and takes
  no locks that outlive the statement.
- The CHECK constraints are not enforcement, they are a proof that enforcement
  worked. A test deliberately writes a negative value outside the application
  path and asserts the database refuses it.
- Reservation ordering lives in the aggregate constructor and in the reserver, so
  no call site can forget it. An integration test runs sixty pairs of opposed
  multi-sku reservations and fails if any of them deadlocks.
