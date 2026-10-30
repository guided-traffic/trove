//go:build integration

package proxy

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestFirstAndSecondRequest is the happy path of R-REPO-025 and R-ARCH-013: a miss
// is fetched, stored and served, and the next request is a hit that asks nothing.
func TestFirstAndSecondRequest(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	doc := []byte(`{"name":"lodash"}`)
	tarball := proxytest.Body("lodash-4.17.21", 300_000)
	up.Set("/pkg/lodash", proxytest.Route{Body: doc, Type: "application/json", ETag: `"d1"`})
	up.Set("/pkg/lodash/-/lodash-4.17.21.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream"})
	rem := e.remote("npm-remote", up)
	r := e.newReplica("a", replicaOpts{})

	t.Run("R-REPO-025 a document miss is fetched and stored", func(t *testing.T) {
		got := r.metadata(rem, "pkg/lodash")
		if got.status != 200 || !bytes.Equal(got.body, doc) {
			t.Fatalf("%d %q", got.status, got.body)
		}
		if cs := got.cacheStatus(); cs != "trove; fwd=uri-miss; fwd-status=200; stored" {
			t.Fatalf("Cache-Status = %q", cs)
		}
		if n := up.Count("GET", "/pkg/lodash"); n != 1 {
			t.Fatalf("upstream GETs = %d", n)
		}
	})
	t.Run("R-REPO-025 a fresh document is a hit that asks nothing", func(t *testing.T) {
		got := r.metadata(rem, "pkg/lodash")
		if got.status != 200 || !bytes.Equal(got.body, doc) {
			t.Fatalf("%d %q", got.status, got.body)
		}
		if cs := got.cacheStatus(); !strings.HasPrefix(cs, "trove; hit; ttl=") {
			t.Fatalf("Cache-Status = %q", cs)
		}
		if n := up.Total(); n != 1 {
			t.Fatalf("upstream requests = %d", n)
		}
	})
	t.Run("R-REPO-025 an artifact miss is fetched, stored as an item and served", func(t *testing.T) {
		got := r.artifact(rem, "pkg/lodash/-/lodash-4.17.21.tgz")
		if got.status != 200 || !bytes.Equal(got.body, tarball) {
			t.Fatalf("%d, %d bytes", got.status, len(got.body))
		}
		if cs := got.cacheStatus(); cs != "trove; fwd=uri-miss; fwd-status=200; stored" {
			t.Fatalf("Cache-Status = %q", cs)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/lodash/-/lodash-4.17.21.tgz' AND upstream_url <> ''"); n != 1 {
			t.Fatalf("items with an upstream URL = %d", n)
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE kind = 'artifact' AND item_id IS NOT NULL"); n != 1 {
			t.Fatalf("artifact entries = %d", n)
		}
	})
	t.Run("R-REPO-025 a stored artifact is a hit", func(t *testing.T) {
		before := up.Total()
		got := r.artifact(rem, "pkg/lodash/-/lodash-4.17.21.tgz")
		if got.status != 200 || !bytes.Equal(got.body, tarball) || got.cacheStatus() != "trove; hit" {
			t.Fatalf("%d %q", got.status, got.cacheStatus())
		}
		if up.Total() != before {
			t.Fatalf("a hit asked the upstream")
		}
	})
}

// TestRequestOrder is the order of R-REPO-025 steps 6 to 10: with every condition on
// at once, each answer names the first step that applies, and removing that
// condition moves the answer to the next.
func TestRequestOrder(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/late", proxytest.Route{Status: 404})
	rem := e.remote("order", up, func(s *repository.RemoteSettings) { s.Availability.FailureThreshold = 1 })
	r := e.newReplica("a", replicaOpts{})

	// A negative entry for the path: the upstream said 404 once.
	if got := r.artifact(rem, "pkg/late/-/late-1.tgz"); got.status != 404 {
		t.Fatalf("first request: %d %s", got.status, got.body)
	}
	if up.Count("GET", "/pkg/late/-/late-1.tgz") != 1 {
		t.Fatal("the upstream was not asked once")
	}
	// An open circuit: one counted failure opens it (threshold 1).
	up.Set("/pkg/other", proxytest.Route{Status: 503})
	_ = r.metadata(rem, "pkg/other")
	waitFor(t, "the circuit to open", 5*time.Second, func() bool { return r.metric("trove_remote_upstream_up", "repo", "order") == 0 })

	offline := rem
	offline.Settings.Availability.Mode = repository.AvailabilityOffline
	refused := func(rq *proxy.Request) { rq.Populate = proxytest.Refused() }

	t.Run("R-REPO-025 step 6 comes before the circuit: offline answers 404 not_cached_offline", func(t *testing.T) {
		got := r.artifactWith(offline, http.MethodGet, "pkg/late/-/late-1.tgz", nil, refused)
		if got.status != 404 || !strings.Contains(string(got.body), "not_cached_offline") {
			t.Fatalf("%d %s", got.status, got.body)
		}
	})
	t.Run("R-REPO-025 step 7 comes before the negative entry: an open circuit answers 503 with Retry-After", func(t *testing.T) {
		got := r.artifactWith(rem, http.MethodGet, "pkg/late/-/late-1.tgz", nil, refused)
		if got.status != 503 || got.header.Get("Retry-After") == "" || !strings.Contains(string(got.body), "upstream_assumed_offline") {
			t.Fatalf("%d %v %s", got.status, got.header, got.body)
		}
	})
	// Close the circuit through the database and tell the replica, as the replica that
	// closes it does.
	e.exec("UPDATE remote_upstream_state SET consecutive_failures = 0, open_until = NULL")
	r.notifyState(rem)
	t.Run("R-REPO-025 step 8 comes before the cache-fill check: a negative entry answers 404 hit", func(t *testing.T) {
		got := r.artifactWith(rem, http.MethodGet, "pkg/late/-/late-1.tgz", nil, refused)
		if got.status != 404 || got.cacheStatus() != "trove; hit; detail=negative" {
			t.Fatalf("%d %q %s", got.status, got.cacheStatus(), got.body)
		}
	})
	t.Run("R-REPO-025 step 9: a caller who may read but not fill gets 403 populate_not_permitted for an uncached path", func(t *testing.T) {
		before := up.Total()
		pop := proxytest.Refused()
		got := r.artifactWith(rem, http.MethodGet, "pkg/never/-/never-1.tgz", nil, func(rq *proxy.Request) { rq.Populate = pop })
		if got.status != 403 || !strings.Contains(string(got.body), "populate_not_permitted") {
			t.Fatalf("%d %s", got.status, got.body)
		}
		if up.Total() != before || pop.Denies() != 1 {
			t.Fatalf("upstream requests %d -> %d, denies %d", before, up.Total(), pop.Denies())
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries"); n != 0 {
			t.Fatalf("a refused fill left %d entries", n)
		}
		if got := r.metric("trove_endpoint_cache_fill_refused_total", "endpoint", "default"); got != 0 {
			t.Fatalf("the default endpoint fills caches, the refusal is not the endpoint's: %v", got)
		}
	})
	t.Run("R-REPO-025 step 10 fetches when no earlier step decides", func(t *testing.T) {
		up.Set("/pkg/fine/-/fine-1.tgz", proxytest.Route{Body: []byte("bytes"), Type: "application/octet-stream"})
		got := r.artifact(rem, "pkg/fine/-/fine-1.tgz")
		if got.status != 200 || string(got.body) != "bytes" {
			t.Fatalf("%d %s", got.status, got.body)
		}
	})
}
