package ansible

import (
	"context"
	"net/http"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// The handlers of the routes live in the files of the classes that serve them (handlers_local.go, handlers_remote.go,
// handlers_virtual.go) and register themselves here by class and route name. A route whose class registered no handler answers 501
// ansible_not_implemented, so the route table (routes.go) is complete and frozen and no class file edits it.
var handlerRegistry = struct {
	mu sync.RWMutex
	by map[formats.Class]map[string]func(*Format) formats.HandlerFunc
}{by: map[formats.Class]map[string]func(*Format) formats.HandlerFunc{}}

// registerHandler registers the handler of a route for one class. build receives the format, so the handler can reach what the format
// holds; it runs when the handler is first needed. Registering a pair twice panics: two files building one route are a programming
// error.
func registerHandler(class formats.Class, route string, build func(*Format) formats.HandlerFunc) {
	handlerRegistry.mu.Lock()
	defer handlerRegistry.mu.Unlock()
	byRoute := handlerRegistry.by[class]
	if byRoute == nil {
		byRoute = map[string]func(*Format) formats.HandlerFunc{}
		handlerRegistry.by[class] = byRoute
	}
	if _, dup := byRoute[route]; dup {
		panic("ansible: the handler of route " + route + " for class " + string(class) + " is registered twice")
	}
	byRoute[route] = build
}

// op is the handler of a route in the table: it decides the class itself, as one handler per path shape (a route that several classes
// serve cannot be two routes, because Match is blind to the class and two routes that overlap in path would match one request twice,
// Q-DEB-45). It looks up the handler the request's class registered and runs it; without one it answers 501.
func (f *Format) op(route string) formats.HandlerFunc {
	return func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
		handlerRegistry.mu.RLock()
		build := handlerRegistry.by[rq.Repo.Class][route]
		handlerRegistry.mu.RUnlock()
		if build == nil {
			return notImplemented(route, rq.Repo.Class)
		}
		return build(f)(ctx, rq, w)
	}
}

// notImplemented is the answer of a route whose class has no handler yet: 501, so a client reads a failure of the server and not a
// missing package.
func notImplemented(route string, class formats.Class) error {
	return errs.E(errs.NotImplemented, errs.CodeAnsibleNotImplemented,
		"The operation "+route+" of the Ansible Galaxy API is not served by this build of trove for "+string(class)+" repositories yet.")
}
