// Package version is the version rule of the Ansible Galaxy format (R-ANS-002): semantic versioning 2.0.0, the only form
// `ansible-galaxy` accepts for a collection. It parses a version strictly (no leading "v", no leading zeros, no empty identifier),
// orders two versions by precedence with build metadata ignored, and derives the sort key of the data port: a byte string whose
// memcmp order is the precedence order, so PostgreSQL can order a version list with `ORDER BY sort_key DESC` and no function.
//
// It is a leaf: it imports the standard library only.
package version

import (
	"bytes"
	"errors"
	"strings"
)

// MaxLength bounds a version string (R-ANS-002). It keeps every length-prefixed part of the sort key below 256 bytes.
const MaxLength = 128

// ErrInvalid is the error of a string that is not a semantic version 2.0.0 within [MaxLength].
var ErrInvalid = errors.New("not a semantic version 2.0.0")

// Semver is a parsed version. The numeric parts are kept as the digits they were written with (a numeric part has no leading zero),
// so a part of any length compares exactly.
type Semver struct {
	// Original is the string as written; a version is stored and served as written (R-ANS-002).
	Original string
	// Major, Minor and Patch are decimal digits without a leading zero.
	Major, Minor, Patch string
	// Pre are the dot-separated identifiers of the pre-release, nil for a release.
	Pre []string
	// Build is the build metadata after "+", "" when there is none. It takes no part in the precedence.
	Build string
}

// Parse reads s as a semantic version 2.0.0 (https://semver.org/spec/v2.0.0.html), without the optional "v" prefix some tools accept.
func Parse(s string) (Semver, error) {
	if s == "" || len(s) > MaxLength {
		return Semver{}, ErrInvalid
	}
	v := Semver{Original: s}
	core := s
	if i := strings.IndexByte(core, '+'); i >= 0 {
		v.Build = core[i+1:]
		core = core[:i]
		if !identifiers(v.Build, false) {
			return Semver{}, ErrInvalid
		}
	}
	if i := strings.IndexByte(core, '-'); i >= 0 {
		pre := core[i+1:]
		core = core[:i]
		if !identifiers(pre, true) {
			return Semver{}, ErrInvalid
		}
		v.Pre = strings.Split(pre, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Semver{}, ErrInvalid
	}
	for _, p := range parts {
		if !numeric(p) || (len(p) > 1 && p[0] == '0') {
			return Semver{}, ErrInvalid
		}
	}
	v.Major, v.Minor, v.Patch = parts[0], parts[1], parts[2]
	return v, nil
}

// Valid reports whether s is a semantic version.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// identifiers checks a dot-separated list of identifiers: [0-9A-Za-z-]+ each. In a pre-release a numeric identifier has no leading
// zero; build identifiers may.
func identifiers(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for i := 0; i < len(id); i++ {
			c := id[i]
			if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
				return false
			}
		}
		if pre && numeric(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func numeric(s string) bool {
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

// Compare orders two versions by the precedence of semantic versioning 2.0.0, build metadata ignored: -1, 0 or +1.
func Compare(a, b Semver) int { return bytes.Compare(SortKey(a), SortKey(b)) }

// Same reports whether two version strings are the same version for immutability: equal in precedence, so versions that differ only
// in build metadata are one (R-ANS-002, R-ANS-008). A string that is no version is only the same as itself.
func Same(a, b string) bool {
	va, ea := Parse(a)
	vb, eb := Parse(b)
	if ea != nil || eb != nil {
		return a == b
	}
	return Compare(va, vb) == 0
}

// The tags of the sort key. A pre-release sorts before the release of its core (preMark < releaseMark); inside a pre-release a numeric
// identifier sorts before an alphanumeric one (numTag < alphaTag) and the end of the list before any identifier (endTag), so a shorter
// list precedes a longer one it is the prefix of.
const (
	endTag      = 0x00
	preMark     = 0x01
	releaseMark = 0x02
	numTag      = 0x02
	alphaTag    = 0x03
)

// SortKey is the byte string whose memcmp order is the precedence order of [Compare]. Build metadata is not part of it, so two
// versions that are the same version have the same key. A numeric part is its digit count (one byte) and its digits: more digits are
// more, equal counts compare by digit, which is the numeric order of numbers without a leading zero.
func SortKey(v Semver) []byte {
	k := make([]byte, 0, 16+len(v.Original))
	for _, n := range []string{v.Major, v.Minor, v.Patch} {
		k = appendNumber(k, n)
	}
	if v.Pre == nil {
		return append(k, releaseMark)
	}
	k = append(k, preMark)
	for _, id := range v.Pre {
		if numeric(id) {
			k = append(k, numTag)
			k = appendNumber(k, id)
			continue
		}
		k = append(k, alphaTag)
		k = append(k, id...)
		k = append(k, endTag)
	}
	return append(k, endTag)
}

// appendNumber appends the length-prefixed digits of a number; the digit count of a bounded version is below 256.
func appendNumber(k []byte, digits string) []byte {
	k = append(k, byte(len(digits))) //nolint:gosec // a part of a version of at most MaxLength characters has fewer than 256 digits
	return append(k, digits...)
}
