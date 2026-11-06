package authz

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
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
				rows = append(rows, Coordinate{Repository: k, Package: pkg, Version: v, Kind: KindMetadata, Actions: []Action{Read}})
			}
		}
	}
	for name, sub := range subjects {
		ctx := context.Background()
		idx, err := e.Begin(ctx, sub, keys...)
		if err != nil {
			t.Fatal(err)
		}
		rs, err := e.ReadScopeOn(ctx, idx, sub, "", keys)
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
// caller a finding of a version the decision point refuses, so the claim is checked against DecideOn for every version.
func TestR_VULN_028_ReadScopeVersionIndependence(t *testing.T) {
	e := newTestEngine(t, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	keys := []string{"npm-a", "npm-b"}
	versions := []string{"0.1.0", "1.0.0", "2.1.0", "3.0.0-beta", "10.0.0"}
	cases := []struct {
		name string
		sub  Subject
		free bool
	}{
		{"a grant with patterns on an endpoint that exposes everything", onEP(aliceSub, "default"), true},
		{"a service account on an endpoint that exposes everything", onEP(carolSub, "partners"), true},
		{"an administrator on an endpoint that exposes everything", onEP(adminSub, "default"), true},
		{"a subject without an endpoint", aliceSub, true},
		{"a token scope with a repository list", onEP(tokenScop(botSub, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"npm-a"}, Actions: []string{"read"}}}}), "default"), true},
		{"anonymous on an endpoint that exposes everything", onEP(anonSub, "default"), true},
		{"a grant with patterns on an endpoint with exposure rules", onEP(aliceSub, "customers"), false},
		{"an administrator on an endpoint with exposure rules", onEP(adminSub, "customers"), false},
		{"an endpoint the configuration does not hold", onEP(aliceSub, "nowhere"), false},
	}
	for _, tc := range cases {
		t.Run("R-VULN-028 Q-VULN-81 "+tc.name, func(t *testing.T) {
			ctx := context.Background()
			idx, err := e.Begin(ctx, tc.sub, keys...)
			if err != nil {
				t.Fatal(err)
			}
			rs, err := e.ReadScopeOn(ctx, idx, tc.sub, "", keys)
			if err != nil {
				t.Fatal(err)
			}
			vi, ok := rs.(VersionIndependent)
			if !ok || vi.VersionIndependent() != tc.free {
				t.Fatalf("VersionIndependent = %v (implemented %v), want %v", ok && vi.VersionIndependent(), ok, tc.free)
			}
			if !tc.free {
				return
			}
			allowed := 0
			for _, k := range keys {
				for i := range 40 {
					pkg := []string{"@a/", "@acme/", "@b/", "plain-"}[i%4] + fmt.Sprint(i)
					stands := rs.MayRead(k, pkg, "")
					for _, v := range versions {
						want := e.DecideOn(ctx, idx, tc.sub, Coordinate{Repository: k, Package: pkg, Version: v, Kind: KindMetadata, Actions: []Action{Read}}).Outcome == Allow
						if want != stands || rs.MayRead(k, pkg, v) != stands {
							t.Fatalf("%s %s@%s: DecideOn %v, MayRead %v, the package-level MayRead %v", k, pkg, v, want, rs.MayRead(k, pkg, v), stands)
						}
					}
					if stands {
						allowed++
					}
				}
			}
			t.Logf("%d of 80 packages readable", allowed)
		})
	}
}

// BenchmarkReadScopeMayRead is the cost of one row of the scope of a caller with a package-pattern grant (H-488, V-7b of
// R-VULN-028): a request decides one per version that has a finding.
func BenchmarkReadScopeMayRead(b *testing.B) {
	e := newTestEngine(b, readScopeFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	keys := []string{"npm-b"}
	sub := onEP(aliceSub, "default")
	idx, err := e.Begin(context.Background(), sub, keys...)
	if err != nil {
		b.Fatal(err)
	}
	rs, err := e.ReadScopeOn(context.Background(), idx, sub, "", keys)
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
		_ = rs.MayRead("npm-b", names[i%len(names)], "1.0.0")
	}
}
