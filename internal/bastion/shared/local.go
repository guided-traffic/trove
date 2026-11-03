package shared

import (
	"context"
	"math"
	"sync"
	"time"
)

// The local forms: every interface over the memory of one replica. They are the whole state without Valkey
// and the fallback with it. Every map is bounded, because the keys are chosen by whoever sends requests: an
// address that sends one request creates an entry (R-BAS-035, the fallback of R-ARCH-034 has the same bound).
// The maps are sharded and evict the least recently used entry in constant time (Q-BAS-128); no request takes
// a lock that every other request takes.

// maxLocalKeys bounds each map of the local forms. A key that arrives with the map full evicts the least
// recently used one; an evicted bucket starts full and an evicted counter at zero, which admits at most one
// extra budget of its address.
const maxLocalKeys = 100_000

// The shortest and the longest window of a counter, the range of the limiters of R-ARCH-034. A configured window
// outside it is refused at the configuration (bastion.failed_auth.window, Q-BAS-128); ClampWindow only holds a
// value that did not come from the configuration, the tests of the other windows for one.
const (
	MinWindow = time.Second
	MaxWindow = 12 * time.Hour
)

// ClampWindow holds a window to the range the limiters support.
func ClampWindow(w time.Duration) time.Duration {
	return min(max(w, MinWindow), MaxWindow).Truncate(time.Millisecond)
}

// local is the limiter in memory: token buckets for Allow, sliding-window counters for Add and Count.
type local struct {
	replicas int
	now      func() time.Time

	buckets *shardMap[bucket]
	windows *shardMap[counter]
}

func newLocal(replicas int, now func() time.Time) *local {
	return &local{replicas: replicas, now: now, buckets: newShardMap[bucket](maxLocalKeys), windows: newShardMap[counter](maxLocalKeys)}
}

type bucket struct {
	tokens float64
	last   time.Time
}

// counter is the sliding-window counter of the limiters of R-ARCH-034: the current and the previous window.
type counter struct {
	idx       int64
	cur, prev int
}

func (l *local) Replicas() int { return l.replicas }

// Allow implements [Limiter]. The configured rate is divided by the replica count (Q-BAS-34); the burst too,
// at least one token. A refused request takes nothing: a client slightly over its rate is admitted at its rate
// (Q-BAS-127).
func (l *local) Allow(_ context.Context, name, key string, rate Rate) (bool, time.Duration, error) {
	perSecond := rate.PerSecond / float64(l.replicas)
	burst := float64(max(1, rate.Burst/l.replicas))
	k, now := name+"\x00"+key, l.now()
	sh := l.buckets.lock(k)
	defer sh.mu.Unlock()
	b, ok := sh.get(k)
	if !ok {
		b = sh.put(k, bucket{tokens: burst, last: now})
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

// position returns the index of the window that now is in and how far into it, 0 to 1.
func position(now time.Time, window time.Duration) (idx int64, elapsed float64) {
	w, ms := window.Milliseconds(), now.UnixMilli()
	return ms / w, float64(ms%w) / float64(w)
}

// estimate rolls the counter to window idx and returns previous x (1 - elapsed) + current, rounded up.
func (c *counter) estimate(idx int64, elapsed float64) int {
	switch c.idx {
	case idx:
	case idx - 1:
		c.prev, c.cur, c.idx = c.cur, 0, idx
	default:
		c.prev, c.cur, c.idx = 0, 0, idx
	}
	return int(math.Ceil(float64(c.prev)*(1-elapsed) + float64(c.cur)))
}

// noLimit is the limit of an unconditional count.
const noLimit = -1

// count counts an event (add) or reads (not add) and returns the estimate in the units of the configured value.
// With limit >= 0 the event is kept only if the estimate with it stays within the limit: a refused event takes
// nothing (Q-BAS-127); admitted says which.
func (l *local) count(name, key string, window time.Duration, add bool, limit int) (n int, admitted bool) {
	window = ClampWindow(window)
	k, now := name+"\x00"+key, l.now()
	idx, el := position(now, window)
	sh := l.windows.lock(k)
	defer sh.mu.Unlock()
	c, ok := sh.get(k)
	if !ok {
		if !add {
			return 0, true
		}
		c = sh.put(k, counter{idx: idx})
	}
	if !add {
		return c.estimate(idx, el) * l.replicas, true
	}
	c.estimate(idx, el)
	c.cur++
	n = c.estimate(idx, el) * l.replicas
	if limit != noLimit && n > limit {
		c.cur--
		return n, false
	}
	return n, true
}

// Add implements [Limiter].
func (l *local) Add(_ context.Context, name, key string, window time.Duration) (int, error) {
	n, _ := l.count(name, key, window, true, noLimit)
	return n, nil
}

// AddIfWithin implements [Limiter].
func (l *local) AddIfWithin(_ context.Context, name, key string, window time.Duration, limit int) (bool, int, error) {
	n, ok := l.count(name, key, window, true, max(limit, 0))
	return ok, n, nil
}

// Count implements [Limiter].
func (l *local) Count(_ context.Context, name, key string, window time.Duration) (int, error) {
	n, _ := l.count(name, key, window, false, noLimit)
	return n, nil
}

// memBlocks is a [BlockList] in memory: the whole list without Valkey, the local half with it.
type memBlocks struct {
	now func() time.Time

	blocks *shardMap[Block]
	levels *shardMap[levelAt]
}

type levelAt struct {
	level int
	at    time.Time
}

func newMemBlocks(now func() time.Time) *memBlocks {
	return &memBlocks{now: now, blocks: newShardMap[Block](maxLocalKeys), levels: newShardMap[levelAt](maxLocalKeys)}
}

// Blocked implements [BlockList].
func (m *memBlocks) Blocked(_ context.Context, addr string) (Block, bool, error) {
	sh := m.blocks.lock(addr)
	defer sh.mu.Unlock()
	b, ok := sh.get(addr)
	if !ok {
		return Block{}, false, nil
	}
	if !b.Until.After(m.now()) {
		sh.remove(addr)
		return Block{}, false, nil
	}
	return *b, true, nil
}

// Set implements [BlockList].
func (m *memBlocks) Set(_ context.Context, addr string, d time.Duration, level int) error {
	now := m.now()
	m.remember(addr, Block{Until: now.Add(d), Level: level})
	sh := m.levels.lock(addr)
	sh.put(addr, levelAt{level: level, at: now})
	sh.mu.Unlock()
	return nil
}

// remember stores a block without touching the level.
func (m *memBlocks) remember(addr string, b Block) {
	sh := m.blocks.lock(addr)
	sh.put(addr, b)
	sh.mu.Unlock()
}

// MaxLevelAge is how long the level of a block is remembered: a block within it doubles the duration
// (R-BAS-037).
const MaxLevelAge = 24 * time.Hour

// Level implements [BlockList].
func (m *memBlocks) Level(_ context.Context, addr string) (int, error) {
	sh := m.levels.lock(addr)
	defer sh.mu.Unlock()
	lv, ok := sh.get(addr)
	if !ok || m.now().Sub(lv.at) > MaxLevelAge {
		return 0, nil
	}
	return lv.level, nil
}

// Size implements [BlockList]. It visits every block, one shard at a time, and drops those that ended.
func (m *memBlocks) Size(_ context.Context) (int, error) {
	now, n := m.now(), 0
	m.blocks.sweep(func(_ string, b *Block) bool {
		if b.Until.After(now) {
			n++
			return true
		}
		return false
	})
	return n, nil
}

// localFlights is the per-replica form of the fetch lock.
type localFlights struct {
	mu   sync.Mutex
	held map[string]struct{}
}

func newLocalFlights() *localFlights { return &localFlights{held: map[string]struct{}{}} }

// TryAcquire implements [FlightLock].
func (f *localFlights) TryAcquire(_ context.Context, keyHash string) (bool, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, held := f.held[keyHash]; held {
		return false, func() {}, nil
	}
	f.held[keyHash] = struct{}{}
	return true, func() {
		f.mu.Lock()
		delete(f.held, keyHash)
		f.mu.Unlock()
	}, nil
}

// localSweep is the lease without Valkey: every replica sweeps (R-BAS-031), so it is always held.
type localSweep struct{}

// TryAcquire implements [SweepLease].
func (localSweep) TryAcquire(context.Context, time.Duration) (bool, func(), error) {
	return true, func() {}, nil
}

// memAnswers is the [AnswerStore] in memory.
type memAnswers struct {
	now func() time.Time

	answers *shardMap[stored]
}

type stored struct {
	record  []byte
	expires time.Time
}

func newMemAnswers(now func() time.Time) *memAnswers {
	return &memAnswers{now: now, answers: newShardMap[stored](maxLocalKeys)}
}

func answerKey(kind AnswerKind, keyHash string) string { return string(kind) + "\x00" + keyHash }

// Get implements [AnswerStore].
func (m *memAnswers) Get(_ context.Context, kind AnswerKind, keyHash string) ([]byte, bool, error) {
	k := answerKey(kind, keyHash)
	sh := m.answers.lock(k)
	defer sh.mu.Unlock()
	s, ok := sh.get(k)
	if !ok {
		return nil, false, nil
	}
	if !s.expires.After(m.now()) {
		sh.remove(k)
		return nil, false, nil
	}
	return s.record, true, nil
}

// Put implements [AnswerStore].
func (m *memAnswers) Put(_ context.Context, kind AnswerKind, keyHash string, record []byte, ttl time.Duration) error {
	k := answerKey(kind, keyHash)
	sh := m.answers.lock(k)
	defer sh.mu.Unlock()
	sh.put(k, stored{record: append([]byte(nil), record...), expires: m.now().Add(ttl)})
	return nil
}

// Delete implements [AnswerStore].
func (m *memAnswers) Delete(_ context.Context, kind AnswerKind, keyHash string) error {
	k := answerKey(kind, keyHash)
	sh := m.answers.lock(k)
	defer sh.mu.Unlock()
	sh.remove(k)
	return nil
}
