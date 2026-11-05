-- trove:phase expand
--
-- Mirror mode of remote repositories (R-REPO-053, R-REPO-056, R-REPO-058), beta phase 6b, expand class (R-HA-018). The two
-- tables are new and empty, so they take their keys and indexes inline.
--
-- mirror_state holds one row per remote repository in mode mirror: the pass in progress and its fence (pass_job_id,
-- pass_holder: every write of a pass is conditional on both, Q-ARCH-287), the cursor of the last complete pass, failures,
-- rate-limit waits and the bound. The dispatch creates the row; the run operation of R-REPO-056 may create it with only
-- pass_requested_*. mirror_pending holds the names a pass has discovered and not yet filled.
--
-- Export classes (R-BAK-004, Q-REPO-147): mirror_state is data, mirror_pending transient. A restore clears the pass in
-- progress (R-REPO-058); the registrations are in internal/meta/postgres/tables_mirror.go.

CREATE TABLE mirror_state (
    repo_id                 bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
    next_pass_at            timestamptz,
    pass_requested_at       timestamptz,
    pass_requested_by       uuid,
    pass_requested_full     boolean NOT NULL DEFAULT false,
    pass_started_at         timestamptz,
    pass_kind               text CHECK (pass_kind IN ('full', 'incremental')),
    pass_method             text CHECK (pass_method IN ('feed', 'listing', 'names')),
    pass_job_id             uuid,
    pass_holder             uuid,
    pass_cursor             jsonb CHECK (pass_cursor IS NULL OR octet_length(pass_cursor::text) <= 4096),
    pass_discovery_done     boolean NOT NULL DEFAULT false,
    pass_names              bigint NOT NULL DEFAULT 0,
    pass_packages_done      bigint NOT NULL DEFAULT 0,
    pass_packages_failed    bigint NOT NULL DEFAULT 0,
    pass_items_stored       bigint NOT NULL DEFAULT 0,
    pass_bytes_stored       bigint NOT NULL DEFAULT 0,
    pass_discovery_complete boolean NOT NULL DEFAULT true,
    pass_discovery_note     text CHECK (length(pass_discovery_note) <= 256),
    feed_cursor             jsonb CHECK (feed_cursor IS NULL OR octet_length(feed_cursor::text) <= 4096),
    feed_method             text CHECK (feed_method IN ('feed', 'listing', 'names')),
    patterns_fingerprint    bytea,
    last_full_pass_at       timestamptz,
    last_complete_pass_at   timestamptz,
    last_pass               jsonb,
    consecutive_failures    integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_error_at           timestamptz,
    last_error_code         text CHECK (length(last_error_code) <= 64),
    last_error              text CHECK (length(last_error) <= 1024),
    rate_limited_until      timestamptz,
    rate_limit_waits        bigint NOT NULL DEFAULT 0,
    rate_limit_wait_seconds bigint NOT NULL DEFAULT 0,
    last_rate_limit_at      timestamptz,
    last_rate_limit_status  integer,
    bound_reached           text CHECK (bound_reached IN ('bytes', 'quota', 'capacity')),
    bound_reached_at        timestamptz,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    CHECK ((pass_started_at IS NULL) = (pass_kind IS NULL))
);

CREATE TABLE mirror_pending (
    repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name       text COLLATE "C" NOT NULL CHECK (length(name) <= 1024),
    queued_at  timestamptz NOT NULL DEFAULT now(),
    attempts   integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    not_before timestamptz,
    last_error text CHECK (length(last_error) <= 1024),
    PRIMARY KEY (repo_id, name)
);
