package nuget

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/search"
)

// The search resource of local repositories (R-NUGET-010, R-AUTHZ-016). Remote and
// virtual repositories join with their adapters (R-NUGET-016, R-NUGET-018).

const (
	// searchScanBatch is the page a scan of a repository the caller reads row by row asks
	// the store for: keys only, no metadata.
	searchScanBatch = 500
	// maxSearchScan bounds the packages one such scan looks at. A request that matches
	// more answers 400 and never a partial count: the scan is linear in the matches, and
	// a caller with a narrow grant on a large repository narrows the query (Q-NUGET-45).
	// The bound is the one the package listings use for the same decision (R-API-029).
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
		if hits, total, err = f.runSearch(ctx, rq, st, p); err != nil {
			return err
		}
	}
	feed := rq.Host.Links(rq).Registry(rq.Addressed()+servingSuffix(rq)) + FeedSegment + "/"
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
// match. The shape of the caller's read decides the way: a repository read whole is counted
// and paged by the store; one decided row by row is scanned in rank order (Q-NUGET-45).
func (f *Format) runSearch(ctx context.Context, rq *formats.Request, st Store, p search.Params) (hits []rows.SearchHit, total int, err error) {
	key := rq.Repo.Key
	scope, err := rq.Host.Resolver().ReadScope(ctx, rq, []string{key})
	if err != nil {
		return nil, 0, err
	}
	q := rows.SearchQuery{
		RepoID: rq.RepoRef().ID, Terms: p.Terms, Prerelease: p.Prerelease, SemVer2: p.SemVer2, PackageType: p.PackageType,
	}
	switch scope.Shape(key) {
	case formats.ReadAll:
		return searchWhole(ctx, st, q, p)
	case formats.ReadRows:
		return searchRows(ctx, st, q, p, func(lowerID, lowerVersion string) bool { return scope.MayRead(key, lowerID, lowerVersion) })
	}
	return nil, 0, nil
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

// windowEntry is a package of the requested page found by the scan: the listed versions the
// caller may read.
type windowEntry struct {
	lowerID  string
	versions []string
}

// searchRows is the search of a caller whose read depends on the package and the version.
// It walks the matches in rank order a batch at a time, with the keys of every listed
// visible version, and keeps those with a readable version; the latest of the readable ones
// is the version the package type filter and the result show. Only the entries of the
// requested page are kept, so the memory is bounded by take; the count is exact because the
// walk reaches the last match. The page is then read with the readable versions alone, so
// nothing of an unreadable version reaches the result.
func searchRows(ctx context.Context, st Store, q rows.SearchQuery, p search.Params, mayRead func(lowerID, lowerVersion string) bool) ([]rows.SearchHit, int, error) {
	scan := q
	scan.PackageType, scan.KeysOnly, scan.Limit = "", true, searchScanBatch
	var (
		total, scanned int
		window         []windowEntry
	)
	for scan.Offset = 0; ; scan.Offset += searchScanBatch {
		batch, err := st.Search(ctx, scan)
		if err != nil {
			return nil, 0, err
		}
		if scanned += len(batch); scanned > maxSearchScan {
			return nil, 0, errs.E(errs.Invalid, errs.CodeInvalid, "The query matches too many packages to count the ones you may read. Narrow the query.")
		}
		for _, h := range batch {
			readable, latest := readableVersions(h, mayRead)
			if latest < 0 || (q.PackageType != "" && !slices.Contains(h.ListedTypes[latest], q.PackageType)) {
				continue
			}
			if total >= p.Skip && total < p.Skip+p.Take {
				window = append(window, windowEntry{lowerID: h.LowerID, versions: readable})
			}
			total++
		}
		if len(batch) < searchScanBatch {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
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
