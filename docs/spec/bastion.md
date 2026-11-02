# trove Bastion

Status: specification — not implemented. Phase 5b of the [project plan](../projektplan/BETA.md#phase-5b--trove-bastion-m13)
builds the MVP requirements of this page (milestone M13 of the [roadmap](../coverage/roadmap.md#p1-platform-milestones), moved
into the beta by the owner on 2026-11-02). The owner's decisions of 2026-10-31 and 2026-11-02 are binding and recorded as
[Q-BAS-1](#q-bas-1--the-bastion-asks-the-main-installation-and-keeps-no-copy-of-users-tokens-or-policies) to
[Q-BAS-21](#q-bas-21--internet-exposure-limits-blocking-failed-sign-in-timeouts-and-a-configurable-cache); the record is
[ADR-142](../decisions.md#adr-142--the-trove-bastion-an-internet-facing-read-only-cache-instance-in-the-beta).

This page specifies the trove Bastion: a second, smaller deployment of trove that is exposed to the internet, gives external
parties read access to the content of a main installation with the main installation's own tokens, keeps a cache of what it
served, and takes as much load as possible off the main installation. It covers:

- the deployment profile: a role of the trove image, its own Helm chart `trove-bastion`, no PostgreSQL, no web interface, no
  settings outside the chart;
- the trust between the Bastion and the main installation: the Bastion token an administrator of the main installation issues,
  what it can and cannot do, and its renewal by hand;
- the Bastion interface of the main installation: its routes and headers, its own version, the handshake, the check of a
  caller's credential and read, the marking of responses, the revocation feed and the intake of download events;
- how the Bastion serves a read without format knowledge: the URL space of the main installation mirrored, fetching with the
  caller's token, answers kept for a bounded time, shared documents for callers who read the whole repository, per-caller
  documents with conditional requests, a negative cache, a concurrency budget towards the main installation;
- the behaviour while the main installation is unreachable or speaks another interface version;
- the cache (an own bucket or a local volume per replica), the optional Valkey, and the blocking in two stages;
- audit, metrics, the PrometheusRule, the chart with its complete values, and the test suites.

| Topic | Owner |
|---|---|
| Roles, listeners, middleware chain, client address, change log, limiters, `FlightLock`, TLS reload, versioning | [architecture.md](architecture.md) R-ARCH-001, R-ARCH-005, R-ARCH-006, R-ARCH-018, R-ARCH-024, R-ARCH-025, R-ARCH-033 to R-ARCH-035 |
| Token format, storage, revocation bound, lifetime, revocation, failed-credential throttle, anonymous access | [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-022, R-AUTH-023, R-AUTH-026, R-AUTH-029, R-AUTH-037, R-AUTH-038 |
| Principals, virtual repositories, populate right, decision point, hidden existence, read shapes, revocation journal | [authorization.md](authorization.md) R-AUTHZ-002, R-AUTHZ-008, R-AUTHZ-009, R-AUTHZ-015, R-AUTHZ-016, R-AUTHZ-031 |
| The default endpoint, its hosts and credentials, response base URL | [endpoints.md](endpoints.md) R-EP-001, R-EP-003, R-EP-010 |
| Streaming download, conditional requests answered from PostgreSQL, verifying reader | [storage.md](storage.md) R-STOR-012, R-STOR-013 |
| Withheld tail, remote request path, merged metadata | [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-020, R-REPO-025, R-REPO-041 |
| Base URLs, audit recording and catalog, metric conventions, redaction, traces | [operations-and-observability.md](operations-and-observability.md) R-OPS-005, R-OPS-007, R-OPS-013 to R-OPS-016, R-OPS-018 |
| Revocation journal, main chart, monitoring rules, network policies | [ha-and-helm.md](ha-and-helm.md) R-HA-031, R-HA-042, R-HA-045, R-HA-054, R-HA-063 |
| Client setup snippets, management API conventions, UI shell | [api-and-ui.md](api-and-ui.md) R-API-030 |
| The security perspective of the Bastion | [../security/bastion.md](../security/bastion.md) |

Covers: the chained remote repository RMT-040, its download statistics reporting RMT-041 and the uncached retrieval for chained
requests RMT-044 of [remote-repositories.md](../features/remote-repositories.md), and the read-only deployment profile DIST-024
with its trust between source and instance DIST-025 of [distribution-and-lifecycle.md](../features/distribution-and-lifecycle.md).
Their priorities are raised to MVP with this page; RMT-042, RMT-043, RMT-045 and RMT-046 stay P2
([Q-BAS-47](#q-bas-47--which-catalog-entries-the-bastion-pulls-forward)). Every other requirement is `trove-only`.

Conventions on this page:

- `https://trove.example.com` is the main endpoint of the main installation *(example)*; `https://packages.example.com` is the
  base URL of a Bastion registered as `partners` *(example)*.
- `npm-all` is a virtual repository over `npm-local` and `npm-remote` (upstream `https://registry.npmjs.org/`), `nuget-all` its
  NuGet counterpart; `npm-partner` is a local repository *(examples)*.
- Fixture principals *(examples)*: the service account `acme-full`, whose policy grants `read` on `npm-all`, `nuget-all` and
  every member, and whose token is `$FULL`; the service account `acme-part`, whose policy grants `read` on `npm-all` and on
  `npm-local` with `packages.include: ["@acme/*"]` only, token `$PART`; `$NONE`, a token of a service account without grants;
  `$BST`, the Bastion token of `partners`.
- *Main installation* is the trove installation whose content a Bastion serves; *caller* is a client of the Bastion; *answer*
  is something the main installation told the Bastion about a caller (R-BAS-020).

<details>
<summary>Facts this page relies on, and how they were verified (2026-11-02)</summary>

| Fact | Verified by |
|---|---|
| A conditional `GET` and a `HEAD` of stored content are answered from PostgreSQL alone, after the authorization decision and the format's serve-time check, so a `304` costs the main installation no object-store read and tells nothing to a caller who may not read the item. The strong `ETag` of stored content is `"<sha256 hex>"`. | [storage.md](storage.md) R-STOR-012; `items.Item.ETag` in [item.go](../../internal/items/item.go) |
| The ETag of an npm document is derived from its inputs, among them the caller's readable members and whether the caller may fill each remote member; a replica answers `304` only when it would serve those bytes to that caller. | [formats/npm.md](formats/npm.md) R-NPM-013 |
| Every format route declares a `RouteKind` (`Download`, `Metadata`, `Computed`, `Upload`, `Delete`, `Admin`, `Volatile`) and a coarse action, which the dispatcher knows before the handler runs; `Volatile` (search) is passed through and never cached. | `formats.Route` and `formats.RouteKind` in [route.go](../../internal/formats/route.go) |
| The decision point classifies a subject's read of a repository as `ReadNone`, `ReadAll` or `ReadRows`; a virtual repository is `ReadNone` there because it holds no rows of its own, so a whole-repository answer for a virtual repository is computed over its members. | `authz.ReadShape` in [visible.go](../../internal/authz/visible.go) |
| The client address is the right-most address of the one header family `server.forwarded_header` selects that is not a trusted proxy; the other family is never read. A forwarded origin selects the response base URL only when the peer is a trusted proxy and the origin is an entry of `server.additional_base_urls`. | [clientaddr.go](../../internal/httpserver/clientaddr.go); [architecture.md](architecture.md) R-ARCH-006, [operations-and-observability.md](operations-and-observability.md) R-OPS-005 |
| The revocation journal is the set of transactional audit events whose registry entry declares `journal: revocation`; they are rows of `audit_event`, keyed by `(occurred_at, id)`, with `occurred_at` the transaction's `now()`. | [00006_audit.sql](../../internal/meta/postgres/migrations/00006_audit.sql), `audit.JournalRevocation` in [registry.go](../../internal/audit/registry.go); [ha-and-helm.md](ha-and-helm.md) R-HA-054 |
| npm's `replace-registry-host` (default `npmjs`) replaces only `registry.npmjs.org` in a lockfile's `resolved` URLs; `always` replaces every host with the configured registry. | `npm help config` of npm 11.20.0 |

Not verified on this page, and this is the gap: the annotation names of HAProxy Ingress for a per-address request and connection
limit and for URI hashing (R-BAS-033, R-BAS-038) come from the controller's documentation as the development agent remembers it;
the chart test of R-BAS-045 pins them against the controller version of the local profile before the chart relies on them.
Whether `dotnet restore` sends every request of one restore with the same credential through one source is assumed from the
NuGet client's source-credential model and is checked by the e2e suite (R-BAS-049).

</details>

## Overview

```text
   external parties: npm, pnpm, yarn, dotnet, curl           the internet, floods included
                │  https://packages.example.com
   ┌────────────▼──────────────────────────────┐
   │ HAProxy Ingress of the Bastion            │  stage 1: coarse per-address limit (optional, wider, R-BAS-038)
   │ TLS Secret <fullname>-tls                 │  routing by path for local volumes (optional, R-BAS-033)
   └────────────┬──────────────────────────────┘
                │  Service <fullname>:8443 (TLS)
   ┌────────────▼───────────┐   ┌────────────────────────┐
   │ Bastion replica 1      │   │ Bastion replica n      │   trove serve --role bastion (R-BAS-001)
   │ stage 2: block list,   │   │ …                      │   answers, feed cursor, audit buffer,
   │ limits, failed-auth    │   │                        │   document copies (memory)
   └──┬────────┬────────┬───┘   └──┬─────────────────────┘
      │        │        └──────────┴───► cache backend: own bucket (shared)  |  local volume per replica (R-BAS-030)
      │        └───────────────────────► Valkey, optional: block list, counters, fetch locks, signed answers (R-BAS-034)
      │  https://trove.example.com, the main endpoint (default endpoint of the main installation)
      │  Trove-Bastion-Authorization: Bearer trv_bst_…   Authorization: <the caller's own credential>
      │  X-Forwarded-For/-Proto/-Host/-Port and Forwarded: the client address and the Bastion's host
   ┌──▼────────────────────────────────────────────────────────────────────────────┐
   │ main installation: its ingress → web replicas, default endpoint               │
   │   /api/<format>/…, /files/…   decided as the caller's own request,            │
   │                               marked (R-BAS-013), writes refused (R-BAS-014)  │
   │   /api/v1/bastion/handshake | check | feed | downloads   (R-BAS-008)          │
   │   PostgreSQL, Valkey, object store: never reached by the Bastion              │
   └───────────────────────────────────────────────────────────────────────────────┘
```

A read at the Bastion goes one of three ways. A **hit** is served from the cache after an answer of the main installation that
the caller may read it, kept for `bastion.answers.period`. A **check** asks the main installation about the caller, by a
conditional request through the real route or through the check operation, and serves the cached bytes on a yes. A **miss** is
fetched from the main installation with the caller's own credential; the main installation decides and serves it as it would
the caller's own request, marks the response, and the Bastion keeps what may be shared. The Bastion holds no users, tokens or
policies and knows no package format: every decision is the main installation's, and every protocol byte it serves was produced
by the main installation, except the few answers of its own of R-BAS-029.

## Roles and trust boundaries

| Party | Trusted with | Not trusted with | Where the boundary is enforced |
|---|---|---|---|
| Caller (anyone on the internet) | its own credential, and what the main installation lets that credential read | forwarded headers, any decision, any write | the Bastion's ingress and its stage 2 (R-BAS-035 to R-BAS-037); the main installation's decision on every answer |
| HAProxy Ingress of the Bastion | the client address it writes, when its addresses are in the Bastion's `server.trusted_proxies` | anything else | R-BAS-019, [architecture.md](architecture.md) R-ARCH-006 |
| Bastion replica | the Bastion token, the signing key, the cached bytes, the client address it reports | users, tokens, policies (it holds none); every write to the main installation except download events | the Bastion interface (R-BAS-005, R-BAS-008) |
| Valkey of the Bastion | block list, counters, fetch locks, answers it stores but cannot forge (signed) | granting anything: a forged or altered answer fails its signature | R-BAS-034 |
| Cache backend (own bucket or volume) | bytes and signed entries | the main installation's object store, which the Bastion cannot read | R-BAS-030, R-BAS-032 |
| Main installation | every decision about a caller, every rendered byte, the audit trail | — | — |
| Administrator of the main installation (`endpoints:write`) | issuing and revoking Bastion tokens | — | R-BAS-004, R-BAS-007 |

```text
   caller ──(1) TLS, own credential──► Bastion ──(2) TLS, Bastion token + caller's credential──► main installation
      ▲                                  │  ▲                                                       │
      │  decided by the main             │  └──(3) answers, marked responses, feed entries ─────────┘
      └──installation, replayed ─────────┘
   (1) crosses the internet boundary; (2) and (3) cross the trust boundary between a DMZ instance and the main installation.
```

## Deployment profile

### R-BAS-001 · The Bastion: a role of the trove image behind its own chart
Priority: **MVP** · Covers: DIST-024

- The Bastion is the role `bastion` of the one `trove` image (INV-1, INV-2): `trove serve --role bastion`
  ([Q-BAS-22](#q-bas-22--the-bastion-is-a-role-of-the-trove-image)). The role enables the Bastion's client listener, its ops
  listener, the Bastion engine of this page and, when configured, the Valkey client. It enables no other subsystem: no
  PostgreSQL pool, no object-store client for the main installation's blobs, no job runner, no format plug-in route table, no
  management API, no UI, no login, no session, no remote repository.
- The role is deployed only by the chart `trove-bastion` in `deploy/helm/trove-bastion` (R-BAS-045), never by the chart
  `trove`, and the chart `trove-bastion` renders no other role. Several Bastions in front of one main installation are several
  releases of that chart, each with a registration of its own (R-BAS-004).
- The configuration schema of the role is its own: `server.listen`, `server.ops_listen`, `server.base_url`,
  `server.trusted_proxies`, `server.forwarded_header`, `server.max_in_flight`, `server.tls`, the section `bastion` of this page,
  `valkey`, `log`, `tracing` and `runtime`. A key of another section (`database`, `storage`, `auth`, `authz`, `ci`, `backup`,
  `audit`, `server.endpoints`, `server.additional_base_urls`, …) stops startup with an error that names the key and the role.
- Horizontal replicas: every Bastion replica is identical and interchangeable (INV-2); a request may reach any replica, and
  nothing a request needs is held only by the replica an earlier request reached (INV-18): answers, document copies and a
  local cache volume are caches whose loss costs performance only. The Bastion has no PostgreSQL; what it must not lose lives
  at the main installation, and what it may lose lives in its cache backend and, optionally, Valkey.

```yaml
server:
  listen: ":8443"                       # default (proposed) for the role bastion
  ops_listen: ":8081"                   # default (proposed)
  base_url: ""                          # required; example: https://packages.example.com
  trusted_proxies: []                   # default (proposed); example: ["10.244.0.0/16"] (the Bastion's ingress pods)
  forwarded_header: x-forwarded-for     # default (proposed): x-forwarded-for | forwarded (R-ARCH-006)
  max_in_flight: 1024                   # default (proposed), per replica
```

Acceptance criteria:

- `trove serve --role bastion` with a file that holds `database.host` stops with an error naming `database.host` and the role
  `bastion`; with the keys above, `bastion.main.url` and the token file it starts, and `ss -ltn` in its pod shows `:8443` and
  `:8081` only.
- A Bastion pod opens no connection to any PostgreSQL port and makes no request to the main installation's bucket during the
  whole e2e suite (NetworkPolicy log and the object store's access log).
- `GET /api/v1/system/ping`, `GET /ui/` and `GET /auth/oidc/corp/start` on the Bastion answer `404` without a request to the
  main installation (R-BAS-018).

### R-BAS-002 · Configuration only through the chart
Priority: **MVP** · Covers: DIST-024

- Every setting of a Bastion is a value of its chart: the main installation and the Bastion token, the cache, the periods, the
  limits, the blocking, the trusted proxies, Valkey, TLS and monitoring (owner,
  [Q-BAS-15](#q-bas-15--the-bastion-is-configured-only-through-its-chart)). The chart renders them into
  `/etc/trove/trove.yaml` and the mounted Secrets (R-BAS-046). The Bastion has no runtime settings, no settings API and no
  setting read from the main installation; a change of a value is a rollout.
- What the Bastion learns from the main installation is state of the main installation, not configuration of the Bastion:
  whether anonymous access is on, whether downloads are recorded, the main installation's interface version (R-BAS-010).
- The two exceptions to "a change is a rollout" are file reloads of material the chart mounts: the Bastion token (R-BAS-006)
  and the TLS material (R-BAS-044), both picked up without a restart.

Acceptance criteria:

- `grep` of the Bastion's code finds no write of a `bastion.*` value at runtime; the chart test renders every value of
  R-BAS-046 into the configuration file or a Secret mount, and a value without a rendering target fails the test.
- Changing `config.bastion.answers.period` with `helm upgrade` rolls the pods (the configuration checksum annotation changes);
  replacing the token Secret's content rolls nothing and the new token is in use within `bastion.main.token_reload_interval`.

### R-BAS-003 · What the Bastion serves: the package clients' read paths, no web interface
Priority: **MVP** · Covers: DIST-024

- The Bastion serves package clients and raw downloads with tokens only (owner,
  [Q-BAS-14](#q-bas-14--no-web-interface-at-the-bastion)): the path families `/api/<format>/…` (every path below `/api/`
  except `/api/v1/…`) and `/files/…` of INV-6, mirrored from the main installation (R-BAS-018). Every other path answers `404`
  from the Bastion itself (R-BAS-029): `/`, `/ui/…`, `/auth/…`, `/api/v1/…`, `/-/…`, `/v2/…`, `/.well-known/…`.
- There is no login, no session, no cookie and no browser flow. A `Cookie` header is ignored and never forwarded. A session
  token never reaches the Bastion from trove, because trove never sets a cookie for the Bastion's host.
- The ops listener serves `/healthz`, `/readyz` and `/metrics` as on every trove role ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-001, R-OPS-006).

Acceptance criteria:

- A table test sends every row of INV-6's path table to the Bastion: only `/api/<format>/…` and `/files/…` reach the read
  flow; the others answer `404` with `bastion_not_served` and the counting double of the main installation records no request.
- A request with `Cookie: __Host-trv_session=…` and no other credential is treated as anonymous, and the request the Bastion
  forwards carries no `Cookie`.

## Trust between the Bastion and the main installation

### R-BAS-004 · Bastion registrations and the Bastion token on the main installation
Priority: **MVP** · Covers: DIST-025

- **Registration.** A Bastion is registered on the main installation as a principal of the kind `bastion`, reference
  `bastion:<name>`, name `^[a-z][a-z0-9-]{0,19}$` ([authorization.md](authorization.md) R-AUTHZ-002). It holds no grants, no
  roles, no group memberships and no package ownership: a policy entry, a role binding, an owner record or a membership that
  names it answers `422 principal_not_allowed`. It is never part of `authenticated` (R-AUTHZ-030). At most
  `bastions.max_registrations` registrations exist. A registration can be disabled, which refuses its tokens like a disabled
  service account's ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-020), and deleted, which revokes
  them.
- **Bastion token.** The Bastion's credential is a token of the type `bst`, prefix `trv_bst_`, issued by an administrator of
  the main installation (owner, [Q-BAS-3](#q-bas-3--the-bastions-credential-is-a-token-issued-by-an-administrator-of-the-main-installation),
  [Q-BAS-23](#q-bas-23--the-bastion-token-is-a-token-type-of-its-own-on-a-bastion-principal)). It follows the format, storage,
  validation, caching and revocation bound of R-AUTH-022 and R-AUTH-023 and the revocation of R-AUTH-029; its owner principal
  is the registration. It has no scope: what it may do is fixed by its type (R-BAS-005). It always expires: `ttlSeconds` is at
  most `auth.tokens.bst.max_ttl`, defaulting to `auth.tokens.bst.default_ttl`. At most `auth.tokens.max_active_per_principal.bst`
  unrevoked tokens exist per registration, so an overlap (R-BAS-006) fits and a forgotten token does not accumulate.
- **API** (management API conventions of [api-and-ui.md](api-and-ui.md); capability in brackets,
  [Q-BAS-24](#q-bas-24--bastion-registrations-are-managed-with-the-endpoint-capabilities)):

| Operation | Capability | Answer |
|---|---|---|
| `POST /api/v1/bastions` `{"name", "description"}` | `endpoints:write` | `201` with the registration |
| `GET /api/v1/bastions`, `GET /api/v1/bastions/{name}` | `endpoints:read` | name, description, `enabled`, its tokens (ID, hint, `expiresAt`, `createdAt`, `lastUsedAt`), and the last contact of R-BAS-010: time, client address, Bastion release, interface version, reported base URL, replica |
| `PATCH /api/v1/bastions/{name}` `{"description", "enabled"}` | `endpoints:write` | `200` |
| `DELETE /api/v1/bastions/{name}` | `endpoints:write` | `204`; revokes its tokens (`token.revoked`, journaled) |
| `POST /api/v1/bastions/{name}/tokens` `{"description", "ttlSeconds"}` | `endpoints:write` | `201` with the token secret, once (R-AUTH-022) |
| `DELETE /api/v1/bastions/{name}/tokens/{id}` | `endpoints:write` or `tokens:revoke` | `204`; `token.revoked`, journaled (R-HA-054) |

- `trove admin bastion create <name>`, `trove admin bastion token create <name> --ttl <duration>` and
  `trove admin bastion token revoke <name> <id>` do the same from a `web` pod (R-AUTH-015's pattern). Every operation writes its
  audit event: `bastion.created`, `bastion.updated`, `bastion.deleted`, and `token.created` / `token.revoked` with
  `resource_type: bastion`.

```yaml
auth:
  tokens:
    bst:
      default_ttl: 8760h               # default (proposed): one year
      max_ttl: 9480h                   # default (proposed): one year and a month, so a yearly renewal fits its overlap
    max_active_per_principal:
      bst: 3                           # default (proposed)
bastions:                              # the main installation's section (the Bastion's own is bastion)
  max_registrations: 16                # default (proposed); bounds the bastion label of R-BAS-008's metrics
```

Acceptance criteria:

- `POST /api/v1/bastions {"name": "partners"}` then `POST /api/v1/bastions/partners/tokens {"ttlSeconds": 31536000}` returns a
  57-character `trv_bst_` token that passes R-AUTH-022's checksum; `ttlSeconds` above `max_ttl` answers `400 invalid_ttl`, and a
  fourth unrevoked token answers `409 too_many_tokens`.
- A policy entry for `bastion:partners` and a role binding of `admin` to it answer `422 principal_not_allowed`.
- A caller with `endpoints:read` only gets `403` on the token creation; `tokens:revoke` alone may revoke a Bastion token.

### R-BAS-005 · What the Bastion token can do
Priority: **MVP** · Covers: DIST-025

The Bastion token identifies the Bastion and may only have tokens checked; it reads no package (owner,
[Q-BAS-7](#q-bas-7--the-bastion-fetches-with-the-callers-token-and-its-own-token-reads-no-package)).

- **Where it is accepted.** Only on the Bastion interface of the default endpoint: as `Authorization: Bearer` on
  `/api/v1/bastion/…`, and as `Trove-Bastion-Authorization: Bearer` on a request the Bastion forwards (R-BAS-008). Every other
  route of every endpoint refuses the type `bst` in `Authorization`, as Basic password and as API key with the `401
  invalid_token` of an unknown token, before any database lookup ([endpoints.md](endpoints.md) R-EP-010 step 2). `bst` is not a
  valid entry of an endpoint's `auth.token_types`: it stops startup and names the key. Further endpoints do not serve the
  Bastion interface.
- **What it grants.** Nothing in the decision point: it is never a subject of a read, a write or a management operation. On
  the interface it allows: the handshake, the check of a caller's credential and read (which tells the Bastion what the caller
  could find out by using its credential itself), reading the revocation feed (token and principal IDs, repository keys), and
  submitting download events (R-BAS-016). It cannot issue tokens, change a Secret, read a package, write content, or read the
  audit trail.
- **Forwarded requests** are decided on the caller's credential alone. The Bastion token on a forwarded request only makes the
  main installation add the marking headers of R-BAS-013 and refuse writes (R-BAS-014); a forwarded request without a caller
  credential is anonymous, whatever the Bastion token is.

Acceptance criteria:

- `$BST` as `Authorization: Bearer`, as Basic password and as `X-NuGet-ApiKey` on `/api/npm/npm-all/lodash`,
  `/files/npm-local/x` and `/api/v1/repositories` answers `401 invalid_token`, and the token table records no query for it.
- `GET /api/npm/npm-all/@acme%2fsdk` with `Trove-Bastion-Authorization: Bearer $BST` and no `Authorization` answers exactly what
  an anonymous request answers, plus the marking headers.
- `server.endpoints.default.auth.token_types: [pat, bst]` stops startup naming the key.

### R-BAS-006 · Renewing the Bastion token by hand with an overlap
Priority: **MVP** · Covers: DIST-025

The renewal is manual (owner, [Q-BAS-19](#q-bas-19--the-bastion-token-is-renewed-by-hand-with-an-overlap)):

1. An administrator issues a second token for the registration before the first expires (R-BAS-004).
2. The new token reaches the Bastion's Secret (`secrets.mainToken`) through GitOps; the chart never writes it, and the Bastion
   holds no right to write a Secret or to issue a token (its ServiceAccount has no Kubernetes API access, R-BAS-045).
3. Every replica re-reads the token file every `bastion.main.token_reload_interval` and on a change of the file, and uses the
   new token from its next request to the main installation; requests in flight finish with the old one. No restart.
4. The administrator revokes the old token once the screen of R-BAS-007 shows the new token's `lastUsedAt` from every replica's
   contact. A replica that still uses a revoked token gets `401 bastion_token_invalid` and treats the main installation as
   unreachable (R-BAS-027), `trove_bastion_main_token_refused` is 1 and `TroveBastionTokenRefused` fires (R-BAS-041).

- **Remaining lifetime.** The handshake returns the expiry of the token in use (R-BAS-010). The Bastion exports
  `trove_bastion_token_remaining_seconds` and logs a warning at start and then once a day while the remaining lifetime is below
  `bastion.token.warn_before`, naming the registration, the token hint and the expiry. The PrometheusRule warns from the same
  threshold, read from the gauge `trove_bastion_token_warn_before_seconds` (R-BAS-041).
- An unreadable or malformed token file (wrong prefix, failed checksum) keeps the previous token in use, logs an error and
  counts `trove_bastion_token_reloads_total{outcome="refused"}`; at start it stops startup.

```yaml
bastion:
  main:
    token_reload_interval: 10s          # default (proposed)
  token:
    warn_before: 720h                   # default (proposed): 30 days (owner's proposal)
```

Acceptance criteria:

- With two unrevoked tokens, replacing the Secret's value moves every replica to the new token within 70 s (the kubelet's
  Secret sync plus `token_reload_interval`), shown by `lastUsedAt` of both tokens; revoking the old one afterwards causes no
  failed request in a concurrent `npm ci` loop.
- A token that expires in 20 days makes the Bastion log `bastion token expires soon` at start, sets
  `trove_bastion_token_remaining_seconds` below 1,728,001, and fires `TroveBastionTokenExpiresSoon` in `promtool test rules`.
- Revoking the token in use makes every replica report `trove_bastion_main_token_refused 1` within `feed.poll_interval` plus
  5 s.

### R-BAS-007 · Bastions in the main installation's screen, client setup and manual
Priority: **MVP** · Covers: trove-only

- **Screen.** Administration → Bastions in the main installation's UI (capabilities of R-BAS-004): the list of registrations
  with the last contact, the interface version and release each replica reported, the token expiries with a warning mark below
  30 days, and the actions create, disable, delete, issue token (the secret shown once, as for every token) and revoke token.
  The screen uses only the operations of R-BAS-004 (INV-17).
- **Client setup.** `GET /api/v1/repositories/{repoKey}/client-setup` ([api-and-ui.md](api-and-ui.md) R-API-030) accepts
  `bastion=<name>`: the snippets are rendered on the base URL the Bastion reported at its last handshake, provided it is an
  entry of `server.additional_base_urls` (else `409 bastion_base_url_unknown`), and carry no publish snippets, since the Bastion
  is read-only. The guide of each format (R-UI-049) offers the Bastion as a target when a registration has reported a base URL.
  For npm, the configure snippet sets `replace-registry-host=always`, so that a lockfile written against another host resolves
  through the Bastion.
- **Manual.** The admin manual gains `docs/manual/admin/bastion.md`: register a Bastion, issue its token, the chart values of a
  minimal and of an HA install, the settings on the main installation (`server.trusted_proxies`,
  `server.additional_base_urls`), verify with the handshake metric, renew the token, and what each alert of R-BAS-041 means.

Acceptance criteria:

- The browser test creates `partners`, issues a token, copies it once, and sees the last contact appear after the Bastion of
  the e2e cluster starts; a token expiring in 20 days shows the warning mark.
- `client-setup?client=npm&bastion=partners` on `npm-all` returns a registry URL
  `https://packages.example.com/api/npm/npm-all/`, `replace-registry-host=always`, and no `publish` tab.

## The Bastion interface of the main installation

### R-BAS-008 · Interface routes, recognition and headers
Priority: **MVP** · Covers: RMT-040

The main installation's side of the Bastion is one interface on its default endpoint
([Q-BAS-25](#q-bas-25--the-interface-lives-under-apiv1bastion-on-the-main-endpoint)). The Bastion connects to the main endpoint
(owner, [Q-BAS-4](#q-bas-4--the-bastion-connects-to-the-main-endpoint-and-replaces-the-customer-endpoint)), through the main
installation's ingress or its Service.

| Route | Purpose | Requirement |
|---|---|---|
| `GET /api/v1/bastion/handshake` | interface version, the Bastion's identity and token expiry, the main installation's state the Bastion needs | R-BAS-010 |
| `POST /api/v1/bastion/check` | a caller's credential and its read shape on repositories, with the answer's validity | R-BAS-011 |
| `GET /api/v1/bastion/feed` | the revocation feed | R-BAS-015 |
| `POST /api/v1/bastion/downloads` | download events served from the Bastion's cache | R-BAS-016 |
| every `/api/<format>/…` and `/files/…` route | a forwarded read, decided as the caller's own request and marked | R-BAS-013, R-BAS-014 |

- **Headers of every request from the Bastion:** `Trove-Bastion-Interface: <n>` (R-BAS-009); `User-Agent:
  trove-bastion/<release>`; `Trove-Bastion-Replica: <pod name>`; W3C `traceparent` (R-BAS-042). On the interface routes the
  Bastion token is `Authorization: Bearer`. On a forwarded request it is `Trove-Bastion-Authorization: Bearer`, the caller's own
  credential stays in `Authorization` exactly as the caller sent it, and the forwarded headers of R-BAS-017 carry the client
  address and the Bastion's host.
- **Recognition.** The main installation treats a request as a Bastion request only when `Trove-Bastion-Authorization` (or, on
  the interface routes, `Authorization`) carries a valid `bst` token of an enabled registration. A request with
  `Trove-Bastion-*` headers but no valid Bastion token is served as an ordinary request: the headers are ignored and no
  marking is added. A request whose Bastion token is invalid answers `401 bastion_token_invalid`; one whose interface version
  differs answers `400 bastion_interface_mismatch` (R-BAS-009). Both are problem details of the management API, also on a
  package route, because they concern the Bastion, never the caller; the Bastion never passes them on (R-BAS-027).
- `Trove-Bastion-Authorization` is redacted like `Authorization` in every log, span and support bundle (R-OPS-013), and the
  main installation never echoes a `Trove-Bastion-*` request header.
- The operations are in the OpenAPI document under the tag `bastion`, marked `x-trove-bastion-interface: <n>`; they are not
  part of the management API's v1 promise but of the interface version (R-ARCH-025).
- Main side metrics: `trove_bastion_interface_requests_total{bastion, operation, outcome}` and
  `trove_bastion_forwarded_requests_total{bastion, class}` (`class` of R-BAS-013); `bastion` is the registration name, bounded
  by `bastions.max_registrations`.

Acceptance criteria:

- A forwarded `GET` with `Trove-Bastion-Authorization: Bearer <a revoked bst token>` answers `401 bastion_token_invalid` as
  `application/problem+json`; with `Trove-Bastion-Authorization: Bearer $PAT` (a valid token of another type) the request is
  served as an ordinary one without marking headers.
- The request log of the main installation shows `Trove-Bastion-Authorization` as `[REDACTED]`.

### R-BAS-009 · The interface version
Priority: **MVP** · Covers: trove-only

The Bastion interface has a version of its own, separate from the release number (owner,
[Q-BAS-20](#q-bas-20--the-bastion-interface-has-a-version-of-its-own-equal-on-both-sides)).

- The version is a positive integer, `bastion.InterfaceVersion` in code, starting at 1. It is raised by every change of the
  interface: a route, a request or response field, a header, a marking value, a feed entry kind, or the meaning of any of them
  ([Q-BAS-26](#q-bas-26--every-change-of-the-interface-raises-its-version)). Nothing is added "within" a version: equality is the
  rule, so neither side has to read a field it does not know.
- Main installation and Bastion talk only when both speak the same version. Every request of the Bastion carries
  `Trove-Bastion-Interface`; every answer of the main installation to a Bastion request carries its own version in the same
  header. A request with another version, or without the header, answers `400 bastion_interface_mismatch` with the problem
  member `"interfaceVersion": <main's>`, and is served in no other way.
- On a mismatch the Bastion treats the main installation as unreachable (R-BAS-027: the grace period, then nothing) and raises
  a Prometheus alert through `trove_bastion_interface_mismatch` and the rule of its chart (R-BAS-028, R-BAS-041).
- **Guard.** `internal/bastion/iface/testdata/interface.txt` holds the version on its first line and the interface surface
  (routes, headers, marking values, feed kinds, the JSON schema of every body) below it.
  `TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion` fails when the generated surface differs from the file;
  regenerating (`go test ./internal/bastion/iface -run TestR_BAS_009 -update-interface`) refuses to write a changed surface
  under an unchanged version.
- **Upgrades.** A release that raises the version is announced in its release notes, and main installation and Bastions are
  upgraded together, within the grace period of the Bastions (R-BAS-027). R-ARCH-025's table holds the interface as a surface
  of its own.

Acceptance criteria:

- An in-process main installation built with `InterfaceVersion = 2` and a Bastion with 1: the handshake answers `400
  bastion_interface_mismatch` with `"interfaceVersion": 2`, the Bastion sets `trove_bastion_interface_mismatch 1` and
  `trove_bastion_main_interface_version 2`, serves cached artifacts to callers with live answers for the grace period, and
  `503` afterwards.
- Adding a field to the check answer without raising the version fails `TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion`,
  and `-update-interface` refuses to regenerate.

### R-BAS-010 · Handshake
Priority: **MVP** · Covers: RMT-040

`GET /api/v1/bastion/handshake`, with the Bastion token. The Bastion calls it at start, every `bastion.main.handshake_interval`,
after every reconnect, and after a feed entry of the kind `settings` (R-BAS-015). It sends its own host as the forwarded host
(R-BAS-017).

```json
{
  "interfaceVersion": 1,
  "bastion": {"name": "partners", "tokenId": "0192f1c4-…", "tokenExpiresAt": "2027-11-02T00:00:00Z"},
  "installation": {"baseUrl": "https://trove.example.com", "release": "0.14.0"},
  "responseBaseUrl": "https://packages.example.com",
  "anonymousAccess": false,
  "recordDownloads": true,
  "feed": {"cursor": "MjAyNi0xMS0wMlQxMDowMDowMFp8MDE5MmYx…"},
  "serverTime": "2026-11-02T10:00:00.123Z"
}
```

*(example answer)*

- `responseBaseUrl` is the response base URL the main installation derived for this request (R-OPS-005, R-EP-003). The Bastion
  requires it to equal its own `server.base_url`; otherwise it logs `bastion base URL not honoured by the main installation`,
  sets `trove_bastion_base_url_mismatch 1` and treats the main installation as unreachable, so that no client ever receives a
  document with the main installation's URLs from the Bastion.
- `anonymousAccess` is the effective anonymous switch of R-AUTH-037 (the YAML ceiling and the runtime setting). `recordDownloads`
  is `audit.record_downloads` (R-BAS-039). `feed.cursor` is the feed position at the time of the answer (R-BAS-015).
- The main installation records the last contact of the registration (time, client address, `release` from `User-Agent`,
  interface version, `responseBaseUrl`, replica) at most once a minute per replica, and writes `bastion.contact` (asynchronous)
  when one of the reported values changes.

Acceptance criteria:

- With `https://packages.example.com` missing from `server.additional_base_urls`, the handshake answers
  `"responseBaseUrl": "https://trove.example.com"`, the Bastion sets `trove_bastion_base_url_mismatch 1` and answers a cold
  `npm view` with `503 main_unreachable`.
- `GET /api/v1/bastions/partners` shows the replica names and the release of every Bastion replica within one
  `handshake_interval`.

### R-BAS-011 · Credential and read check
Priority: **MVP** · Covers: RMT-040

`POST /api/v1/bastion/check`, with the Bastion token. The Bastion asks about a caller it has no usable answer for, when the
content is cached and no fetch is needed (R-BAS-022, R-BAS-023).

```json
{"credential": "trv_pat_3kQ9…", "clientAddress": "203.0.113.7", "repositories": ["npm-all", "npm-partner"]}
```

```json
{
  "subject": {"kind": "token", "tokenId": "0192…", "principalId": "0191…", "expiresAt": "2027-01-31T00:00:00Z"},
  "repositories": {
    "npm-all":     {"read": "full"},
    "npm-partner": {"read": "none", "status": 404}
  }
}
```

*(example request and answer)*

- `credential` is the token string the caller presented (R-BAS-019), or `null` for an anonymous caller. The main installation
  runs R-AUTH-023's validation on it, including the failed-credential throttle of R-AUTH-038 for `clientAddress`, and records
  `token.rejected` with `source_ip = clientAddress` as for a direct request.
- `subject.kind`: `token` (valid; `expiresAt` is the token's expiry), `anonymous` (no credential; anonymous access on),
  `anonymous_refused` (no credential; anonymous access off), `invalid` (malformed, unknown, expired, revoked, owner disabled:
  R-AUTH-002 does not tell which, and neither does this answer), or `throttled` with `retryAfterSeconds` (the address is in
  R-AUTH-038's throttled mode and the credential is not exempt).
- `repositories`: at most `bastions.check.max_repositories` keys; for each, the read shape of R-BAS-012 and, for `none`, the
  status the caller would receive for a read of the repository (`401` for an anonymous caller, otherwise `404` or `403` by
  R-AUTHZ-016 and R-AUTHZ-017). An unknown key answers `none` with the status of a missing repository for that caller, so the
  check reveals no more than the caller's own request would.
- The check writes no `authz.denied` events: it asks about repositories, not about a request a client made; the request that
  follows a `none` is the Bastion's own refusal (R-BAS-029), and its audit is the request log of the Bastion.
- An answer has no lifetime of its own: the Bastion keeps it for its period (R-BAS-020), never beyond `expiresAt`.

```yaml
bastions:                              # the main installation's section (the Bastion's own is bastion)
  check:
    max_repositories: 16               # default (proposed)
```

Acceptance criteria:

- `$FULL` on `["npm-all", "npm-partner"]` answers `full` and `none` with `404`; `$PART` on `["npm-all"]` answers `partial`;
  a revoked token answers `"kind": "invalid"`; `null` with anonymous access off answers `anonymous_refused`.
- 400 checks with random well-formed tokens for one `clientAddress` within a minute: from the 301st on the answer is
  `throttled` (R-AUTH-038's default), and a direct request from that address is throttled too.

### R-BAS-012 · Read shapes: what "reads the whole repository" means
Priority: **MVP** · Covers: RMT-040

The read shape of a subject on an addressed repository is computed by the decision point
([Q-BAS-28](#q-bas-28--what-reads-the-whole-repository-means)):

- `full`: the subject may read every package and every version reachable through the repository, and every response of a read
  route of that repository is the same for it as for every other `full` subject. For a local or remote repository: `ReadAll` of
  `authz.ReadScoper` (an unrestricted read grant or `content:bypass`, a token scope that does not narrow, the endpoint's
  exposure `all`), and for a remote repository additionally the populate right of R-AUTHZ-008. For a virtual repository: the
  gate `read` on the repository itself (R-AUTHZ-009 rule 1) and `full` on every member, recursively.
- `none`: the subject may read nothing through the repository: no gate, `ReadNone` on every member, an anonymous subject on a
  repository with `requireAuthentication` or anonymous access off, an unknown key.
- `partial`: every other case.

The shape is the answer to the owner's "the main installation confirmed a full read of the repository" (Q-BAS-18): a `full`
answer lets the Bastion share a marked document with that caller and serve it every cached artifact of the repository without
a per-artifact question (R-BAS-022, R-BAS-023). Content that the format refuses at serve time (a tombstoned version, a blocked
version) is not a matter of the shape; R-BAS-022's revalidation and R-BAS-015's content entries cover it.

Acceptance criteria:

- `TestR_BAS_012_ShapeAgreesWithTheDecisionPoint`: over every subject, repository, package and version of the R-AUTHZ fixture,
  a `full` shape never meets a refused read, and a `none` shape never meets an allowed one.
- `acme-full` without the populate right on `npm-remote` has shape `partial` on `npm-all`; with it, `full`.
- An exposure rule on the default endpoint makes every shape on the exposed repository `partial` at most.

### R-BAS-013 · Marking of responses to Bastion requests
Priority: **MVP** · Covers: RMT-040, RMT-044

A forwarded request is served by the main installation's own route as the caller's own request: authentication, the
decision point, hidden existence, the format's serve-time checks, resolution, the populate right of a remote member (a request
through a Bastion fills a remote member exactly when the caller's own request would, RMT-044), rendering on the Bastion's host,
download statistics and `artifact.downloaded` of the main installation. The core adds the marking headers to the response of a
recognized Bastion request; no format code is involved, because the dispatcher knows the route's `RouteKind`
([Q-BAS-27](#q-bas-27--a-cached-artifact-is-checked-through-the-real-route)):

| Header | Value |
|---|---|
| `Trove-Bastion-Class` | `artifact` for a `200` or `304` of a `Download` route or of a file under `/files/`; `shared` for a `200` or `304` of a `Metadata` or `Computed` route (or a `/files/` folder listing) when the caller's shape on the addressed repository is `full`; `private` for the same routes otherwise; `absent` for a `404` of a read route when the item does not exist in the repository for any subject (no member holds it, the remote's negative cache or upstream says so); `pass` for every other answer (`Volatile` and other routes, errors, redirects) |
| `Trove-Bastion-Repository` | the addressed repository key |
| `Trove-Bastion-Read` | the caller's shape on the addressed repository (R-BAS-012) |
| `Trove-Bastion-Subject` | `token=<token ID>; principal=<principal ID>; expires=<unix seconds>`, or `anonymous`, or `invalid` on a `401` for a rejected credential |
| `Trove-Bastion-Interface` | the main installation's interface version |

- `absent` is set only by the remote and virtual engines and the local lookup when they know the item is missing for every
  subject; a refusal by the decision point, a hidden item and a serve-time refusal are `pass`, so a negative entry never stands
  for a hidden item (R-BAS-024).
- `shared` follows from the shape and the route kind alone: a document of a `full` subject is the unfiltered rendition for the
  Bastion's host, and its ETag is the same for every `full` subject (R-NPM-013's inputs). A format whose document varies by
  anything but the repository's content and the request headers it names in `Vary` must render such a route as `Volatile`; the
  conformance kit gains a check that sends one document request as two `full` subjects and compares the bytes.
- The headers are added only to Bastion requests and stripped from every other response, so an ordinary caller never learns
  its own shape.

Acceptance criteria:

- Through the in-process main installation: a tarball `GET` with `$FULL` carries `Trove-Bastion-Class: artifact`,
  `Trove-Bastion-Read: full`; the package document with `$FULL` carries `shared` and with `$PART` `private`; `-/v1/search`
  carries `pass`; a missing name in `npm-local` with `$FULL` carries `absent`, and a name `$PART` may not read carries `pass`
  with the same `404` body as a missing one.
- The kit check passes for npm and NuGet: two `full` subjects receive byte-identical documents and the same ETag on every
  `Metadata` and `Computed` route of the fixture.
- No response to a request without a valid Bastion token carries a `Trove-Bastion-` header (contract test over the route table).

### R-BAS-014 · Writes through a Bastion are refused by the main installation
Priority: **MVP** · Covers: trove-only

The Bastion is read-only for package content (owner, [Q-BAS-2](#q-bas-2--the-bastion-is-read-only-for-package-content)). It
answers a write in the client's protocol shape and points at the main installation, without format knowledge of its own
([Q-BAS-38](#q-bas-38--writes-are-refused-by-the-main-installation-from-a-bodiless-probe)):

- A request with a method other than `GET` and `HEAD` is first sent to the main installation as a **probe**: the same method,
  path and query, the request headers of R-BAS-021, `Content-Length: 0`, no body, no caller credential, and
  `Trove-Bastion-Probe: 1`. The Bastion reads none of the caller's body while it waits.
- The main installation's middleware answers a Bastion probe after routing and before any handler or body read:
  - a route whose `RouteKind` is `Upload`, `Delete` or `Admin`, or whose coarse action is not `read`: `403` with the problem
    code `bastion_read_only` in the format's error shape (R-ARCH-023), whose detail names the main installation's URL of the
    addressed repository: `This is a read-only Bastion of https://trove.example.com; publish to
    https://trove.example.com/api/npm/npm-all/`;
  - a read route that takes a body (npm's `POST …/-/npm/v1/security/advisories/bulk`): `204` with `Trove-Bastion-Route:
    read-with-body`;
  - no route: the format's or the core's `404`.
- The Bastion replays a `403` or `404` probe answer to the caller and keeps it per method and path for
  `bastion.cache.metadata_revalidate_after`. After a `204` it forwards the real request with its body (at most
  `bastion.main.max_forwarded_body`; a larger body answers `413` from the Bastion) and the caller's credential; the answer is
  `pass`. A request with a body above the limit is refused before the probe.
- A forwarded non-probe write that reaches the main installation anyway (a defect) is refused the same way, since the
  Bastion token makes every write `bastion_read_only`.

Acceptance criteria:

- `npm publish` against the Bastion prints `E403` with the main installation's URL in the message, and `dotnet nuget push` exits
  non-zero with `403`; the main installation records no `package.published`, and the Bastion's byte counter shows that less than
  64 KiB of the upload body were read.
- `npm audit` through the Bastion returns the main installation's answer.
- 1,000 `PUT` requests to one path within a minute cause one probe to the main installation.

### R-BAS-015 · Revocation feed
Priority: **MVP** · Covers: RMT-040

`GET /api/v1/bastion/feed?after=<cursor>&limit=<n>`, with the Bastion token. The feed is built on the revocation journal of
[authorization.md](authorization.md) R-AUTHZ-031 and [ha-and-helm.md](ha-and-helm.md) R-HA-054: the journaled audit events, plus
the content events below ([Q-BAS-29](#q-bas-29--the-feed-reads-the-journals-events-from-the-audit-table),
[Q-BAS-30](#q-bas-30--the-feed-also-carries-withdrawn-content)).

| Audit event | Feed entry |
|---|---|
| `token.revoked`, `ci.token.revoked`, `token.rotated` | `{"kind": "token", "tokens": [<token ID>]}` (for `token.rotated`, the predecessor) |
| `user.disabled`, `user.deleted`, `serviceaccount.disabled`, `serviceaccount.deleted` | `{"kind": "principal", "principals": [<ID>]}` |
| `group.members.changed`, `user.groups.synced` | `principal` with every removed member |
| `authz.role.unbound` of a user or service account | `principal` with that principal |
| `group.deleted`, `authz.role.unbound` of a group, `authz.policy.deleted`, `authz.policy.updated`, `authz.settings.changed`, the `ci.issuer.*` and `ci.mapping.*` disables, deletions and updates, `endpoint.exposure.deleted` and `.updated` of the default endpoint | `{"kind": "grants"}`: every permission answer of every caller |
| `settings.auth.changed` | `{"kind": "settings"}`, and `grants` when anonymous access was switched off |
| `npm.unpublish`, `package.deleted` | `{"kind": "content", "repositories": [<key>, …]}`: the repository and every virtual repository that includes it, recursively, at the time the feed is read |
| from M4b: `vulnerability.finding.status_changed` and `vulnerability.finding.reopened` with `block: set` ([vulnerability-scanning.md](vulnerability-scanning.md) R-VULN-013) | `content` for the finding's repository and its virtual repositories |
| `authz.ownership.*`, `package.published` and every other journaled event | none: they bound writes or reserve numbers, which the Bastion never does |

```json
{
  "entries": [
    {"id": "0192f2a0-…", "at": "2026-11-02T10:00:01.250Z", "kind": "token", "tokens": ["0192…"]},
    {"id": "0192f2a1-…", "at": "2026-11-02T10:00:02.900Z", "kind": "content", "repositories": ["npm-local", "npm-all"]}
  ],
  "cursor": "MjAyNi0xMS0wMlQxMDowMDowMi45MDBafDAxOTJmMmExLi4u",
  "more": false,
  "reset": false
}
```

*(example answer)*

- **Order and cursor.** Entries are read from `audit_event` by `(occurred_at, id)`. Because `occurred_at` is the start of the
  transaction, a later commit can carry an earlier time; the main installation therefore returns every entry with
  `occurred_at > cursor.at − bastions.feed.lookback`, and the Bastion skips the IDs it has applied. `cursor` names the last entry
  returned, or the request's cursor when there is none; `more` is true when `limit` (at most `bastions.feed.max_page`) cut the
  answer.
- **Reset.** `reset: true` when the entry the cursor names no longer exists although its time lies within the audit retention
  (a point-in-time restore or a failover that lost commits), when the cursor's time lies after the main installation's clock by
  more than `lookback`, or when it lies before the audit retention. The Bastion then drops every answer (R-BAS-026) and takes the
  returned cursor.
- **History.** `after` may name a synthetic cursor `t:<RFC 3339>` up to the audit retention back, so a starting replica reads the
  entries of the whole lifetime of an answer (R-BAS-026,
  [Q-BAS-33](#q-bas-33--a-replica-starts-with-the-feed-history-of-an-answers-lifetime)).
- Entries carry IDs, never names or secrets. The feed costs one indexed query per poll and replica.

```yaml
bastions:                              # the main installation's section (the Bastion's own is bastion)
  feed:
    lookback: 5m                       # default (proposed); longer than any transaction (the replay_margin of R-HA-054)
    max_page: 1000                     # default (proposed)
```

Acceptance criteria:

- Revoking `$FULL` adds one `token` entry within the commit; a feed read with the earlier cursor returns it once, and the next
  read with the returned cursor returns it again only while it lies within `lookback`, with the same `id`.
- A point-in-time restore of the main installation to before the cursor's entry answers the next feed read with `reset: true`.
- `npm unpublish @acme/sdk@1.0.0` in `npm-local` yields a `content` entry naming `npm-local` and `npm-all`.

### R-BAS-016 · Download intake
Priority: **MVP** · Covers: RMT-041

`POST /api/v1/bastion/downloads`, with the Bastion token: the downloads the Bastion served from its cache, in batches of at
most `bastions.downloads.max_batch` (owner, [Q-BAS-13](#q-bas-13--downloads-served-from-the-cache-are-reported-to-the-main-installations-audit)).

```json
{
  "events": [
    {"at": "2026-11-02T10:00:03.100Z", "tokenId": "0192…", "principalId": "0191…", "clientAddress": "203.0.113.7",
     "repository": "npm-all", "path": "/api/npm/npm-all/@acme/sdk/-/sdk-2.1.0.tgz",
     "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
     "userAgent": "npm/11.20.0 node/v24.0.0", "replica": "trove-bastion-7d9c-x2k4q"}
  ],
  "dropped": 0,
  "droppedSince": "2026-11-02T09:00:00Z"
}
```

*(example request)*

- The main installation writes each event as `artifact.downloaded` (asynchronous, R-OPS-015) with `actor` from `principalId`
  (or `anonymous`), `token_id`, `source_ip = clientAddress`, `repository`, `path` (the request path; the Bastion knows no item
  path), `details.sha256`, `details.origin: "bastion"`, `details.bastion: <registration name>`, `details.replica`, and
  `occurred_at = at`. An event whose `at` lies in the future is recorded at the time of receipt; one older than
  `bastions.downloads.max_age` is not recorded and is counted as dropped.
- `dropped` counts the events the Bastion lost since `droppedSince` (R-BAS-039); a non-zero value writes
  `bastion.downloads.dropped` with the count and the period, so the loss is in the audit trail itself.
- With `audit.record_downloads: false` the operation answers `204` and records nothing; the Bastion learns the switch from the
  handshake and does not send.
- Downloads the Bastion fetched from the main installation are recorded by the main installation itself as the caller's
  request; the Bastion reports only downloads it served from its cache, so no download is counted twice.

```yaml
bastions:                              # the main installation's section (the Bastion's own is bastion)
  downloads:
    max_batch: 1000                    # default (proposed)
    max_age: 720h                      # default (proposed)
```

Acceptance criteria:

- With `audit.record_downloads: true`, a warm `npm ci` of 20 tarballs through the Bastion yields 20 `artifact.downloaded`
  events with `origin: bastion`, the callers' token ID and client address, within two `flush_interval`s; the cold run's 20 were
  recorded by the main installation without `origin`.
- A batch with `"dropped": 7` yields one `bastion.downloads.dropped` event with count 7.

### R-BAS-017 · The real client and the Bastion's host at the main installation
Priority: **MVP** · Covers: trove-only

The main installation counts the real client and renders documents for the Bastion's host (owner,
[Q-BAS-17](#q-bas-17--the-bastion-mirrors-the-main-installations-url-space), [Q-BAS-21](#q-bas-21--internet-exposure-limits-blocking-failed-sign-in-timeouts-and-a-configurable-cache)):

- **Client address.** The Bastion sends exactly one forwarded-for entry, the client address it determined with its own
  `server.trusted_proxies` (R-BAS-019), in both header families: `X-Forwarded-For: <address>` and `Forwarded: for=<address>`
  (quoted and bracketed for IPv6). It replaces whatever forwarded headers the caller sent. The main installation lists the
  Bastion's egress addresses in its `server.trusted_proxies`, so R-ARCH-006 resolves the client address to the caller's, in
  whichever family `server.forwarded_header` selects; its rate limits, its failed-credential throttle and its audit then apply
  to the client, not to the Bastion.
- **Host.** The Bastion sends its base URL's scheme, host and port as `X-Forwarded-Proto`, `X-Forwarded-Host`,
  `X-Forwarded-Port` and as `proto=` and `host=` of the same `Forwarded` element. The main installation lists the Bastion's
  base URL in `server.additional_base_urls`, so every URL it renders for the Bastion is on the Bastion's host (R-OPS-005,
  R-EP-003); the handshake proves it (R-BAS-010). The Bastion therefore carries no format knowledge: it never rewrites a body.
- Main installation values for the example *(example)*:

```yaml
server:
  trusted_proxies: ["10.244.0.0/16", "198.51.100.20/32"]   # example: the main ingress pods and the Bastion's egress address
  additional_base_urls: ["https://packages.example.com"]   # example
```

Acceptance criteria:

- A caller at `203.0.113.7` sending `X-Forwarded-For: 192.0.2.1` through the Bastion appears in the main installation's request
  log and audit as `203.0.113.7`, with either value of the main installation's `server.forwarded_header`.
- `npm view @acme/sdk dist.tarball` through the Bastion prints a URL on `packages.example.com`, and no body or header the e2e run
  captures at the Bastion contains `trove.example.com`, except the `bastion_read_only` detail of R-BAS-014.
- A loop of failed credentials from one caller through the Bastion is throttled by the main installation per that caller's
  address, while a second caller through the same Bastion is not slowed down.

## Serving at the Bastion

### R-BAS-018 · Request classification at the Bastion
Priority: **MVP** · Covers: trove-only

The Bastion holds no repository list; every repository of the main installation is reachable under the same paths (owner,
Q-BAS-17). Each request passes these steps in order; the first that answers ends it:

1. **Host.** The effective host (R-EP-003 rules, with the Bastion's own trusted proxies) must be the host of
   `server.base_url`; any other host, an IP literal and a missing host answer `421 misdirected_request`.
2. **Block list** (R-BAS-037): a blocked address answers `429 address_blocked`.
3. **Path family.** `/api/v1/…` and every path outside `/api/<format>/<key>/…` and `/files/<key>/…` answer `404
   bastion_not_served` (R-BAS-003). `<key>` is the addressed repository key for the Bastion's bookkeeping until the main
   installation's `Trove-Bastion-Repository` confirms it; a key that does not match `^[a-z0-9][a-z0-9._-]{1,63}$` (INV-5)
   answers `404` without a request. The Bastion reads no other part of a path.
4. **Per-address limits** (R-BAS-035) and **credentials** (R-BAS-019), including the failed-authentication timeout (R-BAS-036).
5. **Method.** `GET` and `HEAD` go to the read flow (R-BAS-020 to R-BAS-024); `OPTIONS` answers `405`; every other method goes
   to the write refusal of R-BAS-014.
6. **Read flow**: the cache entry for the request's cache key and the caller's answers decide between a hit, a check and a miss.

- **Cache key**: the path and query exactly as received (percent-encoding kept), and the normalized values of the request
  headers the main installation's `Vary` named for that path (`Accept`, `Accept-Encoding` for npm documents). The host is
  constant.

Acceptance criteria:

- `curl --resolve trove.example.com:443:<bastion address> https://trove.example.com/api/npm/npm-all/lodash` answers `421`.
- `GET /api/npm/NPM-ALL/lodash` answers `404` without a request to the main installation; `GET /api/npm/npm-all/lodash` with
  `Accept: application/vnd.npm.install-v1+json` and without it are two cache entries.

### R-BAS-019 · Credentials at the Bastion
Priority: **MVP** · Covers: trove-only

Reads at the Bastion authenticate exactly as at the main installation (owner,
[Q-BAS-6](#q-bas-6--a-caller-reads-what-its-token-may-read-there-is-no-release-step)), with the client credentials of INV-8 and
without sessions:

- **Sources.** `Authorization: Bearer <token>`, or Basic with the token as password (any username). `X-NuGet-ApiKey` is
  never a read credential: NuGet sends it on pushes, which the probe of R-BAS-014 refuses without it. The cookie is ignored
  (R-BAS-003). The credential key of every answer is the SHA-256 of
  the token string; the Bastion keeps no token in memory beyond the request, on disk or in Valkey.
- **Before any request to the main installation:** a token that fails R-AUTH-022's syntax and checksum answers `401
  invalid_token` with the challenge of R-AUTH-002 for the path family and counts as a failed authentication (R-BAS-036). The
  types `pat`, `sat` and `cit` are accepted; `bst` and an unknown prefix answer the same `401` and count.
- **Anonymous.** A request without a credential is anonymous exactly when the main installation admits anonymous requests
  (owner, [Q-BAS-12](#q-bas-12--anonymous-reads-as-on-the-main-installation-with-misses-absorbed-at-the-bastion)): with
  `anonymousAccess: false` from the handshake the Bastion answers `401 credentials_required` with the challenge itself, without a
  request; with `true` the anonymous caller's answers are kept under the key `anonymous`, shared by every anonymous caller, since
  the main installation's decision does not depend on the client address ([authorization.md](authorization.md) R-AUTHZ-029).
- **Client address.** R-ARCH-006 with the Bastion's `server.trusted_proxies` and `server.forwarded_header`. For the limits and
  blocks of R-BAS-035 to R-BAS-037 an IPv6 address counts as its `/64`.

Acceptance criteria:

- `trv_pat_` with one flipped character answers `401` with `WWW-Authenticate: Basic realm="trove"` on `/api/npm/…`, and the
  main installation's counter records no request.
- With anonymous access off at the main installation, 1,000 anonymous `npm view` within a minute cause no request to the main
  installation after the handshake; after the administrator switches anonymous access on, the next anonymous request is
  checked within `feed.poll_interval` plus 5 s.

### R-BAS-020 · Answers and their validity
Priority: **MVP** · Covers: trove-only

An answer is what the main installation said about a caller (owner,
[Q-BAS-1](#q-bas-1--the-bastion-asks-the-main-installation-and-keeps-no-copy-of-users-tokens-or-policies),
[Q-BAS-8](#q-bas-8--answers-are-kept-for-a-period-and-a-revocation-feed-drops-them-within-seconds)):

| Answer | Key | Holds | From |
|---|---|---|---|
| Credential | credential key | `kind`, token ID, principal ID, token expiry | the check, or `Trove-Bastion-Subject` of any marked response |
| Read shape | credential key, repository | `full`, `partial`, `none` with its status | the check, or `Trove-Bastion-Read` |
| URL | credential key, cache key | allowed, or the stored refusal (status, headers, body) | a forwarded or conditional request for that URL |

- **Validity** ([Q-BAS-32](#q-bas-32--answers-validate-themselves-against-the-feed-and-are-signed-in-valkey)). An answer obtained
  at `o` (the Bastion's clock when it sent the request) is usable while all of these hold:
  1. `now < o + bastion.answers.period`, extended by `bastion.answers.grace_period` only while the main installation is
     unreachable (R-BAS-027);
  2. `now <` the token's expiry, never extended;
  3. no feed entry that matches the answer was received after `o − 15 s`: a `token` entry naming its token, a `principal` entry
     naming its principal, a `grants` entry for a read-shape or URL answer, a `content` entry naming its repository for a URL
     answer; 15 s covers the main installation's revocation bound (at most 10 s, R-AUTH-023) and the clock skew between Bastion
     replicas;
  4. `o` is not earlier than the start of the replica's feed history plus 15 s (R-BAS-026).
- A refused or invalid answer is kept like an allowed one, so a refused caller costs the main installation one request per
  period; a widening of a right may take the period to reach the Bastion (owner).
- Answers live in the replica's memory (at most `bastion.answers.max_entries`, least recently used first) and, with Valkey, in
  Valkey as signed records (R-BAS-034). A replica may refresh an answer that a request uses in the last fifth of its period in
  the background, with one collapsed request.

```yaml
bastion:
  answers:
    period: 5m                          # default (proposed); 30s to 1h
    grace_period: 15m                   # default (proposed; the owner's proposal); 0 to 24h
    max_entries: 200000                 # default (proposed), per replica
```

Acceptance criteria:

- `TestR_BAS_020_AnAnswerObtainedBeforeARevocationIsNeverUsedAfterIt`: an answer whose request left before a `token` entry
  arrived is not used, also when it reaches the replica (or Valkey) after the entry was applied.
- A token that expires 30 s after its answer was obtained is refused at the Bastion from its expiry on, also while the main
  installation is unreachable.
- With `period: 5m`, a policy that grants `$NONE` read on `npm-all` reaches the Bastion within 5 minutes and 5 seconds.

### R-BAS-021 · Fetching on a miss with the caller's credential
Priority: **MVP** · Covers: RMT-040, RMT-044

On a miss the Bastion forwards the caller's request with the caller's own credential; the main installation decides and serves,
and the Bastion keeps what may be kept (owner, Q-BAS-7):

- **Request.** `GET` or `HEAD` with the path and query as received, the caller's `Authorization` unchanged, every end-to-end
  request header except `Cookie`, `Forwarded`, `X-Forwarded-*`, `Trove-*`, `Range`, `If-*` and the hop-by-hop headers, and the
  headers of R-BAS-008 and R-BAS-017. A conditional the Bastion sends is its own (R-BAS-022, R-BAS-023), never the caller's.
- **Response.** The marking decides what is kept: `artifact` (R-BAS-022), `shared` or `private` (R-BAS-023), `absent`
  (R-BAS-024), `pass` (nothing). `Trove-Bastion-Subject`, `Trove-Bastion-Read` and the status give the credential, shape and URL
  answers of R-BAS-020. The caller receives the main installation's status, headers and body, without the `Trove-Bastion-*`
  headers, `X-Trove-Replica`, `Cache-Status` ([Q-BAS-40](#q-bas-40--no-cache-state-in-the-bastions-answers)) and hop-by-hop
  headers.
- **Streaming.** An artifact is spooled while it is streamed to the caller, with R-REPO-041's withheld tail: the last 64 KiB
  are released only after the SHA-256 equals the content address of its `ETag` and the entry is committed; on a mismatch the
  transfer is aborted and nothing is kept. A document is kept once it has arrived whole. An artifact without `Content-Length`
  or above `bastion.cache.max_object_bytes`, and a document above 64 MiB, are passed through and not kept. A `HEAD` miss is
  forwarded as `HEAD` and keeps answers only.
- **Errors.** An answer that carries the marking is the main installation's own, whatever its status, and is passed on
  (`pass`). A connect or TLS error, a timeout, and an unmarked `5xx` (an ingress or proxy in front of the main installation)
  answer `502 main_failed` or `504 main_timeout` from the Bastion (R-BAS-029) and keep no answer.

Acceptance criteria:

- A cold `npm ci` of 20 tarballs with `$FULL` sends 20 tarball requests carrying `Authorization: Bearer $FULL` and
  `Trove-Bastion-Authorization` to the main installation, and the main installation's audit shows `$FULL` as actor.
- A tarball whose bytes the in-process main installation corrupts after the status line is aborted at the client, and a second
  request fetches again.

### R-BAS-022 · Artifacts
Priority: **MVP** · Covers: RMT-040

A cached artifact is served to a caller only after the main installation said that this caller may read it, with an answer kept
for the bound (owner, Q-BAS-7):

- **Entry.** Per cache key: the content address (SHA-256), size, the response headers to replay, the addressed repository, and
  `validatedAt`, the time the main installation last answered `200` or `304` for it. The bytes are stored once per content
  address (R-BAS-030).
- **Shape first.** When the entry exists and the caller has no usable read-shape answer for its repository, the Bastion asks
  the check of R-BAS-011 for it, one request per credential and repository, collapsed; the shape then decides below. A caller
  whose credential the main installation called `invalid` gets the `401` of R-BAS-029.
- **Serve from the cache without a request** when the entry is fresh and the caller is allowed:
  - fresh: `now < validatedAt + bastion.cache.artifact_revalidate_after` and no `content` entry for its repository was received
    after `validatedAt − 15 s` ([Q-BAS-31](#q-bas-31--cached-artifacts-are-revalidated-after-a-period));
  - allowed: a usable read-shape answer `full` for the repository, or a usable URL answer `allowed` for the cache key.
- **Check** otherwise: a conditional `GET` with the caller's own credential and `If-None-Match: "<sha256>"` through the real
  route ([Q-BAS-27](#q-bas-27--a-cached-artifact-is-checked-through-the-real-route)). `304`: the bytes are served from the cache,
  and `validatedAt` and the caller's URL answer are renewed. `200`: the new bytes replace the entry and are served. A refusal:
  the main installation's answer is replayed and kept as the caller's URL answer, and the entry stays for other callers; only a
  `404` marked `absent` removes it. The main installation answers the conditional from PostgreSQL without reading its object
  store (R-STOR-012), so a check costs no artifact transfer.
- **Refuse without a request** when the caller's shape answer is `none`: the Bastion answers the stored status (R-BAS-029).
- `Range` and `If-Range` with the content address, `If-None-Match` and `HEAD` from the caller are answered from the entry
  (RFC 9110), after the same permission rule.

Acceptance criteria:

- Warm cache, `$FULL` with a usable `full` answer: `npm ci` of 20 tarballs sends no request to the main installation.
- Warm cache, `$PART` for `@acme/sdk` tarballs: 1 conditional request per tarball, each answered `304`, no tarball bytes from
  the main installation; for a tarball outside `@acme/*` the main installation's `404` is replayed.
- `$NONE` with a `none` answer on `npm-all`: the tarball request answers `404` without a request.
- After `artifact_revalidate_after`, the next request for a cached tarball by `$FULL` sends one conditional request.

### R-BAS-023 · Documents: shared for confirmed full readers, per caller otherwise
Priority: **MVP** · Covers: RMT-040

The main installation marks a document that is the same for every caller who reads the whole repository; the Bastion shares it
for the metadata period with each caller for whom the main installation confirmed a full read (owner,
[Q-BAS-18](#q-bas-18--metadata-shared-for-confirmed-full-readers-filtered-documents-per-caller)):

- **Shared document** (`Trove-Bastion-Class: shared`): kept in the cache backend under its cache key, with its validator, the
  replay headers, the repository, `validatedAt`, within `bastion.cache.documents_max_bytes` so that documents never evict
  artifacts. Served without a request to a caller with a usable `full` answer for the repository while
  `now < validatedAt + bastion.cache.metadata_revalidate_after` and no `content` entry for the repository was received after
  `validatedAt − 15 s`; a caller without a shape answer gets one first, as in R-BAS-022. Otherwise a conditional `GET` with the
  caller's credential and the stored validator: `304` with
  `shared` renews it and serves it; `200` with `shared` replaces it; `200` with `private` is served to this caller only and kept
  as its private copy; a refusal is replayed.
- **Private document** (`private`, a document filtered for the caller): never shared. It goes to the main installation per
  caller with a conditional request on every use: the Bastion keeps the caller's last copy in memory only, under (credential
  key, cache key), at most `bastion.cache.private_documents_max_bytes` per replica, and sends its validator; `304` serves the
  copy. A private copy is never written to the cache backend or to Valkey.
- **Search** and every other `pass` answer (`Volatile` routes, `-/whoami`) go to the main installation per caller on every
  request and are never kept ([Q-BAS-41](#q-bas-41--search-results-go-to-the-main-installation-per-caller)).
- Metadata of a new version reaches a `full` caller within `metadata_revalidate_after` (H-384).

```yaml
bastion:
  cache:
    metadata_revalidate_after: 1m       # default (proposed)
    artifact_revalidate_after: 10m      # default (proposed)
    documents_max_bytes: 2GiB           # default (proposed); a share of max_bytes
    private_documents_max_bytes: 256MiB # default (proposed), per replica, memory
```

Acceptance criteria:

- Warm cache, `dotnet restore` with a second `full` token that has not been seen: one check answers `full` for `nuget-all`, and
  no document request reaches the main installation within `metadata_revalidate_after`.
- `$PART` never receives the shared npm document of `npm-all` (it lists packages outside `@acme/*`): every `npm view` with
  `$PART` sends a conditional request and receives its filtered copy; the shared entry stays untouched.
- `npm search acme` with `$FULL` twice sends two search requests to the main installation.

### R-BAS-024 · Negative cache
Priority: **MVP** · Covers: trove-only

Names the main installation does not have are answered at the Bastion, because an anonymous request costs an attacker nothing
(owner, Q-BAS-12):

- A `404` marked `absent` is kept per cache key, with its body and headers, for `bastion.cache.negative_period`, in memory and,
  with Valkey, signed in Valkey; at most `bastion.cache.negative_max_entries` per replica.
- It is replayed only to callers with a usable `full` answer for the repository
  ([Q-BAS-42](#q-bas-42--negative-entries-are-served-only-to-full-readers)); every other caller goes to the main installation,
  so the speed of an answer never tells a `partial` caller whether a hidden name exists. A caller with a `none` answer receives
  its refusal anyway.
- A publish of the name at the main installation appears at the Bastion after `negative_period` at the latest (H-384).

```yaml
bastion:
  cache:
    negative_period: 1m                 # default (proposed)
    negative_max_entries: 100000        # default (proposed), per replica
```

Acceptance criteria:

- Anonymous access on, `anonymous` a `full` reader of `npm-all`: 10,000 anonymous requests from 100 addresses for 100 random
  missing names within a minute cause at most one request per name and replica (plus one check per replica) to the main
  installation, and one per name with Valkey.
- `$PART` asking for a missing name twice sends two requests to the main installation.

### R-BAS-025 · Concurrency budget towards the main installation and collapsed misses
Priority: **MVP** · Covers: trove-only

- **Budget** ([Q-BAS-35](#q-bas-35--the-concurrency-budget-is-per-replica-with-a-share-for-anonymous-misses)). Every request to
  the main installation except the handshake and the feed takes a slot: forwards, conditional requests, checks, probes,
  download batches. A replica holds at most `ceil(bastion.main.max_concurrent ÷ bastion.replicas)` slots, of which requests on
  behalf of anonymous callers may hold at most `bastion.main.anonymous_share`. A request that gets no slot within
  `bastion.main.queue_timeout` answers `503 bastion_busy` with `Retry-After: 1`. Misses can therefore never put more than the
  budget onto the main installation, whatever arrives.
- **Collapsing.** Concurrent misses for one cache key share one fetch per replica (single-flight), and with Valkey one fetch
  across replicas with the bucket backend, under a fetch lock of R-ARCH-033's form (`trove-bastion:<prefix>:flight:<sha256 of
  the cache key>`, 30 s, extended by the holder). A waiter is served from the result only when its own answer allows it
  (R-BAS-022, R-BAS-023); otherwise it makes its own conditional request. Collapsing never shares a decision.
- `bastion.replicas` is rendered by the chart: `replicaCount`, or `autoscaling.maxReplicas` with autoscaling, so the sum over
  replicas never exceeds the configured budget ([Q-BAS-34](#q-bas-34--without-valkey-budgets-divide-by-the-charts-replica-count)).

```yaml
bastion:
  replicas: 2                           # chart-managed: replicaCount, or autoscaling.maxReplicas
  main:
    max_concurrent: 64                  # default (proposed), across all replicas
    anonymous_share: 0.25               # default (proposed)
    queue_timeout: 2s                   # default (proposed)
```

Acceptance criteria:

- With `max_concurrent: 8`, two replicas and a main installation that delays every answer by 2 s, 500 concurrent cold requests
  never show more than 8 in-flight requests at the main installation, and anonymous ones never more than 2.
- 50 concurrent cold requests for one tarball cause one request to the main installation per replica, and one with Valkey and
  the bucket backend.

### R-BAS-026 · Applying the revocation feed
Priority: **MVP** · Covers: trove-only

The Bastion follows the revocation feed every few seconds, so a revoked token or a narrowed right is dropped within seconds
(owner, Q-BAS-8):

- Every replica polls `feed` every `bastion.feed.poll_interval` and applies each new entry by its ID: it records the entry with
  its receipt time, which makes every matching answer unusable (R-BAS-020 rule 3), and deletes the matching answers from memory
  and, best effort, from Valkey. `settings` triggers a handshake. Entries are kept for `answers.period + grace_period + 15 s`.
- **Start.** A starting replica reads the feed from `t:<now − answers.period − answers.grace_period − 15 s>` before it uses any
  answer from Valkey; that time is the start of its feed history (rule 4). Until the first read succeeds it uses no shared answer.
- **Reset.** On `reset: true` the replica drops every answer it holds and ignores every answer in Valkey obtained before the
  reset (rule 4 with the reset time as the history start).
- **Bound.** While the main installation is reachable, a narrowing committed at the main installation is in force at every
  Bastion replica within `poll_interval` plus one feed round trip (3 s with the defaults). While it is not, the grace period of
  R-BAS-027 applies (H-383).
- `trove_bastion_feed_lag_seconds` is the age of the last successful poll; `trove_bastion_feed_entries_applied_total{kind}` and
  `trove_bastion_feed_resets_total` count.

```yaml
bastion:
  feed:
    poll_interval: 2s                   # default (proposed); 1s to 10s
```

Acceptance criteria:

- A loop of `npm view` with `$FULL` through each of two replicas: after the token is revoked at the main installation, both
  replicas refuse it within 5 s, with and without Valkey, over 100 trials.
- Removing `acme-full` from the group that holds its grant refuses its next tarball request within 5 s although the tarball is
  cached and its `full` answer was minutes old.
- A replica started after the revocation does not accept the revoked token's answer stored in Valkey.

### R-BAS-027 · The main installation unreachable: grace period, then nothing
Priority: **MVP** · Covers: trove-only

Answers the Bastion already holds stay valid for a configurable grace period beyond their normal period (owner,
[Q-BAS-9](#q-bas-9--the-main-installation-unreachable-grace-period-503-for-unseen-tokens-then-nothing)):

- **Unreachable** ([Q-BAS-44](#q-bas-44--unreachable-means-a-failed-handshake-or-a-stale-feed)) is a state of each replica: it
  begins when the handshake fails (connect or TLS error, timeout, `5xx`, `401 bastion_token_invalid`, `400
  bastion_interface_mismatch`, a base URL mismatch) or when no feed poll has succeeded for `bastion.main.unreachable_after`, and
  ends when a handshake and a feed poll succeed again. Forward errors alone do not begin it.
- While unreachable, the replica sends nothing to the main installation except handshake and feed probes every
  `poll_interval`, and:
  - a caller whose answers allow a cached entry (R-BAS-022, R-BAS-023, with rule 1 extended by `grace_period`) gets cached
    artifacts and shared documents, also past their revalidation periods (possibly stale metadata), and its own private copies;
  - a token the Bastion has not seen, and a miss, get `503 main_unreachable` with `Retry-After: 30`;
  - after the grace period of every answer has passed, at the latest `answers.period + grace_period` after unreachability began,
    the replica serves nothing: every read answers `503`.
- Download events are buffered (R-BAS-039). `trove_bastion_main_reachable` is 0 and
  `trove_bastion_main_unreachable_since_timestamp_seconds` holds the start.

```yaml
bastion:
  main:
    unreachable_after: 10s              # default (proposed); at least 3 × feed.poll_interval
```

Acceptance criteria:

- NetworkPolicy cuts the Bastion off from the main installation: within 10 s `trove_bastion_main_reachable` is 0; `$FULL`, seen
  2 minutes before, completes a warm `npm ci` and `dotnet restore`; a token never seen gets `503`; a cold package gets `503`;
  20 minutes after the cut (`period` 5m + `grace_period` 15m) every request answers `503`.
- After the policy is removed, the replica returns to `reachable` within `poll_interval` plus one handshake, and buffered
  download events arrive at the main installation.

### R-BAS-028 · Interface version mismatch at the Bastion
Priority: **MVP** · Covers: trove-only

- A `400 bastion_interface_mismatch`, or an answer whose `Trove-Bastion-Interface` differs from the Bastion's, makes the replica
  unreachable (R-BAS-027) and sets `trove_bastion_interface_mismatch 1`, `trove_bastion_interface_version{side="bastion"}` and
  `{side="main"}` to the two versions; the log names both and the release of the main installation if the handshake gave it.
- The PrometheusRule of the Bastion's chart fires `TroveBastionInterfaceMismatch` (R-BAS-041). The mismatch clears when a
  handshake with equal versions succeeds, after either side was upgraded.

Acceptance criteria:

- The e2e suite upgrades the main installation to a build with `InterfaceVersion + 1`: the alert fires in the cluster's
  Prometheus within 2 minutes, the Bastion serves warm content for the grace period, and after the Bastion is upgraded the alert
  resolves and cold requests succeed.

### R-BAS-029 · Responses of the Bastion's own
Priority: **MVP** · Covers: trove-only

The Bastion produces only these answers itself; every other body comes from the main installation
([Q-BAS-39](#q-bas-39--the-bastions-own-refusals-are-problem-details)). They are problem details (RFC 9457,
`application/problem+json`, R-ARCH-023's members) with the challenge of R-AUTH-002 for the path family on a `401`, and
`Cache-Control: no-store`:

| Status | Code | When |
|---|---|---|
| `401` | `invalid_token`, `credentials_required` | a malformed or refused credential (R-BAS-019), a kept `invalid` answer, anonymous access off |
| `404`, `403`, `401` | the code of the kept refusal | a read-shape answer `none` (R-BAS-022) |
| `404` | `bastion_not_served` | a path outside the mirrored families (R-BAS-018) |
| `405`, `413`, `421` | `method_not_allowed`, `payload_too_large`, `misdirected_request` | R-BAS-014, R-BAS-018 |
| `429` | `rate_limited`, `auth_timeout`, `address_blocked` | R-BAS-035 to R-BAS-037, with `Retry-After` and the `RateLimit-Policy` and `RateLimit` fields of R-API-016 |
| `502`, `503`, `504` | `main_failed`, `main_unreachable`, `bastion_busy`, `main_timeout` | R-BAS-021, R-BAS-025, R-BAS-027 |

- Package clients act on the status and the challenge; the body is trove's, not the format's. The difference is functional
  and shows only in the client's message.

Acceptance criteria:

- Each row has a table test case; `npm` and `dotnet restore` report the status of each, and a `401` makes both send their
  configured credentials (R-AUTH-002).

## The cache

### R-BAS-030 · Cache backends
Priority: **MVP** · Covers: trove-only

The cache holds a small part of the artifacts, never the main installation's object store, which the Bastion cannot read
(owner, [Q-BAS-10](#q-bas-10--the-cache-an-own-bucket-or-a-local-volume-never-the-main-installations-object-store)). The chart
chooses the backend (`bastion.cache.backend`):

- **`bucket`**: an S3 bucket of the Bastion's own, shared by every replica (credentials from `secrets.cacheBucket` or workload
  identity; a bucket of the guided-traffic stackit-s3-provisioner, or SeaweedFS in the local profile). Layout below `prefix`:
  `sha256/<h0h1>/<h2h3>/<64 hex>` (bytes, INV-4's form), `entries/<k0k1>/<sha256 of the cache key>.json` (signed entries),
  `access/<64 hex>` (empty access markers, R-BAS-031), `audit-spool/<replica>/<unix ms>.jsonl` (R-BAS-039). Only `PutObject`,
  `GetObject`, `HeadObject`, `ListObjectsV2` and `DeleteObject` are used; no versioning, no conditional write.
- **`volume`**: a local volume per replica, the same layout below `bastion.cache.volume.dir`; generic ephemeral
  (`ReadWriteOnce`, deleted with the pod, the default) or persistent per pod (a StatefulSet with a claim template). No volume is
  shared (INV-13). Without routing by path (R-BAS-033) each replica fills its own copy.
- A fill is spooled to the pod's temporary volume (`tmp`, bucket backend) or to a temporary directory of the cache volume, and
  is written or renamed to its content address only after verification (R-BAS-021).
- The backend is gated at the first readiness only (R-STOR-035's pattern); a failing backend afterwards turns hits into misses
  that are passed through, counted in `trove_bastion_cache_errors_total{op}`, never into failed requests.

```yaml
bastion:
  cache:
    backend: volume                     # default (proposed): volume | bucket
    max_bytes: 20GiB                    # default (proposed)
    max_object_bytes: 2GiB              # default (proposed); larger answers are passed through
    volume:
      dir: /var/cache/trove-bastion     # chart-managed
    bucket:
      endpoint: ""                      # example: https://object.storage.eu01.onstackit.cloud
      region: ""                        # example: eu01
      bucket: ""                        # required with backend bucket; example: trove-bastion-partners
      prefix: trove-bastion             # default (proposed)
      force_path_style: false           # default (proposed)
      credentials:
        source: static                  # default (proposed): static | webIdentity | default
        role_arn: ""                    # default (proposed)
```

Acceptance criteria:

- With `backend: bucket` and two replicas, a tarball fetched through replica A is served by replica B from the bucket without a
  request to the main installation (with a usable answer on B).
- With `backend: volume`, killing a pod with an ephemeral volume loses its cache and nothing else; a persistent volume keeps it
  across a rollout.
- The bucket's access log of the e2e run shows only the five operations above.

### R-BAS-031 · Size, eviction by last access and sweeps
Priority: **MVP** · Covers: trove-only

- **Size.** Bytes plus documents stay below `bastion.cache.max_bytes`; documents below `documents_max_bytes`. A sweep runs every
  `bastion.cache.sweep_interval` and, above 95 % of a limit, deletes in order of last access until 85 % remain
  ([Q-BAS-43](#q-bas-43--the-bucket-backend-evicts-by-listing-access-markers)).
- **Last access.** A served entry writes its access marker (an empty object or file named by its content address, or a touch of
  the entry) at most once per `bastion.cache.access_resolution` per replica; the sweep orders by the markers' modification times,
  read with two listings, and deletes the bytes, the entries that point at them and the markers. An entry whose bytes are gone is
  a miss and is removed when it is read.
- **Who sweeps.** With the volume backend every replica sweeps its own volume. With the bucket backend and Valkey one replica
  sweeps, under a lease in Valkey (`trove-bastion:<prefix>:sweep`, 2 × `sweep_interval`); without Valkey every replica sweeps at
  a randomized interval around `sweep_interval`, which is safe because a sweep only deletes, and may evict more than needed.
- `trove_bastion_cache_bytes{area="artifacts|documents"}`, `trove_bastion_cache_capacity_bytes`,
  `trove_bastion_cache_evictions_total{reason="size|stale|missing"}`.

```yaml
bastion:
  cache:
    sweep_interval: 5m                  # default (proposed)
    access_resolution: 1h               # default (proposed)
```

Acceptance criteria:

- With `max_bytes: 100MiB`, filling 300 MiB of tarballs and then reading the first 20 MiB again leaves those 20 MiB cached and
  the total below 100 MiB after the next sweep.
- Two replicas sweeping the same bucket without Valkey never delete an entry that was accessed within `access_resolution`
  while the total is below 85 %.

### R-BAS-032 · Integrity of cached content
Priority: **MVP** · Covers: trove-only

- Artifact bytes are stored under their SHA-256 and verified while they are served, with R-STOR-013's verifying reader and its
  hold-back: a mismatch aborts the transfer, deletes the object and its entries, and counts
  `trove_bastion_cache_corrupt_total`.
- Entries (and documents' digests in them), negative entries and answers in Valkey are signed with HMAC-SHA-256 under the key of
  `secrets.signingKey`, over every field and the cache key; a record that fails its signature is ignored and deleted. A holder
  of write access to the bucket or Valkey without the key can delete records but cannot make the Bastion serve other bytes or
  accept a forged answer (H-387).
- The key is required with the bucket backend or Valkey; it is rotated by replacing the Secret, which makes every signed record
  unverifiable and costs one refill (performance only).

Acceptance criteria:

- Replacing a cached tarball's object in the bucket with other bytes makes the next download abort before its last 64 KiB, and
  the following one fetch from the main installation.
- An entry rewritten in the bucket to point at another content address, and an answer written into Valkey by hand, are ignored.

### R-BAS-033 · Routing by path for local volumes
Priority: **MVP** · Covers: trove-only

- With `backend: volume` and `cacheVolume.routeByPath: true`, the chart makes HAProxy Ingress choose the replica by a
  consistent hash of the request path (URI hashing), so each artifact lives on one replica and the volumes together hold more
  than one could (owner, Q-BAS-11). The annotation names are R-BAS-045's.
- Routing changes nothing in correctness: a request on another replica is a miss. A replica added or drained moves about
  `1 ÷ n` of the paths.
- The chart refuses `routeByPath` with the bucket backend and without `ingress.enabled`.

Acceptance criteria:

- With three replicas and routing on, 300 distinct tarballs requested twice cause 300 requests to the main installation, not
  up to 900, and each tarball is on exactly one volume.

## Shared state

### R-BAS-034 · Optional Valkey
Priority: **MVP** · Covers: trove-only

Valkey is optional (owner, [Q-BAS-11](#q-bas-11--valkey-is-optional-and-without-it-budgets-are-divided-by-the-replicas)). With
it, the replicas share:

| State | Keys (prefix `trove-bastion:<valkey.key_prefix>:`) | Without Valkey |
|---|---|---|
| Block list (R-BAS-037) | `block:<address>`, TTL = remaining duration | per replica |
| Rate-limit and failed-authentication counters (R-BAS-035, R-BAS-036) | R-ARCH-034's sliding windows, limiters `bastion.<name>` | per replica, at the configured value ÷ `bastion.replicas` |
| Fetch coordination (R-BAS-025), sweep lease (R-BAS-031) | `flight:<…>`, `sweep` | per replica |
| The main installation's answers (R-BAS-020), negative entries (R-BAS-024) | `ans:<kind>:<sha256 of the key>`, TTL = remaining validity, signed (R-BAS-032) | per replica |

- This refines INV-3 for the Bastion: its Valkey holds positive authorization results of the main installation, by the
  owner's decision, and only in a form it cannot forge ([Q-BAS-32](#q-bas-32--answers-validate-themselves-against-the-feed-and-are-signed-in-valkey));
  the self-validation of R-BAS-020 holds whatever Valkey returns. Losing Valkey costs performance, never correctness: every
  answer is asked again per replica, blocks and counters work per replica at the divided values, and collapsing is per replica.
- Connection, Sentinel discovery, TLS and degradation follow R-ARCH-031 with the Bastion's own `valkey` section;
  `valkey.mode: none`, the default, means no Valkey. Valkey being unavailable never makes a replica unready;
  `trove_valkey_available` is 0 and `TroveBastionValkeyUnavailable` fires (R-BAS-041).

Acceptance criteria:

- With Valkey, a token checked on replica A is served on replica B without a second check; stopping Valkey makes B check once
  itself, and no request fails.
- With Valkey stopped, the anonymous limit of R-BAS-035 admits at most the configured rate in total across two replicas
  (± one burst per replica).

## Abuse protection

### R-BAS-035 · Per-address rate limits
Priority: **MVP** · Covers: trove-only

Blocking runs in two stages (owner, [Q-BAS-16](#q-bas-16--blocking-in-two-stages)); this is the Bastion's own stage, with
trove's knowledge of credentials and misses:

- **Requests** per client address, in two classes: requests with a credential (`bastion.limits.authenticated`) and requests
  without (`bastion.limits.anonymous`, stricter). Limiters of R-ARCH-034, scope `cluster` with Valkey; refusals answer `429
  rate_limited`.
- **Misses** per client address: requests that need the main installation (forwards, checks, conditional requests, probes),
  `bastion.limits.misses.authenticated_per_minute` and `anonymous_per_minute`. A refused miss answers `429 rate_limited`; hits
  of the same address keep being served.
- Addresses in `bastion.blocking.exempt_cidrs` are not limited.

```yaml
bastion:
  limits:
    authenticated: {requests_per_second: 50, burst: 200}   # default (proposed), per address
    anonymous: {requests_per_second: 10, burst: 40}        # default (proposed), per address
    misses:
      authenticated_per_minute: 300     # default (proposed), per address
      anonymous_per_minute: 30          # default (proposed), per address
```

Acceptance criteria:

- 1,000 anonymous requests from one address within 10 s are admitted at most 140 times (burst 40 plus 10 per second); a second
  address is not slowed down.
- An address that requests 100 random missing names per minute anonymously sends at most 30 requests to the main installation
  per minute, while its hits are served.

### R-BAS-036 · Failed-authentication timeout
Priority: **MVP** · Covers: trove-only

- More than `bastion.failed_auth.max_failures` failed authentications from one address within `bastion.failed_auth.window` put
  the address into a timeout of `bastion.failed_auth.timeout`. A failure is a credential the Bastion refuses itself (R-BAS-019)
  or one the main installation answered `invalid`.
- During the timeout, a request with a credential for which the Bastion has no usable answer answers `429 auth_timeout` with
  `Retry-After` and is not sent to the main installation; a credential with a usable answer keeps working, so colleagues behind
  one address are not cut off ([Q-BAS-36](#q-bas-36--a-failed-authentication-timeout-refuses-unverified-credentials-only));
  anonymous requests stay anonymous requests.
- The counter is a count-only limiter of R-ARCH-034 (`bastion.failed_auth`). The Bastion's limit is stricter than the main
  installation's R-AUTH-038, which still applies to the real client behind it (R-BAS-017).

```yaml
bastion:
  failed_auth:
    max_failures: 20                    # default (proposed)
    window: 10m                         # default (proposed)
    timeout: 15m                        # default (proposed)
```

Acceptance criteria:

- 21 random well-formed tokens from one address: the 21st and every later unknown token within 15 minutes answer `429
  auth_timeout` without a request to the main installation; `$FULL`, used from that address before, keeps working.

### R-BAS-037 · Blocking of flooding addresses and the shared block list
Priority: **MVP** · Covers: trove-only

- An address that collects more than `bastion.blocking.threshold` refusals of R-BAS-035 and R-BAS-036 within
  `bastion.blocking.window` is blocked for `bastion.blocking.duration`; each further block within 24 h doubles the duration, up
  to `bastion.blocking.max_duration` ([Q-BAS-37](#q-bas-37--one-refusal-threshold-blocks-an-address-with-doubling-durations)).
- A blocked address answers `429 address_blocked` with `Retry-After` at step 2 of R-BAS-018, before any further work, and its
  requests are not logged one by one (a counter and one log line per block).
- With Valkey the block list is shared: a block set by one replica holds on every replica at its next request. Without Valkey
  each replica blocks by its own count (H-389).
- `trove_bastion_blocks_total{reason}`, `trove_bastion_blocked_addresses`, and the log line `bastion address blocked` with the
  address, the reason, the duration and the count.

```yaml
bastion:
  blocking:
    threshold: 300                      # default (proposed): refusals per window
    window: 1m                          # default (proposed)
    duration: 15m                       # default (proposed)
    max_duration: 24h                   # default (proposed)
    exempt_cidrs: []                    # default (proposed); example: ["198.51.100.0/24"]
```

Acceptance criteria:

- An address sending 2,000 anonymous requests within a minute is blocked on every replica (with Valkey) within the minute,
  answers `429 address_blocked` for 15 minutes, and a second flood after the block yields 30 minutes.

### R-BAS-038 · The coarse per-address limit at HAProxy Ingress
Priority: **MVP** · Covers: trove-only

- The chart can put a coarser per-address limit in front, at the Bastion's HAProxy Ingress: `edgeLimit.enabled` (default on)
  renders HAProxy Ingress's per-address request-rate and connection limits for `edgeLimit.requestsPerSecond` and
  `edgeLimit.connections` on the Bastion's Ingress (owner, Q-BAS-16). It can be switched off.
- It is set wider than the Bastion's own: the chart refuses `edgeLimit.requestsPerSecond` at or below
  `config.bastion.limits.authenticated.requests_per_second`, so the Bastion's stage, which knows credentials and misses, decides
  first and the ingress only cuts what would overload the Bastion itself.
- Volumetric attacks on bandwidth are the network provider's, not the Bastion's (H-382).

Acceptance criteria:

- `helm template` with `edgeLimit.requestsPerSecond: 50` and the default authenticated limit fails and names both values.
- In the e2e cluster, 20,000 requests from one address within 10 s are partly refused by the ingress (`429` without a request
  log line at the Bastion) and the Bastion's replicas stay below their in-flight limit.

## Audit, observability, lifecycle

### R-BAS-039 · Reporting downloads served from the cache
Priority: **MVP** · Covers: RMT-041

- While the handshake says `recordDownloads: true`, every artifact the Bastion serves from its cache (a hit, or a check answered
  `304`) appends an event of R-BAS-016 to a bounded in-memory buffer (`bastion.audit.buffer_events`); the buffer is sent every
  `bastion.audit.flush_interval` or at `batch_size` events.
- While the main installation is unreachable the buffer keeps growing up to its bound; an event that finds it full is dropped,
  counted in `trove_bastion_audit_events_dropped_total`, and reported in the next batch's `dropped`
  ([Q-BAS-45](#q-bas-45--downloads-are-buffered-in-memory-and-spooled-at-shutdown)).
- At shutdown the replica sends its buffer within the drain; what it cannot send it writes to the cache backend's
  `audit-spool/`, at most `bastion.audit.spool_max_events`, and any replica sends spooled files once the main installation is
  reachable, deleting each after a `2xx`. With a volume backend the spool survives only with a persistent volume (H-391).

```yaml
bastion:
  audit:
    buffer_events: 100000               # default (proposed), per replica
    batch_size: 500                     # default (proposed)
    flush_interval: 1s                  # default (proposed)
    spool_max_events: 1000000           # default (proposed)
```

Acceptance criteria:

- With the main installation cut off for 2 minutes during a warm `npm ci` loop, every download appears in the main
  installation's audit after the cut ends, with its original time; with `buffer_events: 100` the excess is reported as one
  `bastion.downloads.dropped` event whose count equals the Bastion's counter.
- A drain of a replica during a main installation outage leaves its events in `audit-spool/`, and another replica delivers
  them.

### R-BAS-040 · Metrics
Priority: **MVP** · Covers: trove-only

The Bastion exports R-OPS-007's conventions on its ops listener, with the request metrics of R-OPS-008, and these (no label
carries an address, a token, a path or a repository key):

| Metric | Type | Labels |
|---|---|---|
| `trove_bastion_requests_total` | counter | `class` (`artifact`, `shared`, `private`, `absent`, `pass`, `refused`), `result` (`hit`, `checked`, `miss`, `refused`, `error`) |
| `trove_bastion_bytes_served_total` | counter | `source` (`cache`, `main`) |
| `trove_bastion_main_requests_total` | counter | `kind` (`forward`, `conditional`, `check`, `probe`, `feed`, `handshake`, `downloads`), `outcome` |
| `trove_bastion_main_request_duration_seconds` | histogram | `kind` |
| `trove_bastion_main_in_flight`, `trove_bastion_main_budget` | gauge | `class` (`authenticated`, `anonymous`) |
| `trove_bastion_main_budget_exhausted_total` | counter | `class` |
| `trove_bastion_main_reachable`, `trove_bastion_main_unreachable_since_timestamp_seconds`, `trove_bastion_base_url_mismatch` | gauge | — |
| `trove_bastion_interface_mismatch` | gauge | — |
| `trove_bastion_interface_version` | gauge | `side` (`bastion`, `main`) |
| `trove_bastion_grace_period_seconds`, `trove_bastion_answer_period_seconds` | gauge | — |
| `trove_bastion_answers` | gauge | `kind` (`credential`, `shape`, `url`) |
| `trove_bastion_answers_total` | counter | `kind`, `result` (`hit`, `miss`, `expired`, `dropped`, `bad_signature`) |
| `trove_bastion_feed_lag_seconds` | gauge | — |
| `trove_bastion_feed_entries_applied_total` | counter | `kind` |
| `trove_bastion_feed_resets_total` | counter | — |
| `trove_bastion_token_remaining_seconds`, `trove_bastion_token_warn_before_seconds`, `trove_bastion_main_token_refused` | gauge | — |
| `trove_bastion_token_reloads_total` | counter | `outcome` (`loaded`, `refused`) |
| `trove_bastion_cache_bytes` | gauge | `area` (`artifacts`, `documents`) |
| `trove_bastion_cache_capacity_bytes` | gauge | `area` |
| `trove_bastion_cache_evictions_total` | counter | `reason` |
| `trove_bastion_cache_corrupt_total`, `trove_bastion_cache_errors_total` | counter | `op` for errors |
| `trove_bastion_refused_total` | counter | `reason` (`blocked`, `rate_limited`, `miss_limited`, `auth_timeout`, `invalid_token`, `busy`, `unreachable`, `not_served`, `read_only`) |
| `trove_bastion_blocks_total` | counter | `reason` (`refusals`) |
| `trove_bastion_blocked_addresses` | gauge | — |
| `trove_bastion_audit_events_total` | counter | `outcome` (`sent`, `spooled`, `dropped`) |
| `trove_bastion_audit_buffer_events` | gauge | — |
| `trove_bastion_audit_events_dropped_total` | counter | — |

- The cache hit rate is `trove_bastion_requests_total{result="hit"}` over all; bytes served from the cache are
  `trove_bastion_bytes_served_total{source="cache"}`; requests to the main installation are
  `trove_bastion_main_requests_total` (the observability item of phase 5b).
- `trove_valkey_available` and `trove_ratelimit_fallback{limiter}` are the shared ones.

Acceptance criteria:

- The e2e suite reads every metric of the table from both replicas, and `trove_bastion_requests_total{result="hit"}` rises by 20
  on a warm `npm ci` of 20 tarballs.

### R-BAS-041 · PrometheusRule
Priority: **MVP** · Covers: trove-only

The chart's PrometheusRule (`metrics.prometheusRule.enabled`) holds these alerts; names are stable, each can be disabled with
`metrics.prometheusRule.disabledAlerts`, thresholds come from gauges, not from chart values (R-HA-045's rule):

| Alert | Expression (sketch) | For | Severity |
|---|---|---|---|
| `TroveBastionInterfaceMismatch` | `max(trove_bastion_interface_mismatch) == 1` | 1m | critical |
| `TroveBastionMainUnreachable` | `min(trove_bastion_main_reachable) == 0` (every replica) | 2m | critical |
| `TroveBastionMainUnreachableSingleReplica` | `min(trove_bastion_main_reachable) == 0 and max(trove_bastion_main_reachable) == 1` | 5m | warning |
| `TroveBastionGraceExhausted` | `time() - min(trove_bastion_main_unreachable_since_timestamp_seconds > 0) > max(trove_bastion_answer_period_seconds + trove_bastion_grace_period_seconds)` | 0m | critical |
| `TroveBastionBaseURLMismatch` | `max(trove_bastion_base_url_mismatch) == 1` | 1m | critical |
| `TroveBastionTokenExpiresSoon` | `min(trove_bastion_token_remaining_seconds) < max(trove_bastion_token_warn_before_seconds)` | 1h | warning |
| `TroveBastionTokenRefused` | `max(trove_bastion_main_token_refused) == 1` | 1m | critical |
| `TroveBastionFeedStale` | `max(trove_bastion_feed_lag_seconds) > 30` | 2m | warning |
| `TroveBastionMainBudgetSaturated` | `sum(rate(trove_bastion_main_budget_exhausted_total[5m])) > 0` | 10m | warning |
| `TroveBastionAuditEventsDropped` | `increase(trove_bastion_audit_events_dropped_total[15m]) > 0` | 0m | warning |
| `TroveBastionCacheCorrupt` | `increase(trove_bastion_cache_corrupt_total[1h]) > 0` | 0m | warning |
| `TroveBastionValkeyUnavailable` | `max(trove_valkey_available) == 0` (rendered only with Valkey configured) | 5m | warning |
| `TroveBastionReplicasNotReady` | `min(kube_deployment_status_replicas_available{deployment=~"<fullname>"}) < 2` (or the StatefulSet's) | 5m | warning |

Acceptance criteria:

- `promtool check rules` and `promtool test rules` pass, with one test per alert that fires on its failure input and stays
  silent on a healthy one.
- The e2e suite sees `TroveBastionInterfaceMismatch` and `TroveBastionMainUnreachable` fire in the cluster's Prometheus
  (R-BAS-049).

### R-BAS-042 · Logs, traces and redaction
Priority: **MVP** · Covers: trove-only

- The request log of R-OPS-010 at the Bastion carries the client address, the class and result of R-BAS-040, the token hint
  (never the token), the main installation's `X-Request-Id` when one was involved, and the refusal reason. Blocked addresses are
  summarized (R-BAS-037).
- The Bastion starts a span per request and propagates W3C trace context to the main installation, so a forwarded request is one
  trace (R-OPS-018). The Bastion itself never accepts incoming trace context from callers (`tracing.incoming_context: none` is
  fixed for the role); the main installation accepts the Bastion's context when the Bastion's addresses are among its
  `tracing.trusted_peers`.
- `Authorization`, `Trove-Bastion-Authorization`, the token file and the signing key are redacted everywhere (R-OPS-013).

Acceptance criteria:

- A request through the Bastion and its forward share one trace ID in both request logs; no log line of the e2e run contains a
  token secret (scanner pattern of R-AUTH-022 over the collected logs).

### R-BAS-043 · Startup, readiness and drains
Priority: **MVP** · Covers: trove-only

- **Startup**: configuration validation (R-BAS-001), the token file (R-BAS-006), TLS material, the cache backend's first check,
  the listeners; then the handshake and the feed history (R-BAS-026), which do not gate readiness.
- **Readiness** does not depend on the main installation
  ([Q-BAS-44](#q-bas-44--unreachable-means-a-failed-handshake-or-a-stale-feed)): a replica that cannot reach it still serves what
  the grace period allows. It fails while draining, while the listeners are not bound, and before the cache backend's first
  check passed.
- **Drain** follows R-ARCH-024: readiness flips, the drain delay passes, in-flight requests finish within the termination grace
  period, the audit buffer is sent or spooled (R-BAS-039), the Valkey leases are released. A transfer cut at the deadline is
  resumed by the client with `Range` and `If-Range` on another replica (R-BAS-022).
- At least two replicas spread across nodes behind a PodDisruptionBudget that evicts one pod at a time (INV-12's rule for a
  long-running role); the chart enforces it with `highAvailability.enforce` (R-BAS-045).

Acceptance criteria:

- `kubectl drain` of the node of one Bastion replica during a 10-minute `npm ci` and `dotnet restore` loop on the e2e cluster
  causes zero failed client runs.
- A Bastion started while the main installation is down becomes ready, answers `503 main_unreachable`, and serves after the main
  installation comes back without a restart.

### R-BAS-044 · TLS on every connection of the Bastion
Priority: **MVP** · Covers: trove-only

Every connection the Bastion makes or accepts can run with TLS, and its material is reloaded without a restart (INV-20,
R-ARCH-035):

- its client listener (`server.tls`, the pair of `tls.existingSecret` or the cert-manager Certificate of the chart) and its ops
  listener;
- the connection to the main installation (`https` required: an `http` `bastion.main.url` stops startup; the trust is the
  system roots plus `trustedCA`);
- Valkey and Sentinel (R-ARCH-031), and the cache bucket (`https` endpoint, `trustedCA`);
- the chart's Ingress (`ingress.tls`), Service, probes and ServiceMonitor (R-HA-063's wiring).

Acceptance criteria:

- Rotating the listener certificate, the CA bundle and the Valkey client certificate on a running Bastion is picked up without a
  restart; `http://trove.example.com` as `bastion.main.url` stops startup.

## Chart

### R-BAS-045 · Chart `trove-bastion`: resources and naming
Priority: **MVP** · Covers: DIST-024

A second Helm chart for this instance type (owner, [Q-BAS-5](#q-bas-5--a-second-helm-chart-trove-bastion)), in
`deploy/helm/trove-bastion`, released with the chart `trove` by the Release Docker & Helm workflow, with its own SemVer and
`appVersion` = trove version (R-ARCH-025). It renders, for the release name `<fullname>`:

| Resource | Name | Note |
|---|---|---|
| Deployment, or StatefulSet with `cacheVolume.type: persistent` | `<fullname>` | `trove serve --role bastion`; container ports `https` 8443 and `ops` 8081 |
| ConfigMap | `<fullname>-config` | `/etc/trove/trove.yaml` from `config`, with the chart-managed keys |
| Service | `<fullname>` (`https`, no session affinity), `<fullname>-ops` | — |
| Ingress | `<fullname>` | host of `config.server.base_url`; `ingress.tls`; the annotations of `edgeLimit` and `cacheVolume.routeByPath` |
| Certificate (cert-manager), optional | `<fullname>-tls` | with `tls.certManager.enabled` |
| NetworkPolicy | `<fullname>` | R-BAS-047 |
| PodDisruptionBudget, HorizontalPodAutoscaler | `<fullname>` | — |
| ServiceAccount | `<fullname>` | `automountServiceAccountToken: false`; no Role, no RoleBinding |
| ServiceMonitor, PrometheusRule | `<fullname>` | R-BAS-041 |

- It renders no Secret: the Bastion token, the signing key, the bucket and Valkey credentials come from existing Secrets
  (`secrets.*`, created by GitOps, the internal-secrets-operator or the stackit-s3-provisioner).
- **Validation** (fail at `helm template`): `config.server.base_url` and `config.bastion.main.url` set and `https`;
  `secrets.mainToken.existingSecret` set; `secrets.signingKey.existingSecret` set with the bucket backend or Valkey;
  `config.bastion.cache.bucket.bucket` with the bucket backend; `routeByPath` only with the volume backend and `ingress.enabled`;
  `edgeLimit` wider than the Bastion's limits (R-BAS-038); with `highAvailability.enforce` at least two replicas, a PDB and
  host spreading; a value under `config` that is chart-managed or not in the role's schema.
- **HAProxy Ingress annotations** *(not verified on this page; pinned by the chart test against the local profile's
  controller)*: `haproxy-ingress.github.io/limit-rps`, `haproxy-ingress.github.io/limit-connections` and
  `haproxy-ingress.github.io/limit-whitelist` (from `config.bastion.blocking.exempt_cidrs`) for `edgeLimit`;
  `haproxy-ingress.github.io/balance-algorithm: uri` with consistent hashing for `routeByPath`. A controller other than HAProxy
  Ingress ignores them, and the chart's NOTES say so.

Acceptance criteria:

- `helm template` with only the required values (`config.server.base_url`, `config.bastion.main.url`,
  `secrets.mainToken.existingSecret`) renders a Deployment with two replicas, a PDB, the Ingress with the edge-limit annotations,
  a NetworkPolicy, and no Secret, no Role and no PersistentVolumeClaim.
- The chart test runs against HAProxy Ingress in the local profile: the edge limit refuses a flood, routing by path pins a path
  to one pod.

### R-BAS-046 · Values reference
Priority: **MVP** · Covers: DIST-024

The complete `values.yaml` of `trove-bastion`. Every value carries `# default (proposed)` or `# example`. Keys under `config`
are the Bastion's configuration file (R-BAS-001); the chart-managed keys (`server.tls`, `bastion.main.token_file`,
`bastion.main.ca_file`, `bastion.signing_key_file`, `bastion.cache.volume.dir`, `bastion.cache.bucket.ca_file`,
`bastion.replicas`, the Valkey secret files) are rendered by the chart and refused when set.

<details>
<summary>deploy/helm/trove-bastion/values.yaml</summary>

```yaml
# ── Global ──────────────────────────────────────────────────────────────────
global:
  imageRegistry: ""                     # default (proposed): empty = image.registry
  imagePullSecrets: []                  # default (proposed); example: [{name: registry-pull}]

nameOverride: ""                        # default (proposed)
fullnameOverride: ""                    # default (proposed)
commonLabels: {}                        # default (proposed)
commonAnnotations: {}                   # default (proposed)

image:
  registry: docker.io                   # default: Docker Hub, as the chart trove
  repository: guidedtraffic/trove       # default: the one trove image (R-BAS-001)
  tag: ""                               # default (proposed): empty = chart appVersion
  digest: ""                            # default (proposed); example: sha256:<64 hex>; wins over tag
  pullPolicy: IfNotPresent              # default (proposed)

highAvailability:
  enforce: true                         # default (proposed): ≥ 2 replicas, a PDB, DoNotSchedule host spreading (R-BAS-043)

# ── Bastion pods: trove serve --role bastion ─────────────────────────────────
replicaCount: 2                         # default (proposed); ignored when autoscaling.enabled; rendered into bastion.replicas
minReadySeconds: 10                     # default (proposed)
revisionHistoryLimit: 5                 # default (proposed)
strategy:
  type: RollingUpdate                   # default (proposed)
  rollingUpdate:
    maxSurge: 1                         # default (proposed)
    maxUnavailable: 0                   # default (proposed)
terminationGracePeriodSeconds: 120      # default (proposed): long transfers finish or are cut (R-BAS-043)
resources:
  requests:
    cpu: 500m                           # default (proposed)
    memory: 1Gi                         # default (proposed): answers and private copies live in memory
    ephemeral-storage: 1Gi              # default (proposed)
  limits:
    memory: 2Gi                         # default (proposed); no CPU limit by default
autoscaling:
  enabled: false                        # default (proposed)
  minReplicas: 2                        # default (proposed)
  maxReplicas: 6                        # default (proposed); rendered into bastion.replicas (R-BAS-025)
  targetCPUUtilizationPercentage: 70    # default (proposed)
  targetMemoryUtilizationPercentage: 0  # default (proposed): 0 = no memory metric
  metrics: []                           # default (proposed)
  behavior: {}                          # default (proposed): Kubernetes defaults
podDisruptionBudget:
  enabled: true                         # default (proposed)
  maxUnavailable: 1                     # default (proposed)
  minAvailable: null                    # default (proposed); mutually exclusive with maxUnavailable
  unhealthyPodEvictionPolicy: AlwaysAllow  # default (proposed)
topologySpread:
  enabled: true                         # default (proposed)
  nodeTaintsPolicy: Honor               # default (proposed)
  zone:
    maxSkew: 1                          # default (proposed)
    whenUnsatisfiable: ScheduleAnyway   # default (proposed)
  host:
    maxSkew: 1                          # default (proposed)
    whenUnsatisfiable: DoNotSchedule    # default (proposed); required with highAvailability.enforce
topologySpreadConstraints: []           # default (proposed); non-empty replaces the generated constraints
affinity: {}                            # default (proposed)
nodeSelector: {}                        # default (proposed)
tolerations: []                         # default (proposed)
priorityClassName: ""                   # default (proposed)
podAnnotations: {}                      # default (proposed)
podLabels: {}                           # default (proposed)

# ── Cache volume (R-BAS-030, R-BAS-033) ─────────────────────────────────────
cacheVolume:
  type: ephemeral                       # default (proposed): ephemeral | persistent | emptyDir; used with config.bastion.cache.backend volume
  storageClassName: ""                  # default (proposed): cluster default
  size: 25Gi                            # default (proposed); above config.bastion.cache.max_bytes
  accessModes: [ReadWriteOnce]          # default (proposed): ReadWriteOnce | ReadWriteOncePod
  routeByPath: false                    # default (proposed): URI hashing at HAProxy Ingress (R-BAS-033)

# ── Bastion configuration (rendered to /etc/trove/trove.yaml) ───────────────
config:
  server:
    listen: ":8443"                     # default (proposed)
    ops_listen: ":8081"                 # default (proposed)
    base_url: ""                        # required; example: https://packages.example.com
    trusted_proxies: []                 # default (proposed); example: ["10.244.0.0/16"] (the Bastion's HAProxy Ingress pods)
    forwarded_header: x-forwarded-for   # default (proposed): x-forwarded-for | forwarded
    max_in_flight: 1024                 # default (proposed), per replica
  bastion:
    main:
      url: ""                           # required; example: https://trove.example.com (the main endpoint)
      connect_timeout: 5s               # default (proposed)
      response_header_timeout: 30s      # default (proposed)
      max_concurrent: 64                # default (proposed), across all replicas (R-BAS-025)
      anonymous_share: 0.25             # default (proposed)
      queue_timeout: 2s                 # default (proposed)
      max_forwarded_body: 8MiB          # default (proposed): bodies of read routes such as npm's bulk audit (R-BAS-014)
      handshake_interval: 1m            # default (proposed)
      unreachable_after: 10s            # default (proposed); at least 3 × feed.poll_interval
      token_reload_interval: 10s        # default (proposed)
    answers:
      period: 5m                        # default (proposed); 30s to 1h
      grace_period: 15m                 # default (proposed; the owner's proposal); 0 to 24h
      max_entries: 200000               # default (proposed), per replica
    feed:
      poll_interval: 2s                 # default (proposed); 1s to 10s
    cache:
      backend: volume                   # default (proposed): volume | bucket
      max_bytes: 20GiB                  # default (proposed)
      max_object_bytes: 2GiB            # default (proposed)
      documents_max_bytes: 2GiB         # default (proposed)
      private_documents_max_bytes: 256MiB  # default (proposed), per replica, memory
      metadata_revalidate_after: 1m     # default (proposed)
      artifact_revalidate_after: 10m    # default (proposed)
      negative_period: 1m               # default (proposed)
      negative_max_entries: 100000      # default (proposed), per replica
      sweep_interval: 5m                # default (proposed)
      access_resolution: 1h             # default (proposed)
      bucket:
        endpoint: ""                    # default (proposed): AWS resolution; example: https://object.storage.eu01.onstackit.cloud
        region: ""                      # example: eu01
        bucket: ""                      # required with backend bucket; example: trove-bastion-partners
        prefix: trove-bastion           # default (proposed)
        force_path_style: false         # default (proposed)
        credentials:
          source: static                # default (proposed): static (secrets.cacheBucket) | webIdentity | default
          role_arn: ""                  # default (proposed); example: arn:aws:iam::123456789012:role/trove-bastion
    limits:
      authenticated:
        requests_per_second: 50         # default (proposed), per address
        burst: 200                      # default (proposed)
      anonymous:
        requests_per_second: 10         # default (proposed), per address
        burst: 40                       # default (proposed)
      misses:
        authenticated_per_minute: 300   # default (proposed), per address
        anonymous_per_minute: 30        # default (proposed), per address
    failed_auth:
      max_failures: 20                  # default (proposed)
      window: 10m                       # default (proposed)
      timeout: 15m                      # default (proposed)
    blocking:
      threshold: 300                    # default (proposed): refusals per window
      window: 1m                        # default (proposed)
      duration: 15m                     # default (proposed)
      max_duration: 24h                 # default (proposed)
      exempt_cidrs: []                  # default (proposed); example: ["198.51.100.0/24"]
    audit:
      buffer_events: 100000             # default (proposed), per replica
      batch_size: 500                   # default (proposed)
      flush_interval: 1s                # default (proposed)
      spool_max_events: 1000000         # default (proposed)
    token:
      warn_before: 720h                 # default (proposed): 30 days (the owner's proposal)
  valkey:                               # optional (R-BAS-034); the keys of the chart trove (R-HA-056), plus mode none
    mode: none                          # default (proposed): none | sentinel | standalone (development only)
    sentinel:
      addresses: []                     # required with mode sentinel; example: [trove-bastion-valkey-sentinel:26379]
      primary_name: ""                  # required with mode sentinel; example: trove-bastion
      tls: true                         # default (proposed)
      refresh_interval: 5s              # default (proposed)
    address: ""                         # default (proposed); standalone mode only
    database: 0                         # default (proposed)
    key_prefix: ""                      # default (proposed): empty = the host of config.server.base_url
    tls:
      mode: verify-full                 # default (proposed): verify-full | verify-ca | disable
      server_name: ""                   # default (proposed): host name of each address
    connect_timeout: 2s                 # default (proposed)
    command_timeout: 500ms              # default (proposed)
  log:
    level: info                         # default (proposed); operations-and-observability.md
  tracing:                              # operations-and-observability.md R-OPS-018
    enabled: false                      # default (proposed)
    endpoint: ""                        # example: https://otel-collector.observability:4318
    sample_ratio: 0.1                   # default (proposed)
  runtime:
    memory_limit_ratio: 0.85            # default (proposed)

# ── Existing Secrets (the chart never creates one) ──────────────────────────
secrets:
  mainToken:
    existingSecret: ""                  # required; example: trove-bastion-token (the trv_bst_ token, R-BAS-006)
    key: token                          # default (proposed)
  signingKey:
    existingSecret: ""                  # required with the bucket backend or Valkey; example: trove-bastion-signing (32 random bytes)
    key: key                            # default (proposed)
  cacheBucket:
    existingSecret: ""                  # required with credentials.source static; example: trove-bastion-s3 (stackit-s3-provisioner)
    accessKeyIdKey: access-key-id       # default (proposed)
    secretAccessKeyKey: secret-access-key  # default (proposed)
  valkey:
    existingSecret: ""                  # default (proposed): no authentication; example: trove-bastion-valkey
    usernameKey: username               # default (proposed)
    passwordKey: password               # default (proposed)
  valkeyCA:
    existingSecret: ""                  # default (proposed): system roots plus trustedCA
    key: ca.crt                         # default (proposed)

trustedCA:
  existingSecret: ""                    # default (proposed); example: corp-ca-bundle; trust for the main installation, Valkey and the bucket
  existingConfigMap: ""                 # default (proposed); example: corp-ca-bundle (trust-manager target)
  keys: [ca-bundle.crt]                 # default (proposed)

# ── TLS of the Bastion's own listener (R-BAS-044) ───────────────────────────
tls:
  enabled: true                         # default (proposed); false only with the explicit plaintext switch of INV-20
  existingSecret: ""                    # default (proposed): empty = the cert-manager Certificate below
  certManager:
    enabled: true                       # default (proposed)
    issuerRef:
      kind: ClusterIssuer               # default (proposed)
      name: ""                          # required with certManager.enabled; example: internal-ca
    duration: 2160h                     # default (proposed)
    renewBefore: 720h                   # default (proposed)

# ── Pod identity and security ──────────────────────────────────────────────
serviceAccount:
  create: true                          # default (proposed)
  name: ""                              # default (proposed): <fullname>
  annotations: {}                       # default (proposed); example (IRSA for the cache bucket):
  #  eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/trove-bastion  # example
  automountServiceAccountToken: false   # default (proposed): the Bastion never calls the Kubernetes API

podSecurityContext:
  runAsNonRoot: true                    # default (proposed)
  runAsUser: 65532                      # default (proposed)
  runAsGroup: 65532                     # default (proposed)
  fsGroup: 65532                        # default (proposed)
  fsGroupChangePolicy: OnRootMismatch   # default (proposed)
  seccompProfile:
    type: RuntimeDefault                # default (proposed)

securityContext:
  allowPrivilegeEscalation: false       # default (proposed)
  privileged: false                     # default (proposed)
  readOnlyRootFilesystem: true          # default (proposed)
  runAsNonRoot: true                    # default (proposed)
  capabilities:
    drop: [ALL]                         # default (proposed)
  seccompProfile:
    type: RuntimeDefault                # default (proposed)

tmp:
  sizeLimit: 1Gi                        # default (proposed); /tmp, and the spool of fills with the bucket backend

# ── Probes (paths from R-OPS-001) ──────────────────────────────────────────
probes:
  startup:
    enabled: true                       # default (proposed)
    path: /healthz                      # default (proposed)
    periodSeconds: 2                    # default (proposed)
    timeoutSeconds: 2                   # default (proposed)
    failureThreshold: 60                # default (proposed)
  liveness:
    enabled: true                       # default (proposed)
    path: /healthz                      # default (proposed)
    periodSeconds: 10                   # default (proposed)
    timeoutSeconds: 3                   # default (proposed)
    failureThreshold: 6                 # default (proposed)
  readiness:
    enabled: true                       # default (proposed)
    path: /readyz                       # default (proposed)
    periodSeconds: 5                    # default (proposed)
    timeoutSeconds: 3                   # default (proposed)
    failureThreshold: 1                 # default (proposed)
    successThreshold: 1                 # default (proposed)

# ── Exposure ───────────────────────────────────────────────────────────────
service:
  type: ClusterIP                       # default (proposed)
  port: 8443                            # default (proposed)
  opsPort: 8081                         # default (proposed)
  annotations: {}                       # default (proposed)
  labels: {}                            # default (proposed)
  ipFamilyPolicy: ""                    # default (proposed); example: PreferDualStack
  ipFamilies: []                        # default (proposed)

ingress:
  enabled: true                         # default (proposed): the Bastion is reached through its own Ingress
  className: haproxy                    # default (proposed): HAProxy Ingress, the owner's edge
  annotations: {}                       # default (proposed); appended to the generated ones
  labels: {}                            # default (proposed)
  hosts: []                             # default (proposed): empty = host of config.server.base_url
  tls: []                               # default (proposed); example: [{secretName: trove-bastion-public-tls, hosts: [packages.example.com]}]

edgeLimit:                              # stage 1 of the blocking (R-BAS-038)
  enabled: true                         # default (proposed)
  requestsPerSecond: 100                # default (proposed), per address; above config.bastion.limits.authenticated.requests_per_second
  connections: 50                       # default (proposed), per address

# ── Network policy (R-BAS-047) ──────────────────────────────────────────────
networkPolicy:
  enabled: true                         # default (proposed)
  ingress:
    https:
      from:                             # default (proposed): the ingress controller's namespace
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: haproxy-ingress  # default (proposed)
    ops:
      from:                             # default (proposed)
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring  # default (proposed)
    probeCIDRs: []                      # default (proposed)
  egress:
    enabled: true                       # default (proposed): the Bastion reaches nothing but the targets below
    dns:
      to:                               # default (proposed)
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kube-system  # default (proposed)
          podSelector:
            matchLabels:
              k8s-app: kube-dns         # default (proposed)
    main:
      to: []                            # required with egress.enabled; example: [{ipBlock: {cidr: 198.51.100.10/32}}]
      ports: [443]                      # default (proposed)
    valkey:
      to: []                            # default (proposed); example: [{podSelector: {matchLabels: {app.kubernetes.io/name: trove-bastion-valkey}}}]
      ports: [6379, 26379]              # default (proposed)
    cacheBucket:
      to: []                            # default (proposed); required with the bucket backend; example: [{ipBlock: {cidr: 0.0.0.0/0}}]
      ports: [443]                      # default (proposed)
    tracing:
      to: []                            # default (proposed)
      ports: [4318]                     # default (proposed)
    extra: []                           # default (proposed)

# ── Monitoring (R-BAS-040, R-BAS-041) ──────────────────────────────────────
metrics:
  serviceMonitor:
    enabled: false                      # default (proposed)
    namespace: ""                       # default (proposed): release namespace
    interval: 30s                       # default (proposed)
    scrapeTimeout: 10s                  # default (proposed)
    labels: {}                          # default (proposed); example: {release: kube-prometheus-stack}
    relabelings: []                     # default (proposed); appended after the fixed role relabeling (role="bastion")
    metricRelabelings: []               # default (proposed)
  prometheusRule:
    enabled: false                      # default (proposed)
    labels: {}                          # default (proposed)
    disabledAlerts: []                  # default (proposed); example: [TroveBastionCacheCorrupt]
    extraRules: []                      # default (proposed)

# ── Extension points ───────────────────────────────────────────────────────
extraEnv: []                            # default (proposed)
extraEnvFrom: []                        # default (proposed)
extraVolumes: []                        # default (proposed)
extraVolumeMounts: []                   # default (proposed)
initContainers: []                      # default (proposed)
sidecars: []                            # default (proposed)
extraObjects: []                        # default (proposed)
```

</details>

Acceptance criteria:

- `helm show values deploy/helm/trove-bastion` equals this block, except for formatting; a CI test compares the key sets.
- The `config` subtree is validated against the schema the binary emits for the role `bastion` (R-HA-029's mechanism), so a
  key the binary does not know fails `helm install`.

### R-BAS-047 · Network policy
Priority: **MVP** · Covers: trove-only

- Ingress: the `https` port only from the ingress controller's namespace (the Bastion is reached through its Ingress, never
  directly), the ops port only from monitoring, the probe CIDRs.
- Egress on by default, unlike the chart `trove`: DNS, the main installation's address (`networkPolicy.egress.main`), Valkey, the
  cache bucket and the span collector, nothing else. The Bastion contacts no upstream registry, no OIDC issuer and no
  PostgreSQL, so an attacker who takes over a Bastion pod reaches only what it already reaches.

Acceptance criteria:

- From a Bastion pod, a connection to the main installation's PostgreSQL Service and to `registry.npmjs.org:443` is refused by
  the policy on the CNI of the local profile, while the e2e suite passes.

## Tests

### R-BAS-048 · Integration suite against an in-process main installation
Priority: **MVP** · Covers: trove-only

Go tests in `internal/bastion` and `test/integration/bastion` start an in-process main installation (the `web` role against
the integration stack's PostgreSQL, Valkey with Sentinel and S3) and one or two in-process Bastion replicas, each with and
without Valkey and with both cache backends (the volume backend on a temporary directory, the bucket backend on the stack's
S3). Test names carry the requirement IDs (`TestR_BAS_0nn_…`). The suite holds every Go-side acceptance criterion of this page,
at least:

- cold and warm cache for npm and NuGet, with `$FULL`, `$PART`, `$NONE` and anonymous callers;
- revocation by every feed kind, with the 5 s bound; an answer obtained before a revocation and stored after it;
- the main installation unreachable (a fault-injecting transport): grace, `503` for unseen tokens, nothing after;
- interface mismatch; base URL mismatch; a revoked Bastion token;
- write probes, the read route with a body, the marking contract over the route table;
- the negative cache, the budget, collapsing, the limits, the failed-authentication timeout, blocking with and without Valkey;
- corrupt cache content, forged entries and answers, a feed reset.

Acceptance criteria:

- `make test-integration` runs the suite, and the coverage generator records every requirement of this page whose criteria
  are integration tests from its passing test names.

### R-BAS-049 · End-to-end suite on kind with npm and NuGet
Priority: **MVP** · Covers: trove-only

The e2e driver ([developer/e2e.md](../developer/e2e.md)) installs the chart `trove` and, in a second namespace, the chart
`trove-bastion` with two replicas behind HAProxy Ingress, once with the volume backend and once with a SeaweedFS bucket and
Valkey. The clients are npm, pnpm, Yarn 4 and `dotnet restore`, run from a pod outside the Bastion's namespace through the
Bastion's Ingress:

| Scenario | Pass condition |
|---|---|
| Cold cache | `npm ci` and `dotnet restore` succeed with `$FULL`; every tarball and package reaches the main installation once |
| Warm cache | a second run with an empty client cache within `metadata_revalidate_after` sends no request for a tarball, a `.nupkg` or a document to the main installation; after it, only conditional requests that transfer no artifact |
| Main installation unreachable | NetworkPolicy cut: warm runs succeed for the grace period, a cold package and an unseen token get `503`, everything `503` after `period + grace_period`; `TroveBastionMainUnreachable` fires |
| Token revoked at the main installation | a client loop is refused within 5 s on both replicas |
| Interface mismatch | an upgrade of the main installation to `InterfaceVersion + 1` fires `TroveBastionInterfaceMismatch`; the Bastion's upgrade clears it |
| Flooding address | an address sending 2,000 requests a minute is blocked on both replicas; another address is unaffected |
| Failed-authentication timeout | 21 random tokens put the address into the timeout; a known token from it keeps working |
| Anonymous miss flood | anonymous requests for 10,000 random names within a minute send at most the anonymous miss budget and the anonymous share of `max_concurrent` to the main installation |
| Read-only | `npm publish` and `dotnet nuget push` are refused in the client's shape, pointing at the main installation |
| Drain | `kubectl drain` of one Bastion replica's node during a 10-minute client loop: zero failed runs |
| Hidden content | a version hidden by an exposure rule or a permission at the main installation is hidden at the Bastion, cached or not |

- The drain, the flood and the anonymous flood are long tests: they run nightly, on dispatch and locally, not in the release
  path, and a red long leg is a finding, not a blocker (the owner's rule of 2026-11-01). The other scenarios run with every
  change to the Bastion.

Acceptance criteria:

- The e2e job `bastion` runs the short scenarios on every push that changes `internal/bastion`, `deploy/helm/trove-bastion` or
  this page, and the nightly job runs all of them.

## Deferred

| Item | Note |
|---|---|
| Host-root bindings at the Bastion | The Bastion mirrors path-based URLs only. Formats that need a host root (OCI's `/v2/`, Terraform's discovery, R-EP-006 and R-EP-007, P1) need a Bastion host per binding; specified with those formats. |
| Several base URLs per Bastion | One `server.base_url` per Bastion; a second host is a second release of the chart. |
| A Bastion in front of a Bastion | Not supported: a Bastion's upstream is a main installation, which alone has the interface. |
| An address binding of the Bastion token | The main installation accepts the token from any address (H-386); a list of allowed client addresses on the registration is not specified. |
| Prefetching | The cache fills only on demand; warming it from the main installation's download statistics is not specified. |
| Properties, source-absence display, transitive search, parallel chunked transfer | RMT-042, RMT-043, RMT-045 and RMT-046 stay P2 (Q-BAS-47). |

## Security notes

- **What the internet reaches.** The Bastion's ingress and its read flow; no management API, no login, no UI (R-BAS-003). Every
  decision about a caller is the main installation's, made by its own routes or its decision point (R-BAS-011, R-BAS-013); the
  Bastion only replays it for a bounded time (R-BAS-020).
- **What the Bastion token can do.** Check credentials, read the feed, submit download events; never read a package or write
  (R-BAS-005). Its holder's remaining powers are H-386.
- **What a compromised Bastion pod reaches.** Its own cache and Valkey, the main installation's interface with the Bastion token,
  and, through forwarded requests, only what the credentials of callers passing through it can read; it sees those credentials
  in transit. It cannot reach PostgreSQL, the main installation's object store or upstream registries (R-BAS-047).
- **Where abuse stops.** At the network provider for bandwidth (H-382), at HAProxy Ingress for coarse rates (R-BAS-038), at the
  Bastion for credentials, misses and blocks (R-BAS-035 to R-BAS-037), and at the concurrency budget for what reaches the main
  installation (R-BAS-025); the main installation's own limits still apply to the real client (R-BAS-017).
- The perspective page is [../security/bastion.md](../security/bastion.md).

## Decisions

Each question below was decided by the development agent from the listed options, and the owner reviews the choices at the
walkthrough, except Q-BAS-1 and Q-BAS-2, which the owner decided on 2026-10-31, and Q-BAS-3 to Q-BAS-21, which the owner
decided on 2026-11-02.

### Q-BAS-1 · The Bastion asks the main installation and keeps no copy of users, tokens or policies

Status: **Decided by the owner on 2026-10-31** — option (a).

Context: the owner's request of 2026-10-31 for a smaller instance close to its consumers, moved into the beta on 2026-11-02 as an
exposed artifact instance that gives external parties read access, caches artifacts and takes as much load as possible off the
main installation, exposed to the internet and to denial-of-service attempts.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Ask the main installation and keep its answers briefly (chosen) | The Bastion holds no identities; it asks whether a credential is valid and what it may read | No copy to synchronize or leak; revocation stays the main installation's | Every new caller costs the main installation a request |
| (b) Replicate users, tokens and policies to the Bastion | An identity federation to the instance | Works without the main installation | A second copy of every secret hash and policy in a DMZ; a synchronization protocol |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion asks the main installation whether a credential is valid and what it may read, and
keeps the answers for a short, bounded time; it holds no copy of users, tokens or policies." The development agent records no
revisit condition.

**Applied in the spec:** R-BAS-011, R-BAS-020; [ADR-142](../decisions.md#adr-142--the-trove-bastion-an-internet-facing-read-only-cache-instance-in-the-beta).

### Q-BAS-2 · The Bastion is read-only for package content

Status: **Decided by the owner on 2026-10-31** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read-only (chosen) | Publishes, unpublishes and tag writes are refused in the protocol's shape and point at the main installation | An internet-facing instance takes no content | Publishers use the main installation |
| (b) Writes forwarded to the main installation | The Bastion as a write proxy | One URL for everything | Upload bodies through a DMZ instance; a second write path to secure |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "publish, unpublish and tag writes answer in each protocol's shape and point at the main
installation." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-014 (the shape through Q-BAS-38).

### Q-BAS-3 · The Bastion's credential is a token issued by an administrator of the main installation

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A token issued by an administrator of the main installation (chosen) | A bearer token in the Bastion's Secret | Passes any reverse proxy; revocable like every token | A secret to renew (Q-BAS-19) |
| (b) A client certificate | Mutual TLS as the Bastion's identity | No bearer secret | A TLS-terminating reverse proxy in front of the main installation would not pass it on |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion's own credential towards the main installation is a token that a trove
administrator of the main installation issues for the Bastion (read only, nothing else); a client certificate is not its
identity, because a TLS-terminating reverse proxy in front of the main installation would not pass it on." The development
agent records no revisit condition.

**Applied in the spec:** R-BAS-004, R-BAS-005; the token's form in Q-BAS-23.

### Q-BAS-4 · The Bastion connects to the main endpoint and replaces the customer endpoint

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The main endpoint (chosen) | The Bastion talks to the default endpoint of the main installation | One endpoint to secure; the Bastion is the outside entry point | The default endpoint's exposure applies to Bastion callers |
| (b) A further endpoint for Bastions | A listener with its own rules ([endpoints.md](endpoints.md)) | Separate exposure rules | Two outside entry points for one purpose |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "The Bastion connects to the main endpoint of the main installation: the Bastion replaces the
customer endpoint of the earlier plans." The development agent records no revisit condition; the further endpoints of
[endpoints.md](endpoints.md) stay a capability of the main installation, which the Bastion does not use.

**Applied in the spec:** R-BAS-008, R-BAS-017.

### Q-BAS-5 · A second Helm chart `trove-bastion`

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A chart of its own (chosen) | `trove-bastion`, with its own Ingress, Service, NetworkPolicy and TLS Secret | A DMZ install that cannot render the main installation's resources by mistake | A second chart to release |
| (b) A mode of the chart `trove` | One chart with a Bastion switch | One chart | The main chart's required values (database, storage) do not apply |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "A second Helm chart (`trove-bastion`) for this instance type … with its own Ingress, Service,
NetworkPolicy and TLS Secret; horizontal replicas without PostgreSQL of its own if the design allows, otherwise the smallest
state that keeps the replicas consistent." The design allows it: the Bastion has no PostgreSQL (R-BAS-001). The development
agent records no revisit condition.

**Applied in the spec:** R-BAS-001, R-BAS-045, R-BAS-046; cross-page: INV-13 in README.md.

### Q-BAS-6 · A caller reads what its token may read; there is no release step

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The token's right (chosen) | A caller reads through the Bastion exactly what its token may read on the main installation | One permission model; nothing to release separately | Permissions alone decide what outsiders see |
| (b) A release step for the Bastion | Content is published to the Bastion explicitly | A second gate | A second model, and stale releases |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "there is no release step of its own. The right is the token's: a caller reads through the
Bastion exactly what its token may read on the main installation, and read access behaves the same on both." The development
agent records no revisit condition.

**Applied in the spec:** R-BAS-012, R-BAS-019.

### Q-BAS-7 · The Bastion fetches with the caller's token, and its own token reads no package

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The caller's token (chosen) | A miss is forwarded with the caller's credential; the main installation decides and serves | Every fetch is the caller's own decision; the Bastion's token needs no read right | A cached artifact needs an answer per caller |
| (b) The Bastion's own read token | The Bastion fetches with a token that reads everything and decides itself | Fewer requests | A token in a DMZ that reads everything, and a copy of the decision |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "On a cache miss it forwards the caller's token, the main installation decides and serves, and
the Bastion caches the bytes; a cached artifact is served to another caller only after the main installation said that this
caller may read it (an answer kept for the bound). The Bastion's own token identifies the Bastion and may only have tokens
checked; it reads no package." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-005, R-BAS-021, R-BAS-022.

### Q-BAS-8 · Answers are kept for a period and a revocation feed drops them within seconds

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A configurable period plus a feed polled every few seconds (chosen) | Revocations and narrowings within seconds, widenings within the period | Few checks, fast revocation | A feed to build on the main installation |
| (b) The period alone | Answers expire | Simple | A revoked token reads on for the period |
| (c) Ask on every request | No kept answers | Exact | The main installation carries every request |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion keeps the main installation's answers for a configurable period (some minutes) and
follows a revocation feed of the main installation every few seconds, so a revoked token or a narrowed right is dropped within
seconds; widening a right may take the period." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-015, R-BAS-020, R-BAS-026.

### Q-BAS-9 · The main installation unreachable: grace period, 503 for unseen tokens, then nothing

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A grace period (chosen) | Held answers stay valid for a configurable period beyond their normal one | Consumers keep working through short outages | Revocations during the outage are not seen (H-383) |
| (b) Fail closed at once | Nothing without the main installation | No stale decision | Every outage of the main installation is one of the Bastion |
| (c) Serve on indefinitely | Answers never expire while unreachable | Availability | Unbounded stale access |

**Choice:** (a), the owner's decision, with 15 minutes proposed.

**Rationale:** the owner's words: "answers the Bastion already holds stay valid for a configurable grace period (proposed 15
minutes) beyond their normal period; callers with a valid answer get cached artifacts and possibly stale metadata, a token the
Bastion has not seen gets `503`, and after the grace period it serves nothing." The development agent records no revisit
condition.

**Applied in the spec:** R-BAS-020, R-BAS-027; H-383.

### Q-BAS-10 · The cache: an own bucket or a local volume, never the main installation's object store

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An own bucket or a local volume, chosen in the chart (chosen) | The Bastion's cache is its own | The DMZ instance holds no credential for the main installation's blobs | A second copy of what it serves |
| (b) The main installation's object store | Read the blobs directly | No copy | A DMZ instance with read access to every blob |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "a small part of the artifacts, never the main installation's object store, which the Bastion
cannot read; its backend is chosen in the chart, a bucket of the Bastion's own shared by its replicas or a local volume per
replica, with size limit, eviction by last access and the metadata revalidation period configurable." The development agent
records no revisit condition.

**Applied in the spec:** R-BAS-030, R-BAS-031, R-BAS-046.

### Q-BAS-11 · Valkey is optional, and without it budgets are divided by the replicas

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Optional Valkey for shared state (chosen) | Block list, counters, fetch coordination and answers shared; without it per replica | Small installs need no Valkey; large ones share | Two modes to test |
| (b) Valkey required | As the main installation | One mode | A Valkey in every DMZ |
| (c) No Valkey | Per replica always | Simplest | Every replica asks again; blocks per replica |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "Valkey is optional. With it, the replicas share blocked addresses, rate-limit and failed-sign-in
counters, the coordination of fetches and the main installation's answers; without it, each replica works alone with the budgets
divided by the number of replicas; losing Valkey costs performance, never correctness (INV-3). With a local volume per replica,
the chart can route by path so that each artifact lives on one replica." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-025, R-BAS-033, R-BAS-034; the form of shared answers in Q-BAS-32; cross-page: INV-3 in
README.md.

### Q-BAS-12 · Anonymous reads as on the main installation, with misses absorbed at the Bastion

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) As on the main installation, with absorption (chosen) | Anonymous exactly when the main installation allows it; a negative cache, a budget and stricter limits | Public content stays public; floods stay at the Bastion | Anonymous floods cost the Bastion |
| (b) No anonymous reads at the Bastion | Tokens only | No anonymous flood path | Behaves differently from the main installation |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "as on the main installation; the Bastion admits anonymous requests exactly when the main
installation does. Because an anonymous request costs an attacker nothing, the Bastion absorbs misses itself: a negative cache for
names the main installation does not have, a budget of concurrent requests to the main installation that misses cannot exceed,
and stricter per-address limits for requests without a credential." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-019, R-BAS-024, R-BAS-025, R-BAS-035.

### Q-BAS-13 · Downloads served from the cache are reported to the main installation's audit

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Batches to the main installation's audit (chosen) | Who, what, when, address, origin Bastion; buffered while unreachable | One audit trail for every download | Events can be lost to a full buffer (counted) |
| (b) The Bastion's own log only | Downloads in the Bastion's request log | Nothing to send | The audit trail misses every cached download |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion reports the downloads it serves from its cache to the main installation in batches
(who, what, when, address; origin Bastion), under the same `audit.record_downloads` switch; it buffers them, bounded, while the
main installation is unreachable and sends them later; events lost to a full buffer are counted and reported." The development
agent records no revisit condition.

**Applied in the spec:** R-BAS-016, R-BAS-039; H-391.

### Q-BAS-14 · No web interface at the Bastion

Status: **Decided by the owner on 2026-11-02** — option (a).

Context: the earlier plan bullet "same authentication as the main installation … and the same login" is narrowed by this
decision to tokens.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Package clients and raw downloads with tokens only (chosen) | No login, no session, no UI | The smallest internet-facing surface | Browsing happens on the main installation |
| (b) The UI and the login at the Bastion | A full web surface | Browsing for outsiders | Sessions, OIDC and the UI in a DMZ |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion serves the package clients and raw downloads with tokens only; no login, no
session, no UI." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-003, R-BAS-019.

### Q-BAS-15 · The Bastion is configured only through its chart

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only chart values (chosen) | Every value in the chart; a change is a rollout | GitOps; nothing to administer in a DMZ | A change waits for a rollout |
| (b) Settings held at the main installation and pulled by the Bastion | Central administration | Changes without rollouts | A second source of truth |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion has no settings held anywhere else; every value (main installation, its token,
cache, periods, limits, blocking, trusted proxies) is a chart value, and a change is a rollout." The development agent records no
revisit condition.

**Applied in the spec:** R-BAS-002, R-BAS-046.

### Q-BAS-16 · Blocking in two stages

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two stages (chosen) | The Bastion enforces what needs trove's knowledge; HAProxy Ingress a coarser, switchable, wider limit | Each stage does what it can know | Two configurations |
| (b) The Bastion alone | Every limit in trove | One place | Floods reach the pods |
| (c) The ingress alone | Rate limits at the edge | No code | Cannot tell credentials, failures or misses |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion itself enforces the rules that need trove's knowledge (failed authentication,
misses, block duration, a block list shared through Valkey); its chart can put a coarser per-address limit in front at HAProxy
Ingress, which can be switched off and is set wider than the Bastion's own. Volumetric attacks on bandwidth are the network
provider's, not the Bastion's." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-035 to R-BAS-038; H-382.

### Q-BAS-17 · The Bastion mirrors the main installation's URL space

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The same paths, rendered by the main installation for the Bastion's host (chosen) | No repository list; forwarded host; `trusted_proxies` and `additional_base_urls` at the main installation | No format knowledge at the Bastion | Two settings at the main installation |
| (b) A repository list and URL rewriting at the Bastion | The Bastion rewrites documents | The main installation unchanged | Format knowledge in a DMZ instance |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion holds no repository list; every repository of the main installation is reachable
under the same paths, and the main installation renders documents for the Bastion's host (the Bastion sends its base URL as the
forwarded host; the main installation lists the Bastion in `trusted_proxies` and `additional_base_urls`), so the Bastion carries
no format knowledge." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-010, R-BAS-017, R-BAS-018.

### Q-BAS-18 · Metadata: shared for confirmed full readers, filtered documents per caller

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Marked documents shared with confirmed full readers (chosen) | A filtered document is never shared and goes per caller with a conditional request | Most callers read whole repositories; nobody sees another's view | Two document paths |
| (b) Every document per caller | Conditional request on every use | One path | The main installation carries every document request |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the main installation marks a document that is the same for every caller who reads the whole
repository; the Bastion serves that copy for the metadata period to each caller for whom the main installation confirmed a full
read of the repository (an answer kept like the others, under the revocation feed). A filtered document is never shared: it goes
to the main installation per caller with a conditional request. Artifacts are shared after the main installation said the caller
may read them." What "reads the whole repository" means is Q-BAS-28; search is Q-BAS-41. The development agent records no
revisit condition.

**Applied in the spec:** R-BAS-012, R-BAS-013, R-BAS-022, R-BAS-023.

### Q-BAS-19 · The Bastion token is renewed by hand with an overlap

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) By hand with an overlap, reloaded without a restart (chosen) | Second token, GitOps, old one revoked; metric and warning | No write right to Secrets in the DMZ | A yearly manual step |
| (b) Automatic renewal by the Bastion | The Bastion exchanges its token for a new one and writes its Secret | No manual step | A DMZ instance that writes Secrets and issues itself tokens |

**Choice:** (a), the owner's decision, with 30 days proposed for the warning.

**Rationale:** the owner's words: "renewed by hand with an overlap: an administrator issues a second token before the first
expires, it reaches the Bastion's Secret through GitOps, the old one is revoked; the Bastion reloads a changed Secret without a
restart, exports the remaining lifetime as a metric and warns in its log from a chart-set period before expiry (proposed 30
days). The Bastion holds no right to write Secrets or to issue tokens." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-006, R-BAS-041.

### Q-BAS-20 · The Bastion interface has a version of its own, equal on both sides

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A version of its own, equality required, mismatch alerted (chosen) | Raised only when the interface changes | Bastion and main installation fail visibly, never silently wrong | Lockstep upgrades when it changes |
| (b) The release number | Versions compared by release | Nothing to maintain | Every release would break every Bastion |
| (c) Tolerance across versions | Each side reads several versions | Independent upgrades | A compatibility matrix in a security interface |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "the Bastion interface of the main installation (token checks, revocation feed, audit intake)
has a version of its own, separate from the release number, raised only when that interface changes, which after the stable
release is rare. Main installation and Bastion talk only when they speak the same interface version; on a mismatch the Bastion
treats the main installation as unreachable (the grace period, then nothing) and raises a Prometheus alert (a metric plus a rule
its chart ships)." The development agent records no revisit condition.

**Applied in the spec:** R-BAS-009, R-BAS-028, R-BAS-041; how a change is guarded in Q-BAS-26.

### Q-BAS-21 · Internet exposure: limits, blocking, failed-sign-in timeouts and a configurable cache

Status: **Decided by the owner on 2026-11-02** — option (a).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Protection at the Bastion, the real client counted at the main installation (chosen) | Per-address limits, automatic blocks, failed-authentication timeouts; the main installation trusts the Bastion's forwarded address | Abuse stops before the main installation; the main installation's limits hit the client | The Bastion's addresses must be listed at the main installation |
| (b) The main installation's limits alone | The Bastion forwards everything | Nothing new | Every flood reaches the main installation, counted against the Bastion's address |

**Choice:** (a), the owner's decision.

**Rationale:** the owner's words: "rate limits per client address, automatic blocking of addresses that flood it, a timeout for
an address after too many failed authentication attempts, and a configurable cache (size, retention, metadata revalidation)",
and "the Bastion stops abuse before a request reaches the main installation, and the main installation lists the Bastion's
addresses in `server.trusted_proxies`, so its per-address limits apply to the client and not to the Bastion." The development
agent records no revisit condition.

**Applied in the spec:** R-BAS-017, R-BAS-030, R-BAS-031, R-BAS-035 to R-BAS-037.

### Q-BAS-22 · The Bastion is a role of the trove image

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The role `bastion` of the one image (chosen) | `trove serve --role bastion`, with a configuration schema of its own | INV-1 and INV-2 as they are; the Bastion reuses the TLS reloader, limiters, verifying reader, Valkey client and request log; one release | The image carries code the role never enables |
| (b) A binary and image of its own | `trove-bastion` built separately | A smaller binary | A second build, image and release; shared code drifts |
| (c) A mode of the `web` role | `web` with a Bastion switch | No new role | The database pool and the format routes stay in an internet-facing process |

**Choice:** (a).

**Rationale:** INV-2's roles exist to turn subsystems on and off in one binary. What the internet reaches is the role's route
table, which for `bastion` holds no format, API or UI route, and the role's schema refuses every key of another subsystem. The
development agent would revisit this if the image's size or the vulnerability reports of dependencies the role never runs become
an operational burden for DMZ installations.

**Applied in the spec:** R-BAS-001; cross-page: INV-2 and INV-13 in README.md.

### Q-BAS-23 · The Bastion token is a token type of its own on a Bastion principal

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A service-account token with a scope that only holds an interface capability | `trv_sat_` of a service account | No new type | "Reads no package" rests on the scope and on no group or policy ever reaching the service account; one mistake makes it a reader |
| (b) The type `bst` (`trv_bst_`) on a principal of the kind `bastion` (chosen) | Refused by type on every route but the interface, before a lookup; the principal holds no grants | "Reads no package" is structural; scanners recognise the type; the audit names the Bastion | A type, a principal kind and a migration |
| (c) A shared secret in both charts | Static configuration | Nothing in the database | No issuance, expiry or revocation by an administrator |

**Choice:** (b).

**Rationale:** the owner excluded that the Bastion's token reads a package (Q-BAS-7). (a) would make that a property of a scope
and of policy hygiene; (b) makes it a property of the token's type, enforced by R-EP-010's type check before the token table is
read. The development agent would revisit this if a second kind of machine client needs the same shape; both would then share a
machine principal kind.

**Applied in the spec:** R-BAS-004, R-BAS-005; cross-page: INV-8 in README.md, the table and scanner pattern of R-AUTH-022, the
table of R-AUTHZ-002; [ADR-144](../decisions.md#adr-144--the-bastion-token-is-a-type-of-its-own-and-the-interface-a-version-of-its-own).

### Q-BAS-24 · Bastion registrations are managed with the endpoint capabilities

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `endpoints:read` and `endpoints:write`, plus `tokens:revoke` for a revocation (chosen) | Whoever manages outside entry points manages Bastions | No new capability; the security administrator can revoke a leaked token | Endpoint and Bastion administration cannot be separated |
| (b) New capabilities `bastions:read`, `bastions:write` | A row of their own | Separable | One more row in every role |
| (c) `admin` only | No delegation | Simplest | A security administrator could not revoke |

**Choice:** (a).

**Rationale:** the Bastion replaces the customer endpoint (Q-BAS-4); the roles that decide what outsiders can reach through an
endpoint decide it through a Bastion. The development agent would revisit this if an installation needs Bastion administrators
who must not change endpoints.

**Applied in the spec:** R-BAS-004, R-BAS-007; cross-page: the `endpoints:*` row of R-AUTHZ-010.

### Q-BAS-25 · The interface lives under `/api/v1/bastion/` on the main endpoint

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `/api/v1/bastion/…` and marking on the normal routes, the Bastion token in `Trove-Bastion-Authorization` (chosen) | One interface on the main endpoint; the caller's `Authorization` untouched | Passes the main installation's reverse proxy; in the OpenAPI document; a forwarded read is decided exactly as the caller's own | A header the main installation must redact |
| (b) A listener of its own for Bastions | A port for the interface | Separation | Another exposure through the main installation's ingress, against Q-BAS-4 |
| (c) A host-root prefix | `/-/bastion/…` | Outside the API | Host-root routes are reserved for clients that need them (INV-6) |

**Choice:** (a).

**Rationale:** the owner fixed the main endpoint and a token that survives a TLS-terminating proxy (Q-BAS-3, Q-BAS-4). Keeping
the caller's credential in its own header and the Bastion's in another lets the forwarded request run through the main
installation's unchanged authentication stage. The development agent would revisit this if a main installation's proxy strips
unknown headers; the handshake would show it, because the marking would be missing.

**Applied in the spec:** R-BAS-008, R-BAS-013; [ADR-144](../decisions.md#adr-144--the-bastion-token-is-a-type-of-its-own-and-the-interface-a-version-of-its-own).

### Q-BAS-26 · Every change of the interface raises its version

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Any change raises it, guarded by a surface file (chosen) | `interface.txt` with the version line; the test refuses a changed surface under the same version | Equality means both sides know every field | Lockstep upgrades even for an addition |
| (b) Additions within a version | Only breaking changes raise it | Fewer raises | With equality required, the latitude buys nothing and needs a reader of unknown fields |
| (c) Review only | A prose rule | No tooling | A change passes unnoticed |

**Choice:** (a).

**Rationale:** the owner requires equal versions (Q-BAS-20), so the version must change exactly when the interface does; the
surface file turned the same rule into a failing test for the format contract
([ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)). The development agent would revisit this after
the stable release if lockstep upgrades become a burden; a tolerance range would change Q-BAS-20 and needs the owner.

**Applied in the spec:** R-BAS-009; cross-page: the table of R-ARCH-025;
[ADR-144](../decisions.md#adr-144--the-bastion-token-is-a-type-of-its-own-and-the-interface-a-version-of-its-own).

### Q-BAS-27 · A cached artifact is checked through the real route

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A conditional `GET` with the caller's credential through the real route (chosen) | `304` means "this caller may read it, unchanged" | The same decision, hidden existence and serve-time checks as the caller's own request, by construction; answered from PostgreSQL (R-STOR-012) | One request per artifact and period for callers who do not read the whole repository |
| (b) The check classifies a path into a coordinate | The check operation decides paths | Batchable | A second classifier next to the format's routes; a disagreement serves bytes |
| (c) The Bastion parses paths per format | Format knowledge at the Bastion | No request | Against Q-BAS-17 |

**Choice:** (a).

**Rationale:** an authorization path that differs from the one the request itself takes is where such designs leak; (a) has
none. Callers who read the whole repository skip the per-artifact request (Q-BAS-28). The development agent would revisit this if
callers with narrowed reads dominate and their conditional requests show in the main installation's load; a batch form of (a)
would follow.

**Applied in the spec:** R-BAS-013, R-BAS-022; [ADR-143](../decisions.md#adr-143--the-bastion-decides-through-the-main-installations-own-routes-and-answers-it-keeps-briefly).

### Q-BAS-28 · What "reads the whole repository" means

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The shape `full`: `ReadAll` with the populate right of remote members, over every member of a virtual repository (chosen) | One rendition and one ETag for every `full` subject | Matches the inputs of npm's ETag (R-NPM-013) | A reader without the populate right is `partial` |
| (b) `ReadAll` on the addressed repository | `authz.ReadScoper` as it is | No new computation | `ReadNone` for every virtual repository: nothing would be shared |
| (c) Read rights without the populate right | Fill rights ignored | More callers share | Shares bytes the main installation renders differently for that caller |

**Choice:** (a).

**Rationale:** the marking must mean "these bytes are what the main installation would send this caller"; (a) holds that for
every input the documents depend on. The development agent would revisit this if a format's documents vary by another
caller-dependent input; R-BAS-013's kit check would show it.

**Applied in the spec:** R-BAS-012, R-BAS-013; [ADR-143](../decisions.md#adr-143--the-bastion-decides-through-the-main-installations-own-routes-and-answers-it-keeps-briefly).

### Q-BAS-29 · The feed reads the journal's events from the audit table

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `audit_event` rows of the listed types by `(occurred_at, id)` with a lookback (chosen) | The journal's own events; IDs deduplicate | No new write path; the lookback follows the replay margin of R-HA-054 | Each poll re-reads the lookback window |
| (b) The change log of R-ARCH-018 | Gapless epochs of `auth` and `authz` | Exact order | Internal payloads, broadenings included; pruning forces resets |
| (c) A feed table with a sequence | Written by the audit writer | A plain cursor | A second write and a serialization point in every journaled transaction |

**Choice:** (a).

**Rationale:** the owner built the feed on the revocation journal (Q-BAS-8); (a) reads exactly what the journal flags. The
development agent would revisit this if journaled events become so frequent that the lookback window exceeds a page per poll.

**Applied in the spec:** R-BAS-015; [ADR-143](../decisions.md#adr-143--the-bastion-decides-through-the-main-installations-own-routes-and-answers-it-keeps-briefly).

### Q-BAS-30 · The feed also carries withdrawn content

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Access removals, withdrawn content (unpublish, delete, from M4b blocks) and kinds that drop narrowly (chosen) | `token`, `principal`, `grants`, `settings`, `content` | A withdrawn or blocked version leaves the cache within seconds | Events without a target drop every answer |
| (b) Access removals only | The journal's access events | Smallest | Withdrawn versions stay served until revalidation |
| (c) Every package change, publishes included | A full change feed | New versions at once | A publish per entry; against the rule for change areas |

**Choice:** (a).

**Rationale:** withdrawals are rare and urgent, publishes frequent and never narrowing. The development agent would revisit this
if `grants` entries become frequent enough to raise check traffic measurably; they would then carry the affected repositories.

**Applied in the spec:** R-BAS-015, R-BAS-022, R-BAS-023; H-385;
[ADR-143](../decisions.md#adr-143--the-bastion-decides-through-the-main-installations-own-routes-and-answers-it-keeps-briefly).

### Q-BAS-31 · Cached artifacts are revalidated after a period

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Never; served until eviction | Immutable bytes, no further question | Least load | A withdrawal the feed does not carry is served for as long as the entry lives |
| (b) A conditional request after `artifact_revalidate_after` (chosen) | No bytes, a PostgreSQL answer, once per period and artifact | Bounds every withdrawal to the period | One request per hot artifact and period |
| (c) Every request | Always ask | Exact | Defeats the cache for permissions |

**Choice:** (b).

**Rationale:** the bytes, the load the Bastion exists to take, never travel again; only the main installation's decision does.
An automatic malicious block, a version removed upstream and a member removed from a virtual repository have no feed entry
(H-385). The development agent would revisit this if every withdrawal gains a feed entry, which makes (a) safe.

**Applied in the spec:** R-BAS-022; H-385.

### Q-BAS-32 · Answers validate themselves against the feed, and are signed in Valkey

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Delete answers when feed entries arrive | Eviction only | Simple | An answer fetched before a revocation and stored after the eviction lives for the period |
| (b) Validity checked at use against the receipt times of feed entries, and records in Valkey signed (chosen) | Rules 3 and 4 of R-BAS-020; HMAC records | Closes the race whatever the order of writes; a Valkey writer cannot grant | A signing key to distribute |
| (c) No answers in Valkey | INV-3 as for the main installation | No new trust | Against the owner's Q-BAS-11 |

**Choice:** (b).

**Rationale:** INV-3 and Q-ARCH-16 keep positive results out of Valkey because whoever writes Valkey could grant; the owner
wants answers shared (Q-BAS-11). A signature removes the reason, and use-time validation is the self-validating key R-ARCH-018
prefers. The development agent would revisit this if the key's distribution becomes the weaker link (H-387).

**Applied in the spec:** R-BAS-020, R-BAS-032, R-BAS-034; cross-page: INV-3 in README.md;
[ADR-143](../decisions.md#adr-143--the-bastion-decides-through-the-main-installations-own-routes-and-answers-it-keeps-briefly).

### Q-BAS-33 · A replica starts with the feed history of an answer's lifetime

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read the feed back by `period + grace_period + 15 s` before using shared answers (chosen) | A synthetic cursor into the audit history | A new replica uses shared answers at once | One larger query per start |
| (b) Use no shared answer obtained before the replica's start | No history | Nothing to read | Every scale-up re-asks everything at peak |
| (c) Feed entries in Valkey | Shared history | No query | Signed entries, and still nothing during an outage |

**Choice:** (a).

**Rationale:** the main installation's audit holds the history anyway, and one indexed query is cheap. A replica that starts
while the main installation is unreachable has no history and uses no shared answer, which errs on the safe side. The
development agent would revisit this if starts during outages turn out to matter for availability.

**Applied in the spec:** R-BAS-015, R-BAS-026.

### Q-BAS-34 · Without Valkey, budgets divide by the chart's replica count

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `replicaCount`, or `autoscaling.maxReplicas`, rendered as `bastion.replicas` (chosen) | A fixed divisor from the chart | The total never exceeds the configured value | Under autoscaling, the budget is under-used below the maximum |
| (b) Discovery through a headless Service's DNS | A live count | Accurate | A DNS dependency and a counting race |
| (c) No division | Every replica the full value | Simple | n times the configured load on the main installation |

**Choice:** (a).

**Rationale:** the Bastion has no replica registry; (a) errs on the main installation's side, which is the budget's purpose. The
development agent would revisit this if the autoscaling range is wide and the under-use measurable.

**Applied in the spec:** R-BAS-025, R-BAS-034, R-BAS-035.

### Q-BAS-35 · The concurrency budget is per replica, with a share for anonymous misses

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A per-replica share in both modes, anonymous capped at a share (chosen) | Slots counted in memory | No Valkey round trip on a miss; exact per replica | Fragments at many replicas |
| (b) A cluster-wide semaphore in Valkey | Shared slots | Exact in total | A round trip and a failure mode per miss; slots lost on crashes |
| (c) Rates only | No concurrency bound | Nothing new | A slow main installation accumulates unbounded in-flight requests |

**Choice:** (a).

**Rationale:** random distribution evens the load, so per-replica shares sum to the total; the anonymous cap keeps
authenticated callers served during an anonymous flood. The development agent would revisit this above about ten replicas.

**Applied in the spec:** R-BAS-025.

### Q-BAS-36 · A failed-authentication timeout refuses unverified credentials only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The whole address refused | Every request from it | Strongest | Every caller behind a NAT is cut off by one guesser |
| (b) Unverified credentials refused; known credentials and anonymous requests continue (chosen) | As R-AUTH-038 at the main installation | Partners behind one address keep working | A guesser keeps anonymous access |

**Choice:** (b).

**Rationale:** guessing a 256-bit token is infeasible; the timeout protects the main installation from floods of fake tokens,
which (b) stops, while the flood block (R-BAS-037) still catches an address that keeps sending. The development agent would
revisit this if attacks pair guessing with valid tokens from the same address.

**Applied in the spec:** R-BAS-036.

### Q-BAS-37 · One refusal threshold blocks an address, with doubling durations

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One threshold over every refusal, durations doubling to a maximum, IPv6 per `/64` (chosen) | A flood shows as refusals of whatever limit it meets | One value to tune; repeat floods deterred; no permanent block | Coarse |
| (b) A threshold per refusal kind | Separate counts | Finer | More values, the same effect |
| (c) A fixed duration | Always the same | Predictable | A flood resumes every 15 minutes |

**Choice:** (a).

**Rationale:** one IPv6 host holds a `/64`, so per-address counting without it is no limit. The development agent would revisit
this if legitimate CI behind one address trips the threshold; `exempt_cidrs` is the first answer.

**Applied in the spec:** R-BAS-019, R-BAS-037.

### Q-BAS-38 · Writes are refused by the main installation from a bodiless probe

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A bodiless probe, answered in the format's shape (chosen) | The main installation knows the route; the Bastion forwards no body of a write | Protocol shape without format knowledge; tells read routes with bodies (npm audit) apart | One request per method, path and period |
| (b) A generic `405` from the Bastion | No request | Cheapest | Not the protocol's shape (against Q-BAS-2) |
| (c) Forward writes with their bodies | The main installation refuses | Simplest code | Upload bodies on the main installation's network for nothing |

**Choice:** (a).

**Rationale:** the owner asked for the protocol's shape (Q-BAS-2) and no format knowledge (Q-BAS-17); only the main installation
holds both. The development agent would revisit this if a format's write route could not be told from a read route before its
body; npm and NuGet have none.

**Applied in the spec:** R-BAS-014.

### Q-BAS-39 · The Bastion's own refusals are problem details

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Problem details with the family's challenge (chosen) | Limits, reachability and kept refusals answered by the Bastion | No format knowledge; clients act on status and challenge | The body is not the format's |
| (b) Templates learned from the main installation | Stored refusals per repository and format | The format's body | Stored templates per format and status |

**Choice:** (a).

**Rationale:** these answers arise exactly when the main installation is not asked; their statuses and challenges are those the
main installation would give (R-AUTH-002), so clients behave the same. The development agent would revisit this if a client
mishandles a problem-details body for a status it otherwise handles.

**Applied in the spec:** R-BAS-029.

### Q-BAS-40 · No cache state in the Bastion's answers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No `Cache-Status`; the main installation's removed (chosen) | Cache state in metrics and logs only | An outsider does not learn what others fetched | Operators debug with logs |
| (b) `Cache-Status` with hit or miss | RFC 9211 | Transparent | Tells every caller what other callers fetched recently |

**Choice:** (a).

**Rationale:** the same choice as Q-EP-60 for an outside audience; timing still tells (H-390). The development agent would
revisit this if operators need per-request cache state outside the request log.

**Applied in the spec:** R-BAS-021.

### Q-BAS-41 · Search results go to the main installation per caller

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the owner's decision on metadata names search results "likewise" next to filtered documents (Q-BAS-18).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per caller on every request, never kept (chosen) | `pass` | The main installation's own route kind for search is `Volatile`, never cached; no cache pollution by random queries | Search load reaches the main installation |
| (b) Shared with full readers like documents | Kept per query string | Less load | Unbounded keys; one cache entry per query |

**Choice:** (a).

**Rationale:** "likewise" is read as "per caller", which agrees with how the main installation treats search itself. The
development agent would revisit this if search through Bastions shows in the main installation's load, or if the owner meant (b).

**Applied in the spec:** R-BAS-023.

### Q-BAS-42 · Negative entries are served only to full readers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only to callers with a `full` answer (chosen) | Others go to the main installation | No timing oracle for hidden names | Misses of narrowed readers reach the main installation |
| (b) To every caller who may read something | Wider absorption | Fewer misses | A fast `404` for a missing name and a slow one for a hidden name tell them apart |

**Choice:** (a).

**Rationale:** anonymous floods are absorbed where `anonymous` reads the whole repository or nothing, the common cases; the
budget bounds the rest. The development agent would revisit this if misses of narrowed readers become a load problem.

**Applied in the spec:** R-BAS-024.

### Q-BAS-43 · The bucket backend evicts by listing access markers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Access markers and two listings per sweep; signed entries; five basic operations (chosen) | Last access to an hour's resolution | Any S3 service, SeaweedFS and STACKIT included; no Valkey needed | Two listings per sweep |
| (b) A lifecycle rule by age | The bucket expires objects | No code | Evicts hot artifacts |
| (c) An index in Valkey | Last access in Valkey | Fast | Valkey is optional (Q-BAS-11) |
| (d) A self-copy per access | `LastModified` refreshed | One object | Copies whole objects |

**Choice:** (a).

**Rationale:** eviction by last access is the owner's (Q-BAS-10); (a) is the only option that needs neither Valkey nor
conditional writes. The development agent would revisit this above about a million entries, where listing time grows; (c) would
then be used when Valkey is present.

**Applied in the spec:** R-BAS-030, R-BAS-031, R-BAS-032.

### Q-BAS-44 · Unreachable means a failed handshake or a stale feed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Handshake and feed freshness decide; readiness does not depend on the main installation (chosen) | A state per replica | The feed must be fresh for the revocation bound anyway; the grace period keeps the replicas in the Service | A main installation that answers the feed but fails fetches counts as reachable |
| (b) Any forward error | Error counting | Fast | Flaps on one slow request |
| (c) Readiness gated on the main installation | Unready when unreachable | Clear | Ends the grace period the owner wants |

**Choice:** (a).

**Rationale:** the grace period only works if the replicas stay in the Service. The development agent would revisit this if a
real failure leaves the feed working while fetches fail; a forward error counter would join the condition.

**Applied in the spec:** R-BAS-027, R-BAS-043.

### Q-BAS-45 · Downloads are buffered in memory and spooled at shutdown

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A memory buffer, spooled to the cache backend at shutdown (chosen) | Delivered later by any replica | A drain during an outage loses nothing with a bucket or a persistent volume | A crash loses the buffer |
| (b) Memory only | As the main installation's asynchronous events | Simple | A drain during an outage loses the buffer |
| (c) Always to the backend first | Durable at once | Survives crashes | A write per download |

**Choice:** (a).

**Rationale:** the owner wants buffered events sent later (Q-BAS-13); drains during outages are the case to cover. The
development agent would revisit this if crashes lose material numbers of events; (c) in batches would follow.

**Applied in the spec:** R-BAS-039; H-391.

### Q-BAS-46 · Default periods and limits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The defaults of R-BAS-046 (chosen) | Answers 5 min, metadata 1 min, artifacts 10 min, negative 1 min, feed 2 s, grace 15 min, warning 30 days, volume backend, the limits shown | See the rationale | Not measured yet |
| (b) Shorter periods | Fresher | Faster widening and publishing | More checks |
| (c) Longer periods | Less load | Fewer checks | Staler content, longer withdrawals |

**Choice:** (a).

**Rationale:** answers "some minutes" (the owner); metadata one minute, because a pipeline that publishes and then installs
through a Bastion should not wait longer, and a revalidation is a `304`; artifacts ten minutes, which bounds H-385; the feed at
2 s, the main installation's own revocation bound; grace and warning as the owner proposed; the volume backend because it
installs anywhere; per-address limits above what one client's parallel installs need, anonymous five times stricter, the
failed-sign-in limit far below the main installation's 300 a minute. The development agent would revisit the values after the
first load test of a Bastion (R-ARCH-027's scripts against the e2e cluster).

**Applied in the spec:** R-BAS-020, R-BAS-023 to R-BAS-026, R-BAS-030, R-BAS-035 to R-BAS-038, R-BAS-046.

### Q-BAS-47 · Which catalog entries the Bastion pulls forward

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Raise RMT-040 to RMT-046, DIST-024 and DIST-025 to MVP | All the plan names | Literal | Four MVP entries no requirement covers: the MVP share would count open work |
| (b) Raise the covered entries (RMT-040, RMT-041, RMT-044, DIST-024, DIST-025); RMT-042, RMT-043, RMT-045 and RMT-046 stay P2 (chosen) | What the page builds | The MVP numbers stay true | The plan's list is not raised whole |

**Choice:** (b).

**Rationale:** the Bastion has no item properties to synchronize (RMT-042), no UI to show a source deletion in (RMT-043; the
feed's `content` entries remove the cached copy instead), no query language to extend (RMT-045), and serves files below the
size where chunked parallel transfer pays (RMT-046, 1 GB and more). The development agent would revisit this when a Bastion user
needs one of them.

**Applied in the spec:** the `Priority:` lines of RMT-040, RMT-041, RMT-044 ([remote-repositories.md](../features/remote-repositories.md))
and DIST-024, DIST-025 ([distribution-and-lifecycle.md](../features/distribution-and-lifecycle.md)); the roadmap's M13 row.

## What this does not cover

### H-382 · A volumetric attack on bandwidth is not absorbed by the Bastion

Stage: the network in front of the Bastion's HAProxy Ingress (R-BAS-038). Subject: the availability of the Bastion for every
caller.

Dormant until phase 5b builds R-BAS-035 to R-BAS-038; then live on every Bastion reachable from the internet. Every stage of the
Bastion limits requests, not bytes on the wire: a flood that fills the link or the ingress's connection capacity makes the
Bastion unreachable, whatever its limits say. The main installation is not affected, because the concurrency budget bounds what
reaches it (R-BAS-025). What makes it real: an attacker with more bandwidth than the Bastion's uplink; the owner assigns this to
the network provider (Q-BAS-16).

### H-383 · During the grace period a token revoked at the main installation reads on at the Bastion

Stage: R-BAS-027, with rule 1 of R-BAS-020 extended by `bastion.answers.grace_period`. Subject: cached content of the main
installation, read with a credential the main installation has revoked or narrowed.

Dormant until phase 5b; then live on every Bastion with a grace period above 0 (15 minutes by default). While the main
installation is unreachable the feed cannot be followed, so a revocation, a narrowing, an unpublish or a block made at the main
installation during the outage is not seen: a caller with answers keeps reading cached artifacts and documents for up to
`answers.period + grace_period` (20 minutes by default) after the outage began. An attacker who can cut the Bastion off from the
main installation keeps a revoked token working for that long. What makes it real: a partition between Bastion and main
installation together with a revocation; `grace_period: 0` closes it at the price of availability during outages.

### H-384 · A version newly published appears at the Bastion only after the metadata period

Stage: R-BAS-023 and R-BAS-024. Subject: the freshness of what Bastion consumers install, security fixes included.

Dormant until phase 5b; then live on every Bastion. A caller who reads the whole repository sees a new version, a moved tag and a
new name only after `metadata_revalidate_after` (1 minute) or `negative_period` (1 minute), because publishes are not in the feed
(Q-BAS-30). A consumer that installs right after a security fix was published can get the vulnerable version once more. What
makes it real: a fix published and consumed through a Bastion within the period; shorter periods narrow it.

### H-385 · A withdrawal the feed does not carry reaches the Bastion within the artifact revalidation period

Stage: the coverage of R-BAS-015, and R-BAS-022. Subject: content the main installation no longer serves: a version removed
upstream of a remote member, a member removed from a virtual repository, a deleted repository, and a download block set
automatically when an advisory marks a stored version malicious (no transactional event records it;
`vulnerability.download_blocked` is written at download time only).

Dormant until phase 5b (the automatic blocks until phase 6); then live on every Bastion. A caller who reads the whole repository
gets such an artifact from the cache for up to `artifact_revalidate_after` (10 minutes) after the change, and its documents for up
to the metadata period. Withdrawals the feed carries (unpublish, delete, a block set in triage) are dropped within seconds. What
makes it real: a malicious version found while consumers install through a Bastion; a transactional event for an automatic block
on [vulnerability-scanning.md](vulnerability-scanning.md) would move that case into the feed.

### H-386 · Whoever holds the Bastion token can test credentials, name client addresses and add download events

Stage: the Bastion interface (R-BAS-005, R-BAS-011, R-BAS-016, R-BAS-017). Subject: the main installation's failed-credential
throttle, its audit trail, and the token and principal IDs of the feed.

Dormant until phase 5b; then live wherever a Bastion token exists. The token is a bearer secret in a Secret of the Bastion's
namespace, accepted from any address. Its holder can ask the check about any credential with a client address of its choice, so
the main installation's throttle and its `token.rejected` events count against that address; add `artifact.downloaded` events
for any principal and path (always marked with the registration's name, so attributable to the Bastion, not to the principal);
and read the token and principal IDs of the feed. It cannot read a package, write content or issue a token. What makes it real: a
compromised Bastion pod or a leaked Secret; revoking the token ends it, and the last contact on the screen of R-BAS-007 shows
unknown addresses.

### H-387 · The signing key and write access to the Bastion's Valkey or bucket together forge answers and entries

Stage: R-BAS-032 and R-BAS-034. Subject: what the Bastion serves to whom, and the integrity of its cached bytes.

Dormant until phase 5b; then live on every Bastion with the bucket backend or Valkey. The signing key is a Secret every replica
mounts. Whoever holds it and can write the Bastion's Valkey can store a signed `full` answer for a credential of their choice and
read every cached artifact with it; whoever holds it and can write the bucket can point an entry at bytes of their choice, which
the Bastion then serves under a URL clients trust. Write access without the key only deletes (performance). What makes it real: a
compromise of the Bastion's namespace, where the key, the Valkey credentials and the bucket credentials live together.

### H-388 · Anonymous misses from many addresses take the anonymous share of the budget

Stage: R-BAS-025 and R-BAS-035. Subject: the availability of the Bastion for anonymous callers.

Dormant until phase 5b; then live on a Bastion whose main installation allows anonymous access. Per-address limits and blocks do
not bound a flood spread over many addresses. The anonymous share of `max_concurrent` bounds what reaches the main installation,
and authenticated callers keep their share, but legitimate anonymous callers wait or get `503 bastion_busy` while it lasts. What
makes it real: a botnet against a public repository; the edge limit (R-BAS-038) and the network provider are the next lines.

### H-389 · Without Valkey a block and a counter hold on one replica only

Stage: R-BAS-034 to R-BAS-037. Subject: how fast a flood or a guesser is stopped on every replica.

Dormant until phase 5b; then live on every Bastion without Valkey, and while its Valkey is unavailable. Counters run per replica
at the configured value divided by `bastion.replicas`, so the total stays close to the configured one, but a block or a
failed-authentication timeout set by one replica's count does not hold on the others until each counts enough itself; the
Service's random distribution spreads the flood, so each replica blocks on its own schedule. What makes it real: a flood against
a Bastion without Valkey; Valkey closes it.

### H-390 · The time of an answer tells a caller whether others fetched a package recently

Stage: R-BAS-022 and R-BAS-023. Subject: which packages the other consumers of one Bastion use.

Dormant until phase 5b; then live on every Bastion. A hit answers in milliseconds, a miss waits for the main installation; a
caller who may read a package learns from the time whether it was cached, that is whether another caller fetched it within the
cache's lifetime. `Cache-Status` is not sent (Q-BAS-40), but the timing remains. What makes it real: parties sharing one Bastion
who must not learn each other's dependencies; a Bastion per audience closes it.

### H-391 · Download events lost to a full buffer, a crash or an ephemeral spool are missing from the audit

Stage: R-BAS-039. Subject: the completeness of the main installation's download audit for downloads served by a Bastion.

Dormant until phase 5b; then live with `audit.record_downloads: true`. A full buffer drops events (counted and reported in the
audit); a crashed replica loses its buffer, counted nowhere but in its last scraped metrics; a drain during an outage of the main
installation spools to the cache backend, which with an ephemeral volume is deleted with the pod. It is the Bastion's form of
[H-122](../security/audit.md#h-122--a-crashed-replica-loses-the-buffered-asynchronous-events-and-its-open-summaries). What makes it
real: long outages of the main installation, crashes, and the volume backend without a persistent volume.
