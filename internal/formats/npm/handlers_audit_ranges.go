package npm

import (
	"slices"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/semver"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// The text of vulnerable_versions of the answer (R-VULN-017): the affected ranges of an advisory as node-semver text, which
// pnpm and npm evaluate themselves against the installed versions.

// vulnAdapter is the format's VulnerabilityAdapter (vuln.go), which the audit hands to the matcher, so that the audit and
// the evaluation read advisory versions alike (R-VULN-009, Q-VULN-36).
var vulnAdapter formats.VulnerabilityAdapter = (*Format)(nil)

// Wildcard is the text of every version.
const wildcard = "*"

// vulnerableVersions is the vulnerable_versions text of the entries of one advisory for one name: the ranges as node-semver
// text, joined with " || ", then " || =V" for each requested version that the matcher found affected and that the text,
// evaluated without pre-release inclusion as the client does, does not match. The completion is what makes a pre-release
// inside an affected range (OSV order) reported by a client whose own range test would skip it.
func vulnerableVersions(entries []advisory.Affected, affected []requestedVersion) string {
	var parts []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			parts = append(parts, p)
		}
	}
	for _, e := range entries {
		if e.EveryVersion {
			add(wildcard)
			continue
		}
		for _, r := range e.Ranges {
			for _, p := range rangeParts(r) {
				add(p)
			}
		}
		for _, v := range e.Versions {
			if c, ok := semver.Clean(v); ok {
				add("=" + c)
			}
		}
	}
	text := strings.Join(parts, " || ")
	var rng *semver.Range
	if text != "" {
		rng, _ = semver.ParseRange(text, semver.Options{})
	}
	for _, v := range affected {
		if !v.valid {
			continue
		}
		if rng != nil && rng.SatisfiesString(v.canonical) {
			continue
		}
		if p := "=" + v.canonical; !seen[p] {
			seen[p] = true
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " || ")
}

// rangeParts renders the intervals of one OSV range: ">=A <F", ">=A <=L", ">=A", "<F" and "<=L" for an interval that starts
// at introduced "0". A range that is not SEMVER or ECOSYSTEM, or one of whose event versions does not parse, renders
// nothing: it matches nothing either (R-VULN-009).
func rangeParts(r advisory.Range) []string {
	if r.Type != advisory.RangeSemver && r.Type != advisory.RangeEcosystem {
		return nil
	}
	events := make([]advisory.Event, 0, len(r.Events))
	for _, ev := range r.Events {
		if ev.Kind == advisory.EventIntroduced && ev.Version == "0" {
			events = append(events, ev)
			continue
		}
		c, ok := semver.Clean(ev.Version)
		if !ok {
			return nil
		}
		events = append(events, advisory.Event{Kind: ev.Kind, Version: c})
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if (a.Version == "0") != (b.Version == "0") {
			return a.Version == "0"
		}
		if a.Version != "0" {
			x, _ := semver.Parse(a.Version)
			y, _ := semver.Parse(b.Version)
			if c := semver.Compare(x, y); c != 0 {
				return c < 0
			}
		}
		return a.Kind == advisory.EventIntroduced && b.Kind != advisory.EventIntroduced
	})
	var out []string
	open, lower := false, ""
	emit := func(upper string) {
		var p []string
		if lower != "0" {
			p = append(p, ">="+lower)
		}
		if upper != "" {
			p = append(p, upper)
		}
		if len(p) == 0 {
			p = []string{wildcard}
		}
		out = append(out, strings.Join(p, " "))
		open = false
	}
	for _, ev := range events {
		switch ev.Kind {
		case advisory.EventIntroduced:
			if !open {
				open, lower = true, ev.Version
			}
		case advisory.EventFixed:
			if open {
				emit("<" + ev.Version)
			}
		case advisory.EventLastAffected:
			if open {
				emit("<=" + ev.Version)
			}
		case advisory.EventLimit:
			if open {
				emit("<" + ev.Version)
			}
			return out
		}
	}
	if open {
		emit("")
	}
	return slices.Clone(out)
}
