# Blob store

Content-addressed storage on an S3-compatible bucket: [internal/blob](../../internal/blob/) (everything that does not
depend on an S3 client), [internal/blob/s3](../../internal/blob/s3/) (the AWS SDK adapter, the only package that imports
the SDK), [internal/blob/blobtest](../../internal/blob/blobtest/) (an in-memory store with fault injection for tests).
Requirements: R-STOR-001 to R-STOR-006, R-STOR-013, R-STOR-024, R-STOR-025, R-STOR-030 to R-STOR-035, R-STOR-046.
The row is the authority on whether a blob exists; the object is the content. Nothing in this tree uses Valkey or a
session-level advisory lock (R-STOR-046, enforced by [internal/archtest](../../internal/archtest/)). The first caller of `Ingest`,
`Commit` and `Open` is the files transfer ([files.md](files.md)): its link function is the item insert or replace, and a download opens
the verifying reader or a range.

## Files

| File | Responsibility |
|---|---|
| [key.go](../../internal/blob/key.go) | `Digest`, `Key(prefix, digest)`, `ParseKey`, `Digests` (the four digests and the size) |
| [objects.go](../../internal/blob/objects.go) | `ObjectStore` interface, `StoreError` and its kinds, `ErrNotFound`, `ErrCircuitOpen` |
| [spool.go](../../internal/blob/spool.go) | `Spool`: admission, the budget, spool files, the single hashing pass, cleanup |
| [store.go](../../internal/blob/store.go), [commit.go](../../internal/blob/commit.go) | `Store`, `Ingest`, `Commit` (the commit protocol), `Lookup` |
| [transfer.go](../../internal/blob/transfer.go) | single `PutObject` or multipart from the spool file, part sizing, abort |
| [staged.go](../../internal/blob/staged.go), [spool_segment.go](../../internal/blob/spool_segment.go) | the staging area of the upload sessions: `SessionKey`, `Staged` (a blob that is in the bucket already, committed by a server-side copy), `Spool.IngestSegment` (one part of a request, behind the recorded tail, through the session's hash functions) |
| [gc_uploads.go](../../internal/blob/gc_uploads.go) | the session step of a collection run: expiry, `RemoveSession`, the sweep of `_uploads/` |
| [download.go](../../internal/blob/download.go) | `Store.Open` and the verifying reader (hold-back, `content_status`), the mark of a missing object (`markMissing`) and its recheck (`RecheckMissing`) |
| [guard.go](../../internal/blob/guard.go) | `Guard`: circuit breaker and request metrics around any `ObjectStore` |
| [gc.go](../../internal/blob/gc.go), [gcjob.go](../../internal/blob/gcjob.go) | `Collector` (per-blob protocol, hand-over, pause, hold, fault hook) and the job adapter |
| [usagefold.go](../../internal/blob/usagefold.go) | `UsageFolder`: the fold loop of the usage counters (R-STOR-003) and its `Fold` iteration |
| [conformance.go](../../internal/blob/conformance.go) | startup check, conformance checks, `AwaitStartupCheck` |
| [sql.go](../../internal/blob/sql.go), [tables.go](../../internal/blob/tables.go), [metrics.go](../../internal/blob/metrics.go), [errors.go](../../internal/blob/errors.go) | the `DB` interface, the `blobRows` seam and its sqlc implementation, export-class registration, metric families, `errs` mapping |
| [restoreversions/restoreversions.go](../../internal/blob/restoreversions/restoreversions.go), [s3/versions.go](../../internal/blob/s3/versions.go) | `trove admin storage restore-versions` (R-HA-025 step 5, [backup.md](backup.md#the-platform-layer-and-the-point-in-time-drill)): runs `verify`, lists the versions of each key without an object (`Client.ListVersions`), removes the delete markers younger than *t* (`Client.DeleteVersion`); never an object version |
| [verify/verify.go](../../internal/blob/verify/verify.go), [verify/pg.go](../../internal/blob/verify/pg.go) | `trove admin storage verify` ([below](#storage-verify-r-stor-027-q-stor-200)): `Run` over a `Source` (the pages and the items) and an `Objects` (`Head`), `PGSource` |
| [s3/client.go](../../internal/blob/s3/client.go) | `s3.Client` (the `ObjectStore`), `NewFromFactory` |
| [s3/credentials.go](../../internal/blob/s3/credentials.go) | credential sources of R-STOR-031, SDK configuration |
| [s3/errors.go](../../internal/blob/s3/errors.go), [s3/idle.go](../../internal/blob/s3/idle.go) | SDK error classification; stall detection |

## Key layout and digests (R-STOR-001, R-STOR-002)

`blob.Key("trove", d)` is `trove/sha256/<h0h1>/<h2h3>/<hex>`; with an empty prefix the key starts at `sha256/`. The
function takes a 32-byte digest and nothing else, so no repository, path or user input reaches a key. `ParseKey` is its
inverse and refuses every key that does not follow the layout exactly (the orphan sweep will report those as foreign).

`Spool.Ingest` computes SHA-256, SHA-1, MD5 and SHA-512 and the size in one pass over the body (one `Read` loop fanning
out to four hash states and the spool file). SHA-512 is computed for every upload (Q-STOR-2). SHA-256 is the identity;
SHA-1 and MD5 are stored for client compatibility only.

## Upload pipeline (R-STOR-004)

```text
Store.Ingest(body, IngestOptions) ─▶ *Spooled ─▶ Store.Commit(spooled, link) ─▶ spooled.Close()
```

- Admission happens before the first byte is read: a declared length above `min(IngestOptions.MaxSize,
  storage.upload.max_size)` is `413` (`ErrTooLarge`), no budget or too little free space on the spool volume is `503`
  with `Retry-After` (`ErrSpoolFull`). A body of unknown length reserves budget in 4 MiB steps (just what it needs when
  a step does not fit) and fails `413` past the maximum or `503` when the next step cannot be reserved.
- Bodies of known length up to `storage.spool.memory_threshold` stay in memory and use no budget; every other body goes
  to a new file, mode `0600`, named by a random UUID, in `storage.spool.dir`.
- After the body: byte count against `Content-Length` (`400`, `ErrLengthMismatch`), declared digests (`409`,
  `ErrDigestMismatch`, `Expected` carries SHA-256, SHA-512, SHA-1, MD5).
- `NewSpool` deletes only what the spool creates (regular files named by a v4 UUID) at start and refuses to start if
  the directory holds anything else (the error names the entry; `lost+found` of a fresh volume is tolerated), so a
  misconfigured `storage.spool.dir` cannot wipe a data directory. `Spooled.Close` removes the file and returns the
  budget; it is idempotent, and `Ingest` closes its own result on every error path. The caller closes after `Commit`.
- The free-space probe is `statfs` (`spool_unix.go`); on a platform without it the `min_free_bytes` check is skipped.

All errors are `*errs.Error` values with the sentinel as cause (`errors.Is(err, blob.ErrSpoolFull)`).

## Commit protocol (R-STOR-005)

`Store.Commit(ctx, spooled, link)` runs the loop of the specification; `link` runs in the transaction that holds the
row's `FOR SHARE` lock and adds the reference (item insert, hooks). `Store.CommitAll(ctx, []*Spooled, link)` is the same loop over several blobs
and one `link` (a request that writes more than one file, [items.md](items.md)): blobs of one content are one blob, the rows are locked and inserted in digest
order (two commits over overlapping sets never wait on each other's insert in opposite orders), a round in which any blob needs a write, a repair or a wait is rolled back and
the next round starts after all of them were served, and a failure queues every digest that was written. `Commit` is `CommitAll` of one. The statements are sqlc queries in
[queries/blob.sql](../../internal/meta/postgres/queries/blob.sql), run on the transaction behind `meta.Tx`
(`postgres.Unwrap` + `q.New`, [sql.go](../../internal/blob/sql.go)); `meta.Tx` itself cannot run SQL. `Store` needs
only `DB.InTx(ctx, func(meta.Tx) error)`, which `*postgres.DB` has. The commit and download paths reach the table
through the small `blobRows` interface so that the unit tests can simulate it (`WithRows` in `export_test.go`); the
collector has no such seam.

The row is inserted for an object no row announced only within `min(commit_deadline, delete_settle / 2)` of the end
of the object write. Longer ago, a collector may have deleted the key and dropped a row we never saw, so the
object is written again. `delete_settle` therefore has a minimum of 1 s.

| Row state seen | Action |
|---|---|
| no row, object not written (or written longer than `commit_deadline` ago) | roll back, write the object, loop |
| no row, object written | `INSERT … ON CONFLICT DO NOTHING`; 0 rows means a concurrent winner, roll back and loop |
| `deleting` | roll back, forget the write, poll until the row is gone (`blob_wait_timeout`, else `503`), loop |
| `live`, `content_status <> 'ok'` | roll back, write the object, mark `ok`, loop |
| `live` | run `link`, commit (a deduplication hit when this call wrote nothing) |

SQLSTATE `AS001` from `link` (the guard trigger saw a row that is not live) restarts the loop. After
`storage.upload.commit_attempts` rounds the error is `503` (`ErrCommitAttempts`). A failure after the object write
queues the digest in `blob_gc_queue`, so the incremental collection finds the orphan. `CommitResult` says whether this
call wrote the object, inserted the row, or deduplicated.

## Transfer (R-STOR-006)

Up to `storage.s3.multipart.threshold`: one `PutObject`. Above it, trove's own multipart upload (Q-ARCH-4): part size is
`part_size`, raised so the content fits 10,000 parts (whole MiB, `blob.PartSize`); each part is an
`io.SectionReader` on the spool file; `multipart.concurrency` parts per upload, `multipart.max_concurrent_parts` per
`Store`. Any failure or cancellation aborts the upload with a context of its own. After `CompleteMultipartUpload` the
object size is checked with `HeadObject`. The payload checksum is `storage.s3.payload_checksum`: for a single request
SHA-256 or MD5 of the whole content (known from the hashing pass), for parts the digest of the part (computed by one
extra read of the part from the spool file) and `ChecksumAlgorithm: SHA256` on `CreateMultipartUpload`.

## Object-store client (R-STOR-030 to R-STOR-033)

`s3.NewFromFactory(ctx, cfg.S3, outboundFactory)` takes its HTTP client from `internal/outbound` (purpose
`objectstore`) with the connect and response-header timeouts and the proxy of `storage.s3.http`. Retries are the SDK's
standard retryer (`retry.max_attempts`, `retry.max_backoff`). A transfer in which no byte moves for
`http.idle_transfer_timeout` is cancelled (`idle.go`). Credentials (`credentials.source`):

| Source | Behavior | Tested |
|---|---|---|
| `static` | key files read again at most once per second; a rotated Secret applies without restart | unit (rotation against a fake endpoint), integration |
| `webIdentity` | `stscreds` web identity provider at `sts_endpoint` | unit against a fake STS; not against a real STS |
| `default` | `config.LoadDefaultConfig` (environment, shared files, IRSA, Pod Identity, instance metadata) | not tested here |

`role_arn` with `default` or `static` assumes the role on top; that path is not tested. Errors are classified in
`s3/errors.go`: a missing key is `ErrNotFound`; `AccessDenied`, `NoSuchBucket`, bad credentials are `KindConfig`;
5xx, throttling, no answer and credential retrieval failures are `KindTransient`; other 4xx are `KindOther`.

`blob.NewGuard(store, cfg.S3.CircuitBreaker, …)` wraps any `ObjectStore`: `failure_threshold` consecutive transient
failures open the circuit for `open_duration`, then one probe request goes through. Config errors, 404 and other 4xx
are answers, not failures; a cancelled caller is no evidence. Metrics: `trove_storage_s3_requests_total{operation,outcome}`
(the specification writes `op`, which R-OPS-007 does not allow), `trove_storage_s3_request_duration_seconds`,
`trove_storage_s3_bytes_total{direction}`, `trove_storage_s3_circuit_open`. The composition root builds
`blob.NewMetrics(m)` once and passes it to the spool, the guard and the collector.

Endpoint: a `storage.s3.endpoint` (and `presign.endpoint`) without a scheme is a host and means `https://`
(Q-STOR-15). `config.NormalizeEndpoint` does it once at load (`Config.Finalize`, source `derived`) and again in
`s3.New`, so a client built from a hand-made config agrees. Plaintext is always spelled `http://` with
`allow_insecure_endpoint`; the flag never changes a scheme-less host.

TLS: an `https://` endpoint verifies against the factory's trust store (system roots plus
`proxy.outbound.tls.ca_files`), or, with `storage.s3.ca_file` set, against that bundle only: `NewFromFactory` builds a
trust store of its own (`outbound.Factory.TrustStore` + `outbound.WithRootCAs`), read again like the factory's, so a
rotated CA applies to the next connection without a restart (R-ARCH-035). A `ca_file` that does not load at start is a
configuration error naming the key. `Client.HTTPClient()` is the client the store is reached with; a caller that
fetches a presigned URL itself (the conformance check) uses it, so the fetch trusts the same CA.

Gaps against the specification, all visible at construction (nothing is silently ignored):

- `storage.s3.http.proxy_credentials_file`: `NewFromFactory` refuses it. `outbound.WithProxyURL` sends only the user information of the proxy URL, which the configuration forbids (R-OPS-013: URL keys carry no credentials), and a file read once would not follow a rotation. Use a proxy that authenticates by source address until the outbound layer takes a re-read proxy credential.
- `http.max_connections`, `tls_handshake_timeout`, `idle_conn_timeout`: the outbound factory has
  no options for them, so its defaults apply (`proxy_url` and `no_proxy` as host patterns are passed through; CIDR
  patterns in `no_proxy` are not).
- `trove_storage_s3_requests_total` counts calls of the `ObjectStore` interface, not the SDK's retry attempts.

## Download verification (R-STOR-013, Q-STOR-6)

`Store.Open(ctx, digest, size, OpenOptions)` returns a reader. A full read is verified unless
`storage.download.verify` is false and the caller did not set `OpenOptions.Verify` (the UI's shared asset store sets it, Q-UI-143): SHA-256 is computed while reading and the last `hold_back` bytes are not released
before the body ended and the digest matched. On a mismatch the reader returns `ErrCorrupt` instead of the held bytes
(the HTTP layer aborts the response). Only evidence about the object marks the blob `corrupt`: an object whose size
differs from the row's, or a body read to its end with another digest. A transport error aborts the read but changes no
status. `WithDamageHook` is where the audit event hangs; the composition root binds none yet, so no detection writes one. A range is
returned as stored (H-11).

A store that answers "no such object" (`ErrNotFound`), for a full or a ranged read, marks the blob `missing` (`markMissing`,
R-HA-024, [Q-STOR-151](../spec/storage.md#q-stor-151--a-read-that-finds-no-object-marks-the-blob-missing-and-looks-once-more-after-the-mark)):
`MarkBlobDamaged` changes only an `ok` row, so the detection is counted (`trove_storage_blobs_damaged_total{status="missing"}`, the alert
`TroveBlobMissing`) and logged with the digest once per blob. Then the object is looked for once more (`HeadObject`): a commit of the same
content that wrote it again between the read and the mark would otherwise be undone, so a row whose object is there again is set `ok`. The
error stays `ErrNotFound`; `items.Store.Open` answers it `503 content_unavailable`, and a remote repository fills the item again
([proxy.md](proxy.md#lost-content-r-stor-027-q-repo-202-to-q-repo-204)). The mark is cleared by the commit protocol's repair (the next
commit of the same content writes the object, table above), and it heals by itself
([Q-STOR-153](../spec/storage.md#q-stor-153--a-missing-mark-heals-by-itself-a-read-asks-the-store-again-once-per-blob-and-30-s)):
`Store.RecheckMissing(digest, size)`, which every read of a blob marked `missing` calls through `items.Store.Usable` (`/files/`, the format
host's `Serve`, the remote engine), claims the one look of `MissingRecheckInterval` (30 s) with `ClaimMissingRecheck`, a conditional `UPDATE`
of the row's `checked_at` that moves it only when it is that old, so one read per blob and interval asks whatever the number of replicas.
The claimer sends `HeadObject`; an object of the row's size sets `ok` (`MarkBlobOK`) and the read serves it; anything else leaves the mark,
and the moved `checked_at` starts the interval again. `MarkBlobDamaged` sets `checked_at` too, so the interval starts at the detection.
`checked_at` is the column R-STOR-027's scrub orders its existence checks by; no migration.

What a download holds ([Q-ARCH-241](../spec/architecture.md#q-arch-241--a-download-holds-one-buffer-of-its-own-size-and-the-object-store-sends-no-more-than-one-window-ahead)):

- The verifying reader has one buffer (`verifyBuffer`): `buffer_size`, at least `2 × hold_back` and 4 KiB, at most the object's
  size plus one byte (the byte that would show a longer object). It is taken on the first read from `Buffer`, pools of
  power-of-two classes from 4 KiB to 1 GiB ([buffers.go](../../internal/blob/buffers.go)), and given back by `Close`; a read after
  `Close` fails.
- `fill` moves the withheld tail to the front and reads until the buffer is full or the body ended; everything but the last
  `hold_back` bytes is then cleared. With the minimum of twice the hold-back, a refill clears at least as much as it moves. A body
  longer than the row has nothing more cleared and is read to its end, where the digest check fails.
- It implements `io.WriterTo`, and `edge.ServeItem` uses it, so a verified download needs no copy buffer. Any other body (a range,
  `verify: false`) is copied through a pooled buffer of at most `buffer_size` and its length.
- `blob.DownloadBufferBytes` is the class of the full buffer, the unit of the memory guard's request bytes;
  `blob.DownloadCost(cfg, size)` is what a download of that size holds (its buffer class plus `min(size, receive_window)`), which
  `Store.Open` hands to the request's memory charge through `blob.WithMemory` before it asks the object store
  ([architecture.md](architecture.md#stage-4-and-the-memory-guard), Q-ARCH-328).
- The object-store client's HTTP/2 receive window per stream is `storage.s3.http.receive_window` (`outbound.WithStreamReceiveWindow`);
  without it Go's transport lets the store send 4 MiB of an object ahead of a slow reader.

## Staged blobs and the upload sessions (R-STOR-011)

The service that holds the sessions is [internal/uploads](../../internal/uploads/) (see [oci.md](oci.md#upload-sessions-r-stor-011)); this package
gives it three things. `ObjectStore.Copy(src, dst, size)` is a server-side copy (`CopyObject` up to 5 GiB, `UploadPartCopy` parts of 1 GiB
above, the encryption of every write; [s3/copy.go](../../internal/blob/s3/copy.go)). `blob.Staged(digests, key)` returns a `*Spooled` whose
content is the object at `key`: `Commit` and `CommitAll` take it like any spooled body, and the only difference is the protocol's object
write (`putStaged`: the copy to the content address, then a size check), which a deduplication hit never runs. If the staging object is gone
the commit fails with `ErrStagingGone`. A staged body has no reader (its `Reader` fails) and no spool budget.
`Spool.IngestSegment(ctx, prefix, body, n, tee)` reads at most `n` bytes into a `*Spooled` (memory below the spool's threshold, a spool file
above it, its budget reserved as the bytes arrive), the bytes of `prefix` first (the recorded tail, which the session's hash state has seen
already) and then `body`, each byte of which goes through `tee` before it is kept; the result carries the segment's SHA-256 and MD5, the
payload checksums of a part.

## Garbage collection (R-STOR-024, R-STOR-025)

`blob.NewCollector(cfg.GC, prefix, objects, db)`; `Run(ctx, RunOptions{Mode, DryRun, Cursor, Checkpoint})` returns a
`GCResult` (`dryRun, trashPurged, candidates, kept, held, deleted, bytesReclaimed, errors, paused`, and the counters of the
session step: `uploadSessionsAborted, uploadSessionsCleaned, uploadObjectsDeleted, uploadsAborted`). A run:

0. with `WithUploadSessions(cfg.Uploads)` set, runs the session step ([gc_uploads.go](../../internal/blob/gc_uploads.go)): it aborts the
   sessions that are idle for `session_ttl` or (open ones) older than `session_max_age`, removes the multipart upload and the objects of the
   aborted ones and, once their idle time has passed, of the finished ones, deletes their rows, and in a full sweep removes what no session row
   stands for below `<prefix>/_uploads/` ([Q-STOR-170](../spec/storage.md#q-stor-170--the-collection-run-aborts-a-live-session-for-its-idle-time-and-an-open-one-for-its-age),
   [Q-STOR-172](../spec/storage.md#q-stor-172--the-full-collection-sweep-removes-what-no-session-stands-for-until-the-orphan-sweep-is-a-task-of-its-own)).
   The step takes at most 25 batches of 200 per run, lists the staging area in pages (the optional `MultipartPager` of the store, 500 uploads at a time), re-reads the deletion pause before every batch, keeps the position of the staging sweep in `system_state` (`upload_sweep_cursor`) so that successive runs progress, and counts the sessions it expires in `trove_storage_upload_sessions_total{outcome="expired"}` ([Q-STOR-173](../spec/storage.md#q-stor-173--the-sessions-are-counted-by-trove_storage_upload_sessions_total-not-by-an-oci-metric)).
   A failure of the step is counted in `errors` and logged; it does not stop the blob collection below it. A dry run counts the sessions it
   would abort and changes nothing;
1. reads the deletion pause (`system_state` key `deletions_paused_at`; see below) and ends with `paused: true` if set;
2. finishes `deleting` rows left by earlier runs: step (6) once `delete_settle` has passed since the delete, steps
   (4) to (6) for a row nobody finished within `handover_delay`. Such a row is taken over with a compare-and-set
   (`UPDATE … SET state_changed_at = now() … RETURNING`), so of two runs that read it one takes it and the other
   finds it fresh; the winner deletes under the same hand-over deadline as for its own claims;
3. reads candidates in keyset batches of `batch_size` (incremental: `blob_gc_queue` entries older than `grace_period`;
   full: live blobs older than the grace period, anti-joined against every table with a foreign key to `blobs`, read
   from `pg_constraint` once per batch, resuming after `Cursor`; a table added by a migration during one batch is
   seen from the next batch on) and runs the per-blob protocol, one transaction each: `FOR UPDATE`,
   then a separate reference-check statement, then `UPDATE … 'deleting'` and the queue delete guarded by `$seen`;
4. deletes the claimed objects (`DeleteObjects` when `batch_delete`, else `delete_concurrency` parallel `DeleteObject`)
   only while less than `handover_delay − delete_settle` has passed on the collector's own clock since the reading
   before the earliest claim, with that instant as the call's deadline; records `object_deleted_at`;
5. waits up to `delete_settle` (plus two polls) to drop the rows it deleted; the wait ends with a pass that started after its deadline
   ([Q-STOR-216](../spec/storage.md#q-stor-216--the-settle-wait-of-a-collection-run-ends-with-a-pass-that-started-after-its-deadline)).

On cancellation (a drain) the run cancels its S3 calls, waits for them, records the deletes that succeeded and sets
`state_changed_at = now() − (handover_delay − delete_settle)` on its other claims, so the run that continues finishes
them after `delete_settle`. A crash needs none of this: the rows wait for `handover_delay`.

Extension points on `Collector`: `Hold` (the backup hold of step 2b, runs in the claim transaction; nil holds nothing),
`Paused` (replaceable; the default reads the key-value row `system_state.deletions_paused_at` because `system_state`
in migration 00001 is key-value, not the column the sketch in R-HA-025 shows) and `Fault` (a test hook called at named
points, `claim:before|locked|checked|committed`, `delete:before|after`, `record:after`, `finish:delete`, `drop:before`, `settle:counted`;
a non-nil error ends the run at once as a process kill would, with `errors.Is(err, blob.ErrFault)`).

Not in this package yet: the trash purge that R-STOR-025 puts at the start of a run (its package does not exist; the job handler calls
it before `Run` and puts the count in `trashPurged`), the orphan sweep of `sha256/` (R-STOR-026; its pass over `_uploads/` is the
session step) and the scrub (R-STOR-027).

### Job registration

[gcjob.go](../../internal/blob/gcjob.go) holds `Collector.RunJob(ctx, mode, args, run)`, the body of the handlers; it
takes the narrow `JobRun` interface (`*jobs.Run` implements it) so that this infrastructure package does not import the
domain package `internal/jobs`. The kinds and tasks are registered where both are visible, as
`jobs.RegisterCoordinationKinds` does for the cluster: the comment at the top of gcjob.go shows the four
registrations (`gc` and `gc-full`, queue `maintenance`, exclusive, both tasks with dedup key `gc`).

## Usage fold (R-STOR-003)

The statement-level triggers of migration 00003 append to `storage_usage_deltas`; nothing else writes `repository_usage` or
`storage_usage_totals`. [usagefold.go](../../internal/blob/usagefold.go) is the loop that folds them, built in `wireBlob`
([wire_blob.go](../../internal/app/wire_blob.go)) on every role that runs job loops (`worker`, `all`) and started as a component:

- `Fold(ctx)` is one iteration and one `InTx` on the main pool: `FoldTakeUsageDeltas` (`DELETE ... WHERE id IN (SELECT ... ORDER BY id
  LIMIT 10000 FOR UPDATE SKIP LOCKED) RETURNING`, grouped by repository, `ORDER BY repo_id NULLS LAST`), `FoldUpsertRepositoryUsage`
  (one statement over the groups in that order, joined with `repositories ... FOR KEY SHARE`, so the deltas of a deleted repository
  are dropped and the repository cannot go before the insert), then `FoldAddPhysicalUsage` for the group without a repository, last.
  The statements are in [usage.sql](../../internal/meta/postgres/queries/usage.sql). `repository_usage.updated_at` is the transaction's
  `now()`: the mirror's byte bound compares its own stores with it, and a transaction start is never later than the snapshot that
  took the rows, so the bound over-counts at worst and never under-counts ([bounds.go](../../internal/mirror/bounds.go)).
- The loop runs `Fold`, runs it again at once while it took `UsageFoldBatch` (10,000) rows, and otherwise waits
  `storage.usage.fold_interval`. A failed iteration is logged at warn level and repeated at the next interval. There is no lease and
  no Valkey lock: concurrent folds of any number of replicas take disjoint rows and lock the counter rows in one order. A deadlock
  would be retried by `InTx` and not show in the loop, which is why the integration test reads `pg_stat_database.deadlocks`.
- Drain: `Drain` stops new iterations, `Settle` waits for the one in flight, `Quiesce` (5 s before the deadline) cancels it and its
  transaction rolls back; its rows are folded by another replica. The loop is watched as `blob.usage_fold`, each iteration has the
  call deadline of `cluster.CallTimeout`, and the wait beats so a long interval is no stuck loop.
- No metric: the specification names none. A failure shows in the log; the backlog is `SELECT count(*) FROM storage_usage_deltas`.
- Readers that must be exact add the unfolded deltas to the folded rows in one statement (`pgEnv.exact` in
  [usagefold_test.go](../../test/integration/blob/usagefold_test.go) is the pattern); quota admission and the storage summary read
  the folded rows and lag by one interval plus one iteration. `storage_usage_recount()` of R-STOR-047 is not built.

### The figure of a project (R-STOR-049, Q-STOR-158)

A project keeps no counter of its own. `ProjectList` and `ProjectGet` ([project.sql](../../internal/meta/postgres/queries/project.sql))
add a column `logical_bytes`: `sum(repository_usage.logical_bytes)` over the repositories whose `project_id` is the project, a join
by primary key, so no item is scanned and a repository that moves ([move.go](../../internal/project/move.go)) takes its share with
it, with no bookkeeping. `project.Project.LogicalBytes` carries it and `toProject` in
[projects_api.go](../../internal/api/v1/projects_api.go) serves it as `logicalBytes`; it lags a write like the repository counters do.
It counts untrashed rows; when the trash can is built, the trashed bytes need a counter of their own in the deltas and in
`repository_usage` (Q-STOR-158 names the condition). The physical figure of the dashboard is `storage_usage_totals`
(`OverviewStorage`), unrelated to this sum. The integration test is
[storage_test.go](../../test/integration/projects/storage_test.go): it seeds blobs and items, folds with `blob.UsageFolder` and reads
the figure over HTTP.

## Storage verify (R-STOR-027, Q-STOR-200)

`trove admin storage verify` ([cmd/trove/admin_storage.go](../../cmd/trove/admin_storage.go)) checks that the object of every `live` blob row is in the store.
It is a command of the binary like `admin backup check`: it loads the configuration without a role, opens the database in the `web` role and the S3 client
of `storage.s3` (the runtime credential, so it needs no permission the replicas lack), and never opens Valkey, so it runs in any pod of the installation
(`kubectl exec deploy/trove-worker -- trove admin storage verify`) and, with the replicas held not ready, in a one-off pod of the release's configuration.

| Part | Responsibility |
|---|---|
| `verify.Run` | Pages through `blobs` by digest (`VerifyBlobPage`, 1,000 rows, keyset on `sha256`), so memory is one page and the findings; runs up to `--concurrency` `Head` calls per page; one `Head` per row, three attempts with a 1 s and 2 s back-off for an error that is not "no such object"; a `KindConfig` error (`AccessDenied`) ends the run |
| `verify.PGSource` | The two readers of the run (`db.Read`, one short read each, no transaction held over the slow checks): the page and the items of a damaged blob (`VerifyBlobItems`, `VerifyBlobItemCount`, [blob.sql](../../internal/meta/postgres/queries/blob.sql)) |
| `verify.Report` | `Checked`, `Missing`, `SizeMismatch`, `Findings` (with up to `--max-items` items each, trashed ones marked), `Unchecked`, `ItemsAffected`; `--json` prints it whole |
| `writeVerifyReport` | The text: one line per item of a damaged blob, a line for a damaged blob without an item, one per blob that was not checked, the totals last (the line the e2e tests read) |

It writes nothing: no `content_status`, no audit event. Exit codes follow [R-BAK-013](../spec/backup-and-restore.md#r-bak-013--exit-codes-interruption-and-resumption):
`5` for a finding, `75` when only unchecked blobs remain, `2` for usage and configuration, `1` otherwise. A caller in Go (the post-restore step of R-HA-025,
which prints a count from) calls `verify.Run` and reads `Report.Missing` and `Report.SizeMismatch`. Tests: the unit tests in
[verify_test.go](../../internal/blob/verify/verify_test.go) (pages, findings, retries, the cut-off) and [test/integration/storageverify](../../test/integration/storageverify/verify_test.go)
(real PostgreSQL and S3: objects deleted behind the installation's back, a trashed item, `--since`); the end-to-end use is `TestColdCrash` and `TestNPMHACrash`
([e2e.md](e2e.md#a-replica-killed-during-a-cold-install)).

## Conformance check (R-STOR-035)

`blob.StartupCheck` writes, reads back, compares and deletes `<prefix>/_probe/<replica>` and lists
`<prefix>/sha256/` with one key. `blob.AwaitStartupCheck` repeats it: a configuration error (`AccessDenied`,
`NoSuchBucket`, bad credentials) is returned at once, a transient error or an open circuit is retried with exponential
backoff capped at 30 s and logged once per minute, and the call returns only when the check passed or the context ended.
`blob.RunConformance` runs `blob.ConformanceChecks()`, which also cover: a missing key reads as not found, overwrite
and delete visibility to the next read and list (A1), concurrent writes of identical bytes (A6), a ranged read, a
multipart object invisible until completion and complete after, an aborted upload leaving neither object nor listed
upload (A2), a completion that assembles the part it names and refuses an ETag the store never issued (a part number sent twice, A2), a server-side copy that replaces its destination and reports a missing source as not found (the object write of
a session's finalize, R-STOR-011), `DeleteObjects` (optional: its failure names `storage.gc.batch_delete`) and a presigned
URL. Everything it writes lies below `<prefix>/_probe/` and is removed. Conditional writes are not checked: trove uses none.

The command `trove admin storage check` is not written yet; it is `RunConformance` plus a printed report.

### Running it against another S3

The integration tests are the check. `TestConformance` needs only the `TROVE_TEST_S3_*` variables, no PostgreSQL and no
Valkey:

```sh
export TROVE_TEST_S3_ENDPOINT=https://s3.example.com        # example
export TROVE_TEST_S3_REGION=eu-central-1                    # example
export TROVE_TEST_S3_ACCESS_KEY=… TROVE_TEST_S3_SECRET_KEY=…
export TROVE_TEST_S3_BUCKET_BLOBS=trove-conformance         # example; the test writes below it/<run id>/ and removes it
export TROVE_TEST_S3_FORCE_PATH_STYLE=true                  # false for virtual-hosted addressing
go test -tags integration -run Conformance -v ./test/integration/blob/...
```

Store-specific settings (all optional): `TROVE_TEST_S3_PAYLOAD_CHECKSUM` (`sha256` default, `md5`, `none`),
`TROVE_TEST_S3_REQUEST_CHECKSUM` and `TROVE_TEST_S3_RESPONSE_CHECKSUM` (`when_supported` default, `when_required`),
`TROVE_TEST_S3_BATCH_DELETE` (`true` default). Against SeaweedFS (the stack) the checks pass with the defaults, with
`md5`, and with `none` plus `when_required`. The stack's gateway is https with a private CA, which the suite passes as
`storage.s3.ca_file`; the presigned fetch must use `Client.HTTPClient()` (or a client with `WithRootCAs`) to trust it. SeaweedFS creates a missing bucket on the first write, so a wrong bucket name is
not an error there; the credential test uses a wrong secret key instead.

## Tests

| Tier | Where | What it proves |
|---|---|---|
| unit | [internal/blob](../../internal/blob/), [s3](../../internal/blob/s3/), [blobtest](../../internal/blob/blobtest/) | key layout, spool admission and cleanup on every error path, single pass digests, the commit protocol and its branches against a fake database, multipart (threshold, parts, concurrency, abort, checksums), the breaker, verification, the mark of a missing object, its repair and its recheck once per interval (`TestDownloadVerification`, Q-STOR-151, Q-STOR-153), conformance logic, the SDK adapter against a fake endpoint (retries, error mapping, stall, credentials) |
| integration | [test/integration/blob](../../test/integration/blob/) | SeaweedFS conformance; the 100-upload race through four replicas; the race with deletes and collection; the collector killed or cancelled at random points; the drain; hand-over; `AS001`; the usage fold (counters after uploads, an overwrite and a delete against a recount; two loops and four callers on 50,000 and 40,000 deltas with the deadlock counter of the database; a deleted repository with unfolded deltas; a backlog of 25,000 under an interval of one hour); a table added by a newer release; pause, hold, dry run; checkpoint resume; the job handler; verification of a corrupted object |

`make test` runs the unit tier, `make test-integration` the integration tier (`TROVE_TEST_BLOB_RACE_SECONDS` stretches the race test from 15 s to a soak run). The
unit fake (`fakedb_test.go`) simulates only the table operations the commit protocol and the download path use; the real locking
behavior is what the integration tier checks, with the collector's reference check mutated away to see the tests fail.

`TROVE_TEST_PG_POOLED=1` runs the blob and the coordination integration suites with the main pool through PgBouncer in transaction mode
(`testenv.PgPooled`; the role defaults of R-HA-014 are set by `testenv.ThroughPgBouncer`). CI should run the suite once each way.

An object-store response without `Content-Length` has `ObjectInfo.Size == -1` (unknown), never 0: the early size check
of a download is skipped and the digest at the end of the body decides. A caller's own deadline ending a store call
(`context.DeadlineExceeded` that is not a transient `StoreError`) is no answer for the circuit breaker.
