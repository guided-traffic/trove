// Package httpserver is the HTTP shell of a replica: the two listeners, the
// ServeMux behind the client listener, the middleware chain in the order
// R-ARCH-005 fixes, the client address of R-ARCH-006, the in-flight limit, the
// graceful shutdown of R-ARCH-024 and the reservation of /v2/ (R-HA-041).
//
// It owns no business routes. The composition root registers them on a Router;
// the generated management API, the UI and (later) the format dispatcher all go
// through Router.Handle, so the route table is complete and testable.
package httpserver

import (
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// Patterns the router registers itself. CheckRouteTable exempts exactly these
// from its rules.
const (
	// CatchAllPattern answers 404 (or 405) for anything no route claims.
	CatchAllPattern = "/"
	// RootPattern redirects the bare host to the UI.
	RootPattern = "GET /{$}"
)

// Router is the one wrapper through which every route of a listener is
// registered (R-ARCH-005). It records the patterns, so a test can read the whole
// route table, and it answers unmatched requests with problem+json.
//
// It satisfies the ServeMux interface of the generated std-http-server code.
type Router struct {
	mux *http.ServeMux
	// allow, when set, decides which patterns the router registers: a pattern of
	// a route family the endpoint does not serve is dropped (R-EP-005).
	allow func(pattern string) bool
	// servesPath, when set, tells the fallback whether a request path belongs to a
	// family the endpoint serves: for one that does not, the answer is the plain
	// 404, never a 405 that would show a route of another family exists (R-EP-005).
	servesPath func(path string) bool

	mu       sync.Mutex
	patterns []string
	dropped  []string

	// uncleaned holds the prefixes whose handler also takes the paths ServeMux
	// would redirect (HandleUncleaned). It is replaced as a whole, so the request
	// path reads it without a lock.
	uncleaned atomic.Pointer[[]uncleanedRoute]
	// shapes are the error shapes of path prefixes (HandleErrorShape).
	shapes shapeTable
	// hidden holds the predicates of HideMethods; replaced as a whole, so the
	// request path reads it without a lock.
	hidden atomic.Pointer[[]hiddenRoute]
	// custom replaces the answer to a request no route claims (SetFallback).
	custom atomic.Pointer[http.Handler]
}

// ShapeFunc renders an error in the body and with the headers a protocol's
// clients parse. It writes the whole response.
type ShapeFunc func(w http.ResponseWriter, r *http.Request, err error)

type shapeRoute struct {
	prefix string
	fn     ShapeFunc
}

// shapeTable is the set of error shapes of a router; it is replaced as a whole, so
// the request path reads it without a lock.
type shapeTable struct{ routes atomic.Pointer[[]shapeRoute] }

func (t *shapeTable) forPath(p string) ShapeFunc {
	rs := t.routes.Load()
	if rs == nil {
		return nil
	}
	for _, r := range *rs {
		if strings.HasPrefix(p, r.prefix) {
			return r.fn
		}
	}
	return nil
}

// HandleErrorShape makes fn render every error of a request whose path lies below
// prefix: the handler's, and the refusals of the chain's own stages -
// authentication (401 and its challenge), the host check (421), the in-flight limit
// (503) and the rate limits (429) - which the chain renders through [Problem]. A
// package format registers its protocol's error shape for /api/<id>/ here
// (R-ARCH-023; R-NPM-005 asks for npm's body and challenge on every error
// response). The prefix belongs to a route family and is dropped on an endpoint that
// does not serve it, like a pattern.
func (r *Router) HandleErrorShape(prefix string, fn ShapeFunc) {
	if r.allow != nil && !r.allow(prefix) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var cur []shapeRoute
	if p := r.shapes.routes.Load(); p != nil {
		cur = *p
	}
	next := append(slices.Clone(cur), shapeRoute{prefix: prefix, fn: fn})
	r.shapes.routes.Store(&next)
}

type uncleanedRoute struct {
	prefix string
	h      http.Handler
}

type hiddenRoute struct {
	prefix string
	hidden func(*http.Request) bool
}

// HideMethods makes the fallback answer the 404 of an unknown path, never a 405 with Allow, for a request below prefix
// that hidden reports. A route that answers some callers like an unknown path registers here, so that the 405 of a
// method it does not offer does not tell those callers that the path exists: the UI's content routes answer every
// request the session cookie did not authenticate so (R-API-025, Q-UI-106, Q-ARCH-307). hidden runs in the fallback,
// after the authentication stage, so it may read the principal. The prefix belongs to a route family and is dropped on an
// endpoint that does not serve it, like a pattern; there the fallback answers the plain 404 anyway (R-EP-005).
func (r *Router) HideMethods(prefix string, hidden func(*http.Request) bool) {
	if r.allow != nil && !r.allow(prefix) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var cur []hiddenRoute
	if p := r.hidden.Load(); p != nil {
		cur = *p
	}
	next := append(slices.Clone(cur), hiddenRoute{prefix: prefix, hidden: hidden})
	r.hidden.Store(&next)
}

// methodsHidden reports whether a predicate of HideMethods hides the methods of the request's path from its caller.
func (r *Router) methodsHidden(req *http.Request) bool {
	routes := r.hidden.Load()
	if routes == nil {
		return false
	}
	for _, rt := range *routes {
		if strings.HasPrefix(req.URL.Path, rt.prefix) && rt.hidden(req) {
			return true
		}
	}
	return false
}

// NewClientRouter returns the router of the client listener with its two fixed
// rows: GET /{$} redirects to /ui/ (302) and everything else is a 404 problem
// (405 with Allow where the path exists for another method).
func NewClientRouter() *Router {
	r := newRouter()
	r.Handle(RootPattern, http.RedirectHandler("/ui/", http.StatusFound))
	return r
}

// NewEndpointRouter returns the router of one endpoint's listener: it registers
// only the routes of the endpoint's surface (R-EP-005), the fixed GET /{$} row
// only when the surface holds ui. A pattern of another family is dropped, so a
// path of it falls through to the catch-all 404 and is indistinguishable from an
// unknown path; Dropped lists what was left out.
func NewEndpointRouter(e *Endpoint) *Router {
	r := newRouter()
	r.allow = func(pattern string) bool { return e.servesPattern(pattern) }
	r.servesPath = e.servesPath
	if e.Serves(config.SurfaceUI) {
		r.Handle(RootPattern, http.RedirectHandler("/ui/", http.StatusFound))
	}
	return r
}

// NewOpsRouter returns the router of the ops listener. It has only the catch-all.
func NewOpsRouter() *Router { return newRouter() }

func newRouter() *Router {
	r := &Router{mux: http.NewServeMux()}
	r.Handle(CatchAllPattern, http.HandlerFunc(r.fallback))
	return r
}

// Handle registers a route. A conflicting pattern panics, as ServeMux does, so a
// collision cannot ship.
func (r *Router) Handle(pattern string, h http.Handler) {
	if r.allow != nil && !r.allow(pattern) {
		r.mu.Lock()
		r.dropped = append(r.dropped, pattern)
		r.mu.Unlock()
		return
	}
	r.mux.Handle(pattern, h)
	r.mu.Lock()
	r.patterns = append(r.patterns, pattern)
	r.mu.Unlock()
}

// HandleFunc is Handle for a function.
func (r *Router) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	r.Handle(pattern, http.HandlerFunc(f))
}

// Patterns returns every registered pattern, the router's own rows included, in
// registration order.
func (r *Router) Patterns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.patterns)
}

// Dropped returns the patterns an endpoint router left out because their route
// family is not in the endpoint's surface.
func (r *Router) Dropped() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.dropped)
}

// HandleUncleaned makes h the handler of every request below prefix whose path
// ServeMux would redirect to a cleaned path: an empty segment ("a//b") or a "."
// or ".." segment. ServeMux answers those with a 301 before any pattern is
// tried; a handler whose specification answers them itself (/files/ answers 400
// invalid_path, R-GENERIC-002) registers here as well as with Handle. The prefix
// belongs to a route family and is dropped on an endpoint that does not serve
// it, like a pattern. It is not a pattern of the route table, so it refuses what
// the route table protects: a prefix at or below /v2 panics, as a conflicting
// pattern does. Below /v2/ only the registry's enforcement point serves the
// uncleaned paths ([Router.HandleRegistryUncleaned], R-HA-041, Q-OCI-101).
func (r *Router) HandleUncleaned(prefix string, h http.Handler) {
	if belowRegistry(prefix) {
		panic("httpserver: HandleUncleaned(" + prefix + ") lies below " + ReservedOCIPrefix + "/, which only the registry's enforcement point serves")
	}
	r.handleUncleaned(prefix, h)
}

// HandleRegistryUncleaned is [Router.HandleUncleaned] for a prefix at or below /v2: the uncleaned paths of the registry root.
// Only registry.Enforcer.Mount calls it, with the handler it wrapped; the route table's check lists the prefixes registered
// here ([Router.UncleanedPrefixes]) and admits exactly the enforcer's. A prefix outside /v2 panics.
func (r *Router) HandleRegistryUncleaned(prefix string, h http.Handler) {
	if !belowRegistry(prefix) {
		panic("httpserver: HandleRegistryUncleaned(" + prefix + ") lies outside " + ReservedOCIPrefix + "/")
	}
	r.handleUncleaned(prefix, h)
}

func (r *Router) handleUncleaned(prefix string, h http.Handler) {
	if r.allow != nil && !r.allow(prefix) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var cur []uncleanedRoute
	if p := r.uncleaned.Load(); p != nil {
		cur = *p
	}
	// The first prefix that matches wins (uncleanedHandler), so a second registration of one prefix could shadow the first,
	// and the route table's check admits a prefix by its string: a duplicate is refused, as a conflicting pattern is.
	if slices.ContainsFunc(cur, func(rt uncleanedRoute) bool { return rt.prefix == prefix }) {
		panic("httpserver: the uncleaned prefix " + prefix + " is registered twice")
	}
	next := append(slices.Clone(cur), uncleanedRoute{prefix: prefix, h: h})
	r.uncleaned.Store(&next)
}

// belowRegistry reports whether an uncleaned prefix is /v2 or lies below /v2/.
// The prefix is judged as ServeMux compares paths, with its escapes undone ("/v%32/" is "/v2/", R-HA-041).
func belowRegistry(prefix string) bool {
	return below(strings.TrimSuffix(literalPrefix(prefix), "/"), ReservedOCIPrefix)
}

// literalPrefix unescapes an uncleaned prefix; a prefix that does not unescape is judged as written.
func literalPrefix(prefix string) string {
	if u, err := url.PathUnescape(prefix); err == nil {
		return u
	}
	return prefix
}

// UncleanedPrefixes returns the prefixes registered with HandleUncleaned and HandleRegistryUncleaned: the route table's check
// reads them, because they serve requests that no pattern of [Router.Patterns] claims.
func (r *Router) UncleanedPrefixes() []string {
	routes := r.uncleaned.Load()
	if routes == nil {
		return nil
	}
	out := make([]string, 0, len(*routes))
	for _, rt := range *routes {
		out = append(out, rt.prefix)
	}
	return out
}

// HasFallback reports whether a handler of its own answers the requests no route claims ([Router.SetFallback]): such a
// handler serves paths no pattern names, so the route table's check refuses it on a router that serves /v2/.
func (r *Router) HasFallback() bool { return r.custom.Load() != nil }

// uncleanedHandler returns the handler registered with HandleUncleaned for the
// request, nil when the path is clean or no prefix matches.
func (r *Router) uncleanedHandler(req *http.Request) (string, http.Handler) {
	routes := r.uncleaned.Load()
	if routes == nil {
		return "", nil
	}
	p := req.URL.EscapedPath()
	for _, rt := range *routes {
		if strings.HasPrefix(p, rt.prefix) && muxWouldRedirect(p) {
			return rt.prefix, rt.h
		}
	}
	return "", nil
}

// muxWouldRedirect reports whether ServeMux cleans the path into another one:
// the rule of its cleanPath, which keeps one trailing slash.
func muxWouldRedirect(p string) bool {
	if p == "" || p[0] != '/' {
		return true
	}
	np := path.Clean(p)
	if p[len(p)-1] == '/' && np != "/" {
		if len(p) == len(np)+1 && strings.HasPrefix(p, np) {
			np = p
		} else {
			np += "/"
		}
	}
	return np != p
}

// ServeHTTP routes a request.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if _, h := r.uncleanedHandler(req); h != nil {
		h.ServeHTTP(w, req)
		return
	}
	r.mux.ServeHTTP(w, req)
}

// Pattern returns the pattern that serves req without serving it. It is how the
// chain labels a request for the log and the metrics before the mux ran, since
// middleware that copies the request would hide the mux's own r.Pattern from
// the outer stages (R-ARCH-005 step 3: the label is the pattern, never the path).
func (r *Router) Pattern(req *http.Request) string {
	if prefix, h := r.uncleanedHandler(req); h != nil {
		return prefix
	}
	_, p := r.mux.Handler(req)
	return p
}

// SetFallback makes h answer every request that no route claims, in place of the 404 and 405 problems of the
// router. A role that serves one path space with a handler of its own (the Bastion, which mirrors the main
// installation's URL space and answers its own refusals, R-BAS-018) registers it here, and with
// HandleUncleaned for the paths ServeMux would redirect.
func (r *Router) SetFallback(h http.Handler) { r.custom.Store(&h) }

// probeMethods are the methods fallback tries to build an Allow header.
func probeMethods() []string {
	return []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
}

// fallback is the "anything else" row. ServeMux answers a wrong method with 405
// only when no pattern matches the path, and "/" always matches, so the router
// asks the mux itself whether the path exists for another method.
func (r *Router) fallback(w http.ResponseWriter, req *http.Request) {
	if h := r.custom.Load(); h != nil {
		(*h).ServeHTTP(w, req)
		return
	}
	var allow []string
	if r.servesPath != nil && !r.servesPath(req.URL.Path) {
		Problem(w, req, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
		return
	}
	for _, m := range probeMethods() {
		probe := &http.Request{Method: m, URL: &url.URL{Path: req.URL.Path, RawPath: req.URL.RawPath}, Host: req.Host, Header: req.Header}
		if _, p := r.mux.Handler(probe); p != "" && p != CatchAllPattern {
			allow = append(allow, m)
		}
	}
	if len(allow) > 0 && !r.methodsHidden(req) {
		if slices.Contains(allow, http.MethodGet) && !slices.Contains(allow, http.MethodHead) {
			allow = append(allow, http.MethodHead)
		}
		Problem(w, req, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "The resource does not support this method.", errs.WithAllow(allow...)))
		return
	}
	Problem(w, req, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
}
