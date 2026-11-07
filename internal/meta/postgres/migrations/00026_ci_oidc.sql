-- trove:phase expand
--
-- GitLab CI OIDC federation (R-CI-001, R-CI-003, R-CI-009, R-CI-018, R-CI-020), beta phase 5, expand
-- class (R-HA-018). The tables are new and empty, so they take their keys and indexes inline. The one
-- existing table this step touches is package_owner: the foreign key that 00015 left out because
-- ci_issuers did not exist (R-AUTHZ-018, Q-ARCH-191) is added NOT VALID here, its index is built
-- CONCURRENTLY in 00027 and the key is validated in 00028.
--
-- Two change areas carry the copies of this feature (R-ARCH-018): ci_mappings (issuers and their
-- mappings, R-CI-002, R-CI-009) and ci_issuer_keys (the signing key sets, R-CI-003). Their
-- cluster_epoch rows are seeded here. Valkey holds none of the tables (R-CI-029).

INSERT INTO cluster_epoch (area) VALUES ('ci_mappings'), ('ci_issuer_keys');

-- CI issuers (R-CI-001). version is the ETag (R-CI-002); mappings_version is internal and counts
-- the writes of the issuer's mappings (R-CI-009, Q-CI-15). Both are revision columns: a restore
-- lifts them above every value the old installation issued (R-BAK-022).
CREATE TABLE ci_issuers (
    id                         uuid        PRIMARY KEY,
    name                       text        NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
    kind                       text        NOT NULL CHECK (kind IN ('gitlab', 'github', 'generic')),
    issuer                     text        NOT NULL UNIQUE CHECK (issuer ~ '^https://'),
    discovery_url              text,
    jwks_uri                   text,
    allowed_audiences          text[]      NOT NULL CHECK (cardinality(allowed_audiences) >= 1),
    exclusive_audience         boolean     NOT NULL DEFAULT true,
    allowed_algorithms         text[]      NOT NULL DEFAULT '{RS256}',
    allowed_typ                text[]      NOT NULL DEFAULT '{JWT}',
    allow_missing_typ          boolean     NOT NULL DEFAULT true,
    clock_skew                 interval    NOT NULL DEFAULT '60 seconds' CHECK (clock_skew <= interval '300 seconds'),
    required_claims            text[]      NOT NULL,
    public_issuer              boolean     NOT NULL DEFAULT true,
    pin_claims                 text[]      NOT NULL,
    single_use_subject_tokens  boolean     NOT NULL,
    max_ttl                    interval,
    ca_bundle_ref              text,
    use_outbound_proxy         boolean     NOT NULL DEFAULT false,
    job_token_fallback         jsonb       NOT NULL DEFAULT '{"enabled": false}',
    enabled                    boolean     NOT NULL DEFAULT true,
    description                text        NOT NULL DEFAULT '',
    source                     text        NOT NULL DEFAULT 'api' CHECK (source IN ('api', 'configuration')),
    removed_from_configuration boolean     NOT NULL DEFAULT false,
    version                    bigint      NOT NULL DEFAULT 1,
    mappings_version           bigint      NOT NULL DEFAULT 0,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now()
);

-- The last good key set of each issuer (R-CI-003, Q-CI-12). One row per issuer; a key store locks
-- this row alone and never the ci_issuers row. Backup class transient: a restored installation
-- starts without key sets.
CREATE TABLE ci_issuer_keys (
    issuer_id        uuid        PRIMARY KEY REFERENCES ci_issuers (id) ON DELETE CASCADE,
    jwks             jsonb       NOT NULL,
    jwks_uri         text        NOT NULL,
    fetched_at       timestamptz NOT NULL,
    etag             text,
    next_refresh_at  timestamptz NOT NULL,
    last_error       text,
    last_error_at    timestamptz
);
CREATE INDEX ci_issuer_keys_due ON ci_issuer_keys (next_refresh_at);

-- Identity mappings (R-CI-009). The principal row of kind 'ci_mapping' (00007) is the mapping's
-- principal, name '<issuer>/<mapping>' (Q-CI-10), or '<issuer>/<project key>/<mapping>' for a project's
-- mapping (Q-AUTHZ-214); the service inserts both in one transaction and deletes the principal with the
-- mapping. A project Owner's mapping is of the tier 'project:<key>' and names its project in project_id,
-- so a project with mappings cannot be deleted (ON DELETE RESTRICT; the project service answers 409
-- project_not_empty first). The name column holds the bare name in every tier and is unique within the
-- issuer's tier, so one name may be a system mapping and a mapping of each project (R-CI-009).
-- delegation_id has no foreign key: delegation to a group is P1 (R-CI-023) and adds its table.
CREATE TABLE ci_mappings (
    id             uuid        PRIMARY KEY,
    issuer_id      uuid        NOT NULL REFERENCES ci_issuers (id) ON DELETE RESTRICT,
    principal_id   uuid        NOT NULL UNIQUE REFERENCES principal (id),
    tier           text        NOT NULL DEFAULT 'system'
                               CHECK (tier = 'system' OR tier ~ '^delegation:[0-9a-f-]{36}$' OR tier ~ '^project:[a-z][a-z0-9-]{1,31}$'),
    project_id     uuid        REFERENCES project (id) ON DELETE RESTRICT,
    delegation_id  uuid,
    name           text        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
    priority       integer     NOT NULL CHECK (priority BETWEEN 1 AND 1000000),
    enabled        boolean     NOT NULL DEFAULT true,
    subject_kinds  text[]      NOT NULL DEFAULT '{id_token}',
    match          jsonb       NOT NULL,
    grants         jsonb       NOT NULL,
    max_ttl        interval,
    options        jsonb       NOT NULL DEFAULT '{}',
    description    text        NOT NULL DEFAULT '',
    version        bigint      NOT NULL DEFAULT 1,
    created_by     text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer_id, tier, name),
    UNIQUE (issuer_id, tier, priority),
    CHECK ((tier LIKE 'project:%') = (project_id IS NOT NULL))
);
CREATE INDEX ci_mappings_project ON ci_mappings (project_id) WHERE project_id IS NOT NULL;
CREATE TRIGGER ci_mappings_kind BEFORE INSERT OR UPDATE OF principal_id ON ci_mappings
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'ci_mapping');

-- The replay set (R-CI-018, Q-CI-13): only PostgreSQL holds it. Backup class data.
CREATE TABLE ci_subject_token_uses (
    issuer_id           uuid        NOT NULL REFERENCES ci_issuers (id) ON DELETE CASCADE,
    jti                 text        NOT NULL CHECK (length(jti) <= 256),
    first_exchanged_at  timestamptz NOT NULL,
    last_exchanged_at   timestamptz NOT NULL,
    exchange_count      integer     NOT NULL,
    subject_expires_at  timestamptz NOT NULL,
    PRIMARY KEY (issuer_id, jti)
);
CREATE INDEX ci_subject_token_uses_expiry ON ci_subject_token_uses (subject_expires_at);

-- The issuance record (R-CI-020), written in the exchange transaction next to the api_token row.
-- token_id names that row without a foreign key (Q-CI-20): the token retention task deletes token
-- rows long before ci.issuances.retention ends, and the record must outlive them. issuer_id cascades
-- (Q-CI-20): an issuer is deleted only when an administrator decides it, and its records go with it;
-- the audit events keep the history.
CREATE TABLE ci_token_issuances (
    token_id           uuid        PRIMARY KEY,
    issuer_id          uuid        NOT NULL REFERENCES ci_issuers (id) ON DELETE CASCADE,
    mapping_ids        uuid[]      NOT NULL,
    mapping_names      text[]      NOT NULL,
    subject_kind       text        NOT NULL CHECK (subject_kind IN ('id_token', 'job_token')),
    jti                text,
    project_id         text        NOT NULL,
    project_path       text,
    namespace_id       text,
    pipeline_id        text,
    job_id             text        NOT NULL,
    ref                text,
    ref_type           text,
    ref_protected      boolean,
    pipeline_source    text,
    environment        text,
    sha                text,
    user_id            text,
    user_login         text,
    claims             jsonb       NOT NULL,
    grants             jsonb       NOT NULL,
    requested_scope    text,
    granted_scope      text        NOT NULL,
    cross_project      boolean     NOT NULL DEFAULT false,
    issued_at          timestamptz NOT NULL,
    expires_at         timestamptz NOT NULL,
    client_ip          inet,
    user_agent         text
);
CREATE INDEX ci_issuances_project ON ci_token_issuances (issuer_id, project_id, issued_at DESC);
CREATE INDEX ci_issuances_job     ON ci_token_issuances (issuer_id, job_id);
CREATE INDEX ci_issuances_expiry  ON ci_token_issuances (expires_at);

-- The foreign key that 00015 deferred (R-AUTHZ-018). A package owner of kind ci_project goes with
-- its issuer. Validated in 00028.
ALTER TABLE package_owner ADD CONSTRAINT package_owner_ci_issuer_id_fkey
    FOREIGN KEY (ci_issuer_id) REFERENCES ci_issuers (id) ON DELETE CASCADE NOT VALID;
