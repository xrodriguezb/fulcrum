# 0005. Idempotency through a two transaction claim

Status: accepted
Date: 2026-09-21

## Context

`POST /api/v1/orders` reserves inventory. A client that retries after a timeout
must not reserve twice, and a client that never learns the outcome must be able
to ask again and receive the original answer. The header is `Idempotency-Key`.

The subtle part is not storing the key. It is what two concurrent requests
carrying the same key do to each other.

## Decision

Two transactions, in this order.

**Transaction A, the claim.** `INSERT ... ON CONFLICT (key) DO UPDATE ... WHERE
existing.expires_at <= now() OR existing.status = 'failed'`, committing
immediately. Winning the insert means this request owns the work. Losing it means
a record already exists and the request is a duplicate.

**Transaction B, the business transaction.** Reserve inventory, persist the
order, write the outbox event, and update the idempotency row to `completed` with
the response status and body. All of it commits together, so a stored response
can never describe work that did not happen.

**On failure of transaction B**, the key is set to `failed` in its own short
transaction, on a context that cancellation cannot reach, so a client that
disconnected mid-request does not leave its key unusable until the TTL expires.

The stored response body is `text`, not `jsonb`. jsonb normalises whitespace and
reorders object keys, so a replay would not return the bytes the first caller
received, and idempotent replay is a promise about the response.

The fingerprint is a SHA-256 over a canonical encoding of the request body:
object keys sorted, insignificant whitespace removed, numbers kept in their
literal form. Array order is preserved, because element order is part of what a
document says and sorting it would make a reordered request look like a replay.

## Alternatives considered

**One transaction: claim and business work together.** This is the obvious design
and it does not work. Two concurrent duplicates both begin. Neither sees the
other's uncommitted row, because that is what isolation means. The loser blocks
on the primary key until the winner commits, and it blocks after having already
done work of its own, including reserving inventory. When the winner commits, the
loser's insert fails and its work rolls back, which is correct but wasteful, and
under load the blocked transactions hold locks on inventory rows that other
orders need. The two transaction protocol moves the contention to a single row
with no business work attached to it.

**A unique constraint on a business key instead of a client supplied key.**
Attractive because it removes a header from the contract. Rejected because the
system cannot know which requests a client considers the same. Two genuinely
different orders from one customer for the same SKU within a second are legal,
and a business key would merge them.

**A distributed lock, in Redis or with an advisory lock.** Rejected. It adds a
component or a second failure mode to solve a problem the primary key already
solves, and it does not store the response, so a duplicate that arrives after the
first request finished has nothing to replay.

**Returning 200 with the stored order rather than 409 while in progress.**
Rejected. While the first request is still running there is no stored response to
return, and inventing one would mean answering before the work committed. 409
with `Retry-After` states the truth: ask again shortly.

**Deleting the key on failure instead of marking it failed.** Equivalent in
effect and slightly simpler. Marking it was chosen because the row is evidence:
an operator looking at a failed attempt can see that it happened and why, and the
sweep removes it at expiry anyway.

## Consequences

- A crash between transaction A and transaction B leaves the key `in_progress`
  until the TTL expires. Retries during that window receive 409. This is the
  price of the split and it is real. The operations console surfaces stuck keys
  through `CountInProgressOlderThan`, so the condition is visible rather than
  silent, and the API returns `Retry-After` so a client knows what to do.
- Keys expire after 24 hours by default, swept by the worker with an index on
  `expires_at`.
- Reusing a key with a different body returns 422 rather than the original
  response. Replaying the first answer would hide a client bug behind a success.
- Every row of the behaviour matrix has an integration test, including sixteen
  concurrent identical requests, which must leave exactly one order.
