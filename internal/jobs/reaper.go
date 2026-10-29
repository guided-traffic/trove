package jobs

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

const (
	reapBatch      = 100
	retentionBatch = 500
)

// reaperLoop runs on every worker replica, every jobs.schedule_tick (the
// reaper is part of the scheduler tick, R-HA-005, but needs no lease):
// expired running jobs go back to queued with backoff, or to failed once
// max_attempts is used up, and finished jobs older than jobs.retention are
// deleted (R-ARCH-016). It is watched as jobs.reaper.
func (w *Worker) reaperLoop() {
	defer w.loops.Done()
	loop := cluster.WatchLoop(w.o.Watchdog, "jobs.reaper", w.o.WatchdogTimeout)
	defer loop.Stop()
	t := time.NewTicker(w.o.Config.ScheduleTick)
	defer t.Stop()
	for {
		w.reap()
		loop.Beat()
		select {
		case <-w.root.Done():
			return
		case <-t.C:
		}
	}
}

// Reap runs one reaper pass. The loop calls it; tests and the first start-up
// call it directly.
func (w *Worker) Reap(ctx context.Context) (reaped int, err error) {
	ctx, cancel := context.WithTimeout(ctx, w.callTimeout())
	defer cancel()
	rows, err := q.New(w.o.Main).ReapJobs(ctx, reapBatch)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		w.m.completed.WithLabelValues(r.Kind, OutcomeReaped).Inc()
		w.log.Warn("expired job reaped", "job", r.ID.String(), "kind", r.Kind, "state", r.State, "attempt", r.Attempt)
	}
	return len(rows), nil
}

func (w *Worker) reap() {
	if _, err := w.Reap(w.root); err != nil && w.root.Err() == nil {
		w.log.Warn("reaper failed", "error", err)
	}
	ctx, cancel := context.WithTimeout(w.root, w.callTimeout())
	defer cancel()
	n, err := q.New(w.o.Main).DeleteFinishedJobs(ctx, q.DeleteFinishedJobsParams{
		RetentionSecs: w.o.Config.Retention.Seconds(), Batch: retentionBatch,
	})
	if err != nil && w.root.Err() == nil {
		w.log.Warn("job retention failed", "error", err)
		return
	}
	if n > 0 {
		w.log.Debug("finished jobs deleted", "count", n)
	}
}
