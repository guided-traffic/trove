package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
)

// Options are what [Open] and [New] need.
type Options struct {
	// Valkey is the Bastion's valkey section; mode none is the local state.
	Valkey config.BastionValkey
	// Replicas is bastion.replicas, the divisor of the local forms (Q-BAS-34).
	Replicas int
	// Instance is the replica name, which labels the client and names the holder of a lease.
	Instance string
	// Signer signs the answer records (R-BAS-032). Required with Valkey.
	Signer *cache.Signer
	Log    *slog.Logger
	// Registry receives trove_valkey_*, trove_ratelimit_decisions_total and trove_ratelimit_fallback; nil
	// registers nothing.
	Registry prometheus.Registerer
	// Metrics counts the answer records that failed their signature; nil counts nothing.
	Metrics *metrics.Metrics
	// Now is the clock of the limiters, the block list and the local forms; nil means time.Now.
	Now func() time.Time
}

// ErrNoSigner is returned when Valkey is configured without a signing key: an answer in Valkey is trusted
// only by its signature (R-BAS-032, Q-BAS-32).
var ErrNoSigner = errors.New("shared: Valkey holds answers only signed: a signing key is required")

// Open builds the state of a replica: the local one for valkey.mode none, and for sentinel or standalone the
// Valkey form behind the client of internal/cluster, made without a keyspace under the key prefix
// trove-bastion:<valkey.key_prefix>: (R-BAS-034). The returned closer stops the client.
//
// A Valkey that cannot be reached, or that refuses the credential, is not an error here: the client keeps
// trying, the state runs its local forms and the log and trove_valkey_available say so. A replica never
// becomes unready because of it (R-BAS-034).
func Open(ctx context.Context, o Options) (*State, io.Closer, error) {
	o = o.withDefaults()
	if o.Valkey.Mode == "none" || o.Valkey.Mode == "" {
		return NewLocal(o.Replicas, o.Now), noClose{}, nil
	}
	if o.Signer == nil {
		return nil, nil, ErrNoSigner
	}
	v, err := cluster.NewValkey(ctx, o.Valkey.Valkey(), o.Instance, "bastion", o.Log, o.Registry, cluster.WithoutKeyspace())
	if err != nil {
		return nil, nil, fmt.Errorf("shared: valkey: %w", err)
	}
	if serr := v.StartupError(); serr != nil {
		o.Log.Error("the Bastion's Valkey refuses the connection; the replica works with its local state and keeps trying", "error", serr)
	}
	st, err := New(v, o)
	if err != nil {
		v.Close()
		return nil, nil, err
	}
	return st, closer(v.Close), nil
}

type noClose struct{}

func (noClose) Close() error { return nil }

type closer func()

func (c closer) Close() error { c(); return nil }

func (o Options) withDefaults() Options {
	if o.Replicas < 1 {
		o.Replicas = 1
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return o
}

// New builds the Valkey state on a client made with cluster.WithoutKeyspace. Open is the usual way; tests
// that own the client call New.
func New(v *cluster.Valkey, o Options) (*State, error) {
	o = o.withDefaults()
	if o.Signer == nil {
		return nil, ErrNoSigner
	}
	set, err := cluster.NewLimiterSet(cluster.LimiterOptions{
		Valkey: v, Registry: o.Registry, LiveReplicas: func() int { return o.Replicas }, Now: o.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("shared: %w", err)
	}
	store := cluster.NewBastionStore(v)
	lim := &valkeyLimiter{set: set, local: newLocal(o.Replicas, o.Now), replicas: o.Replicas, limiters: map[string]*limiterEntry{}}
	return &State{
		Limiter:  lim,
		Blocks:   newValkeyBlocks(store, cluster.MarkBlock, o.Now),
		Timeouts: newValkeyBlocks(store, cluster.MarkTimeout, o.Now),
		Flights:  valkeyFlights{lock: cluster.NewFlightLock(v, cluster.FlightLockOptions{Registerer: o.Registry, InstanceUID: o.Instance})},
		Sweep:    valkeySweep{store: store},
		Answers:  &valkeyAnswers{store: store, signer: o.Signer, m: o.Metrics, log: o.Log},
		Shared:   v.Available,
	}, nil
}

// valkeyLimiter counts in Valkey through the limiters of R-ARCH-034 and in memory while Valkey cannot be used.
// Allow is the limiter's own Take, whose fallback divides the rate and the burst by the replicas; the counters
// of Add and Count fall back to the local sliding window, scaled by the replicas.
type valkeyLimiter struct {
	set      *cluster.LimiterSet
	local    *local
	replicas int

	mu       sync.Mutex
	limiters map[string]*limiterEntry
}

type limiterEntry struct {
	l      *cluster.Limiter
	window time.Duration // the window of a counter, or 0 for a token bucket
	rate   Rate
}

func (v *valkeyLimiter) Replicas() int { return v.replicas }

// entry returns the cluster limiter of a name, creating it on first use. A name keeps its parameters: a
// second call with others is a programming error, returned as one.
func (v *valkeyLimiter) entry(name string, window time.Duration, rate Rate) (*cluster.Limiter, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if e, ok := v.limiters[name]; ok {
		if e.window != window || e.rate != rate {
			return nil, fmt.Errorf("shared: limiter %q is already registered with other parameters", name)
		}
		return e.l, nil
	}
	var l *cluster.Limiter
	if window > 0 {
		// Only the window matters to a counter: burst over rate is the window.
		l = v.set.NewLimiter(name, cluster.ScopeCluster, 1000/window.Seconds(), 1000)
	} else {
		l = v.set.NewLimiter(name, cluster.ScopeCluster, rate.PerSecond, rate.Burst)
	}
	v.limiters[name] = &limiterEntry{l: l, window: window, rate: rate}
	return l, nil
}

func validName(name string) bool { return strings.HasPrefix(name, "bastion.") }

// Allow implements [Limiter].
func (v *valkeyLimiter) Allow(ctx context.Context, name, key string, rate Rate) (bool, time.Duration, error) {
	if !validName(name) {
		return false, 0, fmt.Errorf("shared: limiter %q must be named bastion.<name> (R-BAS-034)", name)
	}
	if rate.PerSecond <= 0 || rate.Burst < 1 {
		return v.local.Allow(ctx, name, key, rate)
	}
	l, err := v.entry(name, 0, rate)
	if err != nil {
		return false, 0, err
	}
	verdict := l.Take(ctx, key)
	return verdict.Allowed, verdict.RetryAfter, nil
}

func (v *valkeyLimiter) count(ctx context.Context, name, key string, window time.Duration, add bool) (int, error) {
	if !validName(name) {
		return 0, fmt.Errorf("shared: limiter %q must be named bastion.<name> (R-BAS-034)", name)
	}
	window = ClampWindow(window)
	l, err := v.entry(name, window, Rate{})
	if err != nil {
		return 0, err
	}
	n, err := l.Estimate(ctx, key, add)
	if err == nil {
		return n, nil
	}
	return v.local.count(name, key, window, add), nil
}

// Add implements [Limiter].
func (v *valkeyLimiter) Add(ctx context.Context, name, key string, window time.Duration) (int, error) {
	return v.count(ctx, name, key, window, true)
}

// Count implements [Limiter].
func (v *valkeyLimiter) Count(ctx context.Context, name, key string, window time.Duration) (int, error) {
	return v.count(ctx, name, key, window, false)
}

// valkeyBlocks is a [BlockList] in Valkey with a memory next to it. Set writes both; Blocked asks the memory
// first, which holds every block this replica set or served, and then Valkey, so a block set by another
// replica holds at the next request. The memory keeps a block while Valkey is unavailable, and keeps asking
// Valkey off the path of a blocked address: a blocked address costs no round trip.
//
// The record in Valkey is "<level>:<until in Unix ms>"; the key's own expiry is the remaining duration. Until
// comes from the record, not from the key's TTL, so the clock of the state decides (the tests inject one).
type valkeyBlocks struct {
	store *cluster.BastionStore
	kind  cluster.MarkKind
	now   func() time.Time
	mem   *memBlocks
}

func newValkeyBlocks(store *cluster.BastionStore, kind cluster.MarkKind, now func() time.Time) *valkeyBlocks {
	return &valkeyBlocks{store: store, kind: kind, now: now, mem: newMemBlocks(now)}
}

func encodeRecord(level int, t time.Time) string {
	return strconv.Itoa(level) + ":" + strconv.FormatInt(t.UnixMilli(), 10)
}

func decodeRecord(s string) (level int, t time.Time, ok bool) {
	a, b, found := strings.Cut(s, ":")
	if !found {
		return 0, time.Time{}, false
	}
	lv, err1 := strconv.Atoi(a)
	ms, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || lv < 0 {
		return 0, time.Time{}, false
	}
	return lv, time.UnixMilli(ms), true
}

// Blocked implements [BlockList].
func (b *valkeyBlocks) Blocked(ctx context.Context, addr string) (Block, bool, error) {
	if blk, ok, _ := b.mem.Blocked(ctx, addr); ok {
		return blk, true, nil
	}
	val, found, err := b.store.Marked(ctx, b.kind, addr)
	if err != nil || !found {
		return Block{}, false, nil //nolint:nilerr // Valkey being unavailable means: the local list is the list
	}
	level, until, ok := decodeRecord(val)
	if !ok || !until.After(b.now()) {
		return Block{}, false, nil
	}
	blk := Block{Until: until, Level: level}
	b.mem.mu.Lock()
	b.mem.put(addr, blk)
	b.mem.mu.Unlock()
	return blk, true, nil
}

// Set implements [BlockList]. The memory is written first: the block holds on this replica whatever Valkey does.
func (b *valkeyBlocks) Set(ctx context.Context, addr string, d time.Duration, level int) error {
	_ = b.mem.Set(ctx, addr, d, level)
	now := b.now()
	err := b.store.Mark(ctx, b.kind, addr, encodeRecord(level, now.Add(d)), d)
	if b.kind == cluster.MarkBlock {
		err = errors.Join(err, b.store.Mark(ctx, cluster.MarkLevel, addr, encodeRecord(level, now), MaxLevelAge))
	}
	return err
}

// Level implements [BlockList].
func (b *valkeyBlocks) Level(ctx context.Context, addr string) (int, error) {
	lv, _ := b.mem.Level(ctx, addr)
	val, found, err := b.store.Marked(ctx, cluster.MarkLevel, addr)
	if err != nil || !found {
		return lv, nil //nolint:nilerr // see Blocked
	}
	if level, at, ok := decodeRecord(val); ok && b.now().Sub(at) <= MaxLevelAge {
		lv = max(lv, level)
	}
	return lv, nil
}

// Size implements [BlockList]: the addresses blocked now that this replica set or served. Valkey has no
// command for the number of keys of a family that the ACL user may run, so a replica counts the blocks it
// knows; the sum of the gauge over the replicas can count one block more than once.
func (b *valkeyBlocks) Size(ctx context.Context) (int, error) { return b.mem.Size(ctx) }

// valkeyFlights is the fetch lock in Valkey.
type valkeyFlights struct{ lock cluster.FlightLock }

// TryAcquire implements [FlightLock].
func (f valkeyFlights) TryAcquire(ctx context.Context, keyHash string) (bool, func(), error) {
	key, err := cluster.FlightKeyFromHash(keyHash)
	if err != nil {
		return true, func() {}, err
	}
	hold, ok, err := f.lock.Acquire(ctx, key)
	if err != nil {
		return true, func() {}, err
	}
	if !ok {
		return false, func() {}, nil
	}
	return true, func() { _ = hold.Release(context.WithoutCancel(ctx), cluster.OutcomeStored, "") }, nil
}

// valkeySweep is the sweep lease in Valkey.
type valkeySweep struct{ store *cluster.BastionStore }

// TryAcquire implements [SweepLease]. A lease that cannot be asked about is held: every replica sweeps.
func (s valkeySweep) TryAcquire(ctx context.Context, ttl time.Duration) (bool, func(), error) {
	held, release, err := s.store.AcquireSweep(ctx, ttl)
	if err != nil {
		return true, func() {}, nil //nolint:nilerr // a sweep only deletes: without the lease every replica sweeps (R-BAS-031)
	}
	return held, release, nil
}

// valkeyAnswers is the [AnswerStore] in Valkey: signed on Put, verified on Get.
type valkeyAnswers struct {
	store  *cluster.BastionStore
	signer *cache.Signer
	m      *metrics.Metrics
	log    *slog.Logger
}

// domainKey is what a record's signature covers besides the payload: the kind and the key, so a record
// moved to another key or kind fails.
func domainKey(kind AnswerKind, keyHash string) string { return string(kind) + ":" + keyHash }

// Put implements [AnswerStore]. A ttl of zero or less stores nothing: the answer is not valid any more.
func (a *valkeyAnswers) Put(ctx context.Context, kind AnswerKind, keyHash string, record []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	return a.store.PutAnswer(ctx, string(kind), keyHash, a.signer.Sign(cache.DomainAnswer, domainKey(kind, keyHash), record), ttl)
}

// Get implements [AnswerStore]. A record that fails its signature is not returned: it is deleted (only if it
// still holds what failed) and counted.
func (a *valkeyAnswers) Get(ctx context.Context, kind AnswerKind, keyHash string) ([]byte, bool, error) {
	raw, found, err := a.store.GetAnswer(ctx, string(kind), keyHash)
	if err != nil || !found {
		return nil, false, err
	}
	payload, verr := a.signer.Verify(cache.DomainAnswer, domainKey(kind, keyHash), raw)
	if verr == nil {
		return payload, true, nil
	}
	a.log.Warn("an answer record in Valkey failed its signature and is ignored", "kind", string(kind))
	if a.m != nil && kind != AnswerNegative {
		a.m.AnswersTotal.WithLabelValues(string(kind), metrics.AnswerBadSignature).Inc()
	}
	_ = a.store.DeleteAnswer(ctx, string(kind), keyHash, raw)
	return nil, false, nil
}

// Delete implements [AnswerStore]: it deletes whatever the key holds now (read, then delete if unchanged).
func (a *valkeyAnswers) Delete(ctx context.Context, kind AnswerKind, keyHash string) error {
	raw, found, err := a.store.GetAnswer(ctx, string(kind), keyHash)
	if err != nil || !found {
		return err
	}
	return a.store.DeleteAnswer(ctx, string(kind), keyHash, raw)
}

// SweepLease is the lease of the one replica that sweeps a shared bucket while the state is shared, nil otherwise;
// it makes the state the cache's cache.LeaseSource (R-BAS-031).
func (s *State) SweepLease() cache.SweepLease {
	if s == nil || s.Shared == nil || !s.Shared() || s.Sweep == nil {
		return nil
	}
	return s.Sweep
}
