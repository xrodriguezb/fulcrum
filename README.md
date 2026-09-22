# Fulcrum

[![ci](https://github.com/xrodriguezb/fulcrum/actions/workflows/ci.yml/badge.svg)](https://github.com/xrodriguezb/fulcrum/actions/workflows/ci.yml)

An order and inventory reservation platform, built to prove one falsifiable
claim: **inventory cannot be oversold under any level of concurrency**, and to
show what has to be true around that claim for it to survive contact with a
broker outage, a duplicate request, and a crash between two writes.

This repository is a work in progress. Sections that require measured numbers,
such as load test results and the audit findings, are filled in from real runs
rather than written in advance.

## What it demonstrates

- Atomic inventory reservation through a single conditional UPDATE, with
  schema-level CHECK constraints as a backstop.
- An idempotent `POST /api/v1/orders` built on a two transaction claim protocol,
  including the case of two concurrent identical requests.
- A transactional outbox with `FOR UPDATE SKIP LOCKED`, a bounded worker pool,
  real backpressure, and graceful drain on SIGTERM.
- At-least-once delivery with an idempotent consumer, bounded retry with full
  jitter, and a dead letter queue with operator-facing context.
- A React and TypeScript operations console whose API types are generated from
  the OpenAPI contract, so the two languages cannot drift.
- A shift-left pipeline: the cheaper a check is, the earlier it runs.

## Running it

Requirements: Docker. Everything else runs in containers.

```
make setup   # install hooks and Go tooling
make verify  # the gate a commit has to pass
```

The full demonstration, the compose stack and the load test arrive with the
operational phase of the build.

## Non-goals

Stated so that their absence reads as a decision rather than an omission:
authentication, user registration, catalog management, payments, shipping,
refunds, coupons, multi-tenancy, microservices, Kubernetes, service mesh, event
sourcing, and CQRS read models beyond simple projections. Each one would add
breadth where this project is spending its budget on depth.

## Documentation

- `docs/adr/` records the decisions that constrain everything else.
- `api/openapi.yaml` is the API contract and the source of truth for both
  languages.
