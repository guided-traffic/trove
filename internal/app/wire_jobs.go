package app

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/jobs"
)

// Fallback cron schedules of the storage tasks (R-STOR-025). The configuration
// (maintenance.tasks) has the same defaults and wins; these apply only when the
// key is absent from it.
const (
	defaultGCSchedule     = "*/30 * * * *"
	defaultGCFullSchedule = "0 2 * * 0"
)

// jobRegistry builds the replica's job registry: the kinds that packages
// registered in jobs.Default at initialisation, the coordination kinds and the
// garbage-collection kinds. Every role builds it, because a web replica must know
// the kinds to enqueue them (R-ARCH-016). It is frozen before the replica serves.
// A registry per replica, not jobs.Default itself, keeps several replicas in one
// test process apart.
func (a *App) jobRegistry() (*jobs.Registry, error) {
	r := jobs.NewRegistry()
	for _, k := range jobs.Default.Kinds() {
		spec, _ := jobs.Default.Kind(k)
		r.RegisterKind(k, spec)
	}
	for name, t := range jobs.Default.Tasks() {
		r.RegisterTask(name, t)
	}
	jobs.RegisterCoordinationKinds(r, a.cfg.Cluster)
	a.registerGC(r)
	if err := jobs.ValidateSchedules(a.cfg.Maintenance); err != nil {
		return nil, err
	}
	r.Freeze()
	return r, nil
}

// registerGC registers the kinds gc and gc-full and their tasks (R-STOR-025):
// queue maintenance, exclusive, both tasks under the dedup key "gc", so at most
// one collection is queued or running whichever task started it. The handlers
// find the collector at run time; only worker and all build one.
func (a *App) registerGC(r *jobs.Registry) {
	handler := func(mode blob.GCMode) jobs.Handler {
		return func(ctx context.Context, run *jobs.Run) error {
			c := a.collector()
			if c == nil {
				return jobs.Permanent(errors.New("this replica has no garbage collector"))
			}
			return c.RunJob(ctx, mode, run.Args, run)
		}
	}
	for _, k := range []struct {
		kind     string
		mode     blob.GCMode
		fallback string
	}{{"gc", blob.GCIncremental, defaultGCSchedule}, {"gc-full", blob.GCFull, defaultGCFullSchedule}} {
		r.RegisterKind(k.kind, jobs.KindSpec{Queue: "maintenance", DedupMode: jobs.Exclusive, Handler: handler(k.mode)})
		spec := jobs.TaskSpec{Schedule: k.fallback, DedupKey: "gc"}
		if t, ok := a.cfg.Maintenance.Tasks[k.kind]; ok && t.Schedule != "" {
			spec.Schedule, spec.Disabled = t.Schedule, !t.Enabled
		}
		r.RegisterTask(k.kind, spec)
	}
}

func (a *App) collector() *blob.Collector { a.mu.Lock(); defer a.mu.Unlock(); return a.gc }
