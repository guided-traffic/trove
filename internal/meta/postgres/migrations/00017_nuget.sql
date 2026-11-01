-- trove:phase expand
--
-- The NuGet format's tables (M3, WP3.S; R-NUGET-005, R-NUGET-006, R-NUGET-020,
-- R-NUGET-021). The package's revision counter is packages.rev_seq of the core, not
-- a NuGet column (R-NUGET-005), and the display name of the ID casing is
-- packages.display_name (Q-NUGET-5): neither needs a column here. Package documents
-- are not stored; the registration, flat-container and search documents are
-- rendered from these rows.
--
-- The journal of Q-NUGET-10 (the additions and removals a restore replays) is a set
-- of objects in the object store, not a table, so it has no line here.
--
-- Deferred on purpose: the trigram index nuget_version_id_trgm of the specification's
-- sketch. It needs CREATE EXTENSION pg_trgm, which no migration of trove runs and
-- which a managed PostgreSQL may not allow the application's role; search reads
-- search_doc, whose ID tokens answer a prefix or a word, and the latest-version
-- index answers an ID (Q-NUGET-12).
--
-- Beyond the sketch of docs/spec/formats/nuget.md: CHECKs on the empty IDs and the
-- digest lengths, and the indexes the foreign keys and the symbol job's queue need
-- (nuget_symbol_package_item, nuget_symbol_package_pending, nuget_symbol_file_version,
-- nuget_symbol_file_item). nuget_tombstone has no foreign key to package_versions
-- or items: it outlives the version row, the item, the trash can and its permanent
-- removal (R-NUGET-006). nuget_symbol_file belongs to P1 (R-NUGET-021) and is
-- created now so that the M3 schema stays one step.

CREATE TABLE nuget_version (
    version_id       bigint      PRIMARY KEY REFERENCES package_versions (id) ON DELETE CASCADE,
    repository_id    bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    lower_id         text        NOT NULL CHECK (lower_id <> ''),
    declared_id      text        NOT NULL CHECK (declared_id <> ''),
    original_version text        NOT NULL CHECK (original_version <> ''),
    sort_key         bytea       NOT NULL CHECK (octet_length(sort_key) > 0),
    is_prerelease    boolean     NOT NULL,
    is_semver2       boolean     NOT NULL,
    listed           boolean     NOT NULL DEFAULT true,
    published_at     timestamptz NOT NULL,
    package_types    text[]      NOT NULL DEFAULT '{dependency}',
    metadata         jsonb       NOT NULL,
    search_doc       tsvector    NOT NULL,
    origin           text        NOT NULL CHECK (origin IN ('push', 'upload', 'copy', 'cache'))
);
CREATE INDEX nuget_version_latest ON nuget_version (repository_id, lower_id, sort_key DESC) WHERE listed;
CREATE INDEX nuget_version_search ON nuget_version USING gin (search_doc);

-- The tombstone outlives the version row, the item and its permanent removal
-- (R-NUGET-006): it has no foreign key to either. sha512 is the digest of the
-- removed .nupkg, or for a 'reserved' tombstone the journaled one (Q-NUGET-10).
CREATE TABLE nuget_tombstone (
    repository_id bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    lower_id      text        NOT NULL,
    lower_version text        NOT NULL,
    sha512        bytea       NOT NULL CHECK (octet_length(sha512) = 64),
    deleted_at    timestamptz NOT NULL,
    deleted_by    uuid        NOT NULL,
    kind          text        NOT NULL DEFAULT 'removed' CHECK (kind IN ('removed', 'reserved')),
    PRIMARY KEY (repository_id, lower_id, lower_version)
);

CREATE TABLE nuget_symbol_package (
    version_id  bigint PRIMARY KEY REFERENCES package_versions (id) ON DELETE CASCADE,
    item_id     bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    indexed_at  timestamptz,
    index_error text CHECK (length(index_error) <= 1024)
);
CREATE INDEX nuget_symbol_package_item ON nuget_symbol_package (item_id);
-- The packages the symbol indexing has not reached: the age of the oldest is the
-- lag metric of R-NUGET-021, and the data migration that enqueues the packages of
-- earlier releases reads the same set.
CREATE INDEX nuget_symbol_package_pending ON nuget_symbol_package (version_id) WHERE indexed_at IS NULL;

CREATE TABLE nuget_symbol_file (
    repository_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    symbol_key    text   NOT NULL,
    version_id    bigint NOT NULL REFERENCES nuget_symbol_package (version_id) ON DELETE CASCADE,
    item_id       bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    pdb_kind      text   NOT NULL CHECK (pdb_kind IN ('portable', 'windows')),
    checksum      bytea CHECK (octet_length(checksum) = 32),
    PRIMARY KEY (repository_id, symbol_key, version_id)
);
CREATE INDEX nuget_symbol_file_version ON nuget_symbol_file (version_id);
CREATE INDEX nuget_symbol_file_item ON nuget_symbol_file (item_id);
