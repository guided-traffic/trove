package authz

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/repository"
)

// reachFixture is the fixture of the read shapes (R-BAS-012). Repositories:
//
//	npm-a, npm-b      local
//	npm-strict        local, requireAuthentication
//	npmjs             remote, fills on read
//	npm-frozen        remote, fills on deploy
//	npm-v             virtual over npm-a and npmjs
//	npm-v2            virtual over npm-v and npm-frozen (nested)
//	npm-vb            virtual over npm-b
//	npm-empty         virtual without members
//
// Subjects: bot reads everything and deploys into the frozen mirror; devs (alice) read npm-a, npmjs and
// npm-v, and npm-b for @a/* only; bob holds a path-only grant on npm-a; carol holds nothing; anonymous reads
// npm-a and npmjs; the auditor holds content:metadata-read and no grant; ops holds the admin role.
func reachFixture() *fixture {
	f := newFixture().
		repo("npm-a", "local", "npm", "").
		repo("npm-b", "local", "npm", "").
		repo("npm-strict", "local", "npm", `{"requireAuthentication":true}`).
		repo("npmjs", "remote", "npm", `{"remote":{"cacheFillAction":"read"}}`).
		repo("npm-frozen", "remote", "npm", `{"remote":{"cacheFillAction":"deploy"}}`).
		repo("npm-v", "virtual", "npm", `{"virtual":{"members":["npm-a","npmjs"]}}`).
		repo("npm-v2", "virtual", "npm", `{"virtual":{"members":["npm-v","npm-frozen"]}}`).
		repo("npm-vb", "virtual", "npm", `{"virtual":{"members":["npm-b"]}}`).
		repo("npm-empty", "virtual", "npm", `{"virtual":{"members":[]}}`)
	f.policy(policyDef{name: "bot-read", selectors: []string{"@any"}, entries: map[string][]string{idBot: {"read"}}})
	f.policy(policyDef{name: "bot-fill", selectors: []string{"npm-frozen"}, entries: map[string][]string{idBot: {"deploy"}}})
	f.policy(policyDef{name: "readers", selectors: []string{"@any"}, entries: map[string][]string{"group-readers": {"read"}}})
	f.policy(policyDef{name: "v2-readers", selectors: []string{"npm-v2", "npm-a", "npmjs", "npm-frozen"}, entries: map[string][]string{"group-v2": {"read"}}})
	f.policy(policyDef{name: "devs", selectors: []string{"npm-a", "npmjs", "npm-v", "npm-vb"}, entries: map[string][]string{idDevs: {"read"}}})
	f.policy(policyDef{name: "devs-b", selectors: []string{"npm-b"}, pkgInc: []string{"@a/*"}, entries: map[string][]string{idDevs: {"read"}}})
	f.policy(policyDef{name: "bob-path", selectors: []string{"npm-a"}, pathInc: []string{"x/**"}, entries: map[string][]string{idBob: {"read"}}})
	f.policy(policyDef{name: "anon", selectors: []string{"npm-a", "npmjs", "npm-strict", "npm-v"}, entries: map[string][]string{AnonymousID: {"read"}}})
	f.bind(RoleAdmin, idOps).bind(RoleAuditor, idAuditorDirect)
	f.exposure(ruleDef{endpoint: "customers", name: "acme", repo: "npm-a", dims: ruleDims{
		Packages: PatternLists{Include: []string{"@acme/*"}},
	}})
	return f
}

func reachCeiling() *ExposureCeiling {
	return NewExposureCeiling(map[string]string{"default": ExposureModeAll, "customers": ExposureModeRules}, testRegistry())
}

// leaf is a local or remote repository a read through a virtual repository resolves to, with the nested virtual
// repositories on the way, whose gates the resolver asks as a member's read.
type leaf struct {
	key    string
	nested []string
}

func leavesOf(idx *Index, key string, via []string, depth int) []leaf {
	r := idx.repo(key)
	if r == nil || depth > maxReachDepth {
		return nil
	}
	if r.Class != repository.Virtual {
		return []leaf{{key: key, nested: via}}
	}
	var out []leaf
	for _, m := range r.members {
		next := via
		if mr := idx.repo(m); mr != nil && mr.Class == repository.Virtual {
			next = append(append([]string(nil), via...), m)
		}
		out = append(out, leavesOf(idx, m, next, depth+1)...)
	}
	return out
}

// TestR_BAS_012_ShapeAgreesWithTheDecisionPoint: over every subject, endpoint, repository, package, version and
// kind of the fixture, a full reach never meets a refused read and a none reach never an allowed one. A read
// through a virtual repository is decided on its gate and on the leaf that holds the row, with the virtual
// repository as the addressed one (R-AUTHZ-009, Q-EP-11).
func TestR_BAS_012_ShapeAgreesWithTheDecisionPoint(t *testing.T) {
	subjects := map[string]Subject{
		"bot": botSub, "alice": aliceSub, "bob": bobSub, "carol": carolSub, "admin": adminSub, "anonymous": anonSub,
		"auditor": userSub(idAuditorDirect, "auditor"), "reader": userSub("user-reader", "reader", "group-readers"),
		"v2 reader":                userSub("user-v2", "v2", "group-v2"),
		"scoped bot":               tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}}),
		"scoped bot through npm-v": tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-v"}, Actions: []string{"read"}}}}),
		"inheriting bot":           tokenScop(botSub, scope.Scope{Inherit: true}),
		"session bot":              session(botSub),
	}
	keys := []string{"npm-a", "npm-b", "npm-strict", "npmjs", "npm-frozen", "npm-v", "npm-v2", "npm-vb", "npm-empty", "ghost"}
	pkgs := []string{"@a/x", "@acme/sdk", "x", "plain"}
	versions := []string{"", "1.0.0", "3.0.0-beta"}
	paths := []string{"", "x/y", "plain/-/plain-1.0.0.tgz"}
	var counts [3]int
	for _, anonOnState := range []bool{false, true} {
		on := anonOnState
		e := newTestEngine(t, reachFixture(), func(o *Options) {
			o.Ceiling = reachCeiling()
			o.AnonymousAccess = func(context.Context) (bool, error) { return on, nil }
		})
		for name, base := range subjects {
			for _, ep := range []string{"", "default", "customers"} {
				sub := onEP(base, ep)
				ctx := context.Background()
				idx, err := e.Begin(ctx, sub, keys...)
				if err != nil {
					t.Fatal(err)
				}
				got, err := e.ReachOn(ctx, idx, sub, keys)
				if err != nil {
					t.Fatal(err)
				}
				for _, k := range keys {
					res, ok := got[k]
					if !ok {
						t.Fatalf("%s on %q (anonymous %v): no answer for %s", name, ep, on, k)
					}
					counts[res.Reach]++
					where := func() string {
						return name + " on " + ep + " (anonymous access " + map[bool]string{false: "off", true: "on"}[on] + ") " + k
					}
					if res.Reach != ReachNone && res.Denial != Allow {
						t.Errorf("%s: reach %s carries the denial %d", where(), res.Reach, res.Denial)
					}
					if res.Reach == ReachNone && res.Denial == Allow {
						t.Errorf("%s: reach none without a denial", where())
					}
					leaves := leavesOf(idx, k, nil, 0)
					virtual := idx.repo(k) != nil && idx.repo(k).Class == repository.Virtual
					addressed := ""
					if virtual {
						addressed = k
					}
					// gate: the read of the repository itself, for a virtual one.
					gateOK := func(kind RequestKind) bool {
						return e.DecideOn(ctx, idx, sub, Coordinate{Repository: k, Kind: kind, Actions: []Action{Read}}).Outcome == Allow
					}
					anyAllowed, allAllowed := false, len(leaves) > 0
					for _, kind := range []RequestKind{KindMetadata, KindArtifact} {
						if virtual && !gateOK(kind) {
							allAllowed = false
							continue
						}
						for _, lf := range leaves {
							leaf := lf.key
							// the gates of the nested virtual repositories, asked as the read of a member
							nestedOK := true
							for _, n := range lf.nested {
								nc := Coordinate{Repository: n, Kind: kind, Actions: []Action{Read}, Addressed: addressed}
								nestedOK = nestedOK && e.DecideOn(ctx, idx, sub, nc).Outcome == Allow
							}
							for _, p := range pkgs {
								for _, v := range versions {
									for _, path := range paths {
										c := Coordinate{Repository: leaf, Package: p, Version: v, Path: path, Kind: kind, Actions: []Action{Read}, Addressed: addressed}
										ok := nestedOK && e.DecideOn(ctx, idx, sub, c).Outcome == Allow
										if ok && kind == KindArtifact && idx.repo(leaf).Class == repository.Remote {
											ok = e.MayPopulateOn(ctx, idx, sub, c).Outcome == Allow
										}
										anyAllowed = anyAllowed || ok
										allAllowed = allAllowed && ok
									}
								}
							}
						}
					}
					switch res.Reach {
					case ReachFull:
						if !allAllowed {
							t.Errorf("%s: full, and a read through it is refused", where())
						}
					case ReachNone:
						if anyAllowed {
							t.Errorf("%s: none, and a read through it is allowed", where())
						}
					}
				}
			}
		}
	}
	for r, n := range counts {
		if n == 0 {
			t.Errorf("the fixture never produced the reach %s: the test checks nothing about it", Reach(r))
		}
	}
}

// TestR_BAS_012_Shapes pins the cases of the acceptance criteria and of the rules by name.
func TestR_BAS_012_Shapes(t *testing.T) {
	on := func(o *Options) {
		o.Ceiling = reachCeiling()
		o.AnonymousAccess = func(context.Context) (bool, error) { return true, nil }
	}
	off := func(o *Options) {
		o.Ceiling = reachCeiling()
		o.AnonymousAccess = func(context.Context) (bool, error) { return false, nil }
	}
	keys := []string{"npm-a", "npm-strict", "npmjs", "npm-frozen", "npm-v", "npm-v2", "npm-empty", "ghost"}
	reach := func(t *testing.T, mod func(*Options), sub Subject) map[string]ReachResult {
		t.Helper()
		e := newTestEngine(t, reachFixture(), mod)
		idx, err := e.Begin(t.Context(), sub, keys...)
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.ReachOn(t.Context(), idx, sub, keys)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := func(t *testing.T, got map[string]ReachResult, key string, r Reach, denial Outcome) {
		t.Helper()
		if got[key].Reach != r || (r == ReachNone && got[key].Denial != denial) {
			t.Errorf("%s: %s (denial %d), want %s (denial %d)", key, got[key].Reach, got[key].Denial, r, denial)
		}
	}
	t.Run("an unrestricted reader without the populate right of a frozen mirror is partial on the virtual repository over it", func(t *testing.T) {
		got := reach(t, on, aliceSub)
		want(t, got, "npm-a", ReachFull, Allow)
		want(t, got, "npmjs", ReachFull, Allow)
		want(t, got, "npm-v", ReachFull, Allow)
		want(t, got, "npm-v2", ReachNone, DenyHidden) // no gate on npm-v2
	})
	t.Run("the service account that may deploy into the frozen mirror is full on the nested virtual repository", func(t *testing.T) {
		got := reach(t, on, botSub)
		want(t, got, "npm-frozen", ReachFull, Allow)
		want(t, got, "npm-v2", ReachFull, Allow)
	})
	t.Run("a reader without the populate right of a frozen mirror is partial on it and on the virtual repository over it", func(t *testing.T) {
		got := reach(t, on, userSub("user-reader", "reader", "group-readers"))
		want(t, got, "npmjs", ReachFull, Allow)
		want(t, got, "npm-frozen", ReachPartial, Allow)
		want(t, got, "npm-v", ReachFull, Allow)
		want(t, got, "npm-v2", ReachPartial, Allow)
	})
	t.Run("a nested virtual repository whose gate refuses is skipped, so the outer one is partial", func(t *testing.T) {
		got := reach(t, on, userSub("user-v2", "v2", "group-v2"))
		want(t, got, "npm-v", ReachNone, DenyHidden)
		want(t, got, "npm-v2", ReachPartial, Allow)
	})
	t.Run("an exposure rule on the endpoint makes the shape partial at most", func(t *testing.T) {
		got := reach(t, on, onEP(botSub, "customers"))
		want(t, got, "npm-a", ReachPartial, Allow)
		want(t, got, "npm-v", ReachNone, DenyHidden)
		got = reach(t, on, onEP(botSub, "default"))
		want(t, got, "npm-a", ReachFull, Allow)
	})
	t.Run("an anonymous subject is none while the switch is off, with 401", func(t *testing.T) {
		got := reach(t, off, anonSub)
		for _, k := range keys {
			want(t, got, k, ReachNone, DenyUnauthenticated)
		}
	})
	t.Run("an anonymous subject is none on a repository that requires authentication", func(t *testing.T) {
		got := reach(t, on, anonSub)
		want(t, got, "npm-strict", ReachNone, DenyUnauthenticated)
		want(t, got, "npm-a", ReachFull, Allow)
		want(t, got, "npmjs", ReachFull, Allow)
	})
	t.Run("an unknown key is none with the refusal of a missing repository", func(t *testing.T) {
		want(t, reach(t, on, botSub), "ghost", ReachNone, DenyHidden)
		want(t, reach(t, on, anonSub), "ghost", ReachNone, DenyUnauthenticated)
	})
	t.Run("a virtual repository without members reads nothing", func(t *testing.T) {
		want(t, reach(t, on, botSub), "npm-empty", ReachNone, DenyHidden)
	})
	t.Run("the metadata capability alone is never full", func(t *testing.T) {
		got := reach(t, on, userSub(idAuditorDirect, "auditor"))
		want(t, got, "npm-a", ReachPartial, Allow)
		want(t, got, "npm-v", ReachPartial, Allow)
	})
	t.Run("the session cookie never populates, so a remote is partial", func(t *testing.T) {
		want(t, reach(t, on, session(botSub)), "npmjs", ReachPartial, Allow)
	})
	t.Run("a token scope that narrows is partial", func(t *testing.T) {
		sub := tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}})
		want(t, reach(t, on, sub), "npm-a", ReachPartial, Allow)
	})
}
