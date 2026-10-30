package npm

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"github.com/guided-traffic/trove/internal/formats"
)

func renderSetup(t *testing.T, tpl formats.SetupTemplate, d formats.SetupData) string {
	t.Helper()
	tm, err := template.New(tpl.ID).Option("missingkey=error").Parse(tpl.Template)
	if err != nil {
		t.Fatalf("%s: %v", tpl.ID, err)
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, d); err != nil {
		t.Fatalf("%s: %v", tpl.ID, err)
	}
	return b.String()
}

func setupData(scope string, auth bool) formats.SetupData {
	return formats.SetupData{
		RegistryURL: "https://trove.example.com/api/npm/npm-all/", Repository: "npm-all", Endpoint: "default", Scope: scope,
		RegistryKey: "//trove.example.com/api/npm/npm-all/", RequiresAuth: auth,
	}
}

// TestR_NPM_041_SnippetLint is the lint of R-NPM-041 over the generated snippets: no
// credential appended to the registry value, no unkeyed credential, the keyed form with
// the environment variable, and the always-auth switches exactly where the repository
// is not anonymously readable.
func TestR_NPM_041_SnippetLint(t *testing.T) {
	f := New()
	tpls := f.SetupTemplates()
	if len(tpls) == 0 {
		t.Fatal("no templates")
	}
	appended := regexp.MustCompile(`registry=.*_auth`)
	unkeyed := regexp.MustCompile(`(?m)^\s*(_auth|_authToken|_password|username)\b`)
	for _, tpl := range tpls {
		for _, scope := range []string{"", "acme"} {
			for _, auth := range []bool{false, true} {
				out := renderSetup(t, tpl, setupData(scope, auth))
				name := tpl.ID + " scope=" + scope
				if appended.MatchString(out) {
					t.Errorf("R-NPM-041 %s: a credential is appended to registry=:\n%s", name, out)
				}
				if unkeyed.MatchString(out) {
					t.Errorf("R-NPM-041 %s: an unkeyed credential:\n%s", name, out)
				}
				if strings.Contains(out, "<no value>") || strings.Contains(out, "{{") {
					t.Errorf("R-NPM-041 %s: unresolved template text:\n%s", name, out)
				}
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, "_authToken") && !strings.HasPrefix(line, "//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}") {
						t.Errorf("R-NPM-041 %s: the credential line is not the keyed form: %q", name, line)
					}
					if strings.Contains(line, "npmAuthToken") && !strings.Contains(line, `"${TROVE_TOKEN}"`) {
						t.Errorf("R-NPM-041 %s: the token is not the variable: %q", name, line)
					}
				}
				switch tpl.Client + "/" + tpl.Section {
				case "yarn-classic/configure":
					if got := strings.Contains(out, "always-auth=true"); got != auth {
						t.Errorf("R-NPM-004 %s auth=%v: always-auth present %v", name, auth, got)
					}
				case "yarn/configure":
					if got := strings.Contains(out, "npmAlwaysAuth: true"); got != (auth && scope == "") {
						t.Errorf("R-NPM-004 %s auth=%v: npmAlwaysAuth present %v", name, auth, got)
					}
				case "npm/configure", "pnpm/configure":
					if strings.Contains(out, "always-auth") {
						t.Errorf("R-NPM-004 %s: always-auth is Yarn 1's", name)
					}
				}
			}
		}
	}
	t.Run("R-NPM-041 the scoped form binds the scope, the unscoped form the default registry", func(t *testing.T) {
		out := renderSetup(t, tpls[0], setupData("acme", false))
		if !strings.HasPrefix(out, "@acme:registry=https://trove.example.com/api/npm/npm-all/\n//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}") {
			t.Errorf("%q", out)
		}
		out = renderSetup(t, tpls[0], setupData("", false))
		if !strings.HasPrefix(out, "registry=https://trove.example.com/api/npm/npm-all/\n") {
			t.Errorf("%q", out)
		}
	})
	t.Run("R-NPM-041 every client of the descriptor has a configure and an install snippet", func(t *testing.T) {
		have := map[string]bool{}
		for _, tpl := range tpls {
			have[tpl.Client+"/"+tpl.Section] = true
			if tpl.ID == "" || tpl.Lang == "" {
				t.Errorf("%+v", tpl)
			}
		}
		for _, c := range f.Descriptor().Clients {
			for _, s := range []string{"configure", "install"} {
				if !have[c.ID+"/"+s] {
					t.Errorf("%s has no %s snippet", c.ID, s)
				}
			}
		}
	})
	t.Run("R-NPM-041 a template that names a credential does not execute", func(t *testing.T) {
		tm := template.Must(template.New("x").Option("missingkey=error").Parse("{{.Token}}"))
		if err := tm.Execute(&bytes.Buffer{}, setupData("", false)); err == nil {
			t.Fatal("SetupData carries a credential field")
		}
	})
}

// TestR_NPM_041_PnpmCredentialsLiveInTheUserFile: pnpm 11 and 12 ignore a credential with an environment variable
// in a project .npmrc (their warning: "environment variables are not expanded in registry credentials that come
// from a project .npmrc"; seen by the client matrix, E2.1), so the pnpm snippet names the user-level file. The
// other clients read the file of the project.
func TestR_NPM_041_PnpmCredentialsLiveInTheUserFile(t *testing.T) {
	want := map[string]string{"npm": ".npmrc", "pnpm": "~/.npmrc", "yarn": ".yarnrc.yml", "yarn-classic": ".npmrc"}
	for _, tpl := range New().SetupTemplates() {
		if tpl.Section != "configure" {
			continue
		}
		if tpl.File != want[tpl.Client] {
			t.Errorf("%s configure snippet targets %q, want %q", tpl.Client, tpl.File, want[tpl.Client])
		}
	}
}
