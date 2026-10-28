# PHP Composer format

This page catalogs what a repository manager does to act as a Composer repository for PHP
packages: hosting package archives in local repositories and computing their metadata, caching
Packagist or another Composer registry in remote repositories (including downloads from
version-control hosts and Drupal's infrastructure), combining both in virtual repositories, client
configuration and authentication, and deployment. The repository protocol is defined by the
Composer project in [Repositories](https://getcomposer.org/doc/05-repositories.md); where an entry
states protocol behavior, it links that source. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/composer/<repo-key>` stands for the Composer
protocol URL of one repository (the path layout is the implementation's choice),
`https://repo.example.com/files/<repo-key>/<path>` for raw file access, and `composer-local`,
`composer-remote` and `composer-virtual` are example repository keys.

## Repositories and addressing

### PKG-COMPOSER-001 · Composer repositories in all three repository classes
Priority: **P2**

PHP Composer is supported as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)).

| Class | Composer client versions |
|---|---|
| Local | Composer 2; Composer 1 remains usable with local repositories through December 2026 |
| Remote | Composer 2 only; the proxied registry must support Composer 2 as well |
| Virtual | Composer 2 only |

- Packagist.org, the main public registry, stopped supporting Composer 1 on 1 September 2025.
- Packagist hosts no package archives, only metadata that points at the source-control repository
  of each package; a remote repository therefore needs both a metadata address and a download
  source (PKG-COMPOSER-007).
- Creating a repository requires the system administrator or a project administrator role. UI
  location: administration, repositories, create repository, then class and Composer format
  (UI-031 in [../ui-inventory.md](../ui-inventory.md)).

Interactions: the local setting that chooses the Composer 1 or Composer 2 index form is REPO-032
in [../repositories.md](../repositories.md) (default: Composer 2 index).

### PKG-COMPOSER-002 · Composer protocol endpoint of a repository
Priority: **P2**

Clients must address a repository through its Composer protocol URL, not its raw file URL. Under
that URL the repository serves a Composer repository as defined by the Composer project
([composer repository type](https://getcomposer.org/doc/05-repositories.md#composer)):

| Path or field | Meaning in the Composer protocol |
|---|---|
| `packages.json` | Root document; the repository URL is the path before it. Its `packages` field (required, may be empty) maps package names to versions to package definitions with at least `name`, `version` and `dist` or `source` |
| `metadata-url` field, for example `/p2/%package%.json` | Composer 2 per-package metadata. Composer fetches `<name>.json` for tagged versions and `<name>~dev.json` for development versions; each file holds only versions of that one package as `{"packages": {"<name>": [ … ]}}`, optionally minified (then marked with `"minified": "composer/2.0"`) |
| `available-packages`, `available-package-patterns` fields | Optional list of package names or `*` patterns the repository holds, so Composer skips lookups it would answer with `404` |
| `providers-api`, `list` fields | Optional: packages that provide a virtual package name; package-name listing with `?filter=` |
| `provider-includes`, `providers-url` fields | Composer 1 metadata form with SHA-256-addressed provider files; used when `metadata-url` is absent |
| `notify-batch` field | Optional URL Composer posts install notifications to |
| `filter` field | Optional: announces filter lists (for example malware or typosquatting lists) that the per-package metadata carries, with an optional summary or query URL |

Protocol rules a compatible server follows:

- An unknown package answers `404`, quickly and without redirect to an error page.
- Per-package files are revalidated with `If-Modified-Since`; `Last-Modified` must be accurate.
- A server that throttles answers `429` with `Retry-After`; Composer waits and retries up to three
  times for intervals of 60 seconds or less, and shows an optional JSON `warning` message.
- Composer's cooldown policy (hold back versions published too recently) reads the repository's
  `published-time` of each version. A repository is expected to provide it unless the client marks
  the repository with `"require-published-time": false`; with a cooldown active and the value
  missing, Composer stops with an error ([require-published-time](https://getcomposer.org/doc/05-repositories.md#require-published-time)).

Which optional fields the system emits is unspecified.

## Client setup and authentication

### PKG-COMPOSER-003 · Composer client configuration
Priority: **P2**

Repository definition, in the project's `composer.json` or the global Composer configuration
(`config.json` in the Composer home directory):

```json
{
  "repositories": [
    {"type": "composer", "url": "https://repo.example.com/composer/composer-virtual"},
    {"packagist.org": false}
  ]
}
```

- Disabling Packagist.org makes the repository the only source. The older key spelling
  `"packagist": false` also appears in client setups; the Composer documentation shows
  `"packagist.org": false` and the global form `composer config -g repo.packagist.org false`.
- For a repository reached over plain HTTP, Composer must be told to allow it:
  `"config": {"secure-http": false}`.
- Credentials go into `auth.json` (project directory or Composer home), keyed by host:

```json
{
  "http-basic": {
    "repo.example.com": {"username": "<user>", "password": "<token>"}
  }
}
```

- The password is the user's password or, recommended, a token (TOK-005 and TOK-034 in
  [../access-tokens.md](../access-tokens.md)). With anonymous access enabled, no credentials are
  needed.
- The client setup dialog generates both snippets with the user's token (UI-021, UI-026 in
  [../ui-inventory.md](../ui-inventory.md)).

Security: `secure-http: false` sends the Basic credentials in clear text; it belongs only in
test setups.

### PKG-COMPOSER-004 · Install packages
Priority: **P2**

`composer install --prefer-dist` resolves through the configured repository and downloads package
archives (the `dist` form) from it. `--prefer-dist` is the recommended mode.

Edge cases: with `--prefer-source`, Composer clones from the source-control URL in the metadata
instead of downloading through the system. On a remote repository under upstream curation this
bypasses the curation block and is therefore not supported (PKG-COMPOSER-011).

## Local repositories

### PKG-COMPOSER-005 · Deploy a package archive
Priority: **P2**

The Composer client has no publish command. A package is deployed by uploading its archive (a zip
with `composer.json` at the root) with the generic deploy operation (ART-002 in
[../artifacts.md](../artifacts.md)) or the UI deploy dialog (UI-013 in
[../ui-inventory.md](../ui-inventory.md)):

```shell
curl -u "<user>:<token>" -T acme-logger-1.2.0.zip \
  "https://repo.example.com/files/composer-local/acme/logger/acme-logger-1.2.0.zip"
```

| Input | Meaning | Default |
|---|---|---|
| Target path | Destination path of the archive inside the repository, for example `<vendor>/<package>/<file>.zip` | none (required) |
| Version | Taken from the `version` attribute of the archive's `composer.json`. When that attribute is missing, the version is supplied as a deploy-time property on the upload (ART-003 in [../artifacts.md](../artifacts.md)); the property name is format-specific | from `composer.json` |

Required privilege: deploy permission on the target repository.

Edge cases: whether the path must follow a fixed layout, and what happens when an archive has
neither a `version` attribute nor the version property, are unspecified. A `composer.json` without
`version` is normal for packages published from version control, so the deploy-time property is
the usual case for internally built archives.

### PKG-COMPOSER-006 · Metadata calculation for local repositories
Priority: **P2**

The system computes Composer metadata (`packages.json` and the per-package files of
PKG-COMPOSER-002) from the archives deployed to a local repository, using the name and version from
each archive's `composer.json` or the deploy-time version property.

Edge cases: when the calculation runs, whether it can be triggered manually, and whether `dist`
entries carry a checksum are unspecified. REPO-009 in [../repositories.md](../repositories.md)
describes the generic "recalculate index" action; whether it is offered for Composer is
unspecified.

## Remote repositories

### PKG-COMPOSER-007 · Remote Composer repository: metadata and download addresses
Priority: **P2**

A Composer remote repository proxies a registry's metadata and the package archives the metadata
points to. It has two addresses plus Composer-specific settings:

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | Where package archives are downloaded from (RMT-004 in [../remote-repositories.md](../remote-repositories.md)) | pre-filled, value unspecified |
| Registry URL | Where the Composer metadata (`packages.json`) is read from | `https://packagist.org` |
| Download provider ("Git providers") | Which source-control host family serves the archives (PKG-COMPOSER-008) | unspecified |
| Download URL template | Only for a custom provider (PKG-COMPOSER-008) | empty |

UI location: remote repository form, basic tab; provider and template in the Composer settings
section.

- The system validates the metadata it receives from the remote registry; what is validated is
  unspecified.
- Supported upstreams: standard registries such as Packagist and Drupal's package registry
  (PKG-COMPOSER-009).
- Clients see `dist` URLs that lead back through the system; how the system rewrites upstream
  `dist` URLs (for Packagist they point at source-control archive downloads) is unspecified.
- A remote needs Composer 2 metadata from the upstream (PKG-COMPOSER-001).

Interactions: cached archives follow the general remote caching rules (RMT-001, RMT-018, RMT-020).

### PKG-COMPOSER-008 · Download providers and custom download URL template
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Download provider | Host family whose archive download API the remote uses for `dist` downloads. Values: GitHub; Bitbucket (cloud); Bitbucket Server (formerly Stash, self-hosted); Bitbucket Server before 5.1.0; another instance of the system (chained remote, RMT-040 in [../remote-repositories.md](../remote-repositories.md)); *custom* | unspecified |
| Download URL template | For the *custom* provider: URL with positional placeholders `{0}` organization, `{1}` package name, `{2}` commit identifier, `{3}` archive format | empty |

Example for Drupal's GitLab instance: `https://git.drupalcode.org/{0}/{1}/-/archive/{2}.{3}`. The
placeholder numbers are fixed and must not be renumbered. The same provider list and template
mechanism exist for version-control archive repositories (PKG-VCS-002 and PKG-VCS-003 in
[vcs.md](vcs.md)); there `{2}` is a branch or tag, here a commit identifier.

Open: custom providers are either supported (*custom* is a provider value, and the Drupal setup
in PKG-COMPOSER-009 selects it with a template) or not supported; the spec decides.

### PKG-COMPOSER-009 · Drupal upstreams
Priority: **P2**

Remote repositories can serve Drupal 7 and 8 packages through Composer 2.

| Setting | Value |
|---|---|
| Upstream URL | `https://ftp.drupal.org/` |
| Registry URL | `https://packages.drupal.org/` |

Alternatively, archives can be fetched directly from `git.drupalcode.org` with the custom download
provider and the template from PKG-COMPOSER-008.

UI location: administration, repositories, remote repository, basic tab.

## Virtual repositories

### PKG-COMPOSER-010 · Virtual Composer repositories
Priority: **P2**

A virtual Composer repository aggregates local and remote members under one Composer URL (REPO-033
in [../repositories.md](../repositories.md)); it requires Composer 2. Deployment through it goes to
the default deployment repository (REPO-038).

Edge cases: how `packages.json` and per-package files are merged when several members hold the same
package, and whether priority resolution applies, are unspecified. The virtual setting that lets
requests from other instances pull uncached content from remote members (REPO-039) does not exist
for Composer; another instance reading through a Composer virtual repository gets cached content
only. Composer itself, by default,
stops at the first repository that has a package; a virtual repository changes that to whatever
merge rule the system applies.

## Upstream curation

### PKG-COMPOSER-011 · Source installs under upstream curation
Priority: **P2**

When a Composer remote repository is subject to upstream curation (package blocking by policy,
SEC-007 in [../security-scanning-and-curation.md](../security-scanning-and-curation.md)), installs with
`--prefer-source` are not supported: Composer then clones directly from the source-control URL in
the metadata, so the system never sees the download and cannot block it. Only `dist` installs are
covered by curation.

## Notes for implementers

- Answer unknown packages on the `metadata-url` path with a fast `404`; Composer looks up each
  required package name in each configured repository, and slow or redirected misses stall
  installs.
- Serve accurate `Last-Modified` on per-package files; Composer 2 caches them and revalidates with
  `If-Modified-Since` only.
- Keep `<name>.json` (tagged versions) and `<name>~dev.json` (development versions) separate;
  Composer requests the `~dev` file only when the stability settings allow dev versions.
- `composer.json` inside deployed archives often has no `version`; the deploy-time version
  property is the primary version source for internal packages, so persist it with the item.
- Rewrite `dist.url` of proxied metadata to the system, or clients bypass the cache and need direct
  access to the source-control host.
- Emit `published-time` per version (for local packages the deploy time is the natural value)
  and pass the upstream's value through for proxied ones; without it, clients that enable
  Composer's cooldown fail on the repository.
- The `source` entries in proxied metadata still point at the original repositories; clients using
  `--prefer-source` reach them directly.
- Clients must use the Composer protocol URL; serving `packages.json` on the raw file URL as well
  would let clients configure the wrong address silently.
