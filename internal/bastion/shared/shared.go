// Package shared is the state the replicas of a Bastion may share when Valkey is configured and keep per
// replica when it is not (R-BAS-034, Q-BAS-11): the block list, the rate-limit and failed-authentication
// counters, the fetch locks, the sweep lease and the signed answer records. Its consumers (the guard, the
// engine, the cache sweeps) hold the interfaces below and never know which implementation runs; losing Valkey
// costs performance, never correctness (INV-3, R-BAS-034).
//
// [NewLocal] is the implementation without Valkey: it works per replica, with the rates and thresholds divided
// by the chart's replica count (bastion.replicas, Q-BAS-34), so the sum over replicas stays near the configured
// value. Work package 6 adds the Valkey implementation behind the same interfaces with the key families of
// R-BAS-034 (prefix trove-bastion:<valkey.key_prefix>:), degrading to a local one while Valkey is unavailable
// (trove_valkey_available 0, R-ARCH-031), and owns the tests of both.
//
// This package imports nothing of the main installation. The Valkey implementation may use the Valkey client
// of internal/cluster; that package is on the allow-list of the archtest rule for it (R-BAS-001, Q-BAS-51).
package shared

import (
	"context"
	"sync"
	"time"
)

// Rate is a token bucket: a sustained rate per second and a burst (R-BAS-035).
type Rate struct {
	PerSecond float64
	Burst     int
}

// Limiter holds the limiters of R-ARCH-034 under the names bastion.<name> (R-BAS-034): token buckets for the
// per-address request limits and sliding-window counters for the misses, the failed authentications and the
// refusals that lead to a block. The key is the client address (an IPv6 address counted as its /64, R-BAS-019).
type Limiter interface {
	// Allow takes one token of the bucket (name, key). It returns whether the request is admitted and, when
	// not, how long until a token is available.
	Allow(ctx context.Context, name, key string, rate Rate) (ok bool, retryAfter time.Duration, err error)
	// Add counts one event in the sliding window of (name, key) and returns the number of events inside it,
	// this one included.
	Add(ctx context.Context, name, key string, window time.Duration) (count int, err error)
	// Count returns the number of events inside the window without adding one.
	Count(ctx context.Context, name, key string, window time.Duration) (count int, err error)
	// Replicas is the divisor the caller applies to a configured rate or threshold: bastion.replicas without
	// Valkey, 1 with it (the counters are shared, scope cluster, Q-BAS-34).
	Replicas() int
}

// Block is an address on the block list.
type Block struct {
	Until time.Time
	// Level counts the blocks within 24 h, which doubles the duration up to the maximum (R-BAS-037).
	Level int
}

// BlockList is the list of blocked addresses (R-BAS-037). With Valkey a block holds on every replica at its
// next request.
type BlockList interface {
	// Blocked returns the block of an address, if it is blocked now.
	Blocked(ctx context.Context, addr string) (Block, bool, error)
	// Set blocks an address for the duration; level is the new level.
	Set(ctx context.Context, addr string, d time.Duration, level int) error
	// Level returns the level of the address's most recent block within 24 h, 0 for none.
	Level(ctx context.Context, addr string) (int, error)
	// Size is the number of addresses blocked now (trove_bastion_blocked_addresses).
	Size(ctx context.Context) (int, error)
}

// FlightLock collapses concurrent fetches of one cache key across replicas (R-BAS-025, R-ARCH-033's form,
// key trove-bastion:<prefix>:flight:<sha256 of the cache key>, 30 s, extended by the holder). A lost lock or an
// unavailable Valkey only makes a replica fetch itself.
type FlightLock interface {
	// TryAcquire takes the lock; held is false when another holder has it.
	TryAcquire(ctx context.Context, keyHash string) (held bool, release func(), err error)
}

// SweepLease picks the one replica that sweeps a bucket (R-BAS-031, key trove-bastion:<prefix>:sweep, twice
// the sweep interval). Without Valkey every replica sweeps at a randomized interval, which is safe because a
// sweep only deletes.
type SweepLease interface {
	TryAcquire(ctx context.Context, ttl time.Duration) (held bool, release func(), err error)
}

// AnswerKind keys the families of signed answer records (R-BAS-034): ans:<kind>:<sha256 of the key>.
type AnswerKind string

// The kinds of stored answers.
const (
	AnswerCredential AnswerKind = "credential"
	AnswerShape      AnswerKind = "shape"
	AnswerURL        AnswerKind = "url"
	AnswerNegative   AnswerKind = "negative"
)

// AnswerStore keeps signed answer records (R-BAS-020, R-BAS-024, R-BAS-032): with Valkey, shared by the
// replicas; without, the replica's memory. The records are opaque to the store; the engine signs them
// (cache.Signer) and judges them (answers.State) whatever the store returns.
type AnswerStore interface {
	Get(ctx context.Context, kind AnswerKind, keyHash string) (record []byte, ok bool, err error)
	Put(ctx context.Context, kind AnswerKind, keyHash string, record []byte, ttl time.Duration) error
	Delete(ctx context.Context, kind AnswerKind, keyHash string) error
}

// State bundles the shared state the Bastion's packages use.
type State struct {
	Limiter Limiter
	Blocks  BlockList
	Flights FlightLock
	Sweep   SweepLease
	Answers AnswerStore
	// Shared reports whether the state is shared across replicas (Valkey configured and available).
	Shared func() bool
}

// NewLocal returns the state without Valkey, for a Bastion of the given replica count (bastion.replicas).
// The clock is injectable for tests; nil means time.Now.
func NewLocal(replicas int, now func() time.Time) *State {
	if replicas < 1 {
		replicas = 1
	}
	if now == nil {
		now = time.Now
	}
	l := &local{replicas: replicas, now: now}
	return &State{Limiter: l, Blocks: l, Flights: flights{l}, Sweep: sweepLease{}, Answers: l, Shared: func() bool { return false }}
}

// local is the in-memory implementation of every interface.
type local struct {
	replicas int
	now      func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	windows map[string][]time.Time
	blocks  map[string]Block
	levels  map[string]levelAt
	flights map[string]struct{}
	answers map[string]stored
}

type bucket struct {
	tokens float64
	last   time.Time
}

type levelAt struct {
	level int
	at    time.Time
}

type stored struct {
	record  []byte
	expires time.Time
}

func (l *local) lazy() {
	if l.buckets == nil {
		l.buckets, l.windows, l.blocks, l.levels = map[string]*bucket{}, map[string][]time.Time{}, map[string]Block{}, map[string]levelAt{}
		l.flights, l.answers = map[string]struct{}{}, map[string]stored{}
	}
}

func (l *local) Replicas() int { return l.replicas }

// Allow implements [Limiter]. The configured rate is divided by the replica count (Q-BAS-34); the burst too,
// at least one token.
func (l *local) Allow(_ context.Context, name, key string, rate Rate) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	perSecond := rate.PerSecond / float64(l.replicas)
	burst := float64(max(1, rate.Burst/l.replicas))
	k, now := name+"\x00"+key, l.now()
	b, ok := l.buckets[k]
	if !ok {
		b = &bucket{tokens: burst, last: now}
		l.buckets[k] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*perSecond)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0, nil
	}
	if perSecond <= 0 {
		return false, time.Hour, nil
	}
	return false, time.Duration((1 - b.tokens) / perSecond * float64(time.Second)), nil
}

func (l *local) trim(k string, window time.Duration, now time.Time) []time.Time {
	ts := l.windows[k]
	i := 0
	for i < len(ts) && !ts[i].After(now.Add(-window)) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.windows, k)
		return nil
	}
	l.windows[k] = ts
	return ts
}

// Add implements [Limiter].
func (l *local) Add(_ context.Context, name, key string, window time.Duration) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	k, now := name+"\x00"+key, l.now()
	l.trim(k, window, now)
	l.windows[k] = append(l.windows[k], now)
	return len(l.windows[k]), nil
}

// Count implements [Limiter].
func (l *local) Count(_ context.Context, name, key string, window time.Duration) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	return len(l.trim(name+"\x00"+key, window, l.now())), nil
}

// Blocked implements [BlockList].
func (l *local) Blocked(_ context.Context, addr string) (Block, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	b, ok := l.blocks[addr]
	if ok && !b.Until.After(l.now()) {
		delete(l.blocks, addr)
		return Block{}, false, nil
	}
	return b, ok, nil
}

// Set implements [BlockList].
func (l *local) Set(_ context.Context, addr string, d time.Duration, level int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	now := l.now()
	l.blocks[addr] = Block{Until: now.Add(d), Level: level}
	l.levels[addr] = levelAt{level: level, at: now}
	return nil
}

// Level implements [BlockList].
func (l *local) Level(_ context.Context, addr string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	lv, ok := l.levels[addr]
	if !ok || l.now().Sub(lv.at) > 24*time.Hour {
		return 0, nil
	}
	return lv.level, nil
}

// Size implements [BlockList].
func (l *local) Size(_ context.Context) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	now, n := l.now(), 0
	for a, b := range l.blocks {
		if b.Until.After(now) {
			n++
		} else {
			delete(l.blocks, a)
		}
	}
	return n, nil
}

// flights and sweepLease are the two TryAcquire of the local state; they differ in their arguments, so they
// are types of their own.
type flights struct{ l *local }

type sweepLease struct{}

func (f flights) TryAcquire(_ context.Context, keyHash string) (bool, func(), error) {
	f.l.mu.Lock()
	defer f.l.mu.Unlock()
	f.l.lazy()
	if _, held := f.l.flights[keyHash]; held {
		return false, func() {}, nil
	}
	f.l.flights[keyHash] = struct{}{}
	return true, func() {
		f.l.mu.Lock()
		delete(f.l.flights, keyHash)
		f.l.mu.Unlock()
	}, nil
}

// TryAcquire of the sweep lease: without Valkey every replica sweeps (R-BAS-031), so the lease is always held;
// the ttl is the Valkey implementation's.
func (s sweepLease) TryAcquire(context.Context, time.Duration) (bool, func(), error) {
	return true, func() {}, nil
}

// Get implements [AnswerStore].
func (l *local) Get(_ context.Context, kind AnswerKind, keyHash string) ([]byte, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	k := string(kind) + "\x00" + keyHash
	s, ok := l.answers[k]
	if ok && !s.expires.After(l.now()) {
		delete(l.answers, k)
		return nil, false, nil
	}
	return s.record, ok, nil
}

// Put implements [AnswerStore].
func (l *local) Put(_ context.Context, kind AnswerKind, keyHash string, record []byte, ttl time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	l.answers[string(kind)+"\x00"+keyHash] = stored{record: append([]byte(nil), record...), expires: l.now().Add(ttl)}
	return nil
}

// Delete implements [AnswerStore].
func (l *local) Delete(_ context.Context, kind AnswerKind, keyHash string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lazy()
	delete(l.answers, string(kind)+"\x00"+keyHash)
	return nil
}
