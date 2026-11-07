package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/proxy"
)

// AreaProxyEngine is the remote engine: the request path of the remote
// repositories (R-REPO-025).
const AreaProxyEngine Area = "proxy-engine"

// proxyEngineArea is the result: the engine a replica with a client listener
// answers remote requests with. A replica without a listener serves none and has no
// engine.
type proxyEngineArea struct {
	engine *proxy.Engine
	fill   *cluster.FillCount
}

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
func (a *App) wireProxyEngine(ctx context.Context, in infra, px proxyArea, inv invalidationArea, fa filesArea, vz vulnArea) (proxyEngineArea, error) {
	if err := requireInputs(AreaProxyEngine, given("upstream clients", px.clients), given("invalidation set", inv.set),
		given("item store", fa.items)); err != nil {
		return proxyEngineArea{}, err
	}
	flights := cluster.NewFlightLock(in.vk, cluster.FlightLockOptions{
		TTL: a.cfg.Cluster.FlightLockTTL, PollInterval: a.cfg.Proxy.LockPollInterval(),
		InstanceUID: a.id.UID, Registerer: a.tele.metrics.Registry(),
	})
	// The fetch share divides a remote repository's budget by the live replicas that fill client misses, whatever their
	// role and pool (R-REPO-040, Q-ARCH-297, Q-OPS-165): the limiters' count, which is per role and pool, is not it.
	fill, err := cluster.NewFillCount(in.db.ControlPool(), a.cfg.System.HeartbeatStaleAfter, a.cfg.System.HeartbeatInterval)
	if err != nil {
		return proxyEngineArea{}, err
	}
	if err := a.startComponent(ctx, fill); err != nil {
		return proxyEngineArea{}, err
	}
	opts := proxy.Options{
		Runner: in.db, Items: fa.items, Blobs: in.blobs, Flights: flights, Negatives: cluster.NewNegativeStore(in.vk),
		Changes: in.changes, Clients: px.clients, Invalid: inv.set, Caches: in.caches,
		Replicas: fill.Get, Audit: in.rec, Config: a.cfg.Proxy, Enforce: vz.enforcer, SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL,
		Metrics: a.tele.metrics.Registry(), Log: a.log.With("component", "proxy"),
		Detach: httpserver.WithoutTicket,
	}
	if in.vk != nil { // a nil *cluster.Valkey in the interface would not be nil
		opts.Lookups = in.vk
	}
	if in.signals != nil { // the signal mirror_yield: raised by a client fetch that waits, heard by a pass's background fetches
		opts.Signals = in.signals
	}
	engine, err := proxy.New(opts)
	if err != nil {
		return proxyEngineArea{}, err
	}
	a.tele.metrics.Repos().OnForget(engine.Forget)
	if err := a.startComponent(ctx, engine); err != nil {
		return proxyEngineArea{}, err
	}
	return proxyEngineArea{engine: engine, fill: fill}, nil
}

// FetchShareReplicas returns the n the fetch slots divide a remote repository's budget by: the live replicas that fill client
// misses (R-REPO-040, Q-ARCH-297). It is 0 on a replica without the remote engine, which has no fetch share. The integration
// tests of the roles read it.
func (a *App) FetchShareReplicas() int {
	fill := part[proxyEngineArea](a.parts).fill
	if fill == nil {
		return 0
	}
	return fill.Get()
}
