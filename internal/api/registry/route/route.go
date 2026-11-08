// Package route is the vocabulary the dispatcher and the registry root share about a request below /v2/ (Q-OCI-56): the core
// route names, the target a request's match names and the matcher interface. It is a leaf - the standard library and
// internal/qname only - so the dispatcher, whose match the enforcement point asks, depends on neither the registry root nor
// the authentication stage, and the registry root may import the dispatcher without a cycle.
package route

import (
	"context"
	"net/http"
	"sync"

	"github.com/guided-traffic/trove/internal/qname"
)

// APIVersionHeader and APIVersion are the header every response below /v2/ carries (R-OCI-003): Docker clients read it to
// recognise a version 2 registry.
const (
	APIVersionHeader = "Docker-Distribution-API-Version"
	APIVersion       = "registry/2.0"
)

// The names of the core routes below /v2/ (R-OCI-003); the dispatcher's match resolves them.
const (
	NameVersion = "registry.version"
	NameCatalog = "registry.catalog"
	NameToken   = "registry.token" //nolint:gosec // G101: a route name, not a credential
)

// Target is what the dispatcher's match read of a request below /v2/: the route and, for a repository route, the address.
type Target struct {
	// Route is the name of the matched route: a formats.Route.Name for a repository route, [NameVersion], [NameCatalog] or
	// [NameToken] for a core route.
	Route string
	// Repo is the repository the grant is checked on: the repository the path names, the bound repository on a host whose
	// root is bound to one (R-OCI-029), and the base repository of a cache view (<key>~cache, R-REPO-032), which is decided
	// and served on the remote repository itself and is granted under the base repository's name.
	Repo qname.Repo
	// Image is the value of the route's package parameter: the image name.
	Image string
	// CacheView and Bound only shape the scope a challenge names, so the client asks for the name it addressed.
	CacheView bool
	Bound     bool
}

// Matcher is the dispatcher's match of a request below /v2/ (dispatch.Dispatcher.RegistryTarget): the one function that says
// which route serves a request. ok false for a request that reaches no route.
type Matcher interface {
	RegistryTarget(r *http.Request) (Target, bool)
}

// ErrMatcher is a [Matcher] that also says why a request reaches no route: the error is the one the handler writes (a missing
// repository, an unclean path, a failed lookup of the bound repository). The enforcement point answers a failed lookup
// (errs.Unavailable) as the handler does, instead of as a missing scope.
type ErrMatcher interface {
	Matcher
	RegistryMatch(r *http.Request) (Target, error)
}

// Memo holds the match of one request, so that the enforcement point and the handler behind it read the request once: the
// enforcement point puts an empty memo into the request's context before it checks, and the dispatcher's match fills it on the
// first call and returns the same answer on the second. A binding that changes between two reads cannot make the grant be checked
// on one repository and the request be served on another.
type Memo struct {
	once sync.Once
	v    any
	err  error
}

// Do runs f once and returns its result to every caller.
func (m *Memo) Do(f func() (any, error)) (any, error) {
	m.once.Do(func() { m.v, m.err = f() })
	return m.v, m.err
}

type memoKey struct{}

// WithMemo returns the request with an empty memo in its context, unless it carries one.
func WithMemo(r *http.Request) *http.Request {
	if MemoFrom(r.Context()) != nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), memoKey{}, &Memo{}))
}

// MemoFrom returns the request's memo, nil for a request that has none.
func MemoFrom(ctx context.Context) *Memo {
	m, _ := ctx.Value(memoKey{}).(*Memo)
	return m
}
