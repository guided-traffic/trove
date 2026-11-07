//go:build integration

package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The rule of x-trove-owner-rights (R-API-021, Q-API-214) on the running composition root: real tokens through the real
// authentication stage and the shared authorization step. A token whose scope does not inherit its owner's rights does not
// change the owner's preferences, sessions or tokens; a session and an inheriting token do. The avatar routes are asserted by
// TestR_API_055_AvatarRefusals.

// narrowScopes are the scopes of tokens limited to part of the owner's rights.
var narrowScopes = map[string]scope.Scope{
	"one capability":              {System: []string{"settings:read"}},
	"one repository":              {Repositories: []scope.Grant{{Repositories: []string{testenv.DefaultProject + "/npm-local"}, Actions: []string{"read"}}}},
	"admin: true without inherit": {Admin: true},
}

// revocationBound is auth.revocation_bound at its default: a revoked token is refused on every replica within it (R-AUTH-023).
const revocationBound = 2 * time.Second

// browserOrigin is the origin of the default endpoint's base URL, which a session write must send (R-AUTH-012).
const browserOrigin = "http://" + testenv.DefaultHost

// browserSession inserts a live session of the user as a sign-in leaves it, and returns its ID and the headers of a request
// of that browser: the session cookie, the CSRF token and the origin.
func (e *env) browserSession(u testenv.Person) (string, []string) {
	e.t.Helper()
	id, secret, csrf := session.NewID(), session.NewSecret(), session.NewSecret()
	e.exec(`INSERT INTO ui_session (id, secret_sha256, csrf_sha256, principal_id, auth_method, authenticated_at, idle_timeout,
	                                absolute_expires_at, created_addr, user_agent)
	        VALUES ($1::uuid, $2, $3, $4::uuid, 'oidc', now(), interval '1 hour', now() + interval '12 hours', '192.0.2.7', 'it-browser')`,
		id, session.Hash(secret), session.Hash(csrf), u.ID)
	return id, []string{"Cookie", session.CookieSession + "=" + secret, "X-CSRF-Token", csrf, "Origin", browserOrigin}
}

// liveSessions counts the sessions of a user that have not ended.
func (e *env) liveSessions(u testenv.Person) int64 {
	e.t.Helper()
	return e.scalar("SELECT count(*) FROM ui_session WHERE principal_id = $1::uuid AND ended_at IS NULL", u.ID)
}

// refused fails unless the answer is the 403 of a token limited to part of its owner's rights.
func refused(t *testing.T, what string, a testenv.Answer) {
	t.Helper()
	if a.Status != http.StatusForbidden || a.Code(t) != "forbidden" || !strings.Contains(string(a.Body), "limited to part of its owner's rights") {
		t.Errorf("%s: %d %s, want 403 forbidden for a token limited to part of its owner's rights", what, a.Status, a.Body)
	}
}

func TestR_API_021_ANarrowedTokenDoesNotChangeItsOwnersAccount(t *testing.T) {
	const password = "correct horse battery staple 1"
	e := newEnv(t, true)
	erin, frank, gina := e.stack.User("erin"), e.stack.User("frank"), e.stack.User("gina")
	bot := e.stack.ServiceAccount("deploy-bot")
	file := testenv.WriteFile(t, t.TempDir(), "break-glass", password)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "own-account", Role: "api", Env: []string{
		"TROVE_AUTH_BREAK_GLASS_ENABLED=true", "TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE=" + file,
	}})
	narrowOf := func(u testenv.Person) map[string]string {
		out := map[string]string{}
		for name, sc := range narrowScopes {
			out[name] = e.stack.Token(u, sc)
		}
		return out
	}
	patchPrefs := func(token, ifMatch string, hdr ...string) testenv.Answer {
		h := append([]string{"Content-Type", "application/merge-patch+json"}, hdr...)
		if ifMatch != "" {
			h = append(h, "If-Match", ifMatch)
		}
		return e.stack.Do(r, "PATCH", "/api/v1/users/me/preferences", token, map[string]string{"locale": "de"}, h...)
	}

	t.Run("R-API-021 updatePreferences: a narrowed token is refused, also without If-Match, and nothing changes", func(t *testing.T) {
		inherit := e.stack.Token(erin, scope.Default())
		for name, tok := range narrowOf(erin) {
			refused(t, name+" with If-Match", patchPrefs(tok, `"1"`))
			refused(t, name+" without If-Match", patchPrefs(tok, ""))
		}
		got := e.stack.Do(r, "GET", "/api/v1/users/me/preferences", inherit, nil)
		if m := got.JSON(t); got.Status != http.StatusOK || m["revision"] != float64(1) || m["locale"] != nil {
			t.Fatalf("after the refusals: %d %s, want revision 1 and no locale", got.Status, got.Body)
		}
		if n := e.scalar("SELECT count(*) FROM trove.user_preferences WHERE user_id = $1::uuid", erin.ID); n != 0 {
			t.Fatalf("%d preference rows after the refusals", n)
		}
		ok := patchPrefs(inherit, `"1"`)
		if m := ok.JSON(t); ok.Status != http.StatusOK || m["locale"] != "de" || m["revision"] == float64(1) {
			t.Fatalf("the inheriting token: %d %s", ok.Status, ok.Body)
		}
	})

	t.Run("R-API-021 the own sessions: a narrowed token neither lists nor ends them", func(t *testing.T) {
		inherit := e.stack.Token(frank, scope.Default())
		first, _ := e.browserSession(frank)
		_, browser := e.browserSession(frank)
		for name, tok := range narrowOf(frank) {
			refused(t, name+" lists", e.stack.Do(r, "GET", "/api/v1/users/me/sessions", tok, nil))
			refused(t, name+" ends one", e.stack.Do(r, "DELETE", "/api/v1/users/me/sessions/"+first, tok, nil))
			refused(t, name+" ends all", e.stack.Do(r, "DELETE", "/api/v1/users/me/sessions", tok, nil))
			refused(t, name+" ends all but the current", e.stack.Do(r, "DELETE", "/api/v1/users/me/sessions?keepCurrent=true", tok, nil))
		}
		if n := e.liveSessions(frank); n != 2 {
			t.Fatalf("%d live sessions after the refusals, want 2", n)
		}
		list := e.stack.Do(r, "GET", "/api/v1/users/me/sessions", inherit, nil)
		if items, _ := list.JSON(t)["items"].([]any); list.Status != http.StatusOK || len(items) != 2 {
			t.Fatalf("the inheriting token lists: %d %s", list.Status, list.Body)
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/me/sessions/"+first, "", nil, browser...); got.Status != http.StatusNoContent {
			t.Fatalf("the session ends another session: %d %s", got.Status, got.Body)
		}
		if n := e.liveSessions(frank); n != 1 {
			t.Fatalf("%d live sessions after the session ended one, want 1", n)
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/me/sessions", inherit, nil); got.Status != http.StatusNoContent {
			t.Fatalf("the inheriting token ends all: %d %s", got.Status, got.Body)
		}
		if n := e.liveSessions(frank); n != 0 {
			t.Fatalf("%d live sessions after the inheriting token ended all", n)
		}
	})

	t.Run("R-API-021 revokeOwnTokens: a narrowed token is refused and every token keeps working", func(t *testing.T) {
		inherit := e.stack.Token(gina, scope.Default())
		narrow := narrowOf(gina)
		for name, tok := range narrow {
			refused(t, name, e.stack.Do(r, "DELETE", "/api/v1/users/me/tokens", tok, nil))
		}
		for name, tok := range narrow {
			if got := e.stack.Do(r, "GET", "/api/v1/auth/whoami", tok, nil); got.Status != http.StatusOK {
				t.Fatalf("%s after the refusals: %d %s", name, got.Status, got.Body)
			}
		}
		if got := e.stack.Do(r, "DELETE", "/api/v1/users/me/tokens", inherit, nil); got.Status != http.StatusNoContent {
			t.Fatalf("the inheriting token: %d %s", got.Status, got.Body)
		}
		for name, tok := range narrow {
			waitFor(t, "the revoked token "+name+" refused", revocationBound, func() bool {
				return e.stack.Do(r, "GET", "/api/v1/auth/whoami", tok, nil).Status == http.StatusUnauthorized
			})
		}
	})

	t.Run("R-API-021 the reads of the own profile answer a narrowed token", func(t *testing.T) {
		for name, tok := range narrowOf(erin) {
			for _, path := range []string{"/api/v1/users/me", "/api/v1/users/me/preferences", "/api/v1/auth/whoami", "/api/v1/authz/me"} {
				if got := e.stack.Do(r, "GET", path, tok, nil); got.Status != http.StatusOK {
					t.Errorf("%s reads %s: %d %s", name, path, got.Status, got.Body)
				}
			}
		}
	})

	t.Run("R-API-021 a service account has no own account, also with an inheriting token", func(t *testing.T) {
		tok := e.stack.Token(bot, scope.Default())
		for _, c := range []struct{ method, path string }{
			{"GET", "/api/v1/users/me/sessions"},
			{"DELETE", "/api/v1/users/me/sessions"},
			{"DELETE", "/api/v1/users/me/sessions/" + session.NewID()},
			{"DELETE", "/api/v1/users/me/tokens"},
		} {
			if got := e.stack.Do(r, c.method, c.path, tok, nil); got.Status != http.StatusForbidden {
				t.Errorf("%s %s: %d %s, want 403", c.method, c.path, got.Status, got.Body)
			}
		}
		if got := patchPrefs(tok, `"1"`); got.Status != http.StatusForbidden {
			t.Errorf("PATCH preferences: %d %s, want 403", got.Status, got.Body)
		}
	})

	t.Run("R-API-021 the break-glass session passes the step", func(t *testing.T) {
		login := e.stack.Do(r, "POST", "/auth/local/login", "", map[string]string{"username": "admin", "password": password}, "Origin", browserOrigin)
		if login.Status != http.StatusNoContent {
			t.Fatalf("local login: %d %s", login.Status, login.Body)
		}
		var hdr []string
		for _, c := range (&http.Response{Header: login.Header}).Cookies() {
			switch c.Name {
			case session.CookieSession:
				hdr = append(hdr, "Cookie", c.Name+"="+c.Value)
			case session.CookieCSRF:
				hdr = append(hdr, "X-CSRF-Token", c.Value)
			}
		}
		hdr = append(hdr, "Origin", browserOrigin)
		if got := e.stack.Do(r, "GET", "/api/v1/users/me/sessions", "", nil, hdr...); got.Status != http.StatusOK {
			t.Errorf("list: %d %s", got.Status, got.Body)
		}
		if got := patchPrefs("", "*", hdr...); got.Status != http.StatusOK {
			t.Errorf("preferences: %d %s", got.Status, got.Body)
		}
	})
}
