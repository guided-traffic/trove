package store

// Fine uses the transaction-level lock.
const Fine = "SELECT pg_advisory_xact_lock(1)"
