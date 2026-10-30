# Architecture decision records

Status: decision log for the specification. Phase 1 (M0 Foundation) is built; the rest of the specification is not implemented yet.

This page records trove's design decisions, one entry per decision: what forced it, what was
decided, what follows from it, which alternatives were weighed and where the specification states
it in full. The normative text lives on the [specification pages](spec/README.md); an entry here
summarizes and links, it never overrides a spec page. Questions stay on the page that raised them,
with their options, the choice and the rationale, and are only listed here, in
[Decisions by the development agent](#decisions-by-the-development-agent).

Every question on the spec pages has been decided by the development agent: for each one it
worked out the options, selected one of them and recorded the reasoning on the question's page.
The owner reviews these choices at the walkthrough of the first prototype and objects to whatever
he dislikes. Until then every entry outside the owner constraints carries the status *Decided by
the development agent — owner review at the walkthrough*, and development proceeds on it.

**Status values.**

| Status | Meaning |
|---|---|
| Accepted (owner constraint) | Fixed by the project owner ([owner constraints](spec/README.md#owner-constraints)). Not reopened by the spec pages or decided by the development agent; a page that finds one costly raises a question for the owner. |
| Decided by the development agent — owner review at the walkthrough | The design the spec pages build on: the invariants and every option the development agent selected from the options a question's page documents. Becomes *Accepted* when the owner confirms it at the walkthrough; an objection there leads to a new entry that supersedes it. |
| Accepted | Confirmed by the owner. |
| Superseded by ADR-*n* | Replaced; the entry stays for the record. |

**Rules for this log.** Numbers are assigned in order and never reused or renumbered. A changed
decision gets a new entry that supersedes the old one, and the old entry's status changes. When an
owner constraint changes the premise of an entry rather than a later decision replacing it, the
entry is updated in place, and its Context says when and what was replaced. When a
question is decided on its spec page, the decision either becomes a new entry or updates the entry
listed next to it in [Decisions by the development agent](#decisions-by-the-development-agent),
and a question raised later is added there. Alternatives marked as considered here
are the ones the spec pages weighed, plus the obvious designs a reviewer would ask about; the
reasoning for each rejection is one line, the spec page holds the rest.

**Revision of 2026-10-28.** The owner added four constraints that cannot be deselected: high
availability on HA PostgreSQL, HA Valkey with Sentinel and S3
([ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3)),
horizontal scale with random request distribution, split roles and drains without outage
([ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage)), a
configurable backup job that writes consistent backups to S3
([ADR-094](#adr-094--a-configurable-backup-job-writes-consistent-backups-to-s3)), and no
ReadWriteMany storage ([ADR-095](#adr-095--no-readwritemany-storage)). The development agent
checked every earlier decision against them:
- **Invariants updated in place.** INV-2, INV-3, INV-11, INV-12 and INV-13 changed under the new
  constraints, so their entries ([ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2),
  [ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3),
  [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11),
  [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12),
  [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13)) record the revised rule,
  and each Context names what was replaced. The earlier decision "No Redis" is superseded.
- **Revisited questions.** A question whose choice was checked again carries a line "Revisited on
  2026-10-28" on its spec page. Its row in
  [Decisions by the development agent](#decisions-by-the-development-agent) and the entry that
  lists it say whether the choice changed.
- **New entries.** The new invariants INV-18 and INV-19 and the design choices of this revision are
  ADR-096 to ADR-123.

<details>
<summary>Index of all entries</summary>

| ADR | Title | Status |
|---|---|---|
| [ADR-001](#adr-001--backend-in-go-1273) | Backend in Go 1.27.3 | Accepted (owner constraint) |
| [ADR-002](#adr-002--frontend-in-angular-22) | Frontend in Angular 22 | Accepted (owner constraint) |
| [ADR-003](#adr-003--postgresql-as-the-database) | PostgreSQL as the database | Accepted (owner constraint) |
| [ADR-004](#adr-004--s3-as-binary-storage) | S3 as binary storage | Accepted (owner constraint) |
| [ADR-005](#adr-005--high-availability) | High availability | Accepted (owner constraint) |
| [ADR-006](#adr-006--deployment-through-a-helm-chart) | Deployment through a Helm chart | Accepted (owner constraint) |
| [ADR-007](#adr-007--oidc-login-own-tokens-detailed-permissions) | OIDC login, own tokens, detailed permissions | Accepted (owner constraint) |
| [ADR-008](#adr-008--gitlab-ci-jobs-obtain-permissions-through-oidc) | GitLab CI jobs obtain permissions through OIDC | Accepted (owner constraint) |
| [ADR-009](#adr-009--pull-through-proxy-for-external-package-sources) | Pull-through proxy for external package sources | Accepted (owner constraint) |
| [ADR-010](#adr-010--every-catalog-format-npm-and-nuget-first) | Every catalog format, npm and NuGet first | Accepted (owner constraint) |
| [ADR-011](#adr-011--trove-is-a-self-contained-white-label-product) | trove is a self-contained, white-label product | Accepted (owner constraint) |
| [ADR-012](#adr-012--one-toolchain-one-image-inv-1) | One toolchain, one image (INV-1) | Decided by the development agent — owner review at the walkthrough |
| [ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2) | One binary, roles split from the start, identical replicas per role (INV-2) | Decided by the development agent — owner review at the walkthrough |
| [ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3) | PostgreSQL is the system of record; Valkey holds only shared ephemeral state (INV-3) | Decided by the development agent — owner review at the walkthrough |
| [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) | Content-addressed blob store on S3 (INV-4) | Decided by the development agent — owner review at the walkthrough |
| [ADR-016](#adr-016--repository-model-local-remote-virtual-inv-5) | Repository model: local, remote, virtual (INV-5) | Decided by the development agent — owner review at the walkthrough |
| [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6) | URL layout on two listeners (INV-6) | Decided by the development agent — owner review at the walkthrough |
| [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7) | Humans authenticate only through an external OIDC provider (INV-7) | Decided by the development agent — owner review at the walkthrough |
| [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8) | trove issues opaque, prefixed tokens (INV-8) | Decided by the development agent — owner review at the walkthrough |
| [ADR-020](#adr-020--deny-by-default-allow-only-permission-policies-inv-9) | Deny by default, allow-only permission policies (INV-9) | Decided by the development agent — owner review at the walkthrough |
| [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) | GitLab CI identity through ID-token exchange (INV-10) | Decided by the development agent — owner review at the walkthrough |
| [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) | Pull-through proxy semantics (INV-11) | Decided by the development agent — owner review at the walkthrough |
| [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12) | High availability and horizontal scale on HA PostgreSQL, HA Valkey and S3 (INV-12) | Decided by the development agent — owner review at the walkthrough |
| [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) | The Helm chart is the supported deployment (INV-13) | Decided by the development agent — owner review at the walkthrough |
| [ADR-025](#adr-025--npm-and-nuget-v3-are-the-mvp-formats-inv-14) | npm and NuGet v3 are the MVP formats (INV-14) | Decided by the development agent — owner review at the walkthrough |
| [ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15) | One configuration file plus database-held configuration (INV-15) | Decided by the development agent — owner review at the walkthrough |
| [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16) | Structured logs, Prometheus metrics, OTLP traces, audit in PostgreSQL (INV-16) | Decided by the development agent — owner review at the walkthrough |
| [ADR-028](#adr-028--the-ui-uses-only-the-public-api-inv-17) | The UI uses only the public API (INV-17) | Decided by the development agent — owner review at the walkthrough |
| [ADR-029](#adr-029--formats-are-compiled-in-plug-ins-behind-a-narrow-host-interface) | Formats are compiled-in plug-ins behind a narrow host interface | Decided by the development agent — owner review at the walkthrough |
| [ADR-030](#adr-030--one-origin-for-ui-api-and-package-content) | One origin for UI, API and package content | Decided by the development agent — owner review at the walkthrough |
| [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early) | Caches validate themselves; notifications only evict early | Decided by the development agent — owner review at the walkthrough |
| [ADR-032](#adr-032--stored-secrets-are-sealed-under-a-key-ring-the-operator-supplies) | Stored secrets are sealed under a key ring the operator supplies | Decided by the development agent — owner review at the walkthrough |
| [ADR-033](#adr-033--versioning-promises-per-surface-and-a-schema-compatibility-gate) | Versioning promises per surface and a schema compatibility gate | Decided by the development agent — owner review at the walkthrough |
| [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work) | One PostgreSQL job table for maintenance, API operations and internal work | Decided by the development agent — owner review at the walkthrough |
| [ADR-035](#adr-035--virtual-resolution-in-three-tiers-decided-per-member) | Virtual resolution in three tiers, decided per member | Decided by the development agent — owner review at the walkthrough |
| [ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules) | Dependency-confusion protection through namespace claims and pin rules | Decided by the development agent — owner review at the walkthrough |
| [ADR-037](#adr-037--one-name-pattern-syntax-most-specific-wins-applied-to-every-protocol-request) | One name-pattern syntax, most specific wins, applied to every protocol request | Decided by the development agent — owner review at the walkthrough |
| [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving) | Upstream failures: classified outcomes, a shared circuit, bounded stale serving | Decided by the development agent — owner review at the walkthrough |
| [ADR-039](#adr-039--egress-address-checks-after-dns-resolution-for-every-remote-connection) | Egress address checks after DNS resolution for every remote connection | Decided by the development agent — owner review at the walkthrough |
| [ADR-040](#adr-040--strict-repository-configuration-documents-and-asynchronous-deletion) | Strict repository configuration documents and asynchronous deletion | Decided by the development agent — owner review at the walkthrough |
| [ADR-041](#adr-041--the-database-row-decides-whether-a-blob-exists) | The database row decides whether a blob exists | Decided by the development agent — owner review at the walkthrough |
| [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) | Race-free garbage collection with a grace period | Decided by the development agent — owner review at the walkthrough |
| [ADR-043](#adr-043--object-store-outage-no-local-queue-and-201-means-durable) | Object-store outage: no local queue, and 201 means durable | Decided by the development agent — owner review at the walkthrough |
| [ADR-044](#adr-044--a-trash-can-that-also-covers-overwrites-p1) | A trash can that also covers overwrites (P1) | Decided by the development agent — owner review at the walkthrough |
| [ADR-045](#adr-045--presigned-download-redirects-as-a-per-repository-opt-in-p2) | Presigned download redirects as a per-repository opt-in (P2) | Decided by the development agent — owner review at the walkthrough |
| [ADR-046](#adr-046--server-side-ui-sessions-behind-an-opaque-cookie) | Server-side UI sessions behind an opaque cookie | Decided by the development agent — owner review at the walkthrough |
| [ADR-047](#adr-047--a-break-glass-administrator-outside-the-database) | A break-glass administrator outside the database | Decided by the development agent — owner review at the walkthrough |
| [ADR-048](#adr-048--tokens-hashed-with-sha-256-a-positive-only-cache-bounded-revocation-latency) | Tokens hashed with SHA-256, a positive-only cache, bounded revocation latency | Decided by the development agent — owner review at the walkthrough |
| [ADR-049](#adr-049--command-line-login-through-the-browser-session-p1) | Command-line login through the browser session (P1) | Decided by the development agent — owner review at the walkthrough |
| [ADR-050](#adr-050--anonymous-access-is-off-by-default-and-needs-two-switches) | Anonymous access is off by default and needs two switches | Decided by the development agent — owner review at the walkthrough |
| [ADR-051](#adr-051--actions-are-explicit-and-a-token-scope-only-narrows) | Actions are explicit, and a token scope only narrows | Decided by the development agent — owner review at the walkthrough |
| [ADR-052](#adr-052--changing-what-clients-install-needs-deploy-not-annotate) | Changing what clients install needs `deploy`, not `annotate` | Decided by the development agent — owner review at the walkthrough |
| [ADR-053](#adr-053--virtual-repositories-endpoint-gate-plus-member-rights) | Virtual repositories: endpoint gate plus member rights | Decided by the development agent — owner review at the walkthrough |
| [ADR-054](#adr-054--cache-fills-are-guarded-by-a-configurable-action-read-by-default) | Cache fills are guarded by a configurable action, `read` by default | Decided by the development agent — owner review at the walkthrough |
| [ADR-055](#adr-055--refused-reads-answer-404-refused-writes-403) | Refused reads answer 404, refused writes 403 | Decided by the development agent — owner review at the walkthrough |
| [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) | Package ownership is decided by the first publish | Decided by the development agent — owner review at the walkthrough |
| [ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed) | A compiled grant index per replica, consistent through an epoch, failing closed | Decided by the development agent — owner review at the walkthrough |
| [ADR-058](#adr-058--projects-for-delegated-administration-reduced-in-p1) | Projects for delegated administration, reduced, in P1 | Decided by the development agent — owner review at the walkthrough |
| [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint) | CI tokens through a standard token-exchange endpoint | Decided by the development agent — owner review at the walkthrough |
| [ADR-060](#adr-060--ordered-identity-mappings-first-match-per-tier-union-across-tiers) | Ordered identity mappings: first match per tier, union across tiers | Decided by the development agent — owner review at the walkthrough |
| [ADR-061](#adr-061--a-small-claim-template-language-with-escaping) | A small claim-template language with escaping | Decided by the development agent — owner review at the walkthrough |
| [ADR-062](#adr-062--fork-pipelines-are-denied-by-default) | Fork pipelines are denied by default | Decided by the development agent — owner review at the walkthrough |
| [ADR-063](#adr-063--a-ci-tokens-lifetime-is-bounded-by-its-job) | A CI token's lifetime is bounded by its job | Decided by the development agent — owner review at the walkthrough |
| [ADR-064](#adr-064--subject-token-reuse-is-recorded-single-use-depends-on-the-issuer-kind) | Subject-token reuse is recorded; single use depends on the issuer kind | Decided by the development agent — owner review at the walkthrough |
| [ADR-065](#adr-065--further-ci-issuers-through-the-same-mechanism-p1) | Further CI issuers through the same mechanism (P1) | Decided by the development agent — owner review at the walkthrough |
| [ADR-066](#adr-066--npm-package-documents-are-rendered-from-rows-not-stored) | npm package documents are rendered from rows, not stored | Decided by the development agent — owner review at the walkthrough |
| [ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones) | npm and NuGet versions are immutable, guarded by tombstones | Decided by the development agent — owner review at the walkthrough |
| [ADR-068](#adr-068--npm-remotes-fetch-only-the-full-document) | npm remotes fetch only the full document | Decided by the development agent — owner review at the walkthrough |
| [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default) | Versions removed upstream are hidden by default | Decided by the development agent — owner review at the walkthrough |
| [ADR-070](#adr-070--merged-npm-documents-take-each-version-from-the-member-that-would-serve-it) | Merged npm documents take each version from the member that would serve it | Decided by the development agent — owner review at the walkthrough |
| [ADR-071](#adr-071--npm-trusted-publishing-through-package-bound-ci-tokens-p1) | npm trusted publishing through package-bound CI tokens (P1) | Decided by the development agent — owner review at the walkthrough |
| [ADR-072](#adr-072--real-client-acceptance-matrices-gate-each-format) | Real-client acceptance matrices gate each format | Decided by the development agent — owner review at the walkthrough |
| [ADR-073](#adr-073--nuget-v3-only-every-resource-below-the-feed-root) | NuGet v3 only, every resource below the feed root | Decided by the development agent — owner review at the walkthrough |
| [ADR-074](#adr-074--nuget-package-ids-ascii-on-push-case-insensitive-first-casing-displayed) | NuGet package IDs: ASCII on push, case-insensitive, first casing displayed | Decided by the development agent — owner review at the walkthrough |
| [ADR-075](#adr-075--strict-archive-and-manifest-parsing-the-declared-id-is-what-gets-authorized) | Strict archive and manifest parsing; the declared ID is what gets authorized | Decided by the development agent — owner review at the walkthrough |
| [ADR-076](#adr-076--symbol-packages-only-as-snupkg-matching-an-existing-package) | Symbol packages only as `.snupkg` matching an existing package | Decided by the development agent — owner review at the walkthrough |
| [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing) | Singleton work under advisory-lock leases with fencing | Decided by the development agent — owner review at the walkthrough |
| [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) | Restores keep metadata and blobs consistent by ordering, not by coordination | Decided by the development agent — owner review at the walkthrough |
| [ADR-079](#adr-079--the-chart-validates-before-it-renders-and-never-creates-secrets) | The chart validates before it renders and never creates secrets | Decided by the development agent — owner review at the walkthrough |
| [ADR-080](#adr-080--audit-records-commit-with-the-change-they-record) | Audit records commit with the change they record | Decided by the development agent — owner review at the walkthrough |
| [ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1) | Configuration as code: documents own their objects; plan before apply (P1) | Decided by the development agent — owner review at the walkthrough |
| [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) | Events through a transactional outbox, as CloudEvents, signed per Standard Webhooks (P1) | Decided by the development agent — owner review at the walkthrough |
| [ADR-083](#adr-083--the-openapi-31-document-is-the-api-contract) | The OpenAPI 3.1 document is the API contract | Decided by the development agent — owner review at the walkthrough |
| [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) | Uniform API conventions: problem details, cursors, merge patch, ETags, idempotency keys | Decided by the development agent — owner review at the walkthrough |
| [ADR-085](#adr-085--long-running-work-answers-202-with-a-job) | Long-running work answers 202 with a job | Decided by the development agent — owner review at the walkthrough |
| [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) | UI baseline: Angular 22 defaults, a strict CSP, nothing from foreign origins | Decided by the development agent — owner review at the walkthrough |
| [ADR-087](#adr-087--readmes-rendered-on-the-server-through-an-allowlist-sanitizer) | READMEs rendered on the server through an allowlist sanitizer | Decided by the development agent — owner review at the walkthrough |
| [ADR-088](#adr-088--english-and-german-in-the-mvp) | English and German in the MVP | Decided by the development agent — owner review at the walkthrough |
| [ADR-089](#adr-089--derived-metadata-is-rendered-per-request-and-a-publish-never-waits-for-an-index) | Derived metadata is rendered per request, and a publish never waits for an index | Decided by the development agent — owner review at the walkthrough |
| [ADR-090](#adr-090--a-nuget-protocol-delete-unlists-by-default) | A NuGet protocol delete unlists by default | Decided by the development agent — owner review at the walkthrough |
| [ADR-091](#adr-091--forwarded-search-and-audit-requests-withhold-protected-names) | Forwarded search and audit requests withhold protected names | Decided by the development agent — owner review at the walkthrough |
| [ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3) | High availability on HA PostgreSQL, HA Valkey with Sentinel and S3 | Accepted (owner constraint) |
| [ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage) | Horizontal scale: random distribution, split roles, drains without outage | Accepted (owner constraint) |
| [ADR-094](#adr-094--a-configurable-backup-job-writes-consistent-backups-to-s3) | A configurable backup job writes consistent backups to S3 | Accepted (owner constraint) |
| [ADR-095](#adr-095--no-readwritemany-storage) | No ReadWriteMany storage | Accepted (owner constraint) |
| [ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18) | No state between requests on a replica (INV-18) | Decided by the development agent — owner review at the walkthrough |
| [ADR-097](#adr-097--consistent-backups-to-s3-restored-into-an-empty-installation-inv-19) | Consistent backups to S3, restored into an empty installation (INV-19) | Decided by the development agent — owner review at the walkthrough |
| [ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants) | Valkey holds coordination and the negative cache, nothing served and nothing that grants | Decided by the development agent — owner review at the walkthrough |
| [ADR-099](#adr-099--valkey-client-valkey-go-against-valkey-90-and-later-sentinel-in-production) | Valkey client: valkey-go against Valkey 9.0 and later, Sentinel in production | Decided by the development agent — owner review at the walkthrough |
| [ADR-100](#adr-100--valkey-degrades-trove-but-never-gates-readiness-except-for-configuration-errors) | Valkey degrades trove but never gates readiness, except for configuration errors | Decided by the development agent — owner review at the walkthrough |
| [ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers) | Connection model: a transaction-scoped main pool behind an optional pooler, sessions only on workers | Decided by the development agent — owner review at the walkthrough |
| [ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback) | Cluster-wide rate limits in Valkey with a per-replica fallback | Decided by the development agent — owner review at the walkthrough |
| [ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica) | Shutdown protocol: drain delay, transfers cut at the deadline, resumed on any replica | Decided by the development agent — owner review at the walkthrough |
| [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases) | Subsystems per role: web serves, worker owns background work and leases | Decided by the development agent — owner review at the walkthrough |
| [ADR-105](#adr-105--drain-capacity-one-replica-more-than-the-peak-needs-per-role) | Drain capacity: one replica more than the peak needs, per role | Decided by the development agent — owner review at the walkthrough |
| [ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold) | Backup format: a trove-native snapshot export, a shared blob pool and a garbage-collection hold | Decided by the development agent — owner review at the walkthrough |
| [ADR-107](#adr-107--backup-runs-one-at-a-time-in-a-leased-row-cancelled-when-they-stall) | Backup runs: one at a time in a leased row, cancelled when they stall | Decided by the development agent — owner review at the walkthrough |
| [ADR-108](#adr-108--backup-credentials-stay-in-backup-pods-the-api-and-ui-only-read-runs) | Backup credentials stay in backup pods; the API and UI only read runs | Decided by the development agent — owner review at the walkthrough |
| [ADR-109](#adr-109--backup-encryption-server-side-at-mvp-client-side-for-the-database-files-at-p1) | Backup encryption: server-side at MVP, client-side for the database files at P1 | Decided by the development agent — owner review at the walkthrough |
| [ADR-110](#adr-110--restore-an-empty-installation-any-later-release-resumable-as-an-ordinary-job) | Restore: an empty installation, any later release, resumable, as an ordinary Job | Decided by the development agent — owner review at the walkthrough |
| [ADR-111](#adr-111--a-restore-moves-valkey-to-a-new-keyspace-and-offsets-every-revision) | A restore moves Valkey to a new keyspace and offsets every revision | Decided by the development agent — owner review at the walkthrough |
| [ADR-112](#adr-112--the-backup-cronjob-is-on-by-default-and-needs-its-own-target) | The backup CronJob is on by default and needs its own target | Decided by the development agent — owner review at the walkthrough |
| [ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey) | Authentication state lives in PostgreSQL and in-process caches, never in Valkey | Decided by the development agent — owner review at the walkthrough |
| [ADR-114](#adr-114--authentication-caches-answer-only-while-the-change-log-is-fresh) | Authentication caches answer only while the change log is fresh | Decided by the development agent — owner review at the walkthrough |
| [ADR-115](#adr-115--brute-force-counters-count-cluster-wide-the-break-glass-lockout-and-rotation-stay-in-postgresql) | Brute-force counters count cluster-wide; the break-glass lockout and rotation stay in PostgreSQL | Decided by the development agent — owner review at the walkthrough |
| [ADR-116](#adr-116--npm-documents-are-cached-per-replica-under-keys-read-from-postgresql) | npm documents are cached per replica under keys read from PostgreSQL | Decided by the development agent — owner review at the walkthrough |
| [ADR-117](#adr-117--an-identical-npm-publish-retried-after-a-lost-answer-is-acknowledged) | An identical npm publish retried after a lost answer is acknowledged | Decided by the development agent — owner review at the walkthrough |
| [ADR-118](#adr-118--a-restore-withholds-npm-versions-unpublished-after-the-snapshot) | A restore withholds npm versions unpublished after the snapshot | Decided by the development agent — owner review at the walkthrough |
| [ADR-119](#adr-119--cached-npm-tarballs-wait-for-a-current-package-document) | Cached npm tarballs wait for a current package document | Decided by the development agent — owner review at the walkthrough |
| [ADR-120](#adr-120--forwarded-nuget-search-results-are-cached-per-replica) | Forwarded NuGet search results are cached per replica | Decided by the development agent — owner review at the walkthrough |
| [ADR-121](#adr-121--rendered-nuget-documents-carry-weak-etags) | Rendered NuGet documents carry weak ETags | Decided by the development agent — owner review at the walkthrough |
| [ADR-122](#adr-122--a-restore-replays-nuget-publishes-and-deletes-into-tombstones) | A restore replays NuGet publishes and deletes into tombstones | Decided by the development agent — owner review at the walkthrough |
| [ADR-123](#adr-123--nuget-symbol-packages-are-indexed-by-one-job-each-on-the-worker-role) | NuGet symbol packages are indexed by one job each on the worker role | Decided by the development agent — owner review at the walkthrough |
| [ADR-124](#adr-124--releases-stay-on-major-version-0) | Releases stay on major version 0 | Accepted (owner constraint) |
| [ADR-125](#adr-125--gnu-make-and-one-pin-file-for-every-tool) | GNU Make and one pin file for every tool | Decided by the development agent — owner review at the walkthrough |
| [ADR-126](#adr-126--problem-details-with-a-relative-type-a-requestid-member-and-named-error-codes) | Problem details with a relative type, a requestId member and named error codes | Decided by the development agent — owner review at the walkthrough |
| [ADR-127](#adr-127--transport-encryption-on-every-connection-troves-listeners-included) | Transport encryption on every connection, trove's listeners included (INV-20) | Accepted (owner constraint) |
| [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) | Endpoints: one listener each, identified by the listener, with exposure rules as a ceiling (INV-6, INV-9, INV-15) | Decided by the development agent — owner review at the walkthrough |
| [ADR-129](#adr-129--the-owners-kubernetes-operators-are-the-standard-stack) | The owner's Kubernetes operators are the standard stack (dev profile, chart and production) | Accepted (owner constraint, 2026-10-29) |
| [ADR-130](#adr-130--the-organisations-ci-and-release-standard) | The organisation's CI and release standard | Accepted (owner constraint, 2026-10-29) |

</details>

## Owner constraints

### ADR-001 · Backend in Go 1.27.3

**Status:** Accepted (owner constraint)

**Context.** The owner fixes the implementation language and version of the server.

**Decision.** The backend is written in Go and built with Go 1.27.3. INV-1 records the check made
on 2026-10-28: the local `go version` and the go.dev download list both showed go1.27.3 as the
current stable release.

**Consequences.** Libraries come from the Go ecosystem; the choices are decided in
[Q-ARCH-1](spec/architecture.md#q-arch-1--http-router) to [Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation). The binary is statically linked without cgo ([R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles)).
The toolchain version is pinned in one place that local builds and CI both read; no build file
exists yet, so this is a rule for the first one.

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [ADR-012](#adr-012--one-toolchain-one-image-inv-1), [R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles),
[R-ARCH-003](spec/architecture.md#r-arch-003--go-module-and-package-layout).

### ADR-002 · Frontend in Angular 22

**Status:** Accepted (owner constraint)

**Context.** The owner fixes the UI framework and its major version.

**Decision.** The UI is an Angular 22 application. On 2026-10-28 the npm dist-tag `latest` of
`@angular/core` was 22.2.3 (INV-1).

**Consequences.** The UI follows Angular 22's defaults: standalone components, zoneless change
detection, signals ([R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery), [R-UI-046](spec/api-and-ui.md#r-ui-046--state-management-with-signals)). UI dependencies should release in step with
Angular majors, which decided [Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library) (Angular Material, the CDK and `@angular/aria`) and [Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism) (`@angular/localize`). A new Angular major is planned work that
upgrades the framework and its companion packages together.

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [ADR-012](#adr-012--one-toolchain-one-image-inv-1), [R-ARCH-004](spec/architecture.md#r-arch-004--angular-workspace-layout),
[R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery).

### ADR-003 · PostgreSQL as the database

**Status:** Accepted (owner constraint)

**Context.** The owner fixes the database.

**Decision.** All metadata is stored in PostgreSQL.

**Consequences.** [ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3) (INV-3) makes PostgreSQL the system of record, including every
mutual exclusion whose failure would corrupt data, while HA Valkey holds only shared ephemeral
state ([ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3), [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12)). Decided:
PostgreSQL 16 is the version floor ([Q-HA-2](spec/ha-and-helm.md#q-ha-2--postgresql-version-floor)), and the driver is pgx's native API
([Q-ARCH-2](spec/architecture.md#q-arch-2--postgresql-driver-and-query-layer)). Since the revision of 2026-10-28, a transaction-mode connection pooler is supported for
the main pool from the MVP on, with a direct session host for the leases of `worker` replicas,
`trove migrate` and `trove backup`, and a control endpoint of its own; statement-mode poolers are
not supported ([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), [Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler), [ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-HA-013](spec/ha-and-helm.md#r-ha-013--supported-postgresql-versions-and-server-settings), [R-HA-014](spec/ha-and-helm.md#r-ha-014--database-provisioning-roles-and-privileges).

### ADR-004 · S3 as binary storage

**Status:** Accepted (owner constraint)

**Context.** The owner fixes where package binaries live.

**Decision.** Binaries are stored in S3-compatible object storage.

**Consequences.** [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) (INV-4) lays the store out by content address. Every supported store
must pass a conformance check ([R-STOR-035](spec/storage.md#r-stor-035--object-store-consistency-assumptions-and-conformance-check)). Decided: the client library is aws-sdk-go-v2 `service/s3`
with a trove-owned multipart upload ([Q-ARCH-4](spec/architecture.md#q-arch-4--s3-client-library)), and the local development profile and per-change CI
run SeaweedFS, with the MinIO leg on every change and AWS S3 once an AWS account exists (owner decision of 2026-10-29, [ADR-130](#adr-130--the-organisations-ci-and-release-standard); [Q-HA-3](spec/ha-and-helm.md#q-ha-3--s3-component-of-the-local-profile-after-minios-archival)). Backups go to an S3 target of their own,
outside the blob prefix ([ADR-094](#adr-094--a-configurable-backup-job-writes-consistent-backups-to-s3), [ADR-112](#adr-112--the-backup-cronjob-is-on-by-default-and-needs-its-own-target)), and multi-request uploads keep their
data in S3 multipart uploads rather than on a replica ([ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-STOR-030](spec/storage.md#r-stor-030--object-store-connection-and-bucket-settings), [R-STOR-035](spec/storage.md#r-stor-035--object-store-consistency-assumptions-and-conformance-check).

### ADR-005 · High availability

**Status:** Accepted (owner constraint)

**Context.** trove sits on the build path of every pipeline that installs or publishes
packages; an outage stops builds.

**Decision.** trove runs highly available: losing one replica does not interrupt service.

**Consequences.** Replicas of a role are identical and keep no state between requests ([ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2),
[ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18)) and coordinate through PostgreSQL and Valkey ([ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12)). On 2026-10-28 the owner made
the constraint concrete and non-negotiable: HA PostgreSQL, HA Valkey with Sentinel and S3
([ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3)), a load beyond one replica with drains at any time ([ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage)). Three pages decided the same readiness rule for an object-storage outage:
object storage gates only a replica's first readiness, so running replicas stay ready during a
shared outage ([Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness), [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node), [R-HA-007](spec/ha-and-helm.md#r-ha-007--failure-mode-contract).

### ADR-006 · Deployment through a Helm chart

**Status:** Accepted (owner constraint)

**Context.** The owner fixes the deployment mechanism.

**Decision.** trove is deployed to Kubernetes with a Helm chart.

**Consequences.** [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) (INV-13) makes the chart the supported deployment and fixes what it
renders; since 2026-10-28 that is one Deployment per role, the backup CronJob and no ReadWriteMany
volume ([ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage) to [ADR-095](#adr-095--no-readwritemany-storage)). Decided: the HTTPRoute is MVP and a chart-created Gateway P1 ([Q-HA-7](spec/ha-and-helm.md#q-ha-7--priority-of-gateway-api)), and images and
chart are published to Docker Hub and to the GitHub Pages chart repository (owner decision of 2026-10-29, [ADR-130](#adr-130--the-organisations-ci-and-release-standard); [Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart), which named `ghcr.io` before it).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-HA-028](spec/ha-and-helm.md#r-ha-028--chart-scope-rendered-resources-and-naming), [R-HA-031](spec/ha-and-helm.md#r-ha-031--values-reference).

### ADR-007 · OIDC login, own tokens, detailed permissions

**Status:** Accepted (owner constraint)

**Context.** The owner requires single sign-on for people, credentials for package clients, and
fine-grained permission management.

**Decision.** Users log in through OpenID Connect; trove issues tokens; permissions are managed
in detail.

**Consequences.** Spelled out by [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7) (INV-7, human identity), [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8) (INV-8,
tokens) and [ADR-020](#adr-020--deny-by-default-allow-only-permission-policies-inv-9) (INV-9, authorization).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints),
[identity-and-authentication.md](spec/identity-and-authentication.md),
[authorization.md](spec/authorization.md).

### ADR-008 · GitLab CI jobs obtain permissions through OIDC

**Status:** Accepted (owner constraint)

**Context.** CI jobs install dependencies and publish what they build. Long-lived secrets in CI
variables leak and are rarely rotated.

**Decision.** A GitLab CI job obtains trove permissions through an OIDC procedure. The
permissions are detailed and configurable: read other packages, write the packages the job
produces.

**Consequences.** Spelled out by [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) (INV-10) and the CI entries [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint) to
[ADR-065](#adr-065--further-ci-issuers-through-the-same-mechanism-p1). "Write the packages the job produces" needs package ownership ([ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints),
[gitlab-ci-oidc.md](spec/gitlab-ci-oidc.md).

### ADR-009 · Pull-through proxy for external package sources

**Status:** Accepted (owner constraint)

**Context.** Builds depend on public registries; the owner wants them connected through trove.

**Decision.** External package sources can be connected as pull-through proxies.

**Consequences.** Spelled out by [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) (INV-11) and [ADR-035](#adr-035--virtual-resolution-in-three-tiers-decided-per-member) to [ADR-039](#adr-039--egress-address-checks-after-dns-resolution-for-every-remote-connection).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints),
[repositories-and-proxy.md](spec/repositories-and-proxy.md).

### ADR-010 · Every catalog format, npm and NuGet first

**Status:** Accepted (owner constraint)

**Context.** The owner wants every package format of the [feature catalog](features/formats/README.md)
and welcomes more, with npm and NuGet first.

**Decision.** trove targets every catalog format. npm and NuGet are the first formats and the
only ones in the MVP.

**Consequences.** [ADR-025](#adr-025--npm-and-nuget-v3-are-the-mvp-formats-inv-14) (INV-14) fixes the MVP clients. The format plug-in contract
([ADR-029](#adr-029--formats-are-compiled-in-plug-ins-behind-a-narrow-host-interface)) keeps later formats from touching the core. Later formats already constrain today's
URL layout: `/v2/` on the client listener is reserved for OCI ([Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints),
[formats/npm.md](spec/formats/npm.md), [formats/nuget.md](spec/formats/nuget.md).

### ADR-011 · trove is a self-contained, white-label product

**Status:** Accepted (owner constraint)

**Context.** The owner requires trove to stand as its own product, described in its own terms.

**Decision.** Everything in the repository (documentation, code, configuration, examples, commit
messages) uses trove's own vocabulary. It names no other repository product as model or origin,
links to no such product's documentation, and reuses none of its endpoint paths, header names,
field or property names, configuration file names, default ports or edition names. Third-party
standards and ecosystems are named and linked, with their native client commands: npm, NuGet,
OCI, Docker, Maven, PyPI, GitLab, GitHub Actions, Kubernetes, Helm, S3, PostgreSQL, the OIDC and
OAuth RFCs. The design questions that asked the owner for an example screenshot
([Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested), [Q-UI-3](spec/api-and-ui.md#q-ui-3--layout-of-the-package-details-page-example-screenshot-requested), [Q-UI-4](spec/api-and-ui.md#q-ui-4--layout-of-the-policy-editor-and-the-explainer-example-screenshot-requested)) were decided without one; the owner's walkthrough takes its place.
A screenshot the owner brings there adjusts design tokens and spacing, is described in trove's own
terms, and its origin is not recorded.

**Consequences.** The [feature catalog](features/README.md) uses its own IDs and generic names,
and protocol behavior is specified from the ecosystems' own specifications and client sources,
which the spec pages cite. A check for forbidden references cannot live in the repository: its
pattern list would itself name what it forbids. It has to run from outside the repository, for
example from a CI variable or the review tooling. Not verified: no such check exists in the
repository today.

**Alternatives considered.** None; set by the owner.

**Spec:** not yet listed among the [owner constraints](spec/README.md#owner-constraints) of
spec/README.md, which should add it in its next change; [Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested).

### ADR-092 · High availability on HA PostgreSQL, HA Valkey with Sentinel and S3

**Status:** Accepted (owner constraint)

**Context.** On 2026-10-28 the owner stated that high availability of trove on HA PostgreSQL, HA
Valkey including Sentinel, and S3 is a core requirement that cannot be deselected. It makes
[ADR-005](#adr-005--high-availability) concrete and adds Valkey to the services that [ADR-003](#adr-003--postgresql-as-the-database) and [ADR-004](#adr-004--s3-as-binary-storage) fixed.

**Decision.** trove runs highly available on HA PostgreSQL, HA Valkey with Sentinel and
S3-compatible object storage. The requirement cannot be deselected: no supported production
configuration leaves out one of the three or runs PostgreSQL or Valkey as a single instance.

**Consequences.**
- **Topology.** INV-12 names it: HA PostgreSQL (for example CloudNativePG, Patroni or a managed
  service), Valkey with one primary, at least one replica and at least three Sentinels, and S3.
  trove finds the Valkey primary through Sentinel and follows failovers. Single instances serve
  development only, and the local profile itself runs PostgreSQL with a standby and Valkey with a
  replica and three Sentinels ([ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12), [R-HA-051](spec/ha-and-helm.md#r-ha-051--local-development-profile)).
- **What Valkey may hold.** INV-3 limits Valkey to shared ephemeral state whose loss costs
  performance, never correctness or data ([ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3), [ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)). The earlier decision "No Redis"
  is superseded.
- **Degradation.** A Valkey outage degrades trove and is alerted on, but makes no replica unready
  ([ADR-100](#adr-100--valkey-degrades-trove-but-never-gates-readiness-except-for-configuration-errors)). The client and the supported server versions are [ADR-099](#adr-099--valkey-client-valkey-go-against-valkey-90-and-later-sentinel-in-production).
- **Chart.** The chart consumes the three services and bundles no production instance of any
  ([ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [INV-3, INV-12](spec/README.md#design-invariants),
[R-ARCH-030](spec/architecture.md#r-arch-030--valkey-usage-map), [R-ARCH-031](spec/architecture.md#r-arch-031--valkey-client-sentinel-discovery-tls-acl-and-degradation), [R-HA-056](spec/ha-and-helm.md#r-ha-056--valkey-with-sentinel-connection-and-supported-topologies), [R-HA-057](spec/ha-and-helm.md#r-ha-057--valkey-failover-outage-and-degraded-mode).

### ADR-093 · Horizontal scale: random distribution, split roles, drains without outage

**Status:** Accepted (owner constraint)

**Context.** On 2026-10-28 the owner stated the load model: n workers answer requests that a
Kubernetes Service distributes at random, and the expected load exceeds what one worker can carry.

**Decision.** trove runs as n replicas that answer randomly distributed requests behind a
Kubernetes Service. The service is cut for load sharing from the start. A node drain causes no
outage at any time when the setup is sized for it. There may be more than one type of worker, and
the service may be split.

**Consequences.**
- **Roles from the start.** `web` and `worker` run as separate Deployments; `all` exists only for a
  binary started outside the chart in development ([ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2), [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases)). Further splits into pools
  are values, not code ([R-HA-062](spec/ha-and-helm.md#r-ha-062--further-splits-of-a-role-into-several-deployments), P1).
- **No affinity.** No request may depend on state that an earlier request left on one replica
  (INV-18, [ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18)).
- **Connections.** PostgreSQL connections must not grow without bound with the replica count
  ([ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)).
- **Budgets, not per-replica values.** A limit an operator sets holds for the installation: rate
  limits ([ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback)) and the upstream fetch budget ([ADR-022](#adr-022--pull-through-proxy-semantics-inv-11)).
- **Drains.** The shutdown protocol ([ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica)) and the capacity rule with its defaults ([ADR-105](#adr-105--drain-capacity-one-replica-more-than-the-peak-needs-per-role))
  define what "sized for it" means ([R-HA-059](spec/ha-and-helm.md#r-ha-059--node-drain-without-outage)).
- **Caches on n replicas.** They stay correct through the change log in PostgreSQL ([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early),
  [ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed), [ADR-114](#adr-114--authentication-caches-answer-only-while-the-change-log-is-fresh)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [INV-2, INV-12, INV-18](spec/README.md#design-invariants),
[R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles), [R-ARCH-024](spec/architecture.md#r-arch-024--process-lifecycle-start-up-readiness-graceful-shutdown), [R-HA-002](spec/ha-and-helm.md#r-ha-002--role-split-into-web-and-worker-deployments), [R-HA-059](spec/ha-and-helm.md#r-ha-059--node-drain-without-outage), [R-HA-062](spec/ha-and-helm.md#r-ha-062--further-splits-of-a-role-into-several-deployments).

### ADR-094 · A configurable backup job writes consistent backups to S3

**Status:** Accepted (owner constraint)

**Context.** Point-in-time recovery of the database and versioning of the bucket protect the two
stores separately, through the platform. On 2026-10-28 the owner asked for a backup job like
GitLab's: configurable, and producing a consistent backup of the service in S3 whenever it runs.

**Decision.** A configurable backup job creates a consistent backup of the service when it runs
and stores it in S3.

**Consequences.**
- **INV-19.** The job is `trove backup create`: a transactionally consistent snapshot of trove's
  PostgreSQL data together with every blob it references, written to a configurable S3 target. The
  chart schedules it as a CronJob, and `trove backup restore` restores into an empty installation
  ([ADR-097](#adr-097--consistent-backups-to-s3-restored-into-an-empty-installation-inv-19)).
- **MVP.** Backup creation and restore are MVP, although the catalog ranks them P1 and P2. The
  revocation journal and the platform layer moved to MVP with them ([Q-HA-9](spec/ha-and-helm.md#q-ha-9--making-revocations-survive-a-database-restore-h-37), [Q-HA-19](spec/ha-and-helm.md#q-ha-19--priority-of-the-platform-backup-layer-and-the-point-in-time-restore),
  [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination)).
- **Design.** Format and garbage-collection protection, runs, credentials, encryption, restore and
  the CronJob default are [ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold) to [ADR-112](#adr-112--the-backup-cronjob-is-on-by-default-and-needs-its-own-target). Valkey is not part of a backup (INV-3).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [INV-19](spec/README.md#design-invariants),
[backup-and-restore.md](spec/backup-and-restore.md), [R-HA-060](spec/ha-and-helm.md#r-ha-060--backup-cronjob-and-restore-job).

### ADR-095 · No ReadWriteMany storage

**Status:** Accepted (owner constraint)

**Context.** On 2026-10-28 the owner stated that the clusters trove targets offer no storage class
whose volumes several pods can attach at once.

**Decision.** No ReadWriteMany storage class can be assumed, so no volume may be attached to
several pods. A worker type that needs a ReadWriteOnce volume is acceptable, but not desired.

**Consequences.**
- **Nothing to share.** No pod mounts a volume that another pod writes, and nothing a service needs
  lives on a trove volume: PostgreSQL holds metadata, S3 holds blobs, backups and the revocation
  journal, Valkey holds ephemeral state ([R-HA-061](spec/ha-and-helm.md#r-ha-061--volumes-nothing-shared-nothing-persistent)).
- **Scratch only.** Spool and temporary space are `emptyDir` or a generic ephemeral volume
  (ReadWriteOnce, deleted with its pod). The chart renders no PersistentVolumeClaim, no StatefulSet
  and no `volumeClaimTemplates`, its schema admits only `ReadWriteOnce` and `ReadWriteOncePod`, and
  CI checks the rendered output (INV-13, [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13)). No role needs a persistent volume today.
- **No hand-over through a disk.** A cut remote fetch starts over on another replica
  ([Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down)), a cut download resumes from S3 with `Range` ([ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica)), and a
  multi-request upload keeps its bytes in an S3 multipart upload ([ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18)).
- **Outside the chart.** The ReadWriteOnce claims of the PostgreSQL instances belong to their
  operator.

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [INV-13](spec/README.md#design-invariants),
[R-HA-035](spec/ha-and-helm.md#r-ha-035--spool-and-temporary-volumes), [R-HA-061](spec/ha-and-helm.md#r-ha-061--volumes-nothing-shared-nothing-persistent).

### ADR-124 · Releases stay on major version 0

**Status:** Accepted (owner constraint)

**Context.** On 2026-10-29 the owner set the release numbering: development starts with `v0.1.0`,
and there is no major release until the owner explicitly decides otherwise.

**Decision.** trove releases stay on major version 0. Every completed project phase of
[docs/projektplan/](projektplan/BETA.md) is released as the next minor version (`v0.1.0` for the
foundation, `v0.7.0` for the beta, the next minors after it); fixes in between are patch releases.
The Helm chart follows the same numbering.

**Consequences.** Semantic Versioning would allow any change within 0.x; trove does not use that
latitude. The promises of [R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy)
apply between 0.x minors as they would within a major: direct upgrade to any later minor, rolling
upgrade without downtime from N-1 to N, deprecated configuration aliases for at least two minors. A
breaking change in 0.x is announced in the release notes together with its migration path. The
roadmap and the project plans name releases by these versions.

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy),
[roadmap](coverage/roadmap.md#milestones-at-a-glance).

## Design invariants

The invariants of [spec/README.md](spec/README.md#design-invariants) are the baseline every spec
page builds on. A page may refine an invariant but must not contradict it; where a page finds one
wrong, it keeps it and raises a question, which is listed with the entry. Where the decision on
such a question changes the invariant, the entry records the new rule and the question that
changed it; the edit to spec/README.md is that question's cross-page consequence. On 2026-10-28
the owner's constraints ([ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3) to [ADR-095](#adr-095--no-readwritemany-storage)) changed INV-2, INV-3, INV-11, INV-12 and INV-13
and added INV-18 and INV-19:
- **INV-2:** roles `web`, `worker` and `backup`, split from the start; `all` only for development
  ([ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2)).
- **INV-3:** PostgreSQL is the system of record; Valkey holds shared ephemeral state only, and the
  change log in PostgreSQL replaces `LISTEN/NOTIFY` ([ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3)).
- **INV-11:** cross-replica collapsing through a Valkey lock ([ADR-022](#adr-022--pull-through-proxy-semantics-inv-11)).
- **INV-12:** HA PostgreSQL, HA Valkey with Sentinel and S3; load beyond one replica; drains
  without outage; bounded PostgreSQL connections ([ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12)).
- **INV-13:** one Deployment per role, the backup CronJob, no ReadWriteMany volume ([ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13)).
- **INV-18** (new): no state between requests on a replica ([ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18)).
- **INV-19** (new): consistent backups to S3, restored into an empty installation ([ADR-097](#adr-097--consistent-backups-to-s3-restored-into-an-empty-installation-inv-19)).

Earlier, decisions on spec pages changed INV-6, INV-8, INV-12 and INV-17, and aligned the wording
of INV-4, INV-10 and INV-15 with decisions that refine them:
- **INV-4** wording: SHA-512 for every blob ([Q-STOR-2](spec/storage.md#q-stor-2--sha-512-for-every-blob-or-only-on-demand); [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4)).
- **INV-6:** the npm exchange prefix and the `/v2/` reservation at the host root, and trove owns
  its host root ([Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6), [Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci); [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6)). [Q-ARCH-11](spec/architecture.md#q-arch-11--url-layout-and-port-numbers-inv-6) confirmed the layout
  and the ports unchanged.
- **INV-8:** the `__Host-` cookie names ([Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name); [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8)).
- **INV-10** wording: the `CI_JOB_TOKEN` fallback is P2 ([Q-CI-5](spec/gitlab-ci-oidc.md#q-ci-5--build-the-ci_job_token-fallback-at-all); [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10)).
- **INV-12:** the readiness rule ([Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness), [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure); [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12)).
- **INV-15** wording: login providers are file configuration only ([Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives); [ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15)).
- **INV-17:** public, documented endpoints instead of only `/api/v1` ([Q-API-4](spec/api-and-ui.md#q-api-4--inv-17s-wording-and-byte-transfers-on-files); [ADR-028](#adr-028--the-ui-uses-only-the-public-api-inv-17)).

On 2026-10-29 the endpoints of [spec/endpoints.md](spec/endpoints.md) changed three invariants
([ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling)):
- **INV-6:** one client listener per endpoint, the endpoint identified by the listener, the
  `/.well-known/` reservation and host-root bindings ([Q-EP-1](spec/endpoints.md#q-ep-1--how-the-endpoint-of-a-request-is-identified), [Q-EP-6](spec/endpoints.md#q-ep-6--how-far-wildcard-hosts-and-host-root-bindings-go)).
- **INV-9:** the effective permission is also intersected with the endpoint's exposure, for every
  subject ([Q-EP-12](spec/endpoints.md#q-ep-12--whether-administrators-pass-the-exposure-ceiling)).
- **INV-15:** endpoints in the file, their exposure rules in PostgreSQL ([Q-EP-2](spec/endpoints.md#q-ep-2--where-endpoint-configuration-and-exposure-rules-live)).

### ADR-012 · One toolchain, one image (INV-1)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** UI, management API and package protocols could ship as separate deliverables with
separate versions.

**Decision.** Go 1.27.3 and Angular 22. The Angular build is embedded into the Go binary with
`embed`, so one container image serves the UI, the management API and every package protocol.

**Consequences.** UI and server are versioned, tested and rolled back together; the UI cannot be
deployed on its own. UI and API share one origin, so no cross-origin setup is needed
([ADR-030](#adr-030--one-origin-for-ui-api-and-package-content)). Each UI locale adds a build to the binary ([Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism)).

**Alternatives considered.** A separate UI image behind the edge: independent UI releases, but two
artifacts to keep compatible and either path routing at the edge or cross-origin requests. Serving
the UI from a CDN: breaks air-gapped installations, which [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery) supports explicitly.

**Spec:** [INV-1](spec/README.md#design-invariants), [R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles), [R-ARCH-004](spec/architecture.md#r-arch-004--angular-workspace-layout),
[R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery).

### ADR-013 · One binary, roles split from the start, identical replicas per role (INV-2)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** High availability ([ADR-005](#adr-005--high-availability)) needs replicas that can be added, removed and
replaced without coordination steps. Changed by the owner's constraints of 2026-10-28
([ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage)): the load exceeds one replica, requests reach replicas at random, and several worker
types and a split service are allowed. Until then every replica served HTTP and background work,
the chart's default role was `all`, and the split into `api` and `worker` Deployments was P1. That
default and that priority are replaced; the client-facing role is now named `web`.

**Decision.** One binary and one container image, `trove`, with the subcommands `serve`,
`migrate`, `backup` and `admin`. A role is a set of enabled subsystems chosen by configuration, not
a separate build:
- **`web`** serves every HTTP request: UI, management API, package protocols and raw files.
- **`worker`** runs the background work of the job queue and holds the leases.
- **`backup`** runs the backup and restore commands as a Kubernetes Job or CronJob.

`process.role` has no default, and `trove serve` refuses to start without `web`, `worker` or `all`.
The chart deploys `web` and `worker` as separate Deployments from the start, each with its own
replica count, autoscaler, disruption budget, grace period and topology spread, and never renders
`all`, which exists only for a binary started outside the chart during development. Every replica
of a role is identical and interchangeable; there is no primary.

**Consequences.** Scaling is the replica count of a role, and each role scales and drains on its
own. Which subsystem runs in which role is [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases). Anything a later request needs lives in
PostgreSQL, Valkey (ephemeral only) or object storage; in-process state is limited to rebuildable
caches and the scratch space of the running request or job ([ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18)). Work that must run once
runs as a job or under a lease on a `worker` replica ([ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work), [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing)). The role split is MVP
([R-HA-002](spec/ha-and-helm.md#r-ha-002--role-split-into-web-and-worker-deployments), whose heading keeps the earlier name `api`), and splitting a role into pools is P1
([R-HA-062](spec/ha-and-helm.md#r-ha-062--further-splits-of-a-role-into-several-deployments)). The replica registry's table, heartbeat, pruning of stale rows and read-only API are
MVP, and so is the UI's system status screen that shows them ([Q-HA-12](spec/ha-and-helm.md#q-ha-12--replica-registry-at-mvp), option (c); [R-UI-038](spec/api-and-ui.md#r-ui-038--system-status)).
Since 2026-10-28 the registry also records each replica's node and the Sentinels it reaches, so the
status screen groups replicas by role and states drain safety per role ([Q-UI-7](spec/api-and-ui.md#q-ui-7--roles-drain-readiness-and-valkey-on-the-system-status-screen), option (b)).

**Alternatives considered.** A primary node with failover: an election to get right and a single
point of failure while it runs. A separate worker binary or image: two artifacts for one release.
Peer membership between replicas (gossip, consensus): needs replica-to-replica networking, which
this design avoids entirely ([R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node)). `all` as the chart's default with an optional split, the
earlier decision: background work competes with request latency for CPU and memory, and one drain
removes both functions from a node at once.

**Spec:** [INV-2](spec/README.md#design-invariants), [R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles), [R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node),
[R-HA-002](spec/ha-and-helm.md#r-ha-002--role-split-into-web-and-worker-deployments), [R-HA-003](spec/ha-and-helm.md#r-ha-003--replica-registry-as-the-coordination-view), [R-HA-062](spec/ha-and-helm.md#r-ha-062--further-splits-of-a-role-into-several-deployments); decided: [Q-HA-12](spec/ha-and-helm.md#q-ha-12--replica-registry-at-mvp), [Q-UI-7](spec/api-and-ui.md#q-ui-7--roles-drain-readiness-and-valkey-on-the-system-status-screen).

### ADR-014 · PostgreSQL is the system of record; Valkey holds only shared ephemeral state (INV-3)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Configuration, metadata, identities, audit, jobs and coordination all need durable,
shared state. Each additional stateful system needs its own HA, backup and upgrade story. Changed
by the owner's constraints of 2026-10-28 ([ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3)): HA Valkey with Sentinel is part of every
production installation. Until then this entry decided "No Redis": PostgreSQL held all
cross-replica coordination, including the fetch lock of cache fills and `LISTEN/NOTIFY`
invalidation. That decision is superseded; Valkey takes part within the limits below.

**Decision.** PostgreSQL is the system of record. It holds repository configuration, package and
file metadata, users, groups, tokens, permissions, audit events, the durable job queue, the change
log that carries cross-replica invalidation, and backup run records; the backup target, not the
database, is the record of the backups themselves. Valkey holds shared ephemeral state only:
- cross-replica request collapsing;
- the negative cache of remote repositories;
- rate-limit counters with cluster scope;
- the fast path of change messages and signals;
- short-lived coordination.

Three rules bound Valkey:
- **Nothing served, nothing that grants.** No content, metadata or credential that trove serves,
  and no positive authentication or authorization result, is read from Valkey.
- **Never the only copy.** Losing Valkey or its contents (an outage, a Sentinel failover with
  asynchronous replication, a flush, a restart) costs performance only, never correctness or data.
- **Exclusion stays in PostgreSQL.** Mutual exclusion whose failure would corrupt data (schema
  migrations, singleton jobs that must not run twice) uses PostgreSQL.

No shared filesystem and no embedded database. A backup does not include Valkey. Migrations are
embedded in the binary and applied by `trove migrate` (Helm pre-install and pre-upgrade Job) under
a PostgreSQL advisory lock. Migrations follow expand/contract, so N and N-1 run side by side during
a rolling upgrade.

**Consequences.** PostgreSQL's availability is trove's availability for nearly every request;
Valkey's is not ([ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12), [ADR-100](#adr-100--valkey-degrades-trove-but-never-gates-readiness-except-for-configuration-errors)). The change log replaces `NOTIFY`: a writing
transaction increments its change area's epoch and inserts a change row, every replica polls the
log, and a Valkey message can only evict early ([Q-ARCH-15](spec/architecture.md#q-arch-15--cross-replica-invalidation-without-listennotify), [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)). [R-ARCH-030](spec/architecture.md#r-arch-030--valkey-usage-map)
assigns every cross-replica mechanism to one of the two stores ([ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)). A connection budget per
role is part of sizing ([R-HA-016](spec/ha-and-helm.md#r-ha-016--connection-budget-and-pool-sizing), [ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)). Decided:
- **Query layer** ([Q-ARCH-2](spec/architecture.md#q-arch-2--postgresql-driver-and-query-layer), option (d); revisited on 2026-10-28, unchanged). A hybrid on
  pgx's native API: sqlc for every static query, and one confined builder, `listq`, for the
  dynamic list and search queries, with bound values and allowlisted identifiers. trove no longer
  uses `LISTEN`, and pgx's exec modes let the main pool run through a transaction-mode pooler.
- **Counters stay in PostgreSQL** ([Q-STOR-12](spec/storage.md#q-stor-12--counters-that-several-replicas-update-usage-quotas-and-download-statistics), option (a)). Usage and quota counters are
  append-only deltas that every `worker` replica folds every 5 s, because a quota must survive a
  loss of Valkey's data; a restore recounts them.
- **No `database/sql`.** Neither `database/sql` nor `pgx/v5/stdlib` is imported anywhere, which an
  architecture test enforces ([R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget)).
- **Migration runner.** A trove-owned runner on native pgx ([Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner), [ADR-033](#adr-033--versioning-promises-per-surface-and-a-schema-compatibility-gate)).
- **Version floor.** PostgreSQL 16 ([Q-HA-2](spec/ha-and-helm.md#q-ha-2--postgresql-version-floor)).

**Alternatives considered.** PostgreSQL as the only stateful dependency besides object storage
("No Redis"), the earlier decision: every replica held a `LISTEN` session and a pool of
session-locked fetch connections, so direct sessions grew with the replica count, which INV-12
now forbids; superseded by the owner's constraint. Valkey as a second system of record (sessions,
counters, the job queue): a Sentinel failover with asynchronous replication can lose acknowledged
writes. A coordination service such as etcd: the same cost, and not the service the owner named.
An embedded database per replica: no shared state, so no HA.

**Spec:** [INV-3](spec/README.md#design-invariants), [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-ARCH-018](spec/architecture.md#r-arch-018--in-process-caches-and-cross-replica-invalidation), [R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget),
[R-ARCH-026](spec/architecture.md#r-arch-026--schema-migrations-and-the-compatibility-gate), [R-ARCH-030](spec/architecture.md#r-arch-030--valkey-usage-map), [R-HA-013](spec/ha-and-helm.md#r-ha-013--supported-postgresql-versions-and-server-settings), [R-HA-018](spec/ha-and-helm.md#r-ha-018--expandcontract-schema-migrations), [R-STOR-003](spec/storage.md#r-stor-003--postgresql-schema-for-repositories-items-packages-blobs-references-and-properties);
decided: [Q-ARCH-2](spec/architecture.md#q-arch-2--postgresql-driver-and-query-layer), [Q-ARCH-15](spec/architecture.md#q-arch-15--cross-replica-invalidation-without-listennotify), [Q-STOR-12](spec/storage.md#q-stor-12--counters-that-several-replicas-update-usage-quotas-and-download-statistics).

### ADR-015 · Content-addressed blob store on S3 (INV-4)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The same binary is often reachable through several repositories and paths (local
copies, promotions, cached upstream content). Storing it per path multiplies storage and makes copy
and move expensive.

**Decision.** Binaries are content-addressed by SHA-256 under
`<prefix>/sha256/<h0h1>/<h2h3>/<64 hex>` and stored once. An upload is spooled to local temporary
storage while SHA-256, SHA-1, MD5 and SHA-512 are computed in one pass, then written to its
address unless the blob already exists. PostgreSQL holds every reference; garbage
collection deletes unreferenced blobs after a grace period. No binary content in PostgreSQL.

**Consequences.** Deduplication across repositories; copy and move are metadata operations.
Garbage collection becomes a correctness-critical component ([ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period)). storage.md refines the
invariant: the database row, not the object store, decides whether a blob exists ([ADR-041](#adr-041--the-database-row-decides-whether-a-blob-exists)).
Each replica needs spool disk, an `emptyDir` or a generic ephemeral volume that lives and dies with
its pod ([ADR-095](#adr-095--no-readwritemany-storage)), which trove does not encrypt ([H-2](spec/architecture.md#h-2--the-upload-spool-is-not-encrypted-by-trove)). Upload timing reveals
whether content is already stored ([H-13](spec/storage.md#h-13--deduplication-reveals-the-existence-of-known-content-through-upload-timing)). Decided: SHA-512 is computed and stored for every blob,
because both MVP formats need it ([Q-STOR-2](spec/storage.md#q-stor-2--sha-512-for-every-blob-or-only-on-demand), option (a)), which refined INV-4's earlier "where a format needs it".

**Alternatives considered.** Object keys that mirror repository paths: browsable, but no
deduplication and copy or move become object copies. Small blobs in PostgreSQL: two storage paths
and a much larger database backup. A shared filesystem: excluded by the owner constraint and by
INV-3.

**Spec:** [INV-4](spec/README.md#design-invariants), [R-STOR-001](spec/storage.md#r-stor-001--content-addressed-object-key-layout), [R-STOR-002](spec/storage.md#r-stor-002--digest-set-per-blob-and-per-file),
[R-STOR-004](spec/storage.md#r-stor-004--upload-pipeline-admission-spool-and-single-pass-hashing), [R-STOR-005](spec/storage.md#r-stor-005--blob-commit-protocol-and-deduplication); decided: [Q-STOR-2](spec/storage.md#q-stor-2--sha-512-for-every-blob-or-only-on-demand), [Q-ARCH-4](spec/architecture.md#q-arch-4--s3-client-library).

### ADR-016 · Repository model: local, remote, virtual (INV-5)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Teams need hosted repositories, proxies of public registries, and one URL that combines
both.

**Decision.** Every repository has a globally unique key matching `^[a-z0-9][a-z0-9._-]{1,63}$`,
exactly one package format and one class: `local` (hosted), `remote` (pull-through proxy and cache
of one upstream) or `virtual` (ordered aggregation of local and remote repositories of the same
format, with one default deployment target). Federated repositories are not part of the MVP.

**Consequences.** The key appears in every URL ([ADR-017](#adr-017--url-layout-on-two-listeners-inv-6)) and every policy; class and format
are fixed at creation ([R-REPO-001](spec/repositories-and-proxy.md#r-repo-001--repository-classes-format-and-immutable-identity)). Clients point at a virtual repository and get internal and
public packages from one address ([ADR-035](#adr-035--virtual-resolution-in-three-tiers-decided-per-member)).

**Alternatives considered.** Multi-format repositories: one address per team, but every request
needs format detection, and patterns and permissions would mean different things per format. Keys
unique per format only: a key would no longer identify one repository in `/files/` paths and in
policies.

**Spec:** [INV-5](spec/README.md#design-invariants), [R-REPO-001](spec/repositories-and-proxy.md#r-repo-001--repository-classes-format-and-immutable-identity), [R-REPO-002](spec/repositories-and-proxy.md#r-repo-002--repository-keys-and-client-addresses),
[R-REPO-017](spec/repositories-and-proxy.md#r-repo-017--virtual-repository-membership), [R-REPO-021](spec/repositories-and-proxy.md#r-repo-021--default-deployment-target).

### ADR-017 · URL layout on two listeners (INV-6)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Many protocols, the management API, raw file access, the browser login and the UI
share one host; operational endpoints must not be reachable through the public edge.

**Decision.** Client listener on port 8080: `/api/v1/…` (management API, with the OpenAPI 3.1
document at `/api/v1/openapi.json` as the source of truth), `/api/<format>/<repoKey>/…` (package
protocols), `/files/<repoKey>/<path>` (raw file access), `/auth/…` (browser login), `/ui/…`
(Angular UI). Operations listener on port 8081: `/healthz`, `/readyz`, `/metrics`. Host-root routes
exist only where a client fixes the path, and none carries a repository key:
`/-/npm/v1/oidc/token/exchange/package/…` for npm's trusted-publishing exchange
([Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6)), and `/v2/`, reserved from the MVP on for the OCI distribution API and answering
`404` until the OCI format exists ([Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci)). Both amend INV-6. The layout and the ports were
reviewed and confirmed unchanged ([Q-ARCH-11](spec/architecture.md#q-arch-11--url-layout-and-port-numbers-inv-6), option (a)). Amended on 2026-10-29
([ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling)): port 8080 is the default endpoint's listener, each further endpoint
listens on a port of its own, the endpoint is the listener's, `/.well-known/` is reserved, and a host
may bind its root to one repository.

**Consequences.** Every protocol is namespaced per format and repository, so formats cannot
collide. npm's host-root exchange prefix is part of INV-6 ([Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6)), OCI `/v2/` per [Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci);
trove owns its host root (`server.base_url` is an origin, [R-OPS-005](spec/operations-and-observability.md#r-ops-005--external-base-url-and-forwarded-hosts)). A route-table test rejects any
other client-listener pattern under `/v2/`, because ServeMux's conflict check does not catch a more
specific pattern ([R-HA-041](spec/ha-and-helm.md#r-ha-041--container-registry-routing-at-the-edge)). Protocols and the management API share the `/api/` prefix, so
format IDs matching `^v[0-9]+$` are refused at registration, and ServeMux panics on a conflicting
registration, so a collision cannot ship. With metrics authentication off by default, the operations
listener relies on the chart's NetworkPolicy ([H-41](spec/operations-and-observability.md#h-41--egress-is-unrestricted-and-ingress-enforcement-is-verified-on-one-cni-only)).

**Alternatives considered.** One host name per repository: the host root becomes the registry
root, at the cost of DNS, TLS and Ingress objects per repository. One format at the host root:
limits an installation to one format per host. Metrics on the client listener: exposes operational
data through the edge. From [Q-ARCH-11](spec/architecture.md#q-arch-11--url-layout-and-port-numbers-inv-6): formats at the top level (`/<format>/<repoKey>/`), where
format IDs compete with every host-root route a client fixes; a format-free prefix
(`/r/<repoKey>/`), which hides the format from edge rules; another operations port, where every
candidate is some tool's default. Resemblance to other tools is functional, and every candidate
layout matches some tool; the fallback, should the owner object at the walkthrough, is formats at
the top level, before any client is configured. From [Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci): a subdomain per repository, a
separate OCI port, or a dedicated registry host, each adding DNS names, certificates or ports for
every installation.

**Spec:** [INV-6](spec/README.md#design-invariants), [R-ARCH-005](spec/architecture.md#r-arch-005--http-listeners-routing-and-middleware-chain), [R-API-001](spec/api-and-ui.md#r-api-001--base-path-versions-and-compatibility),
[R-NPM-001](spec/formats/npm.md#r-npm-001--registry-endpoint-routing-and-repository-classes), [R-NUGET-001](spec/formats/nuget.md#r-nuget-001--nuget-repositories-and-endpoint-layout), [R-HA-041](spec/ha-and-helm.md#r-ha-041--container-registry-routing-at-the-edge), [R-OPS-005](spec/operations-and-observability.md#r-ops-005--external-base-url-and-forwarded-hosts); decided:
[Q-ARCH-11](spec/architecture.md#q-arch-11--url-layout-and-port-numbers-inv-6), [Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6), [Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci).

### ADR-018 · Humans authenticate only through an external OIDC provider (INV-7)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Organizations already run an identity provider with MFA, joiner and leaver processes
and group management. A second password store duplicates all of that.

**Decision.** Humans log in through an external OpenID Connect provider (authorization code flow
with PKCE; for example Keycloak, Microsoft Entra ID, GitLab, Dex). trove is a relying party and
stores no user passwords. Users are created just in time from ID-token claims; group membership
comes from a configurable claim and maps to trove groups. One local break-glass administrator
exists for bootstrap and recovery ([ADR-047](#adr-047--a-break-glass-administrator-outside-the-database)).

**Consequences.** An identity-provider outage blocks new UI logins; existing tokens and the
break-glass account keep working. Offboarding depends on the provider; without SCIM (P1,
[R-AUTH-039](spec/identity-and-authentication.md#r-auth-039--scim-20-provisioning)) a removed user keeps tokens until dormancy or manual disabling ([H-16](spec/identity-and-authentication.md#h-16--idp-side-deactivation-without-scim)).
The strength of the provider's authentication is not checked ([H-18](spec/identity-and-authentication.md#h-18--authentication-strength-at-the-idp-is-not-checked)). Decided:
- **Missing group claim** ([Q-AUTH-4](spec/identity-and-authentication.md#q-auth-4--missing-group-claim), option (a)). A login without the group claim clears the
  provider's synced groups and continues. It writes the warning event `user.groups.claim_missing`
  and shows a `groupClaimMissing` notice on the user's profile. `keep` and `deny` remain
  per-provider settings. Fewer rights is the safe direction for an unknown group set, and denying
  the login would lock out the people who have to fix the mapper.
- **Provider configuration** ([Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives), option (a)). Login providers, with their claim mapping,
  gates and group rules, are file-only. No API call or configuration document creates or changes
  one ([ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15)).
- **Dormancy** ([Q-AUTH-5](spec/identity-and-authentication.md#q-auth-5--dormancy-default), option (a)). On by default with 90 days, P1 together with SCIM. Users
  without an interactive login for that long are disabled, and their next OIDC login re-enables
  them. SCIM users, service accounts and CI tokens are exempt.

**Alternatives considered.** Local users with passwords: password storage, reset and MFA to build
and secure. LDAP bind: user passwords pass through trove. Protocols other than OIDC were not
specified; the named identity providers all speak OIDC.

**Spec:** [INV-7](spec/README.md#design-invariants), [R-AUTH-004](spec/identity-and-authentication.md#r-auth-004--provider-configuration-and-discovery), [R-AUTH-006](spec/identity-and-authentication.md#r-auth-006--callback-handling-and-id-token-validation),
[R-AUTH-007](spec/identity-and-authentication.md#r-auth-007--claim-mapping-and-just-in-time-provisioning), [R-AUTH-008](spec/identity-and-authentication.md#r-auth-008--group-synchronization-from-claims), [R-AUTH-014](spec/identity-and-authentication.md#r-auth-014--break-glass-administrator), [R-AUTH-021](spec/identity-and-authentication.md#r-auth-021--dormant-user-deactivation); decided:
[Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives), [Q-AUTH-4](spec/identity-and-authentication.md#q-auth-4--missing-group-claim), [Q-AUTH-5](spec/identity-and-authentication.md#q-auth-5--dormancy-default).

### ADR-019 · trove issues opaque, prefixed tokens (INV-8)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Package managers present static credentials and cannot run an OAuth refresh. Identity
provider access tokens are short-lived, audience-bound to other services and not revocable by
trove.

**Decision.** trove issues its own credentials and never accepts an identity provider's access
token on package endpoints. Every token is an opaque random string with a type prefix
(`trv_pat_` personal, `trv_sat_` service account, `trv_cit_` CI), followed by 32 random bytes in
base62 and a 6-character base62 CRC32 checksum, so secret scanners can recognize and pre-validate a
leaked token offline. Tokens are stored as SHA-256 hashes, revocable immediately, and carry an
expiry and a scope. UI sessions are separate: a server-side row referenced by the
`__Host-trv_session` cookie. Clients present a token as Bearer, as the HTTP Basic password, or as
`X-NuGet-ApiKey`.

**Consequences.** Every package client needs a token from trove: created in the UI, through
command-line login ([ADR-049](#adr-049--command-line-login-through-the-browser-session-p1)) or through the CI exchange ([ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10)). Tokens are bearer
credentials ([H-19](spec/identity-and-authentication.md#h-19--tokens-are-bearer-credentials)). A prefix never changes meaning; new types get new prefixes ([R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy)).
Decided:
- **Cookie prefix** ([Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name), option (c)). All three cookies carry the `__Host-` prefix, which
  amends INV-8's cookie name.
- **No refresh tokens** ([Q-AUTH-3](spec/identity-and-authentication.md#q-auth-3--refresh-tokens-or-rotation), option (a)). Rotation and repeated login are the renewal
  paths, and no endpoint returns a `refresh_token`.
- **Only the owner gets a PAT's secret** ([Q-AUTH-7](spec/identity-and-authentication.md#q-auth-7--administrators-issuing-tokens-for-users), option (a)). Administrators can neither create
  nor rotate another user's PAT, because either would let them act as that person in every audit
  record. Automation uses service accounts.

**Alternatives considered.** Accepting identity-provider access tokens on package endpoints:
clients cannot refresh them, and revocation depends on the provider. Self-contained signed tokens:
immediate revocation still needs a lookup per request, so the signature adds size without saving
the lookup.

**Prefix.** The owner chose `trv_` on 2026-10-28, when the project was renamed to trove (also
for the cookies: `__Host-trv_session`, `__Host-trv_csrf` and `__Host-trv_oidc_binding`, where the
browser-defined `__Host-` prefix precedes the owner's naming). Alternatives were `trove_`
(self-explanatory in a leak report, longer) and keeping the pre-rename `apkg_` (no longer
explains itself). With `trv_` a token is 57 characters long.

**Spec:** [INV-8](spec/README.md#design-invariants), [R-AUTH-001](spec/identity-and-authentication.md#r-auth-001--credential-sources-and-precedence), [R-AUTH-022](spec/identity-and-authentication.md#r-auth-022--token-types-and-format),
[R-AUTH-023](spec/identity-and-authentication.md#r-auth-023--token-storage-validation-and-caching), [R-AUTH-024](spec/identity-and-authentication.md#r-auth-024--token-scopes), [R-AUTH-027](spec/identity-and-authentication.md#r-auth-027--token-creation), [R-AUTH-030](spec/identity-and-authentication.md#r-auth-030--token-rotation),
[R-AUTH-033](spec/identity-and-authentication.md#r-auth-033--client-authentication-matrix); decided: [Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name), [Q-AUTH-3](spec/identity-and-authentication.md#q-auth-3--refresh-tokens-or-rotation), [Q-AUTH-7](spec/identity-and-authentication.md#q-auth-7--administrators-issuing-tokens-for-users).

### ADR-020 · Deny by default, allow-only permission policies (INV-9)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Permissions must express "this group reads everything in `npm-all`" as well as "this CI
project writes only `@acme/billing-*`", and administrators must be able to predict and audit the
result.

**Decision.** Deny by default. Permissions are allow-only grants: a permission policy binds
principals (users, groups, service accounts, CI identity mappings) to resources (repositories by
key or pattern, narrowed by include and exclude patterns on paths or package names) and the actions
`read`, `annotate`, `deploy`, `overwrite`, `delete`, `manage`. The effective permission is the
union of the grants of the principal and its groups, intersected with the presented token's scope
and, since 2026-10-29, with the exposure of the endpoint the request arrived on, for every subject
([ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling)).
System administration is a separate role, not a repository action. Every decision can be audited.

**Consequences.** Without deny rules there is no rule order to get wrong; restricting means
narrowing an allow. Actions do not imply each other ([ADR-051](#adr-051--actions-are-explicit-and-a-token-scope-only-narrows)). A decision explainer is part of
the API ([R-AUTHZ-024](spec/authorization.md#r-authz-024--effective-permissions-and-decision-explanation)). Decided ([Q-AUTHZ-3](spec/authorization.md#q-authz-3--built-in-principal-for-all-authenticated-users), option (d), MVP): a built-in principal
`authenticated` covers every user, through a UI session or a personal access token, and never a
service account or a CI token ([R-AUTHZ-030](spec/authorization.md#r-authz-030--the-authenticated-principal)). Its entries may hold only `read`. It takes no roles and no
ownership, and delegated managers and project administrators cannot grant to it. A deny-by-default
installation thereby works on day one without an all-employees group claim. Every right of a
non-human credential still traces back to a grant that names it, its groups or its CI mapping.
Like `anonymous`, it refines INV-9 as an implicit group.

**Alternatives considered.** Allow and deny rules with precedence: more expressive, but the outcome
depends on evaluation order and is hard to review. Fixed per-repository roles (reader, writer,
admin): cannot narrow by package name, which CI grants need. A general policy language: powerful,
but not something repository administrators can review in a form.

**Spec:** [INV-9](spec/README.md#design-invariants), [R-AUTHZ-001](spec/authorization.md#r-authz-001--deny-by-default-allow-only-grants), [R-AUTHZ-006](spec/authorization.md#r-authz-006--repository-actions),
[R-AUTHZ-013](spec/authorization.md#r-authz-013--token-scope-as-a-ceiling), [R-AUTHZ-015](spec/authorization.md#r-authz-015--evaluation-algorithm), [R-AUTHZ-030](spec/authorization.md#r-authz-030--the-authenticated-principal); decided: [Q-AUTHZ-3](spec/authorization.md#q-authz-3--built-in-principal-for-all-authenticated-users).

### ADR-021 · GitLab CI identity through ID-token exchange (INV-10)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** [ADR-008](#adr-008--gitlab-ci-jobs-obtain-permissions-through-oidc) requires CI permissions without stored secrets. GitLab offers two job
credentials: an OIDC ID token that third parties verify offline, and the job token, which only
GitLab itself can verify.

**Decision.** A GitLab job requests an ID token with the `id_tokens:` keyword (audience = trove's
base URL) and exchanges it at `POST /api/v1/auth/oidc/token` for a short-lived `trv_cit_` token.
trove verifies the signature against the configured issuer's JWKS and checks `iss`, `aud`, `exp`,
`nbf`, `iat`. Identity mappings match claims (`project_path`, `namespace_path`, `ref`, `ref_type`,
`ref_protected`, `environment`, `pipeline_source`, `user_login`, …) and grant permissions,
optionally templated from claims. Verifying a raw `CI_JOB_TOKEN` against the GitLab API is an
optional fallback, off by default and P2.

**Consequences.** No CI secret is stored anywhere. trove trusts the GitLab instance's signing key
([H-24](spec/gitlab-ci-oidc.md#h-24--trust-in-the-gitlab-instance-and-its-signing-key)). A CI token can outlive its job by its remaining lifetime ([H-26](spec/gitlab-ci-oidc.md#h-26--ci-tokens-outlive-the-job), [ADR-063](#adr-063--a-ci-tokens-lifetime-is-bounded-by-its-job)). Decided:
- **Job-token fallback** ([Q-CI-5](spec/gitlab-ci-oidc.md#q-ci-5--build-the-ci_job_token-fallback-at-all), option (b)). It stays specified, is P2 and is built only on a
  concrete need. Until then the job-token subject type answers `unsupported_subject_token_type`,
  and enabling the mode is refused, so no configuration suggests a mode that does not exist.
- **Several audiences** ([Q-CI-3](spec/gitlab-ci-oidc.md#q-ci-3--default-for-id-tokens-with-several-audiences), option (c)). A token with several audiences is accepted only if
  every one of them is an allowed audience of the CI issuer (`exclusive_audience`, default
  `true`). A recipient other than trove could replay such a token. Two host names of one trove
  installation are harmless.
- **Key sets across replicas and restores** ([Q-CI-12](spec/gitlab-ci-oidc.md#q-ci-12--ci-issuer-key-sets-across-replicas-and-restores), option (a), 2026-10-28). One PostgreSQL row
  per CI issuer holds its key set, and every replica keeps an in-process copy; nothing of it is
  read from Valkey. The `worker` task `ci-keys-refresh` refreshes the row outside any transaction,
  and a key store locks only the key row. An unknown `kid` first re-reads the row, throttled per
  issuer and replica to one read per poll interval, and only then triggers a throttled per-replica
  fetch. The row is `transient` in backups, so a restored installation fetches current keys.

**Alternatives considered.** Long-lived service-account tokens in CI variables: the leak-prone
practice this replaces. The job token as the primary path: a GitLab API call per exchange, a GitLab
credential in trove's hands ([H-28](spec/gitlab-ci-oidc.md#h-28--the-fallback-hands-trove-a-gitlab-credential)), no way to recognize fork pipelines ([H-29](spec/gitlab-ci-oidc.md#h-29--the-job-token-fallback-cannot-recognize-fork-pipelines)), and never
safe for writes. Accepting the ID token directly on package endpoints: contradicts INV-8, and not
every package client can present it.

**Spec:** [INV-10](spec/README.md#design-invariants), [gitlab-ci-oidc.md](spec/gitlab-ci-oidc.md),
[R-CI-001](spec/gitlab-ci-oidc.md#r-ci-001--ci-issuer-configuration), [R-CI-003](spec/gitlab-ci-oidc.md#r-ci-003--discovery-signing-keys-caching-and-rotation), [R-CI-004](spec/gitlab-ci-oidc.md#r-ci-004--subject-token-validation), [R-CI-007](spec/gitlab-ci-oidc.md#r-ci-007--token-exchange-endpoint), [R-CI-009](spec/gitlab-ci-oidc.md#r-ci-009--identity-mapping-model-and-evaluation-order), [R-CI-022](spec/gitlab-ci-oidc.md#r-ci-022--ci_job_token-fallback); decided:
[Q-CI-3](spec/gitlab-ci-oidc.md#q-ci-3--default-for-id-tokens-with-several-audiences), [Q-CI-5](spec/gitlab-ci-oidc.md#q-ci-5--build-the-ci_job_token-fallback-at-all), [Q-CI-12](spec/gitlab-ci-oidc.md#q-ci-12--ci-issuer-key-sets-across-replicas-and-restores).

### ADR-022 · Pull-through proxy semantics (INV-11)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** CI runs install the same public packages thousands of times, often many at once on a
cold cache, and must keep working when the upstream is slow or down. Changed by the owner's
constraints of 2026-10-28 ([ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3), [ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage)): INV-11 named a PostgreSQL advisory lock across
replicas, held on a dedicated session for the whole transfer. With many `web` replicas those
sessions grew with the replica count, which INV-12 now forbids, so the lock moved to Valkey.

**Decision.** A remote repository fetches from its upstream on a cache miss and stores the binary in
the blob store. It caches metadata for a configurable period, caches misses (negative cache), and
serves stale metadata when the upstream is unreachable. Concurrent misses for the same object are
collapsed: in-process single-flight plus a Valkey lock across replicas. A lock lost in a Valkey
failover only causes a duplicate upstream fetch, which content addressing makes harmless. URLs in
upstream metadata are rewritten to point at trove.

**Consequences.** The upstream sees one request per object, whatever the number of replicas and
concurrent clients, while Valkey is available; without Valkey, at most one per replica and object.
Clients never learn upstream URLs. Decided:
- **The flight lock** ([Q-ARCH-6](spec/architecture.md#q-arch-6--cross-replica-collapse-holds-a-connection-for-the-whole-upstream-transfer), revisited on 2026-10-28: option (d) instead of (a)). One
  Valkey lock per flight with a 30 s TTL: `SET … NX PX` acquires it, the holder extends it with
  `SET … IFEQ` every third of the TTL, and `DELIFEQ` releases it with the outcome. Waiters subscribe
  to the flight's channel and poll. The lock sits behind `cluster.FlightLock`, so the proxy never
  sees the mechanism. No PostgreSQL connection is held while acquiring, waiting or transferring,
  and the lock pool is gone. Without Valkey, collapsing works per replica ([R-ARCH-033](spec/architecture.md#r-arch-033--cross-replica-request-collapsing-through-valkey-flightlock)).
- **The key** ([Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills), revisited on 2026-10-28: option (c) for the fetch lock). The Valkey key
  `trove:<ks>:lock:fetch:v1:<SHA-256 hex of the flight key>` has no collisions. The advisory-lock
  category 2 `fetch` is retired and its number stays reserved; every remaining PostgreSQL advisory
  lock keeps the two-key form ([ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing)).
- **Fetches during a drain** ([Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down), option (b)). A draining replica finishes the fetches it
  holds until 5 s before its deadline, then cancels them with `failed`, and from `SIGTERM` on it
  holds no new cross-replica flight ([ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica)).
- **Upstream fetch budget** ([Q-REPO-8](spec/repositories-and-proxy.md#q-repo-8--upstream-fetch-limits-when-the-replica-count-changes), option (b)). `limits.maxConcurrentFetches` is a budget for
  the installation, 64 by default, shared out as `ceil(budget ÷ n)` per replica from the replica
  registry, so the upstream sees at most `budget + n − 1` concurrent fetches with no shared state.
  Only the request that holds the flight takes a slot. `proxy.max_concurrent_fetches` stays per
  replica, because it protects the replica.
- **Negative cache** in Valkey behind a per-replica L1 ([ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)).

The flight lock is not shown in the lock diagnostics, which list PostgreSQL advisory locks only;
collapsed fetches are diagnosed through metrics, the leader's upstream record and `Cache-Status`
([Q-OPS-17](spec/operations-and-observability.md#q-ops-17--flight-locks-in-the-lock-diagnostics), option (a)). Failure handling is [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving).

**Alternatives considered.** Collapsing only within a replica: N replicas mean up to N upstream
fetches per cold object, and N grows with the load. Redirecting clients to the upstream: no cache,
no availability gain, and the upstream sees every internal client. Mirroring whole registries ahead
of time: storage for packages nobody uses and freshness lag for those they do. From
[Q-ARCH-6](spec/architecture.md#q-arch-6--cross-replica-collapse-holds-a-connection-for-the-whole-upstream-transfer): the session advisory lock on a bounded lock pool, the earlier choice, which pins up
to 16 direct sessions per `web` replica that no transaction-mode pooler can carry; a lease row with
a heartbeat, which writes to the system of record on the hot path. From [Q-REPO-8](spec/repositories-and-proxy.md#q-repo-8--upstream-fetch-limits-when-the-replica-count-changes): per-replica
limits, which multiply with autoscaling; a semaphore in Valkey, a call and a leak case on every
fetch for a bound whose purpose is to stop bursts.

**Spec:** [INV-11](spec/README.md#design-invariants), [R-REPO-024](spec/repositories-and-proxy.md#r-repo-024--remote-repository-settings), [R-REPO-025](spec/repositories-and-proxy.md#r-repo-025--remote-request-path),
[R-REPO-026](spec/repositories-and-proxy.md#r-repo-026--collapsing-concurrent-misses-single-flight-and-valkey-flight-lock), [R-REPO-040](spec/repositories-and-proxy.md#r-repo-040--size-and-concurrency-limits), [R-REPO-043](spec/repositories-and-proxy.md#r-repo-043--metadata-url-rewriting), [R-REPO-049](spec/repositories-and-proxy.md#r-repo-049--repository-and-proxy-state-on-n-replicas-without-valkey-and-during-a-drain),
[R-ARCH-017](spec/architecture.md#r-arch-017--session-connections-leases-and-advisory-lock-categories), [R-ARCH-033](spec/architecture.md#r-arch-033--cross-replica-request-collapsing-through-valkey-flightlock); decided: [Q-ARCH-6](spec/architecture.md#q-arch-6--cross-replica-collapse-holds-a-connection-for-the-whole-upstream-transfer),
[Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills), [Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down), [Q-REPO-8](spec/repositories-and-proxy.md#q-repo-8--upstream-fetch-limits-when-the-replica-count-changes), [Q-OPS-17](spec/operations-and-observability.md#q-ops-17--flight-locks-in-the-lock-diagnostics).

### ADR-023 · High availability and horizontal scale on HA PostgreSQL, HA Valkey and S3 (INV-12)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Replicas must invalidate each other's caches, share a job queue and run singleton
work exactly once, without a coordination system of their own. Changed by the owner's constraints
of 2026-10-28 ([ADR-092](#adr-092--high-availability-on-ha-postgresql-ha-valkey-with-sentinel-and-s3), [ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage)): HA PostgreSQL, HA Valkey with Sentinel and S3 are the
production topology and cannot be deselected, the load exceeds one replica, and a node drain must
cause no outage. Until then replicas coordinated only through PostgreSQL (`LISTEN/NOTIFY` for
invalidation, advisory-lock leases and a session-locked fetch pool on every replica), and
transaction-mode poolers were excluded. Those parts are replaced.

**Decision.**
- **Topology.** HA PostgreSQL, HA Valkey with Sentinel (one primary, at least one replica, at least
  three Sentinels; trove finds the primary through Sentinel and follows failovers) and S3-compatible
  object storage. Single instances of PostgreSQL or Valkey serve development only. HA of the three
  comes from the platform; the chart consumes them and bundles no production instance.
- **Scale.** Capacity planning assumes the load exceeds one replica; load is shared by adding
  replicas of a role. `web` and `worker` each run at least two replicas spread across nodes, each
  role behind a PodDisruptionBudget that evicts one pod at a time. `web` replicas sit behind a
  Kubernetes Service without session affinity. The `backup` role runs as a Job, one pod at a time,
  with no disruption budget; an evicted backup or restore resumes in the Job's next pod.
- **Drains.** A node drain causes no outage when each of `web` and `worker` has at least two
  replicas on different nodes: a replica that is shut down stops being ready, waits for endpoint
  removal, lets in-flight requests finish within the grace period and releases its jobs
  ([ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica), [ADR-105](#adr-105--drain-capacity-one-replica-more-than-the-peak-needs-per-role)).
- **Connections.** PostgreSQL connections do not grow without bound with the replica count: the
  main pool may run through a transaction-mode pooler, and only `worker` replicas hold session
  connections, for their leases, besides the one-off `trove migrate` and `trove backup` processes
  ([ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)).
- **Coordination.** Through PostgreSQL: the durable job queue (`FOR UPDATE SKIP LOCKED`), the
  change log ([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)) and the leases whose loss would corrupt data. Through Valkey: request
  collapsing, the negative cache of remote repositories, rate-limit counters with cluster scope,
  and the fast path of change messages and signals ([ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)).
- **Valkey outage.** Valkey being unavailable degrades trove and is alerted on, but does not make
  replicas unready: collapsing and negative caching work per replica, rate limits are approximated
  per replica at the configured values divided by the live replicas of the role, changes reach the
  other replicas at the speed of the change-log poll, and signals wait for their consumers' next
  poll ([ADR-100](#adr-100--valkey-degrades-trove-but-never-gates-readiness-except-for-configuration-errors)).
- **Readiness.** The `web` replicas sit behind a Service and an Ingress or Gateway route, and
  readiness is tied to PostgreSQL and process health. Object storage gates only a replica's first
  readiness: a replica becomes ready only after its object-store startup check has passed.
  Afterwards S3 failures degrade blob requests one by one through the circuit breaker and are
  reported (`degraded`) and alerted on, while running replicas stay ready. Continuous gating on
  object storage is the opt-in `health.require_object_store: true`.

**Consequences.** Replicas never open connections to each other: no peer discovery, no
replica-to-replica network rule ([R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node)). Decided for the revision of 2026-10-28:
- **Connection model** ([Q-ARCH-14](spec/architecture.md#q-arch-14--postgresql-connection-model-for-n-replicas-and-transaction-mode-poolers), option (b); [Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), revisited: option (b) instead of (a), from
  the MVP on). The main pool is transaction-scoped and may run through a transaction-mode pooler;
  `database.session_host` serves the session connections. One control connection per replica is
  reserved inside `database.pool.max_conns` for the change-log poll, the heartbeats and the
  `database` check, so a saturated request pool cannot fail readiness on one replica after another;
  behind a pooler it uses a control endpoint with a server pool of its own ([Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler)).
  Statement-mode poolers are not supported ([ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)).
- **Valkey and readiness** ([Q-HA-13](spec/ha-and-helm.md#q-ha-13--valkey-and-readiness), option (d)). Only configuration errors (a rejected
  credential, a failed TLS verification, a primary name no reachable Sentinel knows) hold back a
  replica's first readiness; outages never affect readiness.
- **Valkey topologies** ([Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports), option (b)). Sentinel is the only production topology at MVP,
  a primary-endpoint mode is P1, and Valkey Cluster is deferred ([ADR-099](#adr-099--valkey-client-valkey-go-against-valkey-90-and-later-sentinel-in-production)).

The readiness rule for object storage revises INV-12, which tied readiness to
object-storage reachability. storage.md, operations-and-observability.md and ha-and-helm.md decided
the same rule, option (d) on each page ([Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness), [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure)). The edit
to INV-12 in [spec/README.md](spec/README.md#design-invariants) is their cross-page consequence. What follows from the rule:
- A shared S3 outage leaves the UI, the API, logins, the CI token exchange, metadata, deletes and
  deduplicated uploads working. Blob requests answer a retryable `503` through the circuit breaker,
  and two alerts tell a shared outage from a single replica's problem.
- A release with a wrong bucket, endpoint, credential or CA never becomes ready, so the rollout
  stalls with `maxUnavailable: 0` instead of replacing working pods.
- A replica that loses only its own S3 path stays in the Service until its alert is handled.
- Pods restarted, rescheduled or added during a long shared outage stay unready until the store
  returns, so capacity can shrink meanwhile.
- A node drained during a shared outage evicts one pod per role; its replacement stays unready, so
  the disruption budget refuses the next eviction and the drain waits for the store. The outage
  costs each role at most one replica of capacity, and the Service never loses its last ready
  endpoints. Valkey is no input to this rule ([Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), revisited on 2026-10-28, unchanged).
- Liveness never reads readiness or any dependency, so an outage never restarts replicas. It is a
  watchdog over trove's own loops and fails only when a loop stops making progress, which a restart
  repairs ([Q-OPS-1](spec/operations-and-observability.md#q-ops-1--couple-liveness-to-prolonged-unreadiness), option (d)).

**Alternatives considered.** Consensus or gossip between replicas: peer networking and split-brain
handling. Coordination through PostgreSQL alone, the earlier decision: superseded by the owner's
constraint, and its `LISTEN` session and session-locked fetch pool on every replica grew direct
connections with the replica count. Kubernetes Lease objects for leader election: write access to
the Kubernetes API, and no fencing of the database writes the leader makes ([ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing)). Session
affinity at the edge: forbidden by the owner's constraint, and broken by every drain. For the
readiness rule, the options of [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness):
- (a) INV-12 as first written, with continuous gating. One bucket is shared, so an outage removes
  every replica, including the screens operators diagnose with.
- (b) Never gating on object storage. A misconfigured release becomes ready and replaces working
  pods.
- (c) Configurable, with gating as the default. This keeps (a)'s total outage as the default.
- (d) First-readiness gating with continuous gating as an opt-in: chosen.
- (e) Relative gating against the other replicas. Cross-replica coordination for a rare case that
  the circuit breaker and the single-replica alert already cover, and it flaps while replicas
  briefly disagree.

[Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure) also weighed letting a replica skip the gate when a live replica had verified the same S3
settings fingerprint. It was rejected because a revoked role or key behind unchanged settings
passes, which is the misconfiguration the gate exists for.

**Spec:** [INV-12](spec/README.md#design-invariants), [R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node), [R-HA-004](spec/ha-and-helm.md#r-ha-004--cross-replica-invalidation-through-the-change-log-and-valkey), [R-HA-005](spec/ha-and-helm.md#r-ha-005--leader-less-scheduling-and-job-leases),
[R-HA-006](spec/ha-and-helm.md#r-ha-006--singleton-work-under-advisory-lock-leases-with-fencing), [R-HA-008](spec/ha-and-helm.md#r-ha-008--readiness-and-liveness-under-dependency-failure), [R-HA-016](spec/ha-and-helm.md#r-ha-016--connection-budget-and-pool-sizing), [R-HA-056](spec/ha-and-helm.md#r-ha-056--valkey-with-sentinel-connection-and-supported-topologies), [R-HA-057](spec/ha-and-helm.md#r-ha-057--valkey-failover-outage-and-degraded-mode),
[R-HA-058](spec/ha-and-helm.md#r-ha-058--transaction-mode-pooler-for-the-main-pool), [R-HA-059](spec/ha-and-helm.md#r-ha-059--node-drain-without-outage), [R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget), [R-ARCH-030](spec/architecture.md#r-arch-030--valkey-usage-map), [R-OPS-001](spec/operations-and-observability.md#r-ops-001--liveness-and-readiness-probes),
[R-STOR-035](spec/storage.md#r-stor-035--object-store-consistency-assumptions-and-conformance-check), [R-STOR-036](spec/storage.md#r-stor-036--behavior-during-an-object-store-outage); decided: [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness), [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure),
[Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), [Q-HA-13](spec/ha-and-helm.md#q-ha-13--valkey-and-readiness), [Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports), [Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler), [Q-ARCH-14](spec/architecture.md#q-arch-14--postgresql-connection-model-for-n-replicas-and-transaction-mode-poolers).

### ADR-024 · The Helm chart is the supported deployment (INV-13)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** [ADR-006](#adr-006--deployment-through-a-helm-chart) fixes Helm; the chart's scope and its handling of secrets decide how safe
an installation is by default. Changed by the owner's constraints of 2026-10-28 ([ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage) to
[ADR-095](#adr-095--no-readwritemany-storage)): the roles are split from the start, a backup job is part of the service, and no
ReadWriteMany volume may be used. Until then the chart rendered one Deployment by default and an
optional second one, and named no backup job and no rule for volumes.

**Decision.** The chart `trove` in `deploy/helm/trove` renders one Deployment per role (`web`,
`worker`), the backup CronJob, a migration Job hook, Services, an Ingress or Gateway route,
PodDisruptionBudgets, HorizontalPodAutoscalers, a ServiceMonitor and NetworkPolicies. No
ReadWriteMany volume is used anywhere: scratch space is `emptyDir` or a generic ephemeral volume
(ReadWriteOnce, deleted with its pod), and the chart renders no PersistentVolumeClaim and no
StatefulSet. A role that ever needs persistent local storage gets one ReadWriteOnce volume per pod;
none is planned. Secrets come from existing Kubernetes Secrets; S3 credentials from workload
identity (IRSA and similar) or static keys.

**Consequences.** The chart never creates secrets and refuses to render invalid combinations
([ADR-079](#adr-079--the-chart-validates-before-it-renders-and-never-creates-secrets)). The rendered output is checked in CI for `ReadWriteMany`, `ReadOnlyMany`,
claims and StatefulSets ([R-HA-061](spec/ha-and-helm.md#r-ha-061--volumes-nothing-shared-nothing-persistent)). Decided:
- **Backup CronJob** ([Q-HA-17](spec/ha-and-helm.md#q-ha-17--the-backup-cronjob-is-on-by-default), option (a)). Rendered by default; rendering fails without a backup
  target ([ADR-112](#adr-112--the-backup-cronjob-is-on-by-default-and-needs-its-own-target)).
- **Restore Job** ([Q-HA-18](spec/ha-and-helm.md#q-ha-18--how-a-restore-runs-in-the-chart), option (b)). While `restore.enabled`, the chart renders an ordinary
  restore Job with both roles at zero, no migration Job and a suspended backup CronJob
  ([ADR-110](#adr-110--restore-an-empty-installation-any-later-release-resumable-as-an-ordinary-job)).
- **Valkey in the local profile and CI** ([Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci), option (d)). trove's own manifests,
  `deploy/dev/valkey.yaml`, run Valkey and three Sentinels from the official image; the chart
  bundles no production Valkey ([ADR-099](#adr-099--valkey-client-valkey-go-against-valkey-90-and-later-sentinel-in-production)).
- **Gateway API** ([Q-HA-7](spec/ha-and-helm.md#q-ha-7--priority-of-gateway-api), option (b)). An HTTPRoute attached to an existing Gateway is MVP
  ([R-HA-038](spec/ha-and-helm.md#r-ha-038--gateway-api-exposure)). A Gateway rendered by the chart is P1 ([R-HA-053](spec/ha-and-helm.md#r-ha-053--chart-created-gateway)).
- **Egress NetworkPolicy** ([Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy), option (a)). Off by default. `NOTES.txt` warns while it is off
  and points to an example values file rendered in CI ([R-HA-042](spec/ha-and-helm.md#r-ha-042--network-policies)).
- **Publishing location** ([Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart)). The development agent chose option (a), GitHub Container
  Registry with keyless signatures. The owner replaced it on 2026-10-29 ([ADR-130](#adr-130--the-organisations-ci-and-release-standard)): the image is published to
  Docker Hub as `docker.io/guidedtraffic/trove`, the chart to the repository `https://guided-traffic.github.io/trove/`, with provenance and SBOM
  attestations and no signatures ([R-HA-047](spec/ha-and-helm.md#r-ha-047--images-private-registries-air-gapped-installation-and-attestations)).

**Alternatives considered.** A Kubernetes operator with custom resources: automated upgrades, but a
controller with cluster-wide permissions to install and secure. Plain manifests or Kustomize: no
validated parameters. Chart-generated secrets: regenerated wherever Helm's `lookup` is unavailable
([Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key)). A StatefulSet with a persistent spool per pod: data that outlives its pod, which
nothing needs and which a drain would have to move. A shared spool volume: not available
([ADR-095](#adr-095--no-readwritemany-storage)).

**Spec:** [INV-13](spec/README.md#design-invariants), [R-HA-002](spec/ha-and-helm.md#r-ha-002--role-split-into-web-and-worker-deployments), [R-HA-028](spec/ha-and-helm.md#r-ha-028--chart-scope-rendered-resources-and-naming), [R-HA-029](spec/ha-and-helm.md#r-ha-029--fail-fast-chart-validation), [R-HA-030](spec/ha-and-helm.md#r-ha-030--configuration-rendering-and-secret-wiring),
[R-HA-031](spec/ha-and-helm.md#r-ha-031--values-reference), [R-HA-038](spec/ha-and-helm.md#r-ha-038--gateway-api-exposure), [R-HA-042](spec/ha-and-helm.md#r-ha-042--network-policies), [R-HA-047](spec/ha-and-helm.md#r-ha-047--images-private-registries-air-gapped-installation-and-attestations), [R-HA-053](spec/ha-and-helm.md#r-ha-053--chart-created-gateway), [R-HA-060](spec/ha-and-helm.md#r-ha-060--backup-cronjob-and-restore-job),
[R-HA-061](spec/ha-and-helm.md#r-ha-061--volumes-nothing-shared-nothing-persistent); decided: [Q-HA-7](spec/ha-and-helm.md#q-ha-7--priority-of-gateway-api), [Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key), [Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy), [Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart), [Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci),
[Q-HA-17](spec/ha-and-helm.md#q-ha-17--the-backup-cronjob-is-on-by-default), [Q-HA-18](spec/ha-and-helm.md#q-ha-18--how-a-restore-runs-in-the-chart).

### ADR-025 · npm and NuGet v3 are the MVP formats (INV-14)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** [ADR-010](#adr-010--every-catalog-format-npm-and-nuget-first) puts npm and NuGet first; each ecosystem has several clients with
different protocol coverage.

**Decision.** npm (clients: npm, pnpm, Yarn classic and Berry) and NuGet v3 (dotnet CLI, nuget.exe,
Visual Studio), each with local, remote and virtual repositories. NuGet v2 (OData) is P2.

**Consequences.** Each format is accepted only when a pinned matrix of real clients passes
([ADR-072](#adr-072--real-client-acceptance-matrices-gate-each-format)). NuGet v2 requests get a clear 404 pointing at the v3 service index ([ADR-073](#adr-073--nuget-v3-only-every-resource-below-the-feed-root)).

**Alternatives considered.** NuGet v2 in the MVP: a large legacy query surface for clients that all
speak v3 today. npm alone first: the owner named both.

**Spec:** [INV-14](spec/README.md#design-invariants), [R-NPM-045](spec/formats/npm.md#r-npm-045--client-acceptance-matrix), [R-NUGET-001](spec/formats/nuget.md#r-nuget-001--nuget-repositories-and-endpoint-layout),
[R-NUGET-027](spec/formats/nuget.md#r-nuget-027--acceptance-test-suite).

### ADR-026 · One configuration file plus database-held configuration (INV-15)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Some settings are needed before the database is reachable or must survive a broken
database; others are changed daily by administrators.

**Decision.** One YAML file (`/etc/trove/trove.yaml`), every key overridable by an environment
variable `TROVE_<SECTION>_<KEY>`; secrets are referenced from files or environment variables,
never written inline into a ConfigMap. Repositories, permissions and CI identity mappings live in
PostgreSQL and are managed through the API and UI. Login providers, including their claim mapping
and group rules, are file configuration only. Endpoints are file configuration and their exposure
rules live in PostgreSQL (since 2026-10-29,
[ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling)). A declarative bootstrap file (configuration as code)
is P1 ([ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1)).

**Consequences.** Bootstrap-critical settings (database, object store, base URL, keys) change by
rollout; everything else changes at runtime and is audited. [R-ARCH-020](spec/architecture.md#r-arch-020--configuration-schema-in-code-and-the-filedatabase-split) states which side each
setting belongs to. Login providers, decided: file only ([Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives) (a), [Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents) (a)). Login is
bootstrap-critical and must not be breakable through the API or a document apply it protects.
A provider change takes effect with a rollout that validates it before it serves anyone.

**Alternatives considered.** Everything in the file: every repository change needs a rollout.
Everything in the database: no way to start or recover an installation whose database settings are
wrong. Kubernetes custom resources for repositories: ties repository management to cluster
permissions.

**Spec:** [INV-15](spec/README.md#design-invariants), [R-ARCH-020](spec/architecture.md#r-arch-020--configuration-schema-in-code-and-the-filedatabase-split), [R-OPS-028](spec/operations-and-observability.md#r-ops-028--configuration-file-and-environment-mapping),
[R-OPS-029](spec/operations-and-observability.md#r-ops-029--configuration-as-code-document).

### ADR-027 · Structured logs, Prometheus metrics, OTLP traces, audit in PostgreSQL (INV-16)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Operators need standard telemetry; security teams need an audit trail they can query
without an external log system.

**Decision.** Structured JSON logs through `log/slog`; Prometheus metrics prefixed `trove_`;
optional OpenTelemetry traces over OTLP. Audit events (actor, action, repository, path, outcome,
source) are stored in PostgreSQL and emitted to the log stream.

**Consequences.** Audit can be queried and exported through the API and UI ([ADR-080](#adr-080--audit-records-commit-with-the-change-they-record)). Decided
([Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records), option (c)): audit rows are append-only for trove's runtime role, which may only `INSERT`
and `SELECT` them, so a compromised replica or a SQL injection cannot rewrite or erase history.
The details:
- **Partitions.** An owner-held `SECURITY DEFINER` function, `audit_maintain_partitions()`,
  creates and drops the partitions. It attaches new partitions with `ATTACH PARTITION`, which
  inserts do not wait for, and runs under a 2-second `lock_timeout`. A drop it cannot lock is left
  to the next call, so a long audit export never queues transactional writes behind it.
- **Retention.** The retention is owner-written data in `audit_policy`, applied by that
  function.
- **Deduplication.** Deduplicated denials write a first event and one summary event, so no row is
  ever updated.

The owner, superusers and single-role installations are outside this protection; shipping the
`log: audit` stream to an append-only store covers them ([H-39](security/audit.md#h-39--audit-records-are-not-tamper-evident)). Metric labels need
cardinality caps: by default 500 distinct repository keys per metric family and replica, with
`repo="_other"` and an overflow counter beyond it ([R-OPS-007](spec/operations-and-observability.md#r-ops-007--metric-naming-labels-and-cardinality), [Q-OPS-10](spec/operations-and-observability.md#q-ops-10--default-cap-for-the-repo-metric-label), option (a)). Revisited on
2026-10-28 (option (a) with (d)): the keys that keep their label are chosen from the repository
snapshot, include patterns first and then creation order, so every replica labels the same keys
and `sum by (repo)` across replicas stays correct. Further decisions for n replicas:
- **Role and pool** ([Q-OPS-13](spec/operations-and-observability.md#q-ops-13--how-a-series-names-its-role-and-pool), option (b)). `role` and `pool` are target labels that the chart's
  ServiceMonitor attaches from the pod's labels; the process reports its role in
  `trove_build_info{role}` for scrapers that do not relabel.
- **Gauges read from PostgreSQL** ([Q-OPS-18](spec/operations-and-observability.md#q-ops-18--gauges-read-from-postgresql-on-n-replicas), option (b)). Every `web` and `worker` replica
  evaluates them in the background once per `metrics.db_gauge_interval`, and scrapes return the
  last values, so the alerts that read them survive the loss of a whole role.

**Alternatives considered.** Audit only in the log stream: no query API without an external log
store. A separate audit store: another stateful dependency. For tamper evidence
([Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records)):
- External shipping alone, with full rights for the runtime role. A compromised replica erases its
  traces, and in two-role mode the runtime role could not even maintain partitions.
- A hash chain over every row. It serializes every transactional write on one chain head and,
  without an external anchor, proves nothing against the role that writes it.
- Sealed checkpoints on top of the chosen option. A sealing job and a verification path for a
  threat that log shipping already covers.

**Spec:** [INV-16](spec/README.md#design-invariants), [R-OPS-006](spec/operations-and-observability.md#r-ops-006--metrics-endpoint), [R-OPS-009](spec/operations-and-observability.md#r-ops-009--log-stream-and-record-format),
[R-OPS-015](spec/operations-and-observability.md#r-ops-015--audit-recording), [R-OPS-017](spec/operations-and-observability.md#r-ops-017--audit-query-export-and-retention), [R-OPS-018](spec/operations-and-observability.md#r-ops-018--trace-context-and-opentelemetry-export), [R-OPS-045](spec/operations-and-observability.md#r-ops-045--valkey-and-coordination-observability), [R-OPS-046](spec/operations-and-observability.md#r-ops-046--operational-signals-for-availability-horizontal-scale-and-backup); decided: [Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records),
[Q-OPS-10](spec/operations-and-observability.md#q-ops-10--default-cap-for-the-repo-metric-label), [Q-OPS-13](spec/operations-and-observability.md#q-ops-13--how-a-series-names-its-role-and-pool), [Q-OPS-18](spec/operations-and-observability.md#q-ops-18--gauges-read-from-postgresql-on-n-replicas).

### ADR-028 · The UI uses only the public API (INV-17)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Whatever the UI can do, administrators want to script.

**Decision.** The Angular UI uses only public, documented endpoints: the operations of the
OpenAPI document (`/api/v1/…`, `/files/…`) and the browser sign-in flow under `/auth/…`; no
private UI endpoints. Every UI action is therefore scriptable.

**Consequences.** A UI feature needs its documented API first. The wording amends INV-17, which
named only the `/api/v1` API although the UI moves bytes through `/files/…`, which the OpenAPI
document describes, and cannot sign in without the flow under `/auth/…`
([Q-API-4](spec/api-and-ui.md#q-api-4--inv-17s-wording-and-byte-transfers-on-files), option (a)). The edit to INV-17 in [spec/README.md](spec/README.md#design-invariants) is that question's
cross-page consequence.

**Alternatives considered.** A backend-for-frontend with UI-only endpoints: faster UI iteration,
but actions that cannot be automated and a second surface to secure. Mirroring byte transfer under
`/api/v1/`: a second code path for the largest transfers only to satisfy a wording. Keeping
INV-17's text and defining its reading on the API page: an invariant that its own pages contradict
in their words.

**Spec:** [INV-17](spec/README.md#design-invariants), [R-API-002](spec/api-and-ui.md#r-api-002--openapi-31-document-as-the-contract), [R-UI-005](spec/api-and-ui.md#r-ui-005--permission-driven-rendering); decided: [Q-API-4](spec/api-and-ui.md#q-api-4--inv-17s-wording-and-byte-transfers-on-files).

### ADR-096 · No state between requests on a replica (INV-18)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Its premise is the owner's constraint of 2026-10-28 ([ADR-093](#adr-093--horizontal-scale-random-distribution-split-roles-drains-without-outage)): a Kubernetes Service
distributes requests at random and without session affinity, and a drain can end any replica at
any time. A protocol that kept state in one replica's memory or on its disk between requests would
answer differently depending on where a request lands, and would break at every drain.

**Decision.** No request may depend on state that an earlier request left in one replica's memory
or on its local disk. A protocol that spans several requests (chunked or resumable uploads, login
polling, paged listings) keeps its state in PostgreSQL or Valkey and its data in S3, so
consecutive requests may land on different replicas. Valkey qualifies only for state whose loss
costs a retry, not a result (INV-3). Local disk is scratch space for one request or one job.
In-process caches stay allowed where every replica gives the same answer: their keys are read from
PostgreSQL per request, or the change log keeps them current.

**Consequences.** Neither trove nor the chart configures session affinity, and the e2e profile
`scatter` sends consecutive requests round robin to different `web` replicas ([R-ARCH-032](spec/architecture.md#r-arch-032--no-state-between-requests-on-one-replica-inv-18)).
Decided:
- **Multi-request uploads** ([Q-STOR-11](spec/storage.md#q-stor-11--multi-request-uploads-across-replicas), option (b)). One S3 multipart upload per session, in
  parts of `storage.uploads.part_size`. Each writing request keeps its leftover bytes below one
  part as a tail object under its own key, recorded with its SHA-256. The session's offset, hash
  state and parts are recorded in PostgreSQL under a writer claim. Finalize completes the upload
  and copies it server-side to the content address. A drain loses at most one part, and a cut
  request releases its claim, so the client resumes on any replica at once.
- **Login flows** in PostgreSQL rows, changed by conditional statements ([Q-AUTH-12](spec/identity-and-authentication.md#q-auth-12--where-multi-request-login-flows-keep-their-state), [ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey)).
- **Management writes on other replicas** ([Q-API-12](spec/api-and-ui.md#q-api-12--visibility-of-a-management-write-on-the-other-replicas), revisited on 2026-10-28: option (e)
  instead of (c)). Management reads come from PostgreSQL. A repository key unknown to the
  replica's snapshot is looked up in PostgreSQL before `404`; a found key triggers a change-log poll
  and waits for the grant-index rebuild. A client that changed grants may send
  `Trove-Authz-Min-Epoch` ([ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed)). Every other effect follows the propagation bound of
  [R-ARCH-018](spec/architecture.md#r-arch-018--in-process-caches-and-cross-replica-invalidation), which [R-API-052](spec/api-and-ui.md#r-api-052--visibility-of-a-management-write-on-other-replicas) states as the contract.
- **Paged lists and idempotency keys.** Cursors are self-contained, and idempotency keys live in
  PostgreSQL ([ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys)).
- **Same answer on every replica.** npm documents ([ADR-116](#adr-116--npm-documents-are-cached-per-replica-under-keys-read-from-postgresql)), NuGet validators ([ADR-121](#adr-121--rendered-nuget-documents-carry-weak-etags)) and
  forwarded NuGet searches ([ADR-120](#adr-120--forwarded-nuget-search-results-are-cached-per-replica)) are cached per replica under inputs every replica reads
  alike; quota admission reads folded rows in PostgreSQL ([Q-STOR-12](spec/storage.md#q-stor-12--counters-that-several-replicas-update-usage-quotas-and-download-statistics)).
- **Write-only buffers** (download statistics, activity, last-used, asynchronous audit) are never
  read back by a later request and are flushed at shutdown ([ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica)).

**Alternatives considered.** Session affinity at the edge: forbidden by the owner's constraint,
and broken by every drain. Multi-request state in Valkey only: a failover or flush loses it, which
for an upload session is a lost upload rather than a performance cost. Staged chunk objects
assembled at finalize, the earlier upload design: the finalize request grows with the blob, so a
drain cuts the most expensive request. A shared spool volume: not available ([ADR-095](#adr-095--no-readwritemany-storage)).

**Spec:** [INV-18](spec/README.md#design-invariants), [R-ARCH-032](spec/architecture.md#r-arch-032--no-state-between-requests-on-one-replica-inv-18), [R-STOR-011](spec/storage.md#r-stor-011--multi-request-upload-sessions), [R-API-051](spec/api-and-ui.md#r-api-051--requests-that-span-replicas),
[R-API-052](spec/api-and-ui.md#r-api-052--visibility-of-a-management-write-on-other-replicas), [R-AUTH-042](spec/identity-and-authentication.md#r-auth-042--authentication-across-replicas-and-roles); decided: [Q-STOR-11](spec/storage.md#q-stor-11--multi-request-uploads-across-replicas), [Q-AUTH-12](spec/identity-and-authentication.md#q-auth-12--where-multi-request-login-flows-keep-their-state), [Q-API-12](spec/api-and-ui.md#q-api-12--visibility-of-a-management-write-on-the-other-replicas).

### ADR-097 · Consistent backups to S3, restored into an empty installation (INV-19)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** [ADR-094](#adr-094--a-configurable-backup-job-writes-consistent-backups-to-s3) requires a configurable job that produces a consistent backup in S3. The hard
part is consistency across two stores: the database references blobs by digest, and garbage
collection may delete a blob while a backup copies it. Platform point-in-time recovery and bucket
versioning ([R-HA-023](spec/ha-and-helm.md#r-ha-023--backup-of-postgresql-object-storage-and-keys)) protect each store separately and depend on the platform.

**Decision.** `trove backup create` produces a consistent backup of the service: a transactionally
consistent snapshot of trove's PostgreSQL data together with every blob that snapshot references,
written to a configurable S3 target (endpoint, bucket, prefix, credentials, retention). The chart
schedules it as a CronJob. `trove backup restore` restores a backup into an empty installation, and
an interrupted restore of the same backup resumes where it stopped. Valkey is not part of a
backup. The commands form the `backup` role of the one binary: it serves no client and uses no
Valkey.

**Consequences.**
- **Self-contained.** Everything a restore needs is in the target, so a restore works on another
  platform or provider. Run records live in the source database; the target, not the database, is
  the record of the backups ([R-BAK-004](spec/backup-and-restore.md#r-bak-004--table-registry-and-export-classes)).
- **Design.** [ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold) (format and garbage-collection hold), [ADR-107](#adr-107--backup-runs-one-at-a-time-in-a-leased-row-cancelled-when-they-stall) (runs), [ADR-108](#adr-108--backup-credentials-stay-in-backup-pods-the-api-and-ui-only-read-runs)
  (credentials and the API), [ADR-109](#adr-109--backup-encryption-server-side-at-mvp-client-side-for-the-database-files-at-p1) (encryption), [ADR-110](#adr-110--restore-an-empty-installation-any-later-release-resumable-as-an-ordinary-job) (restore), [ADR-111](#adr-111--a-restore-moves-valkey-to-a-new-keyspace-and-offsets-every-revision) (derived state),
  [ADR-112](#adr-112--the-backup-cronjob-is-on-by-default-and-needs-its-own-target) (CronJob default).
- **What a restore undoes.** Changes after the snapshot are lost. Every restore replays the
  revocation journal ([ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination)), withholds npm versions unpublished since the snapshot
  ([ADR-118](#adr-118--a-restore-withholds-npm-versions-unpublished-after-the-snapshot)), reserves lost NuGet version numbers ([ADR-122](#adr-122--a-restore-replays-nuget-publishes-and-deletes-into-tombstones)), and makes single-use CI issuers
  refuse earlier subject tokens ([ADR-064](#adr-064--subject-token-reuse-is-recorded-single-use-depends-on-the-issuer-kind)). What remains is recorded in [H-37](spec/ha-and-helm.md#h-37--a-database-restore-revives-revoked-credentials-and-removed-grants),
  [H-50](spec/formats/nuget.md#h-50--a-restore-revives-nuget-versions-deleted-after-the-backups-snapshot-and-can-free-lost-version-numbers) and the npm page's [H-51](spec/formats/npm.md#h-51--a-restore-reverts-npm-metadata-changes-made-after-the-backups-snapshot).
- **Platform layer.** Point-in-time recovery stays the complementary path with a shorter recovery
  point ([Q-HA-19](spec/ha-and-helm.md#q-ha-19--priority-of-the-platform-backup-layer-and-the-point-in-time-restore)).

**Alternatives considered.** A manifest that references a platform point-in-time restore: not
self-contained and not portable ([Q-BAK-1](spec/backup-and-restore.md#q-bak-1--form-of-the-database-part-of-a-backup)). Coordinated snapshots of database and bucket: not
offered by the platforms the chart targets. Backing up Valkey: its content is disposable by INV-3,
and a restored installation must not read another installation's keys ([ADR-111](#adr-111--a-restore-moves-valkey-to-a-new-keyspace-and-offsets-every-revision)).

**Spec:** [INV-19](spec/README.md#design-invariants), [backup-and-restore.md](spec/backup-and-restore.md), [R-BAK-001](spec/backup-and-restore.md#r-bak-001--what-a-backup-contains),
[R-BAK-009](spec/backup-and-restore.md#r-bak-009--trove-backup-create-phases-and-run-record), [R-BAK-021](spec/backup-and-restore.md#r-bak-021--restore-procedure), [R-HA-060](spec/ha-and-helm.md#r-ha-060--backup-cronjob-and-restore-job).

## Architecture

### ADR-029 · Formats are compiled-in plug-ins behind a narrow host interface

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Every catalog format is planned ([ADR-010](#adr-010--every-catalog-format-npm-and-nuget-first)). Formats differ in routes, path grammar,
metadata documents and merge rules, while authentication, authorization, storage, caching and audit
must behave the same for all of them.

**Decision.** Each format is a Go package `internal/formats/<id>` implementing `formats.Format`. A
format owns its route table and path grammar, how paths behave in caches, package identity and
namespaced properties, the storage hooks, upstream request mapping and metadata rewriting, merging
across virtual members, client setup templates and its native error shape. The core owns
authentication, authorization, the upload and download pipeline, freshness, negative caching, miss
collapsing, offline state, audit, jobs and metrics. Formats reach the core only through `Host` and
must not open outbound connections, run SQL or touch the object store. A conformance kit gates every
format. Untrusted archives are read in memory under fixed limits (10,000 entries, 64 MiB
uncompressed, 100:1 per entry, 1 MiB per manifest entry, 30 s).

**Consequences.** Security-relevant behavior is written once, in the core. A new format is a code
change and a release; the contract version is internal and checked at compile time. Format parsers
run in-process with the replica's credentials: the limits bound resources, not the reach of a logic
flaw ([H-1](spec/architecture.md#h-1--format-code-runs-in-process-without-isolation)). Routing, decided ([Q-ARCH-1](spec/architecture.md#q-arch-1--http-router), option (a)): `net/http.ServeMux` serves every
route, and each format matches its own escaped path behind its literal prefix, because no pattern
router resolves npm's `@scope%2fname` or NuGet's lower-cased paths. The `/api/v1/` operations are
registered on the same ServeMux through the generated `std-http-server` interface
([ADR-083](#adr-083--the-openapi-31-document-is-the-api-contract)). No router dependency; a conflicting registration panics at start-up.

**Alternatives considered.** Out-of-process plug-ins over RPC: isolation, but a deployable per format
and a hop on every request, against [ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2). WebAssembly or scripted plug-ins: isolation and
runtime loading, at the cost of streaming large bodies across a sandbox boundary. Generic path
storage with thin adapters: cannot express npm or NuGet metadata merging. A third-party router or a
web framework: a dependency on the hottest path that still leaves the formats to parse their own
paths.

**Spec:** [R-ARCH-008](spec/architecture.md#r-arch-008--format-plug-in-contract), [R-ARCH-009](spec/architecture.md#r-arch-009--core-services-offered-to-formats), [R-ARCH-010](spec/architecture.md#r-arch-010--format-registry-and-conformance-kit), [R-ARCH-028](spec/architecture.md#r-arch-028--resource-limits-for-untrusted-package-content); decided: [Q-ARCH-1](spec/architecture.md#q-arch-1--http-router).

### ADR-030 · One origin for UI, API and package content

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Package content is attacker-controlled and can be HTML, SVG or XML. Served from the
UI's origin, it could run script with the viewer's session.

**Decision.** In the MVP, UI, management API and package endpoints share one host name and
listener. Every response whose body is package content or package metadata carries the content
headers:
- `X-Content-Type-Options: nosniff`;
- `Content-Security-Policy: sandbox; default-src 'none'`;
- `Cross-Origin-Resource-Policy: same-site`;
- for files, `Content-Disposition: attachment`.

The core sets them, not the handler. The format dispatcher and the `/files/` handler wrap the
response writer before the handler runs, so a handler can change `Content-Type` but cannot remove or
weaken the headers. Every content link handed to a browser comes from one place, `LinkBuilder` or
an API `downloadUrl` field. The UI never embeds package-provided HTML; it renders READMEs only
through its own sanitizer ([ADR-087](#adr-087--readmes-rendered-on-the-server-through-an-allowlist-sanitizer)). The conformance kit fails a format whose content routes lack the
headers, and a handler that removes them.

An optional separate content origin is P1 ([R-ARCH-029](spec/architecture.md#r-arch-029--optional-separate-content-origin), [Q-ARCH-10](spec/architecture.md#q-arch-10--separate-content-origin-for-package-content-a-browser-can-render), option (d)). It is off by
default:
- `server.content_base_url` must name a host name other than the UI's; a different port on the same
  host is refused, because browsers do not isolate cookies by port.
- Browser requests for content are handed off there with `303` and session-bound, signed,
  short-lived URLs. Package clients stay on the UI host.
- Every trove cookie carries the `__Host-` prefix, so a content host on the UI's registrable domain
  can neither receive nor plant them. Such a content host is accepted with a start-up warning,
  since it is still same-site with the UI.

**Consequences.** In the MVP and wherever the content origin stays off: one host name, one
certificate, no cross-origin configuration. The protection is header-based. A route that forgets
the headers can no longer occur. A proxy that strips them, or a browser that ignores both
`attachment` and CSP `sandbox`, reopens the risk ([H-3](spec/architecture.md#h-3--ui-and-package-content-share-one-origin)). None of the headers affects npm, pnpm,
Yarn, dotnet or nuget.exe. The link seam lets R-ARCH-029 move content links to the second origin
without touching formats or the UI. Inline browsing of files (P2) builds on that origin.

**Alternatives considered.** One origin forever: H-3 would stay, and inline viewing could never be
offered safely. A mandatory content origin from the prototype on: every installation, evaluation
included, would need a second DNS name, certificate and edge route for a residual the headers
already cover. An optional content origin already in the prototype: effort and a second test matrix
for a switch that is off by default. The content origin moves into the MVP if the prototype has to
render package files inline, a target edge rewrites response headers, or a header bypass is reported
in a supported browser.

**Spec:** [R-ARCH-007](spec/architecture.md#r-arch-007--one-origin-for-ui-and-package-content), [R-ARCH-029](spec/architecture.md#r-arch-029--optional-separate-content-origin), [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery), [R-UI-008](spec/api-and-ui.md#r-ui-008--sanitized-readme-rendering); decided:
[Q-ARCH-10](spec/architecture.md#q-arch-10--separate-content-origin-for-package-content-a-browser-can-render), [Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name).

### ADR-031 · Caches validate themselves; notifications only evict early

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Every replica caches configuration, grants, tokens and merged metadata, and the
pages promise bounds on how fast a change reaches them, such as a revoked token rejected within
2 s. Revisited on 2026-10-28 after the owner's constraints: until then, administrative changes
sent `NOTIFY` inside the writing transaction and hot-path changes left through a post-commit
`NOTIFY` coalescer, and every replica held a `LISTEN` session. That session is one direct
connection per replica, which INV-12's connection rule and a transaction-mode pooler exclude
([ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)), and INV-3 forbids moving correctness into Valkey. Notifications by class became
change areas and signals; trove issues no `NOTIFY` at all.

**Decision.** Every cache is registered with a size bound and a mandatory safety TTL. Keys that make
an entry self-validating (a source digest, a `rev_seq`, an epoch) are preferred; messages then
only evict early.

Change areas ([Q-ARCH-15](spec/architecture.md#q-arch-15--cross-replica-invalidation-without-listennotify), option (c)) carry administrative changes: configuration, policies,
revocations. They are never written per package request.
- **Epoch and log.** Each area has an epoch row in `cluster_epoch`. `cluster.Notify` inserts the
  change row and increments the area's epoch as the last statements before `COMMIT`, so writers of
  one area serialize on their commits only, and epochs have no gaps. Writers of several areas
  update them in area-name order.
- **The poll decides.** Every replica polls the log on its reserved control connection
  ([R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget)), and only the poll advances a replica's applied epoch. Valkey Pub/Sub carries the
  same change at once, as an early eviction that never advances it, so a lost, duplicated or forged
  message cannot hide a change.
- **Reused epochs.** The poll reads `changed_at` with each epoch and checks the row at the last
  applied epoch, so an epoch that went backwards or was reused after a failover or a restore
  flushes the area.
- **Fill epoch check.** A cache fill checks the applied epochs around its query, so it cannot
  re-insert a value that the poll has just invalidated, such as a revoked token.
- **Freshness.** The `invalidation` readiness check fails when the poll has stalled
  (`cluster.epoch_max_staleness`, 20 s), and fail-closed paths read `cluster.ChangesFresh()` or
  `cluster.ChangesFreshWithin(d)`.

Signals ([Q-ARCH-8](spec/architecture.md#q-arch-8--notify-on-hot-path-commits), revisited on 2026-10-28: option (d) instead of (a)) carry hot-path
wake-ups: job pick-up, remote metadata refreshes, the outbox. A per-replica coalescer publishes
them to Valkey after commit, at most one per area per 50 ms; fill completion uses the flight's own
channel. Delivery is at most once, and every consumer stays correct without a signal through
self-validating keys or polling. No code issues `LISTEN`, `NOTIFY` or `pg_notify`, which a test
enforces.

Merged metadata ([Q-ARCH-9](spec/architecture.md#q-arch-9--freshness-of-merged-metadata-for-local-members), option (a); revisited on 2026-10-28, unchanged and with more
weight, since requests reach replicas at random): the core's merged-metadata cache checks the
`rev_seq` of every eligible local member on each hit, mandatory for every format. The counter
lives on the generic `packages` row, and a member that does not hold the package counts as `0`, so
a first local publish of a name a remote member serves is visible at once. A publish is thus
visible through a virtual repository on every replica at once, message or not.

**Consequences.** With Valkey, a change takes effect on every replica within milliseconds; without
Valkey, within the poll interval, never later. The poll costs one small query per replica per
interval (100 replicas at 1 s: 100 queries per second). Each merged-metadata hit costs one indexed
query. The coalescer bounds signal fan-out to n × 20 × m deliveries per second and area, whatever
the commit rate. A forged message can cause extra evictions and wake-ups, never hide or reorder a
change ([H-49](spec/architecture.md#h-49--write-access-to-valkey-denies-service)). Later formats cannot forget the merged-metadata check, because it is in the
core.

**Alternatives considered.** No in-process caches: database round trips for grants and tokens on
every request. Messages as the only invalidation, including eviction of merged metadata by message
alone: a publish-then-install CI step that reaches another replica first gets the old document.
From [Q-ARCH-15](spec/architecture.md#q-arch-15--cross-replica-invalidation-without-listennotify): keeping `LISTEN/NOTIFY` for administrative changes, the earlier design, with one
direct session per replica and readiness tied to it; Valkey Pub/Sub only, which leaves a revoked
token valid until its safety TTL when a message is lost; an outbox polled by one global sequence,
which can skip a row that commits late. From [Q-ARCH-8](spec/architecture.md#q-arch-8--notify-on-hot-path-commits): `NOTIFY` inside every writing transaction,
which binds upload throughput to PostgreSQL's notify lock; the post-commit `NOTIFY` coalescer, the
earlier choice, which still needed the `LISTEN` session.

**Spec:** [R-ARCH-009](spec/architecture.md#r-arch-009--core-services-offered-to-formats), [R-ARCH-012](spec/architecture.md#r-arch-012--read-your-writes-package-metadata-for-local-repositories), [R-ARCH-015](spec/architecture.md#r-arch-015--request-path-virtual-resolution), [R-ARCH-018](spec/architecture.md#r-arch-018--in-process-caches-and-cross-replica-invalidation), [R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget), [R-REPO-020](spec/repositories-and-proxy.md#r-repo-020--merged-metadata-and-its-cache),
[R-HA-004](spec/ha-and-helm.md#r-ha-004--cross-replica-invalidation-through-the-change-log-and-valkey); decided: [Q-ARCH-8](spec/architecture.md#q-arch-8--notify-on-hot-path-commits), [Q-ARCH-9](spec/architecture.md#q-arch-9--freshness-of-merged-metadata-for-local-members), [Q-ARCH-15](spec/architecture.md#q-arch-15--cross-replica-invalidation-without-listennotify).

### ADR-032 · Stored secrets are sealed under a key ring the operator supplies

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Upstream credentials, client-certificate keys and webhook secrets entered through the
API must be stored so that a database dump alone does not reveal them.

**Decision.** Secrets stored in PostgreSQL are sealed with AES-256-GCM; the additional
authenticated data binds table, primary key and column, so a ciphertext copied to another row does
not decrypt. Keys come from mounted files named by key ID; one key is active for sealing and every
listed key decrypts. Rotation adds a key, re-encrypts through a job and then removes the old key.
There is no default and no generated key: a missing key ring stops start-up, and the chart refuses
to render without the key Secret.

**Consequences.** Every replica and the migration Job mount the same key Secret, and the operator
keeps a copy outside the cluster; without it, sealed values in a restored database are lost. Every
replica holds the key ([H-8](spec/repositories-and-proxy.md#h-8--the-stored-secret-key-sits-on-every-replica)). Configuration-as-code diffs compare secrets by HMAC fingerprints,
which change once after a rotation. Decided:
- **One mechanism** ([Q-REPO-1](spec/repositories-and-proxy.md#q-repo-1--who-specifies-the-stored-secret-mechanism-iam-045), option (b)). architecture.md specifies it for every stored secret,
  and repository secrets only use it.
- **The operator creates the first key** ([Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key), option (b)). The chart fails without it and prints the
  command that creates one.
- **Key-ring status without pod access** ([Q-API-8](spec/api-and-ui.md#q-api-8--stored-secret-key-ring-status-through-the-api), option (b), [R-API-049](spec/api-and-ui.md#r-api-049--stored-secret-key-ring-status), MVP).
  `GET /api/v1/system/encryption` reports key IDs, sealed-value counts per key ID, the canary state
  and the key IDs every live replica holds, and never key material. The metrics
  `trove_secrets_sealed_values` and `trove_secrets_key_rings_compatible` and a card on the system
  status screen read the same state. Re-encryption runs as the manual task `secrets-reencrypt`,
  started through the task trigger. That trigger refuses with `409 key_ring_incompatible` while
  some live replica's ring lacks a key another replica uses, because values sealed then could not
  be opened everywhere.

**Alternatives considered.** References to external secret stores only: no sealed values in the
database, but credentials typed into the UI would need file access on every replica ([Q-REPO-1](spec/repositories-and-proxy.md#q-repo-1--who-specifies-the-stored-secret-mechanism-iam-045)).
A separate mechanism for repository secrets: a second key, rotation and backup item that drift
apart. trove writing Kubernetes Secrets: read access to every Secret in the namespace. A
chart-generated key kept with Helm's `lookup`: silently regenerated under `helm template` and
GitOps tools ([Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key)). Envelope encryption through a cloud KMS: a dependency not every
installation has; it fits later as an additional key source. Key management through the API: key
material would travel through the API and rest next to the values it protects ([Q-API-8](spec/api-and-ui.md#q-api-8--stored-secret-key-ring-status-through-the-api)).

**Spec:** [R-ARCH-021](spec/architecture.md#r-arch-021--encryption-of-stored-secrets-and-the-stored-secret-key), [R-REPO-033](spec/repositories-and-proxy.md#r-repo-033--upstream-authentication-and-stored-secrets), [R-HA-027](spec/ha-and-helm.md#r-ha-027--encryption-keys-for-sealed-secrets-in-a-replicated-deployment), [R-HA-029](spec/ha-and-helm.md#r-ha-029--fail-fast-chart-validation), [R-API-049](spec/api-and-ui.md#r-api-049--stored-secret-key-ring-status); decided:
[Q-REPO-1](spec/repositories-and-proxy.md#q-repo-1--who-specifies-the-stored-secret-mechanism-iam-045), [Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key), [Q-API-8](spec/api-and-ui.md#q-api-8--stored-secret-key-ring-status-through-the-api).

### ADR-033 · Versioning promises per surface and a schema compatibility gate

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Rolling upgrades run N and N-1 against one schema ([ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3)). Clients, scripts,
dashboards and configuration depend on surfaces that change at different speeds.

**Decision.** Releases follow SemVer. Any later minor of the same major installs directly;
zero-downtime rolling upgrades work from the previous minor and between patches, and `helm rollback`
to N-1 works. Per surface: `/api/v1` changes are additive only (checked in CI against the last
release with an OpenAPI breaking-change checker); a renamed configuration key or metric keeps its
old name for at least two minors; a token prefix never changes meaning.

Schema migrations:
- **Steps.** Each migration file declares its phase, `expand` or `contract`, in a
  `-- trove:phase` header. A contract step ships at the earliest one minor after the release that
  stopped using the old objects, and declares through `-- trove:requires-app` the oldest release
  allowed to serve once it is applied.
- **State.** R-HA-018's tables record it. `schema_migration` holds one row per applied step:
  class, shipping release, `last_used_by`, whether it ran in a transaction, checksum and duration.
  The single-row `schema_state` holds the applied schema number and `compat_floor`, the oldest
  trove version allowed to serve. A binary of version V serves only if `applied` is at least its
  `requiredSchema` and `compat_floor` is at most V.
- **Runner** ([Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner), option (d)). `trove migrate` is a trove-owned runner on native pgx in
  `internal/meta/postgres`, with no migration library and no `database/sql`. Under
  `pg_advisory_lock(3, 0)`, category `migration`, it verifies the checksum of every applied step
  and runs the contract guard against the replica registry. It then applies each pending embedded
  file as one argument-less `Exec` together with its `schema_migration` row in one transaction, so
  a crash leaves neither, and updates `schema_state` last.
- **No override.** The guard has no override flag. The downtime path is to scale the replicas to
  zero, so that the registry shows none.

**Consequences.** Removing schema objects takes two releases. Zero-downtime rolling upgrades work
between adjacent minors only; an upgrade that skips a minor goes stepwise or with a short downtime,
and the contract guard enforces this ([Q-HA-5](spec/ha-and-helm.md#q-ha-5--zero-downtime-upgrades-that-skip-minor-releases), option (a)). The schema itself is not a public
interface. The apply loop is trove's own code. Tests on every change cover it: every migration
against each supported PostgreSQL major, the N-1 compatibility test, and the crash and
edited-file criteria of R-ARCH-026.

**Alternatives considered.** Migrations with downtime: simpler code, no zero-downtime upgrades.
Keeping dropped objects for two minors: allows skips, doubles the life of dual-write code. A
migration library ([Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner)):
- goose was the earlier choice and was revised. Its version write carries only a version number,
  so R-HA-018's rows would need a custom store about as large as the runner itself, and it brings
  `database/sql` into the binary.
- golang-migrate leaves a dirty state that needs a manual force before the next deploy, and takes
  a lock of its own.
- tern takes a fixed single-`bigint` advisory lock that R-ARCH-017 forbids.

**Spec:** [R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy), [R-ARCH-026](spec/architecture.md#r-arch-026--schema-migrations-and-the-compatibility-gate), [R-HA-018](spec/ha-and-helm.md#r-ha-018--expandcontract-schema-migrations), [R-HA-019](spec/ha-and-helm.md#r-ha-019--migration-job), [R-HA-020](spec/ha-and-helm.md#r-ha-020--rolling-upgrade-and-mixed-version-operation), [R-HA-021](spec/ha-and-helm.md#r-ha-021--upgrade-policy-and-preparation); decided:
[Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner), [Q-HA-5](spec/ha-and-helm.md#q-ha-5--zero-downtime-upgrades-that-skip-minor-releases).

### ADR-034 · One PostgreSQL job table for maintenance, API operations and internal work

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Garbage collection, retention runs, reindexing, repository deletion, webhook delivery
and data migrations must run once, survive restarts and be visible through the API.

**Decision.** One `job` table serves maintenance tasks, long-running API operations and internal
work. Workers claim jobs with `FOR UPDATE SKIP LOCKED` in short transactions; a lease column is
extended by heartbeat; deduplication keys work in `exclusive` or `coalesce` mode; scheduled runs
take a unique task slot; progress, checkpoint and result columns feed the job API. Long-running API
operations answer 202 with the job ([ADR-085](#adr-085--long-running-work-answers-202-with-a-job)).

**Consequences.** One migration path; no library manages tables of its own. Queue throughput is
bounded by PostgreSQL, which suits maintenance volumes; webhook delivery (P1) is the case to measure
([Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation), decided: the trove-owned queue). Revisited on 2026-10-28, unchanged: only `worker` replicas
claim jobs and run the reaper and the scheduler, while `web` replicas only enqueue; `locked_until`
is the visibility timeout, kept current by the heartbeat, so a drained or lost worker gives its
jobs back without coordination, and a worker that shuts down releases its running jobs without
counting the attempt; wake-ups come through the Valkey signal area `jobs`, with
`jobs.poll_interval` as the bound ([ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases)). The job read and cancel API is MVP and stays with the
task list on operations-and-observability.md, while architecture.md keeps the model ([Q-OPS-9](spec/operations-and-observability.md#q-ops-9--ownership-of-the-job-api)).
Every singleton maintenance task starts only through `POST /api/v1/system/tasks/{name}/run` and
stops only through job cancel; the owning page keeps read-only views of its results. Storage jobs
are such tasks (`gc`, `gc-full`, `orphan-sweep`, `scrub`, `storage-summary`,
`remote-cache-cleanup`), with no run API or run table of their own ([Q-API-9](spec/api-and-ui.md#q-api-9--one-path-for-starting-stopping-and-reading-maintenance-work), [Q-STOR-10](spec/storage.md#q-stor-10--storage-maintenance-jobs-and-the-storage-summary-one-task-framework-one-path)).
Retention policies keep their per-policy run paths and use the same queue.

**Alternatives considered.** An external queue library on PostgreSQL: pre-1.0, with its own tables
and migrations, and a schema that would not hold the operation and task-slot columns. A message
broker: another stateful dependency. A queue in Valkey Streams ([Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation), option (c)): a Sentinel
failover can lose acknowledged writes, which for a job is lost work, and an enqueue could no longer
commit with the change that causes it. Domain start and stop paths per task, or start and stop paths
on both the domain and the task API: two ways to stop work, a special case per task on the system
status screen, and two paths per function.

**Spec:** [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-API-015](spec/api-and-ui.md#r-api-015--long-running-work-as-jobs), [R-API-020](spec/api-and-ui.md#r-api-020--resource-map-and-routing), [R-OPS-019](spec/operations-and-observability.md#r-ops-019--scheduled-maintenance-tasks-and-job-visibility), [R-STOR-025](spec/storage.md#r-stor-025--garbage-collection-schedule-full-sweep-and-manual-run); decided:
[Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation), [Q-OPS-9](spec/operations-and-observability.md#q-ops-9--ownership-of-the-job-api), [Q-API-9](spec/api-and-ui.md#q-api-9--one-path-for-starting-stopping-and-reading-maintenance-work), [Q-STOR-10](spec/storage.md#q-stor-10--storage-maintenance-jobs-and-the-storage-summary-one-task-framework-one-path).

### ADR-089 · Derived metadata is rendered per request, and a publish never waits for an index

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Clients read metadata derived from stored packages. In the MVP formats it is
per-package: the npm package document, NuGet registrations and version lists. Later formats add
repository-wide indexes such as a Helm `index.yaml`, a Debian `Packages` file or a Conda
`repodata.json`. If an index is rebuilt in the background after a deploy, a CI step that publishes
and then installs can miss the new version. If the publish waits for the rebuild, publishing
depends on background capacity.

**Decision.** Three rules ([Q-ARCH-12](spec/architecture.md#q-arch-12--repository-wide-indexes-rendered-per-request-or-built-by-jobs-and-does-a-publish-wait), option (d)):
1. **Per request by default.** Every derived index is rendered from committed rows at request time
   and cached under a revision. Per-package metadata uses the package's `rev_seq`. A
   repository-wide index uses a per-repository revision. The commit increments it only for formats
   whose `Descriptor().RepositoryIndex` is set, so npm and NuGet publishes never contend on a
   repository row.
2. **Jobs only by exception.** A format may build an index by a job only when its page shows one of
   two things: rendering per request misses the P-4 bound at the format's reference repository
   size, or the index must be signed or stored in several compressed variants. The job is a
   `coalesce` job keyed `index:<repository id>`. The previous index is served until the new one
   commits in one transaction, and the format page states a delay bound that a load test measures.
3. **No publish waits.** A publish answers when its own commit is durable, never after an index
   rebuild.

Every change that reaches package rows increments `rev_seq`: publish or upload, delete, trash
restore, copy, move, import, retention removal and remote cache fill. Clients never upload derived
metadata.

**Consequences.** Read-your-writes holds across an active-active cluster without coordination
wherever rendering is affordable, and staleness is bounded and tested where it is not. Publish
latency and success never depend on the job queue. A manual reindex (`exclusive`, `409` while one
runs) and the automatic rebuild share the key `index:<repository id>`, so they never run at the
same time. Every job-built index has to justify its exception on its format page. One virtual
metadata period, `mergedMetadataTtlSeconds`, applies to every format.

**Alternatives considered.** Every index rendered per request, always: signed indexes and
compressed variants of a large `Packages` file would be re-signed and recompressed on the request
path after every change. Repository-wide indexes always built by job, with the publish returning
at once: a publish-then-install step misses the new version even where rendering per request would
have been cheap. Jobs, with the publish waiting for the rebuild: publish latency and success are
bound to worker capacity, and a stuck queue makes publishes time out while clients retry and
duplicate work.

**Spec:** [R-ARCH-008](spec/architecture.md#r-arch-008--format-plug-in-contract), [R-ARCH-012](spec/architecture.md#r-arch-012--read-your-writes-package-metadata-for-local-repositories), [R-ARCH-015](spec/architecture.md#r-arch-015--request-path-virtual-resolution), [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-ARCH-027](spec/architecture.md#r-arch-027--performance-targets),
[R-REPO-016](spec/repositories-and-proxy.md#r-repo-016--recalculate-a-repositorys-index); decided: [Q-ARCH-12](spec/architecture.md#q-arch-12--repository-wide-indexes-rendered-per-request-or-built-by-jobs-and-does-a-publish-wait).

### ADR-098 · Valkey holds coordination and the negative cache, nothing served and nothing that grants

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** INV-3 admits Valkey for shared ephemeral state, shared caches included. Whoever can
write to Valkey can change what a shared cache returns, and every cross-replica mechanism needs one
home whose failure behavior is known.

**Decision.** Valkey holds coordination state and the negative cache of remote repositories;
nothing served to a client and no positive authentication or authorization result is ever read
from Valkey ([Q-ARCH-16](spec/architecture.md#q-arch-16--what-valkey-may-hold), option (b)). [R-ARCH-030](spec/architecture.md#r-arch-030--valkey-usage-map) applies two placement rules to every
mechanism:
1. State whose loss would cost correctness or data lives in PostgreSQL, including every mutual
   exclusion whose failure would corrupt data.
2. A value read from Valkey can at most make trove refuse: a negative entry answers `404` for a
   remote path, a counter answers `429`. A throttle exemption marker can only lift the throttle for
   one credential, which then costs one PostgreSQL lookup; it never accepts a credential.

The usage map that follows:
- **Valkey:** flight locks, negative entries behind a bounded per-replica L1, rate-limit counters,
  throttle exemption markers, change messages as the fast path of change areas, signals and a
  health probe.
- **PostgreSQL:** cache entries and stale copies of remote repositories, the upstream circuit, the
  job queue, leases and the migration lock, login flows and sessions, the replica registry, the
  break-glass lockout and rotation hold, upload sessions, download statistics, and the CI replay
  set and issuance records.
- **In process only:** rendered documents, merged metadata, and the token, session, permission,
  CI-mapping and issuer-key caches; Valkey carries only their change messages.

Rules for every key: the prefix `valkey.key_prefix` followed by the keyspace; a version segment per
encoding, so N and N-1 never misread each other; IDs and hashes only, never names, paths, token
values or client addresses; an expiry on every write; values of at most 1 KiB; validation before
use, with a malformed value ignored and deleted; commands to the primary only; any
`maxmemory-policy`, an eviction counting as an expiry. Only `internal/cluster` talks to Valkey.

**Consequences.** A compromised Valkey can make trove slower or make remote packages temporarily
unavailable, but it cannot grant access or change what a client installs ([H-49](spec/architecture.md#h-49--write-access-to-valkey-denies-service)). The negative
cache's writes leave PostgreSQL, together with its `UNLOGGED` table and cleanup job; a remote miss
gains one Valkey round trip. Negative entries count only within their checked lifetime and after
the newest covering invalidation, so even a forged entry is bounded. Because a Valkey value may
only make trove refuse, the shadowing check of a first publish no longer reads negative entries
([ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules)). The format and identity pages checked the line for their caches and kept it: npm
documents ([ADR-116](#adr-116--npm-documents-are-cached-per-replica-under-keys-read-from-postgresql)), forwarded NuGet searches ([ADR-120](#adr-120--forwarded-nuget-search-results-are-cached-per-replica)), sessions and tokens ([ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey)) and the
grant index ([ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed)).

**Alternatives considered.** Coordination only, with the negative cache in PostgreSQL: an insert per
upstream `404` and a cleanup job on the system of record, for state its own page calls disposable.
A shared second level for every cache: anyone who can write to Valkey could grant access or serve
altered metadata, such as a tarball URL with a matching integrity value, and rendered documents of
several MiB would load Valkey's network. The development agent would consider a shared rendition
cache only with an HMAC under a key Valkey never sees, if rendering limits scale-out.

**Spec:** [INV-3](spec/README.md#design-invariants), [R-ARCH-030](spec/architecture.md#r-arch-030--valkey-usage-map), [R-ARCH-018](spec/architecture.md#r-arch-018--in-process-caches-and-cross-replica-invalidation), [R-REPO-028](spec/repositories-and-proxy.md#r-repo-028--negative-cache), [H-49](spec/architecture.md#h-49--write-access-to-valkey-denies-service);
decided: [Q-ARCH-16](spec/architecture.md#q-arch-16--what-valkey-may-hold).

### ADR-099 · Valkey client: valkey-go against Valkey 9.0 and later, Sentinel in production

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** INV-12 puts Valkey with Sentinel into every production installation. The client must
follow Sentinel failovers, speak TLS and ACL to the data nodes and to the Sentinels, and fit the
statically linked binary without cgo. The supported server versions decide whether the flight lock
needs a script, and the topologies decide what the local profile and CI must exercise.

**Decision.**
- **Client and floor** ([Q-ARCH-13](spec/architecture.md#q-arch-13--valkey-client-library-and-supported-valkey-versions), option (a)). valkey-go (v1.0.79 at decision time) against
  Valkey 9.0 and every later minor line; the server version is checked at connect. Client-side
  caching is switched off, and only `internal/cluster` imports the client.
- **Sentinel protocol.** The client resolves the primary through the Sentinels, verifies it with
  `ROLE`, follows `+switch-master`, resolves again on every reconnection and reconciles
  periodically (`valkey.sentinel.refresh_interval`), which catches a missed failover message.
- **Least privilege.** TLS and an ACL user for the data nodes and for the Sentinels. Credentials
  are read on every connection attempt, so a rotated Secret takes effect without a restart. With
  `SET … IFEQ` and `DELIFEQ`, both present from 9.0, trove runs no scripts, and its ACL user can
  be denied scripting.
- **Topologies** ([Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports), option (b)). Sentinel is the only production topology at MVP;
  a standalone instance exists for development; a primary-endpoint mode for managed services is
  P1; Valkey Cluster is deferred.
- **Local profile and CI** ([Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci), option (d)). `deploy/dev/valkey.yaml` runs Valkey and three
  Sentinels from the official image, pinned by digest, on `emptyDir` only, with TLS, an ACL user
  and Sentinel timings set for failure injection. CI runs Valkey 9.0 and 9.1, the local profile
  9.1.

**Consequences.** A failover needs no restart of trove. A stolen trove credential cannot run
scripts inside Valkey. An installation on Valkey 8.x has to upgrade, and a managed service without
Sentinel waits for the P1 endpoint mode. trove maintains the development manifests and their
Sentinel bootstrap, which keeps every failure scenario of [R-HA-057](spec/ha-and-helm.md#r-ha-057--valkey-failover-outage-and-degraded-mode) a known operation.

**Alternatives considered.** From [Q-ARCH-13](spec/architecture.md#q-arch-13--valkey-client-library-and-supported-valkey-versions): valkey-go with an 8.1 floor, which needs a release
script, scripting rights and two code paths; go-redis, tested against Redis CE, whose
compare-and-delete targets another command than Valkey's; the Valkey project's multi-language
client, which needs cgo. From [Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports): Sentinel only for good; Valkey Cluster, which solves a memory
problem trove's small, ephemeral data does not have; a standalone instance in production, against
INV-12. From [Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci): the official Helm chart, whose latest release has no Sentinel settings
(checked 2026-10-28), or the same chart at an unreleased commit; a third-party operator in every
developer's profile.

**Spec:** [R-ARCH-031](spec/architecture.md#r-arch-031--valkey-client-sentinel-discovery-tls-acl-and-degradation), [R-HA-051](spec/ha-and-helm.md#r-ha-051--local-development-profile), [R-HA-056](spec/ha-and-helm.md#r-ha-056--valkey-with-sentinel-connection-and-supported-topologies), [R-HA-057](spec/ha-and-helm.md#r-ha-057--valkey-failover-outage-and-degraded-mode); decided:
[Q-ARCH-13](spec/architecture.md#q-arch-13--valkey-client-library-and-supported-valkey-versions), [Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports), [Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci).

### ADR-101 · Connection model: a transaction-scoped main pool behind an optional pooler, sessions only on workers

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** INV-12 says the load exceeds one replica and PostgreSQL connections must not grow
without bound with the replica count. Until 2026-10-28 every replica opened the main pool (20), the
lease pool (4), the fetch lock pool (16) and one `LISTEN` session, all direct or session-mode, and
transaction-mode poolers were not supported ([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), earlier option (a)). With 20 `web` and 3
`worker` replicas plus a surge pod, that is 984 server connections, growing by 41 per replica.

**Decision.** Connections are split by class and role ([Q-ARCH-14](spec/architecture.md#q-arch-14--postgresql-connection-model-for-n-replicas-and-transaction-mode-poolers), option (b)):
- **Main pool.** Transaction-scoped only, so it may run through a transaction-mode pooler from the
  MVP on (`database.pooler.mode: transaction`). Session `SET`, `LISTEN`, SQL `PREPARE`, `WITH HOLD`
  cursors and session-level advisory locks are confined to the code of the session classes, which
  a source test enforces. pgx's protocol-level prepared statements work with a pooler that tracks
  them, and `default_query_exec_mode=exec` covers poolers that do not.
- **Session connections** exist only for the lease pool of `worker` replicas, `trove migrate` and
  `trove backup`, through `database.session_host` ([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), revisited on 2026-10-28: option (b)
  instead of (a), and MVP instead of P2). `web` replicas hold none and never connect to the session
  host. Direct connections stay supported and remain the default.
- **Control connection.** One connection per replica is reserved inside `database.pool.max_conns`
  for the change-log poll, the heartbeats and the `database` check, so a request pool saturated
  under overload cannot fail readiness on one replica after another, and the budget does not grow.
  Behind a transaction-mode pooler it connects to a control endpoint (`database.control_host`,
  `control_port`, `control_name`) whose server pool serves only control connections, and the
  chart refuses a control endpoint equal to the request endpoint ([Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler), option (c)).
- **No waits on a connection.** No connection is held across a wait on a client, the object store,
  an upstream or Valkey.
- **Statement-mode poolers** are not supported.
- **Isolation.** `READ COMMITTED` everywhere, with two exceptions: the backup's snapshot
  transactions and the grant-index build, a `REPEATABLE READ READ ONLY` transaction on the request
  pool.

**Consequences.** With a pooler, the server sees the pooler's pool (for example 40), the lease
connections of the workers (4 × 4 in the example), a small control pool and the one-off processes:
about 60 to 70 connections in the example, whatever the `web` count. Without a pooler, 24 × 20 +
4 × 4 = 496 instead of 984. Fencing of the remaining singleton work is unchanged
([ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing)). Where a pooler is used, the request endpoint, the control endpoint and the session host
are configured and secured separately, per-session settings become role defaults ([R-HA-014](spec/ha-and-helm.md#r-ha-014--database-provisioning-roles-and-privileges)),
and the poolers are components to run, spread and drain. The chart's budget check bounds direct
connections ([R-HA-016](spec/ha-and-helm.md#r-ha-016--connection-budget-and-pool-sizing)); an installation whose `web` autoscaling would exceed it adds a pooler
instead of raising `max_connections`.

**Alternatives considered.** Direct or session-mode connections only, the earlier model: 984
connections in the example, against INV-12. Everything through a pooler with table-based leases:
weaker fencing to save a dozen connections. A pooled main pool plus one `LISTEN` session per
replica: a direct session per replica again. For the control connection: sharing the request
pooler, under which overload empties the Service instead of slowing it; the session host, one
server connection per `web` replica again; a separate login through the same pooler, a further role
and rotation pair whose isolation not every managed pooler offers.

**Spec:** [R-ARCH-017](spec/architecture.md#r-arch-017--session-connections-leases-and-advisory-lock-categories), [R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget), [R-HA-006](spec/ha-and-helm.md#r-ha-006--singleton-work-under-advisory-lock-leases-with-fencing), [R-HA-014](spec/ha-and-helm.md#r-ha-014--database-provisioning-roles-and-privileges), [R-HA-016](spec/ha-and-helm.md#r-ha-016--connection-budget-and-pool-sizing),
[R-HA-058](spec/ha-and-helm.md#r-ha-058--transaction-mode-pooler-for-the-main-pool); decided: [Q-ARCH-14](spec/architecture.md#q-arch-14--postgresql-connection-model-for-n-replicas-and-transaction-mode-poolers), [Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), [Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler).

### ADR-102 · Cluster-wide rate limits in Valkey with a per-replica fallback

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The API, identity and CI pages kept their rate-limit buckets in each replica's
memory, so the cluster-wide budget was the configured value times the replica count ([H-43](spec/api-and-ui.md#h-43--general-api-rate-limits-are-per-replica)).
Under the owner's constraints that count grows with the load and changes with autoscaling,
requests reach replicas at random, and an address throttled on one replica was unthrottled on the
next.

**Decision.** The limiter of [R-ARCH-034](spec/architecture.md#r-arch-034--cluster-wide-rate-limit-counters) counts cluster-wide in Valkey, with a sliding-window
counter per bucket and no scripts ([Q-ARCH-17](spec/architecture.md#q-arch-17--rate-limit-counters-across-replicas), option (b)). The round trip is pipelined,
bounded by `valkey.command_timeout` and skipped while the breaker is open. While Valkey is
unavailable, each replica keeps local buckets at the configured value divided by the live replicas
of its role. Every class that limits callers uses cluster scope:
- the API classes `read`, `write` and `anonymous` ([Q-API-3](spec/api-and-ui.md#q-api-3--cluster-wide-rate-limiting), revisited on 2026-10-28: option (e)
  instead of (a));
- the failed-credential throttle, the flow-start limit and the wrong-device-code limit
  ([Q-AUTH-11](spec/identity-and-authentication.md#q-auth-11--brute-force-counters-and-the-failed-credential-throttle-across-replicas), [ADR-115](#adr-115--brute-force-counters-count-cluster-wide-the-break-glass-lockout-and-rotation-stay-in-postgresql));
- the CI exchange, per client address with exempt networks and per CI subject after validation,
  without the earlier per-issuer bucket ([Q-CI-14](spec/gitlab-ci-oidc.md#q-ci-14--exchange-rate-limits-across-replicas), option (c)).

Limits that protect the replica itself, such as `server.max_in_flight` and
`proxy.max_concurrent_fetches`, stay per replica. The break-glass lockout stays in PostgreSQL.

**Consequences.** One budget at any replica count, and the same answer on every replica (INV-18).
Each limited request costs one Valkey round trip, budgeted in [R-ARCH-027](spec/architecture.md#r-arch-027--performance-targets). The configuration keys
are unchanged; their values are now cluster-wide budgets. [H-43](spec/api-and-ui.md#h-43--general-api-rate-limits-are-per-replica) is narrowed to Valkey outages and
counters lost in a failover; tampering with the counters is [H-49](spec/architecture.md#h-49--write-access-to-valkey-denies-service).

**Alternatives considered.** Per replica in memory, the earlier choice: a budget that multiplies with
autoscaling and a fresh throttle per replica for a credential guesser. Counters in PostgreSQL: a
write per request on the system of record, and a lever against it. Per-replica shares of the budget
as the normal mode: kube-proxy balances connections, not requests, so a client with keep-alive gets
a fraction of its budget.

**Spec:** [R-ARCH-034](spec/architecture.md#r-arch-034--cluster-wide-rate-limit-counters), [R-API-016](spec/api-and-ui.md#r-api-016--rate-limits), [R-AUTH-038](spec/identity-and-authentication.md#r-auth-038--brute-force-protection-and-rate-limiting), [R-CI-021](spec/gitlab-ci-oidc.md#r-ci-021--exchange-endpoint-hardening), [H-43](spec/api-and-ui.md#h-43--general-api-rate-limits-are-per-replica);
decided: [Q-ARCH-17](spec/architecture.md#q-arch-17--rate-limit-counters-across-replicas), [Q-API-3](spec/api-and-ui.md#q-api-3--cluster-wide-rate-limiting), [Q-AUTH-11](spec/identity-and-authentication.md#q-auth-11--brute-force-counters-and-the-failed-credential-throttle-across-replicas), [Q-CI-14](spec/gitlab-ci-oidc.md#q-ci-14--exchange-rate-limits-across-replicas).

### ADR-103 · Shutdown protocol: drain delay, transfers cut at the deadline, resumed on any replica

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A node drain or a rollout sends `SIGTERM`, and Kubernetes removes the pod from the
endpoints at the same moment; every proxy in front learns of it with a delay. The grace period
(120 s by default for `web` pods) bounds how long a replica may finish its work, and a slow client
or a large file can need longer. The owner requires a drain without outage when the setup is sized
for it.

**Decision.** Shutdown runs in the order of [R-ARCH-024](spec/architecture.md#r-arch-024--process-lifecycle-start-up-readiness-graceful-shutdown):
1. **Readiness fails at once** (`draining`). A `worker` stops claiming and releases the `scheduler`
   lease at once. A `web` replica holds no new cross-replica flight from now on
   ([Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down), option (b)).
2. **Drain delay.** A `web` replica keeps serving for `health.shutdown_drain_delay`, and every
   response carries `Connection: close`, so clients reconnect through the Service.
3. **Server shutdown.** Open event streams get `retry: 1000` and end; then `http.Server.Shutdown`
   runs with `server.shutdown_timeout`. The change-log poll and the grant-index rebuilds keep
   running.
4. **Five seconds before the deadline,** running job handlers are cancelled and their jobs released
   without counting the attempt, and running remote fetches are cancelled and their flight locks
   released with `failed`.
5. **At the deadline,** requests still running are cut ([Q-ARCH-18](spec/architecture.md#q-arch-18--transfers-that-outlast-the-termination-grace-period), option (a)), with one exception:
   an upload whose request body has been read completely is never cut between its commit and its
   status line. It runs on into the 5 s margin of the grace period, for at most 4 s, bounded by
   P-11, so a retried publish never meets its own committed version as a conflict.
6. **Last,** the write-only buffers and audit summaries are flushed, pending signals published,
   leases released and the registry row deleted; then the pools close and the process exits `0`.

The grace period must exceed the drain delay plus `server.shutdown_timeout` plus 5 s, and the chart
derives the timeout from the grace period ([R-HA-011](spec/ha-and-helm.md#r-ha-011--graceful-termination-and-connection-draining)).

**Consequences.** Drains stay bounded. A cut download is detectable, because every blob response
carries `Content-Length`, and resumable with `Range` and `If-Range` on any replica, because its
strong ETag is the content address ([R-STOR-012](spec/storage.md#r-stor-012--streaming-download-range-and-conditional-requests)); npm and NuGet retry whole requests (npm twice,
NuGet up to six tries). An upload cut while its body was still arriving commits nothing; a cut
request of an upload session keeps its recorded parts and releases its claim. Answers lost outside
trove are covered by npm's retry acknowledgement ([ADR-117](#adr-117--an-identical-npm-publish-retried-after-a-lost-answer-is-acknowledged)) and NuGet's identical-bytes push
([ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones)). Cuts are counted in `trove_http_transfers_cut_total`. A client that neither resumes nor
retries fails a transfer longer than the grace period. A `trove backup create` on a drained node
exits `75`, and its Job retries it, copying only what is still missing.

**Alternatives considered.** A grace period as long as the longest transfer: a node-pool upgrade
takes hours, and one stuck connection holds a node. Handing large downloads to the object store:
presigned redirects, P2, with their own trade-offs ([ADR-045](#adr-045--presigned-download-redirects-as-a-per-repository-opt-in-p2)). Moving live connections to another
replica: not possible without a connection-migrating proxy. From [Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down): cancelling every
fetch at `SIGTERM`, which throws away fetches that would have finished; handing partial transfers
over through S3, a large mechanism for small artifacts; fetching on the `worker` role, which adds
queue latency to every miss and only moves the drain problem.

**Spec:** [R-ARCH-024](spec/architecture.md#r-arch-024--process-lifecycle-start-up-readiness-graceful-shutdown), [R-HA-011](spec/ha-and-helm.md#r-ha-011--graceful-termination-and-connection-draining), [R-HA-059](spec/ha-and-helm.md#r-ha-059--node-drain-without-outage), [R-STOR-012](spec/storage.md#r-stor-012--streaming-download-range-and-conditional-requests),
[R-REPO-026](spec/repositories-and-proxy.md#r-repo-026--collapsing-concurrent-misses-single-flight-and-valkey-flight-lock); decided: [Q-ARCH-18](spec/architecture.md#q-arch-18--transfers-that-outlast-the-termination-grace-period), [Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down).

### ADR-104 · Subsystems per role: web serves, worker owns background work and leases

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** [ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2) splits the roles from the start. Which subsystems each role runs decides what a
drain of one role affects, which processes hold PostgreSQL sessions, and where work that outlasts a
request goes.

**Decision.** The role table of [R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles):
- **`web`** runs the client listener, the ops listener, the main pool, the Valkey client, the
  change-log poll and signal subscriber, and its registry row, and enqueues jobs. It never claims
  or runs jobs, holds a lease, opens a session connection, runs a background loop longer than a
  request, or leaves work running that a later request depends on. Three bounded exceptions remain,
  each ended at shutdown: the detached remote fetch that collapsed waiters depend on, the
  completion of a metadata refresh after the stale copy was served, and the replica's own
  support-bundle part (P1).
- **`worker`** serves only the ops listener and is not selected by the client Service. It runs the
  main pool and the lease pool, the job claim loop, the reaper, the scheduler and, from P1, the
  outbox dispatcher, plus the Valkey client, the poll and its registry row.
- **`backup`** has no client listener, serves `/metrics` only while a run lasts, opens its own
  session connections, uses no Valkey and keeps no registry row.
- **`all`** runs everything, for the local development profile only.

Work that a request starts and that outlasts it is a job: reindexing, repository deletion, bulk
copy or move, garbage collection, re-encryption, symbol indexing ([ADR-123](#adr-123--nuget-symbol-packages-are-indexed-by-one-job-each-on-the-worker-role)). Decided with it:
- **Readiness on `worker`** ([Q-OPS-12](spec/operations-and-observability.md#q-ops-12--what-readiness-means-on-a-worker-replica), option (c)). `worker` replicas run the same checks as
  `web`, and the claim loop claims only while the replica is ready: readiness means "may take
  work" for both roles.
- **Counters** ([Q-STOR-12](spec/storage.md#q-stor-12--counters-that-several-replicas-update-usage-quotas-and-download-statistics), option (a)). Every `worker` replica folds the usage deltas with
  `SKIP LOCKED` in a fixed order, without a lease, so folding scales with workers and survives
  drains.
- **Labels and status** ([Q-OPS-13](spec/operations-and-observability.md#q-ops-13--how-a-series-names-its-role-and-pool), option (b); [Q-UI-7](spec/api-and-ui.md#q-ui-7--roles-drain-readiness-and-valkey-on-the-system-status-screen), option (b)). Every series carries `role`
  and `pool`, and the status screen groups replicas by role and states drain safety per role from
  the registry's node and Valkey view.

**Consequences.** The `web` count does not change PostgreSQL's session count ([ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)), and
background work never competes with request latency. A `worker` drain delays jobs and fails no
request; a stale or misconfigured worker leaves the work to the others. No code path depends on the
number of replicas of a role, and a role can be split into pools through values ([R-HA-062](spec/ha-and-helm.md#r-ha-062--further-splits-of-a-role-into-several-deployments)).

**Alternatives considered.** Background loops on every replica, the earlier model: every replica
needed session connections, and a drain took both functions off a node. From [Q-OPS-12](spec/operations-and-observability.md#q-ops-12--what-readiness-means-on-a-worker-replica): no
readiness probe on `worker`, which lets a broken release replace working workers; readiness for
rollouts only, which lets a stale worker run jobs on stale grants; worker-specific checks, a second
definition to keep in step. Remote fetches as `worker` jobs ([Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down) (e)): queue latency on
every miss.

**Spec:** [R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles), [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-OPS-001](spec/operations-and-observability.md#r-ops-001--liveness-and-readiness-probes), [R-OPS-003](spec/operations-and-observability.md#r-ops-003--replica-registry-and-service-status), [R-HA-002](spec/ha-and-helm.md#r-ha-002--role-split-into-web-and-worker-deployments),
[R-HA-062](spec/ha-and-helm.md#r-ha-062--further-splits-of-a-role-into-several-deployments), [R-STOR-046](spec/storage.md#r-stor-046--storage-work-per-role-and-across-replicas), [R-UI-038](spec/api-and-ui.md#r-ui-038--system-status); decided: [Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation), [Q-OPS-12](spec/operations-and-observability.md#q-ops-12--what-readiness-means-on-a-worker-replica), [Q-OPS-13](spec/operations-and-observability.md#q-ops-13--how-a-series-names-its-role-and-pool),
[Q-STOR-12](spec/storage.md#q-stor-12--counters-that-several-replicas-update-usage-quotas-and-download-statistics), [Q-UI-7](spec/api-and-ui.md#q-ui-7--roles-drain-readiness-and-valkey-on-the-system-status-screen).

## Repositories and pull-through proxy

### ADR-035 · Virtual resolution in three tiers, decided per member

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A virtual repository combines local and remote members. The resolution order decides
whether an internal or a public package is installed, and whether upstreams are contacted at all.

**Decision.** After the endpoint gate ([ADR-053](#adr-053--virtual-repositories-endpoint-gate-plus-member-rights)), members are filtered by state, patterns, pin
rules and claims (decided without regard to the caller), then by the caller's `read` on each member;
unreadable members are skipped silently. Artifacts resolve in three tiers, first hit wins: local
members, then the caches of remote members, then remote upstreams (only for members where the
caller passes the cache-fill check). Latest-version requests consult every member. Metadata is
merged from every member that knows the package, each version taken from the member that would
serve its artifact. Writes go to the single deploy target; without one, the virtual repository is
read-only.

**Consequences.** Cached content beats an upstream round trip. The digest a client reads and the
bytes it downloads come from the same member. Merged-metadata cache entries are keyed by the member
set the caller sees, so callers with different rights never share one. A remote member that fails
transiently yields a partial merge that is not cached.

**Alternatives considered.** Strict member order including upstream fetches: slower, and contacts
upstreams for names a later local member holds. First member wins for metadata: hides versions held
by other members.

**Spec:** [R-REPO-018](spec/repositories-and-proxy.md#r-repo-018--nested-virtual-repositories), [R-REPO-019](spec/repositories-and-proxy.md#r-repo-019--virtual-resolution-algorithm), [R-REPO-020](spec/repositories-and-proxy.md#r-repo-020--merged-metadata-and-its-cache), [R-REPO-021](spec/repositories-and-proxy.md#r-repo-021--default-deployment-target).

### ADR-036 · Dependency-confusion protection through namespace claims and pin rules

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Tier order protects only names that currently exist internally. A name that is not yet
published, was deleted or is misspelt falls through to public upstreams, where anyone can register
it with a higher version.

**Decision.** Local repositories hold claimed name patterns. With auto-claim (proposed default for
formats that support claims), the first deploy of an unclaimed name creates an exact claim with
scope `members`; claims survive deletion. `members` claims act only in virtual repositories that
contain the claiming repository; `global` claims act in every virtual repository of the format and
need `repositories:write`. A claimed name resolves only from the most specifically claiming local
members, and remote members are not asked. With remote refusal on
(`proxy.claims.remote_refusal: true`), every remote repository of the format refuses
`global`-claimed names, also when addressed directly, unless a pin rule of the requesting virtual repository names that remote ([Q-REPO-4](spec/repositories-and-proxy.md#q-repo-4--scope-of-remote-refusal-for-claimed-names),
option (a)). Pin rules on a virtual repository bind name patterns to members. Pins, claims, extra
hosts and upstream credentials are generic repository members. Format pages add semantics, narrow
them and propose defaults, but define no members of their own ([Q-REPO-5](spec/repositories-and-proxy.md#q-repo-5--generic-or-format-level-placement-of-pins-extra-hosts-and-upstream-authentication), option (a)).

The first version of a name that a remote member of a containing virtual repository serves needs
`manage` on the coordinate or a prior manual claim of the local repository; otherwise it is
refused with `403 shadows_remote_package` ([R-REPO-048](spec/repositories-and-proxy.md#r-repo-048--shadowing-check-on-the-first-publish-of-a-name), [Q-REPO-6](spec/repositories-and-proxy.md#q-repo-6--local-publish-of-a-name-that-a-remote-member-already-serves-h-10), option (b)). The rules:
- **When it runs.** On a publish, and from P1 also on a copy, move or trash-can restore that gives
  a local repository its first version of a name. It does not run for names inside the
  repository's own claims.
- **Fail closed.** Whether a remote serves the name is decided from stored state first, then by a
  probe that stores nothing. While the upstream cannot be asked, the check fails closed with
  `503 shadow_check_unavailable`. Revisited on 2026-10-28, choice unchanged: stored state means
  PostgreSQL entries only. Negative entries moved to Valkey, and a value read from Valkey may only
  make trove refuse, never admit a write ([ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)), so a name for which a remote holds only a
  negative entry is probed.
- **Audit.** A permitted shadowing is audited as `repository.remote_package_shadowed`.
- **Auto claims.** An auto claim never takes a name from another repository's claim that is in
  effect in a containing virtual repository.

**Consequences.** The protection sits on the server and does not depend on each project's client
configuration. Claimed names are observable ([H-7](spec/repositories-and-proxy.md#h-7--claimed-names-are-observable)); member-scoped claims do not protect other
virtual repositories ([H-9](spec/repositories-and-proxy.md#h-9--member-scoped-claims-do-not-protect-other-virtual-repositories)). [H-10](spec/repositories-and-proxy.md#h-10--deploy-on-a-local-member-shadows-public-packages-for-every-consumer), a deploy to a local member that shadows a public package for
every consumer, is narrowed by R-REPO-048:
- **Deploy alone no longer suffices.** A first version of a name that shadows a remote package
  needs `manage` or a prior manual claim, also when it arrives through copy, move or restore (P1).
  A `deploy` grant alone, however broad, and a leaked CI token can no longer replace a public
  package.
- **The check fails closed** while the upstream cannot be asked.
- **Claims stay with their owner.** An auto claim never takes a name from another repository's
  claim.

What H-10 still records:
- deliberate shadowing by a `manage` holder;
- configuration changes that make existing names shadow without a decision;
- remotes in offline mode;
- the `403` as an existence oracle;
- probes that disclose a new name to the upstream once. Pre-claiming an internal prefix removes
  the probe.

Who may publish a name is a separate mechanism ([ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish)).

**Alternatives considered.** Client-side scoping only (npm scopes, NuGet package source mapping):
depends on every project getting its configuration right. Tier order alone: misses unpublished
names. `global` claims from any deploy: one deploy into a sandbox repository would block a public
name everywhere. Refusal only inside containing virtual repositories: a client pointed straight at
a remote still sends the internal name upstream. A per-remote exemption from refusal: one setting
reopens the leak for every client of that remote. Format-level members for pins, hosts and
credentials: one mechanism implemented and validated per format. For shadowing ([Q-REPO-6](spec/repositories-and-proxy.md#q-repo-6--local-publish-of-a-name-that-a-remote-member-already-serves-h-10)):
- Refusing the first publish for everyone adds a claim step to every legitimate fork.
- Warning only reports the damage after consumers resolved the replaced package.
- Narrow `deploy` grants alone were the state that left H-10 live.
- Deciding from stored state only leaves exactly the names a team is about to adopt unchecked.

**Spec:** [R-REPO-004](spec/repositories-and-proxy.md#r-repo-004--configuration-document-and-common-settings), [R-REPO-022](spec/repositories-and-proxy.md#r-repo-022--priority-members), [R-REPO-023](spec/repositories-and-proxy.md#r-repo-023--dependency-confusion-protection-pin-rules-and-namespace-claims), [R-REPO-048](spec/repositories-and-proxy.md#r-repo-048--shadowing-check-on-the-first-publish-of-a-name), [R-NPM-033](spec/formats/npm.md#r-npm-033--pin-rules-and-global-namespace-claims-for-npm); decided:
[Q-REPO-4](spec/repositories-and-proxy.md#q-repo-4--scope-of-remote-refusal-for-claimed-names), [Q-REPO-5](spec/repositories-and-proxy.md#q-repo-5--generic-or-format-level-placement-of-pins-extra-hosts-and-upstream-authentication), [Q-REPO-6](spec/repositories-and-proxy.md#q-repo-6--local-publish-of-a-name-that-a-remote-member-already-serves-h-10).

### ADR-037 · One name-pattern syntax, most specific wins, applied to every protocol request

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Include and exclude patterns, claims, pin rules, permission policies and retention
scopes all select packages. Different syntaxes would let a pattern copied from one into another
silently match something else.

**Decision.** A name pattern is a name glob of R-AUTHZ-005, compared with the format's rule (npm
case-sensitive, NuGet case-insensitive):
- `*` is any run of characters except `/`, `**` any run including `/`, `?` one character except
  `/`, and `\` makes the next character literal.
- An exact name and a prefix ending in `*`, the syntax of NuGet's package source mapping, are a
  subset with the same meaning.

One parser and one matcher, those of R-AUTHZ-004 and R-AUTHZ-005, evaluate routing patterns,
claims, pin rules, permission policies and retention scopes. Include and exclude lists have no
precedence among their patterns. Where several claims or pin rules match, the most specific wins:
an exact name beats any prefix, and a longer prefix beats a shorter one. npm and NuGet repositories
use name patterns rather than path patterns, so one pattern covers documents and files. Repository
patterns apply to every package-protocol request, administrators included.

**Consequences.** Routing safety does not depend on who runs the build. A pattern copied between a
repository, a permission policy and a cleanup policy selects the same names. Retention reuses the
R-AUTHZ-004 repository selectors and the R-AUTHZ-005 patterns through the same code
([Q-STOR-9](spec/storage.md#q-stor-9--one-pattern-syntax-across-retention-and-permissions), option (a)). The binding differs on purpose:
- Include key selectors are bound by repository ID, as in authorization, so a repository deleted
  and created again under the same key is not cleaned by an old policy without anyone noticing.
- Exclude key selectors are matched by key, so a re-created excluded repository keeps its
  protection.

On npm, `*` stops at `/`, so a list with `*` but neither `**` nor `@*/*` draws the warning
`pattern_unscoped_only`. Requests that name no package, such as NuGet's upstream service index,
vulnerability documents, search and autocomplete, are admitted as repository-level. A symbol
request is routed by the file name in its symbol key without the last extension, so the patterns,
claims and pin rules that protect `Acme.*` packages also protect `Acme.*` assemblies
([Q-NUGET-7](spec/formats/nuget.md#q-nuget-7--patterns-and-repository-level-upstream-documents), option (a)). Assembly names that no protecting pattern covers still reach an upstream
symbol server.

**Alternatives considered.** Path globs for every format: two patterns per npm package, one for the
document and one for tarballs. A syntax per subsystem: silent mismatches, and for a deletion
feature a silent mismatch deletes. Prefix-only patterns everywhere: cannot express version
selections that cleanup needs, such as `*-SNAPSHOT`. Patterns that skip administrators: an
administrator's build could leak internal names upstream. Separate symbol include and exclude lists
per remote: a second list to keep in step, which claims and pin rules never reach.

**Spec:** [R-REPO-005](spec/repositories-and-proxy.md#r-repo-005--include-and-exclude-patterns), [R-AUTHZ-004](spec/authorization.md#r-authz-004--repository-selectors), [R-AUTHZ-005](spec/authorization.md#r-authz-005--package-name-and-path-patterns), [R-STOR-038](spec/storage.md#r-stor-038--package-cleanup-policy-scope-and-conditions), [R-NUGET-015](spec/formats/nuget.md#r-nuget-015--remote-repository-proxying-caching-and-url-rewriting); decided:
[Q-STOR-9](spec/storage.md#q-stor-9--one-pattern-syntax-across-retention-and-permissions), [Q-NUGET-7](spec/formats/nuget.md#q-nuget-7--patterns-and-repository-level-upstream-documents).

### ADR-038 · Upstream failures: classified outcomes, a shared circuit, bounded stale serving

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Upstream outages and rate limits must not break builds that only need cached content,
and every replica retrying on its own would multiply the load on a struggling upstream.

**Decision.** Every upstream outcome has a fixed class: whether it counts toward the circuit and
what a metadata or artifact request answers. Server errors, connection failures, timeouts and 429
count; 404 and 410 write a negative-cache entry; authentication failures, certificate errors,
egress refusals and integrity failures do not count. Metadata falls back to the stored copy, bounded
by `cache.maxStaleSeconds` when set; artifacts answer 502, 503 or 504 with a specific code. The
circuit opens on a consecutive-failure threshold and is probed, shared across replicas. Only a
connection failure before any byte was sent is retried, once. Negative entries live in Valkey
behind a per-replica L1 since 2026-10-28 ([ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)); the circuit and the stored copies stay in
PostgreSQL. A metadata period of `0` is allowed, because revalidation is conditional and collapsed
([Q-REPO-2](spec/repositories-and-proxy.md#q-repo-2--lower-bound-of-the-metadata-period), option (a); revisited on 2026-10-28, unchanged: while Valkey is unavailable a
repository set to `0` sends up to one conditional request per replica and flight, and the fetch
budget no longer grows with the replica count). A format may register its own default period;
NuGet registers 300 s for its metadata documents and keeps the generic 1800 s for not-found entries
([Q-NUGET-2](spec/formats/nuget.md#q-nuget-2--metadata-cache-period-of-nuget-remotes), option (b); revisited on 2026-10-28, unchanged: refreshes collapse through the
Valkey flight lock, and while Valkey is unavailable an unchanged document costs one `304` per
period per `web` replica instead of one per remote).

Upstream removals ([Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document), option (b)):
- A removed document is marked gone and answers 404.
- A removed package document marks the package gone. Its documents and its cached artifacts then
  answer 404, on the remote, its cache view and in virtual repositories, until the upstream serves
  it again. The mark heals by itself after the negative period, or at once through an
  invalidation.
- Items stay stored and copyable into a local repository, which is how a build keeps a package that
  was removed legitimately.
- Formats that track removal per version replace the rule (npm, [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default)).

**Consequences.** Offline detection is deterministic and cluster-wide; builds continue on cached
metadata during outages. Whole-package removals reach cached artifacts in every format; version
removals only where the format tracks versions ([H-5](spec/repositories-and-proxy.md#h-5--version-removals-reach-cached-artifacts-only-where-the-format-tracks-versions), [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default)). A spurious upstream 404 hides
cached artifacts of that package until its negative entry expires or is invalidated. A new upstream
version of a NuGet package resolves through trove within five minutes, at the cost of more
conditional revalidations, mostly answered with 304.

**Alternatives considered.** A count of background retries before a repository counts as offline:
not deterministic across replicas. Freshness from the upstream's `Cache-Control`: behavior would
depend on each upstream. A 30-second minimum period: protects the upstream from nothing that
collapsing does not already prevent ([Q-REPO-2](spec/repositories-and-proxy.md#q-repo-2--lower-bound-of-the-metadata-period)). For removals ([Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document)):
- Keeping cached artifacts served after a removal was rejected: lockfile clients fetch artifacts by
  exact path, so the cache would keep handing out a takedown to every project that pinned it, as in
  [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default).
- Serving the stale document keeps advertising taken-down packages.
- Debouncing the mark delays every takedown by the negative period.
- A per-remote switch to keep serving duplicates what copying into a local repository already
  covers.

For NuGet's period ([Q-NUGET-2](spec/formats/nuget.md#q-nuget-2--metadata-cache-period-of-nuget-remotes)): the generic 7200 s leaves a new upstream version unresolvable for
up to two hours, the window in which update bots open merge requests. Catalog-driven invalidation
works only for upstreams that offer a catalog.

**Spec:** [R-REPO-027](spec/repositories-and-proxy.md#r-repo-027--metadata-freshness-and-refresh-timeout), [R-REPO-028](spec/repositories-and-proxy.md#r-repo-028--negative-cache), [R-REPO-029](spec/repositories-and-proxy.md#r-repo-029--upstream-failure-handling-and-the-assumed-offline-circuit), [R-REPO-030](spec/repositories-and-proxy.md#r-repo-030--offline-mode-per-repository), [R-REPO-031](spec/repositories-and-proxy.md#r-repo-031--global-offline-switch), [R-REPO-025](spec/repositories-and-proxy.md#r-repo-025--remote-request-path),
[R-NUGET-015](spec/formats/nuget.md#r-nuget-015--remote-repository-proxying-caching-and-url-rewriting); decided: [Q-REPO-2](spec/repositories-and-proxy.md#q-repo-2--lower-bound-of-the-metadata-period), [Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document), [Q-NUGET-2](spec/formats/nuget.md#q-nuget-2--metadata-cache-period-of-nuget-remotes) (NuGet 300 s).

### ADR-039 · Egress address checks after DNS resolution for every remote connection

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Remote URLs, redirect targets and URLs taken from upstream metadata could make a
remote repository reach internal addresses, including cloud metadata endpoints.

**Decision.** Every connection made for a remote repository checks each resolved IP address in the
dialer's `Control` hook, after DNS resolution and before connecting. Denied by default: loopback,
unspecified, link-local (which includes 169.254.169.254), `fd00:ec2::254`, multicast, broadcast and
their IPv4-mapped forms; `allow_cidrs` overrides. Private ranges are allowed for remote repositories,
where internal registries are the common case, unlike for webhooks. A refusal answers 502
`egress_denied` and is logged as a security event. Outbound proxies use Basic authentication only,
and proxy credentials are sent to the proxy only.

**Consequences.** A DNS answer that changes between check and use cannot pass. Through an outbound
proxy the proxy connects on its own, so the check is weaker there ([H-6](spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks), [H-40](spec/operations-and-observability.md#h-40--outbound-address-checks-are-weaker-through-an-outbound-proxy)). The source
address of upstream connections is a network prefix, `remote.sourceNetwork`, from which each
replica binds its own address; a replica without one answers `502 source_network_unavailable`
without opening the shared circuit ([Q-REPO-9](spec/repositories-and-proxy.md#q-repo-9--source-address-for-upstream-connections-with-interchangeable-replicas), option (b), 2026-10-28). One address for every
replica, the catalog's form, fits one pod at most, and per-replica addresses would break INV-2 and
every reschedule. The chart's
egress NetworkPolicy is an opt-in second layer, off by default because every narrower default
breaks a common platform feature (link-local DNS caches, cloud workload identity) or in-cluster
dependencies on some CNIs; `NOTES.txt` warns while it is off ([Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy), option (a)).

**Alternatives considered.** Checking the host name before resolution: bypassed by DNS rebinding.
Denying private ranges by default: breaks the common internal-registry case. NetworkPolicy alone:
not available on every cluster, and unaware of per-repository intent.

**Spec:** [R-REPO-024](spec/repositories-and-proxy.md#r-repo-024--remote-repository-settings), [R-REPO-037](spec/repositories-and-proxy.md#r-repo-037--outbound-http-proxies), [R-REPO-038](spec/repositories-and-proxy.md#r-repo-038--egress-address-rules-for-remote-repositories), [R-REPO-039](spec/repositories-and-proxy.md#r-repo-039--upstream-redirects), [R-ARCH-022](spec/architecture.md#r-arch-022--outbound-http-client-factory), [R-OPS-042](spec/operations-and-observability.md#r-ops-042--delivery-retries-and-outbound-protection); decided:
[Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy), [Q-REPO-9](spec/repositories-and-proxy.md#q-repo-9--source-address-for-upstream-connections-with-interchangeable-replicas).

### ADR-040 · Strict repository configuration documents and asynchronous deletion

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Repository configuration is edited through the API, the UI and configuration
documents. A silently ignored typo or a half-finished delete is hard to diagnose.

**Decision.** Create and update take JSON documents. Unknown members are refused with 400;
validation answers 422 listing every violation; an existing key answers 409. Reads return only the
members of the repository's class and format. Deleting a repository is a job and answers 202. npm
and NuGet repositories refuse content that is not a package.

**Consequences.** A script fixes a document in one round trip; clients that send obsolete members
fail loudly. Deletion progress is visible as a job.

**Alternatives considered.** Tolerating unknown members: misspelt settings ignored without notice.
Returning every known setting: dead fields to misread. Synchronous delete with a report: outlives
an HTTP request for large repositories.

**Spec:** [R-REPO-001](spec/repositories-and-proxy.md#r-repo-001--repository-classes-format-and-immutable-identity), [R-REPO-008](spec/repositories-and-proxy.md#r-repo-008--configuration-validation), [R-REPO-009](spec/repositories-and-proxy.md#r-repo-009--create-a-repository), [R-REPO-010](spec/repositories-and-proxy.md#r-repo-010--read-a-repository-configuration), [R-REPO-013](spec/repositories-and-proxy.md#r-repo-013--delete-a-repository).

### ADR-091 · Forwarded search and audit requests withhold protected names

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Remote repositories forward two kinds of request that carry names instead of fetching
a package: NuGet search and autocomplete queries, and npm's bulk audit requests. Forwarding them
tells the upstream operator what internal developers type or depend on. Not forwarding them breaks
discovery and security audits, and pushes developers to add the public registry as a second source,
the setup that reopens dependency confusion.

**Decision.** Both forward by default and withhold what trove can recognize as internal:
- **NuGet search** ([Q-NUGET-4](spec/formats/nuget.md#q-nuget-4--forwarding-search-to-the-upstream), option (c)). A remote forwards search and autocomplete queries
  (`nuget.forwardSearch: true`, switchable per remote). A query is answered from the cache alone
  when one of its terms names an ID that an exclude pattern, a claim or a pin rule keeps away from
  that remote. The rule applies per virtual repository too and reuses the claim lookup resolution
  already has.
- **npm audit** ([Q-NPM-3](spec/formats/npm.md#q-npm-3--audit-forwarding-on-by-default), option (c)). A remote forwards audit requests, but only names it already
  holds a document for, minus names that local repositories, claims or pin rules mark internal.
  Withheld names are reported in the `npm-notice` header, which npm prints, and a request whose
  names are all withheld is answered with `{}`. Withdrawn packages stay forwardable, because
  malware advisories are what an audit should find.

The tests differ because the requests differ. An audit body is a list of exact names, which can be
checked against what the remote has fetched, so no name leaves that the upstream has not already
seen from this installation. Search terms are free text, so only terms that name a protected ID can
be recognized.

**Consequences.** Discovery and audits work out of the box, and names marked as internal stay
inside. Free-text search terms outside protected namespaces, a project code name for example, still
leave the network; `forwardSearch: false` remains for installations that need more. An audit of a
lockfile resolved elsewhere is partial until its packages have passed through trove, and npm shows
that. The counters `trove_nuget_search_withheld_total` and `trove_npm_audit_names_withheld_total`
show how often names are withheld ([H-34](spec/formats/npm.md#h-34--audit-forwarding-discloses-the-dependency-set)).

**Alternatives considered.** Forwarding every query or every name except recognized internal ones:
discloses exactly the names an administrator marked as internal, or any internal name that nothing
marks. Cache-only search or disabled audit by default: `npm audit` fails on every run and search
finds no public package that nobody restored, so developers bypass trove.

**Spec:** [R-NUGET-014](spec/formats/nuget.md#r-nuget-014--remote-repository-upstream-and-resource-discovery), [R-NUGET-016](spec/formats/nuget.md#r-nuget-016--search-and-autocomplete-through-a-remote-repository), [R-NUGET-018](spec/formats/nuget.md#r-nuget-018--virtual-repository-merged-search-and-autocomplete), [R-NPM-038](spec/formats/npm.md#r-npm-038--security-audit-endpoints); decided:
[Q-NPM-3](spec/formats/npm.md#q-npm-3--audit-forwarding-on-by-default), [Q-NUGET-4](spec/formats/nuget.md#q-nuget-4--forwarding-search-to-the-upstream).

## Storage

### ADR-041 · The database row decides whether a blob exists

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** INV-4 writes an object only if it does not exist yet. An S3 `HEAD` as the existence
check races with garbage collection, and a lock held across a multi-gigabyte transfer would block
other writers.

**Decision.** A blob exists when its PostgreSQL row exists; an object without a row is an orphan.
Commit protocol: under `FOR SHARE` on the blob row, a missing row leads to writing the object and
then inserting the row as `live`; a row in state `deleting` makes the uploader wait until it is gone
and write again; a `live` row is a deduplication hit with no S3 write. No lock is held across an S3
transfer. Guarantee: a committed reference always points at a `live` blob whose object exists.

**Consequences.** A deduplication hit costs one database round trip and no transfer. Objects left
by crashed uploads are found by the orphan sweep ([R-STOR-026](spec/storage.md#r-stor-026--orphan-sweep)). Upload timing reveals whether
content is already stored ([H-13](spec/storage.md#h-13--deduplication-reveals-the-existence-of-known-content-through-upload-timing)).

**Alternatives considered.** S3 `HEAD` as the existence check: races with deletes. A lock across the
transfer: serializes concurrent uploads of the same popular content.

**Spec:** [R-STOR-005](spec/storage.md#r-stor-005--blob-commit-protocol-and-deduplication), [R-STOR-006](spec/storage.md#r-stor-006--transfer-to-s3-single-request-multipart-thresholds), [R-STOR-026](spec/storage.md#r-stor-026--orphan-sweep).

### ADR-042 · Race-free garbage collection with a grace period

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Deduplicated blobs are shared; deleting one while an upload or copy adds a reference
would corrupt a package.

**Decision.** Candidates come from a queue fed whenever a reference disappears and from a periodic
full sweep, both older than `storage.gc.grace_period` (24 h proposed). Per blob, in one READ COMMITTED
transaction: `FOR UPDATE` on the row (which waits for uploads holding `FOR SHARE`), a reference check
in a separate statement, then the state `deleting`; after commit the object is deleted, and the row
is removed once a settle period has passed. Every step is idempotent. Runs are the tasks `gc`
(incremental) and `gc-full` (full sweep) of the shared task framework ([ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work)), which share one
exclusive key so they never overlap. Each has its own schedule in five-field cron with the
Kubernetes macros, evaluated in UTC. Other dialects' forms (`?`, `7`, six fields, time-zone
prefixes) are rejected with `422 invalid_schedule`, so an expression from a dialect that counts
Sunday as 1 fails loudly instead of running on the wrong day ([Q-STOR-4](spec/storage.md#q-stor-4--cron-dialect-shared-by-all-schedules), [Q-OPS-7](spec/operations-and-observability.md#q-ops-7--one-cron-syntax-for-the-whole-system), option (a)
on both pages). Q-OPS-7 was revisited on 2026-10-28 and states its scope: the backup CronJob's
schedule is evaluated by Kubernetes, so the chart validates it against the same rejected forms and
sets `backup.timeZone` to `Etc/UTC` by default (option (e)).

Added on 2026-10-28, when runs started to move between `worker` replicas at any drain:
- **Backup hold check** ([Q-BAK-2](spec/backup-and-restore.md#q-bak-2--keeping-a-running-backups-blobs-away-from-garbage-collection)). The per-blob transaction also keeps a blob while a running
  backup holds `all`, or holds a pin on that digest, under a valid lease. The check only ever keeps
  a blob, so it cannot weaken the protocol ([ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold)).
- **Exclusion by the job key only** ([Q-STOR-13](spec/storage.md#q-stor-13--exclusion-of-storage-maintenance-runs-job-key-fenced-lease-or-valkey-lock), option (a)). Storage maintenance runs are kept apart
  by their exclusive job key in PostgreSQL. Their safety rests on row locks, so they take no
  advisory-lock lease and no Valkey lock, and they run through a transaction-mode pooler.
- **Hand-over rule.** A collector sends the S3 delete for a row it claimed only while less than
  `storage.gc.handover_delay` minus `delete_settle` has passed since its claim, measured on its own
  monotonic clock read before the claim transaction, never compared with the database's clock.
  Another run finishes such a row only after its `state_changed_at` is older than
  `handover_delay` (10 minutes, proposed).
- **Hand-over at release.** A collector that is drained cancels its S3 calls, waits for them to
  return, and hands its unfinished claims over in one statement, so the next run finishes them
  after `delete_settle` instead of `handover_delay`.

**Consequences.** Collection never removes an object a committed reference uses, and no upload
commits against a blob being deleted. The grace period also lets content that is deleted and
republished shortly after (common in CI) deduplicate, and bounds which point-in-time restores stay
consistent ([ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination)). A running backup is never failed by collection, whatever the grace period. A
collector that was drained, paused or partitioned cannot delete an object that an uploader wrote
again after another run finished its row; the remaining gap, a process frozen between its deadline
check and the request, is what the scrub detects. After a crash, uploads of the digests the lost
collector had claimed can answer `503` for up to `handover_delay` plus one `gc` interval.

**Alternatives considered.** A reference counter column: drifts under crashes and concurrency.
Mark-and-sweep over the bucket listing alone: expensive on large buckets and racing with uploads.
No collection: unbounded growth. From [Q-STOR-13](spec/storage.md#q-stor-13--exclusion-of-storage-maintenance-runs-job-key-fenced-lease-or-valkey-lock): a fenced advisory-lock lease, which costs session
connections and cannot stop a late S3 delete; a Valkey lock, which a failover loses. For schedules: the six-field dialect with seconds and `?`, whose
weekday numbering conflicts with every crontab; accepting both dialects by field count, which runs
a pasted expression on the wrong day; a time zone per schedule, which can be added later as a
separate field.

**Spec:** [R-STOR-024](spec/storage.md#r-stor-024--race-free-garbage-collection), [R-STOR-025](spec/storage.md#r-stor-025--garbage-collection-schedule-full-sweep-and-manual-run), [R-STOR-046](spec/storage.md#r-stor-046--storage-work-per-role-and-across-replicas), [R-STOR-047](spec/storage.md#r-stor-047--storage-state-in-backups-and-restores), [R-BAK-007](spec/backup-and-restore.md#r-bak-007--garbage-collection-hold);
decided: [Q-STOR-4](spec/storage.md#q-stor-4--cron-dialect-shared-by-all-schedules), [Q-OPS-7](spec/operations-and-observability.md#q-ops-7--one-cron-syntax-for-the-whole-system), [Q-STOR-10](spec/storage.md#q-stor-10--storage-maintenance-jobs-and-the-storage-summary-one-task-framework-one-path), [Q-STOR-13](spec/storage.md#q-stor-13--exclusion-of-storage-maintenance-runs-job-key-fenced-lease-or-valkey-lock), [Q-BAK-2](spec/backup-and-restore.md#q-bak-2--keeping-a-running-backups-blobs-away-from-garbage-collection).

### ADR-043 · Object-store outage: no local queue, and 201 means durable

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** S3 can become unreachable while PostgreSQL is fine. Queueing uploads locally would
keep accepting writes, but a replica loss would then lose acknowledged packages.

**Decision.** During an outage, uploads of new content answer 503 with `Retry-After` and commit
nothing; a 201 always means the content is durable in S3. Everything that touches only PostgreSQL
keeps working: deduplicated uploads, deploy by checksum, copy, move, delete, properties, trash
operations, `HEAD`, listings, UI and API. Downloads of stored content answer 503. Collection jobs
back off.

Readiness follows the revised INV-12 ([ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12), [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), option (d)):
- **First readiness.** Object storage gates only a replica's first readiness, through the start-up
  check. That check keeps retrying transient errors, so a replica started during an outage waits
  not ready instead of crash-looping, and becomes ready as soon as the store returns.
- **Running replicas** stay ready. `/readyz` reports `degraded`, and the circuit breaker fails blob
  operations per request.
- **Opt-in.** `health.require_object_store: true` gates continuously with the thresholds of the
  operations page.

The storage page's own hysteresis keys are removed. [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12) was revisited on 2026-10-28 and
stays (d): a node drain during an outage costs each role at most one replica of capacity, because
the disruption budget refuses the next eviction while the replacement is unready, and Valkey plays
no part in the rule.

**Consequences.** No acknowledged write is ever lost to a replica failure. A shared outage no
longer removes every replica from the Service, so the operations above stay available during it.
A rollout with a broken S3 configuration stalls instead of replacing working replicas.

**Alternatives considered.** Asynchronous upload to S3 from local disk (deferred catalog entry
STOR-016): would change what 201 means. Keeping INV-12's continuous gating: a shared outage becomes
a total one, although everything above could still be served.

**Spec:** [R-STOR-033](spec/storage.md#r-stor-033--connection-pool-timeouts-retries-and-circuit-breaker), [R-STOR-035](spec/storage.md#r-stor-035--object-store-consistency-assumptions-and-conformance-check), [R-STOR-036](spec/storage.md#r-stor-036--behavior-during-an-object-store-outage); decided: [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12).

### ADR-044 · A trash can that also covers overwrites (P1)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Deletes and overwrites in local repositories are the classic accident, for example a CI
job republishing a release path.

**Decision.** With the trash can enabled (proposed default: on, 14 days), deletions in local
repositories mark items as trashed instead of removing them. This covers user deletes, overwrites of
user-deployed files, retention policies unless they bypass it, and repository caps. Generated
metadata is replaced without trash. Restore and permanent removal are explicit operations.

**Consequences.** Storage is freed only after the retention period; repositories that overwrite
routinely accumulate trash volume. npm and NuGet tombstones survive the trash and its purge
([ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones)). For the MVP formats the overwrite case is rare, because their versions are
immutable; it matters mainly for raw and generic paths. Decided: an overwrite of a user-deployed
file in a local repository sends the previous item to the trash can ([Q-STOR-5](spec/storage.md#q-stor-5--does-overwriting-a-file-send-the-previous-content-to-the-trash-can), option (a)).

**Alternatives considered.** Trash for deletes only: leaves `overwrite` as the one destructive
action without a safety net ([Q-STOR-5](spec/storage.md#q-stor-5--does-overwriting-a-file-send-the-previous-content-to-the-trash-can)). A per-repository switch to opt out: a setting whose
only use is to turn protection off; it can be added later without changing the default.

**Spec:** [R-STOR-022](spec/storage.md#r-stor-022--trash-can), [R-STOR-023](spec/storage.md#r-stor-023--restore-and-permanent-removal-from-the-trash-can); decided: [Q-STOR-5](spec/storage.md#q-stor-5--does-overwriting-a-file-send-the-previous-content-to-the-trash-can).

### ADR-045 · Presigned download redirects as a per-repository opt-in (P2)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Streaming large downloads through the replicas costs bandwidth and CPU that the object
store could serve directly.

**Decision.** A local or caching remote repository may enable presigned downloads (default off).
An eligible `GET` (size above a threshold, verified content, circuit closed) answers 302 to a
presigned `GetObject` URL with a short validity and `Cache-Control: no-store`. Authorization runs
before the URL is issued; download statistics count at issuance. A public endpoint for signing can be
configured separately from the in-cluster endpoint. Specified now so the download path leaves room
for it; implemented in P2.

**Consequences.** A presigned URL is a bearer capability until it expires ([H-12](spec/storage.md#h-12--presigned-download-urls-are-bearer-capabilities)). Download
verification does not apply to redirected downloads.

**Alternatives considered.** Always streaming: simplest and verifiable, the default. Always
redirecting: exposes the object store to every client network.

**Spec:** [R-STOR-012](spec/storage.md#r-stor-012--streaming-download-range-and-conditional-requests), [R-STOR-013](spec/storage.md#r-stor-013--download-integrity-verification), [R-STOR-014](spec/storage.md#r-stor-014--presigned-download-redirect-per-repository).

## Identity and authentication

### ADR-046 · Server-side UI sessions behind an opaque cookie

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The UI needs a login state that can be ended immediately, on every replica.

**Decision.** A session is created only after a successful OIDC callback or break-glass login, and
any session cookie presented at that moment is replaced. The session secret is 32 random bytes;
PostgreSQL stores only its SHA-256, and Valkey holds nothing of a session ([ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey)). The cookie is `__Host-trv_session` with
`Secure; HttpOnly; SameSite=Lax; Path=/`, without `Domain`, `Max-Age` or `Expires`. `Lax` is needed
because the first navigation after the identity provider's redirect must carry the cookie. Unsafe
cookie-authenticated requests need a CSRF token from a second cookie, `__Host-trv_csrf`, sent back
as a header. The login flow's binding cookie is `__Host-trv_oidc_binding`. Default timeouts
(marked proposed in R-AUTH-011): idle 1 h, absolute 12 h.

**Consequences.** Logout and revocation take effect on every replica within the token-cache bound
([ADR-048](#adr-048--tokens-hashed-with-sha-256-a-positive-only-cache-bounded-revocation-latency)). The `__Host-` prefix makes the browser enforce the flags on all three cookies: host-only,
`Secure` and `Path=/`, so a sibling subdomain or a plain-HTTP response can neither plant nor shadow
them, which also closes login CSRF through a planted binding cookie. The binding cookie uses
`Path=/` and is therefore sent on every request during its 10 minutes; it is `HttpOnly` and useless
without its flow row. Clearing a cookie needs `Secure` and `Path=/` as well, or the browser keeps
it.

**Alternatives considered.** Stateless signed session cookies: no immediate logout without a
server-side deny-list. `SameSite=Strict`: the cookie would be missing right after the login
redirect. Unprefixed cookie names: the flags stay a convention, open to cookie tossing. The prefix
for the session and CSRF cookies only: the login-CSRF defense would still rest on a cookie a sibling
subdomain can plant ([Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name)).

**Spec:** [R-AUTH-005](spec/identity-and-authentication.md#r-auth-005--authorization-request-with-pkce-state-and-nonce), [R-AUTH-010](spec/identity-and-authentication.md#r-auth-010--server-side-ui-sessions), [R-AUTH-011](spec/identity-and-authentication.md#r-auth-011--session-timeouts-and-recent-authentication), [R-AUTH-012](spec/identity-and-authentication.md#r-auth-012--csrf-protection-for-cookie-authenticated-requests), [R-AUTH-013](spec/identity-and-authentication.md#r-auth-013--logout-and-rp-initiated-logout); decided:
[Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name).

### ADR-047 · A break-glass administrator outside the database

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** If the identity provider is down or misconfigured, someone must still be able to log in
and repair trove, without depending on the state being repaired.

**Decision.** Exactly one local account, always treated as system administrator without a role
binding. Its password never enters PostgreSQL: it comes from a mounted Kubernetes Secret, preferably
as an Argon2id hash (at least m=65536 KiB, t=3, p=4). A plaintext password must have at least 20
characters and must not be on an embedded list of common passwords, or the replica refuses to start.
A changed password file ends all break-glass sessions; since 2026-10-28 the first replica that
notices the change does that in one PostgreSQL transaction and opens a rotation hold for every
replica ([ADR-115](#adr-115--brute-force-counters-count-cluster-wide-the-break-glass-lockout-and-rotation-stay-in-postgresql)). The account can be disabled.

**Consequences.** Recovery works with an empty or broken permission configuration. The account is
single-factor ([H-17](spec/identity-and-authentication.md#h-17--break-glass-account-is-single-factor)), which is why it can be disabled and is meant for emergencies.

**Alternatives considered.** No local account: an identity-provider misconfiguration locks everyone
out. A local administrator with a password in the database: recovery would depend on the database
state it recovers.

**Spec:** [R-AUTH-014](spec/identity-and-authentication.md#r-auth-014--break-glass-administrator), [R-AUTH-015](spec/identity-and-authentication.md#r-auth-015--bootstrap-and-recovery-commands).

### ADR-048 · Tokens hashed with SHA-256, a positive-only cache, bounded revocation latency

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Package clients present a token on every request, and a revoked token must stop working
everywhere quickly.

**Decision.** PostgreSQL stores the SHA-256 of the full token; with 256 bits of entropy a salt or
slow hash adds nothing. Validation checks syntax and checksum first (no database access on failure),
then a per-replica cache of positive results, then the database. Revised on 2026-10-28: the cache
is invalidated through the `auth` change area of the change log on every revocation, rotation,
principal change and membership change, with Valkey as the fast path ([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)), and it answers
only while the replica's last change-log poll is younger than `auth.revocation_bound` ([ADR-114](#adr-114--authentication-caches-answer-only-while-the-change-log-is-fresh)).
A revoked token is therefore rejected on every replica within 2 s in every failure mode, where
the earlier `LISTEN/NOTIFY` design promised 2 s only while notifications flowed and the cache TTL
(15 s) otherwise. The cache stays in process; no validation result is read from Valkey
([ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey)). Revocation by value
is open to anyone, because possession already allows revocation. It is rate-limited and answers
202 for every well-formed value, so it reveals nothing (decided, [Q-AUTH-6](spec/identity-and-authentication.md#q-auth-6--unauthenticated-revoke-by-value), option (a)). A
by-value revocation follows the rotation chain forward, even when the presented token is already
revoked or expired, so a leaked predecessor ends the chain. Rotation is renewal, never a leak
response: a leaked token is revoked and replaced.

**Consequences.** One indexed lookup per uncached request, and one per lookup while the change-log
view is stale. Secret scanners can revoke leaked tokens directly. The holder of a stale, already rotated value can end its live successor. That is
deliberate: trove cannot tell a holder's rotation from a thief's, and the legitimate holder loses
nothing they could keep safely and creates a new token.

**Alternatives considered.** A slow password hash: costs CPU on every uncached request and protects
nothing at this entropy. Caching negative results: would keep rejecting a token after a transient
error. Revocation by value for administrators or authenticated principals only: leaked tokens live
until someone with an account acts, and external finders are excluded. A time-limited cascade that
follows the chain only for recently rotated tokens was considered and rejected: it fails in the case
the cascade exists for, a leak found after the thief has rotated.

**Spec:** [R-AUTH-023](spec/identity-and-authentication.md#r-auth-023--token-storage-validation-and-caching), [R-AUTH-029](spec/identity-and-authentication.md#r-auth-029--token-revocation), [R-AUTH-030](spec/identity-and-authentication.md#r-auth-030--token-rotation); decided: [Q-AUTH-6](spec/identity-and-authentication.md#q-auth-6--unauthenticated-revoke-by-value), [Q-AUTH-10](spec/identity-and-authentication.md#q-auth-10--revocation-bound-when-a-replica-cannot-read-the-change-log).

### ADR-049 · Command-line login through the browser session (P1)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Users should get a token into npm or another tool without copying it from the UI, and
without trove ever handling their identity-provider password.

**Decision.** `npm login` uses npm's web-login protocol on top of the OIDC session: npm opens an
approval page, the user approves in the browser, npm polls and receives a personal access token.
Other tools use the OAuth 2.0 Device Authorization Grant (RFC 8628) with a user code that binds the
browser to the terminal. npm's legacy login accepts only an existing token as password and never
mints one.

The npm poll is bound to the network it started from (decided, [Q-AUTH-8](spec/identity-and-authentication.md#q-auth-8--address-binding-of-the-npm-web-login-poll), option (d)):
- **Default mode `network`.** A poll matches when it comes from the starting address, from the same
  IPv4 `/24` or IPv6 `/56`, or from the same operator range (`poll_binding.equivalent_ranges`).
  The modes `exact` and `off` stay selectable.
- **Refused polls** appear on the approval page, are audited (`cli_login.poll_refused`) and
  counted.
- **Trusted proxies.** The binding depends on the client address, and with it on
  `server.trusted_proxies`. Behind an edge proxy with that list empty, every poll carries the
  proxy's address. trove cannot detect that state, so the settings warn when the list is empty,
  and also when the mode is `off`.

**Consequences.** No password prompt in any client; every token is created by an authenticated
browser session. Consent phishing of the browser step ([H-15](spec/identity-and-authentication.md#h-15--consent-phishing-of-command-line-browser-login)) and login secrets in URLs outside
trove ([H-20](spec/identity-and-authentication.md#h-20--command-line-login-secrets-in-urls-outside-trove)) remain. With network binding, H-20 is limited to a reader of proxy or ingress logs
inside the user's `/24` or `/56` within the 10-minute flow, which no binding mode stops when that
reader shares the user's egress. Multi-egress corporate networks keep web login working, where an
exact-address binding made npm fall back to the legacy prompt.

**Alternatives considered.** Username and identity-provider password in the client: trove would
handle passwords, against [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7). Copying tokens from the UI only: works, but pushes users to
long-lived tokens in dotfiles. For the poll binding ([Q-AUTH-8](spec/identity-and-authentication.md#q-auth-8--address-binding-of-the-npm-web-login-poll)):
- An exact address breaks web login on every multi-egress network.
- No binding lets any real-time reader of a log anywhere collect the token.
- Exact addresses plus operator ranges fail by default on corporate networks.
- Web login off by default was considered: it removes H-20 by removing the feature, and makes
  every npm user copy a longer-lived token from the UI. It stays available as
  `npm_web_login: false` for operators whose access logs are widely readable.

**Spec:** [R-AUTH-034](spec/identity-and-authentication.md#r-auth-034--npm-web-login), [R-AUTH-035](spec/identity-and-authentication.md#r-auth-035--npm-legacy-login-and-logout), [R-AUTH-036](spec/identity-and-authentication.md#r-auth-036--browser-login-handoff-for-other-command-line-tools), [R-AUTH-040](spec/identity-and-authentication.md#r-auth-040--runtime-authentication-settings), [R-NPM-020](spec/formats/npm.md#r-npm-020--web-login), [R-NPM-021](spec/formats/npm.md#r-npm-021--legacy-login-and-logout);
decided: [Q-AUTH-8](spec/identity-and-authentication.md#q-auth-8--address-binding-of-the-npm-web-login-poll).

### ADR-050 · Anonymous access is off by default and needs two switches

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Some installations serve public packages without credentials; most must not.

**Decision.** A built-in `anonymous` principal always exists but holds no tokens, sessions or group
memberships. Anonymous access needs both an operator ceiling in the configuration file (default
allowed) and a runtime switch set by an administrator (default off). When enabled, a request without
credentials runs as `anonymous` with exactly the grants policies give it. Hard limits apply
regardless of policies: no unsafe methods on `/api/v1`, no administration role, no system scope.

**Consequences.** An operator can rule anonymous access out for an installation; an administrator
cannot enable it by accident through a policy alone.

**Alternatives considered.** A single runtime switch: an administrator could open an installation
the operator meant to keep closed. Anonymous access as an ordinary group: no hard limits.

**Spec:** [R-AUTH-037](spec/identity-and-authentication.md#r-auth-037--anonymous-access), [R-AUTHZ-003](spec/authorization.md#r-authz-003--anonymous-access).

### ADR-113 · Authentication state lives in PostgreSQL and in-process caches, never in Valkey

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** INV-3 admits shared caches in Valkey. With n `web` replicas and random distribution,
one credential in continuous use is looked up by up to n replicas per `cache_ttl`, and a second
level in Valkey would bring that down to about one. Three flows span several requests (browser
login, npm web login, device login), and idle timeouts were checked against one replica's view;
under INV-18 each step may reach another replica. [Q-ARCH-16](spec/architecture.md#q-arch-16--what-valkey-may-hold) forbids reading a positive
authentication result from Valkey.

**Decision.**
- **Caches in process** ([Q-AUTH-9](spec/identity-and-authentication.md#q-auth-9--shared-valkey-cache-for-session-and-token-lookups), option (a)). Sessions, tokens, principals and runtime
  settings are cached in each replica's memory only. Valkey carries their change messages in area
  `auth` and nothing else; no entry is read from or written to Valkey.
- **Login flows in PostgreSQL** ([Q-AUTH-12](spec/identity-and-authentication.md#q-auth-12--where-multi-request-login-flows-keep-their-state), option (a)). The flows keep their state in
  `oidc_login_flow` and `cli_login` rows and change it with conditional statements, so the callback
  consumes its flow once and an approval mints one token, whichever replicas the steps reach. Poll
  pacing (`last_polled_at`) and refused polls are columns of the row, and the `session-cleanup`
  task removes expired rows.
- **Idle timeout from the row** ([Q-AUTH-13](spec/identity-and-authentication.md#q-auth-13--idle-timeout-tracking-across-replicas), option (a)). A replica touches `last_seen_at`
  conditionally, at most once per minute and session, decided from the newest value it has read. A
  cached entry whose idle deadline has passed is re-read before the session ends.

**Consequences.** Valkey can never authenticate anyone, approve a pending login or keep a stolen
session alive. Credential lookups grow with replicas times credentials in use, not with requests:
20 replicas and 1,000 credentials in continuous use cost at most about 1,300 indexed lookups per
second. 100 pending logins cost at most 50 poll reads per second. Every replica ends a session at
the same moment, up to 60 s before its idle timeout and never after it.

**Alternatives considered.** A Valkey second level with plain entries: whoever can write under the
prefix authenticates as any principal. Sealed entries: forgery is closed but replay is not, unless
each entry is bound to the `auth` epoch, and then every revocation empties the level. Sessions or
login flows in Valkey: a failover loses them or brings ended ones back, and a writer could approve
a pending login. Session affinity on `/auth/`: forbidden by INV-12 and broken by drains. A write of
`last_seen_at` on every request: writes multiplied by the request rate. A sliding expiry key in
Valkey: session lifetime would depend on Valkey.

**Spec:** [R-AUTH-010](spec/identity-and-authentication.md#r-auth-010--server-side-ui-sessions), [R-AUTH-023](spec/identity-and-authentication.md#r-auth-023--token-storage-validation-and-caching), [R-AUTH-034](spec/identity-and-authentication.md#r-auth-034--npm-web-login), [R-AUTH-036](spec/identity-and-authentication.md#r-auth-036--browser-login-handoff-for-other-command-line-tools),
[R-AUTH-042](spec/identity-and-authentication.md#r-auth-042--authentication-across-replicas-and-roles), [R-AUTH-043](spec/identity-and-authentication.md#r-auth-043--scheduled-authentication-tasks); decided: [Q-AUTH-9](spec/identity-and-authentication.md#q-auth-9--shared-valkey-cache-for-session-and-token-lookups), [Q-AUTH-12](spec/identity-and-authentication.md#q-auth-12--where-multi-request-login-flows-keep-their-state), [Q-AUTH-13](spec/identity-and-authentication.md#q-auth-13--idle-timeout-tracking-across-replicas).

### ADR-114 · Authentication caches answer only while the change log is fresh

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Until 2026-10-28, [R-AUTH-023](spec/identity-and-authentication.md#r-auth-023--token-storage-validation-and-caching) promised to reject a revoked token within 2 s while
`LISTEN/NOTIFY` delivered and within the cache TTL (15 s) otherwise. Since the change log
([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)), Valkey's state no longer affects the bound. What remains is a replica that cannot
poll, because PostgreSQL is unreachable from it or its poll has stalled; readiness removes such a
replica only after `cluster.epoch_max_staleness`, which is at least the PostgreSQL readiness window
of about 15 s.

**Decision.** The authentication caches answer only while the replica's last completed change-log
poll started less than `auth.revocation_bound` (2 s by default) ago, measured on the replica's own
monotonic clock. Otherwise the lookup goes to PostgreSQL, concurrent lookups of one credential share
one query, and a failed lookup answers `503`, never a cached acceptance. Sessions follow the same
gate ([Q-AUTH-10](spec/identity-and-authentication.md#q-auth-10--revocation-bound-when-a-replica-cannot-read-the-change-log), option (b)).

**Consequences.** The 2 s revocation bound holds in every failure mode: Valkey down, failed over,
flushed, partitioned from one replica or forging messages, and a replica cut off from PostgreSQL.
During a PostgreSQL switchover, requests that would have been cache hits ride out the retry loop
or get `503`; almost every request needs PostgreSQL for its item or package anyway. When a slow
poll trips the gate on every replica, the extra load grows with replicas times credentials in use,
not with the request rate. `cache_ttl` can rise to its 60 s ceiling without costing revocation
latency.

**Alternatives considered.** Serving cache hits until `cache_ttl`, the earlier fallback: the bound
depends on the failure mode. Relying on readiness: a gap of 15 s or more, and an edge that still
routes to the replica keeps getting answers. Revocation hints in Valkey: the bound would depend on
Valkey again, against INV-3.

**Spec:** [R-AUTH-010](spec/identity-and-authentication.md#r-auth-010--server-side-ui-sessions), [R-AUTH-023](spec/identity-and-authentication.md#r-auth-023--token-storage-validation-and-caching), [R-ARCH-018](spec/architecture.md#r-arch-018--in-process-caches-and-cross-replica-invalidation); decided:
[Q-AUTH-10](spec/identity-and-authentication.md#q-auth-10--revocation-bound-when-a-replica-cannot-read-the-change-log).

### ADR-115 · Brute-force counters count cluster-wide; the break-glass lockout and rotation stay in PostgreSQL

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** [R-AUTH-038](spec/identity-and-authentication.md#r-auth-038--brute-force-protection-and-rate-limiting) counted rejected credentials and flow starts per replica and accepted a
cluster-wide limit of the configured value times the replica count. It exempted credentials found
in the replica's own positive cache, so whether a colleague behind a throttled address got `429`
depended on the replica a request reached. A rotated break-glass Secret reaches each node on that
node's kubelet schedule, within about 2 minutes, so replicas hold the old and the new password side
by side, and a drain can remove any replica at any time.

**Decision.**
- **Throttles** ([Q-AUTH-11](spec/identity-and-authentication.md#q-auth-11--brute-force-counters-and-the-failed-credential-throttle-across-replicas), option (c)). The failed-credential throttle, the flow-start
  limit and the wrong-device-code limit count cluster-wide in Valkey through the limiter of
  [ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback), with per-replica shares while Valkey is unavailable. Credentials validated on any
  replica within `recent_token_window` (10 minutes) stay exempt through a Valkey marker keyed by a
  hash of the stored secret hash. A marker only lets a credential be validated; it never accepts
  one. The throttle is consulted on cache misses only.
- **Lockout.** The break-glass lockout stays in PostgreSQL, counted atomically and never cached.
- **Rotation** ([Q-AUTH-14](spec/identity-and-authentication.md#q-auth-14--break-glass-password-rotation-across-replicas), option (b)). The first replica that notices a changed break-glass
  password records `password_changed_at` in the lockout row and ends every earlier break-glass
  session in the same transaction. Every replica then refuses the local login with
  `503 break_glass_rotating` and `Retry-After` until `rotation_hold` (2 minutes) has passed.

**Consequences.** One budget and one answer on every replica, and legitimate traffic, which hits
the caches, pays no Valkey round trip. A failover that loses markers costs colleagues behind a
throttled address some `429`s, and only while an attack throttles it. The lockout survives any loss
of Valkey's data, and the end of old break-glass sessions no longer depends on the noticing replica
surviving. Break-glass login is unavailable for 2 minutes after a rotation. A kubelet slower than
the hold, or a rollout that replaces every pod before any of them notices, escapes the hold; the
identity page records that as [H-52](spec/identity-and-authentication.md#h-52--a-break-glass-rotation-that-no-running-replica-notices), with a manual end of the sessions as the fallback.

**Alternatives considered.** Per-replica counters, the earlier rule: a budget that multiplies and a
fresh throttle per replica. Cluster counters with the local cache as the only exemption: the
answer still depends on the replica. The lockout in Valkey: a failover resets the count against a
password a person chose. Every counter in PostgreSQL: the flood the throttle absorbs lands on the
system of record. For the rotation: each replica ending sessions when it notices, which a drain can
prevent; a verifier fingerprint in PostgreSQL, which puts a guessable verifier into the database or
ties it to the key ring's rotation; no automatic end, a step forgotten in the incident that needs
it.

**Spec:** [R-AUTH-014](spec/identity-and-authentication.md#r-auth-014--break-glass-administrator), [R-AUTH-036](spec/identity-and-authentication.md#r-auth-036--browser-login-handoff-for-other-command-line-tools), [R-AUTH-038](spec/identity-and-authentication.md#r-auth-038--brute-force-protection-and-rate-limiting), [R-AUTH-042](spec/identity-and-authentication.md#r-auth-042--authentication-across-replicas-and-roles), [R-ARCH-034](spec/architecture.md#r-arch-034--cluster-wide-rate-limit-counters),
[H-52](spec/identity-and-authentication.md#h-52--a-break-glass-rotation-that-no-running-replica-notices); decided: [Q-AUTH-11](spec/identity-and-authentication.md#q-auth-11--brute-force-counters-and-the-failed-credential-throttle-across-replicas), [Q-AUTH-14](spec/identity-and-authentication.md#q-auth-14--break-glass-password-rotation-across-replicas).

## Authorization

### ADR-051 · Actions are explicit, and a token scope only narrows

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Implied actions ("deploy implies read") make policies shorter but their effect harder to
read. Tokens should be able to carry less than their owner holds, never more.

**Decision.** No action implies another; presets expand to explicit action lists when a policy is
written. A token scope is a ceiling: an action is allowed only if the subject's grants allow it and
the scope admits it. Repository configuration is not a repository action; it needs a system role or
project administration.

A scope names exactly the resource types the grant model has: a closed set of typed parts,
`repositories` (MVP), `system` (P1) and `builds` (P1), validated by a fixed schema
([Q-AUTHZ-10](spec/authorization.md#q-authz-10--resource-types-a-token-scope-can-name), option (b)). Each element of the `repositories` part is a policy resource set:
`repositories`, `packages` and `paths` with `include` and `exclude`, plus `actions`
([Q-AUTHZ-9](spec/authorization.md#q-authz-9--shape-of-the-repository-part-of-a-token-scope), option (a)). A top-level `include` or `exclude` is refused with `400 invalid_scope`.

**Consequences.** What a policy grants is exactly what it lists. A scoped token loses rights when its
owner loses them, because grants are evaluated at use time. One matcher and one validator serve
policies, scopes and CI inline grants, so a pattern means the same wherever it appears. A
restricted scope without `builds` admits no build action, and project parts wait for project roles
(P2).

**Alternatives considered.** Hierarchical actions: shorter policies, less obvious results. One
`include`/`exclude` pair interpreted per format: ambiguous for a scope that spans formats. A
path-only string grammar: reintroduces the trap of a pattern that admits a tarball but not its
metadata. An open typed grammar with per-repository configuration types: no grant model stands
behind them, so such items would yield nothing or would have to grant on their own.

**Spec:** [R-AUTHZ-001](spec/authorization.md#r-authz-001--deny-by-default-allow-only-grants), [R-AUTHZ-006](spec/authorization.md#r-authz-006--repository-actions), [R-AUTHZ-013](spec/authorization.md#r-authz-013--token-scope-as-a-ceiling); decided: [Q-AUTHZ-9](spec/authorization.md#q-authz-9--shape-of-the-repository-part-of-a-token-scope),
[Q-AUTHZ-10](spec/authorization.md#q-authz-10--resource-types-a-token-scope-can-name).

### ADR-052 · Changing what clients install needs `deploy`, not `annotate`

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Moving an npm dist-tag, deprecating a version or relisting a NuGet version changes what
`npm install` or NuGet's version selection picks.

**Decision.** `deploy` covers creating new content and changing metadata that steers resolution:
npm dist-tags and deprecation, NuGet relist. `annotate` covers descriptive metadata that does not
change resolution, such as item properties. `overwrite` replaces existing bytes and is needed
together with `deploy`; formats may refuse overwrites entirely ([ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones)).

**Consequences.** A principal given `annotate` for property tooling cannot redirect installs.
Ownership narrows both further ([ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish)).

**Alternatives considered.** `annotate` for dist-tags and deprecation: the earlier npm mapping,
decided against ([Q-NPM-9](spec/formats/npm.md#q-npm-9--annotate-or-deploy-for-dist-tags-and-deprecation)).

**Spec:** [R-AUTHZ-006](spec/authorization.md#r-authz-006--repository-actions), [R-AUTHZ-007](spec/authorization.md#r-authz-007--action-map-for-npm-and-nuget-operations), [R-NPM-016](spec/formats/npm.md#r-npm-016--dist-tags-and-the-latest-rule), [R-NPM-018](spec/formats/npm.md#r-npm-018--deprecate-and-undeprecate); decided:
[Q-NPM-9](spec/formats/npm.md#q-npm-9--annotate-or-deploy-for-dist-tags-and-deprecation).

### ADR-053 · Virtual repositories: endpoint gate plus member rights

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** If `read` on a virtual repository granted read on its members, adding a private
repository to it would silently expose that repository.

**Decision.** Using a virtual repository requires `read` on it as an endpoint gate; content is
decided on the member that holds or receives it. Unreadable members are skipped without turning the
answer into 403. Writes are checked on the deploy target.

**Consequences.** Administrators decide which aggregated endpoints a group uses, at the cost of one
extra selector in policies. Membership changes cannot widen anyone's access.

**Alternatives considered.** Implied gate (anyone who reads one member may use the virtual
repository): one grant fewer. `read` on the virtual repository sufficient for members: simplest, and
exposes members silently. Both are weighed in [Q-AUTHZ-1](spec/authorization.md#q-authz-1--endpoint-gate-on-virtual-repositories), which chose the explicit gate.

**Spec:** [R-AUTHZ-009](spec/authorization.md#r-authz-009--virtual-repositories), [R-REPO-019](spec/repositories-and-proxy.md#r-repo-019--virtual-resolution-algorithm); decided: [Q-AUTHZ-1](spec/authorization.md#q-authz-1--endpoint-gate-on-virtual-repositories).

### ADR-054 · Cache fills are guarded by a configurable action, `read` by default

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Fetching an uncached package through a remote repository changes what the cache holds.
Some installations want a pull-through proxy anyone can use; others want a frozen mirror only a few
may extend.

**Decision.** Each remote repository names its cache-fill action. The default is `read`, so any
reader populates the cache; `deploy` turns the repository into a frozen mirror. Refreshing expired
metadata counts as populating, because a refreshed document advertises versions the caller could not
fetch otherwise. A caller who may read but not populate gets 403 `populate_not_permitted`.

**Consequences.** The common case behaves like a plain proxy, without inconsistent 404s between
users. [repositories-and-proxy.md](spec/repositories-and-proxy.md) and [formats/npm.md](spec/formats/npm.md) follow the `read` default, which [Q-AUTHZ-4](spec/authorization.md#q-authz-4--default-populate-action-of-remote-repositories) decided
(option (a)). Anonymous populating needs two explicit administrator decisions, the runtime switch
and a policy that grants `anonymous` read on the remote. The policy API names the consequence when
such a grant is saved, with the warning `anonymous_populates_remote`. A request authenticated by
the session cookie never passes the populate check and gets what the cache holds, so a link or a
README image cannot make an administrator's browser bring an attacker-chosen package into a frozen
mirror. Package clients authenticate with tokens and are not affected. Revisited on 2026-10-28,
choice unchanged: cross-replica collapsing merges concurrent fetches of the same path only, so the
rate of anonymous fetches of different packages is bounded by the fetch limits of [R-REPO-040](spec/repositories-and-proxy.md#r-repo-040--size-and-concurrency-limits),
whose upstream budget counts for the whole installation since [Q-REPO-8](spec/repositories-and-proxy.md#q-repo-8--upstream-fetch-limits-when-the-replica-count-changes); the risk still needs
the same two administrator decisions.

**Alternatives considered.** `deploy` as default: frozen-mirror semantics for every installation,
and the most common support case of such proxies, a package that installs for one developer and
not for a colleague. Populating only for authenticated callers unless a repository opts in:
inconsistent 404s exactly for the anonymous mirror case that anonymous read is granted for.

**Spec:** [R-AUTHZ-008](spec/authorization.md#r-authz-008--remote-repositories-who-may-populate-the-cache), [R-REPO-024](spec/repositories-and-proxy.md#r-repo-024--remote-repository-settings); decided: [Q-AUTHZ-4](spec/authorization.md#q-authz-4--default-populate-action-of-remote-repositories).

### ADR-055 · Refused reads answer 404, refused writes 403

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A 403 on a read confirms that a private package exists.

**Decision.** No credential: 401 with the endpoint family's challenge. Read without `read`: 404,
identical in body and headers to a real 404, with authorization running before the existence lookup.
Write without the needed action: 403 naming only the action and the coordinate sent. Ownership
refusal: 403. Management operations: capability check before any lookup, then 403. A stale grant
index answers 503. Listings and counts contain only readable items.

**Consequences.** Package names do not leak through status codes. NuGet clients send credentials only
after a 401, so anonymous requests are always challenged rather than answered with 404. Response
timing is not guaranteed to be existence-independent ([H-45](spec/api-and-ui.md#h-45--hiding-unauthorized-resources-is-not-timing-safe)).

**Alternatives considered.** 403 everywhere: clearer to users, leaks existence. 404 for refused
writes too: hides the reason from a caller who is entitled to know it lacks `deploy`.

**Spec:** [R-AUTHZ-016](spec/authorization.md#r-authz-016--denial-responses-and-hidden-existence), [R-AUTHZ-017](spec/authorization.md#r-authz-017--configurable-existence-hiding), [R-AUTH-002](spec/identity-and-authentication.md#r-auth-002--authentication-failures-and-challenges).

### ADR-056 · Package ownership is decided by the first publish

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** "A CI job may write the packages it produces" ([ADR-008](#adr-008--gitlab-ci-jobs-obtain-permissions-through-oidc)) must hold even when several
principals hold `deploy` on overlapping name patterns.

**Decision.** Local repositories of formats with package names carry `packageOwnership: enforce` or
`off`. New npm and NuGet repositories get `enforce` when the create request does not set the field
(decided, [Q-AUTHZ-2](spec/authorization.md#q-authz-2--ownership-enforcement-default-for-new-npm-and-nuget-local-repositories), option (a)). The first publish claims the name for the owner its grant
designates: a user, a group, a service account, a CI identity mapping, or a CI project. Later
changes are reserved to owners and holders of `manage`. Ownership is recorded per repository in the
MVP; shared ownership domains follow in P1.

CI grants (decided, [Q-AUTHZ-6](spec/authorization.md#q-authz-6--default-ownership-of-ci-inline-grants-that-write) option (b), [Q-CI-6](spec/gitlab-ci-oidc.md#q-ci-6--how-a-ci-project-acquires-package-ownership) option (d)):
- **Default `bind_project`.** A writing inline grant (`deploy`, `overwrite` or `delete`) that omits
  `ownership` is saved as `bind_project`, so the first publishing project owns the name. The
  default is applied and stored at save time, so reads and explanations show it. Any other inline
  grant is saved as `none`.
- **Sharing is explicit.** An explicit `none` on a writing grant shares names across the mapping's
  projects and is saved with the warning `shared_ownership`.
- **Existing names are not claimed by CI.** A CI publish never claims a name that already has
  versions but no owner; a `manage` holder adds the project as owner first.

**Consequences.** One project cannot publish versions of another project's package even when the
name slugs collide. A person or service account holding `deploy` on an overlapping pattern cannot
publish the next version of a CI project's package. A group-wide mapping does not silently make all
its projects co-owners. A grant to a single user makes that user the personal owner, and imported
names need one owner assignment before anyone without `manage` publishes them. Name prefixes cannot
be reserved by ownership alone ([H-21](spec/authorization.md#h-21--no-reservation-of-name-prefixes)); claims ([ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules)) decide resolution, ownership decides
publishing.

**Alternatives considered.** No ownership, only non-overlapping patterns: collisions go unnoticed.
Administrator pre-registration only: administration for every new package. `off` by default: only
`bind_project` grants are checked, so the owner constraint holds only against other CI jobs. A
record-only mode: a third mode that refuses nothing until someone switches. `none` as the CI
default: every project of a group-wide mapping co-owns everything any of them publishes. No CI
default at all: friction on every mapping although the safe value is obvious.

**Spec:** [R-AUTHZ-014](spec/authorization.md#r-authz-014--ci-tokens-in-evaluation), [R-AUTHZ-018](spec/authorization.md#r-authz-018--package-ownership), [R-AUTHZ-019](spec/authorization.md#r-authz-019--package-ownership-api), [R-AUTHZ-020](spec/authorization.md#r-authz-020--shared-ownership-domains), [R-CI-013](spec/gitlab-ci-oidc.md#r-ci-013--grant-specification),
[R-CI-015](spec/gitlab-ci-oidc.md#r-ci-015--package-ownership-binding); decided: [Q-AUTHZ-2](spec/authorization.md#q-authz-2--ownership-enforcement-default-for-new-npm-and-nuget-local-repositories), [Q-AUTHZ-6](spec/authorization.md#q-authz-6--default-ownership-of-ci-inline-grants-that-write), [Q-CI-6](spec/gitlab-ci-oidc.md#q-ci-6--how-a-ci-project-acquires-package-ownership).

### ADR-057 · A compiled grant index per replica, consistent through an epoch, failing closed

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Authorization runs on every request and must not query policy tables each time, yet a
removed grant must stop working everywhere. Revisited on 2026-10-28 after the owner's
constraints: until then the index kept its own `authz_epoch` row, sent `NOTIFY` in the writing
transaction, held a `LISTEN` connection on every replica, polled every 5 s and made a replica
unconfirmed for 30 s answer `503` and report not ready. The change log replaced `LISTEN/NOTIFY`
([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)), Valkey must hold no positive authorization result ([ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)), and requests now reach
any of n replicas at random, so a client cannot watch the replica that answers next.

**Decision.** Each replica evaluates against an in-memory index compiled from policies, role
bindings, repositories, projects and settings:
- **Change areas, no own epoch** ([Q-AUTHZ-11](spec/authorization.md#q-authz-11--invalidation-path-of-the-grant-index-across-n-replicas), option (c)). The index follows the `authz` and
  `repo_config` change areas of the change log. It has no epoch table of its own and uses no
  `NOTIFY`.
- **One consistent snapshot.** The index is built from one `REPEATABLE READ READ ONLY` snapshot
  on the request pool and labelled with the epochs and the `changed_at` of both areas. An index
  with lower epochs is never swapped in, and an epoch reused after a failover that lost commits
  shows as a different `changed_at` and triggers a rebuild.
- **Valkey only starts a rebuild.** A Valkey message only starts a rebuild that an epoch query
  confirms first, so a forged message costs one indexed query. Valkey holds nothing of
  authorization.
- **Fail closed without a gate of its own** ([Q-AUTHZ-13](spec/authorization.md#q-authz-13--a-replica-whose-grant-index-cannot-be-confirmed-current), option (c)). No decision is made on an
  index unconfirmed for longer than `authz.cache.max_staleness` (10 s). A request that meets such
  an index waits as it would for the database, within its deadline and
  `database.retry.max_elapsed`, and is then decided or answered `503 authz_stale`. Authorization
  adds no readiness check of its own; `database` and `invalidation` cover PostgreSQL outages. The
  poll and the rebuilds keep running through a drain until the last request has finished.
- **Read-your-writes** ([Q-AUTHZ-12](spec/authorization.md#q-authz-12--seeing-ones-own-permission-change-under-random-request-distribution), option (b)). A grant write answers `Trove-Authz-Epoch`, and a
  request may carry `Trove-Authz-Min-Epoch` ([R-AUTHZ-032](spec/authorization.md#r-authz-032--seeing-ones-own-permission-change-on-any-replica)). After authentication succeeded, any
  replica waits for an index at least that new, or answers `412` when the database never reached
  that epoch; a `412` makes the client drop the epoch.

Group memberships, tokens, CI mappings and ownership rows are not in the index; their owners cache
or read them.

**Consequences.** A policy change applies on every replica within milliseconds with Valkey and
within the change-log poll interval without it. The worst window for a removed grant on a replica
cut off from the change log drops from about 30 s to about 10.5 s. The UI and automation see their
own grant changes, added or removed, on whichever replica answers next; package clients rely on the
bound. Every change makes every replica rebuild the whole index: with 100 replicas and a 500 ms
build, about 50 CPU seconds across the cluster per change, small at administrative change rates.
`Trove-Authz-Epoch` covers permission-side changes only, not repository creation, which the
unknown-key lookup of [ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18) covers. A build that fails on one replica while its poll works is
not seen by readiness; it shows in `trove_authz_index_staleness_seconds` and in `authz_stale`
answers.

**Alternatives considered.** Querying policies per request: database load proportional to traffic,
and the 1 ms target fails. Caching without an epoch: no bound on staleness. From
[Q-AUTHZ-11](spec/authorization.md#q-authz-11--invalidation-path-of-the-grant-index-across-n-replicas): keeping the own epoch with `LISTEN/NOTIFY`, the earlier design; an own epoch row
with a second poll, the same guarantee built twice; sharing the compiled index or decisions in
Valkey, which would let a Valkey writer grant access. From [Q-AUTHZ-12](spec/authorization.md#q-authz-12--seeing-ones-own-permission-change-under-random-request-distribution): the bound only; re-checking
every refusal against the database epoch, which misses removals; session affinity, forbidden. From
[Q-AUTHZ-13](spec/authorization.md#q-authz-13--a-replica-whose-grant-index-cannot-be-confirmed-current): the earlier 30 s with its own readiness gate; 10 s with an immediate `503`, which fails
clients with tightly spaced retries during a failover.

**Spec:** [R-AUTHZ-015](spec/authorization.md#r-authz-015--evaluation-algorithm), [R-AUTHZ-016](spec/authorization.md#r-authz-016--denial-responses-and-hidden-existence), [R-AUTHZ-028](spec/authorization.md#r-authz-028--compiled-grant-index-and-invalidation-across-replicas), [R-AUTHZ-032](spec/authorization.md#r-authz-032--seeing-ones-own-permission-change-on-any-replica),
[R-ARCH-018](spec/architecture.md#r-arch-018--in-process-caches-and-cross-replica-invalidation); decided: [Q-AUTHZ-11](spec/authorization.md#q-authz-11--invalidation-path-of-the-grant-index-across-n-replicas), [Q-AUTHZ-12](spec/authorization.md#q-authz-12--seeing-ones-own-permission-change-under-random-request-distribution), [Q-AUTHZ-13](spec/authorization.md#q-authz-13--a-replica-whose-grant-index-cannot-be-confirmed-current).

### ADR-058 · Projects for delegated administration, reduced, in P1

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Larger installations want teams to administer their own repositories and policies
without system administrator rights.

**Decision.** P1 introduces projects in a reduced form: a project owns repositories, has project
administrators and owns policies that apply only to its repositories. Project roles, environments and
sharing between projects stay P2.

**Consequences.** Delegation without a full multi-tenant model. CI delegations stay independent of
projects in P1 ([Q-AUTHZ-7](spec/authorization.md#q-authz-7--ci-delegations-and-p1-projects)).

**Alternatives considered.** Full projects with roles and environments in P1: a large model before
there is usage to shape it. No delegation: every change goes through system administrators.

**Spec:** [R-AUTHZ-025](spec/authorization.md#r-authz-025--delegated-policy-management), [R-AUTHZ-026](spec/authorization.md#r-authz-026--projects-for-delegated-administration), [R-API-047](spec/api-and-ui.md#r-api-047--projects); decided: [Q-AUTHZ-7](spec/authorization.md#q-authz-7--ci-delegations-and-p1-projects).

## GitLab CI OIDC

### ADR-059 · CI tokens through a standard token-exchange endpoint

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A job needs to turn its GitLab ID token into a trove token with standard tooling.

**Decision.** `POST /api/v1/auth/oidc/token` implements OAuth 2.0 Token Exchange (RFC 8693): the ID
token is the subject token, the response carries `access_token` (a `trv_cit_` token),
`expires_in` and `scope`. The issuance (`jti` record, token hash, issuance record, audit event) is
one transaction. Rejected subject tokens answer 400 `invalid_request` with a machine-readable
`reason` and `requestId`, in the OAuth error body of RFC 6749, not as problem details
([Q-CI-8](spec/gitlab-ci-oidc.md#q-ci-8--status-code-for-rejected-subject-tokens), option (a)). An optional `scope` parameter narrows the result. It is an RFC 6749
scope string whose items, `<action>` or `<action>:<selector>` with any repository selector of
R-AUTHZ-004, are a notation for the repository part of the R-AUTH-024 token scope. Each item
becomes one repository grant of that structure, which is what the token stores, and the response's
`scope` uses the same notation ([Q-CI-2](spec/gitlab-ci-oidc.md#q-ci-2--grammar-of-the-scope-request-parameter), option (c)).

**Consequences.** Standard OAuth libraries work against the endpoint. The same code serves the npm
trusted-publishing entry point ([ADR-071](#adr-071--npm-trusted-publishing-through-package-bound-ci-tokens-p1)). One scope model and one selector syntax serve every
token, so `read:@any-local:npm` means the same as in a policy. A pipeline can give its install step,
which runs third-party install scripts, a token without `deploy`. Package and path narrowing is not
available in the request; the mapping's grants carry it. Exchange rate limits count cluster-wide,
per client address with exempt networks and per CI subject after validation, without a per-issuer
bucket ([Q-CI-14](spec/gitlab-ci-oidc.md#q-ci-14--exchange-rate-limits-across-replicas), option (c); [ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback)).

**Alternatives considered.** A custom endpoint and response shape: every client hand-written. 401
for rejected subject tokens: not what RFC 8693 clients expect
([Q-CI-8](spec/gitlab-ci-oidc.md#q-ci-8--status-code-for-rejected-subject-tokens)). For the scope parameter ([Q-CI-2](spec/gitlab-ci-oidc.md#q-ci-2--grammar-of-the-scope-request-parameter)):
- An own string grammar: a second, weaker scope model next to R-AUTH-024.
- The JSON scope in the request: not a valid OAuth scope, so standard clients cannot send it.
- No `scope` parameter: every step of a job would hold the job's full grants.

**Spec:** [R-CI-007](spec/gitlab-ci-oidc.md#r-ci-007--token-exchange-endpoint), [R-CI-008](spec/gitlab-ci-oidc.md#r-ci-008--exchange-errors-and-diagnostics), [R-CI-021](spec/gitlab-ci-oidc.md#r-ci-021--exchange-endpoint-hardening); decided: [Q-CI-2](spec/gitlab-ci-oidc.md#q-ci-2--grammar-of-the-scope-request-parameter), [Q-CI-8](spec/gitlab-ci-oidc.md#q-ci-8--status-code-for-rejected-subject-tokens), [Q-CI-14](spec/gitlab-ci-oidc.md#q-ci-14--exchange-rate-limits-across-replicas).

### ADR-060 · Ordered identity mappings: first match per tier, union across tiers

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Mappings turn verified claims into grants. Administrators need one predictable answer
per job, and narrow rules ("merge request pipelines get less than release pipelines") without
negations everywhere.

**Decision.** A mapping belongs to one CI issuer and has a priority, a claim condition, grants, an
optional maximum TTL and options. Within a tier, mappings are evaluated in ascending priority and the
first true condition is the tier's match; the token's grants are the union of the tier matches. The
MVP has one tier (administrator mappings); P1 adds one per delegation. No match fails the exchange.
A grant that fails to render fails the exchange; evaluation never falls through to the next
mapping ([Q-CI-1](spec/gitlab-ci-oidc.md#q-ci-1--first-match-per-tier-or-union-of-all-matching-mappings), option (a)). Each mapping is a principal named `<CI issuer name>/<mapping name>`,
for example `gitlab-com/acme-release-publish`, referenced as `ci:<issuer>/<mapping>` in policies
([Q-CI-10](spec/gitlab-ci-oidc.md#q-ci-10--principal-name-of-an-identity-mapping), option (a)). Both parts are immutable, so the principal is never renamed, and deleting
the mapping deletes its principal.

**Consequences.** Every exchange is explained by at most one mapping per tier. Delegated mappings
cannot shadow system mappings or the reverse. A misordered rule gives fewer grants, never more. A
grant every pipeline needs is repeated in each mapping a job can match first. Principal names stay
unique per kind without a global namespace for mapping names, and audit events stay readable. A
replica whose mapping cache lags behind PostgreSQL never evaluates the old mappings: the issuer row
carries `mappings_version`, every mapping write increments it, and an exchange whose cache is
behind evaluates again from PostgreSQL; when the issuer row's own `version` changed, the exchange
also re-validates the subject token against the re-read row ([Q-CI-15](spec/gitlab-ci-oidc.md#q-ci-15--exchange-against-a-mapping-cache-that-lags-behind-postgresql), option (b), 2026-10-28).

**Alternatives considered.** Union of all matching mappings: simpler to explain, but narrowing needs
negations. Strict first match across all mappings: delegations fight over priority numbers
([Q-CI-1](spec/gitlab-ci-oidc.md#q-ci-1--first-match-per-tier-or-union-of-all-matching-mappings)). First match with an opt-in fall-through: order-dependent unions return. For the
principal name ([Q-CI-10](spec/gitlab-ci-oidc.md#q-ci-10--principal-name-of-an-identity-mapping)): mapping names unique across all issuers, which forces coordination
between GitLab instances, or the mapping's UUID, which leaves opaque IDs in audit and support work.

**Spec:** [R-CI-009](spec/gitlab-ci-oidc.md#r-ci-009--identity-mapping-model-and-evaluation-order), [R-CI-010](spec/gitlab-ci-oidc.md#r-ci-010--claim-conditions-matchers-and-combinators), [R-CI-011](spec/gitlab-ci-oidc.md#r-ci-011--protected-refs-environments-and-pipeline-sources), [R-CI-023](spec/gitlab-ci-oidc.md#r-ci-023--delegated-mapping-management); decided: [Q-CI-1](spec/gitlab-ci-oidc.md#q-ci-1--first-match-per-tier-or-union-of-all-matching-mappings),
[Q-CI-10](spec/gitlab-ci-oidc.md#q-ci-10--principal-name-of-an-identity-mapping), [Q-CI-15](spec/gitlab-ci-oidc.md#q-ci-15--exchange-against-a-mapping-cache-that-lags-behind-postgresql).

### ADR-061 · A small claim-template language with escaping

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** One mapping should serve many projects, for example "each project may publish
`@<its namespace>/*`", without letting a crafted claim value widen the grant.

**Decision.** Templates may appear in repository, package and path patterns of inline grants. The
language has claim references and a fixed filter set (`lower`, `slug`, `replace`, `trim_prefix`,
`trim_suffix`, `segment`, `require` with an RE2 full match) and no conditionals, loops or
user-defined functions. Rendered claim values are escaped, so pattern characters in a claim are
literal.

**Consequences.** Templates are reviewable and cannot loop. A claim such as a project path containing
`*` cannot turn an exact grant into a wildcard.

**Alternatives considered.** A general template engine: conditionals and loops in authorization
rules. No templating: one mapping per project.

**Spec:** [R-CI-013](spec/gitlab-ci-oidc.md#r-ci-013--grant-specification), [R-CI-014](spec/gitlab-ci-oidc.md#r-ci-014--claim-templating-and-escaping), [R-CI-024](spec/gitlab-ci-oidc.md#r-ci-024--grants-derived-from-the-jobs-subject).

### ADR-062 · Fork pipelines are denied by default

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A merge request pipeline from a fork can run in the parent project, with the fork's CI
configuration and the parent's runners. Its code can exfiltrate anything the token can read.

**Decision.** Before mapping evaluation, a subject token is cross-project when two of `project_id`,
`job_project_id` and `source_project_id` are present and differ. Such a token is denied by default,
and each matched mapping decides for itself ([Q-CI-9](spec/gitlab-ci-oidc.md#q-ci-9--default-for-cross-project-fork-pipelines), option (d)):
- **Opt-in per mapping.** Only a matched mapping that opts into `read_only` contributes, with its
  own grants capped to `read`.
- **Others contribute nothing.** A mapping without the opt-in contributes nothing and is not
  recorded as one of the token's mappings, so policies naming it cannot reach the token.
- **No issuer-wide switch.** If no matched mapping contributes, the exchange fails.

**Consequences.** Fork contributions cannot reach private packages through CI unless a group opts in
deliberately, on the very mapping whose grants it exposes. An opt-in never reaches another mapping
or one created later, delegated mappings (P1) included. GitLab requires a parent-project member to
start such a pipeline, but that member may not review the fork's CI configuration. A group that
wants the opt-in on many mappings sets it on each.

**Alternatives considered.** `read_only` by default: private packages readable by fork code
([Q-CI-9](spec/gitlab-ci-oidc.md#q-ci-9--default-for-cross-project-fork-pipelines)). An issuer-wide switch, the earlier draft: it silently extends to every mapping
created later, and one opted-in mapping released the capped grants of every matched mapping.
Always deny, without any opt-in: removes a deliberate choice for groups whose packages are not
confidential.

**Spec:** [R-CI-001](spec/gitlab-ci-oidc.md#r-ci-001--ci-issuer-configuration), [R-CI-007](spec/gitlab-ci-oidc.md#r-ci-007--token-exchange-endpoint), [R-CI-012](spec/gitlab-ci-oidc.md#r-ci-012--fork-and-merge-request-pipelines); decided: [Q-CI-9](spec/gitlab-ci-oidc.md#q-ci-9--default-for-cross-project-fork-pipelines).

### ADR-063 · A CI token's lifetime is bounded by its job

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A CI token should not outlive the job that obtained it by more than necessary.

**Decision.** The lifetime is the minimum of the requested TTL (default 15 min), the mapping's and
the issuer's maxima, the installation's CI maximum (1 h proposed) and the remaining lifetime of the
ID token, whose expiry GitLab sets to the job timeout. One ceiling (6 h) governs every CI token, and
trove refuses to start with a larger configured value. Long jobs exchange again.

The TTL bound is the guarantee. In addition, jobs revoke their own token through
`DELETE /api/v1/tokens/current` as the last command of `script`; the reference pipeline and the
generated snippets include that step ([Q-CI-7](spec/gitlab-ci-oidc.md#q-ci-7--revoke-ci-tokens-when-the-gitlab-job-ends), option (c)).

**Consequences.** Jobs revoke their own token at the end of `script`, so a successful job, the
common case, ends with no live token, using an endpoint the MVP has anyway. The TTL bounds failed,
cancelled and timed-out jobs, whose last line does not run; their token stays valid for its
remaining lifetime ([H-26](spec/gitlab-ci-oidc.md#h-26--ci-tokens-outlive-the-job)). The revocation belongs in `script`, because a variable exported
there is not visible in `after_script`, which runs in a separate shell.

**Alternatives considered.** The TTL bound alone: every token outlives its job by up to its
remaining lifetime. Revoking through GitLab job webhooks: needs a webhook secret per project or
group and an inbound endpoint, for a window of minutes. A job-scoped revocation with the ID token
from `after_script`: covers failed and cancelled jobs, but needs a new unauthenticated mutating
endpoint and still misses timeouts. It becomes a P1 requirement if audit shows tokens of failed or
cancelled jobs in use after their job ended.

**Spec:** [R-CI-013](spec/gitlab-ci-oidc.md#r-ci-013--grant-specification), [R-CI-016](spec/gitlab-ci-oidc.md#r-ci-016--ci-token-lifetime), [R-CI-017](spec/gitlab-ci-oidc.md#r-ci-017--ci-token-identity-presentation-and-revocation), [R-AUTH-026](spec/identity-and-authentication.md#r-auth-026--token-lifetime-policy); decided: [Q-CI-7](spec/gitlab-ci-oidc.md#q-ci-7--revoke-ci-tokens-when-the-gitlab-job-ends).

### ADR-064 · Subject-token reuse is recorded; single use depends on the issuer kind

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A GitLab job receives its ID tokens once, as variables, and legitimately exchanges the
same token several times (per tool step, after network errors, after the first CI token expired).
Other CI systems hand out a fresh ID token on demand. Replays of a stolen ID token should be visible
everywhere and blocked where no legitimate client needs a repeated exchange.

**Decision.** Every exchanged subject token is recorded by issuer and `jti` with an exchange count,
in the exchange transaction. The single-use default follows how a job obtains its subject token
([Q-CI-4](spec/gitlab-ci-oidc.md#q-ci-4--single-exchange-per-jti-by-default), option (d)), and each CI issuer can override it:
- **GitLab issuers record and count.** Single use is off, and beyond a threshold repeated exchanges
  create audit events.
- **GitHub and generic issuers default to single use.** A second exchange of the same `jti` fails,
  and tokens without `jti` are rejected. A GitHub step requests a fresh ID token whenever it needs
  one. For a generic issuer whose audience is shared, single use is the only replay control left.

Records are deleted after the subject token's expiry. The replay set lives only in PostgreSQL;
after a restore, single-use issuers refuse subject tokens issued before it finished, with the clock
leeway on the refusing side: a token whose `iat` is not later than `restored_at` plus the clock
skew is refused, read in the exchange transaction ([Q-CI-13](spec/gitlab-ci-oidc.md#q-ci-13--where-the-replay-set-lives), option (e), 2026-10-28). A restore
returns the replay set to the backup's snapshot, so without the cut-off a token used between the
snapshot and the loss could be exchanged once more.

**Consequences.** On GitLab, replays are counted and audited rather than blocked, relying on the
exclusive audience ([ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10)) and short ID-token lifetimes; a long GitLab job renews its CI token by
exchanging the same ID token again. A GitHub workflow that reuses one ID token across exchanges
fails. Not verified, and recorded on the question's page: that every token request in one GitHub
job yields a new `jti`.

**Alternatives considered.** Single use for every issuer: breaks GitLab jobs that exchange more than
once. Off for every kind: for shared-audience issuers, every other relying party could replay the
token and only an audit event would record it. Off for GitLab and GitHub, on only for generic, the
earlier choice: leaves GitHub without single use although nothing there needs repeated exchanges.

**Spec:** [R-CI-001](spec/gitlab-ci-oidc.md#r-ci-001--ci-issuer-configuration), [R-CI-016](spec/gitlab-ci-oidc.md#r-ci-016--ci-token-lifetime), [R-CI-018](spec/gitlab-ci-oidc.md#r-ci-018--replay-protection), [R-CI-020](spec/gitlab-ci-oidc.md#r-ci-020--issuance-records-audit-and-metrics), [R-CI-025](spec/gitlab-ci-oidc.md#r-ci-025--github-actions-as-a-ci-issuer); decided:
[Q-CI-4](spec/gitlab-ci-oidc.md#q-ci-4--single-exchange-per-jti-by-default), [Q-CI-13](spec/gitlab-ci-oidc.md#q-ci-13--where-the-replay-set-lives).

### ADR-065 · Further CI issuers through the same mechanism (P1)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** GitHub Actions and other CI systems issue OIDC ID tokens too.

**Decision.** GitHub Actions and generic OIDC issuers are CI issuer kinds that reuse validation,
mappings, templating, lifetime and audit. Presets pin immutable claims (for GitHub
`repository_id` and `repository_owner_id`, which survive renames) and list required claims. GitLab
group hierarchies cannot be pinned by immutable ID ([H-27](spec/gitlab-ci-oidc.md#h-27--group-hierarchies-cannot-be-pinned-by-immutable-id)).

**Consequences.** One trust path for every CI system; a new issuer is configuration plus a preset.

**Alternatives considered.** A separate integration per CI system: duplicated security-critical code.

**Spec:** [R-CI-005](spec/gitlab-ci-oidc.md#r-ci-005--several-gitlab-instances-and-public-issuers), [R-CI-025](spec/gitlab-ci-oidc.md#r-ci-025--github-actions-as-a-ci-issuer), [R-CI-026](spec/gitlab-ci-oidc.md#r-ci-026--generic-oidc-issuers).

## npm

### ADR-066 · npm package documents are rendered from rows, not stored

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** An npm package document aggregates every version of a package. Storing it as a blob
means rewriting a growing document on every publish, tag change or deprecation.

**Decision.** A published version is a package row, a version row and one item, the tarball. npm
state (dist-tags, revision counter, per-version manifests) lives in npm tables, and package
documents are rendered from them and cached. Every change increments the package's `rev_seq`.

**Consequences.** Publishes and tag changes are small transactional writes. Document caches validate
against `rev_seq` ([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)) and live in each replica's memory ([ADR-116](#adr-116--npm-documents-are-cached-per-replica-under-keys-read-from-postgresql)). npm files live only at their canonical paths; there is no layout
setting.

**Alternatives considered.** Storing the document and patching it: write amplification and a race
between concurrent changes to one package.

**Spec:** [R-NPM-006](spec/formats/npm.md#r-npm-006--storage-model), [R-NPM-009](spec/formats/npm.md#r-npm-009--document-revisions-and-read-for-write), [R-NPM-010](spec/formats/npm.md#r-npm-010--full-package-document), [R-NPM-013](spec/formats/npm.md#r-npm-013--conditional-requests-and-cache-headers).

### ADR-067 · npm and NuGet versions are immutable, guarded by tombstones

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** npm lockfiles pin `integrity` per version. NuGet clients cache by ID and version in
the global packages folder, and lock files record content hashes. A version republished with other
bytes breaks installs or, worse, installs different code. NuGet's implementation guidance forbids
modifying packages for the same reason.

**Decision.** A name and version once published to a local repository is never accepted again with
other bytes in that repository. That holds for a second publish or push, after an unpublish, hard
delete, raw delete, cleanup or move out, for a raw upload, for a copy or move into the repository
(P1, refused per item), and for a principal holding `overwrite`. The guard is a tombstone:
- **What writes one.** Every removal of a version from a local repository writes a tombstone with
  the version's digest: unpublish on npm; hard delete, raw delete and cleanup deletion on NuGet; a
  move out of the repository on both, written in the move transaction (P1). Clients of the source
  repository may have cached the version, so a rebuilt version under the same number there would
  diverge exactly as after a delete. A copy writes none, because the version stays.
- **What survives.** The tombstone survives the trash can and its permanent removal.
- **No purge.** No API, UI or `trove admin` operation removes a tombstone, and a republish of
  identical bytes against a tombstone is refused like any other ([Q-NPM-8](spec/formats/npm.md#q-npm-8--purging-tombstones), option (a);
  [Q-NUGET-6](spec/formats/nuget.md#q-nuget-6--overwriting-an-existing-version), option (d)).
- **Identical retries of a stored version.** A NuGet protocol push of the identical bytes of a
  version that is still stored answers 201 and changes nothing, so a push whose answer was lost
  stays safe to repeat ([Q-NUGET-6](spec/formats/nuget.md#q-nuget-6--overwriting-an-existing-version), revisited on 2026-10-28: option (d) instead of (c)). On npm, a
  retry by the same principal with the same bytes and dist-tag within the retry window is
  acknowledged the same way ([ADR-117](#adr-117--an-identical-npm-publish-retried-after-a-lost-answer-is-acknowledged)).
- **What removes one.** Only restoring the original bytes from the trash can, which is not a reuse,
  deleting the whole repository, and on npm an administrator's reinstate of a version a restore
  withheld, which brings back the bytes the tombstone names ([ADR-118](#adr-118--a-restore-withholds-npm-versions-unpublished-after-the-snapshot)).

On NuGet a repeated push of other bytes answers 409 `nuget_version_exists`, a push of a tombstoned
version 409 `nuget_version_deleted`, and a push of other bytes to a number a restore reserved
`nuget_version_reserved` ([ADR-122](#adr-122--a-restore-replays-nuget-publishes-and-deletes-into-tombstones)). A NuGet protocol `DELETE` unlists by default, so a tombstone
results only from a hard delete ([ADR-090](#adr-090--a-nuget-protocol-delete-unlists-by-default)).

Restores, revisited on 2026-10-28 ([Q-NPM-8](spec/formats/npm.md#q-npm-8--purging-tombstones), choice unchanged): a restore returns `npm_tombstones` to
the backup's snapshot and would remove every tombstone written since, which is the purge this entry
rules out. Tombstones survive a restore through [Q-NPM-12](spec/formats/npm.md#q-npm-12--unpublishes-made-after-a-restored-backups-snapshot)'s withholding: the journal replays each
unpublish as a tombstone and deletes nothing. A reinstate is not a purge, because it admits only the
bytes the tombstone names. What a restore still reverts on npm is the npm page's [H-51](spec/formats/npm.md#h-51--a-restore-reverts-npm-metadata-changes-made-after-the-backups-snapshot); [H-50](spec/formats/nuget.md#h-50--a-restore-revives-nuget-versions-deleted-after-the-backups-snapshot-and-can-free-lost-version-numbers)
is NuGet's residual.

**Consequences.** `overwrite` has no effect on npm or NuGet repositories (R-AUTHZ-006 lets formats
refuse regardless). A fix ships as a new version; development feeds use pre-release counters. A
version deleted by mistake cannot be pushed again once the trash can was purged (in the MVP, which
has no trash can, at once). A rerun of an old release pipeline cannot silently re-release a version
someone withdrew on purpose. Tombstone rows accumulate with every cleanup deletion, one small row
per version.

**Alternatives considered.** Allowing reuse after unpublish or hard delete: reopens the
lockfile-integrity hazard in two steps for anyone with `deploy` and `delete`. An administrator purge:
makes the guarantee depend on a role. Accepting a republish of identical bytes: integrity holds,
but a withdrawal no longer sticks against automation that republishes. An opt-in overwrite for
scratch feeds: replaced packages diverge between machines. A system-administrator-only, audited
purge for both formats at once is the addition if an import must reuse deleted version numbers.

**Spec:** [R-NPM-008](spec/formats/npm.md#r-npm-008--version-immutability-and-tombstones), [R-NPM-017](spec/formats/npm.md#r-npm-017--unpublish), [R-NUGET-006](spec/formats/nuget.md#r-nuget-006--push-through-the-package-publish-resource), [R-NUGET-007](spec/formats/nuget.md#r-nuget-007--unlist-relist-and-delete), [R-NUGET-032](spec/formats/nuget.md#r-nuget-032--pushes-and-deletes-across-drains-restarts-and-client-retries), [R-STOR-019](spec/storage.md#r-stor-019--copy-and-move); decided:
[Q-NPM-8](spec/formats/npm.md#q-npm-8--purging-tombstones), [Q-NUGET-6](spec/formats/nuget.md#q-nuget-6--overwriting-an-existing-version), [Q-NPM-12](spec/formats/npm.md#q-npm-12--unpublishes-made-after-a-restored-backups-snapshot), [Q-NUGET-10](spec/formats/nuget.md#q-nuget-10--versions-changed-after-a-backups-snapshot).

### ADR-068 · npm remotes fetch only the full document

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** npm clients request either the abbreviated or the full package document. Release-age
and trust checks need fields (`time`, `_npmUser`) that only the full form carries.

**Decision.** A remote repository fetches and stores only the full document and derives the
abbreviated one. A received document must be valid JSON within the size limit, name the requested
package, have consistent version keys and absolute tarball URLs and valid SRI integrity values;
otherwise the previous entry stays and the request answers 502. Facts needed for tarball serving are
recorded per version, so tarball requests never parse a large document.

**Consequences.** One stored representation, one view of which versions exist. Cold-cache bandwidth
is higher for large documents, reduced by gzip and conditional requests: measured on 2026-10-28
with gzip, about 2.4 MB full against 2.0 MB abbreviated for the largest documents checked.
Revisited on 2026-10-28 ([Q-NPM-5](spec/formats/npm.md#q-npm-5--upstream-representation-for-remote-repositories), choice unchanged, with two more arguments): both request
variants collapse into the one `full` entry, so a package has one flight across replicas; the npm
side tables stay consistent with the stored document, because a duplicate fill under a lost Valkey
lock cannot split them from it; and the stale-copy rule is applied inside the fill transaction.
Under two entries, consecutive requests of one client, which reach different replicas, could see
two views refreshed at different times.

**Alternatives considered.** Fetching both forms separately: two refresh cycles that can disagree,
and the upstream's abbreviated form carries no `time` and no `_npmUser`, so release-age and trust
checks need the full one anyway ([Q-NPM-5](spec/formats/npm.md#q-npm-5--upstream-representation-for-remote-repositories)).

**Spec:** [R-NPM-024](spec/formats/npm.md#r-npm-024--package-document-fetch-and-validation), [R-NPM-025](spec/formats/npm.md#r-npm-025--tarball-urls-and-tarball-fetching); decided: [Q-NPM-5](spec/formats/npm.md#q-npm-5--upstream-representation-for-remote-repositories).

### ADR-069 · Versions removed upstream are hidden by default

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Most upstream removals are takedowns of malicious packages. A cache that keeps serving
them becomes a distribution point.

**Decision.** When the upstream document no longer lists a version, an npm remote hides that version
and its tarball, also for lockfile requests by exact tarball URL, unless `serveRemovedVersions` is
enabled ([Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed), option (a)). A version the upstream lists again is shown again. A whole package
removed upstream answers 404 for its documents and its cached tarballs (generic rule of
[ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving), [Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document)).

Stale-copy rule: an upstream document whose `time.modified` is older than the stored one never
replaces the stored document and never marks versions removed. A lagging CDN edge serves such
copies. The stored `time.modified` is capped at its receive time, so one future-dated document
cannot freeze the cache and with it every later takedown. The rule is skipped when a document has
no `time.modified`.

**Consequences.** Builds pinned to a legitimately unpublished version break visibly; the fix is the
per-repository switch or publishing the version to a local repository. Earlier downloads are not
recalled ([H-33](spec/formats/npm.md#h-33--takedown-delay-and-earlier-downloads)). The stale-copy rule keeps a lagging upstream from producing false removals; the
metric `trove_npm_stale_upstream_documents_total` counts such copies. Revisited on 2026-10-28
([Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed), choice unchanged): hiding still reaches lockfile installs on n replicas and across a
restore. Every request reads the marks from PostgreSQL, the gone mark is the core's single
`upstream_gone_at`, the stale-copy cap uses PostgreSQL's clock, and a cached tarball waits for a
current document after the metadata period or a restore ([ADR-119](#adr-119--cached-npm-tarballs-wait-for-a-current-package-document)).

**Alternatives considered.** Serving everything once cached: maximum availability, also for malware
([Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed)). Hiding removed versions from documents while still serving cached tarballs:
`npm ci` fetches tarballs by URL without reading documents, so every project that already pinned a
malicious version keeps installing it.

**Spec:** [R-NPM-024](spec/formats/npm.md#r-npm-024--package-document-fetch-and-validation), [R-NPM-026](spec/formats/npm.md#r-npm-026--versions-removed-upstream), [R-REPO-029](spec/repositories-and-proxy.md#r-repo-029--upstream-failure-handling-and-the-assumed-offline-circuit); decided: [Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed), [Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document).

### ADR-070 · Merged npm documents take each version from the member that would serve it

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A virtual repository's package document combines members; a version's metadata and its
tarball must not come from different places.

**Decision.** Versions are the union over members that know the package. Each version's manifest,
`dist`, deprecation and publisher come from the member that tier resolution would pick for its
tarball, and `dist.tarball` points at the virtual repository. Dist-tags pointing at versions absent
from the merge are dropped. Top-level descriptive fields come from the member that holds the version
`latest` points to.

**Consequences.** The integrity a client reads matches the bytes it downloads.

**Alternatives considered.** Top-down merge by member order per field: can pair one member's
integrity with another member's tarball.

**Spec:** [R-NPM-030](spec/formats/npm.md#r-npm-030--merged-package-document), [R-NPM-031](spec/formats/npm.md#r-npm-031--dist-tag-merge), [R-REPO-020](spec/repositories-and-proxy.md#r-repo-020--merged-metadata-and-its-cache).

### ADR-071 · npm trusted publishing through package-bound CI tokens (P1)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Recent npm, pnpm and Yarn clients, run in CI with an ID token, exchange it for a
publish token before `npm publish`. The clients fix the path, and they disagree (verified on
2026-10-28 in the published packages): npm posts to the host root of the registry URL
(`/-/npm/v1/oidc/token/exchange/package/…`), while Yarn 4 and current pnpm post the same exchange
below the registry URL. pnpm uses the ID token only when its audience is `npm:<registry host>`.

**Decision.** trove serves that exchange at every entry point the clients use, through one handler
that is a second entry point into the CI exchange of [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint), with the same validation and
mappings ([Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6), option (d)):
- **npm** posts to the host-root prefix `/-/npm/v1/oidc/token/exchange/package/`.
- **Yarn and pnpm** post below every npm registry URL.

trove owns its host root: `server.base_url` is an origin without a path, and a base URL with a path
is refused at start-up. The issued CI token is bound to one package: its scope carries a single
grant on that exact name with `read` and `deploy`, and the name is escaped like a claim value, so it
cannot widen to a scope. Without `deploy` on the name in any npm local repository, no token is
issued.

**Consequences.** The host-root route is part of INV-6, the same exchange is served below every
registry URL for Yarn and pnpm, the issuer audience default includes `npm:<host>`, and clients
without a native exchange use the generic exchange. `npm publish` stays a one-liner in GitLab for
npm, pnpm and Yarn alike. Attestations are served later below the registry URL through a rewritten
`dist.attestations.url`, which keeps the repository known for permissions and upstream selection;
no host-root prefix is reserved for them. Trusted publishing widens the set of credentials that can
publish ([H-31](spec/formats/npm.md#h-31--trusted-publishing-widens-the-set-of-credentials-that-can-publish)).

**Alternatives considered.** No host-root route, exchanging through the generic endpoint in the job:
works everywhere, but turns a native client feature of all three clients into pipeline
boilerplate. Two host-root prefixes, for the exchange and for attestations, and nothing below the
registry URL: Yarn and pnpm would post the ID token to an unimplemented route and publish without
credentials, and a host-root attestation route would have to guess the repository. A path-prefix
fallback: builds for a deployment mode that INV-6, the `__Host-` session cookie and the OCI `/v2/`
reservation rule out. One host per repository: DNS, TLS and Ingress objects per repository.

**Spec:** [R-NPM-001](spec/formats/npm.md#r-npm-001--registry-endpoint-routing-and-repository-classes), [R-NPM-022](spec/formats/npm.md#r-npm-022--trusted-publishing-through-ci-oidc-exchange), [R-CI-007](spec/gitlab-ci-oidc.md#r-ci-007--token-exchange-endpoint), [R-OPS-005](spec/operations-and-observability.md#r-ops-005--external-base-url-and-forwarded-hosts); decided:
[Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6).

### ADR-072 · Real-client acceptance matrices gate each format

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Registry protocols are defined as much by client behavior as by documents. A format is
only done when the clients users run work against it.

**Decision.** A format is accepted when its page's acceptance criteria pass end to end with real
clients against two replicas behind the chart's Service and Ingress, with PostgreSQL and an
S3-compatible store. npm's gate rows follow the rule of R-NPM-045 ([Q-NPM-4](spec/formats/npm.md#q-npm-4--current-client-majors-in-the-acceptance-gate), option (c)):
- **npm:** every major bundled with a Node.js line in active or maintenance LTS, plus the major
  npm's `latest` points to.
- **pnpm:** every major whose newest release is at most 6 months old.
- **Yarn Berry:** `latest`, plus the previous major while its newest release is at most 6 months
  old.
- **Yarn Classic:** 1.22, always.

On 2026-10-28 the rule resolves to npm 10, 11 and 12, pnpm 10, 11 and 12, Yarn 4 and Yarn 1.22.
They run against a deterministic stub upstream, plus a nightly read-only run against the public
registry. NuGet gates on the .NET 10 SDK on Linux, nuget.exe on Windows, nightly runs against
nuget.org, and manual Visual Studio checks per release. Every client version is pinned once in the
harness, read by local runs and CI alike. A scheduled job raises the pins within each major and
re-evaluates the rule at every release. A major that leaves the rule stays as a non-blocking row
for one more release.

**Consequences.** Regressions in client compatibility block a release. The gate tracks what users
run, such as npm 10 in every Node.js 22 image until that line ends, without a spec change per
release. Eight npm rows cost CI minutes, not development effort. Scenarios a row's client cannot
run are marked not applicable per row.

**Alternatives considered.** Protocol-level tests only: miss client quirks that real users hit. A
fixed list of client majors: wrong in one direction as soon as it is written. Gating only on older
majors with the newest ones non-blocking: a regression for the clients new installations use would
not block a release.

**Spec:** [R-NPM-045](spec/formats/npm.md#r-npm-045--client-acceptance-matrix), [R-NUGET-027](spec/formats/nuget.md#r-nuget-027--acceptance-test-suite); decided: [Q-NPM-4](spec/formats/npm.md#q-npm-4--current-client-majors-in-the-acceptance-gate).

### ADR-116 · npm documents are cached per replica under keys read from PostgreSQL

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** npm documents are the most expensive responses trove renders: a large upstream
package's full document is tens of megabytes, and Yarn 2 and later revalidate full documents on
every install. Requests now reach any of n `web` replicas at random. INV-3 admits shared caches in
Valkey, while [ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants) keeps rendered documents in process for every format; this entry records
npm's check of that line.

**Decision.** Rendered local documents, abbreviated forms of remote documents and merged documents
are cached in each replica's rendition and merged-metadata caches, keyed by `rev_seq`, the stored
document's digest and the merge fingerprint, which every request reads from PostgreSQL. The ETag
is computed from the same inputs, including `npmRenderVersion` and the merge fingerprint, and
dates never validate a document ([Q-NPM-10](spec/formats/npm.md#q-npm-10--where-npm-documents-are-cached-when-n-replicas-serve-them), option (a)). The README is stored at publish, so no
rendering reads the object store.

**Consequences.** Every replica gives the same answer and the same validator, with or without
Valkey, and a revalidation (`304`) costs one indexed query on any replica and no rendering. Each
replica renders each package revision once, so n renders per revision, and caches are cold after a
rollout. A date is the one validator input that a restore moves backwards and that settings and
member changes leave unchanged, which is why it validates nothing.

**Alternatives considered.** A shared second level in Valkey: a Valkey writer could serve a changed
`dist.tarball` or `integrity`, values of megabytes break the 1 KiB rule, and a loss of Valkey turns
into a render burst on every replica. Renderings in object storage: a write per publish and
refresh plus collection of superseded renderings, for a read that costs about what rendering a
typical document costs; it is the next step, with a digest check on read, if rendering limits
scale-out.

**Spec:** [R-NPM-010](spec/formats/npm.md#r-npm-010--full-package-document), [R-NPM-013](spec/formats/npm.md#r-npm-013--conditional-requests-and-cache-headers), [R-NPM-046](spec/formats/npm.md#r-npm-046--npm-across-replicas-no-state-between-requests-valkey-degradation-drain); decided:
[Q-NPM-10](spec/formats/npm.md#q-npm-10--where-npm-documents-are-cached-when-n-replicas-serve-them).

### ADR-117 · An identical npm publish retried after a lost answer is acknowledged

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** npm's fetch layer retries every request other than a `POST` after a connection error
and on 408, 420, 429 and any 5xx, so a publish `PUT` whose answer is lost is sent again, and the
Service delivers the retry to any replica. trove's own shutdown never cuts a publish between its
commit and its status line ([ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica)), but an answer can still be lost outside trove: at an edge
proxy on a drained node, through a reset, or by the client's own timeout. The retry then met its own
version and answered 409.

**Decision.** A retry by the same principal with identical bytes (tarball and provenance bundle)
and the same dist-tag, within `npm.publish.retry_window` (10 minutes) on PostgreSQL's clock, is
answered 201 with an `npm-notice` and changes nothing ([Q-NPM-11](spec/formats/npm.md#q-npm-11--a-publish-retried-after-its-answer-was-lost), option (b)). The same check runs
again under the package row lock. A revisioned unpublish or deprecation retried with a stale `_rev`
succeeds when it would change nothing.

**Consequences.** A lost answer costs nothing, and any replica decides the retry alike, because
every fact it reads was committed with the original (INV-18). Nothing is written, so no version,
tag, `rev_seq` or object changes, and immutability ([ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones)) stands: the rule applies to live
versions only and never revives a tombstoned one. A rerun of the same pipeline within the window,
with a reproducible build, succeeds instead of failing with 409. A publish of the same bytes under
another dist-tag still answers 409, because a 201 that says "nothing changed" must not answer a
request that asked for a different tag.

**Alternatives considered.** Always 409: a publish that succeeded fails its CI job when the answer
is lost outside trove. Acknowledging any identical republish: removes the signal that a version
already exists, and another principal would "publish" what someone else released. An idempotency
key from the `npm-session` header: not verified for pnpm and Yarn, and only the client's claim.

**Spec:** [R-NPM-007](spec/formats/npm.md#r-npm-007--publish), [R-NPM-008](spec/formats/npm.md#r-npm-008--version-immutability-and-tombstones), [R-NPM-009](spec/formats/npm.md#r-npm-009--document-revisions-and-read-for-write), [R-NPM-046](spec/formats/npm.md#r-npm-046--npm-across-replicas-no-state-between-requests-valkey-degradation-drain); decided:
[Q-NPM-11](spec/formats/npm.md#q-npm-11--a-publish-retried-after-its-answer-was-lost).

### ADR-118 · A restore withholds npm versions unpublished after the snapshot

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A restore returns every table to the backup's snapshot, `npm_tombstones` included, so
an unpublish made between the snapshot and the loss of the old database is undone: the version is
live again and its tombstone is gone. Unpublish is how a version that leaked a secret or carried
malicious code is withdrawn, and with a daily backup the window is up to a day. The trash can is
P1, so in the MVP every deletion is permanent.

**Decision.** `npm.unpublish` is journaled in the revocation journal ([ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination)). Replay withholds a
restored version that is live with the journaled integrity: it tombstones the version and deletes
nothing, so no route serves it, `/files` included. An administrator confirms a withdrawal by
deleting the items or reverses it with `trove admin npm reinstate` ([Q-NPM-12](spec/formats/npm.md#q-npm-12--unpublishes-made-after-a-restored-backups-snapshot), option (c)).
A version the restored database does not hold is left alone.

**Consequences.** A withdrawn version stops installing at once after the replay, on both restore
paths, without a manual step. A mistaken or forged entry costs availability until an administrator
reinstates the version, never data. A withheld version occupies storage until it is deleted, and
its bytes stay readable to object-store principals meanwhile. Dist-tag moves, deprecations and
versions published after the snapshot stay reverted or lost; the npm page records them as
[H-51](spec/formats/npm.md#h-51--a-restore-reverts-npm-metadata-changes-made-after-the-backups-snapshot). NuGet answers the same question differently ([ADR-122](#adr-122--a-restore-replays-nuget-publishes-and-deletes-into-tombstones)): it keeps serving a revived
version and reports it, and it reserves lost numbers. One rule for both formats is an item for the
owner's walkthrough; both replays delete nothing until the trash can exists.

**Alternatives considered.** Documenting a manual replay: a step easy to miss after a disaster.
Replaying the unpublish as a deletion: without the trash can, a forged entry written with the
runtime S3 credential would destroy restored versions for good. Journaling every npm change: replay
would add content the backup does not hold. Tombstone-only entries for versions the restored
database lacks: a forged entry could burn version numbers for good.

**Spec:** [R-NPM-008](spec/formats/npm.md#r-npm-008--version-immutability-and-tombstones), [R-NPM-047](spec/formats/npm.md#r-npm-047--npm-data-in-backups-and-restores), [R-HA-054](spec/ha-and-helm.md#r-ha-054--revocation-journal-in-object-storage), [R-BAK-024](spec/backup-and-restore.md#r-bak-024--revocation-journal-replay-after-a-restore); decided:
[Q-NPM-12](spec/formats/npm.md#q-npm-12--unpublishes-made-after-a-restored-backups-snapshot).

### ADR-119 · Cached npm tarballs wait for a current package document

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A restore returns `npm_remote_versions` and the core's gone marks to the backup's
snapshot, so a version the upstream withdrew since is listed again and its cached tarball would be
served. Lockfile installs request tarballs without documents, so they never trigger the refresh
that would mark it again. The same blind spot existed without a restore: a cached tarball was
served on its stored marks however old the document was.

**Decision.** A cached tarball is served only while its package document's cache entry is current:
unexpired, and validated after `system_state.restored_at`. Otherwise the request revalidates the
document first, in one flight ([Q-NPM-13](spec/formats/npm.md#q-npm-13--removal-marks-after-a-restore-and-for-cached-tarballs), option (c)).

**Consequences.** The upstream's current state decides, on every replica, after a restore and for
lockfile installs alike, from values on PostgreSQL's clock (INV-18). Lockfile-only traffic costs
one conditional upstream request per package and metadata period, collapsed across replicas. While
the upstream is unreachable, the stored or restored marks decide, so stale serving survives an
upstream outage; that residual is in the npm page's [H-51](spec/formats/npm.md#h-51--a-restore-reverts-npm-metadata-changes-made-after-the-backups-snapshot).

**Alternatives considered.** Leaving it to document requests: a lockfile-only workload keeps
installing a withdrawn version indefinitely. Journaling upstream removals: covers restores only,
keeps a version hidden that the upstream listed again, and lets forged entries hide public
versions.

**Spec:** [R-NPM-025](spec/formats/npm.md#r-npm-025--tarball-urls-and-tarball-fetching), [R-NPM-026](spec/formats/npm.md#r-npm-026--versions-removed-upstream), [R-NPM-047](spec/formats/npm.md#r-npm-047--npm-data-in-backups-and-restores), [R-BAK-022](spec/backup-and-restore.md#r-bak-022--state-a-restore-rebuilds-or-invalidates); decided:
[Q-NPM-13](spec/formats/npm.md#q-npm-13--removal-marks-after-a-restore-and-for-cached-tarballs).

## NuGet

### ADR-073 · NuGet v3 only, every resource below the feed root

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** NuGet 6.7 and later pre-authenticate only URLs in or below the directory of an
authenticated URL. Resources outside it cost a 401 round trip each.

**Decision.** Every NuGet repository speaks v3 only. All resources of a repository live below
`/api/nuget/<repoKey>/v3/`; the service index answers identically with and without `index.json` and
the trailing slash. Any other path, including every v2 request shape, answers 404 with a message
naming the v3 service index URL. Every URL in a NuGet document is absolute and points at the
addressed repository; virtual and remote repositories never expose member or upstream URLs.

**Consequences.** Clients misconfigured for v2 get an actionable error. NuGet v2 is P2 ([ADR-025](#adr-025--npm-and-nuget-v3-are-the-mvp-formats-inv-14)).

**Alternatives considered.** A per-repository protocol switch: two protocol surfaces per repository.

**Spec:** [R-NUGET-001](spec/formats/nuget.md#r-nuget-001--nuget-repositories-and-endpoint-layout), [R-NUGET-002](spec/formats/nuget.md#r-nuget-002--service-index-and-http-conventions), [R-NUGET-015](spec/formats/nuget.md#r-nuget-015--remote-repository-proxying-caching-and-url-rewriting).

### ADR-074 · NuGet package IDs: ASCII on push, case-insensitive, first casing displayed

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The lowercased ID appears in every URL, in duplicate detection and in permission
matching. Go's and .NET's Unicode case tables differ in corner cases.

**Decision.** IDs that enter a local repository are restricted to ASCII per nuget.org's restricted
set, at most 100 characters, with `_` allowed anywhere as NU5052 reads it ([Q-NUGET-3](spec/formats/nuget.md#q-nuget-3--non-ascii-package-ids), option (a)).
The canonical name of such an ID is the ASCII-lowercased ID. Upstream IDs follow NuGet's general
Unicode rule and are keyed by the client's lowercased request path:
- **No lowercasing of non-ASCII IDs.** trove never lowercases a non-ASCII ID itself. It compares the
  declared ID with the client's path by Unicode simple case folding.
- **Fail closed.** A mismatch fails with 502.
- **No copy into local repositories.** A cached legacy ID cannot be copied into a local repository.

The display name is the casing of the version that created the package and does not change on
later pushes; any casing is accepted, and each version keeps its own casing in its catalog entry
([Q-NUGET-5](spec/formats/nuget.md#q-nuget-5--id-casing-on-later-pushes), option (a)). Versions are kept in three forms: original, full normalized and the
lowercased URL form without build metadata.

**Consequences.** An ID cannot lowercase differently on client and server. A case-table difference
can make a legacy upstream package unrestorable but cannot make one ID reach another package's row
or grant. Two clients that lowercase a legacy ID differently can at worst create two cache rows for
one upstream package, whose bytes are stored once. Legacy public IDs stay restorable. Non-ASCII
internal IDs are not supported, which matches what nuget.org has refused on push since 2026-07-15.
A deliberate casing correction shows in new versions' catalog entries but not in the display name.

**Alternatives considered.** NuGet's full Unicode ID rule on push, with lowercasing that matches
.NET exactly: would rebuild inside trove the hazard the ecosystem is retiring. ASCII everywhere: the
legacy IDs nuget.org still serves would become unrestorable through trove. Refusing other casing
on later pushes: blocks a legitimate correction for no protocol reason. The highest version's
casing as display name: a single mis-cased release renames the package in every search result.

**Spec:** [R-NUGET-003](spec/formats/nuget.md#r-nuget-003--package-ids-versions-and-normalization), [R-NUGET-004](spec/formats/nuget.md#r-nuget-004--semver-200-classification-and-visibility), [R-NUGET-015](spec/formats/nuget.md#r-nuget-015--remote-repository-proxying-caching-and-url-rewriting); decided: [Q-NUGET-3](spec/formats/nuget.md#q-nuget-3--non-ascii-package-ids),
[Q-NUGET-5](spec/formats/nuget.md#q-nuget-5--id-casing-on-later-pushes).

### ADR-075 · Strict archive and manifest parsing; the declared ID is what gets authorized

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A `.nupkg` is a ZIP with an XML manifest. Two readers that resolve duplicate entries or
alternative spellings differently could let the identity trove authorizes differ from the one a
client installs.

**Decision.** Archives are read through the central directory only. Encrypted, split or inconsistent
archives, absolute or `..` entry names, and case-insensitive duplicate names are rejected. Exactly
one `.nuspec` must sit at the root. XML is parsed without document type declarations or entity
expansion, within a size limit. The ID the manifest declares is the coordinate that is authorized,
ownership-checked and indexed. Callers with no `deploy` grant on the repository are refused before
the body is read.

**Consequences.** Unauthorized callers cannot fill the spool. Some unusual but valid archives may be
rejected; the limits are per format and can be revisited with evidence.

**Alternatives considered.** Lenient parsing as some clients do: identity ambiguities become
authorization bypasses.

**Spec:** [R-NUGET-005](spec/formats/nuget.md#r-nuget-005--package-ingestion-nuspec-parsing-and-storage-layout), [R-NUGET-006](spec/formats/nuget.md#r-nuget-006--push-through-the-package-publish-resource), [R-ARCH-028](spec/architecture.md#r-arch-028--resource-limits-for-untrusted-package-content).

### ADR-076 · Symbol packages only as `.snupkg` matching an existing package

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Debug symbols must match the binaries of an immutable package version.

**Decision.** Symbol packages are pushed to the dedicated symbol publish resource as `.snupkg`. The
matching `.nupkg` must already exist in the target repository; entries are limited to the extensions
nuget.org accepts and every `.pdb` is checked by file signature. Re-pushing identical bytes succeeds
without change; different bytes answer 409. Legacy `.symbols.nupkg` packages are rejected. The symbol
server for local repositories and symbol resolution through remote and virtual repositories are P1;
indexing runs as one job per symbol package on the `worker` role ([ADR-123](#adr-123--nuget-symbol-packages-are-indexed-by-one-job-each-on-the-worker-role)).

**Consequences.** Symbols cannot drift from the binaries they describe.

**Alternatives considered.** Accepting legacy symbol packages: a second format to validate for
clients that have long moved on.

**Spec:** [R-NUGET-020](spec/formats/nuget.md#r-nuget-020--symbol-package-push), [R-NUGET-021](spec/formats/nuget.md#r-nuget-021--symbol-server-for-local-repositories), [R-NUGET-022](spec/formats/nuget.md#r-nuget-022--symbol-proxy-and-symbol-resolution-through-virtual-repositories).

### ADR-090 · A NuGet protocol delete unlists by default

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** `dotnet nuget delete` and `nuget delete` send the protocol `DELETE`, which the public
gallery treats as unlisting. A hard delete breaks every consumer pinned to the version or
restoring from a lock file, and in the MVP no trash can (P1) catches it.

**Decision.** The protocol `DELETE` unlists, unless the repository sets
`nuget.deleteMode: delete`, which turns it into a hard delete for that repository
([Q-NUGET-1](spec/formats/nuget.md#q-nuget-1--default-meaning-of-a-nuget-delete), option (a)). Management, UI and raw deletes are always hard deletes. A hard delete
writes a tombstone, so the version number stays reserved ([ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones)). Unlisting an unlisted
version answers 204. The UI shows which mode applies. The setting lives in the format member
`nuget` of the repository document, as repositories-and-proxy.md places format settings.

**Consequences.** Consumers that already resolved a version keep restoring it, and unlisting is
reversible by a relist, which needs `deploy` ([ADR-052](#adr-052--changing-what-clients-install-needs-deploy-not-annotate)). With the tombstone, offering hard delete
through the protocol cannot turn into an overwrite in two steps. One client command has two
behaviors, depending on the repository. Storage is freed only by a hard delete or by a retention
policy; purging unlisted versions after a period is a retention policy, not a delete mode.

**Alternatives considered.** Hard delete by default: a routine cleanup command breaks every
consumer of that version, and in the MVP the removal is final. The protocol always unlisting, with
no setting: scripted cleanup with the NuGet client becomes impossible, for no security gain, since
both paths check the same `delete` permission and ownership.

**Spec:** [R-NUGET-006](spec/formats/nuget.md#r-nuget-006--push-through-the-package-publish-resource), [R-NUGET-007](spec/formats/nuget.md#r-nuget-007--unlist-relist-and-delete), [R-STOR-022](spec/storage.md#r-stor-022--trash-can); decided: [Q-NUGET-1](spec/formats/nuget.md#q-nuget-1--default-meaning-of-a-nuget-delete).

### ADR-120 · Forwarded NuGet search results are cached per replica

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A remote repository caches the upstream's answers to forwarded searches for
`remote_result_ttl` (60 s). With the load spread over n `web` replicas at random, a per-replica
cache hits about 1/n as often for one user's consecutive pages, and the upstream may see up to n
requests per distinct query and period. Valkey must not hold content trove serves, and its values
are limited to 1 KiB ([ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants)).

**Decision.** Forwarded search results are cached in each `web` replica's memory, in the registered
core cache `nuget_remote_search`, unfiltered as the upstream sent them. Patterns, claims, pin rules,
permissions and URL rewriting apply on every use, and repository configuration changes flush the
cache. Nothing is shared through Valkey or PostgreSQL ([Q-NUGET-8](spec/formats/nuget.md#q-nuget-8--forwarded-search-results-across-replicas), option (a)).

**Consequences.** Any replica may answer from its copy or ask the upstream within the same period
(INV-18), and a new claim protects cached results as soon as the replica applies it. The upstream
cost is bounded by n times the distinct queries per minute; nuget.org's documentation names its v3
search as not rate limited (page last updated in 2020).

**Alternatives considered.** A shared cache in Valkey: search results are served content, so a
Valkey writer could inject package IDs, download counts or `verified` flags into the list
developers choose from, and a page of results exceeds the value limit. A shared cache in
PostgreSQL: a write per distinct query on the system of record, for data worthless after a minute.
No cache: every search and every browse page reaches the upstream.

**Spec:** [R-NUGET-016](spec/formats/nuget.md#r-nuget-016--search-and-autocomplete-through-a-remote-repository), [R-NUGET-030](spec/formats/nuget.md#r-nuget-030--nuget-on-interchangeable-replicas-inv-18), [R-ARCH-032](spec/architecture.md#r-arch-032--no-state-between-requests-on-one-replica-inv-18); decided:
[Q-NUGET-8](spec/formats/nuget.md#q-nuget-8--forwarded-search-results-across-replicas).

### ADR-121 · Rendered NuGet documents carry weak ETags

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Rendered NuGet documents had a strong ETag derived from `rev_seq` or the stored upstream
digest, the hive and the base URL. Requests now reach any replica, including replicas of the
previous release during every rolling upgrade, and the gzip hives send the same value with and
without content coding. RFC 9110 calls a validator weak when representations with different bytes
share it.

**Decision.** Rendered NuGet documents carry a weak ETag `W/"…"` over the plug-in's render version
and the document's inputs; stored files keep their strong, content-addressed ETag
([Q-NUGET-9](spec/formats/nuget.md#q-nuget-9--etags-of-rendered-documents-on-replicas-of-different-releases), option (c)). A virtual document's inputs are the member fingerprint taken after the pin
rules and claims, every local member's `rev_seq` and every remote input's digest, so a push to a
local member or a new claim changes the value. Both codings send the same value, with
`Vary: Accept-Encoding` on the gzip hives.

**Consequences.** `If-None-Match` uses the weak comparison, so `304` keeps working across replicas,
codings and releases. A release that must invalidate cached documents raises the render version.
Clients cannot use the value with `If-Range`, which no NuGet client does on metadata; package files
keep strong ETags because resuming a download needs them.

**Alternatives considered.** The earlier strong ETag: wrong by RFC 9110 for the gzip hives, and
misleading while N and N-1 render different bytes. A strong ETag that also covers coding and
release: every upgrade turns every cached validator stale, and during a rollout about every second
conditional request answers 200. No ETag: every revalidation transfers the whole document.

**Spec:** [R-NUGET-002](spec/formats/nuget.md#r-nuget-002--service-index-and-http-conventions), [R-NUGET-030](spec/formats/nuget.md#r-nuget-030--nuget-on-interchangeable-replicas-inv-18), [R-NUGET-033](spec/formats/nuget.md#r-nuget-033--nuget-data-in-backups-and-restores); decided:
[Q-NUGET-9](spec/formats/nuget.md#q-nuget-9--etags-of-rendered-documents-on-replicas-of-different-releases).

### ADR-122 · A restore replays NuGet publishes and deletes into tombstones

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A restore returns an installation to its backup's snapshot. Versions pushed after the
snapshot are gone, and their numbers carry no tombstone, so a pipeline that runs again pushes the
same number, generally with other bytes; clients that fetched the old bytes then fail on a content
hash or silently diverge. Versions deleted after the snapshot come back. The MVP has no trash can.

**Decision.** Publishes and deletions on local NuGet repositories are journaled in the revocation
journal ([ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination)), and replay only restricts ([Q-NUGET-10](spec/formats/nuget.md#q-nuget-10--versions-changed-after-a-backups-snapshot), option (b)):
- a version that entered after the snapshot and is absent now gets a `reserved` tombstone that
  admits only its journaled SHA-512;
- a removal of an absent version turns or writes its tombstone into `removed`, so a version
  published and deleted after the snapshot stays refused;
- a removal of a version that is present is reported as `revived` and not applied.

No reservation is made when the replay does not run (journal skipped or disabled, entries not yet
shipped).

**Consequences.** One ID and version keep one content across a restore. A pipeline that rebuilds
byte-identically restores its version, and any other build has to take a new number
(`nuget_version_reserved`). A forged entry can reserve a number for good, which costs availability
only; the credential needed to forge one can already delete blobs. Revived versions stay until an
administrator acts, which [H-50](spec/formats/nuget.md#h-50--a-restore-revives-nuget-versions-deleted-after-the-backups-snapshot-and-can-free-lost-version-numbers) records. npm withholds instead of reviving and reserves no lost numbers
([ADR-118](#adr-118--a-restore-withholds-npm-versions-unpublished-after-the-snapshot)); one rule for both formats is an item for the owner's walkthrough, and both wait for the
trash can (P1), into which replay could then move revived versions.

**Alternatives considered.** Accepting the snapshot as it is: a rebuilt version reuses a number
clients already resolved. Replay that deletes revived versions again: a forged entry could delete
versions and tombstone their numbers, with nothing in the MVP to undo it. Recovering lost versions
from the old blob store: the source may be what was lost, and a restore targets an empty
installation.

**Spec:** [R-NUGET-006](spec/formats/nuget.md#r-nuget-006--push-through-the-package-publish-resource), [R-NUGET-007](spec/formats/nuget.md#r-nuget-007--unlist-relist-and-delete), [R-NUGET-033](spec/formats/nuget.md#r-nuget-033--nuget-data-in-backups-and-restores), [R-HA-054](spec/ha-and-helm.md#r-ha-054--revocation-journal-in-object-storage),
[H-50](spec/formats/nuget.md#h-50--a-restore-revives-nuget-versions-deleted-after-the-backups-snapshot-and-can-free-lost-version-numbers); decided: [Q-NUGET-10](spec/formats/nuget.md#q-nuget-10--versions-changed-after-a-backups-snapshot).

### ADR-123 · NuGet symbol packages are indexed by one job each on the worker role

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Symbol indexing extracts every PDB of a `.snupkg`, writes it to object storage and
computes its key and checksum. Done in the push request, that work follows the body and grows with
the package, while pushes run on `web` replicas that a drain may stop at any time, and the shutdown
protocol protects a push between commit and status line only for work bounded by P-11.

**Decision.** The symbol push only validates and stores the `.snupkg`. Its commit enqueues one
`nuget-symbol-index` job for that package on the `worker` role, with a dedup key per version in
`exclusive` mode; the job extracts and indexes it. A row lock and an `indexed_at` check in the
indexing transaction keep two runs of one package apart, and a package that cannot be indexed ends
its own job with `index_error` ([Q-NUGET-11](spec/formats/nuget.md#q-nuget-11--where-symbol-indexing-runs), option (b)).

**Consequences.** The push stays a plain upload within P-11, and indexing scales and drains with
the `worker` role: the packages of one repository are indexed on every `worker` replica at once. A
symbol key answers `404` until its job commits, with a proposed bound of 30 s at p99. The same job
kind serves the backfill and the reindex, which only enqueues jobs. One job row per symbol push.

**Alternatives considered.** Indexing in the push request: work after the body that a drain can cut
after the commit, on `web`. Serving PDBs from the `.snupkg` without extraction: the key and checksum
still need each PDB inflated at push, and every debugger request inflates again. One coalesced job
per repository: the busiest repository is indexed by one `worker` at a time, and one broken package
holds back every package pushed after it.

**Spec:** [R-NUGET-020](spec/formats/nuget.md#r-nuget-020--symbol-package-push), [R-NUGET-021](spec/formats/nuget.md#r-nuget-021--symbol-server-for-local-repositories), [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql); decided:
[Q-NUGET-11](spec/formats/nuget.md#q-nuget-11--where-symbol-indexing-runs).

## Backup and restore

### ADR-106 · Backup format: a trove-native snapshot export, a shared blob pool and a garbage-collection hold

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A backup must be consistent across the database and the blobs it references
([ADR-097](#adr-097--consistent-backups-to-s3-restored-into-an-empty-installation-inv-19)), must stay practical as a daily job for large installations, and must not be failed by
garbage collection, whatever the grace period and however long a first full backup takes.

**Decision.**
- **Database part** ([Q-BAK-1](spec/backup-and-restore.md#q-bak-1--form-of-the-database-part-of-a-backup), option (b)). Every table is exported with
  `COPY (SELECT …) TO STDOUT` from one `REPEATABLE READ READ ONLY` snapshot, with the tables locked
  `ACCESS SHARE` before the snapshot and the migration lock held shared; a table registry assigns
  each table an export class, and the blob rows are filtered to the referenced set. A restore builds
  the schema through trove's own migrations and loads with `COPY FROM`.
- **Blobs** ([Q-BAK-3](spec/backup-and-restore.md#q-bak-3--blob-layout-in-the-target-and-incremental-backups), option (b)). One content-addressed pool in the target, shared by every
  backup of that target, and one sorted blob list per backup. A run copies only the digests missing
  from the previous backup's list, so backups are incremental by construction. Retention deletes
  backups, and a mark-and-sweep removes pool objects that no remaining backup lists.
- **Garbage-collection hold** ([Q-BAK-2](spec/backup-and-restore.md#q-bak-2--keeping-a-running-backups-blobs-away-from-garbage-collection), option (c)). From before the snapshot until the to-copy
  set is pinned, the run holds `all`; then only pinned digests are kept, and each pin is removed
  once its copy is confirmed. Both phases count only under the run's lease, so a dead run cannot
  block collection. The collector checks the hold in its per-blob transaction ([ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period)).

**Consequences.** No second binary, and the restore is independent of the PostgreSQL major version.
The schema always comes from the migrations every installation runs, and filters, checksums, the
hold and the locks sit in one code path. A completed backup's safety never depends on the hold,
because its manifest is written only after every listed digest is confirmed in the pool; the hold
keeps runs from failing. Deleting a backup frees space only through the sweep, and a damaged pool
object affects every backup that lists it, which `verify` detects. trove maintains the export and
load code and a table registry that a migration test keeps complete.

**Alternatives considered.** `pg_dump` with trove's exported snapshot: a second binary whose major
must track the newest server, and the same filters and checksums as trove code anyway. A platform
point-in-time restore referenced by a manifest: not self-contained. A `SERIALIZABLE DEFERRABLE`
snapshot: no serializable writers to protect, and a start that can wait indefinitely. A full copy
per backup, or one archive per backup: neither is incremental. Relying on source bucket versioning:
the backup would share the source's failure domain. A grace period longer than any backup: unsafe
for exactly the large first backups. Pausing all collection during a run: a daily reclamation stop.
A snapshot-visibility hold: its proof would rest on a queue invariant the storage page does not
guarantee.

**Spec:** [R-BAK-002](spec/backup-and-restore.md#r-bak-002--target-layout-backup-id-and-target-marker), [R-BAK-004](spec/backup-and-restore.md#r-bak-004--table-registry-and-export-classes), [R-BAK-005](spec/backup-and-restore.md#r-bak-005--consistent-snapshot-and-table-export), [R-BAK-006](spec/backup-and-restore.md#r-bak-006--blob-list-and-incremental-copy-into-the-pool),
[R-BAK-007](spec/backup-and-restore.md#r-bak-007--garbage-collection-hold), [R-BAK-016](spec/backup-and-restore.md#r-bak-016--retention-and-deletion-of-backups), [R-BAK-017](spec/backup-and-restore.md#r-bak-017--pool-sweep), [R-STOR-024](spec/storage.md#r-stor-024--race-free-garbage-collection); decided: [Q-BAK-1](spec/backup-and-restore.md#q-bak-1--form-of-the-database-part-of-a-backup), [Q-BAK-2](spec/backup-and-restore.md#q-bak-2--keeping-a-running-backups-blobs-away-from-garbage-collection),
[Q-BAK-3](spec/backup-and-restore.md#q-bak-3--blob-layout-in-the-target-and-incremental-backups).

### ADR-107 · Backup runs: one at a time in a leased row, cancelled when they stall

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** An installation runs one target-writing backup operation at a time. A run can take
hours, a drain can evict its pod, and a hung run (a stuck connection, a store that accepts requests
and never answers) would keep its pins, the one-active slot and the CronJob's `Forbid` forever.

**Decision.**
- **Run row** ([Q-BAK-4](spec/backup-and-restore.md#q-bak-4--where-run-state-and-the-one-at-a-time-rule-live), option (b), with (d) as the first line). Every run of `create`, `delete`
  or `verify` is a `backup_run` row in the source database. A partial unique index admits one active
  target-writing run, a heartbeat renews its lease, and a stale run's lease is waited for, not
  refused; fencing rules stop a stalled process that wakes after its lease ended. The CronJob still
  uses `concurrencyPolicy: Forbid`.
- **Stall detection** ([Q-BAK-11](spec/backup-and-restore.md#q-bak-11--bounding-a-hung-backup-run), option (c)). The heartbeat renews the lease only while the run makes
  progress. After `backup.stall_timeout` (30 minutes) without progress the run fails as `stalled`,
  releases its pins and slot, and the Job retries. The Job has no `activeDeadlineSeconds` by
  default.

**Consequences.** Exclusion lives in PostgreSQL, as INV-3 requires, and works through any
connection path. The same row carries progress, hold and result for the collector, the API and the
metrics. A healthy first backup of a large installation is never killed for its length, and a hung
run frees everything within minutes. A retried run copies only the blobs that are still missing.
`TroveBackupRunningLong` stays the alert for a run that progresses too slowly.

**Alternatives considered.** A session advisory lock: a session-mode connection for hours and
nothing for other processes to read. A lock object in the target: conditional writes are not
available on every store, and the collector and the API cannot see it. `Forbid` alone: manual Jobs
bypass it, and Kubernetes may create two Jobs for one time. A Job deadline: kills a healthy first
backup every time. Alerts only: no backup runs until a person deletes the pod.

**Spec:** [R-BAK-009](spec/backup-and-restore.md#r-bak-009--trove-backup-create-phases-and-run-record), [R-BAK-010](spec/backup-and-restore.md#r-bak-010--one-target-writing-operation-at-a-time), [R-BAK-011](spec/backup-and-restore.md#r-bak-011--configuration), [R-BAK-012](spec/backup-and-restore.md#r-bak-012--kubernetes-cronjob-manual-runs-and-restore-job),
[R-BAK-013](spec/backup-and-restore.md#r-bak-013--exit-codes-interruption-and-resumption), [R-BAK-014](spec/backup-and-restore.md#r-bak-014--run-visibility-api-metrics-alerts-and-audit); decided: [Q-BAK-4](spec/backup-and-restore.md#q-bak-4--where-run-state-and-the-one-at-a-time-rule-live), [Q-BAK-11](spec/backup-and-restore.md#q-bak-11--bounding-a-hung-backup-run).

### ADR-108 · Backup credentials stay in backup pods; the API and UI only read runs

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Backups are trustworthy only if a compromised replica can neither read nor delete
them. The catalog's backup screen edits schedules and starts backups from the UI, and an operator
wants to see which backups exist and what retention kept.

**Decision.** Target credentials exist only in backup and restore pods; `web` and `worker` pods
never receive them, and the chart renders no RBAC for trove ([R-BAK-015](spec/backup-and-restore.md#r-bak-015--credentials-and-privileges-of-the-backup-role)). Following from it:
- **No start endpoint** ([Q-API-10](spec/api-and-ui.md#q-api-10--starting-a-backup-from-the-api-or-the-ui), option (a)). The API and UI start, delete, verify and restore no
  backup, in any priority. The UI shows a copyable `kubectl create job --from=cronjob/<name>`
  command, filled with the namespace and the CronJob's name, and shows the schedule and the
  retention read-only.
- **The stored-backup list comes from the runs** ([Q-API-11](spec/api-and-ui.md#q-api-11--where-the-apis-list-of-stored-backups-comes-from), option (c)). Each create or delete run
  records in its result the complete backups left in the target after its deletions and the
  retention it applied; the newest such run is the source of `storedBackups` and `retention`, with
  its age stated.

**Consequences.** No new privilege anywhere: a manual run is the same Job as a scheduled one and
lands in the same run list. Starting a backup needs `kubectl` access to the namespace. The stored
list is as fresh as the last create or delete run, and a backup removed outside trove shows until
the next run. The backup role itself can read everything and delete backups ([H-47](spec/backup-and-restore.md#h-47--the-backup-role-can-read-everything-and-delete-backups)).

**Alternatives considered.** `web` replicas creating the Job through the Kubernetes API: permission
to create workloads in a namespace implies access to every Secret a pod there can mount, so a
compromised `web` pod could read the target credentials. A request row picked up by a polling
backup pod: a dispatcher that keeps target credentials alive for a convenience one command already
gives. Backups as `worker` jobs: every `worker` pod would hold the target credentials. `web`
replicas reading the target: every `web` pod could read a full copy of the database.

**Spec:** [R-BAK-014](spec/backup-and-restore.md#r-bak-014--run-visibility-api-metrics-alerts-and-audit), [R-BAK-015](spec/backup-and-restore.md#r-bak-015--credentials-and-privileges-of-the-backup-role), [R-BAK-016](spec/backup-and-restore.md#r-bak-016--retention-and-deletion-of-backups), [R-API-050](spec/api-and-ui.md#r-api-050--backup-runs-status-and-stored-backups), [R-UI-048](spec/api-and-ui.md#r-ui-048--backups);
decided: [Q-API-10](spec/api-and-ui.md#q-api-10--starting-a-backup-from-the-api-or-the-ui), [Q-API-11](spec/api-and-ui.md#q-api-11--where-the-apis-list-of-stored-backups-comes-from).

### ADR-109 · Backup encryption: server-side at MVP, client-side for the database files at P1

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The database part of a backup holds personal data, token hashes and permissions; the
blobs are mostly content a registry serves to many readers anyway. Encrypting the pool client-side
would cost server-side copy and deduplication.

**Decision.** At MVP the target uses server-side encryption, `s3` by default. At P1, the database
files and the blob list can be encrypted client-side with age to configured recipients, while pool
objects stay server-side encrypted ([Q-BAK-6](spec/backup-and-restore.md#q-bak-6--encryption-of-backup-contents), option (b)). The backup role needs only the
recipients' public keys; the stored-secret key ring is escrowed separately ([R-BAK-028](spec/backup-and-restore.md#r-bak-028--stored-secret-key-ring-and-key-escrow)).

**Consequences.** Blobs stay deduplicated and copyable server-side. Until P1, anyone with read access
to the target reads the database files ([H-48](spec/backup-and-restore.md#h-48--database-files-are-readable-to-every-reader-of-the-target-until-p1)); a separate target account is the MVP protection.
Backup integrity is not authenticated ([H-46](spec/backup-and-restore.md#h-46--backup-integrity-is-not-authenticated)). The age identity is one more secret to escrow.

**Alternatives considered.** Server-side encryption only, for good: every reader of the target
reads the database files. Client-side encryption of everything: no server-side copy, deduplication
only through convergent encryption, which leaks equality, and every restore decrypts terabytes.

**Spec:** [R-BAK-026](spec/backup-and-restore.md#r-bak-026--encryption-in-transit-and-at-rest-on-the-target), [R-BAK-027](spec/backup-and-restore.md#r-bak-027--client-side-encryption-of-the-database-files), [R-BAK-028](spec/backup-and-restore.md#r-bak-028--stored-secret-key-ring-and-key-escrow); decided:
[Q-BAK-6](spec/backup-and-restore.md#q-bak-6--encryption-of-backup-contents).

### ADR-110 · Restore: an empty installation, any later release, resumable, as an ordinary Job

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A restore happens during an incident, copies every referenced blob and can run for
hours, while a node drain may happen at any time. The installation is already down, so every lost
hour prolongs the outage.

**Decision.**
- **Empty installation** ([Q-BAK-7](spec/backup-and-restore.md#q-bak-7--restore-target-empty-installation-only-or-reuse-of-an-existing-blob-store), option (a) at MVP). A restore requires an empty database and an
  empty blob prefix. Reusing an existing blob prefix is P1 (`--existing-blobs reuse`), together with
  the orphan sweep and the deletion pause.
- **Any later release** ([Q-BAK-8](spec/backup-and-restore.md#q-bak-8--restore-with-a-newer-trove-release), option (b)). Any release whose embedded migrations contain every
  step of the backup with the same checksums restores it: migrate to the backup's step, load,
  migrate forward.
- **Resumable** ([Q-BAK-10](spec/backup-and-restore.md#q-bak-10--resuming-an-interrupted-restore), option (c)). Before its first change the restore writes a schema
  anchor naming the backup ID and holds the migration lock for the whole restore. A later attempt
  for the same backup ID continues where the last one stopped; any other attempt refuses. A
  finished restore removes the anchor.
- **An ordinary Job** ([Q-HA-18](spec/ha-and-helm.md#q-ha-18--how-a-restore-runs-in-the-chart), option (b)). While `restore.enabled`, the chart renders the Job
  `<fullname>-restore` with its own ServiceAccount, keeps both roles at zero, renders no migration
  Job and suspends the backup CronJob; `trove migrate` refuses an anchored database. A second
  `helm upgrade` with normal values starts the roles. The Job retries an evicted pod
  (`restore.backoffLimit: 2`).

**Consequences.** No two installations share state by accident, and no writer runs during a
restore. The path forward is the one every upgrade takes, so no second migration path exists. A
drain or crash during a restore costs one pod restart, and no Helm or GitOps timeout limits the
copy. A restore always copies every blob, also when the old bucket survived, until P1. Restoring
takes two Helm commands and a wait.

**Alternatives considered.** Reusing an existing blob prefix at MVP: needs the orphan sweep and the
deletion pause, both P1. An in-place restore: stops everything anyway and mixes states on failure.
The backup's exact release only: an old image to find during an incident. Converting while loading:
a second, untested migration path. No retry, or an explicit `--resume`: the Job's own retry pod
would refuse, so a drain still fails the restore. A pre-install hook: Helm and GitOps time out
during a long restore. A documented `kubectl run`: mounts and credentials reproduced by hand at
the worst moment.

**Spec:** [R-BAK-020](spec/backup-and-restore.md#r-bak-020--restore-preconditions), [R-BAK-021](spec/backup-and-restore.md#r-bak-021--restore-procedure), [R-BAK-023](spec/backup-and-restore.md#r-bak-023--schema-and-postgresql-version-compatibility-on-restore), [R-BAK-025](spec/backup-and-restore.md#r-bak-025--restore-onto-an-existing-blob-store),
[R-HA-019](spec/ha-and-helm.md#r-ha-019--migration-job), [R-HA-060](spec/ha-and-helm.md#r-ha-060--backup-cronjob-and-restore-job); decided: [Q-BAK-7](spec/backup-and-restore.md#q-bak-7--restore-target-empty-installation-only-or-reuse-of-an-existing-blob-store), [Q-BAK-8](spec/backup-and-restore.md#q-bak-8--restore-with-a-newer-trove-release), [Q-BAK-10](spec/backup-and-restore.md#q-bak-10--resuming-an-interrupted-restore), [Q-HA-18](spec/ha-and-helm.md#q-ha-18--how-a-restore-runs-in-the-chart).

### ADR-111 · A restore moves Valkey to a new keyspace and offsets every revision

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A restore moves counters backwards, and two kinds of consumer remember them: Valkey
inside trove, and clients outside, through `ETag` and `If-Match` values built from `rev_seq` and
epochs. Without a fix-up, keys and validators repeat with different content, and a shared Valkey
serves entries of the old installation.

**Decision.** Every restore writes a new `system_state.keyspace_id`, which every Valkey key and
channel carries, and adds 2^32 to every registered revision column, `packages.rev_seq` and
`cluster_epoch.epoch` among them ([Q-BAK-5](spec/backup-and-restore.md#q-bak-5--derived-state-that-outlives-the-old-database), option (c)). The restore Job needs no Valkey
credentials or egress, and `trove admin post-restore` applies the same two fix-ups after a
point-in-time restore.

**Consequences.** Stale Valkey entries become unreachable without touching Valkey, and a restored
installation is isolated from an old one still running on the same Valkey; old keys expire by
their TTLs. Client validators from the old installation can never match again, while `ETag`s keep
their format. Every Valkey key carries one more segment, and a registry test keeps the revision
columns complete.

**Alternatives considered.** Nothing: wrong `304`s after a restore. Flushing Valkey during the
restore: Valkey credentials in the restore Job, a still-running old installation refills it, and
client validators stay wrong. An incarnation in every key and `ETag`: edits on every format page,
and every client revalidates everything after each restore.

**Spec:** [R-BAK-022](spec/backup-and-restore.md#r-bak-022--state-a-restore-rebuilds-or-invalidates), [R-ARCH-030](spec/architecture.md#r-arch-030--valkey-usage-map), [R-HA-025](spec/ha-and-helm.md#r-ha-025--restore-procedure), [R-HA-057](spec/ha-and-helm.md#r-ha-057--valkey-failover-outage-and-degraded-mode); decided:
[Q-BAK-5](spec/backup-and-restore.md#q-bak-5--derived-state-that-outlives-the-old-database).

### ADR-112 · The backup CronJob is on by default and needs its own target

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The owner made the backup job part of the service ([ADR-094](#adr-094--a-configurable-backup-job-writes-consistent-backups-to-s3)). An installation without
backups should not be the result of doing nothing, and a backup in the same bucket as the data
shares its failure domain.

**Decision.** The chart renders the backup CronJob by default, and rendering fails without
`config.backup.target.bucket` unless `backup.enabled: false` is set deliberately
([Q-BAK-9](spec/backup-and-restore.md#q-bak-9--backup-cronjob-on-by-default), option (b); [Q-HA-17](spec/ha-and-helm.md#q-ha-17--the-backup-cronjob-is-on-by-default), option (a); one question seen from both pages). The local
profile creates a second bucket, `trove-backups`, so the drill runs everywhere.

**Consequences.** Every installation has a backup from its first day, and the rendering error names
the missing value. The minimal install needs one more value.

**Alternatives considered.** Off by default with a warning: the unsafe state as default. A default
target inside the blob bucket: the same failure domain and credential as the data, and under the
blob prefix the orphan sweep would delete the backups.

**Spec:** [R-BAK-011](spec/backup-and-restore.md#r-bak-011--configuration), [R-BAK-012](spec/backup-and-restore.md#r-bak-012--kubernetes-cronjob-manual-runs-and-restore-job), [R-HA-029](spec/ha-and-helm.md#r-ha-029--fail-fast-chart-validation), [R-HA-031](spec/ha-and-helm.md#r-ha-031--values-reference), [R-HA-060](spec/ha-and-helm.md#r-ha-060--backup-cronjob-and-restore-job);
decided: [Q-BAK-9](spec/backup-and-restore.md#q-bak-9--backup-cronjob-on-by-default), [Q-HA-17](spec/ha-and-helm.md#q-ha-17--the-backup-cronjob-is-on-by-default).

## High availability and Helm

### ADR-077 · Singleton work under advisory-lock leases with fencing

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The scheduler's slots, background data migrations and similar work must not run twice
at once, and a replica that lost its lease must not keep writing. Revisited on 2026-10-28 after the
owner's constraints: every replica used to keep a coordination session for `LISTEN` and named
leases, the fetch lock of cache fills was advisory-lock category 2, garbage collection, the orphan
sweep and retention runs were listed as lease holders, and transaction-mode poolers were not
supported. Each of these changed.

**Decision.** Singleton work takes a session-level advisory lock on a connection from a dedicated
lease pool, which exists only on `worker` replicas ([ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases)). Every state change the singleton
makes runs on that same session: if the session dies, the lock is gone and the writes fail with
it. Actions outside PostgreSQL happen only after the database change that authorizes them has
committed on that session. The worker checks the session periodically, cancels on loss, and the
next scheduled run starts fresh from a persisted cursor. There is no per-replica coordination
session: invalidation runs on the change log ([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)), and a lost lease connection flushes no
cache. Every advisory lock uses the two-key form with a registered category, so lock diagnostics and
`pg_locks` can name every lock ([Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills), option (a) for every PostgreSQL advisory lock):
- category 2 `fetch` is retired and its number stays reserved, because the fetch lock moved to
  Valkey with the full SHA-256 of the flight key ([Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills) revisited, option (c) for that lock;
  [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11)); no category hashes its `objid` any more;
- the migration lock is `(3, 0)`;
- the single-`bigint` forms are forbidden everywhere, which a test enforces. That also excludes
  migration libraries with a fixed single-`bigint` lock.

Storage maintenance runs (garbage collection, the orphan sweep, retention) hold no lease: their
safety rests on row locks, and the exclusive job key keeps them apart ([ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period)).

**Consequences.** A partitioned worker cannot commit work it no longer owns. All singleton jobs
must be restartable. `web` replicas hold no session connection. A transaction-mode pooler is
supported for the main pool from the MVP on, with `database.session_host` for the lease pool,
`trove migrate` and `trove backup`, and a control endpoint for the control connection
([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), revisited: option (b) instead of (a); [Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler); [ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers)). Statement-mode poolers
are not supported. The `notify` readiness check is replaced by `invalidation`, which fails when the
change-log poll has stalled. A `worker` that shuts down releases the `scheduler` lease at once and
its other leases last ([ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica)).

**Alternatives considered.** Table-based leases with heartbeats: no held connection, but no immediate
release on session end, which fencing builds on; they would save only the dozen lease connections
of the workers ([Q-ARCH-14](spec/architecture.md#q-arch-14--postgresql-connection-model-for-n-replicas-and-transaction-mode-poolers), option (c)). Kubernetes leader election: no fencing of database
writes. A single 64-bit hash per lock: collisions practically impossible, but anonymous in
`pg_locks` and a second key space next to the categories. Leases in Valkey: a failover can lose a
lock, and fencing on a Valkey lock does not protect PostgreSQL writes ([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), option (d)).

**Spec:** [R-ARCH-017](spec/architecture.md#r-arch-017--session-connections-leases-and-advisory-lock-categories), [R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget), [R-HA-004](spec/ha-and-helm.md#r-ha-004--cross-replica-invalidation-through-the-change-log-and-valkey), [R-HA-005](spec/ha-and-helm.md#r-ha-005--leader-less-scheduling-and-job-leases), [R-HA-006](spec/ha-and-helm.md#r-ha-006--singleton-work-under-advisory-lock-leases-with-fencing),
[R-HA-058](spec/ha-and-helm.md#r-ha-058--transaction-mode-pooler-for-the-main-pool); decided: [Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills), [Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers), [Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler), [Q-STOR-13](spec/storage.md#q-stor-13--exclusion-of-storage-maintenance-runs-job-key-fenced-lease-or-valkey-lock).

### ADR-078 · Restores keep metadata and blobs consistent by ordering, not by coordination

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** PostgreSQL and the bucket are backed up by different mechanisms. A point-in-time restore
of the database must never reference a blob whose object is gone. Revisited on 2026-10-28: trove's
own backup ([ADR-097](#adr-097--consistent-backups-to-s3-restored-into-an-empty-installation-inv-19)) became a second restore path at MVP, and a restore from a scheduled backup
undoes up to one backup interval of changes instead of the minutes a point-in-time restore loses.

**Decision.** For the platform layer, three orderings guarantee it: an object is written before any
reference to it commits; an object is deleted no earlier than the grace period after its last
reference disappeared; and the grace period is at least the declared recovery window, or the bucket
keeps deleted objects as noncurrent versions for that long. PostgreSQL is protected by WAL archiving
and base backups in a bucket other than the blob bucket; the blob bucket by versioning; the
encryption keys outside the cluster. The runtime S3 credential must not be able to delete object
versions or change versioning, lifecycle or bucket policy. A restore of a trove backup gets its
consistency from the garbage-collection hold instead ([ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold)). Both restore paths move Valkey to a
new keyspace and offset every revision column ([ADR-111](#adr-111--a-restore-moves-valkey-to-a-new-keyspace-and-offsets-every-revision)), and both replay the revocation
journal.

**Consequences.** A compromised replica can create delete markers but cannot destroy history.
Restoring the database revives revoked credentials and removed grants ([H-37](spec/ha-and-helm.md#h-37--a-database-restore-revives-revoked-credentials-and-removed-grants)). The platform layer
and the point-in-time restore are MVP and stay a recommendation for installations
([Q-HA-19](spec/ha-and-helm.md#q-ha-19--priority-of-the-platform-backup-layer-and-the-point-in-time-restore), option (c)). Decided: [R-HA-054](spec/ha-and-helm.md#r-ha-054--revocation-journal-in-object-storage) ([Q-HA-9](spec/ha-and-helm.md#q-ha-9--making-revocations-survive-a-database-restore-h-37) option (b); revisited on 2026-10-28: MVP
instead of P1), a write-once revocation journal in object storage that `post-restore` and the
restore of a trove backup replay:
- **Filled by the audit writer.** The audit writer fills the journal's outbox, in the transaction of
  the change, for registry-flagged events (`journal: revocation`). An event type added later is
  covered by its flag, not by new emission code on each page.
- **Replay never grants.** A narrowing is applied only when the result grants nothing beyond the
  object's current state; otherwise the object is suspended. The worst a writer with the runtime
  S3 credential can do is revoke access.
- **CI mappings.** Under first match, disabling a mapping can let its jobs fall through to a
  broader one. A replayed disable therefore also suspends every later enabled mapping of the same
  tier ([Q-CI-11](spec/gitlab-ci-oidc.md#q-ci-11--replaying-a-mapping-disable-under-first-match), option (b)). The choice did not change with the revisit of Q-HA-9 and is MVP
  with the journal.
- **Content withdrawals.** Two formats journal withdrawals as well, and replay deletes nothing:
  npm unpublishes are withheld ([ADR-118](#adr-118--a-restore-withholds-npm-versions-unpublished-after-the-snapshot)), NuGet publishes and deletes become tombstones
  ([ADR-122](#adr-122--a-restore-replays-nuget-publishes-and-deletes-into-tombstones)).
- **Shared event types.** The registry flag means "journaled when the producer supplies an
  entry"; every flagged type requires an entry unless it is one of the allow-listed shared types,
  today `package.published` and `package.deleted` ([Q-OPS-22](spec/operations-and-observability.md#q-ops-22--journal-flag-on-event-types-that-several-producers-share), option (d)).

What stays live: removals whose outbox rows had not been shipped when the old database stopped
(normally less than one shipping interval, default 5 s), removals made while the journal was
disabled, and the whole journal when the blob bucket that holds it is lost together with the
database. The manual replay from the audit log stream is the fallback for those.

**Alternatives considered.** Coordinated snapshots of database and bucket: not offered by the
platforms the chart targets. An application-level export: now trove's own backup ([ADR-097](#adr-097--consistent-backups-to-s3-restored-into-an-empty-installation-inv-19)),
which complements this layer and does not replace its shorter recovery point. Revoking every token
on restore: breaks every pipeline and service account and does not cover permission removals.
Replay from an export of the audit log stream: depends on the operator's log pipeline being
complete, and anyone who can write to it can inject events. Dropping the point-in-time path: the
recovery point would become one backup interval everywhere ([Q-HA-19](spec/ha-and-helm.md#q-ha-19--priority-of-the-platform-backup-layer-and-the-point-in-time-restore)).

**Spec:** [R-HA-023](spec/ha-and-helm.md#r-ha-023--backup-of-postgresql-object-storage-and-keys), [R-HA-024](spec/ha-and-helm.md#r-ha-024--restore-consistency-between-metadata-and-blobs), [R-HA-025](spec/ha-and-helm.md#r-ha-025--restore-procedure), [R-HA-054](spec/ha-and-helm.md#r-ha-054--revocation-journal-in-object-storage), [R-BAK-024](spec/backup-and-restore.md#r-bak-024--revocation-journal-replay-after-a-restore), [R-STOR-024](spec/storage.md#r-stor-024--race-free-garbage-collection); decided:
[Q-HA-9](spec/ha-and-helm.md#q-ha-9--making-revocations-survive-a-database-restore-h-37), [Q-HA-19](spec/ha-and-helm.md#q-ha-19--priority-of-the-platform-backup-layer-and-the-point-in-time-restore), [Q-CI-11](spec/gitlab-ci-oidc.md#q-ci-11--replaying-a-mapping-disable-under-first-match), [Q-OPS-22](spec/operations-and-observability.md#q-ops-22--journal-flag-on-event-types-that-several-producers-share).

### ADR-079 · The chart validates before it renders and never creates secrets

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** A misconfigured installation should fail at `helm install`, not at first traffic, and
generated secrets disappear under `helm template` and GitOps tools.

**Decision.** The chart refuses to render, naming the offending value, when required settings are
missing (base URL, database, bucket, key Secret), when HA is enforced with fewer than two replicas
per role or without a disruption budget, when the `worker` Deployment has no replicas, when the
maximum upload exceeds the spool budget, or when the termination grace period is shorter than the
drain delay plus 15 s. Every secret comes from an existing Kubernetes Secret named in the values.
Since 2026-10-28 it also refuses: a missing backup target while backups are enabled
([Q-HA-17](spec/ha-and-helm.md#q-ha-17--the-backup-cronjob-is-on-by-default)); fewer than three Sentinel addresses under `highAvailability.enforce` ([Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports));
a control endpoint equal to the request endpoint behind a transaction-mode pooler ([Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler));
replica counts above zero while a restore is enabled ([Q-HA-18](spec/ha-and-helm.md#q-ha-18--how-a-restore-runs-in-the-chart)); and any access mode other than
`ReadWriteOnce` or `ReadWriteOncePod` for an ephemeral spool volume ([R-HA-061](spec/ha-and-helm.md#r-ha-061--volumes-nothing-shared-nothing-persistent)).

**Consequences.** First installs need the operator to create Secrets up front, documented with
commands. That includes the encryption key, which the operator creates by hand or through secret
tooling; the rendering error prints the one-line command that creates it ([Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key), option (b)).
No secret is ever rotated by an upgrade by accident.

**Alternatives considered.** Validation at start-up only: the error surfaces in a crash loop instead
of the install command. A chart-generated key kept with Helm's `lookup`, or an opt-in hook Job that
creates it: silent, irreversible loss of every stored credential when the key is regenerated, and
for the hook Job a Secret-write permission the release otherwise never needs.

**Spec:** [R-HA-027](spec/ha-and-helm.md#r-ha-027--encryption-keys-for-sealed-secrets-in-a-replicated-deployment), [R-HA-029](spec/ha-and-helm.md#r-ha-029--fail-fast-chart-validation), [R-HA-030](spec/ha-and-helm.md#r-ha-030--configuration-rendering-and-secret-wiring), [R-HA-061](spec/ha-and-helm.md#r-ha-061--volumes-nothing-shared-nothing-persistent); decided: [Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key),
[Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports), [Q-HA-17](spec/ha-and-helm.md#q-ha-17--the-backup-cronjob-is-on-by-default), [Q-HA-18](spec/ha-and-helm.md#q-ha-18--how-a-restore-runs-in-the-chart), [Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler).

### ADR-100 · Valkey degrades trove but never gates readiness, except for configuration errors

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** INV-12 states that Valkey being unavailable degrades trove and does not make replicas
unready. Open were whether any Valkey condition may hold back a replica's first readiness, as the
object-store startup check does, which failovers deserve an alert, and how an operator flushes
caches when Valkey itself is suspect.

**Decision.**
- **Readiness** ([Q-HA-13](spec/ha-and-helm.md#q-ha-13--valkey-and-readiness), option (d)). Only configuration errors hold back a replica's first
  readiness: a rejected credential (`WRONGPASS`, `NOAUTH`, `NOPERM`), a failed TLS verification,
  or a primary name that no reachable Sentinel knows. Unreachability never does, before or after
  the first readiness. The `valkey` check is otherwise reported as degraded, never gating.
- **Degraded mode** ([R-HA-057](spec/ha-and-helm.md#r-ha-057--valkey-failover-outage-and-degraded-mode)). While Valkey is unavailable, collapsing and negative caching work per
  replica, rate limits fall back to per-replica shares, changes arrive at the change-log poll's
  speed, and signals wait for the next poll.
- **Alerts** ([Q-OPS-15](spec/operations-and-observability.md#q-ops-15--alerting-on-valkey-failovers), option (c)). A single failover raises no alert and stays visible in the
  counter and one log record per replica; repeated failovers and replicas that disagree about the
  primary raise alerts.
- **Flush** ([Q-OPS-16](spec/operations-and-observability.md#q-ops-16--flushing-caches-across-the-installation), option (b) at P1). An installation-wide cache flush works through an
  invalidation time in PostgreSQL that readers check, so it works while Valkey is suspect
  ([R-OPS-047](spec/operations-and-observability.md#r-ops-047--installation-wide-cache-flush)). At MVP, deleting trove's keys in Valkey is stated as always safe ([R-OPS-045](spec/operations-and-observability.md#r-ops-045--valkey-and-coordination-observability)).

**Consequences.** A rollout with a mistyped Sentinel name, password or CA stalls with
`maxUnavailable: 0`, as a wrong bucket does, while a replica started during an outage still becomes
ready. A Sentinel that lost its configuration answers like a wrong primary name, so a broken Valkey
deployment can stall a trove rollout. Operators are paged for failover patterns that harm trove,
including a replica still bound to a demoted primary, not for routine failovers.

**Alternatives considered.** Never gating: a typo keeps every replica degraded for good, noticed
only by an alert. Gating continuously, or the first readiness on any Valkey failure: contradicts
INV-12, and a replica started during an outage would remove capacity. An alert on every failover:
noise for normal operation. A flush that deletes Valkey keys only: it would depend on the store that
is suspect.

**Spec:** [R-HA-007](spec/ha-and-helm.md#r-ha-007--failure-mode-contract), [R-HA-008](spec/ha-and-helm.md#r-ha-008--readiness-and-liveness-under-dependency-failure), [R-HA-056](spec/ha-and-helm.md#r-ha-056--valkey-with-sentinel-connection-and-supported-topologies), [R-HA-057](spec/ha-and-helm.md#r-ha-057--valkey-failover-outage-and-degraded-mode), [R-OPS-001](spec/operations-and-observability.md#r-ops-001--liveness-and-readiness-probes),
[R-OPS-045](spec/operations-and-observability.md#r-ops-045--valkey-and-coordination-observability), [R-OPS-047](spec/operations-and-observability.md#r-ops-047--installation-wide-cache-flush); decided: [Q-HA-13](spec/ha-and-helm.md#q-ha-13--valkey-and-readiness), [Q-OPS-15](spec/operations-and-observability.md#q-ops-15--alerting-on-valkey-failovers), [Q-OPS-16](spec/operations-and-observability.md#q-ops-16--flushing-caches-across-the-installation).

### ADR-105 · Drain capacity: one replica more than the peak needs, per role

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** INV-12 requires at least two replicas per role and states that the load exceeds one
replica; a node drain must cause no outage when the setup is sized for it. Kubernetes evicts before
the replacement exists, so the remaining replicas carry the peak alone for a while.

**Decision.** The defaults are three `web` and two `worker` replicas
(`web.replicaCount: 3`, `web.autoscaling.minReplicas: 3`), and the N + 1 rule of [R-HA-059](spec/ha-and-helm.md#r-ha-059--node-drain-without-outage) is
documented and tested: for each role and pool, the minimum replica count is at least the replicas
the peak needs plus the `maxUnavailable` of its disruption budget ([Q-HA-16](spec/ha-and-helm.md#q-ha-16--default-replica-counts-and-the-capacity-rule-for-drains), option (b)). Each
role's budget evicts one pod at a time, and both roles spread over nodes with
`nodeTaintsPolicy: Honor`, so a replacement can schedule off a cordoned node.

**Consequences.** The default installation survives a drain at a peak that two `web` replicas
carry, and the drain test runs with the defaults. A `worker` drain delays queued jobs for a moment
and fails no request. The capacity of one replica is unknown until the reference load test
publishes it; until then the rule is a documented calculation, not a validation.

**Alternatives considered.** Two per role: a drain leaves less capacity than the load needs. A
capacity value the chart validates: an unmeasured number that gives false confidence; worth it once
the load test publishes the capacity of one replica. A budget with `minAvailable` equal to the
peak: blocks drains whenever the replica count equals the peak.

**Spec:** [R-HA-031](spec/ha-and-helm.md#r-ha-031--values-reference), [R-HA-032](spec/ha-and-helm.md#r-ha-032--replicas-disruption-budget-spreading-and-autoscaling), [R-HA-050](spec/ha-and-helm.md#r-ha-050--sizing-guidance-and-profiles), [R-HA-059](spec/ha-and-helm.md#r-ha-059--node-drain-without-outage); decided:
[Q-HA-16](spec/ha-and-helm.md#q-ha-16--default-replica-counts-and-the-capacity-rule-for-drains).

## Operations and observability

### ADR-080 · Audit records commit with the change they record

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** An audit trail that can miss changes is not an audit trail; one that blocks requests
under a flood of denials is an outage.

**Decision.** Events about state changes (configuration, identities, tokens, permissions, content
writes) are inserted in the same transaction as the change; if the insert fails, the change rolls
back. Access events (logins, denials, rejected tokens, failed exchanges) go through a bounded
in-process buffer written in batches; when it is full, the event still goes to the log stream and a
drop counter increases. Denials and rejected tokens are deduplicated per window by writing a first
event and one summary event that refers to it (`repeat_of`), so no audit row is ever updated.
Refused reads are audited by default and allowed reads only with `authz.audit.reads: all`
([Q-AUTHZ-5](spec/authorization.md#q-authz-5--default-audit-level-for-reads), option (b); revisited on 2026-10-28, unchanged: Valkey now offers the deduplication
store that first-read sampling lacked, but a Valkey round trip on every allowed read and the
installation-wide write volume of `all` still decide for refusals only); anonymous denials are
recorded only when enabled. After commit, each event is also written to the log stream; the
database copy is authoritative. Deduplication stays per replica ([Q-OPS-14](spec/operations-and-observability.md#q-ops-14--audit-deduplication-across-replicas), option (a)): with n
replicas, a key and window produce at most n first events and n summaries, and the sum of
`details.count` stays exact, because an audit record must not depend on a store whose loss the
design accepts.

Audit rows are append-only for trove's runtime role, which may only `INSERT` and `SELECT` them
([Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records), option (c)):
- **Partitions.** An owner-held `SECURITY DEFINER` function, `audit_maintain_partitions()`, creates
  and drops the partitions, serialized by a row lock on `audit_policy`. New partitions are attached
  with `ATTACH PARTITION`, which inserts do not wait for. The function runs under a 2-second
  `lock_timeout` and leaves a drop it cannot lock to the next call.
- **Retention** is owner-written data in `audit_policy`. A change takes effect with `trove migrate`.
- **Calls.** `trove migrate`, every replica at start-up and a daily task call the function. The
  `startup` check requires partitions for the current and the next month.

**Consequences.** No change exists without its record. Allowed downloads are not attributable by
default ([H-23](spec/authorization.md#h-23--allowed-downloads-are-not-attributable-by-default)); one setting makes them so, at the cost of an audit write per download. A compromised
replica or a SQL injection, acting as the runtime role, cannot rewrite or erase history, and a long
audit export never makes transactional writes queue behind partition maintenance. The owner,
superusers and single-role installations are outside that protection; shipping the `log: audit`
stream to an append-only store covers them ([H-39](security/audit.md#h-39--audit-records-are-not-tamper-evident)).

**Alternatives considered.** Writing every event asynchronously: changes without records after a
crash. Writing every event synchronously: a denial flood blocks request handling. Auditing every
read by default: one `npm ci` issues hundreds of requests, and audit rows would become the largest
write load of an HA installation. Refused reads plus one sampled event per principal, version and
day: a cluster-wide deduplication store on the hottest path. A hash chain over audit rows, or sealed
checkpoints: weighed in [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16).

**Spec:** [R-OPS-015](spec/operations-and-observability.md#r-ops-015--audit-recording), [R-OPS-016](spec/operations-and-observability.md#r-ops-016--audit-event-catalog), [R-OPS-017](spec/operations-and-observability.md#r-ops-017--audit-query-export-and-retention), [R-AUTHZ-029](spec/authorization.md#r-authz-029--audit-of-authorization-decisions); decided: [Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records),
[Q-AUTHZ-5](spec/authorization.md#q-authz-5--default-audit-level-for-reads), [Q-OPS-14](spec/operations-and-observability.md#q-ops-14--audit-deduplication-across-replicas).

### ADR-081 · Configuration as code: documents own their objects; plan before apply (P1)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Platform teams manage repositories, groups, policies and CI mappings in Git and expect a
reviewable plan before anything changes.

**Decision.** A configuration document declares database-held configuration and owns every object
it declares; with pruning on, owned objects no longer declared are deleted. A diff returns a plan
with a hash; apply can require that hash, refuses conflicts (objects owned by nobody need explicit
adoption) and blocks destructive changes without an explicit flag. At start-up, replicas can apply
documents under an advisory lock; destructive changes are never applied at start-up, and a failing
document keeps the replica unready so a rollout stalls while old replicas serve. Export turns an
existing installation into a document. Further rules:
- **Document kinds.** Documents declare repositories, groups, policies and the CI kinds `ciIssuers`
  and `ciMappings`. A mapping names its `issuer`, and the CI kind is never called a provider, so
  that no reader takes it for a login provider.
- **No login configuration.** Login providers, their claim mapping and claim-to-group rules are
  never document content. They live only in the configuration file, and a `loginProviders` key or a
  claim-mapping field on a group answers `422` ([Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents), option (a); [ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15)).
- **Locking.** Managed objects are locked by default (`lock_managed_objects: true`, one global
  switch). API and UI changes to them answer `409`, naming the owning document
  ([Q-OPS-8](spec/operations-and-observability.md#q-ops-8--locking-objects-managed-by-configuration-documents), option (a)). The emergency path leads through the source of truth: correct the
  document and apply it through the API, or through the CLI's database mode when nobody can log in;
  `adopt` moves ownership.

**Consequences.** One reviewable path for configuration changes; a broken document cannot take over a
running installation. A bad apply cannot remove the only working login, and no apply can strip the
rule that makes someone an administrator. A UI fix is never silently reverted by the next pipeline
run, because the lock refuses it and names the document instead.

**Alternatives considered.** Imperative scripts against the API: no plan, no drift detection.
Kubernetes custom resources: configuration bound to cluster permissions. Login providers, or only
their claim-to-group rules, as a document kind: one apply, also through the API without a Git
review, could lock every OIDC administrator out. Unlocked managed objects: an emergency UI fix is
reverted by the next pipeline run. Per-document locking, or an audited administrator override per
edit: a second place to look, or the same revert one step later.

**Spec:** [R-OPS-029](spec/operations-and-observability.md#r-ops-029--configuration-as-code-document), [R-OPS-030](spec/operations-and-observability.md#r-ops-030--diff-and-apply), [R-OPS-031](spec/operations-and-observability.md#r-ops-031--startup-bootstrap-from-configuration-documents), [R-OPS-032](spec/operations-and-observability.md#r-ops-032--configuration-export), [R-API-044](spec/api-and-ui.md#r-api-044--configuration-as-code); decided:
[Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents), [Q-OPS-8](spec/operations-and-observability.md#q-ops-8--locking-objects-managed-by-configuration-documents).

### ADR-082 · Events through a transactional outbox, as CloudEvents, signed per Standard Webhooks (P1)

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Webhook receivers need every committed change exactly as it happened and nothing from
rolled-back transactions, in a format they can parse with existing libraries.

**Decision.** A webhook-capable change inserts an outbox row in its own transaction; without an
enabled subscription for the domain no row is written. Dispatch claims rows with `SKIP LOCKED` and
delivers through the job queue with retries. The body is a CloudEvents 1.0 event in structured JSON
mode; requests are signed following the Standard Webhooks specification. Redirects are not followed,
and target addresses pass the outbound address checks, with private ranges denied by default. The
CloudEvents `type` is `trove.` followed by the registry name, for example
`trove.package.published` ([Q-OPS-5](spec/operations-and-observability.md#q-ops-5--cloudevents-type-prefix), option (a)).

Templated request bodies and headers use Go's `text/template`, restricted
([Q-OPS-11](spec/operations-and-observability.md#q-ops-11--template-language-conditionals-loops-and-functions), option (b)):
- **Allowed.** `if`, `else`, `with`, variables, and at most 16 non-nested `range` actions over event
  data.
- **Functions.** A fixed set without `call`, with number-aware comparisons, plus `json`, `yaml`,
  `base64` and `default`.
- **Data** is passed as JSON with `json.Number` numbers, so a `range` over a number fails and numbers
  print exactly.
- **Rejected on save.** `define`, `template`, `block`, nested `range`, and `range` over literals,
  function results or variables.

**Consequences.** Events cost nothing until someone subscribes. A new subscription receives events
committed after it was enabled. The `type` prefix is frozen from the first release that ships
webhooks (P1); receivers match it literally on every installation, and `source` (the base URL)
identifies the producing installation. With arrays capped at 1,000 items by the event schemas, one
render is bounded to 16,000 iterations, so a template cannot tie up a delivery worker.

Delivery on n `worker` replicas, decided on 2026-10-28:
- **Per-subscription limit** ([Q-OPS-19](spec/operations-and-observability.md#q-ops-19--per-subscription-delivery-limit-across-worker-replicas), option (b)). The claim of a `webhook.deliver` job locks its
  subscription's row with `SKIP LOCKED`, counts the running deliveries and claims only below the
  limit, so the limit protects a receiver exactly, whatever the `worker` count, without Valkey.
- **Unrecorded success** ([Q-OPS-20](spec/operations-and-observability.md#q-ops-20--final-event-for-a-successful-delivery-that-is-not-recorded), option (b)). A success that is not recorded leaves a tombstone row
  with its final state for `webhooks.delivery_log.tombstone_retention` (15 minutes), which only the
  follow stream shows.
- **Follow-stream position** ([Q-OPS-21](spec/operations-and-observability.md#q-ops-21--position-of-the-delivery-log-follow-stream), option (b)). The stream's position is `change_seq`, taken from
  a sequence on every insert and update, with a re-read window behind the cursor and duplicates
  dropped by record and version, so a reconnect on any replica never skips a committed event.

**Alternatives considered.** Publishing after commit from request code: events lost on a crash
between commit and send. A custom envelope: every receiver hand-written. A reverse-DNS `type`
prefix: no project domain exists, and a code-hosting namespace or a per-installation prefix fails
stability or uniformity. A minimal template subset: almost the same validator, without useful
conditionals. A large third-party function library: functions that read the process environment
would expose trove's own secrets. A different template language: unfamiliar to receiver authors
and awkward for form-encoded and plain-text bodies.

**Spec:** [R-OPS-035](spec/operations-and-observability.md#r-ops-035--event-outbox-and-dispatch), [R-OPS-036](spec/operations-and-observability.md#r-ops-036--event-envelope), [R-OPS-040](spec/operations-and-observability.md#r-ops-040--request-signing), [R-OPS-041](spec/operations-and-observability.md#r-ops-041--templated-requests-and-handler-secrets), [R-OPS-042](spec/operations-and-observability.md#r-ops-042--delivery-retries-and-outbound-protection); decided:
[Q-OPS-5](spec/operations-and-observability.md#q-ops-5--cloudevents-type-prefix), [Q-OPS-11](spec/operations-and-observability.md#q-ops-11--template-language-conditionals-loops-and-functions), [Q-OPS-19](spec/operations-and-observability.md#q-ops-19--per-subscription-delivery-limit-across-worker-replicas), [Q-OPS-20](spec/operations-and-observability.md#q-ops-20--final-event-for-a-successful-delivery-that-is-not-recorded), [Q-OPS-21](spec/operations-and-observability.md#q-ops-21--position-of-the-delivery-log-follow-stream).

## Management API and UI

### ADR-083 · The OpenAPI 3.1 document is the API contract

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The UI, scripts and configuration tools all use the management API. Drift between
documentation, server and clients breaks them silently.

**Decision.** One OpenAPI 3.1 document describes every `/api/v1` operation and the raw file path
`/files/…`; package protocols follow their ecosystems' specifications. It is served without
authentication. Every operation declares a stable `operationId`, its authorization and its errors. CI
fails on breaking changes within v1.

The generation approach is decided ([Q-API-1](spec/api-and-ui.md#q-api-1--openapi-generation-approach), option (a)). The document is written by hand:
- **Go.** oapi-codegen (v2.8 or later) generates the models and the `std-http-server` interface into
  `internal/api/v1/gen/`, registered on `net/http.ServeMux`. The `/files` operations are excluded.
  The strict-server variant is not used, because handlers set `ETag`, `Location`, `Link` and
  `Retry-After` themselves and stream answers.
- **TypeScript.** openapi-typescript (7.x, pinned in the lockfile) generates types into
  `web/src/app/api/generated/`, used by thin hand-written Angular clients on `httpResource()`.
- **Construct fixture.** oapi-codegen calls its 3.1 support initial, so a fixture document with
  every construct the contract uses is generated in CI. A construct the generator mishandles is
  written in an equivalent form, for example `enum: [x]` for `const: x`.
- **Authorization.** The authorization step reads each operation's declaration from the embedded
  document by `http.Request.Pattern`. A route without one stops start-up, so permission enforcement
  never depends on the generator.
- **Fallback.** Generated types only, with routes and parameter binding written by hand, the
  document and the TypeScript side unchanged. It applies if a construct has no equivalent form or
  the generator's 3.1 line stops moving.

**Consequences.** The contract can be reviewed before code exists. Drift between document and
server, and between document and UI, is a compile error, and a missing handler does not compile.

**Alternatives considered.** Code-first generation: inverts the source of truth and ties the API to a
framework. A generated Angular service layer: observable-based services between the UI and the
signals and `httpResource()` it is built on, and one more generator to move with every Angular
major. Types only from the start: hand-written parsing and registration for every operation, with a
forgotten handler found in CI instead of at compile time.

**Spec:** [R-API-001](spec/api-and-ui.md#r-api-001--base-path-versions-and-compatibility), [R-API-002](spec/api-and-ui.md#r-api-002--openapi-31-document-as-the-contract), [R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy); decided: [Q-API-1](spec/api-and-ui.md#q-api-1--openapi-generation-approach).

### ADR-084 · Uniform API conventions: problem details, cursors, merge patch, ETags, idempotency keys

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Consistent conventions make every resource predictable for scripts and generated
clients.

**Decision.** The conventions:
- **Errors.** Errors under `/api/v1`, `/auth` and `/files` are RFC 9457 problem details with a
  stable code whose description is served by every installation. Package protocol endpoints keep
  their ecosystems' error formats.
- **Lists.** Every list endpoint uses keyset pagination with `limit` (default 50, maximum 500) and
  `cursor`, the envelope `items` and `nextCursor`, and a `Link` header. There are no per-page
  variants ([Q-API-6](spec/api-and-ui.md#q-api-6--pagination-parameter-names), option (a)). Cursors are self-contained (INV-18): each page is one keyset
  query in its own transaction, with no server-side cursor or stored result, so consecutive pages
  may reach different replicas. Cursor versions follow expand/contract, because the pages of one
  traversal may alternate between replicas of N and N-1 during a rolling upgrade.
- **Updates.** PATCH bodies are JSON Merge Patch (RFC 7396); JSON Patch is not offered. Mutable
  resources carry a revision as strong ETag. `If-Match` is required on every PUT and PATCH of a
  resource that carries a revision ETag, and a request without it answers `428`. `If-Match: *` is
  the explicit blind write. DELETE and delta-style edits (`add`/`remove` lists, role bindings,
  assignments) are exempt, because they cannot overwrite another writer's change
  ([Q-API-2](spec/api-and-ui.md#q-api-2--is-if-match-mandatory-on-writes), option (b)).
- **Creates.** Creating and job-starting POSTs and job-starting DELETEs accept `Idempotency-Key`;
  the keys live in PostgreSQL, so a retry replays, waits or runs again on whichever replica it
  reaches. A concurrent duplicate waits at most 1 s, then answers 409 `idempotency_in_progress`
  with `Retry-After`.
- **Actions.** Every action, on an item or on a collection, is a kebab-case sub-path verb, such as
  `POST /api/v1/repositories/batch-create`. A literal segment in a key position is a reserved name
  of that collection: declared in the OpenAPI document, refused as a key with
  `422 reserved_name`, and checked in CI ([Q-API-7](spec/api-and-ui.md#q-api-7--syntax-of-custom-methods), option (a)).

**Consequences.** One client library pattern for every resource; the UI is safe against double
submits and lost updates. A stale write cannot silently revert a permission policy, the member
order of a virtual repository or the conditions of a CI mapping, which decide what clients receive
and who may publish. Every script that writes configuration sends one more header. The rule is
derived from the document, with no switch, so there is one behavior to test. Five repository keys
become reserved names (`batch-create`, `batch-update`, `batch-get`, `batch-delete`,
`quick-setup`). Literals that already sat in key positions, such as `me` for users or `deliveries`
for webhooks, are now reserved instead of silently shadowing a key. The pages that used other
parameter names or action syntax are aligned with this.

**Alternatives considered.** Offset pagination: unstable under concurrent writes. `pageSize` and
`pageToken`: every other page would change. `If-Match` optional unless the owning page requires
it, or required only on grant-defining resources: each depends on someone recognizing every field
that is dangerous to lose, and a stale write of a virtual repository's members is a
dependency-confusion path. Colon methods or a segment the key grammar excludes for collection
actions: a second URL shape, while the reserved-name rule is needed anyway. A batch resource:
operations with different status and atomicity behind one discriminated body.

**Spec:** [R-API-004](spec/api-and-ui.md#r-api-004--resource-naming-identifiers-and-methods), [R-API-009](spec/api-and-ui.md#r-api-009--errors-as-problem-details), [R-API-010](spec/api-and-ui.md#r-api-010--cursor-pagination), [R-API-012](spec/api-and-ui.md#r-api-012--optimistic-concurrency-with-etag-and-if-match), [R-API-013](spec/api-and-ui.md#r-api-013--partial-updates-with-json-merge-patch), [R-API-014](spec/api-and-ui.md#r-api-014--idempotency-keys-for-creates);
decided: [Q-API-2](spec/api-and-ui.md#q-api-2--is-if-match-mandatory-on-writes), [Q-API-6](spec/api-and-ui.md#q-api-6--pagination-parameter-names), [Q-API-7](spec/api-and-ui.md#q-api-7--syntax-of-custom-methods).

### ADR-085 · Long-running work answers 202 with a job

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Deleting a large repository, reindexing or applying configuration outlives an HTTP
request and a replica restart.

**Decision.** Such work answers 202 with the job's location and representation. Jobs survive replica
restarts, are visible from every replica and can be cancelled. Owning pages may add a read-only
domain view of their runs that references the job. Every singleton maintenance task has exactly one
start path, `POST /api/v1/system/tasks/{name}/run`, and one stop path, job cancel
([Q-API-9](spec/api-and-ui.md#q-api-9--one-path-for-starting-stopping-and-reading-maintenance-work), option (c); [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work)). Per-resource actions and retention-policy runs keep their own
resource paths.

**Consequences.** Clients poll one job API for every kind of long-running work. "Run now" on the
system status screen works for every task without a special case, and a run that is active answers
409 the same way everywhere.

**Alternatives considered.** Synchronous calls with long timeouts: break at proxies and on replica
restarts. Domain start and stop paths per task: a trigger and a stop mechanism per task. Generic
paths with no domain views: domain results such as reclaimed bytes per run end up in generic job
results.

**Spec:** [R-API-015](spec/api-and-ui.md#r-api-015--long-running-work-as-jobs), [R-API-020](spec/api-and-ui.md#r-api-020--resource-map-and-routing), [R-API-040](spec/api-and-ui.md#r-api-040--garbage-collection-and-storage-summary), [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-OPS-019](spec/operations-and-observability.md#r-ops-019--scheduled-maintenance-tasks-and-job-visibility); decided:
[Q-API-9](spec/api-and-ui.md#q-api-9--one-path-for-starting-stopping-and-reading-maintenance-work).

### ADR-086 · UI baseline: Angular 22 defaults, a strict CSP, nothing from foreign origins

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The UI shares its origin with package content ([ADR-030](#adr-030--one-origin-for-ui-api-and-package-content)) and must work air-gapped.

**Decision.** Standalone components only, zoneless change detection, `OnPush`, strict TypeScript and
templates. Signals are the state primitive; server state is read with `httpResource()`, with no global
cache of server data. UI documents carry a CSP with a per-response nonce and Trusted Types, so every
`bypassSecurityTrust*` call fails. The UI requests nothing from other origins: fonts and icons are
self-hosted, no analytics, no CDN. Initial JavaScript is budgeted at 400 kB compressed (proposed);
every feature is a lazy route.

Components ([Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library), option (a)):
- **Library.** Angular Material and the CDK in the release line of the Angular major, plus
  `@angular/aria` for the file tree and the member editor's lists, themed through design tokens.
- **Icons** come from a self-hosted font used through ligatures. SVG icon registration through
  `MatIconRegistry` is excluded: it takes URLs only from a `bypassSecurityTrust*` value and inserts
  markup through the CDK's Trusted Types policy `angular#components`, which the CSP's
  `trusted-types` list does not name.

Shell ([Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested), option (c)):
- **Navigation.** A left side bar pinned from 1024 CSS px and a drawer below.
- **Density.** Compact density (Material density `-2`) everywhere, which keeps targets above the
  24 CSS px of WCAG 2.5.8.
- **Layouts.** One- or two-column layouts switch by container queries on the content area.

Two further screens follow the same approach:
- **Package details** ([Q-UI-3](spec/api-and-ui.md#q-ui-3--layout-of-the-package-details-page-example-screenshot-requested), option (c)). Two columns with a sticky install panel from 960 CSS px
  of content width, otherwise one column with the install panel directly below the header.
- **Policy editor** ([Q-UI-4](spec/api-and-ui.md#q-ui-4--layout-of-the-policy-editor-and-the-explainer-example-screenshot-requested), option (a)). One page for create and edit, with an in-page table of
  contents and a sticky preview fed by validation. The explainer is its own screen, opened in a new
  tab.

**Consequences.** Script injection through Angular's escape hatches is closed by the browser, and
the icon choice keeps it closed without widening the CSP for a cosmetic gain. The widget layer
ships in lockstep with Angular, one version of every dependency. Collapsing or pinning the side bar
never breaks a screen, and the UI works down to 320 CSS px. The owner's example screenshots are no
longer a precondition; the walkthrough takes their place and adjusts tokens and spacing, not the
structure.

**Alternatives considered.** A looser CSP for third-party widgets: widens the attack surface on the
shared origin. A third-party component suite: majors that trail Angular's, and another large
dependency whose CSP behavior must be verified. Own components on the CDK only: weeks of widget
work before the first screen. Top navigation: two areas with 25 routes do not fit a top bar.
Breakpoints on the viewport: every two-column screen would carry the shell's layout logic. A
navigation rail: a second navigation design for an audience that scans by name. A wizard for
policies: hides two of principals, resources and actions at every step.

**Spec:** [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery), [R-UI-002](spec/api-and-ui.md#r-ui-002--application-shell), [R-UI-007](spec/api-and-ui.md#r-ui-007--package-details-and-versions), [R-UI-027](spec/api-and-ui.md#r-ui-027--permission-policy-list-and-editor), [R-UI-046](spec/api-and-ui.md#r-ui-046--state-management-with-signals); decided:
[Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library), [Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested), [Q-UI-3](spec/api-and-ui.md#q-ui-3--layout-of-the-package-details-page-example-screenshot-requested), [Q-UI-4](spec/api-and-ui.md#q-ui-4--layout-of-the-policy-editor-and-the-explainer-example-screenshot-requested).

### ADR-087 · READMEs rendered on the server through an allowlist sanitizer

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Package READMEs are author-controlled Markdown with embedded HTML, displayed on the UI's
origin.

**Decision.** The server renders CommonMark with common extensions and sanitizes the result against a
fixed allowlist of elements and attributes. Links are limited to `http`, `https`, `mailto` and
fragments; `id` and `name` are kept only with a `user-content-` prefix; images pointing at trove's
own origin are dropped, so a README cannot make the viewer's browser send authenticated requests.
External images are blocked by default and replaced by a link. An operator can list exact `https`
origins in `ui.readme.external_image_origins`, default empty. The sanitizer keeps images from
those origins, and the same list is mirrored into the UI's CSP `img-src`, so the browser enforces
exactly what the sanitizer allows ([Q-UI-6](spec/api-and-ui.md#q-ui-6--external-images-in-readmes), option (d)).

**Consequences.** One renderer and one policy for every client. Air-gapped and cautious
installations lose nothing; others enable exactly the hosts they trust, typically an internal
GitLab or badge service. A sanitizer bug cannot load an image from an unlisted host, and the
cross-origin request carries no page URL, because UI documents use
`Referrer-Policy: same-origin`. Badges of public packages from unlisted hosts still show as links.
The README cache stores the hash of the list.

**Alternatives considered.** Rendering in the browser: a sanitizer per client. Proxying external
images through trove: a server-side request forgery surface controlled by package authors.
Allowing every `https` image: every viewer's address and the package name reach any host a package
author picks, including tracking pixels in public packages. Blocking always: internal badges and
diagrams never show, even from hosts the operator trusts.

**Spec:** [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery), [R-UI-008](spec/api-and-ui.md#r-ui-008--sanitized-readme-rendering), [R-API-029](spec/api-and-ui.md#r-api-029--packages-versions-and-readme); decided: [Q-UI-6](spec/api-and-ui.md#q-ui-6--external-images-in-readmes).

### ADR-088 · English and German in the MVP

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** The owner's users read English and German.

**Decision.** English (source and default) and German ship with the MVP. Every UI text is
translatable, sentences are never concatenated from fragments, and counts use ICU plural and select
messages. Locale choice: user preference, then `Accept-Language`, then the configured default. API
texts stay English; the UI translates problem codes.

The mechanism is decided: Angular's compile-time i18n, `@angular/localize`, with one build per
locale under `/ui/{locale}/` and no runtime translation library; switching the language reloads the
route under the other locale ([Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism), option (a)).

**Consequences.** Every UI change carries its German text, and a missing translation fails the
build instead of reaching a German user. No dependency outside Angular's release line and no
runtime cost. Each locale adds a build to the binary, and UI URLs carry a locale prefix.

**Alternatives considered.** English only: excludes part of the intended audience. A runtime
translation library: one build and an instant switch, but string keys whose typos compile, and a
dependency outside Angular's release line. `@angular/localize` with translations loaded at run time:
the build-time check of missing translations would have to be rebuilt as a separate step.

**Spec:** [R-UI-045](spec/api-and-ui.md#r-ui-045--internationalization), [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery); decided: [Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism).

## Foundation (M0)

### ADR-125 · GNU Make and one pin file for every tool

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Phase 1 (M0) gave the repository its first commands and CI jobs. Local builds and CI
had to run the same commands with the same tool versions, and no contributor should need a second
task runner to build the project.

**Decision.** GNU Make is the task runner: the root `Makefile` holds every target of
[ha-and-helm.md](spec/ha-and-helm.md) R-HA-051 and of the build, test and chart steps. Every tool and
image pin that is not a Go module, an npm package or a Node version lives in `tools/versions.env`
as `KEY=value` lines. The Makefile includes that file, and CI loads it through the `load-pins` action.
Go is pinned by `go.mod`, sqlc and oapi-codegen by `tool` directives, Angular by `web/package-lock.json`
and Node by `web/.nvmrc`.

**Consequences.** A version change is one line that the local build and CI both read. `make versions-check`
compares the installed tools with the pins. A stale binary of a tool is detected by that check, and the
linter binary's name carries its pin and toolchain, so a change rebuilds it. Windows contributors need WSL
or a Make port.

**Alternatives considered.** Taskfile (one more pinned tool), mage (a second Go toolchain for the build
scripts), just (one more binary to install). Decided in
[Q-HA-21](spec/ha-and-helm.md#q-ha-21--task-runner-and-tool-pin-file).

**Spec:** [Q-HA-21](spec/ha-and-helm.md#q-ha-21--task-runner-and-tool-pin-file), [R-HA-051](spec/ha-and-helm.md).

### ADR-126 · Problem details with a relative type, a requestId member and named error codes

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** Every error on `/api/v1/` must be readable by a client that has never seen the installation.
Two details had drifted between the spec and the code: the request-ID member's spelling, and the set of
codes the statement timeout and the concurrency bound answer with.

**Decision.** The problem `type` is the relative reference `/api/v1/problems/<code>`, which trove serves from
its embedded code registry, so it is the same on every installation. The extension member is `requestId`,
equal to the `X-Request-Id` header. A request that cannot be parsed answers 400 `malformed_request`; a request
whose body parsed and whose fields are invalid answers 422 `validation_failed` with `errors`. A replica at its
concurrency bound answers 503 `server_busy`, and a statement over its time limit 503 `database_timeout`.

**Consequences.** A code is never renamed or reused within an API major version (R-ARCH-025), so the codes
above are frozen at their first release. The UI and every client match on `code`. `requestId` keeps the camel
case of the rest of the API's JSON.

**Alternatives considered.** An absolute `type` on the public base URL (differs per installation); `about:blank`
(no catalog); a snake-case `request_id` member (inconsistent with the API's JSON); `invalid_request` (the OAuth
form of the token endpoint, R-CI-008) and `too_many_requests_in_flight` (a `429`-style name for a `503` case). Decided in
[Q-API-13](spec/api-and-ui.md#q-api-13--problem-member-name-problem-type-status-of-validation-errors-and-error-codes).

**Spec:** [Q-API-13](spec/api-and-ui.md#q-api-13--problem-member-name-problem-type-status-of-validation-errors-and-error-codes),
[R-ARCH-023](spec/architecture.md#r-arch-023--error-model), [R-API-009](spec/api-and-ui.md).

### ADR-127 · Transport encryption on every connection, trove's listeners included

**Status:** Accepted (owner constraint, 2026-10-29); it is the record of INV-20

**Context.** On 2026-10-29 the owner required that every component of this repository can run fully
TLS-encrypted: PostgreSQL, Valkey and Sentinel, the object store, the outbound HTTP clients, trove's own listeners
(so that a reverse proxy can connect internally over TLS), and the chart's probes and monitoring. Certificates may be
rotated while the service runs, and trove must pick up its own rotated certificates without a restart. Before this,
the design left plain HTTP inside the cluster as the default (H-38), and the CA bundles were read once at start-up.

**Decision.** INV-20: every connection that trove or a component of this repository makes or accepts can run with TLS,
trove's listeners included. TLS material that trove presents or trusts is reloaded without a restart; the reload bound
is `server.tls.reload_interval` for the listeners and the bounds of R-ARCH-035 for the clients. The chart defaults to
`tls.enabled: true` and refuses to render without the certificate Secret, unless `tls.enabled: false` is set, which
keeps H-38 open for that installation only.

**Consequences.** Certificate handling is part of every component: a reloader for the listeners, a re-read per
connection or handshake for the clients, and expiry metrics. The dev profile and the integration stack run every
connection over TLS, which makes the tests exercise the same paths as production. Plain HTTP stays possible as an
explicit chart setting, not as a design assumption.

**Alternatives considered.** TLS ending at the edge only, with plain HTTP inside the cluster (the earlier design, now
limited to `tls.enabled: false`); a service mesh as the only encryption (it stays a platform option, not a requirement);
restart-based rotation (rejected by the owner constraint). Decided in
[Q-HA-32](spec/ha-and-helm.md#q-ha-32--default-of-the-charts-tls-wiring),
[Q-ARCH-31](spec/architecture.md#q-arch-31--how-tls-material-is-reloaded),
[Q-ARCH-32](spec/architecture.md#q-arch-32--client-certificates-on-troves-client-listener) and
[Q-ARCH-33](spec/architecture.md#q-arch-33--one-certificate-pair-for-both-listeners-or-one-pair-each).

**Spec:** [INV-20](spec/README.md#design-invariants), [R-ARCH-035](spec/architecture.md),
[R-HA-063](spec/ha-and-helm.md), [H-38](spec/ha-and-helm.md).

## Endpoints

### ADR-128 · Endpoints: one listener each, identified by the listener, with exposure rules as a ceiling

**Status:** Decided by the development agent — owner review at the walkthrough

**Context.** On 2026-10-29 the owner asked for several endpoints, so that a registry with internal
artifacts can be offered to customers through an external endpoint, with detailed control over
which packages are retrievable there. He also suspected that some registries need a wildcard
Ingress. INV-6 knew one client listener, INV-9 one ceiling (the token scope), and INV-15 nothing
about entry points.

**Decision.** An endpoint is a named client listener with its own hosts (exact names and at most
one single-label wildcard), base URL, certificates chosen by SNI, optional web pool, surface,
accepted credentials, exposure mode, cache-fill policy and rate-limit class. The endpoint of a
request is the listener's, never derived from `Host` or forwarded headers; a host outside the
endpoint's set answers `421`. Network binding is file configuration; exposure rules live in
PostgreSQL, are compiled into the grant index, and act in the one decision point as a ceiling for
every subject, administrators included: effective = grants ∩ token scope ∩ exposure. Hidden content
answers `404`, writes on an endpoint with rules `403`. Further endpoints default to package routes
only, bound service-account tokens, no anonymous access and no cache fill. A host may bind its root
to one repository, a mechanism built in the MVP for Docker Engine mirrors, OCI subdomains and
Terraform discovery later. Path-based routing stays the default; no MVP format needs a wildcard.

**Consequences.** One more port, Service, edge route and NetworkPolicy per endpoint, rendered by the
chart. Every format must implement an exposure filter before it can be served on an endpoint with
rules, and its ETags and caches include the endpoint. Exposure narrowings are journaled for restores.
The listener boundary is only as good as the edge routing: traffic routed to port 8080 gets the
default endpoint ([H-59](spec/endpoints.md#h-59--traffic-routed-to-the-default-endpoints-port-bypasses-exposure)), and every endpoint runs with a `web` replica's credentials
([H-57](spec/endpoints.md#h-57--a-further-endpoint-runs-with-every-credential-of-a-web-replica)).

**Alternatives considered.** Endpoints by `Host` on one listener (the client chooses the endpoint);
by TLS server name (absent behind re-encrypting edges); by path prefix (changes every format's URL
layout); exposure in the configuration file (a rollout per release decision); exposure bypassed by
`content:bypass` (a leaked administrator token opens the external endpoint); `403` for hidden content
(an existence oracle). Decided in
[Q-EP-1](spec/endpoints.md#q-ep-1--how-the-endpoint-of-a-request-is-identified) to
[Q-EP-13](spec/endpoints.md#q-ep-13--sign-in-and-the-ui-on-further-endpoints).

**Spec:** [spec/endpoints.md](spec/endpoints.md), [INV-6, INV-9, INV-15](spec/README.md#design-invariants),
[R-ARCH-005](spec/architecture.md#r-arch-005--http-listeners-routing-and-middleware-chain),
[R-AUTHZ-015](spec/authorization.md#r-authz-015--evaluation-algorithm),
[R-AUTH-024](spec/identity-and-authentication.md#r-auth-024--token-scopes),
[R-HA-037](spec/ha-and-helm.md#r-ha-037--service-ingress-and-tls-at-the-edge).

### ADR-129 · The owner's Kubernetes operators are the standard stack

**Status:** Accepted (owner constraint, 2026-10-29); it is the record of the owner's toolstack in the spec

**Context.** On 2026-10-29 the owner stated which Kubernetes operators the production stack and the local profile use.
Several requirements had been written for components the owner does not run (Traefik as the edge, hand-written Valkey
manifests, generated passwords by a script). The research on each operator, its CRDs, its Secret keys and its gaps against
trove's requirements is in the development agent's notes; the gaps that remain are recorded on the pages of their mechanisms.

**Decision.** The chart and the local profile use the guided-traffic operators and the owner's standard components:
internal-secrets-operator for generated Secrets; HAProxy Ingress as the edge; valkey-operator for Valkey with Sentinel over
TLS; CloudNativePG for PostgreSQL; stackit-s3-provisioner for buckets and object-store credentials (SeaweedFS in the local
profile, because the provisioner has no other backend); dex-operator for the OIDC client from M1; cert-manager for
certificates. Where an operator cannot yet provide a feature that a requirement needs, the requirement stays as written, the
production values follow it, and the gap is recorded: the Valkey gaps under [Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci),
the edge under [Q-HA-24](spec/ha-and-helm.md#q-ha-24--edge-controller-failover-mechanism-and-the-scatter-profile-of-the-local-profile),
and the security consequences on [security/](security/README.md).

**Consequences.** The chart reads the keys the operators write: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `S3_BUCKET`,
`S3_REGION` and `S3_ENDPOINT` from the provisioner, and the keys of the generated Secrets. The key ring accepts a 32-byte
raw key file, because the generator of random bytes writes that form ([Q-ARCH-34](spec/architecture.md#q-arch-34--key-files-base64-text-only-or-raw-bytes-too)).
The valkey-operator creates no ACL users, no separate Sentinel password and no `maxmemory` yet; until it does, the local
profile runs trove as the operator's `default` user, and that is [H-72](security/privilege-footprint.md#h-72--the-default-valkey-user-holds-every-right-until-acl-users-exist).

**Alternatives considered.** Hand-written Valkey manifests with full control over ACL and failure injection (the earlier
choice of Q-HA-15, option d, for the dev profile only); an object-store operator for MinIO with its own bucket model, which
is the owner's on-premises pattern and is not the local profile's choice because the owner's standard for buckets is the
provisioner; Traefik as the edge (the earlier choice of Q-HA-24).

**Spec:** [spec/README.md](spec/README.md#owner-constraints) (the dated owner constraint), [Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci),
[Q-HA-24](spec/ha-and-helm.md#q-ha-24--edge-controller-failover-mechanism-and-the-scatter-profile-of-the-local-profile).

### ADR-130 · The organisation's CI and release standard

**Status:** Accepted (owner constraint, 2026-10-29)

**Context.** On 2026-10-29 the owner decided that trove follows the organisation's CI and release standard, as in
[guided-traffic/valkey-operator](https://github.com/guided-traffic/valkey-operator) and guided-traffic/cogpit. Before
that, trove had its own pipeline: a CI workflow and a nightly workflow (`ci.yml`, `nightly.yml`), and a release workflow
that published the image and the chart to GitHub Container Registry with keyless cosign signatures. The first release,
`v0.1.0`, was published by that pipeline to `ghcr.io`. The owner's standard replaces all of it.

**Decision.** trove follows the standard in four workflows:

- **Test and Release** ([release.yml](../.github/workflows/release.yml)) runs on every push and every pull request to
  `main`. Every check is a job of its own that calls a Makefile target, so a failure reproduces with the same command.
  Every job runs on the organisation's self-hosted runners (`runs-on: self-hosted`).
- **Semantic Release** ([.releaserc.json](../.releaserc.json)), in the Test and Release run of a push to `main`, takes the
  version from the Conventional Commits since the last tag, writes the release notes, commits the coverage badge with
  `[skip ci]`, and publishes the tag and the GitHub release. A breaking change releases a minor version while trove is on
  major version 0 ([ADR-124](#adr-124--releases-stay-on-major-version-0)).
- **Release Docker & Helm** ([build.yml](../.github/workflows/build.yml)) runs on every published release: the image
  `docker.io/guidedtraffic/trove` with provenance and SBOM attestations, the SPDX SBOM attached to the release and a Docker
  Scout scan; the Helm chart in the chart repository `https://guided-traffic.github.io/trove/`, served from the `gh-pages`
  branch.
- **Renovate** ([renovate.yml](../.github/workflows/renovate.yml), [renovate.json](../renovate.json)) runs every day.

Not part of the standard: ghcr.io, keyless cosign signatures, a nightly workflow, and the AWS S3 conformance leg until an
AWS account and a CI role exist. The MinIO conformance leg runs in Test and Release on every change.

**Consequences.**

- **Versions come from the commits, not from people.** Commit messages follow Conventional Commits: `feat` makes a minor
  release, `fix` and `perf` a patch release, and a breaking change a minor release while trove is on major version 0. A
  tag or a GitHub release made by hand bypasses that rule, and a published release starts the image and chart pipeline
  with a version the commits do not describe.
- **A phase is closed by a release run.** The Semantic Release run that publishes the last change of a phase closes the
  phase, and the roadmap records the version that run published. The version the roadmap names beforehand
  ([ADR-124](#adr-124--releases-stay-on-major-version-0)) is the expectation; the commits decide the number.
- **Docker Hub and GitHub Pages replace ghcr.io.** The image needs a stored Docker Hub access token (`DOCKERHUB_PAT`).
  Semantic Release and Renovate act through a GitHub App whose token the workflows create (`APP_CLIENT_ID`,
  `APP_PRIVATE_KEY`). No signature is produced, so a consumer has nothing to verify
  ([H-79](spec/ha-and-helm.md#h-79--artifacts-are-unsigned-and-nothing-checks-their-origin)).
- **Self-hosted runners.** The runners are ephemeral pods without `make` or a C compiler, so every job installs the tools
  it calls, pinned in `tools/versions.env` or `go.mod`. The jobs of a pull request receive the repository's secrets
  ([H-80](spec/ha-and-helm.md#h-80--self-hosted-runners-build-the-code-of-every-pull-request)).
- **Actions and Renovate.** Actions are pinned by major version tag, as the organisation's other repositories pin them;
  Renovate updates the tags. Renovate's merges of minor and patch updates are `fix` commits, so each is a patch release.
- **Nothing runs nightly.** The end-to-end failover set and the MinIO leg run on every change
  ([Q-ROAD-7](coverage/roadmap.md#q-road-7--which-end-to-end-jobs-run-on-each-push-and-which-nightly),
  [Q-HA-3](spec/ha-and-helm.md#q-ha-3--s3-component-of-the-local-profile-after-minios-archival)). The AWS S3 leg waits for
  an account ([Q-HA-25](spec/ha-and-helm.md#q-ha-25--aws-leg-of-the-nightly-run-when-no-aws-account-is-configured)).
  The nightly acceptance runs that other pages name are not in the pipeline.

**Alternatives considered.** None; set by the owner. The earlier pipeline (ghcr.io, cosign, nightly workflows) is replaced.

**Spec:** [R-HA-047](spec/ha-and-helm.md#r-ha-047--images-private-registries-air-gapped-installation-and-attestations),
[Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart), [Q-HA-3](spec/ha-and-helm.md#q-ha-3--s3-component-of-the-local-profile-after-minios-archival),
[Q-ROAD-7](coverage/roadmap.md#q-road-7--which-end-to-end-jobs-run-on-each-push-and-which-nightly),
[release process](developer/releasing.md).

## Decisions by the development agent

The development agent selected each of these decisions from the options documented in the
question's block on its spec page, which also records the rationale, and the owner reviews them
at the walkthrough. The roadmap's questions `Q-ROAD-n` are decided in the same form on
[coverage/roadmap.md](coverage/roadmap.md#decisions) and close the table. A row that says
"Revisited 2026-10-28" belongs to a question the development agent checked again after the owner's
constraints of that day; the row says whether the choice changed and, if so, what it was before.
Questions raised by that revision are listed in their page's order like every other.

| ID | Question | Chosen option | ADR |
|---|---|---|---|
| [Q-ARCH-1](spec/architecture.md#q-arch-1--http-router) | Which HTTP router? | (a) `net/http.ServeMux` for every route; each format resolves its own escaped path behind its literal prefix | [ADR-029](#adr-029--formats-are-compiled-in-plug-ins-behind-a-narrow-host-interface) |
| [Q-ARCH-2](spec/architecture.md#q-arch-2--postgresql-driver-and-query-layer) | Which PostgreSQL driver and query layer? | (d) pgx's native API, sqlc for static queries and the confined `listq` builder for dynamic list and search queries; no `database/sql`. Revisited 2026-10-28, unchanged: no `LISTEN` any more, and pgx's exec modes let the main pool run through a transaction-mode pooler | [ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3) |
| [Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner) | Which migration runner? | (d) a trove-owned runner on native pgx that applies each step with its `schema_migration` row in one transaction; replaces the earlier goose choice | [ADR-033](#adr-033--versioning-promises-per-surface-and-a-schema-compatibility-gate) |
| [Q-ARCH-4](spec/architecture.md#q-arch-4--s3-client-library) | Which S3 client library? | (a) aws-sdk-go-v2 `service/s3` with a trove-owned multipart upload that reads its parts from the spool file | [ADR-004](#adr-004--s3-as-binary-storage), [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) |
| [Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation) | Own job queue or an external queue library? | (a) the trove-owned PostgreSQL job table and claim query of R-ARCH-016. Revisited 2026-10-28, unchanged: only `worker` replicas claim, wake-ups come from Valkey with polling as the bound, a Valkey Streams queue was rejected | [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work), [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases) |
| [Q-ARCH-6](spec/architecture.md#q-arch-6--cross-replica-collapse-holds-a-connection-for-the-whole-upstream-transfer) | Keep the cross-replica fetch lock although it holds a connection for the whole transfer? | (d) a Valkey lock per flight with a 30 s TTL that the holder extends, behind `cluster.FlightLock`; collapsing per replica without Valkey. Revisited 2026-10-28: was (a), the session advisory lock on a lock pool | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) |
| [Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills) | Lock key form for cache fills? | (c) for the fetch lock: a Valkey key with the full SHA-256 of the flight key; (a), the two-key form with a registered category, for every remaining PostgreSQL advisory lock; category 2 `fetch` retired. Revisited 2026-10-28: was (a) with `(2, <32-bit hash>)` | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11), [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing) |
| [Q-ARCH-8](spec/architecture.md#q-arch-8--notify-on-hot-path-commits) | How to NOTIFY on hot-path commits? | (d) signals leave after commit through a per-replica coalescer to Valkey Pub/Sub, at most one per area per 50 ms; trove issues no `NOTIFY`. Revisited 2026-10-28: was (a), the coalescer sending `NOTIFY` | [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early) |
| [Q-ARCH-9](spec/architecture.md#q-arch-9--freshness-of-merged-metadata-for-local-members) | Validate local members' revision on every merged-metadata hit? | (a) yes: the core checks every eligible local member's `rev_seq` on each hit, for every format. Revisited 2026-10-28, unchanged | [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early) |
| [Q-ARCH-10](spec/architecture.md#q-arch-10--separate-content-origin-for-package-content-a-browser-can-render) | Separate content origin for package content a browser can render? | (d) MVP: one origin with core-enforced content headers and one place that builds content links; an optional content origin in P1 (R-ARCH-029) | [ADR-030](#adr-030--one-origin-for-ui-api-and-package-content) |
| [Q-ARCH-11](spec/architecture.md#q-arch-11--url-layout-and-port-numbers-inv-6) | Change INV-6's URL layout or port numbers? | (a) keep INV-6: `/api/<format>/<repoKey>/`, `/api/v1/`, ports 8080 and 8081 | [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6) |
| [Q-ARCH-12](spec/architecture.md#q-arch-12--repository-wide-indexes-rendered-per-request-or-built-by-jobs-and-does-a-publish-wait) | Repository-wide indexes rendered per request or built by jobs, and does a publish wait? | (d) per request by default, jobs only by justified exception, a publish never waits | [ADR-089](#adr-089--derived-metadata-is-rendered-per-request-and-a-publish-never-waits-for-an-index) |
| [Q-ARCH-13](spec/architecture.md#q-arch-13--valkey-client-library-and-supported-valkey-versions) | Which Valkey client and which Valkey versions? | (a) valkey-go against Valkey 9.0 and later, Sentinel mode in production; no scripts, so the ACL user can be denied scripting | [ADR-099](#adr-099--valkey-client-valkey-go-against-valkey-90-and-later-sentinel-in-production) |
| [Q-ARCH-14](spec/architecture.md#q-arch-14--postgresql-connection-model-for-n-replicas-and-transaction-mode-poolers) | PostgreSQL connection model for n replicas and transaction-mode poolers? | (b) a transaction-scoped main pool that may run through a transaction-mode pooler; session connections only for the `worker` lease pool, `trove migrate` and `trove backup`; a control connection reserved inside `max_conns`; statement-mode poolers excluded | [ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers), [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12) |
| [Q-ARCH-15](spec/architecture.md#q-arch-15--cross-replica-invalidation-without-listennotify) | Cross-replica invalidation without `LISTEN/NOTIFY`? | (c) a per-area epoch and change log in PostgreSQL, polled by every replica, the epoch written last before commit; Valkey Pub/Sub only evicts early; a fill checks the epochs around its query | [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early), [ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3) |
| [Q-ARCH-16](spec/architecture.md#q-arch-16--what-valkey-may-hold) | What may Valkey hold? | (b) coordination state and the negative cache; nothing served to a client and no positive authentication or authorization result | [ADR-098](#adr-098--valkey-holds-coordination-and-the-negative-cache-nothing-served-and-nothing-that-grants) |
| [Q-ARCH-17](spec/architecture.md#q-arch-17--rate-limit-counters-across-replicas) | Rate-limit counters across replicas? | (b) cluster scope in Valkey with sliding windows; per-replica shares of the budget while Valkey is unavailable; same choice as Q-API-3 | [ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback) |
| [Q-ARCH-18](spec/architecture.md#q-arch-18--transfers-that-outlast-the-termination-grace-period) | Transfers that outlast the termination grace period? | (a) cut at the deadline; clients resume with `Range` and `If-Range` on any replica or retry; an upload whose body has fully arrived is not cut before its status line | [ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica) |
| [Q-ARCH-19](spec/architecture.md#q-arch-19--package-level-state-the-state-rule-allows) | Which package-level variables does the state rule allow? | (b) an exact exemption of `*regexp.Regexp`; every other pointer is reported | — |
| [Q-ARCH-20](spec/architecture.md#q-arch-20--web-build-output-in-git-and-the-placeholder) | Is built UI output committed, and what does a partial build do? | (c) placeholder `index.html` committed, locale directories ignored, a partial build fails start-up | — |
| [Q-ARCH-21](spec/architecture.md#q-arch-21--setup-failures-of-the-outbound-factory-and-the-object-store-exception) | How do outbound setup failures show, and does the egress policy cover the object store? | (b) the factory keeps its setup error and its clients fail every request; the egress policy skips `objectstore` (H-56) | — |
| [Q-ARCH-22](spec/architecture.md#q-arch-22--sealed-column-registry-and-the-envelope-format) | How are sealed columns registered and enveloped? | (b) only `secrets.Register` creates a column; envelope `v1:<key id>:<base64>` with AES-256-GCM and the row and column as additional data | — |
| [Q-ARCH-23](spec/architecture.md#q-arch-23--query-execution-mode-behind-a-transaction-mode-pooler) | Which query execution mode runs behind a transaction-mode pooler? | (b) `QueryExecModeExec` for the pooled main class; a session-state refusal is never retried | — |
| [Q-ARCH-24](spec/architecture.md#q-arch-24--opening-the-postgresql-pool-and-a-connection-lost-during-commit) | Which PostgreSQL failures are retried, and what happens to a lost COMMIT? | (b) connection errors retried within `retry.max_elapsed`; an unknown COMMIT outcome is never retried and answers 503 | — |
| [Q-ARCH-25](spec/architecture.md#q-arch-25--readonly-replies-and-the-circuit-breaker) | Does a `READONLY` reply open the Valkey circuit breaker? | (b) no: neutral for the breaker, counted under `kind="readonly"`, the next probe marks Valkey degraded | — |
| [Q-ARCH-26](spec/architecture.md#q-arch-26--valkey-key-families-expiry-and-keyspace) | How are Valkey keys formed, and what expires them? | (b) key families registered with a mandatory maximum expiry; `Key(family, parts...)`; parts `[A-Za-z0-9_-]`; keyspace from the UUID without dashes | — |
| [Q-ARCH-27](spec/architecture.md#q-arch-27--valkey-connections-data-pubsub-and-sentinel-probes) | How many Valkey connections, and which carry Pub/Sub and the Sentinel probes? | (b) a data client, one dedicated Pub/Sub connection `trove:<instance>:pubsub`, a short probe connection per Sentinel at each refresh | — |
| [Q-ARCH-28](spec/architecture.md#q-arch-28--layering-of-the-blob-store-and-where-its-garbage-collection-job-registers) | Where does the blob store sit in the layers, and where does its GC job register? | (b) `blob` is infrastructure, formats reach it only through the host, the GC job registers in `internal/app` through `RunJob` | — |
| [Q-ARCH-29](spec/architecture.md#q-arch-29--what-the-scheduler-enqueues-on-each-tick) | What does the scheduler enqueue on each tick? | (b) the latest due slot of each enabled task, deduplicated by `job_task_slot` | — |
| [Q-ARCH-30](spec/architecture.md#q-arch-30--fencing-of-singleton-writes) | How are singleton writes fenced? | (a) no token: writes on the lease's own session, as R-HA-006 states; the xid8 token was rejected | [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing) |
| [Q-ARCH-31](spec/architecture.md#q-arch-31--how-tls-material-is-reloaded) | How is TLS material reloaded? | (a) content polling every `server.tls.reload_interval`, not a filesystem notifier and not a restart | [ADR-127](#adr-127--transport-encryption-on-every-connection-troves-listeners-included) |
| [Q-ARCH-32](spec/architecture.md#q-arch-32--client-certificates-on-troves-client-listener) | Are client certificates required on the client listener? | (b) optional: verified when `server.tls.client_ca_file` is set; off by default | [ADR-127](#adr-127--transport-encryption-on-every-connection-troves-listeners-included) |
| [Q-ARCH-33](spec/architecture.md#q-arch-33--one-certificate-pair-for-both-listeners-or-one-pair-each) | One certificate pair for both listeners, or one pair each? | (a) one pair by default; `server.ops_tls` may name its own pair | [ADR-127](#adr-127--transport-encryption-on-every-connection-troves-listeners-included) |
| [Q-ARCH-34](spec/architecture.md#q-arch-34--key-files-base64-text-only-or-raw-bytes-too) | Does a key file hold base64 text only, or raw bytes too? | (b) a file of exactly 32 bytes is the key, anything else is base64 text | — |
| [Q-ARCH-35](spec/architecture.md#q-arch-35--which-forwarded-header-family-does-the-edge-rewrite) | Which forwarded header family does the edge rewrite? | (a) one family, chosen by `server.forwarded_header`, no fallback | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-ARCH-36](spec/architecture.md#q-arch-36--the-ops-listener-before-its-certificate-loads) | Ops listener before its certificate loads? | (b) a temporary self-generated certificate, never offered by a client or an endpoint listener | — |
| [Q-ARCH-37](spec/architecture.md#q-arch-37--who-may-build-an-endpoint-and-what-an-endpoint-answers-outside-its-surface) | Who may build an endpoint, and what does it answer outside its surface? | (b) a typed rule over `go/packages` with an unexported setter, three rules closing the hole from the other side | — |
| [Q-ARCH-38](spec/architecture.md#q-arch-38--the-limiter-is-created-from-a-set-not-by-a-package-function) | Is the limiter created from a set or by a package function? | (a) `LimiterSet.NewLimiter(name, scope, rate, burst)` | — |
| [Q-ARCH-39](spec/architecture.md#q-arch-39--the-start-up-canary-of-r-arch-021-is-built-with-the-key-ring-status) | When is the start-up canary of R-ARCH-021 built? | (a) at step 8, with the API wiring | — |
| [Q-ARCH-40](spec/architecture.md#q-arch-40--the-lookup-of-an-unknown-key-runs-before-the-decision-and-the-decision-waits-for-the-grant-index) | Does the lookup of an unknown key run before the decision? | (b) look the key up first, discard the result, then wait for the grant index | — |
| [Q-ARCH-41](spec/architecture.md#q-arch-41--the-content-headers-go-on-responses-that-carry-content-not-on-problems) | Where do the content headers go? | (b) on responses that carry the file or the index, not on problems | — |
| [Q-ARCH-42](spec/architecture.md#q-arch-42--paths-that-servemux-would-redirect-reach-the-files-handler) | Do paths that ServeMux would redirect reach the files handler? | (b) `Router.HandleUncleaned("/files/", h)` | — |
| [Q-ARCH-43](spec/architecture.md#q-arch-43--the-files-transfer-is-two-packages-internalfiles-and-internalapirawfiles) | Is the files transfer one package or two? | (b) `internal/files` (domain) and `internal/api/rawfiles` (edge) | — |
| [Q-ARCH-45](spec/architecture.md#q-arch-45--a-limiter-may-divide-its-fallback-share-by-its-own-count) | May a limiter divide its fallback share by its own count? | (a) `LimiterSet.NewLimiterShared` with a count of its own | — |
| [Q-ARCH-76](spec/architecture.md#q-arch-76--the-pace-of-the-download-that-a-rollout-restart-must-not-break) | Which download pace must a rollout restart not break? | (b) 6 MB/s (about 70 s), with the serving pod drained before the transfer ends | — |
| [Q-ARCH-86](spec/architecture.md#q-arch-86--the-resource-policy-of-content-read-with-the-session-cookie) | Which resource policy applies to content read with the session cookie? | (b) `same-origin` when the session cookie authenticated the request | — |
| [Q-ARCH-147](spec/architecture.md#q-arch-147--one-replica-count-per-pool-for-every-limiter-of-the-client-listener) | One replica count per pool for every limiter of the client listener? | (b) one component | — |
| [Q-ARCH-187](spec/architecture.md#q-arch-187--a-connection-is-held-until-the-peer-has-taken-its-last-bytes-and-a-cut-is-a-reset) | Is a connection held until the peer has taken its last bytes? | (a) hold the connection | — |
| [Q-ARCH-188](spec/architecture.md#q-arch-188--the-format-catalog-is-a-leaf-package-that-every-consumer-reads-not-a-copy-in-each-layer) | Where do the facts of a format live, and how does a consumer get them? | (a) `internal/formats/catalog`, a leaf; consumers take a `*catalog.Registry`, nil is the process registry | — |
| [Q-ARCH-189](spec/architecture.md#q-arch-189--shared-value-types-are-declared-once-in-a-leaf-and-named-by-alias-never-mirrored) | How do layers share a value type such as the class, the request kind or the token scope? | (a) declared once in a leaf, named by alias, never mirrored | — |
| [Q-ARCH-190](spec/architecture.md#q-arch-190--the-m2-tables-ship-as-three-expand-only-migrations-before-any-package-that-uses-them) | Which migrations carry the M2 tables? | (a) one schema package, three files by area (`00014` to `00016`); later packages add query files only | — |
| [Q-ARCH-191](spec/architecture.md#q-arch-191--columns-and-tables-the-m2-schema-leaves-out-or-leaves-without-a-constraint) | Which sketched columns and tables does the M2 schema leave out? | (a) no `search_vector`, no P1 tables; `package_owner.ci_issuer_id` without its foreign key until M4 | — |
| [Q-ARCH-192](spec/architecture.md#q-arch-192--the-per-caller-virtual-engine-is-a-domain-package-of-its-own) | Where does the per-caller virtual engine live? | (a) routing in `internal/repository`, per-caller resolution in the new domain package `internal/virtual` | — |
| [Q-ARCH-193](spec/architecture.md#q-arch-193--a-format-reaches-its-own-tables-through-a-store-port-that-a-domain-package-implements) | How does a format reach its own tables? | (a) a store port in the format, implemented in `internal/formatdata/<id>` | — |
| [Q-ARCH-194](spec/architecture.md#q-arch-194--the-areas-of-m2-claims-cache-invalidation-and-upstream-state-are-change-areas-the-merged-metadata-eviction-a-signal) | Who registers the M2 change areas? | (a) the schema package registers all four and seeds the epoch rows in the migrations | — |
| [Q-ARCH-195](spec/architecture.md#q-arch-195--what-a-waiter-learns-when-a-flight-ends) | What does a waiter learn when a flight ends? | (b) `stored` and `failed` are published (a recorded 404 is `stored`), `Wait` also returns `gone`; the leader's `traceparent` follows the outcome after a space | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) |
| [Q-ARCH-196](spec/architecture.md#q-arch-196--the-life-of-a-hold-and-what-a-failed-call-is) | Who extends a hold, and which errors are `ErrUnavailable`? | (a) one goroutine per hold bound to the client; every failed call wraps `ErrUnavailable`; the in-process single-flight group is the proxy's | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) |
| [Q-ARCH-197](spec/architecture.md#q-arch-197--the-negative-store-checks-the-form-of-an-entry-the-proxy-judges-its-validity) | Who validates a negative entry? | (a) the store checks the form and deletes a malformed value; the proxy keeps the L1, the window and the invalidations | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) |
| [Q-ARCH-198](spec/architecture.md#q-arch-198--source-address-binding-of-a-remotes-connections) | How does a remote's source address get enumerated and bound? | (b) enumeration on the dial path when the last one is 60 s old; a failing lister keeps the last good address; no fallback to the kernel | — |
| [Q-ARCH-199](spec/architecture.md#q-arch-199--where-the-credential-decision-of-each-hop-is-made) | Where is the credential decision of each redirect hop made? | (b) the client's round tripper calls a hop policy on every request; the client certificate follows the same rule | — |
| [Q-ARCH-205](spec/architecture.md#q-arch-205--one-edge-package-for-the-conversions-every-http-entry-point-shares) | Where do the conversions every HTTP entry point shares live? | (a) `internal/api/edge`, imported by `rawfiles`, the dispatcher and `v1` | — |
| [Q-ARCH-206](spec/architecture.md#q-arch-206--the-files-transfer-is-three-packages-the-item-store-the-generic-layer-and-the-raw-routes) | How is the files transfer cut so that the proxy and the formats store items without a decision? | (b) `internal/items` (no authz), `internal/files` (the generic layer) over it, `internal/api/rawfiles` without `v1`; supersedes Q-ARCH-43 | — |
| [Q-ARCH-207](spec/architecture.md#q-arch-207--one-commit-in-the-item-store-the-item-row-then-the-committers-write-then-the-formats-hook-then-the-audit-row) | What does an item commit do, and in which order? | (b) one `Commit(CommitSpec)`: item row, `Extra`, `OnCommit`, audit row, in one transaction | — |
| [Q-ARCH-208](spec/architecture.md#q-arch-208--a-deploy-decides-again-on-a-new-index-after-its-body-arrived) | Which index does the commit-time re-check of a deploy use? | (b) a new holder begun after the body arrived | — |
| [Q-REPO-1](spec/repositories-and-proxy.md#q-repo-1--who-specifies-the-stored-secret-mechanism-iam-045) | Which page specifies the stored-secret mechanism? | (b) architecture.md's R-ARCH-021, for every stored secret; repository secrets only use it | [ADR-032](#adr-032--stored-secrets-are-sealed-under-a-key-ring-the-operator-supplies) |
| [Q-REPO-2](spec/repositories-and-proxy.md#q-repo-2--lower-bound-of-the-metadata-period) | Lower bound of the metadata period? | (a) `0` is allowed and means revalidate on every request; no minimum. Revisited 2026-10-28, choice unchanged | [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving) |
| [Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document) | Generic default when the upstream removes a cached document? | (b) mark the package gone; its documents and cached artifacts answer 404 until the upstream serves it again | [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving), [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default) |
| [Q-REPO-4](spec/repositories-and-proxy.md#q-repo-4--scope-of-remote-refusal-for-claimed-names) | Scope of remote refusal for claimed names? | (a) every remote of the format refuses `global`-claimed names, also when addressed directly, unless a pin rule names that remote | [ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules) |
| [Q-REPO-5](spec/repositories-and-proxy.md#q-repo-5--generic-or-format-level-placement-of-pins-extra-hosts-and-upstream-authentication) | Generic or format-level settings for pins, extra hosts and upstream authentication? | (a) generic members; format pages add semantics and may narrow them, but define none of their own | [ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules) |
| [Q-REPO-6](spec/repositories-and-proxy.md#q-repo-6--local-publish-of-a-name-that-a-remote-member-already-serves-h-10) | Local publish of a name a remote member already serves? | (b) the first publish needs `manage` or a prior manual claim, is audited and fails closed. Revisited 2026-10-28, choice unchanged: stored state means PostgreSQL entries only; a name with only a negative entry is probed | [ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules) |
| [Q-REPO-7](spec/repositories-and-proxy.md#q-repo-7--remote-fetches-on-a-replica-that-is-shutting-down) | Remote fetches on a replica that is shutting down? | (b) held fetches finish until 5 s before the deadline, then end with `failed`; from `SIGTERM` on, the replica holds no new cross-replica flight | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11), [ADR-103](#adr-103--shutdown-protocol-drain-delay-transfers-cut-at-the-deadline-resumed-on-any-replica) |
| [Q-REPO-8](spec/repositories-and-proxy.md#q-repo-8--upstream-fetch-limits-when-the-replica-count-changes) | Upstream fetch limits when the replica count changes? | (b) `limits.maxConcurrentFetches` is a budget for the installation (default 64), shared out as `ceil(budget ÷ n)` per replica from the replica registry | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) |
| [Q-REPO-9](spec/repositories-and-proxy.md#q-repo-9--source-address-for-upstream-connections-with-interchangeable-replicas) | Source address for upstream connections with interchangeable replicas? | (b) `remote.sourceNetwork`, a CIDR prefix; each replica binds its own address in it, and a replica without one fails closed without opening the shared circuit | [ADR-039](#adr-039--egress-address-checks-after-dns-resolution-for-every-remote-connection) |
| [Q-REPO-10](spec/repositories-and-proxy.md#q-repo-10--zoned-ipv6-and-the-nat64-prefix-in-the-egress-rules) | Zoned IPv6 and the NAT64 prefix in the egress rules? | (a) refuse zoned IPv6 before the CIDR rules; check the well-known NAT64 prefix through its IPv4 address; the deny list adds `::/96` and two metadata addresses | [ADR-039](#adr-039--egress-address-checks-after-dns-resolution-for-every-remote-connection) |
| [Q-REPO-11](spec/repositories-and-proxy.md#q-repo-11--repository-columns-added-in-m1) | Which repository columns does M1 add? | (b) the M1 columns now, the others with their requirements | — |
| [Q-REPO-12](spec/repositories-and-proxy.md#q-repo-12--the-error-code-of-a-422-with-one-or-several-violations) | Error code of a 422 with one or several violations? | (b) one violation under its own code, several under `validation_failed`; superseded by Q-REPO-84 | — |
| [Q-REPO-13](spec/repositories-and-proxy.md#q-repo-13--management-reads-come-from-postgresql-the-list-filters-in-the-domain) | Do management reads come from PostgreSQL or the snapshot? | (b) read and list from PostgreSQL; filter and page over one query | — |
| [Q-REPO-14](spec/repositories-and-proxy.md#q-repo-14--one-snapshot-entry-flushed-by-every-repo_config-change) | One snapshot entry, flushed by every `repo_config` change? | (a) one cache entry for the whole table, flushed on any change | — |
| [Q-REPO-15](spec/repositories-and-proxy.md#q-repo-15--a-repository-deletion-records-two-events-and-joins-a-running-job) | How many events does a repository deletion record, and what does a repeated delete do? | (b) `repository.deleted` with `phase: started` first; a repeated delete joins the running job | — |
| [Q-REPO-16](spec/repositories-and-proxy.md#q-repo-16--the-shape-of-the-stored-document) | Shape of the stored repository document? | the settings as read back, defaults filled in; unknown or duplicate members answer 400 `invalid_document`; a no-op update writes nothing | — |
| [Q-REPO-17](spec/repositories-and-proxy.md#q-repo-17--the-pattern-matcher-lives-in-internalrepository) | Where does the pattern matcher live? | (a) in `internal/repository`, exported as `CompilePattern` | — |
| [Q-REPO-18](spec/repositories-and-proxy.md#q-repo-18--the-wait-for-an-unknown-key-is-repositorieslookup_wait) | Wait for an unknown key: which setting? | (a) its own key, `repositories.lookup_wait`, default 5 s | — |
| [Q-REPO-19](spec/repositories-and-proxy.md#q-repo-19--a-holder-of-repositoriesread-config-sees-every-repository-in-the-list) | Which repositories does a holder of `repositories:read-config` see in the list? | (b) every repository | — |
| [Q-REPO-20](spec/repositories-and-proxy.md#q-repo-20--the-consumer-view-needs-read-and-the-etag-names-the-view) | Which action does the consumer view need, and what does its ETag name? | (b) `read`; the ETag is `"<config_version>-consumer"` | — |
| [Q-REPO-21](spec/repositories-and-proxy.md#q-repo-21--a-misspelt-or-malformed-member-of-a-repository-document-answers-400-invalid_document-with-its-pointer) | Malformed member of a repository document: which error? | (b) `invalid_document` with the pointer; superseded by Q-REPO-84 | — |
| [Q-REPO-22](spec/repositories-and-proxy.md#q-repo-22--the-list-filters-repeated-or-comma-separated-values-and-a-project-that-selects-nothing) | List filters: repeated or comma-separated values, and a project that selects nothing? | (b) both forms; `project` selects nothing | — |
| [Q-REPO-23](spec/repositories-and-proxy.md#q-repo-23--last_activity_at-is-flushed-once-a-minute-by-a-constant-interval) | Flush interval of `last_activity_at`? | (b) a constant of one minute | — |
| [Q-REPO-84](spec/repositories-and-proxy.md#q-repo-84--a-violation-answers-422-validation_failed-a-body-error-400-malformed_request) | Which code answers a violation and which a body error? | (b) R-API-009 everywhere: a violation `422 validation_failed`, a body error `400 malformed_request` | — |
| [Q-REPO-85](spec/repositories-and-proxy.md#q-repo-85--the-integer-keys-of-proxy-keep-the-unit-in-their-name) | Do the `proxy.*` keys that the spec names with a unit become durations? | (a) the names and integer values stay; the code reads them through duration accessors | — |
| [Q-REPO-86](spec/repositories-and-proxy.md#q-repo-86--remote_cache_entries-has-a-surrogate-id-so-the-blob-release-trigger-applies-unchanged) | How does the blob release trigger apply to `remote_cache_entries`? | (a) a surrogate `id` column, so `blob_ref_release_update()` applies unchanged | — |
| [Q-REPO-87](spec/repositories-and-proxy.md#q-repo-87--invalidation-rows-record-their-fold-in-folded_at) | How does the fold job record which invalidations it has folded? | (a) a column `folded_at` on `remote_cache_invalidations` | — |
| [Q-REPO-100](spec/repositories-and-proxy.md#q-repo-100--the-prefix-of-an-invalidation-is-whole-segments-validated-up-front) | What is the prefix of a cache invalidation? | (c) whole segments, validated: no leading or trailing slash, no empty or dot segment, at most 256 bytes, else `422` | — |
| [Q-REPO-101](spec/repositories-and-proxy.md#q-repo-101--the-row-is-stamped-inside-its-transaction-and-the-count-is-of-metadata-entries-only) | When is an invalidation row stamped, and what does the count cover? | (b) `clock_timestamp()` in the insert; metadata entries only, so `negative` answers 0 | — |
| [Q-REPO-102](spec/repositories-and-proxy.md#q-repo-102--the-matcher-keeps-the-newest-time-per-prefix-and-applies-a-row-from-its-payload) | How does a replica answer "is this entry invalidated" per request? | (b) the newest time per prefix, one map access per path segment; a row is applied from its payload | — |
| [Q-REPO-103](spec/repositories-and-proxy.md#q-repo-103--the-fold-never-extends-an-expiry-does-not-skip-locked-rows-and-checks-once-more-before-a-deletion) | How does the fold treat an expiry, a locked entry and a late fill? | (b) `LEAST`, locked in key order and not skipped; a folded `metadata` row is deleted after an hour, after one more fold pass | — |
| [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12) | Should object-storage reachability gate readiness? | (d) only the first readiness, through the start-up check; continuous gating as an opt-in; changes INV-12; same choice as Q-OPS-2 and Q-HA-1. Revisited 2026-10-28, unchanged: a drain during an outage costs each role at most one replica; Valkey plays no part | [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12), [ADR-043](#adr-043--object-store-outage-no-local-queue-and-201-means-durable) |
| [Q-STOR-2](spec/storage.md#q-stor-2--sha-512-for-every-blob-or-only-on-demand) | SHA-512 for every blob or only on demand? | (a) every blob; aligns INV-4's wording | [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) |
| [Q-STOR-3](spec/storage.md#q-stor-3--owner-of-build-records) | Which page owns build records? | (a) a new page `builds.md` (`R-BLD`), written before P1 build work starts; same choice as Q-AUTHZ-8 and Q-API-5 | — |
| [Q-STOR-4](spec/storage.md#q-stor-4--cron-dialect-shared-by-all-schedules) | Which cron dialect for all schedules? | (a) five-field cron in UTC with the Kubernetes macros; other dialects' forms rejected; same choice as Q-OPS-7 | [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) |
| [Q-STOR-5](spec/storage.md#q-stor-5--does-overwriting-a-file-send-the-previous-content-to-the-trash-can) | Does an overwrite send the previous content to the trash can? | (a) yes, for user-deployed files in local repositories | [ADR-044](#adr-044--a-trash-can-that-also-covers-overwrites-p1) |
| [Q-STOR-6](spec/storage.md#q-stor-6--download-verification-on-by-default) | Download verification on by default? | (a) on; `storage.download.verify: false` remains for CPU-bound replicas | — |
| [Q-STOR-7](spec/storage.md#q-stor-7--status-code-for-an-exceeded-quota) | Status code for an exceeded quota? | (a) `507` for every quota and capacity limit, `413` only for the per-request size limit | — |
| [Q-STOR-8](spec/storage.md#q-stor-8--copy-and-move-target-semantics) | Copy and move target semantics? | (a) the target path is the exact destination; existing folders are merged; identical content counts as `unchanged` | — |
| [Q-STOR-9](spec/storage.md#q-stor-9--one-pattern-syntax-across-retention-and-permissions) | One pattern syntax for retention and permissions? | (a) the selectors and patterns of R-AUTHZ-004 and R-AUTHZ-005 through the same code; include key selectors bound by ID, exclude key selectors by key | [ADR-037](#adr-037--one-name-pattern-syntax-most-specific-wins-applied-to-every-protocol-request) |
| [Q-STOR-10](spec/storage.md#q-stor-10--storage-maintenance-jobs-and-the-storage-summary-one-task-framework-one-path) | Storage jobs and the storage summary: own run API or the shared task framework? | (a) tasks of the shared framework with one start and one stop path each; storage keeps the read paths of its results; same choice as Q-API-9 | [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work), [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) |
| [Q-STOR-11](spec/storage.md#q-stor-11--multi-request-uploads-across-replicas) | Multi-request uploads across replicas? | (b) one S3 multipart upload per session; parts and a per-request tail object recorded in PostgreSQL under a writer claim; the claim is released on drain | [ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18) |
| [Q-STOR-12](spec/storage.md#q-stor-12--counters-that-several-replicas-update-usage-quotas-and-download-statistics) | Counters that several replicas update? | (a) PostgreSQL deltas folded by every worker every 5 s; quota admission reads the folded rows; a recount after a restore | [ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3), [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases) |
| [Q-STOR-13](spec/storage.md#q-stor-13--exclusion-of-storage-maintenance-runs-job-key-fenced-lease-or-valkey-lock) | How are storage maintenance runs kept apart? | (a) by the exclusive job key only; S3 deletes follow the hand-over rule on the collector's own clock, and claims are handed over on drain; no lease, no Valkey lock | [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) |
| [Q-STOR-14](spec/storage.md#q-stor-14--object-store-settings-the-outbound-layer-does-not-yet-support-and-plaintext-endpoints) | Which object-store settings does the outbound layer not yet support, and how are plaintext endpoints allowed? | (c) refuse `ca_file`; the timeouts and the connection limit take the factory defaults; `http://` needs `allow_insecure_endpoint`. Revisited in M0 (2026-10-29): `ca_file` is supported now; `proxy_credentials_file` stays refused | — |
| [Q-STOR-15](spec/storage.md#q-stor-15--object-store-endpoint-without-a-scheme) | What does an object-store endpoint without a scheme mean? | (b) a host without a scheme means `https://`; plaintext needs `http://` and `allow_insecure_endpoint` | — |
| [Q-STOR-16](spec/storage.md#q-stor-16--what-the-spool-may-delete-at-start) | What may the spool delete at start? | (b) only the regular files named by a UUID; any other entry refuses the start, `lost+found` tolerated | — |
| [Q-STOR-17](spec/storage.md#q-stor-17--replacing-a-file-without-overwrite-answers-403) | Replacing a file without `overwrite`: which answer? | (b) 403 `forbidden` naming `overwrite` | — |
| [Q-STOR-18](spec/storage.md#q-stor-18--implicit-folders-are-rows-that-go-with-their-last-child-serialised-by-row-locks) | How are implicit folders represented? | (b) folder rows, created with the file and removed with its last child | — |
| [Q-STOR-19](spec/storage.md#q-stor-19--a-delete-is-permanent-in-m1-one-event-per-file-a-large-subtree-by-the-job-items-delete) | Permanent delete in M1: when is permission checked? | (b) the check completes before the job is enqueued | — |
| [Q-STOR-20](spec/storage.md#q-stor-20--a-listing-scans-a-bounded-number-of-rows-and-its-cursor-is-the-last-row-scanned) | Listing with a bounded scan: filter in SQL or scan and return a short page? | (b) scan up to 100,000 rows per page and return a short page | — |
| [Q-STOR-21](spec/storage.md#q-stor-21--a-folder-addressed-without-its-trailing-slash-is-redirected-a-download-counts-after-its-body-was-sent) | Folder without its trailing slash, and when a download counts? | (b) 301 to the URL with the slash; the download counts after the whole response | — |
| [Q-STOR-62](spec/storage.md#q-stor-62--a-listing-that-ends-on-a-hidden-row-its-cursor-and-its-cost) | Cursor of a listing that ends on a hidden row? | (c) the item's ID, when the page ends at the scan limit beyond its last visible entry | — |
| [Q-STOR-143](spec/storage.md#q-stor-143--the-delete-job-removes-only-the-items-its-permission-check-saw) | Which items does the delete job remove? | (c) the set is fixed at check time; a late file stays | — |
| [Q-STOR-144](spec/storage.md#q-stor-144--a-deploy-locks-the-whole-ancestor-chain-and-overwrite-is-settled-before-the-commit-transaction) | Deploy locking and the `overwrite` decision? | (b) lock every ancestor `FOR SHARE`, deepest first; `overwrite` settled by `PathsAllowed` before commit | — |
| [Q-STOR-145](spec/storage.md#q-stor-145--a-repositorys-maxuploadsize-is-a-validated-integer-and-fails-closed) | Validation of a repository's `maxUploadSize`? | (b) validate at write, refuse at use; digits only, must fit an int64 | — |
| [Q-STOR-146](spec/storage.md#q-stor-146--a-remote-fill-is-an-item-with-its-upstream-url-and-no-artifactdeployed-event) | Does a remote fill write `artifact.deployed`? | (b) no; the item carries `upstream_url` and the proxy raises its own event | — |
| [Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives) | Where does login provider configuration live? | (a) the configuration file only, claim mapping, gates and group rules included; same choice as Q-OPS-6 | [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7), [ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15) |
| [Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name) | Should the cookies use the `__Host-` prefix? | (c) all three: `__Host-trv_session`, `__Host-trv_csrf`, `__Host-trv_oidc_binding`; changes INV-8 | [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8), [ADR-046](#adr-046--server-side-ui-sessions-behind-an-opaque-cookie) |
| [Q-AUTH-3](spec/identity-and-authentication.md#q-auth-3--refresh-tokens-or-rotation) | Refresh tokens or rotation only? | (a) no refresh tokens; rotation and repeated login are the renewal paths | [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8) |
| [Q-AUTH-4](spec/identity-and-authentication.md#q-auth-4--missing-group-claim) | What happens when the group claim is missing at login? | (a) clear the synced groups, with the warning event `user.groups.claim_missing`; `keep` and `deny` stay per-provider settings | [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7) |
| [Q-AUTH-5](spec/identity-and-authentication.md#q-auth-5--dormancy-default) | Dormant-user deactivation on by default? | (a) on, after 90 days (P1, with R-AUTH-021) | [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7) |
| [Q-AUTH-6](spec/identity-and-authentication.md#q-auth-6--unauthenticated-revoke-by-value) | Accept unauthenticated revocation by token value? | (a) yes, rate-limited and without an existence oracle; the forward cascade has no time limit | [ADR-048](#adr-048--tokens-hashed-with-sha-256-a-positive-only-cache-bounded-revocation-latency) |
| [Q-AUTH-7](spec/identity-and-authentication.md#q-auth-7--administrators-issuing-tokens-for-users) | May administrators create tokens owned by other users? | (a) no, neither create nor rotate; automation uses service accounts | [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8) |
| [Q-AUTH-8](spec/identity-and-authentication.md#q-auth-8--address-binding-of-the-npm-web-login-poll) | Address binding of the npm web-login poll? | (d) bound to the starting network (IPv4 `/24`, IPv6 `/56` or an operator range); refused polls shown, audited and counted | [ADR-049](#adr-049--command-line-login-through-the-browser-session-p1) |
| [Q-AUTH-9](spec/identity-and-authentication.md#q-auth-9--shared-valkey-cache-for-session-and-token-lookups) | A shared Valkey cache for session and token lookups? | (a) no: sessions, tokens, principals and settings are cached in process only; Valkey carries their change messages | [ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey) |
| [Q-AUTH-10](spec/identity-and-authentication.md#q-auth-10--revocation-bound-when-a-replica-cannot-read-the-change-log) | Revocation bound when a replica cannot read the change log? | (b) the caches answer only while the last poll is younger than `auth.revocation_bound` (2 s); otherwise PostgreSQL, and `503` if it fails | [ADR-114](#adr-114--authentication-caches-answer-only-while-the-change-log-is-fresh), [ADR-048](#adr-048--tokens-hashed-with-sha-256-a-positive-only-cache-bounded-revocation-latency) |
| [Q-AUTH-11](spec/identity-and-authentication.md#q-auth-11--brute-force-counters-and-the-failed-credential-throttle-across-replicas) | Brute-force counters and the failed-credential throttle across replicas? | (c) cluster-wide in Valkey with per-replica fallback and a Valkey exemption marker; the break-glass lockout stays in PostgreSQL | [ADR-115](#adr-115--brute-force-counters-count-cluster-wide-the-break-glass-lockout-and-rotation-stay-in-postgresql), [ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback) |
| [Q-AUTH-12](spec/identity-and-authentication.md#q-auth-12--where-multi-request-login-flows-keep-their-state) | Where do multi-request login flows keep their state? | (a) in PostgreSQL rows, changed by conditional statements; nothing in a replica's memory or in Valkey | [ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey), [ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18) |
| [Q-AUTH-13](spec/identity-and-authentication.md#q-auth-13--idle-timeout-tracking-across-replicas) | Idle-timeout tracking across replicas? | (a) a conditional touch at most once per minute and session; idle expiry decided from the row | [ADR-113](#adr-113--authentication-state-lives-in-postgresql-and-in-process-caches-never-in-valkey) |
| [Q-AUTH-14](spec/identity-and-authentication.md#q-auth-14--break-glass-password-rotation-across-replicas) | Break-glass password rotation across replicas? | (b) a rotation hold in PostgreSQL: the first replica that notices ends earlier sessions in one transaction; every replica refuses local login for 2 minutes | [ADR-115](#adr-115--brute-force-counters-count-cluster-wide-the-break-glass-lockout-and-rotation-stay-in-postgresql) |
| [Q-AUTH-15](spec/identity-and-authentication.md#q-auth-15--token-lineage-pointers-without-a-foreign-key) | Token lineage pointers without a foreign key? | (b) both are plain `uuid` columns | — |
| [Q-AUTH-16](spec/identity-and-authentication.md#q-auth-16--principal-integrity-enforced-in-the-database) | Principal integrity enforced in the database? | (b) `CHECK` per kind and guard triggers | — |
| [Q-AUTH-17](spec/identity-and-authentication.md#q-auth-17--the-command-line-login-table-ships-with-the-login-state) | Does the command-line login table ship with the login state? | (b) yes, in `00008_login.sql` | — |
| [Q-AUTH-18](spec/identity-and-authentication.md#q-auth-18--the-journal-entry-of-settingsauthchanged) | Is the journal entry of `settings.auth.changed` optional? | (b) always supplied | — |
| [Q-AUTH-19](spec/identity-and-authentication.md#q-auth-19--which-unauthenticated-requests-the-stage-lets-through) | Which unauthenticated requests does the stage let through? | (c) the stage asks which operation the request matches | — |
| [Q-AUTH-20](spec/identity-and-authentication.md#q-auth-20--where-the-failed-credential-throttle-trips) | Where does the failed-credential throttle trip? | (b) exceeded at the burst, in `auth.LocalLimiter` | — |
| [Q-AUTH-21](spec/identity-and-authentication.md#q-auth-21--what-the-login-flow-hands-to-the-provisioner) | What does the login flow hand to the provisioner? | (b) the policy, with the claims | — |
| [Q-AUTH-22](spec/identity-and-authentication.md#q-auth-22--the-payload-kinds-of-the-change-area-auth) | Payload kinds of the change area `auth`? | (b) kinds with a precise reaction | — |
| [Q-AUTH-30](spec/identity-and-authentication.md#q-auth-30--provider-entries-as-a-list-or-a-map) | Provider entries as a list or a map? | (b) a map keyed by the provider ID, with `order` | — |
| [Q-AUTH-31](spec/identity-and-authentication.md#q-auth-31--verifying-id-tokens-a-jose-library-or-a-minimal-verifier) | Verify ID tokens with a JOSE library or a minimal verifier? | (b) a minimal verifier on the standard library | — |
| [Q-AUTH-32](spec/identity-and-authentication.md#q-auth-32--provisioning-and-the-session-in-one-transaction-or-two) | Provisioning and the session in one transaction or two? | (b) two transactions, each with the principal row lock | — |
| [Q-AUTH-33](spec/identity-and-authentication.md#q-auth-33--an-enabled-break-glass-account-without-a-password-file) | An enabled break-glass account without a password file? | (c) enabled without a file means unusable | — |
| [Q-AUTH-34](spec/identity-and-authentication.md#q-auth-34--the-common-password-list-under-a-20-character-minimum) | Common-password list under a 20-character minimum? | (b) no list while the minimum is 20 | — |
| [Q-AUTH-35](spec/identity-and-authentication.md#q-auth-35--the-test-idp-of-the-dev-profile) | Test IdP of the dev profile? | (b) Keycloak with a realm import | — |
| [Q-AUTH-36](spec/identity-and-authentication.md#q-auth-36--where-the-claim-mapping-the-gates-and-the-group-rules-run) | Where do the claim mapping, the gates and the group rules run? | (a) the login maps and gates, the provisioner writes | — |
| [Q-AUTH-37](spec/identity-and-authentication.md#q-auth-37--the-event-of-a-break-glass-unlock) | Event of a break-glass unlock? | (a) `auth.breakglass.unlocked` | — |
| [Q-AUTH-38](spec/identity-and-authentication.md#q-auth-38--a-namespaced-group-rule-on-a-public-issuer) | Namespaced group rule on a public issuer? | (a) the literal prefix starts with a namespace, checked with `regexp.LiteralPrefix` | — |
| [Q-AUTH-39](spec/identity-and-authentication.md#q-auth-39--the-provider-label-of-the-group-claim-metric) | Provider label of the group-claim metric? | (a) `name` holds the provider ID | — |
| [Q-AUTH-40](spec/identity-and-authentication.md#q-auth-40--who-owns-the-shared-authentication-metrics-and-in-which-order-the-login-is-wired) | Who owns the shared authentication metrics, and in which order is the login wired? | (b) the identity area owns the families; `wireSessions` runs first | — |
| [Q-AUTH-41](spec/identity-and-authentication.md#q-auth-41--the-etag-of-the-runtime-authentication-settings) | ETag of the runtime authentication settings? | reversed by Q-AUTH-50: `auth_settings.revision` | — |
| [Q-AUTH-42](spec/identity-and-authentication.md#q-auth-42--a-disabled-service-account-keeps-its-tokens) | Does a disabled service account keep its tokens? | (b) tokens are not revoked; the disable call takes an optional `revokeTokens` | — |
| [Q-AUTH-43](spec/identity-and-authentication.md#q-auth-43--the-permission-of-each-identity-operation) | Permission of each identity operation? | (b) the capability of R-AUTHZ-010; self-service operations are `authenticated` | — |
| [Q-AUTH-44](spec/identity-and-authentication.md#q-auth-44--the-shape-of-whoami) | Shape of `whoami`? | (b) the page shape plus `principal.groups` and `credential.type: session` | — |
| [Q-AUTH-45](spec/identity-and-authentication.md#q-auth-45--the-settings-document-can-be-sent-back) | Can the settings document be sent back? | (b) runtime values at the top, `ceilings` and `status` next to them | — |
| [Q-AUTH-46](spec/identity-and-authentication.md#q-auth-46--an-idempotent-create-is-one-transaction-with-its-idempotency-row) | Is an idempotent create one transaction with its idempotency row? | (a) the creating methods take the transaction (`…Tx`); replaced (b) in M2, Q-AUTH-174 | — |
| [Q-AUTH-47](spec/identity-and-authentication.md#q-auth-47--creating-a-service-account-does-not-bind-a-role) | Does creating a service account bind a role? | (b) no flag on the create; the running manager has no `AdminBinder` | — |
| [Q-AUTH-48](spec/identity-and-authentication.md#q-auth-48--a-session-list-is-one-page) | Is a session list one page? | (b) `{items, nextCursor: null}` | — |
| [Q-AUTH-50](spec/identity-and-authentication.md#q-auth-50--users-groups-and-service-accounts-carry-a-revision-so-does-the-authentication-settings-row) | Do users, groups, service accounts and the settings carry a revision? | (c) one column on `principal`, one on `auth_settings` | — |
| [Q-AUTH-79](spec/identity-and-authentication.md#q-auth-79--how-the-end-to-end-tests-drive-the-browser-login-of-e11-and-disable-the-break-glass-account) | How do the end-to-end tests drive the browser login and disable the break-glass account? | (b) an HTTP client with a cookie jar for E1.1 and R-AUTH-042 | — |
| [Q-AUTH-89](spec/identity-and-authentication.md#q-auth-89--a-rotated-token-keeps-its-lineage-and-lineage-changes-are-serialised) | Does a rotated token keep its lineage? | (b) the successor keeps the lineage; lineage changes are serialised | — |
| [Q-AUTH-90](spec/identity-and-authentication.md#q-auth-90--authflow_starts-counts-an-ipv6-client-per-64) | `auth.flow_starts` for an IPv6 client: per what? | (b) an IPv6 address per `/64` | — |
| [Q-AUTH-151](spec/identity-and-authentication.md#q-auth-151--token-scope-patterns-are-validated-by-the-authorization-validators) | Who validates token scope patterns? | (b) the validators of `internal/authz`, wired by the composition root | — |
| [Q-AUTH-171](spec/identity-and-authentication.md#q-auth-171--a-break-glass-attempt-is-counted-before-it-is-verified) | Is a break-glass attempt counted before it is verified? | (c) reserve the attempt, then settle it; a refused attempt answers 429 with `Retry-After: 1` | — |
| [Q-AUTH-172](spec/identity-and-authentication.md#q-auth-172--a-session-is-created-only-for-an-active-principal) | Is a session created only for an active principal? | (b) return the status with the lock and refuse | — |
| [Q-AUTH-174](spec/identity-and-authentication.md#q-auth-174--the-identity-writes-take-the-callers-transaction-tx-forms) | How do the identity writes join an idempotent create's transaction? | (a) `…Tx` for the idempotent creates and rotations, the record read on the transaction | — |
| [Q-AUTH-175](spec/identity-and-authentication.md#q-auth-175--the-browser-login-routes-are-an-edge-package-over-plain-calls-of-the-login-domain) | Who writes the cookies, redirects and pages of `/auth/`? | (a) `internal/api/login` over plain calls of `oidc.Service` | — |
| [Q-AUTH-176](spec/identity-and-authentication.md#q-auth-176--the-local-login-is-admitted-before-its-body-is-read) | Where does the local login's limit and account check sit against the body? | (b) `AdmitLocalLogin` before the body, `LocalLogin` after | — |
| [Q-AUTHZ-1](spec/authorization.md#q-authz-1--endpoint-gate-on-virtual-repositories) | Does using a virtual repository require `read` on it? | (a) yes, as an endpoint gate that never grants member content | [ADR-053](#adr-053--virtual-repositories-endpoint-gate-plus-member-rights) |
| [Q-AUTHZ-2](spec/authorization.md#q-authz-2--ownership-enforcement-default-for-new-npm-and-nuget-local-repositories) | Ownership enforcement default for new npm and NuGet local repositories? | (a) `enforce` unless the create request says `off` | [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) |
| [Q-AUTHZ-3](spec/authorization.md#q-authz-3--built-in-principal-for-all-authenticated-users) | A built-in principal for all authenticated users? | (d) a read-only `authenticated` principal for users only, never service accounts or CI tokens, in the MVP | [ADR-020](#adr-020--deny-by-default-allow-only-permission-policies-inv-9) |
| [Q-AUTHZ-4](spec/authorization.md#q-authz-4--default-populate-action-of-remote-repositories) | Default cache-fill action of remote repositories? | (a) `read`; `deploy` per repository for frozen mirrors; anonymous populating is flagged. Revisited 2026-10-28, unchanged | [ADR-054](#adr-054--cache-fills-are-guarded-by-a-configurable-action-read-by-default) |
| [Q-AUTHZ-5](spec/authorization.md#q-authz-5--default-audit-level-for-reads) | Default audit level for reads? | (b) refused reads are audited; allowed reads only with `authz.audit.reads: all`. Revisited 2026-10-28, unchanged; its grounds now weigh a Valkey deduplication store | [ADR-080](#adr-080--audit-records-commit-with-the-change-they-record) |
| [Q-AUTHZ-6](spec/authorization.md#q-authz-6--default-ownership-of-ci-inline-grants-that-write) | Default ownership mode of CI inline grants that write? | (b) `bind_project`; any other inline grant is saved as `none` | [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) |
| [Q-AUTHZ-7](spec/authorization.md#q-authz-7--ci-delegations-and-p1-projects) | How do CI delegations relate to P1 projects? | (a) independent of projects in P1 | [ADR-058](#adr-058--projects-for-delegated-administration-reduced-in-p1) |
| [Q-AUTHZ-8](spec/authorization.md#q-authz-8--spec-page-for-build-metadata) | Which page owns build metadata? | (a) a new page `builds.md` (`R-BLD`); same choice as Q-STOR-3 and Q-API-5 | — |
| [Q-AUTHZ-9](spec/authorization.md#q-authz-9--shape-of-the-repository-part-of-a-token-scope) | Shape of the repository part of a token scope? | (a) a policy resource set (repositories, packages, paths) plus `actions` | [ADR-051](#adr-051--actions-are-explicit-and-a-token-scope-only-narrows) |
| [Q-AUTHZ-10](spec/authorization.md#q-authz-10--resource-types-a-token-scope-can-name) | Which resource types can a token scope name? | (b) the grant model's closed set `repositories`, `system`, `builds`, always intersected with the subject's rights | [ADR-051](#adr-051--actions-are-explicit-and-a-token-scope-only-narrows) |
| [Q-AUTHZ-11](spec/authorization.md#q-authz-11--invalidation-path-of-the-grant-index-across-n-replicas) | Invalidation path of the grant index across n replicas? | (c) the `authz` and `repo_config` change areas; one snapshot labelled with epochs and `changed_at`; a Valkey message only starts a confirmed rebuild | [ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed) |
| [Q-AUTHZ-12](spec/authorization.md#q-authz-12--seeing-ones-own-permission-change-under-random-request-distribution) | Seeing one's own permission change under random request distribution? | (b) the request header `Trove-Authz-Min-Epoch`, honoured after authentication; `412` for an epoch the database never reached | [ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed) |
| [Q-AUTHZ-13](spec/authorization.md#q-authz-13--a-replica-whose-grant-index-cannot-be-confirmed-current) | A replica whose grant index cannot be confirmed current? | (c) no decision on an index unconfirmed for more than 10 s; requests wait like database calls, then `503 authz_stale`; no own readiness gate | [ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed) |
| [Q-AUTHZ-14](spec/authorization.md#q-authz-14--policy-tables-in-m1-without-the-project-column) | Policy tables in M1 with the project column? | (b) leave the column out until projects arrive | — |
| [Q-AUTHZ-15](spec/authorization.md#q-authz-15--where-the-grant-index-reads-the-repositories-from) | Where does the grant index read the repositories from? | (b) rows in the build snapshot | — |
| [Q-AUTHZ-16](spec/authorization.md#q-authz-16--the-dev-profile-bridge-systembootstrap_anonymous_read-in-the-new-engine) | The dev-profile bridge `system.bootstrap_anonymous_read` in the new engine? | (b) a check in front of the engine, in `bridgeAccess`; it opens `settings:read` only | — |
| [Q-AUTHZ-17](spec/authorization.md#q-authz-17--how-trove-authz-min-epoch-reaches-the-engine) | How does `Trove-Authz-Min-Epoch` reach the engine? | (a) a field of `httpserver.Principal` | — |
| [Q-AUTHZ-18](spec/authorization.md#q-authz-18--event-names-of-the-decision-audit) | Event names of the decision audit? | (b) one type per delivery | — |
| [Q-AUTHZ-19](spec/authorization.md#q-authz-19--where-the-denial-deduplication-lives) | Where does the denial deduplication live? | (b) in the producer, at most 10,000 open keys per replica | — |
| [Q-AUTHZ-20](spec/authorization.md#q-authz-20--details-of-the-pattern-syntax-that-r-authz-005-leaves-open) | Pattern syntax details that R-AUTHZ-005 leaves open? | `releases/**` matches `releases`; `**` only as a whole segment; a package-name `*` does not cross `/`; case folding for NuGet only | — |
| [Q-AUTHZ-21](spec/authorization.md#q-authz-21--what-a-virtual-repository-does-with-manage-and-with-a-refused-gate-on-a-write) | What does a virtual repository do with `manage` and with a refused gate on a write? | `manage` is decided on the virtual repository; a refused write answers 403 | — |
| [Q-AUTHZ-22](spec/authorization.md#q-authz-22--the-permission-names-of-the-management-api) | Permission names of the management API: keep the alias? | (b) migrate and remove the alias | — |
| [Q-AUTHZ-23](spec/authorization.md#q-authz-23--authzhide_existence-in-m1) | `authz.hide_existence` in M1? | (b) implemented in the refusal | — |
| [Q-AUTHZ-24](spec/authorization.md#q-authz-24--the-denial-deduplication-lives-in-the-audit-writer-only) | Denial deduplication in the audit writer only, or in both stages? | (b) the writer alone | — |
| [Q-AUTHZ-25](spec/authorization.md#q-authz-25--one-pattern-compiler) | One pattern compiler? | (b) `repository.CompilePattern` | — |
| [Q-AUTHZ-26](spec/authorization.md#q-authz-26--a-policy-also-accepts-a-merge-patch) | Does a policy accept a merge patch? | (b) `PUT` and `PATCH`; `PATCH` needs `If-Match` (428 without it) | — |
| [Q-AUTHZ-27](spec/authorization.md#q-authz-27--the-effective-permissions-views-and-the-explainer-are-not-in-m1) | Effective-permissions views and the explainer in M1? | (a) not built in M1; the preview comes from `POST /authz/validate-policy` | — |
| [Q-AUTHZ-28](spec/authorization.md#q-authz-28--the-shapes-the-requirements-leave-open) | Shapes that the requirements leave open? | 409 `already_exists` for a duplicate policy name; no `project` before P1; validate-policy answers 200 with `valid`; `GET /roles` is a fixed list | — |
| [Q-AUTHZ-69](spec/authorization.md#q-authz-69--what-identitieswrite-may-change-on-a-principal-that-holds-a-system-role) | What may `identities:write` change on a principal that holds a system role? | (b) every change of a role holder needs `roles:write` | — |
| [Q-AUTHZ-70](spec/authorization.md#q-authz-70--what-last_admin-counts-and-which-changes-it-guards) | What does `last_admin` count, and which changes does it guard? | (b) active holders; every change that can end the role | — |
| [Q-AUTHZ-71](spec/authorization.md#q-authz-71--the-bootstrap-bridge-opens-two-operations-not-a-capability) | Does the bootstrap bridge open a capability or two operations? | (b) the two operations, in `internal/app/access.go` | — |
| [Q-AUTHZ-132](spec/authorization.md#q-authz-132--identity-and-repository-writes-that-change-grant-index-inputs-call-an-authz-owned-hook) | How do identity and repository writes notify the grant index? | (b) an authz-owned hook called in their transactions | — |
| [Q-AUTHZ-133](spec/authorization.md#q-authz-133--the-decision-point-looks-an-unknown-repository-up-itself) | Does the decision point look an unknown repository up itself? | (b) `authz.Engine` holds the `repository.Reader` | — |
| [Q-AUTHZ-134](spec/authorization.md#q-authz-134--a-change-message-starts-a-build-of-its-own-a-capability-probe-records-nothing-the-anonymous-switch-is-read-once) | Change message build, capability probe and the anonymous switch? | (1) a change message requests a `message` build; (2) `Holds` records no audit event; (3) the anonymous switch is read once | — |
| [Q-AUTHZ-152](spec/authorization.md#q-authz-152--the-time-of-the-repository-lookup-before-the-decision-is-not-promised) | Is the time of the repository lookup before a decision promised? | (d) the bytes are promised, the time is recorded | — |
| [Q-AUTHZ-159](spec/authorization.md#q-authz-159--a-request-takes-its-index-once-and-carries-it-in-a-holder) | How does a request decide every question on one index? | (a) `authz.Decider` with the `…On` forms; `edge.Access` holds the subject, the index and the decider | — |
| [Q-CI-1](spec/gitlab-ci-oidc.md#q-ci-1--first-match-per-tier-or-union-of-all-matching-mappings) | First match per tier, or union of all matching mappings? | (a) first match within a tier, union across tiers | [ADR-060](#adr-060--ordered-identity-mappings-first-match-per-tier-union-across-tiers) |
| [Q-CI-2](spec/gitlab-ci-oidc.md#q-ci-2--grammar-of-the-scope-request-parameter) | Grammar of the exchange's `scope` parameter? | (c) an RFC 6749 scope string as notation for the repository part of the R-AUTH-024 scope | [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint) |
| [Q-CI-3](spec/gitlab-ci-oidc.md#q-ci-3--default-for-id-tokens-with-several-audiences) | Accept ID tokens with several audiences? | (c) only if every audience is an allowed audience of the CI issuer | [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) |
| [Q-CI-4](spec/gitlab-ci-oidc.md#q-ci-4--single-exchange-per-jti-by-default) | Single exchange per `jti` by default? | (d) off for GitLab issuers, on for GitHub and generic issuers | [ADR-064](#adr-064--subject-token-reuse-is-recorded-single-use-depends-on-the-issuer-kind) |
| [Q-CI-5](spec/gitlab-ci-oidc.md#q-ci-5--build-the-ci_job_token-fallback-at-all) | Build the `CI_JOB_TOKEN` fallback at all? | (b) keep it specified, P2, built only on a concrete need; aligns INV-10's wording | [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) |
| [Q-CI-6](spec/gitlab-ci-oidc.md#q-ci-6--how-a-ci-project-acquires-package-ownership) | How does a CI project acquire package ownership? | (d) claim on first publish, with `bind_project` as the default of writing inline grants | [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) |
| [Q-CI-7](spec/gitlab-ci-oidc.md#q-ci-7--revoke-ci-tokens-when-the-gitlab-job-ends) | Revoke CI tokens when the GitLab job ends? | (c) the TTL bound as the guarantee, plus self-revocation as the last step of `script` | [ADR-063](#adr-063--a-ci-tokens-lifetime-is-bounded-by-its-job) |
| [Q-CI-8](spec/gitlab-ci-oidc.md#q-ci-8--status-code-for-rejected-subject-tokens) | Status code for rejected subject tokens? | (a) 400 `invalid_request` with a stable `reason`, in the OAuth error body | [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint) |
| [Q-CI-9](spec/gitlab-ci-oidc.md#q-ci-9--default-for-cross-project-fork-pipelines) | Default for cross-project (fork) pipelines? | (d) deny; an opt-in to `read_only` per mapping, for that mapping's grants only | [ADR-062](#adr-062--fork-pipelines-are-denied-by-default) |
| [Q-CI-10](spec/gitlab-ci-oidc.md#q-ci-10--principal-name-of-an-identity-mapping) | Principal name of an identity mapping? | (a) `<CI issuer name>/<mapping name>` | [ADR-060](#adr-060--ordered-identity-mappings-first-match-per-tier-union-across-tiers) |
| [Q-CI-11](spec/gitlab-ci-oidc.md#q-ci-11--replaying-a-mapping-disable-under-first-match) | How does restore replay disable a mapping under first match? | (b) disable it and suspend every later enabled mapping of its tier | [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) |
| [Q-CI-12](spec/gitlab-ci-oidc.md#q-ci-12--ci-issuer-key-sets-across-replicas-and-restores) | CI issuer key sets across replicas and restores? | (a) key sets: one PostgreSQL row per issuer, in-process copies, worker task `ci-keys-refresh`, key stores lock only the key row, unknown `kid` re-reads the row (throttled per issuer and replica to one read per poll interval) before a throttled per-replica fetch, `transient` in backups | [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) |
| [Q-CI-13](spec/gitlab-ci-oidc.md#q-ci-13--where-the-replay-set-lives) | Where does the replay set live? | (e) replay set in PostgreSQL only; after a restore single-use issuers refuse subject tokens whose `iat` is not later than `restored_at + clock_skew`, read in the exchange transaction | [ADR-064](#adr-064--subject-token-reuse-is-recorded-single-use-depends-on-the-issuer-kind) |
| [Q-CI-14](spec/gitlab-ci-oidc.md#q-ci-14--exchange-rate-limits-across-replicas) | Exchange rate limits across replicas? | (c) cluster-wide per-address (with `exempt_cidrs`) and per-subject limiters, per-issuer bucket removed | [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint), [ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback) |
| [Q-CI-15](spec/gitlab-ci-oidc.md#q-ci-15--exchange-against-a-mapping-cache-that-lags-behind-postgresql) | Exchange against a mapping cache that lags behind PostgreSQL? | (b) `mappings_version` check; an issuer `version` change re-validates the subject token against the re-read row | [ADR-060](#adr-060--ordered-identity-mappings-first-match-per-tier-union-across-tiers) |
| [Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6) | Add host-root routes next to INV-6? | (d) the exchange at npm's host-root path and below every registry URL; trove owns its host root; attestations later below the registry URL; changes INV-6 | [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6), [ADR-071](#adr-071--npm-trusted-publishing-through-package-bound-ci-tokens-p1) |
| [Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed) | Serve versions the upstream removed? | (a) no: removed versions and their tarballs are hidden by default; a per-remote switch for availability-first repositories. Revisited 2026-10-28, unchanged: marks read per request, the core gone mark only, and Q-NPM-13 | [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default) |
| [Q-NPM-3](spec/formats/npm.md#q-npm-3--audit-forwarding-on-by-default) | Forward `npm audit` to the upstream by default? | (c) forward, but only names the remote already holds a document for, minus internal names; withheld names are reported | [ADR-091](#adr-091--forwarded-search-and-audit-requests-withhold-protected-names) |
| [Q-NPM-4](spec/formats/npm.md#q-npm-4--current-client-majors-in-the-acceptance-gate) | Gate on the current npm and pnpm majors? | (c) rule-based gate rows; today npm 10–12, pnpm 10–12, Yarn 4 and Yarn 1.22 | [ADR-072](#adr-072--real-client-acceptance-matrices-gate-each-format) |
| [Q-NPM-5](spec/formats/npm.md#q-npm-5--upstream-representation-for-remote-repositories) | Which upstream representation do remotes fetch? | (a) the full document only; the abbreviated one is derived. Revisited 2026-10-28, unchanged: one flight per package, consistent side tables, the stale-copy rule inside the fill transaction | [ADR-068](#adr-068--npm-remotes-fetch-only-the-full-document) |
| [Q-NPM-6](spec/formats/npm.md#q-npm-6--manifest-consistency-verify-or-derive) | Verify the publish manifest against the tarball, or derive it? | (a) verify and reject, after the union of the clients' normalization steps (R-NPM-007) | — |
| [Q-NPM-7](spec/formats/npm.md#q-npm-7--git-dependencies-in-the-external-dependency-rewrite) | Rewrite git dependencies in the external dependency rewrite? | (a) only HTTP(S) tarball URLs, in P1 | — |
| [Q-NPM-8](spec/formats/npm.md#q-npm-8--purging-tombstones) | Offer a tombstone purge? | (a) no purge and no identical-bytes exception. Revisited 2026-10-28, unchanged: tombstones survive a restore through Q-NPM-12's withholding; a reinstate is not a purge | [ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones) |
| [Q-NPM-9](spec/formats/npm.md#q-npm-9--annotate-or-deploy-for-dist-tags-and-deprecation) | `annotate` or `deploy` for dist-tags and deprecation? | (b) `deploy` plus ownership | [ADR-052](#adr-052--changing-what-clients-install-needs-deploy-not-annotate) |
| [Q-NPM-10](spec/formats/npm.md#q-npm-10--where-npm-documents-are-cached-when-n-replicas-serve-them) | Where are npm documents cached when n replicas serve them? | (a) per-replica document caches under keys read from PostgreSQL, deterministic validators, no date validation | [ADR-116](#adr-116--npm-documents-are-cached-per-replica-under-keys-read-from-postgresql) |
| [Q-NPM-11](spec/formats/npm.md#q-npm-11--a-publish-retried-after-its-answer-was-lost) | A publish retried after its answer was lost? | (b) an acknowledged retry: same principal, bytes and dist-tag within the retry window; revisioned retries that change nothing succeed | [ADR-117](#adr-117--an-identical-npm-publish-retried-after-a-lost-answer-is-acknowledged) |
| [Q-NPM-12](spec/formats/npm.md#q-npm-12--unpublishes-made-after-a-restored-backups-snapshot) | Unpublishes made after a restored backup's snapshot? | (c) unpublish in the revocation journal; replay withholds and deletes nothing; an administrator reinstates or deletes | [ADR-118](#adr-118--a-restore-withholds-npm-versions-unpublished-after-the-snapshot) |
| [Q-NPM-13](spec/formats/npm.md#q-npm-13--removal-marks-after-a-restore-and-for-cached-tarballs) | Removal marks after a restore and for cached tarballs? | (c) a cached tarball waits for a current document after the metadata period or a restore | [ADR-119](#adr-119--cached-npm-tarballs-wait-for-a-current-package-document) |
| [Q-NPM-14](spec/formats/npm.md#q-npm-14--package-name-rules-the-reference-validator-leaves-open) | Which name rules does the reference validator leave open? | (b) the validator plus the leading-character rule on scope and name alike, and lookup hygiene (control characters, UTF-8, 512 bytes); the look-alike fold covers the whole name, ASCII only | — |
| [Q-NPM-15](spec/formats/npm.md#q-npm-15--which-version-grammar-applies-where-and-how-identifiers-compare) | Which version grammar applies where? | (b) three entry points: SemVer 2.0.0 as written for what trove decides, node-semver's own parse for upstream versions, `clean()`; numeric identifiers compare exactly at any size | — |
| [Q-NPM-16](spec/formats/npm.md#q-npm-16--version-ranges-of-exposure-rules-strictness-pre-releases-bound) | Strictness, pre-release rule and size of an exposure rule's version range? | (c) strict acceptance, evaluation with `includePrerelease` so the rule's `prereleases` flag decides, 4096-byte bound, acceptance checked in both option sets | — |
| [Q-NPM-17](spec/formats/npm.md#q-npm-17--limits-of-the-streaming-tarball-pass-and-of-manifest-reading) | Which limits bind the streaming tarball pass and manifest reading? | (b) one finite number per resource the pass touches: inflated bytes, names kept, gzip header, `package.json` size, depth and ratio | — |
| [Q-NPM-18](spec/formats/npm.md#q-npm-18--tarball-archives-that-two-readers-could-resolve-differently) | Which tar and gzip features does a publish refuse beyond R-NPM-007's path rules? | (b) everything two readers could resolve differently; links that stay inside the package are allowed | — |
| [Q-NPM-19](spec/formats/npm.md#q-npm-19--how-the-manifest-normalization-is-built-and-verified) | How is the manifest normalization built and verified? | (a) a port of the steps, replayed against npm's own `prepare()` over a committed corpus | — |
| [Q-NPM-41](spec/formats/npm.md#q-npm-41--where-the-test-upstream-gets-its-packages) | Where does the test upstream get its packages? | (c) a seeded synthetic corpus for scale plus a small recorded set for real shapes; byte-stable, offline | — |
| [Q-NPM-42](spec/formats/npm.md#q-npm-42--how-the-test-upstream-is-reached-and-controlled) | How is the test upstream reached and controlled? | (b) TLS only; the control API on the same listener below `/-/stub/` behind a bearer token, off without a token | — |
| [Q-NPM-43](spec/formats/npm.md#q-npm-43--which-npm-writes-the-reference-lockfile-and-how-it-is-kept-honest) | Which npm writes the reference lockfile? | (b) the npm of the pinned Node image against the stub in containers, a Go test of the lockfile against the corpus, and a regeneration check | — |
| [Q-NPM-44](spec/formats/npm.md#q-npm-44--how-the-end-to-end-suite-gets-the-eight-clients) | How does the e2e suite get the eight clients? | (a) one image of npm aliases locked in `package-lock.json`, clients in cluster pods behind a forwarder on the base-URL port | — |
| [Q-NPM-45](spec/formats/npm.md#q-npm-45--the-nodejs-the-clients-run-on) | Which Node.js do the clients run on? | (a) the repository's Node.js (`web/.nvmrc`) for all eight rows, digest-pinned glibc image; every `engines` range accepts it | — |
| [Q-NUGET-1](spec/formats/nuget.md#q-nuget-1--default-meaning-of-a-nuget-delete) | Default meaning of a NuGet delete? | (a) the protocol `DELETE` unlists unless the repository sets `deleteMode: delete`; management, UI and raw deletes are hard deletes | [ADR-090](#adr-090--a-nuget-protocol-delete-unlists-by-default) |
| [Q-NUGET-2](spec/formats/nuget.md#q-nuget-2--metadata-cache-period-of-nuget-remotes) | Metadata cache period of NuGet remotes? | (b) 300 s as the NuGet format default; 1800 s stays for not-found entries. Revisited 2026-10-28, unchanged: refreshes collapse through Valkey, and degrade to one per replica while Valkey is unavailable | [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving) |
| [Q-NUGET-3](spec/formats/nuget.md#q-nuget-3--non-ascii-package-ids) | Allow non-ASCII package IDs? | (a) ASCII-restricted IDs for everything entering a local repository; upstream IDs keyed by the client's lowercased path | [ADR-074](#adr-074--nuget-package-ids-ascii-on-push-case-insensitive-first-casing-displayed) |
| [Q-NUGET-4](spec/formats/nuget.md#q-nuget-4--forwarding-search-to-the-upstream) | Forward search to the upstream? | (c) forward by default, switchable per remote; never forward a query naming a protected ID | [ADR-091](#adr-091--forwarded-search-and-audit-requests-withhold-protected-names) |
| [Q-NUGET-5](spec/formats/nuget.md#q-nuget-5--id-casing-on-later-pushes) | ID casing on later pushes? | (a) any casing is accepted; the creating version's casing stays the display name | [ADR-074](#adr-074--nuget-package-ids-ascii-on-push-case-insensitive-first-casing-displayed) |
| [Q-NUGET-6](spec/formats/nuget.md#q-nuget-6--overwriting-an-existing-version) | May an existing version be overwritten? | (d) never: versions are immutable for the repository's lifetime, and every removal, moves included, leaves a tombstone; a protocol push of the identical bytes of a stored version answers 201 without change. Revisited 2026-10-28: was (c) | [ADR-067](#adr-067--npm-and-nuget-versions-are-immutable-guarded-by-tombstones) |
| [Q-NUGET-7](spec/formats/nuget.md#q-nuget-7--patterns-and-repository-level-upstream-documents) | Do patterns apply to repository-level upstream documents? | (a) package-less requests are admitted; symbol requests are routed by the file name in the symbol key | [ADR-037](#adr-037--one-name-pattern-syntax-most-specific-wins-applied-to-every-protocol-request) |
| [Q-NUGET-8](spec/formats/nuget.md#q-nuget-8--forwarded-search-results-across-replicas) | Forwarded search results across replicas? | (a) a per-replica forwarded-search cache, unfiltered, filtered on every use; nothing shared | [ADR-120](#adr-120--forwarded-nuget-search-results-are-cached-per-replica) |
| [Q-NUGET-9](spec/formats/nuget.md#q-nuget-9--etags-of-rendered-documents-on-replicas-of-different-releases) | ETags of rendered documents on replicas of different releases? | (c) weak ETags over a render version and the inputs; virtual inputs taken after pin rules and claims | [ADR-121](#adr-121--rendered-nuget-documents-carry-weak-etags) |
| [Q-NUGET-10](spec/formats/nuget.md#q-nuget-10--versions-changed-after-a-backups-snapshot) | Versions changed after a backup's snapshot? | (b) journal replay with `reserved` and `removed` tombstones and a `revived` report; no reservation without the replay | [ADR-122](#adr-122--a-restore-replays-nuget-publishes-and-deletes-into-tombstones) |
| [Q-NUGET-11](spec/formats/nuget.md#q-nuget-11--where-symbol-indexing-runs) | Where does symbol indexing run? | (b) one `nuget-symbol-index` job per symbol package on the `worker` role | [ADR-123](#adr-123--nuget-symbol-packages-are-indexed-by-one-job-each-on-the-worker-role) |
| [Q-GENERIC-1](spec/formats/generic.md#q-generic-1--the-format-is-generic-flat-paths-and-the-descriptor-lists-only-implemented-classes) | Is the format `generic` with flat paths, and which classes does the descriptor list? | (a) the implemented classes only | — |
| [Q-GENERIC-2](spec/formats/generic.md#q-generic-2--path-limits-of-the-generic-format) | Path limits of the generic format? | (a) 4096 bytes per path, 255 bytes per segment, valid UTF-8 | — |
| [Q-GENERIC-3](spec/formats/generic.md#q-generic-3--a-path-is-a-file-or-a-folder-not-both) | May a path be a file and a folder at once? | (a) refuse with 409 `path_conflict` | — |
| [Q-GENERIC-4](spec/formats/generic.md#q-generic-4--paths-outside-the-include-and-exclude-patterns-answer-404-on-read) | Paths outside the include and exclude patterns on read? | (a) 404 on `GET`, `HEAD` and `DELETE` | — |
| [Q-GENERIC-5](spec/formats/generic.md#q-generic-5--a-generic-virtual-repository-accepts-members-of-every-format) | Which members does a generic virtual repository accept? | (a) members of any format | — |
| [Q-GENERIC-6](spec/formats/generic.md#q-generic-6--a-replacement-answers-201-and-a-folder-without-its-slash-is-redirected) | Status of a replacement, and a folder without its slash? | (b) 201 when the bytes are new for the path, 200 when nothing changes; `301` for the folder | — |
| [Q-BAK-1](spec/backup-and-restore.md#q-bak-1--form-of-the-database-part-of-a-backup) | Form of the database part of a backup? | (b) a trove-native `COPY` export of every table from one `REPEATABLE READ READ ONLY` snapshot, restored through trove's own migrations | [ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold) |
| [Q-BAK-2](spec/backup-and-restore.md#q-bak-2--keeping-a-running-backups-blobs-away-from-garbage-collection) | Keeping a running backup's blobs away from garbage collection? | (c) a global hold until the to-copy set is pinned, then per-blob pins, both under the run's lease | [ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold), [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) |
| [Q-BAK-3](spec/backup-and-restore.md#q-bak-3--blob-layout-in-the-target-and-incremental-backups) | Blob layout in the target and incremental backups? | (b) a shared content-addressed pool, one sorted blob list per backup, incremental by digest, mark-and-sweep retention | [ADR-106](#adr-106--backup-format-a-trove-native-snapshot-export-a-shared-blob-pool-and-a-garbage-collection-hold) |
| [Q-BAK-4](spec/backup-and-restore.md#q-bak-4--where-run-state-and-the-one-at-a-time-rule-live) | Where do run state and the one-at-a-time rule live? | (b) a `backup_run` row with a partial unique index and a lease, with the CronJob's `Forbid` as the first line | [ADR-107](#adr-107--backup-runs-one-at-a-time-in-a-leased-row-cancelled-when-they-stall) |
| [Q-BAK-5](spec/backup-and-restore.md#q-bak-5--derived-state-that-outlives-the-old-database) | Derived state that outlives the old database? | (c) a new Valkey keyspace ID and an offset of 2^32 on every registered revision column | [ADR-111](#adr-111--a-restore-moves-valkey-to-a-new-keyspace-and-offsets-every-revision) |
| [Q-BAK-6](spec/backup-and-restore.md#q-bak-6--encryption-of-backup-contents) | Encryption of backup contents? | (b) server-side encryption (`s3`) at MVP; client-side encryption of the database files to age recipients at P1 | [ADR-109](#adr-109--backup-encryption-server-side-at-mvp-client-side-for-the-database-files-at-p1) |
| [Q-BAK-7](spec/backup-and-restore.md#q-bak-7--restore-target-empty-installation-only-or-reuse-of-an-existing-blob-store) | Restore into an empty installation only, or reuse an existing blob store? | (a) empty database and empty blob prefix at MVP; reuse at P1 | [ADR-110](#adr-110--restore-an-empty-installation-any-later-release-resumable-as-an-ordinary-job) |
| [Q-BAK-8](spec/backup-and-restore.md#q-bak-8--restore-with-a-newer-trove-release) | Restore with a newer trove release? | (b) any release that embeds every step of the backup with the same checksums: migrate to the step, load, migrate forward | [ADR-110](#adr-110--restore-an-empty-installation-any-later-release-resumable-as-an-ordinary-job) |
| [Q-BAK-9](spec/backup-and-restore.md#q-bak-9--backup-cronjob-on-by-default) | Backup CronJob on by default? | (b) on by default; rendering fails without a target bucket unless backups are disabled deliberately; same choice as Q-HA-17 | [ADR-112](#adr-112--the-backup-cronjob-is-on-by-default-and-needs-its-own-target) |
| [Q-BAK-10](spec/backup-and-restore.md#q-bak-10--resuming-an-interrupted-restore) | Resuming an interrupted restore? | (c) automatic resumption keyed to the backup ID through a schema anchor, with the migration lock held for the whole restore | [ADR-110](#adr-110--restore-an-empty-installation-any-later-release-resumable-as-an-ordinary-job) |
| [Q-BAK-11](spec/backup-and-restore.md#q-bak-11--bounding-a-hung-backup-run) | Bounding a hung backup run? | (c) the run cancels itself after `backup.stall_timeout` (30 minutes) without progress; no `activeDeadlineSeconds` by default | [ADR-107](#adr-107--backup-runs-one-at-a-time-in-a-leased-row-cancelled-when-they-stall) |
| [Q-BAK-12](spec/backup-and-restore.md#q-bak-12--the-backup-command-in-m0) | What does `trove backup` do in M0? | (c) the name is reserved; every subcommand returns "not implemented yet" | — |
| [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure) | Readiness on object-storage failure? | (d) gate only the first readiness; continuous gating as an opt-in; changes INV-12; same choice as Q-STOR-1 and Q-OPS-2 | [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12) |
| [Q-HA-2](spec/ha-and-helm.md#q-ha-2--postgresql-version-floor) | PostgreSQL version floor? | (b) 16; raised only after a version's community end of life, announced one minor release ahead | [ADR-003](#adr-003--postgresql-as-the-database), [ADR-014](#adr-014--postgresql-is-the-system-of-record-valkey-holds-only-shared-ephemeral-state-inv-3) |
| [Q-HA-3](spec/ha-and-helm.md#q-ha-3--s3-component-of-the-local-profile-after-minios-archival) | Which S3 store for the local profile after MinIO's archival? | (e) SeaweedFS in the local profile and per-change CI; MinIO and AWS S3 nightly. Owner decision of 2026-10-29: the MinIO leg on every change, AWS S3 once an AWS account exists | [ADR-004](#adr-004--s3-as-binary-storage), [ADR-130](#adr-130--the-organisations-ci-and-release-standard) |
| [Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci) | Reserve `/v2/` on the client listener for OCI? | (a) reserved in INV-6 from the MVP on, answering `404` until the OCI format exists; changes INV-6 | [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6) |
| [Q-HA-5](spec/ha-and-helm.md#q-ha-5--zero-downtime-upgrades-that-skip-minor-releases) | Zero-downtime upgrades that skip minor releases? | (a) adjacent minors only; skips go stepwise or with downtime, enforced by the contract guard | [ADR-033](#adr-033--versioning-promises-per-surface-and-a-schema-compatibility-gate) |
| [Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers) | Support transaction-mode connection poolers? | (b) supported from the MVP on and optional: the main pool through a transaction-mode pooler, `database.session_host` for the `worker` leases, `trove migrate` and `trove backup`; direct connections stay the default. Revisited 2026-10-28: was (a), not supported, with split connections at P2 | [ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers), [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12), [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing) |
| [Q-HA-7](spec/ha-and-helm.md#q-ha-7--priority-of-gateway-api) | Priority of Gateway API? | (b) the HTTPRoute in the MVP, a chart-created Gateway in P1 | [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) |
| [Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key) | Who creates the first encryption key? | (b) the operator; the chart fails without it and prints the command that creates one | [ADR-032](#adr-032--stored-secrets-are-sealed-under-a-key-ring-the-operator-supplies), [ADR-079](#adr-079--the-chart-validates-before-it-renders-and-never-creates-secrets) |
| [Q-HA-9](spec/ha-and-helm.md#q-ha-9--making-revocations-survive-a-database-restore-h-37) | Make revocations survive a database restore? | (b) at MVP: a write-once revocation journal in object storage that `post-restore` and the restore of a trove backup replay. Revisited 2026-10-28: was P1, with the manual procedure in the MVP | [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) |
| [Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy) | Default of the egress NetworkPolicy? | (a) off, with a `NOTES.txt` warning and an example values file | [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13), [ADR-039](#adr-039--egress-address-checks-after-dns-resolution-for-every-remote-connection) |
| [Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart) | Where are images and the chart published? | (a) `ghcr.io/guided-traffic`, signed from the GitHub Actions release workflow. Owner decision of 2026-10-29 (overrides (a)): Docker Hub for the image, the GitHub Pages chart repository for the chart, provenance and SBOM attestations, no signatures | [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13), [ADR-130](#adr-130--the-organisations-ci-and-release-standard) |
| [Q-HA-12](spec/ha-and-helm.md#q-ha-12--replica-registry-at-mvp) | Replica registry at MVP? | (c) table, heartbeat, pruning and read-only API in the MVP, and the UI status screen (R-UI-038); since 2026-10-28 the registry also records each replica's node and reachable Sentinels (Q-UI-7) | [ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2) |
| [Q-HA-13](spec/ha-and-helm.md#q-ha-13--valkey-and-readiness) | Valkey and readiness? | (d) only configuration errors (rejected credential, failed TLS verification, unknown primary name) hold back the first readiness; outages never affect readiness | [ADR-100](#adr-100--valkey-degrades-trove-but-never-gates-readiness-except-for-configuration-errors), [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12) |
| [Q-HA-14](spec/ha-and-helm.md#q-ha-14--valkey-topologies-trove-supports) | Which Valkey topologies does trove support? | (b) Sentinel only in production at MVP; standalone for development; a primary-endpoint mode at P1; Valkey Cluster deferred | [ADR-099](#adr-099--valkey-client-valkey-go-against-valkey-90-and-later-sentinel-in-production) |
| [Q-HA-15](spec/ha-and-helm.md#q-ha-15--valkey-with-sentinel-in-the-local-profile-and-ci) | Valkey with Sentinel in the local profile and CI? | (d) trove's own manifests, `deploy/dev/valkey.yaml`, from the official image; superseded for the dev profile on 2026-10-29 by the owner's valkey-operator (option c, with the gaps recorded) | [ADR-099](#adr-099--valkey-client-valkey-go-against-valkey-90-and-later-sentinel-in-production), [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13), [ADR-129](#adr-129--the-owners-kubernetes-operators-are-the-standard-stack) |
| [Q-HA-16](spec/ha-and-helm.md#q-ha-16--default-replica-counts-and-the-capacity-rule-for-drains) | Default replica counts and the capacity rule for drains? | (b) three `web` and two `worker` replicas by default; the N + 1 rule of R-HA-059 documented and tested | [ADR-105](#adr-105--drain-capacity-one-replica-more-than-the-peak-needs-per-role) |
| [Q-HA-17](spec/ha-and-helm.md#q-ha-17--the-backup-cronjob-is-on-by-default) | Is the backup CronJob on by default? | (a) on by default; rendering fails without a backup target; same choice as Q-BAK-9 | [ADR-112](#adr-112--the-backup-cronjob-is-on-by-default-and-needs-its-own-target), [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) |
| [Q-HA-18](spec/ha-and-helm.md#q-ha-18--how-a-restore-runs-in-the-chart) | How does a restore run in the chart? | (b) an ordinary Job with both roles at zero, no migration Job and a suspended backup CronJob, while `restore.enabled` | [ADR-110](#adr-110--restore-an-empty-installation-any-later-release-resumable-as-an-ordinary-job), [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) |
| [Q-HA-19](spec/ha-and-helm.md#q-ha-19--priority-of-the-platform-backup-layer-and-the-point-in-time-restore) | Priority of the platform backup layer and the point-in-time restore? | (c) R-HA-023 and R-HA-025 are MVP; the platform layer stays a recommendation for installations | [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) |
| [Q-HA-20](spec/ha-and-helm.md#q-ha-20--control-connection-behind-a-transaction-mode-pooler) | Control connection behind a transaction-mode pooler? | (c) a control endpoint with a server pool of its own; the chart refuses one equal to the request endpoint | [ADR-101](#adr-101--connection-model-a-transaction-scoped-main-pool-behind-an-optional-pooler-sessions-only-on-workers), [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12) |
| [Q-HA-21](spec/ha-and-helm.md#q-ha-21--task-runner-and-tool-pin-file) | Which task runner, and where are the tool pins? | (a) GNU Make in the root `Makefile`; one pin file `tools/versions.env` read by the Makefile and by CI | [ADR-125](#adr-125--gnu-make-and-one-pin-file-for-every-tool) |
| [Q-HA-22](spec/ha-and-helm.md#q-ha-22--upload-limit-against-the-spool-of-each-role) | How does the upload limit relate to the spool of each role? | (c) the rule holds for the roles `web` and `all`; the worker spool is 5 GiB | — |
| [Q-HA-23](spec/ha-and-helm.md#q-ha-23--chart-validation-of-the-config-subtree-until-the-binary-emits-its-schema) | How does the chart validate the `config` subtree? | (b) free-form in `values.schema.json`, the chart-owned keys typed, until the binary emits its schema | — |
| [Q-HA-24](spec/ha-and-helm.md#q-ha-24--edge-controller-failover-mechanism-and-the-scatter-profile-of-the-local-profile) | Which edge, failover mechanism and load profile does the local profile use? | (a) Traefik as Ingress controller; `TROVE_PROFILE=scatter` without idle backend connections; failover patches `status.targetPrimary`. Revisited 2026-10-29: the edge is the owner's HAProxy Ingress, the failover choice stands | [ADR-129](#adr-129--the-owners-kubernetes-operators-are-the-standard-stack) |
| [Q-HA-25](spec/ha-and-helm.md#q-ha-25--aws-leg-of-the-nightly-run-when-no-aws-account-is-configured) | What does the nightly AWS leg show without an account? | (c) an explicit SKIPPED summary; the AWS job is grey, never green. Owner decision of 2026-10-29: no nightly run, and no AWS leg until an account exists; (c) is not built | [ADR-130](#adr-130--the-organisations-ci-and-release-standard) |
| [Q-HA-26](spec/ha-and-helm.md#q-ha-26--release-image-how-the-binary-gets-into-the-image) | How does the binary get into the release image? | (b) the host builds the static binary with the embedded UI; the image wraps it in the pinned distroless base | — |
| [Q-HA-27](spec/ha-and-helm.md#q-ha-27--which-replicas-the-contract-guard-counts-as-live) | Which replicas count as live for the contract guard? | (b) those whose heartbeat is younger than `system.heartbeat_stale_after` | — |
| [Q-HA-28](spec/ha-and-helm.md#q-ha-28--who-creates-the-database-schema-at-migration) | Who creates the database schema? | (b) `trove migrate` creates `database.schema` when the role may; never roles or databases | — |
| [Q-HA-29](spec/ha-and-helm.md#q-ha-29--where-the-garbage-collection-pause-is-stored) | Where is the garbage-collection pause stored? | (b) a key-value row `deletions_paused_at` in `system_state`; R-HA-025's sketch settled at M5 | — |
| [Q-HA-30](spec/ha-and-helm.md#q-ha-30--plaintext-switches-of-the-valkey-connection) | Which plaintext switches does the Valkey connection have? | (b) each requires `valkey.allow_insecure: true`; `key_prefix` ends in `:` | — |
| [Q-HA-31](spec/ha-and-helm.md#q-ha-31--job-heartbeat-attempt-counting-and-the-slot-record) | When does a failed lease extension cancel a job, and what counts an attempt? | (b) cancel at once when the row is taken, otherwise once the lease could have expired; claims count attempts; `job_task_slot` records slots | — |
| [Q-HA-32](spec/ha-and-helm.md#q-ha-32--default-of-the-charts-tls-wiring) | Is the chart's TLS wiring on by default? | (b) `tls.enabled` defaults to `true`; rendering fails without the certificate Secret unless `tls.enabled: false` | [ADR-127](#adr-127--transport-encryption-on-every-connection-troves-listeners-included) |
| [Q-HA-33](spec/ha-and-helm.md#q-ha-33--sentinel-trust-settings-warn-or-refuse-at-start) | Are the Sentinel trust settings warned about or refused at start? | (b) warn once at start; never refuse; no strictness switch yet | [ADR-129](#adr-129--the-owners-kubernetes-operators-are-the-standard-stack) |
| [Q-HA-64](spec/ha-and-helm.md#q-ha-64--how-the-end-to-end-tests-of-the-access-core-hold-valkey-down-reach-a-replica-directly-and-get-the-login-configuration-into-the-dev-profile) | How do the end-to-end tests hold Valkey down, and how is the login configuration put into the dev profile? | (b) a stop script and a start script the test calls; the latency injection is `CLIENT PAUSE` on the primary for 4 s | — |
| [Q-OPS-1](spec/operations-and-observability.md#q-ops-1--couple-liveness-to-prolonged-unreadiness) | Couple liveness to prolonged unreadiness? | (d) never; liveness is a watchdog over trove's own loops | [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12) |
| [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness) | Should object-storage reachability gate readiness? | (d) gate only the first readiness, then report and degrade; continuous gating as an opt-in; changes INV-12; same choice as Q-STOR-1 and Q-HA-1 | [ADR-023](#adr-023--high-availability-and-horizontal-scale-on-ha-postgresql-ha-valkey-and-s3-inv-12) |
| [Q-OPS-3](spec/operations-and-observability.md#q-ops-3--anonymous-access-to-the-version-endpoint) | Anonymous access to the version endpoint? | (b) authenticated by default; `system.public_version: true` opens it | — |
| [Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records) | Tamper evidence for audit records? | (c) append-only for the runtime role, partitions and retention through an owner-held function, plus external shipping | [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16), [ADR-080](#adr-080--audit-records-commit-with-the-change-they-record) |
| [Q-OPS-5](spec/operations-and-observability.md#q-ops-5--cloudevents-type-prefix) | CloudEvents `type` prefix? | (a) `trove.`, frozen from the first release that ships webhooks | [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) |
| [Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents) | May configuration documents declare the human-login OIDC client? | (a) no: configuration file only, claim-to-group rules included; same choice as Q-AUTH-1 | [ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15), [ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1) |
| [Q-OPS-7](spec/operations-and-observability.md#q-ops-7--one-cron-syntax-for-the-whole-system) | One cron syntax for the whole system? | (a) five-field Kubernetes CronJob syntax with macros, in UTC, through one parser; same choice as Q-STOR-4. Revisited 2026-10-28: (e) added for the backup CronJob, whose schedule the chart validates against the same rejected forms, with `backup.timeZone` defaulting to `Etc/UTC` | [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) |
| [Q-OPS-8](spec/operations-and-observability.md#q-ops-8--locking-objects-managed-by-configuration-documents) | Lock objects managed by configuration documents? | (a) locked by default, with one global switch | [ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1) |
| [Q-OPS-9](spec/operations-and-observability.md#q-ops-9--ownership-of-the-job-api) | Which page owns the job API? | (a) operations keeps the API, raised to MVP; architecture.md keeps the model | [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work) |
| [Q-OPS-10](spec/operations-and-observability.md#q-ops-10--default-cap-for-the-repo-metric-label) | Default cap for the `repo` metric label? | (a) with (d): 500 distinct repository keys per metric family and replica, the same keys on every replica, chosen from the repository snapshot; `_other` beyond it. Revisited 2026-10-28: (d) added | [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16) |
| [Q-OPS-11](spec/operations-and-observability.md#q-ops-11--template-language-conditionals-loops-and-functions) | Which template language for webhook requests? | (b) restricted `text/template`: conditionals, at most 16 non-nested `range` actions, a fixed function set | [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) |
| [Q-OPS-12](spec/operations-and-observability.md#q-ops-12--what-readiness-means-on-a-worker-replica) | What does readiness mean on a `worker` replica? | (c) the same checks as `web`, and the claim loop claims only while the replica is ready | [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases) |
| [Q-OPS-13](spec/operations-and-observability.md#q-ops-13--how-a-series-names-its-role-and-pool) | How does a series name its role and pool? | (b) target labels `role` and `pool` from the ServiceMonitor; `trove_build_info{role}` for scrapers that do not relabel | [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16), [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases) |
| [Q-OPS-14](spec/operations-and-observability.md#q-ops-14--audit-deduplication-across-replicas) | Audit deduplication across replicas? | (a) per replica; at most n first events and n summaries per key and window; the sum of `details.count` stays exact | [ADR-080](#adr-080--audit-records-commit-with-the-change-they-record) |
| [Q-OPS-15](spec/operations-and-observability.md#q-ops-15--alerting-on-valkey-failovers) | Alerting on Valkey failovers? | (c) no alert for a single failover; alerts for repeated failovers and for replicas that disagree about the primary | [ADR-100](#adr-100--valkey-degrades-trove-but-never-gates-readiness-except-for-configuration-errors) |
| [Q-OPS-16](spec/operations-and-observability.md#q-ops-16--flushing-caches-across-the-installation) | Flushing caches across the installation? | (b) an installation-wide flush through PostgreSQL at P1; at MVP, deleting trove's Valkey keys is stated as always safe | [ADR-100](#adr-100--valkey-degrades-trove-but-never-gates-readiness-except-for-configuration-errors) |
| [Q-OPS-17](spec/operations-and-observability.md#q-ops-17--flight-locks-in-the-lock-diagnostics) | Flight locks in the lock diagnostics? | (a) the lock view shows PostgreSQL advisory locks only; collapsed fetches are diagnosed through metrics, the leader's upstream record and `Cache-Status` | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) |
| [Q-OPS-18](spec/operations-and-observability.md#q-ops-18--gauges-read-from-postgresql-on-n-replicas) | Gauges read from PostgreSQL on n replicas? | (b) every `web` and `worker` replica evaluates them in the background once per `metrics.db_gauge_interval`; scrapes return the last values | [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16) |
| [Q-OPS-19](spec/operations-and-observability.md#q-ops-19--per-subscription-delivery-limit-across-worker-replicas) | Per-subscription delivery limit across `worker` replicas? | (b) the claim locks the subscription row with `SKIP LOCKED`, counts running deliveries and claims below the limit | [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) |
| [Q-OPS-20](spec/operations-and-observability.md#q-ops-20--final-event-for-a-successful-delivery-that-is-not-recorded) | Final event for a successful delivery that is not recorded? | (b) a tombstone row with the final state for 15 minutes, shown only by the follow stream | [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) |
| [Q-OPS-21](spec/operations-and-observability.md#q-ops-21--position-of-the-delivery-log-follow-stream) | Position of the delivery-log follow stream? | (b) `change_seq` from a sequence on every insert and update, with a re-read window and duplicates dropped by record and version | [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) |
| [Q-OPS-22](spec/operations-and-observability.md#q-ops-22--journal-flag-on-event-types-that-several-producers-share) | Journal flag on event types that several producers share? | (d) "journaled when the producer supplies an entry"; required for every flagged type except the allow-listed shared types `package.published` and `package.deleted` | [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) |
| [Q-OPS-23](spec/operations-and-observability.md#q-ops-23--configuration-file-that-is-not-there) | What happens when the configuration file is missing? | (b) the default file may be absent; an explicit `--config` or `TROVE_CONFIG_FILE` must exist | — |
| [Q-OPS-24](spec/operations-and-observability.md#q-ops-24--required-keys-per-command-reserved-variable-names-and-source-labels) | Which keys does each command require, and which variable names are reserved? | (b) required keys per command; `TROVE_CAC_*` and `TROVE_TEST_*` reserved; source labels printed | — |
| [Q-OPS-25](spec/operations-and-observability.md#q-ops-25--otlp-span-export-own-queue-transport-and-endpoint-rule) | How are spans queued and exported? | (b) an own bounded queue with a drop counter in front of the SDK's OTLP exporter; the endpoint is optional with the OTel variables | — |
| [Q-OPS-26](spec/operations-and-observability.md#q-ops-26--metric-label-names-of-the-outbound-and-the-object-store-metrics) | Which label names do the outbound and object-store metrics use? | (b) `kind` for the outbound purpose; `operation` for the object-store requests | — |
| [Q-OPS-27](spec/operations-and-observability.md#q-ops-27--secret-values-in-configuration-dumps-and-logs) | How are secret values kept out of dumps and logs? | (b) `config.Secret` implements `MarshalJSON` and `LogValue` | — |
| [Q-OPS-28](spec/operations-and-observability.md#q-ops-28--instance-name-source) | Where does the instance name come from? | (b) `TROVE_POD_NAME`, `POD_NAME`, `HOSTNAME` inside Kubernetes, otherwise `<hostname>-<pid>` | — |
| [Q-OPS-29](spec/operations-and-observability.md#q-ops-29--a-replica-started-on-a-database-that-is-not-yet-migrated) | What does a replica do on a database that is not yet migrated? | (b) wait, not ready, until the schema check passes; a schema below the floor stops the start | — |
| [Q-OPS-30](spec/operations-and-observability.md#q-ops-30--ping-while-the-replica-drains) | What does ping answer while a replica drains? | (b) `200` while the replica serves, the drain phase included; `503` when it is unready for another reason or never was ready | — |
| [Q-OPS-31](spec/operations-and-observability.md#q-ops-31--database-readiness-during-a-short-outage) | How does database readiness treat a short PostgreSQL outage? | (c) `health.database_outage_grace` (20 s): a ready replica stays ready, `degraded`, for an outage shorter than that | — |
| [Q-OPS-32](spec/operations-and-observability.md#q-ops-32--trace-trust-for-an-incoming-traceparent) | Which peers may continue an incoming trace context? | (a) `tracing.trusted_peers`, empty by default; `server.trusted_proxies` grants no trace trust | — |
| [Q-OPS-33](spec/operations-and-observability.md#q-ops-33--bounding-the-in-memory-log-ring-by-bytes) | How is the in-memory log ring bounded? | (b) by records and by `log.ring_buffer_bytes`; a record above 16 KiB is kept as a stub | — |
| [Q-OPS-34](spec/operations-and-observability.md#q-ops-34--the-invalidation-check-and-the-outage-grace) | Invalidation check and the outage grace? | (b) the same grace when the cause is the database | — |
| [Q-OPS-35](spec/operations-and-observability.md#q-ops-35--tls-to-the-span-collector) | TLS to the span collector? | (b) `https` unless `tracing.allow_insecure` | — |
| [Q-OPS-36](spec/operations-and-observability.md#q-ops-36--bootstrap-anonymous-read-is-the-default-endpoints) | Bootstrap anonymous read: which endpoints? | (b) the default endpoint only, and only while it admits anonymous requests | — |
| [Q-OPS-37](spec/operations-and-observability.md#q-ops-37--spelling-of-the-authentication-event-names) | Spelling of the authentication event names? | (a) the names of R-AUTH-041 | — |
| [Q-OPS-38](spec/operations-and-observability.md#q-ops-38--the-first-audit-partitions-and-the-retention-row-come-from-the-migration) | Where do the first audit partitions and the retention row come from? | (b) the migration seeds the row and creates the first partitions | — |
| [Q-OPS-39](spec/operations-and-observability.md#q-ops-39--where-the-retention-write-and-the-partition-call-belong) | Where do the retention write and the partition call belong? | (a) the migrate Job writes it, with a reconciliation at start-up | — |
| [Q-OPS-40](spec/operations-and-observability.md#q-ops-40--the-event-carries-every-column-of-the-table) | Does the event carry every column of the audit table? | (a) the columns are added to the contract | — |
| [Q-OPS-41](spec/operations-and-observability.md#q-ops-41--what-the-asynchronous-writer-does-while-postgresql-does-not-answer) | What does the asynchronous writer do while PostgreSQL does not answer? | (a) keep the batch and retry with a backoff; a batch refused for its data is split | — |
| [Q-OPS-42](spec/operations-and-observability.md#q-ops-42--keys-and-bounds-of-the-deduplication) | Keys and bounds of the deduplication? | (a) a bounded in-process table; an unknown token ID keys by client | — |
| [Q-OPS-43](spec/operations-and-observability.md#q-ops-43--event-time-event-id-and-the-lookup-by-id) | Event time, event ID and the lookup by ID? | (a) `occurred_at` from the database, ID time from the producer | — |
| [Q-OPS-44](spec/operations-and-observability.md#q-ops-44--the-audit-query-is-one-static-statement-per-direction) | Audit query: one static statement per direction, or a `listq` builder? | (a) static statements with optional filters | — |
| [Q-OPS-46](spec/operations-and-observability.md#q-ops-46--a-finished-job-remembers-the-replica-that-ran-it) | Does a finished job remember the replica that ran it? | (a) column `job.ran_by`, set by the claim | — |
| [Q-OPS-47](spec/operations-and-observability.md#q-ops-47--secrets-reencrypt-is-registered-as-a-manual-only-task-whose-body-is-empty-until-a-sealed-column-exists) | Register `secrets-reencrypt` now, with an empty body? | (a) register it now | — |
| [Q-OPS-48](spec/operations-and-observability.md#q-ops-48--the-task-list-is-paginated-by-name) | Is the task list paginated by name? | (a) cursor pagination over the sorted registry | — |
| [Q-OPS-49](spec/operations-and-observability.md#q-ops-49--events-carry-the-source-of-the-request-they-were-made-in) | Who completes the request source of an event? | (b) the recorder completes it | — |
| [Q-OPS-50](spec/operations-and-observability.md#q-ops-50--the-shape-of-the-audit-operations) | Shape of the audit operations? | `limit` 1 to 1,000, default 100; cursor holds `(occurredAt, id)` and a filter fingerprint; permission `audit:read` | — |
| [Q-OPS-91](spec/operations-and-observability.md#q-ops-91--login-secrets-in-request-urls) | Login secrets in request URLs? | (b) fixed patterns in `internal/observability/redact.go` | — |
| [Q-OPS-152](spec/operations-and-observability.md#q-ops-152--one-actor-and-one-source-for-every-audit-event-of-a-request) | One actor and one source for every audit event of a request? | (b) one path | — |
| [Q-OPS-153](spec/operations-and-observability.md#q-ops-153--the-fold-of-the-cache-invalidations-is-the-scheduled-task-proxy-invalidation-fold) | How does the fold of the cache invalidations run? | (a) the scheduled task `proxy-invalidation-fold`, every five minutes, exclusive | — |
| [Q-OPS-154](spec/operations-and-observability.md#q-ops-154--the-change-payload-of-cache_invalidation-carries-the-whole-row) | What does the change of `cache_invalidation` carry? | (c) the whole row with the prefix as base64url, and a kind (`add`, `del`, `virt`) | — |
| [Q-OPS-155](spec/operations-and-observability.md#q-ops-155--the-entry-list-is-open-to-a-holder-of-repositoriesread-config-or-of-manage-decided-in-the-handler) | Who may list the cache entries, given one declaration form per operation? | (c) declared `authenticated`, decided in the handler | — |
| [Q-API-1](spec/api-and-ui.md#q-api-1--openapi-generation-approach) | How is the OpenAPI document produced? | (a) spec-first: oapi-codegen models and `net/http` server interface, openapi-typescript types, thin hand-written Angular clients | [ADR-083](#adr-083--the-openapi-31-document-is-the-api-contract) |
| [Q-API-2](spec/api-and-ui.md#q-api-2--is-if-match-mandatory-on-writes) | Is `If-Match` mandatory on writes? | (b) required on PUT and PATCH of revisioned resources; `If-Match: *` is the explicit blind write | [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) |
| [Q-API-3](spec/api-and-ui.md#q-api-3--cluster-wide-rate-limiting) | Cluster-wide rate limiting? | (e) cluster-wide sliding-window counters in Valkey (R-ARCH-034) with a per-replica fallback; revisited 2026-10-28; H-43 narrowed; same choice as Q-ARCH-17 | [ADR-102](#adr-102--cluster-wide-rate-limits-in-valkey-with-a-per-replica-fallback) |
| [Q-API-4](spec/api-and-ui.md#q-api-4--inv-17s-wording-and-byte-transfers-on-files) | Does INV-17 cover byte transfers on `/files`? | (a) amend INV-17 to "public, documented endpoints"; changes INV-17 | [ADR-028](#adr-028--the-ui-uses-only-the-public-api-inv-17) |
| [Q-API-5](spec/api-and-ui.md#q-api-5--owning-page-for-build-information) | Which page owns build information? | (a) a new page `builds.md`; same choice as Q-AUTHZ-8 and Q-STOR-3 | — |
| [Q-API-6](spec/api-and-ui.md#q-api-6--pagination-parameter-names) | Pagination parameter names? | (a) `limit` and `cursor` with the `items` and `nextCursor` envelope, everywhere | [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) |
| [Q-API-7](spec/api-and-ui.md#q-api-7--syntax-of-custom-methods) | Syntax of custom methods? | (a) kebab-case sub-path verbs; literal segments in key positions are reserved names | [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) |
| [Q-API-8](spec/api-and-ui.md#q-api-8--stored-secret-key-ring-status-through-the-api) | Key-ring status through the API? | (b) a read-only status endpoint, metrics and a UI card; re-encryption through the task trigger | [ADR-032](#adr-032--stored-secrets-are-sealed-under-a-key-ring-the-operator-supplies) |
| [Q-API-9](spec/api-and-ui.md#q-api-9--one-path-for-starting-stopping-and-reading-maintenance-work) | One path for starting, stopping and reading maintenance work? | (c) one start and one stop path per maintenance task; owning pages keep read-only views; same choice as Q-STOR-10 | [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work), [ADR-085](#adr-085--long-running-work-answers-202-with-a-job) |
| [Q-API-10](spec/api-and-ui.md#q-api-10--starting-a-backup-from-the-api-or-the-ui) | Starting a backup from the API or the UI? | (a) no start, delete, verify or restore endpoint; the UI shows a copyable `kubectl` command | [ADR-108](#adr-108--backup-credentials-stay-in-backup-pods-the-api-and-ui-only-read-runs) |
| [Q-API-11](spec/api-and-ui.md#q-api-11--where-the-apis-list-of-stored-backups-comes-from) | Where does the API's list of stored backups come from? | (c) the stored-backup list recorded by the newest create or delete run | [ADR-108](#adr-108--backup-credentials-stay-in-backup-pods-the-api-and-ui-only-read-runs) |
| [Q-API-12](spec/api-and-ui.md#q-api-12--visibility-of-a-management-write-on-the-other-replicas) | Visibility of a management write on the other replicas? | (e) management reads from PostgreSQL; an unknown repository key is looked up, with a change-log poll and a grant-index rebuild, before `404`; `Trove-Authz-Min-Epoch` for grant changes; the propagation bound otherwise. Revisited 2026-10-28: was (c) | [ADR-096](#adr-096--no-state-between-requests-on-a-replica-inv-18), [ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed) |
| [Q-API-13](spec/api-and-ui.md#q-api-13--problem-member-name-problem-type-status-of-validation-errors-and-error-codes) | Problem member name, type, validation status and error codes? | (b) relative `type` `/api/v1/problems/<code>`; `requestId`; 400 `malformed_request`, 422 `validation_failed`; codes `server_busy` and `database_timeout` | [ADR-126](#adr-126--problem-details-with-a-relative-type-a-requestid-member-and-named-error-codes) |
| [Q-API-14](spec/api-and-ui.md#q-api-14--stand-in-for-systemread-until-authentication-ships) | What stands in for `system.read` until authentication ships? | (b) `system.bootstrap_anonymous_read`, default `false`, refused with an `https` base URL; a presented credential gets 401 | — |
| [Q-API-15](spec/api-and-ui.md#q-api-15--target-of-user_preferencesuser_id) | Target of `user_preferences.user_id`? | (a) `app_user (principal_id)`, `ON DELETE CASCADE` | — |
| [Q-API-16](spec/api-and-ui.md#q-api-16--a-limit-above-the-maximum-is-clamped) | Is a `limit` above the maximum clamped or refused? | (a) clamped; a `limit` that is not an integer of 1 or more answers 400 `malformed_request` | — |
| [Q-API-17](spec/api-and-ui.md#q-api-17--the-body-of-an-idempotent-answer-is-the-databases-rendering-and-a-refusal-is-stored-in-a-second-transaction) | Body of an idempotent answer: the handler's bytes or the database's rendering? | (a) the stored `jsonb` rendering for the first answer and every replay; a refusal is stored by the savepoint of Q-API-137 | — |
| [Q-API-18](spec/api-and-ui.md#q-api-18--an-unknown-or-duplicate-member-of-a-body-is-400-although-it-lists-a-field) | Unknown or duplicate body member: which error? | (a) 400 with `errors[]`; member names compared exactly | — |
| [Q-API-19](spec/api-and-ui.md#q-api-19--m1-builds-no-settings-resource-it-supplies-the-rule) | Does M1 build a generic settings resource? | (a) the rule only | — |
| [Q-API-20](spec/api-and-ui.md#q-api-20--no-storagegc-runs) | Is there a `/storage/gc-runs` resource? | (a) not built | — |
| [Q-API-21](spec/api-and-ui.md#q-api-21--how-the-job-and-system-operations-declare-their-permission) | How do the job and system operations declare their permission? | (a) capabilities; `authenticated` plus a handler check where the rule is per object | — |
| [Q-API-22](spec/api-and-ui.md#q-api-22--every-operation-documents-429) | Does every operation document `429`? | (a) yes, in every operation | — |
| [Q-API-23](spec/api-and-ui.md#q-api-23--a-job-the-caller-may-not-see-answers-404) | A job the caller may not see answers which code? | (a) 404; a caller who sees the job but may not stop it gets 403 on cancel | — |
| [Q-API-24](spec/api-and-ui.md#q-api-24--if-match-is-an-optional-header-in-the-document) | Is `If-Match` an optional header in the document? | superseded by Q-API-28 and Q-API-42: required, one 428 rule | — |
| [Q-API-25](spec/api-and-ui.md#q-api-25--the-identity-resources-have-no-revision) | Do the identity resources have a revision? | reversed by Q-AUTH-50: they carry a revision and require `If-Match` | — |
| [Q-API-26](spec/api-and-ui.md#q-api-26--preferences-without-a-row) | Preferences without a row? | (b) defaults at revision 1; the first write inserts revision 2 | — |
| [Q-API-28](spec/api-and-ui.md#q-api-28--a-missing-if-match-is-428-by-one-rule-and-a-write-keeps-its-epoch-through-an-idempotent-replay) | Missing `If-Match` by one rule, and an idempotent replay keeps its epoch? | (b) the document keeps the header required and `bindError` answers 428; (c) an idempotent outcome completes itself after the commit hooks, so a replay carries the epoch | — |
| [Q-API-32](spec/api-and-ui.md#q-api-32--a-missing-if-match-is-428-even-where-the-generated-binder-reads-the-header) | Missing `If-Match` where the generated binder reads the header? | folded into Q-API-28 | — |
| [Q-API-33](spec/api-and-ui.md#q-api-33--the-files-operations-are-in-the-document-outside-the-generated-interface-and-outside-the-authorization-step) | How are the `/files/` operations documented? | (b) documented, nothing generated, the routes compared | — |
| [Q-API-34](spec/api-and-ui.md#q-api-34--the-representation-of-a-repository-read-only-members-and-the-warnings) | Representation of a repository: read-only members and warnings? | (b) read-only members in the same object, ignored on input | — |
| [Q-API-40](spec/api-and-ui.md#q-api-40--recent-authentication-is-enforced-by-the-shared-authorization-step) | Where is recent authentication enforced? | (b) the shared step, after the permission | — |
| [Q-API-41](spec/api-and-ui.md#q-api-41--the-request-log-learns-the-principal-through-the-request-scoped-holder) | How does the request log learn the principal? | (b) the authentication stage fills the request-scoped holder | — |
| [Q-API-42](spec/api-and-ui.md#q-api-42--one-mechanism-for-a-missing-if-match-and-one-stale-error) | One mechanism for a missing `If-Match`, and one stale error? | (b) one declaration and one error | — |
| [Q-API-43](spec/api-and-ui.md#q-api-43--the-revision-of-an-identity-resource-is-in-the-etag-not-in-the-body) | Where is the revision of an identity resource? | (b) in the ETag only | — |
| [Q-API-44](spec/api-and-ui.md#q-api-44--one-header-name-and-one-setter-for-the-authorization-epoch) | One header name and one setter for the authorization epoch? | `authz.HeaderEpoch`, `setEpoch` for a plain answer, `finishEpoch` for an idempotent one | — |
| [Q-API-75](spec/api-and-ui.md#q-api-75--how-deeply-a-json-request-body-may-nest) | How deeply may a JSON request body nest? | (b) 64 levels (`maxBodyDepth`) | — |
| [Q-API-136](spec/api-and-ui.md#q-api-136--one-rule-for-the-top-level-code-of-an-error) | One rule for the top-level code of an error? | (b) the convention's codes | — |
| [Q-API-137](spec/api-and-ui.md#q-api-137--a-refused-idempotent-request-is-stored-in-the-transaction-that-claimed-the-key) | Where is a refused idempotent request stored? | (b) a savepoint around the operation | — |
| [Q-API-138](spec/api-and-ui.md#q-api-138--a-cors-preflight-is-answered-before-authentication) | When is a CORS preflight answered? | (b) a preflight stage between stage 5 and stage 6 | — |
| [Q-API-156](spec/api-and-ui.md#q-api-156--the-tag-of-a-repository-read-follows-lastactivityat) | Does the tag of a repository read follow `lastActivityAt`? | (d) the activity time is part of the read tag | — |
| [Q-API-157](spec/api-and-ui.md#q-api-157--the-api-conventions-own-the-merge-patch-the-domain-receives-it-as-a-function) | Who owns the RFC 7396 merge? | (a) the API conventions; the domain receives it as a `Patcher` | — |
| [Q-API-158](spec/api-and-ui.md#q-api-158--the-shared-step-begins-the-index-only-for-an-operation-that-decides-a-handler-that-decides-begins-it-itself) | Where does a management request take its authorization index? | (b) the shared step for the forms that decide, the handler itself otherwise | — |
| [Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library) | Which component library? | (a) Angular Material with the CDK and `@angular/aria`; font icons | [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) |
| [Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested) | Shell layout and density? | (c) a left side bar, compact density, container queries | [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) |
| [Q-UI-3](spec/api-and-ui.md#q-ui-3--layout-of-the-package-details-page-example-screenshot-requested) | Layout of the package details page? | (c) two columns with a sticky install panel from 960 CSS px of content width; one column below | [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) |
| [Q-UI-4](spec/api-and-ui.md#q-ui-4--layout-of-the-policy-editor-and-the-explainer-example-screenshot-requested) | Layout of the policy editor and the explainer? | (a) one page with a sticky preview; the explainer as its own screen | [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) |
| [Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism) | Internationalization mechanism? | (a) `@angular/localize`, one build per locale under `/ui/{locale}/` | [ADR-088](#adr-088--english-and-german-in-the-mvp) |
| [Q-UI-6](spec/api-and-ui.md#q-ui-6--external-images-in-readmes) | External images in READMEs? | (d) blocked by default; an operator allowlist of exact `https` origins, mirrored into the CSP | [ADR-087](#adr-087--readmes-rendered-on-the-server-through-an-allowlist-sanitizer) |
| [Q-UI-7](spec/api-and-ui.md#q-ui-7--roles-drain-readiness-and-valkey-on-the-system-status-screen) | Roles, drain readiness and Valkey on the system status screen? | (b) the registry gains node and Sentinel reachability; replicas grouped by role, with drain safety per role and a Valkey summary on the status screen | [ADR-104](#adr-104--subsystems-per-role-web-serves-worker-owns-background-work-and-leases), [ADR-013](#adr-013--one-binary-roles-split-from-the-start-identical-replicas-per-role-inv-2) |
| [Q-UI-8](spec/api-and-ui.md#q-ui-8--bundle-budget-csp-nonce-and-inline-code) | How are the bundle budget, the CSP nonce and inline code handled? | (b) and (c) together: the raw guard of `angular.json` and the 400 kB compressed budget of `scripts/check-budget.mjs`; nonce placeholder; inlining off | — |
| [Q-UI-9](spec/api-and-ui.md#q-ui-9--test-tiers-of-the-web-ui-at-m0) | Which web test tiers exist at M0? | (b) unit and component tiers on jsdom; browser, contract, Playwright and axe tiers with the M1 screens | — |
| [Q-UI-10](spec/api-and-ui.md#q-ui-10--start-page-and-the-shells-first-routes-at-m1) | Start page and the first routes at M1? | (b) `/` redirects to `/browse`; the other routes of the M1 map are routed | — |
| [Q-UI-11](spec/api-and-ui.md#q-ui-11--accessibility-tooling-and-its-scope-at-m1) | Accessibility tooling and its scope at M1? | (b) axe-core inside the Vitest component tier on jsdom; Playwright later | — |
| [Q-UI-12](spec/api-and-ui.md#q-ui-12--http-layer-service-interfaces-and-endpoints-that-are-not-in-the-openapi-document-yet) | HTTP layer and service interfaces before the endpoints exist in the document? | (b) one `ApiClient` on `HttpClient` plus an abstract service per endpoint family, with fakes | — |
| [Q-UI-13](spec/api-and-ui.md#q-ui-13--choices-on-the-system-status-screen-and-the-navigation) | Cards of the system status screen, the re-encryption gate and the Administration area? | cards render only from endpoints that exist; re-encryption is offered to holders of `content:bypass`; the area appears per capability | — |
| [Q-UI-14](spec/api-and-ui.md#q-ui-14--forms-of-the-repository-and-endpoint-screens-signals-and-native-controls) | Forms of the repository and endpoint screens? | (b) plain signals and native controls | — |
| [Q-UI-15](spec/api-and-ui.md#q-ui-15--settings-of-ui-that-the-browser-cannot-read) | Settings of `ui.*` that the browser cannot read? | (b) constants with the documented defaults next to their use | — |
| [Q-UI-16](spec/api-and-ui.md#q-ui-16--upload-an-xhr-client-of-its-own-create-only-unless-replace-is-ticked) | Upload client, replacement and retry? | a second XHR `HttpClient`; create-only (`If-None-Match: *`) unless "Replace" is ticked; hash up to 256 MiB; one retry | — |
| [Q-UI-17](spec/api-and-ui.md#q-ui-17--file-tree-angulararia-tree-actions-in-a-toolbar) | File tree widget and where the actions live? | the `@angular/aria` tree with explicit selection; actions in a toolbar above the details | — |
| [Q-UI-18](spec/api-and-ui.md#q-ui-18--repository-screens-summary-view-settings-of-the-generic-format-delete) | Repository screens: list columns, settings forms, edit and delete? | the list shows last activity until the summary carries creation time; forms hand-built for the generic format; a merge patch with `ETag`; delete by typing the key | — |
| [Q-UI-19](spec/api-and-ui.md#q-ui-19--endpoint-screens-audit-log-and-the-completed-status-screen) | Endpoint screens, the audit log and the completed status screen? | rule editor by pattern subject; diff before `PUT`; preview 500 ms after a change; jobs polled every 2 s | — |
| [Q-UI-20](spec/api-and-ui.md#q-ui-20--forms-of-the-identity-token-settings-and-policy-screens) | Forms of the identity, token, settings and policy screens? | (c) plain signals with two small field components | — |
| [Q-UI-21](spec/api-and-ui.md#q-ui-21--why-can-x-do-y-in-the-policy-preview-while-the-explain-operation-is-not-built) | "Why can X do Y" in the policy preview while the explain operation is not built? | (b) answer from the draft behind a `PolicyExplainer` service | — |
| [Q-UI-22](spec/api-and-ui.md#q-ui-22--users-groups-and-service-accounts-are-edited-without-a-version-tag) | Users, groups and service accounts edited without a version tag? | (b) require `ETag` and `If-Match` with the conflict dialog | — |
| [Q-UI-23](spec/api-and-ui.md#q-ui-23--drafts-across-re-authentication-and-an-ended-session) | Drafts across re-authentication and an ended session? | (a) session storage per route for non-secret forms; token values never kept | — |
| [Q-UI-24](spec/api-and-ui.md#q-ui-24--where-a-new-token-value-is-shown-and-a-lost-one) | Where is a new token value shown, and what about a lost one? | (a) an inline panel of the creating component; a 409 `token_already_issued` revokes and creates again | — |
| [Q-UI-25](spec/api-and-ui.md#q-ui-25--controls-the-caller-may-not-use-the-filter-unused-for-and-what-the-settings-answer-lacks) | Controls the caller may not use, the "unused for" filter, and the settings answer that lacks fields? | disabled controls with the reason note; "unused for" sent as a day count; the missing settings facts are not shown | — |
| [Q-UI-68](spec/api-and-ui.md#q-ui-68--layout-of-the-application-shell-and-the-list-screens) | Layout of the application shell and the list screens? | (b) dark side bar with folding groups and an icon rail, dark top bar, breadcrumbs from the routes, one data table and one filter panel on every list screen | — |
| [Q-UI-69](spec/api-and-ui.md#q-ui-69--client-addresses-come-from-the-servers-base-url-never-from-the-pages-location) | Where do client addresses in the UI come from? | (c) the server base URL: system info for a holder of `settings:read`, else the repository addresses; never the location | — |
| [Q-EP-1](spec/endpoints.md#q-ep-1--how-the-endpoint-of-a-request-is-identified) | How is the endpoint of a request identified? | (a) by the listener the connection arrived on; `Host` only selects among the endpoint's own hosts, a foreign host answers `421` | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-2](spec/endpoints.md#q-ep-2--where-endpoint-configuration-and-exposure-rules-live) | Where do endpoint configuration and exposure rules live? | (c) network binding in the file, exposure rules in PostgreSQL, compiled into the grant index | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-3](spec/endpoints.md#q-ep-3--separate-pool-or-shared-pods-for-a-further-endpoint) | Separate pool or shared pods for a further endpoint? | (c) an optional pool per endpoint, off by default, recommended for endpoints with exposure rules | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-4](spec/endpoints.md#q-ep-4--the-answer-for-content-an-endpoint-does-not-expose) | What answers content an endpoint does not expose? | (a) `404` identical to a missing resource; writes `403` `endpoint_read_only` | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-5](spec/endpoints.md#q-ep-5--cache-fill-from-upstreams-on-further-endpoints) | Cache fill from upstreams on further endpoints? | (b) off by default, `cache_fill` per endpoint | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-6](spec/endpoints.md#q-ep-6--how-far-wildcard-hosts-and-host-root-bindings-go) | How far do wildcard hosts and host-root bindings go? | (b) exact hosts and at most one single-label wildcard per endpoint, host-root bindings of exact hosts or the wildcard label; the mechanism in the MVP, its consumers P1 | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-7](spec/endpoints.md#q-ep-7--endpoints-in-the-beta) | Do endpoints belong in the beta? | (a) MVP, split over M0 to M3 with the journal entries in M5; the owner was asked, and the choice stands until he objects | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-8](spec/endpoints.md#q-ep-8--how-exposure-rules-select-repositories) | How do exposure rules select repositories? | (a) one repository per rule, named by key and stored by ID | — |
| [Q-EP-9](spec/endpoints.md#q-ep-9--writes-through-an-endpoint-with-exposure-rules) | Writes through an endpoint with exposure rules? | (a) read only | — |
| [Q-EP-10](spec/endpoints.md#q-ep-10--credentials-a-further-endpoint-accepts-by-default) | Which credentials does a further endpoint accept by default? | (b) service-account tokens bound to the endpoint; no anonymous access, no session | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-11](spec/endpoints.md#q-ep-11--exposure-on-virtual-repositories-the-addressed-repository-or-its-members) | Is exposure decided on the addressed repository or on its members? | (a) on the addressed repository | — |
| [Q-EP-12](spec/endpoints.md#q-ep-12--whether-administrators-pass-the-exposure-ceiling) | Do administrators pass the exposure ceiling? | (a) no: exposure binds every subject, `content:bypass` included | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-13](spec/endpoints.md#q-ep-13--sign-in-and-the-ui-on-further-endpoints) | Sign-in and the UI on further endpoints? | (a) the default endpoint only | — |
| [Q-EP-14](spec/endpoints.md#q-ep-14--labels-of-the-tls-series-and-of-the-ops-series) | Labels of the TLS series and of the ops series? | (a) `kind`, `listener`, `name`; the listener is `client:<endpoint>`; the ops listener's `endpoint` is empty | [ADR-127](#adr-127--transport-encryption-on-every-connection-troves-listeners-included) |
| [Q-EP-15](spec/endpoints.md#q-ep-15--which-forwarded-host-counts-on-an-endpoint) | Which forwarded host counts on an endpoint? | (a) only the header family `server.forwarded_header` selects; no fallback | [ADR-128](#adr-128--endpoints-one-listener-each-identified-by-the-listener-with-exposure-rules-as-a-ceiling) |
| [Q-EP-16](spec/endpoints.md#q-ep-16--rendering-of-the-endpoint-map-and-of-the-pool-values) | Rendering of the endpoint map and of the pool values? | (a) `server.endpoints` as one JSON row in the effective view; pool values merged key by key | — |
| [Q-EP-17](spec/endpoints.md#q-ep-17--backend-tls-of-a-further-endpoints-ingress) | Backend TLS of a further endpoint's Ingress? | (a) each `ingress.backendTLS` key of the endpoint overrides the global block for that endpoint | — |
| [Q-EP-18](spec/endpoints.md#q-ep-18--writing-an-exposure-rule-put-merge-patch-and-the-immutable-repository) | Writing an exposure rule: `PUT`, merge patch, and the repository? | (b) `PUT` and `PATCH`; a different `repository` answers 422 `validation_failed` at `/repository` | — |
| [Q-EP-19](spec/endpoints.md#q-ep-19--what-check-exposure-checks-paths-and-the-editors-draft) | What does check-exposure check: paths and the editor's draft? | (b) `paths` for the path dimension, plus an optional draft `rule` | — |
| [Q-EP-20](spec/endpoints.md#q-ep-20--how-the-endpoint-of-a-request-reaches-the-decision-point) | How does the endpoint of a request reach the decision point? | (b) the subject carries it (`Subject.Endpoint`), set by the one conversion from the listener; rewritten from (c) the request context | — |
| [Q-EP-21](spec/endpoints.md#q-ep-21--where-the-ceiling-sits-in-the-evaluation-order) | Where does the exposure ceiling sit in the evaluation order? | (b) first in `Decide`, after the anonymous gate | — |
| [Q-EP-22](spec/endpoints.md#q-ep-22--limiter-name-and-fallback-share-of-an-endpoint-class) | Limiter name and fallback share of an endpoint class? | (a) `endpoint_<class>`, sharing from a count of its own | — |
| [Q-EP-23](spec/endpoints.md#q-ep-23--an-empty-list-of-accepted-token-types) | An empty list of accepted token types? | (c) startup refuses an empty list | — |
| [Q-EP-24](spec/endpoints.md#q-ep-24--token-listings-show-the-bound-endpoints-that-are-not-configured) | Do token listings show bound endpoints that are not configured? | (b) the token representation shows them; the creating answer does not | — |
| [Q-EP-25](spec/endpoints.md#q-ep-25--a-deleted-repository-records-the-exposure-rules-it-takes-with-it) | Which mechanism records the exposure rules a deleted repository takes with it? | (b) a delete hook of the repository service, called by both delete paths | — |
| [Q-EP-26](spec/endpoints.md#q-ep-26--a-rule-patch-is-written-conditional-on-the-revision-it-was-computed-on) | Is a rule patch written conditional on the revision it was computed on? | yes: the handler compares `If-Match` with the revision it read (412 with the current tag) and writes with that revision | — |
| [Q-EP-54](spec/endpoints.md#q-ep-54--the-surface-of-the-dev-profiles-endpoint-customers) | Surface of the dev profile's endpoint `customers`? | (a) `[packages, files, api]` | — |
| [Q-ROAD-1](coverage/roadmap.md#q-road-1--generic-local-repositories-in-m1) | Generic local repositories in M1? | (a) the local class of the generic format is built in M1 as MVP, from a new spec page `formats/generic.md`; remote and virtual generic stay P1 | — |
| [Q-ROAD-2](coverage/roadmap.md#q-road-2--backup-and-restore-in-the-beta) | Backup and restore in the beta? | (e) trove's own backup create and restore, the platform layer (R-HA-023, R-HA-025) and the revocation journal (R-HA-054, R-AUTHZ-031) are MVP and gate in M5; their work runs in its own lane from M1 on, and M0 gives every table its export class. Revisited 2026-10-28: was (a), both kept at P1 and the journal after 1.0 | [ADR-094](#adr-094--a-configurable-backup-job-writes-consistent-backups-to-s3), [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) |
| [Q-ROAD-3](coverage/roadmap.md#q-road-3--ecosystems-the-owners-teams-use-after-npm-and-nuget) | Which ecosystems after npm and NuGet? | (a) the P1 format order of the roadmap stands; revisited 2026-10-29: Ansible Galaxy (owner demand) is F0 and P1, ahead of F1 | — |
| [Q-ROAD-4](coverage/roadmap.md#q-road-4--how-the-coverage-generator-learns-implementation-status) | How does the coverage generator learn implementation status? | (a) tests name the requirement IDs they verify, and the generator derives the status from CI results | — |
| [Q-ROAD-5](coverage/roadmap.md#q-road-5--evidence-rules-of-the-coverage-generator) | Which evidence counts for the coverage generator? | (b) a failing test vetoes its ID; a skip counts as neither; an unknown ID fails the run | — |
| [Q-ROAD-6](coverage/roadmap.md#q-road-6--how-the-end-to-end-driver-reads-and-drives-the-cluster) | How does the end-to-end driver read and drive the cluster? | (a) a diagnostic job kind `system.probe` in the release image, enqueued through the product's job path | — |
| [Q-ROAD-7](coverage/roadmap.md#q-road-7--which-end-to-end-jobs-run-on-each-push-and-which-nightly) | Which end-to-end jobs run on each push, and which nightly? | (b) a push set on every push and a nightly failover set. Owner decision of 2026-10-29: no nightly workflow; both sets run on every change | [ADR-130](#adr-130--the-organisations-ci-and-release-standard) |
