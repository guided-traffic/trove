package v1

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm"
)

// R-UI-047 asks that a deliberately broken snippet template fails the client-setup test. The client-setup journey
// (web/e2e/tests/08-packages.spec.ts, Q-UI-82) holds the page against the configure snippets of the npm matrix written out as text,
// character for character; this test runs the same comparison on the renderer the API answers with, once for the shipped templates
// and once for every template broken in each of the ways a template can break, so the proof that a broken template fails does not
// wait for a browser run on a cluster. The expected texts below are the journey's `matrixSnippet`, spelled out a second time on
// purpose: a change of a template edits them, and so the journey, together with the template.

const (
	journeyRegistry = "https://localhost:30443/api/npm/e2e/npm-journey/"
	journeyScheme   = "//localhost:30443/api/npm/e2e/npm-journey/"
)

// journeySnippets are the four configure snippets of the journey's repository, which is not anonymously readable, with the credential
// mode "Reference an environment variable".
var journeySnippets = map[string]struct{ file, content string }{
	"npm":          {".npmrc", "registry=" + journeyRegistry + "\n" + journeyScheme + ":_authToken=${TROVE_TOKEN}\n"},
	"pnpm":         {"~/.npmrc", "registry=" + journeyRegistry + "\n" + journeyScheme + ":_authToken=${TROVE_TOKEN}\n"},
	"yarn-classic": {".npmrc", "registry=" + journeyRegistry + "\n" + journeyScheme + ":_authToken=${TROVE_TOKEN}\nalways-auth=true\n"},
	"yarn":         {".yarnrc.yml", "npmRegistryServer: \"" + journeyRegistry + "\"\nnpmAlwaysAuth: true\nnpmAuthToken: \"${TROVE_TOKEN}\"\n"},
}

// journeyDiffers renders the configure snippet of every client from the templates and reports each one that differs from the
// journey's text; it is the comparison of the journey, run on the API's renderer.
func journeyDiffers(t *testing.T, templates []formats.SetupTemplate) []string {
	t.Helper()
	var differs []string
	for client, want := range journeySnippets {
		got, err := renderSnippets(setupInput{
			Templates: templates, Client: client, Credentials: credEnv, TokenEnvVar: "TROVE_TOKEN",
			Data: formats.SetupData{
				RegistryURL: journeyRegistry, Repository: "e2e/npm-journey", Project: "e2e", Key: "npm-journey", Endpoint: "default", RequiresAuth: true,
			},
		})
		if err != nil {
			differs = append(differs, client+": "+err.Error())
			continue
		}
		var found *snippet
		for i := range got {
			if got[i].Tab == "configure" {
				found = &got[i]
			}
		}
		if found == nil || found.FileName != want.file || found.Content != want.content {
			differs = append(differs, client)
		}
	}
	return differs
}

func TestR_UI_047_TheShippedSnippetTemplatesRenderTheTextsOfTheClientSetupJourney(t *testing.T) {
	if d := journeyDiffers(t, npm.New().SetupTemplates()); len(d) != 0 {
		t.Fatalf("the shipped templates differ from the journey's texts for %v", d)
	}
}

func TestR_UI_047_ADeliberatelyBrokenSnippetTemplateFailsTheClientSetupComparison(t *testing.T) {
	breaks := map[string]func(string) string{
		"the registry line is dropped":      func(s string) string { return strings.Replace(s, "registry={{end}}{{.RegistryURL}}", "{{end}}", 1) },
		"the credential line is dropped":    func(s string) string { return strings.Replace(s, ":_authToken=${TROVE_TOKEN}", "", 1) },
		"the credential is spelled wrongly": func(s string) string { return strings.Replace(s, "_authToken", "_auth", 1) },
		"a trailing newline is lost":        func(s string) string { return strings.TrimSuffix(s, "\n") },
		"the always-auth switch is lost": func(s string) string {
			return strings.NewReplacer("{{if .RequiresAuth}}always-auth=true\n{{end}}", "", "{{if .RequiresAuth}}npmAlwaysAuth: true\n{{end}}", "").Replace(s)
		},
		"the URL is not written": func(s string) string { return strings.ReplaceAll(s, "{{.RegistryURL}}", "") },
	}
	for name, mutate := range breaks {
		t.Run("R-UI-047 "+name, func(t *testing.T) {
			templates := npm.New().SetupTemplates()
			changed := false
			for i := range templates {
				if templates[i].Section != "configure" {
					continue
				}
				if m := mutate(templates[i].Template); m != templates[i].Template {
					templates[i].Template, changed = m, true
				}
			}
			if !changed {
				t.Fatal("the break changes no template, so the test proves nothing")
			}
			if len(journeyDiffers(t, templates)) == 0 {
				t.Errorf("a template with %s still renders the journey's texts", name)
			}
		})
	}
}
