# Mirror mode of remote repositories

Status: phase 6b, specified, not scheduled; the role-neutral contracts are laid, work package A (npm discovery) is built, B, C and D are pending. Specified on
[repositories-and-proxy.md](../spec/repositories-and-proxy.md#mirror-mode) (R-REPO-050 to R-REPO-059) and the format pages'
sections "Mirror discovery" ([npm](../spec/formats/npm.md#mirror-discovery), [NuGet](../spec/formats/nuget.md#mirror-discovery));
the cut into work packages is [Q-ARCH-288](../spec/architecture.md#q-arch-288--the-seams-of-the-mirror).

A remote repository runs in mode `pullThrough` (the default) or `mirror`. A mirror adds background passes, jobs of the job
framework on their own queue `mirror`, run by whatever process claims that queue: the format discovers names at the upstream,
the engine keeps them in a pending table and fills each package through the remote engine with the background priority, so
every rule of a client's fill applies. The pass honours rate limits, halves its window on them, never moves the clients'
circuit and yields to client fetches.

## What the contract wave laid

Only pieces that bind no process topology and are cheap to change (the owner is reconsidering the topology, 2026-11-04). The
tables, the operations, the audit types and the signal area are specified and are added by the work packages that build them.

| Piece | Where | Who changes it from now on |
|---|---|---|
| The discovery contract: `Lister` (`MirrorMethods`, `MirrorNames`, `MirrorPlan`, `MirrorHosts`), `Upstream` (`Get`, `Document`), `PageRequest`, `Page`, `Plan`, `Item`, `Cursor`, `Scope`, `RateLimited`, `AsRateLimited`, `ErrMethodUnavailable`, `LiteralPrefix`, `Bounding` | [internal/mirror/discover](../../internal/mirror/discover/discover.go) (leaf) | shared; a change is an addition to the frozen contract (ADR-152) and a note to A, B and C |
| The format contract's alias `formats.MirrorLister`, `RemoteCaps.Mirror`, the catalog fact `Entry.Mirror` | [internal/formats/remote.go](../../internal/formats/remote.go), [formats.go](../../internal/formats/formats.go), [catalog.go](../../internal/formats/catalog/catalog.go), [testdata/contract.txt](../../internal/formats/testdata/contract.txt) | shared (ADR-152) |
| npm's remote adapter implements `discover.Lister`, so the catalog fact `Mirror` is true for npm and `mode: mirror` is accepted for it (R-REPO-050); NuGet has no lister yet, so its `mode: mirror` is refused with `not_applicable`. Until the engine exists an accepted npm mirror starts no pass | [internal/formats/npm/mirror.go](../../internal/formats/npm/mirror.go) (A, built), `internal/formats/nuget/mirror.go` (B, to be created) | A and B own their formats; C builds the engine |
| The typed rate-limit outcome `RateLimit`, `RateLimitOf`, `RateLimitError` | [internal/proxy/upstream/ratelimit.go](../../internal/proxy/upstream/ratelimit.go) | C |
| The fetch priority `proxy.Priority`, `Request.Priority`, `BackgroundShare`, the background slots (`acquireBackground`, no pre-emption yet) | [internal/proxy/types.go](../../internal/proxy/types.go), [slots.go](../../internal/proxy/slots.go), [fetch.go](../../internal/proxy/fetch.go) (`acquireSlot`) | C |
| `remote.mode`, `remote.mirror` with defaults, validation, the bound rule, the delegate rule, the read-back of an earlier stored section; the codes `mirror_unbounded`, `mirror_restricted` | [internal/repository/remote.go](../../internal/repository/remote.go), [validate_mirror.go](../../internal/repository/validate_mirror.go), [delegated.go](../../internal/repository/delegated.go), [internal/errs/codes.go](../../internal/errs/codes.go) | shared; C and D read, nobody changes without the spec |
| OpenAPI: `RemoteSettings.mode`, `RemoteSettings.mirror`, `MirrorSettings` | [api/openapi.yaml](../../api/openapi.yaml), the generated `web/src/app/api/generated/schema.d.ts` | D |
| The names `mirror.KindPass`, `mirror.KindDispatch`, `mirror.QueueMirror`, `mirror.PassKey`, `mirror.PassArgs`; the kinds registered with a handler that does nothing; the queue's slots `jobs.workers.mirror: 2` | [internal/mirror/mirror.go](../../internal/mirror/mirror.go), [internal/app/wire_mirror.go](../../internal/app/wire_mirror.go), [internal/config/jobs.go](../../internal/config/jobs.go) | C (D reads the names) |

## The work packages

| | Owns | Builds | Must not |
|---|---|---|---|
| **A · npm discovery** (built, [below](#work-package-a-npm-discovery)) | `internal/formats/npm/mirror*.go`, the npm part of `test/npmstub` | `Lister` on npm's remote adapter: `feed` on the public registry through the replication host (`GET /` for `update_seq`, `registry/_all_docs` key ranges per include prefix with `limit=10000`, `registry/_changes?since=&limit=10000`, deleted changes as `Removed`), `names` elsewhere, the plan from the full document (semver order, prerelease by the pre-release part, `latest` pinned, removed versions left out), `MirrorHosts` = `replicate.npmjs.com` for the public registry only | import `internal/mirror` or `internal/proxy` (archtest), send a credential to the replication host |
| **B · NuGet discovery** | `internal/formats/nuget/mirror*.go` | `Lister` on NuGet's remote adapter: `feed` from `Catalog/3.0.0` of the service index (pages in `commitTimeStamp` order, the cursor of the last commit queued whole, `PackageDelete` as `Removed`), `listing` from the search (`take=1000`, the truncation note), `names`; the plan from the version list and the registration, `.nupkg` per version, unlisted ones included | as A |
| **C · engine** | `internal/mirror` (the engine), the migration of `mirror_state` and `mirror_pending` (R-REPO-053's data model) with their export classes, `internal/proxy` (pre-emption, background rules: no circuit move, no probe, `RateLimitOf` for background answers, `flight_role: mirror`), `internal/formathost` (the pass's `discover.Upstream` and the artifact fill through the format's gate), `internal/app/wire_mirror.go` (the handlers, the dispatch task), the engine's queries, the metrics, the signal area `mirror_yield`, the pass events | dispatch, pass, fence, window, waits, bounds, pre-emption, gauges | change the client path's behaviour of R-REPO-040 and R-REPO-029; bind a pass to a Deployment rather than to its queue |
| **D · API and UI** | `internal/api/v1/mirror_api.go`, the status and run operations and their schemas in the OpenAPI document, the format descriptor's `mirror`, the API's queries, `mirror.pass_requested`, `web/` (the mirror section of the remote form, the Mirror tab with pause, resume and run), the manual | status and run with their permissions | write the engine's columns other than `pass_requested_*` |

Dependencies: A, B and D's configuration and UI parts start from the contract wave; C starts when the owner has settled the
process topology and adds the tables; D's operations follow C's tables. C tests with a fake `Lister` until A and B land. The e2e
set needs all four.

## Work package A: npm discovery

Built: the format's part of the mirror, with unit tests over a fake `discover.Upstream` and the counting stub's replication and
throttling paths. The engine, the integration criteria of [npm.md](../spec/formats/npm.md#mirror-discovery) and the e2e set are
C's and the e2e wave's. Decisions: Q-NPM-73 (the cursor of a full pass carries the sequence) and Q-NPM-74 (the plan's size and
planned versions) in [npm.md](../spec/formats/npm.md).

| File | Responsibility |
|---|---|
| [mirror.go](../../internal/formats/npm/mirror.go) | `MirrorMethods` (`feed` for `https://registry.npmjs.org/` only, `names` always, never `listing`), `MirrorHosts` (`replicate.npmjs.com` for the public registry only), `MirrorNames` (the dispatch by method, the method `names` over the include patterns without a wildcard), the cursor type, `mirrorAdmitted` (name rule of R-NPM-002 and `Scope.Admits` before any name is returned), `replicationGet` (a rate-limit error comes back as it came; 404, 410 and 501 end the method with `ErrMethodUnavailable`) |
| [mirror_feed.go](../../internal/formats/npm/mirror_feed.go) | the method `feed`: `keyRanges` (one range per literal prefix, a prefix inside another's range left out, the whole key space without a prefix), `feedFull` (the root's `update_seq` on the first page, one `registry/_all_docs` per page, `startkey` after the last id, `limit=10000`), `feedIncremental` (`registry/_changes?since=&limit=10000`, `deleted` to `Page.Removed`) |
| [mirror_plan.go](../../internal/formats/npm/mirror_plan.go) | `MirrorPlan`: the full document through `Upstream.Document`, versions newest first, tarball paths, `Prerelease`, `Pinned` for `latest`, the size from `_attachments` only |
| [mirror_test.go](../../internal/formats/npm/mirror_test.go) | the fake upstream and the tests: the exact query strings, paging, cursors, ranges, deletions, `names`, plan order and pins, a rate-limit error returned unchanged, admitted names only |

What a colleague needs to know:

- **The cursor of a full pass is `{"seq", "after", "range"}`** and the last page's is `{"seq"}`: `Page.Cursor` of the page with
  `Done` is the `Since` of the next incremental pass, as the contract says. The first page of an incremental pass has
  `PageRequest.Cursor` nil and `Since` set; the format reads the position from `Since` then and from the page's own cursor after.
- **A page holds at most `PageRequest.Limit` names** (10,000 when the engine sends 0): the format asks for 10,000 ids per request
  and, when it is asked for fewer, continues from the cursor of the last id it used; a page may hold no names and not be `Done`.
- **The first page of a full pass sends two requests** (`GET /` for `update_seq`, then the first `_all_docs`); a repeated call
  after a rate limit on the second sends the first again. Every later page is one request.
- **The format adds no Authorization header**: it has no way to, `Upstream.Get` takes a URL and an `Accept` only. Keeping the
  credential from `replicate.npmjs.com` is the engine's `Upstream`.
- **`Plan.Items[].Size` is 0 for the public registry**: its documents announce no tarball size (Q-NPM-74); `maxBytes` counts those
  artifacts after the fetch (R-REPO-052).
- **`mode: mirror` is accepted for npm now** and starts nothing until the engine exists. The existing contract tests that assert
  the refusal use fake formats (`internal/repository/mirror_test.go`, `npmlike`) and still hold; the npm contract is
  `TestMirrorListerIsRegisteredWithTheCatalog`.

The counting stub ([e2e.md](e2e.md#the-counting-npm-test-upstream), [test/npmstub](../../test/npmstub)): `replication.go` serves
`GET /`, `registry/_changes` and `registry/_all_docs` under a second host name (`Config.ReplicationHosts`, flag
`-replication-hosts`, default `replicate.npmjs.com`; the same listener and certificate, a request is a replication request by its
`Host` header), logs each request with the scheme of its credential (`Replications`, `GET /-/stub/replication`), and takes
changes (`Change`, `POST /-/stub/change`, `Publish`); `throttle.go` holds the throttling modes of R-REPO-059 on its own (one
`Throttle` in `Mode`, one `throttleState` in the `Server`, one gate call), so that `test/nugetstub` can take its shape over.

## Tests each package writes

- **A, B:** unit tests over a fake `discover.Upstream` (pages, cursors, ranges, deletions, the plan's order and pins, a rate-limit
  error returned unchanged); the format's part of the counting stub and its criteria in `test/integration/mirror`.
- **C:** unit tests with a fake clock and a fake lister (window, back-off with jitter, the long wait, the fence); in
  `test/integration/mirror` the counting stubs of R-REPO-059: `429` with `Retry-After` in seconds and as a date, `503` with
  `SlowDown` and without a hint, `403` with `X-RateLimit-Remaining: 0`, release and resume, lease loss, the bound, pre-emption
  in one process and across a client-serving process and one that claims the queue `mirror`; the e2e set `mirror`: a pass
  while `npm ci` and `dotnet restore` run through the same remotes (clients first), the drain of the node of the process that
  runs a pass, once with Valkey stopped.
- **D:** handler tests with permission-narrowed callers and a project administrator; `test/integration/mirrorapi` for the status
  and the run (202, 409 `mirror_pass_running`, `mirror_off`, `mirror_paused`); the browser journey of the Mirror tab with axe.

Every test names the requirement it checks (R-REPO-050 to R-REPO-059), so that the coverage generator counts it.

## Rules every package keeps

- A format sends no request of its own: every discovery request goes through `Upstream.Get` or `Upstream.Document`.
- A rate-limit answer is an error for which `discover.AsRateLimited` is true; whoever receives it returns it unchanged or wrapped.
- Every write of a pass in progress is conditional on the fence (`pass_job_id`, `pass_holder`); a write that matches no row stops
  the pass.
- No background outcome moves or resets the circuit of R-REPO-029; client requests keep its classification.
- The mirror deletes nothing (Q-REPO-144).
