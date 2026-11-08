-- trove:phase expand
--
-- The signing keys of the Debian (APT) format's local and virtual repositories (R-DEB-015, Q-DEB-9, Q-DEB-10, Q-DEB-31): a key
-- belongs to one repository, and a repository has at most one primary and one next key (the partial unique indexes). The fingerprint
-- is unique in every state and every repository, so a retired or withheld key cannot come back by import.
--
-- private_key is sealed with the stored-secret key ring (R-ARCH-021, registered in internal/secrets with the row key id) and is NULL
-- exactly in state retired; a withheld key keeps it. key_created is the key's own creation time (one hour before the creating
-- transaction, Q-DEB-30) and bound_since the creation time of its earliest valid binding self-signature (Q-DEB-40).
CREATE TABLE deb_signing_keys (
    id           uuid        PRIMARY KEY,
    repo_id      bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    fingerprint  text        NOT NULL CHECK (fingerprint ~ '^[0-9A-F]{40}$'),
    algorithm    smallint    NOT NULL CHECK (algorithm IN (1, 22)),
    bits         integer     NOT NULL,
    key_created  timestamptz NOT NULL,
    bound_since  timestamptz NOT NULL,
    state        text        NOT NULL CHECK (state IN ('primary', 'next', 'withheld', 'retired')),
    origin       text        NOT NULL CHECK (origin IN ('generated', 'imported')),
    public_key   text        NOT NULL CHECK (octet_length(public_key) <= 65536),
    private_key  text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    created_by   uuid,
    promoted_at  timestamptz,
    withheld_at  timestamptz,
    retired_at   timestamptz,
    retired_by   uuid,
    CHECK ((state = 'retired') = (private_key IS NULL))
);
CREATE UNIQUE INDEX deb_signing_keys_primary ON deb_signing_keys (repo_id) WHERE state = 'primary';
CREATE UNIQUE INDEX deb_signing_keys_next ON deb_signing_keys (repo_id) WHERE state = 'next';
CREATE UNIQUE INDEX deb_signing_keys_fpr ON deb_signing_keys (fingerprint);
