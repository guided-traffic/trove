//go:build integration

package authz

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTHZ_028_RemovedGrantIsRefusedOnEveryReplica: a policy is deleted through
// replica A; the replicas that did not write follow the change log alone (there
// is no Valkey in this test), and a request that depended on the policy is
// refused within cluster.epoch_poll_interval plus 1 s.
func TestR_AUTHZ_028_RemovedGrantIsRefusedOnEveryReplica(t *testing.T) {
	e := newEnv(t)
	const poll = 500 * time.Millisecond
	a, b, c := e.newReplica("a", replicaOpts{poll: poll}), e.newReplica("b", replicaOpts{poll: poll}), e.newReplica("c", replicaOpts{poll: poll})
	admin := sub(e.user("sec-alice"))
	bob, devs := e.user("bob"), e.group("developers", false)
	e.repo("npm-internal", "local", "npm", "")
	bobSub := sub(bob, devs)
	ctx := t.Context()
	bound := poll + time.Second

	for trial := 0; trial < 6; trial++ {
		name := fmt.Sprintf("readers-%d", trial)
		_, epoch, err := a.svc.Create(ctx, admin, authz.PolicyInput{
			Name: name, Resources: authz.Resources{Repositories: []string{"npm-internal"}},
			Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []*replica{a, b, c} {
			waitFor(t, "replica "+r.name+" granting", bound, func() bool {
				return r.engine.Decide(ctx, bobSub, read("npm-internal", "x", "")).Outcome == authz.Allow
			})
		}
		if _, err := a.svc.Delete(ctx, admin, name, authz.AnyVersion); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		var wg sync.WaitGroup
		for _, r := range []*replica{b, c} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				waitFor(t, "replica "+r.name+" refusing the removed grant", bound, func() bool {
					return r.engine.Decide(ctx, bobSub, read("npm-internal", "x", "")).Outcome == authz.DenyHidden
				})
			}()
		}
		wg.Wait()
		if took := time.Since(start); took > bound {
			t.Errorf("trial %d: refused after %s, bound %s", trial, took, bound)
		}
		_ = epoch
	}
}

// TestR_AUTHZ_028_ReusedEpoch: a failover that lost a committed transaction can
// be followed by writes that raise the epoch to the same number again. The
// replica that built from the lost history must not mistake it for the current
// one (the comparison includes changed_at), and must flush.
func TestR_AUTHZ_028_ReusedEpoch(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{poll: 30 * time.Second, staleness: 120 * time.Second})
	b := e.newReplica("b", replicaOpts{poll: 30 * time.Second, staleness: 120 * time.Second})
	admin := sub(e.user("sec-alice"))
	alice, carol, devs, web := e.user("alice"), e.user("carol"), e.group("developers", false), e.group("team-web", false)
	e.repo("npm-internal", "local", "npm", "")
	ctx := t.Context()
	aliceSub, carolSub := sub(alice, devs), sub(carol, web)

	if _, _, err := a.svc.Create(ctx, admin, authz.PolicyInput{
		Name: "alice-reads", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
		Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// The grant to Carol commits through A as epoch e, and B builds from it.
	_, eEpoch, err := a.svc.Create(ctx, admin, authz.PolicyInput{
		Name: "carol-reads", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
		Entries: []authz.Entry{{Principal: "group:team-web", Actions: []string{"read"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	carolSub.MinEpoch = eEpoch
	if d := b.engine.Decide(ctx, carolSub, read("npm-internal", "x", "")); d.Outcome != authz.Allow {
		t.Fatalf("carol before the failover: %+v", d)
	}
	if err := b.changes.PollNow(ctx); err != nil { // B applies epoch e
		t.Fatal(err)
	}

	// As a failover that lost the commit would: the grant and its change row are
	// gone and the epoch row is back at e-1 with its earlier changed_at.
	e.exec("DELETE FROM authz_policy WHERE name = 'carol-reads'")
	e.exec("DELETE FROM cluster_change WHERE area = 'authz' AND epoch = $1", eEpoch)
	e.exec("UPDATE cluster_epoch SET epoch = $1, changed_at = changed_at - interval '1 minute' WHERE area = 'authz'", eEpoch-1)
	// A removal of one of Alice's grants commits through A as epoch e again.
	if _, err := a.svc.Delete(ctx, admin, "alice-reads", authz.AnyVersion); err != nil {
		t.Fatal(err)
	}
	if got := e.authzEpoch(); got != eEpoch {
		t.Fatalf("the test did not reuse the epoch: %d, want %d", got, eEpoch)
	}

	flushes := func() float64 { return rebuilds(b, "flush") }
	before := flushes()
	if err := b.changes.PollNow(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B installing a new index after the flush", 3*time.Second, func() bool { return flushes() > before })
	waitFor(t, "B refusing both the lost and the removed grant", 3*time.Second, func() bool {
		return b.engine.Decide(ctx, carolSub, read("npm-internal", "x", "")).Outcome == authz.DenyHidden &&
			b.engine.Decide(ctx, aliceSub, read("npm-internal", "x", "")).Outcome == authz.DenyHidden
	})
}

// TestR_AUTHZ_028_ConsistentBuild: policies are replaced with their selectors
// and entries together while another replica rebuilds continuously. Every index
// it installs grants exactly what one database state grants.
func TestR_AUTHZ_028_ConsistentBuild(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	b := e.newReplica("b", replicaOpts{})
	admin := sub(e.user("sec-alice"))
	x, y := e.user("x"), e.user("y")
	e.repo("repo-a", "local", "generic", "")
	e.repo("repo-b", "local", "generic", "")
	ctx := t.Context()
	states := []authz.PolicyInput{
		{Name: "flip", Resources: authz.Resources{Repositories: []string{"repo-a"}}, Entries: []authz.Entry{{Principal: "user:x", Actions: []string{"read"}}}},
		{Name: "flip", Resources: authz.Resources{Repositories: []string{"repo-b"}}, Entries: []authz.Entry{{Principal: "user:y", Actions: []string{"read"}}}},
	}
	if _, _, err := a.svc.Create(ctx, admin, states[0]); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	var checks, torn atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			if err := b.engine.Refresh(ctx); err != nil {
				t.Error(err)
				return
			}
			idx := b.engine.Index()
			ask := func(p person, repo string) bool {
				return b.engine.DecideOn(ctx, idx, sub(p), read(repo, "", "f")).Outcome == authz.Allow
			}
			xa, xb, ya, yb := ask(x, "repo-a"), ask(x, "repo-b"), ask(y, "repo-a"), ask(y, "repo-b")
			checks.Add(1)
			// a state of the database grants x on repo-a only, or y on repo-b only
			if onlyX, onlyY := xa && !xb && !ya && !yb, !xa && !xb && !ya && yb; !onlyX && !onlyY {
				torn.Add(1)
				t.Errorf("an index grants x@a=%v x@b=%v y@a=%v y@b=%v at epoch %d", xa, xb, ya, yb, idx.Epoch())
			}
		}
	}()
	for i := 1; i <= 40; i++ {
		if _, _, err := a.svc.Update(ctx, admin, "flip", authz.AnyVersion, states[i%2]); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	t.Logf("%d indexes built while 40 policies replaced their selectors and entries together; %d torn", checks.Load(), torn.Load())
	if checks.Load() < 5 {
		t.Errorf("only %d builds ran", checks.Load())
	}
}

// rebuilds reads trove_authz_index_rebuilds_total{trigger} of a replica.
func rebuilds(r *replica, trigger string) float64 {
	mfs, err := r.reg.Gather()
	if err != nil {
		return 0
	}
	for _, mf := range mfs {
		if mf.GetName() != "trove_authz_index_rebuilds_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "trigger" && l.GetValue() == trigger {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestE1_12_OwnPermissionChangeOnEveryReplica: three replicas, Valkey stopped,
// propagation slowed to a poll every 30 s. Twenty parallel checks carrying
// Trove-Authz-Min-Epoch follow a new grant and then its removal on every
// replica.
func TestE1_12_OwnPermissionChangeOnEveryReplica(t *testing.T) {
	e := newEnv(t)
	slow := replicaOpts{poll: 30 * time.Second, staleness: 120 * time.Second}
	reps := []*replica{e.newReplica("a", slow), e.newReplica("b", slow), e.newReplica("c", slow)}
	a := reps[0]
	admin := sub(e.user("sec-alice"))
	dev, mobile := e.user("mobile-dev"), e.group("team-mobile", false)
	e.repo("files-internal", "local", "generic", "")
	ctx := t.Context()
	devSub := sub(dev, mobile)

	check := func(minEpoch int64, i int) (authz.Decision, string) {
		r := reps[i%len(reps)]
		s := devSub
		s.MinEpoch = minEpoch
		return r.engine.Decide(ctx, s, write("files-internal", "", fmt.Sprintf("app-%d.zip", i), authz.Deploy)), r.name
	}
	twenty := func(minEpoch int64) (decisions []authz.Decision, replicas map[string]int) {
		decisions = make([]authz.Decision, 20)
		replicas = map[string]int{}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d, name := check(minEpoch, i)
				mu.Lock()
				decisions[i] = d
				replicas[name]++
				mu.Unlock()
			}()
		}
		wg.Wait()
		return decisions, replicas
	}

	// Before the grant every replica refuses.
	if ds, _ := twenty(0); ds[0].Outcome != authz.DenyVisible {
		t.Fatalf("before the grant: %+v", ds[0])
	}

	_, n, err := a.svc.Create(ctx, admin, authz.PolicyInput{
		Name: "mobile-deploy", Resources: authz.Resources{Repositories: []string{"files-internal"}},
		Entries: []authz.Entry{{Principal: "group:team-mobile", Actions: []string{"deploy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reps[1].engine.Epoch() >= n || reps[2].engine.Epoch() >= n {
		t.Fatalf("the other replicas had applied epoch %d already (b %d, c %d); the test needs the slowed propagation", n, reps[1].engine.Epoch(), reps[2].engine.Epoch())
	}
	ds, seen := twenty(n)
	for i, d := range ds {
		if d.Outcome != authz.Allow || d.Epoch < n {
			t.Errorf("upload %d after the grant: %+v", i, d)
		}
	}
	if len(seen) != 3 {
		t.Errorf("the checks reached %v, want all three replicas", seen)
	}

	m, err := a.svc.Delete(ctx, admin, "mobile-deploy", authz.AnyVersion)
	if err != nil {
		t.Fatal(err)
	}
	if reps[1].engine.Epoch() >= m {
		t.Fatalf("replica b had applied the removal already")
	}
	ds, seen = twenty(m)
	for i, d := range ds {
		if d.Outcome != authz.DenyVisible || d.Missing != authz.Deploy || d.Epoch < m {
			t.Errorf("upload %d after the removal: %+v", i, d)
		}
	}
	if len(seen) != 3 {
		t.Errorf("the checks reached %v, want all three replicas", seen)
	}

	t.Run("R-AUTHZ-032 an epoch the database never reached answers 412 quickly", func(t *testing.T) {
		s := devSub
		s.MinEpoch = m + 1000
		start := time.Now()
		d := reps[1].engine.Decide(ctx, s, write("files-internal", "", "x", authz.Deploy))
		if d.Outcome != authz.DenyEpochUnknown {
			t.Fatalf("%+v", d)
		}
		problem(t, d.Err(), 412, errs.CodeAuthzEpochUnknown)
		if took := time.Since(start); took > 500*time.Millisecond {
			t.Errorf("answered after %s", took)
		}
	})

	t.Run("R-AUTHZ-032 a request without a credential ignores the header", func(t *testing.T) {
		s := authz.Subject{Anonymous: true, MinEpoch: m + 1000}
		if d := reps[1].engine.Decide(ctx, s, write("files-internal", "", "x", authz.Deploy)); d.Outcome != authz.DenyUnauthenticated {
			t.Fatalf("%+v", d)
		}
	})
}
