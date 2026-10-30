package authz

import (
	"fmt"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// The pattern syntax of R-AUTHZ-005 is the one of R-REPO-005, and one compiler
// implements it: [repository.CompilePattern] (Q-AUTHZ-25). Package-name patterns
// use the subject "name", path patterns "path"; this file adds only what a grant
// needs on top: the limits, the folding of a name with the rule of its format
// (the catalog's one normaliser, [catalog.Names]) and the include and exclude
// semantics of a resource set.

// Limits of R-AUTHZ-005. The length is counted in characters; the compiler
// counts bytes and is the stricter of the two.
const (
	maxPatternLen   = 1024
	maxPatternCount = 256
)

// fold normalizes a package name or a package-name pattern with the rule of its
// format (R-AUTHZ-005): names and patterns are folded by the same function, so
// "Acme.*" and "ACME.Core" meet in the form the format compares. A nil rule (a
// format without package names, a format the catalog does not know) folds
// nothing, and a string the rule does not take for a name stays as it is.
func fold(names catalog.Names, s string) string {
	if names == nil {
		return s
	}
	if n, ok := names.Normalize(s); ok {
		return n
	}
	return s
}

// namesIn returns the name rule of a format in the catalog; nil for a format
// without package names and for one the catalog does not know.
func namesIn(reg *catalog.Registry, format string) catalog.Names {
	e, _ := reg.Lookup(format)
	return e.Names
}

// compileName compiles a package-name glob (R-AUTHZ-005) folded with names.
func compileName(pattern string, names catalog.Names) (*repository.Pattern, error) {
	if err := checkLength(pattern); err != nil {
		return nil, err
	}
	return repository.CompilePattern(repository.SubjectName, fold(names, pattern))
}

// compilePath compiles a path pattern (R-AUTHZ-005).
func compilePath(pattern string) (*repository.Pattern, error) {
	if err := checkLength(pattern); err != nil {
		return nil, err
	}
	return repository.CompilePattern(repository.SubjectPath, pattern)
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
func ValidatePackagePattern(p string) error { _, err := compileName(p, nil); return err }

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
	include []*repository.Pattern
	exclude []*repository.Pattern
	names   catalog.Names
}

// compileSet compiles an include and an exclude list of one dimension; names is
// the name rule of the package dimension and is ignored for paths.
func compileSet(dim dimension, include, exclude []string, names catalog.Names) (globSet, error) {
	g := globSet{}
	if dim == dimPackage {
		g.names = names
	}
	compile := func(p string) (*repository.Pattern, error) {
		if dim == dimPackage {
			return compileName(p, g.names)
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
	v = fold(g.names, v)
	if len(g.include) == 0 {
		return !present || !g.excluded(v)
	}
	return present && g.included(v) && !g.excluded(v)
}

func (g globSet) included(v string) bool {
	for _, re := range g.include {
		if re.Match(v) {
			return true
		}
	}
	return false
}

func (g globSet) excluded(v string) bool {
	for _, re := range g.exclude {
		if re.Match(v) {
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
