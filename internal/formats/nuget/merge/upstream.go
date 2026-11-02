package merge

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// upstreamIndex is a registration index as an upstream serves it, reduced to what the
// merge reads: the pages and, inlined in them, the leaves with their catalog entries.
type upstreamIndex struct {
	Items []struct {
		ID    string `json:"@id"`
		Count int    `json:"count"`
		Items []struct {
			CatalogEntry upstreamEntry `json:"catalogEntry"`
		} `json:"items"`
	} `json:"items"`
}

// upstreamEntry is the catalog entry of a leaf. Members trove does not show are not read;
// the pass-through members are kept as they are.
type upstreamEntry struct {
	ID                       string                 `json:"id"`
	Version                  string                 `json:"version"`
	Authors                  json.RawMessage        `json:"authors"`
	Description              string                 `json:"description"`
	IconURL                  string                 `json:"iconUrl"`
	Language                 string                 `json:"language"`
	LicenseExpression        string                 `json:"licenseExpression"`
	LicenseURL               string                 `json:"licenseUrl"`
	MinClientVersion         string                 `json:"minClientVersion"`
	ProjectURL               string                 `json:"projectUrl"`
	RequireLicenseAcceptance *bool                  `json:"requireLicenseAcceptance"`
	Summary                  string                 `json:"summary"`
	Tags                     json.RawMessage        `json:"tags"`
	Title                    string                 `json:"title"`
	Listed                   *bool                  `json:"listed"`
	Published                string                 `json:"published"`
	DependencyGroups         []upstreamDependencies `json:"dependencyGroups"`
	Deprecation              json.RawMessage        `json:"deprecation"`
	Vulnerabilities          json.RawMessage        `json:"vulnerabilities"`
}

type upstreamDependencies struct {
	TargetFramework string `json:"targetFramework"`
	Dependencies    []struct {
		ID    string `json:"id"`
		Range string `json:"range"`
	} `json:"dependencies"`
}

// DecodeUpstream reads the stored registration index of an upstream, with every page
// inlined, into entries (R-NUGET-015, R-NUGET-017). The remote repository hands the merge
// that form; a page the upstream lists by URL alone, without its leaves, is an index the
// merge cannot read, and the member fails as an upstream failure rather than contribute a
// partial list. Versions that are no version are dropped.
func DecodeUpstream(b []byte) ([]Entry, error) {
	var idx upstreamIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A registration index of a member is no registration index.", errs.WithCause(err))
	}
	var out []Entry
	for _, pg := range idx.Items {
		if pg.Items == nil && pg.Count > 0 {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
				"A registration page of a member is not inlined, so its versions cannot be merged.")
		}
		for _, l := range pg.Items {
			e, ok := l.CatalogEntry.entry()
			if ok {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// entry converts a catalog entry; ok is false for a version that is no version.
func (u upstreamEntry) entry() (Entry, bool) {
	v, err := version.Parse(u.Version)
	if err != nil {
		return Entry{}, false
	}
	m := registration.Metadata{
		Title: u.Title, Summary: u.Summary, Description: u.Description, Language: u.Language,
		Authors: joinWords(u.Authors, ", "), Tags: registration.Tags(words(u.Tags)),
		ProjectURL: u.ProjectURL, IconURL: u.IconURL, LicenseURL: u.LicenseURL,
		RequireLicenseAcceptance: u.RequireLicenseAcceptance, MinClientVersion: u.MinClientVersion,
	}
	if u.LicenseExpression != "" {
		m.License = &registration.License{Type: "expression", Value: u.LicenseExpression}
	}
	semver2 := v.IsSemver2()
	for _, g := range u.DependencyGroups {
		dg := registration.DependencyGroup{TargetFramework: g.TargetFramework}
		for _, d := range g.Dependencies {
			dg.Dependencies = append(dg.Dependencies, registration.Dependency{ID: d.ID, Version: d.Range})
			if r, err := version.ParseRange(d.Range); err == nil && r.IsSemver2() {
				semver2 = true
			}
		}
		m.DependencyGroups = append(m.DependencyGroups, dg)
	}
	meta, err := json.Marshal(m)
	if err != nil { // cannot happen: strings and lists only
		return Entry{}, false
	}
	e := Entry{
		Version: v.URLForm(), Full: v.FullNormalized(), DeclaredID: u.ID,
		Listed: u.Listed == nil || *u.Listed, Prerelease: v.IsPrerelease(), Semver2: semver2,
		Published: parseTime(u.Published), Metadata: meta,
		Deprecation: nonNull(u.Deprecation), Vulnerabilities: nonNull(u.Vulnerabilities),
	}
	return e, true
}

// words reads a member that is a list of strings or one string of words.
func words(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.Fields(text)
	}
	return nil
}

// joinWords reads a member that is a string or a list of strings as one string.
func joinWords(raw json.RawMessage, sep string) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, sep)
	}
	return ""
}

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// nonNull drops a JSON null, which the omitted member and the null member of an upstream
// both mean.
func nonNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}
