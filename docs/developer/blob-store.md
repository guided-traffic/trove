# Blob store

Content-addressed storage on an S3-compatible bucket: [internal/blob](../../internal/blob/) (everything that does not
depend on an S3 client), [internal/blob/s3](../../internal/blob/s3/) (the AWS SDK adapter, the only package that imports
the SDK), [internal/blob/blobtest](../../internal/blob/blobtest/) (an in-memory store with fault injection for tests).
Requirements: R-STOR-001 to R-STOR-006, R-STOR-013, R-STOR-024, R-STOR-025, R-STOR-030 to R-STOR-035, R-STOR-046.
The row is the authority on whether a blob exists; the object is the content. Nothing in this tree uses Valkey or a
session-level advisory lock (R-STOR-046, enforced by [internal/archtest](../../internal/archtest/)).

## Files

| File | Responsibility |
|---|---|
| [key.go](../../internal/blob/key.go) | `Digest`, `Key(prefix, digest)`, `ParseKey`, `Digests` (the four digests and the size) |
| [objects.go](../../internal/blob/objects.go) | `ObjectStore` interface, `StoreError` and its kinds, `ErrNotFound`, `ErrCircuitOpen` |
| [spool.go](../../internal/blob/spool.go) | `Spool`: admission, the budget, spool files, the single hashing pass, cleanup |
| [store.go](../../internal/blob/store.go), [commit.go](../../internal/blob/commit.go) | `Store`, `Ingest`, `Commit` (the commit protocol), `Lookup` |
| [transfer.go](../../internal/blob/transfer.go) | single `PutObject` or multipart from the spool file, part sizing, abort |
| [download.go](../../internal/blob/download.go) | `Store.Open` and the verifying reader (hold-back, `content_status`) |
| [guard.go](../../internal/blob/guard.go) | `Guard`: circuit breaker and request metrics around any `ObjectStore` |
| [gc.go](../../internal/blob/gc.go), [gcjob.go](../../internal/blob/gcjob.go) | `Collector` (per-blob protocol, hand-over, pause, hold, fault hook) and the job adapter |
| [conformance.go](../../internal/blob/conformance.go) | startup check, conformance checks, `AwaitStartupCheck` |
| [sql.go](../../internal/blob/sql.go), [tables.go](../../internal/blob/tables.go), [metrics.go](../../internal/blob/metrics.go), [errors.go](../../internal/blob/errors.go) | the `DB` interface, the `blobRows` seam and its sqlc implementation, export-class registration, metric families, `errs` mapping |
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
row's `FOR SHARE` lock and adds the reference (item insert, hooks). The statements are sqlc queries in
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

Gaps against the specification, all visible at construction (nothing is silently ignored):

- `storage.s3.ca_file`: `NewFromFactory` refuses it. Add the CA to `proxy.outbound.tls.ca_files` until
  `internal/outbound` has a per-client trust option.
- `storage.s3.http.proxy_credentials_file`: `NewFromFactory` refuses it. `outbound.WithProxyURL` sends only the user information of the proxy URL, which the configuration forbids (R-OPS-013: URL keys carry no credentials), and a file read once would not follow a rotation. Use a proxy that authenticates by source address until the outbound layer takes a re-read proxy credential.
- `http.max_connections`, `tls_handshake_timeout`, `idle_conn_timeout`: the outbound factory has
  no options for them, so its defaults apply (`proxy_url` and `no_proxy` as host patterns are passed through; CIDR
  patterns in `no_proxy` are not).
- `trove_storage_s3_requests_total` counts calls of the `ObjectStore` interface, not the SDK's retry attempts.

## Download verification (R-STOR-013, Q-STOR-6)

`Store.Open(ctx, digest, size, OpenOptions)` returns a reader. A full read is verified unless
`storage.download.verify` is false: SHA-256 is computed while reading and the last `hold_back` bytes are not released
before the body ended and the digest matched. On a mismatch the reader returns `ErrCorrupt` instead of the held bytes
(the HTTP layer aborts the response). Only evidence about the object marks the blob `corrupt`: an object whose size
differs from the row's, or a body read to its end with another digest. A transport error aborts the read but changes no
status. `WithDamageHook` is where the audit event hangs. A range is returned as stored (H-11).

## Garbage collection (R-STOR-024, R-STOR-025)

`blob.NewCollector(cfg.GC, prefix, objects, db)`; `Run(ctx, RunOptions{Mode, DryRun, Cursor, Checkpoint})` returns a
`GCResult` (`dryRun, trashPurged, candidates, kept, held, deleted, bytesReclaimed, errors, paused`). A run:

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
5. waits up to `delete_settle` (plus two polls) to drop the rows it deleted.

On cancellation (a drain) the run cancels its S3 calls, waits for them, records the deletes that succeeded and sets
`state_changed_at = now() − (handover_delay − delete_settle)` on its other claims, so the run that continues finishes
them after `delete_settle`. A crash needs none of this: the rows wait for `handover_delay`.

Extension points on `Collector`: `Hold` (the backup hold of step 2b, runs in the claim transaction; nil holds nothing),
`Paused` (replaceable; the default reads the key-value row `system_state.deletions_paused_at` because `system_state`
in migration 00001 is key-value, not the column the sketch in R-HA-025 shows) and `Fault` (a test hook called at named
points, `claim:before|locked|checked|committed`, `delete:before|after`, `record:after`, `finish:delete`, `drop:before`;
a non-nil error ends the run at once as a process kill would, with `errors.Is(err, blob.ErrFault)`).

Not in this package yet: the trash purge and the expiry of upload sessions that R-STOR-025 puts at the start of a run
(their packages do not exist; the job handler calls them before `Run` and puts the count in `trashPurged`), the orphan
sweep (R-STOR-026) and the scrub (R-STOR-027).

### Job registration

[gcjob.go](../../internal/blob/gcjob.go) holds `Collector.RunJob(ctx, mode, args, run)`, the body of the handlers; it
takes the narrow `JobRun` interface (`*jobs.Run` implements it) so that this infrastructure package does not import the
domain package `internal/jobs`. The kinds and tasks are registered where both are visible, as
`jobs.RegisterCoordinationKinds` does for the cluster: the comment at the top of gcjob.go shows the four
registrations (`gc` and `gc-full`, queue `maintenance`, exclusive, both tasks with dedup key `gc`).

## Conformance check (R-STOR-035)

`blob.StartupCheck` writes, reads back, compares and deletes `<prefix>/_probe/<replica>` and lists
`<prefix>/sha256/` with one key. `blob.AwaitStartupCheck` repeats it: a configuration error (`AccessDenied`,
`NoSuchBucket`, bad credentials) is returned at once, a transient error or an open circuit is retried with exponential
backoff capped at 30 s and logged once per minute, and the call returns only when the check passed or the context ended.
`blob.RunConformance` runs `blob.ConformanceChecks()`, which also cover: a missing key reads as not found, overwrite
and delete visibility to the next read and list (A1), concurrent writes of identical bytes (A6), a ranged read, a
multipart object invisible until completion and complete after, an aborted upload leaving neither object nor listed
upload (A2), `DeleteObjects` (optional: its failure names `storage.gc.batch_delete`) and a presigned URL. Everything it
writes lies below `<prefix>/_probe/` and is removed. Conditional writes and server-side copy are not checked: trove
uses neither until upload sessions (R-STOR-011, P1).

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
`md5`, and with `none` plus `when_required`. An endpoint with a private CA needs the CA in the system trust store
(`ca_file` is not supported, above). SeaweedFS creates a missing bucket on the first write, so a wrong bucket name is
not an error there; the credential test uses a wrong secret key instead.

## Tests

| Tier | Where | What it proves |
|---|---|---|
| unit | [internal/blob](../../internal/blob/), [s3](../../internal/blob/s3/), [blobtest](../../internal/blob/blobtest/) | key layout, spool admission and cleanup on every error path, single pass digests, the commit protocol and its branches against a fake database, multipart (threshold, parts, concurrency, abort, checksums), the breaker, verification, conformance logic, the SDK adapter against a fake endpoint (retries, error mapping, stall, credentials) |
| integration | [test/integration/blob](../../test/integration/blob/) | SeaweedFS conformance; the 100-upload race through four replicas; the race with deletes and collection; the collector killed or cancelled at random points; the drain; hand-over; `AS001`; a table added by a newer release; pause, hold, dry run; checkpoint resume; the job handler; verification of a corrupted object |

`make test` runs the unit tier, `make test-integration` the integration tier (`TROVE_TEST_BLOB_RACE_SECONDS` stretches the race test from 15 s to a soak run). The
unit fake (`fakedb_test.go`) simulates only the table operations the commit protocol and the download path use; the real locking
behavior is what the integration tier checks, with the collector's reference check mutated away to see the tests fail.

`TROVE_TEST_PG_POOLED=1` runs the blob and the coordination integration suites with the main pool through PgBouncer in transaction mode
(`testenv.PgPooled`; the role defaults of R-HA-014 are set by `testenv.ThroughPgBouncer`). CI should run the suite once each way.

An object-store response without `Content-Length` has `ObjectInfo.Size == -1` (unknown), never 0: the early size check
of a download is skipped and the digest at the end of the body decides. A caller's own deadline ending a store call
(`context.DeadlineExceeded` that is not a transient `StoreError`) is no answer for the circuit breaker.
