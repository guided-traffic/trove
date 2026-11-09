# Debian mirrors

Perspective: what a Debian remote in mode `mirror` stores on its own, without a client asking, from an upstream it has to trust to a
chosen degree, and what that adds to the pull-through remote of [deb-remote.md](deb-remote.md). The mechanisms are the lister
([mirror.go](../../internal/formats/deb/mirror.go)), the index step and the rows it reads
([mirror_sync.go](../../internal/formatdata/deb/mirror_sync.go), [mirror_store.go](../../internal/formatdata/deb/mirror_store.go)) and the
rules of the repository document ([validate_deb_mirror.go](../../internal/repository/validate_deb_mirror.go)); the pass itself, its fence,
its bounds and its throttling are the engine's ([mirror.md](mirror.md)). The requirements are
[R-DEB-024 and Mirror discovery](../spec/formats/debian.md#r-deb-024--mirror-settings-of-a-debian-remote); the developer view is
[deb.md](../developer/deb.md#the-mirror).

A mirror keeps the content of an archive for clients that read it when the archive is gone. Nothing is stored that a verified release
does not list, and nothing is planned from a release no key verifies: a mirror hands the upstream's content on to many clients, so the
checks of a pull-through remote are conditions here.

## What a pass trusts

| Step | Condition | If it fails |
|---|---|---|
| The repository is saved as a mirror | `deb.upstreamKeys` set (422 `deb_upstream_keys_required`), at least one suite in `deb.mirror.suites` (422 `deb_mirror_suites_required`), a bound for the well-known upstream (R-REPO-052) | not saved |
| A suite's release is read | the upstream serves `InRelease` or `Release` with `Release.gpg`; the fill verifies it against the keys and the anti-rollback floor ([deb-remote.md](deb-remote.md)) | no release: the suite is recorded `absent` and noted `suite_unavailable`; a bad signature fails the pass's discovery with 502 `deb_upstream_signature_invalid` and stores nothing |
| The release is used | a key of the current set verifies it (`release_unverified` otherwise), its `Valid-Until` has not passed (`release_expired`), it names the suite | the suite plans nothing |
| An index file is kept | the release lists it; the file's hash is the listed one (the fill checks it) | `index_unlisted`, or the fill stores nothing |
| A package file is kept | the digest and size of the newest parsed index row of the file; the gate of R-DEB-022 verifies the download against it | the item fails and is counted by the pass |

Who may change what a mirror covers: a holder of `repositories:write-config`. A project Owner keeps `deb.mirror`, the patterns, the
upstream URL and the mode as stored and may pause the pass (422 `mirror_restricted`, Q-REPO-142, Q-REPO-193, Q-REPO-295). Removing a key
from a mirror's `deb.upstreamKeys` leaves it unverified: the validation refuses an update that would leave a mirror without keys, and the
replay of a restore disables a mirror that a journaled removal would leave without them ([deb-remote.md](deb-remote.md#the-journal-and-the-restore)).

## What stays out

Index files of other suites, components and architectures, `Contents`, `dep11` and `cnf` files, other translation languages, pdiffs,
`debian-installer` indexes and `.udeb` files are not requested by a pass. A client that asks for one is served by pull-through while
the upstream is reachable and fails otherwise ([H-628](../spec/formats/debian.md#h-628--an-offline-mirror-serves-only-clients-whose-index-targets-it-keeps)).
Package files are never deleted by the mirror or its housekeeping; only by-hash files no remembered release names are
([deb-remote.md](deb-remote.md#keys-saving-rotating-removing)).

Integration tests: `TestR_DEB_024_APassStoresTheScopeAndTheMirrorServesWithTheStubStopped`,
`TestR_DEB_024_TheVersionBoundKeepsTheNewestVersionOfEachNameAcrossTheSuites`, `TestR_DEB_024_ASecondPassIsQuietAndANewGenerationIsStored`,
`TestR_DEB_024_TheByteBoundEndsThePassAndAPackageNotStoredIsPulledThrough`,
`TestR_DEB_024_AReleaseSignedByAnUnknownKeyFailsTheDiscoveryAndStoresNothing`,
`TestR_DEB_024_SourcesAreKeptOnRequestAndClientsInstallDuringThePass` in [test/integration/mirror](../../test/integration/mirror/deb_test.go).

## What this does not cover

- [H-628](../spec/formats/debian.md#h-628--an-offline-mirror-serves-only-clients-whose-index-targets-it-keeps): an offline mirror serves only clients whose index targets it keeps.

### H-744 · A release that expires during a pass is still planned

The index step judges each release when the pass begins (Q-DEB-93). The names and the plans of the rest of the pass read the rows of
suites whose release has `verified_by`, without reading the release again, so a release whose `Valid-Until` passes in the middle of a long
pass keeps being planned until the pass ends. The next pass notes it `release_expired`. A client that reads the mirror refuses the expired
release itself. Live for every mirror; real for passes longer than the remaining validity of a release.

### H-745 · The notes of a pass are shown only when its discovery is truncated

The notes of the index step (`suite_unavailable`, `release_unverified`, `release_expired`, `index_unlisted`) travel in the note of the
first page (Q-DEB-94), which the engine keeps and shows with the status only when the page says the discovery was truncated. A suite that
plans nothing is therefore visible as a pass with fewer names, not as a message. Live for every mirror; real for an operator who
misspells a suite or whose upstream drops one.

### H-746 · The index step parses every index of the scope before the first name of a pass

The first page of a pass fetches and parses the Packages and Sources indexes of all its suites, components and architectures in one call
(Q-DEB-91): each parse holds a transaction while it decompresses, up to 1 GiB, and reads a compressed index of up to 256 MiB into memory.
A mirror of several suites of a large archive keeps the worker busy and its memory high for minutes before the first package is stored,
and a drain during the step repeats it. Live for every mirror; real for mirrors of the well-known archives with several architectures.
