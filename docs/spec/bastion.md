# trove Bastion

Status: implemented in beta phase 5b (2026-11-03) for R-BAS-001 to R-BAS-049 — the role as built is described on
[developer/bastion.md](../developer/bastion.md), the integration suite is `test/integration/bastion` (R-BAS-048) and the end-to-end
suite `make e2e SET=bastion` and `SET=bastion-long` (R-BAS-049, [developer/e2e.md](../developer/e2e.md)); the open gaps are the H- items of
[What this does not cover](#what-this-does-not-cover). The owner's decisions of 2026-10-31 and 2026-11-02 are binding and recorded as
[Q-BAS-1](#q-bas-1--the-bastion-asks-the-main-installation-and-keeps-no-copy-of-users-tokens-or-policies) to
[Q-BAS-21](#q-bas-21--internet-exposure-limits-blocking-failed-sign-in-timeouts-and-a-configurable-cache); the record is
[ADR-142](../decisions.md#adr-142--the-trove-bastion-an-internet-facing-read-only-cache-instance-in-the-beta); the development agent's
decisions are Q-BAS-22 to Q-BAS-131, for the owner's walkthrough.

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
| HAProxy Ingress v0.16.3, the controller of the local profile, reads `limit-rps`, `limit-connections`, `limit-whitelist` (backend scope; HTTP answer `429`), `balance-algorithm`, `assign-backend-server-id`, `config-backend` and `secure-backends`, `secure-sni`, `secure-verify-ca-secret` as annotations with the prefix `haproxy-ingress.github.io/`; it has no key for `hash-type`, so consistent hashing is a `config-backend` snippet, which the controller accepts unless `--disable-config-keywords` forbids it. | `docs/content/en/docs/configuration/keys.md` and `command-line.md`, `rootfs/etc/templates/haproxy/haproxy.tmpl`, `pkg/converters/ingress/annotations/backend.go` and `pkg/converters/ingress/types/annotations.go` of `jcmoraisjr/haproxy-ingress` at the tag `v0.16.3` (`DEV_HAPROXY_INGRESS_IMAGE` in `tools/versions.env`), fetched with `gh api` on 2026-11-03; [Q-BAS-56](#q-bas-56--haproxy-ingress-annotations-as-verified-against-the-controller-of-the-local-profile) |

Not verified on this page, and this is the gap: how the controller behaves when it runs (a flood refused with `429`, a path
pinned to one pod, what `limit-rps` counts for a keep-alive connection) is read from its documentation and templates, not
observed; the e2e suite does that (R-BAS-049). Whether `dotnet restore` sends every request of one restore with the same credential through one source is assumed from the
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
  management API, no UI, no login, no session, no remote repository. "Enables" is the operative word: the platform packages the
  role shares (the Valkey client in `internal/cluster`, the listeners in `internal/httpserver`, the S3 client in
  `internal/blob/s3`) import packages of the main installation (the PostgreSQL store, the audit types, the scope and CI claim
  types, the blob store interface, the format areas), which are in the role's binary and never started. The import rule of the
  role is transitive and lists those packages with their reason ([Q-BAS-129](#q-bas-129--the-import-rule-follows-the-dependency-closure-and-lists-what-it-cannot-narrow),
  [H-473](#h-473--the-roles-binary-contains-packages-of-the-main-installation-it-never-starts)); a test of the role proves that it
  opens no connection to a PostgreSQL address.
- The role is deployed only by the chart `trove-bastion` in `deploy/helm/trove-bastion` (R-BAS-045), never by the chart
  `trove`, and the chart `trove-bastion` renders no other role. Several Bastions in front of one main installation are several
  releases of that chart, each with a registration of its own (R-BAS-004).
- The configuration schema of the role is its own: `server.listen`, `server.ops_listen`, `server.base_url`,
  `server.trusted_proxies`, `server.forwarded_header`, `server.max_in_flight`, `server.tls`, the section `bastion` of this page,
  `valkey`, `log`, `tracing` and `runtime`. A key of another section (`database`, `storage`, `auth`, `authz`, `ci`, `backup`,
  `audit`, `server.endpoints`, `server.additional_base_urls`, …) stops startup with an error that names the key and the role.
  The role has `server.ops_tls` as well (R-BAS-044), is chosen by `--role bastion` and by nothing else, and takes `--log-level` as
  its one flag ([Q-BAS-50](#q-bas-50--the-role-has-a-configuration-tree-of-its-own-chosen-by---role-alone)).
- Horizontal replicas: every Bastion replica is identical and interchangeable (INV-2); a request may reach any replica, and
  nothing a request needs is held only by the replica an earlier request reached (INV-18): answers, document copies and a
  local cache volume are caches whose loss costs performance only. The Bastion has no PostgreSQL; what it must not lose lives
  at the main installation, and what it may lose lives in its cache backend and, optionally, Valkey.

```yaml
server:
  listen: ":8443"                       # default (proposed) for the role bastion
  ops_listen: ":8081"                   # default (proposed)
  base_url: ""                          # required; example: https://packages.example.com
  trusted_proxies: []                   # default (proposed); the chart requires it while its Ingress is on (Q-BAS-130); example: ["10.244.0.0/16"] (the Bastion's ingress pods)
  forwarded_header: x-forwarded-for     # default (proposed): x-forwarded-for | forwarded (R-ARCH-006)
  max_in_flight: 1024                   # default (proposed), per replica
```

Acceptance criteria:

- `trove serve --role bastion` with a file that holds `database.host` stops with an error naming `database.host` and the role
  `bastion`; with the keys above, `bastion.main.url` and the token file it starts, and `ss -ltn` in its pod shows `:8443` and
  `:8081` only.
- A Bastion pod opens no connection to any PostgreSQL port and makes no request to the main installation's bucket during the
  whole e2e suite (NetworkPolicy log and the object store's access log). The role started with a trap listening where a
  PostgreSQL client of the process would look sees no connection and exports no database metric
  (`TestR_BAS_001_TheRoleOpensNoConnectionToPostgreSQL`).
- The import rule names every package of the main installation in the role's dependency closure, with the platform package that
  pulls it in; a new one fails `go test ./internal/archtest/...` with the chain.
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
  from the Bastion itself (R-BAS-029): `/`, `/ui/…`, `/auth/…`, `/api/v1/…`, `/-/…`, `/v2/…`, `/.well-known/…`. The main
  installation's router decodes every segment before it compares it, so `/api/%761/users` is `/api/v1/users` there: a path
  whose family, format or key segment contains a percent-encoding, and a path with a `.` or `..` segment, answer `404` from the
  Bastion as well ([Q-BAS-121](#q-bas-121--a-path-is-classified-as-the-main-installations-router-will-read-it-and-a-token-refusal-on-a-forward-is-no-statement-about-the-bastions-token)).
- There is no login, no session, no cookie and no browser flow. A `Cookie` header is ignored and never forwarded. A session
  token never reaches the Bastion from trove, because trove never sets a cookie for the Bastion's host.
- The ops listener serves `/healthz`, `/readyz` and `/metrics` as on every trove role ([operations-and-observability.md](operations-and-observability.md)
  R-OPS-001, R-OPS-006).

Acceptance criteria:

- A table test sends every row of INV-6's path table to the Bastion: only `/api/<format>/…` and `/files/…` reach the read
  flow; the others answer `404` with `bastion_not_served` and the counting double of the main installation records no request.
- A request with `Cookie: __Host-trv_session=…` and no other credential is treated as anonymous, and the request the Bastion
  forwards carries no `Cookie`.
- `/api/%761/users`, `/api/%76%31/bastion/handshake` and every other encoded spelling of a family, format or key segment
  answer `404 bastion_not_served`, reach the main installation never, and leave the replica reachable however often they are
  repeated (`TestR_BAS_003_R_BAS_018_APathIsClassifiedAsTheMainInstallationsRouterWillRouteIt`,
  `TestR_BAS_003_R_BAS_006_AnEncodedSpellingOfTheInterfaceRoutesCannotTurnTheReplicaUnreachable`).

## Trust between the Bastion and the main installation

### R-BAS-004 · Bastion registrations and the Bastion token on the main installation
Priority: **MVP** · Covers: DIST-025

- **Registration.** A Bastion is registered on the main installation as a principal of the kind `bastion`, reference
  `bastion:<name>`, name `^[a-z][a-z0-9-]{0,19}$` ([authorization.md](authorization.md) R-AUTHZ-002). It holds no grants, no
  roles, no group memberships and no package ownership: a policy entry, a role binding, an owner record or a membership that
  names it answers `422 principal_not_allowed` ([Q-BAS-60](#q-bas-60--a-bastion-registration-is-refused-by-every-write-path-with-one-top-level-422-principal_not_allowed)).
  It is never part of `authenticated` (R-AUTHZ-030): its token is refused by every route that could admit it, and the decision point
  gives the kind `bastion` an empty principal set (no `authenticated`, no grant, no role), should any route ever decide for it. At most
  `bastions.max_registrations` registrations exist. A registration can be disabled, which refuses its tokens like a disabled
  service account's ([identity-and-authentication.md](identity-and-authentication.md) R-AUTH-020), and deleted, which revokes
  them.
- **Bastion token.** The Bastion's credential is a token of the type `bst`, prefix `trv_bst_`, issued by an administrator of
  the main installation (owner, [Q-BAS-3](#q-bas-3--the-bastions-credential-is-a-token-issued-by-an-administrator-of-the-main-installation),
  [Q-BAS-23](#q-bas-23--the-bastion-token-is-a-token-type-of-its-own-on-a-bastion-principal)). It follows the format, storage,
  validation, caching and revocation bound of R-AUTH-022 and R-AUTH-023 and the revocation of R-AUTH-029; its owner principal
  is the registration. It has no scope: what it may do is fixed by its type (R-BAS-005). It always expires: `ttlSeconds` is at
  most `auth.tokens.bst.max_ttl`, defaulting to `auth.tokens.bst.default_ttl`; 0, a negative value, under 60 seconds and above the
  maximum are `400 invalid_ttl`, and a missing description becomes `Bastion <name>`
  ([Q-BAS-63](#q-bas-63--a-bastion-tokens-lifetime-is-ttlseconds-alone-every-violation-is-400-invalid_ttl-the-description-is-optional)).
  At most `auth.tokens.max_active_per_principal.bst` unrevoked tokens exist per registration, so an overlap (R-BAS-006) fits and a
  forgotten token does not accumulate. The validation is the token service's, through its cache and revocation bound
  ([Q-BAS-62](#q-bas-62--the-bastion-token-authenticator-is-the-token-services-with-its-cache-gate-and-change-area)); disabling a
  registration revokes nothing, deleting it revokes every token first
  ([Q-BAS-61](#q-bas-61--the-registration-events-carry-no-journal-entry-a-bastion-tokens-events-name-the-registration-in-their-data)).
- **API** (management API conventions of [api-and-ui.md](api-and-ui.md); capability in brackets,
  [Q-BAS-24](#q-bas-24--bastion-registrations-are-managed-with-the-endpoint-capabilities)):

| Operation | Capability | Answer |
|---|---|---|
| `POST /api/v1/bastions` `{"name", "description"}` | `endpoints:write` | `201` with the registration |
| `GET /api/v1/bastions`, `GET /api/v1/bastions/{name}` | `endpoints:read` | name, description, `enabled`, its tokens (ID, hint, `expiresAt`, `createdAt`, `lastUsedAt`), and the last contact of R-BAS-010: time, client address, Bastion release, interface version, reported base URL, replica |
| `PATCH /api/v1/bastions/{name}` `{"description", "enabled"}` | `endpoints:write` | `200` |
| `DELETE /api/v1/bastions/{name}` | `endpoints:write` | `204`; revokes its tokens (`token.revoked`, journaled) |
| `POST /api/v1/bastions/{name}/tokens` `{"description", "ttlSeconds"}` | `endpoints:write`; a session needs a recent authentication ([api-and-ui.md](api-and-ui.md) R-API-007) | `201` with the token secret, once (R-AUTH-022) |
| `DELETE /api/v1/bastions/{name}/tokens/{id}` | `endpoints:write` or `tokens:revoke` | `204`; `token.revoked`, journaled (R-HA-054) |

- `trove admin bastion create <name>`, `trove admin bastion token create <name> --ttl <duration>` and
  `trove admin bastion token revoke <name> <id>` do the same from a `web` pod (R-AUTH-015's pattern). Every operation writes its
  audit event: `bastion.created`, `bastion.updated`, `bastion.deleted`, and `token.created` / `token.revoked` with
  `resource_type: bastion`. The three registration events carry no journal entry; `token.revoked` keeps its flag and its entry. At most
  `bastions.max_registrations` is held exactly, also when creates race
  ([Q-BAS-64](#q-bas-64--creating-registrations-is-serialized-by-an-advisory-lock-a-registrations-name-never-changes)).

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
  fourth unrevoked token answers `409 token_limit_reached` (the registry's code for the limit, [Q-BAS-53](#q-bas-53--unbuilt-operations-answer-501-bastion_not_implemented-three-codes-follow-the-existing-registry)).
- A policy entry for `bastion:partners` and a role binding of `admin` to it answer `422 principal_not_allowed`.
- A caller with `endpoints:read` only gets `403` on the token creation; `tokens:revoke` alone may revoke a Bastion token.
- Deleting `partners` revokes its tokens (a `token.revoked` with `resource_type: bastion` and a journal entry each), removes the
  registration and its contacts, and the revoked token is refused on every replica within `auth.revocation_bound`; disabling it
  refuses the tokens in the same time and enabling it restores them.

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
   new token from its next request to the main installation; requests in flight finish with the old one. No restart. A change
   of the file is noticed by a check of its identity (the file behind the link, its size and its time) every second, so a
   rotation of the mounted Secret does not wait for the interval
   ([Q-BAS-77](#q-bas-77--the-token-file-is-watched-by-a-one-second-identity-check-besides-the-interval-read)).
4. The administrator revokes the old token once the screen of R-BAS-007 shows the new token's `lastUsedAt` from every replica's
   contact. A replica that still uses a revoked token gets `401 bastion_token_invalid` to a call of its own (handshake, feed,
   check, download batch) and treats the main installation as unreachable (R-BAS-027; the same answer to a forward is not that statement,
   [Q-BAS-121](#q-bas-121--a-path-is-classified-as-the-main-installations-router-will-read-it-and-a-token-refusal-on-a-forward-is-no-statement-about-the-bastions-token)), `trove_bastion_main_token_refused` is 1 and `TroveBastionTokenRefused` fires (R-BAS-041).

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
  The screen uses only the operations of R-BAS-004 (INV-17). It is one page with a card per registration, which shows the
  registration's facts and actions, the last contact of every replica and its tokens ([Q-BAS-95](#q-bas-95--the-bastions-screen-is-one-page-with-a-card-per-registration)).
- **Client setup.** `GET /api/v1/repositories/{repoKey}/client-setup` ([api-and-ui.md](api-and-ui.md) R-API-030) accepts
  `bastion=<name>`: the snippets are rendered on the base URL the Bastion reported at its last handshake, provided it is an
  entry of `server.additional_base_urls` (else `409 bastion_base_url_unknown`, also while it has reported none; a name that is no
  registration answers `404`; [Q-BAS-97](#q-bas-97--the-reported-address-is-matched-against-the-listed-addresses-and-the-setup-needs-no-base-url-of-its-own)),
  and carry no publish snippets, since the Bastion is read-only. The guide of each format (R-UI-049) and the setup page of
  R-UI-016 offer the Bastion as a target when a registration has reported a base URL
  ([Q-BAS-96](#q-bas-96--a-bastion-is-a-target-of-the-setup-page-and-of-the-guides-read-only-and-by-link-for-everyone)).
  For npm, the configure snippet sets `replace-registry-host=always`, so that a lockfile written against another host resolves
  through the Bastion.
- **Manual.** The admin manual gains `docs/manual/admin/bastion.md`: register a Bastion, issue its token, the chart values of a
  minimal and of an HA install, the settings on the main installation (`bastions.trusted_proxies`,
  `server.additional_base_urls`), verify with the handshake metric, renew the token, and what each alert of R-BAS-041 means.

Acceptance criteria:

- The browser test creates `partners`, issues a token, copies it once, and sees the last contact appear after the Bastion of
  the e2e cluster starts; a token expiring in 20 days shows the warning mark.
- `client-setup?client=npm&bastion=partners` on `npm-all` returns a registry URL
  `https://packages.example.com/api/npm/npm-all/`, `replace-registry-host=always`, and no `publish` tab
  (`TestR_BAS_007_ClientSetupRendersOnTheBaseURLTheBastionReported`, and through the stack
  `TestR_BAS_007_ClientSetupForABastionThroughTheStack`).

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
- **Forward or interface call.** A request that carries `Trove-Bastion-Authorization` is a forward, whatever its path; a request
  without it whose path the router decodes to a route below `/api/v1/bastion/` is an interface call, with the Bastion token in
  `Authorization`. A forward of a path outside `/api/<format>/` and `/files/` answers `404 not_found` before authentication
  (R-BAS-014, [Q-BAS-115](#q-bas-115--a-request-with-trove-bastion-authorization-is-a-forward-and-a-forward-reaches-only-the-package-families)).
- **Recognition.** The main installation treats a request as a Bastion request only when `Trove-Bastion-Authorization` (or, on
  the interface routes, `Authorization`) carries a valid `bst` token of an enabled registration. A request with
  `Trove-Bastion-*` headers but no valid Bastion token is served as an ordinary request: the headers are ignored and no
  marking is added. A request whose Bastion token is invalid answers `401 bastion_token_invalid`; one whose interface version
  differs answers `400 bastion_interface_mismatch` (R-BAS-009). Both are problem details of the management API, also on a
  package route, because they concern the Bastion, never the caller; the Bastion never passes them on (R-BAS-027).
- `Trove-Bastion-Authorization` is redacted like `Authorization` in every log, span and support bundle (R-OPS-013), and the
  main installation never echoes a `Trove-Bastion-*` request header.
- The operations are in the OpenAPI document under the tag `bastion`, marked `x-trove-bastion-interface: <n>` and declared
  `x-trove-permission: {bastion: true}`, which the shared authorization step admits on the recognition result alone
  ([Q-BAS-52](#q-bas-52--recognition-and-marking-are-one-chain-stage-before-authentication-the-dispatcher-tells-an-observer-about-the-route));
  they are not part of the management API's v1 promise but of the interface version (R-ARCH-025).
- Main side metrics: `trove_bastion_interface_requests_total{bastion, operation, outcome}` and
  `trove_bastion_forwarded_requests_total{bastion, class}` (`class` of R-BAS-013); `bastion` is the registration name, bounded
  by `bastions.max_registrations`.
- **Recognition in detail** ([Q-BAS-65](#q-bas-65--the-recognition-admits-an-interface-route-on-its-own-proof-throttles-tokens-it-cannot-place-and-counts-the-bastion-in-the-read-class)).
  The token is the Bearer credential of the first line of the header, and only a value that starts with `trv_bst_` is looked at; any
  other credential leaves the request ordinary. The stages behind the recognition receive the request without the Bastion token. A
  rejected Bastion token counts as a failure of the address that sent it (the address before the Bastion's forwarded headers count,
  R-BAS-017) in a budget of its own; while that address is in throttled mode, a token the replica does not hold in its token cache, and
  whose marker does not say it was valid recently, is refused `429 rate_limited` without a lookup, as every other credential is
  (R-AUTH-038); a token the replica holds costs no read of the shared counters. A request whose token names no registration is counted
  under the registration `unknown`. `token.rejected` of a Bastion token names the request's source, and the last use of an accepted one
  records the address the Bastion connected from, never a caller's it forwards for. A recognized interface request is the registration's,
  not the address's: the request log names it, also on the check, which runs a caller's credential through the authentication stage
  without touching the line ([Q-BAS-119](#q-bas-119--the-check-runs-the-credential-through-a-stage-that-records-nothing-in-the-request-log)), and the general limits charge it to the class `read` under the bucket `token:<token ID>`,
  whatever the method.

Acceptance criteria:

- A forwarded `GET` with `Trove-Bastion-Authorization: Bearer <a revoked bst token>` answers `401 bastion_token_invalid` as
  `application/problem+json`; with `Trove-Bastion-Authorization: Bearer $PAT` (a valid token of another type) the request is
  served as an ordinary one without marking headers.
- The request log of the main installation shows `Trove-Bastion-Authorization` as `[REDACTED]`.
- A forward of `GET /api/%76%31/tokens` or `GET /api/%761/bastion/handshake` (the router decodes both to routes of `/api/v1/`) with a
  valid Bastion token in `Trove-Bastion-Authorization` answers `404 not_found` and runs no route, never `401 bastion_token_invalid`
  (`TestR_BAS_014_AForwardOutsideThePackageFamiliesReachesNoRoute`); the log line of `POST /api/v1/bastion/check` names the registration
  (`TestR_BAS_008_TheCheckLeavesTheRequestLogLineOfTheRegistration`).

### R-BAS-009 · The interface version
Priority: **MVP** · Covers: trove-only

The Bastion interface has a version of its own, separate from the release number (owner,
[Q-BAS-20](#q-bas-20--the-bastion-interface-has-a-version-of-its-own-equal-on-both-sides)).

- The version is a positive integer, `iface.InterfaceVersion` in `internal/bastion/iface`, starting at 1. It is raised by every change of the
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
  `trove_bastion_interface_version{side="main"} 2` ([Q-BAS-106](#q-bas-106--the-main-installations-interface-version-is-the-series-with-side-main-of-trove_bastion_interface_version)), serves cached artifacts to callers with live answers for the grace period, and
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

- `responseBaseUrl` is the response base URL the main installation derived for this request (R-OPS-005, R-EP-003), with the
  Bastion's forwarded host counted only when the Bastion connected from an address of `bastions.trusted_proxies` (R-BAS-017). The Bastion
  requires it to equal its own `server.base_url`; otherwise it logs `bastion base URL not honoured by the main installation`,
  sets `trove_bastion_base_url_mismatch 1` and treats the main installation as unreachable, so that no client ever receives a
  document with the main installation's URLs from the Bastion.
- `anonymousAccess` is the effective anonymous switch of R-AUTH-037 (the YAML ceiling and the runtime setting). `recordDownloads`
  is `audit.record_downloads` (R-BAS-039). `feed.cursor` is the feed position at the time of the answer (R-BAS-015).
- The main installation records the last contact of the registration (time, client address, `release` from `User-Agent`,
  interface version, `responseBaseUrl`, replica) at most once a minute per replica, and writes `bastion.contact` (asynchronous)
  when one of the reported values changes.
  The contact is bounded: a registration keeps at most 256 replica rows, the newest, and a failure to record it does not fail the
  handshake ([Q-BAS-69](#q-bas-69--the-last-contact-is-remembered-per-replica-bounded-per-registration-and-its-event-is-asynchronous)).

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
- **How the check validates** ([Q-BAS-66](#q-bas-66--the-check-runs-the-credential-through-the-endpoints-authentication-stage)). The
  credential goes through the authentication stage of the default endpoint as a direct request from `clientAddress` would, so the
  endpoint's token types and bindings apply, a rejected Bastion token presented as the caller's credential is `invalid`, and the last use
  of the token names the caller's address. A subject that is not usable (`invalid`, `anonymous_refused`, `throttled`) carries an empty
  `repositories` object. A `none` always names a status, and `401` for an anonymous caller.

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

The rules the list leaves open are decided in [Q-BAS-73](#q-bas-73--the-read-shape-is-computed-inside-the-decision-point-and-agrees-with-it-by-test):
the capability `content:metadata-read` alone never makes a shape `full` (it reads no artifact); the gate of a nested virtual
repository is asked as a member's read; a virtual repository without a readable member is `none`; and a `none` shape carries the
status a refused read gets (`401` for an anonymous subject, `403` or `404` as R-AUTHZ-016 says), taken from the decision point
itself. The tests are `TestR_BAS_012_ShapeAgreesWithTheDecisionPoint` and `TestR_BAS_012_Shapes` in `internal/authz`.

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
| `Trove-Bastion-Subject` | `token=<token ID>; principal=<principal ID>; expires=<unix seconds>` (`expires` left out for a token that does not expire, [Q-BAS-49](#q-bas-49--the-interface-is-one-package-of-wire-types-four-details-of-the-contract-are-fixed-in-it)), or `anonymous`, or `invalid` on a `401` for a rejected credential |
| `Trove-Bastion-Interface` | the main installation's interface version |

- `absent` is set only when the core's own lookups (the remote engine, through which a virtual repository's remote members
  answer, and the local lookup of a package or an item) know the item is missing for every subject; a refusal by the decision
  point, a hidden item and a serve-time refusal are `pass`, so a negative entry never stands for a hidden item (R-BAS-024). How
  the lookups tell the marking without format code, and why `absent` needs a `full` shape as well, is
  [Q-BAS-70](#q-bas-70--absent-is-derived-from-the-notes-of-the-cores-lookups-and-only-for-a-full-reader).
- `shared` follows from the shape and the route kind alone: a document of a `full` subject is the unfiltered rendition for the
  Bastion's host, and its ETag is the same for every `full` subject (R-NPM-013's inputs). A format whose document varies by
  anything but the repository's content and the request headers it names in `Vary` must render such a route as `Volatile`; the
  conformance kit has a check (check 12, [Q-BAS-72](#q-bas-72--the-conformance-kit-compares-documents-between-callers-as-check-12)) that
  sends one document request as a user, a service account and the anonymous caller - each with a request context of its own and request
  headers of its own (credential, client, session, forwarded address), except the headers the first answer names in `Vary`, which
  all send alike - and compares status, bytes and validators.
  npm's `-/whoami` and `-/ping` answer by who asks and are `Volatile` since that check found them.
- The headers are added only to Bastion requests and stripped from every other response, so an ordinary caller never learns
  its own shape.
- Details the table leaves open ([Q-BAS-71](#q-bas-71--the-mark-stage-is-an-edge-package-and-learns-the-caller-from-the-authentication-stage)):
  the subject is `invalid` on every `401` and on a credential that is no token (a session cookie), and `anonymous` for a caller
  without a credential; a response that no route matched or that the decision refused carries the repository when the route is
  known and no `Trove-Bastion-Read` (on `/files/` too: the raw file handler tells the marking the decision once the service allowed
  the read); a response on an interface route of the Bastion carries `Trove-Bastion-Class: pass` and
  `Trove-Bastion-Interface` only; a `/files/` path with a trailing slash, and the root, is a listing (`shared` or `private`
  like a document), any other `/files/` path a download.

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
- The probe carries no credential of a caller by design. The authentication stage admits a recognized probe as the anonymous caller
  for routing only, whatever the anonymous switches say, so that it reaches the route match ([Q-BAS-118](#q-bas-118--a-write-probe-is-admitted-as-the-anonymous-caller-for-routing-only));
  every route below `/api/<format>/` and `/files/` tells the marking stage its kind there first, and the probe goes no further (H-469).
- The main installation's middleware answers a Bastion probe after routing and before any handler or body read:
  - a route whose `RouteKind` is `Upload`, `Delete` or `Admin`, or whose coarse action is not `read`: `403` with the problem
    code `bastion_read_only` in the format's error shape (R-ARCH-023), whose detail names the main installation's URL of the
    addressed repository: `This is a read-only Bastion of https://trove.example.com; publish to
    https://trove.example.com/api/npm/npm-all/`;
  - a read route that takes a body (npm's `POST …/-/npm/v1/security/advisories/bulk`): `204` with `Trove-Bastion-Route:
    read-with-body`;
  - no route: the format's or the core's `404`.
- The Bastion reads `Trove-Bastion-Route` of the probe answer before its client removes the `Trove-Bastion-` headers
  ([Q-BAS-108](#q-bas-108--the-route-of-a-probe-answer-is-read-before-the-marking-is-stripped)).
- The Bastion replays a `403` or `404` probe answer to the caller and keeps it per method and path for
  `bastion.cache.metadata_revalidate_after`. After a `204` it forwards the real request with its body (at most
  `bastion.main.max_forwarded_body`; a larger body answers `413` from the Bastion) and the caller's credential; the answer is
  `pass`. A request with a body above the limit is refused before the probe. The credential of the real request is checked after the `204` ([Q-BAS-81](#q-bas-81--a-write-is-decided-by-the-probe-first-its-credential-is-checked-only-if-the-real-request-is-forwarded)).
- A forward of any path outside `/api/<format>/` and `/files/` - judged on the path as the router decodes it, segment by segment, so
  `/api/%76%31/tokens` is `/api/v1/tokens` - answers `404 not_found` as problem details of the management API before authentication,
  whatever its method and credential: a forward reaches no route of the management API, the login tree or the UI ([Q-BAS-115](#q-bas-115--a-request-with-trove-bastion-authorization-is-a-forward-and-a-forward-reaches-only-the-package-families)).
- A forwarded non-probe write that reaches the main installation anyway (a defect) is refused the same way, since the
  Bastion token makes every write `bastion_read_only`.
- The test is the route's kind and action, not the method alone ([Q-BAS-74](#q-bas-74--a-write-is-a-route-of-a-writing-kind-or-a-coarse-action-other-than-read)):
  a `PUT`, `DELETE` or `POST` on an `Upload`, `Delete` or `Admin` route, or on any route whose coarse action is not `read`, is
  refused whoever sent it; npm's family of unimplemented endpoints (`Admin`, whatever the method) is therefore `403
  bastion_read_only` to a Bastion, where a direct client gets its `404` or `501`. The real request of a read route that takes a
  body (npm's audit) is served.

Acceptance criteria:

- `npm publish` against the Bastion prints `E403` with the main installation's URL in the message, and `dotnet nuget push` exits
  non-zero with `403`; the main installation records no `package.published`, and the Bastion's byte counter shows that less than
  64 KiB of the upload body were read.
- `npm audit` through the Bastion returns the main installation's answer.
- 1,000 `PUT` requests to one path within a minute cause one probe to the main installation.
- With anonymous access off, the probe of `npm publish` answers `403 bastion_read_only` with the main installation's URL through the
  real authentication stage, the probe of `npm audit` `204`, and no handler runs
  (`TestR_BAS_014_AProbeIsAnsweredThroughTheRealAuthenticationStage`).

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
| `authz.project.repository_unassigned` | `grants`: the repository left its project's policies behind ([Q-BAS-120](#q-bas-120--unassigning-a-repository-from-a-project-drops-every-permission-answer-and-every-journaled-event-is-classified)) |
| `authz.ownership.removed`, `authz.ownership.released`, `package.published`, `authz.project.updated`, `authz.project.deleted`, `authz.project.admin_removed` | none: they bound publishing, reserve numbers, bound what project administrators may change, or narrow reads through events of their own (a project's policies are deleted before it, each `authz.policy.deleted`); each is listed with its reason, and every journaled event of the audit registry is either mapped or listed (`TestR_BAS_015_EveryJournaledEventIsClassified`) |

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
- **Entry IDs and paging** ([Q-BAS-49](#q-bas-49--the-interface-is-one-package-of-wire-types-four-details-of-the-contract-are-fixed-in-it)).
  An event that yields more than one entry gives the others the IDs `<event ID>:1`, `<event ID>:2`. A read is two queries: all
  entries within the lookback up to and including the cursor, and the entries after it, cut by `limit`, so a page always advances;
  when the entries behind the cursor exceed four times `bastions.feed.max_page`, the answer is `reset: true`.
- **Reset.** `reset: true` when the entry the cursor names no longer exists although its time lies within the audit retention
  (a point-in-time restore or a failover that lost commits), when the cursor's time lies after the main installation's clock by
  more than `lookback`, or when it lies before the audit retention. The Bastion then drops every answer (R-BAS-026) and takes the
  returned cursor.
- **History.** `after` may name a synthetic cursor `t:<RFC 3339>` up to the audit retention back, so a starting replica reads the
  entries of the whole lifetime of an answer (R-BAS-026,
  [Q-BAS-33](#q-bas-33--a-replica-starts-with-the-feed-history-of-an-answers-lifetime)).
- Entries carry IDs, never names or secrets. The feed costs one indexed query per poll and replica.
- **Cursor and cuts in detail** ([Q-BAS-68](#q-bas-68--the-feed-advances-over-every-event-it-consumed-is-cut-at-event-boundaries-and-resolves-member-names-when-it-is-read)).
  The cursor names the last audit event the read consumed, also one that yielded no entry; a page ends between events, so the two
  entries of one event are never separated; the member names of a membership event become IDs when the feed is read, and an event
  whose target cannot be read drops every answer (`grants`).

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
- **Events that cannot be recorded** ([Q-BAS-67](#q-bas-67--a-download-event-that-breaks-its-contract-is-dropped-and-counted-the-batch-is-refused-only-when-it-is-too-large)).
  An event that breaks its contract (no time, repository or path, a SHA-256 that is no digest) is dropped like one older than `max_age`;
  both are added to the count of `bastion.downloads.dropped`, which also names the parts (`reported`, `expired`, `invalid`). A batch above
  `max_batch` is refused `422`.
- **Budget** ([Q-BAS-117](#q-bas-117--download-events-are-charged-to-their-registration-by-their-count)). Every batch is charged to its registration by its events, in units of 100 rounded up, on a cluster limiter of R-ARCH-034
  (`bastion.download_events`, bucket the registration). A batch the budget does not hold answers `429 rate_limited` with `Retry-After`
  and records nothing; the Bastion keeps it and sends it again (Q-BAS-78), and what its bounded buffer cannot hold meanwhile is counted
  as lost (R-BAS-039). `events_burst` holds `max_batch`, so every batch can fit; the budget is per registration (H-468).

```yaml
bastions:                              # the main installation's section (the Bastion's own is bastion)
  downloads:
    max_batch: 1000                    # default (proposed)
    max_age: 720h                      # default (proposed)
    events_per_second: 200             # default (proposed): per registration, across the replicas
    events_burst: 2000                 # default (proposed): holds max_batch; the window is events_burst / events_per_second; units of 100
```

Acceptance criteria:

- With `audit.record_downloads: true`, a warm `npm ci` of 20 tarballs through the Bastion yields 20 `artifact.downloaded`
  events with `origin: bastion`, the callers' token ID and client address, within two `flush_interval`s; the cold run's 20 were
  recorded by the main installation without `origin`.
- A batch with `"dropped": 7` yields one `bastion.downloads.dropped` event with count 7.
- With the defaults, two batches of 1,000 events are recorded and a third within the window answers `429` with `Retry-After` and records
  nothing; another registration is not slowed down (`TestR_BAS_016_DownloadEventsAreChargedToTheRegistrationByTheirCount`).

### R-BAS-017 · The real client and the Bastion's host at the main installation
Priority: **MVP** · Covers: trove-only

The main installation counts the real client and renders documents for the Bastion's host (owner,
[Q-BAS-17](#q-bas-17--the-bastion-mirrors-the-main-installations-url-space), [Q-BAS-21](#q-bas-21--internet-exposure-limits-blocking-failed-sign-in-timeouts-and-a-configurable-cache)):

- **Client address.** The Bastion sends exactly one forwarded-for entry, the client address it determined with its own
  `server.trusted_proxies` (R-BAS-019), in both header families: `X-Forwarded-For: <address>` and `Forwarded: for=<address>`
  (quoted and bracketed for IPv6). It replaces whatever forwarded headers the caller sent. The main installation lists the
  Bastions' egress addresses in `bastions.trusted_proxies`, not in `server.trusted_proxies` ([Q-BAS-116](#q-bas-116--the-bastions-forwarded-headers-count-only-for-a-recognized-request-from-an-address-of-bastionstrusted_proxies)). After the Bastion
  token was checked, a request whose address - as R-ARCH-006 resolves it with `server.trusted_proxies`, which name the main
  installation's own ingress - is one of `bastions.trusted_proxies` is resolved again with those addresses trusted as well, so the
  client address is the caller's, in whichever family `server.forwarded_header` selects; the rate limits, the failed-credential
  throttle and the audit then apply to the client, not to the Bastion. A request from the same address without a valid Bastion
  token - any workload that shares the Bastion's egress - forwards nothing: its client address stays the egress address.
- **Host.** The Bastion sends its base URL's scheme, host and port as `X-Forwarded-Proto`, `X-Forwarded-Host`,
  `X-Forwarded-Port` and as `proto=` and `host=` of the same `Forwarded` element; the main installation counts them by the same rule
  as the client address (behind its own ingress they are what the ingress reports, H-467). The main installation lists the Bastion's
  base URL in `server.additional_base_urls`, so every URL it renders for the Bastion is on the Bastion's host (R-OPS-005,
  R-EP-003); the handshake proves it (R-BAS-010). The Bastion therefore carries no format knowledge: it never rewrites a body.
- Main installation values for the example *(example)*:

```yaml
server:
  trusted_proxies: ["10.244.3.17", "10.244.5.9"]           # example: the main installation's own ingress pods, never the Bastion
  additional_base_urls: ["https://packages.example.com"]   # example
bastions:
  trusted_proxies: ["198.51.100.20/32"]                    # default []; example: the address the Bastion connects from
```

Acceptance criteria:

- A caller at `203.0.113.7` sending `X-Forwarded-For: 192.0.2.1` through the Bastion appears in the main installation's request
  log and audit as `203.0.113.7`, with either value of the main installation's `server.forwarded_header`.
- `npm view @acme/sdk dist.tarball` through the Bastion prints a URL on `packages.example.com`, and no body or header the e2e run
  captures at the Bastion contains `trove.example.com`, except the `bastion_read_only` detail of R-BAS-014.
- A request from the Bastion's egress address without a Bastion token that sends `X-Forwarded-For: 203.0.113.7` and
  `X-Forwarded-Host: packages.example.com` is served with the egress address as its client and on the main installation's base URL
  (`TestR_BAS_017_AnUnrecognizedRequestFromTheBastionsEgressForwardsNothing`,
  `TestR_BAS_017_TheBastionsForwardedHeadersCountOnlyForARecognizedBastionRequest`).
- A loop of failed credentials from one caller through the Bastion is throttled by the main installation per that caller's
  address, while a second caller through the same Bastion is not slowed down.

## Serving at the Bastion

### R-BAS-018 · Request classification at the Bastion
Priority: **MVP** · Covers: trove-only

The Bastion holds no repository list; every repository of the main installation is reachable under the same paths (owner,
Q-BAS-17). Each request passes these steps in order; the first that answers ends it:

1. **Host.** The effective host (R-EP-003 rules, with the Bastion's own trusted proxies) must be the host of
   `server.base_url`; any other host, an IP literal and a missing host answer `421 misdirected_request` (a port is not part of the host, [Q-BAS-83](#q-bas-83--the-engine-decides-the-host-and-takes-the-client-address-from-the-chain)).
2. **Block list** (R-BAS-037): a blocked address answers `429 address_blocked`.
3. **Path family.** `/api/v1/…` and every path outside `/api/<format>/<key>/…` and `/files/<key>/…` answer `404
   bastion_not_served` (R-BAS-003). `<key>` is the addressed repository key for the Bastion's bookkeeping until the main
   installation's `Trove-Bastion-Repository` confirms it; a key that does not match `^[a-z0-9][a-z0-9._-]{1,63}$` (INV-5)
   answers `404` without a request. The Bastion reads no other part of a path. A family, format or key segment that is
   percent-encoded and a `.` or `..` segment are not classified: the path answers `404 bastion_not_served` (Q-BAS-121).
4. **Per-address limits** (R-BAS-035) and **credentials** (R-BAS-019), including the failed-authentication timeout (R-BAS-036).
5. **Method.** `GET` and `HEAD` go to the read flow (R-BAS-020 to R-BAS-024); `OPTIONS` answers `405`; every other method goes
   to the write refusal of R-BAS-014. A write needs no credential before its probe ([Q-BAS-81](#q-bas-81--a-write-is-decided-by-the-probe-first-its-credential-is-checked-only-if-the-real-request-is-forwarded)).
6. **Read flow**: the cache entry for the request's cache key and the caller's answers decide between a hit, a check and a miss.

- **Cache key**: the path and query exactly as received (percent-encoding kept), and the normalized values of the request
  headers the main installation's `Vary` named for that path (`Accept`, `Accept-Encoding` for npm documents). The host is
  constant. A replica learns the names from the responses it receives and, for a target it has not seen, from the entry that a peer
  also stored under the key without the values, so a document one replica kept is a hit on every replica that shares the backend
  (`TestR_BAS_023_AnotherReplicaServesTheDocumentAPeerKeptWithoutKnowingItsVary`).

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
  request; before the first successful handshake the setting is not known and an anonymous request answers `503 main_unreachable`
  ([Q-BAS-99](#q-bas-99--a-replica-that-has-not-handshaken-answers-an-anonymous-request-503)); with `true` the anonymous caller's answers are kept under the key `anonymous`, shared by every anonymous caller, since
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
  period; a widening of a right may take the period to reach the Bastion (owner). A `403` is kept; a `404` that is not marked `absent` never is,
  whether it answers a plain fetch or a conditional request, because it may be a missing name or a hidden one, and replaying it only
  for names the Bastion holds would show a narrowed reader which hidden names are cached
  ([Q-BAS-124](#q-bas-124--a-404-that-is-not-marked-absent-is-never-kept-as-a-url-answer), which replaces [Q-BAS-80](#q-bas-80--a-404-that-answers-a-plain-fetch-is-not-kept-as-the-callers-url-answer)).
- Answers live in the replica's memory (at most `bastion.answers.max_entries`, least recently used first) and, with Valkey, in
  Valkey as signed records (R-BAS-034). A replica may refresh an answer that a request uses in the last fifth of its period in
  the background, with one collapsed request (the shape answer, through the check; URL answers renew by use, [Q-BAS-84](#q-bas-84--the-background-refresh-renews-shape-and-credential-answers-url-answers-renew-by-use)).

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
  transfer is aborted and nothing is kept. The spooling does not wait for the caller
  ([Q-BAS-123](#q-bas-123--the-fill-reads-the-main-installation-at-its-own-speed-and-no-client-holds-a-slot-or-a-waiter)): the fill reads the
  main installation's body into the spool at the main installation's speed and frees the budget slot when the body ends, and the
  caller is served from the spool at its own speed. A fill goes on when its caller goes away, so the next request is a hit. A spool that
  cannot take the bytes cuts the transfer; the bytes are never passed on unverified. A document is kept once it has arrived whole. An artifact without `Content-Length`
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
  - fresh: `now < validatedAt + bastion.cache.artifact_revalidate_after` and no `content` or `grants` entry for its repository was received
    after `validatedAt − 15 s` ([Q-BAS-31](#q-bas-31--cached-artifacts-are-revalidated-after-a-period)); the `content` entry also ends
    an entry's service while the main installation is unreachable and its periods are extended
    ([Q-BAS-122](#q-bas-122--a-content-entry-ends-the-service-of-a-cached-entry-also-while-the-main-installation-is-unreachable));
  - allowed: a usable read-shape answer `full` for the repository, or a usable URL answer `allowed` for the cache key.
- **Check** otherwise: a conditional `GET` with the caller's own credential and `If-None-Match: "<sha256>"` through the real
  route ([Q-BAS-27](#q-bas-27--a-cached-artifact-is-checked-through-the-real-route)). `304`: the bytes are served from the cache,
  and `validatedAt` and the caller's URL answer are renewed. `200`: the new bytes replace the entry and are served. A refusal:
  the main installation's answer is replayed, a `403` is kept as the caller's URL answer and a `404` is not (Q-BAS-124), and the entry stays for other callers; only a
  `404` marked `absent` removes it. The main installation answers the conditional from PostgreSQL without reading its object
  store (R-STOR-012), so a check costs no artifact transfer.
- **Refuse without a request** when the caller's shape answer is `none`: the Bastion answers the stored status (R-BAS-029).
- `Range` and `If-Range` with the content address, `If-None-Match` and `HEAD` from the caller are answered from the entry
  (RFC 9110), after the same permission rule. A range is verified by reading the object to its end ([Q-BAS-82](#q-bas-82--a-range-is-verified-by-reading-the-object-to-its-end-a-callers-range-or-conditional-on-a-miss-is-answered-from-the-verified-spool)).

Acceptance criteria:

- Warm cache, `$FULL` with a usable `full` answer: `npm ci` of 20 tarballs sends no request to the main installation.
- Warm cache, `$PART` for `@acme/sdk` tarballs: 1 conditional request per tarball, each answered `304`, no tarball bytes from
  the main installation; for a tarball outside `@acme/*` the main installation's `404` is replayed, and asked for again by each request.
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
  as its private copy; a refusal is replayed. A `304` marked `private` to the Bastion's own condition (the caller is a partial
  reader whose document is the same bytes) is answered with the entry's bytes as the caller's `200`, and the entry is not renewed
  ([Q-BAS-107](#q-bas-107--a-304-for-a-private-document-is-answered-with-the-shared-entrys-bytes)).
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
- `$PART` asking for a missing name twice sends two requests to the main installation, and asking for a name that is cached but
  hidden from it twice sends two as well (`TestR_BAS_024_ANarrowedReaderCannotTellACachedHiddenNameFromAMissingOne`).

### R-BAS-025 · Concurrency budget towards the main installation and collapsed misses
Priority: **MVP** · Covers: trove-only

- **Budget** ([Q-BAS-35](#q-bas-35--the-concurrency-budget-is-per-replica-with-a-share-for-anonymous-misses)). Every request to
  the main installation except the handshake and the feed takes a slot: forwards, conditional requests, checks, probes,
  download batches. A replica holds at most `ceil(bastion.main.max_concurrent ÷ bastion.replicas)` slots, of which requests on
  behalf of anonymous callers may hold at most `bastion.main.anonymous_share`, rounded down and at least one slot while the share is above 0
  ([Q-BAS-75](#q-bas-75--the-anonymous-cap-of-the-budget-is-the-share-rounded-down-at-least-one-slot)). A request that gets no slot within
  `bastion.main.queue_timeout` answers `503 bastion_busy` with `Retry-After: 1`. Misses can therefore never put more than the
  budget onto the main installation, whatever arrives. A slot is held until the response body ends, and no client's speed
  decides how long that is ([Q-BAS-123](#q-bas-123--the-fill-reads-the-main-installation-at-its-own-speed-and-no-client-holds-a-slot-or-a-waiter)):
  an artifact fill reads its body at the main installation's speed; the body of a pass-through is cut when the client takes a chunk
  slower than 10 s or, after a grace period of 30 s, averages less than 64 KiB per second. A write probe takes the class of the
  caller (a caller that presented a credential takes an authenticated slot, so `anonymous_share: 0` does not refuse its write
  with `503`).
- **Collapsing.** Concurrent misses for one cache key share one fetch per replica (single-flight), and with Valkey one fetch
  across replicas with the bucket backend, under a fetch lock of R-ARCH-033's form (`trove-bastion:<prefix>:flight:<sha256 of
  the cache key>`, 30 s, extended by the holder). A waiter is served from the result only when its own answer allows it
  (R-BAS-022, R-BAS-023); otherwise it makes its own conditional request. Collapsing never shares a decision. The flight ends
  when the fill is committed, not when the leader's client has taken the bytes, and a waiter that has waited 30 s answers `503
  bastion_busy` with `Retry-After: 1` and makes no request of its own. A result that depends on the caller's rights (a private
  document, a refusal) keeps collapsing off for that caller only ([Q-BAS-125](#q-bas-125--a-result-that-depends-on-the-callers-rights-switches-collapsing-off-for-that-caller-only)).
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
- Three connections that take a cold tarball one socket buffer at a time hold no slot: an unrelated anonymous request is
  served at once, and a second caller of the same cold key is served from the committed entry
  (`TestR_BAS_025_ClientsThatReadSlowlyDoNotHoldTheBudgetSlots`, `TestR_BAS_025_AWaiterOfACollapsedFetchIsNotHeldByTheLeadersClient`).

### R-BAS-026 · Applying the revocation feed
Priority: **MVP** · Covers: trove-only

The Bastion follows the revocation feed every few seconds, so a revoked token or a narrowed right is dropped within seconds
(owner, Q-BAS-8):

- Every replica polls `feed` every `bastion.feed.poll_interval` and applies each new entry by its ID: it records the entry with
  its receipt time, which makes every matching answer unusable (R-BAS-020 rule 3), and deletes the matching answers from memory
  and, best effort, from Valkey. `settings` triggers a handshake. A page is recorded as a whole and then the answers it matches are deleted in one pass
  ([Q-BAS-126](#q-bas-126--the-link-applies-a-page-as-a-unit-ends-the-start-read-at-the-end-of-the-feed-and-bounds-each-call)). Entries are kept for
  `answers.period + grace_period + 15 s`; the IDs of applied entries for at least the feed's lookback (5 minutes, the default of
  `bastions.feed.lookback`) as well, so that an entry the feed sends again is not applied again (H-470).
- **Start.** A starting replica reads the feed from `t:<now − answers.period − answers.grace_period − 15 s>` before it uses any
  answer from Valkey; that time is the start of its feed history (rule 4). Until the first read succeeds it uses no shared answer. A read that returns no entry moves a synthetic cursor forward to
  the time of the request, so the next read is cheap and the main installation's lookback covers late commits
  ([Q-BAS-76](#q-bas-76--the-synthetic-feed-cursor-moves-to-the-send-time-of-an-empty-read)). The history starts, and the
  unreachable state of a start ends, when the start read returns a page that says `more: false`; a first page that says
  more is not that read.
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
- The 5 s of a page of 1,000 entries against 200,000 answers is a target of the reference environment: `make test-performance` enforces it with `TROVE_PERF_GATE=1`; per change the test bounds the page by 100 times one matching pass over the same answers ([Q-BAS-140](#q-bas-140--where-the-5-s-of-a-burst-page-is-enforced)).

### R-BAS-027 · The main installation unreachable: grace period, then nothing
Priority: **MVP** · Covers: trove-only

Answers the Bastion already holds stay valid for a configurable grace period beyond their normal period (owner,
[Q-BAS-9](#q-bas-9--the-main-installation-unreachable-grace-period-503-for-unseen-tokens-then-nothing)):

- **Unreachable** ([Q-BAS-44](#q-bas-44--unreachable-means-a-failed-handshake-or-a-stale-feed)) is a state of each replica: it
  begins when the handshake fails (connect or TLS error, timeout, `5xx`, `401 bastion_token_invalid`, `400
  bastion_interface_mismatch`, a base URL mismatch) or when no feed poll has succeeded for `bastion.main.unreachable_after`, and
  ends when a handshake and a feed poll succeed again. Forward errors alone do not begin it (a `401 bastion_token_invalid` to a forward is a forward error, Q-BAS-121), and neither does one failed feed
  poll. Every call of the link ends after `unreachable_after`, so a connection that swallows the request is a failed call at that time
  and not at `response_header_timeout`. A replica starts unreachable, since its start; the first poll that succeeds after a failed one is followed by a
  handshake; a `401 bastion_token_invalid` or a mismatch in the answer to any request begins the state at once
  ([Q-BAS-79](#q-bas-79--the-link-is-one-state-machine-per-replica-run-by-the-server)).
- While unreachable, the replica sends nothing to the main installation except handshake and feed probes every
  `poll_interval`, and:
  - a caller whose answers allow a cached entry (R-BAS-022, R-BAS-023, with rule 1 extended by `grace_period`) gets cached
    artifacts and shared documents, also past their revalidation periods (possibly stale metadata), and its own private copies; an
    entry that a `content` entry of the feed (which the link still reads while it can) names, received after `validatedAt − 15 s`,
    is not served: the grace period extends an answer, never past a withdrawal (Q-BAS-122);
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
  `sha256/<h0h1>/<h2h3>/<64 hex>` (the bytes of artifacts, INV-4's form), `documents/<h0h1>/<h2h3>/<64 hex>` (the bytes of shared
  documents, [Q-BAS-87](#q-bas-87--the-bytes-of-documents-have-a-tree-of-their-own)), `entries/<k0k1>/<sha256 of the cache key>.json` (signed entries),
  `access/<64 hex>` (empty access markers, R-BAS-031), `audit-spool/<replica>/<unix ms>.jsonl` (R-BAS-039). Only `PutObject`,
  `GetObject`, `HeadObject`, `ListObjectsV2` and `DeleteObject` are used; no versioning, no conditional write.
- **`volume`**: a local volume per replica, the same layout below `bastion.cache.volume.dir`; generic ephemeral
  (`ReadWriteOnce`, deleted with the pod, the default) or persistent per pod (a StatefulSet with a claim template). No volume is
  shared (INV-13). Without routing by path (R-BAS-033) each replica fills its own copy.
- A fill is spooled to the pod's temporary volume (`tmp`, bucket backend) or to a temporary directory of the cache volume, and
  is written or renamed to its content address only after verification (R-BAS-021). The fills in progress of a replica hold
  together at most `bastion.cache.spool_max_bytes`; a write that would pass it makes that fill unkept (the answer is passed
  through, `trove_bastion_cache_errors_total{op="spool"}`) and gives its bytes back at once. The chart sizes the `tmp` volume from
  the spool and refuses a volume the spool does not fit
  ([Q-BAS-131](#q-bas-131--the-bytes-of-the-fills-in-progress-are-bounded-and-the-chart-sizes-the-volumes-from-the-bound),
  [H-450](#h-450--a-fill-that-does-not-fit-the-spool-is-passed-through-unkept-and-a-request-answered-from-the-spool-fails)).
- The bucket's client fails fast: three attempts and at most 2 s of backoff, not the object store's five attempts and 20 s, so a
  failing bucket is a miss within seconds ([Q-BAS-89](#q-bas-89--the-bucket-backend-reaches-s3-through-a-plain-adapter-and-fails-fast)).
- The backend is gated at the first readiness only (R-STOR-035's pattern); a failing backend afterwards turns hits into misses
  that are passed through, counted in `trove_bastion_cache_errors_total{op}`, never into failed requests.

```yaml
bastion:
  cache:
    backend: volume                     # default (proposed): volume | bucket
    max_bytes: 20GiB                    # default (proposed)
    max_object_bytes: 2GiB              # default (proposed); larger answers are passed through
    spool_max_bytes: 2GiB               # default (proposed); the fills in progress of a replica together; at least max_object_bytes
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

- **Size.** Bytes plus documents stay below `bastion.cache.max_bytes`; documents below `documents_max_bytes`, which is their reserved
  share: artifacts are limited to `max_bytes − documents_max_bytes` ([Q-BAS-88](#q-bas-88--documents-have-a-reserved-share-and-entries-are-removed-when-read)). A sweep runs every
  `bastion.cache.sweep_interval` and, above 95 % of a limit, deletes in order of last access until 85 % remain
  ([Q-BAS-43](#q-bas-43--the-bucket-backend-evicts-by-listing-access-markers)).
- **Last access.** A served entry writes its access marker (an empty object or file named by its content address, or a touch of
  the entry) at most once per `bastion.cache.access_resolution` per replica; the sweep orders by the markers' modification times,
  read with two listings, and deletes the bytes, the entries that point at them and the markers. An entry whose bytes are gone is
  a miss and is removed when it is read; the sweep itself does not read entries (Q-BAS-88, H-452).
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
  unverifiable and costs one refill (performance only). With the volume backend and no Valkey the key may be absent: the entries are
  then stored without a signature, the bytes are verified all the same ([Q-BAS-98](#q-bas-98--the-cache-takes-no-signer-where-the-configuration-allows-no-key-and-keeps-plain-entries), H-460).

Acceptance criteria:

- Replacing a cached tarball's object in the bucket with other bytes makes the next download abort before its last 64 KiB, and
  the following one fetch from the main installation.
- An entry rewritten in the bucket to point at another content address, and an answer written into Valkey by hand, are ignored.

### R-BAS-033 · Routing by path for local volumes
Priority: **MVP** · Covers: trove-only

- With `backend: volume` and `cacheVolume.routeByPath: true`, the chart makes HAProxy Ingress choose the replica by a
  consistent hash of the request path (URI hashing), so each artifact lives on one replica and the volumes together hold more
  than one could (owner, Q-BAS-11). The annotation names are R-BAS-045's; the controller has no key for consistent hashing, so
  the chart renders it as a `config-backend` snippet, which a controller that forbids snippets drops without failing (H-434).
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
  `trove_valkey_available` is 0 and `TroveBastionValkeyUnavailable` fires (R-BAS-041). The shared metrics are
  `trove_valkey_available` and `trove_ratelimit_fallback{limiter}` (1 for a limiter that answers from its share).
- **The keys as formed** ([Q-BAS-91](#q-bas-91--the-bastions-keys-are-formed-by-the-cluster-package-without-a-keyspace-and-reuse-the-fetch-lock)):
  `trove-bastion:<valkey.key_prefix>:<mechanism>:v1:<parts>`: `block:v1:<address>`, `block:level:v1:<address>`,
  `timeout:v1:<address>`, `ans:v1:<kind>:<sha256>`, `lock:fetch:v1:<sha256>`, `sweep:v1`, and the counters `rl:v1:…` of R-ARCH-034.
  The table above names the families; the version segment, the encoding of an address and the reuse of the fetch lock are decided
  in Q-BAS-91.
- **Signed answers** ([Q-BAS-93](#q-bas-93--the-store-signs-and-verifies-the-answer-records)): the store signs a record on `Put` and verifies it on `Get`; a
  record that fails is not returned, is deleted (only if it still holds the failed value) and counts as `bad_signature`.

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
- The request limits are the token bucket (without Valkey) or the sliding window (with Valkey) of R-ARCH-034; the miss limits
  are counts of the sliding minute in both forms
  ([Q-BAS-92](#q-bas-92--the-counters-are-sliding-windows-and-the-timeout-starts-at-the-last-allowed-failure)). A limit admits up
  to its rate: a refused request or miss does not consume the budget of its address, so a client slightly over its limit is
  admitted at the limit and not starved, and the refusals it collects stay far below the threshold that blocks
  ([Q-BAS-127](#q-bas-127--a-refused-request-or-miss-does-not-consume-the-limit-it-was-refused-by)). The sliding estimate is
  approximate around a window boundary ([H-474](#h-474--the-sliding-estimate-of-a-limit-admits-more-than-the-limit-in-a-minute-that-spans-a-boundary)).

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
- An address that asks for 330 misses a minute with a credential (limit 300) is admitted about 300 a minute from the second
  minute on, answers `rate_limited` for the rest, and is not blocked; the same holds for an anonymous client at 11 requests a
  second against 10, with and without Valkey.

### R-BAS-036 · Failed-authentication timeout
Priority: **MVP** · Covers: trove-only

- More than `bastion.failed_auth.max_failures` failed authentications from one address within `bastion.failed_auth.window` put
  the address into a timeout of `bastion.failed_auth.timeout`. A failure is a credential the Bastion refuses itself (R-BAS-019)
  or one the main installation answered `invalid`.
- During the timeout, a request with a credential for which the Bastion has no usable answer answers `429 auth_timeout` with
  `Retry-After` and is not sent to the main installation; a credential with a usable answer keeps working, so colleagues behind
  one address are not cut off ([Q-BAS-36](#q-bas-36--a-failed-authentication-timeout-refuses-unverified-credentials-only));
  anonymous requests stay anonymous requests.
- The counter is a count-only limiter of R-ARCH-034 (`bastion.failed_auth`). Its `window` is 1 s to 12 h, the range of the
  limiters of R-ARCH-034; the configuration refuses a longer one, so a configured window is never counted shorter than written
  ([Q-BAS-128](#q-bas-128--the-local-forms-evict-in-constant-time-the-block-decision-locks-per-address-and-the-window-range-is-the-limiters)). The Bastion's limit is stricter than the main
  installation's R-AUTH-038, which still applies to the real client behind it (R-BAS-017).
- The failure that brings the count of the window to `max_failures` starts the timeout, so the next credential without a usable
  answer is the first one refused (Q-BAS-92); the acceptance below is that reading.

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
- Blocking is the answer to a flood and not to a client slightly over its limit: only a request or miss beyond the limit is a
  refusal and a refusal consumes nothing (Q-BAS-127), so a client at 110 % of its limit collects a tenth of its requests as
  refusals, and the default threshold of 300 a minute is reached by an address that sends several times its limit.
- A blocked address answers `429 address_blocked` with `Retry-After` at step 2 of R-BAS-018, before any further work, and its
  requests are not logged one by one (a counter and one log line per block).
- With Valkey the block list is shared: a block set by one replica holds on every replica at its next request. Without Valkey
  each replica blocks by its own count (H-389).
- `trove_bastion_blocks_total{reason}`, `trove_bastion_blocked_addresses`, and the log line `bastion address blocked` with the
  address, the reason, the duration and the count: the number of blocks of the address within 24 h, this one included
  (Q-BAS-92). The reason of the metric is `refusals`; the log line names the kind of refusal that crossed the threshold.

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
- An address that sends 3,000 anonymous misses in 30 seconds is blocked, whereas an address that sends 330 misses a minute
  with a credential is not (Q-BAS-127).

### R-BAS-038 · The coarse per-address limit at HAProxy Ingress
Priority: **MVP** · Covers: trove-only

- The chart can put a coarser per-address limit in front, at the Bastion's HAProxy Ingress: `edgeLimit.enabled` (default on)
  renders HAProxy Ingress's per-address request-rate and connection limits for `edgeLimit.requestsPerSecond` and
  `edgeLimit.connections` on the Bastion's Ingress (owner, Q-BAS-16). It can be switched off.
- It is set wider than the Bastion's own: the chart refuses `edgeLimit.requestsPerSecond` at or below
  `config.bastion.limits.authenticated.requests_per_second`, so the Bastion's stage, which knows credentials and misses, decides
  first and the ingress only cuts what would overload the Bastion itself.
- What the controller does with the values (verified for v0.16.3, [Q-BAS-56](#q-bas-56--haproxy-ingress-annotations-as-verified-against-the-controller-of-the-local-profile)):
  it tracks the connection's source address in a stick table and answers `429` while the address has more than
  `edgeLimit.connections` concurrent tracked streams or more than `edgeLimit.requestsPerSecond` new ones in the last second,
  except for the addresses of `limit-whitelist`. There is no burst: a client whose rate is above the per-second value is
  refused while it stays above. The address is the TCP peer's, so behind a load balancer that does not pass the client
  address on, all callers share one budget (H-432).
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
- A drain writes one spool file, one batch of at most `batch_size` events per line, the unreported `dropped` count on the first
  line; `spool_max_events` bounds one drain's file. A batch the main installation refuses as a whole (`400`, `413`, `422`) is
  dropped and counted, every other failure is retried; a spool file of which some lines were delivered is replaced by one with
  the rest ([Q-BAS-78](#q-bas-78--a-refused-batch-is-dropped-everything-else-is-retried-and-a-spool-file-is-one-drain)).

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
| `TroveBastionMainUnreachable` | `max(trove_bastion_main_reachable) == 0` (every replica) | 2m | critical |
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

Expressions are sketches. Two things differ from them in the chart ([Q-BAS-57](#q-bas-57--the-rule-tests-run-with-a-pinned-promtool-and-the-rules-are-scoped-to-the-release)):
`TroveBastionMainUnreachable` uses `max(…) == 0`, because `min(…) == 0` is true as soon as one replica is cut off and would fire
next to the single-replica alert instead of only when every replica is; and every selector is scoped to the release
(`namespace` and `service="<fullname>-ops"`, the labels the chart's ServiceMonitor puts on the series), because one Prometheus
scrapes several Bastions and an aggregate over all of them would blend their states.

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
- **Who runs what.** The server runs the link (handshake, feed, reachability), the token reloader, the download buffer and the cache
  sweeper for the life of the process and drains them in the order below; the engine reads their state, starts none of them and runs
  only its own janitor ([Q-BAS-79](#q-bas-79--the-link-is-one-state-machine-per-replica-run-by-the-server),
  [Q-BAS-100](#q-bas-100--one-implementation-of-the-link-the-budget-the-sweep-and-the-cache-use-and-the-role-builds-real-components-only),
  [Q-BAS-101](#q-bas-101--the-server-runs-the-cache-sweeper-and-the-drain-stops-it-before-the-engine)).
- **Readiness** does not depend on the main installation
  ([Q-BAS-44](#q-bas-44--unreachable-means-a-failed-handshake-or-a-stale-feed)): a replica that cannot reach it still serves what
  the grace period allows. It fails while draining, while the listeners are not bound, and before the cache backend's first
  check passed.
- **Drain** follows R-ARCH-024: readiness flips, the drain delay passes, in-flight requests finish within the termination grace
  period, the sweeper and the buffer's flush loop stop, the engine drains, the audit buffer is sent or spooled (R-BAS-039), the link
  and the token reloader stop, the Valkey leases are released. A transfer cut at the deadline is
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
- the connection to the main installation (`https` required: an `http` `bastion.main.url` or `bastion.main.packages_url`
  stops startup; the trust is the system roots plus `trustedCA`);
- **after the main installation's cut into roles** (phase 6a; the key `bastion.main.packages_url` is built, the chart of the
  main installation renders the roles with WP6a.3; [ha-and-helm.md](ha-and-helm.md) R-HA-002, Q-HA-74): the forwards and
  the probes of the client (`mainclient.HTTPClient.Do`) go to `packages_url`, the four interface operations (handshake,
  check, feed, downloads) to `url`; the same trust (system roots plus `trustedCA`) applies to both; the interface of R-BAS-008 (`/api/v1/bastion/…`) is served by the
  main installation's role `api` and the forwarded package and raw-file requests by `packages`. A `bastion.main.url` that
  names the main installation's edge reaches both, because the edge splits by path. A Bastion that calls the main
  installation's Services directly sets `bastion.main.url` to the Service of `api` and `bastion.main.packages_url` to the
  Service of `packages`; the forwards then go to `packages_url`, everything else to `url`. `packages_url` empty (the
  default) means `url` for both;
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
| Ingress | `<fullname>` | host of `config.server.base_url`; `ingress.tls`; the annotations of `edgeLimit`, `cacheVolume.routeByPath` and `ingress.backendTLS` |
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
  `edgeLimit` wider than the Bastion's limits (R-BAS-038); `config.server.trusted_proxies` while `ingress.enabled`, because behind
  the Ingress every request arrives from the controller's pods
  ([Q-BAS-130](#q-bas-130--behind-the-ingress-the-chart-requires-the-trusted-proxies)); the spool of fills
  (`config.bastion.cache.spool_max_bytes`) at least `max_object_bytes`, and a `tmp.sizeLimit` (bucket backend) or
  `cacheVolume.size` (volume backend) that holds it ([Q-BAS-131](#q-bas-131--the-bytes-of-the-fills-in-progress-are-bounded-and-the-chart-sizes-the-volumes-from-the-bound));
  with `highAvailability.enforce` at least two replicas, a PDB and
  host spreading; a value under `config` that is chart-managed or not in the role's schema; a certificate source for the
  listener while `tls.enabled` (`tls.existingSecret`, or `tls.certManager.issuerRef.name`); an egress target
  (`networkPolicy.egress.main.to`, and `valkey`, `cacheBucket` or `tracing` while they are used) and an ingress peer for the
  https port, because an empty peer list would mean "anywhere"; exactly one key in `trustedCA.keys`; an entry of
  `ingress.annotations` that the chart renders itself; a name in `metrics.prometheusRule.disabledAlerts` that is no alert
  ([Q-BAS-55](#q-bas-55--what-a-render-needs-and-what-the-values-file-left-open)).
- **HAProxy Ingress annotations** *(verified against HAProxy Ingress v0.16.3, the controller of the local profile,
  [Q-BAS-56](#q-bas-56--haproxy-ingress-annotations-as-verified-against-the-controller-of-the-local-profile))*, all with the
  controller's default prefix `haproxy-ingress.github.io/`:
  - `edgeLimit`: `limit-rps`, `limit-connections` and `limit-whitelist` (from `config.bastion.blocking.exempt_cidrs`,
    comma-separated, omitted while the list is empty);
  - `cacheVolume.routeByPath`: `balance-algorithm: uri`, `assign-backend-server-id: "true"` (stable server IDs, so a pod added or
    replaced moves about `1 ÷ n` of the paths) and `config-backend: hash-type consistent`, because the controller has no key of
    its own for consistent hashing;
  - `tls.enabled`: `secure-backends: "true"`, `secure-sni: <fullname>.<namespace>.svc` and, while `ingress.backendTLS.verify`,
    `secure-verify-ca-secret` (`ingress.backendTLS.caSecret`, empty = the listener's Secret, which must carry `ca.crt`).

  A user value of `ingress.annotations` for one of these keys is refused. A controller other than HAProxy Ingress ignores them,
  and the chart's NOTES say so (H-434).

Acceptance criteria:

- `helm template` with only the required values (`config.server.base_url`, `config.server.trusted_proxies`,
  `config.bastion.main.url`, `secrets.mainToken.existingSecret`, a certificate source `tls.certManager.issuerRef.name` and the
  egress target `networkPolicy.egress.main.to`, [Q-BAS-55](#q-bas-55--what-a-render-needs-and-what-the-values-file-left-open)) renders a
  Deployment with two replicas, a PDB, the Ingress with the edge-limit annotations, a NetworkPolicy, and no Secret, no Role and no
  PersistentVolumeClaim.
- The chart test pins the annotation names to the controller version of the local profile: a change of
  `DEV_HAPROXY_INGRESS_IMAGE` fails it until the names are verified against the new version. The e2e suite (R-BAS-049) runs
  against that controller: the edge limit refuses a flood, routing by path pins a path to one pod.

### R-BAS-046 · Values reference
Priority: **MVP** · Covers: DIST-024

Every value of `trove-bastion` with its default; in the block below each value carries `# default (proposed)` or
`# example`, and the chart's `values.yaml` holds the same keys and comments a value only where its name does not say it. Keys under `config`
are the Bastion's configuration file (R-BAS-001); the chart-managed keys (`server.tls`, `bastion.main.token_file`,
`bastion.main.ca_file`, `bastion.signing_key_file`, `bastion.cache.volume.dir`, `bastion.cache.bucket.ca_file`,
`bastion.cache.bucket.credentials.access_key_id_file` and `secret_access_key_file`, `bastion.replicas`, the Valkey secret files
`valkey.username_file`, `valkey.password_file`, `valkey.sentinel.username_file`, `valkey.sentinel.password_file` and `valkey.tls.ca_file`) are rendered by the chart and refused when set; the binary
marks them in its schema (`x-trove-chart-managed`), and so are `server.ops_tls` and `tracing.ca_file`. The schema the chart
validates `config` against is `internal/config/bastion.schema.json`. The role has no `incoming_context` key under `tracing`
(R-BAS-042).

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
    trusted_proxies: []                 # default (proposed); REQUIRED while ingress.enabled: the CIDRs of the ingress controller's pods, example: ["10.244.0.0/16"] (Q-BAS-130)
    forwarded_header: x-forwarded-for   # default (proposed): x-forwarded-for | forwarded
    max_in_flight: 1024                 # default (proposed), per replica
  bastion:
    main:
      url: ""                           # required; example: https://trove.example.com (the main endpoint)
      packages_url: ""                  # default (proposed): empty = url; example: https://trove-packages.trove.svc:8080 (the main installation's packages Service, R-BAS-044)
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
      spool_max_bytes: 2GiB             # default (proposed): the fills in progress of a replica hold at most this in temporary storage; at least max_object_bytes (Q-BAS-131)
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
  valkeySentinel:
    existingSecret: ""                  # default (proposed): no authentication at the Sentinels; example: trove-bastion-valkey-sentinel
    usernameKey: username               # default (proposed): empty = the default user
    passwordKey: password               # default (proposed)
  valkeyCA:
    existingSecret: ""                  # default (proposed): system roots plus trustedCA
    key: ca.crt                         # default (proposed)

trustedCA:
  existingSecret: ""                    # default (proposed); example: corp-ca-bundle; trust for the main installation, Valkey and the bucket
  existingConfigMap: ""                 # default (proposed); example: corp-ca-bundle (trust-manager target)
  keys: [ca-bundle.crt]                 # default (proposed); exactly one key: the bundle is one file (bastion.main.ca_file)

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
  sizeLimit: ""                         # default (proposed): empty = 1Gi with the volume backend; with the bucket backend (whose fills are spooled here) config.bastion.cache.spool_max_bytes plus 256Mi; example: 3Gi

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
  backendTLS:                           # TLS from the Ingress to the pods while tls.enabled (R-BAS-044, Q-BAS-55); HAProxy Ingress keys
    verify: true                        # default (proposed): the controller verifies the pods' certificate against caSecret
    caSecret: ""                        # default (proposed): empty = the listener's Secret (tls.existingSecret or <fullname>-tls), which must carry ca.crt

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
      to: []                            # default (proposed); required with the bucket backend; example: [{ipBlock: {cidr: 203.0.113.0/24}}] (the object store's addresses; a wide range such as 0.0.0.0/0 undoes the egress promise of R-BAS-047)
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

**As built** ([Q-BAS-105](#q-bas-105--the-integration-suites-main-installation-is-the-web-role-behind-a-tls-front-another-interface-version-is-simulated-on-the-wire)):
`test/integration/bastion` starts, per test, the web role of the stack (PostgreSQL, Valkey with Sentinel, S3) with `npm-local`, `npm-remote`
(over the counting test upstream), `npm-all` and the NuGet counterparts, the fixture principals, a registration `partners` with its token,
`server.trusted_proxies` and `server.additional_base_urls`, behind a TLS front that records every request of the Bastion and can cut the link,
delay, hold or alter the interface version of a request or an answer; and one or two Bastion replicas from a configuration file, with the volume
backend or the stack's S3, with and without Valkey. The tests are named `TestR_BAS_0nn_…`; the table of
[developer/bastion.md](../developer/bastion.md#the-integration-suite-r-bas-048) lists them.

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
| Main installation unreachable | NetworkPolicy cut: warm runs succeed for the grace period, a cold package and an unseen token get `503`, everything `503` after `period + grace_period`; `TroveBastionMainUnreachable` fires ([Q-BAS-112](#q-bas-112--the-alert-rows-assert-the-gauges-the-rules-read-held-for-the-rules-for-the-profile-has-no-prometheus)) |
| Token revoked at the main installation | a client loop is refused within 5 s on both replicas |
| Interface mismatch | an upgrade of the main installation to `InterfaceVersion + 1` fires `TroveBastionInterfaceMismatch`; the Bastion's upgrade clears it ([Q-BAS-111](#q-bas-111--the-interface-mismatch-is-exercised-with-a-release-binary-built-under-a-go-overlay), Q-BAS-112) |
| Flooding address | an address sending 2,000 requests a minute is blocked on both replicas; another address is unaffected |
| Failed-authentication timeout | 21 random tokens put the address into the timeout; a known token from it keeps working |
| Anonymous miss flood | anonymous requests for 10,000 random names within a minute send at most the anonymous miss budget and the anonymous share of `max_concurrent` to the main installation |
| Read-only | `npm publish` and `dotnet nuget push` are refused in the client's shape, pointing at the main installation |
| Drain | `kubectl drain` of one Bastion replica's node during a 10-minute client loop: zero failed runs |
| Hidden content | a package hidden by a permission at the main installation is hidden at the Bastion, cached or not, and a narrowed permission hides cached content within the revocation bound ([Q-BAS-114](#q-bas-114--hidden-content-in-the-suite-is-a-package-permission-the-exposure-rules-of-an-endpoint-are-not-the-bastions)) |

- The drain, the flood and the anonymous flood are long tests: they run nightly, on dispatch and locally, not in the release
  path, and a red long leg is a finding, not a blocker (the owner's rule of 2026-11-01). The other scenarios run with every
  change to the Bastion.

Acceptance criteria:

- The e2e job `bastion` runs the short scenarios on every push that changes `internal/bastion`, `internal/bastionmain`,
  `deploy/helm/trove-bastion`, this page or the suite's own files, and is green without running anything for any other change (the
  release's `needs` holds it); the nightly job runs all of them, the long ones in the set `bastion-long`.
- The suite is deployed and measured as [developer/e2e.md](../developer/e2e.md#the-bastion-tests) says, with the decisions
  [Q-BAS-110](#q-bas-110--the-e2e-bastions-are-two-releases-of-the-chart-in-one-namespace-the-bucket-one-with-a-valkey-of-its-own) to
  [Q-BAS-114](#q-bas-114--hidden-content-in-the-suite-is-a-package-permission-the-exposure-rules-of-an-endpoint-are-not-the-bastions) where the profile cannot do
  what a row says. The second flood of the flooding-address row, which yields 30 minutes (R-BAS-037), needs the first block to expire (15 minutes) and is the
  integration tier's.
- The end-to-end run found four defects, each fixed with a test: the Bastion chart had no Sentinel credential (R-BAS-046, `secrets.valkeySentinel`); a replica did not find
  the document its peer kept (R-BAS-018, R-BAS-023: the Vary names are learned per replica); a write probe through a main installation that refuses anonymous requests
  got `401` instead of `403 bastion_read_only` (R-BAS-014, fixed on main by Q-BAS-118); and a partial reader got a `304` it had not asked for (R-BAS-023, Q-BAS-107).

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

### Q-BAS-48 · A registration is a principal row plus a subtype table; the kind and the token type are admitted by one contract step

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the contract work of phase 5b lays the schema the parallel work packages share. `principal.kind` and `api_token.type` are
`CHECK … IN (…)` lists in migration 00007, which is immutable; neither lists `bastion` or `bst`. R-HA-018 classifies every `DROP` of
a constraint as a contract step, and `ParseSteps` refuses it in an expand file.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Expand 00032 (the table `bastion`, `bastion_contact`, the name rule, the token guard), contract 00033 (replace the two checks, `NOT VALID`), expand 00034 (validate) (chosen) | The checks are replaced the one way the rules allow; `requires-app >= 0.1.0` because the step only loosens | Every constraint stays honest: the database refuses a kind or type outside the list; the rules of R-HA-018 and R-ARCH-026 hold unchanged; no release is made unable to serve | A contract step ships in a feature release; the floor of an empty database becomes `0.1.0` instead of `0.0.0` (one test states the rule instead of the constant) |
| (b) Keep the checks, store a registration as a `service_account` with a marker table | No schema change of the existing tables | No contract step | The kind would not be `bastion`; the guards of group membership and role binding would admit it, so "holds no grant, no membership" would rest on the API alone (against Q-BAS-23) |
| (c) Replace the checks inside a `DO` block built from strings | The lint does not see the `DROP` | Stays an expand file | Evades the rule instead of meeting it |

**Choice:** (a).

**Rationale:** a registration is a principal of the kind `bastion` (R-AUTHZ-002, Q-BAS-23) and the database must be able to say so
and refuse the rest: the guards of `authz_role_binding` (00011) and `group_membership` (00007) name the kinds they admit, and
`bastion` is none of them (`AI001`), which makes "never a grant holder" structural. The registration's own columns go to a subtype
table, as a service account's do, with `principal.revision` as its ETag (a trigger bumps it when the description changes). The
last contact of a replica is a transient table pruned by its writer after 30 days; the feed needs no index of its own, because
`(occurred_at, id)` is the primary key of `audit_event` and the lookback window is minutes long (an index on the partitioned
table cannot be built concurrently). The development agent would revisit this if a second principal kind needs the same step: the
checks could then be replaced by a table of kinds.

The token API does not see a Bastion token: `IdentityListTokens` and `IdentityTokenRow` exclude the type `bst`, so `GET /tokens` never
lists one and `GET`, `DELETE` and rotate of `/tokens/{id}` answer `404` for it. A Bastion token belongs to its registration
(`/bastions/{name}/tokens`), and the enums of the token API stay as they are (adding `bst` to a response enum is a breaking change
of the contract, R-ARCH-025).

**Applied in the spec:** R-BAS-004 (the error for a fourth token is the existing `token_limit_reached`, see Q-BAS-53),
R-BAS-010, R-BAS-015; code: `internal/meta/postgres/migrations/00032_bastion.sql` to `00034_bastion_validate.sql`, the query files
`bastion_registration.sql`, `bastion_contact.sql`, `bastion_feed.sql`.

### Q-BAS-49 · The interface is one package of wire types; four details of the contract are fixed in it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-008 to R-BAS-016 give the shapes of the bodies and headers; writing them as types showed four places the page leaves
open.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/bastion/iface` holds every header, class, shape, kind, cursor, body and problem code, with the rules below (chosen) | One import for both sides; the surface guard reads this one package | Both sides cannot drift; the guard of R-BAS-009 is a test over one directory | A change anywhere in it raises the version, also a comment-only change of a constant's value |
| (b) Types on each side, equal by test | Two copies | No shared package | A field added on one side passes until the other side is built |

**Choice:** (a), with these details:

1. **Subject header.** `expires=` is left out for a token that does not expire (a service-account token may); the check answer's
   `expiresAt` is absent then. `Trove-Bastion-Subject` carries `token`, `anonymous` or `invalid` only; `anonymous_refused` and
   `throttled` exist in the check answer alone.
2. **Entry IDs.** An audit event that yields more than one entry (`settings.auth.changed` yields `settings` and, when anonymous access
   was switched off, `grants`) gives the first the event's ID and the others `<event ID>:1`, `<event ID>:2`, so every entry has an ID
   of its own and the same event always yields the same IDs.
3. **Paging with the lookback.** A read is two queries: the entries within the lookback up to and including the cursor (late commits,
   all of them, re-sent for the Bastion to skip) and the entries after it, cut by `limit`. A page therefore always advances. When the
   entries behind the cursor exceed four times `bastions.feed.max_page`, the answer is `reset: true`, never a truncated list.
4. **Strict decoding.** Both sides decode every body with `iface.DecodeStrict`: an unknown member is an error, because equality of the
   versions is the rule (R-BAS-009). The cursor's text form is `base64url(<RFC 3339 time>|<event ID>)`, opaque to the Bastion; the
   synthetic form stays `t:<RFC 3339>`.

**Rationale:** (1) a non-expiring service-account token is real and a made-up expiry would be a lie in a security header; (2) without
it the Bastion's deduplication by ID would drop the second entry of an event; (3) with one query and a limit, a lookback window that
holds more than a page returns the same first page forever, which would stall the revocation feed; (4) a Bastion that ignores a
member it does not know serves under rules it does not understand. The development agent would revisit (3) if journaled events become
so frequent that the safety bound is reached in normal operation (Q-BAS-29's condition).

**Applied in the spec:** R-BAS-009 (the constant is `iface.InterfaceVersion`), R-BAS-013, R-BAS-015; code: `internal/bastion/iface`,
`testdata/interface.txt`, `TestR_BAS_009_TheInterfaceChangesOnlyWithItsVersion`.

### Q-BAS-50 · The role has a configuration tree of its own, chosen by `--role` alone

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A second root struct `BastionConfig` loaded by `LoadBastion`, with its own generated JSON Schema (chosen) | Same loader, tags and units; a key of another section is unknown to the tree | The refusal of R-BAS-001 is the loader's own unknown-key error, naming key and role; the chart validates `config` against exactly the role's schema (R-HA-029) | Two trees and two schema files |
| (b) The main tree with a role check per section | One tree | One file | The role's schema would list every key of the main installation; the refusal is a second mechanism |

**Choice:** (a). Details: the role is chosen by `trove serve --role bastion` and nothing else (`process.role` is not a key of the
tree, so a file that holds it is refused); the one flag the role takes is `--log-level`; a file key or an environment variable of
another section is refused with the key and the role in the message, and a mapping is walked, so `database.host` is named rather than
`database`; the keys the chart renders carry the tag `chart:"managed"`, which the schema exports as `x-trove-chart-managed: true`
(`server.tls`, `server.ops_tls`, `bastion.main.token_file`, `bastion.main.ca_file`, `bastion.signing_key_file`,
`bastion.cache.volume.dir`, `bastion.cache.bucket.ca_file`, the three bucket credential files, `bastion.replicas`, the Valkey
username and password files, `tracing.ca_file`); the role has `server.ops_tls` (R-BAS-044 names the ops listener's TLS); the sections
`health` and `metrics` of the main tree do not exist in the role, the server uses their defaults and a fixed drain delay of 5 s;
`valkey.key_prefix` empty is derived from the host of `server.base_url` and the client sees `trove-bastion:<prefix>:`; the schema file
is `internal/config/bastion.schema.json`, written by the same `go generate` as `schema.json`.

**Rationale:** the refusal in the acceptance of R-BAS-001 comes for free from a loader that does not know the key, and the chart's
schema cannot admit a key the binary refuses. The development agent would revisit the fixed drain delay if a Bastion behind a slower
ingress needs a different one.

**Applied in the spec:** R-BAS-001, R-BAS-002, R-BAS-034, R-BAS-044, R-BAS-046; code: `internal/config/bastion_role.go`.

### Q-BAS-51 · The Bastion's code is a tree of small packages under an allow-list; the read-shape resolver is package 3's

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/bastion/{iface, answers, metrics, mainclient, cache, shared, guard, downloads, engine, server}` under an allow-list rule, and `internal/bastionmain` for the main side (chosen) | One package per parallel work package, an archtest rule that holds the tree to the platform packages | The packages are built in parallel without editing a shared file; R-BAS-001's "no subsystem" is a failing test | Several small packages |
| (b) One package `internal/bastion` | Fewer files | Simple | Eight developers in one package; no rule can say which part imports what |

**Choice:** (a). The allow-list (`internal/archtest/bastion.go`): `errs`, `config`, `buildinfo`, `observability` and its health
package, `tlsfiles`, `outbound`, `httpserver` with its content wrapper; `internal/cluster` for `internal/bastion/shared` only (the
Valkey client and its degradation, R-ARCH-031) and `internal/blob/s3` for `internal/bastion/cache` only (the S3 client for the bucket
backend), because only those two packages may import the Valkey client and the AWS SDK (R-ARCH-002). The main installation's side
lives in `internal/bastionmain`, a domain package, because it needs the decision point and the Bastion's tree may not import it. The
**read-shape resolver** (R-BAS-012) is declared in `internal/bastionmain` and implemented by work package 3: the marking asks it on
the hot path of every forwarded read and the acceptance test `TestR_BAS_012_ShapeAgreesWithTheDecisionPoint` belongs to it; the check
operation of work package 2 consumes it, and tests it against a double until package 3 lands.

**Rationale:** the rule is an allow-list, as R-ARCH-008's is, so a package that did not exist when it was written is refused by
default. The development agent would revisit the entry for `cluster` if its Valkey client is split from the change log, which would
let the Bastion import the client alone.

**Applied in the spec:** R-BAS-001, R-BAS-012; code: `internal/archtest/bastion.go`, `internal/archtest/layers.go`.

### Q-BAS-52 · Recognition and marking are one chain stage before authentication; the dispatcher tells an observer about the route

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A stage `Chain.Bastion` between the client address and the authentication, composed of Recognize (package 2) and Mark (package 3), and a `RouteObserver` in the request context that the dispatcher calls after the match and after the decision (chosen) | The recognition result is a context value; the shared authorization step admits the interface routes on it (`x-trove-permission: {bastion: true}`); the observer refuses write probes in the format's shape and the marking wrapper adds the headers | No format code is involved (R-BAS-013); the token is taken out of `Authorization` before the stage looks at credentials; a nil stage costs nothing | A change to the dispatcher and the chain |
| (b) Marking in the authentication stage | Fewer stages | One place | The stage knows no route; the probe answer needs the format's `WriteError` |
| (c) Each format marks its responses | Format code | None | Against R-BAS-013 and Q-BAS-27 |

**Choice:** (a). The stages exist only on the chain of the default endpoint. The observer sees `RouteMatch` (format, repository key, route
name, method, kind, coarse action) before the decision and `RouteDecision` (subject, index, repository class) after it; the raw file
handler (`/files/`) gets the same two calls from work package 3. The interface routes carry the new declaration form `{bastion: true}`
with `x-trove-bastion-interface: <n>`, and the document refuses a version other than the build's.

**Rationale:** the authentication stage would refuse `trv_bst_` as an unknown token on every route (R-BAS-005), so the recognition has
to run first and to move the validated token out of `Authorization` on an interface route; the marking has to wrap the writer outside
the stage to mark a `401` for a rejected credential. The development agent would revisit the rawfiles call if `/files/` moves under the
dispatcher.

**Applied in the spec:** R-BAS-005, R-BAS-008, R-BAS-013, R-BAS-014; code: `internal/bastionmain`, `internal/httpserver/chain.go`,
`internal/api/dispatch/dispatcher.go`, `internal/api/v1/authorize.go`.

### Q-BAS-53 · Unbuilt operations answer 501 `bastion_not_implemented`; three codes follow the existing registry

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stub handlers answer `501 bastion_not_implemented` when the composition root says so, `503` otherwise; the registry gains the Bastion codes; the existing `token_limit_reached` serves the fourth-token error (chosen) | The operations are in the document and routed; the smoke tests of the document stay green | The build, the contract tests and the generated client types exist before the logic | A flag in `v1.Options` until the last stub leaves |
| (b) Leave the operations out of the document until built | Nothing to stub | Less to remove | The packages would edit the shared document; the interface surface could not be reviewed |
| (c) A new code `too_many_tokens` | Matches the page | The words of the page | A second code for the rule the registry already has |

**Choice:** (a). `principal_not_allowed` is registered (422, R-BAS-004), because the existing `principal_outside_project` names a
project; the codes of R-BAS-029 and `bastion_base_url_unknown` are registered with their kinds; `trove_bastion_interface_requests_total`
and `trove_bastion_forwarded_requests_total` live in `internal/bastionmain`, the Bastion's metrics in `internal/bastion/metrics`,
all defined once.

**Rationale:** a code is never reused or renamed (R-ARCH-025); the page's `too_many_tokens` was never in the registry. The development
agent would revisit this if the owner wants the page's name, which would be a rename of one constant before the first release that
carries it.

**Applied in the spec:** R-BAS-004; code: `internal/errs/codes.go`, `internal/api/v1/bastion_*_api.go`, `internal/app/wire_bastion.go`.

### Q-BAS-54 · The role's process reuses the shell of the others and answers `bastion_not_served` until the engine exists

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. The placeholder engine it names is gone: the engine
([Q-BAS-100](#q-bas-100--one-implementation-of-the-link-the-budget-the-sweep-and-the-cache-use-and-the-role-builds-real-components-only)) is the handler of the client listener.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/bastion/server` builds the listeners, the TLS reloaders, the chain stages 1 to 5 and the ops handler from `internal/httpserver`, `observability` and `tlsfiles`, and hands every path to the engine through `Router.SetFallback` (chosen) | The TLS reload, request log, metrics, client address and shutdown are the ones of the other roles; a placeholder engine refuses | One implementation of each; the skeleton runs and can be probed before the engine exists | The binary links code the role never runs |
| (b) A new `app` area per role | Fits the wiring table | Same place | The areas are PostgreSQL-shaped; the role would need a shell profile of its own |

**Choice:** (a). Startup reads the token file (R-BAS-006) and the signing key before a port is bound; readiness is the start-up check,
the TLS material and the engine's `Ready`, and does not depend on the main installation (Q-BAS-44); the drain is readiness off, the
drain delay, the client listener's shutdown by the deadline, the engine's `Drain`, then the ops listener. `trove admin config validate`
does not know the role (the check is the start of the role, which stops on a defect).

**Rationale:** the specification says the role reuses the TLS reloader, the limiters, the request log and the client address
(Q-BAS-22); a second shell would be a second implementation of each. The server builds its chain itself, an `httpserver.Chain`
without an `Endpoint`: it is the one place besides `internal/app` that the endpoint-construction rule of R-EP-002 lets build one,
because the role has no endpoints, no host set and no surface (`internal/archtest/endpoint.go`). The development agent would revisit the exclusion of `admin
config validate` when the first operator asks for an offline check of a Bastion file.

**Applied in the spec:** R-BAS-001, R-BAS-006, R-BAS-043, R-BAS-044; code: `internal/bastion/server`, `internal/app/bastion.go`,
`cmd/trove/serve.go`.

### Q-BAS-55 · What a render needs, and what the values file left open

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep the acceptance criterion as written, derive the rest | Three required values; the chart invents a certificate source and an egress target | Matches the criterion | An invented egress target is "anywhere", and no certificate source means a listener without a key pair: both silently weaker than the values file says |
| (b) The values file wins: correct the criterion (chosen) | The two values the file marks `required` (`tls.certManager.issuerRef.name`, `networkPolicy.egress.main.to`) join the three; an empty peer list is refused everywhere, because Kubernetes reads it as "anywhere" | Nothing is weakened silently; every refusal names its value | A first render needs five values, not three |
| (c) Default both to permissive values | Egress to `0.0.0.0/0` on 443, a self-signed listener certificate | Three values render | Defeats R-BAS-047 and R-BAS-044 by default |

**Choice:** (b).

**Rationale:** the values file of R-BAS-046 says `required` for both and the acceptance criterion of R-BAS-045 lists three; one
of the two texts had to give, and the file is the one a user reads. The same reasoning refuses an empty `networkPolicy.egress.valkey.to`
while Valkey is configured, `cacheBucket.to` with the bucket backend, `tracing.to` with tracing, `networkPolicy.ingress.https.from`
(the Bastion is reached through its Ingress only), and a `trustedCA.keys` with other than one key (`bastion.main.ca_file` is one file).
Two values were missing for a working install and are added to the values file: `ingress.backendTLS.verify` and `.caSecret`, because
the listener speaks TLS (R-BAS-044) and an Ingress that is not told so sends it plaintext. The chart also renders the bucket
credential files `bastion.cache.bucket.credentials.access_key_id_file` and `secret_access_key_file` (managed keys, named by analogy
with the chart `trove`; they are reconciled with the role's generated schema when it lands). The development agent would
revisit (b) if the Bastion gets an operator that can supply the issuer and the egress target.

**Applied in the spec:** R-BAS-045 (validation list, acceptance criteria), R-BAS-046 (`ingress.backendTLS`, `trustedCA.keys`, managed keys).

### Q-BAS-56 · HAProxy Ingress annotations as verified against the controller of the local profile

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The names of the sketch, unverified | `limit-rps`, `limit-connections`, `limit-whitelist`, `balance-algorithm: uri` "with consistent hashing" | No work | The sketch named a hashing mode no key sets |
| (b) Names verified at the controller's tag, consistent hashing as a snippet (chosen) | The three limit keys as they are; `balance-algorithm: uri` plus `assign-backend-server-id` plus `config-backend: hash-type consistent`; a chart test that fails when the controller pin moves | Every name read in the documentation and the template of the tag the local profile installs; a version change cannot pass unnoticed | A snippet is the only way to consistent hashing, and a controller that forbids snippets drops it (H-434) |
| (c) Leave balancing to the user | No routing annotations | No snippet | R-BAS-033 would not be a chart feature |

**Choice:** (b).

**Rationale:** verified on 2026-11-03 against `jcmoraisjr/haproxy-ingress` at `v0.16.3` (the chart version and image of
`DEV_HAPROXY_INGRESS_CHART_VERSION` and `DEV_HAPROXY_INGRESS_IMAGE`): `keys.md` lists `limit-connections`, `limit-rps` and
`limit-whitelist` as backend-scope keys and documents the prefix `haproxy-ingress.github.io/`; `haproxy.tmpl` renders them as a
stick table keyed on `src`, `http-request deny deny_status 429` on `sc1_conn_cur` and `sc1_conn_rate` (so `limit-rps` is a rate of new
tracked streams per second per address, with no burst) and an `acl wlist_conn src …` for the whitelist (ten CIDRs per line);
`balance-algorithm` is copied verbatim into `balance`; no template line or key sets `hash-type`, so consistent hashing needs the
`config-backend` snippet, which `command-line.md` says is accepted unless `--disable-config-keywords` lists its keyword (the local
profile's `deploy/dev/haproxy-values.yaml` sets no such flag); `assign-backend-server-id` (since v0.13) is documented as what keeps
hash assignments stable when pods change. `secure-backends`, `secure-sni` and `secure-verify-ca-secret` (a Secret with `ca.crt`,
read in the Ingress's namespace) are the same keys the chart `trove` uses. What is not verified is the behaviour of a running
controller (R-BAS-049 does that). The development agent would revisit this when the controller pin moves; the chart test says so.

**Applied in the spec:** the facts table, R-BAS-033, R-BAS-038, R-BAS-045.

### Q-BAS-57 · The rule tests run with a pinned promtool, and the rules are scoped to the release

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `go install` the module at a pin, as the other Go tools | `.bin/promtool-<pin>-<go>` | One mechanism | Refused by Go: the module's `go.mod` carries `replace` directives |
| (b) The release archive at a pin, checked against a SHA-256 per platform (chosen) | `PROMTOOL_VERSION` and four digests in `tools/versions.env`, `make promtool` into `.bin`, `make chart-test` depends on it and hands the path to the test | No Docker, runs on the runners and laptops, tampering fails the digest | A new version is five lines, not one |
| (c) The Prometheus container image | Pinned by digest | Same as the other images | Needs Docker in the chart job and from a Go test |

**Choice:** (b), and the rules are scoped to the release.

**Rationale:** the chart `trove` renders no PrometheusRule yet, so there was no promtool pin or CI job to reuse; this change adds the
first one, in the existing `chart` job (it runs `make chart-test`), and `make versions-check` compares the installed copies with
the pin. The rule tests are a Go test (`TestR_BAS_041_PromtoolRules`) that renders the rule file and runs `promtool check rules` and
`promtool test rules` against `deploy/helm/trove-bastion/tests/rules_test.yaml`, so the result is in `.out/test-chart.json` and the
coverage generator sees the requirement. Two expressions differ from the sketch of R-BAS-041: `TroveBastionMainUnreachable` is
`max(…) == 0` (the sketch's `min(…) == 0` fires when one replica is cut off) and every selector carries the release's `namespace`
and ops `service`. The development agent would revisit the scoping if a Bastion is scraped without the chart's ServiceMonitor, whose
labels it relies on.

**Applied in the spec:** R-BAS-041.

### Q-BAS-58 · The `config` subtree is typed loosely until the role's generated schema is wired in

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Wait for the generated schema | No `values.schema.json` yet | One step | The chart ships unvalidated |
| (b) Everything outside `config` strict now, `config` as the chart `trove` has it (chosen) | `config` is a free-form object with the keys the chart reads typed; the generated role schema replaces that one property when it exists | Same shape as the chart `trove`, one property to swap, the chart is validated today | Until then a typo inside `config` fails in the binary, not at `helm install` |
| (c) Hand-write the role's schema | A copy of the binary's keys in the chart | Strict today | A second source that drifts |

**Choice:** (b).

**Rationale:** R-BAS-046 wants the `config` subtree validated against the schema the binary emits for the role `bastion`; that file
is being produced by another work package, and the chart `trove` has the same open gap for its own schema. The wiring is
described in [docs/developer/helm-chart.md](../developer/helm-chart.md#the-bastion-chart). The development agent would revisit this
when the generated file lands.

**Applied in the spec:** R-BAS-046 (acceptance criteria stay; the mechanism is staged).

**Completed:** the staged mechanism is complete. `go generate ./internal/config` (so `make generate` and `make generate-check`)
writes the generated role schema as the `config` property of `deploy/helm/trove-bastion/values.schema.json` and the managed
subtrees as `deploy/helm/trove-bastion/chart-managed-keys.yaml`, which `templates/_validate.tpl` reads to refuse a managed key with a
message naming it. A typo under `config` now fails `helm template` and `helm lint`. Reconciliation of the chart and the role
schema: `valkey.tls.ca_file` is marked chart-managed (own type `BastionValkeyTLS`); `bastion.cache.bucket.credentials.web_identity_token_file`
is not, because the chart renders no volume for it and the binary requires it with `source: webIdentity`; the chart now renders
`tracing.ca_file` from the trust bundle while tracing is enabled, because the role schema marks it managed.

### Q-BAS-59 · One release job publishes both charts

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Both charts in the existing job `release-helm-gh` (chosen) | Version and appVersion of both `Chart.yaml` set to the release, two packages, one index merge, one push to `gh-pages` | One push, one index, one flow; the chart versions equal the trove version | The two charts always share a version number |
| (b) A second job | Its own push to `gh-pages` | Independent | Two pushes race on one branch with `--force` |
| (c) Independent chart versions | The Bastion chart versions on its own | Chart changes without an image | Against R-ARCH-025's `appVersion` = trove version and the single semantic-release flow |

**Choice:** (a).

**Rationale:** semantic-release sets the version, the workflow `Release Docker & Helm` reads it from the release tag, and nothing is
tagged by hand ([docs/developer/releasing.md](../developer/releasing.md)). A single job keeps the `gh-pages` branch a single writer.
The development agent would revisit (c) if the chart ever has to release without the image.

**Applied in the spec:** R-BAS-045 (released with the chart `trove`).

### Q-BAS-60 · A Bastion registration is refused by every write path with one top-level `422 principal_not_allowed`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-004 says a policy entry, a role binding, an owner record or a membership that names `bastion:<name>` answers `422
principal_not_allowed`. The paths that take a principal reference are the policy writes and the validation of a policy
(`internal/authz/validate.go`), the role bindings (`bindings.go`), the owner records (`ownership.go`), the group members
(`internal/auth/principal/manager.go`) and the project administrators (`internal/project/members.go`). The reference parser knew no
prefix `bastion`, so each of them would have answered `unknown principal`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `authz.ParseRef` parses `bastion:<name>` into the kind `bastion`; each write path refuses that kind with the registry's `422 principal_not_allowed` as the top-level code (chosen) | One helper, `principal.BastionNotAllowed`, one answer everywhere; the filters of the reads (`?principal=bastion:…`) answer an empty list | The code is the one the page names and the registry holds (Q-BAS-53); a caller learns why, not that the principal is unknown | The policy validation (`validate-policy`), which reports findings in a body, answers the error for this one finding |
| (b) The same code as a field error inside `validation_failed`, like `principal_outside_project` | The policy report keeps its shape | Consistent with the neighbouring codes | The page says `422 principal_not_allowed`; the registry kind is `Unprocessable`, which carries no fields |
| (c) Leave the prefix unknown | `unknown_principal` everywhere | No code change | A caller cannot tell a Bastion from a typo; the page's acceptance would not hold |

**Choice:** (a). The database stays the second line: the guards of `authz_role_binding`, `group_membership` and `api_token` name the kinds
they admit (migrations 00007, 00011, 00032), which the integration test shows by writing around the API.

**Rationale:** the answer is the page's and the registry's own wording, and a single helper keeps the five paths from drifting. The
validation of a policy returning the error instead of a finding is accepted because no policy that names a Bastion can ever be
valid. The development agent would revisit (b) if the policy editor needs the finding in the report to point at the entry.

**Applied in the spec:** R-BAS-004; code: `internal/authz/ref.go`, `validate.go`, `bindings.go`, `policy_list.go`, `ownership.go`,
`internal/auth/principal/errors.go`, `manager.go`, `internal/project/members.go`.

### Q-BAS-61 · The registration events carry no journal entry; a Bastion token's events name the registration in their data

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-004 lists `bastion.created`, `bastion.updated`, `bastion.deleted` and `token.created` / `token.revoked` with
`resource_type: bastion`, "registered with their journal flags". The committed OpenAPI text said that a switch to disabled is
journaled for the feed as `principal`, which R-BAS-015's table does not list.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The three `bastion.*` events are transactional without a journal flag; `token.revoked` keeps its flag and carries the entry `{tokenId, reason}` for a Bastion token as for every token; the resource of a Bastion token's event is the token, the registration is in the data (`bastion`, `owner: bastion:<name>`, `tokenId`) (chosen) | Nothing new for the feed's mapper: a `token.revoked` row names its token in `resource_id` as ever | The feed carries what changes for callers (R-BAS-015); the revocation of a Bastion token is journaled as asked | The audit trail lists a Bastion token's events under the token, not under the registration; a filter by registration uses `details.bastion` |
| (b) Journal `bastion.updated` and `bastion.deleted` as `principal` entries | A Bastion would learn that a registration was disabled or deleted | One more entry kind | The Bastion learns that from `401 bastion_token_invalid` on its next call (R-BAS-006); the entries concern no caller the Bastion answers |
| (c) The registration is the resource of the token events | The trail groups by registration | Natural reading of `resource_type: bastion` | The mapper of R-BAS-015 would read the registration's ID as a token ID |

**Choice:** (a). Disabling a registration revokes nothing: its tokens fail as a disabled service account's do (R-AUTH-020) and work
again when it is enabled. Deleting it revokes every token first (reason `admin`), one `token.revoked` with its journal entry each, then
removes the registration, its tokens and its contacts, and records `bastion.deleted` last.

**Rationale:** the feed exists for the callers' credentials; the Bastion's own token is refused where it is presented. The development
agent would revisit (c) if the audit screen needs to list a registration's token events without reading the data.

**Applied in the spec:** R-BAS-004; the OpenAPI text of `updateBastion`; code: `internal/audit/events_bastion.go`,
`events_identity.go`, `internal/auth/token/revoke.go`.

### Q-BAS-62 · The Bastion token authenticator is the token service's, with its cache, gate and change area

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `token.Service.AuthenticateBastion` validates through the replica's `auth_tokens` cache, the freshness gate and the change area `auth`; `bastionmain.Authenticator` maps its verdicts to `ErrTokenInvalid` and notes the use (chosen) | The revocation bound of R-AUTH-023, the single-flight lookup, the rejection log (`token.rejected` once an hour) and the last-used buffer are the ones of every token; a disabled registration and a revocation flush the same cache entries | No second cache to keep correct; `Service.Check` stays closed to the type | The type `bst` is known to the token service, which refuses it on `Check` and accepts it on a method of its own |
| (b) A cache and a lookup of its own in `internal/bastionmain` | Independent of the token service | No change to the token package | A second implementation of the bound, the flights and the invalidation on the hot path of the interface |

**Choice:** (a). The lookup is the contract's query `BastionTokenBySecret`, extended by `rejection_due`. The authenticator was called
without the failed-credential throttle of R-AUTH-038 (the seam had no request); since the review of the main installation's side the seam
carries the request (`bastionmain.AuthRequest`: the throttle of the sending address, asked after a cache miss, and the source that
`token.rejected` names and the last use records), and that gap is closed.

**Rationale:** the bound is a property of the cache, not of the type. The development agent would revisit (b) if the interface gets its
own revocation channel.

**Applied in the spec:** R-BAS-004, R-BAS-005; code: `internal/auth/token/bastion.go`, `internal/bastionmain/authenticator.go`.

### Q-BAS-63 · A Bastion token's lifetime is `ttlSeconds` alone, every violation is `400 invalid_ttl`, the description is optional

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ttlSeconds` from 60 to `auth.tokens.bst.max_ttl`, default `default_ttl`; 0, a negative value, under 60 and above the maximum are `400 invalid_ttl`; no `expiresAt`; a missing description becomes `Bastion <name>` (chosen) | The page's acceptance holds for every violation; the document has `ttlSeconds` only | One code | An absolute expiry cannot be requested; the maximum has its own text in the detail |
| (b) The lifetime rules of the other types (`ttl_exceeds_maximum` above the maximum, `0` for non-expiring where allowed) | One rule set | Reuse | The page names `invalid_ttl`; a non-expiring Bastion token must not exist |

**Choice:** (a). The command takes `--ttl <duration>`, which becomes the same value.

**Rationale:** a Bastion token always expires (R-BAS-004), so the rules of the other types that allow "never" do not apply. The
development agent would revisit the missing `expiresAt` if an operator needs to align the expiry to a date.

**Applied in the spec:** R-BAS-004; code: `internal/auth/token/bastion.go`.

### Q-BAS-64 · Creating registrations is serialized by an advisory lock; a registration's name never changes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The transaction-scoped configuration lock of R-ARCH-017 (`cluster.ConfigBastionRegistrations`, object type 4) before the count of `bastions.max_registrations` (chosen) | Two creates on different replicas cannot both pass the limit | Exact limit, no trigger | Creates are serialized (at most 256 rows, an administrator's action) |
| (b) Count without a lock | Simplest | No lock | The limit can be exceeded by the number of concurrent creates; it bounds a metric label (R-BAS-008) |
| (c) A constraint trigger on `principal` | Enforced in the database | Holds for writes around the API | A new trigger on a hot table for a limit of 16 |

**Choice:** (a). The name is the handle in the API, the audit trail and the metrics label, and the patch accepts it as a read-only
member that is ignored; a rename is a delete and a create with a new token.

**Rationale:** the limit protects the cardinality of the `bastion` label. The development agent would revisit the missing rename if
installations register Bastions by a name that later needs correcting.

**Applied in the spec:** R-BAS-004; code: `internal/meta/postgres/queries/bastion_registration.sql`, `internal/bastionmain/registrations.go`.

### Q-BAS-65 · The recognition admits an interface route on its own proof, throttles tokens it cannot place, and counts the Bastion in the read class

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the Recognize stage runs before the authentication stage (Q-BAS-52). On an interface route the authentication stage would
see no credential and refuse the request whenever the default endpoint admits no anonymous request or the runtime switch is off,
which is the normal state of a main installation that has a Bastion; and the stage runs before the rate limits and before the
failed-credential throttle, so a flood of well-formed `trv_bst_` tokens would reach the token store unthrottled.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The authentication step of the chain admits a recognized interface route as the registration, the recognition applies the failed-credential throttle itself, and the registration is a principal of the kind `bastion` for the rate limits (chosen) | `v1.BastionRouteAuth` wraps the endpoint's stage; `bastionmain.Recognize` takes a throttle of the client address; the general limits classify the registration's requests in the read class under `token:<ID>` | The Bastion is never refused by the anonymous rules; the request log names the registration; a flood of tokens is throttled like any other credential; check and downloads (POSTs) are not charged to the write class | A line in the chain's authentication function and in the limit classifier |
| (b) Declare the interface operations public and let the stage pass them | `{public: true}` plus the recognition as the real gate | No change to the chain | The document would state that the operations are open; the declaration `{bastion: true}` of Q-BAS-52 would mean nothing |
| (c) Leave the interface routes to the anonymous classes by address | The Bastion's egress address is the bucket | No change to the limits | One address for all of a Bastion's traffic, in the class sized for strangers |

**Choice:** (a). The recognition takes a token only from the first line of `Trove-Bastion-Authorization` (a forwarded request) or of
`Authorization` (an interface route) and only when the scheme is Bearer and the value starts with `trv_bst_`; anything else is no
Bastion request and every `Trove-Bastion-*` header is ignored. A recognized request reaches the stages behind it as a clone without
the Bastion token. A token the authenticator refuses counts as a failure of the client address, in a bucket of its own (the limiter of R-AUTH-038, the
prefix `bastion:`, so that the rejected credentials of the authentication stage never refuse a Bastion token and the other way round), and an
address in throttled mode (300 rejected Bastion tokens a minute by default) is refused `429 rate_limited` with `Retry-After` before the
authenticator is asked, a valid token included: an address that sent 300 rejected Bastion tokens in a minute is not a Bastion. The principal of a recognized interface request is the registration
(`httpserver.KindBastion`); it is the bucket `token:<token ID>` of the class `read` of R-API-016 whatever the method.

**Rationale:** the stage that refuses anonymous callers must not refuse the one caller whose proof it has just checked; and the cheapest
way to hold the token store to the strength of every other credential is to count its failures where they are seen. The development
agent would revisit the read class if a Bastion with many cold callers meets the budget (H-438), and the refusal of a valid token from
a throttled address if the Bastion's egress address turns out to be shared with strangers that send rejected tokens (H-440).

**Applied in the spec:** R-BAS-008; code: [recognize.go](../../internal/bastionmain/recognize.go), [bastion_services.go](../../internal/api/v1/bastion_services.go),
[ratelimit.go](../../internal/api/v1/ratelimit.go), [limiter.go](../../internal/auth/limiter.go), [wire_bastion.go](../../internal/app/wire_bastion.go).

**Amended by the review of the main installation's side:** the throttle is asked after the replica's token cache, by the token service
(`AuthenticateBastion` with the request's throttle), as for every other credential: a token the replica holds passes a throttled
address and costs no read of the shared counters, a token it does not hold is refused without a lookup unless its marker says it was valid
recently (`TestR_BAS_008_ABastionTokenAsksTheThrottleOnlyOnACacheMiss`). The address counted is the one that sent the token, before the
Bastion's forwarded headers count ([Q-BAS-116](#q-bas-116--the-bastions-forwarded-headers-count-only-for-a-recognized-request-from-an-address-of-bastionstrusted_proxies)).
H-440 is narrowed accordingly.

### Q-BAS-66 · The check runs the credential through the endpoint's authentication stage

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The handler hands the credential to the authentication stage of the default endpoint in a request of its own and turns the verdict into the subject (chosen) | `GET /api/bastion-check` with `Authorization: Bearer <credential>` and the context of the request, whose client address is the one the body names | Exactly what a direct request is: the token types and bindings of the endpoint, the failed-credential throttle for the address, `token.rejected` with `source_ip = clientAddress`, `token.rejected` for a type the endpoint refuses, the last use of the token with the caller's address; one code path | The request is synthetic, and the stage's path rules (a package-protocol path with a safe method) are assumed |
| (b) The handler calls `token.Service.Check` with a throttle of its own | Reimplements the stage's order | Smaller surface | A second implementation of R-AUTH-023, R-EP-010 and R-AUTH-038 that drifts from the first |
| (c) A method on the stage that takes a credential and an address | Exported entry point of the stage | No synthetic request | A change to `internal/auth` for one caller |

**Choice:** (a). The verdicts become the subject: a principal is `token` (with `tokenId`, `principalId` and the token's expiry,
`httpserver.Credential.ExpiresAt`, absent for a token that does not expire) or `anonymous`; `invalid_token` and `principal_disabled`
are `invalid`; `credentials_required` is `anonymous_refused`; `rate_limited` is `throttled` with `retryAfterSeconds` rounded up; a
failure of the stage's stores is an error of the operation (`503`), never a verdict. A subject that cannot be used (`invalid`,
`anonymous_refused`, `throttled`) carries an empty `repositories` object: the Bastion answers its caller from the kind. For a usable
subject the shapes come from `bastionmain.ShapeResolver` on the index the request begins (`authz.Decider.Begin`, no audit event); a
`none` without a status is completed with `404`, `401` for an anonymous caller, and a status the wire does not carry is replaced the
same way. A body that is not valid is `400` (unknown member, trailing data, a wrong type) or `422` (an address that is none, no or too
many repositories, an empty key, a credential longer than 8 KiB).

**Rationale:** the acceptance of R-BAS-011 is that the check is a direct request's validation; the stage is the only place that
holds it. The development agent would revisit (a) if the stage's rules start to depend on more of the request than its path, method,
credential headers and client address.

**Applied in the spec:** R-BAS-011; code: [bastion_check_api.go](../../internal/api/v1/bastion_check_api.go).

### Q-BAS-67 · A download event that breaks its contract is dropped and counted; the batch is refused only when it is too large

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Drop and count the single event; refuse only a batch above `max_batch` (chosen) | An event without a time, repository or path, or with a SHA-256 that is no digest, and one older than `max_age`, are not recorded and counted | A poison event cannot hold the Bastion's buffer forever; the loss is in the audit trail | The Bastion learns of it from the audit trail and a log line only |
| (b) Refuse the whole batch with `422` | Strict | The Bastion sees its bug at once | The Bastion (work package 7) retries the same batch, which blocks every later event |
| (c) Record whatever arrives | No validation | Simple | An event that the table or the schema of `artifact.downloaded` cannot hold would be dropped by the writer without a trace |

**Choice:** (a). One `bastion.downloads.dropped` event per batch holds `count` (the sum of the reported `dropped`, the events older than
`max_age` and the invalid ones), the three parts as `reported`, `expired` and `invalid`, the period (`since`, when events were reported
lost) and the replica; the batch without a loss writes none. The actor of an `artifact.downloaded` event is the principal the
Bastion reports (kind from the principal table; a principal that is gone is recorded as a user with its ID), or `anonymous`, with the
caller's token ID; `source_ip` is the caller's address and `occurred_at` the reported time, the time of receipt for one in the future.
With `audit.record_downloads` off nothing is recorded, the lost-event count included.

**Rationale:** R-BAS-016 says the loss belongs in the audit trail and an event older than `max_age` is "counted as dropped"; one event
carries both. The development agent would revisit it if the owner wants a metric for the main side as well; that is a line in
`bastionmain/metrics.go` and R-BAS-008.

**Applied in the spec:** R-BAS-016; code: [downloads.go](../../internal/bastionmain/downloads.go), [events_bastion_iface.go](../../internal/audit/events_bastion_iface.go),
[events_files.go](../../internal/audit/events_files.go) (`origin`, `bastion`, `replica`), [event.go](../../internal/audit/event.go) (`Event.At`).

### Q-BAS-68 · The feed advances over every event it consumed, is cut at event boundaries and resolves member names when it is read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The cursor names the last audit event consumed, a page is cut between events, names become IDs at read time, and a target that cannot be read drops every answer (chosen) | Two queries (behind and ahead) over `audit_event`; at most one lookup of the virtual closure per repository and one of member names per page | The cursor never stalls on an event that yields no entry; an event with two entries is never split; no new write path | A read does up to two small extra queries |
| (b) The cursor names the last entry | As the specification reads | Literal | An event that yields no entry (a membership event that only added members) would be re-read forever once it is the last one |
| (c) Store the names resolved at write time | The producers put member IDs in the journal | No lookup at read | Changes the events of the identity area for the Bastion |

**Choice:** (a). The table of R-BAS-015 is `mapEvent` in [feed.go](../../internal/bastionmain/feed.go). Details the table leaves open:
`authz.role.unbound` reads the principal reference of the event (`user:` and `sa:` give a `principal` entry with `principalId`, every other
reference a `grants` entry); `group.members.changed` entries come from the removed members only and the names are looked up in the principal table
(`sa:<name>` for a service account) — members that no longer resolve give `grants`; `user.groups.synced` is a `principal` entry for the
user only when groups were removed; an exposure event names the endpoint, and only the default endpoint's reaches the feed;
`settings.auth.changed` yields `settings` and, when `anonymousAccess` went from on (or unknown) to off, `grants`; a token event whose ID cannot be
read yields `grants`; a withdrawal event without a repository yields no entry and a warning in the log. The entries behind the cursor
are counted as events, `4 × max_page` of them at most (`reset` beyond). A reset answers no entries and the position of now, which is the
newest journaled event within the lookback or, without one, `t:<database clock>`; the handshake's `feed.cursor` is the same position.
`content` entries list the repository first, then every virtual repository that includes it, recursively, in key order.
The ownership events and the project events other than an unassignment are journaled but yield no entry, each with its reason in `noFeedEntry`;
`authz.project.repository_unassigned` yields `grants` ([Q-BAS-120](#q-bas-120--unassigning-a-repository-from-a-project-drops-every-permission-answer-and-every-journaled-event-is-classified),
amended by the review); the vulnerability events join with M4b.

**Rationale:** each choice keeps a Bastion from keeping an answer an event may have narrowed, and from stalling. The development agent
would revisit the count of events behind the cursor if journaled events become so frequent that the bound is reached in normal operation
(Q-BAS-49's condition), and `grants` for an unresolvable member if the identity area records member IDs.

**Applied in the spec:** R-BAS-015, R-BAS-010 (the cursor of the handshake); code: [feed.go](../../internal/bastionmain/feed.go),
[bastion_feed_names.sql](../../internal/meta/postgres/queries/bastion_feed_names.sql), [bastion_feed_api.go](../../internal/api/v1/bastion_feed_api.go).

### Q-BAS-69 · The last contact is remembered per replica, bounded per registration, and its event is asynchronous

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The statement of `BastionContactUpsert` enforces the minute, the replica remembers its own writes, and the rows of a registration are capped at 256 (chosen) | A repeat of unchanged values within a minute costs no statement; a statement that wrote nothing is not remembered; the newest 256 rows stay | The rule holds across replicas of the main installation; the replica name, which the Bastion chooses, cannot grow the table | Up to 256 replicas per registration are shown |
| (b) The memory alone | Smaller | No statement for a repeat | Two replicas of the main installation write twice a minute |
| (c) The statement alone, no cap | As the contract work wrote it | Fewer lines | A holder of the Bastion token creates a row per name until the 30-day prune |

**Choice:** (a). The release is the text after `trove-bastion/` in the `User-Agent` up to the first space, at most 64 characters (empty for
any other agent); the replica is `Trove-Bastion-Replica` (control characters removed, at most 253 characters) or `unknown`; the client address is the
address after the trusted-proxy rule. `bastion.contact` is written (asynchronous, category `system`, the registration as actor) when the row is new or a
reported value (address, release, version, base URL) changed, with these values in `details`. A failure to record the contact is logged and
does not fail the handshake. The contacts not heard for 30 days are pruned by the writer, at most once an hour per replica.

**Rationale:** the handshake is the Bastion's way to learn the main installation's state; the record about the call must not take it down, and
must not be a way to fill a table. The development agent would revisit the cap if one registration legitimately runs more than 256 replicas.

**Applied in the spec:** R-BAS-010; code: [contact.go](../../internal/bastionmain/contact.go), [bastion_contact_cap.sql](../../internal/meta/postgres/queries/bastion_contact_cap.sql),
[bastion_handshake_api.go](../../internal/api/v1/bastion_handshake_api.go).

### Q-BAS-70 · `absent` is derived from the notes of the core's lookups, and only for a full reader

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The remote engine marks the `NotFound` it answers for an upstream 404 or 410 (`errs.WithAbsent`); the host's local lookups and the remote wrappers note into a per-request record whether something was found missing for every subject or something made the 404 mean less; the Mark stage sets `absent` for a `404` of a read route when the shape is `full`, something was missing and nothing was unknown (chosen) | No format code is involved; a refusal, a serve-time check, a pattern refusal, a remote member that could not tell and a package row the lookup holds are all `unknown` or `found` | Exact where the lookups are the core's; a hidden item is never absent because a `full` reader has nothing hidden by grant | The record is filled by the lookups the core makes, not by the routes: a lookup a format makes through its own data port is not seen (H-441) |
| (b) Each format returns a typed error for a missing item | The format says it | Exact per route | Format code in every route of every format; against R-BAS-013 and Q-BAS-27 |
| (c) `absent` for every `404` of a read route | The status alone | Trivial | A hidden item or a blocked version would stand as a negative entry (R-BAS-024) |

**Choice:** (a). The sites: `proxy` marks the upstream 404 and 410 of an artifact and of a document, the negative hit of both, with
`errs.WithAbsent`; `formathost` notes **missing** for a package row or an item that a *local* repository does not hold and for a
remote answer that is marked or is a negative document, and **unknown** for the refusal of `Authorize` (the decision, the
repository's include and exclude patterns, a global claim on a remote name), for a serve-time refusal (`ServeCheck`, a damaged
item) and for any other `NotFound` of the remote engine (offline, not cached, only-if-cached, a gone package); `files` notes
missing for a local path that holds nothing. The record lives in the request's context only while a Bastion request is
recognized (`bastionmain.WithAbsence`), so the calls cost nothing for anyone else. The class is `absent` only for a `full` shape:
a caller who may read everything has nothing the decision point hides from it, and a negative entry is served to full readers
only (Q-BAS-42), so a `partial` or `none` caller's `404` is `pass` whatever the lookups found.

**Rationale:** "missing for every subject" is a fact about the repository, which the lookups know and the routes only relay. The
remote answers are the upstream's, so they are subject-independent; a local package lookup is by repository and name; everything
else that produces a `404` is either decided by subject (the decision point) or by state that can change without the item
changing (a block, a tombstone), which Q-BAS-27 already routes to `pass`. The development agent would revisit the choice if a
format route is found that answers a `404` of its own after an empty lookup (H-441); the contract then gains a typed reason on the
format's side.

**Applied in the spec:** R-BAS-013, R-BAS-024; code: `internal/errs` (`Absent`), `internal/proxy`, `internal/formathost`,
`internal/files`, `internal/bastionmain/absence.go`, `internal/api/bastionmark`.

### Q-BAS-71 · The Mark stage is an edge package and learns the caller from the authentication stage

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/api/bastionmark` (edge) implements `Stages.Mark`; the chain's authentication stage reports the caller it accepted through a generic hook in the context (`httpserver.WithPrincipalNote`) (chosen) | The stage runs before authentication, so it sees the response of every later stage, and reads the principal the way edge code does | Layering holds (`bastionmain` stays a domain package); the subject is right for a `429`, a `404` of no route and a refusal that never reach the observer | A small hook in the chain |
| (b) The marking in `internal/bastionmain` | One package | Matches the contracts page | It would import `internal/httpserver` for the principal and the content, an edge package |
| (c) The observer captures the subject at `Decided` only | No hook | Nothing in the chain | Every earlier answer would have no subject |

**Choice:** (a). The authentication stage calls the note after it accepted a credential or the anonymous caller; a request that
never reached a successful authentication (a rejected credential, a refusal of an earlier stage) has no note and its subject is
`invalid`. The stage maps the principal to the header: a token with its ID, principal ID and expiry (`httpserver.Credential` gains
`ExpiresAt`, filled from the token the validator returned), `anonymous` without a credential, and `invalid` for a credential that
is no token. A `401` is `invalid` whatever the principal was: the Bastion cannot use the credential. Interface routes carry
`Trove-Bastion-Class: pass` and `Trove-Bastion-Interface` and nothing else. The stage counts
`trove_bastion_forwarded_requests_total{bastion, class}` (the class `probe` for a write probe) on the metrics instance of the
composition root.

**Rationale:** the contracts page named the stage and its place in the chain; where it lives was open. The development agent would
revisit (b) if the recognition and the principal become one value in a domain package.

**Applied in the spec:** R-BAS-013, R-BAS-008 (the counters); code: `internal/api/bastionmark`, `internal/httpserver/context.go`,
`internal/httpserver/chain.go`, `internal/auth/stage.go`, `internal/app/wire_bastion.go`.

### Q-BAS-72 · The conformance kit compares documents between callers as check 12

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Check 12: every content case that a `Metadata` or `Computed` route serves is sent to one seeded Host as a user, a service account and the anonymous caller; status, bytes and the validators must be equal; the unbuilt vulnerability check becomes 13 (chosen) | One number, one place; the anonymous caller is included because a shared copy would otherwise reach it | Finds what a Bastion would share wrongly | Renumbers a check that is not built |
| (b) Check 13, leaving 12 for the vulnerability check | No renumbering | Nothing renumbered | A gap in the numbers for as long as M4b is unbuilt |
| (c) Compare two `full` subjects only | The spec's wording | Smaller | `-/ping` answers `401` to anonymous and `200` to a user, and a shared copy would serve the `200` to the anonymous full reader |

**Choice:** (a). The seed is made once, by the fixture's own subject, so only the rendering is compared and not what a publisher
left in a row. The kit builds its three callers from the fixture's Host subject and is exempt from the rule of R-EP-013 that only
`internal/authz` and `internal/api/edge` build an `authz.Subject` (it is test support in non-test files and serves no request). The
check found two routes in npm: `-/whoami` (the caller's name) and `-/ping` (`401` for anonymous) are `Volatile` now, and
`Classify` says `PathVolatile` for both. NuGet passes unchanged. The testformat gains the defect `varies-by-caller`.

**Rationale:** the property is the one a Bastion relies on (R-BAS-013): a `shared` document is the same for every full reader. The
development agent would revisit the number when the vulnerability check is built.

**Amended by the review of the main installation's side:** the callers differ in more than `rq.Subject`. Each arrives with a request
context of its own (a request-log holder naming it, as the chain fills it) and with request headers of its own - `Authorization`,
`Cookie`, `User-Agent`, `Referer`, `X-Forwarded-For`, `Npm-Session`, `Npm-Command`, `X-NuGet-Session-Id` - except those the first answer
names in `Vary`, which every caller sends alike (all of them for `Vary: *`). A document that varies by a header it does not name in `Vary`
fails the check (the defect `varies-by-header` of the testformat; with `Vary: User-Agent` it passes). A format reads no principal from the
context, which R-ARCH-008 keeps out of its imports; the context is varied so that a host or contract code that read it would be compared
as well. npm and NuGet pass unchanged.

**Applied in the spec:** R-BAS-013, R-ARCH-010 (checks 12 and 13); code: `internal/formats/formattest/checks_shared.go`,
`internal/formats/npm/routes.go`, `internal/formats/npm/format.go`, `internal/archtest/subject.go`.

### Q-BAS-73 · The read shape is computed inside the decision point and agrees with it by test

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `authz.Engine.ReachOn` classifies keys as full, partial or none with the refusal a read gets; `bastionmain.NewShapeResolver` maps it to the wire (chosen) | The rules sit next to `readShape`, `populate` and `evaluate`, which they must agree with | The index's private data (members, require-authentication, the populate action) stays private; one oracle test | A new exported method of the engine |
| (b) The resolver in `bastionmain` over `ReadScoper`, `Decider.DecideOn` and the repository reader | No change to authz | Only public calls | It cannot tell `content:metadata-read` from a grant, audits every probe it makes and reads the members from a second source |

**Choice:** (a). A local or remote repository is `full` when the read shape of the grants alone, with `content:metadata-read` left
out, is "all" and, for a remote repository, the populate check passes; `none` when the shape is "none" or the endpoint does not
expose it; `partial` otherwise (an endpoint with exposure rules, a token scope that narrows, a path-only grant, a missing populate
right, the session cookie). A virtual repository is `none` when its gate refuses (the anonymous rule 6 included) or no member is
readable, `full` when the gate admits an artifact read and every member is `full` (a nested virtual repository is a member whose
own gate is asked as a read through the addressed one), `partial` otherwise; the empty virtual repository is `none`. The status
of a `none` is the outcome of a metadata read of the key by the same subject. The oracle test crosses thirteen kinds of subject,
three endpoints, two states of the anonymous switch, ten repositories (local, remote that fills on read and on deploy, one that
requires authentication, virtual, nested virtual, empty, unknown), packages, versions, paths and both request kinds, and asks
`DecideOn` and `MayPopulateOn` for every read.

**Rationale:** "never full where a read is refused" is a property of one function, so it belongs beside the function. The
development agent would revisit the entry if the resolver of a virtual repository asks more of a member than the gate and the
read (the oracle then fails).

**Applied in the spec:** R-BAS-012; code: `internal/authz/reach.go`, `internal/bastionmain/shape_resolver.go`.

### Q-BAS-74 · A write is a route of a writing kind or a coarse action other than read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The observer refuses a request whose method is not `GET` or `HEAD` on a route of kind `Upload`, `Delete`, `Admin`, an unknown kind or an action other than `read`, probe or not; a probe of a read route answers `204` with `Trove-Bastion-Route: read-with-body`; `GET` and `HEAD` pass (chosen) | The route decides, as the spec says; the real request of npm's audit is served | Covers a defect of the Bastion (a write forwarded without the probe) | npm's unimplemented endpoints (`Admin`, action `read`) answer `403` to a Bastion instead of `404`/`501` |
| (b) Refuse on the method alone | Simple | Trivial | The audit `POST` could not be forwarded |
| (c) Refuse only the probe | Narrow | Nothing else changes | A forwarded write would reach the handler |

**Choice:** (a). The refusal is rendered by the format (`WriteError`, R-ARCH-023): npm's body carries `bastion_read_only` and the
detail `This is a read-only Bastion of <server.base_url>; publish to <server.base_url>/api/npm/<key>/` (`/files/<key>/` for the
raw file routes, which call the observer themselves with the kinds `download`, `metadata` for a listing, `upload` and `delete`).
A `GET` on an `Admin` route is not a write and is decided as the caller's own request. The class label of a probe in
`trove_bastion_forwarded_requests_total` is `probe`.

**Rationale:** the Bastion token makes every write `bastion_read_only` (R-BAS-014); the kind and the action are what the format
declared for the route, so no route table is copied. The development agent would revisit the `Admin`-with-`read` case if a format
adds a route of that shape that a Bastion must forward.

**Applied in the spec:** R-BAS-014; code: `internal/api/bastionmark`, `internal/api/rawfiles/handler.go`.

### Q-BAS-75 · The anonymous cap of the budget is the share rounded down, at least one slot

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `floor(slots × anonymous_share)`, at least 1 while the share is above 0 (chosen) | 64 slots and 0.25 give 16 anonymous slots; 4 slots give 1 | Never more than the share, except by the one slot of a tiny budget; anonymous callers are not locked out by rounding | A budget of 1 to 3 slots lets anonymous requests hold a quarter or more |
| (b) `floor` only | 4 slots and 0.25 give 1, 2 slots give 0 | Exactly at most the share | A small budget silently refuses every anonymous miss |
| (c) `ceil` | 5 slots and 0.25 give 2 | Simple | Above the share at most budgets |

**Choice:** (a). A share of 0 gives anonymous callers no slot, which is how an operator says "no anonymous misses".

**Rationale:** the acceptance of R-BAS-025 (8 slots, 2 replicas, anonymous at most 2 in all) holds: 4 slots per replica, 1
anonymous each. The cap is part of the total, so a flood of anonymous misses leaves the rest of the slots to authenticated
callers. The development agent would revisit this if replicas ever run with a budget of fewer than 4 slots in production.

**Applied in the spec:** R-BAS-025; code: `internal/bastion/mainclient/budget.go`, `TestR_BAS_025_TheBudgetIsTheCeilingOfTheShareAndAnonymousIsACapOfIt`.

### Q-BAS-76 · The synthetic feed cursor moves to the send time of an empty read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) After a read without entries, `t:<time the read was sent>` (chosen) | The next read starts there; the main installation's lookback covers late commits | One cheap indexed query per poll; no new member in the interface | Trusts the two clocks (H-445) |
| (b) Keep `t:<start - period - grace - 15 s>` until an entry arrives | The cursor of the first read stays | Nothing to trust | Every poll re-reads the whole history of an answer's lifetime, for as long as no entry arrives |
| (c) Switch to the cursor of the handshake | The main installation's own position | No clock | The handshake's position can lie after the history read: entries in between are covered only if the lookback is long enough, which the Bastion cannot know |

**Choice:** (a). An answer that names an entry replaces the cursor by that entry's, as the interface defines.

**Rationale:** R-BAS-015 returns "the request's cursor when there is none", which for the synthetic form would be re-read in full at
every poll. The lookback of the main installation exists for commits that carry an earlier time than their neighbours; moving
the time to the request is what it is for. The development agent would revisit this if H-445 becomes real, with the main
installation's `serverTime` as the reference for the cursor.

**Applied in the spec:** R-BAS-026; code: `internal/bastion/link`, `TestR_BAS_026_TheFeedStartsAtTheSyntheticCursorAndTheHistoryStartsThere`.

### Q-BAS-77 · The token file is watched by a one-second identity check besides the interval read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every second compare the file's identity (the file behind the link, size, time); read when it differs; read in full every `token_reload_interval` regardless (chosen) | A rotated Secret is used within about a second | No new dependency; works for the kubelet's symbolic-link swap; the interval read catches a change that keeps identity | A stat per second per replica |
| (b) A file-system notification library | Event driven | Immediate | A new dependency; the kubelet swaps a link in the directory, which needs a directory watch and its own care |
| (c) The interval alone | Ten seconds | Simplest | Not "on a change of the file" (R-BAS-006) |

**Choice:** (a).

**Rationale:** the acceptance of R-BAS-006 (70 s from the Secret's value to every replica) is dominated by the kubelet's sync; the
Bastion's share of it is a second. The development agent would revisit this if a platform without stable file identities
appears (the interval read still holds).

**Applied in the spec:** R-BAS-006; code: `internal/bastion/mainclient/reload.go`.

### Q-BAS-78 · A refused batch is dropped, everything else is retried, and a spool file is one drain

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `400`, `413` and `422` for a batch drop it, count it and report the count; everything else keeps the events; a drain writes one file, a batch per line; a file of which some lines were delivered is replaced by the rest; a spool file is claimed through the flight lock (chosen) | The buffer cannot be blocked by one malformed batch; delivery is at least once | No poison batch; no duplicate lines after a partial failure | A replica without Valkey on a shared bucket can deliver a file twice (H-444) |
| (b) Retry every failure | Nothing is dropped | Simple | A batch the main installation will never take stops every later one |
| (c) One file per batch | Atomic per file | No rewrite | A thousand files per drain; a name collision per millisecond in a backend that names files by time |

**Choice:** (a).

**Rationale:** the loss is in the audit trail itself (`bastion.downloads.dropped`), which is what R-BAS-039 asks of a loss. The
development agent would revisit the claim if package 5's backends offer an atomic move, which would replace the flight lock.

**Applied in the spec:** R-BAS-039; H-444; code: `internal/bastion/downloads/reporter.go`.

### Q-BAS-79 · The link is one state machine per replica, run by the server

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/bastion/link` holds the state; the server runs the link, the token reloader and the download buffer; the engine reads `Link.State()` and wraps its client with `Link.Guard` (chosen) | A failed handshake, a refused token or a mismatch begins unreachable; one failed poll does not; the first poll after a failure is followed by a handshake; a replica starts unreachable | The lifecycle of the three loops is in one place, with the drain; the request flow has no state machine of its own | The engine's `Start` no longer starts them |
| (b) The engine runs the loops | Everything in one package | One owner of the request flow | The drain order and the readiness live in the server; two owners of the same loops |
| (c) A handshake after every failed poll | A reconnect handshakes at once | Fast detection | A transient connect error makes the replica unreachable at the next tick, so `unreachable_after` never decides anything |

**Choice:** (a). The client never follows a redirect (the Bastion token travels in a header that a redirect policy does not strip),
reads the main URL's CA bundle again without a restart, and is built by the shared outbound factory with an empty egress
address policy (H-446).

**Rationale:** Q-BAS-44 puts reachability on the handshake and the feed, and R-BAS-043 puts the drain in the server. The development
agent would revisit this if the engine needs to react to a change of state within a request, which a `Watch` on the link already
allows.

**Applied in the spec:** R-BAS-027, R-BAS-028, R-BAS-043; H-446; code: `internal/bastion/link`, `internal/bastion/server`, `internal/bastion/mainclient`.

### Q-BAS-80 · A 404 that answers a plain fetch is not kept as the caller's URL answer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded by [Q-BAS-124](#q-bas-124--a-404-that-is-not-marked-absent-is-never-kept-as-a-url-answer)**: the conditional `404` is no longer kept either.

Context: R-BAS-020 keeps "the stored refusal" of a forwarded or conditional request as a URL answer, so a refused caller costs the main
installation one request per period. R-BAS-024's acceptance says a `partial` reader asking for a missing name twice sends two
requests. A `404` marked `pass` for a `partial` reader may be a missing name or a hidden one; kept, the second request would be
answered from memory.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep every `403` and `404` refusal | The second request for a missing name is answered at the Bastion | Fewest requests | Contradicts the acceptance of R-BAS-024; a flood of distinct missing names is not helped by it anyway |
| (b) Keep a `403`, and a `404` only when it answers a conditional request for content the Bastion holds (chosen) | A refused caller of a cached artifact or document costs one request per period; a missing name is asked every time | Both acceptances hold; the refusal that matters (a cached item the caller may not read, R-BAS-022) is kept | A `partial` reader's misses for plain names reach the main installation, bounded by the per-address miss limit and the budget |
| (c) Keep nothing | Every refusal asks again | Simplest | Against R-BAS-020 and R-BAS-022 |

**Choice:** (b).

**Rationale:** the acceptance criteria of R-BAS-022 (a `404` replayed for a tarball outside `@acme/*`) and R-BAS-024 (two requests for
a missing name) describe different situations: the first has a cache entry and a conditional request, the second has neither. (b) keeps
exactly that difference. The development agent would revisit it if narrowed readers' misses dominate the main installation's load.

**Applied in the spec:** R-BAS-020 (the sentence on kept refusals); code: `engine.onPass`, `TestR_BAS_024_APartialReaderAsksTheMainInstallationEveryTime`,
`TestR_BAS_022_APartialReaderCostsOneConditionalRequestPerTarballAnsweredByA304`.

### Q-BAS-81 · A write is decided by the probe first; its credential is checked only if the real request is forwarded

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-018 lists the credential step before the method step, and R-BAS-019 answers `401 credentials_required` to an anonymous
request while anonymous access is off. NuGet pushes with `X-NuGet-ApiKey` and no `Authorization` (R-BAS-019), and its acceptance is a
`403` from the probe, not a `401`. The real request of a read route that takes a body needs a checked credential.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Credentials first for every method | A push without `Authorization` answers `401` | The order as written | `dotnet nuget push` gets a `401` that asks for a credential the Bastion will never accept for a write; the acceptance of R-BAS-014 fails |
| (b) The probe first for a method other than `GET` and `HEAD`; after a `204` the credential steps run, then the real request is forwarded (chosen) | A write is refused in the protocol's shape whatever it carries | Both acceptances hold; no credential is sent with the probe anyway | The per-address request limit and the miss limit apply, the failed-authentication count does not |
| (c) Credentials first, but a missing credential is not an error for a write | Two rules | — | A malformed `Authorization` on a write is neither counted nor refused |

**Choice:** (b). A body whose length is known is streamed to the real request, at most `max_forwarded_body` (a larger announced length
answers `413` before the probe). A chunked body is held in memory up to the limit and answers `413` above it (H-448).

**Rationale:** the probe carries no credential, so the credential steps protect nothing before it. The development agent would revisit it
if a format's write route needed the caller's identity to be refused in its own shape.

**Applied in the spec:** R-BAS-014, R-BAS-018 (step 5 precedes the credential step for writes); code: `engine.write`, `TestR_BAS_014_ANuGetPushWithoutACredentialIsStillRefusedByTheProbe`.

### Q-BAS-82 · A range is verified by reading the object to its end; a caller's Range or conditional on a miss is answered from the verified spool

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Serve a range without verification | Read only the requested bytes | Cheapest | R-BAS-032's "verified while they are served" does not hold for a range; a replaced object is served on ranges |
| (b) Read the object to its end, write the bytes of the range, withhold the last 64 KiB of the output until the SHA-256 of the whole object matched (chosen) | A range costs one pass over the object | R-BAS-032 holds for every transfer, including the resume of a cut transfer with `Range` and `If-Range` (R-BAS-043) | A small range of a large object reads all of it (H-449) |
| (c) Refuse ranges | Always `200` | Simple | R-BAS-022 asks for `Range` and `If-Range` |

**Choice:** (b). A caller's `Range`, `If-Range` or `If-None-Match` on a miss is not forwarded (R-BAS-021): the whole artifact is fetched,
verified and committed, then the request is answered from the spool with the same rules. Several ranges, another unit and a failed
`If-Range` answer `200` with the whole representation, which RFC 9110 allows.

**Rationale:** correctness of the integrity claim over speed of a rare request; package clients resume whole downloads, they do not seek.
The development agent would revisit it if a client was found that ranges through large cached objects, which would call for a verified
block map in the entry.

**Applied in the spec:** R-BAS-022, R-BAS-032; code: `engine.streamVerified`, `engine.fillArtifact`.

### Q-BAS-83 · The engine decides the host and takes the client address from the chain

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the Bastion's chain (Q-BAS-54) has no endpoint, so the resolver sets no effective host and flags no misdirected request, while R-BAS-018 step 1 asks for `421`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The engine compares the host (chosen) | The request's host, or the right-most forwarded host of the configured family when the peer is in `server.trusted_proxies`, lower-cased, without port and trailing dot, must equal the host of `server.base_url`; an IP literal and an empty host never match | The step is where the spec puts it, no change to the chain | The engine reads `server.trusted_proxies` once more |
| (b) A host set on the chain | Misdirected answered by the chain | Shared code | The chain's endpoint machinery for a role without endpoints (Q-BAS-54) |

**Choice:** (a). The client address is the chain's (`httpserver.InfoFrom`), the TCP peer without a chain; the guard gets
`guard.AddressKey` of it, the main installation gets the address itself.

**Rationale:** a port is not part of R-EP-003's host rule. The development agent would revisit it when the chain grows a host check that
needs no endpoint.

**Applied in the spec:** R-BAS-018 step 1; code: `engine.stepHost`, `TestR_BAS_018_TheHostIsTheHostOfTheBaseURL`.

### Q-BAS-84 · The background refresh renews shape and credential answers; URL answers renew by use

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refresh every answer kind | A conditional request in the background for each URL | Complete | A background request for every cached URL of every caller; the request that triggers it is already answered |
| (b) Refresh the shape (and with it the credential) answer through one collapsed check (chosen) | A `full` reader in steady use never waits for the renewal; the check answers for the repository | One request per credential and repository | A URL answer of a `partial` reader expires at the period and is renewed by its next conditional request |
| (c) No refresh | The first request after the period waits | None | R-BAS-020 offers the refresh |

**Choice:** (b), in the last fifth of the period, only while the main installation is reachable. The goroutine holds the token string for the
duration of that check (H-447).

**Rationale:** the `full` reader is the one who gets the most hits and the one for whom the answer is a single entry per repository.
The development agent would revisit it if `partial` readers' conditional requests show as a burst at each period boundary.

**Applied in the spec:** R-BAS-020; code: `engine.refreshShape`, `TestR_BAS_020_AnAnswerInTheLastFifthOfItsPeriodIsRefreshedInTheBackgroundWithOneCollapsedCheck`.

### Q-BAS-85 · The engine owns the budget, the sweep schedule and the janitor, and sees the link through one interface

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. **Superseded in part by
[Q-BAS-100](#q-bas-100--one-implementation-of-the-link-the-budget-the-sweep-and-the-cache-use-and-the-role-builds-real-components-only):** the budget is
`mainclient.SlotBudget`, the link is `link.Link`, the sweep is `Cache.RunSweeper`; what stays is the janitor and the sums (slots per replica, the two
areas of the cache).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The engine package defines `Link` (reachable, anonymous access, history start, failed, run), `NewBudget` and the loops (chosen) | The link package of work package 4a implements `Link`; `mainclient.Options.Budget` is `engine.NewBudget`; `engine.Start` runs the link, the download buffer, the sweep and the janitor | The interface says "the engine owns the implementation" of the budget; one place starts and stops the loops | The link package must satisfy an interface of the engine |
| (b) Each package starts its own loops | The server wires every goroutine | — | Several shutdown paths (R-BAS-043) |

**Choice:** (a). `engine.New` returns the placeholder while a dependency (including a missing `Link`) is a stub. The budget of a replica is
`ceil(max_concurrent ÷ replicas)` slots, anonymous at most `max(1, floor(slots × anonymous_share))`. The sweep runs every `sweep_interval` ±20 % under the
sweep lease: the documents area is held to `documents_max_bytes`, the artifacts area to `max_bytes − documents_max_bytes`, so documents never evict artifacts
(R-BAS-031). The janitor (every minute) drops answers past period plus grace, the feed log's entries older than period plus grace plus 15 s, and the
registries' expired items.

**Rationale:** the contract page assigned the budget implementation and the loops to the engine; one lifecycle keeps the drain of R-BAS-043 in one place.
The development agent would revisit the janitor's pruning of the feed log if the link package prunes it on its own schedule.

**Applied in the spec:** R-BAS-025, R-BAS-031, R-BAS-043; code: `engine.NewBudget`, `engine.Link`, `engine.Start`.

### Q-BAS-86 · The cache key learns Vary per replica, and results that cannot be shared are not collapsed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A replica learns the headers a target varies by from the responses it saw, per replica (chosen) | The key is the target as received plus the lower-cased values of those headers, in the order of their names; `Vary: *`, `Authorization` and `Cookie` make a response not kept | No format knowledge, no new shared record | A replica that restarted fetches a target once more to learn it (performance only) |
| (b) A shared Vary index in the bucket | Learned once | Fewer fetches | A second record type to sign and sweep |

**Choice:** (a). A fetch whose result is not shareable (a `private` document, a `pass` answer, a refusal, an oversized document) marks its cache key
for one minute; requests for a marked key are not collapsed, so no waiter pays a second round trip for nothing (R-BAS-025). A waiter whose leader
left a shareable result decides again with it and does not wait twice.

**Rationale:** collapsing is for results every caller can use; for the others it only serialises callers. The development agent would revisit (a) if
restarts of replicas with a large document set show as a burst of fetches.

**Applied in the spec:** R-BAS-018, R-BAS-025; code: `engine.computeKey`, `engine.learnVary`, `engine.markVolatile`.

### Q-BAS-87 · The bytes of documents have a tree of their own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `documents/<h0h1>/<h2h3>/<64 hex>` beside `sha256/`, chosen by an area on the call's context (chosen) | Artifacts and documents are two listings; the caller names the area of an entry's kind | The two limits of R-BAS-031 are two listings, with no read of entries; a call without the area is a miss, never a wrong answer | The layout of R-BAS-030 gains a directory; every call that handles a document carries its area |
| (b) One tree; the area read from the entries | The sweep reads every entry to learn which bytes are documents | The layout of the spec stays | One request per entry in every sweep |
| (c) The area in the object's metadata | `x-amz-meta-*` on the object | One tree | A listing does not return metadata; one head per object |
| (d) A new `area` argument on every method of `Backend` | Explicit | No context | Breaks the packages that build against the committed interface in parallel |

**Choice:** (a).

**Rationale:** R-BAS-031 asks that documents never evict artifacts, which needs the two areas measured separately, and it asks for
two listings. (a) is the only option that gives both on a store with five operations. `cache.WithArea(ctx, cache.EntryDocument)`
selects the area; the default is the artifact area, so a caller that forgets it for a document gets a miss. The access markers stay
in one `access/` namespace, one per content address. The development agent would revisit (d) if a third area appeared.

**Applied in the spec:** R-BAS-030 (layout), [docs/developer/bastion.md](../developer/bastion.md).

### Q-BAS-88 · Documents have a reserved share, and entries are removed when read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A reserved share: artifacts up to `max_bytes − documents_max_bytes`, documents up to `documents_max_bytes`; each swept on its own (chosen) | Two limits, 95 % and 85 % of each | Documents never evict artifacts, and the sum never exceeds `max_bytes` | A share the documents do not use is not used by artifacts |
| (b) The total decides | Above 95 % of `max_bytes` delete the oldest of both areas | The whole size is used | A burst of documents evicts artifacts (R-BAS-023) |
| (c) The sweep reads every entry and removes those of evicted bytes | Entries are deleted with their bytes | No orphan entry | One read per entry in every sweep, the cost Q-BAS-43 avoids |

**Choice:** (a), with the removal of entries as R-BAS-031 states it: when read. The sweep deletes bytes and markers; an entry whose
bytes are gone is a miss and is removed by the next lookup (counted `missing`), and an entry that fails its signature is removed and
counted `stale`.

**Rationale:** (a) is R-BAS-023's guarantee read literally; `trove_bastion_cache_capacity_bytes{area}` exports the two limits. The
entries of evicted bytes stay in the bucket until read and are not counted in the size (H-452). The development agent would revisit
(c) when entries become a measurable share of a bucket (about a million).

**Applied in the spec:** R-BAS-031.

### Q-BAS-89 · The bucket backend reaches S3 through a plain adapter and fails fast

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Plain-typed methods in `internal/blob/s3` (`PutPlain`, `OpenPlain`, `StatPlain`, `ListPlain`, `IsNotFound`) and three attempts with 2 s of backoff (chosen) | The cache imports only `internal/blob/s3` (R-BAS-001) and gets the five operations without `internal/blob` types | The allow-list of the archtest stays one package; the client's credential sources, TLS trust and stall detection are reused | A small addition to a package the main installation uses |
| (b) Allow `internal/blob` for the cache | The types are importable | No adapter | Pulls the blob store's PostgreSQL-bound package into the Bastion's tree |
| (c) A second S3 client in the cache | Own SDK use | No change in `s3` | A second place that imports the SDK ("the only package that imports it") |
| (d) The object store's five attempts and 20 s of backoff | Unchanged retry | One setting | A failing bucket holds every request for seconds before it is a miss (R-BAS-030) |

**Choice:** (a).

**Rationale:** the methods add no request: each is the existing method of the same operation in plain types, and the integration
suite counts the requests the client sends. `BastionCacheBucket.StorageS3` maps the Bastion's keys onto the client's configuration.
The retry is not configurable; the development agent would revisit that if a provider needs more attempts.

**Applied in the spec:** R-BAS-030.

### Q-BAS-90 · The cache package offers a fill, a verified read and the first-readiness gate beyond `Backend`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `cache.Cache` around the backend: `NewFill`, `Open` (verified, with the hold-back), `LookupEntry`, `StoreEntry`, `Touch` (once per resolution), `Ready`, `SweepOnce`, `RunSweeper` (chosen) | R-BAS-021, R-BAS-022, R-BAS-031 and R-BAS-032 are applied once | The engine and the download buffer cannot get verification, signing or the throttle wrong; `Backend` is unchanged apart from the area on the context | The engine uses two types |
| (b) The engine implements them over `PutObject(r)` | The fill is spooled by the engine | `Backend` is all | The engine needs the volume's temporary directory; two implementations of verification |
| (c) Verification inside `GetObject` | The backend hashes | One place | A range cannot be verified; the backend does not know the entry |

**Choice:** (a).

**Rationale:** the spool of R-BAS-030 is a directory of the backend (the volume's `tmp`, or the pod's), which only the cache knows;
a fill that is committed by a rename on the volume costs no second write. A range read is not verified (R-STOR-013's carve-out,
H-451). The development agent would revisit it if a third consumer needed the raw backend.

**Applied in the spec:** nothing beyond what R-BAS-021, R-BAS-030 and R-BAS-032 state; the interface is on [docs/developer/bastion.md](../developer/bastion.md).

### Q-BAS-91 · The Bastion's keys are formed by the cluster package without a keyspace, and reuse the fetch lock

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Typed mechanisms in `internal/cluster`, no keyspace segment, the families `block`, `block:level`, `timeout`, `ans`, `sweep`, the fetch lock reused (chosen) | Every key is `trove-bastion:<key_prefix>:<mechanism>:v1:<parts>`; `internal/bastion/shared` calls typed methods and sees no key | Q-ARCH-16 holds (no code outside `cluster` reads or writes a key); the key registry, the TTL rule and the part pattern apply; the fetch lock keeps its extension, its release order and its metric | The names differ from the spec's table by the version segment (`block:v1:…`) and the fetch lock is `lock:fetch:v1:…`, not `flight:…` |
| (b) A second Valkey client in `shared` with its own key forming | `shared` writes `trove-bastion:<prefix>:block:<address>` literally | The names as in the table | A second client and a second set of commands; Q-ARCH-16 and the ACL rule are bypassed; no registry |
| (c) The main installation's keyspace ID | A Bastion reads a keyspace ID like the others | No new option | The Bastion has no PostgreSQL and no `system_state.keyspace_id` |

**Choice:** (a).

**Rationale:** the keys are `<key_prefix><mechanism>:v1:<part>` and the client of the role is made with `WithoutKeyspace`, so the whole
separation between installations is the `trove-bastion:<valkey.key_prefix>:` prefix of R-BAS-034. A part may hold identifiers and
hashes only; an address is written with `:` as `_` and `.` and `/` as `-` (`203.0.113.7` is `203-0-113-7`, the /64 `2001:db8:1:2::/64` is
`2001_db8_1_2__-64`), which is injective for the keys the guard forms and keeps the key readable at the console. The fetch lock is the
`FlightLock` of R-ARCH-033 over the hex SHA-256 of the cache key (`FlightKeyFromHash`). The ACL rule of the Bastion's user is the rule of the
chart `trove` with the key pattern `~trove-bastion:*` and the channel pattern `&trove-bastion:*`, and `test/integration/conf/users.acl` carries
it. A key whose block lasts longer than 24 h is allowed up to the 720 h that `bastion.blocking.max_duration` admits. The development agent
would revisit this if the Bastion ever shares a Valkey database with another installation under a prefix that cannot be told apart.

**Applied in the spec:** R-BAS-034 (the keys as formed); [valkey.md](../developer/valkey.md#the-bastions-keys).

### Q-BAS-92 · The counters are sliding windows, and the timeout starts at the last allowed failure

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Amended by [Q-BAS-127](#q-bas-127--a-refused-request-or-miss-does-not-consume-the-limit-it-was-refused-by): a refused miss or
request is no longer counted ("a refused event counts" below holds for the failures and the refusals that lead to a block only), and the window of `bastion.failed_auth` is held to 1 s to 12 h by
the configuration ([Q-BAS-128](#q-bas-128--the-local-forms-evict-in-constant-time-the-block-decision-locks-per-address-and-the-window-range-is-the-limiters)).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One counting form: a sliding window per key, a refused event counts, a local form with the same estimate scaled by the replicas (chosen) | Misses, failures and refusals are `previous x (1 - elapsed) + current` in Valkey and in memory; a caller compares the count with the configured value; the failure that reaches `max_failures` starts the timeout | The acceptance numbers hold in both forms (at most 30 misses in any minute, the 21st token refused); the memory is two integers per key; with Valkey the form is the one of R-ARCH-034 | A client that keeps sending at ten times its rate is refused until it stops: it gets its burst and nothing more |
| (b) A token bucket for the misses | Burst equal to the minute's budget, refilled per second | Familiar | Admits twice the budget in the first minute: 30 plus 0.5 per second |
| (c) Timestamps in memory | Exact windows | Exact | Memory grows with the rate of a flood |

**Choice:** (a).

**Rationale:** R-BAS-035 states the miss limits per minute and the acceptance criterion bounds them at 30 per minute; a token bucket
that refills at 0.5 per second admits 60 in the first minute. The local counters scale their count by `bastion.replicas`
(`shared.Limiter.Add` returns the count in the units of the configured value), so the guard has one comparison for every form. The request
limits stay a token bucket without Valkey (burst plus rate times the time is the bound the acceptance criterion names: 40 plus 10 per
second is 140 in 10 s) and the sliding window with Valkey. R-BAS-036's acceptance criterion refuses the 21st token with `max_failures`
at 20: the failure that brings the count to `max_failures` starts the timeout. A window is held to 1 s to 12 h, the range of the limiters of
R-ARCH-034, so `bastion.failed_auth.window` above 12 h counts 12 h. The log line's `count` is the number of blocks of the address within 24 h
(the level that sets the duration); the refusals that crossed the threshold are logged as `refusals`. The retry delay of a refused miss is a
quarter of the minute. The development agent would revisit this if clients with a legitimate cold start (a CI job with several hundred new
dependencies) trip the miss limit repeatedly: the first answer is the authenticated class, whose default is 300.

**Applied in the spec:** R-BAS-035, R-BAS-036, R-BAS-037.

### Q-BAS-93 · The store signs and verifies the answer records

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The Valkey form of `shared.AnswerStore` signs on `Put` and verifies on `Get` (chosen) | The engine hands over the record; a record that fails is not returned, is deleted and is counted | One place that trusts Valkey, and it trusts only a signature; the engine cannot forget to verify | The record is signed by the store under `kind:key`, so the engine does not choose the domain |
| (b) The engine signs and verifies | The store is opaque | The engine knows the domain | Every caller must verify; a forgotten check is an unsigned answer in use |

**Choice:** (a).

**Rationale:** R-BAS-034 says that a record failing its signature is ignored and deleted and that nothing Valkey returns is trusted
beyond the signature. The signature covers the domain `answer`, the kind, the key hash and the payload (`cache.Signer`), so a record moved to
another key or kind fails, as does one signed with another key or without a signature. The delete is `DELIFEQ` with the failed value: a
record replaced meanwhile by a good one stays. The signed record is 65 bytes larger than the payload and the 1 KiB limit of R-ARCH-030 applies;
a record that does not fit is not stored (the replica keeps it in its memory). `trove_bastion_answers_total{result="bad_signature"}`
counts the failures for the kinds of that metric. The content is still judged by the validity rules of R-BAS-020. The development agent would
revisit this if the answers of a large `shape` do not fit 1 KiB in practice.

**Applied in the spec:** R-BAS-034.

### Q-BAS-94 · The guard decides, the engine renders and counts; a failure of the shared state admits

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Verdicts with a reason; the engine counts `trove_bastion_refused_total` and renders the problem; the guard counts the blocks; an error of the shared state admits (chosen) | `Verdict` gains `Quiet`; the state gains `Timeouts`; `guard.Stub` is gone | One place counts a refusal; the guard never refuses on its own fault; losing Valkey costs performance, never correctness | A defect in the shared state shows as a missing limit, not as an outage |
| (b) The guard counts every refusal and renders | The guard owns the metric and the answer | Fewer engine lines | Two packages render 429; the engine must still count its own refusals |
| (c) An error of the shared state refuses | Fail closed | Strict | An unavailable Valkey would cut every client off |

**Choice:** (a).

**Rationale:** the engine already counts its own refusals by reason and renders the problem details of R-BAS-029, so the guard returns
what it decided. Two additions to the contract: `Verdict.Quiet` (a blocked address is not logged request by request, R-BAS-037; the
engine keeps such a request out of the request log), and `shared.State.Timeouts`, the failed-authentication timeouts, a list of the kind of
the block list in a family of its own. `trove_bastion_blocked_addresses` is the number of blocks the replica set or served (Valkey has
no command for the size of a family that the ACL user may run), refreshed every 5 s and after a block (H-454). The placeholder `guard.Stub` and
`guard.IsStub` are removed; the role builds real components only (Q-BAS-100). The development agent would revisit the fail-open rule if a
defect of the state were found to hide a missing limit for long: the guard would then count the errors in a metric.

**Applied in the spec:** R-BAS-037; [bastion.md](../developer/bastion.md#shared-state-and-the-guard).

### Q-BAS-95 · The Bastions screen is one page with a card per registration

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A list and a detail page, as the service accounts have | `/admin/bastions` lists name and status, `/admin/bastions/:name` holds the contacts and the tokens | The pattern of the other identity screens; a registration's page can be linked | R-BAS-007 describes the list with the last contact; two pages for at most `bastions.max_registrations` (16) rows; a renewal watches two tokens and every replica, which one page shows at once |
| (b) One page, a card per registration (chosen) | Each card shows the status, the description, the actions, the table of the replicas' last contact and the table of the tokens | The renewal of R-BAS-006 (issue, copy, wait for **Last used**, revoke) happens in one place; the list is one page by the API's own bound, so no paging | A long page at the upper bound |
| (c) A table with a row per registration and expandable rows | Contacts and tokens expand under the row | Compact | Nested tables in a table are hard to use with a keyboard and a screen reader |

**Choice:** (b).

**Rationale:** `GET /api/v1/bastions` answers every registration with its tokens and contacts in one document, at most 16 of
them, and the renewal needs the contacts and the tokens side by side. The change of `enabled` needs the registration's `ETag`
(R-API-012), which the list does not carry, so the card reads the registration for it and then sends the merge patch; it never
sends `If-Match: *`. A token's value stays in the card that issued it and is dropped with the card (R-UI-043). The development
agent would revisit (a) if the bound grows well beyond 16.

**Applied in the spec:** R-BAS-007. Implementation: [web-ui.md](../developer/web-ui.md#the-bastions-screen).

### Q-BAS-96 · A Bastion is a target of the setup page and of the guides, read only, and by link for everyone

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Only the client setup page offers it | The guides link to it | The setup operation already takes `bastion=` | R-BAS-007 says the guide of each format offers it |
| (b) Both pages offer it; a guide for a Bastion shows the Bastion's address and the configure and install snippets of the setup operation, not the format's text (chosen) | A fieldset **Install from** with **This installation** and each registration that has reported a base URL | No format knowledge in the UI; no guide text that names the main installation's address next to the Bastion's; the full guide stays one click away | The guide for a Bastion is shorter than the guide of the installation |
| (c) Render the format's guide for a Bastion | `bastion=` on the two guide operations | The full text for the Bastion | Changes two operations of the API and every format's guide template; most sections (publishing, tags, unpublishing) do not hold through a read-only cache |

**Choice:** (b).

**Rationale:** the choice is built from `GET /api/v1/bastions`, which needs `endpoints:read` (R-BAS-004), so a caller without it
is not offered a Bastion; a Bastion named in the query (`?bastion=partners`) works for every caller, because the setup operation
needs only `read` on the repository, so an administrator can hand a link to a developer ([H-457](#h-457--a-reader-without-endpointsread-is-not-offered-the-bastion-as-a-target)).
The page numbers its steps again when the target step is there. The development agent would revisit (c) when a format's guide
gains Bastion-specific text.

**Applied in the spec:** R-BAS-007, R-UI-049 (through R-BAS-007). Implementation: [web-ui.md](../developer/web-ui.md#client-setup-and-guides-for-a-bastion).

### Q-BAS-97 · The reported address is matched against the listed addresses, and the setup needs no base URL of its own

Status: Decided by the development agent — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Exact string comparison | The reported value must equal an entry byte for byte | Simplest | A trailing slash or the case of the host in a configured entry makes a working Bastion answer 409 |
| (b) Comparison of the origin, ignoring a trailing slash and the case (chosen) | `strings.EqualFold` over both values without a trailing `/`; the snippets use the listed entry, not the reported value | A harmless spelling difference does not hide the instructions; only a listed address ever reaches a snippet | Two spellings of one origin are the same entry |
| (c) Also accept the base URLs of the further endpoints | Every configured base URL | More addresses | R-BAS-017 and R-BAS-010 name `server.additional_base_urls`; an endpoint is another listener, not a Bastion |

**Choice:** (b).

**Rationale:** the answers are `404` for a name that is no registration (the name rule of R-BAS-004 is checked first, so a malformed
name answers the same), `409 bastion_base_url_unknown` for a registration that has not reported an address and for one whose address
is not listed, and `503` on a replica that cannot read the registrations. The address does not come from `server.base_url`, so the
operation does not need the endpoint's base URL: it answers a Bastion's snippets even where the direct operation answers 503. The
OpenAPI document lists `409` on the operation. Whether a name is a registration is visible to every caller who may read the
repository ([H-458](#h-458--the-address-a-registration-reports-chooses-among-the-listed-addresses-and-the-name-is-testable)). The
development agent would revisit (c) when a Bastion needs an address of an endpoint.

**Applied in the spec:** R-BAS-007. Implementation: `bastionBase` in [packages_api.go](../../internal/api/v1/packages_api.go).

### Q-BAS-98 · The cache takes no signer where the configuration allows no key, and keeps plain entries

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-032 requires the signing key with the bucket backend or Valkey, and the configuration accepts a missing key with the volume backend and no
Valkey. `cache.New` required a signer, so the role could not start in the one configuration that the validation allows.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `cache.New` accepts a nil signer; `SignEntry` and `VerifyEntry` of a nil signer write and read the plain JSON (chosen) | The volume keeps unsigned entries; the bytes are still verified against their content address | The configuration stays as specified; the cache survives a restart; one code path | Whoever can write the volume can point an entry at other bytes of the volume (H-460) |
| (b) A key per process, generated at start | Entries are signed | Looks uniform | Every restart makes every entry unverifiable: the volume's persistence is worth nothing |
| (c) A key generated into the volume | Entries are signed | Persists | The key lies next to the data it protects: no protection |
| (d) Require the key always | The validation changes | One mode | The chart and the configuration contract change for a case in which the writer of the volume is the pod itself |

**Choice:** (a).

**Rationale:** with the volume backend and no Valkey the only writers of the entries are the replica and whoever has the volume, and that writer can already delete
the cache or fill it with objects under any content address it can compute; the digest check at every read still stops a replaced object. The signature
exists for the shared stores (the bucket, Valkey), where the key separates the replicas from a writer of the store (H-387). The development agent would
revisit this if the volume is ever shared by several pods or by a sidecar.

**Applied in the spec:** R-BAS-032, H-460; code: `internal/bastion/cache/sign.go`, `internal/bastion/cache/store.go`, `TestR_BAS_032_WithoutASigningKey…`.

### Q-BAS-99 · A replica that has not handshaken answers an anonymous request 503

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-019 admits an anonymous request exactly when the handshake said `anonymousAccess: true`. Before the first successful handshake of a process (a start
while the main installation is down) the setting is not known.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `503 main_unreachable` until the first handshake (chosen) | Neither admitted nor refused as a credential problem | Honest: the Bastion does not know; the client retries, as for an unseen token | A warm cache serves no anonymous request until the main installation answered once |
| (b) `401 credentials_required` | Unknown means off | Safe | A wrong statement to a client: anonymous access may be on; clients stop retrying |
| (c) Admit from the cache | Unknown means on | Availability | Serves content the main installation may have closed to anonymous readers |

**Choice:** (a). The same holds for an unseen token, and for every answer, because `HistoryStart` is zero before the first feed read (R-BAS-026).

**Rationale:** a start during an outage is the case R-BAS-043 accepts (ready, `503`, serves after the main installation is back); the three options differ only
for anonymous readers, and only (a) neither misleads nor widens access. The development agent would revisit this if the handshake's last result were persisted.

**Applied in the spec:** R-BAS-019; code: `engine.stepCredentials`, `TestR_BAS_019_AnAnonymousRequestBeforeTheFirstHandshakeIs503`.

### Q-BAS-100 · One implementation of the link, the budget, the sweep and the cache use, and the role builds real components only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the engine (package 4b) and the link, the client and the server (package 4a) were built in parallel against the contracts, and each wrote what the other was
to provide: an `engine.Link`, an `engine.NewBudget`, a sweep, a fill with verification and signing over the raw `cache.Backend`, and loops that the engine started. Package 5
(the cache) and package 6 (the shared state and the guard) were merged meanwhile and implement the same things.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `link.Link`, `mainclient.SlotBudget`, `Cache.RunSweeper`, `cache.Cache` and the Valkey `AnswerStore` stay; the engine's copies go (chosen) | The engine holds a `*link.Link` (`State`, `AnswerState`, `Watch`, `SetSink`), a client wrapped by `Link.Guard`, and a `*cache.Cache`; the stubs and `Deps.Stubbed` go | One implementation of each thing, each tested where it lives; the engine keeps only what is its own: the request flow, the withheld tail towards the client, the janitor | The engine's tests move onto the real link and a real cache on a temporary volume |
| (b) The engine's interfaces stay and the others adapt | Adapters between the two sets | Few changes in the engine | Two implementations of the budget and the sweep run side by side |

**Choice:** (a). `Failed(err)` of the engine's link interface is `Link.Guard`: a refused token and an interface mismatch in any answer make the replica unreachable at
once, and no other error of a forward does (R-BAS-027, Q-BAS-44). The slot of a request is held until the body ends or is closed, so the engine closes every body
on every path. The sweep keeps the limits of Q-BAS-88 and the lease of Q-BAS-90.

**Rationale:** the contract page assigned each thing to one package; the duplicates arose only because the packages could not see each other. Q-BAS-85 (the engine owns the
budget and the sweep) is superseded where it says so; Q-BAS-82's "verified spool" is the committed fill. The development agent would revisit this if the cache's `Open`
cannot serve a ranged read cheaply enough (H-449).

**Applied in the spec:** R-BAS-021, R-BAS-025, R-BAS-027, R-BAS-031, R-BAS-043; code: `internal/bastion/engine`, `internal/bastion/server`,
`TestR_BAS_025_EveryPathClosesTheResponseBodyAndSoReleasesItsSlot`.

### Q-BAS-101 · The server runs the cache sweeper, and the drain stops it before the engine

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Cache.RunSweeper` is started by the server with `shared.State` as its lease source, and stopped with the flush loop, before `Engine.Drain` (chosen) | The sweep runs for the life of the process; the sweep lease is the shared state's | The cache owns its schedule (R-BAS-031: ±20 % for a bucket); the engine does not need to know the sweep | The sweep and the janitor are two loops |
| (b) The engine starts it | One `Start` | — | The engine would own a loop of another package (Q-BAS-79's rule) |

**Choice:** (a). Readiness has one cache check, `cache`: the cache's first `Check` (`Cache.Ready`), through `Engine.Ready`.

**Rationale:** the sweeper reads and deletes only the cache; no request waits for it, so it stops as soon as the listener stopped. The development agent would revisit
this if a sweep ever has to wait for the last request.

**Applied in the spec:** R-BAS-031, R-BAS-043; code: `server.runLink`, `server.Shutdown`, `TestR_BAS_031_TheServerRunsTheCacheSweeper`.

### Q-BAS-102 · A quiet verdict reaches the request log through a slot in the request context

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `guard.Verdict.Quiet` says a request is not to be logged one by one (R-BAS-037): a blocked address answers many requests and the guard logs one line per block.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The server puts a slot into the request context before the request log; the engine sets it; the logger of the request log skips a record whose context carries a set slot (chosen) | `engine.WithQuietSlot`, `engine.Quiet` | Nothing outside the Bastion changes; the request is still counted in the metrics | A wrapper around the log handler |
| (b) `observability.RequestLog` gains a flag in `RequestInfo` | A shared package changes | Cleaner for every role | Outside the Bastion's packages; the other roles would carry a field they never set |
| (c) Log every request | — | Simple | A flood from a blocked address fills the log: the thing R-BAS-037 forbids |

**Choice:** (a).

**Rationale:** the log record is written after the response with the request's context, so a slot put in before the log is seen by it. The development agent would revisit this if
`observability` offers a skip for a request.

**Applied in the spec:** R-BAS-037; code: `engine/quiet.go`, `server.requestLog`, `TestR_BAS_037_ARequestFromABlockedAddressIsCountedButNotLoggedOneByOne`.

### Q-BAS-103 · A fill can be kept readable after its commit, and a cache can be given a fault for a test

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: a request with `Range` or a conditional header on a miss is answered from the fill after it was committed (Q-BAS-82). R-BAS-030 says a failing backend never fails a request.
`Fill.Commit` removed the spool file, so a backend that refused the commit left nothing to answer from. The tests of the engine and the server also need a backend that is down at the
first check or refuses writes.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Fill.Keep` leaves the spool readable after `Commit` (`Fill.Reader`), `Fill.Abort` releases it; `cache.Options.Fault` wraps the storage with a switch (chosen) | A refused commit still answers the request; tests inject failures through the real code path | No failed request for a failing backend; no second storage in the tests | Two small additions to package 5's API |
| (b) Answer `502` when the commit is refused | Nothing to add | Simple | A cache failure fails a request |
| (c) Test doubles of `cache.Backend` | The engine's tests replace the cache | No production seam | The engine uses `*cache.Cache`, not the interface; the double would reimplement the cache |

**Choice:** (a). `Fault` is nil in production and the storage is not wrapped.

**Rationale:** both additions are narrow and tested in `internal/bastion/cache`. The development agent would revisit `Fault` if the bucket's own transport hook (`Options.WrapTransport`) were enough for
every test.

**Applied in the spec:** R-BAS-030; code: `cache/fill.go`, `cache/fault.go`, `TestR_BAS_021_AKeptFillCanStillAnswerAfterTheCommitWasRefused`.

### Q-BAS-104 · The whole-role scenario runs on one clock in `roletest`, started from a configuration file

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-048 asks for the role against an in-process main installation, with both cache backends.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One scenario in `internal/bastion/roletest`, run by a test in that package (volume) and by one in `test/integration/bastion` (bucket); one settable clock serves the replica (through `server.Options.Deps`) and `maintest` (chosen) | The periods (30 s, 1 m) are crossed without waiting; the timers of the link run in real time | The same assertions for both backends; no wait of minutes | The clock is injected through a test hook of the server |
| (b) Real time with the shortest periods | No hook | Nothing injected | The grace period is at least 30 s of waiting per run, and a loaded runner makes it flaky |
| (c) Two scenarios | Each backend its own | Free to differ | The backends would drift apart |

**Choice:** (a). The revocation bound of 5 s is measured in real time: the poll interval is the configuration's minimum, 1 s.

**Rationale:** the scenario is the Go-side acceptance of R-BAS-043 and R-BAS-048 for the volume and the bucket. The development agent would revisit the single clock if the link's timers
themselves become the subject of a test.

**Applied in the spec:** R-BAS-043, R-BAS-048; code: `internal/bastion/roletest`, `test/integration/bastion`.

### Q-BAS-110 · The e2e Bastions are two releases of the chart in one namespace, the bucket one with a Valkey of its own

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-049 installs the chart `trove-bastion` "once with the volume backend and once with a SeaweedFS bucket and Valkey" next to the dev profile, and leaves open whether
that Valkey is the profile's own, a database or prefix of it, or a separate one.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two releases, `bastion-volume` and `bastion-bucket`, in the namespace `trove-bastion`; the bucket one uses a Valkey resource of that namespace (`bastion-valkey`: two data pods, three Sentinels, TLS) (chosen) | The DMZ shape of the design: nothing the Bastion uses is in the main installation's namespace except the bucket and the main installation itself | The NetworkPolicy of R-BAS-047 is exercised with the Valkey, the bucket and the main installation as three different egress targets; the Valkey cannot be reached from the main installation's pods; both backends run side by side, so a scenario runs against both | Five more small pods (about 160m CPU and 400 MiB requested); a copy of the dev CA's Secret in the namespace |
| (b) The profile's Valkey with a key prefix or a database of its own | One Valkey less | Cheaper | A Bastion would be allowed to reach the main installation's Valkey, which R-BAS-047 does not allow in production; the profile's NetworkPolicy of that Valkey admits only the main installation's pods |
| (c) One release, reinstalled with each backend in turn | Half the pods at a time | Cheapest | Doubles the run, and the cache of the first backend is gone when the second runs |

**Choice:** (a). The periods of the e2e values (`deploy/dev/bastion-chart-common.yaml`) are short so that the unreachable and revalidation scenarios end in minutes:
`answers.period` 30 s, `answers.grace_period` 90 s, `metadata_revalidate_after` and `artifact_revalidate_after` 90 s, `feed.poll_interval` 1 s; the chart's defaults stay in
R-BAS-046. The volume release routes by path (R-BAS-033), because with two replicas and no routing a second run would meet the replica that does not hold a tarball. Both releases
share the main installation of the `bastion` profile (`deploy/dev/values-bastion.yaml`: the pod range as `bastions.trusted_proxies`, both base URLs in
`server.additional_base_urls`) and are registered as `partners` (volume) and `partners-bucket` (bucket).

**Rationale:** the scenarios that need shared state (the flood blocked on both replicas, the token refused on both, the warm run after the cut) can only be shown with the bucket
and Valkey; the scenarios that show the cache as a volume only with the volume. The development agent would revisit the choice if a runner cannot hold the pods.

**Applied in the spec:** R-BAS-049 (the deployment), R-BAS-046 (`secrets.valkeySentinel`, found when the Sentinels of the profile's Valkey required a password); code:
`deploy/dev/bastion.sh`, `bastion.yaml`, `bastion-chart-*.yaml`, `values-bastion.yaml`.

### Q-BAS-111 · The interface mismatch is exercised with a release binary built under a Go overlay

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-028 and R-BAS-049 upgrade the main installation to `InterfaceVersion + 1`. The version is a constant (`iface.InterfaceVersion`), so no linker flag changes it, and the
checkout must not change for a test.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `go build -overlay` replaces `internal/bastion/iface/version.go` and the embedded `internal/api/v1/openapi.yaml` by copies that say 2; the binary goes into the release image as `guidedtraffic/trove:iface` (chosen) | Nothing in the tree changes; the real release code runs on both sides at versions 1 and 2 | No seam in production code; the test fails with a clear message when the line it replaces moves | The build takes a minute on a cold cache; the embedded OpenAPI document names the version too and the main installation refuses to start when it differs, so the overlay replaces that copy as well |
| (b) A build tag or an environment variable that raises the version | A seam in the binary | Faster | A way to run a Bastion at a wrong version in production |
| (c) The integration tier only | No cluster | Cheap | The rolling upgrade of the main installation and the Bastion's upgrade are not shown |

**Choice:** (a). The scenario upgrades the main installation to the `iface` image (`up.sh` with `TROVE_HELM_EXTRA`), waits for both replicas to report `trove_bastion_interface_mismatch 1`
with the main installation's version 2, serves warm content, upgrades the Bastion to the same image, and waits for the mismatch to clear; its cleanup returns both to `dev`.

**Rationale:** the property is that two real builds refuse each other and recover when both move. The development agent would revisit the choice if the version becomes a variable.

**Applied in the spec:** R-BAS-028, R-BAS-049; code: `test/e2e/bastion_upgrade_test.go`.

### Q-BAS-112 · The alert rows assert the gauges the rules read, held for the rule's `for`; the profile has no Prometheus

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-027, R-BAS-028, R-BAS-041 and R-BAS-049 ask that `TroveBastionMainUnreachable` and `TroveBastionInterfaceMismatch` fire "in the cluster's Prometheus". The local profile
installs no Prometheus, no ServiceMonitor CRD and no PrometheusRule CRD (checked: `kubectl get crd`).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The suite reads the gauges of every replica and requires the rule's condition to hold for the rule's `for` (2 minutes of `trove_bastion_main_reachable 0`; 1 minute of `trove_bastion_interface_mismatch 1` with the two versions); the rules themselves are covered by `promtool test rules` (R-BAS-041) (chosen) | The inputs of the alert are proven on the cluster, the expression and the delay in the rule tests | No new component; nothing to pin | The step from the gauge to a fired alert is not exercised on a cluster |
| (b) Install a Prometheus and the Prometheus Operator CRDs into the profile | The alert fires for real | Complete | Two more pinned components, memory and time on every run of the profile, for two rows |
| (c) Leave the rows out | — | — | A hole in the table |

**Choice:** (a), recorded as not covered in [developer/e2e.md](../developer/e2e.md#the-bastion-tests).

**Rationale:** the rules read gauges and `for` only; a rule that is correct under `promtool` and gauges that are right on the cluster leave only the scrape and the delivery, which are
Prometheus's. The development agent would revisit the choice when the profile gets a Prometheus for another reason.

**Applied in the spec:** R-BAS-027, R-BAS-028, R-BAS-041, R-BAS-049 (acceptance wording); code: `test/e2e/bastion_unreachable_test.go`, `bastion_upgrade_test.go`.

### Q-BAS-113 · What the main installation was asked is counted from its request log, by the address of the Bastion's pods

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the rows "cold cache", "warm cache" and "anonymous miss flood" count requests at the main installation, "e.g. its request log or metrics".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The request log of the `web` replicas: a record is a request of a Bastion when its `peer_addr` is the address of a Bastion pod; the interface (`/api/v1/bastion/…`) is set aside; tarballs and `.nupkg` files are artifacts, every other path of the two test repositories a document (chosen) | Independent of the Bastion's own counters; shows the status and the bytes of each request (a `304` with 0 bytes is a conditional request that transferred no artifact) | The path, the status and the bytes per request | Needs the request log on (the dev profile has it) and the addresses of the pods, which the harness reads |
| (b) The metrics of the main installation | `trove_http_requests_total` per handler and code | No log parsing | No peer: a client's own request is not told from a Bastion's; no bytes |
| (c) The Bastion's `trove_bastion_main_requests_total` | Simple | The Bastion's view only, which is what is under test |

**Choice:** (a); the Bastion's counters are used as a second witness where they say something else (hits per replica, the refusals per reason).

**Rationale:** a count made by the system under test proves nothing about the other side. The development agent would revisit the choice if the request log gets a field for the
Bastion's registration.

**Applied in the spec:** R-BAS-049 (acceptance); code: `test/e2e/bastion_env_test.go` (`mainLog`, `fromBastion`).

### Q-BAS-114 · Hidden content in the suite is a package permission; the exposure rules of an endpoint are not the Bastion's

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the last row of R-BAS-049 says "a version hidden by an exposure rule or a permission". The exposure rules (R-EP-012 to R-EP-017) decide what an endpoint with `exposure: rules`
serves; the Bastion connects to the default endpoint (Q-BAS-4), which serves everything the caller may read.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The scenario hides a package by a permission (`$PART` reads `bas<run>-open*` and nothing else; the policy is narrowed during the scenario), cached at the Bastion or not (chosen) | The decision point of the main installation hides it; the Bastion must follow, also within the revocation bound after a change | The mechanism the Bastion relies on (R-BAS-012, R-BAS-022) | The exposure rule is not exercised |
| (b) A Bastion in front of the endpoint `customers` | Exposure rules apply | Both mechanisms | Contradicts Q-BAS-4: the Bastion connects to the default endpoint |
| (c) A rule on the default endpoint | — | — | The default endpoint is `exposure: all` (R-EP-002): its rules decide nothing |

**Choice:** (a).

**Rationale:** exposure is a property of an endpoint's listener, which no Bastion shares. The development agent would revisit the choice if a Bastion may be given another endpoint.

**Applied in the spec:** R-BAS-049 (the row names the permission); code: `test/e2e/bastion_scenarios_test.go` (`TestBastion_R_BAS_049_HiddenContent`).

### Q-BAS-105 · The integration suite's main installation is the web role behind a TLS front; another interface version is simulated on the wire

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: R-BAS-048 asks for the web role as the main installation, and R-BAS-009 for "a main installation built with `InterfaceVersion = 2`". The Bastion talks to the
main endpoint over TLS only (R-BAS-044), the stack's replicas listen in plain HTTP, and `InterfaceVersion` is a constant of `internal/bastion/iface`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A TLS front in the test process, in front of the web replica; it records the Bastion's requests and rewrites the version of a request or of an answer (chosen) | The replica is the real one; the front is the ingress of a deployment; a cut link, a delay and a hold are one switch each | No change to the code under test; the request count is exact; the real main installation's refusal of a foreign version is exercised | A "main installation built with another version" is not literally built |
| (b) Turn `InterfaceVersion` into a variable the test sets | The real main installation answers with version 2 | Literal | Changes `internal/bastion/iface`, whose surface is guarded; a variable invites production use |
| (c) The replica serves TLS itself and a counting wrapper sits in its handler | No front | One hop less | The cut and the answer rewrite need a front anyway; the request log is not a count |

**Choice:** (a). In one direction the front raises the version the Bastion's request carries, and the real main installation refuses it with its real `400
bastion_interface_mismatch`; in the other it raises the version its answers claim, and the Bastion's own check refuses them.

**Rationale:** the two directions are the two code paths of the mismatch (R-BAS-009, R-BAS-028); neither needs the constant to move. The development agent would revisit
(b) if the interface gets a version negotiation. The hop from the front to the replica is plain HTTP (H-461); a main installation that itself speaks a higher version in its
problem body is not exercised (H-462).

**Applied in the spec:** R-BAS-048; code: `test/integration/bastion/front_test.go`, `outage_test.go`.

### Q-BAS-106 · The main installation's interface version is the series with side main of `trove_bastion_interface_version`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the acceptance criterion of R-BAS-009 named the metric `trove_bastion_main_interface_version`, R-BAS-028 and the table of R-BAS-040 name `trove_bastion_interface_version{side}`.
The metrics test compares the code with the table of R-BAS-040.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The criterion of R-BAS-009 names `trove_bastion_interface_version{side="main"}` (chosen) | One metric, two series | The code, R-BAS-028 and R-BAS-040 agree; no new metric | A text edit |
| (b) A second metric `trove_bastion_main_interface_version` | The criterion stays | The criterion is literal | A second name for one fact; alert rules would have to read both |

**Choice:** (a).

**Rationale:** the criterion was the only place with the other name. The development agent would revisit it if alert rules ask for a metric without a label.

**Applied in the spec:** R-BAS-009; code: `TestR_BAS_009_R_BAS_028_InterfaceMismatch`.

### Q-BAS-107 · A 304 for a private document is answered with the shared entry's bytes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: for a caller without a usable `full` answer the Bastion sends a conditional request with the shared entry's validator (R-BAS-023). A partial reader whose document is the same bytes
as the shared one (it may read every package the document lists, but not the whole repository) gets `304` from the main installation, marked `private`. The engine had no private copy of the
caller and passed the `304` to the caller, who had sent no condition: an npm client received an empty `304`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Serve the entry's bytes as the caller's `200`, do not renew the entry (chosen) | The main installation said the caller's document has this validator, so the bytes are the caller's | The caller gets its document; the shared entry stays as `full` readers made it | The class counted in the metrics is `shared` |
| (b) Refetch without a validator | One more transfer | Simple | A document transfer for every such request |
| (c) Keep the 304 and require the caller's condition | Nothing changes | None | It is wrong for a client that sent no condition |

**Choice:** (a).

**Rationale:** R-NPM-013's validators derive from what the caller may read, so a `304` to the shared validator proves equality for this caller. The development agent would revisit it if a format's validator
stops being derived from the caller's readable content.

**Applied in the spec:** R-BAS-023; code: `internal/bastion/engine/forward.go` (`onPrivate`), `TestR_BAS_023_A304ForAPrivateDocumentAnswersTheCallerWithTheSharedEntrysBytes`,
`TestR_BAS_021_R_BAS_023_R_BAS_024_EveryCallerIsAnsweredAsTheMainInstallationAnswers`.

### Q-BAS-108 · The route of a probe answer is read before the marking is stripped

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the probe of a read route that takes a body is answered `204` with `Trove-Bastion-Route: read-with-body` (R-BAS-014). The client removes every `Trove-Bastion-` header from the response (R-BAS-021), and the
engine looked for the route in the stripped header, so it never saw it: the Bastion answered the caller `204` and never forwarded the real request (npm's bulk advisories).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `mainclient.Response.Route`, read before the strip (chosen) | The client hands the route over in a field | Nothing else leaves the client with a `Trove-Bastion-` name | A field on `Response` (not on `iface`: no interface version change) |
| (b) Keep this one header | The strip skips it | Small | The header could be replayed to a caller |

**Choice:** (a).

**Rationale:** the strip exists so that nothing is replayed; a field is the narrowest way across. The development agent would revisit it if more headers of the family have to cross.

**Applied in the spec:** R-BAS-014; code: `internal/bastion/mainclient/client.go`, `internal/bastion/engine/probe.go`, `TestR_BAS_014_TheRouteOfAProbeAnswerSurvivesTheStrippedMarking`,
`TestR_BAS_014_WritesAreRefusedInTheFormatsShape`.

### Q-BAS-115 · A request with Trove-Bastion-Authorization is a forward, and a forward reaches only the package families

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the review of the main installation's side found that a recognized forward reached every route of the endpoint with the
caller's credential: `GET /api/v1/tokens`, and `GET /api/%76%31/tokens`, which the router decodes segment by segment to the same route,
listed the caller's tokens through a Bastion. It also found (a proof of concept of the Bastion-side review) that a forward whose path
decodes to an interface route was taken for an interface call: `Authorization` then held the caller's credential, the answer was `401
bastion_token_invalid`, and the Bastion read that as the refusal of its own token and marked the replica unreachable - one request, a
denial of service. R-BAS-014 also says a forwarded non-probe write is refused.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The presence of `Trove-Bastion-Authorization` makes a request a forward on every path; an interface call is a request without it, on a path the router decodes to `/api/v1/bastion/…`; the Recognize stage answers a recognized forward outside `/api/<format>/` and `/files/` - judged on the path split and decoded segment by segment, as `net/http.ServeMux` matches - with `404 not_found`, a problem of the management API, before authentication (chosen) | One rule in the stage that already holds the token; the families are the ones whose routes all call the marking stage's observer before deciding or reading a body | No route of another family runs for a forward, whatever the escapes; a forward can never be answered `bastion_token_invalid` for the caller's credential; the answer is the one every unrouted path gets, so it needs no code and tells nothing new; no change to `internal/bastion/iface` | The Bastion sees a `404` for a path it should never have sent |
| (b) A dedicated code, `bastion_not_forwarded` (404) | The same rule, a code of its own | Says why | A new problem code on the wire is a change of the interface surface and raises `InterfaceVersion` |
| (c) Judge the decoded `r.URL.Path`, or the pattern the router picked (`r.Pattern`) | Prefix tests on the decoded path, or on the route label | Shorter | The decoded path is not the router's: `/api%2Fv1/x` decodes to `/api/v1/x`, which no route serves; the pattern is empty outside the chain, so the rule would depend on how the request reached the stage |
| (d) The refusal in the Mark stage | After recognition, before the router | The stage knows the routes | It runs after the token check as well; nothing is gained, and the stage that recognizes the request is the one that decides what it is |

**Choice:** (a). `routingSegments` decodes each segment with `url.PathUnescape` and keeps one that does not decode, which is what the
router does; `/files/…` and `/api/<x>/…` with `<x>` neither empty nor `v1` pass, everything else - the management API, the interface,
`/auth/`, `/ui/`, `/`, a path whose first segment holds an encoded slash - is refused, for every method. Inside the two families the
marking stage refuses the unsafe methods of the routes that are not read routes (Q-BAS-74), and a path with no route ends in the format's
or the core's `404` without a handler, which together is "unsafe methods outside the read routes are refused". The interface accepts
the Bastion token in `Authorization` only when `Trove-Bastion-Authorization` is absent; with that header on an interface path the request
is a forward and is refused `404`, and with another credential in it the request is an ordinary one.

**Rationale:** the Bastion forwards the package clients' read paths and nothing else (R-BAS-003); the main installation must not rely on
it doing so, because the forwarded credential is the caller's. The development agent would revisit the code of the refusal if the
Bastion needs to tell a refused forward from an unknown path (that raises the interface version), and the family rule if a format
mounts a route outside `/api/<format>/`.

**Applied in the spec:** R-BAS-008, R-BAS-014; code: [recognize.go](../../internal/bastionmain/recognize.go) (`routingSegments`,
`interfacePath`, `forwardablePath`), `TestR_BAS_014_AForwardReachesOnlyThePackageFamilies`,
`TestR_BAS_008_AnInterfaceCallCarriesNoTroveBastionAuthorization`, `TestR_BAS_014_AForwardOutsideThePackageFamiliesReachesNoRoute`
(through the real chain and the routes of the management API).

### Q-BAS-116 · The Bastion's forwarded headers count only for a recognized request from an address of `bastions.trusted_proxies`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. It narrows
the means of the owner's [Q-BAS-21](#q-bas-21--internet-exposure-limits-blocking-failed-sign-in-timeouts-and-a-configurable-cache) and
[Q-BAS-17](#q-bas-17--the-bastion-mirrors-the-main-installations-url-space), whose words list the Bastion in `server.trusted_proxies`,
and keeps their intent: the main installation's limits and audit apply to the real client, and documents are rendered for the
Bastion's host.

Context: with the Bastion's egress address in `server.trusted_proxies`, every request from that address was believed, Bastion token or
not. Any workload sharing the egress (a node's SNAT, a NAT in the DMZ) could set `X-Forwarded-For` or `Forwarded` and so rotate its
address past the failed-credential throttle, push a victim's address into throttled mode, write any `source_ip` into the audit trail,
and choose the response base URL among `server.additional_base_urls`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A key of its own, `bastions.trusted_proxies`; after the Bastion token was checked, a recognized request whose address - stage 5's client address, resolved with `server.trusted_proxies` (the main installation's own ingress) - lies in it is resolved again with both lists trusted (`httpserver.WithResolved`), and the request log learns the new address (chosen) | The forwarded entry counts only when the hop that wrote it is a listed Bastion and the request proved it is one | Nothing a request without a valid Bastion token sends moves its address or its base URL; the main installation's own ingress works as before; a stolen token used from elsewhere forwards nothing; the old configuration still works | One more key to set; an entry also listed in `server.trusted_proxies` is trusted for everyone, which start-up logs |
| (b) Keep `server.trusted_proxies` and refuse forwarded headers of unrecognized requests from the egress | Needs the egress known anyway | No new key | The same list would mean two things, and an operator cannot tell which entries are Bastions |
| (c) The Bastion sends the caller's address in a header of the interface and the main installation reads it only after recognition | No forwarded headers for the client address | Exact | A new header is a change of `internal/bastion/iface` (raises `InterfaceVersion`), and the host still needs the forwarded host |

**Choice:** (a). The re-derivation is `RecognizeOptions.Forwarded`, built by the composition root (`app.bastionForwarded`) from a second
resolver of the default endpoint over `server.trusted_proxies` plus `bastions.trusted_proxies`; it runs for interface calls too, so
the handshake's `responseBaseUrl` honours the Bastion's host only from a listed address. The source of the token itself - the
`token.rejected` of a rejected one, the last use of an accepted one, the throttle bucket of rejected ones - is the address before the
re-derivation, the Bastion's own. Without `bastions.trusted_proxies` nothing is re-derived. `bastions.trusted_proxies` is validated as
`server.trusted_proxies` is (an address or a CIDR, no `/0`).

**Rationale:** the trust in a forwarded address belongs to the proof that the forwarder is a Bastion, which only the recognition has.
The development agent would revisit the key if the Bastion connects through an egress whose address changes (then the registration
would have to carry it, H-386's deferred address binding).

**Applied in the spec:** R-BAS-007 (the manual), R-BAS-008, R-BAS-010, R-BAS-017; code: [bastions.go](../../internal/config/bastions.go)
(`TrustedProxies`), [recognize.go](../../internal/bastionmain/recognize.go), [wire_bastion.go](../../internal/app/wire_bastion.go)
(`bastionForwarded`), [context.go](../../internal/httpserver/context.go) (`WithResolved`),
`TestR_BAS_017_TheBastionsForwardedHeadersCountOnlyForARecognizedBastionRequest`,
`TestR_BAS_017_AnUnrecognizedRequestFromTheBastionsEgressForwardsNothing`; open: H-467.

### Q-BAS-117 · Download events are charged to their registration by their count

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: one request of the intake carries up to `max_batch` events. The request rate of the read class (50 a second by default) let one
Bastion token write some 50,000 `artifact.downloaded` rows a second, and it filled each replica's asynchronous audit buffer
(`audit.async_buffer`, 10,000), after which other asynchronous events went to the log copy only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A cluster limiter of R-ARCH-034, `bastion.download_events`, bucket the registration, charged by the events of a batch in units of 100, rounded up (`TakeNAt`: n units are n `INCR` in the one transaction of the check, since the least-privilege ACL of R-ARCH-030 has no `INCRBY`); a batch it does not hold answers `429 rate_limited` with `Retry-After` and records nothing (chosen) | `bastions.downloads.events_per_second` (200) and `events_burst` (2,000, holding `max_batch`) | Bounds the rows a token can write, across the replicas, with one round trip per batch and at most 100 `INCR` (`max_batch` is at most 10,000); the Bastion already keeps and resends a batch answered `429` (Q-BAS-78); a burst of a cold start fits two full batches | A batch of one event costs a unit of 100; a Bastion that serves more than the budget for long loses what its buffer cannot hold, counted (R-BAS-039) |
| (b) Lower `max_batch` | Smaller requests | No new mechanism | The request rate still multiplies; more requests for the same events |
| (c) Refuse while the audit writer's buffer is above a level | Back-pressure | Follows the real load | Per replica, invisible to the other replicas, and it refuses everyone's events at once |
| (d) One `INCRBY` per batch, charged per event | Exact | One command | Needs `INCRBY` in the ACL of the main installation's Valkey user, which the chart, the local profile and the integration stack would all have to grant; the integration test against the stack found the limiter falling back to the replica's share without it |

**Choice:** (a). An empty batch that reports only lost events costs nothing. The limiter falls back to the replica's share while Valkey
is unavailable, like every limiter of R-ARCH-034; a request of more than `cluster.MaxTakeUnits` (100) units is refused without being
counted. Configuration refuses an `events_burst` whose whole units do not hold `max_batch` rounded up (a full batch would never fit), one
below 100, and a window above 12 hours.

**Rationale:** the events are what costs, so the events are what is charged; the registration is who sends them. The development agent
would revisit the default if a Bastion's normal traffic meets it (the lost count in `bastion.downloads.dropped` shows it), and a
budget shared by all registrations if many registrations at once fill the audit buffer (H-468).

**Applied in the spec:** R-BAS-016; code: [downloads.go](../../internal/bastionmain/downloads.go) (`EventBudget`),
[limiter.go](../../internal/cluster/limiter.go) (`TakeNAt`), [valkey_ratelimit.go](../../internal/cluster/valkey_ratelimit.go),
[wire_bastion.go](../../internal/app/wire_bastion.go) (`LimiterBastionDownloadEvents`), [bastions.go](../../internal/config/bastions.go),
`TestR_BAS_016_DownloadEventsAreChargedToTheRegistrationByTheirCount`, `TestR_BAS_016_ARequestIsChargedItsUnits`.

### Q-BAS-118 · A write probe is admitted as the anonymous caller for routing only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: with anonymous access off (the default), the authentication stage answered a probe - recognized, `Trove-Bastion-Probe: 1`, no
credential of a caller by design - `401 credentials_required` before the router ran, so `npm publish` through a Bastion got a `401`
instead of R-BAS-014's `403 bastion_read_only`. The tests had used a stand-in authentication that admitted every request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The Bastion's wrapper of the authentication stage (`v1.BastionRouteAuth`) returns the anonymous caller for a recognized probe without running the stage (chosen) | The probe reaches the route match, where the marking stage answers it | One line where the interface calls are admitted; the decision point still refuses the anonymous caller everywhere, so no read opens; the rate limits charge the probe to the caller's address | The admission rests on every route of the two families telling the marking stage its kind first (H-469) |
| (b) Admit it as the registration | The Bastion principal | Names the Bastion in the log | A principal the decision point should never see would reach the route; the rate limits would charge the Bastion |
| (c) Answer the probe in the Recognize stage | No router | Simple | The stage does not know the route, its kind or the format's error shape, which R-BAS-014 asks for |

**Choice:** (a). A probe never passes the route match: a route that is not a read route is refused `403 bastion_read_only`, a read route
that takes a body is answered `204` (Q-BAS-74), and a path with no route ends in the format's or the core's `404`. Only forwards of the
package families reach the stage at all (Q-BAS-115). A forwarded request without a caller credential that is no probe is still the
stage's, and refused while anonymous access is off.

**Rationale:** the probe is the Bastion's question about a route, not a request of a caller, so the anonymous rule has nothing to decide
on it. The development agent would revisit (a) if a handler is mounted below `/api/<format>/` or `/files/` that does not call the
observer (H-469).

**Applied in the spec:** R-BAS-014; code: [bastion_services.go](../../internal/api/v1/bastion_services.go) (`BastionRouteAuth`),
`TestR_BAS_014_AProbeIsAnsweredThroughTheRealAuthenticationStage` (the real stage of `internal/auth`, the real dispatcher and raw file
handler).

### Q-BAS-119 · The check runs the credential through a stage that records nothing in the request log

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the check (Q-BAS-66) runs the caller's credential through the endpoint's authentication stage on a clone of the Bastion's
request; the stage recorded the principal it accepted in the request-log holder of the context, which the clone shares, so the line of
`POST /api/v1/bastion/check` named the checked caller instead of the registration (R-BAS-008).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The composition root's credential stage records nothing; the chain's authentication function records the principal it returns (chosen) | `credentialStage` returns the principal, `authenticate` wraps it with `recordPrincipal` | The log line of every request stays the chain's; the check needs no special case | None found |
| (b) Give the clone a request-log holder of its own | `observability.WithRequestInfo` on the clone's context | Local to the check | The clone's holder is thrown away; a later caller of the stage would meet the same trap |

**Choice:** (a).

**Rationale:** what the request log says about a request is the chain's business, not the business of whoever runs the stage. The
development agent would revisit it if a second caller of the stage needs the record.

**Applied in the spec:** R-BAS-008; code: [wire_identity.go](../../internal/app/wire_identity.go) (`authenticate`, `credentialStage`),
`TestR_BAS_008_TheCheckLeavesTheRequestLogLineOfTheRegistration`.

### Q-BAS-120 · Unassigning a repository from a project drops every permission answer, and every journaled event is classified

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `authz.project.repository_unassigned` is journaled as a revocation (R-AUTHZ-031): a repository that leaves its project leaves
the project's policies behind, so callers' reads of it can narrow. The feed mapped it to no entry, and the test of R-BAS-015 checked a
fixed list, so a new journaled event could leave the feed unnoticed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `grants`; every journaled event of the audit registry is either read by the feed or listed with its reason in `noFeedEntry`, and the test is driven by the registry (chosen) | All permission answers dropped on an unassignment | Exact enough: the Bastion drops only URL answers on a `content` entry, not shape answers (R-BAS-026, rule 3 of R-BAS-020), so a `full` answer the unassignment ended would survive anything else | Unassignments are rare; each drops every caller's answers once |
| (b) `content` for the repository and its virtual closure | Repository-scoped | Fewer answers dropped | Not exact: shape answers survive it |
| (c) `content` with a new meaning that drops shape answers too | Repository-scoped and exact | Fewest answers dropped | Changes the meaning of a feed entry kind, which raises `InterfaceVersion` |

**Choice:** (a). The other journaled events yield no entry, with these reasons: `authz.ownership.removed` and `.released` bound who may
publish a name; `package.published` reserves numbers; `authz.project.updated` changes the switches that bound what project administrators
may change; `authz.project.deleted` deletes only an empty project, whose policies were deleted first, each `authz.policy.deleted`
(`grants`); `authz.project.admin_removed` ends the administration, which gives `manage` and no read (no action implies another,
R-AUTHZ-001).

**Rationale:** a narrowing the Bastion keeps is worse than answers it asks again. The development agent would revisit (c) when the
interface version is raised for another reason.

**Applied in the spec:** R-BAS-015; code: [feed.go](../../internal/bastionmain/feed.go) (`mapEvent`, `feedActions`, `noFeedEntry`),
`TestR_BAS_015_EveryJournaledEventIsClassified`, `TestR_BAS_015_UnassigningARepositoryDropsEveryPermissionAnswer`.

### Q-BAS-121 · A path is classified as the main installation's router will read it, and a token refusal on a forward is no statement about the Bastion's token

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: security finding P1. The main installation's router decodes every segment of a path before it compares it, and the engine compared the raw segment with `v1`. `GET /api/%761/users`
reached the management API through the Bastion, and `GET /api/%761/bastion/handshake` made the main installation answer `401 bastion_token_invalid`, which the client and the link read as a
refusal of the Bastion's own token: the replica turned unreachable, once per poll, for one request from anyone. Two causes, two decisions.

**The classification.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse what cannot be read as the router will read it: a percent-encoding in the family, format or key segment, and a `.` or `..` segment, answer `404 bastion_not_served` (chosen) | The Bastion still compares what it received; it just does not classify an ambiguous spelling | No format or router knowledge in the Bastion; a test holds the rule against `net/http.ServeMux` | A client that encodes a letter of the family, format or key (none does) is refused |
| (b) Decode every segment and forward the decoded form | The Bastion reads as the router does | Equal reading | Changes the cache key and the bytes that reach the main installation; two spellings of one URL are two readings |
| (c) Link the main installation's router | Same code | Exact | Against R-BAS-001 (the Bastion links none of the main installation's code) |

**The reading of `401 bastion_token_invalid`.**

| Option | What it means | For | Against |
|---|---|---|---|
| (a) It refuses the Bastion's token only in the answer to a call the Bastion makes itself: handshake, feed, check, download batch; the same answer to a forward is a bad answer (`502 main_failed`), the gauge stays and the link is not told (chosen) | The path of a forward is the caller's, so the answer to it can come from any route | The only calls whose path the Bastion chooses are the ones that count | A really revoked token is noticed by the next feed poll (within `poll_interval`), not by a forward |
| (b) Keep reading it on a forward | Nothing changes | One mechanism | Any caller turns the replica unreachable by naming a route of the interface in a spelling the Bastion does not catch |
| (c) Tell the two apart by a header of the answer | The main installation marks its answers | Exact | Needs a change of the interface, and `InterfaceVersion` |

**Choice:** (a) and (a).

**Rationale:** the classification now fails closed, and the reading of the refusal no longer depends on it: the second line of defence holds even for a spelling the first one misses. The main
installation refuses a forward outside the mirrored families in parallel (its side of the same finding). The development agent would revisit the first choice if a format needs an encoded
segment in the first three positions of its paths, and the second if the interface gains a header that marks the answers to its own calls.

**Applied in the spec:** R-BAS-003, R-BAS-006, R-BAS-018, R-BAS-027; code: `engine.classifyPath`, `mainclient.classify`, `link.Guard`;
`TestR_BAS_003_R_BAS_018_APathIsClassifiedAsTheMainInstallationsRouterWillRouteIt`, `TestR_BAS_003_AnEncodedSpellingOfTheManagementAPIIsNeverForwarded`,
`TestR_BAS_003_R_BAS_006_AnEncodedSpellingOfTheInterfaceRoutesCannotTurnTheReplicaUnreachable`, `TestR_BAS_006_R_BAS_021_ATokenRefusalAnswerToAForwardIsNoRefusalOfTheBastionsToken`,
`TestR_BAS_006_R_BAS_021_ARefusalOfTheTokenOnAForwardDoesNotMakeTheReplicaUnreachable`.

### Q-BAS-122 · A `content` entry ends the service of a cached entry also while the main installation is unreachable

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: security finding P2. The engine served an entry when the replica was unreachable or the entry was fresh, and only freshness looked at the `content` entries of the feed. A replica the main
installation could not be reached by, but whose link still read the feed (a forced handshake failure, for one), kept serving an artifact that was withdrawn through the feed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The entry is unservable when a `content` entry naming its repository was received after `validatedAt − 15 s`, reachable or not (chosen) | The grace period extends an answer, never past a withdrawal | Judged at use, like rule 3 of R-BAS-020; nothing to delete; the entry is usable again after the next successful check | While the feed is readable and the main installation is not, a publish in a repository makes the entries of it validated before unservable |
| (b) Delete the entry when the `content` entry arrives | The entry is gone | Memory | A deletion that fails leaves the entry; the entry may be wanted after the outage |
| (c) Leave it | Grace serves everything | Most availability | The finding |

**Choice:** (a).

**Rationale:** a feed that is readable means the main installation is reachable enough to say what changed; a replica that cannot read the feed receives no entry and serves as R-BAS-027 says. The
case where the two differ (the handshake fails and the feed does not) is rare and short. The development agent would revisit it if such partial outages turn out to be common and the repositories
change so often that the grace period stops helping.

**Applied in the spec:** R-BAS-022, R-BAS-027; code: `answers.FeedLog.ContentSince`, `engine.withdrawn`; `TestR_BAS_022_R_BAS_027_AWithdrawnArtifactIsNotServedWhileTheMainInstallationIsUnreachable`.

### Q-BAS-123 · The fill reads the main installation at its own speed, and no client holds a slot or a waiter

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: security findings P3 and B4. A budget slot is held until the response body ends, and the body was read at the client's write speed, so seventeen connections that took their tarball one
socket buffer at a time held every anonymous slot (everybody else got `503 bastion_busy`), and a second caller of the same cold key waited behind a slow leader without a bound.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decouple the fill: a goroutine reads the main installation's body into the spool and frees the slot when the body ends; the caller is served from the spool at its own pace, with the withheld tail; the flight of the collapsed fetch ends at the commit (chosen) | The speed of the main installation decides how long a slot is held | Uses the spool the fill already writes; the withheld tail is the same | The spool file lives as long as the slowest follower; a fill goes on when its caller leaves (H-472); a spool that fails cuts the transfer |
| (b) Tee into a bounded memory buffer between the body and the client | A client that falls behind blocks the body | No spool read | The slot is held again once the buffer is full |
| (c) Cut slow clients with a shorter stall timeout | `server.transfer_stall_timeout` is the bound | One value | A client that takes one chunk per timeout still holds the slot; a legitimate slow link is cut |

For the waiters:

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A waiter that has waited 30 s answers `503 bastion_busy` with `Retry-After: 1` (chosen) | The retry joins the fetch again or finds the entry | No extra load on a main installation that is slow | The caller retries |
| (b) It makes its own request | N requests after the wait | The caller is served | A slow main installation gets the multiplied load the collapsing exists to avoid |

For the body of a pass-through, which cannot be spooled (an answer above `max_object_bytes`, without `Content-Length`, a document above the limit): the client must take each 32 KiB chunk within 10 s,
and after a grace period of 30 s it must have averaged 64 KiB per second, or the transfer is cut and the body closed (chosen over a total time, which would cut a legitimate large download on a slow link).

**Choice:** (a), (a), and the floor.

**Rationale:** a slot is the scarce thing, and the only party that should decide how long it is held is the main installation. The values are constants of the engine (`collapseWaitMax`, `paceGraceMax`,
`paceMinRate`, `paceChunkMax`, `fillStallMax` for a main installation that sends no byte for 60 s) and not keys: no operator has asked to tune them. The development agent would revisit it if
a deployment needs another floor, and the pass-through at the floor is the gap H-471. A spool that cannot take the bytes now cuts the transfer instead of passing them on unverified, which closes
the gap H-459 of the first build.

**Applied in the spec:** R-BAS-021, R-BAS-025; code: `engine.fillRun`, `engine.fillArtifact`, `engine.follow`, `engine.waitLeader`, `engine.passThrough`, `engine.handle`, `engine.mainHold`;
`TestR_BAS_025_ClientsThatReadSlowlyDoNotHoldTheBudgetSlots`, `TestR_BAS_025_AWaiterOfACollapsedFetchIsNotHeldByTheLeadersClient`, `TestR_BAS_021_R_BAS_025_TheFillOutlivesTheClientThatStartedIt`,
`TestR_BAS_025_AWaiterThatWaitsTooLongForTheLeaderAnswers503WithRetryAfter`, `TestR_BAS_025_APassThroughBodyDoesNotHoldItsSlotForAClientThatDoesNotTakeIt`,
`TestR_BAS_025_TheFloorOfAPassThroughStartsAfterTheGracePeriodAndIsAnAverage`.

### Q-BAS-124 · A `404` that is not marked `absent` is never kept as a URL answer

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: security finding P4. A `404` to a conditional request was kept as the caller's URL answer and replayed without a request, while the `404` to a plain fetch was not (Q-BAS-80). A narrowed
reader asking for a name twice got a second answer from memory for a name the Bastion holds and from the main installation for one it does not: the timing told which hidden names are cached,
against R-BAS-024 and Q-BAS-42.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Never keep it, on either path (chosen) | Every `404` that is not `absent` is asked for again | One rule; the acceptance of R-BAS-024 holds as written | A narrowed reader's hidden cached name costs one request per read (it costs what a missing name costs) |
| (b) Always keep it, per caller and cache key | Both paths answer the second request from memory | Fewer requests | Contradicts the acceptance of R-BAS-024 (`$PART` asking twice sends two requests); a name published inside the period stays a `404` |
| (c) Keep the conditional one (Q-BAS-80) | The first build | Fewest requests for the hidden cached name | The finding |

**Choice:** (a).

**Rationale:** the per-address miss limit and the budget bound what a narrowed reader's repeated reads cost the main installation; the oracle was the difference between two paths, and (a) removes the
difference. A `403` is kept as before: it is the same for a name that does not exist and for one that does, so it shows nothing. The development agent would revisit it if narrowed readers' reads
dominate the main installation's load; the answer then is a short per-caller memory for both cases at once, never for one.

**Applied in the spec:** R-BAS-020, R-BAS-022, R-BAS-024; Q-BAS-80 is superseded; code: `engine.onPass`; `TestR_BAS_024_ANarrowedReaderCannotTellACachedHiddenNameFromAMissingOne`,
`TestR_BAS_022_APartialReaderCostsOneConditionalRequestPerTarballAnsweredByA304`.

### Q-BAS-125 · A result that depends on the caller's rights switches collapsing off for that caller only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: security finding B10. A refusal, a private document or any other answer that is not shared marked the cache key volatile for everyone, so the fetches of the key were no longer collapsed. A caller
without rights could keep collapsing off for a popular key.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A mark that depends on the caller (a refusal, a private document) is the caller's, keyed by its credential and the cache key; a mark that does not (a search, a document above the limit, a `Vary` the Bastion cannot key on) stays the key's (chosen) | The result decides whose mark it is | An attacker affects only itself | A partial reader's first request after a minute may wait for a leader and then fetch itself |
| (b) No marks | Collapsing always on | Simple | Every waiter of a private result pays a second round trip, which the marks exist to avoid |
| (c) Marks only from callers with a `full` answer | A caller without rights cannot mark | Simple | A private document is the result of a partial reader, so the marks would never be set |

**Choice:** (a).

**Rationale:** collapsing never shares a decision (R-BAS-025), so scoping a mark to its caller loses nothing but the saved round trip of a result that was not shareable anyway. The marks are in the
least-recently-used registry that already bounds them. The development agent would revisit it if the registry fills with per-caller marks.

**Applied in the spec:** R-BAS-025; code: `engine.markVolatileFor`, `engine.isVolatileFor`; `TestR_BAS_025_ARefusalOfOneCallerDoesNotSwitchCollapsingOffForOthers`.

### Q-BAS-126 · The link applies a page as a unit, ends the start read at the end of the feed, and bounds each call

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: security findings B3, B7, B8 and B11, all in the link. (1) The history started, and the replica turned reachable, after the first page even when it said `more`; a failing second page left a
replica that knew a part of the feed. (2) Every entry of a page was recorded and then followed by a full pass over the answers that dropped what it matched: a burst page of 1,000 entries against 200,000
answers was a thousand passes. (3) Reachability was evaluated after a whole tick, and a connection that swallowed the request held the call until `response_header_timeout`. (4) The IDs of applied
entries were pruned with the entries, after `period + grace_period + 15 s`, which is shorter than the feed's lookback (5 minutes) for a short period: an entry the feed sent again was applied again
and made every answer obtained since unusable.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The start read ends at the page that says `more: false`; a page is recorded whole and then dropped in one pass with sets of the tokens, principals and repositories it names; every call ends after `unreachable_after`; IDs are kept for at least the lookback (chosen) | Each cause is removed | Four small changes with one test each | The default lookback is a constant of the Bastion (H-470) |
| (b) Keep the first page's behaviour and the per-entry drop, and rely on the 5 s bound | Nothing changes | None | B3 leaves a replica with a half-known history; B7 costs seconds under a burst |
| (c) Read the lookback from the handshake | The Bastion follows the main installation's value | Exact | A change of the interface, and `InterfaceVersion` |

**Choice:** (a).

**Rationale:** the validity rules already make every matching answer unusable at the moment the page is recorded (R-BAS-020 rule 3), so the deletion is memory and Valkey housekeeping and may be one pass. The
development agent would revisit (c) if operators set `bastions.feed.lookback` above its default.

**Applied in the spec:** R-BAS-026, R-BAS-027; code: `link.pollOnce`, `link.applyEntries`, `link.callContext`, `engine.answerStore.Drop`, `answers.PruneCutoffs`;
`TestR_BAS_026_TheHistoryStartsOnlyWhenTheStartReadReachedItsEnd`, `TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound`,
`TestR_BAS_027_ACallThatTheMainInstallationNeverAnswersIsCutAtUnreachableAfter`, `TestR_BAS_026_AnAppliedIDIsKeptForAtLeastTheFeedLookback`.

### Q-BAS-127 · A refused request or miss does not consume the limit it was refused by

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the review of the guard (a proof of concept at 330 misses a minute against a limit of 300) found that a refused miss was counted like an admitted one (Q-BAS-92), and that every refusal counted toward the
block threshold. From the second minute on no miss was admitted, then the address was blocked for 15 minutes, doubling to 24 hours, with its hits: one client behind a shared NAT blocked every partner there. The request limits
had the same shape with Valkey, where R-ARCH-034's `Take` counts a refused request ("a caller that keeps sending while refused stays refused until its rate falls below the limit").

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A limit admits up to its rate; a refusal consumes nothing; blocking stays the answer to a flood (chosen) | The miss count and the request window are counts of the admitted events; a refused one is given back. The block threshold is unchanged: only events beyond the limit are refusals, so 110 % of a limit yields 10 % refusals | A client slightly over its limit gets the limit and is not blocked; R-BAS-035 to R-BAS-037's acceptance numbers hold; a flood (several times the limit) still reaches the threshold | A client that keeps hammering gets no longer a penalty by the limit itself, only by the block |
| (b) Keep counting refusals, raise the default threshold | The starvation stays; only the block moves away | No code change | The client still starves at the limit, and every limit tuned for it has to be wider than the real need |
| (c) Count refusals with a decay | Refusals count a fraction | Fewer refusals count | Another parameter, an estimate nobody can state in a sentence |

**Choice:** (a). With Valkey the counter is incremented before the decision, in one round trip and without a script, as R-ARCH-034 has it, and a refusal counts itself a second time in a counter of refusals of its own
(same window, same expiry); the estimate is the counter minus the refusals (`Limiter.TakeUncharged` for the request limits, `Limiter.EstimateWithin` for the miss limits, [internal/cluster/limiter_uncharged.go](../../internal/cluster/limiter_uncharged.go)).
The Bastion's Valkey user has no `DECR` (its ACL lists `INCR`, `EXPIRE`, `GET`, `MULTI`, `EXEC`), so a refund by decrement was not available. Without Valkey the request limit was a token bucket, which takes nothing when it refuses,
and the miss count is undone in the same lock (`shared.Limiter.AddIfWithin`). The failure counter of R-BAS-036 and the counter of refusals keep counting every event: they count what happened, not what is admitted.

**Rationale:** R-ARCH-034's semantics ("refused requests use up budget") serve the main installation's logins and API, where a caller that is refused should stop; the Bastion's guard protects partners that share an address, and a limit that
starves a client slightly over it, and then blocks the address, turns the guard into the denial of service it exists to stop. R-ARCH-034 itself is unchanged: the Bastion's limiters are the other reading of the same window counter.
The development agent would revisit this if a client that stays at exactly the limit for long must be told apart from a flood by the Bastion (a lower threshold or a rate of refusals would then be the lever), or if the Bastion's Valkey user
gains `DECR`, which would save the second counter.

**Applied in the spec:** R-BAS-035, R-BAS-036, R-BAS-037, Q-BAS-92 (amended), H-474; code: [guard](../../internal/bastion/guard/instance.go), [shared](../../internal/bastion/shared/local.go), [cluster](../../internal/cluster/limiter_uncharged.go),
`TestR_BAS_035_R_BAS_037_AClientSlightlyOverTheMissLimitKeepsItsBudgetAndIsNotBlocked`, `TestR_BAS_035_R_BAS_037_AClientSlightlyOverTheRequestRateKeepsItsRateAndIsNotBlocked`, `TestR_BAS_037_AFloodOfMissesStillBlocks`,
`TestR_BAS_035_R_BAS_037_WithValkeyAClientSlightlyOverItsLimitsKeepsItsRateAndIsNotBlocked`.

### Q-BAS-128 · The local forms evict in constant time, the block decision locks per address, and the window range is the limiters'

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: three findings of the review of the shared state. (1) Without Valkey (`valkey.mode: none`, the default) each map of the local forms held at most 100,000 keys, and a new key arriving at a full map scanned all of it under the one mutex
every request takes; an IPv6 /47 has enough /64 prefixes to keep every request on that path. (2) The decision to block an address was serialized by one process-wide mutex held across three or four Valkey round trips. (3)
`bastion.failed_auth.window` accepted up to 24 h and the limiter silently counted at most 12 h.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Sharded maps with a least-recently-used list per shard; a lock per address for the block decision; the configuration refuses a window above 12 h (chosen) | 64 shards, each with its own mutex and a list: a new key at a full shard evicts the oldest one in constant time. The block decision takes a lock keyed by the address, which exists only while somebody holds or waits for it. `failed_auth.window` is limited to 1 s to 12 h by the schema | Constant time per request; unrelated addresses never wait for one another; the configured value is the counted value | An address evicted while it still counts starts again (as before); a shard evicts at its share of the bound, so a skewed hash can evict a little before the whole map is full |
| (b) Keep one lock, evict a random entry | Delete the first key a range finds | Smaller change | The bound is arbitrary and the lock stays global |
| (c) Support windows up to 24 h in the limiters | Raise `MaxWindow` | The configuration stays as it is | The limiters of R-ARCH-034 are specified to 12 h, and the Valkey counters expire at twice the window |

**Choice:** (a). The least recently used entry is the one that most likely no longer counts (a counter two windows old, a bucket that is full again, a block that ended), so the order is a better preference than the arbitrary one it replaces.
**Rationale:** the Bastion is exposed to exactly the many-address flood the old scan rewarded. The development agent would revisit it if the evicted-but-still-counting case shows in the metrics of a real deployment (the bound would then be raised or
the shards enlarged), or if a longer failed-authentication window is wanted (the limiters would have to support it first).

**Applied in the spec:** R-BAS-036; code: [shared/lru.go](../../internal/bastion/shared/lru.go), [shared/local.go](../../internal/bastion/shared/local.go), [guard/keyedlock.go](../../internal/bastion/guard/keyedlock.go),
[config](../../internal/config/bastion_role.go), `TestR_BAS_035_AFullMapEvictsInConstantTime`, `TestR_BAS_035_AnActiveKeySurvivesAFloodOfNewKeys`, `TestR_BAS_037_ABlockInFlightForOneAddressDoesNotHoldAnotherBack`,
`TestR_BAS_036_TheConfigurationAcceptsExactlyTheWindowsTheLimiterCounts`.

### Q-BAS-129 · The import rule follows the dependency closure and lists what it cannot narrow

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the rule of R-BAS-001 checked the direct imports of the Bastion's packages. The role's dependency closure is larger: `internal/cluster` imports the PostgreSQL store, the change log's table registry and the format areas, `internal/httpserver`
imports the audit types, the scope type and the CI claims, and `internal/blob/s3` imports the blob store interface. "Imports no subsystem" was not true of the closure.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The rule is transitive; the packages it cannot narrow are listed with their importer and reason (chosen) | `CheckBastionImports` walks the closure from the allowed platform packages; a package that enters it and is not allowed or listed is a violation that names the chain. A runtime test proves the operative claim: no connection attempt to a PostgreSQL address | Honest and enforced; a new edge fails the build with its chain | The binary still contains the listed packages (H-473) |
| (b) Split `internal/cluster`, `internal/httpserver` and `internal/blob/s3` now | A Valkey-only package, a request-context package without audit and principal types, the S3 client without the interface | The closure is really narrow | `cluster.NewValkey`, `httpserver.SourceFrom` and `httpserver.Principal` have dozens of callers in the packages the other developers of this wave are changing; the split belongs to a wave of its own |
| (c) Keep the direct rule and correct the sentence | Smaller | Nothing is enforced for the closure | A new transitive subsystem enters unseen |

**Choice:** (a). **Rationale:** R-BAS-001's operative claim is that the role enables no other subsystem; the test of the process proves it, and the rule makes the compile-time closure a reviewed list instead of an accident. The development agent would revisit it when
a platform package is split anyway (the exceptions it removes are deleted from [bastionTransitive](../../internal/archtest/bastion.go)).

**Applied in the spec:** R-BAS-001, H-473; code: [archtest/bastion.go](../../internal/archtest/bastion.go), `TestR_BAS_001_TheRoleOpensNoConnectionToPostgreSQL`, [developer page](../developer/bastion.md).

### Q-BAS-130 · Behind the Ingress the chart requires the trusted proxies

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: with the chart's defaults (`config.server.trusted_proxies: []`, the Ingress on, the https port admitted from the ingress namespace only) every request reaches the pods from the ingress controller, so every caller's address is the controller's pod:
one client's bad tokens put every unverified credential into the timeout, a flood blocks the Bastion for everyone, and the anonymous limits apply to the whole internet together.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `config.server.trusted_proxies` is required while `ingress.enabled`; the render fails naming the value and what to list (chosen) | The operator lists the CIDRs of the controller's pods; the message says why | Explicit; the value is in the operator's hands, where the knowledge is | One more required value |
| (b) Derive it from `networkPolicy.ingress.https.from` | A peer that is an `ipBlock` yields its CIDR | No extra value for operators who use `ipBlock` | The default peer is a `namespaceSelector`, which has no CIDR; the pod CIDR cannot be read at render time, so the derivation covers the rare case only and invites a wide range |
| (c) Default to a private range | `10.0.0.0/8` or the like | Renders out of the box | Trusts the forwarded address of any pod in the range, and a wrong default is worse than a refused render |

**Choice:** (a). **Rationale:** the failure of leaving it empty is silent and total, the fix needs a fact only the operator has, and a render that fails costs a minute. The load refuses a prefix of length 0, so the widest value is still bounded by the
role. The development agent would revisit it if a controller that terminates the connection can be asked for its own address range (an ingress class parameter).

**Applied in the spec:** R-BAS-001, R-BAS-045, R-BAS-046; code: [chart validation](../../deploy/helm/trove-bastion/templates/_validate.tpl), `TestR_BAS_045_TheClientAddressNeedsTheTrustedProxiesBehindTheIngress`; [values](../../deploy/helm/trove-bastion/values.yaml), [chart page](../developer/helm-chart.md), [admin manual](../manual/admin/bastion.md).

### Q-BAS-131 · The bytes of the fills in progress are bounded, and the chart sizes the volumes from the bound

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the bucket backend spools fills into `/tmp`, an `emptyDir` of 1 GiB, while `max_object_bytes` defaults to 2 GiB and the concurrent fills are bounded only by the miss budget. An `emptyDir` over its `sizeLimit` evicts the pod, which is worse than a miss.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A run-time bound on the bytes of all fills in progress, a chart that sizes `tmp` from it and refuses a volume that cannot hold it (chosen) | `bastion.cache.spool_max_bytes` (2 GiB, at least `max_object_bytes`); a fill that would pass it is passed through unkept and gives its bytes back; the default `tmp.sizeLimit` is the spool plus 256 MiB with the bucket backend; the volume backend's `cacheVolume.size` must hold `max_bytes` plus the spool | The pod can no longer fill the volume with fills, whatever the traffic | A burst of large cold artifacts keeps fewer of them |
| (b) Only the chart validation (`tmp.sizeLimit` ≥ `max_object_bytes` × concurrent fills) | A volume of 64 GiB and more | No code | A volume sized for the worst case of a rare burst, and nothing holds if the budget or the traffic changes |
| (c) Only the run-time bound | No chart change | Small | The chart default still evicts at its first large object |

**Choice:** (a). **Rationale:** the run-time bound is the only thing that keeps the promise whatever the configuration; the chart makes the numbers agree so that the bound is a rare event and not the normal one. The development agent would revisit it if
fills of objects near `max_object_bytes` are common (the bound would be raised, or a fill would reserve its announced size up front, which the engine's call does not give the cache yet).

**Applied in the spec:** R-BAS-030, R-BAS-045, R-BAS-046, H-450; code: [cache/spool.go](../../internal/bastion/cache/spool.go), [cache/fill.go](../../internal/bastion/cache/fill.go), [chart helpers](../../deploy/helm/trove-bastion/templates/_helpers.tpl),
`TestR_BAS_021_R_BAS_030_TheFillsInProgressNeverHoldMoreThanTheSpoolBudget`, `TestR_BAS_030_TheSpoolFitsTheTemporaryVolume`.

### Q-BAS-140 · Where the 5 s of a burst page is enforced

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: finding F-45. `TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound` failed in the unit job of Test and Release run 41797860424 (`make test`: `-race -covermode=atomic -coverpkg=./...`, all packages in parallel) with "the page took 5.47 s to apply, the bound is 5 s". Its comment claimed two orders of magnitude of headroom. Measured for one page (1,000 token entries, 200,000 answers, 100,000 deleted): about 0.1 s natively, 0.65 s with `-race -coverpkg=./...`, 1.0 to 1.2 s in the pinned `golang:1.27.3` image on 2 CPUs with `-coverpkg=./...` and 0.57 to 0.67 s without, so 5 to 50 times headroom when the test runs alone. The cost is CPU work that scales with the load: the page is one pass (map iteration, a matcher lookup per answer, the deletion of the half that matches, with the allocation and GC that go with it) and costs 2.0 to 2.4 times one matching pass over the same answers, natively and under the race detector. A pass per entry (the design of Q-BAS-126 replaced) would be 1,000 passes. One real waste was in the pass: `dropWhere` hashed (SHA-256 and hex) the key of every deleted answer for the shared store even when the shared store is off, 100,000 hashes in this test.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Widen the fixed bound | 30 s | Smallest change | A number that is only right for one machine; blind to the regression the test guards |
| (b) Remove the per-change check | The 5 s only in the nightly run | No flake | A pass per entry (a thousand times the cost) passes every change |
| (c) The 5 s of the reference environment under `TROVE_PERF_GATE=1`; per change a bound relative to the cost of one matching pass over the same answers, measured right before (chosen) | Follows [Q-ARCH-293](architecture.md#q-arch-293--where-a-wall-clock-target-of-a-functional-test-is-enforced) | The load scales both sides; still fails on a pass per entry (1,000 passes against a bound of 100) | One number (100) tuned to a measurement of 2 to 3 |
| (d) Dedicated quiet runners | Fixed bound stays | Strictest | The runners are shared ([Q-ARCH-220](architecture.md#q-arch-220--p-13-starts-its-clock-at-the-commit-and-its-p99-is-enforced-in-the-nightly-performance-run)) |

**Choice:** (c), and `dropWhere` computes the shared record keys only when the shared store is enabled. The test runs in `make test-performance` with the gate (the 5 s of R-BAS-026 enforced); per change the page may cost at most 100 times the median of 5 matching passes measured right before it.

**Rationale:** the ratio of the page to a pass was 2.0 to 2.4 in all environments measured, while the absolute time varied by a factor of 50 with the load, so the ratio is the stable measure. A per-entry design exceeds 100 passes by a factor of 10. The development agent would revisit the choice when the unit tier runs on runners without a CPU limit (then the fixed 5 s returns) or when a regression slips through the factor.

**Applied in the spec:** R-BAS-026 (test note); code: `engine.answerStore.dropWhere`; `TestR_BAS_026_AFeedPageOfAThousandEntriesIsAppliedAgainstTwoHundredThousandAnswersWithinTheBound`; [releasing.md](../developer/releasing.md); `make test-performance`.

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
for any principal and path, at most `bastions.downloads.events_per_second` a second (Q-BAS-117; always marked with the registration's
name, so attributable to the Bastion, not to the principal);
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

### H-428 · The ops listener of a Bastion serves its metrics without authentication

Stage: R-BAS-040 and R-BAS-047. Subject: what the series of the Bastion tell a party that reaches the ops port.

Live on every Bastion from the first build of the role. The role has no `metrics` section: the ops listener serves `/metrics` as the
main installation does with `metrics.auth: none`, and the chart's NetworkPolicy admits the ops port from the monitoring namespace and
the probe CIDRs only (R-BAS-047). The series carry no address, token, path or repository key (R-BAS-040), but they tell how much
traffic the Bastion carries, how full its cache is, whether the main installation is reachable and when its token expires. What makes
it real: a pod in the allowed namespace that is not the monitoring stack, or a chart with `networkPolicy.enabled: false`.

### H-432 · The edge limit counts per TCP peer address and has no burst

Stage: R-BAS-038 (stage 1, HAProxy Ingress). Subject: the callers behind one address, and the coarse protection of the Bastion.

Dormant until a Bastion is installed with `edgeLimit.enabled`; then live on every one. HAProxy Ingress keys its limit on the source
address of the TCP connection it accepts. Behind a load balancer that does not pass the client address on (no proxy protocol, no
preserved source address), every caller shares the budget of the balancer's address: the limit then refuses everyone together or
nobody. Callers behind one NAT share a budget as well. The limit has no burst allowance: a client whose rate is above
`edgeLimit.requestsPerSecond` streams a second is refused while it stays above. What makes it real: an edge that terminates
connections behind a balancer without the real address, which the chart cannot detect (NOTES.txt says so); the Bastion's own stage 2
reads the forwarded address through `server.trusted_proxies` and is not affected. `edgeLimit.enabled: false` closes it.

### H-433 · The egress policy names the main installation by address

Stage: R-BAS-047, `networkPolicy.egress.main.to`. Subject: the availability of the Bastion towards the main installation.

Dormant until a Bastion runs with the egress policy (on by default); then live on every one. A NetworkPolicy selects by pod, namespace
or address, never by name, so `networkPolicy.egress.main.to` holds the address of the main installation's ingress. When that address
changes, the Bastion is cut off until the value is changed and rolled out, and then serves from answers for the grace period
(R-BAS-027) and refuses afterwards. What makes it real: a main installation behind a load balancer or an ingress whose address
is not fixed; a CNI with FQDN policies (an `extra` rule in `networkPolicy.egress.extra`) closes it.

### H-434 · A controller that ignores the annotations turns the edge limit and routing off silently

Stage: R-BAS-038 and R-BAS-033, the Ingress annotations. Subject: the coarse limit in front of the Bastion, and the placement of artifacts
on the volumes.

Dormant until the Bastion is installed behind an ingress controller other than HAProxy Ingress, or behind HAProxy Ingress started with
`--disable-config-keywords` for `hash-type`; then live. The controller ignores an annotation it does not know and drops a snippet it
forbids, with a log line at most; the chart cannot see which controller reads its Ingress and renders no error. The edge limit is then
absent (the Bastion's own limits and blocking still hold), and with `routeByPath` each replica fills its own copy of every path
instead of one (performance only, the volumes together hold less). What makes it real: a different ingress class, or a hardened
HAProxy Ingress; NOTES.txt names the dependence, and the e2e run checks the annotations against the local profile's controller only.

### H-436 · Deleting a registration deletes its token rows, so a later use of a deleted token is unknown and not logged

Stage: `DELETE /api/v1/bastions/{name}` (R-BAS-004) and the rejection log of R-AUTH-023 step 4. Subject: the audit evidence of a
Bastion token that is presented after its registration was deleted.

Live on every deletion. The deletion revokes each token (one `token.revoked` with its journal entry each) and then removes the
registration, which removes its tokens by cascade. A replica that still holds the deleted token and presents it is told
`bastion_token_invalid`, as for a revoked one, but the main installation finds no row and writes no `token.rejected` for it (an
unknown token is only counted). The Bastion's own metric and alert (R-BAS-041) still show the refused token. What makes it real: a
leaked Bastion token that is used after its registration was deleted rather than its token revoked; revoking the token first and
deleting the registration later keeps the row for the retention (`auth.tokens.retention_after_end`) and the rejection log.

### H-437 · A Bastion token's last use is one value per token, at the granularity of the last-used buffer

Stage: R-BAS-004 (the registration's tokens) and R-BAS-006 step 4, with R-AUTH-031. Subject: the evidence that every replica of a
Bastion uses the new token before the old one is revoked.

Live on every registration with two tokens. `lastUsedAt` is written from the replicas' last-used buffers, one value per token, at most
once per `auth.tokens.last_used_granularity` (5 minutes by default): it shows that some replica used the token recently, not that every
replica did. The per-replica evidence is the last contact (`bastion_contact.token_id`, written by the handshake), which names the token
each replica used at its last handshake, up to `bastion.main.handshake_interval` old. What makes it real: an administrator who revokes
the old token on the strength of the new token's `lastUsedAt` alone, with a replica that has not reloaded its Secret; the screen of
R-BAS-007 shows the contacts next to the tokens, and a replica that still uses a revoked token gets `bastion_token_invalid` and treats
the main installation as unreachable (R-BAS-027).

### H-438 · The interface calls of a Bastion share the read budget of its token

Stage: the general limits of R-API-016 on the Bastion interface (Q-BAS-65). Subject: the availability of the check, the feed and the
handshake for a Bastion that serves many callers it has no answer for.

Live on every main installation with a Bastion and `api.rate_limits.enabled`. Every interface request, the POSTs included, is
charged to the class `read` under the bucket `token:<token ID>` of the Bastion token: 50 requests a second with a burst of 200 by default,
cluster-wide. A Bastion that meets a flood of callers it has no answer for asks one check per credential and repository, bounded by its
concurrency budget towards the main installation (R-BAS-025), which is far below that; beyond the budget the main installation answers `429`
and the Bastion treats it as the main installation not answering. What makes it real: a cold Bastion in front of thousands of distinct
credentials arriving within seconds, or a raised concurrency budget; `api.rate_limits.read` is the knob. The same budget bounds a
compromised Bastion token (H-386), which is a feature.

### H-439 · Download events enter the audit trail with the time the Bastion reports

Stage: R-BAS-016 and the keyset readers of the audit trail (R-OPS-017). Subject: consumers that follow the trail by `(occurred_at, id)`.

Live wherever `audit.record_downloads` is on and a Bastion reports. An `artifact.downloaded` event is stored with `occurred_at` set to the time
the Bastion gives, up to `bastions.downloads.max_age` (30 days) before its arrival, and the Bastion buffers for minutes: the event lands
behind the position of a consumer that already read past that instant (an export that pages by time, a SIEM that tails the trail) and that
consumer never sees it unless it re-reads. The holder of the Bastion token can place events anywhere in the last 30 days (H-386). The events
carry `origin: bastion` and the registration, so they are recognizable; the feed (R-BAS-015) reads no download event and is not affected.
What makes it real: a tailing consumer of `GET /api/v1/audit/events` or of the export that must see every download; a shorter
`max_age` narrows the window, and the consumer can filter on the registration and re-read the last window.

### H-440 · An address that sends rejected Bastion tokens is refused its valid Bastion token for a minute

Stage: the failed-credential throttle in the recognition (Q-BAS-65). Subject: the Bastion whose egress address is shared with a client that sends
rejected tokens to the main installation.

Live on every main installation with a Bastion, for a token the replica does not hold. The recognition counts a rejected `trv_bst_` token
against the address that sent it (the Bastion's egress for every request a Bastion sends, forwards included: the forwarded headers count
only after the token check, Q-BAS-116), in a budget of its own (the size of the failed-credential budget of R-AUTH-038: 300 a minute by
default). While the address is in throttled mode, a Bastion token that is in the replica's token cache, or whose marker says it was valid
recently, passes; one that is neither - after a restart of the main installation's replica, or once the cache entry aged out - is refused
`429` without a lookup, a valid one included. What makes it real: another workload behind the same egress address that sends 300 or more
rejected Bastion tokens to the main installation within a minute, or a Bastion whose own token was revoked and keeps calling;
`auth.rate_limit.exempt_cidrs` with the Bastion's egress address closes it, at the price of letting that address guess tokens.
### H-441 · `absent` rests on the notes of the core's lookups, not on the routes

Stage: R-BAS-013 and R-BAS-024, the marking of a `404`. Subject: a negative entry at the Bastion for an item that is merely
refused or blocked for everyone.

Live on every Bastion from the day the Mark stage is wired; it errs towards `pass`, so the live part is speed, and the dormant
part is correctness. Speed: a missing version of a package a local repository holds, and every route whose lookup goes through a
format's own data port (NuGet's registration and flat container, npm's version and dist-tag documents), is `pass`, so the
Bastion asks the main installation each time. Correctness: `absent` needs a note of a missing lookup and none of anything
unknown. A route that looks up an optional row (found is not an error), then answers a `404` for a reason of its own that is no
refusal and no serve-time check, would be marked `absent` for a `full` reader. Verified in this repository: the lookups of
`formathost` and `files`, and the `404` paths of the npm and NuGet routes that reach `Authorize` or `ServeCheck`. Not verified:
every `404` of every handler of both formats. What makes it real: a new format route of that shape; kit check 12 does not look
at `404`s.

### H-442 · A `401` states the subject as `invalid` whether the credential was rejected or none was sent

Stage: R-BAS-013, `Trove-Bastion-Subject` on a `401`. Subject: the Bastion's picture of its callers.

Live on every `401` of a recognized request. The header cannot tell a rejected credential from an anonymous caller who must
sign in (a repository that requires authentication, anonymous access off): both are `invalid`, because the Bastion has no use for
the credential either way. A Bastion that counted `invalid` subjects as failed authentications would count the anonymous
callers; the failed-authentication timeout of R-BAS-036 counts credentials the main installation rejected, which the Bastion
knows from the request it sent (a credential present) and not from this header. What makes it real: a change of the Bastion that
reads the header for that count.

### H-444 · Download events spooled by a draining replica can be delivered twice

Stage: R-BAS-039, the audit spool. Subject: the uniqueness of the main installation's download audit for downloads served by a Bastion.

Dormant until a Bastion drains during an outage of the main installation; then live with `audit.record_downloads: true`. Replicas
claim a spool file through the flight lock before they send it: with Valkey two replicas do not take one file, without it on a
bucket shared by several replicas nothing coordinates, and two replicas that list the file in the same moment both send it. The
events carry no ID the main installation could deduplicate on. A file of which some lines were delivered is replaced by a file with
the rest, written before the old one is deleted; a crash between the two sends those lines again. What makes it real: a drain
during an outage, the bucket backend without Valkey, several replicas becoming reachable together.

### H-445 · A Bastion clock ahead of the main installation's shortens the feed lookback

Stage: R-BAS-026, the synthetic cursor of Q-BAS-76. Subject: revocations that commit late.

Live on every Bastion. After a read without entries the cursor time is the Bastion's clock; the main installation reads from that
time minus `bastions.feed.lookback` in its own. A Bastion clock ahead by *s* leaves a lookback of *lookback − s* for a transaction
that starts before a poll and commits after it: a revocation whose transaction stays open longer than that is not returned by the
next read. A skew beyond the lookback is answered with `reset: true`, which is safe. What makes it real: a Bastion node whose
clock drifts by minutes together with a transaction at the main installation that stays open for minutes; rule 3 of R-BAS-020
assumes a bounded skew between replicas as well.

### H-446 · The connection to the main installation is not under the egress address policy

Stage: R-BAS-044, `bastion.main.url`. Subject: where the Bastion token and the callers' credentials are sent.

Live on every Bastion. The client is built by the shared outbound factory with an empty deny list: `bastion.main.url` is operator
configuration, as the object store endpoint is, and a main installation in a private range has to be reachable. The connect-time
policy that keeps an upstream registry from pointing into the cluster's own services does not apply; what protects the token and the
credentials is the verification of the main installation's certificate against the system roots and `ca_file`. What makes it real: a
DNS record for the main installation's host name that an attacker can change, together with a certificate for that name that the
roots or the bundle accept; the chart's egress policy (R-BAS-047) limits the connection to the main installation's address as well.

### H-447 · A background refresh keeps the token string beyond its request

Stage: R-BAS-020 (the refresh in the last fifth of a period, Q-BAS-84), R-BAS-019 (no token kept beyond the request). Subject: the
credentials of callers in steady use.

Live on every Bastion, for a caller whose `full` shape answer is in the last fifth of its period while the main installation is
reachable. R-BAS-019 says the Bastion keeps no token beyond the request. The refresh is one collapsed check sent after the request
that triggered it, and its goroutine holds the token string until the check ends: at most `bastion.main.response_header_timeout` plus
`bastion.main.queue_timeout`. The string is in the process's memory only, never in a log, a metric, the cache or Valkey. What makes it
real: a memory dump of a replica taken in that window, which is the same exposure as the request that is in flight. There is no setting
that closes it other than the refresh itself; a caller whose answer expires without a refresh waits for the check instead.

### H-448 · A chunked body of a read route that takes a body is held in memory

Stage: R-BAS-014 (the real request after a `204` probe, Q-BAS-81). Subject: the memory of a replica.

Live on every Bastion, for a request to a route the main installation answered `204 read-with-body` (npm's bulk advisory request) whose
body has no `Content-Length`. The Bastion needs a known length to forward it, so it reads up to `bastion.main.max_forwarded_body` into
memory before the request is sent, and answers `413` above it. A body whose length is announced is streamed and bounded by that length.
With `server.max_in_flight` such requests at once a replica holds up to that many times the limit (1024 × 8 MiB by default). What makes it
real: many clients sending chunked bodies to a read-with-body route, from addresses that pass the per-address limits; lowering
`max_forwarded_body` or `server.max_in_flight` closes it.

### H-449 · A range request costs a read of the whole object

Stage: R-BAS-022 and R-BAS-032 (Q-BAS-82). Subject: the bandwidth between a replica and its cache backend.

Live on every Bastion with cached artifacts. The bytes of a range are verified against the content address, so the object is read to its
end for any range. A caller who may read a large cached artifact can ask for many small ranges and make the replica read the object
each time; with the bucket backend that is egress from the bucket. The per-address limits (R-BAS-035) bound the rate, not the volume.
What makes it real: a reader of a large object and a client that ranges through it; `bastion.cache.max_object_bytes` bounds the object.

### H-450 · A fill that does not fit the spool is passed through unkept, and a request answered from the spool fails

Stage: R-BAS-021 and R-BAS-030 (the spool of a fill, [Q-BAS-131](#q-bas-131--the-bytes-of-the-fills-in-progress-are-bounded-and-the-chart-sizes-the-volumes-from-the-bound)). Subject: the large artifacts a replica keeps, and
the callers that ask for them with `Range` or a conditional header.

Live on every Bastion. The fills in progress of a replica hold together at most `bastion.cache.spool_max_bytes` (2 GiB by default) in
temporary storage, and the chart sizes the volume from it, so the pod is not evicted for a burst of cold requests for large artifacts. What
the bound costs: a fill that would pass it is passed through unkept and counted in `trove_bastion_cache_errors_total{op="spool"}`, so
under a burst of large cold artifacts the cache keeps fewer of them (a client retrying later finds the spool free); and a request with
`Range` or a conditional header, which is answered from the spool, answers `502 main_failed`; one whose spool fails during the transfer is cut (Q-BAS-123). A spool file lives until the last client of its fill is done, so a
slow client keeps its file for as long as it reads.
With the volume backend the spool is a directory of the cache volume: the chart checks that `cacheVolume.size` holds `max_bytes` plus
the spool, but nothing watches what else a volume holds, and a volume (`emptyDir`) filled by something else is still evicted. What makes
it real: several concurrent fills of objects near `max_object_bytes` (two of 1.5 GiB pass 2 GiB), or `spool_max_bytes` set to the
size of `max_object_bytes` while artifacts of that size are common.

### H-451 · A ranged read of a cached artifact is not verified

Stage: R-BAS-022's answer to `Range` and `If-Range` from the cache, and R-BAS-032. Subject: the integrity of the bytes of a ranged
download.

Dormant: the engine does not use `Cache.OpenRange`. It answers a range through the verifying read of the whole object (Q-BAS-82, H-449), so a
ranged download is verified. The gap is live for any consumer that calls `OpenRange`. The verifying reader checks the SHA-256
of a whole object; a range is a part of it and cannot be checked against the digest (the carve-out of R-STOR-013). Whoever can write
the bucket, or the volume of a replica, and replaces a cached object, makes a ranged reader receive the other bytes, where a whole
read aborts and deletes the object. The package managers the Bastion serves read whole files. What makes it real: a client that
resumes a download with `Range` after the object was replaced; the next whole read deletes the object.

### H-452 · The entries of evicted bytes stay until they are read and are not counted in the cache size

Stage: R-BAS-031, Q-BAS-88. Subject: the size of the bucket and the volume.

Dormant until phase 5b; then live on every Bastion. A sweep deletes bytes and markers and does not read entries, so the entry of an
evicted object (about 1 KiB) stays until a request reads it, and the entries are not part of `trove_bastion_cache_bytes`. The number
of entries is bounded by the distinct cache keys the main installation served (path and Vary values), not by a caller. What makes it
real: a catalog with millions of keys and a short working set, where entries outgrow what `max_bytes` suggests; the revisit
condition of Q-BAS-88 names it.

### H-453 · Objects and markers are not signed: a replaced object is found only when it is read, and markers steer eviction

Stage: R-BAS-030 to R-BAS-032 below the entries. Subject: the integrity of bytes at rest and the order of eviction.

Dormant until phase 5b; then live on every Bastion. Only entries are signed; the bytes are checked against their address when they
are served, and the access markers and modification times that order a sweep carry no signature. A holder of write access to the
bucket (or of a replica's volume) who replaces an object leaves it until the next read, which aborts and deletes it; one who
refreshes the markers of worthless objects makes a sweep evict the useful ones instead. Neither lets the Bastion serve other bytes;
both cost a refill. What makes it real: the compromise of the Bastion's bucket credential (H-387's setting, without the key).

### H-454 · The gauge of blocked addresses counts the blocks a replica knows

Stage: R-BAS-037, `trove_bastion_blocked_addresses`. Subject: the number an operator reads.

Live on every Bastion with the guard. The gauge is the number of blocks that the replica set or served; with Valkey a block set by
another replica enters the count of this one when a request from the address reaches it. A replica that has not seen the address does
not count it, and the sum over the replicas counts a block once per replica that knows it. The block itself is not affected. What
makes it real: reading the sum or the maximum of the gauge as the number of blocked addresses; Valkey has no command for the size of
the family that the Bastion's ACL user may run.

### H-455 · Every request costs the Bastion's Valkey round trips, and an unblocked flood from many addresses costs it per request

Stage: R-BAS-034 to R-BAS-037. Subject: the load a request flood puts on the Bastion's Valkey.

Live on every Bastion with Valkey. A request of an address that is not blocked asks Valkey for the block (one `GET`) and for the limiter
(one `MULTI`/`EXEC`), a miss one more, a refusal two more; a blocked address costs none, the block is held in the replica's memory. A flood
from many addresses, each below its limits, costs Valkey two round trips per request. The breaker of R-ARCH-031 opens after repeated
failures and the guard runs from memory until it closes, so a Valkey that cannot keep up costs the shared counters, not the service; the
coarse limit at the ingress (R-BAS-038) bounds the request rate before the pods. What makes it real: a flood of the size of the edge limit
from thousands of addresses against a Valkey sized for the main installation's load; the first answer is the ingress limit and a Valkey of
the Bastion's own.

### H-456 · A block or a timeout set without Valkey, or while it is unavailable, is forgotten when the replica restarts

Stage: R-BAS-034 and R-BAS-037. Subject: how long a block lasts.

Live on every Bastion without Valkey, and for blocks set while its Valkey is unavailable. The block list, the level that doubles the
duration and the counters of such a replica are in its memory; a restart (a rollout, a crash, an eviction) empties them, so an address
blocked at level 4 for 2 hours returns at once and its next block lasts 15 minutes. A block set while Valkey is unavailable is not written
to Valkey when it returns. What makes it real: an attacker who outlasts one replica's lifetime; Valkey closes it.

### H-457 · A reader without endpoints:read is not offered the Bastion as a target

Stage: R-BAS-007, the **Install from** step of the setup page and of the guides. Subject: a developer who may read a repository and
should install through a Bastion.

Live on every installation with a registered Bastion. The offer is built from `GET /api/v1/bastions`, which needs `endpoints:read`
(administrators, security administrators, auditors and repository administrators); a developer does not see the choice, and learns
of the Bastion from the documentation of the team or from a link with `?bastion=<name>` that an administrator sends, which works for
every caller. Nothing is exposed by this, and nothing is hidden by it either: the operation behind the link needs only `read`. What
makes it real: a team that expects the page to show the Bastion to everyone; a list of Bastions for every caller would need an
operation that discloses registration names and addresses to every caller, which this design does not add.

### H-458 · The address a registration reports chooses among the listed addresses, and the name is testable

Stage: R-BAS-007, `client-setup` with `bastion=<name>`. Subject: the address in the snippets a reader copies, and the names of the
registrations.

Live on every installation with a registered Bastion. The address comes from the registration's last handshake, which the holder of
the Bastion token controls (H-386), and it is rendered only when it is an entry of `server.additional_base_urls`. A holder of a token
can therefore make the snippets of that registration name any listed address, for example the address of another Bastion or of an
alias of the main installation, so a reader who chose that registration is sent to a listed address that is not the Bastion they
meant; no other address can reach a snippet. Separately, the answers `404`, `409` and `200` tell any caller who may read a repository
whether a registration of that name exists. What makes it real: a stolen Bastion token and a listed address that a reader trusts for a
different purpose; an attacker who guesses names (at most 16 registrations, each name matching `^[a-z][a-z0-9-]{0,19}$`) learns only
that a name exists, never a token or a contact.

### H-460 · Without a signing key the entries on the volume are not authenticated

Stage: R-BAS-032, Q-BAS-98. Subject: which bytes an entry names.

Live on every Bastion with the volume backend and no Valkey that has no `signing_key_file`; dormant otherwise (the bucket and Valkey require the key). The entry names the content address
and the headers of a cached response and is stored as plain JSON. Whoever can write the volume can point an entry at another object of the volume, or change its headers and its
`validatedAt`, so that a caller receives other cached bytes under the name of the first, or an entry that never expires. The bytes themselves are verified against their content address at every
read, and a replaced object is detected. What makes it real: write access to the volume of a replica, which is a compromised pod or a mounted volume that something else writes to; the
chart gives the volume to one pod.

### H-461 · The suite's front reaches the main installation's replica in plain HTTP

Stage: R-BAS-044, R-BAS-048. Subject: the hop between the main installation's ingress and its web replica.

Live in `test/integration/bastion` only; dormant in a deployment, where the chart's Service and Ingress carry the TLS of the main chart (R-HA-063). The Bastion's own connection is TLS and verified
against the front's CA; the hop from the front to the replica is not. What makes it real: a defect in the main installation's own TLS listener that only shows with a Bastion's headers (the
forwarded scheme of a Bastion request arriving over a TLS peer). The listener's TLS is tested by `test/integration/tls`.

### H-462 · A main installation that reports a higher interface version in its own problem body is not exercised

Stage: R-BAS-009, R-BAS-028. Subject: the version the Bastion learns from a `400 bastion_interface_mismatch`.

Live in the suite (Q-BAS-105): the front simulates the foreign version in the answer's header and in the request, so the Bastion reads the main installation's version from the header and a real `400`
carries the real main installation's own version. A `400` whose `interfaceVersion` member names a higher version than the header does is covered by the unit tests of `mainclient`, not by an in-process
main installation. What makes it real: a main installation of a later release that moves the member and the header apart.

### H-463 · The bound on the upload body a Bastion reads is shown by the answer, not by a counter

Stage: R-BAS-014. Subject: how much of an upload body the Bastion consumes before it refuses.

Live in the suite. The Bastion has no counter of request-body bytes, so the test announces 4 MiB, sends 1 KiB, and requires the refusal to arrive before the rest is sent; it proves the refusal does
not wait for the body, and bounds what was read by what was sent (1 KiB), not by what the Bastion's socket buffers took in a long upload. What makes it real: a change that reads the body after
answering, which this test would not see.

### H-467 · Behind the main installation's own ingress the forwarded host is what the ingress reports

Stage: R-BAS-017, R-OPS-005 (the response base URL). Subject: which entry of `server.additional_base_urls` - the Bastion's host among
them - a response is rendered on.

Live where the main installation's ingress passes on an `X-Forwarded-Host` (or the `host=` of `Forwarded`) that a client sent; dormant
where it overwrites them with its own. The client address of a request counts a Bastion's entry only when the request was recognized
and came from an address of `bastions.trusted_proxies` (Q-BAS-116), but the forwarded host is read from the right-most entry whenever the
TCP peer is a trusted proxy, as for every request (R-OPS-005): behind an ingress that keeps a client's header, any client of the main
installation can have its documents rendered on the Bastion's host, as on any other additional base URL. It changes where links point,
not what a caller may read. What makes it real: an ingress configured to pass the client's forwarded host; the ingress's own setting
that overwrites it closes it.

### H-468 · The download budget is per registration, so many registrations at their budget still fill the audit buffer

Stage: the download intake (R-BAS-016, Q-BAS-117). Subject: the asynchronous audit buffer of the main installation's replicas and the
events other areas record asynchronously.

Dormant with one or a few registrations; live when many registrations report at their budget together. Each registration may report
`events_per_second` events (200 by default) across the replicas, so the default maximum of 16 registrations may write 3,200
`artifact.downloaded` events a second. A replica whose `audit.async_buffer` (10,000) is full sends further asynchronous events to the
log copy only. What makes it real: many Bastion tokens - legitimate Bastions under load, or a holder of several tokens (H-386) - reporting
at once; a lower `events_per_second` bounds it.

### H-469 · A probe admitted for routing runs any handler below the package families that does not tell the marking its route

Stage: the authentication of a recognized probe (R-BAS-014, Q-BAS-118). Subject: what a holder of the Bastion token can run as the
anonymous caller while anonymous access is off.

Dormant: every route below `/api/<format>/` and `/files/` today is the format dispatcher's or the raw file handler's, which call the
marking stage's observer after the route match and before any decision, handler or body read, and a probe never passes that call. A
handler mounted below those prefixes that does not call it would run for a probe as the anonymous caller - the decision point still
refuses the anonymous caller everything anonymous access off forbids, but a handler that decides nothing would serve. What makes it
real: a new route below the package families outside the dispatcher; a test of the route table that every pattern below them is the
dispatcher's or the raw file handler's would hold it.
### H-470 · The Bastion assumes the feed's default lookback when it keeps the IDs of applied entries

Stage: R-BAS-026, `answers.FeedLog`. Subject: the entries a Bastion applies once.

Live on every Bastion whose main installation sets `bastions.feed.lookback` above its default of 5 minutes (the key allows up to 1 hour), and dormant otherwise. The Bastion keeps the ID of an applied entry
for `answers.period + grace_period + 15 s` and at least 5 minutes plus 15 s, so that an entry the feed sends again is not applied again. With a longer lookback and a short period, the feed sends an entry
again after the ID is forgotten, and it is applied again: it is recorded with a new receipt time, so every answer obtained since the first receipt is unusable once more. That costs the main installation
requests; it cannot serve anything that should not be served. What makes it real: a lookback of 30 minutes and `answers.period: 1m`, `grace_period: 1m`.

### H-471 · A pass-through body at the floor rate holds its budget slot for the length of the transfer

Stage: R-BAS-025, the pass-through of a response that is not kept (Q-BAS-123). Subject: the slots of the budget.

Live on every Bastion. A fill frees its slot when the main installation's body ends; the body of a pass-through (an answer above `max_object_bytes`, one without `Content-Length`, a document above the
limit) is read at the pace of the client and holds the slot meanwhile. A client that takes each chunk within 10 s and averages 64 KiB per second after the first 30 s is not cut, so a reader at that rate of
an object of 2 GiB holds one slot for about nine hours. Readers from many addresses can take the share of anonymous callers this way (H-388 is the same budget). The Bastion does not cut by total time,
because it would cut a large download on a slow link. What makes it real: an attacker with a credential, or anonymous access on, a repository that serves an object above the limit or without a length,
and one connection per slot.

### H-472 · A fill goes on after its client has left

Stage: R-BAS-021, `engine.fillRun`. Subject: a slot of the budget, the spool, and the main installation's bandwidth.

Live on every Bastion. The fill of an artifact is independent of the client that started it, so a client that asks for a cold object and disconnects leaves a fill that reads the whole object and commits it,
which is what makes the next request a hit. An attacker who sends cold requests for distinct large objects and disconnects makes the Bastion fetch them. What bounds it: the per-address request and miss limits, the concurrency budget (a
fill holds a slot until its body ends), `bastion.cache.max_object_bytes` and a main installation that sends no byte for 60 s is abandoned. What makes it real: an attacker below the limits who names objects
that exist, from many addresses.

### H-473 · The role's binary contains packages of the main installation it never starts

Stage: R-BAS-001 (the dependency closure of the role, [Q-BAS-129](#q-bas-129--the-import-rule-follows-the-dependency-closure-and-lists-what-it-cannot-narrow)). Subject: the code in the Bastion's image.

Live on every Bastion. Through `internal/cluster` (the Valkey client shares a package with the change log and the locks), `internal/httpserver` (the request context carries the audit source and the principal's scope and CI claims) and
`internal/blob/s3` (the S3 client implements the blob store interface) the binary contains the PostgreSQL store (`internal/meta`, `meta/postgres`, `meta/postgres/q`, `backup/tables`), `internal/formats/catalog`, `internal/audit`, `internal/auth/scope`,
`internal/ci/claims`, `internal/ci/reason` and `internal/blob`. The role opens no pool, no object store of the main installation and builds no authorization engine, and a test starts it with a trap where PostgreSQL would be. The archtest rule lists these packages with their
importer and fails on any other. What makes it real: a change that calls into one of them from a path the Bastion runs (the rule would not see a call inside an allowed package), and the size of the attack surface that code is: a flaw in a package the role never
starts is still in the image. The fix is to split the three platform packages so that the Bastion imports their narrow halves; the exceptions leave the list as it happens.

### H-474 · The sliding estimate of a limit admits more than the limit in a minute that spans a boundary

Stage: R-BAS-035 (the miss limits, and the request limits with Valkey, [Q-BAS-127](#q-bas-127--a-refused-request-or-miss-does-not-consume-the-limit-it-was-refused-by)). Subject: the number of requests to the main installation per address and minute.

Live on every Bastion. The estimate is `previous x (1 - elapsed) + current` over the admitted events of the window pair, the form of R-ARCH-034. An address that sends its whole budget just before a window boundary and continues after it is admitted
`current <= limit x elapsed` more events in the next window, so a true sliding minute that spans the boundary can hold up to twice the limit (a measured cold start with a 30-a-minute limit: 37 in 60 s). The counted-in-fixed-window bound of the acceptance holds (at most the limit
per window), and the concurrency budget (R-BAS-025) bounds what reaches the main installation either way. Before Q-BAS-127 the refusals also counted, which hid the effect behind starvation. What makes it real: an attacker who times its bursts to the boundary
(window = one minute for the misses; `burst / rate` for the request limits), gaining at most one more budget per boundary; a smaller error needs more counters per key.

### H-464 · A client that sends `Accept-Encoding: gzip` was asked about at the main installation on every artifact read, cause not found

Stage: R-BAS-022, a read of a cached artifact. Subject: the load on the main installation, not a confidentiality property.

Observed once by the end-to-end run (`TestBastion_R_BAS_049_RoutingByPath` before it used `curl` from a client pod): 36 tarballs requested twice by the Go client of the test
machine, which sends `Accept-Encoding: gzip` by default, caused 72 conditional requests (every one answered `304`) and no hit, on the volume Bastion, while the same paths requested
without that header by `curl` caused one conditional request per stale path and hits afterwards (a probe of one path, and a loop of 20 requests, checked in the cluster). The package
managers of the suite (npm, pnpm, Yarn 4, `dotnet restore`) were served from the cache as the acceptance requires. The cache key carries the values of the headers the main
installation's `Vary` names (R-BAS-018); the cause is probably a variant that is keyed by `Accept-Encoding` and never becomes fresh, and it was not found. Live for a client that sends the header
and reads a cached artifact: it costs the main installation one conditional request per read, never the bytes. What makes it real: a client built on a library that compresses by default
and reads the same artifacts many times; the Bastion's concurrency budget (R-BAS-025) bounds what reaches the main installation.
