package npm

import (
	_ "embed" // the settings schema
	"encoding/json"

	"github.com/guided-traffic/trove/internal/formats/npm/merge"
)

// settingsSchema is the JSON Schema 2020-12 of the npm member of a repository document
// (R-ARCH-008, R-NPM-016, R-NPM-026, R-NPM-031). The repository service validates the member
// against it when a document is written, and GET /api/v1/formats serves it to the create form.
//
//go:embed settings.schema.json
var settingsSchema []byte

// settings are the members of the npm section of a repository document (R-NPM-016,
// R-NPM-026, R-NPM-031). The repository service stores the section as the document carries
// it, validated against [settingsSchema]; the format reads it back from the stored document
// of the request's repository, with the defaults for what the document leaves out.
type settings struct {
	// LatestGuard is npm.latestGuard of a local repository.
	LatestGuard bool
	// ServeRemovedVersions is npm.serveRemovedVersions of a remote repository.
	ServeRemovedVersions bool
	// LatestStrategy and DistTagStrategy are those of a virtual repository.
	LatestStrategy, DistTagStrategy string
}

func defaultSettings() settings {
	return settings{LatestGuard: true, LatestStrategy: merge.HighestMemberLatest, DistTagStrategy: merge.MemberOrder}
}

// settingsOf reads the npm section of a stored repository document. The service refuses a
// value the schema does not allow, so a value that is none of the strategies can only come
// from a row written around the service; it is the default.
func settingsOf(config []byte) settings {
	out := defaultSettings()
	var doc struct {
		NPM struct {
			LatestGuard          *bool  `json:"latestGuard"`
			ServeRemovedVersions bool   `json:"serveRemovedVersions"`
			LatestStrategy       string `json:"latestStrategy"`
			DistTagStrategy      string `json:"distTagStrategy"`
		} `json:"npm"`
	}
	if json.Unmarshal(config, &doc) != nil {
		return out
	}
	if doc.NPM.LatestGuard != nil {
		out.LatestGuard = *doc.NPM.LatestGuard
	}
	out.ServeRemovedVersions = doc.NPM.ServeRemovedVersions
	switch doc.NPM.LatestStrategy {
	case merge.HighestMemberLatest, merge.NewestPublished:
		out.LatestStrategy = doc.NPM.LatestStrategy
	}
	switch doc.NPM.DistTagStrategy {
	case merge.MemberOrder, merge.HighestSemver, merge.NewestPublished:
		out.DistTagStrategy = doc.NPM.DistTagStrategy
	}
	return out
}
