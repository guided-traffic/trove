package authz

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// addressedFixture has two npm repositories and a virtual one over the first, with the rule of
// the endpoint customers on the virtual repository and none on its member: a listing through
// the virtual repository must decide its rows on the virtual repository's rule (Q-EP-11).
//
//	bot reads everything
func addressedFixture() *fixture {
	f := newFixture().
		repo("npm-a", "local", "npm", "").
		repo("npm-b", "local", "npm", "").
		repo("npm-v", "virtual", "npm", `{"virtual": {"members": ["npm-a"]}}`)
	f.policy(policyDef{name: "bot", selectors: []string{"@any"}, entries: map[string][]string{idBot: {"read"}}})
	f.exposure(ruleDef{endpoint: "customers", name: "acme-v", repo: "npm-v", dims: ruleDims{
		Packages: PatternLists{Include: []string{"@acme/*"}}, Versions: VersionLists{Range: ptr(">=2.0.0")},
	}})
	return f
}

// scopeOn is ReadScopeOn on a fresh index of the subject.
func scopeOn(t *testing.T, e *Engine, sub Subject, addressed string, keys ...string) ReadScope {
	t.Helper()
	idx, err := e.Begin(context.Background(), sub, append([]string{"npm-v"}, keys...)...)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := e.ReadScopeOn(context.Background(), idx, sub, addressed, keys)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryAppliesItsExposure: the rows of a member read
// for a virtual repository are exposed by the rule on the virtual repository, as its documents
// are (R-EP-012, Q-EP-11); the same rows listed on their own are decided on the member, which
// has no rule on that endpoint.
func TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryAppliesItsExposure(t *testing.T) {
	e := newTestEngine(t, addressedFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	sub := onEP(botSub, "customers")
	through := scopeOn(t, e, sub, "npm-v", "npm-a")
	if got := through.Shape("npm-a"); got != ReadRows {
		t.Fatalf("shape of the member through the virtual repository: %s", got)
	}
	for _, c := range []struct {
		pkg, version string
		want         bool
	}{
		{"@acme/sdk", "2.1.0", true},
		{"@acme/sdk", "1.9.0", false},
		{"@acme/sdk", "3.0.0-beta", false},
		{"other", "2.1.0", false},
	} {
		if got := through.MayRead("npm-a", c.pkg, c.version); got != c.want {
			t.Errorf("through npm-v: %s@%s = %v, want %v", c.pkg, c.version, got, c.want)
		}
	}
	direct := scopeOn(t, e, sub, "", "npm-a")
	if direct.MayRead("npm-a", "@acme/sdk", "2.1.0") {
		t.Error("a listing of the member on its own applies the rule of the virtual repository")
	}
}

// TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryLiftsATokenScopeThatNamesIt: a token scoped
// to the virtual repository by its key lists the member's rows through it (Q-AUTHZ-163) and
// not on their own.
func TestQ_AUTHZ_168_AListingThroughAVirtualRepositoryLiftsATokenScopeThatNamesIt(t *testing.T) {
	e := newTestEngine(t, addressedFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	sub := onEP(tokenScop(bearer(botSub), scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-v"}, Actions: []string{"read"}}}}), "default")
	through := scopeOn(t, e, sub, "npm-v", "npm-a")
	if through.Shape("npm-a") != ReadRows || !through.MayRead("npm-a", "left-pad", "1.0.0") {
		t.Fatalf("through npm-v: shape %s, read %v", through.Shape("npm-a"), through.MayRead("npm-a", "left-pad", "1.0.0"))
	}
	if scopeOn(t, e, sub, "", "npm-a").MayRead("npm-a", "left-pad", "1.0.0") {
		t.Error("the scope on the virtual repository opened the member addressed on its own")
	}
}

// TestQ_AUTHZ_168_ARepositoryTheAddressedOneDoesNotReachIsNotListed: a key that is not the
// addressed repository and not a member it resolves through is no read the client asked for.
func TestQ_AUTHZ_168_ARepositoryTheAddressedOneDoesNotReachIsNotListed(t *testing.T) {
	e := newTestEngine(t, addressedFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	sub := onEP(botSub, "default")
	for _, addressed := range []string{"npm-v", "npm-a", "ghost"} {
		rs := scopeOn(t, e, sub, addressed, "npm-b")
		if rs.Shape("npm-b") != ReadNone || rs.MayRead("npm-b", "left-pad", "1.0.0") {
			t.Errorf("addressed %s: npm-b is %s, read %v", addressed, rs.Shape("npm-b"), rs.MayRead("npm-b", "left-pad", "1.0.0"))
		}
	}
	if rs := scopeOn(t, e, sub, "npm-b", "npm-b"); rs.Shape("npm-b") != ReadAll {
		t.Errorf("the addressed repository itself: %s", rs.Shape("npm-b"))
	}
}

// TestQ_AUTHZ_168_AReadScopeThroughAVirtualRepositoryAgreesWithTheDecisionPoint: MayRead through
// the virtual repository is the decision of DecideOn for the coordinate whose Addressed is the
// virtual repository, and the shape never promises more or less.
func TestQ_AUTHZ_168_AReadScopeThroughAVirtualRepositoryAgreesWithTheDecisionPoint(t *testing.T) {
	e := newTestEngine(t, addressedFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	subjects := map[string]Subject{
		"bot": botSub, "carol": carolSub, "admin": adminSub,
		"scoped to the virtual repository": tokenScop(bearer(botSub), scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-v"}, Actions: []string{"read"}}}}),
		"scoped to the member":             tokenScop(bearer(botSub), scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}}),
	}
	checked := 0
	for name, base := range subjects {
		for _, ep := range []string{"", "default", "customers"} {
			sub := onEP(base, ep)
			ctx := context.Background()
			idx, err := e.Begin(ctx, sub, "npm-a", "npm-v")
			if err != nil {
				t.Fatal(err)
			}
			rs, err := e.ReadScopeOn(ctx, idx, sub, "npm-v", []string{"npm-a"})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"@acme/sdk", "left-pad"} {
				for _, v := range []string{"", "1.9.0", "2.1.0", "3.0.0-beta"} {
					c := Coordinate{Repository: "npm-a", Package: p, Version: v, Kind: KindMetadata, Actions: []Action{Read}, Addressed: "npm-v"}
					decided := e.DecideOn(ctx, idx, sub, c).Outcome == Allow
					if got := rs.MayRead("npm-a", p, v); got != decided {
						t.Errorf("%s on %q reads npm-a %s@%s through npm-v: MayRead %v, DecideOn %v", name, ep, p, v, got, decided)
					}
					switch rs.Shape("npm-a") {
					case ReadAll:
						if !decided {
							t.Errorf("%s on %q: ReadAll and %s@%s is refused", name, ep, p, v)
						}
					case ReadNone:
						if decided {
							t.Errorf("%s on %q: ReadNone and %s@%s is allowed", name, ep, p, v)
						}
					}
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("nothing was checked")
	}
}
