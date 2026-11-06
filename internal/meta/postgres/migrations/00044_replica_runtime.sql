-- trove:phase expand
--
-- The replica registry (R-OPS-003, Q-OPS-175): every heartbeat also writes the replica's own measurements of the period
-- since its previous heartbeat (the requests its client listener finished, their 5xx and latency percentiles, the requests
-- in flight, CPU, memory and goroutines), which GET /api/v1/system/replicas returns as `runtime`. Replicas never ask each
-- other: the row is the only channel. A binary of a release before the column neither writes nor reads it; its rows keep
-- the default '{}', which the API shows as `null`. Adding a column with a constant default changes the catalog only and
-- rewrites no row.
ALTER TABLE replica ADD COLUMN runtime jsonb NOT NULL DEFAULT '{}';
