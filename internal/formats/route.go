package formats

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// RouteKind says what a route does, which fixes the content headers its response
// carries (R-ARCH-007) and the request kind of its coarse check (R-REPO-003).
type RouteKind uint8

const (
	// Download serves stored content; the response carries the content headers
	// with Content-Disposition.
	Download RouteKind = iota + 1
	// Metadata serves a package document: rendered from rows (local) or fetched
	// (remote). The response carries the content headers without
	// Content-Disposition.
	Metadata
	// Computed is rendered by trove from rows and never fetched. Same headers as
	// Metadata.
	Computed
	// Upload stores content or documents.
	Upload
	// Delete removes content or a package.
	Delete
	// Admin changes package rows without content: dist-tags, deprecation.
	Admin
	// Volatile is passed through and never cached: search.
	Volatile
)

func (k RouteKind) String() string {
	switch k {
	case Download:
		return "download"
	case Metadata:
		return "metadata"
	case Computed:
		return "computed"
	case Upload:
		return "upload"
	case Delete:
		return "delete"
	case Admin:
		return "admin"
	case Volatile:
		return "volatile"
	}
	return "unknown"
}

// Valid reports whether k is one of the kinds above.
func (k RouteKind) Valid() bool { return k >= Download && k <= Volatile }

// ServesContent reports whether the response of a route of this kind carries
// package content or package metadata, and so the content headers of R-ARCH-007.
// A route of any other kind that returns package-derived bytes is a contract
// violation (R-ARCH-010 check 7).
func (k RouteKind) ServesContent() bool { return k == Download || k == Metadata || k == Computed }

// RequestKind maps a route kind to the request kind of the coarse check
// (R-REPO-003): what a request behaves as in authorization and in caches.
func (k RouteKind) RequestKind() RequestKind {
	switch k {
	case Download:
		return KindArtifact
	case Metadata, Computed:
		return KindMetadata
	case Volatile:
		return KindSearch
	case Upload, Delete, Admin:
		return KindWrite
	}
	return KindOther
}

// HandlerFunc serves one route. It returns an error for a failure before the
// status line; the dispatcher renders it with [Format.WriteError]. Once the
// handler has written the status line it cannot report an error any more: the
// connection is aborted instead ([ServeRoute]).
type HandlerFunc func(ctx context.Context, rq *Request, w http.ResponseWriter) error

// Route is one protocol operation below /api/<id>/<repoKey>/ (R-ARCH-008).
type Route struct {
	// Name is the stable metric, log and audit label: "npm.tarball.get". The
	// request log and the metrics name the handler of a request by it.
	Name   string
	Method string
	// Template is the documentation form and the default matcher: "/{package}/-/{file}".
	// A placeholder is one path segment ({name}), or the whole tail ({rest...}).
	Template string
	// Examples are escaped paths this route must match and no other route of its
	// method may (kit check 1; Register runs the same check). At least one.
	Examples []string
	// Action is the coarse check before the body is read (R-ARCH-008).
	Action authz.Action
	Kind   RouteKind
	// Classes are the classes that serve the route; empty is every class of the
	// format.
	Classes []Class
	// Match overrides the template matcher for a grammar a template cannot state
	// (npm's scoped names). It receives the escaped repository-relative path.
	Match   func(escapedPath string) (Params, bool)
	Handler HandlerFunc
}

// matcher returns the route's matcher: Match, or the compiled Template.
func (r *Route) matcher() func(string) (Params, bool) {
	if r.Match != nil {
		return r.Match
	}
	return Template(r.Template)
}

// ServesClass reports whether the route serves repositories of a class.
func (r *Route) ServesClass(c Class) bool {
	return len(r.Classes) == 0 || slices.Contains(r.Classes, c)
}

// Params are the decoded placeholder values of a match. A %2F inside one segment
// stays inside its value.
type Params map[string]string

// Get returns the value of a placeholder, "" when the route has none by that name.
func (p Params) Get(name string) string { return p[name] }

var templates sync.Map // template text -> matcher

// Template compiles a route template into a matcher over the escaped
// repository-relative path. A segment of the template is a literal, compared with
// the decoded segment, a placeholder {name} that takes one non-empty decoded
// segment, or - as the last segment - {name...}, which takes the rest of the
// path, decoded segment by segment and joined with "/". A leading slash on the
// template or the path is ignored. A template that is not of this form panics:
// it is a programming error found by the first test.
func Template(t string) func(escapedPath string) (Params, bool) {
	if m, ok := templates.Load(t); ok {
		return m.(func(string) (Params, bool))
	}
	m := compileTemplate(t)
	templates.Store(t, m)
	return m
}

func compileTemplate(t string) func(string) (Params, bool) {
	type seg struct {
		lit  string
		name string
		rest bool
	}
	var segs []seg
	for i, s := range strings.Split(strings.TrimPrefix(t, "/"), "/") {
		switch {
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "...}"):
			if i != strings.Count(strings.TrimPrefix(t, "/"), "/") {
				panic("formats: the tail placeholder of template " + t + " is not its last segment")
			}
			segs = append(segs, seg{name: s[1 : len(s)-4], rest: true})
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
			segs = append(segs, seg{name: s[1 : len(s)-1]})
		case strings.ContainsAny(s, "{}"):
			panic("formats: template " + t + " mixes a placeholder with literal text in one segment; use Route.Match")
		default:
			segs = append(segs, seg{lit: s})
		}
	}
	return func(escaped string) (Params, bool) {
		parts := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
		var p Params
		for i, s := range segs {
			if i >= len(parts) {
				return nil, false
			}
			if s.rest {
				var decoded []string
				for _, part := range parts[i:] {
					d, err := url.PathUnescape(part)
					if err != nil || d == "" {
						return nil, false
					}
					decoded = append(decoded, d)
				}
				p = ensure(p)
				p[s.name] = strings.Join(decoded, "/")
				return p, true
			}
			d, err := url.PathUnescape(parts[i])
			if err != nil || d == "" {
				return nil, false
			}
			if s.name == "" {
				if d != s.lit {
					return nil, false
				}
				continue
			}
			p = ensure(p)
			p[s.name] = d
		}
		if len(parts) != len(segs) {
			return nil, false
		}
		return ensure(p), true
	}
}

func ensure(p Params) Params {
	if p == nil {
		return Params{}
	}
	return p
}

// MatchRoutes is the Match of [Base]: exactly one route accepts the method and
// the path, else errs.NotFound, or errs.MethodNotAllowed with Allow when routes
// accept the path for other methods. A path that is no canonical repository path
// (an empty, "." or ".." segment, NUL, a backslash or a control character, more
// than 4 KiB) never reaches a matcher: it is errs.Invalid with code invalid_path,
// so the kit's traversal inputs fail in the format, not only in the dispatcher
// that cleans the path first (R-ARCH-005, R-ARCH-010 check 2). HEAD is served by
// the GET route.
func MatchRoutes(routes []Route, method, escapedPath string) (*Route, Params, error) {
	if _, err := repository.CleanPath(escapedPath); err != nil {
		return nil, nil, err
	}
	want := method
	if want == http.MethodHead {
		want = http.MethodGet
	}
	var (
		hit    *Route
		params Params
		allow  []string
	)
	for i := range routes {
		r := &routes[i]
		p, ok := r.matcher()(escapedPath)
		if !ok {
			continue
		}
		if !slices.Contains(allow, r.Method) {
			allow = append(allow, r.Method)
		}
		if r.Method != want {
			continue
		}
		if hit != nil {
			return nil, nil, errs.E(errs.Internal, errs.CodeInternal, "Two routes accept the same request.")
		}
		hit, params = r, p
	}
	switch {
	case hit != nil:
		return hit, params, nil
	case len(allow) > 0:
		if slices.Contains(allow, http.MethodGet) && !slices.Contains(allow, http.MethodHead) {
			allow = append(allow, http.MethodHead)
		}
		slices.Sort(allow)
		return nil, nil, errs.E(errs.MethodNotAllowed, errs.CodeMethodNotAllowed,
			"The resource does not support this method.", errs.WithAllow(allow...))
	}
	return nil, nil, errs.E(errs.NotFound, errs.CodeNotFound, "No route matches the request path.")
}

// Request is one protocol request after routing, authentication and the coarse
// check. It is built by the dispatcher and is immutable for the handler.
type Request struct {
	*http.Request
	// Repo is the addressed repository, an immutable snapshot entry.
	Repo   *repository.Repository
	Route  *Route
	Params Params
	// Path is the canonical repository path (repository.CleanPath); Escaped is the
	// repository-relative path exactly as received, which is what a format
	// matches and what keeps an encoded slash apart from a separator.
	Path    Path
	Escaped string
	// Subject is who acts, and on which endpoint (Subject.Endpoint, Q-EP-20 (b)).
	Subject authz.Subject
	// Index is the index the request began on; every decision of the request runs
	// on it (R-AUTHZ-015).
	Index *authz.Index
	// Endpoint holds the facts of the request's endpoint (C-17).
	Endpoint EndpointFacts
	// CacheView says that the request addressed the cache view of a remote repository,
	// `<key>~cache` (R-REPO-032): the remote engine then serves stored entries only
	// and never contacts the upstream. The dispatcher sets it from the address.
	CacheView bool
	// Host is the core as the format reaches it. It shadows the http.Request's
	// Host string on purpose: no handler reads the Host header (R-ARCH-006).
	Host Host
	// Log is the request-scoped logger; nil logs to slog's default.
	Log *slog.Logger
	// HostState belongs to the Host implementation: the decisions it must record in
	// the transaction of the write (R-AUTHZ-029). A format never reads or sets it.
	HostState any
}

// EndpointFacts are what a format needs of the request's endpoint (R-EP-018,
// R-EP-019). Name equals Subject.Endpoint; the rest comes from the endpoint's
// configuration and rq.Index, so each fact has one source. The dispatcher fills
// it; nothing is read from request data.
type EndpointFacts struct {
	Name string
	// Rules is true for an endpoint with exposure: rules. Its responses carry no
	// Trove-Resolved-From, no Cache-Status and no member names (R-EP-018).
	Rules bool
	// CacheFill says whether the endpoint may make trove contact an upstream
	// (R-EP-019). The core folds it into every populate decision; a format only
	// reads it.
	CacheFill bool
	// ExposureFingerprint is "" in mode all; rendition keys and ETags include it
	// (R-EP-018).
	ExposureFingerprint string
	// Exposure is what the endpoint exposes of the addressed repository, the view
	// FilterDocument takes; nil exactly when the endpoint does not restrict (mode
	// all, or no endpoint). It is read from rq.Index, like ExposureFingerprint, so a
	// document is filtered by the rules its validator names (R-EP-018).
	Exposure ExposureView
}

// Target is what a format asks the decision point inside the addressed
// repository. The host completes the coordinate - repository, class, format, the
// request kind of the route - so a format can name neither another repository nor
// another endpoint (Q-ARCH-189). The values are normalized.
type Target struct {
	Package, Version, Path string
	// Actions are the actions the operation needs.
	Actions []Action
}

// PathKind says how a path behaves in caches (R-REPO-003).
type PathKind uint8

const (
	// Content is immutable once stored.
	Content PathKind = iota + 1
	// PathMetadata is a document that expires on a remote repository and is
	// rendered from rows on a local one (R-ARCH-012).
	PathMetadata
	// PathComputed is rendered by trove and never fetched.
	PathComputed
	// PathVolatile is passed through and never cached.
	PathVolatile
)

// RequestKind maps a path kind to the request kind of R-REPO-003.
func (k PathKind) RequestKind() RequestKind {
	switch k {
	case Content:
		return KindArtifact
	case PathMetadata, PathComputed:
		return KindMetadata
	case PathVolatile:
		return KindSearch
	}
	return KindOther
}

func (k PathKind) String() string {
	switch k {
	case Content:
		return "content"
	case PathMetadata:
		return "metadata"
	case PathComputed:
		return "computed"
	case PathVolatile:
		return "volatile"
	}
	return "unknown"
}

// PathInfo is the format's R-REPO-003 descriptor of a canonical path: how the
// path behaves and which package, if any, it belongs to. The generic layer asks
// for it to give a /files/ request its coordinate (R-EP-014).
type PathInfo struct {
	Kind PathKind
	// Package and Version are the normalized identity; "" when the path belongs
	// to no package or version.
	Package string
	Version string
	// RoutingName is the name a format derives for an artifact that belongs to no
	// package, so that patterns, claims and pin rules can protect it.
	RoutingName string
}

// RepoRef is the addressed repository as the Host's calls name it: the numeric id
// of its row (an id that is not a number yields 0 and matches nothing), its key,
// class and format.
func (rq *Request) RepoRef() RepoRef {
	var n int64
	for i := range len(rq.Repo.ID) {
		if rq.Repo.ID[i] < '0' || rq.Repo.ID[i] > '9' {
			n = 0
			break
		}
		n = n*10 + int64(rq.Repo.ID[i]-'0')
	}
	return RepoRef{ID: n, Key: rq.Repo.Key, Class: rq.Repo.Class, Format: ID(rq.Repo.Format)}
}
