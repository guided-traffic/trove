{{if .Anonymous}}You are reading this guide without signing in. {{if .Repository}}`{{.Repository}}` is open to anonymous readers, so your client needs no credential to restore from it.{{else}}A repository that is open to anonymous readers needs no credential to restore from.{{end}} Pushing, unlisting, relisting and deleting always need a token: sign in to trove to see how to create one and how your client sends it.
{{else}}Your client signs in with a trove token, never with your password.

- A **personal access token** is yours: create it under Profile, Personal tokens. Limit it to the repositories and actions it needs and give it an expiry: restoring needs read, pushing and relisting also deploy, unlisting and deleting delete.
- A **service-account token** belongs to a service account, for CI jobs and other automation. An administrator creates the account and its tokens.

The token goes to trove in two places.

- **As the password of Basic authentication** for everything a restore does, with any user name. NuGet sends the credential of the `nuget.config` only after trove has answered a request with `401`; since NuGet 6.7 it sends it with the first request of every resource of the source. A repository that is open to anonymous readers is read without it, unless an administrator turned on forced authentication for it.
- **As the API key** for push, unlist, relist and delete. The API key is the credential of those four operations (`--api-key` of `dotnet nuget push` and `dotnet nuget delete`, `-ApiKey` of nuget.exe). trove accepts the token as the whole key or after the last colon (`ci:<token>`), so a key written as `<user name>:<token>` works too. A key that is no token, such as the placeholder `az` that some CI templates pass, is ignored and the credential of the `nuget.config` applies. The session cookie of the browser is never accepted for these operations.

### Keep the token in {{.TokenEnvVar}}

The snippets read the token from the environment variable `{{.TokenEnvVar}}`, which the shell or NuGet expands itself when the command runs. The `nuget.config` then holds no secret and can be committed. Never commit a file that holds the token itself: the client setup page writes a token into its snippets when you create or paste one there, and such a file belongs in your home directory or another place outside version control. An API key is a full trove token, so give the key of a CI job only the deploy permission on the one repository it pushes to, and let it expire.

### In CI

Store a service-account token with only the permissions the job needs as a masked, protected CI/CD variable named `{{.TokenEnvVar}}`, and commit the `nuget.config` of the snippet; the job needs nothing else. In GitLab CI the job needs no stored token at all: it exchanges its job ID token at `/api/v1/auth/oidc/token` for a CI token that lives minutes, exports it as `{{.TokenEnvVar}}` and revokes it as its last step. An Owner of your trove project or an administrator writes the identity mapping that decides what such a token may do; the manual's page GitLab CI pipelines shows the exchange.

### Checking the sign-in

`curl -u any:$TOKEN` on the address of the service index answers `200` with a valid token. A missing, expired or revoked token answers `401` with the challenge `Basic realm="trove"`; a token that lacks the permission answers `403`.{{if eq .Client "dotnet"}} `dotnet restore -v n` prints the sources it asked and the status of every request.{{else if eq .Client "nuget"}} `nuget list Acme -Source trove` lists the packages the token may see.{{else}} Visual Studio reports a source it cannot read in the Package Manager Output window, with NuGet's `NU1301` and the status of the request.{{end}}
{{end}}
