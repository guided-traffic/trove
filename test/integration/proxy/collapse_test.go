//go:build integration

package proxy

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// pgSample watches the database while a test runs: R-REPO-026 allows no advisory lock
// and no connection that sits idle in a transaction (R-ARCH-019). A transaction is open
// for microseconds between its BEGIN and its first statement, so a connection counts
// when it has been idle in a transaction for 250 ms: the transfers of these tests last
// seconds, and a transaction held across one would show.
type pgSample struct {
	advisory, idleInTx atomic.Int64
	detail             atomic.Value
	stop               chan struct{}
	done               chan struct{}
}

func (e *env) sampleDatabase() *pgSample {
	s := &pgSample{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			select {
			case <-s.stop:
				return
			case <-time.After(25 * time.Millisecond):
			}
			e.mu.Lock()
			var adv, idle int64
			_ = e.admin.QueryRow(context.Background(),
				`SELECT (SELECT count(*) FROM pg_locks l JOIN pg_database d ON d.oid = l.database WHERE l.locktype = 'advisory' AND d.datname = $1),
				        (SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND state LIKE 'idle in transaction%' AND now() - state_change > interval '250 milliseconds')`, e.db).Scan(&adv, &idle)
			e.mu.Unlock()
			s.advisory.Add(adv)
			s.idleInTx.Add(idle)
			if idle > 0 && s.detail.Load() == nil {
				e.mu.Lock()
				var app, q string
				var age float64
				_ = e.admin.QueryRow(context.Background(), `SELECT application_name, left(query, 200), extract(epoch FROM now() - xact_start) FROM pg_stat_activity WHERE datname = $1 AND state LIKE 'idle in transaction%' AND now() - state_change > interval '250 milliseconds' LIMIT 1`, e.db).Scan(&app, &q, &age)
				e.mu.Unlock()
				s.detail.Store(app + " | " + q + " | " + time.Duration(age*float64(time.Second)).String())
			}
		}
	}()
	return s
}

func (s *pgSample) finish() (advisory, idleInTx int64) {
	close(s.stop)
	<-s.done
	return s.advisory.Load(), s.idleInTx.Load()
}

// burst sends n requests for one artifact, half to each replica, and returns what the
// clients got.
func burst(n int, reps []*replica, rem proxy.Remote, path string, mutate ...func(*proxy.Request)) []served {
	out := make([]served, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = reps[i%len(reps)].artifact(rem, path, mutate...)
		}()
	}
	wg.Wait()
	return out
}

func collapsedOrHit(s served) bool {
	cs := s.cacheStatus()
	return strings.Contains(cs, "collapsed") || strings.HasPrefix(cs, "trove; hit")
}

// TestCollapse is the collapsing protocol of R-REPO-026 on two replicas that share
// PostgreSQL, the object store and Valkey.
func TestCollapse(t *testing.T) {
	const path = "pkg/big/-/big-1.0.0.tgz"
	tarball := proxytest.Body("big", 400_000)
	slow := proxytest.Route{Body: tarball, Type: "application/octet-stream", Delay: time.Second}

	t.Run("R-REPO-026 100 concurrent misses over two replicas cause one upstream GET and one populate event", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, slow)
		rem := e.remote("collapse", up)
		a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
		pop := proxytest.Allowed()
		sample := e.sampleDatabase()
		got := burst(100, []*replica{a, b}, rem, path, func(rq *proxy.Request) { rq.Populate = pop })
		advisory, idle := sample.finish()

		if n := up.Count("GET", "/"+path); n != 1 {
			t.Fatalf("the upstream saw %d GETs, want exactly 1", n)
		}
		collapsed := 0
		for i, s := range got {
			if s.status != 200 || !bytes.Equal(s.body, tarball) {
				t.Fatalf("request %d: %d, %d bytes (%v)", i, s.status, len(s.body), s.err)
			}
			if collapsedOrHit(s) {
				collapsed++
			}
		}
		if collapsed < 99 {
			t.Fatalf("%d of 100 responses are collapsed or hits, want at least 99", collapsed)
		}
		if pop.Records() != 1 {
			t.Fatalf("populate events = %d, want exactly 1: the number follows the fetches, not the requesters", pop.Records())
		}
		if advisory != 0 || idle != 0 {
			t.Fatalf("advisory locks seen %d times, connections idle in a transaction seen %d times (%v)", advisory, idle, sample.detail.Load())
		}
		if _, ok := getKey(t, e.valkeyAdmin(a), e.lockKey(rem, path, "")); ok {
			t.Fatal("the flight lock outlived the fetch")
		}
		if a.metric("trove_remote_collapsed_requests_total", "repo", "collapse")+b.metric("trove_remote_collapsed_requests_total", "repo", "collapse") < 90 {
			t.Fatal("trove_remote_collapsed_requests_total did not count the waiters")
		}
	})

	t.Run("R-REPO-026 with Valkey unavailable 100 requests succeed and the upstream sees at most one GET per replica", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, slow)
		rem := e.remote("collapse-down", up)
		a, b := e.newReplica("a", replicaOpts{valkey: vkUnreachable}), e.newReplica("b", replicaOpts{valkey: vkUnreachable})
		got := burst(100, []*replica{a, b}, rem, path)
		for i, s := range got {
			if s.status != 200 || !bytes.Equal(s.body, tarball) {
				t.Fatalf("request %d: %d (%v)", i, s.status, s.err)
			}
		}
		if n := up.Count("GET", "/"+path); n < 1 || n > 2 {
			t.Fatalf("the upstream saw %d GETs, want at most one per replica", n)
		}
		if got := a.metric("trove_flight_lock_bypass_total", "reason", "valkey_unavailable") + b.metric("trove_flight_lock_bypass_total", "reason", "valkey_unavailable"); got < 1 {
			t.Fatal("the bypass was not counted")
		}
	})

	t.Run("R-REPO-026 a holder that dies without releasing frees the flight within the lock TTL plus one poll", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream"})
		rem := e.remote("collapse-kill", up)
		ttl := 3 * time.Second
		b := e.newReplica("b", replicaOpts{cluster: func(c *config.Cluster) { c.FlightLockTTL = ttl }})
		// The holder is another process that takes the flight and is killed: its client
		// closes, so nothing extends or releases the lock and its key expires by itself.
		holderVK, _ := e.valkeyClient("holder", vkReal)
		holder := cluster.NewFlightLock(holderVK, cluster.FlightLockOptions{TTL: ttl, PollInterval: 100 * time.Millisecond})
		h, ok, err := holder.Acquire(context.Background(), cluster.NewFlightKey(rem.ID(), path, ""))
		if err != nil || !ok || h == nil {
			t.Fatalf("Acquire: %v %v", ok, err)
		}
		holderVK.Close()
		start := time.Now()
		got := b.artifact(rem, path)
		took := time.Since(start)
		if got.status != 200 || !bytes.Equal(got.body, tarball) {
			t.Fatalf("%d (%v)", got.status, got.err)
		}
		if took < time.Second || took > ttl+time.Second+500*time.Millisecond {
			t.Fatalf("the waiter was served after %s; the lock lives %s and the poll is 100 ms", took, ttl)
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries"); n != 1 {
			t.Fatalf("entries = %d, want one for the complete body", n)
		}
	})

	t.Run("R-REPO-026 R-ARCH-024 a holder that is cancelled releases with failed and the waiter takes over at once", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream", Chunk: 20_000, ChunkDelay: 300 * time.Millisecond})
		rem := e.remote("collapse-quiesce", up)
		a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
		first := make(chan served, 1)
		go func() { first <- a.artifact(rem, path) }()
		waitFor(t, "the first fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/"+path) == 1 })
		second := make(chan served, 1)
		go func() { second <- b.artifact(rem, path) }()
		time.Sleep(500 * time.Millisecond) // the second request waits on a's flight
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		a.engine.Quiesce(ctx)
		cancel()
		select {
		case s := <-second:
			if s.status != 200 || !bytes.Equal(s.body, tarball) {
				t.Fatalf("the waiter got %d (%v)", s.status, s.err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the waiter was not served")
		}
		// The waiter fetched the whole body itself; the time it took to take over is the
		// time it needed minus the transfer. The transfer takes 20 chunks of 300 ms.
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("took %s", took)
		}
		if s := <-first; s.status == 200 && bytes.Equal(s.body, tarball) {
			t.Fatal("the cancelled fetch's own client completed its download")
		}
		if n := up.Count("GET", "/"+path); n != 2 {
			t.Fatalf("the upstream saw %d GETs, want the cancelled one and the takeover", n)
		}
	})

	t.Run("R-REPO-026 a draining replica takes no lock and bypasses with reason draining", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, slow)
		rem := e.remote("collapse-drain", up)
		a := e.newReplica("a", replicaOpts{})
		a.engine.Drain(context.Background())
		admin := e.valkeyAdmin(a)
		res := make(chan served, 1)
		go func() { res <- a.artifact(rem, path) }()
		waitFor(t, "the fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/"+path) == 1 })
		if v, ok := getKey(t, admin, e.lockKey(rem, path, "")); ok {
			t.Fatalf("a draining replica took the lock: %q", v)
		}
		if s := <-res; s.status != 200 || !bytes.Equal(s.body, tarball) {
			t.Fatalf("%d (%v)", s.status, s.err)
		}
		if got := a.metric("trove_flight_lock_bypass_total", "reason", "draining"); got != 1 {
			t.Fatalf("trove_flight_lock_bypass_total{reason=draining} = %v", got)
		}
	})

	t.Run("R-REPO-026 a draining replica joins a flight another replica holds as a waiter", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, slow)
		rem := e.remote("collapse-drain-wait", up)
		a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
		first := make(chan served, 1)
		go func() { first <- b.artifact(rem, path) }()
		waitFor(t, "the holder to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/"+path) == 1 })
		a.engine.Drain(context.Background())
		s := a.artifact(rem, path)
		if s.status != 200 || !bytes.Equal(s.body, tarball) || !collapsedOrHit(s) {
			t.Fatalf("%d %q (%v)", s.status, s.cacheStatus(), s.err)
		}
		<-first
		if n := up.Count("GET", "/"+path); n != 1 {
			t.Fatalf("the upstream saw %d GETs", n)
		}
		if got := a.metric("trove_flight_lock_bypass_total", "reason", "draining"); got != 0 {
			t.Fatalf("the draining replica waited and still counted a bypass: %v", got)
		}
	})

	t.Run("R-REPO-026 waiters complete by polling when the outcome message never arrives", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, slow)
		rem := e.remote("collapse-poll", up)
		// The holder is a hand-written lock whose release publishes nothing; the entry is
		// stored by a replica that does not use the lock, and the key is then deleted.
		b := e.newReplica("b", replicaOpts{cluster: func(c *config.Cluster) { c.FlightLockTTL = 30 * time.Second }})
		plain := e.newReplica("plain", replicaOpts{valkey: vkUnreachable})
		admin := e.valkeyAdmin(b)
		setForeign(t, admin, e.lockKey(rem, path, ""), 30*time.Second)
		results := make(chan served, 50)
		for range 50 {
			go func() { results <- b.artifact(rem, path) }()
		}
		time.Sleep(300 * time.Millisecond)
		if s := plain.artifact(rem, path); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		released := time.Now()
		delKey(t, admin, e.lockKey(rem, path, ""))
		for range 50 {
			select {
			case s := <-results:
				if s.status != 200 || !bytes.Equal(s.body, tarball) {
					t.Fatalf("%d (%v)", s.status, s.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("a waiter was not served")
			}
		}
		if took := time.Since(released); took > time.Second {
			t.Fatalf("the waiters needed %s after the lock vanished; the poll interval is 100 ms", took)
		}
		if n := up.Count("GET", "/"+path); n != 1 {
			t.Fatalf("the upstream saw %d GETs", n)
		}
	})

	t.Run("R-REPO-026 a foreign lock delays a fetch by proxy.fetch_wait_timeout_seconds and counts wait_timeout", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream"})
		rem := e.remote("collapse-foreign", up)
		b := e.newReplica("b", replicaOpts{proxy: func(p *config.Proxy) { p.FetchWaitTimeoutSeconds = 5 }})
		setForeign(t, e.valkeyAdmin(b), e.lockKey(rem, path, ""), time.Minute)
		start := time.Now()
		s := b.artifact(rem, path)
		took := time.Since(start)
		if s.status != 200 || !bytes.Equal(s.body, tarball) {
			t.Fatalf("%d (%v)", s.status, s.err)
		}
		if took < 4*time.Second || took > 8*time.Second {
			t.Fatalf("served after %s, want about 5 s", took)
		}
		if got := b.metric("trove_flight_lock_bypass_total", "reason", "wait_timeout"); got != 1 {
			t.Fatalf("trove_flight_lock_bypass_total{reason=wait_timeout} = %v", got)
		}
	})

	t.Run("R-REPO-026 a foreign lock without expiry on an expired document serves it stale after the refresh timeout", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		doc := []byte(`{"name":"held"}`)
		up.Set("/pkg/held", proxytest.Route{Body: doc, Type: "application/json", ETag: `"h1"`})
		rem := e.remote("collapse-stale", up, func(s *repository.RemoteSettings) {
			s.Cache.MetadataTTLSeconds, s.Cache.MetadataRefreshTimeoutSeconds = 1, 2
		})
		b := e.newReplica("b", replicaOpts{proxy: func(p *config.Proxy) { p.FetchWaitTimeoutSeconds = 5 }})
		if s := b.metadata(rem, "pkg/held"); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		time.Sleep(1200 * time.Millisecond) // the document expires
		admin := e.valkeyAdmin(b)
		setForeign(t, admin, e.lockKey(rem, "pkg/held", ""), 0)
		t.Cleanup(func() { delKey(t, admin, e.lockKey(rem, "pkg/held", "")) })
		start := time.Now()
		s := b.metadata(rem, "pkg/held")
		took := time.Since(start)
		if s.status != 200 || !bytes.Equal(s.body, doc) || !strings.Contains(s.cacheStatus(), "detail=served-stale") {
			t.Fatalf("%d %q", s.status, s.cacheStatus())
		}
		if took < 1500*time.Millisecond || took > 3*time.Second {
			t.Fatalf("served stale after %s, want about 2 s", took)
		}
		waitFor(t, "the revalidation without the lock", 10*time.Second, func() bool { return up.Conditional("/pkg/held") == 1 })
		if seen := up.Seen(); seen[len(seen)-1].At.Sub(start) < 4*time.Second {
			t.Fatalf("the conditional request came %s after the request, the wait bound is 5 s", seen[len(seen)-1].At.Sub(start))
		}
		if s := b.metadata(rem, "pkg/held"); !strings.HasPrefix(s.cacheStatus(), "trove; hit; ttl=") {
			t.Fatalf("the next request is %q, want a fresh hit", s.cacheStatus())
		}
	})

	t.Run("R-REPO-026 two fetches that return different bytes leave one entry and a proxy.upstream_digest_changed event", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		var n atomic.Int64
		first, second := proxytest.Body("first", 300_000), proxytest.Body("second", 300_000)
		up.Set("/"+path, proxytest.Route{Handler: func(w http.ResponseWriter, r *http.Request) {
			body := first
			if n.Add(1) > 1 {
				body = second
			}
			time.Sleep(600 * time.Millisecond)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		}})
		rem := e.remote("collapse-dup", up)
		a, b := e.newReplica("a", replicaOpts{valkey: vkUnreachable}), e.newReplica("b", replicaOpts{valkey: vkUnreachable})
		got := burst(2, []*replica{a, b}, rem, path)
		if c := e.number("SELECT count(*) FROM remote_cache_entries WHERE kind = 'artifact'"); c != 1 {
			t.Fatalf("entries = %d, want exactly one", c)
		}
		stored := first
		if e.number("SELECT count(*) FROM blobs WHERE sha256 = $1", proxytest.SHA256(second)) == 1 &&
			e.number("SELECT count(*) FROM remote_cache_entries WHERE blob_sha256 = $1", proxytest.SHA256(second)) == 1 {
			stored = second
		}
		completed := 0
		for i, s := range got {
			switch {
			case s.aborted:
			case s.status == 200 && bytes.Equal(s.body, stored):
				completed++
			default:
				t.Fatalf("response %d: %d, %d bytes, aborted %v: neither aborted nor the stored artifact", i, s.status, len(s.body), s.aborted)
			}
		}
		if completed == 0 {
			t.Fatal("no client completed")
		}
		changed := len(a.rec.ofType(audit.EventProxyUpstreamDigestChanged)) + len(b.rec.ofType(audit.EventProxyUpstreamDigestChanged))
		if changed < 1 {
			t.Fatal("no proxy.upstream_digest_changed event")
		}
	})
}
