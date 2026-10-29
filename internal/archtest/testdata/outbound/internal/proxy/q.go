package proxy

// Query takes a lock that outlives its transaction.
const Query = "SELECT pg_advisory_lock(1)"
