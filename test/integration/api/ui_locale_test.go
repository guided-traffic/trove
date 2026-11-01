//go:build integration

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestUILocaleFollowsTheSavedPreference is R-UI-001 on the running composition
// root: a signed-in user whose stored locale is German is sent from /ui/ to
// /ui/de/ although the browser asks for English, and a request without a session
// (or with one that is not valid) still follows Accept-Language.
func TestUILocaleFollowsTheSavedPreference(t *testing.T) {
	e := newEnv(t, true)
	const password = "correct horse battery staple 1"
	file := testenv.WriteFile(t, t.TempDir(), "break-glass", password)
	r := e.stack.Start(t, testenv.ReplicaOptions{Name: "web-ui", Env: []string{
		"TROVE_AUTH_BREAK_GLASS_ENABLED=true", "TROVE_AUTH_BREAK_GLASS_PASSWORD_FILE=" + file,
	}})
	const origin = "http://trove.test"

	login := e.stack.Do(r, "POST", "/auth/local/login", "", map[string]string{"username": "admin", "password": password}, "Origin", origin)
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

	patch := e.stack.Do(r, "PATCH", "/api/v1/users/me/preferences", "", map[string]string{"locale": "de"},
		"Cookie", cookie, "X-CSRF-Token", csrf, "Origin", origin, "If-Match", "*", "Content-Type", "application/merge-patch+json")
	if patch.Status != http.StatusOK {
		t.Fatalf("saving the locale: %d %s", patch.Status, patch.Body)
	}

	location := func(hdr ...string) string {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), "GET", "http://"+r.Addr("default")+"/ui/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = testenv.DefaultHost
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		c := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusFound {
			t.Fatalf("GET /ui/ = %d", res.StatusCode)
		}
		return res.Header.Get("Location")
	}

	if got := location("Cookie", cookie, "Accept-Language", "en"); got != "/ui/de/" {
		t.Errorf("signed in with a stored German locale: Location %q, want /ui/de/", got)
	}
	if got := location("Accept-Language", "en"); got != "/ui/en/" {
		t.Errorf("without a session the browser's language applies: Location %q, want /ui/en/", got)
	}
	bogus := strings.Replace(cookie, "=", "=x", 1)
	if got := location("Cookie", bogus, "Accept-Language", "en"); got != "/ui/en/" {
		t.Errorf("with a session cookie that is not valid: Location %q, want /ui/en/", got)
	}
}
