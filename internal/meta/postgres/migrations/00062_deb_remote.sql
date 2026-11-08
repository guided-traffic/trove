-- trove:phase expand
--
-- The rows of the Debian (APT) format's remote repositories (R-DEB-023, Q-DEB-16, Q-DEB-36): the stored release document of each
-- release directory with the verdict of its verification, the Packages and Sources indexes whose rows are parsed, and the parsed
-- entries. They are cache rows: a restore without remote caches deletes them with the cache (R-BAK-008, H-630). No blob reference.
--
-- document 'absent' records that the upstream answered 404 or 410 for the suite's release documents, so the shadowing check can
-- settle a suite the remote never stored (Q-DEB-41); such a row has neither a digest nor a date. verified_by is the fingerprint of the
-- key that verified the release, NULL without upstreamKeys or once a key change cleared it (Q-DEB-42); floor_date is the newest Date
-- of any release verified for the directory, the rollback floor.
CREATE TABLE deb_remote_releases (
    repo_id       bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    dir           text        COLLATE "C" NOT NULL,
    document      text        NOT NULL CHECK (document IN ('InRelease', 'Release', 'absent')),
    sha256        bytea       CHECK (octet_length(sha256) = 32),
    release_date  timestamptz,
    by_hash       boolean,
    verified_by   text,
    floor_date    timestamptz,
    stored_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, dir, document),
    CHECK ((document = 'absent') = (sha256 IS NULL)),
    CHECK ((document = 'absent') = (release_date IS NULL))
);

-- An index whose rows are parsed: path is relative to dir without the compression suffix ("main/binary-amd64/Packages"), sha256 is
-- the digest of the parsed file, skipped counts the paragraphs left out for a grammar or path rule (R-DEB-023, R-DEB-034).
CREATE TABLE deb_remote_indexes (
    repo_id    bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    dir        text        COLLATE "C" NOT NULL,
    path       text        COLLATE "C" NOT NULL,
    sha256     bytea       NOT NULL CHECK (octet_length(sha256) = 32),
    entries    integer     NOT NULL,
    skipped    integer     NOT NULL DEFAULT 0,
    parsed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, dir, path)
);

-- One row per package file an index lists; architecture is "source" for an entry of a Sources index, path the Filename relative to
-- remote.url. The serve-facts kind deb_remote reads the digest and size of a path from here (R-DEB-022, Q-ARCH-381).
CREATE TABLE deb_remote_entries (
    repo_id       bigint NOT NULL,
    dir           text   COLLATE "C" NOT NULL,
    index_path    text   COLLATE "C" NOT NULL,
    package       text   COLLATE "C" NOT NULL,
    version       text   COLLATE "C" NOT NULL,
    architecture  text   COLLATE "C" NOT NULL,
    path          text   COLLATE "C" NOT NULL,
    sha256        bytea  NOT NULL CHECK (octet_length(sha256) = 32),
    size          bigint NOT NULL CHECK (size >= 0),
    FOREIGN KEY (repo_id, dir, index_path) REFERENCES deb_remote_indexes (repo_id, dir, path) ON DELETE CASCADE
);
CREATE INDEX deb_remote_entries_path ON deb_remote_entries (repo_id, path);
CREATE INDEX deb_remote_entries_name ON deb_remote_entries (repo_id, package);
