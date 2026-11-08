package app

import (
	"context"
	"errors"
	"sync"

	"github.com/guided-traffic/trove/internal/api/rawfiles"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
)

// The wiring of the files transfer (WP1.5): the item store on the blob store and
// the items tables, the generic layer over it (the files service), the write-only
// buffers of the download statistics and the repository activity, the route
// /files/ and the job that deletes a large subtree. The decision point is not
// wired into the services: a request begins its index at the entry point
// (rawfiles, the items operations of the management API) and hands the generic
// layer the holder, and the item store makes no decision.

// filesComponent owns the buffers of the files transfer. Their loops run until
// Stop; Flush writes what is left after the last request of a shutdown
// (R-ARCH-024 step 6), so a drain loses no statistics and no activity.
type filesComponent struct {
	stats    *items.Stats
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
func (c *filesComponent) Flush(ctx context.Context) error { return c.Sync(ctx) }

// Sync is Flush for a running replica: the buffers keep working afterwards
// (App.Flush).
func (c *filesComponent) Sync(ctx context.Context) error {
	return errors.Join(c.stats.Flush(ctx), c.activity.Flush(ctx))
}

// wireFiles is the files area: the item store, then the files service over it.
// The files service names its decision point through the entry points that
// serve it, so the area takes the authz area: a role that serves /files/ without
// a decision point is refused at start-up instead of answering 503 on the route.
func (a *App) wireFiles(ctx context.Context, in infra, repo repositoryArea, az authzArea, fm formatsArea, vz vulnArea) (filesArea, error) {
	if err := requireInputs(AreaFiles, given("repository reader", repo.reader), given("authorizer", az.engine), given("format hooks", fm.hooks), given("format seam", fm.seam)); err != nil {
		return filesArea{}, err
	}
	log := a.log.With("component", "files")
	stats := items.NewStats(in.db, a.cfg.Storage.Stats.FlushInterval, log)
	activity := repository.NewActivity(in.db, 0, log)
	store, err := items.New(items.Options{
		Runner: in.db, Blobs: in.blobs, Recorder: in.rec, Jobs: in.queue,
		Storage: a.cfg.Storage, RecordDownloads: a.cfg.Audit.RecordDownloads, Activity: activity, Stats: stats, Hooks: fm.hooks, Log: log,
	})
	if err != nil {
		return filesArea{}, err
	}
	svc, err := files.New(files.Options{Items: store, Repos: repo.reader, Formats: fm.seam, Enforce: vz.enforcer, Log: log})
	if err != nil {
		return filesArea{}, err
	}
	store.SetJobGuard(svc.DeleteJobGuard(az.engine))
	if err := a.startComponent(ctx, &filesComponent{stats: stats, activity: activity}); err != nil {
		return filesArea{}, err
	}
	a.itemStore.set(store)
	if err := a.wireServeCheckHost(in, repo, az, fm, store); err != nil {
		return filesArea{}, err
	}
	return filesArea{items: store, service: svc, decider: az.engine}, nil
}

// wireServeCheckHost gives the files seam the Host its serve check reads format state through, on a role that serves
// the files service but builds no format host (the role api, Q-ARCH-301): the UI's downloads reach the item store
// through the files service, and the format's ServeCheck reads the stored marks of the item through the data port,
// FormatData of the Host (R-ARCH-008 "Serve-time check"). It is the Host without a remote engine, a resolver and a
// dispatcher: none of the calls that fetch, resolve or write for a protocol route is reachable on the role, because
// no route that makes them is registered, and the Host answers them with the internal error of a Host built without
// them. A role with the format host sets the Host in wireFormatHost.
func (a *App) wireServeCheckHost(in infra, repo repositoryArea, az authzArea, fm formatsArea, store *items.Store) error {
	if !a.sub.ClientListener || a.sub.Has(AreaFormatHost) {
		return nil
	}
	pkgs, err := packages.NewStore(in.db)
	if err != nil {
		return err
	}
	host, err := formathost.New(formathost.Options{
		Runner: in.db, Decider: az.engine, Items: store, Packages: pkgs, Deps: fm.deps, Repos: repo.reader,
		DownloadBuffer: int(a.cfg.Storage.Download.BufferSize), Log: a.log.With("component", "files"),
	})
	if err != nil {
		return err
	}
	a.host.set(host)
	return nil
}

// registerFiles mounts /files/ and the content routes on a router. A replica that serves a client
// listener has the files area (requireServing); only the shell profile mounts the
// routes without a service, where they answer 503.
func (a *App) registerFiles(r *httpserver.Router) {
	fa := part[filesArea](a.parts)
	h := rawfiles.New(rawfiles.Options{Service: fa.service, Decider: fa.decider, Download: a.cfg.Storage.Download, API: a.cfg.API})
	h.Register(r)
	// The UI's content routes below /api/v1/ (R-API-025, Q-UI-106) are of the family api: the router drops them on a
	// role or endpoint that does not serve it. They share the service of /files/, so they sit beside it for now.
	h.Content(int64(a.cfg.UI.Deploy.MaxUploadBytes)).Register(r)
}

// repositoryAPI returns the services of the repository operations of the
// management API.
func (a *App) repositoryAPI() v1.RepositoryServices {
	repo, id := part[repositoryArea](a.parts), part[identityArea](a.parts)
	anonymous := id.anonymousAccess
	if anonymous == nil { // the shell profile: no switch, so anonymous access is off
		anonymous = func(context.Context) (bool, error) { return false, nil }
	}
	return v1.RepositoryServices{Service: repo.service, Authz: a.decider(), Reader: a.Repositories(), AnonymousAccess: anonymous}
}

// registerFilesJobs registers the job that deletes a large subtree (R-STOR-018).
// The handler finds the item store through a late value, because the registry is
// built, and frozen, before the files area is wired. The request that queued the job
// decided every item of the subtree; the job decides only what a hook cascades to, for the
// requesting subject, through the job guard (items.Store.SetJobGuard, H-653).
func (a *App) registerFilesJobs(r *jobs.Registry) {
	r.RegisterKind(items.DeleteJobKind, items.DeleteJobSpec(
		a.itemStore.handler("item store", func(ctx context.Context, store *items.Store, run *jobs.Run) error {
			return store.RunDeleteJob(ctx, run)
		})))
}

// Items returns the item store of the files area, nil on a replica without it. The restore hands it to the remote-cache
// purgers as the storage delete path (fixup.Env.Items, R-BAK-008), and tests read it the same way.
func (a *App) Items() *items.Store { s, _ := a.itemStore.get(); return s }
