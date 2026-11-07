package authz

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
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
	Name   qname.Repo
	Class  repository.Class
	Format string
}

// selector is one repository selector of a resource set (R-AUTHZ-004). Policies
// store key selectors by repository ID; token scopes and CI grants name keys. A selector is read in a
// context (qname.Context): an instance context (global policies, token scopes) names a repository by
// <project>/<key> and a pattern by <projectGlob>/<keyGlob>; a project context (project policies) by the
// bare key within its project (Q-AUTHZ-206, D4).
type selector struct {
	kind string
	// repoID is set for a key selector of a policy; key (and project) for one read from text.
	repoID int64
	key    string
	// project is the exact project the selector is restricted to: the project of a qualified key and
	// the project of every selector read in a project context. Empty for the pattern and class
	// selectors of an instance context.
	project string
	// inProject is set for a selector read in a project context: its text is the bare key.
	inProject bool
	// pattern is the original text of a key pattern without the format filter (<projectGlob>/<keyGlob> in
	// an instance context, the bare key glob in a project context); projectGlob and keyGlob are its parts.
	pattern     string
	projectGlob string
	keyGlob     string
	// class is "local", "remote", "virtual" or "any".
	class string
	// format is the optional ':<format>' filter of a pattern or class selector.
	format string
}

// instanceContext and projectContext are the contexts a selector is read in.
var instanceContext = qname.Context{}

func projectContext(project string) qname.Context { return qname.Context{Project: project} }

// parseSelector parses the selector text of the API and of token scopes in the context in; the
// grammar is at [qname.Selector]:
//
//	web/npm-internal      a key (instance context); npm-internal in a project context
//	web/npm-*             a pattern (a bare key glob in a project context)
//	@any-local            a class; @any-remote, @any-virtual, @any
//	@any-local:npm        a class or pattern restricted to a format
func parseSelector(s string, in qname.Context) (selector, error) {
	qs, err := qname.ParseSelector(s, in)
	if err != nil {
		return selector{}, err
	}
	sel := selector{format: qs.Format, project: qs.Project, inProject: !in.Instance()}
	switch qs.Kind {
	case qname.SelectorKey:
		sel.kind, sel.key = selKey, qs.Key
	case qname.SelectorPattern:
		sel.kind, sel.projectGlob, sel.keyGlob = selPattern, qs.ProjectGlob, qs.KeyGlob
		sel.pattern = qs.KeyGlob
		if sel.projectGlob != "" {
			sel.pattern = qs.ProjectGlob + "/" + qs.KeyGlob
		}
	default:
		sel.kind, sel.class = selClass, qs.Class
	}
	return sel, nil
}

// ValidateSelector reports why a repository selector text of an instance context (a token scope, a
// global policy, the system tier of a CI mapping, an exposure rule) is invalid, nil if it is valid:
// <project>/<key>, <projectGlob>/<keyGlob>, a class, the last two with the optional format filter
// (R-AUTHZ-004). It is [ValidateSelectorIn] with no project.
func ValidateSelector(s string) error { return ValidateSelectorIn(s, "") }

// ValidateSelectorIn reports why a repository selector text is invalid in the context of the project
// with the key project, "" for an instance context; nil if it is valid. A project context (a project
// policy, the project tier of a CI mapping, a virtual repository's members and deployment target) takes
// a bare key, a bare key pattern or a class, each with the optional format filter.
//
// The error says which rule the text broke, so that each surface answers with its own code
// (R-AUTHZ-004, Q-AUTHZ-216): errors.Is(err, qname.ErrBareKeyInInstanceContext) for a bare key or key
// pattern in an instance context, which a token scope answers 400 invalid_scope (R-AUTH-024) and the CI
// exchange scope_malformed (R-CI-007); errors.Is(err, qname.ErrQualifiedInProject) for a selector with a
// "/" in a project context; qname.ErrInvalidSelector for every other syntax error. A policy, a CI mapping
// and an exposure rule answer all three with 422 invalid_pattern ([SelectorProblemCode]). Whether a key
// names an existing repository and a format filter a known format is the caller's question.
func ValidateSelectorIn(s, project string) error {
	if err := checkLength(s); err != nil {
		return fmt.Errorf("%w: %w", qname.ErrInvalidSelector, err)
	}
	in := instanceContext
	if project != "" {
		in = projectContext(project)
	}
	_, err := parseSelector(s, in)
	return err
}

// SelectorProblemCode is the field code of a selector that [ValidateSelectorIn] refused, where selectors
// are validated as patterns: a permission policy, a CI mapping, an exposure rule (R-AUTHZ-004).
const SelectorProblemCode = errs.CodeInvalidPattern

// dynamic reports whether the selector covers repositories created later.
func (s selector) dynamic() bool { return s.kind != selKey }

// text renders the selector in the API form of its context: a key as <project>/<key> in an instance
// context and as the bare key in a project context. nameOf resolves the name of a policy's key selector
// by repository ID; nil, or a zero answer, keeps the name the selector holds (project and key).
func (s selector) text(nameOf func(int64) qname.Repo) string {
	switch s.kind {
	case selKey:
		name := s.keyName(nameOf)
		if s.inProject || name.Project == "" {
			return name.Key
		}
		return name.String()
	case selPattern:
		return s.pattern + s.suffix()
	}
	if s.class == classAny {
		return "@any" + s.suffix()
	}
	return "@any-" + s.class + s.suffix()
}

// instanceText renders the selector in the grammar of an instance context, where a selector of a
// project context gains its project: a key as <project>/<key>, a key pattern as <project>/<keyGlob>.
// ok is false for a class of a project context, which an instance context cannot spell (Q-AUTHZ-216
// "web/@any-local cannot be written"), and for a key whose repository nameOf does not know.
func (s selector) instanceText(nameOf func(int64) qname.Repo) (string, bool) {
	if !s.inProject {
		if s.kind == selKey && s.keyName(nameOf).Key == "" {
			return "", false
		}
		return s.text(nameOf), true
	}
	switch s.kind {
	case selKey:
		name := s.keyName(nameOf)
		if name.Key == "" {
			return "", false
		}
		return qname.Repo{Project: s.project, Key: name.Key}.String(), true
	case selPattern:
		return s.project + "/" + s.keyGlob + s.suffix(), true
	}
	return "", false
}

// keyName is the name of a key selector: by repository ID through nameOf when it knows it, else the
// project and key the selector holds.
func (s selector) keyName(nameOf func(int64) qname.Repo) qname.Repo {
	if s.repoID != 0 && nameOf != nil {
		if n := nameOf(s.repoID); !n.IsZero() {
			return n
		}
	}
	return qname.Repo{Project: s.project, Key: s.key}
}

// suffix is the format filter as text, ":<format>" or "".
func (s selector) suffix() string {
	if s.format == "" {
		return ""
	}
	return ":" + s.format
}

// coversProject reports whether the selector names the project: the project of a key and of a selector read in a
// project context, a pattern whose project glob matches it, and a class of the instance context, which covers every
// project. The key glob and the format filter do not narrow it: the question is which projects a selector reaches,
// not which repositories it matches today.
func (s selector) coversProject(project string) bool {
	if s.project != "" {
		return s.project == project
	}
	if s.kind == selPattern {
		return qname.Glob(s.projectGlob, project)
	}
	return s.kind == selClass
}

// matches reports whether the selector covers the repository.
func (s selector) matches(r repoView) bool {
	if s.format != "" && s.format != r.Format {
		return false
	}
	if s.project != "" && s.project != r.Name.Project {
		return false
	}
	switch s.kind {
	case selKey:
		if s.repoID != 0 {
			return s.repoID == r.ID
		}
		return s.key == r.Name.Key
	case selPattern:
		return (s.projectGlob == "" || qname.Glob(s.projectGlob, r.Name.Project)) && qname.Glob(s.keyGlob, r.Name.Key)
	case selClass:
		return s.class == classAny || s.class == string(r.Class)
	}
	return false
}
