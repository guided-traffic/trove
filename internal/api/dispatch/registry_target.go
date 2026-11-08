package dispatch

import (
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The registry root's match of a request below /v2/ (Q-OCI-56). The enforcement point of a registry token's grant
// (registry.Enforcer) asks the dispatcher which route a request reaches, as the authentication stage asks APIKeyRoute: one
// match, so the route the grant is checked for is the route that serves the request, and no second matcher can disagree. The
// vocabulary is the leaf internal/api/registry/route, so the dispatcher depends on neither the registry root nor the
// authentication stage (an archtest rule holds it).

// registryFormat returns the format that declares the registry root, nil when none is registered.
func registryFormat(list []formats.Format) formats.Format {
	for _, f := range list {
		if f.Descriptor().RegistryRoot && len(f.Routes()) > 0 {
			return f
		}
	}
	return nil
}

// RegistryTarget implements route.Matcher: the route and the address a request below /v2/ reaches. The core routes
// (/v2/, /v2/_catalog, /v2/_token) resolve to their names; a repository route is the registry-root format's Match on the path
// after the repository segment, with the image name from the route's PackageParam. The repository is the one the path
// names - the base repository of a cache view (<key>~cache), which is decided and served on the remote repository itself -
// or, on a host whose root is bound to a repository (R-OCI-029, R-EP-006), that repository with the whole path after /v2/
// as the image's path. ok is false for a path that is no route: a registry token is refused there. The answer reads the
// request line and the resolved host only; it looks nothing up.
func (d *Dispatcher) RegistryTarget(r *http.Request) (route.Target, bool) {
	escaped := r.URL.EscapedPath()
	switch escaped {
	case "/v2", "/v2/":
		return route.Target{Route: route.NameVersion}, true
	case "/v2/_catalog":
		return route.Target{Route: route.NameCatalog}, true
	case "/v2/_token":
		return route.Target{Route: route.NameToken}, true
	}
	rest, ok := strings.CutPrefix(escaped, "/v2/")
	f := registryFormat(d.o.Formats())
	if !ok || f == nil {
		return route.Target{}, false
	}
	t := route.Target{}
	if root := httpserver.InfoFrom(r.Context()).RootRepository; !root.IsZero() {
		t.Repo, t.Bound = root, true
	} else {
		project, tail, ok1 := strings.Cut(rest, "/")
		key, after, ok2 := strings.Cut(tail, "/")
		if !ok1 || !ok2 || strings.Contains(project, "%") || strings.Contains(key, "%") {
			return route.Target{}, false
		}
		key, t.CacheView = strings.CutSuffix(key, qname.CacheSuffix)
		t.Repo, rest = qname.Repo{Project: project, Key: key}, after
		if !t.Repo.Valid() {
			return route.Target{}, false
		}
	}
	if strings.HasPrefix(rest, "/") {
		return route.Target{}, false
	}
	if _, err := repository.CleanPath(rest); err != nil {
		return route.Target{}, false
	}
	matched, params, err := f.Match(r.Method, rest)
	if err != nil {
		return route.Target{}, false
	}
	t.Route = matched.Name
	if matched.PackageParam != "" {
		t.Image = params[matched.PackageParam]
	}
	return t, true
}

var _ route.Matcher = (*Dispatcher)(nil)
