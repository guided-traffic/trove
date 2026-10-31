{{if .Anonymous}}You are reading this guide without signing in. {{if .Repository}}`{{.Repository}}` is open to anonymous readers, so your client needs no credential to install from it.{{else}}A repository that is open to anonymous readers needs no credential to install from.{{end}} Publishing, changing dist-tags and unpublishing always need a token: sign in to trove to see how to create one and how your client sends it.
{{else}}Your client signs in with a trove token, never with your password.

- A **personal access token** is yours: create it under Profile, Personal tokens. Limit it to the repositories and actions it needs and give it an expiry: installing needs read, publishing and changing dist-tags also deploy, unpublishing delete.
- A **service-account token** belongs to a service account, for CI jobs and other automation. An administrator creates the account and its tokens.

The client sends the token as a bearer token (`_authToken` in `.npmrc`, `npmAuthToken` in `.yarnrc.yml`) or as the password of Basic authentication, with any user name.

### Keep the token in {{.TokenEnvVar}}

The snippets read the token from the environment variable `{{.TokenEnvVar}}`, which the client expands itself when it runs. The configuration file then holds no secret and can be committed. Never commit a file that holds the token itself: the client setup page writes a token into its snippets when you create or paste one there, and such a file belongs in your home directory or another place outside version control.

### In CI

Store a service-account token with only the permissions the job needs as a masked, protected CI/CD variable named `{{.TokenEnvVar}}`, and commit the configuration of the snippet; the job needs nothing else. Coming in a later release: GitLab CI jobs exchange their job ID token for a short-lived trove token, so that no long-lived token is stored in the project at all.

### Checking the sign-in

{{if eq .Client "npm"}}`npm whoami` prints your trove name (`sa:<name>` for a service account), and `npm ping` succeeds with a valid token.{{else if eq .Client "pnpm"}}`pnpm whoami` prints your trove name (`sa:<name>` for a service account), and `pnpm ping` succeeds with a valid token.{{else if eq .Client "yarn"}}`yarn npm whoami` prints your trove name (`sa:<name>` for a service account).{{else}}Yarn Classic has no command that prints who you are; an install from a repository that is not open to anonymous readers fails with `401` while the token is missing or wrong.{{end}} A missing, expired or revoked token answers `401`, and trove never asks the client for a one-time password.

{{if eq .Client "npm"}}`npm login` does not work with trove yet{{else if eq .Client "pnpm"}}`pnpm login` does not work with trove yet{{else if eq .Client "yarn"}}`yarn npm login` does not work with trove yet{{else}}`yarn login` only stores a name on your machine and signs nothing in{{end}}: put the token into the configuration as the snippet shows.
{{end}}