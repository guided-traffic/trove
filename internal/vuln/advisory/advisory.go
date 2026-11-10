// Package advisory holds the normalized record types of the vulnerability scanning (docs/spec/vulnerability-scanning.md,
// R-VULN-006): one OSV record as trove stores and matches it, its severity levels and ranks, the attribution of its
// source (R-VULN-003), the numeric identifier the npm audit answer gives it (Q-VULN-17) and the advisory URL rule of
// R-VULN-017.
//
// It is a leaf: it imports the standard library only, touches no database and holds no state, so that the format
// contract (which hands advisories to a format's audit routes), the OSV decoder, the matcher, the store and the report
// all share one set of types without importing each other.
package advisory

import (
	"encoding/json"
	"fmt"
	"time"
)

// EcosystemOCI is the ecosystem of container images. Its advisories come from the analyser's database (R-VULN-035), never from the OSV
// export, and a finding's version is a manifest digest (R-VULN-012).
const EcosystemOCI = "OCI"

// Kind says what an advisory is (R-VULN-006).
type Kind string

// The kinds. malicious is every record whose identifier starts with MAL- (the OpenSSF malicious-packages records).
const (
	KindVulnerability Kind = "vulnerability"
	KindMalicious     Kind = "malicious"
)

// Valid reports whether k is one of the two kinds the schema allows.
func (k Kind) Valid() bool { return k == KindVulnerability || k == KindMalicious }

// Severity is the level of an advisory (R-VULN-007).
type Severity string

// The levels, from the worst to the absence of one. Unknown is a level of its own: it is sent to clients as high
// (R-VULN-017) and sorts below none, so it never meets a threshold.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityNone     Severity = "none"
	SeverityUnknown  Severity = "unknown"
)

// Severities lists the levels in descending rank, the order of the list's default sort.
func Severities() []Severity {
	return []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityNone, SeverityUnknown}
}

// Rank is the sort key stored in vuln_finding.severity_rank (R-VULN-012): critical 5, high 4, medium 3, low 2, none 1,
// unknown 0. A string that is no level ranks as unknown.
func (s Severity) Rank() int {
	switch s {
	case SeverityCritical:
		return 5
	case SeverityHigh:
		return 4
	case SeverityMedium:
		return 3
	case SeverityLow:
		return 2
	case SeverityNone:
		return 1
	}
	return 0
}

// Valid reports whether s is one of the six levels the schema allows.
func (s Severity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityNone, SeverityUnknown:
		return true
	}
	return false
}

// SeverityRank returns the rank of a level written as text.
func SeverityRank(level string) int { return Severity(level).Rank() }

// SeverityOfRank is the inverse of [Severity.Rank].
func SeverityOfRank(rank int) Severity {
	for _, s := range Severities() {
		if s.Rank() == rank {
			return s
		}
	}
	return SeverityUnknown
}

// Higher reports whether s outranks o.
func (s Severity) Higher(o Severity) bool { return s.Rank() > o.Rank() }

// SeveritySource says where the level came from (R-VULN-007).
type SeveritySource string

// The sources of a severity.
const (
	SourceCVSS      SeveritySource = "cvss"
	SourceAdvisory  SeveritySource = "advisory"
	SourceMalicious SeveritySource = "malicious"
	SourceNone      SeveritySource = "none"
)

// CVSS is the vector the severity was scored from. The zero value says there is none.
type CVSS struct {
	// Version is "4.0", "3.1", "3.0" or "2.0".
	Version string
	Vector  string
	Score   float64
}

// Present reports whether the advisory carries a scored vector.
func (c CVSS) Present() bool { return c.Version != "" && c.Vector != "" }

// EventKind is the one key of an OSV range event.
type EventKind string

// The event kinds of the OSV schema.
const (
	EventIntroduced   EventKind = "introduced"
	EventFixed        EventKind = "fixed"
	EventLastAffected EventKind = "last_affected"
	EventLimit        EventKind = "limit"
)

// Event is one event of a range: exactly one of introduced, fixed, last_affected and limit with its version, as the
// OSV schema writes it ({"fixed": "1.2.3"}). Its JSON form is the one the schema has and the one vuln_affected.ranges
// stores.
type Event struct {
	Kind    EventKind
	Version string
}

// MarshalJSON writes the one-key object.
func (e Event) MarshalJSON() ([]byte, error) {
	switch e.Kind {
	case EventIntroduced, EventFixed, EventLastAffected, EventLimit:
		return json.Marshal(map[string]string{string(e.Kind): e.Version})
	}
	return nil, fmt.Errorf("advisory: %q is no range event", e.Kind)
}

// UnmarshalJSON reads the one-key object and refuses any other shape: an event that names two keys or none is not
// evaluable and the record is rejected (R-VULN-006).
func (e *Event) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("advisory: a range event is an object: %w", err)
	}
	if len(m) != 1 {
		return fmt.Errorf("advisory: a range event has exactly one key, got %d", len(m))
	}
	for k, raw := range m {
		switch kind := EventKind(k); kind {
		case EventIntroduced, EventFixed, EventLastAffected, EventLimit:
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				return fmt.Errorf("advisory: the version of %q is a string: %w", k, err)
			}
			*e = Event{Kind: kind, Version: v}
		default:
			return fmt.Errorf("advisory: %q is no range event", k)
		}
	}
	return nil
}

// RangeType is the type of a range.
type RangeType string

// The range types that are evaluated; GIT ranges are ignored (R-VULN-009).
const (
	RangeSemver    RangeType = "SEMVER"
	RangeEcosystem RangeType = "ECOSYSTEM"
)

// Range is one range of an affected entry. Its JSON form is the element of vuln_affected.ranges.
type Range struct {
	Type   RangeType `json:"type"`
	Events []Event   `json:"events"`
}

// Affected is one affected entry of an advisory for one package name of its ecosystem (vuln_affected).
type Affected struct {
	// Name is normalized by the format's adapter.
	Name   string
	Ranges []Range
	// Versions are the explicit versions, normalized by the format.
	Versions []string
	// EveryVersion is set for an entry of one range [{"introduced": "0"}] and no versions: it matches every version
	// without parsing it.
	EveryVersion bool
	// Fixed collects the fixed events, for display and for fixAvailable (R-VULN-006).
	Fixed []string
}

// Reference is one link of an advisory; only http and https URLs are kept (R-VULN-006).
type Reference struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url"`
}

// Advisory is one stored OSV record (vuln_advisory with its vuln_affected rows).
type Advisory struct {
	Ecosystem string
	// ID is the identifier as published, CanonicalID the one of the alias group (R-VULN-006): MAL-, then GHSA-, then the
	// lexically smallest.
	ID, CanonicalID string
	Modified        time.Time
	// Published and Withdrawn are the zero time when absent.
	Published, Withdrawn time.Time
	Kind                 Kind
	Aliases              []string
	Summary, Details     string
	Severity             Severity
	SeveritySource       SeveritySource
	CVSS                 CVSS
	// KnownExploited and KEVDateAdded come from the CISA catalog (R-VULN-008); the date is the zero time when not listed.
	KnownExploited bool
	KEVDateAdded   time.Time
	References     []Reference
	// CWEIDs are database_specific.cwe_ids (R-VULN-017 cwe, Q-VULN-24).
	CWEIDs   []string
	Affected []Affected
}

// IsWithdrawn reports whether the record is withdrawn: it stays stored and affects nothing (R-VULN-009).
func (a Advisory) IsWithdrawn() bool { return !a.Withdrawn.IsZero() }
