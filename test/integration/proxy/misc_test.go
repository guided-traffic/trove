//go:build integration

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestURLMap is R-REPO-043 at the engine: a recorded URL is used, a host outside the
// upstream and extraHosts is refused without a request, and a document records
// mappings for its own package only.
func TestURLMap(t *testing.T) {
	e := newEnv(t)
	up, cdn := e.upstream(), e.upstream()
	tarball := proxytest.Body("partner", 50_000)
	cdn.Set("/partner-1.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream"})
	up.Set("/pkg/partner", proxytest.Route{Type: "application/json", Body: []byte("{}\nmap pkg/partner/-/partner-1.tgz " + cdn.URL + "/partner-1.tgz\n")})
	// A document of package evil names the artifact of package lodash.
	up.Set("/pkg/evil", proxytest.Route{Type: "application/json", Body: []byte("{}\nmap pkg/lodash/-/lodash-4.tgz " + cdn.URL + "/evil.tgz\nmap pkg/evil/-/evil-1.tgz " + cdn.URL + "/e.tgz\n")})
	// A document that names the URL the format derives anyway.
	up.Set("/pkg/plain", proxytest.Route{Type: "application/json", Body: []byte("{}\nmap pkg/plain/-/plain-1.tgz " + up.URL + "/pkg/plain/-/plain-1.tgz\n")})
	rem := e.remote("urlmap", up)
	r := e.newReplica("a", replicaOpts{ups: []*proxytest.Upstream{cdn}})

	t.Run("R-REPO-043 an artifact on a host that is neither the upstream nor in extraHosts is refused without a request", func(t *testing.T) {
		if s := r.metadata(rem, "pkg/partner"); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		s := r.artifact(rem, "pkg/partner/-/partner-1.tgz")
		if s.status != 502 || !strings.Contains(string(s.body), "foreign_host_denied") {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if cdn.Total() != 0 {
			t.Fatalf("the foreign host got %d requests", cdn.Total())
		}
	})
	t.Run("R-REPO-043 with the host in extraHosts the artifact is fetched from there and served under the trove path", func(t *testing.T) {
		allowed := rem
		allowed.Repo.Version++ // a new configuration version is a new client
		allowed.Settings.ExtraHosts = []repository.ExtraHost{{Pattern: "127.0.0.1"}}
		s := r.artifact(allowed, "pkg/partner/-/partner-1.tgz")
		if s.status != 200 || !bytes.Equal(s.body, tarball) {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if cdn.Count("GET", "/partner-1.tgz") != 1 {
			t.Fatal("the artifact did not come from the mapped host")
		}
		if n := e.number("SELECT count(*) FROM items WHERE upstream_url LIKE '%partner-1.tgz'"); n != 1 {
			t.Fatalf("the item does not record its upstream URL")
		}
	})
	t.Run("R-REPO-043 a document records mappings for its own package only", func(t *testing.T) {
		if s := r.metadata(rem, "pkg/evil"); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		if n := e.number("SELECT count(*) FROM remote_url_map WHERE path LIKE 'pkg/lodash/%'"); n != 0 {
			t.Fatalf("remote_url_map holds %d rows for another package", n)
		}
		if n := e.number("SELECT count(*) FROM remote_url_map WHERE path = 'pkg/evil/-/evil-1.tgz'"); n != 1 {
			t.Fatalf("the own mapping was not recorded")
		}
	})
	t.Run("R-REPO-043 the URL the format derives anyway records nothing", func(t *testing.T) {
		if s := r.metadata(rem, "pkg/plain"); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		if n := e.number("SELECT count(*) FROM remote_url_map WHERE path LIKE 'pkg/plain/%'"); n != 0 {
			t.Fatalf("rows = %d", n)
		}
	})
	t.Run("R-REPO-025 the upstream URL is built from the canonical segments: %2e%2e stays one segment", func(t *testing.T) {
		up.Set("/pkg/%2e%2e/-/x.tgz", proxytest.Route{Body: []byte("x")})
		_ = r.artifactWith(rem, "GET", "pkg/%2e%2e/-/x.tgz", nil)
		for _, s := range up.Seen() {
			if strings.Contains(s.Path, "/../") || strings.HasSuffix(s.Path, "/..") {
				t.Fatalf("the upstream saw a dot segment: %q", s.Path)
			}
		}
		if got := up.Count("GET", "/pkg/%2e%2e/-/x.tgz"); got != 1 {
			t.Fatalf("the segment reached the upstream as %v", up.Seen())
		}
	})
}

// TestOfflineAndCacheView is R-REPO-030 and R-REPO-032: offline mode, the cache view
// and only-if-cached.
func TestOfflineAndCacheView(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	doc := []byte(`{"name":"stored"}`)
	tarball := proxytest.Body("stored", 20_000)
	up.Set("/pkg/stored", proxytest.Route{Body: doc, Type: "application/json"})
	up.Set("/pkg/stored/-/stored-1.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream"})
	up.Set("/pkg/other/-/other-1.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream"})
	rem := e.remote("offline", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 1 })
	r := e.newReplica("a", replicaOpts{})
	_ = r.metadata(rem, "pkg/stored")
	_ = r.artifact(rem, "pkg/stored/-/stored-1.tgz")
	time.Sleep(1200 * time.Millisecond) // the document is expired
	asked := up.Total()

	t.Run("R-REPO-030 an offline repository serves stored entries regardless of expiry and answers 404 for the rest, asking nothing", func(t *testing.T) {
		off := rem
		off.Settings.Availability.Mode = repository.AvailabilityOffline
		s := r.metadata(off, "pkg/stored")
		if s.status != 200 || !bytes.Equal(s.body, doc) || s.cacheStatus() != "trove; hit; detail=served-stale" {
			t.Fatalf("%d %q", s.status, s.cacheStatus())
		}
		if s := r.artifact(off, "pkg/stored/-/stored-1.tgz"); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		s = r.artifact(off, "pkg/other/-/other-1.tgz")
		if s.status != 404 || !strings.Contains(string(s.body), "not_cached_offline") {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if up.Total() != asked {
			t.Fatalf("an offline repository sent %d requests", up.Total()-asked)
		}
	})
	t.Run("R-REPO-031 the global offline switch behaves like the repository's mode", func(t *testing.T) {
		g := e.newReplica("g", replicaOpts{proxy: func(p *config.Proxy) { p.GlobalOffline = true }})
		if s := g.artifact(rem, "pkg/other/-/other-1.tgz"); s.status != 404 || !strings.Contains(string(s.body), "not_cached_offline") {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if up.Total() != asked {
			t.Fatal("the global switch let a request through")
		}
	})
	t.Run("R-REPO-032 the cache view serves the expired document and never contacts the upstream", func(t *testing.T) {
		view := func(rq *proxy.Request) { rq.Mode = proxy.CacheView }
		s := r.metadata(rem, "pkg/stored", view)
		if s.status != 200 || !bytes.Equal(s.body, doc) {
			t.Fatalf("%d", s.status)
		}
		if s := r.artifactWith(rem, "GET", "pkg/other/-/other-1.tgz", nil, view); s.status != 404 {
			t.Fatalf("%d", s.status)
		}
		if s := r.metadata(rem, "pkg/unknown", view); s.status != 404 {
			t.Fatalf("%d", s.status)
		}
		if up.Total() != asked {
			t.Fatal("the cache view contacted the upstream")
		}
	})
	t.Run("R-REPO-032 only-if-cached is answered from storage or with 504", func(t *testing.T) {
		only := func(rq *proxy.Request) { rq.Mode = proxy.OnlyIfCached }
		if s := r.artifactWith(rem, "GET", "pkg/other/-/other-1.tgz", nil, only); s.status != 504 {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if s := r.artifactWith(rem, "GET", "pkg/stored/-/stored-1.tgz", nil, only); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		if s := r.metadata(rem, "pkg/stored", only); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		if up.Total() != asked {
			t.Fatal("only-if-cached contacted the upstream")
		}
	})
}

// TestHeadAndObjectStore is R-REPO-025: HEAD for an uncached artifact stores nothing,
// and an artifact miss is streamed when the object store is down.
func TestHeadAndObjectStore(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	tarball := proxytest.Body("head", 300_000)
	up.Set("/pkg/h/-/h-1.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream", ETag: `"hh"`})
	// Other bytes than the tarball above: a blob that already exists needs no object write.
	tarballS := proxytest.Body("object store down", 300_000)
	up.Set("/pkg/s/-/s-1.tgz", proxytest.Route{Body: tarballS, Type: "application/octet-stream"})
	up.Set("/pkg/d", proxytest.Route{Body: []byte(`{"d":1}`), Type: "application/json"})
	rem := e.remote("head", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 1 })
	r := e.newReplica("a", replicaOpts{})

	t.Run("R-REPO-025 HEAD of an uncached artifact returns the upstream's headers and leaves no entry", func(t *testing.T) {
		s := r.artifactWith(rem, http.MethodHead, "pkg/h/-/h-1.tgz", nil)
		if s.status != 200 || s.header.Get("ETag") != `"hh"` || s.header.Get("Content-Length") != "300000" || len(s.body) != 0 {
			t.Fatalf("%d %v", s.status, s.header)
		}
		if up.Count("HEAD", "/pkg/h/-/h-1.tgz") != 1 || up.Count("GET", "/pkg/h/-/h-1.tgz") != 0 {
			t.Fatalf("the upstream saw %+v", up.Seen())
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries") + e.number("SELECT count(*) FROM items WHERE kind = 'file'"); n != 0 {
			t.Fatalf("a HEAD stored %d rows", n)
		}
	})
	t.Run("R-REPO-025 HEAD of a cached artifact is answered from the cache", func(t *testing.T) {
		_ = r.artifact(rem, "pkg/h/-/h-1.tgz")
		before := up.Total()
		s := r.artifactWith(rem, http.MethodHead, "pkg/h/-/h-1.tgz", nil)
		if s.status != 200 || up.Total() != before {
			t.Fatalf("%d, %d requests", s.status, up.Total()-before)
		}
	})
	t.Run("R-REPO-025 with the object store down an uncached artifact is streamed, verified, and no entry is written", func(t *testing.T) {
		r.obj.down.Store(true)
		defer r.obj.down.Store(false)
		s := r.artifact(rem, "pkg/s/-/s-1.tgz")
		if s.status != 200 || !bytes.Equal(s.body, tarballS) {
			t.Fatalf("%d (%d bytes) %v", s.status, len(s.body), s.err)
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE path = 'pkg/s/-/s-1.tgz'"); n != 0 {
			t.Fatalf("entries = %d", n)
		}
		// A mismatch still fails the transfer: the pass-through keeps the verification.
		bad := r.artifact(rem, "pkg/s/-/s-1.tgz", func(rq *proxy.Request) { rq.Expected = proxy.Digests{SHA512: proxytest.SHA512([]byte("x"))} })
		if bad.status == 200 && bytes.Equal(bad.body, tarballS) {
			t.Fatal("a mismatching artifact was streamed to completion")
		}
	})
	t.Run("R-REPO-025 with the object store down a refreshed document is served and not stored", func(t *testing.T) {
		r.obj.down.Store(true)
		defer r.obj.down.Store(false)
		s := r.metadata(rem, "pkg/d")
		if s.status != 200 || string(s.body) != `{"d":1}` {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE path = 'pkg/d'"); n != 0 {
			t.Fatalf("entries = %d", n)
		}
	})
}

// TestProbe is R-REPO-048 steps 3 to 5 at the engine: the probe asks once, stores
// nothing and counts toward the circuit.
func TestProbe(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/there", proxytest.Route{Body: []byte("{}"), Type: "application/json"})
	up.Set("/pkg/busy", proxytest.Route{Status: 500})
	rem := e.remote("probe", up, func(s *repository.RemoteSettings) { s.Availability.FailureThreshold = 2 })
	r := e.newReplica("a", replicaOpts{})
	probe := func(path string) proxy.ProbeResult {
		res, err := r.engine.Probe(context.Background(), rem, path, &proxytest.Adapter{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	t.Run("R-REPO-048 2xx is served, 404 is not served, and the probe stores nothing", func(t *testing.T) {
		if got := probe("pkg/there").Outcome; got != proxy.Served {
			t.Fatalf("served: %v", got)
		}
		if got := probe("pkg/missing").Outcome; got != proxy.NotServed {
			t.Fatalf("not served: %v", got)
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries"); n != 0 {
			t.Fatalf("entries = %d", n)
		}
		if _, ok := getKey(t, e.valkeyAdmin(r), e.negativeKey(rem, "pkg/missing", "")); ok {
			t.Fatal("a probe wrote a negative entry")
		}
		if up.Count("GET", "/pkg/missing") != 1 {
			t.Fatal("the probe asked more than once")
		}
	})
	t.Run("R-REPO-048 a failure is undetermined, counts toward the circuit, and an open circuit asks nothing", func(t *testing.T) {
		if got := probe("pkg/busy").Outcome; got != proxy.Undetermined {
			t.Fatalf("%v", got)
		}
		_ = probe("pkg/busy")
		waitFor(t, "the circuit to open", 2*time.Second, func() bool { return r.metric("trove_remote_upstream_up", "repo", "probe") == 0 })
		before := up.Total()
		res := probe("pkg/there")
		if res.Outcome != proxy.Undetermined || res.RetryAfter <= 0 || up.Total() != before {
			t.Fatalf("%+v, %d requests", res, up.Total()-before)
		}
	})
	t.Run("R-REPO-048 an offline repository is not served and is not asked", func(t *testing.T) {
		off := rem
		off.Settings.Availability.Mode = repository.AvailabilityOffline
		before := up.Total()
		res, err := r.engine.Probe(context.Background(), off, "pkg/there", &proxytest.Adapter{})
		if err != nil || res.Outcome != proxy.NotServed || up.Total() != before {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

// TestEntries covers EntryState, Cached and CachedArtifacts, which the virtual engine
// and the shadowing check read.
func TestEntries(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/e", proxytest.Route{Body: []byte("{}"), Type: "application/json"})
	for _, v := range []string{"1", "2"} {
		up.Set("/pkg/e/-/e-"+v+".tgz", proxytest.Route{Body: []byte("v" + v), Type: "application/octet-stream"})
	}
	up.Set("/pkg/ee/-/ee-1.tgz", proxytest.Route{Body: []byte("x"), Type: "application/octet-stream"})
	rem := e.remote("entries", up)
	r := e.newReplica("a", replicaOpts{})
	ctx := context.Background()

	if st, _ := r.engine.EntryState(ctx, rem.ID(), "pkg/e"); st.Stored {
		t.Fatalf("%+v before any fetch", st)
	}
	_ = r.metadata(rem, "pkg/e")
	for _, p := range []string{"pkg/e/-/e-1.tgz", "pkg/e/-/e-2.tgz", "pkg/ee/-/ee-1.tgz"} {
		_ = r.artifact(rem, p)
	}
	t.Run("R-REPO-048 EntryState reports a stored document without contacting the upstream", func(t *testing.T) {
		before := up.Total()
		st, err := r.engine.EntryState(ctx, rem.ID(), "pkg/e")
		if err != nil || !st.Stored || !st.GoneAt.IsZero() || up.Total() != before {
			t.Fatalf("%+v %v", st, err)
		}
	})
	t.Run("R-REPO-020 CachedArtifacts lists the cached tarballs of one package, not of its namesakes", func(t *testing.T) {
		got, err := r.engine.CachedArtifacts(ctx, rem.ID(), "pkg/e/")
		if err != nil || strings.Join(got, ",") != "pkg/e/-/e-1.tgz,pkg/e/-/e-2.tgz" {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("R-REPO-025 Cached reads the cache tier and never contacts the upstream", func(t *testing.T) {
		before := up.Total()
		it, ok, err := r.engine.Cached(ctx, r.request(rem, "pkg/e/-/e-1.tgz", proxy.Artifact))
		if err != nil || !ok || it.Path != "pkg/e/-/e-1.tgz" {
			t.Fatalf("%+v %v %v", it, ok, err)
		}
		if _, ok, _ := r.engine.Cached(ctx, r.request(rem, "pkg/e/-/e-3.tgz", proxy.Artifact)); ok || up.Total() != before {
			t.Fatal("an uncached artifact was found, or the upstream was asked")
		}
	})
}

// TestObservability is R-REPO-047 and R-OPS-011: the Cache-Status of each situation,
// the metrics and the fields of the upstream log.
func TestObservability(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	tarball := proxytest.Body("obs", 20_000)
	up.Set("/pkg/obs", proxytest.Route{Body: []byte(`{"o":1}`), Type: "application/json", ETag: `"o"`})
	up.Set("/pkg/obs/-/obs-1.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream", Delay: 500 * time.Millisecond})
	rem := e.remote("obs", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 1 })
	var logs syncBuffer
	r := e.newReplica("a", replicaOpts{upstreamLog: &logs})

	t.Run("R-REPO-047 the Cache-Status of a miss, a hit, a revalidation and a stale answer", func(t *testing.T) {
		want := func(got served, prefix string) {
			t.Helper()
			if !strings.HasPrefix(got.cacheStatus(), prefix) {
				t.Fatalf("Cache-Status = %q, want %q", got.cacheStatus(), prefix)
			}
		}
		want(r.metadata(rem, "pkg/obs"), "trove; fwd=uri-miss; fwd-status=200; stored")
		want(r.metadata(rem, "pkg/obs"), "trove; hit; ttl=")
		time.Sleep(1200 * time.Millisecond)
		want(r.metadata(rem, "pkg/obs"), "trove; fwd=stale; fwd-status=304")
		time.Sleep(1200 * time.Millisecond)
		up.Set("/pkg/obs", proxytest.Route{Status: 503})
		want(r.metadata(rem, "pkg/obs"), "trove; fwd=stale; fwd-status=503; detail=served-stale")
	})
	t.Run("R-REPO-047 R-OPS-011 the upstream log carries repo, flight_role, collapsed_waiters and cache_status", func(t *testing.T) {
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() { defer wg.Done(); _ = r.artifact(rem, "pkg/obs/-/obs-1.tgz") }()
		}
		wg.Wait()
		var rec map[string]any
		sc := bufio.NewScanner(strings.NewReader(logs.String()))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil && m["purpose"] == "remote" && strings.HasSuffix(asString(m["url"]), "/obs-1.tgz") {
				rec = m
			}
		}
		if rec == nil {
			t.Fatalf("no upstream log record for the artifact in %q", logs.String())
		}
		if rec["repo"] != "obs" || rec["flight_role"] != "leader" || !strings.HasPrefix(asString(rec["cache_status"]), "trove; fwd=uri-miss") {
			t.Fatalf("%v", rec)
		}
		if n, _ := rec["collapsed_waiters"].(float64); n < 15 {
			t.Fatalf("collapsed_waiters = %v, 19 requests joined the flight", rec["collapsed_waiters"])
		}
	})
	t.Run("R-REPO-047 the metrics of the engine count lookups, upstream requests and the circuit", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			labels []string
		}{
			{"trove_remote_cache_lookups_total", []string{"repo", "obs", "kind", "metadata", "result", "hit"}},
			{"trove_remote_cache_lookups_total", []string{"repo", "obs", "kind", "artifact", "result", "miss"}},
			{"trove_remote_upstream_requests_total", []string{"repo", "obs", "code", "200"}},
			{"trove_remote_upstream_requests_total", []string{"repo", "obs", "code", "304"}},
			{"trove_remote_upstream_requests_total", []string{"repo", "obs", "code", "503"}},
			{"trove_remote_upstream_request_duration_seconds", []string{"repo", "obs"}},
			{"trove_remote_collapsed_requests_total", []string{"repo", "obs"}},
			{"trove_remote_fetch_queue_wait_seconds", []string{"repo", "obs"}},
			{"trove_remote_upstream_up", []string{"repo", "obs"}},
		} {
			if r.metric(tc.name, tc.labels...) <= 0 && tc.name != "trove_remote_upstream_up" {
				t.Errorf("%s%v = 0", tc.name, tc.labels)
			}
		}
	})
	t.Run("R-REPO-047 a failed first attempt is retried once after 250 ms and logged as attempt 2", func(t *testing.T) {
		e2 := newEnv(t)
		up2 := e2.upstream()
		rem2 := e2.remote("retry", up2)
		var l2 syncBuffer
		r2 := e2.newReplica("a", replicaOpts{upstreamLog: &l2})
		up2.Close() // nothing listens: the connection is refused before a byte is sent
		start := time.Now()
		s := r2.metadata(rem2, "pkg/gone-host")
		if s.status != 502 || time.Since(start) < 250*time.Millisecond {
			t.Fatalf("%d after %s", s.status, time.Since(start))
		}
		if !strings.Contains(l2.String(), `"attempt":2`) {
			t.Fatalf("no second attempt in %q", l2.String())
		}
	})
}

func asString(v any) string { s, _ := v.(string); return s }

// syncBuffer collects log output from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestPopulateAtTheEngine is R-AUTHZ-008 and R-EP-019 where the engine asks the check.
func TestPopulateAtTheEngine(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	doc := []byte(`{"p":1}`)
	up.Set("/pkg/pop", proxytest.Route{Body: doc, Type: "application/json"})
	up.Set("/pkg/pop/-/pop-2.tgz", proxytest.Route{Body: []byte("2"), Type: "application/octet-stream"})
	rem := e.remote("pop", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 1 })
	r := e.newReplica("a", replicaOpts{})
	_ = r.metadata(rem, "pkg/pop")
	time.Sleep(1200 * time.Millisecond)
	asked := up.Total()
	customers := &proxytest.Populate{Allow: false, Name: "customers", Fills: false}

	t.Run("R-AUTHZ-008 a caller who may read but not populate gets the expired document as it is, without a refresh", func(t *testing.T) {
		s := r.metadata(rem, "pkg/pop", func(rq *proxy.Request) { rq.Populate = customers })
		if s.status != 200 || !bytes.Equal(s.body, doc) || s.cacheStatus() != "trove; hit; detail=served-stale" {
			t.Fatalf("%d %q", s.status, s.cacheStatus())
		}
		if customers.Denies() != 0 {
			t.Fatal("serving the cache is a question, not an audited refusal")
		}
	})
	t.Run("R-EP-019 an uncached path on an endpoint without cache_fill is 403, asks nothing, writes no negative entry and is counted", func(t *testing.T) {
		s := r.artifactWith(rem, "GET", "pkg/pop/-/pop-2.tgz", nil, func(rq *proxy.Request) { rq.Populate = customers })
		if s.status != 403 || !strings.Contains(string(s.body), "populate_not_permitted") {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if up.Total() != asked {
			t.Fatal("the endpoint made trove contact the upstream")
		}
		if _, ok := getKey(t, e.valkeyAdmin(r), e.negativeKey(rem, "pkg/pop/-/pop-2.tgz", "")); ok {
			t.Fatal("a refused fill wrote a negative entry")
		}
		if got := r.metric("trove_endpoint_cache_fill_refused_total", "endpoint", "customers"); got < 2 {
			t.Fatalf("trove_endpoint_cache_fill_refused_total{customers} = %v", got)
		}
	})
	t.Run("R-AUTHZ-008 a populate that fetches writes its event in the fill transaction, once", func(t *testing.T) {
		pop := proxytest.Allowed()
		if s := r.artifactWith(rem, "GET", "pkg/pop/-/pop-2.tgz", nil, func(rq *proxy.Request) { rq.Populate = pop }); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		if pop.Records() != 1 {
			t.Fatalf("populate events = %d", pop.Records())
		}
		_ = r.artifactWith(rem, "GET", "pkg/pop/-/pop-2.tgz", nil, func(rq *proxy.Request) { rq.Populate = pop })
		if pop.Records() != 1 {
			t.Fatalf("a hit wrote a populate event")
		}
	})
}

// orderAdapter records the calls the engine makes on a format, in order.
type orderAdapter struct {
	*proxytest.Adapter
	mu    sync.Mutex
	calls []string
	// onFill runs inside OnFill, in the fill transaction.
	onFill func()
}

func (a *orderAdapter) note(s string) {
	a.mu.Lock()
	a.calls = append(a.calls, s)
	a.mu.Unlock()
}

func (a *orderAdapter) UpstreamRequest(ctx context.Context, b proxy.UpstreamBase, path, variant string) (*http.Request, error) {
	a.note("UpstreamRequest")
	return a.Adapter.UpstreamRequest(ctx, b, path, variant)
}

func (a *orderAdapter) AcceptResponse(path string, resp *http.Response) error {
	a.note("AcceptResponse")
	return a.Adapter.AcceptResponse(path, resp)
}

func (a *orderAdapter) PrepareFill(ctx context.Context, path, variant string, body io.ReaderAt, size int64) (proxy.Prepared, error) {
	a.note("PrepareFill")
	return a.Adapter.PrepareFill(ctx, path, variant, body, size)
}

func (a *orderAdapter) OnFill(ctx context.Context, tx meta.Tx, f proxy.FillEvent) (bool, error) {
	a.note("OnFill")
	if a.onFill != nil {
		a.onFill()
	}
	return a.Adapter.OnFill(ctx, tx, f)
}

// recordingFlights notes Acquire in the same log.
type recordingFlights struct {
	cluster.FlightLock
	a *orderAdapter
}

func (r recordingFlights) Acquire(ctx context.Context, f cluster.FlightKey) (*cluster.FlightHold, bool, error) {
	r.a.note("Acquire")
	return r.FlightLock.Acquire(ctx, f)
}

// TestDownloadOrder is R-ARCH-013: on a miss the flight is acquired, the format builds
// the request, accepts the response, validates the document outside the transaction,
// the transaction stores the entry, and only then is the lock released.
func TestDownloadOrder(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/ord", proxytest.Route{Body: []byte("{}"), Type: "application/json"})
	rem := e.remote("order13", up)
	oa := &orderAdapter{Adapter: &proxytest.Adapter{}}
	r := e.newReplica("a", replicaOpts{flights: func(f cluster.FlightLock) cluster.FlightLock { return recordingFlights{FlightLock: f, a: oa} }})
	admin := e.valkeyAdmin(r)
	lockHeld := false
	oa.onFill = func() { _, lockHeld = getKey(t, admin, e.lockKey(rem, "pkg/ord", "")) }

	s := r.metadata(rem, "pkg/ord", func(rq *proxy.Request) { rq.Adapter = oa })
	if s.status != 200 {
		t.Fatalf("%d", s.status)
	}
	want := "Acquire,UpstreamRequest,AcceptResponse,PrepareFill,OnFill"
	if got := strings.Join(oa.calls, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	if !lockHeld {
		t.Fatal("the lock was not held while the fill transaction ran")
	}
	if _, ok := getKey(t, admin, e.lockKey(rem, "pkg/ord", "")); ok {
		t.Fatal("the lock was not released after the commit")
	}
}

// roles reads the flight_role of the upstream log records of a path.
func roles(logs *syncBuffer, suffix string) map[string]int {
	out := map[string]int{}
	sc := bufio.NewScanner(strings.NewReader(logs.String()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["purpose"] == "remote" && strings.HasSuffix(asString(m["url"]), suffix) {
			out[asString(m["flight_role"])]++
		}
	}
	return out
}

// TestFlightRoles is Q-REPO-92: the role of the request that sends an upstream request
// says how it came to send it.
func TestFlightRoles(t *testing.T) {
	t.Run("R-OPS-011 a follower whose flight left nothing to read fetches for itself with the role follower", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		body := proxytest.Body("follower", 100_000)
		up.Set("/pkg/f/-/f-1.tgz", proxytest.Route{Body: body, Type: "application/octet-stream", Delay: 300 * time.Millisecond})
		rem := e.remote("roles-f", up)
		var logs syncBuffer
		r := e.newReplica("a", replicaOpts{valkey: vkUnreachable, upstreamLog: &logs})
		r.obj.down.Store(true) // nothing is stored: the followers find nothing to read
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if s := r.artifact(rem, "pkg/f/-/f-1.tgz"); s.status != 200 || !bytes.Equal(s.body, body) {
					t.Errorf("%d", s.status)
				}
			}()
		}
		wg.Wait()
		got := roles(&logs, "/f-1.tgz")
		if got["leader"] != 1 || got["follower"] < 1 {
			t.Fatalf("roles = %v", got)
		}
	})
	t.Run("R-OPS-011 a request that waited for another replica's flight and then fetched has the role waiter", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/w/-/w-1.tgz", proxytest.Route{Body: []byte("w"), Type: "application/octet-stream"})
		rem := e.remote("roles-w", up)
		var logs syncBuffer
		r := e.newReplica("a", replicaOpts{upstreamLog: &logs, proxy: func(p *config.Proxy) { p.FetchWaitTimeoutSeconds = 1 }})
		setForeign(t, e.valkeyAdmin(r), e.lockKey(rem, "pkg/w/-/w-1.tgz", ""), time.Minute)
		if s := r.artifact(rem, "pkg/w/-/w-1.tgz"); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		if got := roles(&logs, "/w-1.tgz"); got["waiter"] != 1 {
			t.Fatalf("roles = %v", got)
		}
	})
	t.Run("R-OPS-011 R-REPO-047 the upstream request of a shadowing check has the role probe", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/p", proxytest.Route{Body: []byte("{}"), Type: "application/json"})
		rem := e.remote("roles-p", up)
		var logs syncBuffer
		r := e.newReplica("a", replicaOpts{upstreamLog: &logs})
		if res, err := r.engine.Probe(context.Background(), rem, "pkg/p", &proxytest.Adapter{}); err != nil || res.Outcome != proxy.Served {
			t.Fatalf("%+v %v", res, err)
		}
		if got := roles(&logs, "/pkg/p"); got["probe"] != 1 {
			t.Fatalf("roles = %v", got)
		}
	})
}
