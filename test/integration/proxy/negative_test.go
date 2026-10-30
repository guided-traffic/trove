//go:build integration

package proxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestNegativeCache is R-REPO-028: the L1, Valkey behind it, the validation window and
// the invalidations the readers apply.
func TestNegativeCache(t *testing.T) {
	const path = "pkg/does-not-exist-4711"

	t.Run("R-REPO-028 the second request is a negative hit, on either replica", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		rem := e.remote("neg", up)
		a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
		if s := a.metadata(rem, path); s.status != 404 {
			t.Fatalf("first answer: %d %q", s.status, s.cacheStatus())
		}
		for _, r := range []*replica{a, b} {
			s := r.metadata(rem, path)
			if s.status != 404 || s.cacheStatus() != "trove; hit; detail=negative" {
				t.Fatalf("%s: %d %q", r.name, s.status, s.cacheStatus())
			}
		}
		if n := up.Count("GET", "/"+path); n != 1 {
			t.Fatalf("the upstream saw %d requests, want one for both replicas", n)
		}
		if got := b.metric("trove_remote_cache_lookups_total", "repo", "neg", "result", "negative_hit"); got != 1 {
			t.Fatalf("negative_hit = %v", got)
		}
	})

	t.Run("R-REPO-028 no negative entry is written when the caller may not fill the cache", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		rem := e.remote("neg-nofill", up)
		a := e.newReplica("a", replicaOpts{})
		s := a.artifactWith(rem, "GET", "pkg/x/-/x-1.tgz", nil, func(rq *proxy.Request) { rq.Populate = proxytest.Refused() })
		if s.status != 403 {
			t.Fatalf("%d", s.status)
		}
		if _, ok := getKey(t, e.valkeyAdmin(a), e.negativeKey(rem, "pkg/x/-/x-1.tgz", "")); ok {
			t.Fatal("a refused fill wrote a negative entry")
		}
	})

	t.Run("R-REPO-028 with Valkey unavailable ten sequential requests cause at most two upstream requests", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		rem := e.remote("neg-down", up)
		a, b := e.newReplica("a", replicaOpts{valkey: vkUnreachable}), e.newReplica("b", replicaOpts{valkey: vkUnreachable})
		for i := range 10 {
			r := a
			if i%2 == 1 {
				r = b
			}
			if s := r.metadata(rem, path); s.status != 404 {
				t.Fatalf("request %d: %d", i, s.status)
			}
		}
		if n := up.Count("GET", "/"+path); n > 2 {
			t.Fatalf("the upstream saw %d requests, want at most one per replica", n)
		}
	})

	t.Run("R-REPO-028 a lost Valkey key costs one more upstream request and the answer stays 404", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		rem := e.remote("neg-lost", up)
		a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
		_ = a.metadata(rem, path)
		// What a flush of Valkey does to this test's keys; no other tenant's keys are touched.
		delKey(t, e.valkeyAdmin(a), e.negativeKey(rem, path, ""))
		if s := b.metadata(rem, path); s.status != 404 {
			t.Fatalf("%d", s.status)
		}
		if n := up.Count("GET", "/"+path); n != 2 {
			t.Fatalf("the upstream saw %d requests, want 2", n)
		}
	})

	t.Run("R-REPO-028 an invalidation of scope negative makes both replicas ask again", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		rem := e.remote("neg-inv", up)
		a, b := e.newReplica("a", replicaOpts{invalidations: true}), e.newReplica("b", replicaOpts{invalidations: true})
		_ = a.metadata(rem, "pkg/late")
		_ = b.metadata(rem, "pkg/late")
		up.Set("/pkg/late", proxytest.Route{Body: []byte(`{"late":true}`), Type: "application/json"})
		for _, r := range []*replica{a, b} {
			if s := r.metadata(rem, "pkg/late"); s.status != 404 {
				t.Fatalf("%s sees the package inside the period: %d", r.name, s.status)
			}
		}
		time.Sleep(50 * time.Millisecond)
		if _, err := a.invSvc.Invalidate(context.Background(), repository.Caller{}, rem.Repo, "", "negative"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the invalidation on the other replica", 5*time.Second, func() bool {
			return !b.inv.NegativeValid(rem.ID(), "pkg/late", time.Now().Add(-time.Second))
		})
		if s := a.metadata(rem, "pkg/late"); s.status != 200 {
			t.Fatalf("a: %d %q", s.status, s.cacheStatus())
		}
		// b's own L1 holds the old entry; it is refused by the invalidation, and b reads the
		// stored document that a fetched.
		if s := b.metadata(rem, "pkg/late"); s.status != 200 {
			t.Fatalf("b: %d %q", s.status, s.cacheStatus())
		}
	})

	t.Run("R-REPO-028 an entry written into Valkey older than an invalidation of its path is ignored", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/forged", proxytest.Route{Body: []byte(`{"f":1}`), Type: "application/json"})
		rem := e.remote("neg-forged", up)
		a := e.newReplica("a", replicaOpts{invalidations: true})
		admin := e.valkeyAdmin(a)
		put := func(sentAt time.Time) {
			v := fmt.Sprintf(`{"s":404,"c":%d}`, sentAt.UnixMilli())
			if err := admin.Do(context.Background(), admin.B().Set().Key(e.negativeKey(rem, "pkg/forged", "")).Value(v).Px(time.Hour).Build()).Error(); err != nil {
				t.Fatal(err)
			}
		}
		put(time.Now())
		if s := a.metadata(rem, "pkg/forged"); s.status != 404 || up.Total() != 0 {
			t.Fatalf("the current forged entry should answer 404 without a request: %d, %d requests", s.status, up.Total())
		}
		b := e.newReplica("b", replicaOpts{invalidations: true})
		time.Sleep(20 * time.Millisecond)
		if _, err := b.invSvc.Invalidate(context.Background(), repository.Caller{}, rem.Repo, "", "all"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		put(time.Now().Add(-time.Minute)) // created before the invalidation
		if s := b.metadata(rem, "pkg/forged"); s.status != 200 || up.Count("GET", "/pkg/forged") != 1 {
			t.Fatalf("%d, %d requests", s.status, up.Count("GET", "/pkg/forged"))
		}
		if got := gather(b.vkReg, "trove_valkey_cache_lookups_total", "cache", "negative", "result", "rejected"); got < 1 {
			t.Fatalf("the refusal was not counted: trove_valkey_cache_lookups_total{negative,rejected} = %v", got)
		}
	})

	t.Run("R-REPO-028 a malformed entry is ignored and deleted, and one older than the period does not count", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/pkg/m", proxytest.Route{Body: []byte(`{}`), Type: "application/json"})
		rem := e.remote("neg-bad", up, func(s *repository.RemoteSettings) { s.Cache.NotFoundTTLSeconds = 60 })
		a := e.newReplica("a", replicaOpts{})
		admin := e.valkeyAdmin(a)
		key := e.negativeKey(rem, "pkg/m", "")
		set := func(v string) {
			t.Helper()
			if err := admin.Do(context.Background(), admin.B().Set().Key(key).Value(v).Px(time.Hour).Build()).Error(); err != nil {
				t.Fatal(err)
			}
		}
		set(`{"s":404,"c":"yesterday"}`)
		if s := a.metadata(rem, "pkg/m"); s.status != 200 {
			t.Fatalf("a malformed entry answered %d", s.status)
		}
		if v, ok := getKey(t, admin, key); ok {
			t.Fatalf("the malformed entry was not deleted: %q", v)
		}
		// A well-formed entry from before the period counts for nothing.
		key = e.negativeKey(rem, "pkg/old", "")
		set(fmt.Sprintf(`{"s":404,"c":%d}`, time.Now().Add(-10*time.Minute).UnixMilli()))
		b := e.newReplica("b", replicaOpts{})
		if s := b.metadata(rem, "pkg/old"); s.status != 404 || up.Count("GET", "/pkg/old") != 1 {
			t.Fatalf("an entry older than notFoundTtlSeconds counted: %d, %d requests", s.status, up.Count("GET", "/pkg/old"))
		}
	})
}
