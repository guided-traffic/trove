//go:build integration

package virtual

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

func allowGlobal(context.Context) error { return nil }

// topology creates, through replica a, the repositories of most tests: a local team-local (no
// automatic claims, so that a publish changes no member set), a remote upstream and the virtual
// repository all over both with team-local as the deploy target.
func topology(a *replica, remote *vt.Remote) {
	a.create("team-local", "local", `"local":{"autoClaim":false}`)
	a.create("upstream", "remote", "")
	a.create("all", "virtual", `"virtual":{"members":["team-local","upstream"],"deployTarget":"team-local"}`)
	remote.Register(a.repo("upstream"))
}

// settle brings the other replicas up to the changes of the setup, so that a repository change the poll
// would apply late cannot flush a merged entry the test is about to count.
func settle(t *testing.T, rs ...*replica) {
	t.Helper()
	for _, r := range rs {
		if err := r.changes.PollNow(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func standardGrants() grants {
	everything := []string{"all", "team-local", "upstream", "all2", "sandbox", "old-local"}
	return grants{
		reads:  map[string][]string{"alice": everything, "bob": {"all", "team-local"}, "ci-bot": {"all", "team-local", "upstream"}, "lead": everything},
		fills:  map[string]bool{"alice": true, "lead": true, "ci-bot": true},
		manage: map[string]bool{"lead": true},
	}
}

func upstreamDoc(remote *vt.Remote, pkg string, versions ...string) {
	remote.SetDoc("upstream", pkg, versions, time.Now().Add(time.Hour), true)
}

func TestR_ARCH_015_LocalPublishIsVisibleThroughVirtualOnEveryReplica(t *testing.T) {
	e := newEnv(t)
	remote := vt.NewRemote()
	opts := replicaOpts{grants: standardGrants(), remote: remote}
	a, b, c := e.newReplica("virt-a", opts), e.newReplica("virt-b", opts), e.newReplica("virt-c", opts)
	topology(a, remote)
	settle(t, b, c)
	alice := subject("alice")

	t.Run("R-REPO-020 a document cached on replica B shows a local publish to replica A at once, with no Valkey", func(t *testing.T) {
		// lodash is served only by the remote member and cached on B.
		upstreamDoc(remote, "lodash", "4.17.21")
		m, err := b.metadata("all", alice, "lodash")
		if err != nil || len(vt.Versions(m.Bytes)) != 1 || b.mergedEntries() != 1 {
			t.Fatalf("merged %s err %v entries %d", m.Bytes, err, b.mergedEntries())
		}
		a.publish("team-local", "lodash", "99.0.0")
		m, err = b.metadata("all", alice, "lodash")
		if err != nil || vt.Versions(m.Bytes)["99.0.0"] != "team-local" || vt.Versions(m.Bytes)["4.17.21"] != "upstream" {
			t.Fatalf("after the publish on A: %s (%v)", m.Bytes, err)
		}
	})

	t.Run("R-ARCH-015 R-ARCH-012 publish on A, view through the virtual repository on B and C, 50 iterations, no sleep", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			v := fmt.Sprintf("1.0.%d", i)
			a.publish("team-local", "@acme/lib", v)
			for _, r := range []*replica{b, c} {
				m, err := r.metadata("all", alice, "@acme/lib")
				if err != nil || vt.Versions(m.Bytes)[v] != "team-local" {
					t.Fatalf("iteration %d: %s lists %s (%v)", i, r.name, m.Bytes, err)
				}
			}
		}
	})

	t.Run("R-REPO-020 a hit is validated with one RevSeqs query and merges nothing", func(t *testing.T) {
		before := b.merger.Merges.Load()
		for range 3 {
			if _, err := b.metadata("all", alice, "@acme/lib"); err != nil {
				t.Fatal(err)
			}
		}
		if got := b.merger.Merges.Load() - before; got != 0 {
			t.Fatalf("%d merges for three requests of an unchanged package", got)
		}
	})
}

func TestR_REPO_020_CallersWithDifferentReadableMembers(t *testing.T) {
	for name, order := range map[string][]string{"alice first": {"alice", "bob"}, "bob first": {"bob", "alice"}} {
		t.Run("R-REPO-020 R-AUTHZ-009 rule 4 "+name, func(t *testing.T) {
			e := newEnv(t)
			remote := vt.NewRemote()
			a := e.newReplica("virt-a", replicaOpts{grants: standardGrants(), remote: remote})
			topology(a, remote)
			a.publish("team-local", "lodash", "1.0.0")
			upstreamDoc(remote, "lodash", "4.17.21")
			got := map[string]map[string]string{}
			for _, who := range order {
				m, err := a.metadata("all", subject(who), "lodash")
				if err != nil {
					t.Fatal(err)
				}
				got[who] = vt.Versions(m.Bytes)
			}
			if len(got["alice"]) != 2 || got["alice"]["4.17.21"] != "upstream" {
				t.Fatalf("alice = %v", got["alice"])
			}
			if len(got["bob"]) != 1 || got["bob"]["1.0.0"] != "team-local" {
				t.Fatalf("bob read npm-all and team-local only and was shown %v", got["bob"])
			}
		})
	}
}

func TestR_REPO_023_ClaimsReachTheMergedEntriesOfEveryReplica(t *testing.T) {
	e := newEnv(t)
	remote := vt.NewRemote()
	opts := replicaOpts{grants: standardGrants(), remote: remote}
	a, b := e.newReplica("virt-a", opts), e.newReplica("virt-b", opts)
	topology(a, remote)
	settle(t, b)
	alice := subject("alice")
	a.publish("team-local", "@acme/util", "1.0.0")
	upstreamDoc(remote, "@acme/util", "9.9.9")

	m, err := b.metadata("all", alice, "@acme/util")
	if err != nil || len(vt.Versions(m.Bytes)) != 2 || b.mergedEntries() != 1 {
		t.Fatalf("before the claim: %s (%v) entries %d", m.Bytes, err, b.mergedEntries())
	}
	if _, err := a.svc.PutClaims(context.Background(), caller, "team-local", repository.MatchAny(),
		repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*"}}}, allowGlobal); err != nil {
		t.Fatal(err)
	}
	// The area claims reaches the routing and the merged entries of B within the change-log bound,
	// whatever Valkey does. (The entry may also go earlier, with a repository change the poll applies
	// late; that is why the test waits for the claim itself.)
	waitFor(t, "the claim and the loss of the merged entry on replica B", 5*time.Second, func() bool {
		rt, err := b.routing.Route(context.Background(), b.repo("all"), repository.RouteRequest{Name: "@acme/util", Path: "@acme/util"})
		return err == nil && rt.Decision.Kind == repository.DecidedByClaim && b.mergedEntries() == 0
	})
	remoteCalls := remote.Count("metadata:")
	m, err = b.metadata("all", alice, "@acme/util")
	if err != nil || len(vt.Versions(m.Bytes)) != 1 || vt.Versions(m.Bytes)["1.0.0"] != "team-local" {
		t.Fatalf("after the claim: %s (%v)", m.Bytes, err)
	}
	if remote.Count("metadata:") != remoteCalls {
		t.Fatalf("a name a local member claims reached the remote member: %v", remote.Calls())
	}
}

func TestR_REPO_019_ExplanationAgainstTheRealRouting(t *testing.T) {
	e := newEnv(t)
	remote := vt.NewRemote()
	a := e.newReplica("virt-a", replicaOpts{grants: standardGrants(), remote: remote})
	topology(a, remote)
	a.create("old-local", "local", `"disabled":true,"local":{"autoClaim":false}`)
	a.create("all2", "virtual", `"virtual":{"members":["old-local","team-local","upstream"]}`)
	a.publish("team-local", "@acme/util", "1.0.0")
	upstreamDoc(remote, "@acme/util", "9.9.9")
	upstreamDoc(remote, "lodash", "4.17.21")
	if _, err := a.svc.PutClaims(context.Background(), caller, "team-local", repository.MatchAny(),
		repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*"}}}, allowGlobal); err != nil {
		t.Fatal(err)
	}
	explain := func(v, name, who string) virtual.Explanation {
		t.Helper()
		ex, err := a.engine.Explain(context.Background(), virtual.Caller{Subject: subject(who)}, a.repo(v),
			virtual.Descriptor{Format: vt.Format, Path: name, Kind: "metadata", Package: name})
		if err != nil {
			t.Fatal(err)
		}
		return ex
	}
	decisions := func(ex virtual.Explanation) map[string]string {
		out := map[string]string{}
		for _, m := range ex.Members {
			out[m.Key] = m.Decision
		}
		return out
	}

	t.Run("R-REPO-019 R-REPO-023 a claim of a member leaves the remote member out of the claimed names", func(t *testing.T) {
		var ex virtual.Explanation
		waitFor(t, "the claim to reach the routing", 5*time.Second, func() bool {
			ex = explain("all", "@acme/util", "alice")
			return ex.DecidedBy != nil
		})
		if ex.DecidedBy.Claim != "@acme/*" || !slices.Equal(ex.DecidedBy.ClaimedBy, []string{"team-local"}) {
			t.Fatalf("decidedBy = %+v", ex.DecidedBy)
		}
		if d := decisions(ex); d["team-local"] != "hit" || d["upstream"] != "claimed-elsewhere" {
			t.Fatalf("decisions = %v", d)
		}
		if got := decisions(explain("all", "lodash", "alice")); got["upstream"] != "hit" || got["team-local"] != "miss" {
			t.Fatalf("an unclaimed name: %v", got)
		}
	})

	t.Run("R-REPO-019 a disabled member is named with its reason, in the order of the member list", func(t *testing.T) {
		ex := explain("all2", "lodash", "alice")
		if len(ex.Members) != 3 || ex.Members[0].Key != "old-local" || ex.Members[0].Decision != "disabled" {
			t.Fatalf("members = %+v", ex.Members)
		}
	})

	t.Run("R-REPO-019 members the caller cannot read are a count: bob reads two of three", func(t *testing.T) {
		ex := explain("all", "lodash", "bob")
		if ex.UnreadableMembers != 1 || len(ex.Members) != 1 {
			t.Fatalf("explanation = %+v", ex)
		}
	})

	t.Run("R-REPO-019 the explanation contacted no upstream", func(t *testing.T) {
		if remote.Count("probe:") != 0 || remote.Count("metadata:") != 0 {
			t.Fatalf("calls %v", remote.Calls())
		}
	})

	t.Run("R-REPO-021 the deploy target is the member the document names; without one the repository is read-only", func(t *testing.T) {
		tgt, err := a.engine.DeployTarget(a.repo("all"))
		if err != nil || tgt.Key != "team-local" {
			t.Fatalf("target %q err %v", tgt.Key, err)
		}
		_, err = a.engine.DeployTarget(a.repo("all2"))
		if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeNoDeployTarget || ee.Kind != errs.MethodNotAllowed {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_REPO_048_ShadowingCheckAgainstTheRealExposureSet(t *testing.T) {
	e := newEnv(t)
	remote := vt.NewRemote()
	a := e.newReplica("virt-a", replicaOpts{grants: standardGrants(), remote: remote})
	topology(a, remote)
	a.create("all2", "virtual", `"virtual":{"members":["team-local","upstream"]}`)
	a.create("sandbox", "local", `"local":{"autoClaim":false}`)
	remote.OnProbe = func(virtual.RemoteCall) {
		if n := a.runner.open.Load(); n != 0 {
			t.Errorf("%d transactions were open while a probe ran (R-REPO-048: none may be)", n)
		}
	}
	check := func(who, local, name string) virtual.ShadowOutcome {
		t.Helper()
		out, err := a.engine.ShadowCheck(context.Background(), virtual.Caller{Subject: subject(who)}, a.repo(local), name, name)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	probes := func() int { return remote.Count("probe:") }

	t.Run("R-REPO-048 the remote member serves the name: a deploy holder is refused, a manage holder proceeds; one probe per publish", func(t *testing.T) {
		remote.SetProbe("upstream", virtual.ProbeResult{Outcome: virtual.Served})
		out := check("ci-bot", "team-local", "lodash")
		if out.Proceed || out.Code != errs.CodeShadowsRemotePackage {
			t.Fatalf("ci-bot: %+v", out)
		}
		// The remote member is in two virtual repositories, and ci-bot reads one of them.
		if probes() != 1 || !slices.Equal(out.Virtuals, []string{"all"}) {
			t.Fatalf("probes %d virtual repositories %v", probes(), out.Virtuals)
		}
		out = check("lead", "team-local", "lodash")
		if !out.Proceed || out.Outcome != virtual.OutcomeShadowingApproved || len(out.Shadowed) != 2 || probes() != 2 {
			t.Fatalf("lead: %+v probes %d", out, probes())
		}
	})

	t.Run("R-REPO-048 a local repository in no virtual repository is not checked and nothing is requested", func(t *testing.T) {
		before := probes()
		out := check("ci-bot", "sandbox", "react")
		if !out.Proceed || out.Outcome != virtual.OutcomeNoExposure || probes() != before {
			t.Fatalf("out %+v probes %d", out, probes()-before)
		}
	})

	t.Run("R-REPO-048 no negative entry plays a part: a name the upstream does not know is published after one probe", func(t *testing.T) {
		remote.SetProbe("upstream", virtual.ProbeResult{Outcome: virtual.NotServed})
		remote.SetState("upstream", virtual.EntryState{Negative: true})
		before := probes()
		out := check("ci-bot", "team-local", "acme-brand-new")
		if !out.Proceed || out.Outcome != virtual.OutcomeClear || probes()-before != 1 {
			t.Fatalf("out %+v probes %d", out, probes()-before)
		}
		remote.SetProbe("upstream", virtual.ProbeResult{Outcome: virtual.Served})
		if out = check("ci-bot", "team-local", "acme-brand-new-3"); out.Proceed {
			t.Fatalf("a negative entry admitted a publish: %+v", out)
		}
		remote.SetState("upstream", virtual.EntryState{})
	})

	t.Run("R-REPO-048 a manual claim of the local repository that covers the name satisfies the check", func(t *testing.T) {
		if _, err := a.svc.PutClaims(context.Background(), caller, "team-local", repository.MatchAny(),
			repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "left-pad"}}}, allowGlobal); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the claim to reach the routing", 5*time.Second, func() bool {
			ok, err := a.routing.CoveredByOwnClaim(context.Background(), a.repo("team-local"), "left-pad")
			return err == nil && ok
		})
		before := probes()
		out := check("ci-bot", "team-local", "left-pad")
		if !out.Proceed || probes() != before {
			t.Fatalf("out %+v probes %d", out, probes()-before)
		}
	})

	t.Run("R-REPO-048 the check does not run when the local repository already holds a version", func(t *testing.T) {
		a.publish("team-local", "react", "1.0.0")
		before := probes()
		out := check("ci-bot", "team-local", "react")
		if !out.Proceed || probes() != before {
			t.Fatalf("out %+v probes %d", out, probes()-before)
		}
	})

	t.Run("R-REPO-048 an upstream that cannot be asked fails closed for a deploy holder and proceeds for manage", func(t *testing.T) {
		remote.SetProbe("upstream", virtual.ProbeResult{Outcome: virtual.Undetermined, RetryAfter: 20 * time.Second})
		out := check("ci-bot", "team-local", "brand-new-2")
		if out.Proceed || out.Code != errs.CodeShadowCheckUnavailable || out.RetryAfter != 20*time.Second {
			t.Fatalf("ci-bot: %+v", out)
		}
		out = check("lead", "team-local", "brand-new-2")
		if !out.Proceed || !out.Undetermined {
			t.Fatalf("lead: %+v", out)
		}
	})
}
