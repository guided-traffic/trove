package oci

import (
	_ "embed" // the settings schema
	"encoding/json"
)

// settingsSchema is the JSON Schema 2020-12 of the oci member of a repository document (R-ARCH-008, R-OCI-026). The repository
// service validates the member against it when a document is written, and GET /api/v1/formats serves it to the create form.
//
//go:embed settings.schema.json
var settingsSchema []byte

// MirrorSettings are the members of oci.mirror of a remote repository.
type MirrorSettings struct {
	// Tags are the tag patterns a tag must match to be mirrored.
	Tags []string
	// ExcludeTags are the tag patterns that are never mirrored.
	ExcludeTags []string
	// Platforms are the platforms of an index whose manifests are mirrored; empty is every platform.
	Platforms []string
}

// mirrorSettingsOf reads oci.mirror from a stored repository document, with the defaults for what the document leaves out.
// The service refuses a value the schema does not allow, so only a row written around the service can fail to parse; it
// yields the defaults.
func mirrorSettingsOf(config []byte) MirrorSettings {
	out := MirrorSettings{Tags: []string{"latest"}, ExcludeTags: []string{}, Platforms: []string{}}
	var doc struct {
		OCI struct {
			Mirror struct {
				Tags        []string `json:"tags"`
				ExcludeTags []string `json:"excludeTags"`
				Platforms   []string `json:"platforms"`
			} `json:"mirror"`
		} `json:"oci"`
	}
	if json.Unmarshal(config, &doc) != nil {
		return out
	}
	if m := doc.OCI.Mirror; m.Tags != nil {
		out.Tags = m.Tags
	}
	if m := doc.OCI.Mirror; m.ExcludeTags != nil {
		out.ExcludeTags = m.ExcludeTags
	}
	if m := doc.OCI.Mirror; m.Platforms != nil {
		out.Platforms = m.Platforms
	}
	return out
}
