-- trove:phase expand
--
-- The tables of the Debian (APT) format's local and virtual repositories (R-DEB-008, Q-DEB-5, Q-DEB-6): the suites of a repository
-- with the sequence numbers of the index build, a row per stored package file with its normalized control paragraph, the suites and
-- components a file is listed in, the generations an index build published and the mark of a package file the journal replay
-- withheld. They carry no blob reference (no blob_sha256 column, R-STOR-003 rule 1), so they never hold a blob against garbage
-- collection; the items do. The rows are written by the storage hooks in the transaction that writes the items.
--
-- deb_suites.change_seq and built_seq are revision columns (R-ARCH-009): clients hold a changeSeq for the index state read
-- (R-DEB-014), and a restore raises both by 2^32. change_seq is raised by every change of the suite's content, keys or settings and
-- built_seq is the change_seq the published generation was built from. A virtual repository's suites live in the same table: inputs
-- holds the fingerprint of each member's input and checked_at the last check of the members (R-DEB-026).
CREATE TABLE deb_suites (
    repo_id       bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    suite         text        COLLATE "C" NOT NULL CHECK (suite ~ '^[a-z0-9][a-z0-9._+-]{0,63}$'),
    change_seq    bigint      NOT NULL DEFAULT 1,
    built_seq     bigint      NOT NULL DEFAULT 0,
    generation    bigint      NOT NULL DEFAULT 0,
    release_date  timestamptz,
    published_at  timestamptz,
    state         text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'current', 'failed')),
    reason        text        CHECK (length(reason) <= 256),
    inputs        jsonb,
    checked_at    timestamptz,
    PRIMARY KEY (repo_id, suite)
);

-- One row per stored package file of a local repository. control is the normalized paragraph (R-DEB-007): the fields in their order
-- without the ones an index computes, at most 1 MiB. A package, version and architecture is stored once per repository (Q-DEB-4).
CREATE TABLE deb_binaries (
    item_id       bigint PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    repo_id       bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    package       text   COLLATE "C" NOT NULL,
    version       text   COLLATE "C" NOT NULL,
    architecture  text   COLLATE "C" NOT NULL,
    source        text   COLLATE "C" NOT NULL,
    control       text   NOT NULL CHECK (octet_length(control) <= 1048576),
    UNIQUE (repo_id, package, version, architecture)
);

-- The suites and components a file is listed in (R-DEB-009). A suite stays while a file is listed in it (the foreign key), and a
-- suite without files keeps its last generation until a reindex drops it (R-DEB-014).
CREATE TABLE deb_coordinates (
    item_id    bigint NOT NULL REFERENCES deb_binaries (item_id) ON DELETE CASCADE,
    repo_id    bigint NOT NULL,
    suite      text   COLLATE "C" NOT NULL,
    component  text   COLLATE "C" NOT NULL CHECK (component ~ '^[a-z0-9][a-z0-9._+-]{0,63}$'),
    PRIMARY KEY (item_id, suite, component),
    FOREIGN KEY (repo_id, suite) REFERENCES deb_suites (repo_id, suite) ON DELETE CASCADE
);
CREATE INDEX deb_coordinates_suite ON deb_coordinates (repo_id, suite, component);

-- The published and the retained generations of a suite (R-DEB-012). files maps the path relative to dists/<suite>/ to the SHA-256
-- (hex) of the file; signed_by lists the fingerprints of the keys that signed the generation.
CREATE TABLE deb_generations (
    repo_id        bigint      NOT NULL,
    suite          text        COLLATE "C" NOT NULL,
    generation     bigint      NOT NULL,
    built_seq      bigint      NOT NULL,
    published_at   timestamptz NOT NULL DEFAULT now(),
    superseded_at  timestamptz,
    files          jsonb       NOT NULL,
    signed_by      text[]      NOT NULL,
    PRIMARY KEY (repo_id, suite, generation),
    FOREIGN KEY (repo_id, suite) REFERENCES deb_suites (repo_id, suite) ON DELETE CASCADE
);

-- A package file the journal replay withheld after a restore (R-DEB-032, Q-DEB-22): a package deleted after the snapshot may have been
-- malicious, and a restore must not serve it again. The replay never deletes content (R-HA-054), so it writes this row and leaves the
-- item and its bytes stored; the serve check refuses the file and the upload of the same bytes until an administrator reinstates it,
-- which removes the row. Written by the replay only. withheld_at is when the replay withheld it, deleted_by the principal of the
-- journaled deletion.
CREATE TABLE deb_tombstones (
    repo_id      bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    path         text        COLLATE "C" NOT NULL,
    sha256       bytea       NOT NULL CHECK (octet_length(sha256) = 32),
    withheld_at  timestamptz NOT NULL DEFAULT now(),
    deleted_by   uuid,
    PRIMARY KEY (repo_id, path)
);
