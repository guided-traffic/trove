package authz

import (
	"slices"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/repository"
)

// The questions the token exchange asks the index about the bundle it is about to issue (R-CI-007, steps 8 and
// 10, work package W4 of docs/developer/ci-oidc.md): whether a `scope` item names a repository that exists,
// whether the token's grants hold an item that the text of the inline grants cannot decide (a policy or group
// grant, a class or a format filter against a key), and which `<action>:<selector>` items the policy and group
// grants give, for the `scope` member of the answer. The answers are the index's, in memory: the exchange asks
// them inside its transaction, which calls no other service (R-ARCH-019).

// textGrant is one policy entry of a CI mapping principal or of a group as the policy writes it: the policy's
// project ("" for a global policy), its selectors as text, and the actions the entry gives on at least one
// repository the selectors cover (a virtual repository keeps read and manage only, as for the grants).
type textGrant struct {
	project   string
	selectors []string
	actions   ActionSet
}

// CIScope is the view of one CI bundle on the index of the replica.
type CIScope struct {
	e   *Engine
	idx *Index
	g   CIGrantSet
}

// CIScopeOf resolves a bundle against the installed index. Before the first index exists (never on a replica that
// serves) the view holds nothing and knows no repository.
func (e *Engine) CIScopeOf(f *claims.Frozen) CIScope {
	idx := e.Index()
	if idx == nil || f == nil {
		return CIScope{e: e}
	}
	sub := Subject{Kind: principal.KindCI, Name: "ci-exchange", CI: f, Mappings: f.Mappings}
	return CIScope{e: e, idx: idx, g: idx.CIGrants(sub)}
}

// UnknownKey reports whether a selector of a `scope` item is a key selector naming no repository of the
// installation (R-CI-007: scope_malformed). Patterns and classes are never unknown.
func (s CIScope) UnknownKey(selector string) bool {
	sel, err := parseSelector(selector)
	if err != nil || sel.kind != selKey {
		return false
	}
	return s.idx == nil || s.idx.repo(sel.key) == nil
}

// Held reports whether the token's grants hold the action on at least one repository the selector covers: the
// policy and group grants of every contribution and its inline grants, each under its contribution's cap and, for a
// project tier, its project's bound; never manage (R-CI-012, R-CI-013, Q-CI-26). Package and path patterns do not
// matter here: an item names a repository and an action, and the stored scope is the ceiling at use time
// (R-AUTHZ-013).
func (s CIScope) Held(a claims.Action, selector string) bool {
	if s.idx == nil {
		return false
	}
	sel, err := parseSelector(selector)
	if err != nil {
		return false
	}
	act := Action(a)
	for _, r := range s.idx.repos {
		if sel.matches(r.repoView) && s.holdsOn(act, r) {
			return true
		}
	}
	return false
}

// holdsOn is Held for one repository.
func (s CIScope) holdsOn(act Action, r *repoInfo) bool {
	if r.Class == repository.Virtual && act != Read {
		return false
	}
	for i := range s.g.contributions {
		c := &s.g.contributions[i]
		if !c.admits(act) {
			continue
		}
		for _, id := range append([]string{c.pid}, c.groups...) {
			if id == "" {
				continue
			}
			for _, gr := range s.idx.grantsFor(r.ID, id) {
				if (c.project == "" || gr.project == c.project) && gr.actions.Has(act) {
					return true
				}
			}
		}
		for _, ig := range c.inlineFor(r) {
			if slices.Contains(ig.Actions, claims.Action(act)) && s.e.compile.selectorsMatch(ig.Repositories, r.repoView) {
				return true
			}
		}
	}
	return false
}

// PolicyItems lists the items of the token's policy and group grants in the grammar of the `scope` parameter,
// `<action>:<selector>`, the selectors as the policies write them, each once: per contribution, per policy entry
// of its mapping principal and of its groups, per selector, per action in the order read, annotate, deploy,
// overwrite, delete. A contribution's cap and project bound apply; manage never appears (R-CI-007, R-CI-013).
func (s CIScope) PolicyItems() []string {
	if s.idx == nil {
		return nil
	}
	var out []string
	for i := range s.g.contributions {
		c := &s.g.contributions[i]
		for _, id := range append([]string{c.pid}, c.groups...) {
			if id == "" {
				continue
			}
			for _, tg := range s.idx.ciTexts[id] {
				if c.project != "" && tg.project != c.project {
					continue
				}
				for _, sel := range tg.selectors {
					for _, a := range []Action{Read, Annotate, Deploy, Overwrite, Delete} {
						if !tg.actions.Has(a) || !c.admits(a) {
							continue
						}
						if item := string(a) + ":" + sel; !slices.Contains(out, item) {
							out = append(out, item)
						}
					}
				}
			}
		}
	}
	return out
}
