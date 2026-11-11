package v1

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/oidc"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

type fakeLogin struct{ password oidc.PasswordStatus }

func (fakeLogin) LoginOptions(bool) oidc.LoginOptions { return oidc.LoginOptions{} }
func (f fakeLogin) BreakGlassPassword() oidc.PasswordStatus {
	return f.password
}

func breakGlassStatus(t *testing.T, cfg config.AuthBreakGlass, login LoginService) map[string]any {
	t.Helper()
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := config.Auth{BreakGlass: cfg}
	a.Tokens.CacheTTL = 15 * time.Second
	s := &Server{o: Options{Auth: AuthServices{
		Config: a, Login: login,
		Settings: token.NewSettingsStore(token.SettingsOptions{Cache: reg, Tokens: a.Tokens}),
	}}}
	out, err := s.authSettings(httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/settings/auth", nil), token.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m["status"].(map[string]any)["breakGlass"].(map[string]any)
}

func TestR_AUTH_040_BreakGlassStatusNamesTheMissingPassword(t *testing.T) {
	enabled := func(file string) config.AuthBreakGlass {
		return config.AuthBreakGlass{Enabled: true, Username: "admin", PasswordFile: file}
	}
	for _, tc := range []struct {
		name  string
		cfg   config.AuthBreakGlass
		login LoginService
		want  string
	}{
		{"enabled without a password file", enabled(""), fakeLogin{password: oidc.PasswordNotConfigured}, "not_configured"},
		{"enabled without a password file and without a login service", enabled(""), nil, "not_configured"},
		{"a password file the replica could not use", enabled("/run/secrets/bg"), fakeLogin{password: oidc.PasswordUnusable}, "unusable"},
		{"a usable password", enabled("/run/secrets/bg"), fakeLogin{password: oidc.PasswordOK}, "ok"},
		{"a file and no login service to check it", enabled("/run/secrets/bg"), nil, "unusable"},
		{"the account is disabled", config.AuthBreakGlass{Username: "admin"}, fakeLogin{password: oidc.PasswordNotApplicable}, "not_applicable"},
	} {
		t.Run("R-AUTH-040 "+tc.name, func(t *testing.T) {
			got := breakGlassStatus(t, tc.cfg, tc.login)
			if got["passwordStatus"] != tc.want {
				t.Fatalf("status.breakGlass = %v, want passwordStatus %q", got, tc.want)
			}
		})
	}
}

func TestR_AUTH_040_TheETagFollowsTheStatusAndAWriteTakesOnlyTheRevision(t *testing.T) {
	var a, b gen.AuthSettingsStatus
	b.BreakGlass.PasswordStatus = gen.AuthSettingsStatusBreakGlassPasswordStatusNotConfigured
	if settingsETag(7, a) == settingsETag(7, b) {
		t.Fatal("a change of the status left the ETag as it was: a client would be told 304 about a stale status")
	}
	again := a
	if settingsETag(7, a) != settingsETag(7, again) || settingsETag(7, a) == settingsETag(8, a) {
		t.Fatal("the tag must be stable for one revision and status, and move with the revision")
	}
	for _, tc := range []struct {
		ifMatch string
		want    int64
	}{{settingsETag(7, b), 7}, {`"7"`, 7}, {"*", errs.AnyRevision}, {`"junk-x"`, 0}} {
		r := httptest.NewRequestWithContext(context.Background(), "PATCH", "/", nil)
		r.Header.Set("If-Match", tc.ifMatch)
		pre, err := ParseIfMatch(r)
		if err != nil || settingsRevision(pre) != tc.want {
			t.Errorf("If-Match %s: revision %d (%v), want %d", tc.ifMatch, settingsRevision(pre), err, tc.want)
		}
	}
	if settingsRevision(Precondition{}) != errs.AnyRevision {
		t.Error("no header means no revision condition")
	}
}
