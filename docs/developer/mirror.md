# Mirror mode of remote repositories

Status: phase 6b, specified, not scheduled; the role-neutral contracts are laid, the four work packages are pending. Specified on
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
| No lister in npm or NuGet yet: neither remote adapter implements `discover.Lister`, so the catalog fact `Mirror` is false and `mode: mirror` is refused with `not_applicable` (R-REPO-050) until the engine exists | `internal/formats/npm/mirror.go`, `internal/formats/nuget/mirror.go` (to be created) | A and B create them together with C |
| The typed rate-limit outcome `RateLimit`, `RateLimitOf`, `RateLimitError` | [internal/proxy/upstream/ratelimit.go](../../internal/proxy/upstream/ratelimit.go) | C |
| The fetch priority `proxy.Priority`, `Request.Priority`, `BackgroundShare`, the background slots (`acquireBackground`, no pre-emption yet) | [internal/proxy/types.go](../../internal/proxy/types.go), [slots.go](../../internal/proxy/slots.go), [fetch.go](../../internal/proxy/fetch.go) (`acquireSlot`) | C |
| `remote.mode`, `remote.mirror` with defaults, validation, the bound rule, the delegate rule, the read-back of an earlier stored section; the codes `mirror_unbounded`, `mirror_restricted` | [internal/repository/remote.go](../../internal/repository/remote.go), [validate_mirror.go](../../internal/repository/validate_mirror.go), [delegated.go](../../internal/repository/delegated.go), [internal/errs/codes.go](../../internal/errs/codes.go) | shared; C and D read, nobody changes without the spec |
| OpenAPI: `RemoteSettings.mode`, `RemoteSettings.mirror`, `MirrorSettings` | [api/openapi.yaml](../../api/openapi.yaml), the generated `web/src/app/api/generated/schema.d.ts` | D |
| The names `mirror.KindPass`, `mirror.KindDispatch`, `mirror.QueueMirror`, `mirror.PassKey`, `mirror.PassArgs`; the kinds registered with a handler that does nothing; the queue's slots `jobs.workers.mirror: 2` | [internal/mirror/mirror.go](../../internal/mirror/mirror.go), [internal/app/wire_mirror.go](../../internal/app/wire_mirror.go), [internal/config/jobs.go](../../internal/config/jobs.go) | C (D reads the names) |

## The work packages

| | Owns | Builds | Must not |
|---|---|---|---|
| **A · npm discovery** | `internal/formats/npm/mirror*.go` | `Lister` on npm's remote adapter: `feed` on the public registry through the replication host (`GET /` for `update_seq`, `registry/_all_docs` key ranges per include prefix with `limit=10000`, `registry/_changes?since=&limit=10000`, deleted changes as `Removed`), `names` elsewhere, the plan from the full document (semver order, prerelease by the pre-release part, `latest` pinned, removed versions left out), `MirrorHosts` = `replicate.npmjs.com` for the public registry only | import `internal/mirror` or `internal/proxy` (archtest), send a credential to the replication host |
| **B · NuGet discovery** | `internal/formats/nuget/mirror*.go` | `Lister` on NuGet's remote adapter: `feed` from `Catalog/3.0.0` of the service index (pages in `commitTimeStamp` order, the cursor of the last commit queued whole, `PackageDelete` as `Removed`), `listing` from the search (`take=1000`, the truncation note), `names`; the plan from the version list and the registration, `.nupkg` per version, unlisted ones included | as A |
| **C · engine** | `internal/mirror` (the engine), the migration of `mirror_state` and `mirror_pending` (R-REPO-053's data model) with their export classes, `internal/proxy` (pre-emption, background rules: no circuit move, no probe, `RateLimitOf` for background answers, `flight_role: mirror`), `internal/formathost` (the pass's `discover.Upstream` and the artifact fill through the format's gate), `internal/app/wire_mirror.go` (the handlers, the dispatch task), the engine's queries, the metrics, the signal area `mirror_yield`, the pass events | dispatch, pass, fence, window, waits, bounds, pre-emption, gauges | change the client path's behaviour of R-REPO-040 and R-REPO-029; bind a pass to a Deployment rather than to its queue |
| **D · API and UI** | `internal/api/v1/mirror_api.go`, the status and run operations and their schemas in the OpenAPI document, the format descriptor's `mirror`, the API's queries, `mirror.pass_requested`, `web/` (the mirror section of the remote form, the Mirror tab with pause, resume and run), the manual | status and run with their permissions | write the engine's columns other than `pass_requested_*` |

Dependencies: A, B and D's configuration and UI parts start from the contract wave; C starts when the owner has settled the
process topology and adds the tables; D's operations follow C's tables. C tests with a fake `Lister` until A and B land. The e2e
set needs all four.

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
