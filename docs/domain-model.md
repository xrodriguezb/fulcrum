# Domain model

## Values

Every value object validates in its constructor and is unusable in its zero
form, so a struct literal cannot produce an invalid value.

| Type | Rule |
|---|---|
| `SKU` | 3 to 32 characters, letters, digits and dashes, normalised to upper case, cannot start with a dash |
| `Quantity` | 1 to 1000 |
| `Money` | Non-negative integer minor units plus a three letter currency code |
| `OrderID`, `CustomerID` | Canonical UUID, parsed and normalised to lower case |

Amounts are integers because binary floating point cannot represent a cent.
Identifiers are parsed rather than generated, which keeps the domain
deterministic: generation is a port the application supplies.

The order context and the inventory context define their own `SKU` and
`Quantity`. They agree on the format today and may not tomorrow, and a shared
type would make a change in one context a change in both. Translation happens in
the application layer, where it is visible.

## The order aggregate

```
                 Confirm()                    Cancel()
    ┌─────────┐ ──────────▶ ┌───────────┐ ──────────▶ ┌───────────┐
    │ pending │             │ confirmed │             │ cancelled │
    └─────────┘             └───────────┘             └───────────┘
         │                                                  ▲
         └──────────────────── Cancel() ────────────────────┘
```

All fields are private. The only way to change an order is through a method that
checks the transition first, so an order cannot reach a state the system does not
model.

Two details carry weight:

**Lines are sorted by sku on construction.** Reserving in a fixed order is what
prevents two orders touching the same pair of items in opposite sequence from
deadlocking. Putting the sort in the constructor means no call site can forget
it.

**Confirming an already confirmed order returns `ErrAlreadyConfirmed`, not
`ErrInvalidTransition`.** Delivery is at-least-once, so a redelivered
`order.created` is ordinary traffic. The consumer treats the first as success and
the second as a protocol violation, and it can only do that if the aggregate
tells them apart.

`PullEvents` drains the recorded events, so one aggregate instance cannot emit
the same event twice.

## Inventory

`Item` states the reservation rule in code that can be read without a database:
a reservation of more units than are available is refused, and nothing changes.

The authoritative enforcement is the conditional UPDATE, because only the
database can arbitrate concurrent callers. The model and the statement say the
same thing on purpose, and the integration suite proves the statement.

## Events

`order.created` is versioned from the first release and carries its own line
shape rather than the value objects, so an internal refactor cannot change what
consumers receive. A golden file pins the serialised form: any change to it fails
a test whose message says to bump the version or revert.

The consumer rejects a version it does not know rather than ignoring it, so a
rollout that outruns its consumers appears in the dead letter queue within
seconds instead of silently doing nothing.
