# Ansible Galaxy: credentials, archives and immutability

Perspective: what a caller can do to a local Ansible Galaxy repository through the protocol, what stands between an uploaded archive and the stored
rows, which credentials the Galaxy clients present and how the stage reads them, and what keeps a deleted version number from coming back. The
mechanisms are the scheme `Token` of the authentication stage ([stage.go](../../internal/auth/stage.go),
[dispatcher.go](../../internal/api/dispatch/dispatcher.go)), the archive reader ([collection](../../internal/formats/ansible/collection/archive.go)), the
publish ([publish.go](../../internal/formats/ansible/publish.go)), the commit and removal hooks ([hooks.go](../../internal/formats/ansible/hooks.go)), the serve
check ([servecheck.go](../../internal/formats/ansible/servecheck.go)), the data port ([internal/formatdata/ansible](../../internal/formatdata/ansible/local_store.go)) and its
journal replay ([journal_replay.go](../../internal/formatdata/ansible/journal_replay.go)). The requirements are
[R-ANS-006 to R-ANS-013, R-ANS-029 and R-ANS-031](../spec/formats/ansible.md); the developer view is [ansible.md](../developer/ansible.md). The remote
and virtual classes have their own pages when they are built.

## Credentials

| Client configuration | On the wire | What the stage does |
|---|---|---|
| `token=` in `ansible.cfg`, `ANSIBLE_GALAXY_SERVER_<NAME>_TOKEN`, `--token` | `Authorization: Token <token>` | reads it as a trove token on a route that declares `TokenScheme`, with the checks of a Bearer token; the method is `galaxy_token` |
| `username=` and `password=`, `~/.netrc` | `Authorization: Basic` | the password is the token, the user name is ignored; needs `auth.clients.basic.enabled` |
| a Bearer token from another tool | `Authorization: Bearer` | as everywhere |

**The scheme `Token` is a credential on the Galaxy routes only.** The stage runs before the dispatcher, so it asks the dispatcher whether the
request reaches a route that declares the scheme (`Dispatcher.TokenSchemeRoute`, the same match the dispatcher serves) and asks it at most once and only
for a header with that scheme. On any other route, the management API included, the header is "not a trove token" and is rejected without a
lookup, so the scheme cannot be used to slip a token past a route that never agreed to read it. A malformed token, an unknown, revoked or expired
one, one of a type the endpoint does not accept and one bound to another endpoint are 401, as for Bearer; none falls through to anonymous or to the
session cookie, which is never validated next to an `Authorization` header.

**A 401 carries the Basic challenge.** Every 401 of the format answers `WWW-Authenticate: Basic realm="trove"` in the Galaxy shape, so a client that retries on
a challenge sends the credential it can send. The token is never in a URL, a log line or an error detail: the format's error renderer takes the
text of the error and nothing of the request.

## The archive

An uploaded archive is untrusted until the reader has accepted it ([collection](../../internal/formats/ansible/collection/archive.go), R-ANS-010,
R-ANS-031). It is read once, as a stream, never extracted: names with `..`, absolute names and backslashes, links, devices, sparse files, repeated names and
data after the archive's end are refused; the entry count, the inflated bytes, the compression ratio of every entry and the size of the three documents it
keeps are bounded, and the reader ends with the context's deadline. The archive must be consistent: `MANIFEST.json` names the digest of `FILES.json`,
`FILES.json` lists every file with its digest and no file of the archive is missing from it or unlisted. `meta/runtime.yml` is decoded into a node tree and
walked for one scalar: an anchor, an alias, a tag, a merge key or more than 1,000 nodes ignore the field, and nothing of it is ever decoded into Go
values. A refusal is a failed import task the client prints and stores nothing; a publish that fails before the inspection (a missing credential, a wrong
`sha256`, a body over the limit) is an HTTP error.

**The identity is the archive's.** The name and the version are read from `MANIFEST.json` and checked against the grammar of R-ANS-002 (lower case, a
semantic version); the permission on the name, its ownership and the shadowing check of a first publish are decided on that identity before the commit and
decided again inside the commit transaction under the package row's lock. The collection's `dependencies` are resolved by the client through the same
server, so claims, pin rules and the shadowing check keep an internal name away from a public member (R-ANS-022).

## Immutability and the tombstone

A version is never accepted again with other bytes: the same bytes are a repeat (a new task, nothing else changes), other bytes are 409
`version_exists`, and two versions that differ only in build metadata are one number. A deletion removes the item, the row and the generic version, and
leaves a tombstone keyed by the number with the digest that was deleted; a tombstoned number is 409 `version_tombstoned` whatever the bytes, and is checked
before the stored version, so a number a restore's replay withheld is refused as withheld. Only `manage` clears a tombstone, and the clearing is an audit
event.

**The journal.** `ansible.version.deleted` and `ansible.tombstone.cleared` are written in the transaction of the change and flagged for the revocation
journal. A restore from an older backup brings a deleted version back; the replay of the journaled deletion withholds its number again, drops the restored
row and deletes nothing from the store, and the serve check refuses the archive that stayed (it is one statement by primary key on a download of a local
repository). An entry is a claim, not a fact: it acts only on a local Ansible Galaxy repository the entry names and on a collection name, a version and a digest of
the valid forms; an entry about another repository finds nothing, an incomplete one is skipped. A forged entry can cost the availability of one version until an
administrator clears its tombstone, never data or access. The replay of the clearing frees the number as the administrator did.

## What the management API adds

The import tasks are read with `read` on the repository, the tombstones are listed and cleared with `manage`. A task names the collection and the version and
nothing else of the archive; its id is a random UUID. A virtual repository shows its deployment target's tasks only to a caller who may read the virtual
repository. Tasks are deleted after `ansible.import_task_keep` by a task that touches no version.

## What this does not cover

- [H-841](../spec/formats/ansible.md#h-841--collections-are-not-evaluated-for-vulnerabilities) - collections are not evaluated for vulnerabilities (no OSV ecosystem).
- [H-843](../spec/formats/ansible.md#h-843--a-publish-inspects-the-archive-inside-the-request) - the publish inspects the archive inside the request, under the core's 30-second deadline.
- [H-845](../spec/formats/ansible.md#h-845--galaxy-namespaces-are-not-entities-of-trove) - Galaxy namespaces are not entities of trove.
- [H-851](../spec/formats/ansible.md#h-851--a-collection-archive-beyond-the-cores-reader-bounds-is-refused) - an archive beyond the core's reader bounds is refused.
- [H-852](../spec/formats/ansible.md#h-852--the-restore-of-a-version-from-the-trash-is-not-served) - the restore of a version from the trash is not served.
- [H-853](../spec/formats/ansible.md#h-853--the-readme-of-a-version-is-read-by-streaming-the-archive-up-to-it) - the README of a version is read by streaming the archive up to it.
- [H-854](../spec/formats/ansible.md#h-854--a-caller-whose-grants-hide-versions-makes-the-list-read-all-versions-of-the-collection) - a caller whose grants hide versions makes the list read all versions of the collection.
