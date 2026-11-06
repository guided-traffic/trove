package bastionmain

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/authz"
)

// Route kinds as the observer sees them: the names of formats.RouteKind's String method, so this domain
// package needs no import of the format layer (R-ARCH-002). "unknown" is a kind this build does not know.
const (
	KindDownload = "download"
	KindMetadata = "metadata"
	KindComputed = "computed"
	KindUpload   = "upload"
	KindDelete   = "delete"
	KindAdmin    = "admin"
	KindVolatile = "volatile"
)

// FormatFiles is RouteMatch.Format of the raw file routes under /files/, which have no format prefix
// (R-BAS-013, R-BAS-014).
const FormatFiles = "files"

// RouteMatch is what the dispatcher tells the observer once the format matched the request to a route and
// before any decision or handler (R-BAS-013, R-BAS-014).
type RouteMatch struct {
	// Format is the format ID of the prefix ("npm") and RepoKey the addressed repository key as the path
	// names it, without the "~cache" view suffix.
	Format  string
	Repo string
	// Name is the route's stable label ("npm.tarball.get").
	Name string
	// Method is the request method.
	Method string
	// Kind is one of the Kind constants, taken from formats.Route.Kind.
	Kind string
	// Action is the route's coarse action (formats.Route.Action).
	Action authz.Action
}

// RouteDecision is what the dispatcher tells the observer after the coarse decision allowed the request and
// the repository resolved, still before the handler runs: the subject and the index the request decides on,
// which the observer needs to compute the caller's read shape (R-BAS-013).
type RouteDecision struct {
	Match   RouteMatch
	Subject authz.Subject
	Index   *authz.Index
	// Class is the class of the addressed repository ("local", "remote", "virtual").
	Class string
}

// RouteObserver is the seam through which the marking stage of work package 3 learns the route of a request
// ([Q-BAS-52]): the dispatcher of the format prefixes (internal/api/dispatch, built) calls it after the match
// and after the decision, when the request context carries one ([WithObserver]); the raw file handler
// (internal/api/rawfiles) does the same for /files/, which work package 3 adds. A nil observer costs nothing.
type RouteObserver interface {
	// Matched is called right after the format matched the route. A non-nil error ends the request and is
	// rendered by the format in its own error shape: the write refusal of a probe is bastion_read_only
	// (R-BAS-014). handled reports that the observer answered the request itself (the 204 with
	// Trove-Bastion-Route: read-with-body), so the dispatcher writes nothing.
	Matched(w http.ResponseWriter, r *http.Request, m RouteMatch) (handled bool, err error)
	// Decided is called when the decision allowed the request and the repository resolved. The raw file
	// handler calls it as soon as the caller exists, before its service decides the path, with no Class:
	// the observer takes the subject and the index and computes the shape, which does not depend on the path.
	Decided(r *http.Request, d RouteDecision)
}

type observerKey struct{}

// WithObserver returns ctx carrying the observer of the request. The marking stage calls it.
func WithObserver(ctx context.Context, o RouteObserver) context.Context {
	return context.WithValue(ctx, observerKey{}, o)
}

// ObserverFrom returns the observer of the request, nil when the request is no Bastion request.
func ObserverFrom(ctx context.Context) RouteObserver {
	o, _ := ctx.Value(observerKey{}).(RouteObserver)
	return o
}
