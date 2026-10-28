# RubyGems format

This page catalogs what a repository manager does to act as a RubyGems source and push target:
hosting gems in local repositories, caching an upstream gem registry in remote repositories,
combining both in virtual repositories, the two index forms clients read (the classic spec index
and the compact index used by Bundler), the subset of the RubyGems.org API it serves, push, yank
and index maintenance, and client setup for `gem` and Bundler. The protocol is defined by the
RubyGems project ([RubyGems.org API](https://guides.rubygems.org/rubygems-org-api/),
[Compact Index API](https://guides.rubygems.org/rubygems-org-compact-index-api/), and the client
sources in [rubygems/rubygems](https://github.com/rubygems/rubygems)); where an entry states
protocol behavior, it links that source. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/gems/<repo-key>/` stands for the RubyGems base URL
of one repository (the path layout is the implementation's choice),
`https://repo.example.com/files/<repo-key>/<path>` for raw file access, and `gems-local`,
`gems-remote` and `gems-virtual` are example repository keys.

## Repositories and addressing

### PKG-GEMS-001 · RubyGems repositories in all three repository classes
Priority: **P2**

RubyGems is supported as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)): local repositories hold the organization's gems, remote
repositories proxy and cache an upstream gem registry (PKG-GEMS-017), virtual repositories offer
one source URL for local and remote members (PKG-GEMS-016).

Creating a repository requires the system administrator or a project administrator role; for others
the create action is not offered. UI location: administration, repositories, create repository,
then class and RubyGems format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).

| Setting (system-wide) | Meaning | Default |
|---|---|---|
| Seed new local repositories | A newly created local RubyGems repository is filled with the `rubygems-update` gem (`rubygems-update-*.gem`) | on |

Edge cases: which `rubygems-update` version is seeded, and why, is unspecified. A compatible
implementation can leave the switch off without client impact; it exists only as a system-wide
setting.

### PKG-GEMS-002 · RubyGems endpoint of a repository
Priority: **P2**

Every RubyGems repository answers under its RubyGems base URL. Besides plain download, deploy and
delete of files, the URL space contains the following, all defined by the RubyGems project:

| Path below the base URL | Purpose | Entry |
|---|---|---|
| `gems/<name>-<version>[-<platform>].gem` | Gem file download | PKG-GEMS-007 |
| `specs.4.8.gz`, `latest_specs.4.8.gz`, `prerelease_specs.4.8.gz` | Classic spec index: gzip-compressed Ruby Marshal lists of all, latest and pre-release name/version/platform tuples | PKG-GEMS-013 |
| `quick/Marshal.4.8/<name>-<version>[-<platform>].gemspec.rz` | Classic per-gem specification, zlib-deflated Marshal | PKG-GEMS-013 |
| `versions`, `info/<name>`, `names` | Compact index | PKG-GEMS-015 |
| `api/v1/gems` (`POST`) | Push | PKG-GEMS-006 |
| `api/v1/gems/yank` (`DELETE`) | Yank | PKG-GEMS-008 |
| `api/v1/gems/<name>.(json\|yaml)` | Gem information | PKG-GEMS-009 |
| `api/v1/search.(json\|yaml)?query=<q>` | Search | PKG-GEMS-010 |
| `api/v1/dependencies?gems=<a>,<b>` | Dependency API | PKG-GEMS-011 |
| `api/v1/versions/<name>` | Version list | PKG-GEMS-012 |
| `api/v1/api_key.yaml` (`GET`) | Retrieve push credentials | PKG-GEMS-004 |

`4.8` is the Ruby Marshal format version (`Gem.marshal_version`), constant for all current Ruby
releases. Client behavior that depends on this space: the `gem` command first requests `versions`;
if that succeeds it resolves through the compact index, otherwise it falls back to the classic spec
index (`Gem::Source#new_dependency_resolver_set` in the RubyGems sources).

Which paths each repository class serves is unspecified, except for the compact index, which all
three classes serve (PKG-GEMS-015).

## Client setup and authentication

### PKG-GEMS-003 · Gem source configuration
Priority: **P2**

Resolution sources live in `~/.gemrc` and are managed with `gem sources`:

```shell
gem sources -a "https://<user>:<token>@repo.example.com/gems/gems-virtual/"
gem sources --list          # effective sources in order
gem sources -r <url>        # remove a source
```

- Every repository a client resolves from must be configured as a source; sources are tried in
  the listed order. A virtual repository reduces this to one source.
- Credentials embedded in the source URL are used for resolution only; push uses separate
  credentials (PKG-GEMS-004). An `@` inside the user name or token has to be percent-encoded.
- With anonymous access enabled, the source URL needs no credentials.
- Per-command override: `gem install <gem> --source https://repo.example.com/gems/gems-local`.
- The client setup dialog generates the command with the user's token (UI-021, UI-026 in
  [../ui-inventory.md](../ui-inventory.md)).

Edge cases (`401` from `gem` or `bundle`), remedies: include credentials in
the source URL; or grant the anonymous user read access through a permission policy on the
repository and enable anonymous access (IAM-005 and the permission model in
[../identity-and-access.md](../identity-and-access.md)).

Security: the credential sits in clear text in `~/.gemrc` and in `gem sources` output.

### PKG-GEMS-004 · Push credentials
Priority: **P2**

Push credentials are kept in the RubyGems credentials file (`~/.gem/credentials`;
`%USERPROFILE%\.gem\credentials` on Windows), separate from the source URLs. The system offers an
operation that returns a ready-made credentials file:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Retrieve push credentials | HTTP Basic authentication (user name and password or token) on `GET <base>/api/v1/api_key.yaml` | YAML credentials document for `~/.gem/credentials` | authenticated user |

```shell
curl -L -u "<user>:<token>" https://repo.example.com/gems/gems-local/api/v1/api_key.yaml \
  > ~/.gem/credentials
chmod 600 ~/.gem/credentials
```

- RubyGems refuses to use a credentials file whose mode is not `0600` (Windows excepted), hence
  the `chmod 600`.
- RubyGems sends the stored key verbatim as the `Authorization` header value on push and yank;
  whatever the operation returns must therefore be accepted back as an `Authorization` value. The
  exact content of the returned document is unspecified.
- Alternative without a file: the environment variable `GEM_HOST_API_KEY`. The companion client
  (PKG-GEMS-018) fills it with `Basic <base64(user:password)>`, an ordinary HTTP Basic value;
  a bare `user:password` is not valid there.
- RubyGems.org itself retired the `GET` variant of this endpoint (it answers `410`); keys are
  created there with `POST /api/v1/api_key`. Whether the system supports the `POST` form is
  unspecified.

### PKG-GEMS-005 · Bundler configuration
Priority: **P2**

Bundler reads sources from the `Gemfile` and credentials from its own configuration
(`~/.bundle/config` or environment variables). Two configuration forms point Bundler at the system
without editing every `Gemfile`:

| Bundler setting | Example | Effect |
|---|---|---|
| Mirror for rubygems.org | `BUNDLE_MIRROR__HTTPS://RUBYGEMS__ORG/: "https://<user>:<token>@repo.example.com/gems/gems-virtual/"` | A `Gemfile` that still names `https://rubygems.org` resolves through the repository |
| Per-host credentials | `BUNDLE_REPO__EXAMPLE__COM: "<user>:<token>"` | A `Gemfile` that names the repository URL authenticates without credentials in the `Gemfile` |

Host-key spelling: Bundler 2 and later uppercase the host and replace `.` with `__` and `-` with
`___` (`my-repo.example.com` becomes `BUNDLE_MY___REPO__EXAMPLE__COM`); Bundler 1 replaces only
`.` and keeps `-`.

## Publishing and lifecycle

### PKG-GEMS-006 · Push a gem
Priority: **P2**

`gem push` uploads a built `.gem` file to a local repository. RubyGems sends
`POST <host>/api/v1/gems` with the gem as the request body and the key in `Authorization`
([RubyGems.org API: POST /api/v1/gems](https://guides.rubygems.org/rubygems-org-api/#gem-methods)).

```shell
export RUBYGEMS_HOST=https://repo.example.com/gems/gems-local
gem push acme-tools-2.1.0.gem
# or per command:
gem push acme-tools-2.1.0.gem --host https://repo.example.com/gems/gems-local --key <key-name>
```

- `RUBYGEMS_HOST` or `--host` names the repository's RubyGems base URL without a trailing slash.
  RubyGems appends `/api/v1/gems`; a trailing slash produces `//api/v1/gems`, which the system
  rejects.
- `--key` selects a named key from the credentials file (PKG-GEMS-004).
- The system stores the gem under the repository's `gems/` folder and updates the indexes
  (PKG-GEMS-013, PKG-GEMS-015).

Required privilege: deploy permission on the target repository.

Edge cases: pushing to a virtual repository, and the response to a duplicate version, are
unspecified. Client-side traps, from the RubyGems push command:

- When the gemspec metadata sets `allowed_push_host` (or `default_gem_server`), `gem push` without
  `--host` sends the gem to that host and ignores `RUBYGEMS_HOST`; with a `--host` naming a
  different host it refuses to push. Internal gems need the repository's URL in that metadata.
- The plain push body is the raw `.gem` with `Content-Type: application/octet-stream`. With
  `--attestation`, RubyGems instead sends `multipart/form-data` with the gem and the attestation
  as separate parts; whether the system accepts that form is unspecified.

### PKG-GEMS-007 · Deploy a gem file by path
Priority: **P2**

A gem can also be uploaded as a plain file through the UI deploy dialog (UI-013 in
[../ui-inventory.md](../ui-inventory.md)) or the generic deploy operation (ART-002 in
[../artifacts.md](../artifacts.md)). Only files placed in the repository's `gems` folder
(`gems/<name>-<version>[-<platform>].gem`) are included in the index calculation; elsewhere the
file is stored but invisible to clients.

### PKG-GEMS-008 · Yank a gem version
Priority: **P2**

Removes a version from the repository's indexes so that new resolutions no longer select it
(RubyGems.org semantics). RubyGems' `gem yank <name> -v <version> [--platform <p>]` sends
`DELETE <host>/api/v1/gems/yank` with form fields `gem_name`, `version` and optional `platform`
([RubyGems.org API: yank](https://guides.rubygems.org/rubygems-org-api/#gem-methods)).

- Yank is one of the supported RubyGems.org API commands, with gem name, version and platform as
  inputs. A compatible implementation serves it at the RubyGems.org path that `gem yank` uses.
- Whether yank deletes the file or only hides it from the indexes is unspecified.

Required privilege: unspecified (delete permission on the repository is the natural fit).

## RubyGems.org API subset

### PKG-GEMS-009 · Gem information
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Gem information | gem name; `.json` or `.yaml` | Basic data of the gem in the RubyGems.org shape: name, current version, platform, authors, info, licenses, metadata, SHA-256, project and gem URIs, runtime and development dependencies |

Reference shape:
[`GET /api/v1/gems/[GEM NAME].(json|yaml)`](https://guides.rubygems.org/rubygems-org-api/#gem-methods).
Which fields the system fills (download counts, URIs) is unspecified. Privilege: read permission on
the repository (inferred).

### PKG-GEMS-010 · Search
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Search gems | `query` parameter; `.json` or `.yaml` | Array of gem documents matching the query |

Reference shape:
[`GET /api/v1/search.(json|yaml)?query=…`](https://guides.rubygems.org/rubygems-org-api/#gem-methods);
RubyGems.org pages results by 30 with a `page` parameter. Paging and matching rules of the system
are unspecified. Used by `gem search --remote`.

### PKG-GEMS-011 · Dependency API
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Dependencies | `gems` parameter: comma-separated gem names | Dependency information for every version of the named gems |

- This is the pre-compact-index resolution API of Bundler. The RubyGems.org API guide no longer
  lists it; it is deprecated in favor of the compact index, on which the system's indexing relies
  (PKG-GEMS-015; whether that is the default is open there).
- The response encoding is unspecified.

Interaction: its deprecation is why virtual RubyGems repositories offer no priority resolution
(PKG-GEMS-016).

### PKG-GEMS-012 · Version list of a gem
Priority: **P2**

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Version list | repository, gem name (both non-empty) | Array of versions with authors, build date, creation date, description, download count, metadata map, version number, summary, platform, required Ruby version, pre-release flag, licenses, requirements, SHA-256; JSON or YAML | authenticated user |

- Unknown gem: `404` with an error document; missing or invalid credentials: `401`.
- The path carries no format suffix; the response is JSON or YAML by content type. RubyGems.org's
  equivalent, [`GET /api/v1/versions/[GEM NAME].(json|yaml)`](https://guides.rubygems.org/rubygems-org-api/#gem-version-methods),
  selects the format by suffix; whether the system also accepts the suffixed form is unspecified.
- Same operation as ART-052 in [../artifacts.md](../artifacts.md).

## Indexing

### PKG-GEMS-013 · Automatic spec index maintenance
Priority: **P2**

The system maintains the classic spec index files (PKG-GEMS-002) of a repository automatically in
the background after gems are added or removed; no client or administrator action is needed. Only
gems in the `gems` folder are indexed (PKG-GEMS-007).

Edge cases: the delay between a push and the updated index, and whether index updates are batched,
are unspecified. Clients that read the classic index may see a pushed gem only after the
background update.

### PKG-GEMS-014 · Reindex and update-index operations
Priority: **P2**

| Operation | Inputs | Effect | Output | Privilege |
|---|---|---|---|---|
| Reindex | repository key | Re-creates all spec index files of the repository | `200` with plain text; `401`, `403`, `500` on failure | unspecified |
| Update index | repository key | Updates the spec index files where needed | unspecified | unspecified |

- Both are `POST` operations on the repository's RubyGems endpoint.
- Reindex is meant to calculate the index on a specified path, but its only input is the
  repository key; path-scoped reindexing is unspecified.

Interaction: REPO-009 (recalculate repository index) in [../repositories.md](../repositories.md).

### PKG-GEMS-015 · Compact index for Bundler
Priority: **P2**

The compact index lets Bundler and current RubyGems pick the newest version compatible with the
installed Ruby without loading the full classic index. Local, remote and virtual repositories all
offer it. Format defined by the RubyGems project
([Compact Index API](https://guides.rubygems.org/rubygems-org-compact-index-api/)):

| Path | Content |
|---|---|
| `versions` | Opaque header lines, `---`, then `<name> [-]<version>[,<version>…] <md5 of info file>` per gem; appended over time, a leading `-` marks a version yanked since the last full recalculation; only the last MD5 of a gem is current |
| `info/<name>` | Opaque header, `---`, then one line per version: `<version>[-<suffix>] <dep>:<constraint>[&<constraint>],…\|checksum:<sha256>[,ruby:<req>][,rubygems:<req>][,platform:<p>][,created_at:<time>]`; the suffix is the platform, or for content-addressable gems a content address (those lines then carry `platform:` and require a newer RubyGems, so older clients skip them); yanked versions are removed, never flagged |
| `names` | `---`, then one gem name per line |

- All three support `ETag`/`If-None-Match` and are designed for `Range: bytes=<n>-` requests
  that fetch only the appended tail; responses for `versions` and `info` carry a `Repr-Digest` with
  the SHA-256 of the whole file.
- Format version 2 adds `created_at` per version, which Bundler's cooldown feature uses; versions
  without it are treated as outside any cooldown window.
- Which of these protocol details (ranged requests, digests, format version) the system
  implements is unspecified.

| Setting (system-wide) | Meaning | Default |
|---|---|---|
| Compact index | Serve the compact index | unspecified (open, see below) |

Open: either administrators switch the compact index on through this system-wide setting, or the
default indexing already uses the compact index (PKG-GEMS-011); the spec decides.

### PKG-GEMS-016 · Virtual RubyGems repositories
Priority: **P2**

A virtual RubyGems repository aggregates local and remote members under one source URL (REPO-033
in [../repositories.md](../repositories.md)); deployment through it goes to the default deployment
repository (REPO-038).

Limit: priority resolution (REPO-018) is not supported for RubyGems virtual repositories, because
of the deprecation of the dependency API (PKG-GEMS-011) and the switch to the compact index.

Edge cases: how `versions` and `info/<name>` are merged across members (MD5 of a merged info file,
conflicting checksums for the same version) is unspecified.

### PKG-GEMS-017 · Remote RubyGems repository
Priority: **P2**

A remote RubyGems repository proxies and caches an upstream gem registry or another instance
(RMT-001, RMT-004 in [../remote-repositories.md](../remote-repositories.md)). The form shows a
pre-filled upstream URL to confirm or change; the pre-filled value and any RubyGems-specific
remote settings are unspecified.

Edge case known from the client: when the source host is `rubygems.org`, RubyGems itself fetches
the compact index from `index.rubygems.org`; a remote whose upstream is `https://rubygems.org`
may have to do the same (`Gem::Source#new_dependency_resolver_set` in the RubyGems sources).

## Companion client

### PKG-GEMS-018 · Companion client for RubyGems and Bundler
Priority: **P2**

The companion command-line client (BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)) has a
setup command and a wrapper for `gem` and `bundle`.

<details>
<summary>Setup command and wrapper behavior</summary>

Setup command:

| Input | Meaning | Default |
|---|---|---|
| Connection profile, or URL with user and password, token, client certificate and key, or SSH key and passphrase | Server and credentials | the default profile |
| Repository | RubyGems repository key | interactive selection of a compatible virtual repository |
| Project | Project used to list repositories | none |

- Writes `~/.bundle/config` (mirror for rubygems.org with credentials in the URL, plus per-host
  credentials, in both Bundler 2 and, for hosts containing `-`, Bundler 1 spelling;
  PKG-GEMS-005) and `~/.gemrc` (the repository as a source with credentials, the public RubyGems
  source removed, other system sources kept with the newest first).
- Keeps unrelated settings; writes both files readable by the owner only. Credentials must be
  refreshed by re-running setup after they change or expire.
- A separate older project-configuration command stores resolution and deployment repositories in
  a project file; the wrapper does not read that file.

Wrapper:

- Passes all native arguments, in their original order, to the real `gem` or `bundle`; only the
  wrapper's own options (connection profile, repository key, build name, build number, module,
  project) are taken out.
- Repository discovery order: the repository-key option (turned into the repository's RubyGems
  URL), a native `--source`/`-s`/`--host` URL, a system `source` in the active `Gemfile`, the first
  system source in `gem sources --list`.
- Injects credentials only when the discovered host matches the configured server and the native
  tool has none: `GEM_HOST_API_KEY` as `Basic <base64(user:password)>` for `gem install`, `fetch`
  and `push`, and `BUNDLE_<HOST_KEY>` as `user:password` for Bundler (both host-key spellings).
  Never overwrites variables already set. For `gem push` it adds a temporary host entry to
  `~/.gem/credentials` and restores the file afterwards, also on interrupt.
- Removes a trailing slash from a `--host` value before running `gem push` (PKG-GEMS-006).
- Build metadata, only with build name and number: `bundle install`, `update`, `add` record
  dependencies from `Gemfile.lock` (respecting `--with`/`--without`); `gem install` and `fetch`
  record the gems that invocation changed (scoped to `--install-dir` when given); `gem build`
  records lockfile dependencies only if a lockfile exists; `gem push` records the pushed gem as an
  artifact; `bundle lock` records nothing. Git and path gems are recorded without checksums. The
  wrapper looks up checksums on the server through the query language
  ([../search-and-query.md](../search-and-query.md)); against a virtual repository that lookup
  fails, and the record is kept without checksums.
- The transparent command interception (BLD-042) can route plain `gem` and `bundle` to the
  wrapper when switched to that mode; the default is pass-through.

</details>

## Notes for implementers

- `gem` probes `<source>/versions` first and silently falls back to the classic Marshal index;
  serve both, or clients on the fallback path see a different (possibly stale) view.
- Push and yank carry the key verbatim in `Authorization`; accept standard `Basic …` values there
  as well as whatever the credentials operation hands out.
- Accept a push both as a raw `application/octet-stream` body and as `multipart/form-data` (the
  form RubyGems uses when attestations are attached), or reject the multipart form with a clear
  error.
- Reject or normalize `//api/v1/gems`: a trailing slash in `RUBYGEMS_HOST` is the most common push
  failure.
- The compact index is append-only within a period; yanks append a `-version` entry to
  `versions` and rewrite `info/<name>`. Keep MD5 values in `versions` consistent with the served
  `info` files, or Bundler refetches in a loop.
- Support `Range` requests and `ETag` on `versions` and `info/*`; Bundler relies on partial
  fetches and verifies the full-file SHA-256 from `Repr-Digest`.
- Index only `gems/<file>.gem` paths; files elsewhere must not appear in any index.
- The version-list operation (`api/v1/versions/<gem>`) requires an authenticated user; whether
  anonymous access, where enabled, lifts that is unspecified.
- Platform-specific gems are separate index entries (`<version>-<platform>`); yank takes the
  platform into account.
