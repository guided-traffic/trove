package authz

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits of R-AUTHZ-005.
const (
	maxPatternLen   = 1024
	maxPatternCount = 256
)

// FormatNuGet is the format whose package names compare case-insensitively
// (R-AUTHZ-005): patterns and names are lower-cased by the same rule.
const FormatNuGet = "nuget"

// foldFor reports whether package names of the format compare case-insensitively.
func foldFor(format string) bool { return format == FormatNuGet }

// compileName compiles a package-name glob (R-AUTHZ-005): `*` any run of
// characters except `/`, `**` any run including `/`, `?` one character except
// `/`, `\` makes the next character literal whatever it is.
func compileName(pattern string, fold bool) (*regexp.Regexp, error) {
	if err := checkLength(pattern); err != nil {
		return nil, err
	}
	if fold {
		pattern = strings.ToLower(pattern)
	}
	var b strings.Builder
	b.WriteString(`^`)
	rs := []rune(pattern)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("a backslash at the end of the pattern escapes nothing")
			}
			i++
			b.WriteString(regexp.QuoteMeta(string(rs[i])))
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				i++
				b.WriteString(`.*`)
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(rs[i])))
		}
	}
	b.WriteString(`$`)
	return regexp.Compile("(?s)" + b.String())
}

// pathToken is one element of a path pattern segment.
type pathSegment struct {
	// doubleStar marks the segment `**`.
	doubleStar bool
	expr       string
}

// compilePath compiles a path pattern (R-AUTHZ-005): `*` any run within one
// segment, `**` as a whole segment any number of segments (also none), `?` one
// character within a segment, `\` as for names.
func compilePath(pattern string) (*regexp.Regexp, error) {
	if err := checkLength(pattern); err != nil {
		return nil, err
	}
	segs, err := splitPath(pattern)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(`^`)
	needSlash := false
	for i, s := range segs {
		switch {
		case s.doubleStar && i == 0 && len(segs) == 1:
			b.WriteString(`.*`)
		case s.doubleStar && i == 0:
			b.WriteString(`(?:[^/]+/)*`)
		case s.doubleStar:
			b.WriteString(`(?:/[^/]+)*`)
		default:
			if needSlash {
				b.WriteString(`/`)
			}
			b.WriteString(s.expr)
			needSlash = true
		}
	}
	b.WriteString(`$`)
	return regexp.Compile("(?s)" + b.String())
}

// splitPath splits a path pattern at its unescaped slashes and translates every
// segment.
func splitPath(pattern string) ([]pathSegment, error) {
	rs := []rune(pattern)
	var segs []pathSegment
	var raw []rune
	flush := func() error {
		if len(raw) == 0 {
			return errors.New("a path pattern has no empty segments")
		}
		s, err := translateSegment(raw)
		if err != nil {
			return err
		}
		segs = append(segs, s)
		raw = raw[:0]
		return nil
	}
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			if i+1 >= len(rs) {
				return nil, errors.New("a backslash at the end of the pattern escapes nothing")
			}
			raw = append(raw, rs[i], rs[i+1])
			i++
		case '/':
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			raw = append(raw, rs[i])
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return segs, nil
}

func translateSegment(raw []rune) (pathSegment, error) {
	if string(raw) == "**" {
		return pathSegment{doubleStar: true}, nil
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\\':
			i++
			b.WriteString(regexp.QuoteMeta(string(raw[i])))
		case '*':
			if i+1 < len(raw) && raw[i+1] == '*' {
				return pathSegment{}, errors.New("`**` is a whole segment of a path pattern")
			}
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(raw[i])))
		}
	}
	return pathSegment{expr: b.String()}, nil
}

func checkLength(p string) error {
	n := utf8.RuneCountInString(p)
	if n < 1 || n > maxPatternLen {
		return fmt.Errorf("a pattern has 1 to %d characters", maxPatternLen)
	}
	return nil
}

// ValidatePackagePattern reports why a package-name pattern is invalid, nil if
// it is valid (R-AUTHZ-005).
func ValidatePackagePattern(p string) error { _, err := compileName(p, false); return err }

// ValidatePathPattern reports why a path pattern is invalid, nil if it is valid.
func ValidatePathPattern(p string) error { _, err := compilePath(p); return err }

// ValidatePatternCount reports a list longer than the limit.
func ValidatePatternCount(list []string) error {
	if len(list) > maxPatternCount {
		return fmt.Errorf("a pattern list holds at most %d patterns", maxPatternCount)
	}
	return nil
}

// dimension is a pattern dimension of a resource set.
type dimension int

const (
	dimPackage dimension = iota
	dimPath
)

// globSet is one dimension of a resource set: include and exclude lists
// (R-AUTHZ-005).
type globSet struct {
	include []*regexp.Regexp
	exclude []*regexp.Regexp
	fold    bool
}

// compileSet compiles an include and an exclude list of one dimension.
func compileSet(dim dimension, include, exclude []string, fold bool) (globSet, error) {
	g := globSet{fold: fold && dim == dimPackage}
	compile := func(p string) (*regexp.Regexp, error) {
		if dim == dimPackage {
			return compileName(p, g.fold)
		}
		return compilePath(p)
	}
	for _, p := range include {
		re, err := compile(p)
		if err != nil {
			return globSet{}, fmt.Errorf("pattern %q: %w", p, err)
		}
		g.include = append(g.include, re)
	}
	for _, p := range exclude {
		re, err := compile(p)
		if err != nil {
			return globSet{}, fmt.Errorf("pattern %q: %w", p, err)
		}
		g.exclude = append(g.exclude, re)
	}
	return g, nil
}

// match applies the rules of R-AUTHZ-005: an empty include list leaves the
// dimension unrestricted (a coordinate without a value passes it, unless the
// value matches an exclude); a non-empty one requires a value that matches an
// include and no exclude.
func (g globSet) match(v string, present bool) bool {
	if g.fold {
		v = strings.ToLower(v)
	}
	if len(g.include) == 0 {
		return !present || !g.excluded(v)
	}
	return present && g.included(v) && !g.excluded(v)
}

func (g globSet) included(v string) bool {
	for _, re := range g.include {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}

func (g globSet) excluded(v string) bool {
	for _, re := range g.exclude {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}

// resourceSet is the package and path part of a resource set.
type resourceSet struct {
	packages globSet
	paths    globSet
}

// matches reports whether the coordinate is inside the set. A repository-level
// coordinate (search, the NuGet service index, a listing) matches whatever the
// patterns say (R-AUTHZ-005); results that list items are filtered per item.
func (r resourceSet) matches(repoLevel bool, pkg, path string) bool {
	if repoLevel {
		return true
	}
	return r.packages.match(pkg, pkg != "") && r.paths.match(path, path != "")
}
