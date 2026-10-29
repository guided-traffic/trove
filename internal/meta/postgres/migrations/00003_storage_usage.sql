-- trove:phase expand
--
-- Usage counters (R-STOR-003, Q-STOR-12). Writers never update a shared counter
-- row: statement-level triggers on items and blobs append to storage_usage_deltas
-- and the worker fold loop sums the deltas into repository_usage and
-- storage_usage_totals.

CREATE TABLE repository_usage (
    repo_id        bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
    logical_bytes  bigint NOT NULL DEFAULT 0,
    files          bigint NOT NULL DEFAULT 0,
    folders        bigint NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE storage_usage_totals (
    id             smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    physical_bytes bigint NOT NULL DEFAULT 0,
    blobs          bigint NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now()
);
INSERT INTO storage_usage_totals (id) VALUES (1);

-- No index besides the primary key: the fold takes rows in id order, and no
-- request reads them. repo_id has no foreign key: the fold drops the deltas of a
-- repository deleted meanwhile.
CREATE TABLE storage_usage_deltas (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id        bigint,
    logical_bytes  bigint NOT NULL DEFAULT 0,
    files          bigint NOT NULL DEFAULT 0,
    folders        bigint NOT NULL DEFAULT 0,
    physical_bytes bigint NOT NULL DEFAULT 0,
    blobs          bigint NOT NULL DEFAULT 0
);

-- Untrashed rows count. A file adds its size and one file, a folder one folder.
CREATE FUNCTION items_usage_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_usage_deltas (repo_id, logical_bytes, files, folders)
    SELECT n.repo_id,
           sum(CASE WHEN n.kind = 'file' THEN n.size ELSE 0 END),
           count(*) FILTER (WHERE n.kind = 'file'),
           count(*) FILTER (WHERE n.kind = 'folder')
      FROM new_rows n
     WHERE n.trashed_at IS NULL
     GROUP BY n.repo_id;
    RETURN NULL;
END $$;

CREATE FUNCTION items_usage_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_usage_deltas (repo_id, logical_bytes, files, folders)
    SELECT o.repo_id,
           -sum(CASE WHEN o.kind = 'file' THEN o.size ELSE 0 END),
           -count(*) FILTER (WHERE o.kind = 'file'),
           -count(*) FILTER (WHERE o.kind = 'folder')
      FROM old_rows o
     WHERE o.trashed_at IS NULL
     GROUP BY o.repo_id;
    RETURN NULL;
END $$;

-- An update subtracts the old rows and adds the new ones; an update that
-- changes nothing the counters see (a property touch) nets to zero and appends
-- no row.
CREATE FUNCTION items_usage_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_usage_deltas (repo_id, logical_bytes, files, folders)
    SELECT d.repo_id, sum(d.logical_bytes), sum(d.files), sum(d.folders)
      FROM (
            SELECT o.repo_id,
                   -(CASE WHEN o.kind = 'file' THEN o.size ELSE 0 END) AS logical_bytes,
                   -(CASE WHEN o.kind = 'file' THEN 1 ELSE 0 END)      AS files,
                   -(CASE WHEN o.kind = 'folder' THEN 1 ELSE 0 END)    AS folders
              FROM old_rows o WHERE o.trashed_at IS NULL
            UNION ALL
            SELECT n.repo_id,
                   (CASE WHEN n.kind = 'file' THEN n.size ELSE 0 END),
                   (CASE WHEN n.kind = 'file' THEN 1 ELSE 0 END),
                   (CASE WHEN n.kind = 'folder' THEN 1 ELSE 0 END)
              FROM new_rows n WHERE n.trashed_at IS NULL
           ) d
     GROUP BY d.repo_id
    HAVING sum(d.logical_bytes) <> 0 OR sum(d.files) <> 0 OR sum(d.folders) <> 0;
    RETURN NULL;
END $$;

CREATE TRIGGER items_usage_ins AFTER INSERT ON items
    REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION items_usage_insert();
CREATE TRIGGER items_usage_del AFTER DELETE ON items
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION items_usage_delete();
CREATE TRIGGER items_usage_upd AFTER UPDATE ON items
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION items_usage_update();

-- The physical totals count live blob rows: a row inserted as live adds, a row
-- leaving live (R-STOR-024 step 3) subtracts, and a row inserted as deleting
-- (the orphan sweep, R-STOR-026) never counts.
CREATE FUNCTION blobs_usage_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_usage_deltas (repo_id, physical_bytes, blobs)
    SELECT NULL, sum(n.size), count(*) FROM new_rows n WHERE n.state = 'live'
    HAVING count(*) > 0;
    RETURN NULL;
END $$;

CREATE FUNCTION blobs_usage_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_usage_deltas (repo_id, physical_bytes, blobs)
    SELECT NULL, -sum(o.size), -count(*) FROM old_rows o WHERE o.state = 'live'
    HAVING count(*) > 0;
    RETURN NULL;
END $$;

CREATE FUNCTION blobs_usage_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO storage_usage_deltas (repo_id, physical_bytes, blobs)
    SELECT NULL, sum(d.physical_bytes), sum(d.blobs)
      FROM (
            SELECT -o.size AS physical_bytes, -1 AS blobs FROM old_rows o WHERE o.state = 'live'
            UNION ALL
            SELECT n.size, 1 FROM new_rows n WHERE n.state = 'live'
           ) d
    HAVING coalesce(sum(d.physical_bytes), 0) <> 0 OR coalesce(sum(d.blobs), 0) <> 0;
    RETURN NULL;
END $$;

CREATE TRIGGER blobs_usage_ins AFTER INSERT ON blobs
    REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION blobs_usage_insert();
CREATE TRIGGER blobs_usage_del AFTER DELETE ON blobs
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION blobs_usage_delete();
CREATE TRIGGER blobs_usage_upd AFTER UPDATE ON blobs
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION blobs_usage_update();
