package nuget

import (
	"context"
	"slices"
	"sync"

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
// member are kept by the caller's read scope (R-AUTHZ-016, R-NUGET-013). Remote members
// are asked by the forwarded search of R-NUGET-016, which joins at [Format.searchRemote].
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
	var wg sync.WaitGroup
	for i, m := range plan.Members {
		results[i].member = m
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m.Tier == formats.TierLocal {
				results[i].hits, results[i].total, results[i].err = searchRepo(ctx, st, scope, m.Member.Key, m.Member.ID, mp)
				return
			}
			results[i].hits, results[i].total, results[i].err = f.searchRemote(ctx, rq, m, mp)
		}()
	}
	wg.Wait()

	removed := 0
	for i := range results {
		r := &results[i]
		if r.err != nil {
			return nil, 0, r.err
		}
		kept := r.hits[:0:0]
		for _, h := range r.hits {
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

// searchRemote is the search of a remote member. The forwarded search of R-NUGET-016 is the
// work package that fills it; until a remote member is searched, it contributes nothing.
func (f *Format) searchRemote(context.Context, *formats.Request, formats.PlannedMember, search.Params) ([]rows.SearchHit, int, error) {
	return nil, 0, nil
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
