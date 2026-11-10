package ansible

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
	"text/template"

	"github.com/guided-traffic/trove/internal/formats"
)

// renderSetup runs a template with the identity quoting functions of the API plus the real hostname.
func renderSetup(t *testing.T, tpl formats.SetupTemplate, d formats.SetupData) string {
	t.Helper()
	id := func(v string) string { return v }
	tm, err := template.New(tpl.ID).Option("missingkey=error").Funcs(template.FuncMap{
		"shell": id, "powershell": id, "cmd": id, "xml": id, "schemeless": id,
		"hostname": func(u string) string { p, _ := url.Parse(u); return p.Hostname() },
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
	return formats.SetupData{RegistryURL: "https://trove.example.com:8443/api/ansible/web/galaxy/", Repository: "web/galaxy", Project: "web", Key: "galaxy", RequiresAuth: auth}
}

func templateByID(t *testing.T, id string) formats.SetupTemplate {
	t.Helper()
	for _, tpl := range New().SetupTemplates() {
		if tpl.ID == id {
			return tpl
		}
	}
	t.Fatalf("no template %s", id)
	return formats.SetupTemplate{}
}

// TestR_ANS_025_TheTokenIsAnEnvironmentVariableAndNeverInTheConfigFile: the ansible.cfg snippet names the server trove with the
// repository address and holds no credential line (the file has no variable expansion), the token comes from the variable
// ANSIBLE_GALAXY_SERVER_TROVE_TOKEN in the spelling of each shell (Q-ANS-22), and an anonymously readable repository gets no
// credential line at all.
func TestR_ANS_025_TheTokenIsAnEnvironmentVariableAndNeverInTheConfigFile(t *testing.T) {
	cfg := renderSetup(t, templateByID(t, "ansible-galaxy.ansible-cfg.configure"), setupData(true))
	for _, want := range []string{"server_list = trove", "[galaxy_server.trove]", "url=https://trove.example.com:8443/api/ansible/web/galaxy/\n"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("ansible.cfg lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(strings.ToLower(cfg), "token") {
		t.Errorf("ansible.cfg spells a token:\n%s", cfg)
	}
	for id, want := range map[string]string{
		"ansible-galaxy.token-env.configure":            "export ANSIBLE_GALAXY_SERVER_TROVE_TOKEN=\"" + formats.SetupTokenRef + "\"\n",
		"ansible-galaxy.token-env-powershell.configure": "$env:ANSIBLE_GALAXY_SERVER_TROVE_TOKEN = \"" + formats.SetupTokenRefPS + "\"\n",
		"ansible-galaxy.token-env-cmd.configure":        "set ANSIBLE_GALAXY_SERVER_TROVE_TOKEN=" + formats.SetupTokenRefWin + "\n",
	} {
		tpl := templateByID(t, id)
		if got := renderSetup(t, tpl, setupData(true)); got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
		open := renderSetup(t, tpl, setupData(false))
		for _, ref := range formats.SetupTokenRefs() {
			if strings.Contains(open, ref) || strings.Contains(open, "ANSIBLE_GALAXY") {
				t.Errorf("%s for an anonymous repository still sets a credential: %q", id, open)
			}
		}
	}
}

// TestR_ANS_025_TheNetrcLineNamesTheHostAlone: the machine of the ~/.netrc line is the host name of the repository address without
// scheme, port or path, because the client looks it up by host name (R-ANS-015, Q-ANS-48).
func TestR_ANS_025_TheNetrcLineNamesTheHostAlone(t *testing.T) {
	tpl := templateByID(t, "ansible-galaxy.netrc.configure")
	got := renderSetup(t, tpl, setupData(true))
	want := "machine trove.example.com login trove password " + formats.SetupTokenRef + "\n"
	if !strings.Contains(got, want) {
		t.Errorf("netrc = %q, want a line %q", got, want)
	}
	if tpl.File != "~/.netrc" {
		t.Errorf("file = %q", tpl.File)
	}
	if open := renderSetup(t, tpl, setupData(false)); strings.Contains(open, "machine") {
		t.Errorf("an anonymous repository gets a netrc entry: %q", open)
	}
}

// TestR_ANS_025_TheInstallSnippetsTakeThePackageAndTheVersion: the version follows the collection as `:<version>` and a
// requirements.yml carries it as a key of the entry.
func TestR_ANS_025_TheInstallSnippetsTakeThePackageAndTheVersion(t *testing.T) {
	var install, requirements formats.SetupTemplate
	for _, tpl := range New().SetupTemplates() {
		switch tpl.ID {
		case "ansible-galaxy.collection.install":
			install = tpl
		case "ansible-galaxy.requirements.install":
			requirements = tpl
		}
	}
	if !strings.Contains(install.Template, formats.SetupPackageRef) || install.VersionSpec != ":"+formats.SetupVersionRef {
		t.Errorf("install: %q %q", install.Template, install.VersionSpec)
	}
	if !strings.Contains(requirements.Template, "- name: "+formats.SetupPackageRef+"\n") || !strings.Contains(requirements.VersionSpec, "version: ") {
		t.Errorf("requirements: %q %q", requirements.Template, requirements.VersionSpec)
	}
}

// TestR_ANS_025_TheGuideTextRendersForEveryClass: every section runs over the data of every class the format will have, with and
// without a sign-in, and none renders a missing value. The kit runs the sections of the registered classes only.
func TestR_ANS_025_TheGuideTextRendersForEveryClass(t *testing.T) {
	for _, class := range []formats.Class{formats.Local, formats.Remote, formats.Virtual} {
		for _, anonymous := range []bool{false, true} {
			d := formats.GuideData{SetupData: setupData(!anonymous), Class: class, Client: ClientGalaxy, Anonymous: anonymous, TokenEnvVar: "TROVE_TOKEN"}
			if class != formats.Remote {
				d.PublishTarget = "galaxy"
			}
			for _, s := range guideSections() {
				if !s.AppliesTo(class, ClientGalaxy) {
					continue
				}
				out, err := formats.ExecuteGuide(s, d)
				if err != nil || strings.Contains(out, "<no value>") || strings.TrimSpace(out) == "" {
					t.Errorf("%s for %s: %q, %v", s.ID, class, out, err)
				}
			}
		}
	}
}
