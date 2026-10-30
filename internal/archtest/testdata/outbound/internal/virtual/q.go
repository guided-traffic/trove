package virtual

// Query takes a lock that outlives its transaction.
const Query = "SELECT pg_try_advisory_lock(2)"
