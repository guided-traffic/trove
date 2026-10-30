package app

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers every format (R-ARCH-010)
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/repository"
)

// wireRepository builds the repository snapshot reader and the service at step 8
// of the bootstrap (docs/developer/m1-contracts.md "Wiring order"). Nothing is
// built on a replica without PostgreSQL (a unit-test build).
func (a *App) wireRepository() error {
	db := a.database()
	a.mu.Lock()
	changes, caches, queue := a.changes, a.caches, a.queue
	a.mu.Unlock()
	if db == nil || changes == nil || caches == nil {
		return nil
	}
	log := a.log.With("component", "repository")
	reader, err := repository.NewReader(repository.ReaderOptions{
		Runner: db, Catalog: catalog.Default(), Caches: caches, Poller: changes,
		SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL, UnknownKeys: a.cfg.Cache.UnknownRepoKeys,
		LookupWait: a.cfg.Repositories.LookupWait, Log: log,
	})
	if err != nil {
		return err
	}
	// The recorder is built at step 4; a shell without infrastructure has none and
	// discards the events.
	var rec audit.Recorder = audit.Nop{}
	if a.m1.recorder != nil {
		rec = a.m1.recorder
	}
	opts := repository.Options{
		Runner: db, Catalog: catalog.Default(), Recorder: rec, Notifier: changes,
		DeleteBatch: a.cfg.Storage.Delete.BatchSize, Log: log,
		MaxVirtualMembers: a.cfg.Repositories.MaxVirtualMembers,
		// The authorization area records authz.settings.changed in the transaction of an update
		// (R-AUTHZ-031) and endpoint.exposure.deleted for the rules a deletion takes with it (H-164);
		// the policy hook moves the epoch and the policy versions the deletion changes (R-AUTHZ-028).
		Hooks: repository.MultiHooks(authz.NewSettingsHook(rec), authz.NewExposureHook(a.exposureService()), authz.NewPolicyHook(changes)),
	}
	if queue != nil { // a nil *jobs.Queue in the interface would not be nil
		opts.Jobs = queue
	}
	svc, err := repository.NewService(opts)
	if err != nil {
		return err
	}
	a.m1.repositories = reader
	a.mu.Lock()
	a.m1.repositoryService = svc
	a.mu.Unlock()
	return a.wireVirtual(db, reader, caches, changes, rec)
}

// registerRepositoryJobs registers the deletion job of R-REPO-013 in the replica's
// job registry. The handler finds the service at run time, because the registry
// is built (and frozen) before the repository component is wired; a job claimed
// before that is retried.
func (a *App) registerRepositoryJobs(r *jobs.Registry) {
	a.registerFilesJobs(r)
	r.RegisterKind(repository.DeleteJobKind, repository.DeleteJobSpec(func(ctx context.Context, run *jobs.Run) error {
		a.mu.Lock()
		svc := a.m1.repositoryService
		a.mu.Unlock()
		if svc == nil {
			return errors.New("repository: this replica has not wired the repository service yet")
		}
		return svc.RunDeleteJob(ctx, run)
	}))
}
