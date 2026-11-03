package engine

import (
	"context"
	"errors"
	"sync"
)

// call is one in-flight operation that concurrent callers of the same key share.
type call struct {
	done chan struct{}
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

// finish ends the leader's call and wakes the waiters.
func (g *group) finish(key string, c *call, val any, err error) {
	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	c.val, c.err = val, err
	close(c.done)
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
