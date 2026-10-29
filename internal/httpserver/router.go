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
	"slices"
	"sync"

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

	mu       sync.Mutex
	patterns []string
	dropped  []string
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

// ServeHTTP routes a request.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) { r.mux.ServeHTTP(w, req) }

// Pattern returns the pattern that serves req without serving it. It is how the
// chain labels a request for the log and the metrics before the mux ran, since
// middleware that copies the request would hide the mux's own r.Pattern from
// the outer stages (R-ARCH-005 step 3: the label is the pattern, never the path).
func (r *Router) Pattern(req *http.Request) string {
	_, p := r.mux.Handler(req)
	return p
}

// probeMethods are the methods fallback tries to build an Allow header.
func probeMethods() []string {
	return []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
}

// fallback is the "anything else" row. ServeMux answers a wrong method with 405
// only when no pattern matches the path, and "/" always matches, so the router
// asks the mux itself whether the path exists for another method.
func (r *Router) fallback(w http.ResponseWriter, req *http.Request) {
	var allow []string
	for _, m := range probeMethods() {
		probe := &http.Request{Method: m, URL: &url.URL{Path: req.URL.Path, RawPath: req.URL.RawPath}, Host: req.Host, Header: req.Header}
		if _, p := r.mux.Handler(probe); p != "" && p != CatchAllPattern {
			allow = append(allow, m)
		}
	}
	if len(allow) > 0 {
		if slices.Contains(allow, http.MethodGet) && !slices.Contains(allow, http.MethodHead) {
			allow = append(allow, http.MethodHead)
		}
		Problem(w, req, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed, "The resource does not support this method.", errs.WithAllow(allow...)))
		return
	}
	Problem(w, req, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
}
