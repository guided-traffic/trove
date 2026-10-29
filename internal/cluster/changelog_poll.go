package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// pollOnce is one change-log poll (R-ARCH-018 step 3): one query for the epoch
// and changed_at of every area, and for each area above its applied epoch a
// second query for the rows. It runs on the control connection with a deadline
// below the watchdog bound.
func (c *Changes) pollOnce(ctx context.Context) error {
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	err := c.poll(ctx)
	c.fmu.Lock()
	c.lastErr = err
	c.fmu.Unlock()
	return err
}

// poll is one poll; the caller holds pollMu.
func (c *Changes) poll(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.pollTimeout())
	defer cancel()
	start := time.Now()
	qs := q.New(c.o.DB)

	names := make([]string, 0, len(c.areas))
	for n := range c.areas {
		names = append(names, n)
	}
	rows, err := qs.ListClusterEpochs(ctx, names)
	if err != nil {
		c.m.pollErrs.Inc()
		return fmt.Errorf("reading cluster_epoch: %w", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Area] = true
		st := c.areas[r.Area]
		if st == nil {
			continue
		}
		if err := c.reconcile(ctx, qs, st, r.Epoch, r.ChangedAt.Time); err != nil {
			c.m.pollErrs.Inc()
			return fmt.Errorf("area %s: %w", r.Area, err)
		}
	}
	for n, st := range c.areas {
		if !seen[n] && !st.missing {
			st.missing = true
			c.log.Warn("change area has no cluster_epoch row; its changes cannot propagate until the migration that seeds it ran", "area", n)
		}
	}
	end := time.Now()
	c.fmu.Lock()
	c.lastStart, c.lastEnd = start, end
	c.fmu.Unlock()
	c.m.lastOK.Set(float64(end.Unix()))
	return nil
}

// reconcile brings one area up to the epoch the database reports. It holds no
// lock across the queries or the listener calls: a listener may call
// [Changes.AreaState] (st.mu only guards the pair that call reads, set), and the
// poll never blocks a reader of it behind the database. The poll is the only
// writer of an area's state; pollOnce serialises its callers.
func (c *Changes) reconcile(ctx context.Context, qs *q.Queries, st *areaState, epoch int64, changedAt time.Time) error {
	area := st.meta.Name

	if !st.known {
		// The first poll: a replica starts from the current epoch. Nothing is
		// cached yet that an earlier change could have made stale.
		st.known = true
		st.set(epoch, changedAt)
		return nil
	}
	applied := st.applied.Load()
	st.mu.Lock()
	at := st.at
	st.mu.Unlock()
	resync := func(reason string) {
		// Take the database's current epoch as the starting point, even when it
		// is lower, so later changes are not skipped as already applied.
		c.log.Warn("change area flushed", "area", area, "reason", reason, "applied_epoch", applied, "database_epoch", epoch)
		c.flushArea(area, reason)
		st.set(epoch, changedAt)
	}

	switch {
	case epoch < applied:
		resync(FlushEpochRegressed)
		return nil
	case epoch == applied:
		if !changedAt.Equal(at) {
			resync(FlushEpochRegressed) // a reused epoch: later writes raised it to the same number
		}
		return nil
	}

	from := applied
	if from < 1 {
		from = 1
	}
	rows, err := qs.ListClusterChanges(ctx, q.ListClusterChangesParams{Area: area, FromEpoch: from, ToEpoch: epoch})
	if err != nil {
		return fmt.Errorf("reading cluster_change: %w", err)
	}
	if applied > 0 {
		// The row at the last applied epoch proves the history is the one this
		// replica applied: it must exist and carry the created_at it saw.
		switch {
		case len(rows) == 0 || rows[0].Epoch != applied:
			resync(FlushChangeLogGap)
			return nil
		case !rows[0].CreatedAt.Time.Equal(at):
			resync(FlushEpochRegressed)
			return nil
		}
		rows = rows[1:]
	}
	if int64(len(rows)) != epoch-applied {
		resync(FlushChangeLogGap)
		return nil
	}
	for _, r := range rows {
		v, p, ok := parseChangePayload(r.Payload)
		if !ok || v > st.meta.PayloadVersion {
			resync(FlushUnknownPayload)
			return nil
		}
		c.applyChange(area, v, p)
		c.m.applied.WithLabelValues(area, sourcePoll).Inc()
		st.set(r.Epoch, r.CreatedAt.Time)
	}
	return nil
}

// changeRow is the stored form of a change: the payload version and the payload.
type changeRow struct {
	V int             `json:"v"`
	P json.RawMessage `json:"p"`
}

func parseChangePayload(raw []byte) (version int, payload json.RawMessage, ok bool) {
	var r changeRow
	if err := json.Unmarshal(raw, &r); err != nil || r.V < 1 {
		return 0, nil, false
	}
	return r.V, r.P, true
}
