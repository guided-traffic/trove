package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The tests of the handshake drive Engine.Get, the engine's request of a mirror's pass, against a registry that challenges and a
// token service on a second listener: the handshake is the engine's own code on that path, and a repository without a circuit
// needs no PostgreSQL. The paths through PostgreSQL (a fetched document, the freshness rows) are in test/integration/proxy.

type hsClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *hsClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *hsClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// hsAuth is a format's handshake: the realm and the service are read from the challenge, the scope from the request, and the
// answer is {"token": …}.
type hsAuth struct {
	// key is the cache key of every token; "" names the request's path (the scope).
	key string
	// none answers ok false: the format knows no handshake for the request.
	none bool
	ttl  time.Duration
	// raw replaces the Authorization value ReadToken returns.
	raw    string
	useRaw bool
	// realm replaces the realm the challenge names; userinfo puts a user into it.
	realm    string
	userinfo bool
	// panics makes ReadToken panic.
	panics bool
	asks   atomic.Int64
}

func (a *hsAuth) TokenRequest(ctx context.Context, _ UpstreamBase, orig *http.Request, _ string, _ int, challenge http.Header) (*http.Request, string, bool, error) {
	a.asks.Add(1)
	if a.none {
		return nil, "", false, nil
	}
	_, rest, _ := strings.Cut(challenge.Get("Www-Authenticate"), `realm="`)
	realm, _, _ := strings.Cut(rest, `"`)
	if a.realm != "" {
		realm = a.realm
	}
	key := a.key
	if key == "" {
		key = orig.URL.Path
	}
	if a.userinfo {
		realm = strings.Replace(realm, "://", "://robot:s3cret@", 1)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?scope="+url.QueryEscape(key), nil)
	return req, key, true, err
}

func (a *hsAuth) ReadToken(_ context.Context, _ int, body []byte) (string, time.Duration, error) {
	if a.panics {
		panic("the format failed")
	}
	if a.useRaw {
		return a.raw, a.ttl, nil
	}
	var doc struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", 0, err
	}
	return "Bearer " + doc.Token, a.ttl, nil
}

// hsRecorder is the audit recorder of a test.
type hsRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *hsRecorder) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }

func (r *hsRecorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *hsRecorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.Type+" "+string(e.Details))
	}
	return out
}

// hsWorld is a registry, its token service and the engine under test.
type hsWorld struct {
	t        *testing.T
	e        *Engine
	reg      *cache.Registry
	clk      *hsClock
	registry *httptest.Server
	realm    *httptest.Server
	realmB   *httptest.Server // a second token service the registry can move to
	other    *httptest.Server // the target of a redirect of the token service
	rem      Remote
	audit    *hsRecorder
	metrics  *prometheus.Registry

	// the token service
	issued, realmHits atomic.Int64
	realmDelay        time.Duration
	realmPad          int
	realmStatus       int
	realmRedirect     bool
	realmHeader       map[string]string
	realmAuth         []string // the Authorization header of every request the token service got
	realmBHits        atomic.Int64
	realmAStale       atomic.Bool // the registry takes no token of the first token service any more
	challengeRealm    string      // the realm the registry names; "" is the first token service
	otherChallenges   bool        // the redirect target answers 401 with a challenge
	otherOnce         bool        // ... for its first request only
	otherHits         atomic.Int64
	otherAuth         []string

	mu     sync.Mutex
	valid  map[string]bool
	seen   []string // the Authorization header of every request the registry got
	denyAl bool     // the registry refuses every token
	// scopeErr adds error="insufficient_scope" to the challenge of a refused token.
	scopeErr bool
}

type staticClients struct{ cl *upstream.Client }

func (s staticClients) For(context.Context, repository.Repository, repository.RemoteSettings) (*upstream.Client, error) {
	return s.cl, nil
}

type (
	noRunner struct{ meta.Runner }
	noItems  struct{ ItemStore }
)

func hsProxyConfig(t *testing.T) config.Proxy {
	t.Helper()
	l, err := config.Load(config.Options{
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Environ: []string{
			"TROVE_DATABASE_HOST=db.invalid", "TROVE_DATABASE_NAME=trove", "TROVE_STORAGE_S3_BUCKET=blobs",
			"TROVE_STORAGE_SPOOL_DIR=" + t.TempDir(),
		},
		Command: config.CommandMigrate,
	})
	if err != nil {
		t.Fatalf("proxy defaults: %v", err)
	}
	return l.Config.Proxy
}

// newHSWorld starts the servers and the engine. mutate changes the repository's settings before the client is built.
func newHSWorld(t *testing.T, mutate ...func(*repository.RemoteSettings)) *hsWorld {
	t.Helper()
	return newHSWorldDeny(t, nil, mutate...)
}

// newHSWorldDeny is newHSWorld with deny CIDRs in the egress policy.
func newHSWorldDeny(t *testing.T, deny []string, mutate ...func(*repository.RemoteSettings)) *hsWorld {
	t.Helper()
	w := &hsWorld{
		t: t, valid: map[string]bool{}, clk: &hsClock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)},
		audit: &hsRecorder{}, metrics: prometheus.NewRegistry(),
	}
	w.other = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.otherAuth = append(w.otherAuth, r.Header.Get("Authorization"))
		w.mu.Unlock()
		if w.otherChallenges || (w.otherOnce && w.otherHits.Add(1) == 1) {
			rw.Header().Set("WWW-Authenticate", `Bearer realm="`+w.realm.URL+`/token",service="test"`)
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = rw.Write([]byte(`{"token":"from-the-redirect"}`))
	}))
	t.Cleanup(w.other.Close)
	w.realm = httptest.NewServer(http.HandlerFunc(w.serveRealm))
	t.Cleanup(w.realm.Close)
	w.realmB = httptest.NewServer(http.HandlerFunc(w.serveRealmB))
	t.Cleanup(w.realmB.Close)
	w.registry = httptest.NewServer(http.HandlerFunc(w.serveRegistry))
	t.Cleanup(w.registry.Close)

	s := repository.RemoteSettings{
		URL: w.registry.URL + "/", ExtraHosts: []repository.ExtraHost{{Pattern: "127.0.0.1"}}, AuthMode: repository.AuthNone,
		Headers: []repository.Header{}, OutboundProxy: repository.OutboundProxy{Mode: repository.ProxyDirect},
		ConnectTimeoutMs: 10000, ReadTimeoutMs: 15000, CacheFillAction: repository.FillRead,
		Availability: repository.Availability{Mode: repository.AvailabilityOnline},
		Limits:       repository.RemoteLimits{MaxConcurrentFetches: 64},
	}
	for _, m := range mutate {
		m(&s)
	}
	pcfg := hsProxyConfig(t)
	cfg := config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}
	cfg.AddressPolicy.AllowCIDRs = []string{"127.0.0.0/8", "::1/128"}
	cfg.AddressPolicy.DenyCIDRs = deny
	var err error
	if w.reg, err = cache.NewRegistry(nil, nil); err != nil {
		t.Fatal(err)
	}
	clients, err := upstream.New(upstream.Options{Factory: outbound.New(cfg), Caches: w.reg, Proxy: pcfg, SafetyTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var pw *observability.Secret
	if s.AuthMode != repository.AuthNone {
		v := observability.Secret("s3cret")
		pw = &v
	}
	cl, err := clients.Ephemeral("registry", s, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	w.e, err = newEngine(Options{
		Runner: noRunner{}, Items: noItems{}, Caches: w.reg, Clients: staticClients{cl}, Config: pcfg, Now: w.clk.now,
		Metrics: w.metrics, Audit: w.audit,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.e.cancel)
	w.rem = Remote{
		Repo:     repository.Repository{ID: "7", Name: qname.Repo{Project: "it", Key: "registry"}, Class: repository.Remote, Version: 3},
		Settings: s, id: 7,
	}
	return w
}

func (w *hsWorld) serveRealm(rw http.ResponseWriter, r *http.Request) {
	w.realmHits.Add(1)
	w.mu.Lock()
	w.realmAuth = append(w.realmAuth, r.Header.Get("Authorization"))
	w.mu.Unlock()
	if w.realmDelay > 0 {
		time.Sleep(w.realmDelay)
	}
	if w.realmRedirect {
		http.Redirect(rw, r, w.other.URL+"/token", http.StatusFound)
		return
	}
	for k, v := range w.realmHeader {
		rw.Header().Set(k, v)
	}
	if w.realmStatus != 0 {
		http.Error(rw, "refused", w.realmStatus)
		return
	}
	n := w.issued.Add(1)
	tok := "tok-" + string(rune('0'+n%10)) + "-" + r.URL.Query().Get("scope")
	w.mu.Lock()
	w.valid[tok] = !w.realmAStale.Load()
	w.mu.Unlock()
	doc := map[string]any{"token": tok}
	if w.realmPad > 0 {
		doc["padding"] = strings.Repeat("x", w.realmPad)
	}
	_ = json.NewEncoder(rw).Encode(doc)
}

// serveRealmB is the second token service: its tokens are always taken.
func (w *hsWorld) serveRealmB(rw http.ResponseWriter, r *http.Request) {
	n := w.realmBHits.Add(1)
	tok := "tokB-" + string(rune('0'+n%10)) + "-" + r.URL.Query().Get("scope")
	w.mu.Lock()
	w.valid[tok] = true
	w.mu.Unlock()
	_ = json.NewEncoder(rw).Encode(map[string]any{"token": tok})
}

// challenge is the realm the registry names in its challenge.
func (w *hsWorld) challenge() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.challengeRealm != "" {
		return w.challengeRealm
	}
	return w.realm.URL + "/token"
}

func (w *hsWorld) serveRegistry(rw http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	w.mu.Lock()
	w.seen = append(w.seen, auth)
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	good := ok && w.valid[tok] && !w.denyAl
	w.mu.Unlock()
	if r.URL.Path == "/redir-auth" && good {
		http.Redirect(rw, r, w.other.URL+"/blob", http.StatusFound)
		return
	}
	if r.URL.Path == "/redir" {
		http.Redirect(rw, r, w.other.URL+"/blob", http.StatusFound)
		return
	}
	if !good {
		c := `Bearer realm="` + w.challenge() + `",service="test"`
		w.mu.Lock()
		if w.scopeErr && ok {
			c += `,error="insufficient_scope"`
		}
		w.mu.Unlock()
		rw.Header().Set("WWW-Authenticate", c)
		http.Error(rw, "unauthorized", http.StatusUnauthorized)
		return
	}
	_, _ = rw.Write([]byte("ok " + r.URL.Path))
}

func (w *hsWorld) get(path string, a Authenticator) (*GetResult, error) {
	w.t.Helper()
	u, _ := url.Parse(w.registry.URL + path)
	return w.e.Get(context.Background(), Get{Remote: w.rem, URL: u, Auth: a})
}

func (w *hsWorld) registrySeen() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.seen...)
}

// anonymous is the number of requests the registry got without an Authorization header.
func (w *hsWorld) anonymous() int {
	n := 0
	for _, a := range w.registrySeen() {
		if a == "" {
			n++
		}
	}
	return n
}

func mustGet(t *testing.T, w *hsWorld, path string, a Authenticator) {
	t.Helper()
	res, err := w.get(path, a)
	if err != nil || res.Status != http.StatusOK {
		t.Fatalf("GET %s: %v, %+v", path, err, res)
	}
}

func codeOfErr(err error) string {
	if ee, ok := errs.As(err); ok {
		return ee.Code
	}
	return ""
}

// TestR_REPO_033_HandshakeAnswersTheChallengeAndRetriesOnce: a 401 with a challenge makes the engine ask the format for the token
// request, send it to the realm, and retry the original request once with the token.
func TestR_REPO_033_HandshakeAnswersTheChallengeAndRetriesOnce(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{key: "scope"}
	res, err := w.get("/v2/x", a)
	if err != nil || res.Status != http.StatusOK || string(res.Body) != "ok /v2/x" {
		t.Fatalf("GET: %v, %+v", err, res)
	}
	if seen := w.registrySeen(); len(seen) != 2 || seen[0] != "" || seen[1] != "Bearer tok-1-scope" {
		t.Errorf("the registry saw %q, want an anonymous request and the retry with the token", seen)
	}
	if w.realmHits.Load() != 1 {
		t.Errorf("the realm got %d requests, want 1", w.realmHits.Load())
	}
}

// TestR_REPO_033_TokenIsCachedPerKeyAndReused: within its lifetime a token serves another request of the same key without a
// request to the realm and without a challenged request; a request of another key gets a token of its own.
func TestR_REPO_033_TokenIsCachedPerKeyAndReused(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{key: "repository:a:pull", ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/a/blobs/1", a)
	mustGet(t, w, "/v2/a/blobs/2", a)
	if w.realmHits.Load() != 1 {
		t.Fatalf("two requests of one key made %d token requests, want 1", w.realmHits.Load())
	}
	if n := len(w.registrySeen()); n != 3 {
		t.Errorf("the registry got %d requests, want 3: the challenged one, its retry, and the second request with the token", n)
	}
	other := &hsAuth{key: "repository:b:pull", ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/b/blobs/1", other)
	if w.realmHits.Load() != 2 {
		t.Errorf("a second key made %d token requests in all, want 2", w.realmHits.Load())
	}
}

// TestR_REPO_033_SecondRequestOfAColdPullCarriesItsTokenWithoutAChallenge: the engine remembers the challenge of the repository and
// asks the format with it before a request, so only the first request of a replica is challenged (Q-OCI-82). The scope comes from
// the request, so a request of another scope is sent with a token fetched first.
func TestR_REPO_033_SecondRequestOfAColdPullCarriesItsTokenWithoutAChallenge(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{ttl: 5 * time.Minute} // the scope is the request's path
	mustGet(t, w, "/v2/a/manifests/1", a)
	for _, p := range []string{"/v2/b/manifests/1", "/v2/c/blobs/1", "/v2/d/blobs/1"} {
		mustGet(t, w, p, a)
	}
	if w.anonymous() != 1 {
		t.Errorf("the registry got %d requests without a token (%q), want the first alone", w.anonymous(), w.registrySeen())
	}
	if n := len(w.registrySeen()); n != 5 {
		t.Errorf("the registry got %d requests, want 5", n)
	}
	if w.realmHits.Load() != 4 {
		t.Errorf("the realm got %d requests, want one per scope", w.realmHits.Load())
	}
	// A repeated request of a known scope costs one request to the registry and none to the realm.
	before := len(w.registrySeen())
	mustGet(t, w, "/v2/b/manifests/1", a)
	if len(w.registrySeen()) != before+1 || w.realmHits.Load() != 4 {
		t.Errorf("a known scope cost %d registry requests and %d token requests", len(w.registrySeen())-before, w.realmHits.Load()-4)
	}
}

// TestR_REPO_033_ARememberedChallengeBelongsToItsConfigurationVersion: a challenge remembered under one configuration version is
// not used under another.
func TestR_REPO_033_ARememberedChallengeBelongsToItsConfigurationVersion(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/a/manifests/1", a)
	mustGet(t, w, "/v2/b/manifests/1", a)
	if w.anonymous() != 1 {
		t.Fatalf("%d anonymous requests before the change, want 1", w.anonymous())
	}
	w.rem.Repo.Version++
	mustGet(t, w, "/v2/c/manifests/1", a)
	if w.anonymous() != 2 {
		t.Errorf("%d anonymous requests after the change, want the new version's own first request", w.anonymous())
	}
}

// TestR_REPO_033_RejectedPreemptiveTokenIsReplacedOnce: a token the replica holds that the registry no longer takes is dropped, one
// fresh handshake runs, and the request is sent with the new token.
func TestR_REPO_033_RejectedPreemptiveTokenIsReplacedOnce(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{key: "k", ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/x", a)
	w.mu.Lock()
	clear(w.valid) // the registry revokes what it issued
	w.mu.Unlock()
	mustGet(t, w, "/v2/x", a)
	if w.realmHits.Load() != 2 {
		t.Errorf("%d token requests, want 2", w.realmHits.Load())
	}
	seen := w.registrySeen()
	if len(seen) != 4 || seen[0] != "" || seen[1] != "Bearer tok-1-k" || seen[2] != "Bearer tok-1-k" || seen[3] != "Bearer tok-2-k" {
		t.Errorf("the registry saw %q, want: anonymous, token, the rejected token again, a fresh token", seen)
	}
}

// TestR_REPO_033_FreshPreemptiveTokenThatIsRefusedWithTheSameKeyStands: a token fetched for the request that the registry refuses,
// when the 401's own challenge names the same key and token service, leaves the 401 as the answer with no second token request,
// and the challenge that led to it is forgotten.
func TestR_REPO_033_FreshPreemptiveTokenThatIsRefusedWithTheSameKeyStands(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/a", a)
	w.denyAl = true
	res, err := w.get("/v2/b", a)
	if err != nil || res.Status != http.StatusUnauthorized {
		t.Fatalf("GET: %v, %+v; want the 401", err, res)
	}
	if w.realmHits.Load() != 2 {
		t.Errorf("%d token requests, want 2", w.realmHits.Load())
	}
	if w.e.challenges.Len() != 0 {
		t.Error("the challenge stayed remembered")
	}
}

// TestR_REPO_033_ARegistryThatMovedItsTokenServiceIsLearnedFromTheFirst401: the remembered challenge names the old token service;
// the first request for a new scope is refused, its 401 names the new one, and one handshake with it serves that request and the
// next ones. The old service is asked once, not once per request.
func TestR_REPO_033_ARegistryThatMovedItsTokenServiceIsLearnedFromTheFirst401(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/a", a)
	w.realmAStale.Store(true)
	w.mu.Lock()
	clear(w.valid)
	w.challengeRealm = w.realmB.URL + "/token"
	w.mu.Unlock()
	for _, p := range []string{"/v2/b", "/v2/c", "/v2/d"} {
		mustGet(t, w, p, a)
	}
	if w.realmHits.Load() != 2 {
		t.Errorf("the old token service got %d requests, want 2 (the first one and one pre-emptive)", w.realmHits.Load())
	}
	if w.realmBHits.Load() != 3 {
		t.Errorf("the new token service got %d requests, want 3", w.realmBHits.Load())
	}
}

// TestR_REPO_033_ARegistryWhoseOldTokenServiceIsGoneStillServes: the remembered challenge names a token service that no longer
// answers; the pre-emptive token fails, the request is sent without one, and its own 401 names the new service. The repository is
// not blocked by what was remembered.
func TestR_REPO_033_ARegistryWhoseOldTokenServiceIsGoneStillServes(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/a", a)
	w.realm.Close() // the old token service is gone
	w.mu.Lock()
	w.challengeRealm = w.realmB.URL + "/token"
	w.mu.Unlock()
	mustGet(t, w, "/v2/b", a)
	mustGet(t, w, "/v2/c", a)
	if w.realmBHits.Load() != 2 {
		t.Errorf("the new token service got %d requests, want 2", w.realmBHits.Load())
	}
	if n := w.anonymous(); n != 2 {
		t.Errorf("%d requests went out without a token, want the first one and the one that learned the move", n)
	}
}

// TestR_REPO_033_A401FromARedirectTargetIsNotRemembered: a challenge is learned from the upstream's origin only; the answer of a
// host the request was redirected to (a blob host) names a token service of its own choosing.
func TestR_REPO_033_A401FromARedirectTargetIsNotRemembered(t *testing.T) {
	w := newHSWorld(t)
	w.otherChallenges = true
	res, err := w.get("/redir", &hsAuth{key: "k", ttl: time.Minute})
	if err != nil || res.Status != http.StatusUnauthorized {
		t.Fatalf("GET: %v, %+v; want the redirect target's 401", err, res)
	}
	if w.e.challenges.Len() != 0 {
		t.Error("the challenge of a redirect target was remembered")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, a := range w.otherAuth {
		if a != "" {
			t.Errorf("the redirect target saw %q", a)
		}
	}
}

// TestR_REPO_033_ADeadRealmOnTheOriginCostsOneChallengedRequest: a 401 that names a token service that cannot serve is the answer of
// that request; it is not remembered, so the next request is challenged afresh and served once the registry is right again.
func TestR_REPO_033_ADeadRealmOnTheOriginCostsOneChallengedRequest(t *testing.T) {
	w := newHSWorld(t)
	w.mu.Lock()
	w.challengeRealm = w.registry.URL + "/nonexistent"
	w.mu.Unlock()
	a := &hsAuth{ttl: 5 * time.Minute}
	if _, err := w.get("/v2/a", a); err == nil {
		t.Fatal("the dead realm served a token")
	}
	if w.e.challenges.Len() != 0 {
		t.Fatal("the challenge of a dead realm was remembered")
	}
	w.mu.Lock()
	w.challengeRealm = ""
	w.mu.Unlock()
	mustGet(t, w, "/v2/a", a)
	if w.anonymous() != 3 {
		// the dead realm is itself a path on the registry: its request is the second anonymous one
		t.Errorf("%d anonymous requests (%q)", w.anonymous(), w.registrySeen())
	}
}

// TestR_REPO_033_OnlyARequestToTheOriginIsSentWithAPreemptiveToken: a request to another host (an extraHosts entry, a mapped URL)
// can never carry the token, so none is fetched for it.
func TestR_REPO_033_OnlyARequestToTheOriginIsSentWithAPreemptiveToken(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/a", a)
	hits := w.realmHits.Load()
	cl, _ := w.e.o.Clients.For(context.Background(), w.rem.Repo, w.rem.Settings)
	h := &handshake{cl: cl, rem: w.rem, auth: a}
	foreign, _ := http.NewRequest(http.MethodGet, w.other.URL+"/blob", nil) // another port: another origin
	if got := w.e.preemptive(context.Background(), h, foreign); got != nil || w.realmHits.Load() != hits {
		t.Errorf("a token was fetched for another origin: %v, %d token requests", got, w.realmHits.Load()-hits)
	}
}

// TestR_REPO_033_TokenExpiresTenSecondsBeforeItsLifetime: a token is used until 10 s before the lifetime its service named, and a
// service that names none gets 60 s.
func TestR_REPO_033_TokenExpiresTenSecondsBeforeItsLifetime(t *testing.T) {
	for _, tc := range []struct {
		name string
		ttl  time.Duration
		// within is a time at which the token is still used, after one at which it is not.
		within, after time.Duration
	}{
		{"a lifetime of 300 s", 300 * time.Second, 289 * time.Second, 291 * time.Second},
		{"no lifetime means 60 s", 0, 49 * time.Second, 51 * time.Second},
		{"a lifetime of 30 s", 30 * time.Second, 19 * time.Second, 21 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHSWorld(t)
			a := &hsAuth{key: "k", ttl: tc.ttl}
			mustGet(t, w, "/v2/x", a)
			w.clk.advance(tc.within)
			mustGet(t, w, "/v2/x", a)
			if w.realmHits.Load() != 1 {
				t.Fatalf("after %v the token was fetched again (%d token requests)", tc.within, w.realmHits.Load())
			}
			w.clk.advance(tc.after - tc.within)
			mustGet(t, w, "/v2/x", a)
			if w.realmHits.Load() != 2 {
				t.Errorf("after %v the token was not fetched again (%d token requests)", tc.after, w.realmHits.Load())
			}
		})
	}
}

// TestR_REPO_033_ATokenThatLivesTenSecondsOrLessIsNotKept: there is nothing left to reuse of a lifetime the skew eats up.
func TestR_REPO_033_ATokenThatLivesTenSecondsOrLessIsNotKept(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{key: "k", ttl: 10 * time.Second}
	mustGet(t, w, "/v2/x", a)
	mustGet(t, w, "/v2/x", a)
	if w.realmHits.Load() != 2 {
		t.Errorf("%d token requests, want 2", w.realmHits.Load())
	}
}

// TestR_ARCH_032_ConfigurationVersionChangeDropsTheToken: the token is cached under the repository's configuration version, so a
// changed configuration (credential, extraHosts, URL) never meets a token obtained under the old one.
func TestR_ARCH_032_ConfigurationVersionChangeDropsTheToken(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{key: "k", ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/x", a)
	w.rem.Repo.Version++
	mustGet(t, w, "/v2/x", a)
	if w.realmHits.Load() != 2 {
		t.Errorf("a new configuration version made %d token requests in all, want 2", w.realmHits.Load())
	}
}

// TestR_ARCH_032_ChangeEvictsTheTokensAndChallengesOfOlderVersions: a change of repo_config reaches the registered caches: they drop
// a repository's entries of versions below the one the change produced, and keep a newer one and another repository's.
func TestR_ARCH_032_ChangeEvictsTheTokensAndChallengesOfOlderVersions(t *testing.T) {
	w := newHSWorld(t)
	exp := w.clk.now().Add(time.Minute)
	for _, k := range []tokenKey{{repo: 7, version: 3}, {repo: 7, version: 4}, {repo: 8, version: 1}} {
		w.e.tokens.Set(k, tokenEntry{authorization: "Bearer a", expires: exp})
		w.e.challenges.Set(challengeKey{repo: k.repo, version: k.version}, http.Header{"Www-Authenticate": {"Bearer realm=\"x\""}})
	}
	w.reg.ApplyChange(cluster.AreaRepoConfig, 1, json.RawMessage(`{"id":"7","key":"registry","op":"updated","version":4}`))
	for _, c := range []struct {
		repo, version int64
		kept          bool
	}{{7, 3, false}, {7, 4, true}, {8, 1, true}} {
		if _, ok := w.e.tokens.Get(tokenKey{repo: c.repo, version: c.version}); ok != c.kept {
			t.Errorf("token of repository %d version %d kept = %v, want %v", c.repo, c.version, ok, c.kept)
		}
		if _, ok := w.e.challenges.Get(challengeKey{repo: c.repo, version: c.version}); ok != c.kept {
			t.Errorf("challenge of repository %d version %d kept = %v, want %v", c.repo, c.version, ok, c.kept)
		}
	}
	w.reg.ApplyChange(cluster.AreaRepoConfig, 1, json.RawMessage(`{"id":"7","key":"registry","op":"deleted"}`))
	if w.e.tokens.Len() != 1 || w.e.challenges.Len() != 1 {
		t.Errorf("after the deletion %d tokens and %d challenges remain, want the other repository's", w.e.tokens.Len(), w.e.challenges.Len())
	}
}

// TestR_REPO_033_TokenIsNotReusedUnderAnotherCredentialOrTokenService: a rotation of the secret behind a reference moves no
// configuration version, so the credential in effect and the token service are part of the key.
func TestR_REPO_033_TokenIsNotReusedUnderAnotherCredentialOrTokenService(t *testing.T) {
	w := newHSWorld(t, func(s *repository.RemoteSettings) { s.AuthMode = repository.AuthBasic; s.Username = "robot" })
	cl, _ := w.e.o.Clients.For(context.Background(), w.rem.Repo, w.rem.Settings)
	h := handshake{cl: cl, rem: w.rem}
	u1, _ := url.Parse("https://auth.example.com/token?service=a&scope=x")
	base := tokenKeyOf(h, "k", u1)
	for name, other := range map[string]tokenKey{
		"another key":           tokenKeyOf(h, "k2", u1),
		"another host":          tokenKeyOf(h, "k", mustURL("https://auth.example.net/token?service=a")),
		"another port":          tokenKeyOf(h, "k", mustURL("https://auth.example.com:8443/token?service=a")),
		"another scheme":        tokenKeyOf(h, "k", mustURL("http://auth.example.com/token?service=a")),
		"another path":          tokenKeyOf(h, "k", mustURL("https://auth.example.com/other?service=a")),
		"another service":       tokenKeyOf(h, "k", mustURL("https://auth.example.com/token?service=b")),
		"another realm query":   tokenKeyOf(h, "k", mustURL("https://auth.example.com/token?service=a&account=other")),
		"another configuration": tokenKeyOf(handshake{cl: cl, rem: func() Remote { r := w.rem; r.Repo.Version++; return r }()}, "k", u1),
	} {
		if other == base {
			t.Errorf("%s gives the key of the original", name)
		}
	}
	if tokenKeyOf(h, "k", mustURL("https://auth.example.com:443/token?service=a&scope=y")) != base {
		t.Error("the default port and another scope change the key")
	}

	// The rotated secret: a client with another password has another fingerprint.
	w2 := newHSWorld(t, func(s *repository.RemoteSettings) { s.AuthMode = repository.AuthBasic; s.Username = "robot" })
	cl2, _ := w2.e.o.Clients.For(context.Background(), w2.rem.Repo, w2.rem.Settings)
	if cl.CredentialFingerprint() != cl2.CredentialFingerprint() || cl.CredentialFingerprint() == ([32]byte{}) {
		t.Fatal("the same credential must give the same, non-zero fingerprint")
	}
	pw := observability.Secret("rotated")
	clients, _ := upstream.New(upstream.Options{Factory: outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"}}), Caches: mustRegistry(t), Proxy: hsProxyConfig(t), SafetyTTL: time.Minute})
	rotated, err := clients.Ephemeral("registry", w.rem.Settings, &pw)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.CredentialFingerprint() == cl.CredentialFingerprint() {
		t.Error("a rotated secret keeps the fingerprint")
	}
	if tokenKeyOf(handshake{cl: rotated, rem: w.rem}, "k", u1) == base {
		t.Error("a rotated secret keeps the key")
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func mustRegistry(t *testing.T) *cache.Registry {
	t.Helper()
	r, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestR_REPO_033_SecondUnauthorizedStands: the original request is retried once; a 401 to the retry is the answer, and the engine
// asks the token service once.
func TestR_REPO_033_SecondUnauthorizedStands(t *testing.T) {
	w := newHSWorld(t)
	w.denyAl = true
	res, err := w.get("/v2/x", &hsAuth{key: "k", ttl: time.Minute})
	if err != nil || res.Status != http.StatusUnauthorized {
		t.Fatalf("GET: %v, %+v; want the 401", err, res)
	}
	if n := len(w.registrySeen()); n != 2 {
		t.Errorf("the registry got %d requests, want the original and one retry", n)
	}
	if w.realmHits.Load() != 1 {
		t.Errorf("the realm got %d requests, want 1", w.realmHits.Load())
	}
}

// TestR_REPO_033_OversizedRealmAnswerIsRefused: the engine reads at most 64 KiB of the token service's answer; a larger one is
// 502 upstream_error, no token is kept and the original request is not retried.
func TestR_REPO_033_OversizedRealmAnswerIsRefused(t *testing.T) {
	w := newHSWorld(t)
	w.realmPad = MaxTokenAnswerBytes + 1
	_, err := w.get("/v2/x", &hsAuth{key: "k", ttl: time.Minute})
	if codeOfErr(err) != errs.CodeUpstreamError {
		t.Fatalf("GET: %v, want upstream_error", err)
	}
	if n := len(w.registrySeen()); n != 1 {
		t.Errorf("the registry got %d requests, want the original alone", n)
	}
	if w.e.tokens.Len() != 0 {
		t.Error("a token was kept")
	}
	// Just under the bound is read.
	w2 := newHSWorld(t)
	w2.realmPad = MaxTokenAnswerBytes - 200
	mustGet(t, w2, "/v2/x", &hsAuth{key: "k", ttl: time.Minute})
}

// TestR_REPO_033_ConcurrentRequestsShareOneTokenFetch: identical handshakes of one replica wait for one request to the token
// service, and a request that finds the token another stored since takes it.
func TestR_REPO_033_ConcurrentRequestsShareOneTokenFetch(t *testing.T) {
	w := newHSWorld(t)
	w.realmDelay = 300 * time.Millisecond
	a := &hsAuth{key: "k", ttl: time.Minute}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mustGet(t, w, "/v2/x", a)
		}()
	}
	wg.Wait()
	if w.realmHits.Load() != 1 {
		t.Errorf("12 concurrent requests made %d token requests, want 1", w.realmHits.Load())
	}
}

// TestR_REPO_033_ARejectionOfAnOldTokenDoesNotDropANewerOne: the drop after a rejection compares the value, so a token that another
// request stored since stays.
func TestR_REPO_033_ARejectionOfAnOldTokenDoesNotDropANewerOne(t *testing.T) {
	w := newHSWorld(t)
	key := tokenKey{repo: 7, version: 3}
	w.e.tokens.Set(key, tokenEntry{authorization: "Bearer new", expires: w.clk.now().Add(time.Minute)})
	w.e.dropToken(key, "Bearer old")
	if _, ok := w.e.tokens.Get(key); !ok {
		t.Fatal("a token other than the rejected one was dropped")
	}
	w.e.dropToken(key, "Bearer new")
	if _, ok := w.e.tokens.Get(key); ok {
		t.Fatal("the rejected token stayed")
	}
	// tokenOnce takes the newer token another request stored instead of asking again.
	w.e.tokens.Set(key, tokenEntry{authorization: "Bearer new", expires: w.clk.now().Add(time.Minute)})
	ent, cached, err := w.e.tokenOnce(context.Background(), "r", key, "Bearer old", func(context.Context) (tokenEntry, error) {
		t.Error("the token service was asked")
		return tokenEntry{}, nil
	})
	if err != nil || !cached || ent.authorization != "Bearer new" {
		t.Errorf("tokenOnce = %+v, %v, %v", ent, cached, err)
	}
}

// TestR_REPO_033_APanickingFormatDoesNotLeaveACallBehind: a panic in the format's ReadToken is the request's failure, the call is
// released, and the next request is served.
func TestR_REPO_033_APanickingFormatDoesNotLeaveACallBehind(t *testing.T) {
	w := newHSWorld(t)
	_, err := w.get("/v2/x", &hsAuth{key: "k", panics: true})
	if codeOfErr(err) != errs.CodeUpstreamError {
		t.Fatalf("GET: %v, want upstream_error", err)
	}
	w.e.amu.Lock()
	n := len(w.e.acalls)
	w.e.amu.Unlock()
	if n != 0 {
		t.Fatalf("%d calls left behind", n)
	}
	mustGet(t, w, "/v2/x", &hsAuth{key: "k"})
}

// TestR_REPO_033_ACallerThatLeavesDoesNotEndTheTokenRequest: a waiter whose context ends gets an unavailable error that counts toward
// no circuit; the request to the token service runs on, and its token serves the next request.
func TestR_REPO_033_ACallerThatLeavesDoesNotEndTheTokenRequest(t *testing.T) {
	w := newHSWorld(t)
	w.realmDelay = 400 * time.Millisecond
	a := &hsAuth{key: "k", ttl: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	u, _ := url.Parse(w.registry.URL + "/v2/x")
	_, err := w.e.Get(ctx, Get{Remote: w.rem, URL: u, Auth: a})
	if err == nil {
		t.Fatal("the request outlived its context")
	}
	if he, ok := asHandshakeError(err); ok && he.fail != nil {
		t.Errorf("a context error is classified as a failure of the token service: %+v", he.fail)
	}
	time.Sleep(600 * time.Millisecond)
	mustGet(t, w, "/v2/x", a)
	if w.realmHits.Load() != 1 {
		t.Errorf("%d token requests: the first one did not run on", w.realmHits.Load())
	}
}

// TestR_REPO_033_RealmOnAnotherHostNeedsAnExtraHostsEntry: a token service on a host that is neither the upstream nor an extraHosts
// entry is refused before a byte is sent (502 foreign_host_denied); with the entry it is asked. (The two servers are two origins on
// one host name; the credential rule on distinct host names is in test/integration/proxy.) An address with a user is refused.
func TestR_REPO_033_RealmOnAnotherHostNeedsAnExtraHostsEntry(t *testing.T) {
	w := newHSWorld(t, func(s *repository.RemoteSettings) { s.ExtraHosts = []repository.ExtraHost{} })
	_, err := w.get("/v2/x", &hsAuth{key: "k"})
	if codeOfErr(err) != errs.CodeForeignHostDenied {
		t.Fatalf("GET: %v, want foreign_host_denied", err)
	}
	if w.realmHits.Load() != 0 {
		t.Errorf("the realm got %d requests, want none", w.realmHits.Load())
	}
	mustGet(t, newHSWorld(t), "/v2/x", &hsAuth{key: "k"})

	w = newHSWorld(t)
	_, err = w.get("/v2/x", &hsAuth{key: "k", userinfo: true})
	if codeOfErr(err) != errs.CodeUpstreamError || w.realmHits.Load() != 0 {
		t.Errorf("an address with a user: %v, %d token requests", err, w.realmHits.Load())
	}
}

// TestR_REPO_033_TokenServiceRefusalNamesItsHostInTheSecurityEvent: an address the egress policy refuses at the token service's host
// is counted and audited with that host (R-REPO-038), and is the request's answer; the circuit is not told.
func TestR_REPO_033_TokenServiceRefusalNamesItsHostInTheSecurityEvent(t *testing.T) {
	w := newHSWorldDeny(t, []string{"10.0.0.0/8"}, func(s *repository.RemoteSettings) {
		s.ExtraHosts = []repository.ExtraHost{{Pattern: "10.0.0.1"}}
	})
	_, err := w.get("/v2/x", &hsAuth{key: "k", realm: "http://10.0.0.1:9/token"})
	if codeOfErr(err) != errs.CodeEgressDenied {
		t.Fatalf("GET: %v, want egress_denied", err)
	}
	ev := w.audit.types()
	if len(ev) != 1 || !strings.HasPrefix(ev[0], audit.EventProxyEgressDenied+" ") || !strings.Contains(ev[0], `"host":"10.0.0.1"`) {
		t.Errorf("audit events = %q, want proxy.egress_denied naming 10.0.0.1", ev)
	}
	var m dto.Metric
	if err := w.e.m.egressDenied.WithLabelValues("it/registry").Write(&m); err != nil || m.GetCounter().GetValue() != 1 {
		t.Errorf("trove_remote_egress_denied_total = %v (%v), want 1", m.GetCounter().GetValue(), err)
	}
}

// TestR_REPO_033_TokenRequestsCarryNoCredentialOnARedirect: the credential goes to the token service's host and to no redirect
// target, even with forwardAuthOnRedirect (the redirect is the upstream's choice).
func TestR_REPO_033_TokenRequestsCarryNoCredentialOnARedirect(t *testing.T) {
	w := newHSWorld(t, func(s *repository.RemoteSettings) {
		s.AuthMode, s.Username, s.ForwardAuthOnRedirect = repository.AuthBasic, "robot", true
		s.ExtraHosts = []repository.ExtraHost{{Pattern: "127.0.0.1", SendCredentials: true}}
	})
	w.realmRedirect = true
	_, _ = w.get("/v2/x", &hsAuth{key: "k"})
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.realmAuth) != 1 || !strings.HasPrefix(w.realmAuth[0], "Basic ") {
		t.Fatalf("the token service saw %q, want the credential", w.realmAuth)
	}
	if len(w.otherAuth) != 1 || w.otherAuth[0] != "" {
		t.Errorf("the redirect target saw %q, want no credential", w.otherAuth)
	}
}

// TestR_REPO_033_NoHandshakeLeavesTheUnauthorized: a format that knows no handshake for the answer, and a repository that sends a
// static bearer token, leave the 401 as the answer and ask nobody.
func TestR_REPO_033_NoHandshakeLeavesTheUnauthorized(t *testing.T) {
	w := newHSWorld(t)
	res, err := w.get("/v2/x", &hsAuth{none: true})
	if err != nil || res.Status != http.StatusUnauthorized || w.realmHits.Load() != 0 {
		t.Errorf("a format without a handshake: %v, %+v, %d token requests", err, res, w.realmHits.Load())
	}

	w = newHSWorld(t, func(s *repository.RemoteSettings) { s.AuthMode = repository.AuthBearer })
	a := &hsAuth{key: "k"}
	res, err = w.get("/v2/x", a)
	if err != nil || res.Status != http.StatusUnauthorized || a.asks.Load() != 0 || w.realmHits.Load() != 0 {
		t.Errorf("authMode bearer: %v, %+v, %d asks, %d token requests", err, res, a.asks.Load(), w.realmHits.Load())
	}
	if seen := w.registrySeen(); len(seen) != 1 || seen[0] != "Bearer s3cret" {
		t.Errorf("the registry saw %q, want the stored secret as the bearer token and no retry", seen)
	}

	w = newHSWorld(t)
	res, err = w.get("/v2/x", nil)
	if err != nil || res.Status != http.StatusUnauthorized {
		t.Errorf("a request without an authenticator: %v, %+v", err, res)
	}
}

// TestR_REPO_033_TokenServiceFailureIsTheAnswer: a token service that refuses, and a format that rejects its answer or returns a
// value that cannot be a header, make the request fail with an upstream error; nothing is kept and the registry is asked once.
func TestR_REPO_033_TokenServiceFailureIsTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		raw    string
		code   string
	}{
		{"the service refuses the credentials", http.StatusUnauthorized, "", errs.CodeUpstreamAuthFailed},
		{"the service fails", http.StatusInternalServerError, "", errs.CodeUpstreamError},
		{"the token holds a line break", 0, "Bearer a\r\nX-Injected: 1", errs.CodeUpstreamError},
		{"the token holds a NUL", 0, "Bearer a\x00b", errs.CodeUpstreamError},
		{"the token holds a control character", 0, "Bearer a\x01b", errs.CodeUpstreamError},
		{"the token holds DEL", 0, "Bearer a\x7fb", errs.CodeUpstreamError},
		{"the token is empty", 0, "", errs.CodeUpstreamError},
		{"the token is blank", 0, "  ", errs.CodeUpstreamError},
		{"the token is above 8 KiB", 0, "Bearer " + strings.Repeat("a", maxAuthorizationBytes), errs.CodeUpstreamError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newHSWorld(t)
			w.realmStatus = tc.status
			_, err := w.get("/v2/x", &hsAuth{key: "k", raw: tc.raw, useRaw: tc.status == 0})
			if codeOfErr(err) != tc.code {
				t.Fatalf("GET: %v, want %s", err, tc.code)
			}
			if n := len(w.registrySeen()); n != 1 {
				t.Errorf("the registry got %d requests, want 1", n)
			}
			if w.e.tokens.Len() != 0 {
				t.Error("a token was kept")
			}
		})
	}
	// The largest value is sent.
	w := newHSWorld(t)
	res, err := w.get("/v2/x", &hsAuth{key: "k", raw: "Bearer " + strings.Repeat("a", maxAuthorizationBytes-len("Bearer ")), useRaw: true})
	if seen := w.registrySeen(); err != nil || res.Status != http.StatusUnauthorized || len(seen) != 2 || len(seen[1]) != maxAuthorizationBytes {
		t.Errorf("a value of the largest size: %v, %+v, %d requests", err, res, len(seen))
	}
}

// TestR_REPO_033_ATokenKeyAboveOneKiBIsRefused: the key comes from the upstream's request and challenge, and bounds the cache.
func TestR_REPO_033_ATokenKeyAboveOneKiBIsRefused(t *testing.T) {
	w := newHSWorld(t)
	_, err := w.get("/v2/x", &hsAuth{key: strings.Repeat("k", maxTokenKeyBytes+1)})
	if codeOfErr(err) != errs.CodeUpstreamError || w.realmHits.Load() != 0 {
		t.Errorf("GET: %v, %d token requests", err, w.realmHits.Load())
	}
	mustGet(t, w, "/v2/x", &hsAuth{key: strings.Repeat("k", maxTokenKeyBytes)})
}

// TestR_REPO_054_TokenServiceRateLimitIsAMirrorsRateLimit: a 429 of the token service to a pass's request reaches the mirror as the
// typed rate-limit error, so the back-off of R-REPO-054 applies, not the failure of an item.
func TestR_REPO_054_TokenServiceRateLimitIsAMirrorsRateLimit(t *testing.T) {
	w := newHSWorld(t)
	w.realmStatus = http.StatusTooManyRequests
	w.realmHeader = map[string]string{"Retry-After": "7"}
	_, err := w.get("/v2/x", &hsAuth{key: "k"})
	var rl *upstream.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("GET: %T %v, want *upstream.RateLimitError", err, err)
	}
	if wait, hinted := rl.RetryAfter(); rl.Status != http.StatusTooManyRequests || !hinted || wait != 7*time.Second {
		t.Errorf("rate limit = %+v", rl)
	}
}

// TestR_REPO_033_ATokenWhoseRetryFailsIsNotKept: a retry that fails for another reason than the caller's context drops the token it
// used, so a token the transport refuses is not kept for its lifetime.
func TestR_REPO_033_ATokenWhoseRetryFailsIsNotKept(t *testing.T) {
	w := newHSWorld(t)
	cl, _ := w.e.o.Clients.For(context.Background(), w.rem.Repo, w.rem.Settings)
	h := &handshake{cl: cl, rem: w.rem, auth: &hsAuth{key: "k", ttl: time.Minute}}
	u, _ := url.Parse(w.registry.URL + "/v2/x")
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	resp, err := cl.HTTP().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("the transport refused the header")
	failing := func(context.Context) (*http.Response, context.CancelFunc, *atomic.Bool, error) {
		return nil, nil, nil, boom
	}
	_, _, _, err = w.e.authenticate(context.Background(), h, req, resp, func() {}, nil, failing, nil) //nolint:bodyclose // the retry fails: there is no response
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if w.e.tokens.Len() != 0 {
		t.Error("the token stayed")
	}
}

// TestR_REPO_033_OnlyTheFirstRequestOfAFetchReportsTheSlotWait: the challenged request's retry, a revalidation request and the GET
// after it report 0 (Q-REPO-236).
func TestR_REPO_033_OnlyTheFirstRequestOfAFetchReportsTheSlotWait(t *testing.T) {
	f := &fetcher{hasWait: true, wait: 5 * time.Second}
	got := make([]time.Duration, 0, 4)
	for _, attempt := range []int{1, 1, 1, 2} {
		d, ok := f.slotWait(attempt)
		if !ok {
			t.Fatal("a fetch that took a slot reports its wait")
		}
		got = append(got, d)
	}
	if got[0] != 5*time.Second || got[1] != 0 || got[2] != 0 || got[3] != 0 {
		t.Errorf("reported waits = %v, want the first only", got)
	}
}

// TestR_REPO_033_AChallengeIsRememberedFromTheAnswerItCameWith: the origin is checked for the answer whose challenge is followed, not
// for the first one: the origin's 401 is followed with a held token that a redirect target's 401 then refuses, whose challenge
// leads to the token that is accepted; that challenge is the redirect target's and is not remembered.
func TestR_REPO_033_AChallengeIsRememberedFromTheAnswerItCameWith(t *testing.T) {
	w := newHSWorld(t)
	a := &hsAuth{key: "k", ttl: 5 * time.Minute}
	mustGet(t, w, "/v2/a", a) // a token under the key is held
	w.e.challenges.Flush("test")
	w.otherOnce = true
	res, err := w.get("/redir-auth", a)
	if err != nil || res.Status != http.StatusOK {
		t.Fatalf("GET: %v, %+v", err, res)
	}
	if w.otherHits.Load() != 2 {
		t.Fatalf("the redirect target got %d requests, want its 401 and the accepted retry", w.otherHits.Load())
	}
	if w.e.challenges.Len() != 0 {
		t.Error("the challenge of a redirect target was remembered")
	}
}
