package qname

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxSelectorLength is the longest selector text, in characters (R-AUTHZ-005).
const MaxSelectorLength = 1024

// Errors of ParseSelector; callers test them with errors.Is.
var (
	// ErrBareKeyInInstanceContext: a selector of an instance context (a global policy, a token
	// scope, a system CI grant, an exposure rule, the configuration) names no project. It is
	// refused, never read as a selector that silently matches nothing.
	ErrBareKeyInInstanceContext = errors.New("qname: a selector outside a project names the project: <project>/<key>")
	// ErrQualifiedInProject: a selector of a project's policy, CI mapping, virtual repository or
	// deployment target names repositories of its own project by bare key.
	ErrQualifiedInProject = errors.New("qname: a selector inside a project names a bare key, never another project")
	// ErrInvalidSelector: the text is no selector of the grammar.
	ErrInvalidSelector = errors.New("qname: invalid repository selector")
)

// SelectorKind is the shape of a selector.
type SelectorKind uint8

// The selector shapes (R-AUTHZ-004).
const (
	// SelectorKey names one repository: Project and Key.
	SelectorKey SelectorKind = iota + 1
	// SelectorPattern names repositories by a glob of the project key and a glob of the key.
	SelectorPattern
	// SelectorClass names repositories by class.
	SelectorClass
)

// Selector is a parsed repository selector. The grammar by context:
//
//	instance context (global policies, token scopes, system CI tier, exposure rules, host_roots, configuration):
//	  web/npm-internal        a key: project and key
//	  web/npm-*               a pattern: <projectGlob>/<keyGlob>[:<format>]
//	  w*/npm-*:npm
//	  @any, @any-local, @any-remote, @any-virtual [:<format>]   a class, across all projects
//	project context (project policies, project CI tier, virtual repositories, deployment targets):
//	  npm-internal            a bare key
//	  npm-*[:<format>]        a bare key glob
//	  @any…[:<format>]        a class within the project
//
// `*` matches any run of characters and `?` one character; neither crosses a "/", so a project glob
// never reaches into the key. A bare key in an instance context and a qualified one in a project
// context are errors.
type Selector struct {
	Kind SelectorKind
	// Project is the exact project of a key selector, and the project of every selector read in
	// a project context. Empty for a pattern or class selector of an instance context.
	Project string
	// Key is the key of a key selector.
	Key string
	// ProjectGlob and KeyGlob are the globs of a pattern selector; ProjectGlob is empty in a
	// project context (the project is Project).
	ProjectGlob string
	KeyGlob     string
	// Class is "local", "remote", "virtual" or "any" for a class selector.
	Class string
	// Format is the optional ":<format>" filter of a pattern or class selector.
	Format string
}

// Dynamic reports whether the selector covers repositories created later.
func (s Selector) Dynamic() bool { return s.Kind != SelectorKey }

// AllProjects reports whether the selector reaches beyond one project: a pattern or class
// selector of the instance context.
func (s Selector) AllProjects() bool { return s.Kind != SelectorKey && s.Project == "" }

// Text renders the selector in the form of context c, as ParseSelector reads it.
func (s Selector) Text(c Context) string {
	suffix := ""
	if s.Format != "" {
		suffix = ":" + s.Format
	}
	var base string
	switch s.Kind {
	case SelectorKey:
		base = s.Key
		if c.Instance() {
			base = s.Project + "/" + s.Key
		}
	case SelectorPattern:
		base = s.KeyGlob
		if c.Instance() {
			base = s.ProjectGlob + "/" + s.KeyGlob
		}
	case SelectorClass:
		base = "@any"
		if s.Class != "any" {
			base = "@any-" + s.Class
		}
	}
	return base + suffix
}

// ParseSelector reads the syntax of a selector in the context in (the grammar is at [Selector]). It
// does not ask whether a project, a key or a format exists, and the authorization engine and the CI
// claims compile the globs.
func ParseSelector(s string, in Context) (Selector, error) {
	if n := utf8.RuneCountInString(s); n < 1 || n > MaxSelectorLength {
		return Selector{}, fmt.Errorf("%w: a selector has 1 to %d characters", ErrInvalidSelector, MaxSelectorLength)
	}
	base, format := s, ""
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		base, format = s[:i], s[i+1:]
		if format == "" {
			return Selector{}, fmt.Errorf("%w: %q has an empty format filter", ErrInvalidSelector, s)
		}
		if !ValidFormat(format) {
			return Selector{}, fmt.Errorf("%w: %q: %q is no format", ErrInvalidSelector, s, format)
		}
	}
	if strings.HasPrefix(base, "@") {
		class, ok := classOf(base)
		if !ok {
			return Selector{}, fmt.Errorf("%w: %q: a class is one of @any-local, @any-remote, @any-virtual, @any", ErrInvalidSelector, s)
		}
		return Selector{Kind: SelectorClass, Project: in.Project, Class: class, Format: format}, nil
	}
	projectPart, keyPart, qualified := strings.Cut(base, "/")
	if in.Instance() {
		if !qualified {
			return Selector{}, fmt.Errorf("%w: %q", ErrBareKeyInInstanceContext, s)
		}
	} else {
		if qualified {
			return Selector{}, fmt.Errorf("%w: %q", ErrQualifiedInProject, s)
		}
		keyPart = base
	}
	if strings.Contains(keyPart, "/") {
		return Selector{}, fmt.Errorf("%w: %q has more than one slash", ErrInvalidSelector, s)
	}
	isGlob := strings.ContainsAny(base, "*?")
	if !isGlob {
		if format != "" {
			return Selector{}, fmt.Errorf("%w: %q: a format filter needs a pattern or a class selector", ErrInvalidSelector, s)
		}
		if in.Instance() && !ValidProjectKey(projectPart) {
			return Selector{}, fmt.Errorf("%w: %q: %q is no project key", ErrInvalidSelector, s, projectPart)
		}
		if !ValidKey(keyPart) {
			return Selector{}, fmt.Errorf("%w: %q: %q is no repository key", ErrInvalidSelector, s, keyPart)
		}
		if in.Instance() {
			return Selector{Kind: SelectorKey, Project: projectPart, Key: keyPart}, nil
		}
		return Selector{Kind: SelectorKey, Project: in.Project, Key: keyPart}, nil
	}
	if in.Instance() && !validGlob(projectPart, false) {
		return Selector{}, fmt.Errorf("%w: %q: %q is no project key pattern", ErrInvalidSelector, s, projectPart)
	}
	if !validGlob(keyPart, true) {
		return Selector{}, fmt.Errorf("%w: %q: %q is no repository key pattern", ErrInvalidSelector, s, keyPart)
	}
	if in.Instance() {
		return Selector{Kind: SelectorPattern, ProjectGlob: projectPart, KeyGlob: keyPart, Format: format}, nil
	}
	return Selector{Kind: SelectorPattern, Project: in.Project, KeyGlob: keyPart, Format: format}, nil
}

func classOf(base string) (string, bool) {
	switch base {
	case "@any":
		return "any", true
	case "@any-local":
		return "local", true
	case "@any-remote":
		return "remote", true
	case "@any-virtual":
		return "virtual", true
	}
	return "", false
}

// validGlob reports whether g is a non-empty run of the characters a key (dots too when keys is
// set) or a project key can hold, and the glob characters.
func validGlob(g string, keys bool) bool {
	if g == "" {
		return false
	}
	for i := 0; i < len(g); i++ {
		c := g[i]
		if isLower(c) || isDigit(c) || c == '-' || c == '*' || c == '?' || keys && (c == '.' || c == '_') {
			continue
		}
		return false
	}
	return true
}

// Glob reports whether name matches the glob: `*` any run of characters (`**` is the same), `?`
// one character, every other character itself. Names hold no "/", so the glob of one segment
// never crosses one. The match is linear in the product of the lengths at worst, never exponential.
func Glob(glob, name string) bool {
	g, s := 0, 0
	star, mark := -1, 0
	for s < len(name) {
		switch {
		case g < len(glob) && glob[g] == '*':
			star, mark = g, s
			g++
		case g < len(glob) && (glob[g] == '?' || glob[g] == name[s]):
			g++
			s++
		case star >= 0:
			g = star + 1
			mark++
			s = mark
		default:
			return false
		}
	}
	for g < len(glob) && glob[g] == '*' {
		g++
	}
	return g == len(glob)
}
