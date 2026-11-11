-- trove:phase expand
--
-- The documentation of a collection version (R-ANS-033): the README, the Markdown files of docs/ and one entry per content item (module,
-- plugin, role). It is derived data and a source of truth for nothing: a local repository's rows are generated from the archive at publish,
-- in the transaction of the commit, and rebuilt by the job ansible-reindex; a remote repository's rows are parsed from the docs-blob
-- document the upstream sent, which is stored as a remote document of its own; a virtual repository has none. The three tables are not
-- exported by a backup (R-BAK-004): a restore enqueues the reindex, which rebuilds them. Deleting the version, or the repository, deletes
-- the rows.
--
-- name is "<ns>.<name>" and version the version as written, as in ansible_collection_versions.

-- One row per version that has documentation.
CREATE TABLE ansible_version_docs (
    repo_id     bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name        text        COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 3 AND 129),
    version     text        COLLATE "C" NOT NULL CHECK (length(version) BETWEEN 5 AND 128),
    -- generated: derived from an archive by trove; upstream: parsed from the docs-blob document of an upstream.
    source      text        NOT NULL CHECK (source IN ('generated', 'upstream')),
    -- The version of the code that derived the rows (docgen.Version; 0 for upstream); a reindex rebuilds older ones.
    generator   integer     NOT NULL CHECK (generator >= 0),
    entry_count integer     NOT NULL CHECK (entry_count >= 0),
    -- Of the stored entries and files, against the bound of R-ANS-031.
    total_bytes bigint      NOT NULL CHECK (total_bytes >= 0),
    -- A bound cut entries or files off.
    truncated   boolean     NOT NULL DEFAULT false,
    -- [{path, code, message}], at most 100.
    warnings    jsonb       NOT NULL DEFAULT '[]',
    built_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, name, version)
);

-- One row per content item. search_text is built by the data port in Go (at most 4 KiB), so that the generated vector is a function of
-- one column and the same on every replica.
CREATE TABLE ansible_doc_entries (
    repo_id           bigint NOT NULL,
    name              text   COLLATE "C" NOT NULL,
    version           text   COLLATE "C" NOT NULL,
    -- The Galaxy's content_type and content_name.
    content_type      text   COLLATE "C" NOT NULL CHECK (length(content_type) BETWEEN 1 AND 64),
    content_name      text   COLLATE "C" NOT NULL CHECK (length(content_name) BETWEEN 1 AND 256),
    short_description text   NOT NULL DEFAULT '',
    -- doc_strings present; doc is then the document in the Galaxy form of R-ANS-035.
    has_docs          boolean NOT NULL,
    doc               jsonb,
    -- Roles: the generated source, or the upstream's HTML as received.
    readme_md         text,
    readme_html       text,
    search_text       text   NOT NULL DEFAULT '' CHECK (length(search_text) <= 4096),
    search            tsvector GENERATED ALWAYS AS (to_tsvector('simple', search_text)) STORED,
    PRIMARY KEY (repo_id, name, version, content_type, content_name),
    FOREIGN KEY (repo_id, name, version) REFERENCES ansible_version_docs (repo_id, name, version) ON DELETE CASCADE
);

CREATE INDEX ansible_doc_entries_search ON ansible_doc_entries USING gin (search);

-- The README and the Markdown files of docs/. format is markdown for a generated set and html for an upstream's, which is untrusted and
-- sanitized on every serving.
CREATE TABLE ansible_doc_files (
    repo_id   bigint NOT NULL,
    name      text   COLLATE "C" NOT NULL,
    version   text   COLLATE "C" NOT NULL,
    file_name text   COLLATE "C" NOT NULL CHECK (length(file_name) BETWEEN 1 AND 256),
    format    text   NOT NULL CHECK (format IN ('markdown', 'html')),
    body      text   NOT NULL,
    PRIMARY KEY (repo_id, name, version, file_name),
    FOREIGN KEY (repo_id, name, version) REFERENCES ansible_version_docs (repo_id, name, version) ON DELETE CASCADE
);
