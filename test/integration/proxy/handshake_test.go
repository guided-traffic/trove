//go:build integration

package proxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// The upstream's token handshake and its credential rule (R-REPO-033, R-REPO-033), and the revalidation by the format's own
// request (R-REPO-027), against the generic test upstream: a registry that challenges, a token service reached as another host
// name, and a blob host. Nothing here knows a registry protocol.

// robotBasic is the credential of the repositories of these tests: user robot, the password of bearerSource.
var robotBasic = "Basic " + base64.StdEncoding.EncodeToString([]byte("robot:s3cret"))

// hsReplicaOpts gives a replica the credential of the repositories: user robot, the password of bearerSource.
func hsReplicaOpts(ups ...*proxytest.Upstream) replicaOpts {
	return replicaOpts{
		ups: ups, secrets: bearerSource{},
		env: func(k string) (string, bool) { return "s3cret", k == "TROVE_CAC_IT_QUERY_TOKEN" },
	}
}

// basic is the credential of a repository: authMode basic with the stored password.
func basic(st *repository.RemoteSettings) {
	st.AuthMode, st.Username, st.Password = repository.AuthBasic, "robot", &repository.SecretField{Set: true}
}

func authOfAll(u *proxytest.Upstream) []string {
	var out []string
	for _, s := range u.Seen() {
		out = append(out, s.Authorization)
	}
	return out
}

// handshakeAdapter is an adapter with the generic handshake; every path shares one scope unless perPath.
func handshakeAdapter(perPath bool) *proxytest.Adapter {
	a := &proxytest.Adapter{Auth: true}
	if !perPath {
		a.ScopeOf = func(*http.Request, string) string { return "scope" }
	}
	return a
}

func withAdapter(a proxy.Adapter) func(*proxy.Request) {
	return func(rq *proxy.Request) { rq.Adapter = a }
}

func TestHandshake(t *testing.T) {
	e := newEnv(t)
	registry, realm, cdn := e.upstream(), e.upstream(), e.upstream()
	realmBase, cdnBase := realm.As("auth.example.com"), cdn.As("cdn.example.com")
	// use makes t the token service of the realm and the set of tokens the registry accepts.
	var current *proxytest.Tokens
	use := func(t *proxytest.Tokens) {
		current = t
		realm.TokenService("/token", t)
	}
	use(proxytest.NewTokens(300))
	challenge := func(path string, r proxytest.Route) {
		registry.Challenge(path, realmBase+"/token", func(tok string) bool { return current.Accept(tok) }, r)
	}
	doc := proxytest.Route{Body: []byte(`{"name":"x"}`), Type: "application/json"}
	r := e.newReplica("a", hsReplicaOpts())
	reset := func() { registry.Reset(); realm.Reset(); cdn.Reset() }
	extra := func(hosts ...repository.ExtraHost) func(*repository.RemoteSettings) {
		return func(st *repository.RemoteSettings) { st.ExtraHosts = hosts }
	}

	t.Run("R-REPO-033 the realm on a credential host gets the repository's credential, the registry the token after its 401", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		challenge("/pkg/cred", doc)
		rem := e.remote("hs-cred", registry, basic, extra(repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}))
		s := r.metadata(rem, "pkg/cred", withAdapter(handshakeAdapter(false)))
		if s.status != 200 || string(s.body) != string(doc.Body) {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if got := authOfAll(realm); len(got) != 1 || got[0] != robotBasic {
			t.Errorf("the token service saw %q, want the repository's credential", got)
		}
		if got := realm.Seen()[0].Host; !strings.HasPrefix(got, "auth.example.com:") {
			t.Errorf("the token service was reached as %q", got)
		}
		got := authOfAll(registry)
		if len(got) != 2 || got[0] != robotBasic || got[1] != "Bearer tok-1" {
			t.Errorf("the registry saw %q, want the credential once and then the token", got)
		}
	})

	t.Run("R-REPO-033 the realm on an extraHosts entry without sendCredentials is asked anonymously", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		challenge("/pkg/anon", doc)
		rem := e.remote("hs-anon", registry, basic, extra(repository.ExtraHost{Pattern: "auth.example.com"}))
		if s := r.metadata(rem, "pkg/anon", withAdapter(handshakeAdapter(false))); s.status != 200 {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if got := authOfAll(realm); len(got) != 1 || got[0] != "" {
			t.Errorf("the token service saw %q: the credential went to a host that may not have it", got)
		}
	})

	t.Run("R-REPO-033 a realm on a host the repository does not name is refused and nothing is sent to it", func(t *testing.T) {
		reset()
		challenge("/pkg/foreign", doc)
		rem := e.remote("hs-foreign", registry, basic)
		s := r.metadata(rem, "pkg/foreign", withAdapter(handshakeAdapter(false)))
		if s.status != http.StatusBadGateway || codeOf(s.err) != errs.CodeForeignHostDenied {
			t.Fatalf("%d %v", s.status, s.err)
		}
		if realm.Total() != 0 {
			t.Fatalf("the foreign realm got %d requests, with %q", realm.Total(), authOfAll(realm))
		}
		if registry.Total() != 1 {
			t.Errorf("the registry got %d requests, want the one that was challenged", registry.Total())
		}
	})

	t.Run("R-REPO-033 a repository without a credential sends none to any host", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		challenge("/pkg/none", doc)
		rem := e.remote("hs-none", registry, extra(repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}))
		if s := r.metadata(rem, "pkg/none", withAdapter(handshakeAdapter(false))); s.status != 200 {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if got := authOfAll(realm); len(got) != 1 || got[0] != "" {
			t.Errorf("the token service saw %q", got)
		}
		if got := authOfAll(registry); len(got) != 2 || got[0] != "" || got[1] != "Bearer tok-1" {
			t.Errorf("the registry saw %q", got)
		}
	})

	t.Run("R-REPO-033 the token goes to the registry only, not to the host it redirects to", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		cdn.Set("/blob", proxytest.Route{Body: []byte(`{"blob":true}`), Type: "application/json"})
		challenge("/pkg/redir", proxytest.Route{Status: http.StatusFound, Header: map[string]string{"Location": cdnBase + "/blob"}})
		rem := e.remote("hs-redir", registry, basic, extra(
			repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}, repository.ExtraHost{Pattern: "cdn.example.com"}))
		s := r.metadata(rem, "pkg/redir", withAdapter(handshakeAdapter(false)))
		if s.status != 200 || string(s.body) != `{"blob":true}` {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if got := authOfAll(cdn); len(got) != 1 || got[0] != "" {
			t.Errorf("the blob host saw %q: the token or the credential followed the redirect", got)
		}
	})

	t.Run("R-REPO-033 a token serves another request of its scope on the replica; another replica asks for its own", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		a := handshakeAdapter(false)
		challenge("/pkg/one", doc)
		challenge("/pkg/two", doc)
		rem := e.remote("hs-cache", registry, basic, extra(repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}))
		for _, p := range []string{"pkg/one", "pkg/two"} {
			if s := r.metadata(rem, p, withAdapter(a)); s.status != 200 {
				t.Fatalf("%s: %d %s", p, s.status, s.body)
			}
		}
		if n := realm.Total(); n != 1 {
			t.Fatalf("two requests of one scope made %d token requests", n)
		}
		r2 := e.newReplica("b", hsReplicaOpts())
		challenge("/pkg/three", doc)
		if s := r2.metadata(rem, "pkg/three", withAdapter(a)); s.status != 200 {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if n := realm.Total(); n != 2 {
			t.Errorf("another replica made %d token requests in all, want 2", n)
		}
	})

	t.Run("R-REPO-033 a new configuration version asks for a new token", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		a := handshakeAdapter(false)
		challenge("/pkg/v1", doc)
		challenge("/pkg/v2", doc)
		rem := e.remote("hs-version", registry, basic, extra(repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}))
		if s := r.metadata(rem, "pkg/v1", withAdapter(a)); s.status != 200 {
			t.Fatalf("%d %s", s.status, s.body)
		}
		e.exec("UPDATE repositories SET config_version = config_version + 1 WHERE key = 'hs-version'")
		rem = e.lookup("hs-version")
		if s := r.metadata(rem, "pkg/v2", withAdapter(a)); s.status != 200 {
			t.Fatalf("%d %s", s.status, s.body)
		}
		if n := realm.Total(); n != 2 {
			t.Errorf("%d token requests, want one per configuration version", n)
		}
	})

	t.Run("R-REPO-033 concurrent requests of one scope share one token request", func(t *testing.T) {
		reset()
		toks := proxytest.NewTokens(300)
		toks.Delay = 300 * time.Millisecond
		use(toks)
		a := handshakeAdapter(false)
		rem := e.remote("hs-conc", registry, basic, extra(repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}))
		var wg sync.WaitGroup
		for i := range 8 {
			p := fmt.Sprintf("pkg/c%d", i)
			challenge("/"+p, doc)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if s := r.metadata(rem, p, withAdapter(a)); s.status != 200 {
					t.Errorf("%s: %d %s", p, s.status, s.body)
				}
			}()
		}
		wg.Wait()
		if n := realm.Total(); n != 1 {
			t.Errorf("8 concurrent requests made %d token requests", n)
		}
	})

	t.Run("R-REPO-033 a second 401 stands as the upstream's refusal", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		registry.Challenge("/pkg/refused", realmBase+"/token", func(string) bool { return false }, doc)
		rem := e.remote("hs-refused", registry, basic, extra(repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}))
		s := r.metadata(rem, "pkg/refused", withAdapter(handshakeAdapter(false)))
		if s.status != http.StatusBadGateway || codeOf(s.err) != errs.CodeUpstreamAuthFailed {
			t.Fatalf("%d %v", s.status, s.err)
		}
		if realm.Total() != 1 || registry.Total() != 2 {
			t.Errorf("token requests %d, registry requests %d; want 1 and 2", realm.Total(), registry.Total())
		}
	})

	t.Run("R-REPO-033 an artifact is fetched through the handshake", func(t *testing.T) {
		reset()
		use(proxytest.NewTokens(300))
		body := proxytest.Body("blob", 4096)
		challenge("/pkg/a/-/blob.bin", proxytest.Route{Body: body, Type: "application/octet-stream"})
		rem := e.remote("hs-artifact", registry, basic, extra(repository.ExtraHost{Pattern: "auth.example.com", SendCredentials: true}))
		s := r.artifact(rem, "pkg/a/-/blob.bin", withAdapter(handshakeAdapter(false)))
		if s.status != 200 || string(s.body) != string(body) {
			t.Fatalf("%d %d bytes %v", s.status, len(s.body), s.err)
		}
		if s := r.artifact(rem, "pkg/a/-/blob.bin", withAdapter(handshakeAdapter(false))); s.status != 200 || registry.Total() != 2 {
			t.Errorf("the stored artifact cost the registry requests: %d in all", registry.Total())
		}
	})
}

// TestR_REPO_027_HeadRevalidation: an expired stored document is revalidated with the request of the format's adapter. An answer
// the adapter calls unchanged renews the document's freshness and fetches no body; any other answer fetches the document.
func TestR_REPO_027_HeadRevalidation(t *testing.T) {
	e := newEnv(t)
	registry := e.upstream()
	r := e.newReplica("a", replicaOpts{})
	digestOf := func(b []byte) string { return fmt.Sprintf("%x", proxytest.SHA256(b)) }
	serve := func(path string, body []byte) {
		registry.Set(path, proxytest.Route{Body: body, Type: "application/json", Header: map[string]string{proxytest.DigestHeader: digestOf(body)}})
	}
	short := func(st *repository.RemoteSettings) { st.Cache.MetadataTTLSeconds = 1 }

	t.Run("an unchanged document costs one HEAD and no GET, and is fresh again", func(t *testing.T) {
		a := &proxytest.Adapter{HeadRevalidation: true}
		serve("/pkg/same", []byte(`{"v":1}`))
		rem := e.remote("rv-same", registry, short)
		if s := r.metadata(rem, "pkg/same", withAdapter(a)); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		time.Sleep(1200 * time.Millisecond)
		s := r.metadata(rem, "pkg/same", withAdapter(a))
		if s.status != 200 || string(s.body) != `{"v":1}` || s.cacheStatus() != "trove; fwd=stale; fwd-status=200" {
			t.Fatalf("%d %q %q", s.status, s.body, s.cacheStatus())
		}
		if registry.Count("HEAD", "/pkg/same") != 1 || registry.Count("GET", "/pkg/same") != 1 || a.Revalidations() != 1 {
			t.Fatalf("HEAD %d, GET %d, revalidation requests %d", registry.Count("HEAD", "/pkg/same"), registry.Count("GET", "/pkg/same"), a.Revalidations())
		}
		if s := r.metadata(rem, "pkg/same", withAdapter(a)); !strings.HasPrefix(s.cacheStatus(), "trove; hit; ttl=") {
			t.Fatalf("the document was not fresh after its revalidation: %q", s.cacheStatus())
		}
		if got := e.number("SELECT (expires_at > now())::int FROM remote_cache_entries WHERE path = 'pkg/same'"); got != 1 {
			t.Fatalf("the entry's expiry was not renewed")
		}
	})

	t.Run("a changed document costs a HEAD and a GET and replaces the stored one", func(t *testing.T) {
		a := &proxytest.Adapter{HeadRevalidation: true}
		serve("/pkg/moved", []byte(`{"v":1}`))
		rem := e.remote("rv-moved", registry, short)
		_ = r.metadata(rem, "pkg/moved", withAdapter(a))
		serve("/pkg/moved", []byte(`{"v":2}`))
		time.Sleep(1200 * time.Millisecond)
		s := r.metadata(rem, "pkg/moved", withAdapter(a))
		if s.status != 200 || string(s.body) != `{"v":2}` {
			t.Fatalf("%d %q", s.status, s.body)
		}
		if registry.Count("HEAD", "/pkg/moved") != 1 || registry.Count("GET", "/pkg/moved") != 2 {
			t.Fatalf("HEAD %d, GET %d", registry.Count("HEAD", "/pkg/moved"), registry.Count("GET", "/pkg/moved"))
		}
	})

	t.Run("an answer without the digest header fetches the document", func(t *testing.T) {
		a := &proxytest.Adapter{HeadRevalidation: true}
		registry.Set("/pkg/nohdr", proxytest.Route{Body: []byte(`{"v":1}`), Type: "application/json"})
		rem := e.remote("rv-nohdr", registry, short)
		_ = r.metadata(rem, "pkg/nohdr", withAdapter(a))
		time.Sleep(1200 * time.Millisecond)
		if s := r.metadata(rem, "pkg/nohdr", withAdapter(a)); s.status != 200 {
			t.Fatalf("%d", s.status)
		}
		if registry.Count("HEAD", "/pkg/nohdr") != 1 || registry.Count("GET", "/pkg/nohdr") != 2 {
			t.Fatalf("HEAD %d, GET %d", registry.Count("HEAD", "/pkg/nohdr"), registry.Count("GET", "/pkg/nohdr"))
		}
	})

	t.Run("a HEAD answered 404 is the upstream's gone: the document is marked and not served", func(t *testing.T) {
		a := &proxytest.Adapter{HeadRevalidation: true}
		serve("/pkg/gone", []byte(`{"v":1}`))
		rem := e.remote("rv-gone", registry, short)
		_ = r.metadata(rem, "pkg/gone", withAdapter(a))
		registry.Remove("/pkg/gone")
		time.Sleep(1200 * time.Millisecond)
		if s := r.metadata(rem, "pkg/gone", withAdapter(a)); s.status != http.StatusNotFound {
			t.Fatalf("%d %q", s.status, s.body)
		}
		if registry.Count("GET", "/pkg/gone") != 1 {
			t.Fatalf("GET %d: the document was fetched after the HEAD said it is gone", registry.Count("GET", "/pkg/gone"))
		}
	})

	t.Run("a HEAD answered 503 serves the stored copy as a failed conditional GET does", func(t *testing.T) {
		a := &proxytest.Adapter{HeadRevalidation: true}
		serve("/pkg/down", []byte(`{"v":1}`))
		rem := e.remote("rv-down", registry, short)
		_ = r.metadata(rem, "pkg/down", withAdapter(a))
		registry.Set("/pkg/down", proxytest.Route{Status: http.StatusServiceUnavailable})
		time.Sleep(1200 * time.Millisecond)
		s := r.metadata(rem, "pkg/down", withAdapter(a))
		if s.status != 200 || string(s.body) != `{"v":1}` || !strings.Contains(s.cacheStatus(), "stale") {
			t.Fatalf("%d %q %q", s.status, s.body, s.cacheStatus())
		}
	})

	t.Run("without a revalidation request the conditional GET stays", func(t *testing.T) {
		a := &proxytest.Adapter{}
		registry.Set("/pkg/plain", proxytest.Route{Body: []byte(`{"v":1}`), Type: "application/json", ETag: `"p1"`})
		rem := e.remote("rv-plain", registry, short)
		_ = r.metadata(rem, "pkg/plain", withAdapter(a))
		time.Sleep(1200 * time.Millisecond)
		if s := r.metadata(rem, "pkg/plain", withAdapter(a)); s.cacheStatus() != "trove; fwd=stale; fwd-status=304" {
			t.Fatalf("%q", s.cacheStatus())
		}
		if registry.Count("HEAD", "/pkg/plain") != 0 {
			t.Fatal("a HEAD was sent")
		}
	})
}

// TestR_REPO_027_HeadRevalidationRunsTheHandshake: the HEAD of a revalidation is sent with the token of the remembered challenge,
// and a token that is refused runs the handshake once more.
func TestR_REPO_027_HeadRevalidationRunsTheHandshake(t *testing.T) {
	e := newEnv(t)
	registry, realm := e.upstream(), e.upstream()
	realmBase := realm.As("auth.example.com")
	tokens := proxytest.NewTokens(1) // the lifetime ends before the revalidation
	realm.TokenService("/token", tokens)
	body := []byte(`{"v":1}`)
	registry.Challenge("/pkg/hs", realmBase+"/token", tokens.Accept, proxytest.Route{
		Body: body, Type: "application/json", Header: map[string]string{proxytest.DigestHeader: fmt.Sprintf("%x", proxytest.SHA256(body))},
	})
	r := e.newReplica("a", replicaOpts{})
	rem := e.remote("rv-hs", registry, func(st *repository.RemoteSettings) {
		st.Cache.MetadataTTLSeconds = 1
		st.ExtraHosts = []repository.ExtraHost{{Pattern: "auth.example.com"}}
	})
	a := &proxytest.Adapter{Auth: true, HeadRevalidation: true, ScopeOf: func(*http.Request, string) string { return "scope" }}
	if s := r.metadata(rem, "pkg/hs", withAdapter(a)); s.status != 200 {
		t.Fatalf("%d %s", s.status, s.body)
	}
	time.Sleep(1200 * time.Millisecond)
	s := r.metadata(rem, "pkg/hs", withAdapter(a))
	if s.status != 200 || s.cacheStatus() != "trove; fwd=stale; fwd-status=200" {
		t.Fatalf("%d %q %v", s.status, s.cacheStatus(), s.err)
	}
	// The first request was challenged (GET, token, GET); the revalidation carries the token the remembered challenge names, fetched
	// first because the first one had run out (Q-OCI-82), so its HEAD is not challenged.
	if registry.Count("HEAD", "/pkg/hs") != 1 || registry.Count("GET", "/pkg/hs") != 2 {
		t.Fatalf("HEAD %d, GET %d; want the GET challenged once and the HEAD not at all", registry.Count("HEAD", "/pkg/hs"), registry.Count("GET", "/pkg/hs"))
	}
	if tokens.Issued() != 2 {
		t.Errorf("%d tokens issued, want one per expired lifetime", tokens.Issued())
	}
}

// TestR_REPO_027_ARevalidationThatSaysMovedFetchesWithAnUnconditionalGet: after the format said the document moved, the GET carries
// no validator of the old copy, so a 304 cannot renew it; and the format's request derives from the engine's GET.
func TestR_REPO_027_ARevalidationThatSaysMovedFetchesWithAnUnconditionalGet(t *testing.T) {
	e := newEnv(t)
	registry := e.upstream()
	r := e.newReplica("a", replicaOpts{})
	a := &proxytest.Adapter{HeadRevalidation: true}
	v1, v2 := []byte(`{"v":1}`), []byte(`{"v":2}`)
	hdr := func(b []byte) map[string]string {
		return map[string]string{proxytest.DigestHeader: fmt.Sprintf("%x", proxytest.SHA256(b))}
	}
	// The upstream keeps the ETag when the document moves (a broken upstream): a conditional GET would answer 304.
	registry.Set("/pkg/stale-etag", proxytest.Route{Body: v1, Type: "application/json", ETag: `"same"`, Header: hdr(v1)})
	rem := e.remote("rv-etag", registry, func(st *repository.RemoteSettings) { st.Cache.MetadataTTLSeconds = 1 })
	_ = r.metadata(rem, "pkg/stale-etag", withAdapter(a))
	registry.Set("/pkg/stale-etag", proxytest.Route{Body: v2, Type: "application/json", ETag: `"same"`, Header: hdr(v2)})
	time.Sleep(1200 * time.Millisecond)
	s := r.metadata(rem, "pkg/stale-etag", withAdapter(a))
	if s.status != 200 || string(s.body) != string(v2) {
		t.Fatalf("%d %q: the moved document was not fetched", s.status, s.body)
	}
	if registry.Conditional("/pkg/stale-etag") != 0 {
		t.Fatalf("a conditional request was sent after the revalidation said the document moved")
	}
	if got := a.RevalidationGets(); len(got) != 1 || !strings.Contains(got[0], "/pkg/stale-etag") {
		t.Fatalf("the format was handed %q", got)
	}
}

// countingRunner counts the transactions the engine opens.
type countingRunner struct {
	meta.Runner
	n *atomic.Int64
}

func (c countingRunner) InTx(ctx context.Context, fn func(meta.Tx) error) error {
	c.n.Add(1)
	return c.Runner.InTx(ctx, fn)
}

// TestR_REPO_027_ARevalidationBuildsTheUpstreamRequestOnce: the engine builds the format's GET (and reads the URL map for it) once
// per fetch, whether or not the format has a revalidation request, so a format without one (npm, NuGet) pays nothing for the seam
// (R-ARCH-027).
func TestR_REPO_027_ARevalidationBuildsTheUpstreamRequestOnce(t *testing.T) {
	e := newEnv(t)
	registry := e.upstream()
	var txs atomic.Int64
	r := e.newReplica("a", replicaOpts{runner: func(rn meta.Runner) meta.Runner { return countingRunner{Runner: rn, n: &txs} }})
	short := func(st *repository.RemoteSettings) { st.Cache.MetadataTTLSeconds = 1 }

	// The format has a revalidation request (ok false): the conditional GET stays.
	plain := &proxytest.Adapter{}
	registry.Set("/pkg/once", proxytest.Route{Body: []byte(`{"v":1}`), Type: "application/json", ETag: `"o1"`})
	rem := e.remote("rv-once", registry, short)
	_ = r.metadata(rem, "pkg/once", withAdapter(plain))
	time.Sleep(1200 * time.Millisecond)
	beforeReq, beforeTx := plain.Requests(), txs.Load()
	if s := r.metadata(rem, "pkg/once", withAdapter(plain)); s.cacheStatus() != "trove; fwd=stale; fwd-status=304" {
		t.Fatalf("%q", s.cacheStatus())
	}
	if got := plain.Requests() - beforeReq; got != 1 {
		t.Errorf("the format built %d upstream requests for one revalidation, want 1", got)
	}
	plainTx := txs.Load() - beforeTx

	// The format has one: the HEAD derives from the same GET.
	head := &proxytest.Adapter{HeadRevalidation: true}
	body := []byte(`{"v":1}`)
	registry.Set("/pkg/once-head", proxytest.Route{Body: body, Type: "application/json", Header: map[string]string{proxytest.DigestHeader: fmt.Sprintf("%x", proxytest.SHA256(body))}})
	rem2 := e.remote("rv-once-head", registry, short)
	_ = r.metadata(rem2, "pkg/once-head", withAdapter(head))
	time.Sleep(1200 * time.Millisecond)
	beforeReq, beforeTx = head.Requests(), txs.Load()
	_ = r.metadata(rem2, "pkg/once-head", withAdapter(head))
	if got := head.Requests() - beforeReq; got != 1 {
		t.Errorf("the format built %d upstream requests for one HEAD revalidation, want 1", got)
	}
	if headTx := txs.Load() - beforeTx; headTx > plainTx {
		t.Errorf("a HEAD revalidation opened %d transactions, a conditional GET %d", headTx, plainTx)
	}
}
