package match

import (
	"slices"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// zeroVersion is the version of an introduced event that sorts before every version (the OSV schema).
const zeroVersion = "0"

// Affects reports whether the stored version is affected by an affected entry of an advisory of a's ecosystem, by the
// evaluation algorithm of the OSV schema over the format's own order (R-VULN-009): the version is listed in
// versions[] (normalized string equality), or the entry is an every-version entry (matched without parsing the
// version), or the version lies in one of the SEMVER or ECOSYSTEM ranges, evaluated over the range's events in the
// format's order with "0" sorting before every version, fixed and last_affected ending an interval and limit bounding
// the range. GIT ranges are ignored. The caller has matched the entry's name and dropped withdrawn advisories.
//
// version is a stored (normalized) version. A range one of whose event versions does not parse is skipped and counted in
// Result.Unevaluable, whether or not the stored version parses, so that the count depends on the advisory alone; a stored
// version that does not parse matches only through versions[] and every_version. Affects never reads a database and never
// fails: what it cannot evaluate is counted.
func Affects(a Adapter, e advisory.Affected, version string) Result {
	return Prepare(a, e).Affects(version)
}

// Entry is an affected entry prepared for many versions: its ranges are normalized and sorted once, so that a client audit
// that asks for thousands of versions of one name does not normalize and sort the same events for each of them (R-VULN-017).
// It gives the answers of [Affects] and is safe for concurrent use.
type Entry struct {
	a           Adapter
	every       bool
	versions    []string
	ranges      [][]advisory.Event // sorted events of the evaluable ranges
	unevaluable int
}

// Prepare normalizes the entry's ranges with the adapter and sorts their events.
func Prepare(a Adapter, e advisory.Affected) *Entry {
	p := &Entry{a: a, every: e.EveryVersion, versions: e.Versions}
	if p.every {
		return p
	}
	for _, r := range e.Ranges {
		if r.Type != advisory.RangeSemver && r.Type != advisory.RangeEcosystem {
			continue
		}
		events, ok := normalizeEvents(a, r.Events)
		if !ok {
			p.unevaluable++
			continue
		}
		p.ranges = append(p.ranges, sortEvents(a, events))
	}
	return p
}

// Affects is [Affects] for the prepared entry.
func (p *Entry) Affects(version string) Result {
	if p.every {
		return Result{Affected: true}
	}
	res := Result{Affected: slices.Contains(p.versions, version), Unevaluable: p.unevaluable}
	if _, err := p.a.CompareVersions(version, version); err != nil {
		return res // a stored version that does not parse matches only through versions[] and every_version
	}
	for _, events := range p.ranges {
		if in, fix := evalSorted(p.a, events, version); in {
			res.Affected = true
			res.FixAvailable = res.FixAvailable || fix
		}
	}
	return res
}

// normalizeEvents normalizes the event versions with the adapter and checks that each parses; ok is false for a range
// that cannot be evaluated. The result is a copy.
func normalizeEvents(a Adapter, in []advisory.Event) ([]advisory.Event, bool) {
	out := make([]advisory.Event, len(in))
	for i, ev := range in {
		out[i] = ev
		if ev.Kind == advisory.EventIntroduced && ev.Version == zeroVersion {
			continue
		}
		n, ok := a.AdvisoryVersion(ev.Version)
		if !ok {
			return nil, false
		}
		if _, err := a.CompareVersions(n, n); err != nil {
			return nil, false
		}
		out[i].Version = n
	}
	return out, true
}

// sortEvents orders the events by version, "0" first and an introduced event before the others at the same version.
func sortEvents(a Adapter, events []advisory.Event) []advisory.Event {
	cmp := func(x, y string) int {
		c, _ := a.CompareVersions(x, y) // every version was checked to parse
		return c
	}
	sorted := slices.Clone(events)
	slices.SortStableFunc(sorted, func(x, y advisory.Event) int {
		switch {
		case x.Version == zeroVersion && y.Version != zeroVersion:
			return -1
		case y.Version == zeroVersion && x.Version != zeroVersion:
			return 1
		case x.Version == y.Version:
			return rankOf(x.Kind) - rankOf(y.Kind)
		}
		if c := cmp(x.Version, y.Version); c != 0 {
			return c
		}
		return rankOf(x.Kind) - rankOf(y.Kind) // two spellings of one version
	})
	return sorted
}

// evalSorted runs the state machine of the OSV schema over the sorted events. in is whether the version is affected; fix
// whether the interval that contains it ends in a fixed event.
func evalSorted(a Adapter, sorted []advisory.Event, version string) (in, fix bool) {
	cmp := func(x, y string) int {
		c, _ := a.CompareVersions(x, y)
		return c
	}
	opened := -1
	for i, ev := range sorted {
		switch ev.Kind {
		case advisory.EventLimit:
			if cmp(version, ev.Version) >= 0 {
				return false, false
			}
		case advisory.EventIntroduced:
			if opened < 0 && (ev.Version == zeroVersion || cmp(version, ev.Version) >= 0) {
				opened = i
			}
		case advisory.EventFixed:
			if opened >= 0 && cmp(version, ev.Version) >= 0 {
				opened = -1
			}
		case advisory.EventLastAffected:
			if opened >= 0 && cmp(version, ev.Version) > 0 {
				opened = -1
			}
		}
	}
	if opened < 0 {
		return false, false
	}
	// The interval ends at the first terminating event after the one that opened it.
	for _, ev := range sorted[opened+1:] {
		switch ev.Kind {
		case advisory.EventFixed:
			return true, true
		case advisory.EventLastAffected:
			return true, false
		case advisory.EventIntroduced, advisory.EventLimit:
		}
	}
	return true, false
}

// rankOf orders the kinds at one version: introduced first, so that a range that introduces and fixes at the same
// version is empty.
func rankOf(k advisory.EventKind) int {
	if k == advisory.EventIntroduced {
		return 0
	}
	return 1
}
