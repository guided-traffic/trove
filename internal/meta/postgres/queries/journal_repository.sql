-- The statements of the replay of a repository narrowing and of a claims change (R-HA-054, R-REPO-061):
-- internal/repository/journal_replay.go. The lock and the configuration write are JournalRepositoryLock and
-- JournalRepositorySetConfig (journal.sql); the member and pin rows are written with the statements of virtual.sql.

-- name: JournalRepositoryFormat :one
-- The format of a repository the replay holds locked (JournalRepositoryLock does not return it).
SELECT format FROM repositories WHERE id = sqlc.arg(id)::bigint;

-- name: JournalMembersClaimHolders :many
-- Which of the local repositories hold a claim of scope members: the claims that leave effect in a virtual repository
-- when the holder is taken out of it while another member stays (Q-REPO-248).
SELECT DISTINCT r.id
  FROM repositories r
  JOIN namespace_claims c ON c.repo_id = r.id
 WHERE r.id = ANY(sqlc.arg(ids)::bigint[]) AND r.class = 'local' AND c.scope = 'members';

-- name: JournalFormatClaims :many
-- Every claim of the repositories of a format, for the overlap checks of the replay (Q-REPO-249, Q-REPO-250).
SELECT c.repo_id, c.pattern, c.scope
  FROM namespace_claims c
  JOIN repositories r ON r.id = c.repo_id
 WHERE r.format = sqlc.arg(format)::text
 ORDER BY c.repo_id, c.pattern COLLATE "C";

-- name: JournalWidenClaim :execrows
-- A claim of scope members becomes global; a claim that is global already, or is missing, is left as it is.
UPDATE namespace_claims
   SET scope = 'global'
 WHERE repo_id = sqlc.arg(repo_id)::bigint AND pattern = sqlc.arg(pattern)::text AND scope = 'members';
