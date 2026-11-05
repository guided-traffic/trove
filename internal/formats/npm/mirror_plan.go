package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// MirrorPlan fills the full package document of the package through the engine's upstream (variant full, R-NPM-024) and
// returns its versions newest first by semver precedence (R-REPO-051, the section "Mirror discovery" of npm.md): each
// with the canonical path of its tarball (R-NPM-025), the size the document announces where it has a real tarball size,
// whether it is a prerelease and whether it is the version of the `latest` dist-tag, which counts whatever the version
// bounds say (Q-NPM-71).
//
// The document lists the versions the upstream lists now, so a version removed upstream (R-NPM-026) is not in it and not
// in the plan; a package the upstream does not have, or withdrew, is an empty plan. A version whose text is not the
// canonical form of R-NPM-015 is left out as well: no client can request its tarball under a canonical path.
func (remoteAdapter) MirrorPlan(ctx context.Context, u discover.Upstream, pkg string) (discover.Plan, error) {
	n, err := name.Parse(pkg)
	if err != nil {
		return discover.Plan{}, err
	}
	plan := discover.Plan{Name: n.String()}
	doc, found, err := u.Document(ctx, n.String(), documentVariant)
	if err != nil {
		return discover.Plan{}, err
	}
	if !found {
		return plan, nil
	}
	parsed, err := remote.Parse(doc)
	if err != nil {
		return discover.Plan{}, fmt.Errorf("npm mirror: the document of %s: %w", n, err)
	}

	type entry struct {
		text string
		v    semver.Version
	}
	var versions []entry
	for text := range parsed.Versions {
		v, err := semver.Parse(text)
		if err != nil || v.Canonical() != text {
			continue
		}
		versions = append(versions, entry{text, v})
	}
	sort.Slice(versions, func(i, j int) bool {
		if c := semver.CompareBuild(versions[i].v, versions[j].v); c != 0 {
			return c > 0
		}
		return versions[i].text > versions[j].text
	})

	latest := ""
	if tag, ok := parsed.Tags()["latest"]; ok {
		if v, err := semver.Parse(tag); err == nil {
			latest = v.Canonical()
		}
	}
	sizes := attachmentSizes(parsed.Top["_attachments"])
	for _, e := range versions {
		plan.Items = append(plan.Items, discover.Item{
			Version:    e.text,
			Path:       TarballPath(n, e.text),
			Size:       sizes[TarballFile(n, e.text)],
			Prerelease: e.v.IsPrerelease(),
			Pinned:     e.text == latest,
		})
	}
	return plan, nil
}

// attachmentSizes reads the sizes a document announces for its tarballs: the `length` of the `_attachments` entries of
// the registry's database form, which is the byte size of the tarball (Q-NPM-74). `dist.unpackedSize` is the size of the
// unpacked files, not of the tarball, and is never used; a document without `_attachments`, as the public registry's is
// today, announces no size and the item's size is 0.
func attachmentSizes(raw json.RawMessage) map[string]int64 {
	var m map[string]struct {
		Length json.Number `json:"length"`
	}
	out := map[string]int64{}
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return out
	}
	for file, a := range m {
		if n, err := a.Length.Int64(); err == nil && n > 0 {
			out[file] = n
		}
	}
	return out
}
