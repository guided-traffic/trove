-- trove:phase expand
--
-- The OCI format's tables (R-OCI-007, R-OCI-033, Q-OCI-7): a row per manifest of an image name with the facts a referrers answer and a
-- package screen need, a row per reference from a manifest to a manifest or a blob of the same name, and the mark of a manifest the
-- journal replay withheld. They carry no blob reference (no blob_sha256 column, R-STOR-003 rule 1), so they never hold a blob against garbage
-- collection; the items do. The rows are written by the storage hooks in the transaction that writes the items (OnCommit,
-- OnItemsRemoved). They stay when an item goes to the trash, as NuGet's rows do, and go with the manifest item's removal.
--
-- annotations_cut records that the annotations a referrers answer needs were cut at 64 KiB (R-OCI-007): the answer then says so.
-- The referrers index carries the digest, so a page of referrers is read in order from the index alone.
CREATE TABLE oci_manifests (
    repo_id          bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name             text        COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    digest           bytea       NOT NULL CHECK (octet_length(digest) = 32),
    media_type       text        NOT NULL CHECK (length(media_type) <= 255),
    artifact_type    text        CHECK (length(artifact_type) <= 255),
    subject          bytea       CHECK (subject IS NULL OR octet_length(subject) = 32),
    size             bigint      NOT NULL CHECK (size >= 0),
    content_size     bigint      NOT NULL DEFAULT 0 CHECK (content_size >= 0),
    annotations      jsonb       CHECK (annotations IS NULL OR octet_length(annotations::text) <= 65536),
    annotations_cut  boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, name, digest)
);
CREATE INDEX oci_manifests_subject ON oci_manifests (repo_id, name, subject, digest) WHERE subject IS NOT NULL;

-- platform is the "os/arch[/variant]" an index names for the child manifest the edge points at; the edge is the one place the fact
-- lives, because only the index knows it. NULL for every other edge and for blobs.
CREATE TABLE oci_references (
    repo_id   bigint NOT NULL,
    name      text   COLLATE "C" NOT NULL,
    manifest  bytea  NOT NULL,
    target    bytea  NOT NULL CHECK (octet_length(target) = 32),
    kind      text   NOT NULL CHECK (kind IN ('manifest', 'blob')),
    platform  text   CHECK (length(platform) <= 128),
    PRIMARY KEY (repo_id, name, manifest, target),
    CHECK (kind = 'manifest' OR platform IS NULL),
    FOREIGN KEY (repo_id, name, manifest) REFERENCES oci_manifests (repo_id, name, digest) ON DELETE CASCADE
);
CREATE INDEX oci_references_target ON oci_references (repo_id, name, target);

-- A manifest the journal replay withheld after a restore (R-OCI-033, Q-OCI-33): an image that was deleted after the snapshot may have been
-- malicious, and a restore must not serve it again. The replay never deletes content (R-HA-054), so it writes this row and leaves the
-- items and the bytes stored; the serve check refuses the manifest and the push of the same digest under the same name until an
-- administrator reinstates it, which removes the row. A live deletion writes no row: the digest may be pushed again. The row has no
-- foreign key to the manifest row or the item (nuget_tombstone's rule), only to the repository. withheld_at is when the replay
-- withheld it, deleted_by the principal of the journaled deletion.
CREATE TABLE oci_tombstones (
    repo_id      bigint      NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    name         text        COLLATE "C" NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    digest       bytea       NOT NULL CHECK (octet_length(digest) = 32),
    withheld_at  timestamptz NOT NULL DEFAULT now(),
    deleted_by   uuid,
    PRIMARY KEY (repo_id, name, digest)
);

-- The media type of a stored metadata document of a remote repository, as the upstream sent it without parameters
-- (RemoteDocument.MediaType, R-OCI-022): a manifest fetched by tag is served with the type it was fetched with. 00014 is not edited.
ALTER TABLE remote_cache_entries ADD COLUMN media_type text CHECK (length(media_type) <= 255);
