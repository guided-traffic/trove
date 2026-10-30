package v1

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

func testData() formats.SetupData {
	return formats.SetupData{
		RegistryURL: "https://trove.example.com/api/npm/npm-all/", Repository: "npm-all", Endpoint: "default",
		RegistryKey: "//trove.example.com/api/npm/npm-all/", RequiresAuth: true,
	}
}

func fakeTemplates() []formats.SetupTemplate {
	return []formats.SetupTemplate{
		{
			ID: "x.conf.configure", Client: "x", Section: "configure", Lang: "ini", File: "x.conf",
			Template: "{{if .Scope}}@{{.Scope}}:{{end}}registry={{.RegistryURL}}\n{{.RegistryKey}}:token=" + formats.SetupTokenRef + "\n{{if .RequiresAuth}}always=true\n{{end}}",
		},
		{ID: "x.install", Client: "x", Section: "install", Lang: "shell", Template: "x add " + formats.SetupPackageRef + "\n"},
		{ID: "x.xml.install", Client: "x", Section: "install", Lang: "xml", Template: `<Add Include="` + formats.SetupPackageRef + `" Repo="{{xml .Repository}}" />` + "\n"},
		{ID: "x.publish", Client: "x", Section: "publish", Lang: "shell", Template: "x publish --to {{shell .Repository}}\n"},
		{ID: "y.install", Client: "y", Section: "install", Lang: "shell", Template: "y add\n"},
	}
}

func byID(ss []snippet) map[string]snippet {
	out := map[string]snippet{}
	for _, s := range ss {
		out[s.ID] = s
	}
	return out
}

func TestR_API_030_CredentialModes(t *testing.T) {
	in := setupInput{Templates: fakeTemplates(), Client: "x", TokenEnvVar: "MY_TOKEN", Data: testData()}
	for _, tc := range []struct {
		mode string
		want string
	}{
		{credPlaceholder, "registry=https://trove.example.com/api/npm/npm-all/\n//trove.example.com/api/npm/npm-all/:token={{token}}\nalways=true\n"},
		{credEnv, "registry=https://trove.example.com/api/npm/npm-all/\n//trove.example.com/api/npm/npm-all/:token=${MY_TOKEN}\nalways=true\n"},
		// Without a credential there is nothing for an always-auth switch to send.
		{credNone, "registry=https://trove.example.com/api/npm/npm-all/\n"},
	} {
		in.Credentials = tc.mode
		ss, err := renderSnippets(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := byID(ss)["x.configure.conf"].Content; got != tc.want {
			t.Errorf("%s:\n%q\nwant\n%q", tc.mode, got, tc.want)
		}
		for _, s := range ss {
			if strings.Contains(s.Content, formats.SetupTokenRef) {
				t.Errorf("%s: %s still holds the template's reference: %q", tc.mode, s.ID, s.Content)
			}
		}
	}
}

func TestR_API_030_SnippetIDsFollowTheContract(t *testing.T) {
	in := setupInput{Templates: fakeTemplates(), Client: "x", Credentials: credEnv, Data: testData(), Scope: "acme"}
	ss, err := renderSnippets(in)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range ss {
		ids = append(ids, s.ID+"/"+s.Tab+"/"+s.Language)
	}
	want := "x.configure.conf/configure/ini x.configure.conf-scoped/configure/ini x.install/install/shell x.install.xml/install/xml"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("%s\nwant\n%s", got, want)
	}
	if got := byID(ss)["x.configure.conf-scoped"].Content; !strings.HasPrefix(got, "@acme:registry=") {
		t.Errorf("scoped variant: %q", got)
	}
	if got := byID(ss)["x.configure.conf"].FileName; got != "x.conf" {
		t.Errorf("file name %q", got)
	}
	if got := byID(ss)["x.install"].FileName; got != "" {
		t.Errorf("a command has a file name: %q", got)
	}
}

func TestR_API_030_PublishSnippetsNeedTheDeployRight(t *testing.T) {
	in := setupInput{Templates: fakeTemplates(), Client: "x", Credentials: credNone, Data: testData()}
	ss, _ := renderSnippets(in)
	if _, ok := byID(ss)["x.publish"]; ok {
		t.Fatal("a publish snippet for a caller who may not deploy")
	}
	in.MayPublish = true
	ss, _ = renderSnippets(in)
	if got := byID(ss)["x.publish"]; got.Tab != "publish" || got.Content != "x publish --to npm-all\n" {
		t.Fatalf("%+v", got)
	}
}

func TestR_API_030_ClientsAreTheOnesTheTemplatesServe(t *testing.T) {
	if got := strings.Join(setupClients(fakeTemplates()), ","); got != "x,y" {
		t.Fatal(got)
	}
}

func TestR_API_030_ShellValuesAreQuoted(t *testing.T) {
	for in, want := range map[string]string{
		"left-pad":              "left-pad",
		"@team-a/ui@1.4.0":      "@team-a/ui@1.4.0",
		"a_b.c+d:e=f":           "a_b.c+d:e=f",
		"a(b)@1.0.0":            "'a(b)@1.0.0'",
		"it's":                  `'it'\''s'`,
		"a b":                   "'a b'",
		"$(id)":                 "'$(id)'",
		"`id`":                  "'`id`'",
		"a;b":                   "'a;b'",
		"a!b*c":                 "'a!b*c'",
		"~/x":                   "'~/x'",
		"\"q\"":                 `'"q"'`,
		"back\\slash":           `'back\slash'`,
		"":                      "''",
		"a\nb":                  "'a\nb'",
		"x@1.0.0-beta.1+build5": "x@1.0.0-beta.1+build5",
		// Q-NPM-50: a value that starts with "-" never stands bare in a command.
		"-g":                        "'-g'",
		"--registry=https://x.test": "'--registry=https://x.test'",
		"a-b":                       "a-b",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestR_API_030_PackageSubstitutionIsQuotedForTheLanguage(t *testing.T) {
	in := setupInput{Templates: fakeTemplates(), Client: "x", Credentials: credNone, Data: testData(), Package: `a<b>&"c'`, Version: "1.0.0"}
	ss, err := renderSnippets(in)
	if err != nil {
		t.Fatal(err)
	}
	m := byID(ss)
	if got := m["x.install"].Content; got != `x add 'a<b>&"c'\''@1.0.0'`+"\n" {
		t.Errorf("shell: %q", got)
	}
	if got := m["x.install.xml"].Content; got != `<Add Include="a&lt;b&gt;&amp;&#34;c&#39;@1.0.0" Repo="npm-all" />`+"\n" {
		t.Errorf("xml: %q", got)
	}
	in.Package, in.Version = "", "1.0.0"
	ss, _ = renderSnippets(in)
	if got := byID(ss)["x.install"].Content; got != "x add <package>\n" {
		t.Errorf("without a package: %q", got)
	}
}

func TestR_API_030_XMLValuesOfTheRepositoryAreEscaped(t *testing.T) {
	d := testData()
	d.Repository = `r"><x/>`
	in := setupInput{Templates: fakeTemplates(), Client: "x", Credentials: credNone, Data: d, Package: "p"}
	ss, _ := renderSnippets(in)
	got := byID(ss)["x.install.xml"].Content
	if strings.Contains(got, `"><x/>`) || !strings.Contains(got, "&lt;x/&gt;") {
		t.Fatalf("%q", got)
	}
}

func TestR_API_030_ATemplateCannotReachForACredential(t *testing.T) {
	in := setupInput{Templates: []formats.SetupTemplate{
		{ID: "z.install", Client: "z", Section: "install", Lang: "shell", Template: "{{.Token}}"},
	}, Client: "z", Credentials: credEnv, Data: testData()}
	if _, err := renderSnippets(in); err == nil {
		t.Fatal("a template that names a field SetupData does not have rendered")
	}
}

func TestR_API_030_TokenShape(t *testing.T) {
	for _, s := range []string{"trv_pat_abc", "x trv_sat_y", "trv_cit_"} {
		if !holdsTokenShape(s) {
			t.Errorf("%q is not recognized", s)
		}
	}
	for _, s := range []string{"{{token}}", "${TROVE_TOKEN}", "trv_xyz_abc", "trove_pat_x", ""} {
		if holdsTokenShape(s) {
			t.Errorf("%q is taken for a token", s)
		}
	}
}
