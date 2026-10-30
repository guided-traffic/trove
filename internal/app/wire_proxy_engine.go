package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/proxy"
)

// AreaProxyEngine is the remote engine: the request path of the remote
// repositories (R-REPO-025).
const AreaProxyEngine Area = "proxy-engine"

// proxyEngineArea is the result: the engine a replica with a client listener
// answers remote requests with. A replica without a listener serves none and has no
// engine.
type proxyEngineArea struct{ engine *proxy.Engine }

// remote is the engine as the format host takes it.
func (p proxyEngineArea) remote() formathost.RemoteEngine {
	if p.engine == nil { // a nil *proxy.Engine in the interface would not be nil
		return nil
	}
	return p.engine
}

// wireProxyEngine builds the engine on the upstream clients, the item store, the
// invalidation set and the flight lock. It is a component: the shutdown drains it
// (no new cross-replica flight, R-ARCH-024 step 1) and cancels the fetches still
// running 5 s before the deadline, releasing their locks with failed (step 4).
func (a *App) wireProxyEngine(ctx context.Context, in infra, px proxyArea, inv invalidationArea, fa filesArea, lim limitsArea) (proxyEngineArea, error) {
	if err := requireInputs(AreaProxyEngine, given("upstream clients", px.clients), given("invalidation set", inv.set),
		given("item store", fa.items), given("replica count", lim.replicas)); err != nil {
		return proxyEngineArea{}, err
	}
	flights := cluster.NewFlightLock(in.vk, cluster.FlightLockOptions{
		TTL: a.cfg.Cluster.FlightLockTTL, PollInterval: a.cfg.Proxy.LockPollInterval(),
		InstanceUID: a.id.UID, Registerer: a.tele.metrics.Registry(),
	})
	opts := proxy.Options{
		Runner: in.db, Items: fa.items, Blobs: in.blobs, Flights: flights, Negatives: cluster.NewNegativeStore(in.vk),
		Changes: in.changes, Clients: px.clients, Invalid: inv.set, Caches: in.caches,
		Replicas: lim.replicas.Get, Audit: in.rec, Config: a.cfg.Proxy, SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL,
		Metrics: a.tele.metrics.Registry(), Log: a.log.With("component", "proxy"),
	}
	if in.vk != nil { // a nil *cluster.Valkey in the interface would not be nil
		opts.Lookups = in.vk
	}
	engine, err := proxy.New(opts)
	if err != nil {
		return proxyEngineArea{}, err
	}
	if err := a.startComponent(ctx, engine); err != nil {
		return proxyEngineArea{}, err
	}
	return proxyEngineArea{engine: engine}, nil
}
