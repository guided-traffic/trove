//go:build integration

package authz

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTHZ_031_BlindWritesJournalTheStateTheyReplace: two blind updates
// (If-Match: *) of one policy run at once. The row is locked before the
// before-state is read, so the second write records the first write's result as
// its before-state and not the state both started from.
func TestR_AUTHZ_031_BlindWritesJournalTheStateTheyReplace(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	admin := sub(e.user("sec-alice"))
	e.user("alice")
	e.repo("gen", "local", "generic", "")
	ctx := t.Context()
	input := func(pkg string) authz.PolicyInput {
		in := authz.PolicyInput{
			Name: "p", Resources: authz.Resources{Repositories: []string{"gen"}},
			Entries: []authz.Entry{{Principal: "user:alice", Actions: []string{"read"}}},
		}
		if pkg != "" {
			in.Resources.Packages.Include = []string{pkg}
		}
		return in
	}
	if _, _, err := a.svc.Create(ctx, admin, input("")); err != nil {
		t.Fatal(err)
	}
	type states struct {
		Before struct{ Packages struct{ Include []string } }
		After  struct{ Packages struct{ Include []string } }
	}
	for trial := range 10 {
		var wg sync.WaitGroup
		for _, pkg := range []string{fmt.Sprintf("a%d", trial), fmt.Sprintf("b%d", trial)} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, _, err := a.svc.Update(ctx, admin, "p", errs.AnyRevision, input(pkg)); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		evs := a.rec.ofType(authz.EventPolicyUpdated)
		if len(evs) < 2*(trial+1) {
			t.Fatalf("trial %d: %d events", trial, len(evs))
		}
		var first, second states
		for i, dst := range []*states{&first, &second} {
			if err := json.Unmarshal(evs[2*trial+i].Details, dst); err != nil {
				t.Fatal(err)
			}
		}
		if fmt.Sprint(second.Before.Packages.Include) != fmt.Sprint(first.After.Packages.Include) {
			t.Fatalf("trial %d: the second write recorded the before-state %v, the first wrote %v",
				trial, second.Before.Packages.Include, first.After.Packages.Include)
		}
	}
}
