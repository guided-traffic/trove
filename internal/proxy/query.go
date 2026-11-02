package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
)

// CacheQueryResults is the name of the registered cache of pass-through answers
// (R-ARCH-013 step 6, R-ARCH-032): the upstream's response to a query, kept for the period the
// caller asked for. It lives in process, per replica. The key carries the repository's
// configuration version, so a change of the configuration (area repo_config) drops the entries
// of that repository's older versions only, and a deletion all of them (Q-NUGET-94).
const CacheQueryResults = "proxy_query_results"

// maxQueryTTL bounds how long a pass-through answer is kept, whatever the caller asks: it is the
// SafetyTTL of the cache (R-ARCH-018).
const maxQueryTTL = time.Hour

// QueryRequest is one pass-through request of a `Volatile` path (R-ARCH-013 step 6): a request
// whose answer is stored neither as a cache entry nor as an item, such as a package search. The
// engine sends it through the repository's client, with the egress rules, the trust store, the
// redirect rules and the credential policy of a fetch, under the repository's fetch slots and
// circuit, and keeps the answer in a per-replica cache for a bounded period.
type QueryRequest struct {
	Remote Remote
	// Path is the canonical repository path of the query. It names the request in the audit
	// events and is handed to Accept.
	Path string
	// Build maps the query to the upstream request. The URL it builds comes from [UpstreamBase];
	// like a fetch's it must be on the upstream's host or on an extraHosts entry. The request has
	// no body: the answer is cached by method and URL, so a body would not be part of the key.
	Build func(ctx context.Context, b UpstreamBase) (*http.Request, error)
	// Accept checks the status and headers of a 2xx answer before a byte is read; nil accepts.
	Accept func(path string, resp *http.Response) error
	// TTL is how long the answer is kept; 0 keeps nothing. It is cut to one hour.
	TTL time.Duration
	// MaxBytes bounds the answer below the repository's and the system's metadata limit; 0 keeps
	// them.
	MaxBytes int64
	// Mode says whether the upstream may be contacted: the cache view and only-if-cached never
	// do.
	Mode Mode
	// Populate is the populate check of R-AUTHZ-008: a caller who may not bring content into the
	// repository does not make the engine contact the upstream either, and is served from the
	// cache only. A nil Populate refuses.
	Populate Populate
	Caller   CallerFacts
}

// QueryResponse is the upstream's answer to a query.
type QueryResponse struct {
	// ContentType is the Content-Type of the upstream's answer.
	ContentType string
	// Body is the answer as received. It is shared with the cache: a caller must not change it.
	Body []byte
	// FetchedAt is when the upstream answered.
	FetchedAt time.Time
	// Cached says that the answer came from this replica's cache.
	Cached bool
	// Collapsed says that the answer is the result of another request's contact of the upstream.
	Collapsed bool
}

type queryKey struct {
	repo, version int64
	url           string
}

type queryEntry struct {
	resp    QueryResponse
	expires time.Time
}

// queryCall is one contact of the upstream that identical requests of this replica wait for.
type queryCall struct {
	done chan struct{}
	res  *QueryResponse
	err  error
	// abandoned: the leader's own request ended before the contact did, so its error says
	// nothing about the upstream and the waiters ask again.
	abandoned bool
}

// registerQueries creates the cache of pass-through answers; a size of 0 keeps none.
//
// A replica applies a change of repo_config twice, at once and again with the change-log poll
// (R-ARCH-018), and the key holds the configuration version a request read, so an entry is only
// ever served under the configuration it was fetched with. A change therefore drops the entries
// of the repository's versions below the one the change produced, which an entry fetched after
// the first application survives at the second (Q-NUGET-94). A deletion, and a payload without a
// version (an older replica's), drop every entry of the repository; an unreadable payload flushes
// the cache.
func (e *Engine) registerQueries() {
	if e.o.Config.QueryCacheBytes <= 0 {
		return
	}
	e.queries = cache.Register(e.o.Caches, CacheQueryResults, cache.Options[queryKey, *queryEntry]{
		MaxBytes: int64(e.o.Config.QueryCacheBytes),
		Weigh: func(k queryKey, v *queryEntry) int64 {
			return int64(len(v.resp.Body)+len(k.url)+len(v.resp.ContentType)) + 128
		},
		SafetyTTL: maxQueryTTL, Areas: []string{cluster.AreaRepoConfig},
		OnChange: func(c *cache.Cache[queryKey, *queryEntry], _ string, _ int, payload json.RawMessage) {
			id, older, ok := repository.ChangeEviction(payload)
			if !ok {
				c.Flush(cache.ReasonChange)
				return
			}
			c.InvalidateFunc(func(k queryKey) bool { return k.repo == id && older(k.version) })
		},
	})
}

// Query sends a pass-through request to the upstream of a remote repository and returns its
// answer (R-ARCH-013 step 6).
//
//   - A current copy in the replica's cache is returned without contacting the upstream, to any
//     caller and whatever the offline mode says: the copy is the upstream's answer as received,
//     and what a caller may see of it is for the format to filter.
//   - Without one, a repository that may not contact its upstream (offline, the global switch,
//     the cache view, only-if-cached, a caller who may not fill) answers [ErrOffline] and sends
//     nothing.
//   - Otherwise the request takes a fetch slot, is refused while the circuit is open (503
//     upstream_assumed_offline) and is sent through the repository's client. Identical queries
//     of the replica wait for one contact. A 2xx answer counts toward the circuit as a success,
//     a failure as a fetch's does; a 404 or 410 is a missing resource that counts for nothing,
//     and no negative entry is written.
//   - A 2xx answer is kept for rq.TTL under the method and URL of the upstream request. It is
//     kept as received, so a format that filters or rewrites it does so on every use.
func (e *Engine) Query(ctx context.Context, rq QueryRequest) (*QueryResponse, error) {
	rem := rq.Remote
	switch {
	case rem.id == 0 || rq.Build == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "proxy: the query is not complete")
	case rem.Repo.Disabled:
		return nil, errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The repository is disabled.")
	}
	cl, err := e.o.Clients.For(ctx, rem.Repo, rem.Settings)
	if err != nil {
		return nil, err
	}
	req, err := rq.Build(ctx, UpstreamBase{URL: cl.Origin()})
	if err != nil {
		return nil, err
	}
	if err := cl.Allowed(req.URL); err != nil {
		return nil, err
	}
	key := queryKey{repo: rem.id, version: rem.Repo.Version, url: req.Method + " " + req.URL.String()}
	ttl := min(rq.TTL, maxQueryTTL)
	if e.queries != nil && ttl > 0 {
		if ent, ok := e.queries.Get(key); ok {
			if e.now().Before(ent.expires) {
				e.m.lookups.WithLabelValues(rem.Repo.Key, "query", resultHit).Inc()
				out := ent.resp
				out.Cached = true
				return &out, nil
			}
			e.queries.Remove(key)
		}
	}
	e.m.lookups.WithLabelValues(rem.Repo.Key, "query", resultMiss).Inc()
	if rq.Mode != Normal || e.offline(rem) || !e.mayContact(ctx, rq.Populate) {
		return nil, ErrOffline
	}
	return e.queryOnce(ctx, rem.Repo.Key, key, func(ctx context.Context) (*QueryResponse, error) {
		keep := e.queries != nil && ttl > 0
		if keep {
			// A leader that ended just before this one began has stored the answer.
			if ent, ok := e.queries.Get(key); ok && e.now().Before(ent.expires) {
				return &ent.resp, nil
			}
		}
		ent, err := e.queryFetch(ctx, rq, cl, req, ttl)
		if err != nil {
			return nil, err
		}
		if keep {
			// Stored without the fill rule of R-ARCH-018: the key holds the configuration version
			// this request read, so the answer is served only under the configuration it was
			// fetched with, and a change of the area during the fetch, of this repository or of
			// another, makes it stale for nobody (Q-NUGET-94).
			e.queries.Set(key, ent)
		}
		return &ent.resp, nil
	})
}

// mayContact asks the populate check as a question and counts a refusal on an endpoint that does
// not fill caches (R-EP-019).
func (e *Engine) mayContact(ctx context.Context, p Populate) bool {
	if p != nil && p.May(ctx) {
		return true
	}
	if p != nil {
		if name, fills := p.Endpoint(); name != "" && !fills {
			e.m.fillRefused.WithLabelValues(name).Inc()
		}
	}
	return false
}

// queryOnce runs fn once for the identical queries in flight on this replica. A request that
// waited is told so; one whose leader ended before the contact did asks again.
func (e *Engine) queryOnce(ctx context.Context, repoKey string, key queryKey, fn func(context.Context) (*QueryResponse, error)) (*QueryResponse, error) {
	for {
		e.qmu.Lock()
		c, joined := e.qcalls[key]
		if !joined {
			c = &queryCall{done: make(chan struct{})}
			e.qcalls[key] = c
		}
		e.qmu.Unlock()
		if !joined {
			res, err := fn(ctx)
			e.qmu.Lock()
			delete(e.qcalls, key)
			e.qmu.Unlock()
			c.res, c.err, c.abandoned = res, err, err != nil && ctx.Err() != nil
			close(c.done)
			return res, err
		}
		select {
		case <-c.done:
		case <-ctx.Done():
			return nil, errs.E(errs.Unavailable, errs.CodeUnavailable, "The request ended before the upstream answered.", errs.WithCause(ctx.Err()))
		}
		if c.abandoned && ctx.Err() == nil {
			continue
		}
		if c.err != nil {
			return nil, c.err
		}
		e.m.collapsed.WithLabelValues(repoKey).Inc()
		out := *c.res
		out.Collapsed = true
		return &out, nil
	}
}

// queryFetch is the contact of the upstream: slot, circuit, request, classification and the
// bounded read of the body.
func (e *Engine) queryFetch(ctx context.Context, rq QueryRequest, cl *upstream.Client, req *http.Request, ttl time.Duration) (*queryEntry, error) {
	rem := rq.Remote
	f := &fetcher{
		e: e, rq: Request{Remote: rem, Path: rq.Path, Kind: Metadata, MaxBytes: rq.MaxBytes, Caller: rq.Caller},
		st: &reqState{role: "query"}, fl: &flight{done: make(chan struct{})}, cl: cl,
	}
	release, wait, err := e.slots.acquire(ctx, rem.id, rem.Settings.Limits.MaxConcurrentFetches, e.o.Config.QueueTimeout())
	e.m.queueWait.WithLabelValues(rem.Repo.Key).Observe(wait.Seconds())
	if err != nil {
		return nil, err
	}
	defer release()
	adm, err := e.circuitAdmit(ctx, rem)
	if err != nil {
		return nil, err
	}
	if adm.open {
		return nil, assumedOffline(adm.retryAfter)
	}
	defer adm.release()

	out := req.Clone(ctx)
	out.Header.Del("Authorization") // the client's hop policy alone adds the credential
	out.Header.Set("Accept-Encoding", "identity")
	f.target = out.URL
	resp, rcancel, idle, err := f.send(ctx, out) //nolint:bodyclose // closed below
	if err != nil {
		return nil, f.transportFailure(ctx, err, false).err
	}
	defer rcancel()
	defer func() { _ = resp.Body.Close() }()
	switch s := resp.StatusCode; {
	case s >= 200 && s < 300:
		return f.queryBody(ctx, rq, resp, idle, ttl)
	case s == http.StatusNotFound || s == http.StatusGone:
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The upstream of "+rem.Repo.Key+" has no such resource.")
	}
	return nil, f.failed(ctx, classifyStatus(rem, resp)).err
}

// queryBody checks and reads the body of a 2xx answer.
func (f *fetcher) queryBody(ctx context.Context, rq QueryRequest, resp *http.Response, idle *atomic.Bool, ttl time.Duration) (*queryEntry, error) {
	rem := rq.Remote
	if rq.Accept != nil {
		if err := rq.Accept(rq.Path, resp); err != nil {
			return nil, asUpstreamError(err)
		}
	}
	if f.contentTypeBlocked(resp) {
		return nil, f.failed(ctx, failure{count: true, status: resp.StatusCode, err: errs.E(errs.UpstreamFailed, errs.CodeUpstreamContentTypeBlocked,
			"The upstream of "+rem.Repo.Key+" answered with a media type that is not expected for the item.")}).err
	}
	lim := f.limit()
	if lim > 0 && resp.ContentLength > lim {
		return nil, tooLarge(rem)
	}
	body, err := io.ReadAll(&limitReader{r: resp.Body, max: lim})
	switch {
	case errors.Is(err, errTooLargeBody):
		return nil, tooLarge(rem)
	case err != nil:
		return nil, f.transportFailure(ctx, err, idle.Load()).err
	}
	f.e.circuitSuccess(ctx, rem)
	now := f.e.now()
	return &queryEntry{
		resp:    QueryResponse{ContentType: resp.Header.Get("Content-Type"), Body: body, FetchedAt: now},
		expires: now.Add(ttl),
	}, nil
}
