package index

import (
	"path"
	"slices"
	"time"
)

// KeptSuperseded is the number of superseded generations that stay whatever their age (R-DEB-012, Q-DEB-51), besides the current one.
const KeptSuperseded = 3

// Generation is a published generation of a suite as retention sees it.
type Generation struct {
	Number int64
	// Files maps the path of each index file relative to dists/<suite>/ to its SHA-256 (hex).
	Files map[string]string
	// SupersededAt is nil for the current generation.
	SupersededAt *time.Time
}

// Retain splits the generations of a suite into those whose by-hash files stay and those whose files are given up: the current one,
// the superseded ones for keep after they were superseded, and the newest KeptSuperseded of the rest whatever their age (R-DEB-012).
// The generations are in any order; the result is in generation order.
func Retain(gens []Generation, now time.Time, keep time.Duration) (kept, dropped []Generation) {
	sorted := slices.Clone(gens)
	slices.SortFunc(sorted, func(a, b Generation) int { return int(a.Number - b.Number) })
	superseded := 0
	for i := len(sorted) - 1; i >= 0; i-- {
		g := sorted[i]
		switch {
		case g.SupersededAt == nil:
			kept = append(kept, g)
		case superseded < KeptSuperseded || now.Sub(*g.SupersededAt) < keep:
			superseded++
			kept = append(kept, g)
		default:
			dropped = append(dropped, g)
		}
	}
	slices.Reverse(kept)
	slices.Reverse(dropped)
	return kept, dropped
}

// ByHashPath is the path of an index file's by-hash copy relative to dists/<suite>/: the file's directory, then by-hash/SHA256/<hex>.
func ByHashPath(file, sha256hex string) string {
	dir := path.Dir(file)
	if dir == "." {
		dir = ""
	} else {
		dir += "/"
	}
	return dir + "by-hash/SHA256/" + sha256hex
}

// ByHashSet is the by-hash paths (relative to dists/<suite>/) that the generations name.
func ByHashSet(gens ...Generation) map[string]bool {
	out := map[string]bool{}
	for _, g := range gens {
		for file, sum := range g.Files {
			out[ByHashPath(file, sum)] = true
		}
	}
	return out
}

// Given up are the paths, relative to dists/<suite>/, that a commit removes: the by-hash paths of the dropped generations that no kept
// generation (the new one included) names, and the plain index files of the previous current generation that the new one does not
// have (for instance a Packages.xz after xz was taken out of deb.compression).
func GivenUp(kept, dropped []Generation, previous, next map[string]string) []string {
	keep := ByHashSet(kept...)
	var out []string
	for p := range ByHashSet(dropped...) {
		if !keep[p] {
			out = append(out, p)
		}
	}
	for file := range previous {
		if _, ok := next[file]; !ok {
			out = append(out, file)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// PickDate chooses the Release Date of a build (Q-DEB-30, Q-DEB-40): the database time minus 60 s in whole seconds, at least one second
// after the previous Date and not before the earliest time every signing key may sign. A Date is never later than the database time
// minus 59 s: when the rule asks for a later one, wait says how long to wait until it is no longer ahead, and the caller asks the
// database for its time again then.
func PickDate(now time.Time, previous *time.Time, earliest time.Time) (date time.Time, wait time.Duration) {
	date = now.Add(-60 * time.Second).UTC().Truncate(time.Second)
	if previous != nil {
		if floor := previous.UTC().Truncate(time.Second).Add(time.Second); floor.After(date) {
			date = floor
		}
	}
	if earliest.After(date) {
		date = earliest.UTC().Truncate(time.Second)
		if date.Before(earliest) {
			date = date.Add(time.Second)
		}
	}
	if ceiling := now.Add(-59 * time.Second); date.After(ceiling) {
		wait = date.Sub(ceiling)
	}
	return date, wait
}
