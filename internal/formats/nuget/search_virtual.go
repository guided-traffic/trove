package nuget

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/search"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// Search through a virtual repository (R-NUGET-018): the result lists of the members that
// remain after resolution are merged. Every member is asked in parallel for the first
// min(skip + take, window) results of the request's filters; the merged list is cut to the
// page afterwards, so a page is computed again on every request and nothing is kept between
// two (R-NUGET-030).

// defaultVirtualMergeWindow is nuget.search.virtual_merge_window when a replica was not
// bound with the configuration.
const defaultVirtualMergeWindow = 1000

// maxMemberSearches bounds the members one search through a virtual repository asks at the
// same time (Q-NUGET-76): a local member holds a database connection for each batch of its
// scan, a remote member an upstream query. Four of the pool's default twenty connections
// leave the other requests of the replica theirs, whatever the number of members.
const maxMemberSearches = 4

// memberResult is the answer of one member: its first hits in its own order and the number
// of packages it counts.
type memberResult struct {
	member formats.PlannedMember
	hits   []rows.SearchHit
	total  int
	err    error
}

// runVirtualSearch is runSearch for a virtual repository. The members are those of
// Resolver.Plan, which leaves out every member the caller cannot read; the rows of a local
// member are kept by the caller's read scope (R-AUTHZ-016, R-NUGET-013), which decides each
// row as a read through the virtual repository: its exposure rules and the lift of a token
// scope that names it apply, as they do to its documents (Q-EP-11, Q-AUTHZ-163, Q-NUGET-75).
// The include and exclude patterns of the virtual repository and of each member apply to the
// IDs that member contributes (R-REPO-005, Q-NUGET-77). At most maxMemberSearches members are
// asked at once, and the scans of the members share one budget (Q-NUGET-76). Remote
// members are asked by the forwarded search of R-NUGET-016, which joins at
// [Format.searchRemote].
func (f *Format) runVirtualSearch(ctx context.Context, rq *formats.Request, st Store, p search.Params) ([]rows.SearchHit, int, error) {
	plan, err := rq.Host.Resolver().Plan(ctx, rq, formats.Path{}, formats.PathInfo{Kind: formats.PathVolatile})
	if err != nil {
		return nil, 0, err
	}
	if !plan.Admitted || len(plan.Members) == 0 {
		return nil, 0, nil
	}
	window := f.replicaOf(rq.Host).cfg.Search.VirtualMergeWindow
	if window <= 0 {
		window = defaultVirtualMergeWindow
	}
	admits, err := memberPatterns(rq, plan)
	if err != nil {
		return nil, 0, err
	}
	var keys []string
	for _, m := range plan.Members {
		if m.Tier == formats.TierLocal {
			keys = append(keys, m.Member.Key)
		}
	}
	var scope formats.ReadScope
	if len(keys) > 0 {
		if scope, err = rq.Host.Resolver().ReadScope(ctx, rq, keys); err != nil {
			return nil, 0, err
		}
	}
	// The members are asked for the window the page needs; a page beyond the window needs
	// no hit, and the members only count (Take 0).
	mp := p
	mp.Skip, mp.Take = 0, min(p.Skip+p.Take, window)
	if p.Skip >= window {
		mp.Take = 0
	}

	results := make([]memberResult, len(plan.Members))
	budget := newScanBudget()
	sem := make(chan struct{}, maxMemberSearches)
	var wg sync.WaitGroup
	for i, m := range plan.Members {
		results[i].member = m
		if err := ctx.Err(); err != nil { // a request that ended asks no further member
			results[i].err = err
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			results[i].err = ctx.Err()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if m.Tier == formats.TierLocal {
				t := searchTarget{key: m.Member.Key, id: m.Member.ID, admits: admits[m.Member.Key]}
				results[i].hits, results[i].total, results[i].err = searchRepo(ctx, st, scope, t, mp, budget)
				return
			}
			results[i].hits, results[i].total, results[i].err = f.searchRemote(ctx, rq, st, m, mp, admits[m.Member.Key], budget)
		}()
	}
	wg.Wait()

	view := viewOf(rq)
	removed := 0
	for i := range results {
		r := &results[i]
		if r.err != nil {
			return nil, 0, r.err
		}
		kept := r.hits[:0:0]
		for _, hit := range r.hits {
			// A local member's hits passed both filters in its scan; these are the filters of
			// what a remote member contributes, which no read scope decided.
			if a := admits[r.member.Member.Key]; a != nil && !a(hit.LowerID) {
				removed++
				continue
			}
			h, exposed := exposedHit(view, hit)
			if !exposed {
				removed++
				continue
			}
			away, err := rq.Host.Resolver().KeepsAway(ctx, rq, r.member.Member, h.LowerID)
			if err != nil {
				return nil, 0, err
			}
			if away {
				removed++
				continue
			}
			kept = append(kept, h)
		}
		r.hits = kept
	}
	merged, duplicates := mergeHits(results, exactIDs(p.Terms))
	total := 0
	for _, r := range results {
		total += r.total
	}
	// totalHits is an estimate (R-NUGET-018 step 6): the members' counts minus what the
	// window showed to be a duplicate or a removal; it is never below what was found.
	total = max(total-removed-duplicates, len(merged))
	if p.Skip >= window || p.Skip >= len(merged) {
		return nil, total, nil
	}
	return merged[p.Skip:min(p.Skip+p.Take, len(merged), window)], total, nil
}

// memberPatterns returns, for each planned member, the test of the include and exclude
// patterns that apply to the IDs it contributes: the member's own and the virtual
// repository's, nil when neither restricts (R-REPO-005, Q-NUGET-77). A search has no name, so
// the plan did not apply them. The patterns are read from the virtual repository's own member
// list; a planned member that is not on it is a fault of the plan.
func memberPatterns(rq *formats.Request, plan *formats.VirtualPlan) (map[string]func(lowerID string) bool, error) {
	all := rq.Repo.Members()
	out := make(map[string]func(string) bool, len(plan.Members))
	for _, m := range plan.Members {
		found := false
		for j := range all {
			if all[j].Key == m.Member.Key {
				out[m.Member.Key], found = patternsOf(&all[j], rq.Repo), true
				break
			}
		}
		if !found {
			return nil, errs.E(errs.Internal, errs.CodeInternal, "A planned member is not a member of the virtual repository.",
				errs.WithCause(fmt.Errorf("%q is not a member of %q", m.Member.Key, rq.Repo.Key)))
		}
	}
	return out, nil
}

// exposedHit narrows a member's hit to what the endpoint exposes of the virtual repository the
// client addressed (R-NUGET-034, Q-NUGET-75). A local member's hit holds only versions its read
// scope admitted, which decided them under the same rules, so nothing changes there; this is
// the filter of what a remote member contributes. The versions outside the view are dropped,
// and a hit whose latest version is outside it is dropped whole, because the package-level
// fields of the result are that version's.
func exposedHit(view formats.ExposureView, h rows.SearchHit) (rows.SearchHit, bool) {
	if view == nil {
		return h, true
	}
	if !view.Package(h.LowerID) || !view.Version(h.LowerID, h.Latest.LowerVersion) {
		return h, false
	}
	out := h
	out.Listed, out.ListedTypes, out.ListedMatch = nil, nil, nil
	for i, v := range h.Listed {
		if !view.Version(h.LowerID, v.LowerVersion) {
			continue
		}
		var types []string
		if i < len(h.ListedTypes) {
			types = h.ListedTypes[i]
		}
		out.Listed, out.ListedTypes = append(out.Listed, v), append(out.ListedTypes, types)
	}
	// A forwarded hit counts the downloads of the versions it shows, as readableOnly does for
	// the versions a caller may read (Q-NUGET-78).
	if fw := h.Latest.Forwarded; fw != nil && len(out.Listed) < len(h.Listed) {
		shown := *fw
		shown.TotalDownloads = 0
		for _, v := range out.Listed {
			shown.TotalDownloads += v.Downloads
		}
		out.Latest.Forwarded = &shown
	}
	return out, true
}

// searchRemote is the search of a remote member: its forwarded search of R-NUGET-016 with the
// withheld-query rule of this virtual repository, or its cache-only search (search_remote.go).
// The cache-only search scans with the member's and the virtual repository's patterns (admits)
// on the request's budget, as a local member does, so its count is exact; a forwarded count is
// the upstream's less what the page removed (H-346, H-347).
func (f *Format) searchRemote(ctx context.Context, rq *formats.Request, st Store, m formats.PlannedMember, p search.Params, admits func(string) bool, budget *scanBudget) ([]rows.SearchHit, int, error) {
	return f.searchRemoteMember(ctx, rq, st, m, p, admits, budget)
}

// exactIDs are the values of the terms that name an ID whole: a free-text word or an id: /
// packageid: term. A package whose ID equals one of them comes first (R-NUGET-018 step 3).
func exactIDs(terms []rows.SearchTerm) map[string]bool {
	out := map[string]bool{}
	for _, t := range terms {
		if t.Field == "" || t.Field == search.FieldID || t.Field == search.FieldPackageID {
			out[t.Value] = true
		}
	}
	return out
}

// mergeHits merges the members' hits in the order of R-NUGET-018: the packages whose ID is
// exactly a term, from any member, then the others member by member in resolution order,
// each member's own order kept. A package of several members becomes one hit with the union
// of the versions; duplicates counts the hits that merged into another.
func mergeHits(results []memberResult, exact map[string]bool) (out []rows.SearchHit, duplicates int) {
	byID := map[string]int{}
	var all []rows.SearchHit
	for _, r := range results {
		for _, h := range r.hits {
			if i, ok := byID[h.LowerID]; ok {
				all[i] = unionHit(all[i], h)
				duplicates++
				continue
			}
			byID[h.LowerID] = len(all)
			all = append(all, h)
		}
	}
	out = make([]rows.SearchHit, 0, len(all))
	for _, h := range all {
		if exact[h.LowerID] {
			out = append(out, h)
		}
	}
	for _, h := range all {
		if !exact[h.LowerID] {
			out = append(out, h)
		}
	}
	return out, duplicates
}

// unionHit merges a hit of a later member into the hit of an earlier one: the versions are
// the union, for equal versions the earlier member's wins (the member tier resolution would
// pick), and the package-level fields come from the member that holds the highest version
// (R-NUGET-018 step 4). Downloads are not counted, so the sum of them is 0 as well.
func unionHit(a, b rows.SearchHit) rows.SearchHit {
	type listed struct {
		entry rows.VersionEntry
		types []string
	}
	seen := map[string]bool{}
	var vs []listed
	for _, h := range []rows.SearchHit{a, b} {
		for i, v := range h.Listed {
			if seen[v.LowerVersion] {
				continue
			}
			seen[v.LowerVersion] = true
			l := listed{entry: v}
			if i < len(h.ListedTypes) {
				l.types = h.ListedTypes[i]
			}
			vs = append(vs, l)
		}
	}
	slices.SortStableFunc(vs, func(x, y listed) int { return compareVersions(x.entry.LowerVersion, y.entry.LowerVersion) })
	out := a
	if compareVersions(b.Latest.LowerVersion, a.Latest.LowerVersion) > 0 {
		out.DisplayName, out.Latest = b.DisplayName, b.Latest
	}
	out.Listed, out.ListedTypes = make([]rows.VersionEntry, len(vs)), make([][]string, len(vs))
	for i, l := range vs {
		out.Listed[i], out.ListedTypes[i] = l.entry, l.types
	}
	return out
}

// compareVersions orders two URL-form versions by precedence; a text that is no version
// sorts first, which a stored version never is.
func compareVersions(a, b string) int {
	va, ea := version.Parse(a)
	vb, eb := version.Parse(b)
	switch {
	case ea != nil && eb != nil:
		return 0
	case ea != nil:
		return -1
	case eb != nil:
		return 1
	}
	return version.Compare(va, vb)
}
