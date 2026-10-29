package authz

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/repository"
)

// The pattern syntax of R-AUTHZ-005 is the one of R-REPO-005, and one compiler
// implements it: [repository.CompilePattern] (Q-AUTHZ-25). Package-name patterns
// use the subject "name", path patterns "path"; this file adds only what a grant
// needs on top: the limits, the NuGet case folding and the include and exclude
// semantics of a resource set.

// Limits of R-AUTHZ-005. The length is counted in characters; the compiler
// counts bytes and is the stricter of the two.
const (
	maxPatternLen   = 1024
	maxPatternCount = 256
)

// FormatNuGet is the format whose package names compare case-insensitively
// (R-AUTHZ-005): patterns and names are lower-cased by the same rule.
const FormatNuGet = "nuget"

// foldFor reports whether package names of the format compare case-insensitively.
func foldFor(format string) bool { return format == FormatNuGet }

// compileName compiles a package-name glob (R-AUTHZ-005).
func compileName(pattern string, fold bool) (*repository.Pattern, error) {
	if err := checkLength(pattern); err != nil {
		return nil, err
	}
	if fold {
		pattern = strings.ToLower(pattern)
	}
	return repository.CompilePattern(repository.SubjectName, pattern)
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
	include []*repository.Pattern
	exclude []*repository.Pattern
	fold    bool
}

// compileSet compiles an include and an exclude list of one dimension.
func compileSet(dim dimension, include, exclude []string, fold bool) (globSet, error) {
	g := globSet{fold: fold && dim == dimPackage}
	compile := func(p string) (*repository.Pattern, error) {
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
