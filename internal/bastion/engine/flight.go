package engine

import (
	"context"
	"errors"
	"sync"
)

// call is one in-flight operation that concurrent callers of the same key share.
type call struct {
	done chan struct{}
	once sync.Once
	val  any
	err  error
}

// group is a single-flight per key (R-BAS-025): the first caller of a key is the leader and runs the
// operation, the others wait for it. It is local to the replica; the cross-replica form is the fetch lock of
// shared.FlightLock.
type group struct {
	mu    sync.Mutex
	calls map[string]*call
}

func newGroup() *group { return &group{calls: map[string]*call{}} }

// join returns the call of a key and whether the caller is its leader.
func (g *group) join(key string) (*call, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.calls[key]; ok {
		return c, false
	}
	c := &call{done: make(chan struct{})}
	g.calls[key] = c
	return c, true
}

// running returns the call of a key that has a leader, or nil.
func (g *group) running(key string) *call {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[key]
}

// finish ends the leader's call and wakes the waiters. A call ends once: a second finish changes nothing, and
// never removes a later call of the same key.
func (g *group) finish(key string, c *call, val any, err error) {
	c.once.Do(func() {
		g.mu.Lock()
		if g.calls[key] == c {
			delete(g.calls, key)
		}
		g.mu.Unlock()
		c.val, c.err = val, err
		close(c.done)
	})
}

// wait blocks until the leader finished or ctx ended.
func (c *call) wait(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handle is what a request that leads a fetch holds: the end of the flight, which wakes the waiters and frees the
// fetch lock of the other replicas. The request ends it when it returns; a fill that outlives the request takes
// it over ([handle.take]) and ends it at the commit, so the waiters never wait for a client's speed (Q-BAS-123).
type handle struct {
	mu    sync.Mutex
	end   func()
	taken bool
}

func newHandle(end func()) *handle {
	var once sync.Once
	return &handle{end: func() { once.Do(end) }}
}

// done ends the flight unless it was taken over.
func (h *handle) done() {
	if h == nil {
		return
	}
	h.mu.Lock()
	taken := h.taken
	h.mu.Unlock()
	if !taken {
		h.end()
	}
}

// take gives the end of the flight to the caller, who must call the returned function once the result of the
// fetch is in the cache or lost. Taking from a nil handle (a request that leads no flight) gives a no-op.
func (h *handle) take() func() {
	if h == nil {
		return func() {}
	}
	h.mu.Lock()
	h.taken = true
	h.mu.Unlock()
	return h.end
}

// do runs fn once per key at a time and hands its result to every concurrent caller. A waiter whose leader
// was cancelled by the leader's own client runs the operation itself.
func (g *group) do(ctx context.Context, key string, fn func(context.Context) (any, error)) (any, error) {
	for {
		c, leader := g.join(key)
		if leader {
			return g.lead(ctx, key, c, fn)
		}
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		if errors.Is(c.err, context.Canceled) && ctx.Err() == nil {
			continue
		}
		return c.val, c.err
	}
}

func (g *group) lead(ctx context.Context, key string, c *call, fn func(context.Context) (any, error)) (any, error) {
	defer func() {
		if r := recover(); r != nil {
			g.finish(key, c, nil, errors.New("engine: the shared operation panicked"))
			panic(r)
		}
	}()
	v, err := fn(ctx)
	g.finish(key, c, v, err)
	return v, err
}
