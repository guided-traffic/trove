//go:build integration

package proxy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// The two parts of a fetch slot (R-REPO-040, R-REPO-055, Q-REPO-235): the repository's share ends with the upstream exchange, the
// replica's slot with the fetch. A commit that is held back on purpose stands for a database that stalls.

// commitGate holds back the commits it selects until it is let go. Every test lets go in its cleanup, so that no request is left
// blocked.
type commitGate struct {
	match func(path string) bool
	open  chan struct{}
	once  sync.Once

	mu   sync.Mutex
	held []string
}

func newCommitGate(t *testing.T, match func(path string) bool) *commitGate {
	g := &commitGate{match: match, open: make(chan struct{})}
	t.Cleanup(g.let)
	return g
}

// let lets go of every held commit and of the ones that follow.
func (g *commitGate) let() { g.once.Do(func() { close(g.open) }) }

// enter blocks a selected commit until the gate is let go.
func (g *commitGate) enter(ctx context.Context, path string) {
	if !g.match(path) {
		return
	}
	g.mu.Lock()
	g.held = append(g.held, path)
	g.mu.Unlock()
	select {
	case <-g.open:
	case <-ctx.Done():
	}
}

// count is how many commits were held back so far.
func (g *commitGate) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.held)
}

// has reports whether the commit of a path (a suffix of it) is held back.
func (g *commitGate) has(suffix string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.held {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

// gatedItems holds the commit of an artifact.
type gatedItems struct {
	proxy.ItemStore
	g *commitGate
}

func (s gatedItems) Commit(ctx context.Context, spec items.CommitSpec) (items.CommitResult, error) {
	s.g.enter(ctx, spec.Path.Path)
	return s.ItemStore.Commit(ctx, spec)
}

func (g *commitGate) items() func(proxy.ItemStore) proxy.ItemStore {
	return func(s proxy.ItemStore) proxy.ItemStore { return gatedItems{ItemStore: s, g: g} }
}

// gatedBlobs holds the commit of a metadata document; the blob store knows no path, so the gate's match sees "".
type gatedBlobs struct {
	proxy.BlobStore
	g *commitGate
}

func (s gatedBlobs) Commit(ctx context.Context, sp *blob.Spooled, link blob.LinkFunc) (blob.CommitResult, error) {
	s.g.enter(ctx, "")
	return s.BlobStore.Commit(ctx, sp, link)
}

func (g *commitGate) blobs() func(proxy.BlobStore) proxy.BlobStore {
	return func(s proxy.BlobStore) proxy.BlobStore { return gatedBlobs{BlobStore: s, g: g} }
}

// only selects the commits of the given paths.
func only(paths ...string) func(string) bool {
	return func(p string) bool {
		for _, x := range paths {
			if p == x {
				return true
			}
		}
		return false
	}
}

// delayed is a tarball route that the counting handler holds for d. The handler does not flush, so the count falls before a client
// can read the last byte.
func delayed(c *concurrency, d time.Duration, body []byte) proxytest.Route {
	return proxytest.Route{Handler: func(w http.ResponseWriter, _ *http.Request) {
		c.enter()
		defer c.leave()
		time.Sleep(d)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}}
}

// recordOf is the upstream log record of a request for a path.
func recordOf(log *syncBuffer, path string) map[string]any {
	for _, l := range jsonLines(log.String()) {
		if u, _ := l["url"].(string); strings.HasSuffix(u, path) {
			return l
		}
	}
	return nil
}

func TestR_REPO_040_ABlockedCommitDoesNotHoldTheUpstreamShare(t *testing.T) {
	const a, b = "pkg/a/-/a-1.tgz", "pkg/b/-/b-1.tgz"
	bodyA, bodyB := []byte("tarball-a"), []byte("tarball-b")

	t.Run("a tarball", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+a, proxytest.Route{Body: bodyA, Type: "application/octet-stream", Delay: 300 * time.Millisecond})
		up.Set("/"+b, proxytest.Route{Body: bodyB, Type: "application/octet-stream"})
		rem := e.remote("share-tarball", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 1 })
		g := newCommitGate(t, only(a))
		var log syncBuffer
		r := e.newReplica("a", replicaOpts{items: g.items(), upstreamLog: &log})

		gotA := make(chan served, 1)
		go func() { gotA <- r.artifact(rem, a) }()
		waitFor(t, "the commit of the first tarball to be held", 10*time.Second, func() bool { return g.has(a) })

		gotB := make(chan served, 1)
		go func() { gotB <- r.artifact(rem, b) }()
		waitFor(t, "the upstream to receive the second tarball while the first commit is held", 2*time.Second,
			func() bool { return up.Count("GET", "/"+b) == 1 })
		select {
		case s := <-gotB:
			if s.status != 200 || !bytes.Equal(s.body, bodyB) {
				t.Fatalf("the second tarball: %d %s (%v)", s.status, s.body, s.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the second tarball was not answered while the first commit is held")
		}
		select {
		case <-gotA:
			t.Fatal("the first tarball was answered before its commit was let go")
		default:
		}
		recA, recB := recordOf(&log, a), recordOf(&log, b)
		if recA == nil || recB == nil {
			t.Fatalf("records: %v %v", recA, recB)
		}
		waitB, okB := recB["slot_wait_ms"].(float64)
		durA, okA := recA["duration_ms"].(float64)
		if !okA || !okB || waitB >= durA {
			t.Fatalf("slot_wait_ms of the second tarball %v, duration_ms of the first %v", recB["slot_wait_ms"], recA["duration_ms"])
		}

		g.let()
		select {
		case s := <-gotA:
			if s.status != 200 || !bytes.Equal(s.body, bodyA) {
				t.Fatalf("the first tarball: %d %s (%v)", s.status, s.body, s.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the first tarball was not answered after its commit was let go")
		}
		for _, p := range []string{a, b} {
			if n := e.number("SELECT count(*) FROM items WHERE path = $1", p); n != 1 {
				t.Errorf("%s: %d items", p, n)
			}
			if n := up.Count("GET", "/"+p); n != 1 {
				t.Errorf("%s: the upstream served it %d times", p, n)
			}
		}
	})

	t.Run("a document", func(t *testing.T) {
		const docA, docB = "pkg/doc-a", "pkg/doc-b"
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+docA, proxytest.Route{Body: []byte(`{"name":"a"}`), Type: "application/json", Delay: 300 * time.Millisecond})
		up.Set("/"+docB, proxytest.Route{Body: []byte(`{"name":"b"}`), Type: "application/json"})
		rem := e.remote("share-document", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 1 })
		var commits atomic.Int32
		g := newCommitGate(t, func(string) bool { return commits.Add(1) == 1 }) // the first document's commit
		var log syncBuffer
		r := e.newReplica("a", replicaOpts{blobs: g.blobs(), upstreamLog: &log})

		gotA := make(chan served, 1)
		go func() { gotA <- r.metadata(rem, docA) }()
		waitFor(t, "the commit of the first document to be held", 10*time.Second, func() bool { return g.count() == 1 })

		gotB := make(chan served, 1)
		go func() { gotB <- r.metadata(rem, docB) }()
		waitFor(t, "the upstream to receive the second document while the first commit is held", 2*time.Second,
			func() bool { return up.Count("GET", "/"+docB) == 1 })
		select {
		case s := <-gotB:
			if s.status != 200 || string(s.body) != `{"name":"b"}` {
				t.Fatalf("the second document: %d %s (%v)", s.status, s.body, s.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the second document was not answered while the first commit is held")
		}
		recA, recB := recordOf(&log, "/"+docA), recordOf(&log, "/"+docB)
		waitB, okB := recB["slot_wait_ms"].(float64)
		durA, okA := recA["duration_ms"].(float64)
		if !okA || !okB || waitB >= durA {
			t.Fatalf("slot_wait_ms of the second document %v, duration_ms of the first %v", recB["slot_wait_ms"], recA["duration_ms"])
		}

		g.let()
		select {
		case s := <-gotA:
			if s.status != 200 || string(s.body) != `{"name":"a"}` {
				t.Fatalf("the first document: %d %s (%v)", s.status, s.body, s.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the first document was not answered after its commit was let go")
		}
		for _, p := range []string{docA, docB} {
			if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE path = $1", p); n != 1 {
				t.Errorf("%s: %d entries", p, n)
			}
			if n := up.Count("GET", "/"+p); n != 1 {
				t.Errorf("%s: the upstream served it %d times", p, n)
			}
		}
	})
}

// armedGate holds back the first transaction or negative-cache write that follows its arming, until it is let go.
type armedGate struct {
	armed atomic.Bool
	*commitGate
}

func newArmedGate(t *testing.T) *armedGate {
	return &armedGate{commitGate: newCommitGate(t, func(string) bool { return true })}
}

// hold blocks the caller when the gate is armed, once.
func (g *armedGate) hold(ctx context.Context) {
	if g.armed.CompareAndSwap(true, false) {
		g.enter(ctx, "armed")
	}
}

type gatedRunner struct {
	meta.Runner
	g *armedGate
}

func (r gatedRunner) InTx(ctx context.Context, fn func(tx meta.Tx) error) error {
	r.g.hold(ctx)
	return r.Runner.InTx(ctx, fn)
}

type gatedNegatives struct {
	cluster.NegativeStore
	g *armedGate
}

func (n gatedNegatives) Record(ctx context.Context, repoID int64, f cluster.FlightKey, e cluster.NegativeEntry, ttl time.Duration) error {
	n.g.hold(ctx)
	return n.NegativeStore.Record(ctx, repoID, f, e, ttl)
}

// recordWith is the upstream log record of a request for a path that ended with a status.
func recordWith(log *syncBuffer, path string, status int) map[string]any {
	for _, l := range jsonLines(log.String()) {
		if u, _ := l["url"].(string); strings.HasSuffix(u, path) && l["status"] == float64(status) {
			return l
		}
	}
	return nil
}

func TestR_REPO_040_AHeldWriteAfterA304DoesNotHoldTheUpstreamShare(t *testing.T) {
	const docA, docB = "pkg/doc-a", "pkg/doc-b"
	e := newEnv(t)
	up := e.upstream()
	g := newArmedGate(t)
	up.Set("/"+docA, proxytest.Route{Handler: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			g.armed.Store(true) // the first transaction after the 304 is held back
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(`{"name":"a"}`))
	}})
	up.Set("/"+docB, proxytest.Route{Body: []byte(`{"name":"b"}`), Type: "application/json"})
	rem := e.remote("share-304", up, func(s *repository.RemoteSettings) {
		s.Limits.MaxConcurrentFetches = 1
		s.Cache.MetadataTTLSeconds = 1
	})
	var log syncBuffer
	r := e.newReplica("a", replicaOpts{runner: func(in meta.Runner) meta.Runner { return gatedRunner{Runner: in, g: g} }, upstreamLog: &log})

	if s := r.metadata(rem, docA); s.status != 200 {
		t.Fatalf("the first fetch: %d (%v)", s.status, s.err)
	}
	time.Sleep(1200 * time.Millisecond) // the document is stale now
	gotA := make(chan served, 1)
	go func() { gotA <- r.metadata(rem, docA) }()
	waitFor(t, "the revalidation to be held after the 304", 10*time.Second, func() bool { return g.count() == 1 })

	gotB := make(chan served, 1)
	go func() { gotB <- r.metadata(rem, docB) }()
	waitFor(t, "the upstream to receive the second document while the 304's write is held", 2*time.Second, func() bool { return up.Count("GET", "/"+docB) == 1 })
	select {
	case s := <-gotB:
		if s.status != 200 {
			t.Fatalf("the second document: %d (%v)", s.status, s.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second document was not answered while the 304's write is held")
	}
	g.let()
	select {
	case s := <-gotA:
		if s.status != 200 || string(s.body) != `{"name":"a"}` {
			t.Fatalf("the revalidated document: %d %s (%v)", s.status, s.body, s.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the revalidation did not end")
	}
	rec := recordWith(&log, "/"+docA, http.StatusNotModified)
	if rec == nil {
		t.Fatalf("no record of the 304 in %s", log.String())
	}
	if cs, _ := rec["cache_status"].(string); !strings.Contains(cs, "stale") {
		t.Fatalf("cache_status of the 304's record = %#v", rec["cache_status"])
	}
}

func TestR_REPO_040_AHeldWriteAfterA404DoesNotHoldTheUpstreamShare(t *testing.T) {
	const docA, docB = "pkg/gone-a", "pkg/doc-b"
	e := newEnv(t)
	up := e.upstream()
	up.Set("/"+docA, proxytest.Route{Status: http.StatusNotFound})
	up.Set("/"+docB, proxytest.Route{Body: []byte(`{"name":"b"}`), Type: "application/json"})
	rem := e.remote("share-404", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 1 })
	g := newArmedGate(t)
	g.armed.Store(true) // the first negative entry is held back
	r := e.newReplica("a", replicaOpts{negatives: func(in cluster.NegativeStore) cluster.NegativeStore { return gatedNegatives{NegativeStore: in, g: g} }})

	gotA := make(chan served, 1)
	go func() { gotA <- r.metadata(rem, docA) }()
	waitFor(t, "the negative entry to be held back", 10*time.Second, func() bool { return g.count() == 1 })

	gotB := make(chan served, 1)
	go func() { gotB <- r.metadata(rem, docB) }()
	waitFor(t, "the upstream to receive the second document while the 404's write is held", 2*time.Second, func() bool { return up.Count("GET", "/"+docB) == 1 })
	select {
	case s := <-gotB:
		if s.status != 200 {
			t.Fatalf("the second document: %d (%v)", s.status, s.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second document was not answered while the 404's write is held")
	}
	g.let()
	select {
	case s := <-gotA:
		if s.status != http.StatusNotFound {
			t.Fatalf("the missing document: %d (%v)", s.status, s.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first request did not end")
	}
}

// A pass's fill that joined a client's flight, found nothing to read after it and fetched for itself has used its one try: a
// failure after its own spool is the pass's, and the pass fetches no second time (Q-REPO-180).
func TestR_REPO_040_AFillWhoseOwnFetchFailedAfterItsSpoolFetchesNoSecondTime(t *testing.T) {
	const path = "pkg/j/-/j-1.tgz"
	body := proxytest.Body("joined", 100_000) // above the withheld tail: the client's flight streams and can end unstored
	e := newEnv(t)
	up := e.upstream()
	var hits atomic.Int32
	var firstEnd atomic.Int64 // when the client's fetch got its answer, in unix nanoseconds
	up.Set("/"+path, proxytest.Route{Handler: func(w http.ResponseWriter, _ *http.Request) {
		first := hits.Add(1) == 1
		if first {
			time.Sleep(800 * time.Millisecond) // the client's fetch, which the fill joins
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
		if first {
			firstEnd.Store(time.Now().UnixNano())
		}
	}})
	rem := e.remote("fill-joined", up)
	r := e.newReplica("a", replicaOpts{})
	r.obj.down.Store(true) // the client's flight cannot store: it ends unusable for whoever joined it

	clientDone := make(chan served, 1)
	go func() { clientDone <- r.artifact(rem, path) }()
	waitFor(t, "the client's fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/"+path) == 1 })

	rq := r.bg(rem, path, proxy.Artifact, 1, "job-1")
	rq.Expected = proxy.Digests{SHA256: make([]byte, 32)} // the own fetch fails after its spool
	rq.AlwaysVerify = true
	started := time.Now()
	_, err := r.engine.Fill(context.Background(), rq)
	ended := time.Now()
	// The guard is only exercised if the fill joined the client's flight: it started while the flight was open (200 ms or more
	// before the client's answer) and returned only after the flight ended.
	if end := firstEnd.Load(); end == 0 || end-started.UnixNano() < int64(200*time.Millisecond) || ended.UnixNano() < end {
		t.Fatalf("the fill did not join the client's open flight: started %v, client answered at %v, fill ended %v", started, time.Unix(0, firstEnd.Load()), ended)
	}
	if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeUpstreamIntegrityMismatch {
		t.Fatalf("the fill: %v, want the integrity mismatch of its own fetch", err)
	}
	<-clientDone // the client's answer is not the subject: its flight ended without a stored item
	if n := up.Count("GET", "/"+path); n != 2 {
		t.Fatalf("the upstream served the item %d times, want the client's and the fill's own", n)
	}
}

func TestR_REPO_040_TheUpstreamBoundHoldsWhileEveryCommitIsHeld(t *testing.T) {
	const n = 20
	path := func(i int) string { return "pkg/u/-/u-" + strconv.Itoa(i) + ".tgz" }
	setup := func(t *testing.T, budget int, mutate func(*config.Proxy)) (*proxytest.Upstream, *concurrency, *commitGate, *replica, proxy.Remote) {
		e := newEnv(t)
		up := e.upstream()
		c := &concurrency{}
		for i := range n {
			up.Set("/"+path(i), delayed(c, 100*time.Millisecond, []byte("tarball-"+strconv.Itoa(i))))
		}
		rem := e.remote("bound", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = budget })
		g := newCommitGate(t, func(string) bool { return true })
		r := e.newReplica("a", replicaOpts{items: g.items(), proxy: mutate})
		return up, c, g, r, rem
	}
	// run sends the requests and returns the channel their statuses arrive on.
	run := func(r *replica, rem proxy.Remote) chan int {
		out := make(chan int, n)
		for i := range n {
			go func() { out <- r.artifact(rem, path(i)).status }()
		}
		return out
	}

	t.Run("a budget of 2 serves all 20 from the upstream with at most 2 open", func(t *testing.T) {
		up, c, g, r, rem := setup(t, 2, nil)
		out := run(r, rem)
		waitFor(t, "the upstream to serve all 20 tarballs while every commit is held", 20*time.Second, func() bool { return up.Total() == n })
		if p := c.peak.Load(); p > 2 || p < 2 {
			t.Fatalf("the upstream held %d connections open at most, want the budget of 2", p)
		}
		g.let()
		for range n {
			select {
			case s := <-out:
				if s != 200 {
					t.Fatalf("status %d", s)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("a request was not answered after the commits were let go")
			}
		}
	})

	t.Run("a replica limit of 3 holds the fourth fetch until a commit is let go", func(t *testing.T) {
		up, _, g, r, rem := setup(t, 2, func(p *config.Proxy) { p.MaxConcurrentFetches = 3 })
		out := run(r, rem)
		waitFor(t, "the upstream to serve 3 tarballs", 10*time.Second, func() bool { return up.Total() == 3 })
		time.Sleep(time.Second)
		if got := up.Total(); got != 3 {
			t.Fatalf("the upstream served %d tarballs while 3 commits are held, the replica's limit is 3", got)
		}
		g.let()
		for range n {
			select {
			case s := <-out:
				if s != 200 {
					t.Fatalf("status %d", s)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("a request was not answered after the commits were let go")
			}
		}
	})
}

func TestR_REPO_055_ABackgroundFillWhoseCommitIsHeldIsNotPreempted(t *testing.T) {
	const p, q = "pkg/p/-/p-1.tgz", "pkg/q/-/q-1.tgz"
	e := newEnv(t)
	up := e.upstream()
	up.Set("/"+p, proxytest.Route{Body: []byte("tarball-p"), Type: "application/octet-stream"})
	up.Set("/"+q, proxytest.Route{Body: []byte("tarball-q"), Type: "application/octet-stream"})
	for _, c := range []string{"c1", "c2"} {
		up.Set("/pkg/"+c+"/-/"+c+"-1.tgz", proxytest.Route{Body: []byte(c), Type: "application/octet-stream", Delay: 500 * time.Millisecond})
	}
	// maxConcurrentFetches 2: the background share is B = 1 and the repository's share is 2.
	rem := e.remote("bg-commit", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 2 })
	g := newCommitGate(t, only(p))
	r := e.newReplica("a", replicaOpts{items: g.items(), signals: true})

	type result struct {
		out proxy.FillOutcome
		err error
	}
	done := make(chan result, 1)
	go func() { o, err := r.fill(rem, p, 1); done <- result{o, err} }()
	waitFor(t, "the commit of the background fill to be held", 10*time.Second, func() bool { return g.has(p) })

	// Two client fetches in flight at once: the second would have to pre-empt the background fetch if it still held its share.
	type clientResult struct {
		name string
		s    served
	}
	clients := make(chan clientResult, 2)
	for _, c := range []string{"c1", "c2"} {
		go func() { clients <- clientResult{c, r.artifact(rem, "pkg/"+c+"/-/"+c+"-1.tgz")} }()
	}
	for _, c := range []string{"c1", "c2"} {
		waitFor(t, "the client fetch "+c+" to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/"+c+"/-/"+c+"-1.tgz") == 1 })
	}
	for range 2 {
		select {
		case res := <-clients:
			if res.s.status != 200 || string(res.s.body) != res.name {
				t.Fatalf("%s: %d %s (%v)", res.name, res.s.status, res.s.body, res.s.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a client fetch was not answered")
		}
	}
	// The signal of another process pre-empts a running background fetch; this one is storing.
	r.signals.Publish(cluster.AreaMirrorYield, rem.Repo.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := r.engine.Fill(ctx, r.bg(rem, q, proxy.Artifact, 1, "job-1")); err == nil {
		t.Fatal("a background fetch started inside the window of the signal: the signal did not arrive")
	}
	select {
	case res := <-done:
		t.Fatalf("the fill ended while its commit is held: %+v %v", res.out, res.err)
	default:
	}

	g.let()
	select {
	case res := <-done:
		if errors.Is(res.err, proxy.ErrPreempted) || res.err != nil || res.out.Bytes <= 0 {
			t.Fatalf("the fill: %+v %v", res.out, res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the fill did not end after its commit was let go")
	}
	if n := up.Count("GET", "/"+p); n != 1 {
		t.Fatalf("the upstream served the background item %d times", n)
	}
	if n := e.number("SELECT count(*) FROM items WHERE path = $1", p); n != 1 {
		t.Fatalf("%d items", n)
	}
}

func TestR_REPO_055_TheDiscoveryDocumentUnderABackgroundSlotKeepsTheItemsShare(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	var mu sync.Mutex
	starts, ends := map[string]time.Time{}, map[string]time.Time{}
	timed := func(delay time.Duration) proxytest.Route {
		return proxytest.Route{Handler: func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			starts[r.URL.Path] = time.Now()
			mu.Unlock()
			time.Sleep(delay)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("tarball"))
			mu.Lock()
			ends[r.URL.Path] = time.Now()
			mu.Unlock()
		}}
	}
	up.Set("/disc/index", proxytest.Route{Body: []byte("base v2/\n"), Type: "text/plain"})
	up.Set("/v2/pkg/x/-/x-1.tgz", timed(time.Second))
	up.Set("/v2/pkg/y/-/y-1.tgz", timed(0))
	// maxConcurrentFetches 4 and a ceiling of 1: only one background fetch of the repository runs at a time.
	rem := e.remote("bg-disc", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 4 })
	r := e.newReplica("a", replicaOpts{})
	fill := func(path string) error {
		rq := r.bg(rem, path, proxy.Artifact, 1, "job-1")
		rq.Adapter = &proxytest.Adapter{Discovery: "disc/index"}
		_, err := r.engine.Fill(context.Background(), rq)
		return err
	}

	errX := make(chan error, 1)
	go func() { errX <- fill("pkg/x/-/x-1.tgz") }()
	waitFor(t, "the discovery document to be served", 10*time.Second, func() bool { return up.Count("GET", "/disc/index") == 1 })
	errY := make(chan error, 1)
	go func() { errY <- fill("pkg/y/-/y-1.tgz") }()
	for _, c := range []chan error{errX, errY} {
		select {
		case err := <-c:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("a fill did not end")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	xEnd, yStart := ends["/v2/pkg/x/-/x-1.tgz"], starts["/v2/pkg/y/-/y-1.tgz"]
	if xEnd.IsZero() || yStart.IsZero() {
		t.Fatalf("starts %v, ends %v", starts, ends)
	}
	if yStart.Before(xEnd) {
		t.Fatalf("the second item's request started %v before the first item's body was complete: the discovery document returned the item's share",
			xEnd.Sub(yStart))
	}
}

// The slot wait of the record is the time the fetch queued for its slot. The test holds the first fetch's slot at the upstream
// until the engine itself says that the second fetch queues (the mirror_yield signal a waiting client fetch raises), lets the
// hold run for a while, and measures the events with its own clock: the second fetch started queueing before the signal and
// got its slot not before the release, so its wait lies between the time from the signal to the release and the whole duration
// of the second request. The signal is delivered at most once and throttled to once a second per repository and replica, and every
// way this coupling can break fails loudly: a lost signal fails the test after 10 s, an earlier signal only tightens the bound.
func TestR_OPS_011_TheUpstreamRecordOfAFetchCarriesItsSlotWait(t *testing.T) {
	const a, b = "pkg/a/-/a-1.tgz", "pkg/b/-/b-1.tgz"
	e := newEnv(t)
	up := e.upstream()
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	up.Set("/"+a, proxytest.Route{Handler: func(w http.ResponseWriter, req *http.Request) {
		once.Do(func() { close(reached) })
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("tarball-a"))
	}})
	up.Set("/"+b, proxytest.Route{Body: []byte("tarball-b"), Type: "application/octet-stream"})
	rem := e.remote("slot-wait", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 1 })
	var log syncBuffer
	r := e.newReplica("a", replicaOpts{upstreamLog: &log, signals: true})

	queuing := make(chan time.Time, 1)
	id := strconv.FormatInt(rem.ID(), 10)
	cancel, err := r.signals.Subscribe(context.Background(), cluster.AreaMirrorYield, func(payloads []string, _ bool) {
		if len(payloads) > 0 && !slices.Contains(payloads, id) {
			return // the signal of another repository
		}
		select {
		case queuing <- time.Now():
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	gotA := make(chan served, 1)
	go func() { gotA <- r.artifact(rem, a) }()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the first tarball did not reach the upstream")
	}
	gotB := make(chan served, 1)
	startB := time.Now()
	go func() { gotB <- r.artifact(rem, b) }()
	var queued time.Time
	select {
	case queued = <-queuing:
	case <-time.After(10 * time.Second):
		t.Fatal("the second fetch did not queue for the slot")
	}
	time.Sleep(200 * time.Millisecond) // the interval the slot stays held; the bound below is the measured one
	released := time.Now()
	close(release)
	var sb served
	select {
	case sb = <-gotB:
	case <-time.After(10 * time.Second):
		t.Fatal("the second tarball was not answered")
	}
	endB := time.Now()
	if sb.status != 200 {
		t.Fatalf("the second tarball: %d (%v)", sb.status, sb.err)
	}
	if s := <-gotA; s.status != 200 {
		t.Fatalf("the first tarball: %d (%v)", s.status, s.err)
	}
	recA, recB := recordOf(&log, a), recordOf(&log, b)
	if recA == nil || recB == nil {
		t.Fatalf("records: %v %v", recA, recB)
	}
	least, most := float64(released.Sub(queued).Microseconds())/1000, float64(endB.Sub(startB).Microseconds())/1000
	// The first fetch found a free slot: its wait is the time of taking one, far below the hold the second one measured.
	if v, ok := recA["slot_wait_ms"].(float64); !ok || v >= least {
		t.Errorf("slot_wait_ms of the first tarball = %#v, want a wait near 0 (below %v)", recA["slot_wait_ms"], least)
	}
	v, ok := recB["slot_wait_ms"].(float64)
	if !ok {
		t.Fatalf("slot_wait_ms of the second tarball = %#v", recB["slot_wait_ms"])
	}
	// The record carries microseconds as a fraction of milliseconds; a microsecond of rounding is no wait.
	const rounding = 0.001
	if v < least-rounding || v > most+rounding {
		t.Errorf("slot_wait_ms of the second tarball = %v, want within [%v (queuing signal to release), %v (whole request)]", v, least, most)
	}
}
