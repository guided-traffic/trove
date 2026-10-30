// Package name parses, validates and normalizes npm package names (R-NPM-002).
//
// Two rule sets apply. Looking a package up accepts every name a registry can
// hold, legacy upstream names with capitals included, and refuses only what
// cannot be a name or would address something else: Parse and ParseEscaped.
// Publishing a new package must satisfy npm's rules for new names:
// ValidateNew. The canonical form, "@scope/name" or "name", is the package
// identity of the request descriptor, of include and exclude patterns and of
// permission and claim coordinates.
//
// The package is a leaf: it imports the standard library and internal/errs.
package name

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
)

// CodeInvalid is the error code of every name this package refuses (R-NPM-005).
const CodeInvalid = "npm_invalid_name"

const (
	// MaxNewLength is the longest name of a new package, scope included
	// (R-NPM-002).
	MaxNewLength = 214

	// MaxLookupLength bounds a name that is only looked up (Q-NPM-14). npm's
	// limit for new names is 214; legacy names may exceed it, so the lookup
	// bound is wider but still keeps a client-chosen name from growing keys.
	MaxLookupLength = 512
)

// Name is a parsed package name. The zero value is not a name.
type Name struct {
	scope string // without the leading "@"; empty for an unscoped name
	pkg   string
}

// Parse validates an already decoded name for lookup (R-NPM-002): at most one
// "/", which then separates a scope starting with "@" from the name; no empty,
// "." or ".." segment; no NUL or other control character, backslash or "%";
// valid UTF-8; at most MaxLookupLength bytes. Case is kept, so legacy upstream
// names are served unchanged.
func Parse(decoded string) (Name, error) {
	if decoded == "" {
		return Name{}, invalid(decoded, "the name is empty")
	}
	if len(decoded) > MaxLookupLength {
		return Name{}, invalid(decoded, "the name is too long")
	}
	if !utf8.ValidString(decoded) {
		return Name{}, invalid(decoded, "the name is not valid UTF-8")
	}
	for i := 0; i < len(decoded); i++ {
		switch c := decoded[i]; {
		case c < 0x20 || c == 0x7f:
			return Name{}, invalid(decoded, "the name contains a control character")
		case c == '\\':
			return Name{}, invalid(decoded, "the name contains a backslash")
		case c == '%':
			// One decoding step has run; a second layer of escapes is aimed at an
			// upstream (R-REPO-025).
			return Name{}, invalid(decoded, "the name contains a percent sign")
		}
	}
	if decoded == "-" {
		return Name{}, invalid(decoded, "the name is reserved for protocol operations")
	}
	first, second, scoped := strings.Cut(decoded, "/")
	if !scoped {
		if strings.HasPrefix(decoded, "@") {
			return Name{}, invalid(decoded, "a scope needs a name after it")
		}
		if badSegment(decoded) {
			return Name{}, invalid(decoded, "the name is an empty, \".\" or \"..\" segment")
		}
		return Name{pkg: decoded}, nil
	}
	if strings.Contains(second, "/") {
		return Name{}, invalid(decoded, "the name has more than one \"/\"")
	}
	scope, ok := strings.CutPrefix(first, "@")
	if !ok {
		return Name{}, invalid(decoded, "a name with \"/\" must start with a scope \"@\"")
	}
	if badSegment(scope) || badSegment(second) {
		return Name{}, invalid(decoded, "the scope or the name is an empty, \".\" or \"..\" segment")
	}
	return Name{scope: scope, pkg: second}, nil
}

// ParseEscaped percent-decodes a name exactly once and validates it with
// Parse. The scope separator may arrive as "/", "%2f" or "%2F" and the leading
// "@" as "%40" (R-NPM-002); a "%" left after the decoding step is refused.
func ParseEscaped(escaped string) (Name, error) {
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return Name{}, invalid(escaped, "the name has an invalid percent escape")
	}
	return Parse(decoded)
}

// Normalize returns the canonical form of a possibly escaped name. It is
// idempotent: the canonical form holds no "%", so a second pass changes
// nothing.
func Normalize(escaped string) (string, error) {
	n, err := ParseEscaped(escaped)
	if err != nil {
		return "", err
	}
	return n.String(), nil
}

// badSegment reports a segment that is empty or a path traversal element.
func badSegment(s string) bool { return s == "" || s == "." || s == ".." }

// String returns the canonical form: "@scope/name" or "name".
func (n Name) String() string {
	if n.scope == "" {
		return n.pkg
	}
	return "@" + n.scope + "/" + n.pkg
}

// Scope returns the scope without its "@", or "" for an unscoped name.
func (n Name) Scope() string { return n.scope }

// Package returns the part after the scope, or the whole unscoped name.
func (n Name) Package() string { return n.pkg }

// IsScoped reports whether the name has a scope.
func (n Name) IsScoped() bool { return n.scope != "" }

// EscapedPath returns the form npm clients put in a request path:
// "@scope%2fname", with each part escaped as a path segment. ParseEscaped
// inverts it.
func (n Name) EscapedPath() string {
	if n.scope == "" {
		return url.PathEscape(n.pkg)
	}
	return "@" + url.PathEscape(n.scope) + "%2f" + url.PathEscape(n.pkg)
}

// Fold returns the look-alike key of the name; see the function Fold.
func (n Name) Fold() string { return Fold(n.String()) }

// Fold lower-cases an ASCII name and removes "-", "_" and "." from it, scope
// included. Two new names with the same fold are look-alikes; the column
// npm_packages.name_fold stores it (R-NPM-002). Only ASCII is folded: names
// that pass ValidateNew are ASCII, and a Unicode case mapping is neither
// stable across PostgreSQL locales nor guaranteed idempotent. Fold is
// idempotent.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '-' || c == '_' || c == '.':
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// LookAlike reports whether two names differ but fold to the same key, the
// case R-NPM-002 refuses for a new name in a local repository.
func LookAlike(a, b Name) bool { return a != b && a.Fold() == b.Fold() }

func invalid(name, reason string) *errs.Error {
	return errs.E(errs.Invalid, CodeInvalid, "package name "+quote(name)+": "+reason)
}

// quote renders a client-supplied name for an error detail: printable ASCII
// only and bounded, so a detail can neither carry control characters nor grow.
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
