# Storage

Status: specification — not implemented

This page specifies how trove keeps package content and the metadata that describes it: the
content-addressed blob store on S3-compatible object storage (INV-4), the PostgreSQL schema for
repositories, items, packages, versions, blobs, blob references and properties (INV-3), the upload
pipeline (spooling, single-pass hashing, size limits, multipart transfer, deduplication, deploy by
checksum), the download path (streaming, HTTP range and conditional requests, integrity
verification, optional presigned redirects per repository), item operations (information,
listing, delete, copy, move, properties), the trash can, garbage collection that is safe against
concurrent uploads, the integrity scrub and orphan sweep, quotas and capacity thresholds, retention
and cleanup policies, the S3 configuration and credential chain, the consistency assumptions about
the object store, and the behavior while the object store is unavailable. Repository semantics
(classes, virtual resolution, pull-through) are in
[repositories-and-proxy.md](repositories-and-proxy.md); the permission model is in
[authorization.md](authorization.md); the format plug-in interface and the job framework are in
[architecture.md](architecture.md); Helm values, backup and failure drills are in
[ha-and-helm.md](ha-and-helm.md).

Covers: the catalog pages [storage-and-blobs.md](../features/storage-and-blobs.md) (`STOR`),
[retention-and-cleanup.md](../features/retention-and-cleanup.md) (`RET`) and
[artifacts.md](../features/artifacts.md) (`ART`, the content-handling entries), plus OPS-066 and
OPS-067 from [operations-and-monitoring.md](../features/operations-and-monitoring.md).

## Terms

| Term | Meaning on this page |
|---|---|
| Blob | One immutable byte sequence, identified by its SHA-256, stored once as one S3 object. |
| Item | Anything addressable by repository key and path: a file or a folder. A file item references exactly one blob. |
| Reference | A row in any table that points at a blob through a `blob_sha256` foreign key (file items, including items in the trash can; stored run reports; cached documents of other subsystems). |
| Package, version | The logical unit a format defines (an npm package and its versions, a NuGet package id and its versions). A version groups one or more file items. |
| Lead item | The file item whose timestamps, downloads and properties stand for its whole version in retention decisions (npm: the tarball; NuGet: the `.nupkg`). |
| Spool | Replica-local temporary storage that holds an upload while it is hashed and before it is written to S3. |
| Principal | A user, service account or CI identity as defined in [identity-and-authentication.md](identity-and-authentication.md). |

## Handover to other pages

Catalog entries of the three source pages that this page does not own:

| Catalog IDs | Owner | Reason |
|---|---|---|
| ART-006, ART-040, ART-041, ART-048, ART-049 | [api-and-ui.md](api-and-ui.md) | UI screens; they consume the APIs specified here. |
| ART-031 | [authorization.md](authorization.md) | Effective permissions of an item are an evaluation of the permission model. |
| ART-050, RMT-017, RMT-021, RMT-028, REPO-019, REPO-025, REPO-027 | [repositories-and-proxy.md](repositories-and-proxy.md) | Repository settings and remote-cache metadata semantics. This page specifies the storage mechanisms those settings drive (R-STOR-014, R-STOR-044, R-STOR-045). |
| OPS-065 | [operations-and-observability.md](operations-and-observability.md) | The overview of maintenance jobs; the storage jobs themselves are specified here (R-STOR-025 to R-STOR-028). |
| SRCH-008 | no page yet | Checksum search; the indexes it needs are defined in R-STOR-003. |

Storage-facing hooks the format plug-in interface in [architecture.md](architecture.md) has to
offer, used throughout this page:

| Hook | Called | Purpose |
|---|---|---|
| `IngestOptions` | before an upload is spooled | Expected digests from the protocol (npm `integrity`, OCI digest), whether SHA-512 is needed (always computed, see Q-STOR-2), media type. |
| `OnCommit` | inside the commit transaction of an upload | Create or update the package and version rows, mark the lead item, write format metadata. |
| `OnItemsRemoved`, `OnItemsRestored` | inside the delete, trash and restore transactions | Keep format indexes consistent (an npm packument no longer lists a removed version). |
| `RemoveVersion` | by retention and cap enforcement | Remove every item that belongs only to one version (an OCI tag with its manifest and exclusively owned children). |
| `VersionGroup` | after a push, for repository caps | Name the group whose variants a cap counts (snapshot base version, image name). |

## Blob store layout

### R-STOR-001 · Content-addressed object key layout
Priority: **MVP** · Covers: STOR-001

Every blob MUST be stored as exactly one S3 object whose key is derived only from its SHA-256:

```text
<prefix>/sha256/<h0h1>/<h2h3>/<h0…h63>
```

`<h0…h63>` is the lowercase hexadecimal SHA-256 of the content; `<h0h1>` and `<h2h3>` are its
first and second byte. Example: content with SHA-256
`5be0c1d2…` *(example)* lies at `trove/sha256/5b/e0/5be0c1d2…`. With an empty prefix the key
starts at `sha256/` (no leading slash).

- The key MUST NOT contain any part of a repository key, item path, file name or user input.
  Repository paths live only in PostgreSQL; the same blob serves any number of items in any
  number of repositories.
- The two-level fan-out (65,536 leaf prefixes) is fixed and not configurable. It spreads requests
  over many key prefixes, which object stores use to partition request rates, and it lets the
  orphan sweep (R-STOR-026) work prefix by prefix and resume.
- trove writes only these key families below `<prefix>/`:

  | Key family | Content | Lifecycle |
  |---|---|---|
  | `sha256/…` | Blobs | Created by the upload pipeline, deleted only by garbage collection (R-STOR-024) and the orphan sweep (R-STOR-026). |
  | `_uploads/<session-uuid>/<seq>` | Staged chunks of multi-request upload sessions (R-STOR-011) | Deleted on finalize, cancel or expiry. |
  | `_probe/<replica-id>` | Startup and conformance probe objects (R-STOR-035) | Deleted after each probe. |

  Any other object below `<prefix>/` is foreign: trove reports it and never deletes it.
- Objects are written with the bucket's default storage class unless
  `storage.s3.storage_class` names another (R-STOR-030). Object content type is
  `application/octet-stream`; response headers come from item metadata, never from object
  metadata.

Acceptance criteria:

- After `curl -fsS -H "Authorization: Bearer $TROVE_TOKEN" -T widget.bin https://trove.example.com/files/generic-local/tools/widget.bin`
  *(example)*, `aws s3api head-object --bucket trove-blobs --key trove/sha256/<h0h1>/<h2h3>/<hex>`
  succeeds, where `<hex>` is the output of `shasum -a 256 widget.bin`.
- Uploading the same file to a second repository and a second path creates no second object
  (`aws s3api list-objects-v2 --prefix trove/sha256/<h0h1>/<h2h3>/` lists it once).
- A path containing `../`, `%2F` or a key of another tenant has no effect on the object key (unit
  test over the key function: input is a 32-byte digest, nothing else).

### R-STOR-002 · Digest set per blob and per file
Priority: **MVP** · Covers: ART-008, STOR-004

- The upload pipeline MUST compute SHA-256, SHA-1, MD5 and SHA-512 of every blob in one pass over
  the bytes (R-STOR-004) and store all four on the blob row. SHA-256 is the identity; SHA-1 and MD5
  exist only for client compatibility and search and MUST NOT be used to decide that two contents
  are equal (deduplication, deploy by checksum).
- Each file item MUST additionally keep the digests the deploying client declared (*declared
  digests*), separately from the computed ones, together with a flag that records a declared
  digest that disagreed with the content (only reachable through sidecar uploads, R-STOR-010; a
  mismatching declaration on the upload request itself rejects the upload).
- Digests MUST appear in: the deploy response (R-STOR-007), item information (R-STOR-016), listings
  (SHA-256 and SHA-1 per file, R-STOR-017), download responses (`ETag` and `Repr-Digest`,
  R-STOR-012) and digest sidecar files (R-STOR-010).
- Clients declare digests with the `Repr-Digest` request field of
  [RFC 9530](https://www.rfc-editor.org/rfc/rfc9530.html) (`sha-256`, `sha-512`); SHA-1 and MD5
  declarations are accepted only through sidecar files and format protocols that carry them (npm
  `shasum`).

Acceptance criteria:

- For a deployed file, `GET /api/v1/repositories/{repoKey}/items?path=…` returns `digests.sha256`,
  `digests.sha1`, `digests.md5` and `digests.sha512` equal to `shasum -a 256`, `shasum -a 1`,
  `md5sum` and `shasum -a 512` of the file.
- `curl -T f.bin -H "Repr-Digest: sha-256=:<base64 of a wrong digest>:" …` answers `409` and
  creates no item and no blob.

## Metadata schema

### R-STOR-003 · PostgreSQL schema for repositories, items, packages, blobs, references and properties
Priority: **MVP** · Covers: ART-001, STOR-001, STOR-002

The tables below are the storage core. Other pages add tables and columns through expand/contract
migrations (INV-3); they MUST follow the reference rules at the end of this entry.

- `repositories` holds identity and class; the configuration document `config` belongs to
  [repositories-and-proxy.md](repositories-and-proxy.md) except its `storage` member, which this
  page defines (`maxUploadSize`, `quota`, `presignedDownloads`, `checksumPolicy`).
- `items` holds files and folders. A file references one blob. Folders are rows so that they can
  carry properties and be created explicitly; implicit folders (created as parents of a file) are
  removed when their last child goes, explicit ones stay.
- Trashed items stay in `items` with `trashed_at` set; they are still references, so their blobs
  survive until the trash can is emptied (R-STOR-022). The path uniqueness index covers only
  untrashed rows, so a path can be trashed several times and redeployed.
- `packages` and `package_versions` are filled by format plug-ins in the commit transaction;
  `items.version_id` links files to their version and `package_versions.lead_item_id` names the
  lead item.
- Paths are stored without a leading slash, with `COLLATE "C"`, so that byte order defines listing
  order and a subtree is a plain index range (`path >= 'a/b/' AND path < 'a/b0'`).

Path rules enforced on every write (protocol plug-ins may be stricter): valid UTF-8 in Unicode
NFC; at most 1,024 bytes; segments separated by `/`, each 1–255 bytes; no empty, `.` or `..`
segment; no control characters (U+0000–U+001F, U+007F) and no backslash. Violations answer `400`.

<details>
<summary>DDL sketch: core storage tables</summary>

```sql
CREATE TABLE repositories (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key         text NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),  -- INV-5
    format      text NOT NULL,
    class       text NOT NULL CHECK (class IN ('local', 'remote', 'virtual')),
    config      jsonb NOT NULL DEFAULT '{}',   -- repositories-and-proxy.md; member "storage" defined here
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE blobs (
    sha256            bytea PRIMARY KEY CHECK (octet_length(sha256) = 32),
    sha1              bytea NOT NULL CHECK (octet_length(sha1) = 20),
    md5               bytea NOT NULL CHECK (octet_length(md5) = 16),
    sha512            bytea NOT NULL CHECK (octet_length(sha512) = 64),
    size              bigint NOT NULL CHECK (size >= 0),
    state             text NOT NULL DEFAULT 'live' CHECK (state IN ('live', 'deleting')),
    content_status    text NOT NULL DEFAULT 'ok' CHECK (content_status IN ('ok', 'missing', 'corrupt')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    state_changed_at  timestamptz NOT NULL DEFAULT now(),
    object_deleted_at timestamptz,             -- set by GC once the S3 delete has succeeded
    checked_at        timestamptz,             -- last existence check (R-STOR-027)
    deep_checked_at   timestamptz              -- last full re-hash (R-STOR-027)
);
CREATE INDEX blobs_sha1     ON blobs (sha1);   -- sidecars, checksum search
CREATE INDEX blobs_md5      ON blobs (md5);
CREATE INDEX blobs_deleting ON blobs (state_changed_at) WHERE state = 'deleting';
CREATE INDEX blobs_damaged  ON blobs (content_status) WHERE content_status <> 'ok';
CREATE INDEX blobs_check    ON blobs (checked_at NULLS FIRST) WHERE state = 'live';

CREATE TABLE packages (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name          text NOT NULL,               -- normalized by the format (npm: as published; NuGet: lowercase id)
    display_name  text NOT NULL,
    attributes    jsonb NOT NULL DEFAULT '{}', -- format-owned (npm dist-tags, NuGet owners …)
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, name)
);

CREATE TABLE package_versions (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    package_id       bigint NOT NULL REFERENCES packages (id) ON DELETE CASCADE,
    version          text NOT NULL,            -- normalized (NuGet: normalized SemVer 2.0)
    display_version  text NOT NULL,
    lead_item_id     bigint,                   -- FK added below (cycle with items)
    attributes       jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    created_by       uuid,                     -- principal id (identity-and-authentication.md)
    trashed_at       timestamptz
);
CREATE UNIQUE INDEX package_versions_live ON package_versions (package_id, version) WHERE trashed_at IS NULL;

CREATE TABLE items (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id            bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    path               text COLLATE "C" NOT NULL,
    parent_path        text COLLATE "C" NOT NULL,   -- '' for top-level entries
    kind               text NOT NULL CHECK (kind IN ('file', 'folder')),
    blob_sha256        bytea REFERENCES blobs (sha256),
    size               bigint,
    media_type         text,
    declared_sha256    bytea,
    declared_sha512    bytea,
    declared_sha1      bytea,
    declared_md5       bytea,
    declared_mismatch  boolean NOT NULL DEFAULT false,
    explicit_folder    boolean NOT NULL DEFAULT false,
    generated          boolean NOT NULL DEFAULT false, -- format-generated metadata document
    version_id         bigint REFERENCES package_versions (id) ON DELETE SET NULL,
    upstream_url       text,                           -- remote caches; further cache columns: repositories-and-proxy.md
    created_at         timestamptz NOT NULL DEFAULT now(),
    created_by         uuid,
    modified_at        timestamptz NOT NULL DEFAULT now(),  -- content or location changed
    modified_by        uuid,
    updated_at         timestamptz NOT NULL DEFAULT now(),  -- any change, including properties
    trashed_at         timestamptz,
    trashed_by         uuid,
    trash_operation    uuid,                           -- groups the items of one delete call
    CHECK ((kind = 'file') = (blob_sha256 IS NOT NULL)),
    CHECK ((kind = 'file') = (size IS NOT NULL))
);
CREATE UNIQUE INDEX items_live_path ON items (repo_id, path) WHERE trashed_at IS NULL;
CREATE INDEX items_children ON items (repo_id, parent_path, path) WHERE trashed_at IS NULL;
CREATE INDEX items_blob     ON items (blob_sha256);
CREATE INDEX items_version  ON items (version_id);
CREATE INDEX items_trash    ON items (trashed_at, repo_id, path) WHERE trashed_at IS NOT NULL;

ALTER TABLE package_versions
    ADD FOREIGN KEY (lead_item_id) REFERENCES items (id) ON DELETE SET NULL;

CREATE TABLE item_properties (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id  bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    key      text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
    value    text NOT NULL CHECK (char_length(value) <= 2400)
);
-- values may exceed the btree entry limit; index a hash and a prefix instead of the full value
CREATE UNIQUE INDEX item_properties_unique ON item_properties (item_id, key, md5(value));
CREATE INDEX item_properties_lookup ON item_properties (key, left(value, 256));

CREATE TABLE item_download_stats (
    item_id           bigint PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    downloads         bigint NOT NULL DEFAULT 0,
    last_download_at  timestamptz,
    last_downloader   uuid                     -- principal id; NULL for anonymous or erased principals
);

CREATE TABLE blob_gc_queue (                    -- no FK: the blob row may already be gone
    sha256       bytea PRIMARY KEY,
    enqueued_at  timestamptz NOT NULL
);

CREATE TABLE repository_usage (
    repo_id        bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
    logical_bytes  bigint NOT NULL DEFAULT 0,   -- sum of untrashed file sizes
    files          bigint NOT NULL DEFAULT 0,
    folders        bigint NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE storage_usage_deltas (             -- append-only, folded into repository_usage by a job
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id        bigint,                      -- NULL: physical (blob) delta
    logical_bytes  bigint NOT NULL DEFAULT 0,
    files          bigint NOT NULL DEFAULT 0,
    folders        bigint NOT NULL DEFAULT 0,
    physical_bytes bigint NOT NULL DEFAULT 0,
    blobs          bigint NOT NULL DEFAULT 0
);
```

</details>

<details>
<summary>DDL sketch: reference guard and release triggers</summary>

```sql
-- Every table with a blob reference names the column blob_sha256, declares
-- FOREIGN KEY (blob_sha256) REFERENCES blobs (sha256), indexes it, and installs both triggers.

CREATE FUNCTION blob_ref_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.blob_sha256 IS NULL
       OR (TG_OP = 'UPDATE' AND NEW.blob_sha256 IS NOT DISTINCT FROM OLD.blob_sha256) THEN
        RETURN NEW;
    END IF;
    -- FOR SHARE conflicts with the FOR UPDATE lock garbage collection takes (R-STOR-024)
    PERFORM 1 FROM blobs WHERE sha256 = NEW.blob_sha256 AND state = 'live' FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'blob % is not live', encode(NEW.blob_sha256, 'hex')
              USING ERRCODE = 'AS001';      -- mapped to a retry by the commit protocol (R-STOR-005)
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION blob_ref_release() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO blob_gc_queue (sha256, enqueued_at)
    SELECT DISTINCT o.blob_sha256, now()
      FROM old_rows o
     WHERE o.blob_sha256 IS NOT NULL
    ON CONFLICT (sha256) DO UPDATE SET enqueued_at = EXCLUDED.enqueued_at;
    RETURN NULL;
END $$;

CREATE TRIGGER items_blob_guard BEFORE INSERT OR UPDATE OF blob_sha256 ON items
    FOR EACH ROW EXECUTE FUNCTION blob_ref_guard();
CREATE TRIGGER items_blob_release_del AFTER DELETE ON items
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release();
-- UPDATE OF blob_sha256 (overwrite in place) uses a variant that compares old and new rows.
```

</details>

Reference rules (normative for every page that adds a reference to a blob):

1. The referencing column MUST be named `blob_sha256`, MUST carry a foreign key to
   `blobs(sha256)`, MUST be indexed, and the table MUST install the guard and release triggers.
2. No code path may insert a reference except through the guard; the guard is in the database, so
   a forgotten application check cannot create a reference to a blob that is being deleted.
3. Garbage collection derives the list of referencing tables from the PostgreSQL catalog
   (foreign keys whose target is `blobs`) inside each collection transaction (R-STOR-024). A table
   added by a newer migration is therefore honored even by an older replica during a rolling
   upgrade.
4. A migration test MUST fail when a foreign key to `blobs` lacks the index or either trigger.

Usage counters (`repository_usage`, physical totals) are maintained by statement-level triggers
on `items` and `blobs` that append to `storage_usage_deltas`; a job folds the deltas every 30
seconds (`storage.usage.fold_interval`). Writers never update a shared counter row, so concurrent
uploads to one repository do not contend on it.

Acceptance criteria:

- `trove migrate` on an empty database creates the tables; the migration test lists every
  foreign key to `blobs` with index and triggers present.
- A property value of 2,400 four-byte characters can be stored and found by key and value.
- Two items with paths `a/b` and `a/b0` in one repository: a subtree query for `a/b` returns only
  `a/b` and its descendants.

## Upload

### R-STOR-004 · Upload pipeline: admission, spool and single-pass hashing
Priority: **MVP** · Covers: STOR-019, STOR-001

Every write of content (raw `PUT /files/…`, npm publish, NuGet push, remote-cache fill, report
generation) MUST go through one pipeline:

```text
request ─▶ authorize (deploy, overwrite) ─▶ admission ─▶ spool + hash (one pass)
        ─▶ verify size and declared digests ─▶ commit protocol (R-STOR-005)
        ─▶ 201 to the client ─▶ remove spool file
```

1. **Admission.** Before reading the body the pipeline MUST reject:
   - a declared `Content-Length` above the effective maximum upload size with `413`
     ([RFC 9110 §15.5.14](https://www.rfc-editor.org/rfc/rfc9110#section-15.5.14)); the
     effective maximum is the smaller of `storage.upload.max_size` and the repository's
     `config.storage.maxUploadSize`;
   - an upload that would exceed a quota with `507` (R-STOR-029);
   - an upload for which the spool budget cannot be reserved, or whose spool volume has less than
     `storage.spool.min_free_bytes` free, with `503` and `Retry-After`.

   Rejecting before reading means no `100 Continue` is sent to clients that use
   `Expect: 100-continue`. Bodies without a length reserve spool budget in increments while
   streaming and fail with `413` as soon as they pass the maximum.
2. **Spool.** Bodies with a known length up to `storage.spool.memory_threshold` MAY be buffered in
   memory; all others MUST be written to a new file in `storage.spool.dir`, created with mode
   `0600`, named by a random UUID. The spool is replica-local scratch space (an `emptyDir` volume
   in the Helm chart, [ha-and-helm.md](ha-and-helm.md)); it never holds acknowledged data.
3. **Hash.** While spooling, SHA-256, SHA-1, MD5 and SHA-512 MUST be computed in the same pass
   (one read of the stream, fanned out to the four hash states) and the byte count recorded.
4. **Verify.** After the body ends the pipeline MUST compare the byte count with
   `Content-Length` (mismatch: `400`) and every declared or protocol-supplied digest
   (`Repr-Digest`, npm `integrity`/`shasum`, OCI digest) with the computed one (mismatch: `409`,
   nothing stored).
5. **Commit.** R-STOR-005 writes the object if needed and commits the metadata. The client
   receives `201` only after the object is durable in S3 and the commit transaction has
   committed: a `201` always means "durable in the object store and visible".
6. **Cleanup.** The spool file and its budget reservation MUST be released on every exit path
   (success, error, client disconnect). At startup a replica MUST delete every file in its spool
   directory, because no spool file survives its request.

Remote-cache fills use the same pipeline; whether the waiting client is served while the upstream
body is still being spooled is decided in [repositories-and-proxy.md](repositories-and-proxy.md).

Configuration:

```yaml
storage:
  upload:
    max_size: 10GiB           # default (proposed); must not exceed storage.spool.max_bytes
  spool:
    dir: /var/lib/trove/spool  # default (proposed)
    max_bytes: 50GiB          # default (proposed); budget shared by all concurrent uploads of a replica
    min_free_bytes: 2GiB      # default (proposed)
    memory_threshold: 1MiB    # default (proposed)
    warn_percent: 80          # default (proposed); metric and log warning above this budget use
```

Metrics (names per [operations-and-observability.md](operations-and-observability.md)
conventions): `trove_storage_spool_reserved_bytes`, `trove_storage_spool_free_bytes`,
`trove_storage_upload_rejections_total{reason}`.

Acceptance criteria:

- With `maxSize: 10MiB`, `curl -T 11mb.bin …/files/generic-local/x.bin` answers `413` without the
  body being transferred (`curl -v` shows no `100 Continue`).
- Killing the client mid-upload leaves no spool file and no blob row; the spool budget metric
  returns to its previous value.
- `npm publish` of a tarball whose `dist.integrity` was edited to a wrong value answers an error
  and stores nothing.
- A replica restarted with files in its spool directory starts with an empty spool directory.

### R-STOR-005 · Blob commit protocol and deduplication
Priority: **MVP** · Covers: STOR-001, STOR-002, STOR-019

After hashing, the pipeline knows the SHA-256 and holds the content in the spool. The commit
protocol MUST guarantee: *a committed reference always points at a blob row in state `live` whose
object exists in S3*. It works together with garbage collection (R-STOR-024) and needs no lock
held across an S3 transfer.

Existence of a blob is decided by its PostgreSQL row, not by an S3 `HEAD` (INV-4 refined: the
row is the authority; an object without a row is an orphan, R-STOR-026).

```text
putDone := false
loop (at most storage.upload.commitAttempts times):
  BEGIN (READ COMMITTED)
  SELECT state, content_status FROM blobs WHERE sha256 = $h FOR SHARE
  case no row:
      if not putDone or now - putAt > storage.upload.commitDeadline:
          ROLLBACK; write object (R-STOR-006); putDone, putAt := true, now; continue
      INSERT INTO blobs (…, state = 'live') ON CONFLICT DO NOTHING
      if no row inserted: ROLLBACK; continue          -- a concurrent uploader won; re-read
  case state = 'deleting':
      ROLLBACK; putDone := false                      -- a GC delete may still land on the key
      wait until the row is gone (≤ storage.upload.blob_wait_timeout); continue
  case state = 'live' and content_status <> 'ok':
      ROLLBACK; write object; mark content_status = 'ok' in its own transaction; continue
  case state = 'live':                                -- deduplication hit: no S3 write
  insert or update the item (guard trigger re-checks under the same lock),
  format OnCommit hook, properties, audit event, event outbox
  COMMIT
```

- The `FOR SHARE` lock is held from the state check to commit; garbage collection needs a
  `FOR UPDATE` lock on the same row to start a deletion, so it either sees the new reference or
  makes this transaction wait and then fail the guard.
- `putDone` is reset whenever a `deleting` row is seen: an object written before a collection
  claimed the row may have been deleted by it, so the object is written again after the row is
  gone.
- `storage.upload.commit_deadline` bounds the time between a successful object write and the
  insert of a new blob row. The orphan sweep deletes only objects older than
  `storage.orphans.min_age` (R-STOR-026); the deadline MUST be less than half of that age, so an
  object this uploader wrote cannot be swept before its row exists.
- A failure after a successful object write and before commit (database error, client gone)
  leaves either an orphan object (swept later) or a `live` row without references (collected by
  the full sweep after the grace period); the pipeline SHOULD enqueue the digest in
  `blob_gc_queue` on that path so that the incremental collection finds it.
- After `commit_attempts` the request fails with `503` and `Retry-After`.
- Overwrite: when an untrashed item exists at the path, the caller MUST hold `overwrite` in
  addition to `deploy` (INV-9), unless the existing item references the same blob, in which case
  the request succeeds as a no-op with `200` and needs only `deploy`. A replaced user-deployed
  file goes to the trash can when it is enabled (R-STOR-022, Q-STOR-5); a replaced `generated`
  item does not. Two concurrent uploads to one path serialize on the path index; the second is
  an overwrite.
- Item timestamps: `created_at` and `modified_at` are the commit time of the transaction, so the
  creation time is never later than the last-modified time.

Configuration:

```yaml
storage:
  upload:
    commit_attempts: 5         # default (proposed)
    commit_deadline: 1h        # default (proposed); must be < storage.orphans.min_age / 2
    blob_wait_timeout: 120s    # default (proposed); must exceed storage.gc.delete_settle
```

Acceptance criteria:

- Publishing the same npm tarball to `npm-a` and `npm-b`
  (`npm publish --registry https://trove.example.com/api/npm/npm-b/`) causes exactly one S3
  `PutObject` (S3 request metric `trove_storage_s3_requests_total{op="put"}` grows by one) and
  two item rows referencing one blob row.
- Race test (Go integration test against PostgreSQL and MinIO, run in CI): 32 goroutines upload
  the same content to random paths while other goroutines delete those items and garbage
  collection runs with `gracePeriod: 0s` in a loop for 10 minutes; afterwards every untrashed and
  trashed file item's blob is `live` and `aws s3api head-object` succeeds for each of them.
- An upload that finds the row in state `deleting` succeeds after the collection finishes and the
  object exists afterwards.

### R-STOR-006 · Transfer to S3: single request, multipart, thresholds
Priority: **MVP** · Covers: STOR-023

- Content up to `storage.s3.multipart.threshold` MUST be written with one `PutObject`; larger
  content MUST use a multipart upload whose parts are read directly from the spool file (section
  readers on the file; no per-part memory buffers).
- Part size is `storage.s3.multipart.part_size`, raised when needed so that the content fits into
  10,000 parts (rounded up to whole MiB). S3 limits as documented by AWS: parts of 5 MiB to 5 GiB
  except the last, at most 10,000 parts, objects up to 48.8 TiB
  ([multipart upload limits](https://docs.aws.amazon.com/AmazonS3/latest/userguide/qfacts.html)).
  S3-compatible stores may have lower limits; `storage.upload.max_size` must respect them.
- Each upload uses at most `storage.s3.multipart.concurrency` parallel part requests; a replica
  uses at most `storage.s3.multipart.max_concurrent_parts` in total.
- Because the digests are known before the transfer starts, every `PutObject` and every part
  SHOULD carry a payload checksum the store verifies (`storage.s3.payload_checksum`): SHA-256
  where the store supports additional checksums (AWS S3), otherwise `Content-MD5`. A store-side
  mismatch fails the transfer.
- On any failure the pipeline MUST call `AbortMultipartUpload`; the orphan sweep aborts uploads
  left behind by crashed replicas (R-STOR-026), and operators SHOULD also configure a bucket
  lifecycle rule that aborts incomplete multipart uploads after one day
  ([AWS lifecycle rule](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpu-abort-incomplete-mpu-lifecycle-config.html)).
- After `CompleteMultipartUpload` the pipeline MUST confirm the object size with `HeadObject`
  before the row may become `live`.

Configuration:

```yaml
storage:
  s3:
    payload_checksum: sha256    # default (proposed): sha256 | md5 | none
    multipart:
      threshold: 100MiB         # default (proposed)
      part_size: 16MiB          # default (proposed); minimum 5MiB
      concurrency: 4            # default (proposed); parallel parts per upload
      max_concurrent_parts: 64  # default (proposed); per replica
```

Acceptance criteria:

- A 300 MiB generic upload produces one multipart upload with 19 parts (16 MiB parts) in the S3
  access log of MinIO, and `aws s3api head-object` reports 314,572,800 bytes.
- Killing the replica in the middle of a multipart upload leaves an incomplete multipart upload
  that `aws s3api list-multipart-uploads` shows until the next orphan sweep, which aborts it.
- With `payloadChecksum: none` against a store without additional-checksum support, uploads work.

### R-STOR-007 · Raw deploy and folder creation
Priority: **MVP** · Covers: ART-002

`PUT /files/{repoKey}/{path}` deploys one file into a local repository, or into the default
deployment target of a virtual repository ([repositories-and-proxy.md](repositories-and-proxy.md)).

| | |
|---|---|
| Request | Body: the content. Optional `Repr-Digest` (sha-256, sha-512); optional `Content-Type` (stored as media type); optional properties (R-STOR-009); `Authorization` per INV-8. |
| Response | `201 Created` with `Location` (the `/files` URL) and the item description below; `200` for an identical re-upload (R-STOR-005). |
| Permission | `deploy` on the path; additionally `overwrite` when replacing different content. |
| Errors | `400` invalid path or length mismatch; `401`; `403`; `404` unknown repository; `405` repository class does not accept deploys (remote, virtual without deployment target); `409` digest mismatch, or an existing item and no `overwrite` permission, or a format rule refuses the path; `413` too large; `503` spool or object store unavailable; `507` quota exceeded. Bodies are problem details per [api-and-ui.md](api-and-ui.md). |

`PUT /files/{repoKey}/{path}/` (trailing slash, empty body) creates an explicit folder and its
missing parents and answers `201`; an existing folder answers `200`.

Response sketch:

```json
{
  "repository": "generic-local",
  "path": "tools/widget/2.3/widget-2.3.bin",
  "kind": "file",
  "size": 48213,
  "mediaType": "application/octet-stream",
  "created": { "at": "2026-10-28T09:14:03.117Z", "by": "user:alice" },
  "digests": { "sha256": "5be0c1d2…", "sha512": "…", "sha1": "…", "md5": "…" },
  "declaredDigests": { "sha256": "5be0c1d2…" },
  "downloadUrl": "https://trove.example.com/files/generic-local/tools/widget/2.3/widget-2.3.bin",
  "itemUrl": "https://trove.example.com/api/v1/repositories/generic-local/items?path=tools%2Fwidget%2F2.3%2Fwidget-2.3.bin"
}
```

Format repositories accept raw deploys only where the format plug-in allows them (an npm
repository accepts tarballs only through `npm publish`); the plug-in answers `409` with a
reason otherwise.

Acceptance criteria:

- `curl -fsS -u ci:$TROVE_TOKEN -T widget.bin https://trove.example.com/files/generic-local/tools/widget.bin`
  answers `201` with the JSON above; repeating it answers `200`.
- `curl -X PUT …/files/generic-local/empty-folder/` answers `201`; the folder appears in the
  listing (R-STOR-017) with no children.
- A `PUT` to a virtual repository without a deployment target answers `405`.

### R-STOR-008 · Deploy by checksum and checksum-first transfer
Priority: **P1** · Covers: ART-004, STOR-003

A client can create a file item from content trove already stores without sending the bytes.

- **Explicit form.** `PUT /files/{repoKey}/{path}` with an empty body, the header
  `Trove-Deploy-By-Checksum: ?1` (a structured-field boolean) and
  `Repr-Digest: sha-256=:<base64>:`. If a `live` blob with that SHA-256 and `content_status = 'ok'`
  exists **and the caller can read at least one untrashed item that references it**, the item is
  created through the normal commit transaction and the answer is `201` with the R-STOR-007 item
  description. Otherwise the answer is `404` with problem type `content-not-found`, telling the
  client to upload normally. A `sha-512` member, if present, MUST also match.
- **Transparent form.** A normal upload that carries `Repr-Digest: sha-256=…` and
  `Expect: 100-continue` MAY be answered `201` before the body is read, when the same conditions
  hold; RFC 9110 allows a final response instead of `100 Continue`
  ([§10.1.1](https://www.rfc-editor.org/rfc/rfc9110#section-10.1.1)). Without the conditions the
  server sends `100 Continue` and the upload proceeds; the received bytes MUST then match the
  announced digest (`409` otherwise).
- Only SHA-256 identifies content here. SHA-1 and MD5 announcements are not accepted for lookup
  ([RFC 9530](https://www.rfc-editor.org/rfc/rfc9530.html) lists both as deprecated; chosen-prefix
  SHA-1 collisions are practical).
- Readability is evaluated with the permission evaluator of [authorization.md](authorization.md)
  over at most `storage.deploy_by_checksum.max_candidates` referencing items; if none of them is
  readable the answer is `404`, exactly as for unknown content.
- Downloads get the same shortcut through conditional requests (`If-None-Match` with the
  SHA-256 `ETag`, R-STOR-012).

Permission: `deploy` on the target path (and `overwrite` when replacing).

Configuration:

```yaml
storage:
  deploy_by_checksum:
    enabled: true          # default (proposed)
    max_candidates: 100    # default (proposed)
```

Acceptance criteria:

- After uploading `widget.bin` to `generic-a`, a user with read on `generic-a` runs
  `curl -X PUT -H "Trove-Deploy-By-Checksum: ?1" -H "Repr-Digest: sha-256=:$(openssl dgst -sha256 -binary widget.bin | base64):" …/files/generic-b/widget.bin`
  and gets `201`; no S3 request is made.
- A user without read on `generic-a` gets `404` for the same request.
- `curl -T big.bin -H "Expect: 100-continue" -H "Repr-Digest: sha-256=:…:"` for stored content
  completes without transferring the body (`curl -v` shows `201` and no `100 Continue`).

### R-STOR-009 · Properties attached at deploy time
Priority: **P1** · Covers: ART-003

- A raw deploy MAY carry properties as matrix parameters directly after the repository key:
  `PUT /files/{repoKey};ci.run=8812;git.sha=4f2a9c1/{path}` *(example)*. Each `key=value` pair
  becomes a property of the deployed file; a key repeated or a value list separated by `,` gives
  several values. Keys and values are percent-decoded once.
- Matrix parameters are recognized only in the repository-key segment. Repository keys cannot
  contain `;` (INV-5), so the syntax is unambiguous; file names that contain `;` stay legal and
  are never parsed. This position also serves build tools that template a deploy base URL and
  append the artifact path (Maven distribution URLs).
- Alternatively the header `Trove-Properties` carries a structured-field dictionary of string
  or inner-list values (`Trove-Properties: ci.run="8812", targets=("linux" "darwin")`
  *(example)*).
- Deploy-time properties are written in the commit transaction and follow the property model
  (R-STOR-020); they require `annotate` in addition to `deploy`. Without `annotate` the deploy
  fails with `403` and nothing is stored.

Acceptance criteria:

- `curl -T w.jar "https://trove.example.com/files/maven-local;build.number=17/com/acme/w/1.0/w-1.0.jar"`
  stores the file at `com/acme/w/1.0/w-1.0.jar` with property `build.number=17`.
- A file named `a;b=c.txt` uploaded to `…/files/generic-local/dir/a;b=c.txt` keeps its name and
  gets no property.

### R-STOR-010 · Client checksum policy and digest sidecar files
Priority: **P1** · Covers: ART-009

- Every repository has `config.storage.checksumPolicy`: `verify-client` or `trust-server`.
  Default (proposed): `verify-client` for the Maven-family formats, `trust-server` for all
  others.
- A request for `<file>.sha256`, `<file>.sha512`, `<file>.sha1` or `<file>.md5` through `/files`
  where no item with that exact path exists is a *sidecar* request:
  - `trust-server`: answer `200` with the lowercase hex digest from the blob row
    (`text/plain`).
  - `verify-client`: answer `200` only if the client declared that digest and it matched; `404`
    if it was never declared; `409` while a declared value disagrees.
- A `PUT` of a sidecar path where the target file exists is not stored as an item: the body is
  parsed as a hex digest and recorded as the declared digest of the target (match: `201`;
  mismatch: `declared_mismatch = true`, answer `201` under `trust-server`, `409` under
  `verify-client`). A sidecar uploaded before its file is kept in a short-lived pending table and
  applied when the file arrives (`storage.sidecars.pending_ttl`).
- A mismatching `Repr-Digest` on the upload of the file itself is always rejected (R-STOR-004);
  the policy governs only declarations that arrive separately.

Configuration:

```yaml
storage:
  sidecars:
    pending_ttl: 1h      # default (proposed)
```

Acceptance criteria:

- `mvn deploy` against a `verify-client` Maven repository uploads `.sha1` and `.md5` files; a
  following `curl …/w-1.0.jar.sha1` returns the digest; a manual `curl -T wrong.sha1 …/w-1.0.jar.sha1`
  makes the same request answer `409`.
- In a `trust-server` generic repository, `curl …/widget.bin.sha256` returns the digest without
  any sidecar ever being uploaded.

### R-STOR-011 · Multi-request upload sessions
Priority: **P1** · Covers: trove-only

Some protocols upload one blob over several requests (OCI chunked blob upload, see the OCI format
page once written; resumable generic uploads). Replicas are stateless (INV-2), so consecutive
requests of one session may reach different replicas; the session therefore MUST NOT depend on a
replica's spool.

- A session row in PostgreSQL holds the repository, principal, received byte count, chunk count,
  expiry and the serialized state of the four hash functions. Go's hash implementations support
  serializing and restoring their internal state
  ([`crypto/sha256`](https://pkg.go.dev/crypto/sha256) and the SHA-1, MD5 and SHA-512 packages),
  so any replica continues hashing where the previous request stopped.
- Each chunk is appended by streaming it to `<prefix>/_uploads/<session-uuid>/<seq>` while the
  restored hash states consume it; the session row is updated with optimistic locking on the
  byte count (a concurrent or out-of-order chunk answers `416`).
- Finalize assembles the blob: when every chunk except the last is at least 5 MiB, through a
  multipart upload with server-side part copies; otherwise by streaming the chunks through the
  finalizing replica into the final key. Then the commit protocol (R-STOR-005) runs and the
  staging objects are deleted.
- Sessions expire after `storage.uploads.session_ttl` without activity; the orphan sweep removes
  their staging objects (R-STOR-026).

```sql
CREATE TABLE upload_sessions (
    id            uuid PRIMARY KEY,
    repo_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    principal_id  uuid,
    received      bigint NOT NULL DEFAULT 0,
    chunks        integer NOT NULL DEFAULT 0,
    hash_state    bytea NOT NULL,              -- serialized SHA-256, SHA-1, MD5, SHA-512 states
    state         text NOT NULL CHECK (state IN ('open', 'finalizing', 'done', 'aborted')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL
);
```

Configuration:

```yaml
storage:
  uploads:
    session_ttl: 24h     # default (proposed)
```

Acceptance criteria:

- A chunked upload whose three chunks are sent through three different replicas (test pins each
  request to one pod) yields a blob whose SHA-256 equals the file's.
- A session idle for longer than `session_ttl` is aborted and its staging objects disappear after
  the next orphan sweep.

## Download

### R-STOR-012 · Streaming download, Range and conditional requests
Priority: **MVP** · Covers: ART-011

`GET` and `HEAD` on `/files/{repoKey}/{path}` (and every format endpoint that returns stored
content) resolve the item (virtual and remote resolution:
[repositories-and-proxy.md](repositories-and-proxy.md)) and stream its blob from S3.

- **Streaming.** The body MUST be copied from the S3 `GetObject` response to the client through
  a bounded buffer (`storage.download.buffer_size`), never spooled. Client disconnect cancels the
  S3 request.
- **HEAD and conditional requests** MUST be answered from PostgreSQL alone, without an S3 call.
- **Response fields:** `Content-Length`; `Content-Type` from the item; `ETag: "<sha256 hex>"`
  (a strong validator: the content address); `Last-Modified` from `modified_at`;
  `Accept-Ranges: bytes`; `Repr-Digest: sha-256=:<base64>:`, plus `sha-512` when the request
  asks for it with `Want-Repr-Digest` ([RFC 9530](https://www.rfc-editor.org/rfc/rfc9530.html)).
- **User content is never rendered as part of trove's origin.** Responses carrying stored
  content MUST include `X-Content-Type-Options: nosniff`,
  `Content-Security-Policy: sandbox; default-src 'none'` and
  `Content-Disposition: attachment; filename*=UTF-8''<percent-encoded file name>`.
- **Conditional:** `If-None-Match` (ETag) and `If-Modified-Since` answer `304`.
- **Range** ([RFC 9110 §14](https://www.rfc-editor.org/rfc/rfc9110#section-14)): a single byte
  range MUST be served with `206` and `Content-Range`, forwarded to S3 as a ranged `GetObject`;
  `If-Range` with the ETag or date is honored; an unsatisfiable range answers `416` with
  `Content-Range: bytes */<size>`. A request with several ranges is answered `200` with the full
  content (RFC 9110 permits ignoring `Range`).
- **Caching:** `Cache-Control: private, no-cache` for authenticated requests, `no-cache` for
  anonymous ones; clients revalidate with the ETag.
- **Errors:** `401`; `403` no `read`; `404` no such untrashed item; `500` with problem type
  `content-unavailable` when the blob's `content_status` is `missing` or `corrupt` (remote
  repositories instead treat the item as a cache miss and refetch); `503` with `Retry-After` when
  the object store is unreachable (R-STOR-036).
- A `GET` on a folder returns the directory index (R-STOR-017).
- `?stats=false` suppresses the download statistics update (R-STOR-015).

Permission: `read` on the item (anonymous where granted).

Configuration:

```yaml
storage:
  download:
    buffer_size: 256KiB    # default (proposed)
```

Acceptance criteria:

- `curl -r 0-99 -o part …/files/generic-local/widget.bin` answers `206` with 100 bytes equal to
  `head -c 100 widget.bin`.
- `curl -I …` answers `200` with the headers above while the S3 request counter does not grow.
- `curl -H 'If-None-Match: "<sha256>"' …` answers `304`.
- `curl -r 5000000000- …` on a small file answers `416` with `Content-Range: bytes */<size>`.
- An uploaded `evil.html` served from `/files` carries `Content-Disposition: attachment` and the
  sandbox CSP; a browser opening the URL downloads it instead of rendering it.
- `npm install @acme/widget --registry https://trove.example.com/api/npm/npm-all/` and
  `dotnet restore` against the NuGet source download through this path.

### R-STOR-013 · Download integrity verification
Priority: **MVP** · Covers: trove-only

- For full-content `GET` responses streamed through trove, the server MUST compute the SHA-256
  of the bytes it sends and hold back the final `storage.download.hold_back` bytes until the S3
  body has ended and the digest matches the blob's SHA-256.
- On a mismatch, or when S3 returns fewer or more bytes than the blob size, the server MUST NOT
  send the held-back bytes, MUST abort the response (HTTP/1.1: close the connection; HTTP/2 and
  HTTP/3: reset the stream), MUST set the blob's `content_status` to `corrupt`, and MUST emit an
  audit event and the metric `trove_storage_blobs_damaged_total{status="corrupt"}`. The client
  sees a truncated transfer, never a complete corrupted file.
- Ranged responses and presigned redirects (R-STOR-014) cannot be verified this way; see H-11.
- `storage.download.verify: false` disables the check (CPU-bound proxies; see Q-STOR-6).

Configuration:

```yaml
storage:
  download:
    verify: true          # default (proposed)
    hold_back: 64KiB      # default (proposed)
```

Acceptance criteria:

- Overwriting an object in MinIO with different bytes of the same length (`mc cp` directly into
  the bucket) and downloading the item with `curl --http1.1` makes `curl` report a partial
  transfer (exit code 18), and the blob shows `content_status = 'corrupt'`.

### R-STOR-014 · Presigned download redirect per repository
Priority: **P2** · Covers: STOR-032, STOR-033

Specified now because the download path must leave room for it; implementation is P2.

- A local or remote repository MAY set `config.storage.presignedDownloads: true` (default
  `false`). A remote repository qualifies only if it stores fetched content (the store-locally
  setting of [repositories-and-proxy.md](repositories-and-proxy.md)); enabling it on a
  pass-through remote repository is rejected with `422`.
- An eligible `GET` (repository switch on, item size at least `storage.s3.presign.min_size`,
  blob `content_status = 'ok'`, object store circuit closed, not a sidecar, not `HEAD`) is answered
  `302 Found` with `Location` set to an S3 presigned `GetObject` URL valid for
  `storage.s3.presign.ttl`, carrying `response-content-disposition` (the file name, as in
  R-STOR-012) and `response-content-type`, and `Cache-Control: no-store`.
- A virtual repository redirects only when the member that serves the item has the switch on.
- Authorization happens before the URL is issued; download statistics are counted at issuance.
- URLs are signed against `storage.s3.presign.endpoint` when set (a public host name in front
  of an in-cluster object store), otherwise against the configured endpoint. With temporary
  credentials (R-STOR-031) a URL stops working when the signing credentials expire, whichever
  comes first ([presigned URLs](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ShareObjectPreSignedURL.html)).

Configuration:

```yaml
storage:
  s3:
    presign:
      endpoint: ""            # default (proposed): empty = storage.s3.endpoint
      ttl: 60s                # default (proposed)
      min_size: 1MiB          # default (proposed)
```

Acceptance criteria:

- With the switch on, `curl -sI …/files/docker-local/<layer>` answers `302`;
  `curl -L -o layer.bin …` downloads a file whose SHA-256 matches; the URL fails after `ttl`.
- Files below `min_size` are streamed (`200`).

### R-STOR-015 · Download statistics
Priority: **P1** · Covers: ART-029

- A download counts when trove answers `200` with the full content, `206` for a range starting
  at byte 0, or issues a presigned redirect; `HEAD`, `304`, sidecars and requests with
  `?stats=false` do not count.
- Through a virtual repository the count goes to the item of the serving member.
- Each replica aggregates counts in memory and flushes them every `storage.stats.flush_interval`
  with one upsert per item (`downloads = downloads + n`,
  `last_download_at = greatest(…)`, `last_downloader` of the latest download). A replica crash
  loses at most one interval of counts; statistics are not audit data.
- `GET /api/v1/repositories/{repoKey}/stats?path=…` answers
  `{"downloads": 1832, "lastDownloadAt": "2026-10-27T22:01:55Z", "lastDownloader": "ci:acme/web"}`
  *(example)*. Permission: `read`.
- `last_downloader` is set to `NULL` when the principal is erased
  ([identity-and-authentication.md](identity-and-authentication.md)).

Configuration:

```yaml
storage:
  stats:
    flush_interval: 10s    # default (proposed)
```

Acceptance criteria:

- Three `npm install` runs that each download a tarball raise its `downloads` by three after at
  most `flush_interval`.
- `curl -I` does not change the count.

## Item operations

### R-STOR-016 · Item information
Priority: **MVP** · Covers: ART-027, ART-001

`GET /api/v1/repositories/{repoKey}/items?path={path}` returns the metadata of a file or folder
(an empty `path` addresses the repository root).

```json
{
  "repository": "npm-local",
  "path": "@acme/widget/-/widget-1.4.0.tgz",
  "kind": "file",
  "size": 48213,
  "mediaType": "application/gzip",
  "created":  { "at": "2026-10-28T09:14:03.117Z", "by": "ci:acme/widget" },
  "modified": { "at": "2026-10-28T09:14:03.117Z", "by": "ci:acme/widget" },
  "updatedAt": "2026-10-28T10:02:41.009Z",
  "digests": { "sha256": "…", "sha512": "…", "sha1": "…", "md5": "…" },
  "declaredDigests": { "sha512": "…", "sha1": "…" },
  "declaredMismatch": false,
  "contentStatus": "ok",
  "package": { "name": "@acme/widget", "version": "1.4.0", "lead": true },
  "upstreamUrl": null,
  "downloadUrl": "https://trove.example.com/files/npm-local/@acme/widget/-/widget-1.4.0.tgz"
}
```

Folders return `kind: "folder"`, `explicit`, timestamps and the URL of their listing; they do not
embed their children (R-STOR-017). Through a virtual repository the item of the serving member
is returned with an additional `resolvedFrom` repository key. Permission: `read`. Errors: `400`
malformed path; `401`; `403`; `404`.

Acceptance criteria:

- After `dotnet nuget push Acme.Widget.1.4.0.nupkg --source https://trove.example.com/api/nuget/nuget-local/v3/index.json --api-key $TROVE_TOKEN`
  the item of the `.nupkg` shows `package.name = "acme.widget"`, `version = "1.4.0"`,
  `lead = true`.

### R-STOR-017 · Folder listing and directory index
Priority: **P1** · Covers: ART-028, ART-042

- `GET /api/v1/repositories/{repoKey}/children?path=&recursive=false&maxDepth=&folders=true&cursor=&limit=`
  lists the untrashed children of a folder in byte order of their path, with cursor pagination
  (`limit` default 1,000, maximum 10,000). `recursive=true` walks the subtree, optionally bounded
  by `maxDepth`.
- Entry: `{"path": "<relative>", "kind": "file", "size": 48213, "modifiedAt": "…", "sha256": "…", "sha1": "…"}`;
  folders carry no size or digests.
- `GET /files/{repoKey}/{folder}/` answers with a directory index: HTML for `Accept: text/html`
  (every name HTML-escaped, every link written as `./` followed by the percent-encoded name so that
  a name such as `javascript:x` stays a relative path, the same CSP as R-STOR-012 without
  `sandbox`, no scripts), JSON
  (the same entries) otherwise. Under the repository's checksum policy (R-STOR-010) sidecar
  entries appear next to files.
- Virtual repositories list the merged view defined in
  [repositories-and-proxy.md](repositories-and-proxy.md).

Permission: `read` on the folder; entries the caller cannot read are omitted.

Acceptance criteria:

- `curl -H 'Accept: text/html' …/files/generic-local/tools/` returns links for each child; a file
  named `<script>.txt` appears escaped, and a file named `javascript:alert(1)` is linked as
  `./javascript%3Aalert(1)`.
- Listing a folder with 25,000 children with `limit=10000` takes three pages and returns every
  child exactly once.

### R-STOR-018 · Delete items
Priority: **MVP** · Covers: ART-034, STOR-002

- `DELETE /files/{repoKey}/{path}` (or `DELETE /api/v1/repositories/{repoKey}/items?path=…`)
  removes a file, or a folder with its whole subtree, and answers `204`.
- Deletion touches only PostgreSQL: items are moved to the trash can (local repositories with the
  trash can enabled, R-STOR-022) or deleted; blobs stay until garbage collection (R-STOR-024).
- The format plug-in's `OnItemsRemoved` hook runs in the same transaction, so format indexes
  never list removed content.
- Permission: `delete` on every item of the subtree. The check completes before anything is
  removed; if any item is not deletable the answer is `403` and nothing changes.
- A subtree larger than `storage.delete.max_sync_items` is removed by a background job
  (`202 Accepted` with the job URL, job API per [architecture.md](architecture.md)); the
  permission check still completes before the first removal. The job removes in transactions of
  `storage.delete.batch_size` items; a crash resumes with the remainder.
- Remote repositories: a delete removes the cached copy permanently (no trash can); the next
  request refetches it. Virtual repositories answer `405`.
- Deleting a whole repository (operation of
  [repositories-and-proxy.md](repositories-and-proxy.md)) removes its items permanently in the
  background; their blobs are queued for collection.

Configuration:

```yaml
storage:
  delete:
    max_sync_items: 10000   # default (proposed)
    batch_size: 5000        # default (proposed)
```

Acceptance criteria:

- `curl -X DELETE …/files/npm-local/@acme/widget/-/widget-1.4.0.tgz` answers `204`; afterwards
  `npm view @acme/widget versions --registry …` no longer lists 1.4.0.
- Deleting a folder that contains one file the caller may not delete answers `403` and removes
  nothing.

### R-STOR-019 · Copy and move
Priority: **P1** · Covers: ART-032, ART-033, STOR-002

```http
POST /api/v1/content/copy
POST /api/v1/content/move
Content-Type: application/json

{
  "source": { "repository": "generic-staging", "path": "tools/widget/2.3" },
  "target": { "repository": "generic-release", "path": "tools/widget/2.3" },
  "dryRun": false,
  "stopOnError": true,
  "overwrite": false,
  "copyProperties": true
}
```

- Both operations are metadata-only: copy inserts item rows that reference the same blobs (via
  the guard trigger), move updates repository and path. No S3 request is made.
- `target.path` names the exact destination. If a folder already exists there, the source
  content is merged into it; it is not nested inside (Q-STOR-8).
- Sources: copy from local repositories and remote caches; move from local repositories only.
  Targets: a local repository, or a virtual repository with a deployment target, of the same
  format as the source or of the generic format. The target format plug-in may reject items that
  do not fit its layout (reported per item).
- Permissions: copy needs `read` on each source item and `deploy` on each target path; move
  needs `delete` on each source item and `deploy` on each target path; replacing an existing
  target item needs `overwrite` (the replaced item goes to the trash can).
- Properties are copied when `copyProperties` is true (default); download statistics are not.
  Copied items get the caller and the current time as creator.
- Format plug-in hooks run for both repositories in the same transaction.
- `dryRun: true` evaluates permissions, conflicts and format rules and changes nothing.
- Response `200`: `{"files": 12, "folders": 3, "bytes": 81233920, "messages": [{"level": "error", "path": "tools/widget/2.3/a.bin", "code": "exists", "text": "target exists and overwrite is false"}]}`.
  Above `storage.delete.max_sync_items` items the operation runs as a background job (`202`).

Acceptance criteria:

- Copying a 2 GiB file between repositories completes in under one second on the reference test
  setup and issues no S3 request.
- A dry run of a move without `delete` on the source lists one `forbidden` message per item and
  changes nothing.

### R-STOR-020 · Item properties
Priority: **P1** · Covers: ART-021, ART-022, ART-023, ART-024, ART-025

- Properties are string key/value annotations on files, folders and the repository root; a key
  holds a set of values. Keys: 1–255 characters, no control characters and none of `= , ; |`.
  Values: up to 2,400 characters, no control characters except tab. At most
  `storage.properties.max_values_per_item` values per item.
- Keys starting with `trove.` are reserved for system-maintained properties and cannot be
  written by clients.
- Writable in local repositories and remote caches; readable but not writable through virtual
  repositories (`405`).
- API (permission: `read` for reading, `annotate` for writing):

  | Method | Path | Body | Result |
  |---|---|---|---|
  | `GET` | `/api/v1/repositories/{repoKey}/properties?path=&keys=a,b` | — | `200 {"properties": {"stage": ["qa"]}}`; `{}` when none |
  | `PATCH` | `/api/v1/repositories/{repoKey}/properties?path=&recursive=false` | `application/merge-patch+json` ([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)): `{"stage": "qa", "targets": ["linux", "darwin"], "obsolete": null}` | `204`; a string or array replaces the key's values, `null` removes the key, absent keys stay |
  | `DELETE` | `/api/v1/repositories/{repoKey}/properties?path=&keys=a,b&recursive=false` | — | `204` |

- A recursive change applies to the item and every item below it in one transaction and is
  refused with `422` when the subtree exceeds `storage.properties.max_recursive_items`.
- Property changes update `items.updated_at`, emit an audit event and feed retention
  conditions (R-STOR-038).

Configuration:

```yaml
storage:
  properties:
    max_values_per_item: 1000   # default (proposed)
    max_recursive_items: 100000 # default (proposed)
```

Acceptance criteria:

- `curl -X PATCH -H 'Content-Type: application/merge-patch+json' -d '{"stage":"qa"}' "…/properties?path=tools/widget.bin"`
  answers `204`; `GET` returns `{"properties":{"stage":["qa"]}}`.
- Setting a property on a virtual repository path answers `405`.

### R-STOR-021 · Property sets
Priority: **P1** · Covers: ART-026

- A property set is an administrator-defined, named list of property definitions (name, value
  type `any` | `single` | `multi`, optional default, allowed values) assigned to repositories.
  The UI offers the assigned sets when annotating items; the server does not enforce them.
- API (permission: system administrator; assigning to a repository also allowed with `manage`
  on that repository):

  | Method | Path | Notes |
  |---|---|---|
  | `GET`, `POST` | `/api/v1/property-sets` | list, create |
  | `GET`, `PUT`, `DELETE` | `/api/v1/property-sets/{name}` | `DELETE` answers `409` while repositories are assigned |
  | `PUT` | `/api/v1/property-sets/{name}/repositories` | body: list of repository keys |

```sql
CREATE TABLE property_sets (
    name        text PRIMARY KEY CHECK (name ~ '^[A-Za-z0-9._-]{1,64}$'),
    description text NOT NULL DEFAULT ''
);
CREATE TABLE property_set_entries (
    set_name       text NOT NULL REFERENCES property_sets (name) ON DELETE CASCADE,
    property       text NOT NULL,
    value_type     text NOT NULL CHECK (value_type IN ('any', 'single', 'multi')),
    default_value  text,
    allowed_values text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (set_name, property)
);
CREATE TABLE property_set_repositories (
    set_name  text NOT NULL REFERENCES property_sets (name) ON DELETE RESTRICT,
    repo_id   bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    PRIMARY KEY (set_name, repo_id)
);
```

Acceptance criteria:

- Deleting a set assigned to `generic-local` answers `409`; after unassigning, `204`.

## Trash can

### R-STOR-022 · Trash can
Priority: **P1** · Covers: ART-036, RET-038

- Runtime settings (stored in PostgreSQL, changed through
  `PUT /api/v1/settings/storage`, cached per replica and invalidated through `LISTEN/NOTIFY`,
  INV-12):

  ```json
  { "trash": { "enabled": true, "retention": "336h" } }
  ```

  `enabled: true` and `retention: 336h` (14 days) are the defaults (proposed).
- With the trash can enabled, deletions of items in **local** repositories set `trashed_at`,
  `trashed_by` and a `trash_operation` id on every affected item (and version row) instead of
  removing them. This applies to user deletes (R-STOR-018), overwrites of user-deployed files
  (R-STOR-005), retention policies unless they bypass it (R-STOR-037), and repository caps
  (R-STOR-044). Remote-cache deletes, generated items and repository deletion bypass it.
- Trashed items are invisible to every protocol endpoint, `/files`, listings and package
  metadata; they remain blob references, count toward physical storage, and do not count
  toward the repository quota (R-STOR-029).
- Expiry is computed from the current setting: an item expires when
  `trashed_at < now() - retention`. Shortening the retention takes effect on the next purge.
  Disabling the trash can stops new entries; existing entries expire normally.
- The purge runs with every garbage-collection run (R-STOR-025): it deletes expired trashed
  items in batches, which queues their blobs for collection.
- Without the trash can, every deletion, including policy deletions, is permanent.
- Visibility: a principal sees trashed items of an original path where it holds `deploy` or
  `manage` (evaluated as if the item were live).

Acceptance criteria:

- With defaults, `curl -X DELETE …/files/generic-local/a.bin` makes the file disappear from
  `/files` and appear in `GET /api/v1/trash-can/items?repository=generic-local`.
- After setting `retention` to `0s` and running garbage collection, the entry is gone and its
  blob is queued.

### R-STOR-023 · Restore and permanent removal from the trash can
Priority: **P1** · Covers: ART-037, ART-038

| Method | Path | Body / parameters | Result | Permission |
|---|---|---|---|---|
| `GET` | `/api/v1/trash-can/items` | `repository`, `path` (prefix), `cursor`, `limit` | entries `{id, repository, path, kind, size, trashedAt, trashedBy, operationId, expiresAt}` | visibility rule of R-STOR-022 |
| `POST` | `/api/v1/trash-can/restorations` | `{"ids": [..]}` or `{"operationId": "…"}` or `{"source": {"repository": "…", "path": "…"}}`; optional `"target": {"repository": "…", "path": "…"}`; `"overwrite": false` | `200 {"restored": n, "skipped": n, "messages": [..]}`; `202` with job URL above `storage.delete.max_sync_items` | `deploy` and `delete` on the original path; `deploy` (and `overwrite` if replacing) on a different target |
| `DELETE` | `/api/v1/trash-can/items` | `ids` or `repository` + `path` | `204` | `delete` or `manage` on the original path |
| `POST` | `/api/v1/trash-can/purge` | — | `202` | system administrator |

- Restore clears the trash fields, re-links the version row and runs the format plug-in's
  `OnItemsRestored` hook. When an untrashed item already occupies the target path, that entry is
  skipped with a message unless `overwrite` is true (the occupant then goes to the trash can).
  When the version was re-published meanwhile, the version's entries are skipped.
- Restoring a trash operation restores exactly the items one delete call removed, so a folder
  comes back as it was.
- Denied permissions answer `403`.

Acceptance criteria:

- Deleting an npm version through the API and restoring its `operationId` makes
  `npm view @acme/widget versions` list the version again and `npm install @acme/widget@1.4.0`
  succeed.
- A restore into a path that is occupied, without `overwrite`, reports one `skipped` message and
  leaves both items unchanged.

## Garbage collection and integrity

### R-STOR-024 · Race-free garbage collection
Priority: **MVP** · Covers: STOR-036

Garbage collection deletes blobs that no reference uses any more. It MUST never delete a blob
that a committed reference uses, and MUST never let an upload commit a reference to a blob whose
object it is deleting.

**Candidates.**

- *Incremental*: digests in `blob_gc_queue` whose `enqueued_at` is older than
  `storage.gc.grace_period` (the release trigger enqueues a digest whenever a reference to it
  disappears).
- *Full sweep*: every `live` blob older than the grace period, found in keyset batches over
  `blobs.sha256` with an anti-join against all referencing tables, skipping digests whose queue
  entry is younger than the grace period. The set-based query only proposes candidates; each one
  is confirmed by the per-blob protocol.
- *Interrupted deletions*: rows in state `deleting`.

**Per-blob protocol.**

```sql
BEGIN ISOLATION LEVEL READ COMMITTED;
-- (1) lock: waits for every transaction that holds FOR SHARE on the row (an upload or copy
--     that is adding a reference) and blocks new ones until this transaction ends
SELECT state FROM blobs WHERE sha256 = $1 FOR UPDATE;
-- (2) separate statement, so it runs on a snapshot taken after the lock was granted;
--     the table list comes from the foreign keys that point at blobs, read in this transaction
SELECT EXISTS (SELECT 1 FROM items WHERE blob_sha256 = $1)
    OR EXISTS (SELECT 1 FROM retention_runs WHERE blob_sha256 = $1)
    OR …;
-- (3) only when (1) returned state = 'live' and (2) returned false
UPDATE blobs SET state = 'deleting', state_changed_at = now() WHERE sha256 = $1;
DELETE FROM blob_gc_queue WHERE sha256 = $1;     -- also when (2) found a reference
COMMIT;
-- (4) outside any transaction; 204 and 404 both count as success
S3 DeleteObject <prefix>/sha256/…
-- (5)
UPDATE blobs SET object_deleted_at = now() WHERE sha256 = $1 AND state = 'deleting';
-- (6) later, once object_deleted_at < now() - storage.gc.deleteSettle
DELETE FROM blobs WHERE sha256 = $1 AND state = 'deleting';
```

**Why it is safe.**

1. *A reference is only ever committed to a `live` row, under a `FOR SHARE` lock held until
   commit.* The guard trigger enforces this in the database for every referencing table.
2. *`live` → `deleting` happens only under `FOR UPDATE` and after a reference check whose
   snapshot was taken after the lock was granted.* `FOR UPDATE` conflicts with `FOR SHARE`
   ([PostgreSQL row-level locks](https://www.postgresql.org/docs/current/explicit-locking.html#LOCKING-ROWS)).
   A transaction that added a reference either committed before the lock was granted, so the
   check in step (2), which under READ COMMITTED takes a fresh snapshot per statement
   ([isolation levels](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-READ-COMMITTED)),
   sees it and the blob is kept; or it requests its `FOR SHARE` lock later, waits for the
   collector to commit, then re-reads the row, finds `deleting`, and the guard fails. The commit
   protocol (R-STOR-005) then waits for the row to disappear and writes the object again. The
   check MUST therefore be a separate statement after the lock, and the transaction MUST NOT
   run at REPEATABLE READ or SERIALIZABLE (their snapshot would predate the lock).
3. *`deleting` is terminal; the object is deleted only in that state; the row disappears only
   after the delete succeeded and `delete_settle` has passed.* A new row for the same digest can
   be inserted only after that (primary key), and its uploader writes the object only after it
   inserted or found the new row, that is, after the delete. Under the consistency assumptions of
   R-STOR-035 the later write survives. `delete_settle` covers a delete request still in flight in
   a proxy or SDK retry when the collector already saw a success.
4. *A row becomes `live` only after its object write succeeded* (R-STOR-005, R-STOR-006).

From 1–4: every committed reference points at a `live` row whose object exists. Damage from
outside trove (objects deleted or altered directly in the bucket) is detected by the scrub
(R-STOR-027), not prevented.

**Recovery.** Every step is idempotent. A collector that crashes after (3) leaves a `deleting`
row; the next run repeats (4)–(6). Rows in `deleting` are processed on every run before new
candidates.

**Grace period.** The protocol is correct without it; the grace period exists for three other
reasons: (a) content deleted and re-published shortly after (common in CI) is still stored, so
the re-upload is a deduplication hit; (b) a PostgreSQL point-in-time restore to any moment within
the last `grace_period` finds every blob it references still in the bucket, because a blob is
deleted no earlier than `grace_period` after its last reference disappeared (longer restore
windows need bucket versioning with noncurrent-version expiry at least that long; backup is
specified in [ha-and-helm.md](ha-and-helm.md)); (c) defense in depth.

**Deletion batching.** The collector MAY claim up to `storage.gc.batch_size` blobs, one
transaction each or one transaction for the batch with the locks taken in digest order, and
delete their objects with `DeleteObjects` (up to 1,000 keys per request) when
`storage.gc.batch_delete` is true.

**Relation to other jobs.** Retention runs, deletes and copies may run concurrently with
collection; the protocol needs no mutual exclusion beyond the row locks. Physical-usage deltas
are recorded when rows are removed.

Configuration:

```yaml
storage:
  gc:
    grace_period: 24h        # default (proposed)
    delete_settle: 30s       # default (proposed)
    batch_size: 1000         # default (proposed)
    batch_delete: true       # default (proposed); false for stores without DeleteObjects
    delete_concurrency: 8    # default (proposed)
```

Metrics: `trove_storage_gc_blobs_deleted_total`, `trove_storage_gc_bytes_reclaimed_total`,
`trove_storage_gc_candidates_kept_total`, `trove_storage_gc_deleting_rows`.

Acceptance criteria:

- The race test of R-STOR-005 passes, including a variant in which the collector process is
  killed at random points.
- A deleted item's blob still exists 23 hours later and is gone after the first collection run
  past 24 hours (with defaults and the trash can disabled).
- A test that inserts a reference row with `blob_sha256` of a `deleting` blob through plain SQL
  fails with SQLSTATE `AS001`.
- A test migration adds a new referencing table while an old binary runs collection; a blob
  referenced only from the new table is kept.

### R-STOR-025 · Garbage-collection schedule, full sweep and manual run
Priority: **MVP** · Covers: STOR-037

- Collection runs as singleton jobs under an advisory-lock lease (INV-12): the incremental run on
  `storage.gc.schedule`, the full sweep on `storage.gc.full_schedule`. Each run first purges
  expired trash entries (R-STOR-022), then finishes `deleting` rows, then processes candidates.
- Schedules are 5-field cron expressions (minute, hour, day of month, month, day of week),
  evaluated in UTC, with the descriptors `@hourly`, `@daily`, `@weekly` (Q-STOR-4).
- API (permission: system administrator):

  | Method | Path | Body | Result |
  |---|---|---|---|
  | `POST` | `/api/v1/maintenance/gc-runs` | `{"mode": "incremental" \| "full", "dryRun": false}` | `202 {"id": "…", "status": "queued"}`; `409` when a run is active |
  | `GET` | `/api/v1/maintenance/gc-runs` | `cursor`, `limit` | runs with `mode`, `status`, `startedAt`, `finishedAt`, `trashPurged`, `candidates`, `kept`, `deleted`, `bytesReclaimed`, `errors`, `nextScheduledAt` |
  | `GET` | `/api/v1/maintenance/gc-runs/{id}` | — | one run |
  | `POST` | `/api/v1/maintenance/gc-runs/{id}/stop` | — | `202`; the run stops after the current batch |

- A dry run reports what would be deleted and changes nothing.

```sql
CREATE TABLE maintenance_runs (
    id            uuid PRIMARY KEY,
    kind          text NOT NULL CHECK (kind IN ('gc', 'scrub', 'orphan-sweep', 'summary')),
    mode          text,
    dry_run       boolean NOT NULL DEFAULT false,
    status        text NOT NULL CHECK (status IN ('queued', 'running', 'stopping', 'stopped', 'completed', 'failed')),
    triggered_by  uuid,                         -- NULL for scheduled runs
    started_at    timestamptz,
    finished_at   timestamptz,
    cursor        jsonb,                        -- resume position (orphan sweep, scrub, full sweep)
    counters      jsonb NOT NULL DEFAULT '{}',
    error         text
);
```

Configuration:

```yaml
storage:
  gc:
    schedule: "*/30 * * * *"    # default (proposed): incremental every 30 minutes
    full_schedule: "0 2 * * 0"  # default (proposed): full sweep Sundays 02:00 UTC
```

Acceptance criteria:

- `curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -d '{"mode":"full"}' …/api/v1/maintenance/gc-runs`
  answers `202`; a second call while it runs answers `409`.
- With two replicas, scheduled runs execute on exactly one replica at a time (lease visible in
  `pg_locks`).

### R-STOR-026 · Orphan sweep
Priority: **P1** · Covers: STOR-038, OPS-067

The reverse check: walk the bucket and remove objects no blob row knows.

- The sweep lists `<prefix>/sha256/<h0h1>/` prefix by prefix (`ListObjectsV2`, 256 top-level
  prefixes), stores its position in `maintenance_runs.cursor` and can be stopped and resumed.
- For each object without a blob row and with `LastModified` older than
  `storage.orphans.min_age`, the sweep MUST claim the digest by inserting a `deleting` row
  (`INSERT … ON CONFLICT DO NOTHING`) before deleting the object, then follow steps (4)–(6) of
  R-STOR-024. An uploader that wrote the same object concurrently finds the `deleting` row and
  writes again after it is gone (R-STOR-005); the commit deadline keeps freshly written objects
  out of reach of `min_age`.
- Keys below `<prefix>/sha256/` that do not match the layout, and any other foreign key below
  `<prefix>/`, are reported and never deleted.
- The sweep also aborts multipart uploads below `<prefix>/` initiated longer than
  `storage.orphans.multipart_max_age` ago, and deletes staging objects of expired upload
  sessions (R-STOR-011).
- API (system administrator): `POST /api/v1/maintenance/orphan-sweeps` with
  `{"dryRun": true, "resume": true, "minAge": "24h"}` → `202`; `GET …/orphan-sweeps/{id}` →
  status with `prefixesDone`, `prefixesTotal`, `objectsSeen`, `orphansFound`, `orphansDeleted`,
  `bytesReclaimed`, `multipartAborted`, `foreignKeys`; `POST …/orphan-sweeps/{id}/stop` → `202`.
  Timestamps are RFC 3339 with offset.

Configuration:

```yaml
storage:
  orphans:
    schedule: "0 5 * * 6"     # default (proposed): Saturdays 05:00 UTC
    min_age: 24h              # default (proposed); must be > 2 × storage.upload.commit_deadline
    multipart_max_age: 24h    # default (proposed)
```

Acceptance criteria:

- `aws s3 cp junk.bin s3://trove-blobs/trove/sha256/00/00/<64 hex>` with a back-dated test
  clock is removed by a sweep, reported by a dry run before that, and an object with a
  non-hex name under `sha256/` is only reported.
- A sweep stopped at prefix `7f` and resumed continues at `7f` without re-listing earlier
  prefixes.

### R-STOR-027 · Integrity scrub
Priority: **P1** · Covers: trove-only

- **Existence check**: every `live` blob MUST be checked with `HeadObject` at least once per
  `storage.scrub.check_interval` (oldest `checked_at` first). `404` sets `content_status = 'missing'`;
  a size different from `blobs.size` sets `corrupt`. Errors other than `404` (timeouts, `5xx`,
  `403`) MUST NOT change the status; they are counted and retried.
- **Deep check**: every `live` blob SHOULD be downloaded and re-hashed at least once per
  `storage.scrub.deep_interval`, throttled to `storage.scrub.deep_bytes_per_second` per replica; a
  SHA-256 mismatch sets `corrupt`.
- Damaged blobs raise `trove_storage_blobs_damaged{status}`, an audit event per blob, and
  appear in `GET /api/v1/maintenance/damaged-blobs` with their referencing items.
- **Repair**: a later upload of identical content rewrites the object and resets the status
  (R-STOR-005); a remote repository treats an item with damaged content as a cache miss and
  refetches it; `POST /api/v1/maintenance/damaged-blobs/{sha256}/recheck` re-runs both checks.
- Runs: `POST /api/v1/maintenance/scrub-runs` `{"deep": false}`, status and stop as for
  collection. Permission: system administrator.

Configuration:

```yaml
storage:
  scrub:
    schedule: "0 4 * * *"          # default (proposed)
    check_interval: 168h           # default (proposed): every blob HEAD-checked weekly
    deep_interval: 2160h           # default (proposed): every blob re-hashed every 90 days
    deep_bytes_per_second: 50MiB   # default (proposed)
```

Acceptance criteria:

- `aws s3 rm` of one blob object followed by a scrub run marks it `missing`; the damaged-blobs
  API lists the items referencing it; re-uploading the same file with `curl -T` resets it to
  `ok`.
- An S3 outage during a scrub run leaves every status unchanged.

## Capacity

### R-STOR-028 · Storage summary
Priority: **P1** · Covers: STOR-039

- A singleton job computes the summary on `storage.summary.schedule` and stores it as a
  snapshot; reads never compute.
- `GET /api/v1/storage/summary` (system administrator, or a principal with the read-only
  monitoring role of [authorization.md](authorization.md)):

  ```json
  {
    "computedAt": "2026-10-28T09:17:00Z",
    "physical": { "blobs": 412093, "bytes": 1893341184512 },
    "logical":  { "files": 1203381, "folders": 98112, "bytes": 4120938391040 },
    "dedupRatio": 0.46,
    "trash": { "items": 3210, "bytes": 9123012345 },
    "pendingCollection": { "blobs": 812, "bytes": 1029381233 },
    "damaged": { "missing": 0, "corrupt": 0 },
    "quota": { "capacityBytes": 5497558138880, "usedPercent": 34.4, "warnPercent": 80, "limitPercent": 95 },
    "repositories": [
      { "key": "npm-local", "class": "local", "format": "npm", "files": 52011, "folders": 8812, "bytes": 81233920123, "share": 1.97 }
    ]
  }
  ```

  Sizes are integers in bytes; `share` is the percentage of the total logical bytes;
  `dedupRatio` is physical over logical bytes.
- `POST /api/v1/storage/summary/refresh` (system administrator) answers `202`; the next snapshot
  replaces the current one.
- Spool usage is per replica and exported only as metrics (R-STOR-004).

Configuration:

```yaml
storage:
  summary:
    schedule: "17 * * * *"    # default (proposed): hourly
```

Acceptance criteria:

- After uploading the same 10 MiB file to two repositories and refreshing, `logical.bytes` grows by
  20 MiB and `physical.bytes` by 10 MiB.

### R-STOR-029 · Quotas and capacity thresholds
Priority: **P1** · Covers: STOR-040, OPS-066

An object store does not report free space through the S3 API, so capacity is configured, not
measured.

- System settings (runtime, `PUT /api/v1/settings/storage`):

  ```json
  { "quota": { "enabled": false, "capacityBytes": null, "warnPercent": 80, "limitPercent": 95 } }
  ```

  Defaults (proposed) as shown. Used capacity is physical bytes (all `live` blobs, including
  trash and content awaiting collection).
- Per repository: `config.storage.quota` (bytes of untrashed logical content; default none).
- Above `warn_percent`: warning log, metric `trove_storage_quota_used_ratio`, an event for
  webhooks ([operations-and-observability.md](operations-and-observability.md)) and a banner in
  the UI. At or above `limitPercent`, or when a repository quota would be exceeded: every upload
  is rejected with `507 Insufficient Storage` and problem type `quota-exceeded`; deploy by
  checksum and copy into a repository also count against its repository quota.
- Enforcement uses the folded counters plus the unfolded deltas, so it can lag by at most one fold
  interval (R-STOR-003); concurrent uploads may exceed a quota by the content in flight.
- Deletes, moves within a repository, property changes and downloads are never blocked by a
  quota.
- The spool volume is the only replica-local disk on the upload path; its thresholds are checked
  when an upload is admitted (R-STOR-004).

Acceptance criteria:

- With `capacityBytes` set to the current physical size and `limitPercent: 95`,
  `npm publish` fails with a `507` error; deleting content and running collection lets it succeed
  again.
- A repository quota of 100 MiB rejects the upload that would cross it and accepts uploads to
  other repositories.

## Object-store configuration

### R-STOR-030 · Object-store connection and bucket settings
Priority: **MVP** · Covers: STOR-006, STOR-020, STOR-029

- trove talks to one bucket through the S3 API with Signature Version 4 (AWS SDK for Go v2).
  Any store that implements the operations below and meets R-STOR-035 is supported (AWS S3,
  MinIO, Ceph RGW and similar).
- Operations used: `PutObject`, `GetObject` (with `Range`), `HeadObject`, `DeleteObject`,
  `DeleteObjects` (optional), `ListObjectsV2`, `CreateMultipartUpload`, `UploadPart`,
  `UploadPartCopy`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `ListMultipartUploads`,
  and presigning for R-STOR-014. trove never creates or configures the bucket.
- Configuration is read at startup; a change needs a rolling restart (Helm, INV-13). All
  replicas MUST use the same bucket and prefix; the prefix is fixed for the life of an
  installation (changing it makes all content unreachable).
- Validation at startup: bucket required; prefix without leading or trailing `/` and without
  `//`, at most 256 characters of `[A-Za-z0-9!_.*'()/-]`; `endpoint`, when set, an absolute
  `https://` URL (`http://` only with `allowInsecureEndpoint: true`).
- S3-compatible stores: `forcePathStyle: true` for stores without virtual-hosted addressing;
  explicit `region` (any value the store accepts); `checksums.requestCalculation: when_required`
  and `checksums.responseValidation: when_required` for stores that reject the SDK's default
  payload checksums (SDK options `RequestChecksumCalculation` and `ResponseChecksumValidation`,
  [aws package](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws)); `ca_file` for private CAs.
- Every key can be overridden by environment variables per INV-15, for example
  `TROVE_STORAGE_S3_BUCKET`.

Configuration:

```yaml
storage:
  s3:
    endpoint: ""                 # default (proposed): empty = AWS endpoint resolution for region
    region: eu-central-1         # example
    bucket: trove-blobs        # example; required
    prefix: trove              # default (proposed)
    force_path_style: false      # default (proposed)
    allow_insecure_endpoint: false  # default (proposed)
    ca_file: ""                  # default (proposed): system trust store
    storage_class: ""            # default (proposed): bucket default
    checksums:
      request_calculation: when_supported  # default (proposed): when_supported | when_required
      response_validation: when_supported  # default (proposed)
    startup_check: true          # default (proposed); R-STOR-035
```

Acceptance criteria:

- The CI matrix runs the storage integration suite against AWS S3 (nightly), MinIO and Ceph RGW
  (per change) with the documented settings for each.
- A prefix of `/` or `a//b` stops startup with a configuration error naming the key.

### R-STOR-031 · Object-store credentials
Priority: **MVP** · Covers: STOR-021

| `credentials.source` | Behavior |
|---|---|
| `default` (default, proposed) | The AWS SDK default chain: environment variables, shared config files, web identity token (used by [IAM roles for service accounts](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html)), container credentials (used by [EKS Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html)), instance metadata. No secret in trove's configuration. |
| `webIdentity` | Exchange the projected service-account token in `web_identity_token_file` for temporary credentials of `role_arn` at `sts_endpoint` (AssumeRoleWithWebIdentity). For S3-compatible stores with an STS that trusts the Kubernetes service-account issuer, and for AWS outside EKS. |
| `static` | Access key ID and secret read from files (`access_key_id_file`, `secret_access_key_file`, optional `session_token_file`), mounted from a Kubernetes Secret. Files are re-read on change so rotated keys apply without restart. Inline values are rejected (INV-15). |

- An optional `role_arn` with `default` or `static` assumes that role on top of the base
  credentials.
- Temporary credentials are refreshed by the SDK before expiry; a refresh failure counts as an
  object-store failure (R-STOR-033).
- Minimum permissions on the bucket and prefix: `s3:GetObject`, `s3:PutObject`,
  `s3:DeleteObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts` on
  `arn:aws:s3:::<bucket>/<prefix>/*`; `s3:ListBucket` (which may be conditioned on `s3:prefix`
  = `<prefix>/*`) and `s3:ListBucketMultipartUploads` on the bucket. No bucket-level write, no
  `CreateBucket`, no ACL permissions. With SSE-KMS add `kms:GenerateDataKey` and `kms:Decrypt` on the key
  (R-STOR-032).

Configuration:

```yaml
storage:
  s3:
    credentials:
      source: default                      # default (proposed): default | webIdentity | static
      role_arn: ""                         # example: arn:aws:iam::123456789012:role/trove
      web_identity_token_file: ""          # example: /var/run/secrets/tokens/s3
      sts_endpoint: ""                     # example: https://minio.example.com
      access_key_id_file: ""               # example: /var/run/secrets/trove-s3/access-key-id
      secret_access_key_file: ""           # example: /var/run/secrets/trove-s3/secret-access-key
      session_token_file: ""
```

Acceptance criteria:

- On EKS with a service account annotated for IRSA and `source: default`, the startup check
  passes with no S3 secret in the cluster.
- With `static`, replacing the Secret's files makes subsequent S3 requests use the new key without
  a pod restart.
- A policy limited to the permissions above passes the conformance check (R-STOR-035).

### R-STOR-032 · Server-side encryption
Priority: **P1** · Covers: STOR-022

- `sse.mode`: `none` (rely on the bucket's default encryption), `s3` (SSE-S3, AES-256) or `kms`
  (SSE-KMS with `kms_key_id`, and S3 Bucket Keys when `bucket_key` is true)
  ([SSE-KMS](https://docs.aws.amazon.com/AmazonS3/latest/userguide/UsingKMSEncryption.html)).
  The mode is sent on every `PutObject` and `CreateMultipartUpload`.
- With `kms`, the credentials need `kms:GenerateDataKey` (writes, multipart) and `kms:Decrypt`
  (reads, multipart completion, presigned downloads).
- SSE with customer-provided keys and client-side encryption are not supported: the first would
  make trove hold the data key and break presigned redirects, the second cannot coexist with
  server-side payload checksums and ranged reads in the same way (Deferred).
- Changing the mode affects new objects only; existing objects keep their encryption.

Configuration:

```yaml
storage:
  s3:
    sse:
      mode: none            # default (proposed): none | s3 | kms
      kms_key_id: ""        # example: arn:aws:kms:eu-central-1:123456789012:key/0f1e…
      bucket_key: true      # default (proposed)
```

Acceptance criteria:

- With `mode: kms`, `aws s3api head-object` of a new blob shows `ServerSideEncryption: aws:kms`
  and the configured key; downloads and multipart uploads work with the minimal KMS permissions.

### R-STOR-033 · Connection pool, timeouts, retries and circuit breaker
Priority: **MVP** · Covers: STOR-024, STOR-017

- The S3 client uses one HTTP connection pool per replica with `storage.s3.http.max_connections`, and
  explicit dial, TLS-handshake, response-header and idle timeouts. Body transfers have no fixed
  total timeout; they fail when no byte moves for `storage.s3.http.idle_transfer_timeout`.
- Retries: the SDK's standard retryer with `retry.maxAttempts` and exponential backoff capped at
  `retry.maxBackoff`, for throttling, `5xx` and network errors on idempotent operations (all
  operations trove uses are idempotent on content-addressed keys). Streaming `GetObject` bodies
  are not retried after the first byte reached the client.
- Circuit breaker per replica: after `circuitBreaker.failureThreshold` consecutive failures
  (network errors, timeouts, `5xx`, credential errors; not `404` or other `4xx`) the circuit opens
  for `circuitBreaker.openDuration`; blob operations then fail fast (R-STOR-036); one probe
  request closes it again.
- Metrics: `trove_storage_s3_requests_total{op,outcome}`,
  `trove_storage_s3_request_duration_seconds{op}`, `trove_storage_s3_bytes_total{direction}`,
  `trove_storage_s3_circuit_open`.

Configuration:

```yaml
storage:
  s3:
    http:
      max_connections: 256         # default (proposed)
      dial_timeout: 5s             # default (proposed)
      tls_handshake_timeout: 5s    # default (proposed)
      response_header_timeout: 30s # default (proposed)
      idle_conn_timeout: 90s       # default (proposed)
      idle_transfer_timeout: 60s   # default (proposed)
    retry:
      max_attempts: 5              # default (proposed)
      max_backoff: 20s             # default (proposed)
    circuit_breaker:
      failure_threshold: 5         # default (proposed)
      open_duration: 10s           # default (proposed)
```

Acceptance criteria:

- Injecting two `503 SlowDown` answers through a fault proxy before a `PutObject` succeeds makes
  the upload succeed; six consecutive failures make the next download answer `503` within
  milliseconds and `trove_storage_s3_circuit_open` report 1.

### R-STOR-034 · Outbound proxy for object-store traffic
Priority: **P1** · Covers: STOR-025

- `storage.s3.http.proxy_url` sets an HTTP(S) proxy for S3 and STS traffic only; credentials, when needed,
  are read from `storage.s3.http.proxy_credentials_file` (`user:password`), never inline. `storage.s3.http.no_proxy` is a
  comma-separated list of host names, domain suffixes (`.example.com`) and CIDR ranges.
- When `proxy_url` is empty, the standard `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` environment
  variables apply.
- This proxy is independent of the outbound proxies of remote repositories
  ([repositories-and-proxy.md](repositories-and-proxy.md)).

Configuration:

```yaml
storage:
  s3:
    http:
      proxy_url: ""                  # default (proposed): empty = environment
      proxy_credentials_file: ""     # example: /var/run/secrets/trove-proxy/credentials
      no_proxy: ""                   # example: .svc.cluster.local,10.0.0.0/8
```

Acceptance criteria:

- With `proxy_url` pointing at a test proxy, all S3 requests appear in the proxy log and none
  bypass it; hosts in `no_proxy` are reached directly.

### R-STOR-035 · Object-store consistency assumptions and conformance check
Priority: **MVP** · Covers: STOR-029, STOR-006

The correctness of R-STOR-005, R-STOR-024 and R-STOR-026 rests on these assumptions about the
object store. Operators MUST use a store that satisfies them.

| ID | Assumption |
|---|---|
| A1 | Strong read-after-write consistency for new objects, overwrites and deletes: a read or list started after a successful write or delete response observes it. AWS documents this for S3 ([consistency model](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html#ConsistencyModel)). |
| A2 | Single-key atomicity: a read returns the old or the new object, never a partial one; a multipart object becomes visible only on completion. |
| A3 | A request whose response has been received is not executed again later. `delete_settle` (R-STOR-024) absorbs in-flight retries up to its length. |
| A4 | One bucket in one consistency domain. Active-active multi-site replication that lets another site write or delete keys under the prefix is not supported; one-way replication to a passive disaster-recovery bucket is. |
| A5 | Object `LastModified` comes from the store's clock, which differs from replica clocks by at most a few minutes; `storage.orphans.min_age` and `commit_deadline` keep a margin of hours. |
| A6 | Concurrent writes to one key carry identical bytes (content addressing), so last-writer-wins is harmless. |

- AWS S3 satisfies A1–A2 by documentation. MinIO and Ceph RGW (within one zone) are expected to;
  the conformance check is the operator's evidence.
- **Startup check** (`startupCheck: true`): write, read back, compare and delete
  `<prefix>/_probe/<replica-id>`, then list `<prefix>/sha256/` with one key. Configuration and
  permission errors (`AccessDenied`, `NoSuchBucket`, invalid region) stop the process with a
  clear message; transient errors are retried with backoff while the replica stays not ready, and
  the process exits after `startup_timeout`.
- **Conformance check**: `trove admin storage check` runs the startup check plus multipart
  upload and abort, ranged read, `DeleteObjects`, presigning, read-after-delete and
  list-after-write, and reports each result. It changes nothing outside `_probe/`.

Configuration:

```yaml
storage:
  s3:
    startup_check: true      # default (proposed)
    startup_timeout: 5m      # default (proposed)
```

Acceptance criteria:

- `trove admin storage check` passes against MinIO and AWS S3 in CI; against a bucket without
  `s3:DeleteObject` it reports exactly that failure.
- A replica started with a wrong bucket name exits with a non-zero status and a log line naming
  the bucket.

### R-STOR-036 · Behavior during an object-store outage
Priority: **MVP** · Covers: trove-only

| Operation | While S3 is unreachable or the circuit is open |
|---|---|
| Upload of new content (any protocol) | `503` with `Retry-After`; nothing committed; spool file removed. No local queue: a `201` always means durable in S3. |
| Upload of content already stored (deduplication hit), deploy by checksum | Succeeds (PostgreSQL only). |
| Copy, move, delete, properties, trash operations | Succeed (PostgreSQL only). |
| `HEAD`, `304` revalidation, item information, listings, UI, API | Succeed. |
| Download of stored content | `503` with `Retry-After`; presigned redirects are not issued. |
| Remote-repository miss | Behavior defined in [repositories-and-proxy.md](repositories-and-proxy.md) (it MAY pass the upstream body through without caching). |
| Garbage collection, orphan sweep, scrub | Back off; claimed `deleting` rows are retried later; no status becomes `missing` because of errors. |
| Retention runs | Continue; their deletions touch only PostgreSQL. |
| Readiness | Per INV-12, with hysteresis: a replica reports not ready only after S3 checks have failed for `storage.s3.readiness.fail_after` and ready again after `recover_after` of success (see Q-STOR-1). |

Configuration:

```yaml
storage:
  s3:
    readiness:
      fail_after: 60s        # default (proposed)
      recover_after: 10s     # default (proposed)
```

Acceptance criteria:

- Stopping MinIO in the integration environment: `npm publish` of a new version fails with a
  `503`; `npm view` and `curl -I` of existing tarballs succeed; deleting an item succeeds;
  restarting MinIO lets the publish succeed without manual steps; no blob is marked `missing`.

## Retention and cleanup

Retention policies run inside trove's job framework and delete through the normal delete path,
so format indexes, the trash can, audit events and webhooks behave exactly as for a manual delete.
Projects do not exist in the MVP; all policies are global and managed by system administrators
(project-scoped policies: Deferred).

### R-STOR-037 · Retention policy model and lifecycle
Priority: **P1** · Covers: RET-001, RET-013, RET-019

- Kinds: `package-cleanup` (R-STOR-038) and `build-cleanup` (R-STOR-043).
- Common fields: `key` (`^[A-Za-z0-9_-]{3,64}$`, unique), `description` (≤ 256 characters),
  `schedule` (cron, R-STOR-040; empty = manual only), `maxRunDurationMinutes` (1–300),
  `bypassTrash` (default `false`), `enabled`.
- A policy is created disabled; a create request with `"enabled": true` is rejected with `422`.
  Enabling and disabling are separate calls. Disabled policies can be dry-run, edited and
  deleted; only enabled policies run on schedule or manually.
- A policy with an active run cannot be updated or deleted (`409`). Deleting a policy keeps its
  run history (runs keep a snapshot of the configuration).
- API (permission: system administrator):

  | Method | Path | Result |
  |---|---|---|
  | `GET` | `/api/v1/retention/policies?kind=` | list with last-run summary and `nextRunAt` |
  | `POST` | `/api/v1/retention/policies` | `201`; `409` key exists; `422` invalid |
  | `GET`, `PUT`, `DELETE` | `/api/v1/retention/policies/{key}` | `PUT` replaces the configuration; the key is immutable |
  | `POST` | `/api/v1/retention/policies/{key}/enable`, `…/disable` | `204` |

```sql
CREATE TABLE retention_policies (
    key                  text PRIMARY KEY CHECK (key ~ '^[A-Za-z0-9_-]{3,64}$'),
    kind                 text NOT NULL CHECK (kind IN ('package-cleanup', 'build-cleanup')),
    description          text NOT NULL DEFAULT '' CHECK (char_length(description) <= 256),
    enabled              boolean NOT NULL DEFAULT false,
    schedule             text,
    max_run_minutes      integer CHECK (max_run_minutes BETWEEN 1 AND 300),
    bypass_trash         boolean NOT NULL DEFAULT false,
    spec                 jsonb NOT NULL,          -- scope and conditions, validated against a JSON Schema
    revision             integer NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    updated_by           uuid
);
```

Acceptance criteria:

- `POST` with `"enabled": true` answers `422`; after `POST …/enable`, the policy runs on its
  schedule.
- `PUT` during a run answers `409`.

### R-STOR-038 · Package cleanup policy: scope and conditions
Priority: **P1** · Covers: RET-003, RET-004, RET-005, RET-006, RET-007, RET-008, RET-022

```json
{
  "key": "npm-ci-prereleases",
  "kind": "package-cleanup",
  "description": "Remove CI prereleases nobody pulled for 30 days",
  "schedule": "30 2 * * 6",
  "maxRunDurationMinutes": 120,
  "bypassTrash": false,
  "spec": {
    "formats": ["npm"],
    "repositories": { "include": ["npm-ci-*"], "exclude": ["npm-ci-pinned"] },
    "paths": { "include": [], "exclude": [] },
    "packages": { "include": ["@acme/*"], "exclude": ["@acme/legacy-sdk"] },
    "versions": { "include": ["*-ci.*"], "exclude": [] },
    "conditions": {
      "olderThanDays": 90,
      "unusedForDays": 30,
      "properties": { "include": { "lifecycle": ["deprecated"] }, "exclude": { "retain": ["true"] } },
      "keepNewest": 3
    }
  }
}
```

*(example)*

- **Scope.** `formats` (at least one) and `repositories.include` (at least one; `*` for all)
  select **local** repositories only; remote and virtual repositories are never processed
  (remote caches: R-STOR-045). `paths` narrow by the lead item's path prefix. `packages` and
  `versions` match normalized names and versions.
- **Patterns.** Exact values or globs where `*` matches any sequence of characters (Q-STOR-9).
- **Conditions.** At least one of `olderThanDays`, `unusedForDays`, `properties.include` and
  `keepNewest` is required, so a policy cannot select a whole scope by accident. All given
  conditions combine with AND:
  1. `olderThanDays`: the version's `created_at` is before run start minus the days.
  2. `unusedForDays`: the lead item's `last_download_at`, or its `created_at` if it was never
     downloaded, is before run start minus the days.
  3. `properties.include`: the lead item carries each listed key with at least one listed value;
     `properties.exclude`: a version whose lead item carries a listed key and value is never
     selected.
  4. `keepNewest: N` (N ≥ 1): per package, the N versions with the latest `created_at` are never
     selected, whatever the other conditions say; the newest version is therefore always kept.
- A day is 24 hours counted back from the run's start; no calendar arithmetic.
- Selected versions are removed through the format plug-in's `RemoveVersion` hook, into the trash
  can unless `bypassTrash` is set or the trash can is disabled.
- Repository caps (R-STOR-044) and policies act independently; whichever keeps less wins.

Acceptance criteria:

- Fixture: package `@acme/w` with versions 1.0–1.5 created 200 to 10 days ago, 1.1 downloaded
  yesterday. A dry run with `olderThanDays: 60, unusedForDays: 60, keepNewest: 2` selects exactly
  the versions older than 60 days that were not downloaded in 60 days and are not among the two
  newest.
- A policy with only `formats` and `repositories` is rejected with `422`.

### R-STOR-039 · Lead item, version units and container images
Priority: **P1** · Covers: RET-009, RET-010

- Every format plug-in MUST declare the lead item of each version when it commits the version
  (npm: the tarball; NuGet: the `.nupkg`; Maven: the POM; container images: the manifest or
  index the tag points at). Conditions are evaluated on the lead item only.
- A version is removed as one unit through `RemoveVersion`; partially removed versions do not
  occur. Generic repositories have no versions: each file is its own unit, `keepNewest` is not
  available (`422`), age, usage and property conditions apply per file.
- Container images (OCI, Docker and Helm OCI repositories; P1 formats): a tag is a version; an
  index and its child manifests form one unit and are removed together or not at all; layers
  and configs are removed when no remaining manifest of the repository references them (the
  format plug-in owns that rule). `keepNewest` counts tags per image name. Kept revisions of an
  overwritten tag (R-STOR-044) are removed together with their tag.
- A format without a lead-item declaration cannot be selected by package policies (`422` on
  save).

Acceptance criteria:

- For a Maven fixture, a policy whose property condition matches a property set only on the
  `.jar` of a version, not on its `.pom` (the lead item), selects nothing.
- Removing a multi-platform image tag by policy removes the index and all child manifests in one
  transaction; a concurrent `docker pull` of the tag sees either the whole image or `404`.

### R-STOR-040 · Policy scheduling, run limits and concurrency
Priority: **P1** · Covers: RET-011, RET-012, RET-018

- Schedules use the cron syntax of R-STOR-025 (UTC). Saving a schedule that fires more often than
  `retention.min_schedule_interval` is rejected with `422`.
- A run stops when it reaches `maxRunDurationMinutes`, `retention.max_versions_per_run` removed
  versions, or `retention.error_threshold_percent` failed versions (after at least 100 attempts).
  Everything removed stays removed; the next run re-evaluates from scratch, so a backlog is
  worked off incrementally. Selection is idempotent and safe to interrupt at any version.
- At most `retention.max_concurrent_runs` runs execute system-wide (counted in PostgreSQL); further
  runs wait as `queued` (visible in the history) up to `retention.queue_size`; beyond that, a
  scheduled trigger is skipped with a log entry and an audit event, a manual trigger answers
  `429`. A policy never runs twice at the same time; a trigger while it runs answers `409`.
- Runs are jobs in the job queue (INV-12); the scheduler that enqueues due runs holds an
  advisory-lock lease. A run whose worker disappears (lease lost) is marked `failed` with reason
  `worker-lost`.
- Runs and garbage collection may overlap (R-STOR-024).

Configuration:

```yaml
retention:
  min_schedule_interval: 6h      # default (proposed)
  max_concurrent_runs: 3         # default (proposed)
  queue_size: 10                 # default (proposed)
  max_versions_per_run: 100000   # default (proposed)
  error_threshold_percent: 10    # default (proposed)
  batch_size: 500                # default (proposed); versions removed per transaction
```

Acceptance criteria:

- `schedule: "0 * * * *"` is rejected; `"0 */6 * * *"` is accepted.
- A run with `maxRunDurationMinutes: 1` over 50,000 matching versions stops after about one minute
  with status `stopped-limit`; the next run continues with the remaining versions.

### R-STOR-041 · Manual run, dry run, stop and disable all
Priority: **P1** · Covers: RET-014, RET-015, RET-016, RET-017

| Method | Path | Body | Result |
|---|---|---|---|
| `POST` | `/api/v1/retention/policies/{key}/runs` | `{"dryRun": false}` | `202 {"runId": "…"}`; `409` disabled policy for a real run or run active; `429` queue full |
| `POST` | `/api/v1/retention/runs/{runId}/stop` | — | `202`; status `stopping`, then `stopped` |
| `POST` | `/api/v1/retention/runs/stop-all` | — | `202`; stops every running and queued run |
| `POST` | `/api/v1/retention/policies/disable-all` | — | `200 {"disabled": n}` |

- A dry run is allowed on disabled policies, evaluates exactly like a real run, removes nothing
  and produces a report (R-STOR-042). It reflects the moment it ran.
- A stopped run cannot be resumed; the next run starts from scratch.
- Every manual trigger, stop and disable-all records the acting principal in the run and in the
  audit log. Permission: system administrator.

Acceptance criteria:

- A dry run of a disabled policy answers `202`, finishes with status `completed` and
  `dryRun: true`, and leaves every version in place.
- `stop-all` during two active runs makes both end as `stopped` within one batch.

### R-STOR-042 · Run history and reports
Priority: **P1** · Covers: RET-020, RET-021

- Every run, real or dry, is a row with: policy key and configuration snapshot, `dryRun`,
  `manual`, `triggeredBy`, `status` (`queued`, `running`, `running-with-errors`, `completed`,
  `completed-with-errors`, `stopping`, `stopped`, `stopped-limit`, `failed`), start and end,
  versions evaluated, selected, removed, failed, logical and physical bytes freed (physical:
  blobs no other reference uses at the time of removal).
- Per-version records (package, version, repository, lead path, size, created, last download,
  status, message) are kept for `retention.item_detail_retention`.
- When a run selected at least one version, a CSV report is generated at the end (RFC 4180,
  UTF-8, one row per version) and stored as a blob referenced by the run row, so it survives as
  long as the run. Runs that select nothing have no report.
- API (system administrator):

  | Method | Path | Result |
  |---|---|---|
  | `GET` | `/api/v1/retention/runs?policy=&status=&from=&to=&dryRun=&excludeEmpty=&cursor=&limit=&sort=` | newest first |
  | `GET` | `/api/v1/retention/runs/{runId}` | one run with configuration snapshot |
  | `GET` | `/api/v1/retention/runs/{runId}/report` | `text/csv`; `404` when none |
  | `GET` | `/api/v1/retention/runs/{runId}/items?status=&cursor=` | per-version records while retained |

- Timestamps are RFC 3339 throughout.

```sql
CREATE TABLE retention_runs (
    id               uuid PRIMARY KEY,
    policy_key       text,                     -- not a FK: history outlives the policy
    policy_snapshot  jsonb NOT NULL,
    dry_run          boolean NOT NULL,
    manual           boolean NOT NULL,
    triggered_by     uuid,
    status           text NOT NULL,
    started_at       timestamptz,
    finished_at      timestamptz,
    counters         jsonb NOT NULL DEFAULT '{}',
    blob_sha256      bytea REFERENCES blobs (sha256),   -- CSV report; guard and release triggers installed
    error            text
);
CREATE INDEX retention_runs_blob ON retention_runs (blob_sha256);
CREATE INDEX retention_runs_policy ON retention_runs (policy_key, started_at DESC);

CREATE TABLE retention_run_items (
    run_id        uuid NOT NULL REFERENCES retention_runs (id) ON DELETE CASCADE,
    seq           integer NOT NULL,
    repository    text NOT NULL,
    package       text NOT NULL,
    version       text NOT NULL,
    lead_path     text NOT NULL,
    size          bigint,
    created_at    timestamptz,
    last_download timestamptz,
    status        text NOT NULL,
    message       text,
    PRIMARY KEY (run_id, seq)
);
```

Configuration:

```yaml
retention:
  history_retention: 2160h       # default (proposed): runs and reports kept 90 days
  item_detail_retention: 720h    # default (proposed): per-version records kept 30 days
```

Acceptance criteria:

- After a dry run that selects three versions, `curl …/runs/{id}/report` returns a CSV with a
  header and three rows; a run selecting nothing answers `404` for the report.
- The report blob survives garbage collection while the run row exists and is collected after
  the run row expires.

### R-STOR-043 · Build cleanup policy and per-build retention
Priority: **P1** · Covers: RET-023, RET-037

Depends on build records, which no spec page owns yet (Q-STOR-3). The requirement is written
against this minimal model: a build run has a name, a number, a start time and a list of
produced files given as repository, path and SHA-256.

- `build-cleanup` policy spec: `builds.include` / `builds.exclude` (name patterns, `*` allowed
  as in R-STOR-038), `olderThanDays` (required), `removeArtifacts` (default `true`).
- A selected build run's record is removed; with `removeArtifacts`, each recorded file is
  removed when the item at the recorded path still references the recorded SHA-256 (an
  overwritten path is left alone and reported). The result per build is `removed`,
  `partially-removed (x of y)` or `skipped`.
- Per-build retention: `PUT /api/v1/builds/{buildName}/retention` with
  `{"keepNewest": 20, "maxAgeDays": 180, "protectedNumbers": ["1.0.0-rc.1"], "removeArtifacts": false, "wait": false}`
  *(example)* stores the parameters on the build name and applies them at once (`202`, or `200`
  when `wait` is true and the work is done) and again after every new build of that name.
  Permission: `delete` on the build (build permissions per [authorization.md](authorization.md)).
  Without `keepNewest` and `maxAgeDays` the call stores nothing and answers `422`.
- Build records and reports are never selected by package policies.

Acceptance criteria:

- With `keepNewest: 2` on build `web`, publishing a third build removes the oldest record (and
  its files when `removeArtifacts` is true).

### R-STOR-044 · Repository caps enforced on push
Priority: **P1** · Covers: RET-035

The caps themselves (maximum distinct snapshots per artifact, maximum distinct tags per image,
kept revisions per overwritten tag) are repository settings owned by
[repositories-and-proxy.md](repositories-and-proxy.md). This page specifies enforcement.

- After a push commits, the format plug-in's `VersionGroup` names the affected group; the commit
  transaction writes it to an outbox. A job running every `retention.caps.interval` removes,
  per group, the oldest variants (by `created_at`) beyond the cap through the normal delete path
  (trash can applies).
- Enforcement is lazy: only groups that received a push are evaluated; lowering a cap does not
  shrink groups until their next push.
- For kept tag revisions, a revision is a hidden version row of the tag; when a tag is removed
  by the tag cap or a policy, its kept revisions go with it.
- A snapshot cap below 2 is rejected (an in-flight multi-file deploy would otherwise remove its
  own siblings).

Configuration:

```yaml
retention:
  caps:
    interval: 5m      # default (proposed)
```

Acceptance criteria:

- With a tag cap of 3 on `docker-local`, pushing tags `t1`–`t4` of one image leaves `t2`–`t4`
  within one interval; `t1` is in the trash can.

### R-STOR-045 · Unused remote-cache expiry
Priority: **P1** · Covers: RET-036

- The per-remote-repository setting "unused expiry" (a duration; zero or absent keeps cached
  content forever) is owned by [repositories-and-proxy.md](repositories-and-proxy.md).
- A system job on `retention.remote_cache_cleanup.schedule` removes cached file items of remote
  repositories whose last use, `coalesce(last_download_at, created_at)`, is older than their
  repository's expiry. Removal is permanent (no trash can); the content is fetched again on the
  next request. Removed blobs are collected by R-STOR-024.
- The job processes repositories one after another in batches and can be started manually with
  `POST /api/v1/maintenance/remote-cache-cleanups` (system administrator, `202`).

Configuration:

```yaml
retention:
  remote_cache_cleanup:
    schedule: "0 3 * * *"    # default (proposed): daily 03:00 UTC
```

Acceptance criteria:

- With expiry `48h` on `npm-remote`, a tarball last downloaded 3 days ago is removed by the job; a
  following `npm install` fetches it from the upstream again.

## Configuration reference

<details>
<summary>All storage and retention keys of trove.yaml</summary>

```yaml
storage:
  s3:
    endpoint: ""                         # default (proposed)
    region: eu-central-1                 # example
    bucket: trove-blobs                # example; required
    prefix: trove                      # default (proposed)
    force_path_style: false              # default (proposed)
    allow_insecure_endpoint: false       # default (proposed)
    ca_file: ""                          # default (proposed)
    storage_class: ""                    # default (proposed)
    payload_checksum: sha256             # default (proposed)
    checksums:
      request_calculation: when_supported  # default (proposed)
      response_validation: when_supported  # default (proposed)
    credentials:
      source: default                    # default (proposed)
      role_arn: ""
      web_identity_token_file: ""
      sts_endpoint: ""
      access_key_id_file: ""
      secret_access_key_file: ""
      session_token_file: ""
    sse:
      mode: none                         # default (proposed)
      kms_key_id: ""
      bucket_key: true                   # default (proposed)
    http:
      max_connections: 256               # default (proposed)
      dial_timeout: 5s                   # default (proposed)
      tls_handshake_timeout: 5s          # default (proposed)
      response_header_timeout: 30s       # default (proposed)
      idle_conn_timeout: 90s             # default (proposed)
      idle_transfer_timeout: 60s         # default (proposed)
      proxy_url: ""                      # default (proposed)
      proxy_credentials_file: ""
      no_proxy: ""
    retry:
      max_attempts: 5                    # default (proposed)
      max_backoff: 20s                   # default (proposed)
    circuit_breaker:
      failure_threshold: 5               # default (proposed)
      open_duration: 10s                 # default (proposed)
    readiness:
      fail_after: 60s                    # default (proposed)
      recover_after: 10s                 # default (proposed)
    multipart:
      threshold: 100MiB                  # default (proposed)
      part_size: 16MiB                   # default (proposed)
      concurrency: 4                     # default (proposed)
      max_concurrent_parts: 64           # default (proposed)
    presign:
      endpoint: ""                       # default (proposed)
      ttl: 60s                           # default (proposed)
      min_size: 1MiB                     # default (proposed)
    startup_check: true                  # default (proposed)
    startup_timeout: 5m                  # default (proposed)
  spool:
    dir: /var/lib/trove/spool          # default (proposed)
    max_bytes: 50GiB                     # default (proposed)
    min_free_bytes: 2GiB                 # default (proposed)
    memory_threshold: 1MiB               # default (proposed)
    warn_percent: 80                     # default (proposed)
  upload:
    max_size: 10GiB                      # default (proposed)
    commit_attempts: 5                   # default (proposed)
    commit_deadline: 1h                  # default (proposed)
    blob_wait_timeout: 120s              # default (proposed)
  uploads:
    session_ttl: 24h                     # default (proposed)
  deploy_by_checksum:
    enabled: true                        # default (proposed)
    max_candidates: 100                  # default (proposed)
  sidecars:
    pending_ttl: 1h                      # default (proposed)
  download:
    buffer_size: 256KiB                  # default (proposed)
    verify: true                         # default (proposed)
    hold_back: 64KiB                     # default (proposed)
  stats:
    flush_interval: 10s                  # default (proposed)
  delete:
    max_sync_items: 10000                # default (proposed)
    batch_size: 5000                     # default (proposed)
  properties:
    max_values_per_item: 1000            # default (proposed)
    max_recursive_items: 100000          # default (proposed)
  usage:
    fold_interval: 30s                   # default (proposed)
  gc:
    schedule: "*/30 * * * *"             # default (proposed)
    full_schedule: "0 2 * * 0"           # default (proposed)
    grace_period: 24h                    # default (proposed)
    delete_settle: 30s                   # default (proposed)
    batch_size: 1000                     # default (proposed)
    batch_delete: true                   # default (proposed)
    delete_concurrency: 8                # default (proposed)
  orphans:
    schedule: "0 5 * * 6"                # default (proposed)
    min_age: 24h                         # default (proposed)
    multipart_max_age: 24h               # default (proposed)
  scrub:
    schedule: "0 4 * * *"                # default (proposed)
    check_interval: 168h                 # default (proposed)
    deep_interval: 2160h                 # default (proposed)
    deep_bytes_per_second: 50MiB         # default (proposed)
  summary:
    schedule: "17 * * * *"               # default (proposed)
retention:
  min_schedule_interval: 6h              # default (proposed)
  max_concurrent_runs: 3                 # default (proposed)
  queue_size: 10                         # default (proposed)
  max_versions_per_run: 100000           # default (proposed)
  error_threshold_percent: 10            # default (proposed)
  batch_size: 500                        # default (proposed)
  history_retention: 2160h               # default (proposed)
  item_detail_retention: 720h            # default (proposed)
  caps:
    interval: 5m                         # default (proposed)
  remote_cache_cleanup:
    schedule: "0 3 * * *"                # default (proposed)
```

Durations use Go duration syntax (`h`, `m`, `s`; there is no `d` unit); sizes use IEC units
(`KiB`, `MiB`, `GiB`). Runtime settings (trash can, system quota) live in PostgreSQL and are
changed through `PUT /api/v1/settings/storage`.

</details>

## Deferred

<details>
<summary>Deferred catalog entries (51)</summary>

| Catalog ID | Priority | Note |
|---|---|---|
| STOR-005 | P2 | SHA-256 backfill: not needed, every digest is computed at ingest from the first release. |
| STOR-007 | P2 | Composable provider chain: one S3 store by design (INV-4). |
| STOR-008 | P2 | Local file-system store: unsafe with stateless replicas; MinIO covers on-premises use. |
| STOR-009 | P2 | Node-local read cache in front of S3. |
| STOR-010 | P2 | Database blob store: contradicts INV-4 (no binary content in PostgreSQL); not planned. |
| STOR-011 | P2 | Sharded store with redundancy. |
| STOR-012 | P2 | Health-aware and read-only shards. |
| STOR-013 | P2 | Cross-zone sharding. |
| STOR-014 | P2 | Cluster-shared node-local stores. |
| STOR-015 | P2 | Rebalancing of sharded stores. |
| STOR-016 | P2 | Asynchronous upload queue: conflicts with "201 means durable in S3" on stateless replicas. |
| STOR-018 | Later | Binary fetch workers for federated repositories. |
| STOR-026 | P2 | Multi-bucket sharding. |
| STOR-027 | P2 | Migration of an existing store into object storage. |
| STOR-028 | P2 | Storage-class (tiering) compatibility; `storage_class` exists, guidance pending. |
| STOR-030 | P2 | Google Cloud Storage native client. |
| STOR-031 | P2 | Azure Blob Storage. |
| STOR-032, STOR-033 | P2 | Specified as R-STOR-014 so the download path leaves room for it. |
| STOR-034 | P2 | CDN-signed download URLs. |
| STOR-035 | P2 | Client choice of the redirect target. |
| STOR-041 | Out of scope | Embedded database compaction; trove has no embedded database. |
| ART-005 | P2 | Deploy an archive and extract it on arrival. |
| ART-007 | P2 | Client-driven multipart upload with presigned part URLs; R-STOR-011 sessions are the server-side counterpart. |
| ART-010 | P2 | SHA-256 backfill operation; not needed (see STOR-005). |
| ART-012 | P2 | Property-constrained resolution on download. |
| ART-013 | P2 | Latest-version tokens in download paths. |
| ART-014 | P2 | Download an entry from inside an archive. |
| ART-015 | P2 | Folder or repository download as an archive. |
| ART-016 | P2 | Download the artifacts of a build as an archive. |
| ART-017 | P2 | Server-side download without transferring content. |
| ART-018, ART-019 | P2 | User-created time-limited signed download URLs and key rotation. |
| ART-020 | P2 | Filtered (templated) resources. |
| ART-030 | P2 | Last-modified lookup in a subtree. |
| ART-035 | P2 | Bulk version deletion dialog. |
| ART-039 | P2 | Purge of redundant trash-can folder records; the trash design here keeps no such records. |
| ART-043 | P2 | Public repository view under a path prefix. |
| ART-044 | P2 | Browsing uncached remote content. |
| ART-045 | P2 | Content browsing of files and archives (would need a separate content origin). |
| ART-046 | P2 | Followers (change notifications). |
| ART-047 | P2 | WebDAV access. |
| ART-051 | P2 | Background task list (job API in [architecture.md](architecture.md)). |
| ART-052 | P2 | RubyGems version list. |
| ART-053 | Out of scope | Push to a discontinued external distribution service. |
| RET-002 | P2 | Project-scoped policies; projects are not in the MVP. |
| RET-024 | Later | Release-set cleanup policy. |
| RET-025 | Later | Cleanup and archiving on federated repositories. |
| RET-026 | P2 | Retention and push replication. |
| RET-027 – RET-034 | Later | Package archive tier, archive pairing, search, restore, reports, overview, logging, artifact-level archive. |
| RET-039 | Later | Release-set retention on receiving instances. |
| RET-040, RET-041 | Out of scope | License-tier gating; portal provisioning of hosted archive instances. |
| RET-042 | P2 | Search-driven and scripted custom cleanup. |

</details>

## Security notes

- **No user input reaches object keys.** Keys derive only from a SHA-256 the server computed
  (R-STOR-001), so path traversal or key injection into the bucket is impossible by construction.
- **Deploy by checksum requires read access to existing content and SHA-256.** Without the read
  check, knowing a digest would be enough to copy content into a repository the attacker can
  read. Without the SHA-256 restriction, a crafted SHA-1 collision could bind a victim's path to
  attacker-chosen bytes. Unknown and unreadable content answer the same `404` (R-STOR-008).
- **Stored content is served as an attachment in a sandbox.** Anyone with `deploy` can upload HTML
  or SVG; served inline from trove's origin it would run script with the session of whoever
  opens it (the UI session cookie is same-origin). `Content-Disposition: attachment`, `nosniff`
  and `Content-Security-Policy: sandbox` close that path; directory indexes escape every name
  (R-STOR-012, R-STOR-017).
- **The bucket is a trust boundary.** Principals with access to the bucket read every blob of
  every repository, bypassing trove's permissions. The IAM policy of R-STOR-031 grants trove
  only its prefix; operators keep S3 Block Public Access on, require TLS (`aws:SecureTransport`
  in the bucket policy) and grant no other principal access to the prefix. Residual risk: H-11.
- **Credentials.** S3 credentials come from workload identity or from mounted files, never from
  inline configuration (R-STOR-031); the outbound proxy password likewise (R-STOR-034). Static
  keys are the weakest mode and need rotation by the operator.
- **Spool files contain unencrypted uploads** on the replica's ephemeral volume for the duration
  of a request. They are created `0600`, removed on every exit path and wiped at startup
  (R-STOR-004). Node-level disk encryption is the platform's responsibility.
- **Integrity.** Uploads are verified against declared digests and, where the store supports it,
  by the store against the payload checksum (R-STOR-006); full downloads are verified before the
  last bytes leave (R-STOR-013); the scrub catches damage at rest (R-STOR-027).
- **Mass deletion is privileged and observable.** Retention policies, trash purge, garbage
  collection and the orphan sweep are system-administrator operations; policies start disabled
  and can be dry-run; every run, trigger and removal is audited (INV-16).
- **Download statistics are personal data.** `last_downloader` links a principal to a download;
  it is cleared when the principal is erased (R-STOR-015).

## Open questions

### Q-STOR-1 · Readiness tied to object-storage reachability (INV-12)

INV-12 ties readiness to object-storage reachability. All replicas share one bucket, so an S3
outage makes every replica unready at the same moment, the Service loses all endpoints, and an
outage of blob traffic becomes a total outage: UI, API, logins, CI token exchange, metadata
requests, deletes and deduplicated uploads, all of which R-STOR-036 could still serve.

- (a) Keep INV-12 as written (readiness fails on S3 failure), softened only by the hysteresis of
  R-STOR-036.
- (b) Readiness reflects PostgreSQL and process health only; S3 health is a metric and an alert,
  and blob requests degrade individually through the circuit breaker (R-STOR-033).
- (c) Readiness fails on S3 only when this replica fails while others succeed (needs
  cross-replica health exchange through PostgreSQL).

Recommendation: (b). Readiness exists to route traffic away from a broken replica towards healthy
ones; with a shared dependency there is no healthy replica to route to, so (a) only removes the
parts that still work. (c) adds coordination for a rare case (a single replica losing its network
path to S3) that the circuit breaker already handles per request. This page specifies (a) with
hysteresis until INV-12 is revised.

### Q-STOR-2 · SHA-512 for every blob or only on demand

INV-4 says SHA-512 "where a format needs it".

- (a) Compute SHA-512 for every blob (this page).
- (b) Compute it only when the format plug-in asks for it; fill it later when another format needs
  it for an existing blob.

Recommendation: (a). Deduplication makes "the format that uploaded first" arbitrary: a tarball
first stored through a generic repository and later referenced by npm would need SHA-512 that
was never computed, which requires a re-read from S3 and a backfill job, the very machinery
(STOR-005) this design avoids. SHA-512 adds roughly one more hash pass of CPU per upload, which is
small next to network and S3 latency; revisit only if profiling shows uploads CPU-bound.

### Q-STOR-3 · Owner of build records

RET-023 and RET-037 (P1) need build records (catalog `BLD`), but no spec page owns them.

- (a) Add a spec page `builds.md` (`R-BLD`) for build records, their API and permissions; this page
  keeps only the cleanup mechanics (R-STOR-043).
- (b) This page also specifies build records.
- (c) Move RET-023 and RET-037 to P2 together with build metadata.

Recommendation: (a). Build records are a model of their own (publishing, promotion, permissions,
CI integration) and P1 in the catalog's rubric ("build metadata basics"); folding them into
storage would mix two subjects, and deferring them would leave the P1 rubric unmet.

### Q-STOR-4 · Cron dialect shared by all schedules

Storage jobs, retention policies and the maintenance jobs of
[operations-and-observability.md](operations-and-observability.md) all take schedules.

- (a) Standard 5-field cron (minute to day of week) in UTC, with `@hourly`, `@daily`, `@weekly`.
- (b) 6-field cron with a seconds field and `?` placeholders.
- (c) (a) with a configurable time zone per installation.

Recommendation: (a), the same parser for every schedule in trove. It is what Kubernetes CronJobs
and most operators read fluently; seconds precision has no use for jobs bounded to six-hour or
longer intervals; UTC avoids daylight-saving gaps that skip or double runs. (c) can be added
later without breaking (a).

### Q-STOR-5 · Does overwriting a file send the previous content to the trash can?

- (a) Yes, for user-deployed files in local repositories when the trash can is enabled (this
  page); generated metadata is replaced without trash.
- (b) No; overwrite replaces in place, only deletes go to the trash can.

Recommendation: (a). An overwrite destroys the previous content exactly like a delete; making it
irreversible would make the `overwrite` permission the one action the trash can does not protect
against, which is precisely the accident (a CI job republishing a release path) that a trash can
is for. The cost is trash volume for repositories that overwrite routinely, bounded by the
retention period.

### Q-STOR-6 · Download verification on by default

- (a) On by default (`storage.download.verify: true`, this page).
- (b) Off by default, recommended for high-assurance installations.

Recommendation: (a). The cost is one SHA-256 pass over streamed bytes, which is well above network
throughput on CPUs with SHA extensions; the benefit is that silent corruption or tampering in the
bucket never reaches a client as a complete file. Installations that redirect most downloads
(R-STOR-014) get no benefit either way.

### Q-STOR-7 · Status code for an exceeded quota

- (a) `507 Insufficient Storage` for quotas, `413` only for the per-request size limit (this page).
- (b) `413` for both.

Recommendation: (a). `413` tells a client its request was too large and invites splitting or
compressing it; for a full quota that diagnosis is wrong. `507` states the actual cause, and the
problem-details body carries the quota that was hit; package clients print the status line, so the
message reaches the user either way.

### Q-STOR-8 · Copy and move target semantics

- (a) `target.path` is the exact destination; an existing folder at the target is merged into
  (this page).
- (b) Unix `cp` semantics: copying folder `a/b` to an existing `x/b` produces `x/b/b`.

Recommendation: (a). An API call cannot see whether the target exists at the moment it runs, so
(b) makes the result of one request depend on state the caller may not know (the second run of a
promotion script nests instead of updating). (a) is idempotent: repeating a copy yields the same
tree.

### Q-STOR-9 · One pattern syntax across retention and permissions

Retention scopes (R-STOR-038) and permission policies ([authorization.md](authorization.md))
both match repository keys, paths and package names.

- (a) One matcher for both: exact values and `*` globs (with `**` for path segments if
  authorization needs it).
- (b) Each subsystem defines its own syntax.

Recommendation: (a). Operators write both kinds of pattern for the same repositories; two
syntaxes guarantee that a pattern copied from one into the other silently matches something
else. The authorization page should own the matcher and this page should reference it.

## What this does not cover

### H-11 · Object-store principals bypass trove's authorization and integrity checks

Anyone who can read the bucket prefix reads every blob of every repository; anyone who can write
it can replace content. trove detects replacement only on full streamed downloads (R-STOR-013)
and in the scrub (R-STOR-027): ranged downloads and presigned redirects deliver whatever the
bucket holds, and between two scrub passes altered content can be served this way. There is no
client-side encryption and no object signature. Live today for every installation; the exposure
is the set of principals with access to the bucket, which trove cannot see. Mitigations are
operational: least-privilege bucket policy, no other principals on the prefix, S3 Object Lock or
versioning to make replacement detectable and reversible.

### H-12 · Presigned download URLs are bearer capabilities

A redirect URL (R-STOR-014, P2, dormant until implemented) grants the object to anyone holding it
until it expires (`presign.ttl`, default 60 s) or the signing credentials expire. It cannot be
revoked, is not bound to the client, and may be logged by proxies, browsers or CI logs. Revoking
the requester's token does not invalidate URLs already issued.

### H-13 · Deduplication reveals the existence of known content through upload timing

A principal with `deploy` on any repository can upload a large file it already knows and observe
whether the S3 write was skipped (deduplication hit, R-STOR-005): the upload finishes measurably
faster when the same content is stored anywhere, including repositories the principal cannot
read. The attacker learns only that content it already possesses exists in the installation;
relevant where repositories separate tenants. Live today for every upload path. Deploy by
checksum does not leak this (unreadable content answers `404`).

### H-14 · No per-principal limit on concurrent uploads or spool use

The spool budget (R-STOR-004) is per replica and shared by all principals. A single token with
`deploy` can hold many slow concurrent uploads and exhaust the budget, making every other upload
on that replica fail with `503` until the requests time out. Per-principal concurrency and rate
limits are not specified on this page; if [api-and-ui.md](api-and-ui.md) defines request rate
limiting, it has to count concurrent upload bodies, not only requests per second.
