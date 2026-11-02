//go:build integration

package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// The pass-through of a Volatile request (R-ARCH-013 step 6, Q-ARCH-270): a package search that the
// engine sends through the repository's client and keeps, as received, in a per-replica cache.

// searchUpstream serves /search and records the raw query of every request.
type searchUpstream struct {
	mu      sync.Mutex
	queries []string
	n       atomic.Int64
}

func (s *searchUpstream) count() int { return int(s.n.Load()) }

func (s *searchUpstream) queryCount(raw string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, q := range s.queries {
		if q == raw {
			n++
		}
	}
	return n
}

func serveSearch(up *proxytest.Upstream, body string, delay time.Duration) *searchUpstream {
	s := &searchUpstream{}
	up.Set("/search", proxytest.Route{Handler: func(w http.ResponseWriter, r *http.Request) {
		s.n.Add(1)
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.RawQuery)
		s.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}})
	return s
}

// query drives Query as a format's handler would: GET <upstream>/<path>?<params>.
func (r *replica) query(rem proxy.Remote, path string, params url.Values, ttl time.Duration, mutate ...func(*proxy.QueryRequest)) (*proxy.QueryResponse, error) {
	return r.queryCtx(context.Background(), rem, path, params, ttl, mutate...)
}

func (r *replica) queryCtx(ctx context.Context, rem proxy.Remote, path string, params url.Values, ttl time.Duration, mutate ...func(*proxy.QueryRequest)) (*proxy.QueryResponse, error) {
	rq := proxy.QueryRequest{
		Remote: rem, Path: path, TTL: ttl, Populate: proxytest.Allowed(),
		Caller: proxy.CallerFacts{Principal: "it-" + r.name, Endpoint: "default"},
		Build: func(ctx context.Context, b proxy.UpstreamBase) (*http.Request, error) {
			u, err := b.Path(path)
			if err != nil {
				return nil, err
			}
			u.RawQuery = params.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
	}
	for _, m := range mutate {
		m(&rq)
	}
	return r.engine.Query(ctx, rq)
}

func codeOf(err error) string {
	if e, ok := errs.As(err); ok {
		return e.Code
	}
	return ""
}

func lookups(r *replica, repo, result string) float64 {
	return r.metric("trove_remote_cache_lookups_total", "repo", repo, "kind", "query", "result", result)
}

func TestR_ARCH_013_QueryIsKeptPerReplicaForThePeriod(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	s := serveSearch(up, `{"totalHits":3}`, 0)
	rem := e.remote("q-cache", up)
	a, b := e.newReplica("a", replicaOpts{valkey: vkUnreachable}), e.newReplica("b", replicaOpts{valkey: vkUnreachable})
	acme := url.Values{"q": {"acme"}, "take": {"20"}}

	t.Run("a second identical query within the period is the replica's copy and asks nobody", func(t *testing.T) {
		first, err := a.query(rem, "search", acme, time.Minute)
		if err != nil || string(first.Body) != `{"totalHits":3}` || first.ContentType != "application/json" || first.Cached {
			t.Fatalf("first: %+v %v", first, err)
		}
		second, err := a.query(rem, "search", acme, time.Minute)
		if err != nil || !second.Cached || string(second.Body) != string(first.Body) {
			t.Fatalf("second: %+v %v", second, err)
		}
		if s.count() != 1 {
			t.Fatalf("the upstream saw %d requests", s.count())
		}
		if lookups(a, "q-cache", "hit") != 1 || lookups(a, "q-cache", "miss") != 1 {
			t.Fatalf("hit %v miss %v", lookups(a, "q-cache", "hit"), lookups(a, "q-cache", "miss"))
		}
	})
	t.Run("INV-18 another replica holds no copy and asks the upstream itself", func(t *testing.T) {
		got, err := b.query(rem, "search", acme, time.Minute)
		if err != nil || got.Cached || s.count() != 2 {
			t.Fatalf("%+v %v after %d requests", got, err, s.count())
		}
	})
	t.Run("other parameters are another entry", func(t *testing.T) {
		if got, err := a.query(rem, "search", url.Values{"q": {"acme"}, "take": {"40"}}, time.Minute); err != nil || got.Cached {
			t.Fatalf("%+v %v", got, err)
		}
		if s.queryCount("q=acme&take=40") != 1 {
			t.Fatalf("queries: %v", s.queries)
		}
	})
	t.Run("a copy older than the period is not served", func(t *testing.T) {
		short := url.Values{"q": {"short"}}
		if _, err := a.query(rem, "search", short, 300*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(450 * time.Millisecond)
		got, err := a.query(rem, "search", short, 300*time.Millisecond)
		if err != nil || got.Cached || s.queryCount("q=short") != 2 {
			t.Fatalf("%+v %v after %d", got, err, s.queryCount("q=short"))
		}
	})
	t.Run("a period of 0 keeps nothing", func(t *testing.T) {
		none := url.Values{"q": {"none"}}
		for range 2 {
			if got, err := a.query(rem, "search", none, 0); err != nil || got.Cached {
				t.Fatalf("%+v %v", got, err)
			}
		}
		if s.queryCount("q=none") != 2 {
			t.Fatalf("the upstream saw %d", s.queryCount("q=none"))
		}
	})
}

func TestR_ARCH_013_IdenticalQueriesWaitForOneContact(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	s := serveSearch(up, `{}`, 400*time.Millisecond)
	rem := e.remote("q-collapse", up)
	r := e.newReplica("a", replicaOpts{valkey: vkUnreachable})
	var wg sync.WaitGroup
	var collapsed atomic.Int64
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.query(rem, "search", url.Values{"q": {"same"}}, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if got.Collapsed {
				collapsed.Add(1)
			}
		}()
	}
	wg.Wait()
	if s.count() != 1 {
		t.Fatalf("the upstream saw %d requests for 20 identical queries", s.count())
	}
	if collapsed.Load() == 0 || r.metric("trove_remote_collapsed_requests_total", "repo", "q-collapse") == 0 {
		t.Fatalf("no query was told it waited: %d", collapsed.Load())
	}

	t.Run("a leader whose request ended does not fail the queries that wait", func(t *testing.T) {
		s2 := serveSearch(up, `{"late":true}`, 600*time.Millisecond)
		ctx, cancel := context.WithCancel(context.Background())
		led := make(chan error, 1)
		go func() {
			_, err := r.queryCtx(ctx, rem, "search", url.Values{"q": {"abandoned"}}, time.Minute)
			led <- err
		}()
		waitFor(t, "the leader to reach the upstream", 5*time.Second, func() bool { return s2.count() == 1 })
		waited := make(chan error, 1)
		go func() {
			_, err := r.query(rem, "search", url.Values{"q": {"abandoned"}}, time.Minute)
			waited <- err
		}()
		time.Sleep(100 * time.Millisecond)
		cancel()
		if err := <-waited; err != nil {
			t.Fatalf("the waiter failed with its leader: %v", err)
		}
		<-led
	})
}

func TestR_ARCH_013_QueryContactsTheUpstreamOnlyWhereAFetchMay(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	s := serveSearch(up, `{"ok":true}`, 0)
	rem := e.remote("q-may", up)
	r := e.newReplica("a", replicaOpts{valkey: vkUnreachable})
	if _, err := r.query(rem, "search", url.Values{"q": {"warm"}}, time.Minute); err != nil {
		t.Fatal(err)
	}
	base := s.count()

	t.Run("R-REPO-032 the cache view serves a copy and sends nothing for a miss", func(t *testing.T) {
		view := func(q *proxy.QueryRequest) { q.Mode = proxy.CacheView }
		if got, err := r.query(rem, "search", url.Values{"q": {"warm"}}, time.Minute, view); err != nil || !got.Cached {
			t.Fatalf("%+v %v", got, err)
		}
		if _, err := r.query(rem, "search", url.Values{"q": {"cold"}}, time.Minute, view); !errors.Is(err, proxy.ErrOffline) {
			t.Fatalf("err = %v", err)
		}
		if _, err := r.query(rem, "search", url.Values{"q": {"cold"}}, time.Minute, func(q *proxy.QueryRequest) { q.Mode = proxy.OnlyIfCached }); !errors.Is(err, proxy.ErrOffline) {
			t.Fatalf("only-if-cached: %v", err)
		}
		if s.count() != base {
			t.Fatalf("%d requests left", s.count()-base)
		}
	})
	t.Run("R-AUTHZ-008 a caller who may not fill is served a copy and sends nothing for a miss", func(t *testing.T) {
		readOnly := func(q *proxy.QueryRequest) { q.Populate = &proxytest.Populate{Name: "mirror", Fills: false} }
		if got, err := r.query(rem, "search", url.Values{"q": {"warm"}}, time.Minute, readOnly); err != nil || !got.Cached {
			t.Fatalf("%+v %v", got, err)
		}
		if _, err := r.query(rem, "search", url.Values{"q": {"cold"}}, time.Minute, readOnly); !errors.Is(err, proxy.ErrOffline) {
			t.Fatalf("err = %v", err)
		}
		if _, err := r.query(rem, "search", url.Values{"q": {"cold"}}, time.Minute, func(q *proxy.QueryRequest) { q.Populate = nil }); !errors.Is(err, proxy.ErrOffline) {
			t.Fatalf("a nil populate check allowed a contact: %v", err)
		}
		if s.count() != base {
			t.Fatalf("%d requests left", s.count()-base)
		}
		if r.metric("trove_endpoint_cache_fill_refused_total", "endpoint", "mirror") != 1 {
			t.Fatalf("the refusal on an endpoint that does not fill was not counted")
		}
	})
	t.Run("R-REPO-030 an offline repository sends nothing", func(t *testing.T) {
		off := e.remote("q-off", up, func(st *repository.RemoteSettings) { st.Availability.Mode = repository.AvailabilityOffline })
		if _, err := r.query(off, "search", url.Values{"q": {"warm"}}, time.Minute); !errors.Is(err, proxy.ErrOffline) {
			t.Fatalf("err = %v", err)
		}
		if s.count() != base {
			t.Fatalf("%d requests left", s.count()-base)
		}
	})
	t.Run("R-REPO-031 proxy.global_offline sends nothing", func(t *testing.T) {
		g := e.newReplica("g", replicaOpts{valkey: vkUnreachable, proxy: func(p *config.Proxy) { p.GlobalOffline = true }})
		if _, err := g.query(rem, "search", url.Values{"q": {"warm"}}, time.Minute); !errors.Is(err, proxy.ErrOffline) {
			t.Fatalf("err = %v", err)
		}
		if s.count() != base {
			t.Fatalf("%d requests left", s.count()-base)
		}
	})
	t.Run("a disabled repository answers 404 repository_disabled", func(t *testing.T) {
		dis := rem
		dis.Repo.Disabled = true
		if _, err := r.query(dis, "search", url.Values{"q": {"warm"}}, time.Minute); codeOf(err) != errs.CodeRepositoryDisabled {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_ARCH_013_QueryMeetsTheCircuitAndClassifiesLikeAFetch(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	s := serveSearch(up, `{"ok":true}`, 0)
	up.Set("/broken", proxytest.Route{Status: http.StatusInternalServerError})
	up.Set("/gone", proxytest.Route{Status: http.StatusNotFound})
	up.Set("/denied", proxytest.Route{Status: http.StatusForbidden})
	up.Set("/page", proxytest.Route{Body: []byte("<html></html>"), Type: "text/html"})
	up.Set("/big", proxytest.Route{Body: []byte(strings.Repeat("x", 4096)), Type: "application/json"})
	rem := e.remote("q-circuit", up, func(st *repository.RemoteSettings) {
		st.Availability.FailureThreshold, st.Availability.CircuitOpenSeconds = 2, 60
	})
	r := e.newReplica("a", replicaOpts{valkey: vkUnreachable})
	if _, err := r.query(rem, "search", url.Values{"q": {"warm"}}, time.Minute); err != nil {
		t.Fatal(err)
	}

	t.Run("R-REPO-029 a 404 is a missing resource that counts for nothing and leaves no negative entry", func(t *testing.T) {
		for range 3 {
			if _, err := r.query(rem, "gone", nil, time.Minute); errs.KindOf(err) != errs.NotFound {
				t.Fatalf("err = %v", err)
			}
		}
		if up.Count("GET", "/gone") != 3 {
			t.Fatalf("the upstream saw %d requests: a negative entry was written", up.Count("GET", "/gone"))
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries"); n != 0 {
			t.Fatalf("the pass-through stored %d cache entries", n)
		}
	})
	t.Run("R-REPO-029 a 403 of the upstream is 502 upstream_auth_failed and not a 403", func(t *testing.T) {
		if _, err := r.query(rem, "denied", nil, time.Minute); codeOf(err) != errs.CodeUpstreamAuthFailed || errs.KindOf(err) != errs.UpstreamFailed {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-REPO-042 an unexpected media type is refused", func(t *testing.T) {
		if _, err := r.query(rem, "page", nil, time.Minute); codeOf(err) != errs.CodeUpstreamContentTypeBlocked {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-REPO-040 an answer above the limit is refused", func(t *testing.T) {
		if _, err := r.query(rem, "big", nil, time.Minute, func(q *proxy.QueryRequest) { q.MaxBytes = 1024 }); codeOf(err) != errs.CodeUpstreamItemTooLarge {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the format's check of the response is the answer", func(t *testing.T) {
		reject := func(q *proxy.QueryRequest) {
			q.Accept = func(string, *http.Response) error {
				return errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "not a search answer")
			}
		}
		if _, err := r.query(rem, "search", url.Values{"q": {"rejected"}}, time.Minute, reject); codeOf(err) != errs.CodeUpstreamError {
			t.Fatalf("err = %v", err)
		}
		if got, err := r.query(rem, "search", url.Values{"q": {"rejected"}}, time.Minute); err != nil || got.Cached {
			t.Fatalf("a refused answer was kept: %+v %v", got, err)
		}
	})
	t.Run("R-REPO-029 failures open the circuit; then no request leaves, and a current copy is still served", func(t *testing.T) {
		for i := range 2 {
			if _, err := r.query(rem, "broken", nil, time.Minute); codeOf(err) != errs.CodeUpstreamError {
				t.Fatalf("failure %d: %v", i+1, err)
			}
		}
		waitFor(t, "the circuit to open", 3*time.Second, func() bool {
			return r.metric("trove_remote_upstream_up", "repo", "q-circuit") == 0
		})
		before := s.count() + up.Count("GET", "/broken")
		_, err := r.query(rem, "search", url.Values{"q": {"during-outage"}}, time.Minute)
		if codeOf(err) != errs.CodeUpstreamAssumedOffline {
			t.Fatalf("err = %v", err)
		}
		if got, err := r.query(rem, "search", url.Values{"q": {"warm"}}, time.Minute); err != nil || !got.Cached {
			t.Fatalf("a current copy was refused while the circuit is open: %+v %v", got, err)
		}
		if s.count()+up.Count("GET", "/broken") != before {
			t.Fatal("a request left while the circuit was open")
		}
	})
}

// bearerSource gives every repository the same environment reference as its password.
type bearerSource struct{}

func (bearerSource) Secrets(context.Context, int64) ([]repository.StoredSecret, error) {
	return []repository.StoredSecret{{Field: "/remote/password", Ref: &repository.SecretRef{Env: "TROVE_CAC_IT_QUERY_TOKEN"}}}, nil
}

func TestR_REPO_033_QueryNeverSendsTheCredentialToAHostTheRemoteDoesNotSendItTo(t *testing.T) {
	e := newEnv(t)
	up, other := e.upstream(), e.upstream()
	serveSearch(up, `{"from":"upstream"}`, 0)
	serveSearch(other, `{"from":"other"}`, 0)
	opts := replicaOpts{
		valkey: vkUnreachable, ups: []*proxytest.Upstream{other}, secrets: bearerSource{},
		env: func(k string) (string, bool) { return "s3cret", k == "TROVE_CAC_IT_QUERY_TOKEN" },
	}
	r := e.newReplica("a", opts)
	towards := func(target *proxytest.Upstream) func(*proxy.QueryRequest) {
		return func(q *proxy.QueryRequest) {
			q.Build = func(ctx context.Context, _ proxy.UpstreamBase) (*http.Request, error) {
				return http.NewRequestWithContext(ctx, http.MethodGet, target.URL+"/search?q=x", nil)
			}
		}
	}
	bearer := func(st *repository.RemoteSettings) {
		st.AuthMode, st.Password = repository.AuthBearer, &repository.SecretField{Set: true}
	}
	authOf := func(u *proxytest.Upstream) string {
		seen := u.Seen()
		if len(seen) == 0 {
			t.Fatal("the upstream saw no request")
		}
		return seen[len(seen)-1].Authorization
	}

	t.Run("the upstream of the repository gets the credential", func(t *testing.T) {
		rem := e.remote("q-cred", up, bearer)
		if _, err := r.query(rem, "search", url.Values{"q": {"x"}}, 0); err != nil {
			t.Fatal(err)
		}
		if got := authOf(up); got != "Bearer s3cret" {
			t.Fatalf("Authorization = %q", got)
		}
	})
	t.Run("R-REPO-043 a host that is neither the upstream nor in extraHosts is refused without a request", func(t *testing.T) {
		rem := e.remote("q-foreign", up, bearer)
		if _, err := r.query(rem, "search", nil, 0, towards(other)); codeOf(err) != errs.CodeForeignHostDenied {
			t.Fatalf("err = %v", err)
		}
		if other.Total() != 0 {
			t.Fatalf("the foreign host got %d requests", other.Total())
		}
	})
	t.Run("R-REPO-033 a host of extraHosts without sendCredentials gets the request and no credential", func(t *testing.T) {
		rem := e.remote("q-extra", up, bearer, func(st *repository.RemoteSettings) {
			st.ExtraHosts = []repository.ExtraHost{{Pattern: "127.0.0.1"}}
		})
		if got, err := r.query(rem, "search", nil, 0, towards(other)); err != nil || !strings.Contains(string(got.Body), "other") {
			t.Fatalf("%+v %v", got, err)
		}
		if got := authOf(other); got != "" {
			t.Fatalf("the credential went to a host it was not meant for: %q", got)
		}
	})
	t.Run("R-REPO-033 a host of extraHosts with sendCredentials gets it", func(t *testing.T) {
		other.Reset()
		rem := e.remote("q-extra-cred", up, bearer, func(st *repository.RemoteSettings) {
			st.ExtraHosts = []repository.ExtraHost{{Pattern: "127.0.0.1", SendCredentials: true}}
		})
		if _, err := r.query(rem, "search", nil, 0, towards(other)); err != nil {
			t.Fatal(err)
		}
		if got := authOf(other); got != "Bearer s3cret" {
			t.Fatalf("Authorization = %q", got)
		}
	})
	t.Run("the credential of a caller never reaches the upstream", func(t *testing.T) {
		rem := e.remote("q-caller", up)
		set := func(q *proxy.QueryRequest) {
			build := q.Build
			q.Build = func(ctx context.Context, b proxy.UpstreamBase) (*http.Request, error) {
				req, err := build(ctx, b)
				if err == nil {
					req.Header.Set("Authorization", "Bearer from-the-caller")
				}
				return req, err
			}
		}
		if _, err := r.query(rem, "search", url.Values{"q": {"y"}}, 0, set); err != nil {
			t.Fatal(err)
		}
		if got := authOf(up); got != "" {
			t.Fatalf("Authorization = %q", got)
		}
	})
}

func TestR_ARCH_013_AChangeOfTheRepositoryFlushesItsCopies(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	s := serveSearch(up, `{}`, 0)
	rem := e.remote("q-flush", up)
	other := e.remote("q-keep", up)
	r := e.newReplica("a", replicaOpts{valkey: vkUnreachable})
	for _, rr := range []proxy.Remote{rem, other} {
		if _, err := r.query(rr, "search", url.Values{"q": {"x"}}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	entries := func() float64 { return r.metric("trove_cache_entries", "cache", proxy.CacheQueryResults) }
	if entries() != 2 {
		t.Fatalf("entries = %v", entries())
	}
	// A payload without a version, as an older replica writes it during a rolling upgrade, drops
	// every copy of the repository (Q-NUGET-94).
	err := r.db.InTx(context.Background(), func(tx meta.Tx) error {
		return r.changes.Notify(tx, cluster.AreaRepoConfig, 1, map[string]string{"id": fmt.Sprint(rem.ID()), "key": "q-flush", "op": "update"})
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the change to drop the repository's copy", 5*time.Second, func() bool { return entries() == 1 })
	before := s.count()
	if got, err := r.query(rem, "search", url.Values{"q": {"x"}}, time.Hour); err != nil || got.Cached {
		t.Fatalf("a copy survived the change: %+v %v", got, err)
	}
	if got, err := r.query(other, "search", url.Values{"q": {"x"}}, time.Hour); err != nil || !got.Cached {
		t.Fatalf("the copy of another repository was dropped: %+v %v", got, err)
	}
	if s.count() != before+1 {
		t.Fatalf("requests = %d, want %d", s.count(), before+1)
	}

	t.Run("a new configuration version is another key", func(t *testing.T) {
		next := rem
		next.Repo.Version++
		if got, err := r.query(next, "search", url.Values{"q": {"x"}}, time.Hour); err != nil || got.Cached {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// TestR_ARCH_013_ACopyOfTheCurrentConfigurationOutlivesTheChange: a replica applies every change of
// repo_config twice, at once (the writer itself, or Valkey's message) and again with the change-log
// poll up to cluster.epoch_poll_interval later (R-ARCH-018 steps 2 to 4). The key of a copy carries
// the configuration version, so a change drops the copies of the older versions only: the poll's
// second application of a change keeps the copy fetched under the version the change produced, and
// a change of another repository during a fill does not keep the fill out of the cache (Q-NUGET-94).
// This is what lets R-NUGET-016's replicas still hold the forwarded answer that a claim then filters.
func TestR_ARCH_013_ACopyOfTheCurrentConfigurationOutlivesTheChange(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	s := serveSearch(up, `{}`, 0)
	// /slow answers when the test releases it, so a change can be applied while a fill runs.
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	var slow atomic.Int64
	up.Set("/slow", proxytest.Route{Handler: func(w http.ResponseWriter, r *http.Request) {
		slow.Add(1)
		<-gate
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	}})
	e.remote("q-outlive", up)
	other := e.remote("q-bystander", up)
	// The poll runs only when the test asks for it, so the order of the two applications is the test's.
	r := e.newReplica("a", replicaOpts{valkey: vkUnreachable, poll: time.Hour})
	x := url.Values{"q": {"x"}}
	entries := func() float64 { return r.metric("trove_cache_entries", "cache", proxy.CacheQueryResults) }
	// announce writes the change of a repository's row as the repository service does
	// (internal/repository, recordAndNotify); the writer applies it at once.
	announce := func(rem proxy.Remote, op string) {
		t.Helper()
		err := r.db.InTx(context.Background(), func(tx meta.Tx) error {
			return r.changes.Notify(tx, cluster.AreaRepoConfig, 1, map[string]any{
				"id": fmt.Sprint(rem.ID()), "key": rem.Repo.Key, "op": op, "version": rem.Repo.Version,
			})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// update writes a new configuration version of a repository and announces it.
	update := func(key string) proxy.Remote {
		t.Helper()
		e.exec("UPDATE repositories SET config_version = config_version + 1 WHERE key = $1", key)
		next := e.lookup(key)
		announce(next, "updated")
		return next
	}
	poll := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.changes.PollNow(ctx); err != nil {
			t.Fatal(err)
		}
	}

	var next proxy.Remote
	t.Run("the poll's second application of the creation keeps the copy fetched after the first", func(t *testing.T) {
		// The e2e run of TestNuGetSearchClaimTiming: a search cached within one poll interval of the
		// remote's creation, and the claim's search afterwards asked the upstream again.
		created := e.lookup("q-outlive")
		announce(created, "created")
		if got, err := r.query(created, "search", x, time.Hour); err != nil || got.Cached {
			t.Fatalf("the first query: %+v %v", got, err)
		}
		before := s.count()
		poll()
		if got, err := r.query(created, "search", x, time.Hour); err != nil || !got.Cached {
			t.Fatalf("the poll's application of the creation dropped the copy: %+v %v", got, err)
		}
		if s.count() != before {
			t.Fatalf("the upstream saw %d new searches", s.count()-before)
		}
	})
	t.Run("the poll's second application of an update keeps the copy fetched under the new version", func(t *testing.T) {
		next = update("q-outlive")
		if entries() != 0 {
			t.Fatalf("the update left %v copies of the older version", entries())
		}
		if got, err := r.query(next, "search", x, time.Hour); err != nil || got.Cached {
			t.Fatalf("the first query of the new version: %+v %v", got, err)
		}
		before := s.count()
		poll()
		if got, err := r.query(next, "search", x, time.Hour); err != nil || !got.Cached {
			t.Fatalf("the poll's application of the update dropped the copy fetched under the version it produced: %+v %v", got, err)
		}
		if s.count() != before {
			t.Fatalf("the upstream saw %d new searches", s.count()-before)
		}
	})
	t.Run("a change of another repository during a fill does not keep the fill out of the cache", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			_, err := r.query(next, "slow", x, time.Hour)
			done <- err
		}()
		waitFor(t, "the fill to reach the upstream", 5*time.Second, func() bool { return slow.Load() == 1 })
		update(other.Repo.Key)
		poll()
		release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got, err := r.query(next, "slow", x, time.Hour); err != nil || !got.Cached || slow.Load() != 1 {
			t.Fatalf("the fill was not kept: %+v %v after %d upstream requests", got, err, slow.Load())
		}
	})
	t.Run("a deletion drops every copy of the repository", func(t *testing.T) {
		if entries() != 2 {
			t.Fatalf("entries = %v", entries())
		}
		err := r.db.InTx(context.Background(), func(tx meta.Tx) error {
			return r.changes.Notify(tx, cluster.AreaRepoConfig, 1, map[string]any{
				"id": fmt.Sprint(next.ID()), "key": next.Repo.Key, "op": "deleted", "version": next.Repo.Version,
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if entries() != 0 {
			t.Fatalf("the deletion left %v copies", entries())
		}
	})
}
