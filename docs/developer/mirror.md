# Mirror mode of remote repositories

Status: phase 6b, in progress; the role-neutral contracts are laid, the tables and the seam to the remote engine are added,
and work packages A (npm discovery), B (NuGet discovery), C (C1, the remote engine's side: background requests, pre-emption, the
yield signal, `mirror.Host`; C2, the engine: dispatch, pass, fence, window, waits, bounds, metrics, events) and the API half of D
(the status and the run, [below](#the-status-and-run-operations-work-package-d)) are built; the Mirror tab of D and the integration
and e2e suites are in progress. Specified on
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
| The listers: npm's and NuGet's remote adapters implement `discover.Lister`, so the catalog fact `Mirror` is true for both and `mode: mirror` is accepted (R-REPO-050) | [internal/formats/npm/mirror.go](../../internal/formats/npm/mirror.go) (A, built), [internal/formats/nuget/mirror.go](../../internal/formats/nuget/mirror.go) and its siblings (B, built) | A and B own their formats; C builds the engine |
| The typed rate-limit outcome `RateLimit`, `RateLimitOf`, `RateLimitError` (returned by the background requests of the engine) | [internal/proxy/upstream/ratelimit.go](../../internal/proxy/upstream/ratelimit.go) | C |
| The fetch priority `proxy.Priority`, `Request.Priority`, `BackgroundShare`, the background slots, pre-emption and the yield (built by C1, see [The remote engine's side](#the-remote-engines-side-c1)) | [internal/proxy/types.go](../../internal/proxy/types.go), [slots.go](../../internal/proxy/slots.go), [fetch.go](../../internal/proxy/fetch.go) (`acquireSlot`) | C |
| `remote.mode`, `remote.mirror` with defaults, validation, the bound rule, the delegate rule, the read-back of an earlier stored section; the codes `mirror_unbounded`, `mirror_restricted` | [internal/repository/remote.go](../../internal/repository/remote.go), [validate_mirror.go](../../internal/repository/validate_mirror.go), [delegated.go](../../internal/repository/delegated.go), [internal/errs/codes.go](../../internal/errs/codes.go) | shared; C and D read, nobody changes without the spec |
| OpenAPI: `RemoteSettings.mode`, `RemoteSettings.mirror`, `MirrorSettings`, and from the API half of D the operations `getRepositoryMirror` and `runRepositoryMirror`, the schemas `MirrorStatus`, `MirrorPass`, `MirrorPassSummary`, `MirrorErrors`, `MirrorRateLimit`, `MirrorBound`, `MirrorMethod`, `MirrorBoundKind`, `MirrorRunRequest` and `FormatDescriptor.mirror` | [api/openapi.yaml](../../api/openapi.yaml), the generated `web/src/app/api/generated/schema.d.ts` | D |
| The names `mirror.KindPass`, `mirror.KindDispatch`, `mirror.QueueMirror`, `mirror.PassKey`, `mirror.PassArgs`; the kinds, now registered with the engine's handlers and the task `mirror-dispatch` ([wire_mirror.go](../../internal/app/wire_mirror.go)); the queue's slots `jobs.workers.mirror: 2` | [internal/mirror/mirror.go](../../internal/mirror/mirror.go), [internal/app/wire_mirror.go](../../internal/app/wire_mirror.go), [internal/config/jobs.go](../../internal/config/jobs.go) | C (D reads the names) |

## The work packages

| | Owns | Builds | Must not |
|---|---|---|---|
| **A · npm discovery** (built, [below](#work-package-a-npm-discovery)) | `internal/formats/npm/mirror*.go`, the npm part of `test/npmstub` | `Lister` on npm's remote adapter: `feed` on the public registry through the replication host (`GET /` for `update_seq`, `registry/_all_docs` key ranges per include prefix with `limit=10000`, `registry/_changes?since=&limit=10000`, deleted changes as `Removed`), `names` elsewhere, the plan from the full document (semver order, prerelease by the pre-release part, `latest` pinned, removed versions left out), `MirrorHosts` = `replicate.npmjs.com` for the public registry only | import `internal/mirror` or `internal/proxy` (archtest), send a credential to the replication host |
| **B · NuGet discovery** (built) | `internal/formats/nuget/mirror*.go`, the NuGet part of `test/nugetstub` | `Lister` on NuGet's remote adapter: `feed` from `Catalog/3.0.0` of the service index (pages in `commitTimeStamp` order, the cursor of the last commit queued whole, `PackageDelete` as `Removed`), `listing` from the search (`take=1000`, the truncation note), `names`; the plan from the version list and the registration, `.nupkg` per version, unlisted ones included | as A |
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

Two rules of the validation changed after the audit: a project administrator's update of a mirror that stays a mirror keeps the include and
exclude lists, `remote.url` and `remote.extraHosts` as stored (Q-REPO-193, `checkDelegatedMirrorScope`), and "is this the well-known upstream" is
`discover.SameUpstream` for the bound rule and for npm's lister (Q-REPO-194, Q-NPM-75), with a query in a mirror's url refused (Q-REPO-195). A
format's lister that must know its well-known upstream calls `discover.SameUpstream` with the catalog's default upstream and does not compare
strings.

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

## The status and run operations (work package D)

`GET /api/v1/repositories/{key}/mirror` (`getRepositoryMirror`) and `POST /api/v1/repositories/{key}/mirror/run`
(`runRepositoryMirror`), R-REPO-056. The decisions are Q-API-196 to Q-API-200 and Q-REPO-163 to Q-REPO-166.

| File | Responsibility |
|---|---|
| [internal/api/v1/mirror_api.go](../../internal/api/v1/mirror_api.go) | the two handlers; the permission decisions (`requireMirrorReader`, `requireMirrorWriter`, `hiddenOr`); the state derivation (`mirrorStateOf`); the status document (`buildMirrorStatus`, `decodeLastPass`); the enqueue and the request in one transaction (`enqueueMirrorPass`); `MirrorServices`, `MirrorState` and `MirrorRepositories`, the seams that the handler tests replace, with the PostgreSQL implementation `pgMirrorState` |
| [internal/meta/postgres/queries/mirror_api.sql](../../internal/meta/postgres/queries/mirror_api.sql) | `MirrorStatusRead` (one row from the repository outwards: the state row if any, the owning job, the count of `mirror_pending`, `repository_usage.logical_bytes`) and `MirrorRequestPass` (the only write) |
| [internal/audit/events_mirror_api.go](../../internal/audit/events_mirror_api.go) | the type `mirror.pass_requested`; C registers the other `mirror.*` types in its own file |
| [internal/errs/codes.go](../../internal/errs/codes.go) | `mirror_pass_running`, `mirror_off`, `mirror_paused` (all 409) |
| [internal/api/v1/repositories_api.go](../../internal/api/v1/repositories_api.go) | `ListFormats` serves `mirror` from the catalog fact (`mirrorFact`) |

The run, in order: the permission (write-config, a project administrator with the switch, or `manage`), the body, the repository from
PostgreSQL (`not_applicable`, `mirror_off`, `mirror_paused` before anything is written), then one transaction that enqueues
`mirror.KindPass` under `mirror.PassKey(id)` (an existing queued, running or cancelling job answers 409 `mirror_pass_running` and nothing is
written), upserts the request and records `mirror.pass_requested`. The status never writes.

**What C must keep to** (the API depends on it; the integration tests of `test/integration/mirrorapi` seed rows in this shape):

- The API writes only `pass_requested_at`, `pass_requested_by` and `pass_requested_full` of `mirror_state`, creating a row with the
  defaults when the dispatch has not. It does not touch `updated_at`. A request not yet taken keeps `pass_requested_full` true when a
  later plain request arrives (Q-REPO-163), so the engine **takes a request by clearing `pass_requested_at`, `pass_requested_by` and
  `pass_requested_full` together**; a request that finds `pass_requested_at` clear starts over.
- `last_pass` is a JSON object with exactly the members of the schema `MirrorPassSummary`: `kind` (`full`, `incremental`), `method`
  (`feed`, `listing`, `names`, or `null`), `startedAt` and `finishedAt` (RFC 3339), `outcome` (`complete`, `partial`, `bound_reached`,
  `offline`, `stopped`), `packages`, `packagesFailed`, `itemsStored`, `bytesStored`, `discoveryComplete` and `discoveryNote` (`null` or text).
  The status decodes it with `encoding/json` into the generated type and shows `null` for a document that is missing, `null`, not an
  object or has no `outcome` (Q-API-197); pin the shape in the engine's tests.
- The pass's job is the one in `pass_job_id`; the status shows its `kind` and `state` from the job table. `running` is the state `running`
  or `cancelling`; a pass in progress whose job is any other state, or gone, is `waiting` (Q-API-196).
- `bound_reached` is `bytes`, `quota` or `capacity` (the check of the table), cleared by the engine when the bound no longer holds; the status
  shows `bound_reached` for as long as the column is set. `consecutive_failures >= 3` shows `failing`, ahead of the bound and the pass.
- `last_error` is shown unchanged as `errors.lastMessage`: it must name hosts and never a URL or a credential (Q-REPO-166, H-530).

**Tests.** Handler tests with narrowed callers and every error path:
[internal/api/v1/mirror_api_test.go](../../internal/api/v1/mirror_api_test.go) (the decider `mirrorDecider` answers as the engine does: a refused
`manage` is 403, only a refused `read` is hidden) and
[mirror_format_test.go](../../internal/api/v1/mirror_format_test.go). Integration, against the stack:
[test/integration/mirrorapi](../../test/integration/mirrorapi): the permission table of both operations with the real decision point and a
project administrator, 202 then 409 with the same job, the three refused modes, the status of a seeded row in each state, the request columns
and the audit event, the pause through `PATCH` with `If-Match`.

Until the npm and NuGet listers exist, `mode: mirror` is refused for every format (R-REPO-050). The integration suite therefore registers a
catalog entry of its own before the replica starts, a copy of npm's with the fact `Mirror` set (`registerMirrorFormat` in
[env_test.go](../../test/integration/mirrorapi/env_test.go)); its repositories are created, patched and read through the management API.
The entry is a catalog entry only, not a registered format, so the answers of the repository operations about it do not satisfy the
document's enum of format names, and the suite sends them with `rawDo`, which does not validate. **When the npm lister lands:** set
`mirrorFormat` to `"npm"`, delete `registerMirrorFormat` and `rawDo` (use `do`), and the suite runs against npm.


## The engine (work package C2)

The engine is [internal/mirror](../../internal/mirror/) beside the contract's files. It sends no request itself: it speaks to the
remote engine's side through `mirror.Host` ([host.go](../../internal/mirror/host.go)), to the formats through `discover.Lister`, and to
PostgreSQL through `mirror.Store`.

| File | Responsibility |
|---|---|
| [mirror.go](../../internal/mirror/mirror.go), [host.go](../../internal/mirror/host.go) | the names of the kinds and the queue, `PassKey`, `PassArgs`; the seam `Host`, `Pass`, `FillResult`, `ErrPreempted`, `*WaitError` (the contract, C1 implements it) |
| [engine.go](../../internal/mirror/engine.go) | `Options`, `New`, `Dispatch` (the task `mirror-dispatch`), the summary shape, `background` (the share B of R-REPO-055 through `proxy.BackgroundShare`) |
| [pass.go](../../internal/mirror/pass.go) | `RunPass` (the job `mirror-pass`): `begin` (the checks before a claim, the claim), `run`, `prepare`, `execute`, the halts (`setHalt`, `checkStop`, `applySnapshot`) |
| [discovery.go](../../internal/mirror/discovery.go) | the discovery step: the method, the held and exact names of a full pass, a page of names and its cursor, the codes of a failed attempt |
| [fill.go](../../internal/mirror/fill.go) | the fill step: the feeder, the package workers, the plan and the artifacts of a package, what an error of a request means |
| [request.go](../../internal/mirror/request.go) | `call`: one request through the window, with the waits of a rate limit and of a `*WaitError`; the event of a rate limit; the long wait |
| [throttle.go](../../internal/mirror/throttle.go) | the window: slow start, additive increase, one halving per wait, the ceiling, the wait itself |
| [bounds.go](../../internal/mirror/bounds.go) | the byte bound: the counter, the pass's own stores since its fold, the announced size |
| [end.go](../../internal/mirror/end.go) | the ends of an attempt: complete, partial, bound reached, offline, stopped, deferred, fence lost, failed; the summary; `sanitize` |
| [policy.go](../../internal/mirror/policy.go) | the pure rules: `Clock`, `Jitter`, the back-offs, `DecideKind`, `FinalKind`, `SelectItems`, `Fingerprint` |
| [store.go](../../internal/mirror/store.go), [store_pg.go](../../internal/mirror/store_pg.go) | the port `Store` and its PostgreSQL implementation `PGStore` over [mirror.sql](../../internal/meta/postgres/queries/mirror.sql); `ClearAfterRestore` |
| [metrics.go](../../internal/mirror/metrics.go) | the ten families of R-REPO-057 and the database gauge source |
| [events_mirror.go](../../internal/audit/events_mirror.go) | the four asynchronous event types (`mirror.pass_requested` is the run operation's) |
| [wire_mirror.go](../../internal/app/wire_mirror.go), [wire_mirror_host.go](../../internal/app/wire_mirror_host.go) | the area `mirror` (roles `worker`, `all`), the kinds and the task, `MirrorOverride`; the host placeholder that C1's file replaces |

### How a pass runs

1. **The dispatch** (every minute, exclusive): lists the snapshot, ensures a `mirror_state` row for every remote in mode mirror,
   clears the pass and the pending names of every remote repository of the snapshot that left the mode (Q-REPO-162; an empty snapshot clears nothing), reads the rows that are due
   (`next_pass_at` passed or null, or a request, and no `rate_limited_until` ahead) and enqueues `mirror-pass` under `PassKey` for
   each that is in service, not paused and has a lister (`Host.Lister`).
2. **The claim** (`begin`): a job that finds the repository gone, no longer a mirror, paused or disabled does nothing (its result
   says `skipped`); otherwise one statement makes it the owner of the pass (the fence, [the security page](../security/mirror.md)).
   The kind of a new pass is `DecideKind`; the method may raise it to full (`FinalKind`, Q-REPO-156).
3. **The checks**: offline mode or the global switch end the attempt `offline`; a bound that was set and still holds (the counter at
   `maxBytes`) ends it `bound_reached` without any request.
4. **Discovery**: while fewer than 10,000 names are pending, one page: a full pass first adds the exact include names and pages the held
   names (Q-REPO-158), then the format's pages; a page's names and its cursor are one statement; a deleted name refreshes its document
   through the plan and stores nothing.
5. **Fill**: the pending names in name order go to as many workers as the ceiling; a package asks its plan, applies the version bounds
   (`SelectItems`), checks the byte bound and fills each artifact through `Host.Fill`; a finished package deletes its row and raises
   the counters in one statement. The snapshot is read before each package: a deleted, disabled, paused, offline or switched
   repository halts the attempt.
6. **The end** (`finish`): one of the ends in the table below; the summary, the cursor of the last complete pass, the next pass and
   the event are written in one statement.

| Ends with | When | Pass | `next_pass_at` | Event | Metric outcome |
|---|---|---|---|---|---|
| `complete`, `partial` | discovery ended and no name is left to take (`partial`: a package was left after three attempts) | cleared; the cursor, `last_full_pass_at` and the fingerprint advance; failures reset; the bound clears | start + `intervalSeconds`, or now | `mirror.pass_completed` | `complete`, `partial` |
| `bound_reached` | an artifact would cross `maxBytes`, or the bound still holds at the start | cleared; the pending names stay; the cursor advances only if discovery had ended | as above | `mirror.pass_completed`, `mirror.bound_reached` once per transition | `bound_reached` |
| `stopped` (pause, disabled) | before a package | kept | now | `mirror.pass_completed` | `stopped` |
| `stopped` (mode change) | before a package | cleared, the pending names deleted | now | `mirror.pass_completed` | `stopped` |
| `offline` | offline mode or the global switch | kept | now + `min(intervalSeconds, 5 min)` | `mirror.pass_completed` | `offline` |
| deferred | a rate-limit wait over 300 s | kept; `rate_limited_until` | the end of the wait | (`mirror.rate_limited`) | `deferred` |
| failed | a failure of the pass itself | kept; `consecutive_failures` + 1, the error recorded | now + `min(60 s x 2^(failures-1), 6 h)` +-20 % | `mirror.pass_failed` | `failed` |
| fence lost | a write matched no row | not touched | not touched | none | `stopped` |
| released, lease lost | the job's context ended | not touched | not touched | none | none |

### What D and the integration wave read

- **`last_pass`** (jsonb): `{kind, method, startedAt, finishedAt, outcome, packages, packagesFailed, itemsStored, bytesStored,
  discoveryComplete, discoveryNote}`, times RFC 3339 UTC, `method` and `discoveryNote` null when there are none. It is the summary
  of the last attempt that ended with one of the five outcomes (Q-REPO-155); `packages` is the number of packages done.
- **The pass in progress** is `pass_started_at IS NOT NULL`; `pass_names` counts the names inserted, `pass_packages_done`,
  `pass_packages_failed`, `pass_items_stored` and `pass_bytes_stored` what the attempts did. Pending names are the rows of
  `mirror_pending` (`attempts < 3` are the ones a pass still takes). `pass_job_id` is the job to name in `pass.job`.
- **`rate_limited_until`** is set by a deferred attempt and cleared by the next claim and by an end; `rate_limit_waits`,
  `rate_limit_wait_seconds`, `last_rate_limit_at` and `last_rate_limit_status` count the waits of all passes.
- **`bound_reached`** (`bytes`) and `bound_reached_at` are set when a pass ends at the bound and cleared when a pass ends complete
  or partial.
- **`state`** of the status: `waiting` is a pass in progress with no running job (a deferred or failed attempt, a release);
  `failing` is `consecutive_failures >= 3`.
- **The job results**: `{"outcome", "kind", "method", "packages", "packagesFailed", "itemsStored", "bytesStored",
  "discoveryComplete"}` for an end, `{"deferred": "rate_limited", "until": <RFC 3339>}` for a long wait,
  `{"outcome": "stopped", "reason": "fence_lost"}`, `{"skipped": "repository_gone" | "not_mirror" | "paused"}` for a job that
  started nothing; the dispatch's `{"repositories", "enqueued"}`. A failed attempt ends the job with the error
  `mirror pass failed (<code>): <message>`.
- **The codes of a failure** (`last_error_code`): `discovery_unavailable`, `discovery_failed`, `discovery_stalled`,
  `cursor_too_large`, `not_applicable`, `upstream_unavailable`, `internal`.
- **A run request** (`pass_requested_*`) is consumed by the claim; D writes only these columns and enqueues `KindPass` under
  `PassKey` with the repository and its numeric id.

### The seams of the engine's tests

`mirror.Options` takes a `Clock` (`Now`, `Sleep`) and a `Jitter`; the unit tests use a clock whose sleeps cost nothing and move its
time, and a fixed jitter ([rig_test.go](../../internal/mirror/rig_test.go), with the in-memory store, the fake host and the fake
lister). The integration tests start replicas through the composition root with `app.Options.Mirror`
(`testenv.ReplicaOptions.Mirror`) and give them a host that counts what it was asked to store
([test/integration/mirror](../../test/integration/mirror/)). `repository.Build` builds a repository of the read model without a
database.

### Tests of C2

| Tier | Where | What |
|---|---|---|
| Unit | [policy_test.go](../../internal/mirror/policy_test.go), [engine_test.go](../../internal/mirror/engine_test.go) | the window (slow start, one halving per wait, the ceiling, the floor), the waits and their ranges, the cap of a hint, the failure back-off (60, 120, 240 s, +-20 %), the version bounds with a pinned version, the full or incremental decision, a pass end to end, discovery and its bounds, the long wait and the next job at that time, pre-emption and `*WaitError` not counted as failures, the byte bound and its transitions, a pause, a mode change, offline, the fence, a release, the dispatch, the share B, several workers, the series at 0 |
| Integration | [test/integration/mirror](../../test/integration/mirror/) | a pass through the job framework, two dispatchers and the dedup key, the claim and the fence at the statements, the lost lease, the release at shutdown, pause and resume, the mode switch, offline, the error back-off against the real table, the byte bound with `repository_usage` and, with the real fold of a replica counting real file items, across three passes (`TestR_REPO_052_TheByteBoundHoldsAcrossPassesWhileTheFoldCountsWhatWasStored`), the restore statement, the gauges |
| Chart | [rules_test.yaml](../../deploy/helm/trove/tests/rules_test.yaml) (`make chart-test`) | `TroveMirrorFailing`, `TroveMirrorStale` |

### Not built, and open

- The remote engine's side (`Host`): until C1's file replaces [wire_mirror_host.go](../../internal/app/wire_mirror_host.go) the host
  has no lister, so no pass starts.
- Quota and capacity bounds ([H-506](../spec/repositories-and-proxy.md)): `maxBytes` is the only bound.
- The byte bound reads the counter that the usage fold writes ([blob-store.md](blob-store.md#usage-fold-r-stor-003)), so it holds across
  passes; it lags by one fold interval plus the artifacts in flight ([H-527](../security/mirror.md)).
- **The restore of R-REPO-058** is `PGStore.ClearAfterRestore`; phase 7's restore job calls it (no post-restore hook mechanism exists:
  the vulnerability scanning's restore is tested as the state it leaves, Q-VULN-58). Phase 7 owns that call.

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

## Work package B: what is built

The files of NuGet's discovery, all in [internal/formats/nuget](../../internal/formats/nuget) (the specification is
[nuget.md](../spec/formats/nuget.md#mirror-discovery), the decisions Q-NUGET-99 to Q-NUGET-105):

| File | Responsibility |
|---|---|
| [mirror.go](../../internal/formats/nuget/mirror.go) | the `discover.Lister` methods on the remote adapter: `MirrorMethods` (what the service index offers, read through `Upstream.Document`), `MirrorNames` (dispatch by method), the exact IDs of `names`, `MirrorHosts` (none), the helpers (`admitted`: normalize and `Scope.Admits`, `fetchJSON`) |
| [mirror_feed.go](../../internal/formats/nuget/mirror_feed.go) | `feed`: the catalog index and pages in `commitTimeStamp` order, whole commits queued, the cursor `{"commit","page","skip"}`, `PackageDelete` as `Removed` |
| [mirror_listing.go](../../internal/formats/nuget/mirror_listing.go) | `listing`: the search with `take=1000` and `skip`, the cursor `{"skip","total"}`, the truncation note |
| [mirror_plan.go](../../internal/formats/nuget/mirror_plan.go) | `MirrorPlan`: the version list (at most 32,768 entries, `remote.VersionListBounded`) and the registration (one `remote.Budget`) through `Upstream.Document`, the `.nupkg` per version newest first, the sizes the registration announces |
| [mirror_test.go](../../internal/formats/nuget/mirror_test.go) | the unit tests over a fake `discover.Upstream` |
| [remote/discovery.go](../../internal/formats/nuget/remote/discovery.go) | `Discovery.Catalog`, `CatalogURL`: the catalog resource of the service index (a bad address leaves it out and never fails the index) |

The NuGet part of the counting stub ([test/nugetstub](../../test/nugetstub), described in [e2e.md](e2e.md#the-counting-nuget-test-upstream)) has
the catalog ([catalog.go](../../test/nugetstub/catalog.go)), the search cap (`Mode.SearchSkipCap`) and the throttling modes
of R-REPO-059 ([throttle.go](../../test/nugetstub/throttle.go)); [mirror_discovery_test.go](../../test/nugetstub/mirror_discovery_test.go)
runs the format's discovery against it over HTTP with the criteria of nuget.md.

What the engine builder must know of NuGet's lister:

- **A `feed` call reads at most 50 catalog pages** (`maxCatalogPagesPerCall`, Q-NUGET-106) and may return no name with `Done: false`
  and a moved cursor; `pageStep` accepts that, because it stalls only on an unchanged cursor. Discovery bodies are requested with
  `Get(..., max = 32 MiB)` and inflated within it (Q-NUGET-108); a plan fails on a version list or a registration over the bounds of a
  client's rendering (Q-NUGET-107, Q-NUGET-109).
- **Calls are stateless.** `feed` reads the catalog index in every call that has no page in its cursor, and the page in progress
  first when it has one; the engine passes the `Cursor` of the last page back as `PageRequest.Cursor`, and the `Cursor` of the last
  page of a complete pass as `Since` of the next incremental one. The cursor of the end of a pass is `{"commit": "<stamp>"}` (`{}` for
  an empty catalog); `listing` and `names` return a nil cursor at their end, so they have no incremental pass.
- **`Limit` counts names and removed names together.** A page holds whole commits, or a part of a commit larger than `Limit`
  (the cursor's `skip`); a name can appear in two pages of a pass, as the contract says.
- **An error from `Upstream.Get` or `Upstream.Document` is returned unchanged**, so a rate limit reaches the engine with
  `discover.AsRateLimited` true. A status other than 200 of a catalog or search request is an upstream failure (a plain
  error, not a rate limit); a 4xx of the search at a `skip` above 0 is the end of a capped window, and not an error.
- **`MirrorPlan` takes only a name.** It normalizes it (a name that is no ID is an empty plan and no request); the engine
  checked it against the scope when it queued it. The version list is the first document it asks for: not found is an empty plan.
  A registration the upstream does not have leaves the plan standing on the version list, with no sizes.
- **`Item.Version` is the URL form** of the version (`1.0.0`, lower-case, no build metadata), the same as in `Item.Path`.
- **`MirrorHosts` is empty**: a catalog page or a search request on a host that is neither the upstream's nor an `extraHosts` entry
  fails the pass with `foreign_host_denied` from `Upstream.Get`.

## The remote engine's side (C1)

What the engine does with a background request (`proxy.Request.Priority == PriorityBackground`), and the implementation of `mirror.Host`
behind it. Specified in R-REPO-054, R-REPO-055 and R-REPO-057; the decisions are Q-REPO-149 to Q-REPO-154 and Q-ARCH-308 to Q-ARCH-311.

| File | Responsibility |
|---|---|
| [internal/proxy/slots.go](../../internal/proxy/slots.go) | The slots of R-REPO-040 with the background counter per repository, the running background fetches (`bgRun`), pre-emption by a client fetch (`acquire`, `preempt`, `take`), the yield window (`yield`, `yieldAll`, `yieldOpen`), `onWait` (a client has to queue) |
| [internal/proxy/background.go](../../internal/proxy/background.go) | `ErrPreempted`, `*BackgroundWait` (`WaitCircuitOpen`, `WaitSpoolBusy`), `resultOf` and `backgroundOutcome` (the outcomes that only the leading background request sees), `rateLimited` (`upstream.RateLimitOf` on a background answer), the signal: `Signals`, `raiseYield`, `onYield`, `listenYield` |
| [internal/proxy/fetch.go](../../internal/proxy/fetch.go), [flight.go](../../internal/proxy/flight.go), [metadata.go](../../internal/proxy/metadata.go), [artifact.go](../../internal/proxy/artifact.go) | The fetch path with the background rules: the circuit is read (`circuitPeek`), never claimed or changed (`fetcher.success`, `fetcher.failed`); the slot is the background one; the upstream log call carries `flight_role: mirror` and `JobID`; `flightResult.own`; a pre-empted fetch returns `ErrPreempted` and releases its flight with `failed` |
| [internal/proxy/fill.go](../../internal/proxy/fill.go) | `Engine.Fill`: steps 4 to 10 of R-REPO-025 for a background artifact request without a client: `FillOutcome{Present, Bytes}` |
| [internal/proxy/get.go](../../internal/proxy/get.go) | `Engine.Get`: one discovery GET through the repository's client (`proxy.Get`, `GetResult`, `DiscoveryDocumentMaxBytes`) |
| [internal/proxy/upstream/client.go](../../internal/proxy/upstream/client.go), [internal/outbound/hop.go](../../internal/outbound/hop.go) | `Client.AllowedMirror` (the hosts of a discovery request), `outbound.WithoutCredentials` (no credential to a discovery host) |
| [internal/formathost/mirror_host.go](../../internal/formathost/mirror_host.go) | `MirrorHost`, the implementation of `mirror.Host`: `Lister`, `Admits`, `Upstream` (the pass's `discover.Upstream`), `Fill`; `mirrorPopulate` (the populate event with the system as actor and reason `mirror`); `mapBackground`; the context mark of the pass (`withBackground`) |
| [internal/app/wire_mirror_host.go](../../internal/app/wire_mirror_host.go) | `App.mirrorHost()`: the host, built on first use |
| [internal/cluster/areas.go](../../internal/cluster/areas.go) | The signal area `mirror_yield` (`AreaMirrorYield`) |

### What the host does, for the engine (C2)

- **Every request is a background request** of the pass: `Pass.Ceiling` is the share B the request keeps within (`proxy.BackgroundShare`),
  `Pass.JobID` is written to the upstream log as `job_id` with `flight_role: mirror`; nothing the host sends moves or resets the circuit,
  and nothing probes.
- **Errors**, as `mirror/host.go` says: a rate-limit answer is a `*upstream.RateLimitError` (`discover.AsRateLimited` is true; a
  status of 403, 429 or 503 only, per `upstream.RateLimitOf`); a pre-emption wraps `mirror.ErrPreempted` (a client fetch took the slot, or
  the signal `mirror_yield` arrived); an open circuit is `*mirror.WaitError{circuit_open}` with `After` between 1 s and 5 s, a refused
  spool admission `*mirror.WaitError{spool_busy}` with `After` 5 s; anything else is a failure of the item. A request that waits for a
  slot (the pass's window is above what the replica can give, or a client waits) is not an error: it blocks until it has one or the
  context ends.
- **The engine counts what it sees.** The host counts nothing: `trove_mirror_preempted_total` and `trove_mirror_items_total{result=preempted}`
  are the engine's, on `mirror.ErrPreempted`, and so are the rate-limit metrics, on `discover.AsRateLimited`.
- **`Fill`** answers `Present` for an artifact that is stored (no upstream request, no gate, no event). Otherwise it runs the format's
  `BeforeArtifact` (its document requests are background requests of the pass), then the engine's fill. Offline, a negative entry, a
  refused populate check, a version blocked by the vulnerability scanning and a version the format's gate refuses are errors that
  are failures of the item (not a wait); the host does not check the repository's `mode` or `pausedAt`: that is the engine's.
- **`Admits`** applies the include and exclude patterns on the name (a format whose patterns match names), the format's name rule
  (`PackageDocumentPath`, `catalog.NormalizeName`), and the claims of R-REPO-023 through `Deps.Refusal`; a failure to read the claims
  is answered by `Admits` itself, and later by a name that is not admitted. It is cheap: the claim index is the routing's snapshot.
- **`Upstream(repo, pass)`** needs the format's lister: it fails for a format without one (`Lister` is false). Its `Get` refuses a host
  that is not the upstream's, an `extraHosts` entry or one of `lister.MirrorHosts(base)` with `foreign_host_denied` and sends nothing; a
  lister's host never gets a credential. `Get` with `max` 0 reads up to 32 MiB (`proxy.DiscoveryDocumentMaxBytes`). `Document` is
  `found=false` for a negative entry (404, 410), a gone document or a package marked gone; a stored document is returned while fresh,
  and a stale one is revalidated by a background request.
- **A flight is not cancelled with the pass.** A background fetch runs detached from the context of its caller, like a client's: when the
  pass's context ends, `Fill` returns at once and the fetch finishes or is cancelled by a pre-emption, the yield or the shutdown (R-ARCH-024).
  Its slot is held until then.
- **Where it runs.** In a client-serving process the host shares the replica's engine, so a client fetch pre-empts the pass in the process;
  in a `worker` the host builds an engine and a format host of its own on first use (Q-ARCH-308), which subscribes to `mirror_yield`. The
  worker's engine has not been exercised by a test that starts a worker with the queue `mirror`: that needs the engine's handlers.

### Tests

- Unit: [preempt_test.go](../../internal/proxy/preempt_test.go) (pre-emption, the joined fetch, the replica limit, a waiting client, the yield with a fake clock, the signal, the
  classification of a background answer), [allowed_mirror_test.go](../../internal/proxy/upstream/allowed_mirror_test.go),
  [without_credentials_test.go](../../internal/outbound/without_credentials_test.go), [mirror_host_test.go](../../internal/formathost/mirror_host_test.go),
  [wire_mirror_host_test.go](../../internal/app/wire_mirror_host_test.go).
- Integration ([test/integration/proxy](../../test/integration/proxy)): `mirror_test.go` (pre-emption on one replica with `maxConcurrentFetches` 2, the joined fetch, the signal across two
  replicas on the real Valkey, the circuit, the credentials of `Get`, the upstream log), `mirror_host_test.go` (the host over the real engine: the populate event, `Present`, the
  mapping of the errors, `Admits`).
