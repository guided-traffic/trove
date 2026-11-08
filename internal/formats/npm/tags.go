package npm

import (
	"maps"

	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// The dist-tag rules of R-NPM-016 are decisions on the SemVer order, so they live
// here, next to the order; the store persists the map they produce. Every function
// takes the tags as they were read under the package row lock and returns a new map.

// tagOutcome is what a publish did with the tag it carried.
type tagOutcome uint8

const (
	// tagMoved: the tag points to the new version.
	tagMoved tagOutcome = iota
	// tagGuarded: the latest guard kept latest where it was.
	tagGuarded
	// tagHeld: the tag points to a version the publisher may not read and stays there (Q-NPM-85).
	tagHeld
)

// tagAfterPublish applies the tag of a publish to the tags the package holds. A
// tag that points to a version the publisher may not read (readable false; nil
// reads every version) stays where it is, latest included and whatever the new
// version is: a write never changes what the caller may not read (Q-NPM-85). A
// publish under latest does not move it backwards: when the new version is lower
// than the current latest, or is a pre-release while the current latest is not,
// the version is stored and latest stays (the latest guard, when guard is on). A
// package with a version always has latest: a first publish under another tag sets
// it too.
func tagAfterPublish(cur map[string]string, tag, version string, guard bool, readable func(string) bool) (map[string]string, tagOutcome) {
	tags := maps.Clone(cur)
	if tags == nil {
		tags = map[string]string{}
	}
	if now, ok := tags[tag]; ok && readable != nil && !readable(now) {
		return tags, tagHeld
	}
	if tag == "latest" {
		if now, ok := tags["latest"]; ok && guard && lowersLatest(now, version) {
			return tags, tagGuarded
		}
		tags["latest"] = version
		return tags, tagMoved
	}
	tags[tag] = version
	if _, ok := tags["latest"]; !ok {
		tags["latest"] = version
	}
	return tags, tagMoved
}

// lowersLatest reports whether making version the latest would move it backwards or
// put a pre-release in front of a release.
func lowersLatest(current, version string) bool {
	cur, err1 := semver.Parse(current)
	next, err2 := semver.Parse(version)
	if err1 != nil || err2 != nil {
		return false
	}
	return semver.Compare(next, cur) < 0 || next.IsPrerelease() && !cur.IsPrerelease()
}

// tagsAfterRemoval moves the tags off a version that is removed (R-NPM-016); the rule is
// semver.TagsAfterRemoval, shared with the restore's journal replay (R-NPM-047).
func tagsAfterRemoval(cur map[string]string, removed string, remaining []string) map[string]string {
	return semver.TagsAfterRemoval(cur, removed, remaining)
}

// highest returns the highest non-pre-release version of vs, or the highest version
// when all are pre-releases.
func highest(vs []string) (string, bool) { return semver.Highest(vs) }
