package engine

import (
	"container/list"
	"sync"
)

// lruCache is a least-recently-used map bounded by an entry count, a total cost, or both; a bound of 0 is
// no bound. It is the one eviction structure of the engine: the answers (R-BAS-020), the negative entries
// (R-BAS-024), the private document copies (R-BAS-023, bounded by bytes) and the small registries.
type lruCache[V any] struct {
	mu       sync.Mutex
	maxItems int
	maxCost  int64
	cost     int64
	ll       *list.List
	items    map[string]*list.Element
	onRemove func(key string, v V)
}

type lruEntry[V any] struct {
	key  string
	val  V
	cost int64
}

func newLRU[V any](maxItems int, maxCost int64, onRemove func(key string, v V)) *lruCache[V] {
	return &lruCache[V]{maxItems: maxItems, maxCost: maxCost, ll: list.New(), items: map[string]*list.Element{}, onRemove: onRemove}
}

// get returns the value and marks it as recently used.
func (c *lruCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*lruEntry[V]).val, true
	}
	var zero V
	return zero, false
}

// put stores a value and evicts from the cold end until the bounds hold. It reports false when the value
// alone exceeds the cost bound; nothing is stored then.
func (c *lruCache[V]) put(key string, v V, cost int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxCost > 0 && cost > c.maxCost {
		c.removeLocked(key)
		return false
	}
	if el, ok := c.items[key]; ok {
		e := el.Value.(*lruEntry[V])
		c.cost += cost - e.cost
		old := e.val
		e.val, e.cost = v, cost
		c.ll.MoveToFront(el)
		if c.onRemove != nil {
			c.onRemove(key, old)
		}
		c.shrink()
		return true
	}
	c.items[key] = c.ll.PushFront(&lruEntry[V]{key: key, val: v, cost: cost})
	c.cost += cost
	c.shrink()
	return true
}

func (c *lruCache[V]) shrink() {
	for c.ll.Len() > 1 && ((c.maxItems > 0 && c.ll.Len() > c.maxItems) || (c.maxCost > 0 && c.cost > c.maxCost)) {
		last := c.ll.Back()
		c.removeLocked(last.Value.(*lruEntry[V]).key)
	}
}

func (c *lruCache[V]) removeLocked(key string) {
	el, ok := c.items[key]
	if !ok {
		return
	}
	e := el.Value.(*lruEntry[V])
	c.ll.Remove(el)
	delete(c.items, key)
	c.cost -= e.cost
	if c.onRemove != nil {
		c.onRemove(key, e.val)
	}
}

// remove deletes a key.
func (c *lruCache[V]) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
}

// len is the number of entries.
func (c *lruCache[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// retain removes every entry for which keep is false.
func (c *lruCache[V]) retain(keep func(key string, v V) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, el := range c.items {
		if !keep(key, el.Value.(*lruEntry[V]).val) {
			c.removeLocked(key)
		}
	}
}
