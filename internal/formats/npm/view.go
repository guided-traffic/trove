package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// viewPort is the part of the data port the package view reads (R-API-029): the
// statements are in internal/formatdata/npm/view.go. It is a separate interface
// because the contract's DataPort (Store) is the port of the publish and the
// protocol; the same bound value implements both.
type viewPort interface {
	ViewSummaries(ctx context.Context, ids []int64) ([]rows.ViewSummary, error)
	ViewDetails(ctx context.Context, ids []int64, withReadme bool) ([]rows.ViewDetail, error)
	ViewFind(ctx context.Context, repos []string, text, keyword string, limit int) ([]int64, error)
	ViewWithheld(ctx context.Context, repos []string) ([]int64, error)
}

// View returns the format's part of the package screens (R-API-029).
func (f *Format) View() formats.ViewAdapter { return viewAdapter{} }

type viewAdapter struct{}

var _ formats.ViewAdapter = viewAdapter{}

func portOf(ports formats.DataPorts) (viewPort, error) {
	if ports != nil {
		if p, ok := ports.FormatData(ID).(viewPort); ok {
			return p, nil
		}
	}
	return nil, errs.E(errs.Internal, errs.CodeInternal, "npm: the data port has no package view")
}

// Compare orders versions by SemVer precedence, build metadata included as a last
// resort so that two different strings never compare equal. A string that is no
// version sorts before every version.
func (viewAdapter) Compare(a, b string) int {
	va, ea := semver.Parse(a)
	vb, eb := semver.Parse(b)
	switch {
	case ea != nil && eb != nil:
		return strings.Compare(a, b)
	case ea != nil:
		return -1
	case eb != nil:
		return 1
	}
	if c := semver.CompareBuild(va, vb); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// Prerelease reports whether the version has a pre-release tag.
func (viewAdapter) Prerelease(version string) bool {
	v, err := semver.Parse(version)
	return err == nil && v.IsPrerelease()
}

// Describe reads the manifests of the versions. The summary fields come from the
// manifest; the details add the licenses, the README flag and the metadata members
// of R-API-029.
func (viewAdapter) Describe(ctx context.Context, ports formats.DataPorts, vs []formats.ViewVersion, want formats.ViewFields, visible func(string) bool) (map[formats.VersionID]formats.ViewData, error) {
	p, err := portOf(ports)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(vs))
	byID := make(map[int64]formats.ViewVersion, len(vs))
	for i, v := range vs {
		ids[i] = int64(v.ID)
		byID[int64(v.ID)] = v
	}
	out := make(map[formats.VersionID]formats.ViewData, len(vs))
	if !want.Has(formats.ViewDetails) {
		ss, err := p.ViewSummaries(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, s := range ss {
			out[formats.VersionID(s.VersionID)] = formats.ViewData{Description: s.Description, Keywords: s.Keywords}
		}
		return out, nil
	}
	ds, err := p.ViewDetails(ctx, ids, want.Has(formats.ViewReadme))
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		data, err := describeManifest(d, visible)
		if err != nil {
			return nil, fmt.Errorf("npm: version %d: %w", d.VersionID, err)
		}
		out[formats.VersionID(d.VersionID)] = data
	}
	return out, nil
}

// manifestView is the part of a stored manifest the screens read. Every member is
// raw, because a manifest is what a publisher wrote: a member of an unexpected type
// is left out and fails nothing.
type manifestView struct {
	Description  json.RawMessage `json:"description"`
	Keywords     json.RawMessage `json:"keywords"`
	License      json.RawMessage `json:"license"`
	Licenses     json.RawMessage `json:"licenses"`
	Deps         json.RawMessage `json:"dependencies"`
	DevDeps      json.RawMessage `json:"devDependencies"`
	PeerDeps     json.RawMessage `json:"peerDependencies"`
	OptionalDeps json.RawMessage `json:"optionalDependencies"`
	Engines      json.RawMessage `json:"engines"`
	Author       json.RawMessage `json:"author"`
	Contributors json.RawMessage `json:"contributors"`
	Homepage     json.RawMessage `json:"homepage"`
	Repository   json.RawMessage `json:"repository"`
	Bugs         json.RawMessage `json:"bugs"`
}

func describeManifest(d rows.ViewDetail, visible func(string) bool) (formats.ViewData, error) {
	var m manifestView
	if err := json.Unmarshal(d.Manifest, &m); err != nil {
		return formats.ViewData{}, err
	}
	data := formats.ViewData{
		Description: text(m.Description),
		Keywords:    stringList(m.Keywords),
		Licenses:    licenses(m.License, m.Licenses),
		HasReadme:   d.HasReadme,
		Readme:      d.Readme,
		ReadmeFile:  d.ReadmeFilename,
		Metadata:    map[string]any{},
	}
	md := data.Metadata
	if tags := visibleTags(d.DistTags, visible); len(tags) > 0 {
		md["distTags"] = tags
	}
	for key, raw := range map[string]json.RawMessage{
		"dependencies": m.Deps, "devDependencies": m.DevDeps, "peerDependencies": m.PeerDeps,
		"optionalDependencies": m.OptionalDeps, "engines": m.Engines,
	} {
		if mm := stringMap(raw); len(mm) > 0 {
			md[key] = mm
		}
	}
	if authors := authors(m.Author, m.Contributors); len(authors) > 0 {
		md["authors"] = authors
	}
	if s := text(m.Homepage); s != "" {
		md["homepage"] = s
	}
	if s := urlOf(m.Repository); s != "" {
		md["repositoryUrl"] = s
	}
	if s := urlOf(m.Bugs); s != "" {
		md["bugsUrl"] = s
	}
	if d.Deprecated != "" {
		md["deprecated"] = d.Deprecated
	}
	return data, nil
}

// visibleTags keeps the dist-tags whose version the caller may read: a tag that
// names a hidden version would reveal its number.
func visibleTags(tags map[string]string, visible func(string) bool) map[string]string {
	out := map[string]string{}
	for tag, v := range tags {
		if visible != nil && visible(v) {
			out[tag] = v
		}
	}
	return out
}

func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func stringList(raw json.RawMessage) []string {
	var in []any
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	var out []string
	for _, v := range in {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func stringMap(raw json.RawMessage) map[string]string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// licenses reads the declared license: an SPDX expression as a string, the legacy
// {"type": ...} object, or the legacy list of such objects.
func licenses(license, legacy json.RawMessage) []string {
	one := func(raw json.RawMessage) string {
		if s := text(raw); s != "" {
			return s
		}
		var o struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &o) == nil {
			return strings.TrimSpace(o.Type)
		}
		return ""
	}
	var out []string
	if s := one(license); s != "" {
		out = append(out, s)
	}
	var list []json.RawMessage
	if json.Unmarshal(legacy, &list) == nil {
		for _, raw := range list {
			if s := one(raw); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// authors reads the author and the contributors as the "Name <email> (url)" strings
// npm documents; an object is joined in that form.
func authors(author, contributors json.RawMessage) []string {
	person := func(raw json.RawMessage) string {
		if s := text(raw); s != "" {
			return s
		}
		var o struct{ Name, Email, URL string }
		if json.Unmarshal(raw, &o) != nil || strings.TrimSpace(o.Name) == "" {
			return ""
		}
		s := strings.TrimSpace(o.Name)
		if e := strings.TrimSpace(o.Email); e != "" {
			s += " <" + e + ">"
		}
		if u := strings.TrimSpace(o.URL); u != "" {
			s += " (" + u + ")"
		}
		return s
	}
	var out []string
	if s := person(author); s != "" {
		out = append(out, s)
	}
	var list []json.RawMessage
	if json.Unmarshal(contributors, &list) == nil {
		for _, raw := range list {
			if s := person(raw); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// urlOf reads a repository or bugs member: a string, or an object with a url.
func urlOf(raw json.RawMessage) string {
	if s := text(raw); s != "" {
		return s
	}
	var o struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return strings.TrimSpace(o.URL)
	}
	return ""
}

// Find asks the data port for the versions whose manifest matches.
func (viewAdapter) Find(ctx context.Context, ports formats.DataPorts, f formats.ViewFind, limit int) ([]formats.VersionID, error) {
	p, err := portOf(ports)
	if err != nil {
		return nil, err
	}
	ids, err := p.ViewFind(ctx, f.Repos, f.Text, f.Keyword, limit+1)
	return versionIDs(ids), err
}

// Withheld asks the data port for the stored versions the format does not serve.
func (viewAdapter) Withheld(ctx context.Context, ports formats.DataPorts, repos []string) ([]formats.VersionID, error) {
	p, err := portOf(ports)
	if err != nil {
		return nil, err
	}
	ids, err := p.ViewWithheld(ctx, repos)
	return versionIDs(ids), err
}

func versionIDs(in []int64) []formats.VersionID {
	out := make([]formats.VersionID, len(in))
	for i, id := range in {
		out[i] = formats.VersionID(id)
	}
	return out
}
