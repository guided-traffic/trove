package formathost

import (
	"context"
	"fmt"
	"github.com/guided-traffic/trove/internal/qname"
	"sort"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/packages/view"
)

// ViewAdapters adapts the view adapters of the registered formats
// (formats.Format.View) to the terms of the read model, which is a domain package
// and imports no format, and to the source of stored versions of the exposure
// check (authz.StoredPackages). The data ports are the ones the composition root
// bound.
type ViewAdapters struct {
	by      map[string]view.Adapter
	bridges map[string]viewBridge
	formats []string
}

var (
	_ view.Adapters        = (*ViewAdapters)(nil)
	_ authz.StoredPackages = (*ViewAdapters)(nil)
)

// NewViewAdapters collects the adapters of the given formats; a format without one
// has no package screens and is absent.
func NewViewAdapters(fs []formats.Format, ports formats.DataPorts) *ViewAdapters {
	v := &ViewAdapters{by: map[string]view.Adapter{}, bridges: map[string]viewBridge{}}
	for _, f := range fs {
		if a := f.View(); a != nil {
			id := string(f.Descriptor().ID)
			b := viewBridge{a: a, ports: ports, pre: prereleaseOf(f)}
			v.by[id], v.bridges[id] = b, b
			v.formats = append(v.formats, id)
		}
	}
	sort.Strings(v.formats)
	return v
}

// For returns the adapter of a format.
func (v *ViewAdapters) For(format string) (view.Adapter, bool) {
	a, ok := v.by[format]
	return a, ok
}

// Formats lists the formats that have an adapter, sorted.
func (v *ViewAdapters) Formats() []string { return append([]string(nil), v.formats...) }

// Stored implements [authz.StoredPackages]: the view adapter of the repository's format says what a
// local or a remote repository stores of the package (R-EP-016, Q-EP-61). A virtual repository
// stores nothing of its own and a format without a view adapter lists nothing: both answer
// unlisted, which the check turns into the warning stored_versions_not_listed.
func (v *ViewAdapters) Stored(ctx context.Context, repo authz.StoredRepo, pkg string, limit int, withTags bool) (authz.StoredPackage, error) {
	b, ok := v.bridges[repo.Format]
	class := formats.Class(repo.Class)
	if !ok || (class != formats.Local && class != formats.Remote) {
		return authz.StoredPackage{}, nil
	}
	st, err := b.a.Stored(ctx, b.ports, formats.StoredQuery{
		Repo:    formats.RepoRef{ID: repo.ID, Name: repo.Name, Class: class, Format: formats.ID(repo.Format)},
		Package: pkg, Limit: limit, Tags: withTags,
	})
	if err != nil {
		return authz.StoredPackage{}, fmt.Errorf("stored versions of %s in %s: %w", pkg, repo.Name, err)
	}
	return authz.StoredPackage{Listed: true, Versions: st.Versions, Tags: st.Tags}, nil
}

type viewBridge struct {
	a     formats.ViewAdapter
	ports formats.DataPorts
	pre   func(string) bool
}

// prereleaseOf derives "is a pre-release" for the screens from the format's exposure
// adapter, the one place a format says it; a format that is not exposable has none, and
// none of its versions is a pre-release.
func prereleaseOf(f formats.Format) func(string) bool {
	x := f.Exposure()
	if x == nil {
		return func(string) bool { return false }
	}
	return func(v string) bool {
		pre, ok := x.Prerelease(v)
		return ok && pre
	}
}

func (b viewBridge) Compare(x, y string) int  { return b.a.Compare(x, y) }
func (b viewBridge) Prerelease(v string) bool { return b.pre(v) }

func (b viewBridge) Describe(ctx context.Context, vs []view.Version, want view.Fields, visible func(string) bool) (map[int64]view.Data, error) {
	in := make([]formats.ViewVersion, len(vs))
	for i, v := range vs {
		in[i] = formats.ViewVersion{
			ID: formats.VersionID(v.ID), Repo: formats.RepoRef{Name: nameOf(v.Repo), Format: formats.ID(v.Format)},
			Package: v.Package, Version: v.Version,
		}
	}
	var f formats.ViewFields
	if want&view.FieldSummary != 0 {
		f |= formats.ViewSummary
	}
	if want&view.FieldDetails != 0 {
		f |= formats.ViewDetails
	}
	if want&view.FieldReadme != 0 {
		f |= formats.ViewReadme
	}
	got, err := b.a.Describe(ctx, b.ports, in, f, visible)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]view.Data, len(got))
	for id, d := range got {
		out[int64(id)] = view.Data{
			Description: d.Description, Keywords: d.Keywords, Licenses: d.Licenses, HasReadme: d.HasReadme,
			Metadata: d.Metadata, Readme: d.Readme, ReadmeFile: d.ReadmeFile,
		}
	}
	return out, nil
}

func (b viewBridge) Find(ctx context.Context, f view.Find, limit int) ([]int64, error) {
	ids, err := b.a.Find(ctx, b.ports, formats.ViewFind{Repos: f.Repos, Text: f.Text, Keyword: f.Keyword}, limit)
	return toInt64(ids), err
}

func (b viewBridge) Withheld(ctx context.Context, repos []string) ([]int64, error) {
	ids, err := b.a.Withheld(ctx, b.ports, repos)
	return toInt64(ids), err
}

func toInt64(in []formats.VersionID) []int64 {
	out := make([]int64, len(in))
	for i, id := range in {
		out[i] = int64(id)
	}
	return out
}

// nameOf reads the qualified name "<project>/<key>" the package views use as the repository key; a string that
// is none gives the zero name.
func nameOf(k string) qname.Repo {
	r, _ := qname.ParseRepo(k)
	return r
}
