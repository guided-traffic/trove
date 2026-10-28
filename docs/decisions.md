# Architecture decision records

Status: decision log for the specification. Nothing described here is implemented yet.

This page records trove's design decisions, one entry per decision: what forced it, what was
decided, what follows from it, which alternatives were weighed and where the specification states
it in full. The normative text lives on the [specification pages](spec/README.md); an entry here
summarizes and links, it never overrides a spec page. Open questions stay on the page that raised
them and are only indexed here, in [Open decisions](#open-decisions).

**Status values.**

| Status | Meaning |
|---|---|
| Accepted (owner constraint) | Fixed by the project owner ([owner constraints](spec/README.md#owner-constraints)). Not reopened by the spec pages; a page that finds one costly raises an open question. |
| Proposed | The design the spec pages build on. Becomes *Accepted* when the owner confirms it. |
| Accepted | Confirmed by the owner. |
| Superseded by ADR-*n* | Replaced; the entry stays for the record. |

**Rules for this log.** Numbers are assigned in order and never reused or renumbered. A changed
decision gets a new entry that supersedes the old one, and the old entry's status changes. When an
open question is decided on its spec page, the decision either becomes a new entry or updates the
entry listed next to it in [Open decisions](#open-decisions). Alternatives marked as considered here
are the ones the spec pages weighed, plus the obvious designs a reviewer would ask about; the
reasoning for each rejection is one line, the spec page holds the rest.

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
| [ADR-012](#adr-012--one-toolchain-one-image-inv-1) | One toolchain, one image (INV-1) | Proposed |
| [ADR-013](#adr-013--one-binary-identical-stateless-replicas-inv-2) | One binary, identical stateless replicas (INV-2) | Proposed |
| [ADR-014](#adr-014--postgresql-is-the-only-stateful-dependency-besides-object-storage-inv-3) | PostgreSQL is the only stateful dependency besides object storage (INV-3) | Proposed |
| [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) | Content-addressed blob store on S3 (INV-4) | Proposed |
| [ADR-016](#adr-016--repository-model-local-remote-virtual-inv-5) | Repository model: local, remote, virtual (INV-5) | Proposed |
| [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6) | URL layout on two listeners (INV-6) | Proposed |
| [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7) | Humans authenticate only through an external OIDC provider (INV-7) | Proposed |
| [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8) | trove issues opaque, prefixed tokens (INV-8) | Proposed |
| [ADR-020](#adr-020--deny-by-default-allow-only-permission-policies-inv-9) | Deny by default, allow-only permission policies (INV-9) | Proposed |
| [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) | GitLab CI identity through ID-token exchange (INV-10) | Proposed |
| [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) | Pull-through proxy semantics (INV-11) | Proposed |
| [ADR-023](#adr-023--high-availability-through-postgresql-coordination-inv-12) | High availability through PostgreSQL coordination (INV-12) | Proposed |
| [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) | The Helm chart is the supported deployment (INV-13) | Proposed |
| [ADR-025](#adr-025--npm-and-nuget-v3-are-the-mvp-formats-inv-14) | npm and NuGet v3 are the MVP formats (INV-14) | Proposed |
| [ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15) | One configuration file plus database-held configuration (INV-15) | Proposed |
| [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16) | Structured logs, Prometheus metrics, OTLP traces, audit in PostgreSQL (INV-16) | Proposed |
| [ADR-028](#adr-028--the-ui-uses-only-the-public-api-inv-17) | The UI uses only the public API (INV-17) | Proposed |
| [ADR-029](#adr-029--formats-are-compiled-in-plug-ins-behind-a-narrow-host-interface) | Formats are compiled-in plug-ins behind a narrow host interface | Proposed |
| [ADR-030](#adr-030--one-origin-for-ui-api-and-package-content) | One origin for UI, API and package content | Proposed |
| [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early) | Caches validate themselves; notifications only evict early | Proposed |
| [ADR-032](#adr-032--stored-secrets-are-sealed-under-a-key-ring-the-operator-supplies) | Stored secrets are sealed under a key ring the operator supplies | Proposed |
| [ADR-033](#adr-033--versioning-promises-per-surface-and-a-schema-compatibility-gate) | Versioning promises per surface and a schema compatibility gate | Proposed |
| [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work) | One PostgreSQL job table for maintenance, API operations and internal work | Proposed |
| [ADR-035](#adr-035--virtual-resolution-in-three-tiers-decided-per-member) | Virtual resolution in three tiers, decided per member | Proposed |
| [ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules) | Dependency-confusion protection through namespace claims and pin rules | Proposed |
| [ADR-037](#adr-037--one-name-pattern-syntax-most-specific-wins-applied-to-every-protocol-request) | One name-pattern syntax, most specific wins, applied to every protocol request | Proposed |
| [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving) | Upstream failures: classified outcomes, a shared circuit, bounded stale serving | Proposed |
| [ADR-039](#adr-039--egress-address-checks-after-dns-resolution-for-every-remote-connection) | Egress address checks after DNS resolution for every remote connection | Proposed |
| [ADR-040](#adr-040--strict-repository-configuration-documents-and-asynchronous-deletion) | Strict repository configuration documents and asynchronous deletion | Proposed |
| [ADR-041](#adr-041--the-database-row-decides-whether-a-blob-exists) | The database row decides whether a blob exists | Proposed |
| [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) | Race-free garbage collection with a grace period | Proposed |
| [ADR-043](#adr-043--object-store-outage-no-local-queue-and-201-means-durable) | Object-store outage: no local queue, and 201 means durable | Proposed |
| [ADR-044](#adr-044--a-trash-can-that-also-covers-overwrites-p1) | A trash can that also covers overwrites (P1) | Proposed |
| [ADR-045](#adr-045--presigned-download-redirects-as-a-per-repository-opt-in-p2) | Presigned download redirects as a per-repository opt-in (P2) | Proposed |
| [ADR-046](#adr-046--server-side-ui-sessions-behind-an-opaque-cookie) | Server-side UI sessions behind an opaque cookie | Proposed |
| [ADR-047](#adr-047--a-break-glass-administrator-outside-the-database) | A break-glass administrator outside the database | Proposed |
| [ADR-048](#adr-048--tokens-hashed-with-sha-256-a-positive-only-cache-bounded-revocation-latency) | Tokens hashed with SHA-256, a positive-only cache, bounded revocation latency | Proposed |
| [ADR-049](#adr-049--command-line-login-through-the-browser-session-p1) | Command-line login through the browser session (P1) | Proposed |
| [ADR-050](#adr-050--anonymous-access-is-off-by-default-and-needs-two-switches) | Anonymous access is off by default and needs two switches | Proposed |
| [ADR-051](#adr-051--actions-are-explicit-and-a-token-scope-only-narrows) | Actions are explicit, and a token scope only narrows | Proposed |
| [ADR-052](#adr-052--changing-what-clients-install-needs-deploy-not-annotate) | Changing what clients install needs `deploy`, not `annotate` | Proposed |
| [ADR-053](#adr-053--virtual-repositories-endpoint-gate-plus-member-rights) | Virtual repositories: endpoint gate plus member rights | Proposed |
| [ADR-054](#adr-054--cache-fills-are-guarded-by-a-configurable-action-read-by-default) | Cache fills are guarded by a configurable action, `read` by default | Proposed |
| [ADR-055](#adr-055--refused-reads-answer-404-refused-writes-403) | Refused reads answer 404, refused writes 403 | Proposed |
| [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) | Package ownership is decided by the first publish | Proposed |
| [ADR-057](#adr-057--a-compiled-grant-index-per-replica-consistent-through-an-epoch-failing-closed) | A compiled grant index per replica, consistent through an epoch, failing closed | Proposed |
| [ADR-058](#adr-058--projects-for-delegated-administration-reduced-in-p1) | Projects for delegated administration, reduced, in P1 | Proposed |
| [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint) | CI tokens through a standard token-exchange endpoint | Proposed |
| [ADR-060](#adr-060--ordered-identity-mappings-first-match-per-tier-union-across-tiers) | Ordered identity mappings: first match per tier, union across tiers | Proposed |
| [ADR-061](#adr-061--a-small-claim-template-language-with-escaping) | A small claim-template language with escaping | Proposed |
| [ADR-062](#adr-062--fork-pipelines-are-denied-by-default) | Fork pipelines are denied by default | Proposed |
| [ADR-063](#adr-063--a-ci-tokens-lifetime-is-bounded-by-its-job) | A CI token's lifetime is bounded by its job | Proposed |
| [ADR-064](#adr-064--subject-token-reuse-is-recorded-single-use-is-opt-in) | Subject-token reuse is recorded; single use is opt-in | Proposed |
| [ADR-065](#adr-065--further-ci-issuers-through-the-same-mechanism-p1) | Further CI issuers through the same mechanism (P1) | Proposed |
| [ADR-066](#adr-066--npm-package-documents-are-rendered-from-rows-not-stored) | npm package documents are rendered from rows, not stored | Proposed |
| [ADR-067](#adr-067--npm-versions-are-immutable-guarded-by-tombstones) | npm versions are immutable, guarded by tombstones | Proposed |
| [ADR-068](#adr-068--npm-remotes-fetch-only-the-full-document) | npm remotes fetch only the full document | Proposed |
| [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default) | Versions removed upstream are hidden by default | Proposed |
| [ADR-070](#adr-070--merged-npm-documents-take-each-version-from-the-member-that-would-serve-it) | Merged npm documents take each version from the member that would serve it | Proposed |
| [ADR-071](#adr-071--npm-trusted-publishing-through-package-bound-ci-tokens-p1) | npm trusted publishing through package-bound CI tokens (P1) | Proposed |
| [ADR-072](#adr-072--real-client-acceptance-matrices-gate-each-format) | Real-client acceptance matrices gate each format | Proposed |
| [ADR-073](#adr-073--nuget-v3-only-every-resource-below-the-feed-root) | NuGet v3 only, every resource below the feed root | Proposed |
| [ADR-074](#adr-074--nuget-package-ids-ascii-on-push-case-insensitive-first-casing-displayed) | NuGet package IDs: ASCII on push, case-insensitive, first casing displayed | Proposed |
| [ADR-075](#adr-075--strict-archive-and-manifest-parsing-the-declared-id-is-what-gets-authorized) | Strict archive and manifest parsing; the declared ID is what gets authorized | Proposed |
| [ADR-076](#adr-076--symbol-packages-only-as-snupkg-matching-an-existing-package) | Symbol packages only as `.snupkg` matching an existing package | Proposed |
| [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing) | Singleton work under advisory-lock leases with fencing | Proposed |
| [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) | Restores keep metadata and blobs consistent by ordering, not by coordination | Proposed |
| [ADR-079](#adr-079--the-chart-validates-before-it-renders-and-never-creates-secrets) | The chart validates before it renders and never creates secrets | Proposed |
| [ADR-080](#adr-080--audit-records-commit-with-the-change-they-record) | Audit records commit with the change they record | Proposed |
| [ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1) | Configuration as code: documents own their objects; plan before apply (P1) | Proposed |
| [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) | Events through a transactional outbox, as CloudEvents, signed per Standard Webhooks (P1) | Proposed |
| [ADR-083](#adr-083--the-openapi-31-document-is-the-api-contract) | The OpenAPI 3.1 document is the API contract | Proposed |
| [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) | Uniform API conventions: problem details, cursors, merge patch, ETags, idempotency keys | Proposed |
| [ADR-085](#adr-085--long-running-work-answers-202-with-a-job) | Long-running work answers 202 with a job | Proposed |
| [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) | UI baseline: Angular 22 defaults, a strict CSP, nothing from foreign origins | Proposed |
| [ADR-087](#adr-087--readmes-rendered-on-the-server-through-an-allowlist-sanitizer) | READMEs rendered on the server through an allowlist sanitizer | Proposed |
| [ADR-088](#adr-088--english-and-german-in-the-mvp) | English and German in the MVP | Proposed |

</details>

## Owner constraints

### ADR-001 · Backend in Go 1.27.3

**Status:** Accepted (owner constraint)

**Context.** The owner fixes the implementation language and version of the server.

**Decision.** The backend is written in Go and built with Go 1.27.3. INV-1 records the check made
on 2026-10-28: the local `go version` and the go.dev download list both showed go1.27.3 as the
current stable release.

**Consequences.** Libraries come from the Go ecosystem; the open choices are
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
Angular majors, which drives [Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library) and [Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism). A new Angular major is planned work that
upgrades the framework and its companion packages together.

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [ADR-012](#adr-012--one-toolchain-one-image-inv-1), [R-ARCH-004](spec/architecture.md#r-arch-004--angular-workspace-layout),
[R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery).

### ADR-003 · PostgreSQL as the database

**Status:** Accepted (owner constraint)

**Context.** The owner fixes the database.

**Decision.** All metadata is stored in PostgreSQL.

**Consequences.** [ADR-014](#adr-014--postgresql-is-the-only-stateful-dependency-besides-object-storage-inv-3) (INV-3) goes further and makes PostgreSQL the only stateful
dependency besides object storage, including cross-replica coordination ([ADR-023](#adr-023--high-availability-through-postgresql-coordination-inv-12)). Open:
the supported version floor ([Q-HA-2](spec/ha-and-helm.md#q-ha-2--postgresql-version-floor)), connection poolers ([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers)), the driver
([Q-ARCH-2](spec/architecture.md#q-arch-2--postgresql-driver-and-query-layer)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-HA-013](spec/ha-and-helm.md#r-ha-013--supported-postgresql-versions-and-server-settings), [R-HA-014](spec/ha-and-helm.md#r-ha-014--database-provisioning-roles-and-privileges).

### ADR-004 · S3 as binary storage

**Status:** Accepted (owner constraint)

**Context.** The owner fixes where package binaries live.

**Decision.** Binaries are stored in S3-compatible object storage.

**Consequences.** [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) (INV-4) lays the store out by content address. Every supported store
must pass a conformance check ([R-STOR-035](spec/storage.md#r-stor-035--object-store-consistency-assumptions-and-conformance-check)). Open: the client library ([Q-ARCH-4](spec/architecture.md#q-arch-4--s3-client-library)) and the
store used by the local development profile ([Q-HA-3](spec/ha-and-helm.md#q-ha-3--s3-component-of-the-local-profile-after-minios-archival)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-STOR-030](spec/storage.md#r-stor-030--object-store-connection-and-bucket-settings), [R-STOR-035](spec/storage.md#r-stor-035--object-store-consistency-assumptions-and-conformance-check).

### ADR-005 · High availability

**Status:** Accepted (owner constraint)

**Context.** trove sits on the build path of every pipeline that installs or publishes
packages; an outage stops builds.

**Decision.** trove runs highly available: losing one replica does not interrupt service.

**Consequences.** Replicas are identical and stateless ([ADR-013](#adr-013--one-binary-identical-stateless-replicas-inv-2)) and coordinate only through
PostgreSQL ([ADR-023](#adr-023--high-availability-through-postgresql-coordination-inv-12)). How readiness reacts to an object-storage outage is open on three pages
([Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness), [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure)).

**Alternatives considered.** None; set by the owner.

**Spec:** [owner constraints](spec/README.md#owner-constraints), [R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node), [R-HA-007](spec/ha-and-helm.md#r-ha-007--failure-mode-contract).

### ADR-006 · Deployment through a Helm chart

**Status:** Accepted (owner constraint)

**Context.** The owner fixes the deployment mechanism.

**Decision.** trove is deployed to Kubernetes with a Helm chart.

**Consequences.** [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) (INV-13) makes the chart the supported deployment and fixes what it
renders. Open: Gateway API priority ([Q-HA-7](spec/ha-and-helm.md#q-ha-7--priority-of-gateway-api)) and where images and chart are published
([Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart)).

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
URL layout ([Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci) for OCI).

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
OAuth RFCs. Where a design question needs a visual reference, the owner is asked for an example
screenshot ([Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested), [Q-UI-3](spec/api-and-ui.md#q-ui-3--layout-of-the-package-details-page-example-screenshot-requested), [Q-UI-4](spec/api-and-ui.md#q-ui-4--layout-of-the-policy-editor-and-the-explainer-example-screenshot-requested)); the outcome is described in trove's own terms and
the screenshot's origin is not recorded.

**Consequences.** The [feature catalog](features/README.md) uses its own IDs and generic names,
and protocol behavior is specified from the ecosystems' own specifications and client sources,
which the spec pages cite. A check for forbidden references cannot live in the repository: its
pattern list would itself name what it forbids. It has to run from outside the repository, for
example from a CI variable or the review tooling. Not verified: no such check exists in the
repository today.

**Alternatives considered.** None; set by the owner.

**Spec:** not yet listed among the [owner constraints](spec/README.md#owner-constraints) of
spec/README.md, which should add it in its next change; [Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested).

## Design invariants

The invariants of [spec/README.md](spec/README.md#design-invariants) are the baseline every spec
page builds on. A page may refine an invariant but must not contradict it; where a page finds one
wrong, it keeps it and raises an open question, which is listed with the entry.

### ADR-012 · One toolchain, one image (INV-1)

**Status:** Proposed

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

### ADR-013 · One binary, identical stateless replicas (INV-2)

**Status:** Proposed

**Context.** High availability ([ADR-005](#adr-005--high-availability)) needs replicas that can be added, removed and
replaced without coordination steps.

**Decision.** One binary, `trove`, with the subcommands `serve`, `migrate` and `admin`. Every
replica is identical and stateless and serves HTTP as well as background work; there is no
primary. The roles `api` and `worker` can be split by the chart; the default is `all`.

**Consequences.** Scaling is the replica count. Anything a later request needs (sessions, upload
sessions, jobs, locks, cache-fill state) lives in PostgreSQL or object storage; in-process state is
limited to rebuildable caches and the running request's spool file. Work that must run once runs as
a job or under a lease ([ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work), [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing)). The role split is P1 ([R-HA-002](spec/ha-and-helm.md#r-ha-002--role-split-into-api-and-worker-deployments)). The replica
registry's MVP scope is open ([Q-HA-12](spec/ha-and-helm.md#q-ha-12--replica-registry-at-mvp)).

**Alternatives considered.** A primary node with failover: an election to get right and a single
point of failure while it runs. A separate worker binary: two artifacts for one release. Peer
membership between replicas (gossip, consensus): needs replica-to-replica networking, which this
design avoids entirely ([R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node)).

**Spec:** [INV-2](spec/README.md#design-invariants), [R-ARCH-001](spec/architecture.md#r-arch-001--one-binary-identical-replicas-process-roles), [R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node),
[R-HA-002](spec/ha-and-helm.md#r-ha-002--role-split-into-api-and-worker-deployments).

### ADR-014 · PostgreSQL is the only stateful dependency besides object storage (INV-3)

**Status:** Proposed

**Context.** Configuration, metadata, identities, audit, jobs and coordination all need durable,
shared state. Each additional stateful system needs its own HA, backup and upgrade story.

**Decision.** PostgreSQL holds repository configuration, package and file metadata, users, groups,
tokens, permissions, audit events, the job queue and cross-replica coordination. No Redis, no
shared filesystem, no embedded database. Migrations are embedded in the binary and applied by
`trove migrate` (Helm pre-install and pre-upgrade Job) under an advisory lock. Migrations follow
expand/contract, so N and N-1 run side by side during a rolling upgrade.

**Consequences.** PostgreSQL's availability is trove's availability for nearly every request.
Coordination traffic lands on the database and is engineered to stay small ([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early),
[Q-ARCH-8](spec/architecture.md#q-arch-8--notify-on-hot-path-commits)). A connection budget per replica is part of sizing ([R-HA-016](spec/ha-and-helm.md#r-ha-016--connection-budget-and-pool-sizing)). Open: driver and
query layer ([Q-ARCH-2](spec/architecture.md#q-arch-2--postgresql-driver-and-query-layer)), migration runner ([Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner)), version floor ([Q-HA-2](spec/ha-and-helm.md#q-ha-2--postgresql-version-floor)).

**Alternatives considered.** Redis for caches, locks and queues: a second stateful system to make
highly available and back up. A coordination service such as etcd: the same cost for a narrow
use. An embedded database per replica: no shared state, so no HA.

**Spec:** [INV-3](spec/README.md#design-invariants), [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-ARCH-019](spec/architecture.md#r-arch-019--database-access-connection-naming-and-budget),
[R-ARCH-026](spec/architecture.md#r-arch-026--schema-migrations-and-the-compatibility-gate), [R-HA-013](spec/ha-and-helm.md#r-ha-013--supported-postgresql-versions-and-server-settings), [R-HA-018](spec/ha-and-helm.md#r-ha-018--expandcontract-schema-migrations).

### ADR-015 · Content-addressed blob store on S3 (INV-4)

**Status:** Proposed

**Context.** The same binary is often reachable through several repositories and paths (local
copies, promotions, cached upstream content). Storing it per path multiplies storage and makes copy
and move expensive.

**Decision.** Binaries are content-addressed by SHA-256 under
`<prefix>/sha256/<h0h1>/<h2h3>/<64 hex>` and stored once. An upload is spooled to local temporary
storage while SHA-256, SHA-1, MD5 and, where a format needs it, SHA-512 are computed, then written
to its address unless the blob already exists. PostgreSQL holds every reference; garbage
collection deletes unreferenced blobs after a grace period. No binary content in PostgreSQL.

**Consequences.** Deduplication across repositories; copy and move are metadata operations.
Garbage collection becomes a correctness-critical component ([ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period)). storage.md refines the
invariant: the database row, not the object store, decides whether a blob exists ([ADR-041](#adr-041--the-database-row-decides-whether-a-blob-exists)).
Each replica needs spool disk, which trove does not encrypt ([H-2](spec/architecture.md#h-2--the-upload-spool-is-not-encrypted-by-trove)). Upload timing reveals
whether content is already stored ([H-13](spec/storage.md#h-13--deduplication-reveals-the-existence-of-known-content-through-upload-timing)). Open: SHA-512 for every blob ([Q-STOR-2](spec/storage.md#q-stor-2--sha-512-for-every-blob-or-only-on-demand)).

**Alternatives considered.** Object keys that mirror repository paths: browsable, but no
deduplication and copy or move become object copies. Small blobs in PostgreSQL: two storage paths
and a much larger database backup. A shared filesystem: excluded by the owner constraint and by
INV-3.

**Spec:** [INV-4](spec/README.md#design-invariants), [R-STOR-001](spec/storage.md#r-stor-001--content-addressed-object-key-layout), [R-STOR-002](spec/storage.md#r-stor-002--digest-set-per-blob-and-per-file),
[R-STOR-004](spec/storage.md#r-stor-004--upload-pipeline-admission-spool-and-single-pass-hashing), [R-STOR-005](spec/storage.md#r-stor-005--blob-commit-protocol-and-deduplication).

### ADR-016 · Repository model: local, remote, virtual (INV-5)

**Status:** Proposed

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

**Status:** Proposed

**Context.** Many protocols, the management API, raw file access, the browser login and the UI
share one host; operational endpoints must not be reachable through the public edge.

**Decision.** Client listener on port 8080: `/api/v1/…` (management API, with the OpenAPI 3.1
document at `/api/v1/openapi.json` as the source of truth), `/api/<format>/<repoKey>/…` (package
protocols), `/files/<repoKey>/<path>` (raw file access), `/auth/…` (browser login), `/ui/…`
(Angular UI). Operations listener on port 8081: `/healthz`, `/readyz`, `/metrics`.

**Consequences.** Every protocol is namespaced per format and repository, so formats cannot
collide. Protocols that resolve paths against the host root do not fit: npm trusted publishing and
attestations ([Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6)) and the OCI distribution API's `/v2/` ([Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci)); both questions
propose amending this invariant. With metrics authentication off by default, the operations
listener relies on the chart's NetworkPolicy ([H-41](spec/operations-and-observability.md#h-41--the-operations-listener-relies-on-the-networkpolicy)).

**Alternatives considered.** One host name per repository: the host root becomes the registry
root, at the cost of DNS, TLS and Ingress objects per repository. One format at the host root:
limits an installation to one format per host. Metrics on the client listener: exposes operational
data through the edge.

**Spec:** [INV-6](spec/README.md#design-invariants), [R-ARCH-005](spec/architecture.md#r-arch-005--http-listeners-routing-and-middleware-chain), [R-API-001](spec/api-and-ui.md#r-api-001--base-path-versions-and-compatibility),
[R-NPM-001](spec/formats/npm.md#r-npm-001--registry-endpoint-routing-and-repository-classes), [R-NUGET-001](spec/formats/nuget.md#r-nuget-001--nuget-repositories-and-endpoint-layout).

### ADR-018 · Humans authenticate only through an external OIDC provider (INV-7)

**Status:** Proposed

**Context.** Organizations already run an identity provider with MFA, joiner and leaver processes
and group management. A second password store duplicates all of that.

**Decision.** Humans log in through an external OpenID Connect provider (authorization code flow
with PKCE; for example Keycloak, Microsoft Entra ID, GitLab, Dex). trove is a relying party and
stores no user passwords. Users are created just in time from ID-token claims; group membership
comes from a configurable claim and maps to trove groups. One local break-glass administrator
exists for bootstrap and recovery ([ADR-047](#adr-047--a-break-glass-administrator-outside-the-database)).

**Consequences.** An identity-provider outage blocks new UI logins; existing tokens and the
break-glass account keep working. Offboarding depends on the provider; without SCIM (P1,
[R-AUTH-039](spec/identity-and-authentication.md#r-auth-039--scim-20-provisioning)) a removed user keeps tokens until dormancy or manual disabling ([H-16](spec/identity-and-authentication.md#h-16--idp-side-deactivation-without-scim),
[Q-AUTH-5](spec/identity-and-authentication.md#q-auth-5--dormancy-default)). The strength of the provider's authentication is not checked ([H-18](spec/identity-and-authentication.md#h-18--authentication-strength-at-the-idp-is-not-checked)). Open: the
behavior when the group claim is missing ([Q-AUTH-4](spec/identity-and-authentication.md#q-auth-4--missing-group-claim)) and where provider configuration lives
([Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives)).

**Alternatives considered.** Local users with passwords: password storage, reset and MFA to build
and secure. LDAP bind: user passwords pass through trove. Protocols other than OIDC were not
specified; the named identity providers all speak OIDC.

**Spec:** [INV-7](spec/README.md#design-invariants), [R-AUTH-004](spec/identity-and-authentication.md#r-auth-004--provider-configuration-and-discovery), [R-AUTH-006](spec/identity-and-authentication.md#r-auth-006--callback-handling-and-id-token-validation),
[R-AUTH-007](spec/identity-and-authentication.md#r-auth-007--claim-mapping-and-just-in-time-provisioning), [R-AUTH-008](spec/identity-and-authentication.md#r-auth-008--group-synchronization-from-claims), [R-AUTH-014](spec/identity-and-authentication.md#r-auth-014--break-glass-administrator).

### ADR-019 · trove issues opaque, prefixed tokens (INV-8)

**Status:** Proposed

**Context.** Package managers present static credentials and cannot run an OAuth refresh. Identity
provider access tokens are short-lived, audience-bound to other services and not revocable by
trove.

**Decision.** trove issues its own credentials and never accepts an identity provider's access
token on package endpoints. Every token is an opaque random string with a type prefix
(`trv_pat_` personal, `trv_sat_` service account, `trv_cit_` CI), followed by 32 random bytes in
base62 and a 6-character base62 CRC32 checksum, so secret scanners can recognize and pre-validate a
leaked token offline. Tokens are stored as SHA-256 hashes, revocable immediately, and carry an
expiry and a scope. UI sessions are separate: a server-side row referenced by the `trv_session`
cookie. Clients present a token as Bearer, as the HTTP Basic password, or as `X-NuGet-ApiKey`.

**Consequences.** Every package client needs a token from trove: created in the UI, through
command-line login ([ADR-049](#adr-049--command-line-login-through-the-browser-session-p1)) or through the CI exchange ([ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10)). Tokens are bearer
credentials ([H-19](spec/identity-and-authentication.md#h-19--tokens-are-bearer-credentials)). A prefix never changes meaning; new types get new prefixes ([R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy)).
Open: the `__Host-` cookie prefix ([Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name)), refresh tokens ([Q-AUTH-3](spec/identity-and-authentication.md#q-auth-3--refresh-tokens-or-rotation)), administrators
issuing tokens for users ([Q-AUTH-7](spec/identity-and-authentication.md#q-auth-7--administrators-issuing-tokens-for-users)).

**Alternatives considered.** Accepting identity-provider access tokens on package endpoints:
clients cannot refresh them, and revocation depends on the provider. Self-contained signed tokens:
immediate revocation still needs a lookup per request, so the signature adds size without saving
the lookup.

**Prefix.** The owner chose `trv_` on 2026-10-28, when the project was renamed to trove (also
for the cookies: `trv_session`, `trv_csrf`, `trv_oidc_binding`). Alternatives were `trove_`
(self-explanatory in a leak report, longer) and keeping the pre-rename `apkg_` (no longer
explains itself). With `trv_` a token is 57 characters long.

**Spec:** [INV-8](spec/README.md#design-invariants), [R-AUTH-001](spec/identity-and-authentication.md#r-auth-001--credential-sources-and-precedence), [R-AUTH-022](spec/identity-and-authentication.md#r-auth-022--token-types-and-format),
[R-AUTH-023](spec/identity-and-authentication.md#r-auth-023--token-storage-validation-and-caching), [R-AUTH-024](spec/identity-and-authentication.md#r-auth-024--token-scopes), [R-AUTH-033](spec/identity-and-authentication.md#r-auth-033--client-authentication-matrix).

### ADR-020 · Deny by default, allow-only permission policies (INV-9)

**Status:** Proposed

**Context.** Permissions must express "this group reads everything in `npm-all`" as well as "this CI
project writes only `@acme/billing-*`", and administrators must be able to predict and audit the
result.

**Decision.** Deny by default. Permissions are allow-only grants: a permission policy binds
principals (users, groups, service accounts, CI identity mappings) to resources (repositories by
key or pattern, narrowed by include and exclude patterns on paths or package names) and the actions
`read`, `annotate`, `deploy`, `overwrite`, `delete`, `manage`. The effective permission is the
union of the grants of the principal and its groups, intersected with the presented token's scope.
System administration is a separate role, not a repository action. Every decision can be audited.

**Consequences.** Without deny rules there is no rule order to get wrong; restricting means
narrowing an allow. Actions do not imply each other ([ADR-051](#adr-051--actions-are-explicit-and-a-token-scope-only-narrows)). A decision explainer is part of
the API ([R-AUTHZ-024](spec/authorization.md#r-authz-024--effective-permissions-and-decision-explanation)). Open: a built-in principal for all authenticated users
([Q-AUTHZ-3](spec/authorization.md#q-authz-3--built-in-principal-for-all-authenticated-users)).

**Alternatives considered.** Allow and deny rules with precedence: more expressive, but the outcome
depends on evaluation order and is hard to review. Fixed per-repository roles (reader, writer,
admin): cannot narrow by package name, which CI grants need. A general policy language: powerful,
but not something repository administrators can review in a form.

**Spec:** [INV-9](spec/README.md#design-invariants), [R-AUTHZ-001](spec/authorization.md#r-authz-001--deny-by-default-allow-only-grants), [R-AUTHZ-006](spec/authorization.md#r-authz-006--repository-actions),
[R-AUTHZ-013](spec/authorization.md#r-authz-013--token-scope-as-a-ceiling), [R-AUTHZ-015](spec/authorization.md#r-authz-015--evaluation-algorithm).

### ADR-021 · GitLab CI identity through ID-token exchange (INV-10)

**Status:** Proposed

**Context.** [ADR-008](#adr-008--gitlab-ci-jobs-obtain-permissions-through-oidc) requires CI permissions without stored secrets. GitLab offers two job
credentials: an OIDC ID token that third parties verify offline, and the job token, which only
GitLab itself can verify.

**Decision.** A GitLab job requests an ID token with the `id_tokens:` keyword (audience = trove's
base URL) and exchanges it at `POST /api/v1/auth/oidc/token` for a short-lived `trv_cit_` token.
trove verifies the signature against the configured issuer's JWKS and checks `iss`, `aud`, `exp`,
`nbf`, `iat`. Identity mappings match claims (`project_path`, `namespace_path`, `ref`, `ref_type`,
`ref_protected`, `environment`, `pipeline_source`, `user_login`, …) and grant permissions,
optionally templated from claims. Verifying a raw `CI_JOB_TOKEN` against the GitLab API is an
optional fallback, off by default.

**Consequences.** No CI secret is stored anywhere. trove trusts the GitLab instance's signing key
([H-24](spec/gitlab-ci-oidc.md#h-24--trust-in-the-gitlab-instance-and-its-signing-key)). A CI token can outlive its job by its remaining lifetime ([H-26](spec/gitlab-ci-oidc.md#h-26--ci-tokens-outlive-the-job), [ADR-063](#adr-063--a-ci-tokens-lifetime-is-bounded-by-its-job)). Open:
whether to build the job-token fallback at all ([Q-CI-5](spec/gitlab-ci-oidc.md#q-ci-5--build-the-ci_job_token-fallback-at-all)) and how to treat tokens with several
audiences ([Q-CI-3](spec/gitlab-ci-oidc.md#q-ci-3--default-for-id-tokens-with-several-audiences)).

**Alternatives considered.** Long-lived service-account tokens in CI variables: the leak-prone
practice this replaces. The job token as the primary path: a GitLab API call per exchange, a GitLab
credential in trove's hands ([H-28](spec/gitlab-ci-oidc.md#h-28--the-fallback-hands-trove-a-gitlab-credential)), no way to recognize fork pipelines ([H-29](spec/gitlab-ci-oidc.md#h-29--the-job-token-fallback-cannot-recognize-fork-pipelines)), and never
safe for writes. Accepting the ID token directly on package endpoints: contradicts INV-8, and not
every package client can present it.

**Spec:** [INV-10](spec/README.md#design-invariants), [gitlab-ci-oidc.md](spec/gitlab-ci-oidc.md),
[R-CI-004](spec/gitlab-ci-oidc.md#r-ci-004--subject-token-validation), [R-CI-007](spec/gitlab-ci-oidc.md#r-ci-007--token-exchange-endpoint), [R-CI-009](spec/gitlab-ci-oidc.md#r-ci-009--identity-mapping-model-and-evaluation-order), [R-CI-022](spec/gitlab-ci-oidc.md#r-ci-022--ci_job_token-fallback).

### ADR-022 · Pull-through proxy semantics (INV-11)

**Status:** Proposed

**Context.** CI runs install the same public packages thousands of times, often many at once on a
cold cache, and must keep working when the upstream is slow or down.

**Decision.** A remote repository fetches from its upstream on a cache miss and stores the binary in
the blob store. It caches metadata for a configurable period, caches misses (negative cache), and
serves stale metadata when the upstream is unreachable. Concurrent misses for the same object are
collapsed: single-flight within a process plus a PostgreSQL advisory lock across replicas. URLs in
upstream metadata are rewritten to point at trove.

**Consequences.** The upstream sees one request per object, whatever the number of replicas and
concurrent clients. Clients never learn upstream URLs. The cross-replica lock holds a connection for
the whole transfer, harmless for npm and NuGet but not for large container layers ([Q-ARCH-6](spec/architecture.md#q-arch-6--cross-replica-collapse-holds-a-connection-for-the-whole-upstream-transfer));
the lock key form is open ([Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills)). Failure handling is [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving).

**Alternatives considered.** Collapsing only within a replica: N replicas mean up to N upstream
fetches per cold object. Redirecting clients to the upstream: no cache, no availability gain, and
the upstream sees every internal client. Mirroring whole registries ahead of time: storage for
packages nobody uses and freshness lag for those they do.

**Spec:** [INV-11](spec/README.md#design-invariants), [R-REPO-024](spec/repositories-and-proxy.md#r-repo-024--remote-repository-settings), [R-REPO-025](spec/repositories-and-proxy.md#r-repo-025--remote-request-path),
[R-REPO-026](spec/repositories-and-proxy.md#r-repo-026--collapsing-concurrent-misses-single-flight-and-advisory-lock), [R-REPO-043](spec/repositories-and-proxy.md#r-repo-043--metadata-url-rewriting).

### ADR-023 · High availability through PostgreSQL coordination (INV-12)

**Status:** Proposed

**Context.** Replicas must invalidate each other's caches, share a job queue and run singleton
work exactly once, without a coordination system of their own.

**Decision.** At least two replicas behind a Kubernetes Service and Ingress, with a
PodDisruptionBudget and readiness tied to database and object-storage reachability. Replicas
coordinate only through PostgreSQL: `LISTEN/NOTIFY` for cache invalidation, `FOR UPDATE SKIP
LOCKED` for the job queue, advisory-lock leases for singleton schedulers. PostgreSQL HA and S3
durability come from the platform; the chart does not bundle a production database.

**Consequences.** Replicas never open connections to each other: no peer discovery, no
replica-to-replica network rule ([R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node)). Session-level locks and `LISTEN` rule out
transaction-mode connection poolers ([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers)). The readiness clause is contested: storage.md,
operations-and-observability.md and ha-and-helm.md each show that a shared S3 outage turns every
replica unready at once and recommend revising it ([Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12), [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness), [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure)).

**Alternatives considered.** Consensus or gossip between replicas: peer networking and split-brain
handling. A separate lock or queue service: another stateful dependency against [ADR-014](#adr-014--postgresql-is-the-only-stateful-dependency-besides-object-storage-inv-3).
Kubernetes Lease objects for leader election: write access to the Kubernetes API, and no fencing of
the database writes the leader makes ([ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing)).

**Spec:** [INV-12](spec/README.md#design-invariants), [R-HA-001](spec/ha-and-helm.md#r-ha-001--active-active-replicas-without-a-primary-node), [R-HA-004](spec/ha-and-helm.md#r-ha-004--cross-replica-invalidation-over-listennotify), [R-HA-005](spec/ha-and-helm.md#r-ha-005--leader-less-scheduling-and-job-leases),
[R-HA-006](spec/ha-and-helm.md#r-ha-006--singleton-work-under-advisory-lock-leases-with-fencing), [R-HA-008](spec/ha-and-helm.md#r-ha-008--readiness-and-liveness-under-dependency-failure).

### ADR-024 · The Helm chart is the supported deployment (INV-13)

**Status:** Proposed

**Context.** [ADR-006](#adr-006--deployment-through-a-helm-chart) fixes Helm; the chart's scope and its handling of secrets decide how safe
an installation is by default.

**Decision.** The chart `trove` in `deploy/helm/trove` renders the Deployment(s), a migration
Job hook, Service, Ingress or Gateway route, PodDisruptionBudget, HorizontalPodAutoscaler,
ServiceMonitor and NetworkPolicy. Secrets come from existing Kubernetes Secrets; S3 credentials from
workload identity (IRSA and similar) or static keys.

**Consequences.** The chart never creates secrets and refuses to render invalid combinations
([ADR-079](#adr-079--the-chart-validates-before-it-renders-and-never-creates-secrets)). Open: Gateway API priority ([Q-HA-7](spec/ha-and-helm.md#q-ha-7--priority-of-gateway-api)), the egress NetworkPolicy default
([Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy)), and the publishing location ([Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart)).

**Alternatives considered.** A Kubernetes operator with custom resources: automated upgrades, but a
controller with cluster-wide permissions to install and secure. Plain manifests or Kustomize: no
validated parameters. Chart-generated secrets: regenerated wherever Helm's `lookup` is unavailable
([Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key)).

**Spec:** [INV-13](spec/README.md#design-invariants), [R-HA-028](spec/ha-and-helm.md#r-ha-028--chart-scope-rendered-resources-and-naming), [R-HA-029](spec/ha-and-helm.md#r-ha-029--fail-fast-chart-validation), [R-HA-030](spec/ha-and-helm.md#r-ha-030--configuration-rendering-and-secret-wiring),
[R-HA-031](spec/ha-and-helm.md#r-ha-031--values-reference).

### ADR-025 · npm and NuGet v3 are the MVP formats (INV-14)

**Status:** Proposed

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

**Status:** Proposed

**Context.** Some settings are needed before the database is reachable or must survive a broken
database; others are changed daily by administrators.

**Decision.** One YAML file (`/etc/trove/trove.yaml`), every key overridable by an environment
variable `TROVE_<SECTION>_<KEY>`; secrets are referenced from files or environment variables,
never written inline into a ConfigMap. Repositories, permissions and OIDC mappings live in
PostgreSQL and are managed through the API and UI. A declarative bootstrap file (configuration as
code) is P1 ([ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1)).

**Consequences.** Bootstrap-critical settings (database, object store, base URL, keys) change by
rollout; everything else changes at runtime and is audited. [R-ARCH-020](spec/architecture.md#r-arch-020--configuration-schema-in-code-and-the-filedatabase-split) states which side each
setting belongs to. Open: whether login providers stay in the file ([Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives), [Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents)).

**Alternatives considered.** Everything in the file: every repository change needs a rollout.
Everything in the database: no way to start or recover an installation whose database settings are
wrong. Kubernetes custom resources for repositories: ties repository management to cluster
permissions.

**Spec:** [INV-15](spec/README.md#design-invariants), [R-ARCH-020](spec/architecture.md#r-arch-020--configuration-schema-in-code-and-the-filedatabase-split), [R-OPS-028](spec/operations-and-observability.md#r-ops-028--configuration-file-and-environment-mapping),
[R-OPS-029](spec/operations-and-observability.md#r-ops-029--configuration-as-code-document).

### ADR-027 · Structured logs, Prometheus metrics, OTLP traces, audit in PostgreSQL (INV-16)

**Status:** Proposed

**Context.** Operators need standard telemetry; security teams need an audit trail they can query
without an external log system.

**Decision.** Structured JSON logs through `log/slog`; Prometheus metrics prefixed `trove_`;
optional OpenTelemetry traces over OTLP. Audit events (actor, action, repository, path, outcome,
source) are stored in PostgreSQL and emitted to the log stream.

**Consequences.** Audit can be queried and exported through the API and UI ([ADR-080](#adr-080--audit-records-commit-with-the-change-they-record)). The
database copy is not tamper-evident ([H-39](spec/operations-and-observability.md#h-39--audit-records-are-not-tamper-evident), [Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records)). Metric labels need cardinality caps
([R-OPS-007](spec/operations-and-observability.md#r-ops-007--metric-naming-labels-and-cardinality), [Q-OPS-10](spec/operations-and-observability.md#q-ops-10--default-cap-for-the-repo-metric-label)).

**Alternatives considered.** Audit only in the log stream: no query API without an external log
store. A separate audit store: another stateful dependency.

**Spec:** [INV-16](spec/README.md#design-invariants), [R-OPS-006](spec/operations-and-observability.md#r-ops-006--metrics-endpoint), [R-OPS-009](spec/operations-and-observability.md#r-ops-009--log-stream-and-record-format),
[R-OPS-015](spec/operations-and-observability.md#r-ops-015--audit-recording), [R-OPS-018](spec/operations-and-observability.md#r-ops-018--trace-context-and-opentelemetry-export).

### ADR-028 · The UI uses only the public API (INV-17)

**Status:** Proposed

**Context.** Whatever the UI can do, administrators want to script.

**Decision.** The Angular UI uses only the public `/api/v1` API, so every UI action is scriptable.

**Consequences.** No private UI endpoints; a UI feature needs its documented API first. The UI also
moves bytes through `/files/…`, which the OpenAPI document describes; [Q-API-4](spec/api-and-ui.md#q-api-4--inv-17s-wording-and-byte-transfers-on-files) proposes amending
the wording to "public, documented endpoints".

**Alternatives considered.** A backend-for-frontend with UI-only endpoints: faster UI iteration,
but actions that cannot be automated and a second surface to secure.

**Spec:** [INV-17](spec/README.md#design-invariants), [R-API-002](spec/api-and-ui.md#r-api-002--openapi-31-document-as-the-contract), [R-UI-005](spec/api-and-ui.md#r-ui-005--permission-driven-rendering).

## Architecture

### ADR-029 · Formats are compiled-in plug-ins behind a narrow host interface

**Status:** Proposed

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
flaw ([H-1](spec/architecture.md#h-1--format-code-runs-in-process-without-isolation)).

**Alternatives considered.** Out-of-process plug-ins over RPC: isolation, but a deployable per format
and a hop on every request, against [ADR-013](#adr-013--one-binary-identical-stateless-replicas-inv-2). WebAssembly or scripted plug-ins: isolation and
runtime loading, at the cost of streaming large bodies across a sandbox boundary. Generic path
storage with thin adapters: cannot express npm or NuGet metadata merging.

**Spec:** [R-ARCH-008](spec/architecture.md#r-arch-008--format-plug-in-contract), [R-ARCH-009](spec/architecture.md#r-arch-009--core-services-offered-to-formats), [R-ARCH-010](spec/architecture.md#r-arch-010--format-registry-and-conformance-kit), [R-ARCH-028](spec/architecture.md#r-arch-028--resource-limits-for-untrusted-package-content); open: [Q-ARCH-1](spec/architecture.md#q-arch-1--http-router).

### ADR-030 · One origin for UI, API and package content

**Status:** Proposed

**Context.** Package content is attacker-controlled and can be HTML, SVG or XML. Served from the
UI's origin, it could run script with the viewer's session.

**Decision.** UI, management API and package endpoints share one host name and listener. Every
response whose body is package content or package metadata carries
`X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox; default-src 'none'`, and
files are served with `Content-Disposition: attachment`. The UI never embeds package-provided HTML;
it renders READMEs only through its own sanitizer ([ADR-087](#adr-087--readmes-rendered-on-the-server-through-an-allowlist-sanitizer)). The conformance kit fails a format
whose content routes lack the headers. A separate content origin is not supported in the MVP.

**Consequences.** One host name, one certificate, no cross-origin configuration. The protection is
header-based: a route that omits the headers, a proxy that strips them, or a browser that ignores
CSP `sandbox` reopens the risk ([H-3](spec/architecture.md#h-3--ui-and-package-content-share-one-origin)).

**Alternatives considered.** A separate content origin without the session cookie: stronger
isolation, at the cost of a second host name with its own DNS, certificate and edge route; it stays
the upgrade path if H-3 becomes pressing.

**Spec:** [R-ARCH-007](spec/architecture.md#r-arch-007--one-origin-for-ui-and-package-content), [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery), [R-UI-008](spec/api-and-ui.md#r-ui-008--sanitized-readme-rendering).

### ADR-031 · Caches validate themselves; notifications only evict early

**Status:** Proposed

**Context.** Every replica caches configuration, grants, tokens and merged metadata.
`LISTEN/NOTIFY` delivery is lost while a connection is down, and PostgreSQL serializes the commits
of notifying transactions on one lock.

**Decision.** Every cache is registered with a size bound and a mandatory safety TTL. Keys that make
an entry self-validating (a source digest, a `rev_seq`, an epoch) are preferred; notifications then
only evict early. Administrative changes notify inside the writing transaction. Hot-path changes
(publish, delete, cache fill, metadata refresh) go through a per-replica post-commit coalescer. A
replica that loses its `LISTEN` session flushes every registered cache. Merged metadata checks the
`rev_seq` of local members on each hit, so a publish is visible through a virtual repository on
every replica at once.

**Consequences.** A missed notification costs freshness for at most the safety TTL, never more.
Each merged-metadata hit costs one indexed query. The coalescer caps notifying commits per replica
whatever the load.

**Alternatives considered.** NOTIFY inside every writing transaction: couples upload throughput to
a server-wide lock. Notifications as the only invalidation: stale data after any dropped
connection. No in-process caches: database round trips for grants and tokens on every request.

**Spec:** [R-ARCH-012](spec/architecture.md#r-arch-012--read-your-writes-package-metadata-for-local-repositories), [R-ARCH-015](spec/architecture.md#r-arch-015--request-path-virtual-resolution), [R-ARCH-018](spec/architecture.md#r-arch-018--in-process-caches-and-listennotify-plumbing), [R-REPO-020](spec/repositories-and-proxy.md#r-repo-020--merged-metadata-and-its-cache); open: [Q-ARCH-8](spec/architecture.md#q-arch-8--notify-on-hot-path-commits),
[Q-ARCH-9](spec/architecture.md#q-arch-9--freshness-of-merged-metadata-for-local-members).

### ADR-032 · Stored secrets are sealed under a key ring the operator supplies

**Status:** Proposed

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
which change once after a rotation.

**Alternatives considered.** References to external secret stores only: no sealed values in the
database, but credentials typed into the UI would need file access on every replica ([Q-REPO-1](spec/repositories-and-proxy.md#q-repo-1--who-specifies-the-stored-secret-mechanism-iam-045)).
A chart-generated key kept with Helm's `lookup`: silently regenerated under `helm template` and
GitOps tools ([Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key)). Envelope encryption through a cloud KMS: a dependency not every
installation has; it fits later as an additional key source.

**Spec:** [R-ARCH-021](spec/architecture.md#r-arch-021--encryption-of-stored-secrets-and-the-stored-secret-key), [R-REPO-033](spec/repositories-and-proxy.md#r-repo-033--upstream-authentication-and-stored-secrets), [R-HA-027](spec/ha-and-helm.md#r-ha-027--encryption-keys-for-sealed-secrets-in-a-replicated-deployment), [R-HA-029](spec/ha-and-helm.md#r-ha-029--fail-fast-chart-validation); open: [Q-REPO-1](spec/repositories-and-proxy.md#q-repo-1--who-specifies-the-stored-secret-mechanism-iam-045),
[Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key).

### ADR-033 · Versioning promises per surface and a schema compatibility gate

**Status:** Proposed

**Context.** Rolling upgrades run N and N-1 against one schema ([ADR-014](#adr-014--postgresql-is-the-only-stateful-dependency-besides-object-storage-inv-3)). Clients, scripts,
dashboards and configuration depend on surfaces that change at different speeds.

**Decision.** Releases follow SemVer. Any later minor of the same major installs directly;
zero-downtime rolling upgrades work from the previous minor and between patches, and `helm rollback`
to N-1 works. Each migration declares its phase, `expand` or `contract`; contract steps ship at the
earliest one minor after the release that stopped using the old objects and declare the oldest
release they allow. A single-row `schema_compat` table records the schema version and that release,
and `trove migrate` runs behind this gate under the `migration` advisory lock. Per surface:
`/api/v1` changes are additive only (checked in CI against the last release with an OpenAPI
breaking-change checker); a renamed configuration key or metric keeps its old name for at least two
minors; a token prefix never changes meaning.

**Consequences.** Removing schema objects takes two releases. Upgrades that skip a minor need steps
or downtime ([Q-HA-5](spec/ha-and-helm.md#q-ha-5--zero-downtime-upgrades-that-skip-minor-releases)). The schema itself is not a public interface.

**Alternatives considered.** Migrations with downtime: simpler code, no zero-downtime upgrades.
Keeping dropped objects for two minors: allows skips, doubles the life of dual-write code. A
migration tool's own failure state: needs a manual force before the next deploy ([Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner)).

**Spec:** [R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy), [R-ARCH-026](spec/architecture.md#r-arch-026--schema-migrations-and-the-compatibility-gate), [R-HA-018](spec/ha-and-helm.md#r-ha-018--expandcontract-schema-migrations), [R-HA-020](spec/ha-and-helm.md#r-ha-020--rolling-upgrade-and-mixed-version-operation), [R-HA-021](spec/ha-and-helm.md#r-ha-021--upgrade-policy-and-preparation); open:
[Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner), [Q-HA-5](spec/ha-and-helm.md#q-ha-5--zero-downtime-upgrades-that-skip-minor-releases).

### ADR-034 · One PostgreSQL job table for maintenance, API operations and internal work

**Status:** Proposed

**Context.** Garbage collection, retention runs, reindexing, repository deletion, webhook delivery
and data migrations must run once, survive restarts and be visible through the API.

**Decision.** One `job` table serves maintenance tasks, long-running API operations and internal
work. Workers claim jobs with `FOR UPDATE SKIP LOCKED` in short transactions; a lease column is
extended by heartbeat; deduplication keys work in `exclusive` or `coalesce` mode; scheduled runs
take a unique task slot; progress, checkpoint and result columns feed the job API. Long-running API
operations answer 202 with the job ([ADR-085](#adr-085--long-running-work-answers-202-with-a-job)).

**Consequences.** One migration path; no library manages tables of its own. Queue throughput is
bounded by PostgreSQL, which suits maintenance volumes; webhook delivery (P1) is the case to measure
([Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation)).

**Alternatives considered.** An external queue library on PostgreSQL: pre-1.0, with its own tables
and migrations, and a schema that would not hold the operation and task-slot columns. A message
broker: another stateful dependency.

**Spec:** [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-API-015](spec/api-and-ui.md#r-api-015--long-running-work-as-jobs), [R-OPS-019](spec/operations-and-observability.md#r-ops-019--scheduled-maintenance-tasks-and-job-visibility); open: [Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation), [Q-OPS-9](spec/operations-and-observability.md#q-ops-9--ownership-of-the-job-api).

## Repositories and pull-through proxy

### ADR-035 · Virtual resolution in three tiers, decided per member

**Status:** Proposed

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

**Status:** Proposed

**Context.** Tier order protects only names that currently exist internally. A name that is not yet
published, was deleted or is misspelt falls through to public upstreams, where anyone can register
it with a higher version.

**Decision.** Local repositories hold claimed name patterns. With auto-claim (proposed default for
formats that support claims), the first deploy of an unclaimed name creates an exact claim with
scope `members`; claims survive deletion. `members` claims act only in virtual repositories that
contain the claiming repository; `global` claims act in every virtual repository of the format and
need `repositories:write`. A claimed name resolves only from the most specifically claiming local
members, and remote members are not asked. With remote refusal on, every remote repository of the
format refuses `global`-claimed names, also when addressed directly. Pin rules on a virtual
repository bind name patterns to members.

**Consequences.** The protection sits on the server and does not depend on each project's client
configuration. Claimed names are observable ([H-7](spec/repositories-and-proxy.md#h-7--claimed-names-are-observable)); member-scoped claims do not protect other
virtual repositories ([H-9](spec/repositories-and-proxy.md#h-9--member-scoped-claims-do-not-protect-other-virtual-repositories)); a deploy to a local member shadows a public package for every
consumer ([H-10](spec/repositories-and-proxy.md#h-10--deploy-on-a-local-member-shadows-public-packages-for-every-consumer)). Who may publish a name is a separate mechanism ([ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish)).

**Alternatives considered.** Client-side scoping only (npm scopes, NuGet package source mapping):
depends on every project getting its configuration right. Tier order alone: misses unpublished
names. `global` claims from any deploy: one deploy into a sandbox repository would block a public
name everywhere.

**Spec:** [R-REPO-022](spec/repositories-and-proxy.md#r-repo-022--priority-members), [R-REPO-023](spec/repositories-and-proxy.md#r-repo-023--dependency-confusion-protection-pin-rules-and-namespace-claims), [R-NPM-033](spec/formats/npm.md#r-npm-033--pin-rules-and-reserved-namespaces-for-npm); open: [Q-REPO-4](spec/repositories-and-proxy.md#q-repo-4--scope-of-remote-refusal-for-claimed-names), [Q-REPO-5](spec/repositories-and-proxy.md#q-repo-5--generic-or-format-level-placement-of-pins-extra-hosts-and-upstream-authentication).

### ADR-037 · One name-pattern syntax, most specific wins, applied to every protocol request

**Status:** Proposed

**Context.** Include and exclude patterns, claims, pin rules, permission policies and retention
scopes all select packages. Different syntaxes would let a pattern copied from one into another
silently match something else.

**Decision.** A name pattern is an exact name or a prefix ending in `*`, compared after the format's
normalization: the syntax of NuGet's package source mapping. Where several match, an exact name
beats any prefix and a longer prefix beats a shorter one. npm and NuGet repositories use name
patterns rather than path patterns, so one pattern covers documents and files. Repository patterns
apply to every package-protocol request, administrators included.

**Consequences.** Routing safety does not depend on who runs the build. Documents that name no
package, such as NuGet's upstream service index, need an exemption ([Q-NUGET-7](spec/formats/nuget.md#q-nuget-7--patterns-and-repository-level-upstream-documents)). Retention and
permissions should share the matcher ([Q-STOR-9](spec/storage.md#q-stor-9--one-pattern-syntax-across-retention-and-permissions)).

**Alternatives considered.** Path globs for every format: two patterns per npm package, one for the
document and one for tarballs. A syntax per subsystem: silent mismatches. Patterns that skip
administrators: an administrator's build could leak internal names upstream.

**Spec:** [R-REPO-005](spec/repositories-and-proxy.md#r-repo-005--include-and-exclude-patterns), [R-AUTHZ-005](spec/authorization.md#r-authz-005--package-name-and-path-patterns), [R-STOR-038](spec/storage.md#r-stor-038--package-cleanup-policy-scope-and-conditions); open: [Q-STOR-9](spec/storage.md#q-stor-9--one-pattern-syntax-across-retention-and-permissions), [Q-NUGET-7](spec/formats/nuget.md#q-nuget-7--patterns-and-repository-level-upstream-documents).

### ADR-038 · Upstream failures: classified outcomes, a shared circuit, bounded stale serving

**Status:** Proposed

**Context.** Upstream outages and rate limits must not break builds that only need cached content,
and every replica retrying on its own would multiply the load on a struggling upstream.

**Decision.** Every upstream outcome has a fixed class: whether it counts toward the circuit and
what a metadata or artifact request answers. Server errors, connection failures, timeouts and 429
count; 404 and 410 write a negative-cache entry; authentication failures, certificate errors,
egress refusals and integrity failures do not count. Metadata falls back to the stored copy, bounded
by `cache.maxStaleSeconds` when set; artifacts answer 502, 503 or 504 with a specific code. The
circuit opens on a consecutive-failure threshold and is probed, shared across replicas. Only a
connection failure before any byte was sent is retried, once. A metadata period of `0` is allowed,
because revalidation is conditional and collapsed. A document the upstream removes is marked
`upstream_gone` and answers 404; cached artifacts stay served by default.

**Consequences.** Offline detection is deterministic and cluster-wide; builds continue on cached
metadata during outages. Upstream removals reach cached artifacts only where the format tracks
versions ([H-5](spec/repositories-and-proxy.md#h-5--upstream-removals-reach-cached-artifacts-only-where-the-format-tracks-versions), [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default)).

**Alternatives considered.** A count of background retries before a repository counts as offline:
not deterministic across replicas. Freshness from the upstream's `Cache-Control`: behavior would
depend on each upstream. A 30-second minimum period: protects the upstream from nothing that
collapsing does not already prevent ([Q-REPO-2](spec/repositories-and-proxy.md#q-repo-2--lower-bound-of-the-metadata-period)).

**Spec:** [R-REPO-027](spec/repositories-and-proxy.md#r-repo-027--metadata-freshness-and-refresh-timeout), [R-REPO-028](spec/repositories-and-proxy.md#r-repo-028--negative-cache), [R-REPO-029](spec/repositories-and-proxy.md#r-repo-029--upstream-failure-handling-and-the-assumed-offline-circuit), [R-REPO-030](spec/repositories-and-proxy.md#r-repo-030--offline-mode-per-repository), [R-REPO-031](spec/repositories-and-proxy.md#r-repo-031--global-offline-switch); open:
[Q-REPO-2](spec/repositories-and-proxy.md#q-repo-2--lower-bound-of-the-metadata-period), [Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document), [Q-NUGET-2](spec/formats/nuget.md#q-nuget-2--metadata-cache-period-of-nuget-remotes).

### ADR-039 · Egress address checks after DNS resolution for every remote connection

**Status:** Proposed

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
proxy the proxy connects on its own, so the check is weaker there ([H-6](spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks), [H-40](spec/operations-and-observability.md#h-40--outbound-address-checks-are-weaker-through-an-outbound-proxy)). The chart's
egress NetworkPolicy is an opt-in second layer ([Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy)).

**Alternatives considered.** Checking the host name before resolution: bypassed by DNS rebinding.
Denying private ranges by default: breaks the common internal-registry case. NetworkPolicy alone:
not available on every cluster, and unaware of per-repository intent.

**Spec:** [R-REPO-037](spec/repositories-and-proxy.md#r-repo-037--outbound-http-proxies), [R-REPO-038](spec/repositories-and-proxy.md#r-repo-038--egress-address-rules-for-remote-repositories), [R-REPO-039](spec/repositories-and-proxy.md#r-repo-039--upstream-redirects), [R-ARCH-022](spec/architecture.md#r-arch-022--outbound-http-client-factory), [R-OPS-042](spec/operations-and-observability.md#r-ops-042--delivery-retries-and-outbound-protection); open:
[Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy).

### ADR-040 · Strict repository configuration documents and asynchronous deletion

**Status:** Proposed

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

## Storage

### ADR-041 · The database row decides whether a blob exists

**Status:** Proposed

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

**Status:** Proposed

**Context.** Deduplicated blobs are shared; deleting one while an upload or copy adds a reference
would corrupt a package.

**Decision.** Candidates come from a queue fed whenever a reference disappears and from a periodic
full sweep, both older than `storage.gc.grace_period` (24 h proposed). Per blob, in one READ COMMITTED
transaction: `FOR UPDATE` on the row (which waits for uploads holding `FOR SHARE`), a reference check
in a separate statement, then the state `deleting`; after commit the object is deleted, and the row
is removed once a settle period has passed. Every step is idempotent. Runs are singleton jobs under
a lease, scheduled with five-field cron in UTC.

**Consequences.** Collection never removes an object a committed reference uses, and no upload
commits against a blob being deleted. The grace period also lets content that is deleted and
republished shortly after (common in CI) deduplicate, and bounds which point-in-time restores stay
consistent ([ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination)).

**Alternatives considered.** A reference counter column: drifts under crashes and concurrency.
Mark-and-sweep over the bucket listing alone: expensive on large buckets and racing with uploads.
No collection: unbounded growth.

**Spec:** [R-STOR-024](spec/storage.md#r-stor-024--race-free-garbage-collection), [R-STOR-025](spec/storage.md#r-stor-025--garbage-collection-schedule-full-sweep-and-manual-run); open: [Q-STOR-4](spec/storage.md#q-stor-4--cron-dialect-shared-by-all-schedules).

### ADR-043 · Object-store outage: no local queue, and 201 means durable

**Status:** Proposed

**Context.** S3 can become unreachable while PostgreSQL is fine. Queueing uploads locally would
keep accepting writes, but a replica loss would then lose acknowledged packages.

**Decision.** During an outage, uploads of new content answer 503 with `Retry-After` and commit
nothing; a 201 always means the content is durable in S3. Everything that touches only PostgreSQL
keeps working: deduplicated uploads, deploy by checksum, copy, move, delete, properties, trash
operations, `HEAD`, listings, UI and API. Downloads of stored content answer 503. Collection jobs
back off. Readiness follows INV-12 with hysteresis (fail after 60 s, recover after 10 s, proposed).

**Consequences.** No acknowledged write is ever lost to a replica failure. Under the current INV-12
wording a shared outage still removes every replica from the Service; storage.md recommends
changing that ([Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12)).

**Alternatives considered.** Asynchronous upload to S3 from local disk (deferred catalog entry
STOR-016): would change what 201 means.

**Spec:** [R-STOR-033](spec/storage.md#r-stor-033--connection-pool-timeouts-retries-and-circuit-breaker), [R-STOR-036](spec/storage.md#r-stor-036--behavior-during-an-object-store-outage); open: [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12).

### ADR-044 · A trash can that also covers overwrites (P1)

**Status:** Proposed

**Context.** Deletes and overwrites in local repositories are the classic accident, for example a CI
job republishing a release path.

**Decision.** With the trash can enabled (proposed default: on, 14 days), deletions in local
repositories mark items as trashed instead of removing them. This covers user deletes, overwrites of
user-deployed files, retention policies unless they bypass it, and repository caps. Generated
metadata is replaced without trash. Restore and permanent removal are explicit operations.

**Consequences.** Storage is freed only after the retention period; repositories that overwrite
routinely accumulate trash volume. npm tombstones survive the trash and its purge ([ADR-067](#adr-067--npm-versions-are-immutable-guarded-by-tombstones)).

**Alternatives considered.** Trash for deletes only: leaves `overwrite` as the one destructive
action without a safety net ([Q-STOR-5](spec/storage.md#q-stor-5--does-overwriting-a-file-send-the-previous-content-to-the-trash-can)).

**Spec:** [R-STOR-022](spec/storage.md#r-stor-022--trash-can), [R-STOR-023](spec/storage.md#r-stor-023--restore-and-permanent-removal-from-the-trash-can); open: [Q-STOR-5](spec/storage.md#q-stor-5--does-overwriting-a-file-send-the-previous-content-to-the-trash-can).

### ADR-045 · Presigned download redirects as a per-repository opt-in (P2)

**Status:** Proposed

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

**Status:** Proposed

**Context.** The UI needs a login state that can be ended immediately, on every replica.

**Decision.** A session is created only after a successful OIDC callback or break-glass login, and
any session cookie presented at that moment is replaced. The session secret is 32 random bytes;
PostgreSQL stores only its SHA-256. The cookie is `Secure; HttpOnly; SameSite=Lax; Path=/`, without
`Domain`, `Max-Age` or `Expires`. `Lax` is needed because the first navigation after the identity
provider's redirect must carry the cookie. Unsafe cookie-authenticated requests need a CSRF token
from a second cookie, sent back as a header. Proposed timeouts: idle 1 h, absolute 12 h.

**Consequences.** Logout and revocation take effect on every replica within the token-cache bound
([ADR-048](#adr-048--tokens-hashed-with-sha-256-a-positive-only-cache-bounded-revocation-latency)). The cookie flags are a convention until the `__Host-` prefix makes the browser
enforce them ([Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name)).

**Alternatives considered.** Stateless signed session cookies: no immediate logout without a
server-side deny-list. `SameSite=Strict`: the cookie would be missing right after the login
redirect.

**Spec:** [R-AUTH-010](spec/identity-and-authentication.md#r-auth-010--server-side-ui-sessions), [R-AUTH-011](spec/identity-and-authentication.md#r-auth-011--session-timeouts-and-recent-authentication), [R-AUTH-012](spec/identity-and-authentication.md#r-auth-012--csrf-protection-for-cookie-authenticated-requests), [R-AUTH-013](spec/identity-and-authentication.md#r-auth-013--logout-and-rp-initiated-logout); open: [Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name).

### ADR-047 · A break-glass administrator outside the database

**Status:** Proposed

**Context.** If the identity provider is down or misconfigured, someone must still be able to log in
and repair trove, without depending on the state being repaired.

**Decision.** Exactly one local account, always treated as system administrator without a role
binding. Its password never enters PostgreSQL: it comes from a mounted Kubernetes Secret, preferably
as an Argon2id hash (at least m=65536 KiB, t=3, p=4). A plaintext password must have at least 20
characters and must not be on an embedded list of common passwords, or the replica refuses to start.
A changed password file ends all break-glass sessions. The account can be disabled.

**Consequences.** Recovery works with an empty or broken permission configuration. The account is
single-factor ([H-17](spec/identity-and-authentication.md#h-17--break-glass-account-is-single-factor)), which is why it can be disabled and is meant for emergencies.

**Alternatives considered.** No local account: an identity-provider misconfiguration locks everyone
out. A local administrator with a password in the database: recovery would depend on the database
state it recovers.

**Spec:** [R-AUTH-014](spec/identity-and-authentication.md#r-auth-014--break-glass-administrator), [R-AUTH-015](spec/identity-and-authentication.md#r-auth-015--bootstrap-and-recovery-commands).

### ADR-048 · Tokens hashed with SHA-256, a positive-only cache, bounded revocation latency

**Status:** Proposed

**Context.** Package clients present a token on every request, and a revoked token must stop working
everywhere quickly.

**Decision.** PostgreSQL stores the SHA-256 of the full token; with 256 bits of entropy a salt or
slow hash adds nothing. Validation checks syntax and checksum first (no database access on failure),
then a per-replica cache of positive results, then the database. The cache is invalidated through
`LISTEN/NOTIFY` on every revocation, rotation, principal change and membership change; when the
listener drops, the cache is emptied and bypassed. A revoked token is rejected on every replica
within 2 s while notifications flow and never later than the cache TTL (15 s). Revocation by value
is open to anyone, because possession already allows revocation (proposed, [Q-AUTH-6](spec/identity-and-authentication.md#q-auth-6--unauthenticated-revoke-by-value)).

**Consequences.** One indexed lookup per uncached request. Secret scanners can revoke leaked tokens
directly.

**Alternatives considered.** A slow password hash: costs CPU on every uncached request and protects
nothing at this entropy. Caching negative results: would keep rejecting a token after a transient
error.

**Spec:** [R-AUTH-023](spec/identity-and-authentication.md#r-auth-023--token-storage-validation-and-caching), [R-AUTH-029](spec/identity-and-authentication.md#r-auth-029--token-revocation), [R-AUTH-030](spec/identity-and-authentication.md#r-auth-030--token-rotation); open: [Q-AUTH-6](spec/identity-and-authentication.md#q-auth-6--unauthenticated-revoke-by-value).

### ADR-049 · Command-line login through the browser session (P1)

**Status:** Proposed

**Context.** Users should get a token into npm or another tool without copying it from the UI, and
without trove ever handling their identity-provider password.

**Decision.** `npm login` uses npm's web-login protocol on top of the OIDC session: npm opens an
approval page, the user approves in the browser, npm polls and receives a personal access token.
Other tools use the OAuth 2.0 Device Authorization Grant (RFC 8628) with a user code that binds the
browser to the terminal. npm's legacy login accepts only an existing token as password and never
mints one.

**Consequences.** No password prompt in any client; every token is created by an authenticated
browser session. Consent phishing of the browser step ([H-15](spec/identity-and-authentication.md#h-15--consent-phishing-of-command-line-browser-login)) and login secrets in URLs outside
trove ([H-20](spec/identity-and-authentication.md#h-20--command-line-login-secrets-in-urls-outside-trove)) remain.

**Alternatives considered.** Username and identity-provider password in the client: trove would
handle passwords, against [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7). Copying tokens from the UI only: works, but pushes users to
long-lived tokens in dotfiles.

**Spec:** [R-AUTH-034](spec/identity-and-authentication.md#r-auth-034--npm-web-login), [R-AUTH-035](spec/identity-and-authentication.md#r-auth-035--npm-legacy-login-and-logout), [R-AUTH-036](spec/identity-and-authentication.md#r-auth-036--browser-login-handoff-for-other-command-line-tools), [R-NPM-020](spec/formats/npm.md#r-npm-020--web-login), [R-NPM-021](spec/formats/npm.md#r-npm-021--legacy-login-and-logout).

### ADR-050 · Anonymous access is off by default and needs two switches

**Status:** Proposed

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

## Authorization

### ADR-051 · Actions are explicit, and a token scope only narrows

**Status:** Proposed

**Context.** Implied actions ("deploy implies read") make policies shorter but their effect harder to
read. Tokens should be able to carry less than their owner holds, never more.

**Decision.** No action implies another; presets expand to explicit action lists when a policy is
written. A token scope is a ceiling: an action is allowed only if the subject's grants allow it and
the scope admits it. Repository configuration is not a repository action; it needs a system role or
project administration.

**Consequences.** What a policy grants is exactly what it lists. A scoped token loses rights when its
owner loses them, because grants are evaluated at use time.

**Alternatives considered.** Hierarchical actions: shorter policies, less obvious results.

**Spec:** [R-AUTHZ-001](spec/authorization.md#r-authz-001--deny-by-default-allow-only-grants), [R-AUTHZ-006](spec/authorization.md#r-authz-006--repository-actions), [R-AUTHZ-013](spec/authorization.md#r-authz-013--token-scope-as-a-ceiling); open: [Q-AUTHZ-9](spec/authorization.md#q-authz-9--shape-of-the-repository-part-of-a-token-scope).

### ADR-052 · Changing what clients install needs `deploy`, not `annotate`

**Status:** Proposed

**Context.** Moving an npm dist-tag, deprecating a version or relisting a NuGet version changes what
`npm install` or NuGet's version selection picks.

**Decision.** `deploy` covers creating new content and changing metadata that steers resolution:
npm dist-tags and deprecation, NuGet relist. `annotate` covers descriptive metadata that does not
change resolution, such as item properties. `overwrite` replaces existing bytes and is needed
together with `deploy`; formats may refuse overwrites entirely ([ADR-067](#adr-067--npm-versions-are-immutable-guarded-by-tombstones)).

**Consequences.** A principal given `annotate` for property tooling cannot redirect installs.
Ownership narrows both further ([ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish)).

**Alternatives considered.** `annotate` for dist-tags and deprecation: the earlier npm mapping,
decided against ([Q-NPM-9](spec/formats/npm.md#q-npm-9--annotate-or-deploy-for-dist-tags-and-deprecation)).

**Spec:** [R-AUTHZ-006](spec/authorization.md#r-authz-006--repository-actions), [R-AUTHZ-007](spec/authorization.md#r-authz-007--action-map-for-npm-and-nuget-operations), [R-NPM-016](spec/formats/npm.md#r-npm-016--dist-tags-and-the-latest-rule), [R-NPM-018](spec/formats/npm.md#r-npm-018--deprecate-and-undeprecate); resolved:
[Q-NPM-9](spec/formats/npm.md#q-npm-9--annotate-or-deploy-for-dist-tags-and-deprecation).

### ADR-053 · Virtual repositories: endpoint gate plus member rights

**Status:** Proposed

**Context.** If `read` on a virtual repository granted read on its members, adding a private
repository to it would silently expose that repository.

**Decision.** Using a virtual repository requires `read` on it as an endpoint gate; content is
decided on the member that holds or receives it. Unreadable members are skipped without turning the
answer into 403. Writes are checked on the deploy target.

**Consequences.** Administrators decide which aggregated endpoints a group uses, at the cost of one
extra selector in policies. Membership changes cannot widen anyone's access.

**Alternatives considered.** Implied gate (anyone who reads one member may use the virtual
repository): one grant fewer. `read` on the virtual repository sufficient for members: simplest, and
exposes members silently. Both are weighed in [Q-AUTHZ-1](spec/authorization.md#q-authz-1--endpoint-gate-on-virtual-repositories).

**Spec:** [R-AUTHZ-009](spec/authorization.md#r-authz-009--virtual-repositories), [R-REPO-019](spec/repositories-and-proxy.md#r-repo-019--virtual-resolution-algorithm); open: [Q-AUTHZ-1](spec/authorization.md#q-authz-1--endpoint-gate-on-virtual-repositories).

### ADR-054 · Cache fills are guarded by a configurable action, `read` by default

**Status:** Proposed

**Context.** Fetching an uncached package through a remote repository changes what the cache holds.
Some installations want a pull-through proxy anyone can use; others want a frozen mirror only a few
may extend.

**Decision.** Each remote repository names its cache-fill action. Proposed default `read`, so any
reader populates the cache; `deploy` turns the repository into a frozen mirror. Refreshing expired
metadata counts as populating, because a refreshed document advertises versions the caller could not
fetch otherwise. A caller who may read but not populate gets 403 `populate_not_permitted`.

**Consequences.** The common case behaves like a plain proxy, without inconsistent 404s between
users. [repositories-and-proxy.md](spec/repositories-and-proxy.md) and [formats/npm.md](spec/formats/npm.md) follow the `read` default; [Q-AUTHZ-4](spec/authorization.md#q-authz-4--default-populate-action-of-remote-repositories) keeps the alternative open.

**Alternatives considered.** `deploy` as default: frozen-mirror semantics for every installation.

**Spec:** [R-AUTHZ-008](spec/authorization.md#r-authz-008--remote-repositories-who-may-populate-the-cache), [R-REPO-024](spec/repositories-and-proxy.md#r-repo-024--remote-repository-settings); open: [Q-AUTHZ-4](spec/authorization.md#q-authz-4--default-populate-action-of-remote-repositories).

### ADR-055 · Refused reads answer 404, refused writes 403

**Status:** Proposed

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

**Status:** Proposed

**Context.** "A CI job may write the packages it produces" ([ADR-008](#adr-008--gitlab-ci-jobs-obtain-permissions-through-oidc)) must hold even when several
principals hold `deploy` on overlapping name patterns.

**Decision.** Local repositories of formats with package names carry `packageOwnership: enforce` or
`off`; the proposed default for new npm and NuGet repositories is `enforce`. The first publish claims
the name for the owner its grant designates: a user, a group, a service account, a CI identity
mapping, or a CI project. Later changes are reserved to owners and holders of `manage`. Ownership is
recorded per repository in the MVP; shared ownership domains follow in P1. CI grants with
`ownership: bind_project` always check ownership.

**Consequences.** One project cannot publish versions of another project's package even when the
name slugs collide. Name prefixes cannot be reserved by ownership alone ([H-21](spec/authorization.md#h-21--no-reservation-of-name-prefixes)); claims
([ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules)) decide resolution, ownership decides publishing.

**Alternatives considered.** No ownership, only non-overlapping patterns: collisions go unnoticed.
Administrator pre-registration only: administration for every new package.

**Spec:** [R-AUTHZ-018](spec/authorization.md#r-authz-018--package-ownership), [R-AUTHZ-019](spec/authorization.md#r-authz-019--package-ownership-api), [R-AUTHZ-020](spec/authorization.md#r-authz-020--shared-ownership-domains), [R-CI-015](spec/gitlab-ci-oidc.md#r-ci-015--package-ownership-binding); open:
[Q-AUTHZ-2](spec/authorization.md#q-authz-2--ownership-enforcement-default-for-new-npm-and-nuget-local-repositories), [Q-AUTHZ-6](spec/authorization.md#q-authz-6--default-ownership-of-ci-inline-grants-that-write), [Q-CI-6](spec/gitlab-ci-oidc.md#q-ci-6--how-a-ci-project-acquires-package-ownership).

### ADR-057 · A compiled grant index per replica, consistent through an epoch, failing closed

**Status:** Proposed

**Context.** Authorization runs on every request and must not query policy tables each time, yet a
removed grant must stop working everywhere.

**Decision.** Each replica evaluates against an in-memory index compiled from policies, role
bindings, repositories, projects and settings. Every change increments an epoch in the same
transaction and notifies; replicas rebuild on a higher epoch, poll it every 5 s (proposed), and
rebuild after reconnecting. A replica that has not confirmed the current epoch for longer than 30 s
(proposed) answers 503 to requests that need authorization and reports not ready. Group memberships,
tokens, CI mappings and ownership rows are not in the index; their owners cache or read them.

**Consequences.** Policy changes apply on every replica within the poll interval at worst, typically
within a second. A replica cut off from notifications and polling stops serving rather than serving
outdated grants.

**Alternatives considered.** Querying policies per request: database load proportional to traffic.
Caching without an epoch: no bound on staleness.

**Spec:** [R-AUTHZ-015](spec/authorization.md#r-authz-015--evaluation-algorithm), [R-AUTHZ-028](spec/authorization.md#r-authz-028--compiled-grant-index-and-invalidation-across-replicas).

### ADR-058 · Projects for delegated administration, reduced, in P1

**Status:** Proposed

**Context.** Larger installations want teams to administer their own repositories and policies
without system administrator rights.

**Decision.** P1 introduces projects in a reduced form: a project owns repositories, has project
administrators and owns policies that apply only to its repositories. Project roles, environments and
sharing between projects stay P2.

**Consequences.** Delegation without a full multi-tenant model. CI delegations stay independent of
projects in P1 ([Q-AUTHZ-7](spec/authorization.md#q-authz-7--ci-delegations-and-p1-projects)).

**Alternatives considered.** Full projects with roles and environments in P1: a large model before
there is usage to shape it. No delegation: every change goes through system administrators.

**Spec:** [R-AUTHZ-025](spec/authorization.md#r-authz-025--delegated-policy-management), [R-AUTHZ-026](spec/authorization.md#r-authz-026--projects-for-delegated-administration), [R-API-047](spec/api-and-ui.md#r-api-047--projects); open: [Q-AUTHZ-7](spec/authorization.md#q-authz-7--ci-delegations-and-p1-projects).

## GitLab CI OIDC

### ADR-059 · CI tokens through a standard token-exchange endpoint

**Status:** Proposed

**Context.** A job needs to turn its GitLab ID token into a trove token with standard tooling.

**Decision.** `POST /api/v1/auth/oidc/token` implements OAuth 2.0 Token Exchange (RFC 8693): the ID
token is the subject token, the response carries `access_token` (a `trv_cit_` token),
`expires_in` and `scope`. The issuance (`jti` record, token hash, issuance record, audit event) is
one transaction. Rejected subject tokens answer 400 `invalid_request` with a machine-readable
`reason`. An optional `scope` parameter narrows the result.

**Consequences.** Standard OAuth libraries work against the endpoint. The same code serves the npm
trusted-publishing entry point ([ADR-071](#adr-071--npm-trusted-publishing-through-package-bound-ci-tokens-p1)).

**Alternatives considered.** A custom endpoint and response shape: every client hand-written. 401
for rejected subject tokens: not what RFC 8693 clients expect
([Q-CI-8](spec/gitlab-ci-oidc.md#q-ci-8--status-code-for-rejected-subject-tokens)).

**Spec:** [R-CI-007](spec/gitlab-ci-oidc.md#r-ci-007--token-exchange-endpoint), [R-CI-008](spec/gitlab-ci-oidc.md#r-ci-008--exchange-errors-and-diagnostics), [R-CI-021](spec/gitlab-ci-oidc.md#r-ci-021--exchange-endpoint-hardening); open: [Q-CI-2](spec/gitlab-ci-oidc.md#q-ci-2--grammar-of-the-scope-request-parameter), [Q-CI-8](spec/gitlab-ci-oidc.md#q-ci-8--status-code-for-rejected-subject-tokens).

### ADR-060 · Ordered identity mappings: first match per tier, union across tiers

**Status:** Proposed

**Context.** Mappings turn verified claims into grants. Administrators need one predictable answer
per job, and narrow rules ("merge request pipelines get less than release pipelines") without
negations everywhere.

**Decision.** A mapping belongs to one CI issuer and has a priority, a claim condition, grants, an
optional maximum TTL and options. Within a tier, mappings are evaluated in ascending priority and the
first true condition is the tier's match; the token's grants are the union of the tier matches. The
MVP has one tier (administrator mappings); P1 adds one per delegation. No match fails the exchange.
A grant that fails to render fails the exchange; evaluation never falls through to the next
mapping.

**Consequences.** Every exchange is explained by at most one mapping per tier. Delegated mappings
cannot shadow system mappings or the reverse.

**Alternatives considered.** Union of all matching mappings: simpler to explain, but narrowing needs
negations. Strict first match across all mappings: delegations fight over priority numbers
([Q-CI-1](spec/gitlab-ci-oidc.md#q-ci-1--first-match-per-tier-or-union-of-all-matching-mappings)).

**Spec:** [R-CI-009](spec/gitlab-ci-oidc.md#r-ci-009--identity-mapping-model-and-evaluation-order), [R-CI-010](spec/gitlab-ci-oidc.md#r-ci-010--claim-conditions-matchers-and-combinators), [R-CI-011](spec/gitlab-ci-oidc.md#r-ci-011--protected-refs-environments-and-pipeline-sources), [R-CI-023](spec/gitlab-ci-oidc.md#r-ci-023--delegated-mapping-management); open: [Q-CI-1](spec/gitlab-ci-oidc.md#q-ci-1--first-match-per-tier-or-union-of-all-matching-mappings).

### ADR-061 · A small claim-template language with escaping

**Status:** Proposed

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

**Status:** Proposed

**Context.** A merge request pipeline from a fork can run in the parent project, with the fork's CI
configuration and the parent's runners. Its code can exfiltrate anything the token can read.

**Decision.** Before mapping evaluation, a subject token is cross-project when two of `project_id`,
`job_project_id` and `source_project_id` are present and differ. With the issuer's default `deny`,
such an exchange fails unless a matched mapping opts into `read_only`, which caps the grants to
`read`.

**Consequences.** Fork contributions cannot reach private packages through CI unless a group opts in
deliberately. GitLab requires a parent-project member to start such a pipeline, but that member may
not review the fork's CI configuration.

**Alternatives considered.** `read_only` by default: private packages readable by fork code
([Q-CI-9](spec/gitlab-ci-oidc.md#q-ci-9--default-for-cross-project-fork-pipelines)).

**Spec:** [R-CI-012](spec/gitlab-ci-oidc.md#r-ci-012--fork-and-merge-request-pipelines); open: [Q-CI-9](spec/gitlab-ci-oidc.md#q-ci-9--default-for-cross-project-fork-pipelines).

### ADR-063 · A CI token's lifetime is bounded by its job

**Status:** Proposed

**Context.** A CI token should not outlive the job that obtained it by more than necessary.

**Decision.** The lifetime is the minimum of the requested TTL (default 15 min), the mapping's and
the issuer's maxima, the installation's CI maximum (1 h proposed) and the remaining lifetime of the
ID token, whose expiry GitLab sets to the job timeout. One ceiling (6 h) governs every CI token, and
trove refuses to start with a larger configured value. Long jobs exchange again.

**Consequences.** A finished job's token stays valid for its remaining lifetime ([H-26](spec/gitlab-ci-oidc.md#h-26--ci-tokens-outlive-the-job));
revocation on job end is not specified ([Q-CI-7](spec/gitlab-ci-oidc.md#q-ci-7--revoke-ci-tokens-when-the-gitlab-job-ends)).

**Alternatives considered.** Revoking through GitLab job webhooks: needs a webhook secret per project
or group and an inbound endpoint, for a window of minutes.

**Spec:** [R-CI-016](spec/gitlab-ci-oidc.md#r-ci-016--ci-token-lifetime), [R-CI-017](spec/gitlab-ci-oidc.md#r-ci-017--ci-token-identity-presentation-and-revocation), [R-AUTH-026](spec/identity-and-authentication.md#r-auth-026--token-lifetime-policy); open: [Q-CI-7](spec/gitlab-ci-oidc.md#q-ci-7--revoke-ci-tokens-when-the-gitlab-job-ends).

### ADR-064 · Subject-token reuse is recorded; single use is opt-in

**Status:** Proposed

**Context.** Jobs legitimately exchange their ID token several times (per tool step, after network
errors, after the first CI token expired). Replays of a stolen ID token should still be visible.

**Decision.** Every exchanged subject token is recorded by issuer and `jti` with an exchange count,
in the exchange transaction. Single use is off by default for GitLab and GitHub issuers; beyond a
threshold, repeated exchanges create audit events. With single use on, a second exchange fails and
tokens without `jti` are rejected. Records are deleted after the subject token's expiry.

**Consequences.** Replays are counted and audited rather than blocked, relying on audience binding
and short ID-token lifetimes. Issuers whose audience is shared should turn single use on
([Q-CI-4](spec/gitlab-ci-oidc.md#q-ci-4--single-exchange-per-jti-by-default)).

**Alternatives considered.** Single use for every issuer: breaks jobs that exchange more than once.

**Spec:** [R-CI-018](spec/gitlab-ci-oidc.md#r-ci-018--replay-protection), [R-CI-020](spec/gitlab-ci-oidc.md#r-ci-020--issuance-records-audit-and-metrics); open: [Q-CI-4](spec/gitlab-ci-oidc.md#q-ci-4--single-exchange-per-jti-by-default).

### ADR-065 · Further CI issuers through the same mechanism (P1)

**Status:** Proposed

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

**Status:** Proposed

**Context.** An npm package document aggregates every version of a package. Storing it as a blob
means rewriting a growing document on every publish, tag change or deprecation.

**Decision.** A published version is a package row, a version row and one item, the tarball. npm
state (dist-tags, revision counter, per-version manifests) lives in npm tables, and package
documents are rendered from them and cached. Every change increments the package's `rev_seq`.

**Consequences.** Publishes and tag changes are small transactional writes. Document caches validate
against `rev_seq` ([ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early)). npm files live only at their canonical paths; there is no layout
setting.

**Alternatives considered.** Storing the document and patching it: write amplification and a race
between concurrent changes to one package.

**Spec:** [R-NPM-006](spec/formats/npm.md#r-npm-006--storage-model), [R-NPM-009](spec/formats/npm.md#r-npm-009--document-revisions-and-read-for-write), [R-NPM-010](spec/formats/npm.md#r-npm-010--full-package-document), [R-NPM-013](spec/formats/npm.md#r-npm-013--conditional-requests-and-cache-headers).

### ADR-067 · npm versions are immutable, guarded by tombstones

**Status:** Proposed

**Context.** Lockfiles pin `integrity` per version. A version republished with other bytes breaks
installs or, worse, installs different code.

**Decision.** A name and version once published to a local repository is never accepted again with
other bytes: not by a second publish, not after unpublish, not by a raw upload, not for a principal
holding `overwrite`. Unpublish writes a tombstone with the version's integrity, which survives the
trash can and its purge. Restoring the original bytes from the trash removes the tombstone.

**Consequences.** `overwrite` has no effect on npm repositories. A fix ships as a new patch version.
No purge operation is planned ([Q-NPM-8](spec/formats/npm.md#q-npm-8--purging-tombstones)).

**Alternatives considered.** Allowing reuse after unpublish: reopens the lockfile-integrity hazard.

**Spec:** [R-NPM-008](spec/formats/npm.md#r-npm-008--version-immutability-and-tombstones), [R-NPM-017](spec/formats/npm.md#r-npm-017--unpublish); open: [Q-NPM-8](spec/formats/npm.md#q-npm-8--purging-tombstones).

### ADR-068 · npm remotes fetch only the full document

**Status:** Proposed

**Context.** npm clients request either the abbreviated or the full package document. Release-age
and trust checks need fields (`time`, `_npmUser`) that only the full form carries.

**Decision.** A remote repository fetches and stores only the full document and derives the
abbreviated one. A received document must be valid JSON within the size limit, name the requested
package, have consistent version keys and absolute tarball URLs and valid SRI integrity values;
otherwise the previous entry stays and the request answers 502. Facts needed for tarball serving are
recorded per version, so tarball requests never parse a large document.

**Consequences.** One stored representation, one view of which versions exist. Cold-cache bandwidth
is higher for large documents, reduced by gzip and conditional requests.

**Alternatives considered.** Fetching both forms separately: two refresh cycles that can disagree
([Q-NPM-5](spec/formats/npm.md#q-npm-5--upstream-representation-for-remote-repositories)).

**Spec:** [R-NPM-024](spec/formats/npm.md#r-npm-024--package-document-fetch-and-validation), [R-NPM-025](spec/formats/npm.md#r-npm-025--tarball-urls-and-tarball-fetching); open: [Q-NPM-5](spec/formats/npm.md#q-npm-5--upstream-representation-for-remote-repositories).

### ADR-069 · Versions removed upstream are hidden by default

**Status:** Proposed

**Context.** Most upstream removals are takedowns of malicious packages. A cache that keeps serving
them becomes a distribution point.

**Decision.** When the upstream document no longer lists a version, an npm remote hides that version
and its tarball unless `serveRemovedVersions` is enabled. A whole document removed upstream answers
404 (generic rule of [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving)).

**Consequences.** Builds pinned to a legitimately unpublished version break visibly; the fix is the
per-repository switch or publishing the version to a local repository. Earlier downloads are not
recalled ([H-33](spec/formats/npm.md#h-33--takedown-delay-and-earlier-downloads)).

**Alternatives considered.** Serving everything once cached: maximum availability, also for malware
([Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed)).

**Spec:** [R-NPM-026](spec/formats/npm.md#r-npm-026--versions-removed-upstream), [R-REPO-029](spec/repositories-and-proxy.md#r-repo-029--upstream-failure-handling-and-the-assumed-offline-circuit); open: [Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed), [Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document).

### ADR-070 · Merged npm documents take each version from the member that would serve it

**Status:** Proposed

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

**Status:** Proposed

**Context.** Recent npm clients, run in GitLab CI with an ID token, exchange it for a publish token at
a fixed path below the registry's host root before `npm publish`.

**Decision.** trove serves that exchange at the host root as a second entry point into the CI
exchange of [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint), with the same validation and mappings. The issued CI token is bound to one
package: its scope carries a single grant on that exact name with `read` and `deploy`, and the name
is escaped like a claim value, so it cannot widen to a scope. Without `deploy` on the name in any
npm local repository, no token is issued.

**Consequences.** `npm publish` stays a one-liner in GitLab. The host-root route needs an amendment
to INV-6 and trove on its own host name ([Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6)). Trusted publishing widens the set of
credentials that can publish ([H-31](spec/formats/npm.md#h-31--trusted-publishing-widens-the-set-of-credentials-that-can-publish)).

**Alternatives considered.** No host-root route, exchanging through the generic endpoint in the job:
works, but turns a native client feature into pipeline boilerplate. One host per repository: DNS,
TLS and Ingress objects per repository.

**Spec:** [R-NPM-022](spec/formats/npm.md#r-npm-022--trusted-publishing-through-ci-oidc-exchange), [R-CI-007](spec/gitlab-ci-oidc.md#r-ci-007--token-exchange-endpoint); open: [Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6).

### ADR-072 · Real-client acceptance matrices gate each format

**Status:** Proposed

**Context.** Registry protocols are defined as much by client behavior as by documents. A format is
only done when the clients users run work against it.

**Decision.** A format is accepted when its page's acceptance criteria pass end to end with real
clients against two replicas behind the chart's Service and Ingress, with PostgreSQL and an
S3-compatible store. npm gates on npm 11, pnpm 10, Yarn 4 and Yarn 1.22 against a deterministic stub
upstream, plus a nightly read-only run against the public registry. NuGet gates on the .NET 10 SDK
on Linux, nuget.exe on Windows, nightly runs against nuget.org, and manual Visual Studio checks per
release. Every client version is pinned once in the harness, read by local runs and CI alike.

**Consequences.** Regressions in client compatibility block a release. Adding the current npm and
pnpm majors is open ([Q-NPM-4](spec/formats/npm.md#q-npm-4--current-client-majors-in-the-acceptance-gate)).

**Alternatives considered.** Protocol-level tests only: miss client quirks that real users hit.

**Spec:** [R-NPM-045](spec/formats/npm.md#r-npm-045--client-acceptance-matrix), [R-NUGET-027](spec/formats/nuget.md#r-nuget-027--acceptance-test-suite); open: [Q-NPM-4](spec/formats/npm.md#q-npm-4--current-client-majors-in-the-acceptance-gate).

## NuGet

### ADR-073 · NuGet v3 only, every resource below the feed root

**Status:** Proposed

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

**Status:** Proposed

**Context.** The lowercased ID appears in every URL, in duplicate detection and in permission
matching. Go's and .NET's Unicode case tables differ in corner cases.

**Decision.** Pushed IDs are restricted to ASCII (NuGet's ID rule limited to ASCII, at most 100
characters); upstream IDs pass through as given. The canonical name is the ASCII-lowercased ID. The
display name is the casing of the first stored version; each version keeps its own casing in its
catalog entry. Versions are kept in three forms: original, full normalized and the lowercased URL
form without build metadata.

**Consequences.** An ID cannot lowercase differently on client and server. Non-ASCII internal IDs
are not supported for now ([Q-NUGET-3](spec/formats/nuget.md#q-nuget-3--non-ascii-package-ids)).

**Alternatives considered.** NuGet's full Unicode ID rule with lowercasing that matches .NET
exactly: possible later with a dedicated conformance test.

**Spec:** [R-NUGET-003](spec/formats/nuget.md#r-nuget-003--package-ids-versions-and-normalization), [R-NUGET-004](spec/formats/nuget.md#r-nuget-004--semver-200-classification-and-visibility); open: [Q-NUGET-3](spec/formats/nuget.md#q-nuget-3--non-ascii-package-ids), [Q-NUGET-5](spec/formats/nuget.md#q-nuget-5--id-casing-on-later-pushes).

### ADR-075 · Strict archive and manifest parsing; the declared ID is what gets authorized

**Status:** Proposed

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

**Status:** Proposed

**Context.** Debug symbols must match the binaries of an immutable package version.

**Decision.** Symbol packages are pushed to the dedicated symbol publish resource as `.snupkg`. The
matching `.nupkg` must already exist in the target repository; entries are limited to the extensions
nuget.org accepts and every `.pdb` is checked by file signature. Re-pushing identical bytes succeeds
without change; different bytes answer 409. Legacy `.symbols.nupkg` packages are rejected. The symbol
server for local repositories and symbol resolution through remote and virtual repositories are P1.

**Consequences.** Symbols cannot drift from the binaries they describe.

**Alternatives considered.** Accepting legacy symbol packages: a second format to validate for
clients that have long moved on.

**Spec:** [R-NUGET-020](spec/formats/nuget.md#r-nuget-020--symbol-package-push), [R-NUGET-021](spec/formats/nuget.md#r-nuget-021--symbol-server-for-local-repositories), [R-NUGET-022](spec/formats/nuget.md#r-nuget-022--symbol-proxy-and-symbol-resolution-through-virtual-repositories).

## High availability and Helm

### ADR-077 · Singleton work under advisory-lock leases with fencing

**Status:** Proposed

**Context.** Garbage collection, retention runs, reindexing and similar work must not run twice at
once, and a replica that lost its lease must not keep writing.

**Decision.** Singleton work takes a session-level advisory lock on a connection from a dedicated
lease pool. Every state change the singleton makes runs on that same session: if the session dies,
the lock is gone and the writes fail with it. Actions outside PostgreSQL (S3 deletes) happen only
after the database change that authorizes them has committed on that session. The worker checks the
session periodically, cancels on loss, and the next scheduled run starts fresh from a persisted
cursor. Each replica keeps one coordination session for `LISTEN` and named leases; every advisory
lock uses the two-key form with a registered category, so lock diagnostics can name every lock.

**Consequences.** A partitioned replica cannot commit work it no longer owns. All singleton jobs
must be restartable. Transaction-mode poolers are excluded ([Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers)).

**Alternatives considered.** Table-based leases with heartbeats: no held connection, but no immediate
release on session end, which fencing builds on. Kubernetes leader election: no fencing of database
writes.

**Spec:** [R-ARCH-017](spec/architecture.md#r-arch-017--coordination-session-leases-and-advisory-lock-categories), [R-HA-005](spec/ha-and-helm.md#r-ha-005--leader-less-scheduling-and-job-leases), [R-HA-006](spec/ha-and-helm.md#r-ha-006--singleton-work-under-advisory-lock-leases-with-fencing); open: [Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills), [Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers).

### ADR-078 · Restores keep metadata and blobs consistent by ordering, not by coordination

**Status:** Proposed

**Context.** PostgreSQL and the bucket are backed up by different mechanisms. A point-in-time restore
of the database must never reference a blob whose object is gone.

**Decision.** Three orderings guarantee it: an object is written before any reference to it commits;
an object is deleted no earlier than the grace period after its last reference disappeared; and the
grace period is at least the declared recovery window, or the bucket keeps deleted objects as
noncurrent versions for that long. PostgreSQL is protected by WAL archiving and base backups in a
bucket other than the blob bucket; the blob bucket by versioning; the encryption keys outside the
cluster. The runtime S3 credential must not be able to delete object versions or change versioning,
lifecycle or bucket policy.

**Consequences.** A compromised replica can create delete markers but cannot destroy history.
Restoring the database revives revoked credentials and removed grants ([H-37](spec/ha-and-helm.md#h-37--a-database-restore-revives-revoked-credentials-and-removed-grants)); a write-once
revocation journal is proposed for P1 ([Q-HA-9](spec/ha-and-helm.md#q-ha-9--making-revocations-survive-a-database-restore-h-37)).

**Alternatives considered.** Coordinated snapshots of database and bucket: not offered by the
platforms the chart targets. An application-level export: deferred.

**Spec:** [R-HA-023](spec/ha-and-helm.md#r-ha-023--backup-of-postgresql-object-storage-and-keys), [R-HA-024](spec/ha-and-helm.md#r-ha-024--restore-consistency-between-metadata-and-blobs), [R-HA-025](spec/ha-and-helm.md#r-ha-025--restore-procedure), [R-STOR-024](spec/storage.md#r-stor-024--race-free-garbage-collection); open: [Q-HA-9](spec/ha-and-helm.md#q-ha-9--making-revocations-survive-a-database-restore-h-37).

### ADR-079 · The chart validates before it renders and never creates secrets

**Status:** Proposed

**Context.** A misconfigured installation should fail at `helm install`, not at first traffic, and
generated secrets disappear under `helm template` and GitOps tools.

**Decision.** The chart refuses to render, naming the offending value, when required settings are
missing (base URL, database, bucket, key Secret), when HA is enforced with fewer than two replicas or
without a disruption budget, when a role split has no workers, when the maximum upload exceeds the
spool budget, or when the termination grace period is shorter than the drain delay plus 15 s. Every secret
comes from an existing Kubernetes Secret named in the values.

**Consequences.** First installs need the operator to create Secrets up front, documented with
commands. No secret is ever rotated by an upgrade by accident.

**Alternatives considered.** Validation at start-up only: the error surfaces in a crash loop instead
of the install command.

**Spec:** [R-HA-027](spec/ha-and-helm.md#r-ha-027--encryption-keys-for-sealed-secrets-in-a-replicated-deployment), [R-HA-029](spec/ha-and-helm.md#r-ha-029--fail-fast-chart-validation), [R-HA-030](spec/ha-and-helm.md#r-ha-030--configuration-rendering-and-secret-wiring); open: [Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key).

## Operations and observability

### ADR-080 · Audit records commit with the change they record

**Status:** Proposed

**Context.** An audit trail that can miss changes is not an audit trail; one that blocks requests
under a flood of denials is an outage.

**Decision.** Events about state changes (configuration, identities, tokens, permissions, content
writes) are inserted in the same transaction as the change; if the insert fails, the change rolls
back. Access events (logins, denials, rejected tokens, failed exchanges) go through a bounded
in-process buffer written in batches; when it is full, the event still goes to the log stream and a
drop counter increases. Denials and rejected tokens are deduplicated per window; anonymous denials and
successful downloads are recorded only when enabled. After commit, each event is also written to the
log stream; the database copy is authoritative.

**Consequences.** No change exists without its record. Allowed downloads are not attributable by
default ([H-23](spec/authorization.md#h-23--allowed-downloads-are-not-attributable-by-default), [Q-AUTHZ-5](spec/authorization.md#q-authz-5--default-audit-level-for-reads)). Records in PostgreSQL are not tamper-evident ([H-39](spec/operations-and-observability.md#h-39--audit-records-are-not-tamper-evident),
[Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records)).

**Alternatives considered.** Writing every event asynchronously: changes without records after a
crash. Writing every event synchronously: a denial flood blocks request handling.

**Spec:** [R-OPS-015](spec/operations-and-observability.md#r-ops-015--audit-recording), [R-OPS-016](spec/operations-and-observability.md#r-ops-016--audit-event-catalog), [R-OPS-017](spec/operations-and-observability.md#r-ops-017--audit-query-export-and-retention), [R-AUTHZ-029](spec/authorization.md#r-authz-029--audit-of-authorization-decisions); open: [Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records),
[Q-AUTHZ-5](spec/authorization.md#q-authz-5--default-audit-level-for-reads).

### ADR-081 · Configuration as code: documents own their objects; plan before apply (P1)

**Status:** Proposed

**Context.** Platform teams manage repositories, groups, policies and CI mappings in Git and expect a
reviewable plan before anything changes.

**Decision.** A configuration document declares database-held configuration and owns every object
it declares; with pruning on, owned objects no longer declared are deleted. A diff returns a plan
with a hash; apply can require that hash, refuses conflicts (objects owned by nobody need explicit
adoption) and blocks destructive changes without an explicit flag. At start-up, replicas can apply
documents under an advisory lock; destructive changes are never applied at start-up, and a failing
document keeps the replica unready so a rollout stalls while old replicas serve. Export turns an
existing installation into a document. Managed objects are proposed to be locked against API and UI
edits ([Q-OPS-8](spec/operations-and-observability.md#q-ops-8--locking-objects-managed-by-configuration-documents)).

**Consequences.** One reviewable path for configuration changes; a broken document cannot take over a
running installation.

**Alternatives considered.** Imperative scripts against the API: no plan, no drift detection.
Kubernetes custom resources: configuration bound to cluster permissions.

**Spec:** [R-OPS-029](spec/operations-and-observability.md#r-ops-029--configuration-as-code-document), [R-OPS-030](spec/operations-and-observability.md#r-ops-030--diff-and-apply), [R-OPS-031](spec/operations-and-observability.md#r-ops-031--startup-bootstrap-from-configuration-documents), [R-OPS-032](spec/operations-and-observability.md#r-ops-032--configuration-export), [R-API-044](spec/api-and-ui.md#r-api-044--configuration-as-code); open:
[Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents), [Q-OPS-8](spec/operations-and-observability.md#q-ops-8--locking-objects-managed-by-configuration-documents).

### ADR-082 · Events through a transactional outbox, as CloudEvents, signed per Standard Webhooks (P1)

**Status:** Proposed

**Context.** Webhook receivers need every committed change exactly as it happened and nothing from
rolled-back transactions, in a format they can parse with existing libraries.

**Decision.** A webhook-capable change inserts an outbox row in its own transaction; without an
enabled subscription for the domain no row is written. Dispatch claims rows with `SKIP LOCKED` and
delivers through the job queue with retries. The body is a CloudEvents 1.0 event in structured JSON
mode; requests are signed following the Standard Webhooks specification. Redirects are not followed,
and target addresses pass the outbound address checks, with private ranges denied by default.

**Consequences.** Events cost nothing until someone subscribes. A new subscription receives events
committed after it was enabled. The CloudEvents `type` prefix becomes a public contract at the first
release ([Q-OPS-5](spec/operations-and-observability.md#q-ops-5--cloudevents-type-prefix)).

**Alternatives considered.** Publishing after commit from request code: events lost on a crash
between commit and send. A custom envelope: every receiver hand-written.

**Spec:** [R-OPS-035](spec/operations-and-observability.md#r-ops-035--event-outbox-and-dispatch), [R-OPS-036](spec/operations-and-observability.md#r-ops-036--event-envelope), [R-OPS-040](spec/operations-and-observability.md#r-ops-040--request-signing), [R-OPS-042](spec/operations-and-observability.md#r-ops-042--delivery-retries-and-outbound-protection); open: [Q-OPS-5](spec/operations-and-observability.md#q-ops-5--cloudevents-type-prefix).

## Management API and UI

### ADR-083 · The OpenAPI 3.1 document is the API contract

**Status:** Proposed

**Context.** The UI, scripts and configuration tools all use the management API. Drift between
documentation, server and clients breaks them silently.

**Decision.** One OpenAPI 3.1 document describes every `/api/v1` operation and the raw file path
`/files/…`; package protocols follow their ecosystems' specifications. It is served without
authentication. Every operation declares a stable `operationId`, its authorization and its errors. CI
fails on breaking changes within v1.

**Consequences.** The contract can be reviewed before code exists. The generation approach is open;
the recommendation is spec-first with generated Go server interfaces and generated TypeScript types
([Q-API-1](spec/api-and-ui.md#q-api-1--openapi-generation-approach)).

**Alternatives considered.** Code-first generation: inverts the source of truth and ties the API to a
framework.

**Spec:** [R-API-001](spec/api-and-ui.md#r-api-001--base-path-versions-and-compatibility), [R-API-002](spec/api-and-ui.md#r-api-002--openapi-31-document-as-the-contract), [R-ARCH-025](spec/architecture.md#r-arch-025--versioning-and-compatibility-policy); open: [Q-API-1](spec/api-and-ui.md#q-api-1--openapi-generation-approach).

### ADR-084 · Uniform API conventions: problem details, cursors, merge patch, ETags, idempotency keys

**Status:** Proposed

**Context.** Consistent conventions make every resource predictable for scripts and generated
clients.

**Decision.** Errors under `/api/v1`, `/auth` and `/files` are RFC 9457 problem details with a stable
code whose description is served by every installation. Lists use keyset pagination with `limit`
(default 50, maximum 500) and `cursor`, plus a `Link` header. PATCH bodies are JSON Merge Patch
(RFC 7396); JSON Patch is not offered. Mutable resources carry a revision as strong ETag; `If-Match`
is optional unless the owning page makes it mandatory (permission policies do). Creating POSTs accept
an `Idempotency-Key`. Package protocol endpoints keep their ecosystems' error formats.

**Consequences.** One client library pattern for every resource; the UI is safe against double
submits and lost updates. Some pages still use other parameter names or action syntax and must be
aligned ([Q-API-6](spec/api-and-ui.md#q-api-6--pagination-parameter-names), [Q-API-7](spec/api-and-ui.md#q-api-7--syntax-of-custom-methods)).

**Alternatives considered.** Offset pagination: unstable under concurrent writes. Mandatory
`If-Match`: breaks deliberate blind updates by scripts ([Q-API-2](spec/api-and-ui.md#q-api-2--is-if-match-mandatory-on-writes)).

**Spec:** [R-API-009](spec/api-and-ui.md#r-api-009--errors-as-problem-details), [R-API-010](spec/api-and-ui.md#r-api-010--cursor-pagination), [R-API-012](spec/api-and-ui.md#r-api-012--optimistic-concurrency-with-etag-and-if-match), [R-API-013](spec/api-and-ui.md#r-api-013--partial-updates-with-json-merge-patch), [R-API-014](spec/api-and-ui.md#r-api-014--idempotency-keys-for-creates); open:
[Q-API-2](spec/api-and-ui.md#q-api-2--is-if-match-mandatory-on-writes), [Q-API-6](spec/api-and-ui.md#q-api-6--pagination-parameter-names), [Q-API-7](spec/api-and-ui.md#q-api-7--syntax-of-custom-methods).

### ADR-085 · Long-running work answers 202 with a job

**Status:** Proposed

**Context.** Deleting a large repository, reindexing or applying configuration outlives an HTTP
request and a replica restart.

**Decision.** Such work answers 202 with the job's location and representation. Jobs survive replica
restarts, are visible from every replica and can be cancelled. Owning pages may add a domain view of
their runs that references the job.

**Consequences.** Clients poll one job API for every kind of long-running work.

**Alternatives considered.** Synchronous calls with long timeouts: break at proxies and on replica
restarts.

**Spec:** [R-API-015](spec/api-and-ui.md#r-api-015--long-running-work-as-jobs), [R-ARCH-016](spec/architecture.md#r-arch-016--background-job-framework-on-postgresql), [R-OPS-019](spec/operations-and-observability.md#r-ops-019--scheduled-maintenance-tasks-and-job-visibility).

### ADR-086 · UI baseline: Angular 22 defaults, a strict CSP, nothing from foreign origins

**Status:** Proposed

**Context.** The UI shares its origin with package content ([ADR-030](#adr-030--one-origin-for-ui-api-and-package-content)) and must work air-gapped.

**Decision.** Standalone components only, zoneless change detection, `OnPush`, strict TypeScript and
templates. Signals are the state primitive; server state is read with `httpResource()`, with no global
cache of server data. UI documents carry a CSP with a per-response nonce and Trusted Types, so every
`bypassSecurityTrust*` call fails. The UI requests nothing from other origins: fonts and icons are
self-hosted, no analytics, no CDN. Initial JavaScript is budgeted at 400 kB compressed (proposed);
every feature is a lazy route.

**Consequences.** Script injection through Angular's escape hatches is closed by the browser. The
component library and the shell layout are open ([Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library), [Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested)).

**Alternatives considered.** A looser CSP for third-party widgets: widens the attack surface on the
shared origin.

**Spec:** [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery), [R-UI-002](spec/api-and-ui.md#r-ui-002--application-shell), [R-UI-046](spec/api-and-ui.md#r-ui-046--state-management-with-signals); open: [Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library), [Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested).

### ADR-087 · READMEs rendered on the server through an allowlist sanitizer

**Status:** Proposed

**Context.** Package READMEs are author-controlled Markdown with embedded HTML, displayed on the UI's
origin.

**Decision.** The server renders CommonMark with common extensions and sanitizes the result against a
fixed allowlist of elements and attributes. Links are limited to `http`, `https`, `mailto` and
fragments; `id` and `name` are kept only with a `user-content-` prefix; images pointing at trove's
own origin are dropped, so a README cannot make the viewer's browser send authenticated requests.
External images are blocked by default and replaced by a link.

**Consequences.** One renderer and one policy for every client. Air-gapped installations lose
nothing; others can allow external images as an operator option ([Q-UI-6](spec/api-and-ui.md#q-ui-6--external-images-in-readmes)).

**Alternatives considered.** Rendering in the browser: a sanitizer per client. Proxying external
images through trove: a server-side request forgery surface controlled by package authors.

**Spec:** [R-UI-008](spec/api-and-ui.md#r-ui-008--sanitized-readme-rendering), [R-API-029](spec/api-and-ui.md#r-api-029--packages-versions-and-readme); open: [Q-UI-6](spec/api-and-ui.md#q-ui-6--external-images-in-readmes).

### ADR-088 · English and German in the MVP

**Status:** Proposed

**Context.** The owner's users read English and German.

**Decision.** English (source and default) and German ship with the MVP. Every UI text is
translatable, sentences are never concatenated from fragments, and counts use ICU plural and select
messages. Locale choice: user preference, then `Accept-Language`, then the configured default. API
texts stay English; the UI translates problem codes.

**Consequences.** Every UI change carries its German text. The mechanism is open; the
recommendation is Angular's compile-time i18n with one build per locale under `/ui/<locale>/`
([Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism)).

**Alternatives considered.** English only: excludes part of the intended audience.

**Spec:** [R-UI-045](spec/api-and-ui.md#r-ui-045--internationalization), [R-UI-001](spec/api-and-ui.md#r-ui-001--technology-baseline-and-delivery); open: [Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism).

## Open decisions

Every open question on the spec pages, with the option its page recommends. The reasoning stays on
the page; follow the link. The **ADR** column names the entry the answer changes or confirms.

**Questions that appear on several pages.** Readiness on object-storage failure: [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12),
[Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness) and [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure) (storage.md and ha-and-helm.md recommend gating readiness on PostgreSQL
only; operations-and-observability.md recommends a configurable switch and puts the same change to
the owner). Owner of build records: [Q-AUTHZ-8](spec/authorization.md#q-authz-8--spec-page-for-build-metadata), [Q-API-5](spec/api-and-ui.md#q-api-5--owning-page-for-build-information) and [Q-STOR-3](spec/storage.md#q-stor-3--owner-of-build-records) (all recommend a new
page `builds.md`). Cron syntax: [Q-OPS-7](spec/operations-and-observability.md#q-ops-7--one-cron-syntax-for-the-whole-system) and [Q-STOR-4](spec/storage.md#q-stor-4--cron-dialect-shared-by-all-schedules) (both recommend five-field cron in UTC).
Where the human-login OIDC client is configured: [Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives) and [Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents) (both recommend the
configuration file).

### Architecture

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-ARCH-1](spec/architecture.md#q-arch-1--http-router) | Which HTTP router? | (a) `net/http.ServeMux` with format-owned path matching | [ADR-029](#adr-029--formats-are-compiled-in-plug-ins-behind-a-narrow-host-interface) |
| [Q-ARCH-2](spec/architecture.md#q-arch-2--postgresql-driver-and-query-layer) | Which PostgreSQL driver and query layer? | (a) pgx v5 with sqlc-generated queries | [ADR-014](#adr-014--postgresql-is-the-only-stateful-dependency-besides-object-storage-inv-3) |
| [Q-ARCH-3](spec/architecture.md#q-arch-3--migration-runner) | Which migration runner? | (a) goose v3 Provider API, under trove's own lock and compatibility gate | [ADR-033](#adr-033--versioning-promises-per-surface-and-a-schema-compatibility-gate) |
| [Q-ARCH-4](spec/architecture.md#q-arch-4--s3-client-library) | Which S3 client library? | (a) aws-sdk-go-v2 `service/s3` with a trove-owned multipart upload | [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) |
| [Q-ARCH-5](spec/architecture.md#q-arch-5--job-queue-implementation) | Own job queue or an external queue library? | (a) the queue of R-ARCH-016 | [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work) |
| [Q-ARCH-6](spec/architecture.md#q-arch-6--cross-replica-collapse-holds-a-connection-for-the-whole-upstream-transfer) | Keep the cross-replica fetch lock although it holds a connection for the whole transfer? | (a) keep it with a bounded pool and bypass; revisit when OCI remotes exist | [ADR-022](#adr-022--pull-through-proxy-semantics-inv-11) |
| [Q-ARCH-7](spec/architecture.md#q-arch-7--lock-key-form-for-cache-fills) | Lock key form for cache fills? | (a) two-key form with category `fetch` | [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing) |
| [Q-ARCH-8](spec/architecture.md#q-arch-8--notify-on-hot-path-commits) | How to NOTIFY on hot-path commits? | (a) post-commit coalescer, at most one NOTIFY per channel per 50 ms per replica | [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early) |
| [Q-ARCH-9](spec/architecture.md#q-arch-9--freshness-of-merged-metadata-for-local-members) | Validate local members' revision on every merged-metadata hit? | (a) yes, in every format | [ADR-031](#adr-031--caches-validate-themselves-notifications-only-evict-early) |

### Repositories and pull-through proxy

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-REPO-1](spec/repositories-and-proxy.md#q-repo-1--who-specifies-the-stored-secret-mechanism-iam-045) | Which page specifies the stored-secret mechanism? | (b) architecture.md, for every stored secret | [ADR-032](#adr-032--stored-secrets-are-sealed-under-a-key-ring-the-operator-supplies) |
| [Q-REPO-2](spec/repositories-and-proxy.md#q-repo-2--lower-bound-of-the-metadata-period) | Lower bound of the metadata period? | (a) allow `0` | [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving) |
| [Q-REPO-3](spec/repositories-and-proxy.md#q-repo-3--generic-default-when-the-upstream-removes-a-cached-document) | Generic default when the upstream removes a cached document? | (a) mark it gone, answer 404 for the document, keep serving cached artifacts | [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving) |
| [Q-REPO-4](spec/repositories-and-proxy.md#q-repo-4--scope-of-remote-refusal-for-claimed-names) | Scope of remote refusal for claimed names? | (a) system-wide, for `global` claims only | [ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules) |
| [Q-REPO-5](spec/repositories-and-proxy.md#q-repo-5--generic-or-format-level-placement-of-pins-extra-hosts-and-upstream-authentication) | Generic or format-level settings for pins, extra hosts and upstream authentication? | (a) generic members; align the npm page | [ADR-036](#adr-036--dependency-confusion-protection-through-namespace-claims-and-pin-rules) |

### Storage

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-STOR-1](spec/storage.md#q-stor-1--readiness-tied-to-object-storage-reachability-inv-12) | Should object-storage reachability gate readiness? | (b) readiness on PostgreSQL and process health only | [ADR-023](#adr-023--high-availability-through-postgresql-coordination-inv-12), [ADR-043](#adr-043--object-store-outage-no-local-queue-and-201-means-durable) |
| [Q-STOR-2](spec/storage.md#q-stor-2--sha-512-for-every-blob-or-only-on-demand) | SHA-512 for every blob or only on demand? | (a) every blob | [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) |
| [Q-STOR-3](spec/storage.md#q-stor-3--owner-of-build-records) | Which page owns build records? | (a) a new page `builds.md` | — |
| [Q-STOR-4](spec/storage.md#q-stor-4--cron-dialect-shared-by-all-schedules) | Which cron dialect for all schedules? | (a) five-field cron in UTC with `@hourly`, `@daily`, `@weekly` | [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) |
| [Q-STOR-5](spec/storage.md#q-stor-5--does-overwriting-a-file-send-the-previous-content-to-the-trash-can) | Does an overwrite send the previous content to the trash can? | (a) yes, for user-deployed files in local repositories | [ADR-044](#adr-044--a-trash-can-that-also-covers-overwrites-p1) |
| [Q-STOR-6](spec/storage.md#q-stor-6--download-verification-on-by-default) | Download verification on by default? | (a) on | — |
| [Q-STOR-7](spec/storage.md#q-stor-7--status-code-for-an-exceeded-quota) | Status code for an exceeded quota? | (a) 507 for quotas, 413 only for the per-request size limit | — |
| [Q-STOR-8](spec/storage.md#q-stor-8--copy-and-move-target-semantics) | Copy and move target semantics? | (a) the target path is the exact destination; existing folders are merged | — |
| [Q-STOR-9](spec/storage.md#q-stor-9--one-pattern-syntax-across-retention-and-permissions) | One pattern syntax for retention and permissions? | (a) one matcher, owned by authorization.md | [ADR-037](#adr-037--one-name-pattern-syntax-most-specific-wins-applied-to-every-protocol-request) |

### Identity and authentication

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-AUTH-1](spec/identity-and-authentication.md#q-auth-1--where-login-provider-configuration-lives) | Where does login provider configuration live? | (a) configuration file only | [ADR-026](#adr-026--one-configuration-file-plus-database-held-configuration-inv-15) |
| [Q-AUTH-2](spec/identity-and-authentication.md#q-auth-2--session-cookie-name) | Should the session cookie use the `__Host-` prefix? | (b) amend INV-8 to `__Host-trv_session` (and the CSRF cookie) | [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8), [ADR-046](#adr-046--server-side-ui-sessions-behind-an-opaque-cookie) |
| [Q-AUTH-3](spec/identity-and-authentication.md#q-auth-3--refresh-tokens-or-rotation) | Refresh tokens or rotation only? | (a) rotation only | [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8) |
| [Q-AUTH-4](spec/identity-and-authentication.md#q-auth-4--missing-group-claim) | What happens when the group claim is missing at login? | (a) clear the synced groups, with a warning event | [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7) |
| [Q-AUTH-5](spec/identity-and-authentication.md#q-auth-5--dormancy-default) | Dormant-user deactivation on by default? | (a) on, after 90 days | [ADR-018](#adr-018--humans-authenticate-only-through-an-external-oidc-provider-inv-7) |
| [Q-AUTH-6](spec/identity-and-authentication.md#q-auth-6--unauthenticated-revoke-by-value) | Accept unauthenticated revocation by token value? | (a) yes, rate-limited | [ADR-048](#adr-048--tokens-hashed-with-sha-256-a-positive-only-cache-bounded-revocation-latency) |
| [Q-AUTH-7](spec/identity-and-authentication.md#q-auth-7--administrators-issuing-tokens-for-users) | May administrators create tokens owned by other users? | (a) no; automation uses service accounts | [ADR-019](#adr-019--trove-issues-opaque-prefixed-tokens-inv-8) |

### Authorization

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-AUTHZ-1](spec/authorization.md#q-authz-1--endpoint-gate-on-virtual-repositories) | Does using a virtual repository require `read` on it? | (a) explicit gate plus member rights | [ADR-053](#adr-053--virtual-repositories-endpoint-gate-plus-member-rights) |
| [Q-AUTHZ-2](spec/authorization.md#q-authz-2--ownership-enforcement-default-for-new-npm-and-nuget-local-repositories) | Ownership enforcement default for new npm and NuGet local repositories? | (a) `enforce` | [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) |
| [Q-AUTHZ-3](spec/authorization.md#q-authz-3--built-in-principal-for-all-authenticated-users) | A built-in principal for all authenticated users? | (b) `authenticated`, covering users and service accounts but not CI tokens; P1 | [ADR-020](#adr-020--deny-by-default-allow-only-permission-policies-inv-9) |
| [Q-AUTHZ-4](spec/authorization.md#q-authz-4--default-populate-action-of-remote-repositories) | Default cache-fill action of remote repositories? | (a) `read`; refreshing expired metadata counts as populating | [ADR-054](#adr-054--cache-fills-are-guarded-by-a-configurable-action-read-by-default) |
| [Q-AUTHZ-5](spec/authorization.md#q-authz-5--default-audit-level-for-reads) | Default audit level for reads? | (b) `denied` | [ADR-080](#adr-080--audit-records-commit-with-the-change-they-record) |
| [Q-AUTHZ-6](spec/authorization.md#q-authz-6--default-ownership-of-ci-inline-grants-that-write) | Default ownership mode of CI inline grants that write? | (b) `bind_project` | [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) |
| [Q-AUTHZ-7](spec/authorization.md#q-authz-7--ci-delegations-and-p1-projects) | How do CI delegations relate to P1 projects? | (a) independent in P1; (b) project-bound once projects gain roles in P2 | [ADR-058](#adr-058--projects-for-delegated-administration-reduced-in-p1) |
| [Q-AUTHZ-8](spec/authorization.md#q-authz-8--spec-page-for-build-metadata) | Which page owns build metadata? | (a) a new page `builds.md` | — |
| [Q-AUTHZ-9](spec/authorization.md#q-authz-9--shape-of-the-repository-part-of-a-token-scope) | Shape of the repository part of a token scope? | (a) the policy resource set: repositories, packages, paths, actions | [ADR-051](#adr-051--actions-are-explicit-and-a-token-scope-only-narrows) |

### GitLab CI OIDC

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-CI-1](spec/gitlab-ci-oidc.md#q-ci-1--first-match-per-tier-or-union-of-all-matching-mappings) | First match per tier, or union of all matching mappings? | (a) first match within a tier, union across tiers | [ADR-060](#adr-060--ordered-identity-mappings-first-match-per-tier-union-across-tiers) |
| [Q-CI-2](spec/gitlab-ci-oidc.md#q-ci-2--grammar-of-the-scope-request-parameter) | Grammar of the exchange's `scope` parameter? | (b) the token scope grammar of the identity page, with the interim grammar as its subset | [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint) |
| [Q-CI-3](spec/gitlab-ci-oidc.md#q-ci-3--default-for-id-tokens-with-several-audiences) | Accept ID tokens with several audiences? | (a) reject | [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) |
| [Q-CI-4](spec/gitlab-ci-oidc.md#q-ci-4--single-exchange-per-jti-by-default) | Single exchange per `jti` by default? | (a) off for GitLab and GitHub issuers, tracked and counted; on for generic issuers with shared audiences | [ADR-064](#adr-064--subject-token-reuse-is-recorded-single-use-is-opt-in) |
| [Q-CI-5](spec/gitlab-ci-oidc.md#q-ci-5--build-the-ci_job_token-fallback-at-all) | Build the `CI_JOB_TOKEN` fallback at all? | (b) move it to P2 and build it only on a concrete need | [ADR-021](#adr-021--gitlab-ci-identity-through-id-token-exchange-inv-10) |
| [Q-CI-6](spec/gitlab-ci-oidc.md#q-ci-6--how-a-ci-project-acquires-package-ownership) | How does a CI project acquire package ownership? | (a) claim on first publish within the grant pattern | [ADR-056](#adr-056--package-ownership-is-decided-by-the-first-publish) |
| [Q-CI-7](spec/gitlab-ci-oidc.md#q-ci-7--revoke-ci-tokens-when-the-gitlab-job-ends) | Revoke CI tokens when the GitLab job ends? | (a) rely on the TTL bound for the MVP; reconsider job webhooks in P2 | [ADR-063](#adr-063--a-ci-tokens-lifetime-is-bounded-by-its-job) |
| [Q-CI-8](spec/gitlab-ci-oidc.md#q-ci-8--status-code-for-rejected-subject-tokens) | Status code for rejected subject tokens? | (a) 400 `invalid_request` with a `reason` | [ADR-059](#adr-059--ci-tokens-through-a-standard-token-exchange-endpoint) |
| [Q-CI-9](spec/gitlab-ci-oidc.md#q-ci-9--default-for-cross-project-fork-pipelines) | Default for cross-project (fork) pipelines? | (a) `deny` | [ADR-062](#adr-062--fork-pipelines-are-denied-by-default) |

### npm

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-NPM-1](spec/formats/npm.md#q-npm-1--host-root-routes-next-to-inv-6) | Add host-root routes next to INV-6? | A: two fixed host-root prefixes for the trusted-publishing exchange and attestations | [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6), [ADR-071](#adr-071--npm-trusted-publishing-through-package-bound-ci-tokens-p1) |
| [Q-NPM-2](spec/formats/npm.md#q-npm-2--serve-versions-the-upstream-removed) | Serve versions the upstream removed? | A: no, by default | [ADR-069](#adr-069--versions-removed-upstream-are-hidden-by-default) |
| [Q-NPM-3](spec/formats/npm.md#q-npm-3--audit-forwarding-on-by-default) | Forward `npm audit` to the upstream by default? | A: forward, with internal names removed | — |
| [Q-NPM-4](spec/formats/npm.md#q-npm-4--current-client-majors-in-the-acceptance-gate) | Gate on the current npm and pnpm majors? | A: gate on npm 11 and 12, pnpm 10 and 12, Yarn 4, Yarn 1.22 | [ADR-072](#adr-072--real-client-acceptance-matrices-gate-each-format) |
| [Q-NPM-5](spec/formats/npm.md#q-npm-5--upstream-representation-for-remote-repositories) | Which upstream representation do remotes fetch? | A: the full document only; derive the abbreviated one | [ADR-068](#adr-068--npm-remotes-fetch-only-the-full-document) |
| [Q-NPM-6](spec/formats/npm.md#q-npm-6--manifest-consistency-verify-or-derive) | Verify the publish manifest against the tarball, or derive it? | A: verify and reject on mismatch | — |
| [Q-NPM-7](spec/formats/npm.md#q-npm-7--git-dependencies-in-the-external-dependency-rewrite) | Rewrite git dependencies in the external dependency rewrite? | A: HTTP(S) tarball URLs only in P1 | — |
| [Q-NPM-8](spec/formats/npm.md#q-npm-8--purging-tombstones) | Offer a tombstone purge? | A: no purge in MVP and P1 | [ADR-067](#adr-067--npm-versions-are-immutable-guarded-by-tombstones) |
| [Q-NPM-9](spec/formats/npm.md#q-npm-9--annotate-or-deploy-for-dist-tags-and-deprecation) | `annotate` or `deploy` for dist-tags and deprecation? | Resolved on its page: B, `deploy` | [ADR-052](#adr-052--changing-what-clients-install-needs-deploy-not-annotate) |

### NuGet

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-NUGET-1](spec/formats/nuget.md#q-nuget-1--default-meaning-of-a-nuget-delete) | Default meaning of a NuGet delete? | (a) unlist; hard delete as a setting or through management deletes | — |
| [Q-NUGET-2](spec/formats/nuget.md#q-nuget-2--metadata-cache-period-of-nuget-remotes) | Metadata cache period of NuGet remotes? | (b) 300 s as the NuGet format default | [ADR-038](#adr-038--upstream-failures-classified-outcomes-a-shared-circuit-bounded-stale-serving) |
| [Q-NUGET-3](spec/formats/nuget.md#q-nuget-3--non-ascii-package-ids) | Allow non-ASCII package IDs? | (a) ASCII-only for pushes; upstream IDs pass through | [ADR-074](#adr-074--nuget-package-ids-ascii-on-push-case-insensitive-first-casing-displayed) |
| [Q-NUGET-4](spec/formats/nuget.md#q-nuget-4--forwarding-search-to-the-upstream) | Forward search to the upstream? | (a) forward by default, switchable per remote | — |
| [Q-NUGET-5](spec/formats/nuget.md#q-nuget-5--id-casing-on-later-pushes) | ID casing on later pushes? | (a) accept any casing; the first version's casing is the display name | [ADR-074](#adr-074--nuget-package-ids-ascii-on-push-case-insensitive-first-casing-displayed) |
| [Q-NUGET-6](spec/formats/nuget.md#q-nuget-6--overwriting-an-existing-version) | May an existing version be overwritten? | (a) no; a repeated push answers 409 | [ADR-052](#adr-052--changing-what-clients-install-needs-deploy-not-annotate) |
| [Q-NUGET-7](spec/formats/nuget.md#q-nuget-7--patterns-and-repository-level-upstream-documents) | Do patterns apply to repository-level upstream documents? | (a) no; the format marks them as repository-level | [ADR-037](#adr-037--one-name-pattern-syntax-most-specific-wins-applied-to-every-protocol-request) |

### High availability and Helm

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-HA-1](spec/ha-and-helm.md#q-ha-1--readiness-on-object-storage-failure) | Readiness on object-storage failure? | (b) process and PostgreSQL only; revise INV-12's wording | [ADR-023](#adr-023--high-availability-through-postgresql-coordination-inv-12) |
| [Q-HA-2](spec/ha-and-helm.md#q-ha-2--postgresql-version-floor) | PostgreSQL version floor? | (b) 16 | [ADR-014](#adr-014--postgresql-is-the-only-stateful-dependency-besides-object-storage-inv-3) |
| [Q-HA-3](spec/ha-and-helm.md#q-ha-3--s3-component-of-the-local-profile-after-minios-archival) | Which S3 store for the local profile after MinIO's archival? | (b) SeaweedFS if the conformance check passes; MinIO stays in the nightly matrix | [ADR-015](#adr-015--content-addressed-blob-store-on-s3-inv-4) |
| [Q-HA-4](spec/ha-and-helm.md#q-ha-4--reserve-v2-on-the-client-listener-for-oci) | Reserve `/v2/` on the client listener for OCI? | (a) amend INV-6 | [ADR-017](#adr-017--url-layout-on-two-listeners-inv-6) |
| [Q-HA-5](spec/ha-and-helm.md#q-ha-5--zero-downtime-upgrades-that-skip-minor-releases) | Zero-downtime upgrades that skip minor releases? | (a) adjacent minors only; skips go stepwise | [ADR-033](#adr-033--versioning-promises-per-surface-and-a-schema-compatibility-gate) |
| [Q-HA-6](spec/ha-and-helm.md#q-ha-6--transaction-mode-connection-poolers) | Support transaction-mode connection poolers? | (a) not in the MVP; (b) a separate direct host for session work as P2 | [ADR-077](#adr-077--singleton-work-under-advisory-lock-leases-with-fencing) |
| [Q-HA-7](spec/ha-and-helm.md#q-ha-7--priority-of-gateway-api) | Priority of Gateway API? | (b) HTTPRoute in the MVP, chart-created Gateways P1 | [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) |
| [Q-HA-8](spec/ha-and-helm.md#q-ha-8--who-creates-the-first-encryption-key) | Who creates the first encryption key? | (b) the operator; the chart fails without it | [ADR-032](#adr-032--stored-secrets-are-sealed-under-a-key-ring-the-operator-supplies), [ADR-079](#adr-079--the-chart-validates-before-it-renders-and-never-creates-secrets) |
| [Q-HA-9](spec/ha-and-helm.md#q-ha-9--making-revocations-survive-a-database-restore-h-37) | Make revocations survive a database restore? | (b) a write-once journal in object storage, P1 | [ADR-078](#adr-078--restores-keep-metadata-and-blobs-consistent-by-ordering-not-by-coordination) |
| [Q-HA-10](spec/ha-and-helm.md#q-ha-10--default-of-the-egress-networkpolicy) | Default of the egress NetworkPolicy? | (a) off | [ADR-039](#adr-039--egress-address-checks-after-dns-resolution-for-every-remote-connection) |
| [Q-HA-11](spec/ha-and-helm.md#q-ha-11--publishing-location-of-images-and-chart) | Where are images and the chart published? | The registry of the forge that hosts the repository, decided with the forge | [ADR-024](#adr-024--the-helm-chart-is-the-supported-deployment-inv-13) |
| [Q-HA-12](spec/ha-and-helm.md#q-ha-12--replica-registry-at-mvp) | Replica registry at MVP? | (a) table and heartbeat in the MVP; API and UI P1 | [ADR-013](#adr-013--one-binary-identical-stateless-replicas-inv-2) |

### Operations and observability

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-OPS-1](spec/operations-and-observability.md#q-ops-1--couple-liveness-to-prolonged-unreadiness) | Couple liveness to prolonged unreadiness? | (a) never by default; an opt-in grace period | — |
| [Q-OPS-2](spec/operations-and-observability.md#q-ops-2--should-object-storage-reachability-gate-readiness) | Should object-storage reachability gate readiness? | (c) configurable with gating as default; (b) put to the owner as a change to INV-12 | [ADR-023](#adr-023--high-availability-through-postgresql-coordination-inv-12) |
| [Q-OPS-3](spec/operations-and-observability.md#q-ops-3--anonymous-access-to-the-version-endpoint) | Anonymous access to the version endpoint? | (b) authenticated by default, opt-in public | — |
| [Q-OPS-4](spec/operations-and-observability.md#q-ops-4--tamper-evidence-for-audit-records) | Tamper evidence for audit records? | (a) external append-only log store in P1; (b) hash chain as P2 hardening | [ADR-080](#adr-080--audit-records-commit-with-the-change-they-record) |
| [Q-OPS-5](spec/operations-and-observability.md#q-ops-5--cloudevents-type-prefix) | CloudEvents `type` prefix? | (a) `trove.`, unless a project domain is named before the first webhook release | [ADR-082](#adr-082--events-through-a-transactional-outbox-as-cloudevents-signed-per-standard-webhooks-p1) |
| [Q-OPS-6](spec/operations-and-observability.md#q-ops-6--the-human-login-oidc-client-in-configuration-documents) | May configuration documents declare the human-login OIDC client? | (a) no, configuration file only | [ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1) |
| [Q-OPS-7](spec/operations-and-observability.md#q-ops-7--one-cron-syntax-for-the-whole-system) | One cron syntax for the whole system? | (a) five-field Kubernetes CronJob syntax with macros, in UTC | [ADR-042](#adr-042--race-free-garbage-collection-with-a-grace-period) |
| [Q-OPS-8](spec/operations-and-observability.md#q-ops-8--locking-objects-managed-by-configuration-documents) | Lock objects managed by configuration documents? | (a) locked by default, with a global switch | [ADR-081](#adr-081--configuration-as-code-documents-own-their-objects-plan-before-apply-p1) |
| [Q-OPS-9](spec/operations-and-observability.md#q-ops-9--ownership-of-the-job-api) | Which page owns the job API? | (a) operations keeps the API, architecture.md the model | [ADR-034](#adr-034--one-postgresql-job-table-for-maintenance-api-operations-and-internal-work) |
| [Q-OPS-10](spec/operations-and-observability.md#q-ops-10--default-cap-for-the-repo-metric-label) | Default cap for the `repo` metric label? | (a) 500 per metric family and replica | [ADR-027](#adr-027--structured-logs-prometheus-metrics-otlp-traces-audit-in-postgresql-inv-16) |

### Management API and UI

| ID | Question | Recommended | ADR |
|---|---|---|---|
| [Q-API-1](spec/api-and-ui.md#q-api-1--openapi-generation-approach) | How is the OpenAPI document produced? | A: spec-first, generated Go server interfaces and TypeScript types, confirmed by a spike | [ADR-083](#adr-083--the-openapi-31-document-is-the-api-contract) |
| [Q-API-2](spec/api-and-ui.md#q-api-2--is-if-match-mandatory-on-writes) | Is `If-Match` mandatory on writes? | A: optional, mandatory where the owning page says so | [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) |
| [Q-API-3](spec/api-and-ui.md#q-api-3--cluster-wide-rate-limiting) | Cluster-wide rate limiting? | A: per replica for the MVP; B: shared counters for unauthenticated classes in P1 | — |
| [Q-API-4](spec/api-and-ui.md#q-api-4--inv-17s-wording-and-byte-transfers-on-files) | Does INV-17 cover byte transfers on `/files`? | A: amend INV-17 to "public, documented endpoints" | [ADR-028](#adr-028--the-ui-uses-only-the-public-api-inv-17) |
| [Q-API-5](spec/api-and-ui.md#q-api-5--owning-page-for-build-information) | Which page owns build information? | A: a new page `builds.md` | — |
| [Q-API-6](spec/api-and-ui.md#q-api-6--pagination-parameter-names) | Pagination parameter names? | A: `limit` and `cursor` everywhere | [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) |
| [Q-API-7](spec/api-and-ui.md#q-api-7--syntax-of-custom-methods) | Syntax of custom methods? | A: sub-path verbs | [ADR-084](#adr-084--uniform-api-conventions-problem-details-cursors-merge-patch-etags-idempotency-keys) |
| [Q-UI-1](spec/api-and-ui.md#q-ui-1--component-library) | Which component library? | A: Angular Material with the CDK and `@angular/aria` | [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) |
| [Q-UI-2](spec/api-and-ui.md#q-ui-2--visual-direction-of-the-shell-example-screenshot-requested) | Shell layout and density? Owner's example screenshot requested | A: left navigation, compact tables, until the screenshot decides | [ADR-086](#adr-086--ui-baseline-angular-22-defaults-a-strict-csp-nothing-from-foreign-origins) |
| [Q-UI-3](spec/api-and-ui.md#q-ui-3--layout-of-the-package-details-page-example-screenshot-requested) | Layout of the package details page? Owner's example screenshot requested | A: two columns on wide screens, one column on narrow ones | — |
| [Q-UI-4](spec/api-and-ui.md#q-ui-4--layout-of-the-policy-editor-and-the-explainer-example-screenshot-requested) | Layout of the policy editor and the explainer? Owner's example screenshot requested | A: one page with a sticky preview; the explainer as its own screen | — |
| [Q-UI-5](spec/api-and-ui.md#q-ui-5--internationalization-mechanism) | Internationalization mechanism? | A: `@angular/localize`, one build per locale | [ADR-088](#adr-088--english-and-german-in-the-mvp) |
| [Q-UI-6](spec/api-and-ui.md#q-ui-6--external-images-in-readmes) | External images in READMEs? | A: blocked by default; B (allow `https`) as an operator option | [ADR-087](#adr-087--readmes-rendered-on-the-server-through-an-allowlist-sanitizer) |
