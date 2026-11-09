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
	// remote holds the handlers a route has for the repositories of the remote class. The format's Match is blind to the class
	// (Q-DEB-45), so three routes (deb.dists.get, deb.dists.by_hash, deb.pool.get) serve every class; the handler a package registers
	// with registerHandler answers the local and virtual classes, the one registered with registerRemoteHandler the remote class, and
	// no package edits the file of the other (apt-work-packages.md, 2.5).
	remote map[string]func(*Format) formats.HandlerFunc
}{by: map[string]func(*Format) formats.HandlerFunc{}, remote: map[string]func(*Format) formats.HandlerFunc{}}

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

// registerRemoteHandler registers the handler a route has for repositories of the remote class; a request addressed to a remote
// repository is answered by it and no other. Registering a route twice panics.
func registerRemoteHandler(route string, build func(*Format) formats.HandlerFunc) {
	handlerRegistry.mu.Lock()
	defer handlerRegistry.mu.Unlock()
	if _, dup := handlerRegistry.remote[route]; dup {
		panic("deb: the remote handler of route " + route + " is registered twice")
	}
	handlerRegistry.remote[route] = build
}

// handlerFor returns the registered handler of a route, or the 501 handler of the skeleton. A route that has a handler for the remote
// class dispatches on the class of the repository the request is addressed to.
func (f *Format) handlerFor(route string) formats.HandlerFunc {
	handlerRegistry.mu.RLock()
	build, remote := handlerRegistry.by[route], handlerRegistry.remote[route]
	handlerRegistry.mu.RUnlock()
	var shared, forRemote formats.HandlerFunc
	if build != nil {
		shared = build(f)
	}
	if remote != nil {
		forRemote = remote(f)
	}
	switch {
	case shared == nil && forRemote == nil:
		return func(context.Context, *formats.Request, http.ResponseWriter) error { return notImplemented() }
	case forRemote == nil:
		return shared
	}
	return func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
		if rq != nil && rq.Repo != nil && rq.Repo.Class == formats.Remote {
			return forRemote(ctx, rq, w)
		}
		if shared == nil {
			return notImplemented()
		}
		return shared(ctx, rq, w)
	}
}

// handlerRegistered reports whether a work package registered the handler of a route, for any class.
func handlerRegistered(route string) bool {
	handlerRegistry.mu.RLock()
	defer handlerRegistry.mu.RUnlock()
	return handlerRegistry.by[route] != nil || handlerRegistry.remote[route] != nil
}
