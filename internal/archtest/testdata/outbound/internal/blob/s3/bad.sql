-- session lock in a migration
SELECT pg_advisory_lock(2);
