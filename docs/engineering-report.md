# Engineering report

## What was built

An order and inventory reservation platform: one business capability, built to
the depth where the interesting failures live. Two Go binaries over PostgreSQL
and NATS JetStream, plus a React operations console. Roughly 5300 lines of Go
excluding comments, an equal amount of Go test code, 800 lines of TypeScript, and
126 commits.

The system exists to make one claim falsifiable: inventory cannot be oversold
under any level of concurrency. Everything else is what that claim needs in order
to survive contact with a broker outage, a retrying client, and a crash between
two writes.

## Important decisions

Ten decisions are recorded in `docs/adr/`, each with the alternatives that were
genuinely considered and what they would have cost. The four that shaped
everything else:

**A conditional UPDATE arbitrates inventory** (ADR 0003). One statement whose
guard and write are atomic, rather than `SELECT FOR UPDATE` and a second
statement, or SERIALIZABLE with retry, or a Redis counter. The workload decides:
contention on a few hot rows, short writes, refusal as a normal outcome, and a
write that must compose with the order insert and the outbox append in one
transaction.

**PostgreSQL is the only system of record** (ADR 0002). The broker carries
messages and holds nothing that cannot be rebuilt from the database. That is what
makes the outage demonstration possible rather than merely described.

**The idempotency claim commits before the business transaction** (ADR 0005).
Inside it, two concurrent duplicates both begin, neither sees the other, and the
loser blocks on the primary key after having already reserved inventory. The cost
of splitting is a key that can be stuck in progress after a crash, which is
documented, surfaced in the console, and answered with 409 and `Retry-After`.

**JetStream rather than core NATS** (ADR 0008). This changed during the build.
Core NATS was the plan; it is fire and forget, so an event published while no
consumer is connected would be lost after the publisher had already marked the
row published. That failure is silent, which makes it the worst kind.

## Reliability

The transactional outbox makes the order and its event commit together. Delivery
is at-least-once and the consumer is idempotent, with the deduplication row
written in the same transaction as the side effect. Retries are bounded with full
jitter, and transient versus permanent is decided by the error taxonomy rather
than by matching driver text, so a malformed payload fails once instead of five
times.

`docs/reliability.md` is a table of failures and what an operator sees for each.

## Testing

149 Go test functions, 40 of them integration tests against real PostgreSQL and
NATS through Testcontainers, 15 web tests behind MSW, one end to end script and
one k6 scenario. The integration matrix runs on PostgreSQL 15 and 16.

The tests that carry the argument are named in `docs/testing-strategy.md`. Test
code is about the same size as production code, which is what proving a
concurrency claim costs.

## Performance

Measured, not estimated. Five units, two hundred concurrent buyers, one unit
each:

| metric | local | CI runner |
| --- | --- | --- |
| attempted | 200 | 200 |
| succeeded | 5 | 5 |
| conflicted | 195 | 195 |
| oversold | 0 | 0 |
| p50 | 19.2 ms | 148.3 ms |
| p95 | 22.6 ms | 175.1 ms |
| p99 | 23.5 ms | 177.3 ms |

The CI numbers are on a shared runner with the whole stack on one machine. They
are included because hiding them would make the local numbers look like a claim
about hardware this project does not have.

Contention is not throughput. That scenario starves the shelf on purpose, so
most of its responses are refusals and its rate says nothing about how much
work the write path does. `test/load/throughput.js` answers the other question
with a constant arrival rate against stocked inventory, on the same laptop with
the whole stack in Docker Desktop:

| offered | achieved | created | dropped | p50 | p95 | p99 |
| --- | --- | --- | --- | --- | --- | --- |
| 100 /s | 100.0 /s | 3001 | 0 | 1.9 ms | 3.9 ms | 5.8 ms |
| 1000 /s | 998.5 /s | 20001 | 0 | 2.2 ms | 29.1 ms | 50.4 ms |
| 2500 /s | 1018.1 /s | 25046 | 24955 | 4612.6 ms | 4831.2 ms | 4844.7 ms |

The third row is the ceiling, and it is reported rather than trimmed. At 2500
offered orders a second the machine sustains about a thousand, k6 cannot start
the rest, and the requests that do run queue for four and a half seconds. The
run fails its thresholds, which is the correct outcome: a load test that only
reports rates the system can meet is a load test that never finds the limit.

Nothing was oversold at any rate, and no order was refused while stock lasted.
The arrival rate is open by design. A fixed pool of virtual users would have
lowered the offered rate as responses slowed, and the system would have looked
healthy at every level.

### A captured profile

`PPROF_ENABLED=true` opens the runtime profiles on a loopback listener of the
binary's own. `docs/profiles/api-cpu-20s.pprof` is a twenty second cpu profile
of the api under about 550 orders a second, with its `pprof -top -cum` view
beside it in `api-cpu-20s.txt`. Read it with:

```
go tool pprof bin/api docs/profiles/api-cpu-20s.pprof
```

What it says, in the order it says it:

| cumulative | where |
| --- | --- |
| 39.2% | `syscall.rawsyscalln`, which is the network and the database socket |
| 35.3% | the whole middleware chain and handler, from `recovery` inward |
| 33.9% | `CreateOrderHandler.Handle` |
| 28.9% | the business transaction inside `WithinTx` |
| 24.8% | `pgx.Conn.Exec`, the statements themselves |
| 4.8% | the idempotency claim |
| 3.7% | the reservation update |

There is no application hotspot, which is the useful finding. The time is in
syscalls and in round trips to PostgreSQL, the middleware chain costs almost
nothing measurable next to them, and the two pieces of logic this repository
argues about, the idempotency claim and the conditional reservation, are under
five percent each. Optimising Go code here would buy nothing. The write path is
bound by the database, which is also what the throughput ceiling says.

The profile was captured with the api running on the host against the
containerised database, which is why its rate is lower than the 1000 a second
the in network run reaches. A profile is a shape rather than a benchmark, and
the shape is the same.

Two hot paths are benchmarked, because both are paid per request or per event
before any I/O happens. On an M1 Max:

```
BenchmarkFingerprint/lines=1        2141 ns/op    45.77 MB/s    2297 B/op     40 allocs/op
BenchmarkFingerprint/lines=10       8857 ns/op    45.62 MB/s    7931 B/op    163 allocs/op
BenchmarkFingerprint/lines=50      38365 ns/op    45.98 MB/s   37833 B/op    689 allocs/op
BenchmarkEnvelopeRoundTrip/marshal  1364 ns/op                   648 B/op      3 allocs/op
```

Fingerprinting is linear in body size at a steady 46 MB/s, which puts it three
orders of magnitude below the round trip it precedes: a 50 line order spends 38
microseconds being fingerprinted and around 20 milliseconds being reserved. It is
measured rather than assumed, because it runs before the work that could refuse
the request, so every rejected request pays it too.

Two query plans were checked with `EXPLAIN ANALYZE`. The reservation is an index
scan on the primary key, seven buffers. The outbox claim was sorting every due
row on every claim, which is in the audit findings below.

## Audit findings

The audit phase is where the honest part of this report lives. Nine areas were
re-examined on the assumption that every earlier decision was wrong. Seven real
defects were found. Four of them were found by running the system rather than by
reading it, which is the argument for the end to end script existing at all.

**1. Two publishers published the same event twice.** `FOR UPDATE SKIP LOCKED`
holds a row only for the claim transaction. Once the claim committed, the row was
still unpublished and still due, so a second instance claimed it while the first
was publishing. 108 of 120 events were published twice. The claim now leases the
row by pushing `next_attempt_at` forward, and the configuration refuses a lease
shorter than the publish timeout. Found by a test written for exactly this.

**2. The worker never started the consumer.** Events were published and nothing
processed them, so orders stayed pending forever. Both test suites passed,
because each constructs a consumer itself. Found by the compose smoke test, which
failed on "orders confirmed by the consumer: expected 2, got 0".

**3. A shutdown dead lettered healthy events.** A cancelled context failed the
transaction, that error is unclassified, and unclassified errors are permanent by
design, so the event was terminated and recorded as a failure. Every deployment
would have left entries an operator had to triage and discard. Cancellation is
now recognised before classification.

**4. Both services refused to start.** The telemetry resource carried a different
schema version than the SDK default, and merging them fails. No test had ever
called `telemetry.Setup`; one does now, for every exporter setting.

**5. The outbox claim sorted every due row.** Ordering by `occurred_at` while
filtering on a range of `next_attempt_at` cannot use the index order.
`EXPLAIN ANALYZE` on 20000 pending rows: 5.9 ms with the sort, 0.2 ms once the
ordering matched the index.

**6. Listing orders issued one query per order.** A page of a hundred orders cost
a hundred and one round trips. Invisible with two rows in the table. The
regression test counts round trips rather than asserting a time.

**7. A failed background refresh blanked the console.** An operator reading a
table during an incident lost the numbers they already had to an error banner.
Stale data now stays visible and says it may be behind. The stale case of the
request state union had been unreachable, which is how the gap went unnoticed.

Two smaller ones: the readiness probe wrote a problem document labelled as plain
json and called `WriteHeader` twice, and routing rejections were the only
responses in the API that were not problem documents.

The nightly image scan then found 35 high and 2 critical advisories in the
published console image, inherited from a base image that went stale in place
after weeks of green pull request pipelines. That is the check earning its place.

## Known limitations

- No authentication. Anyone who can reach the API can create and read orders.
- The outbox and `processed_events` grow without bound. Partitioning and a
  retention window are needed before real volume, and choosing the window needs
  operational data rather than a guess.
- A crash between the idempotency claim and the business transaction leaves a key
  in progress until its TTL.
- One PostgreSQL primary is the write ceiling, by design.
- Go production code is about 5300 lines against a stated budget of 4000. A
  simplification pass removed duplicated queries and an unreachable path, which
  recovered about seventy lines. The remainder is concentrated in
  `internal/platform` and in the order context, and cutting it further would mean
  removing a capability rather than removing waste. The budget was exceeded and
  the accounting is here rather than in a footnote.

## Production evolution

**100x traffic.** The hot row is the serialisation point. First move: batch the
reservation of a multi line order into one statement to cut round trips. Then
shard high velocity skus into multiple rows summing to the same stock, which
turns one hot row into several at the cost of a fan-out on read. Add read
replicas for the operational queries, which do not need the primary.

**Multiple regions.** The reservation cannot be made regional without giving up
the single arbiter, so the honest design is one write region with regional read
replicas, and reservation requests routed to the write region. Anything else
trades the oversell guarantee for latency, and that trade has to be a product
decision rather than an infrastructure one.

**The database becoming the bottleneck.** In order: connection pooling in front
of PostgreSQL, partitioning the outbox by month with old partitions dropped,
moving the operational read model to a replica, and only then considering a
separate store for inventory with the protocol cost that implies.

**Event volume growth.** The publisher scales horizontally today because of
`SKIP LOCKED` and the lease. The next constraint is the claim query itself, which
would be addressed by partitioning the outbox and claiming per partition.

**Bounded contexts needing independent deployment.** The domain has no
infrastructure imports and the application layer already talks to ports, so the
code moves cleanly. What does not move cleanly is the transaction: extracting
inventory means replacing one atomic write with a protocol, and the first
question would be whether the oversell guarantee is still worth what that
protocol costs. The answer is probably yes, and it would deserve its own ADR.
