package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// TestR_AUTHZ_005_TheCatalogsNameRuleFoldsEveryPatternSurface: grants, token
// scopes and exposure rules fold a name and a pattern with the one function the
// format registered, and a format that registered no rule folds nothing
// (Q-ARCH-188). Nothing in the decision point names a format.
func TestR_AUTHZ_005_TheCatalogsNameRuleFoldsEveryPatternSurface(t *testing.T) {
	fixture := func() *fixture {
		f := newFixture().repo("pkgs-internal", "local", "nuget", "").repo("exact-internal", "local", "npm", "")
		f.policy(policyDef{name: "grant", selectors: []string{"@any"}, pkgInc: []string{"Acme.*"}, entries: map[string][]string{idBob: {"read"}}})
		f.exposure(ruleDef{endpoint: "customers", name: "acme", repo: "pkgs-internal", dims: ruleDims{Packages: PatternLists{Include: []string{"Acme.*"}}}})
		return f
	}
	exposable := func(names catalog.Names) *catalog.Registry {
		r := catalog.NewRegistry()
		r.Register(catalog.Entry{
			Facts: catalog.Facts{ID: "nuget", Classes: []catalog.Class{catalog.Local}, Layout: "nuget", PatternSubject: catalog.SubjectName, PackageNames: names != nil},
			Names: names, Exposure: catalog.Exposure{Exposable: true, Packages: names != nil},
		})
		r.Register(catalog.Entry{
			Facts: catalog.Facts{ID: "npm", Classes: []catalog.Class{catalog.Local}, Layout: "npm", PatternSubject: catalog.SubjectName, PackageNames: true},
			Names: exactNames{}, Exposure: catalog.Exposure{Exposable: true, Packages: true},
		})
		return r
	}
	folding := newTestEngine(t, fixture(), func(o *Options) {
		o.Formats = exposable(lowerNames{})
		o.Ceiling = NewExposureCeiling(map[string]string{"customers": ExposureModeRules}, o.Formats)
	})
	plain := newTestEngine(t, fixture(), func(o *Options) {
		o.Formats = exposable(nil)
		o.Ceiling = NewExposureCeiling(map[string]string{"customers": ExposureModeRules}, o.Formats)
	})
	cases := []struct {
		name string
		e    *Engine
		sub  Subject
		c    Coordinate
		want Outcome
	}{
		{"a grant folds the pattern and the name", folding, bobSub, read("pkgs-internal", "ACME.Core", ""), Allow},
		{"a grant without a name rule compares as written", plain, bobSub, read("pkgs-internal", "ACME.Core", ""), DenyHidden},
		{"a name rule of another format does not fold this one", folding, bobSub, read("exact-internal", "ACME.Core", ""), DenyHidden},
		{"a token scope folds with the repository's format", folding, tokenScop(bobSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"@any"}, Packages: scope.Patterns{Include: []string{"ACME.*"}}, Actions: []string{"read"}}}}), read("pkgs-internal", "acme.core", ""), Allow},
		{"a token scope without a name rule compares as written", plain, tokenScop(bobSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"@any"}, Packages: scope.Patterns{Include: []string{"ACME.*"}}, Actions: []string{"read"}}}}), read("pkgs-internal", "acme.core", ""), DenyHidden},
		{"an exposure rule folds on the endpoint", folding, onEP(bobSub, "customers"), read("pkgs-internal", "aCmE.core", ""), Allow},
		{"an exposure rule without a name rule compares as written", plain, onEP(bobSub, "customers"), read("pkgs-internal", "acme.core", ""), DenyHidden},
	}
	for _, c := range cases {
		t.Run("R-AUTHZ-005 R-EP-012 "+c.name, func(t *testing.T) {
			// The plain engine's grant is "Acme.*": only an exact spelling matches there.
			if d := c.e.Decide(t.Context(), c.sub, c.c); d.Outcome != c.want {
				t.Fatalf("%+v", d)
			}
		})
	}
	t.Run("R-AUTHZ-005 the plain engine still matches the spelling the pattern gives", func(t *testing.T) {
		if d := plain.Decide(t.Context(), bobSub, read("pkgs-internal", "Acme.Core", "")); d.Outcome != Allow {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("R-AUTHZ-005 the unit registry of the tests is the catalog's, not a constant", func(t *testing.T) {
		if got := fold(lowerNames{}, "Acme.*"); got != "acme.*" {
			t.Fatalf("fold = %q", got)
		}
		if got := fold(nil, "Acme.*"); got != "Acme.*" {
			t.Fatalf("a nil rule folds %q", got)
		}
		if got := fold(exactNames{}, ""); got != "" {
			t.Fatalf("a string that is no name stays as it is: %q", got)
		}
	})
}
