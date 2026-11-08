-- trove:phase contract
-- trove:requires-app >= 0.1.0
--
-- The key grammar of Q-OCI-3 (R-OCI-002): a repository key and a project key are path components of every image reference, so both
-- are valid OCI name components for every format. A repository key is 2 to 64 characters of ^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$
-- (no key that ends in a separator, none with two different separators in a row), a project key 2 to 32 characters of
-- ^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$. The checks of 00002 and 00019 are replaced, and so are the checks that embed the project-key
-- grammar in a composite name (a project's service account, policy and CI mapping, and the tier of a mapping), so that no table
-- accepts a project part the project table refuses. Replacing a check is a DROP, which only a contract step may hold (R-HA-018).
--
-- The step tightens, and the checks are added in their validated form: the statement scans the table under a lock and stops at a row
-- the new grammar refuses, naming the constraint. A key is immutable (R-REPO-002, R-AUTHZ-026), so the remedy is not a rename: the
-- operator deletes that repository or project on the previous release, or starts from a new database. Pre-alpha has no installation
-- that has to survive this (the owner's rule of 2026-11-06).
--
-- requires-app names the first release, 0.1.0, as the step before it (00033) does, so the compatibility floor stays where it is.
-- The reason is practical and not a claim that the step is harmless to older releases: the integration harnesses migrate as release
-- 0.1.0, and a floor above it would make every one of them unservable. An older release that is still live would see a key its own
-- validation accepts and the check refuses fail that insert, and nothing else; from the first stable release a step like this one
-- names the release that validates the new grammar.

-- repositories_key_dots (00010) said "no .. and no trailing ." on top of the old character class; the grammar says it by itself.
ALTER TABLE repositories
    DROP CONSTRAINT repositories_key_dots,
    DROP CONSTRAINT repositories_key_check,
    ADD CONSTRAINT repositories_key_check CHECK (
        char_length(key) BETWEEN 2 AND 64 AND key ~ '^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$');

ALTER TABLE project
    DROP CONSTRAINT project_key_check,
    ADD CONSTRAINT project_key_check CHECK (
        char_length(key) BETWEEN 2 AND 32 AND key ~ '^[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$');

-- The project part of a composite name is the project key: 2 to 32 characters of the project grammar before the slash. The look-ahead
-- bounds the length of the part, which the expression alone cannot.
ALTER TABLE principal
    DROP CONSTRAINT principal_check,
    ADD CONSTRAINT principal_check CHECK (CASE kind
        WHEN 'user'            THEN name ~ '^[a-z0-9][a-z0-9._@+-]{0,127}$' AND name NOT IN ('anonymous', 'authenticated', 'system', 'me')
        WHEN 'group'           THEN name ~ '^[a-z0-9][a-z0-9._-]{0,127}$'
        WHEN 'service_account' THEN name ~ '^((?=[a-z0-9-]{2,32}/)[a-z][a-z0-9]*(?:-+[a-z0-9]+)*/)?[a-z0-9][a-z0-9-]{1,62}$'
        WHEN 'anonymous'       THEN name = 'anonymous'
        WHEN 'authenticated'   THEN name = 'authenticated'
        WHEN 'ci_mapping'      THEN name ~ '^[a-z0-9][a-z0-9._-]{1,63}/((?=[a-z0-9-]{2,32}/)[a-z][a-z0-9]*(?:-+[a-z0-9]+)*/)?[a-z0-9][a-z0-9._-]{1,63}$'
    END);

ALTER TABLE authz_policy
    DROP CONSTRAINT authz_policy_name_check,
    ADD CONSTRAINT authz_policy_name_check CHECK (
        name ~ '^((?=[a-z0-9-]{2,32}/)[a-z][a-z0-9]*(?:-+[a-z0-9]+)*/)?[a-z0-9][a-z0-9._-]{0,127}$');

ALTER TABLE ci_mappings
    DROP CONSTRAINT ci_mappings_tier_check,
    ADD CONSTRAINT ci_mappings_tier_check CHECK (
        tier = 'system' OR tier ~ '^delegation:[0-9a-f-]{36}$'
        OR (tier ~ '^project:[a-z][a-z0-9]*(?:-+[a-z0-9]+)*$' AND char_length(tier) BETWEEN 10 AND 40));
