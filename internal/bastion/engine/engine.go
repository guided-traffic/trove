// Package engine is the Bastion's request flow (R-BAS-018 to R-BAS-029, R-BAS-042): the classification of a
// request, the credentials, the answers, the fetch on a miss, the artifacts and documents, the negative cache,
// the concurrency budget and the collapsing of misses, the write probe, the unreachable behaviour and the
// Bastion's own responses. It is the one place that knows how the pieces fit; every piece it uses is behind an
// package of its own (mainclient.Client, cache.Cache, guard.Guard, downloads.Buffer, link.Link).
//
// [Deps] lists everything the engine is given, [Engine] is what it hands back. The engine is given the pieces
// the server built and runs (the link, the client with its budget, the cache of package 5, the shared state, the
// guard, the download buffer) and runs only its own janitor; the server runs the loops of the others.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/downloads"
	"github.com/guided-traffic/trove/internal/bastion/guard"
	"github.com/guided-traffic/trove/internal/bastion/link"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
	"github.com/guided-traffic/trove/internal/config"
)

// Deps is everything the engine is given. The server builds it from the configuration and runs the loops of
// the pieces it hands over (the link, the token reloader, the download buffer, the cache sweeper); the engine
// runs only what is its own. It takes no global state.
type Deps struct {
	// Config is the validated configuration of the role.
	Config *config.BastionConfig
	Log    *slog.Logger
	// Metrics is the one set of the Bastion's metrics (internal/bastion/metrics); the engine and every package
	// below it count through it.
	Metrics *metrics.Metrics
	// Replica is the pod name; Release the version of the binary.
	Replica string
	Release string
	// Now is the clock; nil means time.Now. Tests inject one.
	Now func() time.Time

	// Main is the client of the main installation (internal/bastion/mainclient), wrapped by Link.Guard so that
	// a refused token or an interface mismatch in any answer makes the replica unreachable (R-BAS-006,
	// R-BAS-028), and built with the budget (mainclient.SlotBudget): it takes a slot for every call but the
	// handshake and the feed, and holds it until the response body ends or is closed. The engine therefore
	// closes every body on every path.
	Main mainclient.Client
	// Link is the replica's standing connection (internal/bastion/link): Link.State() says reachable or not and
	// since when, anonymousAccess, recordDownloads and the start of the feed history; Link.AnswerState(now) is
	// the answers.State the validity rules are judged against; Link.Feed() is the log of the feed entries. The
	// server runs it; the engine hands it its [link.AnswerSink] (Link.SetSink) in [New].
	Link *link.Link
	// Cache is the cache of package 5: entries, the verified read, the fill, the access markers. The server
	// runs its sweeper.
	Cache *cache.Cache
	// Shared is the state the replicas share, or the local one (package 6).
	Shared *shared.State
	// Guard is the abuse protection (guard.New, package 6); the server builds it with the shared state.
	Guard guard.Guard
	// Downloads is the download event buffer (internal/bastion/downloads); the server runs and drains it and
	// follows recordDownloads of the handshake.
	Downloads downloads.Buffer
}

// Engine is the Bastion's request flow.
type Engine interface {
	// Handler serves every request of the client listener, whatever its path and method: the engine
	// classifies the path itself (R-BAS-018), so the server registers it as the router's fallback.
	http.Handler
	// Start runs the engine's own loop, the janitor of the answers and private copies, until ctx ends or Drain
	// was called. It does not block. The handshake, the feed poll, the token reload, the download buffer and
	// the cache sweep are the server's.
	Start(ctx context.Context) error
	// Drain stops the janitor and waits for the background refreshes of answers that are still running
	// (R-BAS-043); the server calls it after the listeners stopped accepting and the in-flight requests
	// finished or were cut, and then drains the download buffer.
	Drain(ctx context.Context) error
	// Ready reports what the engine contributes to readiness: nil, or an error naming what is missing. It
	// does not depend on the main installation (R-BAS-043, Q-BAS-44); it fails before the cache backend's
	// first check passed.
	Ready(ctx context.Context) error
}

// New builds the engine from its dependencies and hands the link the sink that deletes the answers a feed
// entry matches (R-BAS-026).
func New(d Deps) (Engine, error) {
	if d.Config == nil || d.Metrics == nil || d.Main == nil || d.Link == nil || d.Cache == nil || d.Guard == nil || d.Downloads == nil {
		return nil, errors.New("engine: Config, Metrics, Main, Link, Cache, Guard and Downloads are required")
	}
	e, err := newFlow(d)
	if err != nil {
		return nil, err
	}
	d.Link.SetSink(e.answers)
	return e, nil
}

// engine is the request flow.
type engine struct {
	d       Deps
	cfg     *config.BastionSettings
	log     *slog.Logger
	nowFunc func() time.Time

	baseHost  string
	trusted   []netip.Prefix
	forwarded bool // server.forwarded_header is "forwarded"

	answers  *answerStore
	neg      *lruCache[*negEntry]
	priv     *lruCache[*privDoc]
	vary     *lruCache[[]string]
	volatile *lruCache[time.Time]
	probes   *lruCache[*probeRec]

	checks, probeFlights, fetches *group

	mu       sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  bool
	draining bool
}

const (
	volatileTTL     = time.Minute
	volatileEntries = 10000
	varyEntries     = 20000
	probeEntries    = 20000
)

func newFlow(d Deps) (*engine, error) {
	if d.Shared == nil {
		d.Shared = shared.NewLocal(d.Config.Bastion.Replicas, d.Now)
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	u, err := url.Parse(d.Config.Server.BaseURL)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("engine: server.base_url has no host")
	}
	e := &engine{
		d: d, cfg: &d.Config.Bastion, log: d.Log, nowFunc: d.Now, baseHost: strings.ToLower(u.Hostname()),
		forwarded: d.Config.Server.ForwardedHeader == "forwarded",
		checks:    newGroup(), probeFlights: newGroup(), fetches: newGroup(),
	}
	if e.nowFunc == nil {
		e.nowFunc = time.Now
	}
	for _, s := range d.Config.Server.TrustedProxies {
		if p, err := netip.ParsePrefix(s); err == nil {
			e.trusted = append(e.trusted, p.Masked())
		} else if a, err := netip.ParseAddr(s); err == nil {
			a = a.Unmap()
			e.trusted = append(e.trusted, netip.PrefixFrom(a, a.BitLen()))
		} else {
			return nil, errors.New("engine: server.trusted_proxies entry " + s + " is neither an address nor a CIDR")
		}
	}
	e.answers = newAnswerStore(e)
	e.neg = newLRU[*negEntry](e.cfg.Cache.NegativeMaxEntries, 0, nil)
	e.priv = newLRU[*privDoc](0, int64(e.cfg.Cache.PrivateDocumentsMaxBytes), nil)
	e.vary = newLRU[[]string](varyEntries, 0, nil)
	e.volatile = newLRU[time.Time](volatileEntries, 0, nil)
	e.probes = newLRU[*probeRec](probeEntries, 0, nil)
	return e, nil
}

func (e *engine) now() time.Time { return e.nowFunc() }

// anonymousAccess is the handshake's anonymousAccess (R-BAS-019): an anonymous request is admitted exactly
// while it is true; before the first handshake it is not known and nothing is admitted (Q-BAS-99).
func (e *engine) anonymousAccess() bool {
	st := e.d.Link.State()
	return st.Handshaken && st.AnonymousAccess
}

// Start implements [Engine].
func (e *engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine: already started")
	}
	e.started = true
	ctx, e.cancel = context.WithCancel(ctx)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		t := time.NewTicker(janitorEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.janitor()
			}
		}
	}()
	return nil
}

// janitorEvery is how often the janitor runs.
const janitorEvery = time.Minute

// Drain implements [Engine].
func (e *engine) Drain(ctx context.Context) error {
	e.mu.Lock()
	cancel := e.cancel
	e.draining = true
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// background runs f in a goroutine the engine waits for in Drain; after Drain began it runs nothing and
// reports false.
func (e *engine) background(f func()) bool {
	e.mu.Lock()
	if e.draining {
		e.mu.Unlock()
		return false
	}
	e.wg.Add(1)
	e.mu.Unlock()
	go func() { defer e.wg.Done(); f() }()
	return true
}

// Ready implements [Engine]: the cache backend's first check (R-BAS-043).
func (e *engine) Ready(ctx context.Context) error { return e.d.Cache.Ready(ctx) }

// janitor drops what no answer can use any more (R-BAS-026): answers past period and grace, the feed entries
// older than the longest answer, the registries' expired items.
func (e *engine) janitor() {
	now := e.now()
	e.answers.prune(now)
	e.d.Link.Feed().Prune(now.Add(-(e.cfg.Answers.Period + e.cfg.Answers.GracePeriod + answers.SkewMargin)))
	e.volatile.retain(func(_ string, t time.Time) bool { return now.Before(t) })
	e.probes.retain(func(_ string, p *probeRec) bool { return now.Before(p.until) })
}
