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
// It is an edge package: it imports the shared conversions of internal/api/edge and
// the host, never the management API.
package dispatch

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// Ceiling is the part of the endpoint exposure the dispatcher reads: the
// fingerprint of the exposure rules of an endpoint for a repository and the view a
// format filters its documents with (R-EP-018). *authz.ExposureCeiling implements
// it.
type Ceiling interface {
	ExposureFingerprint(x *authz.Index, endpoint, repoKey string) string
	View(x *authz.Index, endpoint, repoKey string) *authz.ExposureView
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
}

// cacheViewSuffix turns a repository key into the address of its cache view (R-REPO-032).
const cacheViewSuffix = "~cache"

// Dispatcher serves the format prefixes.
type Dispatcher struct {
	o       Options
	formats map[string]formats.Format
}

// New creates the dispatcher over the formats that have routes.
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
	d := &Dispatcher{o: o, formats: map[string]formats.Format{}}
	for _, f := range o.Formats() {
		if len(f.Routes()) > 0 {
			d.formats[string(f.Descriptor().ID)] = f
		}
	}
	return d, nil
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
		r.Handle("/api/"+id+"/{repoKey}", d)
		r.Handle("/api/"+id+"/{repoKey}/{rest...}", d)
		r.HandleUncleaned("/api/"+id+"/", d)
		registerShape(r, id, d.formats[id])
	}
}

// ServeHTTP is the request path of R-ARCH-011 as far as the dispatcher owns it.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, key, rest, ok := splitPath(r.URL.EscapedPath())
	f := d.formats[id]
	if !ok || f == nil {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path."))
		return
	}
	fail := func(err error) { f.WriteError(w, r, err) }
	// <key>~cache addresses the cache view of remote repository <key> (R-REPO-032): the
	// request is decided and served on the remote repository itself.
	key, cacheView := strings.CutSuffix(key, cacheViewSuffix)
	if !repository.ValidKeyShape(key) {
		fail(errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key."))
		return
	}
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
	cleaned, err := repository.CleanPath(rest)
	if err != nil {
		fail(err)
		return
	}
	route, params, err := f.Match(r.Method, rest)
	if err != nil {
		fail(err)
		return
	}
	if d.o.Label != nil {
		d.o.Label(ctx, route.Name)
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
	repo, err := d.resolve(ctx, f, key)
	if err != nil {
		fail(err)
		return
	}
	if cacheView && repo.Class != repository.Remote {
		fail(errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key."))
		return
	}
	rq := &formats.Request{
		Request: r, Repo: &repo, Route: route, Params: params, Path: cleaned, Escaped: rest,
		Subject: acc.Subject, Index: acc.Index(), Endpoint: d.endpointFacts(r, acc, key),
		Host: d.o.Host, Log: httpserver.Logger(ctx), CacheView: cacheView,
	}
	formathost.Prepare(rq)
	defer formathost.Finish(rq)
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
// served. A repository of another format is the same 404 as a missing one.
func (d *Dispatcher) resolve(ctx context.Context, f formats.Format, key string) (repository.Repository, error) {
	repo, err := d.o.Repos.ByKey(ctx, key)
	if err != nil {
		return repository.Repository{}, err
	}
	notFound := errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "No repository has the requested key.")
	switch {
	case repo.Lifecycle == repository.Deleting, repo.Format != string(f.Descriptor().ID):
		return repository.Repository{}, notFound
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
func (d *Dispatcher) endpointFacts(r *http.Request, acc *edge.Access, key string) formats.EndpointFacts {
	facts := formats.EndpointFacts{Name: acc.Subject.Endpoint, CacheFill: true}
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

// splitPath cuts "/api/<id>/<key>[/<rest>]" into the format ID, the decoded
// repository key and the rest, which stays escaped (R-ARCH-005: the path is
// decoded exactly once, after it was split).
func splitPath(escaped string) (id, key, rest string, ok bool) {
	tail, found := strings.CutPrefix(escaped, "/api/")
	if !found {
		return "", "", "", false
	}
	id, tail, found = strings.Cut(tail, "/")
	if !found {
		return "", "", "", false
	}
	rawKey, rest, _ := strings.Cut(tail, "/")
	key, err := url.PathUnescape(rawKey)
	if err != nil || key == "" {
		return "", "", "", false
	}
	return id, key, rest, true
}
