package remote

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/nuget/registration"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// Version is a version of a remote registration, ready for the renderer of the registration
// documents: the row the renderer reads, and the members of the upstream's catalog entry that
// trove does not compute and passes through (R-NUGET-015).
type Version struct {
	Row    rows.RegistrationVersion
	Extras registration.Passthrough
}

// metadata is nuget_version.metadata as the registration documents read it (rows.Metadata
// names the keys), built from an upstream catalog entry. It differs from rows.Metadata in one
// thing: a requireLicenseAcceptance of false that the upstream states is kept.
type metadata struct {
	Title                    string      `json:"title,omitempty"`
	Summary                  string      `json:"summary,omitempty"`
	Description              string      `json:"description,omitempty"`
	Language                 string      `json:"language,omitempty"`
	Authors                  string      `json:"authors,omitempty"`
	Tags                     []string    `json:"tags,omitempty"`
	ProjectURL               string      `json:"projectUrl,omitempty"`
	IconURL                  string      `json:"iconUrl,omitempty"`
	LicenseURL               string      `json:"licenseUrl,omitempty"`
	License                  *rowLicense `json:"license,omitempty"`
	RequireLicenseAcceptance *bool       `json:"requireLicenseAcceptance,omitempty"`
	MinClientVersion         string      `json:"minClientVersion,omitempty"`
	DependencyGroups         []depGroup  `json:"dependencyGroups,omitempty"`
}

type rowLicense struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type depGroup struct {
	TargetFramework string `json:"targetFramework,omitempty"`
	Dependencies    []dep  `json:"dependencies,omitempty"`
}

type dep struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// computed are the members of a catalog entry that the renderer writes itself or that trove
// does not show: they are never passed through. readmeUrl is left out by R-NUGET-009 (README
// access is R-NUGET-024's).
func computed(k string) bool {
	switch k {
	case "@id", "id", "version", "listed", "published", "packageContent", "authors", "description", "iconUrl", "language", "licenseExpression", "licenseUrl", "minClientVersion", "projectUrl", "requireLicenseAcceptance", "summary", "tags", "title", "dependencyGroups", "deprecation", "vulnerabilities", "readmeUrl":
		return true
	}
	return false
}

// Convert turns a leaf into a version: its identity by the rules of R-NUGET-003, the metadata
// the documents render, the SemVer 2.0.0 flag over the version and the bounds of its dependency
// ranges (R-NUGET-004), and the members to pass through. ok is false for a leaf that names no
// version trove can parse: it is left out of every document, as the client would skip it.
// lowerID is the ID of the request, the fallback of an entry that declares none.
func Convert(l Leaf, lowerID string) (v Version, ok bool) {
	e := l.Entry
	ver, err := version.Parse(stringOf(e["version"]))
	if err != nil {
		return Version{}, false
	}
	declared := stringOf(e["id"])
	if declared == "" {
		declared = lowerID
	}
	listed := true
	if raw, has := e["listed"]; has {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			listed = b
		}
	}
	m := metadata{
		Title: stringOf(e["title"]), Summary: stringOf(e["summary"]), Description: stringOf(e["description"]),
		Language: stringOf(e["language"]), Authors: authors(e["authors"]), Tags: tags(e["tags"]),
		ProjectURL: stringOf(e["projectUrl"]), IconURL: stringOf(e["iconUrl"]), LicenseURL: stringOf(e["licenseUrl"]),
		MinClientVersion: stringOf(e["minClientVersion"]),
	}
	if expr := stringOf(e["licenseExpression"]); expr != "" {
		m.License = &rowLicense{Type: "expression", Value: expr}
	}
	if raw, has := e["requireLicenseAcceptance"]; has {
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			m.RequireLicenseAcceptance = &b
		}
	}
	semver2 := ver.IsSemver2()
	m.DependencyGroups, semver2 = dependencies(e["dependencyGroups"], semver2)
	meta, err := json.Marshal(m)
	if err != nil { // strings and lists of strings only
		return Version{}, false
	}
	return Version{
		Row: rows.RegistrationVersion{
			VersionEntry: rows.VersionEntry{
				LowerVersion: ver.URLForm(), FullVersion: ver.FullNormalized(), DeclaredID: declared,
				Listed: listed, IsPrerelease: ver.IsPrerelease(), IsSemver2: semver2, PublishedAt: published(e["published"]),
			},
			PackageTypes: []string{"dependency"}, Metadata: meta,
		},
		Extras: registration.Passthrough{
			Deprecation: passThrough(e["deprecation"], false), Vulnerabilities: passThrough(e["vulnerabilities"], true), Other: unknown(e),
		},
	}, true
}

// authors reads the authors member: a string, or a list the registration of older feeds used.
func authors(raw json.RawMessage) string {
	if s := stringOf(raw); s != "" {
		return s
	}
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return strings.Join(l, ", ")
	}
	return ""
}

// tags reads the tags member: a list of strings, or one string separated by white space.
func tags(raw json.RawMessage) []string {
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		return l
	}
	return strings.Fields(stringOf(raw))
}

// dependencies reads the dependency groups; semver2 is the flag so far, raised by a range that
// has a SemVer 2.0.0-specific bound.
func dependencies(raw json.RawMessage, semver2 bool) ([]depGroup, bool) {
	var groups []struct {
		TargetFramework string `json:"targetFramework"`
		Dependencies    []struct {
			ID    string `json:"id"`
			Range string `json:"range"`
		} `json:"dependencies"`
	}
	if json.Unmarshal(raw, &groups) != nil {
		return nil, semver2
	}
	out := make([]depGroup, 0, len(groups))
	for _, g := range groups {
		dg := depGroup{TargetFramework: g.TargetFramework}
		for _, d := range g.Dependencies {
			if d.ID == "" {
				continue
			}
			dg.Dependencies = append(dg.Dependencies, dep{ID: d.ID, Version: d.Range})
			if d.Range != "" {
				if r, err := version.ParseRange(d.Range); err == nil && r.IsSemver2() {
					semver2 = true
				}
			}
		}
		out = append(out, dg)
	}
	return out, semver2
}

// published reads the instant of a leaf; the zero time for a missing or unreadable one.
func published(raw json.RawMessage) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, stringOf(raw)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// compact returns the member without insignificant white space, nil when it is absent or null.
func compact(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil
	}
	return b.Bytes()
}

// unknown returns the members of the catalog entry that no rule of trove names, in key order
// (R-NUGET-015: unknown properties are kept). A member that holds an address is dropped: the
// documents carry no address of the upstream except the web links of people (projectUrl,
// licenseUrl, iconUrl), and an unknown member could point anywhere, including to a resource of
// the upstream trove has replaced (Q-NUGET-52).
func unknown(e map[string]json.RawMessage) []registration.Member {
	keys := make([]string, 0, len(e))
	for k := range e {
		if !computed(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []registration.Member
	for _, k := range keys {
		raw := compact(e[k])
		if raw == nil || holdsAddress(raw) {
			continue
		}
		out = append(out, registration.Member{Key: k, Value: raw})
	}
	return out
}

// holdsAddress reports whether a JSON value contains a string that starts like an absolute
// http or https address, at any depth.
func holdsAddress(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		if s, ok := t.(string); ok && looksLikeAddress(s) {
			return true
		}
	}
}

// Merge returns the versions of leaves ascending by precedence. A version that two leaves name
// is listed once, by the first leaf (an index and a page can both name it while the upstream
// moves a version between pages).
func Merge(leaves []Leaf, lowerID string) []Version {
	c := NewCollector(lowerID)
	c.Add(leaves)
	return c.Versions()
}

// Collector converts the leaves of a registration to versions as they are read, page by page, so
// that the leaves of a page are not held once they are converted (Q-NUGET-83). A version that two
// leaves name is listed once, by the first leaf added.
type Collector struct {
	lowerID string
	seen    map[string]bool
	out     []collected
}

type collected struct {
	v      Version
	parsed version.Version
}

// NewCollector returns a collector for the leaves of the ID lowerID.
func NewCollector(lowerID string) *Collector {
	return &Collector{lowerID: lowerID, seen: map[string]bool{}}
}

// Add converts leaves; a leaf without a version trove can parse is left out (Convert).
func (c *Collector) Add(leaves []Leaf) {
	for _, l := range leaves {
		v, ok := Convert(l, c.lowerID)
		if !ok || c.seen[v.Row.LowerVersion] {
			continue
		}
		parsed, err := version.Parse(v.Row.LowerVersion)
		if err != nil { // cannot happen: Convert parsed it
			continue
		}
		c.seen[v.Row.LowerVersion] = true
		c.out = append(c.out, collected{v: v, parsed: parsed})
	}
}

// Versions returns the versions added, ascending by precedence.
func (c *Collector) Versions() []Version {
	sort.SliceStable(c.out, func(i, j int) bool { return version.Compare(c.out[i].parsed, c.out[j].parsed) < 0 })
	out := make([]Version, len(c.out))
	for i, x := range c.out {
		out[i] = x.v
	}
	return out
}
