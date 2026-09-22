# 0009. Event versioning

Status: accepted
Date: 2026-09-21

## Context

An event is a contract with code that has already been written and deployed by
someone else. Unlike an API response, it cannot be negotiated per request, and
unlike a database row, it cannot be migrated: copies of it are already in a
broker, in a log, and possibly in another team's database.

The question is not whether the shape will change. It is what happens on the day
it does.

## Decision

Every envelope carries `version`, an integer, from the first release. Version 1
exists even though there has never been a version 0, because a field added later
is a field consumers must treat as optional forever.

The shape is pinned by a golden file test. `MarshalWire` is compared against
`testdata/order_created_v1.json`, byte for byte. Any change to the published
shape fails that test, and the failure message says what to do: bump the version
and update the golden file, or revert the change.

The rules for change are:

- **Adding an optional field is not a version bump.** A consumer that ignores
  unknown fields is unaffected, and requiring a bump for every addition makes
  version numbers meaningless.
- **Removing a field, renaming one, changing its type, or changing the meaning of
  an existing value is a version bump.** Those are the changes that break a
  consumer silently.
- **A new version does not replace the old one.** Both are published until every
  consumer has moved, which is visible because consumers declare the versions they
  handle and reject the rest.

The consumer enforces its side: `ConfirmOrderHandler` rejects any version other
than 1 with a permanent error, so an unsupported version lands in the dead letter
queue with the reason stated rather than being silently ignored.

## Alternatives considered

**No version field, rely on additive changes only.** Rejected. It works until the
first change that cannot be additive, at which point there is no mechanism and
the discussion happens during an incident.

**A schema registry, with Avro or Protobuf.** The right answer at a larger scale:
it makes compatibility a build time check rather than a review time convention.
Rejected here on weight. It adds a service, a build step and a serialization
format to a system with one event type, and the compatibility rules it enforces
are the ones written above, which currently fit in a paragraph.

**Semantic versioning of the event, such as 1.2.0.** Rejected. The only
distinction that matters to a consumer is "can I still read this". A minor
version communicates something no consumer can act on, and it invites arguing
about whether a change is minor.

**Versioning by subject, publishing to `order.created.v2`.** A real alternative,
and it has the advantage that a consumer subscribes to what it understands and
never sees anything else. Rejected because it multiplies subjects and makes a
consumer that wants both versions subscribe twice. The version is in the envelope
where a consumer can read it before deciding.

**Tolerant readers with no explicit rejection.** Rejected for this system. A
consumer that silently ignores an unknown version produces no error and no effect,
which is the hardest failure to notice. Rejecting loudly puts it in the dead letter
queue where it is counted and visible.

## Consequences

- The golden file is a gate, not documentation. Changing the envelope means an
  explicit decision by whoever changes it.
- `data` never mirrors a database row. The event carries an `EventLine` shape of
  its own, so renaming a column cannot change what consumers receive.
- Supporting a second version means a second handler branch and a second golden
  file, not an edit to the first.
- An unsupported version is a dead letter with a stated reason, so a rollout that
  outruns its consumers is visible in the operations console within seconds.
