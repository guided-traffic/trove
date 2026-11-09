package deb

import "github.com/guided-traffic/trove/internal/formats"

// The client ID (R-DEB-029): apt and apt-get read one source format, so they are one client.
const ClientAPT = "apt"

// clients are the tested clients of the format: the APT of the four images the e2e sets use (R-DEB-035): Debian 13 (3.0), Debian 12
// (2.6), Ubuntu 24.04 (2.7) and Ubuntu 22.04 (2.4).
func clients() []formats.Client {
	return []formats.Client{{ID: ClientAPT, DisplayName: "APT (apt, apt-get)", Versions: []string{"2.4", "2.6", "2.7", "3.0"}}}
}

// The names the snippets derive from the repository: the keyring file, the source file and the credential file, per project and
// repository so that two repositories of one host never share one (R-DEB-029, R-DEB-018). The project and the key are the SetupData's.
const (
	tplName    = `trove-{{or .Project "<project>"}}-{{or .Key "<repository>"}}`
	tplKeyFile = `/etc/apt/keyrings/` + tplName + `.asc`
	tplSources = `/etc/apt/sources.list.d/` + tplName + `.sources`
	tplAuth    = `/etc/apt/auth.conf.d/` + tplName + `.conf`
	tplSuite   = `{{if .Variant}}{{.Variant}}{{else}}stable{{end}}`
	// tplSuiteArg is the suite as a template argument of printf.
	tplSuiteArg = `(or .Variant "stable")`
	tplUser     = `{{if .Username}}{{shell .Username}}{{else}}trove{{end}}`
	tplKeyURL   = `{{shell (printf "%ssigning-key.asc" .RegistryURL)}}`
)

// SetupTemplates are the client setup snippets of R-DEB-029 and R-UI-018: how to trust the repository's key and add the source (the
// deb822 form, and the one-line form), where the credentials go (a file only root can read, never the URL of the source), how to
// install, and how to upload. A template sees [formats.SetupData] and never a token: the credential is a reference to the environment
// variable that the shell expands itself, in a line the API drops when the reader asked for snippets without a credential.
//
// The variant of a configure snippet is the suite. No snippet uses trusted=yes, allow-insecure, apt-key or a credential in a URL: those
// would land in world-readable files and in logs.
func (f *Format) SetupTemplates() []formats.SetupTemplate {
	configure := `sudo install -d -m 0755 /etc/apt/keyrings
{{if .RequiresAuth}}curl -fsSL -u ` + tplUser + `:"` + formats.SetupTokenRef + `" ` + tplKeyURL + ` | sudo tee ` + tplKeyFile + ` >/dev/null
{{else}}curl -fsSL ` + tplKeyURL + ` | sudo tee ` + tplKeyFile + ` >/dev/null
{{end}}sudo tee ` + tplSources + ` >/dev/null <<'SOURCES'
Types: deb
URIs: {{.RegistryURL}}
Suites: ` + tplSuite + `
Components: main
Signed-By: ` + tplKeyFile + `
SOURCES
{{if .RequiresAuth}}sudo install -m 0600 /dev/null ` + tplAuth + `
printf 'machine %s\nlogin %s\npassword %s\n' {{shell (slice (schemeless .RegistryURL) 2)}} ` + tplUser + ` "` + formats.SetupTokenRef + `" | sudo tee ` + tplAuth + ` >/dev/null
{{end}}`
	list := `# ` + `/etc/apt/sources.list.d/` + tplName + `.list, once the key is installed as above
deb [signed-by=` + tplKeyFile + `] {{.RegistryURL}} ` + tplSuite + ` main
`
	install := "sudo apt-get update && sudo apt-get install " + formats.SetupPackageRef + "\n"
	publish := `curl -fsS -u ` + tplUser + `:"` + formats.SetupTokenRef + `" --upload-file ./hello_1.0_amd64.deb \
  {{shell (printf "%supload/hello_1.0_amd64.deb?distribution=%s&component=main" .RegistryURL ` + tplSuiteArg + `)}}
`
	out := []formats.SetupTemplate{
		{ID: "apt.shell.configure", Client: ClientAPT, Section: "configure", Lang: "shell", Template: configure},
		{ID: "apt.list.configure", Client: ClientAPT, Section: "configure", Lang: "ini", Template: list},
		{ID: "apt.install", Client: ClientAPT, Section: "install", Lang: "shell", Template: install, VersionSpec: "=" + formats.SetupVersionRef},
		{ID: "apt.curl.publish", Client: ClientAPT, Section: "publish", Lang: "shell", Template: publish},
	}
	return append(out, f.remoteSetupTemplates()...)
}

// optionNames are the client options a template spells although their names contain "password" (kit check 8): the name of the field
// of auth.conf, never a value.
func optionNames() []string { return []string{"password"} }
