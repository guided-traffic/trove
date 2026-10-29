package app

import "context"

// Component is one started part of a replica. Start brings it up and returns
// once it runs; it must keep retrying transient failures itself until ctx ends,
// because a replica that cannot reach PostgreSQL stays alive and unready
// (R-ARCH-024 step 3). Start returns an error only for a failure that retrying
// cannot fix, such as a configuration error, and the process then exits. Stop is
// the last act of the component, called in reverse start order.
//
// A component registers its readiness checks and watched loops on the health
// registry it receives from the composition root.
type Component interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// The optional phases of the shutdown sequence (R-ARCH-024). The composition root
// calls each in the order below on every component that implements it.

// Drainer is told at SIGTERM, before anything else (step 1): a worker stops
// claiming jobs and releases the scheduler lease, the registry row turns
// draining. Drain returns at once; it does not wait for running work.
type Drainer interface{ Drain(ctx context.Context) }

// Settler waits for running work to finish (step 3). ctx ends at the shutdown
// deadline. A worker waits for its running jobs here.
type Settler interface {
	Settle(ctx context.Context) error
}

// Quiescer cancels work that is still running 5 s before the deadline (step 4):
// job handlers are cancelled and their jobs released without counting the
// attempt, remote fetches are cancelled and their flight locks released.
type Quiescer interface{ Quiesce(ctx context.Context) }

// Flusher writes the write-only buffers after the last request (step 6):
// statistics, activity, audit. It runs before the pools close.
type Flusher interface {
	Flush(ctx context.Context) error
}
