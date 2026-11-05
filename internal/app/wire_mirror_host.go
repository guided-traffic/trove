package app

import (
	"context"
	"errors"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
)

// mirrorHost returns the host of this process's passes (mirror.Host, Q-ARCH-288): the remote engine and the format host behind the
// seam. It is built on its first use, because the job registry that holds the pass's handlers is frozen before the areas it
// needs are wired. A process that serves clients (packages, web, all) lends the engine and the host it has: a pass's background
// requests then share the replica's fetch slots with its client fetches, so that a client pre-empts them in the process
// (R-REPO-055). A process that does not (a worker that claims the queue mirror) builds an engine and a host of its own from the
// areas every role has, and subscribes the engine to the signal mirror_yield, so that the clients of other processes pre-empt its
// passes (Q-ARCH-308).
func (a *App) mirrorHost() mirror.Host { return &lazyMirrorHost{a: a} }

// lazyMirrorHost resolves the host on the first call and keeps the answer, an error included: a process that cannot build it
// fails every call with the same error instead of building half of it again.
type lazyMirrorHost struct {
	a    *App
	once sync.Once
	host *formathost.MirrorHost
	err  error
}

var _ mirror.Host = (*lazyMirrorHost)(nil)

func (l *lazyMirrorHost) resolve() (*formathost.MirrorHost, error) {
	l.once.Do(func() { l.host, l.err = l.a.buildMirrorHost() })
	return l.host, l.err
}

// Lister implements [mirror.Host].
func (l *lazyMirrorHost) Lister(format string) (discover.Lister, bool) {
	h, err := l.resolve()
	if err != nil {
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

// buildMirrorHost assembles the host from the wired areas.
func (a *App) buildMirrorHost() (*formathost.MirrorHost, error) {
	w := a.parts
	eng, host := part[proxyEngineArea](w), part[formatHostArea](w)
	if eng.engine != nil && host.host != nil {
		if h, ok := host.host.(*formathost.Host); ok {
			return formathost.NewMirrorHost(h, eng.engine)
		}
		return nil, errors.New("app: the format host of this process is not a *formathost.Host")
	}
	// A process that does not fill client misses has neither: it builds them from the areas every role has.
	in, fa, fm := part[infra](w), part[filesArea](w), part[formatsArea](w)
	px, inv, vz, repo := part[proxyArea](w), part[invalidationArea](w), part[vulnArea](w), part[repositoryArea](w)
	if in.db == nil || fa.items == nil || fa.decider == nil || px.clients == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The mirror host needs the database, the item store, the decision point and the upstream clients, which this process has not wired.")
	}
	own, err := a.wireProxyEngine(context.Background(), in, px, inv, fa, vz)
	if err != nil {
		return nil, err
	}
	pkgs, err := packages.NewStore(in.db)
	if err != nil {
		return nil, err
	}
	h, err := formathost.New(formathost.Options{
		Runner: in.db, Decider: fa.decider, Items: fa.items, Packages: pkgs, Deps: fm.deps, Engine: own.remote(),
		Vuln: vz.hostPorts(), Enforce: vz.enforcer, Repos: repo.reader, Log: a.log.With("component", "mirror-host"),
	})
	if err != nil {
		return nil, err
	}
	var met *observability.Metrics
	if a.tele != nil {
		met = a.tele.metrics
	}
	for _, f := range formats.List() {
		if b, ok := f.(hostBinder); ok {
			b.Bind(h, a.cfg, met)
		}
	}
	return formathost.NewMirrorHost(h, own.engine)
}
