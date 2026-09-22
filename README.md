# Fulcrum

[![ci](https://github.com/xrodriguezb/fulcrum/actions/workflows/ci.yml/badge.svg)](https://github.com/xrodriguezb/fulcrum/actions/workflows/ci.yml)

An order and inventory reservation platform, built to prove one falsifiable
claim: **inventory cannot be oversold under any level of concurrency**, and to
show what has to be true around that claim for it to survive a broker outage, a
duplicate request, and a crash between two writes.

```
                    HTTP                          
 client  ────────────────────────▶  cmd/api
                                      │
                                      │  one transaction
                                      ▼
              ┌───────────────────────────────────────────┐
              │ reserve inventory (conditional UPDATE)     │
              │ persist order and lines                    │
              │ append order.created to outbox_events      │
              │ complete the idempotency key               │
              └───────────────────────────────────────────┘
                                      │
                                      ▼
                                 PostgreSQL
                                      ▲
                                      │ claim with SKIP LOCKED and a lease
                                      │
 cmd/worker ──────────── publish ────▶ NATS JetStream
      │                                      │
      │◀────────────── consume ──────────────┘
      │
      └── dedup row and side effect in one transaction
```

## Run it

Requirements: Docker. Nothing else.

```
make demo
```

That builds the stack, waits on real health checks, seeds one sku with five
units, fires two hundred concurrent buyers at it, kills the broker and keeps
accepting orders, restores the broker and drains the backlog, and poisons a
message to show the dead letter path. It exits non-zero if a single unit was ever
oversold.

## The proof

Five units on the shelf, two hundred concurrent buyers, one unit each. Numbers
from an actual run of `make demo`, not from an argument:

| metric | value |
| --- | --- |
| attempted | 200 |
| succeeded | 5 |
| conflicted | 195 |
| unexpected responses | 0 |
| **oversold** | **0** |
| final available | 0 |
| final reserved | 5 |
| p50 latency | 19.2 ms |
| p95 latency | 22.6 ms |
| p99 latency | 23.5 ms |

The same claim is a deterministic Go test that runs on PostgreSQL 15 and 16 in
CI, under the race detector.

`docs/demo-transcript.txt` is the captured output of that run, including the
broker being killed and the backlog draining afterwards.

## What this demonstrates

- **Atomic reservation.** One conditional UPDATE, guard and write in a single
  statement, with CHECK constraints as a backstop that a test proves is real.
- **Idempotency done properly.** A two transaction claim protocol, a canonical
  request fingerprint, and every row of the behaviour matrix tested, including
  sixteen identical requests racing on one key.
- **A transactional outbox.** The event commits with the order. Claiming uses
  `FOR UPDATE SKIP LOCKED` with a lease, so a second publisher divides the work
  instead of duplicating it.
- **At-least-once delivery with an idempotent consumer.** Deduplication shares a
  transaction with the side effect. Nothing here claims exactly-once.
- **Failure handling that is shown, not described.** The demo kills the broker
  and the system stays correct. The same scenario runs in CI on every pull
  request.
- **One contract, two languages.** The TypeScript client types are generated from
  `api/openapi.yaml`, committed, and the pipeline fails if regenerating them
  produces a diff.
- **A shift-left pipeline.** Hooks catch what hooks can catch, and the pipeline
  runs what a laptop cannot afford to.

## Reading it in thirty minutes

| Minute | Where to look |
|---|---|
| 1 | This page |
| 3 | `make demo` |
| 8 | `internal/inventory/infra/reserver.go`, `internal/order/app/create_order.go`, `internal/outbox/app/publisher.go` |
| 15 | `test/integration/reservation_test.go` and `test/integration/create_order_test.go` |
| 20 | `git log --oneline`, including the fixes found during the audit |
| 25 | `docs/adr/0003-inventory-concurrency-control.md` and `docs/adr/0005-idempotency-contract.md` |
| 30 | `docs/engineering-report.md` |

## Documentation

| Document | Contents |
|---|---|
| `docs/engineering-report.md` | What was built, what was found, what is left |
| `docs/architecture.md` | Shape, contexts, enforced boundaries |
| `docs/domain-model.md` | Value objects, the aggregate, events |
| `docs/concurrency.md` | The claim, its proof, deadlocks, shutdown |
| `docs/reliability.md` | Failure by failure, and what an operator sees |
| `docs/testing-strategy.md` | What is tested where, and what is not |
| `docs/security.md` | Threat model with residual risk named |
| `docs/observability.md` | One order followed across three processes |
| `docs/ci-cd.md` | The five layers and the reasoning |
| `docs/adr/` | Ten decisions, each with its rejected alternatives |

## Commands

```
make setup     install hooks and tooling
make verify    fmt, lint, arch, test, race, contract, security, build
make test-int  integration tests against real PostgreSQL and NATS
make up/down   compose lifecycle
make e2e       compose smoke test, including broker kill and recovery
make load      k6 against a running stack
make demo      the narrated demonstration
make ci        everything the pipeline runs, in its order
```

## Non-goals

Stated so their absence reads as a decision: authentication, user registration,
catalog management, payments, shipping, refunds, coupons, multi-tenancy,
microservices, Kubernetes, service mesh, event sourcing, and CQRS read models
beyond simple projections. Each would add breadth where this project spends its
budget on depth.

## Known limitations

- **No authentication.** Anyone who can reach the API can create and read orders.
  First thing to add for real use.
- **The outbox and deduplication tables grow without bound.** Both need
  partitioning and a retention policy before real volume. Deciding the retention
  window needs operational data rather than a guess.
- **A crash between the idempotency claim and the business transaction** leaves a
  key in progress until its TTL. Retries receive 409 with `Retry-After` during
  that window, and the console shows the stuck keys.
- **One PostgreSQL primary is the write ceiling.** Deliberate: see ADR 0002.
- **Go production code is about 5300 lines against a 4000 line budget.** The
  overage is real and is accounted for in the engineering report rather than
  presented as a rounding error.
- **The nightly image scan can fail on an advisory nobody here introduced.** That
  is the check working, and it has already caught one.
