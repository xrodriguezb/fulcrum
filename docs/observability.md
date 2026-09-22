# Observability

Three signals, kept proportionate to a system with one business capability.

## Structured logging

`log/slog`, JSON in production and text in development. Identifiers are attached
by the handler rather than by each call site, so code that forgets to pass them
is still correlated.

Three identifiers travel through every layer:

- `request_id` is per HTTP request. It is generated when the caller does not
  supply one, and a supplied value that is oversized or contains control
  characters is replaced rather than trusted, because it ends up in log output.
- `correlation_id` survives across processes. It is written into the outbox row,
  carried in the event envelope, and read back by the consumer.
- `trace_id` comes from the active span, so a log line and a trace can be joined.

What is never logged: request bodies, idempotency keys in full, and anything
resembling a credential. An idempotency key is chosen by the client and can
encode a customer identifier, so only a stable SHA-256 prefix of it is written.

## One order, followed end to end

This is a real capture. An order was created with a chosen correlation id, and a
single grep finds its whole journey across two services:

```
api    msg=request method=POST route=/api/v1/orders status=201 duration_ms=4
       request_id=36b61677a7ec14f66b64c159eb93cc89
       correlation_id=order-journey-1790045473
       trace_id=c1981f0730f51fde121435ca9a58094c

worker msg="event published" event_id=9c166567-063b-47e5-a631-68ce110db84e
       event_type=order.created aggregate_id=a9564f68-940e-408a-bc27-2acb2d41b223
       correlation_id=order-journey-1790045473
       trace_id=c1981f0730f51fde121435ca9a58094c

worker msg="event processed" event_id=9c166567-063b-47e5-a631-68ce110db84e
       event_type=order.created aggregate_id=a9564f68-940e-408a-bc27-2acb2d41b223
       correlation_id=order-journey-1790045473
       trace_id=c1981f0730f51fde121435ca9a58094c
```

Reproduce it with:

```
curl -X POST localhost:8080/api/v1/orders \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: journey-1' \
  -H 'X-Correlation-Id: my-journey' \
  -d '{"customer_id":"11111111-2222-4333-8444-555555555555",
       "lines":[{"sku":"WIDGET-001","quantity":1}]}'

docker compose -f deploy/compose.yaml logs api worker | grep my-journey
```

The trace id is identical across the three lines because the publisher injects a
W3C trace context into the broker message headers and the consumer extracts it.

## Tracing

OpenTelemetry, with spans at the four places this system crosses a boundary: the
HTTP handler, the business transaction, the publish, and the consume.
Instrumenting further would produce spans nobody reads.

The exporter writes to stdout in development. Pointing it at a collector is one
variable, `OTEL_EXPORTER=otlp` with `OTEL_EXPORTER_OTLP_ENDPOINT`. A four
container observability stack was not added, because at this size it costs more
to run and to read than the traces are worth.

Sampling is parent based with a configurable ratio, so a sampled caller keeps its
whole trace rather than losing half of it at a service boundary.

## Metrics

Prometheus exposition at `/metrics` on the API and, separately, on the worker.
The worker exposes its own because it owns the outbox and consumer numbers, and
routing them through the API would mean the API reporting on work it does not do.

```
http_requests_total{route,method,status}
http_request_duration_seconds{route,method}
http_requests_in_flight
outbox_pending_total
outbox_failing_total
outbox_oldest_unpublished_seconds
outbox_published_total{result}
outbox_publish_failures_total{exhausted}
outbox_claim_batch_size
consumer_processed_total{event_type}
consumer_failed_total{event_type,reason_class}
dead_letter_total{event_type}
```

Outbox depth is collected at scrape time rather than on a ticker. A ticker would
query the database when nobody is asking and still report a number that is one
interval stale.

## Cardinality

Every label is a closed set. The rule applied throughout: a label value must come
from a set the code enumerates, never from data.

What was deliberately rejected:

- **Order id, sku or customer id as labels.** Each one makes the series count
  grow with traffic, which turns a metric into a memory leak that only appears
  under load.
- **The request path as the `route` label.** The matched route pattern is used
  instead, so `/api/v1/orders/{id}` is one series rather than one per order. A
  test asserts an identifier never reaches a label.
- **The error message as a failure label.** `reason_class` is the error kind from
  the taxonomy, which is a closed set of ten values.
- **Idempotency key as a label on the idempotency metrics.** The outcome is
  labelled, the key is not.

`outbox_oldest_unpublished_seconds` is the single most useful number here: it
answers whether publication is keeping up, which neither a rate nor a count does.
