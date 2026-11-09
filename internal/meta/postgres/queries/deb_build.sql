-- Statements of the Debian index builder (R-DEB-010, R-DEB-012, R-DEB-014, apt-work-packages.md WP4). The local class's rows are
-- queries/deb_local.sql, the signing keys queries/deb_keys.sql.

-- name: DebSuiteStates :many
-- Every suite of a repository with the fingerprints of the keys that signed its current generation.
SELECT s.suite, s.change_seq, s.built_seq, s.generation, COALESCE((extract(epoch FROM s.release_date) * 1000000)::bigint, 0)::bigint AS release_us,
       COALESCE((extract(epoch FROM s.published_at) * 1000000)::bigint, 0)::bigint AS published_us, s.state, COALESCE(s.reason, '')::text AS reason,
       COALESCE(g.signed_by, '{}'::text[])::text[] AS signed_by
  FROM deb_suites s
  LEFT JOIN deb_generations g ON g.repo_id = s.repo_id AND g.suite = s.suite AND g.generation = s.generation
 WHERE s.repo_id = sqlc.arg(repo_id)::bigint
 ORDER BY s.suite;

-- name: DebSuiteState :many
SELECT s.suite, s.change_seq, s.built_seq, s.generation, COALESCE((extract(epoch FROM s.release_date) * 1000000)::bigint, 0)::bigint AS release_us,
       COALESCE((extract(epoch FROM s.published_at) * 1000000)::bigint, 0)::bigint AS published_us, s.state, COALESCE(s.reason, '')::text AS reason,
       COALESCE(g.signed_by, '{}'::text[])::text[] AS signed_by
  FROM deb_suites s
  LEFT JOIN deb_generations g ON g.repo_id = s.repo_id AND g.suite = s.suite AND g.generation = s.generation
 WHERE s.repo_id = sqlc.arg(repo_id)::bigint AND s.suite = sqlc.arg(suite)::text;

-- name: DebDueSuites :many
-- The suites a build reads: the ones with changes not yet built, or every suite of a full build.
SELECT suite, change_seq, built_seq, generation, COALESCE((extract(epoch FROM release_date) * 1000000)::bigint, 0)::bigint AS release_us,
       COALESCE((extract(epoch FROM dirty_since) * 1000000)::bigint, 0)::bigint AS dirty_us
  FROM deb_suites
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND (sqlc.arg(every)::boolean OR change_seq > built_seq)
 ORDER BY suite;

-- name: DebBuildEntries :many
-- The files listed in a suite, one row per coordinate: only live items that no tombstone withholds (R-DEB-032), with the digests of
-- their blobs. A build reads no package file (R-DEB-010).
SELECT c.suite, c.component, b.architecture, b.package, b.version, b.control, i.path AS filename, i.size::bigint AS size,
       encode(bl.md5, 'hex')::text AS md5, encode(bl.sha256, 'hex')::text AS sha256
  FROM deb_coordinates c
  JOIN deb_binaries b ON b.item_id = c.item_id
  JOIN items i ON i.id = b.item_id AND i.trashed_at IS NULL
  JOIN blobs bl ON bl.sha256 = i.blob_sha256
 WHERE c.repo_id = sqlc.arg(repo_id)::bigint AND c.suite = ANY(sqlc.arg(suites)::text[])
   AND NOT EXISTS (SELECT 1 FROM deb_tombstones t WHERE t.repo_id = b.repo_id AND t.path = i.path AND t.sha256 = i.blob_sha256)
 ORDER BY c.suite, c.component, b.package, b.version, b.architecture;

-- name: DebGenerationsOf :many
SELECT suite, generation, COALESCE((extract(epoch FROM superseded_at) * 1000000)::bigint, 0)::bigint AS superseded_us, files
  FROM deb_generations
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = ANY(sqlc.arg(suites)::text[])
 ORDER BY suite, generation;

-- name: DebDatabaseNow :one
SELECT (extract(epoch FROM now()) * 1000000)::bigint;

-- name: DebApplyGeneration :execrows
-- Sets the suite's published generation while its built_seq is still below the change_seq the build read and the generation is the one the
-- build started from: a slower build of an older snapshot changes nothing (R-DEB-010). A full build of rows that did not change (a reindex)
-- reads the number the suite has built already and publishes again.
UPDATE deb_suites
   SET built_seq = sqlc.arg(read_seq)::bigint, generation = sqlc.arg(generation)::bigint, release_date = to_timestamp(sqlc.arg(release_unix)::bigint),
       published_at = now(), state = 'current', reason = NULL,
       dirty_since = CASE WHEN change_seq > sqlc.arg(read_seq)::bigint THEN now() ELSE NULL END
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = sqlc.arg(suite)::text AND generation = sqlc.arg(prev_generation)::bigint
   AND (built_seq < sqlc.arg(read_seq)::bigint OR (sqlc.arg(every_suite)::boolean AND built_seq = sqlc.arg(read_seq)::bigint));

-- name: DebInsertGeneration :exec
INSERT INTO deb_generations (repo_id, suite, generation, built_seq, files, signed_by)
VALUES (sqlc.arg(repo_id)::bigint, sqlc.arg(suite)::text, sqlc.arg(generation)::bigint, sqlc.arg(built_seq)::bigint,
        sqlc.arg(files)::jsonb, sqlc.arg(signed_by)::text[]);

-- name: DebSupersedeGenerations :exec
UPDATE deb_generations SET superseded_at = now()
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = sqlc.arg(suite)::text AND superseded_at IS NULL AND generation <> sqlc.arg(generation)::bigint;

-- name: DebDeleteGenerations :exec
DELETE FROM deb_generations
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = sqlc.arg(suite)::text AND generation = ANY(sqlc.arg(generations)::bigint[]);

-- name: DebFailSuite :exec
-- A failed build keeps the suite's generation and says why (R-DEB-010).
UPDATE deb_suites SET state = 'failed', reason = left(sqlc.arg(reason)::text, 256)
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = sqlc.arg(suite)::text AND built_seq < sqlc.arg(read_seq)::bigint;

-- name: DebDropSuite :exec
-- A full build drops a suite that has no files (R-DEB-014); its generations go with it.
DELETE FROM deb_suites WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = sqlc.arg(suite)::text AND change_seq <= sqlc.arg(read_seq)::bigint;

-- name: DebRepositoryOf :many
-- The repository a build is for: key, class, format and the stored document.
SELECT r.id, p.key AS project, r.key, r.class, r.format, r.config
  FROM repositories r JOIN project p ON p.id = r.project_id
 WHERE r.id = sqlc.arg(id)::bigint;

-- name: DebIndexedRepositories :many
-- The local and virtual Debian repositories, for the restore's full builds (R-DEB-032).
SELECT id FROM repositories WHERE format = 'deb' AND class IN ('local', 'virtual') ORDER BY id;

-- name: DebRetentionDue :many
-- The local repositories with a superseded generation older than the keep time beyond the three newest: the task enqueues a build for them
-- (Q-DEB-33); the build itself decides what to remove.
SELECT DISTINCT g.repo_id
  FROM deb_generations g
  JOIN repositories r ON r.id = g.repo_id AND r.class = 'local'
 WHERE g.superseded_at IS NOT NULL AND g.superseded_at < now() - make_interval(secs => sqlc.arg(keep_seconds)::double precision)
   AND (SELECT count(*) FROM deb_generations n
         WHERE n.repo_id = g.repo_id AND n.suite = g.suite AND n.generation > g.generation AND n.superseded_at IS NOT NULL) >= 3;

-- name: DebChangedRepositories :many
-- The local repositories with a suite that has changes no build has published yet: the task's safety net (R-DEB-010).
SELECT DISTINCT s.repo_id FROM deb_suites s JOIN repositories r ON r.id = s.repo_id AND r.class = 'local'
 WHERE s.change_seq > s.built_seq AND s.state <> 'failed';
