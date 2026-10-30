# Files transfer and items

The raw file path of a repository: `PUT`, `GET`, `HEAD` and `DELETE` on `/files/{repoKey}/{path}`, and the items operations
of the management API (`items`, `children`, `stats`). Requirements: [R-STOR-007, R-STOR-012, R-STOR-013, R-STOR-015 to
R-STOR-018](../spec/storage.md), [R-GENERIC-002 to R-GENERIC-008](../spec/formats/generic.md),
[R-API-025](../spec/api-and-ui.md), [R-ARCH-007, R-ARCH-011, R-ARCH-014, R-ARCH-024](../spec/architecture.md). Code:
[internal/files](../../internal/files/) (the service), [internal/api/rawfiles](../../internal/api/rawfiles/) (the `/files/`
handler), [items_api.go](../../internal/api/v1/items_api.go) (the management operations), the statements in
[queries/files.sql](../../internal/meta/postgres/queries/files.sql). The security view is
[file-transfer.md](../security/file-transfer.md). Milestone M1 serves the local class of the generic format; remote and
virtual repositories are specified and not built, and the trash can, copy and move, properties, deploy by checksum and
quotas are P1.

## What has to be in your head first

- **The service decides, the handlers translate.** Every operation of [Service](../../internal/files/files.go) asks the
  decision point first (`Decide`, or `PathsAllowed` for many paths) and answers a refusal with the error of the decision, so a
  hidden item is `authz.NotFound()`, the same bytes as a missing one. A handler never decides on its own.
- **PostgreSQL decides, nothing is kept between requests.** `Stat`, `ItemInfo`, `Children` and `Stats` read PostgreSQL on every
  call. The only state in memory is write-only: the download statistics (`files.Stats`) and the repository activity
  (`repository.Activity`), flushed in batches and at shutdown.
- **`HEAD` and conditional requests never reach the object store.** The item row carries the digest, the size, the media type and the
  modification time; the ETag is the content address.
- **A body is streamed to the spool, hashed once, committed by the blob protocol.** The service does not buffer a body; the
  integration test uploads 500 MiB and measures the heap.

## Files

| File | Responsibility |
|---|---|
| [files.go](../../internal/files/files.go) | `Service`, `Options`, `Caller`; `decide` (`Decide`; the engine looks an unknown key up itself, R-ARCH-011 step 2; the endpoint's ceiling comes from `Caller.Subject.Endpoint`, not from the coordinate), the optional interfaces `PathDecider`, `WriteRecorder` |
| [deploy.go](../../internal/files/deploy.go) | `Deploy`: the coarse check, admission, spool, the fine check (`overwrite`), the link step of the commit protocol, folder creation |
| [download.go](../../internal/files/download.go) | `Stat` (PostgreSQL only), `Open` (the verifying reader or a range), `Served` (statistics, activity, `artifact.downloaded`) |
| [children.go](../../internal/files/children.go) | `ItemInfo`, `Stats`, `Children` (cursor, recursion, depth, per-entry decision) |
| [delete.go](../../internal/files/delete.go) | `Delete`, the synchronous and the job form, `RunDeleteJob` (job kind `items-delete`) |
| [stats.go](../../internal/files/stats.go) | `Stats`: the in-memory download counter and its flush |
| [item.go](../../internal/files/item.go) | `Item`, `View` (the JSON description), `Links` (the one place that builds content URLs, R-ARCH-007) |
| [store.go](../../internal/files/store.go) | the sqlc seam |
| [rawfiles/handler.go](../../internal/api/rawfiles/handler.go) | routes, path parsing, `get` / `serveFile` / `put` / `delete` |
| [rawfiles/conditional.go](../../internal/api/rawfiles/conditional.go) | `If-Match`, `If-None-Match`, `If-(Un)Modified-Since`, `Range`, `If-Range` of RFC 9110 |
| [rawfiles/digest.go](../../internal/api/rawfiles/digest.go) | `Repr-Digest` in, `Repr-Digest` / `Want-Repr-Digest` out (RFC 9530) |
| [rawfiles/index.go](../../internal/api/rawfiles/index.go) | the directory index, HTML and JSON |
| [httpserver/content.go](../../internal/httpserver/content.go) | `ContentWriter`, `ContentWriterFor`: the content headers of R-ARCH-007, `same-origin` for a request the session cookie authenticated (Q-ARCH-86) |
| [httpserver/router.go](../../internal/httpserver/router.go) | `Router.HandleUncleaned`: paths `ServeMux` would redirect reach the handler |
| [api/v1/items_api.go](../../internal/api/v1/items_api.go) | `GetItem`, `DeleteItem`, `ListChildren`, `GetItemStats`; `CallerFrom` |
| [app/wire_files.go](../../internal/app/wire_files.go) | `wireFiles`, `filesComponent` (flush loops, `Flusher`), `registerFiles`, `registerFilesJobs`, `repositoryAPI` |
| [audit/events_files.go](../../internal/audit/events_files.go) | `artifact.deployed`, `artifact.deleted` (transactional), `artifact.downloaded` (asynchronous) |
| [authz/paths.go](../../internal/authz/paths.go), [authz/catchup.go](../../internal/authz/catchup.go) | `PathsAllowed`, `CatchUp`: what the transfer needs of the decision point beyond `Decide` |

## Request path

```text
/files/{repoKey}/{path...}   (routes registered by rawfiles.Handler.Register; HandleUncleaned takes "a//b" and "a/../b")
  splitPath(EscapedPath)  ->  key, rest (still escaped)
  repository.CleanPath(rest)            400 invalid_path, before any authorization (R-GENERIC-002)
  v1.CallerFrom(r)                       Subject, endpoint, actor, source
  Service.decide:  repos.ByKey(key)      an unknown key is looked up and the change log polled (R-ARCH-011 step 2)
                   Engine.CatchUp(key)   the grant index at least as new as the database, for a key it does not hold
                   Authorizer.Decide     401 / 404 hidden / 403 / 503 / 412  (R-AUTHZ-016)
  repository lookup, lifecycle, disabled (404 repository_disabled), class (405), include/exclude patterns
  GET/HEAD: Stat -> ETag, conditionals, Range -> Open -> copy      PUT: Deploy      DELETE: Delete
```

An error before the status line is a problem (`httpserver.Problem`) written to the plain writer. A response that carries the
file or the directory index is written through `ContentWriterFor(w, r, …)`, which sets `X-Content-Type-Options`, the sandbox
policy, `Cross-Origin-Resource-Policy` and `Content-Disposition` (the index set without `sandbox` and without a disposition) and
applies them again at `WriteHeader` and at the first `Write`, so a handler cannot weaken them. The resource policy is `same-origin`
when the session cookie authenticated `r` and `same-site` otherwise (Q-ARCH-86); `ContentWriter` without a request is `same-site`.

The path tail is refused with `400 invalid_path` when it starts with a slash (`/files/<key>//x`), before `CleanPath` drops one
leading slash; the stored media type is the request's `Content-Type` parsed with `mime.ParseMediaType` and written back with
`mime.FormatMediaType` (a value that does not parse is `400 invalid_header`).

## Deploy

`Deploy` is [R-ARCH-014](../spec/architecture.md) in code:

1. `deploy` on the path is decided before the body is read (a refused PUT reads nothing and sends no `100 Continue`).
2. The repository must be a local one (else 405), not disabled (404), and its patterns must admit the path (403 `not_admitted`, for
   every caller).
3. `Ingest` spools the body (declared length, `storage.upload.max_size` and `config.storage.maxUploadSize`, the digests of
   `Repr-Digest`: 400 for a length mismatch, 409 for a digest mismatch, nothing stored). The repository's own limit is validated
   as a non-negative integer when the configuration is written (`validByteLimit`, 422 at `/storage/maxUploadSize`); a stored value
   that is none (`maxUpload` returns an error) refuses the upload with 500 instead of reading as "no limit" (Q-STOR-145).
4. The commit-time re-check of R-AUTHZ-015: the write is decided again now that the body has arrived, `deploy` with `overwrite` for an
   existing item with other bytes, `deploy` alone otherwise, **before** the object is written and before the commit transaction opens
   (a stale index makes a decision wait). That decision is the one `RecordAllowed` records. Whether `overwrite` is held is settled
   there too (`mayOverwrite`, `PathsAllowed`, no audit event), for the case that a concurrent upload creates the path before the link
   step: the link step then replaces content or returns `errOverwriteDenied`, and no decision is asked inside the transaction
   (Q-STOR-144). A folder at the path, or a file at an
   ancestor, is 409 `path_conflict`. `If-None-Match: *` makes the deploy create-only (412).
5. `BeginCommit` tells the server that the request is committing (R-ARCH-024 step 5), then `Commit` runs the protocol of
   [blob-store.md](blob-store.md). Its link function, in the transaction that holds the blob row's `FOR SHARE` lock:
   - inserts the missing ancestor folders (`ON CONFLICT DO NOTHING`) and refuses a file among them;
   - holds every ancestor folder `FOR SHARE` until the commit, deepest first (`lockAncestors`, the order in which a delete takes its
     exclusive locks), and requires all of them present, else the round repeats and `ensureParents` creates them again; so neither the
     removal of an empty implicit folder nor a folder delete can take an ancestor from under the new file (Q-STOR-144);
   - locks the item row `FOR UPDATE` (two uploads to one path serialise here, the second is an overwrite), or inserts it; an insert
     that loses the race against another upload repeats the step (at most 5 rounds);
   - writes `artifact.deployed` and, through `RecordAllowed`, the audit row of the allowed write, in the same transaction.
6. The answer is the item description: 201 for a new path or a replacement, 200 for an identical re-upload (nothing is written, no
   event) and for an existing folder.

`PUT …/folder/` with an empty body creates an explicit folder and its missing parents.

## Download

`Stat` decides `read`, loads the repository, hides a path the patterns do not admit, reads the item and refuses a blob whose
`content_status` is `missing` (503 `content_unavailable` with `Retry-After`) or `corrupt` (500). `serveFile` then:

- sets `ETag: "<sha256 hex>"`, `Last-Modified` (second precision), `Accept-Ranges`, `Repr-Digest`, `Cache-Control`;
- evaluates the conditional headers in the order of RFC 9110 section 13.2.2 (`304`, `412`);
- evaluates `Range` and `If-Range`: one satisfiable range is `206`, several ranges or an `If-Range` that does not match give the whole
  content, a start beyond the end is `416` with `Content-Range: bytes */size`;
- declares `Content-Length` for 200 and 206 and never uses the chunked coding;
- opens the body (`Store.Open`: a whole body through the verifying reader that holds back the last bytes, a range as stored) **before**
  the status line, so an object-store failure is a problem the client can read; an error in the middle of the body ends in
  `panic(http.ErrAbortHandler)`, which cuts the connection: the client sees a truncated transfer, never a complete corrupted file.

Any replica answers a resumed range alike, because the ETag is the content address and the item is read from PostgreSQL. The
integration test `TestR_ARCH_024_ADownloadCutAtTheShutdownDeadlineResumesOnAnotherReplica` cuts a download at the shutdown deadline
and resumes it on the other replica with `Range` and `If-Range`.

A folder without its trailing slash is `301` to the URL with it; the folder (or the root) with the slash is the directory index:
HTML for `Accept: text/html`, JSON otherwise, in pages (`limit`, `cursor`), names HTML-escaped and every link `./` plus the
percent-encoded name.

## Delete

`Delete` decides `delete` on the path, then counts the subtree. Up to `storage.delete.max_sync_items` items it lists the subtree in a
transaction, decides `delete` for every item with `PathsAllowed` (403 and nothing changes if one is refused; the message names
the path the caller sent), deletes the files by id, writes one `artifact.deleted` per file, removes the folders deepest first
(each only if it is empty then) and the empty implicit folders above. A larger subtree is checked in full at the request and
removed by the job `items-delete` in batches of `storage.delete.batch_size` (files first, then the folders), answered `202` with
the job. The job carries the highest item id the check saw (`DeleteArgs.MaxID`) and removes only items up to it, so a file that is
deployed into the subtree afterwards stays (Q-STOR-143). A virtual repository holds no items: a delete there is `405`, as a deploy
is. Items are deleted for good: the trash can is P1 (Q-STOR-19).

## Listings and statistics

`Children` reads a keyset batch from PostgreSQL, decides `read` for each entry with one `PathsAllowed` call (one index, no audit
event per omitted entry), and continues until the page is full or `scanLimit` rows were read (Q-STOR-20). A batch is `limit + 1`
rows and doubles after each batch that left the page short, up to `maxChunk` (5,000), so a page costs a few dozen queries at most.
The cursor is the path of the page's last entry; a page that ended at `scanLimit` beyond its last visible entry carries
`hiddenPosition` (a NUL) and the item id of the last row read instead, which `cursorPath` turns back into a path with
`ItemPathByID`, refusing an id outside the repository or below another folder as `400 invalid_cursor` (Q-STOR-62). `files.Stats` counts a download after its body was copied (200, or 206 from byte 0; not HEAD, 304 or
`?stats=false`) and flushes with one upsert in item id order every `storage.stats.flush_interval`; `repository.Activity` writes
`last_activity_at` once a minute and at shutdown (R-REPO-012). Both are flushed by `filesComponent.Flush` (R-ARCH-024 step 6).

## What the transfer needs of the decision point

| Method | Why |
|---|---|
| `Decide(ctx, subject, coordinate)` | every file and listing decision; the endpoint ceiling of R-EP-013 applies inside it |
| `Engine.PathsAllowed(ctx, subject, base, action, paths)` | the entries of a listing and the items of a subtree, on one index, without an audit event per refused entry |
| `Engine.CatchUp(ctx, key)` | a repository that the snapshot knows and the grant index does not (just created on another replica): reads the database epochs and waits for an index that new, so the repository is usable at once (Q-ARCH-40) |
| `Engine.RecordAllowed(ctx, tx, …)` | the audit row of an allowed write, in the writer's transaction |

`PathsAllowed`, `CatchUp` and `RecordAllowed` are reached by optional interfaces of the service; with another `Authorizer` the
service falls back to one `Decide` per path and skips the other two.

## Tests

| Tier | Where | Proves |
|---|---|---|
| unit | [internal/files](../../internal/files/files_test.go), [rawfiles](../../internal/api/rawfiles/rawfiles_test.go), [httpserver/content_test.go](../../internal/httpserver/content_test.go), [authz/paths_test.go](../../internal/authz/paths_test.go) | path helpers and subtree bounds, the item description, range and conditional rules, `Repr-Digest`, the directory index escaping, invalid paths before everything else, route parity with the document, the content header wrapper, `HandleUncleaned`, `PathsAllowed` |
| integration | [test/integration/files](../../test/integration/files/) | E1.4 (201 then 200, a range of 100 bytes, digest mismatch, `evil.svg`), R-GENERIC-002 to -007, hidden existence byte for byte, the overwrite permission, listings filtered by grants, folder and job deletes, statistics and activity, the 500 MiB upload through the spool (heap and spool file measured), a download cut at the shutdown deadline and resumed on another replica, uploads during a rollout restart, concurrent writers and the folder protocol, the repository API (contract suite of E1.6, validation, ETag views, merge patch, delete job, idempotency across replicas) |

Run the area with the shared stack:
`set -a; . .dev/integration/env; set +a; go test -count=1 -tags integration ./test/integration/files/...`.
Every replica of the suite is a whole web request path with its own pools, change log, grant index, blob store and spool, behind
the middleware chain and the real handlers; only the authentication stage is a test double (a bearer name is a principal).
Valkey is not part of it: nothing in the transfer needs it.

## Decisions

[Q-STOR-17 to Q-STOR-21, Q-STOR-143 to Q-STOR-145](../spec/storage.md#decisions), [Q-ARCH-40 to Q-ARCH-43](../spec/architecture.md#decisions),
[Q-GENERIC-6](../spec/formats/generic.md#decisions), [Q-API-32 to Q-API-34](../spec/api-and-ui.md#decisions).
