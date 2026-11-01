// Package registration renders the registration documents of a local NuGet repository
// (R-NUGET-009): the index of an ID with its pages, a page fetched on its own, and a leaf,
// in the three hives. It reads the rows the data port returns and writes bytes; it reads no
// host, no request and no clock, so the same rows, feed and paging always give the same
// bytes (R-NUGET-030). The ETag, the content coding and the response are the document
// leaf's.
//
// Members are written in a fixed order by the structs below. Where the spec sketches a
// document (R-NUGET-009), the order is the sketch's.
package registration

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"

	"github.com/guided-traffic/trove/internal/formats/nuget/document"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// Hive is a registration hive: the resource a registration document is served under
// (R-NUGET-009).
type Hive string

// The three hives, by their path segment below the feed root.
const (
	// Plain is registration/: SemVer 1 versions, never compressed.
	Plain Hive = "registration"
	// Gz is registration-gz/: SemVer 1 versions, gzip when the client accepts it.
	Gz Hive = "registration-gz"
	// SemVer2 is registration-semver2/: every version, gzip when the client accepts it.
	SemVer2 Hive = "registration-semver2"
)

// ParseHive resolves the path segment of a hive.
func ParseHive(s string) (Hive, bool) {
	switch h := Hive(s); h {
	case Plain, Gz, SemVer2:
		return h, true
	}
	return "", false
}

// Gzipped reports whether the hive answers with gzip to a client that accepts it.
func (h Hive) Gzipped() bool { return h != Plain }

// IncludesSemver2 reports whether the hive lists the versions that are SemVer 2.0.0
// specific (R-NUGET-004).
func (h Hive) IncludesSemver2() bool { return h == SemVer2 }

// The defaults of the paging (R-NUGET-009): nuget.org's heuristic.
const (
	DefaultPageSize    = 64
	DefaultInlineBelow = 128
)

// Paging is the nuget.registration section of the configuration.
type Paging struct {
	// PageSize is the number of leaves of a page.
	PageSize int
	// InlineBelow is the number of visible versions below which the index inlines its
	// pages.
	InlineBelow int
}

func (p Paging) size() int {
	if p.PageSize <= 0 {
		return DefaultPageSize
	}
	return p.PageSize
}

func (p Paging) inlineBelow() int {
	if p.InlineBelow <= 0 {
		return DefaultInlineBelow
	}
	return p.InlineBelow
}

// Feed is the v3 root of the repository the documents are served from, with its trailing
// slash: "<base>/api/nuget/<key>/v3/". Every URL of a document is built from it.
type Feed string

// Index is the URL of the registration index of an ID in a hive.
func (f Feed) Index(h Hive, lowerID string) string {
	return string(f) + string(h) + "/" + url.PathEscape(lowerID) + "/index.json"
}

// Page is the URL of a registration page.
func (f Feed) Page(h Hive, lowerID, lower, upper string) string {
	return string(f) + string(h) + "/" + url.PathEscape(lowerID) + "/page/" + url.PathEscape(lower) + "/" + url.PathEscape(upper) + ".json"
}

// Leaf is the URL of the registration leaf of a version.
func (f Feed) Leaf(h Hive, lowerID, lowerVersion string) string {
	return string(f) + string(h) + "/" + url.PathEscape(lowerID) + "/" + url.PathEscape(lowerVersion) + ".json"
}

// Nupkg is the URL of the package of a version in the flat container.
func (f Feed) Nupkg(lowerID, lowerVersion string) string {
	i, v := url.PathEscape(lowerID), url.PathEscape(lowerVersion)
	return string(f) + "flatcontainer/" + i + "/" + v + "/" + i + "." + v + ".nupkg"
}

// Passthrough are the members of a catalog entry that trove does not compute: a remote
// repository passes the upstream's deprecation and vulnerabilities through (R-NUGET-015).
// A local package has none and the members are absent (R-NUGET-009).
type Passthrough struct {
	Deprecation, Vulnerabilities json.RawMessage
}

// Doc renders the documents of one ID in one hive.
type Doc struct {
	Feed    Feed
	Hive    Hive
	LowerID string
	Paging  Paging
	// Extras are the pass-through members by URL-form version; nil for a local package.
	Extras map[string]Passthrough
}

// Visible keeps the versions a hive lists: the SemVer 2.0.0-specific ones only in the
// semver2 hive (R-NUGET-004), and those keep accepts, which is the exposure filter of an
// endpoint (R-NUGET-034); nil keeps all. Listed state does not filter: an unlisted version
// is in the registration with "listed": false. The order of vs is kept.
func Visible(vs []rows.RegistrationVersion, h Hive, keep func(lowerVersion string) bool) []rows.RegistrationVersion {
	out := make([]rows.RegistrationVersion, 0, len(vs))
	for _, v := range vs {
		if v.IsSemver2 && !h.IncludesSemver2() {
			continue
		}
		if keep != nil && !keep(v.LowerVersion) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// The structs below are the documents; the order of their members is the order of the
// bytes.

type indexDoc struct {
	Count int    `json:"count"`
	Items []page `json:"items"`
}

// page is a page of the index: inlined with its leaves, or only its bounds and its URL.
type page struct {
	ID     string     `json:"@id"`
	Count  int        `json:"count"`
	Lower  string     `json:"lower"`
	Upper  string     `json:"upper"`
	Parent string     `json:"parent,omitempty"`
	Items  []leafItem `json:"items,omitempty"`
}

// pageDoc is a page fetched on its own: its items are always there, a page with none
// holds an empty list.
type pageDoc struct {
	ID     string     `json:"@id"`
	Count  int        `json:"count"`
	Lower  string     `json:"lower"`
	Upper  string     `json:"upper"`
	Parent string     `json:"parent"`
	Items  []leafItem `json:"items"`
}

type leafItem struct {
	ID             string `json:"@id"`
	PackageContent string `json:"packageContent"`
	Registration   string `json:"registration"`
	CatalogEntry   entry  `json:"catalogEntry"`
}

// leafDoc is the document of a leaf URL.
type leafDoc struct {
	ID             string `json:"@id"`
	Listed         bool   `json:"listed"`
	PackageContent string `json:"packageContent"`
	Published      string `json:"published"`
	Registration   string `json:"registration"`
}

// entry is the catalog entry of a version (R-NUGET-009): the identity, the metadata
// members in alphabetical order, then the state and the links.
type entry struct {
	ID                       string            `json:"@id"`
	PackageID                string            `json:"id"`
	Version                  string            `json:"version"`
	Authors                  string            `json:"authors,omitempty"`
	Description              string            `json:"description,omitempty"`
	IconURL                  string            `json:"iconUrl,omitempty"`
	Language                 string            `json:"language,omitempty"`
	LicenseExpression        string            `json:"licenseExpression,omitempty"`
	LicenseURL               string            `json:"licenseUrl,omitempty"`
	MinClientVersion         string            `json:"minClientVersion,omitempty"`
	ProjectURL               string            `json:"projectUrl,omitempty"`
	RequireLicenseAcceptance *bool             `json:"requireLicenseAcceptance,omitempty"`
	Summary                  string            `json:"summary,omitempty"`
	Tags                     []string          `json:"tags,omitempty"`
	Title                    string            `json:"title,omitempty"`
	Listed                   bool              `json:"listed"`
	Published                string            `json:"published"`
	PackageContent           string            `json:"packageContent"`
	DependencyGroups         []dependencyGroup `json:"dependencyGroups,omitempty"`
	Deprecation              json.RawMessage   `json:"deprecation,omitempty"`
	Vulnerabilities          json.RawMessage   `json:"vulnerabilities,omitempty"`
}

type dependencyGroup struct {
	TargetFramework string       `json:"targetFramework,omitempty"`
	Dependencies    []dependency `json:"dependencies,omitempty"`
}

type dependency struct {
	ID           string `json:"id"`
	Range        string `json:"range,omitempty"`
	Registration string `json:"registration,omitempty"`
}

// Index renders the registration index of the visible versions vs, ascending: the pages
// of Paging.PageSize leaves, inlined with their leaves below Paging.InlineBelow versions
// and listed by bounds and URL from there on (R-NUGET-009). vs must not be empty; an ID
// without a visible version has no document.
func (d Doc) Index(vs []rows.RegistrationVersion) ([]byte, error) {
	if len(vs) == 0 {
		return nil, fmt.Errorf("registration index of %q: no visible version", d.LowerID)
	}
	indexURL := d.Feed.Index(d.Hive, d.LowerID)
	inline := len(vs) < d.Paging.inlineBelow()
	size := d.Paging.size()
	doc := indexDoc{Items: make([]page, 0, (len(vs)+size-1)/size)}
	for lo := 0; lo < len(vs); lo += size {
		chunk := vs[lo:min(lo+size, len(vs))]
		p := page{Count: len(chunk), Lower: chunk[0].LowerVersion, Upper: chunk[len(chunk)-1].LowerVersion}
		if inline {
			p.ID = indexURL + "#page/" + p.Lower + "/" + p.Upper
			p.Parent = indexURL
			items, err := d.items(chunk)
			if err != nil {
				return nil, err
			}
			p.Items = items
		} else {
			p.ID = d.Feed.Page(d.Hive, d.LowerID, p.Lower, p.Upper)
		}
		doc.Items = append(doc.Items, p)
	}
	doc.Count = len(doc.Items)
	return document.Marshal(doc)
}

// Page renders the page between two inclusive bounds, URL-form versions, from the visible
// versions vs at request time: the leaves inside the bounds, however many there are, so
// that the page URL of an older index keeps answering (R-NUGET-009). found is false for
// bounds that are no range (lower above upper, or no version); a range without a visible
// version is a page without leaves.
func (d Doc) Page(vs []rows.RegistrationVersion, lower, upper string) (body []byte, found bool, err error) {
	lo, err := version.Parse(lower)
	if err != nil {
		return nil, false, nil //nolint:nilerr // bounds that are no version name no page
	}
	hi, err := version.Parse(upper)
	if err != nil || version.Compare(lo, hi) > 0 {
		return nil, false, nil //nolint:nilerr // see above
	}
	var inside []rows.RegistrationVersion
	for _, v := range vs {
		pv, err := version.Parse(v.LowerVersion)
		if err != nil {
			return nil, false, fmt.Errorf("version %q of %q: %w", v.LowerVersion, d.LowerID, err)
		}
		if version.Compare(pv, lo) >= 0 && version.Compare(pv, hi) <= 0 {
			inside = append(inside, v)
		}
	}
	items, err := d.items(inside)
	if err != nil {
		return nil, false, err
	}
	body, err = document.Marshal(pageDoc{
		ID: d.Feed.Page(d.Hive, d.LowerID, lo.URLForm(), hi.URLForm()), Count: len(items),
		Lower: lo.URLForm(), Upper: hi.URLForm(), Parent: d.Feed.Index(d.Hive, d.LowerID), Items: items,
	})
	return body, true, err
}

// Leaf renders the leaf of a version, found is false when vs does not hold it.
func (d Doc) Leaf(vs []rows.RegistrationVersion, lowerVersion string) (body []byte, found bool, err error) {
	i := slices.IndexFunc(vs, func(v rows.RegistrationVersion) bool { return v.LowerVersion == lowerVersion })
	if i < 0 {
		return nil, false, nil
	}
	v := vs[i]
	body, err = document.Marshal(leafDoc{
		ID: d.Feed.Leaf(d.Hive, d.LowerID, v.LowerVersion), Listed: v.Listed,
		PackageContent: d.Feed.Nupkg(d.LowerID, v.LowerVersion), Published: document.Timestamp(v.PublishedAt),
		Registration: d.Feed.Index(d.Hive, d.LowerID),
	})
	return body, true, err
}

func (d Doc) items(vs []rows.RegistrationVersion) ([]leafItem, error) {
	out := make([]leafItem, 0, len(vs))
	for _, v := range vs {
		e, err := d.entry(v)
		if err != nil {
			return nil, err
		}
		leaf := d.Feed.Leaf(d.Hive, d.LowerID, v.LowerVersion)
		out = append(out, leafItem{
			ID: leaf, PackageContent: e.PackageContent, Registration: d.Feed.Index(d.Hive, d.LowerID), CatalogEntry: e,
		})
	}
	return out, nil
}

// entry builds the catalog entry of a version (R-NUGET-009).
func (d Doc) entry(v rows.RegistrationVersion) (entry, error) {
	m, err := ParseMetadata(v.Metadata)
	if err != nil {
		return entry{}, fmt.Errorf("version %q of %q: %w", v.LowerVersion, d.LowerID, err)
	}
	e := entry{
		ID: d.Feed.Leaf(d.Hive, d.LowerID, v.LowerVersion), PackageID: v.DeclaredID, Version: v.FullVersion,
		Authors: m.Authors, Description: m.Description, IconURL: m.IconURL, Language: m.Language,
		LicenseURL: m.LicenseURL, MinClientVersion: m.MinClientVersion, ProjectURL: m.ProjectURL,
		RequireLicenseAcceptance: m.RequireLicenseAcceptance, Summary: m.Summary, Tags: m.Tags, Title: m.Title,
		Listed: v.Listed, Published: document.Timestamp(v.PublishedAt),
		PackageContent: d.Feed.Nupkg(d.LowerID, v.LowerVersion),
	}
	if l := m.License; l != nil && (l.Type == "" || l.Type == "expression") {
		e.LicenseExpression = l.Value
	}
	for _, g := range m.DependencyGroups {
		dg := dependencyGroup{TargetFramework: g.TargetFramework}
		for _, dep := range g.Dependencies {
			x := dependency{ID: dep.ID, Range: dep.Version}
			// The dependency's registration is a URL of this repository, which can hold
			// the ID only when the local rule admits it (R-NUGET-003).
			if id.ValidateLocal(dep.ID) == nil {
				x.Registration = d.Feed.Index(d.Hive, id.Lower(dep.ID))
			}
			dg.Dependencies = append(dg.Dependencies, x)
		}
		e.DependencyGroups = append(e.DependencyGroups, dg)
	}
	if x, ok := d.Extras[v.LowerVersion]; ok {
		e.Deprecation, e.Vulnerabilities = x.Deprecation, x.Vulnerabilities
	}
	return e, nil
}
