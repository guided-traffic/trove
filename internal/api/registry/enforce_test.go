package registry_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/api/registry/registrytest"
	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/auth"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the OCI format, whose routes are mounted
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/secrets"
)

// R-OCI-005 ("Use", the grant as a ceiling below /v2/). The tests below build the mount the way the registry root does - the dispatcher's own match, every route of the
// OCI format and the core routes mounted through the enforcer - and put the real authentication stage with a real signer in
// front of the router, so a registry token travels in the Authorization header as in production.

var (
	hub   = qname.Repo{Project: "web", Key: "docker-hub"}
	local = qname.Repo{Project: "web", Key: "docker-local"}
)

const exampleBase = "/v2/web/docker-local/"

// stubs the dispatcher needs to be created; its match uses none of them.
type (
	stubHost    struct{ formats.Host }
	stubDecider struct{ authz.Decider }
	stubRepos   struct{ repository.Reader }
)

// tokens knows one PAT of alice, by its value and by its hash.
type tokens struct {
	pat  string
	info token.Info
}

func (f tokens) Validate(context.Context, string) (token.Info, error) {
	return token.Info{}, token.ErrUnknown
}

func (f tokens) CheckSecretHash(_ context.Context, h [32]byte, typ token.Type, _ token.Request) (token.Info, error) {
	if h != token.Hash(f.pat) || typ != f.info.Type {
		return token.Info{}, token.ErrUnknown
	}
	return f.info, nil
}

type principals struct{}

func (principals) ByID(context.Context, string) (principal.Principal, error) {
	return principal.Principal{}, principal.ErrNotFound
}

func (principals) ByName(context.Context, principal.Kind, string) (principal.Principal, error) {
	return principal.Principal{}, principal.ErrNotFound
}
func (principals) Groups(context.Context, string) ([]principal.GroupRef, error) { return nil, nil }

var alice = principal.Principal{ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Kind: principal.KindUser, Name: "alice", Status: principal.StatusActive}

// mount is the endpoint of the test: its router, its enforcer, its whole handler and a minter of registry tokens.
type mount struct {
	router   *httpserver.Router
	enforcer *registry.Enforcer
	handler  http.Handler
	mint     func(registrytoken.Grant) string
	oci      formats.Format
	// served is the route the stand-in handler saw last, by the matcher the mount was built with.
	served string
}

func writeError(w http.ResponseWriter, _ *http.Request, err error) {
	_, e := errs.ProblemOf(err, "", "")
	if e.Challenge != "" {
		w.Header().Set("WWW-Authenticate", e.Challenge)
	}
	w.Header().Set("X-Code", e.Code)
	w.WriteHeader(e.Status())
}

var ok200 = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

// newMount builds the endpoint. matcher overrides the dispatcher's match (nil: the dispatcher's); skip leaves the named OCI
// route out of the mount.
func newMount(t *testing.T, matcher route.Matcher, skip string) *mount {
	t.Helper()
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: stubRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	if matcher == nil {
		matcher = disp
	}
	e, err := registry.NewEnforcer(registry.EnforcerOptions{Matcher: matcher, WriteError: writeError})
	if err != nil {
		t.Fatal(err)
	}
	var oci formats.Format
	for _, f := range formats.List() {
		if f.Descriptor().RegistryRoot {
			oci = f
		}
	}
	if oci == nil {
		t.Fatal("no registry-root format is registered")
	}
	m := &mount{router: httpserver.NewClientRouter(), enforcer: e, oci: oci}
	// The stand-in of the handlers records which route the match names for the request it serves, as the dispatcher's handler
	// would serve by that same match.
	recording := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.served = ""
		if t, ok := matcher.RegistryTarget(r); ok {
			m.served = t.Route
		}
		w.WriteHeader(http.StatusOK)
	})
	var routes []formats.Route
	for _, rt := range oci.Routes() {
		if rt.Name != skip {
			routes = append(routes, rt)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.Mount(m.router, []string{"/v2/{project}/{key}/{rest...}"}, recording, registry.RepositoryRoutes(routes, exampleBase)...))
	must(e.Mount(m.router, []string{"GET /v2/{$}", "GET /v2"}, recording, registry.Route{Name: route.NameVersion, Kind: registry.RouteVersion, Method: "GET", Example: "/v2/"}))
	must(e.Mount(m.router, []string{"GET /v2/_catalog"}, recording, registry.Route{Name: route.NameCatalog, Kind: registry.RouteCatalog, Method: "GET", Example: "/v2/_catalog"}))
	// The token service refuses a registry token as its credential; its stand-in here does the same.
	must(e.Mount(m.router, []string{"/v2/_token"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recording.ServeHTTP(httptest.NewRecorder(), r)
		w.Header().Set("WWW-Authenticate", `Basic realm="trove"`)
		w.WriteHeader(http.StatusUnauthorized)
	}), registry.Route{Name: route.NameToken, Kind: registry.RouteToken, Method: "GET", Example: "/v2/_token"}))

	signer := newSigner(t)
	pat, err := token.Generate(token.TypePAT)
	must(err)
	stage := auth.New(auth.Options{
		Tokens:     tokens{pat: pat, info: token.Info{ID: "t1", Type: token.TypePAT, Scope: token.Scope{Inherit: true}, Owner: alice}},
		Principals: principals{}, Registry: signer,
	})
	rules := auth.EndpointRules{Name: "default", TokenTypes: []token.Type{token.TypePAT}}
	m.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(httpserver.WithResolved(r.Context(), httpserver.RequestInfo{BaseURL: "https://trove.test"}))
		res, err := stage.Authenticate(r, rules)
		if err != nil {
			writeError(w, r, err)
			return
		}
		p := httpserver.Principal{Kind: httpserver.KindUser, ID: res.Principal.ID, Name: res.Principal.Name, Anonymous: res.Anonymous}
		if g := res.Credential.Registry; g != nil {
			p.Credential.Registry = &httpserver.RegistryGrant{Catalog: g.Catalog}
			for _, en := range g.Entries {
				p.Credential.Registry.Entries = append(p.Credential.Registry.Entries, httpserver.RegistryEntry{
					Repo: en.Repo, Name: en.Name,
					Pull: en.Actions.Has(registrytoken.Pull), Push: en.Actions.Has(registrytoken.Push), Delete: en.Actions.Has(registrytoken.Delete),
				})
			}
		}
		m.router.ServeHTTP(w, r.WithContext(httpserver.WithPrincipal(r.Context(), p)))
	})
	m.mint = func(g registrytoken.Grant) string {
		now := time.Now().Truncate(time.Second)
		tok, err := signer.Issue(registrytoken.Claims{
			PrincipalKind: string(alice.Kind), PrincipalID: alice.ID, Endpoint: "default", IssuedAt: now, ExpiresAt: now.Add(time.Minute),
			Binding: registrytoken.Binding{TokenID: "t1", TokenType: "pat", SecretSHA256: token.Hash(pat)}, Grant: g,
		})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	return m
}

func newSigner(t *testing.T) *registrytoken.Signer {
	t.Helper()
	p := filepath.Join(t.TempDir(), "k1.key")
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	ring, err := secrets.Load(config.Secrets{EncryptionKeyFiles: []string{p}, ActiveKeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := registrytoken.NewSigner(ring, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (m *mount) walk() registrytest.Walk {
	return registrytest.Walk{
		Enforcer: m.enforcer, Handler: m.handler, Patterns: m.router.Patterns(), Format: m.oci, Repo: local, Mint: m.mint,
		Served: func(do func()) string {
			m.served = ""
			do()
			return m.served
		},
	}
}

// send sends a request with a registry token of the grant and returns the status, the challenge and the error code.
func (m *mount) send(method, path string, g registrytoken.Grant) (int, string, string) {
	r := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	r.Header.Set("Authorization", "Bearer "+m.mint(g))
	w := httptest.NewRecorder()
	m.handler.ServeHTTP(w, r)
	return w.Code, w.Header().Get("WWW-Authenticate"), w.Header().Get("X-Code")
}

// failures records what a walk reports, so a test can prove that the walk catches a wrong mount.
type failures struct {
	testing.TB
	msgs []string
}

func (f *failures) Helper()                   {}
func (f *failures) Errorf(s string, a ...any) { f.msgs = append(f.msgs, fmt.Sprintf(s, a...)) }
func (f *failures) Error(a ...any)            { f.msgs = append(f.msgs, fmt.Sprint(a...)) }
func (f *failures) Fatal(a ...any)            { f.msgs = append(f.msgs, fmt.Sprint(a...)) }

func (f *failures) has(part string) bool {
	for _, m := range f.msgs {
		if strings.Contains(m, part) {
			return true
		}
	}
	return false
}

// R-OCI-005, unit level: the end-to-end proof is in test/integration/registry.
func TestTheEnforcementPointThroughTheWholeHandler(t *testing.T) {
	m := newMount(t, nil, "")
	t.Run("the walk passes: every route mounted, matched to itself, refused for an empty grant, opened by exactly its action", func(t *testing.T) {
		m.walk().Run(t)
	})
	t.Run("R-ARCH-005 the route table admits exactly the patterns mounted through the enforcer", func(t *testing.T) {
		if v := httpserver.CheckRouteTable(m.router.Patterns(), m.enforcer.Patterns(), nil); len(v) != 0 {
			t.Fatalf("violations %v", v)
		}
		m.router.Handle("GET /v2/sneaky", ok200)
		v := httpserver.CheckRouteTable(m.router.Patterns(), m.enforcer.Patterns(), nil)
		if len(v) != 1 || !strings.Contains(v[0], "/v2/sneaky") {
			t.Fatalf("violations %v", v)
		}
		var none *registry.Enforcer
		if len(httpserver.CheckRouteTable([]string{"/v2/{project}/{key}/{rest...}"}, none.Patterns(), nil)) != 1 {
			t.Fatal("without an enforcer a /v2/ pattern passed the route table")
		}
		f := &failures{TB: t}
		m.walk().Run(f)
		if !f.has(`"GET /v2/sneaky" below /v2/ was not mounted`) {
			t.Fatalf("the walk missed a pattern mounted around the enforcer: %v", f.msgs)
		}
	})
	t.Run("a request without a registry token is the decision point's alone", func(t *testing.T) {
		r := httptest.NewRequestWithContext(context.Background(), "DELETE", exampleBase+"app/manifests/1", nil)
		w := httptest.NewRecorder()
		m.router.ServeHTTP(w, r.WithContext(httpserver.WithPrincipal(r.Context(), httpserver.AnonymousPrincipal())))
		if w.Code != 200 {
			t.Fatalf("%d", w.Code)
		}
	})
	t.Run("the challenge names the missing scope and the refusal has its own code", func(t *testing.T) {
		g := registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: local, Name: "app", Actions: registrytoken.Pull}}}
		if code, _, _ := m.send("GET", exampleBase+"app/manifests/1", g); code != 200 {
			t.Fatalf("pull: %d", code)
		}
		code, ch, c := m.send("POST", exampleBase+"app/blobs/uploads/", g)
		if code != 401 || c != errs.CodeOCIInsufficientScope ||
			ch != `Bearer realm="https://trove.test/v2/_token",service="trove.test",scope="repository:web/docker-local/app:push",error="insufficient_scope"` {
			t.Fatalf("push: %d %s %s", code, ch, c)
		}
	})
	t.Run("the cache view is decided on its base repository and the challenge names the address", func(t *testing.T) {
		path := "/v2/web/docker-hub~cache/library/alpine/manifests/3"
		g := registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: hub, Name: "library/alpine", Actions: registrytoken.Pull}}}
		if code, _, _ := m.send("GET", path, g); code != 200 {
			t.Fatalf("cache view: %d", code)
		}
		if code, ch, _ := m.send("GET", path, registrytoken.Grant{}); code != 401 || !strings.Contains(ch, `scope="repository:web/docker-hub~cache/library/alpine:pull"`) {
			t.Fatalf("%d %s", code, ch)
		}
	})
	t.Run("a path no route matches is refused", func(t *testing.T) {
		all := registrytoken.Grant{Catalog: true, Entries: []registrytoken.Entry{{Repo: local, Name: "app", Actions: registrytoken.All}}}
		if code, ch, _ := m.send("GET", exampleBase+"app/nothing/here", all); code != 401 || !strings.Contains(ch, `error="insufficient_scope"`) {
			t.Fatalf("%d %s", code, ch)
		}
	})
}

// lying matches like the dispatcher, except where it is told to lie.
type lying struct {
	inner route.Matcher
	lie   func(*http.Request) (route.Target, bool)
}

func (l lying) RegistryTarget(r *http.Request) (route.Target, bool) {
	if t, ok := l.lie(r); ok {
		return t, true
	}
	return l.inner.RegistryTarget(r)
}

// R-OCI-005, unit level: the end-to-end proof is in test/integration/registry.
func TestTheWalkCatchesAWrongMount(t *testing.T) {
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: stubRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a route of the format left out", func(t *testing.T) {
		m := newMount(t, nil, "oci.manifest.delete")
		f := &failures{TB: t}
		m.walk().Run(f)
		if !f.has("route oci.manifest.delete is not mounted") {
			t.Fatalf("%v", f.msgs)
		}
	})
	t.Run("a matcher that resolves a push to the pull route", func(t *testing.T) {
		m := newMount(t, lying{inner: disp, lie: func(r *http.Request) (route.Target, bool) {
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
				return route.Target{Route: "oci.manifest.get.tag", Repo: local, Image: "app"}, true
			}
			return route.Target{}, false
		}}, "")
		f := &failures{TB: t}
		m.walk().Run(f)
		if !f.has(`a grant of exactly push is refused`) {
			t.Fatalf("%v", f.msgs)
		}
	})
	t.Run("the version check passes only /v2/ itself, whatever a matcher says", func(t *testing.T) {
		m := newMount(t, lying{inner: disp, lie: func(r *http.Request) (route.Target, bool) {
			if r.URL.Path == "/v2/_catalog" {
				return route.Target{Route: route.NameVersion}, true
			}
			return route.Target{}, false
		}}, "")
		if code, _, _ := m.send("GET", "/v2/_catalog", registrytoken.Grant{}); code != 401 {
			t.Fatalf("a request matched to the version check off /v2/ passed: %d", code)
		}
	})
}

// R-OCI-005, unit level: the end-to-end proof is in test/integration/registry.
func TestMountRefusesWhatItCannotEnforce(t *testing.T) {
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: stubRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	version := registry.Route{Name: route.NameVersion, Kind: registry.RouteVersion, Method: "GET", Example: "/v2/"}
	repo := registry.Route{Name: "x", Kind: registry.RouteRepository, Action: authz.Read, Method: "GET", Example: "/v2/a/b/x"}
	for name, c := range map[string]struct {
		pattern string
		routes  []registry.Route
	}{
		"outside /v2/":         {"/api/x", []registry.Route{repo}},
		"no route":             {"/v2/x", nil},
		"no name":              {"/v2/x", []registry.Route{{Kind: registry.RouteRepository, Action: authz.Read, Method: "GET", Example: "/v2/x"}}},
		"no example":           {"/v2/x", []registry.Route{{Name: "x", Kind: registry.RouteRepository, Action: authz.Read}}},
		"an example elsewhere": {"/v2/x", []registry.Route{{Name: "x", Kind: registry.RouteRepository, Action: authz.Read, Method: "GET", Example: "/api/x"}}},
		"manage":               {"/v2/x", []registry.Route{{Name: "x", Kind: registry.RouteRepository, Action: authz.Manage, Method: "GET", Example: "/v2/x"}}},
		"no kind":              {"/v2/x", []registry.Route{{Name: "x", Method: "GET", Example: "/v2/x"}}},
		"one name twice":       {"/v2/x", []registry.Route{repo, repo}},
		"a core name":          {"/v2/x", []registry.Route{{Name: route.NameCatalog, Kind: registry.RouteRepository, Action: authz.Read, Method: "GET", Example: "/v2/x"}}},
		"version elsewhere":    {"/v2/x", []registry.Route{{Name: route.NameVersion, Kind: registry.RouteVersion, Method: "GET", Example: "/v2/x"}}},
		"version by POST":      {"/v2/{$}", []registry.Route{{Name: route.NameVersion, Kind: registry.RouteVersion, Method: "POST", Example: "/v2/"}}},
		"version renamed":      {"/v2/{$}", []registry.Route{{Name: "ping", Kind: registry.RouteVersion, Method: "GET", Example: "/v2/"}}},
	} {
		e, _ := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: writeError})
		if err := e.Mount(httpserver.NewClientRouter(), []string{c.pattern}, ok200, c.routes...); err == nil {
			t.Errorf("%s: mounted", name)
		}
	}
	e, _ := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: writeError})
	r := httpserver.NewClientRouter()
	if err := e.Mount(r, []string{"GET /v2/{$}"}, ok200, version); err != nil {
		t.Fatal(err)
	}
	if err := e.Mount(r, []string{"HEAD /v2/{$}"}, ok200, registry.Route{Name: route.NameVersion, Kind: registry.RouteVersion, Method: "HEAD", Example: "/v2/"}); err == nil {
		t.Error("a second version check was mounted")
	}
	if err := e.Mount(r, []string{"GET /v2/{$}"}, ok200, repo); err == nil {
		t.Error("a pattern was mounted twice")
	}
	if len(e.Patterns()) != 1 {
		t.Fatalf("patterns %v: a refused mount left a pattern", e.Patterns())
	}
	if _, err := registry.NewEnforcer(registry.EnforcerOptions{}); err == nil {
		t.Error("an enforcer without the dispatcher's match was created")
	}
}

// R-OCI-006, unit level: the end-to-end proof is in test/integration/registry.
func TestAlsoChecksTheMountsSourceActionByAction(t *testing.T) {
	m := newMount(t, nil, "")
	check := func(g registrytoken.Grant, source route.Target, a registrytoken.Actions) error {
		r := httptest.NewRequestWithContext(context.Background(), "POST", exampleBase+"app/blobs/uploads/", nil)
		hg := &httpserver.RegistryGrant{}
		for _, en := range g.Entries {
			hg.Entries = append(hg.Entries, httpserver.RegistryEntry{
				Repo: en.Repo, Name: en.Name,
				Pull: en.Actions.Has(registrytoken.Pull), Push: en.Actions.Has(registrytoken.Push), Delete: en.Actions.Has(registrytoken.Delete),
			})
		}
		p := httpserver.Principal{Kind: httpserver.KindUser, ID: alice.ID, Credential: httpserver.Credential{Registry: hg}}
		ctx := httpserver.WithResolved(r.Context(), httpserver.RequestInfo{BaseURL: "https://trove.test"})
		return m.enforcer.Also(r.WithContext(httpserver.WithPrincipal(ctx, p)), source, a)
	}
	pushOnTarget := registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: local, Name: "app", Actions: registrytoken.Push}}}
	source := route.Target{Repo: hub, Image: "lib"}
	if err := check(pushOnTarget, source, registrytoken.Pull); err == nil {
		t.Fatal("push on the target and no pull on the source passed")
	} else if e, _ := errs.As(err); e.Code != errs.CodeOCIInsufficientScope || !strings.Contains(e.Challenge, `scope="repository:web/docker-hub/lib:pull"`) {
		t.Fatalf("refusal %+v", e)
	}
	both := registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: hub, Name: "lib", Actions: registrytoken.Pull}}}
	if err := check(both, source, registrytoken.Pull); err != nil {
		t.Fatalf("pull on the source: %v", err)
	}
	if err := check(both, source, registrytoken.Pull|registrytoken.Push); err == nil {
		t.Fatal("two actions passed with one of them granted")
	}
	pullPush := registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: hub, Name: "lib", Actions: registrytoken.Pull | registrytoken.Push}}}
	if err := check(pullPush, source, registrytoken.Pull|registrytoken.Push); err != nil {
		t.Fatalf("two actions, both granted: %v", err)
	}
	if err := check(both, route.Target{Repo: qname.Repo{Project: "web", Key: "docker-hub~cache"}, Image: "lib"}, registrytoken.Pull); err != nil {
		t.Fatalf("a cache view as the source is its base repository: %v", err)
	}
	if err := check(both, source, 0); err == nil {
		t.Fatal("no action passed")
	}
}
