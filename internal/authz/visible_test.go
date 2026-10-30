package authz

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// readScopeFixture has two npm repositories and a virtual one over the first:
//
//	bot    reads everything
//	alice  reads npm-b only for the packages @a/*
//	bob    holds a grant on npm-a that only covers the paths x/**
//	carol  holds no grant
//	admin  holds the admin role, so content:bypass
//
// and the endpoint customers exposes npm-a for @acme/* from 2.0.0 on.
func readScopeFixture() *fixture {
	f := newFixture().
		repo("npm-a", "local", "npm", "").
		repo("npm-b", "local", "npm", "").
		repo("npm-v", "virtual", "npm", `{"virtual": {"members": ["npm-a"]}}`)
	f.policy(policyDef{name: "bot", selectors: []string{"@any"}, entries: map[string][]string{idBot: {"read"}}})
	f.policy(policyDef{name: "alice-b", selectors: []string{"npm-b"}, pkgInc: []string{"@a/*"}, entries: map[string][]string{idAlice: {"read"}}})
	f.policy(policyDef{name: "bob-path", selectors: []string{"npm-a"}, pathInc: []string{"x/**"}, entries: map[string][]string{idBob: {"read"}}})
	f.bind(RoleAdmin, idOps)
	f.exposure(ruleDef{endpoint: "customers", name: "acme", repo: "npm-a", dims: ruleDims{
		Packages: PatternLists{Include: []string{"@acme/*"}}, Versions: VersionLists{Range: ptr(">=2.0.0")},
	}})
	return f
}

// TestR_API_029_ReadScopeShapes: the classification of a repository for a listing
// that aggregates versions (R-API-029, R-EP-014).
func TestR_API_029_ReadScopeShapes(t *testing.T) {
	e := newTestEngine(t, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	keys := []string{"npm-a", "npm-b", "npm-v", "ghost"}
	shapes := func(t *testing.T, sub Subject) map[string]ReadShape {
		t.Helper()
		idx, err := e.Begin(context.Background(), sub, keys...)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := e.ReadScopeOn(context.Background(), idx, sub, keys)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]ReadShape{}
		for _, k := range keys {
			out[k] = rs.Shape(k)
		}
		return out
	}
	adminOnly := adminSub
	cases := []struct {
		name string
		sub  Subject
		want map[string]ReadShape
	}{
		{"an unrestricted read grant on endpoint default reads every package", onEP(botSub, "default"), map[string]ReadShape{"npm-a": ReadAll, "npm-b": ReadAll, "npm-v": ReadNone, "ghost": ReadNone}},
		{"a subject without an endpoint has no ceiling", botSub, map[string]ReadShape{"npm-a": ReadAll, "npm-b": ReadAll, "npm-v": ReadNone, "ghost": ReadNone}},
		{"an endpoint with rules decides the rows one by one", onEP(botSub, "customers"), map[string]ReadShape{"npm-a": ReadRows, "npm-b": ReadRows, "npm-v": ReadNone, "ghost": ReadNone}},
		{"an endpoint the configuration does not hold is treated as rules", onEP(botSub, "nowhere"), map[string]ReadShape{"npm-a": ReadRows, "npm-b": ReadRows, "npm-v": ReadNone, "ghost": ReadNone}},
		{"a grant narrowed by package patterns is decided row by row", onEP(aliceSub, "default"), map[string]ReadShape{"npm-a": ReadNone, "npm-b": ReadRows, "npm-v": ReadNone, "ghost": ReadNone}},
		{"a grant that only covers paths is decided row by row, and reads no package", onEP(bobSub, "default"), map[string]ReadShape{"npm-a": ReadRows, "npm-b": ReadNone, "npm-v": ReadNone, "ghost": ReadNone}},
		{"no grant reads nothing", onEP(carolSub, "default"), map[string]ReadShape{"npm-a": ReadNone, "npm-b": ReadNone, "npm-v": ReadNone, "ghost": ReadNone}},
		{"content:bypass reads every package", onEP(adminOnly, "default"), map[string]ReadShape{"npm-a": ReadAll, "npm-b": ReadAll, "npm-v": ReadNone, "ghost": ReadNone}},
		{"content:bypass is bound by the ceiling of an endpoint with rules", onEP(adminOnly, "customers"), map[string]ReadShape{"npm-a": ReadRows, "npm-b": ReadRows, "npm-v": ReadNone, "ghost": ReadNone}},
		{"a token scope that lists elements decides row by row", onEP(tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}}), "default"), map[string]ReadShape{"npm-a": ReadRows, "npm-b": ReadRows, "npm-v": ReadNone, "ghost": ReadNone}},
		{"a token scope that inherits does not narrow", onEP(tokenScop(botSub, scope.Scope{Inherit: true}), "default"), map[string]ReadShape{"npm-a": ReadAll, "npm-b": ReadAll, "npm-v": ReadNone, "ghost": ReadNone}},
		{"an anonymous subject reads nothing while the switch is off", onEP(anonSub, "default"), map[string]ReadShape{"npm-a": ReadNone, "npm-b": ReadNone, "npm-v": ReadNone, "ghost": ReadNone}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shapes(t, tc.sub)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s: %s, want %s", k, got[k], want)
				}
			}
		})
	}
}

// TestR_API_029_ReadScopeAgreesWithTheDecisionPoint: whatever shape a repository has,
// it never promises more or less than DecideOn answers for a package read of a
// repository that holds rows: ReadAll allows every row, ReadNone allows none, and
// MayRead is the decision of DecideOn.
func TestR_API_029_ReadScopeAgreesWithTheDecisionPoint(t *testing.T) {
	e := newTestEngine(t, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	keys := []string{"npm-a", "npm-b", "npm-v"}
	subjects := map[string]Subject{
		"bot": botSub, "alice": aliceSub, "bob": bobSub, "carol": carolSub, "admin": adminSub,
		"scoped bot": tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}}),
	}
	pkgs := []string{"@a/x", "@acme/sdk", "@b/y", "plain"}
	versions := []string{"", "1.0.0", "2.1.0", "3.0.0-beta"}
	checked := 0
	for name, base := range subjects {
		for _, ep := range []string{"", "default", "customers", "nowhere"} {
			sub := onEP(base, ep)
			ctx := context.Background()
			idx, err := e.Begin(ctx, sub, keys...)
			if err != nil {
				t.Fatal(err)
			}
			rs, err := e.ReadScopeOn(ctx, idx, sub, keys)
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range keys {
				for _, p := range pkgs {
					for _, v := range versions {
						c := Coordinate{Repository: k, Package: p, Version: v, Kind: KindMetadata, Actions: []Action{Read}}
						decided := e.DecideOn(ctx, idx, sub, c).Outcome == Allow
						if got := rs.MayRead(k, p, v); got != decided {
							t.Errorf("%s on %q reads %s %s@%s: MayRead %v, DecideOn %v", name, ep, k, p, v, got, decided)
						}
						// A virtual repository holds no rows: ReadNone says so, and its gate
						// allows a read whatever the shape.
						shape := rs.Shape(k)
						if k == "npm-v" {
							shape = ReadRows
						}
						switch shape {
						case ReadAll:
							if !decided {
								t.Errorf("%s on %q: %s is ReadAll and %s@%s is refused", name, ep, k, p, v)
							}
						case ReadNone:
							if decided {
								t.Errorf("%s on %q: %s is ReadNone and %s@%s is allowed", name, ep, k, p, v)
							}
						}
						checked++
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("nothing was checked")
	}
}

// TestR_API_029_ReadScopeExposureDecidesVersions: on an endpoint with rules the rows
// follow the rule's packages and range, and a pre-release is hidden unless the rule
// admits it.
func TestR_API_029_ReadScopeExposureDecidesVersions(t *testing.T) {
	e := newTestEngine(t, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	sub := onEP(botSub, "customers")
	idx, err := e.Begin(context.Background(), sub, "npm-a", "npm-b")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := e.ReadScopeOn(context.Background(), idx, sub, []string{"npm-a", "npm-b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		repo, pkg, version string
		want               bool
	}{
		{"npm-a", "@acme/sdk", "2.1.0", true},
		{"npm-a", "@acme/sdk", "1.9.0", false},
		{"npm-a", "@acme/sdk", "3.0.0-beta", false},
		{"npm-a", "other", "2.1.0", false},
		{"npm-b", "@acme/sdk", "2.1.0", false},
	} {
		if got := rs.MayRead(tc.repo, tc.pkg, tc.version); got != tc.want {
			t.Errorf("%s %s@%s: %v, want %v", tc.repo, tc.pkg, tc.version, got, tc.want)
		}
	}
}
