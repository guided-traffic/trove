# Mirror mode

Status: the engine of the mirror mode ([internal/mirror](../../internal/mirror/)) is built; this page describes its own mechanism:
who owns a pass and how a stale writer is stopped, what a pass takes from an upstream and what bounds it, what its state and its
messages show, and what a restore leaves. The specification is
[repositories-and-proxy.md](../spec/repositories-and-proxy.md#mirror-mode) (R-REPO-050 to R-REPO-059); the build is described in
[mirror.md](../developer/mirror.md). The requests themselves, with their credentials, egress rules and integrity checks, belong to the
remote engine's side of the seam (`mirror.Host`), which the remote engine's work package implements; this page covers what the
engine does with the answers.

## Who writes, and as whom

The passes run in whatever process claims the queue `mirror`: the roles `worker` and `all` build the area `mirror`
([wire_mirror.go](../../internal/app/wire_mirror.go)); `web`, `api` and `packages` replicas only enqueue a pass on request and read
the state. The engine holds no upstream credential and sends no request itself: discovery goes through the `discover.Upstream` that
the host lends the pass, and every fill through `Host.Fill`, so the egress rules, the credential policy and the integrity checks
of a client's fill apply to a pass's (R-REPO-053 step 3, Q-REPO-146). The events a pass writes have the actor `system` named
`mirror`, the repository as subject and as event column ([events_mirror.go](../../internal/audit/events_mirror.go)); they are
asynchronous and carry counts, codes and the host name of the upstream, never a URL or a package name (Q-OPS-170).

## The fence

A pass outlives its jobs: a failed, released or crashed job is continued by the next dispatch. The row `mirror_state` therefore
names the owner of the pass in progress by the job id and the instance id of the claiming process (`pass_job_id`, `pass_holder`,
Q-ARCH-287).

- **The claim** ([mirror.sql](../../internal/meta/postgres/queries/mirror.sql), `MirrorClaim`) is one statement: it locks the row,
  writes the job and the holder, starts a new pass (zeroed counters, the attempts of the pending names given back) or keeps the
  pass in progress, and consumes the run request. Two claims serialize on the row; the later one owns the pass.
- **Every later write of the pass** (the method, the names and the cursor, the result of a package, a wait, the end, a failure)
  names the fence in its `WHERE`. A statement that matches no row returns `fenced = 0` or no rows, the store answers
  `ErrFenceLost`, and the job stops at once without writing again: its result says `{"outcome":"stopped","reason":"fence_lost"}`.
  A late write of a process that lost its lease, or of an attempt that was released and claimed elsewhere, changes no row
  (`TestR_REPO_053_AWriteUnderAnOldFenceChangesNoRow`, `TestR_REPO_053_ALostLeaseIsContinuedByTheNextDispatchAndTheOldFenceWritesNothing`).
- **The comparison is on uuids.** The instance id of a process is 16 random bytes in hex without hyphens; a text comparison against
  the column would never match the holder's own row (Q-ARCH-313).
- **A released or lease-less attempt writes nothing.** When the job's context ends the attempt returns the cause at once; the pending
  names and the counters are the state the next job continues from (R-REPO-053 drain and restart, crash and failover).
- **The dispatch clears what a mode change left**, fence included, so that a pass that still runs for a repository that left the
  mode matches no row either (Q-REPO-162).

## What a pass takes from an upstream

A hostile or broken upstream decides which names it lists. What bounds them:

| Bound | Where | Effect |
|---|---|---|
| The include and exclude patterns, the claims, the format's name rule | `Host.Admits`, applied by the format to every name it returns and again by the engine to the held and the exact names | a name outside them is never inserted, never asked for, not even its document |
| At most 10,000 pending names | `MaxPending`, checked by `PendingCount` before every page; the held names are paged under the same bound | discovery pauses until the fill has worked the batch off |
| A name of at most 1024 characters, valid UTF-8 | `fitNames` | longer or invalid names are dropped before the insert, so one name cannot fail the statement of a page |
| A cursor of at most 4096 bytes | `discover.MaxCursorBytes`, the `CHECK` of the columns | a longer cursor fails the attempt with `cursor_too_large` |
| A page that makes no progress | `pageStep` | the attempt fails with `discovery_stalled` instead of asking for the same page forever |
| Three attempts per package, a minute more each | `MaxPackageAttempts`, `PackageRetryStep` | a package that fails is left to the next pass and counted failed |
| The window and the ceiling | `throttle.go` | never more requests in flight than the background share of R-REPO-055 |
| The byte bound, `maxBytes` | `bounds.go` | no artifact whose announced size would cross it (R-REPO-052) |

The pass sends nothing while a rate limit asks it to wait, honours a wait of up to six hours, and never moves the circuit of the
clients ([Q-REPO-139](../spec/repositories-and-proxy.md#q-repo-139--the-mirrors-requests-neither-move-nor-reset-the-circuit-and-an-open-circuit-pauses-the-mirror)).

## What the state and the messages show

`mirror_state.last_error` and the job's error hold the message of a failed attempt with every URL reduced to its host
([sanitize in end.go](../../internal/mirror/end.go)); the status of R-REPO-056 shows the same text to a holder of
`repositories:read-config`. The codes are closed (`discovery_unavailable`, `discovery_failed`, `discovery_stalled`,
`cursor_too_large`, `not_applicable`, `upstream_unavailable`, `internal`). The counters and gauges carry the repository key and no
package name (Q-OPS-158); the label sets are closed.

## What a restore leaves

`mirror_state` is data, `mirror_pending` transient ([tables_mirror.go](../../internal/meta/postgres/tables_mirror.go)). The restore
rule of R-REPO-058 is one statement, `PGStore.ClearAfterRestore`: the pass in progress and its fence, the request, the rate-limit
wait and the bound are cleared, the next pass is due at the restore time, and a restore without remote caches also forgets the cursor
of the last complete pass, so that its first pass is full. The restore job of phase 7 calls it; until it exists the function is
tested alone (`TestR_REPO_058_ARestoreClearsThePassAndKeepsOrForgetsTheCursor`).

## What this does not cover

### H-527 · The bound's base is a lagging counter and a per-process tally

Component: the byte bound of a mirror (R-REPO-052). Subject: a remote repository with `maxBytes`. Live today.

The pass reads `repository_usage.logical_bytes` and adds the bytes it stored itself after the counter's last fold. The fold loop
of [storage.md](../spec/storage.md) R-STOR-003 is not built in this release, so nothing writes `repository_usage` outside tests: the
counter does not move, content that clients filled is not counted, and the pass's own tally lives in the memory of one attempt, so a
second attempt or pass starts from the counter again. `maxBytes` therefore holds within one attempt and not across attempts or
passes until the fold exists; with it, the bound can still be passed by the artifacts in flight and one fold interval
([H-505](../spec/repositories-and-proxy.md)). What makes it real: a mirror with a `maxBytes` that runs several passes.

### H-528 · A process that lost its lease keeps fetching until its heartbeat notices

Component: the fence of a pass (Q-ARCH-287). Subject: the cache of a mirrored remote repository. Live today.

The fence stops every write of the pass's state, but a fetch that is in flight is a request to the remote engine, which commits the
artifact it fetched through its own transaction. A process whose job lost its lease keeps its pass running until the heartbeat
finds the loss (within `jobs.lease_duration`, 60 s by default) and cancels the job's context; a fill that commits in that time stores
an artifact the pass would have stored. The content is addressed and checked like any fill (R-REPO-026), so the result is an item
that the next job would store as well; the cost is bandwidth and a store that the pass's counters do not show. What makes it real: a
process that is partitioned from PostgreSQL but not from its upstream and the object store.

### H-529 · The message of a failed attempt is reduced for URLs only

Component: the messages of a pass (R-REPO-056). Subject: a reader of the status, the job and the log of a mirrored repository.
Dormant.

`sanitize` replaces every URL in a message by its host, which is what the error texts of the discovery and of a fill carry today. A
secret that a lower layer puts in an error text outside a URL (a header echoed by an upstream's error body, a token in a free-form
message) is kept as it is up to 1024 characters. Nothing in the code that builds those messages does that; what would make it real
is a format or a host that returns an upstream's body in its error.
