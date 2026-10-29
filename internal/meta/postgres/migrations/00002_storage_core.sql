-- trove:phase expand
--
-- Storage core (R-STOR-003): repositories, blobs, packages, versions, items,
-- properties, download statistics, the garbage-collection queue and the
-- blob reference guard and release triggers.

CREATE TABLE repositories (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key         text NOT NULL UNIQUE CHECK (key ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
    format      text NOT NULL,
    class       text NOT NULL CHECK (class IN ('local', 'remote', 'virtual')),
    config      jsonb NOT NULL DEFAULT '{}',
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
    object_deleted_at timestamptz,
    checked_at        timestamptz,
    deep_checked_at   timestamptz
);
CREATE INDEX blobs_sha1     ON blobs (sha1);
CREATE INDEX blobs_md5      ON blobs (md5);
CREATE INDEX blobs_deleting ON blobs (state_changed_at) WHERE state = 'deleting';
CREATE INDEX blobs_damaged  ON blobs (content_status) WHERE content_status <> 'ok';
CREATE INDEX blobs_check    ON blobs (checked_at NULLS FIRST) WHERE state = 'live';

CREATE TABLE packages (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name          text NOT NULL,
    display_name  text NOT NULL,
    attributes    jsonb NOT NULL DEFAULT '{}',
    rev_seq       bigint NOT NULL DEFAULT 1,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, name)
);

CREATE TABLE package_versions (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    package_id       bigint NOT NULL REFERENCES packages (id) ON DELETE CASCADE,
    version          text NOT NULL,
    display_version  text NOT NULL,
    lead_item_id     bigint,
    attributes       jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    created_by       uuid,
    trashed_at       timestamptz
);
CREATE UNIQUE INDEX package_versions_live ON package_versions (package_id, version) WHERE trashed_at IS NULL;

CREATE TABLE items (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id            bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    path               text COLLATE "C" NOT NULL,
    parent_path        text COLLATE "C" NOT NULL,
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
    generated          boolean NOT NULL DEFAULT false,
    version_id         bigint REFERENCES package_versions (id) ON DELETE SET NULL,
    upstream_url       text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    created_by         uuid,
    modified_at        timestamptz NOT NULL DEFAULT now(),
    modified_by        uuid,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    trashed_at         timestamptz,
    trashed_by         uuid,
    trash_operation    uuid,
    CHECK ((kind = 'file') = (blob_sha256 IS NOT NULL)),
    CHECK ((kind = 'file') = (size IS NOT NULL))
);
CREATE UNIQUE INDEX items_live_path ON items (repo_id, path) WHERE trashed_at IS NULL;
CREATE INDEX items_children ON items (repo_id, parent_path, path) WHERE trashed_at IS NULL;
CREATE INDEX items_blob     ON items (blob_sha256);
CREATE INDEX items_version  ON items (version_id);
CREATE INDEX items_trash    ON items (trashed_at, repo_id, path) WHERE trashed_at IS NOT NULL;

-- The cycle between items and package_versions: restore loads lead_item_id as
-- NULL and fills it afterwards (R-BAK-004 CycleBreak).
ALTER TABLE package_versions
    ADD FOREIGN KEY (lead_item_id) REFERENCES items (id) ON DELETE SET NULL;

CREATE TABLE item_properties (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id  bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    key      text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
    value    text NOT NULL CHECK (char_length(value) <= 2400)
);
-- Values may exceed the btree entry limit: index a hash and a prefix instead.
CREATE UNIQUE INDEX item_properties_unique ON item_properties (item_id, key, md5(value));
CREATE INDEX item_properties_lookup ON item_properties (key, left(value, 256));

CREATE TABLE item_download_stats (
    item_id           bigint PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    downloads         bigint NOT NULL DEFAULT 0,
    last_download_at  timestamptz,
    last_downloader   uuid
);

-- No foreign key: the blob row may already be gone when the candidate is read.
CREATE TABLE blob_gc_queue (
    sha256       bytea PRIMARY KEY,
    enqueued_at  timestamptz NOT NULL
);

-- Reference guard and release (R-STOR-003, reference rules). Every table with a
-- blob reference names the column blob_sha256, declares the foreign key to
-- blobs(sha256), indexes it and installs both triggers.
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
              USING ERRCODE = 'AS001';
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

-- The variant for an overwrite in place: only the blobs that stopped being
-- referenced by their row are candidates.
CREATE FUNCTION blob_ref_release_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO blob_gc_queue (sha256, enqueued_at)
    SELECT DISTINCT o.blob_sha256, now()
      FROM old_rows o
      JOIN new_rows n ON n.id = o.id
     WHERE o.blob_sha256 IS NOT NULL
       AND o.blob_sha256 IS DISTINCT FROM n.blob_sha256
    ON CONFLICT (sha256) DO UPDATE SET enqueued_at = EXCLUDED.enqueued_at;
    RETURN NULL;
END $$;

CREATE TRIGGER items_blob_guard BEFORE INSERT OR UPDATE OF blob_sha256 ON items
    FOR EACH ROW EXECUTE FUNCTION blob_ref_guard();
CREATE TRIGGER items_blob_release_del AFTER DELETE ON items
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release();
CREATE TRIGGER items_blob_release_upd AFTER UPDATE ON items
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release_update();
