package config

import (
	"strings"
	"testing"
	"time"
)

func loadLogin(t *testing.T, file string, env ...string) (*Loaded, error) {
	t.Helper()
	return Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandAdmin})
}

const corpYAML = `auth:
  oidc:
    providers:
      corp:
        display_name: Corporate SSO
        issuer: https://login.example.com/realms/acme
        client_id: trove
        client_secret_file: /var/run/secrets/trove/oidc-corp/client-secret
        clock_skew: 30s
        group_sync:
          rules:
            - match_regex: "trove-team-(.+)"
              group: "team-$1"
`

func TestLoginConfig(t *testing.T) {
	t.Run("R-AUTH-004 a provider entry loads; keys left out take the defaults of the specification", func(t *testing.T) {
		l, err := loadLogin(t, corpYAML)
		if err != nil {
			t.Fatal(err)
		}
		p := l.Config.Auth.OIDC.Providers["corp"].Defaulted()
		if p.ClockSkew != 30*time.Second || p.ClientAuthMethod != OIDCClientAuthBasic || p.MaxIDTokenAge != 10*time.Minute ||
			!*p.UseUserInfo || !*p.RPInitiatedLogout || !*p.JIT || !*p.SyncUsername || !*p.GroupSync.Enabled ||
			p.GroupSync.OnMissingClaim != OnMissingClear || p.Claims.Username != "preferred_username" ||
			strings.Join(p.Scopes, " ") != "openid profile email" || strings.Join(p.AllowedSigningAlgs, ",") != "RS256,PS256,ES256" {
			t.Fatalf("%+v", p)
		}
	})

	t.Run("R-AUTH-010 R-AUTH-014 the session and break-glass defaults", func(t *testing.T) {
		l, err := loadLogin(t, "")
		if err != nil {
			t.Fatal(err)
		}
		a := l.Config.Auth
		if a.Sessions.IdleTimeout != time.Hour || a.Sessions.AbsoluteTimeout != 12*time.Hour || a.Sessions.MaxPerUser != 20 ||
			!a.BreakGlass.Enabled || a.BreakGlass.Username != "admin" || a.BreakGlass.RotationHold != 2*time.Minute ||
			a.BreakGlass.Lockout.Threshold != 5 || a.BreakGlass.MaxConcurrentVerifications != 2 || !a.LoginPage.AutoRedirect {
			t.Fatalf("%+v", a)
		}
	})

	bad := []struct {
		name, yaml, want string
	}{
		{"R-AUTH-004 a provider ID outside the pattern", strings.Replace(corpYAML, "corp:", "Corp_1:", 1), "providers.Corp_1"},
		{"R-AUTH-004 an http issuer that is not loopback", strings.Replace(corpYAML, "https://login", "http://login", 1), "issuer must be https"},
		{"R-AUTH-004 a reserved extra parameter", corpYAML + "        extra_auth_params: {state: x}\n", `must not set "state"`},
		{"R-AUTH-004 an inline secret is impossible and a missing one is reported", strings.Replace(corpYAML, "        client_secret_file: /var/run/secrets/trove/oidc-corp/client-secret\n", "", 1), "client_secret_file"},
		{"R-AUTH-006 alg none or HS256 in allowed_signing_algs", corpYAML + "        allowed_signing_algs: [RS256, HS256]\n", `"HS256" is not supported`},
		{"R-AUTH-008 a rule with match and match_regex", corpYAML + "            - match: a\n              match_regex: b\n              group: c\n", "exactly one of match and match_regex"},
		{"R-AUTH-011 an idle timeout above 24h", "auth:\n  sessions:\n    idle_timeout: 25h\n", "auth.sessions.idle_timeout"},
		{"R-AUTH-011 an absolute timeout above 168h", "auth:\n  sessions:\n    absolute_timeout: 200h\n", "auth.sessions.absolute_timeout"},
		{"R-AUTH-007 a reserved break-glass username", "auth:\n  break_glass:\n    username: me\n", "is reserved"},
	}
	for _, c := range bad {
		t.Run(c.name+" stops the load", func(t *testing.T) {
			_, err := loadLogin(t, c.yaml)
			if err == nil || !strings.Contains(strings.Join(issuesOf(t, err), "\n"), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}

	t.Run("R-AUTH-007 a public issuer with jit and no login_requires_groups stops the load and names the provider", func(t *testing.T) {
		y := strings.Replace(corpYAML, "https://login.example.com/realms/acme", "https://gitlab.com", 1)
		_, err := loadLogin(t, y)
		msgs := strings.Join(issuesOf(t, err), "\n")
		if !strings.Contains(msgs, "login_requires_groups") || !strings.Contains(msgs, "provider corp has a public issuer") {
			t.Fatal(msgs)
		}
	})

	t.Run("R-AUTH-007 a public issuer starts with login_requires_groups and a namespaced rule, and fails with an open rule", func(t *testing.T) {
		y := strings.Replace(corpYAML, "https://login.example.com/realms/acme", "https://gitlab.com", 1)
		y = strings.Replace(y, `"trove-team-(.+)"`, `"acme/trove-(.+)"`, 1) + "        login_requires_groups: [acme]\n"
		if _, err := loadLogin(t, y); err != nil {
			t.Fatalf("namespaced: %v", err)
		}
		y = strings.Replace(y, `"acme/trove-(.+)"`, `"trove-(.+)"`, 1)
		_, err := loadLogin(t, y)
		if err == nil || !strings.Contains(strings.Join(issuesOf(t, err), "\n"), "literal namespace ending in /") {
			t.Fatalf("open rule: %v", err)
		}
	})

	t.Run("R-AUTH-004 the whole provider map comes from the environment as JSON", func(t *testing.T) {
		l, err := loadLogin(t, "", `TROVE_AUTH_OIDC_PROVIDERS={"corp":{"display_name":"C","issuer":"https://idp.example.com","client_id":"t","client_auth_method":"none"}}`)
		if err != nil {
			t.Fatal(err)
		}
		if l.Config.Auth.OIDC.Providers["corp"].Issuer != "https://idp.example.com" {
			t.Fatalf("%+v", l.Config.Auth.OIDC)
		}
	})
}
