package v1

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

// nugetTemplates are the shapes NuGet needs and npm does not: a token reference in
// the syntax of Windows and of PowerShell, and a version the client takes as an option.
func nugetTemplates() []formats.SetupTemplate {
	return []formats.SetupTemplate{
		{
			ID: "n.config.configure", Client: "n", Section: "configure", Lang: "xml", File: "nuget.config",
			Template: `<add key="Username" value="{{xml .Username}}" />` + "\n" + `<add key="ClearTextPassword" value="` + formats.SetupTokenRefWin + `" />` + "\n",
		},
		{ID: "n.ps.configure", Client: "n", Section: "configure", Lang: "powershell", Template: `$h = @{ Authorization = "Bearer ` + formats.SetupTokenRefPS + `" }` + "\n"},
		{ID: "n.sh.configure", Client: "n", Section: "configure", Lang: "shell", Template: `curl -H "Authorization: Bearer ` + formats.SetupTokenRef + `" x` + "\n"},
		{ID: "n.install", Client: "n", Section: "install", Lang: "shell", Template: "dotnet add package " + formats.SetupPackageRef + "\n", VersionSpec: " --version " + formats.SetupVersionRef},
		{ID: "n.ps.install", Client: "n", Section: "install", Lang: "powershell", Template: "Install-Package " + formats.SetupPackageRef + "\n", VersionSpec: " -RequiredVersion " + formats.SetupVersionRef},
		{ID: "n.cmd.install", Client: "n", Section: "install", Lang: "cmd", Template: "nuget install " + formats.SetupPackageRef + "\n", VersionSpec: " -Version " + formats.SetupVersionRef},
	}
}

func mustRender(t *testing.T, in setupInput) []snippet {
	t.Helper()
	ss, err := renderSnippets(in)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}

func TestR_API_030_EachShellGetsTheTokenReferenceItExpands(t *testing.T) {
	in := setupInput{Templates: nugetTemplates(), Client: "n", TokenEnvVar: "MY_TOKEN", Data: testData()}
	for _, tc := range []struct {
		mode string
		want map[string]string
	}{
		{credEnv, map[string]string{
			"n.configure.config": `<add key="Username" value="" />` + "\n" + `<add key="ClearTextPassword" value="%MY_TOKEN%" />` + "\n",
			"n.configure.ps":     `$h = @{ Authorization = "Bearer $env:MY_TOKEN" }` + "\n",
			"n.configure.sh":     `curl -H "Authorization: Bearer ${MY_TOKEN}" x` + "\n",
		}},
		{credPlaceholder, map[string]string{
			"n.configure.config": `<add key="Username" value="" />` + "\n" + `<add key="ClearTextPassword" value="{{token}}" />` + "\n",
			"n.configure.ps":     `$h = @{ Authorization = "Bearer {{token}}" }` + "\n",
			"n.configure.sh":     `curl -H "Authorization: Bearer {{token}}" x` + "\n",
		}},
		{credNone, map[string]string{
			"n.configure.config": `<add key="Username" value="" />` + "\n",
			"n.configure.ps":     "",
			"n.configure.sh":     "",
		}},
	} {
		in.Credentials = tc.mode
		ss := mustRender(t, in)
		m := byID(ss)
		for id, want := range tc.want {
			if got := m[id].Content; got != want {
				t.Errorf("%s %s:\n%q\nwant\n%q", tc.mode, id, got, want)
			}
		}
		for _, s := range ss {
			for _, ref := range formats.SetupTokenRefs() {
				if strings.Contains(s.Content, ref) {
					t.Errorf("%s: %s still holds %s", tc.mode, s.ID, ref)
				}
			}
		}
	}
}

func TestR_API_030_TheFormatChoosesHowTheVersionIsAttached(t *testing.T) {
	in := setupInput{Templates: nugetTemplates(), Client: "n", Credentials: credNone, Data: testData(), Package: "Acme.Widgets", Version: "1.2.3-beta.1"}
	m := byID(mustRender(t, in))
	for id, want := range map[string]string{
		"n.install":     "dotnet add package Acme.Widgets --version 1.2.3-beta.1\n",
		"n.install.ps":  "Install-Package Acme.Widgets -RequiredVersion 1.2.3-beta.1\n",
		"n.install.cmd": "nuget install Acme.Widgets -Version 1.2.3-beta.1\n",
	} {
		if got := m[id].Content; got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
	in.Version = ""
	if got := byID(mustRender(t, in))["n.install"].Content; got != "dotnet add package Acme.Widgets\n" {
		t.Errorf("without a version: %q", got)
	}
	// A version the shell would interpret is quoted on its own, inside the option.
	in.Version = "1 $(id)"
	if got := byID(mustRender(t, in))["n.install"].Content; got != "dotnet add package Acme.Widgets --version '1 $(id)'\n" {
		t.Errorf("a hostile version: %q", got)
	}
	// Without a VersionSpec the version stays attached with "@" (npm).
	npm := setupInput{Templates: fakeTemplates(), Client: "x", Credentials: credNone, Data: testData(), Package: "left-pad", Version: "1.0.0"}
	if got := byID(mustRender(t, npm))["x.install"].Content; got != "x add left-pad@1.0.0\n" {
		t.Errorf("default: %q", got)
	}
}

func TestR_API_030_PowerShellAndCmdValuesAreQuoted(t *testing.T) {
	for in, want := range map[string]string{
		"Acme.Widgets": "Acme.Widgets",
		"it's":         "'it''s'",
		"$(id)":        "'$(id)'",
		"a b":          "'a b'",
		"\u2018x":      "'\u2018\u2018x'",
		"":             "''",
	} {
		if got := powershellQuote(in); got != want {
			t.Errorf("powershellQuote(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Acme.Widgets": "Acme.Widgets",
		"%PATH%":       `"%%PATH%%"`,
		`a"b`:          `"a""b"`,
		"a&b":          `"a&b"`,
		"a\nb":         `"ab"`,
	} {
		if got := cmdQuote(in); got != want {
			t.Errorf("cmdQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
