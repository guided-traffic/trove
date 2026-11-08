package deb

import (
	_ "embed" // the settings schema
	"encoding/json"
)

// settingsSchema is the JSON Schema 2020-12 of the deb member of a repository document (R-ARCH-008, R-DEB-008, R-DEB-024). The
// repository service validates the member against it when a document is written, and GET /api/v1/formats serves it to the create
// form. The cross-member rules (mode mirror needs suites and upstreamKeys, a virtual member needs keys) are the repository service's
// (internal/repository/validate_deb.go, built with the remote class).
//
//go:embed settings.schema.json
var settingsSchema []byte

// Settings are the members of the deb member of a repository document (R-DEB-008), with the defaults for what the document leaves
// out.
type Settings struct {
	// Architectures, Compression, Origin and Label apply to local and virtual repositories. Label "" means the repository key.
	Architectures []string
	Compression   []string
	Origin        string
	Label         string
	// Suites and Components apply to virtual repositories.
	Suites     []string
	Components []string
	// UpstreamKeys applies to remote repositories: the armored block, "" for none.
	UpstreamKeys string
	Mirror       MirrorSettings
}

// MirrorSettings are the members of deb.mirror of a remote repository (R-DEB-024).
type MirrorSettings struct {
	Suites        []string
	Components    []string
	Architectures []string
	Translations  []string
	Sources       bool
}

// DefaultSettings are the settings of a repository whose document sets none (R-DEB-008, R-DEB-024).
func DefaultSettings() Settings {
	return Settings{
		Architectures: []string{"amd64", "arm64"}, Compression: []string{}, Origin: "trove",
		Suites: []string{}, Components: []string{"main"},
		Mirror: MirrorSettings{Suites: []string{}, Components: []string{"main"}, Architectures: []string{"amd64"}, Translations: []string{"en"}},
	}
}

// SettingsOf reads the deb member from the format's member of a stored repository document (the raw JSON of the member, or nil),
// with the defaults for what it leaves out. The service refuses a value the schema does not allow, so only a row written around the
// service can fail to parse; it yields the defaults.
func SettingsOf(member json.RawMessage) Settings {
	out := DefaultSettings()
	if len(member) == 0 {
		return out
	}
	var doc struct {
		Architectures []string `json:"architectures"`
		Compression   []string `json:"compression"`
		Origin        *string  `json:"origin"`
		Label         *string  `json:"label"`
		Suites        []string `json:"suites"`
		Components    []string `json:"components"`
		UpstreamKeys  *string  `json:"upstreamKeys"`
		Mirror        struct {
			Suites        []string `json:"suites"`
			Components    []string `json:"components"`
			Architectures []string `json:"architectures"`
			Translations  []string `json:"translations"`
			Sources       *bool    `json:"sources"`
		} `json:"mirror"`
	}
	if json.Unmarshal(member, &doc) != nil {
		return out
	}
	if doc.Architectures != nil {
		out.Architectures = doc.Architectures
	}
	if doc.Compression != nil {
		out.Compression = doc.Compression
	}
	if doc.Origin != nil {
		out.Origin = *doc.Origin
	}
	if doc.Label != nil {
		out.Label = *doc.Label
	}
	if doc.Suites != nil {
		out.Suites = doc.Suites
	}
	if doc.Components != nil {
		out.Components = doc.Components
	}
	if doc.UpstreamKeys != nil {
		out.UpstreamKeys = *doc.UpstreamKeys
	}
	m := doc.Mirror
	if m.Suites != nil {
		out.Mirror.Suites = m.Suites
	}
	if m.Components != nil {
		out.Mirror.Components = m.Components
	}
	if m.Architectures != nil {
		out.Mirror.Architectures = m.Architectures
	}
	if m.Translations != nil {
		out.Mirror.Translations = m.Translations
	}
	if m.Sources != nil {
		out.Mirror.Sources = *m.Sources
	}
	return out
}
