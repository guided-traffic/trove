package engine

import (
	"context"
	"sync/atomic"
)

type quietKey struct{}

// WithQuietSlot returns a context in which the engine can mark the request as one that is not logged one by
// one (guard.Verdict.Quiet, R-BAS-037): a blocked address answers many requests and the guard logs one line
// per block instead. The server puts the slot into the context before its request log, and the request log
// asks [Quiet] after the response.
func WithQuietSlot(ctx context.Context) context.Context {
	return context.WithValue(ctx, quietKey{}, new(atomic.Bool))
}

// Quiet reports whether the engine marked the request of ctx as quiet.
func Quiet(ctx context.Context) bool {
	b, _ := ctx.Value(quietKey{}).(*atomic.Bool)
	return b != nil && b.Load()
}

// markQuiet marks the request; without a slot in the context it does nothing.
func markQuiet(ctx context.Context) {
	if b, _ := ctx.Value(quietKey{}).(*atomic.Bool); b != nil {
		b.Store(true)
	}
}
