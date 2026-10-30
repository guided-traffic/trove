package app

import (
	"context"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
)

// AreaInvalidation is the cache invalidation of remote repositories.
const AreaInvalidation Area = "invalidation"

// The wiring of the cache invalidation of remote repositories (R-REPO-045,
// R-OPS-021): the replica's set of invalidation rows that the proxy engine asks,
// the service behind the cache operations of the management API and the fold task
// of the worker.

// invalidationArea is the result: set is the replica's copy of the rows (nil on a
// replica without a client listener, which never serves a proxy request), svc the
// service of the cache operations and of the update of a remote's upstream, and
// folder the fold task.
type invalidationArea struct {
	set    *invalidation.Set
	svc    *invalidation.Service
	folder *invalidation.Folder
}

// invalidationComponent starts and stops the set with the other components.
type invalidationComponent struct{ set *invalidation.Set }

func (c *invalidationComponent) Name() string                    { return "invalidation" }
func (c *invalidationComponent) Start(ctx context.Context) error { return c.set.Start(ctx) }
func (c *invalidationComponent) Stop(ctx context.Context) error  { return c.set.Stop(ctx) }

// wireInvalidation builds the set (on a replica with a client listener), the
// service and the folder after the change log and the audit recorder exist.
func (a *App) wireInvalidation(ctx context.Context, in infra) (invalidationArea, error) {
	log := a.log.With("component", "invalidation")
	var out invalidationArea
	var err error
	if a.sub.ClientListener {
		if out.set, err = invalidation.NewSet(invalidation.SetOptions{
			Runner: in.db, Changes: in.changes, SafetyInterval: a.cfg.Cache.SnapshotSafetyTTL, Log: log,
		}); err != nil {
			return invalidationArea{}, err
		}
		if err := a.startComponent(ctx, &invalidationComponent{set: out.set}); err != nil {
			return invalidationArea{}, err
		}
	}
	if out.svc, err = invalidation.NewService(invalidation.ServiceOptions{Runner: in.db, Notifier: in.changes, Recorder: in.rec}); err != nil {
		return invalidationArea{}, err
	}
	if out.folder, err = invalidation.NewFolder(invalidation.FolderOptions{Runner: in.db, Notifier: in.changes, Log: log}); err != nil {
		return invalidationArea{}, err
	}
	a.folder.set(out.folder)
	return out, nil
}

// InvalidationMatcher is the replica's answer to "does an invalidation make this
// entry stale" for the proxy engine (docs/developer/proxy.md). It is nil on a
// replica that holds none, which serves no proxy request.
func (a *App) InvalidationMatcher() invalidation.Matcher {
	set := part[invalidationArea](a.parts).set
	if set == nil {
		return nil
	}
	return set
}

// cacheAPI returns the services of the cache operations of the management API.
func (a *App) cacheAPI() v1.CacheServices {
	var out v1.CacheServices
	inv, repo := part[invalidationArea](a.parts), part[repositoryArea](a.parts)
	if inv.svc != nil { // a nil pointer in the interface would not be nil
		out.Invalidations = inv.svc
	}
	if repo.service != nil {
		out.Repositories = repo.service
	}
	out.Authz = a.decider()
	return out
}

// registerInvalidationJobs registers the fold task (R-REPO-045). The handler finds
// the folder through a late value, because the registry is built, and frozen,
// before the area is wired; a job claimed before that is retried. The schedule is
// the default of the package unless maintenance.tasks names the task.
func (a *App) registerInvalidationJobs(r *jobs.Registry) {
	r.RegisterKind(invalidation.FoldKind, invalidation.JobSpec(
		a.folder.handler("invalidation folder", func(ctx context.Context, f *invalidation.Folder, run *jobs.Run) error {
			return f.Run(ctx, run)
		})))
	spec := invalidation.TaskSpec(invalidation.DefaultFoldSchedule)
	if t, ok := a.cfg.Maintenance.Tasks[invalidation.FoldKind]; ok && t.Schedule != "" {
		spec.Schedule, spec.Disabled = t.Schedule, !t.Enabled
	}
	r.RegisterTask(invalidation.FoldKind, spec)
}
