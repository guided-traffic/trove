package cluster

import (
	"context"
	"math"
)

// Estimate is the counting form of a limiter that needs the number: the events of bucket inside the sliding
// window, rounded up (previous x (1 - elapsed / W) + current, the estimate of the other forms). With add it
// counts one event first and the number includes it; without, it reads only. The trove Bastion uses it for
// the counters that lead to a block or a timeout (R-BAS-036, R-BAS-037), which compare the number with a
// threshold of their own.
//
// It never falls back: while Valkey cannot be used it returns [ErrUnavailable] (and marks the limiter as in
// its fallback for trove_ratelimit_fallback), and the caller counts in its own memory with the share of the
// threshold that is its own.
func (l *Limiter) Estimate(ctx context.Context, bucket string, add bool) (int, error) {
	if l.scope != ScopeCluster {
		return 0, ErrUnavailable
	}
	if !l.usesValkey() {
		if l.set.store != nil {
			l.set.store.degraded()
		}
		l.inFallback.Store(true)
		return 0, ErrUnavailable
	}
	now := l.set.now()
	idx, el := l.windowIndex(now)
	var cur, prev int64
	var err error
	if add {
		cur, prev, err = l.set.store.hit(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window)
	} else {
		cur, prev, err = l.set.store.read(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window)
	}
	if err != nil {
		l.set.store.degraded()
		l.inFallback.Store(true)
		return 0, ErrUnavailable
	}
	l.inFallback.Store(false)
	return int(math.Ceil(float64(prev)*(1-el) + float64(cur))), nil
}
