# Developer documentation

Status: specification phase. The repository holds documentation only: no Go code, no Angular
workspace, no Helm chart, no CI workflow and no task runner. This page covers how to work on the
documents today. The code layout, test tiers and build commands below are the planned design from
[docs/spec/](../spec/README.md), and they are marked as planned.

## What to have in your head first

- **Four parts, one direction.** The [feature catalog](../features/README.md) says *what* a
  full-featured binary repository manager does. The [specification](../spec/README.md) says *how*
  trove will do it. Each requirement names the catalog entries it covers. [Coverage](../coverage/README.md)
  measures the gap between the two, and [decisions.md](../decisions.md) records *why*.
- **Owner constraints are fixed.** Go 1.27.3, Angular 22, PostgreSQL, S3, Helm, OIDC login, tokens,
  detailed permissions, GitLab CI OIDC, pull-through proxying, and npm and NuGet first. Since
  2026-10-28 also: high availability on HA PostgreSQL, HA Valkey with Sentinel and S3, which cannot
  be deselected; load beyond one replica, shared by a Kubernetes Service that spreads requests at
  random; node drains without outage; a configurable backup job that writes a consistent backup to
  S3; and no ReadWriteMany volume. They are listed in
  [spec/README.md](../spec/README.md#owner-constraints) and are not up for redesign.
- **Invariants `INV-1` to `INV-19` bind every spec page.** A page may refine an invariant but must
  not contradict it. If an invariant looks wrong, raise a question `Q-<AREA>-n` in the page's
  `## Decisions` section and decide it there; a choice that changes an invariant edits it in
  [spec/README.md](../spec/README.md#design-invariants). Every invariant is a record in
  [decisions.md](../decisions.md).
- **Three roles, one binary** (INV-2, [R-ARCH-001](../spec/architecture.md)). `web` serves every
  HTTP request and never claims a job or holds a lease. `worker` runs jobs and serves only the ops
  listener. `backup` runs the backup and restore commands and uses no Valkey. Work that outlasts a
  request on `web` is enqueued as a job. `all` combines `web` and `worker` for a binary started
  outside the chart; the chart never renders it.
- **PostgreSQL decides, Valkey accelerates** (INV-3, [R-ARCH-030](../spec/architecture.md)). State
  whose loss would cost correctness or data, including every mutual exclusion whose failure would
  corrupt data, lives in PostgreSQL. Valkey holds request collapsing, the negative cache,
  cluster-wide rate-limit counters, change messages and signals. Every use of Valkey has a fallback
  that works without it. A value read from Valkey can at most make trove refuse (a `404` from a
  negative entry, a `429` from a counter), never grant access or supply served content. Every key
  expires, and only `internal/cluster` talks to Valkey.
- **No state between requests** (INV-18, [R-ARCH-032](../spec/architecture.md)). Consecutive
  requests of one client may land on different replicas. A protocol that spans several requests
  keeps its state in PostgreSQL or Valkey and its data in S3; local disk is scratch space for one
  request or one job.
- **IDs are permanent.** No ID is ever renumbered or reused. A new entry takes the next free number
  in its prefix.
- **Tools read the headings.** A `### <ID> · <Title>` heading followed directly by a `Priority:` line
  is how catalog entries and requirements are found. Keep both on one line each.
- **Neutral names.** Catalog entries describe behavior with descriptive, product-neutral names.
  Links go to open standards and to the documentation of the ecosystems trove speaks.
- **One version per tool** (planned, R-ARCH-003, R-ARCH-004). `go.mod` states `go 1.27.3` and pins
  code generators as `tool` directives. `web/package.json` uses exact versions with a committed
  lockfile. `web/.nvmrc` names the one Node.js version. Local builds and CI read the same pins.

## Pages

| Page | Content |
|---|---|
| This page | Entry point: ID schemes, adding catalog entries and requirements, coverage, planned layout, the local development profile and test tiers |

Topic pages are created together with the code they describe. Until then their design lives in the
spec:

| Future topic | Design today |
|---|---|
| Architecture: components, request paths, startup | [spec/architecture.md](../spec/architecture.md) |
| Process roles, graceful shutdown and node drains | [spec/architecture.md](../spec/architecture.md) R-ARCH-001, R-ARCH-024, R-ARCH-032; [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-002, R-HA-011, R-HA-059, R-HA-062 |
| Valkey: usage map, client, request collapsing, rate limits, degraded mode | [spec/architecture.md](../spec/architecture.md) R-ARCH-030, R-ARCH-031, R-ARCH-033, R-ARCH-034; [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-056, R-HA-057 |
| PostgreSQL connections, change log and leases | [spec/architecture.md](../spec/architecture.md) R-ARCH-016 to R-ARCH-019; [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-016, R-HA-058 |
| Backup and restore | [spec/backup-and-restore.md](../spec/backup-and-restore.md); chart side [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-060 |
| Build, test and lint matrix | [spec/architecture.md](../spec/architecture.md) R-ARCH-003, R-ARCH-004; no task runner chosen yet |
| Local development profile | [Local development profile](#local-development-profile-planned) below; [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-051 |
| Adding a package format | [spec/architecture.md](../spec/architecture.md) R-ARCH-008 to R-ARCH-010 |
| CI and release: images, chart, signatures | [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-047; published to ghcr.io/guided-traffic (image `trove`, chart `charts/trove`), Q-HA-11 |

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

## How to add a catalog entry

1. Pick the page by scope from the page table in [features/README.md](../features/README.md#pages).
   Format-specific behavior goes to the format's page under
   [features/formats/](../features/formats/README.md), and behavior shared by formats goes to
   `FMT`.
2. Take the next free number in the page's prefix (command above).
3. Write the heading `### <ID> · <Title>` and, directly below it, `Priority: **<P>**` using the
   rubric.
4. Describe what the feature does, its settings with descriptive names, meanings and defaults, the
   API operations, the UI location, limits, edge cases and interactions. Write *unspecified* where a
   value is not known. Put long tables into `<details>`.
5. If the spec already handles the feature, add the new ID to the `Covers:` line of that
   requirement. Then regenerate coverage (below).

## How to add a spec requirement

1. Pick the page by scope from the page table in [spec/README.md](../spec/README.md#pages) and take
   the next free number in its prefix.
2. Write `### R-<AREA>-nnn · <Title>` and directly below
   `Priority: **<P>** · Covers: <catalog IDs>`. Use `Covers: trove-only` when no catalog entry
   applies.
3. Write the normative text with MUST, SHOULD and MAY in the
   [RFC 2119](https://www.rfc-editor.org/rfc/rfc2119) sense. Include the data model, the API, the
   configuration and acceptance criteria that a test can check. Mark each configuration value
   `# default (proposed)` or `# example`.
4. Check the requirement against the invariants and the owning pages named in it. A conflict
   becomes a question `Q-<AREA>-n` under `## Decisions`, decided in the block format of
   [spec/README.md](../spec/README.md#requirement-format) and listed in
   [decisions.md](../decisions.md#decisions-by-the-development-agent).
5. A security gap the requirement leaves open goes under `## What this does not cover` as
   `### H-<n> · <title>`. Take the next free number across all spec and security pages. Name the component, the
   subject it affects, whether it is live or dormant (and which requirement makes it live), and
   what makes it real.
6. A design choice with real alternatives gets a record in [decisions.md](../decisions.md).
7. Regenerate coverage.

## How coverage is produced

Coverage compares two machine-read inputs:

- the catalog: every `### <ID> · <Title>` heading in `docs/features/` with the `Priority:` line
  below it;
- the spec: every `Covers:` list on the `Priority:` line of a requirement in `docs/spec/`.

A catalog entry counts as *specified* when at least one requirement lists its ID, and *open*
otherwise. The generated output is [coverage/checklist.md](../coverage/checklist.md) plus the marked
tables in [coverage/README.md](../coverage/README.md). The full rules, including the checks that
stop generation, are in
[How the numbers are produced](../coverage/README.md#how-the-numbers-are-produced).

The generator is not committed to the repository yet, so nothing keeps the coverage pages current
when a heading or a `Covers:` line changes. Until it is, these commands, run from the repository
root, reproduce the raw comparison. They skip the format examples inside code fences:

```shell
t=$(mktemp -d)

# Catalog entries: "<ID> Priority: **<P>**", one per line
find docs/features -name '*.md' -exec awk '
  FNR == 1 { fence = 0 }
  /^```/   { fence = !fence; next }
  !fence && /^### [A-Z0-9-]+-[0-9]{3} · / { id = $2; getline; if ($0 ~ /^Priority:/) print id, $0 }
' {} + | LC_ALL=C sort > "$t/catalog.txt"

# Catalog IDs that at least one requirement covers
find docs/spec -name '*.md' -exec awk '
  FNR == 1 { fence = 0 }
  /^```/   { fence = !fence; next }
  !fence && /^Priority: .*Covers:/ { sub(/.*Covers:/, ""); print }
' {} + | grep -oE '[A-Z][A-Z0-9]*(-[A-Z0-9]+)*-[0-9]{3}' | LC_ALL=C sort -u > "$t/covered.txt"

# Uncovered entries with their priority, then covered IDs that do not exist in the catalog
LC_ALL=C join -v 1 "$t/catalog.txt" "$t/covered.txt"
LC_ALL=C join -v 2 "$t/catalog.txt" "$t/covered.txt"
```

## Repository layout

Today:

```text
trove/
├── LICENSE                 # Apache License 2.0
├── README.md               # user-facing overview, naming conventions, target quick start
├── SECURITY.md             # vulnerability reporting
└── docs/
    ├── README.md           # map of the documentation
    ├── features/           # feature catalog, IDs <PREFIX>-nnn
    │   └── formats/        # format matrix (FMT) and one page per format (PKG-<FORMAT>)
    ├── spec/               # target design, IDs R-<AREA>-nnn
    │   └── formats/        # npm.md, nuget.md
    ├── coverage/           # catalog entries covered by the spec, work list
    ├── decisions.md        # decision records
    ├── developer/          # this page
    └── security/           # security architecture
```

Planned, from [R-ARCH-003](../spec/architecture.md) and [R-ARCH-004](../spec/architecture.md)
(abbreviated; the spec has the full tree):

```text
trove/
├── go.mod                       # one module; go 1.27.3; generators as tool directives
├── cmd/trove/main.go            # subcommands serve, migrate, backup, admin; flag parsing only
├── api/openapi.yaml             # OpenAPI 3.1 source of truth, served as /api/v1/openapi.json
├── internal/                    # all Go code; no public Go API
│   ├── app/                     # composition root: build components per role, start/stop order
│   ├── config/                  # schema, YAML and environment loading, validation
│   ├── httpserver/              # listeners, routing, middleware, client address
│   ├── api/v1/                  # management API handlers
│   ├── auth/{oidc,session,token,ciexchange}/
│   ├── authz/                   # permission decisions
│   ├── repository/              # model, snapshot, path cleaning, virtual resolver
│   ├── formats/                 # plug-in contract, registry, conformance kit, npm/, nuget/
│   ├── proxy/                   # remote fetch engine
│   ├── blob/s3/                 # spool, digests, content-addressed objects
│   ├── meta/postgres/           # pgx pools, migrations, sqlc queries and generated code
│   ├── cluster/                 # the only Valkey client and key registry (keys.go), change log
│   │                            # and signal areas, FlightLock, shared counters, leases, lock categories
│   ├── backup/                  # trove backup: create, restore, table registry
│   ├── cache/  jobs/  audit/  outbound/  secrets/  errs/  observability/
│   ├── webui/                   # embeds the Angular build (dist/ has a committed placeholder)
│   └── archtest/                # fails on forbidden imports between layers
├── web/                         # Angular workspace; exact versions, .nvmrc
├── deploy/helm/trove/           # Helm chart: web and worker Deployments, backup CronJob
├── deploy/dev/                  # HA-shaped local profile, see below
├── test/integration/            # real PostgreSQL, PgBouncer, Valkey with Sentinel and SeaweedFS (S3) in containers
├── test/e2e/                    # kind + Helm + real clients (npm, pnpm, yarn, dotnet)
└── perf/                        # load scripts, reference lockfiles
```

The responsibility of each Go package and the import layering are in
[R-ARCH-002](../spec/architecture.md).

## Local development profile (planned)

The local profile is HA-shaped, so that failover of every dependency, node drains and a backup and
restore can be exercised on a laptop and in CI with the same files ([R-HA-051](../spec/ha-and-helm.md)).
It runs the real chart, not a single process. Files live in `deploy/dev/`; the tool versions (kind,
kubectl, Helm, openssl, the CloudNativePG operator chart, the PostgreSQL, Valkey and SeaweedFS
images) are pinned once in the repository's tool pins, and images are pinned by digest.

| Component | What runs | File |
|---|---|---|
| Cluster | kind, one control-plane and three worker nodes labeled with three zones, so two replicas per role can spread, drain and lose a node | `deploy/dev/kind.yaml` |
| trove | the chart with `trove-web` and `trove-worker` at two replicas each, `highAvailability.enforce: true`, and the backup CronJob every 6 hours | `deploy/dev/values-dev.yaml` |
| PostgreSQL | CloudNativePG `Cluster` `trove-db` with two instances (primary and standby), TLS with `verify-full` | `deploy/dev/postgres.yaml` |
| Pooler (optional, `dev-up POOLER=1`) | CloudNativePG `Pooler` resources `trove-db-pooler-rw` and `trove-db-pooler-control` in transaction mode, two instances each | `deploy/dev/pooler.yaml` |
| Valkey with Sentinel | StatefulSet `trove-valkey` (primary and replica) and StatefulSet `trove-valkey-sentinel` (three Sentinels, quorum 2, `down-after-milliseconds 5000`), spread over the three nodes, `emptyDir` only, TLS only, ACL user `trove`; the dev CA comes from `deploy/dev/valkey-certs.sh` and lives in `.dev/valkey/` | `deploy/dev/valkey.yaml` |
| S3 | single-process SeaweedFS, Service `trove-s3` on port 9000, buckets `trove-blobs` (versioning on), `trove-pg-archive` and `trove-backups` | `deploy/dev/s3.yaml` |

Every component is single-attach or ephemeral: the PostgreSQL instances use one ReadWriteOnce claim
each through CloudNativePG, Valkey and SeaweedFS use `emptyDir`, and nothing needs ReadWriteMany
([R-HA-061](../spec/ha-and-helm.md)).

Targets (the names are examples until the task runner is chosen):

| Target | What it does |
|---|---|
| `dev-up` / `dev-down` | Creates the kind cluster with everything above and port-forwards `svc/trove` to `localhost:8080` / deletes the cluster |
| `dev-failover` | Promotes the PostgreSQL standby (`kubectl cnpg promote trove-db <standby>`) |
| `dev-valkey-failover`, `dev-valkey-kill`, `dev-valkey-outage` | `SENTINEL FAILOVER trove`; force-deletes the Valkey primary pod; scales Valkey and the Sentinels to 0 for a given duration |
| `dev-s3-outage` | Scales the S3 store to 0 for a given duration |
| `dev-drain` | Runs variant A of the drain test of [R-HA-059](../spec/ha-and-helm.md) against a node the target picks |
| `dev-backup`, `dev-restore` | Creates a Job from the backup CronJob and waits for it; installs a second release into an empty namespace from the latest backup |
| `dev-chaos` | Runs the failure-injection suite of [R-HA-052](../spec/ha-and-helm.md) |

`trove serve --role all` from an IDE uses the same dependencies through `kubectl port-forward`. It
is a convenience, not a tested profile.

## Test tiers (planned)

| Tier | Where | Scope | Spec |
|---|---|---|---|
| Unit | next to the code | Package logic; the token format test vector | R-AUTH-022 |
| Architecture | `internal/archtest` | Forbidden imports between layers | R-ARCH-002 |
| Format conformance | `internal/formats/formattest` | Routing, path rejection, classification, rewrite and merge determinism, limits, content headers, client snippet templates | R-ARCH-010 |
| Integration | `test/integration` | Real PostgreSQL, PgBouncer, Valkey with Sentinel (newest patch of each supported line, 9.0 and 9.1 today) and SeaweedFS (S3) in containers on every change; MinIO and AWS S3 nightly | R-ARCH-003, R-ARCH-031, R-HA-051 |
| Chart | CI | `helm lint`, `helm template` per value set, schema validation; no `ReadWriteMany`, no PersistentVolumeClaim, no StatefulSet, no Secret, never `--role all` | R-HA-002, R-HA-028, R-HA-029, R-HA-061 |
| End to end | `test/e2e` | kind, the real chart, and npm, pnpm, Yarn and `dotnet` against local, remote and virtual repositories; the client suite also with Valkey stopped | R-ARCH-010, R-ARCH-030, R-NPM-045, R-NUGET-027 |
| Failure injection | local profile in CI | Every failure mode of R-HA-007 under real client load, including PostgreSQL switchover, Valkey failover, outage and content loss, object-store outage, node loss and backup under load | R-HA-052, R-HA-057 |
| Node drain | local profile in CI | Variant A on every change to `main`; variants B to E (Valkey primary, PostgreSQL primary, pooler instance, running backup pod on the drained node) nightly | R-HA-059 |
| Backup and restore | `test/integration`, kind | Consistency under load, overlapping runs, drain and crash during backup and restore, revocations surviving a restore; the restore drill nightly | R-BAK-029, R-HA-052 |
| UI | `web/` | Unit, component, contract, end-to-end, accessibility, internationalization and security tiers | R-UI-047 |
| Performance | `perf/` | Proposed targets, not measurements | R-ARCH-027 |
