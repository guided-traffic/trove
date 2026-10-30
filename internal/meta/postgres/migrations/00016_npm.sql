-- trove:phase expand
--
-- The npm format's tables (M2, WP2.S; R-NPM-006, R-NPM-008, R-NPM-024). The
-- package's revision counter is packages.rev_seq of the core, not an npm column
-- (R-NPM-006). Package documents are not stored; they are rendered from these
-- rows.
--
-- Deferred: the search_vector columns arrive with R-NPM-037 (P1) as an expand
-- step, so no write path maintains a column that nothing reads (Q-ARCH-191).
-- npm_remote_packages.doc_sha256 is a digest, not a blob reference: it has no
-- foreign key to blobs, so the blob reference rule does not apply. The bytes of
-- the stored document are referenced by remote_cache_entries.blob_sha256.
--
-- Beyond the sketch of docs/spec/formats/npm.md: CHECKs on the lengths and the
-- non-negative counts.

CREATE TABLE npm_packages (
    package_id  bigint PRIMARY KEY REFERENCES packages (id) ON DELETE CASCADE,
    repo_id     bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name_fold   text NOT NULL,
    dist_tags   jsonb NOT NULL DEFAULT '{}'::jsonb,
    modified_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, name_fold)
);

CREATE TABLE npm_versions (
    version_id         bigint PRIMARY KEY REFERENCES package_versions (id) ON DELETE CASCADE,
    manifest           jsonb       NOT NULL,
    integrity          text        NOT NULL,
    shasum             char(40)    NOT NULL,
    deprecated         text CHECK (length(deprecated) <= 1024),
    file_count         integer     NOT NULL CHECK (file_count >= 0),
    unpacked_size      bigint      NOT NULL CHECK (unpacked_size >= 0),
    has_install_script boolean     NOT NULL,
    has_shrinkwrap     boolean     NOT NULL,
    readme             text CHECK (octet_length(readme) <= 65536),
    readme_filename    text,
    publish_tag        text        NOT NULL,
    published_at       timestamptz NOT NULL
);

-- The tombstone outlives the version row, the item and its permanent removal
-- (R-NPM-008): it has no foreign key to either.
CREATE TABLE npm_tombstones (
    repo_id        bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name           text        NOT NULL,
    version        text        NOT NULL,
    integrity      text        NOT NULL,
    source         text        NOT NULL CHECK (source IN ('unpublish', 'move', 'journal')),
    unpublished_at timestamptz NOT NULL,
    unpublished_by uuid        NOT NULL,
    PRIMARY KEY (repo_id, name, version)
);

CREATE TABLE npm_remote_packages (
    repo_id      bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name         text   NOT NULL,
    doc_sha256   bytea  NOT NULL CHECK (octet_length(doc_sha256) = 32),
    doc_modified timestamptz,
    PRIMARY KEY (repo_id, name)
);

CREATE TABLE npm_remote_versions (
    repo_id      bigint  NOT NULL,
    name         text    NOT NULL,
    version      text    NOT NULL,
    integrity    text,
    shasum       text,
    published_at timestamptz,
    listed       boolean NOT NULL DEFAULT true,
    PRIMARY KEY (repo_id, name, version),
    FOREIGN KEY (repo_id, name) REFERENCES npm_remote_packages (repo_id, name) ON DELETE CASCADE
);
