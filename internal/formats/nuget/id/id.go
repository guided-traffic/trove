// Package id validates and normalizes NuGet package IDs (R-NUGET-003).
//
// Two rule sets apply. An ID that enters a local repository must satisfy the restricted ASCII
// rule, ValidateLocal. An ID of a package cached by a remote repository must satisfy NuGet's
// general rule with .NET's definition of a word character, ValidateUpstream, so legacy IDs
// outside ASCII stay restorable. IDs are case-insensitive; the canonical package name is
// Lower, which lowercases ASCII letters only: trove never lowercases a non-ASCII ID itself, so
// no ID can map to different URLs on client and server (Q-NUGET-3).
//
// The package is a leaf: it imports the standard library and internal/errs.
package id

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
)

// CodeInvalid is the error code of every ID this package refuses.
const CodeInvalid = "nuget_invalid_id"

// MaxLength is the longest ID, in characters of the .NET sense (UTF-16 code units; the upstream
// rule admits no character outside the Basic Multilingual Plane, so it is also the number of
// code points).
const MaxLength = 100

// ValidateLocal checks the rule for IDs that enter a local repository: at most MaxLength
// characters matching ^[A-Za-z0-9_]+([.-][A-Za-z0-9_]+)*$, that is ASCII letters, digits and
// "_" in segments separated by a single "." or "-". The result is an error of kind Invalid
// (HTTP 400) or nil.
func ValidateLocal(id string) error {
	return validate(id, localWord, "an ID of a local repository may contain only ASCII letters, digits, \"_\", and single \".\" or \"-\" between them")
}

// ValidateUpstream checks the rule for IDs of packages cached by a remote repository: at most
// MaxLength characters matching ^\w+([.-]\w+)*$, where a word character is a letter, a nonspacing
// mark, a decimal digit or a connector punctuation character (Unicode categories L, Mn, Nd, Pc;
// the .NET definition of \w, which Go's regexp does not share). Characters outside the Basic
// Multilingual Plane are refused, as .NET sees them as surrogates, and so is a trailing line
// feed that a $ anchor would let through.
func ValidateUpstream(id string) error {
	return validate(id, upstreamWord, "an ID may contain only letters, nonspacing marks, digits and connector punctuation, and single \".\" or \"-\" between them")
}

func validate(id string, word func(rune) bool, rule string) error {
	if id == "" {
		return invalid(id, "the ID is empty")
	}
	if !utf8.ValidString(id) {
		return invalid(id, "the ID is not valid UTF-8")
	}
	if utf8.RuneCountInString(id) > MaxLength {
		return invalid(id, "the ID is longer than 100 characters")
	}
	// Words are separated by exactly one "." or "-"; neither starts or ends an ID.
	wordStart := true
	for _, r := range id {
		switch {
		case word(r):
			wordStart = false
		case (r == '.' || r == '-') && !wordStart:
			wordStart = true
		default:
			return invalid(id, rule)
		}
	}
	if wordStart {
		return invalid(id, rule)
	}
	return nil
}

func localWord(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func upstreamWord(r rune) bool {
	// .NET works on UTF-16 code units: a supplementary character is two surrogates (category
	// Cs), which \w does not match.
	return r <= 0xFFFF && unicode.In(r, unicode.L, unicode.Mn, unicode.Nd, unicode.Pc)
}

// Lower returns the canonical package name of an ID: the ID with its ASCII letters lowercased
// and every other character unchanged. For an ID under the local rule this equals .NET's
// ToLowerInvariant.
func Lower(id string) string {
	for i := 0; i < len(id); i++ {
		if c := id[i]; c >= 'A' && c <= 'Z' {
			b := []byte(id)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return id
}

// SameID reports whether two IDs are equal under Unicode simple case folding. R-NUGET-003 uses
// it to hold the ID declared in a package to the ID of the request path that filled the cache.
func SameID(a, b string) bool { return strings.EqualFold(a, b) }

func invalid(id, reason string) *errs.Error {
	return errs.E(errs.Invalid, CodeInvalid, "package ID "+quote(id)+": "+reason)
}

// quote renders a client-supplied ID for an error detail: printable ASCII only and bounded, so
// a detail can neither carry control characters nor grow.
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
