package v1

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the formats whose templates are checked
)

// TestR_API_030_EverySetupTemplateLanguageIsReportedAsAValueOfTheEnum: the language of a setup
// template reaches the client as the language of the snippet, and the enum of the document is
// frozen by R-ARCH-025; a format that declares a language the mapping does not cover would
// answer a body that breaks its own contract (Q-API-191).
func TestR_API_030_EverySetupTemplateLanguageIsReportedAsAValueOfTheEnum(t *testing.T) {
	for _, f := range formats.List() {
		for _, tpl := range f.SetupTemplates() {
			if !snippetLanguage(tpl.Lang).Valid() {
				t.Errorf("%s: template %s has language %q, which the ClientSnippet enum of api/openapi.yaml lacks", f.Descriptor().ID, tpl.ID, tpl.Lang)
			}
		}
	}
}

func TestR_API_030_PowerShellAndCmdAreReportedAsShell(t *testing.T) {
	for _, l := range []string{"powershell", "cmd", "shell"} {
		if got := snippetLanguage(l); got != "shell" {
			t.Errorf("%s reported as %q", l, got)
		}
	}
	if got := snippetLanguage("xml"); got != "xml" {
		t.Errorf("xml reported as %q", got)
	}
}
