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

## Design invariants

Proposed baseline that every spec page builds on, so the pages agree. A page may refine an
invariant but must not contradict it; a page that finds an invariant wrong keeps it and raises an
open question. Each invariant becomes an ADR in [docs/decisions.md](../decisions.md).

**INV-1 · Toolchain.** Go 1.27.3 (verified 2026-10-28: local `go version` and the go.dev
download list both show go1.27.3 as current stable). Angular 22 (npm `@angular/core` dist-tag
`latest` = 22.2.3 on 2026-10-28). The Angular build is embedded into the Go binary (`embed`), so
one container image serves UI, management API and package protocols.

**INV-2 · Process model.** One binary, `trove`. Every replica is identical and stateless: it
serves HTTP and runs background workers. There is no primary node. Subcommands: `trove serve`,
`trove migrate`, `trove admin …` (bootstrap and recovery). The Helm chart can split the roles
(`api`, `worker`); the default is `all`.

**INV-3 · Metadata store.** PostgreSQL is the only stateful dependency besides object storage:
repository configuration, package and file metadata, users, groups, tokens, permissions, audit
events, job queue and cross-replica coordination. No Redis, no shared filesystem, no embedded
database. Schema migrations are embedded in the binary and applied by `trove migrate` (Helm
pre-install/pre-upgrade Job) under a PostgreSQL advisory lock. Migrations follow
expand/contract, so version N and N-1 replicas can run side by side during a rolling upgrade.

**INV-4 · Blob store.** S3-compatible object storage (AWS S3, MinIO, Ceph RGW and similar).
Binaries are content-addressed by SHA-256 under `<prefix>/sha256/<h0h1>/<h2h3>/<64 hex>` and
stored once, however many repositories and paths reference them. An upload is spooled to local
temporary storage while SHA-256, SHA-1, MD5 (and SHA-512 where a format needs it) are computed,
then written to its content address unless that object already exists. PostgreSQL holds every
reference to a blob; a garbage-collection job deletes unreferenced blobs after a grace period.
No binary content is stored in PostgreSQL.

**INV-5 · Repository model.** Every repository has a globally unique key, exactly one package
format and one class: `local` (hosted), `remote` (pull-through proxy and cache of one upstream)
or `virtual` (ordered aggregation of local and remote repositories of the same format, with one
default deployment target). Federated repositories are not part of the MVP. Repository keys match
`^[a-z0-9][a-z0-9._-]{1,63}$`.

**INV-6 · URL layout.** One HTTP listener for clients (port 8080), one for operations (port
8081).

| Path | Purpose |
|---|---|
| `/api/v1/…` | Management REST API (JSON). The OpenAPI 3.1 document at `/api/v1/openapi.json` is the source of truth. |
| `/api/<format>/<repoKey>/…` | Package-manager protocols, e.g. `/api/npm/npm-all/`, `/api/nuget/nuget-all/v3/index.json` |
| `/files/<repoKey>/<path>` | Raw file access to any repository |
| `/auth/…` | Browser login flow (OIDC redirect and callback, logout) |
| `/ui/…` | Angular UI |
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
a server-side session row referenced by the `trv_session` cookie. Clients present a token as
`Authorization: Bearer <token>`, as HTTP Basic password (any username; for NuGet and other
clients that only speak Basic) or as `X-NuGet-ApiKey` for NuGet pushes.

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
[gitlab-ci-oidc.md](gitlab-ci-oidc.md) weighs it.

**INV-11 · Pull-through proxy.** A remote repository fetches from its upstream on a cache miss,
stores the binary in the blob store, caches metadata for a configurable period, caches misses
(negative cache), and serves stale metadata when the upstream is unreachable. Concurrent misses
for the same object are collapsed: in-process single-flight plus a PostgreSQL advisory lock
across replicas. URLs in upstream metadata are rewritten to point at trove.

**INV-12 · High availability.** At least two replicas behind a Kubernetes Service and Ingress,
with a PodDisruptionBudget and readiness tied to database and object-storage reachability.
Replicas coordinate only through PostgreSQL: `LISTEN/NOTIFY` for cache invalidation,
`FOR UPDATE SKIP LOCKED` for the job queue, advisory-lock leases for singleton schedulers
(garbage collection, cleanup). PostgreSQL high availability and S3 durability come from the
platform (e.g. CloudNativePG, Patroni, Amazon RDS; AWS S3, MinIO); the chart consumes them and
does not bundle a production database.

**INV-13 · Deployment.** The Helm chart `trove` in `deploy/helm/trove` is the supported
deployment. It renders the Deployment(s), a migration Job hook, Service, Ingress or Gateway
route, PodDisruptionBudget, HorizontalPodAutoscaler, ServiceMonitor and NetworkPolicy. Secrets
come from existing Kubernetes Secrets; S3 credentials from workload identity (IRSA and similar)
or static keys.

**INV-14 · MVP formats.** npm (clients: npm, pnpm, Yarn classic and Berry) and NuGet v3 (dotnet
CLI, nuget.exe, Visual Studio), each with local, remote and virtual repositories. NuGet v2
(OData) is P2.

**INV-15 · Configuration.** One YAML file (`/etc/trove/trove.yaml`), every key overridable by
an environment variable `TROVE_<SECTION>_<KEY>`; secrets are referenced from files or
environment variables, never written inline into a ConfigMap. Repositories, permissions and OIDC
mappings live in PostgreSQL and are managed through the API and UI; a declarative bootstrap file
(configuration as code) is P1.

**INV-16 · Observability and audit.** Structured JSON logs through `log/slog`; Prometheus
metrics prefixed `trove_`; optional OpenTelemetry traces over OTLP. Audit events (actor,
action, repository, path, outcome, source) are stored in PostgreSQL and emitted to the log
stream.

**INV-17 · UI.** The Angular UI uses only the public `/api/v1` API, so every UI action is
scriptable.

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
- Every page ends with `## Open questions` (IDs `Q-<AREA>-<n>`, each with options and a
  recommendation). Security-relevant gaps that stay open go into `## What this does not cover` as
  `### H-<n> · <title>`. H numbers are unique across all pages of this directory; a new item takes
  the next free number. Each item names the component, the subject it affects, whether it is live
  or dormant (and which requirement makes it live), and what makes it real.

## Pages

| Page | Requirement prefix | Scope |
|---|---|---|
| [architecture.md](architecture.md) | `R-ARCH` | Components, Go and Angular code layout, request paths, format plug-in interface, background jobs, caching |
| [repositories-and-proxy.md](repositories-and-proxy.md) | `R-REPO` | Repository model and API, virtual resolution, pull-through proxy semantics |
| [storage.md](storage.md) | `R-STOR` | Blob store on S3, metadata schema, upload/download pipeline, garbage collection, quotas, cleanup |
| [identity-and-authentication.md](identity-and-authentication.md) | `R-AUTH` | OIDC login, sessions, users, groups, service accounts, tokens, client authentication |
| [authorization.md](authorization.md) | `R-AUTHZ` | Permission model, evaluation, package ownership, administration roles |
| [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | `R-CI` | GitLab CI OIDC token exchange, identity mappings, `CI_JOB_TOKEN` fallback |
| [formats/npm.md](formats/npm.md) | `R-NPM` | npm registry protocol, local/remote/virtual behavior |
| [formats/nuget.md](formats/nuget.md) | `R-NUGET` | NuGet v3 protocol, symbols, local/remote/virtual behavior |
| [ha-and-helm.md](ha-and-helm.md) | `R-HA` | High availability, failure modes, upgrades, backup, Helm chart values |
| [operations-and-observability.md](operations-and-observability.md) | `R-OPS` | Health, metrics, logs, audit, maintenance operations, webhooks, configuration as code |
| [api-and-ui.md](api-and-ui.md) | `R-API`, `R-UI` | Management API conventions and surface, Angular UI information architecture |
