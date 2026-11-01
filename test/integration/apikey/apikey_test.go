//go:build integration

package apikey

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_AUTH_033_TheAPIKeyOnARunningReplica is E3.5's API-key half and R-AUTH-033's
// acceptance on the routes of a format: a token as API key pushes, deletes and relists
// as its owner, in either form; the placeholder az leaves Basic in charge; the API key
// decides over Basic credentials of another principal; a revoked token gets 401 even
// with valid Basic credentials; the header is not read where the route does not declare
// it; the audit, the request log and the metrics name the method.
func TestR_AUTH_033_TheAPIKeyOnARunningReplica(t *testing.T) {
	e := newEnv(t)
	r := e.start("web-a")

	for _, c := range []struct {
		name, method, path string
		hdr                []string
		status             int
		who                string
		via                string
	}{
		{"R-NUGET-012 a token as API key pushes", "PUT", push, []string{apiKey, e.alicePAT}, 201, "alice", "api_key"},
		{"R-NUGET-012 <user>:<token> pushes", "PUT", push, []string{apiKey, "ci:" + e.alicePAT}, 201, "alice", "api_key"},
		{"R-NUGET-012 a token as API key deletes", "DELETE", version, []string{apiKey, e.alicePAT}, 200, "alice", "api_key"},
		{"R-NUGET-012 a token as API key relists", "POST", version, []string{apiKey, e.alicePAT}, 200, "alice", "api_key"},
		{
			"R-NUGET-012 the placeholder az leaves the Basic credentials in charge", "PUT", push,
			[]string{apiKey, "az", "Authorization", basic("ci", e.ciSAT)},
			201, "ci-bot", "basic",
		},
		{
			"R-AUTH-033 the API key decides over Basic credentials of another principal", "PUT", push,
			[]string{apiKey, e.alicePAT, "Authorization", basic("bob", e.bobPAT)},
			201, "alice", "api_key",
		},
		{"R-AUTH-033 the placeholder alone is no credential", "PUT", push, []string{apiKey, "az"}, 401, "", ""},
		{
			"R-NUGET-012 the header is not read on a route that does not declare it", "GET", feed + "/index.json",
			[]string{apiKey, e.alicePAT},
			401, "", "",
		},
		{
			"R-NUGET-012 Basic and Bearer work on every route", "GET", feed + "/index.json",
			[]string{"Authorization", "Bearer " + e.bobPAT},
			200, "bob", "bearer",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, who := e.send(r, c.method, c.path, c.hdr...)
			if a.Status != c.status {
				t.Fatalf("%d %s, want %d", a.Status, a.Body, c.status)
			}
			if c.status == http.StatusUnauthorized {
				if got := a.Header.Get("WWW-Authenticate"); got != `Basic realm="trove"` {
					t.Fatalf("challenge %q", got)
				}
				return
			}
			if who.Name != c.who || who.Method != c.via || who.TokenID == "" {
				t.Fatalf("reached as %+v, want %s by %s", who, c.who, c.via)
			}
		})
	}

	t.Run("E3.5 a revoked token as API key gets 401 although valid Basic credentials come with it", func(t *testing.T) {
		if a, who := e.send(r, "PUT", push, apiKey, e.doomPAT); a.Status != 201 || who.Name != "alice" {
			t.Fatalf("before the revocation: %d %s", a.Status, a.Body)
		}
		if a := e.stack.Do(r, "POST", "/api/v1/tokens/revoke", "", map[string]string{"token": e.doomPAT}); a.Status != 202 {
			t.Fatalf("revoke by value: %d %s", a.Status, a.Body)
		}
		var last testenv.Answer
		testenv.WaitFor(t, "the revoked API key refused", guard, func() bool {
			last, _ = e.send(r, "PUT", push, apiKey, e.doomPAT, "Authorization", basic("ci", e.ciSAT))
			return last.Status == http.StatusUnauthorized
		})
		if last.Code(t) != errs.CodeInvalidToken || last.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Fatalf("%d %s %v", last.Status, last.Body, last.Header)
		}
	})

	t.Run("R-AUTH-041 the audit names the API key's owner and the method", func(t *testing.T) {
		// bob may only read: his push is refused by the decision point, which records every
		// refused write with the actor (R-AUTHZ-029). The Basic credentials of alice, who
		// may push, change nothing: the API key decides.
		if a, _ := e.send(r, "PUT", push, apiKey, e.bobPAT, "Authorization", basic("alice", e.alicePAT2)); a.Status != http.StatusForbidden {
			t.Fatalf("bob's push: %d %s", a.Status, a.Body)
		}
		const q = `SELECT count(*) FROM audit_event WHERE action = 'authz.denied' AND actor_id = $1::uuid`
		testenv.WaitFor(t, "the authz.denied event of bob's push", guard, func() bool {
			n, err := e.stack.TryNumber(q, e.bob.ID)
			return err == nil && n > 0
		})
		got := e.stack.Text(`SELECT actor_name || ' ' || auth_method || ' ' || (token_id IS NOT NULL)::text FROM audit_event
			WHERE action = 'authz.denied' AND actor_id = $1::uuid LIMIT 1`, e.bob.ID)
		if got != "bob api_key true" {
			t.Fatalf("actor, method, token: %q", got)
		}
	})

	t.Run("R-OPS-015 the request log and the metrics say nuget_apikey and carry no token", func(t *testing.T) {
		logs := r.Logs.String()
		if !strings.Contains(logs, `"auth_method":"nuget_apikey"`) {
			t.Fatalf("no request log line with auth_method nuget_apikey:\n%s", testenv.Tail(logs, 20))
		}
		for _, tok := range []string{e.alicePAT, e.doomPAT, e.bobPAT} {
			if strings.Contains(logs, tok[:12]) {
				t.Fatal("a token value reached the log")
			}
		}
		m := r.Metrics(t)
		if v := testenv.MetricValue(m, `trove_auth_attempts_total{method="nuget_apikey",outcome="success",reason=""}`); v < 5 {
			t.Fatalf("successful API-key attempts %v", v)
		}
		if v := testenv.MetricValue(m, `trove_auth_attempts_total{method="nuget_apikey",outcome="failure",reason="revoked"}`); v < 1 {
			t.Fatalf("revoked API-key attempts %v", v)
		}
	})
}

// TestR_NUGET_012_ASessionCookieIsNoCredentialOnAnAPIKeyRoute: a signed-in browser's
// cookie, even with its CSRF token, cannot push, delete or relist (R-NUGET-012 security
// note); the same cookie still reads a package route with GET.
func TestR_NUGET_012_ASessionCookieIsNoCredentialOnAnAPIKeyRoute(t *testing.T) {
	e := newEnv(t)
	r := e.start("web-a")
	login := e.stack.Do(r, "POST", "/auth/local/login", "", map[string]string{"username": "admin", "password": breakPass}, "Origin", origin)
	if login.Status != http.StatusNoContent {
		t.Fatalf("local login: %d %s", login.Status, login.Body)
	}
	var cookie, csrf string
	for _, c := range (&http.Response{Header: login.Header}).Cookies() {
		switch c.Name {
		case session.CookieSession:
			cookie = c.Name + "=" + c.Value
		case session.CookieCSRF:
			csrf = c.Value
		}
	}
	if cookie == "" || csrf == "" {
		t.Fatalf("the login set no session cookies: %v", login.Header.Values("Set-Cookie"))
	}
	for _, c := range []struct{ method, path string }{{"POST", version}, {"PUT", push}, {"DELETE", version}} {
		a, _ := e.send(r, c.method, c.path, "Cookie", cookie, "X-CSRF-Token", csrf, "Origin", origin)
		if a.Status != http.StatusUnauthorized || a.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Errorf("%s %s with a session cookie: %d %s", c.method, c.path, a.Status, a.Body)
		}
	}
	if a, who := e.send(r, "GET", version, "Cookie", cookie); a.Status != http.StatusOK || who.Method != "session" {
		t.Fatalf("a GET with the cookie: %d %s", a.Status, a.Body)
	}
}

// TestR_AUTH_033_TheSwitchTurnsTheHeaderOff: with auth.clients.nuget_api_key_header
// false the header is never examined; the source credentials push.
func TestR_AUTH_033_TheSwitchTurnsTheHeaderOff(t *testing.T) {
	e := newEnv(t)
	r := e.start("web-off", "TROVE_AUTH_CLIENTS_NUGET_API_KEY_HEADER=false")
	if a, _ := e.send(r, "PUT", push, apiKey, e.alicePAT); a.Status != http.StatusUnauthorized {
		t.Fatalf("API key with the switch off: %d %s", a.Status, a.Body)
	}
	a, who := e.send(r, "PUT", push, apiKey, e.alicePAT, "Authorization", basic("ci", e.ciSAT))
	if a.Status != http.StatusCreated || who.Name != "ci-bot" || who.Method != "basic" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
}
