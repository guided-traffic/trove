package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/packages"
)

// The wiring of the format plug-in contract (R-ARCH-008 to R-ARCH-010): the storage
// hooks of the item store, the Host the formats reach the core through, the data
// ports of the formats, the files seam of the generic layer and the dispatcher that
// serves /api/<format>/. All of it is built here and nowhere else.
//
// The pieces come up in two moments. The hooks and the seam are needed when the item
// store is built (step 8, wireFiles), and a worker has them too, because its
// `items-delete` job runs the hooks of the files it deletes; they depend on the
// registry, the data ports and the seams of the package writer, and on nothing a
// request carries. The Host and the dispatcher are built when the client listener's
// routers are (step 9, startClient), once the item store and the decision point
// exist, and only on a replica that serves clients.

// formatsWiring is what the format wiring builds once per replica. It is read after
// step 9 and never changes.
type formatsWiring struct {
	deps formathost.Deps

	once sync.Once
	host *formathost.Host
	disp *dispatch.Dispatcher
	err  error
}

// formatPorts returns the data port of every format that declares a DataPort, by
// format ID. The composition root alone imports a format's data package
// (internal/formatdata/<id>; archtest, Q-ARCH-193): a format with side tables adds
// its constructor here, and a missing or wrong binding is a start-up error (typed
// wiring). No format has side tables yet.
func formatPorts(meta.Runner) map[formats.ID]any { return map[formats.ID]any{} }

// bindFormatPorts binds the ports and checks them against the formats: a format that
// declares a DataPort without a binding, a binding that does not implement it and a
// binding for no format stop the start-up (Q-ARCH-193).
func bindFormatPorts(fs []formats.Format, ports map[formats.ID]any) (*formathost.Bindings, error) {
	b := formathost.NewBindings()
	for id, port := range ports {
		if err := b.Bind(id, port); err != nil {
			return nil, err
		}
	}
	if err := b.Check(fs); err != nil {
		return nil, err
	}
	return b, nil
}

// formatsDeps builds the dependencies the hooks and the Host share, binds the data
// ports and checks them against the registry. It also ends the registration of
// formats: from here the catalog the authorization and the repository model read is
// read-only (R-ARCH-010).
func (a *App) formatsDeps(db meta.Runner) (*formatsWiring, error) {
	catalog.Default().Freeze()
	b, err := bindFormatPorts(formats.List(), formatPorts(db))
	if err != nil {
		return nil, err
	}
	d := formathost.Deps{Bindings: b}
	// A nil pointer in an interface would not be nil: the seams are set only where
	// the component exists. Package ownership (R-AUTHZ-018) and the namespace auto
	// claim (R-REPO-023) are not bound here yet: the package writer refuses a claim
	// without them, which no format asks for before one has package names.
	a.mu.Lock()
	changes, signals, queue := a.changes, a.signals, a.queue
	a.mu.Unlock()
	if changes != nil {
		d.Changes = changes
	}
	if signals != nil {
		d.Signals = signals
	}
	if queue != nil {
		d.Jobs = queue
	}
	return &formatsWiring{deps: d}, nil
}

// formatHooks builds the storage hooks and the files seam at step 8 and keeps the
// shared wiring for the Host. The returned hooks go into the item store, the seam
// into the files service. The seam reads the Host when a request needs it; the Host
// does not exist on a worker, which serves no request.
func (a *App) formatHooks(db meta.Runner) (items.Hooks, files.Formats, error) {
	fw, err := a.formatsDeps(db)
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	a.m1.formats = fw
	a.mu.Unlock()
	seam := formathost.NewFilesSeam(func() (formats.Host, error) {
		h, _, err := a.formatHost()
		return h, err
	}, fw.deps)
	return formathost.NewHooks(fw.deps), seam, nil
}

// formatHost builds the Host and the dispatcher on first use, once the item store
// and the decision point exist. A replica that wired no files (a shell build) has
// none, and the format routes are not registered.
func (a *App) formatHost() (formats.Host, *dispatch.Dispatcher, error) {
	a.mu.Lock()
	fw := a.m1.formats
	var db meta.Runner
	if a.db != nil { // a nil *postgres.DB in the interface would not be nil
		db = a.db
	}
	a.mu.Unlock()
	if fw == nil {
		return nil, nil, fmt.Errorf("formats: the storage hooks were not wired")
	}
	fw.once.Do(func() { fw.err = a.buildFormatHost(fw, db) })
	return fw.host, fw.disp, fw.err
}

func (a *App) buildFormatHost(fw *formatsWiring, db meta.Runner) error {
	store, decider, repos := a.itemStore(), a.filesDecider(), a.m1.repositories
	switch {
	case db == nil || store == nil:
		return fmt.Errorf("formats: no item store")
	case decider == nil:
		return fmt.Errorf("formats: no decision point")
	case repos == nil:
		return fmt.Errorf("formats: no repository reader")
	}
	pkgs, err := packages.NewStore(db)
	if err != nil {
		return err
	}
	rend, err := a.renditionCache()
	if err != nil {
		return err
	}
	host, err := formathost.New(formathost.Options{
		Runner: db, Decider: decider, Items: store, Packages: pkgs, Deps: fw.deps, Renditions: rend,
		DownloadBuffer: int(a.cfg.Storage.Download.BufferSize), Log: a.log.With("component", "formats"),
	})
	if err != nil {
		return err
	}
	var ceiling dispatch.Ceiling
	if a.m1.exposureCeiling != nil { // a nil *authz.ExposureCeiling in the interface would not be nil
		ceiling = a.m1.exposureCeiling
	}
	disp, err := dispatch.New(dispatch.Options{
		Host: host, Decider: decider, Repos: repos, Ceiling: ceiling,
		Label: func(ctx context.Context, name string) { observability.RequestInfoFrom(ctx).SetHandler(name) },
	})
	if err != nil {
		return err
	}
	fw.host, fw.disp = host, disp
	return nil
}

// renditionCache registers the rendition cache of the replica (cache.rendition_bytes);
// without a cache registry (a shell build) there is none and no document is cached.
func (a *App) renditionCache() (formats.RenditionCache, error) {
	a.mu.Lock()
	caches := a.caches
	a.mu.Unlock()
	if caches == nil {
		return nil, nil
	}
	return formathost.NewRenditionCache(caches, int64(a.cfg.Cache.RenditionBytes))
}

// registerFormats mounts /api/<format>/ on a router for every format that has
// routes, with the format's error shape (R-ARCH-005, Q-ARCH-214). A replica without
// the item store registers nothing: the prefixes then fall through to the 404 of
// the router, as the files routes answer 503 there.
func (a *App) registerFormats(r *httpserver.Router) error {
	if a.itemStore() == nil {
		return nil
	}
	_, disp, err := a.formatHost()
	if err != nil {
		return fmt.Errorf("formats: %w", err)
	}
	disp.Register(r)
	return nil
}
