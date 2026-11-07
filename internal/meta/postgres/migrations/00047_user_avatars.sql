-- trove:phase expand
--
-- The avatar of a user (R-API-055, Q-STOR-166, Q-STOR-167). The image is the PNG that the server made of the upload
-- (internal/avatar): its content is a blob like any other (INV-4), committed through the protocol of R-STOR-005 and
-- deduplicated by SHA-256, and a row here names it for the user. A user has at most one avatar, so the key is the user.
--
-- The blob reference follows the rules of R-STOR-003: the column is blob_sha256, it has the foreign key and an index, and the
-- three triggers guard a new reference and queue the old blob for collection when the row is replaced or deleted, also when
-- the deletion of the user cascades to it (R-STOR-024). The id column is what blob_ref_release_update joins old and new rows
-- on.
--
-- The avatar URL is a member of the user record, so a change of the row bumps principal.revision, the ETag of the user
-- (R-API-012, 00013_identity_revisions). The update trigger fires only when the blob changes: an upload of the same image
-- leaves the record as it is.
--
-- The table is new and empty, so its key and index are inline. Export class data (R-BAK-004): the rows and the blobs they
-- reference travel in a backup (tables_avatar.go). The CHECK on size is the bound of internal/avatar.MaxUpload, which the
-- stored PNG, a fixed-size square, stays far below.

CREATE TABLE user_avatars (
    id          bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    user_id     uuid PRIMARY KEY REFERENCES app_user (principal_id) ON DELETE CASCADE,
    blob_sha256 bytea NOT NULL REFERENCES blobs (sha256),
    size        bigint NOT NULL CHECK (size > 0 AND size <= 1048576),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_avatars_blob ON user_avatars (blob_sha256);

CREATE TRIGGER user_avatars_blob_guard BEFORE INSERT OR UPDATE OF blob_sha256 ON user_avatars
    FOR EACH ROW EXECUTE FUNCTION blob_ref_guard();
CREATE TRIGGER user_avatars_blob_release_del AFTER DELETE ON user_avatars
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release();
CREATE TRIGGER user_avatars_blob_release_upd AFTER UPDATE ON user_avatars
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release_update();

CREATE TRIGGER user_avatars_revision_change AFTER INSERT OR DELETE ON user_avatars
    FOR EACH ROW EXECUTE FUNCTION principal_revision_bump('user_id');
CREATE TRIGGER user_avatars_revision_replace AFTER UPDATE ON user_avatars
    FOR EACH ROW WHEN (OLD.blob_sha256 IS DISTINCT FROM NEW.blob_sha256)
    EXECUTE FUNCTION principal_revision_bump('user_id');
