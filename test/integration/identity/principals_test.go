//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

func claims(username, sub string, mut ...func(*principal.LoginClaims)) principal.LoginClaims {
	c := principal.LoginClaims{
		Provider: "corp", Issuer: "https://idp.example.com", Subject: sub, Username: username,
		Email: username + "@example.com", EmailVerified: true, DisplayName: "User " + username,
		GroupSync: true, JIT: true, SyncUsername: true,
	}
	for _, m := range mut {
		m(&c)
	}
	return c
}

func groupNames(t *testing.T, r *replica, id string) []string {
	t.Helper()
	gs, err := r.principals.Groups(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return out
}

func TestR_AUTH_007_JustInTimeProvisioning(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()

	t.Run("the first login creates a jit user and one user.created event, the second creates nothing", func(t *testing.T) {
		p, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice"))
		if err != nil || p.Kind != principal.KindUser || p.Name != "alice" {
			t.Fatalf("%+v %v", p, err)
		}
		again, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice"))
		if err != nil || again.ID != p.ID {
			t.Fatalf("%+v %v", again, err)
		}
		evs := a.rec.named(audit.EventUserCreated)
		if len(evs) != 1 {
			t.Fatalf("%d user.created events", len(evs))
		}
		var d struct{ Source string }
		_ = json.Unmarshal(evs[0].Details, &d)
		if d.Source != "jit" {
			t.Fatalf("source %q", d.Source)
		}
		u, err := a.manager.GetUser(ctx, "alice")
		if err != nil || u.Origin != "jit" || u.Email != "alice@example.com" || !u.EmailVerified || u.LastLoginAt.IsZero() || len(u.Identities) != 1 || !u.Identities[0].Linked {
			t.Fatalf("%+v %v", u, err)
		}
	})
	t.Run("a different identity with a taken username fails with username_taken and changes nothing", func(t *testing.T) {
		_, err := a.provisioner.SignIn(ctx, claims("alice", "sub-other", func(c *principal.LoginClaims) { c.Issuer = "https://other.example.com" }))
		if !errors.Is(err, principal.ErrUsernameTaken) {
			t.Fatalf("err = %v", err)
		}
		if n := e.scalar(`SELECT count(*) FROM principal WHERE kind = 'user' AND name = 'alice'`); n != 1 {
			t.Fatalf("%d alice", n)
		}
	})
	t.Run("an unknown identity with jit off is not_provisioned", func(t *testing.T) {
		_, err := a.provisioner.SignIn(ctx, claims("zed", "sub-zed", func(c *principal.LoginClaims) { c.JIT = false }))
		if !errors.Is(err, principal.ErrNotProvisioned) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("reserved and malformed usernames fail with username_invalid", func(t *testing.T) {
		for _, n := range []string{"me", "anonymous", "authenticated", "system", "breakglass", "Mixed", ""} {
			if _, err := a.provisioner.SignIn(ctx, claims(n, "sub-"+n)); !errors.Is(err, principal.ErrUsernameInvalid) {
				t.Errorf("%q: %v", n, err)
			}
		}
	})
	t.Run("a pre-provisioned user with a pending link logs in once and is linked", func(t *testing.T) {
		_, err := a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "bob", Identities: []principal.PendingIdentity{{Provider: "corp", Issuer: "https://idp.example.com"}}})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := a.manager.GetUser(ctx, "bob")
		if u.Identities[0].Linked || u.Origin != "admin" {
			t.Fatalf("%+v", u)
		}
		p, err := a.provisioner.SignIn(ctx, claims("bob", "sub-bob", func(c *principal.LoginClaims) { c.JIT = false }))
		if err != nil || p.ID != u.ID {
			t.Fatalf("%+v %v", p, err)
		}
		u, _ = a.manager.GetUser(ctx, "bob")
		if !u.Identities[0].Linked || u.Origin != "admin" {
			t.Fatalf("%+v", u)
		}
		// The link is used once: a second identity cannot claim the same user.
		_, err = a.provisioner.SignIn(ctx, claims("bob", "sub-bob-2", func(c *principal.LoginClaims) { c.JIT = false }))
		if !errors.Is(err, principal.ErrUsernameTaken) {
			t.Fatalf("the pending link was used twice: %v", err)
		}
	})
	t.Run("a changed mapped username renames the user and its tokens keep working", func(t *testing.T) {
		p, _ := a.provisioner.SignIn(ctx, claims("carol", "sub-carol"))
		u, _ := a.manager.GetUser(ctx, "carol")
		pat := a.pat(u)
		renamed, err := a.provisioner.SignIn(ctx, claims("carol.smith", "sub-carol"))
		if err != nil || renamed.ID != p.ID || renamed.Name != "carol.smith" {
			t.Fatalf("%+v %v", renamed, err)
		}
		res, err := a.bearer(pat.Secret, "/x")
		if err != nil || res.Principal.Name != "carol.smith" {
			t.Fatalf("%+v %v", res, err)
		}
		if len(a.rec.named(audit.EventUserRenamed)) != 1 {
			t.Fatal("user.renamed")
		}
	})
	t.Run("a rename onto a taken name keeps the old name and records user.rename_conflict", func(t *testing.T) {
		if _, err := a.provisioner.SignIn(ctx, claims("dave", "sub-dave")); err != nil {
			t.Fatal(err)
		}
		p, err := a.provisioner.SignIn(ctx, claims("alice", "sub-dave"))
		if err != nil || p.Name != "dave" {
			t.Fatalf("%+v %v", p, err)
		}
		if len(a.rec.named(audit.EventUserRenameConflict)) != 1 {
			t.Fatal("user.rename_conflict")
		}
	})
	t.Run("without sync_username the name stays", func(t *testing.T) {
		if _, err := a.provisioner.SignIn(ctx, claims("erin", "sub-erin")); err != nil {
			t.Fatal(err)
		}
		p, err := a.provisioner.SignIn(ctx, claims("erin2", "sub-erin", func(c *principal.LoginClaims) { c.SyncUsername = false }))
		if err != nil || p.Name != "erin" {
			t.Fatalf("%+v %v", p, err)
		}
	})
	t.Run("a disabled user is refused, a dormant one is re-enabled", func(t *testing.T) {
		if _, err := a.provisioner.SignIn(ctx, claims("frank", "sub-frank")); err != nil {
			t.Fatal(err)
		}
		if err := a.manager.DisableUser(ctx, admin, "frank", principal.DisableOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.provisioner.SignIn(ctx, claims("frank", "sub-frank")); !errors.Is(err, principal.ErrUserDisabled) {
			t.Fatalf("err = %v", err)
		}
		if n := e.scalar(`SELECT count(*) FROM principal WHERE name = 'frank' AND status = 'disabled'`); n != 1 {
			t.Fatal("the refused login changed the user")
		}
	})
}

func TestR_AUTH_021_DormantUserIsReEnabledByALogin(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	p, _ := a.provisioner.SignIn(ctx, claims("frank", "sub-frank"))
	u, _ := a.manager.GetUser(ctx, "frank")
	pat := a.pat(u)
	if err := a.manager.DisableUser(ctx, admin, "frank", principal.DisableOptions{Reason: "dormant"}); err != nil {
		t.Fatal(err)
	}
	_, err := a.bearer(pat.Secret, "/x")
	wantCode(t, err, 401, errs.CodePrincipalDisabled)
	if _, err := a.provisioner.SignIn(ctx, claims("frank", "sub-frank")); err != nil {
		t.Fatalf("the login re-enables a dormant user: %v", err)
	}
	if res, err := a.bearer(pat.Secret, "/x"); err != nil || res.Principal.ID != p.ID {
		t.Fatalf("the same PAT works again: %+v %v", res, err)
	}
	// A user disabled by an administrator stays disabled.
	_ = a.manager.DisableUser(ctx, admin, "frank", principal.DisableOptions{Reason: "admin"})
	if _, err := a.provisioner.SignIn(ctx, claims("frank", "sub-frank")); !errors.Is(err, principal.ErrUserDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestR_AUTH_007_ConcurrentFirstLoginsOfOneIdentityCreateOneUser(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make([]string, 8)
	errsOut := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := []*replica{a, b}[i%2]
			p, err := r.provisioner.SignIn(ctx, claims("gina", "sub-gina"))
			ids[i], errsOut[i] = p.ID, err
		}()
	}
	wg.Wait()
	for i, err := range errsOut {
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Fatalf("two users for one identity: %v", ids)
		}
	}
	if n := e.scalar(`SELECT count(*) FROM principal WHERE kind = 'user' AND name = 'gina'`); n != 1 {
		t.Fatalf("%d users", n)
	}
	if n := e.scalar(`SELECT count(*) FROM user_identity`); n != 1 {
		t.Fatalf("%d identity links", n)
	}
}

func TestR_AUTH_008_GroupSynchronization(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	withGroups := func(g ...string) func(*principal.LoginClaims) {
		return func(c *principal.LoginClaims) { c.Groups = g; c.CreateGroups = true }
	}
	p, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice", withGroups("team-web", "Not Valid")))
	if err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, a, p.ID); !slices.Equal(got, []string{"team-web"}) {
		t.Fatalf("groups %v", got)
	}
	g, _ := a.manager.GetGroup(ctx, "team-web")
	if g.Origin != "oidc" || g.MemberCount != 1 {
		t.Fatalf("%+v", g)
	}
	// A manual membership survives the next sync, which removes the oidc one.
	if _, err := a.manager.PatchMembers(ctx, admin, "team-web", "", []string{"alice"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.provisioner.SignIn(ctx, claims("alice", "sub-alice", func(c *principal.LoginClaims) { c.Groups = []string{}; c.CreateGroups = true })); err != nil {
		t.Fatal(err)
	}
	if got := groupNames(t, a, p.ID); !slices.Equal(got, []string{"team-web"}) {
		t.Fatalf("the manual membership must survive: %v", got)
	}
	ms, _ := a.manager.ListMembers(ctx, "team-web", "", "", "", 10)
	if len(ms) != 1 || ms[0].Source != "manual" {
		t.Fatalf("%+v", ms)
	}
	synced := a.rec.named(audit.EventUserGroupsSynced)
	if len(synced) != 2 {
		t.Fatalf("%d user.groups.synced events (only when something changed)", len(synced))
	}
	if len(synced[1].Journal) == 0 {
		t.Fatal("the removal has no journal entry")
	}

	t.Run("a missing claim clears the oidc memberships and records user.groups.claim_missing", func(t *testing.T) {
		bob, _ := a.provisioner.SignIn(ctx, claims("bob", "sub-bob", withGroups("team-web")))
		if got := groupNames(t, a, bob.ID); !slices.Equal(got, []string{"team-web"}) {
			t.Fatalf("%v", got)
		}
		_, err := a.provisioner.SignIn(ctx, claims("bob", "sub-bob", func(c *principal.LoginClaims) { c.ClaimState = principal.ClaimOverage }))
		if err != nil {
			t.Fatal(err)
		}
		if got := groupNames(t, a, bob.ID); len(got) != 0 {
			t.Fatalf("the memberships stayed: %v", got)
		}
		evs := a.rec.named(audit.EventUserGroupsClaimMiss)
		if len(evs) != 1 || evs[0].Reason != "overage" {
			t.Fatalf("%+v", evs)
		}
		u, _ := a.manager.GetUser(ctx, "bob")
		if u.GroupClaimMissing == nil || u.GroupClaimMissing.Reason != "overage" || u.GroupClaimMissing.Provider != "corp" {
			t.Fatalf("groupClaimMissing: %+v", u.GroupClaimMissing)
		}
		if _, err := a.provisioner.SignIn(ctx, claims("bob", "sub-bob", withGroups("team-web"))); err != nil {
			t.Fatal(err)
		}
		if u, _ = a.manager.GetUser(ctx, "bob"); u.GroupClaimMissing != nil {
			t.Fatalf("the next login that carries the claim clears the field: %+v", u.GroupClaimMissing)
		}
	})
	t.Run("on_missing_claim keep leaves the memberships", func(t *testing.T) {
		cy, _ := a.provisioner.SignIn(ctx, claims("cy", "sub-cy", withGroups("team-web")))
		if _, err := a.provisioner.SignIn(ctx, claims("cy", "sub-cy", func(c *principal.LoginClaims) { c.ClaimState = principal.ClaimAbsent; c.KeepOnMissing = true })); err != nil {
			t.Fatal(err)
		}
		if got := groupNames(t, a, cy.ID); !slices.Equal(got, []string{"team-web"}) {
			t.Fatalf("%v", got)
		}
	})
	t.Run("without create_groups an unknown target group is skipped", func(t *testing.T) {
		d, _ := a.provisioner.SignIn(ctx, claims("dee", "sub-dee", func(c *principal.LoginClaims) { c.Groups = []string{"nowhere"} }))
		if got := groupNames(t, a, d.ID); len(got) != 0 {
			t.Fatalf("%v", got)
		}
		if n := e.scalar(`SELECT count(*) FROM principal WHERE kind = 'group' AND name = 'nowhere'`); n != 0 {
			t.Fatal("the group was created")
		}
	})
	t.Run("two replicas creating the same missing group at the same moment end with one group", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := []*replica{a, b}[i%2]
				name := "user" + string(rune('a'+i))
				if _, err := r.provisioner.SignIn(ctx, claims(name, "sub-"+name, withGroups("shared-new"))); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if n := e.scalar(`SELECT count(*) FROM principal WHERE kind = 'group' AND name = 'shared-new'`); n != 1 {
			t.Fatalf("%d groups", n)
		}
		if n := e.scalar(`SELECT count(*) FROM group_membership m JOIN principal g ON g.id = m.group_id WHERE g.name = 'shared-new'`); n != 6 {
			t.Fatalf("%d memberships", n)
		}
	})
	t.Run("R-AUTHZ-002 the other replica sees the new membership within the revocation bound", func(t *testing.T) {
		z, _ := a.provisioner.SignIn(ctx, claims("zoe", "sub-zoe"))
		if got := groupNames(t, b, z.ID); len(got) != 0 {
			t.Fatal(got)
		}
		if _, err := a.provisioner.SignIn(ctx, claims("zoe", "sub-zoe", withGroups("team-web"))); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B resolving the new group", revocationBound, func() bool {
			return slices.Equal(groupNames(t, b, z.ID), []string{"team-web"})
		})
	})
}

func TestR_AUTH_018_GroupsAndMemberships(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	alice := a.user("alice")
	sa := a.serviceAccount("ci-bot")
	pat := a.pat(alice)
	sat := a.sat(sa)

	if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "team-web", Description: "web", Members: []string{"alice", "sa:ci-bot"}}); err != nil {
		t.Fatal(err)
	}
	t.Run("a service account added as sa:<name> gets the group with its token", func(t *testing.T) {
		res, err := b.bearer(sat.Secret, "/x")
		if err != nil || len(res.Groups) != 1 || res.Groups[0].Name != "team-web" || res.Principal.Kind != principal.KindServiceAccount {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("an unknown member answers 404 and nothing is created or applied", func(t *testing.T) {
		_, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "ghosts", Members: []string{"alice", "nobody"}})
		wantCode(t, err, 404, errs.CodeNotFound)
		if _, err := a.manager.GetGroup(ctx, "ghosts"); errCode(err) != errs.CodeNotFound {
			t.Fatal("the group was created")
		}
		_, err = a.manager.PatchMembers(ctx, admin, "team-web", "", []string{"alice", "nobody"}, nil)
		wantCode(t, err, 404, errs.CodeNotFound)
	})
	t.Run("authenticated and anonymous are never members", func(t *testing.T) {
		for _, n := range []string{"authenticated", "anonymous"} {
			_, err := a.manager.PatchMembers(ctx, admin, "team-web", "", []string{n}, nil)
			wantCode(t, err, 404, errs.CodeNotFound)
		}
	})
	t.Run("removing a member removes only the manual membership and shows what remains", func(t *testing.T) {
		// Give alice an oidc membership of the same group by a direct row.
		e.exec(`INSERT INTO group_membership (group_id, member_id, source) SELECT g.id, $1::uuid, 'oidc:corp' FROM principal g WHERE g.name = 'team-web'`, alice.ID)
		res, err := a.manager.PatchMembers(ctx, admin, "team-web", "", nil, []string{"alice"})
		if err != nil || len(res) != 1 || !slices.Equal(res[0].Sources, []string{"oidc:corp"}) {
			t.Fatalf("%+v %v", res, err)
		}
		if names := groupNames(t, a, alice.ID); !slices.Equal(names, []string{"team-web"}) {
			t.Fatalf("alice still has the group through oidc:corp: %v", names)
		}
		evs := a.rec.named(audit.EventGroupMembersChanged)
		last := evs[len(evs)-1]
		var entry struct {
			Group   string
			Removed []struct{ Member, Source string }
		}
		if err := json.Unmarshal(last.Journal, &entry); err != nil || entry.Group != "team-web" || len(entry.Removed) != 1 || entry.Removed[0].Source != "manual" {
			t.Fatalf("journal entry %s: %v", last.Journal, err)
		}
	})
	t.Run("a membership change reaches the other replica's token within the bound", func(t *testing.T) {
		if _, err := a.bearer(pat.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		if res, err := b.bearer(pat.Secret, "/x"); err != nil || len(res.Groups) != 1 {
			t.Fatalf("%+v %v", res, err)
		}
		e.exec(`DELETE FROM group_membership WHERE member_id = $1::uuid AND source = 'oidc:corp'`, alice.ID)
		if _, err := a.manager.PatchMembers(ctx, admin, "team-web", "", nil, []string{"sa:ci-bot"}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B resolving the changed membership of the service account", revocationBound, func() bool {
			res, err := b.bearer(sat.Secret, "/x")
			return err == nil && len(res.Groups) == 0
		})
	})
	t.Run("groups are flat records with origin and auto-join", func(t *testing.T) {
		g, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "everyone", AutoJoin: true})
		if err != nil || !g.AutoJoin || g.Origin != "admin" {
			t.Fatalf("%+v %v", g, err)
		}
		_, err = a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "everyone"})
		wantCode(t, err, 409, errs.CodeAlreadyExists)
		_, err = a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "Bad Name"})
		wantCode(t, err, 400, errs.CodeValidation)
		// Auto-join gives a new user an auto membership, existing users none (R-AUTH-019).
		bob := a.user("bob")
		if got := groupNames(t, a, bob.ID); !slices.Equal(got, []string{"everyone"}) {
			t.Fatalf("%v", got)
		}
		if got := groupNames(t, a, alice.ID); slices.Contains(got, "everyone") {
			t.Fatal("not retroactive")
		}
		// Any role binding makes the group refuse auto-join; auditor, so the group
		// does not become the installation's only administrator (R-AUTHZ-022).
		e.exec(`INSERT INTO authz_role_binding (role, principal_id) SELECT 'auditor', id FROM principal WHERE name = 'team-web'`)
		_, err = a.manager.UpdateGroup(ctx, admin, "team-web", principal.GroupPatch{Expected: errs.AnyRevision, AutoJoin: ptr(true)})
		wantCode(t, err, 409, errs.CodeAutoJoinAdminConflict)
	})
	t.Run("a group with origin scim answers 409 managed_by_scim", func(t *testing.T) {
		e.exec(`WITH g AS (INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'group', 'scim-group') RETURNING id) INSERT INTO user_group (principal_id, origin) SELECT id, 'scim' FROM g`)
		_, err := a.manager.PatchMembers(ctx, admin, "scim-group", "", []string{"alice"}, nil)
		wantCode(t, err, 409, errs.CodeManagedBySCIM)
		wantCode(t, a.manager.DeleteGroup(ctx, admin, "scim-group"), 409, errs.CodeManagedBySCIM)
		_, err = a.manager.UpdateGroup(ctx, admin, "scim-group", principal.GroupPatch{Expected: errs.AnyRevision, Name: ptr("renamed")})
		wantCode(t, err, 409, errs.CodeManagedBySCIM)
	})
	t.Run("renaming keeps the grants and the memberships", func(t *testing.T) {
		if _, err := a.manager.PatchMembers(ctx, admin, "team-web", "", []string{"alice"}, nil); err != nil {
			t.Fatal(err)
		}
		g, err := a.manager.UpdateGroup(ctx, admin, "team-web", principal.GroupPatch{Expected: errs.AnyRevision, Name: ptr("web-team"), Description: ptr("renamed")})
		if err != nil || g.Name != "web-team" || g.MemberCount != 1 {
			t.Fatalf("%+v %v", g, err)
		}
	})
	t.Run("deleting a group removes its memberships and journals the deletion", func(t *testing.T) {
		if err := a.manager.DeleteGroup(ctx, admin, "web-team"); err != nil {
			t.Fatal(err)
		}
		if n := e.scalar(`SELECT count(*) FROM group_membership m JOIN principal g ON g.id = m.group_id WHERE g.name = 'web-team'`); n != 0 {
			t.Fatal("memberships stayed")
		}
		evs := a.rec.named(audit.EventGroupDeleted)
		if len(evs) != 1 || len(evs[0].Journal) == 0 {
			t.Fatalf("%+v", evs)
		}
		waitFor(t, "the group disappearing from the other replica", revocationBound, func() bool {
			return len(groupNames(t, b, alice.ID)) == 0
		})
	})
}

func ptr[T any](v T) *T { return &v }

func TestR_AUTH_016_DisableEnableDelete(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()

	t.Run("disable ends sessions and makes tokens fail with principal_disabled, on every replica", func(t *testing.T) {
		u := a.user("alice")
		pat := a.pat(u)
		e.exec(`INSERT INTO ui_session (id, secret_sha256, csrf_sha256, principal_id, auth_method, authenticated_at, idle_timeout, absolute_expires_at)
		        VALUES (gen_random_uuid(), sha256('s'), sha256('c'), $1::uuid, 'oidc', now(), interval '1 hour', now() + interval '12 hours')`, u.ID)
		for _, r := range []*replica{a, b} {
			if _, err := r.bearer(pat.Secret, "/x"); err != nil {
				t.Fatal(err)
			}
		}
		if err := a.manager.DisableUser(ctx, admin, "alice", principal.DisableOptions{}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B refusing the token of the disabled user", revocationBound, func() bool {
			_, err := b.bearer(pat.Secret, "/x")
			return errCode(err) == errs.CodePrincipalDisabled
		})
		if n := e.scalar(`SELECT count(*) FROM ui_session WHERE principal_id = $1::uuid AND end_reason = 'principal_disabled'`, u.ID); n != 1 {
			t.Fatalf("%d sessions ended", n)
		}
		evs := a.rec.named(audit.EventUserDisabled)
		if len(evs) != 1 || len(evs[0].Journal) == 0 {
			t.Fatalf("%+v", evs)
		}
		// Enabling brings the token back: it was not revoked.
		if err := a.manager.EnableUser(ctx, admin, "alice"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B accepting the token of the re-enabled user", revocationBound, func() bool {
			_, err := b.bearer(pat.Secret, "/x")
			return err == nil
		})
	})
	t.Run("disable with revokeTokens revokes, and enabling does not resurrect", func(t *testing.T) {
		u := a.user("bob")
		pat := a.pat(u)
		if err := a.manager.DisableUser(ctx, admin, "bob", principal.DisableOptions{RevokeTokens: true}); err != nil {
			t.Fatal(err)
		}
		if err := a.manager.EnableUser(ctx, admin, "bob"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B refusing the revoked token", revocationBound, func() bool {
			_, err := b.bearer(pat.Secret, "/x")
			return errCode(err) == errs.CodeInvalidToken
		})
		rec, _ := a.tokens.Get(ctx, pat.Record.ID)
		if rec.RevocationReason != "principal_disabled" {
			t.Fatalf("reason %q", rec.RevocationReason)
		}
	})
	t.Run("delete removes the user with tokens and memberships; a new login gets a new id", func(t *testing.T) {
		p1, _ := a.provisioner.SignIn(ctx, claims("carol", "sub-carol"))
		u, _ := a.manager.GetUser(ctx, "carol")
		pat := a.pat(u)
		if err := a.manager.DeleteUser(ctx, admin, "carol"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B refusing the token of the deleted user", revocationBound, func() bool {
			_, err := b.bearer(pat.Secret, "/x")
			return errCode(err) == errs.CodeInvalidToken
		})
		if _, err := a.manager.GetUser(ctx, "carol"); errCode(err) != errs.CodeNotFound {
			t.Fatal("the user still exists")
		}
		p2, err := a.provisioner.SignIn(ctx, claims("carol", "sub-carol"))
		if err != nil || p2.ID == p1.ID {
			t.Fatalf("the old id came back: %+v %v", p2, err)
		}
		evs := a.rec.named(audit.EventUserDeleted)
		var d struct{ TokensDeleted int }
		_ = json.Unmarshal(evs[0].Details, &d)
		if len(evs) != 1 || d.TokensDeleted != 1 {
			t.Fatalf("%s", evs[0].Details)
		}
	})
	t.Run("the break-glass account can be neither disabled nor deleted", func(t *testing.T) {
		bg := a.user("rescue")
		e.exec(`UPDATE app_user SET is_break_glass = true WHERE principal_id = $1::uuid`, bg.ID)
		if err := a.manager.DisableUser(ctx, admin, "rescue", principal.DisableOptions{}); !errors.Is(err, principal.ErrBreakGlass) {
			t.Fatal(err)
		}
		if err := a.manager.DeleteUser(ctx, admin, "rescue"); !errors.Is(err, principal.ErrBreakGlass) {
			t.Fatal(err)
		}
		p, err := a.principals.ByName(ctx, principal.KindBreakGlass, "rescue")
		if err != nil || p.Kind != principal.KindBreakGlass {
			t.Fatalf("%+v %v", p, err)
		}
	})
	t.Run("create: duplicate 409, reserved 422 reserved_name, invalid 422, user edits", func(t *testing.T) {
		u := a.user("dave")
		_, err := a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "dave"})
		wantCode(t, err, 409, errs.CodeAlreadyExists)
		_, err = a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "me"})
		wantCode(t, err, 422, "reserved_name")
		_, err = a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "breakglass"})
		wantCode(t, err, 422, "reserved_name")
		_, err = a.manager.CreateUser(ctx, admin, principal.NewUser{Name: "Dave Smith"})
		wantCode(t, err, 400, errs.CodeUsernameInvalid)
		got, err := a.manager.UpdateUser(ctx, admin, "dave", principal.UserPatch{Expected: errs.AnyRevision, DisplayName: ptr("Dave D."), Email: ptr("d@example.org")})
		if err != nil || got.DisplayName != "Dave D." || got.Email != "d@example.org" || got.ID != u.ID {
			t.Fatalf("%+v %v", got, err)
		}
		_, err = a.manager.UpdateUser(ctx, admin, "nobody", principal.UserPatch{Expected: errs.AnyRevision})
		wantCode(t, err, 404, errs.CodeNotFound)
	})
	t.Run("list filters by name, status and group", func(t *testing.T) {
		if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "ops", Members: []string{"dave"}}); err != nil {
			t.Fatal(err)
		}
		names := func(f principal.UserFilter) []string {
			us, err := a.manager.ListUsers(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, u := range us {
				out = append(out, u.Name)
			}
			return out
		}
		if got := names(principal.UserFilter{Group: "ops"}); !slices.Equal(got, []string{"dave"}) {
			t.Fatalf("%v", got)
		}
		if got := names(principal.UserFilter{Status: "disabled"}); len(got) != 0 && !slices.Contains(got, "alice") && !slices.Contains(got, "bob") {
			t.Fatalf("%v", got)
		}
		if got := names(principal.UserFilter{Query: "dav"}); !slices.Equal(got, []string{"dave"}) {
			t.Fatalf("%v", got)
		}
		all := names(principal.UserFilter{Limit: 2})
		if len(all) != 2 {
			t.Fatalf("%v", all)
		}
		if next := names(principal.UserFilter{After: all[1], Limit: 2}); len(next) == 0 || next[0] <= all[1] {
			t.Fatalf("keyset %v after %v", next, all)
		}
	})
}

func TestR_AUTH_020_ServiceAccounts(t *testing.T) {
	e := newEnv(t)
	a, b := e.newReplica("a", replicaOpts{valkey: vkDead}), e.newReplica("b", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	sa := a.serviceAccount("ci-bot")
	sat := a.sat(sa)

	if res, err := b.bearer(sat.Secret, "/x"); err != nil || res.Principal.Kind != principal.KindServiceAccount || res.Principal.Name != "ci-bot" || res.Credential.TokenType != token.TypeSAT {
		t.Fatalf("%+v %v", res, err)
	}
	t.Run("names", func(t *testing.T) {
		for _, n := range []string{"x", "Ci-Bot", "ci_bot", "-bot"} {
			_, err := a.manager.CreateServiceAccount(ctx, admin, principal.NewServiceAccount{Name: n})
			wantCode(t, err, 400, errs.CodeValidation)
		}
		_, err := a.manager.CreateServiceAccount(ctx, admin, principal.NewServiceAccount{Name: "ci-bot"})
		wantCode(t, err, 409, errs.CodeAlreadyExists)
	})
	t.Run("admin needs a binder", func(t *testing.T) {
		_, err := a.manager.CreateServiceAccount(ctx, admin, principal.NewServiceAccount{Name: "ops", Admin: true})
		if !errors.Is(err, principal.ErrAdminBinding) {
			t.Fatal(err)
		}
	})
	t.Run("disabling makes all tokens fail with principal_disabled, on every replica", func(t *testing.T) {
		if err := a.manager.DisableServiceAccount(ctx, admin, "ci-bot", false); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B", revocationBound, func() bool {
			_, err := b.bearer(sat.Secret, "/x")
			return errCode(err) == errs.CodePrincipalDisabled
		})
		if err := a.manager.EnableServiceAccount(ctx, admin, "ci-bot"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B", revocationBound, func() bool {
			_, err := b.bearer(sat.Secret, "/x")
			return err == nil
		})
	})
	t.Run("update and list", func(t *testing.T) {
		got, err := a.manager.UpdateServiceAccount(ctx, admin, "ci-bot", "deploys", errs.AnyRevision)
		if err != nil || got.Description != "deploys" {
			t.Fatalf("%+v %v", got, err)
		}
		list, err := a.manager.ListServiceAccounts(ctx, "ci", "", 10)
		if err != nil || len(list) != 1 {
			t.Fatalf("%+v %v", list, err)
		}
	})
	t.Run("deleting removes the account with its tokens", func(t *testing.T) {
		if err := a.manager.DeleteServiceAccount(ctx, admin, "ci-bot"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B", revocationBound, func() bool {
			_, err := b.bearer(sat.Secret, "/x")
			return errCode(err) == errs.CodeInvalidToken
		})
		if len(a.rec.named(audit.EventSADeleted)) != 1 {
			t.Fatal("serviceaccount.deleted")
		}
	})
	t.Run("a service account is not a user", func(t *testing.T) {
		a.serviceAccount("other-bot")
		if _, err := a.principals.ByName(ctx, principal.KindUser, "other-bot"); !errors.Is(err, principal.ErrNotFound) {
			t.Fatal("a service account resolves as a user")
		}
	})
}

func TestR_AUTHZ_002_PrincipalStoreLookups(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	alice := a.user("alice")
	for _, c := range []struct {
		id   string
		kind principal.Kind
		name string
	}{{principal.AnonymousID, principal.KindAnonymous, "anonymous"}, {principal.AuthenticatedID, principal.KindAuthenticated, "authenticated"}, {alice.ID, principal.KindUser, "alice"}} {
		p, err := a.principals.ByID(ctx, c.id)
		if err != nil || p.Kind != c.kind || p.Name != c.name || p.Status != principal.StatusActive {
			t.Errorf("%s: %+v %v", c.name, p, err)
		}
		if p, err = a.principals.ByName(ctx, c.kind, c.name); err != nil || p.ID != c.id {
			t.Errorf("%s by name: %+v %v", c.name, p, err)
		}
	}
	if _, err := a.principals.ByID(ctx, principal.NewID()); !errors.Is(err, principal.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := a.principals.ByID(ctx, "alice"); !errors.Is(err, principal.ErrNotFound) {
		t.Fatal(err)
	}
	t.Run("R-AUTH-037 the built-in principals cannot be changed or deleted", func(t *testing.T) {
		for _, sql := range []string{
			`DELETE FROM principal WHERE kind = 'anonymous'`,
			`UPDATE principal SET status = 'disabled' WHERE kind = 'authenticated'`,
			`UPDATE principal SET name = 'x' WHERE kind = 'anonymous'`,
		} {
			if _, err := e.admin.Exec(ctx, sql); err == nil {
				t.Errorf("%s was allowed", sql)
			}
		}
	})
	t.Run("only positive results are cached and a change evicts them", func(t *testing.T) {
		if _, err := a.principals.ByID(ctx, alice.ID); err != nil {
			t.Fatal(err)
		}
		cached := func() int {
			for _, s := range a.caches.Stats() {
				if s.Name == "auth_principals" {
					return s.Entries
				}
			}
			return -1
		}
		before := cached()
		_, _ = a.principals.ByID(ctx, principal.NewID())
		if cached() != before {
			t.Fatal("a miss was cached")
		}
		if err := a.manager.DisableUser(ctx, admin, "alice", principal.DisableOptions{}); err != nil {
			t.Fatal(err)
		}
		p, err := a.principals.ByID(ctx, alice.ID)
		if err != nil || p.Status != principal.StatusDisabled {
			t.Fatalf("the writing replica serves its own change at once: %+v %v", p, err)
		}
	})
}
