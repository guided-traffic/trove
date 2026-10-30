package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
)

// CacheNegativeL1 is the name of the registered cache that holds a replica's copy of
// the negative entries it wrote or read (R-REPO-028, Q-ARCH-197).
const CacheNegativeL1 = "proxy_negative_l1"

// negativeLifetime bounds the age of an L1 entry: the largest notFoundTtlSeconds
// (24 h) plus the clock allowance, the longest a negative entry can count.
const negativeLifetime = 24*time.Hour + invalidation.NegativeAllowance

// futureAllowance is how far into the future a negative entry's creation time may
// lie (the clock allowance of R-ARCH-030 between two replicas).
const futureAllowance = invalidation.NegativeAllowance

type negKey struct {
	repo int64
	f    string
}

// negEntry is a remembered upstream 404 or 410: the status and when the upstream
// request was sent, on the clock of the replica that sent it ("c" of R-REPO-028).
type negEntry struct {
	status int
	sentAt time.Time
}

// negatives is a replica's side of the negative cache: the L1 in memory, and the
// Valkey store behind it (R-REPO-028). A negative entry can make trove answer 404
// and nothing else.
type negatives struct {
	l1      *cache.Cache[negKey, negEntry]
	store   cluster.NegativeStore
	inv     invalidation.Matcher
	counter LookupCounter
	now     func() time.Time
}

// LookupCounter counts a lookup of a Valkey-backed cache that the reader refused
// (trove_valkey_cache_lookups_total); *cluster.Valkey implements it.
type LookupCounter interface {
	CountCacheLookup(cache, result string)
}

// valid applies the rules every reader applies (R-REPO-028): the creation time lies
// between now minus the repository's current notFoundTtlSeconds and now plus 5 s,
// and no invalidation of scope negative or all covering the path was created later
// than the creation time minus 5 s.
func (n *negatives) valid(repoID int64, path string, ttl time.Duration, v negEntry) bool {
	now := n.now()
	if v.sentAt.Before(now.Add(-ttl)) || v.sentAt.After(now.Add(futureAllowance)) {
		return false
	}
	return n.inv == nil || n.inv.NegativeValid(repoID, path, v.sentAt)
}

// lookup asks the L1 first, then Valkey, and copies a Valkey hit into the L1. While
// Valkey is unavailable only the L1 answers (R-REPO-025 step 8).
func (n *negatives) lookup(ctx context.Context, rem Remote, path, variant string) (negEntry, bool) {
	ttl := time.Duration(rem.Settings.Cache.NotFoundTTLSeconds) * time.Second
	if ttl <= 0 {
		return negEntry{}, false
	}
	f := cluster.NewFlightKey(rem.id, path, variant)
	key := negKey{repo: rem.id, f: f.String()}
	if v, ok := n.l1.Get(key); ok {
		if n.valid(rem.id, path, ttl, v) {
			return v, true
		}
		n.l1.Remove(key)
	}
	if n.store == nil {
		return negEntry{}, false
	}
	e, found, err := n.store.Lookup(ctx, rem.id, f)
	if err != nil || !found {
		return negEntry{}, false
	}
	v := negEntry{status: e.Status, sentAt: e.SentAt}
	if !n.valid(rem.id, path, ttl, v) {
		if n.counter != nil {
			n.counter.CountCacheLookup("negative", "rejected")
		}
		return negEntry{}, false
	}
	n.l1.Set(key, v)
	return v, true
}

// record remembers an upstream 404 or 410 in the L1 and in Valkey, outside every
// transaction, before the flight is released (R-REPO-028). A failure of Valkey
// leaves the L1 entry: each replica then asks at most once per path and period.
func (n *negatives) record(ctx context.Context, rem Remote, path, variant string, status int, sentAt time.Time) error {
	ttl := time.Duration(rem.Settings.Cache.NotFoundTTLSeconds) * time.Second
	if ttl <= 0 {
		return nil
	}
	f := cluster.NewFlightKey(rem.id, path, variant)
	v := negEntry{status: status, sentAt: sentAt}
	n.l1.Set(negKey{repo: rem.id, f: f.String()}, v)
	if n.store == nil {
		return nil
	}
	err := n.store.Record(ctx, rem.id, f, cluster.NegativeEntry{Status: status, SentAt: sentAt}, ttl)
	if err != nil && !errors.Is(err, cluster.ErrUnavailable) {
		return err
	}
	return nil
}
