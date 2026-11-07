package authz

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/qname"
)

// The cost of a project role in the decision point (R-AUTHZ-015 performance target, R-ARCH-033): a decision asks the
// role of the subject in the one project of the repository it decides, so neither a decision nor a listing grows with
// the number of projects the subject is a member of.

// membershipsFixture builds projects m000… with one npm repository each, a user who is a reader of the first n of
// them through its own membership and a developer of the last one, and returns the engine, the subject and the
// repositories, one per project, the last first.
func membershipsFixture(t testing.TB, n int) (*Engine, Subject, []qname.Repo) {
	t.Helper()
	f := newFixture()
	const projects = 60
	var keys []qname.Repo
	for p := range projects {
		key := fmt.Sprintf("m%03d", p)
		id := "project-" + key
		f.project(id, key)
		f.next++
		name := qname.Repo{Project: key, Key: "npm-00"}
		f.s.Repositories = append(f.s.Repositories, repoRow(f.next, name, id))
		keys = append(keys, name)
		switch {
		case p == projects-1:
			f.member(id, idDave, "developer")
		case p < n-1:
			f.member(id, idDave, "reader")
		}
	}
	e := newTestEngine(t, f, func(o *Options) { o.Config.Cache.MaxStaleness = time.Hour })
	keys[0], keys[projects-1] = keys[projects-1], keys[0]
	return e, userSub(idDave, "dave", "group-a", "group-b"), keys
}

// TestR_AUTHZ_015_ARoleDecisionDoesNotGrowWithTheMemberships: a decision through a role allocates the same for a
// subject with 4 memberships and one with 50; the role is read from the repository's project, and the subject's other
// memberships are not derived for it.
func TestR_AUTHZ_015_ARoleDecisionDoesNotGrowWithTheMemberships(t *testing.T) {
	ctx := context.Background()
	allocs := map[int]float64{}
	for _, n := range []int{4, 50} {
		e, sub, keys := membershipsFixture(t, n)
		if got := len(e.MembershipsOn(e.Index(), sub)); got != n {
			t.Fatalf("%d memberships, want %d", got, n)
		}
		c := Coordinate{Repository: keys[0], Package: "p", Kind: KindWrite, Actions: []Action{Deploy}}
		if d := e.Decide(ctx, sub, c); d.Outcome != Allow {
			t.Fatalf("%d memberships: %+v", n, d)
		}
		allocs[n] = testing.AllocsPerRun(100, func() { e.Decide(ctx, sub, c) })
	}
	if allocs[4] != allocs[50] {
		t.Errorf("a decision allocates %v with 4 memberships and %v with 50", allocs[4], allocs[50])
	}
}

// TestR_AUTHZ_015_AListingDerivesTheSubjectOnce: ReadableOn decides every key on one state derived from the subject, as
// a read scope does (readScope.MayRead): a key more costs what one decision on that state costs, whatever the number
// of memberships, and the answers are the decisions'.
func TestR_AUTHZ_015_AListingDerivesTheSubjectOnce(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{4, 50} {
		e, sub, keys := membershipsFixture(t, n)
		idx := e.Index()
		readable, err := e.ReadableOn(ctx, idx, sub, keys)
		if err != nil {
			t.Fatal(err)
		}
		if len(readable) != n {
			t.Fatalf("%d memberships: %d of %d keys readable", n, len(readable), len(keys))
		}
		for _, k := range keys {
			want := e.DecideOn(ctx, idx, sub, Coordinate{Repository: k, Kind: KindMetadata, Actions: []Action{Read}}).Outcome == Allow
			got := false
			for _, r := range readable {
				got = got || r == k
			}
			if got != want {
				t.Errorf("%s: ReadableOn %v, DecideOn %v", k, got, want)
			}
		}
		one := testing.AllocsPerRun(50, func() { _, _ = e.ReadableOn(ctx, idx, sub, keys[:1]) })
		all := testing.AllocsPerRun(50, func() { _, _ = e.ReadableOn(ctx, idx, sub, keys) })
		st := e.newEvalState(idx, sub)
		perKey := testing.AllocsPerRun(50, func() {
			e.evaluateWith(idx, sub, st, Coordinate{Repository: keys[0], Kind: KindMetadata, Actions: []Action{Read}})
		})
		if limit := one + float64(len(keys)-1)*perKey; all > limit {
			t.Errorf("%d memberships: ReadableOn of %d keys allocates %v, one key %v, a decision on a derived state %v: more than %v",
				n, len(keys), all, one, perKey, limit)
		}
	}
}

func BenchmarkRoleDecideMemberships50(b *testing.B) {
	e, sub, keys := membershipsFixture(b, 50)
	ctx := context.Background()
	c := Coordinate{Repository: keys[0], Package: "p", Kind: KindWrite, Actions: []Action{Deploy}}
	b.ReportAllocs()
	for b.Loop() {
		if d := e.Decide(ctx, sub, c); d.Outcome != Allow {
			b.Fatal(d)
		}
	}
}

func BenchmarkReadableOnMemberships50(b *testing.B) {
	e, sub, keys := membershipsFixture(b, 50)
	ctx := context.Background()
	idx := e.Index()
	b.ReportAllocs()
	for b.Loop() {
		if r, err := e.ReadableOn(ctx, idx, sub, keys); err != nil || len(r) != 50 {
			b.Fatal(len(r), err)
		}
	}
}
