//go:build integration

package identity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
)

// The guards between the identity operations and the role bindings: who may
// change a principal that holds a system role (R-AUTHZ-011, Q-AUTHZ-69), and the
// last administrator (R-AUTHZ-022).

func (e *env) bindRole(role, principalID string) {
	e.t.Helper()
	e.exec(`INSERT INTO authz_role_binding (role, principal_id) VALUES ($1, $2::uuid) ON CONFLICT DO NOTHING`, role, principalID)
}

// wantGuard expects the refusal of the role-holder guard: 403 naming roles:write,
// not the operation's own identities:write.
func wantGuard(t *testing.T, rp reply) {
	t.Helper()
	want(t, rp, 403, errs.CodeForbidden)
	if d, _ := rp.json(t)["detail"].(string); !strings.Contains(d, "roles:write") {
		t.Fatalf("refused for another reason: %s", rp.Body)
	}
}

func TestR_AUTHZ_011_IdentitiesWriteDoesNotReachTheRoles(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	fx := e.newFixture(a)
	ctx := context.Background()
	sec := a.user("sec")
	e.bindRole("security-admin", sec.ID)
	// A token exercises system capabilities only through its system items
	// (R-AUTHZ-013); the test replica's scope parser knows identities:read only,
	// so the stored scope is set directly.
	secTok := a.pat(sec)
	e.exec(`UPDATE api_token SET scope = '{"inherit":false,"admin":false,"repositories":[],"system":["identities:read","identities:write"]}'::jsonb WHERE id = $1::uuid`, secTok.Record.ID)
	secPAT := secTok.Secret
	if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "platform-admins", Members: []string{"root"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "team-web"}); err != nil {
		t.Fatal(err)
	}
	g, err := a.manager.GetGroup(ctx, "platform-admins")
	if err != nil {
		t.Fatal(err)
	}
	e.bindRole("admin", g.ID)
	mallory := a.user("mallory")
	// An administrator's token whose scope holds identities:write only.
	narrow := a.pat(principal.User{ID: fx.admin.ID, Name: fx.admin.Name})
	e.exec(`UPDATE api_token SET scope = '{"inherit":false,"admin":false,"repositories":[],"system":["identities:write"]}'::jsonb WHERE id = $1::uuid`, narrow.Record.ID)
	s := a.serve(e, nil)

	t.Run("R-AUTHZ-011 a security administrator cannot join a group that carries a role", func(t *testing.T) {
		rp := s.call(patch, "/api/v1/groups/platform-admins/members", secPAT, map[string]any{"add": []string{"sec"}})
		wantGuard(t, rp)
		rp = s.call(patch, "/api/v1/users/sec/groups", secPAT, map[string]any{"add": []string{"platform-admins"}})
		wantGuard(t, rp)
		if n := e.scalar(`SELECT count(*) FROM group_membership WHERE group_id = $1::uuid AND member_id = $2::uuid`, g.ID, sec.ID); n != 0 {
			t.Fatal("the security administrator joined the administrator group")
		}
	})
	t.Run("R-AUTHZ-011 R-AUTHZ-013 a token scoped to identities:write cannot either", func(t *testing.T) {
		rp := s.call(patch, "/api/v1/groups/platform-admins/members", narrow.Secret, map[string]any{"add": []string{"mallory"}})
		wantGuard(t, rp)
		if n := e.scalar(`SELECT count(*) FROM group_membership WHERE member_id = $1::uuid`, mallory.ID); n != 0 {
			t.Fatal("a token without roles:write made a user an administrator")
		}
	})
	t.Run("R-AUTHZ-011 an identity link on a user that holds a role is refused", func(t *testing.T) {
		rp := s.call(patch, "/api/v1/users/root", secPAT, map[string]any{"identities": []any{map[string]any{"provider": "corp", "subject": nil}}}, "If-Match", "*")
		wantGuard(t, rp)
		if n := e.scalar(`SELECT count(*) FROM user_identity WHERE user_id = $1::uuid`, fx.admin.ID); n != 0 {
			t.Fatal("a pending link was added to an administrator")
		}
	})
	t.Run("R-AUTHZ-011 renaming, deleting or disabling a role holder is refused", func(t *testing.T) {
		wantGuard(t, s.call(patch, "/api/v1/groups/platform-admins", secPAT, map[string]any{"name": "team-sec"}, "If-Match", "*"))
		wantGuard(t, s.call(del, "/api/v1/groups/platform-admins", secPAT, nil))
		wantGuard(t, s.call(post, "/api/v1/users/root/disable", secPAT, map[string]any{}))
		wantGuard(t, s.call(del, "/api/v1/users/root", secPAT, nil))
	})
	t.Run("R-AUTHZ-011 principals without a role stay the security administrator's to manage", func(t *testing.T) {
		rp := s.call(patch, "/api/v1/groups/team-web/members", secPAT, map[string]any{"add": []string{"mallory"}})
		want(t, rp, 200, "")
		rp = s.call(patch, "/api/v1/users/mallory", secPAT, map[string]any{"displayName": "Mallory"}, "If-Match", "*")
		want(t, rp, 200, "")
	})
	t.Run("R-AUTHZ-011 the administrator changes a role holder", func(t *testing.T) {
		rp := s.call(patch, "/api/v1/groups/platform-admins/members", fx.adminToken, map[string]any{"add": []string{"mallory"}})
		want(t, rp, 200, "")
	})
}

func TestR_AUTHZ_022_LastAdministratorAcrossTheIdentityOperations(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	// No break-glass account: the guard is in force.
	u1, u2 := a.user("u1"), a.user("u2")
	if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "admins", Members: []string{"u1"}}); err != nil {
		t.Fatal(err)
	}
	g, _ := a.manager.GetGroup(ctx, "admins")
	e.bindRole("admin", g.ID)
	e.bindRole("admin", u2.ID)
	sa := a.serviceAccount("robot")

	t.Run("R-AUTHZ-022 disabling one of two holders is allowed, the last one is refused", func(t *testing.T) {
		if err := a.manager.DisableUser(ctx, admin, "u2", principal.DisableOptions{}); err != nil {
			t.Fatal(err)
		}
		err := a.manager.DisableUser(ctx, admin, "u1", principal.DisableOptions{})
		wantCode(t, err, 409, errs.CodeLastAdmin)
	})
	t.Run("R-AUTHZ-022 removing the last holder from its group, deleting it or its group is refused", func(t *testing.T) {
		_, err := a.manager.PatchMembers(ctx, admin, "admins", "manual", nil, []string{"u1"})
		wantCode(t, err, 409, errs.CodeLastAdmin)
		_, err = a.manager.PatchUserGroups(ctx, admin, "u1", nil, []string{"admins"})
		wantCode(t, err, 409, errs.CodeLastAdmin)
		wantCode(t, a.manager.DeleteUser(ctx, admin, "u1"), 409, errs.CodeLastAdmin)
		wantCode(t, a.manager.DeleteGroup(ctx, admin, "admins"), 409, errs.CodeLastAdmin)
		if n := e.scalar(`SELECT count(*) FROM group_membership WHERE member_id = $1::uuid`, u1.ID); n != 1 {
			t.Fatal("the refused change was written")
		}
	})
	t.Run("R-AUTHZ-022 a service account that holds admin counts and is guarded the same way", func(t *testing.T) {
		e.bindRole("admin", sa.ID)
		if err := a.manager.DisableUser(ctx, admin, "u1", principal.DisableOptions{}); err != nil {
			t.Fatalf("u1 is no longer the last holder: %v", err)
		}
		wantCode(t, a.manager.DisableServiceAccount(ctx, admin, "robot", false), 409, errs.CodeLastAdmin)
		wantCode(t, a.manager.DeleteServiceAccount(ctx, admin, "robot"), 409, errs.CodeLastAdmin)
	})
	t.Run("R-AUTHZ-022 with the break-glass administrator active the guard steps aside", func(t *testing.T) {
		e.insertBreakGlass("breakglass")
		if err := a.manager.DeleteServiceAccount(ctx, admin, "robot"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestR_AUTH_019_AutoJoinWaitsForARoleBindingOfTheGroup(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "ops"}); err != nil {
		t.Fatal(err)
	}
	g, _ := a.manager.GetGroup(ctx, "ops")
	// A role binding of the group that holds the group's lock and has not
	// committed yet, as authz.Service.BindRole does.
	tx, err := e.conn().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM user_group WHERE principal_id = $1::uuid FOR UPDATE`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO authz_role_binding (role, principal_id) VALUES ('auditor', $1::uuid)`, g.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var uerr error
	go func() {
		defer close(done)
		on := true
		_, uerr = a.manager.UpdateGroup(ctx, admin, "ops", principal.GroupPatch{Expected: errs.AnyRevision, AutoJoin: &on})
	}()
	if !stillRunning(done, 300*time.Millisecond) {
		t.Fatalf("turning on auto-join did not wait for the binding: %v", uerr)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	wantCode(t, uerr, 409, errs.CodeAutoJoinAdminConflict)
	if n := e.scalar(`SELECT count(*) FROM user_group WHERE principal_id = $1::uuid AND auto_join`, g.ID); n != 0 {
		t.Fatal("a group with a role binding became an auto-join group")
	}
}
