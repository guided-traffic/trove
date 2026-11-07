package authz

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/claims"
)

// The questions the exchange asks the index (R-CI-007, scope_malformed, scope_not_granted and the answer's scope),
// on the fixture of ci_test.go: the mapping gitlab-internal/acme is named by ci-read-internal (read on npm-all,
// npm-internal, npm-web), ci-manage (read and manage on npm-loose) and the project policy web-ci (read and deploy on
// npm-web); its group team-web by ci-readers-loose (read on npm-loose). The request's scope and the answer's items
// are an instance context: every selector names its project (Q-AUTHZ-216).
func TestR_CI_007_TheScopeQuestionsOfTheExchange(t *testing.T) {
	e, _ := ciEngine(t)
	acme := systemTier("gitlab-internal/acme", []string{"team-web"},
		inline([]string{"it/npm-internal"}, []string{"@acme-billing/*"}, "", claims.Read, claims.Deploy))
	s := e.CIScopeOf(frozenOf("101", acme))

	t.Run("R-CI-007 the policy and group items in the request grammar, selectors as written, never manage", func(t *testing.T) {
		got := s.PolicyItems()
		for _, want := range []string{"read:it/npm-all", "read:it/npm-internal", "read:web/npm-web", "deploy:web/npm-web", "read:it/npm-loose"} {
			if !slices.Contains(got, want) {
				t.Errorf("PolicyItems() = %v lacks %s", got, want)
			}
		}
		seen := map[string]bool{}
		for _, it := range got {
			if it == "manage:it/npm-loose" || seen[it] {
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
			{claims.Read, "it/npm-loose", true},
			{claims.Deploy, "it/npm-internal", true},
			{claims.Deploy, "web/npm-web", true},
			{claims.Deploy, "*/npm-web", true},
			{claims.Deploy, "it/npm-loose", false},
			{claims.Delete, "@any", false},
			{claims.Deploy, "it/npm-all", false}, // a virtual repository takes no deploy (R-AUTHZ-009)
			{claims.Read, "npm-loose", false},    // a bare key is no selector of the request's scope (Q-AUTHZ-216)
		} {
			if got := s.Held(c.a, c.sel); got != c.want {
				t.Errorf("Held(%s, %s) = %v, want %v", c.a, c.sel, got, c.want)
			}
		}
	})

	t.Run("R-CI-012 a capped contribution holds read only, and lists read items only", func(t *testing.T) {
		capped := e.CIScopeOf(frozenOf("900", capped(acme)))
		if capped.Held(claims.Deploy, "it/npm-internal") || capped.Held(claims.Deploy, "web/npm-web") || !capped.Held(claims.Read, "web/npm-web") {
			t.Error("the cap read does not bound what is held")
		}
		if slices.Contains(capped.PolicyItems(), "deploy:web/npm-web") {
			t.Errorf("PolicyItems() under the cap = %v", capped.PolicyItems())
		}
	})

	t.Run("H-418 a key selector naming no repository is held by nothing, as one the grants do not cover", func(t *testing.T) {
		for _, sel := range []string{"it/npm-nowhere", "shop/npm-shop", "web/npm-internal"} {
			if s.Held(claims.Read, sel) {
				t.Errorf("Held(read, %s)", sel)
			}
		}
	})

	t.Run("R-CI-007 a bundle no policy names holds its inline grants only", func(t *testing.T) {
		other := e.CIScopeOf(frozenOf("102", systemTier("gitlab-internal/other", nil,
			inline([]string{"it/npm-internal"}, []string{"*"}, "", claims.Read))))
		if len(other.PolicyItems()) != 0 || other.Held(claims.Read, "it/npm-all") || !other.Held(claims.Read, "it/npm-internal") {
			t.Errorf("items %v", other.PolicyItems())
		}
	})
}
