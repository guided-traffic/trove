package ansible

import (
	"context"
	"sort"
	"sync"

	"github.com/guided-traffic/trove/internal/formats"
)

// The documentation of a collection is the wiki part of the repository (R-ANS-033, R-ANS-037): who may read the artifacts of a
// repository may not be who may read its wiki. The rule is the wiki check of R-AUTHZ-034, which task T-18 builds and owns as
// Host.WikiReadable; this file is the one place the format meets it, so that every surface asks through [wikiReadable] and the day the
// check exists only this file changes.
//
// The sites that change with it, one line each: wikiCheck below (becomes rq.Host.WikiReadable); the Route.Wiki field of routeDocs in
// routes.go; the marker x-trove-part: wiki of the three operations in api/openapi.yaml (already set); and, when the version details
// announce the documentation, ViewData.WikiMetadata = []string{"docs"}.

// wikiReader has the signature of Host.WikiReadable: whether the subject of rq may read the wiki part of the repository rq addresses,
// for the target t. A handler that gets false answers as an absent resource (R-ANS-037), or leaves the wiki member out of a document whose
// artifacts it serves.
type wikiReader func(ctx context.Context, rq *formats.Request, t formats.Target) bool

// wikiCheck is the check [wikiReadable] calls. It is the stand-in of the repository read decision until Host.WikiReadable exists: whoever
// may read the target may read its documentation, which is the behaviour of a repository whose wiki follows its artifacts (H-894). A test
// replaces it with a denying fake and restores it.
//
// replaced by the wiki check of R-AUTHZ-034
var wikiCheck wikiReader = repositoryReadable

// repositoryReadable is the read decision of the request's subject for the target.
func repositoryReadable(ctx context.Context, rq *formats.Request, t formats.Target) bool {
	t.Actions = []formats.Action{formats.ActionRead}
	return rq.Host.Authorize(ctx, rq, t) == nil
}

// wikiReadable asks the wiki check for the target. Every surface that serves a collection's documentation calls it before it reads a row
// or a stored document (R-ANS-037).
func wikiReadable(ctx context.Context, rq *formats.Request, t formats.Target) bool {
	return wikiCheck(ctx, rq, t)
}

// wikiSurfaces is the registry of the surfaces of this format that are wiki content: the route names listed by R-ANS-037. A test walks it,
// so a surface that is added without asking the check is found.
var wikiSurfaces = struct {
	mu    sync.Mutex
	names map[string]bool
}{names: map[string]bool{}}

// registerWikiSurface lists a surface as wiki content; it must be called from init. A name registered twice is a programming error.
func registerWikiSurface(name string) {
	wikiSurfaces.mu.Lock()
	defer wikiSurfaces.mu.Unlock()
	if wikiSurfaces.names[name] {
		panic("ansible: the wiki surface " + name + " is registered twice")
	}
	wikiSurfaces.names[name] = true
}

// registeredWikiSurfaces lists the registered surfaces in name order.
func registeredWikiSurfaces() []string {
	wikiSurfaces.mu.Lock()
	defer wikiSurfaces.mu.Unlock()
	out := make([]string, 0, len(wikiSurfaces.names))
	for n := range wikiSurfaces.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() { registerWikiSurface(routeDocs) }
