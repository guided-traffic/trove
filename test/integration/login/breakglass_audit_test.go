//go:build integration

package login

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
)

// TestR_AUTH_014_TheBreakGlassEventDoesNotKeepThePresentedUsername: a failed
// local login records whether the username was the account's, never the value,
// so a password typed into the username field stays out of the audit trail.
func TestR_AUTH_014_TheBreakGlassEventDoesNotKeepThePresentedUsername(t *testing.T) {
	e, _, _ := breakGlassEnv(t, 1, nil)
	if resp, body := e.browser().localLogin(bgPassword, "x"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, body := e.browser().localLogin("admin", "wrong-password"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	evs := e.audit.events(audit.EventAuthBreakGlassLogin)
	if len(evs) != 2 {
		t.Fatalf("%d events", len(evs))
	}
	for i, want := range []string{`"usernameMatched":false`, `"usernameMatched":true`} {
		d := string(evs[i].Details)
		if strings.Contains(d, bgPassword) || strings.Contains(d, `"username"`) {
			t.Fatalf("event %d keeps the presented username: %s", i, d)
		}
		if !strings.Contains(d, want) {
			t.Fatalf("event %d: %s, want %s", i, d, want)
		}
	}
}
