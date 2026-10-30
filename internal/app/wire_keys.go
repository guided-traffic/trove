package app

import (
	"context"

	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/secrets"
)

// wireKeys loads the key ring and builds the outbound factory (R-ARCH-024 step
// 5, which runs first because it is a configuration check and cheap). A missing
// or malformed key ring stops the process (R-ARCH-021). The outbound factory
// reports a configuration error through Err; it holds the first readiness back
// instead of stopping the process.
func (a *App) wireKeys(context.Context) (keysArea, error) {
	cfg := a.cfg
	ring, err := secrets.Load(cfg.Secrets)
	if err != nil {
		return keysArea{}, err
	}
	out := outbound.New(cfg.Proxy.Outbound, outbound.WithTracer(a.tele.tracer), outbound.WithMetrics(a.tele.metrics),
		outbound.WithUpstreamLog(a.log), outbound.WithVersion(a.versionString()),
		outbound.WithLogger(a.log), outbound.WithTLSMetrics(a.tele.tls))
	a.health.AddReadiness("outbound", func(context.Context) error { return out.Err() }, true)
	return keysArea{ring: ring, out: out}, nil
}

// wireInfra bundles the results of the infrastructure steps into the one value
// the domain steps take. A member that is nil stops the start-up naming it.
func (a *App) wireInfra(_ context.Context, keys keysArea, pg postgresArea, au auditArea, bl blobArea, co coordArea) (infra, error) {
	in := infra{
		db: pg.db, facts: pg.facts, changes: co.changes, caches: co.caches, signals: co.signals, queue: co.queue,
		blobs: bl.store, vk: co.vk, ring: keys.ring, out: keys.out, rec: au.rec,
	}
	if err := requireInputs(AreaInfra,
		given("database", in.db), given("change log", in.changes), given("cache registry", in.caches), given("signals", in.signals),
		given("job queue", in.queue), given("blob store", in.blobs), given("valkey client", in.vk), given("key ring", in.ring),
		given("outbound factory", in.out), given("audit recorder", in.rec)); err != nil {
		return infra{}, err
	}
	// The exported accessors of the replica (Blobs, Queue, Caches, Changes, Signals)
	// read these; wireInfra is their only writer.
	a.mu.Lock()
	a.signals, a.changes, a.caches, a.queue, a.blobs = co.signals, co.changes, co.caches, co.queue, bl.store
	a.mu.Unlock()
	return in, nil
}
