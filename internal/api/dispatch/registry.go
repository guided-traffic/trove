package dispatch

import (
	"errors"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// CoreRoutes are the handlers of the three routes below /v2/ that belong to the core and name no repository (R-OCI-003). The
// registry root builds them; the dispatcher calls the one its match names.
type CoreRoutes struct {
	// Version answers GET and HEAD of /v2/.
	Version http.Handler
	// Catalog answers GET and HEAD of /v2/_catalog.
	Catalog http.Handler
	// Token is the token service at /v2/_token.
	Token http.Handler
}

// RegistryHandler is the one handler of everything below /v2/ (R-OCI-001, R-OCI-003, Q-OCI-100): it matches the request with
// registryMatch, the function the enforcement point of a registry token's grant asks as well, and serves the route that match
// names - a core route through its handler in core, a repository route through the same path as a request below
// /api/<format>/ - so a request reaches no route its match did not name. It carries Docker-Distribution-API-Version on every
// response and writes every refusal of its own in the format's shape. The registry root mounts it through its enforcement
// point as the only handler below /v2/.
func (d *Dispatcher) RegistryHandler(core CoreRoutes) (http.Handler, error) {
	if core.Version == nil || core.Catalog == nil || core.Token == nil {
		return nil, errors.New("dispatch: the registry root needs the handlers of the version check, the catalog and the token service")
	}
	f := registryFormat(d.o.Formats())
	if f == nil {
		return nil, errors.New("dispatch: no registered format declares the registry root")
	}
	return &registryHandler{d: d, f: f, core: core}, nil
}

type registryHandler struct {
	d    *Dispatcher
	f    formats.Format
	core CoreRoutes
}

func (h *registryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(route.APIVersionHeader, route.APIVersion)
	m, err := h.d.registryMatch(r)
	if err != nil {
		h.f.WriteError(w, r, err)
		return
	}
	if h.d.o.Label != nil {
		switch m.target.Route {
		case route.NameVersion, route.NameCatalog, route.NameToken:
			h.d.o.Label(r.Context(), m.target.Route)
		}
	}
	switch m.target.Route {
	case route.NameVersion:
		h.core.Version.ServeHTTP(w, r)
		return
	case route.NameCatalog:
		h.core.Catalog.ServeHTTP(w, r)
		return
	case route.NameToken:
		h.core.Token.ServeHTTP(w, r)
		return
	}
	// From here on a refusal can build links: it knows the repository and the base URL (Q-ARCH-263).
	r = formats.WithRefusal(r, formats.Refusal{
		Format: h.f.Descriptor().ID, Name: m.target.Repo, CacheView: m.target.CacheView,
		BaseURL: strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/"),
	})
	h.d.serveRoute(w, r, h.f, m.target.Repo, m.target.CacheView, routed{route: m.route, params: m.params, escaped: m.rest, cleaned: m.cleaned, boundRepo: boundRepoOf(m.target)})
}

// boundRepoOf is the repository a bound match names, the zero value for an unbound one.
func boundRepoOf(t route.Target) qname.Repo {
	if t.Bound {
		return t.Repo
	}
	return qname.Repo{}
}
