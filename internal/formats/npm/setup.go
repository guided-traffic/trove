package npm

import "github.com/guided-traffic/trove/internal/formats"

// SetupTemplates are the client setup snippets of R-NPM-041: how to point a client at
// the repository (configure) and how to install a package through it (install). The
// dialog that shows them and the token it mints are the API's (R-API-033); a template
// sees [formats.SetupData] and never a token, so the credential is always the
// environment variable ${TROVE_TOKEN} that the client expands itself.
//
// The credential line is keyed: it starts with the registry URL without its scheme,
// the form npm and pnpm bind a credential to, so the client sends it to this
// registry and to no other URL. A snippet never appends a token to the registry= value
// and never writes an unkeyed _auth or _authToken (R-NPM-004, R-NPM-041). Yarn 1 and
// Yarn 2+ need their always-auth switches when the repository is not anonymously
// readable.
func (f *Format) SetupTemplates() []formats.SetupTemplate {
	const npmrc = `{{if .Scope}}@{{.Scope}}:registry={{else}}registry={{end}}{{.RegistryURL}}
{{.RegistryKey}}:_authToken=${TROVE_TOKEN}
`
	const yarnClassic = npmrc + `{{if .RequiresAuth}}always-auth=true
{{end}}`
	const yarn = `{{if .Scope}}npmScopes:
  {{.Scope}}:
    npmRegistryServer: "{{.RegistryURL}}"
    npmAuthToken: "${TROVE_TOKEN}"
{{else}}npmRegistryServer: "{{.RegistryURL}}"
{{if .RequiresAuth}}npmAlwaysAuth: true
{{end}}npmAuthToken: "${TROVE_TOKEN}"
{{end}}`
	return []formats.SetupTemplate{
		{ID: "npm.npmrc.configure", Client: "npm", Section: "configure", Lang: "ini", File: ".npmrc", Template: npmrc},
		{ID: "pnpm.npmrc.configure", Client: "pnpm", Section: "configure", Lang: "ini", File: ".npmrc", Template: npmrc},
		{ID: "yarn.yarnrc.configure", Client: "yarn", Section: "configure", Lang: "yaml", File: ".yarnrc.yml", Template: yarn},
		{ID: "yarn-classic.npmrc.configure", Client: "yarn-classic", Section: "configure", Lang: "ini", File: ".npmrc", Template: yarnClassic},
		{ID: "npm.install", Client: "npm", Section: "install", Lang: "shell", Template: "npm install <package>\n"},
		{ID: "pnpm.install", Client: "pnpm", Section: "install", Lang: "shell", Template: "pnpm add <package>\n"},
		{ID: "yarn.install", Client: "yarn", Section: "install", Lang: "shell", Template: "yarn add <package>\n"},
		{ID: "yarn-classic.install", Client: "yarn-classic", Section: "install", Lang: "shell", Template: "yarn add <package>\n"},
	}
}
