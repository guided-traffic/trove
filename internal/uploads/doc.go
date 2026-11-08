// Package uploads is the multi-request upload session service (R-STOR-011): one blob that a client sends over several requests,
// each of which may reach another replica and any of which a drain may end (INV-12, INV-18).
//
// A session is a row in PostgreSQL (upload_sessions, upload_session_parts) and one S3 multipart upload of the staging object
// <prefix>/_uploads/<session-id>/data. Nothing of a session stays in a replica's memory, spool or Valkey after a request ends:
//
//   - claims: a request takes the writer claim of the session in one short transaction, only if no other claim is unexpired and
//     its first byte is the received count; the lease is judged by the database's clock (writer_lease);
//   - parts: the body passes through the restored hash states into the spool and is cut into parts of exactly the session's part
//     size, each sent with UploadPart and recorded with the new received count and hash state;
//   - tails: the bytes after the last part, fewer than one part, are kept as the object tail-<writer>, one object per request that
//     wrote it, so that a writer that lost its claim can never overwrite the tail the session records; the next request reads it
//     back and checks it against the recorded size and SHA-256;
//   - finalize: the state advances open, completing, assembled, done, each step recorded before the next starts and each
//     idempotent, so that a finalize cut anywhere is repeated by the client's retry on any replica and continues where it stopped;
//     the last step is the commit protocol of R-STOR-005 with a server-side copy of the staging object as its object write
//     (blob.Staged), in the transaction that also moves the session to done;
//   - expiry: the collection run (internal/blob, gc_uploads.go) aborts sessions without a request for session_ttl or older than
//     session_max_age and removes what they staged; every request checks the age itself.
//
// The service decides nothing about who may do what: the host (internal/formathost) resolves the principal, the repository and the
// binding of the request into a [Scope] and decides `deploy` on the session's target before every call. A session that the scope
// does not match is the same error as a session that does not exist.
package uploads
