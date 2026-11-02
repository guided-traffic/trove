package claims

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxGlobLength is the longest glob of a condition leaf, in characters: the pattern limit of
// R-AUTHZ-005 (IAM-021), so a stored mapping bounds the cost of every match (Q-CI-28).
const MaxGlobLength = 1024

// The glob of a condition (R-CI-010) has its own semantics, explicit for claim values; the
// separator is always "/":
//
//	*    zero or more characters within one segment (never "/")
//	?    exactly one character other than "/"
//	**   only as a whole segment: in the middle zero or more whole segments, at the end one or more,
//	     at the start zero or more leading segments
//	\x   the literal character x (an escaped "/" is the separator, like an unescaped one)
//
// The whole value must match. There are no character classes and no alternation.

// globTok is one token of a segment pattern.
type globTok struct {
	kind byte // 'l' a literal rune, '*' a run within the segment, '?' one rune
	r    rune
}

// globSeg is one segment of a pattern: a globstar or a list of tokens. In the segment-level match,
// a globstar in the middle or at the start is a "star" over whole segments; a trailing globstar is
// one arbitrary segment followed by such a star (one or more segments).
type globSeg struct {
	star bool // zero or more whole segments
	any  bool // exactly one arbitrary segment
	toks []globTok
}

// GlobValid refuses a pattern the mapping save must not accept (R-CI-010): `a**`, `**b`, and an
// unescaped `[`, `]`, `{` or `}`, a trailing backslash, and a pattern longer than MaxGlobLength
// characters (Q-CI-28).
func GlobValid(pattern string) error {
	if utf8.RuneCountInString(pattern) > MaxGlobLength {
		return errors.New("the glob is longer than 1024 characters")
	}
	for _, seg := range splitEscaped(pattern) {
		if seg == "**" {
			continue
		}
		if err := segmentValid(seg); err != nil {
			return err
		}
	}
	return nil
}

// segmentValid checks one segment that is not a globstar: no two unescaped stars in a row (`a**`,
// `**b`, `***`), no unescaped bracket or brace, no trailing backslash.
func segmentValid(seg string) error {
	escaped, prevStar := false, false
	for _, r := range seg {
		if escaped {
			escaped, prevStar = false, false
			continue
		}
		switch r {
		case '\\':
			escaped, prevStar = true, false
		case '[', ']', '{', '}':
			return errors.New("an unescaped " + string(r) + " (the glob has no character classes and no alternation; escape it as \\" + string(r) + ")")
		case '*':
			if prevStar {
				return errors.New("** is valid only as a whole segment (a/**/b, a/**, **/b), not inside one")
			}
			prevStar = true
		default:
			prevStar = false
		}
	}
	if escaped {
		return errors.New("a trailing backslash escapes nothing")
	}
	return nil
}

// splitEscaped splits a pattern at every "/", escaped or not, keeping the escapes of the other
// characters: a value's "/" is always the separator, so an escaped one is the separator too.
func splitEscaped(pattern string) []string {
	var segs []string
	var b strings.Builder
	escaped := false
	for _, r := range pattern {
		switch {
		case escaped && r == '/':
			// `\/`: the backslash escaped the separator; drop it and split.
			s := b.String()
			segs = append(segs, s[:len(s)-1])
			b.Reset()
			escaped = false
			continue
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '/':
			segs = append(segs, b.String())
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	return append(segs, b.String())
}

// compileGlob turns a pattern into its segments. An invalid pattern compiles leniently (a "**"
// inside a segment is two stars, a bracket is a literal), so GlobMatch never fails on a pattern
// that skipped GlobValid.
func compileGlob(pattern string) []globSeg {
	raw := splitEscaped(pattern)
	segs := make([]globSeg, 0, len(raw)+1)
	for i, s := range raw {
		if s == "**" {
			if i == len(raw)-1 {
				segs = append(segs, globSeg{any: true}, globSeg{star: true})
			} else {
				segs = append(segs, globSeg{star: true})
			}
			continue
		}
		segs = append(segs, globSeg{toks: segmentTokens(s)})
	}
	return segs
}

func segmentTokens(s string) []globTok {
	toks := make([]globTok, 0, len(s))
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			toks = append(toks, globTok{kind: 'l', r: r})
			escaped = false
		case r == '\\':
			escaped = true
		case r == '*':
			toks = append(toks, globTok{kind: '*'})
		case r == '?':
			toks = append(toks, globTok{kind: '?'})
		default:
			toks = append(toks, globTok{kind: 'l', r: r})
		}
	}
	if escaped { // a trailing backslash, which GlobValid refuses: the literal backslash
		toks = append(toks, globTok{kind: 'l', r: '\\'})
	}
	return toks
}

// GlobMatch reports whether the whole value matches the pattern under the glob semantics of R-CI-010
// (separator `/`, `*` within a segment, `?` one character, `**` whole segments, `\x` the literal x).
// ignoreCase applies Unicode simple case folding. The pattern is assumed valid.
//
// Both levels use the greedy match with one remembered star, which is exact for patterns whose
// tokens each consume one unit (a rune within a segment, a segment within the value) and costs at
// most the product of the two lengths, never more: there is no backtracking beyond the last star.
func GlobMatch(pattern, value string, ignoreCase bool) bool {
	segs := compileGlob(pattern)
	vals := strings.Split(value, "/")
	p, v := 0, 0
	starP, starV := -1, 0
	for v < len(vals) {
		switch {
		case p < len(segs) && segs[p].star:
			starP, starV = p, v
			p++
		case p < len(segs) && (segs[p].any || segmentMatch(segs[p].toks, vals[v], ignoreCase)):
			p++
			v++
		case starP >= 0:
			p = starP + 1
			starV++
			v = starV
		default:
			return false
		}
	}
	for p < len(segs) && segs[p].star {
		p++
	}
	return p == len(segs)
}

// segmentMatch matches one segment's tokens against one value segment, rune by rune.
func segmentMatch(toks []globTok, seg string, ignoreCase bool) bool {
	runes := []rune(seg)
	p, v := 0, 0
	starP, starV := -1, 0
	for v < len(runes) {
		switch {
		case p < len(toks) && toks[p].kind == '*':
			starP, starV = p, v
			p++
		case p < len(toks) && (toks[p].kind == '?' || runeEqual(toks[p].r, runes[v], ignoreCase)):
			p++
			v++
		case starP >= 0:
			p = starP + 1
			starV++
			v = starV
		default:
			return false
		}
	}
	for p < len(toks) && toks[p].kind == '*' {
		p++
	}
	return p == len(toks)
}

// runeEqual compares two runes, under Unicode simple case folding when fold is set: the orbit of
// unicode.SimpleFold, the folding the regexp package applies for (?i) and strings.EqualFold for
// equals and in.
func runeEqual(a, b rune, fold bool) bool {
	if a == b {
		return true
	}
	if !fold {
		return false
	}
	for r := unicode.SimpleFold(a); r != a; r = unicode.SimpleFold(r) {
		if r == b {
			return true
		}
	}
	return false
}
