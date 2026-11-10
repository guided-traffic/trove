package ansible

import "github.com/guided-traffic/trove/internal/formats"

// ClientGalaxy is the client ID (R-ANS-025): `ansible-galaxy` of ansible-core, the only client of the format.
const ClientGalaxy = "ansible-galaxy"

// clients are the tested clients of the format: ansible-core 2.17 and 2.21, the two client images of the e2e set (R-ANS-032).
func clients() []formats.Client {
	return []formats.Client{{ID: ClientGalaxy, DisplayName: "Ansible Galaxy (ansible-galaxy)", Versions: []string{"2.17", "2.21"}}}
}

// tokenEnv is the variable `ansible-galaxy` reads the token of the server `trove` from: ANSIBLE_GALAXY_SERVER_<SERVER>_<KEY> with the
// server name and the key in upper case (Q-ANS-22). The server is named `trove` by the ansible.cfg snippet, so the name is fixed.
const tokenEnv = "ANSIBLE_GALAXY_SERVER_TROVE_TOKEN" //nolint:gosec // the name of an environment variable, not a credential

// optionNames are the client names the templates and the guide spell although they contain "token" or "password" (kit check 8): the
// variable above, the option --token and the netrc keyword. None is a value.
func optionNames() []string { return []string{tokenEnv, "--token", "password"} }

// anonymousNote is what the snippets that carry a credential say when the repository needs none.
const anonymousNote = "This repository is open to anonymous readers: no credential is needed to install from it."

// SetupTemplates are the client setup snippets of R-ANS-025 and R-UI-018 (Q-ANS-22, Q-ANS-48): the server in an ansible.cfg, the token
// in the environment (the file has no variable expansion, so the token is never written into it), a ~/.netrc line for the role
// archives (R-ANS-015), the install commands, a requirements.yml and the publish command. A template sees [formats.SetupData] and never
// a token: the credential is a reference that the shell expands itself, in a line the API drops when the reader asked for snippets
// without a credential. The configure snippets that carry a credential say in a comment that none is needed when the repository is
// open to anonymous readers.
func (f *Format) SetupTemplates() []formats.SetupTemplate {
	cfg := `[galaxy]
server_list = trove

[galaxy_server.trove]
url={{.RegistryURL}}
`
	envShell := `{{if .RequiresAuth}}export ` + tokenEnv + `="` + formats.SetupTokenRef + `"
{{else}}# ` + anonymousNote + `
{{end}}`
	envPS := `{{if .RequiresAuth}}$env:` + tokenEnv + ` = "` + formats.SetupTokenRefPS + `"
{{else}}# ` + anonymousNote + `
{{end}}`
	envCmd := `{{if .RequiresAuth}}set ` + tokenEnv + `=` + formats.SetupTokenRefWin + `
{{else}}REM ` + anonymousNote + `
{{end}}`
	// netrc is a command and not the content of a file: ~/.netrc expands no variable, so a line that holds the reference would be read as the
	// password itself (the client sends it as Basic, in place of its Token header, which the e2e set showed with ansible-core 2.17 and 2.21).
	netrc := `{{if .RequiresAuth}}touch ~/.netrc && chmod 600 ~/.netrc
printf 'machine %s login trove password %s\n' {{hostname .RegistryURL}} "` + formats.SetupTokenRef + `" >>~/.netrc
{{else}}# This repository is open to anonymous readers: no ~/.netrc entry is needed.
{{end}}`
	requirements := `---
collections:
  - name: ` + formats.SetupPackageRef + `
    source: trove
# Roles are served by remote and virtual repositories of a Galaxy only. Install them with
#   ansible-galaxy role install -r requirements.yml
# roles:
#   - name: <owner>.<role>
`
	publish := `ansible-galaxy collection publish ./acme-tools-1.0.0.tar.gz --server {{shell .RegistryURL}} --token=` + formats.SetupTokenRef + "\n"
	return []formats.SetupTemplate{
		{ID: "ansible-galaxy.ansible-cfg.configure", Client: ClientGalaxy, Section: "configure", Lang: "ini", File: "ansible.cfg", Template: cfg},
		{ID: "ansible-galaxy.token-env.configure", Client: ClientGalaxy, Section: "configure", Lang: "shell", Template: envShell},
		{ID: "ansible-galaxy.token-env-powershell.configure", Client: ClientGalaxy, Section: "configure", Lang: "powershell", Template: envPS},
		{ID: "ansible-galaxy.token-env-cmd.configure", Client: ClientGalaxy, Section: "configure", Lang: "cmd", Template: envCmd},
		{ID: "ansible-galaxy.netrc.configure", Client: ClientGalaxy, Section: "configure", Lang: "shell", Template: netrc},

		{
			ID: "ansible-galaxy.collection.install", Client: ClientGalaxy, Section: "install", Lang: "shell",
			Template: "ansible-galaxy collection install " + formats.SetupPackageRef + "\n", VersionSpec: ":" + formats.SetupVersionRef,
		},
		{
			ID: "ansible-galaxy.requirements.install", Client: ClientGalaxy, Section: "install", Lang: "yaml", File: "requirements.yml",
			Template: requirements, VersionSpec: "\n    version: \"" + formats.SetupVersionRef + "\"",
		},

		{ID: "ansible-galaxy.collection.publish", Client: ClientGalaxy, Section: "publish", Lang: "shell", Template: publish},
	}
}
