//go:build integration

package identity

import (
	"testing"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/token"
)

// Q-AUTH-209: a token and a service account state whether the owner holds the administrator role now, so that the screens can say
// that administrator rights are inactive and offer the choice only where it can take effect. The state is read from the role
// bindings at the time of the read, directly and through a group, for the list and for the single reads.
func TestQ_AUTH_209_TheRecordsStateWhetherTheOwnerIsAnAdministrator(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkLive, auditDB: true})
	fx := e.newFixture(a)
	val := apitest.New(t, v1.EmbeddedYAML())
	s := a.serve(e, val)

	alice := a.user("alice")
	aliceAdminToken := a.pat(alice, func(r *token.CreateRequest) { r.Scope = []byte(`{"inherit":true,"admin":true}`) })
	byOwner := func(name string) map[string]any {
		t.Helper()
		for _, it := range list(t, s.call(get, "/api/v1/tokens?state=all&limit=200", fx.adminToken, nil)) {
			if str(it["owner"].(map[string]any), "name") == name && it["scope"].(map[string]any)["admin"] == true {
				return it
			}
		}
		t.Fatalf("no administrator token of %s in the list", name)
		return nil
	}

	t.Run("a token whose owner holds no administrator role says false, and says true once the role is bound", func(t *testing.T) {
		if got := byOwner("alice")["ownerIsAdmin"]; got != false {
			t.Fatalf("ownerIsAdmin before the role: %v", got)
		}
		if got := byOwner("root")["ownerIsAdmin"]; got != true {
			t.Fatalf("the administrator's own token: %v", got)
		}
		e.bindAdmin(alice.ID)
		if got := byOwner("alice")["ownerIsAdmin"]; got != true {
			t.Fatalf("ownerIsAdmin after the role: %v", got)
		}
		single := s.call(get, "/api/v1/tokens/"+str(byOwner("alice"), "id"), fx.adminToken, nil)
		want(t, single, 200, "")
		if single.json(t)["ownerIsAdmin"] != true {
			t.Fatalf("single read: %s", single.Body)
		}
		current := s.call(get, "/api/v1/tokens/current", aliceAdminToken.Secret, nil)
		want(t, current, 200, "")
		if current.json(t)["ownerIsAdmin"] != true {
			t.Fatalf("current token: %s", current.Body)
		}
	})
	t.Run("the role of a group counts", func(t *testing.T) {
		bob := a.user("bob")
		bobToken := a.pat(bob, func(r *token.CreateRequest) { r.Scope = []byte(`{"inherit":true,"admin":true}`) })
		want(t, s.call(post, "/api/v1/groups", fx.adminToken, map[string]any{"name": "ops"}), 201, "")
		want(t, s.call(patch, "/api/v1/groups/ops/members", fx.adminToken, map[string]any{"add": []string{"bob"}}), 200, "")
		if got := s.call(get, "/api/v1/tokens/current", bobToken.Secret, nil).json(t)["ownerIsAdmin"]; got != false {
			t.Fatalf("before the group holds the role: %v", got)
		}
		e.exec(`INSERT INTO authz_role_binding (role, principal_id) SELECT 'admin', id FROM principal WHERE kind = 'group' AND name = 'ops'`)
		if got := s.call(get, "/api/v1/tokens/current", bobToken.Secret, nil).json(t)["ownerIsAdmin"]; got != true {
			t.Fatalf("after the group holds the role: %v", got)
		}
	})
	t.Run("a service account states whether it holds the role, on the list and on the single read", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "plain-bot"}), 201, "")
		want(t, s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "admin-bot"}), 201, "")
		e.exec(`INSERT INTO authz_role_binding (role, principal_id) SELECT 'admin', id FROM principal WHERE kind = 'service_account' AND name = 'admin-bot'`)
		if got := s.call(get, "/api/v1/service-accounts/plain-bot", fx.adminToken, nil).json(t)["isAdmin"]; got != false {
			t.Fatalf("plain-bot: %v", got)
		}
		if got := s.call(get, "/api/v1/service-accounts/admin-bot", fx.adminToken, nil).json(t)["isAdmin"]; got != true {
			t.Fatalf("admin-bot: %v", got)
		}
		seen := map[string]any{}
		for _, it := range list(t, s.call(get, "/api/v1/service-accounts", fx.adminToken, nil)) {
			seen[str(it, "name")] = it["isAdmin"]
		}
		if seen["plain-bot"] != false || seen["admin-bot"] != true {
			t.Fatalf("list: %v", seen)
		}
	})
}
