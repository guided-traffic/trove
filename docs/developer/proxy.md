# The pull-through proxy

Status: this page starts with the cache invalidation of remote repositories (package
[`internal/proxy/invalidation`](../../internal/proxy/invalidation)). The engine that answers a request for
a remote repository adds its sections here when it is built; until then the invalidation is a library
and an API that nothing in the request path consults yet. The requirements are
[R-REPO-045](../spec/repositories-and-proxy.md), [R-REPO-032](../spec/repositories-and-proxy.md),
[R-OPS-021](../spec/operations-and-observability.md) and [R-API-024](../spec/api-and-ui.md).

## Cache invalidation

An invalidation makes the cached metadata (scope `metadata`), the cached misses (`negative`) or both
(`all`) of a remote repository under a path prefix count as expired. It deletes nothing and touches no
cache entry: it is one row in `remote_cache_invalidations`, so invalidating a million entries costs one
insert and a count. Each replica holds the rows in memory and answers the engine's question per request
without a query.

```mermaid
flowchart LR
  API["POST …/cache/invalidate"] --> TX["one transaction: insert row, count,<br/>Notify(cache_invalidation), audit"]
  TX --> PG[("remote_cache_invalidations<br/>cluster_change")]
  PG -- "poll within epoch_poll_interval" --> SETB["Set on every replica"]
  TX -- "Valkey message (early)" --> SETB
  TX -- "after commit, at once" --> SETA["Set of the writing replica"]
  SETA --> ENG["engine: MetadataValid / NegativeValid"]
  SETB --> ENG
  FOLD["task proxy-invalidation-fold<br/>(worker)"] -- "expires_at, folded_at, delete" --> PG
  FOLD -- "Notify(del)" --> SETB
```

### Files

| File | Responsibility |
|---|---|
| [prefix.go](../../internal/proxy/invalidation/prefix.go) | `CheckPrefix` (the grammar of a prefix, Q-REPO-100), `Covers` (whole segments), the scopes |
| [payload.go](../../internal/proxy/invalidation/payload.go) | The payload of the change area `cache_invalidation` and its kinds (Q-OPS-154) |
| [set.go](../../internal/proxy/invalidation/set.go) | `Set`: the replica's copy of the rows, the matcher (`Matcher`), the `cluster.Listener`, the reload loop |
| [store.go](../../internal/proxy/invalidation/store.go) | The load of the rows through the sqlc queries |
| [service.go](../../internal/proxy/invalidation/service.go) | `Service.Invalidate` (the write side) and `Service.Entries` (the entry list of R-REPO-032) |
| [fold.go](../../internal/proxy/invalidation/fold.go) | `Folder` and the job kind and task `proxy-invalidation-fold` |
| [invalidation.sql](../../internal/meta/postgres/queries/invalidation.sql) | Every statement of the package |
| [cache_api.go](../../internal/api/v1/cache_api.go) | The two operations of the management API |
| [wire_invalidation.go](../../internal/app/wire_invalidation.go) | The wiring: one component with the set, the service and the folder |

### What the engine calls

The engine (R-REPO-025 steps 4 and 8) takes a `invalidation.Matcher`. `*invalidation.Set` implements it,
and `(*app.App).InvalidationMatcher()` returns the replica's set (nil on a replica without a client
listener, which serves no proxy request).

```go
package invalidation

type Matcher interface {
	// MetadataValid reports whether a metadata entry of the repository that was last
	// validated at validatedAt still counts: no invalidation of scope metadata or all
	// whose prefix covers path is newer than validatedAt.
	MetadataValid(repoID int64, path string, validatedAt time.Time) bool

	// NegativeValid reports whether a negative entry whose upstream request was sent at
	// sentAt (the "c" of the Valkey value, R-REPO-028) still counts: no invalidation of
	// scope negative or all with a covering prefix was created later than sentAt minus 5 s.
	NegativeValid(repoID int64, path string, sentAt time.Time) bool
}
```

Both answer from memory, take no lock and allocate nothing (about 30 ns with 50,000 rows). Rules for
the caller:

- `repoID` is the repository's identity column (`remote_cache_entries.repo_id`, the `<repo id>` of the
  negative key), not its key. `path` is the canonical path of the entry, the one stored in
  `remote_cache_entries.path`.
- `validatedAt` is the `validated_at` that the engine read from the entry, so the comparison is against a
  time the database wrote. If the engine stamps `validated_at` with a replica clock, the comparison needs
  the allowance that negative entries have (Q-REPO-101).
- An entry for which `MetadataValid` is false is expired for this request whatever its `expires_at` says:
  the engine revalidates it conditionally and serves the stale copy if the upstream fails (INV-11,
  R-REPO-027). An entry that is refreshed gets a new `validated_at` and counts again; nothing else has to
  be done.
- A negative entry for which `NegativeValid` is false is ignored like a malformed one (R-REPO-028). The
  entry is not deleted from Valkey.
- A request for the cache view (`<key>~cache`) and `Cache-Control: only-if-cached` ignore expiry and
  negative entries (R-REPO-032), so they do not ask the matcher.
- An artifact is never invalidated; do not ask for one.

The matcher is empty and answers "valid" until `Set.Start` has loaded the rows. The wiring starts it at
step 8, before the listener opens, and a failing load stops the start-up.

### The write side

`Service.Invalidate(ctx, caller, repo, prefix, scope) (int64, error)`:

| Class of `repo` | What happens | Answer |
|---|---|---|
| remote | One transaction: insert the row (its time is `clock_timestamp()`, Q-REPO-101), count the covered metadata entries (scope `negative`: 0), `Notify(cache_invalidation, {k: add, …})`, record `repository.cache_invalidated` | the count |
| virtual | `Notify(cache_invalidation, {k: virt, r: <virtual id>, p, s})` and the audit event; no row. The merged-metadata cache of the virtual engine consumes the change ([`ParsePayload`](../../internal/proxy/invalidation/payload.go)) | 0 |
| local | Nothing is written | `400 not_applicable` |

A prefix or scope that is invalid answers `422 validation_failed` with the pointer. A repository whose
lifecycle is not `active` is `404 repository_not_found`. The audit record is part of the transaction: a
failing recorder rolls the row and the epoch back (R-OPS-015). The operation is allowed in read-only mode;
the mode itself is not built yet, so nothing in this code consults it.

### Reaching the other replicas

The area `cache_invalidation` is a change area with payload version 1
([areas.go](../../internal/cluster/areas.go)); its `cluster_epoch` row is seeded by migration 00014. A
payload is `{"k","r","i","p","s","t"}` ([payload.go](../../internal/proxy/invalidation/payload.go)); the
prefix is base64url because the payload is limited to 1 KiB and `json.Marshal` writes `<` as six bytes.

| Kind | Written by | A replica's `Set` |
|---|---|---|
| `add` | `Invalidate` on a remote repository | applies the row from the payload, without a query; applying twice changes nothing |
| `del` | the fold task, after it deleted rows | reloads that repository's rows from PostgreSQL |
| `virt` | `Invalidate` on a virtual repository | ignores it |
| unknown, or unreadable | a newer build | reloads every repository |

The poll applies a change within `cluster.epoch_poll_interval`; the Valkey message is an early eviction
and the writing replica applies its own change right after the commit. A `FlushArea` (a gap in the change
log, an epoch that went backwards, an unknown payload version) reloads everything, and so does the timer
`cache.snapshot_safety_ttl`, which covers the rows that the cascade of a repository deletion removed
without a notice. A reload merges the rows that arrived by payload while its query ran, so a row is never
lost to the race; a failed reload keeps what is in memory and tries again. **A row leaves memory only when
a reload no longer finds it in the database**: an `all` row whose fold is late must keep invalidating
(R-REPO-045), so the set is bounded by the table and never drops a row by itself.

### The fold task

`proxy-invalidation-fold` (Q-OPS-153) is a scheduled task on the `maintenance` queue, exclusive, every five
minutes unless `maintenance.tasks.proxy-invalidation-fold` says otherwise; it can be started with
`POST /api/v1/system/tasks/proxy-invalidation-fold/run`. One run:

1. For each unfolded row, oldest first, in batches of 5,000 entries, each batch a transaction:
   `expires_at = LEAST(expires_at, row time)` for the metadata entries under the prefix with
   `validated_at` before and `expires_at` after the row time, locked in key order and not skipped
   (Q-REPO-103). Then `folded_at` is set. A `negative` row has nothing to fold and is marked at once.
2. For each row that is due, one more fold pass, then the delete, and a `Notify(del)` per repository in the
   same transaction. Due is: a `metadata` row one hour after `folded_at`; a `negative` or `all` row 86,405 s
   after `created_at` (`NegativeRetention`), the largest `notFoundTtlSeconds` plus the clock allowance, and
   only if it is folded.

The task is idempotent and resumable by the columns alone. The clock of the retention checks is an option
of `Folder`, which is how the tests move time. It takes about 70 s for a million entries on the
integration stack; the call that created the row does not wait for it.

### The management API

| Operation | Declaration | Handler |
|---|---|---|
| `POST /api/v1/repositories/{repoKey}/cache/invalidate` | `action: manage` on the path's repository; the system administrator holds it | `InvalidateRepositoryCache` |
| `GET /api/v1/repositories/{repoKey}/cache/entries` | `authenticated: true`; the handler admits a holder of `repositories:read-config` or of `manage` and applies the endpoint ceiling to both (Q-OPS-155) | `ListCacheEntries` |

Both live in [cache_api.go](../../internal/api/v1/cache_api.go) and follow the shared authorization step:
the request's holder (`edge.Access`) is the one the step took, and the handler asks it. The services
arrive in `v1.Options.Cache` (`CacheServices`); a nil member answers 503. The entry list pages by
`(path, variant)` with the cursor of R-API-010 and lists `kind=metadata` (default) or `kind=artifact`; it
answers `400 not_applicable` for a repository that is not remote. It reads PostgreSQL: it is a
diagnostic, not a request path.

### Wiring

[wire_invalidation.go](../../internal/app/wire_invalidation.go) builds one component, `invalidation`, at the
M2 proxy marker of step 8: the `Set` (only on a replica with a client listener), the `Service` and the
`Folder`. It adds no field to `App`: the accessors find the component in the list of started ones. The
registration lines in the core files are three, each inside an `M2 proxy` marker:
`a.wireInvalidation(ctx, infra)` in [app.go](../../internal/app/app.go) (the bootstrap),
`Cache: a.cacheAPI()` in the same file (the handler options) and `a.registerInvalidationJobs(r)` in
[wire_jobs.go](../../internal/app/wire_jobs.go) (the job registry).

### Tests

| Test | Tier | Requirement |
|---|---|---|
| `internal/proxy/invalidation` unit tests (`TestCovers`, `TestCheckPrefix`, `TestMatcher`, `TestSetApplyChange`, `TestSetReload`, `TestSetLoop`, `TestPayloadFitsAChangeMessage`) | unit | R-REPO-045, R-REPO-028 allowance, R-ARCH-018 payload limit |
| `internal/api/v1` `TestCacheAPI` | unit, answers validated against the OpenAPI document | R-API-024, R-REPO-032, R-API-003 |
| `test/integration/repository` `TestInvalidationAtScale` (1,000,000 entries, under 1 s, one row) | integration | R-REPO-045 |
| `test/integration/repository` `TestInvalidationPrefixes`, `TestInvalidationNegativeRetention` (86,405 s with a test clock), `TestInvalidationRetention`, `TestInvalidationService`, `TestCacheEntryList` | integration | R-REPO-045, R-REPO-032, R-OPS-015 |
| `test/integration/coordination` `TestInvalidationPropagation` (two replicas: without Valkey, with the Valkey client unavailable, with Valkey) | integration | R-OPS-021 |
| `test/integration/files` `TestR_API_024_CacheOperations` (the real decision point, two replicas) | integration | R-API-024, R-REPO-032 |

Not covered here, and owned by the engine's package: the conditional upstream request after an invalidation
and the stale copy served while the stub is down (R-REPO-045 acceptance), because they need the engine.
The variant of R-OPS-021 with Valkey really stopped belongs to the end-to-end set; here the client is
unavailable through an unreachable Sentinel, which is the same path of the client.

### Security notes

- `manage` on the repository is the only permission that invalidates (or the administrator role); the
  call is the one way to cut a forged negative entry short without a Valkey credential (H-49).
- An invalidation can force a conditional upstream request for every cached package of a repository. The
  rate limit of the management API applies, and the call is audited with the prefix, the scope and the
  count.
- The entry list shows the upstream URLs a repository was fetched from (without user info, R-REPO-025);
  it is open to the holders of `repositories:read-config` and `manage` only.
