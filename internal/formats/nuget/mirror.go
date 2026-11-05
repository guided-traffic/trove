package nuget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The mirror's discovery (R-REPO-050, R-REPO-051; nuget.md "Mirror discovery", Q-NUGET-95 to Q-NUGET-105). NuGet's remote
// adapter implements discover.Lister, so that the catalog fact Mirror is true and a remote may run in mode mirror. The
// format sends no request of its own: the service index, the version lists and the registrations are documents of the
// repository and come through Upstream.Document, the catalog and the search through Upstream.Get, which applies the egress
// rules, the credential policy and the throttling of the pass. Nothing here keeps state between calls, so every call is
// idempotent and the engine may repeat it after a drain or a crash.
//
// Files: mirror.go (the methods, the exact IDs, the helpers), mirror_feed.go (the catalog), mirror_listing.go (the search),
// mirror_plan.go (the plan of an ID).

var _ discover.Lister = remoteAdapter{}

const (
	// mirrorDefaultLimit is the page size of a request that names none.
	mirrorDefaultLimit = 1000
	// searchTake is the take of a listing request: the most nuget.org's search answers (Q-NUGET-98).
	searchTake = 1000
	// maxDiscoveryBytes bounds a discovery body, asked of Upstream.Get and inflated within the same bound: the engine's bound for a
	// discovery document (proxy.DiscoveryDocumentMaxBytes, Q-REPO-152), which this package may not import (archtest). Naming it makes
	// the request independent of the engine's default, and the inflation of a gzip body, which the stored-document bound of the client
	// path would let reach 128 MiB, stops here (Q-NUGET-108). It is also the byte budget of a plan's registration (Q-NUGET-109).
	maxDiscoveryBytes = 32 << 20
	// maxNote is the longest note of a truncated page (discover.Page.Note).
	maxNote = 256
)

// MirrorHosts is empty: the catalog and the search are resources of the upstream's service index and are reached under the
// host rules of R-NUGET-014 (the upstream origin or an extraHosts entry), the version lists and registrations through
// the remote engine; the format adds no host of its own (Q-NUGET-99).
func (remoteAdapter) MirrorHosts(*url.URL) []string { return nil }

// MirrorMethods reports what the upstream's service index offers: feed with Catalog/3.0.0, listing with a search
// resource, names always (R-REPO-051, Q-NUGET-95).
func (remoteAdapter) MirrorMethods(ctx context.Context, u discover.Upstream) ([]discover.Method, error) {
	d, err := feedDiscovery(ctx, u)
	if err != nil {
		return nil, err
	}
	var out []discover.Method
	if d != nil && d.Catalog != nil {
		out = append(out, discover.MethodFeed)
	}
	if d != nil && d.Search != nil {
		out = append(out, discover.MethodListing)
	}
	return append(out, discover.MethodNames), nil
}

// MirrorNames returns the next page of names of a pass by the method asked for.
func (remoteAdapter) MirrorNames(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	switch rq.Method {
	case discover.MethodFeed:
		return mirrorFeed(ctx, u, rq)
	case discover.MethodListing:
		return mirrorListing(ctx, u, rq)
	case discover.MethodNames:
		return mirrorExactNames(rq)
	}
	return discover.Page{}, discover.ErrMethodUnavailable
}

// feedDiscovery reads the upstream's service index through the remote engine, as the remote does for a client. It is nil
// when the upstream has none.
func feedDiscovery(ctx context.Context, u discover.Upstream) (*remote.Discovery, error) {
	doc, found, err := u.Document(ctx, canonDiscovery+"/"+indexFile, documentVariant)
	if err != nil || !found {
		return nil, err
	}
	return remote.ParseDiscovery(doc)
}

// admitted normalizes an ID to the package name (R-NUGET-003) and reports whether the scope admits it. A scope without
// the check admits nothing: a name that was not checked is never returned.
func admitted(sc discover.Scope, raw string) (string, bool) {
	name, ok := lowerID(raw)
	if !ok || sc.Admits == nil || !sc.Admits(name) {
		return "", false
	}
	return name, true
}

// limitOf is the number of names a page may hold.
func limitOf(rq discover.PageRequest) int {
	if rq.Limit > 0 {
		return rq.Limit
	}
	return mirrorDefaultLimit
}

// fetchJSON reads one document of the upstream through Upstream.Get. A rate-limit answer is the error of Get, returned
// as it is.
func fetchJSON(ctx context.Context, u discover.Upstream, target *url.URL, what string) ([]byte, error) {
	resp, err := u.Get(ctx, target, "application/json", maxDiscoveryBytes)
	if err != nil {
		return nil, err
	}
	return fetchResponse(resp, what)
}

// fetchResponse judges an answer of Get: any other status than 200 is an upstream failure naming what was asked.
func fetchResponse(resp *discover.Response, what string) ([]byte, error) {
	if resp.Status != 200 {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, fmt.Sprintf("The upstream answered %d for its %s.", resp.Status, what))
	}
	return remote.Decode(resp.Body, maxDiscoveryBytes)
}

// invalidDocument is the error of a discovery document that is not what the resource promises.
func invalidDocument(detail string) error {
	return errs.E(errs.UpstreamFailed, errs.CodeNuGetInvalidUpstreamDocument, detail)
}

// errBadCursor is the error of a cursor the format did not write.
var errBadCursor = errors.New("the mirror cursor is not one this format wrote")

// mirrorExactNames returns the include patterns without a wildcard as IDs (Q-NUGET-95), sorted, in pages of Limit. It has
// no incremental pass: every pass is full, and the engine adds the IDs the repository holds.
func mirrorExactNames(rq discover.PageRequest) (discover.Page, error) {
	var c struct {
		Skip int `json:"skip"`
	}
	if len(rq.Cursor) > 0 {
		if err := json.Unmarshal(rq.Cursor, &c); err != nil || c.Skip < 0 {
			return discover.Page{}, errBadCursor
		}
	}
	seen := map[string]bool{}
	var all []string
	for _, pattern := range rq.Scope.Include {
		prefix, wildcard := discover.LiteralPrefix(pattern)
		if wildcard {
			continue
		}
		if name, ok := admitted(rq.Scope, prefix); ok && !seen[name] {
			seen[name] = true
			all = append(all, name)
		}
	}
	sort.Strings(all)
	from := min(c.Skip, len(all))
	to := min(from+limitOf(rq), len(all))
	page := discover.Page{Names: all[from:to], Done: to == len(all)}
	if !page.Done {
		page.Cursor, _ = json.Marshal(map[string]int{"skip": to})
	}
	return page, nil
}
