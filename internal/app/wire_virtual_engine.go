package app

import (
	"context"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/virtual"
)

// AreaVirtualEngine is the virtual engine: resolution, the merged-metadata cache, the
// shadowing check and the explanation (docs/developer/virtual.md).
const AreaVirtualEngine Area = "virtual-engine"

// virtualEngineArea is the engine of a replica with a client listener.
type virtualEngineArea struct{ engine *virtual.Resolver }

// virtualComponent starts the subscription to metadata_changed and ends it with the replica.
type virtualComponent struct{ engine *virtual.Resolver }

func (c *virtualComponent) Name() string                    { return "virtual" }
func (c *virtualComponent) Start(ctx context.Context) error { return c.engine.Start(ctx) }
func (c *virtualComponent) Stop(context.Context) error {
	c.engine.Stop()
	return nil
}

// wireVirtualEngine builds the engine on the routing, the decision point and the item
// store (read from the files area when that runs; without it there is no engine). The proxy engine is not bound: virtualRemotes answers nil until it is, and a
// remote member is then one that cannot be asked (502 for an artifact, a partial merge,
// `offline` in the explanation, undetermined for the shadowing check). The Host does
// not take the engine as its Resolver yet; that is one line in wireFormatHost when the
// first format with a virtual class registers (docs/developer/virtual.md).
func (a *App) wireVirtualEngine(ctx context.Context, in infra, v virtualArea, az authzArea) (virtualEngineArea, error) {
	if err := requireInputs(AreaVirtualEngine, given("routing", v.routing), given("decision point", az.engine)); err != nil {
		return virtualEngineArea{}, err
	}
	// A role without the files area serves no package protocol; it has no engine either.
	store, ok := a.itemStore.get()
	if !ok {
		return virtualEngineArea{}, nil
	}
	pkgs, err := packages.NewStore(in.db)
	if err != nil {
		return virtualEngineArea{}, err
	}
	o := virtual.Options{
		Router: v.routing, Decider: az.engine, Locals: formathost.NewVirtualLocals(pkgs, store),
		Remotes: virtualRemotes(), Caches: in.caches, CacheBytes: int64(a.cfg.Proxy.MergedMetadataCacheBytes),
		Metrics: a.tele.metrics.Registry(), Log: a.log.With("component", "virtual"),
	}
	if in.signals != nil { // a nil *cluster.Signals in the interface would not be nil
		o.Signals = in.signals
	}
	eng, err := virtual.New(o)
	if err != nil {
		return virtualEngineArea{}, err
	}
	if err := a.startComponent(ctx, &virtualComponent{engine: eng}); err != nil {
		return virtualEngineArea{}, err
	}
	return virtualEngineArea{engine: eng}, nil
}

// virtualRemotes is the remote engine as the virtual engine asks it (Cached, Metadata,
// EntryState, Probe, CachedArtifacts). The proxy engine is not on main yet; binding it
// is returning it here (docs/developer/virtual.md "Binding the proxy engine").
func virtualRemotes() virtual.Remotes { return nil }

// resolutionExplainer is the engine as the management API's explanation operation takes
// it, nil on a replica without one.
func (a *App) resolutionExplainer() v1.ResolutionExplainer {
	if e := part[virtualEngineArea](a.parts).engine; e != nil { // a nil *virtual.Resolver in the interface would not be nil
		return e
	}
	return nil
}
