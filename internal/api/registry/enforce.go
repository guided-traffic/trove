package registry

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
	// ValidName is the image-name grammar of the format: a challenge names a scope only for an image name that holds it, so the
	// challenge never echoes what a client sent as a name (R-OCI-002). Nil names every image.
	ValidName func(string) bool
	// MountRoute is the name of the route that starts an upload and may mount a blob from another repository
	// (oci.upload.start); MountSource parses the `from` of such a request into the source repository and image name, as the
	// format reads it (the composition root passes its copy ociMountSource until the format's oci.MountSource is on the branch). Both set, a registry token's request of that route with `mount` and `from` is checked
	// for pull on the source (R-OCI-006, Q-OCI-102). Both empty: no mount is enforced.
	MountRoute string
	// Log records a stripped mount at debug level; nil is slog.Default.
	Log         *slog.Logger
	MountSource func(from string, bound qname.Repo) (repo qname.Repo, image string, cache, ok bool)
}

// Enforcer is the enforcement point. Routes are mounted at start-up; it is safe for concurrent use.
type Enforcer struct {
	o         EnforcerOptions
	mu        sync.RWMutex
	routes    map[string]Route
	patterns  []string
	uncleaned []string
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

// Mount puts h on the router under each of the patterns, behind the enforcement point, and registers the routes h serves, in one
// call: the only way to mount a handler below /v2/ (Q-OCI-56). A handler that serves a path with and without its slash takes both
// patterns. It refuses, before anything is mounted, no pattern, a pattern outside /v2/ (the literal segments are unescaped
// first, as ServeMux compares them), a pattern mounted before or twice, no route, and every route [Enforcer] cannot enforce: a name registered before, an unknown kind, a
// repository route whose action has no token action, a route without its example request, a second version check, a version
// check other than GET or HEAD of /v2/, and a core route under another name than its own. A start-up whose mount is refused
// does not serve.
func (e *Enforcer) Mount(router *httpserver.Router, patterns []string, h http.Handler, routes ...Route) error {
	if len(patterns) == 0 {
		return errors.New("registry: Mount needs a pattern")
	}
	if len(routes) == 0 {
		return fmt.Errorf("registry: patterns %q mount no route", patterns)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, pattern := range patterns {
		if !belowV2(httpserver.LiteralPatternPath(pattern)) {
			return fmt.Errorf("registry: pattern %q lies outside /v2/", pattern)
		}
		if slices.Contains(e.patterns, pattern) || slices.Contains(patterns[:i], pattern) {
			return fmt.Errorf("registry: pattern %q is mounted twice", pattern)
		}
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
	wrapped := e.wrap(h)
	for _, pattern := range patterns {
		e.patterns = append(e.patterns, pattern)
		router.Handle(pattern, wrapped)
		// A subtree pattern also takes the paths ServeMux would redirect (an empty or a dot segment): they reach the handler,
		// which answers them in the registry's shape, and not the router's 301 without the registry's headers. The router
		// refuses to register the prefix any other way (httpserver.Router.HandleUncleaned), so these are the only uncleaned
		// prefixes below /v2/.
		if prefix, ok := subtreePrefix(httpserver.PatternPath(pattern)); ok && !slices.Contains(e.uncleaned, prefix) {
			e.uncleaned = append(e.uncleaned, prefix)
			router.HandleRegistryUncleaned(prefix, wrapped)
		}
	}
	return nil
}

func (e *Enforcer) log() *slog.Logger {
	if e.o.Log != nil {
		return e.o.Log
	}
	return slog.Default()
}

// match is the dispatcher's match of the request, with the reason when it reaches no route if the matcher gives one.
func (e *Enforcer) match(r *http.Request) (route.Target, error) {
	if em, ok := e.o.Matcher.(route.ErrMatcher); ok {
		return em.RegistryMatch(r)
	}
	t, ok := e.o.Matcher.RegistryTarget(r)
	if !ok {
		return route.Target{}, errors.New("registry: the request reaches no route")
	}
	return t, nil
}

// scopeOf renders the scope entry for an image, "" for a name that is no image name of the format.
func (e *Enforcer) scopeOf(t route.Target, a registrytoken.Actions) string {
	if e.o.ValidName != nil && !e.o.ValidName(t.Image) {
		return ""
	}
	return registrytoken.RepositoryScope(scopeName(t), a)
}

// subtreePrefix returns the literal prefix of a subtree pattern (a path ending in "/" or in a "{name...}" wildcard).
func subtreePrefix(path string) (string, bool) {
	if i := strings.Index(path, "{"); i >= 0 {
		if !strings.HasSuffix(path, "...}") || strings.Contains(path[i:len(path)-4], "/") {
			return "", false
		}
		return path[:i], true
	}
	return path, strings.HasSuffix(path, "/")
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

// UncleanedPrefixes returns the prefixes the enforcer registered for the paths ServeMux would redirect, the only uncleaned
// prefixes the route table admits at or below /v2/ (httpserver.CheckRouter). A nil enforcer has none.
func (e *Enforcer) UncleanedPrefixes() []string {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return slices.Clone(e.uncleaned)
}

// Scope is the scope entry of the challenge of a request below /v2/ (R-OCI-004 step 1): the token action its route needs on
// the image the request addresses, in the name the client used, "registry:catalog:*" for the catalog and "" for a route that
// names no image or is not mounted. It is the dispatcher's match and the mounted route's action, nothing else, and is the
// stage's Options.RegistryScope. A nil enforcer yields none.
func (e *Enforcer) Scope(r *http.Request) string {
	if e == nil {
		return ""
	}
	t, err := e.match(r)
	if err != nil {
		return ""
	}
	e.mu.RLock()
	rt, known := e.routes[t.Route]
	e.mu.RUnlock()
	if !known {
		return ""
	}
	switch rt.Kind {
	case RouteCatalog:
		return "registry:catalog:*"
	case RouteRepository:
		if act, ok := ActionOf(rt.Action); ok && !t.Repo.IsZero() && t.Image != "" {
			return e.scopeOf(t, act)
		}
	}
	return ""
}

// wrap puts the check in front of h. The request carries a memo of its match from here on, so the handler behind the check
// serves the match the check decided on and reads the request, the host and the binding once.
func (e *Enforcer) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = route.WithMemo(r)
		if err := e.Check(r); err != nil {
			e.o.WriteError(w, r, err)
			return
		}
		r = e.withoutUnreadableMount(r)
		h.ServeHTTP(&challengeWriter{ResponseWriter: w, e: e, r: r}, r)
	})
}

// withoutUnreadableMount applies the pull check on the source of a cross-repository blob mount (R-OCI-006). A format cannot ask the
// registry token's grant (it may not import this package), so the enforcement point does: when a registry token starts an upload
// with `mount` and `from` and its grant lacks pull on the source, the two parameters are dropped from the request, and the handler
// opens an ordinary session (202), which is the answer it gives for a source that does not exist or that the caller may not read
// (R-OCI-012). The refusal is therefore not an answer of its own and tells nothing about whether the source exists (Q-OCI-102).
func (e *Enforcer) withoutUnreadableMount(r *http.Request) *http.Request {
	g := httpserver.PrincipalFrom(r.Context()).Credential.Registry
	if g == nil || e.o.MountRoute == "" || e.o.MountSource == nil || r.Method != http.MethodPost {
		return r
	}
	q := r.URL.Query()
	if !q.Has("mount") || !q.Has("from") {
		return r
	}
	q0 := r.URL.Query()
	t, err := e.match(r)
	if err != nil || t.Route != e.o.MountRoute {
		return r
	}
	var bound qname.Repo
	if t.Bound {
		bound = t.Repo
	}
	repo, image, cache, ok := e.o.MountSource(q.Get("from"), bound)
	if !ok {
		return r // the handler treats a `from` that names no image as no mount
	}
	if e.Also(r, route.Target{Repo: repo, Image: image, CacheView: cache, Bound: t.Bound}, registrytoken.Pull) == nil {
		return r
	}
	q.Del("mount")
	q.Del("from")
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	// Clone copies a form already parsed from the old query: the handler reads the stripped values, and a form marked as parsed
	// is never parsed again, so a later FormValue does not read the request body.
	r2.Form, r2.PostForm = q, url.Values{}
	// A stripped mount leaves a trace besides the access log: which trove token asked for which source.
	e.log().Debug("registry token mount stripped: no pull on the source", "token_id", httpserver.PrincipalFrom(r.Context()).Credential.TokenID,
		"source", q0.Get("from"), "target", t.Repo.String()+"/"+t.Image)
	return r2
}

// Check decides a request: nil for a request that carries no registry token (a trove token presented directly, the
// anonymous request, R-OCI-004 "Direct credentials": the decision point alone decides those), and for a registry token
// whose grant holds what the matched route needs; otherwise the 401 with error="insufficient_scope".
func (e *Enforcer) Check(r *http.Request) error {
	g := httpserver.PrincipalFrom(r.Context()).Credential.Registry
	if g == nil {
		return nil
	}
	t, err := e.match(r)
	if err != nil {
		// A failed lookup is the service's trouble and not the grant's: the client is not sent to fetch another token. That
		// the answer differs from a missing scope tells a registry-token holder only that the lookup failed.
		if ee, ok := errs.As(err); ok && ee.Kind == errs.Unavailable {
			return err
		}
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
			return e.insufficient(r, e.scopeOf(t, a))
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
		return e.insufficient(r, e.scopeOf(t, act))
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
