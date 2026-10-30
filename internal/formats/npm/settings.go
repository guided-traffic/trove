package npm

import (
	"encoding/json"

	"github.com/guided-traffic/trove/internal/formats/npm/merge"
)

// settings are the members of the npm section of a repository document that the remote
// and virtual classes read (R-NPM-026, R-NPM-031). The repository document has no
// format section yet (Q-NPM-23), so the section is read from the stored document as it
// is: a document that carries one is honoured, and one that carries none runs on the
// defaults.
type settings struct {
	// ServeRemovedVersions is npm.serveRemovedVersions of a remote repository.
	ServeRemovedVersions bool
	// LatestStrategy and DistTagStrategy are those of a virtual repository.
	LatestStrategy, DistTagStrategy string
}

func defaultSettings() settings {
	return settings{LatestStrategy: merge.HighestMemberLatest, DistTagStrategy: merge.MemberOrder}
}

// settingsOf reads the npm section of a repository document; a value that is not one
// the strategies know is the default.
func settingsOf(config []byte) settings {
	out := defaultSettings()
	var doc struct {
		NPM struct {
			ServeRemovedVersions bool   `json:"serveRemovedVersions"`
			LatestStrategy       string `json:"latestStrategy"`
			DistTagStrategy      string `json:"distTagStrategy"`
		} `json:"npm"`
	}
	if json.Unmarshal(config, &doc) != nil {
		return out
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
