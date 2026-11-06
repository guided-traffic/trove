package authz

import (
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

// ValidateSelector reports why a repository selector text of an instance context (a token scope, a global
// policy) is invalid, nil if it is valid: a key, a key pattern, a class selector, each with the optional
// format filter (R-AUTHZ-004). A token scope names its repositories with it (R-AUTH-024).
func ValidateSelector(s string) error {
	if err := checkLength(s); err != nil {
		return err
	}
	_, err := parseSelector(s, instanceContext)
	return err
}

// dynamic reports whether the selector covers repositories created later.
func (s selector) dynamic() bool { return s.kind != selKey }

// text renders the selector in the API form of its context. keyOf resolves the key of a policy's key
// selector by repository ID (the project is s.project then).
func (s selector) text(keyOf func(int64) string) string {
	suffix := ""
	if s.format != "" {
		suffix = ":" + s.format
	}
	switch s.kind {
	case selKey:
		key := s.key
		if key == "" && keyOf != nil {
			key = keyOf(s.repoID)
		}
		if s.inProject || s.project == "" {
			return key
		}
		return s.project + "/" + key
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
