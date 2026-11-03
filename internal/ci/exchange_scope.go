package ci

import (
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
)

// The scope of the exchange (R-CI-007 step 8, Q-CI-2): the request's items narrow the bundle, the token stores
// them as its scope, and the answer's `scope` lists the effective grants in the request grammar. What the text of
// the inline grants cannot tell (a policy or group grant, a class or a format filter against a key) the grant index
// of the replica answers ([GrantIndex]), in memory. Whether a key names a repository is asked of nobody: a key that
// names none is not held, and answers scope_not_granted like a repository that exists and is not granted (H-418,
// Q-CI-51), so a matched job learns nothing of which repositories exist.

// GrantIndex is the grant index of the replica seen from one bundle: internal/authz.CIScope.
type GrantIndex interface {
	// Held reports whether the bundle's grants (policies, groups, inline grants, each under its contribution's cap
	// and project bound) hold the action on a repository the selector covers.
	Held(a claims.Action, selector string) bool
	// PolicyItems lists the `<action>:<selector>` items of the bundle's policy and group grants.
	PolicyItems() []string
}

// GrantIndexes resolves a bundle against the index.
type GrantIndexes interface {
	CIScope(f *claims.Frozen) GrantIndex
}

// GrantIndexFunc adapts a function to [GrantIndexes].
type GrantIndexFunc func(f *claims.Frozen) GrantIndex

// CIScope implements GrantIndexes.
func (fn GrantIndexFunc) CIScope(f *claims.Frozen) GrantIndex { return fn(f) }

// scoped is the outcome of step 8: the narrowed bundle, the token's stored scope and the answer's scope string.
type scoped struct {
	bundle  claims.Frozen
	stored  scope.Scope
	granted string
}

// narrow applies the request's items to the bundle. Without items the token inherits the bundle unnarrowed
// ({"inherit": true}) and the answer lists the policy and group items, then the inline items. With items, an item no
// grant holds is scope_not_granted (no partial token), whether or not its key names a repository (H-418), and the
// answer lists the items the index held, then the narrowed inline items.
func narrow(f claims.Frozen, items []claims.ScopeItem, idx GrantIndex) (scoped, *ExchangeError) {
	if len(items) == 0 {
		return scoped{bundle: f, stored: scope.Scope{Inherit: true}, granted: joinItems(idx.PolicyItems(), f.InlineScope())}, nil
	}
	var held []string
	narrowed, err := claims.NarrowFrozen(f, items, func(a claims.Action, selector string) bool {
		if !idx.Held(a, selector) {
			return false
		}
		item := claims.ScopeItem{Action: a, Selector: selector}
		if selector == "@any" {
			item.Selector = ""
		}
		held = append(held, item.String())
		return true
	})
	if err != nil {
		return scoped{}, fromClaims(err)
	}
	stored := scope.Scope{Repositories: make([]scope.Grant, 0, len(items))}
	for _, it := range items {
		sel := it.Selector
		if sel == "" {
			sel = "@any"
		}
		stored.Repositories = append(stored.Repositories, scope.Grant{Repositories: []string{sel}, Actions: []string{string(it.Action)}})
	}
	return scoped{bundle: narrowed, stored: stored, granted: joinItems(held, narrowed.InlineScope())}, nil
}

// joinItems is the answer's scope: the items in front, then the inline ones, each once.
func joinItems(front []string, inline string) string {
	out := slices.Clone(front)
	for _, it := range strings.Fields(inline) {
		if !slices.Contains(out, it) {
			out = append(out, it)
		}
	}
	return strings.Join(out, " ")
}
