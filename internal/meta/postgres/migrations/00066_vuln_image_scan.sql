-- The scanning of container images (R-VULN-033 to R-VULN-037): what an analysis found out about a manifest, how far each manifest of a
-- repository has been scanned, and which vulnerability database the installation uses. A finding of an image names the operating-system
-- and language packages inside the image that the advisory affects (R-VULN-012, Q-VULN-135).

ALTER TABLE vuln_finding ADD COLUMN components jsonb NOT NULL DEFAULT '[]';

-- One manifest of one image name in one repository. The row goes with the manifest (a cascade of the foreign key); the job then resolves
-- the findings that have no row any more.
CREATE TABLE vuln_image_scan (
    repo_id       bigint NOT NULL,
    name          text COLLATE "C" NOT NULL,
    digest        bytea NOT NULL CHECK (octet_length(digest) = 32),
    state         text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'scanned', 'failed')),
    not_before    timestamptz NOT NULL DEFAULT now(),
    attempts      integer NOT NULL DEFAULT 0,
    db_digest     text,                                 -- the database of the last detection; another current one makes the row due again
    scanned_at    timestamptz,
    error_code    text,
    error_detail  text CHECK (length(error_detail) <= 1024),
    os_family     text CHECK (length(os_family) <= 64),
    os_name       text CHECK (length(os_name) <= 64),
    os_eosl       boolean NOT NULL DEFAULT false,
    packages      integer NOT NULL DEFAULT 0,
    PRIMARY KEY (repo_id, name, digest),
    FOREIGN KEY (repo_id, name, digest) REFERENCES oci_manifests (repo_id, name, digest) ON DELETE CASCADE
);
CREATE INDEX vuln_image_scan_due ON vuln_image_scan (not_before) WHERE state IN ('pending', 'failed');
CREATE INDEX vuln_image_scan_db ON vuln_image_scan (db_digest) WHERE state = 'scanned';

-- What the analysis of the layers found, by manifest digest and analyser version: a detection against a newer database reads it instead
-- of the layers, and two repositories that hold one image share it. Gzip of a CycloneDX document.
CREATE TABLE vuln_image_sbom (
    digest      bytea NOT NULL CHECK (octet_length(digest) = 32),
    analyser    text NOT NULL,
    sbom        bytea NOT NULL CHECK (octet_length(sbom) <= 67108864),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (digest, analyser)
);

-- The vulnerability database in use: one row. The archive is an object of the blob store, and this row is the reference that keeps it
-- (the reference rules of R-STOR-003: blob_sha256 with its foreign key, an index, the guard trigger and the two release triggers).
CREATE TABLE vuln_image_db (
    id           bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    singleton    smallint PRIMARY KEY DEFAULT 1 CHECK (singleton = 1),
    digest       text NOT NULL,                         -- the SHA-256 of the archive, "sha256:<hex>"; also what a detection records
    blob_sha256  bytea NOT NULL REFERENCES blobs (sha256),
    size         bigint NOT NULL CHECK (size >= 0),
    updated_at   timestamptz NOT NULL,                  -- metadata.json of the archive: when the database was built
    next_update  timestamptz,
    imported_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX vuln_image_db_blob ON vuln_image_db (blob_sha256);

CREATE TRIGGER vuln_image_db_blob_guard BEFORE INSERT OR UPDATE OF blob_sha256 ON vuln_image_db
    FOR EACH ROW EXECUTE FUNCTION blob_ref_guard();
CREATE TRIGGER vuln_image_db_blob_release_del AFTER DELETE ON vuln_image_db
    REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release();
CREATE TRIGGER vuln_image_db_blob_release_upd AFTER UPDATE ON vuln_image_db
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION blob_ref_release_update();
