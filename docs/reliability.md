# Reliability

What happens when each part fails, and what the system promises in return.

## The promises

1. An order that returns 201 has its inventory reserved and its event recorded.
   There is no window in which one exists without the other.
2. An accepted order's event is eventually published, for any broker outage
   shorter than the outbox's capacity to hold rows.
3. Every event produces exactly one side effect, however many times it is
   delivered.
4. An event that cannot be processed ends in the dead letter queue with enough
   context to act, rather than being retried forever or dropped.

## Failure by failure

| What fails | What happens | What the operator sees |
|---|---|---|
| A concurrent buyer takes the last unit | The reservation matches zero rows, the caller gets 409 `INVENTORY_INSUFFICIENT` | Nothing. This is normal traffic. |
| The client retries a request | The idempotency key replays the stored response, byte for byte | `idempotency_hits_total` and `Idempotent-Replay: true` |
| Two identical requests race | One creates the order, the other replays it or receives 409 with `Retry-After` | Nothing |
| The business transaction fails | Everything rolls back, the key is released, the client can retry | An error log with the cause and the trace id |
| The API crashes between the claim and the transaction | The key stays in progress until its TTL | `stuck_idempotency_keys` in the console |
| The broker is down | Orders keep being accepted, the outbox grows, publication retries with jittered backoff | `outbox_pending_total` and `outbox_oldest_unpublished_seconds` rising |
| The broker returns | The backlog drains, no event is published twice from one row | Both numbers falling to zero |
| A publisher dies mid publish | The lease expires and another instance takes the row | A brief rise in `outbox_oldest_unpublished_seconds` |
| An event is delivered twice | The deduplication row makes the second delivery a no-op | `consumer_processed_total` counts both |
| An event payload is malformed | Dead lettered on the first delivery, never retried | `dead_letter_total` and the console entry |
| The projection is briefly unavailable | Retried with full jitter up to the attempt budget, then dead lettered | `consumer_failed_total{reason_class}` |
| The database is unreachable | The API reports not ready, the worker keeps running and retries | `readyz` failing, error logs |
| A deployment interrupts processing | The event returns to the broker and is redelivered | Nothing |

## The three mechanisms

**The transactional outbox.** The event is written in the transaction that
reserves inventory and persists the order, so neither can exist without the
other. There is no ordering of two writes to two systems that survives a crash
between them, which is the failure this pattern removes. See ADR 0004.

**At-least-once delivery with an idempotent consumer.** The consumer inserts a
row keyed by `(consumer_name, event_id)` in the same transaction as its side
effect. A redelivery finds the key and does nothing. Nothing in this system
claims exactly-once, because nothing in this system can provide it. See ADR 0007.

**Bounded retry with full jitter.** Transient failures are retried with a delay
drawn from the whole backoff window rather than a band around it, so a broker
coming back up does not receive the entire backlog in one burst. Permanent
failures are not retried at all: a malformed payload fails the same way five
times, so it goes straight to the dead letter queue.

Transient and permanent are distinguished by the error taxonomy, never by
matching on driver error text.

## Demonstrated, not described

`make demo` kills the broker, creates twenty orders, shows the API still
answering 201 with the outbox holding twenty events, restores the broker, and
shows the backlog draining to zero with a maximum of one processing per event.
Then it publishes a payload that is not an envelope and shows it landing in the
dead letter queue. The same scenario runs in CI on every pull request through
`scripts/e2e.sh`.

## What is not handled

- **A partial outage of the database mid transaction** leaves the transaction
  rolled back, which is correct, but a sustained outage makes the API unavailable
  for writes. There is no write-side queue in front of PostgreSQL by design.
- **A poisoned consumer that dead letters everything** is visible in the console
  and in `dead_letter_total`, but nothing stops it automatically. A circuit
  breaker was considered and left out: at this volume an operator reading the
  queue is faster than a heuristic.
- **Outbox and deduplication tables grow without bound.** Both are listed as known
  limitations in the README with the concrete next step.
