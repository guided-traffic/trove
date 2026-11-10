package ansible

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The mirror's discovery (R-ANS-021, R-REPO-050 to R-REPO-052; ansible.md "Mirror discovery", Q-ANS-16). The remote adapter implements
// discover.Lister, so that a remote may run in mode mirror: it keeps the collections its patterns select. The format sends no request
// of its own: the API root, the version lists and the version documents are documents of the repository and come through
// Upstream.Document (each fill records the digest its version announces, R-ANS-018), the collection index through Upstream.Get, which
// applies the egress rules, the credential policy and the throttling of the pass. Nothing here keeps state between calls, so every call
// is idempotent and the engine may repeat it after a drain or a crash. The role index is never read (R-ANS-020).

var _ discover.Lister = remoteAdapter{}

const (
	// indexPageSize is the page of the collection index the format asks for, the most the public Galaxy answers (R-ANS-021).
	indexPageSize = 100
	// mirrorDefaultLimit is the page size of a request that names none.
	mirrorDefaultLimit = 1000
	// maxIndexBytes bounds one page of the collection index: the engine's bound for a discovery document, which this package may not
	// import (archtest).
	maxIndexBytes = 32 << 20
	// maxIndexOffset bounds the walk of the index: the public Galaxy lists 4,685 collections, and a listing that goes on past a
	// million says the upstream does not end.
	maxIndexOffset = 1_000_000
)

// MirrorHosts is empty: the index is a resource of the upstream's API root and is reached under the host rules of R-ANS-016.
func (remoteAdapter) MirrorHosts(*url.URL) []string { return nil }

// MirrorMethods reports the methods of the Galaxy: listing, the collection index, and names, the literal include patterns. The Galaxy
// has no change feed (R-ANS-021).
func (remoteAdapter) MirrorMethods(context.Context, discover.Upstream) ([]discover.Method, error) {
	return []discover.Method{discover.MethodListing, discover.MethodNames}, nil
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

// admittedName normalizes "<ns>.<name>" and reports whether the scope admits it; a scope without the check admits nothing.
func admittedName(sc discover.Scope, ns, name string) (string, bool) {
	n, ok := normalizeName(ns + "." + name)
	if !ok || sc.Admits == nil || !sc.Admits(n) {
		return "", false
	}
	return n, true
}

func limitOf(rq discover.PageRequest) int {
	if rq.Limit > 0 {
		return rq.Limit
	}
	return mirrorDefaultLimit
}

// errBadCursor is the error of a cursor the format did not write.
var errBadCursor = errors.New("the mirror cursor is not one this format wrote")

// offsetCursor reads {"offset": n}.
func offsetCursor(c discover.Cursor) (int, error) {
	if len(c) == 0 {
		return 0, nil
	}
	var v struct {
		Offset int `json:"offset"`
	}
	if err := json.Unmarshal(c, &v); err != nil || v.Offset < 0 || v.Offset > maxIndexOffset {
		return 0, errBadCursor
	}
	return v.Offset, nil
}

func cursorAt(offset int) discover.Cursor {
	b, _ := json.Marshal(struct {
		Offset int `json:"offset"`
	}{offset})
	return b
}

// namePair splits "<ns>.<name>" at its dot; a string without one yields two empty strings, which no scope admits.
func namePair(s string) (string, string) {
	ns, name, _ := strings.Cut(s, ".")
	return ns, name
}

// mirrorExactNames returns the include patterns without a wildcard that read as "<ns>.<name>", sorted, in pages of Limit. It has no
// incremental pass: every pass is full, and the engine adds the names the repository holds.
func mirrorExactNames(rq discover.PageRequest) (discover.Page, error) {
	skip, err := offsetCursor(rq.Cursor)
	if err != nil {
		return discover.Page{}, err
	}
	seen := map[string]bool{}
	var all []string
	for _, p := range rq.Scope.Include {
		if _, wild := discover.LiteralPrefix(p); wild {
			continue
		}
		ns, nm := namePair(p)
		if n, ok := admittedName(rq.Scope, ns, nm); ok && !seen[n] {
			seen[n] = true
			all = append(all, n)
		}
	}
	sort.Strings(all)
	end := min(skip+limitOf(rq), len(all))
	page := discover.Page{Cursor: cursorAt(end), Done: end >= len(all)}
	if skip < len(all) {
		page.Names = all[skip:end]
	}
	return page, nil
}

// mirrorListing reads the collection index by offset, 100 entries a request, and returns the names the scope admits in the index's
// order. The cursor {"offset": n} is the index position after the last entry consumed, so a page that the limit cut resumes inside an
// upstream page. A pass is always full. Every request is a request of the pass: the engine throttles it, and a rate-limit answer is
// returned unchanged for the engine to wait and repeat the call.
func mirrorListing(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	offset, err := offsetCursor(rq.Cursor)
	if err != nil {
		return discover.Page{}, err
	}
	d, err := mirrorDiscovery(ctx, u)
	if err != nil {
		return discover.Page{}, err
	}
	limit := limitOf(rq)
	page := discover.Page{}
	done := false
	for len(page.Names) < limit && !done {
		if offset > maxIndexOffset {
			page.Truncated, page.Note = true, "the upstream's collection index goes on past "+strconv.Itoa(maxIndexOffset)+" entries; the pass stops there"
			done = true
			break
		}
		entries, count, err := indexPage(ctx, u, d, offset)
		if err != nil {
			return discover.Page{}, err
		}
		if len(entries) == 0 {
			done = true
			break
		}
		for i, e := range entries {
			if len(page.Names) == limit {
				// The limit cut the upstream's page: the cursor resumes at the first entry not consumed.
				page.Cursor = cursorAt(offset + i)
				return page, nil
			}
			if n, ok := admittedName(rq.Scope, e.ns, e.name); ok {
				page.Names = append(page.Names, n)
			}
		}
		offset += len(entries)
		done = offset >= count
	}
	page.Cursor, page.Done = cursorAt(offset), done
	return page, nil
}

type indexEntry struct{ ns, name string }

// mirrorDiscovery reads the API root through the remote engine: the stored document, as for a client.
func mirrorDiscovery(ctx context.Context, u discover.Upstream) (*discovery, error) {
	doc, found, err := u.Document(ctx, RootDocument, documentVariant)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream has no API root: its URL is not the API root of a Galaxy, which usually ends in /api/.")
	}
	return parseDiscovery(doc)
}

// indexPage reads one page of the collection index at offset.
func indexPage(ctx context.Context, u discover.Upstream, d *discovery, offset int) ([]indexEntry, int, error) {
	base := *u.Base()
	base.User, base.Fragment, base.RawFragment, base.RawQuery = nil, "", "", ""
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	target, err := url.Parse(base.String() + d.v3 + "collections/?limit=" + strconv.Itoa(indexPageSize) + "&offset=" + strconv.Itoa(offset))
	if err != nil {
		return nil, 0, errs.E(errs.Internal, errs.CodeInternal, "The index address does not build.", errs.WithCause(err))
	}
	resp, err := u.Get(ctx, target, "application/json", maxIndexBytes)
	if err != nil {
		return nil, 0, err
	}
	if resp.Status != 200 {
		return nil, 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream answered "+strconv.Itoa(resp.Status)+" for its collection index.")
	}
	var doc struct {
		Meta struct {
			Count int `json:"count"`
		} `json:"meta"`
		Data []struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"data"`
	}
	if len(resp.Body) > maxIndexBytes || json.Unmarshal(resp.Body, &doc) != nil || doc.Meta.Count < 0 {
		return nil, 0, invalidDocument("a collection index", "it is not a page of collections")
	}
	out := make([]indexEntry, 0, len(doc.Data))
	for _, e := range doc.Data {
		out = append(out, indexEntry{e.Namespace, e.Name})
	}
	return out, doc.Meta.Count, nil
}

// MirrorPlan reads the version lists and the version documents of a collection through the remote engine and returns its archives,
// newest first by precedence. The documents of the versions the engine keeps are read (each fill records the announced digest); the
// versions it would drop (the version bounds) are left out before their documents are read. A collection the upstream does not have is
// an empty plan.
func (remoteAdapter) MirrorPlan(ctx context.Context, u discover.Upstream, name string) (discover.Plan, error) {
	ns, n, ok := collection.SplitName(name)
	if !ok {
		return discover.Plan{Name: name}, nil
	}
	versions, found, err := mirrorVersions(ctx, u, ns, n)
	if err != nil || !found {
		return discover.Plan{Name: name}, err
	}
	type pv struct {
		raw string
		v   version.Semver
	}
	var vs []pv
	for _, e := range versions {
		if sv, err := version.Parse(e); err == nil {
			vs = append(vs, pv{e, sv})
		}
	}
	sort.SliceStable(vs, func(i, j int) bool { return version.Compare(vs[i].v, vs[j].v) > 0 })
	maxVersions, includePre := discover.BoundsOf(u)
	plan := discover.Plan{Name: name}
	kept := 0
	for _, p := range vs {
		pre := len(p.v.Pre) > 0
		if pre && !includePre {
			continue
		}
		if maxVersions > 0 && kept >= maxVersions {
			continue
		}
		kept++
		doc, found, err := u.Document(ctx, VersionDocumentPath(ns, n, p.raw), documentVariant)
		if err != nil {
			return discover.Plan{}, err
		}
		if !found {
			continue
		}
		v, _, err := galaxydoc.ParseUpstreamVersion(doc)
		if err != nil {
			return discover.Plan{}, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream sent a version document that trove cannot use: "+err.Error()+".", errs.WithCause(err))
		}
		plan.Items = append(plan.Items, discover.Item{Version: p.raw, Path: ArtifactPath(ns, n, p.raw), Size: v.Size, Prerelease: pre})
	}
	return plan, nil
}

// mirrorVersions reads the version list of a collection page by page through the stored documents; found is false when the
// upstream has no such collection.
func mirrorVersions(ctx context.Context, u discover.Upstream, ns, name string) (list []string, found bool, err error) {
	path := VersionsDocumentPath(ns, name)
	first, found, err := u.Document(ctx, path, offsetVariant(0))
	if err != nil || !found {
		return nil, false, err
	}
	head, err := parsePage(first)
	if err != nil {
		return nil, false, err
	}
	for _, e := range head.entries {
		list = append(list, e.Version)
	}
	size := upstreamListLimit
	if head.count > len(head.entries) && len(head.entries) > 0 {
		size = len(head.entries)
	}
	for start := size; start < head.count; start += size {
		doc, ok, err := u.Document(ctx, path, offsetVariant(start))
		if err != nil {
			return nil, false, err
		}
		if !ok {
			break
		}
		p, err := parsePage(doc)
		if err != nil {
			return nil, false, err
		}
		for _, e := range p.entries {
			list = append(list, e.Version)
		}
	}
	return list, true, nil
}
