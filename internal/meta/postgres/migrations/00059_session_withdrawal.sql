-- trove:phase expand
--
-- A session's withdrawal and its successor, for the delete job that a session queued (R-STOR-018, Q-AUTHZ-261, Q-AUTHZ-262). The job
-- stops when the session that asked was withdrawn, and a session that already ended by time, by the session limit or by a
-- re-authentication could not be withdrawn any more: every end statement matches live rows only. withdrawn_at is set by every
-- withdrawal (a sign-out, the end of a user's other sessions, an administrator's end, a disable, a changed break-glass password) on the
-- sessions it reaches, ended ones included; successor_id names the session a re-authentication replaced this one by, so that the
-- withdrawal of the successor reaches the job of its predecessor. No foreign key: a successor that is gone is a session the job can no
-- longer check, and the reader treats it so.
ALTER TABLE ui_session ADD COLUMN withdrawn_at timestamptz;
ALTER TABLE ui_session ADD COLUMN successor_id uuid;
CREATE INDEX ui_session_successor ON ui_session (successor_id) WHERE successor_id IS NOT NULL;
