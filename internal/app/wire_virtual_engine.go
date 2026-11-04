package app

import (
	"context"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/proxy"
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
// store of the files area. The remote engine is an optional input (withOptional,
// Q-ARCH-300):
//
//   - a role that fills caches (packages, web, all) enables the proxy-engine area, and the
//     remote members are asked through the engine (formathost.NewVirtualRemotes); the Host
//     takes the virtual engine as its Resolver (wireFormatHost);
//   - the role api does not, and the remote members are asked through the read-only view
//     (proxy.View): the stored entries, the negative entries, the circuit and the offline
//     mode, never an upstream (Q-ARCH-294). The engine serves the explanation of a resolution
//     there and nothing else: no route of api resolves a request through it, because the
//     format host that does is not built (Q-ARCH-301).
//
// An input that is missing stops the start-up: a replica without the engine would let a
// first publish skip the shadowing check (R-REPO-048).
func (a *App) wireVirtualEngine(ctx context.Context, in infra, v virtualArea, az authzArea, repo repositoryArea, fm formatsArea, inv invalidationArea, vz vulnArea, fa filesArea, px proxyEngineArea) (virtualEngineArea, error) {
	ins := []input{
		given("routing", v.routing), given("decision point", az.engine),
		given("repository reader", repo.reader), given("item store", fa.items),
	}
	if a.sub.Has(AreaProxyEngine) {
		ins = append(ins, given("remote engine", px.engine))
	}
	if err := requireInputs(AreaVirtualEngine, ins...); err != nil {
		return virtualEngineArea{}, err
	}
	store := fa.items
	pkgs, err := packages.NewStore(in.db)
	if err != nil {
		return virtualEngineArea{}, err
	}
	ro := formathost.VirtualRemotesOptions{Repos: repo.reader, Decider: az.engine, Deps: fm.deps}
	var fillRefused func(string)
	if px.engine != nil {
		ro.Engine, fillRefused = px.engine, px.engine.FillRefused
	} else {
		view, err := a.remoteView(ctx, in, inv, vz, fa)
		if err != nil {
			return virtualEngineArea{}, err
		}
		ro.View = view
	}
	remotes, err := formathost.NewVirtualRemotes(ro)
	if err != nil {
		return virtualEngineArea{}, err
	}
	o := virtual.Options{
		Router: v.routing, Decider: az.engine, Locals: formathost.NewVirtualLocals(pkgs, store),
		Remotes: remotes, Caches: in.caches, CacheBytes: int64(a.cfg.Proxy.MergedMetadataCacheBytes),
		Metrics: a.tele.metrics.Registry(), Log: a.log.With("component", "virtual"),
		FillRefused: fillRefused,
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

// remoteView builds the read-only view of the remote members for a role without the
// proxy-engine area (Q-ARCH-294): the engine's readers over the item store, the cache
// entries, the negative store and the circuit state, without upstream clients, a blob
// store, a fetch slot or a flight lock, and without the engine's metrics. It is a
// component, so its background reloads of the circuit state end with the replica.
func (a *App) remoteView(ctx context.Context, in infra, inv invalidationArea, vz vulnArea, fa filesArea) (*proxy.View, error) {
	opts := proxy.Options{
		Runner: in.db, Items: fa.items, Negatives: cluster.NewNegativeStore(in.vk), Invalid: inv.set, Caches: in.caches,
		Config: a.cfg.Proxy, Enforce: vz.enforcer, SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL,
		Log: a.log.With("component", "proxy-view"),
	}
	if in.vk != nil { // a nil *cluster.Valkey in the interface would not be nil
		opts.Lookups = in.vk
	}
	view, err := proxy.NewView(opts)
	if err != nil {
		return nil, err
	}
	if err := a.startComponent(ctx, view); err != nil {
		return nil, err
	}
	return view, nil
}

// resolutionExplainer is the engine as the management API's explanation operation takes
// it, nil on a replica without one.
func (a *App) resolutionExplainer() v1.ResolutionExplainer {
	if e := part[virtualEngineArea](a.parts).engine; e != nil { // a nil *virtual.Resolver in the interface would not be nil
		return e
	}
	return nil
}
