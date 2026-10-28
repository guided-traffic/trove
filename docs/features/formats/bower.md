# Bower package format

This page catalogs what a repository manager does for Bower, the front-end package manager for
HTML, CSS and JavaScript components: acting as a Bower registry, hosting packages in local
repositories, proxying the public Bower registry and the Git hosts its entries point to in remote
repositories, aggregating both in virtual repositories with optional rewriting of external
dependencies, and the client-side resolver plugin the setup depends on. Bower itself is no longer
developed by its maintainers; the format matters for legacy front-end builds. The registry protocol
is the one of the [Bower registry](https://github.com/bower/registry) and its
[registry client](https://github.com/bower/bower/tree/master/packages/bower-registry-client);
entries whose behavior follows from them say so.
Repository classes in general are described in [../repositories.md](../repositories.md),
pull-through caching in [../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/bower/<repo-key>` stands for the registry URL of one
repository and `https://repo.example.com/files/<repo-key>/<path>` for raw file access to it (both
path layouts are the implementation's choice), `bower-local`, `bower-remote` and `bower-virtual`
are example repository keys, and `${TOKEN}` is an access token.

## Repositories and addressing

### PKG-BOWER-001 · Bower repositories in all three repository classes
Priority: **P2**

Bower is supported as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold internal packages; the system calculates a Bower index for them
  (PKG-BOWER-009, PKG-BOWER-010);
- remote repositories proxy the public Bower registry, which maps names to Git repositories, and
  Git hosts such as GitHub or Bitbucket (PKG-BOWER-011);
- virtual repositories aggregate local and remote members and can rewrite external dependencies
  (PKG-BOWER-013, PKG-BOWER-014);
- the native `bower` command resolves, with a resolver plugin (PKG-BOWER-006);
- user and group permissions apply per repository, so access can follow projects or teams (IAM-019
  and IAM-020 in [../identity-and-access.md](../identity-and-access.md)).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, then the class and the Bower format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)). Scanner indexing (REPO-021) is supported for
Bower local and remote repositories. Federation: Bower is a federated format; federation admits
every format except the Terraform state backend (REPL-026 in
[../replication-and-federation.md](../replication-and-federation.md)).

### PKG-BOWER-002 · Registry URL of a repository
Priority: **P2**

Every Bower repository answers the registry protocol under its *registry URL*, which the client
stores as `registry` in `.bowerrc`. Every Bower command, including `bower install` and `bower
info`, must use this package-protocol URL; the raw file URL of the repository does not work as a
registry.

The authenticated setup embeds the credentials in the registry URL:
`https://<user>:${TOKEN}@repo.example.com/bower/bower-virtual` (the token is used as Basic
password, TOK-034 in [../access-tokens.md](../access-tokens.md)); no other way to authenticate is
specified. Anonymous access follows the
global anonymous-access setting (AUTHN-036 in
[../authentication-providers.md](../authentication-providers.md)).

## Registry protocol

### PKG-BOWER-003 · Package lookup
Priority: **P2**

`GET <registry-url>/packages/<name>` (name URL-encoded) returns `{"name": "<name>", "url":
"<endpoint>"}` (Bower registry client, `lookup.js`).

- 404 means "not in this registry"; the client then asks the next configured registry.
- Any other non-2xx status or a body that is not a JSON object aborts the lookup.
- The public registry answers with a Git URL (`git://github.com/jquery/jquery.git`). For packages
  the system serves, the returned endpoint uses the resolver plugin's scheme so that the plugin
  takes over (PKG-BOWER-006); the exact form is the implementation's choice.

### PKG-BOWER-004 · Package list and search
Priority: **P2**

| Request | Returns |
|---|---|
| `GET <registry-url>/packages` | All packages as an array of name and URL objects (`bower search` without a query) |
| `GET <registry-url>/packages/search/<query>` | Packages whose names match the query (`bower search`) |

Source: Bower registry client (`list.js`, `search.js`). The client caches search results. Whether
the system serves both requests is unspecified.

### PKG-BOWER-005 · Register and unregister packages
Priority: **P2**

| Request | Body | Success | Failures |
|---|---|---|---|
| `POST <registry-url>/packages` | Form fields `name` and `url` (Git URL) | `201` | `403` duplicate name, `400` invalid URL format |
| `DELETE <registry-url>/packages/<name>` | none | `204` | `403` forbidden |

Source: Bower registry client (`register.js`, `unregister.js`). The client appends a configured
access token as `access_token` query parameter. The system supports registration through remote and
virtual repositories (PKG-BOWER-012); unregistering is unspecified.

## Client setup

### PKG-BOWER-006 · Resolver plugin and client configuration
Priority: **P2**

Bower 1.5 and later supports pluggable resolvers. The system's Bower support relies on a resolver
plugin, installed from npm and listed in `.bowerrc`:

```json
{
  "registry": "https://<user>:${TOKEN}@repo.example.com/bower/bower-virtual",
  "resolvers": ["<resolver-plugin-package>"]
}
```

`.bowerrc` lives at `~/.bowerrc` (Linux) or `%userprofile%\.bowerrc` (Windows).

- The plugin claims every endpoint that starts with its own URL scheme (PKG-BOWER-003) and resolves
  it against the first configured registry.
- What the plugin asks the registry, as observed in its published source: a version listing that
  returns the source repository's tags as `<hash> refs/tags/<tag>` lines (in the style of
  `git ls-remote`), from which semver-valid tags become versions (with a fallback to the `master`
  branch as version `0.0.0` when no tag exists), and a download of a tarball for a given tag that
  the plugin extracts. The plugin's line pattern accepts only a hash of exactly 44 lowercase
  hexadecimal characters followed by whitespace, so a listing with plain 40-character Git SHA-1
  values yields no versions; peeled tag lines ending in `^{}` are skipped. The plugin always
  queries the first configured registry.
- A compatible implementation has to ship its own resolver plugin and define those two calls; the
  request paths are the implementation's choice.
- The client setup instructions (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md))
  render `.bowerrc` with the user's token filled in. A virtual repository is the recommended
  registry.

### PKG-BOWER-007 · Shorthand resolver for GitHub dependencies
Priority: **P2**

Bower's shorthand resolver turns `owner/package` dependencies into GitHub URLs
([Bower configuration spec](https://github.com/bower/spec/blob/master/config.md#shorthand-resolver)).

- For dependencies of packages downloaded from remote repositories, the system resolves shorthand
  dependencies hosted on GitHub. The client output marks such dependencies with a reserved prefix
  in the version target.
- For a `bower.json` on the local machine, the user sets a `shorthand-resolver` template in
  `.bowerrc` that maps `{{owner}}/{{package}}` to the resolver plugin's scheme.

### PKG-BOWER-008 · Clients below Bower 1.5
Priority: **Later**

Older clients without pluggable resolvers needed a patched Bower command-line client published
for this purpose, run in place of `bower`, plus the resolver plugin as peer dependency. Supported
only by old releases of the system; updating required uninstalling the patched client first
because npm before version 7 does not update peer dependencies. A new implementation needs no
equivalent; the entry exists so the inventory stays complete.

## Local repositories

### PKG-BOWER-009 · Deploy packages
Priority: **P2**

The Bower client has no publish command. Packages are deployed as plain files through the UI deploy
dialog or the deploy operation (ART-002 and ART-006 in [../artifacts.md](../artifacts.md)):

```shell
curl -u "user:${TOKEN}" -X PUT -T jquery.min.js \
  "https://repo.example.com/files/bower-local/jquery/1.9.1/jquery.min.js"
```

- `bower.json` must carry a `version` property, maintained by hand or with `bower version`.
- The example above uploads a single JavaScript file under `<name>/<version>/`; which
  files the index calculation expects (an archive with `bower.json`, or a folder per version) is
  unspecified.
- Required privilege: deploy on the target repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).

### PKG-BOWER-010 · Recalculate the Bower index
Priority: **P2**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Recalculate Bower index | Repository key | Plain-text acknowledgement; 401, 403 or 500 on failure | Unspecified; a caller without the needed permission gets 403 |

The repository-level recalculate action is described in REPO-009 in
[../repositories.md](../repositories.md). Whether the index also updates automatically after
deployments is unspecified.

## Remote repositories

### PKG-BOWER-011 · Remote Bower repository settings
Priority: **P2**

The public registry `https://registry.bower.io/` is a key-value store from package names to Git
repositories; the packages themselves live on Git hosts. A remote Bower repository therefore pairs a
Git host with the registry.

<details>
<summary>Format-specific remote settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | The remote's repository URL, to be verified when the remote is set up. Pairing it with a Git host such as `https://github.com` follows from the registry being a name-to-Git map (inferred) | unspecified |
| Git provider | Which provider API to use: GitHub, Bitbucket, Bitbucket Server ("Stash"), Bitbucket Server before 5.1.0, another instance of the system (chained remote, RMT-040 in [../remote-repositories.md](../remote-repositories.md)), custom; open whether GitHub Enterprise is a further provider value | GitHub |
| Registry URL | Base URL of the Bower registry API the remote consults | `https://registry.bower.io` |

</details>

- Additional remotes can proxy further Git hosts such as GitHub or Bitbucket.
- UI location: remote repository settings, format-specific section.
- How the remote turns a tag of the Git repository into a cached tarball is unspecified.

### PKG-BOWER-012 · Registering private Git repositories
Priority: **P2**

The system acts as a Bower registry for registrations sent to remote and virtual repositories, so
packages can come straight from private Git servers:

```shell
bower register internal-widgets ssh://git@git.example.com:7999/web/internal-widgets.git
bower install internal-widgets
```

- The package is then downloaded from the Git server and cached in the remote repository.
- For a remote that serves only registered private repositories, the registry URL setting is
  redundant and can stay at its default.
- Where registrations are stored and who may register (required privilege) is unspecified.

## Virtual repositories

### PKG-BOWER-013 · Virtual Bower repositories
Priority: **P2**

A virtual repository aggregates local and remote Bower repositories under one registry URL
(REPO-033 in [../repositories.md](../repositories.md)); lookups resolve in member order
(REPO-035). Using a virtual repository as the client's registry lets members change without client
changes.

### PKG-BOWER-014 · External dependency rewrite
Priority: **P2**

A package's `bower.json` can name dependencies on external hosts, which can pull unknown and
possibly malicious transitive code. A virtual Bower repository can rewrite such dependencies so
clients fetch them through the system.

| Setting | Meaning | Default |
|---|---|---|
| Rewrite external dependencies | Turns the rewrite on | off |
| Remote repository for external dependencies | Member remote repository that caches fetched external dependencies; a dedicated remote is recommended | empty |
| External dependency allow list | Ant-style patterns of sources dependencies may be fetched from; replace the default with, for example, `**/github.com/**` to restrict to one host | `**` (any source) |

Workflow when a client downloads a package through the virtual repository:

1. The system analyzes the package's dependency list.
2. For each dependency on an external host that matches the allow list, it downloads the
   dependency and caches it in the configured remote repository.
3. Before handing the package to the client, it changes that dependency in `bower.json` so it
   points at the cached copy in the remote repository.
4. Later requests for the dependency are served from that location.

Shared settings: REPO-040 in [../repositories.md](../repositories.md). The exact rewritten form of an
entry is unspecified.

## Notes for implementers

- Without the resolver plugin, a lookup result in the plugin's scheme matches no built-in resolver
  and resolution fails, while a lookup result with a plain Git URL makes the client download from
  the Git host directly; the plugin and the lookup format must be designed together.
- A 404 on lookup must stay a plain 404; anything else stops the client from trying the next
  registry in `.bowerrc`.
- Credentials travel in the registry URL and therefore end up in `.bowerrc` files, which are easily
  committed by mistake; keep these tokens read-only and short-lived.
- The resolver treats only semver-valid tags as versions; non-semver tags are offered as raw
  targets, and a package without tags resolves to a branch literally named `master`, whatever the
  repository's default branch is.
- Compatibility with the published resolver requires the 44-hex-character hash form in the
  version listing (PKG-BOWER-006); an implementation that ships its own resolver is free to use
  real Git hashes instead.
- Rewriting `bower.json` changes the downloaded archive; checksums of rewritten packages differ from
  the upstream ones, and the rewrite must be deterministic so repeated downloads stay identical.
- The allow list is matched against the external dependency's URL as a path pattern; the default
  `**` allows everything, so turning on the rewrite without narrowing the list adds caching but no
  protection.
