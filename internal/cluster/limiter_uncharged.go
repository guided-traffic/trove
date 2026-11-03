package cluster

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/valkey-io/valkey-go"
)

// The Bastion's limits do not charge what they refuse (Q-BAS-127): a limit admits up to its rate, and a refused
// request does not use up the budget of its address, so a client slightly over its rate keeps being admitted at
// the rate instead of starving. [Limiter.Take] charges the refused ones (R-ARCH-034, the main installation's
// logins and API limits want that: a caller that keeps sending stays refused); the two methods below are the
// other reading, for the counters of the Bastion's guard.
//
// The window counter is still incremented before the decision, in one round trip and without a script. A
// refusal then counts itself a second time in a counter of refusals of its own, with the same window and
// expiry, and the estimate of the window is the counter minus the refusals: previous x (1 - elapsed / W) +
// current, each net of what was refused. Only the commands the Bastion's Valkey user may run are used (INCR,
// EXPIRE, GET, MULTI, EXEC). A request that is decided while another one's refusal is not counted yet sees one
// count too many; the error is bounded by the requests of one address in flight and only ever refuses.

// refusedSuffix names the counter of refusals of a limiter.
const refusedSuffix = "-refused"

// unchargedStore is a windowStore that keeps the counter of refusals. The Valkey store does; the map of the
// unit tests does not.
type unchargedStore interface {
	hitNet(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (cur, prev int64, err error)
	refuse(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) error
}

// hitNet is windowHit with the counters of refusals: MULTI, INCR of the current window's key, EXPIRE NX, GET of
// the previous window's key, GET of the current and of the previous refusal keys, EXEC. It returns the counters
// net of the refusals; the current one includes this request.
func (w valkeyWindows) hitNet(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (cur, prev int64, err error) {
	v := w.v
	ck, pk, err := v.windowKeys(limiter, bucket, idx, ttl)
	if err != nil {
		return 0, 0, err
	}
	rck, rpk, err := v.windowKeys(limiter+refusedSuffix, bucket, idx, ttl)
	if err != nil {
		return 0, 0, err
	}
	err = v.exec(ctx, "incr", func(ctx context.Context, c valkey.Client) error {
		res := c.DoMulti(ctx,
			c.B().Multi().Build(),
			c.B().Incr().Key(ck.name).Build(),
			c.B().Expire().Key(ck.name).Seconds(ceilSeconds(ck.ttl)).Nx().Build(),
			c.B().Get().Key(pk.name).Build(),
			c.B().Get().Key(rck.name).Build(),
			c.B().Get().Key(rpk.name).Build(),
			c.B().Exec().Build(),
		)
		for _, r := range res[:6] {
			if err := r.Error(); err != nil {
				return err
			}
		}
		out, err := res[6].ToArray()
		if err != nil {
			return err
		}
		if len(out) != 5 {
			return fmt.Errorf("EXEC returned %d replies, want 5", len(out))
		}
		var rcur, rprev, p int64
		if cur, err = out[0].AsInt64(); err != nil {
			return err
		}
		if p, err = optionalCounter(out[2]); err != nil {
			return err
		}
		if rcur, err = optionalCounter(out[3]); err != nil {
			return err
		}
		if rprev, err = optionalCounter(out[4]); err != nil {
			return err
		}
		cur, prev = max(0, cur-rcur), max(0, p-rprev)
		return nil
	})
	return cur, prev, err
}

// refuse counts a refusal of the window idx.
func (w valkeyWindows) refuse(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) error {
	rck, _, err := w.v.windowKeys(limiter+refusedSuffix, bucket, idx, ttl)
	if err != nil {
		return err
	}
	_, err = w.v.incr(ctx, rck)
	return err
}

// markRefused counts a refusal; a failure only leaves the refused event counted until its window ends.
func (l *Limiter) markRefused(ctx context.Context, s unchargedStore, bucket string, idx int64) {
	_ = s.refuse(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window)
}

// TakeUncharged is [Limiter.Take] for a limit whose refusals do not consume it: the request is admitted when
// the admitted requests of the sliding window, this one included, stay within the burst, and the Retry-After of
// a refusal is computed from the counters without it. In the fallback it is the token bucket of Take, which
// takes nothing when it refuses; on a store without the counters of refusals it is Take.
func (l *Limiter) TakeUncharged(ctx context.Context, bucket string) Verdict {
	s, ok := l.set.store.(unchargedStore)
	if l.scope != ScopeCluster || !ok || !l.usesValkey() {
		return l.Take(ctx, bucket)
	}
	now := l.set.now()
	idx, el := l.windowIndex(now)
	cur, prev, err := s.hitNet(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window)
	if err != nil {
		l.set.store.degraded()
		l.inFallback.Store(true)
		return l.record(l.takeLocal(bucket, now, l.share(), 1, ModeFallback))
	}
	l.inFallback.Store(false)
	v := l.windowVerdict(cur, prev, el, 1)
	if !v.Allowed {
		l.markRefused(ctx, s, bucket, idx)
		v.RetryAfter = wholeSeconds(l.retryAfter(cur-1, prev, el, 1))
	}
	return l.record(v)
}

// EstimateWithin is [Limiter.Estimate] with add, for a counter that is a limit: the event is counted only when
// the number, this event included, stays within limit; a refused one takes nothing from the budget. It returns
// whether the event was admitted and the number with it. Like Estimate it never falls back: [ErrUnavailable]
// while Valkey cannot be used, and on a store without the counters of refusals.
func (l *Limiter) EstimateWithin(ctx context.Context, bucket string, limit int) (admitted bool, n int, err error) {
	s, ok := l.set.store.(unchargedStore)
	if l.scope != ScopeCluster || !ok {
		return false, 0, ErrUnavailable
	}
	if !l.usesValkey() {
		l.set.store.degraded()
		l.inFallback.Store(true)
		return false, 0, ErrUnavailable
	}
	idx, el := l.windowIndex(l.set.now())
	cur, prev, err := s.hitNet(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window)
	if err != nil {
		l.set.store.degraded()
		l.inFallback.Store(true)
		return false, 0, ErrUnavailable
	}
	l.inFallback.Store(false)
	n = int(math.Ceil(float64(prev)*(1-el) + float64(cur)))
	if n > limit {
		l.markRefused(ctx, s, bucket, idx)
		return false, n, nil
	}
	return true, n, nil
}
