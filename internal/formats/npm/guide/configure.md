{{if eq .Client "npm"}}Save the snippet as `.npmrc` in your project, or in `~/.npmrc` to use {{if .Repository}}`{{.Repository}}`{{else}}the repository{{end}} for every project. The `registry=` line sends every package request to this address; the address keeps its trailing slash.{{if not .Anonymous}} The second line binds the credential to the same address without its scheme, so npm sends it to this repository and to no other URL. npm reads `${TROVE_TOKEN}` from the environment when it runs.{{end}} `npm config get registry` prints the address npm uses.
{{else if eq .Client "pnpm"}}Save the snippet in your user-level `~/.npmrc`.{{if not .Anonymous}} pnpm 11 and 12 ignore a credential that names an environment variable in a project `.npmrc`, so a project file would leave them without one. pnpm reads `${TROVE_TOKEN}` from the environment when it runs, and sends the credential to this repository's address only.{{end}} The address keeps its trailing slash. `pnpm config get registry` prints the address pnpm uses.
{{else if eq .Client "yarn"}}Save the snippet as `.yarnrc.yml` in your project. Yarn Berry (Yarn 2 and later) reads `.yarnrc.yml` and not `.npmrc`.{{if not .Anonymous}} Yarn expands `${TROVE_TOKEN}` from the environment itself.{{if .RequiresAuth}} `npmAlwaysAuth: true` makes Yarn send the token with every request: the repository is not open to anonymous readers, and without the switch Yarn sends it for scoped packages only.{{end}}{{end}} `yarn config get npmRegistryServer` prints the address Yarn uses. If Yarn holds back a version you have just published as too new, its `npmMinimalAgeGate` setting decides how old a version must be.
{{else}}Save the snippet as `.npmrc` in your project; Yarn Classic (Yarn 1) reads it like npm does.{{if not .Anonymous}}{{if .RequiresAuth}} `always-auth=true` makes Yarn Classic send the token with every request: the repository is not open to anonymous readers, and without the switch Yarn Classic sends it for scoped packages only.{{end}}{{end}} The address keeps its trailing slash. `yarn config list` shows the configuration Yarn Classic reads.
{{end}}{{if .Anonymous}}
You are not signed in, so the snippet carries no credential. It works for repositories that are open to anonymous readers.
{{end}}
### Only one scope from this repository

To take only the packages of one scope from {{if .Repository}}`{{.Repository}}`{{else}}the repository{{end}}, and every other package from your client's default registry, {{if eq .Client "yarn"}}bind the scope in `.yarnrc.yml` instead of setting `npmRegistryServer`:

```yaml
npmScopes:
  your-scope:
    npmRegistryServer: "{{.RegistryURL}}"
    npmAuthToken: "${TROVE_TOKEN}"
```
{{else}}replace the `registry=` line by a line for the scope{{if not .Anonymous}} and keep the credential line{{end}}:

```ini
@your-scope:registry={{.RegistryURL}}
```
{{end}}
Write your scope without its `@` where the example has `your-scope`.
