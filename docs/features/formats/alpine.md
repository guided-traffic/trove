# Alpine Linux package format

This page catalogs what a repository manager does to serve Alpine Linux packages (`.apk`) to the
`apk` client (Alpine Package Keeper): the branch/repository/architecture directory structure,
automatic `APKINDEX.tar.gz` calculation for local repositories, index signing with RSA key pairs
(including re-signing merged indexes in virtual repositories), proxying the public Alpine mirrors,
and client setup. The repository structure follows Alpine's own convention
([Alpine Package Keeper](https://wiki.alpinelinux.org/wiki/Alpine_Package_Keeper)); entries that
rely on apk-tools behavior say so. Throughout this page,
`<repo-url>` stands for the URL under which a repository's files are served by path and
`https://repo.example.com` for the system's base URL; the concrete path layout is the
implementation's choice. `alpine-local`, `alpine-remote` and `alpine-virtual` are example
repository keys.

## Repositories and layout

### PKG-APK-001 · Alpine repositories in all three repository classes
Priority: **P2**

Alpine is a repository format for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold an organization's own `.apk` packages; the system computes their index
  (PKG-APK-003);
- remote repositories proxy and cache the public Alpine package mirrors (PKG-APK-011);
- virtual repositories merge local and remote Alpine repositories behind one URL and re-sign the
  merged index (PKG-APK-007).

Supported clients: Alpine Linux 3.9.6 and later with apk-tools 2.10.3 and later. Authentication
through the `HTTP_AUTH` environment variable requires apk-tools 2.12 or later (PKG-APK-014).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, choose the class and the Alpine format
(UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-APK-002 · Branch, repository and architecture structure
Priority: **P2**

An Alpine repository is addressed by three coordinates, and the system uses the same directory
layout as the Alpine mirrors:

```text
<branch>/<repository>/<architecture>/APKINDEX.tar.gz
<branch>/<repository>/<architecture>/<name>-<version>.apk
```

| Coordinate | Meaning | Example |
|---|---|---|
| Branch | Alpine release branch | `v3.20`, `edge` |
| Repository | Collection within the branch | `main`, `community` |
| Architecture | CPU architecture | `x86_64`, `aarch64`, `armhf` |

- Every package must be deployed under `<branch>/<repository>/<architecture>/`. Packages stored
  anywhere else are kept but never appear in any index.
- The coordinates are taken from the path; no properties are needed at deploy time.

## Metadata

### PKG-APK-003 · Automatic index calculation for local repositories
Priority: **P2**

The system computes `APKINDEX.tar.gz` for each `<branch>/<repository>/<architecture>/` folder of a
local repository from the packages in it, so no manual indexing is needed. What exactly triggers a
recalculation (deploy, delete, copy, move) and how long it takes are unspecified. The index is
signed when the repository has an RSA key pair (PKG-APK-006).

### PKG-APK-004 · Recalculate Alpine index
Priority: **P2**

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Recalculate Alpine index | repository key | plain-text confirmation | unspecified; the operation rejects unauthenticated (401) and unauthorized (403) callers |

- Supported by local repositories only.
- Always asynchronous: the call returns before the calculation finishes.
- UI: the "recalculate index" row action on the repository list (REPO-009 in
  [../repositories.md](../repositories.md)).

### PKG-APK-005 · Package metadata as searchable properties
Priority: **P2**

The system writes five values from each package's metadata as properties on the stored file:
package name, version, branch, repository and architecture. The property keys are the
implementation's choice. They feed the package search with the Alpine package type as a criterion
(SRCH-050, SRCH-051 in [../search-and-query.md](../search-and-query.md)).

## Signing

### PKG-APK-006 · Index signing with an RSA key pair
Priority: **P2**

apk-tools verifies repository indexes with RSA keys by default. The system signs the index file
`APKINDEX.tar.gz`, not individual packages.

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Signing key pair | local, virtual (and federated, IAM-046) | RSA key pair from the key store (PKG-APK-008) that signs the repository's `APKINDEX.tar.gz` files. | unspecified (optional) |

- Each Alpine repository can use a different key pair.
- Without a key pair the index is unsigned and clients must run with `--allow-untrusted`
  (PKG-APK-010).
- Implicit apk-tools behavior: the signature is embedded in the index archive
  as an entry named after the public key file (`.SIGN.RSA.<key-file-name>`), and apk looks for a
  file of exactly that name under `/etc/apk/keys/`. The key file name served to clients
  (PKG-APK-009) must therefore match the name used in the signature.

UI location: repository settings, advanced tab, signing key selector.

### PKG-APK-007 · Index re-signing in virtual repositories
Priority: **P2**

A virtual Alpine repository signs the merged index with its own RSA key pair:

- the key pair of the virtual repository signs the virtual index;
- key pairs configured on local members are ignored for the virtual index;
- metadata coming from remote members is re-signed with the virtual repository's key.

Consequence: clients trust exactly one public key (the virtual repository's) instead of managing
the keys of every upstream. Remote repositories are therefore best placed inside a virtual
repository.

| Setting | Meaning | Default |
|---|---|---|
| Metadata cache period | Seconds the merged index is reused before the members are checked for newer content; 0 disables caching (REPO-037 in [../repositories.md](../repositories.md)). | 7200 |

How the merged index is built (ordering, duplicate versions across members) is unspecified
(general rules: REPO-035, REPO-037 in [../repositories.md](../repositories.md)).

### PKG-APK-008 · RSA key pairs in the key store
Priority: **P2**

RSA key pairs are kept in the central key store (IAM-046 in
[../identity-and-access.md](../identity-and-access.md)) and assigned to Alpine repositories.

| Field | Meaning | Default |
|---|---|---|
| Key pair name | Unique name, referenced by repositories. | required |
| Alias | Short nickname. | required |
| Private key | PEM-encoded RSA private key. | required |
| Public key | PEM-encoded RSA public key. | required |
| Passphrase | Protects the private key. | optional |

<details>
<summary>Key pair operations</summary>

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Create or replace a key pair | name, type (RSA or GPG), alias, private key, public key, optional passphrase, optional reference to a key held in an external secret manager | 201, empty body; 400 for invalid key data | administrator |
| Update a key pair | same as create | updated summary (name, type, alias, public key) | administrator |
| Verify a key pair | same as create | plain-text result, for example "key was verified"; 400 when verification fails | administrator |
| List key pairs | none | array of summaries (alias, name, public key, type) | administrator |
| Get a key pair | key pair name | summary (name, type, alias, public key); 404 if unknown | any authenticated user, or anonymous when anonymous access is on |
| Delete a key pair | key pair name | `OK`; 404 if unknown | administrator |

</details>

- Typical keys are 2048-bit RSA keys generated with Alpine's `abuild-keygen`.
- Edge case: one infrastructure-as-code tooling path rejected PKCS#8 private keys with "RSA
  private key is of the wrong type"; generating the key in the traditional PKCS#1 PEM form
  (`openssl genrsa -traditional`) avoided it. An implementation should accept both PEM forms.
- Keys can be held in HashiCorp Vault instead of being uploaded (IAM-047); converting an uploaded
  key to a Vault-held key deletes the uploaded copy irreversibly.
- Keys must be transmitted byte-exact in request bodies (line breaks preserved).

UI location: security, key management, signing keys tab, "add keys", RSA keys.

### PKG-APK-009 · Public key for clients
Priority: **P2**

Clients download the repository's public key and store it under `/etc/apk/keys/`:

```sh
wget -O /etc/apk/keys/<key-file-name> "<repository-public-key-url>"
```

The download uses the operation that returns the public key assigned to a repository
(PKG-DEB-018 in [debian.md](debian.md)): readable by any authenticated user, or anonymously when
anonymous access is on. It returns the key as a plain-text attachment; clients should save it
under the server-supplied file name (`Content-Disposition`), which is what keeps the file name
consistent with the signature entry (PKG-APK-006). Saving the key under an arbitrary name works
only if the name happens to match.

### PKG-APK-010 · Unsigned operation with untrusted mode
Priority: **P2**

Repositories without an RSA key serve unsigned indexes. Clients then pass `--allow-untrusted` to
`apk`. With that flag, apk may print the warning "This apk-tools is OLD! Some packages might not
function properly."; it is expected and harmless when signature checks are off.

## Remote repositories

### PKG-APK-011 · Remote Alpine repositories
Priority: **P2**

A remote Alpine repository proxies the public Alpine package mirrors (for example
`https://pkgs.alpinelinux.org/packages`) or another repository manager, and caches what clients
request (RMT-001 in [../remote-repositories.md](../remote-repositories.md)).

- Upstream indexes are signed with the upstream's keys; to avoid installing those keys on every
  client, the remote repository is placed inside a virtual repository, which re-signs
  (PKG-APK-007).
- Index caching follows the general metadata retrieval cache period (RMT-018); an Alpine-specific
  rule is unspecified.

## Deploying and client setup

### PKG-APK-012 · Deploy Alpine packages
Priority: **P2**

```sh
curl -u <user>:<token> -X PUT \
  "https://repo.example.com/alpine-local/v3.20/main/x86_64/my-app-1.0.0-r1.apk" \
  -T my-app-1.0.0-r1.apk
```

- Plain HTTP `PUT` of the `.apk` file to `<repo-url>/<branch>/<repository>/<architecture>/<file>`
  (PKG-APK-002).
- Required permission: deploy on the target path (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).
- UI: the deploy dialog (UI-013 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-APK-013 · apk client configuration
Priority: **P2**

The client setup instructions (UI-021, UI-026 in [../ui-inventory.md](../ui-inventory.md)) add one
line per branch and repository to `/etc/apk/repositories`:

```text
https://<user>:<token>@repo.example.com/alpine-virtual/v3.20/main
```

- The line names branch and repository; apk appends the architecture itself.
- Credentials are embedded in the URL as HTTP Basic user information; the password part can be an
  access token (TOK-034 in [../access-tokens.md](../access-tokens.md)).
- Install and upgrade: `apk update`, then `apk add <name>` or `apk add <name>=<version>`;
  `apk add -u <name>` upgrades an installed package.
- The recommended source is a virtual repository.

### PKG-APK-014 · Companion CLI integration for apk
Priority: **P2**

The companion CLI (BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)) configures apk, injects
credentials, uploads packages and records build metadata.

Setup:

- writes `/etc/apk/repositories` with a line `<repo-url>/<alpine-version>/main/` when the Alpine
  version can be read from `/etc/alpine-release`, embedding the configured user and password or
  token in the URL;
- downloads the repository's RSA public key and stores it under `/etc/apk/keys/`;
- the repository can be chosen interactively (virtual first, then local or remote).

Install and upgrade through the CLI:

- runs the native `apk` with `HTTP_AUTH=basic:<host>:<user>:<password-or-token>` in the
  environment (host without scheme or path), instead of relying on credentials in the URL; an
  existing `HTTP_AUTH` value is kept unless credentials are given explicitly;
- with build name and number, records newly installed or upgraded packages as dependencies of a
  build module of type `apk`, with IDs `<name>:<version>` and a default module ID
  `<repo>:<arch>:<alpine-version>` (BLD-005, BLD-039);
- checksums come from the local package cache; when a repository key is given, missing checksums
  are filled from the server through the query language;
- every other native `apk` subcommand (for example `update`, `search`, `info`, `del`) runs through
  the CLI with injected credentials but without build metadata; build options on such a command
  only produce a warning.

Upload:

- uploads to `<repo-key>/<alpine-version>/<branch>/<arch>/<file>` with branch default `main` and
  architecture taken from the package's `.PKGINFO`, then the system architecture. The CLI's naming
  is shifted against PKG-APK-002: its "Alpine version" (for example `v3.20`) fills the branch
  coordinate and its "branch" (`main`, `community`) fills the repository coordinate;
- sets properties for operating system name (`alpine`), operating system version, architecture,
  package name and package version (keys are the implementation's choice);
- optionally records the uploaded file as a build artifact.

Edge cases:

- a short-lived token embedded in `/etc/apk/repositories` is never refreshed; native `apk` calls
  fail with permission errors after it expires, until setup is rerun;
- signature failures point the user to rerun setup so the current public key is installed.

## Notes for implementers

- Packages outside `<branch>/<repository>/<architecture>/` are silently left out of every index
  (PKG-APK-002); a deploy-time warning would save users time.
- apk matches the signature to a key file by name; serve the public key with a file name equal to
  the one inside `APKINDEX.tar.gz` (PKG-APK-006, PKG-APK-009).
- A virtual repository must sign with its own key and ignore member keys, otherwise clients would
  need every upstream key (PKG-APK-007).
- The index recalculation operation is asynchronous only; callers cannot wait for completion
  through it (PKG-APK-004).
- apk-tools older than 2.12 ignore `HTTP_AUTH`; for them, credentials must be in the repository
  URL (PKG-APK-014).
- Keys may arrive as PKCS#1 or PKCS#8 PEM; accept both (PKG-APK-008).
- Credentials embedded in `/etc/apk/repositories` expire with the token; client setup snippets
  should prefer long-lived read tokens for build images.
