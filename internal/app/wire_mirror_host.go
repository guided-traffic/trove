package app

import (
	"context"
	"errors"
	"sync"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/repository"
)

// AreaMirrorEngine is the remote engine and the format host a process that runs the passes of a mirror stands on (Q-ARCH-316,
// docs/developer/mirror.md): the roles worker and all. In role all it is the engine and the host the role has for its clients, so
// a client fetch pre-empts a pass in the process (R-REPO-055); in role worker it is an engine of its own for background requests,
// built with caches sized for them and a slot limit that does not divide by the replicas that fill client misses.
const AreaMirrorEngine Area = "mirror-engine"

// mirrorEngineArea is the result: the builder that makes the host on its first use. The step cannot fail; building can (the format
// registry, the key ring, a start-up error of a component), and a failure is reported by the first job that needs the host and
// tried again by the next one (Q-ARCH-312).
type mirrorEngineArea struct{ b *mirrorHostBuilder }

// The sizes of the background engine's caches in a worker (Q-ARCH-317): the client-sized defaults (128 MiB of metadata bytes,
// 100,000 negative entries, an 8 MiB query cache) would sit under the worker's 1 GiB limit for a process that serves no client.
const (
	backgroundMetadataCacheBytes = 16 << 20
	backgroundNegativeL1Entries  = 10000
)

// wireMirrorEngine prepares the host's builder from the areas every role has and, in role all, the engine and the host of the
// client path.
func (a *App) wireMirrorEngine(_ context.Context, in infra, px proxyArea, inv invalidationArea, fa filesArea, vz vulnArea, fm formatsArea,
	repo repositoryArea, eng proxyEngineArea, fh formatHostArea,
) (mirrorEngineArea, error) {
	return mirrorEngineArea{b: &mirrorHostBuilder{a: a, in: in, px: px, inv: inv, fa: fa, vz: vz, fm: fm, repo: repo, shared: eng, sharedHost: fh}}, nil
}

// mirrorHost returns the host of this process's passes (mirror.Host, Q-ARCH-288). It builds on its first call.
func (a *App) mirrorHost() mirror.Host { return &lazyMirrorHost{a: a} }

// lazyMirrorHost asks the area's builder on every call: a build that failed is tried again, a build that succeeded is kept.
type lazyMirrorHost struct{ a *App }

var _ mirror.Host = (*lazyMirrorHost)(nil)

func (l *lazyMirrorHost) resolve() (*formathost.MirrorHost, error) {
	b := part[mirrorEngineArea](l.a.parts).b
	if b == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "This process has no mirror engine area: it runs no passes.")
	}
	return b.get()
}

// Ready implements [mirror.Host]: the error of building the host, if it failed; the next call builds again what is missing.
func (l *lazyMirrorHost) Ready(ctx context.Context) error {
	h, err := l.resolve()
	if err != nil {
		return err
	}
	return h.Ready(ctx)
}

// Lister implements [mirror.Host].
func (l *lazyMirrorHost) Lister(format string) (discover.Lister, bool) {
	h, err := l.resolve()
	if err != nil {
		l.a.log.Warn("the mirror host cannot be built; no repository of this process is mirrored until it can", "error", err)
		return nil, false
	}
	return h.Lister(format)
}

// Admits implements [mirror.Host].
func (l *lazyMirrorHost) Admits(ctx context.Context, repo repository.Repository) (func(name string) bool, error) {
	h, err := l.resolve()
	if err != nil {
		return nil, err
	}
	return h.Admits(ctx, repo)
}

// Upstream implements [mirror.Host].
func (l *lazyMirrorHost) Upstream(repo repository.Repository, pass mirror.Pass) (discover.Upstream, error) {
	h, err := l.resolve()
	if err != nil {
		return nil, err
	}
	return h.Upstream(repo, pass)
}

// Fill implements [mirror.Host].
func (l *lazyMirrorHost) Fill(ctx context.Context, repo repository.Repository, pass mirror.Pass, item discover.Item) (mirror.FillResult, error) {
	h, err := l.resolve()
	if err != nil {
		return mirror.FillResult{}, err
	}
	return h.Fill(ctx, repo, pass, item)
}

// mirrorHostBuilder builds the host in stages and keeps what succeeded, so that a retry never builds a stage twice: a cache
// registered twice panics, a flight lock registers its metrics once, a component started twice runs twice.
type mirrorHostBuilder struct {
	a          *App
	in         infra
	px         proxyArea
	inv        invalidationArea
	fa         filesArea
	vz         vulnArea
	fm         formatsArea
	repo       repositoryArea
	shared     proxyEngineArea // the engine of the client path, in role all
	sharedHost formatHostArea

	mu      sync.Mutex
	flights cluster.FlightLock
	set     *invalidation.Set // the worker's own set of cache invalidations (R-REPO-045), started once
	engine  *proxy.Engine
	started bool
	host    *formathost.Host
	built   *formathost.MirrorHost
}

func (b *mirrorHostBuilder) get() (*formathost.MirrorHost, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.built != nil {
		return b.built, nil
	}
	if b.shared.engine != nil && b.sharedHost.host != nil {
		h, ok := b.sharedHost.host.(*formathost.Host)
		if !ok {
			return nil, errors.New("app: the format host of this process is not a *formathost.Host")
		}
		mh, err := formathost.NewMirrorHost(h, b.shared.engine)
		if err != nil {
			return nil, err
		}
		b.built = mh
		return mh, nil
	}
	if b.in.db == nil || b.fa.items == nil || b.fa.decider == nil || b.px.clients == nil || b.in.changes == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The mirror host needs the database, the item store, the decision point, the upstream clients and the change log, which this process has not wired.")
	}
	if err := b.buildEngine(); err != nil {
		return nil, err
	}
	if b.host == nil {
		pkgs, err := packages.NewStore(b.in.db)
		if err != nil {
			return nil, err
		}
		h, err := formathost.New(formathost.Options{
			Runner: b.in.db, Decider: b.fa.decider, Items: b.fa.items, Packages: pkgs, Deps: b.fm.deps, Engine: b.engine,
			Vuln: b.vz.hostPorts(), Enforce: b.vz.enforcer, Repos: b.repo.reader, Log: b.a.log.With("component", "mirror-host"),
		})
		if err != nil {
			return nil, err
		}
		var met *observability.Metrics
		if b.a.tele != nil {
			met = b.a.tele.metrics
		}
		for _, f := range formats.List() {
			if bd, ok := f.(hostBinder); ok {
				bd.Bind(h, b.a.cfg, met)
			}
		}
		b.host = h
	}
	mh, err := formathost.NewMirrorHost(b.host, b.engine)
	if err != nil {
		return nil, err
	}
	b.built = mh
	return mh, nil
}

// buildEngine builds the worker's own engine: the stages flight lock, engine and start are each done once.
func (b *mirrorHostBuilder) buildEngine() error {
	a := b.a
	if b.flights == nil {
		b.flights = cluster.NewFlightLock(b.in.vk, cluster.FlightLockOptions{
			TTL: a.cfg.Cluster.FlightLockTTL, PollInterval: a.cfg.Proxy.LockPollInterval(),
			InstanceUID: a.id.UID, Registerer: a.tele.metrics.Registry(),
		})
	}
	if b.set == nil {
		// A worker has no client listener and so no set of invalidations; the engine's freshness rules (R-REPO-027, R-REPO-045)
		// need one, or a pass would read a document an invalidation made stale as fresh.
		if b.inv.set != nil {
			b.set = b.inv.set
		} else {
			set, err := invalidation.NewSet(invalidation.SetOptions{
				Runner: b.in.db, Changes: b.in.changes, SafetyInterval: a.cfg.Cache.SnapshotSafetyTTL, Log: a.log.With("component", "invalidation"),
			})
			if err != nil {
				return err
			}
			if err := a.startComponent(context.Background(), &invalidationComponent{set: set}); err != nil {
				return err
			}
			b.set = set
		}
	}
	if b.engine == nil {
		opts := proxy.Options{
			Runner: b.in.db, Items: b.fa.items, Blobs: b.in.blobs, Flights: b.flights, Negatives: cluster.NewNegativeStore(b.in.vk),
			Changes: b.in.changes, Clients: b.px.clients, Invalid: b.set, Caches: b.in.caches,
			// The engine of a worker serves no client: it is not one of the replicas that fill client misses, and its background
			// slots must reach the share B of the repository, which a division by their number would not (Q-ARCH-317).
			Replicas: nil,
			Audit:    b.in.rec, Config: backgroundProxyConfig(a.cfg.Proxy), Enforce: b.vz.enforcer, SafetyTTL: a.cfg.Cache.SnapshotSafetyTTL,
			Metrics: a.tele.metrics.Registry(), Log: a.log.With("component", "proxy"),
		}
		if b.in.vk != nil { // a nil *cluster.Valkey in the interface would not be nil
			opts.Lookups = b.in.vk
		}
		if b.in.signals != nil {
			opts.Signals = b.in.signals
		}
		e, err := proxy.New(opts)
		if err != nil {
			return err
		}
		b.engine = e
	}
	if !b.started {
		if err := a.startComponent(context.Background(), b.engine); err != nil {
			return err
		}
		b.started = true
	}
	return nil
}

// backgroundProxyConfig is the proxy section for an engine that serves background requests only: the caches that a client's hit
// uses are small, and the query cache of pass-through searches is off.
func backgroundProxyConfig(p config.Proxy) config.Proxy {
	p.MetadataCacheBytes = min(p.MetadataCacheBytes, backgroundMetadataCacheBytes)
	p.NegativeL1Entries = min(p.NegativeL1Entries, backgroundNegativeL1Entries)
	p.QueryCacheBytes = 0
	return p
}
