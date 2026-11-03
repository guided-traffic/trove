package cache

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
)

// The thresholds of R-BAS-031: a sweep acts above 95 % of a limit and deletes until 85 % remain.
const (
	sweepHighPercent   = 95
	sweepTargetPercent = 85
)

// sweepJitter is the spread of the randomized interval of a bucket without Valkey: sweep_interval +- 20 %.
const sweepJitter = 0.2

func percent(limit, p int64) int64 { return limit * p / 100 }

// SweepOnce is one cycle of R-BAS-031 for this replica: it measures what the backend holds, publishes
// trove_bastion_cache_bytes, and evicts by last access in each area above 95 % of its limit down to 85 %. With
// the bucket backend and Valkey only the replica that holds the sweep lease runs it; without Valkey every
// replica does, which is safe because a sweep only deletes. With the volume backend each replica sweeps its own
// volume. st may be nil, which is a replica without shared state.
func (c *Cache) SweepOnce(ctx context.Context, st *shared.State) error {
	if c.cfg.Backend == "bucket" && st != nil && st.Shared != nil && st.Shared() && st.Sweep != nil {
		held, release, err := st.Sweep.TryAcquire(ctx, 2*c.cfg.SweepInterval)
		if err != nil {
			c.log.Warn("the sweep lease could not be taken; sweeping without it, which is safe", "error", err)
		} else {
			if !held {
				return nil
			}
			defer release()
		}
	}
	u, err := c.back.Usage(ctx)
	if err != nil {
		return err
	}
	c.publish(u)
	arts, docs := c.limits()
	var errs []error
	for _, a := range []struct {
		area  string
		limit int64
		used  int64
	}{{metrics.AreaArtifacts, arts, u.Artifacts}, {metrics.AreaDocuments, docs, u.Documents}} {
		if a.used <= percent(a.limit, sweepHighPercent) {
			continue
		}
		n, err := c.back.Sweep(ctx, a.area, percent(a.limit, sweepHighPercent), percent(a.limit, sweepTargetPercent))
		c.m.CacheEvictions.WithLabelValues(metrics.EvictSize).Add(float64(n))
		if err != nil {
			errs = append(errs, err)
		}
		if n > 0 {
			c.log.Info("cache sweep", "area", a.area, "evicted", n, "limit", a.limit)
		}
	}
	if u2, err := c.back.Usage(ctx); err == nil {
		c.publish(u2)
	}
	return errors.Join(errs...)
}

func (c *Cache) publish(u Usage) {
	c.m.CacheBytes.WithLabelValues(metrics.AreaArtifacts).Set(float64(u.Artifacts))
	c.m.CacheBytes.WithLabelValues(metrics.AreaDocuments).Set(float64(u.Documents))
}

// interval is the time to the next sweep: sweep_interval, randomized around it for a bucket (R-BAS-031).
func (c *Cache) interval() time.Duration {
	d := c.cfg.SweepInterval
	if c.cfg.Backend != "bucket" {
		return d
	}
	return time.Duration(float64(d) * (1 - sweepJitter + 2*sweepJitter*rand.Float64())) //nolint:gosec // jitter, not security
}

// RunSweeper sweeps every sweep_interval until ctx ends. A failed cycle is logged and counted by the backend; the
// next one runs on schedule.
func (c *Cache) RunSweeper(ctx context.Context, st *shared.State) {
	if u, err := c.back.Usage(ctx); err == nil {
		c.publish(u)
	}
	t := time.NewTimer(c.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := c.SweepOnce(ctx, st); err != nil && ctx.Err() == nil {
			c.log.Warn("cache sweep failed", "error", err)
		}
		t.Reset(c.interval())
	}
}
