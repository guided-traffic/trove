package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Scope is where a limiter counts (R-ARCH-034).
type Scope string

const (
	// ScopeCluster counts across all replicas in Valkey and falls back to a
	// share of the budget per replica while Valkey cannot be used.
	ScopeCluster Scope = "cluster"
	// ScopeReplica counts in the replica's memory; it protects the replica
	// itself and never uses Valkey.
	ScopeReplica Scope = "replica"
)

// Mode is how one decision was made; it is the mode label of
// trove_ratelimit_decisions_total.
type Mode string

// The decision modes.
const (
	ModeCluster  Mode = "cluster"
	ModeFallback Mode = "fallback"
	ModeReplica  Mode = "replica"
)

// Verdict is the outcome of one check.
type Verdict struct {
	Allowed bool
	// Limit is the burst of the policy; during the fallback the replica's share.
	Limit int
	// Remaining is what the bucket still admits, at least 0.
	Remaining int
	// RetryAfter is, when the request is refused, the time after which the next
	// request would be admitted if the caller sent nothing more, rounded up to
	// whole seconds and at least one second. Zero when allowed.
	RetryAfter time.Duration
	// Reset is the time until the current window ends (the t of the RateLimit
	// field).
	Reset time.Duration
	// Window is the window of the policy, burst divided by rate (at least 1 s).
	Window time.Duration
	Mode   Mode
}

// windowStore is the counter storage of the cluster scope. *Valkey is the
// production implementation (valkey_ratelimit.go); the unit tests use a map.
type windowStore interface {
	hit(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (cur, prev int64, err error)
	read(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (cur, prev int64, err error)
	count(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) error
	available() bool
	degraded()
}

type valkeyWindows struct{ v *Valkey }

func (w valkeyWindows) hit(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (int64, int64, error) {
	return w.v.windowHit(ctx, limiter, bucket, idx, ttl)
}

func (w valkeyWindows) read(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) (int64, int64, error) {
	return w.v.windowRead(ctx, limiter, bucket, idx, ttl)
}

func (w valkeyWindows) count(ctx context.Context, limiter, bucket string, idx int64, ttl time.Duration) error {
	return w.v.windowCount(ctx, limiter, bucket, idx, ttl)
}

func (w valkeyWindows) available() bool { return w.v.Available() }
func (w valkeyWindows) degraded()       { w.v.CountDegraded(MechanismRateLimit) }

// LimiterOptions configure [NewLimiterSet].
type LimiterOptions struct {
	// Valkey carries the cluster-scoped counters; nil means every limiter runs
	// its fallback.
	Valkey *Valkey
	// Registry receives trove_ratelimit_decisions_total and
	// trove_ratelimit_fallback; nil registers nothing.
	Registry prometheus.Registerer
	// LiveReplicas returns the number n of live replicas of this replica's role
	// in the replica registry, refreshed with the heartbeat (R-ARCH-034). Values
	// below 1 count as 1; nil means 1.
	LiveReplicas func() int
	// Now is the clock; nil means time.Now. Tests set it.
	Now func() time.Time
	// store replaces the Valkey counters in unit tests.
	store windowStore
}

// MaxFallbackBuckets bounds the buckets one limiter keeps in memory while it
// runs its fallback or has replica scope. When it is reached, idle buckets are
// dropped first; a bucket that is dropped starts full, which admits at most one
// extra burst of its caller.
const MaxFallbackBuckets = 100_000

// LimiterSet creates the named limiters of a replica and exports their metrics.
type LimiterSet struct {
	store windowStore
	n     func() int
	now   func() time.Time

	decisions *prometheus.CounterVec

	mu       sync.Mutex
	limiters map[string]*Limiter
}

// NewLimiterSet creates the factory of the limiters of one replica.
func NewLimiterSet(o LimiterOptions) (*LimiterSet, error) {
	ls := &LimiterSet{store: o.store, n: o.LiveReplicas, now: o.Now, limiters: map[string]*Limiter{}}
	if ls.store == nil && o.Valkey != nil {
		ls.store = valkeyWindows{o.Valkey}
	}
	if ls.now == nil {
		ls.now = time.Now
	}
	ls.decisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "trove_ratelimit_decisions_total",
		Help: "Rate-limit checks by limiter, mode (cluster, fallback, replica) and outcome (admitted, refused).",
	}, []string{"limiter", "mode", "outcome"})
	if o.Registry != nil {
		if err := o.Registry.Register(ls.decisions); err != nil {
			return nil, fmt.Errorf("cluster: registering trove_ratelimit_decisions_total: %w", err)
		}
		if err := o.Registry.Register(fallbackCollector{ls}); err != nil {
			return nil, fmt.Errorf("cluster: registering trove_ratelimit_fallback: %w", err)
		}
	}
	return ls, nil
}

var limiterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// NewLimiter registers a limiter: name is lower case words separated by "_" or
// "." (api_read, auth.credential_failures), rate the sustained requests per
// second and burst the most requests one window admits. The window is burst
// divided by rate, at least one second (R-ARCH-034). It panics on a bad or
// duplicate name or a window above 12 hours, because each is a programming
// error that start-up must surface.
func (ls *LimiterSet) NewLimiter(name string, scope Scope, rate float64, burst int) *Limiter {
	return ls.NewLimiterShared(name, scope, rate, burst, nil)
}

// NewLimiterShared is NewLimiter for a limiter whose fallback share divides by a
// number of its own instead of the live replicas of the role: share returns the
// live replicas that serve what the limiter guards, for example the replicas of
// an endpoint's pool (R-EP-015). Values below 1 count as 1; nil is NewLimiter.
func (ls *LimiterSet) NewLimiterShared(name string, scope Scope, rate float64, burst int, share func() int) *Limiter {
	if !limiterNamePattern.MatchString(name) {
		panic(fmt.Sprintf("cluster: invalid limiter name %q", name))
	}
	if scope != ScopeCluster && scope != ScopeReplica {
		panic(fmt.Sprintf("cluster: limiter %q has scope %q", name, scope))
	}
	if rate <= 0 || burst < 1 {
		panic(fmt.Sprintf("cluster: limiter %q needs a positive rate and a burst of 1 or more", name))
	}
	w := time.Duration(float64(burst) / rate * float64(time.Second))
	if w < time.Second {
		w = time.Second
	}
	if w > maxLimiterWindow {
		panic(fmt.Sprintf("cluster: limiter %q has a window of %s, the longest is %s", name, w, maxLimiterWindow))
	}
	// Whole milliseconds, so the window index is an integer.
	w = w.Truncate(time.Millisecond)
	l := &Limiter{
		set: ls, name: name, keyName: strings.ReplaceAll(name, ".", "-"), scope: scope,
		rate: rate, burst: burst, window: w, buckets: map[string]*bucket{}, shareFn: share,
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if _, dup := ls.limiters[name]; dup {
		panic(fmt.Sprintf("cluster: limiter %q is registered twice", name))
	}
	ls.limiters[name] = l
	return l
}

// share returns n, the number of live replicas of the role, at least 1.
func (ls *LimiterSet) share() int {
	if ls.n == nil {
		return 1
	}
	return max(1, ls.n())
}

// Limiter is one named limiter. It is safe for concurrent use.
type Limiter struct {
	set     *LimiterSet
	name    string
	keyName string
	scope   Scope
	rate    float64
	burst   int
	window  time.Duration

	inFallback atomic.Bool
	// shareFn is the n of the fallback share when the limiter has its own
	// (NewLimiterShared); nil means the set's.
	shareFn func() int

	mu      sync.Mutex
	buckets map[string]*bucket
}

// share returns the n the fallback divides the budget by, at least 1.
func (l *Limiter) share() int {
	if l.shareFn != nil {
		return max(1, l.shareFn())
	}
	return l.set.share()
}

// bucket is the local state of one bucket key: a token bucket for Take, and
// sliding-window counters for the count-only form.
type bucket struct {
	tokens   float64
	updated  time.Time
	idx      int64
	cur, pre int64
}

// Name is the limiter's name.
func (l *Limiter) Name() string { return l.name }

// Burst is the most requests one window admits.
func (l *Limiter) Burst() int { return l.burst }

// Window is the window of the policy.
func (l *Limiter) Window() time.Duration { return l.window }

// Rate is the sustained requests per second.
func (l *Limiter) Rate() float64 { return l.rate }

func (l *Limiter) bucketKey(bucket string) string {
	sum := sha256.Sum256([]byte(l.name + "\x00" + bucket))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (l *Limiter) windowIndex(now time.Time) (idx int64, elapsed float64) {
	w := l.window.Milliseconds()
	ms := now.UnixMilli()
	return ms / w, float64(ms%w) / float64(w)
}

// usesValkey reports whether a check should try the shared counters.
func (l *Limiter) usesValkey() bool {
	return l.scope == ScopeCluster && l.set.store != nil && l.set.store.available()
}

// Allow is Take reduced to its verdict: it counts the request, then reports
// whether it was admitted.
func (l *Limiter) Allow(ctx context.Context, bucket string) bool { return l.Take(ctx, bucket).Allowed }

// Take counts one request of bucket and decides (R-ARCH-034). In the cluster
// scope the counter is incremented before the decision, so a refused request
// uses up budget like an admitted one: a caller that keeps sending while
// refused stays refused until its rate falls below the limit. While Valkey
// cannot be used the check runs against a local token bucket with the rate
// divided by the number of live replicas; it never fails and never waits for
// more than valkey.command_timeout.
func (l *Limiter) Take(ctx context.Context, bucket string) Verdict {
	return l.TakeAt(ctx, bucket, time.Time{})
}

// TakeAt is Take for a request that arrived at at. In the cluster scope it is
// counted in, and decided against, the window it arrived in (Q-HA-66): a request
// that a stage before the limiter held, the token lookup during a database
// switchover for one, would otherwise be charged to the window it is released
// in, where the held requests of a client below its rate add up to a burst it
// never sent. A zero or future at is now; the local buckets decide at now.
func (l *Limiter) TakeAt(ctx context.Context, bucket string, at time.Time) Verdict {
	now := l.set.now()
	if at.IsZero() || at.After(now) {
		at = now
	}
	if l.scope == ScopeReplica {
		return l.record(l.takeLocal(bucket, now, 1, ModeReplica))
	}
	if l.usesValkey() {
		idx, el := l.windowIndex(at)
		cur, prev, err := l.set.store.hit(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window)
		if err == nil {
			l.inFallback.Store(false)
			return l.record(l.windowVerdict(cur, prev, el))
		}
		l.set.store.degraded()
	} else if l.set.store != nil {
		l.set.store.degraded()
	}
	l.inFallback.Store(true)
	return l.record(l.takeLocal(bucket, now, l.share(), ModeFallback))
}

// windowVerdict applies the sliding-window rule to the counters of a check
// that counted the request: admitted when previous x (1 - elapsed / W) +
// current <= burst.
func (l *Limiter) windowVerdict(cur, prev int64, elapsed float64) Verdict {
	est := float64(prev)*(1-elapsed) + float64(cur)
	v := Verdict{
		Allowed: est <= float64(l.burst), Limit: l.burst, Window: l.window, Mode: ModeCluster,
		Remaining: max(0, int(math.Floor(float64(l.burst)-est))),
		Reset:     time.Duration((1 - elapsed) * float64(l.window)),
	}
	if !v.Allowed {
		v.RetryAfter = wholeSeconds(l.retryAfter(cur, prev, elapsed))
	}
	return v
}

// retryAfter is the time after which a request would be admitted if the caller
// sent nothing more, computed from the counters of the check. It solves the
// admission rule for the rest of the current window, and when the current
// window cannot admit again, for the next one, whose previous counter is cur.
func (l *Limiter) retryAfter(cur, prev int64, elapsed float64) time.Duration {
	burst, w := float64(l.burst), float64(l.window)
	// Within the current window the next request counts as cur+1 and is admitted
	// when prev x (1 - x') <= burst - cur - 1, that is from x' = 1 - need/prev.
	if need := burst - float64(cur) - 1; need >= 0 {
		if prev == 0 {
			return 0
		}
		if x := 1 - need/float64(prev); x < 1 {
			return time.Duration((max(x, elapsed) - elapsed) * w)
		}
	}
	// Next window: previous = cur, current = 0, the next request counts as 1.
	y := 0.0
	if c := float64(cur); c > 0 {
		y = max(0, 1-(burst-1)/c)
	}
	return time.Duration(((1 - elapsed) + y) * w)
}

func wholeSeconds(d time.Duration) time.Duration {
	s := time.Duration(math.Ceil(d.Seconds())) * time.Second
	return max(s, time.Second)
}

// takeLocal is the token bucket of the fallback and of the replica scope. The
// bucket has rate/n and burst max(1, burst/n).
func (l *Limiter) takeLocal(key string, now time.Time, n int, mode Mode) Verdict {
	rate := l.rate / float64(n)
	capacity := float64(max(1, l.burst/n))
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.local(key, now)
	if b.updated.IsZero() {
		b.tokens, b.updated = capacity, now
	} else if dt := now.Sub(b.updated).Seconds(); dt > 0 {
		b.tokens, b.updated = math.Min(capacity, b.tokens+dt*rate), now
	}
	v := Verdict{Limit: int(capacity), Window: l.window, Mode: mode, Reset: l.window}
	if b.tokens >= 1 {
		b.tokens--
		v.Allowed = true
	} else {
		v.RetryAfter = wholeSeconds(time.Duration((1 - b.tokens) / rate * float64(time.Second)))
	}
	v.Remaining = int(math.Floor(b.tokens))
	return v
}

// local returns the bucket of key, creating it. The caller holds l.mu.
func (l *Limiter) local(key string, now time.Time) *bucket {
	hashed := l.bucketKey(key)
	if b, ok := l.buckets[hashed]; ok {
		return b
	}
	if len(l.buckets) >= MaxFallbackBuckets {
		l.sweep(now)
	}
	b := &bucket{}
	l.buckets[hashed] = b
	return b
}

// sweep drops buckets that are idle for two windows, and when that frees
// nothing, an arbitrary bucket, so the map stays bounded. The caller holds l.mu.
func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.updated) > 2*l.window {
			delete(l.buckets, k)
		}
	}
	for k := range l.buckets {
		if len(l.buckets) < MaxFallbackBuckets {
			return
		}
		delete(l.buckets, k)
	}
}

// Exceeded reports whether the failures counted for bucket in the sliding
// window have used up the burst: previous x (1 - elapsed / W) + current >= burst,
// so a limit of n failures admits n checks and refuses the next (Q-AUTH-20). It
// reads the counters and writes nothing. It is the check of the count-only
// form, made before a request is processed (R-ARCH-034). While Valkey cannot
// be used it asks the local counters, whose limit is max(1, burst/n).
func (l *Limiter) Exceeded(ctx context.Context, bucket string) bool {
	now := l.set.now()
	idx, el := l.windowIndex(now)
	if l.scope == ScopeCluster && l.usesValkey() {
		cur, prev, err := l.set.store.read(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window)
		if err == nil {
			l.inFallback.Store(false)
			return float64(prev)*(1-el)+float64(cur) >= float64(l.burst)
		}
		l.set.store.degraded()
	} else if l.scope == ScopeCluster && l.set.store != nil {
		l.set.store.degraded()
	}
	limit := l.burst
	if l.scope == ScopeCluster {
		l.inFallback.Store(true)
		limit = max(1, l.burst/l.share())
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.local(bucket, now)
	cur, prev := b.rolled(idx)
	exceeded := float64(prev)*(1-el)+float64(cur) >= float64(limit)
	l.countOutcome(l.localMode(), !exceeded)
	return exceeded
}

// Count records one failure of bucket: MULTI, INCR of the current window's key,
// EXPIRE NX, EXEC. While Valkey cannot be used it counts locally.
func (l *Limiter) Count(ctx context.Context, bucket string) {
	now := l.set.now()
	idx, _ := l.windowIndex(now)
	if l.scope == ScopeCluster && l.usesValkey() {
		if err := l.set.store.count(ctx, l.keyName, l.bucketKey(bucket), idx, 2*l.window); err == nil {
			l.inFallback.Store(false)
			return
		}
		l.set.store.degraded()
	} else if l.scope == ScopeCluster && l.set.store != nil {
		l.set.store.degraded()
	}
	if l.scope == ScopeCluster {
		l.inFallback.Store(true)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.local(bucket, now)
	cur, _ := b.rolled(idx)
	b.cur = cur + 1
	b.updated = now
}

// rolled moves the sliding-window counters to window idx and returns them.
func (b *bucket) rolled(idx int64) (cur, prev int64) {
	switch b.idx {
	case idx:
	case idx - 1:
		b.pre, b.cur, b.idx = b.cur, 0, idx
	default:
		b.pre, b.cur, b.idx = 0, 0, idx
	}
	return b.cur, b.pre
}

func (l *Limiter) localMode() Mode {
	if l.scope == ScopeReplica {
		return ModeReplica
	}
	return ModeFallback
}

func (l *Limiter) record(v Verdict) Verdict {
	l.countOutcome(v.Mode, v.Allowed)
	return v
}

func (l *Limiter) countOutcome(mode Mode, admitted bool) {
	outcome := "refused"
	if admitted {
		outcome = "admitted"
	}
	l.set.decisions.WithLabelValues(l.name, string(mode), outcome).Inc()
}

// fallbackCollector exports trove_ratelimit_fallback{limiter}: 1 while a
// cluster-scoped limiter answers from its local share, which is the case from
// its first fallback decision until a decision used the shared counters again,
// and whenever Valkey cannot be used at all.
type fallbackCollector struct{ ls *LimiterSet }

var fallbackDesc = prometheus.NewDesc("trove_ratelimit_fallback",
	"1 while a cluster-scoped limiter answers from per-replica shares because Valkey is unavailable.", []string{"limiter"}, nil)

func (c fallbackCollector) Describe(ch chan<- *prometheus.Desc) { ch <- fallbackDesc }

func (c fallbackCollector) Collect(ch chan<- prometheus.Metric) {
	c.ls.mu.Lock()
	ls := make([]*Limiter, 0, len(c.ls.limiters))
	for _, l := range c.ls.limiters {
		ls = append(ls, l)
	}
	c.ls.mu.Unlock()
	down := c.ls.store != nil && !c.ls.store.available()
	for _, l := range ls {
		if l.scope != ScopeCluster {
			continue
		}
		v := 0.0
		if l.inFallback.Load() || down {
			v = 1
		}
		ch <- prometheus.MustNewConstMetric(fallbackDesc, prometheus.GaugeValue, v, l.name)
	}
}
