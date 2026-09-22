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
| 1000 /s | 999.9 /s | 30001 | 0 | 2.4 ms | 30.0 ms | 43.8 ms |
| 2500 /s | 1018.1 /s | 25046 | 24955 | 4612.6 ms | 4831.2 ms | 4844.7 ms |

The third row is the ceiling, and it is reported rather than trimmed. At 2500
offered orders a second the machine sustains about a thousand, k6 cannot start
the rest, and the requests that do run queue for four and a half seconds. The
run fails its thresholds, which is the correct outcome: a load test that only
reports rates the system can meet is a load test that never finds the limit.

The delivered rate at a thousand a second repeats; the tail does not. Four runs
of that row on the same machine, minutes apart and with nothing else changed,
gave a p95 of 30.0, 183.0, 186.7 and 30.0 ms while the achieved rate stayed
within a tenth of a percent every time. The table reports one run of each row,
so read the p95 as the better half of a bimodal distribution rather than as a
number this laptop delivers reliably. The likely cause is the host, not the
write path: Docker Desktop shares a virtual machine with everything else running
on a development laptop. A number measured on dedicated hardware would mean
something; this one says the throughput holds and the tail is not worth
quoting to three significant figures.

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

## Second audit

A second pass went over the repository on the assumption that the first audit had
also been wrong. Twelve defects were found and fixed. The ones that matter were
again found by running the system rather than by reading it, and two of them were
in the machinery that was supposed to be doing the finding.

| # | Severity | Component | Finding | Commit |
| --- | --- | --- | --- | --- |
| 1 | must fix | consumer | an event id the stores cannot hold loops forever | `69c7c51` |
| 2 | must fix | pipeline | pre-commit vet and lint analysed nothing | `9ea3ebb` |
| 3 | must fix | listings | page totals wrong past the end, and a full scan per page | `5d8ed1c` |
| 4 | blocker | idempotency | a committed key could be reopened and the order duplicated | `21a9c6d` |
| 5 | must fix | console | a reconnected stream left the polling fallback running | `adda024` |
| 6 | should fix | deployment | the published container was the least hardened one | `e8ed73d` |
| 7 | must fix | error contract | a database outage answered 500 rather than 503 | `5443459` |
| 8 | should fix | configuration | ten tunables existed only in the source | `836d192` |
| 9 | should fix | error contract | an error code the system never emits | `de97903` |
| 10 | should fix | load testing | the oversell scenario could not run from a fresh stack | `9a73841` |
| 11 | should fix | documentation | a variable latency reported as a stable one | `29ee684` |
| 12 | should fix | pipeline | the content scanner drowned its own output in binary | `6ba1589` |

**1. A poison event that could never be dead lettered.** An envelope only had to
carry a non-empty id, while the deduplication row and the dead letter entry both
keep it in a uuid column. An envelope that decoded with an id of any other shape
failed the deduplication insert, which is classified as unavailable and therefore
retried, and then failed the dead letter insert too. A consumer that cannot
record a failure cannot terminate the delivery, so the message went back to the
broker and came round again forever. The test that found it watched ninety
seconds of redelivery with nothing in the queue an operator reads. The envelope
now validates the shape of both identifiers, which turns the case into an
undecodable payload, and those were already dead lettered on the first delivery.

**2. The pre-commit hook analysed nothing.** The script that turns staged files
into package directories deduplicated with an associative array, which needs bash
4. The bash on the path of a macOS machine is 3.2, where `declare -A` fails, so
the script printed nothing and exited 2. Its callers read only its output and
treat an empty selection as a commit with no Go in it, so `go vet` and
`golangci-lint` both reported success while looking at nothing. Every Go commit
in this repository had been pushed without either of them running locally, which
is the explanation for lint failures reaching the pipeline earlier. The selector
is now portable, the callers fail when it fails, and a self-test covers the cases
including the exit status.

**3. Listings counted the whole table on every page.** Both listings carried the
collection total on the rows of the page through `count(*) OVER ()`. A page past
the last one has no rows to carry it, so with 71237 orders in the table an offset
of 999999 answered that the collection was empty, which a console pager cannot
recover from. It was also expensive: that window carried every column of every
row through the aggregate, so a twenty row page touched 62422 buffers and spilled
465 blocks to a temporary file in 32 ms. Counting in its own statement is 1071
buffers and 9 ms, and the page alone is 23 buffers.

**4. A committed order could be created twice.** The use case releases the
idempotency key whenever the business transaction returns an error, and the
release was unconditional. A commit that reaches the server and then loses its
acknowledgement returns an error to a caller whose work is committed, so the
release arrived for a key the same transaction had just completed: it overwrote
the status with failed and replaced the stored response with the failure reason.
The store hands a failed key to the next claim by design, so the client's retry
then won the claim and ran the whole use case again, against stock reserved a
second time. The release now applies only while the claim is still in progress.
This is the one finding in the audit that could corrupt business state, and it
needed a partition at exactly the wrong moment to happen.

**5. The console kept polling after the stream came back.** Stopping the polling
fallback cleared the pending timer, but a request already in flight schedules the
next one when it lands, and that request routinely outlives the reconnection: the
poll interval is three seconds and the reconnect delay is five. Every drop left
another polling loop behind. A test that holds a poll open across the
reconnection sees five requests where two are correct.

**6. The exposed container was the least hardened.** The api and the worker run
with a read only root filesystem and every capability dropped. The console, which
is the only service published on a host port, had neither. It now has both, with
tmpfs for the two directories nginx writes.

**7. A database outage looked like a bug.** Every failure the postgres
repositories returned was classified as internal, so with the database stopped,
creating an order answered 500 while readiness correctly answered 503. The two
say different things: 500 tells a client the request will fail the same way again
and tells an operator to look for a defect in the order path. Driver errors now
go through one classifier, where the connection, resource and operator
intervention SQLSTATE classes are unavailable and a constraint violation or a bad
scan stays internal. The operation name travels in the cause for the log rather
than in the message, so a client learns nothing about the statement that failed.

**8 through 12** are smaller: ten environment variables that were read by the
configuration and documented nowhere, among them the outbox claim lease and the
pool timeouts, now with a test that fails on any future drift; an error code
declared in the public contract and never emitted; a load scenario that refused
to run against a stack that had just been brought up, which is the scenario
proving nothing is oversold; a latency figure in this document that four re-runs
showed to be bimodal, now reported as such; and a content scanner that decoded
the captured profile as text and put 4823 warnings on the same stream it reports
violations on, which is where a real violation would have been missed.

### What the audit did not find

The suite is not theatre. Removing the sku ordering from the reservation made the
multi-item test fail three times out of three with a real PostgreSQL deadlock, and
changing the guard on the reservation would be caught by the oversell test's exact
counts. The central invariant was re-proved independently through the HTTP API
against the running stack: two hundred concurrent requests against five units gave
five creations, a hundred and ninety-five conflicts, zero available, five
reserved and nothing oversold. A hundred concurrent requests sharing one
idempotency key gave one order, ninety-nine identical replays and one in-progress
conflict. Semantically identical json with reordered keys replayed; the same key
with a different body was refused.

Hostile input was answered correctly without leaking: injection strings, script
tags, malformed and deeply nested json, oversized and mistyped bodies, unexpected
methods, huge identifiers and out of range pagination all produced problem
documents carrying a trace id and no driver text, query, path or host. Route
labels are patterns rather than paths, so metric cardinality stays bounded. Every
declared metric has a site that increments it. `govulncheck`, `npm audit` and
`trivy` are clean. No exported symbol in the production packages is unused, and
every linter suppression names its reason.

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
