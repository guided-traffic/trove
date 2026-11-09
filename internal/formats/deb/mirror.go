package deb

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/formats/deb/version"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The mirror's discovery (R-REPO-050 to R-REPO-052; debian.md "Mirror discovery", R-DEB-024, Q-DEB-91 to Q-DEB-95). The remote adapter
// implements discover.Lister, so that the catalog fact Mirror is true and a remote repository of the format may run in mode mirror.
//
// An APT archive has no list of its suites: the operator names them (deb.mirror), and what a pass keeps is the release documents of
// those suites, the Packages (and Sources) indexes of the named components and architectures, the named translations, and the package
// files those indexes list. The first page of a pass runs the index step through the data port ([MirrorStore.MirrorSync]), which
// fetches the documents through the pass's upstream and parses the indexes into rows in the pass; the names and the plans are then
// read from those rows. Nothing here keeps state between calls, so every call is idempotent and the engine may repeat it after a
// drain or a crash.

var _ discover.Lister = remoteAdapter{}

const (
	// mirrorDefaultLimit is the page size of a request that names none.
	mirrorDefaultLimit = 1000
	// maxNoteBytes bounds the note of a page (the engine keeps 256 characters).
	maxNoteBytes = 240
)

// MirrorHosts is empty: the archive's documents and files are on the upstream origin.
func (remoteAdapter) MirrorHosts(*url.URL) []string { return nil }

// MirrorMethods reports what the upstream offers: listing, the Packages indexes of the scope, and names. An APT archive has no change
// feed, so no pass is incremental.
func (remoteAdapter) MirrorMethods(context.Context, discover.Upstream) ([]discover.Method, error) {
	return []discover.Method{discover.MethodListing, discover.MethodNames}, nil
}

// repositoryIDer is implemented by the upstream of a pass and of a background run: the repository the call is for.
type repositoryIDer interface{ RepositoryID() int64 }

func (a remoteAdapter) mirrorScope(u discover.Upstream) (MirrorStore, rows.MirrorScope, error) {
	st := a.f.mirrorStoreOfBound()
	ider, ok := u.(repositoryIDer)
	if st == nil || !ok {
		return nil, rows.MirrorScope{}, errs.E(errs.Internal, errs.CodeInternal, "The mirror of the Debian format has no data port or no repository to read.")
	}
	m := SettingsOf(u.Settings()).Mirror
	return st, rows.MirrorScope{
		RepoID: ider.RepositoryID(), Suites: m.Suites, Components: m.Components, Architectures: m.Architectures,
		Translations: m.Translations, Sources: m.Sources,
	}, nil
}

var errBadCursor = errors.New("the mirror cursor is not one this format wrote")

// MirrorNames returns the next page of names of a pass. The first page runs the index step; every page reads the names from the rows of
// the suites whose release a key verifies.
func (a remoteAdapter) MirrorNames(ctx context.Context, u discover.Upstream, rq discover.PageRequest) (discover.Page, error) {
	if rq.Method != discover.MethodListing && rq.Method != discover.MethodNames {
		return discover.Page{}, discover.ErrMethodUnavailable
	}
	st, sc, err := a.mirrorScope(u)
	if err != nil {
		return discover.Page{}, err
	}
	var c struct {
		Last string `json:"last"`
		Skip int    `json:"skip"`
	}
	if len(rq.Cursor) > 0 {
		if err := json.Unmarshal(rq.Cursor, &c); err != nil || c.Skip < 0 {
			return discover.Page{}, errBadCursor
		}
	}
	note := ""
	if len(rq.Cursor) == 0 {
		_, notes, err := st.MirrorSync(ctx, u, sc)
		if err != nil {
			return discover.Page{}, err
		}
		note = trimNote(strings.Join(notes, "; "))
	}
	limit := rq.Limit
	if limit <= 0 {
		limit = mirrorDefaultLimit
	}
	if rq.Method == discover.MethodNames {
		return mirrorExactNames(rq, c.Skip, limit, note), nil
	}
	raw, err := st.MirrorNames(ctx, sc, c.Last, limit)
	if err != nil {
		return discover.Page{}, err
	}
	page := discover.Page{Done: len(raw) < limit, Note: note}
	for _, n := range raw {
		if ValidPackage(n) && rq.Scope.Admits != nil && rq.Scope.Admits(n) {
			page.Names = append(page.Names, n)
		}
	}
	if !page.Done {
		page.Cursor, _ = json.Marshal(map[string]string{"last": raw[len(raw)-1]})
	}
	return page, nil
}

// mirrorExactNames returns the include patterns without a wildcard as package names, sorted, in pages of limit.
func mirrorExactNames(rq discover.PageRequest, skip, limit int, note string) discover.Page {
	var all []string
	for _, pattern := range rq.Scope.Include {
		name, wildcard := discover.LiteralPrefix(pattern)
		if wildcard || slices.Contains(all, name) || !ValidPackage(name) || rq.Scope.Admits == nil || !rq.Scope.Admits(name) {
			continue
		}
		all = append(all, name)
	}
	sort.Strings(all)
	from := min(skip, len(all))
	to := min(from+limit, len(all))
	page := discover.Page{Names: all[from:to], Done: to == len(all), Note: note}
	if !page.Done {
		page.Cursor, _ = json.Marshal(map[string]int{"skip": to})
	}
	return page
}

func trimNote(s string) string {
	if len(s) > maxNoteBytes {
		return s[:maxNoteBytes]
	}
	return s
}

// MirrorPlan returns the files of a package name: per version the package files of the mirrored architectures and of "all" and, with
// sources, the files of its source package, newest version first by dpkg order. A version counts once however many files it has; the
// digests reach the engine through the gate of R-DEB-022 from the same rows.
func (a remoteAdapter) MirrorPlan(ctx context.Context, u discover.Upstream, name string) (discover.Plan, error) {
	st, sc, err := a.mirrorScope(u)
	if err != nil {
		return discover.Plan{}, err
	}
	entries, err := st.MirrorEntries(ctx, sc, name)
	if err != nil {
		return discover.Plan{}, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if c := version.Compare(entries[i].Version, entries[j].Version); c != 0 {
			return c > 0
		}
		return entries[i].Path < entries[j].Path
	})
	plan := discover.Plan{Name: name}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		plan.Items = append(plan.Items, discover.Item{Version: e.Version, Path: e.Path, Size: e.Size})
	}
	return plan, nil
}
