package dispatch

import (
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/registry/route"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The registry root's match of a request below /v2/ (Q-OCI-56, Q-OCI-100). The enforcement point of a registry token's grant
// (registry.Enforcer) asks the dispatcher which route a request reaches, as the authentication stage asks APIKeyRoute, and the
// /v2/ handler of the dispatcher serves by the same answer: both go through registryMatch, the one function that reads the
// request line, the resolved host and the binding of its root. The route the grant is checked for is the route that serves
// the request because nothing else parses it. The vocabulary is the leaf internal/api/registry/route, so the dispatcher
// depends on neither the registry root nor the authentication stage (an archtest rule holds it).

// registryFormat returns the format that declares the registry root, nil when none is registered.
func registryFormat(list []formats.Format) formats.Format {
	for _, f := range list {
		if f.Descriptor().RegistryRoot && len(f.Routes()) > 0 {
			return f
		}
	}
	return nil
}

// RegistryFormat returns the format that declares the registry root, nil when none is registered: the format whose routes
// are served below /v2/.
func (d *Dispatcher) RegistryFormat() formats.Format { return registryFormat(d.o.Formats()) }

// matched is what registryMatch read of a request below /v2/.
type matched struct {
	target route.Target
	// format, route, params and rest are set for a repository route: the format that owns the registry root, its matched
	// route, the route's parameters and the escaped path after the repository segment (the whole path after /v2/ on a bound
	// host). A core route has none of them.
	format formats.Format
	route  *formats.Route
	params formats.Params
	rest   string
	// cleaned is rest after repository.CleanPath.
	cleaned repository.Cleaned
}

// errNotAnAddress is the answer to a path below /v2/ that names no repository: the same 404 NAME_UNKNOWN for a path that is no
// address, a project or repository that does not exist and one the caller cannot see (R-OCI-001, R-AUTHZ-016).
func errNotAnAddress() error {
	return errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested name.")
}

// BoundRepository returns the repository the host root of the request is bound to for the registry (R-OCI-029, R-EP-006),
// false when the host is unbound and when the binding names a repository that does not exist or is not of the format that
// declares the registry root: such a binding behaves as no binding. A repository that is being deleted or is disabled stays
// bound (the request then answers its 404 like any request on it). The error is a failed lookup. It is the one reading of the
// binding: the match of /v2/ and the token service's scope parser (registry.TokenOptions.Bound) both call it, so a token's
// scopes and the address of the request that uses it name the same repository.
func (d *Dispatcher) BoundRepository(r *http.Request) (qname.Repo, bool, error) {
	root := httpserver.InfoFrom(r.Context()).RootRepository
	if root.IsZero() {
		return qname.Repo{}, false, nil
	}
	f := registryFormat(d.o.Formats())
	if f == nil {
		return qname.Repo{}, false, nil
	}
	repo, err := d.o.Repos.ByName(r.Context(), root)
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
			return qname.Repo{}, false, nil
		}
		return qname.Repo{}, false, err
	}
	if repo.Format != string(f.Descriptor().ID) {
		return qname.Repo{}, false, nil
	}
	return root, true, nil
}

// registryMatch is the dispatcher's one match below /v2/. The core routes (/v2/, /v2/_catalog, /v2/_token) resolve to their
// names whatever the method; a repository route is the registry-root format's Match on the path after the repository segment,
// with the image name from the route's PackageParam. The repository is the one the path names - the base repository of a
// cache view (<key>~cache), which is decided and served on the remote repository itself - or, on a host whose root is bound to
// a repository of the format (R-OCI-029, R-EP-006), that repository with the whole path after /v2/ as the image's path. The
// error says why a request reaches no route, in the shape the handler writes it; a lookup of the bound repository is the only
// thing it asks besides the request line.
func (d *Dispatcher) registryMatch(r *http.Request) (matched, error) {
	if memo := route.MemoFrom(r.Context()); memo != nil {
		v, err := memo.Do(func() (any, error) { return d.matchRegistry(r) })
		m, _ := v.(matched)
		return m, err
	}
	return d.matchRegistry(r)
}

func (d *Dispatcher) matchRegistry(r *http.Request) (matched, error) {
	switch r.URL.EscapedPath() {
	case "/v2", "/v2/":
		return matched{target: route.Target{Route: route.NameVersion}}, nil
	case "/v2/_catalog":
		return matched{target: route.Target{Route: route.NameCatalog}}, nil
	case "/v2/_token":
		return matched{target: route.Target{Route: route.NameToken}}, nil
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/v2/")
	f := registryFormat(d.o.Formats())
	if !ok || f == nil {
		return matched{}, errNotAnAddress()
	}
	m := matched{format: f}
	root, bound, err := d.BoundRepository(r)
	if err != nil {
		return matched{}, err
	}
	if bound {
		m.target.Repo, m.target.Bound = root, true
	} else {
		project, tail, ok1 := strings.Cut(rest, "/")
		key, after, ok2 := strings.Cut(tail, "/")
		if !ok1 || !ok2 || strings.Contains(project, "%") || strings.Contains(key, "%") {
			return matched{}, errNotAnAddress()
		}
		key, m.target.CacheView = strings.CutSuffix(key, qname.CacheSuffix)
		m.target.Repo, rest = qname.Repo{Project: project, Key: key}, after
		if !m.target.Repo.Valid() {
			return matched{}, errNotAnAddress()
		}
	}
	if strings.HasPrefix(rest, "/") {
		return matched{}, errs.E(errs.Invalid, errs.CodeInvalidPath, "the path has an empty segment")
	}
	cleaned, err := repository.CleanPath(rest)
	if err != nil {
		return matched{}, err
	}
	rt, params, err := f.Match(r.Method, rest)
	if err != nil {
		return matched{}, err
	}
	m.route, m.params, m.rest, m.cleaned = rt, params, rest, cleaned
	m.target.Route = rt.Name
	if rt.PackageParam != "" {
		m.target.Image = params[rt.PackageParam]
	}
	return m, nil
}

// RegistryTarget implements route.Matcher: the route and the address a request below /v2/ reaches, registryMatch reduced to
// the target. ok is false for a path that is no route and for a failed lookup of the bound repository: a registry token is
// refused there (fail closed).
func (d *Dispatcher) RegistryTarget(r *http.Request) (route.Target, bool) {
	m, err := d.registryMatch(r)
	if err != nil {
		return route.Target{}, false
	}
	return m.target, true
}

// RegistryMatch implements route.ErrMatcher: [Dispatcher.RegistryTarget] with the reason a request reaches no route.
func (d *Dispatcher) RegistryMatch(r *http.Request) (route.Target, error) {
	m, err := d.registryMatch(r)
	return m.target, err
}

var _ route.ErrMatcher = (*Dispatcher)(nil)
