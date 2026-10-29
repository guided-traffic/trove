//go:build integration

package login

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/config"
)

func TestR_AUTH_042_BrowserLoginAcrossReplicas(t *testing.T) {
	e := newEnv(t)
	e.start(3, e.defaultAuth())

	t.Run("R-AUTH-005 R-AUTH-042 consecutive logins succeed with start and callback on different replicas, Valkey absent", func(t *testing.T) {
		for i := range 12 {
			b := e.browser()
			b.mustLogin("corp")
			// start, provider, callback: the edge served start and callback.
			served := e.servedBy(2)
			if served[0] == served[1] {
				t.Fatalf("login %d: start and callback on the same replica %v", i, served)
			}
			// The session is valid on every replica.
			for range 3 {
				if code, m := b.whoami(); code != 200 || m["name"] != "alice" {
					t.Fatalf("login %d: whoami %d %v", i, code, m)
				}
			}
		}
	})

	t.Run("R-AUTH-005 the authorization request carries S256, 43-character challenge, state and nonce, and the binding cookie's attributes", func(t *testing.T) {
		b := e.browser()
		resp, _ := b.get(e.baseURL + "/auth/oidc/corp/start?return_to=/ui/")
		u, _ := url.Parse(resp.Header.Get("Location"))
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || len(q.Get("state")) != 43 || len(q.Get("nonce")) != 43 {
			t.Fatalf("%v", q)
		}
		var bind string
		for _, c := range resp.Header.Values("Set-Cookie") {
			if strings.HasPrefix(c, session.CookieBinding+"=") {
				bind = c
			}
		}
		if !strings.Contains(bind, "Secure") || !strings.Contains(bind, "HttpOnly") || !strings.Contains(bind, "SameSite=Lax") ||
			!strings.Contains(bind, "Path=/") || strings.Contains(bind, "Domain") {
			t.Fatalf("binding cookie %q", bind)
		}
	})

	t.Run("R-AUTH-003 the redirect URI comes from the base URL, never from the Host header", func(t *testing.T) {
		b := e.browser()
		req, _ := http.NewRequest(http.MethodGet, e.baseURL+"/auth/oidc/corp/start", nil)
		req.Host = "evil.test"
		resp, _ := b.do(req)
		u, _ := url.Parse(resp.Header.Get("Location"))
		if got := u.Query().Get("redirect_uri"); got != e.baseURL+"/auth/oidc/corp/callback" {
			t.Fatalf("redirect_uri %q", got)
		}
	})

	t.Run("R-AUTH-005 return_to outside /ui/ ends the login at /ui/", func(t *testing.T) {
		for _, rt := range []string{"https://evil.test/", "//evil.test"} {
			b := e.browser()
			cb := b.authorize(b.startLogin("corp", "?return_to="+url.QueryEscape(rt)))
			resp, _ := b.get(cb)
			if resp.Header.Get("Location") != "/ui/" {
				t.Errorf("return_to %q ended at %q", rt, resp.Header.Get("Location"))
			}
		}
	})

	t.Run("R-AUTH-006 replaying a callback URL that already succeeded gives 400 flow_expired", func(t *testing.T) {
		b := e.browser()
		cb := b.authorize(b.startLogin("corp", ""))
		if resp, _ := b.get(cb); resp.StatusCode != http.StatusFound {
			t.Fatalf("first: %d", resp.StatusCode)
		}
		resp, body := b.get(cb)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "flow_expired") {
			t.Fatalf("replay: %d %s", resp.StatusCode, body)
		}
	})

	t.Run("R-AUTH-006 a callback opened in a second browser without the binding cookie gives 400 browser_mismatch", func(t *testing.T) {
		b := e.browser()
		cb := b.authorize(b.startLogin("corp", ""))
		resp, body := e.browser().get(cb)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "browser_mismatch") {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("the error page can be framed")
		}
	})

	t.Run("R-AUTH-041 each failure writes one auth.login.failed with the reason and the client address", func(t *testing.T) {
		var reasons []string
		for _, ev := range e.audit.events(audit.EventAuthLoginFailed) {
			reasons = append(reasons, ev.Reason)
			if ev.Source.ClientAddr == "" {
				t.Errorf("event without client address: %+v", ev)
			}
		}
		if !slices.Contains(reasons, oidc.ReasonFlowExpired) || !slices.Contains(reasons, oidc.ReasonBrowserMismatch) {
			t.Fatalf("reasons %v", reasons)
		}
	})
}

func TestR_AUTH_006_ProviderMisbehaviour(t *testing.T) {
	e := newEnv(t)
	auth := e.defaultAuth()
	e.start(2, auth)

	cases := []struct {
		name   string
		set    func(*fakeIDP)
		status int
		reason string
	}{
		{"a wrong nonce gives nonce_mismatch", func(f *fakeIDP) { f.wrongNonce = true }, 400, oidc.ReasonNonceMismatch},
		{"a token signed with an unpublished key gives id_token_invalid", func(f *fakeIDP) { f.unpublishedKey = true }, 400, oidc.ReasonIDTokenInvalid},
		{"alg none gives id_token_invalid", func(f *fakeIDP) { f.algNone = true }, 400, oidc.ReasonIDTokenInvalid},
		{"an extra untrusted audience gives id_token_invalid", func(f *fakeIDP) { f.extraAudience = "untrusted" }, 400, oidc.ReasonIDTokenInvalid},
		{"a userinfo sub other than the ID token's gives userinfo_mismatch", func(f *fakeIDP) { f.omitUserinfoSub = true }, 400, oidc.ReasonUserinfoMismatch},
		{"a wrong client secret at the token endpoint gives 502", func(f *fakeIDP) { f.secret = "other" }, 502, oidc.ReasonTokenExchange},
	}
	for _, c := range cases {
		t.Run("R-AUTH-006 "+c.name, func(t *testing.T) {
			before := len(e.audit.events(audit.EventAuthLoginFailed))
			e.idp.set(c.set)
			defer e.idp.set(func(f *fakeIDP) {
				f.wrongNonce, f.unpublishedKey, f.algNone, f.extraAudience, f.omitUserinfoSub, f.secret = false, false, false, "", false, "s3cret-one"
			})
			b := e.browser()
			resp, body := b.login("corp")
			if resp.StatusCode != c.status || !strings.Contains(body, c.reason) {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			if b.cookie(session.CookieSession) != "" {
				t.Fatal("a session was created")
			}
			evs := e.audit.events(audit.EventAuthLoginFailed)
			if len(evs) != before+1 || evs[len(evs)-1].Reason != c.reason {
				t.Fatalf("events: %d new, last %+v", len(evs)-before, evs[len(evs)-1])
			}
		})
	}

	t.Run("R-AUTH-006 a JWKS rotation is picked up by one refetch inside the callback", func(t *testing.T) {
		e.idp.rotate()
		b := e.browser()
		b.mustLogin("corp")
	})

	t.Run("R-AUTH-004 a rewritten client secret file is used by the next token request without a restart", func(t *testing.T) {
		// A different size or modification time makes the provider read the file again.
		time.Sleep(10 * time.Millisecond)
		writeFile(t, e.secretFile, "s3cret-two-rotated\n")
		e.idp.set(func(f *fakeIDP) { f.secret = "s3cret-two-rotated" })
		defer e.idp.set(func(f *fakeIDP) { f.secret = "s3cret-one" })
		defer writeFile(t, e.secretFile, "s3cret-one\n")
		b := e.browser()
		b.mustLogin("corp")
	})

	t.Run("R-AUTH-006 R-ARCH-019 no trove connection is idle in transaction while the token endpoint is slow", func(t *testing.T) {
		e.idp.set(func(f *fakeIDP) { f.tokenDelay = 2 * time.Second })
		defer e.idp.set(func(f *fakeIDP) { f.tokenDelay = 0 })
		b := e.browser()
		cb := b.authorize(b.startLogin("corp", ""))
		done := make(chan struct{})
		go func() {
			defer close(done)
			resp, _ := b.get(cb)
			if resp.StatusCode != http.StatusFound {
				t.Errorf("slow login: %d", resp.StatusCode)
			}
		}()
		deadline := time.After(1800 * time.Millisecond)
	loop:
		for {
			var n int
			if err := e.admin.QueryRow(context.Background(),
				`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'idle in transaction' AND application_name LIKE 'web-%'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				t.Fatalf("%d connections idle in transaction", n)
			}
			select {
			case <-deadline:
				break loop
			case <-time.After(100 * time.Millisecond):
			}
		}
		<-done
	})
}

func TestR_AUTH_006_ProviderMixUp(t *testing.T) {
	e := newEnv(t)
	auth := e.defaultAuth()
	auth.OIDC.Providers["second"] = e.provider(func(p *config.AuthOIDCProvider) { p.DisplayName = "Second" })
	e.start(2, auth)

	t.Run("R-AUTH-006 feeding provider A's callback a state created for provider B gives provider_mismatch", func(t *testing.T) {
		b := e.browser()
		cb := b.authorize(b.startLogin("second", ""))
		u, _ := url.Parse(cb)
		wrong := e.baseURL + "/auth/oidc/corp/callback?" + u.RawQuery
		resp, body := b.get(wrong)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, oidc.ReasonProviderMismatch) {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
	})

	t.Run("R-AUTH-003 each provider has its own redirect URI", func(t *testing.T) {
		b := e.browser()
		a, s := b.startLogin("corp", ""), b.startLogin("second", "")
		if a.Query().Get("redirect_uri") == s.Query().Get("redirect_uri") {
			t.Fatal("both providers share one redirect URI")
		}
	})

	t.Run("R-AUTH-009 two providers are listed in configuration order without auto redirect", func(t *testing.T) {
		o := e.replicas[0].login.LoginOptions(false)
		if len(o.Providers) != 2 || o.Providers[0].ID != "corp" || o.AutoRedirect != "" || !o.Providers[0].Available {
			t.Fatalf("%+v", o)
		}
	})
}
