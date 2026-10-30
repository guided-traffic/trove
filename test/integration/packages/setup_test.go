//go:build integration

package packages

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// snippets returns the content of the snippets of an answer by id.
func snippets(t testing.TB, a testenv.Answer) map[string]string {
	t.Helper()
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	out := map[string]string{}
	for _, s := range a.JSON(t)["snippets"].([]any) {
		m := s.(map[string]any)
		out[m["id"].(string)] = m["content"].(string)
	}
	return out
}

func (e *env) setup(token, repo, query string) testenv.Answer {
	e.t.Helper()
	return e.get(token, "/api/v1/repositories/"+repo+"/client-setup?"+query)
}

// TestR_API_030_ClientSetupCredentialModes: the three modes of credentials, with the
// addresses from the configured base URL.
func TestR_API_030_ClientSetupCredentialModes(t *testing.T) {
	e := newEnv(t)

	t.Run("placeholder is the default and holds the literal the browser replaces", func(t *testing.T) {
		a := e.setup(e.tokA, "npm-local", "client=npm")
		s := snippets(t, a)
		v := a.JSON(t)
		if v["registryUrl"] != "http://trove.test/api/npm/npm-local/" || v["endpointPath"] != "//trove.test/api/npm/npm-local/" ||
			v["username"] != "alice" || v["publishTarget"] != "npm-local" || num(v["tokenTtlSeconds"]) != 720*3600 ||
			v["credentials"] != "placeholder" || v["repository"] != "npm-local" || v["format"] != "npm" || v["client"] != "npm" {
			t.Fatalf("%v", v)
		}
		want := "registry=http://trove.test/api/npm/npm-local/\n//trove.test/api/npm/npm-local/:_authToken={{token}}\n"
		if s["npm.configure.npmrc"] != want {
			t.Fatalf("npmrc:\n%q\nwant\n%q", s["npm.configure.npmrc"], want)
		}
		if s["npm.install"] != "npm install <package>\n" {
			t.Fatalf("install %q", s["npm.install"])
		}
	})
	t.Run("env holds the reference of the variable, as the acceptance case of the spec has it", func(t *testing.T) {
		s := snippets(t, e.setup(e.tokA, "npm-local", "client=npm&credentials=env"))
		if !strings.Contains(s["npm.configure.npmrc"], "//trove.test/api/npm/npm-local/:_authToken=${TROVE_TOKEN}") {
			t.Fatalf("%q", s["npm.configure.npmrc"])
		}
	})
	t.Run("the variable is ui.client_setup.token_env_var", func(t *testing.T) {
		other := e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Env: []string{"TROVE_UI_CLIENT_SETUP_TOKEN_ENV_VAR=NPM_TOKEN", "TROVE_UI_CLIENT_SETUP_TOKEN_TTL=24h"}})
		a := e.st.Do(other, "GET", "/api/v1/repositories/npm-local/client-setup?client=yarn&credentials=env", e.tokA, nil)
		s := snippets(t, a)
		if got := s["yarn.configure.yarnrc"]; !strings.Contains(got, `npmAuthToken: "${NPM_TOKEN}"`) || strings.Contains(got, "TROVE_TOKEN") {
			t.Fatalf("%q", got)
		}
		if num(a.JSON(t)["tokenTtlSeconds"]) != 86400 {
			t.Fatalf("tokenTtlSeconds %v", a.JSON(t)["tokenTtlSeconds"])
		}
	})
	t.Run("none holds no authentication line and no always-auth switch", func(t *testing.T) {
		for client, id := range map[string]string{"npm": "npm.configure.npmrc", "pnpm": "pnpm.configure.npmrc", "yarn": "yarn.configure.yarnrc", "yarn-classic": "yarn-classic.configure.npmrc"} {
			s := snippets(t, e.setup(e.tokA, "npm-local", "client="+client+"&credentials=none"))
			got := s[id]
			if got == "" || strings.Contains(got, "uth") || strings.Contains(got, "TOKEN") || strings.Contains(got, "{{token}}") {
				t.Errorf("%s: %q", client, got)
			}
		}
		s := snippets(t, e.setup(e.tokA, "npm-local", "client=npm&credentials=none"))
		if s["npm.configure.npmrc"] != "registry=http://trove.test/api/npm/npm-local/\n" {
			t.Fatalf("%q", s["npm.configure.npmrc"])
		}
	})
	t.Run("a repository that is not anonymously readable asks the client to send its credential first", func(t *testing.T) {
		classic := snippets(t, e.setup(e.tokA, "npm-local", "client=yarn-classic"))["yarn-classic.configure.npmrc"]
		if !strings.Contains(classic, "always-auth=true") {
			t.Fatalf("%q", classic)
		}
		berry := snippets(t, e.setup(e.tokA, "npm-local", "client=yarn"))["yarn.configure.yarnrc"]
		if !strings.Contains(berry, "npmAlwaysAuth: true") {
			t.Fatalf("%q", berry)
		}
	})
	t.Run("a value the API does not know is refused", func(t *testing.T) {
		for _, q := range []string{"client=bun", "client=npm&credentials=token", "client=yarn-berry", "client=npm&package=" + strings.Repeat("x", 300), "client=npm&package=a%0Ab"} {
			if a := e.setup(e.tokA, "npm-local", q); a.Status != 400 {
				t.Errorf("%s: %d %s", q, a.Status, a.Body)
			}
		}
		if a := e.get(e.tokA, "/api/v1/repositories/npm-local/client-setup"); a.Status != 400 {
			t.Errorf("no client: %d", a.Status)
		}
	})
}

// TestR_API_030_ClientSetupNeverHoldsAToken: no answer, for any client, mode and
// caller, matches the shape of a token, and a token handed in as input is refused
// rather than echoed.
func TestR_API_030_ClientSetupNeverHoldsAToken(t *testing.T) {
	e := newEnv(t)
	shape := regexp.MustCompile(`trv_(pat|sat|cit)_`)
	n := 0
	for _, tok := range []string{e.tokA, e.tokB, e.tokE} {
		for _, client := range []string{"npm", "pnpm", "yarn", "yarn-classic"} {
			for _, creds := range []string{"placeholder", "env", "none"} {
				for _, extra := range []string{"", "&package=left-pad&version=1.0.0", "&scope=%40team-a", "&package=a(b)&version=1.0.0&scope=%40x"} {
					a := e.setup(tok, "npm-local", "client="+client+"&credentials="+creds+extra)
					if a.Status != 200 {
						t.Fatalf("%d %s", a.Status, a.Body)
					}
					if shape.Match(a.Body) {
						t.Fatalf("an answer holds a token: %s", a.Body)
					}
					n++
				}
			}
		}
	}
	t.Logf("%d answers checked", n)
	for _, q := range []string{"package=" + e.tokA, "version=" + e.tokA, "scope=" + e.tokA, "package=trv_sat_abc"} {
		if a := e.setup(e.tokA, "npm-local", "client=npm&"+q); a.Status != 400 || shape.Match(a.Body) {
			t.Errorf("%s: %d %s", q[:12], a.Status, a.Body)
		}
	}
}

// TestR_API_030_ClientSetupQuoting: the install snippets quote what they substitute.
func TestR_API_030_ClientSetupQuoting(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ client, id, pkg, version, want string }{
		{"npm", "npm.install", "@team-a/ui", "1.4.0", "npm install @team-a/ui@1.4.0\n"},
		{"npm", "npm.install", "a(b)", "1.0.0", "npm install 'a(b)@1.0.0'\n"},
		{"npm", "npm.install", "it's", "1.0.0", `npm install 'it'\''s@1.0.0'` + "\n"},
		{"npm", "npm.install", "a!b*", "2.0.0-beta.1", "npm install 'a!b*@2.0.0-beta.1'\n"},
		{"npm", "npm.install", "left-pad", "", "npm install left-pad\n"},
		{"pnpm", "pnpm.install", "$(touch x)", "1.0.0", "pnpm add '$(touch x)@1.0.0'\n"},
		{"yarn", "yarn.install", "a b", "1.0.0", "yarn add 'a b@1.0.0'\n"},
		{"yarn-classic", "yarn-classic.install", "`id`", "", "yarn add '`id`'\n"},
	}
	for _, c := range cases {
		q := "client=" + c.client + "&credentials=none&package=" + url.QueryEscape(c.pkg)
		if c.version != "" {
			q += "&version=" + url.QueryEscape(c.version)
		}
		if got := snippets(t, e.setup(e.tokA, "npm-local", q))[c.id]; got != c.want {
			t.Errorf("%s %q: %q, want %q", c.client, c.pkg, got, c.want)
		}
	}
	t.Run("a version without a package is ignored", func(t *testing.T) {
		if got := snippets(t, e.setup(e.tokA, "npm-local", "client=npm&version=1.0.0"))["npm.install"]; got != "npm install <package>\n" {
			t.Fatalf("%q", got)
		}
	})
}

// TestR_API_030_ClientSetupScopeAndPermissions: the scoped variant, the read permission and
// the publish target.
func TestR_API_030_ClientSetupScopeAndPermissions(t *testing.T) {
	e := newEnv(t)
	t.Run("a scope adds the scoped variant of the registry configuration", func(t *testing.T) {
		s := snippets(t, e.setup(e.tokA, "npm-local", "client=npm&scope=%40team-a"))
		if got := s["npm.configure.npmrc-scoped"]; !strings.HasPrefix(got, "@team-a:registry=http://trove.test/api/npm/npm-local/\n") || !strings.Contains(got, ":_authToken={{token}}") {
			t.Fatalf("%q", got)
		}
		if !strings.HasPrefix(s["npm.configure.npmrc"], "registry=") {
			t.Fatalf("the default variant is missing: %v", s)
		}
		y := snippets(t, e.setup(e.tokA, "npm-local", "client=yarn&scope=team-a&credentials=env"))["yarn.configure.yarnrc-scoped"]
		if !strings.Contains(y, "npmScopes:\n  team-a:\n    npmRegistryServer: \"http://trove.test/api/npm/npm-local/\"") {
			t.Fatalf("%q", y)
		}
		if a := e.setup(e.tokA, "npm-local", "client=npm&scope=a%2Fb"); a.Status != 400 {
			t.Fatalf("a scope with a slash: %d", a.Status)
		}
	})
	t.Run("a caller who may read but not deploy gets no publish target", func(t *testing.T) {
		v := e.setup(e.tokB, "npm-local", "client=npm").JSON(t)
		if v["publishTarget"] != nil || v["username"] != "bob" {
			t.Fatalf("%v", v)
		}
	})
	t.Run("a caller who may not read the repository gets the 404 of a missing one; an anonymous one 401", func(t *testing.T) {
		missing := e.setup(e.tokD, "npm-nowhere", "client=npm")
		hidden := e.setup(e.tokD, "npm-local", "client=npm")
		if missing.Status != 404 || hidden.Status != 404 || missing.Code(t) != hidden.Code(t) {
			t.Fatalf("missing %d %s, hidden %d %s", missing.Status, missing.Body, hidden.Status, hidden.Body)
		}
		if a := e.setup("", "npm-local", "client=npm"); a.Status != 401 {
			t.Fatalf("anonymous: %d", a.Status)
		}
	})
	t.Run("the formats list carries the base URL for every caller", func(t *testing.T) {
		for _, tok := range []string{e.tokA, e.tokD} {
			a := e.get(tok, "/api/v1/formats")
			if a.Status != 200 || a.JSON(t)["baseUrl"] != "http://trove.test" {
				t.Fatalf("%d %s", a.Status, a.Body)
			}
		}
	})
}
