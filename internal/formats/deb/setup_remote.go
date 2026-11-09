package deb

import "github.com/guided-traffic/trove/internal/formats"

// remoteSetupTemplates are the snippets the remote class adds to the client's (R-DEB-029): the source of a remote that has no
// deb.upstreamKeys. Such a remote passes its upstream's signed release through, so the client verifies it with the archive's own
// keyring and not with a key of trove's: the snippet names the keyring that Debian and Ubuntu install on every system, and it
// installs no key. A remote with upstreamKeys serves them as signing-key.asc and the configure snippet of the local class installs
// that block; the guide says which of the two applies (guide_remote.go). No snippet turns the verification off.
func (f *Format) remoteSetupTemplates() []formats.SetupTemplate {
	if f.remote == nil {
		return nil
	}
	upstream := `# The remote has no upstreamKeys: its release is signed by the archive itself, and APT verifies it with the archive's keyring.
# Debian: /usr/share/keyrings/debian-archive-keyring.gpg (package debian-archive-keyring)
# Ubuntu: /usr/share/keyrings/ubuntu-archive-keyring.gpg (package ubuntu-keyring)
sudo tee ` + tplSources + ` >/dev/null <<'SOURCES'
Types: deb
URIs: {{.RegistryURL}}
Suites: ` + tplSuite + `
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
SOURCES
{{if .RequiresAuth}}sudo install -m 0600 /dev/null ` + tplAuth + `
printf 'machine %s\nlogin %s\npassword %s\n' {{shell (slice (schemeless .RegistryURL) 2)}} ` + tplUser + ` "` + formats.SetupTokenRef + `" | sudo tee ` + tplAuth + ` >/dev/null
{{end}}`
	return []formats.SetupTemplate{
		{ID: "apt.shell.configure.upstream-keyring", Client: ClientAPT, Section: "configure", Lang: "shell", Template: upstream},
	}
}
