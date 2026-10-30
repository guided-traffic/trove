package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
)

// CacheMetadataBytes is the name of the registered cache of the bytes of metadata
// documents by blob digest (R-REPO-025).
const CacheMetadataBytes = "proxy_metadata_bytes"

// ItemStore is the part of the item store the engine uses; *items.Store implements
// it. The engine commits a fetched artifact through the same commit as an upload
// (R-REPO-025), with the origin fill.
type ItemStore interface {
	Spool(ctx context.Context, body io.Reader, o items.IngestOptions) (*items.Spooled, error)
	Commit(ctx context.Context, spec items.CommitSpec) (items.CommitResult, error)
	Live(ctx context.Context, repo items.RepoRef, path string) (items.Item, bool, error)
}

// BlobStore is the part of the blob store the engine uses; *blob.Store implements
// it. A metadata document is committed as a blob whose reference is its cache entry
// (R-REPO-025: "Metadata documents are cache entries only; their bytes are blobs").
type BlobStore interface {
	Commit(ctx context.Context, sp *blob.Spooled, link blob.LinkFunc) (blob.CommitResult, error)
	Open(ctx context.Context, d blob.Digest, size int64, o blob.OpenOptions) (io.ReadCloser, error)
}

// Clients returns the HTTP client of a remote repository; *upstream.Clients
// implements it.
type Clients interface {
	For(ctx context.Context, r repository.Repository, s repository.RemoteSettings) (*upstream.Client, error)
}

// Notifier attaches a change of a change area to the writing transaction;
// *cluster.Changes implements it.
type Notifier interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Signaler attaches a signal to the transaction, published after the commit;
// *cluster.Signals implements it.
type Signaler interface {
	Notify(tx meta.Tx, area, payload string)
}

// Options configure [New].
type Options struct {
	Runner meta.Runner
	Items  ItemStore
	Blobs  BlobStore
	// Flights is the cross-replica lock; nil is a replica without Valkey, which
	// fetches under its in-process single-flight only.
	Flights cluster.FlightLock
	// Negatives is the Valkey side of the negative cache; nil keeps the L1 alone.
	Negatives cluster.NegativeStore
	// Lookups counts a negative entry the reader refused; *cluster.Valkey. Optional.
	Lookups LookupCounter
	Changes Notifier
	Signals Signaler
	Clients Clients
	// Invalid answers whether an invalidation covers an entry (R-REPO-045); nil
	// treats every entry as valid.
	Invalid invalidation.Matcher
	Caches  *cache.Registry
	// Replicas is the number of live replicas that serve client traffic, the n of
	// R-REPO-040; nil is 1.
	Replicas func() int
	// GlobalOffline says that every remote repository is offline (R-REPO-031); nil
	// reads proxy.global_offline.
	GlobalOffline func() bool
	Audit         audit.Recorder
	Config        config.Proxy
	// SafetyTTL bounds the age of the per-repository state cache
	// (cache.snapshot_safety_ttl); zero is five minutes.
	SafetyTTL time.Duration
	Metrics   prometheus.Registerer
	Log       *slog.Logger
	// Now replaces the clock of the replica, for tests.
	Now func() time.Time
}

// Engine is the remote engine of one replica.
type Engine struct {
	o     Options
	log   *slog.Logger
	m     *metrics
	slots *slots
	neg   *negatives

	state   *cache.Cache[int64, *repoState]
	mdBytes *cache.Cache[[32]byte, []byte]

	base   context.Context
	cancel context.CancelFunc
	bg     sync.WaitGroup

	draining atomic.Bool

	fmu     sync.Mutex
	flights map[string]*flight

	keys sync.Map // repository id -> key, for the gauge of repositories no request touched

	pmu     sync.Mutex
	probes  map[int64]*atomic.Bool
	ensured map[int64]bool

	tmu       sync.Mutex
	throttled map[string]time.Time
}

// New creates the engine and registers its caches (R-ARCH-032): the per-repository
// state, the negative L1 and the bytes of documents by digest.
func New(o Options) (*Engine, error) {
	switch {
	case o.Runner == nil:
		return nil, errors.New("proxy: no transaction runner")
	case o.Items == nil:
		return nil, errors.New("proxy: no item store")
	case o.Blobs == nil:
		return nil, errors.New("proxy: no blob store")
	case o.Clients == nil:
		return nil, errors.New("proxy: no upstream clients")
	case o.Caches == nil:
		return nil, errors.New("proxy: no cache registry")
	}
	if o.Audit == nil {
		o.Audit = audit.Nop{}
	}
	if o.Flights == nil {
		o.Flights = cluster.NewFlightLock(nil, cluster.FlightLockOptions{})
	}
	if o.SafetyTTL <= 0 {
		o.SafetyTTL = 5 * time.Minute
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.GlobalOffline == nil {
		off := o.Config.GlobalOffline
		o.GlobalOffline = func() bool { return off }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	e := &Engine{
		o: o, log: o.Log, m: newMetrics(o.Metrics),
		slots:   newSlots(o.Config.MaxConcurrentFetches, o.Replicas),
		flights: map[string]*flight{}, probes: map[int64]*atomic.Bool{}, ensured: map[int64]bool{},
		throttled: map[string]time.Time{},
	}
	e.base, e.cancel = context.WithCancel(context.Background())
	e.state = cache.Register(o.Caches, CacheState, cache.Options[int64, *repoState]{
		MaxEntries: 4096, SafetyTTL: o.SafetyTTL, Areas: []string{cluster.AreaUpstreamState},
		OnChange: e.onStateChange, OnFlush: func(string) { e.reloadAll() },
	})
	e.neg = &negatives{
		l1: cache.Register(o.Caches, CacheNegativeL1, cache.Options[negKey, negEntry]{
			MaxEntries: o.Config.NegativeL1Entries, SafetyTTL: negativeLifetime,
		}),
		store: o.Negatives, inv: o.Invalid, counter: o.Lookups, now: o.Now,
	}
	if o.Config.MetadataCacheBytes > 0 {
		e.mdBytes = cache.Register(o.Caches, CacheMetadataBytes, cache.Options[[32]byte, []byte]{
			MaxBytes: int64(o.Config.MetadataCacheBytes), Weigh: func(_ [32]byte, b []byte) int64 { return int64(len(b)) },
			SafetyTTL: 24 * time.Hour, // content-addressed: it cannot go stale, the bound is the rule's
		})
	}
	return e, nil
}

func (e *Engine) now() time.Time { return e.o.Now() }

// Drain is the first act of the shutdown (R-ARCH-024 step 1): from now on this replica
// leads no new cross-replica flight (R-REPO-026, Q-REPO-7). It joins a flight that
// another replica holds and, when none does, fetches under its own single-flight.
func (e *Engine) Drain(context.Context) { e.draining.Store(true) }

// Quiesce cancels the fetches that are still running 5 s before the shutdown
// deadline (R-ARCH-024 step 4): each flight's holder releases with failed, so the
// waiters on other replicas take over at once. A cancelled fetch leaves no entry; its
// spool is the pod's own scratch space. It returns when the flights have ended or ctx
// does.
func (e *Engine) Quiesce(ctx context.Context) {
	e.cancel()
	for {
		e.fmu.Lock()
		n := len(e.flights)
		e.fmu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Name, Start and Stop make the engine a component of the composition root.
func (e *Engine) Name() string { return "proxy" }

// Start does nothing: the engine's state is filled by its first requests.
func (e *Engine) Start(context.Context) error { return nil }

// Stop ends the background reloads.
func (e *Engine) Stop(ctx context.Context) error {
	e.cancel()
	done := make(chan struct{})
	go func() { e.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// Draining reports whether Drain was called.
func (e *Engine) Draining() bool { return e.draining.Load() }

// validate checks what the engine relies on in a request.
func (e *Engine) validate(rq Request, kind Kind) error {
	switch {
	case rq.Remote.id == 0 || rq.Adapter == nil || rq.Kind != kind:
		return errs.E(errs.Internal, errs.CodeInternal, "proxy: the request is not complete")
	case rq.Remote.Repo.Disabled:
		return errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The repository is disabled.")
	}
	return nil
}

// offline reports the offline mode of the repository or the global switch
// (R-REPO-030, R-REPO-031).
func (e *Engine) offline(rem Remote) bool {
	return rem.Settings.Availability.Mode == repository.AvailabilityOffline || e.o.GlobalOffline()
}

// mayFill asks the populate check at step 9 (R-AUTHZ-008). A refusal on an endpoint
// that does not fill caches is counted (R-EP-019).
func (e *Engine) mayFill(ctx context.Context, rq Request) bool {
	if rq.Populate != nil && rq.Populate.May(ctx) {
		return true
	}
	if rq.Populate != nil {
		if name, fills := rq.Populate.Endpoint(); name != "" && !fills {
			e.m.fillRefused.WithLabelValues(name).Inc()
		}
	}
	return false
}

// denyFill is the refusal of a request that has nothing to serve: 403
// populate_not_permitted, audited by the populate check (R-AUTHZ-008). No negative
// entry is written.
func (e *Engine) denyFill(ctx context.Context, rq Request) error {
	if rq.Populate != nil {
		if err := rq.Populate.Deny(ctx); err != nil {
			return err
		}
	}
	return errs.E(errs.Forbidden, errs.CodePopulateNotPermitted,
		"The caller may read "+rq.Remote.Repo.Key+" but not bring new content into its cache.")
}

func notStored(m Mode) error {
	if m == OnlyIfCached {
		return errs.E(errs.UpstreamTimeout, errs.CodeNotCachedOnlyIfCached, "The item is not cached and the request asked for a cached response only.")
	}
	return errs.E(errs.NotFound, errs.CodeNotFound, "The item is not in the cache.")
}

func offlineMiss(rem Remote) error {
	return errs.E(errs.NotFound, errs.CodeNotCachedOffline,
		rem.Repo.Key+" is offline and the item is not in its cache.")
}

func assumedOffline(retry time.Duration) *errs.Error {
	return errs.E(errs.Unavailable, errs.CodeUpstreamAssumedOffline,
		"The upstream is assumed to be offline after repeated failures and the item is not cached.", errs.WithRetryAfter(max(retry, time.Second)))
}

// withStatus adds the Cache-Status header to the answer of an error (R-REPO-047:
// every response of a remote repository carries it).
func withStatus(err error, cs CacheStatus) error {
	ee, ok := errs.As(err)
	if !ok {
		return err
	}
	c := *ee
	c.Headers = maps.Clone(ee.Headers)
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	c.Headers["Cache-Status"] = cs.Header()
	return &c
}

// event builds an audit event of the engine for a request.
func (e *Engine) event(rq Request, typ string, details map[string]any) audit.Event {
	rem := rq.Remote
	return audit.Event{
		Type: typ, Actor: auditActor(rq.Caller), Outcome: audit.Failure, Source: rq.Caller.Source,
		Subject:    audit.Subject{Kind: "repository", ID: rem.Repo.ID, Name: rem.Repo.Key},
		Repository: rem.Repo.Key, Path: rq.Path, Endpoint: rq.Caller.Endpoint, Details: mustJSON(details),
	}
}

// securityEvent writes a security event about a broken upstream at most once a
// minute per repository, kind and host: a broken upstream fails every request.
func (e *Engine) securityEvent(ctx context.Context, rq Request, typ, host string, details map[string]string) {
	if !e.throttle(rq.Remote.Repo.Key+"\x00"+typ+"\x00"+host, time.Minute) {
		return
	}
	d := make(map[string]any, len(details))
	for k, v := range details {
		d[k] = v
	}
	e.o.Audit.Record(ctx, e.event(rq, typ, d))
}

// throttle reports whether key has not been seen within ttl, and marks it seen.
func (e *Engine) throttle(key string, ttl time.Duration) bool {
	now := e.now()
	e.tmu.Lock()
	defer e.tmu.Unlock()
	if t, ok := e.throttled[key]; ok && now.Sub(t) < ttl {
		return false
	}
	if len(e.throttled) > 4096 {
		for k, t := range e.throttled {
			if now.Sub(t) >= ttl {
				delete(e.throttled, k)
			}
		}
	}
	e.throttled[key] = now
	return true
}

// EntryState reads what the cache entries of a path say, any variant: stored, and
// marked gone (R-REPO-048 steps 1 and 2). It never contacts the upstream.
func (e *Engine) EntryState(ctx context.Context, repoID int64, path string) (EntryState, error) {
	return e.entryState(ctx, repoID, path)
}

// maxCachedArtifacts bounds the list of CachedArtifacts.
const maxCachedArtifacts = 100000

// CachedArtifacts lists the cached artifact paths below a prefix, the cached
// tarballs of one package for the merge tiers of a virtual repository. Entries
// marked gone are not artifacts' marks; the caller applies the gone rules.
func (e *Engine) CachedArtifacts(ctx context.Context, repoID int64, prefix string) ([]string, error) {
	return e.cachedArtifacts(ctx, repoID, prefix, maxCachedArtifacts)
}

// goneGate applies the gone-package rule (R-REPO-029, R-REPO-025 "Gone packages") to
// a request that does not ask for the package document itself. It returns the 404 to
// answer, or nil when the request goes on. A format that tracks removal per version
// has its own rule and is skipped.
func (e *Engine) goneGate(ctx context.Context, rq Request) (*errs.Error, error) {
	docPath, variant, gone, err := e.goneDocument(ctx, rq)
	if err != nil || !gone {
		return nil, err
	}
	notFound := errs.E(errs.NotFound, errs.CodeNotFound, "The package is not available.")
	if rq.Mode != Normal {
		return notFound, nil // the cache view and only-if-cached never refresh
	}
	// The mark heals itself for clients that request artifacts without documents: when
	// the package document has no valid negative entry, refresh it first and go on if
	// the upstream serves it again; any other outcome is 404.
	rem := rq.Remote
	if _, hit := e.neg.lookup(ctx, rem, docPath, variant); hit {
		return notFound, nil
	}
	doc := rq
	doc.Path, doc.Variant, doc.Kind, doc.Mode = docPath, variant, Metadata, Normal
	doc.Expected, doc.Serve = Digests{}, nil
	d, err := e.metadata(ctx, doc, time.Time{})
	if err != nil || d == nil || d.Status.Negative || !d.GoneAt.IsZero() {
		return notFound, nil
	}
	return nil, nil
}

// packageGone reports whether the request names a package marked gone, for the
// callers that never refresh.
func (e *Engine) packageGone(ctx context.Context, rq Request) (bool, error) {
	_, _, gone, err := e.goneDocument(ctx, rq)
	return gone, err
}

// goneDocument finds the package document of the request's package in the gone set
// of the repository.
func (e *Engine) goneDocument(ctx context.Context, rq Request) (docPath, variant string, gone bool, err error) {
	if rq.Package == "" || rq.Adapter == nil || rq.Adapter.TracksVersionRemoval() {
		return "", "", false, nil
	}
	docPath, ok := rq.Adapter.PackageDocumentPath(rq.Package)
	if !ok || docPath == rq.Path {
		return "", "", false, nil
	}
	st, err := e.stateOf(ctx, rq.Remote)
	if err != nil {
		return "", "", false, err
	}
	variant, gone = st.gone[docPath]
	return docPath, variant, gone, nil
}

// Peek is what the explanation of a resolution shows of a remote repository's cache for a
// path, read without contacting the upstream (R-REPO-019): a current negative entry (the
// L1, then Valkey; an unavailable Valkey means none), the offline mode, and how long the
// circuit stays open.
type Peek struct {
	Negative       bool
	Offline        bool
	CircuitOpenFor time.Duration
}

// Peek reads the three facts. It writes nothing and asks nobody.
func (e *Engine) Peek(ctx context.Context, rem Remote, path, variant string) (Peek, error) {
	out := Peek{Offline: e.offline(rem)}
	open, retry, err := e.circuitPeek(ctx, rem)
	if err != nil {
		return Peek{}, err
	}
	if open {
		out.CircuitOpenFor = retry
	}
	_, out.Negative = e.neg.lookup(ctx, rem, path, variant)
	return out, nil
}
