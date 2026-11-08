package deb

import (
	"context"
	"net/http"
	"sync"

	"github.com/guided-traffic/trove/internal/formats"
)

// The handlers of the routes live in files of the work packages that build them (handlers_local.go, handlers_upload.go,
// handlers_remote.go, handlers_virtual.go) and register themselves here by route name. A route without a registered handler answers
// 501 deb_not_implemented (R-DEB-005), so the table of routes (routes.go) is complete and frozen from the first package on and no
// package edits it (apt-work-packages.md, 2.5).
var handlerRegistry = struct {
	mu sync.RWMutex
	by map[string]func(*Format) formats.HandlerFunc
}{by: map[string]func(*Format) formats.HandlerFunc{}}

// registerHandler registers the handler of a route. build receives the format, so the handler can reach what the format holds; it
// runs once when the format is created. Registering a name twice panics: two packages building one route is a programming error.
func registerHandler(route string, build func(*Format) formats.HandlerFunc) {
	handlerRegistry.mu.Lock()
	defer handlerRegistry.mu.Unlock()
	if _, dup := handlerRegistry.by[route]; dup {
		panic("deb: the handler of route " + route + " is registered twice")
	}
	handlerRegistry.by[route] = build
}

// handlerFor returns the registered handler of a route, or the 501 handler of the skeleton.
func (f *Format) handlerFor(route string) formats.HandlerFunc {
	handlerRegistry.mu.RLock()
	build := handlerRegistry.by[route]
	handlerRegistry.mu.RUnlock()
	if build == nil {
		return func(context.Context, *formats.Request, http.ResponseWriter) error { return notImplemented() }
	}
	return build(f)
}
