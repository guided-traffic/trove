-- trove:phase expand
--
-- Vulnerability scanning (R-VULN-004, R-VULN-006, R-VULN-008, R-VULN-010, R-VULN-012), beta phase 6, expand class
-- (R-HA-018). The eight tables are new and empty, so they take their keys and indexes inline. The one existing table
-- the feature adds an index to is packages (the lookup of the advisory-driven evaluation by name, R-VULN-011): that
-- index is built CONCURRENTLY in 00036, as the index of 00027 is (Q-VULN-24).
--
-- One change area carries the per-replica copy of the source state (R-VULN-027): vuln_data. Its cluster_epoch row is
-- seeded here. Valkey holds none of the tables.
--
-- Export classes (R-VULN-026, Q-VULN-13): vuln_finding and vuln_finding_event are data (triage is work people did); the
-- other six hold public data that the next sync fetches again and are transient. The registrations are in
-- internal/meta/postgres/tables_vuln.go.
--
-- The deviations from the page's data model (Q-VULN-24): the column vuln_advisory.cwe_ids, the CWE identifiers of an
-- advisory from database_specific.cwe_ids, which the npm audit answer carries as `cwe` (R-VULN-017); and the quoting of
-- the column "references", a reserved word that the page's DDL writes bare.

INSERT INTO cluster_epoch (area) VALUES ('vuln_data');

-- The state of one source and ecosystem (R-VULN-004). data_epoch is raised by every run that changed records; the
-- replica's cache of this table is keyed by it.
CREATE TABLE vuln_source_state (
    source          text NOT NULL,                  -- 'osv', 'kev'
    ecosystem       text NOT NULL,                  -- OSV ecosystem name; '' for kev
    state           text NOT NULL DEFAULT 'empty' CHECK (state IN ('empty', 'complete')),
    data_epoch      bigint NOT NULL DEFAULT 0,      -- raised by every run that changed records
    etag            text,
    archive_sha256  bytea,                          -- mode repository: digest of the item imported last
    watermark       timestamptz,
    full_synced_at  timestamptz,
    synced_at       timestamptz,                    -- last run that ended changed or unchanged
    attempted_at    timestamptz,
    failures        integer NOT NULL DEFAULT 0,     -- consecutive
    last_error      jsonb,                          -- problem object (R-ARCH-023)
    records         integer NOT NULL DEFAULT 0,
    rejected        integer NOT NULL DEFAULT 0,     -- of the last run
    unevaluable     integer NOT NULL DEFAULT 0,     -- ranges R-VULN-009 cannot evaluate
    matcher_version integer NOT NULL DEFAULT 0,     -- R-VULN-011
    full_evaluation_requested_at timestamptz,       -- R-VULN-011: a full evaluation is due (Q-VULN-18)
    PRIMARY KEY (source, ecosystem)
);

-- One OSV record (R-VULN-006).
CREATE TABLE vuln_advisory (
    ecosystem        text NOT NULL,
    id               text NOT NULL,                      -- as published
    canonical_id     text NOT NULL,                      -- R-VULN-006 aliases rule
    modified         timestamptz NOT NULL,
    published        timestamptz,
    withdrawn        timestamptz,
    kind             text NOT NULL CHECK (kind IN ('vulnerability', 'malicious')),
    aliases          text[] NOT NULL DEFAULT '{}',
    summary          text NOT NULL DEFAULT '',
    details          text NOT NULL DEFAULT '',
    severity         text NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low', 'none', 'unknown')),
    severity_source  text NOT NULL CHECK (severity_source IN ('cvss', 'advisory', 'malicious', 'none')),
    cvss_version     text,                               -- '4.0', '3.1', '3.0', '2.0'
    cvss_vector      text,
    cvss_score       numeric(3,1),
    known_exploited  boolean NOT NULL DEFAULT false,     -- R-VULN-008
    kev_date_added   date,
    "references"     jsonb NOT NULL DEFAULT '[]',
    cwe_ids          text[] NOT NULL DEFAULT '{}',       -- database_specific.cwe_ids (Q-VULN-24)
    record_sha256    bytea NOT NULL,                     -- SHA-256 of the record as received
    PRIMARY KEY (ecosystem, id)
);
CREATE INDEX vuln_advisory_aliases ON vuln_advisory USING gin (aliases);
CREATE INDEX vuln_advisory_canonical ON vuln_advisory (ecosystem, canonical_id);

-- One affected entry of an advisory (R-VULN-006).
CREATE TABLE vuln_affected (
    ecosystem      text NOT NULL,
    advisory_id    text NOT NULL,
    ord            smallint NOT NULL,                    -- position in affected[]
    name           text NOT NULL,                        -- normalized by the format
    ranges         jsonb NOT NULL DEFAULT '[]',          -- [{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "1.2.3"}]}]
    versions       text[] NOT NULL DEFAULT '{}',         -- normalized by the format
    every_version  boolean NOT NULL,                     -- one range [{"introduced": "0"}] and no versions[]
    fixed          text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (ecosystem, advisory_id, ord),
    FOREIGN KEY (ecosystem, advisory_id) REFERENCES vuln_advisory (ecosystem, id) ON DELETE CASCADE
);
CREATE INDEX vuln_affected_name ON vuln_affected (ecosystem, name);

-- The advisories whose stored versions the next vuln-evaluate run evaluates (R-VULN-011).
CREATE TABLE vuln_pending_evaluation (
    ecosystem    text NOT NULL,
    advisory_id  text NOT NULL,
    enqueued_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ecosystem, advisory_id)
);

-- The CISA known-exploited catalog (R-VULN-008).
CREATE TABLE vuln_kev (
    cve_id      text PRIMARY KEY,
    date_added  date NOT NULL,
    due_date    date,
    ransomware  text                                     -- knownRansomwareCampaignUse
);

-- Public advisories that name an internal name (R-VULN-010); recomputed by evaluation.
CREATE TABLE vuln_claimed_match (
    ecosystem      text NOT NULL,
    advisory_id    text NOT NULL,                        -- canonical
    name           text NOT NULL,
    kind           text NOT NULL,
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ecosystem, advisory_id, name)
);

-- A finding: one canonical advisory affects one stored version in one repository (R-VULN-012). revision is the ETag
-- of R-API-012 and a revision column: a restore lifts it above every value the old installation issued (R-BAK-022).
CREATE TABLE vuln_finding (
    id                        uuid PRIMARY KEY,          -- UUIDv7
    repo_id                   bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    ecosystem                 text NOT NULL,
    package_name              text NOT NULL,             -- packages.name
    version                   text NOT NULL,             -- package_versions.version
    advisory_id               text NOT NULL,             -- canonical
    package_id                bigint REFERENCES packages (id) ON DELETE SET NULL,
    version_id                bigint REFERENCES package_versions (id) ON DELETE SET NULL,
    kind                      text NOT NULL CHECK (kind IN ('vulnerability', 'malicious')),
    severity                  text NOT NULL,
    severity_rank             smallint NOT NULL,         -- critical 5, high 4, medium 3, low 2, none 1, unknown 0
    cvss_score                numeric(3,1),
    known_exploited           boolean NOT NULL DEFAULT false,
    fix_available             boolean NOT NULL,
    fixed_versions            text[] NOT NULL DEFAULT '{}',
    status                    text NOT NULL DEFAULT 'needs_triage'
                              CHECK (status IN ('needs_triage', 'confirmed', 'blocked', 'dismissed', 'resolved')),
    status_before_resolution  text,
    dismissal_reason          text CHECK (dismissal_reason IN ('not_exploitable', 'false_positive', 'risk_accepted', 'mitigated', 'test_only')),
    comment                   text CHECK (length(comment) <= 4000),
    resolved_reason           text CHECK (resolved_reason IN ('no_longer_affected', 'advisory_withdrawn', 'version_removed', 'scanning_disabled')),
    revision                  bigint NOT NULL DEFAULT 1, -- ETag of R-API-012
    advisory_modified         timestamptz NOT NULL,
    detected_at               timestamptz NOT NULL DEFAULT now(),
    resolved_at               timestamptz,
    status_changed_at         timestamptz NOT NULL DEFAULT now(),
    status_changed_by         uuid,
    UNIQUE (repo_id, ecosystem, package_name, version, advisory_id)
);
CREATE INDEX vuln_finding_open_version ON vuln_finding (version_id) WHERE status IN ('needs_triage', 'confirmed', 'blocked');
CREATE INDEX vuln_finding_advisory ON vuln_finding (ecosystem, advisory_id);
CREATE INDEX vuln_finding_severity ON vuln_finding (severity_rank, cvss_score, id);
CREATE INDEX vuln_finding_detected ON vuln_finding (detected_at, id);
CREATE INDEX vuln_finding_package ON vuln_finding (package_name, id);

-- The history of a finding (R-VULN-012).
CREATE TABLE vuln_finding_event (
    finding_id   uuid NOT NULL REFERENCES vuln_finding (id) ON DELETE CASCADE,
    seq          bigint GENERATED ALWAYS AS IDENTITY,
    at           timestamptz NOT NULL DEFAULT now(),
    actor        uuid,                                   -- NULL for trove
    change       text NOT NULL CHECK (change IN ('detected', 'status_changed', 'resolved', 'redetected', 'reopened', 'advisory_changed')),
    from_status  text,
    to_status    text,
    reason       text,
    comment      text,
    details      jsonb,                                  -- the advisory change, the bulk operation id
    PRIMARY KEY (finding_id, seq)
);
