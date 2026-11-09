-- trove:phase expand
--
-- The files a remote's stored releases list (R-DEB-021, Q-DEB-80): the format needs the SHA-256 of an index path while it decides
-- whether a fetched index is the file the stored release names, and a statement on this table answers that in one lookup where the
-- release document would have to be read and parsed again. The rows are cache rows like the three tables of 00062: a restore without
-- remote caches deletes them (R-BAK-008, H-630). No blob reference.
--
-- The rows of the release that is stored now have current = true. When a release is replaced, the rows of the old one stay for
-- deb.by_hash_keep with current = false and superseded_at, so that the housekeeping of the remote keeps the by-hash files and plain
-- index documents a recent release still names (R-DEB-021).
CREATE TABLE deb_remote_files (
    repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    dir            text        COLLATE "C" NOT NULL,
    path           text        COLLATE "C" NOT NULL,
    sha256         bytea       NOT NULL CHECK (octet_length(sha256) = 32),
    size           bigint      NOT NULL CHECK (size >= 0),
    current        boolean     NOT NULL,
    superseded_at  timestamptz,
    PRIMARY KEY (repo_id, dir, path, sha256),
    CHECK (current = (superseded_at IS NULL))
);
CREATE INDEX deb_remote_files_current ON deb_remote_files (repo_id, dir, path) WHERE current;
CREATE INDEX deb_remote_files_hash ON deb_remote_files (repo_id, sha256);
