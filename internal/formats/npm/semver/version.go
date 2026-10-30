// Package semver implements the version and range semantics npm clients use:
// SemVer 2.0.0 versions with their precedence, and node-semver's range
// language with its pre-release rule (R-NPM-002, R-NPM-007, R-NPM-016,
// R-NPM-048).
//
// The behavior follows node-semver 7.8.5, the library every npm client calls,
// because a range trove accepts, and a version it selects, must be the ones the
// client would accept and select. Where Go cannot reproduce the original
// exactly the difference is listed in Q-NPM-15 and Q-NPM-16 of the npm
// specification. Three entry points parse a version, by decreasing strictness:
//
//   - Parse: SemVer 2.0.0 as written, no "v", no whitespace.
//   - ParseNode: node-semver's "new SemVer(s, {loose})": surrounding
//     whitespace is trimmed and a leading "v" is allowed; loose also allows
//     "=", whitespace after it, leading zeros and a pre-release without its
//     hyphen.
//   - Clean: npm's clean(), which strips leading "=" and "v" characters first.
//
// The package is a leaf: it imports the standard library and internal/errs.
package semver

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
)

// Error codes of the npm error body (R-NPM-005).
const (
	CodeInvalidVersion = "npm_invalid_version"
	CodeInvalidRange   = "npm_invalid_range"
	CodeInvalidTag     = "npm_invalid_tag"
)

// Version is a parsed SemVer version. Pre and Build hold the identifiers
// without separators and are nil when empty, so two parses of one text compare
// equal with reflect.DeepEqual. Numeric pre-release identifiers are kept as
// decimal text without leading zeros, so Compare never converts to a float.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               []string
}

// Parse parses SemVer 2.0.0 as the specification writes it: MAJOR.MINOR.PATCH
// with optional -PRERELEASE and +BUILD, no "v" prefix and no whitespace.
// Components stay within node-semver's range: at most 2^53-1, and the text at
// most 256 characters.
func Parse(s string) (Version, error) {
	if s == "" || s[0] == 'v' || isJSSpace(rune(s[0])) {
		return Version{}, invalidVersion(s)
	}
	if t := jsTrim(s); len(t) != len(s) {
		return Version{}, invalidVersion(s)
	}
	return ParseNode(s, false)
}

// ParseNode parses s as node-semver's "new SemVer(s, {loose})" does.
func ParseNode(s string, loose bool) (Version, error) {
	if utf8.RuneCountInString(s) > maxLength {
		return Version{}, invalidVersion(s)
	}
	re := compile().full
	if loose {
		re = compile().loose
	}
	m := re.FindStringSubmatch(jsTrim(s))
	if m == nil {
		return Version{}, invalidVersion(s)
	}
	var v Version
	var ok bool
	if v.Major, ok = component(m[1]); !ok {
		return Version{}, invalidVersion(s)
	}
	if v.Minor, ok = component(m[2]); !ok {
		return Version{}, invalidVersion(s)
	}
	if v.Patch, ok = component(m[3]); !ok {
		return Version{}, invalidVersion(s)
	}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
		for i, id := range v.Pre {
			v.Pre[i] = normalizeNumeric(id)
		}
	}
	if m[5] != "" {
		v.Build = strings.Split(m[5], ".")
	}
	return v, nil
}

// Clean is npm's clean(): the version after trimming and removing leading "="
// and "v" characters, without build metadata, or false when it is not a valid
// version. R-NPM-007 compares the published version against this form.
func Clean(s string) (string, bool) {
	t := strings.TrimLeft(jsTrim(s), "=v")
	v, err := ParseNode(t, false)
	if err != nil {
		return "", false
	}
	return v.Canonical(), true
}

// component parses a major, minor or patch number of at most 2^53-1.
func component(s string) (uint64, bool) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > maxSafeInteger {
		return 0, false
	}
	return n, true
}

// normalizeNumeric turns a numeric identifier below 2^53-1 into its decimal
// form without leading zeros, as node-semver does when it converts the
// identifier to a number. Larger numeric identifiers stay as written.
func normalizeNumeric(id string) string {
	if !isNumeric(id) {
		return id
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n >= maxSafeInteger {
		return id
	}
	return strconv.FormatUint(n, 10)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Canonical returns node-semver's version string: MAJOR.MINOR.PATCH and the
// pre-release, without build metadata. It is the form R-NPM-007 stores.
func (v Version) Canonical() string {
	var b strings.Builder
	b.WriteString(strconv.FormatUint(v.Major, 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(v.Minor, 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(v.Patch, 10))
	if len(v.Pre) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.Pre, "."))
	}
	return b.String()
}

// String returns the version with its build metadata. Parse(v.String()) equals
// v for every version Parse returned.
func (v Version) String() string {
	if len(v.Build) == 0 {
		return v.Canonical()
	}
	return v.Canonical() + "+" + strings.Join(v.Build, ".")
}

// IsPrerelease reports whether the version has a pre-release part.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// Compare orders by SemVer 2.0.0 precedence and returns -1, 0 or 1. Build
// metadata is ignored.
func Compare(a, b Version) int {
	if c := cmpUint(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpUint(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpUint(a.Patch, b.Patch); c != 0 {
		return c
	}
	return comparePre(a.Pre, b.Pre)
}

// CompareBuild is Compare, then the build identifiers as node-semver's
// compareBuild orders them: a version without build metadata sorts before one
// with it. It is the order of Sort.
func CompareBuild(a, b Version) int {
	if c := Compare(a, b); c != 0 {
		return c
	}
	for i := 0; ; i++ {
		switch {
		case i >= len(a.Build) && i >= len(b.Build):
			return 0
		case i >= len(b.Build):
			return 1
		case i >= len(a.Build):
			return -1
		}
		if c := compareIdentifiers(a.Build[i], b.Build[i]); c != 0 {
			return c
		}
	}
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// comparePre: a version without pre-release is greater than one with; then
// identifier by identifier, a shorter list that is a prefix sorting first.
func comparePre(a, b []string) int {
	switch {
	case len(a) > 0 && len(b) == 0:
		return -1
	case len(a) == 0 && len(b) > 0:
		return 1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdentifiers(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(a)), uint64(len(b)))
}

// compareIdentifiers: numeric identifiers compare by value and sort before
// alphanumeric ones, which compare in ASCII order. Numeric values compare
// exactly at any length; node-semver converts to a float and so cannot tell
// apart numbers above 2^53 (Q-NPM-15).
func compareIdentifiers(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if c := cmpUint(uint64(len(a)), uint64(len(b))); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

// Sort orders versions ascending by CompareBuild, stably.
func Sort(vs []Version) { sortVersions(vs, false) }

// Rsort orders versions descending by CompareBuild, stably.
func Rsort(vs []Version) { sortVersions(vs, true) }

func invalidVersion(s string) *errs.Error {
	return errs.E(errs.Invalid, CodeInvalidVersion, "invalid version "+quote(s))
}

// quote renders client-supplied text for an error detail: printable ASCII only
// and bounded, so a detail can neither carry control characters nor grow.
func quote(s string) string {
	const limit = 64
	var b strings.Builder
	b.WriteByte('"')
	n := 0
	for i := 0; i < len(s) && n < limit; i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			b.WriteByte('?')
		} else {
			b.WriteByte(c)
		}
		n++
	}
	if n < len(s) {
		b.WriteString("...")
	}
	b.WriteByte('"')
	return b.String()
}
