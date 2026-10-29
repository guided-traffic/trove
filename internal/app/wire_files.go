package app

import (
	"context"
	"errors"
	"sync"

	"github.com/guided-traffic/trove/internal/api/rawfiles"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/repository"
)

// The wiring of the files transfer (WP1.5): the files service on the blob store,
// the items and the decision point, the write-only buffers of the download
// statistics and the repository activity, the route /files/ and the job that
// deletes a large subtree.

// filesComponent owns the buffers of the files transfer. Their loops run until
// Stop; Flush writes what is left after the last request of a shutdown
// (R-ARCH-024 step 6), so a drain loses no statistics and no activity.
type filesComponent struct {
	stats    *files.Stats
	activity *repository.Activity

	mu     sync.Mutex
	cancel context.CancelFunc
}

var (
	_ Component = (*filesComponent)(nil)
	_ Flusher   = (*filesComponent)(nil)
)

func (c *filesComponent) Name() string { return "files" }

// Start runs the flush loops. They live until Stop, not until the context of the
// start-up ends, because the first act of a shutdown ends that context while
// requests are still being served.
func (c *filesComponent) Start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	c.stats.Start(ctx)
	c.activity.Start(ctx)
	return nil
}

// Stop ends the loops. Flush has run before it.
func (c *filesComponent) Stop(context.Context) error {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.stats.Stop()
	c.activity.Stop()
	return nil
}

// Flush writes the pending download statistics and repository activity.
func (c *filesComponent) Flush(ctx context.Context) error {
	return errors.Join(c.stats.Flush(ctx), c.activity.Flush(ctx))
}

// wireFiles builds the files service at step 8 of the bootstrap, after the
// repository snapshot and the decision point it reads. A replica without
// PostgreSQL or the object store wires nothing, and /files/ answers 503 there.
func (a *App) wireFiles(ctx context.Context, infra bool) error {
	if !infra || !a.sub.MainPool {
		return nil
	}
	db := a.database()
	a.mu.Lock()
	blobs, queue := a.blobs, a.queue
	a.mu.Unlock()
	if db == nil || blobs == nil || a.m1.repositories == nil || a.m1.authorizer == nil {
		return nil
	}
	log := a.log.With("component", "files")
	stats := files.NewStats(db, a.cfg.Storage.Stats.FlushInterval, log)
	activity := repository.NewActivity(db, 0, log)
	opts := files.Options{
		Runner: db, Blobs: blobs, Repos: a.m1.repositories, Authz: a.m1.authorizer, Recorder: a.auditRecorder(),
		Storage: a.cfg.Storage, RecordDownloads: a.cfg.Audit.RecordDownloads, Activity: activity, Stats: stats, Log: log,
	}
	if queue != nil { // a nil *jobs.Queue in the interface would not be nil
		opts.Jobs = queue
	}
	svc, err := files.New(opts)
	if err != nil {
		return err
	}
	if err := a.startComponent(ctx, &filesComponent{stats: stats, activity: activity}); err != nil {
		return err
	}
	a.mu.Lock()
	a.m1.files = svc
	a.mu.Unlock()
	return nil
}

// filesService returns the wired service, nil in a shell without infrastructure.
func (a *App) filesService() *files.Service {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m1.files
}

// registerFiles mounts /files/ on a router. The routes exist on every replica
// that serves the files family; without the service they answer 503.
func (a *App) registerFiles(r *httpserver.Router) {
	rawfiles.New(rawfiles.Options{Service: a.filesService(), Download: a.cfg.Storage.Download, API: a.cfg.API}).Register(r)
}

// repositoryAPI returns the services of the repository operations of the
// management API.
func (a *App) repositoryAPI() v1.RepositoryServices {
	a.mu.Lock()
	svc := a.m1.repositoryService
	a.mu.Unlock()
	return v1.RepositoryServices{
		Service: svc, Authz: a.m1.authorizer, Reader: a.m1.repositories,
		AnonymousAccess: func(ctx context.Context) (bool, error) {
			if a.m1.anonymousAccess == nil {
				return false, nil
			}
			return a.m1.anonymousAccess(ctx)
		},
	}
}

// registerFilesJobs registers the job that deletes a large subtree (R-STOR-018).
// The handler finds the service at run time, because the registry is built, and
// frozen, before the files component is wired.
func (a *App) registerFilesJobs(r *jobs.Registry) {
	r.RegisterKind(files.DeleteJobKind, files.DeleteJobSpec(func(ctx context.Context, run *jobs.Run) error {
		svc := a.filesService()
		if svc == nil {
			return errors.New("files: this replica has not wired the files service yet")
		}
		return svc.RunDeleteJob(ctx, run)
	}))
}
