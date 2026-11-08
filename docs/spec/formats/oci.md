# OCI and Docker registry

Status: specification — not implemented (phase T-16, FULL.md block B F4): local, remote (modes `pullThrough` and `mirror`) and
virtual repositories for container images and other OCI content, Docker CLI and Podman as tested clients

This page specifies how trove serves container images: the OCI distribution API under `/v2/` for local, remote and virtual
repositories of the format `oci`, which serves Docker image manifests and OCI manifests alike; the registry's token service and
how it maps onto trove tokens and permissions; blob uploads over several requests and replicas; manifests, image indexes and
multi-platform images; tags, digests and the referrers API; deletion and the collection of unreferenced blobs; proxying Docker
Hub and other registries, in both modes of a remote repository; what a mirror of a registry discovers and keeps; virtual
resolution of image names; the bound host of a Docker Engine mirror; the limits on untrusted content; operation on
interchangeable replicas; and the test suites. Mechanisms shared by all formats are owned by other pages and only refined here:

| Topic | Owner |
|---|---|
| Repository model, virtual resolution, remote request path, mirror engine, bounds, throttling | [repositories-and-proxy.md](../repositories-and-proxy.md) (R-REPO-001 to R-REPO-060) |
| Items, blobs, the upload pipeline, multi-request upload sessions, garbage collection, quotas | [storage.md](../storage.md) (R-STOR-003 to R-STOR-011, R-STOR-024, R-STOR-039) |
| Credential sources, token types, challenges, anonymous access | [identity-and-authentication.md](../identity-and-authentication.md) (R-AUTH-001, R-AUTH-002, R-AUTH-022, R-AUTH-023, R-AUTH-037) |
| Actions, evaluation, ownership, populate rules, virtual gate | [authorization.md](../authorization.md) (R-AUTHZ-005 to R-AUTHZ-009, R-AUTHZ-015, R-AUTHZ-016, R-AUTHZ-018) |
| Format contract, listeners, routes, the `/v2/` reservation, untrusted input, jobs | [architecture.md](../architecture.md) (R-ARCH-005, R-ARCH-008, R-ARCH-009, R-ARCH-016, R-ARCH-021, R-ARCH-028) |
| Endpoints, host-root bindings | [endpoints.md](../endpoints.md) (R-EP-005 to R-EP-007) |
| Edge routing of `/v2` to the role `packages`, proxy requirements | [ha-and-helm.md](../ha-and-helm.md) (R-HA-037, R-HA-039, R-HA-041) |

Covers: [formats/docker.md](../../features/formats/docker.md) (`PKG-DOCKER-*`) and [formats/oci.md](../../features/formats/oci.md)
(`PKG-OCI-*`); the section [Catalog coverage](#catalog-coverage) lists every entry of both pages with what this phase builds,
what stays in FULL.md F4 and what is not offered.

External references, normative wherever this page does not say otherwise:
[OCI distribution specification v1.1.1](https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md),
[OCI image specification v1.1](https://github.com/opencontainers/image-spec/blob/v1.1.1/spec.md),
[Docker image manifest v2, schema 2](https://distribution.github.io/distribution/spec/manifest-v2-2/),
[Docker registry token authentication](https://distribution.github.io/distribution/spec/auth/token/) and its
[scope grammar](https://distribution.github.io/distribution/spec/auth/scope/).

## Conventions on this page

| Placeholder | Meaning |
|---|---|
| `<base>` | trove's public base URL of the endpoint the request arrived on; `https://trove.example.com` in examples (# example) |
| `<host>` | the host (and port) of `<base>`: `trove.example.com` (# example) |
| `<name>` | an image name in the OCI grammar, below the repository: `library/nginx`, `app` (# example) |
| `<ref>` | a tag or a digest |
| `$TOKEN` | a trove token (`trv_pat_…`, `trv_sat_…` or `trv_cit_…`, INV-8) |

Project and repository keys used in examples (# example): the project `web` with `docker-local` (local), `docker-hub` (remote of
Docker Hub, mode `pullThrough`), `docker-hub-mirror` (remote of Docker Hub, mode `mirror`) and `docker` (virtual over
`docker-local` and `docker-hub`, deploy target `docker-local`). An image reference therefore reads
`trove.example.com/web/docker/library/nginx:1.29`: registry host, project key, repository key, image name, tag.

Priorities: every requirement below is **P1** (block B of the roadmap) unless it says otherwise; what this phase leaves to F4 is
in [Deferred](#deferred).

## Requirements

### R-OCI-001 · The format `oci`, its classes and the registry address
Priority: **P1** · Covers: PKG-DOCKER-001, PKG-DOCKER-004, PKG-DOCKER-005, PKG-DOCKER-006, PKG-DOCKER-016, PKG-OCI-001, PKG-OCI-005

trove MUST offer the format `oci` for all three repository classes of INV-5. One format serves Docker images (Docker image
manifest v2 schema 2 and the Docker manifest list) and OCI content (image manifests, image indexes and artifacts of any type);
there is no separate Docker format and no per-repository protocol version: every repository speaks the OCI distribution API,
which is a superset of the Docker registry HTTP API V2 (Q-OCI-1).

Format registration (R-REPO-003): name `oci`, display name `OCI (Docker)`; classes `local`, `remote`, `virtual`; protocol
base path template `/v2/{project}/{key}/` (the registry root of R-ARCH-005, Q-OCI-4); default upstream URL
`https://registry-1.docker.io/`, with the proposed extra hosts of R-OCI-021; default metadata period 300 s and not-found period
300 s (R-OCI-022); pattern subject `name` (the image name, R-REPO-005); priority members, pin rules and namespace claims
supported; package name = image name; package version = tag (R-STOR-039); package-document path `<name>/_taglist`
(R-OCI-024); `VerifyAnnouncedDigests: true`, because every artifact is addressed by its SHA-256 (R-REPO-041).

**Address.** Docker and OCI clients fix the API at `/v2/` on the root of a host (R-HA-041). The repository is named by the
first two path segments after `/v2/`, the project key and the repository key, exactly as below `/api/<format>/`
(R-REPO-002, Q-HA-4); the rest of the path is the image name followed by the operation:

| Request | Meaning |
|---|---|
| `GET /v2/web/docker/library/nginx/manifests/1.29` | Repository `web/docker`, image `library/nginx`, tag `1.29` |
| `GET /v2/web/docker-local/app/blobs/sha256:<hex>` | Repository `web/docker-local`, image `app`, a blob |
| `GET /v2/web/docker-local/team/app/tags/list` | Repository `web/docker-local`, image `team/app`, its tags |

- An image reference is `<host>/<projectKey>/<repoKey>/<name>[:<tag>|@<digest>]`. One `docker login <host>` covers every
  repository of every project on that host, because they share one host.
- On a host whose root is bound to one repository (R-EP-006), `/v2/<name>/…` addresses that repository (R-OCI-029).
- Subdomain and port addressing per repository are not offered: an endpoint with its own listener and a host-root binding
  gives the port form where it is needed (R-EP-006, R-EP-007), and a host label cannot carry project and key
  ([endpoints.md](../endpoints.md) Q-EP-76).
- The repository-less routes below `/v2/` (`/v2/`, `/v2/_catalog`, `/v2/_token`) belong to the core, not to a repository
  (R-OCI-003). A path below `/v2/` with fewer than three segments after `/v2/`, or whose project or repository segment does
  not name a readable repository of the format, answers 404 with code `NAME_UNKNOWN`, the same for a project or repository
  that does not exist and one the caller cannot see (R-AUTHZ-016).
- Raw file access (`/files/<projectKey>/<repoKey>/<path>`, INV-6) reads the storage layout of R-OCI-007; a raw deploy into an
  OCI repository answers 409, as for every format that is not served at `/files/` ([architecture.md](../architecture.md)
  Q-ARCH-223, the NuGet case is Q-NUGET-13), so manifests and tags are only written through the protocol. A raw delete runs the
  cascade and the refusals of R-OCI-018.

Acceptance criteria:

- `curl -s <base>/api/v1/formats | jq '.[] | select(.name=="oci")'` lists the classes `local`, `remote`, `virtual`, the
  protocol base path `/v2/{project}/{key}/` and the default upstream URL `https://registry-1.docker.io/`.
- `docker push trove.example.com/web/docker-local/app:1` followed by `docker pull trove.example.com/web/docker/app:1` on
  another machine succeeds.
- `GET /v2/web/nope/app/manifests/1`, `GET /v2/nope/docker-local/app/manifests/1` and `GET /v2/web/docker-local` each answer
  404 with an OCI error body whose code is `NAME_UNKNOWN`.
- `curl -X PUT --data-binary @m.json <base>/files/web/docker-local/app/_tags/1` answers 409 and stores nothing.

### R-OCI-002 · Keys, image names, tags and digests
Priority: **P1** · Covers: PKG-DOCKER-002, PKG-DOCKER-026, PKG-OCI-001

- **Keys.** A project key and a repository key are path components of every image reference, so both MUST be valid OCI name
  components. INV-5 and R-AUTHZ-026 are tightened for every format (Q-OCI-3): a repository key matches
  `^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$` with 2 to 64 characters, a project key `^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$` with 2
  to 32 characters. The rules "no `..`" and "no trailing `.`" of R-REPO-002 follow from the grammar.
- **Image names** match the component grammar of the distribution specification,
  `[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(\/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*`. The whole name as a client sends it,
  `<projectKey>/<repoKey>/<name>`, is at most 255 bytes, the bound many clients apply to host and name together. A name
  outside the grammar or the bound answers 400 `NAME_INVALID`. Upper-case letters are refused, never folded.
- **Tags** match `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}`; another tag answers 400 `TAG_INVALID` on push and 404
  `MANIFEST_UNKNOWN` on pull. Tags are case-sensitive.
- **Digests** are `sha256:` followed by 64 lower-case hex digits (Q-OCI-9). Any other algorithm, an upper-case hex digit or a
  wrong length answers 400 `DIGEST_INVALID`, and `?digest-algorithm=` with another value than `sha256` answers 400
  `UNSUPPORTED`.
- Names, tags and digests are validated on the escaped path before any lookup; `%` in a name answers 400 `NAME_INVALID`.

Acceptance criteria:

- Creating repositories with the keys `docker-`, `a.-b`, `a._b` and `a___b` answers 422 `invalid_key`; `docker-local`,
  `docker.hub` and `a__b` are accepted; a project key `web-` answers 422 `invalid_key` (unit tests of both grammars against
  the distribution specification's regular expression).
- `PUT /v2/web/docker-local/App/manifests/1` answers 400 `NAME_INVALID`; a name that makes the whole name 256 bytes long
  answers 400 `NAME_INVALID`; `PUT …/app/manifests/-x` answers 400 `TAG_INVALID`.
- `GET …/app/blobs/sha512:<128 hex>` and `GET …/app/blobs/sha256:<63 hex>` answer 400 `DIGEST_INVALID`.

### R-OCI-003 · The registry root: version check, catalog and token endpoint
Priority: **P1** · Covers: PKG-DOCKER-012, PKG-DOCKER-016, PKG-DOCKER-022

| Route | Answer |
|---|---|
| `GET`, `HEAD /v2/` | 200 with `{}` for a request with a valid credential of any kind, the anonymous registry token included; 401 with the challenge of R-OCI-004 for a request without a credential (Q-OCI-20); 401 `UNAUTHORIZED` for an invalid one |
| `GET /v2/_catalog?n=&last=` | Image names the caller may read (R-OCI-017) |
| `GET`, `POST /v2/_token` | The token service (R-OCI-004, R-OCI-005) |

- Every response below `/v2/` carries `Docker-Distribution-API-Version: registry/2.0`, which Docker clients read to recognise
  a version 2 registry.
- Every error below `/v2/` has the OCI body `{"errors":[{"code":…,"message":…,"detail":…}]}` with the codes of the
  distribution specification (R-OCI-008), including the refusals of the middleware chain (`401` of step 6, `429` of step 7,
  `503 server_busy` of step 4; R-ARCH-005, Q-ARCH-214).
- The routes of this table are registered by the core on every endpoint whose surface holds `packages` (R-EP-005); they are
  the only patterns under `/v2/` besides the format's repository prefix, and the route-table test of R-ARCH-005 admits exactly
  these (Q-OCI-4).
- Names starting with `_` cannot be project keys (R-OCI-002), so `_catalog` and `_token` never collide with a repository
  address.

Acceptance criteria:

- `curl -si <base>/v2/` answers 401 with `WWW-Authenticate: Bearer realm="<base>/v2/_token",service="<host>"` and
  `Docker-Distribution-API-Version: registry/2.0`; `curl -si -u x:$TOKEN <base>/v2/` answers 200 with `{}`.
- The route-table test fails on a fixture plug-in that registers `GET /v2/x`, and passes with the OCI format registered.

### R-OCI-004 · Authentication challenge and token service
Priority: **P1** · Covers: PKG-DOCKER-011, PKG-DOCKER-012, PKG-DOCKER-015, PKG-DOCKER-052, PKG-OCI-005

The registry uses the token flow of the Docker registry token authentication specification (Q-OCI-5):

1. A request without a credential that needs one, and the version check, answer 401 with
   `WWW-Authenticate: Bearer realm="<base>/v2/_token",service="<host>"`; on a repository route the challenge adds
   `scope="repository:<full name>:<actions>"` naming what the request needs, so the client asks for the right token.
2. The client calls `GET <base>/v2/_token?service=<host>&scope=…` with `Authorization: Basic base64(<user>:<trove token>)`, or
   without credentials for an anonymous token. `POST /v2/_token` with the form fields `grant_type=password`, `username`,
   `password`, `service`, `scope` and `client_id` (the OAuth 2.0 form that containerd tries first) is answered the same way. A
   `POST` carries its credential in the form only: a form without `grant_type` or without `password`, a form that is not
   `application/x-www-form-urlencoded` or larger than 16 KiB, and a `POST` with an `Authorization` header as well answer 400
   `invalid_request`; a `grant_type` other than `password` (`refresh_token` among them) answers 400 `unsupported_grant_type`.
   A `service` other than `<host>` answers 400 `invalid_request`. A 400 of the token service carries the error list of
   R-OCI-008 with the code `UNSUPPORTED` and, in the same object, the members `error` and `error_description` of
   [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749) §5.2 (Q-OCI-52).
3. The token service authenticates the credential through the authentication stage of R-AUTH-001 (the Basic user name is
   recorded and ignored, INV-8): an invalid one answers 401 `UNAUTHORIZED` with `WWW-Authenticate: Basic realm="trove"`;
   none yields the anonymous principal where the endpoint allows anonymous requests (R-AUTH-037, R-EP-010), else 401. The
   chain does not authenticate `/v2/_token`: the token service presents the one credential of the request, the
   `Authorization` header or the form's password, to the stage itself, which applies every rule of R-AUTH-001 and of the
   endpoint to it (Q-OCI-51). A registry token is no credential of the token service, so a registry token cannot renew itself.
   The form's password is a Basic presentation of the trove token: with `auth.clients.basic.enabled: false` the token service
   refuses it like a `GET` with `Basic`, and no registry client gets a token (Q-OCI-57).
4. It grants per scope entry the actions the caller may perform (R-OCI-006) and answers 200 with
   `{"token": "<registry token>", "access_token": "<the same>", "expires_in": <seconds>, "issued_at": "<RFC 3339>"}`. A
   partial or empty grant is not an error. No refresh token is issued, whatever `offline_token` says, so Docker stores the
   trove token itself as it does for any registry without refresh tokens.
5. The client repeats the request with `Authorization: Bearer <registry token>`.

- **Direct credentials.** A trove token is also accepted directly on every `/v2/` route, as `Basic` with any user name or as
  `Bearer` (R-AUTH-001): clients that send credentials up front (`curl -u`, a cloud registry's pass-through repository,
  PKG-DOCKER-052) need no token round trip. The session cookie is not accepted below `/v2/`.
- **Token requests carry the scope grammar** `repository:<full name>:<action>[,<action>…]` (R-OCI-006). Docker sends one
  `scope` per resource; at most 16 entries are read, more answer 400 `invalid_request`.
- **Insufficient scope.** A request whose registry token lacks the action the route needs answers 401 with the challenge of
  step 1 and `error="insufficient_scope"`, so the client fetches a new token. A request whose credential allows the action by
  the token but not by the decision point answers 403 `DENIED`, or 404 `NAME_UNKNOWN` where the repository is hidden
  (R-AUTHZ-016).
- **Realm.** The realm is built from the response base URL of the endpoint the request arrived on (R-EP-003), so it is
  reachable under every host the endpoint serves; on a bound host it is that host's own `/v2/_token` (R-EP-007).
- **Kubernetes** pulls with an image pull secret whose password is a trove token (PKG-DOCKER-015); the kubelet's container
  runtime follows the same flow.

Acceptance criteria:

- `echo "$TOKEN" | docker login trove.example.com -u ci --password-stdin` succeeds; `podman login` likewise.
- `curl -s "<base>/v2/_token?service=<host>&scope=repository:web/docker-local/app:pull,push" -u x:$TOKEN` for a caller with
  `read` and `deploy` on `web/docker-local` returns a token whose verified grant lists `pull` and `push` for that name; the same
  for a caller with `read` only lists `pull`; with a revoked `$TOKEN` it answers 401.
- Anonymous: with anonymous `read` on `docker-hub`, `docker pull trove.example.com/web/docker-hub/library/alpine:3` without
  login succeeds; `docker push` to `docker-local` without login fails with `unauthorized`.
- `curl -s -X POST <base>/v2/_token -d grant_type=password -d username=x -d password=$TOKEN -d service=<host> -d
  scope=repository:web/docker-local/app:pull` answers 200 with `access_token`.
- `curl -s -o /dev/null -w '%{http_code}' -u x:$TOKEN <base>/v2/web/docker-local/app/manifests/1` answers 200 without a token
  request (direct credential).
- A registry token granted `pull` only, used for `POST …/app/blobs/uploads/`, answers 401 with `error="insufficient_scope"`
  and `scope="repository:web/docker-local/app:push"` in the challenge.

### R-OCI-005 · Registry tokens
Priority: **P1** · Covers: PKG-DOCKER-012

A registry token is a short-lived, self-contained credential that trove issues and verifies itself (Q-OCI-5). It narrows a
principal's access; it never widens it.

- **Content.** Version, the key ID of the key that signed it, the principal (kind and ID; the anonymous principal has no ID),
  the trove token that authenticated the token request, named by its ID, its type and the SHA-256 of its secret (none for
  anonymous; Q-OCI-50), the endpoint, the issue and expiry times, and the grant: at most 16 entries of qualified repository,
  image name and actions (`pull`, `push`, `delete`), and the catalog entry.
- **Form.** `trv_rgt_` + base64url(payload) + `.` + base64url(HMAC-SHA-256(key, `trv_rgt_` + base64url(payload))), at most
  4 KiB, base64url without padding ([RFC 4648](https://www.rfc-editor.org/rfc/rfc4648) §5). The payload is `1:<key id>:<claims
  as JSON>`: the key ID stands before the claims, so the verifier finds the key and checks the MAC before it parses anything
  the client sent (Q-OCI-54). The key is derived with HKDF-SHA-256 from a key of the stored-secret key ring with info `trove
  registry token v1` (R-ARCH-021): the active key signs, every key of the ring verifies, so a rotation of the ring needs no new
  secret and old tokens end with their lifetime or with the removal of their key from the ring. A grant that does not fit in
  4 KiB is refused with 400 `invalid_request`, never cut. The token is not a reference token (R-AUTH-022): it is never stored,
  and the scanner pattern of R-AUTH-022 does not match it.
- **Lifetime.** `oci.token_lifetime` (# default (proposed) `300s`, range 60 s to 3600 s), never beyond the expiry of the token
  that authenticated the request. `expires_in` says it.
- **Verification** happens in the authentication stage (R-AUTH-001) and never touches PostgreSQL on its own: the MAC with the
  named key, the expiry, the endpoint of the request (R-EP-002), and then the state of the token that authenticated the token
  request through the token cache of R-AUTH-023, by the hash the registry token carries (step 2 of R-AUTH-023; revoked,
  expired, owner disabled answer 401 `UNAUTHORIZED`), so a revocation stops its registry tokens within the bound that holds for
  the token itself. The endpoint's token types and bindings (R-EP-010) apply to that token as if it were presented itself. An
  anonymous registry token is accepted while the endpoint and the runtime switch allow anonymous requests (R-AUTH-037).
- **Use.** A request carrying a registry token is a request of its principal with the token's credential scope (R-AUTH-024)
  and the grant as a further ceiling: the route's action on the request's repository and image name must be in the grant
  (R-OCI-004 for the answer otherwise), and then the decision point decides as for every request (R-AUTHZ-015). A grant is
  therefore never a permission of its own. One enforcement point checks the grant in front of every handler below `/v2/`:
  mounting through it is the only way to put a handler there (the route table admits no other `/v2/` pattern, R-ARCH-005),
  and it learns the route of a request from the dispatcher's own match. A registry token on a route without an entry, on an
  address the match cannot read, or on a route whose action no token action maps answers 401 `insufficient_scope` (error
  code `oci_insufficient_scope`), so a route that is not registered fails closed. The version check passes any registry
  token, on `/v2/` itself only. The cache view of a remote repository (`<key>~cache`) is granted and checked under its base
  repository, and the source of a cross-repository mount needs `pull` there, action by action (Q-OCI-56).
- **Lifetime of the claims.** A token whose claims span more than `oci.token_lifetime`, or whose issue time lies more than one
  minute ahead of the verifying replica's clock, is refused like a forged one, so even claims signed with the key ring verify for
  at most the configured lifetime.
- Logs redact the whole value, as for every `Authorization` header (R-AUTH-001); the request log records the principal and the
  ID of the authenticating token.

Acceptance criteria:

- Flipping one character of a registry token, presenting it after its expiry, or presenting it on another endpoint answers 401
  without a database query (integration test with the query counter of R-AUTH-001).
- Revoking the PAT behind a registry token makes the next request with that registry token answer 401 on every replica
  within the bound of R-AUTH-023.
- Removing the caller's `deploy` grant after the token was issued makes the next `PUT …/manifests/…` with that token answer 403
  `DENIED`.
- After a key-ring rotation (new active key, the old one still in the ring), a token signed before the rotation is accepted
  until it expires, and new tokens name the new key ID.

### R-OCI-006 · Permissions per operation
Priority: **P1** · Covers: PKG-DOCKER-003, PKG-DOCKER-017, PKG-DOCKER-018, PKG-DOCKER-027, PKG-DOCKER-031

The coordinate of every request is the repository, the image name as the package and, for a tag, the tag as the version
(R-AUTHZ-007 rules; this table joins it for the OCI format):

| Client operation | Coordinate | Actions | Token scope action |
|---|---|---|---|
| Version check `/v2/` | none | a valid credential, or none (answered 401) | — |
| Catalog | per repository and name, filtered | `read` | `registry:catalog:*` |
| Manifest or blob `GET`/`HEAD`, tags list, referrers | name (and tag) | `read`; a remote fill also its `cacheFillAction` (R-AUTHZ-008) | `pull` |
| Start, continue, finish, query or cancel a blob upload | name | `deploy` | `push` |
| Mount a blob from another name | target name; source repository and name | `deploy` on the target, `read` on the source | `push` on the target, `pull` on the source |
| Push a manifest by digest, or under a tag that does not exist or already points at the same digest | name, tag | `deploy`; for the first version of the name in the repository also the shadowing check (R-REPO-048) | `push` |
| Push a manifest under a tag that points at another digest | name, tag | `deploy` and `overwrite` | `push` |
| `DELETE` a tag, a manifest or a blob | name | `delete` | `delete` |

- Writes pass the ownership check of R-AUTHZ-018 with the image name as the package, as npm and NuGet do; the repository's
  `packageOwnership` decides.
- **How the token service grants.** For each scope entry it runs the coarse check of the route kind on the named repository
  (R-ARCH-008: may the caller do this action anywhere in the repository, with the name as package when the repository's
  patterns or the caller's grants are per package): `pull` → `read`, `push` → `deploy`, `delete` → `delete`, `*` → all three.
  It asks it as a question on the request's index that writes no audit event (Q-OCI-55): a grant that leaves an entry out is
  no refusal, and the request that later needs the action is decided and audited on its own.
  A repository that does not exist, is not of the format or is hidden from the caller grants nothing and is not reported
  (Q-OCI-6). `registry:catalog:*` is granted to every authenticated caller and to anonymous where the endpoint allows anonymous
  requests; what the catalog lists is decided per name (R-OCI-017).
- A grant is a ceiling only; each request is decided again (R-OCI-005). `push` covers `overwrite` as a ceiling; the decision
  point still requires `overwrite` for a moved tag.
- Image names visible to a reader are the names it may read: the catalog and the tags list never show a name a path- or
  package-scoped grant hides (PKG-DOCKER-003 decided the strict way).

Acceptance criteria (`TestR_AUTHZ_007_OCIActionMap`, one test per row, principals with exactly the action against one without
it, repositories with `packageOwnership: off`):

- With `read` and `deploy` but not `overwrite` on `docker-local`, pushing `app:1` succeeds, pushing the same image again
  succeeds, and pushing a different image as `app:1` fails with `denied`.
- With `deploy` but not `delete`, `DELETE …/app/manifests/1` answers 403 `DENIED`.
- A token with `read` on `docker-local` scoped to the package pattern `team/*` gets `team/app` in the catalog and not `other/x`.

### R-OCI-007 · Storage layout, packages and the format's tables
Priority: **P1** · Covers: PKG-DOCKER-019, PKG-DOCKER-043, PKG-OCI-008, PKG-OCI-010

Every manifest and blob is an item of the repository; a tag is an item whose content is the manifest it points at, so the
blob store's SHA-256 is the manifest's digest and nothing is stored twice (Q-OCI-7). Segments that start with `_` cannot be
name components (R-OCI-002), so the layout never collides with an image name:

| Item path | Content | Path kind (R-ARCH-008) |
|---|---|---|
| `<name>/_manifests/sha256:<hex>` | the manifest's bytes exactly as pushed or fetched; `media_type` is its media type | `Content` |
| `<name>/_tags/<tag>` | the bytes of the manifest the tag points at (the same blob) | `Metadata` |
| `<name>/_taglist` | remote only: the upstream's tags list document (R-OCI-024) | `Metadata` |
| `<name>/_blobs/sha256:<hex>` | a layer, a config or any other blob of the name | `Content` |

- **Packages.** The package is the image name (`packages.name`); a version is a tag (`package_versions.version`) whose lead
  item is the tag item (R-STOR-039). A manifest without a tag (a platform manifest of an index, a referrer) has no version
  row; it belongs to the units of R-STOR-039 through the format's references.
- **The format's tables** (data port `internal/formatdata/oci`, Q-OCI-7): a row per manifest of a name with its media type,
  `artifactType`, `subject`, size, the summed size of its config and layers and the annotations a referrers answer needs (at most
  64 KiB, the rest is cut and the cut is recorded in `annotations_cut`); a row per reference from a manifest to a manifest or a
  blob of the same name, which carries the platform where an index names one for the child (the fact belongs to the edge: only the
  index knows it, Q-OCI-28); and the mark of a manifest the journal replay withheld after a restore (`oci_tombstones`, R-OCI-033). They carry no blob reference (no `blob_sha256`
  column, R-STOR-003 rule 1), so they never hold a blob against garbage collection; the items do.

```sql
CREATE TABLE oci_manifests (
  repo_id        bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name           text COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
  digest         bytea NOT NULL CHECK (octet_length(digest) = 32),   -- the manifest item's blob_sha256
  media_type     text NOT NULL CHECK (length(media_type) <= 255),
  artifact_type  text CHECK (length(artifact_type) <= 255),
  subject        bytea CHECK (subject IS NULL OR octet_length(subject) = 32),
  size           bigint NOT NULL CHECK (size >= 0),
  content_size   bigint NOT NULL DEFAULT 0 CHECK (content_size >= 0), -- config and layers of an image manifest
  annotations    jsonb CHECK (annotations IS NULL OR octet_length(annotations::text) <= 65536),
  annotations_cut boolean NOT NULL DEFAULT false,                       -- the annotations were cut at 64 KiB
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, name, digest)
);
CREATE INDEX oci_manifests_subject ON oci_manifests (repo_id, name, subject, digest) WHERE subject IS NOT NULL;

CREATE TABLE oci_references (
  repo_id   bigint NOT NULL,
  name      text COLLATE "C" NOT NULL,
  manifest  bytea NOT NULL,
  target    bytea NOT NULL CHECK (octet_length(target) = 32),
  kind      text NOT NULL CHECK (kind IN ('manifest', 'blob')),
  platform  text CHECK (length(platform) <= 128),             -- os/arch[/variant], on the edge from an index to a child
  PRIMARY KEY (repo_id, name, manifest, target),
  CHECK (kind = 'manifest' OR platform IS NULL),
  FOREIGN KEY (repo_id, name, manifest) REFERENCES oci_manifests (repo_id, name, digest) ON DELETE CASCADE
);
CREATE INDEX oci_references_target ON oci_references (repo_id, name, target);

CREATE TABLE oci_tombstones (                                  -- a manifest the journal replay withheld (R-OCI-033); written by the replay only
  repo_id      bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name         text COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
  digest       bytea NOT NULL CHECK (octet_length(digest) = 32),
  withheld_at  timestamptz NOT NULL DEFAULT now(),
  deleted_by   uuid,                                          -- the principal of the journaled deletion
  PRIMARY KEY (repo_id, name, digest)
);

ALTER TABLE remote_cache_entries ADD COLUMN media_type text CHECK (length(media_type) <= 255);  -- R-OCI-022
```

- The rows are written by the storage hooks (`OnCommit` for a push and a remote fill, `OnItemsRemoved` for a removal) in the
  transaction that writes the items, and every change increments the package's `rev_seq` (R-STOR-003). A hook that has to remove
  more than the item it was told of (the tags of a deleted manifest, the children nobody else names) does so through
  `ItemsEvent.Cascade`, which removes in the same transaction and runs the hook again for those items (Q-OCI-24); the hook that
  records a manifest keeps one reference row per target, however often the manifest names it (the empty descriptor as config and as
  a layer).
- The rows stay while an item is in the trash can, as NuGet's do, and go when the item is removed for good: a restore from the
  trash needs no hook for them. The rows of a remote repository are tied to the alias item `<name>/_manifests/<digest>` that its fill
  entered (R-OCI-022, Q-OCI-25): the eviction of a metadata entry (the tag document) reaches no format hook, but the removal of an
  item does, so the hook removes the rows of a manifest with its alias item. The tag documents and the tags list are metadata
  entries and own no row.
- Layer deduplication across names and repositories is the blob store's: a blob item references the one object of its
  SHA-256 (R-STOR-005).

Acceptance criteria:

- After `docker push …/docker-local/app:1`, `GET /files/web/docker-local/app/_tags/1` returns bytes whose SHA-256 is the
  digest `docker push` printed, and `/files/web/docker-local/app/_manifests/<that digest>` returns the same bytes; the blob
  store holds one object for both.
- Pushing the same layer to `app` and `other` in two repositories stores one object (`blobs` count grows by one).

### R-OCI-008 · Error codes and response headers
Priority: **P1** · Covers: PKG-DOCKER-016, PKG-OCI-006

The format's `WriteError` (R-ARCH-008) maps every core error onto the distribution specification's codes:

| Core outcome | Status | Code |
|---|---|---|
| Name, tag or digest invalid | 400 | `NAME_INVALID`, `TAG_INVALID`, `DIGEST_INVALID` |
| Repository unknown or hidden; name unknown | 404 | `NAME_UNKNOWN` |
| Manifest unknown | 404 | `MANIFEST_UNKNOWN` |
| Blob unknown | 404 | `BLOB_UNKNOWN` |
| Upload session unknown, expired or of another principal | 404 | `BLOB_UPLOAD_UNKNOWN` |
| Chunk out of order, claim held by another request | 416 | `BLOB_UPLOAD_INVALID` with `Range` |
| Manifest invalid, refused media type | 400 | `MANIFEST_INVALID` |
| Manifest references what the repository lacks | 400 | `MANIFEST_BLOB_UNKNOWN` |
| Too large (manifest, blob, quota) | 413 | `SIZE_INVALID` |
| No credential, invalid credential, insufficient scope | 401 | `UNAUTHORIZED` with the challenge of R-OCI-004 |
| Denied by the decision point | 403 | `DENIED` |
| A refusal of the registry itself: the push of a manifest the journal replay withheld (R-OCI-013, R-OCI-033), a deletion that would leave a dangling reference (R-OCI-018) | 409 | `DENIED` |
| Operation not offered (write to a remote, to a virtual without deploy target, an unsupported parameter) | 405 or 400 | `UNSUPPORTED` |
| Rate limit (R-ARCH-005 step 7) | 429 | `TOOMANYREQUESTS` with `Retry-After` |
| Upstream rate limit without a stored copy (R-REPO-029: 503 with `Retry-After`) | 503 | `TOOMANYREQUESTS` |
| Upstream failure, offline, circuit open, object lost | 502, 503, 504 | `UNAVAILABLE` (distribution's code; the specification allows registry-specific codes) |

- `Retry-After` is the engine's wait in whole seconds, at least 1, and is never cut: a client told 60 s for a wait of six hours
  would retry into the same refusal, and the throttling of an upstream (R-REPO-054) is exactly such a wait.
- `Docker-Content-Digest` is set on every successful manifest and blob response and on `201` of a push; `Content-Length` on
  every `HEAD`; `ETag` of a manifest or blob is `"sha256:<hex>"`, so `If-None-Match` gives 304.
- `Content-Type` of a manifest response is the stored media type without parameters; a blob is
  `application/octet-stream`. The content headers of R-ARCH-007 (`nosniff`, the sandbox policy) apply to every response.

Acceptance criteria:

- A conformance-kit test drives each row of the table through the format's `WriteError` and checks status, code and headers.

### R-OCI-009 · Pulling manifests
Priority: **P1** · Covers: PKG-DOCKER-017, PKG-OCI-007

- `GET` and `HEAD /v2/<p>/<r>/<name>/manifests/<ref>` with a tag read `<name>/_tags/<tag>`, with a digest
  `<name>/_manifests/<digest>`, and answer the stored bytes with their media type and digest (R-OCI-008).
- **No conversion, no negotiation by rewriting** (Q-OCI-8). The stored manifest is served as it is, whatever `Accept` says, so
  its digest stays the digest the client verifies. Every client this phase supports accepts all four manifest types.
- A tag is resolved at request time; a client that pulls by digest (Kubernetes does, PKG-DOCKER-015) gets the manifest as long
  as it is stored, whether a tag still points at it or not (untagged manifests stay, R-OCI-018).
- Read-your-writes (R-ARCH-012): a push answered `201` on one replica is served by the next pull on any replica.

Acceptance criteria:

- `curl -sI -H "Authorization: Bearer $RT" …/app/manifests/1` answers 200 with `Docker-Content-Digest`, `Content-Length` and
  the pushed `Content-Type`, and no body; the `GET` returns bytes whose SHA-256 is that digest.
- A manifest pushed as `application/vnd.docker.distribution.manifest.v2+json` is served with that type to a request whose
  `Accept` lists only `application/vnd.oci.image.manifest.v1+json`.

### R-OCI-010 · Pulling blobs
Priority: **P1** · Covers: PKG-DOCKER-017, PKG-OCI-006

- `GET` and `HEAD /v2/<p>/<r>/<name>/blobs/<digest>` read `<name>/_blobs/<digest>`: a blob is readable through a name only
  when an item of that name holds it (pushed, mounted or fetched for it), never because another name or repository holds the
  same bytes.
- `Range` requests are answered with 206 (R-STOR-012); a repository with presigned downloads (R-STOR-014) answers `307` to the
  object store for a `GET` of a blob, which every supported client follows without forwarding `Authorization` (the redirect is the
  core's P2 and is not built, H-601: a blob is streamed).
- Download integrity (R-STOR-013) applies: a blob whose stored bytes do not match their SHA-256 ends the response.

Acceptance criteria:

- `curl -H 'Range: bytes=0-99'` on a layer answers 206 with 100 bytes.
- A layer pushed only to `web/docker-local/app` answers 404 `BLOB_UNKNOWN` through `web/docker-local/other`, although the blob
  store holds it.

### R-OCI-011 · Blob uploads across requests and replicas
Priority: **P1** · Covers: PKG-DOCKER-018, PKG-OCI-006

Uploads follow the distribution specification; an upload of several requests is an upload session of R-STOR-011, so any
replica continues it and none keeps anything of it between requests (INV-18):

| Request | Answer |
|---|---|
| `POST …/<name>/blobs/uploads/` | 202, `Location: /v2/<p>/<r>/<name>/blobs/uploads/<session id>`, `Range: 0-0`, `OCI-Chunk-Min-Length: <storage.uploads.part_size>`, `Docker-Upload-UUID` |
| `POST …/<name>/blobs/uploads/?digest=<d>` with the whole blob | 201 with `Location` of the blob and `Docker-Content-Digest`: a single-request upload through the upload pipeline of R-STOR-004, no session |
| `PATCH <location>` with `Content-Range: <start>-<end>` or a streamed body | 202 with `Range: 0-<received − 1>` and `Location`; a start other than the received count answers 416 `BLOB_UPLOAD_INVALID` |
| `PUT <location>?digest=<d>`, optionally with the last bytes | 201 with the blob's `Location` and `Docker-Content-Digest`; a mismatch answers 400 `DIGEST_INVALID` and keeps the session open |
| `GET <location>` | 204 with `Range` and `Location` |
| `DELETE <location>` | 204; the session is aborted |

- The session row records the repository the client addressed, the target repository it writes to (R-OCI-028), the image
  name and the principal; a request of another principal, or for another name, answers 404 `BLOB_UPLOAD_UNKNOWN` (R-STOR-011
  step 1). The name and the repository are checked again on every request with `deploy` (R-OCI-006).
- The finalize commits the item `<name>/_blobs/<digest>` in the transaction of the commit protocol (R-STOR-005, R-STOR-011
  finalize); a blob already stored is deduplicated without a copy.
- **Drain.** A `PATCH` cut by a drain releases its claim first (R-STOR-004 step 8); the client either asks for the status
  and continues on any replica, or starts the blob again (Docker and Podman restart a failed blob push with a new session).
  An abandoned session expires (R-STOR-011 *Expiry*) and leaves no object.
- Sizes: the effective maximum upload size of R-STOR-004 bounds a blob; beyond it the request answers 413 `SIZE_INVALID` and
  the session is aborted.

Acceptance criteria:

- A three-chunk upload whose requests reach three different replicas (integration, pinned per request) yields a blob whose
  SHA-256 is the file's, answered 201.
- `docker push` of an image with a 300 MiB layer while the `packages` pod receiving its `PATCH` is drained exits 0, the
  pulled image has the pushed digest, and after `storage.uploads.session_ttl` plus a collection run
  `aws s3api list-multipart-uploads` lists no upload of the abandoned session (e2e set `oci-long`).
- A `PATCH` with `Content-Range: 10-19` on a session that has received nothing answers 416 `BLOB_UPLOAD_INVALID` with
  `Range: 0-0`, the form the distribution specification uses for a session without bytes.
- The OCI distribution conformance suite's push category passes (R-OCI-035).

### R-OCI-012 · Cross-repository blob mount
Priority: **P1** · Covers: PKG-DOCKER-019, PKG-OCI-006

- `POST …/<name>/blobs/uploads/?mount=<digest>&from=<source>` with `<source>` a full name `<projectKey>/<repoKey>/<name>`
  (on a bound host: a name of the bound repository) creates `<name>/_blobs/<digest>` from the source's item and answers 201,
  when the caller may `read` the source name and the source holds the blob; otherwise it opens an upload session and answers
  202, saying nothing about whether the source exists (Q-OCI-12).
- The source may be any repository of the format on the same installation the caller can read, a remote's cache included;
  through a virtual source the member resolution of R-OCI-027 applies, without an upstream request.
- `mount` without `from` opens a session and answers 202: there is no automatic mount, because it would tell a caller that a
  blob exists somewhere it may not read.

Acceptance criteria:

- With `read` on `web/docker-local/base` and `deploy` on `web/team-local/app`, a mount of the base layer answers 201 and
  `GET /v2/web/team-local/app/blobs/<d>` answers 200; without `read` on the source it answers 202.
- `mount` without `from` for a blob the installation holds answers 202.

### R-OCI-013 · Pushing manifests and tags
Priority: **P1** · Covers: PKG-DOCKER-020, PKG-DOCKER-021, PKG-DOCKER-026, PKG-DOCKER-027, PKG-OCI-007, PKG-OCI-008

`PUT /v2/<p>/<r>/<name>/manifests/<ref>` with `Content-Type` set to the manifest's media type:

1. **Admit before reading** (R-ARCH-005): the coarse `deploy` check runs before the body is read; a body above 4 MiB answers
   413 `SIZE_INVALID` (Q-OCI-8).
2. **Media type.** Accepted are `application/vnd.oci.image.manifest.v1+json`, `application/vnd.oci.image.index.v1+json`,
   `application/vnd.docker.distribution.manifest.v2+json` and `application/vnd.docker.distribution.manifest.list.v2+json`.
   Docker schema 1 (`…manifest.v1+json`, `…manifest.v1+prettyjws`) and every other type answer 400 `MANIFEST_INVALID`. When
   the document has a `mediaType` field it must equal `Content-Type`; an OCI manifest without one is read by its
   `Content-Type`.
3. **Parse** with the untrusted-input helpers (R-OCI-020): `schemaVersion` 2, the shape of the type (an image manifest has
   `config` and `layers`; an index has `manifests`, and a document that has both shapes is refused), every descriptor with a
   valid digest, a non-negative integer size and a media type in the form RFC 6838 gives (no parameters). Config and layer media
   types and `artifactType` are not restricted beyond that form: the registry stores any OCI content (PKG-OCI-008). The reader is
   strict where a second reader could interpret the document differently (Q-OCI-60): an object with the same key twice, a key that
   only differs in case from one the registry reads, a document that is not UTF-8 or has data after its value, and a size that is
   not a plain integer are `MANIFEST_INVALID`; unknown fields are accepted and ignored (the bytes are stored as they came). The
   `data` of a descriptor must be the standard base64 of exactly the content its size and digest name, `urls` must be absolute
   `http` or `https` URLs, and the `platform` of an index entry names an `os` and an `architecture` in a short plain form.
4. **References.** Every config, layer and index entry must be stored under the name (`MANIFEST_BLOB_UNKNOWN` otherwise), with
   two exceptions: a layer with `urls` or a non-distributable media type is not required (foreign layers, Q-OCI-10), and the
   `subject` may name a manifest that does not exist (yet). An index entry whose manifest the journal replay withheld is refused
   the same way. The check runs before anything is stored and again in the commit transaction, on rows held against a removal
   (Q-OCI-61); the sizes of the descriptors are the pusher's claims and are not compared with the stored blobs (H-600).
5. **Digest.** The digest is the SHA-256 of the received bytes. A `PUT` by digest whose digest differs answers 400
   `DIGEST_INVALID`. A manifest whose digest the journal replay withheld under this name answers 409 `DENIED` until it is reinstated (R-OCI-033).
6. **Tag.** A `PUT` by tag points the tag at the manifest: a new tag or one that points at the same digest needs `deploy`,
   one that points at another digest also `overwrite` (R-OCI-006). The answer is 201 with `Location`
   (`/v2/<p>/<r>/<name>/manifests/<digest>`) and `Docker-Content-Digest`.
7. **Subject.** A manifest with `subject` is answered with `OCI-Subject: <subject digest>` (the registry processed the
   referrer, R-OCI-016).
8. **Commit.** The manifest item, the tag item (for a `PUT` by tag), the format's rows and the package version are written in
   one transaction (R-STOR-005); a concurrent push of the same tag ends with one of the two digests, never with a tag item and
   a version row that disagree.

- Pushing a tag that did not exist writes the audit event of a version publish; moving a tag writes the event of an
  overwrite (R-OCI-034): the commit of the tag item records `artifact.deployed` with `overwritten` set for a moved tag, and the
  write decisions `deploy` and `overwrite` are recorded with it (Q-OCI-65).
- A push by tag whose manifest is stored already links the tag item to the stored manifest and writes no second copy; the
  commit of a new manifest is the tag item as the lead and the manifest item as its companion (Q-OCI-61).
- The name's first push, by digest or by tag, creates its package row and is the first version for the ownership rule and the
  shadowing check (Q-OCI-63); a push by digest creates no version, a tag does.
- Extra tags in the query (`?tag=`) are not supported in this phase: the parameter answers 400 `UNSUPPORTED`; clients fall back
  to one `PUT` per tag.

Acceptance criteria:

- A schema 1 manifest answers 400 `MANIFEST_INVALID`; a 4 MiB + 1 byte manifest answers 413 `SIZE_INVALID`; a 4 MiB manifest
  is accepted.
- A manifest whose layer was never uploaded answers 400 `MANIFEST_BLOB_UNKNOWN`; the same manifest with that layer carrying
  `urls` and the media type `application/vnd.docker.image.rootfs.foreign.diff.tar.gzip` is accepted.
- A manifest whose `mediaType` field says `…index.v1+json` sent with `Content-Type: …manifest.v1+json` answers 400
  `MANIFEST_INVALID`.
- An artifact manifest with `artifactType: application/vnd.example+json` and the empty config descriptor is accepted and pulled
  back byte for byte.
- Two concurrent pushes of different manifests as `app:1` leave one digest under the tag and one version row naming it.

### R-OCI-014 · Multi-platform images
Priority: **P1** · Covers: PKG-DOCKER-023, PKG-DOCKER-025

- An index (OCI image index or Docker manifest list) is pushed after the manifests it lists, each by digest; its entries'
  `platform` objects are recorded on the references from the index to the children (R-OCI-007).
- Index entries that point at attestation manifests (`platform` `unknown/unknown` with the annotation
  `vnd.docker.reference.type: attestation-manifest`) are ordinary entries for the registry.
- An index and the children only it references form one unit for deletion (R-OCI-018) and retention (R-STOR-039).
- Overwriting a tag that points at an index with another index or with a single-platform manifest moves only the tag; the old
  index and its children stay as untagged manifests (R-OCI-018).

Acceptance criteria:

- `docker buildx build --platform linux/amd64,linux/arm64 -t trove.example.com/web/docker-local/multi:1 --push .` (a `FROM
  scratch` image, no emulation needed) succeeds; `docker pull --platform linux/arm64 …/multi:1` pulls the arm64 manifest;
  `podman manifest inspect …/multi:1` lists both platforms.
- `podman manifest create`, `podman build --platform linux/amd64,linux/arm64 --manifest m .` and `podman manifest push --all m
  …/docker-local/pm:1` store an OCI image index; `docker manifest create … --amend …` and `docker manifest push` store a Docker
  manifest list.

### R-OCI-015 · Tags list
Priority: **P1** · Covers: PKG-DOCKER-022, PKG-OCI-006

- `GET …/<name>/tags/list?n=<int>&last=<tag>` answers `{"name": "<p>/<r>/<name>", "tags": [...]}` in lexical (byte) order;
  `n` caps the page at 1,000 and defaults to 1,000; `last` starts after that tag. When more tags follow, the answer carries
  `Link: </v2/<p>/<r>/<name>/tags/list?n=<n>&last=<last tag>>; rel="next"`. `n=0` answers an empty list.
- A name the repository holds no tag of, but some manifest of, answers an empty list; a name it holds nothing of answers 404
  `NAME_UNKNOWN`.
- Only tags the caller may read are listed (R-OCI-006).

Acceptance criteria:

- With the tags `a`, `b`, `c`, `n=2` answers `a`, `b` and a `Link` to `last=b`; following it answers `c` without `Link`.
- The conformance suite's content-discovery category passes (R-OCI-035).

### R-OCI-016 · Referrers API
Priority: **P1** · Covers: PKG-OCI-009, PKG-OCI-010, PKG-OCI-011

- `GET …/<name>/referrers/<digest>` answers 200 with an OCI image index (`Content-Type: application/vnd.oci.image.index.v1+json`)
  of one descriptor per manifest of the name whose `subject` is the digest: `mediaType`, `size`, `digest`, `artifactType` (the
  manifest's own, else its config's media type; none for an index without one) and its `annotations`.
- No referrer, or a subject that does not exist: an empty index with 200. An invalid digest answers 400 `DIGEST_INVALID`.
- `?artifactType=<type>` filters and the answer carries `OCI-Filters-Applied: artifactType`.
- Pages hold at most 1,000 descriptors or 4 MiB; a longer list continues with `Link: <…>; rel="next"`, which repeats the
  `artifactType` filter and carries `last=<digest>`, the digest the page ended at (the list is in digest order). An answer that
  holds a manifest whose annotations were cut at 64 KiB when it was recorded (R-OCI-007) carries `Trove-Annotations-Cut: true`
  (Q-OCI-70). A manifest the journal replay withheld is not listed (R-OCI-033).
- The list is read from the rows of R-OCI-007, so concurrent pushes of referrers to one subject never lose one: each push
  inserts its own row.
- The referrers tag schema (`sha256-<hex>` tags) is not maintained by trove: the API exists from the start, so clients never
  fall back to it; a client that pushes such a tag stores an ordinary tag.

Acceptance criteria:

- Two signatures pushed concurrently with `subject` set to `app:1`'s digest both appear in `GET …/app/referrers/<d>`; with
  `?artifactType=application/vnd.dev.cosign.artifact.sig.v1+json` only matching ones are listed and the header is set.
- The conformance suite's referrers specifications pass (R-OCI-035).

### R-OCI-017 · Catalog
Priority: **P1** · Covers: PKG-DOCKER-003, PKG-DOCKER-022

- `GET /v2/_catalog?n=&last=` answers `{"repositories": [...]}` with the full names `<projectKey>/<repoKey>/<name>` of every
  image name the caller may read in local repositories and in the caches of remote repositories of the format, in lexical
  order, with `n` capped at 1,000 and the `Link` rule of R-OCI-015 (Q-OCI-19). Virtual repositories are not expanded: their
  members appear under their own names.
- The data port gives the registry root one page per repository: the image names that hold a manifest row, in byte order after a
  cursor (Q-OCI-75); the root merges the pages of the repositories the caller may read and decides each name.
- On a bound host the catalog lists the names of the bound repository only, without the prefix.
- A registry token needs the `registry:catalog:*` grant; a direct trove token needs nothing more than its read grants.

Acceptance criteria:

- A caller with `read` on `web/docker-local` only gets `web/docker-local/app` and no name of `web/team-local`; anonymous with no
  anonymous grant gets an empty list.

### R-OCI-018 · Deletion
Priority: **P1** · Covers: PKG-DOCKER-025, PKG-DOCKER-031, PKG-OCI-006, PKG-OCI-012

The content-management category of the distribution specification is supported (Q-OCI-11):

| Request | Effect | Answer |
|---|---|---|
| `DELETE …/manifests/<tag>` | removes the tag item and its version; the manifest stays | 202 |
| `DELETE …/manifests/<digest>` | removes the manifest, every tag of the name that points at it, its referrers, and, for an index, every child manifest that no other manifest or tag of the name references | 202 |
| `DELETE …/manifests/<digest>` of a manifest that an index of the name lists | refused, the index must go first | 409 `DENIED` |
| `DELETE …/blobs/<digest>` of a blob no manifest of the name references | removes the blob item | 202 |
| `DELETE …/blobs/<digest>` of a referenced blob | refused | 409 `DENIED` |
| `DELETE` of a manifest or a blob on a remote or a virtual repository | not offered: a remote's cache is evicted through `/files/` or the management API (R-REPO-045), a virtual repository routes no deletes (cancelling an upload session is not a deletion, R-OCI-011) | 405 `UNSUPPORTED` |

- Removals go through the item store and the hook `OnItemsRemoved`, so a removal through the package screens, `/files/` or a
  retention policy (R-STOR-039) applies the same cascade and the same refusals.
- Removed items go to the trash can when it is enabled (R-STOR-022). The format's rows stay while an item is in the trash (R-OCI-007),
  so a restore from it brings the item back with its rows and needs no hook. A hook that cascades follows the reason of its
  event: a manifest that goes to the trash takes its tags to the trash with it and not out of the repository for good
  (`ItemsEvent.Cascade`, Q-OCI-24).
- A removal that has no request (a retention run, the purge of the trash, `/files/` through a job) runs the same hook with the same
  cascade, recorded as the system actor `removal` (Q-OCI-24).
- The refusals and the cascade are the hook's (Q-OCI-73): a manifest an index of the name lists, and a blob a manifest references, are
  refused with 409 `DENIED` unless the same removal takes the referencing manifest as well; a referrer that an index lists stays when
  its subject goes; each removed manifest writes `oci.manifest.deleted` (R-OCI-033). Deleting a tag removes its version; deleting a
  blob changes no row of the format and raises no revision.
- Untagged manifests are kept: a tag moved away leaves its old manifest pullable by digest until it is deleted or a retention
  policy removes it.

Acceptance criteria:

- Deleting a multi-platform index by digest removes the index and both children; a child that a second index also lists stays.
- `DELETE` of a child digest while its index exists answers 409 `DENIED`.
- The conformance suite's content-management category passes with `OCI_DELETE_MANIFEST_BEFORE_BLOBS=1` (R-OCI-035).

### R-OCI-019 · Unreferenced blobs
Priority: **P1** · Covers: PKG-DOCKER-019, PKG-DOCKER-031

- A blob item of a name that no manifest of the name references, and that is older than
  `oci.unreferenced_blob_grace` (# default (proposed) `24h`; it must exceed `storage.uploads.session_max_age`), is removed by
  the periodic job `oci-sweep` (queue `maintenance`, every hour, `exclusive`; R-ARCH-016). The grace keeps the blobs of a push
  whose manifest has not arrived yet, measured from the creation of the blob item: a blob that is older and is reused is not
  protected ([H-643](../../security/oci-content.md#h-643--a-blob-reused-after-the-grace-can-be-swept-before-its-manifest-arrives)).
- The job removes each blob item in a transaction of its own that locks the package row of the image name when it has one (a name
  that holds only blobs has none, and none is made: the rows of the blobs arbitrate) and reads the references again, and it leaves a blob whose digest an unfinished upload session of the name holds (Q-OCI-71); it removes through
  the item store as the system actor `removal`, so the audit trail and the refusals of R-OCI-018 apply. The task `oci-sweep` is
  scheduled in `maintenance.tasks` (default `0 * * * *`), and `trove_oci_sweep_blobs_removed_total` counts what it removed
  (R-OCI-034).
- The blob object itself is collected by the garbage collection of R-STOR-024 once no item references it.
- A remote's cached blobs are not swept: they are cache entries, evicted by R-STOR-045.

Acceptance criteria:

- A blob uploaded and never referenced is gone from `<name>/_blobs/` after the grace and a sweep (fake clock), and the object
  after the next collection; a blob referenced by a manifest stays.

### R-OCI-020 · Limits for untrusted content
Priority: **P1** · Covers: PKG-OCI-007

Every manifest, pushed or fetched, is untrusted input (R-ARCH-028):

- A manifest is read by the manifest parser (`internal/formats/oci/manifest`, Q-OCI-60) within 4 MiB (`Descriptor.MaxDocumentBytes`),
  JSON nesting of 32 levels, at most 10,000 descriptors and 100,000 JSON values, after the scan of `untrusted.CheckJSON`; annotations
  count towards the bytes. A bound that is crossed is 413 `SIZE_INVALID`, a document the parser will not read 400 `MANIFEST_INVALID`.
- Every digest in a descriptor is validated by the grammar of R-OCI-002 before it is used as a path segment; nothing of a
  manifest is ever used as a file-system path.
- A fetched manifest is accepted only when its SHA-256 equals the digest it was asked by, or the `Docker-Content-Digest` the
  upstream announced for a tag; otherwise the fill answers 502 and stores nothing (R-REPO-041).
- Tags list and catalog documents of an upstream are read within 4 MiB and 100,000 entries; a referrers answer of an upstream
  within 4 MiB.
- Token responses of an upstream's realm are read within 64 KiB; a token longer than 16 KiB is refused.
- Blobs are not parsed by the registry; layers are never unpacked.

Acceptance criteria:

- The conformance kit's crafted inputs for the format (a manifest nested 100,000 deep, one with 20,000 layers, a 5 MiB index,
  a descriptor digest `sha256:../../x`) are refused within 5 s with 400 or 413 and the replica's RSS stays within 512 MiB of its
  baseline.

### R-OCI-021 · Remote repository: upstream and its token handshake
Priority: **P1** · Covers: PKG-DOCKER-033, PKG-DOCKER-038, PKG-OCI-003

- `remote.url` is the registry's origin, optionally with a path that is a **namespace prefix**: every image name is requested
  as `<path>/<name>` (`https://ghcr.io/acme` serves `app` from `ghcr.io/acme/app`; another trove's virtual repository is
  `https://other.example.com/web/docker`) (Q-OCI-13).
- **Token handshake** (R-REPO-033: the protocol's handshake belongs to the format, its I/O to the core). An upstream answer 401
  with `WWW-Authenticate: Bearer realm=…,service=…[,scope=…]` makes the remote engine ask the format for the token request
  (the realm, the service, the scope `repository:<prefixed name>:pull`), send it through the repository's upstream client and
  retry the request once with `Authorization: Bearer <token>`. The realm's host must be the upstream origin or an
  `extraHosts` entry (`foreign_host_denied` otherwise); the repository's credential goes to the realm as Basic only when that
  host is the upstream origin or an `extraHosts` entry with `sendCredentials: true` (R-REPO-033), otherwise the request is
  anonymous. A `Basic` challenge is answered with the repository's credential under the same host rule.
- **Upstream tokens** are kept in the replica's memory per repository, configuration version, scope, token service and credential,
  until 10 s before the answer's `expires_in` (60 s when absent); they are never written to PostgreSQL or Valkey (R-OCI-032). The
  engine remembers the last challenge of the repository and asks the format before a request, so that only the first request of a
  replica and configuration version is challenged ([Q-OCI-82](#q-oci-82--the-engine-asks-the-format-before-a-request-with-the-last-challenge-so-only-the-first-request-of-a-replica-is-challenged)): the
  adapter takes the scope from the request and only the realm and the service from the challenge, the engine uses a token of that
  scope from the cache or fetches one first, and sends the request with it. The remembered challenge is only a hint and never the
  answer: when the pre-emptive handshake fails the request is sent without a token, and a `401` runs the handshake with its own
  challenge, which replaces the remembered one. A cached token that the registry refuses is dropped and the handshake runs once more;
  a fresh token that is refused leaves the `401` as the answer when the `401`'s own challenge names the same token service and key.
  The token goes to the registry's origin only (R-REPO-033).
- **Default extra hosts** proposed for the well-known upstream (R-REPO-024): `auth.docker.io` with `sendCredentials: true`
  (Docker Hub's realm), and, without credentials, the hosts Docker Hub redirects blob downloads to. The remote work package
  confirms the current redirect hosts against Docker Hub and records them here before the default ships (Q-OCI-13); the catalog
  gains the list of default hosts that receive credentials (`catalog.Facts.DefaultCredentialHosts`, an additive fact).
- `authMode: bearer` sends the stored secret as a static bearer token and runs no handshake.
- A write (blob upload, manifest push) to a remote repository answers 405 `UNSUPPORTED`.

Acceptance criteria (integration, the counting stub registry of R-OCI-035):

- Against the stub with its realm on a second host and `authMode: basic`, a pull through `docker-hub` succeeds; the realm records
  `Authorization: Basic …` and the registry host records only `Bearer` after the first 401.
- With the realm host not in `extraHosts`, the fill answers 502 `foreign_host_denied` and the realm host records nothing.
- Within the token's lifetime a second pull of another layer sends no token request; on another replica it sends one.

### R-OCI-022 · Remote repository: manifests by tag and by digest
Priority: **P1** · Covers: PKG-DOCKER-017, PKG-DOCKER-034

- A manifest by tag is the metadata document `<name>/_tags/<tag>`: fetched with `GET /v2/<prefix/name>/manifests/<tag>` and
  `Accept` listing the four manifest types of R-OCI-013 (so the upstream returns its index, not a platform manifest it picked),
  stored byte for byte with the upstream's `Content-Type` (kept in the cache entry's `media_type` and served as the stored media
  type), and fresh for the metadata period (# default (proposed) 300 s).
- **Revalidation by `HEAD`** (Q-OCI-13). An expired tag document is revalidated with `HEAD` on the same URL: a
  `Docker-Content-Digest` equal to the stored manifest's digest renews its freshness without a body; another digest, or an
  answer without the header, fetches the manifest with a `GET` that carries no validator of the stored copy; a `404`, `410`, `429` or `5xx` to the `HEAD` is read by the engine's
  rules for a failed revalidation ([Q-OCI-81](#q-oci-81--a-head-that-is-not-a-2xx-is-the-engines-answer-and-a-2xx-that-is-not-unchanged-is-a-get)).
  Docker Hub counts `GET` of manifests against its pull limit and
  not `HEAD` ([Docker Hub usage](https://docs.docker.com/docker-hub/usage/pulls/)), so an unchanged tag costs no pull.
- A manifest fetched by tag is also served by its digest from the cache: a later `GET …/manifests/<digest>` sends no upstream
  request. The format names the digest path as an alias of the fetched document (`Prepared.Aliases`), and the engine enters it in
  the fill transaction as an item for the same blob and an artifact cache entry (Q-OCI-25); the manifest row is written for the alias
  by the hook, with the origin `fill`.
- A manifest by digest is content: fetched once, verified against the digest (R-OCI-020), never revalidated.
- A tag the upstream does not have answers 404 `MANIFEST_UNKNOWN` and is remembered for the not-found period (# default
  (proposed) 300 s, R-REPO-028).
- The upstream's schema 1 manifests are not served: the fill answers 502 with `MANIFEST_INVALID` in the detail and stores
  nothing.

Acceptance criteria:

- `docker pull …/docker-hub/library/alpine:3` twice within 300 s: the stub records one `GET` of the tag manifest. After 300 s
  with the tag unchanged, the next pull records one `HEAD` and no `GET`; after the stub moves the tag, one `HEAD` and one
  `GET`.
- `crane manifest …/docker-hub/library/alpine@<digest of the tag>` after the tag pull records no upstream request.
- The stub answering a tag with bytes whose SHA-256 differs from its `Docker-Content-Digest` makes the pull fail with 502 and
  stores nothing.

### R-OCI-023 · Remote repository: blobs
Priority: **P1** · Covers: PKG-DOCKER-017, PKG-DOCKER-019

- A blob is content `<name>/_blobs/<digest>`, filled through the remote request path (R-REPO-025) with the cross-replica
  flight of R-REPO-026: concurrent pulls of one layer on several replicas cause one upstream request. The fill verifies the
  SHA-256 against the digest (R-REPO-041, `VerifyAnnouncedDigests`).
- Upstream redirects (R-REPO-039) are followed within the host rules; credentials never go to a redirect target that is not a
  `sendCredentials` host.
- `HEAD` of a blob that is not cached is answered from the upstream's `HEAD` without a fill.
- A foreign layer (a digest the upstream does not serve) answers what the upstream answers; trove does not fetch `urls`
  (Deferred, PKG-DOCKER-036).

Acceptance criteria:

- Six concurrent `docker pull` of one image through `docker` on three replicas: the stub records each layer once.
- The stub redirecting blobs to its third host (the blob CDN, after the registry and the realm): the layer is cached and the CDN host records no `Authorization`.

### R-OCI-024 · Remote repository: tags list and referrers
Priority: **P1** · Covers: PKG-DOCKER-022, PKG-OCI-009

- The tags list of a name is the metadata document `<name>/_taglist`: the upstream's answer to `tags/list?n=10000`, kept for the
  metadata period. It is the package document of the format (R-ARCH-008): an upstream 404 marks the name gone (R-REPO-029) and
  it is what the shadowing check probes (R-REPO-048).
- A client's `tags/list` with `n` and `last` is answered from that document when it holds the requested window; a window
  beyond a document the upstream truncated (its answer had a `Link`) is passed through to the upstream as a volatile query
  (R-ARCH-013), with the upstream's `Link` rewritten to trove's address. Offline or with the circuit open, the stored document
  answers.
- Referrers of a remote are passed through to the upstream's referrers API as a volatile query, cached for the metadata period;
  an upstream 404 is answered 404, so that the client falls back to the tag schema through the same remote (Q-OCI-15). The
  manifests a client then fetches by digest are cached like every manifest.

Acceptance criteria:

- `curl …/docker-hub/library/alpine/tags/list?n=5` answers the stub's first five tags and a `Link` to trove's address; the
  stub records one request for the document within the period.
- Against a stub without the referrers API, `GET …/referrers/<d>` answers 404; against one with it, the stub's index.

### R-OCI-025 · Remote repository: Docker Hub
Priority: **P1** · Covers: PKG-DOCKER-034, PKG-DOCKER-035

- **`library/`.** For a remote whose upstream host is `registry-1.docker.io`, a one-component name `<n>` is the name
  `library/<n>`: in the upstream request, in the cached path and in the package row, so `…/docker-hub/nginx` and
  `…/docker-hub/library/nginx` resolve to one cached copy (Q-OCI-14). No other upstream gets the mapping.
- **Rate limits.** An upstream `429` reaches clients as R-REPO-029 says (the stored copy while it lasts, else 503 with
  `Retry-After`, code `TOOMANYREQUESTS`, R-OCI-008) and reaches a mirror as a rate-limit answer of R-REPO-054. The quota header
  `RateLimit-Remaining` is read by its integer before the first `;`, so Docker Hub's `0;w=21600` counts as 0 (a correction of the
  parser of R-REPO-054 that every format shares).
- Authenticating the remote to Docker Hub (`authMode: basic` with a Docker Hub user and access token) is what the guide
  recommends for every Docker Hub remote that serves more than a few pulls per hour, and for every mirror (R-OCI-030).

Acceptance criteria:

- `docker pull …/docker-hub/nginx:1` and `docker pull …/docker-hub/library/nginx:1` store one tag item
  `library/nginx/_tags/1`; the stub (configured as `registry-1.docker.io` through the test resolver) records one manifest `GET`.
- The stub answering `429` with `RateLimit-Remaining: 0;w=21600` and no `Retry-After` to a pass is counted with the reason
  `quota_header` in `trove_mirror_rate_limited_total`.

### R-OCI-026 · Mirror settings of an OCI remote
Priority: **P1** · Covers: trove-only

A remote repository of the format runs in either mode of R-REPO-050. What a mirror of a registry discovers and keeps is in the
section [Mirror discovery](#mirror-discovery) (Q-OCI-16); its own settings are the member `oci.mirror` of the repository's
format settings (R-REPO-004), present in both modes and effective in mode `mirror`:

| Member (`oci.mirror.…`) | Meaning | Default (proposed) | Range |
|---|---|---|---|
| `tags` | Tag patterns (`*` and `?`, R-AUTHZ-005 syntax) a tag must match to be mirrored; a pattern without a wildcard names a tag that is always kept | `["latest"]` | 1–64 patterns |
| `excludeTags` | Tag patterns that are never mirrored | `[]` | 0–64 |
| `platforms` | Platforms (`os/arch` or `os/arch/variant`) of an index whose manifests are mirrored; empty is every platform | `[]` | 0–32 |

- The generic members keep their meaning: `maxVersionsPerPackage` is the number of newest tags kept per image among the tags
  matched by a wildcard pattern, by the tag order of Q-OCI-17; `includePrereleases` decides about tags the tag order marks as
  pre-releases; `maxBytes` bounds the repository's bytes (R-REPO-052).
- `oci.mirror` decides what the mirror covers like the patterns do, so the rule of R-REPO-050 that keeps `includePatterns`,
  `excludePatterns`, `remote.url` and `remote.extraHosts` for an Owner without `repositories:write-config` keeps
  `<format>.mirror` as well (Q-OCI-22).
- Docker Hub as upstream: R-REPO-052's bound applies unchanged (the include list names images, or `maxBytes`); the default
  `tags: ["latest"]` bounds the tags of each image.

Acceptance criteria:

- A remote created without `oci` reads back `"oci": {"mirror": {"tags": ["latest"], "excludeTags": [], "platforms": []}}`.
- `"platforms": ["linux"]` answers 422 `invalid_value` at `/oci/mirror/platforms/0`.
- An Owner of project `web` without `repositories:write-config` patching `oci.mirror.tags` of the mirror `docker-hub-mirror`
  gets 422 `mirror_restricted`.

### R-OCI-027 · Virtual repository: resolution
Priority: **P1** · Covers: PKG-DOCKER-039, PKG-OCI-004

Virtual resolution follows R-REPO-019 with these format rules (Q-OCI-18):

| Request | Strategy |
|---|---|
| Manifest by tag (`GET`, `HEAD`) | The first member that has the tag, local members first in member order, then remote members in member order, each remote through the remote request path (fresh copy, revalidation or upstream fetch for a caller who may fill it, R-REPO-025); a remote member's stored tag is not preferred over an earlier member's upstream |
| Manifest or blob by digest | The three artifact tiers of R-REPO-019 step 6: identical bytes wherever they are found |
| Tags list | The union of the members' tags of the name, sorted, deduplicated, paged by R-OCI-015 |
| Referrers | The union of the members' descriptors for the digest, deduplicated by digest, filtered and paged by R-OCI-016 |
| Catalog | Not answered for a virtual repository (R-OCI-017) |

- Pin rules and namespace claims (R-REPO-023) apply to image names as the client addresses them at the virtual repository:
  a claim of `acme/*` for `docker-local` keeps every remote member from serving `acme/app` through `docker` (dependency
  confusion for images). A remote member applies its own `library/` mapping (R-OCI-025) after the claim decision.
- `Trove-Resolved-From` names the member that answered (R-REPO-019).
- Tag resolution by push time across members (PKG-DOCKER-040) is not offered in this phase (Deferred).

Acceptance criteria:

- `app:1` in `docker-local` and in the stub behind `docker-hub`: `docker pull …/docker/app:1` gets the local image
  (`Trove-Resolved-From: docker-local`).
- A claim `acme/*` for `docker-local`: `…/docker/acme/tool:1`, which only the stub has, answers 404, and the stub records no
  request.
- `tags/list` through `docker` lists the union of `docker-local`'s and the stub's tags of the name.

### R-OCI-028 · Virtual repository: pushes through a virtual repository
Priority: **P1** · Covers: PKG-DOCKER-018, PKG-DOCKER-039

- Uploads, mounts and manifest pushes to a virtual repository are executed on its deploy target (R-REPO-021); without one they
  answer 405 `UNSUPPORTED` naming the missing deploy target. Upload locations stay below the virtual repository's address and
  the session records the target (R-OCI-011).
- **Linking from members** (Q-OCI-18). Docker asks `HEAD …/blobs/<d>` before uploading and skips a blob the virtual answers
  200 for, which a member other than the target may have answered. A manifest push through the virtual repository therefore
  links each referenced blob or manifest that the target lacks from the member that holds it (a local member, or a remote
  member's cache, never an upstream fetch), when the caller may read it there, before the reference check of R-OCI-013 step 4;
  only then is a missing one `MANIFEST_BLOB_UNKNOWN`.
- `DELETE` of a manifest or a blob through a virtual repository answers 405 `UNSUPPORTED`; cancelling an upload session
  through it acts on the session's target (R-OCI-011).

Acceptance criteria:

- `docker push …/docker/app:2` stores the image in `docker-local`; an image based on a layer that `docker-hub` cached is pushed
  through `docker` without uploading that layer, and `docker pull …/docker-local/app:2` succeeds.
- With the deploy target removed, `docker push …/docker/app:3` fails and the first upload answers 405 `UNSUPPORTED`.

### R-OCI-029 · Bound hosts: the Docker Engine mirror
Priority: **P1** · Covers: PKG-DOCKER-005, PKG-DOCKER-008

- On a host whose root is bound to an OCI repository R (`host_roots`, R-EP-006), `/v2/<name>/…` is the distribution API of R
  with the image name as sent, the token service and the version check are that host's, and scopes name `<name>` without a
  prefix (R-EP-007). A Docker Engine with `registry-mirrors: ["https://mirror.example.com"]` pulls Docker Hub images through R.
- The format declares `/v2/` as its bound-host prefix; the core's dispatcher fixes the repository from the binding and runs
  authorization and exposure on it exactly as for an unbound address.
- A binding to a repository that does not exist or is not of the format behaves as no binding (R-EP-006).

Acceptance criteria:

- With `host_roots: {mirror.example.com: web/docker-hub}`, a Docker Engine configured with that mirror pulls `alpine:3`, and the
  stub records one manifest request (R-EP-007).
- On `mirror.example.com`, `GET /v2/web/docker-hub/library/alpine/manifests/3` reads the name `web/docker-hub/library/alpine` in
  R and answers 404 `NAME_UNKNOWN`.

### R-OCI-030 · Client setup snippets and guide
Priority: **P1** · Covers: PKG-DOCKER-011, PKG-DOCKER-013, PKG-DOCKER-015, PKG-DOCKER-046, PKG-OCI-013, PKG-OCI-014, PKG-OCI-019

- Clients registered (`Descriptor.Clients`, FMT-002): `docker` (Docker CLI with Buildx) and `podman`, each with the versions
  the e2e suite pins (R-OCI-035).
- Setup templates (R-API-030; no credential field, the token only by reference): `configure` (`docker login <host> -u <user>
  --password-stdin` reading `$TROVE_TOKEN`; `podman login` the same), `install` (`docker pull <image prefix>/<name>:<tag>`),
  `publish` (`docker tag` and `docker push`; `podman push`; a multi-platform `docker buildx build --push`), and a Kubernetes
  image pull secret (`kubectl create secret generic --type=kubernetes.io/dockerconfigjson` reading a docker config file that the shell's `printf` builds from the token variable and hands over on standard input, so the token is in no argument list). The image prefix
  `<host>/<projectKey>/<repoKey>` comes from the new field `SetupData.ImagePrefix`, filled by the core for the format whose
  base path is the registry root (Q-OCI-4).
- The guide (R-UI-049) covers every class and both clients with the required topics (kit check 8): what each class is, login,
  pull, push and multi-platform push, the Kubernetes secret, the Docker Engine mirror of a bound host, Docker Hub rate limits
  and why a remote of Docker Hub should be authenticated, and troubleshooting (`x509` without the CA, `denied` for a moved tag
  without `overwrite`).

Acceptance criteria:

- The setup API for `docker-local` and client `docker` answers snippets whose image references start with
  `trove.example.com/web/docker-local/`; the e2e suite runs every rendered snippet (as the npm matrix does) and each succeeds.

### R-OCI-031 · Package screens
Priority: **P1** · Covers: PKG-DOCKER-041, PKG-DOCKER-023

The format's view adapter (R-API-029) shows an image name as a package and its tags as versions, newest first by push time in a
local repository and by fetch time in a remote one (the list of versions is ordered by push, `Stored`; the precedence of two tags,
`Compare`, is the tag order, and decides which tag is "latest"): per tag the digest, the media type, the summed size, and for an index its
platforms with each child's digest and size, from the rows of R-OCI-007. Layer history and labels are not shown (Deferred).
The version `metadata` of a tag holds `digest`, `mediaType`, `size` (the summed size of the config and the layers; of an index the
sum over its children) and `artifactType` (the manifest's own, else the media type of its config, so every image shows its config type); an index adds `platforms`, one object per child with
`platform` (`os/arch[/variant]`, `unknown/unknown` for an attestation), `digest`, `mediaType` and `size`. `Compare` is the tag order
of the format (Q-OCI-17, Q-OCI-64), and a string that is no tag sorts before every tag; a tag whose manifest the journal replay withheld is not listed (R-OCI-033).

Acceptance criteria:

- The package screen of `docker-local/multi` shows the tag `1` with two platforms; the API returns them in `metadata`.

### R-OCI-032 · OCI on n replicas, without Valkey and during a drain
Priority: **P1** · Covers: trove-only

| State | Where | Across replicas | Valkey unavailable | Drain or crash of the replica |
|---|---|---|---|---|
| Manifests, tags, blobs, the format's rows | PostgreSQL and S3 | One copy | Unaffected | Unaffected; a cut push is retried by the client and commits once |
| Upload sessions | PostgreSQL and one S3 multipart upload (R-STOR-011) | Any replica continues | Unaffected | The claim is released on a drain, expires after a crash; the client resumes or restarts |
| Registry tokens | Nowhere: self-contained, verified with the key ring | Every replica verifies | Unaffected | Unaffected |
| Upstream tokens of remotes | The replica's memory | Each replica fetches its own | Unaffected | Gone; the next request runs the handshake |
| Concurrent fills of one blob | The cross-replica flight of R-REPO-026 | One upstream request | Each replica fetches once (R-ARCH-033) | The flight is released |

- Valkey grants nothing for the format: no manifest, tag, blob, session or permission is read from it.
- No session affinity is needed (INV-12): every request of a push may reach another replica.

Acceptance criteria:

- The e2e set `oci` runs in the `scatter` profile (R-ARCH-032): every push and pull of the client matrix succeeds with every
  request on another `packages` replica.
- With Valkey stopped, `docker push` and `docker pull` through local, remote and virtual repositories succeed (closing run).

### R-OCI-033 · OCI data in backups and restores
Priority: **P1** · Covers: trove-only

- `oci_manifests`, `oci_references` and `oci_tombstones` have the backup export class `data`; `upload_sessions` and
  `upload_session_parts` are `transient` (R-STOR-011): a client restarts an upload after a restore.
- Tags are mutable and digests verify themselves, so a restore keeps no reservation for a push: a tag moved after the snapshot
  points where it pointed at the snapshot, and a digest can never come back with other bytes.
- **A deleted image does not come back** (Q-OCI-30, Q-OCI-33). An image that was deleted may have been malicious, and a restore
  returns the repository to the snapshot. The deletion of a manifest writes the audit event `oci.manifest.deleted` in the transaction
  of the deletion; the event is in the revocation journal (R-HA-054) with an entry that names the repository, the image name, the
  digest and the actor. A live deletion writes no mark, so a digest that was deleted can be pushed again.
- **Replay withholds and never deletes** (R-HA-054, as npm's does, R-NPM-047). When the restored repository holds the manifest under
  the name, the replay writes its mark in `oci_tombstones` and increments `packages.rev_seq` in one transaction (`applied`); the items
  and their bytes stay stored. A manifest that is withheld already is `already in effect`; one the restored repository does not hold
  is `subject unknown` and changes nothing, so a forged or mistaken entry costs availability until an administrator acts, never
  integrity, access or data.
- **What withheld means.** The serve check of the format (`ServeCheck`) refuses a manifest item or a tag item whose digest is marked
  with the 404 `MANIFEST_UNKNOWN` of a missing manifest, on `/v2/` and on `/files/`, and a push of a manifest with a marked digest
  under the same name answers 409 `DENIED` until the mark is removed (the same bytes cannot be pushed back around a withholding).
  The check reads the manifest row and the mark with the item in the one statement of the download (the serve-facts kind `oci_local`
  of R-ARCH-027, Q-OCI-33), so the hottest route costs no further query. A tag that points at a withheld manifest answers the same.
  The tags list omits a tag whose manifest is withheld. A withheld manifest counts toward the repository's storage like a live one.
- **The way back.** The replay output lists every withheld manifest. An administrator confirms the withdrawal by deleting the
  manifest through the management item API (R-STOR-018; the mark stays, so the digest stays refused), or reverses it with
  `trove admin oci reinstate <projectKey>/<repoKey> <name>@<digest>`, which removes the mark only while the manifest row and its items
  are stored with that digest (so it brings back the same bytes and never admits new ones), increments `rev_seq` and writes
  `oci.reinstate` (not journaled, as `npm.reinstate`). There is no other way to push a digest back: reinstating is the explicit
  allowance.
- The event, the table, the journal registration, the serve-facts kind, the replay handler and the command are built; the entry of the
  event is `{"repository": <id>, "name", "digest", "actorId"}`, a claim that the replay checks against the restored database.

Acceptance criteria:

- The restore drill of R-BAK-029 includes an image pushed before the snapshot: after the restore `docker pull` by tag and by
  digest succeeds and `tags/list` lists its tags.
- An image deleted after the snapshot is withheld after the restore: `docker pull` by tag and by digest answers 404, the replay
  reports the entry as applied, the items are still stored, and a push of the same digest answers 409 `DENIED` until
  `trove admin oci reinstate` brings it back.

### R-OCI-034 · Audit events and metrics
Priority: **P1** · Covers: trove-only

- Writes record the authorization events of R-AUTHZ-029 like every format (`authz.write.allowed` with `deploy`, `overwrite` or
  `delete`; a remote fill with `populate`). Tag moves are writes with `overwrite`.
- The token service writes `auth.registry_token.denied` for a token request the authentication stage refused with 401, as an
  access event with the endpoint in its column, deduplicated per client address, endpoint, reason and form of the credential
  within `audit.token_rejection_dedup_window` (R-OPS-015), and no event per issued token; issued tokens are counted. A rejected
  credential also counts in the failed-credential throttle of R-AUTH-038, like every authentication failure; a request without
  a credential does not (Q-OCI-53).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `trove_oci_registry_tokens_total` | counter | `outcome` (`issued`, `anonymous`, `denied`) | Token service answers |
| `trove_storage_upload_sessions_total` | counter | `outcome` (`completed`, `aborted`, `expired`) | Upload sessions by how they ended; a storage family, counted where the sessions end ([Q-STOR-173](../storage.md#q-stor-173--the-sessions-are-counted-by-trove_storage_upload_sessions_total-not-by-an-oci-metric)) |
| `trove_oci_upstream_token_requests_total` | counter | `repo`, `outcome` (`ok`, `denied`, `failed`) | Handshakes of remote repositories |
| `trove_oci_sweep_blobs_removed_total` | counter | — | Blob items removed by `oci-sweep` |

Acceptance criteria:

- After the e2e matrix, `trove_oci_registry_tokens_total{outcome="issued"}` grew and the request log contains no
  `trv_rgt_` substring followed by more than 4 characters.

### R-OCI-035 · Test suites
Priority: **P1** · Covers: trove-only

- **Unit**: the grammars of R-OCI-002, the manifest parser and its crafted inputs (R-OCI-020), the registry token's form and
  verification (R-OCI-005), the scope grammar, the tag order of Q-OCI-17 against a table of real tag sets, the mirror plan
  against fixtures, the format conformance kit of R-ARCH-010 with no check skipped.
- **Counting stub registry** (`test/ocistub`): a deterministic image set generated in code (single-platform images and indexes
  in Docker and OCI media types, attestation entries, an image with 2,500 tags in four pages, artifacts with `subject`), a token
  realm on a second host with anonymous and Basic modes, blob redirects to a third host, `_catalog` and the referrers API each
  behind a switch, counters per host, method and path, and failure modes: down, a delay per response, a tag moved, a manifest
  whose bytes do not match its `Docker-Content-Digest`, a schema 1 tag, `429` with Docker Hub's headers and no `Retry-After`,
  and the throttling modes of R-REPO-059.
- **Integration** (`test/integration/oci`, against the stack of R-HA-051: PostgreSQL, Valkey with Sentinel, S3, several
  replicas): every acceptance criterion of this page that names the protocol, through HTTP; the action map
  (`TestR_AUTHZ_007_OCIActionMap`); remote and virtual against the stub; mirror passes in `test/integration/mirror` (the OCI
  criteria of [Mirror discovery](#mirror-discovery)).
- **The OCI distribution conformance suite** (`github.com/opencontainers/distribution-spec/conformance`, tag `v1.1.1`, Apache
  License 2.0): built with `go test -c` from the pinned tag by the repository's Go toolchain (`make oci-conformance`, the tag
  pinned once in the Makefile), and run against a replica of the integration stack with `OCI_TEST_PULL`, `OCI_TEST_PUSH`,
  `OCI_TEST_CONTENT_DISCOVERY` and `OCI_TEST_CONTENT_MANAGEMENT` set, `OCI_NAMESPACE=it/oci-local/conformance`,
  `OCI_CROSSMOUNT_NAMESPACE=it/oci-other/conformance`, `OCI_AUTOMATIC_CROSSMOUNT=0`, `OCI_DELETE_MANIFEST_BEFORE_BLOBS=1`,
  `OCI_HIDE_SKIPPED_WORKFLOWS=0`; a run of each category through a virtual repository's address as well. Its JUnit report is a
  CI artifact; every specification passes and none is skipped beyond the switches above (Q-OCI-21).
- **e2e** (kind, chart, the `scatter` profile, three `packages` replicas): a client pod with the Docker CLI (dockerd in the pod,
  Buildx included) and Podman, both pinned in the Makefile and trusting the dev CA. Set `oci` (every change that touches the
  format or the registry root): login, push and pull with both clients through local, remote `pullThrough`, remote `mirror`
  and virtual repositories; multi-platform push with Buildx, `docker manifest` and Podman; push through the virtual repository;
  anonymous pull; a Kubernetes pod whose image comes from the virtual repository with an image pull secret; the Docker Engine
  mirror of a bound host; the rendered setup snippets. Set `oci-long` (nightly, R-HA-052): a push whose upload is cut by a
  drain (R-OCI-011). The set `mirror` gains an OCI mirror of the stub on two worker processes while clients pull through it, and
  `mirror-long` the drain of the node of the process that runs an OCI pass (R-REPO-058, R-REPO-059).
- **Closing run** (the full run of Test and Release on the commit that closes the phase): the set `oci` with Valkey stopped,
  and a read-only check against Docker Hub through a `pullThrough` remote and a mirror of `library/busybox` with `tags:
  ["latest"]` (at most six manifest `GET` requests in total, anonymous).

Acceptance criteria: the suites are green in CI for every change that touches the format, the registry root or the token
service, and the closing run is green.

## Mirror discovery

What an OCI remote in mode `mirror` discovers and stores ([repositories-and-proxy.md](../repositories-and-proxy.md) R-REPO-050
to R-REPO-052; tests name R-REPO-051). The engine, the bounds and the throttling are the core's; this section is what the format
contributes through the mirror lister (R-ARCH-008, the optional interface `MirrorLister`). A registry cannot be mirrored
whole: Docker Hub alone holds millions of image names and many of them thousands of tags. The mirror keeps what the operator
named (Q-OCI-16).

- **What a registry offers.** The distribution specification has no change feed. `GET /v2/_catalog` lists names with `n` and
  `last` where a registry offers it; Docker Hub does not. `GET /v2/<name>/tags/list` lists the tags of one name, in pages with
  `n`, `last` and `Link`. A manifest names its blobs; an index names one manifest per platform.
- **Methods** (`discover.Method`):
  - `feed`: not offered.
  - `listing`, offered when the upstream answers `GET /v2/_catalog?n=1` with 200 and a `repositories` array: pages of
    `n=1000` following `Link`, else `last`; each name below the remote's namespace prefix (R-OCI-021) loses the prefix, is
    checked against the include and exclude patterns and the claims, and is returned. A catalog that ends with a page shorter
    than `n` and no `Link` is complete. Every pass is full.
  - `names`: the include patterns without a wildcard, read as image names (`library/nginx`, `bitnami/redis`), and, as for every
    method, the names the repository holds. For Docker Hub this is the only method; a one-component pattern `nginx` is the name
    `library/nginx` (R-OCI-025).
- **The plan of a name.** The format reads the tags list through `discover.Upstream.Get` (pages of 1,000, at most 100,000 tags;
  a name with more fails its plan with the reason `too_many_tags`), keeps the tags that match `oci.mirror.tags` and none of
  `oci.mirror.excludeTags`, and orders them newest first by the tag order of Q-OCI-17, the exact tag patterns marked
  `Pinned`. For each kept tag:
  1. the tag document `<name>/_tags/<tag>` through `discover.Upstream.Document` (stored before the attempt: revalidated by `HEAD`,
     R-OCI-022, so an unchanged tag costs no manifest `GET`);
  2. for an index, the child manifests whose platform is in `oci.mirror.platforms` (all when empty), plus each attestation entry
     whose `vnd.docker.reference.digest` names a kept child, each read through the new `discover.Upstream.Artifact` (filled and
     verified like any artifact, then read back within 4 MiB; a stored one costs no request);
  3. the items: the selected child manifests and, of every selected image manifest, its config and its layers, each with the
     `Size` its descriptor announces, so that `maxBytes` (R-REPO-052) is checked before a fetch. Layers with `urls` are skipped.

  Every item of a tag carries the tag as its `Version`; the engine counts versions, not items, for `maxVersionsPerPackage`
  (a correction of `SelectItems` that changes nothing for npm and NuGet, whose plans have one item per version). A
  single-platform image is kept whatever `platforms` says, because its platform is only in its config.
- **Moving tags.** Every pass is full and re-plans every name, so a tag moved upstream is found by the `HEAD` of step 1 and its
  new manifest and blobs are stored; the old manifest stays (the mirror deletes nothing, R-REPO-051).
- **Referrers** are not mirrored; they stay pull-through (R-OCI-024).
- **Hosts.** The catalog and the tags lists are on the upstream origin; the format adds no host of its own (`MirrorHosts` is
  empty). The token handshake of R-OCI-021 applies to every request of a pass.
- **Rate limits.** Docker Hub's answers are rate-limit answers of R-REPO-054 (R-OCI-025). The plan's manifest `GET` requests are
  the only requests Docker Hub counts as pulls; `HEAD`, tags lists and blobs are not counted. A first pass of n tags costs about n
  manifest `GET` requests plus one per kept platform manifest; later passes cost one per moved tag.
- **Anonymous or authenticated.** With `authMode: none` the handshake asks the realm for anonymous tokens, and the mirror draws on
  Docker's anonymous allowance of the installation's egress address, which every client of that address shares. With `authMode:
  basic` (a Docker Hub user and access token; the realm `auth.docker.io` is a credential host by default, R-OCI-021) it draws on
  that account's allowance. The guide recommends an authenticated remote for every mirror of Docker Hub (R-OCI-030); the
  numbers are Docker's and change, so trove names none.
- **Cursor.** A listing pass keeps `{"last": "<name>"}` within the pass (at most 4 KiB); there is no cursor between passes.

Acceptance criteria (integration, the OCI stub of R-OCI-035):

- A stub with `_catalog` listing `acme/a`, `acme/b` and `other/c`, and a mirror with `includePatterns: ["acme/*"]`,
  `oci.mirror.tags: ["1.*", "latest"]` and `maxVersionsPerPackage: 2`, where `acme/a` has the tags `1.0`, `1.1`, `1.2`, `2.0` and
  `latest`: the pass stores `1.2`, `1.1` and `latest` of `acme/a` with their manifests and blobs; the stub records no request for
  `other/c` and no manifest request for `1.0` or `2.0`.
- A stub without `_catalog` and `includePatterns: ["acme/a"]`: the pass takes `names` and stores the same.
- `oci.mirror.platforms: ["linux/arm64"]` on an index with `linux/amd64`, `linux/arm64` and two attestation entries: the pass
  stores the index, the arm64 manifest, its attestation manifest and their blobs, and no amd64 layer.
- A second pass with nothing changed upstream records one `HEAD` per kept tag, the tags lists, and no manifest `GET`; after the
  stub moves `latest`, the next pass records one manifest `GET` for it and stores its new blobs.
- With `maxBytes` below the image's size, the pass ends `bound_reached` and `docker pull` of an unstored layer through the remote
  still succeeds by pull-through.
- After a pass, with the stub stopped, `docker pull` of a mirrored tag through the remote succeeds.

## Test plan

Where each requirement is proven (R-OCI-035 has the suites; the testing rule of the phase run asks for integration over HTTP
against PostgreSQL, Valkey with Sentinel and S3 plus the conformance kit, and e2e with the real clients on several replicas):

| Requirements | Unit | Integration (`test/integration/oci`, `…/mirror`) | Conformance | e2e set |
|---|---|---|---|---|
| R-OCI-001 to R-OCI-003 (address, keys, root) | grammars | routes, 404 shapes, route table | pull category | `oci` |
| R-OCI-004 to R-OCI-006 (token service, tokens, action map) | token form, scope grammar | token flow, revocation, query counter, `TestR_AUTHZ_007_OCIActionMap` | all categories with credentials | `oci` (login, anonymous pull, Kubernetes secret) |
| R-OCI-007 to R-OCI-010 (layout, errors, pulls) | `WriteError` table | layout, read-your-writes across replicas | pull | `oci` |
| R-OCI-011, R-OCI-012 (uploads, mount) | — | three replicas per session, fault injection of R-STOR-011, mount rules | push | `oci`; `oci-long` (drain during a push) |
| R-OCI-013, R-OCI-014 (manifests, indexes) | parser, crafted inputs | references, overwrite, concurrent tag push | push | `oci` (Buildx, `docker manifest`, Podman manifests) |
| R-OCI-015 to R-OCI-019 (tags, referrers, catalog, deletion, sweep) | tag order | paging, concurrent referrers, cascades, sweep with a fake clock | discovery, management | `oci` |
| R-OCI-020 (untrusted input) | crafted inputs (kit) | — | — | — |
| R-OCI-021 to R-OCI-025 (remote) | handshake parser, `library/` | the counting stub: realm, `HEAD` revalidation, flights, redirects, 429 | — | `oci` (remote `pullThrough`) |
| R-OCI-026, Mirror discovery | plan against fixtures | stub passes (catalog, names, platforms, moved tag, bound) | — | `mirror` (OCI pass on two worker processes while clients pull), `mirror-long` (drain) |
| R-OCI-027, R-OCI-028 (virtual) | — | resolution order, claims, union lists, link step | all categories through a virtual address | `oci` (pull and push through `docker`) |
| R-OCI-029 (bound host) | — | dispatch on a bound host | — | `oci` (Docker Engine mirror) |
| R-OCI-030, R-OCI-031 (setup, screens) | templates render | view API | — | `oci` (rendered snippets), browser journey of the package screen |
| R-OCI-032 to R-OCI-034 (replicas, backups, metrics) | — | Valkey stopped, metrics | — | `oci` in `scatter`; closing run with Valkey stopped; restore drill (R-BAK-029) |

## Catalog coverage

Every entry of the two catalog pages, with what this phase does (Q-OCI-2). "Built" means specified on this page and built in
T-16; "F4" means it stays in FULL.md F4 for a later phase; "Not offered" means trove decided against it.

<details>
<summary>PKG-DOCKER-001 to PKG-DOCKER-052</summary>

| Entry | Decision | Where |
|---|---|---|
| PKG-DOCKER-001 Three classes | Built | R-OCI-001 |
| PKG-DOCKER-002 Keys as registry names | Built (grammar tightened for every format; no soft limit, no subdomains) | R-OCI-002, Q-OCI-3 |
| PKG-DOCKER-003 Image-name visibility | Built, strict: names filtered by read | R-OCI-006, R-OCI-017 |
| PKG-DOCKER-004 API version per repository | Not offered: version 2 only | R-OCI-001, Q-OCI-1 |
| PKG-DOCKER-005 Access methods | Built: repository path; the port form through an endpoint with a bound host | R-OCI-001, R-OCI-029 |
| PKG-DOCKER-006 Repository path method | Built | R-OCI-001 |
| PKG-DOCKER-007 Subdomain method | Not offered | Q-EP-76 |
| PKG-DOCKER-008 Port method | Built as an endpoint with a bound host | R-OCI-029 |
| PKG-DOCKER-009 Proxy configuration generator | Not offered: the chart renders the edge | R-HA-037, R-HA-039 |
| PKG-DOCKER-010 Hosted address formats | Not offered (out of scope) | — |
| PKG-DOCKER-011 Login, stored credentials | Built | R-OCI-004, R-OCI-030 |
| PKG-DOCKER-012 Authentication challenge | Built: token service | R-OCI-004, R-OCI-005, Q-OCI-5 |
| PKG-DOCKER-013 Credential snippet operation | Built as setup snippets without a credential | R-OCI-030 |
| PKG-DOCKER-014 Login through a browser session | F4 | Deferred |
| PKG-DOCKER-015 Kubernetes pull secrets | Built | R-OCI-004, R-OCI-030 |
| PKG-DOCKER-016 Registry API v2 | Built | R-OCI-001, R-OCI-003 |
| PKG-DOCKER-017 Pulling | Built | R-OCI-009, R-OCI-010 |
| PKG-DOCKER-018 Pushing layers | Built | R-OCI-011, R-OCI-028 |
| PKG-DOCKER-019 Deduplication, mount | Built | R-OCI-007, R-OCI-012 |
| PKG-DOCKER-020 Pushing manifests | Built; labels as properties F4 | R-OCI-013 |
| PKG-DOCKER-021 Schema 1 blocking | Built as a fixed refusal | R-OCI-013, Q-OCI-8 |
| PKG-DOCKER-022 Catalog and tags | Built; the system-level remote catalog cache is not offered | R-OCI-015, R-OCI-017, R-OCI-024 |
| PKG-DOCKER-023 Indexes | Built; the nested-manifest table as package metadata | R-OCI-014, R-OCI-031 |
| PKG-DOCKER-024 OS-qualified extra tag | Not offered | Q-OCI-2 |
| PKG-DOCKER-025 Delete a multi-platform tag | Built through `DELETE` by digest and the item store | R-OCI-018 |
| PKG-DOCKER-026 Tag syntax | Built | R-OCI-002 |
| PKG-DOCKER-027 Overwrite permission | Built through `overwrite` | R-OCI-006, R-OCI-013 |
| PKG-DOCKER-028 Maximum unique tags | F4, with the repository caps (R-REPO-007, R-STOR-044) | Deferred |
| PKG-DOCKER-029 Retention of overwritten tags | F4, with the repository caps | Deferred |
| PKG-DOCKER-030 Shared download statistics | F4 | Deferred |
| PKG-DOCKER-031 Deleting images and tags | Built | R-OCI-018, R-OCI-019 |
| PKG-DOCKER-032 Promotion | F4, with copy and move (R-STOR-019) | Deferred |
| PKG-DOCKER-033 Proxying registries | Built | R-OCI-021 to R-OCI-024 |
| PKG-DOCKER-034 Docker Hub rate limits | Built | R-OCI-022, R-OCI-025 |
| PKG-DOCKER-035 `library/` | Built | R-OCI-025 |
| PKG-DOCKER-036 Foreign layers | F4 | Deferred |
| PKG-DOCKER-037 Project scope for a cloud registry | Not offered: the namespace prefix of the URL covers it | R-OCI-021 |
| PKG-DOCKER-038 Chaining to another instance | Built as a plain remote with a namespace prefix | R-OCI-021 |
| PKG-DOCKER-039 Virtual registry | Built | R-OCI-027, R-OCI-028 |
| PKG-DOCKER-040 Tags by latest timestamp | F4 | Deferred |
| PKG-DOCKER-041 Tag details | Built in part; layer history F4 | R-OCI-031 |
| PKG-DOCKER-042 Labels as properties | F4 | Deferred |
| PKG-DOCKER-043 Storage layout | Built (trove's layout) | R-OCI-007 |
| PKG-DOCKER-044 Docker Content Trust | Built by nature: signatures live on a separate server | — |
| PKG-DOCKER-045 Supported clients | Built: Docker CLI with Buildx, Podman, Kubernetes; Kaniko and OpenShift F4 | R-OCI-035 |
| PKG-DOCKER-046 Setup instructions | Built for Docker and Podman | R-OCI-030 |
| PKG-DOCKER-047 Build metadata through a companion CLI | Not offered | — |
| PKG-DOCKER-048 Local scanning through a companion CLI | Not offered | — |
| PKG-DOCKER-049 Registry API v1 | Not offered | Q-OCI-1 |
| PKG-DOCKER-050 Migrating v1 to v2 | Not offered | Q-OCI-1 |
| PKG-DOCKER-051 Container-only profile | Not offered (out of scope) | — |
| PKG-DOCKER-052 Upstream for a cloud registry's pass-through repository | Built: Basic credentials on `/v2/` | R-OCI-004 |

</details>

<details>
<summary>PKG-OCI-001 to PKG-OCI-020</summary>

| Entry | Decision | Where |
|---|---|---|
| PKG-OCI-001 Three classes | Built; one format for Docker and OCI | R-OCI-001, Q-OCI-1 |
| PKG-OCI-002 Local settings (tag limits, retention) | F4, with the repository caps | Deferred |
| PKG-OCI-003 Proxying OCI registries | Built; the `ns` parameter is ignored | R-OCI-021, Q-OCI-13 |
| PKG-OCI-004 Virtual repositories | Built; the merged lists use the merged-metadata cache of R-REPO-020 | R-OCI-027 |
| PKG-OCI-005 Addressing | Built | R-OCI-001 |
| PKG-OCI-006 Distribution endpoints | Built: pull, push, discovery and management; extra tags in the query F4 | R-OCI-003, R-OCI-009 to R-OCI-018 |
| PKG-OCI-007 Manifest kinds, negotiation | Built: no conversion | R-OCI-009, R-OCI-013, Q-OCI-8 |
| PKG-OCI-008 Non-image artifacts | Built on the registry side (content-agnostic); ORAS, wasm-to-oci and BuildKit as tested clients F4 | R-OCI-013 |
| PKG-OCI-009 Referrers API | Built | R-OCI-016, R-OCI-024 |
| PKG-OCI-010 Referrer storage | Built (rows, no index item) | R-OCI-007, R-OCI-016 |
| PKG-OCI-011 Referrers tag schema | Built by not needing it; remotes pass the upstream's 404 | R-OCI-016, R-OCI-024 |
| PKG-OCI-012 Moving referrers with their subject | F4, with copy and promotion | Deferred |
| PKG-OCI-013 Podman | Built | R-OCI-030, R-OCI-035 |
| PKG-OCI-014 Docker Buildx | Built for multi-platform push; registry build caches F4 | R-OCI-014, R-OCI-035 |
| PKG-OCI-015 BuildKit and registry caches | F4 | Deferred |
| PKG-OCI-016 WebAssembly modules | F4 | Deferred |
| PKG-OCI-017 ORAS | F4 | Deferred |
| PKG-OCI-018 Helm charts in OCI repositories | F5 | Deferred |
| PKG-OCI-019 Setup instructions | Built for Docker and Podman; the other clients F4 | R-OCI-030 |
| PKG-OCI-020 Scanning limited to images | F4, with image scanning | Deferred |

</details>

## Changes to other pages

The decisions of this page change other pages. Each change is made by the work package that builds its mechanism, in the same
change as its code, because several of the blocks are held equal to the code by tests (the contract blocks of R-ARCH-008 and
R-ARCH-009 by `TestR_ARCH_008_TheWrittenContractEqualsTheCode`):

| Page | Change | Decision |
|---|---|---|
| [README.md](../README.md) | INV-5: the key grammar; INV-6: `/v2/` served by the format `oci`, `_catalog` and `_token` at the registry root | Q-OCI-3, Q-OCI-4 |
| [architecture.md](../architecture.md) | R-ARCH-005: the `/v2/` row and the route-table rule admit the format's prefix and the core's three root routes; R-ARCH-008: `ProtocolBasePath` may be `/v2/{project}/{key}/` for the one format that declares the registry root, `Refusal.Registry` and the link builder render the base path, the optional remote interfaces `Authenticator` and `Revalidator`, `discover.Upstream.Artifact`, `catalog.Facts.DefaultCredentialHosts`, `SetupData.ImagePrefix`; R-ARCH-009: the upload-session port of the host; R-ARCH-032: the state rows of R-OCI-032 | Q-OCI-4, Q-OCI-12, Q-OCI-13, Q-OCI-16 |
| [identity-and-authentication.md](../identity-and-authentication.md) | R-AUTH-001: the registry token as a credential of the `Authorization` header on `/v2/`, no session cookie there, `/v2/_token` among the routes whose credential the handler presents; R-AUTH-002: the `/v2/` rows of the challenge table; R-AUTH-022: the registry token, its form and that it is not a reference token; R-AUTH-023: a registry token enters the validation at step 2 with the hash it carries; R-AUTH-033: the Basic switch covers the token service; R-AUTH-041: the event `auth.registry_token.denied` (made with the token service) | Q-OCI-5, Q-OCI-50, Q-OCI-51, Q-OCI-53, Q-OCI-57 |
| [authorization.md](../authorization.md) | R-AUTHZ-007: the OCI table of R-OCI-006 (made with the token service); R-AUTHZ-026: the project key grammar | Q-OCI-3, Q-OCI-6 |
| [operations-and-observability.md](../operations-and-observability.md) | R-OPS-015: `auth.registry_token.denied` deduplicated like `token.rejected`; the event registry's row of `auth.registry_token.denied`; the metric `trove_oci_registry_tokens_total` (made with the token service) | Q-OCI-53 |
| [repositories-and-proxy.md](../repositories-and-proxy.md) | R-REPO-002: the key grammar; R-REPO-033: the token handshake's credential rule; R-REPO-050: `<format>.mirror` kept for an Owner without `repositories:write-config`, OCI among the formats that can mirror; R-REPO-051: versions are counted per version, not per item; R-REPO-054: the quota header is read by its integer before `;` | Q-OCI-3, Q-OCI-13, Q-OCI-16, Q-OCI-22 |
| [storage.md](../storage.md) | R-STOR-011: status built, with the OCI page as its first user; the session row gains the repository the client addressed and a binding the format names (the OCI image name), both checked on every request | Q-OCI-12 |
| [endpoints.md](../endpoints.md) | R-EP-007: the Docker Engine mirror points at R-OCI-029 | — |
| [ha-and-helm.md](../ha-and-helm.md) | R-HA-041: the acceptance criterion points at the e2e set `oci` | — |
| [vulnerability-scanning.md](../vulnerability-scanning.md) | Deferred: image scanning stays with F4 after this phase | Q-OCI-2 |

## Deferred

| Catalog ID | Priority | Note |
|---|---|---|
| PKG-DOCKER-014 | P2 | Docker login through a browser session (a credential helper); tokens from the UI cover it. |
| PKG-DOCKER-028, PKG-DOCKER-029, PKG-OCI-002 | P1 | Tag caps and kept tag revisions arrive with the repository caps (R-REPO-007, R-STOR-044), which need the format's version-group hook. |
| PKG-DOCKER-030 | P2 | Download statistics shared across the tags of a digest. |
| PKG-DOCKER-032, PKG-OCI-012 | P1 | Promotion and moving referrers with their subject arrive with copy and move (R-STOR-019). |
| PKG-DOCKER-036 | P2 | Fetching and caching foreign layers; their manifests are accepted and served (R-OCI-013). |
| PKG-DOCKER-040 | P1 | Tag resolution by push time across virtual members. |
| PKG-DOCKER-041 (layer history), PKG-DOCKER-042 | P1 | Reading image configs for history and labels. |
| PKG-DOCKER-045 (Kaniko, OpenShift) | P2 | Further producers, no registry behaviour of their own. |
| PKG-OCI-006 (extra tags in the query) | P2 | `PUT …/manifests/<digest>?tag=…`; answered 400 `UNSUPPORTED` until then. |
| PKG-OCI-014 (build caches), PKG-OCI-015 | P1 | BuildKit registry caches as a tested client. |
| PKG-OCI-016, PKG-OCI-017 | P1 | wasm-to-oci and ORAS as tested clients; the registry side is content-agnostic already. |
| PKG-OCI-018 | P1 | Helm charts in OCI repositories (F5). |
| PKG-OCI-020 | P1 | Image vulnerability scanning ([vulnerability-scanning.md](../vulnerability-scanning.md#deferred)). |
| Cosign beyond referrers | P1 | Signature verification and policies; signatures are stored as referrers or tags today. |
| OCI on endpoints with exposure rules | P1 | The format has no exposure adapter yet, so an endpoint with `exposure: rules` does not serve it (R-EP-018, Q-OCI-23). |
| OCI through the Bastion | P2 | The Bastion's mirrored URL space has no `/v2/` and no token service (Q-OCI-23). |

## Security notes

- **The long-lived credential crosses the wire once per token.** Clients send the trove token to `/v2/_token` and use a
  registry token of at most `oci.token_lifetime` that names only the repositories and actions they asked for. A registry token
  that leaks from a client's debug log is worth minutes and those actions; it never widens what its principal may do, because
  every request is decided again (R-OCI-005).
- **Registry tokens are self-contained** and verified without the database. Their MAC key is derived from the stored-secret key
  ring, so whoever holds a key of the ring and the hash of a live trove token can mint registry tokens for that token's
  principal; that is the same trust the ring already carries for sealed upstream credentials
  ([H-579](#h-579--a-holder-of-the-stored-secret-key-ring-can-mint-registry-tokens)). A stolen registry token is usable from
  anywhere for its lifetime within its grant ([H-595](../../security/oci-registry.md#h-595--a-registry-token-is-a-bearer-credential-for-its-whole-lifetime)).
- **Revocation** of the trove token behind a registry token takes effect within the bound of R-AUTH-023, not at the registry
  token's expiry, because verification reads the authenticating token's state.
- **No automatic cross-mount.** A mount names its source and needs `read` there; without `from` nothing is mounted, so the
  registry never tells a caller that bytes exist in a repository it cannot read (R-OCI-012).
- **Blobs belong to names.** A blob is served through a name only when an item of that name holds it (R-OCI-010), so knowing a
  digest gives no access to another team's layer.
- **Dependency confusion for images.** Claims and pin rules apply to image names through virtual repositories (R-OCI-027): an
  image name an organisation claims is never served from Docker Hub through the same virtual address.
- **Upstream credentials stay with their hosts.** The token handshake sends the repository's credential only to the upstream
  origin and `sendCredentials` hosts; a realm elsewhere gets an anonymous request or none (R-OCI-021).
- **Manifests are parsed within bounds**, digests are validated before they become path segments, and fetched manifests must
  match their digest (R-OCI-020).
- **Anonymous pulls hide identities.** With anonymous `read`, clients that are not logged in pull without credentials and are
  recorded as the anonymous principal; repositories whose usage must be attributable grant no anonymous `read`.

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the
walkthrough.

### Q-OCI-1 · One format `oci` serves Docker and OCI content, on the page `formats/oci.md` with the prefix `R-OCI`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One format `oci`, display name "OCI (Docker)", one page `oci.md` (chosen) | Docker v2 schema 2 and OCI manifests in every repository, one implementation, one prefix | The wire protocol is one (OCI distribution is the Docker registry API V2 plus referrers); INV-6 reserves `/v2/` for "the OCI format"; F5 builds on it; no setting that only selects a subset | Users who search for "Docker" find the format by its display name, not its ID |
| (b) Formats `docker` and `oci`, pages `docker.md` and `oci.md` | The catalog's split | Mirrors the catalog's two pages | Two formats with one protocol and one route prefix that only one format can own (R-ARCH-005); repository settings that differ in nothing trove builds (schema 1 blocking becomes a fixed rule, the v1 API is not offered) |
| (c) One format `docker` | The client name users know | Familiar | OCI artifacts, referrers and Helm charts are not Docker; the ID would mislead every non-Docker client |

**Choice:** (a). FULL.md F4's "spec pages `formats/oci.md` and `formats/docker.md`" becomes the one page `formats/oci.md`.

**Rationale:** one protocol, one route owner, one implementation; the catalog's differences between its Docker and OCI formats are
settings trove does not offer (schema 1, v1, store-locally). The development agent would revisit a second format if a later
phase needs a repository that refuses one of the two manifest families as a matter of configuration.

**Applied in the spec:** R-OCI-001; [Catalog coverage](#catalog-coverage); [README.md](../README.md) pages table.

### Q-OCI-2 · What this phase builds and what stays in F4

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The registry for Docker CLI and Podman in full (the four API categories, token service, uploads across replicas, indexes, referrers, deletion, remote in both modes, virtual, bound hosts, Kubernetes pulls, Buildx multi-platform push); F4 keeps BuildKit caches, ORAS, wasm-to-oci, Cosign beyond referrers, image scanning, repository caps, promotion, foreign layers, timestamp resolution, labels and history (chosen) | The owner's minimum plus what the registry side needs anyway | Every API category the conformance suite tests is built once and fully; the deferred items are clients or features with machinery of their own (caps, copy and move, an analyser) | The catalog's P1 caps and promotion wait for their core features |
| (b) The owner's minimum only (pull, push, tags, one client) | Smallest phase | Fast | Deletion, referrers and the conformance suite would be half-built and re-opened |
| (c) All of F4 | One phase | Done at once | Image scanning needs an analyser decision of its own (Q-VULN-1); caps and promotion need core machinery that serves every format |

**Choice:** (a). Not offered at all: the v1 API, the OS-qualified extra tag, the proxy configuration generator, hosted address
formats, a companion CLI, a container-only profile, subdomain addressing.

**Rationale:** what a registry must answer is built completely, so the conformance suite runs every category; what depends on
other machinery or on a client of its own waits for it. The development agent would revisit an F4 item when an owner workload
needs it before its machinery exists.

**Applied in the spec:** [Catalog coverage](#catalog-coverage); [Deferred](#deferred); FULL.md F4 (status text, by the closing
change).

### Q-OCI-3 · Repository and project keys follow the OCI component grammar for every format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Tighten INV-5 and the project key grammar for every format (chosen) | `^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$` (2–64) and `^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$` (2–32) | INV-5 already claims keys are valid for container formats, which `docker-`, `a.-b` and `web-` disprove; one grammar, no per-format key rule, no special case in a move between projects | Keys like `npm-` that are valid today are refused (pre-alpha: no installations to keep) |
| (b) A key rule of the OCI format only | Refuse the bad keys at creation of an OCI repository | Other formats keep their freedom | A project key cannot depend on the formats of its repositories; a move (R-REPO-060) needs a second check; a catalog fact for one format |
| (c) Refuse at request time (`NAME_INVALID`) | No validation change | Nothing to build | A repository that can be created and never used |

**Choice:** (a), applied with a migration that changes the `CHECK` constraints and the OpenAPI patterns.

**Rationale:** the claim of INV-5 becomes true, and the rule lives in one place. The development agent would revisit it if a
format needs a character the grammar refuses.

**Applied in the spec:** R-OCI-002; the edits of [Changes to other pages](#changes-to-other-pages) (README INV-5, R-REPO-002,
R-AUTHZ-026).

### Q-OCI-4 · The registry root is mounted through the format contract, its repository-less routes are the core's

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ProtocolBasePath` `/v2/{project}/{key}/` for the one format that declares the registry root; the dispatcher mounts it like `/api/<id>/`; `/v2/`, `/v2/_catalog` and `/v2/_token` are core routes (chosen) | The format keeps routes relative to the repository; the core owns authentication, the catalog over package rows and the token service | One dispatcher path (coarse check, exposure, refusal shape) for every format; the format runs no authentication and no cross-repository listing, as R-ARCH-008 requires | Three contract facts change (the base path rule, `Refusal.Registry`, the link builder) |
| (b) A rewrite at the edge from `/v2/` to `/api/oci/` | No contract change | Small | R-HA-041 forbids edge rewrites; pushes break where the rewrite and the server disagree; the `Location` headers would name `/api/oci/` |
| (c) A second dispatcher just for `/v2/` | Separate code path | Free of the format contract | Every check of the dispatcher duplicated; two places to secure |

**Choice:** (a). `SetupData` gains `ImagePrefix` (`<host>/<projectKey>/<repoKey>`), because a setup template cannot strip
`/v2/` from the registry URL.

**Rationale:** the reservation of Q-HA-4 was made for this; mounting through the contract keeps one request path. The
development agent would revisit it if a second format needs a host-root base path.

**Applied in the spec:** R-OCI-001, R-OCI-003, R-OCI-029, R-OCI-030; [Changes to other pages](#changes-to-other-pages)
(R-ARCH-005, R-ARCH-008).

### Q-OCI-5 · Bearer token service with short-lived self-contained registry tokens; trove tokens also accepted directly

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Bearer` challenge, token service at `/v2/_token`, registry tokens signed with a key derived from the key ring, verified without the database, bound to the authenticating token's state; trove tokens also accepted as `Basic` or `Bearer` (chosen) | The protocol's own flow | The scope grammar narrows each token to what the operation needs; the long-lived token crosses the wire once per lifetime; the flow every registry client implements first; `Basic` still works for clients that send it up front | A self-contained credential next to the reference tokens of R-AUTH-022; a key derivation; one more stage in authentication |
| (b) `Basic realm="trove"` challenge only | The challenge of the other formats (R-AUTH-002) | Nothing new: the authentication stage already accepts `user:token` | No scopes; the trove token on every request; some tools follow bearer flows only as their tested path |
| (c) Bearer, the token service returns the presented trove token as the bearer token | A token service without new tokens | Small | No anonymous token (an anonymous caller has nothing to return), no narrowing, and the long-lived token on every request again |
| (d) Bearer with reference registry tokens in PostgreSQL | Registry tokens as rows | Revocable by row | A row written per pull; a database write on the hottest path |

**Choice:** (a).

**Rationale:** the scope grammar exists to narrow each token, and self-containment is what makes the token flow cheap on every
replica; binding verification to the authenticating token keeps revocation where R-AUTH-023 puts it. The development agent
would revisit (b) if a supported client fails the token flow, and (d) if the bound of R-AUTH-023 turns out too slow for
registry tokens.

**Applied in the spec:** R-OCI-004, R-OCI-005, [Security notes](#security-notes); [Changes to other pages](#changes-to-other-pages)
(R-AUTH-001, R-AUTH-002, R-AUTH-022).

### Q-OCI-6 · A token grant is the coarse check per scope entry, and a ceiling only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The coarse check of the route kind per entry (`pull` → `read`, `push` → `deploy`, `delete` → `delete`), nothing reported for a hidden repository; each request decided again (chosen) | The token says what the caller may plausibly do; the decision point decides each request | Never under-grants a push that the decision point allows (a client would fail); never discloses a repository; no second evaluation algorithm | A token may name an action that a later request is denied for a particular tag (moved tag without `overwrite`) |
| (b) The full decision per entry, including the tag | Exact grants | Precise | The tag is unknown at token time; a push routed through a virtual repository is decided on the target |
| (c) Grant what was asked | No check at issue | Simplest | A token that names repositories the caller cannot see tells nothing, but invites useless round trips and hides `401`/`403` semantics |

**Choice:** (a).

**Rationale:** grants are ceilings and requests are decided anyway, so the grant only has to be generous within what the coarse
check allows. The development agent would revisit it if clients loop on `insufficient_scope`.

**Applied in the spec:** R-OCI-006.

### Q-OCI-7 · Manifests, tags and blobs are items, a tag item holds the manifest's bytes, and two side tables hold the graph

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Items `<name>/_manifests/<d>`, `<name>/_tags/<t>`, `<name>/_blobs/<d>`; `oci_manifests` and `oci_references` for media types, subjects, platforms and references (chosen) | The blob store's SHA-256 is the OCI digest; a tag is a file with the manifest's bytes | Garbage collection, trash, `/files/`, quotas and usage work unchanged; a tag lookup is one item read; the graph is queryable for referrers, cascades and the view | Two rows per tag push (manifest and tag item), both pointing at one blob |
| (b) Tags only in a side table | Tags as rows | One item per manifest | Tags invisible to the item store, the trash and retention (R-STOR-039 names the tag's item as the lead item) |
| (c) Blobs per repository instead of per name | Fewer items | Fewer rows | A digest would open another name's layer; the distribution specification scopes blobs per name |

**Choice:** (a).

**Rationale:** the content address of the blob store is exactly the OCI digest, so the format adds a graph and no storage of its
own. The development agent would revisit (c) if blob item counts become a measured cost.

**Applied in the spec:** R-OCI-007.

### Q-OCI-8 · Four manifest types, no schema 1, no conversion, 4 MiB

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) OCI manifest and index, Docker schema 2 and manifest list; schema 1 refused everywhere; stored bytes served whatever `Accept` says; 4 MiB per manifest (chosen) | The four types every current client uses | Digests stay stable; no signing problem of schema 1; 4 MiB is the specification's floor for what a registry should accept | A client that only understands schema 1 cannot pull (none is supported) |
| (b) Convert between types on `Accept` | Down-conversion as some registries do | Older clients | A converted manifest has another digest; signatures and pins break |
| (c) Accept schema 1 behind a switch | The catalog's setting | Old images | Signed schema 1 is deprecated since Docker 20.10; a switch nobody should turn on |

**Choice:** (a).

**Rationale:** byte-exact storage is what makes digests trustworthy. The development agent would revisit the size if a supported
producer pushes larger indexes.

**Applied in the spec:** R-OCI-009, R-OCI-013, R-OCI-020.

### Q-OCI-9 · SHA-256 is the only digest algorithm

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `sha256` only (chosen) | The blob store's address and the algorithm of every supported client | No second index over blobs; every digest is a content address | `sha512` digests are refused |
| (b) `sha256` and `sha512` | The store computes SHA-512 anyway | Wider | A lookup by SHA-512 needs an index on `blobs.sha512` and a second path form; no client in scope uses it |

**Choice:** (a).

**Rationale:** no supported client sends another algorithm. The development agent would revisit it when a client pushes
`sha512` by default.

**Applied in the spec:** R-OCI-002.

### Q-OCI-10 · References must exist, except a `subject` and foreign layers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every config, layer and index entry stored under the name, except `subject` and layers with `urls` or a non-distributable type (chosen) | What the distribution specification allows registries to require | A stored manifest is always pullable; garbage collection can trust the graph | A client that pushes a manifest before its blobs fails (none in scope does) |
| (b) No reference check | Store what arrives | Simple | Broken images; unreferenced blobs indistinguishable from pending ones |

**Choice:** (a).

**Rationale:** the referrers rule needs the `subject` exception, Windows images the foreign-layer one. The development agent would
revisit it if a supported producer pushes in another order.

**Applied in the spec:** R-OCI-013.

### Q-OCI-11 · `DELETE` is supported with cascades and refusals; untagged manifests stay

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `DELETE` of tags, manifests (with tags, referrers and exclusive children) and unreferenced blobs; a referenced child or blob refused with 409; untagged manifests kept; unreferenced blob items swept after a grace (chosen) | The content-management category in full | Conformance in all four categories; the same cascade for every removal path; Kubernetes pulls by digest keep working after a tag moves | Untagged manifests accumulate until a retention policy removes them |
| (b) Deletion only through the management API | `405` on the protocol | Fewer paths | A category of the specification missing; tools that clean registries fail |
| (c) Delete untagged manifests at once | Storage stays small | Small | Running workloads that pull by digest break when a tag moves |

**Choice:** (a).

**Rationale:** the protocol's deletes and trove's item store meet in one hook, so every path behaves alike. The development agent
would revisit untagged retention with the retention policies of M7.

**Applied in the spec:** R-OCI-018, R-OCI-019.

### Q-OCI-12 · Uploads use the sessions of R-STOR-011; mounts name their source; no automatic mount

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Sessions in PostgreSQL and one S3 multipart upload (R-STOR-011), single-request `POST` through the upload pipeline, mounts from any readable repository by `from`, `202` without it (chosen) | The design storage.md made for this protocol | Any replica continues a session; a drain loses at most a part; a mount copies nothing and leaks nothing | The upload-session port is a new part of the host contract |
| (b) Sessions in Valkey | Faster state | Small | A lost Valkey loses every running upload, more than R-ARCH-032 allows |
| (c) Automatic mount without `from` | Fewer uploads | Saves bandwidth | Tells a caller that bytes exist where it cannot read |

**Choice:** (a).

**Rationale:** the storage page already decided where session state lives; the format only uses it. The development agent would
revisit the automatic mount if it can be limited to repositories the caller reads without a disclosure.

**Applied in the spec:** R-OCI-011, R-OCI-012; [Changes to other pages](#changes-to-other-pages) (R-ARCH-009, R-STOR-011).

### Q-OCI-13 · Remotes: the handshake's I/O in the core, revalidation by `HEAD`, a namespace prefix in the URL

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Two optional interfaces of the remote adapter: `Authenticator` (the format builds the token request from a challenge and reads the answer; the engine sends it, caches the token per replica and retries) and `Revalidator` (a `HEAD` whose digest decides); the URL's path is a name prefix; the `ns` parameter is ignored (chosen) | The format knows the protocol, the core does the I/O (R-ARCH-008) | Formats still open no connection; the egress, host and credential rules apply to the realm; an unchanged tag costs no Docker Hub pull; chaining to another trove and to an organisation's namespace is one URL | Two contract additions and an ADR |
| (b) Conditional `GET` with `If-None-Match` | The engine's revalidation | Nothing new | Whether a conditional manifest `GET` counts as a Docker Hub pull is not documented; `HEAD` is documented as not counted |
| (c) Static bearer tokens only (`authMode: bearer`) | No handshake | Nothing new | Docker Hub and most registries issue short-lived tokens per scope |

**Choice:** (a). The default extra hosts for Docker Hub include `auth.docker.io` with credentials, which needs the additive catalog
fact `DefaultCredentialHosts`; the blob redirect hosts are confirmed against Docker Hub by the remote work package.

**Rationale:** R-REPO-033 and Q-REPO-5 already placed the container token exchange in the format's handshake. The development agent
would revisit (b) if an upstream answers `HEAD` without `Docker-Content-Digest` often enough to double the requests.

**Applied in the spec:** R-OCI-021, R-OCI-022; [Changes to other pages](#changes-to-other-pages) (R-ARCH-008, R-REPO-033).

### Q-OCI-14 · `library/` only for the Docker Hub upstream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One-component names map to `library/<n>` when the upstream host is `registry-1.docker.io`, in request, cache and package (chosen) | Docker's own normalisation, applied where it holds | Both spellings share one cached copy; no setting | Another mirror of Docker Hub (for example a cloud provider's) needs the full name |
| (b) A setting per remote | Explicit | Covers mirrors of Docker Hub | A setting for one convention of one registry |
| (c) No mapping | Clients spell `library/` | Nothing to build | `docker pull …/docker-hub/nginx` fails, the most common first test |

**Choice:** (a).

**Rationale:** only Docker Hub has the convention. The development agent would revisit (b) if operators proxy Docker Hub mirrors.

**Applied in the spec:** R-OCI-025.

### Q-OCI-15 · A remote's tags list is a cached document; its referrers are passed through

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `<name>/_taglist` as metadata (also the package document); referrers as a volatile query, the upstream's 404 passed on (chosen) | One cached list per name; referrers as the upstream answers | The gone rule and the shadowing probe get the document they need; clients fall back to the tag schema where the upstream lacks the API | A truncated upstream list needs pass-through for later windows |
| (b) Both volatile | No cache | Always current | No package document; offline nothing answers |
| (c) Referrers answered empty when the upstream lacks the API | Uniform answers | Simple | Hides signatures that the upstream keeps under the tag schema |

**Choice:** (a).

**Rationale:** the package document must be a cacheable metadata path (R-ARCH-008), and referrers must not hide what the upstream
holds. The development agent would revisit it if upstream lists beyond 10,000 tags are common.

**Applied in the spec:** R-OCI-024.

### Q-OCI-16 · What a mirror of a registry discovers and keeps

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Names from the catalog where the upstream has one, else the exact include patterns, plus held names; per name the tags that match `oci.mirror.tags` (default `latest`) minus `excludeTags`, the newest `maxVersionsPerPackage` of the wildcard matches by the format's tag order, exact tags always; per tag the index or manifest, the children of the chosen platforms with their attestations, configs and layers; no referrers (chosen) | The operator names images, tags and platforms; everything is bounded by default | Works on Docker Hub (no catalog) and on registries with one; an unchanged tag costs one `HEAD`; `maxBytes` is checked with announced sizes; a mirror with defaults keeps one tag per image | Tag patterns are a format setting next to the generic version bounds |
| (b) Every tag of every named image | The generic version bound only | No format setting | `library/node` alone has thousands of tags; a default that fills terabytes |
| (c) The newest n tags by push time | Recency by date | Matches intuition | The distribution API has no push time; reading every tag's config to learn it costs one manifest `GET` per tag, each a Docker Hub pull |
| (d) Referrers too | Signatures and SBOMs with their images | Complete | The referrers API is optional upstream; a second discovery per manifest; pull-through serves them on first use |

**Choice:** (a). Two core corrections come with it: `SelectItems` counts versions, not items; `discover.Upstream` gains
`Artifact`, so that a plan can read the child manifests it needs through the engine. The setting `oci.mirror` is format settings,
kept from Owners like the patterns (Q-OCI-22).

**Rationale:** the owner asked that a registry mirror keep named repositories, tag patterns, platforms and the newest n tags within
the bounds of R-REPO-051 and R-REPO-052; each is a setting here, with defaults that keep a mirror small until the operator widens
it. The development agent would revisit (c) if the tag order of Q-OCI-17 picks the wrong tags for the images in use, and (d) when
signature verification arrives (F4).

**Applied in the spec:** R-OCI-026; [Mirror discovery](#mirror-discovery); [Changes to other pages](#changes-to-other-pages)
(R-REPO-050, R-REPO-051).

### Q-OCI-17 · The tag order of the format

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Version-aware: a tag `v?N(.N)*` with an optional `-suffix` is a version, ordered by its numbers, then without suffix before with, then by suffix bytes; every other tag after all versions, by bytes descending; a suffix that starts with `alpha`, `beta`, `rc`, `pre` or `dev` (any case, optionally followed by digits and dots) marks a pre-release (chosen) | "Newest" from the tag itself | No request per tag; matches the release tags of most images (`1.29.3`, `v2.1`, `3.20`); deterministic | Variants of one version (`1.29.3-alpine`, `1.29.3-perl`) count as separate tags, so `tags` patterns pick a variant family |
| (b) Lexical order | The order of the tags list | Trivial | `9.0` after `10.0`; date tags only by accident |
| (c) Push time from the image config | Real recency | Correct | One manifest `GET` per tag (Q-OCI-16 (c)) |

**Choice:** (a). The view adapter's `Compare` uses the same order.

**Rationale:** the only order available without a request per tag. The development agent would revisit it if mirrors of the owner's
images keep the wrong tags.

**Applied in the spec:** [Mirror discovery](#mirror-discovery), R-OCI-026, R-OCI-031.

### Q-OCI-18 · Virtual resolution: tags by member order with locals first, digests by tiers, lists merged, missing blobs linked from members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Tags: local members first, then remote members, each in member order, a remote through its own request path; digests: the three tiers; tags list and referrers: union; manifest push through the virtual links blobs the target lacks from readable members (chosen) | Mutable tags by order, immutable content wherever it is | A remote member's stale tag never shadows an earlier member's current one; content is identical by digest; Docker's `HEAD`-then-skip cannot leave the target without a layer | The link step reads members during a push |
| (b) The artifact tiers for tags too | One rule | Simple | A cached tag of a later remote beats a current one of an earlier remote |
| (c) Answer blob `HEAD` from the deploy target only | No link step | Simple | The same `HEAD` serves pulls; a pull through the virtual repository would miss every member's blob |

**Choice:** (a).

**Rationale:** tags are metadata, digests are content, and Docker's push protocol needs the link step. The development agent would
revisit (b) with tag resolution by push time (PKG-DOCKER-040).

**Applied in the spec:** R-OCI-027, R-OCI-028.

### Q-OCI-19 · The catalog lists readable names of local and remote repositories

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Full names of local repositories and remote caches, filtered per name by `read`, virtual repositories not expanded (chosen) | A listing of what exists, as the caller may see it | No duplicates; another trove can mirror by catalog listing; no name a grant hides | A virtual repository's address does not appear |
| (b) No catalog (`404`) | Many registries restrict it | Nothing to build | A trove chained behind another cannot mirror by listing |
| (c) Names of every readable repository without per-name filtering | Catalog-style visibility | Cheaper | Discloses names a package-scoped grant hides (PKG-DOCKER-003's warning) |

**Choice:** (a).

**Rationale:** the read scope of R-ARCH-008 already filters rows per name. The development agent would revisit virtual expansion if
users browse virtual repositories through the catalog.

**Applied in the spec:** R-OCI-017.

### Q-OCI-20 · The version check answers 401 to a caller without credentials

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 401 with the challenge without credentials, whatever anonymous may read; 200 with any valid credential, the anonymous registry token included (chosen) | The client always learns the realm | Docker registers its token handler from the version check; a 200 there leaves it without one, so a later push fails without retrying | An anonymous client makes one token request before its first pull |
| (b) 200 when anonymous may read something | Fewer requests | One round trip less | Pushes after an anonymous version check fail |

**Choice:** (a).

**Rationale:** clients decide their authentication from this one answer. The development agent would revisit it if a client fails
on the anonymous token request.

**Applied in the spec:** R-OCI-003.

### Q-OCI-21 · The real clients and the conformance suite in the test tiers

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One client pod with dockerd (Buildx included) and Podman in a namespace that admits privileged pods, both pinned and trusting the dev CA; the conformance suite built from its `v1.1.1` tag with the repository's Go and run in the integration tier and against a virtual address (chosen) | The real clients beside the cluster; the specification's own suite | dockerd needs privileges that trove's namespace does not give; Podman alone would miss Docker's push order; the suite is Apache 2.0, a Go test binary, so the one Go version builds it | A privileged pod in the dev cluster |
| (b) The Docker daemon of the CI runner | No pod | Simpler | The runner's daemon is shared with kind; certificates go into the host's `/etc/docker` |
| (c) The suite's published container image | Prebuilt | No build | A second Go toolchain inside an image, against the rule of one version per tool |

**Choice:** (a).

**Rationale:** the clients must be real and isolated from the runner; the suite must be built by the pinned toolchain. The development
agent would revisit (b) if the privileged namespace is refused by the cluster's policy.

**Applied in the spec:** R-OCI-035.

### Q-OCI-22 · A format's mirror settings live in `<format>.mirror` and are kept like the patterns

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The member `oci.mirror` of the format settings; R-REPO-050's rule for Owners without `repositories:write-config` covers `<format>.mirror` of every format (chosen) | Format settings keep the generic `remote.mirror` free of format knowledge | Settings schema and UI are generated from the format's schema (R-UI-021); the restriction is one generic rule | A second place to look for a mirror's scope |
| (b) Members of `remote.mirror` for every format | One place | Visible together | Format-specific members in the generic section, against Q-REPO-5 |

**Choice:** (a).

**Rationale:** Q-REPO-5 keeps format settings under the format's member. The development agent would revisit it if a second format
needs the same members.

**Applied in the spec:** R-OCI-026.

### Q-OCI-23 · No exposure rules and no Bastion for the format in this phase

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Exposure()` returns nil, so an endpoint with `exposure: rules` does not serve the format; the Bastion does not serve `/v2/` (chosen) | Both stay deferred | The phase stays the registry; exposure of tags needs a decision about tag ranges | A customer endpoint cannot hand out images yet |
| (b) Exposure by image name only | Names, no tags | A first customer case | Half a rule set that a later tag dimension would change |

**Choice:** (a).

**Rationale:** R-ARCH-008 makes a format without the adapter unservable on such endpoints, which is safe. The development agent would
revisit it when a customer endpoint needs images.

**Applied in the spec:** [Deferred](#deferred).

### Q-OCI-24 · A storage hook cascades through the host, and a removal without a request has a system actor

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ItemsEvent.Cascade(ctx, paths…)` removes in the transaction of the event and runs the hook again; the actor is the removal's, or the system actor `removal` when there was no request; the sweep job `oci-sweep` lives in `internal/formatdata/oci` and removes through the item store as NuGet's index job does (chosen) | One road for every removal (route, `/files/`, retention, trash purge, job) | The tags and children of a deleted manifest cannot be missed by a removal that has no request | The contract grows a field of `ItemsEvent` |
| (b) Make `PackageWriter.DeleteItems` work for the writer of an event | No new member | Smaller contract | The writer of an event is bound to no request and has no actor; the hook would re-enter its own event by a path the host does not control |
| (c) The format cascades only in its routes | Nothing to add | Simplest | A removal through `/files/`, a retention run or the purge of the trash leaves tags that point at nothing |

**Choice:** (a).

**Rationale:** A format's removal rules (a manifest takes its tags, a tag takes the manifest nobody else names) must hold for every removal, and the host is the only party that knows the transaction and the actor. The development agent would revisit it if a second format needs a cascade that depends on the order of removal.

**Applied in the spec:** [R-OCI-007](#r-oci-007--storage-layout-packages-and-the-formats-tables), [R-OCI-018](#r-oci-018--deletion); [ADR-161](../../decisions.md#adr-161--the-format-contract-gains-the-registry-root-the-upstream-handshake-head-revalidation-and-upload-sessions).

### Q-OCI-25 · A remote manifest keeps its media type in the cache entry and is entered under its digest in the fill transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `remote_cache_entries.media_type` filled from the upstream `Content-Type`, and `Prepared.Aliases`: the engine enters each alias as an item for the same blob and an artifact entry in the fill transaction (chosen) | A tag fetched once serves its digest and its type with no second request; the third bullet of R-OCI-022 holds | No second fetch; the type survives replicas and restarts | Two core changes (a column, the alias step) |
| (b) Derive the media type from the manifest's `mediaType` member | No column | Small | A Docker v2 manifest may omit it; the stored type must be the one the upstream sent, or the client's digest check of a re-encoded type fails |
| (c) Fetch the digest from the upstream on its first use | No engine change | Simplest | `crane manifest …@<digest>` right after a tag pull costs a pull against Docker Hub's limit, which R-OCI-022 forbids |

**Choice:** (a).

**Rationale:** The stored bytes are the only thing the client verifies, and the type is part of what the upstream served; both must come back as fetched. The development agent would revisit it if the alias step needs more than paths derived from the document's bytes.

**Applied in the spec:** [R-OCI-022](#r-oci-022--remote-repository-manifests-by-tag-and-by-digest), [R-OCI-007](#r-oci-007--storage-layout-packages-and-the-formats-tables).

### Q-OCI-26 · Upload session calls repeat a session reference, the host decides, and a 416 carries the received count as a value

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `SessionRef{ID, Binding}` on every call; the host decides principal, binding and `deploy` on the session's target on every call and validates the target at `Start`; `Append` returns the recorded count with a 416 and appends at the received count for an offset below zero (chosen) | A session cannot be continued by a request that names another image or lost its permission; the format renders `Range` from a typed value | The checks cannot be forgotten by a format | Every call carries two values |
| (b) The id alone; the format passes the binding at `Start` only | Smaller calls | Simple | A session id from one image name continues under another; the binding check would be the format's |
| (c) Parse the range out of the 416 error text | No signature change | Smallest | A text is not a contract |

**Choice:** (a).

**Rationale:** The checks that a continued upload still belongs to the caller are security checks and belong to the one party that cannot be bypassed. The development agent would revisit it if a format needs sessions without a binding.

**Applied in the spec:** [R-OCI-011](#r-oci-011--blob-uploads-across-requests-and-replicas), [storage.md](../storage.md) R-STOR-011.

### Q-OCI-27 · A link names the source item, and the host checks it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Ingestor.Link(ctx, rq, from ItemRef, c Commit)`: the host checks that the source is a live untrashed file whose blob is served, that no finding blocks it and that the caller may read it; size and digests come from the blob row; independent of `storage.deploy_by_checksum` (chosen) | A mount, the link step of a virtual push and a tag push share one checked road | A digest alone names nothing the caller may not read | The host side is built in the contract package, with the blob store's `LinkExisting` |
| (b) `Link(sha256, size, Commit)` as first drafted | One value pair | Small | A caller could link any blob that exists; the read check on the source would be the format's |
| (c) Re-upload the bytes through the spool | No new road | Nothing to build | A mount of a 5 GiB layer would pass through the replica |

**Choice:** (a).

**Rationale:** Deploy by checksum is the one thing that lets a caller reach a blob without holding its bytes, so the proof of a right to read is the item, not the digest. The development agent would revisit it if a mount source can be a blob that no item names.

**Applied in the spec:** [R-OCI-012](#r-oci-012--cross-repository-blob-mount), [R-OCI-013](#r-oci-013--pushing-manifests-and-tags), [storage.md](../storage.md) R-STOR-008.

### Q-OCI-28 · The platform is a fact of the reference from an index to a child; a few schema rules for the port

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `oci_references.platform` (NULL for blobs), `oci_manifests.annotations_cut`, the referrers index with the digest, one reference row per target, rows kept across the trash and removed with an evicted remote metadata entry (chosen) | The platform sits where it is known; a page of referrers is read in order from the index | A child that two indexes list has two platforms, one per edge | The row of a child no longer has a platform |
| (b) `oci_manifests.platform` as first drafted | One column | Simple | The child does not know its platform, only an index does; two indexes would overwrite each other |

**Choice:** (a).

**Rationale:** A fact belongs to the row that can know it. The development agent would revisit it if a referrers answer needs the platform of a manifest that no index lists.

**Applied in the spec:** [R-OCI-007](#r-oci-007--storage-layout-packages-and-the-formats-tables), [R-OCI-014](#r-oci-014--multi-platform-images).

### Q-OCI-29 · A route declares its package parameter, and the link builder knows a bound host

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Route.PackageParam`, `EndpointFacts.Bound`, `LinkBuilder.ClientName`; the registry URL of a bound host is `<base>/v2/` (chosen) | The core knows the image name without parsing an OCI path; the token grant and the challenge name the image; a bound host's links are right | One mechanism for any format that has a package in its path | Three small members |
| (b) The core parses `/v2/` paths | No contract change | Direct | The core would know a format's path grammar, against R-ARCH-008 |

**Choice:** (a).

**Rationale:** R-ARCH-008 keeps path grammars in the format. The development agent would revisit it if a route has two packages (a mount names a source name in a query, which the format reads itself).

**Applied in the spec:** [R-OCI-001](#r-oci-001--the-format-oci-its-classes-and-the-registry-address), [R-OCI-029](#r-oci-029--bound-hosts-the-docker-engine-mirror).

### Q-OCI-30 · The deletion of a manifest is journaled, so a restore does not serve a deleted image again

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The audit event `oci.manifest.deleted` with the revocation journal flag, registered now; the table `oci_tombstones` holds the manifests the replay withheld; the replay handler is built with the deletion (chosen) | A deleted, possibly malicious image is not served after a restore | The same road as npm's unpublish; replay never deletes (R-HA-054) | A journaled event without a handler is skipped on replay until the deletion is built |
| (b) No journal for OCI | Nothing to build | Simplest | A restore resurrects every image deleted since the snapshot, malicious ones included, against R-HA-054 |
| (c) The replay deletes the restored manifest | The image is really gone | Tidy | A forged or mistaken entry would destroy data; R-HA-054 allows nothing to be deleted by a replay |

**Choice:** (a).

**Rationale:** R-HA-054 asks the journal for every change that withdraws content where a format page asks for it, and this page does; withholding is reversible and costs availability only. The development agent would revisit it if tags need a journal of their own.

**Applied in the spec:** [R-OCI-018](#r-oci-018--deletion), [R-OCI-033](#r-oci-033--oci-data-in-backups-and-restores); the row of R-HA-054 in [ha-and-helm.md](../ha-and-helm.md).

### Q-OCI-31 · The key grammar migration adds its checks directly and folds the composite names

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 00056 adds the checks in their validated form, replaces the checks of the composite names that embed the project key (principal, policy, CI mapping tier), keeps the compatibility floor at 0.1.0 and says why; the remedy for a refused key is to delete the repository (a key is immutable) (chosen) | One rule for every table; a failing key stops the step with the constraint's name | The floor stays where the harnesses migrate | The step takes a scan lock on the tables |
| (b) `NOT VALID` and `VALIDATE` in one step | Looks gentler | Familiar | In one transaction it holds the stronger lock as long as the direct form and adds nothing |
| (c) Leave the composite names on the old grammar | Fewer statements | Smaller | A project key the project table refuses could appear in a service account name |

**Choice:** (a).

**Rationale:** Pre-alpha has no installation that must survive the step, and the rule should be the same everywhere. The development agent would revisit the floor at the first stable release.

**Applied in the spec:** [R-OCI-002](#r-oci-002--keys-image-names-tags-and-digests); migration 00056.

### Q-OCI-32 · A link names its source by repository name and path, and the host resolves it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Link(rq, from SourceRef{Repo qname.Repo, Path}, c)`: the host resolves the repository by name (id, class, format), finds the item (through the members of a virtual repository, in member order), decides the read on the named repository and runs the checks of R-STOR-008 (chosen) | A cross-repository mount has a road; a virtual source resolves as a read would; nothing the caller supplies is trusted | The format needs no repository snapshot of its own | The host side grows by a resolution |
| (b) `Link(from ItemRef)` with a repository reference the format builds | The format reads the repository | Smaller host side | The id and the name of the reference can disagree, and a format could name a repository it does not hold |

**Choice:** (a).

**Rationale:** The one party that may say which repository a name is, is the host; the answer to a caller who may not read is the same as for a missing item. The development agent would revisit it if a mount needs a source that is not an item (a blob a name does not hold).

**Applied in the spec:** [R-OCI-012](#r-oci-012--cross-repository-blob-mount).

### Q-OCI-33 · A manifest the journal replay withheld is refused by the serve check from facts read with the item, and an administrator reinstates it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The serve-facts kind `oci_local` (R-ARCH-027): the one statement of the item lookup returns the manifest row and whether a tombstone withholds the item's digest; the replay writes the mark and deletes nothing; `trove admin oci reinstate` removes it; a push of a withheld digest is refused until then (chosen) | The hottest route costs no extra query; a restore never deletes; the way back is explicit | The same shape as npm's tombstone and reinstate | A column more in the lookup statement; an admin command to build |
| (b) The serve check queries the data port on every manifest pull | No contract addition | Smaller statement | A second statement on the hottest route, against R-ARCH-027 |
| (c) The replay deletes the manifest | Nothing to serve | Simple | A forged entry destroys data, against R-HA-054 |

**Choice:** (a).

**Rationale:** A restore must neither serve a deleted image nor destroy a possibly wrong entry's target. The development agent would revisit it if withholding needs more than a digest (a tag-level withhold).

**Applied in the spec:** [R-OCI-033](#r-oci-033--oci-data-in-backups-and-restores), [R-OCI-013](#r-oci-013--pushing-manifests-and-tags); the row of R-HA-054 in [ha-and-helm.md](../ha-and-helm.md).

### Q-OCI-34 · The rows of a remote manifest are tied to its alias item

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The hook removes a remote's manifest rows when the alias item `<name>/_manifests/<digest>` is removed; tag documents and the tags list are metadata entries and own no row (chosen) | Eviction of a cached manifest reaches a format hook, because it removes an item | No new hook for metadata eviction | The rows depend on the alias item the fill enters |
| (b) A hook for the eviction of metadata entries | The rows follow the tag document | Direct | A new contract member for one format; the tag document is not the manifest |

**Choice:** (a).

**Rationale:** An item removal already runs the format's hook; a metadata eviction does not. The development agent would revisit it if a remote keeps a manifest that has no alias item.

**Applied in the spec:** [R-OCI-007](#r-oci-007--storage-layout-packages-and-the-formats-tables), [R-OCI-022](#r-oci-022--remote-repository-manifests-by-tag-and-by-digest).

### Q-OCI-50 · A registry token names its trove token by the SHA-256 of its secret and is verified through the token cache

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The claims name the authenticating trove token by its ID, its type and the SHA-256 of its secret; verification asks the token cache of R-AUTH-023 with the hash, exactly as for the trove token presented itself (chosen) | One cache, one freshness gate, one invalidation, one throttle and one rejection event for both credentials | The revocation bound of the registry token is the trove token's by construction; a client that logs in with the trove token fills the entry its registry tokens use, so a pull costs no extra query; a holder of the key ring needs a live token's hash to forge (H-579) | The hash travels in every registry token; it allows no guess of the secret (256 bits, R-AUTH-023) and is what the database holds |
| (b) The claims name the trove token by its ID only; a second cache keyed by ID and a query by ID | The content R-OCI-005 first listed | Nothing but an ID in the token | A fifth auth cache with its own fill, invalidation and gate; a query per replica and cache period that the trove token's own lookup already paid; a ring holder forges with an ID that every listing and audit event shows |
| (c) The claims name the trove token by its ID; every request reads its row | No cache | Simplest | A database read on the hottest route, against R-AUTH-023 and R-OCI-005 |

**Choice:** (a).

**Rationale:** the registry token is the trove token narrowed by a grant and a lifetime, so it should be checked by the same steps;
reusing the cache key makes the revocation bound and the throttle identical rather than parallel. The development agent would revisit it
if the hash of a token had to be kept from the clients that hold its registry tokens (an encrypted payload would then carry it).

**Applied in the spec:** [R-OCI-005](#r-oci-005--registry-tokens), [H-579](#h-579--a-holder-of-the-stored-secret-key-ring-can-mint-registry-tokens);
R-AUTH-022 and R-AUTH-023 in [identity-and-authentication.md](../identity-and-authentication.md); ADR-163.

### Q-OCI-51 · The token service presents the credential of a token request to the authentication stage itself

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `/v2/_token` is a route whose credential the chain does not evaluate; the token service takes the one credential of the request (the `Authorization` header of a `GET`, the password of the form of a `POST`) and hands it to the stage (`AuthenticateTokenRequest`), which applies every rule of R-AUTH-001 and of the endpoint (chosen) | One path for both request forms; the stage stays the one place that checks a credential; the handler never reads a secret, it holds an opaque value whose hash binds the registry token | The form of a `POST` needs no body read in the stage | The chain's request log records the token request as anonymous unless the composition root records the caller the token service authenticated; a rate-limit class keyed by principal counts every token request in the bucket of its address, because the chain sees it as anonymous |
| (b) The chain authenticates the `GET` as every route; the handler re-reads the header to bind the token | No new stage entry point | The chain logs the caller | The handler parses a credential (against R-AUTH-001), and a `POST` on an endpoint without anonymous requests is refused before its form is read |
| (c) The stage reads the form of a `POST /v2/_token` | Everything in the chain | One authentication per request | A body read before the rate limits of step 7 and before routing |

**Choice:** (a).

**Rationale:** the token service is a credential exchange like the CI exchange (R-AUTH-001 "Exchange endpoints"), and handing the
presented value to the stage keeps the rules of the endpoint, the throttle and the metrics in one place for both request forms. A
registry token is refused there by the syntax step of a trove token, so it cannot renew itself. The development agent would revisit it
if a client sends its credential in another field of the form.

**Applied in the spec:** [R-OCI-004](#r-oci-004--authentication-challenge-and-token-service); R-AUTH-001 in
[identity-and-authentication.md](../identity-and-authentication.md).

### Q-OCI-52 · A malformed token request answers 400 in the distribution shape and the OAuth 2.0 shape at once

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One JSON object with the error list of R-OCI-008 (code `UNSUPPORTED`, the OAuth code first in the message) and the members `error` and `error_description` of RFC 6749 §5.2 (chosen) | Every error below `/v2/` keeps the body of R-OCI-003, and a client of the OAuth form reads its member | Both readers find what they parse; no client breaks on an unknown member | Two encodings of one refusal |
| (b) The error list of R-OCI-008 only | One shape below `/v2/` | Uniform | The codes `invalid_request` and `unsupported_grant_type` that R-OCI-004 names have no place |
| (c) The OAuth 2.0 body only | The form's own standard | RFC conformant | Breaks R-OCI-003 for one route |

**Choice:** (a).

**Rationale:** the token endpoint speaks two protocols; a body that satisfies both costs one member more. containerd falls back to a `GET`
on a 400 whatever the body says, so the body is read by people and tools. The development agent would revisit it if a client refuses an
object that carries both.

**Applied in the spec:** [R-OCI-004](#r-oci-004--authentication-challenge-and-token-service).

### Q-OCI-53 · `auth.registry_token.denied` is an access event deduplicated per client address, endpoint, reason and credential

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) An asynchronous event per 401 of the token service with the endpoint in the event's column, deduplicated per replica, client address, endpoint, reason and form of the credential within `audit.token_rejection_dedup_window`; a rejected credential counts in the failed-credential throttle (chosen) | A client that loops on a wrong password writes one event and one summary per window and replica | Bounded volume whatever the request rate (at most three reasons times five forms per address and endpoint); a summary never folds an `invalid_token` or a `principal_disabled` under a harmless `credentials_required`, and a disabled owner's attempts, which write no `token.rejected`, keep a trail of their own | Up to n first events per window and key with n replicas (R-OPS-015) |
| (a') The same, keyed on the client address only | One key per address | Fewest rows | The first event's reason and form stand for the whole window: stuffing after a harmless refusal is hidden |
| (b) One event per refusal | Every attempt recorded | Complete | An anonymous caller fills the audit table at the throttle's rate |
| (c) A log record only, as `auth.credential.rejected` | No row | No volume | A login loop of a registry client leaves no trace in the trail |

**Choice:** (a).

**Rationale:** the deduplication of R-OPS-015 already bounds the token rejections; the token service's refusals are the same kind of
signal, and the key must keep apart what an investigation tells apart. The development agent would revisit it if the per-replica
summaries turn out too coarse for an investigation.

**Applied in the spec:** [R-OCI-034](#r-oci-034--audit-events-and-metrics); R-AUTH-041 in
[identity-and-authentication.md](../identity-and-authentication.md); R-OPS-015 in [operations-and-observability.md](../operations-and-observability.md).

### Q-OCI-54 · The key ID stands before the claims in the payload, so the MAC is checked before anything is parsed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) payload = `1:<key id>:<claims as JSON>`; the MAC covers `trv_rgt_` + base64url(payload) as received (chosen) | The verifier reads the version and the key ID by two cuts, checks the MAC with the named key and parses the JSON only when it is authentic | No parser runs on forged input; no canonical form of the claims is needed | A form of its own instead of a JWT |
| (b) The key ID inside the JSON | One encoding | Plain | The JSON of every forged token is parsed before its MAC is known |
| (c) Every key of the ring is tried | No key ID | Simple | A forged token costs one MAC per key; an error cannot say which key |
| (d) A JWT (JWS compact) | A known format | Libraries | Header-driven algorithm choice and the JWS pitfalls for a token only trove reads |

**Choice:** (a).

**Rationale:** authenticate before parse is the cheapest defence against crafted input on the hottest route. The development agent would
revisit it if another party had to verify registry tokens.

**Applied in the spec:** [R-OCI-005](#r-oci-005--registry-tokens).

### Q-OCI-55 · The grant asks the decision point as a question with the image name as the package

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per scope entry and action, the listing form of the decision (`PathsAllowed`) on the request's index with the image name as the package and the request kind of the action (read: metadata; deploy and delete: write); no audit event (chosen) | The coarse check of R-OCI-006, with the token scope, the endpoint ceiling and the anonymous switch | No `authz.denied` for an action the client merely asked for; anonymous callers are decided like any other | Read is asked on metadata, so a holder of `content:metadata-read` alone gets `pull` and is refused per artifact request |
| (b) `DecideOn` per entry | The request's decision | Exact | Writes `authz.denied` for every scope a client asks for and is not granted |
| (c) The check API of R-AUTHZ-023 (`CheckOn`) | A question without audit | Built for it | Refuses an anonymous subject, so anonymous tokens would grant nothing |

**Choice:** (a).

**Rationale:** a grant is a ceiling that must not be stricter than the request's own decision (Q-OCI-6), and a question must not be
audited as a refusal. The development agent would revisit it if the decision point gains a question form for one coordinate.

**Applied in the spec:** [R-OCI-006](#r-oci-006--permissions-per-operation).

### Q-OCI-56 · One enforcement point in front of /v2/ checks a registry token's grant and fails closed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `registry.Enforcer` is the only way to mount below `/v2/`: `Mount(router, pattern, handler, routes...)` registers the routes a handler serves (kind, coarse-check action, an example request) and puts the handler behind the check in one call; the start-up route-table check admits exactly the patterns mounted through it; the route of a request is the dispatcher's own match (`dispatch.Dispatcher.RegistryTarget`, as the stage shares `APIKeyRoute`); a registry token on a route without an entry, on an address the match cannot read, or on a route whose action has no token action answers 401 `insufficient_scope` with the code `oci_insufficient_scope`; exactly one version check, `GET` or `HEAD` of `/v2/`, which passes on `/v2/` only; `registrytest.Walk` proves it through the endpoint's whole handler with real registry tokens (chosen) | One place decides the ceiling for every route, before any handler, and nothing below `/v2/` can be mounted around it | A route that forgets its entry is refused instead of handing the registry token its trove token's full power; a handler mounted around the enforcer stops the start-up; one match cannot disagree with itself; the walk sees what a client sees: the stage, the conversion and the enforcer | The registry root mounts through an API of the registry package rather than the router directly |
| (a') The same with a separate `Register` and `Wrap` and a resolver of the caller's choice | Fewer constraints on the mount | Flexible | A handler that is registered but not wrapped, or wrapped with a second matcher that disagrees with the dispatcher, defeats the check; the walk cannot tell |
| (b) Each route calls `Allows` itself | The check where the route is | No table | A route that forgets the call grants everything the trove token may do; the catalog, the tags list, the upload sessions and the mount's source are each a place to forget it |
| (c) The grant in `authz.Subject`, decided by the decision point | No new component | The decision point sees everything | A missing scope becomes 403 instead of the 401 `insufficient_scope` clients act on; `authz.denied` for every scope a client did not ask for; the catalog's per-name filtering would have to know the grant |

**Choice:** (a).

**Rationale:** the grant is a ceiling the decision point does not see, so it needs its own single point of enforcement, which must
fail closed for what it does not know and must not be avoidable by the way a handler is mounted. The cache view is granted under the
base repository because the dispatcher decides and serves it on the remote repository itself. `registrytest.Walk` checks that the
registered names are exactly the format's routes and the three core routes, that every `/v2/` pattern of the router was mounted
through the enforcer, that the dispatcher matches each example to its own route, that a real empty-grant token is refused on every
route but the version check, and, per repository route, that every token action but the route's is refused and exactly the route's
passes. The development agent would revisit it if a route below `/v2/` needs a ceiling the table cannot express (more than one image
name per request besides the mount's source, which `Enforcer.Also` checks action by action).

**Applied in the spec:** [R-OCI-005](#r-oci-005--registry-tokens); R-ARCH-005 in [architecture.md](../architecture.md); ADR-163.

### Q-OCI-57 · The password of the OAuth 2.0 form is a Basic presentation and follows `auth.clients.basic.enabled`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The form's password counts as Basic: with `auth.clients.basic.enabled: false` the token service refuses it, as it refuses a `GET` with `Basic` (chosen) | One switch for every presentation of a token as a password | The switch means what it says; an operator who turned Basic off for a reason does not find a second road | No registry client gets a token with Basic off (Docker and Podman send Basic on `GET`, containerd posts the form and falls back to Basic) |
| (b) The form is exempt from the switch | The form is a credential of its own | containerd works with Basic off | Docker and Podman still fail; a switch with an exception nobody expects |
| (c) The token service ignores the switch altogether | The registry always works | Every client works | The switch no longer turns off Basic presentation everywhere (IAM-036) |

**Choice:** (a).

**Rationale:** exempting only the form would make one client work and leave the two most common ones broken, at the price of a switch
that no longer covers every password presentation. The registry clients are named in the switch's description, the configuration
reference and the Basic switch of R-AUTH-033. The development agent would revisit it if an installation needs the registry with Basic
off (a client that presents the trove token as `Bearer` at the token service, which the stage accepts, would be the road).

**Applied in the spec:** [R-OCI-004](#r-oci-004--authentication-challenge-and-token-service); the Basic switch of R-AUTH-033 in
[identity-and-authentication.md](../identity-and-authentication.md).

### Q-OCI-60 · A manifest is read by a strict reader that refuses what two readers could interpret differently

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A reader of its own over the JSON tokens: every object key once, a key that differs from a known key only in case refused, valid UTF-8, one value, sizes as plain integers, `data` verified against size and digest, unknown fields ignored (chosen) | The manifest is understood exactly one way | Clients decode with the standard decoder, which keeps the last of two equal keys and matches `Layers` to `layers`; a registry that reads such a document one way and a client another stores what the client then misreads (a digest-pinned policy check sees one manifest, the runtime pulls another) | A few documents the standard decoder accepts are refused; no supported producer writes them |
| (b) The standard decoder into structs | `encoding/json` after the depth scan | Least code | The differences above; the last duplicate wins silently |
| (c) (a) and refuse unknown fields | Closed schema | Strictest | The image specification is extensible, and a future field would break every push |

**Choice:** (a). Unknown fields are stored with the bytes (the manifest is stored as received) and ignored by the registry.

**Rationale:** the registry's reading of a manifest decides the rows that referrers, deletion and the sweep trust, and clients read the same bytes with other code. Refusing the ambiguous cases costs nothing a producer does. The development agent would revisit it if a supported producer emits a duplicate or a differently cased key.

**Applied in the spec:** [R-OCI-013](#r-oci-013--pushing-manifests-and-tags) step 3, [R-OCI-020](#r-oci-020--limits-for-untrusted-content). Code: `internal/formats/oci/manifest`.

### Q-OCI-61 · A push is checked before it is stored and again in the commit transaction, on rows held against a removal

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The references are checked before the commit, and in the hook under the package row lock with `FOR SHARE` on the named items; a new manifest commits as the tag item (lead) with the manifest item (companion), a stored one is linked (chosen) | A refused push stores nothing, and a blob cannot go between the check and the row | One round trip answers the common refusal before any object is written; the second check closes the race with a removal | Two queries per push, each over at most 10,000 paths |
| (b) Before the commit only | One check | A deletion between the check and the commit leaves a manifest that names a missing blob | The registry then serves an image that cannot be pulled |
| (c) In the hook only | One check, under the lock | The check is exact | The body is spooled and the object written before the client hears that a layer is missing |

**Choice:** (a).

**Rationale:** `FOR SHARE` on the item rows makes the removal of a named blob wait for the push's transaction; the hook takes the package row (`EnsureLocked`) before it locks the items it names. The order of all locks of a push is not one order: the item store has locked the item of the path first (the tag item of a push by tag, the manifest item of a push by digest), then the hook takes the package row, then the companion manifest item and the named items. A removal that takes the package row first and the item after it (the tag delete, the sweep) inverts the order against a push of the same tag, and a tag push and a digest push of the same new manifest invert it against each other; the database breaks such a cycle (40P01) and the transaction runner retries the loser (H-608). A removal must take the package row before it deletes the items a manifest names and read the references again after it has the lock. The development agent would revisit it if the second query shows in the latency of pushes of large indexes, or if retries of a cycle show in the metrics.

**Applied in the spec:** [R-OCI-013](#r-oci-013--pushing-manifests-and-tags) steps 4 and 8.

### Q-OCI-62 · The `ETag` of a manifest or a blob is the digest in quotes; the item store compares the hex

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The response carries `"sha256:<hex>"` (the spelling of R-OCI-008) and the conditional headers of the request are translated to the item store's `"<hex>"` before it evaluates them (chosen) | `Docker-Content-Digest` and `ETag` say the same | A client that sends back what it was given gets `304`; the item store stays as it is for every format | A copy of the request's headers when a validator of the registry's form is present |
| (b) Leave the item store's `"<hex>"` | No translation | Nothing to build | The documented `ETag` is wrong and a client's `If-None-Match` of the documented form never matches |
| (c) A second conditional evaluation in the format | The format answers `304` itself | Independent of the store | It skips the serve check and the download statistics |

**Choice:** (a).

**Rationale:** the validator is a presentation of the digest; translating at the boundary keeps one conditional and range implementation. The development agent would revisit it if the item store's `ETag` moves to the digest's own spelling.

**Applied in the spec:** [R-OCI-008](#r-oci-008--error-codes-and-response-headers).

### Q-OCI-63 · The first push of a name creates its package row and counts as its first version

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The first manifest that reaches a name, by digest or by tag, creates the package row, claims the name and runs the shadowing check while the name holds no live version; a tag is the only thing that makes a version; a moved tag is a change by an owner or a writer and never a claim (chosen) | The ownership rule and the shadowing check see the name when it first appears | The children of a multi-platform image are pushed by digest before any tag exists; without this the name would be unprotected until the tag | A push by digest to an unclaimed name claims it for the pusher, as the first tag would have |
| (b) Only a tag push creates the package | A package always has a version | Package rows without versions do not exist | The children of an image would be written to a name nobody owns, and a second pusher could claim it with the tag |
| (c) The ownership rule and the check run on tag pushes only | Fewer checks | Fewer calls | A push by digest is a write to the name |

**Choice:** (a). The shadowing check runs once while the name has no live version; a commit that finds the push to be the first after the check was skipped asks the client to try again, as NuGet's does.

**Rationale:** a name is protected from the first byte that is stored under it. The development agent would revisit it if package rows without versions show up as noise on the package screens.

**Applied in the spec:** [R-OCI-006](#r-oci-006--permissions-per-operation), [R-OCI-013](#r-oci-013--pushing-manifests-and-tags).

### Q-OCI-64 · The tag order ranks a release above its variants above its pre-releases

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Of Q-OCI-17 (a): equal numbers rank the release (no suffix) first, then a suffix that marks no pre-release (a variant such as `alpine`), then a pre-release, and within a rank by the bytes of the suffix; two different tags never compare equal (chosen) | `1.29.3`, `1.29.3-perl`, `1.29.3-alpine`, `1.29.3-rc1` | A variant of a release is newer than the release candidates before it; a total order, as the view contract asks | The relation of a variant to a later pre-release of the next version is by number first, as a reader expects |
| (b) Suffix bytes only | `-alpine` before `-rc1` by accident | Simplest | A pre-release above a variant of the final release |

**Choice:** (a). The package `internal/formats/oci/tagorder` is the one implementation; the package view's `Compare` uses it and the mirror's tag selection will.

**Rationale:** Q-OCI-17 leaves the rank of the suffixes open. The development agent would revisit it if mirrors of the owner's images keep the wrong tags.

**Applied in the spec:** [R-OCI-031](#r-oci-031--package-screens).

### Q-OCI-65 · The version publish and the overwrite of a tag are the events of the item commit

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The commit of the tag item records `artifact.deployed` with `overwritten` (true for a moved tag) and the write decisions `deploy` and `overwrite` are recorded in the same transaction (chosen) | The events the item store writes for every format | A tag move is visible to the audit as an overwrite with the digest it moved to; no event of the format's own to keep in step | The event names an artifact, not a package version |
| (b) A `package.published` event from the data port, as NuGet writes | The event of the other formats | One vocabulary for versions | A tag is mutable and has no immutable version identity; NuGet's event belongs to an immutable number |

**Choice:** (a).

**Rationale:** a tag is a pointer, and the event that records the pointer moving is the overwrite of its item. The development agent would revisit it if the audit screens need the package and version of a push.

**Applied in the spec:** [R-OCI-013](#r-oci-013--pushing-manifests-and-tags), [R-OCI-034](#r-oci-034--audit-events-and-metrics).

### Q-OCI-70 · The referrers answer pages by the last digest and reports a cut annotation set in a header

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The `Link` of a longer list carries `last=<digest>` (the digest the page ended at) next to the `artifactType` filter; an answer whose descriptors include a manifest whose annotations were cut at 64 KiB carries `Trove-Annotations-Cut: true` (chosen) | The cursor is the index order of R-OCI-007; the cut is told in a header | No state, no token to sign; a client follows the `Link` as it is; the cut is visible without changing a descriptor | A header that is trove's own |
| (b) An opaque, signed cursor | The page position hides behind a token | A format may change its order | A key and a lifetime for a cursor that is a digest anyway |
| (c) Report the cut as an annotation of the descriptor | In the document | One place | Changes what the manifest says about itself and breaks a client that compares annotations |

**Choice:** (a).

**Rationale:** R-OCI-007 keeps the annotations a referrers answer needs and records the cut; the answer has to tell a client that what it reads is cut, and a header does so without touching the descriptors. The development agent would revisit it if the distribution specification adds a way to report a truncated descriptor.

**Applied in the spec:** R-OCI-016.

### Q-OCI-71 · The sweep removes one blob item per transaction, under the lock of the name's package, and reads the references again

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The job scans a repository in path order for blob items older than the grace that no manifest of the name references and no unfinished upload session holds the digest of; it removes each in a transaction of its own that first locks the package row of the image name, reads the references and the sessions again, and removes through the item store, so the storage hook runs (chosen) | A push that commits a reference while the sweep waits for the lock keeps its blob | The sweep and a push meet at the one lock every writer of the name takes; a removal is audited and refused like any other | One transaction per blob |
| (b) One statement deletes every candidate | Fast | Simple | No lock, no hook, no audit event: a manifest that commits between the scan and the delete loses its layer |
| (c) The sweep collects the blob objects itself | No item removal | Skips the item store | A second road to the blob store, against R-STOR-024 |

**Choice:** (a). The scan reads `items` by the parent path `…/_blobs`; it has no index of its own because the job runs once an hour and the grace bounds what it finds.

**Rationale:** the grace of R-OCI-019 keeps the blobs of a push whose manifest has not arrived yet, measured from the item's creation. The
lock and the second read close the window between a manifest's validation and its commit: for a name that has a package row the sweep
takes the same row a push takes first, and for a name that holds only blobs (no package row, and none is made for it) the rows of the
blobs arbitrate, because a push holds them `FOR SHARE` until its own rows are written and the hook of the removal reads the references the
push committed. What the grace does **not** protect is a blob that was created long ago, stayed unreferenced, and is reused: a client that
finds it with `HEAD`, or uploads the same bytes again (an identical commit changes no row), can see it swept before its manifest arrives;
the push then answers `MANIFEST_BLOB_UNKNOWN` and the client uploads again ([H-643](../../security/oci-content.md#h-643--a-blob-reused-after-the-grace-can-be-swept-before-its-manifest-arrives)). The development agent would revisit the scan with an index when a repository holds so many blob items that the hourly scan costs more than the job queue's lease, and the grace when a client is found that reuses blobs that late.

**Applied in the spec:** R-OCI-019.

### Q-OCI-73 · Every removal path refuses what would leave a dangling reference, and the refusal is `409 DENIED`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The hook `OnItemsRemoved` holds the rules (a manifest an index of the name lists, and a blob a manifest references, are refused with a conflict unless the same removal takes the referencing manifest too; a removed manifest takes its tags, its referrers that no index lists, and the children no other manifest and no tag names; it writes `oci.manifest.deleted` per manifest); the routes check authorization, existence and ownership and let the hook decide; the registry maps a conflict to `409` `DENIED` (chosen) | One rule for the protocol, `/files/`, the items API, a retention run and the sweep | A removal through any path keeps the graph whole | A bulk removal of a subtree must be ordered by the item store only by the hook's own set of paths |
| (b) The routes check the references | Simple | Routes only | A raw delete leaves a dangling reference |
| (c) Cascade the referrers that an index lists as well | Tidier | Fewer leftovers | A deletion of a subject would delete a manifest an index still names |

**Choice:** (a). The item store tells the hook whether a referencing manifest belongs to the same removal (`ItemsEvent.InScope`): a delete of a whole image name reaches the hook in events of at most 5,000 files, or in the batches of the job above `storage.delete.max_sync_items`, and the blobs of a name sort before its manifests, so the hook would otherwise refuse the delete of a name with more than 5,000 files. A job commits its batches one by one, so the hook removes a blob **together with** the manifests of the same delete that reference it, in the transaction of the blob (and an index with the child it lists), through the cascade: a batch that is refused or a job that is cancelled afterwards leaves no manifest over a removed blob. A manifest outside the delete (pushed after it was authorized) refuses the blob, and the batch fails whole. The refusal carries the code `oci_referenced`, which the sweep alone treats as "kept". A referrer that an index lists stays (its subject is gone, which a manifest may allow); the trash can does not exist yet, so an event with the reason `trash` keeps the rows and cascades nothing but still refuses; the deletion of a blob changes no row of the format and raises no revision.

**Rationale:** the protocol's deletes and trove's item store meet in one hook (Q-OCI-11, Q-OCI-24). The development agent would revisit the referrers rule if a client needs a subject's deletion to take every referrer.

**Applied in the spec:** R-OCI-018.

### Q-OCI-74 · The setup snippets log in to the image prefix, and the Kubernetes pull secret is a snippet of the Docker client

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `docker login` and `podman login` name `<host>/<projectKey>/<repoKey>` (`SetupData.ImagePrefix`), the token is read from standard input, a reader without a credential gets the interactive login, and the Kubernetes pull secret (`kubectl create secret generic --type=kubernetes.io/dockerconfigjson`, the file on standard input) is a configure snippet of the Docker client with the same prefix as its server (chosen) | One spelling for the login, the pull and the push | No template function and no new `SetupData` field; Kubernetes matches a secret to the images below its server | The login names a path, which Docker keys by host |
| (b) A new `SetupData.Host` | The login names the host only | Exact | A contract change for one format, after ADR-161 |
| (c) A third client `kubernetes` | Its own tab | Clear | No client of the registry; the e2e suite pins only two |

**Choice:** (a). The tested majors are those of the e2e suite (R-OCI-035); until it pins them the descriptor names Docker 28 and Podman 5.

**Rationale:** the snippets are rendered over `SetupData` only, which has no host field. The development agent would revisit it with the first user who finds the path in the login confusing.

**Applied in the spec:** R-OCI-030.

### Q-OCI-75 · The store's catalog page lists the image names that hold a manifest, per repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Store.CatalogNames(repo, after, limit)` returns the distinct names of the repository's manifest rows in byte order; the registry root merges the pages of the repositories the caller may read and decides each name (chosen) | A page is one index range | The rows exist for local repositories and for the cache of a remote one (R-OCI-007); no join with items | A name that only holds blobs, or only a tag document, is not listed |
| (b) Names from the packages table | Generic | Versions only | A manifest without a tag is not a version, so an untagged image would vanish |

**Choice:** (a).

**Rationale:** R-OCI-017 lists the names of images that can be pulled; a pullable image is a manifest. The development agent would revisit it if a name should appear before its first manifest.

**Applied in the spec:** R-OCI-017.

### Q-OCI-76 · A delete is decided by the path of every item it removes, and a delete by digest is refused when its cascade would take an item the caller may not delete

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The routes decide the item they remove by its package, version and **path**; the cascade of the hook is held to the grants of the caller by the host: every path a hook cascades to is decided for `delete` with the listing-form check `Host.PathsAllowed` (one evaluation per path, no audit event), at every depth, and the first refusal refuses the whole removal (403, rolled back); the same guard covers a delete through `/files/`; the tags list and the referrers answer decide their entries in one batch and leave out the ones the caller may not read, and a Link names the last entry **listed**, never a hidden one (chosen) | The registry route is as strict as `/files/`, and the check and the cascade are one list that cannot drift apart | A grant scoped by path (`**/_tags/keep`) holds on every road; nothing is removed or shown that the caller could not remove or read by another road; the audit trail gets no event per hidden entry | A cascade that reaches a path the grant excludes refuses a delete that the caller named innocently; a delete job and a retention run have no subject and cascade unchecked |
| (b) Decide the name only | The cascade is the hook's | Cheap | The registry route bypasses a path-scoped grant that `/files/` honours |
| (c) Let the delete by digest remove the excluded items and document it | Simpler | No refusal | A caller removes what its grant withholds |

**Choice:** (a). The decisions of a list are made in batches with the path of each entry; an entry the caller may not read is left out. The page is filled by reading on until `n` readable entries are found; a request that cannot find one within 10,000 tags (4,000 referrers) is refused with `400 UNSUPPORTED`, because the only cursor left would name a hidden entry (an opaque, sealed cursor needs a key derivation from the key ring and a new Host method: disproportionate for this case, [H-645](#h-645--a-list-with-more-than-10000-consecutive-tags-the-caller-may-not-read-is-refused)). The guard is installed by the request: the writer of `Host.Write` for the registry routes, and the files service for `/files/`.

**Rationale:** a grant is a statement about paths (R-AUTHZ-005); the protocol and `/files/` reach the same items, and a cascade removes items the caller never named. The audited decision writes one `authz.denied` event per hidden entry (its dedup key includes the path), up to 1,000 per page; the listing form writes none, so the audit volume and the latency of a page are both bounded. The development agent would revisit the refusal of an unpageable list when a deployment has more than 10,000 consecutive hidden tags, and the unguarded cascade of jobs when a retention run is given a subject.

**Applied in the spec:** R-OCI-006, R-OCI-015, R-OCI-016, R-OCI-018.

### Q-OCI-80 · The token cache is read after the challenge, and a rejected cached token is replaced once

Status: **Superseded by [Q-OCI-82](#q-oci-82--the-engine-asks-the-format-before-a-request-with-the-last-challenge-so-only-the-first-request-of-a-replica-is-challenged)** (decided by the development agent; the owner reviews it at the walkthrough). The text below is what was decided first.

Context: the key of a token is the scope the format reads from the registry's challenge (`repository:<name>:pull`); the realm and the
service come from the same challenge. The contract (`Authenticator.TokenRequest`) therefore names the key only after a `401`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Ask the format after the `401`, look the key up, retry with the cached token without a request to the token service; a cached token that the retry shows rejected is dropped and one fresh handshake runs (chosen) | Every request to a registry that wants a token costs one challenged request | The contract stays as WP0 froze it; the engine guesses no scope; no token of another scope is sent | One extra request to the registry per request; the registry counts a `401` |
| (b) The engine remembers the key of the last handshake per repository and sends that token first | The first request carries a token | No challenged request when the guess is right | The guess is wrong for every image but the last; a token of another scope goes to the registry; the engine would have to know what a scope is |
| (c) A contract member that names the key before the request | The format derives the key from the path | No challenged request | The realm and service still come from a challenge, so the token request cannot be built without one |

**Choice:** (a).

**Rationale:** the acceptance criterion of R-OCI-021 is that a second pull within the token's lifetime sends no token request, which (a)
meets; the challenged request is a bodiless `401` that no registry counts against a pull quota. The development agent would revisit it if a
registry rates unauthenticated requests.

**Applied in the spec:** [R-OCI-021](#r-oci-021--remote-repository-upstream-and-its-token-handshake). Code: `Engine.token` and
`Engine.authenticate` in [authenticator.go](../../../internal/proxy/authenticator.go).

### Q-OCI-81 · A HEAD that is not a 2xx is the engine's answer, and a 2xx that is not unchanged is a GET

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: `Revalidator.Unchanged` reads the answer of the format's revalidation request. The contract says the engine reads a status that
is not 2xx itself.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A 2xx goes to `Unchanged`: true renews the freshness, false fetches with the engine's `GET`; any other status follows the rules of a conditional `GET` (`404` and `410` mark the document gone, `429` and `5xx` serve the stored copy and count toward the circuit, a `401` runs the handshake first) (chosen) | One status table for both requests | The format reads only what it understands; a `HEAD` is never treated differently from a `GET` for failures | A `404` to the `HEAD` is final, without a confirming `GET` |
| (b) A `HEAD` that is not 2xx falls back to the `GET` | The `GET` decides | A proxy that answers `HEAD` badly cannot break a pull | A tag that is gone costs a `GET` that counts against the pull limit, which the `HEAD` exists to avoid |

**Choice:** (a). The renewal is recorded with the `HEAD`'s status in the `Cache-Status` (`fwd=stale; fwd-status=200`).

**Rationale:** a registry that answers `HEAD` and `GET` differently for the same URL violates the distribution specification. The
development agent would revisit it if Docker Hub or another well-known registry does.

**Applied in the spec:** [R-OCI-022](#r-oci-022--remote-repository-manifests-by-tag-and-by-digest). Code: `fetcher.revalidate` in
[revalidate.go](../../../internal/proxy/revalidate.go).

### Q-OCI-82 · The engine asks the format before a request with the last challenge, so only the first request of a replica is challenged

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough. Supersedes [Q-OCI-80](#q-oci-80--the-token-cache-is-read-after-the-challenge-and-a-rejected-cached-token-is-replaced-once).

Context: Q-OCI-80 read the token cache only after a `401` had named the key, so every request to a registry went out without a token,
was challenged and was sent again. The architecture review of the first build (a separate agent) showed what that costs, and that the
contract allows better: `TokenRequest` receives the request (`orig`) and a challenge, and the realm and the service of a challenge do
not depend on the request.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Ask after the `401` only (Q-OCI-80) | Every request is challenged once | The contract as WP0 froze it; nothing remembered | About twice the requests to the registry (below); the repository's long-lived credential goes to the registry on every challenged request; half of `trove_remote_upstream_requests_total` for a registry remote is `401`; an overall abuse limit per address that counts every request, `401` included, is met sooner |
| (b) Remember the last challenge per repository and configuration version; before a request call `TokenRequest(orig, p, rememberedChallenge)`, attach a token the replica holds under the key it returns, else fetch one first; a rejected pre-emptive token falls into the handshake of the `401`'s own challenge; any failure of the pre-emptive step sends the request without a token (chosen) | Only the first request of a replica and configuration version is challenged | No signature change; the existing fallback loop covers a stale challenge; the credential goes to the registry once | The contract needs one binding sentence: the format takes the scope from `orig` and `p`, and only the realm and the service from the challenge; a token is fetched for a request the registry might have served anonymously |
| (c) Use only tokens that are already cached, never fetch one pre-emptively | A cache hit saves the challenge | Simpler | Saves nothing on a cold pull of a new image: every layer of it is a new scope |

Cost for Docker Hub (a token lives 300 s, the engine uses it for 290 s; counted, not measured):

| Case | (a) | (b) |
|---|---|---|
| Cold pull of a new image (an index, a platform manifest, a config and 7 layers) | 2 × 10 requests to the registry and 1 token request: 21 | 10 and 1: 11 (one more for the first request of the replica) |
| One tag revalidated by `HEAD` every 300 s, its token expired | `HEAD` 401, token request, `HEAD` 200: 3 | token request, `HEAD`: 2 |
| Further tags of the same image within 290 s | 2 each | 1 each |

**Choice:** (b). The binding contract sentence is on `formats.Authenticator` and in R-ARCH-008: the format takes the scope (and so the
key) from `orig` and `p`, and only the realm and the service from the challenge. A format that reads the scope from the challenge is
wrong, because the remembered challenge belongs to an earlier request.

**Rationale:** the challenged request is the cost of a design that learns the scope from the answer, and the contract does not need
that. The remembered challenge is a hint and never the answer: a registry that moves its token service refuses the first pre-emptive
token, and the `401`'s own challenge is followed (one handshake, a different key) and remembered once its token is accepted; a token
service that is gone fails the pre-emptive step, which forgets the challenge and sends the request without a token, so the `401` runs
the normal handshake. A challenge is remembered only from the upstream's origin (not from a redirect target such as a blob host) and
only after the token it led to was accepted, and only a request to the origin is sent with a pre-emptive token. It is kept per
configuration version (a changed URL or credential asks again), for 5 minutes, and is bounded at 8 KiB; 5 minutes bound what a wrong
remembered challenge can cost even if everything else fails, and a registry that is used keeps relearning it. A `401` that stands (a refused fresh token with the same key) forgets the remembered challenge too, which only costs the next request one challenged request. The development agent would revisit it for a registry that serves some paths anonymously and some with a token (the
pre-emptive token request is then wasted for the anonymous ones), or if a registry rejects a token sent with a request that was not
challenged.

**Applied in the spec:** [R-OCI-021](#r-oci-021--remote-repository-upstream-and-its-token-handshake), R-REPO-033, R-ARCH-008 and R-ARCH-032 (the cache of challenges) in
[architecture.md](../architecture.md). Code: `Engine.preemptive`, `Engine.token`, `Engine.authenticate` in
[authenticator.go](../../../internal/proxy/authenticator.go). Tests: `TestR_REPO_033_SecondRequestOfAColdPullCarriesItsTokenWithoutAChallenge`,
`TestR_REPO_033_ARememberedChallengeBelongsToItsConfigurationVersion`, `TestR_REPO_033_RejectedPreemptiveTokenIsReplacedOnce`.

## What this does not cover

### H-579 · A holder of the stored-secret key ring can mint registry tokens

The registry token service (R-OCI-005, built in T-16) derives its MAC key from the stored-secret key ring of R-ARCH-021. Whoever
holds a key file of the ring can mint registry tokens at will, for any repository a principal could be granted, while the named trove
token is live, without trove recording a token request; each minted token verifies for at most `oci.token_lifetime` and is refused
when its issue time lies more than a minute ahead of the verifying replica's clock. The decision point still decides each request and
verification still reads the state of the named authenticating token through the token cache, so a forged token cannot exceed the
principal's own grants and must name a live trove token by the SHA-256 of its secret (Q-OCI-50): the forger needs that hash too, from
the database (`api_token.secret_sha256`) or from a registry token issued for it, and the token's ID alone is not enough. Live once the
registry root mounts `/v2/` (the token service and the verification are built); real for an operator or attacker with read access to
the key ring Secret and to one of those hashes, who already opens every sealed upstream credential with the ring.

### H-580 · Untagged manifests accumulate until a retention policy removes them

A tag moved by a push leaves the earlier manifest stored (R-OCI-018), which keeps digest pulls of running workloads working; nothing
removes such manifests until the retention policies of R-STOR-037 to R-STOR-039 exist for the format. Live once the format is built;
real for repositories that CI pushes the same tag to many times a day, whose storage grows by every build.

### H-600 · The sizes a manifest declares are not compared with the stored blobs

A descriptor's `size` is the pusher's claim. The registry checks that the blob of the digest exists under the name (R-OCI-013 step 4) and
not that the stored blob has the declared size, so a manifest can name a layer with a size that is not its own. Pulls are unaffected (a
client verifies the digest it asked for, and the response carries the real `Content-Length`); the summed size of the package screen
(R-OCI-031, `oci_manifests.content_size`) is the sum of the declared sizes. Live today; real for a pusher that wants a misleading size on
the screen, who can already name any tag and annotation.

### H-601 · Blob downloads are streamed and never redirected to the object store

R-OCI-010 says that a repository with presigned downloads answers `307` for a blob `GET`. The presigned redirect of R-STOR-014 is P2 and
the download path of the core does not issue it yet, so every blob goes through the replica. Live today; real for large layers, which
occupy a replica's connection and bandwidth for the whole transfer.

### H-602 · The first push of a name by digest can claim the name for a caller that never pushes a tag

A push by digest creates the package row and, under the ownership rule of R-AUTHZ-018, claims an unclaimed name for the pusher (Q-OCI-63).
A holder of `deploy` on a repository whose `packageOwnership` is enforced can therefore claim names by pushing a manifest by digest and
never a tag, which leaves a package without versions that only a holder of `manage` releases. The same caller can claim a name with a
first tag, so the new path adds no standing the caller did not have; it adds a name that the package screens may list as empty (not
verified: how the package list treats a package without a live version). Live today; real where `deploy` is granted widely and names are
scarce.

### H-604 · The shadowing check is asked once per push until the name has a tag

The shadowing check of R-REPO-048 runs while the name has no live version (Q-OCI-63), through the virtual repositories that contain the
repository. A multi-platform image pushes its children by digest before its index and tag exist, so each child push of a fresh name asks
the resolver again until the first tag exists. The answer is correct each time; the cost is one resolution per push of the first
manifests of a name. Live today; real for multi-platform builds into a fresh name behind a virtual repository with remote members.

### H-608 · Pushes and removals take their locks in different orders and rely on the retry

A push locks the item of its path before the hook takes the package row; the removals of R-OCI-018 and the sweep of R-OCI-019 take the
package row first, before they delete the rows of the items (`LockRemoval`, Q-OCI-61). A push that moves a tag the same moment the tag is
deleted, and a tag push and a digest push of the same new manifest, can therefore form a cycle. The database detects it (40P01), the
transaction runner retries the loser, and the tests (the concurrent pushes of one new manifest by tag and by digest) end 201 for every
push; a retry that runs out answers 503 and the client pushes again. A removal of blobs takes no package row (a name that holds only
blobs has none): the rows of the blobs arbitrate. The delete job meets the same cycle from the other side: in a batch that holds only blobs, the manifests in the job's scope are removed through the cascade, and their `LockRemoval` takes the package row after the blob rows are locked, while a push takes the package row first and the rows after; the database ends that cycle too and the runner retries. Live today; real for CI that pushes the same new image by two routes at once. Not
measured: how often a retry happens under load.

### H-609 · The cost of a push is bounded by the size of the manifest, not by admission control

A manifest within the limits costs memory to read: about 100,000 keys in 1.3 MB allocate 27 to 32 MiB and 64 to 124 ms, so some sixteen
of them at once are about 512 MiB. The replica bounds a request body and the number of connections; whether its admission control
bounds the pushes in flight is not verified. Live today; real where many clients push manifests at the limit at once.

### H-642 · A pull by digest names no version, so the exposure rules that narrow by tag would not apply to it

A pull by digest reads `<name>/_manifests/<digest>`, which names no tag, so an exposure rule that narrows by version or tag would not
hide it. The format has no exposure adapter, so no rule exists and an endpoint with `exposure: rules` does not serve the format
(Deferred, Q-OCI-23). The tags list decides each tag by its path and the name, as a pull by tag does (a grant that excludes a tag path hides
it from both), so the two stay consistent. Dormant until the format gets an exposure adapter; real the day an adapter filters tags and
not digests.

### H-645 · A list with more than 10,000 consecutive tags the caller may not read is refused

The tags list reads on until it has found `n` tags the caller may read, and a page that cannot go on without a Link at a tag the caller may not
read would name it. A request that finds no readable tag within 10,000 tags (the referrers answer: 4,000 rows) is therefore refused with
`400 UNSUPPORTED`, and a caller whose grant hides a run of more than that many consecutive tags cannot list past it. Dormant until a grant
hides that many tags of one image name; real for a name with tens of thousands of tags and a path-scoped grant that hides most of them. An
opaque, sealed cursor would close it and needs a new Host method and a key derivation from the key ring.

### H-653 · The cascade of a retention run is not held to a subject's grants

The cascade of a hook is held to the grants of the caller for a removal a request makes (the registry routes and `/files/`) and for the
delete job that finishes a large `/files/` delete: the job carries the encoded subject of the request and decides every path a hook
cascades to in a batch for `delete`, on the authorization index installed at that moment; the first refusal fails the batch and the job with
the 403 of the path. What remains is a removal that has no subject: a retention run or a purge removes by policy, and the cascade of
the OCI hook (a removed manifest takes its tags and referrers) is not decided for anyone. Dormant: no retention run reaches the OCI
hook today, and when one does it acts as the system, not as a caller whose grant could be exceeded. That the delete job decides on the
subject's groups, scope and CI grants as of the request (a withdrawn membership, a revoked token or a disabled principal does not narrow
a running job) is a finding of the items-delete job for every format, F-84 in the project plan, not part of this gap.
