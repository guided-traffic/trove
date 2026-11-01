package nuget

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"text/template"

	"github.com/guided-traffic/trove/internal/formats"
)

func renderSetup(t *testing.T, tpl formats.SetupTemplate, d formats.SetupData) string {
	t.Helper()
	id := func(v string) string { return v }
	tm, err := template.New(tpl.ID).Option("missingkey=error").Funcs(template.FuncMap{
		"shell": id, "powershell": id, "cmd": id, "xml": id, "schemeless": id,
	}).Parse(tpl.Template)
	if err != nil {
		t.Fatalf("%s: %v", tpl.ID, err)
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, d); err != nil {
		t.Fatalf("%s: %v", tpl.ID, err)
	}
	return b.String()
}

func setupData(auth bool) formats.SetupData {
	return formats.SetupData{RegistryURL: "https://trove.example.com/api/nuget/nuget-all/", Repository: "nuget-all", Endpoint: "default", RequiresAuth: auth}
}

// refFor is the spelling of the token reference that the language of a template allows.
var refFor = map[string]string{
	"shell": formats.SetupTokenRef, "powershell": formats.SetupTokenRefPS, "cmd": formats.SetupTokenRefWin, "xml": formats.SetupTokenRefWin,
}

// TestR_NUGET_026_TheTemplatesSpellTheCredentialInTheirOwnSyntax: a template spells the token reference of
// its own language (a nuget.config and cmd %V%, a POSIX shell ${V}, PowerShell $env:V) and no other, only
// for a repository that is not anonymously readable, and a snippet without credential parts is a working
// command or file.
func TestR_NUGET_026_TheTemplatesSpellTheCredentialInTheirOwnSyntax(t *testing.T) {
	tpls := New().SetupTemplates()
	if len(tpls) == 0 {
		t.Fatal("no templates")
	}
	for _, tpl := range tpls {
		withAuth, without := renderSetup(t, tpl, setupData(true)), renderSetup(t, tpl, setupData(false))
		want := refFor[tpl.Lang]
		for _, ref := range formats.SetupTokenRefs() {
			if strings.Contains(withAuth, ref) != (ref == want && spellsCredential(tpl)) {
				t.Errorf("R-NUGET-026 %s (%s): the reference %s in\n%s", tpl.ID, tpl.Lang, ref, withAuth)
			}
		}
		if tpl.Section == "configure" {
			for _, ref := range formats.SetupTokenRefs() {
				if strings.Contains(without, ref) {
					t.Errorf("R-NUGET-026 %s: a repository open to anonymous readers still gets a credential:\n%s", tpl.ID, without)
				}
			}
		}
		for _, out := range []string{withAuth, without} {
			if strings.Contains(out, "<no value>") || strings.Contains(out, "{{") || strings.Contains(out, "v2") {
				t.Errorf("R-NUGET-026 %s: %q", tpl.ID, out)
			}
			if tpl.Section != "install" && !strings.Contains(out, "https://trove.example.com/api/nuget/nuget-all/v3/index.json") {
				t.Errorf("R-NUGET-026 %s: not the index.json address:\n%s", tpl.ID, out)
			}
		}
	}
}

func spellsCredential(tpl formats.SetupTemplate) bool { return tpl.Section != "install" }

// TestR_NUGET_026_TheNuGetConfigIsWellFormed: the nuget.config of the three clients is the one of the
// specification: <clear />, one source with protocolVersion 3, and the credentials of that source only
// for a repository that needs them, with the environment reference in ClearTextPassword.
func TestR_NUGET_026_TheNuGetConfigIsWellFormed(t *testing.T) {
	var files int
	for _, tpl := range New().SetupTemplates() {
		if tpl.File != "nuget.config" {
			continue
		}
		files++
		for _, auth := range []bool{true, false} {
			out := renderSetup(t, tpl, setupData(auth))
			if !strings.HasPrefix(out, "<?xml") {
				t.Errorf("%s: %q", tpl.ID, out)
			}
			var names []string
			dec := xml.NewDecoder(strings.NewReader(out))
			for {
				tok, err := dec.Token()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("%s auth=%v is not well-formed XML: %v\n%s", tpl.ID, auth, err, out)
				}
				if se, ok := tok.(xml.StartElement); ok {
					names = append(names, se.Name.Local)
				}
			}
			want := []string{"configuration", "packageSources", "clear", "add"}
			if auth {
				want = append(want, "packageSourceCredentials", "trove", "add", "add")
			}
			if strings.Join(names, " ") != strings.Join(want, " ") {
				t.Errorf("%s auth=%v: elements %v, want %v", tpl.ID, auth, names, want)
			}
			if strings.Contains(out, "ClearTextPassword") != auth {
				t.Errorf("%s auth=%v: ClearTextPassword present is %v", tpl.ID, auth, !auth)
			}
			if auth && !strings.Contains(out, `<add key="ClearTextPassword" value="%TROVE_TOKEN%" />`) {
				t.Errorf("%s: the password is not the environment reference:\n%s", tpl.ID, out)
			}
			if !strings.Contains(out, `protocolVersion="3"`) {
				t.Errorf("%s: no protocolVersion", tpl.ID)
			}
		}
	}
	if files != 3 {
		t.Errorf("%d nuget.config templates, want one per client", files)
	}
}

// TestR_NUGET_026_TheCommandsAreTheOnesOfTheSpecification: the dotnet and nuget.exe commands add the
// source with the credential and the clear-text switch the platform needs, and push with the token as the
// API key.
func TestR_NUGET_026_TheCommandsAreTheOnesOfTheSpecification(t *testing.T) {
	byID := map[string]formats.SetupTemplate{}
	for _, tpl := range New().SetupTemplates() {
		byID[tpl.ID] = tpl
	}
	const url = "https://trove.example.com/api/nuget/nuget-all/v3/index.json"
	for id, want := range map[string]string{
		"dotnet.shell.configure":      "dotnet nuget add source " + url + " --name trove \\\n  --username trove --password \"${TROVE_TOKEN}\" --store-password-in-clear-text\n",
		"dotnet.powershell.configure": "dotnet nuget add source " + url + " --name trove `\n  --username trove --password \"$env:TROVE_TOKEN\" --store-password-in-clear-text\n",
		"nuget.cmd.configure":         "nuget sources add -Name trove -Source " + url + " ^\n  -Username trove -Password %TROVE_TOKEN%\n",
		"nuget.powershell.configure":  "nuget sources add -Name trove -Source " + url + " `\n  -Username trove -Password \"$env:TROVE_TOKEN\"\n",
		"dotnet.shell.publish":        "dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source " + url + " --api-key \"${TROVE_TOKEN}\"\n",
		"dotnet.powershell.publish":   "dotnet nuget push bin/Release/Acme.Widgets.1.2.3.nupkg --source " + url + " --api-key \"$env:TROVE_TOKEN\"\n",
		"nuget.cmd.publish":           "nuget push Acme.Widgets.1.2.3.nupkg -Source " + url + " -ApiKey %TROVE_TOKEN%\n",
		"nuget.powershell.publish":    "nuget push Acme.Widgets.1.2.3.nupkg -Source " + url + " -ApiKey \"$env:TROVE_TOKEN\"\n",
	} {
		tpl, ok := byID[id]
		if !ok {
			t.Errorf("no template %s", id)
			continue
		}
		if got := renderSetup(t, tpl, setupData(true)); got != want {
			t.Errorf("R-NUGET-026 %s:\n%s\nwant\n%s", id, got, want)
		}
	}
	// Without a credential the command is complete on one line.
	if got := renderSetup(t, byID["dotnet.shell.configure"], setupData(false)); got != "dotnet nuget add source "+url+" --name trove\n" {
		t.Errorf("anonymous: %q", got)
	}
	if got := renderSetup(t, byID["nuget.cmd.configure"], setupData(false)); got != "nuget sources add -Name trove -Source "+url+"\n" {
		t.Errorf("anonymous nuget.exe: %q", got)
	}
}

// TestR_NUGET_026_TheInstallTemplatesTakeAVersion: dotnet and nuget.exe attach the version with their own
// option, Visual Studio writes the Version attribute of the PackageReference, and every install template
// places the package the API substitutes.
func TestR_NUGET_026_TheInstallTemplatesTakeAVersion(t *testing.T) {
	want := map[string]string{
		"dotnet.install": " --version " + formats.SetupVersionRef, "nuget.install": " -Version " + formats.SetupVersionRef,
		"visual-studio.packagereference.install": "\" Version=\"" + formats.SetupVersionRef,
	}
	seen := 0
	for _, tpl := range New().SetupTemplates() {
		if tpl.Section != "install" {
			if tpl.VersionSpec != "" {
				t.Errorf("%s: a VersionSpec on a %s template", tpl.ID, tpl.Section)
			}
			continue
		}
		seen++
		if tpl.VersionSpec != want[tpl.ID] || !strings.Contains(tpl.Template, formats.SetupPackageRef) {
			t.Errorf("%s: VersionSpec %q, template %q", tpl.ID, tpl.VersionSpec, tpl.Template)
		}
		// what the API makes of it for Acme.Widgets 1.2.3
		out := strings.ReplaceAll(tpl.Template, formats.SetupPackageRef, "Acme.Widgets"+strings.ReplaceAll(tpl.VersionSpec, formats.SetupVersionRef, "1.2.3"))
		if tpl.Client == ClientVisualStudio {
			var ref struct {
				Include string `xml:"Include,attr"`
				Version string `xml:"Version,attr"`
			}
			if err := xml.Unmarshal([]byte(out), &ref); err != nil || ref.Include != "Acme.Widgets" || ref.Version != "1.2.3" {
				t.Errorf("%s: %q gives %+v, %v", tpl.ID, out, ref, err)
			}
		}
	}
	if seen != 3 {
		t.Errorf("%d install templates, want one per client", seen)
	}
}

// TestR_NUGET_026_EveryClientHasConfigureAndInstall: the clients are the ones of the specification, each
// with the sections the kit requires, and only the two command line clients push.
func TestR_NUGET_026_EveryClientHasConfigureAndInstall(t *testing.T) {
	f := New()
	sections := map[string]map[string]bool{}
	for _, tpl := range f.SetupTemplates() {
		if sections[tpl.Client] == nil {
			sections[tpl.Client] = map[string]bool{}
		}
		sections[tpl.Client][tpl.Section] = true
	}
	var ids []string
	for _, c := range f.Descriptor().Clients {
		ids = append(ids, c.ID)
		s := sections[c.ID]
		if !s["configure"] || !s["install"] || s["publish"] != (c.ID != ClientVisualStudio) {
			t.Errorf("client %s has the sections %v", c.ID, s)
		}
	}
	if strings.Join(ids, " ") != "dotnet nuget visual-studio" {
		t.Errorf("clients %v", ids)
	}
}
