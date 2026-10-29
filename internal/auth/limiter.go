package auth

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

// FailureLimiter is the count-only limiter of R-ARCH-034 that the failed-credential
// throttle of R-AUTH-038 uses: the stage asks whether an address is over its
// budget before it validates a credential that needs a lookup, and counts one
// failure after a rejection. *cluster.Limiter implements it (Exceeded and Count
// of the count-only form); the implementation is cluster-wide in Valkey and falls
// back to per-replica buckets of rate ÷ n while Valkey is unavailable, so neither
// call ever fails a request.
type FailureLimiter interface {
	// Exceeded reports whether previous × (1 − elapsed ÷ W) + current has used
	// up the burst for the bucket.
	Exceeded(ctx context.Context, bucket string) bool
	// Count records one failure for the bucket.
	Count(ctx context.Context, bucket string)
}

// LocalLimiter is a [FailureLimiter] that counts in the replica's memory with the
// same sliding-window arithmetic as the cluster limiter. It serves a replica
// that has no cluster limiter and is the reference of the per-replica share that
// the cluster limiter falls back to while Valkey is unavailable (Q-AUTH-11): the
// burst is the configured value divided by the number of live replicas, at least
// one, so the cluster-wide budget stays close to the configured one.
type LocalLimiter struct {
	burst  int
	window time.Duration
	// replicas returns the number of live replicas of the role; nil means one.
	replicas func() int
	now      func() time.Time

	mu      sync.Mutex
	buckets map[string]*windowCount
	swept   time.Time
}

type windowCount struct {
	start    time.Time
	cur, pre float64
}

// NewLocalLimiter creates the limiter: burst failures per window and bucket,
// divided by replicas() (nil: 1). now overrides the clock for tests.
func NewLocalLimiter(burst int, window time.Duration, replicas func() int, now func() time.Time) *LocalLimiter {
	if now == nil {
		now = time.Now
	}
	return &LocalLimiter{burst: burst, window: window, replicas: replicas, now: now, buckets: map[string]*windowCount{}}
}

var _ FailureLimiter = (*LocalLimiter)(nil)

func (l *LocalLimiter) share() float64 {
	n := 1
	if l.replicas != nil {
		n = max(1, l.replicas())
	}
	return max(1, float64(l.burst)/float64(n))
}

// roll advances the bucket's windows to now. It must run with l.mu held.
func (l *LocalLimiter) roll(b string, now time.Time) *windowCount {
	w := l.buckets[b]
	if w == nil {
		w = &windowCount{start: now}
		l.buckets[b] = w
	}
	switch elapsed := now.Sub(w.start); {
	case elapsed >= 2*l.window:
		w.start, w.cur, w.pre = now, 0, 0
	case elapsed >= l.window:
		w.start, w.pre, w.cur = w.start.Add(l.window), w.cur, 0
	}
	return w
}

// Exceeded implements [FailureLimiter].
func (l *LocalLimiter) Exceeded(_ context.Context, bucket string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w := l.roll(bucket, now)
	frac := float64(now.Sub(w.start)) / float64(l.window)
	// At or above the budget, not only above it: the 300 failures a minute of
	// R-AUTH-038 are 300 validations, so the 301st request is the first refused.
	return w.pre*(1-frac)+w.cur >= l.share()
}

// Count implements [FailureLimiter].
func (l *LocalLimiter) Count(_ context.Context, bucket string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.roll(bucket, now).cur++
	if len(l.buckets) > 4096 && now.Sub(l.swept) > l.window {
		l.swept = now
		for k, w := range l.buckets {
			if now.Sub(w.start) >= 2*l.window {
				delete(l.buckets, k)
			}
		}
	}
}

// addrThrottle is the token.Throttle of one request: the client address is the
// bucket, and an exempt address is never throttled (auth.rate_limit.exempt_cidrs).
type addrThrottle struct {
	l      FailureLimiter
	bucket string
}

func (t addrThrottle) Exceeded(ctx context.Context) bool { return t.l.Exceeded(ctx, t.bucket) }
func (t addrThrottle) Count(ctx context.Context)         { t.l.Count(ctx, t.bucket) }

func exempt(addr netip.Addr, cidrs []netip.Prefix) bool {
	for _, p := range cidrs {
		if p.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}
