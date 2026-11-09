# Debian index builds

Perspective: what turns the rows of a local Debian repository into the signed indexes its clients trust, and what that path lets in. The
mechanisms are the renderers ([internal/formats/deb/index](../../internal/formats/deb/index)), the build job
([build.go](../../internal/formatdata/deb/build.go)), the store behind it ([build_store.go](../../internal/formatdata/deb/build_store.go),
[deb_build.sql](../../internal/meta/postgres/queries/deb_build.sql)), the publication port ([items/publish.go](../../internal/items/publish.go)),
the read routes ([handlers_dists.go](../../internal/formats/deb/handlers_dists.go)) and the index state read
([deb_index.go](../../internal/api/v1/deb_index.go)). The requirements are
[R-DEB-010 to R-DEB-012, R-DEB-014 and R-DEB-016](../spec/formats/debian.md#r-deb-010--index-builds-a-job-a-consistent-snapshot-a-delay-bound); the key
itself is [deb-signing.md](deb-signing.md), the developer view [deb.md](../developer/deb.md).

An APT client trusts the signature, so whatever the build signs is trusted. The build signs what the rows say; this page is about what gets into
the rows, who can change them, and what a reader can fetch.

## The path from an upload to a signed index

| Step | Who or what | What it trusts |
|---|---|---|
| Upload | a principal with `deploy` on the package name and its ownership ([R-DEB-004](../spec/formats/debian.md)) | the package's own control file, read under the bounds of R-DEB-034 |
| The row | the upload's transaction | the normalized paragraph, written once from parsed fields |
| The build | the job `deb-index`, on a replica that claims jobs | the rows of one snapshot; no package file is read |
| The signature | the repository's keys, opened for the build | the clock of the database for the `Date` and the signature time |
| The commit | the publication port | the condition that the suite's `built_seq` is still below the number the build read |

**No injection.** The `Packages` paragraph is built from the parsed fields of the stored paragraph, then `Filename`, `Size`, `MD5sum` and `SHA256`
from the item and its blob; whatever the upload held of these four fields is dropped. The writer of `control` validates what it writes (a value
cannot start a paragraph or add a field), and `FuzzPackages` holds that two entries always read back as two paragraphs. The `Release` is built from
the settings and the digests of the files this build made, never from the upload.

**What a manager can steer.** A principal with `deploy` decides the text of the fields of its own package (its description, dependencies, maintainer)
and which suites and components list it. Another package's paragraph is not reachable by it (ownership, R-AUTHZ-018). A principal with `delete` can take
a file out of an index.

**Keys.** The build asks the key store for the opened secrets once, parses them, clears the byte slices at once, and holds the parsed keys for the
length of the build only. They are not logged (a `Key` prints as its fingerprint) and are not passed to the publication port. A build with no usable
primary key marks the suites `failed` with `no_signing_key` and publishes nothing ([H-722](../spec/formats/debian.md#h-722--a-repository-whose-builds-fail-keeps-serving-its-last-generation)).

## Times

The `Date` of a `Release` and the creation time of its signatures trail the database clock by a minute, never run ahead of it by more than the rule
allows, and follow the previous `Date` by at least a second ([Q-DEB-30](../spec/formats/debian.md)); a key signs no earlier than one second after its
creation and its earliest binding self-signature (`Key.EarliestSignature`, Q-DEB-40). Both are what the clients' verifiers need: a signature from a
clock that runs ahead is refused, and a `Date` that goes backwards would let an old index replace a newer one on a client that compares dates. The wait
the build makes for the clock holds no transaction and reads the database time again after it.

## Who may read what

The index files are generated items with no package name. A read is decided on a path of the repository with no package, so a grant that narrows to
package names does not cover the index (R-DEB-004, and the guide says so): such a principal can download the package files it may read and cannot use
the repository as an APT source. The by-hash routes are decided the same way. A withheld file (R-DEB-032) is left out of every build, and its pool path
answers 404.

## Who may write

Nobody writes an index item but the publication port: a client upload over a generated item and a delete of a subtree that holds one answer 409
`generated_item`. A virtual repository accepts generated items from the port and from nothing else. The port runs no quota admission and no hook.

## Retention and the keep time

By-hash files stay for the current generation, the superseded ones younger than `deb.by_hash_keep` and the three newest superseded ones (Q-DEB-51), and
are removed only in the commit of a build (Q-DEB-33), serialized with every publication of the repository under the key `index:<repository id>`. Nothing
else deletes an index item; the periodic task only enqueues.

## After a restore

A restore enqueues a full build of every local and virtual repository (the fix-up of R-BAK-022, R-DEB-032), so the journal replay's withheld files and
keys take effect in the indexes. Until the build runs the restored generation is served; it lists what the snapshot listed, and a file the replay
withheld answers 404 on its pool path in the meantime.

## What this does not cover

- [H-624](../spec/formats/debian.md#h-624--troves-own-release-files-carry-no-valid-until): trove's own Release files carry no Valid-Until.
- [H-720](../spec/formats/debian.md#h-720--a-path-pattern-that-hides-a-package-file-does-not-hide-its-version-from-the-package-screens): a path pattern that hides a package file does not hide its version from the package screens.
- [H-721](../spec/formats/debian.md#h-721--the-index-of-a-deleted-package-stays-fetchable-by-hash-for-the-keep-time): the index of a deleted package stays fetchable by hash for the keep time.
- [H-722](../spec/formats/debian.md#h-722--a-repository-whose-builds-fail-keeps-serving-its-last-generation): a repository whose builds fail keeps serving its last generation.

The gaps of the signing keys and of their restore (H-620, H-621, H-622, H-627, H-629, H-735) are on [deb-signing.md](deb-signing.md#what-this-does-not-cover).
