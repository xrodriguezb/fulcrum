# 0010. The shift-left pipeline

Status: accepted
Date: 2026-09-21

## Context

Checks can run in an editor, in a git hook, in a pull request pipeline, or after
a merge. The same check costs seconds in one place and tens of minutes in
another, and the difference is not compute: it is how long a developer waits
before learning something, and how much context they have lost by then.

The decision is which check runs where, and it has to be made once, because a
check that exists in two places drifts and a check that exists in none is a
convention.

## Decision

Five layers, ordered by feedback latency, documented in `docs/ci-cd.md`.

Two structural choices make it hold together.

**One definition per check.** Every check is a `make` target. CI calls the
target; a developer calls the same target. There is no second, subtly different
version of "formatted" or "linted" in a YAML file.

**The layer is chosen by cost, not by importance.** The forbidden content scan
runs in a hook because it takes forty milliseconds. The integration matrix runs
in a pull request because it needs containers. Nothing runs in a hook that cannot
finish in the time it takes to type a commit message.

## Alternatives considered

**Everything in CI, no hooks.** The simplest arrangement, and the one most
repositories have. Rejected because it makes the feedback loop a push and a wait
for checks a laptop can run in two seconds. It also makes the first green run of
a branch a matter of luck rather than of the code being ready.

**Everything in hooks, a thin pipeline.** Rejected in the other direction. Hooks
are bypassable, run on one machine with one toolchain, and cannot run a
container matrix. A pipeline that trusts hooks is a pipeline that trusts whoever
last ran `git commit --no-verify`.

**A coverage gate.** Rejected, and this is the one that usually causes an
argument. A percentage is satisfied by tests that execute code and assert
nothing, and it is the easiest gate in this repository to satisfy dishonestly.
The number is reported because the trend says something; the gate is whether new
behaviour has a test that would fail without it, which a human checks in review.

**An integration suite on one PostgreSQL version.** Rejected. The correctness
claim rests on database semantics, so proving it on one version proves less than
it appears to, and the second version costs one matrix entry.

**Mutable action tags.** Rejected. `@v4` is a pointer someone else can move, and
moving it changes what runs in this pipeline without a commit here.

**A performance gate on the load test.** Rejected for now, and the reason is
honesty rather than principle: a shared runner has no stable baseline, and a
flaky performance gate teaches people to rerun jobs until they pass, which
destroys the value of every other gate.

## Consequences

- A developer with hooks installed cannot commit an unformatted file, a secret,
  an emoji, an em dash or a non-conventional commit message. Those failures never
  reach a reviewer.
- The pull request pipeline is the slowest thing in the loop, so the paths filter
  matters: a documentation change skips the heavy jobs entirely.
- `make ci` runs what the pipeline runs, in the order the pipeline runs it, so a
  disagreement between local and CI is a bug rather than a fact of life.
- Hooks are bypassable with `--no-verify`, which is deliberate: they are a safety
  net for mistakes, not an authorisation mechanism. The pipeline is the
  authorisation mechanism, which is why the same checks run there.
