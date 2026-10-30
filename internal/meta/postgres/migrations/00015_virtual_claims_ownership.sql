-- trove:phase expand
--
-- Virtual repositories, namespace claims and package ownership (M2, WP2.S;
-- R-REPO-017 to R-REPO-023, R-AUTHZ-018). Members, deploy targets and pin rules
-- are relational: the foreign keys carry the rule of R-REPO-013 that a repository
-- which is a member or a target cannot be deleted, and the repository document is
-- rendered from the tables. The M1 releases never stored a virtual section
-- (Q-REPO-16), so nothing is backfilled.
--
-- claims_version is the ETag of the claims resource (R-REPO-023) and is registered
-- with meta.RegisterRevisionColumn. The area claims is a change area: claims, pin
-- rules and the auto claim of a first publish write it (R-ARCH-018).
--
-- package_owner.ci_issuer_id has no foreign key yet: ci_issuers belongs to M4
-- (R-CI-002). M4 adds the constraint as NOT VALID and validates it afterwards,
-- both of which an expand file allows (Q-ARCH-191).

INSERT INTO cluster_epoch (area) VALUES ('claims');

ALTER TABLE repositories ADD COLUMN claims_version bigint NOT NULL DEFAULT 1 CHECK (claims_version >= 1);

CREATE TABLE virtual_members (
    virtual_id bigint  NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    position   integer NOT NULL CHECK (position >= 0),
    member_id  bigint  NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT,
    PRIMARY KEY (virtual_id, position),
    UNIQUE (virtual_id, member_id),
    CHECK (virtual_id <> member_id)
);
CREATE INDEX virtual_members_member ON virtual_members (member_id);

CREATE TABLE virtual_deploy_targets (
    virtual_id bigint PRIMARY KEY REFERENCES repositories (id) ON DELETE CASCADE,
    target_id  bigint NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT
);
CREATE INDEX virtual_deploy_targets_target ON virtual_deploy_targets (target_id);

CREATE TABLE namespace_claims (
    repo_id    bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    pattern    text NOT NULL CHECK (length(pattern) BETWEEN 1 AND 256),
    source     text NOT NULL CHECK (source IN ('manual', 'auto')),
    scope      text NOT NULL DEFAULT 'members' CHECK (scope IN ('members', 'global')),
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid,
    PRIMARY KEY (repo_id, pattern),
    CHECK (source = 'manual' OR scope = 'members')
);

CREATE TABLE virtual_pin_rules (
    virtual_id       bigint  NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    position         integer NOT NULL CHECK (position >= 0),
    package_patterns text[]  NOT NULL CHECK (cardinality(package_patterns) BETWEEN 1 AND 50),
    PRIMARY KEY (virtual_id, position)
);
CREATE TABLE virtual_pin_members (
    virtual_id bigint  NOT NULL,
    position   integer NOT NULL,
    member_id  bigint  NOT NULL REFERENCES repositories (id) ON DELETE RESTRICT,
    PRIMARY KEY (virtual_id, position, member_id),
    FOREIGN KEY (virtual_id, position) REFERENCES virtual_pin_rules ON DELETE CASCADE
);
CREATE INDEX virtual_pin_members_member ON virtual_pin_members (member_id);

-- domain is 'repo:<repositories.id>' or 'domain:<name>' (R-AUTHZ-020, P1). The
-- rows of a repository carry no foreign key; its delete job removes them
-- (R-AUTHZ-018).
CREATE TABLE package_ownership (
    domain       text NOT NULL CHECK (domain ~ '^(repo:[0-9]+|domain:.+)$'),
    format       text NOT NULL,
    package_name text NOT NULL,
    state        text NOT NULL CHECK (state IN ('claimed', 'released')),
    claimed_at   timestamptz NOT NULL DEFAULT now(),
    claim_event  uuid,
    PRIMARY KEY (domain, format, package_name)
);
CREATE TABLE package_owner (
    id            uuid PRIMARY KEY,
    domain        text NOT NULL,
    format        text NOT NULL,
    package_name  text NOT NULL,
    owner_kind    text NOT NULL CHECK (owner_kind IN ('principal', 'ci_project')),
    principal_id  uuid REFERENCES principal (id) ON DELETE CASCADE,
    ci_issuer_id  uuid,
    ci_project_id text CHECK (length(ci_project_id) <= 64),
    display       text NOT NULL,
    reason        text NOT NULL CHECK (reason IN ('first-publish', 'added', 'pre-assigned')),
    added_by      uuid REFERENCES principal (id) ON DELETE SET NULL,
    added_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (domain, format, package_name) REFERENCES package_ownership ON DELETE CASCADE,
    CHECK ((owner_kind = 'principal')  = (principal_id IS NOT NULL)),
    CHECK ((owner_kind = 'ci_project') = (ci_issuer_id IS NOT NULL AND ci_project_id IS NOT NULL))
);
CREATE UNIQUE INDEX package_owner_unique ON package_owner
    (domain, format, package_name, coalesce(principal_id::text, ci_issuer_id::text || ':' || ci_project_id));
CREATE INDEX package_owner_principal ON package_owner (principal_id) WHERE principal_id IS NOT NULL;
CREATE INDEX package_owner_added_by  ON package_owner (added_by) WHERE added_by IS NOT NULL;
