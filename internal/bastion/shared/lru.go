package shared

import (
	"container/list"
	"hash/maphash"
	"sync"
)

// shardMap is the bounded map of the local forms (Q-BAS-128): the keys are chosen by whoever sends requests,
// so a key that arrives with the map full evicts the least recently used one in constant time, and the map is
// cut into shards with a lock of their own, so that the requests of different addresses do not wait for one
// another and a shard's eviction touches one list end, never the whole map.
//
// The least recently used entry is the one that most likely no longer counts: a counter two windows old, a
// bucket that is full again, a block that ended. An entry that is evicted while it still counts starts again
// (a bucket full, a counter at zero), which admits at most one extra budget of its address.
type shardMap[V any] struct {
	seed   maphash.Seed
	shards [localShards]lruShard[V]
}

// localShards is the number of shards of a local map; a power of two.
const localShards = 64

type lruShard[V any] struct {
	mu  sync.Mutex
	cap int
	m   map[string]*list.Element
	ll  *list.List // front: most recently used
}

type lruItem[V any] struct {
	key string
	v   V
}

// newShardMap returns a map that holds at most capacity entries (rounded down to a multiple of the shards).
func newShardMap[V any](capacity int) *shardMap[V] {
	s := &shardMap[V]{seed: maphash.MakeSeed()}
	per := max(1, capacity/localShards)
	for i := range s.shards {
		s.shards[i].cap = per
		s.shards[i].m = map[string]*list.Element{}
		s.shards[i].ll = list.New()
	}
	return s
}

// lock locks and returns the shard of a key; the caller unlocks it.
func (s *shardMap[V]) lock(key string) *lruShard[V] {
	sh := &s.shards[maphash.String(s.seed, key)%localShards]
	sh.mu.Lock()
	return sh
}

// get returns the value of key and marks it as used; the caller holds the shard.
func (sh *lruShard[V]) get(key string) (*V, bool) {
	e, ok := sh.m[key]
	if !ok {
		return nil, false
	}
	sh.ll.MoveToFront(e)
	return &e.Value.(*lruItem[V]).v, true
}

// put stores a value (replacing one) as the most recently used and evicts the least recently used entry when
// the shard is over its capacity; the caller holds the shard.
func (sh *lruShard[V]) put(key string, v V) *V {
	if e, ok := sh.m[key]; ok {
		it := e.Value.(*lruItem[V])
		it.v = v
		sh.ll.MoveToFront(e)
		return &it.v
	}
	it := &lruItem[V]{key: key, v: v}
	sh.m[key] = sh.ll.PushFront(it)
	if sh.ll.Len() > sh.cap {
		oldest := sh.ll.Back()
		sh.ll.Remove(oldest)
		delete(sh.m, oldest.Value.(*lruItem[V]).key)
	}
	return &it.v
}

// remove deletes a key; the caller holds the shard.
func (sh *lruShard[V]) remove(key string) {
	if e, ok := sh.m[key]; ok {
		sh.ll.Remove(e)
		delete(sh.m, key)
	}
}

// total is the number of entries held.
func (s *shardMap[V]) total() int {
	n := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		n += sh.ll.Len()
		sh.mu.Unlock()
	}
	return n
}

// sweep calls keep for every entry, one shard at a time, and removes those it returns false for.
func (s *shardMap[V]) sweep(keep func(key string, v *V) bool) {
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for e := sh.ll.Front(); e != nil; {
			next := e.Next()
			it := e.Value.(*lruItem[V])
			if !keep(it.key, &it.v) {
				sh.ll.Remove(e)
				delete(sh.m, it.key)
			}
			e = next
		}
		sh.mu.Unlock()
	}
}
