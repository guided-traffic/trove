//go:build integration

package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// payloadHooks records what reaches the storage hook of a commit.
type payloadHooks struct {
	mu       sync.Mutex
	payloads map[string]any
}

func (h *payloadHooks) OnCommit(_ context.Context, _ meta.Tx, e items.CommitEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.payloads == nil {
		h.payloads = map[string]any{}
	}
	h.payloads[e.Item.Path] = e.Payload
	return nil
}

func (*payloadHooks) OnItemsRemoved(context.Context, meta.Tx, items.RemovedEvent) error { return nil }

func (h *payloadHooks) payload(path string) (any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.payloads[path]
	return v, ok
}

// watchedWriter tells when the first byte or the status line reaches the client.
type watchedWriter struct {
	*httptest.ResponseRecorder
	once  sync.Once
	first chan struct{}
}

func newWatchedWriter() *watchedWriter {
	return &watchedWriter{ResponseRecorder: httptest.NewRecorder(), first: make(chan struct{})}
}

func (w *watchedWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.first) })
	return w.ResponseRecorder.Write(b)
}

func (w *watchedWriter) WriteHeader(code int) {
	w.once.Do(func() { close(w.first) })
	w.ResponseRecorder.WriteHeader(code)
}

func (w *watchedWriter) reached() bool {
	select {
	case <-w.first:
		return true
	default:
		return false
	}
}

// TestFillHook is WP3.0a: the fill hook of the remote adapter runs on the spooled artifact after the digest check
// and before the commit, and no byte reaches a client before it accepted the artifact.
func TestFillHook(t *testing.T) {
	const path = "pkg/hook/-/hook-1.0.0.tgz"
	tarball := proxytest.Body("hook", 400_000)

	t.Run("R-NUGET-015 a refusal answers 502 to every request of the flight, stores nothing and asks the hook once", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream", Delay: 700 * time.Millisecond})
		rem := e.remote("hook-refuse", up)
		a := e.newReplica("a", replicaOpts{})
		ad := &proxytest.Adapter{Inspect: func(string, *items.Spooled) (any, error) {
			return nil, errors.New("the package declares another identity")
		}}
		use := func(rq *proxy.Request) { rq.Adapter = ad }

		got := burst(40, []*replica{a}, rem, path, use)
		for i, s := range got {
			if s.aborted || s.status != http.StatusBadGateway {
				t.Fatalf("request %d: status %d, aborted %v (%v); every waiter must get a 502 and no cut transfer", i, s.status, s.aborted, s.err)
			}
		}
		if n := up.Count("GET", "/"+path); n != 1 {
			t.Fatalf("the upstream saw %d GETs, want 1 for the whole flight", n)
		}
		if n := ad.Inspects(); n != 1 {
			t.Fatalf("the hook ran %d times, want once", n)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = $1", path); n != 0 {
			t.Fatalf("%d items after a refusal", n)
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries"); n != 0 {
			t.Fatalf("%d cache entries after a refusal", n)
		}
		if n := e.number("SELECT count(*) FROM blobs WHERE sha256 = $1", proxytest.SHA256(tarball)); n != 0 {
			t.Fatalf("%d blobs after a refusal", n)
		}
		if n := e.number("SELECT count(*) FROM remote_upstream_state WHERE consecutive_failures > 0"); n != 0 {
			t.Fatalf("a refused package counted toward the circuit")
		}
		// No negative entry: the next request asks the upstream again.
		if s := a.artifact(rem, path, use); s.status != http.StatusBadGateway {
			t.Fatalf("the next request: %d", s.status)
		}
		if n := up.Count("GET", "/"+path); n != 2 {
			t.Fatalf("the upstream saw %d GETs after the second request, want 2 (no negative entry)", n)
		}
	})

	t.Run("R-NUGET-015 over two replicas every request gets the 502 and each replica asks the upstream and the hook at most once", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream", Delay: 700 * time.Millisecond})
		rem := e.remote("hook-refuse-2", up)
		a, b := e.newReplica("a", replicaOpts{}), e.newReplica("b", replicaOpts{})
		ad := &proxytest.Adapter{Inspect: func(string, *items.Spooled) (any, error) {
			return nil, errors.New("the package declares another identity")
		}}
		for i, s := range burst(40, []*replica{a, b}, rem, path, func(rq *proxy.Request) { rq.Adapter = ad }) {
			if s.aborted || s.status != http.StatusBadGateway {
				t.Fatalf("request %d: status %d, aborted %v (%v)", i, s.status, s.aborted, s.err)
			}
		}
		// The holder of the cross-replica flight failed, so the other replica fetches for itself (R-REPO-026 step 4).
		if n := up.Count("GET", "/"+path); n < 1 || n > 2 || ad.Inspects() != n {
			t.Fatalf("the upstream saw %d GETs and the hook ran %d times, want one of each per replica at most", n, ad.Inspects())
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = $1", path); n != 0 {
			t.Fatalf("%d items after a refusal", n)
		}
	})

	t.Run("R-NUGET-015 no byte reaches the leader's client before the hook accepted the artifact", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream"})
		rem := e.remote("hook-hold", up)
		r := e.newReplica("a", replicaOpts{})
		entered, release := make(chan struct{}), make(chan struct{})
		ad := &proxytest.Adapter{Inspect: func(string, *items.Spooled) (any, error) {
			close(entered)
			<-release
			return nil, nil
		}}
		w := newWatchedWriter()
		done := make(chan error, 1)
		go func() {
			rq := r.request(rem, path, proxy.Artifact, func(rq *proxy.Request) { rq.Adapter = ad })
			rq.Serve = r.serveFunc()
			done <- r.engine.Artifact(context.Background(), rq, w, httptest.NewRequest(http.MethodGet, "/"+path, nil))
		}()
		select {
		case <-entered:
		case <-time.After(20 * time.Second):
			t.Fatal("the hook never ran")
		}
		time.Sleep(400 * time.Millisecond)
		if w.reached() {
			t.Fatal("the client got a byte or a status line while the hook was still deciding")
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), tarball) {
			t.Fatalf("%d, %d bytes", w.Code, w.Body.Len())
		}
	})

	t.Run("R-NUGET-015 the control: an artifact without a hook streams to the leader while it is fetched", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream", Chunk: 40_000, ChunkDelay: 150 * time.Millisecond})
		rem := e.remote("hook-control", up)
		r := e.newReplica("a", replicaOpts{})
		w := newWatchedWriter()
		done := make(chan error, 1)
		go func() {
			rq := r.request(rem, path, proxy.Artifact)
			rq.Serve = r.serveFunc()
			done <- r.engine.Artifact(context.Background(), rq, w, httptest.NewRequest(http.MethodGet, "/"+path, nil))
		}()
		waitFor(t, "the first byte before the transfer ends", 5*time.Second, w.reached)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			t.Fatal("the request ended before the observer saw the first byte arrive early; the control proves nothing")
		default:
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-NUGET-015 with the object store down an inspected artifact is streamed to the leader once the hook accepted it and nothing is stored", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream"})
		rem := e.remote("hook-down", up)
		r := e.newReplica("a", replicaOpts{})
		r.obj.down.Store(true)
		ad := &proxytest.Adapter{Inspect: func(string, *items.Spooled) (any, error) { return nil, nil }}
		s := r.artifact(rem, path, func(rq *proxy.Request) { rq.Adapter = ad })
		if s.status != http.StatusOK || !bytes.Equal(s.body, tarball) {
			t.Fatalf("%d (%d bytes) %v", s.status, len(s.body), s.err)
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries"); n != 0 {
			t.Fatalf("%d cache entries", n)
		}
		// A refusal still wins over the pass-through: nothing reaches the client.
		refusing := &proxytest.Adapter{Inspect: func(string, *items.Spooled) (any, error) { return nil, errors.New("no") }}
		if s := r.artifact(rem, path, func(rq *proxy.Request) { rq.Adapter = refusing }); s.status != http.StatusBadGateway || len(s.body) > 2000 {
			t.Fatalf("a refused artifact with the object store down: %d, %d bytes", s.status, len(s.body))
		}
	})

	t.Run("R-NUGET-015 an accepted artifact is stored, its payload reaches the storage hook and the next request is a hit", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/"+path, proxytest.Route{Body: tarball, Type: "application/octet-stream"})
		rem := e.remote("hook-accept", up)
		hooks := &payloadHooks{}
		r := e.newReplica("a", replicaOpts{hooks: hooks})
		var seen []byte
		ad := &proxytest.Adapter{Inspect: func(_ string, sp *items.Spooled) (any, error) {
			seen = make([]byte, sp.Size)
			_, err := io.ReadFull(sp.Section(0, sp.Size), seen)
			return "inspection of hook-1.0.0", err
		}}
		s := r.artifact(rem, path, func(rq *proxy.Request) { rq.Adapter = ad })
		if s.status != http.StatusOK || !bytes.Equal(s.body, tarball) {
			t.Fatalf("%d, %d bytes (%v)", s.status, len(s.body), s.err)
		}
		if !bytes.Equal(seen, tarball) {
			t.Fatal("the hook did not receive the whole artifact")
		}
		if v, ok := hooks.payload(path); !ok || v != "inspection of hook-1.0.0" {
			t.Fatalf("the storage hook saw payload %v (%v)", v, ok)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = $1 AND trashed_at IS NULL", path); n != 1 {
			t.Fatalf("%d items", n)
		}
		if s := r.artifact(rem, path, func(rq *proxy.Request) { rq.Adapter = ad }); s.status != http.StatusOK || !strings.HasPrefix(s.cacheStatus(), "trove; hit") {
			t.Fatalf("the next request: %d %q", s.status, s.cacheStatus())
		}
		if ad.Inspects() != 1 || up.Count("GET", "/"+path) != 1 {
			t.Fatalf("hook %d, upstream %d, want 1 and 1", ad.Inspects(), up.Count("GET", "/"+path))
		}
	})
}

// TestDiscoveryDocument is WP3.0b: a format whose addressing follows a discovery document gets it resolved by the
// engine before any other path, and the URL map applies to metadata paths too.
func TestDiscoveryDocument(t *testing.T) {
	doc := []byte(`{"name":"d"}`)
	tarball := proxytest.Body("disc", 100_000)
	site := func(e *env) *proxytest.Upstream {
		up := e.upstream()
		up.Set("/disc/index", proxytest.Route{Body: []byte("base v2/\n"), Type: "text/plain"})
		up.Set("/v2/pkg/d", proxytest.Route{Body: doc, Type: "application/json"})
		up.Set("/v2/pkg/d/-/d-1.tgz", proxytest.Route{Body: tarball, Type: "application/octet-stream"})
		return up
	}
	disc := func() *proxytest.Adapter { return &proxytest.Adapter{Discovery: "disc/index"} }

	t.Run("R-NUGET-014 the engine resolves the document once, the adapter requests below what it parsed and the document is a cache entry", func(t *testing.T) {
		e := newEnv(t)
		up := site(e)
		rem := e.remote("disc", up)
		r := e.newReplica("a", replicaOpts{})
		ad := disc()
		if s := r.metadata(rem, "pkg/d", func(rq *proxy.Request) { rq.Adapter = ad }); s.status != 200 || !bytes.Equal(s.body, doc) {
			t.Fatalf("%d %q (%v)", s.status, s.body, s.err)
		}
		if up.Count("GET", "/disc/index") != 1 || up.Count("GET", "/v2/pkg/d") != 1 || up.Count("GET", "/pkg/d") != 0 {
			t.Fatalf("upstream saw %+v", up.Seen())
		}
		if s := r.artifact(rem, "pkg/d/-/d-1.tgz", func(rq *proxy.Request) { rq.Adapter = ad }); s.status != 200 || !bytes.Equal(s.body, tarball) {
			t.Fatalf("%d (%v)", s.status, s.err)
		}
		if up.Count("GET", "/disc/index") != 1 {
			t.Fatalf("the discovery document was fetched again: %d GETs", up.Count("GET", "/disc/index"))
		}
		if up.Count("GET", "/v2/pkg/d/-/d-1.tgz") != 1 {
			t.Fatalf("upstream saw %+v", up.Seen())
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE path = 'disc/index' AND kind = 'metadata'"); n != 1 {
			t.Fatalf("%d discovery entries", n)
		}
		// The document is an ordinary entry: a request for it is a hit and needs no discovery of its own.
		if s := r.metadata(rem, "disc/index", func(rq *proxy.Request) { rq.Adapter = ad }); s.status != 200 || !strings.HasPrefix(s.cacheStatus(), "trove; hit") {
			t.Fatalf("%d %q", s.status, s.cacheStatus())
		}
	})

	t.Run("R-NUGET-014 a repository limited to one fetch at a time does not wait for itself", func(t *testing.T) {
		e := newEnv(t)
		up := site(e)
		rem := e.remote("disc-one", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 1 })
		r := e.newReplica("a", replicaOpts{})
		done := make(chan served, 1)
		go func() { done <- r.artifact(rem, "pkg/d/-/d-1.tgz", func(rq *proxy.Request) { rq.Adapter = disc() }) }()
		select {
		case s := <-done:
			if s.status != 200 || !bytes.Equal(s.body, tarball) {
				t.Fatalf("%d (%v)", s.status, s.err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("the fetch waits for its own slot")
		}
	})

	t.Run("R-NUGET-014 a missing discovery document answers 502 and no other request is sent", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/v2/pkg/d", proxytest.Route{Body: doc, Type: "application/json"})
		rem := e.remote("disc-missing", up)
		r := e.newReplica("a", replicaOpts{})
		s := r.metadata(rem, "pkg/d", func(rq *proxy.Request) { rq.Adapter = disc() })
		if s.status != http.StatusBadGateway {
			t.Fatalf("status %d (%v)", s.status, s.err)
		}
		if up.Count("GET", "/v2/pkg/d") != 0 || up.Count("GET", "/pkg/d") != 0 {
			t.Fatalf("a document was requested without an address: %+v", up.Seen())
		}
	})

	t.Run("R-NUGET-014 a discovery document the format cannot parse answers 502", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		up.Set("/disc/index", proxytest.Route{Body: []byte("nothing useful\n"), Type: "text/plain"})
		up.Set("/v2/pkg/d", proxytest.Route{Body: doc, Type: "application/json"})
		rem := e.remote("disc-bad", up)
		r := e.newReplica("a", replicaOpts{})
		s := r.metadata(rem, "pkg/d", func(rq *proxy.Request) { rq.Adapter = disc() })
		if s.status != http.StatusBadGateway || up.Count("GET", "/v2/pkg/d") != 0 {
			t.Fatalf("status %d (%v), upstream %+v", s.status, s.err, up.Seen())
		}
	})

	t.Run("R-NUGET-014 a caller that may not fill the cache gets the refusal for a discovery document that is not stored", func(t *testing.T) {
		e := newEnv(t)
		up := site(e)
		rem := e.remote("disc-refused", up)
		r := e.newReplica("a", replicaOpts{})
		s := r.artifact(rem, "pkg/d/-/d-1.tgz", func(rq *proxy.Request) { rq.Adapter, rq.Populate = disc(), proxytest.Refused() })
		if s.status != http.StatusForbidden || up.Total() != 0 {
			t.Fatalf("status %d (%v), upstream saw %d requests", s.status, s.err, up.Total())
		}
	})

	t.Run("R-REPO-043 the URL map holds metadata paths too: a page the index names is fetched from the URL it came with", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		index := "map pkg/m/page2 " + up.URL + "/elsewhere/page2\nmap pkg/m/page3 https://other.invalid/p3\n"
		up.Set("/pkg/m", proxytest.Route{Body: []byte(index), Type: "application/json"})
		up.Set("/elsewhere/page2", proxytest.Route{Body: []byte(`{"page":2}`), Type: "application/json"})
		up.Set("/pkg/m/page2", proxytest.Route{Body: []byte(`{"page":"wrong"}`), Type: "application/json"})
		rem := e.remote("map-meta", up)
		r := e.newReplica("a", replicaOpts{})
		if s := r.metadata(rem, "pkg/m"); s.status != 200 {
			t.Fatalf("%d (%v)", s.status, s.err)
		}
		s := r.metadata(rem, "pkg/m/page2")
		if s.status != 200 || string(s.body) != `{"page":2}` {
			t.Fatalf("%d %q (%v)", s.status, s.body, s.err)
		}
		if up.Count("GET", "/elsewhere/page2") != 1 || up.Count("GET", "/pkg/m/page2") != 0 {
			t.Fatalf("upstream saw %+v", up.Seen())
		}
		// The foreign-host rule applies to a mapped metadata URL as it does to an artifact's.
		s = r.metadata(rem, "pkg/m/page3")
		if ee, ok := errs.As(s.err); !ok || ee.Code != errs.CodeForeignHostDenied {
			t.Fatalf("status %d, err %v, want foreign_host_denied", s.status, s.err)
		}
	})
}

// TestFetchBounds: a format's bound on a document does not bound its artifacts (R-REPO-040).
func TestFetchBounds(t *testing.T) {
	t.Run("R-REPO-040 Request.MaxBytes bounds a metadata document and not an artifact", func(t *testing.T) {
		e := newEnv(t)
		up := e.upstream()
		big := proxytest.Body("big", 50_000)
		up.Set("/pkg/b", proxytest.Route{Body: big, Type: "application/json"})
		up.Set("/pkg/b/-/b-1.tgz", proxytest.Route{Body: big, Type: "application/octet-stream"})
		rem := e.remote("bounds", up)
		r := e.newReplica("a", replicaOpts{})
		small := func(rq *proxy.Request) { rq.MaxBytes = 1000 }
		if s := r.metadata(rem, "pkg/b", small); s.status != http.StatusBadGateway {
			t.Fatalf("a document above the bound: %d (%v)", s.status, s.err)
		}
		if s := r.artifact(rem, "pkg/b/-/b-1.tgz", small); s.status != http.StatusOK || !bytes.Equal(s.body, big) {
			t.Fatalf("an artifact above the document bound: %d (%v)", s.status, s.err)
		}
	})
}
