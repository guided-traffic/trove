package nuget

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// MirrorPlan fills the documents of an ID that a restore reads, through the remote engine, and returns the .nupkg of
// every version of the version list (Q-NUGET-97, R-REPO-051): the version list (`{lower_id}/index.json`) and the
// registration index with the pages it does not inline. The versions come newest first by NuGet's SemVer 2.0.0
// precedence, each with the canonical path of its package, whether it carries a release label, and the size the
// registration announced where it announced one. Unlisted versions are in the list like listed ones: a lock file that
// pins one stays restorable. An ID the upstream does not have, or that is gone, is an empty plan.
func (remoteAdapter) MirrorPlan(ctx context.Context, u discover.Upstream, name string) (discover.Plan, error) {
	lower, ok := lowerID(name)
	if !ok {
		return discover.Plan{Name: name}, nil // no ID: nothing is requested for it
	}
	plan := discover.Plan{Name: lower}
	doc, found, err := u.Document(ctx, PackageDocumentPath(lower), documentVariant)
	if err != nil || !found {
		return plan, err
	}
	list, err := remote.VersionListBounded(doc, maxDiscoveryBytes, remote.MaxLeaves)
	if err != nil {
		return discover.Plan{}, err
	}
	sizes, err := announcedSizes(ctx, u, lower)
	if err != nil {
		return discover.Plan{}, err
	}
	type entry struct {
		v    version.Version
		item discover.Item
	}
	seen := map[string]bool{}
	var entries []entry
	for _, s := range list {
		v, err := version.Parse(s)
		if err != nil || seen[v.URLForm()] {
			continue // a string that is no version names no package
		}
		seen[v.URLForm()] = true
		entries = append(entries, entry{v: v, item: discover.Item{
			Version:    v.URLForm(),
			Path:       NupkgPath(lower, v.URLForm()),
			Size:       sizes[v.URLForm()],
			Prerelease: v.IsPrerelease(),
		}})
	}
	sort.SliceStable(entries, func(i, j int) bool { return version.Compare(entries[i].v, entries[j].v) > 0 })
	for _, e := range entries {
		plan.Items = append(plan.Items, e.item)
	}
	return plan, nil
}

// announcedSizes fills the registration index of the ID and the pages it does not inline and returns the package sizes
// its entries announce, by version in URL form. A registration the upstream does not have gives none: the plan stands on
// the version list, and the client's own request fills the registration later.
func announcedSizes(ctx context.Context, u discover.Upstream, lower string) (map[string]int64, error) {
	sizes := map[string]int64{}
	doc, found, err := u.Document(ctx, RegistrationIndexPath(lower), documentVariant)
	if err != nil || !found {
		return sizes, err
	}
	budget := remote.NewBudget(maxDiscoveryBytes) // one budget for the index and its pages, as a rendering has (Q-NUGET-83)
	idx, err := budget.Index(doc)
	if err != nil {
		return nil, err
	}
	for _, pg := range idx.Pages {
		leaves := pg.Leaves
		if !pg.Inline {
			lo, ok1 := urlForm(pg.Lower)
			hi, ok2 := urlForm(pg.Upper)
			if !ok1 || !ok2 {
				return nil, invalidDocument("A page of the upstream's registration index has bounds that are no versions.")
			}
			pageDoc, found, err := u.Document(ctx, RegistrationPagePath(lower, lo, hi), documentVariant)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			if leaves, err = budget.Page(pageDoc); err != nil {
				return nil, err
			}
		}
		for _, l := range leaves {
			var s string
			var size int64
			if json.Unmarshal(l.Entry["version"], &s) != nil || json.Unmarshal(l.Entry["packageSize"], &size) != nil || size <= 0 {
				continue
			}
			if v, err := version.Parse(s); err == nil {
				sizes[v.URLForm()] = size
			}
		}
	}
	return sizes, nil
}
