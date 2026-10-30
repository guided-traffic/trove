-- trove:phase expand
--
-- Remote repositories (M2, WP2.S): the cache entries of the pull-through proxy
-- and its URL map (R-REPO-025, R-REPO-043), the circuit state (R-REPO-029), the
-- cache invalidations (R-REPO-045) and the stored repository secrets
-- (R-REPO-033). No remote repository can exist before M2 (Q-REPO-16), so no
-- step backfills anything, and a v0.3.x replica keeps serving against this schema
-- because it never reads these tables.
--
-- Beyond the sketches of docs/spec/repositories-and-proxy.md:
--   * remote_cache_entries.id is a surrogate key. blob_ref_release_update()
--     joins the old and the new rows on id, so an entry whose blob is replaced
--     enqueues the replaced blob only if the table has that column (Q-REPO-86).
--   * remote_cache_invalidations.folded_at records which rows the fold job has
--     folded into the metadata entries: negative and all rows stay for 86,405 s
--     after the fold and must be told apart from the unfolded ones (Q-REPO-87).
--   * defensive CHECKs: the length of last_failure and the form of the JSON
--     pointer in repository_secrets.field.
--
-- cache_invalidation and upstream_state are change areas (R-ARCH-018); their
-- epoch rows are seeded here. None of these tables has a revision column: the
-- ETag of a remote repository is repositories.config_version (R-REPO-011).

INSERT INTO cluster_epoch (area) VALUES ('cache_invalidation'), ('upstream_state');

-- kind 'artifact': the entry of a stored tarball; its item is the file and shares
-- the blob. kind 'metadata': a document that is a cache entry only, with an expiry
-- and, when the upstream answered 404 or 410 for it, the gone mark (R-REPO-029).
-- last_used_at equals fetched_at until R-REPO-046 (P1) batches it.
CREATE TABLE remote_cache_entries (
    id                     bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    repo_id                bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    path                   text COLLATE "C" NOT NULL,
    variant                text NOT NULL DEFAULT '',
    kind                   text NOT NULL CHECK (kind IN ('artifact', 'metadata')),
    item_id                bigint REFERENCES items (id) ON DELETE CASCADE,
    blob_sha256            bytea NOT NULL REFERENCES blobs (sha256),
    upstream_url           text NOT NULL,
    upstream_etag          text,
    upstream_last_modified timestamptz,
    fetched_at             timestamptz NOT NULL,
    validated_at           timestamptz NOT NULL,
    expires_at             timestamptz,
    upstream_gone_at       timestamptz,
    last_used_at           timestamptz NOT NULL,
    PRIMARY KEY (repo_id, path, variant),
    CHECK ((kind = 'artifact') = (item_id IS NOT NULL)),
    CHECK ((kind = 'artifact') = (expires_at IS NULL)),
    CHECK (kind = 'metadata' OR upstream_gone_at IS NULL)
);
CREATE INDEX remote_cache_entries_blob   ON remote_cache_entries (blob_sha256);
CREATE INDEX remote_cache_entries_item   ON remote_cache_entries (item_id) WHERE item_id IS NOT NULL;
CREATE INDEX remote_cache_entries_unused ON remote_cache_entries (repo_id, last_used_at) WHERE kind = 'metadata';
CREATE INDEX remote_cache_entries_gone   ON remote_cache_entries (repo_id) WHERE upstream_gone_at IS NOT NULL;

CREATE TRIGGER remote_cache_entries_blob_guard BEFORE INSERT OR UPDATE OF blob_sha256 ON remote_cache_entries
    FOR EACH ROW EXECUTE FUNCTION blob_ref_guard();
CREATE TRIGGER remote_cache_entries_blob_release_del AFTER DELETE ON remote_cache_entries
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release();
CREATE TRIGGER remote_cache_entries_blob_release_upd AFTER UPDATE ON remote_cache_entries
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release_update();

-- The upstream URL a path was fetched from when it is not the upstream base plus
-- the canonical path (R-REPO-043).
CREATE TABLE remote_url_map (
    repo_id      bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    path         text COLLATE "C" NOT NULL,
    upstream_url text NOT NULL,
    recorded_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, path)
);

-- The circuit of one remote repository (R-REPO-029); every replica keeps an
-- in-memory copy that the change area upstream_state refreshes.
CREATE TABLE remote_upstream_state (
    repo_id              bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    open_until           timestamptz,
    last_failure_at      timestamptz,
    last_failure         text CHECK (length(last_failure) <= 1024),
    last_success_at      timestamptz
);

-- prefix '' is the whole repository. Every replica loads the rows (change area
-- cache_invalidation); the fold job sets folded_at (R-REPO-045).
CREATE TABLE remote_cache_invalidations (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    prefix     text COLLATE "C" NOT NULL,
    scope      text NOT NULL CHECK (scope IN ('metadata', 'negative', 'all')),
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    folded_at  timestamptz
);
CREATE INDEX remote_cache_invalidations_repo     ON remote_cache_invalidations (repo_id);
CREATE INDEX remote_cache_invalidations_unfolded ON remote_cache_invalidations (created_at) WHERE folded_at IS NULL;

-- The secret fields of a repository's configuration (R-REPO-033). field is the
-- JSON pointer of the field in the document. sealed is the value sealed with the
-- column registry of internal/secrets (primary key repo_id, field); secret_ref is
-- the reference to a file or environment variable of the configuration-as-code
-- mechanism. Exactly one of the two is set.
CREATE TABLE repository_secrets (
    repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    field      text NOT NULL CHECK (field ~ '^/[A-Za-z0-9/._~-]{1,255}$'),
    sealed     text,
    secret_ref jsonb,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, field),
    CHECK ((sealed IS NULL) <> (secret_ref IS NULL))
);
