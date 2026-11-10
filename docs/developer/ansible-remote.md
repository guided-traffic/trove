# Ansible Galaxy remote repositories

The remote class, the roles, the mirror and the hub exchange of the format `ansible` ([R-ANS-016 to R-ANS-021](../spec/formats/ansible.md#r-ans-016--remote-repository-upstream-and-discovery)).
This page grows with the packages that build them; today it holds the counting stub, which every test of the format uses as its
upstream.

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
