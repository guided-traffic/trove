package formathost

import (
	"context"
	"sort"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/packages/view"
)

// ViewAdapters adapts the view adapters of the registered formats
// (formats.Format.View) to the terms of the read model, which is a domain package
// and imports no format. The data ports are the ones the composition root bound.
type ViewAdapters struct {
	by      map[string]view.Adapter
	formats []string
}

var _ view.Adapters = (*ViewAdapters)(nil)

// NewViewAdapters collects the adapters of the given formats; a format without one
// has no package screens and is absent.
func NewViewAdapters(fs []formats.Format, ports formats.DataPorts) *ViewAdapters {
	v := &ViewAdapters{by: map[string]view.Adapter{}}
	for _, f := range fs {
		if a := f.View(); a != nil {
			id := string(f.Descriptor().ID)
			v.by[id] = viewBridge{a: a, ports: ports}
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

type viewBridge struct {
	a     formats.ViewAdapter
	ports formats.DataPorts
}

func (b viewBridge) Compare(x, y string) int  { return b.a.Compare(x, y) }
func (b viewBridge) Prerelease(v string) bool { return b.a.Prerelease(v) }

func (b viewBridge) Describe(ctx context.Context, vs []view.Version, want view.Fields, visible func(string) bool) (map[int64]view.Data, error) {
	in := make([]formats.ViewVersion, len(vs))
	for i, v := range vs {
		in[i] = formats.ViewVersion{
			ID: formats.VersionID(v.ID), Repo: formats.RepoRef{Key: v.Repo, Format: formats.ID(v.Format)},
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
