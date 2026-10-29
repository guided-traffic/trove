package authz

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/repository"
)

// Selector kinds as stored (authz_policy_selector.kind).
const (
	selKey     = "key"
	selPattern = "pattern"
	selClass   = "class"
)

// classAny is the class selector value that covers every class.
const classAny = "any"

// repoView is what a selector needs to know about a repository.
type repoView struct {
	ID     int64
	Key    string
	Class  repository.Class
	Format string
}

// selector is one repository selector of a resource set (R-AUTHZ-004). Policies
// store key selectors by repository ID; token scopes and CI grants name keys.
type selector struct {
	kind string
	// repoID is set for a key selector of a policy; key for one of a scope.
	repoID int64
	key    string
	// pattern is the original text of a key pattern; glob its compiled form.
	pattern string
	glob    *regexp.Regexp
	// class is "local", "remote", "virtual" or "any".
	class string
	// format is the optional ':<format>' filter of a pattern or class selector.
	format string
}

var keyRE = regexp.MustCompile(repository.KeyPattern)

var keyGlobRE = regexp.MustCompile(`^[a-z0-9._*?-]+$`)

// classSelectors are the class selector spellings of R-AUTHZ-004.
func classOf(base string) (string, bool) {
	switch base {
	case "@any-local":
		return "local", true
	case "@any-remote":
		return "remote", true
	case "@any-virtual":
		return "virtual", true
	case "@any":
		return classAny, true
	}
	return "", false
}

// parseSelector parses the selector text of the API and of token scopes:
//
//	npm-internal          a key
//	npm-team-*            a key pattern (contains * or ?)
//	@any-local            a class; @any-remote, @any-virtual, @any
//	@any-local:npm        a class or pattern restricted to a format
func parseSelector(s string) (selector, error) {
	base, format := s, ""
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		base, format = s[:i], s[i+1:]
		if format == "" {
			return selector{}, fmt.Errorf("selector %q has an empty format filter", s)
		}
	}
	if strings.HasPrefix(base, "@") {
		class, ok := classOf(base)
		if !ok {
			return selector{}, fmt.Errorf("selector %q: %q is not one of @any-local, @any-remote, @any-virtual, @any", s, base)
		}
		return selector{kind: selClass, class: class, format: format}, nil
	}
	if strings.ContainsAny(base, "*?") {
		if !keyGlobRE.MatchString(base) {
			return selector{}, fmt.Errorf("selector %q is not a repository key pattern", s)
		}
		re, err := compileName(strings.ReplaceAll(base, "**", "*"), false)
		if err != nil {
			return selector{}, fmt.Errorf("selector %q: %w", s, err)
		}
		// A key never holds a slash, so the name rules reduce to the rules of
		// repository keys: * any run of characters, ? one character.
		return selector{kind: selPattern, pattern: base, glob: re, format: format}, nil
	}
	if format != "" {
		return selector{}, fmt.Errorf("selector %q: a format filter needs a pattern or a class selector", s)
	}
	if !keyRE.MatchString(base) {
		return selector{}, fmt.Errorf("selector %q is not a repository key", s)
	}
	return selector{kind: selKey, key: base}, nil
}

// dynamic reports whether the selector covers repositories created later.
func (s selector) dynamic() bool { return s.kind != selKey }

// text renders the selector in the API form.
func (s selector) text(keyOf func(int64) string) string {
	suffix := ""
	if s.format != "" {
		suffix = ":" + s.format
	}
	switch s.kind {
	case selKey:
		if s.key != "" {
			return s.key
		}
		return keyOf(s.repoID)
	case selPattern:
		return s.pattern + suffix
	}
	if s.class == classAny {
		return "@any" + suffix
	}
	return "@any-" + s.class + suffix
}

// matches reports whether the selector covers the repository.
func (s selector) matches(r repoView) bool {
	if s.format != "" && s.format != r.Format {
		return false
	}
	switch s.kind {
	case selKey:
		if s.repoID != 0 {
			return s.repoID == r.ID
		}
		return s.key == r.Key
	case selPattern:
		return s.glob.MatchString(r.Key)
	case selClass:
		return s.class == classAny || s.class == string(r.Class)
	}
	return false
}
