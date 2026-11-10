# Ansible Galaxy remote repositories

The remote class, the roles, the mirror and the hub exchange of the format `ansible` ([R-ANS-016 to R-ANS-021](../spec/formats/ansible.md#r-ans-016--remote-repository-upstream-and-discovery)),
and the counting stub every test of the format uses as its upstream. The security view is [security/ansible-remote.md](../security/ansible-remote.md); the format as a
whole is [ansible.md](ansible.md).

## What has to be in your head first

- **The engine does the I/O, the format decides what a document is.** The remote engine (`internal/proxy`) owns the fetch, the freshness, the
  collapsing across replicas, the negative entries and the circuit. The format adds where a document lives (`UpstreamRequest`), what it must be
  before it is stored (`PrepareFill`), what is recorded when it is stored (`OnFill`) and how it is rendered (`handlers_remote.go`, `Rewrite`).
- **Stored bytes are the upstream's, a response is a rendering.** `Rewrite` and the handlers put the repository's addresses into the documents
  ([galaxydoc](../../internal/formats/ansible/galaxydoc/galaxydoc.go)) and leave the plugin listing out; nothing the upstream sent as an address
  reaches a client.
- **Every document is a canonical path with a variant** ([paths.go](../../internal/formats/ansible/paths.go)):

| Document | Path | Variant | Upstream address (below `remote.url` and the prefix of the API root) |
|---|---|---|---|
| API root (discovery) | `api-root.json` | `doc` | `remote.url` itself |
| Collection | `collections/<ns>/<name>/collection.json` | `doc` | `<v3>collections/<ns>/<name>/` |
| Version list, one page | `collections/<ns>/<name>/versions.json` | `offset=<n>` | `<v3>collections/<ns>/<name>/versions/?limit=100&offset=<n>` |
| Version | `collections/<ns>/<name>/<version>.json` | `doc` | `<v3>collections/<ns>/<name>/versions/<version>/` |
| Collection archive | `collections/<ns>/<name>/<ns>-<name>-<version>.tar.gz` | none | the `download_url` of the version document (URL map) |
| Role lookup | `roles/lookup.json` | `owner=<o>&name=<n>` | `<v1>roles/?owner__username=<o>&name=<n>` |
| A role's versions, one page | `roles/<id>/versions.json` | `page=<n>` | `<v1>roles/<id>/versions/?page_size=50&page=<n>` |
| Role archive | `roles/<id>/<ref>.tar.gz` | none | the `download_url` of the version entry (URL map) |

- **The package of a role is `role/<id>`** ([Q-ANS-36](../spec/formats/ansible.md#q-ans-36--a-role-is-a-package-of-the-cores-name-machinery-named-role-and-its-id)): `Classify`,
  `PackageDocumentPath` and `Names.Normalize` know it, which is what lets the URL map record the archive a version page names.
- **One request per document.** However many clients and replicas ask, the engine fetches a document once while it is fresh and once per period
  after; the collapsing across replicas needs Valkey (the integration test starts the stack with it).

## The files

| File | Responsibility |
|---|---|
| [`handlers_remote.go`](../../internal/formats/ansible/handlers_remote.go) | the registration of the class (`registerClass(formats.Remote, ...)`), the eight handlers of its routes, the validators and replies |
| [`remote.go`](../../internal/formats/ansible/remote.go) | the adapter: `AcceptResponse` (the role archive bound), `ExpectsType`, `PrepareFill`, `OnFill`, `Rewrite`; the checks of a collection and a version document |
| [`remote_discovery.go`](../../internal/formats/ansible/remote_discovery.go) | `Discoverer` (the API root, its prefixes) and `UpstreamRequest` |
| [`remote_target.go`](../../internal/formats/ansible/remote_target.go) | a canonical path taken apart, the variants and their grammar, the paging bounds |
| [`remote_docs.go`](../../internal/formats/ansible/remote_docs.go) | the upstream collection and page documents, the role documents and their rendering, the host rules |
| [`remote_gate.go`](../../internal/formats/ansible/remote_gate.go) | `BeforeArtifact`: the recorded digest of a version, the revalidation of a document that was never stored |
| [`remote_fill.go`](../../internal/formats/ansible/remote_fill.go) | `FillInspector`: the identity of a collection archive, the shape of a role archive |
| [`mirror.go`](../../internal/formats/ansible/mirror.go) | `discover.Lister`: the methods, the index listing by offset, the exact names, the plan of a collection |
| [`hub.go`](../../internal/formats/ansible/hub.go) | `formats.CredentialExchanger`: the refresh-token grant at `ansible.authUrl` and the reading of its answer |
| [`internal/repository/validate_ansible.go`](../../internal/repository/validate_ansible.go) | the rules of `ansible.authUrl` when a document is saved |
| [`internal/proxy/credential_exchange.go`](../../internal/proxy/credential_exchange.go) | the engine's side of the exchange |
| [`remote_bind.go`](../../internal/formats/ansible/remote_bind.go) | `Bind`: the replica's configuration and metrics (`trove_ansible_role_versions_dropped_total`, `trove_ansible_document_bytes`) |
| [`internal/formatdata/ansible/remote_store.go`](../../internal/formatdata/ansible/remote_store.go) | `RecordAnnounced`, `Announced`, `AnnouncedNames` on `ansible_upstream_versions` (migration 00070, [ansible_remote.sql](../../internal/meta/postgres/queries/ansible_remote.sql)) |
| [`remote_tables.go`](../../internal/formatdata/ansible/remote_tables.go), [`remote_purge.go`](../../internal/formatdata/ansible/remote_purge.go), [`internal/backup/fixup/ansible_remote.go`](../../internal/backup/fixup/ansible_remote.go) | the table's export class and the purge of a restore without remote caches |
| [`test/integration/ansibleremote`](../../test/integration/ansibleremote) | the area: one request per document, paging, rewriting, digests, roles, credentials |

## The flows

**A version list.** The handler reads page 0 (`offset=0`) for the count and the page size, then the stored pages that cover the requested range,
cuts the range and renders `meta.count` and the links at the repository's address ([Q-ANS-12](../spec/formats/ansible.md#q-ans-12--a-remote-stores-the-version-list-in-pages-of-100-and-cuts-the-clients-page-when-it-serves),
[Q-ANS-39](../spec/formats/ansible.md#q-ans-39--a-remote-reads-the-version-list-at-the-upstreams-page-size-starting-with-the-first-page)). The entries are in the upstream's order.

**A collection archive.** The handler authorizes the package and the version, then `Host.Remote().Artifact` runs the gate: the recorded digest of
the version (a document that was never stored is fetched first), the engine fetches the archive from the URL-map address under the host rule,
verifies it against the digest, the fill hook checks its identity, and the item is stored. The first request of a version thus costs two upstream
requests (document, archive) and every later request none.

**A role.** Lookup, version pages and archives follow the table above; the version page's `PrepareFill` records an archive address for each version
that passes the host rule and counts the others. The archive has no digest; the hook checks that it is a gzip-compressed tar archive within bounds.

**The mirror** ([R-ANS-021](../spec/formats/ansible.md#r-ans-021--remote-repository-mirror-mode)). `MirrorNames` with the method `listing` reads the collection index through `Upstream.Get`,
100 entries at a time, and returns the names the scope admits in the index's order; the cursor is `{"offset": n}`, the position after the last
entry consumed, so a page the engine's limit cut resumes inside an upstream page. The method `names` returns the include patterns without a
wildcard that read as `<ns>.<name>`. `MirrorPlan` reads the version-list pages and, newest first, the documents of the versions the bounds
keep (`discover.BoundsOf`), through `Upstream.Document`: each document is an ordinary fill, so the digest of every planned version is recorded
before the engine fetches its archive through the gate. Roles and the role index are not mirrored. The role of the engine is
[mirror.md](mirror.md); the area is `test/integration/mirror/ansible_test.go`.

**The hub exchange** ([R-ANS-019](../spec/formats/ansible.md#r-ans-019--remote-repository-a-hub-behind-single-sign-on)). A hub repository has `remote.authMode: bearer` (the offline token is the stored secret) and
`ansible.authUrl`. `send` of the engine asks the adapter's `ExchangeRequest` for a request to the realm, checks that the realm's host may
receive the credential, exchanges (one flight per replica and token, cached until 30 seconds before the realm's `expires_in`) and sends the
upstream request with `Authorization: Bearer <access token>` in place of the credential; a 401 of the upstream exchanges again once. The
stub's realm ([realm.go](../../test/ansiblestub/realm.go)) issues the tokens and counts what the upstream accepted and rejected, so a test
shows that the offline token never reached the upstream (`Rejected == 0`).

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | `internal/formats/ansible/remote*_test.go` | the API root, the upstream addresses, the checks of a version document, the role rendering and its host rule, the role archive bound, the fill hook |
| Kit | `conformance_test.go`, `remote_fixture_test.go` | `Rewrite` (deterministic, idempotent, no upstream address left), the discovery documents of check 6, the name `role/16` |
| Integration | `test/integration/ansibleremote`, `test/integration/mirror/ansible_test.go` | against the stub through the slot wrapper (`hub_test.go` for the realm); the role routes of the kit's content check are listed in `NoContent` because the kit's host is a local repository |

## The counting stub

`test/ansiblestub` is a stub Galaxy in the style of [`test/debstub`](../../test/debstub) and `test/npmstub`: a Go package for
in-process tests and a program and container for the e2e set. It serves a corpus generated in code, counts every request and fails on
command. It answers over TLS only ([R-ANS-032](../spec/formats/ansible.md#r-ans-032--test-suites)).

| File | Responsibility |
|---|---|
| [`stub.go`](../../test/ansiblestub/stub.go) | `Config`, `Server`, `New`, resets, `AddVersion`/`DropVersion`, `Corpus`, which archive a mode serves |
| [`corpus.go`](../../test/ansiblestub/corpus.go) | the collections and roles, the archive cache |
| [`route.go`](../../test/ansiblestub/route.go) | host dispatch (five hosts), the path grammar of the Galaxy host |
| [`galaxy.go`](../../test/ansiblestub/galaxy.go), [`roles.go`](../../test/ansiblestub/roles.go) | the v3 and v1 documents |
| [`serve.go`](../../test/ansiblestub/serve.go) | the handler, the counters, the gate of the switches, archives |
| [`mode.go`](../../test/ansiblestub/mode.go), [`throttle.go`](../../test/ansiblestub/throttle.go), [`realm.go`](../../test/ansiblestub/realm.go) | the switches, the rate limit, the hub's realm |
| [`counts.go`](../../test/ansiblestub/counts.go), [`inflight.go`](../../test/ansiblestub/inflight.go), [`control.go`](../../test/ansiblestub/control.go) | counters, open requests, the control API |
| [`ansiblegen/`](../../test/ansiblestub/ansiblegen) | collection archives (with `MANIFEST.json` and `FILES.json`) and role archives, byte-for-byte deterministic |
| [`cmd/ansiblestub`](../../test/ansiblestub/cmd/ansiblestub), [`Containerfile`](../../test/ansiblestub/Containerfile) | the program and its image (`make ansible-stub-image`) |
| [`deploy/dev/ansiblestub.yaml`](../../deploy/dev/ansiblestub.yaml) | the manifest of the e2e harness (wired by the `ansible` set, `TROVE_E2E_ANSIBLESTUB=1`) |

### What it serves

One listener, five host names told apart by the `Host` header. The Galaxy is every name that is none of the other four.

| Host (default name) | Serves |
|---|---|
| Galaxy (`galaxy.localhost` on loopback) | `/api/` (`{"available_versions":{"v3":"v3/","v1":"v1/"}}`), the collection index, collection, version list and version documents below `/api/v3/plugin/ansible/content/published/collections/index/` (and the short `/api/v3/collections/`), archives below `.../artifacts/`, the signed address `/api/pulp/content/published/<file>`, the v1 role API |
| GitHub stand-in (`github.localhost`) | `/<user>/<repo>/archive/<tag>.tar.gz`, a 302 to the codeload host |
| codeload (`codeload.localhost`) | `/<user>/<repo>/tar.gz/<tag>` |
| unlisted (`unlisted.localhost`) | the same archive path, served directly; one role version points here |
| realm (`sso.localhost`) | the token endpoint `/auth/realms/test/protocol/openid-connect/token` (`grant_type=refresh_token`) |

The documents have the shapes recorded against the public Galaxy on 2026-11-10 ([the verified facts](../spec/formats/ansible.md#client-and-library-behaviour-this-page-relies-on)):
links start with `/`, `download_url` is absolute on the request's host, a list is newest first by semantic version with `meta.count`
and `links.{first,previous,next,last}`, `limit` is clamped to 1..100, a missing object is 404 `{"errors":[{"status":"404","code":"not_found",...}]}`
on v3 and `{"detail":"Not found."}` on v1. The version document keeps `metadata.contents`, `docs_blob`, `manifest` and `files`, so a
remote's rewriting has something to drop. A v1 list pages by `page` and `page_size` and links by `next_link`.

The corpus (`GET /-/stub/corpus` lists it with the digests):

| Collection | Versions | Used for |
|---|---|---|
| `acme.base` | 1.0.0, 1.2.0 | the dependency of `acme.tools` |
| `acme.tools` | 1.0.0, 1.1.0, 2.0.0 (`requires_ansible >=2.15.0`, depends on `acme.base`), 2.1.0-rc.1 | order by semantic version, pre-releases, the mirror's `acme.*` |
| `acme.signed` | 1.0.0 with one opaque signature | signatures pass through |
| `other.stuff` | 1.0.0 | a collection the mirror pattern `acme.*` must not take |
| `many.versions` | 1.0.0 to 1.249.0 (250) | three list pages |
| `filler.c0000`, ... | 1.0.0 each, `Config.Fillers` of them (default none) | an index longer than a page |

| Role | Tags | Used for |
|---|---|---|
| `geerlingguy.pip` (id 16) | 1.0.0, 1.1.0, 2.0.0 | the install flow through the GitHub redirect |
| `acme.rolex` (id 17) | 0.9.0 on the unlisted host, 1.0.0 on GitHub | a version a remote must drop |
| `many.tags` (id 18) | 1.0.0 to 1.59.0 (60) | two pages of versions at `page_size=50` |

### The switches

Every switch is a Go method and a control request. A mode replaces the previous one; `{}` clears it. `POST /-/stub/reset?all=true`
returns the whole stub to its start.

| Spec name | Switch | Effect |
|---|---|---|
| `gzip` | `Mode.Gzip` | JSON documents are compressed for a client that sends `Accept-Encoding: gzip` |
| `redirect` | `Mode.Redirect` | the short path 302s to the long one, an archive 302s to `/api/pulp/content/published/<file>?expires=...&validate_token=...` (403 without the token), as the public Galaxy |
| `ratelimit` | `SetThrottle` | 429/503/quota answers with `Retry-After`, per class, for the n-th request or a period (the shape of the other stubs) |
| `slow` | `Mode.DelayMS` | delay by class (`all` for every request) |
| `reject_token` | `Mode.RejectToken` | every Galaxy request is 401 in the Galaxy shape |
| `digest_changed` | `Mode.DigestChanged` | the listed collections ("ns.name" or "all") have a second archive per version, announced with its own SHA-256 and size |
| `wrong_digest` | `Mode.WrongDigest` | the second archive is served, the document keeps the first digest; wins over `digest_changed` |
| — | `Mode.WrongIdentity` | the archive of another collection (`swapped.other`, the same version) is served and announced with its own digest: the digest verifies, only the identity inside the archive is wrong; wins over the digest modes |
| — | `Mode.NoV1`, `Down`, `FailStatus` | a hub without `v1`; 503, reset or hang; a 5xx per class |
| the hub's realm | `SetRealm` | `Require` makes the Galaxy accept only `Bearer` tokens the realm issued, not expired and not revoked; `RefreshTokens` are the offline tokens the realm takes, `ClientID` the client it requires; `ExpireAccessTokens` revokes what was issued |
| — | `AddVersion`, `DropVersion` | the upstream publishes or removes a version (a dropped one is 404 everywhere) |

The classes of request are `root`, `index`, `collection`, `versions`, `version`, `artifact`, `role`, `role-archive` (all three role
hosts) and `realm`.

### What it counts

`Counts()` has one entry per method, host, path, raw query, conditionality and authorization scheme, with the answers by status, so a
test can say "one request per document" (`Requests`, `Query`, `OfKind`, `Delivered`) and "the token went only to the upstream host"
(`WithAuth(host, "Bearer")`, `OnHost`). The realm's own counters (`RealmState`) hold the exchanges, the refused grants, the accepted and
rejected Galaxy requests and the bearer tokens that were refused. The control API is not counted.

### Driving it

In a Go test:

```go
stub, _ := ansiblestub.New(ansiblestub.Config{Fillers: 150})                 // # example
ss, _ := ansiblestub.NewSelfSigned(ansiblestub.LoopbackHosts...)
run, _ := stub.Start("127.0.0.1:0", ss.ServerConfig())
defer run.Close()
// run.APIRoot() is the remote's upstream URL; run.DialOverride() is the map for the dial seam
// (app.Options.DialOverride), run.GitHubURL and run.CodeloadURL are the hosts to allow in remote.extraHosts.
```

As a process: `go run ./test/ansiblestub/cmd/ansiblestub serve -self-signed galaxy.localhost,github.localhost,codeload.localhost,unlisted.localhost,sso.localhost,localhost,127.0.0.1 -ca-out /tmp/ca.crt -control-token-file token`.
As a container: `make ansible-stub-image`, then [`deploy/dev/ansiblestub.yaml`](../../deploy/dev/ansiblestub.yaml) (five Services on one pod, a control token in
Secret `trove-ansiblestub`). The control API is `/-/stub/` with `Authorization: Bearer <token>`; the list of its requests is in
[`control.go`](../../test/ansiblestub/control.go).

The stub generates and compares versions with its own code (`semver.go`, `ansiblegen`), independent of trove's packages, so a defect in
trove's version order or archive reader does not hide in the test upstream.
