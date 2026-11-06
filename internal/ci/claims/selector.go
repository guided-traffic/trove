package claims

import (
	"github.com/guided-traffic/trove/internal/qname"
)

// The repository selectors of R-AUTHZ-004 are one grammar (R-CI-007, Q-AUTHZ-216), parsed by
// [qname.ParseSelector] and read in a context:
//
//	instance context (the scope parameter of the exchange, the grants of a system mapping, a token scope):
//	  web/npm-internal      a key: project and key
//	  web/npm-team-*        a pattern of project glob and key glob
//	  @any-local            a class across all projects; @any-remote, @any-virtual, @any
//	  @any-local:npm        a class or pattern restricted to a format
//	project context (the grants of a project mapping):
//	  npm-internal          a bare key of the project
//	  npm-team-*            a bare key glob
//
// A bare key in an instance context and a qualified one in a project context are errors, never a
// selector that silently matches nothing. This is the syntax only: whether a key names an existing
// repository needs the repository index and stays with the caller.

// contextOf is the context the selectors of a mapping of the tier project are read in; "" is the
// system tier, whose selectors are instance selectors.
func contextOf(project string) qname.Context { return qname.Context{Project: project} }

// parseSelector checks the syntax of a selector (R-AUTHZ-004) in the context in.
func parseSelector(s string, in qname.Context) (qname.Selector, error) {
	return qname.ParseSelector(s, in)
}

// instanceForm writes a selector of a project mapping's grant, read in the project's context, as the
// instance selector the token carries: a key `k` becomes `<project>/k`, a pattern `g` becomes
// `<project>/g`, `@any` becomes `<project>/*` (a format filter stays), and a class that is not `@any`
// stays as it is, because the token's grants for a project tier are bounded to the project anyway
// (R-AUTHZ-026, Q-AUTHZ-179). A selector that does not parse is returned unchanged: the engine counts
// it as matching nothing.
func instanceForm(project, s string) string {
	sel, err := qname.ParseSelector(s, contextOf(project))
	if err != nil {
		return s
	}
	switch {
	case sel.Kind == qname.SelectorClass && sel.Class == "any":
		return qname.Selector{Kind: qname.SelectorPattern, ProjectGlob: project, KeyGlob: "*", Format: sel.Format}.Text(qname.Context{})
	case sel.Kind == qname.SelectorClass:
		return s
	}
	sel.ProjectGlob = project
	return sel.Text(qname.Context{})
}

// covers reports whether the grant selector g certainly covers every repository the selector t
// selects, as far as the text tells without the repository index: the same selector, the class
// @any without a format filter, or a key pattern without a format filter that matches the project
// and the key of t. Both are instance selectors. false means "not known here", not "disjoint".
func covers(g, t string) bool {
	if g == t || g == "@any" {
		return true
	}
	gs, err := parseSelector(g, qname.Context{})
	if err != nil || gs.Kind != qname.SelectorPattern || gs.Format != "" {
		return false
	}
	ts, err := parseSelector(t, qname.Context{})
	return err == nil && ts.Kind == qname.SelectorKey && qname.Glob(gs.ProjectGlob, ts.Project) && qname.Glob(gs.KeyGlob, ts.Key)
}

// disjoint reports whether the two instance selectors certainly select no common repository: two
// different keys, or a key that a format-free pattern does not match. Everything else may overlap.
func disjoint(a, b string) bool {
	if a == b {
		return false
	}
	as, errA := parseSelector(a, qname.Context{})
	bs, errB := parseSelector(b, qname.Context{})
	if errA != nil || errB != nil {
		return false
	}
	switch {
	case as.Kind == qname.SelectorKey && bs.Kind == qname.SelectorKey:
		return true
	case as.Kind == qname.SelectorKey && bs.Kind == qname.SelectorPattern && bs.Format == "":
		return !patternMatches(bs, as)
	case bs.Kind == qname.SelectorKey && as.Kind == qname.SelectorPattern && as.Format == "":
		return !patternMatches(as, bs)
	}
	return false
}

// patternMatches reports whether the pattern selector p matches the key selector k.
func patternMatches(p, k qname.Selector) bool {
	return qname.Glob(p.ProjectGlob, k.Project) && qname.Glob(p.KeyGlob, k.Key)
}
