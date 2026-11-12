# Debian packages with APT

How to point apt and apt-get on Debian and Ubuntu at a trove repository with its signing key and a credential file, upload a package with curl, list it in suites and components, install it, and what a key rotation and a wrong clock look like on the client. The Client setup page and the How to use tab give the same commands with the addresses of your installation.

## The address and what APT reads

trove serves APT repositories in the Debian repository format, from repositories of the format **Debian (APT)**. One format serves Debian, Ubuntu and their derivatives; the client is `apt` or `apt-get` (the setup lists APT 2.4, 2.6, 2.7 and 3.0). A source line names the address of the repository and a suite:

| Part | Example | Rule |
|---|---|---|
| Address | `https://trove.example.com/api/deb/web/deb-local` | the base URL, `/api/deb/`, the project key and the repository key |
| Suite | `stable` | the `distribution` a package was uploaded to; lower-case letters, digits, `.`, `_`, `+` and `-`, at most 64 characters |
| Component | `main` | the same rule |

APT adds `dists/<suite>/InRelease` and the index files for `apt-get update`, and the `Filename` of an index entry, a path below `pool/`, for `apt-get install`. trove builds those index files itself and signs them with a key that belongs to this one repository. Your client trusts that key for this source and for nothing else on the installation.

## Configure APT

The Client setup page, with **Package type** Debian (APT) and **Client** **APT (apt, apt-get)**, renders this for the repository you choose; the suite is `stable` unless you change it. The files are named after the project and the repository, so two repositories never share a key file:

```shell
# example: the repository deb-local of the project web, suite stable
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL -u trove:"${TROVE_TOKEN}" https://trove.example.com/api/deb/web/deb-local/signing-key.asc \
  | sudo tee /etc/apt/keyrings/trove-web-deb-local.asc >/dev/null
sudo tee /etc/apt/sources.list.d/trove-web-deb-local.sources >/dev/null <<'SOURCES'
Types: deb
URIs: https://trove.example.com/api/deb/web/deb-local/
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/trove-web-deb-local.asc
SOURCES
sudo install -m 0600 /dev/null /etc/apt/auth.conf.d/trove-web-deb-local.conf
printf 'machine %s\nlogin %s\npassword %s\n' trove.example.com/api/deb/web/deb-local/ trove "${TROVE_TOKEN}" \
  | sudo tee /etc/apt/auth.conf.d/trove-web-deb-local.conf >/dev/null
```

| File | What it holds |
|---|---|
| `/etc/apt/keyrings/trove-<project>-<repository>.asc` | the public keys of the repository, downloaded from `signing-key.asc` |
| `/etc/apt/sources.list.d/trove-<project>-<repository>.sources` | the source in the deb822 form, with `Signed-By` naming the key file |
| `/etc/apt/auth.conf.d/trove-<project>-<repository>.conf` | the host and path, the user name and the token, readable by root only (mode `0600`) |

- **The token** is a [personal access token](access-tokens.md), a service-account token or, in GitLab CI, the token of the [token exchange](gitlab-ci-pipelines.md). The snippet reads it from `TROVE_TOKEN` (the installation can name another variable), which must be set in the shell that runs the snippet. The user name is recorded and ignored; the snippets use `trove`.
- **Never put the token into the source line.** A source line ends up in world-readable files, in the output of `apt-get update` and in logs; APT reads credentials from `auth.conf.d` for exactly that reason.
- **Nothing turns verification off.** The snippets use no `trusted=yes`, no `allow-insecure` and no `apt-key`, and you need none of them.
- **The one-line form.** For systems that keep sources in `sources.list.d/*.list`, the page also shows `deb [signed-by=/etc/apt/keyrings/trove-web-deb-local.asc] https://trove.example.com/api/deb/web/deb-local/ stable main` for `/etc/apt/sources.list.d/trove-web-deb-local.list`, once the key file is installed.
- **Anonymous reads.** A repository whose artifacts are **Public** needs no credential to read: the key download has no `-u` and no credential file is written. Uploading always needs a token.
- **Compare the key.** Before you trust a key file, you can compare its fingerprints with the ones the section **Signing keys** of the repository's settings shows, through a channel you trust.

Reading the index needs the read permission on the repository without a package restriction: an index lists every package of a suite, so a token narrowed to some package names can download those package files but cannot use the repository as an APT source (`404` on `dists/<suite>/InRelease`).

### Your own certificate authority

When trove is served with a certificate of your own authority, APT must trust it: name the CA certificate in `Acquire::https::CAInfo` (a file below `/etc/apt/apt.conf.d/`) or install it in the system store. Without it `apt-get update` ends in a certificate error. Do not turn the peer verification off instead.

## Upload a package

A package file goes to a local repository with one request: `PUT` to `upload/<file>` with the `.deb` as the body and the suite in the query. It needs the deploy permission on the package name, and its ownership:

```shell
curl -fsS -u trove:"${TROVE_TOKEN}" --upload-file ./hello_2.10-3_amd64.deb \
  'https://trove.example.com/api/deb/web/deb-local/upload/hello_2.10-3_amd64.deb?distribution=stable&component=main'   # example
```

- `distribution` is required; `component` is `main` when you leave it out. Both may repeat and may hold comma-separated values, at most 16 of each, and the file is listed in every combination: `distribution=stable,testing&component=main&component=contrib` gives four.
- The file name in the address must end in `.deb` and is otherwise only informational. trove names the stored file itself, below the pool directory of the source package: `pool/h/hello/hello_2.10-3_amd64.deb`, or `pool/libx/libxml2/…` for a source whose name starts with `lib`.
- The package name, version and architecture come from the control file inside the package, never from the request. A package whose control data trove could not list in an index is refused, not stored.
- The answer is `201` for a new file and `200` when the identical bytes are stored already; then only the suites and components of the request are added. The body names the stored path, the package, the version, the architecture, the SHA-256, the coordinates and, per suite, `changeSeq` and `publishedSeq`.
- A package, version and architecture is stored once: other bytes under the same identity answer `409 deb_version_exists`, also for a holder of the overwrite permission. Delete the file first, or build the changed package with a new version.

The first upload of a package name records its owners; after that only the owners, holders of the manage permission and administrators upload, move or delete that name, unless the repository turned package ownership off.

### Wait until the index lists it

The upload answers as soon as the file is stored; the index is built afterwards by a job, usually within a few seconds. A script that installs right after an upload waits for the index with the `changeSeq` of the answer:

```shell
curl -fsS -u trove:"${TROVE_TOKEN}" \
  'https://trove.example.com/api/v1/projects/web/repositories/deb-local/deb/index?waitFor=stable:42'   # example: 42 is the changeSeq of the answer
```

The answer comes when the suite's `publishedSeq` has reached the number, or after at most 30 seconds (the installation's `deb.index_wait_max`), with the state either way. Each suite shows `state` `current`, `pending` (a build is due) or `failed` with a `reason`. Several suites are separated by commas: `waitFor=stable:42,testing:9`.

## Suites and components

A package file is listed in one or more suites and components, its **coordinates**. Uploads add coordinates; the coordinates operation replaces them, which is how a package moves from `testing` to `stable` without being uploaded again:

```shell
curl -fsS -u trove:"${TROVE_TOKEN}" -X PUT -H 'Content-Type: application/json' \
  --data '{"path": "pool/h/hello/hello_2.10-3_amd64.deb", "coordinates": [{"distribution": "stable", "component": "main"}]}' \
  https://trove.example.com/api/v1/projects/web/repositories/deb-local/deb/coordinates   # example
```

- An added coordinate needs the deploy permission, a removed one the delete permission, both on the package and with its ownership.
- An empty list is refused; delete the file instead. A repository holds at most 64 suites, a suite at most 32 components, and a file at most 32 coordinates.
- A removed coordinate is recorded like a deletion: a restore of trove from a backup removes it again.
- A package of architecture `all` is listed under every architecture of the suite and in `binary-all`.
- A suite exists while a file is listed in it. A suite whose last file was removed keeps empty indexes until an administrator rebuilds the repository.

## Install

```shell
sudo apt-get update && sudo apt-get install hello          # example
sudo apt-get install hello=2.10-3                          # example: one exact version
```

`apt-get update` fetches `InRelease` of the suite, checks its signature against the key file, and fetches every index file by its SHA-256, so the files it gets are the ones that `InRelease` names, whichever replica answers. Use `--error-on=any` in scripts, so that a warning fails the run.

APT installs the highest version among all of its sources, so a package name that another source also offers is decided by version, not by the order of the source lines.

### Through a remote repository

A remote repository is a pull-through cache of an APT archive, such as `https://deb.debian.org/debian/` or Ubuntu's archive. Its release files are the archive's own, byte for byte: trove signs nothing of a remote. The key your client trusts is therefore the archive's:

- **The repository has upstream keys** (an administrator gave it the archive's public keys): trove checks every release it stores against them, and `signing-key.asc` of the remote serves those keys. The configure snippet above installs them; compare their fingerprints with the ones the repository's page shows.
- **The repository has none**: trove stores what the archive sends without checking it, and your client checks it. The Client setup page then shows the snippet with the archive's own keyring, `/usr/share/keyrings/debian-archive-keyring.gpg` (package `debian-archive-keyring`) or, on Ubuntu, `/usr/share/keyrings/ubuntu-archive-keyring.gpg` (package `ubuntu-keyring`).

Any suite the archive has works without configuration: `stable`, `bookworm`, `noble-updates`, a flat directory such as `binary/`, or `./` for a repository whose address is the flat directory itself. Nothing can be uploaded to a remote repository.

### Through a virtual repository

A virtual repository is one source for several local and remote Debian repositories of a project, with one key file and one credential file.
Its index is the merge of the members' indexes, signed with the virtual repository's own key: install that key with the configure snippet of the
virtual repository, never a member's or the archive's. Keep in mind:

- Reading the index needs the read permission on the virtual repository and on every member; otherwise `apt-get update` gets `403`
  (`deb_members_unreadable`). Each package file comes from the member whose index entry you read; the answer header `Trove-Resolved-From` names it.
- A package that two members list in the same version comes from the first member in the order. A name a local member has claimed is never taken
  from a remote member.
- An upload through the virtual repository goes to its deployment target, a local member; a virtual repository without one refuses uploads with
  `403`. The index wait (`waitFor`) is read on that local member, not on the virtual repository.
- A new upload reaches the virtual repository's index after the member published it, within about a minute (the installation's
  `deb.virtual_index_interval`). The section **Indexes** of the virtual repository's settings shows a suite that could not be built, with the member that stopped it.

## Delete a package

`DELETE` of the pool path removes a file; it needs the delete permission and the ownership of the package:

```shell
curl -fsS -u trove:"${TROVE_TOKEN}" -X DELETE \
  https://trove.example.com/api/deb/web/deb-local/pool/h/hello/hello_2.10-3_amd64.deb   # example
```

The next index no longer lists it, and `apt-get install hello=2.10-3` reports that the version was not found. The file goes to the trash can unless it is purged; restoring it from the trash can lists it again. A deletion is recorded for a restore of trove from a backup: a file deleted after the backup stays withheld after that restore (its pool path answers `404` and an upload of the same bytes `409 deb_withheld`) until an administrator puts it back.

## When the key changes

APT has no way to learn a new key from a repository; a key change reaches your client only when its key file is replaced. A rotation therefore has an overlap in which two keys sign every index:

1. An administrator creates the repository's **next key**. From the next build every index is signed by the old and the new key, and `signing-key.asc` serves both.
2. You replace the key file: run the key download of the configure snippet again, which writes both keys into the file.
3. The administrator **promotes** the next key. From then on only the new key signs.

| Your key file holds | Before step 1 | During the overlap | After step 3 |
|---|---|---|---|
| The old key only | `apt-get update` works | works | fails: the signature cannot be verified |
| Both keys (replaced during the overlap) | | works | works |
| The new key only | fails | works | works |

A client that missed the overlap fails `apt-get update` after the promotion instead of accepting an index signed by a key it does not trust. That is what APT is built to do; download `signing-key.asc` again. trove cannot tell which clients still hold only the old key, so ask the administrator when the promotion is planned.

## When something goes wrong

APT prints the status line of a failed request and no body; `curl` shows the body, which carries a `code`.

| Message or answer | Cause and remedy |
|---|---|
| `The following signatures couldn't be verified because the public key is not available: NO_PUBKEY <key id>` (Debian 12, Ubuntu); on Debian 13 a message of `sqv` that names the missing key | The key file in `Signed-By` is not this repository's: another repository's, an old file after a promotion, or, for a remote, trove's instead of the archive's. Download `signing-key.asc` again; for a remote without upstream keys use the archive's keyring |
| `Release file … is not valid yet` (Debian 12, Ubuntu) or `OpenPGP signature verification failed … created after the --not-after date … is not signed` (Debian 13) | The client's clock is behind trove's. trove dates every `Release` and signature a minute behind the database clock, so a clock that trails by up to 59 seconds works; from about 60 seconds behind Debian 13 fails, from about 70 seconds the clients that verify with `gpgv` (Debian 12, Ubuntu 22.04 and 24.04) fail. Both thresholds follow from the clients' limits and were not measured. Correct the time of the client (NTP, `systemd-timesyncd`); do not turn verification off |
| `401 Unauthorized` on `apt-get update` | No credential file, a revoked or expired token, or a `machine` line that does not match the address of the source |
| `404 Not Found` for `dists/<suite>/InRelease` | Nothing was uploaded to the suite yet, the first build is still due, or the token may read single packages only and not the index |
| `404` for a package right after its upload | The index is built after the upload answered; wait with `waitFor` as above |
| `Hash Sum mismatch` | Through a remote whose archive offers no by-hash files, the client read an older release than the one trove holds now. Run `apt-get update` again |
| `E: Version '2.10-3' for 'hello' was not found` | The version is not in the index of the suite: it was deleted, it is listed in another suite, or the index has not caught up with the upload |
| `400 deb_distribution_required`, `400 deb_invalid_coordinate`, `400 deb_invalid_file_name` | The upload names no `distribution`, a suite or component breaks the rule above, or the file name does not end in `.deb` |
| `409 deb_version_exists` | Other bytes are stored for this package, version and architecture |
| `409 deb_filename_conflict` | Another version of the package has the same file name, because the versions differ only by their epoch |
| `409 deb_withheld` | The bytes were deleted before a restore and are withheld until an administrator reinstates them |
| `413 payload_too_large` | The control archive of the package exceeds a bound (16 MiB compressed, 64 MiB unpacked, 1,000 entries) |
| `422 deb_invalid_archive`, `deb_invalid_control`, `deb_unsupported_compression` | The file is no Debian package; its control data breaks a rule (the answer names the field, for example `Package: Hello` with an upper-case letter); its control archive is compressed with something other than none, gzip, xz or zstd |
| `502 upstream_integrity_mismatch` | Through a remote: the archive sent a package file whose digest is not the one its index lists. Nothing was stored |
| `403 deb_members_unreadable` | Through a virtual repository: the token may not read every member. Ask for read on the members, or use a member directly |
| `502 deb_upstream_signature_invalid`, `502 deb_release_invalid` | Through a remote with upstream keys: the archive's release is not signed by one of them, or it is malformed or dated ahead. The copy trove had stays; tell an administrator |

Give the `X-Request-Id` of the answer to an administrator when you need help ([Errors and troubleshooting](troubleshooting.md#give-the-request-id-to-an-administrator)).

## What is not available

- Source packages cannot be uploaded; `deb-src` lines work only through a remote repository.
- `dput` uploads of a signed `.changes` file, `.udeb` installer packages and debug-symbol packages are not supported.
- An endpoint with exposure rules (a customer endpoint) does not serve Debian repositories, and neither does the trove Bastion.
- trove does not match Debian packages against vulnerability data.
