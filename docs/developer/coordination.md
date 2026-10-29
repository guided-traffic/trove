# Coordination between replicas

Everything the replicas share goes through four mechanisms, each in a package of its own:

| Mechanism | Package | Spec |
|---|---|---|
| Job queue, workers, scheduler | [internal/jobs](../../internal/jobs/) | R-ARCH-016, R-HA-005 |
| Leases (advisory locks with fencing), lock categories | [internal/cluster](../../internal/cluster/) | R-ARCH-017, R-HA-006 |
| Change log, signals, in-process caches | [internal/cluster](../../internal/cluster/), [internal/cache](../../internal/cache/) | R-ARCH-018, R-HA-004 |
| Replica registry | [internal/cluster](../../internal/cluster/) | R-OPS-003, R-HA-003 |

PostgreSQL carries correctness, Valkey carries speed (INV-3). Every Valkey path has a PostgreSQL or polling fallback
with a bound (P-13, P-14, below). The Valkey client itself is described in the `valkey_*.go` files of `internal/cluster`;
this page covers what is built on it.

## Layout

| File | Responsibility |
|---|---|
| `internal/jobs/kinds.go` | `Registry`, `RegisterKind`, `RegisterTask`, `Freeze`; `KindSpec`, `TaskSpec`, `DedupMode` |
| `internal/jobs/queue.go` | `Queue`: `Enqueue` (in the caller's transaction), `EnqueueNow`, `Get`, `Cancel`; exclusive and coalesce keys |
| `internal/jobs/worker.go` | `Worker`: claim loop, handler runs, completion, heartbeat, release; the Component phases `Start`, `Drain`, `Settle`, `Quiesce`, `Stop` |
| `internal/jobs/run.go` | `Run`, the handle a handler gets: `LoadCheckpoint`, `SaveCheckpoint`, `SaveProgress`, `SetResult`, `DB` |
| `internal/jobs/reaper.go` | Reaper pass and retention of finished jobs |
| `internal/jobs/scheduler.go` | Scheduler under the `scheduler` lease; task and schedule-source resolution; `ValidateSchedules` |
| `internal/jobs/cron.go` | `ParseSchedule`, the one cron parser (Q-STOR-4, Q-OPS-7) |
| `internal/jobs/builtin.go` | `RegisterCoordinationKinds`: `cluster-change-prune`, `replica-registry-prune` |
| `internal/jobs/tables.go` | Registers the `job` table (class `data`, R-BAK-004) |
| `internal/cluster/areas.go` | Area registry: `RegisterArea`, `LookupArea`, `Areas` |
| `internal/cluster/locks.go` | Lock categories, `RegisterLease`, `LeaseScheduler`, `LockConfigObject` |
| `internal/cluster/lease_manager.go`, `lease_held.go` | `Leases` (hold loops, check loop), `Held` (writes on the lease's session) |
| `internal/cluster/lease_watch.go` | `Watchdog`, `WatchLoop`, `CallTimeout`: the liveness hooks of the loops |
| `internal/cluster/changelog.go` | `Changes`: start, stop, freshness, listeners, Valkey messages |
| `internal/cluster/changelog_notify.go` | `Changes.Notify`: the two statements of R-ARCH-018 step 1, the post-commit publish |
| `internal/cluster/changelog_poll.go` | The poll and the epoch reconciliation (step 5) |
| `internal/cluster/changelog_prune.go` | `PruneChanges` |
| `internal/cluster/signal_bus.go`, `signal_coalescer.go` | `Bus` (what Valkey must offer), `Signals` (post-commit coalescer, subscriptions) |
| `internal/cluster/replica_registry.go`, `replica_list.go` | `Registry` (register, heartbeat, drain, delete), `ListReplicas`, `PruneReplicas` |
| `internal/cache/` | `Registry`, `Register[K,V]`, `Cache[K,V]`: bounded LRU, SafetyTTL, fill rule |
| `internal/meta/postgres/queries/jobs.sql`, `cluster.sql` | The sqlc queries of the above |

Tables: `job` is registered in [jobs/tables.go](../../internal/jobs/tables.go), `cluster_epoch`, `cluster_change` and
`replica` in [cluster/changelog_tables.go](../../internal/cluster/changelog_tables.go) and
[replica_tables.go](../../internal/cluster/replica_tables.go). The DDL is migration `00004_coordination.sql`; this work
needed no new migration. The revision-column registrations of `cluster_epoch.epoch` and `cluster_change.epoch` stay in
[tables_m0.go](../../internal/meta/postgres/tables_m0.go).

A binary that links `internal/jobs` and `internal/cluster` registers those tables; the registry test of
`test/integration/postgres` links them with [owners_test.go](../../test/integration/postgres/owners_test.go).

## Job framework

### Roles

A `web` replica creates a `jobs.Queue` and enqueues. Only `jobs.NewWorker` claims, and it returns `jobs.ErrNotWorker` for any role
but `worker` and `all`, so a `web` replica's instance ID never reaches `job.locked_by` (R-ARCH-001).
`locked_by` is the replica's `instance_uid`.

### How to add a job kind

1. In the owning package, register the kind at initialisation:

   ```go
   func init() {
       jobs.RegisterKind("index_rebuild", jobs.KindSpec{
           Queue:     "index",           // default "default"; one of jobs.workers.*
           DedupMode: jobs.Coalesce,     // or jobs.Exclusive, or none
           Timeout:   10 * time.Minute,  // per attempt; 0 = none
           Handler:   rebuildIndex,
       })
   }
   ```

   Name pattern `^[a-z][a-z0-9_.:-]{0,62}$`. `MaxAttempts` defaults to 10, `Priority` to 100 (lower runs first). A kind
   registered twice, without a handler, or after `jobs.Freeze()` panics. The composition root calls `jobs.Freeze()` before it
   serves. Every role registers the kinds, because a `web` replica needs them to enqueue.

2. The handler is `func(ctx context.Context, r *jobs.Run) error`. Delivery is at least once, so it must be idempotent.
   Stop when `ctx` ends; `context.Cause(ctx)` says why:

   | Cause | Meaning | What happens to the job |
   |---|---|---|
   | `jobs.ErrReleased` | shutdown (R-ARCH-024 step 4) | released: back to `queued`, `attempt` not counted, checkpoint kept |
   | `jobs.ErrCancelRequested` | `Queue.Cancel` on a running job | ends `cancelled` |
   | `jobs.ErrLeaseLost` | the heartbeat found the row taken, or could not extend it in time | nothing is written; another worker owns it |
   | `jobs.ErrTimeout` | `KindSpec.Timeout` | counts as a failed attempt |

   A returned error fails the attempt: the job goes back to `queued` after `min(2^attempt s, 1 h)` ± 20 % (computed in SQL, on the database's
   clock), or to `failed` when `attempt >= max_attempts`. `jobs.Permanent(err)` fails it at once. A panic is a failed attempt.

3. A long handler checkpoints: `r.SaveCheckpoint(ctx, state)` after every batch, `r.LoadCheckpoint(&state)` at the start. A
   checkpoint is a cursor, not a transaction: it is saved in its own statement on the main pool. `SaveCheckpoint` returns
   `jobs.ErrLeaseLost` when the worker no longer owns the job; return then. A handler that sees `ctx` end saves its checkpoint with
   `context.WithoutCancel(ctx)` and returns `ctx.Err()`; the release follows.

4. Enqueue in the transaction of the change that causes the job:

   ```go
   err := db.InTx(ctx, func(tx meta.Tx) error {
       // ... the change ...
       res, err := queue.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: "index_rebuild", DedupKey: "index:" + repoID, Args: args})
       // res.Created == false: a job with the key exists, res.Job is that job (the API answers 409)
       return err
   })
   ```

   A rollback leaves no job and publishes no wake-up. `Enqueue` needs a `meta.Tx` created by `internal/meta/postgres`.
   `EnqueueNow` runs in a transaction of its own.

   `Exclusive`: no new job while one with the key is queued or running (Q-STOR-13). `Coalesce`: at most one waiting job while another
   runs; jobs with one key never run together. An enqueue with a mode and no key uses the kind's name. A key on a kind without a mode is an error.

5. Add a test that carries the requirement ID in a subtest name (Q-ROAD-4).

### How to add a periodic task

```go
jobs.RegisterKind("storage-summary", jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, Handler: summarise})
jobs.RegisterTask("storage-summary", jobs.TaskSpec{Schedule: "17 * * * *"})
```

The task name is also its kind (override with `TaskSpec.Kind`), and its dedup key is its name, `exclusive`, unless
`TaskSpec.DedupKey` says otherwise (`gc` and `gc-full` share one). `maintenance.tasks.<name>` in the configuration overrides
`enabled` and `schedule`. `Worker.Start` fails naming `maintenance.tasks.<name>.schedule` for a schedule the parser rejects;
every role can call `jobs.ValidateSchedules(cfg.Maintenance)` at configuration load. A task registered in code but absent from the
configuration uses its default; a configured task nobody registered is ignored.

Schedules that live in PostgreSQL (cleanup policies) implement `jobs.ScheduleSource` and go into `WorkerOptions.Sources`.

### Cron dialect

`jobs.ParseSchedule` is the only parser. Five fields in UTC (minute, hour, day of month, month `1-12|JAN-DEC`, day of week
`0-6|SUN-SAT` with 0 = Sunday), `*`, values, ascending ranges, `*/n`, `a-b/n`, lists, and the macros `@yearly @annually @monthly @weekly @daily @midnight @hourly`.
Day matching is POSIX: when neither day field starts with `*`, either may match; otherwise both must. Rejected with
`*jobs.ScheduleError` (`Code() == "invalid_schedule"`, text states the dialect): six or seven fields, `?`, `L`, `W`, `#`, day of week `7`,
descending ranges, `a/n` on a single value, `@every`, `@reboot`, `TZ=` and `CRON_TZ=`, and an expression that does not fire within four years.

### Claim, lease, heartbeat, reaper

| Step | Where | Behaviour |
|---|---|---|
| Claim | `Worker.claim`, query `ClaimJobs` | One statement for all queues with a free slot (`jobs.workers.*`), `FOR UPDATE SKIP LOCKED`, only kinds this replica registered and queues with slots, jobs whose dedup key is not held by a running job. Only while `Ready()` and not draining (Q-OPS-12). |
| Wake-up | signal area `jobs` | `Enqueue` publishes after commit; the claim loop also waits at most `jobs.poll_interval` |
| Heartbeat | `Worker.heartbeat`, control connection | One `UPDATE` per `jobs.heartbeat_interval` for the runs in `Worker.running`, snapshotted under `w.mu`: `WHERE locked_by = uid AND id = ANY(ids) AND attempt = the claimed attempt`. Only snapshot runs are judged: one missing from the result gets `ErrLeaseLost`, one in `cancelling` gets `ErrCancelRequested`. A job claimed while the statement is in flight is not in the snapshot, so it is neither extended nor judged by that beat. A row the replica holds without running it (a claim whose answer never arrived) is not extended: it expires and the reaper returns it. A failing heartbeat cancels a handler only when `jobs.lease_duration` since the last confirmed extension is nearly used up, measured as a duration on the replica's monotonic clock. |
| Reaper | `Worker.Reap`, every `jobs.schedule_tick` on every worker | `locked_until < now()` on the database's clock: `queued` with backoff, `failed` when attempts are used up, `cancelled` for `cancelling` |
| Retention | same pass | Finished jobs older than `jobs.retention`, except the job of each task's latest slot |
| Release | `Worker.Quiesce`, then `settle` | `UPDATE … attempt = attempt - 1` back to `queued`; `cancelling` ends `cancelled` |

Every write that settles or annotates a run (complete, cancel, release, fail, checkpoint, progress, heartbeat) names the owner as `locked_by = uid AND attempt = the
claimed attempt`, so a worker that lost the job, or an older run of it, writes nothing. `Worker.running` is keyed by job ID; a finishing run removes its entry only
if it is still its own (a released job the same worker claims again has a new entry), and the result a handler sets lives on the run, not in a map keyed by ID.
The settle writes (complete, cancel, release, fail) are retried four times with 250 ms doubling backoff inside the settle's 15 s deadline; when they still fail
the log says the write was given up and the lease is left to expire, which the reaper turns into a retry (the attempt counts).

`attempt` counts claims. A release subtracts the interrupted claim, so a drain costs a job its progress since the last checkpoint and nothing else.
A coalesced job that would return to `queued` while another waits ends `cancelled` with `error.code = superseded`.

Liveness: the loops are watched as `jobs.claim`, `jobs.reaper`, `jobs.scheduler` (while the lease is held), `cluster.leases`, `cluster.changes`,
`cluster.signals` and `cluster.registry`. Each call carries a deadline of `min(health.watchdog_timeout / 2, 10 s)` (`cluster.CallTimeout`), and the claim
loop records progress when the replica is unready or every slot is busy. A loop that records progress once per iteration needs its cadence below
`health.watchdog_timeout`: configuration validation rejects `jobs.poll_interval`, `jobs.schedule_tick`, `cluster.epoch_poll_interval`,
`database.lease_check_interval` and `system.heartbeat_interval` at or above it, naming both keys (`Config.validateWatchedLoops` in `internal/config/cluster.go`, called
from the root `Config.Validate`). The rule is the interval alone; an iteration also spends up to one call deadline, so keep real values well below the bound.

### Scheduler

`Worker` holds the `scheduler` lease through `Leaser.Hold`. While held, every `jobs.schedule_tick` `Worker.Tick` reads the database clock, computes
each task's latest due slot with `Schedule.Prev`, and inserts it as `(task, scheduled_for)` with `ON CONFLICT DO NOTHING` on the lease connection (fenced).
A slot refused by `job_task_slot` is already enqueued; one refused by an exclusive key is offered again on every tick until the blocking job ended.
Missed slots are not replayed, only the latest one. A fresh installation therefore enqueues each task's most recent slot at the first tick.

## Leases and lock categories

Leases are session-level advisory locks on a connection of the lease pool (`db.LeasePool()`, `nil` on `web`; `cluster.NewLeases` returns
`ErrNoLeasePool` for it). All advisory locks use the two-`int4` form with a registered first key:

| classid | Constant | Use | Where taken |
|---|---|---|---|
| 1 | `cluster.LockLease` | Lease, second key = registered lease number (`scheduler` = 1) | `lease_manager.go` |
| 2 | `cluster.LockFetchRetired` | Retired, never reused | nowhere |
| 3 | `cluster.LockMigration` | Migration lock, second key 0 | `internal/meta/postgres/migrate.go` (its own `lockMigration`, compared by a test) |
| 4 | `cluster.LockConfig` | Transaction-scoped configuration lock; `cluster.LockConfigObject(ctx, tx, cluster.ConfigDocuments)` | callers in configuration transactions |

[locks_scan_test.go](../../internal/cluster/locks_scan_test.go) scans every Go source and query under `internal/` and `cmd/` and fails on a
one-argument form, an ad-hoc first argument, category 2, or the shared form outside `internal/backup/`.

### How to add a lease

1. `var LeaseFoo = cluster.RegisterLease("foo", 2)` in the owning package (number unique; panics on a clash). Add the row to R-ARCH-017's table.
2. `leases.Hold(LeaseFoo, func(ctx context.Context, h *cluster.Held) { ... })` before or after `Start`. `ctx` ends when the lease is lost or released.
3. Write through `h.DB()` (one statement at a time on the lease connection) or `h.Tx(ctx, func(db q.DBTX) error)`. `Tx` checks the fence first
   (`pg_locks` lists the lock as granted to this backend) and fails with `cluster.ErrNotHeld` after the loss.
4. Work must restart from a persisted cursor; the next run starts fresh.

There is no fencing token. Fencing is "the singleton's writes run on the lease's own session" (R-HA-006): a holder that lost the lock has
no session to write on, and `Tx` proves the lock with `pg_locks` before the first statement. (An `xid8` token was removed: it is not
monotonic across an asynchronous failover and nothing consumed it.) The loss of a lease (failed `SELECT 1` of the check loop every `database.lease_check_interval`, or a
failing statement on a closed connection) cancels `Held.Context()`, counts `trove_lease_lost_total{task}`, and destroys the connection. The holder loop retries
every `cluster.lease_retry_interval`. `Leases.Release(ctx, id)` unlocks at once and stops retrying (the scheduler lease at drain); `Leases.Stop` unlocks all.

## Change areas, signals and caches

### Two classes

| Class | Writes PostgreSQL | Carried by | Use |
|---|---|---|---|
| change | yes: `cluster_epoch` + `cluster_change` | poll (truth) + Valkey `inv` message (early eviction) | configuration, policies, revocations |
| signal | no | Valkey `sig` message, coalesced; consumers also poll | job wake-ups, early evictions of traffic-driven caches |

### How to add a change area

1. A migration (expand) inserts the area's `cluster_epoch` row, in the release that ships the area. `Notify` on an area without a row fails; the poll logs a warning.
2. Add `cluster.RegisterArea(cluster.Area{Name: "authz", Owner: "R-AUTHZ-028", Class: cluster.ChangeArea, PayloadVersion: 1})` to the `init` of `internal/cluster/areas.go`, the only
   place that calls it (`TestAreasAreDeclaredCentrally` fails on any other file). Name `^[a-z][a-z0-9_]{1,31}$`. `TestChangeAreasAreSeeded` (integration) fails when a registered change
   area has no `cluster_epoch` row after the migrations.
3. Write: `changes.Notify(tx, "authz", 1, payload)` inside the writing transaction (a `meta.Tx` of `db.Tx`). The payload is JSON under 1 KiB, never a secret
   (`cluster.ErrPayloadTooLarge` otherwise; send a coarse flush entry instead). The epoch update and the change row run last, just before `COMMIT`; several areas in one
   transaction are updated in name order. After commit the writer applies its own invalidation and publishes the message.
4. Read: register a cache for the area (below), or implement `cluster.Listener` and call `changes.AddListener` before `Start`. `ApplyChange` must be idempotent: it runs
   for the poll, for a Valkey message and for the writer's own commit. Only the poll advances `changes.Applied(area)`.
5. Raising `PayloadVersion` is how a new release writes a new payload shape: replicas that know less flush the area.

The poll (`changelog_poll.go`) applies rows in epoch order and flushes the area (`FlushArea`, reason in parentheses) when the row at the last applied epoch is
missing (`change_log_gap`), a row has a higher payload version (`unknown_payload`), or the history differs: the epoch is lower, equal with another `changed_at`, or the row at the
applied epoch has another `created_at` (`epoch_regressed`). After a flush it takes the database's epoch as its starting point. The poll holds no lock across a
query or a listener call: `areaState.mu` only makes (applied epoch, `changed_at`) one pair for `Changes.AreaState`, so a listener may call `AreaState` from `ApplyChange`
or `FlushArea`. One poll runs at a time (`pollMu`).

### Caches

```go
caches, err := cache.NewRegistry(changes, reg) // once per replica; changes is the cache.Epochs source
changes.AddListener(caches)

tokens := cache.Register[string, Token](caches, "token", cache.Options[string, Token]{
    MaxEntries: 10000,
    SafetyTTL:  5 * time.Minute,               // mandatory
    Areas:      []string{"auth"},
    OnChange: func(c *cache.Cache[string, Token], area string, version int, payload json.RawMessage) {
        c.Invalidate(idFrom(payload))          // without OnChange the whole cache is flushed
    },
})
v, err := tokens.Load(ctx, key, func(ctx context.Context) (Token, error) { return fromDatabase(ctx, key) })
```

`Load` implements the fill rule: it reads `Applied(area)` of every area before the loader, and inserts only if those epochs and the cache's invalidation counter are unchanged afterwards.
`Invalidate`, `InvalidateFunc` and any flush bump the counter, so an early eviction during a fill also stops the insert. `Set` inserts unconditionally (write-through of a value the caller just committed).
A cache with `MaxBytes` needs `Weigh`. Metrics: `trove_cache_requests_total{cache,result}` (`hit`, `miss`, `expired`, `fill_skipped`), `trove_cache_flushes_total{cache,reason}`, `trove_cache_entries`, `trove_cache_bytes`.

### Signals

`signals.Notify(tx, "jobs", payload)` (or `Publish` after the commit) goes to a per-area coalescer: the first signal of an idle area leaves at once, further ones within
`cluster.signal_coalesce_window` leave as one batch, a batch above 1 KiB becomes a coarse signal. Subscribers on the same replica get the batch directly. Without Valkey the signal is dropped and counted
(`trove_cluster_signals_dropped_total{area}`); every consumer polls.

## Replica registry

`cluster.Registry` (one per replica, `web` and `worker`): `Start` upserts the row (retrying until PostgreSQL answers), a loop on the control connection upserts again every
`system.heartbeat_interval` with the snapshot from `RegistryOptions.Snapshot` (state, checks, Valkey view) and `RegistryOptions.Leases`. `Drain` writes `draining`; `Stop` deletes the row;
`StopHeartbeat` leaves it (fault injection). The heartbeat is an upsert, so a row removed by the prune or a restore comes back.

`cluster.ListReplicas(ctx, db, cfg.System)` reads the table on the request pool (`DB.Main`; there is no `Registry.List`); a lost replica lists no leases, so a lease name appears on one live row only. `Lost` is `heartbeat_at < now() - system.heartbeat_stale_after` on PostgreSQL's clock. `PruneReplicas` and `jobs.KindReplicaRegistryPrune`
delete rows stale for 24 h. The HTTP layer converts `cluster.ReplicaInfo` to its own type; `internal/cluster` imports no edge package.

## Bounds P-13 and P-14

| Bound | Spec | Test | Measured (this machine, `-race`) |
|---|---|---|---|
| Change propagation with Valkey p99 ≤ 100 ms | P-13 | `TestChangePropagation`, with poll 2 s, so only the message can explain it | p99 7 to 14 ms; one run under load 76 ms |
| Change propagation without Valkey p99 ≤ `epoch_poll_interval` + 1 s | P-13 | same, Sentinel address that does not answer, poll 1 s | p99 about 1.0 s |
| Job pick-up with Valkey p99 ≤ 1 s | P-14 | `TestJobPickup`, with `jobs.poll_interval` 5 s | p99 29 ms |
| Job pick-up without Valkey p99 ≤ `poll_interval` + 1 s | P-14 | same, poll 1 s | p99 1.02 s |

"Valkey unavailable" is a client with a Sentinel address that refuses connections: it constructs degraded and `Available()` is false, as at a real outage. With fewer than 100 samples p99 is the maximum.

## Tests

Unit tests next to the code (cron table, registry rules, fill rule, notify ordering, signal coalescing, lock-category scan). Integration tests in
[test/integration/coordination](../../test/integration/coordination/) (`//go:build integration`, own database per test, replicas as in-process objects with pools of their own):

```
set -a; . .dev/integration/env; set +a
go test -tags integration -race ./test/integration/coordination/...
```

| File | Covers |
|---|---|
| `leases_test.go` | one holder across two workers, web holds no lease connection, session termination, fencing, explicit release |
| `jobs_test.go` | web never in `locked_by`, exclusive and coalesce keys, backoff, attempts, reaper, lost lease, cancel, readiness gate, unknown kinds |
| `latency_test.go` | P-13, P-14 |
| `changes_test.go` | epoch order, regression, reused epoch, gap, unknown payload, prune, `PollNow`, the fill rule against the log |
| `heartbeat_test.go` | the heartbeat judges and extends only the runs of its worker: a job claimed while a heartbeat is in flight, a row held without a run |
| `areas_test.go` | every registered change area has its `cluster_epoch` row after the migrations |
| `registry_test.go` | registration, heartbeat, lost (no leases listed), replacement, draining, prune; `TestReplicaLostWithDefaults` waits 30 s with the shipped values |
| `scheduler_test.go` | one scheduler, no duplicate slot across a hand-over, retention exception, postponed slot, drain releases the lease |
| `drain_test.go` | E0.14 on two replicas: cursor every 100 ms, graceful shutdown, resume with `attempt` unchanged |
| `watchdog_test.go` | claim held by `pg_sleep` past the watchdog bound |

## Not covered here

- The Kubernetes parts of E0.6, E0.13 and E0.14 (force-deleted pod, `dev-failover`, k6 under a node drain) belong to the e2e harness.
- A PostgreSQL failover under load and Valkey failover during propagation are not exercised at this level; the poll's behaviour after one is covered by the regression tests.
- The wiring in `internal/app` is described in the work package summary and not yet in the code (WIRING POINT comments in `app.go`).
