-- Statements of the vulnerability scanning, work package sync (docs/developer/vulnerabilities.md). One file per work package;
-- every statement of this file is the package's. Plain Go types only (no pgtype): COALESCE nullable columns.
--
-- The sync writes vuln_source_state, vuln_advisory, vuln_affected, vuln_pending_evaluation and vuln_kev (R-VULN-004,
-- R-VULN-006, R-VULN-008). Times cross the boundary as microseconds since the epoch, 0 for none; the advisory documents
-- of a batch cross as one jsonb array each, so that a batch of 1,000 records is two statements.

-- name: VulnStateEnsure :exec
-- The row of a source and ecosystem, created empty on first use (R-VULN-004 data model).
INSERT INTO vuln_source_state (source, ecosystem) VALUES (sqlc.arg(source)::text, sqlc.arg(ecosystem)::text)
ON CONFLICT (source, ecosystem) DO NOTHING;

-- name: VulnStateGet :one
SELECT source, ecosystem, state, data_epoch, COALESCE(etag, '')::text AS etag, COALESCE(archive_sha256, ''::bytea)::bytea AS archive_sha256,
       COALESCE(extract(epoch FROM watermark) * 1000000, 0)::bigint AS watermark_us,
       COALESCE(extract(epoch FROM full_synced_at) * 1000000, 0)::bigint AS full_synced_us,
       COALESCE(extract(epoch FROM synced_at) * 1000000, 0)::bigint AS synced_us,
       COALESCE(extract(epoch FROM attempted_at) * 1000000, 0)::bigint AS attempted_us,
       failures, COALESCE(last_error, 'null'::jsonb)::jsonb AS last_error, records, rejected, unevaluable, matcher_version,
       COALESCE(extract(epoch FROM full_evaluation_requested_at) * 1000000, 0)::bigint AS eval_requested_us, adapter_version
  FROM vuln_source_state
 WHERE source = sqlc.arg(source)::text AND ecosystem = sqlc.arg(ecosystem)::text;

-- name: VulnStateLock :one
-- The same row, locked for the transaction that ends a run (the run's own writer is exclusive; the lock orders it against a
-- manual run of the other kind and against the evaluation's clearing of the request).
SELECT source, ecosystem, state, data_epoch, COALESCE(etag, '')::text AS etag, COALESCE(archive_sha256, ''::bytea)::bytea AS archive_sha256,
       COALESCE(extract(epoch FROM watermark) * 1000000, 0)::bigint AS watermark_us,
       COALESCE(extract(epoch FROM full_synced_at) * 1000000, 0)::bigint AS full_synced_us,
       COALESCE(extract(epoch FROM synced_at) * 1000000, 0)::bigint AS synced_us,
       COALESCE(extract(epoch FROM attempted_at) * 1000000, 0)::bigint AS attempted_us,
       failures, COALESCE(last_error, 'null'::jsonb)::jsonb AS last_error, records, rejected, unevaluable, matcher_version,
       COALESCE(extract(epoch FROM full_evaluation_requested_at) * 1000000, 0)::bigint AS eval_requested_us, adapter_version
  FROM vuln_source_state
 WHERE source = sqlc.arg(source)::text AND ecosystem = sqlc.arg(ecosystem)::text
   FOR UPDATE;

-- name: VulnStateList :many
-- Every row of a source, for the replica's copy of the state (R-VULN-027).
SELECT ecosystem, state, data_epoch, COALESCE(extract(epoch FROM synced_at) * 1000000, 0)::bigint AS synced_us
  FROM vuln_source_state
 WHERE source = sqlc.arg(source)::text;

-- name: VulnStateSave :exec
-- Writes every column a run changes; the caller read the row with VulnStateLock and passes back what stays. 0 for a time is NULL.
UPDATE vuln_source_state
   SET state = sqlc.arg(state)::text, data_epoch = sqlc.arg(data_epoch)::bigint, etag = NULLIF(sqlc.arg(etag)::text, ''),
       archive_sha256 = NULLIF(sqlc.arg(archive_sha256)::bytea, ''::bytea),
       watermark = CASE WHEN sqlc.arg(watermark_us)::bigint = 0 THEN NULL ELSE to_timestamp(sqlc.arg(watermark_us)::bigint / 1000000.0) END,
       full_synced_at = CASE WHEN sqlc.arg(full_synced_us)::bigint = 0 THEN NULL ELSE to_timestamp(sqlc.arg(full_synced_us)::bigint / 1000000.0) END,
       synced_at = CASE WHEN sqlc.arg(synced_us)::bigint = 0 THEN NULL ELSE to_timestamp(sqlc.arg(synced_us)::bigint / 1000000.0) END,
       attempted_at = now(), failures = 0, last_error = NULL,
       records = sqlc.arg(records)::int, rejected = sqlc.arg(rejected)::int, unevaluable = sqlc.arg(unevaluable)::int,
       matcher_version = sqlc.arg(matcher_version)::int, adapter_version = sqlc.arg(adapter_version)::int,
       full_evaluation_requested_at = CASE WHEN sqlc.arg(eval_requested_us)::bigint = 0 THEN NULL
                                           ELSE to_timestamp(sqlc.arg(eval_requested_us)::bigint / 1000000.0) END
 WHERE source = sqlc.arg(source)::text AND ecosystem = sqlc.arg(ecosystem)::text;

-- name: VulnStateFailure :exec
-- A failed run keeps the previous data, records the problem and counts one more consecutive failure (R-VULN-004).
UPDATE vuln_source_state
   SET failures = failures + 1, last_error = sqlc.arg(last_error)::jsonb, attempted_at = now()
 WHERE source = sqlc.arg(source)::text AND ecosystem = sqlc.arg(ecosystem)::text;

-- name: VulnStateCancelled :exec
-- A run whose job an operator cancelled records the problem and leaves the consecutive failures as they are (R-VULN-004,
-- Q-VULN-80): the cancel is neither a failure of the source nor a success.
UPDATE vuln_source_state
   SET last_error = sqlc.arg(last_error)::jsonb, attempted_at = now()
 WHERE source = sqlc.arg(source)::text AND ecosystem = sqlc.arg(ecosystem)::text;

-- name: VulnStateAttempt :one
-- Records that a run started and returns the attempted_at it wrote, as microseconds: the run's mark on the row, which the
-- write of its failure compares, so that a run that lost its lease never overwrites a newer run's state (Q-VULN-80).
UPDATE vuln_source_state SET attempted_at = now()
 WHERE source = sqlc.arg(source)::text AND ecosystem = sqlc.arg(ecosystem)::text
RETURNING COALESCE(extract(epoch FROM attempted_at) * 1000000, 0)::bigint AS attempted_us;

-- name: VulnStateBumpEpoch :one
-- Raises the data epoch of an ecosystem's advisory data, for a change of records that no OSV run made (the known-exploited flags).
UPDATE vuln_source_state SET data_epoch = data_epoch + 1
 WHERE source = 'osv' AND ecosystem = sqlc.arg(ecosystem)::text
RETURNING data_epoch;

-- name: VulnAdvisoryStored :many
-- What a batch compares a record with before it writes it: the stored modified time and digest (R-VULN-004 step 4).
SELECT id, (extract(epoch FROM modified) * 1000000)::bigint AS modified_us, record_sha256, canonical_id
  FROM vuln_advisory
 WHERE ecosystem = sqlc.arg(ecosystem)::text AND id = ANY(sqlc.arg(ids)::text[]);

-- name: VulnAdvisoryIDs :many
-- Every stored identifier of an ecosystem: the full sync's pass 1 finds what the archive no longer has.
SELECT id FROM vuln_advisory WHERE ecosystem = sqlc.arg(ecosystem)::text;

-- name: VulnAdvisoryCount :one
SELECT count(*)::int FROM vuln_advisory WHERE ecosystem = sqlc.arg(ecosystem)::text;

-- name: VulnAdvisoryUpsert :exec
-- Upserts the documents of a batch. A new record's decided severity is its own; a rewritten record keeps its canonical
-- identifier, its decided severity and its known-exploited flag until the group recomputation at the end of the run (R-VULN-006,
-- R-VULN-007 rule 3, R-VULN-008) sets them, and its own severity is written to the own_* columns (Q-VULN-62).
INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, published, withdrawn, kind, aliases, summary, details,
                           severity, severity_source, cvss_version, cvss_vector, cvss_score,
                           own_severity, own_severity_source, own_cvss_version, own_cvss_vector, own_cvss_score,
                           "references", cwe_ids, record_sha256)
SELECT sqlc.arg(ecosystem)::text, d.id, d.id, d.modified, d.published, d.withdrawn, d.kind,
       ARRAY(SELECT jsonb_array_elements_text(d.aliases)), d.summary, d.details, d.severity, d.severity_source,
       d.cvss_version, d.cvss_vector, d.cvss_score, d.severity, d.severity_source, d.cvss_version, d.cvss_vector, d.cvss_score,
       COALESCE(d.refs, '[]'::jsonb),
       ARRAY(SELECT jsonb_array_elements_text(d.cwe_ids)), decode(d.record_sha256, 'hex')
  FROM jsonb_to_recordset(sqlc.arg(docs)::jsonb) AS d(id text, modified timestamptz, published timestamptz, withdrawn timestamptz,
       kind text, aliases jsonb, summary text, details text, severity text, severity_source text, cvss_version text,
       cvss_vector text, cvss_score numeric, refs jsonb, cwe_ids jsonb, record_sha256 text)
ON CONFLICT (ecosystem, id) DO UPDATE
   SET modified = EXCLUDED.modified, published = EXCLUDED.published, withdrawn = EXCLUDED.withdrawn, kind = EXCLUDED.kind,
       aliases = EXCLUDED.aliases, summary = EXCLUDED.summary, details = EXCLUDED.details,
       own_severity = EXCLUDED.own_severity, own_severity_source = EXCLUDED.own_severity_source,
       own_cvss_version = EXCLUDED.own_cvss_version, own_cvss_vector = EXCLUDED.own_cvss_vector,
       own_cvss_score = EXCLUDED.own_cvss_score, "references" = EXCLUDED."references", cwe_ids = EXCLUDED.cwe_ids,
       record_sha256 = EXCLUDED.record_sha256;

-- name: VulnAffectedDelete :exec
DELETE FROM vuln_affected WHERE ecosystem = sqlc.arg(ecosystem)::text AND advisory_id = ANY(sqlc.arg(ids)::text[]);

-- name: VulnAffectedInsert :exec
-- The affected entries of a batch's records, one jsonb array of entries.
INSERT INTO vuln_affected (ecosystem, advisory_id, ord, name, ranges, versions, every_version, fixed)
SELECT sqlc.arg(ecosystem)::text, e.advisory_id, e.ord, e.name, COALESCE(e.ranges, '[]'::jsonb),
       ARRAY(SELECT jsonb_array_elements_text(e.versions)), e.every_version, ARRAY(SELECT jsonb_array_elements_text(e.fixed))
  FROM jsonb_to_recordset(sqlc.arg(entries)::jsonb) AS e(advisory_id text, ord smallint, name text, ranges jsonb, versions jsonb,
       every_version boolean, fixed jsonb);

-- name: VulnPendingAdd :exec
-- Queues advisories for the evaluation (R-VULN-011). One already queued takes a new enqueue time, the time of this statement
-- and not of its transaction: the evaluation deletes a row only while it still holds the time it read, so that an advisory
-- changed again while a batch is being evaluated stays queued.
INSERT INTO vuln_pending_evaluation (ecosystem, advisory_id, enqueued_at)
SELECT sqlc.arg(ecosystem)::text, unnest(sqlc.arg(ids)::text[]), clock_timestamp()
ON CONFLICT (ecosystem, advisory_id) DO UPDATE SET enqueued_at = clock_timestamp();

-- name: VulnAdvisoryDelete :execrows
-- Deletes advisories the full sync's source no longer has; their affected entries go with them.
DELETE FROM vuln_advisory WHERE ecosystem = sqlc.arg(ecosystem)::text AND id = ANY(sqlc.arg(ids)::text[]);

-- name: VulnAdvisoryGroupRows :many
-- The stored records that are or alias any of the names: one round of the closure that finds an alias group (R-VULN-006).
-- severity* is the severity decided for the record, own_* the record's own; a row stored before 00038 has no own_* and its
-- decided columns stand in.
-- One lookup of the primary key and of the aliases index per name (Q-VULN-90, Q-VULN-94). `id = ANY(names) OR aliases && names` left the
-- choice to estimates the data misleads: a table whose aliases are mostly empty makes `&&` look like it matches a share of the
-- table, and the plan was a sequential scan of the whole table for every round. The first form of this statement joined the names
-- to the table with the OR in the join condition and left the order of the join to the planner: on a table that has not been
-- analyzed yet, the first full import of an installation, `ecosystem = ...` is estimated at 0.5 % of the rows, the planner makes the
-- table the outer side and tests every row against every name (99,998,000 tests for 2,000 names and 50,000 rows, 13 s, the statement
-- timeout in the failure suite). The LATERAL makes the names the outer side whatever the estimates are, and each name is looked up
-- in the primary key and, in the second branch, in the aliases index only: `OFFSET 0` keeps the planner from adding the
-- ecosystem's index (which returns the whole ecosystem for every name before the aliases index narrows it) to that branch, the
-- ecosystem is tested on the rows it returns. `@> ARRAY[name]` is the one-key form of `&&`. A record two names reach is returned once.
SELECT DISTINCT ON (v.id) v.id, v.canonical_id, v.aliases, v.kind, (v.withdrawn IS NOT NULL)::boolean AS is_withdrawn, v.severity, v.severity_source,
       COALESCE(v.cvss_version, '')::text AS cvss_version, COALESCE(v.cvss_vector, '')::text AS cvss_vector,
       COALESCE(v.cvss_score, 0)::float8 AS cvss_score,
       (CASE WHEN v.own_severity IS NULL THEN v.severity ELSE v.own_severity END)::text AS own_severity,
       (CASE WHEN v.own_severity IS NULL THEN v.severity_source ELSE v.own_severity_source END)::text AS own_severity_source,
       (CASE WHEN v.own_severity IS NULL THEN COALESCE(v.cvss_version, '') ELSE COALESCE(v.own_cvss_version, '') END)::text AS own_cvss_version,
       (CASE WHEN v.own_severity IS NULL THEN COALESCE(v.cvss_vector, '') ELSE COALESCE(v.own_cvss_vector, '') END)::text AS own_cvss_vector,
       (CASE WHEN v.own_severity IS NULL THEN COALESCE(v.cvss_score, 0) ELSE COALESCE(v.own_cvss_score, 0) END)::float8 AS own_cvss_score
  FROM unnest(sqlc.arg(names)::text[]) AS n(name)
 CROSS JOIN LATERAL (
       SELECT x.* FROM vuln_advisory x WHERE x.ecosystem = sqlc.arg(ecosystem)::text AND x.id = n.name
       UNION ALL
       (SELECT x.* FROM vuln_advisory x WHERE x.aliases @> ARRAY[n.name] OFFSET 0)) AS v
 WHERE v.ecosystem = sqlc.arg(ecosystem)::text
 ORDER BY v.id;

-- name: VulnAdvisorySetCanonical :exec
UPDATE vuln_advisory SET canonical_id = sqlc.arg(canonical)::text
 WHERE ecosystem = sqlc.arg(ecosystem)::text AND id = ANY(sqlc.arg(ids)::text[]) AND canonical_id <> sqlc.arg(canonical)::text;

-- name: VulnAdvisorySetSeverity :exec
-- The severity decided for a record by its alias group (R-VULN-007 rule 3); its own severity stays in the own_* columns.
UPDATE vuln_advisory
   SET severity = sqlc.arg(severity)::text, severity_source = sqlc.arg(severity_source)::text,
       cvss_version = NULLIF(sqlc.arg(cvss_version)::text, ''), cvss_vector = NULLIF(sqlc.arg(cvss_vector)::text, ''),
       cvss_score = CASE WHEN sqlc.arg(cvss_vector)::text = '' THEN NULL ELSE sqlc.arg(cvss_score)::float8::numeric(3,1) END
 WHERE ecosystem = sqlc.arg(ecosystem)::text AND id = sqlc.arg(id)::text;

-- name: VulnKEVDelete :exec
DELETE FROM vuln_kev;

-- name: VulnKEVInsert :exec
INSERT INTO vuln_kev (cve_id, date_added, due_date, ransomware)
SELECT k.cve_id, k.date_added::date, NULLIF(k.due_date, '')::date, NULLIF(k.ransomware, '')
  FROM jsonb_to_recordset(sqlc.arg(entries)::jsonb) AS k(cve_id text, date_added text, due_date text, ransomware text);

-- name: VulnKEVIDs :many
SELECT cve_id FROM vuln_kev;

-- name: VulnKEVCandidates :many
-- The canonical records of an ecosystem that the catalog may change the flag of: the flagged ones, and those that are or alias a
-- listed identifier (R-VULN-008).
SELECT DISTINCT canonical_id FROM vuln_advisory
 WHERE ecosystem = sqlc.arg(ecosystem)::text
   AND (known_exploited OR id = ANY(sqlc.arg(cves)::text[]) OR aliases && sqlc.arg(cves)::text[]);

-- name: VulnKEVApply :many
-- Sets the known-exploited flag and the date of the canonical records among ids from the catalog: a record is known exploited
-- when its identifier, or any alias or member of its group, is listed, and the date is the earliest of them. It returns the
-- records whose flag or date changed.
WITH names AS (
    SELECT c.id AS cid,
           ARRAY(SELECT m.id FROM vuln_advisory m WHERE m.ecosystem = c.ecosystem AND m.canonical_id = c.id
                 UNION
                 SELECT unnest(m.aliases) FROM vuln_advisory m WHERE m.ecosystem = c.ecosystem AND m.canonical_id = c.id) AS names
      FROM vuln_advisory c
     WHERE c.ecosystem = sqlc.arg(ecosystem)::text AND c.id = ANY(sqlc.arg(ids)::text[])
), hit AS (
    SELECT n.cid, (SELECT min(k.date_added) FROM vuln_kev k WHERE k.cve_id = ANY(n.names)) AS added FROM names n
)
UPDATE vuln_advisory a
   SET known_exploited = (h.added IS NOT NULL), kev_date_added = h.added
  FROM hit h
 WHERE a.ecosystem = sqlc.arg(ecosystem)::text AND a.id = h.cid
   AND (a.known_exploited IS DISTINCT FROM (h.added IS NOT NULL) OR a.kev_date_added IS DISTINCT FROM h.added)
RETURNING a.id;

-- name: VulnTouchedAdd :exec
-- The names a batch wrote, in the batch's transaction (Q-VULN-62): the alias groups of the run are recomputed from this set.
INSERT INTO vuln_sync_touched (ecosystem, name)
SELECT sqlc.arg(ecosystem)::text, unnest(sqlc.arg(names)::text[])
ON CONFLICT (ecosystem, name) DO NOTHING;

-- name: VulnTouchedAddRecords :exec
-- The identifiers and aliases of stored records that are about to be deleted, so that the groups they leave are recomputed.
INSERT INTO vuln_sync_touched (ecosystem, name)
SELECT a.ecosystem, n.name
  FROM vuln_advisory a CROSS JOIN LATERAL (SELECT a.id AS name UNION SELECT unnest(a.aliases)) n
 WHERE a.ecosystem = sqlc.arg(ecosystem)::text AND a.id = ANY(sqlc.arg(ids)::text[])
ON CONFLICT (ecosystem, name) DO NOTHING;

-- name: VulnTouchedPage :many
-- One page of the touched names, in name order, after the last one of the previous page.
SELECT name FROM vuln_sync_touched
 WHERE ecosystem = sqlc.arg(ecosystem)::text AND name > sqlc.arg(after)::text
 ORDER BY name
 LIMIT sqlc.arg(max_rows)::int;

-- name: VulnTouchedExists :one
SELECT EXISTS (SELECT 1 FROM vuln_sync_touched WHERE ecosystem = sqlc.arg(ecosystem)::text)::boolean;

-- name: VulnTouchedClear :exec
-- Ends the set in the transaction that ends the run.
DELETE FROM vuln_sync_touched WHERE ecosystem = sqlc.arg(ecosystem)::text;

-- name: VulnAdvisoryOrphans :many
-- Records whose canonical record is gone, after a deletion: their group needs a new canonical identifier.
SELECT a.id FROM vuln_advisory a
 WHERE a.ecosystem = sqlc.arg(ecosystem)::text
   AND NOT EXISTS (SELECT 1 FROM vuln_advisory c WHERE c.ecosystem = a.ecosystem AND c.id = a.canonical_id);
