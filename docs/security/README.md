# Security architecture

Status: specification phase. No security mechanism of trove is implemented yet.

This directory describes trove's security architecture, with one page per perspective. It is
deliberately not one document that covers everything, because nobody finishes such a document and
so nobody checks it. The file names are the index.

How to report a vulnerability is in [SECURITY.md](../../SECURITY.md). This directory covers what is
already known.

## Form

- **One perspective per page.** Each page explains one view of the system: who is trusted with
  what, where credentials go, what the deployment is allowed to do. It references the code that
  implements the view and says only what the code does.
- **Every page ends with `## What this does not cover`.** That section lists the open gaps of the
  page's mechanisms.
- **Open gaps carry a stable identifier.** A gap is a `### H-<n> · <title>` heading. It lives in the
  `What this does not cover` section of the page whose mechanism has the gap, and nowhere else. It
  is never collected in a central list. The number stays the same for the gap's whole life, so
  reviews, commits and issues can refer to it.
- **Each gap states its case.** It names the component and the subject it affects, says whether it
  is live today or dormant (and what would make it live), and says what makes it real.
- **No tickets, no checkbox lists, and no promises of future work** on these pages. They describe
  the system as it is.
- Security notes appear only where a real security relation exists.

## Perspectives

No perspective page exists yet. Each one is written from the code once its mechanism is
implemented. Until then the design lives in [docs/spec/](../spec/README.md), where every page has a
`## Security notes` section and a `## What this does not cover` section.

| Page (not yet written) | Perspective | Design today |
|---|---|---|
| `roles-and-trust-boundaries.md` | Principals, roles, permission policies, and the trust placed in the identity provider, the edge, upstream registries, the shared origin and Valkey. Valkey is a boundary of its own: it holds shared ephemeral state for every replica, and a value read from it can at most make trove refuse a request, never grant access or supply served content, so whoever can write under trove's prefix can cost availability, never access or integrity | [identity-and-authentication.md](../spec/identity-and-authentication.md) (principals, sessions, break-glass); [authorization.md](../spec/authorization.md); [architecture.md](../spec/architecture.md) R-ARCH-005 to R-ARCH-007, R-ARCH-029 (optional content origin; residual risk H-3), R-ARCH-030 and R-ARCH-031 (what Valkey may hold, reader validation, keyspace per restore); [repositories-and-proxy.md](../spec/repositories-and-proxy.md) R-REPO-019, R-REPO-023; [ha-and-helm.md](../spec/ha-and-helm.md) R-HA-037, R-HA-056 (Valkey ACL user, TLS to data nodes and Sentinels) |
| `data-token-and-secret-flow.md` | Where tokens, sessions, stored secrets and package bytes travel and rest inside the running installation, and who can read them there | [identity-and-authentication.md](../spec/identity-and-authentication.md) R-AUTH-010 to R-AUTH-012, R-AUTH-022 to R-AUTH-032; [architecture.md](../spec/architecture.md) R-ARCH-021; [storage.md](../spec/storage.md) R-STOR-001, R-STOR-004, R-STOR-014; [ha-and-helm.md](../spec/ha-and-helm.md) R-HA-030; [operations-and-observability.md](../spec/operations-and-observability.md) R-OPS-013; [api-and-ui.md](../spec/api-and-ui.md) R-UI-043 |
| `backup-data-flow.md` | What `trove backup create` copies out of the installation (a database snapshot with sealed secrets, token hashes and personal data, and every referenced blob), the S3 target it rests in, the identities that read, write and delete there, encryption at rest and the key escrow, and what `trove backup restore` reads back | [backup-and-restore.md](../spec/backup-and-restore.md) R-BAK-001, R-BAK-002, R-BAK-011, R-BAK-015, R-BAK-020 to R-BAK-022, R-BAK-026 to R-BAK-028; [ha-and-helm.md](../spec/ha-and-helm.md) R-HA-014 (backup login), R-HA-030 (backup and restore Secrets), R-HA-042 (backup egress), R-HA-060 (backup and restore ServiceAccounts) |
| `ci-federation.md` | GitLab ID-token validation, identity mappings, grant templating, CI token lifetime and the optional job-token fallback | [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md); [authorization.md](../spec/authorization.md) R-AUTHZ-014; [formats/npm.md](../spec/formats/npm.md) R-NPM-022 |
| `privilege-footprint.md` | What trove itself may do: database roles, object-store permissions, the commands and keys of its Valkey ACL user, the pod's security context and service account, and network reach in and out | [ha-and-helm.md](../spec/ha-and-helm.md) R-HA-014, R-HA-033, R-HA-042; [architecture.md](../spec/architecture.md) R-ARCH-031 (Valkey least privilege); [storage.md](../spec/storage.md) R-STOR-031; [repositories-and-proxy.md](../spec/repositories-and-proxy.md) R-REPO-038; [operations-and-observability.md](../spec/operations-and-observability.md) R-OPS-006, R-OPS-015, R-OPS-042 |
| `input-validation-and-limits.md` | Path cleaning, untrusted package and upstream content, integrity checks, size, rate and concurrency limits, including rate limits counted cluster-wide in Valkey | [architecture.md](../spec/architecture.md) R-ARCH-005, R-ARCH-028, R-ARCH-034; [storage.md](../spec/storage.md) R-STOR-004, R-STOR-010; [repositories-and-proxy.md](../spec/repositories-and-proxy.md) R-REPO-040, R-REPO-041; [formats/npm.md](../spec/formats/npm.md); [formats/nuget.md](../spec/formats/nuget.md); [api-and-ui.md](../spec/api-and-ui.md) R-API-016, R-API-017 |
| `rotation-and-propagation.md` | How revocation, rotation, deactivation and upstream removals reach every replica through the PostgreSQL change log (Valkey only speeds them up), and what a restore undoes | [identity-and-authentication.md](../spec/identity-and-authentication.md) R-AUTH-014, R-AUTH-029, R-AUTH-030, R-AUTH-039; [architecture.md](../spec/architecture.md) R-ARCH-018, R-ARCH-021; [ha-and-helm.md](../spec/ha-and-helm.md) R-HA-017, R-HA-024 to R-HA-027, R-HA-054, R-HA-056 (Valkey credential rotation); [backup-and-restore.md](../spec/backup-and-restore.md) R-BAK-022, R-BAK-024; [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md) R-CI-003; [repositories-and-proxy.md](../spec/repositories-and-proxy.md) R-REPO-029; [operations-and-observability.md](../spec/operations-and-observability.md) R-OPS-040 |

## Gaps recorded so far

The spec pages already record `H-1` to `H-52`. Each item's text lives in the
`What this does not cover` section of the spec page linked below, and this table holds no gap text
of its own. It only says which perspective page each item moves to when that page is written. The
item keeps its number, and the spec page then links to the item's new home.

| Perspective page | Items (linked to their current home) |
|---|---|
| `roles-and-trust-boundaries.md` | [H-3](../spec/architecture.md#what-this-does-not-cover), [H-7, H-9, H-10](../spec/repositories-and-proxy.md#what-this-does-not-cover), [H-13](../spec/storage.md#what-this-does-not-cover), [H-17, H-18](../spec/identity-and-authentication.md#what-this-does-not-cover), [H-21, H-22, H-23](../spec/authorization.md#what-this-does-not-cover), [H-38](../spec/ha-and-helm.md#what-this-does-not-cover), [H-45](../spec/api-and-ui.md#what-this-does-not-cover), [H-49](../spec/architecture.md#what-this-does-not-cover) |
| `data-token-and-secret-flow.md` | [H-2](../spec/architecture.md#what-this-does-not-cover), [H-8](../spec/repositories-and-proxy.md#what-this-does-not-cover), [H-11, H-12](../spec/storage.md#what-this-does-not-cover), [H-15, H-19, H-20](../spec/identity-and-authentication.md#what-this-does-not-cover), [H-34](../spec/formats/npm.md#what-this-does-not-cover), [H-42](../spec/operations-and-observability.md#what-this-does-not-cover), [H-44](../spec/api-and-ui.md#what-this-does-not-cover) |
| `backup-data-flow.md` | [H-46, H-47, H-48](../spec/backup-and-restore.md#what-this-does-not-cover) |
| `ci-federation.md` | [H-24 to H-30](../spec/gitlab-ci-oidc.md#what-this-does-not-cover), [H-31](../spec/formats/npm.md#what-this-does-not-cover) |
| `privilege-footprint.md` | [H-6](../spec/repositories-and-proxy.md#what-this-does-not-cover), [H-39, H-40, H-41](../spec/operations-and-observability.md#what-this-does-not-cover) |
| `input-validation-and-limits.md` | [H-1](../spec/architecture.md#what-this-does-not-cover), [H-4](../spec/repositories-and-proxy.md#what-this-does-not-cover), [H-14](../spec/storage.md#what-this-does-not-cover), [H-32](../spec/formats/npm.md#what-this-does-not-cover), [H-35, H-36](../spec/formats/nuget.md#what-this-does-not-cover), [H-43](../spec/api-and-ui.md#what-this-does-not-cover) |
| `rotation-and-propagation.md` | [H-5](../spec/repositories-and-proxy.md#what-this-does-not-cover), [H-16, H-52](../spec/identity-and-authentication.md#what-this-does-not-cover), [H-33, H-51](../spec/formats/npm.md#what-this-does-not-cover), [H-37](../spec/ha-and-helm.md#what-this-does-not-cover), [H-50](../spec/formats/nuget.md#what-this-does-not-cover) |

A new gap takes the next free number across all spec and security pages. The command is in
[developer/README.md](../developer/README.md#id-schemes).

## What this does not cover

This page describes no mechanism and records no gap of its own. It does not judge whether the
recorded items are complete. Each spec page's `Security notes` section and each review of a
perspective page decide that.
