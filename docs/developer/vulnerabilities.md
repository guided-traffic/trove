# Vulnerability scanning: contracts

Status: the contracts of beta phase 6 (M4b) are laid: the shared types and packages, the ports, the format contract's addition, the
schema, the configuration, the capability, the errors, the audit events, the metrics, the OpenAPI document with its handler stubs, and the
places in the composition root where each work package registers its parts. No work package is built. The specification is
[spec/vulnerability-scanning.md](../spec/vulnerability-scanning.md) (R-VULN-001 to R-VULN-029; R-VULN-030 to R-VULN-032 are P1 and not built), the
roadmap entry [M4b](../coverage/roadmap.md#m4b--vulnerability-scanning), the plan [projektplan/BETA.md](../projektplan/BETA.md). The decisions
behind the contracts are [Q-VULN-23 to Q-VULN-27](../spec/vulnerability-scanning.md#q-vuln-23--the-package-layout-of-the-scanning-and-the-leaf-move-of-the-untrusted-content-limits)
and [ADR-150](../decisions.md#adr-150--the-format-contract-gains-the-optional-vulnerability-adapter).

The page is the contract between the eight work packages that build the scanning in parallel, each in a git worktree of its own, in the shape of
[ci-oidc.md](ci-oidc.md).

## What has to be in your head

- **Trove matches identities, not bytes.** A stored version is a row (repository, ecosystem, normalized name, normalized version); a finding says that
  one canonical advisory affects it; no blob is read (R-VULN-001). Advisory data is the OSV archive of each served ecosystem mirrored into PostgreSQL;
  findings are rows too, and changes of either are re-evaluated by jobs that read their work from tables, not from job arguments (Q-VULN-18).
- **Leaves below, a vocabulary root, machinery above.** The pure parts are leaves with no I/O (`advisory`, `match`, `osv`, `metrics`; and
  `internal/formats/untrusted`, moved to the leaf layer). [internal/vuln](../../internal/vuln) is the shared domain vocabulary (statuses, the
  repository member, the policy and the verdict, the ports, the job names) and imports no other domain package, so that `internal/repository` can decode
  its member and every work package can import it; its ports speak ids, keys and names, never a `repository.Repository` or an `items` type (Q-VULN-23).
- **The format contract grows by one optional interface** ([ADR-150](../decisions.md#adr-150--the-format-contract-gains-the-optional-vulnerability-adapter)):
  `formats.VulnerabilityAdapter` (npm and NuGet implement it, work package B), found by `CapabilitiesOf`; and one Host service, `Host.Vulnerabilities()`,
  through which a format's audit route reads the scanning's data. The scanning's domain never imports the contract: `match.Adapter` has the same method set
  and the adapter is taken by structural typing.
- **The block check is one query.** A download keeps the one-query budget: the verdict is read in the item lookup with the repository's policy as
  parameters, and applied by `vuln.Verdict` (R-VULN-015). Everything else that is per request answers from a per-replica copy (`States`) or the claim index.
- **PostgreSQL decides, a cache accelerates.** The advisory tables are transient in backups, the findings are data (R-VULN-026). Until the first complete
  import of an ecosystem no evaluation resolves a finding (R-VULN-011), so a restore keeps its blocks.
- **Every file has one owner.** The tables below name it. A change to a file in the last section is a contract change: say so in the review.
- **No attribution, no vendor names** in anything the work packages write; the decider of design questions is "the development agent".

## The contracts

### Packages and layers

| Package | Layer | Owner | Content |
|---|---|---|---|
| [internal/vuln/advisory](../../internal/vuln/advisory) | leaf | contract | `Advisory`, `Affected`, `Range`, `Event` (JSON as OSV writes it), `Severity` with `Rank` (critical 5, high 4, medium 3, low 2, none 1, unknown 0), `Kind`, `SeveritySource`, `CVSS`, `DataState`; `SourceOf(id)` (R-VULN-003), `URLOf` (the advisory URL rule of R-VULN-017, used by the NuGet page too), `NPMAuditID` and `NPMAuditIDs` (Q-VULN-17: the first 53 bits of SHA-256 of `npm:<id>`, a collision takes `npm:<id>:1`) |
| [internal/vuln/match](../../internal/vuln/match) | leaf | contract (`match.go`), B (`affects.go`) | `Adapter` (the method set of `formats.VulnerabilityAdapter`), `Result`, `MatcherVersion`; `Affects(a, entry, version)`: the OSV algorithm over the format's order, with a first implementation and its table test that B owns, extends with the corpus and hardens |
| `internal/vuln/osv` | leaf | A | OSV record decode, validation and normalization (R-VULN-006), aliases and the canonical identifier, severity with `github.com/pandatix/go-cvss` (R-VULN-007), the KEV catalog decode (R-VULN-008); imports `advisory`, `match`, `formats/untrusted`, `errs` |
| [internal/vuln/metrics](../../internal/vuln/metrics) | leaf | contract | the names, label values and collectors of R-VULN-024 (`New`), the five gauge descriptors (`GaugeDescs`) |
| [internal/formats/untrusted](../../internal/formats/untrusted) | leaf | as before | moved from the edge layer to the leaf layer; it imports only `errs` (Q-VULN-23) |
| [internal/vuln](../../internal/vuln) | domain | contract | `Status` and `CheckTransition`, `DismissalReason`, `ResolvedReason`, `Change`, `BlockReason`, `Evaluation`, `Trigger`; `Settings` (the repository member), `Policy`, `PolicyFor`, `BlockRow`, `Block`, `Verdict`; the ports `States`, `Evaluator`, `Advisories`; the job kinds, tasks and `SyncArgs` ([jobs.go](../../internal/vuln/jobs.go)); the cache names and `DataChange` ([coordination.go](../../internal/vuln/coordination.go)) |
| [internal/vuln/store](../../internal/vuln/store) | domain | all, one file each | the sqlc wrappers of the `vuln_*` tables ([convention](#queries)) |
| `internal/vuln/sync` | domain | A | the sync jobs online and offline, KEV, the source state, the guards, the checkpoint, `States` |
| `internal/vuln/evaluate` | domain | B | the commit-hook and remote-miss evaluation, `vuln-evaluate`, `vuln-rescan`, the lifecycle, claimed-name matches, the retention task |
| `internal/vuln/triage`, `internal/vuln/report` | domain | E | status changes and their events; the list, detail, summary, trend, export, advisory and sources queries, narrowed by permission |

The architecture test ([layers.go](../../internal/archtest/layers.go)) holds the layers; [imports.go](../../internal/archtest/imports.go) lets a format package import
`internal/vuln/advisory` and `internal/vuln/match` and no other package of the scanning.

### Ports

Declared in [internal/vuln/ports.go](../../internal/vuln/ports.go); the composition root wires them ([wire_vuln.go](../../internal/app/wire_vuln.go)); a nil port is a
replica that has not wired the scanning, and every consumer reads it that way.

| Port | Implemented by | Consumed by | Contract |
|---|---|---|---|
| `States` | A (`sync/states.go`) | the Host, C (the block check), D, G | `State(ecosystem) DataState{Complete, Stale, SyncedAt, Epoch}` from the per-replica cache `vuln_source_state` (area `vuln_data`, safety TTL 60 s); no query per call; an unknown ecosystem is the zero value (not complete) |
| `Evaluator` | B (`evaluate`) | the item store's commit hook, C (remote miss) | `OnVersionCommitted(ctx, tx, CommittedVersion{RepoID, Format, ItemID, Trigger})` in the commit transaction after the format's `OnCommit` linked the item to its version (the version is the item's `items.version_id`); `OnVersionsRemoved(ctx, tx, versionIDs)`; `EvaluateMiss(ctx, Miss{RepoKey, Format, Name, Version}) (Block, error)` before a fetch, reading the repository's snapshot itself |
| `Advisories` | D (store plus evaluate helpers) | the Host, hence the formats | `ForNames` (one query, non-withdrawn), `All` (the NuGet page), `InternalNames` (the claims rule of R-VULN-010 from the claim index) |

The seam of the commit hook is `items.Hooks`: B writes a decorator (`evaluate.Hooks(inner, evaluator)`) and enables it in `vulnArea.hooks`, which `wireFormats` already calls.
The seam of the repository's settings change is `repository.Hooks.OnUpdated(ctx, tx, caller, before, after)`: `after.VulnerabilitySettings()` and `before.VulnerabilitySettings()`
(with `Settings.RescanInputsDiffer`) and the upstream URL tell B whether to enqueue a `vuln-rescan` in the same transaction (R-VULN-002).

### The download policy

`repository.Repository` carries the member and the ecosystem from the replica's snapshot: `VulnerabilitySettings()` (defaults for an absent member),
`VulnerabilityEcosystem()` ("" for a virtual repository or a format without the adapter), `VulnerabilityPolicy(installationEnabled, enforcement) vuln.Policy`.
`Policy.QueryEnforce()` and `QueryBlockMalicious()` are the `$3` and `$4` of the lateral join of R-VULN-015 (the join runs while the repository is scanned, also in the mode
`report`, so that the would-be block can be counted); `vuln.Verdict(policy, BlockRow, DataState)` decides: a blocking row, else `missing_data` when `onMissingData` is `block` and the
ecosystem is not complete, else nothing; `Block.Refused()` is false in the mode `report`. `Block.Code()` is the problem code (`vulnerability_blocked` or `vulnerability_data_missing`).
`Policy.BlockReasonOf(kind, status)` is the same rule for the API's `blocked` and `blockReason`, so the report and the download agree.

### Format contract

[internal/formats/vulnerability.go](../../internal/formats/vulnerability.go): `VulnerabilityAdapter` (R-VULN-009 verbatim), `VulnerabilityData` (`State`, `ForNames`, `All`,
`InternalNames`, `Affects`, `CountAudit`) and `NoVulnerabilityData()`. `Capabilities.Vulnerability` is filled by `CapabilitiesOf`; `formats.Register` derives `catalog.Entry.Ecosystem` from it.
`Host.Vulnerabilities()` is implemented by [formathost/vulnerability.go](../../internal/formathost/vulnerability.go) over `formathost.VulnPorts{States, Advisories, Audit}` (nil-safe: not complete, and
`503 vulnerability_data_unavailable` for a read) and by the conformance kit's in-memory host (`HostOptions.Vulnerabilities`). `proxy.Request.Version` is filled from `Classify` in
[formathost/remote.go](../../internal/formathost/remote.go). The spec blocks of R-ARCH-008 and `internal/formats/testdata/contract.txt` are updated.

### Schema

| File | What |
|---|---|
| [00035_vulnerabilities.sql](../../internal/meta/postgres/migrations/00035_vulnerabilities.sql) | the eight tables exactly as the spec page has them (`vuln_source_state`, `vuln_advisory`, `vuln_affected`, `vuln_pending_evaluation`, `vuln_kev`, `vuln_claimed_match`, `vuln_finding`, `vuln_finding_event`), plus `vuln_advisory.cwe_ids` and the quoted column `"references"` (Q-VULN-24), and the epoch row of `vuln_data` |
| [00036_vulnerabilities_packages_name.sql](../../internal/meta/postgres/migrations/00036_vulnerabilities_packages_name.sql) | `CREATE INDEX CONCURRENTLY packages_name ON packages (name)`, a step of its own (R-HA-018, Q-VULN-24) |
| [tables_vuln.go](../../internal/meta/postgres/tables_vuln.go) | export classes (`vuln_finding` and `vuln_finding_event` data, the other six transient) and the revision column `vuln_finding.revision`; the package `meta/postgres` is linked by every binary, so [cmd/trove/registry.go](../../cmd/trove/registry.go) needs no line |

Tested by [TestVulnMigration](../../test/integration/postgres/vuln_test.go) (classes, steps, the seeded area, the constraints, the cascade).

#### Queries

One sqlc file per work package in [internal/meta/postgres/queries](../../internal/meta/postgres/queries), so that no two packages edit one file; they exist empty and sqlc accepts them:
`vuln_sync.sql` (A), `vuln_evaluate.sql` (B), `vuln_enforce.sql` (C), `vuln_audits.sql` (D), `vuln_api.sql` (E: triage and report), `vuln_ops.sql` (G). The wrappers are
`internal/vuln/store/<package>.go`. Plain Go types only (no `pgtype`): COALESCE nullable columns. Run `make generate` and commit the generated `q/vuln_*.sql.go`.
The change to `GetLiveItem` of [files.sql](../../internal/meta/postgres/queries/files.sql) (the lateral join) is C's, a contract change of a shared file.

### API

Every operation of R-VULN-011, R-VULN-020 and R-VULN-021 is in [api/openapi.yaml](../../api/openapi.yaml) (tag `vulnerabilities`, between `# >>> M4b vulnerabilities` markers in the tags, the
paths, the responses and the schemas), with its `x-trove-permission` (R-VULN-019, Q-VULN-27): the reads of findings, advisories, summary, trend, export and the package views are
`authenticated: true` and narrowed per finding in SQL by the handler; the status changes need `vulnerabilities:triage` (and `read` on the finding, checked in the handler, `404` otherwise); the sources need
`settings:read`; the claimed-name matches `repositories:read-config`; the rescan is `manage` on the repository. The repository document has the member `vulnerabilities`
(`VulnerabilitySettings`), `GET /system/info` the block `vulnerabilityData` (`VulnerabilityDataFacts`, not required), and `TaskRunRequest` documents `acceptShrink`.

| Operation | Path | Package |
|---|---|---|
| `listVulnerabilityFindings`, `exportVulnerabilityFindings` | `GET /vulnerabilities/findings`, `…/findings/export` | E |
| `getVulnerabilityFinding`, `updateVulnerabilityFinding` (merge patch, `If-Match`), `setVulnerabilityFindingsStatus` | `…/findings/{id}`, `POST …/findings/set-status` | E |
| `getVulnerabilityAdvisory` | `GET /vulnerabilities/advisories/{ecosystem}/{id}` | E |
| `getVulnerabilitySummary`, `getVulnerabilityTrend` | `GET /vulnerabilities/summary`, `…/trend` | E |
| `listVulnerabilitySources`, `listClaimedNameMatches` | `GET /vulnerabilities/sources`, `…/claimed-name-matches` | E |
| `getPackageVersionVulnerabilities`, `getPackageVulnerabilities` | `GET /packages/{packageId}/versions/{version}/vulnerabilities`, `…/{packageId}/vulnerabilities` | E |
| `rescanRepositoryVulnerabilities` | `POST /repositories/{key}/vulnerabilities/rescan` (`202` with the job, `409`) | E (the job: B) |
| `vulnerabilityData` of `getSystemInfo` | `Facts` of [handlers.go](../../internal/api/v1/handlers.go) | G |

The generated interface is implemented by the `vulnerabilities*_api.go` files of [internal/api/v1](../../internal/api/v1) (work package E replaced the 503 stubs of Q-VULN-25); a replica that has not wired the scanning still answers 503 (`VulnServices` zero). When E builds an operation it removes nothing from the document but the
503-only state of its handler. The filters of the list are the document's query parameters, the sorts its `sort` enum; the permission narrowing in SQL is E's design.

### Configuration, coordination, audit, metrics, errors

| Where | What |
|---|---|
| [config/vulnerabilities.go](../../internal/config/vulnerabilities.go) | the section `vulnerabilities` exactly as R-VULN-025 with its defaults and `doc` tags, `EffectiveEcosystems()`, `VulnerabilityEcosystems()` (the fixed list `npm`, `NuGet`; A reads the format registry through the catalog when it can, and then deletes the list); refusals: `max_shrink` outside 0 to 1, an ecosystem without an adapter, `mode: repository` without `repository`, a base URL that is not https (the key is named), `epss` other than `off`. The four tasks are in the default of `maintenance.tasks` ([maintenance.go](../../internal/config/maintenance.go)), and [vuln.TaskDefaults](../../internal/vuln/jobs.go) holds the same schedules (a test holds them equal) |
| [cluster/areas.go](../../internal/cluster/areas.go) | the change area `vuln_data` (owner R-VULN-027, payload `vuln.DataChange{ecosystem, epoch}`, version 1); the caches `vuln_source_state` (A, safety TTL 60 s) and `vuln_nuget_page` (D) are registered by their owners, their names are `vuln.CacheSourceState` and `vuln.CacheNuGetPage` |
| [app/wire_vuln.go](../../internal/app/wire_vuln.go) | the step `wireVuln` (area `vuln`, every role, listed after `ci`): the metrics, `vulnArea{states, evaluator, advisories, metrics}`, marked blocks `WP-A`, `WP-B`, `WP-D` for the ports, `registerVulnJobs` with the blocks `WP-A` and `WP-B` for the kinds and tasks (no kind is registered without its handler), `vulnArea.hooks`, `a.vulnTaskSpec(td)` for the configured schedule. The Host takes the ports in `wireFormatHost`; `wire_api.go` has the task arguments `{"acceptShrink": bool}` of `vuln-sync-osv` and `vuln-sync-osv-full` |
| [audit/events_vuln.go](../../internal/audit/events_vuln.go) | the six events of R-VULN-024 with schemas (`vulnerability.finding.status_changed`, `.reopened`, `vulnerability.download_blocked`, `.claimed_name_match`, `.source.failed`, `.source.imported`); none is journaled yet: E adds the journal flag with an optional entry to the two finding events that set a block, with the feed mapping of [bastionmain/feed.go](../../internal/bastionmain/feed.go) and its classification test (R-BAS-015) |
| [errs/codes.go](../../internal/errs/codes.go) | `>>> M4b vulnerabilities`: `vulnerability_blocked`, `vulnerability_data_missing`, `vulnerability_data_unavailable`, `finding_not_confirmed`, `finding_resolved`, `status_not_settable`, `export_too_large`, and the codes of a failed sync run `archive_too_large`, `archive_digest_mismatch`, `archive_limit`, `too_many_invalid_records`, `shrink_refused`, `mirror_repository_invalid` (`egress_denied` and the upstream codes exist) |
| [authz](../../internal/authz/authz.go) | the capability `vulnerabilities:triage` for `admin`, `auditor` and `security-admin`, a system scope item ([vulnerabilities_triage_test.go](../../internal/authz/vulnerabilities_triage_test.go) pins the role sets; Q-VULN-27) |
| [observability/labels.go](../../internal/observability/labels.go), [vuln/metrics](../../internal/vuln/metrics/metrics.go) | the allowed labels gain `ecosystem` and `severity`; the twelve families of R-VULN-024 are defined once, the counters by `metrics.New`, the five gauges read from PostgreSQL by `GaugeDescs` (registered by G as a database gauge source, aggregated with max) |

## Work packages

| Package | Builds | Files it owns |
|---|---|---|
| **S** test advisory source | the HTTPS double of R-VULN-029: fixture archives, change files, single records, the KEV catalog, `ETag`, `304`, a `Range` switch and the faults | `test/osvstub/**`, `deploy/dev/osvstub.yaml`, the switch in `deploy/dev/e2e.sh` |
| **A** advisory data | R-VULN-003 to R-VULN-008, the validation of R-VULN-025 against the registry; `States` | `internal/vuln/osv/**`, `internal/vuln/sync/**`, `internal/vuln/store/sync.go`, `queries/vuln_sync.sql`, the `WP-A` blocks of `wire_vuln.go`, the manual page of the sources |
| **B** matching and findings | R-VULN-009 to R-VULN-014: the adapters, the corpus, the evaluation, the lifecycle, claimed-name matches, the retention; the `Evaluator` | `internal/vuln/match/affects.go` and its tests and `testdata`, `internal/vuln/evaluate/**`, `internal/vuln/store/evaluate.go`, `queries/vuln_evaluate.sql`, `internal/formats/npm/vuln.go`, `internal/formats/nuget/vuln.go`, the `WP-B` blocks of `wire_vuln.go`, the repository hook for the rescan |
| **C** enforcement | R-VULN-015, R-VULN-016: the lateral join, `items.Live` with `VulnBlock`, the verdict in `formathost` and `files`, the refusal in the formats' shapes, the remote miss | `queries/vuln_enforce.sql`, `GetLiveItem` of `queries/files.sql`, `internal/items` (the lookup), `internal/files` and `internal/formathost` (the verdict after `ServeCheck`), the refusal renderers of `internal/formats/npm/errors.go` and `internal/formats/nuget/errors.go`, `internal/proxy` and its caller (the miss), `wireFiles` and `wireProxyEngine` (they take `vulnArea`) |
| **D** client audits | R-VULN-017, R-VULN-018: npm's `local` audit mode, NuGet's `VulnerabilityInfo` page and the cache `vuln_nuget_page`; the `Advisories` port | `queries/vuln_audits.sql`, `internal/vuln/store/audits.go`, `internal/formats/npm/handlers_audit.go`, the NuGet vulnerability handlers and documents, the `WP-D` block of `wire_vuln.go` |
| **E** API and triage | R-VULN-013, R-VULN-019 to R-VULN-021 | `internal/vuln/triage/**`, `internal/vuln/report/**`, `queries/vuln_api.sql`, `internal/vuln/store/api.go`, `internal/api/v1/vuln_api.go` (replaces the stubs), the journal flag of the two finding events |
| **F** UI | R-VULN-022, R-VULN-023 | `web/**` (the section, the package tab, the repository form) |
| **G** operations | R-VULN-024, R-VULN-026 to R-VULN-028, the chart and the security pages | `queries/vuln_ops.sql`, the gauges and the `vulnerabilityData` facts, `deploy/helm/**` (the alerts, the spool check), `docs/security/` pages, the manual pages of operations |
| **H** end-to-end | R-VULN-029's integration and e2e tiers | `test/integration/vuln/**`, `test/e2e/**` (the scanning's tests) |

Order: S, A and B in parallel first (B tests against fixtures until A's data exists), then C and D (both need B's adapters), E after B, F after E's document (it already has the types), G
throughout, H last. A and B meet at `advisory`, `match` and the tables; C at the policy and `Verdict`; D at `Advisories`; E at `report` over C's `blocked` rule (`Policy.BlockReasonOf`).

### Per-package sections

Each work package adds its section here, in the shape of [ci-oidc.md](ci-oidc.md): Files, what the others rely on, contract changes, tests. Numbers it may use: Q-VULN 28 to 29 (S), 30 to 34 (A), 35 to 39 (B), 40 to 43 (C),
44 to 47 (D), 48 to 51 (E), 52 to 54 (F), 55 to 58 (G), 59 to 60 (H); H-numbers 477 to 479 (A), 480 to 482 (B), 483 to 485 (C), 486 to 487 (D), 488 to 489 (E), 490 (F), 491 to 493 (G), 494 (H).

#### D · Client audits (R-VULN-017, R-VULN-018)

What a client's audit reads of the scanning: npm's `local` mode of the bulk endpoint and NuGet's `VulnerabilityInfo/6.7.0`. Decisions:
[Q-VULN-44 to Q-VULN-47](../spec/vulnerability-scanning.md#q-vuln-44--how-the-format-learns-the-installations-default-for-the-audit-modes-and-what-an-explicit-mode-means-on-a-class);
the security view is [security/vulnerability-scanning.md](../security/vulnerability-scanning.md).

#### E · API and triage
Files: [internal/vuln/report](../../internal/vuln/report) (the read side), [internal/vuln/triage](../../internal/vuln/triage) (the status changes),
[internal/vuln/store/api.go](../../internal/vuln/store/api.go) and [queries/vuln_api.sql](../../internal/meta/postgres/queries/vuln_api.sql) (the statements),
[vulnerabilities_api.go](../../internal/api/v1/vulnerabilities_api.go), [vulnerabilities_views_api.go](../../internal/api/v1/vulnerabilities_views_api.go) (the handlers, the rescan,
the `vulnerabilityData` block of `GET /system/info`), [wire_vuln_api.go](../../internal/app/wire_vuln_api.go) (the step `vuln-api`, which takes `vulnArea` for the
advisories port), tests in [test/integration/vulnapi](../../test/integration/vulnapi) and next to the packages.
How it works:
| Part | What it does |
|---|---|
| `report.Service.Scope` / `PackageScope` | classifies the physical repositories with `authz.ReadScope`; a repository read whole goes to the statements as `whole`, the versions read in part as `rows` (`repo\x1fname\x1fversion`), decided once per distinct version; the package views load the stored versions of the package as candidates ([Q-VULN-50](../spec/vulnerability-scanning.md)) |
| `queries/vuln_api.sql` | the filter block is generated into `VulnFindingsList` and `VulnFindingsCount` (the same text); the sort key is `(k_num, k_txt, id)` compared bytewise, so the cursor is `[k_num, k_txt, id]`; `blocked`/`blockReason` filters take the repositories' policy as arrays (`enabled_repos`, `malicious_repos`) |
| block of a finding | `vuln.Verdict` over `Repository.VulnerabilityPolicy`, in every mode (Q-VULN-49); a version's verdict adds `missing_data` |
| `triage.Service.Apply` | locks the readable ids in id order, `vuln.CheckTransition`, history row, audit event with `block` set or lifted, journal entry on `set`; per-id results, one bulk id |
| trend | two aggregate queries (detections and resolutions per day and severity, days before the range summed into the day before) and a running sum in Go |
| export | `Count` against `export.max_rows` first (`422 export_too_large` before any byte), then keyset pages of 500; `csvSafe` of the audit export |
| rescan | enqueues `vuln-rescan` with `vuln.RescanArgs` under the dedup key `vuln.RescanKey`; `409` carries the running job; it needs the kind registered by work package B |
Contract changes of E: `VulnerabilityVersionBlockReason` (`missing_data`) for the version report; `detailsHtml` on the advisory and `repository` on the source item
(Q-VULN-51); `vuln.RescanArgs` in `internal/vuln/jobs.go`; the journal flag with an optional entry on the two finding events and the two names in `audit` shared types
(Q-VULN-48); the `vuln-api` step in `steps.go` and the `Vuln` option of the API. The integration tests that touch a block or an evaluation state need the npm and
NuGet adapters of work package B (a repository's ecosystem comes from them).
Tests: `TestR_VULN_013_*`, `TestR_VULN_019_*`, `TestR_VULN_020_*`, `TestR_VULN_021_*` in `test/integration/vulnapi` (the `@team-a/*` narrowing in list, counts, trend and
export, the auditor, the scoped service-account token, 412, the bulk results, the scatter paging with three replicas, the journal row). Not reached at this tier: the
`403` of an auditor on the download (the file service's), the rescan's `202` and `409` (the kind is B's), and the criteria that need the evaluation to write findings.

#### WP-A · Advisory data (R-VULN-003 to R-VULN-008, the sync half of R-VULN-024, R-VULN-026 and R-VULN-027)
The decisions are [Q-VULN-30 to Q-VULN-34](../spec/vulnerability-scanning.md#q-vuln-30--a-full-sync-validates-before-it-writes-in-two-passes), the gaps
[H-477 to H-479](../security/vulnerability-scanning.md#what-this-does-not-cover), the operator's page is [the manual](../manual/admin/vulnerability-sources.md).

### F · The UI (R-VULN-022, R-VULN-023)

Q-VULN-52 to Q-VULN-54, H-490. The description of the screens, their files and specs is in [web-ui.md](web-ui.md#the-vulnerability-screens); the manual page is
[user/vulnerabilities.md](../manual/user/vulnerabilities.md) (registered in [internal/manual/manual.go](../../internal/manual/manual.go) and in the contents of
[docs/manual/README.md](../manual/README.md)); the security view is [security/vulnerability-scanning.md](../security/vulnerability-scanning.md).

- **Files.** `web/src/app/features/vulnerabilities/**`, `web/src/app/api/vulnerabilities-api.ts` (and its spec, and the fake in `api/testing/fakes-vulnerabilities.ts`),
  `core/http/problem-messages-vulnerability.ts`, `web/src/styles/_vulnerabilities.scss` and the `--trove-chart-*` tokens, the tab, the badge column and the header of
  `features/packages/package-detail.*`, the links of `features/repositories/repository-list.html` and `repository-detail.html`, the route, navigation, help topic and provider blocks marked
  `>>> M4b vulnerabilities`, the German targets in `web/src/locale/messages.de.xlf`, and `web/e2e/vuln/**`.
- **What the others rely on.** The generated types of the contract commit (`schema.d.ts`, regenerated with `npm run generate:api`; the UI compiles against them). The UI reads only the
  API: no screen computes a count, a block or a status the API did not send. The handlers of work package E answer 503 until they are built; the package page then shows no
  tab, no badge and no banner, and the report shows the error notice with its retry.
- **Contract changes.** None to the document. Two small additions to shared web code: `ReadOptions.accept` of `ApiClient` (the export asks for `text/csv`), and `trove-chart-*` tokens
  with their pairs in `palette.spec.ts`. The manual page needed a line in `internal/manual/manual.go` (the contents are fixed there) and in `docs/manual/README.md`.
- **Left to the e2e wave.** Running `web/e2e/vuln/15-vulnerabilities.spec.ts` in the scatter profile with the test advisory source and `TROVE_E2E_VULN=1` (and the Go harness that
  sets it), the acceptance criterion "the same journey across three `web` replicas with random distribution" (the journey asserts two replicas answered), and the browser tier's axe
  run of the German dark scheme on the vulnerability screens against a real backend (it was run against a mocked API, see web-ui.md).

### G · Operations

R-VULN-024 (the gauges, the alerts, the facts), R-VULN-026 (the restore), R-VULN-027 (nothing built: the page is documentation, the
state is the other packages'), R-VULN-028 (the performance run) and the chart's part of R-VULN-025. Decisions:
[Q-VULN-55 to Q-VULN-58](../spec/vulnerability-scanning.md#q-vuln-55--the-alerts-are-scoped-to-the-releases-namespace-and-the-stale-threshold-is-vulnerabilitiesstale_after);
gaps: H-491 to H-493 on [the security page](../security/vulnerability-scanning.md).


**Files**

| File | Responsibility |
|---|---|
| [queries/vuln_ops.sql](../../internal/meta/postgres/queries/vuln_ops.sql) | three read statements: `VulnOpsSourceStates` (the rows of `vuln_source_state`, `synced_at` as epoch seconds, 0 and `has_synced` false for none), `VulnOpsFindingCounts`, `VulnOpsClaimedMatchCounts` |
| [store/ops.go](../../internal/vuln/store/ops.go) | `SourceStates`, `FindingCounts`, `ClaimedMatchCounts` over a `Database` (a retrying `Read`, which `*postgres.DB` implements) |
| [store/gauges.go](../../internal/vuln/store/gauges.go) | `GaugeSource(db, expected, ecosystems)`: the `observability.GaugeSource` `vuln` over `vmetrics.GaugeDescs()`; `ExpectedSources(config)`; `gaugeMetrics`, the pure part |
| [app/wire_vuln.go](../../internal/app/wire_vuln.go), block `WP-G` | `DBGauges().Add(store.GaugeSource(in.db, ...))` on every role, so every replica reports them and the aggregation is `max` |
| [app/vuln_facts.go](../../internal/app/vuln_facts.go) | the block `vulnerabilityData` of `GET /api/v1/system/info`: `vulnerabilityFacts` reads the table through the same `SourceStates`, `vulnFactsFrom` is the pure part. The field `VulnerabilityData` of `v1.Facts` ([handlers.go](../../internal/api/v1/handlers.go)) carries it; `configFacts` sets it when the database answers |
| [deploy/helm/trove/templates/prometheusrule.yaml](../../deploy/helm/trove/templates/prometheusrule.yaml), [_rules.tpl](../../deploy/helm/trove/templates/_rules.tpl), [tests/rules_test.yaml](../../deploy/helm/trove/tests/rules_test.yaml) | the three alerts and their promtool tests; see [helm-chart.md](helm-chart.md#the-prometheusrule) |
| [_validate.tpl](../../deploy/helm/trove/templates/_validate.tpl), [values.yaml](../../deploy/helm/trove/values.yaml), [values.schema.json](../../deploy/helm/trove/values.schema.json), [ci/monitoring.yaml](../../deploy/helm/trove/ci/monitoring.yaml) | the worker spool against `max_archive_size`, `metrics.prometheusRule.*`, the typed keys of `config.vulnerabilities` the chart reads |
| [test/integration/vulnrestore](../../test/integration/vulnrestore) | the restore tests and the gauge tests, below |
| [perf/vuln](../../perf/vuln), job `vuln-perf` of [performance.yml](../../.github/workflows/performance.yml) | the performance run, [perf/README.md](../../perf/README.md#the-vulnerability-scanning-run) |

**What the others rely on.** The gauge source is registered once per replica in `wireVuln` and reads the configuration captured at start
(`vcfg`), so a changed `vulnerabilities` section needs a restart like the rest of the configuration. The gauges and the facts read
`vuln_source_state` only through `store.SourceStates`; E's sources operation reads the same table and should agree with the facts on
`state`, `syncedAt` and `failures`. The facts' `stale` is `now - synced_at > stale_after` with the replica's clock, as `DataState.Stale`
is; it does not read A's cache, because the system information is a rare, bounded read and must not depend on the cache's `SafetyTTL`.

**Gauges.** Zero-filling (Q-VULN-56) is what makes `TroveVulnerabilityDataStale` fire for a restored installation: the gauge carries
`source`/`ecosystem` for every configured source, 0 without a row. If a work package makes the sync create the row of every configured
source at start-up, nothing changes: the value of a row without `synced_at` is 0 as well.

**The restore test** ([restore_test.go](../../test/integration/vulnrestore/restore_test.go)) builds the state a restore leaves, by SQL
(`seedInstallation`): four versions published through a replica, the synced state of an earlier life (source state, advisories,
affected entries, a KEV row, a pending evaluation, a claimed match), three findings with their history (a dismissed one, a blocked one in
a second repository, an open malicious one), then `TRUNCATE` of the six transient tables taken from the export registry
(`transientTables`), so a seventh transient table is emptied too. `snapshot` and `requireHeld` compare the statuses and require that the
history of every finding keeps its first events (an evaluation may add events, never change one).

| Test | Needs |
|---|---|
| `TestR_VULN_026_TheStateARestoreLeaves`: the classes of the eight tables, exported or not, the emulated state | nothing of the other packages (passes today) |
| `TestR_VULN_026_FailSafeBeforeTheFirstSync`: the facts say `empty`, the gauges show the source at 0 and the findings by status (passes today); the 403 answers and the 200 of the dismissed version; an evaluation job on a pending advisory that is not in `vuln_advisory` resolves nothing | the block check (C) for the downloads; the job kind `vuln-evaluate` and the fail-safe of R-VULN-011 (B) for the evaluation, which is inserted into `job` directly because the kind has no task |
| `TestR_VULN_026_AfterTheSyncAndTheFullEvaluation`: the task `vuln-sync-osv` imports from the in-process test source, the full evaluation follows (`full_evaluation_requested_at` cleared, a succeeded `vuln-evaluate` job), the statuses and histories are as before, three findings, the downloads answer as before | the sync and the task (A), the full evaluation after the first complete import (B), the block check (C) |
| `TestR_VULN_024_DatabaseGauges`, `TestR_VULN_024_GaugesWithoutAnyRow`: the values follow the rows on two replicas, `/readyz` stays 200 with a source that failed three times, the zero series | nothing (passes today) |

The tests that need other packages fail on a tree without them; they are the acceptance of R-VULN-026 once the packages are merged. They
use the environment variables `TROVE_VULNERABILITIES_SOURCES_OSV_BASE_URL`, `..._ECOSYSTEMS`, `..._KEV_MODE` and
`TROVE_METRICS_DB_GAUGE_INTERVAL` (1 s) to point the replicas at the stub and to read the gauges quickly.

**Unit tests.** [store/gauges_test.go](../../internal/vuln/store/gauges_test.go) (the three gauges, the zero series, the 60 series of the
findings gauge, `ExpectedSources`) and [app/vuln_facts_test.go](../../internal/app/vuln_facts_test.go).

**Chart tests.** [test/chart/vuln_rules_test.go](../../test/chart/vuln_rules_test.go): `TestR_VULN_024_PromtoolRules`,
`TestR_VULN_024_PrometheusRuleValues`, `TestR_VULN_025_SpoolHoldsOneArchive`, `TestR_VULN_025_ConfigurationPassesThrough` (`make chart-test`).

**Performance.** `perf/vuln` seeds in SQL and measures V-1 to V-9 against the code of the other packages; it compiles and its setup is
tested (`TestR_VULN_028_TheSeededInstallation`, 100,000 versions in about 2 s), the scenarios run once A to E are merged. How to read
a run: [perf/README.md](../../perf/README.md#the-vulnerability-scanning-run).

| [queries/vuln_audits.sql](../../internal/meta/postgres/queries/vuln_audits.sql) | `AuditAdvisoriesForNames` (one query for a body of names, over `vuln_affected_name`) and `AuditAllAdvisories`; one row per affected entry, times as epoch microseconds, so that no driver type leaves the store |
| [vuln/store/audits.go](../../internal/vuln/store/audits.go) | `Audits` implements `vuln.Advisories`: `ForNames` (per name, the advisories with that name's entries), `All`, `InternalNames` over an `InternalNameRule` |
| [vuln/store/audits_pages.go](../../internal/vuln/store/audits_pages.go) | `Pages`, the cache `vuln_nuget_page`: `Key(ecosystem)` (the data epoch and the applied epochs of `claims` and `repo_config`), `Page(ctx, key, render)` over `cache.Cache.Load` (the fill rule of R-ARCH-018); areas `vuln_data`, `claims`, `repo_config`; four entries, safety TTL 15 minutes |
| [app/wire_vuln_audits.go](../../internal/app/wire_vuln_audits.go) | `internalNameRule`, the rule of R-VULN-010 over the claim index (`Routing.ClaimsFor`) and the snapshot (a claim of a local repository of the format counts unless that repository sets `matchPublicAdvisories: always`); `wireAudits`, called from the `WP-D` block; `vulnArea.hostPorts()` |
| [npm/handlers_audit.go](../../internal/formats/npm/handlers_audit.go) | the dispatch by mode and the count of every answer (`trove_vuln_audit_requests_total`); the forwarder reads the effective mode |
| [npm/handlers_audit_local.go](../../internal/formats/npm/handlers_audit_local.go) | mode resolution (`auditModeOf`), the local handler, the answer's members, the notice |
| [npm/handlers_audit_ranges.go](../../internal/formats/npm/handlers_audit_ranges.go) | `vulnerable_versions` text with the `\|\| =V` completion; `auditAdapter`, npm's face for the matcher |
| [nuget/vuln_documents.go](../../internal/formats/nuget/vuln_documents.go) | the two documents, the mode (`vulnInfoModeOf`), the listing flag of the service index, the ETags, the page rendering and the NuGet intervals |
| `npm/settings*.go`, `settings.schema.json`; `nuget/settings*.go`, `settings.schema.json`; `nuget/index.go` | `npm.audit.mode` (`local`, `forward`, `disabled`) on every class; `nuget.vulnerabilityInfo` (`local`, `off`) on every class (`upstream` is refused until R-NUGET-023); the index entry and the ETag input |
| [test/integration/vulnaudit](../../test/integration/vulnaudit) | the HTTP tier: rows inserted directly, two replicas, claims and epochs published as a writer does |
**The mode of an audit.** A repository document that sets the mode is read; a document that leaves it out takes the installation's default, which a
format learns through two Host switches (`Enabled`, `LocalByDefault`; the composition root wires the advisories, and so `Enabled`, only while
`vulnerabilities.enabled` is `true`). `auditModeOf` (npm) and `vulnInfoModeOf` (NuGet) are the two places that resolve it; both are pure functions of
the document, the class and the Host, and the table test of `TestR_VULN_017_TheDefaultMode` pins every cell.
**The npm answer.** Body bounds are the forward's (`readAuditBody`); the data state is checked after the body (so a 413 is still a 413), then the names
are split into internal (withheld, never read) and the rest, whose advisories come from one `ForNames`. An advisory is listed when `Affects` finds one
requested version in one of its entries; its `vulnerable_versions` is the ranges as node-semver text plus `|| =V` for each affected version that the
text, evaluated without pre-release inclusion, does not match. Ids are assigned over the whole answer (`NPMAuditIDs`). Everything is a function of
the stored rows and the request, so two replicas answer the same bytes.
**The NuGet documents.** Both are `Computed` routes (the same for every caller that may read the repository). The index ETag covers the feed the `@id`
names and the data state (epoch, time of the last sync); the page ETag covers the render version, the data epoch and `PageKey`. The service index lists
the resource while the mode is `local` and the data is complete, and its ETag covers that flag, so that a client's cached index gains the entry.
**What the other packages rely on.** `Advisories` is complete; B's matcher and adapters are used by the audit through `Host.Vulnerabilities().Affects` with
an adapter of the audit's own (`auditAdapter`, below), which the development agent folds into the adapter of B (`internal/formats/npm/vuln.go`) at the
merge. The internal-name rule exists twice by design until then: `app.internalNameRule` (this package, R-VULN-010 as the audits read it) and the
evaluation's own (B); the two must say the same.
**Contract changes**
- `formats.VulnerabilityData` gains `Enabled()`, `LocalByDefault()`, `PageKey(ecosystem)` and `Page(ctx, key, render)` (Q-VULN-44, Q-VULN-45), with
  [formathost/vulnerability.go](../../internal/formathost/vulnerability.go) (`VulnPorts.LocalDefault`, `VulnPorts.Pages`, `PageCache`), the spec block of
  R-ARCH-008 and `internal/formats/testdata/contract.txt`.
- `wireVuln` takes the repository and the virtual areas (`repo repositoryArea, vir virtualArea`), for the claim index and the snapshot; `vulnArea` gains
  `pages` and `localAudits`; `wire_formats.go` hands `vz.hostPorts()` to the Host.
- The NuGet route table gains `nuget.vulnerabilities.index` and `nuget.vulnerabilities.page`; `ServiceIndex` keeps its signature (the rendering with the
  entry is `serviceIndexOf`), `indexETag` gains the listing flag.
**Tests.** Unit: `handlers_audit_local_test.go` (npm: rendering, ids, ranges and the completion, severities, the withheld names and the notice, the 503, the
501 cases, the default table), `vuln_documents_test.go` (NuGet: the listing, both documents, the cache, the ETags, the 304), `vulnerability_audit_test.go` (the
Host's switches), `wire_vuln_audits_test.go` (the internal-name rule). Integration: `test/integration/vulnaudit` (R-VULN-017: every class, byte-identical on two
replicas, the internal name and the notice, a claim that is added, the pre-release, the 503, the 501 cases including the cache view and the rules endpoint of
R-EP-012, the scanning switched off; R-VULN-018: the listing on every class, the documents, the 304, the ETag after an epoch change and after a claim, 404 and
503). Not reached at this tier, and why: the client runs (`npm audit`, `pnpm audit`, `yarn npm audit`, `dotnet restore`, NU1903, NU1904,
`NuGetAuditSuppress`, the reference lockfile's identity with the public registry's answer) are the end-to-end tier's; the propagation bound of the page's ETag
"with Valkey stopped" is shown here with the stack's Valkey unreachable (the replicas of the tests run without it) and a poll, not with a timed bound.

| [osv/record.go](../../internal/vuln/osv/record.go) | `Decode`: one OSV record to a `Record` (R-VULN-006): the identifier, `modified` and schema rules, the cut and cleaned texts, the reference filter, aliases, kind, the affected entries normalized by the format's `match.Adapter` (a refused name dropped and counted, a range the format cannot parse stored and counted `Unevaluable`), `every_version`, `fixed`, the digest of the bytes; every failure wraps `ErrRejected` |
| [osv/severity.go](../../internal/vuln/osv/severity.go) | `ScoreVector` over `github.com/pandatix/go-cvss` (2.0, 3.0, 3.1, 4.0, the rating scale of the vector's version), `DecideOwn` (rules 1, 2, 4, 5 of R-VULN-007), `Decide` (rule 3, the alias records) |
| [osv/kev.go](../../internal/vuln/osv/kev.go) | `DecodeKEV`: the catalog (R-VULN-008), refused whole when a rule fails |
| [store/sync.go](../../internal/vuln/store/sync.go) | the wrappers of [queries/vuln_sync.sql](../../internal/meta/postgres/queries/vuln_sync.sql): the state row (`Ensure`, `Get`, `Lock`, `Save`, `Failure`, `BumpEpoch`), a batch (`Stored`, `Upsert` as two jsonb documents, `AddPending`, `Delete`), the alias groups (`GroupRows`, `SetCanonical`, `SetSeverity`), the catalog (`ReplaceKEV`, `KEVCandidates`, `ApplyKEV`) |
| [sync/service.go](../../internal/vuln/sync/service.go) | `Service` and its `Options` (database, change log, cache registry, outbound client, spool, job queue, recorder, `Mirror`, adapters read at run time), `Params` (`acceptShrink`, the principal), the `Run` interface (`*jobs.Run` satisfies it), `Result` |
| [sync/run.go](../../internal/vuln/sync/run.go) | `RunOSV`: every ecosystem in turn, a failing one not stopping the others; `syncOne` picks the mode and full or incremental; `incremental` |
| [sync/fetch.go](../../internal/vuln/sync/fetch.go) | the requests: the conditional archive download through the spool, `googMD5` over every `x-goog-hash` line, the change file in ranges of 64 KiB (a `200` read from its start), a record, a document; the mapping of a transport error to `egress_denied`, `upstream_timeout`, `upstream_unreachable` |
| [sync/archive.go](../../internal/vuln/sync/archive.go) | the two passes of a full sync (`scanArchive`, `applyArchive`), the guards, the checkpoint, `writeChunk`, the deletion, `finishGroups` |
| [sync/records.go](../../internal/vuln/sync/records.go) | `fetchApply`: eight parallel record fetches, validation, the skip of an unchanged record, one transaction |
| [sync/group.go](../../internal/vuln/sync/group.go) | alias groups: the closure over `aliases`, union-find, `canonicalOf`, the severity of the canonical row, the known-exploited flags |
| [sync/finish.go](../../internal/vuln/sync/finish.go) | the final transaction of a run (`finishFull`, `finishIncremental`, `finishUnchanged`): state, watermark, epoch, `vuln_data` notification, the request of the full evaluation, `vuln-evaluate`, the import event |
| [sync/failure.go](../../internal/vuln/sync/failure.go) | the failure record (`last_error`, the hourly `vulnerability.source.failed`) |
| [sync/kev.go](../../internal/vuln/sync/kev.go), [sync/mirror.go](../../internal/vuln/sync/mirror.go) | `RunKEV`; mode `repository` (`Mirror`, `RepositoryMirror` over the repository reader and the item store, `fullMirror`) |
| [sync/states.go](../../internal/vuln/sync/states.go) | `States`: the cache `vuln_source_state` on the area `vuln_data` (safety TTL 60 s), read once per change, the last state kept when the database cannot be read, `Stale` from `stale_after` at call time |
| [app/wire_vuln_sync.go](../../internal/app/wire_vuln_sync.go) | the wiring: the outbound client of purpose `vuln_source`, the adapters from the catalog, the ecosystem check, the three kinds and tasks, `App.VulnSync()` |
**What the others rely on**
- `vuln.States` answers from the replica's cache, with no query per call. `Complete` is the state `complete` of a finished full import; `Epoch` is `vuln_source_state.data_epoch`, raised by every run that wrote
  or deleted records, by the first complete import and by a known-exploited change.
- `vuln_advisory` holds one row per published identifier; `canonical_id` is the alias group's canonical identifier (R-VULN-006) and is the identifier a finding carries; the canonical row carries the group's decided
  severity and its known-exploited flag and date. `vuln_affected` has one row per surviving affected entry, `ord` its position among them, `ranges` as published (the matcher normalizes event versions),
  `versions` and `fixed` normalized by the format.
- `vuln_pending_evaluation` holds every record a run wrote, every record a full sync deleted, every record whose known-exploited flag changed, and, when a group's canonical identifier or severity changed, the
  identifiers of the group and the old canonical identifier. The evaluation reads advisory rows for the identifiers it finds there; a deleted record has no row left, its findings are the way to its versions.
- `vuln_source_state.full_evaluation_requested_at` is set in the transaction of the first complete import and of a raised `match.MatcherVersion`, and `vuln-evaluate` is enqueued in the same transaction; the
  sync never clears it. The enqueue skips an unregistered `vuln-evaluate` kind with a warning (a build without the evaluation), so the sync works before it exists.
- A run that fails leaves the data as it was: `last_error` is a problem object with `at` and `eventAt`, `failures` counts consecutive failures. The metrics are `trove_vuln_sync_runs_total` and
  `trove_vuln_sync_records_total`; the gauges of R-VULN-024 are work package G's and read the same columns.
- The package's goroutines: the eight record fetches of an incremental run end with the batch; nothing outlives a run.
| File | Change |
| [formats/untrusted/zip.go](../../internal/formats/untrusted/zip.go) | `NewZipDeclared`: `NewZip` with limits used as given instead of tightened against the defaults (R-ARCH-028 asks a pass outside `Inspect` to declare its own finite bounds; the advisory archive needs 1,000,000 entries and 4 GiB), every bound required to be set; `NewZip` is unchanged |
| [app/app.go](../../internal/app/app.go), [testenv/replica.go](../../test/integration/testenv/replica.go) | `app.Options.VulnAdapters` and `testenv.ReplicaOptions.VulnAdapters`: replace the adapters of the format registry; production passes nil. The tests of the sync run with adapters of their own, so they need no format |
| [app/wire_vuln.go](../../internal/app/wire_vuln.go) | the field `sync *vsync.Service` of `vulnArea` (inside a `WP-A` marker), the blocks of `wireVuln` and `registerVulnJobs`; the logic is in `wire_vuln_sync.go` |
| [config/vulnerabilities.go](../../internal/config/vulnerabilities.go) | not changed: the fixed list stays (Q-VULN-34), the registry check is the composition root's, and a test of the composition root fails when a registered ecosystem is not in the list |
| [manual](../manual/admin/vulnerability-sources.md) | the page `vulnerability-sources`, in `internal/manual` and `docs/manual/README.md` |
**Tests**
| Test | What it pins |
| [osv_test.go](../../internal/vuln/osv/osv_test.go) | the example vectors of the CVSS 3.1 and 4.0 specifications and their scores (R-VULN-007), the severity rules and the alias rule, every rule of R-VULN-006 (identifier, times, schema version, cut and cleaned texts, references, aliases, affected entries, the unevaluable ranges, limits), the catalog (R-VULN-008) |
| [sync_test.go](../../internal/vuln/sync/sync_test.go) | the canonical identifier, alias groups, `googMD5` over two lines, `Content-Range`, the change file's rows and parts, the state cache (one read per change, stale, last state kept), the problem object |
| [vulnsync](../../test/integration/vulnsync) | every criterion of R-VULN-003 to R-VULN-008 that the integration tier reaches, against [test/osvstub](../../test/osvstub) with a worker and a web replica: the requests of the first and the second run, three changed records, the source without `Range`, the change file in parts, the turn to a full sync, the guards and the override, the digest mismatch, the archive too large, `egress_denied` and the failure record, one OSV sync at a time, the sources off, mode `repository` (the upload, the same bytes again, the shrunken archive, the catalog, the repository that is not generic), the records of R-VULN-006, the severities of R-VULN-007, the catalog flag of R-VULN-008, the matcher version of R-VULN-011, a web replica following the worker (R-VULN-027), the release of a job and its resumption |
Not reachable in the integration tier, and why: the drain of a node ("`queued` again with `attempt` unchanged, another worker completes it", R-VULN-004) is the e2e wave's; the test releases the job with
`jobs.ErrReleased` and resumes it with a run handle that keeps the checkpoint, which is what the job table does, and `Service.SetBatchHook` is the seam. The advisory detail naming
"OpenSSF malicious-packages, Apache-2.0" (R-VULN-003) is the API's of work package E; `advisory.SourceOf` is pinned by the contract tests. The finding that returns to `needs_triage` with the event `reopened`
(R-VULN-008) is the evaluation's of work package B. The alert `TroveVulnerabilitySyncFailing` and the readiness of the replicas (R-VULN-024) are work package G's.

### WP-C · Enforcement

The download block of [R-VULN-015](../spec/vulnerability-scanning.md#r-vuln-015--download-blocking) and [R-VULN-016](../spec/vulnerability-scanning.md#r-vuln-016--refusals-in-the-protocols-shapes).
Decisions: [Q-VULN-40 to Q-VULN-43](../spec/vulnerability-scanning.md#q-vuln-40--the-download-policy-rides-in-the-item-lookups-repository-reference-and-the-block-is-read-by-that-one-statement).
The security view, with the gaps H-483 to H-485, is [security/vulnerability-scanning.md](../security/vulnerability-scanning.md).

**The flow of one download.** The route that serves a stored item resolves the policy of the repository the item is read in from the replica's
snapshot (`repository.Repository.VulnerabilityPolicy`, through `enforce.Enforcer.PolicyOf`) and puts it in the `items.RepoRef` of the lookup.
`GetLiveItem` ([files.sql](../../internal/meta/postgres/queries/files.sql)) joins the finding of R-VULN-015 laterally, with `enforce` and
`block_malicious` as parameters, and returns `items.version_id` and the row; [items.Item](../../internal/items/item.go) carries `VersionID`, `Block`
(a `vuln.BlockRow`) and the `Policy` it was read with. The serving function then calls `Enforcer.Decide` (which is `vuln.Verdict` over the cached
`States`) and `Enforcer.Refuse` (count, audit, error). A lookup whose reference carries no policy reads no block, so every other reader of
`Live` is unchanged.

| Where | File | What |
|---|---|---|
| the lookup | [read.go](../../internal/items/read.go), [item.go](../../internal/items/item.go), [items.go](../../internal/items/items.go) | `RepoRef.Vuln`, `Item.VersionID/Block/Policy` |
| the format routes, the remote hit, the cache view, every virtual tier | [formathost/enforce.go](../../internal/formathost/enforce.go), [ingest.go](../../internal/formathost/ingest.go) | `Host.liveRef` (policy by the item's own repository key, through `Options.Repos`), `Host.refuseBlocked` in `itemStore.Serve` after `ServeCheck`; a `403` is no tier miss, so the virtual walk ends |
| `/files/` | [files/download.go](../../internal/files/download.go) | `Service.Stat`, after `ServeCheck` |
| the remote engine | [proxy/artifact.go](../../internal/proxy/artifact.go) | `Engine.liveRef` for `serveStored` and `Cached`; the miss gate `Enforcer.GateMiss` between `mayFill` and the HEAD or the fetch |
| the decision and the refusal | [vuln/enforce](../../internal/vuln/enforce/enforce.go) | `PolicyOf`, `Decide`, `Refuse`, `GateMiss`; the text, the extensions `advisory` and `findingId`, `Cache-Control: no-store`, the metric, the audit event with its one-minute dedup in the replica's memory |
| the name of a refused version | [vuln/store/enforce.go](../../internal/vuln/store/enforce.go), [queries/vuln_enforce.sql](../../internal/meta/postgres/queries/vuln_enforce.sql) | `VersionNames`, read when a download is refused (and kept for a minute), never on a served one |
| the wiring | [app/wire_vuln.go](../../internal/app/wire_vuln.go) block `WP-C`, `wireFiles`, `wireProxyEngine`, `wireFormatHost` | `vulnArea.enforcer`; the three consumers take the area |

The refusal needs no code in a format: the npm and NuGet `WriteError` and the problem writer render kind, code, text and headers of an `errs`
error already, so the npm body, the NuGet line and the problem details of R-VULN-016 are one error.

**What the others rely on.** `vulnArea.enforcer` (nil-safe: a nil `*Enforcer` resolves no policy and refuses nothing); `items.Item.Block` and
`Item.VersionID` for any reader of the lookup; `enforce.Enforcer.GateMiss` for a further remote path that fetches an artifact. WP-E's `blocked`
and `blockReason` use `Policy.BlockReasonOf`, which `Verdict` uses too. The enforcer resolves a repository as unscanned unless the replica has a
`States` (work package A) and the repository's format has the adapter (work package B).

**Contract changes.** (1) `GetLiveItem` of `files.sql` (the lateral join, `version_id`; its parameters and row type changed, so the generated
`files.sql.go` did too); (2) `items.RepoRef` gains `Vuln` and `items.Item` gains `VersionID`, `Block` and `Policy` (the lookup's contract, not a
file of the list above); (3) `app.Options.Vuln` (`app.VulnOverride`) replaces the `States` and `Evaluator` ports in tests, and the
integration harness passes it (`testenv.ReplicaOptions.Vuln`); (4) `wireFiles`, `wireProxyEngine` take `vulnArea`, `formathost.Options` gains
`Enforce` and `Repos`, `files.Options` and `proxy.Options` gain `Enforce`; (5) the marked block `WP-C` is filled and the comment line of the
header says what it builds.

**The query budget.** R-ARCH-027 budgets one query for a local download and one for a remote hit. The code spent more before the scanning
and spends the same with it: [querycount_test.go](../../test/integration/vulnblock/querycount_test.go) counts the statements a download runs
from the PostgreSQL log, on a replica that scans and one that does not, in one database, and requires the same statements per download and
one `GetLiveItem`. Measured: a local download runs `GetLiveItem` and `NPMTombstoned` (2 statements), the raw view the same, a remote hit
`ProxyEntryGet`, `GetLiveItem` and `NPMRemoteTarballFacts` twice (4); the scanning adds none. The overrun against the budget is not this
package's to fix.

**Tests.** Unit: [enforce_test.go](../../internal/vuln/enforce/enforce_test.go) (the verdict, the text, the dedup, the report mode, the miss
gate). Integration, in [test/integration/vulnblock](../../test/integration/vulnblock/) (`make test-integration`; findings inserted directly, the
`States` and `Evaluator` ports faked through `app.Options.Vuln`, every test with Valkey unreachable unless it asks for it): the npm tarball, the
NuGet `.nupkg`, `/files/`, the remote hit, the cache view, the virtual tiers, the remote miss without an upstream request, `onMissingData`, the
report mode, the dedup, the installation switch, the dismissal on a second replica (with Valkey), and the statement counts. Not reached at this
tier: the client runs (`npm install`, `npm ci`, `dotnet restore -v n`) and a NuGet remote repository, which are the end-to-end wave's; the
text on an endpoint with exposure rules is not asserted.


## Tests that pass now

These contract tests carry their requirement IDs in comments or in names that say what they pin; the evidence of a requirement comes from the tests of its work package (Q-ROAD-5).

| Test | What it pins |
|---|---|
| [advisory_test.go](../../internal/vuln/advisory/advisory_test.go), [affects_test.go](../../internal/vuln/match/affects_test.go), [vuln_test.go](../../internal/vuln/vuln_test.go), [metrics_test.go](../../internal/vuln/metrics/metrics_test.go) | ranks, attribution, the audit id and URL rules, the OSV event JSON, the matcher's cases, the transitions, the verdict, the member's defaults and decode, the task defaults against the configuration, the families against the spec |
| [vulnerabilities_test.go](../../internal/repository/vulnerabilities_test.go) | `PATCH npm-all {"vulnerabilities": {"enabled": false}}` is `422 not_applicable` at `/vulnerabilities` (at the level of the document the service validates), the generic format likewise, the partial member, the read model and the policy |
| [vulnerabilities_test.go](../../internal/config/vulnerabilities_test.go) | the defaults, and the refusals of R-VULN-025's criterion |
| [capabilities_test.go](../../internal/formats/capabilities_test.go), [vulnerability_test.go](../../internal/formathost/vulnerability_test.go) | `CapabilitiesOf` finds the adapter, the catalog's ecosystem, a host that has not wired the scanning is not complete |
| [vulnerabilities_triage_test.go](../../internal/authz/vulnerabilities_triage_test.go) | the capability's role sets and its place among the system scope items |
| [formats_allowlist_test.go](../../internal/archtest/formats_allowlist_test.go) | a format may import the two leaves and nothing else of the scanning |
| [TestVulnMigration](../../test/integration/postgres/vuln_test.go) | the schema |

## Contract files

A work package that edits one of these has made a contract change: say so in the review and update this page.

[internal/vuln](../../internal/vuln) (root), [internal/vuln/advisory](../../internal/vuln/advisory), [internal/vuln/match/match.go](../../internal/vuln/match/match.go),
[internal/vuln/metrics](../../internal/vuln/metrics), [internal/formats/vulnerability.go](../../internal/formats/vulnerability.go), the Host and capability parts of
[internal/formats/host.go](../../internal/formats/host.go) and [formats.go](../../internal/formats/formats.go), `internal/formats/testdata/contract.txt`,
[catalog.go](../../internal/formats/catalog/catalog.go), [formathost/vulnerability.go](../../internal/formathost/vulnerability.go), the migrations 00035 and 00036,
[tables_vuln.go](../../internal/meta/postgres/tables_vuln.go), [config/vulnerabilities.go](../../internal/config/vulnerabilities.go) (the keys and defaults; A changes only the ecosystem rule),
[audit/events_vuln.go](../../internal/audit/events_vuln.go), the M4b block of [errs/codes.go](../../internal/errs/codes.go), the capability in `internal/authz`, the M4b blocks of
[api/openapi.yaml](../../api/openapi.yaml), the layer rows of [archtest/layers.go](../../internal/archtest/layers.go) and the format allow-list of [archtest/imports.go](../../internal/archtest/imports.go),
the member in [repository/document.go](../../internal/repository/document.go), `validate.go` and `repository.go`, the step list of [app/steps.go](../../internal/app/steps.go), and the structure
(not the blocks) of [app/wire_vuln.go](../../internal/app/wire_vuln.go).
