-- trove:phase contract
-- trove:requires-app >= 0.1.0
--
-- The tail of an upload session is a list of segments (R-STOR-011, Q-STOR-174). A request whose body is shorter than what is missing for
-- the next part writes it as one more segment object, with its size and SHA-256 recorded in a row of its own, and reads nothing back; the
-- segments are read once, by the request whose bytes complete the part. The single tail of 00054 (tail_writer, tail_sha256) cannot say that,
-- and its consistency check goes with the columns.
--
-- A multipart upload is created when the first full part is sent (lazily), so the id of a session that has none is the empty string, and the
-- item that a finished session committed is recorded with it, so that a repeated closing request is answered from the record.

ALTER TABLE upload_sessions
    DROP COLUMN tail_writer,
    DROP COLUMN tail_sha256,
    ADD COLUMN item_id bigint;

CREATE TABLE upload_session_tails (
    session_id uuid   NOT NULL REFERENCES upload_sessions (id) ON DELETE CASCADE,
    seq        integer NOT NULL CHECK (seq >= 1),
    writer     uuid   NOT NULL,
    size       bigint NOT NULL CHECK (size > 0),
    sha256     bytea  NOT NULL CHECK (octet_length(sha256) = 32),
    PRIMARY KEY (session_id, seq)
);
