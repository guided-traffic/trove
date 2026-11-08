{{if .Anonymous}}You are reading this guide without signing in. A repository that is open to anonymous readers needs no credential to pull from, and `{{if eq .Client "docker"}}docker{{else}}podman{{end}} pull` works without a login there. Pushing, moving a tag and deleting always need a token: sign in to trove to see how to create one.
{{else}}Your client signs in with a trove token, never with your password.

- A **personal access token** is yours: create it under Profile, Personal tokens. Limit it to the repositories and actions it needs and give it an expiry: pulling needs read, pushing also deploy, moving a tag also overwrite, deleting delete.
- A **service-account token** belongs to a service account, for CI jobs and other automation. An administrator creates the account and its tokens.

### How the login works

The client sends the token to trove's token endpoint (`/v2/_token`) as the password of Basic authentication, with any user name, and receives a registry token that lasts a few minutes and names the repositories and actions it grants. Every later request carries that registry token. The registry token only narrows what your trove token may do: each request is decided again against your permissions, and revoking the trove token ends the registry tokens it issued within the same revocation bound. Tools that send credentials up front (`curl -u`, a registry proxy) may use the trove token directly as the password of Basic authentication on any registry route; no token round trip is needed.

### Keep the token in {{.TokenEnvVar}}

The snippets read the token from the environment variable `{{.TokenEnvVar}}` and pass it on standard input. Never put the token in a file under version control or on a command line: the client setup page writes a token into its snippets when you create or paste one there, and such a snippet belongs in your shell session or a CI secret, not in a repository.

### In CI

Store a service-account token with only the permissions the job needs as a masked, protected CI/CD variable named `{{.TokenEnvVar}}` and run the login command of the snippet before the build. Give a job that pushes only the deploy permission on the one repository it pushes to, and let the token expire. Coming in a later release: GitLab CI jobs exchange their job ID token for a short-lived trove token.

### Checking the sign-in

A `GET` of `/v2/` on the trove host with the token as the password of Basic authentication answers `200` with `{}`. A missing, expired or revoked token answers `401` with a `WWW-Authenticate` challenge that names the token endpoint; a token that lacks the permission for an image answers `403` `DENIED` on that image.
{{end}}
