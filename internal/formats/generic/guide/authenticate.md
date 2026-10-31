{{if .Anonymous}}You are reading this guide without signing in. {{if .Repository}}`{{.Repository}}` is open to anonymous readers, so curl needs no credential to download from it.{{else}}A repository that is open to anonymous readers needs no credential to download from.{{end}} Uploading and deleting always need a token: sign in to trove to see how to create one and how curl sends it.
{{else}}curl signs in with a trove token, never with your password.

- A **personal access token** is yours: create it under Profile, Personal tokens. Limit it to the repositories and actions it needs and give it an expiry: downloading needs read, uploading a new file deploy, replacing a file with other bytes overwrite as well, and deleting delete.
- A **service-account token** belongs to a service account, for CI jobs and other automation. An administrator creates the account and its tokens.

`trove_curl` sends the token as a bearer token. trove also takes it as the password of Basic authentication with any user name, unless an administrator switched Basic authentication off, but `curl --user` would put the token on curl's command line, so the snippets do not use it.

### Keep the token in {{.TokenEnvVar}}

Set `{{.TokenEnvVar}}` in the environment curl runs in and keep the token out of scripts and command lines: a shell keeps the lines you type in its history. The client setup page writes a token into its snippets when you create or paste one there; a snippet with the token written in belongs in a file only you can read, not in a shell you type into.

### In CI

Store a service-account token with only the permissions the job needs as a masked, protected CI/CD variable named `{{.TokenEnvVar}}`, and paste the function into the job's script; the job needs nothing else.

### Checking the sign-in

`trove_curl --fail --head "{{.RegistryURL}}"` succeeds when the token may read {{if .Repository}}`{{.Repository}}`{{else}}the repository{{end}}. An unknown, malformed, expired or revoked token answers `401`, and so does an empty `{{.TokenEnvVar}}`: the function then sends an empty bearer token, which trove rejects. A valid token that may not read the repository gets `404`, the answer for a repository that does not exist, or `403` where an administrator chose not to hide the existence of repositories.
{{end}}
