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
