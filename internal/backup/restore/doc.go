// Package restore is `trove backup restore` (R-BAK-020 to R-BAK-024): the checks of R-BAK-020, the anchor, the migration
// runner's restore mode, the blob copy, the load transaction, the fix-ups of R-BAK-022, the remote-cache purge, the
// revocation journal replay, the verification and the finish (R-BAK-021). It is resumable: every phase either commits
// atomically with a marker (system_state restore_in_progress) or is idempotent, so the Job's next pod continues where the
// killed one stopped.
//
// The package is written against ports: [Session] is the database (internal/meta/postgres.RestoreSession implements it),
// [BlobStore] the new installation's object store (internal/blob/s3.BlobSink), target.Store the backup target and [Journal]
// the revocation journal. Tests use fakes of each.
package restore
