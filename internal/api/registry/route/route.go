// Package route is the vocabulary the dispatcher and the registry root share about a request below /v2/ (Q-OCI-56): the core
// route names, the target a request's match names and the matcher interface. It is a leaf - the standard library and
// internal/qname only - so the dispatcher, whose match the enforcement point asks, depends on neither the registry root nor
// the authentication stage, and the registry root may import the dispatcher without a cycle.
package route

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/qname"
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
