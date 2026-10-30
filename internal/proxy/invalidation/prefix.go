package invalidation

import (
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
)

// MaxPrefixBytes is the longest prefix of an invalidation. It is above the 214
// characters of an npm package name and keeps the change payload (the prefix is
// carried in it) well below the 1 KiB limit of a change message (Q-REPO-100).
const MaxPrefixBytes = 256

// Scopes of an invalidation (R-OPS-021).
const (
	ScopeMetadata = "metadata"
	ScopeNegative = "negative"
	ScopeAll      = "all"
)

// ValidScope reports whether s is one of the three scopes.
func ValidScope(s string) bool {
	return s == ScopeMetadata || s == ScopeNegative || s == ScopeAll
}

// CheckPrefix validates the prefix of an invalidation: empty (the whole
// repository) or a canonical path or package name prefix as a sequence of
// segments, without a leading or trailing slash, without an empty segment and
// without `.` or `..` as a segment (Q-REPO-100). A prefix that could never cover a
// canonical path is refused instead of becoming a silent no-op.
func CheckPrefix(p string) error {
	if p == "" {
		return nil
	}
	bad := func(why string) error {
		return errs.E(errs.Invalid, errs.CodeValidation, "The path prefix "+why+".",
			errs.WithFields(errs.FieldError{Pointer: "/path", Code: "invalid_value", Detail: why}))
	}
	switch {
	case len(p) > MaxPrefixBytes:
		return bad("is longer than 256 bytes")
	case !utf8.ValidString(p):
		return bad("is not valid UTF-8")
	case strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/"):
		return bad("must not start or end with a slash")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return bad("contains a control character")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return bad("contains an empty segment")
		case ".", "..":
			return bad("contains a dot segment")
		}
	}
	return nil
}

// Covers reports whether a prefix covers a canonical path: the prefix is empty,
// equal to the path, or equal to its leading segments. `@acme` covers
// `@acme/util` and does not cover `@acmecorp/util` (R-REPO-045).
func Covers(prefix, path string) bool {
	if prefix == "" || prefix == path {
		return true
	}
	return len(path) > len(prefix) && path[len(prefix)] == '/' && strings.HasPrefix(path, prefix)
}
