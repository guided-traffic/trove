package semver

import "github.com/guided-traffic/trove/internal/errs"

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
