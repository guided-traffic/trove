# Debian (APT) repositories

The Debian format (`internal/formats/deb`) serves APT repositories for Debian, Ubuntu and every archive in the Debian repository format.
This page describes the part that is built: the skeleton of phase T-17 ("WP0, contracts": the contract additions, the ports, the schema, the
configuration keys, the API document), the package-file and deb822 libraries (WP1) and the local class (WP3: upload, coordinates, deletion,
the serve check, the package screens, the setup and the guide). A route whose work package is not built answers `501 deb_not_implemented`. The specification is [formats/debian.md](../spec/formats/debian.md); the plan and the cut into work packages are
[apt-work-packages.md](../projektplan/apt-work-packages.md); the decisions for the contract are
[ADR-170](../decisions.md#adr-170--apt-indexes-are-built-by-a-job-and-published-through-the-item-stores-publication-port-as-generated-items-also-in-virtual-repositories),
[ADR-171](../decisions.md#adr-171--every-debian-local-and-virtual-repository-has-its-own-eddsalegacy-signing-key-generated-by-trove-and-sealed-with-the-stored-secret-key-ring-rotation-by-a-co-signing-next-key-the-journal-withholds-keys-and-never-erases-them)
and [ADR-172](../decisions.md#adr-172--the-format-contract-gains-the-remote-name-index-and-the-serve-facts-kinds-deb_local-and-deb_remote).

## What has to be in your head first

- **One format, `deb`, for Debian and Ubuntu** (Q-DEB-1), at `/api/deb/<projectKey>/<repoKey>/`. An APT source names that URL and a suite; APT
  appends `dists/<suite>/…` for indexes and the `Filename` of an index entry for package files.
- **Indexes are generated items, built by a job.** `deb-index` renders, compresses and signs the indexes of a suite from one snapshot and
  publishes them through `items.Publisher` as package-less generated items, so every replica serves the same signed bytes (ADR-170). Nothing of this
  is built yet: the port ([publish.go](../../internal/items/publish.go)) and the job handler ([build.go](../../internal/formatdata/deb/build.go))
  answer `not_implemented`.
- **A key belongs to one repository** (ADR-171). The private key is the sealed column `deb_signing_keys.private_key`; `secrets-reencrypt`
  walks it already ([reseal.go](../../internal/formatdata/deb/reseal.go)), so registering the column never stops a key-ring rotation.
- **The format has no package document.** `PackageDocumentPath` reports `ok` false; a remote answers the shadowing check from its parsed index rows
  through the optional `NameServer` (ADR-172, Q-REPO-270). The conformance kit takes this with `Fixture.NoDocument`.
- **One route per path shape, not per class** (Q-DEB-45): `Match` does not know the class of the repository, so `deb.dists.get`, `deb.dists.by_hash`
  and `deb.pool.get` serve every class and the handler decides what a class serves.
- **Keys cross the port as plain data** (armored bytes, fingerprints, times); the contracts import no OpenPGP library.

## The routes

[routes.go](../../internal/formats/deb/routes.go) is the whole table of R-DEB-003, written once; a work package registers the handler of a route by its
name ([handlers.go](../../internal/formats/deb/handlers.go), `registerHandler` in an `init`), and a route without one answers 501.

| Route | Method | Classes | Kind | Path |
|---|---|---|---|---|
| `deb.dists.get` | GET | all | Metadata | `dists/<suite>/…`: release documents, indexes, and for a remote any other file of any suite name |
| `deb.dists.by_hash` | GET | all | Download | `dists/<suite>/<dir>/by-hash/SHA256/<hex>` |
| `deb.pool.get` | GET | all | Download | `pool/…` |
| `deb.pool.delete` | DELETE | local | Delete | `pool/…` |
| `deb.members.get` | GET | virtual | Download | `members/<memberKey>/…` |
| `deb.flat.release`, `deb.flat.index`, `deb.flat.file` | GET | remote | Metadata, Metadata, Download | a flat repository outside `dists/`, `pool/`, `members/` and `upload/`; a leading `./` is the root |
| `deb.signing_key.get` | GET | all | Computed | `signing-key.asc` |
| `deb.upload.put` | PUT | local | Upload | `upload/<file>` |

A route of a class the format has not registered is left out of `Format.Routes()`. A class registers in a `class_*.go` file (`registerClass` in an `init`,
[format.go](../../internal/formats/deb/format.go)); only [class_local.go](../../internal/formats/deb/class_local.go) exists.

## Files

| File | Responsibility |
|---|---|
| [format.go](../../internal/formats/deb/format.go) | `Format`, `Descriptor`, `New`, the registry of classes (`registerClass`) |
| [class_local.go](../../internal/formats/deb/class_local.go), [class_remote.go](../../internal/formats/deb/class_remote.go) | Register the local and the remote class; `class_virtual.go` is created by its package |
| [names.go](../../internal/formats/deb/names.go) | The grammars of R-DEB-002 (`ValidPackage`, `ValidVersion`, `ValidArchitecture`, `ValidSuite`) and the package-name rule |
| [paths.go](../../internal/formats/deb/paths.go) | The pool layout (`PoolPath`, `PoolPrefix`, `PackagesPath`, `ByHashPath`), `Classify`, `IsIndexName`; `PackageDocumentPath` reports no document |
| [routes.go](../../internal/formats/deb/routes.go) | The route table and its matchers; `Match` reads a leading `./` as the flat root (Q-DEB-39) |
| [handlers.go](../../internal/formats/deb/handlers.go) | The registry of handlers by route name and the 501 default |
| [errors.go](../../internal/formats/deb/errors.go) | `WriteError`: the problem details of R-ARCH-023 with the codes of R-DEB-005 |
| [settings.go](../../internal/formats/deb/settings.go), [settings.schema.json](../../internal/formats/deb/settings.schema.json) | The `deb` member of a repository document (R-DEB-008, R-DEB-024), its schema and `SettingsOf` with the defaults |
| [servecheck.go](../../internal/formats/deb/servecheck.go) | `ServeCheck` (a withheld package file is 404) and `ServeFactsKey`: `deb_local` for a pool path of a local repository, `deb_remote` for a file of a remote |
| [inspect.go](../../internal/formats/deb/inspect.go) | `Inspect` and `readPackage`: the package is read through `debfile` and its control file checked against the grammars of R-DEB-002 (a violation is 422 `deb_invalid_control` naming the field); `properties` are the read-only properties of R-DEB-007 |
| [store.go](../../internal/formats/deb/store.go), `store_*.go` | The data port: `Store` embeds one group per package (`LocalStore`, `KeyStore`, `BuildStore`, `RemoteStore`, `MirrorStore`, `VirtualStore`, `ViewStore`, `JournalStore`) |
| [rows](../../internal/formats/deb/rows/rows.go) | The values that cross the port (a leaf) |
| [../formatdata/deb](../../internal/formatdata/deb/port.go) | The implementation: `port.go` composes one stub per group (`local_store.go`, `keys_store.go`, `build_store.go`, `remote_store.go`, `mirror_store.go`, `virtual_store.go`, `view_store.go`, `journal_store.go`), `tables_deb.go` registers the tables, the revision columns and the sealed column, `jobs.go` the kinds and the task, `build.go`, `remote_parse.go`, `refresh.go` the handlers (stubs), `reseal.go` the key-ring walk |
| [untrusted/{ar,xz,zstd}.go](../../internal/formats/untrusted/ar.go) | `NewAr` (the bounded `ar` reader: at most the member count of the limits, a member is opened by a section reader, never extracted), `NewXZReader` and `NewZstdReader` (bounded, failing instead of cutting; Q-ARCH-382) |
| [servefacts](../../internal/servefacts/servefacts.go), `files.sql` | The kinds `deb_local` and `deb_remote` and the columns the item lookup returns for them (Q-ARCH-381) |
| [items/publish.go](../../internal/items/publish.go) | `Publisher`, `GeneratedFile`, `ErrSuperseded`; `Store.Publish` answers `not_implemented` |
| [formathost/background_upstream.go](../../internal/formathost/background_upstream.go), [name_servers.go](../../internal/formathost/name_servers.go) | `Host.BackgroundUpstream` (built: a job run of one remote repository fetches under the pass's egress, credential, share and throttling rules, without a pass; Q-REPO-272, [mirror.md](mirror.md)) and the virtual engine's `NameServers` (undetermined until the remote class lands; Q-REPO-270) |
| [repository/create_hooks.go](../../internal/repository/create_hooks.go) | `RegisterCreateHook`: rows of a format's port in the transaction that creates its repository (Q-REPO-273); nothing is bound yet |
| [api/v1/deb_keys.go](../../internal/api/v1/deb_keys.go), [deb_index.go](../../internal/api/v1/deb_index.go), [reindex.go](../../internal/api/v1/reindex.go) | The management operations of the signing keys, the index state and the reindex |
| [api/v1/deb_coordinates.go](../../internal/api/v1/deb_coordinates.go), [app/wire_deb_api.go](../../internal/app/wire_deb_api.go) | `PUT …/deb/coordinates` (R-DEB-009): the handler resolves the repository and the caller; `debCoordinates` in the composition root decides deploy and delete per side, enforces ownership and writes in one transaction (Q-DEB-48) |
| [audit/events_deb.go](../../internal/audit/events_deb.go) | The audit events; four are journaled |

## Tables, migrations and the sealed column

Migrations [00060_deb](../../internal/meta/postgres/migrations/00060_deb.sql), [00061_deb_signing_keys](../../internal/meta/postgres/migrations/00061_deb_signing_keys.sql),
[00062_deb_remote](../../internal/meta/postgres/migrations/00062_deb_remote.sql) and [00063_deb_binaries_detached](../../internal/meta/postgres/migrations/00063_deb_binaries_detached.sql)
are expand steps; every table is export class `data`
([tables_deb.go](../../internal/formatdata/deb/tables_deb.go)). The remote tables (00062 and [00064_deb_remote_files](../../internal/meta/postgres/migrations/00064_deb_remote_files.sql)) are cache rows; the purge that a restore without remote caches
needs is `PurgeRemoteCaches` ([restore_purge.go](../../internal/formatdata/deb/restore_purge.go), R-BAK-008, H-630). `test/integration/postgres/deb_test.go` pins the constraints, the registrations and the item lookup's
facts; `deb_reseal_test.go` pins the key-ring walk.

## Configuration

`deb.index_delay` (2s, 1s to 60s), `deb.virtual_index_interval` (60s, 10s to 1h), `deb.by_hash_keep` (1h, 10m to 168h) and `deb.index_wait_max`
(30s, 1s to 120s); [internal/config/deb.go](../../internal/config/deb.go) and [configuration.md](configuration.md). The per-repository settings are the
`deb` member of the repository document.

## The libraries (WP1)

| Package | Responsibility |
|---|---|
| [formats/deb/version](../../internal/formats/deb/version/version.go) | `Parse`, `Compare`, `Less`, `Equal`: dpkg's order (epoch, then upstream and revision by alternating runs; `~` sorts before everything). A leaf; `FuzzCompare` holds that the order is total |
| [formats/deb/control](../../internal/formats/deb/control/control.go), [parse.go](../../internal/formats/deb/control/parse.go) | Strict deb822: `Reader` reads paragraphs of a stream (one comment style for indexes), `Parse` reads exactly one paragraph. A field name outside the grammar, a continuation line without a field, a CR, a duplicate field, more than 512 fields or a field of more than 1 MiB is an `*Error`; `Refusal(err)` maps it to 422 `deb_invalid_control` naming the field. `Paragraph.Without/With/Bytes` produce the normalized paragraph the index entry is built from |
| [formats/deb/debfile](../../internal/formats/deb/debfile/debfile.go) | `Read(ra, size, limits)`: the `ar` container (`debian-binary` first, then `control.tar.*`, then `data.tar.*`; at most 8 members), the control archive decompressed with the bounds of R-DEB-034 (16 MiB compressed, 64 MiB decompressed, 1,000 entries, ratio 100:1) and its `control` file. The data archive is never decompressed. `Package` holds the identity and the paragraph |
| [formats/untrusted](../../internal/formats/untrusted/ar.go) | `NewAr` and the member readers the two above sit on |

Each has a fuzz target (`FuzzCompare`, `FuzzParse`, `FuzzAr`, `FuzzRead`). The crafted inputs of R-DEB-034 (a control archive that expands past
its bound, 1,001 entries, a ninth member, 600 fields, a field of 2 MiB, a body that is no `ar` archive) are in
[crafted_test.go](../../internal/formats/deb/crafted_test.go) and the kit's inspect check holds the format to a 413 or 422 for each.

## The local class (WP3)

**Upload.** `PUT upload/<file>?distribution=&component=` ([handlers_upload.go](../../internal/formats/deb/handlers_upload.go)). The body is
spooled once; the package is read outside any transaction; the permission, the ownership, the withheld check, the
immutability check (`deb_version_exists`), the file-name check (`deb_filename_conflict`) and the shadowing check run before the commit. The
item is committed at `pool/<prefix>/<source>/<package>_<version without epoch>_<arch>.deb` with the payload `uploadCommit`;
`OnCommit` ([hooks.go](../../internal/formats/deb/hooks.go)) writes the package and version rows, the file's row and its coordinates
and the properties in the commit's transaction. The same bytes uploaded again answer 200 and add the coordinates of the request.

**Coordinates.** `deb_suites.change_seq` is the revision of a suite: it rises with every upload, deletion, restore and change of
coordinates that affects the suite, and a new suite starts at 1 (the schema default), so it is not raised again by the upload that creates it.
The bounds (32 coordinates per file, 64 suites per repository, 32 components per suite) are checked in the transaction after the rows
are written (`checkBounds` in [local_store.go](../../internal/formatdata/deb/local_store.go)); a crossing rolls the write back.

**Deletion.** `DELETE pool/<file>` and every other removal (items API, `/files/`, trash) end in `OnItemsRemoved`. The item store has
deleted a permanently removed item before the hook runs, so `deb_binaries` no longer references `items` (migration
[00063](../../internal/meta/postgres/migrations/00063_deb_binaries_detached.sql)): the hook reads the row, raises the suites, deletes the row
with a purge and writes `deb.package.deleted` with its journal entry. A file in the trash can keeps its rows; `OnItemsRestored` raises the
suites again.

**Journal replay.** A restore replays `deb.package.deleted` (the file is withheld by a mark in `deb_tombstones`, never deleted) and
`deb.coordinates.removed` (the coordinates are removed again; a file left without any is withheld) in
[journal_replay.go](../../internal/formatdata/deb/journal_replay.go). `trove admin deb reinstate <project>/<repository> <path>`
([cmd/trove/admin_deb.go](../../cmd/trove/admin_deb.go), [reinstate.go](../../internal/formatdata/deb/reinstate.go)) removes a mark while the
file is stored with the digest the mark names.

**Reads.** `GET pool/…` serves the file through the core (range and conditional requests), after the read permission on the package and
version is decided; the serve check ([servecheck.go](../../internal/formats/deb/servecheck.go)) answers 404 for a withheld file.
`VersionPath` ([paths.go](../../internal/formats/deb/paths.go)) names the path the decision point decides a version on besides the
package's (Q-DEB-47, H-720).

**Screens, setup, guide.** [view.go](../../internal/formats/deb/view.go) is the view adapter (dpkg order, the first line of the description, the
architectures and coordinates, the control fields); [setup_local.go](../../internal/formats/deb/setup_local.go) the client `apt` (a key file
and a source file named by `SetupData.Project` and `Key`, Q-ARCH-420, and an `auth.conf.d` file for the credential);
[guide_local.go](../../internal/formats/deb/guide_local.go) and [guide/](../../internal/formats/deb/guide) the seven sections.

**Tests.** The twin of the local part of the port ([twin_test.go](../../internal/formats/deb/twin_test.go)) drives the conformance kit
([conformance_test.go](../../internal/formats/deb/conformance_test.go)) and the handler tests
([handlers_local_test.go](../../internal/formats/deb/handlers_local_test.go)); the SQL, the ownership and the permissions per side are in
`test/integration/deblocal`.

## The index builder (WP4b)

**The job.** `deb-index` ([build.go](../../internal/formatdata/deb/build.go), `Indexer.Build`) reads the repository from the job's target key and
`IndexJobArgs` from its arguments. One build: a `REPEATABLE READ` snapshot reads the suites whose `change_seq` exceeds `built_seq` (every suite
for a full build) with their live, unwithheld files, the blobs' digests and the published generations; no transaction is open from there on. The
keys are opened for the build and their secrets cleared at once; the `Date` is chosen with `index.PickDate` (the database time minus 60 s, one
second after the previous `Date`, not before a key may sign, never later than the time minus 59 s: when the rules ask for more, the build waits on
the database clock and reads it again); the suite is rendered, compressed, signed (`ClearSign`, `DetachSign`) and handed to the publication port
([items.Publisher](../../internal/items/publish.go)), whose commit function writes the generation (`RecordGeneration`) and returns
`items.ErrSuperseded` when a newer build has published.

**Renderers.** [formats/deb/index](../../internal/formats/deb/index) is pure and a leaf: `Packages` (the paragraphs ordered by name, dpkg version
and architecture, the computed fields appended), `Gzip` and `XZ` (equal content, equal bytes), `Build` (the layout of a suite: every component, every
architecture and `binary-all`; the `all` files under every architecture; the empty indexes of a component whose last file left), the `Release` text,
`Retain`, `GivenUp` and `PickDate`.

**What is stored.** Per generation, as generated items at `dists/<suite>/…`: `Release`, `InRelease`, `Release.gpg`, every `Packages.gz` (and
`.xz` with `deb.compression: ["xz"]`) and the by-hash copy of each index file. `deb_generations.files` maps the index files to their SHA-256; retention
([index/retention.go](../../internal/formats/deb/index/retention.go)) decides from it which by-hash paths the commit removes (Q-DEB-51), and only a build
removes (Q-DEB-33). `modified_at` of every item is the `Date`.

**Enqueueing.** Every writer of a change calls `group.raise` ([changes.go](../../internal/formatdata/deb/changes.go)): upload, change of coordinates, deletion,
restore from the trash, withholding and reinstating a file. It enqueues after `deb.index_delay` (a waiting job is moved to the earlier time, Q-ARCH-384); a
key change enqueues at once (`Options.KeysChanged`). The task `deb-index-refresh` ([refresh.go](../../internal/formatdata/deb/refresh.go)) enqueues for a local repository
whose retention is due or that has changes no build published. The restore's fix-up ([backup/fixup/deb.go](../../internal/backup/fixup/deb.go)) enqueues a full build of
every local and virtual repository with `jobs.EnqueueTx`, which needs no queue (R-DEB-032).

**Reads.** `GET dists/…` and `…/by-hash/SHA256/<hex>` of a local repository ([handlers_dists.go](../../internal/formats/deb/handlers_dists.go)) authorize a read on a path that
belongs to no package, stat the generated item and serve it with the core's streaming and conditional rules; the `Cache-Control` is the format's (R-DEB-005).
`GET …/deb/index` ([api/v1/deb_index.go](../../internal/api/v1/deb_index.go)) reads the state, with `waitFor` through `buildStore.WaitPublished` (a poll every 250 ms, no
transaction). The composition root binds the builder on a replica that claims jobs (`startDebIndexer`, [wire_deb_index.go](../../internal/app/wire_deb_index.go)).

**Metrics and the delay.** `Indexer.Observe` records `trove_deb_index_builds_total{class,outcome}`, `trove_deb_index_build_seconds` and
`trove_deb_index_delay_seconds` (wire_deb_index.go). The delay runs from `deb_suites.dirty_since` (migration 00065, Q-DEB-58) to the commit.

**Tests of the local class** (`test/integration/deblocal`, Q-DEB-60): `local_test.go` and `replay_test.go` (upload, coordinates, deletion, replay), `index_test.go`
(a published generation, retention, reindex, `waitFor`, fault injection, co-signing), `replicas_test.go` (three replicas: fifty parallel uploads, by-hash on every replica, a deleted
by-hash item written again, identical full builds), `times_test.go` (twenty key changes in five seconds, a freshly bound imported key), `delay_test.go`
(`TestR_DEB_010_DelayBound`, the p99 under `TROVE_PERF_GATE=1`), `actionmap_test.go` (`TestR_AUTHZ_007_DebActionMap`), `crafted_test.go` (R-DEB-034 over HTTP),
`apt_test.go` (real clients, also with xz). The read budget of a download is `test/integration/querybudget/deb_test.go`. The stack runs without Valkey unless
a test asks (`envOpts.valkey`), so the closing run of R-DEB-031 for the local class is every test of the area but the delay bound.

**Tests.** The renderers: [index_test.go](../../internal/formats/deb/index/index_test.go) and `FuzzPackages`. The SQL, the signatures, the times, the retention, the
parallel uploads, the stopped build and the co-signing: `test/integration/deblocal/index_test.go`. A real client: `apt_test.go` runs `apt-get update --error-on=any` and
`apt-get install` in `debian:13` and `ubuntu:22.04` against a replica (skipped without docker). The replicas of these tests run with the role `all`, because the
builder is a job and the role `web` claims none; the stack seeds repositories by SQL, so the test creates and promotes a key where the creation hook would have.

## What is built and what is not

Built: everything above, including the stub archive (WP5), the signing keys (WP2), the background upstream (WP13), the publication
port and the index builder (WP4) with its metrics, the remote class (WP6), the mirror (WP7), the tests of the local class (WP9) and the UI
(WP11). Not built, by work package of [apt-work-packages.md](../projektplan/apt-work-packages.md): the virtual class (WP8), the tests of the
other classes (WP9), the e2e sets (WP10) and the final documentation (WP12).

## The OpenPGP library and the counting stub archive (stage 1 of the remote side)

The Debian format is built in stages (task T-17, [work packages](../projektplan/apt-work-packages.md), spec
[formats/debian.md](../spec/formats/debian.md)). This page holds what stage 1 added: the OpenPGP leaf package
[internal/openpgp](../../internal/openpgp) and the counting stub archive [test/debstub](../../test/debstub), and what stage 2 added: the
signing keys (WP2, [below](#signing-keys)). The rest of the format is described here when it lands.

| Part | Where | Requirements |
|---|---|---|
| The signer and the verifier | [internal/openpgp](../../internal/openpgp) | R-DEB-015, R-DEB-016, R-DEB-020, R-DEB-034, Q-DEB-10, Q-DEB-24, Q-DEB-35, Q-DEB-40 |
| The generator of installable `.deb` files | [test/debstub/debgen](../../test/debstub/debgen) | R-DEB-035 |
| The counting stub archive | [test/debstub](../../test/debstub) | R-DEB-035 |

### internal/openpgp

The only importer of `github.com/ProtonMail/go-crypto` (pinned once in [go.mod](../../go.mod), with `github.com/cloudflare/circl`
that it requires); the counting stub also imports it, on purpose (below). The package is a leaf in the
[layer table](../../internal/archtest/layers.go): standard library and the module only, no clock (every function takes the time it
decides against), no log line, no state.

| File | Responsibility |
|---|---|
| [openpgp.go](../../internal/openpgp/openpgp.go) | `Limits` and `DefaultLimits`, the error classes, `Key` and `Certificate`, `BoundSince` |
| [secret.go](../../internal/openpgp/secret.go) | `Generate`, `ParseSecret` and the import rules of R-DEB-015, `Key.SecretArmored` |
| [certs.go](../../internal/openpgp/certs.go) | `ParseCertificates` (the rules of `deb.upstreamKeys`), `ArmorCertificates` |
| [sign.go](../../internal/openpgp/sign.go) | `ClearSign`, `DetachSign`, `Key.EarliestSignature` (Q-DEB-40) |
| [verify.go](../../internal/openpgp/verify.go) | `VerifyClear`, `VerifyDetached`, the failure reasons |
| [armor.go](../../internal/openpgp/armor.go) | armor blocks: split, decode, encode |
| [testdata](../../internal/openpgp/testdata/README.md) | the archive keyring of `debian:13` |

**Keys.** `Generate(uid, created)` makes a version 4 key with public-key algorithm 22 (EdDSALegacy) over Ed25519, no expiry, no
subkeys, and a binding self-signature made at `created` (the module's `Config.Time`), so `BoundSince` equals `Created`. The caller
passes the PostgreSQL time minus one hour (Q-DEB-30). The module's default, algorithm 27, is never made or accepted: GnuPG 2.2 and
2.4 cannot read it. `Key` and `Certificate` carry `Fingerprint` (40 upper-case hex digits), `Algorithm`, `Bits`, `Created` and
`BoundSince`; a `Key` prints, formats and logs as its fingerprint and never shows the private part.

**Import** (`ParseSecret`, R-DEB-015) refuses, as `ErrUnsupportedKey` with the reason in the text: a version other than 4,
algorithm 27 or any algorithm but 22 and RSA, an EdDSA curve other than Ed25519, RSA below 3072 bits, a key whose secret part is
held elsewhere, a revoked key, one that cannot sign, a key with an expiry (the current self-signature of any identity or the newest
direct-key signature sets a lifetime), a creation or earliest binding self-signature later than `now`, and a signing subkey. An
unparseable or oversized block, two keys in one import, a missing or wrong passphrase answer `ErrInvalid`. A signed test message
proves the key before it is accepted. The returned `Key` keeps the primary key alone.

**Signing** (`ClearSign`, `DetachSign`) uses SHA-512, signature version 4, and refuses with `ErrTooEarly` a time before
`Key.EarliestSignature()`, one second after the later of `Created` and `BoundSince` (Q-DEB-40); the build waits for that time.
`ClearSign` signs the text byte for byte, so a `Release` that ends in a newline is signed with it. `DetachSign` writes all
signatures into one armored block. The signatures carry the module's random salt notation and are not deterministic.

**Verification** (`VerifyClear`, `VerifyDetached`, R-DEB-020, Q-DEB-35) succeeds when one signature of a configured certificate is
acceptable, and ignores signatures by other keys: the hash is SHA-256 or stronger; the signature time is at most
`openpgp.ClockSkew` (10 minutes) after `now`; the signing key (primary key or a signing subkey) is valid now and at the signature's
time, was created before the signature and is not revoked; and the signature is correct. A `Hash` armor header is checked only when
present (a signature whose hash it does not name is ignored). The message is one cleartext message with nothing before or after it.
A failure is a `*VerifyError`, which `errors.Is(err, ErrVerify)` matches and whose `Reason` (`openpgp.Reason(err)`) is the metric
label of `trove_deb_upstream_signature_failures_total`: `bad_signature`, `key_invalid`, `signature_expired`, `future`,
`weak_hash`, `hash_header`, `unknown_key`, `malformed`, `no_signature` or `too_many`. The text `VerifyClear` returns is the
dash-unescaped lines joined by newlines as the signature covers them: for an `InRelease` written by `gpg` that is the `Release`
without its last line ending, for one written by `ClearSign` the `Release` itself.

**Certificates** (`ParseCertificates`) reads one or more armor blocks (anything between them but white space is refused), 1 to 32
certificates in at most 256 KiB. It refuses a block that does not parse (the module's own reader would skip it), secret key
material, and a certificate that is revoked, expired or not yet valid at `now`, cannot sign, or has an RSA key below 2048 bits, as
`ErrInvalid`. Debian 13's archive keyring (9 certificates, 55,918 bytes binary, 75,803 armored) is a test fixture.

**Tests.** [key_test.go](../../internal/openpgp/key_test.go) (generated key shape, import rules, binding times, bounds, certificate
rules), [verify_test.go](../../internal/openpgp/verify_test.go) (signatures checked by the module's own reader and by the verifier,
co-signing, the rules of R-DEB-020 with signers the test builds with the module directly), and the fuzz targets
`FuzzParseCertificates`, `FuzzParseSecret` and `FuzzVerifyClear` in [fuzz_test.go](../../internal/openpgp/fuzz_test.go) (run with
`go test ./internal/openpgp -run xxx -fuzz FuzzVerifyClear`; the format conformance kit's `fuzz.go` is about routes, not parsers).

**Verified with the real clients** (2026-11-08, `apt-get update --error-on=any` against `file:` repositories on `debian:13`,
`debian:12`, `ubuntu:24.04`, `ubuntu:22.04`): `InRelease` and `Release.gpg` alone by one key; co-signed by two keys with a keyring of
either; an imported key whose only self-signature is 30 s old, signing 29 s after it; a keyring with another key fails and APT names
the missing key. Not done in this stage: the rotation table through trove's HTTP path (WP10), the key store, API and creation hook
(WP2's next stage).

### Signing keys

Every local and virtual Debian repository has a primary signing key from the transaction that creates it, can add a co-signing next
key, promote it, and has its keys withheld (not erased) by the replay of a restore. The security view is
[security/deb-signing.md](../security/deb-signing.md); the requirements are R-DEB-015 to R-DEB-018 and R-DEB-032.

| File | Responsibility |
|---|---|
| [formatdata/deb/keys_store.go](../../internal/formatdata/deb/keys_store.go) | the key group of the data port (`debformat.KeyStore`): `CreateFirstKey`, `PublicCertificates`, `Keys`, `SigningKeys`, `CreateNext`, `Promote`, `RetireNext`, `ConfirmWithheld`, `ReinstateKey`, `WithholdKey`, `PromoteByJournal` |
| [formatdata/deb/keys.go](../../internal/formatdata/deb/keys.go) | what the store and the replay share: the rows in the port's words, the repository lookup and its lock (`lockRepo`), the database clock (`dbNow`), sealing (`insertKey`), the audit events and journal entries (`record`), `keysChanged` |
| [formatdata/deb/keys_journal.go](../../internal/formatdata/deb/keys_journal.go) | the replay handlers of `deb.signing_key.promoted` and `deb.signing_key.retired` (registered with `journal.Register` in `init`) and the functions the port's `PromoteByJournal` and `WithholdKey` call |
| [formatdata/deb/reseal.go](../../internal/formatdata/deb/reseal.go) | the re-encryption walk of the sealed column (WP0) |
| [queries/deb_keys.sql](../../internal/meta/postgres/queries/deb_keys.sql) | the statements: times as RFC 3339 text, the private part only in `DebSealedKeys`, the repository lock `DebRepoRefLocked` |
| [api/v1/deb_keys.go](../../internal/api/v1/deb_keys.go) | the five management operations; `v1.DebKeyStore` is the interface of the port they use (`Options.DebKeys`, wired by `App.debKeyStore`) |
| [formats/deb/handlers_keys.go](../../internal/formats/deb/handlers_keys.go) | `signing-key.asc` for the local and virtual classes; the remote class's handler (the certificates of `deb.upstreamKeys`) is WP6's |
| [app/wire_formats.go](../../internal/app/wire_formats.go) | `bindDebCreateHook`: `repository.RegisterCreateHook("deb", …)`, once per process, following the store composed last; the store gets the key ring (`debdata.Options.Ring`) |

**Flows.** *Create a repository:* the repository service runs the hook in its transaction; the hook calls `CreateFirstKey`, which reads the
database's time, generates the key with `openpgp.Generate(uid, now-1h)`, opens it again with `ParseSecret` (the self-test, and the source
of the fingerprint and the times), seals the secret under the new row's id, inserts the row as `primary` and records
`deb.signing_key.created`. A remote repository gets no key. *A change by the API:* the handler resolves the repository from the replica's
snapshot (400 `not_applicable` for a remote repository or another format), the store opens one transaction, locks the repository row
(`lockRepo`), reads the keys with `FOR UPDATE`, changes them, raises `change_seq` of every suite, calls `Options.KeysChanged` and records
the event with the journal entry. *Sign a build:* `SigningKeys` returns the primary first and the next key after it, with the secret
opened; none when there is no primary. *Replay:* see [security/deb-signing.md](../security/deb-signing.md#the-withheld-state-and-the-replay-of-a-restore).

**For the next packages.** WP4 (the index build) calls `SigningKeys`, signs with `openpgp.ClearSign` and `DetachSign` at a time not before
`Key.EarliestSignature()` of each key, and sets `Options.KeysChanged` (the seam that enqueues a build without delay; until it is set a key
change is picked up by the periodic refresh task). The remote branch of `serveSigningKey` and
`debUpstreamKeysRemoved` are built ([the remote class](#the-remote-class)). A change of the sealed column must change `reseal.go` too.

**Tests.** Unit: [keys_journal_test.go](../../internal/formatdata/deb/keys_journal_test.go), [deb_keys_test.go](../../internal/api/v1/deb_keys_test.go),
[handlers_keys_test.go](../../internal/formats/deb/handlers_keys_test.go). Integration, the area [test/integration/debkeys](../../test/integration/debkeys)
(production composition root, repositories created through the API): the first key and its sealing, which repositories have keys, the
import rules, the rotation with the signing path of the build, the permissions, concurrent changes, the sealed column's row binding,
`signing-key.asc`, and the replay in every case of the table of the security page. Run it through the slot wrapper
(`go test -tags integration ./test/integration/debkeys/`).

## Publication, builds and the reindex

What the index builder (T-17 WP4) stands on, built ahead of it:

| What | Where |
|---|---|
| the publication port `items.Publisher` for both classes, and the 409 `generated_item` for a client | [items/publish.go](../../internal/items/publish.go), [items.md](items.md#the-publication-port), Q-STOR-182, Q-STOR-210 |
| the coalesce enqueue that moves the waiting job earlier and merges arguments | [jobs/queue.go](../../internal/jobs/queue.go), [coordination.md](coordination.md#job-framework), Q-ARCH-384 |
| `Store.EnqueueBuild(ctx, tx, repoID, delay)`: an automatic `deb-index` job under the key `index:<repository id>`; the writers of rows that need a build call it with `deb.index_delay`; a key change calls it with no delay (the default of `Options.KeysChanged`) | [formatdata/deb/enqueue.go](../../internal/formatdata/deb/enqueue.go) |
| `Store.Reindex`: the full build of the reindex endpoint; `IndexJobArgs{Full, Reindex}` are the arguments the builder reads (`Reindex` implies `Full`) | the same file |
| `POST …/reindex`: 202 with the job, 409 `reindex_running`, 405 `not_applicable`; `v1.Reindexer` is implemented by a format's data port and offered by `App.reindexers` | [api/v1/reindex.go](../../internal/api/v1/reindex.go), [app/wire_formats.go](../../internal/app/wire_formats.go) |

The index builder replaces `RunIndexJob` (`build.go`): it reads `IndexJobArgs` from `run.Args`, publishes with `Publish` (its `commit` writes the generation
rows and returns `ErrSuperseded` when a newer generation exists), and registers nothing else: the endpoint and the enqueues need no change. Tests: the area
[test/integration/debkeys](../../test/integration/debkeys/reindex_test.go) (`TestR_REPO_016_…`), [itempublish](../../test/integration/itempublish/publish_test.go) and
the queue test in [coordination](../../test/integration/coordination/coalesce_test.go).

## The screens

The Angular side (T-17 WP11) is described in [web-ui.md](web-ui.md#the-debian-screens): the tabs **Signing keys** and **Index** of a local or virtual
repository, the read-only fingerprints of a remote's upstream keys, the list controls of the settings, the client `apt`, the icon and the
"Package details" block. It talks to the five key operations of [Signing keys](#signing-keys), `GET …/deb/index` and `POST …/reindex` through the
generated types (`web/src/app/api/deb-api.ts`); the decisions are Q-DEB-100 to Q-DEB-104 and Q-UI-200 to Q-UI-202. When you add a field to
`DebSigningKey`, `DebIndexState` or the `metadata` of the view (`metadataOf` in [formats/deb/view.go](../../internal/formats/deb/view.go)), change the
panel and its fake in the same step; the ids of the setup snippets (`apt.*`) are checked against the UI by
`TestR_API_030_RenderedSnippetIdsMatchTheUIFixture`.

## The remote class

A remote repository is a pull-through cache of an APT archive (R-DEB-019 to R-DEB-023). The routes are the shared ones plus the three of a flat
directory; a request addressed to a remote repository goes to `handleRemote` ([handlers_remote.go](../../internal/formats/deb/handlers_remote.go)) through
`registerRemoteHandler` ([handlers.go](../../internal/formats/deb/handlers.go), ADR-186), so the local class's handlers are not edited.

**What a path is** (`analyzeRemote`, [remote.go](../../internal/formats/deb/remote.go)): a release document (`InRelease`, `Release`, `Release.gpg`), a file a release
names (any file below `dists/` that is no by-hash file and no pdiff patch, and an index name outside it), a by-hash file (SHA256, SHA512; MD5Sum and SHA1
answer 404), a patch below a `*.diff` directory, or content. The first two are metadata documents of the engine, the rest artifacts.

| Step | Where |
|---|---|
| The upstream URL is `remote.url` + the canonical path | `UpstreamRequest` |
| A release is read, bounded to 16 MiB, parsed ([release](../../internal/formats/deb/release/release.go)) and, with keys, verified (`openpgp.VerifyClear`); a failure is 502 `deb_release_invalid` or `deb_upstream_signature_invalid` and stores nothing | `PrepareFill` |
| The `Date` bound, the anti-rollback decision, the rows of the release and its listed files, and the enqueue of the parse of the indexes the release changes | `OnFill` → `RecordRelease` |
| An index must be the file the stored release lists, or the fill stores nothing (`deb_index_mismatch`); a `Packages` index enqueues its parse | `OnFill` → `indexFilled` |
| The digest of a by-hash file (its path) and of a package file (the newest parsed index) | `BeforeArtifactWith` ([remote_gate.go](../../internal/formats/deb/remote_gate.go)) with the serve facts `deb_remote` |
| The shadowing check in the scope of a virtual repository | `ServesName` ([remote_names.go](../../internal/formats/deb/remote_names.go)) → `RemoteStore.ServesName`, `EnsureParsed`; the bridge is [formathost/name_servers.go](../../internal/formathost/name_servers.go) |
| A release no key verifies is revalidated with a HEAD that says "moved", so the GET carries no validators | `RevalidationRequest` |

**Rows** ([remote_store.go](../../internal/formatdata/deb/remote_store.go), [deb_remote.sql](../../internal/meta/postgres/queries/deb_remote.sql)): `deb_remote_releases` (the stored documents, `verified_by`, `floor_date`,
the `absent` record), `deb_remote_files` (the files the current release lists and, for `deb.by_hash_keep`, the replaced one's), `deb_remote_indexes` and
`deb_remote_entries` (the parse). Writers of one directory take an advisory lock of the directory. `RecordRelease` is the single place that decides the floor.

**Jobs** ([remote_parse.go](../../internal/formatdata/deb/remote_parse.go), [housekeeping.go](../../internal/formatdata/deb/housekeeping.go), [jobs.go](../../internal/formatdata/deb/jobs.go)):
`deb-remote-parse` (queue `index`, coalesced per repository, directory and index path) reads the hash the stored release names, fetches the index through
`Host.BackgroundUpstream` (by hash when the release has `Acquire-By-Hash`), and replaces the rows in one transaction; an empty path fetches the release of a
directory (recording an absent suite) and enqueues the parses of its scope. `deb-index` of a remote re-verifies the unverified releases from the cache and forgets what
`deb.by_hash_keep` has passed. The runtime is `RemoteJobs`; the composition root wires it as a late value on a replica that claims jobs
([wire_deb_remote.go](../../internal/app/wire_deb_remote.go), ADR-187) and implements `Background` over the mirror host.

**Repository rules** ([validate_deb.go](../../internal/repository/validate_deb.go), [members.go](../../internal/repository/members.go), [journal_replay.go](../../internal/repository/journal_replay.go),
[keys_hooks.go](../../internal/repository/keys_hooks.go)): keys are checked on save and their fingerprints derived; a mirror needs keys; a remote without keys cannot be a member and a member
keeps its keys; an update that removes fingerprints runs the keys-removed hook (the data port clears `verified_by` and enqueues the re-verification) and journals
`debUpstreamKeysRemoved` in `repository.narrowed`; the replay is step 8 and disables a mirror or member left without keys.

**Dispatcher.** `deb <URL> ./` requests `<URL>/./InRelease`; the router has no redirect for it (`HandleUncleaned`), the dispatcher cleans the path after dropping the
first `.` segment and hands the format the path as received, and `Format.Match` reads the leading `./` (Q-ARCH-385, Q-DEB-39).

**Tests.** Unit: [release_test.go](../../internal/formats/deb/release/release_test.go), [remote_test.go](../../internal/formats/deb/remote_test.go),
[validate_deb_test.go](../../internal/repository/validate_deb_test.go), [remote_parse_test.go](../../internal/formatdata/deb/remote_parse_test.go), the conformance fixture. Integration, the
area [test/integration/debremote](../../test/integration/debremote) (production composition root in the role `all`, so that its jobs run; the stub on loopback HTTP): the
release and its verification cases, the pair, the absent suite, `signing-key.asc`, indexes as one snapshot, by-hash, the flat directory and the flat root, pool digests and the
`require` policy, the parse rows and their follow-up by pdiff, the paragraph bound, the shadowing check, the key removal with its journal entry, the replay and the purge.
Run it through the slot wrapper (`go test -tags integration ./test/integration/debremote/`; the cases that wait for a period take a minute or two).

### Proven by hand

`TestProofServer` (set `TROVE_DEB_PROOF_DIR` to a directory) starts the installation, a replica in the role `all` and the stub, and writes the port, a token and the stub's key
`a` into the directory. The clients are containers with the directory mounted and `trove.test` added to `/etc/hosts` (the host name of the default endpoint): the container
removes the image's sources, installs `key.asc` as `Signed-By`, writes `auth.conf` with the token for `http://trove.test:<port>` and runs `apt-get update --error-on=any` and
`apt-get install`. Run on 2026-11-09 against `debian:13` (APT 3.0.4, `sqv`) and `ubuntu:22.04` (APT 2.4.14, `gpgv`): the suite `stable main` of the keyed remote (the
release passes through with the stub's signatures, the client verifies them with the key `a`; `trove-hello` and its dependency install), the flat directory `binary/` and the flat
root `./`. The remote hands out the upstream's `InRelease` unchanged; the keyring on the client is the upstream's, never trove's.

## The mirror

A remote in mode `mirror` keeps what the operator names (R-DEB-024, Mirror discovery). The remote adapter is a `discover.Lister`
([mirror.go](../../internal/formats/deb/mirror.go)), so the catalog fact `Mirror` is true and the repository service accepts `remote.mode: mirror`; the engine, the pass, the bounds and the
throttling are the core's ([mirror.md](mirror.md)). What the format adds:

| Step | Where |
|---|---|
| Methods: `listing` and `names`; no feed, so every pass is full | `MirrorMethods` |
| The **index step** on the first page of a pass: the release of each suite through the pass's upstream, judged (signature, suite, `Valid-Until`), the index files of the scope kept in every listed variant (by `SHA256` and, when listed, `SHA512` hash with `Acquire-By-Hash`, at the plain path without), and the parse of Packages and Sources in the pass | `MirrorSync` ([mirror_sync.go](../../internal/formatdata/deb/mirror_sync.go)) over `RemoteJobs.parseIndex` |
| The names: the distinct package names of the rows of the suites whose stored release a key verifies, in the indexes of the components and architectures (and `all`), in pages with the cursor `{"last": name}` | `MirrorNames`, [mirror_store.go](../../internal/formatdata/deb/mirror_store.go), [deb_mirror.sql](../../internal/meta/postgres/queries/deb_mirror.sql) |
| The plan of a name: its files, one item per file, newest version first by dpkg order; the engine counts versions, so the files of one version are kept or dropped together | `MirrorPlan` |
| The digests every stored file is verified against | the gate of R-DEB-022, from the same rows |
| `deb.mirror.suites` required in mode mirror (422 `deb_mirror_suites_required`); keys required (`deb_upstream_keys_required`) | [validate_deb_mirror.go](../../internal/repository/validate_deb_mirror.go), [validate_deb.go](../../internal/repository/validate_deb.go) |
| An Owner keeps `deb.mirror` as stored | the generic rule of the format's mirror member (`checkDelegatedFormatMirror`) |

The lister is stateless: the rows are the state, and a pass that moves to another worker finds them. The lister learns its repository from the optional `RepositoryID()` of the pass's
upstream (Q-ARCH-428). `MirrorHost.Admits` skips the test of a package document for a format that has none (Q-REPO-304); before that every name was refused.

**Tests.** [mirror_test.go](../../internal/formats/deb/mirror_test.go) (the lister over fixed rows), [validate_deb_test.go](../../internal/repository/validate_deb_test.go), and the integration cases
in [test/integration/mirror/deb_test.go](../../test/integration/mirror/deb_test.go): a pass with the defaults and the stub stopped, the version bound over two suites, a quiet second pass and a new generation,
the byte bound with a pull-through install, an unknown key, sources. They run the pass on one of two worker processes with a third process serving the clients.

### Proven by hand

`TestProofMirrorServer` (set `TROVE_DEB_PROOF_DIR`, as for `TestProofServer`) runs a pass, stops the stub and serves the mirror. Run on 2026-11-09 with the clients of the remote proof: `apt-get update --error-on=any` and
`apt-get install trove-hello` from `debian:13` (APT 3.0.4, `sqv`) and `ubuntu:22.04` (APT 2.4.14, `gpgv`), the stub's key `a` as the client keyring, the stub stopped. The first attempt failed with 503 on the
`by-hash/SHA512` paths, which is why the pass keeps the SHA512 variants (Q-DEB-92).

## test/debstub: the counting stub archive

A deterministic APT archive that is not trove: it serves generated content over TLS only, counts every request, and fails on
command. Everything is generated in code (no recorded third-party data); two starts produce the same package, `Packages` and
`Sources` bytes (only the `Release` `Date` and the signatures differ). The stub signs with the OpenPGP module directly and does
not import `internal/openpgp` outside its tests, so trove's verifier is tested against an independent signer; its tests verify
every document it serves with trove's verifier.

| File | Responsibility |
|---|---|
| [stub.go](../../test/debstub/stub.go) | `Config`, `Server`, `New`, the archives, `NewGeneration`, `Generation`, `Reset`, `ResetAll`, `SetMode`, `Archives`, `Release`, `Files` |
| [corpus.go](../../test/debstub/corpus.go) | the package set (`defs`), the source packages, the layout constants |
| [archive.go](../../test/debstub/archive.go) | an archive: its indexes, generations, by-hash tree, pdiffs and `Release` rendering |
| [keys.go](../../test/debstub/keys.go) | the test keys from fixed seeds, `Keys`, `Key`, `Keyring`, the signing of `InRelease` and `Release.gpg` |
| [serve.go](../../test/debstub/serve.go) | the request path: count, gate, resolve, sign (cached per generation and mode), answer with validators |
| [mode.go](../../test/debstub/mode.go) | the switches (`Mode`) and the classes of request |
| [throttle.go](../../test/debstub/throttle.go) | the rate-limit answers of R-REPO-059 |
| [counts.go](../../test/debstub/counts.go), [inflight.go](../../test/debstub/inflight.go) | the counters and the requests open at once (R-REPO-055) |
| [control.go](../../test/debstub/control.go) | the control API below `/-/stub/` |
| [tls.go](../../test/debstub/tls.go) | `Start` (TLS or nothing), a throwaway CA, a certificate reloaded when its files change |
| [debgen](../../test/debstub/debgen) | `Build(Spec)`: an installable `.deb` (`ar` of `debian-binary`, `control.tar.gz`, `data.tar.gz` holding one script that prints a fixed line); `Hello(name, version, arch)` for the tests that upload |
| [cmd/debstub](../../test/debstub/cmd/debstub), [Containerfile](../../test/debstub/Containerfile) | the binary and the image (`make deb-stub-image`, tag `DEB_STUB_IMAGE`, default `trove-e2e-debstub:dev`) |
| [deploy/dev/debstub.yaml](../../deploy/dev/debstub.yaml) | the manifest for kind: Service `debstub`, a certificate of the dev CA, the control token in Secret `trove-debstub`; the e2e harness applies it for the set `deb` ([e2e.md](e2e.md#the-counting-debian-test-upstream-in-the-cluster)) |

#### What it serves

All below one host.

| Path | Content |
|---|---|
| `/debian/dists/stable/`, `/debian/dists/ratel/` | the suite `stable` (codename `ratel`, both names serve the same release): components `main` and `contrib`, `binary-amd64`, `binary-arm64`, `binary-all`, `main/source/` (`Sources`, one source package); `InRelease`, `Release`, `Release.gpg`; each index plain and `.gz`; `by-hash/{MD5Sum,SHA1,SHA256,SHA512}/` next to each |
| `/debian/dists/testing/`, `/debian/dists/badger/` | the suite `testing` (codename `badger`) with newer versions and the package `trove-newpkg` |
| `/debian/dists/bulk/` | one component, `binary-amd64`: `Config.BulkParagraphs` (70,000) synthetic paragraphs, about 24 MB plain, built on the first request; the files they name are not served |
| `/debian/pool/<component>/<prefix>/<source>/` | the `.deb` files and the source files |
| `/flat/binary/` | a flat repository with a directory: `deb <host>/flat binary/`; `Filename` is `binary/<file>`; `Suite` and `Codename` are `binary` |
| `/flatroot/` | a flat repository with the root as directory: `deb <host>/flatroot ./`; APT asks for `/flatroot/./InRelease`, which the stub serves and counts as written; `Filename` is `<file>` |

The packages (all `trove-hello` style: the installed script `/usr/bin/<name>` prints `hello from <name> <version> (<arch>)`):
`trove-hello` (amd64, arm64; depends on `libtrove1 (>= 1.2)`), `trove-hello-doc` (all, source `trove-hello`), `trove-tools` (amd64,
arm64; an alternative dependency), `trove-config` (all; provides `trove-config-default`), `libtrove1` (amd64, arm64; source
`libtrove`), and in `contrib` `trove-extra` (amd64) and `trove-extra-data` (all). A generation adds `trove-zz-g<NNN>`. The flat
repositories hold `trove-flat`. `Packages` paragraphs are the control file plus `Filename`, `Size`, `MD5sum`, `SHA1`, `SHA256` and
`SHA512`. Every file validates: `dpkg` installs a generated package (checked on `debian:13`), and the four client images read the
stub's tree, install from it and run the script (Client and library behaviour in the spec).

**Release.** `Origin`, `Label`, `Suite`, `Codename`, `Date`, `Acquire-By-Hash: yes`, `Architectures` (`all amd64 arm64`),
`Components`, `Description`, and `MD5Sum`, `SHA1`, `SHA256`, `SHA512` lists of every index file, the pdiff `Index` included.
`Date` is the stub's clock minus a minute, and at least a second after the previous generation's. `InRelease` is framed as `gpg`
writes it. The three signed documents are made once per generation and signing configuration, so repeated requests see equal bytes
and conditional requests are answered 304 (`ETag` and `Last-Modified`, `Range` is honored; `Mode.NoValidators` withholds them).

**Generations** (`NewGeneration`, `POST /-/stub/generation`): a package is added to `main/binary-amd64/Packages` (to the one
`Packages` of a flat repository), the `Release` gets a later `Date`, the previous generation's files stay reachable by hash, and the
changed `Packages` gets a pdiff: `Packages.diff/Index` (`SHA256-Current`, `-History`, `-Patches`, `-Download`) and one patch per
generation, an `ed` script that appends the new paragraph (`Packages.diff/<date>.gz`, also by hash). `ResetAll` returns every
archive to its first generation.

#### The test keys

Fixed seeds, algorithm 22, version 4; the certificate is reproducible (a test pins the fingerprints). Created on fixed dates and
not "an hour back" (Q-DEB-70).

| Key | Fingerprint | Created | Use |
|---|---|---|---|
| `a` | `744FA9DC6DDF29FCBBDCFC3F0F1B0C0EE2D15353` | 2026-01-01 | the archive's key; default signer |
| `b` | `1740FB0FDF7BEDEAE568BFDFA14B934907B0A66E` | 2026-01-01 | a second key, for co-signing |
| `c` | `112CAA63A6EDBB4E9727D9D67BF765B76B7E6242` | 2026-01-01 | a key nobody configured |
| `d` | `AA0B4230C41119252C6CFF1C942F62DCF3C4BBE2` | 2024-01-01, one year | an expired key; its signatures are dated 2024-06-01 |

`Server.Keyring("a", "b")`, `GET /-/stub/keyring?names=a,b` and `debstub keyring a` give the armored certificates for
`deb.upstreamKeys`. trove refuses `d` as a configured key (it is expired); a verifier test hands its certificate over directly.

#### The switches (`Mode`, `POST /-/stub/mode`, R-DEB-035)

| Field | Effect |
|---|---|
| `down` | `503`, `reset` (connection cut) or `hang` (held until the client leaves or two minutes) |
| `delay_ms`, `fail_status` | per class (`release`, `index`, `diff`, `by-hash`, `pool`, `other`, `all`): hold the answer; answer a 5xx |
| `no_by_hash` | by-hash paths answer 404 and `Acquire-By-Hash` is left out |
| `no_inrelease` | `InRelease` answers 404; `Release` and `Release.gpg` stay |
| `no_hash_header` | `InRelease` without the `Hash` armor header |
| `sign_with` | the signing keys, `["a"]` by default; `["a","b"]` co-sign; `["c"]` an unknown key; `["a","c"]` a known and an unknown; `["d"]` an expired key with a backdated signature |
| `sign_hash` | `sha512` (default), `sha256`, `sha1` |
| `signature_skew_seconds` | added to the time of every signature (not of `d`): a signature from the future |
| `broken_signature` | `InRelease`'s text is changed after signing, `Release.gpg` signs other bytes |
| `date` | `older` (an hour before the generation's) or `ahead` (20 minutes after the clock) |
| `valid_until` | `passed` (ten minutes ago) or `6d` (six days after the `Date`) |
| `not_found` | archives (suite ids and codenames, `flat`, `flatroot`) whose every path answers 404 |
| `wrong_package` | package file names (or `all`) whose bytes differ from their index entry |
| `index_mismatch` | the plain `Packages` and `Sources` paths (and `.gz`) differ from what the `Release` lists; by-hash stays right |
| `no_validators` | no `ETag` or `Last-Modified`, conditional headers ignored |

`Throttle` (`POST /-/stub/throttle`) answers the n-th request, a run of them or all requests for a period with `429`, `429-date`,
`503`, `503-slowdown`, `403-quota` or `429-quota` (the answers the mirror recognizes, R-REPO-054, R-REPO-059), per class.

#### Counters and the control API

`Counts()` keeps one entry per method, host, path as written, query and conditionality, with the kind (class) and the statuses; a
cut answer counts under `0`. Helpers: `Requests`, `OfKind`, `Prefixed`, `Conditional`, `Delivered`, `NotModified`. `Inflight()` is
the number of requests open now and the most at once since the last reset.

Below `/-/stub/` with `Authorization: Bearer <token>` (`Config.ControlToken`, `-control-token-file`; without a token the API does
not exist; control requests are not counted): `GET counts`, `GET inflight`, `POST reset[?all=true]`, `GET|POST mode`,
`GET|POST throttle`, `GET archives`, `POST generation {"archive"}`, `GET release?archive=`, `GET keys`, `GET keyring?names=`,
`GET files`.

#### Running it

```sh
go run ./test/debstub/cmd/debstub serve -self-signed localhost,127.0.0.1 -ca-out /tmp/ca.crt -listen 127.0.0.1:8443
make deb-stub-image            # the container image, tag $(DEB_STUB_IMAGE)
```

In a Go test, `debstub.New(cfg)`, `stub.Start("127.0.0.1:0", ss.ServerConfig())` with `ss, _ := debstub.NewSelfSigned(debstub.LoopbackHosts...)`;
`Running.URL` is `https://deb.localhost:<port>` and the name resolves through `Running.DialContext` or `Running.DialOverride()`
(trove's `outbound.WithDialOverride`), as with the other stubs.

What it does not do: bzip2, LZMA, xz and zstd indexes, `Contents` and translation files, `dep11` and uploads;
the xz handling of trove is tested with its own unit tests and WP8's bound test (Q-DEB-73).
