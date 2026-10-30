package app

import (
	"context"
	"fmt"

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

// The areas of the format plug-in contract (R-ARCH-008 to R-ARCH-010): the storage
// hooks of the item store, the Host the formats reach the core through, the data
// ports of the formats, the files seam of the generic layer and the dispatcher that
// serves /api/<format>/.
const (
	// AreaFormats is the shared dependencies, the data ports, the storage hooks and
	// the files seam. The item store takes the hooks, so it comes before the files
	// area, and a worker has it too: its `items-delete` job runs the hooks of the
	// files it deletes.
	AreaFormats Area = "formats"
	// AreaFormatHost is the Host and the dispatcher, built once the item store and
	// the decision point exist, and only on a replica that serves clients.
	AreaFormatHost Area = "formathost"
)

// formatsArea is what the hooks and the seam are made of. The seam reads the Host
// when a request needs it (a.host); the Host does not exist on a worker, which
// serves no request.
type formatsArea struct {
	deps  formathost.Deps
	hooks items.Hooks
	seam  files.Formats
}

// formatHostArea is the Host and the dispatcher of a replica with a client
// listener.
type formatHostArea struct {
	host formats.Host
	disp *dispatch.Dispatcher
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

// wireFormats builds the dependencies the hooks and the Host share, binds the data
// ports and checks them against the registry, and builds the storage hooks and the
// files seam. It also ends the registration of formats: from here the catalog the
// authorization and the repository model read is read-only (R-ARCH-010). Package
// ownership (R-AUTHZ-018) and the namespace auto claim (R-REPO-023) are not bound
// here yet: the package writer refuses a claim without them, which no format asks
// for before one has package names.
func (a *App) wireFormats(_ context.Context, in infra) (formatsArea, error) {
	catalog.Default().Freeze()
	b, err := bindFormatPorts(formats.List(), formatPorts(in.db))
	if err != nil {
		return formatsArea{}, err
	}
	d := formathost.Deps{Bindings: b, Changes: in.changes, Signals: in.signals, Jobs: in.queue}
	seam := formathost.NewFilesSeam(func() (formats.Host, error) {
		h, ok := a.host.get()
		if !ok {
			return nil, fmt.Errorf("formats: this replica has no host")
		}
		return h, nil
	}, d)
	return formatsArea{deps: d, hooks: formathost.NewHooks(d), seam: seam}, nil
}

// wireFormatHost builds the Host and the dispatcher on the item store and the
// decision point.
func (a *App) wireFormatHost(_ context.Context, in infra, fm formatsArea, repo repositoryArea, ex exposureArea, fa filesArea) (formatHostArea, error) {
	if err := requireInputs(AreaFormatHost, given("item store", fa.items), given("decision point", fa.decider),
		given("repository reader", repo.reader), given("exposure ceiling", ex.ceiling)); err != nil {
		return formatHostArea{}, err
	}
	pkgs, err := packages.NewStore(in.db)
	if err != nil {
		return formatHostArea{}, err
	}
	rend, err := formathost.NewRenditionCache(in.caches, int64(a.cfg.Cache.RenditionBytes))
	if err != nil {
		return formatHostArea{}, err
	}
	host, err := formathost.New(formathost.Options{
		Runner: in.db, Decider: fa.decider, Items: fa.items, Packages: pkgs, Deps: fm.deps, Renditions: rend,
		DownloadBuffer: int(a.cfg.Storage.Download.BufferSize), Log: a.log.With("component", "formats"),
	})
	if err != nil {
		return formatHostArea{}, err
	}
	disp, err := dispatch.New(dispatch.Options{
		Host: host, Decider: fa.decider, Repos: repo.reader, Ceiling: ex.ceiling,
		Label: func(ctx context.Context, name string) { observability.RequestInfoFrom(ctx).SetHandler(name) },
	})
	if err != nil {
		return formatHostArea{}, err
	}
	a.host.set(host)
	return formatHostArea{host: host, disp: disp}, nil
}

// registerFormats mounts /api/<format>/ on a router for every format that has
// routes, with the format's error shape (R-ARCH-005, Q-ARCH-214). The shell profile
// has no Host and registers nothing: the prefixes then fall through to the 404 of
// the router, as the files routes answer 503 there.
func (a *App) registerFormats(r *httpserver.Router) error {
	disp := part[formatHostArea](a.parts).disp
	if disp == nil {
		return nil
	}
	disp.Register(r)
	return nil
}
