package semver

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// The grammar below is node-semver's (internal/re.js, 7.8.5) written for Go's
// regexp package. Go's engine is leftmost-first like the one the original
// runs on, so a pattern selects the same match; it is also linear in the
// input, which keeps an administrator-supplied range from costing more than
// its length. Two details follow the original's "safe" patterns: a digit run
// is bounded by maxLength and a build or letter run by maxLength-6, and every
// "\s*" between range tokens is "\s{0,1}", because a range reaches the token
// patterns after all whitespace has been collapsed to single spaces.

const (
	maxLength      = 256
	maxBuildLength = maxLength - 6

	// maxSafeInteger is 2^53-1, the largest number node-semver accepts for a
	// major, minor or patch component.
	maxSafeInteger = 1<<53 - 1
)

// jsSpace is the character class of ECMAScript's WhiteSpace and
// LineTerminator, the characters "\s" and String.prototype.trim know.
const jsSpace = `\t\n\x0b\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

const (
	sp = `[` + jsSpace + `]`

	dig    = `\d{1,256}`
	digOpt = `\d{0,256}`

	numeric      = `0|[1-9]` + digOpt
	numericLoose = dig
	nonNumeric   = digOpt + `[a-zA-Z-][a-zA-Z0-9-]{0,250}`

	mainVersion      = `(` + numeric + `)\.(` + numeric + `)\.(` + numeric + `)`
	mainVersionLoose = `(` + numericLoose + `)\.(` + numericLoose + `)\.(` + numericLoose + `)`

	preID      = `(?:` + nonNumeric + `|` + numeric + `)`
	preIDLoose = `(?:` + nonNumeric + `|` + numericLoose + `)`

	prerelease      = `(?:-(` + preID + `(?:\.` + preID + `)*))`
	prereleaseLoose = `(?:-?(` + preIDLoose + `(?:\.` + preIDLoose + `)*))`

	buildID = `[a-zA-Z0-9-]{1,250}`
	build   = `(?:\+(` + buildID + `(?:\.` + buildID + `)*))`

	fullPlain  = `v?` + mainVersion + prerelease + `?` + build + `?`
	loosePlain = `[v=` + jsSpace + `]*` + mainVersionLoose + prereleaseLoose + `?` + build + `?`

	gtlt = `((?:<|>)?=?)`

	xID      = numeric + `|x|X|\*`
	xIDLoose = numericLoose + `|x|X|\*`

	xPlain = `[v=` + jsSpace + `]*(` + xID + `)` +
		`(?:\.(` + xID + `)` +
		`(?:\.(` + xID + `)` +
		`(?:` + prerelease + `)?` + build + `?` +
		`)?)?`
	xPlainLoose = `[v=` + jsSpace + `]*(` + xIDLoose + `)` +
		`(?:\.(` + xIDLoose + `)` +
		`(?:\.(` + xIDLoose + `)` +
		`(?:` + prereleaseLoose + `)?` + build + `?` +
		`)?)?`

	loneTilde = `(?:~>?)`
	loneCaret = `(?:\^)`
)

// tokens holds the compiled patterns. They compile on first use, so a binary
// that links the package but never parses a version pays nothing at start-up.
type tokens struct {
	full, loose                *regexp.Regexp
	build, buildStrip          *regexp.Regexp
	hyphen, hyphenLoose        *regexp.Regexp
	comparatorTrim             *regexp.Regexp
	tildeTrim, caretTrim       *regexp.Regexp
	tilde, tildeLoose          *regexp.Regexp
	caret, caretLoose          *regexp.Regexp
	xrange, xrangeLoose        *regexp.Regexp
	star, gte0, gte0pre        *regexp.Regexp
	comparator, comparatorLoos *regexp.Regexp
}

var compile = sync.OnceValue(func() *tokens {
	c := func(s string) *regexp.Regexp { return regexp.MustCompile(s) }
	return &tokens{
		full:  c(`^` + fullPlain + `$`),
		loose: c(`^` + loosePlain + `$`),
		build: c(build),
		// The stripper of Range.parseRange is the unbounded pattern.
		buildStrip: c(`(?:\+([a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)*))`),

		hyphen:      c(`^` + sp + `{0,1}(` + xPlain + `)` + sp + `{1,1}-` + sp + `{1,1}(` + xPlain + `)` + sp + `{0,1}$`),
		hyphenLoose: c(`^` + sp + `{0,1}(` + xPlainLoose + `)` + sp + `{1,1}-` + sp + `{1,1}(` + xPlainLoose + `)` + sp + `{0,1}$`),

		comparatorTrim: c(`(` + sp + `{0,1})` + gtlt + sp + `{0,1}(` + loosePlain + `|` + xPlain + `)`),
		tildeTrim:      c(`(` + sp + `{0,1})` + loneTilde + sp + `{1,1}`),
		caretTrim:      c(`(` + sp + `{0,1})` + loneCaret + sp + `{1,1}`),

		tilde:       c(`^` + loneTilde + xPlain + `$`),
		tildeLoose:  c(`^` + loneTilde + xPlainLoose + `$`),
		caret:       c(`^` + loneCaret + xPlain + `$`),
		caretLoose:  c(`^` + loneCaret + xPlainLoose + `$`),
		xrange:      c(`^` + gtlt + sp + `{0,1}` + xPlain + `$`),
		xrangeLoose: c(`^` + gtlt + sp + `{0,1}` + xPlainLoose + `$`),

		star:    c(`(<|>)?=?` + sp + `{0,1}\*`),
		gte0:    c(`^` + sp + `{0,1}>=` + sp + `{0,1}0\.0\.0` + sp + `{0,1}$`),
		gte0pre: c(`^` + sp + `{0,1}>=` + sp + `{0,1}0\.0\.0-0` + sp + `{0,1}$`),

		comparator:     c(`^` + gtlt + sp + `{0,1}(` + fullPlain + `)$|^$`),
		comparatorLoos: c(`^` + gtlt + sp + `{0,1}(` + loosePlain + `)$|^$`),
	}
})

// isJSSpace reports whether r is one of the characters of jsSpace.
func isJSSpace(r rune) bool {
	switch {
	case r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' ':
		return true
	case r == 0xa0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a):
		return true
	case r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff:
		return true
	}
	return false
}

// jsTrim is String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// collapseSpace is s.replace(/\s+/g, ' ').
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if isJSSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
			}
			inSpace = true
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// splitSpace is s.split(/\s+/): runs of whitespace separate, leading and
// trailing runs yield an empty first or last element, and "" yields [""].
func splitSpace(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isJSSpace(r) {
			i += size
			continue
		}
		out = append(out, s[start:i])
		i += size
		for i < len(s) {
			r, size := utf8.DecodeRuneInString(s[i:])
			if !isJSSpace(r) {
				break
			}
			i += size
		}
		start = i
	}
	return append(out, s[start:])
}
