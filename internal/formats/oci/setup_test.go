package oci

import (
	"bytes"
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
	return formats.SetupData{
		RegistryURL: "https://trove.example.com/v2/web/docker-local/", ImagePrefix: "trove.example.com/web/docker-local",
		Repository: "docker-local", Endpoint: "default", RequiresAuth: auth,
	}
}

// TestTheSnippetsNameTheImagePrefixAndNoCredential: the setup snippets of a repository start every image reference with
// <host>/<projectKey>/<repoKey>/, carry no "/v2" (the protocol's prefix is no part of a reference), and spell the token only as the
// reference of the environment variable, in a line the API drops for a reader who asked for none (R-OCI-030).
//
// Mechanism of R-OCI-030, not a proof of its criteria: its criterion needs the e2e run of every rendered snippet.
func TestTheSnippetsNameTheImagePrefixAndNoCredential(t *testing.T) {
	const prefix = "trove.example.com/web/docker-local"
	tpls := New().SetupTemplates()
	if len(tpls) == 0 {
		t.Fatal("no templates")
	}
	bySection := map[string]map[string]int{}
	for _, tpl := range tpls {
		if bySection[tpl.Client] == nil {
			bySection[tpl.Client] = map[string]int{}
		}
		bySection[tpl.Client][tpl.Section]++
		for _, auth := range []bool{true, false} {
			out := renderSetup(t, tpl, setupData(auth))
			if !strings.Contains(out, prefix) && (tpl.ID != "docker.kubernetes.configure" || auth) {
				t.Errorf("%s: no image prefix in\n%s", tpl.ID, out)
			}
			if strings.Contains(out, "/v2") || strings.Contains(out, "https://") || strings.Contains(out, "<no value>") || strings.Contains(out, "{{") {
				t.Errorf("%s: %q", tpl.ID, out)
			}
			spells := strings.Contains(out, formats.SetupTokenRef)
			if spells && (!auth || tpl.Section == "install") {
				t.Errorf("%s (auth %v) spells the credential:\n%s", tpl.ID, auth, out)
			}
			if tpl.Section == "configure" && spells != auth {
				t.Errorf("%s (auth %v): the credential is %v:\n%s", tpl.ID, auth, spells, out)
			}
			for _, other := range []string{formats.SetupTokenRefWin, formats.SetupTokenRefPS} {
				if strings.Contains(out, other) {
					t.Errorf("%s: the reference %s is for another syntax", tpl.ID, other)
				}
			}
		}
	}
	for _, c := range clients() {
		if bySection[c.ID]["configure"] == 0 || bySection[c.ID]["install"] == 0 || bySection[c.ID]["publish"] == 0 {
			t.Errorf("client %s: %v", c.ID, bySection[c.ID])
		}
	}
}

// TestTheLoginReadsTheTokenFromStandardInput: the token never sits on a command line (R-OCI-030).
//
// Mechanism of R-OCI-030, not a proof of its criteria: its criterion needs the e2e run of every rendered snippet.
func TestTheLoginReadsTheTokenFromStandardInput(t *testing.T) {
	want := map[string]string{
		"docker.shell.configure": `printf '%s' "${TROVE_TOKEN}" | docker login trove.example.com/web/docker-local --username trove --password-stdin`,
		"podman.shell.configure": `printf '%s' "${TROVE_TOKEN}" | podman login trove.example.com/web/docker-local --username trove --password-stdin`,
	}
	for _, tpl := range New().SetupTemplates() {
		if w, ok := want[tpl.ID]; ok {
			if got := strings.TrimSpace(renderSetup(t, tpl, setupData(true))); got != w {
				t.Errorf("%s:\n got %s\nwant %s", tpl.ID, got, w)
			}
			// A reader without a credential gets the login that asks for it.
			if got := strings.TrimSpace(renderSetup(t, tpl, setupData(false))); strings.Contains(got, "TROVE_TOKEN") || !strings.Contains(got, " login trove.example.com/web/docker-local --username trove") {
				t.Errorf("%s without a credential: %s", tpl.ID, got)
			}
		}
	}
}

// Mechanism of R-OCI-030, not a proof of its criteria: its criterion needs the e2e run of every rendered snippet.
func TestTheKubernetesSecretIsKeyedByTheImagePrefixAndKeepsTheTokenOffTheCommandLine(t *testing.T) {
	for _, tpl := range New().SetupTemplates() {
		if tpl.ID != "docker.kubernetes.configure" {
			continue
		}
		out := renderSetup(t, tpl, setupData(true))
		for _, want := range []string{
			`printf '{"auths":{"%s":{"username":"trove","password":"%s"}}}' trove.example.com/web/docker-local "${TROVE_TOKEN}" |`,
			"kubectl create secret generic trove-registry --type=kubernetes.io/dockerconfigjson --from-file=.dockerconfigjson=/dev/stdin",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in\n%s", want, out)
			}
		}
		// The line that holds the token is a printf, a shell builtin: no kubectl argument carries it.
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "kubectl") && strings.Contains(line, "TROVE_TOKEN") || strings.Contains(out, "--docker-password") {
				t.Errorf("the token reaches kubectl's arguments: %s", line)
			}
		}
		if no := renderSetup(t, tpl, setupData(false)); strings.Contains(no, "kubectl") || strings.Contains(no, "TROVE_TOKEN") || !strings.HasPrefix(no, "#") {
			t.Errorf("without a credential: %q", no)
		}
		return
	}
	t.Fatal("no Kubernetes template")
}

// Mechanism of R-OCI-030, not a proof of its criteria: its criterion needs the e2e run of every rendered snippet.
func TestTheInstallSnippetPlacesTheImageAndItsTag(t *testing.T) {
	for _, tpl := range New().SetupTemplates() {
		if tpl.Section != "install" {
			continue
		}
		if tpl.VersionSpec != ":"+formats.SetupVersionRef || !strings.Contains(tpl.Template, formats.SetupPackageRef) {
			t.Errorf("%s: version spec %q, template %q", tpl.ID, tpl.VersionSpec, tpl.Template)
		}
		out := renderSetup(t, tpl, setupData(true))
		if !strings.HasSuffix(strings.TrimSpace(out), "trove.example.com/web/docker-local/"+formats.SetupPackageRef) {
			t.Errorf("%s: %s", tpl.ID, out)
		}
	}
}

// Mechanism of R-OCI-030, not a proof of its criteria: its criterion needs the e2e run of every rendered snippet.
func TestBuildxPushesBothPlatformsWithoutEmulationAssumption(t *testing.T) {
	for _, tpl := range New().SetupTemplates() {
		if tpl.ID != "docker.buildx.publish" {
			continue
		}
		out := renderSetup(t, tpl, setupData(true))
		if !strings.Contains(out, "docker buildx build --platform linux/amd64,linux/arm64") || !strings.Contains(out, "--push") {
			t.Errorf("%s", out)
		}
		return
	}
	t.Fatal("no Buildx template")
}
