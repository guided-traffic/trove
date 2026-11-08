# Debian signing keys

Perspective: who holds the key that makes the clients of a Debian repository trust its indexes, how it is created, kept, rotated and
restored, and what each party (a manager, a reader, the operator of the database, a holder of the journal) can do to it. The
mechanisms are the key store ([keys_store.go](../../internal/formatdata/deb/keys_store.go), [keys.go](../../internal/formatdata/deb/keys.go)),
its statements ([deb_keys.sql](../../internal/meta/postgres/queries/deb_keys.sql)), the replay of its journaled events
([keys_journal.go](../../internal/formatdata/deb/keys_journal.go)), the management API
([deb_keys.go](../../internal/api/v1/deb_keys.go)), the public key route ([handlers_keys.go](../../internal/formats/deb/handlers_keys.go))
and the OpenPGP library under all of them ([internal/openpgp](../../internal/openpgp)). The requirements are
[R-DEB-015 to R-DEB-018 and R-DEB-032](../spec/formats/debian.md#r-deb-015--signing-keys-one-per-repository-where-the-private-key-lives);
the developer view is [deb.md](../developer/deb.md).

An APT client trusts a repository because it holds the repository's public key. Whoever can make trove sign with that key, or learn it,
can make every client of the repository accept an index of their choosing. This page is therefore about the private key.

## Who may do what

| Operation | Permission | What changes | Event (journaled) |
|---|---|---|---|
| List the keys | `read` on the repository | nothing; the answer holds fingerprints, states, times and public certificates, never a private key | none |
| Fetch `signing-key.asc` | `read` on the repository, decided on the file with no package ([R-DEB-004](../spec/formats/debian.md)) | nothing; the primary and the next certificate as one block, the SHA-256 as its ETag | none |
| Create the first key | the creation of the repository (`repositories:write-config` or project Owner) | the hook generates the primary key in the creating transaction, so a Debian local or virtual repository never exists without one | `deb.signing_key.created` |
| Create the next key (generate or import) | `manage` | a `next` key; every suite is marked changed | `deb.signing_key.created` |
| Promote | `manage` | the next key becomes primary; the old primary is retired and its private key erased | `deb.signing_key.promoted` (journaled) |
| Retire the next key | `manage` | the next key is retired, its private key erased | `deb.signing_key.retired` (journaled) |
| Confirm the retirement of a withheld key | `manage` | the withheld key is retired, its private key erased | `deb.signing_key.retired` (journaled) |
| Reinstate a withheld key | `manage` | to `primary` when there is none, else to `next` when there is none | `deb.signing_key.reinstated` |

The permissions are declared in [api/openapi.yaml](../../api/openapi.yaml) and enforced before the handler runs; a reader or developer of
the project is refused with 403 on every change (integration test `TestR_DEB_015_ReadAndManagePermissions`). Only a local or virtual
Debian repository has keys; any other repository answers 400 `not_applicable`. The primary key cannot be retired directly (409
`deb_key_state`), so a manager cannot leave a repository without a signing key by one call; the only road to that state is the replay
of a restore (below).

## Custody of the private key

- **Generation.** trove generates an OpenPGP v4 key, public-key algorithm 22 (EdDSALegacy) over Ed25519, with the system's random
  source, in the process that serves the request or creates the repository ([openpgp.Generate](../../internal/openpgp/secret.go)). Its
  creation time is the database's transaction time minus one hour and its binding self-signature carries that time, so no signature
  trove makes precedes its key.
- **Sealing.** The secret key is stored in `deb_signing_keys.private_key`, sealed with the stored-secret key ring (AES-256-GCM) under the
  row's id as additional data ([data-and-secret-flow.md](data-and-secret-flow.md#the-stored-secret-key-ring)). The id is made before the
  insert, so a sealed value cannot be moved to another row or repository and still open (integration test
  `TestR_ARCH_021_APrivateKeyIsBoundToItsRow`). The column is in the sealed-column registry, so `trove admin secrets status`, the start-up
  canary and `secrets-reencrypt` cover it ([reseal.go](../../internal/formatdata/deb/reseal.go)).
- **Where it is opened.** Only `SigningKeys` (the index build, for one signing call) and the self-test of `ParseSecret` at generation and
  import hold the plain secret. The format plug-in never sees it: the contracts carry keys as armored bytes and fingerprints, and the
  library is imported by the data port alone. The secret is a byte slice that the API handler wipes after the call; see
  [H-735](#h-735--the-secret-key-of-an-import-and-its-passphrase-stay-in-process-memory-until-the-collector-frees-them).
- **Where it never goes.** Not into an API answer (the schema has no such member), an audit event (details hold fingerprints and states),
  a journal entry, a log line, an error text (the library's errors name a reason and never material), the configuration export, Valkey
  or a replica's disk. A key's `String`, `%v` and log value are its fingerprint. No API signs data a caller chooses: trove is no signing
  oracle.
- **Import.** An uploaded secret key meets the rules of R-DEB-015 in the library before anything is stored: algorithm 22 over Ed25519 or
  RSA of at least 3072 bits, able to sign, not revoked, no expiry, created and bound no later than the database's time, no signing
  subkey, algorithm 27 and version 6 refused. A protected key is opened with the passphrase of the request, which is used once and not
  kept; the stored secret is the unprotected key, sealed. Every refusal is 422 `deb_key_unsupported`. A fingerprint that any repository
  holds in any state answers 409 `deb_key_in_use`, so a retired or withheld key cannot come back by import, and one key cannot serve two
  repositories.
- **Backups** carry the sealed key (export class `data`), so a restore with the key ring in escrow keeps the repository's signature
  identity ([backup-and-restore.md](backup-and-restore.md)).

## Rotation

A key change reaches a client only when its keyring file is replaced, so a rotation has an overlap in which two keys sign. The next key is
created (generated or imported), and from the next build the primary and the next key sign every `InRelease` and `Release.gpg`; the
public block serves both certificates. The overlap ends by a promotion (the next key becomes primary, the old key is retired and its
private key erased) or by retiring the next key. Both write a journaled event, and every key change raises `change_seq` of every suite of
the repository so that the next build re-signs.

All changes of one repository's keys run one after the other: each starts by locking the repository row, and READ COMMITTED gives the
statement after it a fresh snapshot. Two requests that create a next key at once give one 201 and the rest 409 `deb_key_state`; two
promotions give one 200 (integration test `TestR_DEB_015_ConcurrentChangesLeaveOneNextKey`). The unique indexes (one primary and one next
key per repository, one row per fingerprint) are the second line: a violation answers 409, never 500.

What a client sees at each step is the table of [R-DEB-017](../spec/formats/debian.md#r-deb-017--key-rotation-and-what-clients-see). A client
that never replaced its keyring file fails `apt-get update` after the promotion, which is the failure APT is built to show; trove cannot
tell which clients that are ([H-621](../spec/formats/debian.md#h-621--trove-cannot-tell-which-clients-still-trust-only-a-retired-key)).

## The withheld state and the replay of a restore

A restore returns the database to a snapshot, so a key that was retired or replaced afterwards is live again. The journal records
`deb.signing_key.promoted` and `deb.signing_key.retired`, and the replay of a restore applies them with the rules every replay has: it
only restricts and deletes nothing. It sets the key `withheld`: it neither signs nor is served in `signing-key.asc`, and its sealed
private key stays. The entries come from the journal in the object store, which a holder of the runtime S3 credential could write
([H-629](../spec/formats/debian.md#h-629--a-forged-journal-entry-can-withhold-a-repositorys-signing-key-until-an-administrator-acts)), so an
entry is a claim checked against the restored database:

| Entry | The restored repository holds | The replay does | Result |
|---|---|---|---|
| promoted (old A, new B) | A primary, B next | B primary, A withheld | applied |
| promoted | A primary, no B (created after the snapshot) | A withheld; the repository has no primary | applied, with a note naming the repository |
| promoted | B primary already | nothing | already in effect |
| promoted | a primary that is neither A nor B, or B withheld or retired | nothing | subject unknown |
| retired (A) | A primary or next | A withheld | applied |
| retired | A withheld or retired | nothing | already in effect |
| retired or promoted | no such key, a key of another repository, a repository that is not a local or virtual Debian repository | nothing | subject unknown |

A repository without a primary key publishes nothing new (the build fails with `no_signing_key`) while the generation published before
stays served. An administrator then confirms the retirement (`DELETE …/signing-keys/{fingerprint}`, which erases the private key) or
reinstates the key (`POST …/reinstate`). A forged entry therefore costs availability until an administrator acts, never a key; the
common case (a key created before the snapshot and promoted after it) is repaired by the replay itself. Integration tests:
`TestR_DEB_032_ReplayOfAPromotionWithTheNewKeyPresent`, `…WithoutTheNewKey`, `…ReplayOfARetirement`, `…AMalformedKeyEntryIsSkipped`.

## What this does not cover

- [H-620](../spec/formats/debian.md#h-620--a-holder-of-the-stored-secret-key-ring-and-a-database-copy-can-sign-for-every-debian-repository): a holder of the stored-secret key ring and a database copy can sign for every Debian repository.
- [H-621](../spec/formats/debian.md#h-621--trove-cannot-tell-which-clients-still-trust-only-a-retired-key): trove cannot tell which clients still trust only a retired key.
- [H-622](../spec/formats/debian.md#h-622--a-signing-key-created-after-the-newest-backup-is-lost-by-a-restore): a signing key created after the newest backup is lost by a restore.
- [H-627](../spec/formats/debian.md#h-627--an-imported-signing-key-stays-known-to-whoever-imported-it): an imported signing key stays known to whoever imported it.
- [H-629](../spec/formats/debian.md#h-629--a-forged-journal-entry-can-withhold-a-repositorys-signing-key-until-an-administrator-acts): a forged journal entry can withhold a repository's signing key until an administrator acts.

### H-735 · The secret key of an import and its passphrase stay in process memory until the collector frees them

An import arrives as a JSON string. The handler wipes the byte copy it hands to the store, and the store wipes the armored secret it
serializes for sealing, but the decoded request body and the Go strings made from it cannot be overwritten, and the module's parsed key
holds the private scalar until it is collected. A reader of the process's memory (a core dump, a debugger, a swap file on a node without
encrypted swap) can find a key that was imported, a passphrase that was sent, and the opened key of every build that is running. Live
whenever a key is imported or a build signs; real for an attacker who already reads a replica's memory, who also holds the stored-secret
key ring of that replica and so every sealed key it can open ([H-620](../spec/formats/debian.md#h-620--a-holder-of-the-stored-secret-key-ring-and-a-database-copy-can-sign-for-every-debian-repository)).
