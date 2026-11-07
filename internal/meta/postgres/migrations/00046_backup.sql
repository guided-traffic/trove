-- trove:phase expand
--
-- The backup lane (R-BAK-009, R-BAK-010, R-BAK-007, R-STOR-047).
--
-- backup_run is the record of one create, delete or verify run: its state, its lease, its garbage-collection hold and its
-- result. The partial unique index admits one active target-writing run per installation (Q-BAK-4); the database decides,
-- not Kubernetes. backup_blob_pin holds the to-copy set of a running create while its hold is 'pinned' (R-BAK-007). A pin
-- is not a reference: sha256 has no foreign key to blobs, so a pin never holds a blob row against the collector beyond the
-- run's lease. Both tables are export class transient (R-BAK-004): the target, not the database, is the record of backups
-- (tables_backup.go).
--
-- hold and lease_expires_at are read by the collector's per-blob transaction (R-STOR-024 step 2b), which is why the hold
-- index is partial: a run with hold 'none' is never looked at.

CREATE TABLE backup_run (
    id               uuid        PRIMARY KEY,
    operation        text        NOT NULL CHECK (operation IN ('create', 'delete', 'verify')),
    backup_id        text,
    state            text        NOT NULL CHECK (state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning',
                                                           'completed', 'completed_with_warnings', 'failed', 'interrupted',
                                                           'abandoned')),
    hold             text        NOT NULL DEFAULT 'none' CHECK (hold IN ('all', 'pinned', 'none')),
    trigger          text        NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    target           text        NOT NULL,
    pod              text        NOT NULL,
    trove_version    text        NOT NULL,
    snapshot_at      timestamptz,
    progress         jsonb       NOT NULL DEFAULT '{}',
    result           jsonb,
    error            jsonb,
    started_at       timestamptz NOT NULL DEFAULT now(),
    heartbeat_at     timestamptz NOT NULL DEFAULT now(),
    lease_expires_at timestamptz NOT NULL,
    finished_at      timestamptz,
    -- Only a create run holds blobs back from collection.
    CHECK (hold = 'none' OR operation = 'create'),
    -- A finished run holds nothing and has finished.
    CHECK (state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning') OR (hold = 'none' AND finished_at IS NOT NULL))
);

CREATE UNIQUE INDEX backup_run_one_active ON backup_run ((1))
    WHERE operation IN ('create', 'delete')
      AND state IN ('starting', 'snapshot', 'copying', 'finalizing', 'pruning');
CREATE INDEX backup_run_hold ON backup_run (hold) WHERE hold <> 'none';
-- The list endpoint pages newest first (R-API-050); the history prune and the gauges read by finish time.
CREATE INDEX backup_run_started ON backup_run (started_at DESC, id DESC);

CREATE TABLE backup_blob_pin (
    run_id  uuid   NOT NULL REFERENCES backup_run (id) ON DELETE CASCADE,
    sha256  bytea  NOT NULL CHECK (octet_length(sha256) = 32),
    size    bigint NOT NULL CHECK (size >= 0),
    PRIMARY KEY (sha256, run_id)
);
CREATE INDEX backup_blob_pin_run ON backup_blob_pin (run_id, sha256);

-- storage_usage_recount() recomputes the folded usage counters from items and blobs and empties the deltas, in the
-- caller's transaction (R-STOR-003, R-STOR-047). It runs only where no writer runs: as a fix-up of a backup restore, whose
-- blobs filter drops rows that the exported physical totals still count. Triggers do not fire for its own writes to the
-- counter tables, and the deltas are removed with the recount, so no row is counted twice.
CREATE FUNCTION storage_usage_recount() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM storage_usage_deltas;
    DELETE FROM repository_usage;
    INSERT INTO repository_usage (repo_id, logical_bytes, files, folders)
    SELECT i.repo_id,
           coalesce(sum(CASE WHEN i.kind = 'file' THEN i.size ELSE 0 END), 0),
           count(*) FILTER (WHERE i.kind = 'file'),
           count(*) FILTER (WHERE i.kind = 'folder')
      FROM items i
     WHERE i.trashed_at IS NULL
     GROUP BY i.repo_id;
    UPDATE storage_usage_totals
       SET physical_bytes = (SELECT coalesce(sum(b.size), 0) FROM blobs b WHERE b.state = 'live'),
           blobs          = (SELECT count(*) FROM blobs b WHERE b.state = 'live'),
           updated_at     = now()
     WHERE id = 1;
END $$;
