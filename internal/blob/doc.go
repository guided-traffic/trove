// Package blob is the content-addressed blob store (R-STOR-001 to R-STOR-006,
// R-STOR-013, R-STOR-024, R-STOR-025, R-STOR-033, R-STOR-035).
//
// Content lives in an S3-compatible bucket under a key derived from its SHA-256
// alone; the authority on whether a blob exists is its row in PostgreSQL. The
// package holds the parts that do not depend on a particular object-store
// client:
//
//   - Spool and Store.Ingest: admission, the replica-local spool file and the
//     single pass that computes SHA-256, SHA-1, MD5 and SHA-512 (R-STOR-004).
//   - Store.Commit: the commit protocol with deduplication (R-STOR-005) and the
//     transfer to the object store, one request or multipart from the spool
//     file (R-STOR-006).
//   - Store.Open: download with verification (R-STOR-013, Q-STOR-6).
//   - Collector: race-free garbage collection (R-STOR-024, R-STOR-025).
//   - Guard: the circuit breaker and the request metrics around any ObjectStore
//     (R-STOR-033).
//   - Conformance and StartupCheck: the object-store checks of R-STOR-035.
//
// The ObjectStore interface is implemented by internal/blob/s3, the only
// package that imports the AWS SDK; internal/blob/blobtest has an in-memory
// implementation with fault injection for tests.
//
// Nothing here uses Valkey or a session-level advisory lock (R-STOR-046):
// correctness rests on row locks, and exclusion between maintenance runs is the
// job key's business.
package blob
