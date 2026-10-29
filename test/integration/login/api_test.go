//go:build integration

package login

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/session"
)

// send sends a request as the browser does: same origin, the CSRF header from the
// cookie, JSON bodies (merge patch for PATCH). hdr overrides headers; an empty
// value removes one.
func (b *browser) send(method, path, body string, hdr map[string]string) (answer, string) {
	b.t.Helper()
	var req *http.Request
	if body == "" {
		req, _ = http.NewRequest(method, b.e.baseURL+path, nil)
	} else {
		req, _ = http.NewRequest(method, b.e.baseURL+path, strings.NewReader(body))
		ctype := "application/json"
		if method == http.MethodPatch {
			ctype = "application/merge-patch+json"
		}
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Origin", b.e.baseURL)
	if c := b.cookie(session.CookieCSRF); c != "" {
		req.Header.Set(session.HeaderCSRF, c)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	return b.do(req)
}

func jsonOf(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not a JSON object: %s", body)
	}
	return m
}

func (e *env) bearer(token, method, path, body string) (int, string) {
	e.t.Helper()
	var req *http.Request
	if body == "" {
		req, _ = http.NewRequest(method, e.baseURL+path, nil)
	} else {
		req, _ = http.NewRequest(method, e.baseURL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	b := e.browser()
	resp, out := b.do(req)
	return resp.StatusCode, out
}

func apiEnv(t *testing.T, mut func(*env)) *env {
	t.Helper()
	e := newEnv(t)
	e.realAPI = true
	e.validator = apitest.New(t, v1.EmbeddedYAML())
	if mut != nil {
		mut(e)
	}
	return e
}

// TestR_AUTH_042_LoginJourneyOverTheAPI is the login part of E1.11 over the HTTP
// routes: sign in, whoami, a write with the CSRF header, the session list,
// re-authentication, logout, each request on the next of three replicas, with no
// Valkey at all (R-AUTH-042).
func TestR_AUTH_042_LoginJourneyOverTheAPI(t *testing.T) {
	e := apiEnv(t, nil)
	e.start(3, e.defaultAuth())
	root := e.adminToken()
	deadline := time.Now().Add(15 * time.Second)
	for { // the decision points follow the role binding within a poll
		if code, _ := e.bearer(root, http.MethodGet, "/api/v1/users", ""); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the administrator never became one")
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Run("R-AUTH-009 the login options are public and name the providers", func(t *testing.T) {
		for range 3 {
			resp, body := e.browser().get(e.baseURL + "/api/v1/auth/login-options")
			if resp.StatusCode != 200 {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			m := jsonOf(t, body)
			ps := m["providers"].([]any)
			if len(ps) != 1 || ps[0].(map[string]any)["id"] != "corp" || ps[0].(map[string]any)["startUrl"] != "/auth/oidc/corp/start" ||
				m["autoRedirect"] != "corp" || m["localLogin"] != false || m["anonymousAccess"] != false {
				t.Fatalf("%s", body)
			}
			if strings.Contains(body, e.idp.issuer) || strings.Contains(body, "client") {
				t.Fatalf("login options leak provider details: %s", body)
			}
		}
	})

	b := e.browser()
	t.Run("sign in, then whoami names the principal and the session on every replica", func(t *testing.T) {
		resp, body := b.get(e.baseURL + "/api/v1/auth/whoami")
		if resp.StatusCode != 401 {
			t.Fatalf("anonymous whoami: %d %s", resp.StatusCode, body)
		}
		b.mustLogin("corp")
		for range 6 {
			resp, body := b.get(e.baseURL + "/api/v1/auth/whoami")
			m := jsonOf(t, body)
			pr, cr := m["principal"].(map[string]any), m["credential"].(map[string]any)
			if resp.StatusCode != 200 || pr["name"] != "alice" || pr["kind"] != "user" || cr["type"] != "session" || cr["tokenId"] != nil || cr["scope"] != nil {
				t.Fatalf("whoami: %d %s", resp.StatusCode, body)
			}
		}
	})

	t.Run("R-AUTH-012 a write with the CSRF header succeeds on any replica, without it is refused", func(t *testing.T) {
		resp, body := b.send(http.MethodPatch, "/api/v1/users/me/preferences", `{"locale":"de"}`, map[string]string{"If-Match": `"1"`, session.HeaderCSRF: ""})
		if resp.StatusCode != 403 || !strings.Contains(body, "csrf_failed") {
			t.Fatalf("without the header: %d %s", resp.StatusCode, body)
		}
		resp, body = b.send(http.MethodPatch, "/api/v1/users/me/preferences", `{"locale":"de"}`, map[string]string{"If-Match": `"1"`, "Origin": "https://evil.test"})
		if resp.StatusCode != 403 {
			t.Fatalf("cross-origin: %d %s", resp.StatusCode, body)
		}
		resp, body = b.send(http.MethodPatch, "/api/v1/users/me/preferences", `{"locale":"de"}`, map[string]string{"If-Match": `"1"`})
		if resp.StatusCode != 200 || resp.Header.Get("ETag") != `"2"` {
			t.Fatalf("with the header: %d %s", resp.StatusCode, body)
		}
		for range 3 { // the next replica reads it from PostgreSQL
			resp, body := b.get(e.baseURL + "/api/v1/users/me/preferences")
			if resp.StatusCode != 200 || jsonOf(t, body)["locale"] != "de" || resp.Header.Get("ETag") != `"2"` {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
		}
		resp, _ = b.send(http.MethodPatch, "/api/v1/users/me/preferences", `{"locale":"en"}`, map[string]string{"If-Match": `"1"`})
		if resp.StatusCode != 412 {
			t.Fatalf("stale If-Match: %d", resp.StatusCode)
		}
		// The current user and a user record read.
		resp, body = b.get(e.baseURL + "/api/v1/users/me")
		if resp.StatusCode != 200 || jsonOf(t, body)["username"] != "alice" {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		resp, body = b.get(e.baseURL + "/api/v1/users/bob")
		if resp.StatusCode != 403 {
			t.Fatalf("a user without identities:read reads another user: %d %s", resp.StatusCode, body)
		}
	})

	t.Run("R-AUTH-010 the session list shows the current session; another session ends through the API on every replica", func(t *testing.T) {
		other := e.browser()
		other.mustLogin("corp")
		resp, body := b.get(e.baseURL + "/api/v1/users/me/sessions")
		items := jsonOf(t, body)["items"].([]any)
		if resp.StatusCode != 200 || len(items) != 2 {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		current, otherID := 0, ""
		for _, it := range items {
			m := it.(map[string]any)
			if m["current"] == true {
				current++
			} else {
				otherID = m["id"].(string)
			}
			if m["authMethod"] != "oidc" || m["providerId"] != "corp" {
				t.Fatalf("%v", m)
			}
		}
		if current != 1 || otherID == "" {
			t.Fatalf("exactly one current session: %s", body)
		}
		if resp, body := b.send(http.MethodDelete, "/api/v1/users/me/sessions/00000000-0000-7000-8000-0000000000ff", "", nil); resp.StatusCode != 404 {
			t.Fatalf("an unknown session: %d %s", resp.StatusCode, body)
		}
		if resp, body := b.send(http.MethodDelete, "/api/v1/users/me/sessions/"+otherID, "", nil); resp.StatusCode != 204 {
			t.Fatalf("end one: %d %s", resp.StatusCode, body)
		}
		for range 3 {
			if !eventually(t, 2*time.Second, func() bool { code, _ := other.whoami(); return code == http.StatusUnauthorized }) {
				t.Fatal("the ended session still works")
			}
		}
		if code, _ := b.whoami(); code != 200 {
			t.Fatal("the own session ended")
		}
		if n := len(e.audit.events("auth.session.ended")); n < 1 {
			t.Fatalf("auth.session.ended events: %d", n)
		}
	})

	t.Run("R-AUTH-010 keepCurrent ends every other session", func(t *testing.T) {
		others := []*browser{e.browser(), e.browser()}
		for _, o := range others {
			o.mustLogin("corp")
		}
		if resp, body := b.send(http.MethodDelete, "/api/v1/users/me/sessions?keepCurrent=true", "", nil); resp.StatusCode != 204 {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		for _, o := range others {
			if !eventually(t, 2*time.Second, func() bool { code, _ := o.whoami(); return code == http.StatusUnauthorized }) {
				t.Fatal("a session survived")
			}
		}
		_, body := b.get(e.baseURL + "/api/v1/users/me/sessions")
		if items := jsonOf(t, body)["items"].([]any); len(items) != 1 {
			t.Fatalf("%s", body)
		}
		if resp, _ := b.send(http.MethodDelete, "/api/v1/users/me/sessions?keepCurrent=maybe", "", nil); resp.StatusCode != 400 {
			t.Fatalf("keepCurrent=maybe: %d", resp.StatusCode)
		}
	})

	t.Run("R-AUTH-011 a token through a session needs a recent authentication", func(t *testing.T) {
		resp, body := b.send(http.MethodPost, "/api/v1/tokens", `{"description":"laptop","ttlSeconds":3600}`, nil)
		if resp.StatusCode != 201 || resp.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(jsonOf(t, body)["token"].(string), "trv_pat_") {
			t.Fatalf("%d %s %v", resp.StatusCode, body, resp.Header)
		}
		old := b.cookie(session.CookieSession)
		if _, err := e.admin.Exec(context.Background(), `UPDATE ui_session SET authenticated_at = now() - interval '2 hours' WHERE secret_sha256 = sha256($1::text::bytea)`, old); err != nil {
			t.Fatal(err)
		}
		flushAll(t, e)
		resp, body = b.send(http.MethodPost, "/api/v1/tokens", `{"description":"laptop","ttlSeconds":3600}`, nil)
		if resp.StatusCode != 401 || !strings.Contains(body, "reauthentication_required") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		cb := b.authorize(b.startLogin("corp", "?purpose=reauth&return_to=/ui/tokens"))
		if resp, body := b.get(cb); resp.StatusCode != http.StatusFound {
			t.Fatalf("reauth: %d %s", resp.StatusCode, body)
		}
		if resp, body := b.send(http.MethodPost, "/api/v1/tokens", `{"description":"laptop","ttlSeconds":3600}`, nil); resp.StatusCode != 201 {
			t.Fatalf("after the round trip: %d %s", resp.StatusCode, body)
		}
		// The token the session created works as a Bearer credential, and whoami names it.
		resp, body = b.send(http.MethodPost, "/api/v1/tokens", `{"description":"another"}`, nil)
		secret := jsonOf(t, body)["token"].(string)
		if code, out := e.bearer(secret, http.MethodGet, "/api/v1/auth/whoami", ""); code != 200 || !strings.Contains(out, `"type":"pat"`) {
			t.Fatalf("%d %s", code, out)
		}
	})

	t.Run("an administrator lists and ends another user's sessions", func(t *testing.T) {
		code, body := e.bearer(root, http.MethodGet, "/api/v1/users/alice/sessions", "")
		if code != 200 || len(jsonOf(t, body)["items"].([]any)) != 1 {
			t.Fatalf("%d %s", code, body)
		}
		if code, _ := e.bearer(root, http.MethodGet, "/api/v1/users/nobody/sessions", ""); code != 404 {
			t.Fatalf("%d", code)
		}
		if code, body := e.bearer(root, http.MethodDelete, "/api/v1/users/alice/sessions", ""); code != 204 {
			t.Fatalf("%d %s", code, body)
		}
		if !eventually(t, 2*time.Second, func() bool { code, _ := b.whoami(); return code == http.StatusUnauthorized }) {
			t.Fatal("the administrator did not end the session")
		}
		if code, _ := e.bearer(root, http.MethodDelete, "/api/v1/users/nobody/sessions", ""); code != 404 {
			t.Fatalf("%d", code)
		}
	})

	t.Run("R-AUTH-013 logout ends the session on every replica", func(t *testing.T) {
		c := e.browser()
		c.mustLogin("corp")
		if code, _ := c.whoami(); code != 200 {
			t.Fatal("not signed in")
		}
		resp, body := c.post("/auth/logout", "", nil)
		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			t.Fatalf("logout: %d %s", resp.StatusCode, body)
		}
		for range 3 {
			resp, body := c.get(e.baseURL + "/api/v1/auth/whoami")
			if resp.StatusCode != 401 {
				t.Fatalf("after logout: %d %s", resp.StatusCode, body)
			}
		}
	})

	if missing := e.validator.Uncovered("getLoginOptions", "getWhoami", "listOwnSessions", "endOwnSessions", "endOwnSession",
		"listUserSessions", "endUserSessions", "getPreferences", "updatePreferences", "createToken", "getCurrentUser", "listUsers"); len(missing) > 0 {
		t.Errorf("R-API-002 operations the journey did not reach: %v", missing)
	}
}

// TestR_AUTH_014_UnlockOverTheAPI: five wrong passwords lock the break-glass
// account on every replica; an administrator unlocks it through the API at once
// (R-AUTH-014, R-AUTH-038), and the user record shows the lock meanwhile.
func TestR_AUTH_014_UnlockOverTheAPI(t *testing.T) {
	e := apiEnv(t, nil)
	auth := e.defaultAuth()
	pwFile := filepath.Join(t.TempDir(), "password")
	writeFile(t, pwFile, bgPassword+"\n")
	auth.BreakGlass.Enabled, auth.BreakGlass.PasswordFile = true, pwFile
	e.start(3, auth)
	root := e.adminToken()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if code, _ := e.bearer(root, http.MethodGet, "/api/v1/users", ""); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the administrator never became one")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i := range 5 {
		if resp, body := e.browser().localLogin("admin", fmt.Sprintf("wrong-%d", i)); resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d %s", i, resp.StatusCode, body)
		}
	}
	if resp, body := e.browser().localLogin("admin", bgPassword); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked: %d %s", resp.StatusCode, body)
	}
	for range 3 {
		code, body := e.bearer(root, http.MethodGet, "/api/v1/users/admin", "")
		m := jsonOf(t, body)
		if code != 200 || m["lockedUntil"] == nil || m["isAdmin"] != true || fmt.Sprint(m["adminVia"]) != "[break-glass]" {
			t.Fatalf("%d %s", code, body)
		}
	}
	if code, body := e.bearer(root, http.MethodGet, "/api/v1/users?status=locked", ""); code != 200 || !strings.Contains(body, `"username":"admin"`) {
		t.Fatalf("status=locked: %d %s", code, body)
	}
	if code, _ := e.bearer(root, http.MethodPost, "/api/v1/users/alice/unlock", ""); code != 404 {
		t.Fatalf("unlock of another user: %d", code)
	}
	if code, body := e.bearer(root, http.MethodPost, "/api/v1/users/admin/unlock", ""); code != 204 {
		t.Fatalf("unlock: %d %s", code, body)
	}
	b := e.browser()
	if resp, body := b.localLogin("admin", bgPassword); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login after the unlock: %d %s", resp.StatusCode, body)
	}
	// The break-glass session is a session of a user record like any other.
	_, body := b.get(e.baseURL + "/api/v1/users/me/sessions")
	items := jsonOf(t, body)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["authMethod"] != "break_glass" {
		t.Fatalf("%s", body)
	}
	_, body = b.get(e.baseURL + "/api/v1/auth/whoami")
	if jsonOf(t, body)["principal"].(map[string]any)["kind"] != "break_glass" {
		t.Fatalf("%s", body)
	}
	if n := len(e.audit.events("auth.breakglass.unlocked")); n != 1 {
		t.Fatalf("auth.breakglass.unlocked events: %d", n)
	}
}
