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

// maxLocalKeys bounds each map of the local forms. When it is reached, entries that no longer count are
// dropped first and, if that frees nothing, an arbitrary one; a dropped bucket starts full and a dropped counter
// at zero, which admits at most one extra budget of its address.
const maxLocalKeys = 100_000

// The shortest and the longest window of a counter, the range of the limiters of R-ARCH-034.
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

	mu      sync.Mutex
	buckets map[string]*bucket
	windows map[string]*counter
}

func newLocal(replicas int, now func() time.Time) *local {
	return &local{replicas: replicas, now: now, buckets: map[string]*bucket{}, windows: map[string]*counter{}}
}

type bucket struct {
	tokens float64
	last   time.Time
}

// counter is the sliding-window counter of the limiters of R-ARCH-034: the current and the previous window.
type counter struct {
	idx       int64
	cur, prev int
	window    time.Duration
}

func (l *local) Replicas() int { return l.replicas }

// Allow implements [Limiter]. The configured rate is divided by the replica count (Q-BAS-34); the burst too,
// at least one token.
func (l *local) Allow(_ context.Context, name, key string, rate Rate) (bool, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	perSecond := rate.PerSecond / float64(l.replicas)
	burst := float64(max(1, rate.Burst/l.replicas))
	k, now := name+"\x00"+key, l.now()
	b, ok := l.buckets[k]
	if !ok {
		if len(l.buckets) >= maxLocalKeys {
			l.dropBuckets(now)
		}
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

// dropBuckets drops the buckets that are full again, then an arbitrary one. The caller holds l.mu.
func (l *local) dropBuckets(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) > time.Hour {
			delete(l.buckets, k)
		}
	}
	for k := range l.buckets {
		if len(l.buckets) < maxLocalKeys {
			return
		}
		delete(l.buckets, k)
	}
}

// position returns the index of the window that now is in and how far into it, 0 to 1.
func position(now time.Time, window time.Duration) (idx int64, elapsed float64) {
	w, ms := window.Milliseconds(), now.UnixMilli()
	return ms / w, float64(ms%w) / float64(w)
}

// estimate rolls the counter to window idx and returns previous x (1 - elapsed) + current, rounded up. The
// caller holds l.mu.
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

func (l *local) count(name, key string, window time.Duration, add bool) int {
	window = ClampWindow(window)
	l.mu.Lock()
	defer l.mu.Unlock()
	k, now := name+"\x00"+key, l.now()
	idx, el := position(now, window)
	c, ok := l.windows[k]
	if !ok {
		if !add {
			return 0
		}
		if len(l.windows) >= maxLocalKeys {
			l.dropWindows(now)
		}
		c = &counter{idx: idx, window: window}
		l.windows[k] = c
	}
	c.window = window
	if add {
		c.estimate(idx, el)
		c.cur++
	}
	return c.estimate(idx, el) * l.replicas
}

// dropWindows drops the counters that are two windows old, then an arbitrary one. The caller holds l.mu.
func (l *local) dropWindows(now time.Time) {
	for k, c := range l.windows {
		if idx, _ := position(now, c.window); idx-c.idx >= 2 {
			delete(l.windows, k)
		}
	}
	for k := range l.windows {
		if len(l.windows) < maxLocalKeys {
			return
		}
		delete(l.windows, k)
	}
}

// Add implements [Limiter].
func (l *local) Add(_ context.Context, name, key string, window time.Duration) (int, error) {
	return l.count(name, key, window, true), nil
}

// Count implements [Limiter].
func (l *local) Count(_ context.Context, name, key string, window time.Duration) (int, error) {
	return l.count(name, key, window, false), nil
}

// memBlocks is a [BlockList] in memory: the whole list without Valkey, the local half with it.
type memBlocks struct {
	now func() time.Time

	mu     sync.Mutex
	blocks map[string]Block
	levels map[string]levelAt
}

type levelAt struct {
	level int
	at    time.Time
}

func newMemBlocks(now func() time.Time) *memBlocks {
	return &memBlocks{now: now, blocks: map[string]Block{}, levels: map[string]levelAt{}}
}

// Blocked implements [BlockList].
func (m *memBlocks) Blocked(_ context.Context, addr string) (Block, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blocks[addr]
	if ok && !b.Until.After(m.now()) {
		delete(m.blocks, addr)
		return Block{}, false, nil
	}
	return b, ok, nil
}

// Set implements [BlockList].
func (m *memBlocks) Set(_ context.Context, addr string, d time.Duration, level int) error {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.put(addr, Block{Until: now.Add(d), Level: level})
	m.setLevel(addr, level, now)
	return nil
}

// put stores a block without touching the level; the caller holds m.mu.
func (m *memBlocks) put(addr string, b Block) {
	if _, ok := m.blocks[addr]; !ok && len(m.blocks) >= maxLocalKeys {
		m.dropExpired()
	}
	m.blocks[addr] = b
}

func (m *memBlocks) setLevel(addr string, level int, at time.Time) {
	if _, ok := m.levels[addr]; !ok && len(m.levels) >= maxLocalKeys {
		for a, lv := range m.levels {
			if at.Sub(lv.at) > MaxLevelAge {
				delete(m.levels, a)
			}
		}
		for a := range m.levels {
			if len(m.levels) < maxLocalKeys {
				break
			}
			delete(m.levels, a)
		}
	}
	m.levels[addr] = levelAt{level: level, at: at}
}

// dropExpired removes the blocks that ended, then an arbitrary one when the map is still full; the caller
// holds m.mu.
func (m *memBlocks) dropExpired() {
	now := m.now()
	for a, b := range m.blocks {
		if !b.Until.After(now) {
			delete(m.blocks, a)
		}
	}
	for a := range m.blocks {
		if len(m.blocks) < maxLocalKeys {
			return
		}
		delete(m.blocks, a)
	}
}

// MaxLevelAge is how long the level of a block is remembered: a block within it doubles the duration
// (R-BAS-037).
const MaxLevelAge = 24 * time.Hour

// Level implements [BlockList].
func (m *memBlocks) Level(_ context.Context, addr string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lv, ok := m.levels[addr]
	if !ok || m.now().Sub(lv.at) > MaxLevelAge {
		return 0, nil
	}
	return lv.level, nil
}

// Size implements [BlockList].
func (m *memBlocks) Size(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now, n := m.now(), 0
	for a, b := range m.blocks {
		if b.Until.After(now) {
			n++
		} else {
			delete(m.blocks, a)
		}
	}
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

	mu      sync.Mutex
	answers map[string]stored
}

type stored struct {
	record  []byte
	expires time.Time
}

func newMemAnswers(now func() time.Time) *memAnswers {
	return &memAnswers{now: now, answers: map[string]stored{}}
}

// Get implements [AnswerStore].
func (m *memAnswers) Get(_ context.Context, kind AnswerKind, keyHash string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := string(kind) + "\x00" + keyHash
	s, ok := m.answers[k]
	if ok && !s.expires.After(m.now()) {
		delete(m.answers, k)
		return nil, false, nil
	}
	return s.record, ok, nil
}

// Put implements [AnswerStore].
func (m *memAnswers) Put(_ context.Context, kind AnswerKind, keyHash string, record []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := string(kind) + "\x00" + keyHash
	if _, ok := m.answers[k]; !ok && len(m.answers) >= maxLocalKeys {
		now := m.now()
		for a, s := range m.answers {
			if !s.expires.After(now) {
				delete(m.answers, a)
			}
		}
		for a := range m.answers {
			if len(m.answers) < maxLocalKeys {
				break
			}
			delete(m.answers, a)
		}
	}
	m.answers[k] = stored{record: append([]byte(nil), record...), expires: m.now().Add(ttl)}
	return nil
}

// Delete implements [AnswerStore].
func (m *memAnswers) Delete(_ context.Context, kind AnswerKind, keyHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.answers, string(kind)+"\x00"+keyHash)
	return nil
}
