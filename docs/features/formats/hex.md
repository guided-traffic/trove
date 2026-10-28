# Hex package format (Elixir and Erlang)

This page catalogs what a repository manager does to act as a Hex repository for the BEAM
ecosystem (Elixir, Erlang and other languages on the BEAM VM) with the Mix client: hosting package
tarballs in local repositories, proxying `hex.pm` or self-hosted Hex servers in remote
repositories, merging them in virtual repositories, signing the registry with the system's own
key, client setup, and per-package access control. The repository protocol is defined by the Hex
project in the [Hex specifications](https://github.com/hexpm/specifications) (cited below as "the
Hex specification", chiefly
[endpoints](https://github.com/hexpm/specifications/blob/main/endpoints.md),
[registry v2](https://github.com/hexpm/specifications/blob/main/registry-v2.md) and
[package tarball](https://github.com/hexpm/specifications/blob/main/package_tarball.md)); entries
whose behavior follows from it say so. Repository classes in
general are described in [../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/hex/<repo-key>` stands for the Hex repository URL
of one repository and `https://repo.example.com/files/<repo-key>/<path>` for raw file access to it
(both path layouts are the implementation's choice), `hex-local`, `hex-remote` and `hex-virtual`
are example repository keys, and `${TOKEN}` is an access token.

## Repositories and addressing

### PKG-HEX-001 · Hex repositories in all three repository classes
Priority: **P2**

Hex is supported as a local, a remote and a virtual repository format:

- local repositories hold first- and second-party packages (PKG-HEX-014, PKG-HEX-015);
- remote repositories proxy the public `hex.pm` registry (including private organizations on it)
  or a self-hosted Hex server (PKG-HEX-016);
- virtual repositories aggregate local and remote members behind one URL (PKG-HEX-017);
- every registry resource the system serves is signed with an RSA key pair assigned to the
  repository (PKG-HEX-007);
- access can be narrowed per package with include and exclude patterns on the layout folders
  (PKG-HEX-018).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, then the class and the Hex format, then
the RSA key pair (UI-031 in [../ui-inventory.md](../ui-inventory.md)). The class-support table
(REPO-006 in [../repositories.md](../repositories.md)) does not list Hex; this page covers all three
classes. Scanner indexing (REPO-021) is unspecified for Hex. Federation is open: either Hex
repositories can be federated, since federation admits every format except the Terraform state
backend (REPL-026 in [../replication-and-federation.md](../replication-and-federation.md)), or
they cannot, since none of the format lists of the repository configuration names Hex; the spec
decides.

### PKG-HEX-002 · Repository URL and client-side repository name
Priority: **P2**

The Mix client knows each repository by a *name* and a URL. Two configurations exist:

| Client configuration | Use |
|---|---|
| `mix hex.repo set hexpm --url <repository-url> …` | Replaces the default `hexpm` repository, so unqualified dependencies resolve through the system. Used for remote repositories that proxy `hex.pm` and for virtual repositories. |
| `mix hex.repo add <name> <repository-url> …` | Adds a named repository; dependencies select it with `repo: "<name>"`. Used for local repositories and remotes of self-hosted Hex servers. |

- In Mix, a single repository entry stands for `hex.pm`, so only one system repository
  can replace the default `hexpm` entry per client.
- The Hex specification makes the client compare the `repository` field inside each signed package
  resource with the name it configured. The system must therefore write the client's repository
  name into the resources it signs: `hexpm` for a repository that replaces the default, the added
  name otherwise. Open: either the added name is the repository key (as in the examples on this
  page) or it must match "the full URL" of the remote repository; the spec decides.

## Protocol resources

### PKG-HEX-003 · Registry resources
Priority: **P2**

A Hex repository is read-only static resources (Hex specification, registry v2):

| Resource | Content |
|---|---|
| `<repository-url>/names` | All package names (`Names` message) |
| `<repository-url>/versions` | All package names with their versions and retired versions (`Versions` message) |
| `<repository-url>/packages/<name>` | All releases of one package with dependencies, inner and outer checksums, retirement status, security advisories and publication time (`Package` message) |
| `<repository-url>/tarballs/<name>-<version>.tar` | The package tarball (PKG-HEX-004) |
| `<repository-url>/public_key` | Optional; the repository's public key |

- Encoding: every registry resource is a Protocol Buffers payload wrapped in a `Signed` message
  (payload plus an RSA signature over the SHA-512 digest of the payload) and gzip-compressed.
  Repositories are required to sign all registry resources.
- Per the Hex specification the resources are signed and compressed, not encrypted, and tarballs
  are neither.
- Dependencies may name another repository; the client maps that name to a URL itself, and the
  name `hexpm` always means the public repository.
- Caching: resources of private repositories differ per user; the Hex specification requires cache
  headers that keep shared caches from serving them across users.

### PKG-HEX-004 · Package tarballs
Priority: **P2**

A Hex package tarball is an uncompressed tar holding `VERSION` (currently `3`), `metadata.config`
(Erlang term file with name, version, requirements, licenses, files and so on), `contents.tar.gz`,
and `CHECKSUM` (Hex specification). Clients verify the tarball against the registry: the *outer
checksum* (SHA-256 of the whole tarball, current) and the deprecated *inner checksum* stored in
`Release`. The system must compute both when it generates registry resources for local packages.

### PKG-HEX-005 · Installs metadata file
Priority: **P2**

The local and remote layouts (PKG-HEX-015, PKG-HEX-016) contain an `installs` folder holding
`hex-1.x.csv`, package metadata uploaded to a local repository or pulled from the upstream and not
further specified.
The Hex specification does not define this file. In the Mix sources
([`local.hex` task of Elixir 1.16](https://github.com/elixir-lang/elixir/blob/v1.16.0/lib/mix/lib/mix/tasks/local.hex.ex))
`mix local.hex` downloads `/installs/hex-1.x.csv`, a signed CSV listing installable releases of the
Hex client itself with their Elixir versions and checksums; current Elixir versions request
`/installs/hex.csv` instead. So the file belongs to installing the Hex client, not to any package.
What the system stores in it for a local repository, and whether it serves the newer `hex.csv`
name, is unspecified.

### PKG-HEX-006 · Organization (private) packages
Priority: **P2**

`hex.pm` hosts private packages of organizations under `<repository-url>/repos/<org>/…` (names,
versions, packages, tarballs and docs, Hex specification). Through remote and virtual repositories
that proxy `hex.pm`, the system resolves such packages; the cached layout is
`repos/<org>/packages/<name>`.

```elixir
defp deps do
  [{:acme_auth, "1.4.4", organization: "acme"}]
end
```

- Self-hosted Hex servers have no organizations or private packages; they exist only on `hex.pm`.
- Organizations are not supported for remotes of a private Hex server.
- How the remote authenticates to the organization on `hex.pm` is unspecified; only the client
  side (above) is defined. (The Hex specification states that private `/repos/<org>/` resources
  require authentication.)

## Signing and keys

### PKG-HEX-007 · Repository signing key pair
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Signing key pair | RSA key pair from the central key store (IAM-046 in [../identity-and-access.md](../identity-and-access.md)) used to sign the registry resources the repository serves to Mix | no default; chosen when the repository is created |

- Two sets of keys exist when the system sits between Mix and an upstream: the upstream's key pair
  (external; only its public key is configured, PKG-HEX-008) and the system's own key pair
  (internal; assigned to the repository).
- The key pair is generated outside the system and added to the key store (key management in the
  security administration) before the repository is created.
- The setting exists on local and remote repositories and in the generic create flow; on virtual
  repositories it is implied because virtual repositories serve merged,
  re-signed resources (inferred).
- UI location: repository form, format-specific key pair selector. REPO-032 in
  [../repositories.md](../repositories.md) lists the local setting.

### PKG-HEX-008 · Upstream public key on remote repositories
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Upstream public key | Public key of the upstream Hex registry, used to verify the resources downloaded from it | Pre-filled for `hex.pm`; must be entered for self-hosted Hex servers |

A remote repository verifies each upstream resource with this key, decodes it, and re-signs it with
its own key pair (PKG-HEX-007) before serving it (the re-signing is implied by the two-key model;
the exact sequence is unspecified).

### PKG-HEX-009 · Public key download for clients
Priority: **P2**

Mix needs the public key of the system's repository key pair in the project to verify the
registry signatures:

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get repository public key | Repository key | PEM public key of the key pair assigned to the repository | Authenticated user (exact privilege unspecified) |

```shell
curl -u "user:${TOKEN}" -o publickey.pem "<public-key-operation-url-for-hex-local>"
```

The client setup instructions (UI-021 and UI-026 in [../ui-inventory.md](../ui-inventory.md))
show the download command with curl and with wget and the user's token filled in. Whether the Hex
protocol resource `<repository-url>/public_key` is also served is unspecified.

## Client setup

### PKG-HEX-010 · Mix client setup per repository class
Priority: **P2**

```shell
# local repository, or remote of a self-hosted Hex server
mix hex.repo add hex-local https://repo.example.com/hex/hex-local \
  --auth-key "Bearer ${TOKEN}" --public-key ./publickey.pem

# remote that proxies hex.pm, or virtual repository
mix hex.repo set hexpm --url https://repo.example.com/hex/hex-virtual \
  --auth-key "Bearer ${TOKEN}" --public-key ./publickey.pem
```

- `--auth-key` is sent verbatim as the `Authorization` header, which is why the examples above pass
  `"Bearer <token>"` (TOK-033 in [../access-tokens.md](../access-tokens.md)).
- `publickey.pem` comes from PKG-HEX-009 and must be at the path given.
- Anonymous access follows the global anonymous-access setting (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)).

### PKG-HEX-011 · Declaring dependencies
Priority: **P2**

| Source | `mix.exs` entry |
|---|---|
| Local repository or self-hosted remote | `{:jason, "1.4.4", repo: "hex-local"}` |
| Public package through a `hexpm` replacement | `{:jason, "1.4.4"}` |
| Organization package through a `hexpm` replacement | `{:acme_auth, "1.4.4", organization: "acme"}` |

Resolution: `mix deps.get`.

### PKG-HEX-012 · Supported clients, versions and commands
Priority: **P2**

| Item | Support |
|---|---|
| Client | Mix only; `rebar3` is not supported |
| Hex client | 2.1.1 or later |
| Elixir | 1.16 or later |
| Erlang/OTP | 26.2.5.6 or later |
| Supported commands | `mix deps.get` (also `--verbose`), `mix deps.update --all`, `mix hex.outdated <package>`, `mix hex.repo list` |
| Not supported | `mix hex.publish` to a local repository (deploy by file upload instead, PKG-HEX-014) |

Commands outside the supported list are unspecified. Since `mix hex.publish` is explicitly
unsupported, the system presumably offers no Hex HTTP API (publish, retire, owners, keys, search;
see the Hex specification's endpoint list), which would make the corresponding Mix commands fail
(inferred).

### PKG-HEX-013 · OAuth token exchange of newer Hex clients
Priority: **P2**

Hex 2.4.0 introduced an OAuth authentication flow that breaks connections to remote and virtual
repositories that proxy `https://repo.hex.pm`. The workaround disables the exchange per
repository:

```shell
mix hex.repo set <name> --no-oauth-exchange
```

- Local repositories are not affected. "Self-managed" repositories are exempt as well; the term
  is undefined, and remotes of self-hosted Hex servers are the plausible reading.
- A known Hex 2.4.0 defect can still make downloads of private `hex.pm` packages fail with the
  exchange disabled.
- What the exchange requests from the server, and whether the system could support it, is
  unspecified.

## Local repositories

### PKG-HEX-014 · Deploy package tarballs
Priority: **P2**

Packages are deployed as plain files (ART-002 and ART-006 in [../artifacts.md](../artifacts.md)),
always into the `tarballs` folder as `tarballs/<name>-<version>.tar`:

```shell
curl -u "user:${TOKEN}" -X PUT -T my_package-0.1.0.tar \
  "https://repo.example.com/files/hex-local/tarballs/my_package-0.1.0.tar"
```

- Deploying into any other folder is not supported.
- A deploy can also go through a virtual repository, which forwards to its default
  deployment repository (REPO-038 in [../repositories.md](../repositories.md)).
- The UI deploy dialog works the same way.
- Required privilege: deploy on the target repository (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).

### PKG-HEX-015 · Local repository layout and metadata generation
Priority: **P2**

| Folder | Content |
|---|---|
| `names` | Package names (generated) |
| `versions` | Package versions (generated) |
| `installs/hex-1.x.csv` | PKG-HEX-005 |
| `packages/<name>` | Per-package registry resource (generated) |
| `tarballs/<name>-<version>.tar` | Uploaded packages |

- The system generates the signed registry resources of PKG-HEX-003 from the uploaded tarballs;
  reading each tarball's `metadata.config` for that is inferred (it is where the Hex
  specification puts name, version and requirements).
- Information about docs, tests and similar from `mix.exs` is not parsed.
- When regeneration runs (on each upload, or only on demand) is unspecified; no recalculation
  operation for Hex is specified.

## Remote and virtual repositories

### PKG-HEX-016 · Remote Hex repositories
Priority: **P2**

A remote Hex repository proxies `hex.pm` or a self-hosted Hex server (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)).

| Folder | Content |
|---|---|
| `installs/hex-1.x.csv` | Pulled from the upstream |
| `packages/<name>` | Package resources pulled from the upstream |
| `tarballs/<name>-<version>.tar` | Tarballs pulled from the upstream |
| `repos/<org>/packages/<name>` | Organization packages (PKG-HEX-006) |

- Settings: PKG-HEX-007, PKG-HEX-008. The default upstream URL is unspecified; the examples
  and warnings on this page refer to `https://repo.hex.pm`.
- The remote cannot list the upstream registry; only resources already pulled are browsable.
- Compatibility with `hex.pm` depends on decoding its current registry format: an unannounced
  change of the `hex.pm` metadata format has made remotes with an outdated decoder fail with 404
  "package not found", intermittently and especially for packages such as `plug`, `cowboy`,
  `phoenix` and `ecto`. Local repositories were not affected.

### PKG-HEX-017 · Virtual Hex repositories
Priority: **P2**

| Folder | Content |
|---|---|
| `names`, `versions` | Merged indexes |
| `packages/<name>` | Merged package resources |
| `tarballs/<name>-<version>.tar` | Tarballs resolved from members |

A virtual repository aggregates local and remote Hex repositories (REPO-033 in
[../repositories.md](../repositories.md)). It serves public packages and organization packages of
remote members that proxy `hex.pm`. How releases of the same package from several members are
merged into one `Package` resource is unspecified.

- One Mix client can be connected to only one Hex virtual repository.

## Access control

### PKG-HEX-018 · Per-package permissions on layout folders
Priority: **P2**

Permissions narrow access to single packages with include and exclude patterns (IAM-021 in
[../identity-and-access.md](../identity-and-access.md)). Because one package's data lives in
several folders, a rule must name each folder. Example, excluding every package whose name starts
with `abc`:

```text
installs/abc*
tarballs/abc*
packages/abc*
```

A rule that names only `tarballs/` presumably still lets the package appear in `packages/` and in
the merged indexes (inferred from the folder layout; only the three-folder example above is
specified, and it is given for remote repository folders).

## Notes for implementers

- Every registry resource must be gzip-compressed protobuf in a `Signed` envelope with an RSA
  signature over the SHA-512 of the payload; an unsigned or wrongly signed resource is rejected by
  Mix.
- The `repository` field inside a signed `Package` must equal the client's configured repository
  name (`hexpm` when replacing the default); re-signing a proxied resource without rewriting this
  field breaks verification.
- Decode upstream protobuf tolerantly: `hex.pm` keeps adding fields (advisories, publication time,
  outer checksum). Remote resolution has broken after an unannounced, unnamed change of the
  `hex.pm` metadata format (PKG-HEX-016); preserving unknown fields when
  re-encoding is the defensive answer.
- Outer checksum (SHA-256 of the whole tarball) is what current clients verify and lock in
  `mix.lock`; never rebuild a tarball.
- `names` and `versions` must not leak packages a user may not read; per-user views make these
  resources uncacheable in shared caches.
- Mix sends `--auth-key` verbatim as the `Authorization` header; a user who passes a bare token
  instead of `Bearer <token>` sends a header without scheme, which the server should either accept
  or reject with a clear 401.
- The public key users install must be the one of the repository's own key pair, not the
  upstream's; rotating the key pair breaks every client until the new key is distributed.
