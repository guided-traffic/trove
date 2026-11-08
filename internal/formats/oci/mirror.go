package oci

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The mirror's discovery (R-REPO-050, R-REPO-051; oci.md "Mirror discovery", Q-OCI-16 and Q-OCI-180 to Q-OCI-186). The remote adapter
// implements discover.Lister, so that the catalog fact Mirror is true and a remote repository of the format may run in mode mirror.
// The format sends no request of its own: the catalog and the tags lists come through Upstream.Get, the tag documents through
// Upstream.Document and the manifests an index names through Upstream.Artifact, all of which apply the egress rules, the credential
// policy, the token handshake and the throttling of the pass. Nothing here keeps state between calls, so every call is idempotent and
// the engine may repeat it after a drain or a crash.
//
// Files: mirror.go (the methods, the exact names), mirror_listing.go (the catalog), mirror_tags.go (the tags list and the choice of
// tags), mirror_plan.go (the plan of a name: tags, platforms, attestations).

var _ discover.Lister = remoteAdapter{}

const (
	// mirrorDefaultLimit is the page size of a request that names none.
	mirrorDefaultLimit = 1000
	// catalogPage and tagsPage are the n of a catalog and a tags list request (oci.md "Mirror discovery").
	catalogPage = 1000
	tagsPage    = 1000
	// maxTagPages bounds the pages of one tags list: MaxTaglistEntries tags in pages of tagsPage, and one more for the answer that
	// ends it.
	maxTagPages = MaxTaglistEntries/tagsPage + 1
	// maxIndexChildren bounds the child manifests a plan reads of one index (the manifest parser allows 10,000 descriptors, and each
	// selected child is a request).
	maxIndexChildren = 1000
)

// MirrorHosts is empty: the catalog and the tags lists are on the upstream origin, and the token realm and the blob hosts are the
// handshake's and the redirects' (R-OCI-021, R-OCI-023); the format adds no host of its own.
func (remoteAdapter) MirrorHosts(*url.URL) []string { return nil }

// MirrorMethods reports what the upstream offers: listing when it answers GET /v2/_catalog?n=1 with a repositories array, names
// always. The format has no feed (R-REPO-051, oci.md "Mirror discovery").
func (remoteAdapter) MirrorMethods(ctx context.Context, u discover.Upstream) ([]discover.Method, error) {
	offered, err := catalogOffered(ctx, u)
	if err != nil {
		return nil, err
	}
	if offered {
		return []discover.Method{discover.MethodListing, discover.MethodNames}, nil
	}
	return []discover.Method{discover.MethodNames}, nil
}

// MirrorNames returns the next page of names of a pass by the method asked for.
func (remoteAdapter) MirrorNames(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	switch rq.Method {
	case discover.MethodListing:
		return mirrorListing(ctx, u, rq)
	case discover.MethodNames:
		return mirrorExactNames(rq)
	}
	return discover.Page{}, discover.ErrMethodUnavailable
}

// admitted reports whether the scope admits a name that passed the name grammar. A scope without the check admits nothing: a name
// that was not checked is never returned.
func admitted(sc discover.Scope, name string) bool {
	return ValidName(name) && sc.Admits != nil && sc.Admits(name)
}

// limitOf is the number of names a page may hold.
func limitOf(rq discover.PageRequest) int {
	if rq.Limit > 0 {
		return rq.Limit
	}
	return mirrorDefaultLimit
}

// errBadCursor is the error of a cursor the format did not write.
var errBadCursor = errors.New("the mirror cursor is not one this format wrote")

// mirrorExactNames returns the include patterns without a wildcard as image names, sorted, in pages of Limit. The name is the one the
// pattern spells: on Docker Hub `nginx` stays `nginx` here and is mapped to `library/nginx` when its plan is made (R-OCI-025), so that
// the admission the engine applies again to every pending name keeps judging the spelling the operator wrote. It has no incremental
// pass: every pass is full, and the engine adds the names the repository holds.
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
		name, wildcard := discover.LiteralPrefix(pattern)
		if wildcard || seen[name] || !admitted(rq.Scope, name) {
			continue
		}
		seen[name] = true
		all = append(all, name)
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
