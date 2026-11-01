package repository

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// PatternSubject is what a repository's include and exclude patterns match
// (R-REPO-005): the normalized package name, or the canonical path. The catalog
// owns the type: it is the value of the format's PatternSubject fact.
type PatternSubject = catalog.PatternSubject

// The subjects.
const (
	SubjectName = catalog.SubjectName
	SubjectPath = catalog.SubjectPath
)

// Limits of a pattern list (R-REPO-005).
const (
	MaxPatterns      = 100
	MaxPatternLength = 1024
)

// Pattern is a compiled include or exclude pattern. The syntax is that of
// R-AUTHZ-005:
//
//   - name subject: `*` any run of characters except "/", `**` any run including
//     "/", `?` one character except "/", `\` makes the next character literal;
//   - path subject: `*` any run within one segment, `**` as a whole segment any
//     number of segments (zero included), `?` one character within a segment, `\`
//     as above.
//
// A Pattern is immutable and safe for concurrent use.
type Pattern struct {
	subject PatternSubject
	segs    []patSeg
	// lead is the literal text every match starts with: the leading literal
	// characters of a name pattern, or of the first segment of a path pattern
	// that does not start with a globstar. A match refuses an input without it
	// before any state is built. whole says that lead is the entire first
	// segment, so the input must continue with "/" or end there.
	lead  string
	whole bool
}

// newPattern builds a Pattern and derives its literal lead.
func newPattern(subject PatternSubject, segs []patSeg) *Pattern {
	p := &Pattern{subject: subject, segs: segs}
	if len(segs) == 0 || segs[0].globstar {
		return p
	}
	var b strings.Builder
	toks := segs[0].toks
	n := 0
	for n < len(toks) && toks[n].kind == tokLit {
		b.WriteRune(toks[n].r)
		n++
	}
	p.lead = b.String()
	p.whole = subject == SubjectPath && n == len(toks)
	return p
}

// leadRefuses reports whether s cannot match because it lacks the literal lead.
func (p *Pattern) leadRefuses(s string) bool {
	if !strings.HasPrefix(s, p.lead) {
		return true
	}
	return p.whole && len(s) > len(p.lead) && s[len(p.lead)] != '/'
}

type tokKind uint8

const (
	tokLit  tokKind = iota // one literal rune
	tokOne                 // ?
	tokStar                // * : a run without "/"
	tokAny                 // ** inside a name: a run with "/"
)

type tok struct {
	kind tokKind
	r    rune
}

type patSeg struct {
	globstar bool // a whole-segment ** of a path pattern
	toks     []tok
}

// CompilePattern parses one pattern. The error names what is wrong and is safe
// to show (it becomes the detail of an invalid_pattern violation).
func CompilePattern(subject PatternSubject, p string) (*Pattern, error) {
	switch {
	case p == "":
		return nil, errors.New("a pattern must not be empty")
	case len(p) > MaxPatternLength:
		return nil, errors.New("a pattern is at most 1024 characters")
	case !utf8.ValidString(p):
		return nil, errors.New("a pattern must be valid UTF-8")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return nil, errors.New("a pattern must not contain a control character")
		}
	}
	switch subject {
	case SubjectName:
		toks, err := parseTokens(p, true)
		if err != nil {
			return nil, err
		}
		return newPattern(subject, []patSeg{{toks: toks}}), nil
	case SubjectPath:
		return compilePath(p)
	}
	return nil, errors.New("unknown pattern subject")
}

func compilePath(p string) (*Pattern, error) {
	if strings.HasPrefix(p, "/") {
		return nil, errors.New("a path pattern is relative to the repository and does not start with \"/\"")
	}
	var segs []patSeg
	for _, raw := range splitUnescaped(p, '/') {
		if raw == "" {
			return nil, errors.New("a path pattern must not have an empty segment")
		}
		if raw == "**" {
			segs = append(segs, patSeg{globstar: true})
			continue
		}
		toks, err := parseTokens(raw, false)
		if err != nil {
			return nil, err
		}
		segs = append(segs, patSeg{toks: toks})
	}
	return newPattern(SubjectPath, segs), nil
}

// splitUnescaped splits s at sep, ignoring a separator that a backslash escapes.
func splitUnescaped(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune('\\')
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == sep:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if esc {
		cur.WriteRune('\\')
	}
	return append(out, cur.String())
}

// parseTokens turns a segment (or a whole name) into tokens. In a name, "**" is
// the run that crosses "/"; in a path segment "**" next to other characters is a
// syntax error, because "**" is a whole-segment form there.
func parseTokens(s string, name bool) ([]tok, error) {
	rs := []rune(s)
	var out []tok
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("a pattern must not end in a lone backslash")
			}
			i++
			out = append(out, tok{kind: tokLit, r: rs[i]})
		case '?':
			out = append(out, tok{kind: tokOne})
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				if !name {
					return nil, errors.New("\"**\" in a path pattern must be a whole segment")
				}
				for i+1 < len(rs) && rs[i+1] == '*' {
					i++
				}
				out = append(out, tok{kind: tokAny})
				continue
			}
			out = append(out, tok{kind: tokStar})
		default:
			out = append(out, tok{kind: tokLit, r: r})
		}
	}
	return out, nil
}

// Matching is the hot path of every decision with an exposure ceiling: a
// decision evaluates up to MaxExposureRules patterns, so a match allocates
// nothing for the inputs that occur (a path of up to stackRunes characters per
// segment and a pattern of up to stackSegs segments); longer ones fall back to
// the heap with the same result.
const (
	stackSegs  = 32
	stackRunes = 128
)

// Match reports whether the canonical path (or the normalized name) matches.
func (p *Pattern) Match(s string) bool {
	if p.leadRefuses(s) {
		return false
	}
	if p.subject == SubjectName {
		return globMatch(p.segs[0].toks, s)
	}
	if s == "" {
		return false
	}
	return matchSegments(p.segs, s)
}

// segStates returns two cleared state vectors of len(pat)+1 entries.
func segStates(buf *[2 * (stackSegs + 1)]bool, pat []patSeg) (reach, next []bool) {
	n := len(pat) + 1
	if n <= stackSegs+1 {
		return buf[:n:n], buf[n : 2*n : 2*n]
	}
	return make([]bool, n), make([]bool, n)
}

// advance consumes one path segment: next becomes the states reachable from
// reach through seg. It reports whether any state is left; none is left for good,
// so the caller stops reading segments.
func advance(pat []patSeg, reach, next []bool, seg string) bool {
	clear(next)
	alive := false
	for i, ok := range reach {
		if !ok || i == len(pat) {
			continue
		}
		if pat[i].globstar {
			next[i] = true
			alive = true
		} else if matchSegment(pat[i].toks, seg) {
			next[i+1] = true
			alive = true
		}
	}
	closure(pat, next)
	return alive
}

// matchSegments matches the "/"-separated segments of in; in is not empty.
func matchSegments(pat []patSeg, in string) bool {
	var buf [2 * (stackSegs + 1)]bool
	// reach[i] is true when pat[:i] consumed the segments seen so far.
	reach, next := segStates(&buf, pat)
	reach[0] = true
	closure(pat, reach)
	for rest, more := in, true; more; {
		var seg string
		seg, rest, more = strings.Cut(rest, "/")
		if !advance(pat, reach, next, seg) {
			return false
		}
		reach, next = next, reach
	}
	return reach[len(pat)]
}

// closure adds the states reachable without consuming a segment: a globstar
// matches zero segments.
func closure(pat []patSeg, reach []bool) {
	for i := range pat {
		if reach[i] && pat[i].globstar {
			reach[i+1] = true
		}
	}
}

// MayContain reports whether some path below the folder could match the
// pattern, which is how a folder is admitted by an include list (R-GENERIC-007).
// A name pattern has no folders and answers by Match.
func (p *Pattern) MayContain(folder string) bool {
	if p.subject == SubjectName {
		return p.Match(folder)
	}
	pat := p.segs
	var buf [2 * (stackSegs + 1)]bool
	reach, next := segStates(&buf, pat)
	reach[0] = true
	closure(pat, reach)
	if folder != "" {
		for rest, more := folder, true; more; {
			var seg string
			seg, rest, more = strings.Cut(rest, "/")
			if !advance(pat, reach, next, seg) {
				return false
			}
			reach, next = next, reach
		}
	}
	for i := range pat {
		if reach[i] {
			return true
		}
	}
	return false
}

// matchSegment matches the tokens of one path segment against one segment of
// the input. A segment holds no "/" (the input is split at it, the pattern at
// its unescaped ones), so "*" is a plain run here and the match is the greedy one
// with a single retreat point: no state vector, no allocation, and a literal
// first token that differs ends it at once.
func matchSegment(toks []tok, s string) bool {
	ti, si := 0, 0
	star, mark := -1, 0 // the last run token and where in s its run ends now
	for si < len(s) {
		if ti < len(toks) {
			t := toks[ti]
			switch t.kind {
			case tokStar, tokAny:
				star, mark = ti, si
				ti++
				continue
			case tokOne:
				_, w := utf8.DecodeRuneInString(s[si:])
				si += w
				ti++
				continue
			case tokLit:
				if r, w := utf8.DecodeRuneInString(s[si:]); r == t.r {
					si += w
					ti++
					continue
				}
			}
		}
		if star < 0 {
			return false
		}
		_, w := utf8.DecodeRuneInString(s[mark:])
		mark += w
		si, ti = mark, star+1
	}
	for ti < len(toks) && (toks[ti].kind == tokStar || toks[ti].kind == tokAny) {
		ti++
	}
	return ti == len(toks)
}

// globMatch matches the tokens of a name pattern against the whole of s, one
// rune per character; "*" stops at "/", which a retreat point cannot honour, so
// it keeps the state vector.
func globMatch(toks []tok, s string) bool {
	var rbuf [stackRunes]rune
	rs := rbuf[:0]
	for _, r := range s {
		rs = append(rs, r)
	}
	n := len(rs)
	var bbuf [2 * (stackRunes + 1)]bool
	var cur, next []bool
	if n <= stackRunes {
		cur, next = bbuf[:n+1:n+1], bbuf[n+1:2*(n+1):2*(n+1)]
	} else {
		cur, next = make([]bool, n+1), make([]bool, n+1)
	}
	cur[0] = true
	for _, t := range toks {
		clear(next)
		switch t.kind {
		case tokLit:
			for j := 0; j < n; j++ {
				if cur[j] && rs[j] == t.r {
					next[j+1] = true
				}
			}
		case tokOne:
			for j := 0; j < n; j++ {
				if cur[j] && rs[j] != '/' {
					next[j+1] = true
				}
			}
		case tokStar:
			run := false
			for k := 0; k <= n; k++ {
				if cur[k] {
					run = true
				}
				next[k] = run
				if k < n && rs[k] == '/' {
					run = false
				}
			}
		case tokAny:
			run := false
			for k := 0; k <= n; k++ {
				if cur[k] {
					run = true
				}
				next[k] = run
			}
		}
		cur, next = next, cur
	}
	return cur[n]
}

// Admission is the compiled include and exclude lists of one repository
// (R-REPO-005). The zero value admits everything.
type Admission struct {
	include, exclude []*Pattern
}

// NewAdmission compiles both lists. The first error is returned; validation
// reports every one through [validatePatterns].
func NewAdmission(subject PatternSubject, include, exclude []string) (*Admission, error) {
	a := &Admission{}
	for _, src := range []struct {
		in  []string
		dst *[]*Pattern
	}{{include, &a.include}, {exclude, &a.exclude}} {
		for _, s := range src.in {
			p, err := CompilePattern(subject, s)
			if err != nil {
				return nil, err
			}
			*src.dst = append(*src.dst, p)
		}
	}
	return a, nil
}

// restricts reports whether either list holds a pattern; a nil Admission has none.
func (a *Admission) restricts() bool { return a != nil && len(a.include)+len(a.exclude) > 0 }

// Admits reports whether a canonical path (or a normalized name) is admitted:
// it matches at least one include pattern, if there are any, and no exclude
// pattern. A nil Admission admits everything.
func (a *Admission) Admits(s string) bool {
	if a == nil {
		return true
	}
	for _, p := range a.exclude {
		if p.Match(s) {
			return false
		}
	}
	if len(a.include) == 0 {
		return true
	}
	for _, p := range a.include {
		if p.Match(s) {
			return true
		}
	}
	return false
}

// AdmitsFolder is [Admission.Admits] for a folder: it is admitted when it is not
// excluded and some path below it could be included.
func (a *Admission) AdmitsFolder(folder string) bool {
	if a == nil {
		return true
	}
	for _, p := range a.exclude {
		if p.Match(folder) {
			return false
		}
	}
	if len(a.include) == 0 {
		return true
	}
	for _, p := range a.include {
		if p.MayContain(folder) {
			return true
		}
	}
	return false
}
