// Package registrytest holds the test helpers of the registry root: Walk proves that the enforcement point of a registry
// token's grant (registry.Enforcer, Q-OCI-56) stands, through the endpoint's whole handler, in front of every route below /v2/,
// and that the route a request is served by is the route its match names.
package registrytest

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// BoundHost is a host of the endpoint whose root is bound to a repository of the format (R-OCI-029, R-EP-006).
type BoundHost struct {
	Host string
	Repo qname.Repo
}

// Walk is the end-to-end proof of the enforcement point for one endpoint. The registry root's mount test fills it from the
// running composition and calls Run.
//
// The walk never asks the dispatcher what a request means: it keeps its own expectation, the format's Match on the path of
// each route's example, and compares it with what the running handler does - which route served the request (Served) and
// which grants open it. The target of a request is resolved inside the chain (the bound host of a request is placed in its
// context by the chain's host stage), so a walk that called the matcher on a request that never went through the chain would
// miss the binding.
type Walk struct {
	// Enforcer is the endpoint's enforcement point.
	Enforcer *registry.Enforcer
	// Handler is the endpoint's whole handler: the chain with the authentication stage in front of the router, so a request
	// passes the host stage, the authentication stage, the conversion of its result and the enforcer exactly as in production.
	Handler http.Handler
	// Patterns are the patterns of the endpoint's router (httpserver.Router.Patterns): every one below /v2/ must have been
	// mounted through the enforcer.
	Patterns []string
	// Format is the format that declares the registry root: its routes are the routes the enforcer must know besides the
	// three core routes, and its Match is the walk's expectation of what each example reaches.
	Format formats.Format
	// Repo is the repository the examples of the format's routes address (registry.RepositoryRoutes' base,
	// "/v2/<project>/<key>/").
	Repo qname.Repo
	// Mint signs a registry token with the grant for a live trove token of the endpoint (registrytoken.Signer.Issue with the
	// trove token's binding, or a token request to the token service); Walk sends it as Authorization: Bearer.
	Mint func(registrytoken.Grant) string
	// Prepare, when set, completes each request (the Host of the endpoint, a client address).
	Prepare func(*http.Request)
	// Served runs do, which sends one request through Handler, and returns the name of the route the dispatcher served during
	// it (its Label hook, the handler label of the request log and the metrics). Walk compares it with the route the example
	// belongs to.
	Served func(do func()) string
	// Bound, when set, makes the endpoint a bound-host endpoint (R-OCI-029): every request goes to the bound host, the examples
	// of the repository routes lose their project and key, and the grants name the image alone under the bound repository.
	// Mint then mints for that host (the token service parses scopes as image names there).
	Bound *BoundHost
}

// Run checks, and reports every failure on t:
//
//   - the registered route names are exactly the format's route names and the three core routes, with exactly one version
//     check, GET or HEAD of /v2/;
//   - every router pattern below /v2/ was mounted through the enforcer, and every pattern the enforcer mounted is on the router;
//   - each route's example request is served by that very route (not by another one) and is the route the format's Match names;
//   - a real registry token with an empty grant, sent through the whole handler, is refused with 401 insufficient_scope on every
//     route but the version check (passed) and the token service (which refuses a registry token as a credential, 401);
//   - per repository route, a token with every token action but the route's on the example's image is refused with 401
//     insufficient_scope, and a token with exactly the route's action is not;
//   - the catalog passes a token with registry:catalog:*;
//
// With Bound set the same holds on a host bound to a repository, for the repository routes with their examples read as image
// paths.
func (w Walk) Run(t testing.TB) {
	t.Helper()
	if w.Format == nil || w.Served == nil || w.Mint == nil || w.Handler == nil || w.Enforcer == nil || w.Repo.IsZero() {
		t.Fatal("a walk needs the enforcer, the handler, the format, the repository of the examples, a minter and the served-route probe")
	}
	routes := w.Enforcer.Routes()
	w.names(t, routes)
	w.patterns(t)
	empty := w.Mint(registrytoken.Grant{})
	for _, rt := range routes {
		if rt.Kind == registry.RouteVersion {
			// The root without its slash is the version check too, not a redirect.
			host := ""
			if w.Bound != nil {
				host = w.Bound.Host
			}
			w.route(t, rt, "/v2", host, w.Repo, empty, w.Bound != nil)
		}
		if w.Bound == nil {
			w.route(t, rt, rt.Example, "", w.Repo, empty, false)
			continue
		}
		if rt.Kind != registry.RouteRepository {
			w.route(t, rt, rt.Example, w.Bound.Host, w.Bound.Repo, empty, true)
			continue
		}
		prefix := "/v2/" + w.Repo.Project + "/" + w.Repo.Key + "/"
		rest, ok := strings.CutPrefix(rt.Example, prefix)
		if !ok {
			t.Errorf("%s: the example %s does not address %s/%s", rt.Name, rt.Example, w.Repo.Project, w.Repo.Key)
			continue
		}
		w.route(t, rt, "/v2/"+rest, w.Bound.Host, w.Bound.Repo, empty, true)
	}
}

// route walks one route at one example and host: example is the escaped path, base the repository its image name is under.
func (w Walk) route(t testing.TB, rt registry.Route, example, host string, base qname.Repo, empty string, bound bool) {
	t.Helper()
	at := rt.Method + " " + example
	if host != "" {
		at += " on " + host
	}
	image := ""
	if rt.Kind == registry.RouteRepository {
		var ok bool
		if image, ok = w.expected(t, rt, example, base, bound); !ok {
			return
		}
	}
	code, challenge := w.send(t, rt, example, host, empty)
	switch rt.Kind {
	case registry.RouteVersion:
		if code == http.StatusUnauthorized {
			t.Errorf("%s: a registry token is refused by the version check: %s", at, challenge)
		}
		w.served(t, rt, at, example, host, w.Mint(registrytoken.Grant{}))
	case registry.RouteToken:
		if code != http.StatusUnauthorized {
			t.Errorf("%s: the token service accepted a registry token as its credential: %d", at, code)
		}
		w.served(t, rt, at, example, host, "")
	case registry.RouteCatalog:
		w.refused(t, at, "empty grant", code, challenge)
		if code, challenge := w.send(t, rt, example, host, w.Mint(registrytoken.Grant{Catalog: true})); insufficient(code, challenge) {
			t.Errorf("%s: registry:catalog:* is refused", at)
		}
		w.served(t, rt, at, example, host, w.Mint(registrytoken.Grant{Catalog: true}))
	case registry.RouteRepository:
		w.refused(t, at, "empty grant", code, challenge)
		w.complement(t, rt, at, example, host, base, image)
	}
}

// expected is the walk's own reading of an example: the format's Match on the path after the repository segment (the whole
// path after /v2/ on a bound host) must name the route, and gives the image name the grants are minted for.
func (w Walk) expected(t testing.TB, rt registry.Route, example string, base qname.Repo, bound bool) (image string, ok bool) {
	t.Helper()
	rest := strings.TrimPrefix(example, "/v2/")
	if !bound {
		rest = strings.TrimPrefix(rest, base.Project+"/"+base.Key+"/")
	}
	got, params, err := w.Format.Match(rt.Method, rest)
	if err != nil || got.Name != rt.Name {
		name := ""
		if got != nil {
			name = got.Name
		}
		t.Errorf("%s %s: the format's Match of %q names route %q (%v), not %s", rt.Method, example, rest, name, err, rt.Name)
		return "", false
	}
	if got.PackageParam == "" || params[got.PackageParam] == "" {
		t.Errorf("%s %s: route %s names no image in its example", rt.Method, example, rt.Name)
		return "", false
	}
	return params[got.PackageParam], true
}

// served checks that the route a request is served by is the route of its example: a request with a token that passes the
// enforcement point reaches the dispatcher, which names its route when it matched.
func (w Walk) served(t testing.TB, rt registry.Route, at, example, host, token string) {
	t.Helper()
	got := w.Served(func() { w.send(t, rt, example, host, token) })
	if got != rt.Name {
		t.Errorf("%s: the request was served by route %q, not %s", at, got, rt.Name)
	}
}

func (w Walk) names(t testing.TB, routes []registry.Route) {
	t.Helper()
	want := []string{route.NameVersion, route.NameCatalog, route.NameToken}
	for _, r := range w.Format.Routes() {
		want = append(want, r.Name)
	}
	got := make([]string, 0, len(routes))
	versions := 0
	for _, rt := range routes {
		got = append(got, rt.Name)
		if rt.Kind == registry.RouteVersion {
			versions++
			if rt.Example != "/v2/" || (rt.Method != http.MethodGet && rt.Method != http.MethodHead) {
				t.Errorf("the version check is %s %s, not GET or HEAD of /v2/", rt.Method, rt.Example)
			}
		}
	}
	for _, n := range want {
		if !slices.Contains(got, n) {
			t.Errorf("route %s is not mounted through the enforcement point", n)
		}
	}
	for _, n := range got {
		if !slices.Contains(want, n) {
			t.Errorf("the enforcement point knows route %s, which is neither the format's nor a core route", n)
		}
	}
	if versions != 1 {
		t.Errorf("%d version checks are registered, want exactly one", versions)
	}
}

func (w Walk) patterns(t testing.TB) {
	t.Helper()
	mounted := w.Enforcer.Patterns()
	if len(mounted) == 0 {
		t.Error("nothing is mounted through the enforcement point")
	}
	for _, p := range w.Patterns {
		if path := httpserver.LiteralPatternPath(p); (path == "/v2" || strings.HasPrefix(path, "/v2/")) && !slices.Contains(mounted, p) {
			t.Errorf("pattern %q below /v2/ was not mounted through the enforcement point", p)
		}
	}
	for _, p := range mounted {
		if !slices.Contains(w.Patterns, p) {
			t.Errorf("pattern %q of the enforcement point is not on the router", p)
		}
	}
}

// complement sends a token with every token action but the route's, then one with exactly the route's, and checks the route
// that serves the second.
func (w Walk) complement(t testing.TB, rt registry.Route, at, example, host string, base qname.Repo, image string) {
	t.Helper()
	act, ok := registry.ActionOf(rt.Action)
	if !ok {
		t.Errorf("%s: action %q has no token action", rt.Name, rt.Action)
		return
	}
	entry := func(a registrytoken.Actions) registrytoken.Grant {
		if a == 0 {
			return registrytoken.Grant{}
		}
		return registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: base, Name: image, Actions: a}}}
	}
	if others := registrytoken.All &^ act; others != 0 {
		code, challenge := w.send(t, rt, example, host, w.Mint(entry(others)))
		w.refused(t, at, "every action but "+act.String(), code, challenge)
	}
	exact := w.Mint(entry(act))
	if code, challenge := w.send(t, rt, example, host, exact); insufficient(code, challenge) {
		t.Errorf("%s: a grant of exactly %s is refused by the enforcement point: %s", at, act, challenge)
	}
	w.served(t, rt, at, example, host, exact)
}

func (w Walk) refused(t testing.TB, at, grant string, code int, challenge string) {
	t.Helper()
	if !insufficient(code, challenge) {
		t.Errorf("%s with %s: %d, challenge %q; want 401 insufficient_scope", at, grant, code, challenge)
	}
}

func (w Walk) request(t testing.TB, rt registry.Route, example, host, token string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), rt.Method, example, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if w.Prepare != nil {
		w.Prepare(r)
	}
	if host != "" {
		r.Host = host
	}
	return r
}

func (w Walk) send(t testing.TB, rt registry.Route, example, host, token string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	w.Handler.ServeHTTP(rec, w.request(t, rt, example, host, token))
	return rec.Code, rec.Header().Get("WWW-Authenticate")
}

func insufficient(code int, challenge string) bool {
	return code == http.StatusUnauthorized && strings.Contains(challenge, `error="insufficient_scope"`)
}
