package app

import (
	"context"
	"errors"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
)

// The wiring of the cache invalidation of remote repositories (R-REPO-045,
// R-OPS-021): the replica's set of invalidation rows that the proxy engine asks,
// the service behind the cache operations of the management API and the fold task
// of the worker. The three are one component, so the start-up and the shutdown
// treat them as a unit and no field of App is needed: the accessors find the
// component in the list of started ones.

// invalidationWiring is the component of this wiring.
type invalidationWiring struct {
	// set is the replica's copy of the rows; nil on a replica without a client
	// listener, which never serves a proxy request.
	set    *invalidation.Set
	svc    *invalidation.Service
	folder *invalidation.Folder
}

func (w *invalidationWiring) Name() string { return "invalidation" }

func (w *invalidationWiring) Start(ctx context.Context) error {
	if w.set == nil {
		return nil
	}
	return w.set.Start(ctx)
}

func (w *invalidationWiring) Stop(ctx context.Context) error {
	if w.set == nil {
		return nil
	}
	return w.set.Stop(ctx)
}

// wireInvalidation builds the invalidation component at step 8, after the change
// log and the audit recorder exist. A replica without PostgreSQL has none.
func (a *App) wireInvalidation(ctx context.Context, infra bool) error {
	if !infra || !a.sub.MainPool {
		return nil
	}
	db := a.database()
	a.mu.Lock()
	changes := a.changes
	a.mu.Unlock()
	if db == nil || changes == nil {
		return nil
	}
	log := a.log.With("component", "invalidation")
	w := &invalidationWiring{}
	var err error
	if a.sub.ClientListener {
		w.set, err = invalidation.NewSet(invalidation.SetOptions{
			Runner: db, Changes: changes, SafetyInterval: a.cfg.Cache.SnapshotSafetyTTL, Log: log,
		})
		if err != nil {
			return err
		}
	}
	if w.svc, err = invalidation.NewService(invalidation.ServiceOptions{Runner: db, Notifier: changes, Recorder: a.auditRecorder()}); err != nil {
		return err
	}
	if w.folder, err = invalidation.NewFolder(invalidation.FolderOptions{Runner: db, Notifier: changes, Log: log}); err != nil {
		return err
	}
	return a.startComponent(ctx, w)
}

func (a *App) invalidation() *invalidationWiring {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.comps {
		if w, ok := c.(*invalidationWiring); ok {
			return w
		}
	}
	return nil
}

// InvalidationMatcher is the replica's answer to "does an invalidation make this
// entry stale" for the proxy engine (docs/developer/proxy.md). It is nil on a
// replica that holds none, which serves no proxy request.
func (a *App) InvalidationMatcher() invalidation.Matcher {
	if w := a.invalidation(); w != nil && w.set != nil {
		return w.set
	}
	return nil
}

// cacheAPI returns the services of the cache operations of the management API.
func (a *App) cacheAPI() v1.CacheServices {
	var out v1.CacheServices
	if w := a.invalidation(); w != nil {
		out.Invalidations = w.svc
	}
	a.mu.Lock()
	svc := a.m1.repositoryService
	a.mu.Unlock()
	if svc != nil { // a nil *repository.Service in the interface would not be nil
		out.Repositories = svc
	}
	out.Authz = a.m1.authorizer
	return out
}

// registerInvalidationJobs registers the fold task (R-REPO-045). The handler finds
// the folder at run time, because the registry is built, and frozen, before the
// component is wired; a job claimed before that is retried. The schedule is the
// default of the package unless maintenance.tasks names the task.
func (a *App) registerInvalidationJobs(r *jobs.Registry) {
	r.RegisterKind(invalidation.FoldKind, invalidation.JobSpec(func(ctx context.Context, run *jobs.Run) error {
		w := a.invalidation()
		if w == nil || w.folder == nil {
			return errors.New("invalidation: this replica has not wired the fold task yet")
		}
		return w.folder.Run(ctx, run)
	}))
	spec := invalidation.TaskSpec(invalidation.DefaultFoldSchedule)
	if t, ok := a.cfg.Maintenance.Tasks[invalidation.FoldKind]; ok && t.Schedule != "" {
		spec.Schedule, spec.Disabled = t.Schedule, !t.Enabled
	}
	r.RegisterTask(invalidation.FoldKind, spec)
}
