package semver

import (
	"maps"

	"github.com/guided-traffic/trove/internal/errs"
)

// MaxTagLength is the longest dist-tag name (R-NPM-007).
const MaxTagLength = 128

// ValidateTag checks a dist-tag name (R-NPM-007, R-NPM-016): 1 to
// MaxTagLength characters from letters, digits, "." "_" and "-", starting with
// a letter or digit, and not a valid SemVer range. A tag that is a range ("1",
// "1.x", "v1.4", "x", "1.2.3") cannot be told apart from a version selector in
// "npm install pkg@1.x", and npm's own dist-tag command refuses it, so trove
// does too. The range test is the strict one of npm's dist-tag command.
func ValidateTag(tag string) error {
	if !validTagText(tag) {
		return errs.E(errs.Invalid, CodeInvalidTag, "invalid dist-tag "+quote(tag)+": a tag has 1 to 128 letters, digits, \".\", \"_\" or \"-\" and starts with a letter or digit")
	}
	if Validate(tag) == nil {
		return errs.E(errs.Invalid, CodeInvalidTag, "invalid dist-tag "+quote(tag)+": a tag must not be a valid SemVer range")
	}
	return nil
}

func validTagText(tag string) bool {
	if tag == "" || len(tag) > MaxTagLength {
		return false
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if alnum {
			continue
		}
		if i == 0 || (c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}

// TagsAfterRemoval moves the dist-tags off a version that is removed: the other tags that
// pointed to it are dropped, and latest moves to the highest remaining non-pre-release
// version, or to the highest version when all are pre-releases (R-NPM-016). With no
// version left there is no latest. remaining lists the versions that stay. The result is a
// new map. The unpublish and the restore's journal replay (R-NPM-047) apply this rule.
func TagsAfterRemoval(cur map[string]string, removed string, remaining []string) map[string]string {
	tags := maps.Clone(cur)
	if tags == nil {
		tags = map[string]string{}
	}
	for tag, v := range tags {
		if v == removed && tag != "latest" {
			delete(tags, tag)
		}
	}
	if tags["latest"] == removed {
		if v, ok := Highest(remaining); ok {
			tags["latest"] = v
		} else {
			delete(tags, "latest")
		}
	}
	return tags
}

// Highest returns the highest non-pre-release version of vs, or the highest version when
// all are pre-releases; versions that do not parse are ignored.
func Highest(vs []string) (string, bool) {
	var best, bestPre *Version
	for _, s := range vs {
		v, err := Parse(s)
		if err != nil {
			continue
		}
		slot := &best
		if v.IsPrerelease() {
			slot = &bestPre
		}
		if *slot == nil || Compare(v, **slot) > 0 {
			c := v
			*slot = &c
		}
	}
	switch {
	case best != nil:
		return best.Canonical(), true
	case bestPre != nil:
		return bestPre.Canonical(), true
	}
	return "", false
}
