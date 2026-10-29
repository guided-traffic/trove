//go:build integration

package identity

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/token"
)

// identityOperations are the operations of the identity area that a test over
// this package's HTTP harness reaches (the login area's are in test/integration/login).
var identityOperations = []string{
	"listUsers", "createUser", "getCurrentUser", "getUser", "updateUser", "deleteUser", "disableUser", "enableUser",
	"patchUserGroups", "unlockBreakGlass",
	"listGroups", "createGroup", "getGroup", "updateGroup", "deleteGroup", "listGroupMembers", "patchGroupMembers",
	"listServiceAccounts", "createServiceAccount", "getServiceAccount", "updateServiceAccount", "deleteServiceAccount",
	"disableServiceAccount", "enableServiceAccount",
	"createToken", "listTokens", "getToken", "getCurrentToken", "revokeToken", "revokeCurrentToken", "rotateToken",
	"rotateCurrentToken", "revokeTokenByValue", "revokeOwnTokens", "revokeUserTokens", "createServiceAccountToken",
	"revokeServiceAccountTokens", "getAuthSettings", "updateAuthSettings", "getPreferences", "updatePreferences", "getWhoami",
}

const (
	get    = http.MethodGet
	post   = http.MethodPost
	patch  = http.MethodPatch
	del    = http.MethodDelete
	idemHd = "Idempotency-Key"
)

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func list(t testing.TB, rp reply) []map[string]any {
	t.Helper()
	if rp.Status != 200 {
		t.Fatalf("list answered %d: %s", rp.Status, rp.Body)
	}
	var out []map[string]any
	for _, it := range rp.json(t)["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func want(t testing.TB, rp reply, status int, code string) {
	t.Helper()
	if rp.Status != status || (code != "" && rp.code() != code) {
		t.Fatalf("want %d %s, got %d %s: %s", status, code, rp.Status, rp.code(), rp.Body)
	}
}

// insertBreakGlass creates the break-glass user row the login area would create.
func (e *env) insertBreakGlass(name string) string {
	e.t.Helper()
	id := fmt.Sprintf("00000000-0000-7000-8000-%012d", 77)
	e.exec(`INSERT INTO principal (id, kind, name) VALUES ($1::uuid, 'user', $2)`, id, name)
	e.exec(`INSERT INTO app_user (principal_id, origin, is_break_glass, display_name) VALUES ($1::uuid, 'system', true, 'Break-glass administrator')`, id)
	return id
}

// TestAPIOperations drives every operation of the identity area over HTTP against
// three replicas, spreading the requests, and validates every answer against
// api/openapi.yaml (R-API-002). It ends by asserting that no operation of the
// area was left untested.
func TestAPIOperations(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{valkey: vkLive, auditDB: true})
	fx := e.newFixture(a)
	bgID := e.insertBreakGlass("breakglass")
	b := e.newReplica("b", replicaOpts{valkey: vkLive, auditDB: true})
	c := e.newReplica("c", replicaOpts{valkey: vkLive, auditDB: true})
	val := apitest.New(t, v1.EmbeddedYAML())
	sa, sb, sc := a.serve(e, val), b.serve(e, val), c.serve(e, val)
	nodes := []*served{sa, sb, sc}
	n := 0
	// next spreads consecutive calls over the replicas (R-ARCH-032).
	next := func() *served { n++; return nodes[n%3] }
	admin := fx.adminToken

	alice := a.user("alice")
	alicePAT := a.pat(alice).Secret

	t.Run("R-AUTH-016 users", func(t *testing.T) {
		rp := next().call(post, "/api/v1/users", admin, map[string]any{
			"username": "carol", "email": "carol@example.com", "displayName": "Carol",
			"identities": []any{map[string]any{"provider": "corp", "subject": nil}},
		}, idemHd, "u-1")
		want(t, rp, 201, "")
		if rp.Header.Get("Location") != baseURL+"/api/v1/users/carol" {
			t.Fatalf("Location %q", rp.Header.Get("Location"))
		}
		u := rp.json(t)
		if u["origin"] != "admin" || u["status"] != "active" || u["isAdmin"] != false {
			t.Fatalf("%v", u)
		}
		if ids := u["identities"].([]any); len(ids) != 1 || ids[0].(map[string]any)["linked"] != false {
			t.Fatalf("a pending link is expected: %v", u["identities"])
		}
		t.Run("a replay answers the stored outcome", func(t *testing.T) {
			rp := next().call(post, "/api/v1/users", admin, map[string]any{
				"username": "carol", "email": "carol@example.com", "displayName": "Carol",
				"identities": []any{map[string]any{"provider": "corp", "subject": nil}},
			}, idemHd, "u-1")
			want(t, rp, 201, "")
			if rp.Header.Get("Idempotency-Replayed") != "true" || str(rp.json(t), "id") != str(u, "id") {
				t.Fatalf("replay: %v %s", rp.Header, rp.Body)
			}
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "carol2"}, idemHd, "u-1"), 422, "idempotency_key_reused")
		})
		t.Run("a name rule or a conflict refuses", func(t *testing.T) {
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "carol"}), 409, "")
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "me"}), 422, "reserved_name")
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "breakglass"}), 422, "reserved_name")
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "Bad Name"}), 400, "username_invalid")
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "dave", "identities": []any{map[string]any{"provider": "nope", "subject": nil}}}), 422, "validation_failed")
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "dave", "identities": []any{map[string]any{"provider": "corp", "subject": "x"}}}), 422, "validation_failed")
			want(t, next().call(post, "/api/v1/users", admin, map[string]any{"username": "dave", "unknown": 1}), 400, "malformed_request")
		})
		t.Run("a read on another replica shows it at once (R-API-052)", func(t *testing.T) {
			for range 3 {
				rp := next().call(get, "/api/v1/users/carol", admin, nil)
				want(t, rp, 200, "")
			}
		})
		t.Run("a caller without identities:read gets 403, existing or not (R-AUTHZ-016)", func(t *testing.T) {
			want(t, next().call(get, "/api/v1/users/carol", alicePAT, nil), 403, "forbidden")
			want(t, next().call(get, "/api/v1/users/nobody", alicePAT, nil), 403, "forbidden")
			want(t, next().call(get, "/api/v1/users", "", nil), 401, "")
		})
		t.Run("the filters", func(t *testing.T) {
			if got := list(t, next().call(get, "/api/v1/users?admin=true", admin, nil)); len(got) != 2 || str(got[0], "username") != "breakglass" || str(got[1], "username") != "root" {
				t.Fatalf("admin=true: %v", got)
			}
			if got := list(t, next().call(get, "/api/v1/users?q=car&origin=admin", admin, nil)); len(got) != 1 || str(got[0], "username") != "carol" {
				t.Fatalf("q: %v", got)
			}
			want(t, next().call(get, "/api/v1/users?status=bogus", admin, nil), 400, "malformed_request")
			want(t, next().call(get, "/api/v1/users?sort=-username", admin, nil), 400, "")
			want(t, next().call(get, "/api/v1/users?cursor=zz", admin, nil), 400, "invalid_cursor")
		})
		t.Run("R-API-010 a walk with limit=1 visits every user once", func(t *testing.T) {
			var seen []string
			path := "/api/v1/users?limit=1"
			for range 10 {
				rp := next().call(get, path, admin, nil)
				for _, it := range list(t, rp) {
					seen = append(seen, str(it, "username"))
				}
				nc, _ := rp.json(t)["nextCursor"].(string)
				if nc == "" {
					break
				}
				path = "/api/v1/users?limit=1&cursor=" + nc
			}
			if strings.Join(seen, ",") != "alice,breakglass,carol,root" {
				t.Fatalf("walk: %v", seen)
			}
		})
		t.Run("the current user", func(t *testing.T) {
			rp := next().call(get, "/api/v1/users/me", alicePAT, nil)
			want(t, rp, 200, "")
			if str(rp.json(t), "username") != "alice" {
				t.Fatalf("%s", rp.Body)
			}
		})
		t.Run("a patch corrects the profile; a body obtained by GET can be sent back", func(t *testing.T) {
			rp := next().call(patch, "/api/v1/users/carol", admin, map[string]any{"displayName": "Carol C", "email": nil}, "If-Match", "*")
			want(t, rp, 200, "")
			u := rp.json(t)
			if u["displayName"] != "Carol C" || u["email"] != nil {
				t.Fatalf("%v", u)
			}
			back := next().call(patch, "/api/v1/users/carol", admin, string(next().call(get, "/api/v1/users/carol", admin, nil).Body), "If-Match", "*")
			want(t, back, 200, "")
			want(t, next().call(patch, "/api/v1/users/carol", admin, map[string]any{"username": "other"}, "If-Match", "*"), 422, "validation_failed")
			want(t, next().call(patch, "/api/v1/users/carol", admin, map[string]any{"nonsense": 1}, "If-Match", "*"), 400, "malformed_request")
			want(t, next().call(patch, "/api/v1/users/nobody", admin, map[string]any{"displayName": "x"}, "If-Match", "*"), 404, "")
			rp = next().call(patch, "/api/v1/users/carol", admin, map[string]any{"identities": []any{}}, "If-Match", "*")
			want(t, rp, 200, "")
		})
		t.Run("R-API-012 a user has an ETag; a missing If-Match is 428, a stale one 412 with the current tag", func(t *testing.T) {
			g := next().call(get, "/api/v1/users/carol", admin, nil)
			want(t, g, 200, "")
			tag := g.Header.Get("ETag")
			if tag == "" {
				t.Fatal("no ETag")
			}
			if _, has := g.json(t)["revision"]; has {
				t.Error("the revision is in the ETag, not in the body")
			}
			want(t, next().call(get, "/api/v1/users/carol", admin, nil, "If-None-Match", tag), 304, "")
			want(t, next().call(patch, "/api/v1/users/carol", admin, map[string]any{"displayName": "R1"}), 428, "precondition_required")
			ok := next().call(patch, "/api/v1/users/carol", admin, map[string]any{"displayName": "R1"}, "If-Match", tag)
			want(t, ok, 200, "")
			if ok.Header.Get("ETag") == tag {
				t.Errorf("the ETag did not change with the write: %s", tag)
			}
			stale := next().call(patch, "/api/v1/users/carol", admin, map[string]any{"displayName": "R2"}, "If-Match", tag)
			want(t, stale, 412, "precondition_failed")
			if stale.json(t)["currentEtag"] != ok.Header.Get("ETag") {
				t.Errorf("currentEtag = %v, want %s", stale.json(t)["currentEtag"], ok.Header.Get("ETag"))
			}
			// A patch that changes nothing keeps the revision.
			same := next().call(patch, "/api/v1/users/carol", admin, map[string]any{"displayName": "R1"}, "If-Match", ok.Header.Get("ETag"))
			want(t, same, 200, "")
			if same.Header.Get("ETag") != ok.Header.Get("ETag") {
				t.Errorf("a no-op patch moved the ETag: %s -> %s", ok.Header.Get("ETag"), same.Header.Get("ETag"))
			}
		})
		t.Run("disable ends access, enable does not restore revoked tokens (R-AUTH-016)", func(t *testing.T) {
			carol, err := c.manager.GetUser(t.Context(), "carol")
			if err != nil {
				t.Fatal(err)
			}
			pat := c.pat(carol)
			// A disable that keeps the tokens: they fail with principal_disabled.
			want(t, next().call(post, "/api/v1/users/carol/disable", admin, map[string]any{"reason": "leave", "revokeTokens": false}), 204, "")
			waitFor(t, "principal_disabled", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", pat.Secret, nil).code() == "principal_disabled"
			})
			want(t, next().call(post, "/api/v1/users/carol/enable", admin, nil), 204, "")
			waitFor(t, "the token working again", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", pat.Secret, nil).Status == 200
			})
			// The default also revokes them; enabling does not bring them back.
			want(t, next().call(post, "/api/v1/users/carol/disable", admin, nil), 204, "")
			want(t, next().call(post, "/api/v1/users/carol/enable", admin, nil), 204, "")
			waitFor(t, "the revoked token", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", pat.Secret, nil).code() == "invalid_token"
			})
			want(t, next().call(post, "/api/v1/users/nobody/disable", admin, nil), 404, "")
			want(t, next().call(post, "/api/v1/users/nobody/enable", admin, nil), 404, "")
		})
		t.Run("the break-glass account cannot be disabled or deleted, and unlocks (R-AUTH-014, R-AUTH-038)", func(t *testing.T) {
			want(t, next().call(post, "/api/v1/users/breakglass/disable", admin, nil), 409, "break_glass_immutable")
			want(t, next().call(del, "/api/v1/users/breakglass", admin, nil), 409, "break_glass_immutable")
			e.exec(`INSERT INTO auth_lockout (account_key, failures, window_started_at, locked_until) VALUES ('break-glass', 5, now(), now() + interval '10 minutes')`)
			u := next().call(get, "/api/v1/users/breakglass", admin, nil).json(t)
			if u["lockedUntil"] == nil || u["isAdmin"] != true {
				t.Fatalf("a locked break-glass account shows lockedUntil: %v", u)
			}
			if got := list(t, next().call(get, "/api/v1/users?status=locked", admin, nil)); len(got) != 1 || str(got[0], "username") != "breakglass" {
				t.Fatalf("status=locked: %v", got)
			}
			want(t, next().call(post, "/api/v1/users/carol/unlock", admin, nil), 404, "")
			want(t, next().call(post, "/api/v1/users/breakglass/unlock", alicePAT, nil), 403, "")
			want(t, next().call(post, "/api/v1/users/breakglass/unlock", admin, nil), 204, "")
			if u := next().call(get, "/api/v1/users/breakglass", admin, nil).json(t); u["lockedUntil"] != nil {
				t.Fatalf("still locked: %v", u)
			}
			if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'auth.breakglass.unlocked'`); n != 1 {
				t.Fatalf("%d auth.breakglass.unlocked events", n)
			}
			_ = bgID
		})
		t.Run("delete removes the user; a new login would be a new principal (R-AUTH-016)", func(t *testing.T) {
			want(t, next().call(del, "/api/v1/users/carol", admin, nil), 204, "")
			want(t, next().call(get, "/api/v1/users/carol", admin, nil), 404, "")
			want(t, next().call(del, "/api/v1/users/carol", admin, nil), 404, "")
		})
	})

	t.Run("R-AUTH-020 service accounts", func(t *testing.T) {
		rp := next().call(post, "/api/v1/service-accounts", admin, map[string]any{"name": "ci-bot", "description": "Deploy jobs"}, idemHd, "sa-1")
		want(t, rp, 201, "")
		want(t, next().call(post, "/api/v1/service-accounts", admin, map[string]any{"name": "ci-bot"}), 409, "")
		want(t, next().call(post, "/api/v1/service-accounts", admin, map[string]any{"name": "X"}), 400, "")
		want(t, next().call(post, "/api/v1/service-accounts", alicePAT, map[string]any{"name": "mine"}), 403, "")
		want(t, next().call(get, "/api/v1/service-accounts/ci-bot", admin, nil), 200, "")
		want(t, next().call(get, "/api/v1/service-accounts/none", admin, nil), 404, "")
		if got := list(t, next().call(get, "/api/v1/service-accounts?q=ci", admin, nil)); len(got) != 1 {
			t.Fatalf("%v", got)
		}
		rp = next().call(patch, "/api/v1/service-accounts/ci-bot", admin, map[string]any{"description": "Deploys"}, "If-Match", "*")
		want(t, rp, 200, "")
		if rp.json(t)["description"] != "Deploys" {
			t.Fatalf("%s", rp.Body)
		}
		want(t, next().call(patch, "/api/v1/service-accounts/ci-bot", admin, map[string]any{"name": "other"}, "If-Match", "*"), 422, "validation_failed")
		sat := next().call(post, "/api/v1/service-accounts/ci-bot/tokens", admin, map[string]any{"description": "pipeline", "ttlSeconds": 3600})
		want(t, sat, 201, "")
		secret := str(sat.json(t), "token")
		want(t, next().call(post, "/api/v1/service-accounts/ci-bot/disable", admin, nil), 204, "")
		waitFor(t, "the disabled account's token failing", revocationBound, func() bool {
			return next().call(get, "/api/v1/auth/whoami", secret, nil).code() == "principal_disabled"
		})
		want(t, next().call(post, "/api/v1/service-accounts/ci-bot/enable", admin, nil), 204, "")
		waitFor(t, "the enabled account's token working", revocationBound, func() bool {
			return next().call(get, "/api/v1/auth/whoami", secret, nil).Status == 200
		})
		want(t, next().call(del, "/api/v1/service-accounts/ci-bot/tokens", admin, nil), 204, "")
		waitFor(t, "the revoked token", revocationBound, func() bool {
			return next().call(get, "/api/v1/auth/whoami", secret, nil).code() == "invalid_token"
		})
		want(t, next().call(del, "/api/v1/service-accounts/ci-bot/tokens", alicePAT, nil), 403, "")
		want(t, next().call(del, "/api/v1/service-accounts/none/tokens", admin, nil), 404, "")
		want(t, next().call(post, "/api/v1/service-accounts/none/disable", admin, nil), 404, "")
		want(t, next().call(post, "/api/v1/service-accounts/none/enable", admin, nil), 404, "")
		want(t, next().call(patch, "/api/v1/service-accounts/none", admin, map[string]any{"description": "x"}, "If-Match", "*"), 404, "")
		want(t, next().call(del, "/api/v1/service-accounts/none", admin, nil), 404, "")
		want(t, next().call(post, "/api/v1/service-accounts", admin, map[string]any{"name": "tmp-bot"}), 201, "")
		want(t, next().call(del, "/api/v1/service-accounts/tmp-bot", admin, nil), 204, "")
		want(t, next().call(get, "/api/v1/service-accounts/tmp-bot", admin, nil), 404, "")
	})

	t.Run("R-AUTH-018 groups and memberships", func(t *testing.T) {
		want(t, next().call(post, "/api/v1/groups", admin, map[string]any{"name": "team-web", "members": []string{"alice", "nobody"}}), 404, "")
		want(t, next().call(get, "/api/v1/groups/team-web", admin, nil), 404, "") // nothing was created
		rp := next().call(post, "/api/v1/groups", admin, map[string]any{"name": "team-web", "description": "Web", "members": []string{"alice", "sa:ci-bot"}}, idemHd, "g-1")
		want(t, rp, 201, "")
		if rp.json(t)["memberCount"] != float64(2) {
			t.Fatalf("%s", rp.Body)
		}
		want(t, next().call(post, "/api/v1/groups", admin, map[string]any{"name": "team-web"}), 409, "")
		want(t, next().call(post, "/api/v1/groups", admin, map[string]any{"name": "Bad"}), 400, "")
		for range 3 { // a membership change is visible on every replica at once
			if got := list(t, next().call(get, "/api/v1/groups/team-web/members", admin, nil)); len(got) != 2 {
				t.Fatalf("members: %v", got)
			}
		}
		// alice is also a member through the provider; removing the manual membership keeps the group.
		e.exec(`INSERT INTO group_membership (group_id, member_id, source) SELECT g.id, u.id, 'oidc:corp' FROM principal g, principal u WHERE g.name = 'team-web' AND g.kind = 'group' AND u.name = 'alice' AND u.kind = 'user'`)
		rp = next().call(patch, "/api/v1/groups/team-web/members", admin, map[string]any{"remove": []string{"alice"}})
		want(t, rp, 200, "")
		it := list(t, rp)
		if len(it) != 1 || fmt.Sprint(it[0]["sources"]) != "[oidc:corp]" {
			t.Fatalf("the remaining source is oidc:corp: %v", it)
		}
		for range 3 {
			ms := list(t, next().call(get, "/api/v1/groups/team-web/members?source=manual", admin, nil))
			if len(ms) != 1 || str(ms[0], "principal") != "sa:ci-bot" {
				t.Fatalf("manual members: %v", ms)
			}
		}
		want(t, next().call(patch, "/api/v1/groups/team-web/members", admin, map[string]any{"add": []string{"authenticated"}}), 404, "")
		want(t, next().call(patch, "/api/v1/groups/team-web/members", admin, map[string]any{"add": []string{"bob-unknown", "alice"}}), 404, "")
		want(t, next().call(patch, "/api/v1/groups/team-web/members", admin, map[string]any{"source": "scim"}), 400, "validation_failed")
		want(t, next().call(patch, "/api/v1/groups/nope/members", admin, map[string]any{"add": []string{"alice"}}), 404, "")
		want(t, next().call(patch, "/api/v1/users/alice/groups", admin, map[string]any{"add": []string{"team-web"}}), 200, "")
		want(t, next().call(patch, "/api/v1/users/alice/groups", admin, map[string]any{"add": []string{"nope"}}), 404, "")
		ug := next().call(get, "/api/v1/users/alice", admin, nil).json(t)
		if fmt.Sprint(ug["groups"]) != "[map[name:team-web source:manual] map[name:team-web source:oidc:corp]]" {
			t.Fatalf("alice's groups: %v", ug["groups"])
		}
		t.Run("the member walk with limit=1", func(t *testing.T) {
			var seen []string
			path := "/api/v1/groups/team-web/members?limit=1"
			for range 10 {
				rp := next().call(get, path, admin, nil)
				for _, it := range list(t, rp) {
					seen = append(seen, str(it, "principal"))
				}
				nc, _ := rp.json(t)["nextCursor"].(string)
				if nc == "" {
					break
				}
				path = "/api/v1/groups/team-web/members?limit=1&cursor=" + nc
			}
			if strings.Join(seen, ",") != "sa:ci-bot,alice" {
				t.Fatalf("walk: %v", seen)
			}
		})
		rp = next().call(patch, "/api/v1/groups/team-web", admin, map[string]any{"description": "Web team", "autoJoin": true}, "If-Match", "*")
		want(t, rp, 200, "")
		if g := rp.json(t); g["autoJoin"] != true || g["description"] != "Web team" {
			t.Fatalf("%v", g)
		}
		want(t, next().call(patch, "/api/v1/groups/team-web", admin, map[string]any{"name": nil}, "If-Match", "*"), 422, "validation_failed")
		want(t, next().call(patch, "/api/v1/groups/team-web", admin, map[string]any{"name": "team-frontend"}, "If-Match", "*"), 200, "")
		want(t, next().call(get, "/api/v1/groups/team-web", admin, nil), 404, "")
		g := next().call(get, "/api/v1/groups/team-frontend", admin, nil)
		want(t, g, 200, "")
		if got := list(t, next().call(get, "/api/v1/groups?origin=admin&q=team", admin, nil)); len(got) != 1 {
			t.Fatalf("%v", got)
		}
		t.Run("a group with an administration role takes no auto-join (R-AUTH-019)", func(t *testing.T) {
			want(t, next().call(post, "/api/v1/groups", admin, map[string]any{"name": "platform-admins"}), 201, "")
			e.exec(`INSERT INTO authz_role_binding (role, principal_id) SELECT 'admin', id FROM principal WHERE kind = 'group' AND name = 'platform-admins'`)
			want(t, next().call(patch, "/api/v1/groups/platform-admins", admin, map[string]any{"autoJoin": true}, "If-Match", "*"), 409, "auto_join_admin_conflict")
			if r := list(t, next().call(get, "/api/v1/groups", admin, nil)); len(r) != 2 {
				t.Fatalf("%v", r)
			}
			if roles := next().call(get, "/api/v1/groups/platform-admins", admin, nil).json(t)["roles"]; fmt.Sprint(roles) != "[admin]" {
				t.Fatalf("roles: %v", roles)
			}
		})
		t.Run("a group owned by SCIM is read only (R-AUTH-018)", func(t *testing.T) {
			e.exec(`WITH p AS (INSERT INTO principal (id, kind, name) VALUES ('00000000-0000-7000-8000-000000000099', 'group', 'idp-owned') RETURNING id)
			        INSERT INTO user_group (principal_id, origin) SELECT id, 'scim' FROM p`)
			want(t, next().call(patch, "/api/v1/groups/idp-owned", admin, map[string]any{"description": "x"}, "If-Match", "*"), 409, "managed_by_scim")
			want(t, next().call(patch, "/api/v1/groups/idp-owned/members", admin, map[string]any{"add": []string{"alice"}}), 409, "managed_by_scim")
			want(t, next().call(del, "/api/v1/groups/idp-owned", admin, nil), 409, "managed_by_scim")
		})
		want(t, next().call(del, "/api/v1/groups/team-frontend", admin, nil), 204, "")
		want(t, next().call(del, "/api/v1/groups/team-frontend", admin, nil), 404, "")
	})

	t.Run("R-AUTH-027 to R-AUTH-030 tokens", func(t *testing.T) {
		rp := next().call(post, "/api/v1/tokens", alicePAT, map[string]any{"description": "child", "ttlSeconds": 3600, "scope": map[string]any{"inherit": true}}, idemHd, "t-1")
		want(t, rp, 201, "")
		if cc := rp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("Cache-Control %q", cc)
		}
		child := rp.json(t)
		childSecret := str(child, "token")
		if !strings.HasPrefix(childSecret, "trv_pat_") || str(child, "type") != "pat" {
			t.Fatalf("%v", child)
		}
		t.Run("the value is shown once; a replay of the create is a 409 with the token ID", func(t *testing.T) {
			rp := next().call(post, "/api/v1/tokens", alicePAT, map[string]any{"description": "child", "ttlSeconds": 3600, "scope": map[string]any{"inherit": true}}, idemHd, "t-1")
			want(t, rp, 409, "token_already_issued")
			if rp.json(t)["tokenId"] != child["id"] {
				t.Fatalf("%s", rp.Body)
			}
			if strings.Contains(string(rp.Body), childSecret) {
				t.Fatal("the replay carries the token value")
			}
			if n := e.scalar(`SELECT count(*) FROM api_token WHERE description = 'child'`); n != 1 {
				t.Fatalf("%d tokens named child", n)
			}
		})
		t.Run("no read returns the value", func(t *testing.T) {
			for _, path := range []string{"/api/v1/tokens/" + str(child, "id"), "/api/v1/tokens", "/api/v1/tokens/current"} {
				rp := next().call(get, path, childSecret, nil)
				want(t, rp, 200, "")
				if strings.Contains(string(rp.Body), childSecret) || strings.Contains(string(rp.Body), "secret") {
					t.Fatalf("%s returned secret material: %s", path, rp.Body)
				}
			}
		})
		t.Run("the derivation rule", func(t *testing.T) {
			want(t, next().call(post, "/api/v1/tokens", childSecret, map[string]any{"description": "x", "ttlSeconds": 7200}), 403, "derivation_exceeds_parent")
			want(t, next().call(post, "/api/v1/tokens", childSecret, map[string]any{"description": "x", "scope": map[string]any{"admin": true}}), 403, "derivation_exceeds_parent")
			want(t, next().call(post, "/api/v1/tokens", alicePAT, map[string]any{"description": "x", "owner": "bob"}), 403, "forbidden")
			want(t, next().call(post, "/api/v1/tokens", admin, map[string]any{"description": "x", "owner": "alice"}), 403, "forbidden")
			want(t, next().call(post, "/api/v1/tokens", alicePAT, map[string]any{"description": "", "ttlSeconds": 3600}), 400, "description_required")
			want(t, next().call(post, "/api/v1/tokens", alicePAT, map[string]any{"description": "x", "ttlSeconds": 5}), 400, "invalid_ttl")
			want(t, next().call(post, "/api/v1/tokens", alicePAT, map[string]any{"description": "x", "scope": map[string]any{"inherit": true, "repositories": []any{map[string]any{"repositories": []string{"a"}, "actions": []string{"read"}}}}}), 400, "invalid_scope")
			want(t, next().call(post, "/api/v1/tokens", "", map[string]any{"description": "x"}), 401, "")
			want(t, next().call(post, "/api/v1/tokens", alicePAT, `{"description": "x", "bogus": 1}`), 400, "malformed_request")
		})
		t.Run("a list shows own tokens; a token of another user is a 404 (R-AUTH-028)", func(t *testing.T) {
			bob := b.user("bob")
			bobPAT := b.pat(bob)
			mine := list(t, next().call(get, "/api/v1/tokens?owner=bob", alicePAT, nil)) // forced to me
			for _, tk := range mine {
				if tk["owner"].(map[string]any)["name"] != "alice" {
					t.Fatalf("alice sees %v", tk)
				}
			}
			want(t, next().call(get, "/api/v1/tokens/"+bobPAT.Record.ID, alicePAT, nil), 404, "")
			want(t, next().call(del, "/api/v1/tokens/"+bobPAT.Record.ID, alicePAT, nil), 404, "")
			want(t, next().call(post, "/api/v1/tokens/"+bobPAT.Record.ID+"/rotate", alicePAT, nil), 404, "")
			all := list(t, next().call(get, "/api/v1/tokens?state=all", admin, nil))
			if len(all) < 3 {
				t.Fatalf("the administrator sees every token: %v", all)
			}
			if got := list(t, next().call(get, "/api/v1/tokens?owner=bob&type=pat", admin, nil)); len(got) != 1 {
				t.Fatalf("owner=bob: %v", got)
			}
			if got := list(t, next().call(get, "/api/v1/tokens?owner=nobody", admin, nil)); len(got) != 0 {
				t.Fatalf("owner=nobody: %v", got)
			}
			want(t, next().call(get, "/api/v1/tokens?type=x", admin, nil), 400, "")
			want(t, next().call(get, "/api/v1/tokens?expiresBefore=yesterday", admin, nil), 400, "")
			want(t, next().call(get, "/api/v1/tokens?cursor=zz", admin, nil), 400, "invalid_cursor")
			// A walk with limit=1 over all tokens ends and sees each once.
			seen := map[string]bool{}
			path := "/api/v1/tokens?state=all&limit=1"
			for range 20 {
				rp := next().call(get, path, admin, nil)
				for _, it := range list(t, rp) {
					if seen[str(it, "id")] {
						t.Fatalf("token %s twice", str(it, "id"))
					}
					seen[str(it, "id")] = true
				}
				nc, _ := rp.json(t)["nextCursor"].(string)
				if nc == "" {
					break
				}
				path = "/api/v1/tokens?state=all&limit=1&cursor=" + nc
			}
			if len(seen) != len(all) {
				t.Fatalf("walk saw %d of %d tokens", len(seen), len(all))
			}
			// An administrator revokes another user's PAT but cannot rotate it.
			want(t, next().call(post, "/api/v1/tokens/"+bobPAT.Record.ID+"/rotate", admin, nil), 403, "forbidden")
			want(t, next().call(del, "/api/v1/tokens/"+bobPAT.Record.ID, admin, nil), 204, "")
			want(t, next().call(del, "/api/v1/tokens/"+bobPAT.Record.ID, admin, nil), 204, "") // idempotent
			waitFor(t, "bob's revoked token", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", bobPAT.Secret, nil).code() == "invalid_token"
			})
			want(t, next().call(get, "/api/v1/tokens/"+uuidNone, admin, nil), 404, "")
			want(t, next().call(get, "/api/v1/tokens/not-a-uuid", admin, nil), 404, "")
		})
		t.Run("rotation (R-AUTH-030)", func(t *testing.T) {
			one := int64(3600)
			own := a.pat(alice, func(r *token.CreateRequest) { r.TTL = token.TTLRequest{Seconds: &one} })
			rp := next().call(post, "/api/v1/tokens/"+own.Record.ID+"/rotate", alicePAT, map[string]any{"overlapSeconds": 3600}, idemHd, "r-1")
			want(t, rp, 201, "")
			rot := rp.json(t)
			if rot["rotatedFrom"] != own.Record.ID || rp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("%v", rot)
			}
			want(t, next().call(post, "/api/v1/tokens/"+own.Record.ID+"/rotate", alicePAT, map[string]any{"overlapSeconds": 3600}, idemHd, "r-1"), 409, "token_already_issued")
			for _, sec := range []string{own.Secret, str(rot, "token")} { // both work during the overlap
				want(t, next().call(get, "/api/v1/auth/whoami", sec, nil), 200, "")
			}
			want(t, next().call(post, "/api/v1/tokens/"+own.Record.ID+"/rotate", alicePAT, map[string]any{"overlapSeconds": 100000}), 422, "validation_failed")
			// The scope of a presenting token bounds a rotation.
			narrow := a.pat(alice, func(r *token.CreateRequest) {
				r.Scope = []byte(`{"inherit":false,"repositories":[{"repositories":["npm-local"],"actions":["read"]}]}`)
			})
			want(t, next().call(post, "/api/v1/tokens/"+rot["id"].(string)+"/rotate", narrow.Secret, nil), 403, "derivation_exceeds_parent")
			// A token rotates itself only when it was created with selfRotatable.
			want(t, next().call(post, "/api/v1/tokens/current/rotate", alicePAT, nil), 403, "forbidden")
			selfy := a.pat(alice, func(r *token.CreateRequest) { r.SelfRotatable = true })
			rp = next().call(post, "/api/v1/tokens/current/rotate", selfy.Secret, nil)
			want(t, rp, 201, "")
			want(t, next().call(post, "/api/v1/tokens/current/rotate", "", nil), 401, "")
			waitFor(t, "the rotated predecessor", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", selfy.Secret, nil).code() == "invalid_token"
			})
		})
		t.Run("revocation (R-AUTH-029)", func(t *testing.T) {
			p := a.pat(alice)
			derived := next().call(post, "/api/v1/tokens", p.Secret, map[string]any{"description": "derived", "ttlSeconds": 600})
			want(t, derived, 201, "")
			dSecret := str(derived.json(t), "token")
			// By value, without credentials: 202, and the child is revoked with it.
			want(t, next().call(post, "/api/v1/tokens/revoke", "", map[string]any{"token": p.Secret}), 202, "")
			want(t, next().call(post, "/api/v1/tokens/revoke", "", map[string]any{"token": "trv_pat_not-a-token"}), 400, "")
			want(t, next().call(post, "/api/v1/tokens/revoke", "", map[string]any{"token": unknownToken(t)}), 202, "") // well-formed but unknown: the same answer
			for _, sec := range []string{p.Secret, dSecret} {
				waitFor(t, "the revoked token", revocationBound, func() bool {
					return next().call(get, "/api/v1/auth/whoami", sec, nil).code() == "invalid_token"
				})
			}
			// An administrator learns the owner.
			p2 := a.pat(alice)
			rp := next().call(post, "/api/v1/tokens/revoke", admin, map[string]any{"token": p2.Secret})
			want(t, rp, 200, "")
			if rp.json(t)["owner"] != "alice" || rp.json(t)["id"] != p2.Record.ID {
				t.Fatalf("%s", rp.Body)
			}
			// Current: any token revokes itself; the others stay.
			s1, s2 := a.pat(alice), a.pat(alice)
			want(t, next().call(del, "/api/v1/tokens/current", s1.Secret, nil), 204, "")
			want(t, next().call(del, "/api/v1/tokens/current", "", nil), 401, "")
			waitFor(t, "the self-revoked token", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", s1.Secret, nil).code() == "invalid_token"
			})
			want(t, next().call(get, "/api/v1/auth/whoami", s2.Secret, nil), 200, "")
			// All own tokens, and all tokens of a user.
			want(t, next().call(del, "/api/v1/users/me/tokens", s2.Secret, nil), 204, "")
			waitFor(t, "the own tokens revoked", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", alicePAT, nil).code() == "invalid_token"
			})
			carl := c.user("carl")
			carlPAT := c.pat(carl)
			want(t, next().call(del, "/api/v1/users/carl/tokens", alicePAT, nil), 401, "")
			want(t, next().call(del, "/api/v1/users/carl/tokens", admin, nil), 204, "")
			want(t, next().call(del, "/api/v1/users/nobody/tokens", admin, nil), 404, "")
			waitFor(t, "carl's tokens revoked", revocationBound, func() bool {
				return next().call(get, "/api/v1/auth/whoami", carlPAT.Secret, nil).code() == "invalid_token"
			})
			// A service-account token is not a user's: it has no user tokens to revoke.
			s := a.serviceAccount("rev-bot")
			sat := a.sat(s)
			want(t, next().call(del, "/api/v1/users/me/tokens", sat.Secret, nil), 403, "")
			want(t, next().call(get, "/api/v1/tokens/current", "", nil), 401, "")
			want(t, next().call(del, "/api/v1/tokens/current", sat.Secret, nil), 204, "")
		})
		t.Run("a service account's own token creates under the derivation rule (R-AUTH-027)", func(t *testing.T) {
			s := a.serviceAccount("deriver")
			parent := a.sat(s, func(r *token.CreateRequest) { r.TTL = token.TTLRequest{Seconds: new(int64)}; *r.TTL.Seconds = 7200 })
			want(t, next().call(post, "/api/v1/service-accounts/deriver/tokens", parent.Secret, map[string]any{"description": "child", "ttlSeconds": 3600}), 201, "")
			want(t, next().call(post, "/api/v1/service-accounts/deriver/tokens", parent.Secret, map[string]any{"description": "longer", "ttlSeconds": 100000}), 403, "derivation_exceeds_parent")
			want(t, next().call(post, "/api/v1/service-accounts/ci-bot/tokens", parent.Secret, map[string]any{"description": "other"}), 403, "forbidden")
			want(t, next().call(post, "/api/v1/service-accounts/deriver/tokens", "", map[string]any{"description": "x"}), 401, "")
			want(t, next().call(post, "/api/v1/service-accounts/nobody/tokens", admin, map[string]any{"description": "x"}), 404, "")
			// A non-expiring SAT needs the administrator and the switch (off by default).
			want(t, next().call(post, "/api/v1/service-accounts/deriver/tokens", admin, map[string]any{"description": "forever", "ttlSeconds": 0}), 400, "non_expiring_not_allowed")
		})
	})

	t.Run("R-AUTH-040 runtime authentication settings", func(t *testing.T) {
		alicePAT := a.pat(alice).Secret
		rp := next().call(get, "/api/v1/settings/auth", admin, nil)
		want(t, rp, 200, "")
		etag := rp.Header.Get("ETag")
		if etag == "" {
			t.Fatal("no ETag")
		}
		if rp.json(t)["anonymousAccess"] != false {
			t.Fatalf("%s", rp.Body)
		}
		nm := next().call(get, "/api/v1/settings/auth", admin, nil, "If-None-Match", etag)
		if nm.Status != 304 {
			t.Fatalf("If-None-Match: %d", nm.Status)
		}
		want(t, next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"anonymousAccess": true}), 428, "precondition_required")
		want(t, next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"anonymousAccess": true}, "If-Match", `"9999"`), 412, "precondition_failed")
		want(t, next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"anonymousAccess": true}, "If-Match", "bogus"), 400, "invalid_header")
		want(t, next().call(get, "/api/v1/settings/auth", alicePAT, nil), 403, "")
		want(t, next().call(patch, "/api/v1/settings/auth", alicePAT, map[string]any{"anonymousAccess": true}, "If-Match", "*"), 403, "")
		want(t, next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"tokens": map[string]any{"pat": map[string]any{"maxTTLSeconds": 99999999999}}}, "If-Match", etag), 400, "exceeds_ceiling")
		want(t, next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"tokens": map[string]any{"pat": map[string]any{"defaultTTLSeconds": 7000000, "maxTTLSeconds": 3600}}}, "If-Match", etag), 400, "default_above_maximum")
		rp = next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"anonymousAccess": true, "tokens": map[string]any{"pat": map[string]any{"maxTTLSeconds": 86400, "defaultTTLSeconds": 3600}}}, "If-Match", etag)
		want(t, rp, 200, "")
		if rp.Header.Get("ETag") == etag || rp.json(t)["anonymousAccess"] != true {
			t.Fatalf("%v %s", rp.Header, rp.Body)
		}
		want(t, next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"anonymousAccess": false}, "If-Match", etag), 412, "precondition_failed")
		for range 3 { // the change is visible on every replica
			g := next().call(get, "/api/v1/settings/auth", admin, nil).json(t)
			if g["anonymousAccess"] != true || g["tokens"].(map[string]any)["pat"].(map[string]any)["maxTTLSeconds"] != float64(86400) {
				t.Fatalf("%v", g)
			}
		}
		back := next().call(get, "/api/v1/settings/auth", admin, nil)
		want(t, next().call(patch, "/api/v1/settings/auth", admin, string(back.Body), "If-Match", "*"), 200, "")
		now := next().call(get, "/api/v1/settings/auth", admin, nil)
		if now.Header.Get("ETag") == back.Header.Get("ETag") {
			t.Fatal("a change through the body of a GET left the ETag as it was")
		}
		want(t, next().call(patch, "/api/v1/settings/auth", admin, map[string]any{"anonymousAccess": false}, "If-Match", now.Header.Get("ETag")), 200, "")
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'settings.auth.changed'`); n < 3 {
			t.Fatalf("%d settings.auth.changed events", n)
		}
	})

	t.Run("R-API-021 whoami and preferences", func(t *testing.T) {
		rp := next().call(get, "/api/v1/auth/whoami", "", nil)
		want(t, rp, 401, "")
		p := a.pat(alice)
		w := next().call(get, "/api/v1/auth/whoami", p.Secret, nil)
		want(t, w, 200, "")
		cred := w.json(t)["credential"].(map[string]any)
		if cred["type"] != "pat" || cred["tokenId"] != p.Record.ID || cred["expiresAt"] == nil {
			t.Fatalf("%s", w.Body)
		}
		if pr := w.json(t)["principal"].(map[string]any); pr["name"] != "alice" || pr["kind"] != "user" {
			t.Fatalf("%s", w.Body)
		}
		// Preferences.
		g := next().call(get, "/api/v1/users/me/preferences", p.Secret, nil)
		want(t, g, 200, "")
		if g.Header.Get("ETag") != `"1"` || g.json(t)["revision"] != float64(1) || g.json(t)["locale"] != nil {
			t.Fatalf("%v %s", g.Header, g.Body)
		}
		want(t, next().call(patch, "/api/v1/users/me/preferences", p.Secret, map[string]any{"locale": "de"}), 428, "precondition_required")
		want(t, next().call(patch, "/api/v1/users/me/preferences", p.Secret, map[string]any{"locale": "de"}, "If-Match", `"7"`), 412, "precondition_failed")
		rp = next().call(patch, "/api/v1/users/me/preferences", p.Secret, map[string]any{"locale": "de", "keyboardShortcuts": true}, "If-Match", `"1"`)
		want(t, rp, 200, "")
		if rp.Header.Get("ETag") != `"2"` || rp.json(t)["locale"] != "de" || rp.json(t)["revision"] != float64(2) {
			t.Fatalf("%v %s", rp.Header, rp.Body)
		}
		want(t, next().call(patch, "/api/v1/users/me/preferences", p.Secret, map[string]any{"theme": "dark"}, "If-Match", `"1"`), 412, "precondition_failed")
		want(t, next().call(patch, "/api/v1/users/me/preferences", p.Secret, map[string]any{"theme": "blue"}, "If-Match", `"2"`), 422, "validation_failed")
		want(t, next().call(patch, "/api/v1/users/me/preferences", p.Secret, map[string]any{"bogus": 1}, "If-Match", `"2"`), 400, "malformed_request")
		rp = next().call(patch, "/api/v1/users/me/preferences", p.Secret, map[string]any{"locale": nil, "theme": "dark"}, "If-Match", "*")
		want(t, rp, 200, "")
		if rp.json(t)["locale"] != nil || rp.json(t)["theme"] != "dark" || rp.json(t)["revision"] != float64(3) {
			t.Fatalf("%s", rp.Body)
		}
		if nm := next().call(get, "/api/v1/users/me/preferences", p.Secret, nil, "If-None-Match", `"3"`); nm.Status != 304 {
			t.Fatalf("If-None-Match: %d", nm.Status)
		}
		// A service account has no preferences.
		s := a.serviceAccount("pref-bot")
		sat := a.sat(s)
		want(t, next().call(get, "/api/v1/users/me/preferences", sat.Secret, nil), 403, "")
		want(t, next().call(patch, "/api/v1/users/me/preferences", sat.Secret, map[string]any{"theme": "dark"}, "If-Match", "*"), 403, "")
		want(t, next().call(get, "/api/v1/users/me", sat.Secret, nil), 403, "")
		wc := next().call(get, "/api/v1/auth/whoami", sat.Secret, nil)
		if wc.json(t)["credential"].(map[string]any)["type"] != "sat" || wc.json(t)["principal"].(map[string]any)["name"] != "pref-bot" {
			t.Fatalf("%s", wc.Body)
		}
	})

	t.Run("E1.5 the audit trail records the changes without a secret", func(t *testing.T) {
		for _, a := range []struct {
			action string
			min    int64
		}{
			{"user.created", 1},
			{"user.disabled", 1},
			{"user.deleted", 1},
			{"group.created", 1},
			{"group.members.changed", 2},
			{"serviceaccount.created", 1},
			{"token.created", 3},
			{"token.revoked", 3},
			{"token.rotated", 1},
			{"settings.auth.changed", 3},
		} {
			if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = $1`, a.action); n < a.min {
				t.Errorf("%s: %d events, want at least %d", a.action, n, a.min)
			}
		}
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE details::text ~ 'trv_(pat|sat|cit)_[0-9A-Za-z]{20}'`); n != 0 {
			t.Errorf("%d audit events carry a token value", n)
		}
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'token.revoked' AND reason = 'leaked'`); n < 2 {
			t.Errorf("%d token.revoked events with reason leaked", n)
		}
		if n := e.scalar(`SELECT count(*) FROM audit_event WHERE action = 'token.revoked' AND reason = 'parent_revoked'`); n < 1 {
			t.Errorf("no token.revoked with reason parent_revoked")
		}
	})

	// E1.6, Q-AUTH-50: the convention suite on the three identity resources, now that they carry
	// a revision: ETag and 304, PATCH needs If-Match (428), a stale tag is 412 with currentEtag, merge
	// patch, cursor paging and idempotent creates, every request answered by another replica.
	contractDo := func(req *http.Request) (*http.Response, error) {
		node := next()
		req.URL.Scheme, req.URL.Host = "http", node.srv.Listener.Addr().String()
		req.Host = "trove.test"
		req.Header.Set("Authorization", "Bearer "+admin)
		return http.DefaultClient.Do(req)
	}
	unique := fmt.Sprintf("%x", time.Now().UnixNano()%0xffffff)
	for _, res := range []apitest.Resource{
		{
			Name: "users", Collection: baseURL + "/api/v1/users",
			Create: func(n int) map[string]any {
				return map[string]any{"username": fmt.Sprintf("ct%s-%d", unique, n), "displayName": "Contract"}
			},
			Key:   func(m map[string]any) string { return m["username"].(string) },
			Patch: map[string]any{"displayName": "patched"}, PatchMember: "displayName", PatchValue: "patched",
			AltPatch:    map[string]any{"displayName": "patched again"},
			ClearMember: "displayName", ClearedValue: nil,
			ImmutableMember: "username", ImmutableValue: "someone-else",
			OtherQuery: "origin=admin",
		},
		{
			Name: "groups", Collection: baseURL + "/api/v1/groups",
			Create: func(n int) map[string]any {
				return map[string]any{"name": fmt.Sprintf("ct%s-%d", unique, n), "description": "Contract"}
			},
			Key:   func(m map[string]any) string { return m["name"].(string) },
			Patch: map[string]any{"description": "patched"}, PatchMember: "description", PatchValue: "patched",
			AltPatch:    map[string]any{"description": "patched again"},
			ClearMember: "description", ClearedValue: "",
			OtherQuery: "origin=admin",
		},
		{
			Name: "service-accounts", Collection: baseURL + "/api/v1/service-accounts",
			Create: func(n int) map[string]any {
				return map[string]any{"name": fmt.Sprintf("ct%s-%d", unique, n), "description": "Contract"}
			},
			Key:   func(m map[string]any) string { return m["name"].(string) },
			Patch: map[string]any{"description": "patched"}, PatchMember: "description", PatchValue: "patched",
			AltPatch:    map[string]any{"description": "patched again"},
			ClearMember: "description", ClearedValue: "",
			ImmutableMember: "name", ImmutableValue: "someone-else",
			OtherQuery: "q=zzz",
		},
	} {
		res.Do = contractDo
		apitest.RunContract(t, res)
	}

	if missing := val.Uncovered(identityOperations...); len(missing) > 0 {
		t.Errorf("R-API-002 operations no test reached: %v", missing)
	}
}

const uuidNone = "00000000-0000-7000-8000-0000000000ff"

// unknownToken is a well-formed token value that no row holds.
func unknownToken(t testing.TB) string {
	t.Helper()
	s, err := token.Generate(token.TypePAT)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
