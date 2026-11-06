package authz

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
)

// TestR_VULN_019_ReadScopeSharedStateDecidesLikeTheDecisionPoint: a listing of findings decides tens of thousands of rows with
// one scope (H-488), and the scope derives the subject's state once. Many rows decided in one order, then in another, and
// from several goroutines at once, answer what DecideOn answers for each row on its own: no row's answer leaks into the next.
func TestR_VULN_019_ReadScopeSharedStateDecidesLikeTheDecisionPoint(t *testing.T) {
	e := newTestEngine(t, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	keys := []string{"npm-a", "npm-b"}
	subjects := map[string]Subject{
		"alice":    onEP(aliceSub, "default"),
		"bot":      onEP(botSub, "customers"),
		"carol":    onEP(carolSub, "default"),
		"anon":     onEP(anonSub, "default"),
		"admin":    onEP(adminSub, "customers"),
		"scoped":   onEP(tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}}), "default"),
		"alice-nt": aliceSub,
	}
	var rows []Coordinate
	for _, k := range keys {
		for i := range 60 {
			pkg := []string{"@a/", "@acme/", "@b/", "plain-"}[i%4] + fmt.Sprint(i)
			for _, v := range []string{"1.0.0", "2.1.0", "3.0.0-beta"} {
				rows = append(rows, Coordinate{Repository: qr(k), Package: pkg, Version: v, Kind: KindMetadata, Actions: []Action{Read}})
			}
		}
	}
	for name, sub := range subjects {
		ctx := context.Background()
		idx, err := e.Begin(ctx, sub, qrs(keys)...)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := e.ReadScopeOn(ctx, idx, sub, qr(""), qrs(keys))
		if err != nil {
			t.Fatal(err)
		}
		want := make([]bool, len(rows))
		allowed := 0
		for i, c := range rows {
			want[i] = e.DecideOn(ctx, idx, sub, c).Outcome == Allow
			if want[i] {
				allowed++
			}
		}
		check := func(order func(int) int) {
			for n := range rows {
				i := order(n)
				if got := rs.MayRead(rows[i].Repository, rows[i].Package, rows[i].Version); got != want[i] {
					t.Errorf("%s: MayRead(%s %s@%s) = %v, DecideOn says %v", name, rows[i].Repository, rows[i].Package, rows[i].Version, got, want[i])
				}
			}
		}
		check(func(n int) int { return n })
		check(func(n int) int { return len(rows) - 1 - n })
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				check(func(n int) int { return (n * 7) % len(rows) })
			}()
		}
		wg.Wait()
		t.Logf("%s: %d of %d rows allowed", name, allowed, len(rows))
	}
}

// TestR_VULN_028_ReadScopeVersionIndependence: a scope that says its decisions do not depend on the version
// (VersionIndependent) answers the same for every version of a package, and the one input that reads a version, an endpoint
// with exposure rules, makes it say it does. A listing decides such a package once (Q-VULN-81); a wrong "true" would show a
// caller a finding of a version the decision point refuses, so the claim is checked against DecideOn for every version of a
// package. The subjects discriminate: each decides some packages and refuses others, so that a decision which ignored its
// inputs would fail. For a scope that says "dependent" the test shows a package whose versions are decided differently, so that
// the false is needed and not a habit.
func TestR_VULN_028_ReadScopeVersionIndependence(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine(t, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	meta := newTestEngine(t, metadataFixture(), nil)
	ciE, _ := ciEngine(t)
	versions := []string{"0.1.0", "1.0.0", "1.9.0", "2.1.0", "3.0.0-beta", "10.0.0"}
	readA := func(inc ...string) scope.Scope {
		return scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a", "npm-b"}, Packages: scope.Patterns{Include: inc}, Actions: []string{"read"}}}}
	}
	names := func(prefixes ...string) []string {
		var out []string
		for i := range 40 {
			out = append(out, prefixes[i%len(prefixes)]+fmt.Sprint(i))
		}
		return out
	}
	std := names("@a/", "@acme/", "@b/", "plain-")
	cases := []struct {
		name string
		e    *Engine
		sub  Subject
		keys []string
		pkgs []string
		free bool
		// mixed says the subject reads some of the packages and not others (free), or that some package has versions that are
		// decided differently (not free); false for a subject that reads nothing in the fixture and shows neither.
		mixed bool
	}{
		{"a grant with package patterns on an endpoint that exposes everything", e, onEP(aliceSub, "default"), []string{"npm-a", "npm-b"}, std, true, true},
		{"a service account with an unrestricted grant on an endpoint that exposes everything", e, onEP(botSub, "partners"), []string{"npm-a", "npm-b"}, std, true, false},
		{"a user without a grant reads nothing", e, onEP(carolSub, "default"), []string{"npm-a", "npm-b"}, std, true, false},
		{"an administrator on an endpoint that exposes everything", e, onEP(adminSub, "default"), []string{"npm-a", "npm-b"}, std, true, false},
		{"a subject without an endpoint", e, aliceSub, []string{"npm-a", "npm-b"}, std, true, true},
		{"a token scope with a repository list", e, onEP(tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}}), "default"), []string{"npm-a", "npm-b"}, std, true, true},
		{"a token scope with package patterns", e, onEP(tokenScop(botSub, readA("@a/*", "plain-*")), "default"), []string{"npm-a", "npm-b"}, std, true, true},
		{
			"a CI identity with an inline grant with package patterns and no policy", ciE, ciInlineOnly(),
			[]string{"npm-internal"},
			names("@acme-billing/", "@acme-shipping/", "left-pad-"), true, true,
		},
		{"the holder of content:metadata-read under a token scope with package patterns", meta, tokenScop(userSub(idAuditorDirect, "auditor"), scope.Scope{
			System:       []string{string(ContentMetadataRead)},
			Repositories: []scope.Grant{{Repositories: []string{"npm-internal"}, Packages: scope.Patterns{Include: []string{"left-*"}}, Actions: []string{"read"}}},
		}), []string{"npm-internal"}, names("left-", "right-"), true, true},
		{"anonymous on an endpoint that exposes everything", e, onEP(anonSub, "default"), []string{"npm-a", "npm-b"}, std, true, false},
		{"a service account on an endpoint with exposure rules", e, onEP(botSub, "customers"), []string{"npm-a", "npm-b"}, std, false, true},
		{"an administrator on an endpoint with exposure rules", e, onEP(adminSub, "customers"), []string{"npm-a", "npm-b"}, std, false, true},
		{"a grant without a rule of the endpoint on an endpoint with exposure rules", e, onEP(aliceSub, "customers"), []string{"npm-a", "npm-b"}, std, false, false},
		{"an endpoint the configuration does not hold", e, onEP(botSub, "nowhere"), []string{"npm-a", "npm-b"}, std, false, false},
	}
	for _, tc := range cases {
		t.Run("R-VULN-028 Q-VULN-81 "+tc.name, func(t *testing.T) {
			idx, err := tc.e.Begin(ctx, tc.sub, qrs(tc.keys)...)
			if err != nil {
				t.Fatal(err)
			}
			rs, err := tc.e.ReadScopeOn(ctx, idx, tc.sub, qr(""), qrs(tc.keys))
			if err != nil {
				t.Fatal(err)
			}
			vi, ok := rs.(VersionIndependent)
			if !ok || vi.VersionIndependent() != tc.free {
				t.Fatalf("VersionIndependent = %v (implemented %v), want %v", ok && vi.VersionIndependent(), ok, tc.free)
			}
			allowed, refused, differing := 0, 0, 0
			for _, k := range tc.keys {
				for _, pkg := range tc.pkgs {
					stands := rs.MayRead(qr(k), pkg, "")
					if stands {
						allowed++
					} else {
						refused++
					}
					for _, v := range versions {
						want := tc.e.DecideOn(ctx, idx, tc.sub, Coordinate{Repository: qr(k), Package: pkg, Version: v, Kind: KindMetadata, Actions: []Action{Read}}).Outcome == Allow
						if rs.MayRead(qr(k), pkg, v) != want {
							t.Fatalf("%s %s@%s: DecideOn %v, MayRead %v", k, pkg, v, want, rs.MayRead(qr(k), pkg, v))
						}
						if want != stands {
							differing++
							if tc.free {
								t.Fatalf("%s %s@%s: DecideOn %v, the package-level MayRead %v: the scope says it does not look at the version", k, pkg, v, want, stands)
							}
						}
					}
				}
			}
			t.Logf("%d packages readable, %d refused, %d (package, version) pairs decided otherwise than the package", allowed, refused, differing)
			switch {
			case tc.free && tc.mixed && (allowed == 0 || refused == 0):
				t.Errorf("the subject does not discriminate: %d readable, %d refused", allowed, refused)
			case !tc.free && tc.mixed && differing == 0:
				t.Error("no package has versions that are decided differently: the false is not shown to be needed")
			}
		})
	}
}

// BenchmarkReadScopeMayRead is the cost of one row of the scope of a caller with a package-pattern grant (H-488, V-7b of
// R-VULN-028): a request decides one per version that has a finding.
func BenchmarkReadScopeMayRead(b *testing.B) {
	e := newTestEngine(b, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	keys := []string{"npm-b"}
	sub := onEP(aliceSub, "default")
	idx, err := e.Begin(context.Background(), sub, qrs(keys)...)
	if err != nil {
		b.Fatal(err)
	}
	rs, err := e.ReadScopeOn(context.Background(), idx, sub, qr(""), qrs(keys))
	if err != nil {
		b.Fatal(err)
	}
	names := make([]string, 1000)
	for i := range names {
		names[i] = fmt.Sprintf("pkg-%d", i)
		if i%10 == 0 {
			names[i] = fmt.Sprintf("@a/p%d", i)
		}
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_ = rs.MayRead(qr("npm-b"), names[i%len(names)], "1.0.0")
	}
}

// ciInlineOnly is a CI identity whose only right is an inline grant on npm-internal for the packages of @acme-billing: no policy
// names its mapping.
func ciInlineOnly() Subject {
	sub := ciSubject(frozenOf("101", systemTier("gitlab-internal/solo", nil,
		inline([]string{"npm-internal"}, []string{"@acme-billing/*"}, "", claims.Read))))
	sub.ID = "solo-mapping"
	return sub
}
