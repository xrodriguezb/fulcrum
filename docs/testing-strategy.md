# Testing strategy

## What is tested, and where

| Level | Count | Runs against | Answers |
|---|---|---|---|
| Go unit | 109 test functions | Nothing external | Does this rule hold |
| Go integration | 40 test functions | Real PostgreSQL and NATS through Testcontainers | Does the database do what the design assumes |
| Web | 15 tests | MSW at the network boundary | Can the operator do the thing |
| End to end | 1 script, 9 assertions | The compose stack, in CI | Does the assembled system behave |
| Load | 1 k6 scenario | The running stack | Does the claim hold under contention |

Go test code is roughly the same size as Go production code. That ratio is not a
target; it is what proving a concurrency claim costs.

## The rule that decides where a test goes

A test uses a real dependency when the behaviour under test is the dependency's
behaviour. Every claim this repository makes about reservation, claiming,
deduplication and rollback is a claim about what PostgreSQL does, so those tests
run against PostgreSQL. A fake would assert that the fake behaves as expected.

Everything else uses doubles, because a test that starts a container to check a
retry classification is a slow test with no extra information.

## The tests that carry the argument

**`TestReservationCannotOversell`** seeds five units, fires two hundred
concurrent reservations through a start barrier, and asserts five successes, one
hundred and ninety five conflicts and a final state of zero available and five
reserved. Under `-race`, on PostgreSQL 15 and 16.

**`TestIdempotencyMatrix`** covers every row of the behaviour matrix, and
`TestConcurrentDuplicatesCreateExactlyOneOrder` runs sixteen identical requests
on one key and asserts exactly one order exists afterwards.

**`TestTwoPublisherInstancesShareTheOutboxWithoutDuplicating`** runs two
publishers against one database and asserts no event reached the broker twice and
none was starved. This test found a real defect.

**`TestDuplicateDeliveryLeavesOneEffect`** publishes an event, lets the consumer
process it, replays the same event and asserts one deduplication row, one
confirmed order and no dead letter.

**The leak tests.** The problem writer test feeds an error containing a hostname,
a role name and a filesystem path and asserts none of them appear in the
response. The dead letter test does the same for the stored failure reason. Those
two assertions are what catch a leak, rather than describing one.

**The architecture test** walks the import graph. It was verified by temporarily
importing pgx into the order domain and watching it fail.

**The golden envelope test** pins the published event shape byte for byte, so
changing it requires a deliberate version bump.

## Test doubles that model the real thing

The consumer's fake transaction manager undoes the deduplication claim when the
function fails. A double that kept the claim would make a legitimate retry look
like a duplicate, which is exactly the bug the suite exists to catch rather than
to reproduce. A double that is easier than the real thing tests the double.

## What is deliberately not done

- **No coverage gate.** A percentage is satisfied by tests that execute code and
  assert nothing. Coverage is reported in the job summary because the trend is
  informative. The gate is whether new behaviour has a test that would fail
  without it.
- **No mocking of PostgreSQL.** See the rule above.
- **No snapshot tests of rendered markup.** They fail on every restyle and pass
  through every behaviour change. The console is tested through roles and labels,
  which is also what a screen reader uses.
- **No test of the framework.** There is no test asserting that TanStack Query
  caches, or that ServeMux routes.
- **No automated check of the responsive layout.** jsdom has no layout engine, so
  a test asserting behaviour at 375px would assert that a media query string
  exists, which is not the same claim. The layout is a single column by default
  with a two column breakpoint at 900px and tables that scroll horizontally in
  their own container, and it is verified by looking at it. Saying so is more
  honest than a test that would pass with the stylesheet deleted.

## Numbers from the last full run

- Go unit and race suites: pass, 17 packages.
- Go integration: 40 tests, pass under `-race` on PostgreSQL 16 locally and on 15
  and 16 in CI.
- Web: 15 tests, zero axe violations, statements 84 percent, lines 87 percent.
- Console production bundle: 267.77 kB, 82.41 kB gzipped.
- k6: 200 attempted, 5 created, 195 conflicted, 0 oversold, p95 22.6 ms locally
  and 175.1 ms on a shared CI runner.
