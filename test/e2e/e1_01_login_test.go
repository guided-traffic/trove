//go:build e2e

package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestE1_01Login is exit criterion E1.1: through the Ingress, a Keycloak user signs in with the authorization
// code flow and PKCE, is created just in time, takes the groups of the configured claim, logs out (the trove
// session ends, and so does the IdP session through the RP-initiated logout), and the break-glass administrator
// signs in with the generated password and can be disabled.
//
// The browser is an HTTP client with a cookie jar that follows the redirects by hand and posts the IdP's login
// form (m1_env_test.go, oidcLogin). Playwright is not used here: the harness has none, and the journeys of
// R-UI-047 (E1.11) are the place for a real browser (e1_11_journeys_test.go leaves the hook). The IdP is reached
// by its Service name through a port-forward, so the redirect URL of the flow is followed unchanged and its
// certificate is verified against the dev CA for that name.
func TestE1_01Login(t *testing.T) {
	useScatter(t)
	adm := adminAPI(t)
	alicePw := secretValue(t, "trove-keycloak", "alice-password")
	bobPw := secretValue(t, "trove-keycloak", "bob-password")

	// A user that exists from an earlier run would make the just-in-time creation unobservable.
	for _, u := range []string{"alice", "bob"} {
		adm.do(http.MethodDelete, "/api/v1/users/"+u, nil).want(t, "delete "+u+" from an earlier run", 204, 404)
	}

	var alice *browser

	t.Run("R-AUTH-003 R-AUTH-006 R-AUTH-007 R-AUTH-008 E1.1 alice signs in with the authorization code flow and PKCE, is created just in time and gets the groups of the claim", func(t *testing.T) {
		if adm.get("/api/v1/users/alice").Status != 404 {
			t.Fatal("alice exists before her first login")
		}
		alice = newBrowser(t)
		tr := alice.oidcLogin("alice", alicePw)
		t.Logf("hops: %s", strings.Join(tr.Hops, " -> "))
		if tr.AuthURL == nil {
			t.Fatal("start did not redirect to the IdP")
		}
		q := tr.AuthURL.Query()
		for k, want := range map[string]string{"response_type": "code", "client_id": "trove", "code_challenge_method": "S256"} {
			if q.Get(k) != want {
				t.Errorf("authorization request %s = %q, want %q", k, q.Get(k), want)
			}
		}
		for _, k := range []string{"code_challenge", "state", "nonce", "redirect_uri"} {
			if q.Get(k) == "" {
				t.Errorf("authorization request has no %s", k)
			}
		}
		if want := cfg.edgeURL + "/auth/oidc/keycloak/callback"; q.Get("redirect_uri") != want {
			t.Errorf("redirect_uri %q, want %q", q.Get("redirect_uri"), want)
		}
		if !tr.SawLoginForm {
			t.Error("the IdP did not show its login form to a browser without an IdP session")
		}
		// The IdP enforces PKCE for the client (realm: pkce.code.challenge.method S256), so a completed
		// code exchange proves the verifier matched the challenge.
		if tr.CallbackReply.Status != 302 {
			t.Errorf("callback = %s, want a redirect into the UI", tr.CallbackReply)
		}
		if tr.Final.Status != 200 {
			t.Fatalf("the UI after sign-in answered %s", tr.Final)
		}
		for _, c := range []string{"__Host-trv_session", "__Host-trv_csrf"} {
			if alice.cookie(c) == "" {
				t.Errorf("no %s cookie after the sign-in", c)
			}
		}
		r, name := alice.whoami()
		r.want(t, "whoami with the session", 200)
		if name != "alice" {
			t.Fatalf("whoami says %q, want alice: %s", name, r)
		}
		var w struct {
			Principal struct{ Groups []string }
		}
		r.JSON(t, &w)
		slices.Sort(w.Principal.Groups)
		if want := []string{"platform-admins", "team-web"}; !slices.Equal(w.Principal.Groups, want) {
			t.Errorf("groups %v, want %v (claim values trove-team-web and trove-platform-admins through the rule trove-(.+))", w.Principal.Groups, want)
		}
		var u struct {
			Origin string `json:"origin"`
			Status string `json:"status"`
			Email  string `json:"email"`
		}
		adm.get("/api/v1/users/alice").want(t, "alice after the login", 200).JSON(t, &u)
		if u.Origin != "jit" || u.Status != "active" || u.Email != "alice@trove.test" {
			t.Errorf("alice = %+v, want origin jit, active, email alice@trove.test", u)
		}
	})

	t.Run("R-AUTH-007 R-AUTH-008 E1.1 bob signs in and gets only his group", func(t *testing.T) {
		bob := newBrowser(t)
		bob.oidcLogin("bob", bobPw)
		r, name := bob.whoami()
		r.want(t, "whoami with bob's session", 200)
		if name != "bob" {
			t.Fatalf("whoami says %q, want bob", name)
		}
		var w struct {
			Principal struct{ Groups []string }
		}
		r.JSON(t, &w)
		if !slices.Equal(w.Principal.Groups, []string{"team-web"}) {
			t.Errorf("bob's groups %v, want [team-web]", w.Principal.Groups)
		}
	})

	t.Run("R-AUTH-013 E1.1 logout ends the trove session and the IdP session (RP-initiated logout)", func(t *testing.T) {
		if alice == nil {
			t.Skip("alice's sign-in failed")
		}
		// Control: with the IdP session alive a second sign-in needs no password. Without it the check after the
		// logout would prove nothing.
		second := newBrowser(t)
		second.c.Jar = alice.c.Jar // the same browser, so the IdP's cookies and trove's are shared
		sso := second.oidcLogin("alice", alicePw)
		if sso.SawLoginForm {
			t.Fatal("the IdP asked for the password again while its session was alive: the control does not hold")
		}
		second.syncCSRF()
		alice = second

		sess := alice.cookie("__Host-trv_session") // the cookie value a stolen copy of the browser would still hold
		r := alice.do(http.MethodPost, "/auth/logout", nil).want(t, "POST /auth/logout", 200)
		var out struct {
			RedirectTo string `json:"redirectTo"`
		}
		r.JSON(t, &out)
		end := mustURL(t, out.RedirectTo)
		if !strings.HasPrefix(end.Host, "trove-keycloak.") || !strings.Contains(end.Path, "/protocol/openid-connect/logout") {
			t.Fatalf("redirectTo %q is not the IdP's end-session endpoint", out.RedirectTo)
		}
		if end.Query().Get("id_token_hint") == "" || end.Query().Get("client_id") != "trove" {
			t.Errorf("the end-session URL lacks id_token_hint or client_id: %s", end.RawQuery)
		}
		wantBack := cfg.edgeURL + "/ui/logged-out"
		if end.Query().Get("post_logout_redirect_uri") != wantBack {
			t.Errorf("post_logout_redirect_uri %q, want %q", end.Query().Get("post_logout_redirect_uri"), wantBack)
		}
		// The browser follows it: the IdP ends its session and sends the browser back to trove.
		end1 := alice.get(out.RedirectTo)
		if end1.Status < 300 || end1.Status >= 400 || !strings.HasPrefix(end1.Header.Get("Location"), wantBack) {
			t.Fatalf("the IdP answered %s (Location %q), want a redirect to %s", end1, end1.Header.Get("Location"), wantBack)
		}
		page := alice.get(end1.Header.Get("Location"))
		if page.Status == 302 { // the UI adds the locale
			page = alice.get(page.Header.Get("Location"))
		}
		page.want(t, "the logged-out page", 200)

		// The trove session is gone on every replica: consecutive requests reach all three.
		sessionEnded(t, sess)
		// The IdP session is gone: a new sign-in in the same browser shows the login form again.
		again := newBrowser(t)
		again.c.Jar = alice.c.Jar
		tr := again.oidcLogin("alice", alicePw)
		if !tr.SawLoginForm {
			t.Error("a new sign-in after the logout did not ask for the password: the IdP session survived")
		}
		if _, name := again.whoami(); name != "alice" {
			t.Errorf("sign-in after the logout: whoami says %q", name)
		}
	})

	t.Run("R-AUTH-014 E1.1 the break-glass administrator signs in with the generated password and can be disabled", func(t *testing.T) {
		pw := secretValue(t, "trove-break-glass", "password")
		bg := newBrowser(t)
		bg.localLogin("admin", "not-the-password-0123456789").want(t, "a wrong break-glass password", 401)
		bg.localLogin("admin", pw).want(t, "the break-glass login", 204)
		r, name := bg.whoami()
		r.want(t, "whoami as the break-glass account", 200)
		if name != "admin" {
			t.Fatalf("whoami says %q, want admin", name)
		}
		var u struct {
			IsAdmin  bool     `json:"isAdmin"`
			AdminVia []string `json:"adminVia"`
		}
		adm.get("/api/v1/users/admin").want(t, "the break-glass user", 200).JSON(t, &u)
		if !u.IsAdmin || !slices.Contains(u.AdminVia, "break-glass") {
			t.Errorf("the break-glass user = %+v, want an administrator via break-glass", u)
		}

		// Disable it the way an operator does once an OIDC administrator exists: auth.break_glass.enabled false.
		// The environment variable is the same key (docs/developer/configuration.md) and needs no migration Job;
		// the cleanup removes it again.
		restore := func() {
			_, _ = kubectl("-n", cfg.namespace, "set", "env", "deployment/trove-web", "TROVE_AUTH_BREAK_GLASS_ENABLED-")
			_, _ = kubectl("-n", cfg.namespace, "rollout", "status", "deployment/trove-web", "--timeout=5m")
			waitWebSettled(t)
		}
		t.Cleanup(restore)
		kn(t, "set", "env", "deployment/trove-web", "TROVE_AUTH_BREAK_GLASS_ENABLED=false")
		waitWebSettled(t)

		fresh := newBrowser(t)
		seen := map[string]bool{}
		for i := 0; i < 9; i++ {
			l := fresh.localLogin("admin", pw)
			if l.Status != 403 || l.Code() != "local_login_disabled" {
				t.Errorf("break-glass login %d with enabled false = %s, want 403 local_login_disabled", i, l)
			}
			seen[l.Replica()] = true
		}
		if len(seen) != 3 {
			t.Errorf("the nine attempts reached %v, want all three replicas", sortedKeys(seen))
		}
		for i := 0; i < 3; i++ {
			s, _ := bg.whoami()
			if s.Status != 401 || s.Code() != "session_expired" {
				t.Errorf("the session of before the change, request %d = %s, want 401 session_expired", i, s)
			}
		}

		restore()
		again := newBrowser(t)
		again.localLogin("admin", pw).want(t, "the break-glass login after enabling it again", 204)
	})
}
