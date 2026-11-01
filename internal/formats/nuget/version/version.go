// Package version parses, normalizes, orders and classifies NuGet package versions and
// dependency version ranges (R-NUGET-003, R-NUGET-004).
//
// A NuGet version is Major[.Minor[.Patch[.Revision]]][-Prerelease][+Metadata]. A Version keeps
// the three forms of R-NUGET-003: the original string, the full normalized form (kept in
// catalog entries and search results) and the URL form (the identity of a version, used in
// URLs, duplicate detection and canonical paths). Ordering is SemVer 2.0.0 precedence with
// NuGet's optional fourth numeric part, pre-release identifiers compared without regard to
// case, and build metadata ignored. SortKey encodes that order as bytes, so PostgreSQL can
// order a bytea column and pick the latest version.
//
// The accepted grammar, the normalized forms and the order agree with the NuGet.Versioning
// library on the golden vectors in testdata/vectors.json, which tools/nugetvectors generates
// from a pinned library version. One deliberate difference: a version string longer than
// MaxLength is refused (Q-NUGET-21).
//
// The package is a leaf: it imports the standard library and internal/errs.
package version

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/guided-traffic/trove/internal/errs"
)

// CodeInvalid is the error code of every version this package refuses.
const CodeInvalid = "nuget_invalid_version"

// MaxLength bounds the string of a version, as given and normalized (Q-NUGET-21).
// NuGet.Versioning has no bound; the gallery refuses versions of more than 64 characters. The
// bound also keeps a numeric pre-release identifier below the 255 digits a SortKey length byte
// can hold.
const MaxLength = 255

// Version is a parsed NuGet version. The zero value is the version 0.0.0 with an empty original
// string. Two Versions are the same version when Equal says so; the == operator also compares
// the original string, the spelling and the build metadata.
type Version struct {
	original string
	parts    [4]uint32 // Major, Minor, Patch, Revision
	release  string    // pre-release label as written, without the leading "-"
	metadata string    // build metadata as written, without the leading "+"
}

// Parse parses a version the way NuGet.Versioning does: white space around the string is
// ignored, as is white space around a numeric part (NuGet trims each part), and one to four
// numeric parts (leading zeros allowed, each at most 2147483647) are
// optionally followed by "-" and a pre-release label of dot-separated, non-empty identifiers
// of [0-9A-Za-z-] whose numeric identifiers have no leading zero, and by "+" and build
// metadata of dot-separated, non-empty identifiers of [0-9A-Za-z-].
func Parse(s string) (Version, error) {
	if len(s) > MaxLength {
		return Version{}, invalid(s, "the version is longer than 255 characters")
	}
	t := strings.TrimFunc(s, unicode.IsSpace)
	if t == "" {
		return Version{}, invalid(s, "the version is empty")
	}

	v := Version{original: s}
	rest := t
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		v.metadata = rest[i+1:]
		rest = rest[:i]
		if !validIdentifiers(v.metadata, true) {
			return Version{}, invalid(s, "the build metadata is not dot-separated identifiers of letters, digits and \"-\"")
		}
	}
	core := rest
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		v.release = rest[i+1:]
		core = rest[:i]
		if !validIdentifiers(v.release, false) {
			return Version{}, invalid(s, "the pre-release label is not dot-separated identifiers of letters, digits and \"-\" without leading zeros")
		}
	}

	n := 0
	for p := range strings.SplitSeq(core, ".") {
		if n == len(v.parts) {
			return Version{}, invalid(s, "the version has more than four numeric parts")
		}
		num, ok := parsePart(strings.TrimFunc(p, unicode.IsSpace))
		if !ok {
			return Version{}, invalid(s, "a numeric part is empty, not a number or larger than 2147483647")
		}
		v.parts[n] = num
		n++
	}
	// Normalizing can add up to four bytes ("0" is "0.0.0"); the normalized form is bounded too,
	// so that it parses again.
	if len(v.FullNormalized()) > MaxLength {
		return Version{}, invalid(s, "the normalized version is longer than 255 characters")
	}
	return v, nil
}

// MustParse is Parse for a version known to be valid, such as a constant of a test. It panics
// otherwise.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

// parsePart parses one numeric part: ASCII digits only, at most MaxInt32.
func parsePart(p string) (uint32, bool) {
	if p == "" {
		return 0, false
	}
	var n int64
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
		if n > math.MaxInt32 {
			return 0, false
		}
	}
	return uint32(n), true
}

// validIdentifiers reports whether s is a non-empty list of dot-separated identifiers of
// [0-9A-Za-z-]; for a pre-release label (metadata false) a numeric identifier must not have a
// leading zero.
func validIdentifiers(s string, metadata bool) bool {
	if s == "" {
		return false
	}
	for id := range strings.SplitSeq(s, ".") {
		if id == "" {
			return false
		}
		numeric := true
		for i := 0; i < len(id); i++ {
			c := id[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				numeric = false
			default:
				return false
			}
		}
		if !metadata && numeric && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

// Original returns the string the version was parsed from, verbatim.
func (v Version) Original() string { return v.original }

// Major, Minor, Patch and Revision return the numeric parts; a missing part is 0.
func (v Version) Major() int    { return int(v.parts[0]) }
func (v Version) Minor() int    { return int(v.parts[1]) }
func (v Version) Patch() int    { return int(v.parts[2]) }
func (v Version) Revision() int { return int(v.parts[3]) }

// Release returns the pre-release label in its original case, empty for a release version.
func (v Version) Release() string { return v.release }

// Metadata returns the build metadata, empty when there is none.
func (v Version) Metadata() string { return v.metadata }

// IsPrerelease reports whether the version has a pre-release label.
func (v Version) IsPrerelease() bool { return v.release != "" }

// Normalized returns the normalized form without build metadata: at least three numeric parts
// without leading zeros, the fourth only when non-zero, the pre-release label in its original
// case.
func (v Version) Normalized() string {
	var b strings.Builder
	v.writeNormalized(&b)
	return b.String()
}

// FullNormalized returns Normalized plus the build metadata (R-NUGET-003): the form of catalog
// entries and search results.
func (v Version) FullNormalized() string {
	var b strings.Builder
	v.writeNormalized(&b)
	if v.metadata != "" {
		b.WriteByte('+')
		b.WriteString(v.metadata)
	}
	return b.String()
}

// URLForm returns the identity of the version: Normalized, lowercased (R-NUGET-003). Versions
// are the same version exactly when their URL forms are equal.
func (v Version) URLForm() string { return strings.ToLower(v.Normalized()) }

func (v Version) writeNormalized(b *strings.Builder) {
	b.WriteString(strconv.Itoa(int(v.parts[0])))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(int(v.parts[1])))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(int(v.parts[2])))
	if v.parts[3] != 0 {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(int(v.parts[3])))
	}
	if v.release != "" {
		b.WriteByte('-')
		b.WriteString(v.release)
	}
}

// String returns FullNormalized.
func (v Version) String() string { return v.FullNormalized() }

// IsSemver2 reports whether the version is SemVer 2.0.0-specific (R-NUGET-004): its pre-release
// label contains a dot, or it carries build metadata. A dependency range adds its own bounds
// (Range.IsSemver2).
func (v Version) IsSemver2() bool {
	return v.metadata != "" || strings.IndexByte(v.release, '.') >= 0
}

// Equal reports whether a and b are the same version: equal URL forms, which is equal
// precedence.
func (v Version) Equal(o Version) bool { return Compare(v, o) == 0 }

// Compare orders two versions: -1 when a sorts below b, 0 for the same version, 1 above. A
// release sorts above its pre-releases; pre-release identifiers are compared one by one,
// numeric ones numerically and below alphanumeric ones, alphanumeric ones without regard to
// case; with equal leading identifiers the shorter label is lower. Build metadata does not
// take part.
func Compare(a, b Version) int {
	for i := range a.parts {
		if a.parts[i] != b.parts[i] {
			if a.parts[i] < b.parts[i] {
				return -1
			}
			return 1
		}
	}
	return compareRelease(a.release, b.release)
}

func compareRelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	for a != "" && b != "" {
		var x, y string
		x, a, _ = strings.Cut(a, ".")
		y, b, _ = strings.Cut(b, ".")
		if c := compareIdentifier(x, y); c != 0 {
			return c
		}
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	}
	return 1
}

func compareIdentifier(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		// No leading zeros (Parse refuses them) and at most ten digits, so a longer
		// number is larger.
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(asciiLower(a), asciiLower(b))
}

// isNumeric reports whether a pre-release identifier counts as a number: ASCII digits only and
// a value of at most 2147483647. NuGet.Versioning orders a longer digit string as text, among
// the alphanumeric identifiers, so a pre-release of 2147483648 sorts below one of "-" or "a"
// but above every identifier that is a number.
func isNumeric(id string) bool {
	if id == "" || len(id) > 10 {
		return false
	}
	var n int64
	for i := 0; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
		n = n*10 + int64(id[i]-'0')
	}
	return n <= math.MaxInt32
}

func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			return strings.ToLower(s)
		}
	}
	return s
}

// Sort key markers (R-NUGET-003).
const (
	keyRelease    = 0xFF // a release version sorts above every pre-release
	keyNumericID  = 0x01 // numeric identifier: marker, one length byte, the digits
	keyAlphaID    = 0x02 // alphanumeric identifier: marker, lowercased ASCII, 0x00
	keyTerminator = 0x00
)

// SortKey returns the byte encoding of the order of Compare: bytes.Compare of two keys has the
// sign of Compare of the versions, and equal keys mean the same version (R-NUGET-003). It is
// four 4-byte big-endian numeric parts, then 0xFF for a release, or for a pre-release one entry
// per identifier (0x01, a length byte and the digits for a numeric identifier; 0x02, the
// lowercased identifier and 0x00 for an alphanumeric one) and a final 0x00. The key is stored
// as a bytea and ordered by PostgreSQL's bytea comparison.
func (v Version) SortKey() []byte {
	key := make([]byte, 0, 4*4+1+len(v.release)+2)
	for _, p := range v.parts {
		key = binary.BigEndian.AppendUint32(key, p)
	}
	if v.release == "" {
		return append(key, keyRelease)
	}
	for id := range strings.SplitSeq(v.release, ".") {
		if isNumeric(id) {
			// At most ten digits (isNumeric).
			key = append(key, keyNumericID, byte(len(id)&0xff))
			key = append(key, id...)
			continue
		}
		key = append(key, keyAlphaID)
		key = append(key, asciiLower(id)...)
		key = append(key, keyTerminator)
	}
	return append(key, keyTerminator)
}

func invalid(s, reason string) *errs.Error {
	return errs.E(errs.Invalid, CodeInvalid, "version "+quote(s)+": "+reason)
}

// quote renders a client-supplied string for an error detail: printable ASCII only and
// bounded, so a detail can neither carry control characters nor grow.
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
