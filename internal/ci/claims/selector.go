package claims

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The repository selectors of R-AUTHZ-004, as the token scope and the inline grants write them:
//
//	npm-internal          a key
//	npm-team-*            a key pattern (contains * or ?)
//	@any-local            a class; @any-remote, @any-virtual, @any
//	@any-local:npm        a class or pattern restricted to a format
//
// This is the syntax only, the same rules as the authorization engine's selector parser
// (internal/authz, ValidateSelector): whether a key names an existing repository and whether a
// format is known needs the repository index and stays with the caller.

var (
	selectorKey     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)
	selectorPattern = regexp.MustCompile(`^[a-z0-9._*?-]+$`)
)

// maxSelectorLength is the pattern limit of R-AUTHZ-005, in characters.
const maxSelectorLength = 1024

// selectorKind is what a valid selector is.
type selectorKind int

const (
	selKey selectorKind = iota
	selPattern
	selClass
)

// parsedSelector is a selector split into its base and its format filter.
type parsedSelector struct {
	kind   selectorKind
	base   string
	format string
}

// parseSelector checks the syntax of a selector (R-AUTHZ-004).
func parseSelector(s string) (parsedSelector, error) {
	if n := utf8.RuneCountInString(s); n < 1 || n > maxSelectorLength {
		return parsedSelector{}, errors.New("a selector has 1 to 1024 characters")
	}
	base, format := s, ""
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		base, format = s[:i], s[i+1:]
		if format == "" {
			return parsedSelector{}, errors.New("an empty format filter")
		}
	}
	switch {
	case strings.HasPrefix(base, "@"):
		switch base {
		case "@any", "@any-local", "@any-remote", "@any-virtual":
			return parsedSelector{kind: selClass, base: base, format: format}, nil
		}
		return parsedSelector{}, errors.New("a class selector is one of @any-local, @any-remote, @any-virtual, @any")
	case strings.ContainsAny(base, "*?"):
		if !selectorPattern.MatchString(base) {
			return parsedSelector{}, errors.New("not a repository key pattern")
		}
		return parsedSelector{kind: selPattern, base: base, format: format}, nil
	case format != "":
		return parsedSelector{}, errors.New("a format filter needs a pattern or a class selector")
	case !selectorKey.MatchString(base):
		return parsedSelector{}, errors.New("not a repository key")
	}
	return parsedSelector{kind: selKey, base: base}, nil
}

// covers reports whether the grant selector g certainly covers every repository the selector t
// selects, as far as the text tells without the repository index: the same selector, the class
// @any without a format filter, or a key pattern without a format filter that matches the key t.
// false means "not known here", not "disjoint".
func covers(g, t string) bool {
	if g == t || g == "@any" {
		return true
	}
	gs, err := parseSelector(g)
	if err != nil || gs.kind != selPattern || gs.format != "" {
		return false
	}
	ts, err := parseSelector(t)
	return err == nil && ts.kind == selKey && keyGlob(gs.base, ts.base)
}

// disjoint reports whether the two selectors certainly select no common repository: two different
// keys, or a key that a format-free key pattern does not match. Everything else may overlap.
func disjoint(a, b string) bool {
	if a == b {
		return false
	}
	as, errA := parseSelector(a)
	bs, errB := parseSelector(b)
	if errA != nil || errB != nil {
		return false
	}
	switch {
	case as.kind == selKey && bs.kind == selKey:
		return true
	case as.kind == selKey && bs.kind == selPattern && bs.format == "":
		return !keyGlob(bs.base, as.base)
	case bs.kind == selKey && as.kind == selPattern && as.format == "":
		return !keyGlob(as.base, bs.base)
	}
	return false
}

// keyGlob matches a repository key against a key pattern: `*` any run of characters, `?` one
// character (a key holds no "/", so the segment glob is exact).
func keyGlob(pattern, key string) bool {
	toks := make([]globTok, 0, len(pattern))
	for _, r := range pattern {
		switch r {
		case '*':
			toks = append(toks, globTok{kind: '*'})
		case '?':
			toks = append(toks, globTok{kind: '?'})
		default:
			toks = append(toks, globTok{kind: 'l', r: r})
		}
	}
	return segmentMatch(toks, key, false)
}
