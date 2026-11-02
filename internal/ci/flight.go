package ci

import "sync"

// flight collapses concurrent calls with one key into one execution inside the process (the
// single-flight of R-CI-003: the re-read of a key row and the on-demand fetch of one issuer are each
// run once for all the requests that ask at the same time). Callers that join receive the result of the
// call that runs. A call that finishes leaves nothing behind, so the next one runs again.
type flight[T any] struct {
	mu    sync.Mutex
	calls map[string]*flightCall[T]
}

type flightCall[T any] struct {
	done chan struct{}
	val  T
	err  error
}

// do runs fn once for the callers of key that overlap; shared reports whether this caller joined a call
// another caller ran.
func (f *flight[T]) do(key string, fn func() (T, error)) (val T, shared bool, err error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]*flightCall[T]{}
	}
	if c, ok := f.calls[key]; ok {
		f.mu.Unlock()
		<-c.done
		return c.val, true, c.err
	}
	c := &flightCall[T]{done: make(chan struct{})}
	f.calls[key] = c
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		delete(f.calls, key)
		f.mu.Unlock()
		close(c.done)
	}()
	c.val, c.err = fn()
	return c.val, false, c.err
}
