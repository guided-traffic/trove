# Roadmap

Status: plan. Nothing is implemented. Milestones carry no dates: a milestone is reached when every
exit criterion passes on `main` in CI. The calendar does not end a milestone.

This page orders the work from an empty repository to trove 1.0 (the MVP), then through P1, P2
and Later. It is built from these inputs:

- the requirements in [docs/spec/](../spec/README.md), each with a `Priority:` line and a
  `Covers:` line;
- the priority rubric of the [feature catalog](../features/README.md);
- the [format matrix](../features/formats/README.md);
- the [beyond-baseline candidates](../features/beyond-baseline.md).

Once M0's coverage generator exists, it publishes the status of each requirement as a generated
checklist in this directory ([README.md](README.md)). This page owns the order, the scope and the
exit criteria.

Reading the scope lists:

- `R-AUTH-001…018` means every requirement of that prefix from 001 to 018.
- Every MVP requirement appears in exactly one of M0 to M5. Every P1 requirement appears in exactly
  one of M6 to M11, or in the format lane.
- Exit criteria are tests. Where a spec requirement already states the test, the criterion names
  that requirement and does not repeat the test.

## Starting point

These numbers were measured over the catalog and spec pages as they stood when this page was
written. A throwaway script did the counting. It applied the parsing rules that M0's coverage
generator will own.

| Catalog priority | Catalog entries | Listed in a requirement's `Covers:` line | Not listed yet |
|---|---|---|---|
| MVP | 359 | 358 | 1 (FMT-013) |
| P1 | 508 | 285 | 223 |
| P2 | 909 | 13 | 896 |
| Later | 391 | 0 | 391 |
| Out of scope | 72 | 0 | 72 |
| **Total** | **2,239** | **656** | **1,583** |

The spec contains 483 requirements: 336 MVP, 146 P1 and 1 P2 (R-STOR-014). Every ID in a
`Covers:` line exists in the catalog. The spec pages carry 99 open questions and the security gaps
H-1 to H-45.

Five findings shaped this plan:

1. **Generic local repositories are P1, yet 16 MVP requirements test against one.** These
   requirements use a generic repository (`generic-local` or `files-internal`) in their acceptance
   criteria:
   R-STOR-001, R-STOR-004, R-STOR-007, R-STOR-012, R-API-012, R-API-025, R-UI-008, R-ARCH-005,
   R-ARCH-007, R-ARCH-024, R-AUTHZ-006, R-AUTH-012, R-AUTH-033, R-HA-010, R-HA-011 and R-HA-039.
   R-HA-052's load also includes `curl -T` of 200 MB files. npm and NuGet repositories refuse raw
   deploys (R-API-025), so these tests have no other target. [Q-ROAD-1](#q-road-1--generic-local-repositories-in-m1)
   moves the local class of the generic format into M1.
2. **The MVP failure suite runs a P1 procedure.** R-HA-052's restore drill executes the steps of
   R-HA-025. R-HA-025 (restore procedure) and R-HA-023 (backup) are both P1.
   [Q-ROAD-2](#q-road-2--backup-and-restore-in-10) moves both into M5.
3. **The npm acceptance matrix includes P1 operations.** R-NPM-045's scenario list covers
   deprecate (R-NPM-018), login (R-NPM-020, R-NPM-021), audit (R-NPM-038) and search (R-NPM-037),
   all P1. M2 gates only on the MVP scenarios. Until M6 lands, the P1 scenarios check for the
   predictable answers that R-NPM-043 specifies.
4. **One MVP catalog entry is not covered.** No `Covers:` line names FMT-013 (client
   authentication styles). The client authentication matrix of R-AUTH-033 addresses the npm and
   NuGet rows in substance, but nobody has compared the two line by line. In M1, either add FMT-013
   to R-AUTH-033's `Covers:` line or write the missing part.
5. **No P1 format has a spec page.** `docs/spec/formats/` contains only `npm.md` and `nuget.md`.
   Build metadata, search and upstream curation also have P1 catalog entries without requirements.
   Each one needs a spec page before implementation starts. See
   [Spec work before P1 implementation](#spec-work-before-p1-implementation).

## Milestones at a glance

| Milestone | Theme | Requirements | Can start when | Ends with |
|---|---|---|---|---|
| [M0](#m0--foundation) | Foundation: skeleton, pins, CI, PostgreSQL, S3, coordination, chart skeleton, coverage generator | 68 MVP | — | internal build |
| [M1](#m1--access-core) | Access core: OIDC login, tokens, permissions, repositories, raw file transfer, admin UI | 122 MVP, plus generic local (Q-ROAD-1) | M0 | internal build |
| [M2](#m2--npm) | npm with local, remote and virtual repositories; proxy and virtual engines | 75 MVP | M1 | pre-release 0.1 |
| [M3](#m3--nuget) | NuGet v3; freeze of the format contract | 24 MVP | M2's WP2.1 to WP2.3 | pre-release 0.2 |
| [M4](#m4--gitlab-ci-oidc) | GitLab CI OIDC token exchange | 26 MVP | M1 for the work, M3 for the exit | pre-release 0.3 |
| [M5](#m5--ha-hardening-and-helm-ga) | HA hardening and Helm GA | 21 MVP, plus R-HA-023 and R-HA-025 (Q-ROAD-2) | M3 and M4 | **1.0** |
| [M6 to M12](#p1-platform-milestones) | P1 platform | 144 P1 and 1 spec page to write | 1.0 | 1.x minors |
| [F1 to F7](#p1-format-expansion-order) | P1 formats | 2 P1 and 7 format spec pages to write | 1.0 | 1.x minors |

```mermaid
flowchart LR
  M0["M0 Foundation"] --> M1["M1 Access core"]
  M1 --> M2["M2 npm"]
  M2 --> M3["M3 NuGet"]
  M1 --> W4["M4 work: issuers, exchange, mappings"]
  W4 --> M4["M4 exit: reference pipeline"]
  M3 --> M4
  M2 --> H["HA harness: chaos and load loops"]
  H --> M5["M5 HA hardening, Helm GA = 1.0"]
  M4 --> M5
  M5 --> P1["M6 to M12 platform"]
  M5 --> F["F1 to F7 formats"]
```

## Why this order

- **M0 comes first because every later acceptance test runs on the chart.** R-NPM-045,
  R-NUGET-027 and R-CI-028 each require at least two replicas behind the Helm chart, with
  PostgreSQL and S3-compatible storage. These must exist before the first format can be accepted.
  HA is built in from M0 onward. M5 proves it under failure. Adding it after the fact would mean
  revisiting every request path.
- **Access (M1) comes before npm (M2).** Every package request resolves credentials (R-AUTH-001,
  R-AUTH-033) and goes through one decision point (R-AUTHZ-015). npm publish also depends on
  package ownership (R-AUTHZ-018) and token scopes (R-AUTH-024). Building npm first would mean
  building its authentication twice. Raw file transfer on a generic local repository is the smallest
  protected surface that proves login, tokens, permissions and the upload path together, and 16 MVP
  acceptance criteria already use it.
- **npm comes before NuGet.** npm puts the most strain on the generic engines:
  - four gated clients (R-NPM-045);
  - scoped names that make routing ambiguous (the reason given in Q-ARCH-1);
  - the widest dependency fan-out on a cold cache of the two MVP formats.

  So npm drives the proxy engine, virtual merging and miss collapsing. NuGet comes second because
  a second implementation is the real test of the format contract. Freezing the contract after one
  format would build npm's assumptions into it. M3 therefore ends with the contract freeze.
- **CI OIDC (M4) work starts after M1, but its exit waits for M3.** Issuer validation, the exchange
  endpoint, the mapping engine and claim templating depend only on tokens and authorization
  (R-AUTHZ-014), so they run in parallel with M2 and M3. The exit test (R-CI-028) publishes an npm
  package and a NuGet package from one pipeline.
- **M5 comes last.** The load loops of the failure-injection suite are `npm ci`, `dotnet restore`,
  `npm publish` and `curl -T` (R-HA-052), so the suite can gate only once all four exist. The GA
  values reference (R-HA-031) must also cover every MVP setting, CI issuers included. The chaos and
  load harness itself starts in M2 (WP2.8), so M5 tightens a running suite and does not build one
  from scratch.

Orders considered and rejected:

- *npm first, access later.* This gives a faster demo, but publish authentication, ownership and
  token formats would have to be built twice.
- *CI OIDC before any format.* The exit test would have nothing to publish.
- *HA hardening first.* Without load from real clients, the failure suite has nothing to measure.

Rules that hold across all milestones:

- Every end-to-end test runs on at least two replicas behind the chart, from M0 onward.
- Migrations follow expand/contract from the first one (INV-3). The pre-releases of M2 to M4 are
  real upgrade sources: M5 upgrades from the M4 pre-release (E5.4).
- A requirement counts as implemented when a test that names it passes on `main`
  ([Q-ROAD-4](#q-road-4--how-the-coverage-generator-learns-implementation-status)).
- Each tool and dependency is pinned once, and local builds and CI read the same pin.
- A change that alters documented behavior updates the documentation in the same change.

## Decision gates

These open questions must be decided before a milestone starts, because its first commits encode
the answer. Each question carries a recommendation on its own page. The recommendation is the
working default until the owner decides.

| Before | Open questions | What depends on the answer |
|---|---|---|
| M0 | Q-ARCH-1…5, Q-HA-2, Q-HA-3, Q-HA-8, Q-HA-11, Q-HA-12, Q-STOR-2, Q-STOR-4, Q-STOR-6, Q-OPS-7, Q-OPS-9, Q-API-1, Q-ROAD-4 | The `tool` pins in `go.mod`, the first migration (digest columns, replica table), the CI database and S3 matrix, the chart's secret wiring and the cron dialect. Q-HA-11 asks where images and the chart are published. The repository's `origin` remote points to GitHub, which makes the GitHub container registry the natural default. |
| M1 | Q-AUTH-1…7, Q-AUTHZ-3, Q-AUTHZ-5, Q-AUTHZ-9, Q-API-2, Q-API-3, Q-API-4, Q-API-6, Q-API-7, Q-UI-1, Q-UI-2, Q-UI-4, Q-UI-5, Q-OPS-3, Q-OPS-4, Q-STOR-9, Q-ROAD-1 | Session and token schema, the shape of token scopes, API conventions that every later resource inherits, the component library and the shell. Q-UI-2 and Q-UI-4 wait for the owner's example screenshots. |
| M2 | Q-NPM-2…8, Q-ARCH-6…9, Q-REPO-1…5, Q-AUTHZ-1, Q-AUTHZ-2, Q-AUTHZ-4, Q-UI-3, Q-UI-6, Q-OPS-10 | Proxy and virtual semantics, ownership defaults and the package page. Q-UI-3 waits for an example screenshot. Q-NPM-9 is already resolved. |
| M3 | Q-NUGET-1…7 | NuGet delete semantics, upstream search forwarding, ID casing |
| M4 | Q-CI-1…4, Q-CI-6…9, Q-AUTHZ-6 | Mapping evaluation (first match or union), scope grammar, replay default, fork default |
| M5 | Q-HA-1 (same as Q-STOR-1 and Q-OPS-2), Q-HA-5, Q-HA-6, Q-HA-7, Q-HA-10, Q-OPS-1, Q-ROAD-2 | Readiness under object-storage failure, upgrade-skip policy, Gateway API priority, egress default. R-HA-039 already runs its tests through an HTTPRoute, which assumes Q-HA-7 option (b). |
| P1 | Q-HA-4, Q-HA-9, Q-CI-5, Q-AUTHZ-7, Q-AUTHZ-8, Q-API-5, Q-STOR-3, Q-STOR-5, Q-STOR-7, Q-STOR-8, Q-OPS-5, Q-OPS-6, Q-OPS-8, Q-NPM-1, Q-ROAD-3 | The OCI URL layout (F4), the revocation journal, the `CI_JOB_TOKEN` fallback, which page owns build records, trash and quota semantics, configuration documents, npm host-root routes (M10), the order of P1 formats |

## M0 · Foundation

**Goal.** M0 delivers a production-shaped platform with no package format: one binary, two
replicas on kind, PostgreSQL with migrations, a content-addressed blob store on S3, coordination
between replicas, the chart skeleton, an embedded Angular shell, observability, and the coverage
generator that measures everything after it.

**Scope.**

| Area | Requirements |
|---|---|
| Architecture | R-ARCH-001…006, R-ARCH-016…026 |
| Storage | R-STOR-001…006, R-STOR-024, R-STOR-025, R-STOR-030, R-STOR-031, R-STOR-033, R-STOR-035 |
| HA and chart | R-HA-001, R-HA-003…006, R-HA-013…016, R-HA-018, R-HA-019, R-HA-027…030, R-HA-033…037, R-HA-051 |
| Operations | R-OPS-001, R-OPS-002, R-OPS-004, R-OPS-006, R-OPS-007, R-OPS-009, R-OPS-010, R-OPS-013, R-OPS-018, R-OPS-028 |
| API | R-API-001…003, R-API-009, R-API-038 |
| UI | R-UI-001, R-UI-046, R-UI-047 |
| trove-only | Coverage generator (below) |

**Coverage generator.** This is a Go `main` package, proposed at `internal/tools/coverage/` and run
with `go run ./internal/tools/coverage`. R-ARCH-003's layout tree gains the entry in the same
change. It reads every page under `docs/features/` and `docs/spec/` and checks that:

- each `### <ID> · <Title>` heading is followed by a `Priority:` line with one of the five rubric
  values;
- each `### R-…` heading is followed by `Priority: **…** · Covers: …`;
- every `Covers:` ID exists in the catalog or is `trove-only`;
- requirement IDs, catalog IDs, `Q-` IDs and `H-` numbers are unique.

It ignores headings inside fenced code blocks, because the format example in
[docs/spec/README.md](../spec/README.md) reuses `R-NPM-012`. It writes the checklist with a status
per ID and counts per priority. Q-ROAD-4 defines how it learns implementation status.

**Exit criteria.**

- **E0.1** `go build ./cmd/trove` succeeds on a clean checkout without Node.js, and
  `go generate ./... && git diff --exit-code` passes in CI (R-ARCH-003).
- **E0.2** One version per tool. CI fails in any of these cases:
  - the `go version` in its log differs from `go 1.27.3` in `go.mod`;
  - a code generator runs at a version other than its `tool` directive;
  - the Angular packages differ from the lockfile of `web/`.

  The dev profile's tool pins (kind, kubectl, Helm, CloudNativePG chart, PostgreSQL image, S3
  store) are read from one place by both the task runner and CI (R-HA-051).
- **E0.3** On a machine with Docker and the pinned tools, `dev-up` produces two trove replicas,
  a two-instance CloudNativePG cluster and the S3 store on a three-node kind cluster in under
  10 minutes. Both pods answer `200` on `:8081/readyz`, and `GET /api/v1/system/ping` answers `OK`
  through the Ingress. R-HA-051's `npm ping` criterion moves to M2.
- **E0.4** On an empty PostgreSQL at the floor version from Q-HA-2, the migration Job creates the
  schema. If two migration Jobs start at once, one applies the migrations and the other waits on
  the advisory lock, then exits with nothing to do. A migration that fails on purpose leaves the
  running replicas serving and makes `helm upgrade` fail. This is the "Migration failure" row of
  R-HA-052, run early.
- **E0.5** The S3 conformance check of R-STOR-035 passes on every change against the CI store
  chosen in Q-HA-3, and nightly against AWS S3. The race test of R-STOR-005 passes, and so does
  R-STOR-024's variant with the collector killed at random points. When 100 concurrent uploads of
  identical content are spread across both replicas, exactly one object exists under its content
  address.
- **E0.6** A singleton job holds its lease on exactly one replica. Force-deleting that pod moves
  the lease to the other replica, and the lease table never shows two holders with valid fencing
  tokens (R-HA-006). On the dev profile, cross-replica invalidation reaches p99 ≤ 1 s (P-13) and
  job pick-up reaches p99 ≤ 1 s (P-14).
- **E0.7** `helm lint` and `helm template` pass with the dev values. `helm template` without the
  encryption-key Secret fails with R-HA-029's message. The pods start in a namespace that enforces
  the restricted Pod Security Standard (R-HA-033).
- **E0.8** The coverage generator reproduces the table in [Starting point](#starting-point) from
  the pages, fails CI on each rule violation listed above, and its committed output passes
  `git diff --exit-code`.
- **E0.9** The binary serves the Angular 22 shell under `/ui/` from its embedded build. The web unit
  tests run in CI (R-UI-001, R-UI-047).
- **E0.10** `:8081/metrics` exposes `trove_` metrics, the request log is JSON, and a request that
  carries `traceparent` keeps its trace ID in the log and in `X-Request-Id` (R-OPS-002, R-OPS-018).

**Work packages.**

| WP | Content | Depends on | Runs in parallel with |
|---|---|---|---|
| WP0.1 | Repository skeleton: `go.mod` with `tool` pins, `web/` workspace with lockfile, task runner, architecture test of R-ARCH-002, placeholder `dist/` | — | — |
| WP0.2 | CI: lint, unit tests, generate-diff check, image build, chart lint, integration tier with PostgreSQL and the S3 store in containers, version check of E0.2 | WP0.1 | WP0.3 to WP0.9 |
| WP0.3 | PostgreSQL: pool and connection budget, migration runner under an advisory lock with the compatibility gate, core schema of R-STOR-003, roles and TLS | WP0.1 | WP0.4 to WP0.9 |
| WP0.4 | Blob store: S3 client, spool, single-pass hashing, commit protocol, multipart transfer, conformance check, garbage collection | WP0.3 (schema), WP0.5 (GC lease) | WP0.6 to WP0.9 |
| WP0.5 | Coordination: job queue, leases with fencing, advisory-lock categories, LISTEN/NOTIFY, in-process caches, replica registry | WP0.3 | WP0.4, WP0.6 to WP0.9 |
| WP0.6 | Server shell: listeners, middleware, client address, error model, configuration, lifecycle, stored-secret encryption, outbound HTTP client, logs, metrics, traces, OpenAPI skeleton, ping and version | WP0.1 | all |
| WP0.7 | Helm chart skeleton and dev profile | WP0.3 (`migrate`), WP0.6 (probes) | WP0.4, WP0.5, WP0.8, WP0.9 |
| WP0.8 | Angular workspace, signals baseline, embedding and serving | WP0.1 | all |
| WP0.9 | Coverage generator | WP0.1 | all |

**Not in M0:** repositories, users, tokens and any package format. The GC trigger API waits for the
management API of M1. Failure-mode guarantees beyond E0.4 and E0.6 belong to M5.

## M1 · Access core

**Goal.** M1 delivers humans signing in through OIDC, tokens for machines, deny-by-default
permissions, and the repository model, all proven on raw file transfer to a generic local
repository. It also delivers the management API conventions that every later resource inherits,
and the administration UI.

**Scope.**

| Area | Requirements |
|---|---|
| Identity | R-AUTH-001…018, R-AUTH-020, R-AUTH-022…024, R-AUTH-026…029, R-AUTH-032, R-AUTH-033, R-AUTH-037, R-AUTH-038, R-AUTH-040, R-AUTH-041 |
| Authorization | R-AUTHZ-001…006, R-AUTHZ-010, R-AUTHZ-013, R-AUTHZ-015, R-AUTHZ-016, R-AUTHZ-021…023, R-AUTHZ-028, R-AUTHZ-029 |
| Repositories | R-REPO-001…006, R-REPO-008…013 |
| Request paths and items | R-ARCH-007, R-ARCH-011, R-ARCH-014, R-STOR-007, R-STOR-012, R-STOR-013, R-STOR-016, R-STOR-018 |
| Deployment | R-HA-040, R-HA-043 |
| Operations and audit | R-OPS-005, R-OPS-014…017, R-OPS-020 |
| API | R-API-004…008, R-API-010…022, R-API-025, R-API-031…033, R-API-037, R-API-039, R-API-040 |
| UI | R-UI-002…005, R-UI-010, R-UI-011, R-UI-020, R-UI-021, R-UI-024…027, R-UI-029…031, R-UI-033, R-UI-037, R-UI-038, R-UI-042…045 |
| Catalog, no requirement yet (Q-ROAD-1) | Local class of PKG-GENERIC-001, plus PKG-GENERIC-004 and PKG-GENERIC-005; FMT-013 |

M1 is the largest milestone. Its work packages touch separate packages (`auth`, `authz`,
`repository`, `api/v1`, `web`), so this is where parallel teams pay off most.

**Exit criteria.**

- **E1.1** In the dev profile, a Keycloak test IdP signs a user in through the
  authorization code flow with PKCE. The user is created just in time, groups come from the
  configured claim, logout ends the trove session, and R-AUTH-013's IdP logout criterion passes.
  The break-glass administrator signs in and can be disabled (R-AUTH-014).
- **E1.2** A personal access token is shown once, and the database stores only its SHA-256. A test
  scans a database dump for the token value and finds nothing. After revocation through replica A,
  a request with that token through replica B gets `401` within 2 s (R-AUTH-029's bound).
- **E1.3** Deny by default holds on a fresh installation. With one generic local repository and no
  policies, anonymous and authenticated requests are refused as R-AUTHZ-016 specifies. A table of
  at least 200 authorization cases passes, covering policy union, token scope as a ceiling,
  include and exclude patterns, `deploy` without `overwrite`, and hidden existence. R-AUTHZ-006's
  `files-internal` criterion is one of those cases.
- **E1.4** Raw transfer on `generic-local` passes the criteria of R-STOR-007, R-STOR-012 and
  R-API-025: `201` and then `200` on re-upload, a `Range` request returns 100 bytes, and a digest
  mismatch is refused. An `evil.svg` served from `/files/` carries the R-ARCH-007 headers.
- **E1.5** Every login, failed login, token creation and revocation, policy change and denied
  deploy produces an audit event. The event can be found through `GET /api/v1/audit-events` and
  appears in the log stream (R-OPS-015…017).
- **E1.6** The OpenAPI document validates as OpenAPI 3.1. Every implemented route has a test that
  validates its responses against the document (R-API-002). Pagination, `ETag` with `If-Match`,
  merge patch and idempotency keys each have a contract test on the repository resource.
- **E1.7** These UI screens work against the real API: sign-in, repository list and wizard, file
  tree, users, groups, service accounts, policy editor, profile tokens, token administration,
  authentication settings, audit log and system status. An automated accessibility check reports no
  serious violations on any of them (R-UI-044). The English and German builds both pass (R-UI-045).
- **E1.8** R-AUTH-038's brute-force thresholds trigger in an automated test against Basic, Bearer
  and the login flow. R-API-016's rate limits answer `429` with `Retry-After`.

**Work packages.**

| WP | Content | Depends on | Runs in parallel with |
|---|---|---|---|
| WP1.1 | OIDC login, sessions, CSRF, logout, break-glass account, bootstrap commands, test IdP in the dev profile | M0 | WP1.2, WP1.4, WP1.6 to WP1.8 |
| WP1.2 | Users, groups, service accounts, token types, storage, scopes, lifetime, revocation, expiry notices | M0 | WP1.1, WP1.4, WP1.6 to WP1.8 |
| WP1.3 | Authorization engine: policies, selectors, patterns, evaluation, compiled grant index with invalidation, decision audit | WP1.2 (principals) | WP1.4, WP1.6 to WP1.8 |
| WP1.4 | Repository model and API, plus the generic local class (Q-ROAD-1) | M0 | WP1.1 to WP1.3 |
| WP1.5 | Raw transfer: `/files/`, items API, upload and download request paths | WP1.3, WP1.4 | WP1.6 to WP1.8 |
| WP1.6 | API conventions and admin resources: errors, pagination, ETags, idempotency, jobs, rate limits, CORS, settings, audit, GC trigger | M0 | all |
| WP1.7 | Event registry and audit recording | M0 | all |
| WP1.8 | UI shell and administration screens | consumes the OpenAPI types that WP1.6 produces; trails the API by one step | all |

**Not in M1:** any package protocol. Package ownership waits for M2, because it needs package
names. CI identities wait for M4.

## M2 · npm

**Goal.** M2 delivers npm with local, remote and virtual repositories for npm, pnpm and both Yarn
lines, and with it the format contract, the proxy engine and virtual resolution that every later
format reuses.

**Scope.**

| Area | Requirements |
|---|---|
| npm | R-NPM-001…013, R-NPM-015…017, R-NPM-019, R-NPM-024…027, R-NPM-029…033, R-NPM-040…045 |
| Format contract | R-ARCH-008…010, R-ARCH-028 |
| Proxy and virtual engines | R-ARCH-012, R-ARCH-013, R-ARCH-015, R-REPO-017, R-REPO-019…021, R-REPO-023…030, R-REPO-032, R-REPO-033, R-REPO-038…043, R-REPO-045, R-REPO-047, R-OPS-011 |
| Authorization | R-AUTHZ-008, R-AUTHZ-009, R-AUTHZ-018, R-AUTHZ-019 |
| API and UI | R-API-029, R-API-030, R-UI-006…009, R-UI-016, R-UI-017, R-UI-022 |

**Exit criteria.**

- **E2.1** On two replicas behind the chart's Ingress, every MVP scenario of R-NPM-045 passes for
  npm 11, pnpm 10, Yarn 4 and Yarn 1.22. If Q-NPM-4 is decided as recommended, npm 12 and pnpm 12
  must pass as well. The P1 scenarios receive the answers R-NPM-043 specifies. The `e2e-npm` job
  gates every change.
- **E2.2** Cold cache, concurrent clients: three clients run `npm ci` at once, each against a
  different replica of three. They use an empty client cache and the reference lockfile in `perf/`
  (at least 500 packages), and resolve through `npm-all`, a virtual repository over `npm-local` and
  `npm-remote`, with the remote pointing at the counting test upstream. All three succeed. The
  upstream records exactly one request per tarball and at most one per package document (P-12).
- **E2.3** `npm publish` through replica A, followed at once by `npm view` of that version through
  `npm-all` on replica B, returns the new version (R-ARCH-012, Q-ARCH-9).
- **E2.4** With the upstream stopped, `npm ci` on a warm cache succeeds from stale metadata
  (R-REPO-029). With the upstream answering `404` for a name, repeated requests inside the negative
  cache period cause no further upstream request (R-REPO-028).
- **E2.5** A name under a namespace claimed by `npm-local` (R-REPO-023, R-NPM-033) is never
  requested from the upstream, even when the upstream offers a higher version.
- **E2.6** npm passes checks 1 to 8 of the conformance kit (R-ARCH-010). A nightly Go fuzz run of
  npm's `Match` and `Inspect`, lasting at least 30 minutes, finds no crash and no breach of the
  R-ARCH-028 limits.
- **E2.7** `npm ping --registry http://localhost:8080/api/npm/npm-all/` succeeds on the dev profile.
  This is the remaining criterion of R-HA-051.
- **E2.8** The version published in E2.3 appears in the packages list, its README renders
  sanitized, and E2.1's configure step uses the snippets from the client setup dialog verbatim.

**Work packages.**

| WP | Content | Depends on | Runs in parallel with |
|---|---|---|---|
| WP2.1 | Format contract, registry, conformance kit, limits for untrusted content | M1 | — |
| WP2.2 | Proxy engine: remote settings, request path, single-flight and advisory-lock collapse, freshness, negative cache, offline circuit, egress rules, integrity check, URL rewriting | WP2.1 | WP2.3, WP2.4, WP2.7, WP2.8 |
| WP2.3 | Virtual resolution: membership, algorithm, merged-metadata cache, deployment target, pin rules and namespace claims | WP2.1 | WP2.2, WP2.4, WP2.7, WP2.8 |
| WP2.4 | npm local: publish, documents, revisions, dist-tags, unpublish, tombstones, whoami | WP2.1 | WP2.2, WP2.3 |
| WP2.5 | npm remote and virtual: document fetch, tarball URLs, upstream removals, merges | WP2.2 to WP2.4 | WP2.6 to WP2.8 |
| WP2.6 | Client matrix, counting upstream stub, reference lockfile in `perf/` | WP2.4 | WP2.5, WP2.7, WP2.8 |
| WP2.7 | Package UI, install panel, client setup dialog and snippets | WP2.4 | WP2.5, WP2.6, WP2.8 |
| WP2.8 | HA harness start: pod-crash scenario of R-HA-052 and the load scripts of R-ARCH-027 running against npm | WP2.4 | all |

**Not in M2:** the P1 npm operations (M6) and npm trusted publishing (M10).

## M3 · NuGet

**Goal.** M3 delivers NuGet v3 with local, remote and virtual repositories for the .NET CLI,
nuget.exe and Visual Studio. It also proves that the format contract is free of npm assumptions,
and then freezes it.

**Scope.**

| Area | Requirements |
|---|---|
| NuGet | R-NUGET-001…010, R-NUGET-012…020, R-NUGET-026…028 |
| Authorization | R-AUTHZ-007 |
| UI | R-UI-018 |

**Exit criteria.**

- **E3.1** R-NUGET-027 is green:
  - the .NET 10 SDK on Linux;
  - nuget.exe and the .NET 10 SDK on a Windows runner;
  - the nightly run against nuget.org.

  The manual Visual Studio 2022 checklist has been run once against the M3 build.
- **E3.2** Cold cache, concurrent clients: three clients run `dotnet restore --no-cache` at once
  against three replicas. They restore the reference solution in `perf/` (at least 100 packages in
  its restore graph) through `nuget-all`. All three succeed, and the counting upstream records one
  request per `.nupkg`.
- **E3.3** Contract freeze: at the end of M3 the format contract (R-ARCH-008, R-ARCH-009) has
  `ContractVersion` 1, and both formats pass the conformance kit. Any contract change that NuGet
  needed was applied to npm in the same change. From here on, every contract change is an ADR.
- **E3.4** Every row of R-AUTHZ-007's action map has one test per format. The test shows the
  operation succeeds with the action and fails without it.
- **E3.5** A push with a personal access token as `X-NuGet-ApiKey` succeeds, and so does a restore
  with a token as Basic password. A revoked token gets `401` on both (INV-8, R-NUGET-012).

**Work packages.** These can run in parallel:

- WP3.1, NuGet local: ingestion, push, unlist and delete, flat container, registrations, search;
- WP3.2, remote and virtual repositories;
- WP3.3, symbol package push;
- WP3.4, the acceptance suite, including the Windows runner.

After those, WP3.5 holds the contract-freeze review and the action-map tests.

**Not in M3:** the symbol server, symbol proxying, vulnerability data, autocomplete and reindexing
(M6). NuGet v2 is P2 (INV-14).

## M4 · GitLab CI OIDC

**Goal.** M4 lets GitLab CI jobs exchange their OIDC ID token for a short-lived trove token. Mapped
permissions give them read access to the packages they consume and write access to the packages
they produce.

**Scope.**

| Area | Requirements |
|---|---|
| CI | R-CI-001…021, R-CI-028 |
| Authorization | R-AUTHZ-014 |
| API and UI | R-API-035, R-UI-035, R-UI-036 |

**Exit criteria.**

- **E4.1** R-CI-028 passes against a GitLab test instance:
  - on a protected tag, both packages are published;
  - on an unprotected branch, both publishes get `403`;
  - the issuance list shows the right `project_id`, `ref` and `sha`;
  - no job log contains a `trv_cit_` value or the ID token.
- **E4.2** The negative cases of R-CI-004 pass, including the HS256-with-the-public-key case and
  the rejection of multiple audiences. R-CI-003's key-rotation test passes against an issuer
  double.
- **E4.3** With single use on (Q-CI-4), concurrent exchanges of one ID token on two replicas yield
  exactly one success (R-CI-018).
- **E4.4** A fork merge-request pipeline cannot publish to `npm-internal` (R-CI-012).
- **E4.5** For every claim set recorded during E4.1, the mapping dry run (R-CI-019) and the claim
  tester (R-UI-036) return the grants that the real exchange issued.
- **E4.6** A CI token used after its expiry gets `401`. Revoking an issuance makes its token fail
  within R-AUTH-029's 2 s bound.

**Work packages.** WP4.1 to WP4.4 start as soon as M1 ends, in parallel with M2 and M3:

- WP4.1: issuers, discovery, signing keys with caching and rotation, subject-token validation;
- WP4.2: exchange endpoint, errors, hardening, replay protection;
- WP4.3: mapping engine, claim conditions, templating, grants, ownership binding, CI tokens in
  evaluation;
- WP4.4: token lifetime, identity, revocation, issuance records.

WP4.5 (mapping API, dry run and UI) follows WP4.3. WP4.6 sets up the two test environments:

- per-change CI uses an issuer double that signs GitLab-shaped tokens with rotating keys;
- the reference pipeline runs against a real GitLab instance nightly and as the M4 exit.

Not verified: how long a GitLab container takes to become ready on the CI runners. If it exceeds a
job's time budget, the nightly run moves to a standing test instance.

**Not in M4:** the `CI_JOB_TOKEN` fallback (Q-CI-5), delegated mapping management, and GitHub
Actions and generic issuers (M10).

## M5 · HA hardening and Helm GA

**Goal.** M5 proves the failure-mode contract under real client load and finishes the chart for
production. Its result is 1.0.

**Scope.**

| Area | Requirements |
|---|---|
| Failure modes and upgrades | R-HA-007…012, R-HA-020…022, R-HA-024, R-STOR-036 |
| Chart GA | R-HA-031, R-HA-032, R-HA-039, R-HA-042, R-HA-048…050 |
| Proof | R-HA-052, R-ARCH-027, R-OPS-008 |
| P1 today, proposed for M5 (Q-ROAD-2) | R-HA-023, R-HA-025 |

**Exit criteria.**

- **E5.1** The full R-HA-052 suite is green in two consecutive nightly runs, and the restore drill
  has passed once.
- **E5.2** Cold cache with a replica killed mid-run: `npm ci` of the 500-package reference lockfile
  through `npm-all` succeeds on three replicas while one replica is force-deleted partway through.
  The same holds for `dotnet restore --no-cache` of the reference solution through `nuget-all`. No
  client iteration fails after the clients' own retries, and `trove admin storage verify` reports
  no item without its object.
- **E5.3** P-1 to P-15 of R-ARCH-027 are met in two consecutive nightly runs on the reference
  environment.
- **E5.4** A rolling upgrade from the M4 pre-release to the 1.0 candidate with the migration Job,
  followed by `helm rollback`, shows zero failed iterations under R-HA-052's load (R-HA-020).
- **E5.5** The values reference documents every chart value, and the values schema validates it
  (R-HA-031). Production example values render the PodDisruptionBudget, topology spreading,
  HorizontalPodAutoscaler and NetworkPolicy (R-HA-032, R-HA-042). R-HA-039's 6 GiB upload and
  15-minute slow download pass through the Ingress and through the HTTPRoute.
- **E5.6** The coverage generator shows no uncovered MVP catalog entry. Every MVP requirement has at
  least one passing test that names it (Q-ROAD-4).
- **E5.7** [docs/security/](../security/) describes the 1.0 mechanisms. Each H-item from the spec
  pages that applies to 1.0 appears under its H number in the `What this does not cover` section of
  the security page for its mechanism.
- **E5.8** The release pipeline publishes the image and chart to the registry chosen in Q-HA-11.
  `helm install` by digest from there passes E2.1's smoke scenarios. Image signing, SBOM and
  provenance arrive with R-HA-047 (M11).

**Work packages.** These can run in parallel:

| WP | Content |
|---|---|
| WP5.1 | Failure-mode contract, dependency failures, PostgreSQL failover, object-storage outage, graceful termination, node loss |
| WP5.2 | Rolling upgrades, mixed-version operation, background data migrations, upgrade policy |
| WP5.3 | Restore consistency, plus backup and restore under Q-ROAD-2 |
| WP5.4 | Chart GA: values reference and schema, PDB, spreading, HPA, network policies, edge proxy requirements, uninstall, prerequisites, sizing |
| WP5.5 | Completion of the failure-injection suite |
| WP5.6 | Performance runs and the metrics catalog |
| WP5.7 | Security pages and review; release pipeline |

## After 1.0: P1

P1 has two lanes, both starting at 1.0. The platform lane (M6 to M12) extends subsystems that 1.0
already has. The format lane (F1 to F7) adds formats through the frozen contract. Each lane ships 1.x
minor releases. M7 to M11 touch separate subsystems and can run in parallel. The order below is the
sequence to follow if only one team is available.

### P1 platform milestones

| Milestone | Scope | Exit criteria | Why at this position |
|---|---|---|---|
| **M6 · npm and NuGet completion** | R-NPM-014, R-NPM-018, R-NPM-020, R-NPM-021, R-NPM-023, R-NPM-028, R-NPM-034…039, R-NUGET-011, R-NUGET-021…025, R-NUGET-029, R-AUTH-034, R-AUTH-035, R-REPO-014…016, R-REPO-018, R-REPO-022, R-REPO-044, R-API-023, R-API-036, R-UI-023 | R-NPM-045's P1 scenarios (deprecate, web and legacy login, audit, search) join the gate and pass for every gated client. Symbols pushed in M3 are served through `nuget-all`, and nightly through a remote from nuget.org's symbol server. A nested virtual repository that would form a cycle is refused. With pass-through mode on, `npm ci` adds no object to the bucket. | Serves 1.0's users at once and closes the open rows of the acceptance matrix. Small, because it reuses the M2 and M3 engines. |
| **M7 · Storage lifecycle and retention** | R-STOR-026…029, R-STOR-037…042, R-STOR-044, R-STOR-045, R-REPO-007, R-REPO-046, R-OPS-019, R-OPS-021…023, R-API-024, R-API-043 | A cleanup dry run over a repository with 10,000 versions lists exactly the versions the real run then deletes, and GC reclaims their blobs after the grace period. Remote-cache items unused past the configured period are removed and fetched again on request. The integrity scrub reports an object altered in the bucket out of band. A deploy beyond a repository cap answers as Q-STOR-7 decides and leaves no orphaned object after the orphan sweep. | Remote caches and CI publishing grow storage without limit after go-live, so this is the first operational need. It also provides the orphan sweep that upload sessions (M8) rely on. |
| **M8 · Content operations and search** | R-STOR-008…011, R-STOR-015, R-STOR-017, R-STOR-019…023, R-API-026…028, R-UI-012…015, R-OPS-034; the SRCH entries once the search page exists | A chunked upload session survives killing the replica that received the first chunk (R-STOR-011). Copying 1,000 files between repositories creates no new object and keeps every digest. A file restored from the trash can is byte-identical. Checksum-first deploy of a known digest succeeds without a body. R-API-028's search criteria pass. | Start with R-STOR-011, which unblocks F4 (OCI), and with R-STOR-008 and R-STOR-010, which unblock F7 (Maven). |
| **M9 · Automation and integration** | R-OPS-029…032, R-API-044, R-OPS-033, R-OPS-035…044, R-API-041, R-UI-039, R-API-042, R-UI-040, R-AUTHZ-027, R-STOR-043; the BLD entries once the builds page exists | Applying the same configuration document twice yields zero changes on the second apply. Exporting an installation and applying the export to an empty one reproduces its repositories, policies and CI mappings. An npm publish event reaches a test receiver with a valid signature, succeeds by retry after three `503` answers, and every attempt shows in the delivery log. Deliveries to link-local addresses are refused (R-OPS-042). A build record from the reference pipeline links that job's npm and NuGet artifacts. | Operators can script the 1.0 API (INV-17) until then. Configuration documents, webhooks and builds are where they stop scripting it by hand. |
| **M10 · Identity, access and CI breadth** | R-AUTH-019, R-AUTH-021, R-AUTH-025, R-AUTH-030, R-AUTH-031, R-AUTH-036, R-AUTH-039, R-AUTHZ-011, R-AUTHZ-012, R-AUTHZ-017, R-AUTHZ-020, R-AUTHZ-024…026, R-CI-022…027, R-NPM-022, R-API-034, R-API-046…048, R-UI-028, R-UI-032, R-UI-041 | A GitHub Actions job publishes an npm package through the exchange (R-CI-025). A generic issuer works against an issuer double (R-CI-026). `npm publish` from GitLab CI works without a stored token (R-NPM-022, after Q-NPM-1). A SCIM client provisions and deprovisions users and groups, and deprovisioning revokes their tokens. For 10,000 random requests, the effective-permission explainer returns the evaluator's decision. | Extends the access model that 1.0 proved. Trusted publishing needs the host-root route decision (Q-NPM-1), and projects need Q-AUTHZ-7. |
| **M11 · HA and deployment breadth** | R-HA-002, R-HA-017, R-HA-026, R-HA-038, R-HA-044…047, R-HA-023 and R-HA-025 unless Q-ROAD-2 moves them to M5, R-STOR-032, R-STOR-034, R-REPO-031, R-REPO-034…037, R-OPS-003, R-OPS-012, R-OPS-024…027, R-API-045, R-UI-034 | R-HA-052 passes with separate api and worker Deployments. Database credential rotation under load causes zero failed iterations. An air-gapped install from a private registry verifies image and chart signatures before installing. A dual-stack kind cluster passes the npm suite. | Most 1.0 installations do not need these immediately. Signatures (R-HA-047) are the item most likely to be pulled forward. |
| **M12 · Upstream curation** | The 22 P1 curation entries (SEC-001, SEC-002, SEC-004, SEC-007…014, SEC-021…023, SEC-025…028, SEC-032, SEC-035…037); spec page first | A curated npm remote blocks a version flagged malicious in the risk data source, and serves the newest compliant version where the client allows a fallback. A dry-run policy only records its decisions. A waiver lets one version through. | The rubric's P1 list does not name curation, but the catalog marks these entries P1. Curation needs a spec page, an external risk data source and remotes in several ecosystems before it is worth building. Until then, R-NPM-012's release-age policy and R-REPO-023's namespace claims provide the cheapest protections. |

### P1 format expansion order

Every format follows the same path:

1. A spec page `docs/spec/formats/<format>.md` with requirements and `Covers:` lines.
2. The conformance kit.
3. A native-client acceptance matrix on two replicas behind the chart.
4. A cold-cache install of a reference project through a virtual repository on three replicas,
   with one replica killed mid-run.
5. One more load loop in R-HA-052.
6. Client setup instructions (R-UI-019).

The table adds the criterion specific to each format.

How the order was set. Demand counts first. Among formats of similar demand, the one closer to what
already exists goes first, because each new format then adds a single unknown. A format whose
prerequisites are platform work starts that work early and runs as its own lane. Demand here is
judged, not measured: no usage data from the owner's teams was available. Q-ROAD-3 asks for it.

| Order | Format | P1 catalog entries | Closeness to what exists | Prerequisites beyond 1.0 | Format-specific exit criterion |
|---|---|---|---|---|---|
| F1 | Generic: remote and virtual | 8 (local part in M1) | Closest: `/files/` exists, and a remote is the proxy engine without a metadata document. | none | A remote serves a 1 GiB upstream file to 10 concurrent clients with one upstream request. A `PUT` through a virtual repository lands in its deployment target. |
| F2 | PyPI | 23 | Same shape as npm: one index page per project plus files on a separate host, URL rewriting, a merge per project in virtual repositories. Upload uses Basic, which NuGet already accepts. Name normalization resembles NuGet's. | none | `pip`, `uv` and Poetry install through a virtual repository. `twine upload` with a token as password succeeds. Names that differ only in `.`, `_` and `-` resolve to one project (PKG-PYPI-003). |
| F3 | Helm, index-based | part of 18 | One repository-wide `index.yaml` merged across members is npm's document merge applied to a whole repository. Chart parsing is like reading `package.json` from a tarball. | none | `helm repo add` and `helm install` work from a virtual repository whose index merges a local and a remote member. trove's own chart is published there. |
| F4 | OCI and Docker | 15 + 34 | Highest demand: every Kubernetes deployment, trove's own included, pulls images, and CI pipelines commonly build them. Also furthest from what exists: the registry API lives at `/v2/` outside INV-6, needs a bearer-token challenge, chunked uploads, manifests and tags as a data model, referrers, and multi-gigabyte layers. | R-STOR-011 (M8) with the orphan sweep (M7), an INV-6 amendment through Q-HA-4, R-HA-041 edge routing, R-STOR-039 for untagged manifests, a P-12 measurement for Q-ARCH-6 | `docker`, Podman and ORAS push and pull through local, remote and virtual repositories, including referrers (BEYOND-015). A 5 GiB layer push survives the kill of the replica holding the session. In a cold-cache image pull test, the collapse-bypass counter stays at zero, or Q-ARCH-6 moves to its lease-row option. |
| F5 | Helm charts as OCI | part of 18 | A thin layer over F4: chart artifact type and chart metadata. | F4 | `helm push` and `helm install oci://…` work through a virtual OCI repository. |
| F6 | Go | 13 | Close to generic: the module proxy protocol is a fixed file layout (`list`, `.info`, `.mod`, `.zip`), and merging version lists is trivial. The `go` command has no publish operation (PKG-GO-006), so local hosting needs trove's own upload path. Remotes backed by version control systems are a new kind of upstream and come last. | none for the module-mirror remote | `go mod download` of a reference module graph with a cold cache works through a virtual repository, with checksum database proxying on (PKG-GO-014). trove's own `go.mod` resolves through it. |
| F7 | Maven, with Gradle's P1 entries | 23 + 4 | Path-based like generic, but with the largest set of rules apart from OCI: releases and snapshots, metadata calculation, POM consistency, checksum sidecars, layouts. | R-STOR-008 and R-STOR-010 (M8), R-STOR-038 snapshot cleanup (M7), FMT-010 default layouts | `mvn deploy` of a release and a snapshot succeeds. `mvn -U` resolves the newest snapshot through a virtual repository. A Gradle build reads module metadata (PKG-JVM-005) from the same repository. Checksum sidecars behave as R-STOR-010 specifies. |

Lanes:

- **Lane A** (small formats that follow known patterns and test how far the contract stretches):
  F1, then F2 and F3 in parallel.
- **Lane B** (largest format, highest demand): F4, then F5. Its platform prerequisites begin at
  1.0, inside M7 and M8.
- **Lane C** (after Lane A frees capacity): F6, then F7 once M7 and M8 have delivered its storage
  prerequisites.

If Q-ROAD-3 shows the owner's teams depend on Maven, pull R-STOR-008 and R-STOR-010 to the front of
M8 and move F7 directly after F2.

The two P1 requirements of the format lane are R-HA-041 (container-registry routing at the edge,
part of F4) and R-UI-019 (client instructions for further formats, extended by each format).

### Spec work before P1 implementation

The 223 P1 catalog entries that no requirement covers yet, grouped by the spec page that has to
cover them first:

| Spec page to write or extend | Catalog entries | Blocks |
|---|---|---|
| `formats/generic.md` | PKG-GENERIC-001…006, PKG-GENERIC-008, PKG-GENERIC-011, REPO-003 | M1 (local part, Q-ROAD-1), F1 |
| `formats/pypi.md` | 23 PKG-PYPI entries | F2 |
| `formats/helm.md` | 18 PKG-HELM entries | F3, F5 |
| `formats/oci.md`, `formats/docker.md` | 15 PKG-OCI and 34 PKG-DOCKER entries, REPO-043, OPS-062, OPS-063 | F4 |
| `formats/go.md` | 13 PKG-GO entries | F6 |
| `formats/maven.md` | 23 PKG-MAVEN entries, PKG-JVM-001…003, PKG-JVM-005, PKG-BAZEL-011, REPO-022…024, REPO-026, REPO-041, RMT-036, FMT-010 | F7 |
| Builds page (which page owns it: Q-AUTHZ-8, Q-API-5, Q-STOR-3) | BLD-001…007, BLD-010, BLD-012…016, BLD-023, BLD-024 | M9 |
| Search page | 21 SRCH entries | M8 |
| Curation page | 22 SEC entries | M12 |
| Existing pages, small additions | REPO-013, REPO-045, ADMAPI-019, ADMAPI-066, ADMAPI-068 (M10); EVT-034 (M9); FMT-014 (M6); HA-075…077, OPS-057 (M11); BEYOND-015 (F4), BEYOND-021 and BEYOND-031 (M10), BEYOND-027 (each format from F2 on) | as listed |

HA-077 (container installation without Kubernetes) is P1 in the catalog, but the owner's
constraints name Helm as the deployment. Writing its spec section should start with confirming that
it stays in P1.

## P2 breadth

P2 covers the remaining 33 catalog formats, plus R-STOR-014 and 896 P2 catalog entries without a
requirement. No milestone numbers yet. Within each family, the order is demand first, then
closeness to the engines that will exist by then.

| Order | Family | Formats | Shared prerequisite | Why at this position |
|---|---|---|---|---|
| 1 | Variants of P1 engines | Gradle (rest), Ivy, sbt, Eclipse P2 (remote only); NuGet v2 (OData); model-serving containers (OCI remote) | the P1 engine they extend | The smallest step each. NuGet v2 unlocks proxying of the Chocolatey community repository and the PowerShell Gallery, which per [beyond-baseline.md](../features/beyond-baseline.md) are v2 endpoints. |
| 2 | Language registries with a per-package index | Cargo (sparse index), RubyGems, Terraform and OpenTofu registry with the provider network mirror, Composer, Conan, Conda, pub, Hex, Swift, CRAN, LuaRocks, Ansible Galaxy, Puppet, Chef, CocoaPods, Bower | the npm and PyPI patterns of document merge and file rewrite | Shaped like npm and PyPI. Ordered by expected demand. Bower comes last because its ecosystem is deprecated. |
| 3 | Signed operating-system repositories | Debian, RPM, Alpine, Opkg | key custody for repository signing keys (sealed with R-ARCH-021's mechanism) and an index-regeneration job per repository | Debian and RPM come together because they share key custody. Debian has the most P2 entries of any format page (27). |
| 4 | Large artifacts | Hugging Face models and datasets, Git LFS | upload sessions (R-STOR-011), streaming target P-9 | Both move multi-gigabyte objects. They reuse what F4 forced into existence. |
| 5 | Proxy-only and IDE sources | version-control archives, editor extension marketplace proxy, JetBrains IDE plugins | the proxy engine with a host allowlist | Remote only. The JetBrains clients cannot authenticate (FMT-013), so anonymous read-only access must be scoped per repository. |
| 6 | AI agent formats | agent plugins, agent packages, agent skills | none | The protocols are young, and the format matrix marks agent skills as beta. Build them once a client protocol stabilizes. |
| 7 | Remaining | Vagrant, Nix, Bazel modules, Terraform state backend | — | State files can hold secrets, and the backend needs locking semantics, so the state backend gets its own security review. |

P2 platform work, roughly in order:

1. Push and pull replication (REPL-001…017), the first step toward multiple sites.
2. A query language (search entries marked P2).
3. Presigned download redirects (R-STOR-014, STOR-032…035).
4. Further storage back ends (STOR-030 Google Cloud Storage, STOR-031 Azure Blob Storage) and
   multi-bucket sharding (STOR-026).
5. Email notification of expiring tokens (TOK-023, which needs R-OPS-033 from M9).
6. Internal TLS from the edge to the pods (HA-018, closes H-38).
7. Read-only queries on PostgreSQL standbys.
8. Worker autoscaling on queue depth.
9. Transaction-mode pooler support (Q-HA-6, option b).
10. Repository export and import (OPS-036, OPS-037).
11. Build promotion (BLD-020).
12. The P2 curation conditions (licenses, waiver workflows).

## Later

These are large subsystems with no slot in this plan. Each one would get its own spec page and its
own roadmap section when it moves up:

- federation of repositories across sites (51 Later entries on the replication and federation
  page) and active-active operation across sites;
- release sets, distribution to edge instances, lifecycle stages and evidence (69 Later entries on
  the distribution and lifecycle page);
- a vulnerability and license scanning engine (56 Later entries on the scanning page). The curation
  of M12 consumes an external risk data source and does not scan.
- machine-learning model repositories whose only client is a proprietary SDK
  ([format matrix](../features/formats/README.md));
- from the beyond-baseline page: OSTree hosting (BEYOND-005), TUF-signed metadata (BEYOND-025),
  and a checksum database for hosted Go modules (BEYOND-026). Each of these needs signing keys held
  and rotated by trove, which no earlier milestone provides.

## Beyond the baseline: selected candidates

Candidates from [beyond-baseline.md](../features/beyond-baseline.md), placed by what they build on.
The page's sizes are small, medium and large.

| Candidate | Catalog priority, size | Slot | Why |
|---|---|---|---|
| BEYOND-028 namespace claims against dependency confusion | P1, small | M2 (already MVP through R-REPO-023) | Protects local package names from the first remote repository on. |
| BEYOND-030 per-tenant quotas and rate limits | P2, medium | Rate limits in M1 (R-API-016 lists it); quotas after M7 | Rate limits are needed to run 1.0. Tenant quotas need M7's storage accounting. |
| BEYOND-027 minimum release age for remote repositories | P1, medium | npm in M2 (R-NPM-012), then with each format from F2 | Holds back fresh upstream releases at little cost. Each format defines publish time differently, so a missing time must lead to an explicit policy decision. |
| BEYOND-015 referrers through remote and virtual repositories | P1, medium | F4 | Without it, signatures and SBOMs attached to proxied images stay invisible. |
| BEYOND-031 provider presets for further CI systems | P1, small | M10 | Data and validation on top of R-CI-026's generic issuer. |
| BEYOND-021 npm provenance for locally published packages | P1, medium | M10, with trusted publishing (R-NPM-022) | Uses the same CI identity flow. Without it, packages published from CI carry no provenance. |
| BEYOND-032 Kubernetes service-account token trust | P2, small | First P2 identity item | trove runs on Kubernetes, so in-cluster workloads could authenticate without a stored token. |
| BEYOND-012, BEYOND-017, BEYOND-018 OCI additions | P2, small each | After F4 | Small rules on top of stored OCI data. |
| BEYOND-023 project status markers in the Python index | P2, small | After F2 | A per-project field and an index extension. |
| BEYOND-010 JSR through its npm compatibility layer | P2, small | After M2 | An npm remote with a scoped client line. |
| BEYOND-029 cache pre-warming from lockfiles and SBOMs | P2, medium | After M7 | A job type on the M0 job framework. Useful for air-gapped installations. |
| BEYOND-024 SBOM attachment store | P2, medium | After F4 | For non-OCI formats this needs its own attach path. |
| BEYOND-019 Sigstore signature verification gate, then BEYOND-034 Notary Project verification and BEYOND-020 SLSA provenance policy | P2, large then medium | After F4 and BEYOND-015 | One policy framework, reused by all three. |

The remaining candidates are new formats (BEYOND-001…004, BEYOND-006…009, BEYOND-011, BEYOND-013,
BEYOND-014) or depend on a base listed above (BEYOND-016, BEYOND-022, BEYOND-033). They join the P2
breadth list after the catalog formats of their family.

## Open questions

### Q-ROAD-1 · Generic local repositories in M1

The generic format is P1. Sixteen MVP requirements and the load of R-HA-052 nevertheless test
against a generic local repository (see [Starting point](#starting-point)).

- (a) Bring the local class of the generic format (PKG-GENERIC-001 local part, PKG-GENERIC-004,
  PKG-GENERIC-005) into M1 as MVP. Remote and virtual generic repositories stay P1 (F1).
- (b) Keep generic at P1, and rewrite the 16 acceptance criteria against npm and NuGet repositories.
- (c) Add a test-only format compiled into test builds.

Recommendation: **(a)**. npm and NuGet refuse raw deploys (R-API-025), so (b) would have to drop
the large-upload, `Range`, content-header and edge-proxy criteria altogether. (c) proves behavior
that no user receives. The cost of (a) is one format descriptor without metadata and a short spec
section.

### Q-ROAD-2 · Backup and restore in 1.0

R-HA-052 (MVP) runs a restore drill made of R-HA-025's steps. R-HA-025 and R-HA-023 are P1.

- (a) Move R-HA-023 and R-HA-025 into M5.
- (b) Remove the restore drill from R-HA-052 for 1.0.

Recommendation: **(a)**. R-HA-024's restore consistency (MVP) already assumes a restore procedure.
The procedure is mostly the platform's point-in-time recovery plus bucket versioning, and an HA
release whose restore was never run is not ready for production.

### Q-ROAD-3 · Ecosystems the owner's teams use after npm and NuGet

The P1 format order rests on judged demand. Measured demand from the teams that will use trove
could reorder it, for example moving Maven ahead of OCI preparation.

- (a) Keep the order on this page.
- (b) Reorder by an inventory of the package managers in the owner's CI pipelines. That could be a
  count of the lockfiles and descriptor files present in the GitLab projects.

Recommendation: **(b)** if such an inventory is cheap to obtain, otherwise (a). The answer changes
the format lane, not the platform lane.

### Q-ROAD-4 · How the coverage generator learns implementation status

- (a) Tests carry the requirement ID they verify in their name: a Go subtest `t.Run("R-NPM-007 …")`
  or an end-to-end test title. The generator reads `go test -json` and the end-to-end reports from
  CI.
- (b) A status field per requirement on the spec pages, edited by hand.
- (c) A separate status file in this directory.

Recommendation: **(a)**. The status follows from evidence and cannot drift from it. A requirement
without a passing test that names it is not implemented, whatever a field says. (b) and (c) are
edited by hand and fall out of date the first time someone forgets them.
