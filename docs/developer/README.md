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
- **Owner constraints are fixed.** Go 1.27.3, Angular 22, PostgreSQL, S3, high availability, Helm,
  OIDC login, tokens, detailed permissions, GitLab CI OIDC, pull-through proxying, and npm and NuGet
  first. They are listed in [spec/README.md](../spec/README.md#owner-constraints) and are not up for
  redesign.
- **Invariants `INV-1` to `INV-17` bind every spec page.** A page may refine an invariant but must
  not contradict it. If an invariant looks wrong, keep it and raise an open question
  `Q-<AREA>-n` on the page. Every invariant becomes a record in [decisions.md](../decisions.md).
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
| This page | Entry point: ID schemes, adding catalog entries and requirements, coverage, planned layout and test tiers |

Topic pages are created together with the code they describe. Until then their design lives in the
spec:

| Future topic | Design today |
|---|---|
| Architecture: components, request paths, startup | [spec/architecture.md](../spec/architecture.md) |
| Build, test and lint matrix | [spec/architecture.md](../spec/architecture.md) R-ARCH-003, R-ARCH-004; no task runner chosen yet |
| Local development profile | [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-051 |
| Adding a package format | [spec/architecture.md](../spec/architecture.md) R-ARCH-008 to R-ARCH-010 |
| CI and release: images, chart, signatures | [spec/ha-and-helm.md](../spec/ha-and-helm.md) R-HA-047; publishing location still open (Q-HA-11) |

## ID schemes

| ID | Where it lives | Rule |
|---|---|---|
| `<PREFIX>-nnn` | [docs/features/](../features/README.md), one prefix per page (page table there) | Three digits; `### <ID> · <Title>`, then `Priority:` |
| `FMT-nnn` | [features/formats/README.md](../features/formats/README.md) | Cross-format behavior |
| `PKG-<FORMAT>-nnn` | [docs/features/formats/](../features/formats/README.md), one prefix per format page | The prefix is a short form shown by the page's first entry, e.g. `alpine.md` uses `PKG-APK`, `rubygems.md` uses `PKG-GEMS` |
| Priority | Every catalog entry and requirement | `MVP`, `P1`, `P2`, `Later`, `Out of scope`; rubric in [features/README.md](../features/README.md#priority-rubric) |
| `INV-n` | [spec/README.md](../spec/README.md#design-invariants) | Design invariants |
| `R-<AREA>-nnn` | [docs/spec/](../spec/README.md), one prefix per page (page table there) | `### R-<AREA>-nnn · <Title>`, then `Priority: **<P>** · Covers: <catalog IDs>` or `Covers: trove-only` |
| `Q-<AREA>-n` | `## Open questions` of each spec page | Each with options and a recommendation |
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
   becomes an open question `Q-<AREA>-n` under `## Open questions`, with options and a
   recommendation.
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
├── cmd/trove/main.go          # subcommands serve, migrate, admin; flag parsing only
├── api/openapi.yaml             # OpenAPI 3.1 source of truth, served as /api/v1/openapi.json
├── internal/                    # all Go code; no public Go API
│   ├── app/                     # composition root: build components, start/stop order
│   ├── config/                  # schema, YAML and environment loading, validation
│   ├── httpserver/              # listeners, routing, middleware, client address
│   ├── api/v1/                  # management API handlers
│   ├── auth/{oidc,session,token,ciexchange}/
│   ├── authz/                   # permission decisions
│   ├── repository/              # model, snapshot, path cleaning, virtual resolver
│   ├── formats/                 # plug-in contract, registry, conformance kit, npm/, nuget/
│   ├── proxy/                   # remote fetch engine
│   ├── blob/s3/                 # spool, digests, content-addressed objects
│   ├── meta/postgres/           # migrations, sqlc queries and generated code
│   ├── cluster/                 # LISTEN/NOTIFY, leases, lock categories
│   ├── cache/  jobs/  audit/  outbound/  secrets/  errs/  observability/
│   ├── webui/                   # embeds the Angular build (dist/ has a committed placeholder)
│   └── archtest/                # fails on forbidden imports between layers
├── web/                         # Angular workspace; exact versions, .nvmrc
├── deploy/helm/trove/         # Helm chart
├── deploy/dev/                  # kind, CloudNativePG, MinIO, values-dev.yaml
├── test/integration/            # real PostgreSQL and MinIO in containers
├── test/e2e/                    # kind + Helm + real clients (npm, pnpm, yarn, dotnet)
└── perf/                        # load scripts, reference lockfiles
```

The responsibility of each Go package and the import layering are in
[R-ARCH-002](../spec/architecture.md).

## Test tiers (planned)

| Tier | Where | Scope | Spec |
|---|---|---|---|
| Unit | next to the code | Package logic; the token format test vector | R-AUTH-022 |
| Architecture | `internal/archtest` | Forbidden imports between layers | R-ARCH-002 |
| Format conformance | `internal/formats/formattest` | Routing, path rejection, classification, rewrite and merge determinism, limits, content headers, client snippet templates | R-ARCH-010 |
| Integration | `test/integration` | Real PostgreSQL and MinIO in containers | R-ARCH-003 |
| End to end | `test/e2e` | kind, the real chart, and npm, pnpm, Yarn and `dotnet` against local, remote and virtual repositories | R-ARCH-010, R-NPM-045, R-NUGET-027 |
| Failure injection | local profile in CI | Every failure mode of R-HA-007 under real client load | R-HA-052 |
| UI | `web/` | Unit, component, contract, end-to-end, accessibility, internationalization and security tiers | R-UI-047 |
| Performance | `perf/` | Proposed targets, not measurements | R-ARCH-027 |
