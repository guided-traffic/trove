package authz

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/claims"
)

// The questions the exchange asks the index (R-CI-007, scope_malformed, scope_not_granted and the answer's scope),
// on the fixture of ci_test.go: the mapping gitlab-internal/acme is named by ci-read-internal (read on npm-all,
// npm-internal, npm-web), ci-manage (read and manage on npm-loose) and the project policy web-ci (read and deploy on
// npm-web); its group team-web by ci-readers-loose (read on npm-loose).
func TestR_CI_007_TheScopeQuestionsOfTheExchange(t *testing.T) {
	e, _ := ciEngine(t)
	acme := systemTier("gitlab-internal/acme", []string{"team-web"},
		inline([]string{"npm-internal"}, []string{"@acme-billing/*"}, "", claims.Read, claims.Deploy))
	s := e.CIScopeOf(frozenOf("101", acme))

	t.Run("R-CI-007 the policy and group items in the request grammar, selectors as written, never manage", func(t *testing.T) {
		got := s.PolicyItems()
		for _, want := range []string{"read:npm-all", "read:npm-internal", "read:npm-web", "deploy:npm-web", "read:npm-loose"} {
			if !slices.Contains(got, want) {
				t.Errorf("PolicyItems() = %v lacks %s", got, want)
			}
		}
		seen := map[string]bool{}
		for _, it := range got {
			if it == "manage:npm-loose" || seen[it] {
				t.Errorf("PolicyItems() = %v", got)
			}
			seen[it] = true
		}
	})

	t.Run("R-CI-007 held: a class, a policy action, an inline action, nothing beyond", func(t *testing.T) {
		for _, c := range []struct {
			a    claims.Action
			sel  string
			want bool
		}{
			{claims.Read, "@any-local:npm", true},
			{claims.Read, "npm-loose", true},
			{claims.Deploy, "npm-internal", true},
			{claims.Deploy, "npm-web", true},
			{claims.Deploy, "npm-loose", false},
			{claims.Delete, "@any", false},
			{claims.Deploy, "npm-all", false}, // a virtual repository takes no deploy (R-AUTHZ-009)
		} {
			if got := s.Held(c.a, c.sel); got != c.want {
				t.Errorf("Held(%s, %s) = %v, want %v", c.a, c.sel, got, c.want)
			}
		}
	})

	t.Run("R-CI-012 a capped contribution holds read only, and lists read items only", func(t *testing.T) {
		capped := e.CIScopeOf(frozenOf("900", capped(acme)))
		if capped.Held(claims.Deploy, "npm-internal") || capped.Held(claims.Deploy, "npm-web") || !capped.Held(claims.Read, "npm-web") {
			t.Error("the cap read does not bound what is held")
		}
		if slices.Contains(capped.PolicyItems(), "deploy:npm-web") {
			t.Errorf("PolicyItems() under the cap = %v", capped.PolicyItems())
		}
	})

	t.Run("R-CI-007 a key selector naming no repository is unknown; patterns and classes never are", func(t *testing.T) {
		for sel, want := range map[string]bool{"npm-nowhere": true, "npm-internal": false, "npm-*": false, "@any-local:npm": false} {
			if got := s.UnknownKey(sel); got != want {
				t.Errorf("UnknownKey(%s) = %v", sel, got)
			}
		}
	})

	t.Run("R-CI-007 a bundle no policy names holds its inline grants only", func(t *testing.T) {
		other := e.CIScopeOf(frozenOf("102", systemTier("gitlab-internal/other", nil,
			inline([]string{"npm-internal"}, []string{"*"}, "", claims.Read))))
		if len(other.PolicyItems()) != 0 || other.Held(claims.Read, "npm-all") || !other.Held(claims.Read, "npm-internal") {
			t.Errorf("items %v", other.PolicyItems())
		}
	})
}
