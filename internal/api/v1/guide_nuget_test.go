package v1

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

func setupOf(t *testing.T, path string) gen.ClientSetup {
	t.Helper()
	h := setupServer(t, "https://trove.example.com", true)
	w := get(h, path)
	var out gen.ClientSetup
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	return out
}

func snippetContent(s gen.ClientSetup, id string) (string, bool) {
	for _, sn := range s.Snippets {
		if sn.Id == id {
			return sn.Content, true
		}
	}
	return "", false
}

// TestR_NUGET_026_TheSetupSnippetsOfEveryClient: the API renders the NuGet templates for a repository with the
// token reference in the syntax of each snippet's language, quotes the address and the package, and attaches the
// version the way each client takes it.
func TestR_NUGET_026_TheSetupSnippetsOfEveryClient(t *testing.T) {
	const feed = "https://trove.example.com/api/nuget/nuget-local/v3/index.json"
	dotnet := setupOf(t, "/api/v1/repositories/nuget-local/client-setup?client=dotnet&credentials=env&package=Acme.Widgets&version=1.2.3-rc.1")
	for id, want := range map[string]string{
		"dotnet.configure.shell":      "dotnet nuget add source " + feed + " --name trove \\\n  --username trove --password \"${MY_TOKEN}\" --store-password-in-clear-text\n",
		"dotnet.configure.powershell": "dotnet nuget add source " + feed + " --name trove `\n  --username trove --password \"$env:MY_TOKEN\" --store-password-in-clear-text\n",
		"dotnet.install":              "dotnet add package acme.widgets --version 1.2.3-rc.1\n",
		"dotnet.publish.shell":        "dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source " + feed + " --api-key \"${MY_TOKEN}\"\n",
		"dotnet.publish.powershell":   "dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source " + feed + " --api-key \"$env:MY_TOKEN\"\n",
	} {
		if got, ok := snippetContent(dotnet, id); !ok || got != want {
			t.Errorf("%s:\n%q\nwant\n%q", id, got, want)
		}
	}
	cfg, _ := snippetContent(dotnet, "dotnet.configure.nugetconfig")
	for _, want := range []string{`<clear />`, `value="` + feed + `"`, `<add key="ClearTextPassword" value="%MY_TOKEN%" />`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("nuget.config lacks %s:\n%s", want, cfg)
		}
	}
	for _, sn := range dotnet.Snippets {
		if sn.Id == "dotnet.configure.nugetconfig" && (sn.FileName == nil || *sn.FileName != "nuget.config" || sn.Language != "xml") {
			t.Errorf("%+v", sn)
		}
	}

	nuget := setupOf(t, "/api/v1/repositories/nuget-local/client-setup?client=nuget&credentials=env&package=Acme.Widgets&version=1.2.3")
	for id, want := range map[string]string{
		"nuget.configure.cmd":        "nuget sources add -Name trove -Source " + feed + " ^\n  -Username trove -Password %MY_TOKEN%\n",
		"nuget.install":              "nuget install acme.widgets -Version 1.2.3 -Source trove -OutputDirectory packages\n",
		"nuget.publish.cmd":          "nuget push Acme.Widgets.1.2.3.nupkg -Source " + feed + " -ApiKey %MY_TOKEN%\n",
		"nuget.publish.powershell":   "nuget push Acme.Widgets.1.2.3.nupkg -Source " + feed + " -ApiKey \"$env:MY_TOKEN\"\n",
		"nuget.configure.powershell": "nuget sources add -Name trove -Source " + feed + " `\n  -Username trove -Password \"$env:MY_TOKEN\"\n",
	} {
		if got, ok := snippetContent(nuget, id); !ok || got != want {
			t.Errorf("%s:\n%q\nwant\n%q", id, got, want)
		}
	}

	vs := setupOf(t, "/api/v1/repositories/nuget-local/client-setup?client=visual-studio&credentials=env&package=Acme.Widgets&version=1.2.3")
	if got, _ := snippetContent(vs, "visual-studio.install.packagereference"); got != "<PackageReference Include=\"acme.widgets\" Version=\"1.2.3\" />\n" {
		t.Errorf("PackageReference: %q", got)
	}
	if _, ok := snippetContent(vs, "visual-studio.publish"); ok || slices.ContainsFunc(vs.Snippets, func(s gen.ClientSnippet) bool { return s.Tab == "publish" }) {
		t.Error("Visual Studio has a publish snippet")
	}
	// A package that is no NuGet ID is refused, so no value reaches an XML attribute or a shell unchecked.
	if w := get(setupServer(t, "https://trove.example.com", true), "/api/v1/repositories/nuget-local/client-setup?client=visual-studio&package="+url.QueryEscape(`A"B`)); w.Code != 400 {
		t.Errorf("a package that is no ID: %d", w.Code)
	}
}

// TestR_NUGET_026_TheSetupWithoutACredential: with credentials=none the snippets are complete commands and files
// without any credential part, and a placeholder request gets the browser's placeholder in every spelling.
func TestR_NUGET_026_TheSetupWithoutACredential(t *testing.T) {
	none := setupOf(t, "/api/v1/repositories/nuget-local/client-setup?client=dotnet&credentials=none")
	if got, _ := snippetContent(none, "dotnet.configure.shell"); got != "dotnet nuget add source https://trove.example.com/api/nuget/nuget-local/v3/index.json --name trove\n" {
		t.Errorf("%q", got)
	}
	if cfg, _ := snippetContent(none, "dotnet.configure.nugetconfig"); strings.Contains(cfg, "Password") || strings.Contains(cfg, "packageSourceCredentials") {
		t.Errorf("%s", cfg)
	}
	ph := setupOf(t, "/api/v1/repositories/nuget-local/client-setup?client=nuget&credentials=placeholder")
	for _, id := range []string{"nuget.configure.cmd", "nuget.configure.powershell", "nuget.publish.cmd"} {
		if got, _ := snippetContent(ph, id); !strings.Contains(got, "{{token}}") || strings.Contains(got, "TROVE_TOKEN") {
			t.Errorf("%s: %q", id, got)
		}
	}
}

// TestR_UI_049_TheNuGetGuideShowsTheSnippetsOfTheClientSetup: for every client, the snippets the guide of a NuGet
// repository attaches are the client setup's answer for the same repository with credentials=env, byte for byte,
// and the guide answers for the three clients and no other.
func TestR_UI_049_TheNuGetGuideShowsTheSnippetsOfTheClientSetup(t *testing.T) {
	h := setupServer(t, "https://trove.example.com", true)
	for _, client := range []string{"dotnet", "nuget", "visual-studio"} {
		g := guideOf(t, get(h, "/api/v1/repositories/nuget-local/guide?client="+client))
		setup := setupOf(t, "/api/v1/repositories/nuget-local/client-setup?credentials=env&client="+client)
		for _, tab := range []string{"configure", "install", "publish"} {
			var want []gen.ClientSnippet
			for _, sn := range setup.Snippets {
				if string(sn.Tab) == tab {
					want = append(want, sn)
				}
			}
			var got []gen.ClientSnippet
			for _, s := range g.Sections {
				if s.Id == "nuget."+tab {
					got = s.Snippets
				}
			}
			if !slices.EqualFunc(got, want, snippetsEqual) {
				t.Errorf("%s %s: guide %+v, setup %+v", client, tab, got, want)
			}
			if tab != "publish" && len(want) == 0 {
				t.Errorf("%s has no %s snippet", client, tab)
			}
		}
		if g.RegistryUrl != setup.RegistryUrl || g.Format != "nuget" || g.Credentials != gen.GuideCredentialsEnv || g.Client != client {
			t.Errorf("%s: %+v", client, g)
		}
		if got := sectionIDs(g); got[0] != "nuget.local.what" || got[len(got)-1] != "nuget.troubleshooting" {
			t.Errorf("%s: sections %v", client, got)
		}
		conf := sectionOf(t, g, "nuget.configure")
		if !strings.Contains(conf.Html, "&lt;clear /&gt;") || strings.Contains(conf.Html, "TROVE_TOKEN") || strings.Contains(conf.Html, "{{") {
			t.Errorf("%s: %s", client, conf.Html)
		}
	}
	if w := get(h, "/api/v1/repositories/nuget-local/guide?client=npm"); w.Code != 400 {
		t.Errorf("a client of another format: %d", w.Code)
	}
}
