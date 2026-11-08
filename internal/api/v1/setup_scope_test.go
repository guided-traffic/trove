package v1

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/internal/formats/oci"
)

// TestR_API_030_OnlyAConfigureTemplateThatReadsTheVariantComesTwice: a scope adds the scoped copy of
// a configure snippet only where the template has a scoped variant; an OCI repository rendered with
// a scope returns the ids of the unscoped rendering.
func TestR_API_030_OnlyAConfigureTemplateThatReadsTheVariantComesTwice(t *testing.T) {
	ids := func(scope string) []string {
		ss, err := renderSnippets(setupInput{
			Templates: (&oci.Format{}).SetupTemplates(), Client: "docker", Credentials: credEnv, TokenEnvVar: "TROVE_TOKEN", Scope: scope,
			Data: formats.SetupData{RegistryURL: "https://trove.example.net/v2/web/x/", ImagePrefix: "trove.example.net/web/x", RequiresAuth: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return out
	}
	if got, want := ids("acme"), ids(""); !slices.Equal(got, want) {
		t.Errorf("a scope changed the ids of a format without scopes: %v, want %v", got, want)
	}
	if clientHasScopedVariant((&oci.Format{}).SetupTemplates(), "docker") {
		t.Error("docker reports a scoped variant")
	}
	for _, c := range []string{"npm", "pnpm", "yarn", "yarn-classic"} {
		if !clientHasScopedVariant((&npm.Format{}).SetupTemplates(), c) {
			t.Errorf("%s reports no scoped variant", c)
		}
	}
}

func TestR_API_030_TheScopedVariantIsDetectedInEveryTemplateConstruct(t *testing.T) {
	for tpl, want := range map[string]bool{
		"registry={{.RegistryURL}}":                                           false,
		"{{if .Variant}}a{{end}}":                                             true,
		"{{.RegistryURL}}{{with .Variant}}x{{end}}":                           true,
		"{{if .Bastion}}{{else}}{{$.Variant}}{{end}}":                         true,
		"{{range $i, $e := .Repository}}{{.Variant}}{{end}}":                  true,
		"{{shell .Variant}}":                                                  true,
		"{{.Repository | printf \"%s\"}}":                                     false,
		"{{printf \"%s\" .Variant | shell}}":                                  true,
		`{{define "v"}}{{.Variant}}{{end}}{{template "v" .}}`:                 true,
		`{{block "v" .}}{{if .Variant}}@{{.Variant}}:{{end}}registry={{end}}`: true,
		`{{$d := .}}{{if $d.Variant}}x{{end}}`:                                true,
		`{{with $x := .}}{{$x.Variant}}{{end}}`:                               true,
	} {
		got := hasScopedVariant(formats.SetupTemplate{ID: "x.conf.configure", Client: "x", Section: "configure", Template: tpl})
		if got != want {
			t.Errorf("%q: %v, want %v", tpl, got, want)
		}
	}
	if hasScopedVariant(formats.SetupTemplate{ID: "x.install", Client: "x", Section: "install", Template: "{{.Variant}}"}) {
		t.Error("an install template has a scoped variant")
	}
}
