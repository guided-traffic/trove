// Package osv decodes, validates and normalizes OSV records and the CISA known-exploited catalog for the vulnerability
// scanning (docs/spec/vulnerability-scanning.md R-VULN-006, R-VULN-007, R-VULN-008).
//
// It is a leaf: it reads bytes and returns values of internal/vuln/advisory, touches no database and no network, and
// takes the format's name and version rules through match.Adapter. A record is untrusted input: [Decode] runs
// internal/formats/untrusted's JSON bounds first, and every text it keeps is cut and cleaned.
package osv

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

// The bounds of R-VULN-006.
const (
	// MaxSummary and MaxDetails cut the texts of a record, in bytes, on a character boundary.
	MaxSummary = 1 << 10
	MaxDetails = 16 << 10
	// MaxReferences is the number of references kept.
	MaxReferences = 64
	// MaxCWEs is the number of CWE identifiers kept.
	MaxCWEs = 100
	// MaxAliases and MaxAffected bound what a record may list; a record above them is rejected, so that a hostile record
	// cannot fan out into rows (vuln_affected.ord is a smallint).
	MaxAliases  = 256
	MaxAffected = 20000
	// maxSeverities bounds severity[] before the vectors are parsed.
	maxSeverities = 32
	maxURL        = 2048
)

// ErrRejected is wrapped by every error of a record that fails a rule of R-VULN-006; the sync counts such a record as rejected.
var ErrRejected = errors.New("osv: record rejected")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	cwePattern = regexp.MustCompile(`^CWE-[0-9]+$`)
)

// ValidID reports whether id satisfies the identifier rule of R-VULN-006.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Record is one decoded record: the advisory as trove stores it and what the sync needs besides.
type Record struct {
	// Advisory has CanonicalID equal to ID and the severity of the record's own decision ([DecideOwn]); the sync assigns the
	// canonical identifier of the alias group and applies rule 3 of R-VULN-007 after the group is known.
	Advisory advisory.Advisory
	// SHA256 is the digest of the record as received.
	SHA256 [sha256.Size]byte
	// Unevaluable counts the dropped affected entries (a name the format refuses) and the ranges one of whose versions the
	// format cannot parse (R-VULN-009).
	Unevaluable int
	// Own is the severity the record decided for itself.
	Own Own
	// Vectors are the vectors of severity[] that parsed, in record order.
	Vectors []Vector
}

type wireRecord struct {
	SchemaVersion string   `json:"schema_version"`
	ID            string   `json:"id"`
	Modified      string   `json:"modified"`
	Published     string   `json:"published"`
	Withdrawn     string   `json:"withdrawn"`
	Aliases       []string `json:"aliases"`
	Summary       string   `json:"summary"`
	Details       string   `json:"details"`
	Severity      []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	Affected []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string           `json:"type"`
			Events []advisory.Event `json:"events"`
		} `json:"ranges"`
		Versions []string `json:"versions"`
	} `json:"affected"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
	DatabaseSpecific struct {
		Severity string   `json:"severity"`
		CWEIDs   []string `json:"cwe_ids"`
	} `json:"database_specific"`
}

// jsonLimits are the bounds of one record: nesting 64 (R-VULN-004 step 3) and the default number of values.
func jsonLimits() untrusted.Limits { return untrusted.DefaultLimits() }

// Decode validates and normalizes one OSV record of an archive of ecosystem (R-VULN-006). A record that fails a rule returns an
// error wrapping [ErrRejected]; the caller counts it. ad is the format's adapter of the ecosystem: it normalizes the
// package names and versions and decides which ranges are evaluable.
func Decode(data []byte, ecosystem string, ad match.Adapter) (*Record, error) {
	var w wireRecord
	if err := untrusted.UnmarshalJSON(data, jsonLimits(), &w); err != nil {
		return nil, reject("the record is not valid JSON of the expected shape: %v", err)
	}
	if !ValidID(w.ID) {
		return nil, reject("the identifier does not satisfy the rule")
	}
	if major, _, _ := strings.Cut(w.SchemaVersion, "."); w.SchemaVersion != "" && major != "1" {
		return nil, reject("schema_version %q has another major version than 1", clip(w.SchemaVersion))
	}
	modified, err := time.Parse(time.RFC3339Nano, w.Modified)
	if err != nil {
		return nil, reject("modified is not an RFC 3339 time")
	}
	a := advisory.Advisory{
		Ecosystem: ecosystem, ID: w.ID, CanonicalID: w.ID, Modified: modified.UTC(),
		Kind:    advisory.KindVulnerability,
		Summary: cleanText(w.Summary, MaxSummary), Details: cleanText(w.Details, MaxDetails),
	}
	if a.Published, err = optTime(w.Published); err != nil {
		return nil, reject("published is not an RFC 3339 time")
	}
	if a.Withdrawn, err = optTime(w.Withdrawn); err != nil {
		return nil, reject("withdrawn is not an RFC 3339 time")
	}
	if strings.HasPrefix(w.ID, "MAL-") {
		a.Kind = advisory.KindMalicious
	}
	if len(w.Aliases) > MaxAliases {
		return nil, reject("%d aliases", len(w.Aliases))
	}
	a.Aliases = aliasesOf(w.ID, w.Aliases)
	a.References = referencesOf(w)
	a.CWEIDs = cweOf(w.DatabaseSpecific.CWEIDs)

	rec := &Record{SHA256: sha256.Sum256(data)}
	if len(w.Affected) > MaxAffected {
		return nil, reject("%d affected entries", len(w.Affected))
	}
	for _, e := range w.Affected {
		if e.Package.Ecosystem != ecosystem {
			continue
		}
		name, ok := ad.AdvisoryName(e.Package.Name)
		if !ok || name == "" || hasControl(name) {
			rec.Unevaluable++
			continue
		}
		aff, unevaluable := affectedOf(ad, name, e.Ranges, e.Versions)
		rec.Unevaluable += unevaluable
		if len(aff.Ranges) == 0 && len(aff.Versions) == 0 {
			continue
		}
		a.Affected = append(a.Affected, aff)
	}

	if len(w.Severity) > maxSeverities {
		w.Severity = w.Severity[:maxSeverities]
	}
	for _, s := range w.Severity {
		switch s.Type {
		case "CVSS_V2", "CVSS_V3", "CVSS_V4":
			if v, err := ScoreVector(s.Score); err == nil {
				rec.Vectors = append(rec.Vectors, v)
			}
		}
	}
	rec.Own = DecideOwn(a.Kind == advisory.KindMalicious, rec.Vectors, qualitativeOf(w.DatabaseSpecific.Severity))
	a.Severity, a.SeveritySource, a.CVSS = rec.Own.Level, rec.Own.Source, rec.Own.CVSS
	rec.Advisory = a
	return rec, nil
}

// qualitativeOf maps the GitHub Advisory Database's database_specific.severity (R-VULN-007 rule 4); "" for anything else.
func qualitativeOf(s string) advisory.Severity {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "LOW":
		return advisory.SeverityLow
	case "MODERATE":
		return advisory.SeverityMedium
	case "HIGH":
		return advisory.SeverityHigh
	case "CRITICAL":
		return advisory.SeverityCritical
	}
	return ""
}

func optTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t.UTC(), err
}

// aliasesOf keeps the aliases that satisfy the identifier rule, once each, sorted, without the record's own identifier.
func aliasesOf(self string, in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if a != self && ValidID(a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// referencesOf keeps at most MaxReferences references with an http or https URL (R-VULN-006).
func referencesOf(w wireRecord) []advisory.Reference {
	out := make([]advisory.Reference, 0, min(len(w.References), MaxReferences))
	for _, r := range w.References {
		if len(out) == MaxReferences {
			break
		}
		if len(r.URL) > maxURL || hasControl(r.URL) {
			continue
		}
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		out = append(out, advisory.Reference{Type: cleanText(r.Type, 32), URL: r.URL})
	}
	return out
}

// cweOf keeps at most MaxCWEs identifiers of the form CWE-<digits>.
func cweOf(in []string) []string {
	out := make([]string, 0, min(len(in), MaxCWEs))
	for _, c := range in {
		if len(out) == MaxCWEs {
			break
		}
		if cwePattern.MatchString(c) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// affectedOf builds one stored entry: the evaluable ranges as published, the versions and the fixed versions normalized by
// the format, and the every-version flag. It returns the number of ranges the format cannot evaluate.
func affectedOf(ad match.Adapter, name string, ranges []struct {
	Type   string           `json:"type"`
	Events []advisory.Event `json:"events"`
}, versions []string,
) (advisory.Affected, int) {
	aff := advisory.Affected{Name: name}
	unevaluable := 0
	for _, r := range ranges {
		t := advisory.RangeType(r.Type)
		if (t != advisory.RangeSemver && t != advisory.RangeEcosystem) || len(r.Events) == 0 {
			continue // GIT ranges and empty ranges have no effect
		}
		events := slices.Clone(r.Events)
		if !evaluable(ad, r.Events) {
			unevaluable++
			// What cannot be evaluated is stored for the record's sake only; no control or format character of it is.
			for i := range events {
				events[i].Version = cleanVersion(events[i].Version)
			}
		}
		aff.Ranges = append(aff.Ranges, advisory.Range{Type: t, Events: events})
		for _, ev := range r.Events {
			if ev.Kind != advisory.EventFixed {
				continue
			}
			f := ev.Version
			if n, ok := ad.AdvisoryVersion(f); ok {
				f = n
			}
			if !hasControl(f) && !slices.Contains(aff.Fixed, f) {
				aff.Fixed = append(aff.Fixed, f)
			}
		}
	}
	for _, v := range versions {
		if n, ok := ad.AdvisoryVersion(v); ok && !hasControl(n) && !slices.Contains(aff.Versions, n) {
			aff.Versions = append(aff.Versions, n)
		}
	}
	aff.EveryVersion = len(versions) == 0 && len(aff.Ranges) == 1 && len(aff.Ranges[0].Events) == 1 &&
		aff.Ranges[0].Events[0] == advisory.Event{Kind: advisory.EventIntroduced, Version: "0"}
	return aff, unevaluable
}

// evaluable reports whether every event version of a range parses in the format's order ("0" of an introduced event needs no
// parsing), the rule by which match.Affects counts a range as unevaluable (R-VULN-009).
func evaluable(ad match.Adapter, events []advisory.Event) bool {
	for _, ev := range events {
		if ev.Kind == advisory.EventIntroduced && ev.Version == "0" {
			continue
		}
		n, ok := ad.AdvisoryVersion(ev.Version)
		if !ok || hasControl(ev.Version) {
			return false
		}
		if _, err := ad.CompareVersions(n, n); err != nil {
			return false
		}
	}
	return true
}

// maxVersion bounds a version of an unevaluable range as it is stored.
const maxVersion = 128

// invisible reports a character that is not displayed as itself: a control character, a format character (the bidirectional
// controls U+202A to U+202E and U+2066 to U+2069, the zero-width characters U+200B to U+200F, the byte order mark U+FEFF, the
// soft hyphen and the like) or a line or paragraph separator. Such a character in a title lets an author spoof what a reader
// sees.
func invisible(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// cleanVersion strips every invisible character, including tab and newline, and cuts the version to maxVersion bytes.
func cleanVersion(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r == utf8.RuneError {
			r = '�'
		}
		if invisible(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxVersion {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cleanText removes the control, format and separator characters other than tab and newline and cuts the text to max bytes on
// a character boundary (R-VULN-006).
func cleanText(s string, max int) string {
	var b strings.Builder
	b.Grow(min(len(s), max))
	for _, r := range s {
		if r == utf8.RuneError {
			r = '�'
		}
		if invisible(r) && r != '\t' && r != '\n' {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hasControl reports a control or format character, which no identifier, name, version or URL of a record may hold.
func hasControl(s string) bool {
	return strings.IndexFunc(s, invisible) >= 0 || !utf8.ValidString(s)
}

// clip shortens a value for an error message.
func clip(s string) string {
	if len(s) > 32 {
		return s[:32] + "…"
	}
	return s
}
