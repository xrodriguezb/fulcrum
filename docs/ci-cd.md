# Pipeline

The pipeline is a deliverable, not scaffolding. What follows is what runs, where
it runs, and why it runs there.

## The rule

The cheaper a check is, the earlier it runs. A developer should never learn from
CI something a hook could have told them in two seconds, and CI should never
learn from production something a test could have told it in two minutes.

## The five layers

| Layer | When | Budget | What runs |
|---|---|---|---|
| 0. Editor | on save | instant | `.editorconfig`, gofmt, goimports, eslint, prettier |
| 1. Pre-commit | every commit | under 10s | format check, vet, lint and secret scan on staged files, forbidden content scan, commitlint |
| 2. Pre-push | every push | under 90s | unit tests in both languages, race detector, architecture test |
| 3. Pull request | every PR | under 12 min | quality, contract, unit, integration matrix, web, security, images, compose end to end |
| 4. Main and nightly | merge, 03:17 daily | unbounded | image publish, SBOM per image, load test, published image scan |

Layer 1 runs only over staged files, so the cost of committing does not grow with
the repository. Layer 2 runs the whole unit suite, because that is the last point
where a failure is cheaper than a pipeline run.

## Local and CI parity

Every check has exactly one definition, and it lives in the `Makefile`. CI calls
`make fmt-check`, `make lint`, `make test-int`, `make e2e` rather than repeating
the commands inline. "It works on my machine" is not a thing anyone can say when
the machine and the pipeline run the same target.

The one deliberate exception is `make demo`, which is for a person to watch.

## The pull request graph

```
detect-changes
   |
   +-- quality        format, lint, secret scan, forbidden content, commitlint
   +-- contract       validate openapi, regenerate the typescript types, assert no diff
   +-- test-go-unit   go test -race, coverage artifact
   |      |
   |      +-- test-go-integration   matrix: postgres 15 and 16
   +-- test-web       tsc, eslint, vitest, axe, coverage artifact
   +-- security       govulncheck, gosec through golangci-lint, npm audit, trivy fs, hadolint
   +-- build-images   buildx with layer cache, image size report
          |
          +-- e2e-compose   primary flow, idempotent replay, broker kill and recovery
                 |
                 +-- pipeline-summary   one table, every gate
```

Decisions inside that graph worth stating:

**The integration matrix covers PostgreSQL 15 and 16.** Not for coverage
theatre. The reservation guarantee rests on the behaviour of a conditional
UPDATE and of `FOR UPDATE SKIP LOCKED`, and proving it on one version is proving
less than it appears to. If a version specific difference ever appears, it gets
documented rather than pinned around.

**Coverage is reported, never gated.** A coverage gate is satisfied by tests that
execute code without asserting anything, and it is the easiest gate in this
repository to satisfy dishonestly. The number is published in the job summary
because the trend is informative; the gate is whether the behaviour a change
introduces has a test that would fail without it.

**The paths filter exists so a documentation change is cheap.** A pull request
that touches only markdown does not build images or run the integration matrix.
The filter is expressed as "anything that is not documentation", so a new
directory is expensive by default rather than silently skipped.

**Every third party action is pinned to a commit SHA.** A tag is mutable. Pinning
to one means the pipeline runs whatever the tag points at today, which is a
supply chain decision made by someone else.

**Every job has a timeout.** A job that hangs holds the runner and the branch,
and the default is six hours.

**Least privilege on the token.** The workflow grants `contents: read` and jobs
elevate only where they must: publishing images asks for `packages: write` and
nothing else does.

## Branch protection

`main` is protected. Force pushes and deletions are refused, linear history is
required, and three checks must pass before a merge: `quality`, `contract` and
`pipeline summary`.

Those three and not the whole list, for a specific reason. The paths filter means
a documentation-only pull request skips the integration matrix, the web tests and
the image build, and GitHub treats a skipped required check as not satisfied. A
list naming every job would therefore block exactly the pull requests the filter
exists to make cheap.

`quality` and `contract` run on every change, so they can be required directly.
`pipeline summary` depends on every other job and fails when any of them failed,
which makes it the aggregate gate: a pull request that runs the integration
matrix cannot merge with it red, and a pull request that skips it is not
penalised for skipping.

## What is deliberately not automated

- **No automatic deployment.** There is no environment to deploy to, and a
  deployment job that deploys nowhere is decoration.
- **No automatic dependency merging.** Dependabot opens grouped pull requests
  weekly and a person merges them, because an unattended dependency merge is a
  supply chain decision made by a robot.
- **No performance regression gate.** The load test publishes numbers on main.
  Turning them into a gate needs a stable runner and a baseline, and a flaky
  performance gate teaches people to rerun jobs until they pass.
