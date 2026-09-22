# Security

A calibrated threat model for a system with no authentication, deployed as a
demonstration. The point is to be honest about what is protected, what is not,
and why.

## Assets

| Asset | Why it matters |
|---|---|
| Inventory integrity | Overselling is the failure this system exists to prevent. It costs money and customer trust, and it is silent. |
| Order data | Customer identifiers and purchase history. Not payment data: there is none. |
| Service availability | An unavailable order intake is lost revenue for as long as it lasts. |

## Threats and controls

### Inventory integrity

| Threat | Control | Residual risk |
|---|---|---|
| Concurrent buyers oversell a sku | Conditional UPDATE, atomic guard and write, proven under 200 concurrent buyers | None known at this scale |
| A bug or a manual write drives stock negative | `CHECK (available >= 0)` and `CHECK (reserved >= 0)`, tested by a deliberate violation | A superuser can drop the constraint |
| A client sends its own price | Prices come from the inventory table; the contract has no price field on a line | None |
| Replay of a create request reserves twice | Idempotency key, fingerprinted request body, two transaction claim | A crash leaves a key in progress until its TTL |

### Order data

| Threat | Control | Residual risk |
|---|---|---|
| Injection through order input | Every statement is parameterised; there is no string concatenation anywhere in the SQL | None known |
| Schema tampering from the request path | The application role can read and write rows and cannot create, alter or drop anything; migrations use a separate connection | The migration credentials exist in the environment |
| Data exposure through error messages | Problem documents carry a stable code and a safe sentence; the cause goes to the log only, asserted by tests | None known |
| Enumeration of orders | Not mitigated. There is no authentication, so anyone who can reach the API can list orders | Accepted: authentication is an explicit non-goal |

### Service availability

| Threat | Control | Residual risk |
|---|---|---|
| Oversized request bodies | `MaxBytesReader` plus a declared length check, 413 with a stable code | None known |
| Slow client holding connections | Every `http.Server` timeout is set explicitly | A distributed flood is not mitigated |
| Unbounded work from one request | Line count, quantity and page size are all bounded | None known |
| A broker outage stopping order intake | The outbox decouples acceptance from publication, demonstrated by killing the broker | The outbox grows without bound during a long outage |
| Resource exhaustion from concurrency | Fixed publisher pool, bounded channel, bounded connection pool | None known |

## Container and supply chain

- Go services run on distroless static images as uid 65532, with a read-only root
  filesystem, no new privileges and all capabilities dropped. The console runs as
  the unprivileged nginx user.
- Only the API and the console publish ports. The database and the broker are
  reachable only inside the compose network.
- No secrets in the repository. `.env.example` carries placeholders, and
  `gitleaks` runs in the pre-commit hook and in the pipeline.
- Dependencies are scanned by `govulncheck`, `gosec` through golangci-lint,
  `npm audit` and `trivy`, on every pull request.
- Published images are rescanned nightly. That check found 35 high and 2 critical
  advisories in the web image, inherited from a stale base, weeks of pull request
  pipelines having passed. It was fixed the same day.

## Headers and origins

The API sets `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` and a
`Content-Security-Policy` of `default-src 'none'`. The console adds a policy
scoped to its own origin. CORS is an explicit allowlist, never a wildcard, and
the configuration refuses to start in production without one.

## What is not mitigated, deliberately

- **No authentication or authorisation.** Anyone who can reach the API can create
  and read orders. This is the first thing that would be added for real use, and
  it is listed as a non-goal rather than hidden.
- **No rate limiting.** A single client can exhaust the connection pool. In a real
  deployment this belongs at the edge rather than in the application.
- **No encryption in transit inside the stack.** Compose traffic is plaintext on a
  private network. TLS termination belongs at the ingress.
- **No audit log.** Order history is inferable from the outbox, which is not the
  same thing as an audit trail.
- **No secret management.** Credentials come from the environment. A real
  deployment would read them from a secret manager, and the configuration layer
  is the only place that would change.

None of these are oversights. Each one is a scope decision, and each one has a
known cost.
