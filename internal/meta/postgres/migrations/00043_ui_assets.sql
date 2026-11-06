-- trove:phase expand
--
-- The shared asset store of the UI (R-HA-020, Q-HA-120 to Q-HA-123). Every replica that serves the UI publishes the hashed
-- files of its embedded build at start-up: the content goes into the blob store like any other content (INV-4), and a row
-- here names it by the locale directory and the path under it. A replica asked for a hashed file that its own build lacks,
-- which during a rolling upgrade is a file of the other release, looks it up here. The rows are written by the replicas only,
-- never by a request, and only for paths of the hashed pattern of internal/webui (IsHashed); the CHECK keeps dot segments
-- and anything outside that alphabet out on this side too.
--
-- A row holds one build's bytes for its name: the name's hash covers the code before the translations are inlined (Q-UI-140),
-- so two releases that differ only in a translation share a name with different bytes, and every replica serves its own file
-- before the store's. seen_at is renewed, at start and then hourly, only by replicas whose build has those bytes; a replica
-- with other bytes takes the row over when its blob is damaged or nobody renewed it for a while, and a row not renewed for
-- the retention of Q-HA-122 is deleted (Q-UI-142). The update and delete triggers queue the old blob for collection
-- (R-STOR-024).
--
-- The table is new and empty, so its key and index are inline. Export class transient (R-BAK-004): a restore leaves it empty
-- and the replicas publish again (tables_ui.go).

CREATE TABLE ui_assets (
    id           bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    locale       text COLLATE "C" NOT NULL CHECK (locale ~ '^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$'),
    path         text COLLATE "C" NOT NULL CHECK (length(path) <= 255 AND path ~ '^[A-Za-z0-9_./-]+$' AND path !~ '(^|/)\.'),
    blob_sha256  bytea NOT NULL REFERENCES blobs (sha256),
    size         bigint NOT NULL CHECK (size >= 0),
    published_by text NOT NULL CHECK (length(published_by) <= 128),
    published_at timestamptz NOT NULL DEFAULT now(),
    seen_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (locale, path)
);
CREATE INDEX ui_assets_blob ON ui_assets (blob_sha256);

CREATE TRIGGER ui_assets_blob_guard BEFORE INSERT OR UPDATE OF blob_sha256 ON ui_assets
    FOR EACH ROW EXECUTE FUNCTION blob_ref_guard();
CREATE TRIGGER ui_assets_blob_release_del AFTER DELETE ON ui_assets
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release();
CREATE TRIGGER ui_assets_blob_release_upd AFTER UPDATE ON ui_assets
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release_update();
