# npm package format

This page catalogs everything a repository manager does to act as an npm registry: hosting
packages in local repositories, proxying and caching an upstream registry in remote repositories,
merging both behind one registry URL in virtual repositories, client authentication and login,
publish and package-lifecycle operations, metadata calculation and storage, search, audit, and the
specifics of the npm, pnpm and Yarn clients. The protocol itself is defined by the npm project, not
by any repository manager; where the behavior comes from npm's own documentation or from the
client sources, the entry links that source. The last H2 lists every endpoint a compatible
registry must serve. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in general in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/npm/<repo-key>/` stands for the registry URL of
one repository (the path layout is the implementation's choice), `npm-local`, `npm-remote` and
`npm-virtual` are example repository keys, and `${NPM_TOKEN}` is an access token.

## Repositories and addressing

### PKG-NPM-001 · npm repositories in all three repository classes
Priority: **MVP**

npm is supported as a local, a remote and a virtual repository format (see REPO-006 in
[../repositories.md](../repositories.md)). Capabilities of the format as a whole:

- local repositories hold an organization's own packages; the system computes their package
  metadata (PKG-NPM-029);
- remote repositories proxy and cache an external registry such as `https://registry.npmjs.org`
  (PKG-NPM-038);
- virtual repositories expose local and remote members under one registry URL (PKG-NPM-042);
- the system validates data received from remote npm registries (the kind of validation is
  unspecified);
- SHA-512 integrity values are supported for npm packages (PKG-NPM-025).

Supported clients: the npm CLI from version 1.4.3, pnpm, Yarn 1 (Classic) and Yarn 2 and later
(Modern). The recommended client setup points the default registry at a virtual repository, so
that internal packages and the upstream registry are reachable through one URL and membership can
change without touching clients.

Creating a repository requires the system administrator or a project administrator role; for
others the create action is not offered. UI location: administration, repositories, create
repository, then choose the class and the npm format (UI-031 in
[../ui-inventory.md](../ui-inventory.md), REPO-053 in [../repositories.md](../repositories.md)).
A remote repository form has the upstream URL
pre-filled for the user to confirm or change; the pre-filled value is unspecified. A virtual
repository form asks for one or more local or remote members.

### PKG-NPM-002 · Registry URL of a repository
Priority: **MVP**

Every npm repository, whatever its class, answers the npm registry protocol under its own base
URL, the *registry URL*. Clients store it with a trailing slash:

```ini
registry=https://repo.example.com/npm/npm-virtual/
```

- All endpoints of the protocol reference (PKG-NPM-056 to PKG-NPM-060) are relative to this URL.
- A client can override the registry for a single command (`npm install <pkg> --registry <url>`,
  `npm publish --registry <url>`) or per package through `publishConfig.registry` in
  `package.json`, which wins over `.npmrc` for publishing.
- Read operations work against all three classes. Publishing normally targets a local
  repository; a virtual repository accepts deployments through its default deployment repository
  (PKG-NPM-045). What a publish to a remote repository returns is unspecified.
- Credentials are bound to the registry URL on the client side (PKG-NPM-005), so the URLs the
  registry embeds in its own responses (tarball URLs, login URLs) must lie under the same host and
  path prefix (PKG-NPM-007).
- Two endpoints are an exception: npm resolves the CI token exchange (PKG-NPM-013) and the
  attestation download (PKG-NPM-024) against the **host root** of the registry URL, dropping any
  path prefix. A system that serves registries below a path (`/npm/<repo-key>/`) must also answer
  those two paths at the host root and work out the repository from the package name or the
  credential, or those features stay unavailable.

### PKG-NPM-003 · Scoped packages and encoded slashes
Priority: **MVP**

Scoped packages (`@scope/name`, [npm scope documentation](https://docs.npmjs.com/cli/using-npm/scope))
work without any client-side difference: they are published, installed, merged and proxied like
unscoped ones.

- npm and pnpm send a scoped name in the URL path with the slash percent-encoded:
  `GET /@scope%2fname`. Yarn 2+ also encodes it for metadata and for tarballs, and retries a
  failed tarball request with the slash unencoded. The registry must accept both spellings on
  every path that carries a package name.
- HTTP servers and reverse proxies often reject or decode `%2f` by default, which surfaces to the
  client as HTTP 400 or as a wrong path. The registry's web server must allow encoded slashes, and
  a reverse proxy in front of it must pass them through undecoded (for Apache httpd:
  `AllowEncodedSlashes NoDecode` in the virtual host).
- On the client, a scope is bound to one registry (`@scope:registry=<url>` in `.npmrc`, or
  `npm login --scope=@scope`); one registry can serve many scopes, but a scope points at exactly
  one registry.
- Scope ownership in the public-registry sense (a user or organization owns `@scope`) is not
  part of the system; who may publish under a scope is governed by repository permissions,
  optionally narrowed by include/exclude patterns (IAM-021 in
  [../identity-and-access.md](../identity-and-access.md)).

### PKG-NPM-004 · Package name rules
Priority: **MVP**

The system's own name validation is unspecified. npm's rules
([validate-npm-package-name](https://github.com/npm/validate-npm-package-name)) for *new*
packages:

| Rule | Detail |
|---|---|
| Length | 1 to 214 characters, scope included |
| Case | lower case only |
| Characters | URL-safe only; none of `~)('!*`; no spaces; no leading or trailing whitespace |
| Start | not `.` or `_` |
| Reserved | not a Node.js core module name and not a blocked name such as `node_modules` or `favicon.ico` |

Edge case: the public registry contains legacy names that break these rules (for example upper
case). A remote or virtual repository must serve such names unchanged; only names of newly
published packages are validated.

## Client authentication

### PKG-NPM-005 · Bearer token in client configuration
Priority: **MVP**

The primary non-interactive credential. The token is written into `.npmrc`, keyed by the registry
URL without the scheme, and the client sends it as `Authorization: Bearer <token>`
([npmrc auth settings](https://docs.npmjs.com/cli/configuring-npm/npmrc#auth-related-configuration)):

```ini
registry=https://repo.example.com/npm/npm-virtual/
//repo.example.com/npm/npm-virtual/:_authToken=${NPM_TOKEN}
```

Scoped variant:

```ini
@acme:registry=https://repo.example.com/npm/npm-acme/
//repo.example.com/npm/npm-acme/:_authToken=${NPM_TOKEN}
```

- This form of configuration file editing is sometimes called "basic authentication", although
  the credential is a bearer token; the token is a user-bound token of the system (TOK-005,
  TOK-033 in [../access-tokens.md](../access-tokens.md)).
- npm resolves the credential by walking up the request URL to the longest matching
  `//host/path/:` key; a key for `//repo.example.com/` therefore covers every repository on the
  host. `.npmrc` values can reference environment variables (`${NPM_TOKEN}`).
- npm's documentation requires credentials to be keyed to a registry URL; an unkeyed
  `_authToken=` line is not used for authentication.
- A token appended to the registry value itself (`registry=<url>:_authToken=<token>`) is not a
  credential setting; clients only read the keyed form above, so client setup instructions must
  never emit the appended form.
- The same file configures pnpm (PKG-NPM-051) and Yarn 1 (PKG-NPM-052); Yarn 2+ uses its own file
  (PKG-NPM-053).

Interactions: client setup instructions generate this snippet with a fresh or a supplied token
(PKG-NPM-054); the CI OIDC exchange can supply the token at run time (PKG-NPM-013).

### PKG-NPM-006 · Basic credentials in client configuration
Priority: **MVP**

Clients can also send HTTP Basic credentials, configured as either

- `//host/path/:_auth=<base64 of user:secret>`, or
- `//host/path/:username=<user>` plus `//host/path/:_password=<base64 of secret>` (the password
  is stored base64-encoded and decoded by the client before it builds the header).

The system accepts a token in the password position (TOK-034 in
[../access-tokens.md](../access-tokens.md)). Yarn 2+ uses `npmAuthIdent` instead (`user:secret`,
or already base64-encoded).

Edge cases:

- Client setup should recommend a token or login over `_auth`. Although `_auth` is often said to be
  unsupported from npm 9 on, npm's own documentation and its credential lookup code still accept
  `_auth` when it is keyed to a registry URL; an unkeyed `_auth` is not used. Implementations
  should accept Basic credentials and document only the keyed form.
- Basic authentication can be switched off system-wide; that switch is described in
  [../identity-and-access.md](../identity-and-access.md) (IAM-036).

### PKG-NPM-007 · When clients send credentials
Priority: **MVP**

Clients decide per request whether to attach credentials; a registry that requires
authentication for reads must be configured for that on the client, and must shape its URLs so
the clients' rules match.

| Client | Rule (from the client sources) |
|---|---|
| npm, pnpm | Credentials of the longest `.npmrc` key that is a prefix of the request URL. A tarball URL on another host or outside the registry path gets no credentials. |
| Yarn 1 | Only if the request URL starts with the registry URL (host and path) **and** either `always-auth=true` is set or the package is scoped. |
| Yarn 2+ | Writes always authenticate; reads authenticate when `npmAlwaysAuth` is true, and on a best-effort basis for scoped packages; unscoped reads are anonymous otherwise. |

Consequences for the registry:

- `dist.tarball` URLs served by a repository must lie under that repository's registry URL (same
  host and path prefix), otherwise protected tarballs fail with 401.
- Anonymous read access (the anonymous user, IAM-005 in
  [../identity-and-access.md](../identity-and-access.md)) is what lets unscoped installs work
  without `always-auth`; when anonymous access is off, client setup must include `always-auth`
  (Yarn 1) or `npmAlwaysAuth` (Yarn 2+).
- A 401 to an anonymous request must carry a `WWW-Authenticate` header without the token `otp`
  (PKG-NPM-012).

### PKG-NPM-008 · Browser (web) login
Priority: **P1**

`npm login` and `pnpm login` with `--auth-type=web` (npm's default auth type), and
`yarn npm login --web-login` (Yarn 2+ uses web login without the flag only for
`registry.npmjs.org` and `registry.yarnpkg.com`),
open a browser for the user to sign in and receive a token without the user typing a secret into
the terminal. This is the way to connect npm and pnpm interactively:

```shell
npm config set registry https://repo.example.com/npm/npm-virtual/
npm login --auth-type=web
# or, without changing the default registry:
npm login --registry=https://repo.example.com/npm/npm-virtual/ --auth-type=web
# bind a scope at the same time:
npm login --registry=https://repo.example.com/npm/npm-acme/ --auth-type=web --scope=@acme
```

Protocol (from [npm-profile](https://github.com/npm/npm-profile)): the client posts to the login
start endpoint and gets two URLs, opens `loginUrl` in a browser and polls `doneUrl` until it
receives a token, which it stores as `_authToken` for that registry (details in PKG-NPM-058).

- The browser leg is the system's normal UI login, for example OpenID Connect single sign-on
  (AUTHN-009 in [../authentication-providers.md](../authentication-providers.md)).
- Edge case: on a user's very first login, the command may have to be run again after the user
  has signed in to the UI once. The reason is unspecified; a plausible one is
  that single-sign-on accounts are created on first UI sign-in (AUTHN-005 in
  [../authentication-providers.md](../authentication-providers.md)). An implementation should
  create the account during the browser leg so one run suffices.
- Limitation: `npm login` fails for a token whose subject is a transient user that is not
  registered in the system.
- If the login start endpoint answers with any 4xx or 500, npm silently falls back to legacy login
  (PKG-NPM-009).
- The lifetime and scope of the issued token are unspecified.

### PKG-NPM-009 · Legacy username and password login
Priority: **P1**

`npm login --auth-type=legacy` (also `npm adduser`, and the fallback of web login) prompts for
user name and password and sends them in a user-document `PUT`; the registry verifies them and
answers with a token that the client stores as `_authToken`. pnpm's `login` sends the same body
plus a Basic `Authorization` header. Yarn 2+ (`yarn npm login`) uses the same endpoint for every
registry other than the two public ones unless `--web-login` is given; like npm it treats a 409
as "user exists", re-reads the user document and repeats the `PUT` at its revision.

- A system without local passwords (single sign-on only) can only support this flow if it accepts
  a token in the password field; whether that works for `npm login` is unspecified.
- The 409 "user exists" branch of npm's legacy flow (re-read with `?write=true`, retry at the
  revision with Basic credentials) must work if the registry ever answers 409 (PKG-NPM-058).

### PKG-NPM-010 · Logout
Priority: **P1**

`npm logout` deletes the token from the client configuration and, when the credential is a token,
first asks the registry to invalidate it (`DELETE` on the token endpoint, PKG-NPM-058). npm treats
any error status there as a failed logout and leaves the credential in the configuration, so a
registry that does not revoke tokens this way must still answer 2xx. Whether logout revokes the
token is unspecified. Revocation of the
system's tokens in general: TOK-030 to TOK-032 in [../access-tokens.md](../access-tokens.md).

### PKG-NPM-011 · Identity and connectivity checks
Priority: **MVP**

Two cheap read endpoints that tooling uses to verify a configuration:

| Operation | Input | Output | Privilege |
|---|---|---|---|
| Who am I | the presented credential | the user name behind it | any valid credential |
| Ping | optional credential | an empty JSON object | none; with a credential it also validates it |

`npm whoami` calls the first only when the configured credential is a token (with Basic
credentials it prints the configured user name); npm also calls it internally when a command needs
the user name. `npm ping` and `npm doctor` call the second.

### PKG-NPM-012 · One-time-password challenge on writes
Priority: **P2**

npm's two-factor protocol: a write that requires a second factor is answered with 401 and a
`WWW-Authenticate` header containing `otp`; the client prompts for a code and repeats the request
with an `npm-otp` header. A newer variant answers with a body containing `authUrl` and `doneUrl`,
and the client completes the second factor in a browser. A system whose interactive users sign
in through single sign-on gets its second factor at the identity provider and can omit this. The
registry must never emit `otp` in `WWW-Authenticate` unless it implements the flow, because
clients switch into a prompt when they see it.

### PKG-NPM-013 · Trusted publishing through CI OIDC token exchange
Priority: **P1**

Current npm clients, when run in GitHub Actions, GitLab CI or
CircleCI, try to exchange the CI system's OIDC ID token for a short-lived, package-bound registry
token before `npm publish` and `npm dist-tag`, and use it instead of the configured credential
([npm CLI source](https://github.com/npm/cli/blob/latest/lib/utils/oidc.js)):

- the ID token comes from `NPM_ID_TOKEN` (GitLab CI declares it with `id_tokens:`, see CIOIDC-030
  in [../oidc-ci-integration.md](../oidc-ci-integration.md)) or, on GitHub Actions, from the
  runner's token service with audience `npm:<registry host>` (host name only, no path);
- the client posts it as a bearer credential to a package-specific exchange endpoint
  (PKG-NPM-058) and expects `{"token": "…"}`. The endpoint path is resolved against the **host
  root** of the registry URL, not below its path (`https://repo.example.com/-/npm/v1/oidc/…` for a
  registry at `https://repo.example.com/npm/npm-local/`);
- the returned token is used only for the current command and is stored under the registry's
  keyed `_authToken` for that run;
- every failure is silent: the client continues with whatever credential is configured;
- on public GitHub or GitLab projects a successful exchange can also switch on provenance
  generation (PKG-NPM-024).

A registry that implements this endpoint on top of its CI identity mappings (CIOIDC-018) lets a
GitLab job publish with `npm publish` alone, without a separate exchange step.

### PKG-NPM-014 · Client-side token and profile management
Priority: **P2**

`npm token list|create|revoke` and `npm profile get|set` call registry endpoints for the user's
tokens and profile (PKG-NPM-058). The system's own token UI and API
(TOK-025 to TOK-031 in [../access-tokens.md](../access-tokens.md)) cover the need. A registry that
does not implement them should answer 404 rather than a 2xx with an unrelated body.

## Publishing and package lifecycle

### PKG-NPM-015 · Publish through the npm protocol
Priority: **MVP**

`npm publish`, `pnpm publish`, `yarn publish` (Yarn 1) and `yarn npm publish` (Yarn 2+) upload one
package version to a local repository, or to a virtual repository that forwards to its default
deployment repository (PKG-NPM-045). The target is the default registry, the scope's registry, the
`--registry` option or `publishConfig.registry`:

```json
"publishConfig": { "registry": "https://repo.example.com/npm/npm-local/" }
```

Request (from [libnpmpublish](https://github.com/npm/cli/blob/latest/workspaces/libnpmpublish/lib/publish.js)):
one JSON document carrying the package name, a `versions` map with exactly one manifest, a
`dist-tags` map with one tag (the `--tag` value, default `latest`), the requested access level,
and the tarball base64-encoded as an attachment; optionally a second attachment with a provenance
bundle (PKG-NPM-024). The manifest's `dist` object carries `integrity` (SHA-512, Subresource
Integrity format), `shasum` (SHA-1 hex) and a `tarball` URL that the client builds itself.

Expected registry behavior (the steps follow from the protocol):

1. Authorize: deploy permission on the target local repository (IAM-022 in
   [../identity-and-access.md](../identity-and-access.md)).
2. Validate the name (PKG-NPM-004), that the version is valid SemVer, that the version does not
   exist yet (PKG-NPM-016), and that the attachment's digests match `integrity`/`shasum`.
3. Store the tarball at its canonical path (PKG-NPM-034), record the package properties
   (PKG-NPM-033), apply the dist-tag, and recalculate the package's metadata (PKG-NPM-029).
4. Rewrite `dist.tarball` to the system's own URL; npm deliberately sends an `http://` URL there.
5. Answer 2xx; npm ignores the body. On rejection answer 4xx with a JSON body containing `error`
   or `message`, which the client prints.

Limits: maximum request or tarball size is unspecified.

Interactions: webhooks for deployments ([../events-and-extensibility.md](../events-and-extensibility.md));
scanner indexing (REPO-021); build metadata (PKG-NPM-055).

### PKG-NPM-016 · Immutable versions
Priority: **MVP**

From npm's documentation: publishing fails if the name and version already exist, and a name and
version that was once published can never be used again, even after unpublish. Lockfiles store
`integrity` per version and fail the install if the bytes change. Whether the system enforces this
for npm, or whether an administrator with overwrite permission can replace a version (IAM-022), is
unspecified; an implementation should reject a second publish of
an existing version regardless of overwrite permission, or at least make that the default.

### PKG-NPM-017 · Deploy a package tarball through the UI or generic upload
Priority: **P1**

A `.tgz` can also be deployed without the npm client: in the UI through the artifact browser's
deploy dialog (ART-006 in [../artifacts.md](../artifacts.md), UI-013 in
[../ui-inventory.md](../ui-inventory.md)), or by a generic file upload. Path enforcement
(PKG-NPM-018) compares the file name with the package's own metadata, and strict layout
(PKG-NPM-035) indexes uploads on the canonical path. From that it follows that the system reads
name and version from the tarball's `package.json`, indexes the upload like a published package
and recalculates the package document. The companion CLI's
default publish mode uploads this way (it packs, then uploads; PKG-NPM-055).

Edge case: an uploaded file can land at a path that does not match its name and version (for
example `npm-local/misc/widget-1.0.0.tgz` *(example)*). What the system does then depends on
PKG-NPM-018 and PKG-NPM-035.

### PKG-NPM-018 · Reject uploads whose path does not match the package
Priority: **P1**

One switch for the whole system, effective in all npm local repositories at once: the system
refuses an upload whose file name disagrees with the name and version inside the package's own
`package.json`.

| Setting | Meaning | Default |
|---|---|---|
| Enforce npm path layout (system-wide) | Reject npm uploads whose file name disagrees with the package's internal name and version | off (opt-in; must be activated) |

- Not retroactive: packages uploaded before activation stay where they are.
- Differs from strict layout (PKG-NPM-035), which is per repository and stores a non-compliant
  upload without indexing it.

### PKG-NPM-019 · Unpublish
Priority: **MVP**

Required by the npm protocol
([npm unpublish](https://docs.npmjs.com/cli/commands/npm-unpublish),
[libnpmpublish unpublish](https://github.com/npm/cli/blob/latest/workspaces/libnpmpublish/lib/unpublish.js)).

| Client action | Requests | Registry effect |
|---|---|---|
| `npm unpublish name@version` (other versions remain) | read the document with `?write=true`; `PUT` the document without that version at its revision; read again; `DELETE` the tarball at the new revision | the version and its tarball are removed; dist-tags that pointed to it are removed; if it was `latest`, the client has already moved `latest` to the highest remaining version |
| `npm unpublish name`, `name@*`, or the last remaining version | `DELETE` the package at its revision | the whole package is removed |

- Privilege: delete permission on the local repository (IAM-022).
- The removed name and version stays blocked for new publishes (PKG-NPM-016). npm's public
  registry also blocks republishing an entirely unpublished name for 24 hours; whether a private
  registry needs that is unspecified.
- Unpublish through a virtual repository is unspecified; routing it to the
  default deployment repository, like publish (PKG-NPM-045), is the consistent choice. Cached
  copies in a remote repository are removed through the generic delete operation in
  [../artifacts.md](../artifacts.md).

### PKG-NPM-020 · Deprecate and undeprecate
Priority: **P1**

`npm deprecate <pkg>@<range> "<message>"` reads the full document
with `?write=true`, sets `deprecated` on every version that satisfies the range (pre-releases
included), and `PUT`s the whole document back as read, so without tarball data (`_attachments`
empty or absent); an empty message removes the deprecation
([npm deprecate](https://docs.npmjs.com/cli/commands/npm-deprecate)). The registry
must store the message per version and serve it in both the full and the abbreviated document,
where clients print it as a warning on install. Privilege: unspecified (it changes
metadata, not content; deploy or annotate permission are both plausible).

### PKG-NPM-021 · Dist-tag management
Priority: **MVP**

A dist-tag maps a name (`latest`, `next`, `beta`, …) to one version; `npm install <pkg>@<tag>`
installs that version, and a plain `npm install <pkg>` installs `latest`
([npm dist-tag](https://docs.npmjs.com/cli/commands/npm-dist-tag)).

| Operation | Input | Output | Privilege |
|---|---|---|---|
| List tags | package name | map of tag to version | read |
| Set tag | package name, tag, version (JSON string body) | 2xx | unspecified; at least deploy on the local repository is plausible |
| Remove tag | package name, tag | 2xx | as set |

- Publishing sets the tag given with `--tag` (default `latest`) to the new version.
- Tag names that are valid SemVer ranges (for example `v1.4`) are rejected by the client; the
  registry should reject them as well.
- The companion CLI routes `dist-tag` through its configured deployment repository
  (PKG-NPM-055). Dist-tag writes against a virtual repository are unspecified (see PKG-NPM-045).
- How `latest` is computed for local and virtual repositories: PKG-NPM-031; how custom tags are
  resolved across virtual members: PKG-NPM-043.

### PKG-NPM-022 · Maintainer list changes
Priority: **P2**

`npm owner add|rm|ls` reads the `maintainers` of the full document, looks up the other user with
the user-document endpoint, and `PUT`s a *partial* document containing only `_id`, `_rev` and
`maintainers` at the current revision. In a system where
permissions decide who may publish, the maintainer list is informational. A registry that does not
support it must reject the partial `PUT` rather than treat it as a full replacement, which would
delete every version.

### PKG-NPM-023 · Staged publishing
Priority: **Later**

Recent npm versions can publish to a staging area first (`npm publish --stage`, a `POST` with the
publish body that returns a stage ID). The release-set and
promotion features of [../distribution-and-lifecycle.md](../distribution-and-lifecycle.md) address
the same need.

### PKG-NPM-024 · Provenance bundles and attestation links
Priority: **P2**

With `npm publish --provenance` (or automatically under trusted publishing on public CI projects)
npm attaches a Sigstore provenance bundle as a second attachment of the publish request. Provenance
generation is supported only on GitHub Actions and GitLab CI. Public registry versions advertise
attestations through `dist.attestations` (a URL and a predicate type). `npm audit signatures`
does not fetch that URL as given: it takes only its path (`/-/npm/v1/attestations/<name>@<version>`)
and resolves it against the **host root** of the configured registry
([pacote source](https://github.com/npm/pacote/blob/main/lib/registry.js)). A remote or virtual
repository that passes `dist.attestations` through therefore only verifies if the system answers
that path at its host root (PKG-NPM-056).

Remote and virtual repositories pass `dist.attestations` through from the upstream
(PKG-NPM-037); storing and serving provenance for locally published packages is unspecified.

## Package metadata

### PKG-NPM-025 · Full package document
Priority: **MVP**

`GET <registry>/<name>` returns the package document (informally "packument") as defined in
npm's [package metadata documentation](https://github.com/npm/registry/blob/main/docs/responses/package-metadata.md).
It is served when the client asks for `application/json` or sends no `Accept` header.

<details>
<summary>Fields of the full document</summary>

| Field | Content |
|---|---|
| `_id`, `name` | Package name |
| `_rev` | Opaque revision, required for the `-rev` write paths (PKG-NPM-057) |
| `dist-tags` | Map of tag to version; `latest` should always be present |
| `versions` | Map of version to the version's manifest: the published `package.json` plus `_id` (`name@version`), `dist`, and publisher fields such as `_npmUser`, `_npmVersion`, `_nodeVersion` |
| `versions[v].dist` | `tarball` (URL), `shasum` (SHA-1 hex), `integrity` (`sha512-<base64>`), optionally `fileCount`, `unpackedSize`, `signatures` (PKG-NPM-037), `attestations` (PKG-NPM-024) |
| `versions[v].deprecated` | Deprecation message (PKG-NPM-020) |
| `time` | `created`, `modified`, and one ISO-8601 timestamp per version (PKG-NPM-027) |
| `maintainers`, `author`, `contributors`, `description`, `homepage`, `keywords`, `license`, `repository`, `bugs`, `readme`, `readmeFilename` | Hoisted from the latest version; `readme` holds at most the first 64 KiB of the most recently published version's README |
| `users` | Stars (public registry; optional) |

</details>

- The full document can exceed 10 MB for packages with many versions; that is why clients prefer
  the abbreviated form (PKG-NPM-026).
- Every `dist.tarball` must be an absolute URL under the serving repository's registry URL
  (PKG-NPM-007), also for documents served by remote and virtual repositories.
- The per-package metadata file is referred to as the package's `package.json`; its internal
  storage form is otherwise unspecified (PKG-NPM-034).

### PKG-NPM-026 · Abbreviated package document
Priority: **MVP**

When the request's `Accept` header prefers `application/vnd.npm.install-v1+json` (npm, pnpm and
Yarn 1 send `application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*`), the
registry returns only the fields needed to install: top-level `name`, `modified`, `dist-tags`,
`versions`; per version `name`, `version`, `dist` (always) and, where present, `deprecated`,
`dependencies`, `optionalDependencies`, `devDependencies`, `bundleDependencies`,
`peerDependencies`, `peerDependenciesMeta`, `acceptDependencies`, `bin`, `directories`,
`engines`, `cpu`, `os`, `funding`, `_hasShrinkwrap`, `hasInstallScript`
([package metadata, abbreviated format](https://github.com/npm/registry/blob/main/docs/responses/package-metadata.md#abbreviated-metadata-format)).

- The response must carry `Content-Type: application/vnd.npm.install-v1+json`; pnpm uses that
  header to decide whether it got the abbreviated form and strips a document labeled
  `application/json` down on its side.
- If the abbreviated request answers 404, npm retries once with a full-document request
  ([pacote source](https://github.com/npm/pacote/blob/main/lib/registry.js)); a registry that
  supports only the full document still works, at a bandwidth cost.
- npm's format has no `time` in the abbreviated document. A registry may add the per-version
  `time` map anyway; pnpm then needs no full-document request for its release-age check once
  the client declares the registry as supporting it (`registrySupportsTimeField`, PKG-NPM-027).
- Whether the system serves the abbreviated form is unspecified.

### PKG-NPM-027 · Publish times for client-side release-age policies
Priority: **MVP**

npm package managers now ship a client-side "immaturity" policy: freshly published versions are
not installed until a minimum age has passed (for example 24 hours), so a package published to a
local repository may seem unavailable. Those policies read the per-version timestamps in `time`:

| Client | Settings (default) | Exemption list | Metadata need |
|---|---|---|---|
| npm | `before` (date; not set), `min-release-age` (days; not set) | `min-release-age-exclude` (names or globs, e.g. `@acme/*`) | requests the **full** document whenever a cutoff is set, because the abbreviated one has no `time` |
| pnpm | `minimumReleaseAge` (minutes; 1440 in current pnpm documentation) | `minimumReleaseAgeExclude` | requests the full document unless the registry is declared to put `time` into abbreviated documents (`registrySupportsTimeField`, default false); with `minimumReleaseAgeIgnoreMissingTime` (default true) a document without `time` skips the check instead of failing |
| Yarn 2+ | `npmMinimalAgeGate` (`1d` in current Yarn sources) | `npmPreapprovedPackages` | always requests the full document |

Sources: [npm config](https://docs.npmjs.com/cli/using-npm/config),
[pnpm settings](https://pnpm.io/settings),
[Yarn settings](https://yarnpkg.com/configuration/yarnrc). The defaults are the clients' and move
with their releases.

Consequences: the full document of every repository class must contain a correct `time` entry
for every version (for local packages: the publish time; for remote packages: the upstream's
value; for virtual repositories: merged per version). Omitting `time` is not a way around the
policy: pnpm skips its check only under its default setting, and how npm and Yarn treat a
document without `time` is not covered here. Teams publishing internal packages and consuming
them immediately must configure an exemption on the client; the registry cannot override the
policy.

### PKG-NPM-028 · Single-version document
Priority: **P1**

`GET <registry>/<name>/<version>` returns the manifest of one version; `<version>` may also be
`latest` ([REGISTRY-API](https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md)). Used by
tooling and some clients for a quick lookup; whether the system serves it is unspecified.

### PKG-NPM-062 · Tarball download
Priority: **MVP**

Clients download a version's content from the URL in its `dist.tarball`; they do not build the
URL themselves (Yarn 2+ only checks whether it has the conventional shape, PKG-NPM-053). The
conventional path below the registry URL is `<name>/-/<basename>-<version>.tgz`
(`@scope/name/-/name-1.0.0.tgz` for scoped packages; PKG-NPM-034, PKG-NPM-056).

- Integrity: the client checks the bytes against `dist.integrity` (SHA-512) of the document it
  resolved from, or `dist.shasum` (SHA-1) for old entries without `integrity`, and aborts the
  install on a mismatch (`EINTEGRITY`). The tarball served for a version must therefore be
  exactly the one whose digest the serving repository's document advertises.
- Local repositories serve the stored file; with strict layout only from the canonical path
  (PKG-NPM-035).
- Remote repositories serve the cached copy, or fetch the tarball from the upstream's original URL
  and cache it (PKG-NPM-038); fetching an uncached tarball needs the deploy/cache permission
  (IAM-023 in [../identity-and-access.md](../identity-and-access.md)). Cached tarballs never
  expire (PKG-NPM-039).
- Virtual repositories resolve the tarball through their members in resolution order and must
  return the one from the member whose manifest they merged for that version (PKG-NPM-042).
- Credentials arrive only if the URL lies under the registry URL (PKG-NPM-007); tarballs are
  immutable and cacheable without limit (PKG-NPM-032).
- Range requests and `HEAD` support are unspecified.

### PKG-NPM-029 · Metadata calculation for local repositories
Priority: **MVP**

For local repositories the system computes the package documents itself from the stored tarballs
and the manifests they carry; there is no client-supplied index to trust. The documents must
reflect every publish, upload, unpublish, deprecation and dist-tag change; when the calculation
runs is unspecified. The search index of a local repository is calculated asynchronously
(PKG-NPM-048); whether the package document is updated before the publish request returns is
unspecified, but pipelines that publish and immediately install from the same registry depend
on it.

When signing keys are assigned to the repository, calculation also adds signatures
(PKG-NPM-037). Calculated metadata covers only packages stored on their canonical path when strict
layout is on (PKG-NPM-035).

### PKG-NPM-030 · Recalculate npm metadata
Priority: **P1**

An administrative operation that rebuilds all npm package indexes of one repository from the
stored tarballs; the general mechanism is REPO-009 in [../repositories.md](../repositories.md).

| Operation | Input | Output | Privilege |
|---|---|---|---|
| Recalculate npm metadata | repository key | 200 with a plain-text body; 401 for invalid credentials; 403 without permission; 500 on failure | unspecified beyond "authenticated and permitted" |

- Supported for local repositories only.
- Assigning a signing key to a repository triggers the same recalculation, which then signs every
  package (PKG-NPM-037).
- Whether it runs synchronously, and how long it takes for large repositories, is unspecified.

### PKG-NPM-031 · Selection of the `latest` dist-tag
Priority: **MVP**

By default the system treats as `latest` the version with the highest SemVer number. A
system-wide setting switches to "most recently published version wins".

| Setting | Meaning | Default |
|---|---|---|
| Latest by publish time (system-wide) | `latest` points to the most recently uploaded version instead of the highest SemVer version | off |

Edge cases (unspecified):

- whether pre-release versions take part in "highest SemVer";
- how this rule interacts with the `latest` tag that every publish without `--tag` sends in its
  body, and with an explicit `npm dist-tag add pkg@1.2.4 latest` (for example after a
  maintenance release on an older line);
- for virtual repositories the rule applies across members (PKG-NPM-043, REPO-036 in
  [../repositories.md](../repositories.md)).

### PKG-NPM-032 · Conditional requests and cache headers
Priority: **MVP**

From the client sources: Yarn 2+ revalidates cached package
documents with `If-None-Match` and `If-Modified-Since` and reuses its copy on 304; npm and pnpm
use HTTP caching through their fetch layers. A registry should send `ETag` and `Last-Modified` on
package documents, answer conditional requests with 304, and keep the representation (full vs
abbreviated) part of the cache key (`Vary: Accept`). Tarballs are immutable and can be cached
indefinitely.

### PKG-NPM-033 · Stored package properties
Priority: **MVP**

Every npm package that is deployed to a local repository or cached in a remote repository is
annotated with two properties, the package name and the package version (property model: ART-021
in [../artifacts.md](../artifacts.md)). They make npm packages findable through property search
(SRCH-007 in [../search-and-query.md](../search-and-query.md)). Package search additionally offers
npm-specific fields for the scope and the keywords (SRCH-050). The property key names are the
implementation's choice.

### PKG-NPM-034 · Storage layout
Priority: **MVP**

- Tarballs are stored per repository under a folder named after the package; for scoped packages
  the scope is a parent folder (`@scope/name/…`). The canonical path is built from package name,
  optional scope and version; the exact file name pattern below that folder is unspecified. The
  npm protocol path for downloads is `<name>/-/<basename>-<version>.tgz`
  (`@scope/name/-/name-1.0.0.tgz` for scoped packages), and Yarn 2+ only treats a tarball URL of
  that shape as "conventional" (PKG-NPM-053).
- Package documents are kept in a separate per-repository metadata area, again keyed by package
  name (and scope). Because it is separate, path-based permissions must cover both areas
  (PKG-NPM-047). The repository's signing-key document also lives in that metadata area.
- npm uses `.tgz` as the lead file for retention and cleanup policies, and archive policies
  support npm (RET-009 in [../retention-and-cleanup.md](../retention-and-cleanup.md)).
- The system offers bundled repository layouts named "npm" and "npm test" (REPO-047 in
  [../repositories.md](../repositories.md)); their path patterns are unspecified.

### PKG-NPM-035 · Strict layout enforcement per local repository
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Enforce strict layout | Packages are stored and resolved only on their canonical path (name, optional scope, version). An upload to another path is stored but not indexed. | on for newly created npm local repositories; existing repositories keep their previous behavior until the setting is switched on |

- When on, an install resolves a package only from its canonical path; the repository is not
  searched for the same package elsewhere. A direct `GET` on a non-compliant path still returns the
  file.
- A virtual repository resolving from a strict-layout member uses the requested path on that member
  instead of searching it.
- UI location: npm local repository settings, basic tab, npm section (REPO-029 in
  [../repositories.md](../repositories.md)).
- Privilege to change: system or project administrator.
- A system that only ever writes through the npm protocol stores everything canonically and can
  treat this as always on.

### PKG-NPM-036 · Strict-layout conversion of existing repositories
Priority: **P2**

Switching strict layout on for an existing local repository starts a background job that moves
every `.tgz` that is not on its canonical path to that path within the same repository. While the
job runs, packages not yet moved are still resolved, so downloads keep working.

| Operation | Input | Output | Privilege |
|---|---|---|---|
| Start conversion | repository key | unspecified | manage permission on the repository |
| Resume conversion | repository key | unspecified; continues with the remaining packages | manage permission on the repository |

- Local repositories only; not for remote or virtual ones.
- Only a cancelled or failed conversion can be resumed. A full re-run is triggered by switching
  the setting off and on again.

### PKG-NPM-037 · Registry signatures for `npm audit signatures`
Priority: **P2**

`npm audit signatures` verifies, for every installed package, an ECDSA signature over
`<name>@<version>:<integrity>` found in `dist.signatures` (`keyid`, `sig`) against the keys the
registry publishes at its keys endpoint (PKG-NPM-056), and also verifies provenance attestations
([npm audit](https://docs.npmjs.com/cli/commands/npm-audit#audit-signatures)).

| Setting (local repository) | Meaning | Default |
|---|---|---|
| Primary signing key | Name of the ECDSA key pair used to sign the repository's packages | not set |
| Secondary signing key | Name of a second key pair; its role for npm signing is unspecified | not set |

- Keys are managed centrally (IAM-046 in [../identity-and-access.md](../identity-and-access.md));
  UI location: administration, security, key management, add an ECDSA key with name, alias,
  public and private key; then repository settings, signing key section, primary key. The
  elliptic-curve pair is created with GnuPG and both halves are uploaded ASCII-armored.
- Saving a key on the repository re-indexes the repository and signs all its packages
  (PKG-NPM-030). Whether a package is signed is visible in its `dist.signatures`.
- Remote repositories pass the upstream's signatures and `dist.attestations` through unchanged;
  the public npm registry signs with ECDSA. An upstream that does not sign makes the command
  report missing signatures.
- Virtual repositories: every local member needs signing enabled and every remote member must
  point to a signing upstream. Mixing signed and unsigned members makes the client report errors,
  which also stops the client from continuing (for example from downloading).
- If a repository uses include patterns, they must admit the signing-key document in the metadata
  area (PKG-NPM-034), or the command fails.
- The command reports the number of signed packages; the response format of the
  keys endpoint is npm's (PKG-NPM-056). npm looks the keys up in Sigstore's public trust root
  first and, for any other registry, logs a warning and falls back to the registry's own keys
  endpoint ([npm source](https://github.com/npm/cli/blob/latest/lib/utils/verify-signatures.js)).

## Remote repositories

### PKG-NPM-038 · Proxying an upstream npm registry
Priority: **MVP**

A remote npm repository proxies one upstream registry (typically `https://registry.npmjs.org`, or
another instance of the system) and caches what clients fetch (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)).

- Package documents fetched from upstream are cached as metadata that expires (PKG-NPM-039);
  tarballs are cached as immutable content.
- In every document served, `dist.tarball` URLs are rewritten from the upstream host to the remote
  repository's own registry URL (or, when served through a virtual repository, to the virtual
  repository's URL), so that clients fetch tarballs through the system and send their credentials
  (PKG-NPM-007).
- Both `@scope%2fname` and `@scope/name` must map to the same upstream request.
- Consequence of the rewrite: an upstream may serve tarballs from
  a URL that does not follow the conventional `<name>/-/<basename>-<version>.tgz` shape or lies on
  another host. A rewritten request must still be traced back to the upstream's original
  `dist.tarball`, so the remote repository has to keep that URL per version (for example in the
  cached document) rather than derive it from the path.
- Reading cached content needs read permission; fetching uncached content from upstream needs the
  deploy/cache permission on the remote repository (IAM-023).
- Every newly cached package is annotated with its name and version properties (PKG-NPM-033).
- The system validates remote npm repository data (PKG-NPM-001); what is validated (for example
  integrity of downloaded tarballs against `dist.integrity`) is unspecified. Verifying the tarball
  against the upstream's `integrity` before caching is the safe default.
- Edge case: a remote repository whose upstream is another instance of the system uses that
  instance's npm registry URL as upstream.

### PKG-NPM-039 · Remote metadata freshness and invalidation
Priority: **MVP**

The general remote-repository settings apply (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)); npm has no format-specific exception,
so the general defaults hold:

| Setting | Meaning | Default |
|---|---|---|
| Metadata retrieval cache period | How long a cached package document is used before the upstream is asked again | 7200 s; minimum 30 s (values below 30 s on existing repositories stay until edited) |
| Missed retrieval cache period | How long an upstream 404 is remembered; 0 disables negative caching | 1800 s |
| Metadata retrieval timeout | How long a metadata refresh may wait for the upstream; when it runs out, the previously cached document is returned | 60 s |

- Cache invalidation marks all cached metadata of the repository, or of one path, as expired
  without deleting it; each document is refreshed on its next request. Tarballs are never
  invalidated.
- If the upstream is unreachable when expired metadata is requested, the stale copy is served
  with HTTP 200. After a change of the upstream URL or of upstream filtering, this can surface to
  npm as `ETARGET` (requested version not found in the stale document).
- A newly published upstream version becomes visible only after the cache period has elapsed;
  this also delays `npm search` results from remote repositories (PKG-NPM-048).
- Full-repository invalidation on very large caches can take minutes; path-scoped invalidation is
  the recommended form.

### PKG-NPM-040 · Credentials for a private upstream registry
Priority: **P1**

A remote repository can authenticate to its upstream with a user name and a password or personal
access token. For the public npm registry there is an additional constraint: the user name must
be the real npm account name that owns the token; unlike other upstreams, an arbitrary user name
together with a token is not accepted. Field location: remote repository settings, remote
authentication.

### PKG-NPM-041 · FHIR package registry as upstream
Priority: **P2**

Remote npm repositories can proxy the FHIR package registry at `https://packages.simplifier.net/`
(healthcare interoperability packages distributed in npm format). Constraints:

- a virtual npm repository must not mix FHIR remote repositories with ordinary npm repositories;
  it contains either only FHIR repositories and packages or only ordinary npm ones;
- the "test connection" action of the remote repository form is not available for this upstream.

## Virtual repositories

### PKG-NPM-042 · Merged package documents
Priority: **MVP**

A virtual npm repository answers a package-document request by merging the documents of all
members that know the package (local members: calculated metadata; remote members: cached or
fetched upstream metadata) in the configured resolution order (REPO-035 in
[../repositories.md](../repositories.md)).

- `versions`: union over the members. For a version that exists in several members, which
  member's manifest wins is unspecified; resolution order is the natural rule.
- `time`: should be taken per version from the member whose manifest is used (unspecified).
- `dist.tarball`: the form is unspecified; the client credential rules
  (PKG-NPM-007) require URLs under the virtual repository's registry URL, which then resolves the
  tarball through its members in order.
- `dist-tags`: see PKG-NPM-043.
- With priority resolution (REPO-018), only members flagged for priority contribute if any of them
  has the package; this lets an internal package shadow a same-named public one (protection
  against dependency confusion).
- A latest-version request consults every member including upstreams (REPO-036).
- Merged documents are cached (PKG-NPM-044).
- Access: the caller needs read permission on the members (IAM-024), including their metadata
  paths (PKG-NPM-047).

### PKG-NPM-043 · Dist-tag resolution across members
Priority: **MVP**

| Tag | Default rule | Alternatives (system-wide settings) |
|---|---|---|
| `latest` | highest SemVer version over all members (PKG-NPM-031) | most recently published version |
| any other tag | the version from the first member, in resolution order, that has the tag; versions and publish dates are not compared | (a) highest SemVer version among all members' values for the tag; (b) most recently published version among them |

| Setting | Meaning | Default |
|---|---|---|
| Resolve custom dist-tags by strategy | Ignore member order for tags other than `latest`; collect every member's version for the tag and pick the latest one | off |
| Latest by publish time | With the setting above on: "latest" means most recently published instead of highest SemVer; also governs `latest` itself (PKG-NPM-031) | off |

Edge case: with the default rule, an upstream `beta` tag can hide an internal `beta` tag if the
remote member is ordered first.

### PKG-NPM-044 · Virtual metadata cache period
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Metadata retrieval cache period | How long a merged package document is cached before the members are asked again for newer versions; 0 disables caching | 7200 s |

Only expiring files (package documents) are affected; tarballs are not. UI location: virtual
repository settings, basic tab. See REPO-037 in [../repositories.md](../repositories.md).

### PKG-NPM-045 · Publishing through a virtual repository
Priority: **MVP**

A publish sent to a virtual repository's registry URL is stored in the virtual repository's
default deployment repository (REPO-038 in [../repositories.md](../repositories.md)); this is what
lets a team use one registry URL for both install and publish. Whether unpublish, deprecate and
dist-tag writes are routed the same way, and what a write returns when no default deployment
repository is set, is unspecified; routing every write to the default deployment
repository and rejecting writes without one is the consistent behavior. The companion CLI instead
takes separate resolve and deploy repositories (PKG-NPM-055).

### PKG-NPM-046 · External dependency rewrite
Priority: **P1**

Packages may declare dependencies that are not registry packages: git URLs, GitHub shorthands,
HTTP tarball URLs. The client fetches those directly from the internet, bypassing the registry and
its controls. A virtual npm repository can rewrite such dependencies in the documents it serves so
that the client fetches them through the system, which downloads and caches them in a designated
remote repository.

| Setting | Meaning | Default |
|---|---|---|
| Rewrite external dependencies | Turn the rewrite on | off |
| Remote repository for external dependencies | The member remote repository in which fetched external dependencies are cached; a dedicated remote repository is recommended | empty |
| External dependency allow list | Ant-style patterns of sources dependencies may be fetched from, e.g. `github.com` to allow only that host | `**` (any source) |

- Supported source forms include all npm shorthand resolvers, for example
  `git+ssh://user@hostname:project.git#commit-ish`, `git+ssh://user@hostname/project.git#commit-ish`
  and `git+https://git@github.com/<user>/<repo>.git`.
- The rewritten form of a dependency (which URL the client receives) is unspecified. The rewrite
  must produce something every client can fetch with the registry
  credentials (PKG-NPM-007), and lockfiles will record it.
- A source outside the allow list is not fetched; what the client sees then is unspecified.
- UI location: virtual repository settings, advanced tab. Shared definition: REPO-040 in
  [../repositories.md](../repositories.md).
- Client side, current npm refuses git dependencies (`allow-git`) and tarball-URL dependencies
  (`allow-remote`) by default. A URL dependency on the same host as the configured registry is
  still installed ([npm config](https://docs.npmjs.com/cli/using-npm/config)). A rewrite that
  points such dependencies at the system's own host therefore also keeps them installable under
  those defaults.

### PKG-NPM-047 · Permissions covering metadata as well as tarballs
Priority: **MVP**

Because package documents live in a metadata area separate from the tarballs (PKG-NPM-034), a
permission policy narrowed by include patterns must admit both, or metadata requests through a
virtual repository fail although the user may read the tarball. The rule per package: include
the package folder and everything below it, and the package's entry and everything below it in
the metadata area; for scoped packages the same with `@scope/name`. The
rule for path patterns in general is IAM-021 in
[../identity-and-access.md](../identity-and-access.md).

A permission model keyed by package name (or name pattern, e.g. `@acme/*`) rather than by storage
path avoids the trap altogether: one grant then covers document, tarballs, dist-tags and
deprecations of the package.

## Search and audit

### PKG-NPM-048 · `npm search`
Priority: **P1**

`npm search <terms>` (and `pnpm search`) query the registry's search endpoint with free text and
paging (PKG-NPM-056). The system answers it against npm repositories of every class.

- Local repositories: the search index is calculated asynchronously after a publish, so a new
  package may not be found immediately; the same holds for virtual repositories containing local
  members.
- Remote repositories: a new upstream package is found only after the system has refreshed its
  metadata per the retrieval cache period (PKG-NPM-039).
- npm's documented query qualifiers (`author:`, `maintainer:`, `scope:`, `keywords:`, `not:`/`is:`)
  and score weights are unspecified for the system.
- Beyond the npm client, packages are found through the system's own search (SRCH-051 in
  [../search-and-query.md](../search-and-query.md)) and property search on the name and version
  properties (PKG-NPM-033).

### PKG-NPM-049 · `npm audit` forwarding
Priority: **P1**

`npm audit`, `npm audit fix` and the audit that runs during `npm install` post the dependency list
to the registry (PKG-NPM-059); pnpm and `yarn npm audit` use the same bulk endpoint. The system
answers by forwarding the request to an audit provider and returning its report.

| Setting | Meaning | Default |
|---|---|---|
| Audit provider URL (system-wide) | The registry the audit requests are forwarded to | `https://registry.npmjs.org` |

- Enabled by default on virtual npm repositories that contain at least one remote member whose
  upstream supports audit (the public npm registry, or another instance of the system); in newer
  versions also on remote npm repositories whose upstream supports audit directly.
- Privilege: read permission on the remote or virtual repository.
- An integrated vulnerability scanner could enrich or generate the report even without a remote
  member; such enrichment has been discontinued in favor of a separate scanning command, so a
  forwarding-only implementation matches current behavior (scanning:
  [../security-scanning-and-curation.md](../security-scanning-and-curation.md)).
- Privacy: forwarding sends the names and versions of all dependencies, internal ones included, to
  the provider. Whether internal names are filtered first is unspecified; an
  implementation can drop names that exist only in local repositories before forwarding.
- A registry without audit support should answer 404 quickly: the install still succeeds (npm
  records the audit error and continues), while `npm audit` itself reports the failure.

## Clients

### PKG-NPM-050 · npm client configuration
Priority: **MVP**

Client-side steps the system's documentation and setup instructions must cover:

```shell
# default registry and browser login
npm config set registry https://repo.example.com/npm/npm-virtual/
npm login --auth-type=web

# install
npm install lodash
npm install @acme/ui
npm install lodash --registry https://repo.example.com/npm/npm-remote/
npm install colors@beta            # by dist-tag

# publish to the configured or an explicit registry
npm publish
npm publish --registry https://repo.example.com/npm/npm-local/
```

For scripts and CI, `.npmrc` with a keyed token (PKG-NPM-005). The user-level file is
`~/.npmrc` on Linux and macOS and `%USERPROFILE%\.npmrc` on Windows; a project-level `.npmrc` next
to `package.json` overrides it.

Client behaviors that affect the registry: lockfiles record each tarball's full `resolved` URL,
and npm replaces only the public registry's host with the configured one by default
(`replace-registry-host`); lockfiles written against the system therefore keep pointing at it, so
tarball URLs must stay stable across upgrades and URL changes.

### PKG-NPM-051 · pnpm
Priority: **MVP**

pnpm reads the same `.npmrc` keys as npm and is configured the same way (`pnpm config set
registry …`, `pnpm login --auth-type=web`, `pnpm login --registry=<url> --scope=@acme`,
`pnpm add <pkg>`, `pnpm add <pkg>@<version>`, `pnpm publish`, `pnpm publish --registry <url>`,
`publishConfig.registry`). Differences that matter to the registry, from the pnpm sources:

- pnpm checks the response `Content-Type` of package documents to decide whether it got the
  abbreviated form (PKG-NPM-026).
- `pnpm login` sends the legacy login body together with a Basic `Authorization` header
  (PKG-NPM-009).
- `pnpm audit` posts an uncompressed JSON body to the bulk endpoint. It rejects the whole response
  unless it is an object keyed by package name whose advisories each carry a string
  `vulnerable_versions`, and it counts only advisories with a numeric `id` and a severity from
  `info`, `low`, `moderate`, `high`, `critical`
  ([pnpm source](https://github.com/pnpm/pnpm/blob/main/pnpm11/deps/compliance/audit/src/index.ts);
  PKG-NPM-059).
- pnpm's release-age setting `minimumReleaseAge` is on by default in current releases
  (PKG-NPM-027), so freshly published internal packages need `minimumReleaseAgeExclude`.
- `pnpm unpublish`, `pnpm dist-tag`, `pnpm search`, `pnpm whoami` use the npm endpoints.

The first-login caveat applies to `pnpm login` as well (PKG-NPM-008).

### PKG-NPM-052 · Yarn Classic (1.x)
Priority: **MVP**

Yarn 1 uses npm's configuration: set the registry with `npm config set registry <url>`, log in
with `npm login --auth-type=web`, and add `always-auth=true` to `~/.npmrc`, because Yarn 1 sends
credentials for unscoped packages only with that flag (PKG-NPM-007). It requests the abbreviated
document. Publishing: `yarn publish` to `publishConfig.registry` or `--registry <url>`. Install:
`yarn add <pkg>` or `yarn add <pkg>@<version>`.

### PKG-NPM-053 · Yarn Modern (2 and later)
Priority: **MVP**

Yarn 2+ ignores `.npmrc` and reads `.yarnrc.yml`
([Yarn settings](https://yarnpkg.com/configuration/yarnrc)). Setup:

```shell
yarn config set --home npmRegistryServer "https://repo.example.com/npm/npm-virtual/"
yarn config set --home 'npmRegistries["https://repo.example.com/npm/npm-virtual/"].npmAlwaysAuth' true
yarn config set --home 'npmRegistries["https://repo.example.com/npm/npm-virtual/"].npmAuthToken' "${NPM_TOKEN}"
# only for a registry without TLS:
yarn config set unsafeHttpWhitelist --home --json '["repo.example.com"]'
```

<details>
<summary>Yarn 2+ settings that touch the registry</summary>

| Setting | Meaning | Default |
|---|---|---|
| `npmRegistryServer` | Default registry; HTTPS required unless the host is in `unsafeHttpWhitelist` | `https://registry.yarnpkg.com` |
| `npmAuthToken` | Bearer token | none |
| `npmAuthIdent` | Basic credential, `user:password` or base64 | none |
| `npmAlwaysAuth` | Send credentials on every read | false |
| `npmScopes.<scope>.*` | Per-scope registry and credentials | none |
| `npmRegistries."<url>".*` | Per-registry credentials; the key must name the same URL as the registry setting | none |
| `npmPublishRegistry`, `npmAuditRegistry` | Separate registries for publish and audit; default to `npmRegistryServer` | none |
| `npmMinimalAgeGate`, `npmPreapprovedPackages` | Release-age policy (PKG-NPM-027) | `1d`, empty (current Yarn sources) |

</details>

Behaviors that matter to the registry, from the Yarn sources:

- Package documents are requested without the abbreviated `Accept` header (full document) and
  revalidated with `If-None-Match`/`If-Modified-Since` (PKG-NPM-032).
- Tarballs: if a version's `dist.tarball` equals the conventional URL
  (`<registry>/<name>/-/<name>-<version>.tgz`, scoped `<registry>/@scope%2fname/-/name-<version>.tgz`
  or with an unencoded slash), Yarn stores only the version in its lockfile and later fetches from
  whatever registry is configured. Any other tarball URL is recorded verbatim in the lockfile,
  which pins the lockfile to that URL. Registries should therefore serve conventional tarball URLs.
- Publishing: `yarn npm publish` from the package folder; login and dist-tags: `yarn npm login`,
  `yarn npm tag add|remove|list`.
- `yarn npm login` against a private registry uses the legacy user-document `PUT` unless
  `--web-login` is given, and handles a 409 by re-reading the user document and repeating the
  `PUT` at its revision (PKG-NPM-009).

### PKG-NPM-054 · Client setup instructions in the UI
Priority: **MVP**

The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md); npm content: UI-024)
lets the user pick a repository and, for formats with several clients such as npm, a client. It
then generates snippets with the system's URL, the chosen repository and a token filled in: a
newly generated token, or one the user pastes (UI-022). The tabs vary per format; for npm the
setup steps cover:

- configure: the `.npmrc` lines (PKG-NPM-005), the login command, or the Yarn 2+ commands
  (PKG-NPM-053);
- install: the install command;
- publish: the `publishConfig` snippet and publish command.

It is reachable from the user menu and from a repository in the artifact browser. The package
details screen shows install commands too (ART-049 in [../artifacts.md](../artifacts.md)).
Generated snippets must use the keyed credential form (PKG-NPM-005) and a registry URL with a
trailing slash (PKG-NPM-002).

### PKG-NPM-055 · Build metadata through a companion CLI wrapper
Priority: **P1**

A companion command-line tool wraps npm, pnpm and Yarn to resolve and publish through the system
and to record a build metadata record (dependencies with checksums, published artifacts) that is
later uploaded with the build (see [../builds-and-ci.md](../builds-and-ci.md)).

<details>
<summary>Wrapper behavior per client</summary>

| Aspect | npm | pnpm | Yarn |
|---|---|---|---|
| Project configuration | server and repository for resolving, server and repository for deploying, optional "global" scope; stored as a per-project file; repository keys are not validated (a typo shows up as 404/401/403 on first use) | registry and auth come from `.npmrc`, which is mandatory | server and repository for resolving only; publishing is not supported through the wrapper |
| Commands integrated | `install`, `ci`, `publish`, `dist-tag` (through the deploy repository) | `install`, `publish`; everything else passes through without metadata | `install` and other Yarn commands |
| Publish | default: `npm pack`, then a generic upload (PKG-NPM-017), so `prepublish`/`postpublish` scripts do not run and must be renamed `prepack`/`postpack`; with pass-through mode: the native `npm publish` | native `pnpm publish` (with `-r` for workspaces) | not supported; use `npm publish` or `yarn npm publish` |
| Pass-through mode | uses the user's own `.npmrc` instead of a generated temporary one; metadata collection still works | always | Yarn 4+ only; uses `.yarnrc.yml` |
| Collection options | build name and number (both required), project, module, workspaces publish, detailed publish summary, optional scan before upload, fail when selected dependency types (`regular`, `peer`, `optional`, `bundle`, or `all`) cannot be collected (install and `ci` only; by default uncollected dependencies are only logged and left out) | build name and number, project, module, server | build name and number, project, module, server; parallel checksum lookups (default 3); one install at a workspace root collects all members into one module |
| Not collected | — | git dependencies, `optionalDependencies`, local `pnpm patch` changes | `workspace:`, `link:`, `file:`, `portal:`, `patch:` and git dependencies |
| Supported versions | npm 5.4.0 or later | pnpm 10 and 11, Node.js 20 or later | Yarn 2.4.0 or later, up to 4.x; Yarn 1 not supported |

</details>

Server operations the wrapper relies on:

| Operation | Input | Output | Privilege |
|---|---|---|---|
| npm client credentials | the caller's credentials | client configuration lines for npm (used in password-based flows when a token cannot be configured directly); exact content unspecified | authenticated user |
| Checksum lookup for dependencies | a dependency resolved during the install | checksums of the stored tarball; which algorithms is unspecified | read |
| Build upload | build metadata record, optionally enriched beforehand with the CI environment variables and the VCS commit of the working copy | stored build | a token with deploy permission |

## Protocol reference

This section summarizes, from npm's own documentation and the client sources, every endpoint an
npm-compatible registry serves. Paths are relative to the registry URL of one repository
(PKG-NPM-002), except the two rows marked **host root**, which npm resolves against the host
alone. `{name}` is the package name; for scoped packages npm and pnpm send `@scope%2fname`
and the registry must accept `@scope/name` as well (PKG-NPM-003). Sources: npm registry
documentation ([REGISTRY-API.md](https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md),
[package-metadata.md](https://github.com/npm/registry/blob/main/docs/responses/package-metadata.md),
[authentication.md](https://github.com/npm/registry/blob/main/docs/user/authentication.md),
[restful-api-conventions.md](https://github.com/npm/registry/blob/main/docs/restful-api-conventions.md)),
[npm CLI documentation](https://docs.npmjs.com/cli/), and the sources of
[npm CLI](https://github.com/npm/cli), [npm-profile](https://github.com/npm/npm-profile),
[npm-registry-fetch](https://github.com/npm/npm-registry-fetch),
[pacote](https://github.com/npm/pacote), [pnpm](https://github.com/pnpm/pnpm),
[Yarn 2+](https://github.com/yarnpkg/berry) and [Yarn 1](https://github.com/yarnpkg/yarn).

### PKG-NPM-056 · Read endpoints
Priority: **MVP**

<details>
<summary>Read endpoints</summary>

| Method | Path | Purpose | Request | Response | Used by |
|---|---|---|---|---|---|
| GET | `{name}` | Package document | `Accept` selects abbreviated (`application/vnd.npm.install-v1+json`) or full (`application/json`, or no header) form; npm adds `?write=true` before a read-modify-write | 200 with the document and matching `Content-Type`; 404 if unknown | every client |
| GET | `{name}/{version}` | One version's manifest; `{version}` may be `latest` | — | 200 manifest | tooling |
| GET | `{name}/-/{basename}-{version}.tgz` | Tarball; `{basename}` is the name without scope (`@scope/pkg/-/pkg-1.0.0.tgz`); Yarn 2+ requests `@scope%2fpkg/-/pkg-1.0.0.tgz` first | — | 200 `application/octet-stream` | every client |
| GET | `-/package/{name}/dist-tags` | Dist-tags of a package | — | 200 `{"latest":"1.2.0","beta":"2.0.0-rc.1"}` | `npm dist-tag ls`, and before every dist-tag change |
| GET | `-/v1/search` | Search | `text`, `size` (npm docs: default 20, max 250), `from`, `quality`, `popularity`, `maintenance` | 200 `{"objects":[{"package":{name, version, description, keywords, date, links, publisher, maintainers},"score":{…},"searchScore":n}],"total":n,"time":"…"}` | `npm search`, `pnpm search` |
| GET | `-/ping` | Liveness and credential check; current npm sends no query, older clients append `?write=true`, which the registry should accept and ignore | — | 200 with a JSON body, `{}` suffices | `npm ping`, `npm doctor` |
| GET | `/-/npm/v1/attestations/{name}@{version}` (**host root**) | Provenance and publish attestations of one version; npm takes only the path of `dist.attestations.url` and resolves it against the registry's host | — | 200 `{"attestations":[{"predicateType":"…","bundle":{…}}]}` | `npm audit signatures` (PKG-NPM-024) |
| GET | `-/whoami` | User behind the credential | `Authorization` | 200 `{"username":"alice"}`; 401 without a valid credential | `npm whoami`, `pnpm whoami`, `yarn npm whoami` |
| GET | `-/npm/v1/keys` | Public signing keys | — | 200 `{"keys":[{"expires":null,"keyid":"SHA256:…","keytype":"ecdsa-sha2-nistp256","scheme":"ecdsa-sha2-nistp256","key":"<base64>"}]}` | `npm audit signatures` |
| GET | (registry root) | Registry information document of the CouchDB era | — | 200 JSON | listed in the registry API documentation |
| GET | `-/all` | Legacy full listing | — | not used by current clients | listed in the registry API documentation |

</details>

### PKG-NPM-057 · Write endpoints
Priority: **MVP**

<details>
<summary>Write endpoints</summary>

| Method | Path | Purpose | Request body | Response | Used by |
|---|---|---|---|---|---|
| PUT | `{name}` | Publish one version (PKG-NPM-015) | `_id`, `name`, `description`, `dist-tags` (one tag), `versions` (one manifest with `dist.integrity`, `dist.shasum`, `dist.tarball`), `access` (`null`, `public`, `restricted`), `_attachments` (`{"<name>-<version>.tgz":{"content_type":"application/octet-stream","data":"<base64>","length":n}}`, optionally `<name>-<version>.sigstore`) | 2xx (body ignored); 4xx with `error`/`message` | npm, pnpm, Yarn 1 and 2+ publish |
| PUT | `{name}` | Deprecate (PKG-NPM-020) | full document read with `?write=true`, `versions[v].deprecated` set or emptied, `_attachments` empty or absent | 2xx | `npm deprecate` |
| PUT | `{name}/-rev/{rev}` | Remove one version (PKG-NPM-019) | full document without the version, without `_attachments` and `_revisions`, dist-tags adjusted | 2xx | `npm unpublish name@version`, `pnpm unpublish` |
| PUT | `{name}/-rev/{rev}` | Change maintainers (PKG-NPM-022) | **partial**: `_id`, `_rev`, `maintainers` only | 2xx | `npm owner add/rm` |
| DELETE | `{name}/-rev/{rev}` | Remove the whole package | — | 2xx | `npm unpublish name` |
| DELETE | `{tarball path}/-rev/{rev}` | Remove an unpublished version's tarball; path is `dist.tarball` relative to the registry URL, rev re-read after the document update | — | 2xx | `npm unpublish name@version` |
| PUT | `-/package/{name}/dist-tags/{tag}` | Set a dist-tag | JSON string, e.g. `"1.2.0"`, `Content-Type: application/json` | 2xx | `npm dist-tag add`, `pnpm dist-tag add`, `yarn npm tag add` |
| DELETE | `-/package/{name}/dist-tags/{tag}` | Remove a dist-tag | — | 2xx | `npm dist-tag rm`, `yarn npm tag remove` |
| POST | `-/stage/package/{name}` | Staged publish (PKG-NPM-023) | as publish | 2xx JSON with `stageId` | `npm publish --stage` |

</details>

The status for publishing an existing version is not fixed by npm's documentation; any 4xx with a
message works for the clients.

### PKG-NPM-058 · Authentication endpoints
Priority: **MVP**

The entry is MVP for `whoami` and `ping` (listed in PKG-NPM-056); the rows below carry the
priority of the feature entry they serve.

<details>
<summary>Authentication endpoints</summary>

| Method | Path | Purpose | Request | Response | Feature |
|---|---|---|---|---|---|
| POST | `-/v1/login` | Start web login | empty JSON object `{}`; Yarn sends `npm-auth-type: web` and no credentials | 200 `{"loginUrl":"https://…","doneUrl":"https://…"}`, both absolute `http`/`https` URLs or npm rejects the answer; any 4xx/500 makes npm fall back to legacy login | PKG-NPM-008 |
| GET | `<doneUrl>` | Poll web login | — | 202 with `Retry-After: <seconds>` while pending; 200 `{"token":"…"}` when done | PKG-NPM-008 |
| PUT | `-/user/org.couchdb.user:{username}` | Legacy login | `{"_id":"org.couchdb.user:<u>","name":"<u>","password":"<p>","type":"user","roles":[],"date":"<ISO>"}` (pnpm adds `email` and a Basic header); optional `npm-otp` | 201 `{"ok":true,"token":"…"}`; 401 with `{"ok":false}`-style body on bad credentials; 409 means "exists": npm and Yarn 2+ re-read the user document and retry at `…/-rev/{rev}` with Basic credentials | PKG-NPM-009 |
| GET | `-/user/org.couchdb.user:{username}` | User lookup | — | 200 with at least `name` and `email` | PKG-NPM-009, PKG-NPM-022 |
| DELETE | `-/user/token/{token}` | Invalidate the presented token | — | 2xx | PKG-NPM-010 |
| GET, POST | `-/npm/v1/tokens` | List (`perPage`, `page`) and create tokens | create: `password`, `readonly`, `cidr_whitelist` | list: `{"objects":[…],"total":n,"urls":{"next":…}}`; create: token object, secret shown once | PKG-NPM-014 |
| DELETE | `-/npm/v1/tokens/token/{key}` | Revoke a token by its key | — | 204 | PKG-NPM-014 |
| GET, POST | `-/npm/v1/user` | Read or update the profile | partial profile | profile | PKG-NPM-014 |
| POST | `/-/npm/v1/oidc/token/exchange/package/{name}` (**host root**) | Exchange a CI OIDC ID token for a package-bound token | `Authorization: Bearer <ID token>`; ID token audience `npm:<registry host>` on GitHub Actions | 2xx `{"token":"…"}`; failures are ignored by the client | PKG-NPM-013 |

</details>

### PKG-NPM-059 · Security audit endpoints
Priority: **P1**

| Method | Path | Request | Response | Used by |
|---|---|---|---|---|
| POST | `-/npm/v1/security/advisories/bulk` | JSON object mapping each package name to the installed versions, e.g. `{"minimist":["0.0.8","1.2.0"]}`; npm sends it gzip-compressed (`Content-Encoding: gzip`), pnpm uncompressed | 200 JSON mapping package name to advisories: `id` (number), `url`, `title`, `severity` (`info`, `low`, `moderate`, `high`, `critical`), `vulnerable_versions` (SemVer range), optionally `cwe`, `cvss`; packages without advisories are omitted | npm 7+, pnpm, `yarn npm audit` |
| POST | `-/npm/v1/security/audits/quick` | Full dependency tree from the lockfile plus `npm_version`, `node_version`, `platform`, `arch`, `node_env` | audit report | npm 6 during install; npm's documentation names it as the fallback when the bulk endpoint fails, while the current audit code in the npm sources only logs the bulk failure |
| POST | `-/npm/v1/security/audits` | as the quick audit | full audit report | npm 6 `npm audit` |

Sources: [npm audit](https://docs.npmjs.com/cli/commands/npm-audit),
[arborist audit report](https://github.com/npm/cli/blob/latest/workspaces/arborist/lib/audit-report.js),
[npm 6 audit](https://github.com/npm/cli/blob/v6.14.18/lib/install/audit.js).

### PKG-NPM-060 · Headers and error bodies
Priority: **MVP**

| Header | Direction | Meaning |
|---|---|---|
| `Authorization: Bearer <token>` | request | from `_authToken` / `npmAuthToken` |
| `Authorization: Basic <base64>` | request | from `_auth`, `username` + `_password`, or `npmAuthIdent` |
| `Accept` | request | abbreviated vs full package document |
| `npm-otp` | request | one-time password (PKG-NPM-012) |
| `npm-auth-type` | request | `web` or `legacy`: which login and second-factor flows the client can run |
| `npm-command` | request | the npm command being run, e.g. `install`, `publish` |
| `npm-scope`, `npm-session`, `npm-in-ci` | request | project scope, per-run session ID, CI detection; informational, not sent by every version |
| `If-None-Match`, `If-Modified-Since` | request | conditional revalidation (Yarn 2+) |
| `Content-Type: application/vnd.npm.install-v1+json` | response | marks an abbreviated document |
| `ETag`, `Last-Modified` | response | enable revalidation |
| `WWW-Authenticate` | response | on 401: a comma-separated list; the element `otp` triggers the OTP prompt, `ipaddress` reports an IP restriction, anything else is a plain authentication failure |
| `npm-notice` | response | free text that npm prints to the user (Yarn prints it on OTP prompts) |
| `Retry-After` | response | poll interval for the web-login done URL (with 202) |

Error bodies: JSON with `message` and/or `error` (clients read `message` first, then `error`);
legacy endpoints may add `"ok": false`. The HTTP status is authoritative.

### PKG-NPM-061 · Public-registry-only endpoints
Priority: **Out of scope**

The public registry's account-model endpoints: organizations and teams (`-/org/…`, `-/team/…`),
package access level, visibility and collaborators (`-/package/{name}/access`,
`-/package/{name}/visibility`, `-/package/{name}/collaborators`, `-/user/{user}/package`), hooks
(`-/npm/v1/hooks/…`) and stars. A private registry answers them with 404. Note: `npm publish
--provenance` queries the visibility endpoint and treats 404 as "not public", so provenance
publishing to a private registry needs `--access public` on the client.

## Notes for implementers

- `dist.tarball` must lie under the serving repository's registry URL (same host, same path
  prefix) and should have the conventional shape `<name>/-/<basename>-<version>.tgz`. Otherwise
  npm and Yarn 1 fetch tarballs without credentials, and Yarn 2+ pins the URL into lockfiles.
- Accept `@scope%2fname` and `@scope/name` on every name-bearing path, and make sure the web server
  and any reverse proxy pass `%2f` through undecoded.
- Serve the abbreviated document only when asked, label it
  `application/vnd.npm.install-v1+json`, and keep `Vary: Accept`; serve a full document with a
  correct per-version `time` map, because release-age policies in npm, pnpm and Yarn request it.
- `PUT {name}` is overloaded: with a tarball in `_attachments` it publishes exactly one new
  version; with `_attachments` empty or absent it replaces the document's mutable parts
  (deprecation), because `npm deprecate` sends back the document as it read it. `PUT {name}/-rev/{rev}` with only
  `maintainers` is a partial update, not a replacement.
- Every document read with `?write=true` needs a `_rev`, and the `-rev/{rev}` paths must accept it;
  rejecting a stale revision prevents two concurrent read-modify-write clients from losing each
  other's changes.
- Never accept a second publish of an existing name and version, not even after unpublish:
  lockfiles pin `integrity`.
- npm writes an `http://` URL into `dist.tarball` when publishing; always replace it with the
  canonical URL instead of storing the client's value.
- Answer unsupported optional endpoints (web login, audit, token management, account-model
  endpoints) with a fast 404: npm falls back to legacy login on 4xx/500, warns and continues on a
  failed audit, and reports "not supported" for the rest.
- Never put the element `otp` into `WWW-Authenticate` unless the second-factor flow exists; npm and
  pnpm switch to an OTP prompt when they see it.
- Merged virtual documents must be consistent per version: manifest, `dist.integrity`, `time` and
  the tarball served for that version must all come from the same member.
- Two npm endpoints ignore the registry path: the CI token exchange and the attestation download
  are resolved against the host root. With per-repository URL prefixes they need a host-level
  route that maps the request to a repository.
