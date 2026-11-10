-- trove:phase expand
--
-- The digests a remote Ansible Galaxy repository's version documents announce (R-ANS-018, Q-ANS-13): a row per version, written in the
-- transaction that stores the version document. The archive of a version is verified against the digest recorded here, whatever
-- integrity.checksumPolicy says, and the first digest recorded for a version stays: a refreshed document that announces another one
-- changes nothing and writes the event proxy.upstream_digest_changed. The row carries no blob reference and no item reference: it is
-- what the metadata announced, not a stored file, so it never holds a blob against garbage collection and is deleted with the
-- repository. It goes with the cache of a remote: a restore from a backup that left the remote caches out deletes the rows with the
-- cache entries (R-BAK-008), so that the first request for a version records its digest again.
--
-- name is "<ns>.<name>" and version the version as written.
CREATE TABLE ansible_upstream_versions (
    repo_id          bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name             text        COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 3 AND 129),
    version          text        COLLATE "C" NOT NULL CHECK (length(version) BETWEEN 5 AND 128),
    sha256           bytea       NOT NULL CHECK (length(sha256) = 32),
    size             bigint      NOT NULL CHECK (size >= 0),
    requires_ansible text        NOT NULL DEFAULT '' CHECK (length(requires_ansible) <= 256),
    recorded_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, name, version)
);
