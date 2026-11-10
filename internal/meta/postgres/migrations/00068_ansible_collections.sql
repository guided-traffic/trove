-- trove:phase expand
--
-- The tables of the Ansible Galaxy format's local repositories (R-ANS-011, Q-ANS-24): a row per stored collection version with the
-- metadata read from its archive at publish, and the tombstones of deleted versions. The rows carry no blob reference (no blob_sha256
-- column, R-STOR-003 rule 1), so they never hold a blob against garbage collection; the item of the archive does, and the row is
-- deleted with it. They are written by the storage hooks in the transaction that writes the item.
--
-- name is "<ns>.<name>" and version the version as written. sort_key is the memcmp-ordered semantic-version precedence of
-- internal/formats/ansible/version, build metadata excluded, so the version list of a collection is one index scan in descending
-- order. There is no revision column: the documents are rendered from the rows and their ETag is derived from the generic package
-- row's rev_seq, which the commit hook raises (R-ARCH-009, Q-ANS-31).
CREATE TABLE ansible_collection_versions (
    repo_id          bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name             text        COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 3 AND 129),
    version          text        COLLATE "C" NOT NULL CHECK (length(version) BETWEEN 5 AND 128),
    sort_key         bytea       NOT NULL,
    item_id          bigint      NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    sha256           bytea       NOT NULL CHECK (length(sha256) = 32),
    size             bigint      NOT NULL CHECK (size >= 0),
    requires_ansible text        NOT NULL DEFAULT '' CHECK (length(requires_ansible) <= 256),
    dependencies     jsonb       NOT NULL DEFAULT '{}',
    collection_info  jsonb       NOT NULL,
    published_by     text        NOT NULL,
    published_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, name, version)
);

-- The version list of a collection, newest first.
CREATE INDEX ansible_collection_versions_by_precedence ON ansible_collection_versions (repo_id, name, sort_key DESC);
-- The row of an item, for the removal and restore hooks.
CREATE UNIQUE INDEX ansible_collection_versions_by_item ON ansible_collection_versions (item_id);

-- A deleted version: its number is not accepted again, whatever the bytes (R-ANS-012, Q-ANS-9). The journal entry
-- ansible.version.deleted replays it after a restore (R-ANS-029).
CREATE TABLE ansible_tombstones (
    repo_id    bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name       text        COLLATE "C" NOT NULL,
    version    text        COLLATE "C" NOT NULL,
    sha256     bytea       NOT NULL CHECK (length(sha256) = 32),
    deleted_at timestamptz NOT NULL DEFAULT now(),
    deleted_by text        NOT NULL,
    PRIMARY KEY (repo_id, name, version)
);
