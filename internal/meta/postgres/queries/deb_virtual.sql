-- Statements of the build of a virtual Debian repository's indexes (R-DEB-025, R-DEB-026, apt-work-packages.md WP8). The suite rows and the
-- generations are written by deb_build.sql; the remote members' rows are deb_remote.sql.

-- name: DebVirtualSuiteRows :many
-- The suites a virtual repository has, with the inputs its last build compared and the time of the check.
SELECT suite, change_seq, built_seq, generation,
       COALESCE((extract(epoch FROM release_date) * 1000000)::bigint, 0)::bigint AS release_us,
       COALESCE((extract(epoch FROM checked_at) * 1000000)::bigint, 0)::bigint AS checked_us,
       COALESCE(inputs, '{}'::jsonb)::jsonb AS inputs
  FROM deb_suites
 WHERE repo_id = sqlc.arg(repo_id)::bigint
 ORDER BY suite;

-- name: DebLocalMemberSuites :many
-- The published generation of each suite of the local members: its number, its Release Date and the index files it names.
SELECT s.repo_id, s.suite, s.generation,
       COALESCE((extract(epoch FROM s.release_date) * 1000000)::bigint, 0)::bigint AS release_us,
       COALESCE(g.files, '{}'::jsonb)::jsonb AS files
  FROM deb_suites s
  LEFT JOIN deb_generations g ON g.repo_id = s.repo_id AND g.suite = s.suite AND g.generation = s.generation
 WHERE s.repo_id = ANY(sqlc.arg(repo_ids)::bigint[]) AND s.generation > 0
 ORDER BY s.repo_id, s.suite;

-- name: DebRecordCheck :exec
-- A build that found its inputs unchanged records them and the time, and nothing else (R-DEB-026).
UPDATE deb_suites SET inputs = sqlc.arg(inputs)::jsonb, checked_at = to_timestamp(sqlc.arg(checked_unix)::bigint)
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND suite = sqlc.arg(suite)::text;

-- name: DebVirtualsOfMember :many
-- The virtual Debian repositories that list a repository as a member.
SELECT vm.virtual_id FROM virtual_members vm JOIN repositories v ON v.id = vm.virtual_id
 WHERE vm.member_id = sqlc.arg(member_id)::bigint AND v.format = 'deb' ORDER BY vm.virtual_id;

-- name: DebVirtualLastBuild :one
-- The time of the last build or check of a virtual repository, in microseconds since the epoch; 0 when it has none.
SELECT COALESCE(max(extract(epoch FROM GREATEST(published_at, checked_at)) * 1000000)::bigint, 0)::bigint
  FROM deb_suites WHERE repo_id = sqlc.arg(repo_id)::bigint;

-- name: DebVirtualsDue :many
-- The virtual Debian repositories whose last check is older than their merged-metadata period, or that have no suite row yet (Q-DEB-21).
SELECT r.id
  FROM repositories r
 WHERE r.format = 'deb' AND r.class = 'virtual'
   AND COALESCE((SELECT max(GREATEST(s.published_at, s.checked_at)) FROM deb_suites s WHERE s.repo_id = r.id), 'epoch'::timestamptz)
       < now() - make_interval(secs => COALESCE((r.config -> 'virtual' ->> 'mergedMetadataTtlSeconds')::int, 300)::double precision)
 ORDER BY r.id;

-- name: DebRepositoryMeta :many
-- The class and key of a repository and the upstream of a remote, for the member list of a build.
SELECT r.id, p.key AS project, r.key, r.class, r.format, COALESCE(r.config -> 'remote' ->> 'url', '')::text AS url
  FROM repositories r JOIN project p ON p.id = r.project_id
 WHERE r.id = ANY(sqlc.arg(ids)::bigint[]);

-- name: DebClaimsFingerprint :one
-- A digest of the claim revisions of the Debian repositories: a change of a claim changes what a virtual repository's members may serve (R-DEB-025).
SELECT COALESCE(md5(string_agg(id::text || ':' || claims_version::text, ',' ORDER BY id)), '')::text
  FROM repositories WHERE format = 'deb';
