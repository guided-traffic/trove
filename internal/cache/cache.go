package cache

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Options configure a cache. Exactly one of MaxEntries and MaxBytes is set.
type Options[K comparable, V any] struct {
	// MaxEntries bounds the number of entries.
	MaxEntries int
	// MaxBytes bounds the sum of Weigh over the entries; it needs Weigh.
	MaxBytes int64
	// Weigh returns the size of an entry in bytes.
	Weigh func(K, V) int64
	// SafetyTTL bounds the age of every entry. It is mandatory: it bounds the
	// staleness if an invalidation is ever missed (R-ARCH-018).
	SafetyTTL time.Duration
	// Areas names the change areas that invalidate this cache. Loads check their
	// applied epochs (the fill rule).
	Areas []string
	// OnChange handles one change of one of the areas: evict the affected
	// entries with Invalidate or Remove. Without it every change flushes the
	// cache. It runs for early evictions from Valkey as well as for the poll's
	// changes, so it must be idempotent.
	OnChange func(c *Cache[K, V], area string, version int, payload json.RawMessage)
	// OnFlush runs after the cache was emptied, with the reason.
	OnFlush func(reason string)
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Cache is a bounded LRU with a maximum age. Create it with [Register]. It is
// safe for concurrent use.
type Cache[K comparable, V any] struct {
	name string
	r    *Registry
	o    Options[K, V]

	mu    sync.Mutex
	items map[K]*list.Element
	lru   *list.List // front = most recently used
	bytes int64
	// gen counts invalidations. A fill that started before one does not insert.
	gen uint64
}

type entry[K comparable, V any] struct {
	key    K
	val    V
	weight int64
	at     time.Time
}

// Register creates a cache in the registry. It panics on a programming error: a
// bad or duplicate name, no SafetyTTL, no bound or two bounds.
func Register[K comparable, V any](r *Registry, name string, o Options[K, V]) *Cache[K, V] {
	switch {
	case o.SafetyTTL <= 0:
		panic(fmt.Sprintf("cache: %q needs a SafetyTTL (R-ARCH-018)", name))
	case (o.MaxEntries > 0) == (o.MaxBytes > 0):
		panic(fmt.Sprintf("cache: %q needs exactly one of MaxEntries and MaxBytes", name))
	case o.MaxBytes > 0 && o.Weigh == nil:
		panic(fmt.Sprintf("cache: %q has MaxBytes but no Weigh", name))
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	c := &Cache[K, V]{name: name, r: r, o: o, items: map[K]*list.Element{}, lru: list.New()}
	r.add(name, c)
	return c
}

// Name is the cache's registered name.
func (c *Cache[K, V]) Name() string { return c.name }

func (c *Cache[K, V]) count(result string) {
	c.r.metrics.requests.WithLabelValues(c.name, result).Inc()
}

func (c *Cache[K, V]) sizes() {
	c.r.metrics.entries.WithLabelValues(c.name).Set(float64(c.lru.Len()))
	c.r.metrics.bytes.WithLabelValues(c.name).Set(float64(c.bytes))
}

// Get returns the cached value. An entry older than SafetyTTL is dropped and
// counts as a miss.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		c.count("miss")
		var zero V
		return zero, false
	}
	e := el.Value.(*entry[K, V])
	if c.o.Now().Sub(e.at) >= c.o.SafetyTTL {
		c.removeLocked(el)
		c.sizes()
		c.count("expired")
		var zero V
		return zero, false
	}
	c.lru.MoveToFront(el)
	c.count("hit")
	return e.val, true
}

// Set stores a value unconditionally. Fills from PostgreSQL use [Cache.Load],
// which applies the fill rule; Set is for values that need none, such as a
// write-through of a value the caller just committed.
func (c *Cache[K, V]) Set(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(k, v)
}

func (c *Cache[K, V]) setLocked(k K, v V) {
	var w int64 = 1
	if c.o.Weigh != nil {
		w = c.o.Weigh(k, v)
	}
	if el, ok := c.items[k]; ok {
		c.removeLocked(el)
	}
	if c.o.MaxBytes > 0 && w > c.o.MaxBytes {
		c.sizes()
		return // larger than the whole cache
	}
	c.items[k] = c.lru.PushFront(&entry[K, V]{key: k, val: v, weight: w, at: c.o.Now()})
	c.bytes += w
	for (c.o.MaxEntries > 0 && c.lru.Len() > c.o.MaxEntries) || (c.o.MaxBytes > 0 && c.bytes > c.o.MaxBytes) {
		c.removeLocked(c.lru.Back())
	}
	c.sizes()
}

func (c *Cache[K, V]) removeLocked(el *list.Element) {
	e := el.Value.(*entry[K, V])
	delete(c.items, e.key)
	c.lru.Remove(el)
	c.bytes -= e.weight
}

// Remove drops one key. Unlike [Cache.Invalidate] it does not stop a fill that
// is running; use it for evictions that are not invalidations (a replaced value).
func (c *Cache[K, V]) Remove(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.removeLocked(el)
		c.sizes()
	}
}

// Invalidate drops one key and stops every fill that is running from inserting
// its result: the value such a fill read may predate the change.
func (c *Cache[K, V]) Invalidate(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if el, ok := c.items[k]; ok {
		c.removeLocked(el)
	}
	c.sizes()
}

// InvalidateFunc drops every key for which match returns true and stops running
// fills, like [Cache.Invalidate].
func (c *Cache[K, V]) InvalidateFunc(match func(K) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for el := c.lru.Front(); el != nil; {
		next := el.Next()
		if match(el.Value.(*entry[K, V]).key) {
			c.removeLocked(el)
		}
		el = next
	}
	c.sizes()
}

// Flush empties the cache and runs OnFlush.
func (c *Cache[K, V]) Flush(reason string) { c.flush(reason) }

// Len is the number of entries, expired ones included until they are touched.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// Load returns the cached value for k, or calls load and caches its result
// under the fill rule of R-ARCH-018: the applied epoch of every area of the
// cache is read before load runs, and the result is inserted only if those
// epochs and the cache's invalidation counter are unchanged afterwards.
// Otherwise the result serves this call only. An error from load is returned
// and nothing is cached.
func (c *Cache[K, V]) Load(ctx context.Context, k K, load func(context.Context) (V, error)) (V, error) {
	if v, ok := c.Get(k); ok {
		return v, nil
	}
	before := make([]int64, len(c.o.Areas))
	for i, a := range c.o.Areas {
		before[i] = c.r.applied(a)
	}
	c.mu.Lock()
	gen := c.gen
	c.mu.Unlock()

	v, err := load(ctx)
	if err != nil {
		return v, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		c.count("fill_skipped")
		return v, nil
	}
	for i, a := range c.o.Areas {
		if c.r.applied(a) != before[i] {
			c.count("fill_skipped")
			return v, nil
		}
	}
	c.setLocked(k, v)
	return v, nil
}

// Registry hooks.

func (c *Cache[K, V]) areas() []string { return slices.Clone(c.o.Areas) }

func (c *Cache[K, V]) onChange(area string, version int, payload json.RawMessage) {
	if c.o.OnChange == nil {
		c.flush(ReasonChange)
		return
	}
	c.o.OnChange(c, area, version, payload)
}

func (c *Cache[K, V]) flush(reason string) {
	c.mu.Lock()
	c.gen++
	c.items = map[K]*list.Element{}
	c.lru.Init()
	c.bytes = 0
	c.sizes()
	c.mu.Unlock()
	c.r.metrics.flushes.WithLabelValues(c.name, reason).Inc()
	if c.o.OnFlush != nil {
		c.o.OnFlush(reason)
	}
}

func (c *Cache[K, V]) stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Name: c.name, Entries: c.lru.Len(), Bytes: c.bytes}
}
