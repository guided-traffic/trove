// Package invalidation is the cache invalidation of remote repositories
// (R-REPO-045, R-OPS-021): the rows of remote_cache_invalidations, the
// per-replica matcher that the proxy engine asks on every request, the writer of
// the invalidate call and the fold task that turns the rows into expiries.
//
// An invalidation is one row (repository, prefix, scope, time). It makes the
// covered metadata entries and negative entries count as expired without touching
// them, so invalidating a million entries is one insert. Each replica keeps the
// rows in memory ([Set]); the inserting transaction records the change in the
// change area cache_invalidation, so the row is in force on every replica within
// cluster.epoch_poll_interval, and within milliseconds while Valkey is available.
// A request never reads PostgreSQL for it: the engine calls [Set.MetadataValid]
// and [Set.NegativeValid].
//
// The package does not know the proxy engine, the negative store or the entries'
// format. It answers a question about a path and a time.
package invalidation
