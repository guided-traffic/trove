-- trove:phase expand
--
-- The trove Bastion on the main installation (R-BAS-004, R-BAS-010, Q-BAS-23, Q-BAS-48), beta phase 5b,
-- expand class (R-HA-018). A Bastion registration is a principal of the kind 'bastion' (the kind and the
-- token type 'bst' are admitted by 00033, the one contract step of this feature) plus the row of this
-- table, the way a service account is a principal plus a service_account row. It holds no grant, no role
-- binding, no owner record and no membership: the role binding and membership guards of 00007 and 00011
-- name the kinds they admit and 'bastion' is none of them (SQLSTATE AI001).
--
-- bastion_contact is the last contact of a replica of a registration (R-BAS-010): the main installation
-- writes it from the handshake at most once a minute per replica. A replica's pod name changes with every
-- rollout, so the rows of replicas that have not been heard for 30 days are pruned by the writer
-- (queries/bastion_contact.sql, BastionContactPrune). The table is transient: a restored installation
-- shows no contact until the Bastions handshake again.
--
-- The feed (R-BAS-015) needs no index of its own. It reads audit_event by occurred_at within the lookback
-- of the cursor and filters the journaled actions; the primary key (occurred_at, id) serves the range, the
-- partitions prune by time, and the window is minutes long. An index on the partitioned table cannot be
-- built CONCURRENTLY, and a partial index per partition would not shorten a scan of that size.

CREATE TABLE bastion (
    principal_id  uuid PRIMARY KEY REFERENCES principal (id) ON DELETE CASCADE,
    description   text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1024)
);
CREATE TRIGGER bastion_kind BEFORE INSERT OR UPDATE OF principal_id ON bastion
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'bastion');

-- The description is a member of the registration's representation, so a change of it is a new revision
-- (principal.revision is the ETag, 00013).
CREATE TRIGGER bastion_revision AFTER UPDATE ON bastion
    FOR EACH ROW WHEN (OLD.description IS DISTINCT FROM NEW.description)
    EXECUTE FUNCTION principal_revision_bump('principal_id');

-- The name rule of the kind (R-BAS-004). The CASE of the check of 00007 has no branch for the kind and
-- passes it; this check is the rule, added NOT VALID and validated by 00034.
ALTER TABLE principal ADD CONSTRAINT principal_bastion_name
    CHECK (kind <> 'bastion' OR name ~ '^[a-z][a-z0-9-]{0,19}$') NOT VALID;

-- A Bastion token belongs to a registration: the guard of api_token.principal_id admits the new kind.
CREATE OR REPLACE TRIGGER api_token_kind BEFORE INSERT OR UPDATE OF principal_id ON api_token
    FOR EACH ROW EXECUTE FUNCTION principal_kind_guard('principal_id', 'user', 'service_account', 'ci_mapping', 'bastion');

CREATE TABLE bastion_contact (
    bastion_id         uuid        NOT NULL REFERENCES bastion (principal_id) ON DELETE CASCADE,
    replica            text        NOT NULL CHECK (char_length(replica) BETWEEN 1 AND 253),
    contact_at         timestamptz NOT NULL,
    client_address     inet,
    release            text        NOT NULL DEFAULT '' CHECK (char_length(release) <= 64),
    interface_version  integer     NOT NULL CHECK (interface_version >= 1),
    base_url           text        NOT NULL DEFAULT '' CHECK (char_length(base_url) <= 2048),
    token_id           uuid,
    PRIMARY KEY (bastion_id, replica)
);
CREATE INDEX bastion_contact_at ON bastion_contact (contact_at);
