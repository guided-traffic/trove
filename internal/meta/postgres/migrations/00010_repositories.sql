-- trove:phase expand
--
-- Repository model (M1, WP1.4): the columns that R-REPO-001 adds to the table
-- repositories of 00002_storage_core. config (R-REPO-004) keeps the settings
-- document, including the authorization settings cacheFillAction,
-- packageOwnership, requireAuthentication and hideExistence (R-AUTHZ-031).
--
-- Not added yet, because their requirements are not in M1: claims_version (the
-- namespace-claims ETag, R-REPO-023) arrives with the claims table, and
-- project_id (R-AUTHZ-026) with the projects table.
--
-- Every repository create, delete and change of an input of the grant index
-- records itself in the change area `repo_config` in its transaction
-- (R-AUTHZ-028, R-REPO-009 to R-REPO-013); the area's row of cluster_epoch is
-- seeded here.

INSERT INTO cluster_epoch (area) VALUES ('repo_config');

-- lifecycle 'deleting' marks a repository whose deletion job runs: it is not
-- resolvable, and its key stays taken until the job removes the row (R-REPO-013).
-- config_version is the ETag of the configuration (R-REPO-011) and is registered
-- with meta.RegisterRevisionColumn. last_activity_at is written at most once a
-- minute per repository and replica (R-REPO-012). created_by and updated_by name
-- a principal without a foreign key, so a repository outlives its creator.
ALTER TABLE repositories
    ADD COLUMN lifecycle         text        NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'deleting')),
    ADD COLUMN config_version    bigint      NOT NULL DEFAULT 1,
    ADD COLUMN created_by        uuid,
    ADD COLUMN updated_by        uuid,
    ADD COLUMN last_activity_at  timestamptz,
    ADD CONSTRAINT repositories_key_dots CHECK (key !~ '\.\.' AND key !~ '\.$');
