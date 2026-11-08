-- trove:phase contract
-- trove:requires-app >= 0.1.0
--
-- Indexes and one column for the statements that grow with the number of sessions (R-STOR-011, Q-STOR-176). The collection run lists the
-- aborted and the finished sessions oldest first: it needs them ordered by last_activity_at inside a state, which the index on state alone
-- of 00054 could not give, so the sort covered every aborted row for each batch. A start counts the live sessions of one principal on one
-- repository to hold the cap on open sessions; a session without a principal counts as the all-zero uuid, so that the count is an equality
-- an index can serve (IS NOT DISTINCT FROM cannot use one). A session whose objects the request that ended it removed is marked cleaned, so
-- that the collection run deletes its row without a call to the store.

DROP INDEX upload_sessions_cleanup;

CREATE INDEX upload_sessions_cleanup ON upload_sessions (state, last_activity_at) WHERE state IN ('done', 'aborted');

CREATE INDEX upload_sessions_open_per_principal ON upload_sessions
    (repo_id, (COALESCE(principal_id, '00000000-0000-0000-0000-000000000000'::uuid)))
    WHERE state IN ('open', 'completing', 'assembled');

ALTER TABLE upload_sessions ADD COLUMN cleaned boolean NOT NULL DEFAULT false;
