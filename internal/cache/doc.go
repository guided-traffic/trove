// Package cache is the framework for the in-process caches of a replica
// (R-ARCH-018). A cache is bounded, carries a mandatory SafetyTTL, and is
// registered under a name and the change areas that invalidate it. The cluster
// package delivers changes of those areas (from the change log and from Valkey)
// to the registry; the cache package itself knows neither PostgreSQL nor Valkey
// and sits in the leaf layer.
//
// Three rules come from R-ARCH-018:
//
//   - SafetyTTL bounds staleness if an invalidation is ever missed. It is not
//     optional: Register panics without one.
//   - Change messages only evict early. The applied epoch of an area is the
//     truth, and only the change-log poll advances it.
//   - A fill never re-inserts an invalidated value. [Cache.Load] reads the
//     applied epoch of every area of the cache before it calls the loader and
//     inserts the result only if those epochs and the cache's invalidation
//     counter are unchanged afterwards; otherwise the value serves the current
//     request and is not cached.
//
// A cache holds nothing that another replica needs and nothing that decides
// access beyond the replica's own request (R-ARCH-030, Q-ARCH-16).
package cache
