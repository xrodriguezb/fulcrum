# 0001. Modular monolith with two binaries

Status: accepted
Date: 2026-09-21

## Context

Fulcrum implements one business capability: reserving inventory for an order
without ever overselling it, and propagating that fact to other parts of the
system. The capability spans four concerns that are usually discussed as if they
were separate services: order creation, inventory reservation, event publication,
and event consumption.

The deployment shape has to be chosen before any code exists, because it decides
what a transaction can span. That is not a detail that can be revisited cheaply
later: the correctness argument of this system rests on inventory reservation,
order persistence and event publication committing together or not at all.

## Decision

A modular monolith with explicit bounded contexts, compiled into two binaries
that share one codebase and one database:

- `cmd/api` serves HTTP and owns the write path.
- `cmd/worker` publishes outbox events and consumes them back.

Contexts live under `internal/<context>/{domain,app,infra}`. Domain packages
import only the standard library and each other. That boundary is not a
convention: `test/architecture_test.go` walks the import graph and fails the
build if a domain package reaches for `database/sql`, `net/http`, the broker
client or a logging implementation.

The two binaries exist because their lifecycles genuinely differ. The API is
request-scoped and scales with traffic; the worker is a long-running loop that
scales with event volume and must drain in-flight work on shutdown. Running them
in one process would couple a deployment of the write path to a deployment of the
publisher, and would make a slow publisher a source of request latency.

## Alternatives considered

**Microservices, one service per context.** Rejected. The central invariant is
that reserving inventory, persisting the order and recording the event are atomic.
Splitting order and inventory into separate services replaces one transaction with
a distributed protocol: either a saga with compensating actions, or two-phase
commit. A saga makes overselling possible during the compensation window, which
is precisely the property this system exists to rule out. The cost is real and
the benefit, independent deployment of four contexts owned by one team, is not.

**A single binary containing both the API and the worker.** Rejected, though it
is defensible. It would reduce the deployment surface to one unit and remove a
compose service. It was rejected because the worker's shutdown semantics are
strict: on SIGTERM it must stop claiming, drain in-flight publishes within a
bounded timeout, and leave anything it cannot finish unclaimed. Sharing a process
with an HTTP server means one shutdown budget for two very different drain
behaviours, and it makes a publisher that is saturating the database pool an API
latency problem. Keeping them separate costs one container and buys independent
failure and independent scaling.

**A layered monolith organised by technical layer** (`handlers/`, `services/`,
`repositories/`). Rejected. That layout groups code by mechanism rather than by
meaning, so every change to one business rule touches three directories and no
directory describes a capability. It also gives the architecture test nothing to
enforce, because there is no domain package to isolate.

**Event sourcing as the persistence model.** Rejected. It would make the audit
trail a byproduct of the design, which is attractive. It also makes the
"available quantity" question a fold over a stream, which turns the one query
that must be fast and atomic into the most expensive operation in the system. A
conditional UPDATE against a single row is both simpler and stronger here. See
ADR 0003.

## Consequences

- One transaction spans order persistence, inventory reservation and the outbox
  write. The core invariant is enforced by the database rather than by protocol.
- The architecture test is the only thing keeping the boundaries honest. If it is
  deleted, the design degrades silently. It runs in the pre-push hook and in CI.
- Extraction of a context into its own service remains possible: the domain has no
  infrastructure imports, and the application layer already talks to ports. It
  would require replacing the shared transaction with an explicit protocol, which
  is exactly the cost this decision defers until it is justified.
- Both binaries share configuration and platform packages, so a change to the
  connection pool or the logger affects both. That is intended.
