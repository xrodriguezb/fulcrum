# 0011. Readiness reports what this instance can do

Status: accepted
Date: 2026-09-22

## Context

The specification says `/readyz` checks the database and the broker. That was
implemented literally, and then the demonstration exposed the contradiction: with
NATS stopped, the API returns 201 for every order and `/readyz` returns 503.

Both statements are true, and together they are wrong. A load balancer or an
orchestrator reading that probe removes the instance from rotation, so order
intake stops during a broker outage. The outbox exists precisely so that it does
not have to. The probe would cause the outage the architecture was built to
prevent.

This was found by running the system, not by reading it. The demo asserts that
orders keep being accepted while the broker is down, and the probe said the
opposite about the same process at the same moment.

## Decision

Readiness answers one question: can this instance do its job.

A dependency is marked critical or not, per binary:

- **API.** PostgreSQL is critical, because every order is a transaction. The
  broker is not, because acceptance is decoupled from publication. A broker
  failure returns 200 with `"status": "degraded"` and `"nats": "degraded"` in the
  body.
- **Worker.** Both are critical. Publishing and consuming are its job, and it
  cannot do that job without the broker.

The asymmetry is the decision. Readiness is not a health summary of the
environment; it is a claim about this process, and the two binaries have
different jobs.

Liveness stays as it was: it checks nothing, because a liveness probe that fails
during a dependency incident turns a degradation into a restart loop.

## Alternatives considered

**Keep the literal reading of the specification.** Rejected once the failure was
observed. The specification is a description of intent, and its intent is a
system that survives a broker outage. A probe that contradicts the demonstration
is a bug in one of them, and here it was the probe.

**Report unready and rely on the orchestrator's minimum available setting.**
Rejected. It makes correctness depend on a deployment parameter nobody in this
repository controls, and if every replica reports unready at once, every setting
gives the same answer.

**A separate endpoint, `/degraded`, for informational dependencies.** Rejected as
ceremony. Nothing consumes a third probe, and the information belongs in the body
of the one that already exists, where an operator looking at a degraded instance
finds it.

**Return 200 with no signal at all when the broker is down.** Rejected. That
hides a real condition. The degraded status is what tells an operator to look at
the outbox depth before it becomes a backlog.

## Consequences

- A broker outage no longer removes API instances from rotation. The demo and the
  probe now agree.
- `"status": "degraded"` is part of the readiness contract and appears in the
  OpenAPI document.
- The worker is removed from rotation during a broker outage, which is correct:
  it has nothing to do until the broker returns, and its own metrics endpoint
  stays up so the outage remains observable.
- Adding a dependency now forces a decision about what its absence means. That is
  the intended cost.
