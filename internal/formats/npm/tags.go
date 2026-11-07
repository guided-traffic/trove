package npm

import (
	"maps"

	"github.com/guided-traffic/trove/internal/formats/npm/semver"
)

// The dist-tag rules of R-NPM-016 are decisions on the SemVer order, so they live
// here, next to the order; the store persists the map they produce. Every function
// takes the tags as they were read under the package row lock and returns a new map.

// tagAfterPublish applies the tag of a publish to the tags the package holds. A
// publish under latest does not move it backwards: when the new version is lower
// than the current latest, or is a pre-release while the current latest is not,
// the version is stored, latest stays and kept is true (the latest guard, when
// guard is on). A package with a version always has latest: a first publish under
// another tag sets it too.
func tagAfterPublish(cur map[string]string, tag, version string, guard bool) (tags map[string]string, kept bool) {
	tags = maps.Clone(cur)
	if tags == nil {
		tags = map[string]string{}
	}
	if tag == "latest" {
		if now, ok := tags["latest"]; ok && guard && lowersLatest(now, version) {
			return tags, true
		}
		tags["latest"] = version
		return tags, false
	}
	tags[tag] = version
	if _, ok := tags["latest"]; !ok {
		tags["latest"] = version
	}
	return tags, false
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
