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
// store of the files area. The remote members are asked through the proxy engine
// (formathost.NewVirtualRemotes). The Host takes the engine as its Resolver
// (wireFormatHost). An input that is missing stops the start-up: a replica without the
// engine would let a first publish skip the shadowing check (R-REPO-048).
func (a *App) wireVirtualEngine(ctx context.Context, in infra, v virtualArea, az authzArea, repo repositoryArea, fm formatsArea, px proxyEngineArea, fa filesArea) (virtualEngineArea, error) {
	if err := requireInputs(AreaVirtualEngine, given("routing", v.routing), given("decision point", az.engine),
		given("repository reader", repo.reader), given("remote engine", px.engine), given("item store", fa.items)); err != nil {
		return virtualEngineArea{}, err
	}
	store := fa.items
	pkgs, err := packages.NewStore(in.db)
	if err != nil {
		return virtualEngineArea{}, err
	}
	remotes, err := formathost.NewVirtualRemotes(formathost.VirtualRemotesOptions{Engine: px.engine, Repos: repo.reader, Decider: az.engine, Deps: fm.deps})
	if err != nil {
		return virtualEngineArea{}, err
	}
	o := virtual.Options{
		Router: v.routing, Decider: az.engine, Locals: formathost.NewVirtualLocals(pkgs, store),
		Remotes: remotes, Caches: in.caches, CacheBytes: int64(a.cfg.Proxy.MergedMetadataCacheBytes),
		Metrics: a.tele.metrics.Registry(), Log: a.log.With("component", "virtual"),
		FillRefused: px.engine.FillRefused,
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

// resolutionExplainer is the engine as the management API's explanation operation takes
// it, nil on a replica without one.
func (a *App) resolutionExplainer() v1.ResolutionExplainer {
	if e := part[virtualEngineArea](a.parts).engine; e != nil { // a nil *virtual.Resolver in the interface would not be nil
		return e
	}
	return nil
}
