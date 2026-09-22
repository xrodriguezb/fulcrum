# Concurrency

The claim this repository exists to prove: **inventory cannot be oversold under
any level of concurrency, across any number of API instances.**

## How the claim is enforced

One conditional UPDATE, executed inside the business transaction:

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

The guard and the write are one atomic statement, so two transactions cannot both
observe enough stock and then both take it. Zero rows affected is a business
answer, not an error: the reserver reads the row once more to say whether the sku
is unknown or the stock is gone.

Two CHECK constraints sit underneath as a backstop that must never fire. A test
writes a negative value outside the application path and asserts the database
refuses it, which is what makes the backstop a fact rather than a comment.

The alternatives, and why each loses for this workload, are argued in
`docs/adr/0003-inventory-concurrency-control.md`.

## How the claim is proven

`TestReservationCannotOversell` seeds five units, releases two hundred goroutines
through a start barrier so they genuinely contend, and asserts five successes,
one hundred and ninety five conflicts, zero available, five reserved, and no
error other than the expected conflict. It runs under `-race`, against a real
PostgreSQL, on both major versions in CI.

The same claim is then made end to end by k6 against the running stack, so it
holds through the HTTP layer and the connection pool and not only in a test
harness. From a real run:

| metric | value |
| --- | --- |
| attempted | 200 |
| succeeded | 5 |
| conflicted | 195 |
| oversold | 0 |
| final available | 0 |
| final reserved | 5 |

## Deadlock avoidance

An order can reserve several skus. Two orders touching the same pair in opposite
sequence would take row locks in opposite order, and PostgreSQL would resolve the
cycle by killing one of them at random.

Lines are sorted by sku in two places: in the aggregate constructor, and in the
reserver before execution. `TestConcurrentMultiSKUReservationsDoNotDeadlock` runs
sixty pairs of opposed two-sku reservations and fails on any deadlock error.

## The publisher

A single claimer feeds a fixed pool of publishers through a bounded channel.

The bound is what makes backpressure real. When the broker is slow the send
blocks, the claimer stops claiming, and rows stay in the database where they are
durable rather than accumulating in memory where a restart loses them.
`TestClaimingStopsWhenPublishersAreSaturated` asserts exactly that.

Claiming uses `FOR UPDATE SKIP LOCKED` so a second instance divides the work
rather than queueing behind the first, and it leases the row by pushing
`next_attempt_at` forward. The lease is the part that was missing in the first
implementation: the row lock only lasts for the claim transaction, so without a
lease a second publisher claimed rows the first was still publishing.
`TestTwoPublisherInstancesShareTheOutboxWithoutDuplicating` caught it, with 108
of 120 events published twice.

## Shutdown

On SIGTERM the claimer stops immediately and the workers get a bounded drain
budget. A publish that is already in flight does not inherit cancellation:
aborting it could mark an event failed that the broker had already accepted.
Anything the drain cannot finish is left unpublished and unleased, so the next
run takes it.

The consumer treats cancellation as an interruption, not a failure: an
interrupted event goes back to the broker rather than to the dead letter queue.
That was also a defect first, and every deployment produced entries an operator
had to triage.

## Goroutines

`go.uber.org/goleak` runs for the whole publisher package through `TestMain`, so
a pool that is never waited on or a channel that is never closed fails the suite
rather than leaking in production. Nothing in the system creates goroutines
without a bound: the publisher pool is fixed, the consumer is a single loop, and
the SSE endpoint runs one goroutine per connection which the request context
ends.
