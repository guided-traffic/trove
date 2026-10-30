//go:build integration

package proxy

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestFreshness is R-REPO-027: the period, the conditional revalidation, the refresh
// timeout with the stale copy and the detached continuation, and the period 0.
func TestFreshness(t *testing.T) {
	t.Run("R-REPO-027 within the period one request, after it one conditional request answered 304", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		doc := []byte(`{"name":"left-pad"}`)
		up.Set("/pkg/left-pad", proxytest.Route{Body: doc, Type: "application/json", ETag: `"v1"`})
		rem := e.remote("fresh", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 2 })
		r := e.newReplica("a", replicaOpts{})
		for range 2 {
			if s := r.metadata(rem, "pkg/left-pad"); s.status != 200 || !bytes.Equal(s.body, doc) {
				t.Fatalf("%d", s.status)
			}
		}
		if n := up.Total(); n != 1 {
			t.Fatalf("two requests within the period caused %d upstream requests", n)
		}
		time.Sleep(2200 * time.Millisecond)
		s := r.metadata(rem, "pkg/left-pad")
		if s.status != 200 || !bytes.Equal(s.body, doc) || s.cacheStatus() != "trove; fwd=stale; fwd-status=304" {
			t.Fatalf("%d %q", s.status, s.cacheStatus())
		}
		if up.Conditional("/pkg/left-pad") != 1 || up.Seen()[1].IfNoneMatch != `"v1"` {
			t.Fatalf("the revalidation was not an If-None-Match request: %+v", up.Seen())
		}
		// A 304 renewed the period: the next request is a hit again.
		if s := r.metadata(rem, "pkg/left-pad"); !strings.HasPrefix(s.cacheStatus(), "trove; hit; ttl=") {
			t.Fatalf("%q", s.cacheStatus())
		}
	})

	t.Run("R-REPO-027 an If-Modified-Since revalidation when the entry has no ETag", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		modified := time.Now().Add(-time.Hour).Truncate(time.Second)
		up.Set("/pkg/lm", proxytest.Route{Body: []byte("{}"), Type: "application/json", Modified: modified})
		rem := e.remote("fresh-lm", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 1 })
		r := e.newReplica("a", replicaOpts{})
		_ = r.metadata(rem, "pkg/lm")
		time.Sleep(1200 * time.Millisecond)
		if s := r.metadata(rem, "pkg/lm"); s.cacheStatus() != "trove; fwd=stale; fwd-status=304" {
			t.Fatalf("%q", s.cacheStatus())
		}
	})

	t.Run("R-REPO-027 a refresh past the refresh timeout serves the stale copy and the flight stores the new one", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/slow", proxytest.Route{Body: []byte(`{"v":1}`), Type: "application/json", ETag: `"s1"`})
		rem := e.remote("fresh-slow", up, func(s *repository.RemoteSettings) {
			s.Cache.MetadataTTLSeconds, s.Cache.MetadataRefreshTimeoutSeconds = 1, 1
		})
		r := e.newReplica("a", replicaOpts{})
		_ = r.metadata(rem, "pkg/slow")
		time.Sleep(1200 * time.Millisecond)
		up.Set("/pkg/slow", proxytest.Route{Body: []byte(`{"v":2}`), Type: "application/json", ETag: `"s2"`, Delay: 3 * time.Second})
		start := time.Now()
		s := r.metadata(rem, "pkg/slow")
		took := time.Since(start)
		if string(s.body) != `{"v":1}` || s.cacheStatus() != "trove; fwd=stale; detail=served-stale" {
			t.Fatalf("%q %q", s.body, s.cacheStatus())
		}
		if took < 800*time.Millisecond || took > 2*time.Second {
			t.Fatalf("the stale copy came after %s, want about the refresh timeout of 1 s", took)
		}
		waitFor(t, "the detached refresh to store the new document", 10*time.Second, func() bool {
			return strings.HasPrefix(r.metadata(rem, "pkg/slow").cacheStatus(), "trove; hit; ttl=")
		})
		if s := r.metadata(rem, "pkg/slow"); string(s.body) != `{"v":2}` {
			t.Fatalf("the next request sees %q", s.body)
		}
	})

	t.Run("R-REPO-027 a period of 0 revalidates every request and collapses 20 concurrent ones to one", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/zero", proxytest.Route{Body: []byte(`{"z":0}`), Type: "application/json", ETag: `"z"`})
		rem := e.remote("fresh-zero", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 0 })
		a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
		_ = a.metadata(rem, "pkg/zero")
		for range 5 {
			if s := a.metadata(rem, "pkg/zero"); s.status != 200 {
				t.Fatalf("%d", s.status)
			}
		}
		if n := up.Conditional("/pkg/zero"); n != 5 {
			t.Fatalf("five sequential requests caused %d conditional requests", n)
		}
		up.Set("/pkg/zero", proxytest.Route{Body: []byte(`{"z":0}`), Type: "application/json", ETag: `"z"`, Delay: time.Second})
		before := up.Conditional("/pkg/zero")
		results := make(chan served, 20)
		for i := range 20 {
			r := a
			if i%2 == 1 {
				r = b
			}
			go func() { results <- r.metadata(rem, "pkg/zero") }()
		}
		for range 20 {
			if s := <-results; s.status != 200 || string(s.body) != `{"z":0}` {
				t.Fatalf("%d %q", s.status, s.body)
			}
		}
		if n := up.Conditional("/pkg/zero") - before; n != 1 {
			t.Fatalf("20 concurrent requests caused %d conditional requests, want 1", n)
		}
	})

	t.Run("R-REPO-027 an entry validated after a request looked it up counts as fresh for that request", func(t *testing.T) {
		// The period is 0: without the rule every waiter would fetch again after the holder
		// released the lock. The burst above shows it across replicas; this is the same
		// rule for the requests of one replica.
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/rule", proxytest.Route{Body: []byte("{}"), Type: "application/json", ETag: `"r"`, Delay: 500 * time.Millisecond})
		rem := e.remote("fresh-rule", up, func(s *repository.RemoteSettings) { s.Cache.MetadataTTLSeconds = 0 })
		a := e.newReplica("a", replicaOpts{valkey: vkUnreachable})
		_ = a.metadata(rem, "pkg/rule")
		before := up.Total()
		results := make(chan served, 10)
		for range 10 {
			go func() { results <- a.metadata(rem, "pkg/rule") }()
		}
		for range 10 {
			if s := <-results; s.status != 200 {
				t.Fatalf("%d", s.status)
			}
		}
		if n := up.Total() - before; n != 1 {
			t.Fatalf("10 concurrent requests caused %d upstream requests", n)
		}
	})
}

// TestEnsureCurrent is Q-NPM-13 at the engine: a document validated before the restore
// is revalidated even when its period has not run out.
func TestEnsureCurrent(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/cur", proxytest.Route{Body: []byte(`{"c":1}`), Type: "application/json", ETag: `"c"`})
	rem := e.remote("current", up)
	r := e.newReplica("a", replicaOpts{})
	if s := r.metadata(rem, "pkg/cur"); s.status != 200 {
		t.Fatalf("%d", s.status)
	}
	call := func(restored time.Time) (string, int) {
		d, cur, err := r.engine.EnsureCurrent(context.Background(), r.request(rem, "pkg/cur", proxy.Metadata), restored)
		if err != nil {
			t.Fatal(err)
		}
		return d.Status.Header(), int(cur)
	}
	t.Run("a fresh document validated after the restore is current", func(t *testing.T) {
		if hdr, cur := call(time.Now().Add(-time.Hour)); !strings.HasPrefix(hdr, "trove; hit") || cur != int(proxy.Current) {
			t.Fatalf("%q %d", hdr, cur)
		}
	})
	t.Run("a fresh document validated before the restore is revalidated", func(t *testing.T) {
		before := up.Conditional("/pkg/cur")
		hdr, cur := call(time.Now().Add(time.Hour))
		if hdr != "trove; fwd=stale; fwd-status=304" || cur != int(proxy.Revalidated) || up.Conditional("/pkg/cur") != before+1 {
			t.Fatalf("%q %d", hdr, cur)
		}
	})
	t.Run("an upstream that cannot be asked leaves the stored document and the currency unknown", func(t *testing.T) {
		up.Set("/pkg/cur", proxytest.Route{Status: 503})
		hdr, cur := call(time.Now().Add(time.Hour))
		if !strings.Contains(hdr, "detail=served-stale") || cur != int(proxy.Unknown) {
			t.Fatalf("%q %d", hdr, cur)
		}
	})
}
