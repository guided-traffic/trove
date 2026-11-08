-- trove:phase expand
--
-- The state of the multi-request upload sessions (R-STOR-011, Q-OCI-12): one row per session and one per completed part, so
-- that any replica continues a blob that was sent over several requests (INV-18). Both tables are transient (R-STOR-047):
-- staged bytes are not backed up, and a client restarts its upload after a restore.
--
-- Beyond the sketch of docs/spec/storage.md before the OCI page: addressed_repo_id is the repository the client addressed
-- (repo_id is the one the session writes to; they differ when a virtual repository forwards a push to its deployment
-- target), and binding is the string the format names - the OCI image name - which every request must repeat. Both are checked
-- on every request, so a session id from another repository or name is a session that does not exist. A session needs the
-- repository it writes to and the one addressed; deleting either ends the session, and the collection run removes the staging
-- objects of the rows that are gone through the orphan sweep (R-STOR-026).

CREATE TABLE upload_sessions (
    id                uuid        PRIMARY KEY,
    repo_id           bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    addressed_repo_id bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    binding           text        CHECK (length(binding) <= 512),
    principal_id      uuid,
    received          bigint      NOT NULL DEFAULT 0 CHECK (received >= 0),
    hash_state        bytea       NOT NULL,
    part_size         bigint      NOT NULL CHECK (part_size > 0),
    s3_upload_id      text        NOT NULL,
    tail_size         bigint      NOT NULL DEFAULT 0 CHECK (tail_size >= 0 AND tail_size < part_size),
    tail_writer       uuid,
    tail_sha256       bytea       CHECK (tail_sha256 IS NULL OR octet_length(tail_sha256) = 32),
    writer            uuid,
    writer_until      timestamptz,
    digest            bytea       CHECK (digest IS NULL OR octet_length(digest) = 32),
    state             text        NOT NULL CHECK (state IN ('open', 'completing', 'assembled', 'done', 'aborted')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_activity_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((tail_size = 0) = (tail_writer IS NULL) AND (tail_writer IS NULL) = (tail_sha256 IS NULL))
);

-- The collection run reads the sessions that can still be continued by their last activity (R-STOR-011 Expiry); the sessions
-- that are done or aborted wait for the clean-up of their objects and are found by state.
CREATE INDEX upload_sessions_live ON upload_sessions (last_activity_at)
    WHERE state IN ('open', 'completing', 'assembled');
CREATE INDEX upload_sessions_cleanup ON upload_sessions (state) WHERE state IN ('done', 'aborted');
CREATE INDEX upload_sessions_repo ON upload_sessions (repo_id);
CREATE INDEX upload_sessions_addressed_repo ON upload_sessions (addressed_repo_id);

CREATE TABLE upload_session_parts (
    session_id   uuid    NOT NULL REFERENCES upload_sessions (id) ON DELETE CASCADE,
    part_number  integer NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    size         bigint  NOT NULL CHECK (size > 0),
    etag         text    NOT NULL,
    sha256       bytea   NOT NULL CHECK (octet_length(sha256) = 32),
    PRIMARY KEY (session_id, part_number)
);
