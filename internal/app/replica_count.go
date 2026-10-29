package app

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// replicaCount is the number n of live replicas that serve the client endpoints of
// this replica's pool: the web replicas of the same pool and every replica of the
// role all. Every limiter of the client listener divides its per-replica share by
// it while Valkey is unavailable (R-ARCH-034, Q-ARCH-45): the failure throttle and
// the flow starts of the identity area, the request limits of the API and the
// rate-limit classes of the endpoints. It is one component that counts once per
// heartbeat interval, so the families cannot disagree about n, and it stops with
// the other components at shutdown. A failed read keeps the last value, because the
// fallback must not depend on PostgreSQL either; n is 1 until the first read.
type replicaCount struct {
	a *App
	n atomic.Int32

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var _ Component = (*replicaCount)(nil)

// Name implements [Component].
func (c *replicaCount) Name() string { return "replica-count" }

// Get returns the count, at least 1.
func (c *replicaCount) Get() int { return max(1, int(c.n.Load())) }

// Start reads the count once and starts the refresh loop. The loop runs until Stop,
// not until the bootstrap context ends: the limiters need the count until the
// last request has been answered.
func (c *replicaCount) Start(ctx context.Context) error {
	db := c.a.database()
	if db == nil {
		return nil
	}
	stale := c.a.cfg.System.HeartbeatStaleAfter.Seconds()
	refresh := func(ctx context.Context) {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_ = db.Read(rctx, func(d q.DBTX) error {
			n, err := q.New(d).EndpointsCountServingReplicas(rctx, q.EndpointsCountServingReplicasParams{StaleSecs: stale, Pool: c.a.id.Pool})
			if err == nil {
				c.n.Store(n)
			}
			return err
		})
	}
	refresh(ctx)
	lctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.cancel = cancel
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(c.a.cfg.System.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				refresh(lctx)
			}
		}
	}()
	return nil
}

// Stop implements [Component].
func (c *replicaCount) Stop(context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return nil
}

// servingReplicas returns the replica count of the pool, started as a component on
// the first call.
func (a *App) servingReplicas(ctx context.Context) (*replicaCount, error) {
	a.mu.Lock()
	c := a.m1.replicas
	a.mu.Unlock()
	if c != nil {
		return c, nil
	}
	c = &replicaCount{a: a}
	if err := a.startComponent(ctx, c); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.m1.replicas = c
	a.mu.Unlock()
	return c, nil
}
