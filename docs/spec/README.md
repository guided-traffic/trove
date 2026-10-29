# trove specification

Status: specification — nothing described here is implemented yet.

This directory is trove's target design: what trove will do and how, requirement by
requirement. Each requirement names the catalog features it covers (`Covers:` line, IDs from
[docs/features/](../features/README.md)), so coverage can be measured in
[docs/coverage/](../coverage/README.md). Design decisions and their alternatives are logged in
[docs/decisions.md](../decisions.md).

## Owner constraints

Given by the project owner; not up for redesign in these pages:

- Backend in Go 1.27.3; frontend in Angular 22.
- PostgreSQL as database; S3 as binary storage.
- High availability; deployment through a Helm chart.
- OIDC user login, token issuance, detailed permission management.
- GitLab CI jobs obtain trove permissions through an OIDC procedure, with detailed,
  configurable permissions (read other packages, write the packages the job produces).
- Pull-through proxy: external package sources can be connected.
- Package formats: every format in the feature catalog, more welcome; npm and NuGet first.
- High availability is a core requirement that cannot be deselected: trove runs on HA PostgreSQL,
  HA Valkey (with Sentinel) and S3.
- Horizontal scale: n replicas answer requests that a Kubernetes Service distributes at random.
  The expected load exceeds what one replica can carry, so the service is cut for load sharing
  from the start. A node drain causes no outage when the setup is sized for it. There may be more
  than one type of worker, and the service may be split.
- A configurable backup job, comparable to GitLab's, creates a consistent backup of the service
  when it runs and stores it in S3.
- No ReadWriteMany storage class can be assumed in the cluster: no volume may be attached to
  several pods. If a worker type needs a ReadWriteOnce volume, that is acceptable but not desired.

## Design invariants

Baseline that every spec page builds on, so the pages agree. The development agent decided it, and
the owner reviews it at the walkthrough; each invariant is an ADR in
[docs/decisions.md](../decisions.md) (INV-1 to INV-17: ADR-012 to ADR-028; INV-18: ADR-096;
INV-19: ADR-097). A page may refine an invariant but must
not contradict it. A page that finds an invariant wrong raises a question in its `## Decisions`
section; a choice that changes an invariant says so and edits it here as a cross-page consequence,
as the choices behind INV-6, INV-8, INV-12 and INV-17 did.

**INV-1 · Toolchain.** Go 1.27.3 (verified 2026-10-28: local `go version` and the go.dev
download list both show go1.27.3 as current stable). Angular 22 (npm `@angular/core` dist-tag
`latest` = 22.2.3 on 2026-10-28). The Angular build is embedded into the Go binary (`embed`), so
one container image serves UI, management API and package protocols.

**INV-2 · Process model and roles.** One binary and one container image, `trove`, with roles. A
role is a set of enabled subsystems chosen by configuration, not a separate build. Roles of the
MVP: `web` (every HTTP request: UI, management API, package protocols, raw files), `worker`
(background jobs from the job queue: indexing, garbage collection, cleanup, webhook delivery) and
`backup` (the backup and restore commands, run as Kubernetes Job or CronJob). The configuration
key `process.role` selects the role of `trove serve`: `web`, `worker` or `all`, with no default
([architecture.md](architecture.md) R-ARCH-001). The Helm chart deploys `web` and `worker` as
separate Deployments from the start, each with its own replica count and autoscaling, and never
renders `all`. The combined role `all` exists only for a binary started outside the chart during
development; the local development profile runs the chart with separate `web` and `worker`
Deployments ([ha-and-helm.md](ha-and-helm.md) R-HA-001, R-HA-051). A role can be split further
later (for example a separate deployment for package traffic behind its own Service) without code
changes. Every replica of a role is identical and interchangeable; there is no primary node.
Subcommands: `trove serve`, `trove migrate`, `trove backup …`, `trove admin …` (bootstrap and
recovery). Owner constraint since 2026-10-28.

**INV-3 · State.** PostgreSQL is the system of record: repository configuration, package and file
metadata, users, groups, tokens, permissions, audit events, the durable job queue, the change log
that carries cross-replica invalidation ([architecture.md](architecture.md) R-ARCH-018) and backup
run records. The backup target, not the database, is the record of backups: run records are not
part of a backup ([backup-and-restore.md](backup-and-restore.md) R-BAK-004). Valkey holds shared
ephemeral state: request collapsing across replicas, the negative cache of remote repositories,
rate-limit counters with cluster scope, the fast path of change messages and signals, and
short-lived coordination ([architecture.md](architecture.md) R-ARCH-030). Nothing that trove
serves to a client and no positive authentication or authorization result is read from Valkey,
so Valkey holds no shared cache of either (Q-ARCH-16). Nothing in Valkey is the only copy of
durable data: losing Valkey or its contents (an outage, a Sentinel failover with asynchronous
replication, a flush, a restart) costs performance only, never correctness or data. Request
collapsing and negative caching then work per replica, rate limits are approximated per replica,
and changes reach the other replicas at the speed of the change-log poll (INV-12). A backup does
not include Valkey. Mutual exclusion whose failure would corrupt data (schema migrations,
singleton jobs that must not run twice) stays in PostgreSQL. No shared filesystem, no embedded
database. Schema migrations are embedded in the binary and applied by `trove migrate` (Helm
pre-install/pre-upgrade Job) under a PostgreSQL advisory lock. Migrations follow
expand/contract, so version N and N-1 replicas can run side by side during a rolling upgrade.
Valkey is an owner constraint since 2026-10-28.

**INV-4 · Blob store.** S3-compatible object storage (AWS S3, MinIO, Ceph RGW and similar).
Binaries are content-addressed by SHA-256 under `<prefix>/sha256/<h0h1>/<h2h3>/<64 hex>` and
stored once, however many repositories and paths reference them. SHA-256, SHA-1, MD5 and SHA-512
are computed for every upload, in one pass while it is spooled to local temporary storage; the
upload is then written to its content address unless that object already exists
([storage.md](storage.md) R-STOR-002, Q-STOR-2). PostgreSQL holds every reference to a blob; a
garbage-collection job deletes unreferenced blobs after a grace period. No binary content is
stored in PostgreSQL.

**INV-5 · Repository model.** Every repository has a globally unique key, exactly one package
format and one class: `local` (hosted), `remote` (pull-through proxy and cache of one upstream)
or `virtual` (ordered aggregation of local and remote repositories of the same format, with one
default deployment target). Federated repositories are not part of the MVP. Repository keys match
`^[a-z0-9][a-z0-9._-]{1,63}$`.

**INV-6 · URL layout.** One HTTP listener for clients (port 8080), one for operations (port
8081). trove owns the host root of `server.base_url`, which is an origin without a path
([operations-and-observability.md](operations-and-observability.md) R-OPS-005).

| Path | Purpose |
|---|---|
| `/api/v1/…` | Management REST API (JSON). The OpenAPI 3.1 document at `/api/v1/openapi.json` is the source of truth. |
| `/api/<format>/<repoKey>/…` | Package-manager protocols, e.g. `/api/npm/npm-all/`, `/api/nuget/nuget-all/v3/index.json` |
| `/files/<repoKey>/<path>` | Raw file access to any repository |
| `/auth/…` | Browser login flow (OIDC redirect and callback, logout) |
| `/ui/…` | Angular UI |
| `/-/npm/v1/oidc/token/exchange/package/…` (host root) | npm trusted-publishing token exchange (P1). npm resolves it against the registry's host root ([formats/npm.md](formats/npm.md) R-NPM-022). |
| `/v2/…` (host root) | Reserved for the OCI distribution API (OCI format, P1); the repository key is the first path segment; no other route may use this prefix; answers `404` until the OCI format exists ([ha-and-helm.md](ha-and-helm.md) Q-HA-4, R-HA-041). |
| `:8081/healthz`, `:8081/readyz`, `:8081/metrics` | Liveness, readiness, Prometheus metrics |

**INV-7 · Human identity.** Humans authenticate only through an external OpenID Connect provider
(authorization code flow with PKCE; e.g. Keycloak, Microsoft Entra ID, GitLab, Dex). trove is a
relying party and stores no user passwords. Users are created just in time from ID-token claims;
group membership comes from a configurable claim and maps to trove groups. One local
break-glass administrator exists for bootstrap and recovery; its credential comes from a
Kubernetes Secret and it can be disabled.

**INV-8 · Tokens.** trove issues its own credentials and never accepts an identity provider's
access token on package endpoints. Every trove token is an opaque random string with a type
prefix, stored only as a SHA-256 hash, revocable immediately, with an expiry and a scope.

| Prefix | Token |
|---|---|
| `trv_pat_` | Personal access token of a user |
| `trv_sat_` | Service-account token |
| `trv_cit_` | CI token from an OIDC exchange (short-lived) |

Format: `<prefix><32 random bytes, base62><6-character base62 CRC32 of the preceding part>`, so
secret scanners can recognise and pre-validate a leaked token offline. UI sessions are separate:
a server-side session row referenced by the `__Host-trv_session` cookie (CSRF cookie
`__Host-trv_csrf`, login-binding cookie `__Host-trv_oidc_binding`;
[identity-and-authentication.md](identity-and-authentication.md) Q-AUTH-2). Clients present a
token as `Authorization: Bearer <token>`, as HTTP Basic password (any username; for NuGet and
other clients that only speak Basic) or as `X-NuGet-ApiKey` for NuGet pushes.

**INV-9 · Authorization.** Deny by default. Permissions are allow-only grants: a *permission
policy* binds principals (users, groups, service accounts, CI identity mappings) to resources
(repositories by key or pattern, narrowed by include/exclude patterns on paths or package names)
and the actions `read`, `annotate`, `deploy`, `overwrite`, `delete`, `manage`. A request's
effective permission is the union of the grants of its principal and the principal's groups,
intersected with the presented token's scope. System administration is a separate role, not a
repository action. Every authorization decision can be audited.

**INV-10 · CI identity (GitLab).** A GitLab job authenticates with a GitLab-issued OIDC ID token
(`id_tokens:` keyword, audience = trove's base URL) and exchanges it at
`POST /api/v1/auth/oidc/token` for a short-lived `trv_cit_` token. trove validates the
signature against the JWKS of the configured GitLab issuer, and `iss`, `aud`, `exp`, `nbf`,
`iat`. Identity mappings then match claims (`project_path`, `namespace_path`, `ref`, `ref_type`,
`ref_protected`, `environment`, `pipeline_source`, `user_login`, …) and grant permissions; a grant
may be templated from claims (e.g. a package-name pattern derived from `project_path`). Verifying
a raw `CI_JOB_TOKEN` against the GitLab API is an optional fallback mode, off by default;
specified as P2 in [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-022.

**INV-11 · Pull-through proxy.** A remote repository fetches from its upstream on a cache miss,
stores the binary in the blob store, caches metadata for a configurable period, caches misses
(negative cache), and serves stale metadata when the upstream is unreachable. Concurrent misses
for the same object are collapsed: in-process single-flight plus a Valkey lock across replicas. A
lock lost in a Valkey failover only causes a duplicate upstream fetch, which content addressing
makes harmless. URLs in upstream metadata are rewritten to point at trove.

**INV-12 · High availability and horizontal scale.** Owner constraint; it cannot be deselected.
The production topology is HA PostgreSQL (e.g. CloudNativePG, Patroni, a managed service), HA
Valkey with Sentinel (one primary, at least one replica, at least three Sentinels; trove finds the
primary through Sentinel and follows failovers) and S3-compatible object storage. Single-instance
PostgreSQL or Valkey serve development only; the local development profile itself runs PostgreSQL
with a standby and Valkey with a replica and three Sentinels ([ha-and-helm.md](ha-and-helm.md)
R-HA-051). Capacity planning assumes the load exceeds one replica: load is shared by adding
replicas of a role. Each of the long-running roles `web` and `worker` runs at least two replicas
spread across nodes (topology spread constraints), each role behind a PodDisruptionBudget that
evicts one pod at a time; `web` replicas sit behind a Kubernetes Service without session affinity,
so any request may reach any replica. The `backup` role runs as a Job, not as replicas
([architecture.md](architecture.md) R-ARCH-024): one Job pod at a time, because an installation
runs one backup operation at a time, and no PodDisruptionBudget selects it, so a drain evicts it
without waiting for it. Its Job then retries: a retried backup skips what the evicted pod already
copied, and an evicted restore resumes where it stopped
([backup-and-restore.md](backup-and-restore.md) R-BAK-010, R-BAK-012, R-BAK-021). A node drain
causes no outage when each of `web` and `worker` has at least two replicas on different nodes: a
replica that is shut down stops being ready, waits for endpoint removal, lets in-flight requests
finish within the termination grace period, and releases its jobs to other replicas. PostgreSQL
connections do not grow without bound with the replica count: the main pool may run through a
transaction-mode pooler; only `worker` replicas hold session connections, for their leases,
besides the one-off `trove migrate` and `trove backup` processes
([architecture.md](architecture.md) R-ARCH-019, Q-ARCH-14; the budget in
[ha-and-helm.md](ha-and-helm.md) R-HA-016). Valkey being unavailable degrades trove and is
alerted on, but does not make replicas unready ([ha-and-helm.md](ha-and-helm.md) R-HA-057):
request collapsing and negative caching of remote repositories work per replica only, so an
object may be fetched from its upstream once per replica; rate limits are approximated per
replica, at the configured values divided by the live replicas of the role; changes reach the
other replicas at the speed of the change-log poll instead of at once, and signals wait for their
consumers' next poll.

The remaining HA rules: the `web` replicas sit behind a Kubernetes Service and an Ingress or
Gateway route, and readiness is tied to PostgreSQL and process health
([operations-and-observability.md](operations-and-observability.md) R-OPS-001). Object storage
gates only a replica's first readiness (startup check, [storage.md](storage.md) R-STOR-035).
Afterwards S3 failures degrade blob requests one by one through the circuit breaker and are
reported and alerted on, while running replicas stay ready; continuous gating on object storage is
the opt-in `health.require_object_store: true` ([storage.md](storage.md) Q-STOR-1,
[ha-and-helm.md](ha-and-helm.md) Q-HA-1,
[operations-and-observability.md](operations-and-observability.md) Q-OPS-2). Replicas coordinate
through PostgreSQL (the durable job queue, the change log of [architecture.md](architecture.md)
R-ARCH-018, and leases whose loss would corrupt data) and Valkey (request collapsing, the negative
cache of remote repositories, rate-limit counters with cluster scope, the fast path of change
messages and signals); [architecture.md](architecture.md) R-ARCH-030 assigns each mechanism.
PostgreSQL and Valkey high availability and S3 durability come from the platform
(e.g. CloudNativePG, Patroni, Amazon RDS; Valkey with Sentinel; AWS S3); the chart consumes them
and does not bundle production instances.

**INV-13 · Deployment.** The Helm chart `trove` in `deploy/helm/trove` is the supported
deployment. It renders one Deployment per role (`web`, `worker`), the backup CronJob, a migration
Job hook, Services, an Ingress or Gateway route, PodDisruptionBudgets, HorizontalPodAutoscalers,
a ServiceMonitor and NetworkPolicies. No ReadWriteMany volume is used anywhere: scratch space is
`emptyDir` or a generic ephemeral volume (ReadWriteOnce, deleted with its pod). A role that ever
needs persistent local storage gets one ReadWriteOnce volume per pod; none is planned. Secrets
come from existing Kubernetes Secrets; S3 credentials from workload identity (IRSA and similar)
or static keys.

**INV-14 · MVP formats.** npm (clients: npm, pnpm, Yarn classic and Berry) and NuGet v3 (dotnet
CLI, nuget.exe, Visual Studio), each with local, remote and virtual repositories. NuGet v2
(OData) is P2.

**INV-15 · Configuration.** One YAML file (`/etc/trove/trove.yaml`), every key overridable by
an environment variable `TROVE_<SECTION>_<KEY>`; secrets are referenced from files or
environment variables, never written inline into a ConfigMap. Repositories, permissions and CI
identity mappings (INV-10) live in PostgreSQL and are managed through the API and UI. Login
providers, including their claim mapping and group rules, are file configuration only
([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-004). A declarative
bootstrap file (configuration as code) is P1.

**INV-16 · Observability and audit.** Structured JSON logs through `log/slog`; Prometheus
metrics prefixed `trove_`; optional OpenTelemetry traces over OTLP. Audit events (actor,
action, repository, path, outcome, source) are stored in PostgreSQL and emitted to the log
stream.

**INV-17 · UI.** The Angular UI uses only public, documented endpoints: the operations of the
OpenAPI document (`/api/v1/…` and `/files/…`) and the browser sign-in flow under `/auth/…`. It
has no private UI endpoints, so every UI action is scriptable.

**INV-18 · No state between requests on a replica.** No request may depend on state that an
earlier request left in one replica's memory or local disk. A protocol that spans several requests
(chunked or resumable uploads, login polling, paged listings) keeps its state in PostgreSQL or
Valkey and its data in S3 (multipart uploads), so consecutive requests may land on different
replicas. Local disk is scratch space for one request or one job only. Owner constraint since
2026-10-28 (random distribution by the Kubernetes Service).

**INV-19 · Backup.** `trove backup create` produces a consistent backup of the service: a
transactionally consistent snapshot of trove's PostgreSQL data together with every blob that
snapshot references, written to a configurable S3 target (endpoint, bucket, prefix, credentials,
retention). The chart schedules it as a CronJob; `trove backup restore` restores a backup into an
empty installation, and an interrupted restore of the same backup resumes where it stopped
([backup-and-restore.md](backup-and-restore.md) Q-BAK-10). Valkey is not part of a backup
(INV-3). Owner constraint since 2026-10-28; format and procedure in
[backup-and-restore.md](backup-and-restore.md).

## Requirement format

```markdown
### R-NPM-012 · Abbreviated package metadata
Priority: **MVP** · Covers: PKG-NPM-004, RMT-011

Normative text (MUST/SHOULD/MAY in the RFC 2119 sense), data model, API, configuration,
acceptance criteria.
```

- The heading and the `Priority:` line are machine-read; `Covers:` lists catalog IDs or says
  `trove-only`.
- Configuration values are marked `# default (proposed)` or `# example`.
- Every page ends with `## Decisions` followed by `## What this does not cover`.
- `## Decisions` opens with one sentence: each question below was decided by the development agent
  from the listed options, and the owner reviews the choices at the walkthrough. Each question is a
  block headed `### Q-<AREA>-<n> · <title>`; other pages link to these headings, so a published
  heading keeps its wording. A block holds, in this order: the status line, optional context, an
  options table, the choice, the rationale (including the condition under which the development
  agent would revisit the choice), and what the choice changed in the spec. A decision that changes
  an invariant says so and lists the edit to this page as a cross-page consequence. A question
  raised later goes into the same section and is decided in the same form.

  ```markdown
  ### Q-<AREA>-<n> · <title>

  Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

  | Option | What it means | For | Against |
  |---|---|---|---|
  | (a) … | … | … | … |

  **Choice:** (a) — …

  **Rationale:** … The development agent would revisit this if …

  **Applied in the spec:** R-<AREA>-…: …; cross-page: INV-… in README.md.
  ```

- Security-relevant gaps that stay open go into `## What this does not cover` as
  `### H-<n> · <title>`. H numbers are unique across all pages of this directory; a new item takes
  the next free number. Each item names the component, the subject it affects, whether it is live
  or dormant (and which requirement makes it live), and what makes it real.

## Pages

| Page | Requirement prefix | Scope |
|---|---|---|
| [architecture.md](architecture.md) | `R-ARCH` | Components, Go and Angular code layout, request paths, format plug-in interface, background jobs, caching |
| [repositories-and-proxy.md](repositories-and-proxy.md) | `R-REPO` | Repository model and API, virtual resolution, pull-through proxy semantics, proxy state across replicas, without Valkey and during a drain (R-REPO-049) |
| [storage.md](storage.md) | `R-STOR` | Blob store on S3, metadata schema, upload/download pipeline, garbage collection, quotas, cleanup, storage work per role and across replicas, storage state in backups |
| [identity-and-authentication.md](identity-and-authentication.md) | `R-AUTH` | OIDC login, sessions, users, groups, service accounts, tokens, client authentication |
| [authorization.md](authorization.md) | `R-AUTHZ` | Permission model, evaluation, package ownership, administration roles |
| [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | `R-CI` | GitLab CI OIDC token exchange, identity mappings, `CI_JOB_TOKEN` fallback |
| [formats/npm.md](formats/npm.md) | `R-NPM` | npm registry protocol, local/remote/virtual behavior |
| [formats/nuget.md](formats/nuget.md) | `R-NUGET` | NuGet v3 protocol, symbols, local/remote/virtual behavior |
| [backup-and-restore.md](backup-and-restore.md) | `R-BAK` | Backup job, backup format in S3, consistency with garbage collection, restore, retention |
| [ha-and-helm.md](ha-and-helm.md) | `R-HA` | High availability, failure modes, upgrades, backup CronJob and restore Job (chart side), platform backup (PITR, bucket versioning), disaster recovery, Helm chart values |
| [operations-and-observability.md](operations-and-observability.md) | `R-OPS` | Health, metrics, logs, audit, Valkey and coordination observability, operational signals and alerts, maintenance operations, webhooks, configuration as code |
| [api-and-ui.md](api-and-ui.md) | `R-API`, `R-UI` | Management API conventions and surface, Angular UI information architecture |
| `builds.md` (not yet written) | `R-BLD` | Build records, their API, retention, promotion and storage: the record schema and its PostgreSQL tables, the API and permissions ([authorization.md](authorization.md) R-AUTHZ-027), mapping of CI job names to build names, promotion, run retention and the retention hooks of [storage.md](storage.md) R-STOR-043. P1; written before P1 build work starts (milestone M9), and the page that creates it turns `builds.md` in this cell into a link to itself. One owner, decided the same way three times: option (a) of [Q-AUTHZ-8](authorization.md#q-authz-8--spec-page-for-build-metadata), [Q-STOR-3](storage.md#q-stor-3--owner-of-build-records) and [Q-API-5](api-and-ui.md#q-api-5--owning-page-for-build-information); build records are not deferred to P2. It must provide R-STOR-043's contract: a run has a name, a number, a start time and its files as repository, path and SHA-256, and records live in PostgreSQL tables, not as repository items. |
