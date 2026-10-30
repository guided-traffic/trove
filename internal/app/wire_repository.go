package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/authz"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers every format (R-ARCH-010)
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/repository"
)

// wireRepository is the repository area: the snapshot reader and the service at
// step 8 of the bootstrap (docs/developer/m1-contracts.md "Wiring order"). The
// delete hook records the rules a deletion takes with it (H-164), so the
// exposure area comes first.
func (a *App) wireRepository(_ context.Context, in infra, ex exposureArea) (repositoryArea, error) {
	if err := requireInputs(AreaRepository, given("exposure rule service", ex.rules)); err != nil {
		return repositoryArea{}, err
	}
	log := a.log.With("component", "repository")
	reader, err := repository.NewReader(repository.ReaderOptions{
		Runner: in.db, Catalog: catalog.Default(), Caches: in.caches, Poller: in.changes,
		SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL, UnknownKeys: a.cfg.Cache.UnknownRepoKeys,
		LookupWait: a.cfg.Repositories.LookupWait, Log: log,
	})
	if err != nil {
		return repositoryArea{}, err
	}
	svc, err := repository.NewService(repository.Options{
		Runner: in.db, Catalog: catalog.Default(), Recorder: in.rec, Notifier: in.changes, Jobs: in.queue,
		DeleteBatch: a.cfg.Storage.Delete.BatchSize, Log: log,
		MaxVirtualMembers: a.cfg.Repositories.MaxVirtualMembers,
		// The authorization area records authz.settings.changed in the transaction of an update
		// (R-AUTHZ-031) and endpoint.exposure.deleted for the rules a deletion takes with it (H-164);
		// the policy hook moves the epoch and the policy versions the deletion changes (R-AUTHZ-028);
		// the ownership hook removes the ownership rows of the deleted repository (wire_ownership.go).
		Hooks: repository.MultiHooks(authz.NewSettingsHook(in.rec), authz.NewExposureHook(ex.rules), authz.NewPolicyHook(in.changes), ownershipHook()),
	})
	if err != nil {
		return repositoryArea{}, err
	}
	a.repoSvc.set(svc)
	return repositoryArea{reader: reader, snapshot: reader, service: svc}, nil
}

// registerRepositoryJobs registers the deletion job of R-REPO-013 in the replica's
// job registry. The handler finds the service through a late value, because the
// registry is built (and frozen) before the repository area is wired; a job
// claimed before that is retried.
func (a *App) registerRepositoryJobs(r *jobs.Registry) {
	a.registerFilesJobs(r)
	r.RegisterKind(repository.DeleteJobKind, repository.DeleteJobSpec(
		a.repoSvc.handler("repository service", func(ctx context.Context, svc *repository.Service, run *jobs.Run) error {
			return svc.RunDeleteJob(ctx, run)
		})))
}
