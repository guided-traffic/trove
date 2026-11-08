// Package registrytest holds the test helpers of the registry root: Walk proves that the enforcement point of a registry
// token's grant (registry.Enforcer, Q-OCI-56) stands, through the endpoint's whole handler, in front of every route below /v2/.
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
	"github.com/guided-traffic/trove/internal/httpserver"
)

// Walk is the end-to-end proof of the enforcement point for one endpoint. The registry root's mount test fills it from the
// running composition and calls Run.
type Walk struct {
	// Enforcer is the endpoint's enforcement point.
	Enforcer *registry.Enforcer
	// Handler is the endpoint's whole handler: the chain with the authentication stage in front of the router, so a request
	// passes the stage, the conversion of its result and the enforcer exactly as in production.
	Handler http.Handler
	// Patterns are the patterns of the endpoint's router (httpserver.Router.Patterns): every one below /v2/ must have been
	// mounted through the enforcer.
	Patterns []string
	// FormatRoutes are the names of every route of the registry-root format (formats.Route.Name): the enforcer must know each
	// of them and the core routes, and nothing else.
	FormatRoutes []string
	// Mint signs a registry token with the grant for a live trove token of the endpoint (registrytoken.Signer.Issue with the
	// trove token's binding); Walk sends it as Authorization: Bearer.
	Mint func(registrytoken.Grant) string
	// Prepare, when set, completes each request (the Host of the endpoint, a client address).
	Prepare func(*http.Request)
}

// Run checks, and reports every failure on t:
//
//   - the registered route names are exactly the format's route names and the three core routes, with exactly one version
//     check, GET or HEAD of /v2/;
//   - every router pattern below /v2/ was mounted through the enforcer, and every pattern the enforcer mounted is on the router;
//   - each route's example request is matched by the dispatcher to that very route;
//   - a real registry token with an empty grant, sent through the whole handler, is refused with 401 insufficient_scope on every
//     route but the version check (passed) and the token service (which refuses a registry token as a credential, 401);
//   - per repository route, a token with every token action but the route's on the example's image is refused with 401
//     insufficient_scope, and a token with exactly the route's action is not;
//   - the catalog passes a token with registry:catalog:*.
func (w Walk) Run(t testing.TB) {
	t.Helper()
	routes := w.Enforcer.Routes()
	w.names(t, routes)
	w.patterns(t)
	empty := w.Mint(registrytoken.Grant{})
	for _, rt := range routes {
		target, ok := w.Enforcer.Target(w.request(t, rt, ""))
		if !ok || target.Route != rt.Name {
			t.Errorf("%s %s: the dispatcher matches route %q (ok %v), not %s", rt.Method, rt.Example, target.Route, ok, rt.Name)
			continue
		}
		code, challenge := w.send(t, rt, empty)
		switch rt.Kind {
		case registry.RouteVersion:
			if code == http.StatusUnauthorized {
				t.Errorf("%s: a registry token is refused by the version check: %s", rt.Name, challenge)
			}
		case registry.RouteToken:
			if code != http.StatusUnauthorized {
				t.Errorf("%s: the token service accepted a registry token as its credential: %d", rt.Name, code)
			}
		case registry.RouteCatalog:
			w.refused(t, rt, "empty grant", code, challenge)
			if code, challenge := w.send(t, rt, w.Mint(registrytoken.Grant{Catalog: true})); insufficient(code, challenge) {
				t.Errorf("%s: registry:catalog:* is refused", rt.Name)
			}
		case registry.RouteRepository:
			w.refused(t, rt, "empty grant", code, challenge)
			w.complement(t, rt, target)
		}
	}
}

func (w Walk) names(t testing.TB, routes []registry.Route) {
	t.Helper()
	want := append([]string{route.NameVersion, route.NameCatalog, route.NameToken}, w.FormatRoutes...)
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
		if path := httpserver.PatternPath(p); (path == "/v2" || strings.HasPrefix(path, "/v2/")) && !slices.Contains(mounted, p) {
			t.Errorf("pattern %q below /v2/ was not mounted through the enforcement point", p)
		}
	}
	for _, p := range mounted {
		if !slices.Contains(w.Patterns, p) {
			t.Errorf("pattern %q of the enforcement point is not on the router", p)
		}
	}
}

// complement sends a token with every token action but the route's, then one with exactly the route's.
func (w Walk) complement(t testing.TB, rt registry.Route, target route.Target) {
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
		return registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: target.Repo, Name: target.Image, Actions: a}}}
	}
	if others := registrytoken.All &^ act; others != 0 {
		code, challenge := w.send(t, rt, w.Mint(entry(others)))
		w.refused(t, rt, "every action but "+act.String(), code, challenge)
	}
	if code, challenge := w.send(t, rt, w.Mint(entry(act))); insufficient(code, challenge) {
		t.Errorf("%s %s: a grant of exactly %s is refused by the enforcement point: %s", rt.Method, rt.Example, act, challenge)
	}
}

func (w Walk) refused(t testing.TB, rt registry.Route, grant string, code int, challenge string) {
	t.Helper()
	if !insufficient(code, challenge) {
		t.Errorf("%s %s (%s) with %s: %d, challenge %q; want 401 insufficient_scope", rt.Method, rt.Example, rt.Name, grant, code, challenge)
	}
}

func (w Walk) request(t testing.TB, rt registry.Route, token string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), rt.Method, rt.Example, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if w.Prepare != nil {
		w.Prepare(r)
	}
	return r
}

func (w Walk) send(t testing.TB, rt registry.Route, token string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	w.Handler.ServeHTTP(rec, w.request(t, rt, token))
	return rec.Code, rec.Header().Get("WWW-Authenticate")
}

func insufficient(code int, challenge string) bool {
	return code == http.StatusUnauthorized && strings.Contains(challenge, `error="insufficient_scope"`)
}
