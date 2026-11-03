-- trove:phase contract
-- trove:requires-app >= 0.1.0
--
-- The principal kind 'bastion' and the token type 'bst' (R-BAS-004, R-BAS-005, Q-BAS-23, Q-BAS-48). The
-- two checks list their values, so a value is added by replacing the check: a DROP, which only a contract
-- step may hold (R-HA-018). The step only loosens: every row that was valid stays valid and every release
-- reads the tables unchanged, so no release is made unable to serve and requires-app names the first
-- release, 0.1.0, which declares the floor as low as it can be. The compatibility floor therefore stays
-- at 0.1.0 and the contract guard has nothing to refuse; the step is a contract step only because the
-- classification of R-HA-018 puts every DROP of a constraint there (Q-BAS-48).
--
-- The new checks are added NOT VALID and validated by 00034 under a lock that lets reads and writes
-- through; DROP and ADD in one statement keep the table free of a window without a check.

ALTER TABLE principal
    DROP CONSTRAINT principal_kind_check,
    ADD CONSTRAINT principal_kind_check
        CHECK (kind IN ('user', 'group', 'service_account', 'anonymous', 'authenticated', 'ci_mapping', 'bastion')) NOT VALID;

ALTER TABLE api_token
    DROP CONSTRAINT api_token_type_check,
    ADD CONSTRAINT api_token_type_check CHECK (type IN ('pat', 'sat', 'cit', 'bst')) NOT VALID;
