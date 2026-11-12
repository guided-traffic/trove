package ansible

import (
	"context"
	"sort"
	"sync"

	"github.com/guided-traffic/trove/internal/formats"
)

// The documentation of a collection is the wiki part of the repository (R-ANS-033, R-ANS-037, R-AUTHZ-034): who may read the artifacts of a
// repository may not be who may read its wiki. The rule is the wiki check of the decision point, which the Host offers as
// Host.WikiReadable; this file is the one place the format meets it, so that every surface asks through [wikiReadable].
//
// The sites that ask it, one line each: the route docs-blob is declared Wiki in routes.go, so the dispatcher refuses a caller who may
// not read the wiki before the handler runs; the handlers ask [wikiReadable] again for the repository they read from (a member of a
// virtual repository); the management operations of internal/api/v1 are marked x-trove-part: wiki in api/openapi.yaml and ask
// authz.ReadScope.MayReadWiki; the version details announce the documentation only to a caller who may read it.

// wikiReadable asks the Host whether the subject of rq may read the wiki part of the repository rq addresses, for the target t. A
// handler that gets false answers as an absent resource (R-ANS-037), or leaves the wiki member out of a document whose artifacts it
// serves. It is a question: nothing is audited or counted. Every surface that serves a collection's documentation calls it before it
// reads a row or a stored document.
func wikiReadable(ctx context.Context, rq *formats.Request, t formats.Target) bool {
	return rq.Host.WikiReadable(ctx, rq, t)
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
