# Endpoints and exposure rules

An endpoint is a separate client listener of trove with its own port, host names, certificates and rules, for example a listener that customers reach on the internet while employees use the main one. This page explains what an endpoint decides, how to configure one with the Helm chart, how exposure rules limit what an endpoint serves, how to manage those rules on the Endpoints screen, and what npm clients see through a customer endpoint.

## What an endpoint is

Every installation has the default endpoint: the client listener on port 8080 with the web UI, the management API, package clients and file transfer. Each further endpoint is another listener in the same `api` and `packages` processes, each opening it with the route families of its role. A request belongs to the endpoint whose port it arrived on; no header, host name or path can move it to another endpoint.

| Property | What it decides | Default for a further endpoint |
|---|---|---|
| Port | the listener's own port; not 8080, 8081 or another endpoint's | required |
| Base URL | the origin trove writes into URLs it returns, such as npm tarball links | required |
| Hosts | the exact host names it answers; one wildcard host such as `*.packages.example.com` (matches one label) | the host of the base URL |
| Certificates | its own pairs, chosen by SNI | the pair of the client listener |
| Surface | which route families it serves: `ui`, `api`, `ci_exchange`, `packages`, `files` | `packages`; `ui` is refused |
| Accepted credentials | token types, whether tokens must be bound to it, anonymous access | service-account tokens bound to it, no anonymous access |
| Exposure | `rules` (only what its exposure rules select) or `all` | `rules` |
| Cache fill | whether a request may make remote repositories contact their upstream | off |
| Rate limit class | a class of `server.endpoint_rate_limits` | `external` |
| Replica header | when `X-Trove-Replica` names the answering replica: `authenticated`, `always`, `never` | `never` |
| Concurrent requests | per replica and endpoint; over it the endpoint answers `503 server_busy` while other endpoints are unaffected | 1024 |

A path of a family outside the surface answers `404`, the same as an unknown path. Generic repositories are served under `/files/`, so an endpoint that should serve them needs `files` in its surface; npm is in `packages`. The `ci_exchange` family is reserved and has no working route yet.

### Host check and 421

A further endpoint answers only its own hosts. A request for any other host, an IP address or no host answers `421` with the problem code `misdirected_request` before authentication, and is counted in `trove_endpoint_misdirected_total`. Behind a trusted proxy (`server.trusted_proxies` or the endpoint's `trustedProxies`) a forwarded host is used when it is one of the endpoint's hosts and ignored otherwise.

The default endpoint checks hosts only when hosts are configured for it. The chart does that as soon as a further endpoint exists: it lists the hosts of `config.server.base_url` and `additional_base_urls`, so a request for a customer host that reaches port 8080 by mistake answers `421`. If clients reach the default endpoint by another name, for example the in-cluster Service name, add it to `endpoints.default.hosts`.

## Configure endpoints with the Helm chart

Endpoints come from the configuration; there is no API or screen that creates one. With the chart, use the `endpoints` values (the chart renders `server.endpoints`; setting that key under `config` fails the render). Up to 16 further endpoints are allowed. An endpoint name matches `^[a-z][a-z0-9-]{0,19}$`.

```yaml
endpoints:
  customers:                            # example: the endpoint name
    port: 8443                          # example; required
    baseUrl: https://packages.example.com   # example; required
    hosts: []                           # default: the host of baseUrl only
    wildcardHost: ""                    # default: none; example "*.packages.example.com"
    surface: [packages]                 # default
    auth:
      tokenTypes: [sat]                 # default: pat | sat | cit
      boundTokensOnly: true             # default
      anonymous: false                  # default
    exposure: rules                     # default: rules | all
    cacheFill: false                    # default
    rateLimitClass: external            # default
    replicaHeader: never                # default
    maxInFlight: 1024                   # default
    tls:
      existingSecret: trove-packages-tls    # example; default "" = the pair of tls.secretName
      extraSecrets: []                  # default; further pairs chosen by SNI, example [trove-packages-wildcard-tls]
      clientCaSecret: ""                # default: no client certificates
    pool:
      enabled: true                     # example; default false = served by the main web pods
      replicaCount: 2                   # default when enabled
    ingress:
      enabled: true                     # example; default false
      className: haproxy                # example; default "" = the cluster's default class
      tls:
        - secretName: packages-edge-tls # example
          hosts: [packages.example.com] # example
    networkPolicy:
      from: []                          # default: empty = from anywhere
tls:
  ops:
    secretName: trove-ops-tls           # example; keeps the main listener's key out of pool pods
config:
  server:
    endpoint_rate_limits:
      external: {requests_per_second: 50, burst: 200, key: principal}   # default; key: principal | address
```

The key `hostRoots` maps an exact host of the endpoint to a repository's qualified name, `<projectKey>/<repoKey>` (for example `{pkgs.example.org: web/docker-hub}`); for an OCI repository the host's `/v2/` then is the registry of that one repository, which a Docker Engine can use as its `registry-mirrors` entry ([Container image repositories](container-repositories.md#the-docker-engine-mirror-on-a-bound-host)); a binding to a repository of another format, or to one that does not exist, has no effect. The host must be one of the endpoint's `hosts`, and a value that is no qualified name fails the start. The complete list, including the Service and Ingress keys, is in the [full reference](https://github.com/guided-traffic/trove/blob/main/README.md#-full-reference).

### What the chart creates

| Object | Name | Notes |
|---|---|---|
| Service | `<fullname>-ep-<name>` | the endpoint's port; selects the main pods of the roles whose families the endpoint serves, or the pool's |
| Deployment and PodDisruptionBudget | `<fullname>-packages-ep-<name>`, `<fullname>-api-ep-<name>` | with `pool.enabled`; only these pods open the endpoint's port |
| Ingress | `<fullname>-ep-<name>` | with `ingress.enabled`; one rule per host; HTTPS to trove as for the main Ingress |
| NetworkPolicy | `<fullname>-ep-<name>` | the endpoint's port from `networkPolicy.from` |

A pool keeps external load off the internal pods, but its pods hold the same database, Valkey and object-store credentials as every `api` and `packages` pod. The installation notes printed by `helm install` warn about an endpoint in mode `rules` without a pool and about an empty `networkPolicy.from`.

### The default endpoint

The entry `endpoints.default` tunes the default endpoint and may set only `hosts`, `wildcardHost`, `hostRoots`, `surface`, `auth`, `exposure`, `cacheFill`, `rateLimitClass` and `replicaHeader`. Its own defaults are all five route families, token types `pat`, `sat` and `cit`, unbound tokens, anonymous access allowed, exposure `all` and cache fill on. Its port, certificate and concurrency come from `server.listen`, `tls.*` and `server.max_in_flight`.

## Accepted credentials

Sessions from the web UI are accepted only where the surface holds `ui`, which is the default endpoint. On every endpoint the credential rules are checked in this order:

1. A token type outside `tokenTypes` (for example a personal access token on `customers`) is refused.
2. A token bound to other endpoints is refused; with `boundTokensOnly`, an unbound token is refused too.
3. A request without a credential is refused unless `anonymous` is `true`.

A refused credential answers `401` exactly like an unknown token, so the answer does not reveal that the token is valid elsewhere. The refusal is counted in `trove_endpoint_credentials_refused_total` and recorded in the audit log as `token.rejected` with the reason `endpoint_not_accepted`.

### Bind a token to an endpoint

A token's scope has a list of endpoint names; an empty list means unbound. The token screens of the web UI do not offer the binding yet. Create the token through the management API, for example `POST /api/v1/service-accounts/{name}/tokens`, or `POST /api/v1/projects/{projectKey}/service-accounts/{name}/tokens` for a service account of a project, with `"scope": {"endpoints": ["customers"], ...}`, or with `trove admin token` and a scope file. A token bound to `customers` works only there, so a leaked customer token is useless on the default endpoint.

## Exposure rules

On an endpoint in mode `rules`, exposure rules are a ceiling on what anyone may read through it. They never grant anything: a caller still needs a read permission on the content, and the rules then decide what of it the endpoint shows.

- **Everyone is bound.** Administrators, the break-glass administrator and anonymous callers are all limited by the rules of the endpoint they use.
- **Hidden is missing.** Content no rule exposes answers the `404` of a missing resource, decided before the repository is looked up, so a hidden file, a missing file and an unknown repository look the same. The refusal is recorded as `authz.denied` with the reason `deny.exposure` and counted in `trove_exposure_hidden_total`.
- **Read-only.** Every write on such an endpoint answers `403 endpoint_read_only`, whether or not the target exists.
- **Rules add up.** A rule belongs to one endpoint and one repository; content is exposed when any enabled rule admits it. Inside a rule, an exclude pattern wins over an include, and an empty include list leaves that dimension open. A rule with no patterns exposes the whole repository.
- **Listings follow.** Repository lists on the endpoint show only repositories with an enabled rule.
- **Formats.** Generic repositories are narrowed by paths; npm repositories by package names, versions, version range, pre-releases and dist-tags; NuGet repositories by package IDs, versions, version range and pre-releases (NuGet has no tags). A repository of a format without exposure support takes no rule and serves nothing on such an endpoint.

An endpoint in mode `all` has no ceiling; its rules have no effect. At most 1,000 rules can exist per endpoint.

### Rule fields

| Field | Meaning |
|---|---|
| **Name** | lower-case letters, digits, dot, underscore and hyphen, up to 64 characters; fixed after creation |
| **Repository** | the repository the rule is about, named in the API by its qualified name `<projectKey>/<repoKey>` (a bare key, a pattern or a class is refused with `invalid_pattern`); fixed after creation. The rule holds the repository itself: after a move to another project it keeps applying, and reads back the new name. For a virtual repository, the rule applies to what is requested through it |
| **Description**, **Enabled** | free text; a disabled rule exposes nothing |
| **Package names** | include and exclude patterns (formats with package names) |
| **Version range** | in the format's syntax: for npm a node-semver range such as `>=2.0.0`, for NuGet the interval notation of its dependency ranges, such as `[2.0,4.0)`; a bare `2.0` means 2.0 or higher, and floating versions such as `1.*` are refused |
| **Versions** | include and exclude patterns for exact versions. They are compared letter by letter with the version in its canonical form; for NuGet that is the normalized lower-case form without build metadata (`2.1.0-beta.1`, never `2.1`, `2.1.0.0` or `2.1.0-Beta.1`), so use the version range when you are not sure of the spelling |
| **Expose pre-release versions** | off by default: pre-releases stay hidden even inside the range |
| **Tags (formats with tags only)** | include and exclude patterns for dist-tags |
| **Paths** | include and exclude patterns (generic repositories) |

Pattern syntax for names: `*` matches any run of characters except `/`, `**` any run including `/`, `?` one character, `\` makes the next character literal. For paths, `*` and `?` stay within one segment and `**` as a whole segment matches any number of segments. Path patterns on an npm repository draw a warning: they can admit a tarball but not the package document that lists it, so prefer package-name patterns.

## The Endpoints screen

Open **Admin area › Endpoints** (capability `endpoints:read`, held by the roles `admin`, `security-admin`, `auditor` and `repository-admin`). The list shows each endpoint with **Base URL**, **Hosts**, **Surface**, **Exposure**, **Rules (enabled)** and **Pool**. An endpoint in mode `all` is marked "all (rules have no effect)". A name that is no longer configured but still has rules is marked **not configured**: its rules have no effect and can only be read and deleted.

Select an endpoint to see its read-only **Configuration** (including **Accepted credentials** and **Rate limit**) and its **Exposure rules**. The rule table shows the patterns, versions, pre-release setting, an **Enabled** switch, and the **Client address** of the repository on this endpoint with a copy button. "This endpoint does not serve this address." means the endpoint's surface lacks the route family of that repository's format.

### Create or change a rule

Changing rules needs `endpoints:write` (roles `admin` and `security-admin`) and a recent sign-in: if your last interactive sign-in is older than `auth.sessions.recent_auth_window` (1 hour by default), trove asks you to sign in again and keeps your draft.

1. On the endpoint, select **New rule**, or select a rule's name or **Edit**.
2. Choose the **Repository** and enter a **Name**. The form shows the dimensions of that repository's format.
3. Fill in the patterns, and for npm the range, pre-releases and tags.
4. Watch the **Preview** on the right (see below).
5. Select **Create rule**, or **Review and save** for an existing rule, which shows the difference to the stored rule before saving. If someone else changed the rule meanwhile, trove shows both versions and lets you reload or overwrite.

To remove a rule, use **Delete** in the table or **Delete this rule** in the editor; content that only this rule exposes stops being retrievable on the endpoint. To pause a rule, turn its **Enabled** switch off.

### Check what an endpoint exposes

The **Preview** evaluates the rule as it is in the form, saved or not, together with the endpoint's other stored rules, half a second after your last change. It reads only rules: no content is transferred and no upstream is contacted. It needs `read` on the repository.

- For npm, enter a **Package to test** and, optionally, **Versions to test (one per line, empty for the stored ones)**. With versions entered, only those are judged. With none, trove judges the versions the repository stores of the package, newest first (at most the newest 1,000), and only for a package you may read (a package you may not read looks like one the repository does not hold, and so do its versions and tags): a hosted repository lists its live versions (a trashed or unpublished version is not one), a remote repository the versions of its cached package document. A virtual repository stores nothing of its own, so the preview says it cannot list and asks you to name versions or to check a member repository. If versions or tags are cut, the preview says so; the version each tag points to is always judged. The preview also lists the package's **dist-tags** (a hosted repository only) and whether each is visible: a tag is visible when a rule admits its name and the version it points to is exposed.
- For a generic repository, enter **Paths to test (one per line)**.

The result says whether the repository or package is exposed and, per path or version, the verdict, the deciding rule and the reason, such as "no rule admits it", "the version is outside the range" or "pre-releases are not exposed". Up to 1,000 paths or versions can be checked at once. The same check is available as `POST /api/v1/endpoints/{endpoint}/check-exposure`; an absent or empty `versions` list means the stored versions, and the answer carries `distTags`.

## npm through a customer endpoint

An npm client configured with the endpoint's client address (the base URL plus `/api/npm/<project>/<repository>/`) sees only what the rules expose:

- Package documents list only exposed versions. `time`, `maintainers` and the dist-tags follow; a tag that points at a hidden version disappears, and `latest` moves to the highest exposed release (or the highest exposed pre-release when there is none). The README of a hidden former `latest` is not shown.
- Every URL in a document, tarball links included, uses the endpoint's base URL host, never the default endpoint's.
- A hidden version, a hidden tarball, a dropped tag and a package without an exposed version answer `404` like a missing one.
- Publish, unpublish and dist-tag changes answer `403 endpoint_read_only`.
- With cache fill off (the default), remote repositories serve only what their cache already holds, and an item that is not cached answers `403 populate_not_permitted`; a virtual repository skips such members. Fill the cache through the default endpoint, for example by installing the packages there. Each prevented upstream contact is counted in `trove_endpoint_cache_fill_refused_total`.
- Answers carry no `Trove-Resolved-From` and no `Cache-Status` header, so a customer does not learn member names or cache state.
- `npm audit` answers `501` on such an endpoint, because forwarding the audit would reveal which packages the remote has fetched.

The package-level times `time.created`, `time.modified` and the `Last-Modified` header keep their real values, so a customer can tell that a package changed even when the change is hidden.

## NuGet through a customer endpoint

A NuGet client configured with the endpoint's feed (the base URL plus `/api/nuget/<project>/<repository>/v3/index.json`) sees only what the rules expose:

- The version list (`flatcontainer/<id>/index.json`), the registration index, its pages and its leaves, in all three hives, list only exposed versions. The paging of the registration follows the exposed versions, so the page bounds and counts are those of the filtered list.
- Search lists only exposed packages and versions. `version` is the latest exposed version that the request's filters leave, `totalHits` counts exposed packages only.
- A hidden version, its `.nupkg` and `.nuspec`, and a package without an exposed version answer `404` like a missing one. The NuGet client reports `NU1102` for a version it asked for and that is hidden, and "unable to find package" for a package that is hidden. Dependencies in a package are not filtered: a package that depends on an ID the endpoint hides fails the restore, use **Check exposure** on the dependency's ID beforehand.
- The service index lists no publish and no symbol publish resource. `dotnet nuget push` against the endpoint's `index.json` therefore stops in the client; a push, an unlist or a relist sent to the package URL directly (for example with `curl`) answers `403 endpoint_read_only`. Push through the default endpoint.
- Every URL in a document uses the endpoint's base URL host. A change to a rule changes the validators of the documents, so a client revalidating a cached document gets the new one.
- Pre-releases stay hidden unless the rule exposes them, whatever the client's `prerelease` setting says.

## How fast a change takes effect

Rules live in PostgreSQL. A created, changed or deleted rule reaches every replica through the database within about the change-log poll interval plus one second (2 seconds with the default `cluster.epoch_poll_interval` of 1 s), also while Valkey is unavailable. A replica that cannot confirm its rules are current answers `503 authz_stale` instead of deciding on old ones. Every change is audited as `endpoint.exposure.created`, `endpoint.exposure.updated` or `endpoint.exposure.deleted`; deleting a repository deletes its rules and records one `endpoint.exposure.deleted` per rule.

## Things to keep in mind

- **Rules belong to an endpoint name.** If you remove an endpoint and later configure another one under the same name, the old rules apply to it at once. Review or delete the rules of an endpoint marked **not configured**; trove also logs a warning at start for them.
- **Virtual repositories.** A rule on a virtual repository applies to whatever its members hold. A member added later can expose new packages that match the rule's patterns, if the endpoint's callers may read that member. Prefer exact package names and grant customer service accounts read on specific repositories.
- **Traffic on the wrong port.** Requests that reach port 8080 have the default endpoint's rules, which by default expose everything the caller may read. Route each external host only to its endpoint's Service, keep the chart's default host list, and restrict port 8080 with `networkPolicy.ingress.http.from`.

For the token types and service accounts, see [Managing tokens](tokens.md); for read permissions, see [Permissions](permissions.md).
