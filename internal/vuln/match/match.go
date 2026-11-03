// Package match evaluates stored versions against advisory ranges with a format's own version order
// (docs/spec/vulnerability-scanning.md R-VULN-009).
//
// It is a leaf. The format contract declares formats.VulnerabilityAdapter; this package declares [Adapter], the
// same method set, so that the matcher, the OSV decoder and the evaluation (which sit below internal/formats and
// cannot import it) take what a format implements by structural typing, and the format side never imports
// internal/vuln. A test of the composition root holds the two method sets equal.
package match

// MatcherVersion is the version of the matching rules (R-VULN-011 vuln.matcher_version). Any change of what [Affects]
// returns for the same input raises it: a sync run that finds vuln_source_state.matcher_version of an ecosystem lower
// than this constant requests the full evaluation of that ecosystem and stores the constant.
const MatcherVersion = 1

// Adapter is what the matcher needs of a format: its ecosystem, its normalization of advisory names and versions and its
// version order. It has the method set of formats.VulnerabilityAdapter (R-VULN-009).
type Adapter interface {
	// Ecosystem is the OSV ecosystem name of the format ("npm", "NuGet").
	Ecosystem() string
	// AdvisoryName normalizes a package name of an advisory as the format normalizes stored names; ok is false for a name
	// the format refuses.
	AdvisoryName(name string) (normalized string, ok bool)
	// AdvisoryVersion normalizes a version of an advisory as package_versions.version is normalized.
	AdvisoryVersion(v string) (normalized string, ok bool)
	// CompareVersions orders two normalized versions by the format's precedence (-1, 0, +1); an error for a version that
	// does not parse.
	CompareVersions(a, b string) (int, error)
}

// Result is what [Affects] says about one stored version and one affected entry.
type Result struct {
	// Affected is true when the version is in the entry's versions[], the entry is an every-version entry, or the
	// version lies in one of its evaluable ranges.
	Affected bool
	// FixAvailable is true when the range that affects the version ends in a fixed event (R-VULN-012).
	FixAvailable bool
	// Unevaluable counts the ranges of the entry one of whose event versions does not parse: they match nothing and are
	// counted in trove_vuln_unevaluable_ranges_total (R-VULN-009).
	Unevaluable int
}
