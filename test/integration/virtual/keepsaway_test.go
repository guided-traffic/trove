//go:build integration

package virtual

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/repository"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

// keeps asks a replica's routing whether the configuration keeps a term away from a member of the
// virtual repository "all".
func keeps(t *testing.T, r *replica, member, term string, direct bool) bool {
	t.Helper()
	var via *repository.Repository
	if !direct {
		v := r.repo("all")
		via = &v
	}
	away, err := r.routing.KeepsAway(context.Background(), via, r.repo(member), term)
	if err != nil {
		t.Fatalf("KeepsAway(%s, %s) on %s: %v", member, term, r.name, err)
	}
	return away
}

// TestR_NUGET_016_AClaimWithholdsATermOnEveryReplica: the withheld-query rule reads the claim index
// the change log keeps current on every replica, with or without a message bus (the replicas here
// have none), and answers from memory.
func TestR_NUGET_016_AClaimWithholdsATermOnEveryReplica(t *testing.T) {
	e := newEnv(t)
	remote := vt.NewRemote()
	opts := replicaOpts{grants: standardGrants(), remote: remote}
	a, b := e.newReplica("virt-a", opts), e.newReplica("virt-b", opts)
	topology(a, remote)
	settle(t, b)

	for _, r := range []*replica{a, b} {
		if keeps(t, r, "upstream", "partner-sdk", false) || keeps(t, r, "upstream", "partner", false) {
			t.Fatalf("%s: a term is kept away before any claim exists", r.name)
		}
	}
	if _, err := a.svc.PutClaims(context.Background(), caller, qr("team-local"), repository.MatchAny(),
		repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "partner-*"}}}, allowGlobal); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the claim to reach replica B", 5*time.Second, func() bool { return keeps(t, b, "upstream", "partner-sdk", false) })

	for _, r := range []*replica{a, b} {
		for _, term := range []string{"partner-sdk", "partner"} {
			if !keeps(t, r, "upstream", term, false) {
				t.Errorf("%s: %q is not kept away from the remote member", r.name, term)
			}
			if keeps(t, r, "team-local", term, false) {
				t.Errorf("%s: %q is kept away from the member that claims it", r.name, term)
			}
		}
		if keeps(t, r, "upstream", "left-pad", false) {
			t.Errorf("%s: an unclaimed term is kept away", r.name)
		}
	}
	t.Run("a members claim does not keep a name away from a remote addressed directly", func(t *testing.T) {
		if keeps(t, b, "upstream", "partner-sdk", true) {
			t.Fatal("a claim of scope members reached a request that has no virtual repository")
		}
	})
	t.Run("a global claim does, while the remote refusal is on", func(t *testing.T) {
		if _, err := a.svc.PutClaims(context.Background(), caller, qr("team-local"), repository.MatchAny(),
			repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "partner-*", Scope: repository.ScopeGlobal}}}, allowGlobal); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the global claim to reach replica B", 5*time.Second, func() bool { return keeps(t, b, "upstream", "partner", true) })
		if keeps(t, b, "team-local", "partner", true) {
			t.Fatal("a claim keeps a name away from a local repository addressed directly")
		}
	})
}
