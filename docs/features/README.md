# Feature catalog

This directory is the feature catalog of a full-featured binary repository manager: what such a
system does, feature by feature, precise enough to implement it compatibly. It is the yardstick
trove is built and measured against. The catalog describes required behavior independent of
trove's design; the design lives in [docs/spec/](../spec/README.md), the measured state and the
work list in [docs/coverage/](../coverage/README.md).

## Entry format

Every feature has one entry with a stable ID:

```markdown
### RMT-004 · Negative cache period for upstream misses
Priority: **MVP**

What the feature does; its settings (descriptive name, meaning, default); the operations the
API offers for it; where it sits in the UI; limits and edge cases; how it interacts with other
features.
```

- The `### <ID> · <Title>` heading and the `Priority:` line directly below it are machine-read
  (the coverage checklist is generated from them). Keep both on one line each.
- IDs are never renumbered or reused. A new feature takes the next free number in its prefix.
- Values (defaults, limits) are stated only where known; otherwise the entry says
  *unspecified*. Setting names are descriptive, not tied to any product.
- Long reference tables (every field of a settings form, every operation) go into `<details>`
  blocks.

## Priority rubric

The priority is trove's, judged against the owner's goals: a self-hosted repository manager
with Go 1.27.3 backend, Angular 22 UI, PostgreSQL, S3, high availability, Helm deployment, OIDC
login, tokens, fine-grained permissions, GitLab CI OIDC federation and pull-through proxying; npm
and NuGet first.

| Priority | Meaning |
|---|---|
| **MVP** | Needed for the first release: npm and NuGet with local, remote (pull-through) and virtual repositories; OIDC user login; access tokens; fine-grained permissions; GitLab CI OIDC token exchange; PostgreSQL; S3; high availability; Helm chart; the UI and API to operate all of that. |
| **P1** | Directly after the MVP: high-demand formats (Docker/OCI, Maven, PyPI, Go, Helm charts, generic), webhooks, retention and cleanup policies, search API, build metadata basics, configuration as code, further CI OIDC providers. |
| **P2** | Breadth later: the remaining package formats, replication, a query language, signed download URLs, advanced storage options. |
| **Later** | Large subsystems not planned soon: federation across sites, distribution to edge instances, release lifecycle and evidence, a vulnerability scanning engine. |
| **Out of scope** | Hosted-service plumbing without meaning for a self-hosted repository manager (billing, subscriptions and license management of a hosted service, operator-run networking). One short entry each, so the inventory stays complete. |

## Pages

| Page | ID prefix | Scope |
|---|---|---|
| [repositories.md](repositories.md) | `REPO` | Repository classes, local and virtual repository settings, naming, layouts, client support |
| [remote-repositories.md](remote-repositories.md) | `RMT` | Remote (pull-through) repositories, caching, chained instances, outbound proxies |
| [replication-and-federation.md](replication-and-federation.md) | `REPL` | Push/pull replication, federated repositories, identity federation, multi-site topology |
| [artifacts.md](artifacts.md) | `ART` | Deploy, download, properties, copy/move/delete, trash can, checksums, signed URLs, browsing |
| [search-and-query.md](search-and-query.md) | `SRCH` | Search operations and UI search, query language |
| [builds-and-ci.md](builds-and-ci.md) | `BLD` | Build metadata, promotion and retention, CI integrations, companion CLI capabilities |
| [identity-and-access.md](identity-and-access.md) | `IAM` | Users, groups, permissions, roles, projects, environments, security settings |
| [authentication-providers.md](authentication-providers.md) | `AUTHN` | User login: OAuth/OIDC SSO, SAML, LDAP, SCIM, header SSO, MFA, user profile |
| [access-tokens.md](access-tokens.md) | `TOK` | Access, reference, refresh, identity and pairing tokens; scopes; expiry; revocation |
| [oidc-ci-integration.md](oidc-ci-integration.md) | `CIOIDC` | OIDC federation for CI systems, identity mappings, token exchange, GitLab identity facts |
| [storage-and-blobs.md](storage-and-blobs.md) | `STOR` | Content-addressed storage, storage providers, S3, direct cloud download, garbage collection |
| [retention-and-cleanup.md](retention-and-cleanup.md) | `RET` | Retention and cleanup policies, archiving, cold storage |
| [ha-and-deployment.md](ha-and-deployment.md) | `HA` | High availability, clustering, Helm deployment, database, load balancer, sizing |
| [operations-and-monitoring.md](operations-and-monitoring.md) | `OPS` | Logs, metrics, health, backup, import/export, configuration as code, general settings |
| [events-and-extensibility.md](events-and-extensibility.md) | `EVT` | Webhooks, server-side scripting hooks, plugins, OpenAPI, GraphQL, AI tool server |
| [distribution-and-lifecycle.md](distribution-and-lifecycle.md) | `DIST` | Release sets, distribution to edge instances, lifecycle stages, evidence |
| [security-scanning-and-curation.md](security-scanning-and-curation.md) | `SEC` | Vulnerability and license scanning, upstream curation, SBOM (capability level) |
| [ui-inventory.md](ui-inventory.md) | `UI` | Every screen and dialog of the UI, client setup instructions |
| [api-content-operations.md](api-content-operations.md) | `API` | Operation catalog: artifacts, repositories, search, builds, webhooks, plugins |
| [api-admin-operations.md](api-admin-operations.md) | `ADMAPI` | Operation catalog: tokens, users, groups, roles, permissions, OIDC, system, projects |
| [formats/README.md](formats/README.md) | `FMT` | Package-format matrix, cross-format behavior, per-format pages (`PKG-<FORMAT>`) |
| [beyond-baseline.md](beyond-baseline.md) | `BEYOND` | Formats and capabilities beyond this catalog's baseline |
