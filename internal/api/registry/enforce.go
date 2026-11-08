package registry

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// The enforcement point of a registry token's grant below /v2/ (R-OCI-005 "Use", Q-OCI-56). The grant is a ceiling that the
// decision point does not see, so one place must refuse a registry token whatever the route. [Enforcer.Mount] is the only way
// to put a handler below /v2/: it registers the routes the handler serves and mounts the handler behind the check in one call,
// and the route table of the endpoint (httpserver.CheckRouteTable with [Enforcer.Patterns] as its exemptions, R-ARCH-005)
// refuses every /v2/ pattern that was not mounted through it. Which route a request reaches is the dispatcher's own match
// (route.Matcher). A registry-token request on a route the enforcer does not know, whose address the match cannot read, or whose
// action has no token action is refused with 401 error="insufficient_scope", so a route that is not registered fails closed
// instead of handing the registry token the full power of its trove token. registrytest.Walk proves the wiring end to end.

// RouteKind says what a route below /v2/ needs of a registry token's grant.
type RouteKind int

// The kinds of routes.
const (
	// RouteRepository needs the token action of its coarse-check action ([ActionOf]) on the addressed image name of the
	// addressed repository.
	RouteRepository RouteKind = iota + 1
	// RouteCatalog needs registry:catalog:* (R-OCI-017).
	RouteCatalog
	// RouteVersion needs nothing beyond a valid credential (R-OCI-003): any registry token passes, the anonymous one included.
	// There is exactly one, GET and HEAD of /v2/, and it passes only a request whose escaped path is /v2/ or /v2.
	RouteVersion
	// RouteToken is the token service (R-OCI-004). The chain does not authenticate its route, so no registry token arrives
	// there as a credential; one that does is refused.
	RouteToken
)

// Route is one route below /v2/ as the enforcement point knows it.
type Route struct {
	// Name is the stable name the dispatcher's match reports for a request of the route: a formats.Route.Name for a
	// repository route ("oci.manifest.get.tag"), [route.NameVersion], [route.NameCatalog] or [route.NameToken] for a core route.
	Name string
	Kind RouteKind
	// Action is the coarse-check action of a repository route (formats.Route.Action); a route whose action has no token
	// action is refused.
	Action authz.Action
	// Method and Example are a request that reaches the route (an escaped path from the host's root), for registrytest.Walk.
	Method  string
	Example string
}

// EnforcerOptions configure [NewEnforcer].
type EnforcerOptions struct {
	// Matcher is the dispatcher (dispatch.Dispatcher.RegistryTarget). It is asked for registry-token requests only.
	Matcher route.Matcher
	// WriteError renders a refusal in the format's shape (R-OCI-003).
	WriteError func(http.ResponseWriter, *http.Request, error)
	// Realm names the realm and the service of the challenge; nil reads the request's base URL (registrytoken.Realm).
	Realm func(*http.Request) (realm, service string)
}

// Enforcer is the enforcement point. Routes are mounted at start-up; it is safe for concurrent use.
type Enforcer struct {
	o        EnforcerOptions
	mu       sync.RWMutex
	routes   map[string]Route
	patterns []string
}

// NewEnforcer creates the enforcement point.
func NewEnforcer(o EnforcerOptions) (*Enforcer, error) {
	if o.Matcher == nil || o.WriteError == nil {
		return nil, errors.New("registry: the enforcement point needs the dispatcher's match and an error writer")
	}
	if o.Realm == nil {
		o.Realm = func(r *http.Request) (string, string) {
			return registrytoken.Realm(httpserver.InfoFrom(r.Context()).BaseURL)
		}
	}
	return &Enforcer{o: o, routes: map[string]Route{}}, nil
}

// Mount puts h on the router under pattern, behind the enforcement point, and registers the routes h serves, in one call:
// the only way to mount a handler below /v2/ (Q-OCI-56). It refuses, before anything is mounted, a pattern outside /v2/, a
// pattern mounted before, no route, and every route [Enforcer] cannot enforce: a name registered before, an unknown kind, a
// repository route whose action has no token action, a route without its example request, a second version check, a version
// check other than GET or HEAD of /v2/, and a core route under another name than its own. A start-up whose mount is refused
// does not serve.
func (e *Enforcer) Mount(router *httpserver.Router, pattern string, h http.Handler, routes ...Route) error {
	if !belowV2(httpserver.PatternPath(pattern)) {
		return fmt.Errorf("registry: pattern %q lies outside /v2/", pattern)
	}
	if len(routes) == 0 {
		return fmt.Errorf("registry: pattern %q mounts no route", pattern)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if slices.Contains(e.patterns, pattern) {
		return fmt.Errorf("registry: pattern %q is mounted twice", pattern)
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		if err := e.admissible(rt); err != nil {
			return err
		}
		if seen[rt.Name] {
			return fmt.Errorf("registry: route %s is registered twice", rt.Name)
		}
		seen[rt.Name] = true
	}
	for _, rt := range routes {
		e.routes[rt.Name] = rt
	}
	e.patterns = append(e.patterns, pattern)
	router.Handle(pattern, e.wrap(h))
	return nil
}

// admissible checks one route against the rules of Mount; e.mu is held.
func (e *Enforcer) admissible(rt Route) error {
	switch {
	case rt.Name == "":
		return errors.New("registry: a route of the enforcement point needs a name")
	case rt.Method == "" || rt.Example == "":
		return fmt.Errorf("registry: route %s needs a method and an example request", rt.Name)
	case !belowV2(rt.Example):
		return fmt.Errorf("registry: the example of route %s lies outside /v2/", rt.Name)
	}
	if _, dup := e.routes[rt.Name]; dup {
		return fmt.Errorf("registry: route %s is registered twice", rt.Name)
	}
	core := map[RouteKind]string{RouteVersion: route.NameVersion, RouteCatalog: route.NameCatalog, RouteToken: route.NameToken}
	switch rt.Kind {
	case RouteRepository:
		if _, ok := ActionOf(rt.Action); !ok {
			return fmt.Errorf("registry: route %s needs %q, which no registry token can grant", rt.Name, rt.Action)
		}
		if slices.Contains([]string{route.NameVersion, route.NameCatalog, route.NameToken}, rt.Name) {
			return fmt.Errorf("registry: %s is the name of a core route", rt.Name)
		}
		return nil
	case RouteVersion:
		if rt.Example != "/v2/" || (rt.Method != http.MethodGet && rt.Method != http.MethodHead) {
			return fmt.Errorf("registry: the version check is GET or HEAD of /v2/, not %s %s", rt.Method, rt.Example)
		}
	case RouteCatalog, RouteToken:
	default:
		return fmt.Errorf("registry: route %s has no kind", rt.Name)
	}
	if rt.Name != core[rt.Kind] {
		return fmt.Errorf("registry: the core route of kind %d is named %s, not %s", rt.Kind, core[rt.Kind], rt.Name)
	}
	return nil
}

// Routes returns the registered routes, sorted by name.
func (e *Enforcer) Routes() []Route {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Route, 0, len(e.routes))
	for _, rt := range e.routes {
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Patterns returns the patterns mounted through the enforcer, in mount order: the only /v2/ patterns the route table admits
// (httpserver.CheckRouteTable). A nil enforcer, the state before the registry root mounts /v2/, has none, so the route table
// admits no /v2/ pattern at all.
func (e *Enforcer) Patterns() []string {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return slices.Clone(e.patterns)
}

// Target is the dispatcher's match of the request, as the enforcer reads it.
func (e *Enforcer) Target(r *http.Request) (route.Target, bool) { return e.o.Matcher.RegistryTarget(r) }

// wrap puts the check in front of h.
func (e *Enforcer) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := e.Check(r); err != nil {
			e.o.WriteError(w, r, err)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Check decides a request: nil for a request that carries no registry token (a trove token presented directly, the
// anonymous request, R-OCI-004 "Direct credentials": the decision point alone decides those), and for a registry token
// whose grant holds what the matched route needs; otherwise the 401 with error="insufficient_scope".
func (e *Enforcer) Check(r *http.Request) error {
	g := httpserver.PrincipalFrom(r.Context()).Credential.Registry
	if g == nil {
		return nil
	}
	t, ok := e.o.Matcher.RegistryTarget(r)
	if !ok {
		return e.insufficient(r, "")
	}
	return e.decide(r, g, t)
}

// Also checks a further coordinate a route needs besides its own target: the source of a cross-repository mount needs pull
// on the source name (R-OCI-006). Each action of a is checked on its own; a repository key with the cache suffix is the
// base repository's, as the dispatcher's match maps it. nil for a request without a registry token.
func (e *Enforcer) Also(r *http.Request, t route.Target, a registrytoken.Actions) error {
	g := httpserver.PrincipalFrom(r.Context()).Credential.Registry
	if g == nil {
		return nil
	}
	if key, cache := strings.CutSuffix(t.Repo.Key, qname.CacheSuffix); cache {
		t.Repo.Key, t.CacheView = key, true
	}
	if t.Repo.IsZero() || t.Image == "" || a == 0 || a&^registrytoken.All != 0 {
		return e.insufficient(r, "")
	}
	for _, bit := range []registrytoken.Actions{registrytoken.Pull, registrytoken.Push, registrytoken.Delete} {
		if a&bit != 0 && !g.Allows(t.Repo, t.Image, bit.String()) {
			return e.insufficient(r, registrytoken.RepositoryScope(scopeName(t), a))
		}
	}
	return nil
}

func (e *Enforcer) decide(r *http.Request, g *httpserver.RegistryGrant, t route.Target) error {
	e.mu.RLock()
	rt, ok := e.routes[t.Route]
	e.mu.RUnlock()
	if !ok {
		return e.insufficient(r, "")
	}
	switch rt.Kind {
	case RouteVersion:
		if p := r.URL.EscapedPath(); p == "/v2/" || p == "/v2" {
			return nil
		}
	case RouteCatalog:
		if g.Catalog {
			return nil
		}
		return e.insufficient(r, "registry:catalog:*")
	case RouteRepository:
		act, ok := ActionOf(rt.Action)
		if !ok || t.Repo.IsZero() || t.Image == "" {
			return e.insufficient(r, "")
		}
		if g.Allows(t.Repo, t.Image, act.String()) {
			return nil
		}
		return e.insufficient(r, registrytoken.RepositoryScope(scopeName(t), act))
	}
	return e.insufficient(r, "")
}

// scopeName is the name the client addressed: the image alone on a bound host, the full name with the cache suffix for a
// cache view.
func scopeName(t route.Target) string {
	if t.Bound {
		return t.Image
	}
	key := t.Repo.Key
	if t.CacheView {
		key += qname.CacheSuffix
	}
	return t.Repo.Project + "/" + key + "/" + t.Image
}

// insufficient is the 401 of R-OCI-004 for a registry token whose grant lacks what the request needs: the challenge names
// the scope to ask for, so the client fetches a new token. Its code is its own, so logs and metrics tell it from a rejected
// credential.
func (e *Enforcer) insufficient(r *http.Request, scope string) error {
	realm, service := e.o.Realm(r)
	return errs.E(errs.Unauthenticated, errs.CodeOCIInsufficientScope, "The registry token does not grant this operation.",
		errs.WithChallenge(registrytoken.Challenge(realm, service, scope, registrytoken.ErrorInsufficientScope)))
}

// RepositoryRoutes are the enforcer's routes of a format's routes (formats.Route): each with its name, its coarse-check action
// and its method, and its first example below base ("/v2/<projectKey>/<repoKey>/", the repository the walk addresses), so the
// registry root mounts every route of the format and none is left out by hand.
func RepositoryRoutes(routes []formats.Route, base string) []Route {
	out := make([]Route, 0, len(routes))
	for _, rt := range routes {
		ex := ""
		if len(rt.Examples) > 0 {
			ex = strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(rt.Examples[0], "/")
		}
		out = append(out, Route{Name: rt.Name, Kind: RouteRepository, Action: rt.Action, Method: rt.Method, Example: ex})
	}
	return out
}

// belowV2 reports whether a path is /v2 or lies below /v2/.
func belowV2(p string) bool { return p == "/v2" || strings.HasPrefix(p, "/v2/") }
