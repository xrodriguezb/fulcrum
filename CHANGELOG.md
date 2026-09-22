# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
the versions follow [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-09-22

First release. The system exists to make one claim falsifiable: inventory
cannot be oversold under any level of concurrency. Everything below is either
that claim, the proof of it, or the plumbing that keeps it true in an
unreliable world.

### Added

- **Atomic reservation.** A single conditional update refuses to take more than
  is available, with sorted lines so two concurrent orders cannot deadlock on
  each other, and check constraints as a backstop that fails loudly rather than
  storing a negative quantity.
- **Idempotent order creation.** Two transactions, the claim committed before
  the work, with a canonical fingerprint of the request so the same key with a
  different body is a conflict rather than a silent replay.
- **Transactional outbox.** Events are written in the business transaction and
  published afterwards by a worker that claims with `FOR UPDATE SKIP LOCKED`
  under a lease, so two instances share the table without publishing anything
  twice.
- **Idempotent consumer.** At-least-once delivery from NATS JetStream with a
  deduplication row written in the same transaction as the side effect, a
  transient and permanent split taken from the error taxonomy rather than from
  string matching, and a dead letter queue with an operator readable reason.
- **HTTP API.** RFC 9457 problem details everywhere, including the rejections
  the router itself produces, a middleware chain in one place, and health and
  readiness probes that tell a degraded dependency apart from a missing one.
- **Operations console.** React and TypeScript: live pipeline numbers over
  server sent events with a polling fallback, a sparkline of the outbox depth,
  optimistic order creation that rolls back on a refusal, an order detail read
  by id, per panel error boundaries and zero accessibility violations.
- **Observability.** Structured logs carrying request, correlation and trace
  identifiers across both processes, OpenTelemetry traces through the broker
  headers, Prometheus metrics with bounded label cardinality, a Grafana
  dashboard, and the runtime profiles behind configuration on a loopback
  listener.
- **Proof.** Unit, integration, fuzz, chaos and load tests, including two
  hundred concurrent buyers against five units, two publishers against one
  outbox, a blocked broker and a slow one, and a sustained throughput scenario.
- **Pipeline.** Hooks that reject a commit a reviewer should never see, a
  pull request pipeline with a paths filter, container images with software
  bills of material and signed build provenance, and nightly vulnerability
  scans and fuzz campaigns.
- **Documents.** Nine documents and eleven architecture decision records,
  including the decisions that went against the original design and why.

### Known limitations

These are deliberate and documented rather than pending:

- No authentication or authorisation. Adding it would be routine and would
  prove nothing the rest of the repository does not already prove.
- The outbox and the deduplication table grow without bound. A partition or a
  retention job is the answer, and neither is written.
- A crash between the idempotency claim and the business transaction leaves the
  key in progress until its time to live expires. The console shows those keys.
- One PostgreSQL primary is the write ceiling.

[Unreleased]: https://github.com/xrodriguezb/fulcrum/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/xrodriguezb/fulcrum/releases/tag/v0.1.0
