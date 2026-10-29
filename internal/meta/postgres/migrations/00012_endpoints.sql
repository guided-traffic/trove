-- trove:phase expand
--
-- Endpoints and exposure (M1, WP1.9). The endpoints themselves (hosts, listeners,
-- accepted credentials, rate-limit class) are configuration-file data (R-EP-001,
-- R-EP-010); only the exposure rules, which an administrator changes at runtime,
-- are stored (R-EP-012). Every write records itself in the change area `authz`,
-- and the grant index holds the rules as a ceiling (R-EP-013).

-- A rule belongs to one endpoint, named as in the configuration, and one
-- repository, stored by id: deleting the repository deletes its rules, and a
-- repository created later under the same key is not exposed. Rules of an endpoint
-- that is not configured stay stored and inert. revision is the ETag of the rule
-- (R-API-012) and is registered with meta.RegisterRevisionColumn.
CREATE TABLE endpoint_exposure_rule (
    id           uuid        PRIMARY KEY,
    endpoint     text        NOT NULL CHECK (endpoint ~ '^[a-z][a-z0-9-]{0,19}$'),
    name         text        NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    description  text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    repo_id      bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    enabled      boolean     NOT NULL DEFAULT true,
    packages     jsonb       NOT NULL DEFAULT '{"include": [], "exclude": []}',
    paths        jsonb       NOT NULL DEFAULT '{"include": [], "exclude": []}',
    versions     jsonb       NOT NULL DEFAULT '{"include": [], "exclude": [], "range": null}',
    prereleases  boolean     NOT NULL DEFAULT false,
    dist_tags    jsonb       NOT NULL DEFAULT '{"include": [], "exclude": []}',
    revision     bigint      NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    created_by   uuid        REFERENCES principal (id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid        REFERENCES principal (id) ON DELETE SET NULL,
    UNIQUE (endpoint, name)
);
CREATE INDEX endpoint_exposure_rule_repo ON endpoint_exposure_rule (repo_id);
