package osvstub

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the OSV schema version the fixture records declare.
const SchemaVersion = "1.7.0"

// TimeLayout is the layout of every timestamp the stub writes: RFC 3339 in UTC with microseconds, the shape of the
// bucket's change file.
const TimeLayout = "2006-01-02T15:04:05.000000Z"

// Advisory builds an OSV record in the shape the GitHub Advisory Database and the OpenSSF malicious-packages
// project publish. Modified is set by the dataset when the record is added.
type Advisory struct {
	SchemaVersion    string         `json:"schema_version,omitempty"`
	ID               string         `json:"id"`
	Modified         string         `json:"modified,omitempty"`
	Published        string         `json:"published,omitempty"`
	Withdrawn        string         `json:"withdrawn,omitempty"`
	Aliases          []string       `json:"aliases,omitempty"`
	Summary          string         `json:"summary,omitempty"`
	Details          string         `json:"details,omitempty"`
	Severity         []Severity     `json:"severity,omitempty"`
	Affected         []Affected     `json:"affected,omitempty"`
	References       []Reference    `json:"references,omitempty"`
	DatabaseSpecific map[string]any `json:"database_specific,omitempty"`
}

// Severity is one entry of severity[].
type Severity struct {
	Type  string `json:"type"` // CVSS_V2, CVSS_V3, CVSS_V4
	Score string `json:"score"`
}

// Affected is one entry of affected[].
type Affected struct {
	Package          Package        `json:"package"`
	Ranges           []Range        `json:"ranges,omitempty"`
	Versions         []string       `json:"versions,omitempty"`
	DatabaseSpecific map[string]any `json:"database_specific,omitempty"`
}

// Package names a package of an ecosystem.
type Package struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
}

// Range is one entry of ranges[]; its events carry one key each.
type Range struct {
	Type   string              `json:"type"` // SEMVER, ECOSYSTEM, GIT
	Events []map[string]string `json:"events"`
}

// Reference is one entry of references[].
type Reference struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Introduced, Fixed and LastAffected are the events of a range.
func Introduced(v string) map[string]string { return map[string]string{"introduced": v} }

// Fixed is the event of the version that fixes the range.
func Fixed(v string) map[string]string { return map[string]string{"fixed": v} }

// LastAffected is the event of the last affected version.
func LastAffected(v string) map[string]string { return map[string]string{"last_affected": v} }

// JSON is the record as the bucket serves it (compact, no trailing newline).
func (a Advisory) JSON() []byte {
	b, err := json.Marshal(a)
	if err != nil {
		panic(err) // the types above always marshal
	}
	return b
}

// stamp formats t for a record or a change-file row.
func stamp(t time.Time) string { return t.UTC().Format(TimeLayout) }
