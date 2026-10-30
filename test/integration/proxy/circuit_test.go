//go:build integration

package proxy

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestCircuit is R-REPO-029: the classification of upstream failures, the stale copy,
// the circuit that every replica shares through PostgreSQL and the change log, and its
// probe. The second replica has no Valkey, so every bound here is the change log's.
func TestCircuit(t *testing.T) {
	t.Run("R-REPO-029 five 503 open the circuit for both replicas, which then serve stale and answer 503 without asking", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		doc := []byte(`{"name":"cached"}`)
		up.Set("/pkg/cached", proxytest.Route{Body: doc, Type: "application/json", ETag: `"c1"`})
		rem := e.remote("circuit", up, func(s *repository.RemoteSettings) {
			s.Cache.MetadataTTLSeconds = 1
			s.Availability.FailureThreshold, s.Availability.CircuitOpenSeconds = 5, 3
		})
		a, b := e.newReplica("a", replicaOpts{valkey: vkUnreachable}), e.newReplica("b", replicaOpts{valkey: vkUnreachable})
		if s := a.metadata(rem, "pkg/cached"); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		_ = b.metadata(rem, "pkg/cached")   // b knows the repository: its gauge follows the circuit
		time.Sleep(1200 * time.Millisecond) // the stored document expires
		up.Set("/pkg/cached", proxytest.Route{Status: http.StatusServiceUnavailable})
		for i := range 5 {
			s := a.metadata(rem, "pkg/cached")
			if s.status != 200 || !bytes.Equal(s.body, doc) || s.cacheStatus() != "trove; fwd=stale; fwd-status=503; detail=served-stale" {
				t.Fatalf("failure %d: %d %q", i+1, s.status, s.cacheStatus())
			}
		}
		asked := up.Total()
		for _, r := range []*replica{a, b} {
			waitFor(t, r.name+" to hold the circuit open", 2*time.Second, func() bool {
				return r.metric("trove_remote_upstream_up", "repo", "circuit") == 0
			})
		}
		for _, r := range []*replica{a, b} {
			s := r.metadata(rem, "pkg/cached")
			if s.status != 200 || !bytes.Equal(s.body, doc) || s.cacheStatus() != "trove; hit; detail=served-stale" {
				t.Fatalf("%s: %d %q", r.name, s.status, s.cacheStatus())
			}
			u := r.artifact(rem, "pkg/cached/-/cached-1.tgz")
			if u.status != 503 || u.header.Get("Retry-After") == "" || !strings.Contains(string(u.body), "upstream_assumed_offline") {
				t.Fatalf("%s: %d %v %s", r.name, u.status, u.header, u.body)
			}
		}
		if up.Total() != asked {
			t.Fatalf("an open circuit sent %d requests", up.Total()-asked)
		}
		if g := b.metric("trove_remote_upstream_up", "repo", "circuit"); g != 0 {
			t.Fatalf("trove_remote_upstream_up on b = %v", g)
		}

		t.Run("R-REPO-029 after the open period the first request probes, and a 200 closes the circuit for both", func(t *testing.T) {
			up.Set("/pkg/cached", proxytest.Route{Body: []byte(`{"name":"cached","v":2}`), Type: "application/json", ETag: `"c2"`})
			waitFor(t, "the open period to pass", 5*time.Second, func() bool {
				return a.metadata(rem, "pkg/cached").cacheStatus() != "trove; hit; detail=served-stale"
			})
			if s := a.metadata(rem, "pkg/cached"); s.status != 200 || !strings.Contains(string(s.body), `"v":2`) {
				t.Fatalf("%d %s", s.status, s.body)
			}
			waitFor(t, "b to see the circuit closed", 2*time.Second, func() bool { return b.metric("trove_remote_upstream_up", "repo", "circuit") == 1 })
			if n := e.number("SELECT consecutive_failures FROM remote_upstream_state"); n != 0 {
				t.Fatalf("consecutive_failures = %d after a success", n)
			}
		})
	})

	t.Run("R-REPO-029 a 401 answers 502 upstream_auth_failed, keeps the circuit closed and caches nothing negatively", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/secret", proxytest.Route{Status: http.StatusUnauthorized})
		rem := e.remote("circuit-401", up)
		a := e.newReplica("a", replicaOpts{})
		for range 7 {
			s := a.metadata(rem, "pkg/secret")
			if s.status != 502 || !strings.Contains(string(s.body), "upstream_auth_failed") {
				t.Fatalf("%d %s", s.status, s.body)
			}
		}
		if n := up.Count("GET", "/pkg/secret"); n != 7 {
			t.Fatalf("the upstream saw %d requests: a 401 must not be negatively cached", n)
		}
		if n := e.number("SELECT count(*) FROM remote_upstream_state WHERE consecutive_failures > 0 OR open_until IS NOT NULL"); n != 0 {
			t.Fatalf("a 401 was counted toward the circuit")
		}
		if g := a.metric("trove_remote_upstream_up", "repo", "circuit-401"); g == 0 {
			t.Fatal("the circuit opened")
		}
	})

	t.Run("R-REPO-029 a 429 with Retry-After opens the circuit at once", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/busy", proxytest.Route{Status: http.StatusTooManyRequests, Header: map[string]string{"Retry-After": "2"}})
		rem := e.remote("circuit-429", up)
		a := e.newReplica("a", replicaOpts{})
		s := a.metadata(rem, "pkg/busy")
		if s.status != 503 || s.header.Get("Retry-After") == "" {
			t.Fatalf("%d %v", s.status, s.header)
		}
		waitFor(t, "the circuit to open", 2*time.Second, func() bool { return a.metric("trove_remote_upstream_up", "repo", "circuit-429") == 0 })
		before := up.Total()
		if s := a.metadata(rem, "pkg/other"); s.status != 503 || up.Total() != before {
			t.Fatalf("%d, %d requests", s.status, up.Total()-before)
		}
		waitFor(t, "the Retry-After (2 s, not the 300 s period) to pass", 6*time.Second, func() bool {
			up.Set("/pkg/other", proxytest.Route{Body: []byte("{}"), Type: "application/json"})
			return a.metadata(rem, "pkg/other").status == 200
		})
	})

	t.Run("R-REPO-029 a refused connection, a timeout and a content-type refusal count toward the circuit, a size refusal does not", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/html", proxytest.Route{Body: []byte("<html>portal</html>"), Type: "text/html; charset=utf-8"})
		up.Set("/pkg/big", proxytest.Route{Body: proxytest.Body("big", 4096), Type: "application/json"})
		rem := e.remote("circuit-count", up, func(s *repository.RemoteSettings) {
			s.Availability.FailureThreshold = 100
			m := int64(1024)
			s.Limits.MaxMetadataBytes = &m
		})
		a := e.newReplica("a", replicaOpts{})
		count := func() int64 {
			return e.number("SELECT COALESCE(max(consecutive_failures), 0) FROM remote_upstream_state")
		}
		if s := a.metadata(rem, "pkg/html"); s.status != 502 || !strings.Contains(string(s.body), "upstream_content_type_blocked") {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if count() != 1 {
			t.Fatalf("a blocked content type must count: %d", count())
		}
		if s := a.metadata(rem, "pkg/big"); s.status != 502 || !strings.Contains(string(s.body), "upstream_item_too_large") {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if count() != 1 {
			t.Fatalf("a size refusal must not count: %d", count())
		}
		up.Close()
		if s := a.metadata(rem, "pkg/anything"); s.status != 502 || !strings.Contains(string(s.body), "upstream_unreachable") {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if count() != 2 {
			t.Fatalf("a refused connection must count: %d", count())
		}
	})
}

// TestGonePackages is the upstream-gone rule of R-REPO-029 and the gone packages in
// memory of R-REPO-049.
func TestGonePackages(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	doc := []byte(`{"name":"gone"}`)
	tarball := proxytest.Body("gone", 100_000)
	up.Set("/pkg/gone", proxytest.Route{Body: doc, Type: "application/json"})
	up.Set("/pkg/gone/-/gone-1.0.0.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream"})
	rem := e.remote("gone", up, func(s *repository.RemoteSettings) {
		s.Cache.MetadataTTLSeconds, s.Cache.NotFoundTTLSeconds = 1, 2
	})
	// The second replica has no Valkey: the mark reaches it through the change area alone.
	a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{valkey: vkUnreachable})

	if s := a.metadata(rem, "pkg/gone"); s.status != 200 {
		t.Fatalf("%d", s.status)
	}
	if s := a.artifact(rem, "pkg/gone/-/gone-1.0.0.tgz"); s.status != 200 || !bytes.Equal(s.body, tarball) {
		t.Fatalf("%d", s.status)
	}
	if s := b.artifact(rem, "pkg/gone/-/gone-1.0.0.tgz"); s.status != 200 {
		t.Fatalf("b: %d", s.status)
	}
	time.Sleep(1200 * time.Millisecond) // the document expires
	up.Remove("/pkg/gone")
	asked := up.Count("GET", "/pkg/gone/-/gone-1.0.0.tgz")

	t.Run("R-REPO-029 the upstream's 404 marks the stored document gone and writes proxy.upstream_gone once", func(t *testing.T) {
		s := a.metadata(rem, "pkg/gone")
		if s.status != 404 {
			t.Fatalf("%d %q", s.status, s.cacheStatus())
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE upstream_gone_at IS NOT NULL"); n != 1 {
			t.Fatalf("gone marks = %d", n)
		}
		if n := len(a.rec.ofType(audit.EventProxyUpstreamGone)); n != 1 {
			t.Fatalf("proxy.upstream_gone events = %d", n)
		}
		_ = a.metadata(rem, "pkg/gone")
		if n := len(a.rec.ofType(audit.EventProxyUpstreamGone)); n != 1 {
			t.Fatalf("a second 404 wrote a second event: %d", n)
		}
	})

	t.Run("R-REPO-029 the cached artifact answers 404 on both replicas within 2 s, and the upstream is not asked for it", func(t *testing.T) {
		for _, r := range []*replica{a, b} {
			waitFor(t, r.name+" to answer 404 for the cached artifact", 2*time.Second, func() bool {
				return r.artifact(rem, "pkg/gone/-/gone-1.0.0.tgz").status == 404
			})
			if s := r.artifact(rem, "pkg/gone/-/gone-1.0.0.tgz"); s.status != 404 {
				t.Fatalf("%s: %d", r.name, s.status)
			}
		}
		if up.Count("GET", "/pkg/gone/-/gone-1.0.0.tgz") != asked {
			t.Fatal("the upstream was asked for the artifact of a gone package")
		}
	})

	t.Run("R-REPO-032 the cache view answers 404 for the gone package and for its document", func(t *testing.T) {
		for _, path := range []string{"pkg/gone/-/gone-1.0.0.tgz", "pkg/gone"} {
			var s served
			if strings.HasSuffix(path, ".tgz") {
				s = b.artifactWith(rem, http.MethodGet, path, nil, func(rq *proxy.Request) { rq.Mode = proxy.CacheView })
			} else {
				s = b.metadata(rem, path, func(rq *proxy.Request) { rq.Mode = proxy.CacheView })
			}
			if s.status != 404 {
				t.Fatalf("%s: %d", path, s.status)
			}
		}
	})

	t.Run("R-REPO-029 the cached item stays listed", func(t *testing.T) {
		if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/gone/-/gone-1.0.0.tgz' AND kind = 'file' AND trashed_at IS NULL"); n != 1 {
			t.Fatalf("items = %d", n)
		}
	})

	t.Run("R-REPO-025 R-REPO-029 a request for the artifact alone heals the mark when the package comes back", func(t *testing.T) {
		up.Set("/pkg/gone", proxytest.Route{Body: doc, Type: "application/json"})
		time.Sleep(2200 * time.Millisecond) // the negative entry of the document expires
		before := up.Count("GET", "/pkg/gone")
		s := a.artifact(rem, "pkg/gone/-/gone-1.0.0.tgz")
		if s.status != 200 || !bytes.Equal(s.body, tarball) {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if n := up.Count("GET", "/pkg/gone") - before; n != 1 {
			t.Fatalf("the heal caused %d requests for the document, want 1", n)
		}
		if up.Count("GET", "/pkg/gone/-/gone-1.0.0.tgz") != asked {
			t.Fatal("the cached artifact was fetched again")
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE upstream_gone_at IS NOT NULL"); n != 0 {
			t.Fatalf("the mark stayed: %d", n)
		}
		waitFor(t, "b to serve the artifact again", 2*time.Second, func() bool { return b.artifact(rem, "pkg/gone/-/gone-1.0.0.tgz").status == 200 })
	})
}

// TestGoneWithRemovalTracking is the opt-out: a format that tracks removal per version
// replaces the generic rule, so the artifact of a package with a gone document is still
// served by the engine and the format reads the mark itself (R-REPO-029).
func TestGoneWithRemovalTracking(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/tracked", proxytest.Route{Body: []byte(`{}`), Type: "application/json"})
	up.Set("/pkg/tracked/-/tracked-1.tgz", proxytest.Route{Body: []byte("bytes"), Type: "application/octet-stream"})
	rem := e.remote("tracked", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 1 })
	a := e.newReplica("a", replicaOpts{})
	removal := func(rq *proxy.Request) { rq.Adapter = &proxytest.Adapter{Removal: true} }
	_ = a.metadata(rem, "pkg/tracked", removal)
	_ = a.artifact(rem, "pkg/tracked/-/tracked-1.tgz", removal)
	time.Sleep(1200 * time.Millisecond)
	up.Remove("/pkg/tracked")
	_ = a.metadata(rem, "pkg/tracked", removal)

	st, err := a.engine.EntryState(context.Background(), rem.ID(), "pkg/tracked")
	if err != nil || !st.Stored || st.GoneAt.IsZero() {
		t.Fatalf("EntryState = %+v, %v: the format reads the core's mark", st, err)
	}
	if s := a.artifact(rem, "pkg/tracked/-/tracked-1.tgz", removal); s.status != 200 {
		t.Fatalf("the engine served %d; the format decides for a package it tracks", s.status)
	}
}
