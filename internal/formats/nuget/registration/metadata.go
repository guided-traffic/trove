package registration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Metadata is nuget_version.metadata as the registration documents read it, the JSON
// object of R-NUGET-005. Only the keys a catalog entry shows are decoded; the others
// (releaseNotes, copyright, icon, readme, repository, packageTypes, …) stay in the row for
// the parts that use them. A key that is absent is the zero value.
type Metadata struct {
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Authors     string `json:"authors"`
	Tags        Tags   `json:"tags"`

	ProjectURL string   `json:"projectUrl"`
	IconURL    string   `json:"iconUrl"`
	LicenseURL string   `json:"licenseUrl"`
	License    *License `json:"license"`

	// RequireLicenseAcceptance is nil when the row does not say.
	RequireLicenseAcceptance *bool  `json:"requireLicenseAcceptance"`
	MinClientVersion         string `json:"minClientVersion"`

	DependencyGroups []DependencyGroup `json:"dependencyGroups"`
}

// License is the license key: Type is "expression" or "file".
type License struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	Value   string `json:"value"`
}

// DependencyGroup is one dependency group of the package; TargetFramework is verbatim and
// "" for a group without one.
type DependencyGroup struct {
	TargetFramework string       `json:"targetFramework"`
	Dependencies    []Dependency `json:"dependencies"`
}

// Dependency is one dependency: the ID as the .nuspec spells it and its version range
// verbatim, "" when the .nuspec gave none.
type Dependency struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Tags are the tags of a package. The row holds them as a list; a row that holds one
// string of tags separated by white space is read alike.
type Tags []string

// UnmarshalJSON reads a list of strings or one string split on white space.
func (t *Tags) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		*t = nil
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = strings.Fields(s)
	default:
		var l []string
		if err := json.Unmarshal(b, &l); err != nil {
			return err
		}
		*t = l
	}
	return nil
}

// ParseMetadata decodes the metadata object of a version row. An empty value is an empty
// object. Unknown keys are ignored: the row is written by the same release or an older
// one, and a key a later release adds must not fail a read.
func ParseMetadata(raw []byte) (Metadata, error) {
	var m Metadata
	if len(bytes.TrimSpace(raw)) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Metadata{}, fmt.Errorf("the metadata of a version is not the object of R-NUGET-005: %w", err)
	}
	return m, nil
}
