# 0006. Error contract

Status: accepted
Date: 2026-09-21

## Context

Errors cross four boundaries in this system: a domain invariant fails, a use case
turns that into an outcome, an adapter fails for infrastructure reasons, and a
transport has to answer a caller. Each boundary has a different audience. The
domain speaks to the code, the use case speaks to the caller, the adapter speaks
to an operator, and the transport speaks to a client that may be hostile.

Two failure modes are common enough to design against. The first is a switch on
error values that lives in one handler, is copied into a second, and drifts. The
second is an infrastructure error whose text reaches a client, carrying a
hostname, a query, a role name or a path.

## Decision

A single taxonomy in `internal/platform/errs`, with three parts:

- **Kind** decides both the HTTP status and whether a retry could help. The set is
  closed, and a test walks every kind to assert it maps to a valid status, so a
  new kind cannot be added without deciding what it means on the wire.
- **Code** is the stable identifier a client can branch on. Codes are part of the
  public contract and appear in the OpenAPI document.
- **Message** is the sentence a caller may read. Everything else, including the
  wrapped cause, is for logs.

`errs.PublicMessage` collapses anything unclassified, and anything classified as
internal, to one generic sentence. That is deliberate: those are exactly the
errors whose text was written for an operator.

One function, `httpx.WriteProblem`, turns an error into an RFC 9457 problem
document. It is the only place in the codebase where an error becomes a status
code. It also decides what gets logged: at 500 and above the full error text with
its cause, below that a single line with the code and the kind.

Unclassified errors default to internal and to permanent. An error nobody reasoned
about must not be reported as the client's fault, and it must not be retried in a
storm.

## Alternatives considered

**Sentinel errors checked with `errors.Is` at each handler.** Rejected. It works,
and it spreads the mapping across every handler, which is the drift this decision
exists to prevent. It also couples transport to the specific sentinels a use case
happens to return today.

**HTTP status codes carried on the error itself.** Rejected. It makes the domain
and the application layer speak HTTP, so a use case called from the worker, from a
test or from a future gRPC surface carries a status that means nothing there.

**A single error type with a string code and no kind.** Tempting, because the code
already implies the status. Rejected because retry classification would then have
to be derived from the code by a second mapping, and the two would disagree the
first time a code was added.

**Returning the underlying error text in development mode.** Rejected. A behaviour
that only appears when a flag is set is a behaviour nobody tests, and the flag is
one misconfiguration away from production. The trace id in the problem document
solves the same problem without the risk.

## Consequences

- Adding a status means adding a kind, which the mapping test forces someone to
  decide about.
- The leak test is the strongest assertion in the transport suite: it takes an
  infrastructure error containing a hostname, a role and a path, and asserts none
  of those substrings appear in the response.
- Every problem document carries the trace id, so an operator can join a customer
  report to a log line without asking the customer for anything else.
- The consumer retry policy reads `errs.IsTransient` rather than matching on
  driver error text, which is what makes a malformed payload fail once instead of
  five times. See ADR 0007.
