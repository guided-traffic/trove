//go:build integration

package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
)

// R-AUTH-019, R-AUTHZ-026: a group that is a member of a project is never turned into an auto-join group, because
// every user is in an auto-join group and the membership would make every user a member of the project. The member write
// refuses the group that is auto-join already; the identity side refuses the reverse, naming the projects.
func TestH565_AMemberGroupIsNotMadeAutoJoin(t *testing.T) {
	e := newEnv(t)
	e.project("web")
	e.project("data")
	a := e.newReplica("a", replicaOpts{valkey: vkDead})
	ctx := context.Background()
	for _, name := range []string{"team-web", "team-free"} {
		if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	var webGroup string
	if err := e.admin.QueryRow(ctx, `SELECT id::text FROM principal WHERE kind = 'group' AND name = 'team-web'`).Scan(&webGroup); err != nil {
		t.Fatal(err)
	}
	e.member("web", webGroup, "maintainer")
	e.member("data", webGroup, "reader")
	autoJoin := func(name string) bool {
		g, err := a.manager.GetGroup(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return g.AutoJoin
	}

	t.Run("a group that is a member of projects answers 409 auto_join_member_conflict and keeps its flag", func(t *testing.T) {
		_, err := a.manager.UpdateGroup(ctx, admin, "team-web", principal.GroupPatch{Expected: errs.AnyRevision, AutoJoin: ptr(true)})
		wantCode(t, err, 409, errs.CodeAutoJoinMemberConflict)
		if !strings.Contains(err.Error(), "data, web") {
			t.Errorf("the refusal does not name the projects: %v", err)
		}
		if autoJoin("team-web") {
			t.Error("the flag was set although the refusal answered")
		}
	})
	t.Run("a description change of the member group and the flag off are not refused", func(t *testing.T) {
		if _, err := a.manager.UpdateGroup(ctx, admin, "team-web", principal.GroupPatch{Expected: errs.AnyRevision, Description: ptr("web"), AutoJoin: ptr(false)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a group that is no member of a project becomes auto-join", func(t *testing.T) {
		if _, err := a.manager.UpdateGroup(ctx, admin, "team-free", principal.GroupPatch{Expected: errs.AnyRevision, AutoJoin: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		if !autoJoin("team-free") {
			t.Error("the flag was not set")
		}
	})
	t.Run("once the memberships are gone the group becomes auto-join", func(t *testing.T) {
		e.exec(`DELETE FROM project_member WHERE principal_id = $1::uuid`, webGroup)
		if _, err := a.manager.UpdateGroup(ctx, admin, "team-web", principal.GroupPatch{Expected: errs.AnyRevision, AutoJoin: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		if !autoJoin("team-web") {
			t.Error("the flag was not set")
		}
	})
}
