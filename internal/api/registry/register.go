package registry

import (
	"errors"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// Pattern is the one pattern of the registry root on a router (R-OCI-003, Q-OCI-100): the whole subtree below /v2/, with every
// method. The dispatcher's handler matches the request itself (dispatch.Dispatcher.RegistryHandler), so the routes are the
// matcher's and not the multiplexer's: ServeMux unescapes the literal segments of a pattern ("/v2/%5Fcatalog" would reach a
// pattern "/v2/_catalog"), and a core route registered as a pattern of its own would be served by a handler the match did not
// name. The route table admits exactly this pattern below /v2/.
const Pattern = "/v2/{rest...}"

// BarePattern is the path of the registry root without its slash: mounted on the same handler, it answers the version check at
// `/v2` itself and not with the router's redirect, which has neither the registry's shape nor its version header.
const BarePattern = "/v2"

// ExampleRepo is the repository the examples of the format's routes address (RepositoryRoutes): a well-formed name the
// match reads without looking anything up, for the walk (registrytest.Walk).
var ExampleRepo = qname.Repo{Project: "web", Key: "docker-local"}

// RegisterOptions configure [Register].
type RegisterOptions struct {
	// Router is the endpoint's router.
	Router *httpserver.Router
	// Enforcer is the endpoint's enforcement point.
	Enforcer *Enforcer
	// Dispatcher is the format dispatcher: its match is the enforcer's and its handler serves everything below /v2/.
	Dispatcher *dispatch.Dispatcher
	// Format is the format that declares the registry root; its routes are mounted and its WriteError is the shape of /v2/.
	Format formats.Format
	// Version, Catalog and Token are the handlers of the three core routes.
	Version http.Handler
	Catalog http.Handler
	Token   http.Handler
}

// Register mounts the registry root on a router: the dispatcher's handler for everything below /v2/ behind the enforcement
// point, with every route of the format and the three core routes registered with it, and the error shape of the format for
// the refusals of the chain below /v2/ (R-OCI-003, Q-ARCH-214). It is the only place a handler is put below /v2/.
func Register(o RegisterOptions) error {
	if o.Router == nil || o.Enforcer == nil || o.Dispatcher == nil || o.Format == nil || o.Version == nil || o.Catalog == nil || o.Token == nil {
		return errors.New("registry: the registry root needs a router, an enforcement point, the dispatcher, the format and the three core handlers")
	}
	h, err := o.Dispatcher.RegistryHandler(dispatch.CoreRoutes{Version: o.Version, Catalog: o.Catalog, Token: o.Token})
	if err != nil {
		return err
	}
	routes := RepositoryRoutes(o.Format.Routes(), "/v2/"+ExampleRepo.Project+"/"+ExampleRepo.Key+"/")
	routes = append(routes,
		Route{Name: route.NameVersion, Kind: RouteVersion, Method: http.MethodGet, Example: "/v2/"},
		Route{Name: route.NameCatalog, Kind: RouteCatalog, Method: http.MethodGet, Example: "/v2/_catalog"},
		Route{Name: route.NameToken, Kind: RouteToken, Method: http.MethodGet, Example: "/v2/_token"},
	)
	if err := o.Enforcer.Mount(o.Router, []string{BarePattern, Pattern}, h, routes...); err != nil {
		return err
	}
	o.Router.HandleErrorShape("/v2/", o.Format.WriteError)
	return nil
}
