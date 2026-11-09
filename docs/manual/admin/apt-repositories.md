# Debian (APT) repositories

This page explains how administrators set up repositories of the format Debian (APT): local repositories that build and sign their own indexes, remote repositories for the Debian and Ubuntu archives with the archive keys that make trove check them, the mirror mode with its choice of suites, components and architectures, virtual repositories that merge both into one signed source, the signing keys and their rotation, the index state and the rebuild, what a restore does, the configuration keys, and what this version does not support.

## What the format serves

The format **Debian (APT)** serves APT repositories in the Debian repository format, for `apt` and `apt-get` on Debian, Ubuntu and their derivatives. One format covers every distribution; a repository is not tied to one.

| Class | What it does | Address of a client's source line |
|---|---|---|
| Local | Holds the `.deb` files teams upload, builds the indexes of its suites and signs them with a key of its own | `https://trove.example.com/api/deb/<projectKey>/<key>/` |
| Remote | Caches an APT archive and passes its signed release files through unchanged; in mode **Mirror** also copies a selection of it in the background | the same |
| Virtual | Merges the indexes of local and remote members into one index per suite and signs it with a key of its own | the same |

How users configure APT, upload, install and what they see of a key rotation is in [Debian packages with APT](../user/apt-packages.md). The route family is `packages`; every repository of every project is reached on the same host, and no host binding is needed.

## Create a repository

Create it like any other ([Managing repositories](repositories.md#create-a-repository)): choose the tile **Debian (APT)**, then the class. Or through the API:

```bash
# example values: the project, the keys and the token variable
curl -X POST https://trove.example.com/api/v1/projects/web/repositories \
  -H "Authorization: Bearer $TROVE_TOKEN" -H 'Content-Type: application/json' \
  --data '{"key": "deb-local", "class": "local", "format": "deb"}'
```

A local or virtual repository gets its first signing key in the same transaction that creates it: an OpenPGP key that trove generates (EdDSALegacy over Ed25519, no expiry, user ID `trove <projectKey>/<key>`). There is no unsigned Debian repository and no key shared between repositories. A remote repository gets no key; it signs nothing.

## A local repository

The settings of the format are on the format tab of the repository (**Debian (APT) settings**, one entry per line) and in the member `deb` of the repository document:

| Setting | Document key | Default | Meaning |
|---|---|---|---|
| **architectures** | `deb.architectures` | `amd64`, `arm64` | Architectures every suite's indexes are built for, 1 to 32; the architectures of stored files are added. `all` is not listed: such files appear under every architecture and in `binary-all` |
| **compression** | `deb.compression` | none | Index variants besides gzip, which is always built; `xz` adds `Packages.xz` |
| **origin** | `deb.origin` | `trove` | The `Origin` of every `Release`, 1 to 64 printable ASCII characters without `:` |
| **label** | `deb.label` | the repository key | The `Label` of every `Release`, the same rule |

A change of any of them rebuilds every suite at once. What the shared settings mean for the format:

- **Package ownership** (`local.packageOwnership`) is `enforce` by default: the first upload of a package name records its owners, and from then on only they, holders of `manage` and administrators upload, move or delete it.
- **Include and exclude patterns** match binary package names, such as `acme-*`. A path below `dists/` belongs to no package, so a principal whose read is narrowed by package patterns can download package files but cannot read the indexes ([Permissions](permissions.md)).
- A **raw upload** through `/files/` or **Browse** is refused (`409`); a package goes in through `PUT …/upload/<file>` only. The generated index files under `dists/` cannot be written or deleted by anyone (`409 generated_item`). A raw delete of a package file runs the same rules as the format's `DELETE`.

**How an index is built.** Every upload, deletion, change of coordinates and key change marks its suites changed and queues the job `deb-index` of the repository after `deb.index_delay` (2 seconds by default), so a burst of uploads is one build. The build reads the rows of one database snapshot, never a package file, renders `Packages.gz` (and `Packages.xz`) per component and architecture and the `Release`, signs `InRelease` and `Release.gpg`, and publishes the whole suite in one transaction, so a client sees the previous generation or the new one, never a mixture. Every index file is also published by its SHA-256 (`by-hash`), so a client that read an older `InRelease` still gets the files it names, on any replica. The files of a superseded generation stay for `deb.by_hash_keep` (1 hour by default), and those of the three newest superseded generations whatever their age. The build runs on the replicas that claim jobs (the `worker` role, or `all` outside the chart); an installation without one publishes no index.

**Dates and clocks.** Every `Release` is dated a minute behind the database's clock and never earlier than the previous one, and every signature carries that time; a generated key's creation time is an hour back. APT refuses a release or a signature from its own future, so clients whose clock trails the database by up to 59 seconds work. Keep the clients and the database on synchronized time; [Debian packages with APT](../user/apt-packages.md#when-something-goes-wrong) lists the messages of a client that trails further.

## A remote repository for Debian or Ubuntu

A remote repository is a pull-through cache of an APT archive. The **Upstream URL** is the root of the archive, the directory that holds `dists/` and `pool/`:

| Archive | Upstream URL | Keyring on a client of that distribution |
|---|---|---|
| Debian | `https://deb.debian.org/debian/`, the default of the wizard | `/usr/share/keyrings/debian-archive-keyring.gpg`, package `debian-archive-keyring` |
| Ubuntu | `http://archive.ubuntu.com/ubuntu/` | `/usr/share/keyrings/ubuntu-archive-keyring.gpg`, package `ubuntu-keyring` |
| A flat repository | the directory above it, and the client names the flat directory as its suite (`binary/`); or the flat directory itself, and the client names `./` | the publisher's |

The cache periods of a Debian remote are 600 seconds for metadata (how long a release file is served before it is asked again) and 300 seconds for a remembered "not found". **Test connection** requests the upstream URL itself.

What a remote does with the archive:

- **Release files pass through byte for byte.** `InRelease`, `Release` and `Release.gpg` are the archive's own, signed by the archive. trove never re-signs a remote's files, so a client verifies the archive's signature with the archive's key, never with one of trove's.
- **Index files are one snapshot.** An index file is served only while it is the file the stored release lists, by its hash; a path the release does not list answers `404 deb_not_in_release` without asking the archive. By-hash files are fetched once and checked against the digest in their path.
- **Package files** are fetched on the first request and kept. When a parsed index of the archive lists the file's digest, the download is checked against it (`502 upstream_integrity_mismatch` and nothing stored otherwise). A file that no parsed index lists is stored unchecked under the checksum policy `verify-if-available` and refused under `require` (`502 upstream_digest_missing`). A job, `deb-remote-parse`, reads every `Packages` index the remote stores into rows; those rows give the digests, the package screens of the remote and the shadowing check of a first upload.
- Nothing is written through a remote (`405`).

### Upstream keys

Without keys trove stores what the archive sends and does not check it; the clients check it. With the archive's public keys in `deb.upstreamKeys`, trove checks every release before it stores it, so a damaged or forged release never reaches the cache. Give a remote its keys whenever you can, and always for a mirror, which needs them.

1. Export the archive keyring as ASCII armor on a machine of that distribution (`gpg` must be installed): `gpg --no-default-keyring --keyring /usr/share/keyrings/debian-archive-keyring.gpg --export --armor > debian-archive.asc` (Debian 13 also installs the file as `debian-archive-keyring.pgp`; for Ubuntu name the Ubuntu keyring). The block must be a `PGP PUBLIC KEY BLOCK`; the output of `gpg --enarmor` is refused.
2. Paste the block into **upstreamKeys** on the format tab, or set `deb.upstreamKeys` through the API. At most 256 KiB and 32 certificates; Debian 13's archive keyring (nine certificates, about 75 KB armored) fits.
3. Compare the fingerprints the repository shows read-only (**Fingerprints of the upstream keys**, `deb.upstreamKeyFingerprints`) with the ones the distribution publishes, through a channel you trust.

The save refuses a block that does not parse, holds secret key material, or holds a revoked, expired or non-signing key or an RSA key below 2048 bits (`422 invalid_value` at `/deb/upstreamKeys`, with the reason); remove such a certificate from the block. Once keys are set:

- A release is stored only when one of its signatures verifies with a key of the set that is valid now and was valid when it signed, with a hash of SHA-256 or stronger and a signature time not more than 10 minutes ahead of the database. Its `Suite` or `Codename` must be the requested suite and its `Valid-Until` must not have passed. A failure answers `502 deb_upstream_signature_invalid` or `502 deb_release_invalid`, keeps the stored copy, counts in `trove_deb_upstream_signature_failures_total{repo, reason}` and writes the security event `deb.upstream_signature_invalid` (at most once an hour per directory and replica). A failure that only a job meets, not a client, is logged and not counted.
- A release older than the newest one ever verified for its directory never replaces it, even when a key of the set signed it; it is counted in `trove_deb_release_rollbacks_total`.
- `signing-key.asc` of the remote serves the keys, which is what the configure snippet installs on a client.
- **When the archive starts signing with a new key** (Debian adds an archive key per release), add the new certificate before that happens: a release no key of the set verifies is refused with `502` until you add it, and clients fail their updates through the remote meanwhile.
- **Removing a key** takes effect with the save: the releases it verified stop counting as verified, and a job checks them again from their stored bytes against the remaining keys, without a request to the archive. One that no remaining key verifies is refused (`502 deb_upstream_signature_invalid` for the files of its directory) until a newly fetched release verifies. The removal is recorded in the revocation journal, so a restore removes the key again.
- A remote without keys cannot be a mirror (`422 deb_upstream_keys_required`), and its document carries the warning `deb_upstream_unverified`.

## The mirror mode

A remote in mode **Mirror** copies, in background passes, the suites, components and architectures you name, so `apt-get update` and `apt-get install` keep working when the archive is slow or unreachable. Set the mode in the **Mirror** section of the wizard or on the **Mirror** tab ([Mirror](repositories.md#mirror)); the selection is the format's own setting `deb.mirror`, on the format tab:

| Setting | Document key | Default | Meaning |
|---|---|---|---|
| **suites** | `deb.mirror.suites` | none | The suites below `dists/` to mirror, 1 to 32; required in mode mirror (`422 deb_mirror_suites_required`). An archive has no list of its suites, so it copies only what you name: `trixie`, `trixie-updates`, `noble`, `noble-security` |
| **components** | `deb.mirror.components` | `main` | Components whose indexes and package files are kept, 1 to 32 |
| **architectures** | `deb.mirror.architectures` | `amd64` | Architectures whose `Packages` indexes and package files are kept, 1 to 16; `all` is always kept and not listed |
| **translations** | `deb.mirror.translations` | `en` | Languages of `i18n/Translation-<language>` kept, at most 16 |
| **sources** | `deb.mirror.sources` | off | Whether `Sources` indexes and source package files are kept |

- **What a pass does.** It reads the release of each suite and uses it only while a key of the current set verifies it and its `Valid-Until` has not passed. It keeps each index file of the selection in every variant the release lists, by hash when the archive offers by-hash, reads the `Packages` indexes into rows and then stores the package files they list, each checked against the digest of its index entry. Every pass is a full pass; a second pass with nothing changed costs one conditional request per release.
- **The generic bounds.** **Include patterns** match package names, and the **Names list** discovery takes the include patterns without a wildcard as package names. **Versions per package** keeps that many versions of a name across the mirrored suites, newest first by Debian version order; **Include prerelease versions** has no effect, because Debian versions have no pre-release marker. **Storage limit in bytes** ends a pass that reaches it.
- **A bound for the Debian archive.** A mirror of `https://deb.debian.org/debian/` is refused (`422 mirror_unbounded`) unless every include pattern starts with a literal part or a storage limit is set: one component of one architecture of one Debian suite holds tens of thousands of packages. A mirror of any other archive without a bound is saved with a warning.
- **What stays out.** Other suites, components, architectures and languages, `Contents`, `dep11` and `cnf` files, pdiffs, installer indexes and `.udeb` files. A client that asks for one is served by pull-through while the archive is reachable and fails while it is not; a desktop client that fetches `dep11` metadata, or Ubuntu's `command-not-found` data, then sees `apt-get update` fail offline. Mirror what your clients' APT configuration asks for, or switch those index targets off on the clients.
- **Nothing is deleted.** Package files of older versions stay; only superseded index files leave after `deb.by_hash_keep`.

Only a holder of `repositories:write-config` sets the mode and changes `deb.mirror`; an Owner of the project without it may pause the mirror or turn it back to pull-through (`422 mirror_restricted` otherwise). The status of a pass, **Run a pass now** and the pass jobs are those of every mirror ([The status of a mirror](repositories.md#the-status-of-a-mirror)). A suite the archive does not have, an unverified or expired release and an index the release does not list are noted in the pass, but the status shows those notes only when the discovery was truncated; a misspelled suite shows as a pass with fewer names.

## A virtual repository

A virtual Debian repository is one APT source over local and remote Debian repositories of its own project. A job merges the members'
indexes into one index per suite and signs it with the virtual repository's own key, which it gets at its creation like a local
repository; nothing is merged at request time, so every client and replica reads the same signed generation. The wizard creates it
without members; set the members, in merge order, and the deployment target through the API:

```bash
# example values: the project, the keys, the ETag and the token variable
curl -X PATCH https://trove.example.com/api/v1/projects/web/repositories/apt \
  -H "Authorization: Bearer $TROVE_TOKEN" \
  -H 'Content-Type: application/merge-patch+json' \
  -H 'If-Match: "3"' \
  --data '{"virtual": {"members": ["deb-local", "debian"], "deployTarget": "deb-local"}}'
```

| Setting | Document key | Default | Meaning |
|---|---|---|---|
| **suites** | `deb.suites` | none | Suites served besides those of the local members, at most 32; a remote member contributes to every served suite its archive has |
| **components** | `deb.components` | `main` | The components merged, 1 to 32 |
| **architectures**, **compression**, **origin**, **label** | `deb.architectures`, … | as for a local repository | The architectures of the merged indexes (plus `binary-all`), `xz`, and the `Origin` and `Label` of the `Release` |

- **What a remote member must have.** Its `deb.upstreamKeys`: a remote without keys cannot become a member (`422 deb_member_unverified`), and the last key of a
  member cannot be removed. The virtual repository signs what it merges with trove's key, so it merges a remote member's release only while
  a key of the member's current set verified it, its `Valid-Until` has not passed and its `Date` is not older than the one the last build merged.
- **The merge.** Members in their order; for a package, version and architecture that two members list, the first member's paragraph wins. A name
  a local member has claimed loses every remote member's paragraph, which keeps a public package of an internal name out of the index. Every
  `Filename` names the member it came from (`members/<member>/pool/…`), so a client fetches each file from that member (`Trove-Resolved-From`).
- **When it builds.** At once when it is created or when its members or settings change; after a member's publication, at most every
  `deb.virtual_index_interval` (60 seconds by default); and when the refresh task finds the last check older than the merged-metadata period
  (`virtual.mergedMetadataTtlSeconds`). A change of claims has no trigger of its own and reaches the index at the next of these builds.
- **When a member cannot be merged** the build publishes nothing and the suite keeps its last generation with the state **failed** and the reason
  `member_unverified: <member>` (no current key verifies its stored release; after a key change the remote fetches and verifies anew),
  `member_expired: <member>`, `member_rollback: <member>` (an older release than the last one merged; to accept it on purpose, take the member out
  and add it back) or `member_unavailable: <member>`.
- **Reading.** The index of a virtual repository needs `read` on it and on every member without a package restriction; otherwise `403
  deb_members_unreadable` (an anonymous caller `401`). A package file through the member path needs `read` on the member and, for a remote member,
  its cache-fill action.
- **Writing.** An upload or a delete through the virtual repository goes to its deployment target; without one it is refused with `403`. The
  coordinates of a file are changed in the local member that holds it.

## Signing keys

The tab **Signing keys** of a local or virtual repository lists its keys with their fingerprint, state, type, origin and times, and the public certificate of each; no private key is ever shown. The same operations are in the API, below `/api/v1/projects/<projectKey>/repositories/<key>/deb/signing-keys`; listing needs `read`, every change `manage` on the repository (the screen offers the changes to holders of `repositories:write-config` and Owners of the project, a holder of `manage` through a policy alone uses the API):

| State | Signs | In `signing-key.asc` | Private key |
|---|---|---|---|
| **Primary** | yes | yes | sealed in the database |
| **Next** | yes, beside the primary | yes | sealed |
| **Withheld** | no | no | kept, sealed; set only by a restore |
| **Retired** | no | no | erased; the certificate stays as the record of what signed |

**Where the key lives.** The private key is stored in PostgreSQL, sealed with the stored-secret key ring (AES-256-GCM, bound to its row), and opened only by the index build for one signing call. It is never written to a log, an answer, an audit event, Valkey or the configuration export, and no API signs data a caller chooses. Backups carry the sealed key, so a restore with the key ring keeps the repository's identity. Whoever holds the key ring and a copy of the database can sign for every Debian repository: guard both like the signing keys they are.

### Rotate the key

APT cannot learn a new key from a repository; a client trusts a new key only once its key file is replaced. A rotation therefore has an overlap in which both keys sign:

1. **Create the next key**: **Generate a next key**, or `POST …/deb/signing-keys` with `{"generate": true}`. From the build that follows at once, every `InRelease` and `Release.gpg` carries a signature of each key, and `signing-key.asc` serves both. The panel shows since when both keys sign.
2. **Distribute**: have the clients run the key download of their configure snippet again, which writes both keys into their key file. trove cannot tell which clients did.
3. **Promote**: **Promote**, or `POST …/deb/signing-keys/promote`. The next key becomes the primary; the old one is retired, stops signing, leaves `signing-key.asc` and its private key is erased. A client that never replaced its key file fails `apt-get update` from then on.

To end an overlap without promoting, **Remove** the next key (`DELETE …/deb/signing-keys/<fingerprint>`). The primary key cannot be removed directly (`409 deb_key_state`), there is at most one next key, and a promotion without a next key answers `409 deb_no_next_key`. Promotions and retirements are recorded in the revocation journal, so a restore does not bring a retired key back into use.

### Import a key

For an archive whose clients already trust a key, **Import a key** (or `POST …/deb/signing-keys` with `{"privateKey": "<armored secret key>", "passphrase": "…"}`) makes it the next key; promote it afterwards. trove accepts an EdDSALegacy (Ed25519) or RSA primary key of at least 3072 bits that can sign, is not revoked, has **no expiry**, was created no later than the request, and has no signing subkeys; anything else answers `422 deb_key_unsupported`. A key with an expiry has to have it removed with GnuPG first; GnuPG then writes a fresh self-signature, and the builds wait until a signature can be dated after it, about a minute after the import. The passphrase is used once and not stored. A key that trove knows in any state, in this or another repository, answers `409 deb_key_in_use`, so a retired key cannot come back by import. The secret key passes through the browser when you use the screen; send the request with `curl` and the key read from a file to keep it out of a browser.

## The index state and the rebuild

The tab **Index** of a local or virtual repository shows each suite: the number of the last change (`changeSeq`), the number the published generation was built from, the generation, the release date, the keys that signed it, and whether the suite is **current**, **pending** or **failed** with the reason:

| Reason | Meaning | What to do |
|---|---|---|
| `no_signing_key` | The repository has no primary key (a restore withheld it); nothing new is published and the last generation stays served | Reinstate the withheld key, or create a next key and promote it |
| `key_too_young` | A key may not sign yet, because its binding self-signature is younger than the build's time | Nothing; the job is retried |
| `interrupted` | The build was stopped (a drain, a timeout) | Nothing; another worker builds from the rows |
| `build_error` | Any other failure | Look at the job in **System status › Jobs** and the log of the worker |
| `member_unverified`, `member_expired`, `member_rollback`, `member_unavailable` | A virtual repository could not merge the member named after the colon | See [A virtual repository](#a-virtual-repository) |

The same state is `GET …/deb/index`, with `?waitFor=<suite>:<changeSeq>` to wait for an upload's change (at most `deb.index_wait_max`); a virtual repository has no change number of its own and refuses `waitFor`.

**Rebuild all indexes** (`POST /api/v1/projects/<projectKey>/repositories/<key>/reindex`, `manage`) rebuilds every suite from the stored rows, drops suites without packages and removes rows of items that no longer exist; for a virtual repository it reads the members again. It answers `202` with the job; while another rebuild waits or runs it answers `409 reindex_running` naming it. A rebuild while an automatic build waits turns that build into the full one. A remote repository has no index of its own (`405 not_applicable`).

## After a restore

- A restore queues a full build of every local and virtual Debian repository; it runs when the workers start and its release dates are later than every earlier one, so clients take the rebuilt indexes.
- **A package file deleted after the restore point** is withheld: no index lists it, its pool path answers `404`, and an upload of the same bytes answers `409 deb_withheld`. To put it back, run `trove admin deb reinstate <projectKey>/<key> <path>`, which works only while the file is stored with the digest of the deletion; to confirm the deletion, delete the file again. A coordinate removed after the restore point is removed again; a file left without coordinates is withheld the same way.
- **A key retired or replaced after the restore point** is withheld: it neither signs nor is served, and its private key stays. Confirm the retirement (**Confirm retirement**, `DELETE …/deb/signing-keys/<fingerprint>`, which erases it) or **Reinstate** it (`POST …/deb/signing-keys/<fingerprint>/reinstate`: to primary when the repository has none, else to next). When the restore holds the new key of a promotion, the new key becomes the primary by itself. A key created after the newest backup is lost; a repository left without a primary key publishes nothing new until you act, and the replay output names it.
- **An upstream key removed after the restore point** is removed again. A mirror or a member of a virtual repository left without keys is disabled and listed as `suspended`.
- A backup made without remote caches (`trove backup create --exclude-remote-caches`, `backup.exclude.remote_caches`) restores no cached files and no parsed rows of a Debian remote, and so no record of the newest release each had verified: the first release a remote stores afterwards is taken as it is.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `deb.index_delay` | `2s` | Wait after the first change of a suite before its build starts, 1 s to 60 s. The time from an upload to its published index is about this plus 5 s while Valkey is available |
| `deb.by_hash_keep` | `1h` | How long the files of a superseded generation stay served by hash, 10 m to 168 h |
| `deb.index_wait_max` | `30s` | The longest wait of an index state read with `waitFor`, 1 s to 120 s |
| `deb.virtual_index_interval` | `60s` | The least time between two builds of a virtual repository that a member's publication triggers, 10 s to 1 h |
| `maintenance.tasks.deb-index-refresh` | `{enabled: true, schedule: "* * * * *"}` | Queues the builds, the retention and the housekeeping of remotes that are due; deletes nothing itself |

The complete reference is in the [full reference](https://github.com/guided-traffic/trove/blob/main/README.md#-full-reference). What the format records:

- Metrics: `trove_deb_index_builds_total{class, outcome}`, `trove_deb_index_build_seconds` and `trove_deb_index_delay_seconds` for the builds, by the class of the repository (`local` or `virtual`); `trove_deb_upstream_signature_failures_total{repo, reason}`, `trove_deb_release_rollbacks_total{repo}` and `trove_deb_unverified_fills_total{repo}` for the remotes.
- Audit events: `deb.package.deleted`, `deb.coordinates.removed`, `deb.signing_key.promoted` and `deb.signing_key.retired` (recorded in the revocation journal), `deb.coordinates.changed`, `deb.signing_key.created`, `deb.signing_key.reinstated`, `deb.reinstate`, and the security event `deb.upstream_signature_invalid`.

## Not supported in this version

- **No nested virtual repositories**: the members of a virtual Debian repository are local and remote repositories.
- **A change of claims** reaches a virtual repository's index at its next build, not at once.
- **Source packages** cannot be uploaded and local repositories build no `Sources`; `deb-src` works through a remote.
- **No `dput` uploads**, no `.udeb` installer packages and indexes, no debug-symbol packages, no `Contents` or translation files for local repositories.
- **No `Valid-Until`** in trove's own releases: a party between a client and trove that keeps serving the generation the client already holds can keep it on old packages (it cannot roll the client back). TLS to trove prevents that.
- **No snapshots** of a suite, and no copy or move of a Debian package between repositories.
- **Signing keys stay in trove**; there is no external key service or HSM.
- **Endpoints with exposure rules** and **the trove Bastion** do not serve Debian repositories; use an endpoint with `exposure: all`.
- **No mirror of a flat repository**; flat upstreams are served by pull-through.
- **No vulnerability matching** of Debian packages.
- The metric `trove_deb_remote_parse_total` of the parse runs is not counted yet.

[Known limitations](limitations.md) lists the limits of the other areas.
