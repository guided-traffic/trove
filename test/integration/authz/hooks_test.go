//go:build integration

package authz

import (
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers every format
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_AUTHZ_028_IdentityAndRepositoryWritesAdvanceTheEpoch extends the
// completeness test to the writes of the identity and repository areas that
// change inputs of the grant index: the deletion and the rename of a principal
// that a policy names, and the deletion of a repository that a policy selects by
// key. Each moves the epoch of the area authz once, writes one change row and
// moves the version of the policy whose representation changed.
func TestR_AUTHZ_028_IdentityAndRepositoryWritesAdvanceTheEpoch(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	ctx := t.Context()
	actor := sub(e.user("root-admin"))
	e.user("alice")
	e.user("carol")
	e.group("team-web", false)
	e.group("team-old", false)
	e.serviceAccount("ci-bot")
	e.repo("virt-a", "virtual", "generic", "")
	manager := principal.NewManager(principal.ManagerOptions{
		DB: principal.Joinable{DB: a.db}, Notifier: a.changes, Recorder: a.rec, Authz: authz.NewPrincipalHook(a.changes),
	})
	repos, err := repository.NewService(repository.Options{
		Runner: a.db, Catalog: catalog.Default(), Recorder: a.rec, Notifier: a.changes, Hooks: authz.NewPolicyHook(a.changes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.svc.Create(ctx, actor, authz.PolicyInput{
		Name: "p", Resources: authz.Resources{Repositories: []string{"virt-a"}},
		Entries: []authz.Entry{
			{Principal: "user:alice", Actions: []string{"read"}},
			{Principal: "group:team-web", Actions: []string{"read"}},
			{Principal: "group:team-old", Actions: []string{"read"}},
			{Principal: "sa:ci-bot", Actions: []string{"read"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.svc.BindRole(ctx, actor, authz.RoleAuditor, "user:carol"); err != nil {
		t.Fatal(err)
	}
	caller := principal.Caller{}
	renamed := "team-new"
	steps := []struct {
		name         string
		do           func() error
		movesVersion bool
	}{
		{"delete a user a policy names", func() error { return manager.DeleteUser(ctx, caller, "alice") }, true},
		{"delete a user that holds a role", func() error { return manager.DeleteUser(ctx, caller, "carol") }, false},
		{"rename a group a policy names", func() error {
			_, err := manager.UpdateGroup(ctx, caller, "team-web", principal.GroupPatch{Name: &renamed, Expected: errs.AnyRevision})
			return err
		}, true},
		{"delete a group a policy names", func() error { return manager.DeleteGroup(ctx, caller, "team-old") }, true},
		{"delete a service account a policy names", func() error { return manager.DeleteServiceAccount(ctx, caller, "ci-bot") }, true},
		{"delete a repository a policy selects by key", func() error {
			_, err := repos.Delete(ctx, repository.Caller{}, "virt-a", false)
			return err
		}, true},
	}
	for _, s := range steps {
		epoch, changes, version := e.authzEpoch(), e.authzChanges(), e.number("SELECT version FROM authz_policy WHERE name = 'p'")
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if got := e.authzEpoch(); got != epoch+1 || e.authzChanges() != changes+1 {
			t.Errorf("R-AUTHZ-028 %s: epoch %d -> %d, changes %d -> %d", s.name, epoch, got, changes, e.authzChanges())
		}
		if moved := e.number("SELECT version FROM authz_policy WHERE name = 'p'") != version; moved != s.movesVersion {
			t.Errorf("R-AUTHZ-028 %s: policy version moved = %v, want %v", s.name, moved, s.movesVersion)
		}
	}
	t.Run("a principal no policy and no binding names moves nothing", func(t *testing.T) {
		e.user("dave")
		epoch := e.authzEpoch()
		if err := manager.DeleteUser(ctx, caller, "dave"); err != nil {
			t.Fatal(err)
		}
		if e.authzEpoch() != epoch {
			t.Errorf("the epoch moved from %d to %d", epoch, e.authzEpoch())
		}
	})
}

// TestR_AUTHZ_028_OwnWriteIsInForceWithoutWaitingForThePoll: with a poll interval
// far above the test's time, a write through replica A is in force on A almost at
// once, because the change message of the commit starts a build of its own that
// does not wait for the poll (R-API-052, R-AUTHZ-028).
func TestR_AUTHZ_028_OwnWriteIsInForceWithoutWaitingForThePoll(t *testing.T) {
	e := newEnv(t)
	admin := sub(e.user("sec-alice"))
	bob, devs := e.user("bob"), e.group("developers", false)
	e.repo("npm-internal", "local", "npm", "")
	// The repository exists before the replica starts, so the index holds it and no
	// catch-up refreshes the index behind the back of the test: the index label is
	// the state the poll applied, which is the case a poll build would skip.
	a := e.newReplica("a", replicaOpts{poll: 30 * time.Second, staleness: 120 * time.Second})
	bobSub := sub(bob, devs)
	ctx := t.Context()
	for trial := 0; trial < 3; trial++ {
		name := fmt.Sprintf("readers-%d", trial)
		if _, _, err := a.svc.Create(ctx, admin, authz.PolicyInput{
			Name: name, Resources: authz.Resources{Repositories: []string{"npm-internal"}},
			Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
		}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the grant on the writing replica", 3*time.Second, func() bool {
			return a.engine.Decide(ctx, bobSub, read("npm-internal", "x", "")).Outcome == authz.Allow
		})
		if _, err := a.svc.Delete(ctx, admin, name, errs.AnyRevision); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the removal on the writing replica", 3*time.Second, func() bool {
			return a.engine.Decide(ctx, bobSub, read("npm-internal", "x", "")).Outcome == authz.DenyHidden
		})
	}
}
