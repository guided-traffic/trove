package repository

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PatternSubject is what a repository's include and exclude patterns match
// (R-REPO-005): the normalized package name, or the canonical path.
type PatternSubject string

// The subjects. They are the values of the format descriptor's PatternSubject.
const (
	SubjectName PatternSubject = "name"
	SubjectPath PatternSubject = "path"
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
		return &Pattern{subject: subject, segs: []patSeg{{toks: toks}}}, nil
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
	return &Pattern{subject: SubjectPath, segs: segs}, nil
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

// Match reports whether the canonical path (or the normalized name) matches.
func (p *Pattern) Match(s string) bool {
	if p.subject == SubjectName {
		return globMatch(p.segs[0].toks, []rune(s))
	}
	if s == "" {
		return false
	}
	return matchSegments(p.segs, strings.Split(s, "/"))
}

func matchSegments(pat []patSeg, in []string) bool {
	// reach[i] is true when pat[:i] consumed the segments seen so far.
	reach := make([]bool, len(pat)+1)
	reach[0] = true
	closure(pat, reach)
	for _, seg := range in {
		next := make([]bool, len(pat)+1)
		for i, ok := range reach {
			if !ok || i == len(pat) {
				continue
			}
			if pat[i].globstar {
				next[i] = true
			} else if globMatch(pat[i].toks, []rune(seg)) {
				next[i+1] = true
			}
		}
		closure(pat, next)
		reach = next
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
	reach := make([]bool, len(pat)+1)
	reach[0] = true
	closure(pat, reach)
	if folder != "" {
		for _, seg := range strings.Split(folder, "/") {
			next := make([]bool, len(pat)+1)
			for i, ok := range reach {
				if !ok || i == len(pat) {
					continue
				}
				if pat[i].globstar {
					next[i] = true
				} else if globMatch(pat[i].toks, []rune(seg)) {
					next[i+1] = true
				}
			}
			closure(pat, next)
			reach = next
		}
	}
	for i := range pat {
		if reach[i] {
			return true
		}
	}
	return false
}

// globMatch matches tokens against the whole of s.
func globMatch(toks []tok, s []rune) bool {
	n := len(s)
	cur := make([]bool, n+1)
	cur[0] = true
	for _, t := range toks {
		next := make([]bool, n+1)
		switch t.kind {
		case tokLit:
			for j := 0; j < n; j++ {
				if cur[j] && s[j] == t.r {
					next[j+1] = true
				}
			}
		case tokOne:
			for j := 0; j < n; j++ {
				if cur[j] && s[j] != '/' {
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
				if k < n && s[k] == '/' {
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
		cur = next
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
