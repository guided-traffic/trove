-- trove:phase expand
--
-- What the last attempt to fetch the documentation of a version of a remote Ansible Galaxy repository came to (R-ANS-033, R-ANS-042): the
-- states the screens call pending (queued) and unavailable. A version with documentation has a row in ansible_version_docs and needs none
-- here; a queued row older than one hour counts as none, because the job is lost and the next version document queues it again. Derived
-- data like the three tables of ansible_doc_tables: not exported by a backup (R-BAK-004), deleted with the repository.
--
-- name is "<ns>.<name>" and version the version as written.
CREATE TABLE ansible_doc_fetches (
    repo_id    bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name       text        COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 3 AND 129),
    version    text        COLLATE "C" NOT NULL CHECK (length(version) BETWEEN 5 AND 128),
    state      text        NOT NULL CHECK (state IN ('queued', 'unavailable', 'none')),
    reason     text        NOT NULL DEFAULT '' CHECK (reason IN ('', 'upstream_error', 'too_large', 'invalid_document')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, name, version)
);
