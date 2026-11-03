package evaluate

import (
	"sort"

	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The rules of the finding lifecycle of R-VULN-012 that need no database: what the evaluation of a version finds, and what a
// finding does about it.

// matched is one canonical advisory that affects one stored version, with the facts a finding stores of it.
type matched struct {
	ID             string
	Kind           advisory.Kind
	Severity       advisory.Severity
	CVSS           float64
	KnownExploited bool
	ModifiedUs     int64
	// FixAvailable is true when the range that affects the version ends in a fixed event; Fixed lists the fixed events of
	// the affected entries.
	FixAvailable bool
	Fixed        []string
}

// changes lists how the advisory's facts differ from those the finding stored: "severity", "kind", "knownExploited", "cvss",
// "fix" or "modified" alone when only the record changed.
func (m *matched) changes(f *store.Finding) []string {
	var out []string
	if m.Severity != f.Severity {
		out = append(out, "severity")
	}
	if m.Kind != f.Kind {
		out = append(out, "kind")
	}
	if m.KnownExploited != f.KnownExploited {
		out = append(out, "knownExploited")
	}
	if m.CVSS != f.CVSS {
		out = append(out, "cvss")
	}
	if m.FixAvailable != f.FixAvailable || !equalStrings(m.Fixed, f.FixedVersions) {
		out = append(out, "fix")
	}
	if len(out) == 0 && m.ModifiedUs != f.AdvisoryModifiedU {
		out = append(out, "modified")
	}
	return out
}

// material reports whether the change from what the finding stored to what the advisory says now is one that reopens a
// dismissed finding (R-VULN-012): the severity level rose, the kind became malicious, or the advisory became known exploited.
// Text, references, a lower severity or a changed range that still covers the version are not material.
func (m *matched) material(f *store.Finding) (change map[string]any, yes bool) {
	change = map[string]any{}
	if m.Severity.Rank() > f.Severity.Rank() {
		change["severity"] = map[string]string{"from": string(f.Severity), "to": string(m.Severity)}
	}
	if m.Kind == advisory.KindMalicious && f.Kind != advisory.KindMalicious {
		change["kind"] = map[string]string{"from": string(f.Kind), "to": string(m.Kind)}
	}
	if m.KnownExploited && !f.KnownExploited {
		change["knownExploited"] = true
	}
	return change, len(change) > 0
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// redetected is the status a resolved finding returns to when its match returns (R-VULN-012): the status it had before it
// resolved, a blocked finding blocked again; a dismissed finding returns to needs_triage when a material change happened
// meanwhile (reopened is then true).
func redetected(f *store.Finding, m *matched) (status vuln.Status, reopened bool, change map[string]any) {
	before := f.BeforeResolution
	if before == "" || !before.Valid() || before == vuln.StatusResolved {
		before = vuln.StatusNeedsTriage
	}
	if before == vuln.StatusDismissed {
		if ch, yes := m.material(f); yes {
			return vuln.StatusNeedsTriage, true, ch
		}
	}
	return before, false, nil
}

// effectiveStatus is the status a version's finding would have once the evaluation of a fill has run, for the miss of a
// remote repository (R-VULN-015): the stored finding's status, a resolved one's re-detected status, needs_triage for a version
// that has no finding.
func effectiveStatus(f *store.Finding, m *matched) vuln.Status {
	if f == nil {
		return vuln.StatusNeedsTriage
	}
	if f.Status == vuln.StatusResolved {
		st, _, _ := redetected(f, m)
		return st
	}
	if f.Status == vuln.StatusDismissed {
		if _, yes := m.material(f); yes {
			return vuln.StatusNeedsTriage
		}
	}
	return f.Status
}

// pickBlocking chooses, among the findings that block, the one the join of R-VULN-015 returns: a malicious one first, then the
// higher severity, then the smaller advisory identifier.
type blocking struct {
	m      *matched
	id     string
	reason vuln.BlockReason
}

func betterBlock(a, b blocking) bool {
	am, bm := a.reason == vuln.BlockMalicious, b.reason == vuln.BlockMalicious
	switch {
	case am != bm:
		return am
	case a.m.Severity.Rank() != b.m.Severity.Rank():
		return a.m.Severity.Rank() > b.m.Severity.Rank()
	}
	return a.m.ID < b.m.ID
}

// sortedIDs returns the keys of a set of matched advisories in order, so that an evaluation writes in a fixed order.
func sortedIDs(m map[string]*matched) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// union appends the strings of add that are not in list, keeping the order.
func union(list []string, add []string) []string {
	for _, a := range add {
		found := false
		for _, l := range list {
			if l == a {
				found = true
				break
			}
		}
		if !found {
			list = append(list, a)
		}
	}
	return list
}
