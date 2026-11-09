# Debian remote repositories

Perspective: what a remote repository of the Debian (APT) format trusts, what it checks before it stores or serves a byte of its
upstream's archive, and what a client, an operator and the upstream can each do to what the repository serves. The mechanisms are the
remote adapter ([remote.go](../../internal/formats/deb/remote.go), [remote_gate.go](../../internal/formats/deb/remote_gate.go),
[remote_names.go](../../internal/formats/deb/remote_names.go)), the handlers that answer a client
([handlers_remote.go](../../internal/formats/deb/handlers_remote.go)), the reader of a Release
([release](../../internal/formats/deb/release/release.go)), the rows and the jobs behind them
([remote_store.go](../../internal/formatdata/deb/remote_store.go), [remote_parse.go](../../internal/formatdata/deb/remote_parse.go),
[housekeeping.go](../../internal/formatdata/deb/housekeeping.go)), the rules of the repository document
([validate_deb.go](../../internal/repository/validate_deb.go)) and the OpenPGP library under the verification
([internal/openpgp](../../internal/openpgp)). The requirements are
[R-DEB-018 to R-DEB-023](../spec/formats/debian.md#r-deb-019--remote-repository-upstream-and-release-documents); the developer view is
[deb.md](../developer/deb.md#the-remote-class). The keys trove signs with are [deb-signing.md](deb-signing.md); the egress, credential and
redirect rules every remote repository follows are [remote-repositories.md](remote-repositories.md) and [egress.md](egress.md).

A remote repository passes its upstream's archive through. It signs nothing (Q-DEB-13): the release documents reach the client byte for
byte, so the client verifies the upstream's own signatures with a key it holds. What trove adds is a second check in front of its cache,
when the operator gives it the upstream's keys.

## Trust boundaries

```
 APT client ---(1)--> trove remote repository ---(2)--> upstream archive (deb.debian.org, archive.ubuntu.com, ...)
   holds the key         verifies, stores, serves           signs InRelease / Release.gpg with its archive key
   verifies InRelease    deb.upstreamKeys (optional)        serves indexes, by-hash files, pool files

 (1) the client's request is decided by the decision point (read; a package file on its name and version, everything else on its path)
 (2) trove's request is built from remote.url and the path the client asked for; the egress, credential and redirect rules apply
```

| Party | Trusts | Can do |
|---|---|---|
| The upstream | by deb.upstreamKeys when set, otherwise for nothing but availability | serve any bytes; with keys, only releases a key of the set signed are stored |
| The operator (repositories:write-config, project Owner) | sets `remote.url` and `deb.upstreamKeys` | change what is trusted; a removal of keys unverifies the releases they verified |
| A reader | the repository and the key it fetched | read what is stored; cannot write (405) or fill beyond the engine's rules for the caller |
| A holder of the object store or database | nothing | not in scope here ([backup-and-restore.md](backup-and-restore.md)) |

## What is checked, and where

| Document | Checked before it is stored | Checked when it is served |
|---|---|---|
| `InRelease` | at most 16 MiB; one cleartext-signed message; `Hash` header (when present) names a signature's hash; one paragraph with a `Date` and a checksum section whose lines parse; `Date` not more than 10 minutes after the database's time. With keys also: a signature of a key of the set that is valid now and was at the signature's time, created before the signature, a hash of SHA-256 or stronger, a signature time not more than 10 minutes ahead; `Suite` or `Codename` is the requested suite; `Valid-Until` has not passed | by the same bytes the client then verifies with its own key |
| `Release`, `Release.gpg` | the `Release` parses as above | the pair verifies with the keys of the remote ([H-736](#h-736--a-release-and-its-releasegpg-are-stored-before-they-are-verified-as-a-pair)) |
| An index file a release lists | its SHA-256 is the one the stored release lists for the path, or nothing is stored (`deb_index_mismatch`) | the stored document is the listed file, or the listed file is read outside the cache and verified ([H-740](#h-740--an-index-in-the-window-between-a-refreshed-release-and-its-stored-document-costs-an-upstream-request-per-client-request)) |
| A path the stored release does not list | not fetched | `404 deb_not_in_release`, no request to the upstream |
| A by-hash file | its SHA-256 or SHA-512 is the digest of its path | the same |
| A package file | the digest and size the newest parsed index lists, when one does | the same; without a listed digest it is stored unverified ([H-625](../spec/formats/debian.md#h-625--pool-files-no-parsed-index-names-are-stored-unverified)) or refused with `require` |

The order of the rules matters in one place: **the anti-rollback**. `floor_date` holds the newest `Date` of any release that was ever
verified for a directory, rises only, and survives the removal of the key that verified it. While keys are set a received release
older than the floor leaves the stored document where it is and counts in `trove_deb_release_rollbacks_total`, so a validly signed old
release replayed by a mirror or an attacker, also one signed by a key that stays in the set, never replaces a newer one. Between
unverified releases (no keys) the received copy replaces the stored one by fetch time, and the 10-minute bound on `Date` keeps a forged
far-future date from freezing the remote. Integration tests: `TestR_DEB_020_ARepositoryWithKeysStoresAReleaseOnlyWhenAKeyOfTheSetVerifiesIt`,
`TestR_DEB_019_AnOlderReleaseDoesNotReplaceAVerifiedOneButReplacesAnUnverifiedOne`.

## Keys: saving, rotating, removing

- **Saving.** `deb.upstreamKeys` is checked when it is written: a block that does not parse, holds secret key material, or holds a revoked,
  expired or non-signing key or an RSA key below 2048 bits answers 422 `invalid_value` at `/deb/upstreamKeys` with the reason. The
  fingerprints are derived and shown read-only in `deb.upstreamKeyFingerprints`, so an operator can compare them through a second channel;
  `signing-key.asc` serves the same certificates.
- **Using.** The stored set is read without the validity check, so a key that expired after it was saved does not break the
  verification by the others; each signature is judged by the key's validity at its time and now (Q-DEB-89).
- **Adding a key** (the upstream rotates, [H-626](../spec/formats/debian.md#h-626--a-verified-remote-fails-closed-when-its-upstream-signs-with-a-new-key)):
  the operator adds the certificate; nothing else changes.
- **Removing a key.** The update clears `verified_by` of every release the removed key verified, in its own transaction, and enqueues
  the remote's `deb-index` job. The job verifies those releases again from the stored bytes (the cache view; no request to the upstream)
  against the new set. A release no key of the new set verifies stays unverified: the files of its directory answer 502
  `deb_upstream_signature_invalid` until a release verifies, its next revalidation is sent without validators, and an upstream 304 can
  never mark it verified. Integration tests: `TestR_DEB_020_AReleaseAnotherKeyAlsoVerifiesIsVerifiedAgainByTheJobWithoutARequest`,
  `TestR_DEB_020_AReleaseNoKeyOfTheNewSetVerifiesIsFetchedAgainWithoutValidators`.
- **Without keys** the release documents are stored unverified and the client verifies them
  ([H-623](../spec/formats/debian.md#h-623--a-pull-through-remote-without-upstream-keys-stores-what-its-upstream-sends)). Such a repository
  cannot be a mirror (422 `deb_upstream_keys_required`) or a member of a virtual repository (422 `deb_member_unverified`, also when an
  update would take the last key from a member), because both would hand on content nobody checked; its document carries the warning
  `deb_upstream_unverified`.

## The journal and the restore

The removal of fingerprints is a part of `repository.narrowed` (`debUpstreamKeysRemoved`) and is journaled: a restore to a point before
the update brings the keys back, and the replay takes the named fingerprints out again, clears the `verified_by` they set and enqueues the
re-verification. A restored document that holds other keys keeps them. A mirror or a member of a virtual repository that the removal
would leave without keys is **disabled** and reported suspended, never left serving unverified content. An entry whose fingerprints are not
forty upper-case hexadecimal digits, or are more than 32, is malformed and skipped. Integration test:
`TestR_DEB_032_TheReplayOfARemovalTakesTheFingerprintsOutOfTheRestoredDocument`.

The rows of a remote are cache rows. A restore with `backup.exclude.remote_caches` deletes the cache and the four tables
(`deb_remote_releases`, `deb_remote_files`, `deb_remote_indexes`, `deb_remote_entries`) for every Debian remote through the storage
delete path (`PurgeRemoteCaches`), so the floors go with them
([H-630](../spec/formats/debian.md#h-630--a-restore-without-remote-caches-forgets-the-newest-upstream-release-a-remote-had-seen)).
Integration test: `TestR_BAK_008_ARestoreWithoutRemoteCachesDeletesTheRowsOfADebianRemote`.

## The shadowing check

A first upload of a name to a local repository that is a member of a virtual repository with a remote member asks the remote whether it
serves the name in the scope of the virtual repository: the suites, components and architectures it serves. The answer is read from
the parsed rows; a suite with no stored usable release, or whose indexes of the scope are not all parsed, makes it undetermined, and the
caller without `manage` gets 503 `shadow_check_unavailable` while the remote's job fetches and parses what is missing
([H-743](#h-743--the-shadowing-check-answers-undetermined-until-a-worker-has-fetched-and-parsed)). A suite the upstream answers 404 or 410
for is recorded `absent` (for the remote's not-found period) and settles the check without a parse. Integration test:
`TestR_DEB_023_TheShadowingCheckAsksTheRowsOfTheScopeAndEnqueuesWhatSettlesIt`.

## What this does not cover

- [H-623](../spec/formats/debian.md#h-623--a-pull-through-remote-without-upstream-keys-stores-what-its-upstream-sends): a pull-through remote without upstream keys stores what its upstream sends.
- [H-625](../spec/formats/debian.md#h-625--pool-files-no-parsed-index-names-are-stored-unverified): pool files no parsed index names are stored unverified.
- [H-626](../spec/formats/debian.md#h-626--a-verified-remote-fails-closed-when-its-upstream-signs-with-a-new-key): a verified remote fails closed when its upstream signs with a new key.
- [H-628](../spec/formats/debian.md#h-628--an-offline-mirror-serves-only-clients-whose-index-targets-it-keeps): an offline mirror serves only clients whose index targets it keeps (live with the mirror, R-DEB-024).
- [H-630](../spec/formats/debian.md#h-630--a-restore-without-remote-caches-forgets-the-newest-upstream-release-a-remote-had-seen): a restore without remote caches forgets the newest upstream release a remote had seen.

### H-736 · A Release and its Release.gpg are stored before they are verified as a pair

`PrepareFill` sees the body of one document, so a `Release` is stored when it parses, and the pair verifies when a request for either is
served, or when the remote's job runs (Q-DEB-78). A pair that does not verify stays in the cache and is refused on every request until
the next revalidation replaces it. The unverified `Release` names no file of its directory (the files come from a verified document
only), so the damage is a refused `Release` and `Release.gpg`; `InRelease`, which every APT since 1.1 reads, is verified before it is
stored. Live for a remote with keys whose upstream serves a `Release.gpg` that does not belong to its `Release`; real for archives APT
reads through `Release` and `Release.gpg` only.

### H-737 · A by-hash or package file is verified only while the checksum policy binds

The remote engine compares the digests a format announces only under the policies `verify-if-available` and `require`
(`Descriptor.VerifyAnnouncedDigests` is false for the format). Under `ignore` and `pass-through` a by-hash file is stored as the upstream
sent it although its path names its digest (R-DEB-021 says the digest is checked whatever the policy says). The client verifies the file
against the index it read, so a wrong file fails at the client and stays in the cache until it is deleted. Live for a Debian remote with
`checksumPolicy: ignore` or `pass-through`; real only for such a configuration.

### H-738 · A failed verification is written once an hour per replica and only where a client meets it

`trove_deb_upstream_signature_failures_total` and the event `deb.upstream_signature_invalid` are recorded by the handler that sees the
error of the fill (Q-DEB-86). A fill made by a job (the parse, the re-verification, the shadowing check's fetch) logs a failure and does
not count it, and every replica writes the event at most once an hour for a repository and directory, so n replicas may write it n times.
An upstream that only a job meets goes unreported in the metric until a client asks. Live whenever a remote has keys; real for operators
who alert on the metric only.

### H-739 · A pdiff patch is passed through unverified

Files below a `*.diff/` directory other than its `Index` are passed through as content (R-DEB-021): the release lists the patches only
through their `Index`, and trove does not parse that file, so a patch is stored without a digest to compare. APT verifies a patch against
the hashes of the `Index` it read, which the release lists, and fails the update on a wrong one. Live for every remote whose upstream
serves pdiffs; real for upstreams that serve wrong patches.

### H-740 · An index in the window between a refreshed release and its stored document costs an upstream request per client request

When the stored index document is another file than the stored release lists (the release was refreshed and the document has not
expired yet, or the other way round), the listed file is read from the upstream outside the cache for every request until the document
is replaced (Q-DEB-80). The bytes are verified against the listed hash, so the answer is right, but each such request is a request to the
upstream that the cache does not absorb. Live in the seconds between a refreshed release and the expiry of its index documents in an
archive without by-hash; real for many clients updating in that window against a rate-limiting upstream.

### H-741 · A plain index path no release lists any more stays in the cache until the unused-retention period

The housekeeping of a remote removes by-hash files and the rows of replaced releases (Q-DEB-84). A plain index path is one cache entry
that the next fill replaces in place; one that no release lists any more (a dropped component) is not fetched again, is not served
(`deb_not_in_release`) and leaves the cache with the engine's unused-retention rule, not with the job. Live for a remote whose upstream
drops indexes; real as disk use until the unused-retention period.

### H-742 · A parse holds a transaction open for its length

The job `deb-remote-parse` deletes the rows of an index path and inserts the new ones in one transaction (R-DEB-023), so that the earlier
rows stay when a bound is crossed. The transaction is open while the index is decompressed and read, which takes seconds for an index of
tens of megabytes, and a second parse of the same directory waits for its lock. Live on the worker role for every parse; real for a
directory with many large indexes parsed together.

### H-743 · The shadowing check answers undetermined until a worker has fetched and parsed

A suite of the scope with no stored usable release, or with indexes that are not parsed, is undetermined (Q-DEB-41). The remote's job
settles it, and runs only on a replica that claims jobs. In an installation without a worker the check stays undetermined and an upload
of a new name answers 503 `shadow_check_unavailable` for a caller without `manage`. Live for a virtual repository with a Debian remote
member; real for an installation that runs only the web role.
