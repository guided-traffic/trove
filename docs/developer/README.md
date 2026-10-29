# Developer documentation

Status: the foundation milestone (M0) is built. The repository holds the Go binary, its configuration
and database and cache layers, the S3 blob layer, the system API, the ops endpoints, the Angular
workspace with a placeholder page, the Helm chart skeleton, the local development profile on kind, the
integration and chart tests, and the CI workflows. No package format, login, repository or backup exists
yet. Everything marked *planned* below is the target design in [docs/spec/](../spec/README.md).

## What to have in your head first

- **Four parts, one direction.** The [feature catalog](../features/README.md) says *what* a full-featured
  repository manager does. The [specification](../spec/README.md) says *how* trove does it, one
  requirement at a time (`R-<AREA>-nnn`). [Coverage](../coverage/README.md) measures the gap, and
  [decisions.md](../decisions.md) records *why*.
- **Owner constraints are fixed.** Go 1.27.3, Angular 22, PostgreSQL, S3-compatible storage, Helm, OIDC
  login, own tokens, detailed permissions, GitLab CI OIDC, pull-through proxying, npm and NuGet first, and
  the high-availability and scale constraints of 2026-10-28 ([spec/README.md](../spec/README.md#owner-constraints)).
- **Invariants `INV-1` to `INV-19` bind every spec page.** A page may refine an invariant but must not
  contradict it ([spec/README.md](../spec/README.md#design-invariants)).
- **Roles, one binary** ([R-ARCH-001](../spec/architecture.md)). `web` answers HTTP and never claims a job or
  holds a lease. `worker` claims jobs, holds leases and serves only the ops listener. `all` combines both
  for a local run outside the chart. The `backup` role is planned.
- **PostgreSQL decides, Valkey accelerates.** State whose loss would corrupt data lives in PostgreSQL,
  including every mutual exclusion whose failure would corrupt data. Valkey holds shared ephemeral state and
  never the only copy of anything. Only `internal/cluster` talks to Valkey, and every key expires.
- **No state between requests** ([R-ARCH-032](../spec/architecture.md)). Consecutive requests of one client
  may land on different replicas.
- **Tools read the headings.** `### <ID> · <Title>` followed directly by a `Priority:` line is how the coverage
  generator finds catalog entries and requirements. Keep both on one line each.
- **One version per tool.** Pins live in [tools/versions.env](../../tools/versions.env) (tools and images),
  `go.mod` (Go, sqlc, oapi-codegen), `web/package-lock.json` (Angular) and `web/.nvmrc` (Node.js). `make
  versions-check` compares the installed tools with the pins.

## Pages

| Page | Content |
|---|---|
| [architecture.md](architecture.md) | Components, layers, request path, startup and shutdown, coordination, configuration and secrets |
| [build-and-test.md](build-and-test.md) | Build, generate, lint and test targets, and the test tiers and their build tags |
| [releasing.md](releasing.md) | The CI jobs and their make targets, semantic-release and the release pipeline (Docker Hub and the Helm chart repository), the Renovate policy, the secrets and settings the pipeline needs |
| [configuration.md](configuration.md) | How `trove.yaml`, environment variables and flags combine, and how a key is added |
| [coverage.md](coverage.md) | The requirement coverage generator, its rules and its evidence record |
| [database.md](database.md) | PostgreSQL connections, pools, migrations, sqlc and the job and change tables |
| [blob-store.md](blob-store.md) | The S3 blob layer: keys, spool, commit, download, garbage collection |
| [coordination.md](coordination.md) | Change log, signals, leases, the job queue and the replica registry |
| [valkey.md](valkey.md) | The Valkey client, Sentinel discovery, key families, degraded mode |
| [observability.md](observability.md) | Logs, metrics, traces, health and the ops listener |
| [secrets.md](secrets.md) | The stored-secret key ring and the envelope of sealed columns |
| [tls.md](tls.md) | TLS material on every listener and client: loading, reloading, rotating, testing |
| [endpoints.md](endpoints.md) | Endpoints: listeners, identity, hosts, surfaces, TLS per endpoint, the chart rendering, accepted credentials, exposure rules and their ceiling, the endpoint rate-limit class, the endpoint API |
| [http-api.md](http-api.md) | Listeners, the middleware chain, the system API and the problem format |
| [helm-chart.md](helm-chart.md) | The chart: values, rendered objects, validation and the chart tests |
| [local-profile.md](local-profile.md) | The HA-shaped dev profile on kind, its scripts and its pins |
| [e2e.md](e2e.md) | The end-to-end suite on the dev cluster: harness, driver, k6 load, tests and open findings |
| [audit.md](audit.md) | Audit trail: registry, recorder, journal row, query, partitions, how to add an event type |
| [repositories.md](repositories.md) | The repository model: service, snapshot and unknown-key lookup, validation, patterns, deletion job, the format registry and the generic format |
| [login.md](login.md) | The OIDC login, the server-side session, CSRF, logout and the break-glass account: files, flows, seams to the identity, api and audit areas, configuration, tests |
| [identity.md](identity.md) | Principals, groups, service accounts, tokens, the authentication stage, caches and revocation propagation, the failed-credential throttle |
| [authorization.md](authorization.md) | Permission policies, roles, the evaluation, the compiled grant index and its invalidation, epochs and `Trove-Authz-Min-Epoch`, how an operation declares its permission |
| [files.md](files.md) | Raw transfer under `/files/`: deploy, download with Range, folders, the items API, deletes and the `items-delete` job, activity and download statistics |
| [m1-contracts.md](m1-contracts.md) | The M1 package map, shared Go contracts, marked sections in shared files, error codes, Valkey and audit names, API path prefixes per work package |
| [web-ui.md](web-ui.md) | The Angular workspace, the embedded build, the budget and the UI tests |

## Core flows

One fact per line; each links to the page that explains it.

- **Start-up:** `trove serve` loads the configuration, builds the components of its role and waits for the
  PostgreSQL schema check, then becomes ready ([architecture.md](architecture.md)).
- **Configuration:** defaults, then the file, then the environment, then flags; strict validation; the
  effective view is printed by `trove admin config validate` ([configuration.md](configuration.md)).
- **Requests:** the middleware chain assigns `X-Request-Id`, applies the client address and the body limits,
  then the handler; every error is written as `application/problem+json` ([http-api.md](http-api.md)).
- **Migrations:** `trove migrate up` holds the migration advisory lock and applies each step in its own
  transaction; a replica waits, not ready, until the schema is current ([database.md](database.md)).
- **Cross-replica change:** every replica polls the change log; a Valkey message only evicts early
  ([coordination.md](coordination.md)).
- **Singleton work:** a worker takes a session-level advisory lease on its lease pool and fences its writes
  to that session ([coordination.md](coordination.md)).
- **Jobs:** a worker claims rows with `FOR UPDATE SKIP LOCKED`, extends its lease with a heartbeat, and settles
  the attempt with the worker and attempt as owner ([coordination.md](coordination.md)).
- **Valkey:** keys come from registered families with a mandatory expiry; the client follows the primary that
  Sentinel reports ([valkey.md](valkey.md)).
- **Blobs:** content-addressed keys under `<prefix>/sha256/…`; the S3 client sits behind an outbound factory
  ([blob-store.md](blob-store.md)).
- **Health:** readiness checks, watched loops and the liveness watchdog ([observability.md](observability.md)).

## Repository layout

```text
trove/
├── Makefile                  # the task runner: build, generate, lint, scans, test, web, chart, image, dev profile, integration, MinIO
├── go.mod                    # go 1.27.3; sqlc and oapi-codegen as tool directives
├── tools/versions.env        # pins of every tool and image that is not a Go module or an npm package
├── Containerfile             # the release image: web build, cross-compiled Go build, distroless final stage
├── package.json              # release tooling only (semantic-release); its lockfile is package-lock.json, web/ has its own
├── .releaserc.json           # semantic-release: conventional commits, releases from main
├── renovate.json             # Renovate: the Go, npm, GitHub Actions, Docker and tools/versions.env managers
├── cmd/trove/                # main.go (dispatch), serve.go, migrate.go, admin.go (dispatch), admin_login.go and admin_identity.go
│                             # (the recovery and bootstrap commands of R-AUTH-015)
├── api/openapi.yaml          # OpenAPI 3.1 source of truth; go generate copies it into internal/api/v1
├── internal/
│   ├── app/                  # composition root: components per role, start and stop order
│   ├── api/rawfiles/         # the /files/ handler (edge of internal/files)
│   ├── api/v1/               # management API handlers and the conventions (paging, ETag, merge patch, idempotency, CORS,
│   │                         # rate limits); gen/ is generated (oapi-codegen); apitest/ validates responses against the document
│   ├── archtest/             # layer, import, state and source rules, tested on fixtures in testdata/
│   ├── audit/                # event registry (journal flag), recorder (transactional and async), store and query
│   ├── auth/                 # the authentication stage and the failed-credential throttle; principal/ (users, groups,
│   │                         # service accounts, provisioner), token/ (format, store, scopes, lifetime, revocation), session/ (UI sessions, CSRF, the
│   │                         # session cache), oidc/ (providers, login flow, callback, break-glass)
│   ├── authz/                # decision point: engine, compiled grant index, epochs, policy and role service, decision audit
│   ├── backup/tables/        # table registry: the export class of every table (leaf layer)
│   ├── blob/                 # store, spool, digests, commit, download, GC; s3/ is the S3 client; blobtest/ fixtures
│   ├── buildinfo/            # version, commit and build date from -ldflags
│   ├── cache/                # bounded in-process caches with change-area invalidation
│   ├── cluster/              # the only Valkey client and key registry (valkey_keys.go), change log, signals,
│   │                         # FlightLock, leases, lock categories, replica registry, watchdog loops, the cluster limiter
│   ├── config/               # struct tree with defaults, loader, validation, schema.json (generated)
│   ├── errs/                 # error kinds, the code registry (codes.go), problem rendering
│   ├── httpserver/           # listeners, routes, middleware chain, client address, ops endpoints
│   ├── jobs/                 # job kinds, tasks, the queue, workers, scheduler, reaper
│   ├── meta/                 # transactions and the revision-column registry
│   ├── meta/postgres/        # pools, connect, retry, migrations/ (embedded), queries/ (sqlc input), q/ (sqlc output),
│   │                         # listq/ (dynamic queries with bound values)
│   ├── observability/        # logs, metrics, tracing and OTLP export, health checks
│   ├── outbound/             # the one HTTP client factory and the egress address policy
│   ├── repository/           # repository model: service, snapshot reader, validation, patterns, deletion job
│   ├── files/                # raw transfer service: deploy, download, folders, items, deletes, statistics
│   ├── formats/              # format contract and registry; generic/ (raw files), all/ registers every format
│   ├── secrets/              # key ring and sealed-column registry
│   ├── tools/coverage/       # the requirement coverage generator (docs/coverage)
│   └── webui/                # go:embed of the Angular build; dist/index.html is the committed placeholder
├── web/                      # Angular workspace: angular.json, src/, scripts/ (build-embed, check-budget), .nvmrc
├── deploy/
│   ├── helm/trove/           # the chart: templates/, values.yaml, values.schema.json, ci/ value sets
│   └── dev/                  # the local profile: kind, HAProxy Ingress, cert-manager, CloudNativePG, valkey-operator, internal-secrets-operator, SeaweedFS, scripts
├── test/
│   ├── integration/          # build tag `integration`: area folders blob/, coordination/, postgres/, valkey/, server/
│   ├── chart/                # build tag `chart`: render assertions and the lint script
│   └── e2e/                  # build tag `e2e`: scenarios against the dev cluster, with a k6 load script
├── .github/
│   ├── workflows/            # release.yml (Test and Release), build.yml (Release Docker & Helm), renovate.yml
│   ├── actionlint.yaml       # actionlint's configuration (the App token inputs of the pinned version)
│   ├── scripts/              # s3-conformance.sh, run by make s3-conformance
│   └── actions/load-pins/    # loads tools/versions.env into the job
├── hack/                     # verify-release-tooling.mjs: the release notes of .releaserc.json rendered
├── test/coverage/            # report.sh: merges the unit and integration profiles into the badge
└── docs/                     # README.md, decisions.md, developer/, spec/, security/, coverage/, features/
```

M1 contract skeletons ([m1-contracts.md](m1-contracts.md)): `internal/audit/` (event registry, recorder interface),
`internal/auth/` with `principal/`, `token/`, `session/` and `oidc/`, `internal/authz/` and `internal/repository/`
hold the shared types and interfaces; their implementations arrive with the M1 work packages. The `Principal` the
chain stores is in [internal/httpserver/principal.go](../../internal/httpserver/principal.go).

Planned, from [R-ARCH-003](../spec/architecture.md): the package formats (`internal/formats/npm`, `nuget`) and the
remote fetch engine (`internal/proxy/`) do not exist yet.

## Test tiers

| Tier | How it runs | Where |
|---|---|---|
| Unit | `make test` (`go test -race`, JSON to `.out/test-unit.json`) | next to the code; `_test.go` |
| Architecture | part of unit: `go test ./internal/archtest` | `internal/archtest/` |
| Integration | `make integration-up`, then `make test-integration`; build tag `integration`; `-p 1`; S3 conformance against MinIO: `make minio-up`, `make s3-conformance` | `test/integration/<area>/` |
| Chart | `make chart-lint`, `make chart-test`; build tag `chart` | `test/chart/` |
| End-to-end | `make e2e` against the dev cluster; build tag `e2e` | `test/e2e/` |
| Web unit | Angular's `unit-test` builder with Vitest and jsdom | `web/` |

The coverage evidence (`make coverage-refresh`) reads the unit, chart and integration reports; the rules
are in [coverage.md](coverage.md).

## ID schemes

| ID | Where it lives | Rule |
|---|---|---|
| `<PREFIX>-nnn` | [docs/features/](../features/README.md), one prefix per page (page table there) | Three digits; `### <ID> · <Title>`, then `Priority:` |
| `FMT-nnn` | [features/formats/README.md](../features/formats/README.md) | Cross-format behavior |
| `PKG-<FORMAT>-nnn` | [docs/features/formats/](../features/formats/README.md), one prefix per format page | The prefix is a short form shown by the page's first entry, e.g. `alpine.md` uses `PKG-APK`, `rubygems.md` uses `PKG-GEMS` |
| Priority | Every catalog entry and requirement | `MVP`, `P1`, `P2`, `Later`, `Out of scope`; rubric in [features/README.md](../features/README.md#priority-rubric) |
| `INV-n` | [spec/README.md](../spec/README.md#design-invariants) | Design invariants |
| `R-<AREA>-nnn` | [docs/spec/](../spec/README.md), one prefix per page (page table there) | `### R-<AREA>-nnn · <Title>`, then `Priority: **<P>** · Covers: <catalog IDs>` or `Covers: trove-only` |
| `Q-<AREA>-n` | `## Decisions` of each spec page | Status line, options table, the development agent's choice, rationale with a revisit condition, and what the choice changed; format in [spec/README.md](../spec/README.md#requirement-format) |
| `H-n` | `## What this does not cover` of the page whose mechanism has the gap: a spec page today, a security perspective page once the mechanism is implemented | Unique across all spec and security pages; see [security/README.md](../security/README.md) |

Next free number for a prefix (examples for `RMT`, `R-REPO` and `H`):

```shell
grep -rhoE '^### RMT-[0-9]{3} · ' docs/features | sort | tail -n 1
grep -rhoE '^### R-REPO-[0-9]{3} · ' docs/spec | sort | tail -n 1
grep -rhoE '^### H-[0-9]+ · ' docs/spec docs/security | sed -E 's/^### H-([0-9]+).*/\1/' | sort -n | tail -n 1
```

## How to add something

The ordered steps for each kind of addition live on the topic pages, next to the code they describe:

- a configuration key: [configuration.md](configuration.md);
- a system API operation or an error code: [http-api.md](http-api.md) (the code registry is `internal/errs/codes.go`);
- a Valkey key family: [valkey.md](valkey.md);
- a job kind or a periodic task: [coordination.md](coordination.md);
- a metric family: [observability.md](observability.md);
- a Helm value or a rendered object: [helm-chart.md](helm-chart.md);
- a spec requirement or a catalog entry: the steps below.

For a spec requirement: take the next free number in the page's prefix, write the heading and the
`Priority:` line, state the normative text with MUST, SHOULD and MAY, and put any open question under
`## Decisions` in the block format of [spec/README.md](../spec/README.md#requirement-format). A security gap goes
under `## What this does not cover` as `H-<n>`, numbered across all spec and security pages. Then regenerate the
coverage checklist with `make coverage` and check the links with `go run ./internal/tools/coverage -links`.

## Conventions

- English only, in code, comments, commits and documentation.
- Documentation of a behaviour changes in the same change as the behaviour.
- Generated files are committed and carry the `DO NOT EDIT` header; `make generate-check` fails on a diff.
- No secret in a repository file, a ConfigMap or a log line; secrets are referenced as files.
