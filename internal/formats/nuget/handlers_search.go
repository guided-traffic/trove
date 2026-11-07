package nuget

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/search"
)

// The search resource of local repositories (R-NUGET-010, R-AUTHZ-016). A virtual
// repository merges the searches of its members (search_virtual.go, R-NUGET-018); a remote
// one forwards the query to its upstream (search_remote.go, R-NUGET-016).

const (
	// searchScanBatch is the page a scan of a repository the caller reads row by row asks
	// the store for: keys only, no metadata.
	searchScanBatch = 500
	// maxSearchScan bounds the packages the scans of one request look at, all members of a
	// virtual repository together (Q-NUGET-76). A request that matches more answers 400 and
	// never a partial count: the scan is linear in the matches, and a caller with a narrow
	// grant on a large repository narrows the query (Q-NUGET-45). The bound is the one the
	// package listings use for the same decision (R-API-029).
	maxSearchScan = 200_000
)

// searchStoreOf finds the data port the replica was bound with.
func searchStoreOf(h formats.Host) (Store, error) {
	st, ok := h.FormatData(ID).(Store)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The NuGet data port is not bound.")
	}
	return st, nil
}

// handleSearch serves GET <feed>/search. Authorization ran the repository-level read of
// the route (R-ARCH-011 step 2); which packages and versions the caller may see is decided
// here, row by row, before anything is counted (R-AUTHZ-016), so totalHits is exact for what
// the caller may read.
func (f *Format) handleSearch(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	cfg := f.replicaOf(rq.Host).cfg.Search
	p, err := search.ParseParams(rq.URL.Query(), search.Limits{DefaultTake: cfg.DefaultTake, MaxTake: cfg.MaxTake, MaxSkip: cfg.MaxSkip})
	if err != nil {
		return err
	}
	st, err := searchStoreOf(rq.Host)
	if err != nil {
		return err
	}
	var (
		hits  []rows.SearchHit
		total int
	)
	if !p.NoMatch {
		run := f.runSearch
		switch rq.Repo.Class {
		case formats.Virtual:
			run = f.runVirtualSearch
		case formats.Remote:
			run = f.runRemoteSearch // the forwarded search, search_remote.go (R-NUGET-016)
		}
		if hits, total, err = run(ctx, rq, st, p); err != nil {
			return err
		}
	}
	feed := rq.Host.Links(rq).Registry(servingName(rq)) + FeedSegment + "/"
	body, err := search.Render(feed, p.SemVer2, total, hits)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", jsonContentType)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(body)
	return err
}

// runSearch returns the page of hits and the number of packages the caller may read that
// match. The shape of the caller's read decides the way: a repository read whole and without
// include or exclude patterns is counted and paged by the store; any other is scanned in rank
// order (Q-NUGET-45, Q-NUGET-77).
func (f *Format) runSearch(ctx context.Context, rq *formats.Request, st Store, p search.Params) (hits []rows.SearchHit, total int, err error) {
	key := rq.Repo.Name
	scope, err := rq.Host.Resolver().ReadScope(ctx, rq, []qname.Repo{key})
	if err != nil {
		return nil, 0, err
	}
	return searchRepo(ctx, st, scope, searchTarget{key: key, id: rq.RepoRef().ID, admits: patternsOf(rq.Repo)}, p, newScanBudget())
}

// searchTarget is one repository a search reads the rows of: the repository the client
// addressed, or a local member of the virtual repository the client addressed.
type searchTarget struct {
	key qname.Repo
	id  int64
	// admits is the test of the include and exclude patterns that apply to the IDs of the
	// search (R-REPO-005, Q-NUGET-77): the repository's own and, for a member, the virtual
	// repository's. nil when no pattern restricts.
	admits func(lowerID string) bool
}

// patterned is the part of a repository the pattern test reads; *repository.Repository has it.
type patterned interface {
	Restricts() bool
	Admits(path string, folder bool) bool
}

// patternsOf returns the test of the include and exclude patterns of the repositories: an ID
// is admitted when every one of them admits it. NuGet's patterns match the lower-case ID (the
// pattern subject is the name), as Authorize applies them to a document or a file. nil when
// no repository restricts.
func patternsOf(repos ...patterned) func(lowerID string) bool {
	var restricting []patterned
	for _, r := range repos {
		if r.Restricts() {
			restricting = append(restricting, r)
		}
	}
	if len(restricting) == 0 {
		return nil
	}
	return func(lowerID string) bool {
		for _, r := range restricting {
			if !r.Admits(lowerID, false) {
				return false
			}
		}
		return true
	}
}

// searchRepo is the search of one repository's rows for the caller whose read scope is given:
// the hits of the page p names and the number of packages the caller may read that match. A
// virtual repository runs it for each of its local members (R-NUGET-018), all of them on one
// budget. The store counts and pages only for a caller who reads the repository whole when no
// pattern applies; otherwise the patterns and the scope decide each version in the scan.
func searchRepo(ctx context.Context, st Store, scope formats.ReadScope, t searchTarget, p search.Params, budget *scanBudget) (hits []rows.SearchHit, total int, err error) {
	q := rows.SearchQuery{
		RepoID: t.id, Terms: p.Terms, Prerelease: p.Prerelease, SemVer2: p.SemVer2, PackageType: p.PackageType,
	}
	shape := scope.Shape(t.key)
	switch {
	case shape == formats.ReadNone:
		return nil, 0, nil
	case shape == formats.ReadAll && t.admits == nil:
		return searchWhole(ctx, st, q, p)
	}
	mayRead := func(lowerID, lowerVersion string) bool {
		if t.admits != nil && !t.admits(lowerID) {
			return false
		}
		return shape == formats.ReadAll || scope.MayRead(t.key, lowerID, lowerVersion)
	}
	return searchRows(ctx, st, q, p, mayRead, budget)
}

// searchWhole is the search of a caller who reads every package: the store counts and cuts
// the page.
func searchWhole(ctx context.Context, st Store, q rows.SearchQuery, p search.Params) ([]rows.SearchHit, int, error) {
	total, err := st.SearchCount(ctx, q)
	if err != nil || total == 0 || p.Take == 0 || p.Skip >= total {
		return nil, total, err
	}
	q.Offset, q.Limit = p.Skip, p.Take
	hits, err := st.Search(ctx, q)
	return hits, total, err
}

// scanBudget is the number of packages the scans of one request may still look at. Every batch
// a scan reads is taken from it, and a scan that goes beyond it answers 400 instead of a count
// that is partial. The scans of the members of a virtual repository share one, the cache-only
// search of a remote member included, so a request looks at no more than maxSearchScan packages
// whatever the number of its members (Q-NUGET-76).
type scanBudget struct{ left atomic.Int64 }

func newScanBudget() *scanBudget {
	b := &scanBudget{}
	b.left.Store(maxSearchScan)
	return b
}

// spend takes n packages from the budget and reports whether the budget still holds.
func (b *scanBudget) spend(n int) bool { return b.left.Add(-int64(n)) >= 0 }

// refund gives back what an attempt of a scan spent before its transaction started over.
func (b *scanBudget) refund(n int) { b.left.Add(int64(n)) }

// Scanner is what a store offers that reads the scan of a caller whose reads are decided per
// version as one statement in one transaction, in rank order, a batch at a time as the rows
// arrive (Q-NUGET-74): the repository is ranked once and every batch comes from one snapshot.
// start runs at the beginning of every attempt, because a retried transaction starts over;
// visit gets each batch, which it must not keep, and returns false to stop. The PostgreSQL store
// implements it; a store without it is scanned in batches of Search with PerVersion.
type Scanner interface {
	SearchScan(ctx context.Context, q rows.SearchQuery, batch int, start func(), visit func([]rows.SearchHit) (bool, error)) error
}

// errScanTooLarge is the answer to a query that matches more packages than the budget.
func errScanTooLarge() error {
	return errs.E(errs.Invalid, errs.CodeInvalid, "The query matches too many packages to count the ones you may read. Narrow the query.")
}

// windowEntry is a package of the requested page found by the scan: the listed versions the
// caller may read.
type windowEntry struct {
	lowerID  string
	versions []string
}

// searchRows is the search of a caller whose read depends on the package and the version.
// It walks the candidates in rank order a batch at a time, with the keys of every listed
// visible version and whether each satisfies the terms (PerVersion), and keeps a package when
// the caller's reads leave it a version, the latest of those satisfies the terms, and every
// version: term names one of those (Q-NUGET-74): the terms are applied to what the caller may
// read and never to a version the caller may not. The latest readable version is the one the
// package type filter and the result show. Only the entries of the requested page are kept, so
// the memory is bounded by take; the count is exact because the walk reaches the last
// candidate. The page is then read with the readable versions alone, so nothing of an
// unreadable version reaches the result.
func searchRows(ctx context.Context, st Store, q rows.SearchQuery, p search.Params, mayRead func(lowerID, lowerVersion string) bool, budget *scanBudget) ([]rows.SearchHit, int, error) {
	scan := q
	scan.PackageType, scan.KeysOnly, scan.PerVersion = "", true, true
	versionTerms := versionTermsOf(q.Terms)
	var (
		total, spent int
		window       []windowEntry
	)
	start := func() {
		budget.refund(spent)
		total, spent, window = 0, 0, nil
	}
	visit := func(batch []rows.SearchHit) (bool, error) {
		spent += len(batch)
		if !budget.spend(len(batch)) {
			return false, errScanTooLarge()
		}
		for _, h := range batch {
			readable, latest := readableVersions(h, mayRead)
			if latest < 0 || !matchedAt(h, latest) || !namesReadable(versionTerms, readable) ||
				(q.PackageType != "" && !slices.Contains(h.ListedTypes[latest], q.PackageType)) {
				continue
			}
			if total >= p.Skip && total < p.Skip+p.Take {
				window = append(window, windowEntry{lowerID: h.LowerID, versions: readable})
			}
			total++
		}
		return true, ctx.Err()
	}
	if err := scanAll(ctx, st, scan, start, visit); err != nil {
		return nil, 0, err
	}
	if len(window) == 0 {
		return nil, total, nil
	}
	var refs []rows.SearchVersion
	for _, e := range window {
		for _, v := range e.versions {
			refs = append(refs, rows.SearchVersion{LowerID: e.lowerID, LowerVersion: v})
		}
	}
	read, err := st.Search(ctx, rows.SearchQuery{RepoID: q.RepoID, Only: refs})
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[string]rows.SearchHit, len(read))
	for _, h := range read {
		byID[h.LowerID] = h
	}
	hits := make([]rows.SearchHit, 0, len(window))
	for _, e := range window {
		if h, ok := byID[e.lowerID]; ok { // a package removed since the scan drops out
			hits = append(hits, h)
		}
	}
	return hits, total, nil
}

// scanAll hands the scan of a query to visit a batch at a time: through one statement when the
// store is a Scanner, else through batches of Search paged by offset.
func scanAll(ctx context.Context, st Store, scan rows.SearchQuery, start func(), visit func([]rows.SearchHit) (bool, error)) error {
	if sc, ok := st.(Scanner); ok {
		return sc.SearchScan(ctx, scan, searchScanBatch, start, visit)
	}
	start()
	scan.Limit = searchScanBatch
	for scan.Offset = 0; ; scan.Offset += searchScanBatch {
		batch, err := st.Search(ctx, scan)
		if err != nil {
			return err
		}
		if more, err := visit(batch); err != nil || !more {
			return err
		}
		if len(batch) < searchScanBatch {
			return nil
		}
	}
}

// readableVersions returns the URL forms of the listed visible versions of a hit the caller
// may read, ascending, and the index in h.Listed of the highest of them, -1 for none.
func readableVersions(h rows.SearchHit, mayRead func(lowerID, lowerVersion string) bool) (versions []string, latest int) {
	latest = -1
	for i, v := range h.Listed {
		if mayRead(h.LowerID, v.LowerVersion) {
			versions = append(versions, v.LowerVersion)
			latest = i
		}
	}
	return versions, latest
}

// matchedAt reports whether the version at index i of a scan hit satisfies the terms but the
// version: terms. A hit without the flag matches nothing, so a store that does not answer the
// question lists nothing rather than everything.
func matchedAt(h rows.SearchHit, i int) bool { return i < len(h.ListedMatch) && h.ListedMatch[i] }

// versionTermsOf returns the values of the version: terms of a query: the URL forms of the
// versions they name, "" for a text that is no version.
func versionTermsOf(terms []rows.SearchTerm) []string {
	var out []string
	for _, t := range terms {
		if t.Field == search.FieldVersion {
			out = append(out, t.Value)
		}
	}
	return out
}

// namesReadable reports whether every version: term names one of the readable versions; a
// term that is no version names none (R-NUGET-010).
func namesReadable(versionTerms, readable []string) bool {
	for _, v := range versionTerms {
		if v == "" || !slices.Contains(readable, v) {
			return false
		}
	}
	return true
}
