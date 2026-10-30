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
the object store, the behavior while the object store is unavailable, which role runs each part
of the storage work across any number of replicas (R-STOR-046), and what storage contributes to
trove's own backup and restore (R-STOR-047). Repository semantics (classes, virtual resolution,
pull-through) are in [repositories-and-proxy.md](repositories-and-proxy.md); the permission model
is in [authorization.md](authorization.md); the format plug-in interface, the job framework, the
Valkey usage map and the shutdown order are in [architecture.md](architecture.md); Helm values,
volumes, the platform backup layer and failure drills are in [ha-and-helm.md](ha-and-helm.md);
the backup job, its garbage-collection hold and the restore procedure are in
[backup-and-restore.md](backup-and-restore.md).

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
| Spool | Scratch space of one pod (an `emptyDir` or a generic ephemeral volume, never shared and never persistent, INV-13) that holds an upload for the duration of one request or job while it is hashed and before it is written to S3. |
| Upload session | The state of one blob uploaded over several requests: a PostgreSQL row and an S3 multipart upload, so that any replica continues it (R-STOR-011, INV-18). |
| Hold | A running backup's protection of the blobs it still has to copy from garbage collection ([backup-and-restore.md](backup-and-restore.md) R-BAK-007). |
| Deletion pause | The `system_state` flag of [ha-and-helm.md](ha-and-helm.md) R-HA-025 that stops every storage task from deleting until an operator resumes deletions. |
| Principal | A user, service account or CI identity as defined in [identity-and-authentication.md](identity-and-authentication.md). |

## Handover to other pages

Catalog entries of the three source pages that this page does not own:

| Catalog IDs | Owner | Reason |
|---|---|---|
| ART-006, ART-040, ART-041, ART-048, ART-049 | [api-and-ui.md](api-and-ui.md) | UI screens; they consume the APIs specified here. |
| ART-031 | [authorization.md](authorization.md) | Effective permissions of an item are an evaluation of the permission model. |
| ART-050, RMT-017, RMT-021, RMT-028, REPO-019, REPO-025, REPO-027 | [repositories-and-proxy.md](repositories-and-proxy.md) | Repository settings and remote-cache metadata semantics. This page specifies the storage mechanisms those settings drive (R-STOR-014, R-STOR-044, R-STOR-045). |
| OPS-065 | [operations-and-observability.md](operations-and-observability.md) | The task framework: schedules under `maintenance.tasks.<name>`, the task and job API, job visibility (R-OPS-019). The storage tasks run on it; their semantics, parameters and results are specified here (R-STOR-025 to R-STOR-028, R-STOR-045; Q-STOR-10). |
| SRCH-008 | no page yet | Checksum search; the indexes it needs are defined in R-STOR-003. |

Storage-facing hooks the format plug-in interface in [architecture.md](architecture.md) has to
offer, used throughout this page:

| Hook | Called | Purpose |
|---|---|---|
| `IngestOptions` | before an upload is spooled | Expected digests from the protocol (npm `integrity`, OCI digest), whether SHA-512 is needed (it is always computed, Q-STOR-2), media type. |
| `OnCommit` | inside the commit transaction of an upload | Create or update the package and version rows, mark the lead item, write format metadata, increment `packages.rev_seq` (R-STOR-003). |
| `OnItemsRemoved`, `OnItemsRestored` | inside the delete, trash, move and restore transactions | Keep format indexes consistent (an npm packument no longer lists a removed version) and increment `packages.rev_seq`. Formats with version tombstones write them on removal and remove a restored version's tombstone (R-STOR-022, R-STOR-023). |
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
  | `_uploads/<session-id>/data`, `_uploads/<session-id>/tail-<writer>` | The S3 multipart upload of a multi-request upload session, and the session's tail of less than one part, one object per request that wrote it (R-STOR-011, P1) | Everything below `_uploads/<session-id>/` is deleted on finalize, abort or expiry; a superseded tail is deleted once its successor is recorded; the orphan sweep removes leftovers (R-STOR-026). |
  | `_probe/<replica-id>` | Startup and conformance probe objects (R-STOR-035) | Deleted after each probe. |
  | `_journal/revocations/<yyyy>/<mm>/<dd>/<id>.json` | Revocation journal entries ([ha-and-helm.md](ha-and-helm.md) R-HA-054) | Written by the shipping job (a re-ship writes identical content); never deleted by trove; expired only by an operator lifecycle rule. |

  Any other object below `<prefix>/` is foreign: trove reports it and never deletes it.
- A backup copies only `sha256/` objects that its database snapshot references; the other key
  families are not part of a backup ([backup-and-restore.md](backup-and-restore.md) R-BAK-001).
  A backup target never lies below `<prefix>/`, and `<prefix>/` never lies below a backup target
  (R-BAK-002), so neither the orphan sweep nor a restore meets the other's objects.
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
- `packages.rev_seq` is the package's revision sequence ([architecture.md](architecture.md)
  R-ARCH-009). The core reads it, without format code, for read-your-writes metadata (R-ARCH-012)
  and for the merged-metadata check of virtual repositories (R-ARCH-015); a format keeps no second
  counter of its own.
  - Every storage hook or format write that changes a package's rows increments its `rev_seq` in
    the same transaction: publish or upload, delete, trash restore, copy, move, import, retention
    removal and remote cache fill. A copy increments it in the target repository, a move in both
    (R-STOR-019). Conformance kit check 9 (R-ARCH-010) fails a write that forgets it.
  - `rev_seq` never decreases for a repository and package name. A `packages` row therefore stays
    when the package's last version is removed, trashed or purged, and goes only with its
    repository (`ON DELETE CASCADE`). A row without live versions is a package that does not exist
    for every protocol and API. Deleting and re-creating the row would restart the sequence at 1,
    and a rendition cached under an earlier value could then be served again.
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
    rev_seq       bigint NOT NULL DEFAULT 1,   -- +1 on every change to the package's rows (R-ARCH-009)
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, name)                     -- also serves the rev_seq reads (R-ARCH-012, R-ARCH-015)
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
CREATE TABLE storage_usage_totals (             -- one row: physical totals of all live blobs
    id             smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    physical_bytes bigint NOT NULL DEFAULT 0,
    blobs          bigint NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE storage_usage_deltas (             -- append-only, folded by the worker loop below
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id        bigint,                      -- NULL: physical (blob) delta
    logical_bytes  bigint NOT NULL DEFAULT 0,
    files          bigint NOT NULL DEFAULT 0,
    folders        bigint NOT NULL DEFAULT 0,
    physical_bytes bigint NOT NULL DEFAULT 0,
    blobs          bigint NOT NULL DEFAULT 0
);
-- no index besides the primary key: the fold takes rows in id order, and no request reads them
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

Usage counters (`repository_usage` per repository, `storage_usage_totals` for the physical
totals) are maintained by statement-level triggers on `items` and `blobs` that append to
`storage_usage_deltas`. Writers never update a shared counter row, so concurrent uploads to one
repository from any number of `web` replicas do not contend on it (Q-STOR-12). The physical
totals count `live` blob rows: a row inserted as `live` adds to them, and a row leaving `live`
(R-STOR-024 step 3) subtracts; a row that the orphan sweep inserts as `deleting` (R-STOR-026)
never counts.

- **Fold.** Every `worker` replica runs the fold loop every `storage.usage.fold_interval` (5 s
  by default, R-STOR-046), and runs the next iteration at once while the previous one took a full
  10,000 rows, so a backlog cannot grow with the write rate. An iteration is a few statements on
  the main pool, so the short interval costs little. One iteration is one transaction:

  ```sql
  WITH d AS (
      DELETE FROM storage_usage_deltas
       WHERE id IN (SELECT id FROM storage_usage_deltas ORDER BY id LIMIT 10000
                    FOR UPDATE SKIP LOCKED)
      RETURNING repo_id, logical_bytes, files, folders, physical_bytes, blobs)
  SELECT repo_id, sum(logical_bytes), sum(files), sum(folders), sum(physical_bytes), sum(blobs)
    FROM d GROUP BY repo_id ORDER BY repo_id NULLS LAST;
  -- same transaction: upsert the repo_id rows into repository_usage in this order,
  -- then add the physical sums to storage_usage_totals; COMMIT
  ```

  Concurrent folds on several workers take disjoint delta rows (`SKIP LOCKED`) and update the
  counter rows in one order, so they neither count a delta twice nor deadlock
  ([deadlocks](https://www.postgresql.org/docs/current/explicit-locking.html#LOCKING-DEADLOCKS):
  "acquire locks on multiple objects in a consistent order"). No lease and no Valkey lock is
  involved; a worker drained mid-fold rolls back and its rows are folded by another one. The
  upsert joins `repositories`, so the deltas of a repository deleted meanwhile are dropped instead
  of failing every later iteration on the foreign key (physical deltas carry no repository and
  are unaffected). An iteration that fails is repeated at the next interval.
- **Reads.** Readers use the folded rows. Quota admission (R-STOR-029) reads one row by primary
  key per enabled limit, and the storage summary (R-STOR-028) reads them as they are; both lag by
  at most one fold interval plus one iteration. Neither adds the unfolded deltas: their number
  grows with the cluster's write rate, so summing them in every admission would make each
  upload's cost grow with the upload rate of all replicas. A reader that needs exact values
  (tests, the acceptance criterion below) adds the unfolded deltas in the same statement as the
  folded rows, so one snapshot covers both and a concurrent fold can neither hide a delta nor
  count it twice.
- **Recount.** `storage_usage_recount()` recomputes `repository_usage` and `storage_usage_totals`
  from `items` and `blobs` and empties `storage_usage_deltas`, in one transaction. It runs only
  where no writer runs: as a fix-up of a backup restore (R-STOR-047), because a backup exports
  only referenced blob rows and the physical totals would otherwise still count rows the restore
  never loads.

Acceptance criteria:

- `trove migrate` on an empty database creates the tables; the migration test lists every
  foreign key to `blobs` with index and triggers present.
- A property value of 2,400 four-byte characters can be stored and found by key and value.
- Two items with paths `a/b` and `a/b0` in one repository: a subtree query for `a/b` returns only
  `a/b` and its descendants.
- Deleting the only version of a package leaves its `packages` row in place with a higher
  `rev_seq`; publishing a new version afterwards raises it again rather than restarting at 1.
- Three `worker` replicas fold while 32 goroutines on three `web` replicas upload to and delete
  from four repositories for five minutes. One fold interval after the load stops,
  `repository_usage` equals the sums over untrashed file items per repository,
  `storage_usage_totals` equals the count and size of `live` blobs, and
  `pg_stat_database.deadlocks` has not grown.
- A repository is deleted while its items are removed in the background and the fold runs: the
  fold keeps folding, `storage_usage_deltas` is empty one fold interval after the removal ends,
  and no iteration fails twice in a row.

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
   `Expect: 100-continue`. Bodies without a declared length (chunked) reserve spool budget in
   increments while streaming. Such a body fails:
   - with `413` as soon as it passes the effective maximum upload size; `413` is reserved for that
     case;
   - with `503` and `Retry-After` when it cannot reserve its next increment, because the spool
     budget is exhausted or the spool volume has fallen below `storage.spool.min_free_bytes`. The
     spool file is removed and nothing is committed, as for any cut upload.

   The second status matters for clients that always stream. The NuGet clients send every push
   chunked and retry a 5xx after waiting for its `Retry-After`, which
   [formats/nuget.md](formats/nuget.md) R-NUGET-002 caps at 30 s on NuGet endpoints
   (R-NUGET-032). A `503` therefore delays a push on a full spool until budget is free, possibly on
   another replica, while a `413` would fail it for good with a message that blames the package.
2. **Spool.** Bodies with a known length up to `storage.spool.memory_threshold` MAY be buffered in
   memory; all others MUST be written to a new file in `storage.spool.dir`, created with mode
   `0600`, named by a random UUID. The spool is scratch space of one pod: an `emptyDir` or a
   generic ephemeral volume (one ReadWriteOnce or ReadWriteOncePod claim per pod, deleted with
   it), never shared between pods and never persistent (INV-13, [ha-and-helm.md](ha-and-helm.md)
   R-HA-035, R-HA-061). It holds data of the running request or job only and never holds
   acknowledged data. `web` and `worker` pods each have one (R-STOR-046).
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
7. **No state between requests.** Nothing an upload leaves in the spool or in memory is used by a
   later request (INV-18): the next request of the same client may reach any replica. A protocol
   that sends one blob over several requests MUST use an upload session (R-STOR-011), whose state
   is in PostgreSQL and whose bytes are in S3.
8. **Shutdown.** At the shutdown deadline ([architecture.md](architecture.md) R-ARCH-024 step 5)
   an upload whose body is still arriving is cut: its spool file is removed, nothing is
   committed, and the client's retry reaches another replica. An upload whose body has arrived
   completely is not cut at the deadline: it runs on into the margin that R-ARCH-024 grants (at
   most 3 s of the grace period's 5 s margin; [architecture.md](architecture.md) P-11 bounds that
   work). If its object write has not finished by the end of that margin, it is cut before its
   commit in the same way, and its multipart upload is aborted. Once its commit transaction has
   started, it is neither cancelled nor cut before its status line, so a drain never makes a
   client retry a publish that committed, a retry that would meet its own version as a conflict
   (`409`). A request
   of an upload session (R-STOR-011) that is cut keeps the parts it has recorded and never aborts
   the session's multipart upload: it cancels its outstanding `UploadPart`, waits for the call to
   return, and releases its writer claim in one short transaction, so the client's retry continues
   the session on another replica at once instead of waiting for the claim to expire. A
   remote-cache fill still running 5 s before the deadline is cancelled before its commit, and its
   flight lock is released with `failed` (R-ARCH-024 step 4, R-ARCH-033).

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

- With `storage.upload.max_size: 10MiB`, `curl -T 11mb.bin …/files/generic-local/x.bin` answers `413` without the
  body being transferred (`curl -v` shows no `100 Continue`).
- Killing the client mid-upload leaves no spool file and no blob row; the spool budget metric
  returns to its previous value.
- With `storage.spool.max_bytes: 100MiB` and `storage.upload.max_size: 90MiB`, two concurrent
  chunked uploads of 80 MiB each (`curl -T - …/files/generic-local/a.bin < 80mb.bin`, which sends
  `Transfer-Encoding: chunked`): one answers `201`, the other `503` with `Retry-After` and leaves
  no spool file and no item; repeated afterwards, it answers `201`. A chunked upload of 95 MiB
  answers `413`.
- `npm publish` of a tarball whose `dist.integrity` was edited to a wrong value answers an error
  and stores nothing.
- A replica restarted with files in its spool directory starts with an empty spool directory. Only the
  files the spool creates are removed: regular files named by a random UUID, as R-STOR-004 creates them. Any
  other entry makes the start fail, and a `lost+found` directory is the one entry tolerated
  ([Q-STOR-16](#q-stor-16--what-the-spool-may-delete-at-start)).
- A test build delays the S3 write of a 500 MiB `curl -T` past the end of the shutdown margin and
  the serving pod is deleted: `curl` reports an error, no item row and no blob row exists, and
  `aws s3api list-multipart-uploads` lists no upload of that pod. Repeating the same `curl -T`
  through the Service answers `201`.
- `kubectl delete pod` of the `web` replica that receives a chunked upload's streamed `PATCH`
  (P1, R-STOR-011): the session's multipart upload still exists afterwards, and the client's
  status request and next `PATCH` through the Service are answered `204` and `202` without a `416`.

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
loop (at most storage.upload.commit_attempts times):
  BEGIN (READ COMMITTED)
  SELECT state, content_status FROM blobs WHERE sha256 = $h FOR SHARE
  case no row:
      if not putDone or now - putAt > min(storage.upload.commit_deadline, storage.gc.delete_settle / 2):
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
- The insert of a new blob row is bounded by `min(storage.upload.commit_deadline, storage.gc.delete_settle / 2)`
  counted from the end of the object write. After that bound the object is written again before the insert, so
  an insert never follows a collection's delete that may still be in flight (R-STOR-024). `delete_settle` has a
  minimum of 1 s.
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
    blob_wait_timeout: 120s    # default (proposed); must exceed 2 × storage.gc.delete_settle (R-STOR-024)
```

Acceptance criteria:

- Publishing the same npm tarball to `npm-a` and `npm-b`
  (`npm publish --registry https://trove.example.com/api/npm/npm-b/`) causes exactly one S3
  `PutObject` (S3 request metric `trove_storage_s3_requests_total{operation="put"}` grows by one) and
  two item rows referencing one blob row.
- Race test (Go integration test against PostgreSQL and the per-change S3 store of
  [ha-and-helm.md](ha-and-helm.md) R-HA-051, run in CI): 32 goroutines upload
  the same content to random paths while other goroutines delete those items and garbage
  collection runs with `storage.gc.grace_period: 0s` in a loop for 10 minutes; afterwards every untrashed and
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
  The multipart upload of an upload session (R-STOR-011) lives at most
  `storage.uploads.session_max_age`, which stays below both that rule and the sweep's
  `storage.orphans.multipart_max_age`, because both count from the upload's initiation; neither
  can abort an open session.
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

- A 300 MiB generic upload produces one multipart upload with 19 parts (16 MiB parts) in the
  request log of the S3 store, and `aws s3api head-object` reports 314,572,800 bytes.
- Killing the replica in the middle of a multipart upload leaves an incomplete multipart upload
  that `aws s3api list-multipart-uploads` shows until the next orphan sweep, which aborts it.
- With `storage.s3.payload_checksum: none` against a store without additional-checksum support, uploads work.

### R-STOR-007 · Raw deploy and folder creation
Priority: **MVP** · Covers: ART-002

`PUT /files/{repoKey}/{path}` deploys one file into a local repository, or into the default
deployment target of a virtual repository ([repositories-and-proxy.md](repositories-and-proxy.md)).

| | |
|---|---|
| Request | Body: the content. Optional `Repr-Digest` (sha-256, sha-512); optional `Content-Type` (stored as media type); optional properties (R-STOR-009); `Authorization` per INV-8. |
| Response | `201 Created` with `Location` (the `/files` URL) and the item description below; `200` for an identical re-upload (R-STOR-005). |
| Permission | `deploy` on the path; additionally `overwrite` when replacing different content. |
| Errors | `400` invalid path or length mismatch; `401`; `403`; `404` unknown repository; `405` repository class does not accept deploys (remote, virtual without deployment target); `409` digest mismatch, or a format rule refuses the path, or a path conflict (`path_conflict`); an existing item with other content and no `overwrite` permission answers `403` (Q-STOR-17); `413` too large; `503` spool or object store unavailable; `507` quota exceeded. Bodies are problem details per [api-and-ui.md](api-and-ui.md). |

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
  description. Otherwise the answer is `404` with problem code `content_not_found`, telling the
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
  `verify-client`). A sidecar uploaded before its file is kept in the short-lived PostgreSQL table
  `pending_sidecars` and applied when the file arrives (`storage.sidecars.pending_ttl`), whichever
  replica receives the file (INV-18).
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

Some protocols upload one blob over several requests: the OCI chunked blob upload (see the OCI
format page once written) and resumable generic uploads. The Service spreads requests at random
(INV-12), so consecutive requests of one session reach different replicas, and a node drain may
end any of them. Between requests a session therefore keeps its state in PostgreSQL and its bytes
in one S3 multipart upload (INV-18, Q-STOR-11). No replica's spool or memory holds anything of a
session after a request ends, and Valkey holds nothing of it: losing a session would cost the
client a complete re-upload, more than the one retry that [architecture.md](architecture.md)
R-ARCH-032 allows for state in Valkey.

**State.** One `upload_sessions` row per session and one `upload_session_parts` row per completed
part hold:

- the repository, the principal, the received byte count and the serialized state of the four
  hash functions at that count. Go's hash implementations marshal and restore their internal state
  ([`crypto/sha256`](https://pkg.go.dev/crypto/sha256) and the SHA-1, MD5 and SHA-512 packages),
  so any replica continues hashing where the previous request stopped;
- the S3 multipart upload of the staging object `<prefix>/_uploads/<session-id>/data`, created
  with the session, with the server-side encryption of R-STOR-032;
- the *tail*: the bytes after the last completed part, always fewer than
  `storage.uploads.part_size`, kept as the object `<prefix>/_uploads/<session-id>/tail-<writer>`,
  where `<writer>` is the claim token of the request that wrote it (`tail_writer`), together with
  the tail's size and SHA-256. Every request writes its tail under its own key, so a writer that
  lost its claim can never overwrite the tail that the session records;
- the writer claim (`writer`, `writer_until`) and the state.

**A request that carries bytes** (an OCI `PATCH`, or the body of the closing `PUT`):

1. **Authorize.** The request's principal MUST be the session's principal, and `deploy` on the
   session's repository is evaluated again on every request, so a permission revoked during an
   upload ends it. A request of another principal answers `404`, as for an unknown session. The
   session ID is a random UUID (version 4) and appears in upload URLs, so it is not a secret.
2. **Claim.** A closing `PUT` that finds the session in `completing`, `assembled` or `done` is a
   retried finalize (below). Any other request that finds the session not `open`, or older than
   `storage.uploads.session_max_age`, answers `404` (OCI `BLOB_UPLOAD_UNKNOWN`), and an open
   session past that age is set to `aborted`; its objects are removed under *Expiry*. Every
   request checks the age itself, so a session can never be continued past the multipart expiry
   of R-STOR-006, however long collection runs are delayed or paused. Otherwise one short
   transaction sets `writer` to a fresh random token and
   `writer_until = now() + storage.uploads.writer_lease`, only if no other claim is unexpired and
   the request's first byte equals `received`. Otherwise the answer is `416` with the current
   range, which is what the OCI specification requires for a chunk out of order. A claim held by a
   replica that was drained is released at once (R-STOR-004 step 8); only a crashed holder's claim
   blocks the session until `writer_until`. No transaction stays open after the claim
   (R-ARCH-019).
3. **Stream.** The body passes through the restored hash states into the spool and is cut into
   parts of exactly `part_size`. The first part of a request begins with the recorded tail, read
   back from S3 and checked against the recorded size and SHA-256 (a mismatch fails the session);
   its bytes are already in the hash state and are not hashed again. Each full part is sent with
   `UploadPart` (with the payload checksum of R-STOR-006) under the next consecutive part number,
   as S3 requires when parts carry checksums
   ([multipart upload](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html)).
   One transaction then records the part row (number, size, ETag, SHA-256), the new `received`,
   the hash state, `tail_size = 0` and no tail (the tail is now inside the part), `last_activity_at`
   and a renewed `writer_until`, conditional on this request's writer token. A request whose token
   no longer matches stops and answers `416`.
4. **End of body.** The remaining bytes, after the recorded tail if no part consumed it, are
   written as this request's tail object `tail-<writer>`, and one transaction records `received`,
   the hash state, the tail's size, SHA-256 and writer, `last_activity_at`, and releases the claim,
   conditional on the writer token. Only after that commit does the request delete the superseded
   tail object; a request whose transaction failed deletes nothing. The response reports the
   received range (OCI `Range: 0-<received − 1>`).
5. **Limits.** A request that would raise `received` above the effective maximum upload size of
   R-STOR-004 answers `413` and aborts the session. A request occupies at most one part plus its
   own read buffer in the spool, whatever the chunk size.

**Finalize** (the OCI closing `PUT` with `digest`):

1. Any bytes in the request are handled as above. The computed SHA-256 MUST equal the `digest`
   the client names; on a mismatch the protocol's error is returned (OCI `DIGEST_INVALID`) and the
   session stays open. The change from `open` to `completing` takes the writer claim as in step 2
   and is refused with `416` while another request holds an unexpired claim; claims need state
   `open`, so no request writes bytes once a finalize has begun.
2. State `completing`: the tail is read back, checked as in step 3, and uploaded as the last part,
   which S3 allows to be smaller than 5 MiB
   ([`EntityTooSmall`](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html)
   applies to every other part), and `CompleteMultipartUpload` runs with the recorded parts, their
   ETags and checksums. S3 refuses a part whose ETag differs from the recorded one (`InvalidPart`),
   so a part overwritten by a stale writer fails the session instead of corrupting the blob. A
   session without a full part aborts its multipart upload and uses the tail object as its
   staging object instead.
3. State `assembled`: the commit protocol of R-STOR-005 runs, with a server-side copy of the
   staging object to the content address as its object write (`CopyObject` up to 5 GiB,
   `UploadPartCopy` above); a deduplication hit copies nothing. No byte passes through trove.
4. Every object below `_uploads/<session-id>/` is deleted, the state becomes `done`, and the
   answer is `201`.

Each finalize step is recorded before the next starts, and each is idempotent. A finalize cut by
a drain (R-ARCH-024) is therefore repeated by the client's retry on any replica and continues at
the recorded state, without a claim: the retry's `digest` must equal the recorded one (otherwise
`DIGEST_INVALID`), its body is not read again, and a session already `done` answers `201`. A
`CompleteMultipartUpload` that answers `NoSuchUpload` in
state `completing` is resolved by a `HeadObject` of the staging object: an object of the session's
size means the earlier attempt completed. When two finalize requests of one session overlap (a
client retry while the first still runs), a step that fails because the other already advanced
the session (the staging object is gone, the state moved on) re-reads the state and continues
from there, so both answer `201` for the one stored blob.

**Resumption.** A request cut while its body arrives loses only the bytes after its last recorded
part, and `received` stays at its last recorded value. A request cut by a drain releases its claim
first (R-STOR-004 step 8). A client that asks for the upload status (OCI `GET` on
the upload location, answered `204` with `Range`) continues from there on any replica; one that
resends from its own offset gets `416` and asks
([OCI distribution specification](https://github.com/opencontainers/distribution-spec/blob/main/spec.md)).
The session start answers with `OCI-Chunk-Min-Length: <part_size>`, which that specification lets a
registry send and clients SHOULD honour; smaller chunks are accepted through the tail.

**Expiry.** A session without a recorded request for `storage.uploads.session_ttl`, or older than
`storage.uploads.session_max_age`, is aborted by the next collection run (R-STOR-025): state
`aborted`, `AbortMultipartUpload`, every object below `_uploads/<session-id>/` deleted; the run
does the same S3 clean-up for sessions that a request already set to `aborted`. Requests
enforce `session_max_age` themselves (step 2), so a delayed or paused collection run only delays
the clean-up. `session_max_age` MUST be shorter than `storage.orphans.multipart_max_age` and than
the bucket's lifecycle rule (R-STOR-006). Startup refuses a value not below `multipart_max_age`;
keeping the lifecycle rule at one day or longer is the operator's part, because the permissions of
R-STOR-031 do not let trove read it. The orphan sweep (R-STOR-026) removes what an aborted run
left.

```sql
CREATE TABLE upload_sessions (
    id                uuid PRIMARY KEY,            -- random (version 4); appears in upload URLs
    repo_id           bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    principal_id      uuid,                        -- every request must come from this principal
    received          bigint NOT NULL DEFAULT 0,
    hash_state        bytea NOT NULL,              -- SHA-256, SHA-1, MD5, SHA-512 states at `received`
    part_size         bigint NOT NULL,             -- fixed when the session starts
    s3_upload_id      text NOT NULL,               -- multipart upload of _uploads/<id>/data
    tail_size         bigint NOT NULL DEFAULT 0 CHECK (tail_size < part_size),
    tail_writer       uuid,                        -- names the tail object _uploads/<id>/tail-<tail_writer>
    tail_sha256       bytea,                       -- checked when the tail is read back
    writer            uuid,                        -- claim token of the request writing now
    writer_until      timestamptz,
    digest            bytea,                       -- SHA-256 named by the client at finalize
    state             text NOT NULL CHECK (state IN ('open', 'completing', 'assembled', 'done', 'aborted')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_activity_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((tail_size = 0) = (tail_writer IS NULL) AND (tail_writer IS NULL) = (tail_sha256 IS NULL))
);
CREATE INDEX upload_sessions_live ON upload_sessions (last_activity_at)
    WHERE state IN ('open', 'completing', 'assembled');

CREATE TABLE upload_session_parts (
    session_id   uuid NOT NULL REFERENCES upload_sessions (id) ON DELETE CASCADE,
    part_number  integer NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    size         bigint NOT NULL,
    etag         text NOT NULL,
    sha256       bytea NOT NULL,                   -- payload checksum sent with the part
    PRIMARY KEY (session_id, part_number)
);
```

Both tables have the backup export class `transient` (R-STOR-047): staged bytes are not backed up,
and a client restarts its upload after a restore.

Configuration:

```yaml
storage:
  uploads:
    part_size: 16MiB        # default (proposed); minimum 5MiB; storage.upload.max_size must not exceed 10,000 parts
    writer_lease: 60s       # default (proposed); renewed with every recorded part
    session_ttl: 4h         # default (proposed); idle time after which a session is aborted
    session_max_age: 20h    # default (proposed); must be < storage.orphans.multipart_max_age and the lifecycle rule
```

Acceptance criteria:

- A chunked upload whose three chunks are sent through three different replicas (the test pins
  each request to one pod) yields a blob whose SHA-256 equals the file's.
- A 2 GiB blob sent as one streamed `PATCH` while the receiving `web` pod is drained: the `PATCH`
  is cut, the upload status reports a multiple of `part_size`, the client resumes through the
  Service on another replica without receiving a `416`, and the closing `PUT` answers `201` with
  the right digest. Afterwards `aws s3api list-multipart-uploads` shows no upload of that session.
- A fault-injection build delays a `PATCH` after its body ended until its claim has expired and a
  second `PATCH` has continued the session; the first then writes its tail object and fails its
  transaction. The session's recorded tail is unchanged, and the closing `PUT` answers `201` with
  the right digest.
- A session older than `session_max_age` while the deletion pause keeps collection from cleaning
  up: its next `PATCH` answers `404` with `BLOB_UPLOAD_UNKNOWN`, the session is `aborted`, and the
  first collection run after deletions resume removes its multipart upload and objects.
- 100 chunks of 1 MiB each produce parts of 16 MiB (`aws s3api list-parts` during the upload) and
  a correct blob.
- Two concurrent `PATCH` requests for the same range: one is recorded, the other answers `416`,
  and the blob is correct.
- A closing `PUT` that is cut after `CompleteMultipartUpload` and repeated on another replica
  answers `201` and stores one blob.
- A session idle for longer than `session_ttl` is aborted by the next collection run; its
  multipart upload and staging objects are gone.
- A request with another principal's token on the session's URL answers `404`.

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
- **Every blob body has a declared length.** Every response that carries a stored blob's bytes
  (`200`, and `206` with the length of the range) MUST carry `Content-Length` and MUST NOT use the
  chunked transfer coding. The size comes from the blob row, so it is known before the first byte
  is sent. A body that ends early, for example because a replica's shutdown deadline cut it
  ([architecture.md](architecture.md) R-ARCH-024 step 5, Q-ARCH-18), is therefore detectable by
  the client as incomplete and is never taken for the whole file. A remote-cache fill that the
  fetching replica streams to its own client while the fetch runs follows the withheld-tail rule
  of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-041 instead.
- **User content is never rendered as part of trove's origin.** Responses carrying stored
  content MUST include these headers:
  - `X-Content-Type-Options: nosniff`;
  - `Content-Security-Policy: sandbox; default-src 'none'`;
  - `Cross-Origin-Resource-Policy: same-site`;
  - `Content-Disposition: attachment; filename*=UTF-8''<percent-encoded file name>`.

  The core sets them, not the handler ([architecture.md](architecture.md) R-ARCH-007). The
  `/files/` handler and the format dispatcher wrap the response writer before the handler runs,
  and the wrapper checks the headers again at `WriteHeader`. A handler can set `Content-Type` but
  cannot remove or weaken these headers. `same-site` keeps other sites from loading stored content
  as a subresource, while the UI can still show package images with `img`. The optional content
  origin (R-ARCH-029, P1) changes this to `cross-origin` on the content host only.
- **Conditional:** `If-None-Match` (ETag) and `If-Modified-Since` answer `304`.
- **Range** ([RFC 9110 §14](https://www.rfc-editor.org/rfc/rfc9110#section-14)): a single byte
  range MUST be served with `206` and `Content-Range`, forwarded to S3 as a ranged `GetObject`;
  `If-Range` with the ETag or date is honored; an unsatisfiable range answers `416` with
  `Content-Range: bytes */<size>`. A request with several ranges is answered `200` with the full
  content (RFC 9110 permits ignoring `Range`).
- **Resuming on any replica.** Every replica MUST answer `Range` with `If-Range: "<sha256 hex>"`
  alike: `206` with the requested bytes when the ETag matches the item's current blob, `200` with
  the full content when it does not. The ETag is the content address and the item lookup is in
  PostgreSQL, so no replica needs state from the one that served the earlier part (INV-18). A
  download cut at one replica's shutdown deadline therefore resumes with `206` on whichever
  replica the client reaches next through the Service ([architecture.md](architecture.md)
  R-ARCH-024, Q-ARCH-18).
- **Caching:** `Cache-Control: private, no-cache` for authenticated requests, `no-cache` for
  anonymous ones; clients revalidate with the ETag.
- **Errors:** `401`; `403` no `read`; `404` no such untrashed item, and also for a stored item
  that its format refuses through the serve-time check of [architecture.md](architecture.md)
  R-ARCH-008. npm refuses the tarball of a tombstoned version, of a version a restore's journal
  replay withheld, and of a version removed upstream, on `/files` exactly as on its own tarball
  route ([formats/npm.md](formats/npm.md) R-NPM-003, R-NPM-015, R-NPM-026, R-NPM-047). The check
  runs before the `HEAD` and conditional answers, so neither a `200` to `HEAD` nor a `304` tells a
  client that the refused bytes are still stored. Problem code
  `content_unavailable` when the blob's `content_status` is not `ok`: `503` with `Retry-After` for
  `missing`, because the object may come back through a version restore, replication or a restore
  ([ha-and-helm.md](ha-and-helm.md) R-HA-024, [backup-and-restore.md](backup-and-restore.md)
  R-BAK-001), and `500` for `corrupt`; remote repositories instead treat the item as a cache miss
  and refetch. `503` with `Retry-After` when the object store is unreachable (R-STOR-036).
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
- `curl -r 100- -H 'If-Range: "<sha256>"' …`, sent to each `web` pod in turn through
  `kubectl port-forward`, answers `206` with bytes equal to `tail -c +101 widget.bin` on every pod;
  with `If-Range: "<another sha256>"` it answers `200` with the full content. `curl -D - -o /dev/null`
  of the full file shows `Content-Length` and no `Transfer-Encoding: chunked`.
- An uploaded `evil.html` served from `/files` carries `Content-Disposition: attachment`, the
  sandbox CSP, `nosniff` and `Cross-Origin-Resource-Policy: same-site`; a browser opening the URL
  downloads it instead of rendering it.
- A test handler that deletes `Content-Security-Policy` and sets
  `Cross-Origin-Resource-Policy: cross-origin` before writing still sends the sandbox CSP and
  `same-site`.
- `npm install @acme/widget --registry https://trove.example.com/api/npm/npm-all/` and
  `dotnet restore` against the NuGet source download through this path.

### R-STOR-013 · Download integrity verification
Priority: **MVP** · Covers: trove-only

- For full-content `GET` responses streamed through trove, the server MUST compute the SHA-256
  of the bytes it sends and hold back the final `storage.download.hold_back` bytes until the S3
  body has ended and the digest matches the blob's SHA-256.
- When the digest does not match, or the S3 body does not deliver exactly the blob's size, the
  server MUST NOT send the held-back bytes and MUST abort the response (HTTP/1.1: close the
  connection; HTTP/2 and HTTP/3: reset the stream). The client sees a truncated transfer, never a
  complete corrupted file.
- Only evidence about the stored object changes its status: an S3 response whose
  `Content-Length` differs from the blob's size, or a body read to its end whose SHA-256 differs,
  MUST set the blob's `content_status` to `corrupt` and emit an audit event and the metric
  `trove_storage_blobs_damaged_total{status="corrupt"}`. A transport error while reading the body
  (connection reset, idle timeout, unexpected end of the body) aborts the response the same way
  but MUST NOT change the status, as for the scrub (R-STOR-027). A check that is on by default must
  not take content offline (R-STOR-012 answers `500` for a `corrupt` blob) because of a network
  fault.
- Ranged responses and presigned redirects (R-STOR-014) cannot be verified this way; see H-11.
- The check is on by default (Q-STOR-6). `storage.download.verify: false` disables it for
  replicas whose CPU, not their network, limits download throughput.

Configuration:

```yaml
storage:
  download:
    verify: true          # default (proposed)
    hold_back: 64KiB      # default (proposed); at least 1 KiB: 0 would release bytes before the digest check
```

Acceptance criteria:

- Overwriting an object in the S3 store with different bytes of the same length (`aws s3 cp`
  directly into the bucket) and downloading the item with `curl --http1.1` makes `curl` report a partial
  transfer (exit code 18), and the blob shows `content_status = 'corrupt'`.
- A fault proxy that resets the connection between trove and the S3 store in the middle of a download
  makes `curl` report a partial transfer, and the blob keeps `content_status = 'ok'`; the next
  download succeeds.

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
- Each `web` replica aggregates counts in memory and flushes them every
  `storage.stats.flush_interval` with one upsert per item (`downloads = downloads + n`,
  `last_download_at = greatest(…)`, `last_downloader` of the latest download), in one transaction
  per batch with the items in id order. Requests reach replicas at random, so several replicas
  flush the same popular items in every interval; the common order keeps their batches from
  deadlocking on each other's rows. The upsert joins `items`, so the counts of an item removed
  since its download are dropped instead of failing the batch on the foreign key; a batch that
  fails anyway stays in the buffer and is retried at the next flush.
- The buffer is flushed at shutdown ([architecture.md](architecture.md) R-ARCH-024 step 6), so a
  drain loses no counts; a replica crash loses at most one interval. Statistics are not audit
  data. No request reads the buffer: the API below and the retention conditions read PostgreSQL,
  which every replica sees alike (INV-18, Q-STOR-12).
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
- The HTML index is rendered by trove, not stored content, so it carries no `Content-Disposition`.
  It still carries `nosniff` and `Cross-Origin-Resource-Policy: same-site` (`same-origin` when the
  session cookie authenticated the request, [architecture.md](architecture.md) R-ARCH-007). The core's writer
  wrapper of R-STOR-012 selects this header set for a folder `GET` before the handler runs, so the
  handler cannot weaken it either.
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
  `storage.delete.batch_size` items; a crash resumes with the remainder. The job removes only
  items the check saw (Q-STOR-143): one that is deployed into the subtree afterwards stays.
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
- `target.path` names the exact destination (Q-STOR-8). If a folder already exists there, the
  source content is merged into it; it is never nested inside. Repeating a request therefore yields
  the same tree.
- A target item that already references the same blob as its source is left as it is and counted
  as `unchanged`; it is not a conflict, so a repeated copy succeeds without `overwrite`. A target
  item with different content is a conflict (`exists`) unless `overwrite` is true.
- A file whose target path is an existing folder, or a folder whose target path is an existing
  file, is a per-item conflict (`type_conflict`) that `overwrite` does not resolve: a folder is
  never replaced by a file or the other way round.
- Every target path MUST be admitted by the target repository's include and exclude patterns
  ([repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-005); a path they do not admit
  is reported per item (`not_admitted`).
- Sources: copy from local repositories and remote caches; move from local repositories only.
  Targets: a local repository, or a virtual repository with a deployment target, of the same
  format as the source or of the generic format. The target format plug-in may reject items that
  do not fit its layout (reported per item).
- A format may also refuse an item with its own per-item codes, whatever `overwrite` says. NuGet
  refuses a version that the target holds with other bytes (`nuget_version_exists`) and a version
  that is tombstoned there (`nuget_version_deleted`); a version the target holds with identical
  bytes counts as `unchanged` ([formats/nuget.md](formats/nuget.md) R-NUGET-005, R-NUGET-006).
- **Shadowing check.** An item may give a local target repository L, whose format supports claims
  (`pinsAndClaims`, R-REPO-003), its first version of a package name n outside L's claims. L is the
  deployment target when the target is a virtual repository. For each such item, the shadowing
  check of [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048 runs after the item's
  permission checks and before the operation's transaction, with the caller in the publisher's
  place. For a background job it runs before each batch transaction.
  - It runs once per (L, n) and operation, and its outcome applies to every item of n in the
    operation. Its probes hold no transaction open and store nothing.
  - A refused item is reported per item with `shadows_remote_package` or `shadow_check_unavailable`
    and left unchanged, like any other per-item error under `stopOnError`.
  - A caller holding `manage` on the coordinate in L, or `content:bypass`, proceeds as R-REPO-048's
    outcome table says. The audit event `repository.remote_package_shadowed` is then written in the
    operation's transaction.
  - Without this check, a `deploy` holder could publish a name into a repository that no virtual
    repository contains, where nothing is exposed, and then copy or move the package into a shared
    one.
- Permissions: copy needs `read` on each source item and `deploy` on each target path; move
  needs `delete` on each source item and `deploy` on each target path; replacing an existing
  target item needs `overwrite` (the replaced item goes to the trash can).
- Properties are copied when `copyProperties` is true (default); download statistics are not.
  Copied items get the caller and the current time as creator.
- Format plug-in hooks run for both repositories in the same transaction. Each increments the
  `rev_seq` of every package it changes: in the target repository for a copy, in both repositories
  for a move (R-STOR-003).
- A move out of a local npm or NuGet repository runs the source format's removal hook
  (`OnItemsRemoved`), which writes the version's tombstone in the source repository
  ([formats/npm.md](formats/npm.md) R-NPM-008, [formats/nuget.md](formats/nuget.md) R-NUGET-006).
  Clients of the source repository may have cached the version, so its number must not return
  there with other bytes.
- `dryRun: true` evaluates permissions, conflicts, format rules and the shadowing check, and
  changes nothing. It reports the same per-item codes as a real run would.
- Per-item codes: `exists`, `type_conflict`, `not_admitted`, `forbidden`, `shadows_remote_package`,
  `shadow_check_unavailable`, and the target format's own codes (NuGet: `nuget_version_exists`,
  `nuget_version_deleted`).
- Response `200`: `{"files": 12, "folders": 3, "unchanged": 0, "bytes": 81233920, "messages": [{"level": "error", "path": "tools/widget/2.3/a.bin", "code": "exists", "text": "target exists and overwrite is false"}]}`.
  Above `storage.delete.max_sync_items` items the operation runs as a background job (`202`).

Acceptance criteria:

- Copying a 2 GiB file between repositories completes in under one second on the reference test
  setup and issues no S3 request.
- A dry run of a move without `delete` on the source lists one `forbidden` message per item and
  changes nothing.
- Copying `tools/widget/2.3` from `generic-staging` to `generic-release` twice with
  `overwrite: false` succeeds both times; the second response reports every file as `unchanged`
  and no `error` message, and the target tree has no `2.3/2.3` folder.
- Set-up as in [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048:
  `npm-all` = [`npm-local`, `npm-remote`], the stub upstream of `npm-remote` serves `lodash`, and
  `npm-sandbox` is a member of no virtual repository. `ci-bot` holds `deploy` but not
  `manage`. It publishes `lodash@4.17.22` to `npm-sandbox` and copies it to `npm-local`. The item
  is reported with `shadows_remote_package` and `npm-local` holds no `lodash`. A dry run of the same
  copy reports the same code. The same copy by `lead`, who holds `manage`, succeeds and writes a
  `repository.remote_package_shadowed` event.
- Moving NuGet `Acme.Widgets` 1.2.6 from `nuget-local` to `nuget-release` leaves a tombstone in
  `nuget-local`. Moving it back, also with `overwrite: true`, reports `nuget_version_deleted`, and
  the version stays in `nuget-release`.
- After a copy of `@acme/ui@1.4.0` into `npm-release`, the next
  `npm view @acme/ui versions --registry …/api/npm/npm-release/` lists 1.4.0 on every replica.

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
  | `GET` | `/api/v1/repositories/{repoKey}/properties?path=&keys=a&keys=b` | — | `200 {"properties": {"stage": ["qa"]}}`; `{}` when none |
  | `PATCH` | `/api/v1/repositories/{repoKey}/properties?path=&recursive=false` | `application/merge-patch+json` ([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)): `{"stage": "qa", "targets": ["linux", "darwin"], "obsolete": null}` | `204`; a string or array replaces the key's values, `null` removes the key, absent keys stay |
  | `DELETE` | `/api/v1/repositories/{repoKey}/properties?path=&keys=a&keys=b&recursive=false` | — | `204` |

- `keys` is repeated once per key (`keys=qa.status&keys=build.number`), the multi-value rule of
  [api-and-ui.md](api-and-ui.md) R-API-011 as settled in R-API-026. A comma-separated list is not
  accepted: `,` cannot occur in a key, so `keys=a,b` answers `400`.

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
- With `stage` and `targets` set, `GET "…/properties?path=tools/widget.bin&keys=stage&keys=targets"`
  returns both keys, and `keys=stage,targets` answers `400`.
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

- Runtime settings (stored in PostgreSQL, changed through `PUT /api/v1/settings/storage`, cached
  per replica and invalidated through the change area `cache` of
  [architecture.md](architecture.md) R-ARCH-018: the PostgreSQL change log, with the Valkey message
  as the fast path, so a change applies on every replica within `cluster.epoch_poll_interval` even
  while Valkey is unavailable):

  ```json
  { "trash": { "enabled": true, "retention": "336h" } }
  ```

  `enabled: true` and `retention: 336h` (14 days) are the defaults (proposed).
- The storage settings are one document with a revision (`trash` here, `quota` in R-STOR-029).
  `GET /api/v1/settings/storage` returns `ETag: "<revision>"`; `PUT` replaces the whole document
  and requires `If-Match` ([api-and-ui.md](api-and-ui.md) R-API-012, Q-API-2): 428
  `precondition_required` without it, 412 `precondition_failed` when stale, `If-Match: *` for a
  deliberate blind write. Without the guard, a stale `PUT` that changes the trash retention would
  silently reset a quota another administrator had just set, and the reverse.
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
- Format tombstones ([formats/npm.md](formats/npm.md) R-NPM-008,
  [formats/nuget.md](formats/nuget.md) R-NUGET-006) are not trash entries. A format writes them in its removal hook, in the transaction that trashes or
  removes the version.
  - The purge, permanent removal and the manual purge of R-STOR-023 leave them in place, so a
    version number stays reserved after its content is gone.
  - Only a restore of the version (R-STOR-023) and the deletion of the whole repository remove
    one, and `trove admin npm reinstate` for a tombstone that a restore's journal replay wrote for
    a version that is still stored with its integrity ([formats/npm.md](formats/npm.md)
    R-NPM-047). None of them admits other bytes under the version number (R-NPM-008).
  - Tombstones do not appear in the trash-can listing.
- Without the trash can, every deletion, including policy deletions, is permanent.
- Visibility: a principal sees trashed items of an original path where it holds `deploy` or
  `manage` (evaluated as if the item were live).

Acceptance criteria:

- With defaults, `curl -X DELETE …/files/generic-local/a.bin` makes the file disappear from
  `/files` and appear in `GET /api/v1/trash-can/items?repository=generic-local`.
- After setting `retention` to `0s` (a `PUT` with the current `If-Match`) and running garbage
  collection, the entry is gone and its blob is queued.
- `PUT /api/v1/settings/storage` without `If-Match` answers 428 and changes nothing; with the
  `ETag` read before another administrator's `PUT` it answers 412 with the new tag as
  `currentEtag`.
- With defaults, uploading `a1.bin` and then `a2.bin` to `…/files/generic-local/a.bin` (the second
  upload with `overwrite`) serves `a2.bin`'s content, and the trash can lists one entry for the
  path whose restoration (with `overwrite: true`) serves `a1.bin`'s content again (Q-STOR-5).

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
- `OnItemsRestored` lets the format remove the restored version's tombstone in the restore
  transaction, because the original bytes come back ([formats/npm.md](formats/npm.md) R-NPM-008,
  [formats/nuget.md](formats/nuget.md) R-NUGET-006). Permanent removal and purge leave tombstones in place
  (R-STOR-022).
  - A restore with a different `target` repository leaves the original repository's tombstone in
    place, because the version does not return there.
  - The target format may refuse such an entry with its own codes, as for a copy (R-STOR-019).
- **Shadowing check.** An entry may give a local repository L, whose format supports claims
  (`pinsAndClaims`), its first version of a package name n outside L's claims. L is the original
  repository or the given `target`. For each such entry, the shadowing check of
  [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-048 runs after the permission
  checks and before the restore transaction, with the caller in the publisher's place.
  - A refused entry is reported in `messages` with `shadows_remote_package` or
    `shadow_check_unavailable`, counted as `skipped`, and stays in the trash can.
  - A caller holding `manage` on the coordinate in L, or `content:bypass`, proceeds as R-REPO-048
    says, with the audit event in the restore transaction.
  - Without this check, a caller could publish and delete a version in a repository that no
    virtual repository contains, and then restore it with `target` into a shared one where it
    holds only `deploy`.
- Restoring a trash operation restores exactly the items one delete call removed, so a folder
  comes back as it was.
- Denied permissions answer `403`.

Acceptance criteria:

- Deleting an npm version through the API and restoring its `operationId` makes
  `npm view @acme/widget versions` list the version again and `npm install @acme/widget@1.4.0`
  succeed.
- A restore into a path that is occupied, without `overwrite`, reports one `skipped` message and
  leaves both items unchanged.
- After a hard delete of NuGet `Acme.Widgets` 1.2.3 and `POST /api/v1/trash-can/purge`, a push of
  1.2.3 still answers 409 `nuget_version_deleted`. Restoring a deleted 1.2.4 before the purge
  removes its tombstone and serves the original `.nupkg`.
- Set-up as in the shadowing criterion of R-STOR-019. `ci-bot` holds `deploy` and `delete` on `npm-sandbox` and only `deploy`
  on `npm-local`. It publishes `lodash@4.17.22` to `npm-sandbox`, deletes it, and restores the
  entry with `target` `npm-local`. The response counts the entry as `skipped` with
  `shadows_remote_package`, the entry stays in the trash can, and `npm-local` holds no `lodash`.

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

Every candidate query reads the digest's `blob_gc_queue.enqueued_at` along with it, as `$seen`:
the incremental query from the queue row, the full sweep through a left join (NULL when the
digest has no entry). Step (3) uses it to keep a release that commits during the run.

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
-- (2b) only when (2) returned false; a separate statement started after (2):
--      the hold of a running backup (backup-and-restore.md R-BAK-007)
SELECT EXISTS (SELECT 1 FROM backup_run r
                WHERE r.hold = 'all' AND r.lease_expires_at > now())
    OR EXISTS (SELECT 1 FROM backup_blob_pin p JOIN backup_run r ON r.id = p.run_id
                WHERE p.sha256 = $1 AND r.hold = 'pinned' AND r.lease_expires_at > now());
-- (3) only when (1) returned state = 'live', (2) returned false and (2b) returned false
--     now() is the transaction's start: the claim time of the hand-over rules below
UPDATE blobs SET state = 'deleting', state_changed_at = now() WHERE sha256 = $1;
--     also when (2) found a reference or (1) found no 'live' row; not when (2b) held it;
--     only the entry the candidate query read, never one a later release wrote
DELETE FROM blob_gc_queue WHERE sha256 = $1 AND enqueued_at = $seen;
COMMIT;
-- (4) outside any transaction; 204 and 404 both count as success
S3 DeleteObject <prefix>/sha256/…
-- (5)
UPDATE blobs SET object_deleted_at = now() WHERE sha256 = $1 AND state = 'deleting';
-- (6) later, once object_deleted_at < now() - storage.gc.delete_settle
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

The hold check (2b) only ever keeps a blob, so it cannot weaken 1–4. It gives a running backup
liveness: a blob that the backup's snapshot references stays in the bucket until the backup has
copied it. [backup-and-restore.md](backup-and-restore.md) R-BAK-007 proves this and needs from
this protocol only that (2b) starts after (2) of the same blob. A held blob stays `live`, its
queue entry stays, `trove_gc_candidates_held_total` grows, and the next run considers it again.
A run that finds a valid `all` hold when it reaches its candidates skips them and ends; the
per-blob check still guards the race, and the early exit only saves the locking.

**No release is lost from the queue.** The queue row is deleted only while its `enqueued_at`
still equals `$seen`. Take a digest whose reference check (2) found a reference R. A transaction
that removes R and commits during the collector's transaction moves the queue row's
`enqueued_at` forward through the release trigger. Without the condition, the collector's
`DELETE … WHERE sha256 = $1` would, under READ COMMITTED, either see the updated row (the release
committed before the statement began) or wait for the releasing transaction's row lock and
re-evaluate its condition against the updated row; either way it would match and delete the
entry. The blob would then be unreferenced with no queue entry, and only the next full sweep
(R-STOR-025) would reclaim it. With the condition, the updated row does not match, the entry
stays, and the next incremental run handles the digest. A NULL `$seen` deletes nothing, so an
entry written after a full-sweep candidate was read also stays. `$seen` is passed back exactly as
the candidate query returned it, never recomputed. The condition decides only how soon a blob is
reclaimed: rules 1–4 above do not rely on the queue, so safety is the same with or without it.

**Recovery and hand-over between runs.** Every step is idempotent. A collector that crashes after
(3) leaves a `deleting` row; a later run repeats (4)–(6). Rows in `deleting` are processed on every
run before new candidates, and before a run ends it waits up to `delete_settle` to finish (6) for
the rows it deleted and (4)–(6) for handed-over rows that became eligible meanwhile, so an upload
waiting for such a row (R-STOR-005) is not left until the next scheduled run. With several
`worker` replicas and node drains at any time (INV-12), a collection run regularly moves from one
worker to another (R-ARCH-016), so the hand-over has rules of its own:

- A collector reads its own monotonic clock before it begins a claim transaction; that reading
  is no later than the transaction's start, which (3) stores as `state_changed_at`. It sends (4)
  for a row it claimed itself only while less than `storage.gc.handover_delay −
  storage.gc.delete_settle` has passed on that clock since the reading, checks this immediately
  before the call, and gives the call that instant as its deadline. In batch mode the earliest
  reading of the batch counts. A row it did not delete in time is left to a later run. The
  collector never compares its clock with the database's, and a lock wait inside the claim
  transaction only shortens its window.
- A run finishes a `deleting` row without `object_deleted_at` that it did not claim itself only
  after the row's `state_changed_at` is older than `handover_delay`.
- On shutdown, the collection handler cancels its S3 calls and waits for them to return before its
  job is released ([architecture.md](architecture.md) R-ARCH-016), so it sends nothing after the
  release. In the same step it records (5) for the rows whose (4) succeeded and hands its other
  claims over: one statement sets
  `state_changed_at = now() − (handover_delay − delete_settle)` on them, so the run that
  continues the job finishes them after `delete_settle` instead of `handover_delay`. A drain therefore delays the re-upload of a digest that was being collected
  by seconds, not minutes. If that statement fails, the rows wait for `handover_delay`, as after a
  crash.

Without these rules, a collector that was drained, paused or partitioned after (3) could send its
delete late. By then another run could have finished the row, and an uploader could have written
the same digest again for a new `live` row (rule 3). The late delete would remove that new object,
and a committed reference would point at nothing. Under the rules, the new row can exist only
after `handover_delay`, or `delete_settle` after a release that followed the return of all the
collector's calls; by then the first collector's delete has either completed or will not be sent,
and `delete_settle` still absorbs a request already in flight. The remaining gap is a
process frozen between its deadline check and the write of the request itself, which the scrub
detects (R-STOR-027). An upload that meets such a row waits `storage.upload.blob_wait_timeout`,
then answers `503` and is retried by its client (R-STOR-005). After a drain the wait is short,
because the drained collector handed its rows over. After a crash, uploads of the digests the lost
collector had claimed can answer `503` for up to `handover_delay` plus one `gc` interval; this
touches only content that was unreferenced for the whole grace period and is uploaded again in
that window.

**Grace period.** The protocol is correct without it; the grace period exists for three other
reasons: (a) content deleted and re-published shortly after (common in CI) is still stored, so
the re-upload is a deduplication hit; (b) a point-in-time restore of the platform's database
backup ([ha-and-helm.md](ha-and-helm.md) R-HA-023 to R-HA-025) to any moment within the last
`grace_period` finds every blob it references still in the bucket, because a blob is deleted no
earlier than `grace_period` after its last reference disappeared (longer restore windows need
bucket versioning with noncurrent-version expiry at least that long, R-HA-024); (c) defense in
depth. trove's own backups do not depend on it: the hold (2b) keeps every blob a running backup
still has to copy, whatever the grace period is, and a completed backup holds its blobs in its own
target ([backup-and-restore.md](backup-and-restore.md) R-BAK-006, R-BAK-007).

**Deletion pause.** Before every batch of state changes (trash purge, (3), (4) and (6)) a run reads
`system_state.deletions_paused_at` ([ha-and-helm.md](ha-and-helm.md) R-HA-025). While it is set,
the run changes nothing and ends with `result.paused: true`; the next run after an operator
resumes deletions continues.

**Deletion batching.** The collector MAY claim up to `storage.gc.batch_size` blobs, one
transaction each or one transaction for the batch with the locks taken in digest order, and
delete their objects with `DeleteObjects` (up to 1,000 keys per request) when
`storage.gc.batch_delete` is true. In batch mode one hold-check statement covers the batch
(`p.sha256 = ANY($1)`, returning the held digests), issued after the batch's last reference check
(R-BAK-007), and one queue deletion pairs each digest with its own `$seen`
(`DELETE FROM blob_gc_queue q USING unnest($1::bytea[], $2::timestamptz[]) AS s(sha256, seen)
WHERE q.sha256 = s.sha256 AND q.enqueued_at = s.seen`), leaving out the held digests.

**Relation to other jobs.** Retention runs, deletes, copies and backup runs may run concurrently
with collection; the protocol needs no mutual exclusion beyond the row locks and the hold check.
Two collection runs that overlap, for example while a lost worker's run is still finishing, are
safe for the same reason; the exclusive job key of R-STOR-025 only avoids the duplicate work, and
no advisory-lock lease or Valkey lock is involved (Q-STOR-13). Every statement runs in a short
transaction on the main pool, so collection works through a transaction-mode pooler. The
physical-usage delta of a blob is recorded when its row leaves `live` in step (3) (R-STOR-003).

Configuration:

```yaml
storage:
  gc:
    grace_period: 24h        # default (proposed)
    delete_settle: 30s       # default (proposed); at least 1 s
    handover_delay: 10m      # default (proposed); must exceed jobs.lease_duration + 2 × delete_settle
    batch_size: 1000         # default (proposed)
    batch_delete: true       # default (proposed); false for stores without DeleteObjects
    delete_concurrency: 8    # default (proposed)
```

Metrics (the `trove_gc_*` family of
[operations-and-observability.md](operations-and-observability.md) R-OPS-008):
`trove_gc_deleted_blobs_total`, `trove_gc_reclaimed_bytes_total`,
`trove_gc_candidates_kept_total`, `trove_gc_candidates_held_total` (kept by a backup hold,
R-BAK-007), `trove_gc_deleting_rows`.

Acceptance criteria:

- The race test of R-STOR-005 passes, including a variant in which the collector process is
  killed at random points.
- A deleted item's blob still exists 23 hours later and is gone after the first collection run
  past 24 hours (with defaults and the trash can disabled).
- A test that inserts a reference row with `blob_sha256` of a `deleting` blob through plain SQL
  fails with SQLSTATE `AS001`.
- A test migration adds a new referencing table while an old binary runs collection; a blob
  referenced only from the new table is kept.
- The hold criteria of [backup-and-restore.md](backup-and-restore.md) R-BAK-007 pass: with a
  backup paused after its pins are committed, a pinned, unreferenced blob stays `live` through a
  collection run with `grace_period: 0s`, and the first run after the backup deletes it.
- A fault-injection build pauses a collector right after its reference check (2) found an item
  that references the digest. A second transaction deletes that item and commits, and the
  collector then continues. Afterwards `blob_gc_queue` holds the digest with the second
  transaction's `enqueued_at`, and the next incremental run with `grace_period: 0s` deletes the
  blob without a full sweep.
- A fault-injection build pauses a collector between (3) and (4) with `kill -STOP` for longer than
  `handover_delay`, while another worker takes over the run and an uploader re-publishes the same
  content. After `kill -CONT`, the paused collector sends no `DeleteObject` (S3 request log), and
  the re-published item downloads with the right SHA-256.
- A fault-injection build holds a collector's `DeleteObjects` call after (3), and its pod is
  deleted gracefully. The pod sends no `DeleteObject` after its job is released; the run continues
  on the other `worker` replica and removes the claimed rows within `2 × delete_settle` plus one
  job pick-up, and an upload of one of those digests started during the drain answers `201`
  within `storage.upload.blob_wait_timeout`.
- With `system_state.deletions_paused_at` set, a collection run with `grace_period: 0s` changes no
  row and deletes no object, and its result carries `paused: true`.

### R-STOR-025 · Garbage-collection schedule, full sweep and manual run
Priority: **MVP** · Covers: STOR-037

Collection runs on the shared task framework (Q-STOR-10): the job table, scheduler and job leases
(`locked_until`) of [architecture.md](architecture.md) R-ARCH-016, the task and job API of
[operations-and-observability.md](operations-and-observability.md) R-OPS-019 and R-OPS-020. This
requirement fixes the two collection tasks, their parameters and results.

- Two tasks: `gc` (incremental) and `gc-full` (full sweep). Both are registered in the queue
  `maintenance` with the dedup key `gc` in `exclusive` mode, so at most one collection run is
  queued or running at any time, whichever task started it, on whichever `worker` replica claims
  it (R-STOR-046). The exclusion lives in the job table in PostgreSQL (INV-3); it avoids duplicate
  work, while correctness rests on the row locks of R-STOR-024 (Q-STOR-13).
- Each run first purges expired trash entries (R-STOR-022), then aborts expired upload sessions
  (R-STOR-011, from P1 on), then finishes `deleting` rows under the hand-over rules of R-STOR-024,
  then processes candidates. The deletion pause of R-STOR-024 stops every step; the backup hold
  (R-STOR-024 step 2b) applies to the blob candidates, the only step that can remove content a
  running backup still has to copy.
- A node drain releases a running collection job ([architecture.md](architecture.md) R-ARCH-016):
  `gc-full` continues from its keyset checkpoint on another worker; `gc` needs no checkpoint,
  because its candidates are the remaining `blob_gc_queue` entries.
- A scheduled slot that finds the other collection task queued or running is refused by the shared
  key, not dropped: the scheduler offers a task's latest due slot again on every tick
  ([architecture.md](architecture.md) R-ARCH-016), so it is enqueued once the blocking run has
  ended. A full sweep whose slot collides with an incremental run therefore starts after that run.
  Incremental slots that fall inside a full sweep collapse into the first slot after it, which loses
  nothing because the sweep deletes everything those runs would have deleted.
- The full sweep checkpoints its keyset position into the job's `checkpoint`; a run that a lost
  replica returns to the queue continues where it stopped.
- Trigger and observation (permission: system administrator for `POST`, `system.read` for `GET`):

  | Method | Path | Body | Result |
  |---|---|---|---|
  | `POST` | `/api/v1/system/tasks/gc/run`, `/api/v1/system/tasks/gc-full/run` | `{"dryRun": false}` | `202 {"job": {…}}`; `409 {"job": {…}}` with the active job while any collection run is queued or running |
  | `GET` | `/api/v1/system/jobs?kind=gc`, `?kind=gc-full` | `state`, `cursor`, `limit` | runs; each job's `result` carries `dryRun`, `trashPurged`, `candidates`, `kept`, `deleted`, `bytesReclaimed`, `errors` |
  | `GET` | `/api/v1/system/jobs/{id}` | — | one run |
  | `POST` | `/api/v1/system/jobs/{id}/cancel` | — | `202`; the run stops after the current batch |
  | `GET` | `/api/v1/system/tasks` | — | `schedule`, `enabled` and `nextRunAt` of both tasks |

- A dry run reports what would be deleted and changes nothing, the trash purge included.

**Schedule syntax** (Q-STOR-4). Every schedule in trove uses this dialect and one parser:
maintenance tasks (`maintenance.tasks.<name>`), retention policies (R-STOR-040) and any later
schedule.

- Five fields separated by spaces: minute (0–59), hour (0–23), day of month (1–31), month (1–12
  or `JAN`–`DEC`), day of week (0–6 with 0 = Sunday, or `SUN`–`SAT`). Names are case-insensitive.
- A field is `*`, a value, an ascending range `a-b`, a step `*/n` or `a-b/n`, or a comma-separated
  list of these.
- Day matching follows [POSIX `crontab`](https://pubs.opengroup.org/onlinepubs/9799919799/utilities/crontab.html):
  when both day of month and day of week are restricted (neither field starts with `*`), a day
  matches if either field matches; otherwise both must match.
- Macros: `@yearly` (also `@annually`), `@monthly`, `@weekly`, `@daily` (also `@midnight`) and
  `@hourly`, with the meanings of
  [Kubernetes CronJob schedules](https://kubernetes.io/docs/concepts/workloads/controllers/cron-jobs/#schedule-syntax).
- Evaluated in UTC. There is no time-zone setting and no `TZ=` or `CRON_TZ=` prefix.
- Rejected (`422` `invalid_schedule` through the API; a configuration error naming the key at
  startup): six- or seven-field expressions (seconds or year field), `?`, `L`, `W`, `#`, the
  day-of-week value `7`, descending ranges, `@every` and `@reboot`, and an expression that does
  not fire within four years of the time it is saved (`0 0 30 2 *`). The error text states the
  dialect, including "day of week 0–6, Sunday = 0".
- Rejecting `?`, `7` and six-field forms is deliberate: a Quartz-style expression, whose day of
  week counts from 1 = Sunday, fails loudly instead of running on the wrong day.

Configuration:

```yaml
maintenance:
  tasks:
    gc:      {enabled: true, schedule: "*/30 * * * *"}   # default (proposed): incremental every 30 minutes
    gc-full: {enabled: true, schedule: "0 2 * * 0"}      # default (proposed): full sweep Sundays 02:00 UTC
```

Acceptance criteria:

- `curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"dryRun":false}' …/api/v1/system/tasks/gc-full/run`
  answers `202`; `POST …/api/v1/system/tasks/gc/run` while it runs answers `409` with the same job
  ID.
- With two replicas, each scheduled slot of `gc` appears exactly once in
  `GET /api/v1/system/jobs?kind=gc` over a 24-hour window without a `gc-full` run, and no slot
  ever appears twice.
- With both default schedules, which both fire on Sundays at 02:00 UTC, a `gc-full` job for that
  slot runs every Sunday, and no `gc` job and `gc-full` job ever run at the same time.
- `kubectl drain` of the node whose `worker` runs a `gc-full` job: the job continues on the other
  `worker` replica from its checkpoint with `attempt` unchanged, and afterwards exactly the blobs
  are gone that an undisturbed run deletes over the same fixture.
- Parser table test: `0 3 * * 1` and `0 3 * * mon` fire on Mondays at 03:00 UTC; `0 0 1 * 1` fires
  on the first of each month and on every Monday; `0 3 * * 7`, `0 30 3 * * ?`, `30 3 ? * 2`,
  `@every 1h`, `CRON_TZ=Europe/Berlin 0 3 * * *` and `0 0 30 2 *` are rejected.

### R-STOR-026 · Orphan sweep
Priority: **P1** · Covers: STOR-038, OPS-067

The reverse check: walk the bucket and remove objects no blob row knows.

- The sweep lists `<prefix>/sha256/<h0h1>/` prefix by prefix (`ListObjectsV2`, 256 top-level
  prefixes) and checkpoints the next prefix into the job's `checkpoint` after each one.
- For each object without a blob row and with `LastModified` older than the run's `minAge`
  (default `storage.orphans.min_age`), the sweep MUST claim the digest by inserting a `deleting` row
  (`INSERT … ON CONFLICT DO NOTHING`) before deleting the object, then follow steps (4)–(6) of
  R-STOR-024 with its hand-over rules. An uploader that wrote the same object concurrently finds
  the `deleting` row and writes again after it is gone (R-STOR-005); the commit deadline keeps
  freshly written objects out of reach of `min_age`. The sweep needs no backup-hold check: it
  deletes only objects without a blob row, and a backup's snapshot references none of them
  ([backup-and-restore.md](backup-and-restore.md) R-BAK-007).
- The sweep honours the deletion pause of R-STOR-024 before every prefix: while it is set, the
  sweep deletes nothing and ends with `result.paused: true`, and a later run resumes at the
  checkpoint.
- Keys below `<prefix>/sha256/` that do not match the layout, and any other foreign key below
  `<prefix>/`, are reported and never deleted.
- The sweep also aborts multipart uploads below `<prefix>/` initiated longer than
  `storage.orphans.multipart_max_age` ago, and deletes the staging objects of upload sessions
  that are aborted, done or unknown (R-STOR-011). An open session's multipart upload is never
  that old, because `storage.uploads.session_max_age` is shorter than `multipart_max_age`.
- The sweep is the task `orphan-sweep` of the shared task framework (Q-STOR-10; R-OPS-019).
  Trigger: `POST /api/v1/system/tasks/orphan-sweep/run` with
  `{"dryRun": false, "resume": true, "minAge": "24h"}` (all members optional; the values shown are
  the defaults, `minAge` defaulting to `storage.orphans.min_age`) → `202 {"job": {…}}`, `409` with
  the active job. Observation and stop through `/api/v1/system/jobs/{id}` and
  `/api/v1/system/jobs/{id}/cancel`. The job `result` carries `prefixesDone`, `prefixesTotal`,
  `objectsSeen`, `orphansFound`, `orphansDeleted`, `bytesReclaimed`, `multipartAborted` and
  `foreignKeys`. Permission: system administrator.
- `resume: true` starts at the checkpoint of the most recent `orphan-sweep` job that ended
  `cancelled` or `failed`, if it is newer than the most recent completed one; otherwise the sweep
  starts at prefix `00`. Scheduled runs use `resume: true`.
- A `minAge` not greater than twice `storage.upload.commit_deadline` is rejected with `422`: a
  shorter age could reach objects whose uploader has not yet inserted their row (R-STOR-005).

Configuration:

```yaml
storage:
  orphans:
    min_age: 24h              # default (proposed); must be > 2 × storage.upload.commit_deadline
    multipart_max_age: 24h    # default (proposed)
maintenance:
  tasks:
    orphan-sweep: {enabled: true, schedule: "0 5 * * 6"}   # default (proposed): Saturdays 05:00 UTC
```

Acceptance criteria:

- `aws s3 cp junk.bin s3://trove-blobs/trove/sha256/00/00/<64 hex>` with a back-dated test
  clock is removed by a sweep, reported by a dry run before that, and an object with a
  non-hex name under `sha256/` is only reported.
- A sweep cancelled at prefix `7f` and triggered again with `resume: true` continues at `7f`
  without re-listing earlier prefixes.
- `POST …/tasks/orphan-sweep/run` with `{"minAge": "1h"}` and the default `commit_deadline` of 1h
  answers `422`.

### R-STOR-027 · Integrity scrub
Priority: **P1** · Covers: trove-only

- **Existence check**: every `live` blob MUST be checked with `HeadObject` at least once per
  `storage.scrub.check_interval` (oldest `checked_at` first). `404` sets `content_status = 'missing'`;
  a size different from `blobs.size` sets `corrupt`. Errors other than `404` (timeouts, `5xx`,
  `403`) MUST NOT change the status; they are counted and retried.
- **Deep check**: every `live` blob SHOULD be downloaded and re-hashed at least once per
  `storage.scrub.deep_interval`, throttled to `storage.scrub.deep_bytes_per_second` per run (the
  task is `exclusive`, so one run reads at a time whatever the number of `worker` replicas); a
  SHA-256 mismatch sets `corrupt`.
- Every detection (here and in R-STOR-013) increments the counter
  `trove_storage_blobs_damaged_total{status}` and writes an audit event per blob; the gauge
  `trove_storage_blobs_damaged{status}` reports the current number of damaged blobs from the
  latest storage summary (R-STOR-028).
- `GET /api/v1/storage/damaged-blobs` lists damaged blobs with their referencing items
  (permission: `storage.read` or system administrator).
- **Repair**: a later upload of identical content rewrites the object and resets the status
  (R-STOR-005); a remote repository treats an item with damaged content as a cache miss and
  refetches it; `POST /api/v1/storage/damaged-blobs/{sha256}/recheck` re-runs both checks
  (permission: system administrator).
- The scrub is the task `scrub` of the shared task framework (Q-STOR-10; R-OPS-019). A scheduled
  run performs the existence checks and the deep checks that are due. A manual trigger
  `POST /api/v1/system/tasks/scrub/run` with `{"deep": false}` performs only the due existence
  checks; `{"deep": true}` also the due deep checks. Observation and stop through the job API.
  Permission: system administrator.

Configuration:

```yaml
storage:
  scrub:
    check_interval: 168h           # default (proposed): every blob HEAD-checked weekly
    deep_interval: 2160h           # default (proposed): every blob re-hashed every 90 days
    deep_bytes_per_second: 50MiB   # default (proposed)
maintenance:
  tasks:
    scrub: {enabled: true, schedule: "0 4 * * *"}   # default (proposed): daily 04:00 UTC
```

Acceptance criteria:

- `aws s3 rm` of one blob object followed by a scrub run marks it `missing`; the damaged-blobs
  API lists the items referencing it; re-uploading the same file with `curl -T` resets it to
  `ok`.
- An S3 outage during a scrub run leaves every status unchanged.

## Capacity

### R-STOR-028 · Storage summary
Priority: **P1** · Covers: STOR-039

- The `storage-summary` task, its schedule, the snapshot table and the response envelope
  (`computedAt`, `durationMs`) are owned by
  [operations-and-observability.md](operations-and-observability.md) R-OPS-023. This requirement
  owns the figures and the read path (Q-STOR-10):
  - `GET /api/v1/storage/summary` returns the latest snapshot (permission `storage.read`; `404`
    before the first computation). Reads never compute.
  - A recomputation is started only through `POST /api/v1/system/tasks/storage-summary/run`
    (R-OPS-019; system administrator; `202` with the job, `409` with the running job). There is
    no second refresh path ([api-and-ui.md](api-and-ui.md) R-API-020: one path per function).
- The task reads the folded counters of R-STOR-003 and the partial indexes on `blobs` instead of
  scanning items.
- Figures:

  ```json
  {
    "computedAt": "2026-10-28T09:17:00.412Z",
    "durationMs": 412,
    "blobs": { "count": 412093, "bytes": 1893341184512 },
    "logical": { "files": 1203381, "folders": 98112, "bytes": 4120938391040 },
    "deduplicationRatio": 0.4594,
    "trash": { "items": 3210, "bytes": 9123012345 },
    "pendingCollection": { "blobs": 812, "bytes": 1029381233 },
    "damaged": { "missing": 0, "corrupt": 0 },
    "quota": { "enabled": true, "capacityBytes": 5497558138880, "usedRatio": 0.3444, "warnPercent": 80, "limitPercent": 95 },
    "repositories": [
      { "key": "npm-local", "class": "local", "format": "npm", "files": 52011, "folders": 8812, "bytes": 81233920123, "packages": 812, "versions": 9120, "shareOfLogicalBytes": 0.0197 }
    ]
  }
  ```

  *(example values)*

  | Member | Definition |
  |---|---|
  | `blobs` | `live` blob rows: the physical content, including content referenced only from the trash can and content awaiting collection. |
  | `logical` | Untrashed file items and folders of all local and remote repositories; every reference counts, so one blob in two repositories counts twice. |
  | `deduplicationRatio` | `blobs.bytes / logical.bytes`; `0` when `logical.bytes` is 0. |
  | `trash` | Trashed items and their logical bytes (R-STOR-022). |
  | `pendingCollection` | Digests in `blob_gc_queue` plus rows in state `deleting`, with their sizes. |
  | `damaged` | Blob counts per `content_status` other than `ok` (R-STOR-027). |
  | `quota` | The system quota settings of R-STOR-029 and `usedRatio = blobs.bytes / capacityBytes`; `capacityBytes` and `usedRatio` are `null` when no capacity is configured. |
  | `repositories[]` | One entry per local and remote repository; virtual repositories hold no items and are not listed. `packages` and `versions` count untrashed rows. |

- **Per-repository share (STOR-039).** `shareOfLogicalBytes` is the repository's logical bytes
  divided by `logical.bytes` of the whole installation, a ratio from 0 to 1 rounded to four
  decimals (`0` when the total is 0). It is never a share of capacity: object storage reports no
  capacity, and the configured capacity measures physical bytes, which deduplication makes
  non-additive across repositories. The shares of all listed repositories sum to 1 within
  rounding.
- Sizes are integers in bytes and ratios are numbers, never formatted strings (R-OPS-023).
- Spool usage is per replica and exported only as metrics (R-STOR-004).

Configuration: the schedule is `maintenance.tasks.storage-summary` of R-OPS-019
(`17 * * * *`, hourly, default proposed there).

Acceptance criteria:

- After uploading the same 10 MiB file to two repositories and refreshing, `logical.bytes` grows by
  20 MiB and `blobs.bytes` by 10 MiB.
- In an installation whose only content is in `npm-local`, that entry's `shareOfLogicalBytes` is
  `1`; after copying all of it to `generic-local`, both entries show `0.5` while `blobs.bytes` is
  unchanged.

### R-STOR-029 · Quotas and capacity thresholds
Priority: **P1** · Covers: STOR-040, OPS-066

An object store does not report free space through the S3 API, so capacity is configured, not
measured.

- System settings (runtime, `PUT /api/v1/settings/storage` with `If-Match`; one revisioned
  document with the trash settings, R-STOR-022):

  ```json
  { "quota": { "enabled": false, "capacityBytes": null, "warnPercent": 80, "limitPercent": 95 } }
  ```

  Defaults (proposed) as shown. Used capacity is physical bytes (all `live` blobs, including
  trash and content awaiting collection).
- Per repository: `config.storage.quota` (bytes of untrashed logical content; default none).
- Above `warnPercent`: warning log, metric `trove_storage_quota_used_ratio`, an event for
  webhooks ([operations-and-observability.md](operations-and-observability.md)) and a banner in
  the UI. At or above `limitPercent`, or when a repository quota would be exceeded: every upload
  is rejected with `507 Insufficient Storage` and problem code `quota_exceeded` (type
  `/api/v1/problems/quota_exceeded`, [api-and-ui.md](api-and-ui.md) R-API-009; Q-STOR-7); deploy
  by checksum and copy into a repository also count against its repository quota.
- Status codes (Q-STOR-7): `507` for the system capacity limit and for repository quotas; `413`
  only for the per-request size limit (R-STOR-004). The rejection happens at admission, before
  the body is read, whenever the request declares its length.
- A `507` carries no `Retry-After` (nothing predicts when space is freed). The problem details
  name the limit: `quota` (`"system"` or the repository key), `limitBytes` and `usedBytes`. On npm
  endpoints the body follows R-NPM-005 of [formats/npm.md](formats/npm.md) with
  `code: "quota_exceeded"` and a text that names the repository or the system limit; NuGet clients
  show the status line `507 (Insufficient Storage)`.
- Clients retry a `5xx`: npm twice by default, after 10 s and 60 s, so `npm publish` reports a
  quota failure after about 70 s; NuGet with exponential backoff over a few seconds. Every retry is
  rejected again at admission. This delay is the accepted cost of naming the cause in the status
  code (Q-STOR-7).
- Enforcement reads the folded counters of R-STOR-003, so it lags by at most one fold interval
  plus one fold iteration (`storage.usage.fold_interval`, 5 s by default); uploads committed in
  that time and uploads in flight may exceed a quota. The counters live in PostgreSQL only, never
  in Valkey or in a replica's memory, so every replica decides alike, whichever one a request
  reaches, and a Valkey failover cannot reset a quota (Q-STOR-12). One admission costs one
  primary-key read per limit that is enabled, whatever the number of replicas and the upload
  rate.
- Deletes, moves within a repository, property changes and downloads are never blocked by a
  quota.
- The spool volume is the only replica-local disk on the upload path; its thresholds are checked
  when an upload is admitted (R-STOR-004).

Acceptance criteria:

- With `capacityBytes` set to the current physical size and `limitPercent: 95`,
  `npm publish` fails with a `507` error whose printed text names the system limit; deleting
  content and running collection lets it succeed again.
- With a repository quota exceeded, `dotnet nuget push` fails with
  `507 (Insufficient Storage)` and stores nothing.
- A repository quota of 100 MiB rejects the upload that would cross it, once one fold interval
  has passed since the uploads that filled it, and accepts uploads to other repositories.

## Object-store configuration

### R-STOR-030 · Object-store connection and bucket settings
Priority: **MVP** · Covers: STOR-006, STOR-020, STOR-029

**TLS (R-ARCH-035, INV-20).** An `https://` endpoint is the production form. The object store's CA trust is part of
the TLS material of R-ARCH-035: `storage.s3.ca_file` is supported since M0 and replaces the system roots for the
object-store client, and the file is reloaded like the other bundles (Q-STOR-14). A proxy credential file is still refused. An
`http://` endpoint is a development exception behind `storage.s3.allow_insecure_endpoint: true`, and the development
and integration profiles use TLS, so no profile needs the exception. Where the endpoint rules below name plain HTTP as
a permitted form, this paragraph governs.

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
  `https://` URL (`http://` only with `allow_insecure_endpoint: true`). An endpoint without a scheme
  is a host, with an optional port, and means `https://` (Q-STOR-15): `s3.eu01.example.cloud` is read
  as `https://s3.eu01.example.cloud`, as a bucket provisioner writes it. The effective configuration
  shows the URL with the scheme added, and its source is `derived`. The same holds for
  `presign.endpoint`. A scheme-less host never becomes `http://`, not even with
  `allow_insecure_endpoint`; plaintext is always written out.
- S3-compatible stores: `force_path_style: true` for stores without virtual-hosted addressing;
  explicit `region` (any value the store accepts); `checksums.request_calculation: when_required`
  and `checksums.response_validation: when_required` for stores that reject the SDK's default
  payload checksums (SDK options `RequestChecksumCalculation` and `ResponseChecksumValidation`,
  [aws package](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/aws)); `ca_file` for private CAs.
- Every key can be overridden by environment variables per INV-15, for example
  `TROVE_STORAGE_S3_BUCKET`.

Configuration:

```yaml
storage:
  s3:
    endpoint: ""                 # default (proposed): empty = AWS endpoint resolution for region; a host without a scheme means https://
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

- The CI matrix runs the storage integration suite against SeaweedFS on every change and against
  MinIO (pinned source build, on every change since the owner's decision of 2026-10-29) and, once an AWS account exists, AWS S3, with the documented settings
  for each ([ha-and-helm.md](ha-and-helm.md) R-HA-051, Q-HA-3). Ceph RGW and other stores are
  qualified by the conformance check of R-STOR-035, not by a CI job.
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
- Retries: the SDK's standard retryer with `retry.max_attempts` and exponential backoff capped at
  `retry.max_backoff`, for throttling, `5xx` and network errors on idempotent operations (all
  operations trove uses are idempotent on content-addressed keys). Streaming `GetObject` bodies
  are not retried after the first byte reached the client.
- The pool and `storage.s3.multipart.max_concurrent_parts` protect one replica. The store sees
  their sum over all replicas, which grows with the replica count; sizing the store for it is
  [ha-and-helm.md](ha-and-helm.md) R-HA-050's.
- Circuit breaker per replica: after `circuit_breaker.failure_threshold` consecutive failures
  (network errors, timeouts, `5xx`, credential errors; not `404` or other `4xx`) the circuit opens
  for `circuit_breaker.open_duration`; blob operations then fail fast (R-STOR-036); one probe
  request closes it again.
- Metrics: `trove_storage_s3_requests_total{operation,outcome}`,
  `trove_storage_s3_request_duration_seconds{operation}`, `trove_storage_s3_bytes_total{direction}`,
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
- **Startup check** (`storage.s3.startup_check: true`): write, read back, compare and delete
  `<prefix>/_probe/<replica-id>`, then list `<prefix>/sha256/` with one key. A replica does not
  become ready before the check has passed (R-STOR-036, Q-STOR-1). Configuration and permission
  errors (`AccessDenied`, `NoSuchBucket`, invalid region) stop the process with a clear message.
  Transient errors (timeouts, connection errors, `5xx`, throttling) are retried with exponential
  backoff capped at 30 s for as long as they last; the replica stays not ready, MUST NOT exit, and
  logs the failing step once per minute. Exiting would turn a store outage into a crash loop whose
  restart back-off (up to five minutes in Kubernetes) delays readiness after the store returns;
  [ha-and-helm.md](ha-and-helm.md) R-HA-008 and R-HA-034 treat a wait for the schema the same way.
- **Conformance check**: `trove admin storage check` runs the startup check plus multipart
  upload and abort, ranged read, `DeleteObjects`, presigning, read-after-delete and
  list-after-write, and reports each result. It changes nothing outside `_probe/`.

Configuration:

```yaml
storage:
  s3:
    startup_check: true      # default (proposed)
```

Acceptance criteria:

- `trove admin storage check` passes against SeaweedFS (local profile, every change) and against
  MinIO (every change) and AWS S3 (once an AWS account exists); against a bucket without `s3:DeleteObject` it reports exactly that
  failure.
- A replica started with a wrong bucket name exits with a non-zero status and a log line naming
  the bucket.
- A replica started while the local profile's S3 store is stopped stays not ready for ten minutes
  with an unchanged container restart count, and becomes ready within 60 s after the store is
  started again.

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
| Readiness | Object storage gates only a replica's first readiness (Q-STOR-1, INV-12 as revised): a replica becomes ready only after the startup check of R-STOR-035 passed, so a rollout with a broken S3 configuration stalls. Afterwards an outage leaves running replicas ready ([operations-and-observability.md](operations-and-observability.md) R-OPS-001, `health.require_object_store: false`): `/readyz` answers `200` with `"degraded": ["objectStore"]`, the circuit breaker fails blob operations per request (R-STOR-033), and `trove_storage_s3_circuit_open` feeds the shared-outage and single-replica alerts ([ha-and-helm.md](ha-and-helm.md) R-HA-045). With the opt-in `health.require_object_store: true`, R-OPS-001's `objectStore` check gates readiness continuously with that page's failure and success thresholds; this is meant for installations behind a load balancer that can fail over to another installation. |

There is no storage-specific readiness configuration: the opt-in uses the thresholds of R-OPS-001,
so one hysteresis mechanism exists.

Acceptance criteria:

- Stopping the local profile's S3 store: `npm publish` of a new version fails with a `503`;
  `npm view` and `curl -I` of existing tarballs succeed; deleting an item succeeds; restarting the
  store lets the publish succeed without manual steps; no blob is marked `missing`.
- With defaults, the local profile's S3 store stopped for five minutes: `/readyz` keeps answering
  `200` with `"degraded": ["objectStore"]` on every running replica, the UI and
  `GET /api/v1/repositories` keep working, and `trove_storage_s3_circuit_open` reports 1 on every
  replica. A replica started during the outage stays not ready. With
  `health.require_object_store: true`, every replica's `/readyz` answers `503` after R-OPS-001's
  failure threshold.

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
- The key `disable-all` is reserved, because `/api/v1/retention/policies/disable-all` (R-STOR-041)
  sits where a policy key would. A create with it answers `422 reserved_name`
  ([api-and-ui.md](api-and-ui.md) R-API-004). The key schema in the OpenAPI document lists it
  under `x-trove-reserved-names`. Matching is exact, as in the router, so `Disable-All` is not
  reserved.
- A policy is created disabled; a create request with `"enabled": true` is rejected with `422`.
  Enabling and disabling are separate calls. Disabled policies can be dry-run, edited and
  deleted; only enabled policies run on schedule or manually.
- A policy with an active run cannot be updated or deleted (`409`). Deleting a policy keeps its
  run history (runs keep a snapshot of the configuration).
- API (permission: system administrator):

  | Method | Path | Result |
  |---|---|---|
  | `GET` | `/api/v1/retention/policies?kind=` | list with last-run summary and `nextRunAt` |
  | `POST` | `/api/v1/retention/policies` | `201`; `409` key exists; `422` invalid; `422 reserved_name` for the key `disable-all` |
  | `GET`, `PUT`, `DELETE` | `/api/v1/retention/policies/{key}` | `GET` returns `ETag: "<revision>"`; `PUT` replaces the configuration, the key is immutable, and `If-Match` is required ([api-and-ui.md](api-and-ui.md) R-API-012, Q-API-2): `428` `precondition_required` without it, `412` `precondition_failed` when stale, `If-Match: *` for a deliberate blind write; `DELETE` accepts `If-Match` |
  | `POST` | `/api/v1/retention/policies/{key}/enable`, `…/disable` | `204` |

```sql
CREATE TABLE retention_policies (
    key                  text PRIMARY KEY CHECK (key ~ '^[A-Za-z0-9_-]{3,64}$'
                                             AND key <> 'disable-all'),  -- reserved (R-API-004)
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
- `PUT` with the current `If-Match` during a run answers `409`.
- `PUT` without `If-Match` answers `428`; a `PUT` with a stale `If-Match`, for example the tag
  read before another administrator widened the repository `exclude` list, answers `412` and the
  stored scope stays as that administrator left it.
- `POST /api/v1/retention/policies` with key `disable-all` answers `422 reserved_name` and creates
  no policy.

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

- **Scope.** `formats` (at least one) and `repositories.include` (at least one; `@any-local` for
  all) select **local** repositories only; remote and virtual repositories are never processed
  (remote caches: R-STOR-045). `paths` narrow by path patterns matched against the lead item's
  path. `packages` and `versions` match normalized names and versions.
- **Patterns (Q-STOR-9).** Retention uses the one matcher that
  [authorization.md](authorization.md) owns, so a pattern copied between a permission policy and a
  cleanup policy selects the same things:
  - `repositories.include` and `repositories.exclude` take the repository selectors of
    R-AUTHZ-004 (keys, key patterns, `@any-local` with an optional format filter such as
    `@any-local:npm`), parsed and validated by the same code: a key selector must name an existing
    repository when the policy is saved. Class selectors other than `@any-local` are rejected with
    `422`, because only local repositories are processed.
  - In `repositories.include`, key selectors are stored by repository ID as R-AUTHZ-004 states, so
    a repository deleted and created again under the same key is not covered; a policy whose
    include selectors all disappeared matches nothing and is reported with `"inert": true`.
  - In `repositories.exclude`, key selectors are matched by key and are not removed when the
    repository is deleted. An exclusion protects whatever repository carries the key, so deleting
    and re-creating a protected repository never exposes it to the policy; binding the exclusion
    to the ID, as for includes, would silently remove that protection. Authorization has no
    repository-level exclude list, so this is the only point where retention binds a selector
    differently.
  - `packages` and `versions` use the name glob of R-AUTHZ-005 (`*` any run of characters except
    `/`, `**` any run, `?` one character except `/`, `\` escapes the next character), compared
    after the format's normalization and with its case rule (NuGet IDs case-insensitive, npm names
    case-sensitive).
  - `paths` use the path pattern of R-AUTHZ-005 (`*` within one segment, `**` as a whole segment).
  - An empty `include` list leaves a dimension unrestricted except for `repositories`, which
    needs at least one entry; an `exclude` match always removes a version from the scope. Invalid
    patterns answer `422` `invalid_pattern`.
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
- A policy with `repositories.include: ["npm-ci-*"]` and `repositories.exclude: ["npm-ci-pinned"]`:
  after `npm-ci-pinned` is deleted and created again, a dry run selects nothing in it.

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

- Schedules use the schedule syntax of R-STOR-025 (Q-STOR-4): five fields in UTC, day of week
  0–6 with 0 = Sunday or the names `SUN`–`SAT`. `7`, `?` and six-field expressions are rejected,
  so an expression written for a dialect whose week starts at 1 = Sunday cannot be misread. An
  empty schedule means manual runs only (RET-011).
- Minimum interval (RET-011): saving a policy fails with `422` `schedule_too_frequent` when two
  consecutive fire times within the 1,461 days (four years, so 29 February occurs) after the
  save are less than `retention.min_schedule_interval` apart. With the default of 6h,
  `@hourly`, `0 * * * *` and `0 0,3 * * *` are rejected, `0 */6 * * *` and `30 2 * * 6` accepted.
  The limit is checked when a policy is saved; changing the key later does not alter stored
  policies.
- A run stops when it reaches `maxRunDurationMinutes`, `retention.max_versions_per_run` removed
  versions, or `retention.error_threshold_percent` failed versions (after at least 100 attempts).
  Everything removed stays removed; the next run re-evaluates from scratch, so a backlog is
  worked off incrementally. Selection is idempotent and safe to interrupt at any version.
- At most `retention.max_concurrent_runs` runs execute system-wide (counted in PostgreSQL); further
  runs wait as `queued` (visible in the history) up to `retention.queue_size`; beyond that, a
  scheduled trigger is skipped with a log entry and an audit event, a manual trigger answers
  `429`. A policy never runs twice at the same time; a trigger while it runs answers `409`.
- **Run limit across workers.** A worker that claims a run job starts the run in one short
  transaction that first locks the single row of `retention_run_gate` (`SELECT … FOR UPDATE`),
  then counts the runs in `running` or `stopping`, and sets the run to `running` only below the
  limit. Two workers that claim runs at the same moment therefore cannot both take the last slot,
  and the lock is held for that transaction only, so it works through a transaction-mode pooler.
  At the limit, the job goes back to `queued` with `run_at` one `jobs.poll_interval` later and
  without counting the attempt (the release statement of [architecture.md](architecture.md)
  R-ARCH-016), and the run stays `queued`. A run resumed after a drain is already `running` and
  keeps its slot without passing the gate again. The enqueuing side (scheduler or manual trigger)
  applies `retention.queue_size` with the same lock.

  ```sql
  -- one row, seeded by the migration that creates the table
  CREATE TABLE retention_run_gate (id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1));
  ```
- Runs are jobs of the `default` queue, claimed by `worker` replicas (R-STOR-046); the scheduler
  that enqueues due runs is the singleton under the `scheduler` lease of
  [architecture.md](architecture.md) R-ARCH-016 and R-ARCH-017. "A policy never runs twice at the
  same time" is the exclusive dedup key `retention:<policy key>` in the job table, in PostgreSQL;
  no Valkey lock is involved (INV-3, Q-STOR-13).
- **Drain.** A node drain releases a running run (R-ARCH-016: back to `queued`, `attempt`
  unchanged). Another worker continues it: it keeps the run row, its counters and its deadline
  (`maxRunDurationMinutes` counts from the first start) and evaluates the selection again, which is
  idempotent. The run's status stays `running`; a drain never fails a run.
- **Lost worker.** A run whose worker disappears without releasing it (crash, node loss: its job
  lease expires) is marked `failed` with reason `worker-lost`: the job kind is registered with
  `MaxAttempts: 1`, so the reaper of R-ARCH-016 fails it instead of re-queueing it, and the next
  scheduled or manual run starts from scratch.
- **Deletion pause.** Before every batch of removals a run reads the deletion pause of R-STOR-024;
  while it is set, the run ends with status `stopped` and `error.code = deletions_paused`, and
  removes nothing more.
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

- `schedule: "0 * * * *"` is rejected; `"0 */6 * * *"` is accepted; `"0 3 * * 7"` and
  `"0 30 3 ? * SUN"` are rejected with `invalid_schedule`.
- A run with `maxRunDurationMinutes: 1` over 50,000 matching versions stops after about one minute
  with status `stopped-limit`; the next run continues with the remaining versions.
- `kubectl drain` of the node whose `worker` runs a retention run over 50,000 matching versions:
  the run finishes on the other `worker` replica with status `completed`, and its report lists
  every removed version once.
- With `retention.max_concurrent_runs: 1` and three `worker` replicas, manual runs of three
  policies are triggered within one second: at no time are two of them `running`, and all three
  end `completed`.

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
- A stop reaches the run through PostgreSQL, whichever `web` replica receives it: stop and
  stop-all set the runs to `stopping` and their jobs to `cancelling` (queued runs end `stopped` at
  once). The run reads its status before every batch, and the job heartbeat of
  [architecture.md](architecture.md) R-ARCH-016 cancels its context, so it ends `stopped` after the
  current batch on whichever `worker` it runs.
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

Build records are owned by a separate spec page, `builds.md` with prefix `R-BLD`, to be written
before milestone M9 (Q-STOR-3). This requirement fixes the cleanup mechanics against the contract
that page has to provide:

- A build run has a name, a number, a start time and a list of produced files given as
  repository, path and SHA-256.
- Build records live in PostgreSQL tables, not as items of a repository. No package policy,
  path-based cleanup, copy, move or repository delete can reach them.
- Build permissions (`delete` on a build) come from [authorization.md](authorization.md)
  R-AUTHZ-027.

Cleanup mechanics:

- `build-cleanup` policy spec: `builds.include` / `builds.exclude` (the name glob of R-AUTHZ-005,
  as in R-STOR-038; build names may contain `/`, which `*` does not cross and `**` does),
  `olderThanDays` (required), `removeArtifacts` (default `true`).
- A selected build run's record is removed; with `removeArtifacts`, each recorded file is
  removed when the item at the recorded path still references the recorded SHA-256 (an
  overwritten path is left alone and reported). The result per build is `removed`,
  `partially-removed (x of y)` or `skipped`.
- Per-build retention (RET-037): `PUT /api/v1/builds/{buildName}/retention` with
  `{"keepNewest": 20, "maxAgeDays": 180, "protectedNumbers": ["1.0.0-rc.1"], "removeArtifacts": false, "wait": false}`
  *(example)* stores the parameters on the build name and applies them **at once**: the call
  enqueues a `build-retention` job for the name. With `wait: false` (default) it answers `202`
  with the job reference of R-OPS-019. With `wait: true` it waits up to
  `retention.build_retention_wait`; if the job finished, it answers `200` with the job result,
  otherwise `202` with the job reference, so a long removal never holds a request open past
  that bound. The stored parameters are applied again after every new run of that build name is
  published.
- A run is removed when it is outside the newest `keepNewest` runs **or** started more than
  `maxAgeDays` days before the evaluation; each rule removes on its own. Runs whose number is in
  `protectedNumbers` are never removed.
- Without `keepNewest` and `maxAgeDays` the `PUT` stores nothing and answers `422`: a retention
  without a keep rule would remove nothing. `GET` on the same path returns the stored parameters
  (`404` when none); `DELETE` removes them (`204`) and removes no run.
- Permission: `delete` on the build for `PUT` and `DELETE`, `read` on the build for `GET`.
- Build records and reports are never selected by package policies.

Acceptance criteria:

- With `keepNewest: 2` on build `web`, publishing a third build removes the oldest record (and
  its files when `removeArtifacts` is true).
- On a build with three runs, `PUT …/retention` with `{"keepNewest": 1, "wait": true}` answers
  `200` and leaves one run; after `DELETE …/retention`, publishing a fourth run removes nothing.
- `PUT …/retention` with `{"protectedNumbers": ["7"]}` alone answers `422` and stores nothing.

Configuration:

```yaml
retention:
  build_retention_wait: 60s    # default (proposed); longest wait of a PUT with "wait": true
```

### R-STOR-044 · Repository caps enforced on push
Priority: **P1** · Covers: RET-035

The caps themselves (maximum distinct snapshots per artifact, maximum distinct tags per image,
kept revisions per overwritten tag) are repository settings owned by
[repositories-and-proxy.md](repositories-and-proxy.md). This page specifies enforcement.

- All three caps are counts (RET-035): distinct snapshot versions per artifact, distinct tag names
  per image, kept revisions per tag name. Federated repositories are not part of the MVP (INV-5);
  when they arrive, they use the same count semantics, not an age in days.
- After a push, the format plug-in's `VersionGroup` names the affected group, and the push's
  commit transaction appends one row (repository, group, push time) to the table `cap_outbox`.
  Rows are only inserted and deleted, never updated, so concurrent pushes to one group never wait
  for each other or for enforcement.
- A loop on every `worker` replica (R-STOR-046) runs every `retention.caps.interval`. One
  iteration is one transaction: it locks up to 100 outbox rows with `FOR UPDATE SKIP LOCKED`,
  evaluates each group among them, removes the oldest variants beyond the cap through the normal
  delete path, oldest first, and deletes exactly the rows it locked. Each variant is removed by
  the version row the evaluation read, so a tag re-pushed meanwhile, which has a new current
  version row, is not removed.
  - A push that commits during an iteration has a row the iteration did not lock, so its group is
    evaluated again. No push is lost, as it would be with one upserted row per group, where a push
    whose upsert met the row locked by a running evaluation would add nothing that the evaluation
    could still see.
  - A worker drained or lost mid-iteration rolls back, and its rows wait for another worker.
  - Two workers may evaluate one group at once when they locked different rows of it. Each removes
    only what is beyond the cap in the state it read, a variant the other already removed is
    skipped, and removals take their locks oldest first, so the outcome is that of one evaluation
    of the later state and the two cannot deadlock. No lease and no Valkey lock is involved.
  - While the deletion pause of R-STOR-024 is set, the loop claims nothing.
- "Oldest" is the earliest `created_at` of the variant's current version row: for a snapshot its
  deploy time, for a tag the time the tag name last received a push (a re-pushed tag counts as
  new). No tag name is exempt, `latest` included; a tag that keeps receiving pushes is never the
  oldest.
- Removals by caps go to the trash can when it is enabled (R-STOR-022), for snapshots and tags
  alike, and are audited like any delete.
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
- With the same cap, pushing `latest`, `t1`, `t2`, re-pushing `latest`, then pushing `t3` removes
  `t1`, not `latest`.
- With a tag cap of 3 and two `worker` replicas, 20 tags of one image are pushed concurrently
  through three `web` replicas while the loop runs with `retention.caps.interval: 1s`. One
  interval after the last push, exactly the 3 newest tags remain, `cap_outbox` is empty, and
  `pg_stat_database.deadlocks` has not grown.

### R-STOR-045 · Unused remote-cache expiry
Priority: **P1** · Covers: RET-036

- The per-remote-repository setting "unused expiry" (a duration; zero or absent keeps cached
  content forever) is owned by [repositories-and-proxy.md](repositories-and-proxy.md).
- The task `remote-cache-cleanup` of the shared task framework (Q-STOR-10; R-OPS-019) removes
  cached file items of remote repositories whose last use, `coalesce(last_download_at,
  created_at)`, is older than their repository's expiry. Removal is permanent (no trash can); the
  content is fetched again on the next request. Removed blobs are collected by R-STOR-024.
- The task processes repositories one after another in batches and checkpoints after each batch,
  so a drained run continues on another `worker` from its checkpoint. It honours the deletion
  pause of R-STOR-024 before every batch. Manual start:
  `POST /api/v1/system/tasks/remote-cache-cleanup/run` (system administrator,
  `202 {"job": {…}}`, `409` while a run is active).

Configuration:

```yaml
maintenance:
  tasks:
    remote-cache-cleanup: {enabled: true, schedule: "0 3 * * *"}   # default (proposed): daily 03:00 UTC
```

Acceptance criteria:

- With expiry `48h` on `npm-remote`, a tarball last downloaded 3 days ago is removed by the job; a
  following `npm install` fetches it from the upstream again.

## Roles, scale-out and backups

### R-STOR-046 · Storage work per role and across replicas
Priority: **MVP** · Covers: trove-only

Every storage mechanism runs on the role INV-2 assigns, behaves the same on any number of replicas
of that role, and uses no state that an earlier request left on one replica (INV-18). The expected
load exceeds one replica (INV-12), so no mechanism on this page may depend on the replica count.

| Work | Role | How replicas share it |
|---|---|---|
| Uploads (R-STOR-004 to R-STOR-010), downloads (R-STOR-012 to R-STOR-014), item operations up to `storage.delete.max_sync_items`, remote-cache fills | `web` | Any replica serves any request; metadata in PostgreSQL, content in S3 |
| Multi-request upload sessions (R-STOR-011, P1) | `web` | Session row and parts in PostgreSQL, bytes in one S3 multipart upload; each request may reach another replica |
| Download-statistics buffer (R-STOR-015) | `web` | Per replica; flushed every interval and at shutdown, in item order |
| Usage fold (R-STOR-003), cap enforcement (R-STOR-044, P1) | `worker`, every replica | Loops that lock their rows with `FOR UPDATE SKIP LOCKED`; any number of workers, no lease |
| Collection, orphan sweep, scrub, remote-cache cleanup (R-STOR-025 to R-STOR-027, R-STOR-045) | `worker` | Jobs of the queue `maintenance` with an exclusive dedup key in the job table (Q-STOR-10, Q-STOR-13) |
| Storage summary (R-STOR-028) | `worker` | The task of [operations-and-observability.md](operations-and-observability.md) R-OPS-023; reads are served by any `web` replica from its snapshot table |
| Retention runs, build retention, background deletes, copies, moves and trash restorations (R-STOR-018, R-STOR-019, R-STOR-023, R-STOR-040, R-STOR-043) | `worker` | Jobs of the queue `default` ([architecture.md](architecture.md) R-ARCH-016; retention runs and build retention with the dedup keys registered there); the retention run limit through the row lock of `retention_run_gate` (R-STOR-040) |
| Retention reports (R-STOR-042) | `worker` | Written by the run through the upload pipeline and the worker's spool |
| Startup check (R-STOR-035) | `web`, `worker` | Per replica, before its first readiness |

- **Local storage.** The spool is the only local storage on this page: one per `web` and `worker`
  pod, an `emptyDir` or a generic ephemeral volume, never shared between pods and never persistent
  (INV-13, R-STOR-004). No storage mechanism needs a ReadWriteMany or a persistent volume.
- **Exclusion.** Every rule that keeps storage work from running twice lives in PostgreSQL: job
  dedup keys, row locks and `SKIP LOCKED` claims (INV-3). None uses Valkey or an advisory-lock
  lease (Q-STOR-13). The correctness of the commit protocol, collection and the orphan sweep rests
  on row locks alone; exclusion only saves duplicate work.
- **Valkey.** Storage reads and writes nothing in Valkey itself. It uses Valkey only through
  [architecture.md](architecture.md): job wake-up signals and the change messages of the `cache`
  area (R-ARCH-018), and the flight lock of remote fills (R-ARCH-033). While Valkey is unavailable,
  every storage operation behaves as with it; job pick-up waits for `jobs.poll_interval`, and a
  change to the storage settings waits for the change-log poll.
- **Shared values.** Counters and limits that must agree across replicas live in PostgreSQL:
  usage, quotas and download statistics (Q-STOR-12). Per-replica values exist only to protect the
  replica itself: the spool budget, the S3 connection pool and part concurrency, and the circuit
  breaker (R-STOR-004, R-STOR-033).
- **Splitting the service.** Because every replica of a role can do everything this table assigns
  to the role, a web pool that serves only package paths, or a worker pool that claims only the
  `maintenance` queue ([ha-and-helm.md](ha-and-helm.md) R-HA-062, P1), needs no storage change.
- **Drain.** A node drain costs no acknowledged data and fails no storage operation that its client
  retries:
  - on `web`: an upload cut before its commit leaves nothing (R-STOR-004); a cut download resumes
    with `Range` on any replica (R-STOR-012); an upload session continues on another replica from
    its last recorded part, because the cut request releases its writer claim (R-STOR-004 step 8,
    R-STOR-011); download counts are flushed (R-STOR-015);
  - on `worker`: jobs are released with their checkpoints (R-STOR-025, R-STOR-026, R-STOR-045),
    retention runs continue on another worker (R-STOR-040), a collector waits for its S3 calls
    and hands its unfinished claims over before its job is released (R-STOR-024), and an
    interrupted fold or cap iteration rolls back (R-STOR-003, R-STOR-044).

Acceptance criteria:

- With Valkey stopped, the storage integration suite (upload, deduplication, download with
  `Range`, delete, copy, collection with `grace_period: 0s`, quota rejection) passes against three
  `web` and two `worker` replicas, and no replica turns unready.
- The scatter profile of [architecture.md](architecture.md) R-ARCH-032 passes the R-STOR-005 race
  test through the Service, with uploads, deletes and downloads spread round robin over three `web`
  replicas and collection running on two `worker` replicas.
- During [ha-and-helm.md](ha-and-helm.md) R-HA-059's drain test, one `worker` runs `gc-full` and
  another a retention run; both finish on the remaining `worker` replicas, and no client operation
  of the test's load fails.
- A source test fails when a package below `internal/blob` or the storage job handlers imports the
  Valkey client or calls a session-level advisory-lock function.

### R-STOR-047 · Storage state in backups and restores
Priority: **MVP** · Covers: trove-only

[backup-and-restore.md](backup-and-restore.md) owns the backup job, its format and the restore
procedure (INV-19). This requirement states what storage provides to them and what a restore
leaves for storage.

- **Referenced blobs.** A backup finds the blobs its snapshot references through the reference
  rules of R-STOR-003, as collection does (rule 3: the referencing tables come from the catalog's
  foreign keys to `blobs`). A table that a later page adds under those rules is backed up without
  a change to the backup.
- **Garbage collection.** The per-blob protocol of R-STOR-024 contains the hold check (2b), so a
  running backup never loses a blob it still has to copy, whatever `storage.gc.grace_period` is
  and however long the backup runs. The orphan sweep needs none (R-STOR-026). Collection and a
  backup run at the same time; neither waits for the other.
- **Export classes** of this page's tables ([backup-and-restore.md](backup-and-restore.md)
  R-BAK-004):

  | Table | Class | Reason |
  |---|---|---|
  | `blobs` | `blobs` | Only rows that the snapshot references; unreferenced and `deleting` rows are not restored |
  | `blob_gc_queue` | `transient` | Candidates only |
  | `upload_sessions`, `upload_session_parts` | `transient` | Staged bytes under `_uploads/` are not backed up; clients restart their uploads |
  | `pending_sidecars` | `transient` | Declarations waiting for a file for at most `storage.sidecars.pending_ttl` |
  | `repository_usage`, `storage_usage_totals`, `storage_usage_deltas` | `data` | Exported, then recomputed by the restore (below) |
  | every other table of this page | `data` | Items (trashed ones included), packages, versions, properties, download statistics, property sets, retention policies, runs and run items, `retention_run_gate` (its one row), `cap_outbox` (pending cap evaluations; evaluating a group again is harmless) |

- **Restore fix-ups.** In the fix-up transaction of R-BAK-022 the restore calls
  `storage_usage_recount()` (R-STOR-003), because the `blobs` filter drops rows that the exported
  physical totals still count; the quota of R-STOR-029 would otherwise decide on wrong numbers.
  Blob rows of digests the backup recorded as damaged are loaded with
  `content_status = 'missing'` (R-BAK-021 step 4), so their downloads answer `503` with
  `Retry-After` as for any missing object (R-STOR-012,
  [backup-and-restore.md](backup-and-restore.md) R-BAK-001). Trashed items keep
  their `trashed_at`, so trash expiry continues from the original deletion.
- **No storage work during a restore.** While `system_state` marks a restore in progress, `worker`
  replicas wait at their schema check and claim no job ([architecture.md](architecture.md)
  R-ARCH-024 step 3). No collection, orphan sweep or retention run can therefore meet blob objects
  that the restore has copied before the rows that reference them are loaded.
- **Key families.** Only `sha256/` objects that the snapshot references are backed up (R-STOR-001).
  A restore into an empty installation writes exactly those, so it leaves no orphans. A restore
  onto an existing blob prefix (R-BAK-025, P1) sets the deletion pause, and after deletions resume,
  the orphan sweep (R-STOR-026) removes what the restored database does not reference.
- **Platform restores.** A point-in-time restore of the platform's database backup relies on the
  grace period or bucket versioning instead ([ha-and-helm.md](ha-and-helm.md) R-HA-024, R-STOR-024
  "Grace period"), and on the deletion pause that `trove admin post-restore` sets (R-HA-025).

Acceptance criteria:

- A backup taken while three unreferenced `live` blobs and two `deleting` rows exist is restored.
  The first storage summary afterwards (R-STOR-028) reports `blobs.count` and `blobs.bytes` equal
  to `SELECT count(*), sum(size) FROM blobs WHERE state = 'live'`, and a system quota set just
  above that size admits an upload that fits.
- A unit test compares the backup table registrations of this page's tables with the table above.

## Configuration reference

<details>
<summary>All storage, retention and storage-task keys of trove.yaml</summary>

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
  uploads:                               # multi-request upload sessions (R-STOR-011, P1)
    part_size: 16MiB                     # default (proposed)
    writer_lease: 60s                    # default (proposed)
    session_ttl: 4h                      # default (proposed)
    session_max_age: 20h                 # default (proposed); < storage.orphans.multipart_max_age
  deploy_by_checksum:
    enabled: true                        # default (proposed)
    max_candidates: 100                  # default (proposed)
  sidecars:
    pending_ttl: 1h                      # default (proposed)
  download:
    buffer_size: 256KiB                  # default (proposed)
    verify: true                         # default (proposed)
    hold_back: 64KiB                     # default (proposed); at least 1 KiB
  stats:
    flush_interval: 10s                  # default (proposed)
  delete:
    max_sync_items: 10000                # default (proposed)
    batch_size: 5000                     # default (proposed)
  properties:
    max_values_per_item: 1000            # default (proposed)
    max_recursive_items: 100000          # default (proposed)
  usage:
    fold_interval: 5s                    # default (proposed); also the lag of quota decisions (R-STOR-029)
  gc:
    grace_period: 24h                    # default (proposed)
    delete_settle: 30s                   # default (proposed); at least 1 s
    handover_delay: 10m                  # default (proposed)
    batch_size: 1000                     # default (proposed)
    batch_delete: true                   # default (proposed)
    delete_concurrency: 8                # default (proposed)
  orphans:
    min_age: 24h                         # default (proposed)
    multipart_max_age: 24h               # default (proposed)
  scrub:
    check_interval: 168h                 # default (proposed)
    deep_interval: 2160h                 # default (proposed)
    deep_bytes_per_second: 50MiB         # default (proposed)
retention:
  min_schedule_interval: 6h              # default (proposed)
  max_concurrent_runs: 3                 # default (proposed)
  queue_size: 10                         # default (proposed)
  max_versions_per_run: 100000           # default (proposed)
  error_threshold_percent: 10            # default (proposed)
  batch_size: 500                        # default (proposed)
  history_retention: 2160h               # default (proposed)
  item_detail_retention: 720h            # default (proposed)
  build_retention_wait: 60s              # default (proposed)
  caps:
    interval: 5m                         # default (proposed)
maintenance:
  tasks:                                 # task framework of R-OPS-019; schedule syntax of R-STOR-025
    gc:                   {enabled: true, schedule: "*/30 * * * *"}  # default (proposed)
    gc-full:              {enabled: true, schedule: "0 2 * * 0"}     # default (proposed)
    orphan-sweep:         {enabled: true, schedule: "0 5 * * 6"}     # default (proposed)
    scrub:                {enabled: true, schedule: "0 4 * * *"}     # default (proposed)
    storage-summary:      {enabled: true, schedule: "17 * * * *"}    # default (proposed, R-OPS-019)
    remote-cache-cleanup: {enabled: true, schedule: "0 3 * * *"}     # default (proposed)
```

Durations use Go duration syntax (`h`, `m`, `s`; there is no `d` unit); sizes use IEC units
(`KiB`, `MiB`, `GiB`). Runtime settings (trash can, system quota) live in PostgreSQL and are
changed through `PUT /api/v1/settings/storage` with `If-Match` (R-STOR-022). Readiness has no storage key: the opt-in
`health.require_object_store` belongs to R-OPS-001 (R-STOR-036). The internal loops
(`storage.usage.fold_interval` and `retention.caps.interval` on every `worker` replica,
`storage.stats.flush_interval` on every `web` replica, R-STOR-046) are not maintenance tasks and
keep their own interval keys. There is no storage key for Valkey: storage uses it only through
[architecture.md](architecture.md) (R-STOR-046). Startup validation refuses
`storage.gc.handover_delay` not above `jobs.lease_duration` plus twice `storage.gc.delete_settle`,
`storage.uploads.session_max_age` not below `storage.orphans.multipart_max_age`,
`storage.upload.blob_wait_timeout` not above twice `storage.gc.delete_settle`, and a
`storage.upload.max_size` above 10,000 × `storage.uploads.part_size`; each message names the keys.

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
  opens it (the UI session cookie is same-origin). `Content-Disposition: attachment`, `nosniff`,
  `Content-Security-Policy: sandbox; default-src 'none'` and
  `Cross-Origin-Resource-Policy: same-site` close that path; directory indexes escape every name
  (R-STOR-012, R-STOR-017). The core sets these headers in a writer wrapper that no format or file
  handler can weaken ([architecture.md](architecture.md) R-ARCH-007), so a handler that forgets them
  does not reopen the path. A proxy that strips response headers, or a browser that ignores both
  `attachment` and the sandbox, still does; that residual is H-3 in
  [architecture.md](architecture.md), which the optional content origin (R-ARCH-029, P1) narrows.
- **The bucket is a trust boundary.** Principals with access to the bucket read every blob of
  every repository, bypassing trove's permissions. The IAM policy of R-STOR-031 grants trove
  only its prefix; operators keep S3 Block Public Access on, require TLS (`aws:SecureTransport`
  in the bucket policy) and grant no other principal access to the prefix. Residual risk: H-11.
- **Credentials.** S3 credentials come from workload identity or from mounted files, never from
  inline configuration (R-STOR-031); the outbound proxy password likewise (R-STOR-034). Static
  keys are the weakest mode and need rotation by the operator.
- **Spool files contain unencrypted uploads** on the pod's scratch volume for the duration of a
  request. They are created `0600`, removed on every exit path and wiped at startup
  (R-STOR-004). With a generic ephemeral volume, the files of a killed pod stay on the released
  volume unless its storage class deletes it (`reclaimPolicy: Delete`,
  [ha-and-helm.md](ha-and-helm.md) R-HA-048). Node-level disk encryption is the platform's
  responsibility; the residual risk is H-2 in [architecture.md](architecture.md).
- **Upload sessions are bound to their principal.** Any replica continues a session (R-STOR-011),
  so the session ID, which appears in URLs and logs, must not authorize anything by itself. Every
  request of a session is checked against the session's principal and authorized for `deploy`
  again; another principal gets the same `404` as for an unknown session.
- **Integrity.** Uploads are verified against declared digests and, where the store supports it,
  by the store against the payload checksum (R-STOR-006); full downloads are verified before the
  last bytes leave (R-STOR-013); the scrub catches damage at rest (R-STOR-027).
- **Mass deletion is privileged and observable.** Retention policies, trash purge, garbage
  collection and the orphan sweep are system-administrator operations; policies start disabled
  and can be dry-run; every run, trigger and removal is audited (INV-16).
- **Download statistics are personal data.** `last_downloader` links a principal to a download;
  it is cleared when the principal is erased (R-STOR-015).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-STOR-1 · Readiness tied to object-storage reachability (INV-12)

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Revisited on 2026-10-28 after the owner added the Valkey, horizontal-scale, backup and storage-class constraints: the choice stays (d). The rationale now covers a node drain during an object-store outage and states that Valkey plays no part in this rule; no requirement text changed.

All replicas share one bucket. If readiness follows S3, an S3 outage makes every replica unready at
the same moment and the Service loses all endpoints.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep INV-12 as written | Readiness fails once S3 checks have failed for a while, with hysteresis | Literal invariant; traffic leaves a replica whose own path to S3 is broken | A shared outage turns into a total outage: UI, API, logins, CI token exchange, metadata, deletes and deduplicated uploads go down with blob traffic, although R-STOR-036 could serve them |
| (b) Process and PostgreSQL only | Readiness ignores S3; the circuit breaker fails blob requests individually; metrics and alerts report S3 health | Degraded service instead of a total outage; nothing to coordinate | A release with a wrong endpoint, network path or CA, whose errors look transient, becomes ready and replaces every working pod, breaking all blob traffic; no built-in way to take an installation out of an outer load balancer that could fail over to a second installation |
| (c) Fail only the odd replica | Readiness fails on S3 only when this replica fails while others succeed, compared through PostgreSQL | Routes around one replica's broken network path | Cross-replica coordination for a case the circuit breaker and client retries already contain; undefined with two replicas that disagree |
| (d) (b) after start-up, (a) as opt-in | A replica becomes ready only after the startup check of R-STOR-035 passed; afterwards S3 failures degrade blob requests and raise alerts (`health.require_object_store: false`); `true` makes R-OPS-001's `objectStore` check gate readiness continuously | Default keeps the control plane and metadata up; a rollout with a broken S3 configuration still stalls; installations behind a cross-site failover can still drain an installation whose bucket is down | One more configuration branch to test; a replica that loses only its own S3 path stays in the Service and answers blob requests with a fast `503` until the alert is acted on; a replica whose process restarts during an outage (OOM kill, node loss) stays out of the Service until the store returns |

**Choice:** (d) — object storage gates only a replica's first readiness (through the startup
check); after that, running replicas stay ready during an S3 outage, and continuous gating is an
opt-in.

**Rationale:** Readiness exists to move traffic from a broken replica to a healthy one. With a
dependency that every replica shares there is no healthy replica to move to, so (a) only removes
the parts that still work, including the UI and API operators need to diagnose the outage. The
single-replica case that (c) targets is already visible through the alert
`TroveObjectStoreUnreachableSingleReplica` ([ha-and-helm.md](ha-and-helm.md) R-HA-045), and
clients retry its `503` onto other replicas. Gating the first readiness keeps the one protection
(a) offers that matters: new pods with a wrong bucket, endpoint or credential never become ready,
so a rolling update stalls instead of replacing working replicas. The opt-in costs one boolean that
R-OPS-001 already defines, and it serves the one setup where continuous gating helps: a global
load balancer in front of two installations. Using R-OPS-001's thresholds for the opt-in
removes this page's second hysteresis mechanism. The cost of gating the first readiness is that a
replica restarted during an outage stays out until the store returns. The outage itself never
causes such a restart, because liveness never reads a dependency
([operations-and-observability.md](operations-and-observability.md) Q-OPS-1), so only an
unrelated crash or node loss during the outage costs that replica's capacity. The same decision
is recorded as Q-HA-1 in [ha-and-helm.md](ha-and-helm.md) and Q-OPS-2 in
[operations-and-observability.md](operations-and-observability.md).

The owner's drain constraint (INV-12) does not change this. A node drained during an S3 outage
evicts one pod per role; its replacement stays not ready, so the disruption budget
(`maxUnavailable: 1`, [ha-and-helm.md](ha-and-helm.md) R-HA-032, R-HA-059) refuses the next
eviction and the drain waits until the store returns. The outage costs each role at most one
replica of capacity, and the Service never loses its last ready endpoints. Under (a) the same
drain would find every replica already unready. Valkey is no input to this rule: its outage
degrades and never gates readiness (INV-12), and object-storage readiness reads nothing from it.
The development agent would revisit this if a deployment shows a replica-local S3 failure that the
circuit breaker does not contain, or if drains during S3 outages stall maintenance windows often
enough to matter.

**Applied in the spec:** R-STOR-036: the readiness row now states first-readiness gating, the
default and the opt-in; the keys `storage.s3.readiness.fail_after` and `recover_after` are removed
(here and in the configuration reference); an acceptance criterion checks `/readyz` during an S3
outage with both settings and for a replica started during the outage. R-STOR-035: the startup
check keeps retrying transient errors without exiting, so a replica started during an outage waits
not ready instead of crash-looping, and becomes ready as soon as the store returns rather than after
a restart back-off; `storage.s3.startup_timeout` is removed (here and in the configuration
reference) and an acceptance criterion covers the case. This changes INV-12; the edit to
[README.md](README.md) is listed as a cross-page consequence. The revisit of 2026-10-28 changed
no requirement text.

### Q-STOR-2 · SHA-512 for every blob or only on demand

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-4 says SHA-512 "where a format needs it".

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every blob | SHA-512 is computed in the single hashing pass of every upload and stored on the blob row | Both MVP formats need it: npm's `dist.integrity` is a `sha512-…` SRI string, and [formats/nuget.md](formats/nuget.md) verifies remote packages by their SHA-512 hash; content first stored through one format is complete when another format references it; `Repr-Digest: sha-512` and `.sha512` sidecars work everywhere | One more hash state per upload and 64 bytes per blob row |
| (b) On demand | Computed only when the format plug-in asks for it; filled later from S3 when another format needs it | Saves the SHA-512 pass for generic content | Needs a backfill job that re-reads objects from S3 (the machinery STOR-005 describes) and a synchronous re-read when a deduplication hit lacks the digest; two code paths |
| (c) Format-owned only | No SHA-512 on the blob row; formats keep it in their own tables | Smaller blob row | Sidecars, `Repr-Digest`, deploy-by-checksum verification and cross-format reuse lose their single source; each format derives the digest itself |

**Choice:** (a) — every upload computes and stores SHA-512 alongside SHA-256, SHA-1 and MD5.

**Rationale:** With npm and NuGet first, nearly every upload needs SHA-512 anyway, so (b) would save
almost nothing and still require the backfill path. Deduplication makes "the format that uploaded
first" arbitrary: a tarball first stored through a generic repository and later referenced by npm
must not trigger a re-read from S3. NuGet's version tombstones also compare SHA-512
([formats/nuget.md](formats/nuget.md) R-NUGET-006). The extra hash runs in the same pass as the others. CPU time is
small next to network and S3 latency. The development agent would revisit this only if profiling
shows uploads to be CPU-bound.

**Applied in the spec:** No change; the requirement text already follows this option (R-STOR-002,
R-STOR-004, `blobs.sha512 NOT NULL` in R-STOR-003). This refines INV-4 rather than contradicting
it; the alignment of INV-4's wording is listed as a cross-page consequence.

### Q-STOR-3 · Owner of build records

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

RET-023 and RET-037 (P1) need build records (catalog `BLD`), but no spec page owns them.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) New page `builds.md` | A spec page with prefix `R-BLD` owns build records, their API and their lifecycle; this page keeps only the cleanup mechanics (R-STOR-043) | One owner per model; matches the conclusion of [authorization.md](authorization.md) (Q-AUTHZ-8) and [api-and-ui.md](api-and-ui.md) (Q-API-5); storage needs only a narrow contract | One more page to write before M9 |
| (b) This page | storage.md also specifies build records | No new page | Mixes two subjects; publishing, promotion and CI integration have nothing to do with blob storage |
| (c) Defer | RET-023 and RET-037 move to P2 together with build metadata | Less P1 scope | Leaves 15 P1 `BLD` entries and two P1 `RET` entries unmet |
| (d) In [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | GitLab CI is the main producer, so its page owns build records | Close to the main producer | Ties a format-neutral model to one CI system and to an authentication page |

**Choice:** (a) — build records get their own page `builds.md` (`R-BLD`); R-STOR-043 stays here
and is written against a fixed contract.

**Rationale:** Build records are a resource model of their own with publishing, listing,
permissions (R-AUTHZ-027) and later promotion. Every page that met the question reached the
same answer, so one page avoids three partial owners. Storage needs only four facts from it:
name, number, start time, and the produced files as repository, path and SHA-256. The prototype is
not blocked, because builds belong to milestone M9. The development agent would revisit this only
if the builds page turned out to be no larger than the contract, which the 15 P1 `BLD` entries
rule out.

**Applied in the spec:** R-STOR-043 names the owner and states the contract `builds.md` has to
provide, including that build records live in PostgreSQL tables and not as items of a repository,
so no package policy, path cleanup or repository delete can reach them (the hazard the catalog
describes under RET-037). Creating `builds.md` and adding it to the page table is a cross-page
consequence.

### Q-STOR-4 · Cron dialect shared by all schedules

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Storage tasks, retention policies and the maintenance tasks of
[operations-and-observability.md](operations-and-observability.md) all take schedules. The
catalog's dialect (RET-011) has a seconds field and `?`, and leaves open whether the day of week
counts 0–6 from Sunday or 1–7 with Sunday = 1.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Five-field cron in UTC | POSIX/Kubernetes fields, day of week 0–6 with Sunday = 0 or names, the Kubernetes macro set; strict rejection of other dialects' syntax | Operators read it from Kubernetes CronJobs; one parser; UTC has no skipped or doubled runs at daylight-saving changes | No local-time maintenance windows |
| (b) Six-field with seconds and `?` | The catalog's dialect | Expressions written for that dialect paste unchanged | Seconds precision has no use for jobs bounded to six hours or more; unfamiliar to Kubernetes operators; day of week 1 = Sunday conflicts with every crontab |
| (c) (a) plus a time zone | Same syntax, evaluated in a configurable zone | Local-time windows | Daylight-saving gaps and repeats need rules; can be added later without breaking (a) |
| (d) Accept both (a) and (b) | Dialect chosen by field count | Migration convenience | The dialects number weekdays differently (1 is Monday in one, Sunday in the other), so a pasted expression silently runs on the wrong day: the very ambiguity RET-011 leaves open |

**Choice:** (a) — five-field cron in UTC with the Kubernetes macros; `?`, `7`, six-field forms,
`@every` and time-zone prefixes are rejected.

**Rationale:** One parser for every schedule, in the syntax Kubernetes operators already read
(Q-OPS-7 in [operations-and-observability.md](operations-and-observability.md) decided the same
and points to R-STOR-025 as the normative dialect). Kubernetes itself accepts `?` as `*`; trove
rejects it on purpose. Strict rejection turns the dangerous case of (d), an expression
from a 1 = Sunday dialect, into a loud `422` instead of a cleanup on the wrong day. UTC avoids
daylight-saving runs that are skipped or doubled. Option (c) can be added later as a separate field
without changing stored expressions. The development agent would revisit this if operators need
local-time maintenance windows, and would then add (c).

**Applied in the spec:** R-STOR-025 now contains the normative schedule syntax: fields and names,
POSIX day matching, macros, rejected forms with the error code `invalid_schedule`, never-firing
expressions, and a parser test table. R-STOR-040 refers to it and fixes the RET-011 ambiguities:
day of week 0–6 with Sunday = 0, and the 6-hour minimum checked over four years of fire times.

### Q-STOR-5 · Does overwriting a file send the previous content to the trash can?

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Yes, for user-deployed files | In local repositories with the trash can enabled, the replaced item goes to the trash can; generated metadata is replaced without trash | `overwrite` gets the same safety net as `delete`; a CI job re-publishing a release path is recoverable | Trash volume in repositories that overwrite routinely, bounded by the retention period |
| (b) No | Overwrite replaces in place; only deletes go to the trash can | No trash volume from overwrites | The one destructive action the trash can does not cover |
| (c) Per-repository switch | `config.storage.trashOnOverwrite`, default on | Lets a repository that overwrites large files opt out | A setting whose only use is to turn protection off; more surface for a case not yet observed |

**Choice:** (a) — an overwrite of a user-deployed file in a local repository sends the previous
item to the trash can when the trash can is enabled.

**Rationale:** An overwrite destroys the previous content exactly like a delete, and the typical
accident is a pipeline publishing to an existing release path. For the MVP formats the cost is
small: npm and NuGet versions are immutable (a re-publish answers `409`, R-NPM-005 and
[formats/nuget.md](formats/nuget.md)), so overwrites happen mainly on raw and generic paths. (c)
can be added without changing (a)'s default. The development agent would revisit this if a
repository that overwrites large files routinely makes the trash volume a measurable cost.

**Applied in the spec:** No change to the normative text; R-STOR-005 and R-STOR-022 already follow
this option. R-STOR-022 gains an acceptance criterion that restores overwritten content from the
trash can.

### Q-STOR-6 · Download verification on by default

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) On by default | Full streamed downloads are hashed and the last bytes are held back until the digest matches (R-STOR-013) | A client never receives a complete corrupted or tampered file | One SHA-256 pass per downloaded byte |
| (b) Off by default | Operators enable it for high-assurance installations | No CPU cost | Operators rarely know they need it until a corrupted artifact has spread |
| (c) Sampled or conditional | Verify only blobs whose last deep check is older than a threshold, or every n-th download | Lower CPU cost | Breaks the guarantee for exactly the blobs between checks, which is where undetected damage sits |

**Choice:** (a) — verification is on by default; `storage.download.verify: false` remains for
CPU-bound replicas.

**Rationale:** The cost is low: Go's `crypto/sha256` uses the CPU's SHA instructions on arm64 and,
since [Go 1.21](https://go.dev/doc/go1.21), on amd64 (3–4 times faster). That puts hashing well
above a replica's network throughput. The benefit is a guarantee that (c) gives up exactly where
it matters. A default-on check is only acceptable if it cannot hurt availability: a false
`corrupt` makes R-STOR-012 answer `500` for that content until it is repaired, so only evidence
about the stored object (a wrong S3 length or a complete read with the wrong digest) may mark a
blob, never a connection reset or timeout. Ranged downloads and presigned redirects stay
unverified whatever the setting (H-11). The development agent would revisit this if profiling
shows downloads to be CPU-bound on hardware without SHA instructions; the switch exists for that.

**Applied in the spec:** R-STOR-013 keeps `verify: true` as the default and now separates the two
outcomes of a failed check: the response is always aborted, but only a wrong S3
`Content-Length` or a complete read with the wrong SHA-256 marks the blob `corrupt`; transport
errors leave the status unchanged, with an acceptance criterion using a fault proxy.

### Q-STOR-7 · Status code for an exceeded quota

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `507` for quotas, `413` for size | `507 Insufficient Storage` for the system capacity limit and repository quotas; `413` only for the per-request size limit | The status line names the cause; a NuGet push failure is reported as status code and reason phrase, so `507 (Insufficient Storage)` is the message the user sees; one code per cause on every endpoint | `5xx` invites client retries: npm retries non-streamed `PUT` requests on any status ≥ 500 ([make-fetch-happen](https://github.com/npm/make-fetch-happen/blob/main/lib/remote.js)), by default twice, after 10 s and 60 s ([`fetch-retries`, `fetch-retry-*`](https://docs.npmjs.com/cli/using-npm/config#fetch-retries)), so `npm publish` reports the failure after about 70 s; NuGet retries every `5xx` with exponential backoff ([HttpRetryHandler](https://github.com/NuGet/NuGet.Client/blob/dev/src/NuGet.Core/NuGet.Protocol/HttpSource/HttpRetryHandler.cs)) |
| (b) `413` for both | Quota and size limit answer `413 Content Too Large` | Not retried by clients | Misdiagnoses the cause: a NuGet user sees "Request Entity Too Large" and shrinks a package that is not too large |
| (c) `403` with a problem code | Quota exceeded is reported as a refusal | Not retried | NuGet shows "Forbidden" and sends the user into a permission investigation |
| (d) `507`, but `413` on npm endpoints | As (a), except that npm endpoints answer a quota with `413` and the R-NPM-005 body | npm fails at once, and still names the limit because it prints the body's `error` text whatever the status | One cause, two codes depending on the endpoint: metrics and alerts by status, the problem registry and `/files` disagree with npm; npm prints "413 Request Entity Too Large" in front of a text about a quota; every later format whose client retries `5xx` would ask for its own exception |

**Choice:** (a) — `507` for every quota and capacity limit, `413` only for the per-request size
limit.

**Rationale:** For dotnet and nuget.exe the status line is the only text a failed push shows: the
push calls `EnsureSuccessStatusCode` and prints no body. Go's HTTP server cannot set a custom
reason phrase, and HTTP/2 has none, so the code itself has to name the cause; that rules out (b)
and (c). npm prints R-NPM-005's `error` text whatever the code, and that text names the limit.
The cost of (a) is the retry delay on npm, about 70 s with default settings, plus a few seconds
on NuGet. A quota failure cannot succeed by retrying until an administrator frees space or raises
the limit, so the delay postpones a failure the user has to escalate anyway; it does not turn a
success into a failure. Each retry is rejected again at admission before the body is read,
whenever the length is declared; npm, which sends without `Expect: 100-continue`, still transmits
part of its publish document before the connection is closed. (d) removes the delay for one
client at the price of a second code for the same cause on every surface that counts status
codes. The development agent would revisit this if CI users report the npm delay as a problem;
the change would then be (d).

**Applied in the spec:** R-STOR-029 states the code split, says that a `507` carries no
`Retry-After`, names the client retries and their delay, uses the problem code `quota_exceeded`
(snake case as [api-and-ui.md](api-and-ui.md) R-API-009 requires; the same code on npm endpoints),
adds the problem members `quota`, `limitBytes` and `usedBytes`, and adds acceptance criteria for
`npm publish` and `dotnet nuget push`. The matching rows in the npm and NuGet error tables are
cross-page consequences.

### Q-STOR-8 · Copy and move target semantics

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Exact destination, merge | `target.path` is where the source ends up; an existing folder there is merged into; identical items count as unchanged | Idempotent: repeating a promotion yields the same tree and succeeds | A merge can mix old and new files in a folder (only additions; `overwrite: false` never replaces a file) |
| (b) Unix `cp` semantics | Copying folder `a/b` onto an existing `x/b` produces `x/b/b` | Familiar from shells | The result depends on state the caller may not know; the second run of a promotion script nests instead of updating |
| (c) Exact destination, existing folder is a conflict | Like (a), but the call fails when the target folder exists unless `merge: true` is given | Explicit about merging | A repeated promotion fails unless the flag is set; the protection it adds is already given by `overwrite: false` |

**Choice:** (a) — the target path is the exact destination, existing folders are merged, and
items with the same content count as `unchanged`.

**Rationale:** An API call cannot see whether the target exists at the moment it runs, so (b)
makes one request's result depend on hidden state. (c) adds a flag whose protection already exists,
because `overwrite: false` never replaces a different file during a merge. Counting identical items
as `unchanged` makes a repeated copy succeed, which is what promotion scripts need. The development
agent would revisit this if users ask for "fail when the target folder exists"; that can come as an
extra flag without changing the default.

**Applied in the spec:** R-STOR-019 adds the `unchanged` count, the `type_conflict` rule for file
against folder (not resolvable by `overwrite`), the requirement that target paths pass the target
repository's include and exclude patterns (R-REPO-005), and an acceptance criterion for a repeated
copy.

### Q-STOR-9 · One pattern syntax across retention and permissions

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Retention scopes (R-STOR-038) and permission policies ([authorization.md](authorization.md))
both match repository keys, paths and package names.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One matcher, owned by authorization.md | Repository selectors of R-AUTHZ-004; name glob and path pattern of R-AUTHZ-005 | A pattern copied between a permission policy and a cleanup policy selects the same things; one implementation and one test suite | Retention inherits authorization's rule that `*` does not cross `/` (scoped npm names need `@*/*` or `**`) |
| (b) Own syntax per subsystem | Retention defines its own globs | Free to tailor | Two syntaxes guarantee that a copied pattern silently matches something else; for a deletion feature, a silent mismatch deletes |
| (c) Prefix-only name patterns everywhere | Exact names or a trailing `*`, as [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-005 uses for routing | Simplest | Cannot express the version selections cleanup needs, such as `*-ci.*` or `*-SNAPSHOT` |

**Choice:** (a) — retention uses the repository selectors and patterns of R-AUTHZ-004 and
R-AUTHZ-005 through the same code.

**Rationale:** Operators write both kinds of pattern for the same repositories, and in retention a
pattern that matches more than intended removes content. R-AUTHZ-005's glob contains the prefix
form, so `@acme/*` and `Acme.*` mean the same in routing, permissions and cleanup. Binding
include selectors to repository IDs, as R-AUTHZ-004 does, means that a repository deleted and
created again under the same key is not cleaned by an old policy without anyone noticing. Retention
also has something authorization lacks, a repository-level exclude list, and there the same
binding would work against safety: an excluded repository that is deleted and re-created would
lose its protection while a key pattern in the include list still covers it. Exclusions are
therefore matched by key. The syntax and the code stay shared; only the binding of an exclusion
differs. The development agent would revisit this only if authorization.md changes its matcher,
and retention would then follow automatically.

**Applied in the spec:** R-STOR-038 now defines the scope through R-AUTHZ-004 selectors
(`@any-local` for all; other class selectors `422`; include key selectors stored by repository ID,
with inert policies; exclude key selectors matched by key and kept when the repository is deleted)
and R-AUTHZ-005 name and path patterns, including each format's case rule, with an acceptance
criterion for a re-created excluded repository. R-STOR-043 uses the same name glob for build names.
Converging the routing patterns of R-REPO-005 and ADR-037 on the same matcher is a cross-page
consequence.

### Q-STOR-10 · Storage maintenance jobs and the storage summary: one task framework, one path

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

This page specified its own run API for storage jobs: `/api/v1/maintenance/gc-runs`,
`…/orphan-sweeps`, `…/scrub-runs`, a `maintenance_runs` table, `storage.*.schedule` keys and
`/api/v1/storage/summary`. Meanwhile [architecture.md](architecture.md) (job table, scheduler,
checkpoints) and [operations-and-observability.md](operations-and-observability.md) (R-OPS-019,
R-OPS-020, R-OPS-023: `/api/v1/system/tasks/{name}/run`, `/api/v1/system/jobs`,
`maintenance.tasks.<name>`, `/api/v1/system/storage/summary` with its own refresh path) define
the same functions. [api-and-ui.md](api-and-ui.md) R-API-020 required exactly one path per
function. For STOR-039, the two summary requirements also disagreed on the per-repository share:
a percentage here, a ratio there.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Shared task framework | Storage jobs are tasks of R-OPS-019 on the job table of architecture.md, started only through the task endpoint and stopped only through job cancel. This page defines their semantics, parameters and results, and owns the read paths of their results (`/api/v1/storage/summary`, `/api/v1/storage/damaged-blobs`); R-OPS-023 owns the summary task and snapshot table | One scheduler, one run table, one stop mechanism, one job screen; exclusive enqueue, exactly-once slots, leases and checkpoints already exist | Storage-specific listings become `?kind=` filters on the generic job list |
| (b) Storage keeps its own API | `/api/v1/maintenance/*` endpoints, `maintenance_runs` and `storage.*.schedule` stay; the operations page points here | Storage-tailored endpoints | Storage becomes the only subsystem with a second scheduler configuration, a second run table and a second stop API; the job model's guarantees are rebuilt |
| (c) Both | Storage endpoints as aliases over framework jobs | Familiar paths for either reader | Two paths per function, contrary to R-API-020's acceptance criterion |
| (d) (a) plus read-only domain views of runs | Start and stop as in (a); storage adds `GET /api/v1/storage/gc-runs` and similar listings that reference the jobs, as [api-and-ui.md](api-and-ui.md) R-API-015 and Q-API-9 permit | Storage-shaped history (filters on reclaimed bytes, per-run counts as columns) without a second start or stop path | A second listing of the same runs to build, paginate and authorize; the job list already filters by `kind` and `state` and carries each run's result; no MVP screen needs more |

**Choice:** (a) — storage jobs are tasks of the shared framework (`gc`, `gc-full`,
`orphan-sweep`, `scrub`, `storage-summary`, `remote-cache-cleanup`) with one start and one stop
path each; storage keeps only the read paths of its results.

**Rationale:** The job table already offers everything storage jobs need: `exclusive` dedup keys
(one collection run at a time across `gc` and `gc-full`), `(task, scheduled_for)` slots that
enqueue exactly once, leases, `checkpoint` for resumable sweeps, and cancel at the next checkpoint.
The scheduler already reads `maintenance.tasks.<name>`. Under (b) one developer would build and
test all of that twice. Collection needs two tasks rather than one task with a `mode`, because
R-OPS-019 gives each task exactly one schedule and the incremental and full runs need different
ones; the shared dedup key keeps them from overlapping, and a slot it refuses is offered again
on the next scheduler tick, so the weekly sweep is postponed, never lost. (d) is the alternative
that Q-API-9 leaves open; it adds a second listing without adding information, and it can be added
later without moving any path, so the prototype does without it. The STOR-039 share is fixed as a
ratio of logical bytes: object storage reports no capacity, and physical bytes cannot be split
across repositories because deduplication shares them. Retention policy runs are not covered by
this decision: they are per-policy resources with their own history and report (R-STOR-041,
R-STOR-042), which R-API-020 explicitly keeps on their resource paths, while their execution uses
the same job queue. The development agent would revisit this if a storage job needs something the
job model cannot express. Resuming an orphan sweep across runs fits it, through the checkpoint of
the last incomplete job.

**Applied in the spec:** R-STOR-025 uses tasks `gc` and `gc-full` with a shared exclusive key,
states how a slot refused by that key is enqueued later, triggers and observes runs through the
task and job API, uses `maintenance.tasks.*` schedules, drops the `maintenance_runs` table, and
its acceptance criteria now hold in weeks with a full sweep. R-STOR-040 registers retention runs
with one attempt, so a lost worker fails the run as specified instead of the reaper re-queueing
it. R-STOR-026 uses task `orphan-sweep`, resumes from the last
incomplete job's checkpoint, and rejects a `minAge` not above twice the commit deadline. R-STOR-027
uses task `scrub`, moves the damaged-blob API to `/api/v1/storage/damaged-blobs` and defines the
damage counter and gauge. R-STOR-028 keeps the read path `GET /api/v1/storage/summary`, starts
recomputation only through the task, and defines the figures, `shareOfLogicalBytes` as a 0–1
ratio, and the listed repositories. R-STOR-045 uses task `remote-cache-cleanup`. The GC
metrics are renamed to the `trove_gc_*` family of R-OPS-008, and the configuration reference is
updated. The matching edits on the operations, API and HA pages are cross-page consequences.

### Q-STOR-11 · Multi-request uploads across replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-12 and INV-18 (owner constraints of 2026-10-28) send the requests of one upload session to
random replicas without affinity, and a node drain may end any request. R-STOR-011 staged each
chunk as an object and assembled the chunks at finalize. The OCI distribution specification
requires chunks in order, answers an out-of-order chunk with `416`, lets a client read the
current offset with `GET`, and lets a registry announce a minimum chunk size
(`OCI-Chunk-Min-Length`, which clients SHOULD honour). S3 requires every part except the last to
have at least 5 MiB, and consecutive part numbers when parts carry checksums.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Staged chunk objects, assembled at finalize | The earlier R-STOR-011: each chunk becomes an object; finalize assembles them with `UploadPartCopy` when every chunk but the last has 5 MiB, otherwise by streaming all chunks through the finalizing replica | Simple chunk requests; no part bookkeeping | With small chunks the finalize request streams the whole blob through one replica, so its duration grows with the blob and a drain cuts it, losing the assembly; a client that streams the whole blob in one `PATCH` loses everything when that request is cut |
| (b) One S3 multipart upload per session, parts of a fixed size, a bounded tail, progress in PostgreSQL | Each request appends to the session's multipart upload in parts of `part_size`, records every part, hash state and offset in PostgreSQL under a writer claim, and keeps fewer than `part_size` leftover bytes as a tail object that it writes under its own key and records with its SHA-256; finalize completes the upload and copies it server-side to the content address | Per-request work is bounded by the chunk plus one part; a long streamed request records progress at every part, so a drain loses at most one part and the client resumes on any replica; finalize moves no byte through trove; a stale writer can at worst fail the session, never corrupt it, because `CompleteMultipartUpload` checks every part's ETag and the stale writer's tail lands under a key the session never records | A part table and a writer claim; small chunks rewrite the tail, up to one part per request; one server-side copy at finalize |
| (c) Session affinity | Route all requests of a session to one replica and keep the bytes in its spool | No staging in S3 | Contradicts INV-12 and INV-18 (no affinity); a drain or crash loses the session; the spool must hold whole blobs |
| (d) Session state in Valkey, bytes in S3 | Hash state, offset and part list in Valkey | No PostgreSQL write per part | A Valkey failover or flush loses every open session and costs its client a complete re-upload, more than the one retry [architecture.md](architecture.md) R-ARCH-032 allows for state in Valkey; part lists exceed R-ARCH-030's 1 KiB value limit |

**Choice:** (b) — one S3 multipart upload per session with parts of `storage.uploads.part_size`,
a tail object below one part, and the session's offset, hash state and parts recorded in
PostgreSQL under a writer claim.

**Rationale:** The owner's drain constraint is what separates the options. Under (a), the request
that does the most work, the finalize, grows with the blob, and the common client behaviour of one
streamed `PATCH` per blob puts the whole blob into one cuttable request. Under (b), no request
holds more than one part of unrecorded work, and the OCI protocol already has the resume path
(`GET` for the offset, `416` for a wrong one), so a drain costs one part and one retry. (c) is
excluded by the owner's constraint, and (d) by INV-3, because a lost session is not a performance
cost but a lost upload. The writer claim keeps two requests from writing one session; the ETag
check of `CompleteMultipartUpload` turns the remaining race, a writer that lost its claim and
still uploads a part, into a failed session instead of a wrong blob. The tail has no such check
in S3, because it is a plain object, so each request writes its own tail object and the session
records which one counts, with its SHA-256; a stale writer's tail is never read. The final digest
comes from the recorded hash states, not from re-reading the object, which is why every byte that
enters a part must be the byte that was hashed. A drain must not leave the claim behind either: a
client that meets `416` for as long as a cut request's claim lasts may exhaust its retries, so a
cut request releases its claim, and only a crash leaves one to expire. The development agent
would revisit this if a supported store's server-side copy of a 5 GiB blob takes longer than the
`web` grace period, which would make every drain during a large finalize cost a retry.

**Applied in the spec:** R-STOR-011 is rewritten: state, claim, parts, tail (one object per
writing request, checked by its recorded SHA-256 on read-back), finalize steps (the change to
`completing` takes the claim; overlapping finalizes converge), resumption, expiry (every request
enforces `session_max_age`), the `upload_sessions` and `upload_session_parts` tables, configuration
and acceptance criteria. R-STOR-004 step 8 releases a cut request's claim and keeps the session's
multipart upload. R-STOR-001 names the staging keys, R-STOR-006 and R-STOR-026 keep open sessions
out of reach of multipart expiry, R-STOR-025 lets collection runs abort expired sessions,
R-STOR-047 classes both tables `transient`, and the security notes bind a session to its
principal. Cross-page: [backup-and-restore.md](backup-and-restore.md) R-BAK-004 (initial
`transient` table list); [architecture.md](architecture.md) R-ARCH-032 (the chunked-upload entry
names the multipart upload and the part table) and R-ARCH-024 step 5 (a cut session request keeps
its recorded parts and releases its claim).

### Q-STOR-12 · Counters that several replicas update: usage, quotas and download statistics

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The load exceeds one replica (INV-12), so uploads, deletes and downloads of one repository arrive
on many replicas at once. Usage counters feed quotas (R-STOR-029) and the storage summary
(R-STOR-028); download statistics feed retention's `unusedForDays` (R-STOR-038). INV-3 now offers
Valkey for shared ephemeral state, on the condition that losing it costs performance only.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Append-only deltas in PostgreSQL, folded by every worker | Triggers append deltas; every `worker` replica folds them with `SKIP LOCKED`, updating counter rows in one order; readers, quota admission included, read the folded rows; download statistics are buffered per `web` replica and flushed in item order | No hot row on the write path, whatever the replica count; durable and identical for every replica; no lease, so folding scales with workers and survives drains; one primary-key read per quota admission, whatever the upload rate; independent of Valkey | Counters and quota decisions lag by one fold interval; a quota can be exceeded by what is committed in that time and by the content in flight |
| (b) One counter row per repository, updated in every commit | The commit transaction updates `repository_usage` directly | Exact at all times | Every upload to a repository serializes on one row until its commit; with many replicas this row becomes the bottleneck, the pattern R-ARCH-018 already forbids for change areas |
| (c) Counters in Valkey (`INCRBY`), persisted periodically | Writers count in Valkey; a job copies the totals to PostgreSQL | Fast, no PostgreSQL write | A failover with asynchronous replication loses increments, so quotas and the summary drift until a recount; a quota would then admit uploads beyond its limit after a Valkey incident, which is a correctness cost INV-3 rules out |
| (d) Per-replica buffers for usage too | Each replica sums its deltas in memory and flushes them | Fewer rows | A crash loses deltas permanently, so counters drift; between flushes each replica decides quotas on different numbers, which INV-18 rules out |

**Choice:** (a) — usage stays in append-only PostgreSQL deltas, folded by every `worker` replica
with `SKIP LOCKED` and a fixed update order, read in folded form by quota admission, and
recomputed after a backup restore; download statistics stay in per-replica buffers flushed in item
order and at shutdown.

**Rationale:** A quota is a limit operators rely on, so its counter must survive any Valkey
incident; that rules out (c), and (d) fails the same test for a crashed replica. (b) is exact but
puts every upload of a busy repository in one queue, which the owner's load assumption makes the
first bottleneck. (a) was already the design; the new constraints add that the fold must run on
any number of workers without a lease and survive a drain, which `SKIP LOCKED` and a fixed lock
order give, and that a restore must recompute the totals, because the backup does not export
unreferenced blob rows. Admission reads the folded rows only: adding the unfolded deltas would make
it exact, but their number grows with the upload rate of all replicas, so every admission would
read more rows as the installation grows, and the extra precision only narrows an overshoot that
in-flight uploads cause anyway. The fold interval drops from 30 s to 5 s, so the overshoot that
the lag adds stays at a few seconds of uploads, and a fold iteration is cheap enough to run that
often. Download statistics stay buffered: they are not audit data, a
crash loses
at most ten seconds of counts against `unusedForDays` measured in days, and the shutdown flush
makes a drain lose none. The development agent would revisit this if the fold falls behind under
the reference load (`storage_usage_deltas` growing for longer than ten fold intervals), which
would call for partitioning the deltas by repository.

**Applied in the spec:** R-STOR-003 (fold SQL, repeated iterations under backlog, the join that
keeps a deleted repository from blocking the fold, `storage_usage_totals` counting `live` rows,
reads of the folded rows, `storage_usage_recount()`, acceptance criteria with three workers and
with a repository deletion), R-STOR-015 (`web` role, item order, removed items dropped, shutdown
flush), R-STOR-024 (the physical delta at step 3), R-STOR-029 (PostgreSQL only, folded rows, one
primary-key read per limit), R-STOR-046, R-STOR-047, and the configuration reference
(`storage.usage.fold_interval` 5 s instead of 30 s). Cross-page:
[backup-and-restore.md](backup-and-restore.md) R-BAK-022 (the recount as a fix-up).

### Q-STOR-13 · Exclusion of storage maintenance runs: job key, fenced lease or Valkey lock

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

[ha-and-helm.md](ha-and-helm.md) R-HA-006 lists garbage collection, the orphan sweep and retention
runs as singleton work under a fenced advisory-lock lease, with every write on the lease's session.
This page, [architecture.md](architecture.md) R-ARCH-016 and Q-STOR-10 register the same work as
jobs with an exclusive dedup key, whose handlers write through the main pool. INV-3 keeps mutual
exclusion whose failure would corrupt data in PostgreSQL. With n workers and drains at any time
(INV-12), a run moves from one worker to another as a matter of routine.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Exclusive job key; correctness from row locks; a hand-over rule for S3 deletes | One run at a time through the job table; every step is a short transaction whose safety rests on row locks; a collector deletes objects only within a bound after its claim, and another run finishes a claim only after it | Exclusion in PostgreSQL without a session; runs work through a transaction-mode pooler; a drain releases the run with its checkpoint and hands its claimed rows over; the hand-over rule bounds the late-delete race, the one hazard that a run moving between workers creates | A new setting (`storage.gc.handover_delay`); a row abandoned by a crashed collector waits that long, and uploads of its digest answer `503` meanwhile |
| (b) A fenced advisory-lock lease, as R-HA-006 describes, in addition to the job | The handler holds a session lock; all its writes run on that session | A partitioned handler cannot commit after losing the lease | Collection's writes are already safe when two runs overlap, so the fence protects nothing in PostgreSQL that needs it; the dangerous late action is the S3 delete, which no database fence stops; the busiest maintenance writer moves onto session connections, outside the transaction-mode pooler |
| (c) A Valkey lock | `SET NX` with a TTL around each run | No PostgreSQL session | A failover loses the lock and starts a second run; while Valkey is unavailable the runs either stop or run unguarded; INV-3 keeps such exclusion in PostgreSQL |
| (d) No exclusion | Runs may overlap freely | Nothing to build | Duplicate work and duplicate result counts; overlapping scheduled slots |

**Choice:** (a) — storage maintenance runs are excluded only by the exclusive job key in
PostgreSQL; their safety rests on row locks, and S3 deletes follow the hand-over rule of
R-STOR-024. They take no advisory-lock lease and no Valkey lock.

**Rationale:** R-STOR-024's proof shows that two overlapping collectors cannot delete a referenced
blob, and the orphan sweep's claim insert and retention's delete path rely on row locks the same
way. Exclusion is therefore an efficiency property, and the job key provides it in PostgreSQL. The
one hazard that comes from runs moving between workers is a delete that a drained, paused or
partitioned collector sends late, after another run finished the row and an uploader stored the
digest again. A database fence would not stop that request; the hand-over rule does, except for a
process frozen between its deadline check and the write itself, which the scrub detects. The
rule's clock is the collector's own, read before the claim transaction, because comparing a
replica clock with the database's would let clock offset eat the margin. A drained collector is
not a lost one: it knows its calls have returned, so it hands its rows over at release and costs
an upload of the same digest seconds instead of `handover_delay`. (b) costs
session connections for no correctness gain, and (c) would make exclusion depend on Valkey's
availability. The development agent would revisit this if a storage task appears whose
correctness needs exclusion; that task would then take (b).

**Applied in the spec:** R-STOR-024 (hand-over rules with the collector's own clock, the hand-over
of unfinished claims at release, the end-of-run pass, `storage.gc.handover_delay`, relation to
other jobs, acceptance criteria with a paused and with a drained collector), R-STOR-005
(`blob_wait_timeout` above twice `delete_settle`), R-STOR-025 (queue, job key, drain), R-STOR-040
(dedup key, the run limit through `retention_run_gate`, drain, lost worker), R-STOR-041 (stop
through the job state), R-STOR-044 (append-only `SKIP LOCKED` outbox), R-STOR-046.
Cross-page: [ha-and-helm.md](ha-and-helm.md) R-HA-006 (garbage collection, the orphan sweep and
retention runs leave the list of lease holders) and R-HA-002 (its acceptance criterion identifies
the GC worker through `job.locked_by`, not `trove_lease_held`);
[architecture.md](architecture.md) R-ARCH-016 (registration table: queue `maintenance` for the
storage tasks, `default` for retention runs) and Q-ARCH-14 (its rationale no longer names
garbage-collection decisions as what the lease fence protects).

### Q-STOR-14 · Object-store settings the outbound layer does not yet support, and plaintext endpoints

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse every key without its outbound option at start-up | `storage.s3.ca_file`, `http.max_connections`, `http.tls_handshake_timeout`, `http.idle_conn_timeout` and `http.proxy_credentials_file` fail the configuration until the outbound layer supports them | Nothing is silently ignored | Five keys that the schema lists are unusable until the outbound work lands |
| (b) Accept every key and ignore the unsupported ones with a warning | The keys load; the warning names each | Keys usable at once | An operator believes a setting is applied when it is not |
| (c) Accept the keys, refuse the one whose effect is a security decision, and let the others take the outbound defaults, stated in the code (chosen) | `storage.s3.ca_file` is refused with an error naming the fix (add the CA to `proxy.outbound.tls.ca_files`). `max_connections`, `tls_handshake_timeout` and `idle_conn_timeout` take the factory's defaults; the S3 client does not read `proxy_credentials_file` yet. | A trust-store choice is never taken silently; the timeouts and limits stay usable | The three timeouts and the connection limit are not configurable until the outbound options exist |

Plaintext endpoints (chosen with (c)): the endpoint rules of `config.Storage.Validate` apply, and an `http://` endpoint
needs `storage.s3.allow_insecure_endpoint: true`. The development profile and the single-node CI values set it.

**Choice:** (c), with the plaintext rule above.

**Rationale:** The one setting that changes which authority a connection trusts (`ca_file`) must fail loudly, because a
silent fallback to the system roots would verify against a different store than the one configured. The limits and
timeouts have a sane factory default, so a refusal would only block the operator for no safety gain. The development
agent would revisit (c) when the outbound layer gains `WithRootCAFile` and the remaining options, because then the keys
are applied rather than defaulted.

**Revisited in M0 (2026-10-29):** the outbound layer gained the trust store of a single client
(`outbound.TrustStore`), so `storage.s3.ca_file` is supported and replaces the system roots for the object-store client,
reloaded like every other bundle ([blob/s3/client.go](../../internal/blob/s3/client.go)). The option (c) reasoning holds
for `proxy_credentials_file`, which is still refused with an error naming the reason, and for the timeouts and the
connection limit, which still take the factory's defaults.

**Applied in the spec:** R-STOR-033 names the metric label `operation` (replacing `op`, Q-OPS-26) in its metrics line,
and the acceptance criterion of R-STOR-005 that reads the same counter uses `operation` too; no other requirement text changed
except R-STOR-030, which now states that `ca_file` is supported.

### Q-STOR-15 · Object-store endpoint without a scheme

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Refuse it | `storage.s3.endpoint` must carry `https://` or `http://` | One form | The bucket provisioner writes the endpoint as a host without a scheme, so the chart would have to compose a URL from the Secret value |
| (b) A host without a scheme means `https://`; plaintext needs `http://` and `allow_insecure_endpoint` (chosen) | The value is normalised before validation and written back at load | The provisioner's value works unchanged; the secure form is the default one; a plaintext store is still spelled out | A typo such as `htp://host` is not a scheme-less host: it contains `://` and is refused as an unsupported scheme |
| (c) A scheme-less host means `http://` when `allow_insecure_endpoint` is set | The flag decides the scheme | Short for development | The flag would change what the value means, and a host that is meant as `https` silently goes plaintext |

**Choice:** (b).

**Rationale:** TLS is the rule for every connection (INV-20), so the shorter spelling must be the secure one. The
rewrite happens once, at load, so the client, the validation and the effective-configuration view agree on the same
URL. `storage.s3.credentials.sts_endpoint` is not rewritten: it is not a store address a provisioner writes. The
development agent would revisit this if a provisioner appears whose host values already include a scheme or a path.

**Applied in the spec:** R-STOR-030 (validation bullet and the configuration block).

### Q-STOR-16 · What the spool may delete at start

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The spool directory is scratch space of one pod. Its files are removed at start, and the directory may be a volume that
holds more than the spool wrote.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Empty the whole directory | every entry is removed | simplest; the directory is always clean | a misconfigured `storage.spool.dir` pointing at shared data deletes that data |
| (b) Remove only the regular files named by a UUID; refuse the start on any other entry (a `lost+found` directory is tolerated) | the spool's own files, nothing else | a wrong path fails before anything is deleted | a directory with any other content stops the replica until someone looks |
| (c) Remove the files named by a UUID and leave other entries alone | a lenient delete | the replica starts | a foreign entry stays in a scratch directory that is meant to be empty |

**Choice:** (b).

**Rationale:** The spool directory is configured by path, and a deletion at start is irreversible. The rule removes exactly
the files the spool creates, which are regular files with random UUID names and mode `0600`, and it refuses to act on a
directory that is not the spool's. The development agent would revisit this only if a supported volume puts entries of its
own into the spool directory, which the chart does not do (an `emptyDir` or an ephemeral volume).

**Applied in the spec:** [R-STOR-004](#r-stor-004--upload-pipeline-admission-spool-and-single-pass-hashing) (the start rule of
the spool, the bullet after the restart rule); the implementation is `NewSpool` in [internal/blob/spool.go](../../internal/blob/spool.go).

### Q-STOR-17 · Replacing a file without overwrite answers 403

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

The error row of R-STOR-007 lists 409 for "an existing item and no `overwrite` permission"; R-AUTHZ-006, R-AUTHZ-016 and R-GENERIC-004 say 403.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 409 `conflict` | The refusal is a conflict with the existing item | As the R-STOR-007 row says | Contradicts R-AUTHZ-016 ("a write-type request without a required action: 403, naming the missing action") and the acceptance criteria of R-AUTHZ-006 and R-GENERIC-004 |
| (b) 403 `forbidden` naming `overwrite` (chosen) | A missing action is a missing permission | One rule for every refused write; a client sees which action to ask for | The row of R-STOR-007 changes |

**Choice:** (b). A replacement with different bytes by a caller who holds `deploy` and `overwrite` answers 201; an identical re-upload answers 200 and needs `deploy` only.

**Rationale:** Three pages and two acceptance criteria say 403. The development agent would revisit this if the 409 of the row was meant for a format rule, which the generic format has none of.

**Applied in the spec:** R-STOR-007 (the error row).

### Q-STOR-18 · Implicit folders are rows that go with their last child, serialised by row locks

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-STOR-003 says implicit folders are created as parents of a file and removed when their last child goes; a deploy and a delete of the last child can overlap.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Folders are derived from the paths | No folder rows; a listing groups the paths | Nothing to keep in step | Explicit empty folders, folder properties and the folder count of the usage fold need rows; a listing is a scan |
| (b) Folder rows, created with the file and removed with the last child (chosen) | A deploy inserts the missing ancestors (ON CONFLICT DO NOTHING) and holds its parent FOR SHARE until it commits; a delete locks the folder FOR UPDATE and looks for children in a statement of its own | A listing is an index range; the usage counters and explicit folders work; the lock order cannot leave a child without its folder | Every deploy takes one more row lock; a deploy can find its parent gone and repeats the step (bounded) |

**Choice:** (b).

**Rationale:** The integration test `TestConcurrentWritersOnOnePath` runs deploys and deletes of the same folders on two replicas and finds no item without its parent folder. The development agent would revisit this if the share lock on a hot folder shows up as contention.

**Applied in the spec:** R-STOR-003 reads unchanged; the protocol is in [docs/developer/files.md](../developer/files.md).

### Q-STOR-19 · A delete is permanent in M1, one event per file, a large subtree by the job items-delete

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-STOR-018 deletes into the trash can where it is enabled (P1, R-STOR-022), writes the format hook in the same transaction, and removes a subtree above `storage.delete.max_sync_items` by a background job after the permission check.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The job re-checks the permission per batch | The job decides `delete` again for every item it removes | No item is removed that the caller may not delete at that time | The job has no caller: the authorization state of the request would have to be rebuilt in the worker |
| (b) The check completes before the job is enqueued, the job removes what it finds (chosen) | The request checks every item of the subtree (403 and nothing changes otherwise); the job lists and removes in batches | R-STOR-018 as written ("the permission check still completes before the first removal") | An item a concurrent deploy adds below the folder between the check and the job is removed unchecked (H-154) |

**Choice:** (b). Without the trash can an item is deleted for good and its blob goes to the collection through the statement trigger. A file produces one `artifact.deleted` event in the transaction that removes it.

**Rationale:** The synchronous path checks and removes the same rows in one transaction, so it has no such window. The development agent would revisit this when the trash can exists (the delete then moves rows and the window is harmless).

**Applied in the spec:** R-STOR-018 reads unchanged; [docs/developer/files.md](../developer/files.md).

### Q-STOR-20 · A listing scans a bounded number of rows and its cursor is the last row scanned

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-STOR-017 omits the entries the caller may not read and pages with a cursor; a caller who may read little of a huge folder would otherwise make one request scan it all.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Filter in SQL | Push the grant into the query | Exact pages | The grant index is memory-only and has no SQL form (R-AUTHZ-015) |
| (b) Scan up to 100,000 rows per page and return a short page with a cursor (chosen) | The cursor is the path of the last row the page scanned | Bounded work per request; a client continues while `nextCursor` is not null | A page can hold fewer entries than `limit` while more follow |

**Choice:** (b). The default page size is 1,000 and the maximum 10,000, as R-STOR-017 says; they are not the general limits of the other lists.

**Rationale:** The decision per entry reads memory only, so the bound is on rows read from PostgreSQL. The development agent would revisit the figure if a real folder needs more.

**Applied in the spec:** R-STOR-017 reads unchanged; the operation `listChildren` describes it. What the cursor holds when the page ends on a row the caller may not read, and how many rows one query reads, are Q-STOR-62.

### Q-STOR-21 · A folder addressed without its trailing slash is redirected, a download counts after its body was sent

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-STOR-012 says a GET on a folder returns the directory index, whose links are relative to the folder; R-STOR-015 counts a download when trove answers 200, or 206 from byte 0.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Serve the index at both URLs | No redirect | One request | The relative links of the HTML index point one level too high |
| (b) 301 to the URL with the slash; count after the whole response body was copied (chosen) | The index lives at the URL with a slash; the counter is called when the copy ended without error | Links work; a cut or aborted transfer is no download | A client that stops reading after the first bytes of a range from 0 is not counted |

**Choice:** (b).

**Rationale:** Both are what a file server and a statistics counter of record do. The development agent would revisit the counting rule if the audit of downloads needs the cut ones.

**Applied in the spec:** R-STOR-012 and R-STOR-015 read unchanged.

### Q-STOR-62 · A listing that ends on a hidden row: its cursor and its cost

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Under Q-STOR-20 a page that reached the scan limit returned the path of the last row it scanned as its cursor. That row may be
one the caller may not read, and a cursor is base64 JSON, so the client could decode a hidden name (R-AUTHZ-016). A query read
`limit + 1` rows, so `limit=1` on a folder of 100,000 hidden rows cost 50,000 queries.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The path of the last row scanned | As before | Exact resumption | Names a hidden row |
| (b) A sealed cursor | Encrypt the path with a key every replica holds | Reveals nothing | No such key exists; it would be one more secret to distribute and rotate |
| (c) The item's id (chosen) | The cursor holds the numeric id of the last row scanned; the next page reads its path in the repository below the listed folder and continues after it; a purged row is 400 `invalid_cursor` | No name leaves the server; any replica resumes | A forged id tells where that item sorts among the visible entries (H-208) |
| (d) The last visible entry | Resume there | Reveals nothing | Every page scans the hidden run again, and a run longer than the scan limit never ends |

**Choice:** (c) when a page ends at the scan limit beyond its last visible entry; every other page keeps the path of its last
entry, which the caller may read. A query reads `limit + 1` rows and doubles that after each query that left the page short, up
to 5,000, so a page costs a few dozen queries at most (2 + 4 + … + 4096 rows, then the rest of the 100,000 in chunks of 5,000).

**Rationale:** the scan limit exists for callers who see little of a folder; for them the cursor must not carry what they cannot
see, and the cost of a page must not depend on its size. The development agent would revisit (b) when a cluster-wide key for
other purposes exists.

**Applied in the spec:** none; R-STOR-017 and Q-STOR-20 read unchanged apart from the pointer to this decision.

### Q-STOR-143 · The delete job removes only the items its permission check saw

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-STOR-018 decides `delete` for every item of a subtree larger than `storage.delete.max_sync_items` at the request and removes in a
job. The job listed what it found, so a file that a holder of `deploy` added below the folder between the check and the batch that
reached it was removed without a decision (H-154).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep it as a documented gap | H-154 as written | No change | A principal without `delete` loses a file to another's delete |
| (b) Decide again per batch for the stored subject | The job carries the subject and calls `PathsAllowed` for every batch | Also follows a grant that was removed meanwhile | A job has no request: the subject, its token scope and the endpoint ceiling would have to be stored and rebuilt; a refused batch leaves a half-deleted subtree |
| (c) Fix the set at check time (chosen) | The job carries the highest item id the check saw (`DeleteArgs.MaxID`) and removes only items up to it | One integer; the set that was decided is the set that goes; no subject in a job | An item with a lower id that is committed after the check's snapshot (an insert in flight at that moment) is still removed |

**Choice:** (c). The folders go the same way; a folder that still has a child (the late file) stays, with the ancestors above it.

**Rationale:** the grant that applied at the request is the one the caller was told about, and the job is the continuation of that
request. Item ids come from one sequence, so everything inserted after the check has a higher id. The development agent would
revisit it if a trash can (R-STOR-022) made the window recoverable, or if the in-flight insert has to be closed too (a snapshot
horizon stored with the job).

**Applied in the spec:** R-STOR-018 above; [file-transfer.md](../security/file-transfer.md) H-154 is narrowed to the in-flight insert.

### Q-STOR-144 · A deploy locks the whole ancestor chain, and `overwrite` is settled before the commit transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Two defects of the link step of a deploy (R-STOR-005, R-STOR-007, R-STOR-017). It held the parent folder `FOR SHARE` and checked that
the ancestors are folders, not that they exist, so a delete of the subtree that removes the emptied ancestors while the deploy runs
could leave a file below folders that are gone. And when a concurrent upload created the path after the checks, the link step decided
`overwrite` inside the commit transaction, where a decision that waits for the index holds the blob row lock and the item lock.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Lock the parent only, as before | | One lock | The grandparents are not protected |
| (b) Lock every ancestor `FOR SHARE`, deepest first, require all present (chosen) | `LockFoldersShared` after `ensureParents`; a missing one repeats the round | A delete waits for the deploy at every level and finds the child | One statement with as many rows as the path has folders |
| (c) Lock them top down | | Natural reading order | The delete removes folders deepest first and would meet it in the opposite order: a deadlock |
| (d) Decide `overwrite` in the transaction | As before | No extra decision | A wait inside the transaction |
| (e) Settle `overwrite` before the commit (chosen) | `PathsAllowed` for `overwrite` beside the commit-time check, without an audit event; the link step returns `errOverwriteDenied`, and the refusal is then decided and audited outside | No decision inside the transaction | One more evaluation per deploy that did not see the path |

**Choice:** (b) and (e). The `overwrite` evaluation is `PathsAllowed` of the same index family as the commit-time check, not the very
index `Begin` returned (that call has no non-auditing form); the two evaluations are one request apart.

**Rationale:** the lock order of a delete is the one a deploy must follow; the deterministic test holds the lock of a folder two levels
above the file and removes the chain under it. The development agent would revisit the index identity when `DecideOn` gets a
non-auditing form.

**Applied in the spec:** none; R-STOR-017 and R-STOR-007 read unchanged.

### Q-STOR-145 · A repository's `maxUploadSize` is a validated integer and fails closed

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`config.storage.maxUploadSize` (R-STOR-004) was read into an `int64` at deploy time, and the document check only required `storage` to
be an object. `1e9` was accepted and decoded to zero, which is "no limit"; the limit failed open.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Validate at write only | 422 `invalid_value` at `/storage/maxUploadSize` for anything but a non-negative integer literal | The error is where the mistake is made | A stored document that predates the rule still reads as none |
| (b) Validate at write, refuse at use (chosen) | (a), and a stored value that is not an integer refuses the upload with 500 | No path ends in "unlimited" | An old bad document blocks uploads until corrected |

**Choice:** (b). The literal form is digits only (`1e9`, `1.5`, `-1`, `"10"`, `null` are refused); it must fit an int64.

**Rationale:** a limit someone set and mistyped is not a limit they meant to lift. The development agent would revisit it if the
management API gains a migration that rewrites such documents.

**Applied in the spec:** R-STOR-004 reads unchanged; the same change makes deleting in a virtual repository `405` in code, as R-STOR-018 already says.

### Q-STOR-146 · A remote fill is an item with its upstream URL and no `artifact.deployed` event

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: the proxy stores the artifact of a remote repository as an item of that repository (R-REPO-025) through the item store's one commit
(Q-ARCH-207). `artifact.deployed` is specified as "a file was stored in a local repository by any upload path" (R-OPS-014), and the cache event
`artifact.cached` is webhook-only and raised by the proxy.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The commit writes `artifact.deployed` for every origin | One audit row per commit | One rule | A cache fill would claim an upload that nobody made, and the audit trail would hold one row per cached tarball |
| (b) Only an upload writes it; a fill stores `upstream_url` on the item and the proxy raises its own event (chosen) | The audit row is part of the commit for `OriginUpload` | The event means what R-OPS-014 says; the item still names where its bytes came from | The proxy owns the fill's event |

**Choice:** (b). The `upstream_url` column of the item is written by the insert and the replace of a file and read into `items.Item.UpstreamURL`;
the item description (`upstreamUrl`) shows it for a cached item and `null` for an uploaded one. The store removes any user info from the URL before it stores or reports it.

**Rationale:** an audit trail records what a person or a credential did. The development agent would revisit it if an audit requirement for
fills appears.

**Applied in the spec:** R-STOR-016 (the description's `upstreamUrl`) reads unchanged.

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

Component: the spool admission of R-STOR-004 on every `web` replica. Subject: every principal that
uploads. The spool budget is per replica and shared by all principals. A single token with
`deploy` can hold many slow concurrent uploads and exhaust the budget, making every other upload
on that replica fail with `503` until the requests end or are cut. The Service spreads the
attacker's connections over all `web` replicas (INV-12), so the same token can exhaust every
replica's budget; adding replicas raises the number of connections needed, not the protection.
The cluster-scope limiter of [architecture.md](architecture.md) R-ARCH-034 counts requests per
second, not bodies in flight, so it does not bound this. Live in the MVP for every principal with
`deploy` on any repository; it becomes real where upload clients are not all trusted. Mitigation
outside trove: per-client connection limits and request-body timeouts at the edge proxy
([ha-and-helm.md](ha-and-helm.md) R-HA-039). A per-principal limit on bodies in flight would have
to count across replicas and release the slots of a crashed replica; it is not specified on this
page.
