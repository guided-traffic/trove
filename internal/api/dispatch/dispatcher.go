// Package dispatch is the format dispatcher: the HTTP entry point of the package
// protocols under /api/<format>/<repoKey>/ (R-ARCH-005, R-ARCH-011, R-ARCH-014). It
// routes a request to the route of the format that owns the prefix, runs the
// checks every protocol request owes before a format sees it - the repository key,
// the clean path, the match, the index, the coarse authorization, the repository's
// lifecycle and class - and hands the format a [formats.Request] and the Host.
//
// A format registers one prefix when it has routes; generic has none, its protocol
// is /files/ (Q-GENERIC-7). Every error of the prefix is rendered by the format's
// own [formats.Format.WriteError], including the refusals of the chain's stages
// (Q-ARCH-214, errors.go).
//
// The registry root below /v2/ is served by [Dispatcher.RegistryHandler] (registry.go) through the same checks after its own match
// (registry_target.go), which the enforcement point of the registry tokens' grants asks as well.
//
// It is an edge package: it imports the shared conversions of internal/api/edge, never
// the management API, and talks to the host through [formats.Host] only.
package dispatch

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// Ceiling is the part of the endpoint exposure the dispatcher reads: the
// fingerprint of the exposure rules of an endpoint for a repository and the view a
// format filters its documents with (R-EP-018). *authz.ExposureCeiling implements
// it.
type Ceiling interface {
	ExposureFingerprint(x *authz.Index, endpoint string, repoKey qname.Repo) string
	View(x *authz.Index, endpoint string, repoKey qname.Repo) *authz.ExposureView
}

// Options configure [New].
type Options struct {
	Host    formats.Host
	Decider authz.Decider
	Repos   repository.Reader
	// Formats lists the formats; nil is formats.List.
	Formats func() []formats.Format
	// Ceiling gives the exposure fingerprint; nil yields none (mode all).
	Ceiling Ceiling
	// Label names the handler of the request for the request log and the metrics:
	// the dispatcher calls it with the route name (R-ARCH-005 step 3). Nil does not
	// label.
	Label func(ctx context.Context, name string)
	// Writes refuses the routes that change packages (kinds Upload, Delete and Admin) while the installation is in read-only
	// maintenance mode (R-OPS-024); nil allows them. The refusal is rendered in the format's own error shape, after the
	// coarse check, so a caller who may not write is told 403 and not the mode.
	Writes WriteGate
}

// WriteGate is the question the read-only maintenance mode puts to a write: nil while writes are allowed, the 503 otherwise.
// *sysstate.Store implements it.
type WriteGate interface {
	RefuseWrite(ctx context.Context) error
}

// Dispatcher serves the format prefixes.
type Dispatcher struct {
	o       Options
	formats map[string]formats.Format
	// keyed holds the IDs of the formats with a route that declares the API-key
	// header ([Dispatcher.APIKeyRoute]).
	keyed map[string]bool
	// tokened holds the IDs of the formats with a route that declares the Token scheme ([Dispatcher.TokenSchemeRoute]).
	tokened map[string]bool
}

// New creates the dispatcher over the formats that have routes below /api/<id>/. The format that declares the registry root has
// its routes below /v2/<project>/<key>/ ([catalog.Facts.RegistryRoot]): it is served by [Dispatcher.RegistryHandler], which the
// registry root mounts below /v2/, and not at /api/<id>/, so it must not answer there.
func New(o Options) (*Dispatcher, error) {
	switch {
	case o.Host == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "dispatch: no host")
	case o.Decider == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "dispatch: no decision point")
	case o.Repos == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "dispatch: no repository reader")
	}
	if o.Formats == nil {
		o.Formats = formats.List
	}
	d := &Dispatcher{o: o, formats: map[string]formats.Format{}, keyed: map[string]bool{}, tokened: map[string]bool{}}
	for _, f := range o.Formats() {
		routes := f.Routes()
		if len(routes) == 0 || f.Descriptor().RegistryRoot {
			continue
		}
		id := string(f.Descriptor().ID)
		d.formats[id] = f
		d.keyed[id] = slices.ContainsFunc(routes, func(r formats.Route) bool { return r.APIKey })
		d.tokened[id] = slices.ContainsFunc(routes, func(r formats.Route) bool { return r.TokenScheme })
	}
	return d, nil
}

// APIKeyRoute reports whether the request reaches a route that declares
// [formats.Route.APIKey] (Q-ARCH-266). The authentication stage runs before the
// dispatcher and asks it to know where the API-key header is a credential
// (R-AUTH-001 item 1, Q-AUTH-186). The answer is the dispatcher's own: the
// endpoint's surface, the prefix, the key shape, the clean path and the format's
// Match, the steps [Dispatcher.ServeHTTP] takes before the coarse check, so the
// stage and the dispatcher agree on the route. It reads the request line only;
// whether the key names a repository of the format is decided after
// authentication, as on every route. A format without such a route costs a map
// lookup.
func (d *Dispatcher) APIKeyRoute(r *http.Request) bool {
	route := d.matchedRoute(r, d.keyed)
	return route != nil && route.APIKey
}

// TokenSchemeRoute reports whether the request reaches a route that declares [formats.Route.TokenScheme] (Q-ANS-4, ADR-191): the
// route of a format whose clients send a configured token as "Authorization: Token <token>". The authentication stage asks it to know
// where that scheme is a credential (R-ANS-006), with the same match as [Dispatcher.APIKeyRoute], so the stage and the dispatcher agree
// on the route. A format without such a route costs a map lookup.
func (d *Dispatcher) TokenSchemeRoute(r *http.Request) bool {
	route := d.matchedRoute(r, d.tokened)
	return route != nil && route.TokenScheme
}

// matchedRoute is the route the request reaches, or nil: the dispatcher's own match, which the stage asks before the dispatcher runs.
// It reads the request line only (the endpoint's surface, the prefix, the key shape, the clean path and the format's Match, the steps
// [Dispatcher.ServeHTTP] takes before the coarse check); whether the key names a repository of the format is decided after
// authentication, as on every route. flagged holds the IDs of the formats that have a route with the flag asked about.
func (d *Dispatcher) matchedRoute(r *http.Request, flagged map[string]bool) *formats.Route {
	id, addr, ok := splitPath(r.URL.EscapedPath())
	f := d.formats[id]
	if !ok || f == nil || !flagged[id] {
		return nil
	}
	rest := addr.Rest
	if ep := httpserver.EndpointFrom(r.Context()); ep != nil &&
		(!ep.Serves(config.SurfacePackages) || !ep.Serves(httpserver.FamilyOfPath(r.URL.Path))) {
		return nil
	}
	if strings.HasPrefix(rest, "/") {
		return nil
	}
	if _, err := repository.CleanPath(cleanable(rest)); err != nil {
		return nil
	}
	route, _, err := f.Match(r.Method, rest)
	if err != nil {
		return nil
	}
	return route
}

// Prefixes returns the format IDs the dispatcher serves, sorted.
func (d *Dispatcher) Prefixes() []string {
	out := make([]string, 0, len(d.formats))
	for id := range d.formats {
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// Register mounts the prefix of every format with routes (R-ARCH-005): one pattern
// per format ID, the bare repository key too, and the subtree for the paths
// ServeMux would redirect (a//b, a/../b), which reach the dispatcher and answer 400
// invalid_path. No method is in the pattern: the format's Match answers 405 with
// Allow in its own shape. Each prefix also gets the error shape of its format.
func (d *Dispatcher) Register(r *httpserver.Router) {
	for _, id := range d.Prefixes() {
		r.Handle("/api/"+id+"/{projectKey}/{repoKey}", d)
		r.Handle("/api/"+id+"/{projectKey}/{repoKey}/{rest...}", d)
		r.HandleUncleaned("/api/"+id+"/", d)
		registerShape(r, id, d.formats[id])
	}
}

// ServeHTTP is the request path of R-ARCH-011 as far as the dispatcher owns it.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, addr, ok := splitPath(r.URL.EscapedPath())
	f := d.formats[id]
	if f == nil {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
		return
	}
	if !ok {
		// A path with fewer than two segments below the prefix, or with a project or key that is none, names no
		// repository (a project of one segment is not an address).
		f.WriteError(w, r, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested name."))
		return
	}
	// <key>~cache addresses the cache view of remote repository <key> (R-REPO-032): the
	// request is decided and served on the remote repository itself.
	key, rest, cacheView := addr.Repo, addr.Rest, addr.CacheView
	// From here on a refusal can build links: it knows the repository and the base
	// URL (Q-ARCH-263).
	r = formats.WithRefusal(r, formats.Refusal{
		Format: f.Descriptor().ID, Name: key, CacheView: cacheView, BaseURL: strings.TrimSuffix(httpserver.InfoFrom(r.Context()).BaseURL, "/"),
	})
	fail := func(err error) { f.WriteError(w, r, err) }
	ctx := r.Context()
	// A path below the prefix that belongs to a family of its own (npm's exchange)
	// is served only where the endpoint's surface holds that family.
	if fam := httpserver.FamilyOfPath(r.URL.Path); fam != config.SurfacePackages {
		if ep := httpserver.EndpointFrom(ctx); ep != nil && !ep.Serves(fam) {
			fail(errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
			return
		}
	}
	// The path is cleaned before authorization (R-ARCH-005): an escaped dot is as
	// dead as a dot, and an invalid path never reaches the decision point.
	if strings.HasPrefix(rest, "/") { // "/api/npm/key//x": an empty first segment
		fail(errs.E(errs.Invalid, errs.CodeInvalidPath, "the path has an empty segment"))
		return
	}
	cleaned, err := repository.CleanPath(cleanable(rest))
	if err != nil {
		fail(err)
		return
	}
	route, params, err := f.Match(r.Method, rest)
	if err != nil {
		fail(err)
		return
	}
	d.serveRoute(w, r, f, key, cacheView, routed{route: route, params: params, escaped: rest, cleaned: cleaned})
}

// cleanable is the path CleanPath reads: the path as received, except that a first segment "." is dropped. An APT client reads the source
// line `deb <repository URL> ./` as the flat repository at the root and requests `<repository URL>/./InRelease`; a dot segment is
// refused everywhere else, but the root of a flat repository is named by it, so the format's Match is handed the path as received (it
// reads a leading "./" as the root directory and refuses any other dot segment) and the canonical path is the one below the dot
// (Q-ARCH-385, R-DEB-003). A second dot segment ("./.", "././x") stays in the path and is refused.
func cleanable(rest string) string {
	if tail, ok := strings.CutPrefix(rest, "./"); ok {
		return tail
	}
	return rest
}

// routed is a request after the match: the route, its parameters and the path below the repository segment, escaped as
// received and cleaned.
type routed struct {
	route   *formats.Route
	params  formats.Params
	escaped string
	cleaned repository.Cleaned
	// boundRepo is the repository the host is bound to when the match read the request on a host bound to the addressed repository
	// (R-OCI-029), the zero value otherwise: the registry root's match says it, a request below /api/<format>/ never is. It becomes
	// EndpointFacts.BoundRepo and, non-zero, EndpointFacts.Bound, so a format and the host's link builder read the address the way the
	// match and the enforcement point did, and never from the raw binding.
	boundRepo qname.Repo
}

// serveRoute is the request path of R-ARCH-011 from the label of the matched route on: the Bastion's marking, the coarse
// check, the repository's lifecycle and class, the format's handler. The prefix /api/<format>/ and the registry root /v2/
// both end here, so a request is decided and served by one code path whatever its address. r carries the refusal context.
func (d *Dispatcher) serveRoute(w http.ResponseWriter, r *http.Request, f formats.Format, key qname.Repo, cacheView bool, m routed) {
	route, params, rest, cleaned := m.route, m.params, m.escaped, m.cleaned
	id := string(f.Descriptor().ID)
	fail := func(err error) { f.WriteError(w, r, err) }
	ctx := r.Context()
	if d.o.Label != nil {
		d.o.Label(ctx, route.Name)
	}
	// The Bastion marking stage learns the route here (R-BAS-013, R-BAS-014): it may refuse a write probe
	// in the format's shape or answer the request itself, before any decision or body read.
	obs := bastionmain.ObserverFrom(ctx)
	match := bastionmain.RouteMatch{
		Format: id, Repo: key, Name: route.Name, Method: r.Method, Kind: route.Kind.String(), Action: route.Action,
	}
	if obs != nil {
		handled, err := obs.Matched(w, r, match)
		if err != nil {
			fail(err)
			return
		}
		if handled {
			return
		}
	}
	acc, err := edge.Resolve(ctx, d.o.Decider, edge.SubjectFrom(r), key)
	if err != nil { // 503 authz_stale, 412 authz_epoch_unknown
		fail(err)
		return
	}
	// The coarse check: the route's action on the repository, before any body is
	// read and before the repository is looked up. The exposure ceiling of the
	// subject's endpoint applies first (R-EP-013).
	dec := acc.Decide(ctx, authz.Coordinate{Repository: key, Kind: route.Kind.RequestKind(), Actions: []authz.Action{route.Action}})
	if !dec.Allowed() {
		fail(dec.Err())
		return
	}
	if d.o.Writes != nil && route.Kind.RequestKind() == formats.KindWrite {
		if err := d.o.Writes.RefuseWrite(ctx); err != nil {
			fail(err)
			return
		}
	}
	repo, err := d.resolve(ctx, f, key)
	if err != nil {
		fail(err)
		return
	}
	if cacheView && repo.Class != repository.Remote {
		fail(errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key."))
		return
	}
	if obs != nil {
		obs.Decided(r, bastionmain.RouteDecision{Match: match, Subject: acc.Subject, Index: acc.Index(), Class: string(repo.Class)})
	}
	rq := &formats.Request{
		Request: r, Repo: &repo, Route: route, Params: params, Path: cleaned, Escaped: rest,
		Subject: acc.Subject, Index: acc.Index(), Endpoint: d.endpointFacts(r, acc, key, m.boundRepo),
		Host: d.o.Host, Log: httpserver.Logger(ctx), CacheView: cacheView,
	}
	defer d.o.Host.Begin(rq)()
	if !route.ServesClass(repo.Class) {
		target, err := d.redirectClass(ctx, f, rq)
		if err != nil {
			fail(err)
			return
		}
		rq.Repo = target
	}
	formats.ServeRoute(f, w, rq, httpserver.SessionAuthenticated(r))
}

// resolve looks the repository up after the decision allowed the request (R-ARCH-011
// step 2): one being deleted, one of another format and a disabled one are not
// served. A repository of another format is a 404 repository_not_found like a missing
// one, whose message says that the repository is not of this format: the decision
// allowed the caller a request on the key, so the key is one the caller may name
// (Q-ARCH-263).
func (d *Dispatcher) resolve(ctx context.Context, f formats.Format, key qname.Repo) (repository.Repository, error) {
	repo, err := d.o.Repos.ByName(ctx, key)
	if err != nil {
		return repository.Repository{}, err
	}
	switch {
	case repo.Lifecycle == repository.Deleting:
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key.")
	case repo.Format != string(f.Descriptor().ID):
		name := f.Descriptor().DisplayName
		if name == "" {
			name = string(f.Descriptor().ID)
		}
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "The repository is not a "+name+" repository.")
	case repo.Disabled:
		return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryDisabled, "The repository is disabled.")
	}
	return repo, nil
}

// redirectClass answers a route that the addressed repository's class does not
// serve: a write to a virtual repository, whatever kind of write, goes to its deploy
// target (R-REPO-021, 405 no_deploy_target without one); anything else is 405 with
// the methods the path does have for the class. The refusal names the class, so that
// a format words it in its own code (npm: npm_class_not_writable, R-NPM-005).
func (d *Dispatcher) redirectClass(ctx context.Context, f formats.Format, rq *formats.Request) (*repository.Repository, error) {
	if rq.Repo.Class == formats.Virtual && rq.Route.Kind.RequestKind() == formats.KindWrite {
		target, err := d.o.Host.Resolver().DeployTarget(ctx, rq)
		if err != nil {
			return nil, err
		}
		if target != nil && rq.Route.ServesClass(target.Class) {
			return target, nil
		}
	}
	return nil, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed,
		"A "+string(rq.Repo.Class)+" repository does not serve this request.", errs.WithAllow(d.allowFor(f, rq)...),
		errs.WithExtension("repositoryClass", string(rq.Repo.Class)))
}

// allowFor lists the methods the path has a route for in the repository's class.
func (d *Dispatcher) allowFor(f formats.Format, rq *formats.Request) []string {
	var allow []string
	for _, r := range f.Routes() {
		if !r.ServesClass(rq.Repo.Class) {
			continue
		}
		if _, ok := routeMatcher(r)(rq.Escaped); ok {
			allow = append(allow, r.Method)
			if r.Method == http.MethodGet {
				allow = append(allow, http.MethodHead)
			}
		}
	}
	sortStrings(allow)
	return allow
}

func routeMatcher(r formats.Route) func(string) (formats.Params, bool) {
	if r.Match != nil {
		return r.Match
	}
	return formats.Template(r.Template)
}

// endpointFacts derives what a format reads of the request's endpoint from the
// listener's endpoint and the request's index, never from request data (C-17,
// Q-EP-20 (b)).
func (d *Dispatcher) endpointFacts(r *http.Request, acc *edge.Access, key, boundRepo qname.Repo) formats.EndpointFacts {
	facts := formats.EndpointFacts{Name: acc.Subject.Endpoint, CacheFill: true, Bound: !boundRepo.IsZero(), BoundRepo: boundRepo}
	if ep := httpserver.EndpointFrom(r.Context()); ep != nil {
		facts.Rules = ep.ExposureMode() == config.ExposureRules
		facts.CacheFill = ep.CacheFill()
	}
	if d.o.Ceiling != nil && facts.Name != "" {
		facts.ExposureFingerprint = d.o.Ceiling.ExposureFingerprint(acc.Index(), facts.Name, key)
		// The view and the fingerprint come from one index: a filtered document and
		// its validator name the same rules. A nil view stays a nil interface.
		if v := d.o.Ceiling.View(acc.Index(), facts.Name, key); v != nil {
			facts.Exposure = v
		}
	}
	if facts.Rules && facts.Exposure == nil {
		// An endpoint with rules whose rules this dispatcher cannot read exposes
		// nothing (fail closed, R-EP-012).
		facts.Exposure = &authz.ExposureView{}
	}
	return facts
}

// splitPath cuts "/api/<id>/<project>/<key>[~cache][/<rest>]" into the format ID and the address, parsed by
// qname.ParseAddress (R-ARCH-005: the path is decoded exactly once, after it was split; the named segments
// hold no escape). ok is false when the path is no address; id is still the format ID where there is one.
func splitPath(escaped string) (id string, addr qname.Address, ok bool) {
	if tail, found := strings.CutPrefix(escaped, "/api/"); found {
		id, _, _ = strings.Cut(tail, "/")
	}
	addr, ok = qname.ParseAddress(escaped)
	if !ok || addr.Family != qname.FamilyAPI {
		return id, qname.Address{}, false
	}
	return addr.Format, addr, true
}
