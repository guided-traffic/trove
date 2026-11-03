package guard

import "sync"

// keyedLock is a mutex per key that exists only while somebody holds or waits for it: the table holds the
// addresses being decided now, so its size is bounded by the requests in flight, and its own mutex is held for
// map operations only, never across a call to the shared state.
type keyedLock struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

// lock takes the lock of key and returns the function that releases it.
func (k *keyedLock) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedEntry{}
	}
	e, ok := k.m[key]
	if !ok {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// held is the number of keys locked or waited for, for tests.
func (k *keyedLock) held() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}
