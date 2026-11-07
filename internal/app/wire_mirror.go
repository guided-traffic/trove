package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/proxy"
)

// The wiring of the mirror engine (repositories-and-proxy.md R-REPO-053 to R-REPO-058, docs/developer/mirror.md). Every
// process registers the two job kinds, because a process that serves clients enqueues a pass on request (R-REPO-056);
// whatever process claims the queue mirror runs them, and only those processes build the engine, register the counters and
// export the gauges read from PostgreSQL (R-REPO-057).

// AreaMirror is the mirror's part of every process: the counters and the gauges read from PostgreSQL, which every role exports
// so that the alerts see a mirror whatever claims its queue (Q-OPS-171), and the engine, which is built at the first job of a
// process that claims one (the roles worker and all).
const AreaMirror Area = "mirror"

// mirrorArea is the engine of the process. The engine is built when the first job of the queue mirror runs, not when the
// area is wired: its host needs the parts of the remote engine, which the composition root has all built by then.
type mirrorArea struct {
	engine *lazyMirror
}

// lazyMirror builds the engine at first use.
type lazyMirror struct {
	once sync.Once
	make func() (*mirror.Engine, error)
	e    *mirror.Engine
	err  error
}

func (l *lazyMirror) get() (*mirror.Engine, error) {
	l.once.Do(func() { l.e, l.err = l.make() })
	return l.e, l.err
}

// MirrorOverride replaces what the engine is built with, for tests (Options.Mirror): the host (the remote engine's side),
// the clock and the jitter source. A nil member keeps what the composition root builds.
type MirrorOverride struct {
	Host   mirror.Host
	Clock  mirror.Clock
	Jitter mirror.Jitter
}

// defaultMirrorDispatchSchedule is the schedule of the task mirror-dispatch: every minute (R-REPO-053).
const defaultMirrorDispatchSchedule = "* * * * *"

// wireMirror registers the counters and the gauges and prepares the engine.
func (a *App) wireMirror(_ context.Context, in infra, repo repositoryArea) (mirrorArea, error) {
	if err := requireInputs(AreaMirror, given("database", in.db), given("job queue", in.queue), given("audit recorder", in.rec),
		given("repository reader", repo.reader)); err != nil {
		return mirrorArea{}, err
	}
	m, err := mirror.NewMetrics(a.tele.metrics.Registry())
	if err != nil {
		return mirrorArea{}, err
	}
	a.tele.metrics.Repos().OnForget(m.Forget)
	store := mirror.NewPGStore(in.db)
	a.tele.metrics.DBGauges().Add(mirror.GaugeSource(store.Gauges, repo.reader))
	build := func() (*mirror.Engine, error) {
		o := mirror.Options{
			Store: store, Repos: repo.reader, Host: a.mirrorHost(), Queue: in.queue, Audit: in.rec, Metrics: m, Holder: a.id.UID,
			Share:         proxy.BackgroundShare,
			GlobalOffline: func() bool { return a.cfg.Proxy.GlobalOffline }, Log: a.log,
		}
		if ov := a.o.Mirror; ov != nil {
			if ov.Host != nil {
				o.Host = ov.Host
			}
			o.Clock, o.Jitter = ov.Clock, ov.Jitter
		}
		return mirror.New(o)
	}
	return mirrorArea{engine: &lazyMirror{make: build}}, nil
}

// registerMirrorJobs registers the kinds mirror-dispatch and mirror-pass (queue mirror, exclusive, MaxAttempts 1, no timeout)
// and the dispatch task. A pass is continued by the next dispatch after a failure, a release or a crash, never by the job
// framework's retries (R-REPO-053, Q-REPO-136). The handlers find the engine at run time; a process that claims no job has
// none, and none runs a job of this queue there.
func (a *App) registerMirrorJobs(r *jobs.Registry) {
	engine := func() (*mirror.Engine, error) {
		area := part[mirrorArea](a.parts)
		if area.engine == nil {
			return nil, fmt.Errorf("mirror: this replica has not wired the engine")
		}
		return area.engine.get()
	}
	dispatch := func(ctx context.Context, run *jobs.Run) error {
		e, err := engine()
		if err != nil {
			return err
		}
		res, err := e.Dispatch(ctx)
		if err != nil {
			return err
		}
		return run.SetResult(res)
	}
	pass := func(ctx context.Context, run *jobs.Run) error {
		e, err := engine()
		if err != nil {
			return err
		}
		return e.RunPass(ctx, run.ID, run.Args, run)
	}
	r.RegisterKind(mirror.KindPass, jobs.KindSpec{Queue: mirror.QueueMirror, DedupMode: jobs.Exclusive, MaxAttempts: 1, Handler: pass})
	r.RegisterKind(mirror.KindDispatch, jobs.KindSpec{Queue: mirror.QueueDispatch, DedupMode: jobs.Exclusive, MaxAttempts: 1, Handler: dispatch})
	spec := jobs.TaskSpec{Schedule: defaultMirrorDispatchSchedule}
	if c, ok := a.cfg.Maintenance.Tasks[mirror.KindDispatch]; ok && c.Schedule != "" {
		spec.Schedule, spec.Disabled = c.Schedule, !c.Enabled
	}
	r.RegisterTask(mirror.KindDispatch, spec)
}
