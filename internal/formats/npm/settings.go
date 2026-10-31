package npm

import (
	_ "embed" // the settings schema
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/npm/merge"
)

// settingsSchema is the JSON Schema 2020-12 of the npm member of a repository document
// (R-ARCH-008, R-NPM-016, R-NPM-026, R-NPM-031, R-NPM-038). The repository service validates
// the member against it when a document is written, and GET /api/v1/formats serves it to the
// create form.
//
//go:embed settings.schema.json
var settingsSchema []byte

// settings are the members of the npm section of a repository document (R-NPM-016,
// R-NPM-026, R-NPM-031, R-NPM-038). The repository service stores the section as the
// document carries it, validated against [settingsSchema]; the format reads it back from the
// stored document of the request's repository, with the defaults for what the document
// leaves out.
type settings struct {
	// LatestGuard is npm.latestGuard of a local repository.
	LatestGuard bool
	// ServeRemovedVersions is npm.serveRemovedVersions of a remote repository.
	ServeRemovedVersions bool
	// LatestStrategy and DistTagStrategy are those of a virtual repository.
	LatestStrategy, DistTagStrategy string
	// Audit is npm.audit of a remote repository.
	Audit auditSettings
}

// auditSettings are npm.audit (R-NPM-038, Q-NPM-64).
type auditSettings struct {
	Forward bool
	// Provider is providerURL; nil is the remote's upstream URL.
	Provider    *url.URL
	Timeout     time.Duration
	MaxBodySize int64
}

// The audit modes.
const (
	auditForward  = "forward"
	auditDisabled = "disabled"
)

func defaultSettings() settings {
	return settings{
		LatestGuard: true, LatestStrategy: merge.HighestMemberLatest, DistTagStrategy: merge.MemberOrder,
		Audit: auditSettings{Forward: true, Timeout: 30 * time.Second, MaxBodySize: 1 << 20},
	}
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
			Audit                struct {
				Mode        string  `json:"mode"`
				ProviderURL *string `json:"providerURL"`
				Timeout     string  `json:"timeout"`
				MaxBodySize string  `json:"maxBodySize"`
			} `json:"audit"`
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
	a := doc.NPM.Audit
	out.Audit.Forward = a.Mode != auditDisabled
	// A provider gets credentials only through the remote's sealed upstream credentials, never
	// through user info in the URL, which the document shows to every reader of the
	// configuration (H-303); a URL the schema refuses is the default here too.
	if a.ProviderURL != nil {
		if u, err := url.Parse(*a.ProviderURL); err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
			out.Audit.Provider = u
		}
	}
	if d, err := time.ParseDuration(a.Timeout); err == nil && d >= time.Second && d <= 300*time.Second {
		out.Audit.Timeout = d
	}
	if n, ok := byteSize(a.MaxBodySize); ok && n >= 1<<10 && n <= 10<<20 {
		out.Audit.MaxBodySize = n
	}
	return out
}

// byteSize reads the KiB and MiB forms of the schema: "512KiB", "1MiB".
func byteSize(s string) (int64, bool) {
	for suffix, unit := range map[string]int64{"KiB": 1 << 10, "MiB": 1 << 20} {
		if digits, ok := strings.CutSuffix(s, suffix); ok {
			n, err := strconv.ParseInt(digits, 10, 64)
			return n * unit, err == nil && n > 0
		}
	}
	return 0, false
}
