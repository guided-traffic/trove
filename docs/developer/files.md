# Files transfer and items

The raw file path of a repository: `PUT`, `GET`, `HEAD` and `DELETE` on `/files/{repoKey}/{path}`, and the items operations
of the management API (`items`, `children`, `stats`). Requirements: [R-STOR-007, R-STOR-012, R-STOR-013, R-STOR-015 to
R-STOR-018](../spec/storage.md), [R-GENERIC-002 to R-GENERIC-008](../spec/formats/generic.md),
[R-API-025](../spec/api-and-ui.md), [R-ARCH-007, R-ARCH-011, R-ARCH-014, R-ARCH-024](../spec/architecture.md). The transfer is three
packages ([Q-ARCH-206](../spec/architecture.md#decisions)): [internal/items](../../internal/items/) stores and reads without
deciding ([items.md](items.md)), [internal/files](../../internal/files/) is the generic layer that decides, resolves and admits
over it, and [internal/api/rawfiles](../../internal/api/rawfiles/) is the `/files/` handler, with the content response in
[internal/api/edge](../../internal/api/edge/). [items_api.go](../../internal/api/v1/items_api.go) holds the management operations,
the statements are in [queries/files.sql](../../internal/meta/postgres/queries/files.sql). The security view is
[file-transfer.md](../security/file-transfer.md). Milestone M1 serves the local class of the generic format; remote and
virtual repositories are specified and not built, and the trash can, copy and move, properties, deploy by checksum and
quotas are P1.

## What has to be in your head first

- **The generic layer decides, the store stores, the handlers translate.** Every operation of
  [Service](../../internal/files/files.go) asks the request's `Access` first (`Decide`, or `PathsAllowed` for many paths) and
  answers a refusal with the error of the decision, so a hidden item is `authz.NotFound()`, the same bytes as a missing one. The
  item store ([items.md](items.md)) is called only after that and decides nothing. A handler never decides on its own.
- **A path is decided on its format's coordinate.** Before the decision the service looks the repository up in the replica's
  snapshot (`locate`, the one lookup R-ARCH-011 step 2 puts before a decision) and asks the format seam what the path is
  (`Formats.Classify`): an npm tarball is decided on its package and version, a generic path on the path alone (R-EP-014,
  Q-AUTHZ-160). The outcome of the lookup is answered only after the decision allowed the request (`target.resolved`), so a
  missing repository and a hidden one answer alike.
- **One index per request.** The entry point begins the request's index once (`edge.Resolve`: `rawfiles`, and the shared step of
  the items operations) and hands the service an `edge.Access` in `Caller.Access`; the service never begins one. The one
  exception is deliberate: a deploy decides again after its body arrived on a new holder (`Caller.Recheck`, Q-ARCH-208), because
  that re-check must see a grant removed while the body was on the wire (R-AUTHZ-015).
- **PostgreSQL decides, nothing is kept between requests.** `Stat`, `ItemInfo`, `Children` and `Stats` read PostgreSQL on every
  call. The only state in memory is write-only: the download statistics (`items.Stats`) and the repository activity
  (`repository.Activity`), flushed in batches and at shutdown.
- **`HEAD` and conditional requests never reach the object store.** The item row carries the digest, the size, the media type and the
  modification time; the ETag is the content address.
- **A body is streamed to the spool, hashed once, committed by the blob protocol.** The service does not buffer a body; the
  integration test uploads 500 MiB and measures the heap.

## Files

| File | Responsibility |
|---|---|
| [files/files.go](../../internal/files/files.go) | `Service`, `Options`, `Caller` (`Access`, `Subject`, `Actor`, `Source`, `Recheck`), the `Access` and `Items` interfaces, `decide` and `coordinate` (with the format's package and version), `recorder` (the `Extra` that records the allowed write), `locate` and `target.resolved` |
| [files/formats.go](../../internal/files/formats.go) | the seam to the formats: `PathClass`, `Removal`, `Formats` (`Classify`, `ServeCheck`, `RawDeploy`, `CheckRemoval`); `classify`, `classifier` (the `authz.Classifier` of a listing), `admits` (the patterns on the package for a format whose patterns match names), `Classified` (the coordinate of the items operations' shared step) |
| [files/deploy.go](../../internal/files/deploy.go) | `Deploy`: the coarse check, admission, spool, the fine check (`overwrite`), the commit through `items.Store.Commit`, folder creation |
| [files/download.go](../../internal/files/download.go) | `Stat` (PostgreSQL only), `Open`, `Served` |
| [files/children.go](../../internal/files/children.go) | `ItemInfo`, `Stats`, `Children` (the per-entry decision is the store's `Visibility` callback) |
| [files/delete.go](../../internal/files/delete.go) | `Delete`: the decisions, then `items.Store.Delete` with the subtree check as its callback and, in its `Extra`, the recorded write and the format's `CheckRemoval` |
| [items/](../../internal/items/) | the store; see [items.md](items.md) |
| [api/edge/content.go](../../internal/api/edge/content.go) | `ServeItem`: ETag, `Last-Modified`, the conditional rules, `Range`, `Content-Length`, the content headers, the aborted body |
| [api/edge/conditional.go](../../internal/api/edge/conditional.go) | `If-Match`, `If-None-Match`, `If-(Un)Modified-Since`, `Range`, `If-Range` of RFC 9110 |
| [api/edge/digest.go](../../internal/api/edge/digest.go) | `Repr-Digest` in (`ParseReprDigest`), `Repr-Digest` / `Want-Repr-Digest` out (RFC 9530) |
| [api/edge/links.go](../../internal/api/edge/links.go) | `Links` (the one place that builds content URLs, R-ARCH-007), `ItemView`, `DescribeItem` (the JSON description) |
| [api/edge/json.go](../../internal/api/edge/json.go), [list.go](../../internal/api/edge/list.go) | `WriteJSON`, `WriteJob` (202 with the job and its `Location`); `ParseList`, `ListQuery`, `InvalidCursor` |
| [rawfiles/handler.go](../../internal/api/rawfiles/handler.go) | routes, path parsing, `caller` (begins the index), `get` / `put` / `delete` |
| [rawfiles/index.go](../../internal/api/rawfiles/index.go) | the directory index, HTML and JSON |
| [httpserver/content.go](../../internal/httpserver/content.go) | `ContentWriter`, `ContentWriterFor`: the content headers of R-ARCH-007, `same-origin` for a request the session cookie authenticated (Q-ARCH-86) |
| [httpserver/router.go](../../internal/httpserver/router.go) | `Router.HandleUncleaned`: paths `ServeMux` would redirect reach the handler |
| [api/v1/items_api.go](../../internal/api/v1/items_api.go) | `GetItem`, `DeleteItem` (strict for every caller without `content:bypass`, Q-AUTHZ-162), `ListChildren`, `GetItemStats`; `filesCaller`, `filesAccess` |
| [formathost/hooks.go](../../internal/formathost/hooks.go) | `FilesSeam`, the `Formats` of the composition root: `Classify` over the registry, `ServeCheck`, `RawDeploy` (`/files/` deploys only into a format whose protocol is `/files/`, Q-ARCH-223), `CheckRemoval` (the ownership of the packages a delete removes, Q-AUTHZ-161) |
| [app/wire_files.go](../../internal/app/wire_files.go) | `wireFiles` (the store, then the service), `filesComponent` (flush loops, `Flusher`), `registerFiles`, `registerFilesJobs`, `filesDecider`, `repositoryAPI` |
| [audit/events_files.go](../../internal/audit/events_files.go) | `artifact.deployed`, `artifact.deleted` (transactional), `artifact.downloaded` (asynchronous) |

## Request path

```text
/files/{repoKey}/{path...}   (routes registered by rawfiles.Handler.Register; HandleUncleaned takes "a//b" and "a/../b")
  splitPath(EscapedPath)  ->  key, rest (still escaped)
  repository.CleanPath(rest)            400 invalid_path, before any authorization (R-GENERIC-002)
  Handler.caller(r, key)                 edge.Resolve: the request's index, begun once, with the key made known
                                         (R-ARCH-011 step 2, R-API-052); 503 authz_stale / 412 authz_epoch_unknown
  Service.locate                         the repository from the snapshot, the format's class of the path (package, version)
  Service.decide -> Access.Decide        401 / 404 hidden / 403 / 503 / 412  (R-AUTHZ-016), on that index, on the classified
                                         coordinate, the endpoint's ceiling from the subject (R-EP-013, R-EP-014)
  target.resolved: lookup outcome, lifecycle, disabled (404 repository_disabled), class (405), the format's raw deploy (409),
  include/exclude patterns (on the package for a format whose patterns match names)
  GET/HEAD: Stat -> edge.ServeItem (ETag, conditionals, Range, Open, copy)      PUT: Deploy      DELETE: Delete
```

An error before the status line is a problem (`httpserver.Problem`) written to the plain writer. A response that carries the
file or the directory index is written through `ContentWriterFor(w, r, …)` (`edge.ServeItem` for a file), which sets `X-Content-Type-Options`, the sandbox
policy, `Cross-Origin-Resource-Policy` and `Content-Disposition` (the index set without `sandbox` and without a disposition) and
applies them again at `WriteHeader` and at the first `Write`, so a handler cannot weaken them. The resource policy is `same-origin`
when the session cookie authenticated `r` and `same-site` otherwise (Q-ARCH-86); `ContentWriter` without a request is `same-site`.

The path tail is refused with `400 invalid_path` when it starts with a slash (`/files/<key>//x`), before `CleanPath` drops one
leading slash; the stored media type is the request's `Content-Type` parsed with `mime.ParseMediaType` and written back with
`mime.FormatMediaType` (a value that does not parse is `400 invalid_header`).

## Deploy

`Deploy` is [R-ARCH-014](../spec/architecture.md) in code:

1. `deploy` on the path is decided before the body is read (a refused PUT reads nothing and sends no `100 Continue`).
2. The repository must be a local one (else 405), not disabled (404), of a format whose protocol is `/files/` itself (else 409
   `conflict` naming the format's protocol address, `Formats.RawDeploy`, Q-ARCH-223: an npm repository takes neither a file nor a
   folder here), and its patterns must admit the path (403 `not_admitted`, for every caller).
3. `Store.Spool` spools the body (declared length, `storage.upload.max_size` and `config.storage.maxUploadSize`, the digests of
   `Repr-Digest`: 400 for a length mismatch, 409 for a digest mismatch, nothing stored). The repository's own limit is validated
   as a non-negative integer when the configuration is written (`validByteLimit`, 422 at `/storage/maxUploadSize`); a stored value
   that is none (`maxUpload` returns an error) refuses the upload with 500 instead of reading as "no limit" (Q-STOR-145).
4. The commit-time re-check of R-AUTHZ-015: the write is decided again now that the body has arrived, on a new holder
   (`Caller.afterBody`, Q-ARCH-208), `deploy` with `overwrite` for an existing item with other bytes, `deploy` alone otherwise,
   **before** the object is written and before the commit transaction opens (a stale index makes a decision wait). That decision
   is the one `RecordAllowed` records. Whether `overwrite` is held is settled there too (`mayOverwrite`, `PathsAllowed`, no audit
   event), for the case that a concurrent upload creates the path before the link step: the link step then replaces content or
   returns `items.ErrOverwriteDenied` (which `Deploy` turns into the decided 403), and no decision is asked inside the transaction
   (Q-STOR-144). A folder at the path, or a file at an
   ancestor, is 409 `path_conflict`. `If-None-Match: *` makes the deploy create-only (412).
5. `Store.Commit` ([items.md](items.md)) calls `BeginCommit`, which tells the server that the request is committing (R-ARCH-024
   step 5), then runs the protocol of [blob-store.md](blob-store.md). Its link function, in the transaction that holds the blob
   row's `FOR SHARE` lock:
   - inserts the missing ancestor folders (`ON CONFLICT DO NOTHING`) and refuses a file among them;
   - holds every ancestor folder `FOR SHARE` until the commit, deepest first (`lockAncestors`, the order in which a delete takes its
     exclusive locks), and requires all of them present, else the round repeats and `ensureParents` creates them again; so neither the
     removal of an empty implicit folder nor a folder delete can take an ancestor from under the new file (Q-STOR-144);
   - locks the item row `FOR UPDATE` (two uploads to one path serialise here, the second is an overwrite), or inserts it; an insert
     that loses the race against another upload repeats the step (at most 5 rounds);
   - runs `CommitSpec.Extra`, which is `Access.RecordAllowed` (the audit row of the allowed write), then the storage hook, then
     writes `artifact.deployed`, in the same transaction.
6. The answer is the item description: 201 for a new path or a replacement, 200 for an identical re-upload (nothing is written, no
   event) and for an existing folder.

`PUT …/folder/` with an empty body creates an explicit folder and its missing parents.

## Download

`Stat` decides `read`, loads the repository, hides a path the patterns do not admit, reads the item and refuses a blob whose
`content_status` is `missing` (503 `content_unavailable` with `Retry-After`) or `corrupt` (500). `edge.ServeItem` then:

- sets `ETag: "<sha256 hex>"`, `Last-Modified` (second precision), `Accept-Ranges`, `Repr-Digest`, `Cache-Control`;
- evaluates the conditional headers in the order of RFC 9110 section 13.2.2 (`304`, `412`);
- evaluates `Range` and `If-Range`: one satisfiable range is `206`, several ranges or an `If-Range` that does not match give the whole
  content, a start beyond the end is `416` with `Content-Range: bytes */size`;
- declares `Content-Length` for 200 and 206 and never uses the chunked coding;
- opens the body (`Store.Open`, passed to `ServeItem` as a function: a whole body through the verifying reader that holds back the last bytes, a range as stored) **before**
  the status line, so an object-store failure is a problem the client can read; an error in the middle of the body ends in
  `panic(http.ErrAbortHandler)`, which cuts the connection: the client sees a truncated transfer, never a complete corrupted file.

Any replica answers a resumed range alike, because the ETag is the content address and the item is read from PostgreSQL. The
integration test `TestR_ARCH_024_ADownloadCutAtTheShutdownDeadlineResumesOnAnotherReplica` cuts a download at the shutdown deadline
and resumes it on the other replica with `Range` and `If-Range`.

A folder without its trailing slash is `301` to the URL with it; the folder (or the root) with the slash is the directory index:
HTML for `Accept: text/html`, JSON otherwise, in pages (`limit`, `cursor`), names HTML-escaped and every link `./` plus the
percent-encoded name.

## Delete

`Delete` decides `delete` on the path, then hands the subtree to `items.Store.Delete` with the decision on every item as its
`Check` callback (each path on the coordinate its format gives it) and, as `Extra`, the recorded write followed by the format's
`CheckRemoval` with the paths of the files the removal takes away: for npm, `FilesSeam.CheckRemoval` locks the row of every
package of those files in name order and asks `Ownership.EnforceTx` with `delete` (Q-AUTHZ-161), so one package of another
owner refuses the whole delete. Up to `storage.delete.max_sync_items` items the store lists the subtree in a
transaction, asks the check about every item (403 and nothing changes if one is refused; the message names the path the caller
sent), deletes the files by id, tells the storage hook, writes one `artifact.deleted` per file, removes the folders deepest first
(each only if it is empty then) and the empty implicit folders above. A larger subtree is checked in full at the request and
removed by the job `items-delete` (the store's `RunDeleteJob`, which decides nothing) in batches of `storage.delete.batch_size` (files first, then the folders), answered `202` with
the job. The job carries the highest item id the check saw (`DeleteArgs.MaxID`) and removes only items up to it, so a file that is
deployed into the subtree afterwards stays (Q-STOR-143). A virtual repository holds no items: a delete there is `405`, as a deploy
is. Items are deleted for good: the trash can is P1 (Q-STOR-19). The `Extra` of a job runs in the transaction that queues it, with
the files the check saw, so an ownership refusal queues nothing. The items API deletes with `Strict` (patterns and the disabled flag,
as `/files/`) for every caller who does not hold `content:bypass` (Q-AUTHZ-162).

## Listings and statistics

`Children` of the generic layer decides the folder, and `items.Store.Children` reads a keyset batch from PostgreSQL and asks the
layer's `Visibility` callback, which decides `read` for each entry with one `PathsAllowed` call (the request's index, no audit
event per omitted entry), and continues until the page is full or `scanLimit` rows were read (Q-STOR-20). A batch is `limit + 1`
rows and doubles after each batch that left the page short, up to `maxChunk` (5,000), so a page costs a few dozen queries at most.
The cursor is the path of the page's last entry; a page that ended at `scanLimit` beyond its last visible entry carries
`hiddenPosition` (a NUL) and the item id of the last row read instead, which `cursorPath` turns back into a path with
`ItemPathByID`, refusing an id outside the repository or below another folder as `400 invalid_cursor` (Q-STOR-62). `items.Stats` counts a download after its body was copied (200, or 206 from byte 0; not HEAD, 304 or
`?stats=false`) and flushes with one upsert in item id order every `storage.stats.flush_interval`; `repository.Activity` writes
`last_activity_at` once a minute and at shutdown (R-REPO-012). Both are flushed by `filesComponent.Flush` (R-ARCH-024 step 6).

## What the generic layer needs of the decision point

The service takes the request's `edge.Access` through the three-method interface `files.Access`:

| Method | Why |
|---|---|
| `Decide(ctx, coordinate)` | every file and listing decision; the endpoint ceiling of R-EP-013 applies inside it, from the subject |
| `PathsAllowed(ctx, base, action, paths, classify)` | the entries of a listing, the items of a subtree and the `overwrite` question, on the request's index, without an audit event per refused entry; `classify` gives each path its format's package and version |
| `Index()` | the index a format's rule in a write transaction is decided on: the ownership question of `CheckRemoval` |
| `RecordAllowed(ctx, tx, coordinate, decision)` | the audit row of an allowed write, in the item store's transaction |

A key the grant index does not hold yet (a repository created through another replica) is made known when the entry point begins
the request's index (`Decider.Begin(ctx, sub, key)`, R-ARCH-011 step 2, Q-ARCH-40).

## Tests

| Tier | Where | Proves |
|---|---|---|
| unit | [internal/files](../../internal/files/files_test.go), [internal/items](../../internal/items/items_test.go), [edge](../../internal/api/edge/) (`content_test.go`, `conditional_test.go`, `digest_test.go`, `links_test.go`), [rawfiles](../../internal/api/rawfiles/) (`rawfiles_test.go`, `access_test.go`), [httpserver/content_test.go](../../internal/httpserver/content_test.go) | the generic layer's decisions on one holder and the post-body re-check, the content response (R-ARCH-007 headers, 200/206/304/412/416, HEAD without an open, abort on a failed body, counting), range and conditional rules, `Repr-Digest`, the item description, the directory index escaping, invalid paths before everything else, one index per request, route parity with the document, the content header wrapper |
| integration | [test/integration/files](../../test/integration/files/) | E1.4 (201 then 200, a range of 100 bytes, digest mismatch, `evil.svg`), R-GENERIC-002 to -007, hidden existence byte for byte, the overwrite permission, listings filtered by grants, folder and job deletes, statistics and activity, the 500 MiB upload through the spool (heap and spool file measured), a download cut at the shutdown deadline and resumed on another replica, uploads during a rollout restart, concurrent writers and the folder protocol, the item store's commit order and rollback and the delete hooks ([items.md](items.md)), the repository API (contract suite of E1.6, validation, ETag views, merge patch, delete job, idempotency across replicas) |

Run the area with the shared stack:
`set -a; . .dev/integration/env; set +a; go test -count=1 -tags integration ./test/integration/files/...`.
Every replica of the suite is a whole web request path with its own pools, change log, grant index, blob store and spool, behind
the middleware chain and the real handlers; only the authentication stage is a test double (a bearer name is a principal).
Valkey is not part of it: nothing in the transfer needs it.

## Decisions

[Q-STOR-17 to Q-STOR-21, Q-STOR-143 to Q-STOR-146](../spec/storage.md#decisions), [Q-ARCH-40 to Q-ARCH-43, Q-ARCH-206 to Q-ARCH-208](../spec/architecture.md#decisions),
[Q-GENERIC-6](../spec/formats/generic.md#decisions), [Q-API-32 to Q-API-34](../spec/api-and-ui.md#decisions).
