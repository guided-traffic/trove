package deb

import (
	"regexp"
	"strings"
)

// The grammars of R-DEB-002.
var (
	packageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{1,127}$`)
	archRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// suiteRE is the grammar of a suite and of a component of a local or virtual repository, and of the key of a member.
	suiteRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,63}$`)
	epochRE   = regexp.MustCompile(`^[0-9]{1,9}$`)
	upstreamV = regexp.MustCompile(`^[0-9][A-Za-z0-9.+~-]*$`)
	upstreamN = regexp.MustCompile(`^[0-9][A-Za-z0-9.+~]*$`)
	revisionV = regexp.MustCompile(`^[A-Za-z0-9.+~]+$`)
	hexRE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// MaxVersionLength bounds a version (R-DEB-002).
const MaxVersionLength = 128

// patternChars are the characters of a name pattern (R-AUTHZ-005); a string that holds one is a pattern and is not checked against
// the grammar.
const patternChars = `*?[]{},!\`

// ValidPackage reports whether s is a binary package name: lower case, no folding (R-DEB-002).
func ValidPackage(s string) bool { return packageRE.MatchString(s) }

// ValidArchitecture reports whether s is an architecture of a package file or of a setting: the grammar, and neither "any" nor a
// wildcard that contains it as a part ("linux-any", "any-amd64"). "all" is an architecture of a package file (R-DEB-002).
func ValidArchitecture(s string) bool {
	if !archRE.MatchString(s) {
		return false
	}
	for _, part := range strings.Split(s, "-") {
		if part == "any" {
			return false
		}
	}
	return true
}

// ValidSuite reports whether s is a suite or a component of a local or virtual repository (R-DEB-002).
func ValidSuite(s string) bool { return suiteRE.MatchString(s) }

// ValidComponent is [ValidSuite]: both have one grammar.
func ValidComponent(s string) bool { return suiteRE.MatchString(s) }

// ValidSHA256Hex reports whether s is the 64 lower-case hexadecimal digits of a by-hash file name.
func ValidSHA256Hex(s string) bool { return hexRE.MatchString(s) }

// ValidVersion reports whether s is a version of the grammar of R-DEB-002: [<epoch>:]<upstream>[-<revision>] with an epoch of one to
// nine digits, an upstream version that starts with a digit and holds [A-Za-z0-9.+~-] (a hyphen only when a revision follows, never a
// colon), a revision of [A-Za-z0-9.+~]+, in at most 128 characters.
func ValidVersion(s string) bool {
	if s == "" || len(s) > MaxVersionLength {
		return false
	}
	rest := s
	if epoch, after, ok := strings.Cut(s, ":"); ok {
		if !epochRE.MatchString(epoch) {
			return false
		}
		rest = after
	}
	if i := strings.LastIndexByte(rest, '-'); i >= 0 {
		return upstreamV.MatchString(rest[:i]) && revisionV.MatchString(rest[i+1:])
	}
	return upstreamN.MatchString(rest)
}

// StripEpoch returns the version without its epoch, the form a file name carries.
func StripEpoch(v string) string {
	if _, after, ok := strings.Cut(v, ":"); ok {
		return after
	}
	return v
}

// names is the package-name rule of the format (R-AUTHZ-005, R-DEB-001): the package is the binary package name. A name is never
// folded (upper case is refused), so Normalize either returns the string as it is or refuses it; a pattern is left unchanged.
type names struct{}

func (names) Normalize(s string) (string, bool) {
	if strings.ContainsAny(s, patternChars) {
		return s, true
	}
	return s, ValidPackage(s)
}
