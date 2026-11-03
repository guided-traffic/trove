package vuln

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/guided-traffic/trove/internal/errs"
)

// MatchPublic is the value of `matchPublicAdvisories` (R-VULN-010).
type MatchPublic string

// The values of matchPublicAdvisories.
const (
	// MatchAuto matches a remote repository whose upstream host is a public upstream of the format and a local
	// repository's names that no claim covers.
	MatchAuto MatchPublic = "auto"
	// MatchAlways matches every stored version: the repository declares that it holds public packages.
	MatchAlways MatchPublic = "always"
	// MatchNever matches none: every version is not_covered (remote) or internal (local).
	MatchNever MatchPublic = "never"
)

// OnMissingData is the value of `onMissingData` (R-VULN-015, Q-VULN-5).
type OnMissingData string

// The values of onMissingData.
const (
	// MissingAllow serves a version of an ecosystem whose data was never imported completely.
	MissingAllow OnMissingData = "allow"
	// MissingBlock refuses it with vulnerability_data_missing.
	MissingBlock OnMissingData = "block"
)

// Settings is the repository member `vulnerabilities` of R-VULN-002, on local and remote repositories of a format with
// the vulnerability adapter. Its JSON form is the member's; an absent field takes its default, so that the merge patch
// {"vulnerabilities": {"enabled": false}} on a repository without the member is a complete member.
type Settings struct {
	// Enabled is whether the repository's stored versions are evaluated and blocked. Default true.
	Enabled bool `json:"enabled"`
	// MatchPublicAdvisories is auto, always or never (R-VULN-010). Default auto.
	MatchPublicAdvisories MatchPublic `json:"matchPublicAdvisories"`
	// BlockMalicious blocks the versions an open malicious finding names (R-VULN-015). Default true.
	BlockMalicious bool `json:"blockMalicious"`
	// OnMissingData is allow or block (R-VULN-015). Default allow.
	OnMissingData OnMissingData `json:"onMissingData"`
}

// DefaultSettings returns the member's defaults (R-VULN-002, "default (proposed)").
func DefaultSettings() Settings {
	return Settings{Enabled: true, MatchPublicAdvisories: MatchAuto, BlockMalicious: true, OnMissingData: MissingAllow}
}

// UnmarshalJSON reads the member strictly: an unknown or misspelt field is an error (R-REPO-008), an absent field keeps
// its default.
func (s *Settings) UnmarshalJSON(b []byte) error {
	type plain Settings
	p := plain(DefaultSettings())
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*s = Settings(p)
	return nil
}

// Validate checks the values and returns one error per violation; a pointer is relative to the member
// ("/matchPublicAdvisories"), the caller prefixes "/vulnerabilities". The codes are invalid_value, as every enumerated
// setting of the repository document.
func (s Settings) Validate() []errs.FieldError {
	var out []errs.FieldError
	switch s.MatchPublicAdvisories {
	case MatchAuto, MatchAlways, MatchNever:
	default:
		out = append(out, errs.FieldError{Pointer: "/matchPublicAdvisories", Code: "invalid_value", Detail: `must be "auto", "always" or "never"`})
	}
	switch s.OnMissingData {
	case MissingAllow, MissingBlock:
	default:
		out = append(out, errs.FieldError{Pointer: "/onMissingData", Code: "invalid_value", Detail: `must be "allow" or "block"`})
	}
	return out
}

// RescanInputsDiffer reports whether a change from s to o enqueues a vuln-rescan of the repository (R-VULN-002):
// enabled and matchPublicAdvisories do; blockMalicious and onMissingData act without a job. The change of the
// repository's upstream URL is the caller's to compare.
func (s Settings) RescanInputsDiffer(o Settings) bool {
	return s.Enabled != o.Enabled || s.MatchPublicAdvisories != o.MatchPublicAdvisories
}

// String is for logs.
func (s Settings) String() string {
	return fmt.Sprintf("enabled=%t matchPublicAdvisories=%s blockMalicious=%t onMissingData=%s",
		s.Enabled, s.MatchPublicAdvisories, s.BlockMalicious, s.OnMissingData)
}
