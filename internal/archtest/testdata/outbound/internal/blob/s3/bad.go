package s3

// Bad takes a session-level lock.
const Bad = "SELECT pg_try_advisory_lock(7)"
