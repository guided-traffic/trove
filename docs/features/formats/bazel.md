# Bazel modules package format

This page catalogs what a repository manager does for Bazel: serving Bazel modules (Bzlmod) as an
index registry from local, remote and virtual repositories, proxying the Bazel Central Registry
(BCR), publishing an organization's own modules, client setup, and two Bazel uses that run on other
formats (a generic repository as Bazel's HTTP remote cache and Maven repositories for JVM
dependencies). The registry format is defined by the Bazel project in
[Bazel registries](https://bazel.build/external/registry) (cited below as "the Bazel registry
documentation"); entries that rely on it say so. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/bazel/<repo-key>` stands for the registry URL of one
repository and `https://repo.example.com/files/<repo-key>/<path>` for raw file access to it (both
path layouts are the implementation's choice), `bazel-local`, `bazel-remote` and `bazel-virtual`
are example repository keys, and `${TOKEN}` is an access token.

## Repositories and addressing

### PKG-BAZEL-001 · Bazel module repositories
Priority: **P2**

The format is called "Bazel modules". It serves module metadata for dependency resolution only, not
build outputs (PKG-BAZEL-012).

- remote repositories proxy and cache the Bazel Central Registry (PKG-BAZEL-008);
- local repositories store an organization's own modules (PKG-BAZEL-007);
- virtual repositories aggregate local and remote members so the client uses one registry
  (PKG-BAZEL-009);
- the native `bazel` client resolves modules declared in `MODULE.bazel`. The format targets Bazel
  9, which removes the legacy `WORKSPACE` system in favor of Bzlmod.

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, then local, remote or virtual and the
Bazel modules format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).

Open: class support is either local, remote and virtual, as this entry describes (the preferred
reading), or narrower: the class-support matrix (REPO-006 in
[../repositories.md](../repositories.md)) lists remote only, and the repository configuration
interface accepts the format for local, virtual and federated repositories but not for remote
ones. The spec decides. Federation is not in question: the format is federatable, and federation
admits every format except the Terraform state backend (REPL-026 in
[../replication-and-federation.md](../replication-and-federation.md)).

### PKG-BAZEL-002 · Registry URL
Priority: **P2**

Every Bazel modules repository is an *index registry* (Bazel registry documentation) under its own
base URL, the *registry URL*. Bazel requests static files below it (PKG-BAZEL-003); no other API
is involved. The registry URL is what `--registry` points at (PKG-BAZEL-006).

## Registry format

### PKG-BAZEL-003 · Index registry layout
Priority: **P2**

Files Bazel requests relative to the registry URL (Bazel registry documentation):

| Path | Required | Content |
|---|---|---|
| `bazel_registry.json` | no | Registry-wide metadata: `mirrors` (list of mirror prefixes tried for source archive URLs) and `module_base_path` (for `local_path` sources) |
| `modules/<module>/metadata.json` | no | `versions` (array that should match the version subdirectories) and `yanked_versions` (version to reason) |
| `modules/<module>/<version>/MODULE.bazel` | yes | The module file Bazel uses during dependency resolution; this one wins over the copy inside the source archive |
| `modules/<module>/<version>/source.json` | yes | How to fetch the source (PKG-BAZEL-004) |
| `modules/<module>/<version>/patches/` | no | Patch files referenced from `source.json` |
| `modules/<module>/<version>/overlay/` | no | Overlay files referenced from `source.json` |

The BCR additionally requires a `presubmit.yml` per version; other registries need not serve it.
The system stores each version in this layout (`modules/<module>/<version>/`).

### PKG-BAZEL-004 · Source description and source archives
Priority: **P2**

`source.json` tells Bazel where the module's source comes from (Bazel registry documentation):

| Type | Fields |
|---|---|
| `archive` (default) | `url`, `mirror_urls`, `integrity` (Subresource Integrity string), `strip_prefix`, `overlay` (file to integrity), `patches` (file to integrity), `patch_strip`, `archive_type` |
| `git_repository` | `remote`, `commit`, `shallow_since`, `tag`, `init_submodules`, `verbose`, `strip_prefix`, `patches`, `patch_strip` |
| `local_path` | `path`; only valid for registries on local disk |

- For local repositories the system generates `source.json` from the uploaded archive
  (PKG-BAZEL-007); the generated `url` points at the archive in the repository and `integrity` is
  computed from it (inferred: Bazel cannot resolve the module otherwise).
- For remote repositories, BCR entries mostly point at external hosts (for example GitHub release
  archives). Whether the system rewrites `url` to fetch archives through itself, advertises itself
  in `bazel_registry.json` mirrors, or leaves source downloads to the client is unspecified. The
  BCR's own guidance for self-hosting is to mirror source archives and list the mirror in
  `bazel_registry.json`.

## Client setup and authentication

### PKG-BAZEL-005 · Client authentication through netrc
Priority: **P2**

Bazel reads credentials for registry and download requests from the netrc file:

| Platform | File |
|---|---|
| Linux, Unix, macOS | `~/.netrc` |
| Windows | `%USERPROFILE%\_netrc` |

```text
machine repo.example.com
login <user>
password ${TOKEN}
```

The token is used as the Basic password (TOK-034 in [../access-tokens.md](../access-tokens.md)).
The client setup instructions (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md))
offer the snippet with the user's token filled in.

### PKG-BAZEL-006 · Registry configuration
Priority: **P2**

The registry is configured per command in a `.bazelrc` file:

```text
common --registry=https://repo.example.com/bazel/bazel-virtual
```

| Prefix | Effect |
|---|---|
| `common` | All commands resolve through the registry |
| `build`, `fetch`, `test` | Only that command resolves through it |

- Locations: `~/etc/bazel.bazelrc` (Linux, Unix, macOS) and `%ProgramData%\bazel.bazelrc`
  (Windows); a project-level `.bazelrc` works the same way.
- One-off override: `bazel build --registry=https://repo.example.com/bazel/bazel-virtual @rules_go`.
- Resolution then runs with plain Bazel commands, for example `bazel build @rules_go`.
- `--registry` may be repeated; earlier registries take precedence, and once any `--registry` is
  given the BCR is no longer consulted unless listed explicitly (Bazel registry documentation).
  A virtual repository that includes a BCR remote avoids listing several registries.

## Local repositories

### PKG-BAZEL-007 · Publish a module version
Priority: **P2**

A module version is published by uploading two files per version into
`modules/<module>/<version>/`:

| File | Note |
|---|---|
| The module's source archive | Any archive Bazel can extract |
| `MODULE.bazel` | Uploaded separately; the system does not extract it from the archive |

- Do not upload `source.json`: after both files are present the system generates it from the
  archive, and only then can Bazel resolve the module.
- Upload is a plain file deployment (ART-002 in [../artifacts.md](../artifacts.md)). The
  companion command-line client's recursive upload, started in the parent folder of the module
  directories so that the `modules/<module>/<version>/` paths survive, can push every module,
  every version of one module, or a single version. The same result with plain HTTP:

  ```shell
  curl -u "user:${TOKEN}" -T rules_acme-1.0.0.tar.gz \
    "https://repo.example.com/files/bazel-local/modules/rules_acme/1.0.0/rules_acme-1.0.0.tar.gz"
  curl -u "user:${TOKEN}" -T MODULE.bazel \
    "https://repo.example.com/files/bazel-local/modules/rules_acme/1.0.0/MODULE.bazel"
  ```

- Uploading to a virtual repository is possible only when it has a default deployment repository
  (REPO-038 in [../repositories.md](../repositories.md)).
- Whether the system also maintains `modules/<module>/metadata.json` (version list, yanked
  versions) is unspecified.
- Required privilege: deploy on the target repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).

## Remote and virtual repositories

### PKG-BAZEL-008 · Proxying the Bazel Central Registry
Priority: **P2**

A remote Bazel modules repository proxies and caches the BCR (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)), whose public endpoint is
`https://bcr.bazel.build/`.

- The default upstream URL is unspecified; the form asks to verify it.
- The cache gives builds a stable, vetted set of dependencies; how source archives referenced by
  cached entries are handled is open (PKG-BAZEL-004).

### PKG-BAZEL-009 · Virtual Bazel module repositories
Priority: **P2**

A virtual repository aggregates local and remote Bazel modules repositories (REPO-033 in
[../repositories.md](../repositories.md)); it is the recommended configured registry, so private
modules and BCR modules resolve through one URL.

- A module directory present in several members resolves in member order (REPO-035); whether
  `metadata.json` version lists are merged across members is unspecified.
- Uploads go to the default deployment repository (PKG-BAZEL-007).

## Bazel on other formats

### PKG-BAZEL-010 · Generic repository as Bazel HTTP remote cache
Priority: **P2**

Bazel's HTTP remote cache stores build outputs in any server that accepts `PUT` and `GET` on two
key spaces; a local generic repository (REPO-003 in [../repositories.md](../repositories.md)) serves
this without format-specific logic.

```shell
bazel build --remote_cache=https://user:${TOKEN}@repo.example.com/files/bazel-remote-cache
```

| Path below the cache URL | Content | Requests |
|---|---|---|
| `ac/<sha256>` | Action results (metadata) | `GET` to read, `PUT` to write |
| `cas/<sha256>` | Output files, content-addressed | `GET` to read, `PUT` to write |

- In this setup, credentials travel in the URL as Basic authentication.
- Validation: the system's request log shows the `PUT` and `GET` calls on these paths.
- Security: anyone who can write to the cache can inject outputs into other users' builds; grant
  deploy on the cache repository only to trusted build identities (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).

### PKG-BAZEL-011 · Maven repositories for Bazel JVM dependencies
Priority: **P1**

Bazel resolves and publishes JVM artifacts against Maven repositories through the
[`rules_jvm_external`](https://github.com/bazelbuild/rules_jvm_external) ruleset; the system's side
is ordinary Maven behavior (see the Maven format page under [README.md](README.md)).

- Resolve: `maven_install(…, repositories = ["https://user:${TOKEN}@repo.example.com/maven/maven-remote"])`;
  private repositories authenticate with HTTP Basic credentials in the URL.
- Publish: a `java_export` target with Maven coordinates, run as
  `MAVEN_USER=<user> MAVEN_PASSWORD=${TOKEN} bazel run --stamp --define "maven_repo=<url>" --define gpg_sign=true //pkg:<target>.publish`;
  signing uses the default GPG key of the build machine.
- Targets: local Maven repositories, or virtual repositories with a default deployment repository.

### PKG-BAZEL-012 · Limitations
Priority: **P2**

| Limit | Detail |
|---|---|
| Build outputs | Bazel modules repositories hold modules only; build outputs go to generic or Maven repositories (PKG-BAZEL-010, PKG-BAZEL-011) |
| Vulnerability scanning | Not applied to Bazel modules repositories (REPO-021 in [../repositories.md](../repositories.md)) |
| Upstream curation | Not applied (SEC-007 in [../security-scanning-and-curation.md](../security-scanning-and-curation.md)) |
| Companion command-line client | Offers no Bazel wrapper; the native `bazel` client resolves directly |

## Notes for implementers

- `bazel_registry.json` and `metadata.json` are optional by definition: when absent they must
  answer 404, not an error page or a login redirect, or the client cannot tell "absent" from
  "broken".
- `MODULE.bazel` in the registry, not the one in the archive, drives resolution; serving the
  archive's copy instead silently changes dependency graphs.
- `integrity` in `source.json` is a Subresource Integrity string (`sha256-<base64>`), not a hex
  digest; a wrong encoding fails every fetch.
- Generated `source.json` must be byte-stable once served: `MODULE.bazel.lock` records registry
  file hashes, so regenerating it with different content breaks locked builds.
- Bazel stops consulting the BCR as soon as any `--registry` is set; a remote of the BCR must
  therefore serve every module a project needs, including transitive ones.
- `bazel_registry.json` mirrors are prefixed to the source URL without its scheme
  (`https://mirror/` + `github.com/…`); a system that offers itself as mirror must accept that path
  form.
- `cas/` keys are content hashes and never change content, while an `ac/` key can be written again
  with a new action result; the build identities need the overwrite action on the cache repository
  (IAM-022), or repeated writes fail.
