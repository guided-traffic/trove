//go:build integration

package login

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/meta"
)

func TestR_AUTH_013_Logout(t *testing.T) {
	e := newEnv(t)
	e.start(3, e.defaultAuth())

	t.Run("R-AUTH-013 logout ends the session, clears both cookies and redirects to the provider's end_session_endpoint", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		old := b.cookie(session.CookieSession)
		resp, body := b.post("/auth/logout", "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("logout: %d %s", resp.StatusCode, body)
		}
		var out struct {
			RedirectTo string `json:"redirectTo"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		u, _ := url.Parse(out.RedirectTo)
		q := u.Query()
		if !strings.HasPrefix(out.RedirectTo, e.idp.issuer+"/end_session?") || q.Get("client_id") != "trove" ||
			q.Get("post_logout_redirect_uri") != e.baseURL+"/ui/logged-out" || strings.Count(q.Get("id_token_hint"), ".") != 2 {
			t.Fatalf("redirectTo %s", out.RedirectTo)
		}
		cleared := 0
		for _, c := range resp.Header.Values("Set-Cookie") {
			if (strings.HasPrefix(c, session.CookieSession+"=;") || strings.HasPrefix(c, session.CookieCSRF+"=;")) &&
				strings.Contains(c, "Max-Age=0") && strings.Contains(c, "Secure") && strings.Contains(c, "Path=/") {
				cleared++
			}
		}
		if cleared != 2 {
			t.Fatalf("cookies not cleared: %v", resp.Header.Values("Set-Cookie"))
		}
		// R-AUTH-013: the old cookie gets 401 on every replica within the revocation bound.
		b.setCookie(session.CookieSession, old)
		if !eventually(t, 2*time.Second, func() bool {
			for range 3 {
				if code, _ := b.whoami(); code != http.StatusUnauthorized {
					return false
				}
			}
			return true
		}) {
			t.Fatal("the logged-out cookie still authenticates on some replica")
		}
		if n := len(e.audit.events(audit.EventAuthLogout)); n != 1 {
			t.Fatalf("auth.logout events = %d", n)
		}
	})

	t.Run("R-AUTH-013 logout is idempotent: an ended session answers 200 and /ui/logged-out", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		old, csrf := b.cookie(session.CookieSession), b.cookie(session.CookieCSRF)
		if resp, _ := b.post("/auth/logout", "", nil); resp.StatusCode != 200 {
			t.Fatal("first logout")
		}
		b.setCookie(session.CookieSession, old)
		resp, body := b.post("/auth/logout", "", map[string]string{session.HeaderCSRF: csrf})
		if resp.StatusCode != 200 || !strings.Contains(body, `"/ui/logged-out"`) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})

	t.Run("R-AUTH-012 a logout without the CSRF header gets 403 csrf_failed and ends nothing", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		resp, body := b.post("/auth/logout", "", map[string]string{session.HeaderCSRF: ""})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "csrf_failed") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if code, _ := b.whoami(); code != 200 {
			t.Fatal("the session ended")
		}
		resp, _ = b.post("/auth/logout", "", map[string]string{"Origin": "https://evil.test"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("cross-origin logout: %d", resp.StatusCode)
		}
	})

	t.Run("R-AUTH-012 an unsafe cookie request without the header is refused, a fetch from the UI succeeds", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		resp, body := b.post("/api/v1/tokens", "{}", map[string]string{session.HeaderCSRF: ""})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "csrf_failed") {
			t.Fatalf("without header: %d %s", resp.StatusCode, body)
		}
		if resp, body := b.post("/api/v1/tokens", "{}", nil); resp.StatusCode != http.StatusCreated {
			t.Fatalf("with header: %d %s", resp.StatusCode, body)
		}
	})

	t.Run("R-AUTH-013 GET /auth/logout does not log out", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		// The test mux, like the router, has only the POST route.
		resp, _ := b.get(e.baseURL + "/auth/logout")
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET /auth/logout: %d", resp.StatusCode)
		}
		if code, _ := b.whoami(); code != 200 {
			t.Fatal("GET ended the session")
		}
	})
}

func TestR_AUTH_010_Sessions(t *testing.T) {
	e := newEnv(t)
	auth := e.defaultAuth()
	auth.Sessions.MaxPerUser = 3
	e.start(3, auth)

	t.Run("R-AUTH-010 the database holds only the SHA-256 of the session secret", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		secret := b.cookie(session.CookieSession)
		var n int
		if err := e.admin.QueryRow(context.Background(), `SELECT count(*) FROM ui_session WHERE secret_sha256 = sha256($1::text::bytea)`, secret).Scan(&n); err != nil || n != 1 {
			t.Fatalf("hash rows = %d, %v", n, err)
		}
		if err := e.admin.QueryRow(context.Background(), `SELECT count(*) FROM ui_session WHERE position($1::text::bytea in secret_sha256) > 0 OR id_token LIKE '%' || $1 || '%'`, secret).Scan(&n); err != nil || n != 0 {
			t.Fatalf("the secret is stored: %d, %v", n, err)
		}
	})

	t.Run("R-AUTH-010 R-AUTH-042 concurrent logins of one user over three replicas leave exactly max_per_user live sessions", func(t *testing.T) {
		var wg sync.WaitGroup
		for range 9 {
			wg.Go(func() {
				b := e.browser()
				resp, body := b.login("corp")
				if resp.StatusCode != http.StatusFound {
					t.Errorf("login: %d %s", resp.StatusCode, body)
				}
			})
		}
		wg.Wait()
		var live int
		if err := e.admin.QueryRow(context.Background(), `SELECT count(*) FROM ui_session WHERE ended_at IS NULL`).Scan(&live); err != nil || live != 3 {
			t.Fatalf("live sessions = %d, %v", live, err)
		}
		if evs := e.audit.events(audit.EventAuthSessionEnded); len(evs) == 0 {
			t.Fatal("no auth.session.ended for the evicted sessions")
		}
	})

	t.Run("R-AUTH-023 a session ended through one replica fails on every other within the revocation bound, no Valkey", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		for range 3 {
			if code, _ := b.whoami(); code != 200 {
				t.Fatal("not valid everywhere")
			}
		}
		var principalID string
		if err := e.admin.QueryRow(context.Background(), `SELECT principal_id::text FROM ui_session WHERE secret_sha256 = sha256($1::text::bytea)`,
			b.cookie(session.CookieSession)).Scan(&principalID); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err := e.replicas[0].sessions.EndPrincipalSessions(context.Background(), principalID, audit.Actor{Kind: "admin-cli"}); err != nil {
			t.Fatal(err)
		}
		if !eventually(t, 2*time.Second, func() bool {
			for range 3 {
				if code, _ := b.whoami(); code != http.StatusUnauthorized {
					return false
				}
			}
			return true
		}) {
			t.Fatalf("still valid after %s", time.Since(start))
		}
	})
}

// whoamiRaw returns the status and body of whoami.
func whoamiRaw(t *testing.T, b *browser) (int, string) {
	t.Helper()
	resp, body := b.get(b.e.baseURL + "/api/v1/auth/whoami")
	return resp.StatusCode, body
}

func TestR_AUTH_011_Reauthentication(t *testing.T) {
	e := newEnv(t)
	auth := e.defaultAuth()
	e.start(2, auth)

	t.Run("R-AUTH-011 a stale authenticated_at gets 401 reauthentication_required; the round trip replaces the session", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		old := b.cookie(session.CookieSession)
		if _, err := e.admin.Exec(context.Background(), `UPDATE ui_session SET authenticated_at = now() - interval '2 hours' WHERE secret_sha256 = sha256($1::text::bytea)`, old); err != nil {
			t.Fatal(err)
		}
		// The cached entries carry the earlier authenticated_at; a whoami after the
		// cache safety interval is not needed: a change of the area auth evicts.
		flushAll(t, e)
		resp, body := b.post("/api/v1/tokens", "{}", nil)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "reauthentication_required") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		cb := b.authorize(b.startLogin("corp", "?purpose=reauth&return_to=/ui/tokens"))
		if resp, body := b.get(cb); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ui/tokens" {
			t.Fatalf("reauth callback: %d %s", resp.StatusCode, body)
		}
		if resp, body := b.post("/api/v1/tokens", "{}", nil); resp.StatusCode != http.StatusCreated {
			t.Fatalf("after reauth: %d %s", resp.StatusCode, body)
		}
		var reason string
		if err := e.admin.QueryRow(context.Background(), `SELECT end_reason FROM ui_session WHERE secret_sha256 = sha256($1::text::bytea)`, old).Scan(&reason); err != nil || reason != "reauth_rotated" {
			t.Fatalf("old session: %q %v", reason, err)
		}
	})

	t.Run("R-AUTH-011 a re-authentication as another identity gives reauth_user_mismatch and leaves the session unchanged", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		e.idp.set(func(f *fakeIDP) {
			f.user = idpUser{sub: "sub-bob", username: "bob", email: "bob@example.com", groups: []string{}}
		})
		defer e.idp.set(func(f *fakeIDP) {
			f.user = idpUser{sub: "sub-alice", username: "alice", email: "alice@example.com", name: "Alice Example", groups: []string{"trove-team-web", "unrelated"}}
		})
		cb := b.authorize(b.startLogin("corp", "?purpose=reauth"))
		resp, body := b.get(cb)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, oidc.ReasonReauthUserMismatch) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if code, m := b.whoami(); code != 200 || m["name"] != "alice" {
			t.Fatalf("the original session changed: %d %v", code, m)
		}
	})

	t.Run("R-AUTH-011 purpose=reauth needs a live session", func(t *testing.T) {
		resp, body := e.browser().get(e.baseURL + "/auth/oidc/corp/start?purpose=reauth")
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "session_expired") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})
}

// flushAll commits a change of the area auth that the session package does not
// write, the shape of the identity area's principal change (a disable, a
// membership change): every replica flushes its session cache when its poll
// applies it, because the entries hold the principal's status (R-AUTH-023).
func flushAll(t *testing.T, e *env) {
	t.Helper()
	r := e.replicas[0]
	if err := r.db.InTx(context.Background(), func(tx meta.Tx) error {
		return r.changes.Notify(tx, session.ChangeArea, 1, principal.Change{Kind: principal.ChangePrincipal, IDs: []string{"00000000-0000-0000-0000-000000000009"}})
	}); err != nil {
		t.Fatal(err)
	}
	// Two poll intervals of the test replicas.
	time.Sleep(500 * time.Millisecond)
}

func TestR_AUTH_010_IdleTimeoutAcrossReplicas(t *testing.T) {
	e := newEnv(t)
	auth := e.defaultAuth()
	auth.Sessions.IdleTimeout = 2 * time.Second
	e.start(3, auth)

	t.Run("R-AUTH-010 Q-AUTH-13 after idle_timeout without a request every replica answers 401 session_expired and the row ends idle", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		for range 3 {
			if code, _ := b.whoami(); code != 200 {
				t.Fatal("not valid on every replica")
			}
		}
		time.Sleep(2500 * time.Millisecond)
		for i := range 3 {
			code, body := whoamiRaw(t, b)
			if code != http.StatusUnauthorized || !strings.Contains(body, "session_expired") {
				t.Fatalf("request %d: %d %s", i, code, body)
			}
		}
		var reason string
		if err := e.admin.QueryRow(context.Background(), `SELECT coalesce(end_reason, '') FROM ui_session WHERE secret_sha256 = sha256($1::text::bytea)`,
			b.cookie(session.CookieSession)).Scan(&reason); err != nil || reason != "idle" {
			t.Fatalf("end_reason %q, %v", reason, err)
		}
	})

	t.Run("R-AUTH-010 a replica that has not seen the session does not end it while the row's deadline lies ahead", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		// Push the row's last_seen_at forward, as touches through other replicas
		// would; every replica keeps an older view, which must not end the session.
		for range 6 {
			time.Sleep(700 * time.Millisecond)
			if _, err := e.admin.Exec(context.Background(), `UPDATE ui_session SET last_seen_at = now() WHERE secret_sha256 = sha256($1::text::bytea)`,
				b.cookie(session.CookieSession)); err != nil {
				t.Fatal(err)
			}
			if code, body := whoamiRaw(t, b); code != 200 {
				t.Fatalf("ended while the row is fresh: %d %s", code, body)
			}
		}
	})
}

func TestR_AUTH_007_008_Provisioning(t *testing.T) {
	e := newEnv(t)
	e.start(2, e.defaultAuth())

	t.Run("R-AUTH-007 R-AUTH-008 the provisioner receives the identity key, the mapped username and the mapped groups", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		c := e.prov.last()
		if c.Issuer != e.idp.issuer || c.Subject != "sub-alice" || c.Username != "alice" || c.Provider != "corp" || !c.JIT ||
			strings.Join(c.Groups, ",") != "team-web" || !c.GroupSync || c.ClaimState != principal.ClaimPresent {
			t.Fatalf("%+v", c)
		}
	})

	t.Run("R-AUTH-008 an omitted group claim reaches the provisioner as clear with reason absent", func(t *testing.T) {
		e.idp.set(func(f *fakeIDP) { f.user.groups = nil })
		defer e.idp.set(func(f *fakeIDP) { f.user.groups = []string{"trove-team-web", "unrelated"} })
		e.browser().mustLogin("corp")
		c := e.prov.last()
		if c.ClaimState != principal.ClaimAbsent || c.KeepOnMissing || len(c.Groups) != 0 {
			t.Fatalf("%+v", c)
		}
	})

	t.Run("R-AUTH-008 an overage marker reaches the provisioner with reason overage", func(t *testing.T) {
		e.idp.set(func(f *fakeIDP) { f.user.groups = nil; f.user.overage = true })
		defer e.idp.set(func(f *fakeIDP) { f.user.groups = []string{"trove-team-web", "unrelated"}; f.user.overage = false })
		e.browser().mustLogin("corp")
		if c := e.prov.last(); c.ClaimState != principal.ClaimOverage {
			t.Fatalf("%+v", c)
		}
	})

	t.Run("R-AUTH-007 a username taken by another identity fails with username_taken", func(t *testing.T) {
		e.idp.set(func(f *fakeIDP) { f.user.sub = "sub-other" })
		defer e.idp.set(func(f *fakeIDP) { f.user.sub = "sub-alice" })
		resp, body := e.browser().login("corp")
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "username_taken") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})

	t.Run("R-AUTH-016 a disabled user's open session fails on every replica after the disable commits", func(t *testing.T) {
		b := e.browser()
		b.mustLogin("corp")
		if _, err := e.admin.Exec(context.Background(), `UPDATE principal SET status = 'disabled' WHERE name = 'alice'`); err != nil {
			t.Fatal(err)
		}
		// The identity area records the disable in the area auth; this stands in.
		flushAll(t, e)
		for range 2 {
			if code, _ := b.whoami(); code != http.StatusUnauthorized {
				t.Fatalf("disabled user still signed in: %d", code)
			}
		}
		resp, body := e.browser().login("corp")
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "user_disabled") {
			t.Fatalf("login of a disabled user: %d %s", resp.StatusCode, body)
		}
	})
}

func TestR_AUTH_043_SessionCleanup(t *testing.T) {
	e := newEnv(t)
	e.start(1, e.defaultAuth())
	ctx := context.Background()
	b := e.browser()
	b.mustLogin("corp")
	_, _ = b.get(e.baseURL + "/auth/oidc/corp/start") // a pending flow
	if _, err := e.admin.Exec(ctx, `UPDATE ui_session SET ended_at = now() - interval '31 days', end_reason = 'logout'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.admin.Exec(ctx, `UPDATE oidc_login_flow SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	t.Run("R-AUTH-043 session-cleanup deletes expired flows and sessions 30 days after their end", func(t *testing.T) {
		res, err := session.Cleanup(ctx, e.replicas[0].db.Main())
		if err != nil {
			t.Fatal(err)
		}
		if res.Sessions != 1 || res.LoginFlows < 1 {
			t.Fatalf("%+v", res)
		}
		again, err := session.Cleanup(ctx, e.replicas[0].db.Main())
		if err != nil || again.Sessions != 0 || again.LoginFlows != 0 {
			t.Fatalf("a second run deleted %+v, %v", again, err)
		}
	})
}
