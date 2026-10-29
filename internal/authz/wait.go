package authz

import (
	"context"
	"time"
)

// waitTick is how often a waiting request looks at the installed index again.
const waitTick = 10 * time.Millisecond

// forRequest returns the index a request decides on, or the refusal that stands
// for "no index to decide on": 503 authz_stale, 412 authz_epoch_unknown
// (R-AUTHZ-015 `index.ForRequest`, R-AUTHZ-028, R-AUTHZ-032).
//
// The index must be confirmed current (staler than authz.cache.max_staleness it
// is not used, the request waits for a confirmation like it waits for the
// database) and, when the request carries Trove-Authz-Min-Epoch, at least as new
// as the epoch named. A request without a credential ignores the header.
func (e *Engine) forRequest(ctx context.Context, sub Subject) (*Index, *Decision) {
	arrived := time.Now()
	min := sub.MinEpoch
	if sub.Anonymous {
		min = 0
	}
	idx := e.cur.Load()
	if min > 0 {
		if idx != nil && idx.label.AuthzEpoch >= min {
			e.m.minEpoch.WithLabelValues(MinEpochCurrent).Inc()
		} else {
			var d *Decision
			if idx, d = e.waitForEpoch(ctx, min, arrived); d != nil {
				return nil, d
			}
		}
	}
	if idx != nil && e.confirmed(idx) {
		return idx, nil
	}
	return e.waitConfirmed(ctx, min)
}

// waitDeadline is the budget of a wait: the request's own deadline or the
// database retry budget, whichever ends first (R-AUTHZ-028 "Stale index").
func (e *Engine) waitDeadline(ctx context.Context) time.Time {
	d := time.Now().Add(e.o.RetryBudget)
	if cd, ok := ctx.Deadline(); ok && cd.Before(d) {
		d = cd
	}
	return d
}

// waitConfirmed waits, holding no database connection, until the installed index
// is confirmed, then decides on it; at the end of the budget the request is
// refused with 503.
func (e *Engine) waitConfirmed(ctx context.Context, min int64) (*Index, *Decision) {
	deadline := e.waitDeadline(ctx)
	t := time.NewTicker(waitTick)
	defer t.Stop()
	for {
		if idx := e.cur.Load(); idx != nil && idx.label.AuthzEpoch >= min && e.confirmed(idx) {
			e.m.waits.WithLabelValues(WaitDecided).Inc()
			return idx, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			e.m.waits.WithLabelValues(WaitRefused).Inc()
			return nil, &Decision{Outcome: DenyStale, Reason: ReasonStale}
		}
		select {
		case <-t.C:
		case <-e.installedCh():
		case <-ctx.Done():
		}
	}
}

// waitForEpoch implements the "Epoch in" steps 2 and 3 of R-AUTHZ-032: read the
// database epoch with a read that starts after the request arrived; an epoch the
// database never reached answers 412 at once; otherwise start a build and wait
// for an index of at least that epoch.
func (e *Engine) waitForEpoch(ctx context.Context, min int64, arrived time.Time) (*Index, *Decision) {
	rctx, cancel := context.WithDeadline(ctx, e.waitDeadline(ctx))
	defer cancel()
	l, err := e.epochs.read(rctx, arrived)
	if err != nil {
		e.m.minEpoch.WithLabelValues(MinEpochTimeout).Inc()
		return nil, &Decision{Outcome: DenyStale, Reason: ReasonStale}
	}
	if l.AuthzEpoch < min {
		e.m.minEpoch.WithLabelValues(MinEpochUnknown).Inc()
		return nil, &Decision{Outcome: DenyEpochUnknown, Reason: ReasonEpochUnknown, Epoch: l.AuthzEpoch}
	}
	deadline := e.waitDeadline(ctx)
	t := time.NewTicker(waitTick)
	defer t.Stop()
	e.requestBuild(TriggerMinEpoch)
	for {
		if idx := e.cur.Load(); idx != nil && idx.label.AuthzEpoch >= min {
			e.m.minEpoch.WithLabelValues(MinEpochWaited).Inc()
			return idx, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			e.m.minEpoch.WithLabelValues(MinEpochTimeout).Inc()
			return nil, &Decision{Outcome: DenyStale, Reason: ReasonStale}
		}
		select {
		case <-t.C:
			if !e.building.Load() {
				e.requestBuild(TriggerMinEpoch) // a build that started before the commit finished without it
			}
		case <-e.installedCh():
		case <-ctx.Done():
		}
	}
}
