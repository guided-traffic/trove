# File transfer: content isolation, hidden existence and the write path

Status: the raw transfer of the generic local class and the items operations of milestone M1 are implemented
([internal/items](../../internal/items/) stores, [internal/files](../../internal/files/) decides over it,
[internal/api/rawfiles](../../internal/api/rawfiles/) and [internal/api/edge](../../internal/api/edge/) serve; the developer view is
[files.md](../developer/files.md) and [items.md](../developer/items.md)). This page describes the mechanisms of `/files/` and of the items operations that have a
security relation. The decision itself is [permissions.md](permissions.md); the model, the path rules and the patterns are
[repository-model.md](repository-model.md).

## What is stored is served as stored, to whoever may read it

A generic repository neither scans nor validates what it stores (H-131 in
[formats/generic.md](../spec/formats/generic.md)). Everything below is about keeping stored content from becoming a weapon
against trove's own origin, and about who may write and read it.

## The UI's content routes (R-API-025, Q-UI-106)

The session cookie reaches the role `api` only, so the UI downloads and uploads through `GET`, `HEAD` and `PUT
/api/v1/projects/{projectKey}/repositories/{repoKey}/content/{path}` and not through `/files/`. The routes are the code of `/files/`
(`rawfiles.Handler.serve`), so every decision in this page applies to them unchanged, and a caller without `read` gets the same `404`.
Three properties are particular to them. They answer a request that the session cookie did not authenticate with the `404` of an unknown
path, before any decision, so a token is never accepted on `api` for bulk traffic. A `PUT` carries the session's CSRF token like every
unsafe request of a session: the check reads the token header and the origin and never the body, so a streamed body is checked like a
decoded one. A `PUT` above `ui.deploy.max_upload_bytes` is refused with `413` when the deploy has decided the caller and before the body is
spooled. Their responses carry the content headers below, `same-origin` among them, because the session cookie authenticated the request.

## Content isolation (R-ARCH-007)

The UI, the management API and `/files/` share one origin. A file such as `evil.svg` is therefore served so that a browser can
neither render nor script it as part of that origin. [httpserver.ContentWriter](../../internal/httpserver/content.go) wraps the
writer of a response that carries a file:

| Header | Value | Purpose |
|---|---|---|
| `X-Content-Type-Options` | `nosniff` | the declared type is the type; also set on every answer by the chain |
| `Content-Security-Policy` | `sandbox; default-src 'none'` | a document opened from the URL runs without an origin and without scripts |
| `Cross-Origin-Resource-Policy` | `same-site`; `same-origin` when the session cookie authenticated the request | another site cannot load the content as a subresource, and with the session cookie, which `SameSite=Lax` sends with a sibling host's subresource requests, no other origin can (Q-ARCH-86, `ContentWriterFor`, unit test `TestR_ARCH_007_SessionContentIsSameOrigin`); the UI's downloads are navigations and its images are same-origin, so neither is affected |
| `Content-Disposition` | `attachment; filename*=UTF-8''<name>` | a browser downloads instead of rendering; the name is percent-encoded per RFC 8187 |

The wrapper sets them when it is created, again at `WriteHeader` and again at the first `Write`, so code between those points that
deletes or overwrites one of them does not change what is sent (unit test `TestContentWriter`). The same set, with the same checks,
is on `200`, `206`, `304` and `HEAD`. The directory index of a folder is a page trove renders itself: it has `nosniff`,
`same-site` and the policy without `sandbox`, and no disposition, and every name in it is HTML-escaped and linked as `./` plus the
percent-encoded name, so `javascript:x` stays a relative path. Problem answers carry none of this: their body is not content.

A format route is wrapped by `formats.ServeRoute` with the same set: `Download` with the disposition, `Metadata`, `Computed` and `Volatile` without it
(`RouteKind.WrapsContent`). A `Volatile` route is a search, and its answer carries package-derived text (titles, descriptions, icon URLs) that an
uploader chose; it is therefore under the headers as a document is (Q-ARCH-258, ADR-137), and `TestR_ARCH_007_ServeRouteWrapsTheContentRoutes` removes the
policy from inside such a handler and reads it back at the status line. Before this, such a route was a contract violation the kit could only report; now the
core guarantees the headers and the kit still fails a `Volatile` route that serves a stored item, or an `Upload`, `Delete` or `Admin` route that returns
package bytes.
The media type is the one the uploader sent, parsed as a media type and stored in its normalised form (a value that does not
parse is `400 invalid_header`); it is never derived from the content or the name.

## Paths and patterns

The path is canonicalised by `repository.CleanPath` before any authorization (see
[repository-model.md](repository-model.md)), and the paths that `net/http.ServeMux` would silently rewrite (`a//b`, `a/../b`)
reach the handler through `Router.HandleUncleaned` and are refused with `400 invalid_path`, not redirected; so is an empty
segment right after the key (`/files/<project>/<key>//x`). The `path` parameter of the items operations follows the same rules once
decoded, the backslash and the 4096-byte and 255-byte limits included, and an oversized path is refused by its length before
anything else looks at it (`CheckItemPath`). The patterns of a
repository are routing rules for every caller: a deploy outside them is `403 not_admitted` for an administrator too, a read or a
delete of such a path is a `404` through `/files/` while the management items operations still show it; their delete removes it
for a holder of `content:bypass` only (Q-AUTHZ-162).

## Raw paths of a package format

`/files/` is the raw view of every repository, npm's included, and the generic layer decides a path of such a repository on the
coordinate the format gives it: the package and the version of `@acme/ui/-/ui-1.0.0.tgz` ([files.go](../../internal/files/files.go)
`locate`, [formats.go](../../internal/files/formats.go); Q-AUTHZ-160). The repository is looked up in the replica's snapshot for
it, before the decision, and the outcome of that lookup is answered only once the decision allowed the request; no item is
looked up before it. A listing and a subtree are decided per entry with the same classification (`authz.Classifier`), and the
items operations' shared authorization step decides the same coordinate. So a grant's name patterns, a token scope's and an
exposure rule's apply to a tarball as to its package, and a repository's name patterns hide a package under `/files/` as on the
npm protocol (`TestR_AUTHZ_005_FilesDecideNpmItemsOnTheirPackage`, `TestR_REPO_005_NameExcludeOfAnNpmRepositoryUnderFiles`).

A deploy through `/files/` into a repository whose format has a protocol of its own (npm) is refused with `409 conflict`, file or
folder alike, after the decision and before the body is read (`FilesSeam.RawDeploy`, Q-ARCH-223): nothing reaches such a repository
beside its publish, so no raw folder can block the path of a version's tarball and no file can sit where no document lists it
(`TestR_STOR_007_NoRawDeployIntoAnNpmRepository`). A delete of such content is a change of every package whose files it removes:
the generic layer hands the removed files to `FilesSeam.CheckRemoval` in the transaction that removes them or queues their job, and
the package ownership of R-AUTHZ-018 is asked for each package under its row lock; one refusal refuses the whole delete
(Q-AUTHZ-161, see [permissions.md](permissions.md)).

## The order of the checks on a write

A `PUT` is checked in this order, and each step refuses before the next one costs anything:

1. the path (400), then `deploy` on it, before the body is read: a refused request sends no `100 Continue` and reads nothing;
2. class and patterns; the declared length and `storage.upload.max_size` (413) before the spool takes a byte;
3. after the body: length (400) and the digests the client announced (409; nothing is stored);
4. the write decided again on the index the replica holds now, after the body and before anything is stored (R-AUTHZ-015's
   commit-time re-check): `deploy` with `overwrite` when an existing item has other bytes, `deploy` alone for a new file or an
   identical re-upload. A grant removed while the body was arriving stops the write, and a refused replacement never reaches the
   object store (integration tests: the put counter does not move; `TestR_AUTHZ_015_ADeployIsDecidedAgainWhenItCommits`);
5. in the commit transaction the item is locked; whether `overwrite` is held was settled before the transaction opened, so a path
   that appeared meanwhile is refused as a decision (`items.ErrOverwriteDenied`, then `Decide` for `overwrite`) and no decision waits
   inside the transaction (Q-STOR-144).

The item store that performs step 5 makes no decision and cannot: `internal/items` does not import `internal/authz` (the
architecture test `TestModuleBans` fails the build of that edge), and the generic layer reaches the object store and the items
tables only through it. The proxy's cache fills and a format's publish use the same commit, so a storage path exists that is not
decided by the generic layer; whoever calls it owns the decision before it (the request that caused a fill was decided before the
fill started, a format's request on the index of its dispatcher), and the audit row of an allowed write is the committer's `Extra`.

An allowed write is recorded with the change (`authz.write.allowed`, `artifact.deployed`) in one transaction: a failing audit
insert rolls the write back.

## Hidden existence

A caller without `read` gets the same `404` for a file that exists and one that does not, and for a repository that exists and one
that does not: the answer is made by the decision before any lookup (R-AUTHZ-016), and every "missing" answer on the same route is
built from `authz.NotFound()`. The integration test compares the bodies byte for byte, apart from the request path and the request
ID that the problem echoes. A refused write is `403` whether or not the target exists. An anonymous caller is `401` with a
challenge while anonymous access is off. The listing operations drop what the caller may not read, with one `PathsAllowed` call per
page instead of a refusal per entry, which would write a denial event for every omitted name. A page reads at most 100,000 rows in
queries that double in size from `limit + 1` up to 5,000 rows, so a page costs a few dozen queries whatever its size; a page that
ends at that bound beyond its last visible entry carries the item id of the last row it read as its cursor, never its path
([children.go](../../internal/items/children.go), Q-STOR-62; `TestR_STOR_017_AListingOfAMostlyHiddenFolder`).

One deliberate exception to "authorization before lookup": the request path looks up a key that the replica's snapshot does not hold
before it decides, so that a repository created through another replica is usable at once (Q-ARCH-40, H-155).

## Integrity of a download

A whole body is verified against the blob's SHA-256 while it is streamed; the last `storage.download.hold_back` bytes are not
released before the digest matched, and a mismatch cuts the connection (R-STOR-013). A range is served as stored and cannot be
verified (H-11 in [storage.md](../spec/storage.md)). Only evidence about the object marks a blob `corrupt`; a transport error aborts
the response and changes nothing. `Content-Length` is always declared, so a response that a shutdown deadline cut is detectable.
A finished response is not left half delivered by an exiting replica either: a connection that closes during the shutdown with
bytes the peer has not acknowledged is held until it has (at most the commit grace), and one that is cut or still overdue is reset,
so the client never takes a truncated body for a complete one and learns of a cut at once (Q-ARCH-187).

## Buffers and restarts

The download statistics and the repository activity are write-only buffers of a replica, flushed in batches and at shutdown; a crash
loses at most one interval of one replica. No request reads them. A request that is cut at the shutdown deadline while its body is
still arriving leaves nothing behind (the spool file is removed); one whose body has arrived runs on for the commit grace
(R-ARCH-024).

## A delete and its job

A delete decides `delete` on every item of the subtree, each on the coordinate its format gives it, and, for a package format, the
ownership of every package whose files it removes; one refusal refuses the whole delete before anything is removed (R-STOR-018,
Q-AUTHZ-161). A subtree up to `storage.delete.max_sync_items` is decided and removed in one transaction, on the rows that transaction
removes. A larger one is decided in full at the request and removed by the job `items-delete`, which removes only items up to the
highest id the request saw (Q-STOR-143). A subtree that empties between the count and the check queues no job, and a job that names
no such id, or no subject, removes nothing (Q-STOR-192).

The job acts for the caller only while a request of that caller would be allowed (Q-STOR-190). It carries who asked: the principal,
the ID of the token or the session, and the endpoint ([authz.JobSubject](../../internal/authz/jobsubject.go)), never a grant and never a
secret. Before each batch it reads what that caller holds then ([auth.JobStanding](../../internal/auth/standing.go), Q-AUTHZ-260): the
principal's status, kind, name and groups from the principal cache under the freshness gate of R-AUTH-023, and the token's or the
session's row by its ID from PostgreSQL, from which the token's scope and a CI token's frozen bundle come; for a session it follows the
sessions a re-authentication replaced it by (Q-AUTHZ-262). It finds the repository by its ID: a repository moved to another project is
decided, and named, where it is now, one created under the old name is never touched, and one that is gone or being deleted ends the job
as done; a disabled repository stops it unless the caller holds `content:bypass`; and a batch whose index still holds another
repository under that name waits for the next attempt rather than decide on it (Q-STOR-191). On that subject, and on the index
installed at that moment, the batch decides `delete` on each of its items, the ownership of the packages of its files and every path a
storage hook cascades to ([items/delete.go](../../internal/items/delete.go) `BatchGuard`, [files/delete.go](../../internal/files/delete.go)
`DeleteJobGuard`). The job stops without a further attempt, keeping what is left, naming the reason in its error and recording
`artifact.delete_stopped` with the caller as the actor (Q-STOR-193), when:

| Change | The job's error | Seen by the next batch |
|---|---|---|
| The token was revoked, for any reason | `invalid_token` | at once: the row is read by ID |
| The token's row was purged after it ended (`auth.tokens.retention_after_end`) | `invalid_token`, "no longer exists" | at once (Q-STOR-194) |
| The session was withdrawn: a sign-out (also the end of a user's other sessions), an administrator's end, a disable or a changed break-glass password, of the session itself or of a session that replaced it (a re-authentication, a sign-in of the same principal in the same browser), also after it had ended by time or by the session limit | `session_expired` | at once: the rows are read by ID |
| The session's row was purged, 30 days after its end | `session_expired`, "no longer exists" | at once (Q-STOR-194) |
| The principal was disabled, deleted or changed its kind (its sessions end with it; its tokens need not be revoked) | `principal_disabled` | within `auth.revocation_bound` |
| The principal left the group whose grant covered an item | `forbidden`, naming the root path | within `auth.revocation_bound` |
| A policy, a role binding or the endpoint's exposure no longer covers an item, the repository moved where the caller has no grant, or it was disabled | `forbidden`, naming the root path | within the bound of the index ([permissions.md](permissions.md)) or of the snapshot |
| A format refuses the removal of a batch's files (the package ownership of R-AUTHZ-018) | the format's code, `package_owned_by_other_owner` | in the batch's transaction |

A 403 names the path the request named, never the refused item, which may be one the caller cannot read: the job's error is read by
its creator and by every holder of `settings:read`. A cause behind an error, a database's text among them, goes to the replica's log and
never into the job's error (Q-STOR-193). A token that reached its expiry and a session that timed out, was evicted by the session limit
or was replaced by a re-authentication do not stop the job: nobody withdrew them, and the request they authenticated was accepted while
they lived (Q-AUTHZ-261, H-659). A store that does not answer, or an index that is not current, fails only the attempt; the queue
retries it. A job is never run without the guard. The integration tests withdraw each case before the job's first batch and revoke a
token while a running worker is between two batches (`TestR_STOR_018_TheDeleteJobDecidesEveryBatchOnTheStandingOfItsSubject`), stop
a job between two batches through the component test stage (`TestR_STOR_018_AWithdrawalBetweenTwoBatchesStopsTheJobAtTheNext`), move
and delete the repository between two batches (`TestR_STOR_018_TheDeleteJobFollowsItsRepositoryByID`), and refuse a later batch's
ownership and a folder batch (`TestR_STOR_018_EveryBatchOfTheDeleteJobAsksTheFormatAndDecidesItsFolders`).

## What this does not cover

The gaps of the spec pages whose mechanism this page describes:

- [H-3](../spec/architecture.md#h-3--ui-and-package-content-share-one-origin): the UI and package content share one origin.
- [H-11](../spec/storage.md#h-11--object-store-principals-bypass-troves-authorization-and-integrity-checks): object-store principals bypass trove's authorization and integrity checks.
- [H-13](../spec/storage.md#h-13--deduplication-reveals-the-existence-of-known-content-through-upload-timing): deduplication reveals the existence of known content through upload timing; the same signal across projects is H-552.
- [H-552](../spec/storage.md#h-552--a-deduplication-hit-across-projects-can-be-told-apart-by-its-timing): a deduplication hit across projects can be told apart by its timing; it is H-13's signal, accepted by the owner for the cross-project case.
- [H-14](../spec/storage.md#h-14--no-per-principal-limit-on-concurrent-uploads-or-spool-use): no per-principal limit on concurrent uploads or spool use.
- [H-131](../spec/formats/generic.md#h-131--generic-content-is-served-as-stored-to-whoever-can-read-it): generic content is served as stored, to whoever can read it.

### H-658 · The batch in flight when a withdrawal commits still removes its items

Component: the job `items-delete` (R-STOR-018, Q-STOR-190). Subject: the items of one batch of a delete larger than
`storage.delete.max_sync_items`. Live for every such job. A batch reads the standing of its subject and takes its index before its
transaction opens, and removes its items in that transaction; a revocation, a disable, a group left or a grant removed that commits
after the read does not stop that batch. So up to `storage.delete.batch_size` files (5,000 by default), or one batch of folders, go
after the change, and for a disable or a group left, whose reach goes through the principal cache, the batches that begin within
`auth.revocation_bound` after it as well. A batch whose transaction fails over is tried again by the transaction runner for up to
`database.retry.max_elapsed` on the standing it read before the first try (R-ARCH-019), so its window is that long. Every later batch
removes nothing. What makes it real: a stolen credential that queued a large delete, revoked while a batch is running. A request has
the same window between its decision and its commit (H-104 in [permissions.md](permissions.md)); the trash can (R-STOR-022, P1) makes
it recoverable. Reading the token or session row `FOR SHARE` inside the batch's transaction would close the window for those two.

### H-659 · A delete job outlives the expiry of the credential that queued it

Component: the job `items-delete` (R-STOR-018, Q-AUTHZ-261). Subject: a delete queued with a token that expires, or a session that
times out, before the job ends. Live for every such job. The job stops when the credential is withdrawn, not when it reaches its end
of life: a token past its expiry, and a session past its idle or absolute deadline, evicted by the session limit or replaced by a
re-authentication, keep the job running, on the principal's groups and status as they are at each batch. A credential that no longer
authenticates any request so still has one job acting for it. The bound is the credential's row (Q-STOR-194): a token's row is purged
`auth.tokens.retention_after_end` after its expiry (default 2160 h, at least 1 h), a session's 30 days after its end (R-AUTH-043), and
the next batch then stops the job with "no longer exists". A CI token's contributions of a project tier that end before the token
(`ExpiresAt`) stop counting while the job runs, but the contribution of the mapping that owns the token has no such end, so the owner's
grants act for the job until the row is purged. The rules of the endpoint the request arrived on are not asked again either: the
token types it accepts, its bound-tokens-only rule, its anonymous admission and a scope's endpoint binding are decided at the request
only, while the endpoint's exposure ceiling is applied to every batch (dormant: a change of an endpoint's authentication rules needs a
restart, which the job may outlive). What makes it real: a short-lived token (a CI token, a token about to expire) that queues a large
delete behind other jobs, so that the job begins after the expiry; the owner of a leaked token stops it by revoking the token, as for
every other use of it, and an administrator by disabling the principal, removing its grants or cancelling the job.

### H-155 · The first request for an unknown repository key costs a query that a known key does not

Component: the lookup of R-ARCH-011 step 2 before the decision (Q-ARCH-40). Subject: the response time of a request for a key that this
replica has never seen. Live with milestone M1. A key that the snapshot does not hold is looked up in PostgreSQL once per entry
lifetime (`cache.unknown_repo_keys`, 60 s) and then answered from the negative cache; a key it holds costs a map lookup. A caller who
measures the first answer for a fresh key can tell a key that exists but is hidden from it (a lookup that finds the row and polls the
change log) from a key that does not exist (a lookup that finds nothing) only for that first request, and only by the extra poll. What
makes it real: probing for the names of private repositories with timing measurements against a replica that has not seen them.
The answer bytes are the same (R-AUTHZ-016); the time is not. This is the decision of Q-AUTHZ-152: the specification promises the
bytes and records the time here; padding the first answer was weighed and not chosen.

### H-156 · A repository has no quota, so a holder of deploy is bounded only by the size limits

Component: admission of a deploy (R-STOR-004, R-STOR-029). Subject: the storage a principal with `deploy` can consume. Live with
milestone M1. The quota of R-STOR-029 and the `507` it answers are not built; an upload is bounded by `storage.upload.max_size` (and
`config.storage.maxUploadSize` of the repository), by the spool budget of the replica and by nothing else. Distinct content is stored
once per digest, so repeating one file costs nothing, but a stream of different files fills the bucket. What makes it real: a service
account with `deploy` on a repository whose token leaks; grant `deploy` only to the accounts that publish and watch the storage usage of the
repository.

### H-208 · A forged listing cursor tells where a hidden item sorts

Component: the listing of `/files/<project>/<key>/<folder>/` and `/api/v1/projects/{projectKey}/repositories/{key}/children` (R-STOR-017, Q-STOR-62). Subject: the
names of entries the caller may not read. Live for every listing. A page that ends at the scan limit carries the id of the last
row it read, and a cursor is base64 JSON that a client can write itself. A caller who puts the id of another item of the same
repository below the listed folder into a cursor gets the visible entries that sort after that item, and so learns between which
two visible names the hidden one sorts. It learns no name, and only of items in the folder it may list; ids of other repositories
and of purged items are `400 invalid_cursor`. What makes it real: a folder whose visible names are dense enough to narrow a hidden
name down, and a reason to guess ids. A cursor sealed with a cluster-wide key would close it (Q-STOR-62, option b).

### H-209 · A holder of `deploy` without `read` learns whether a file exists

Component: the deploy of `/files/` (R-STOR-007). Subject: the existence and the content of files the caller may not read. Live in
the MVP for every principal that holds `deploy` without `read` on a path (a write-only drop box). R-STOR-007 answers an identical
re-upload `200`, a new file `201`, different content on an existing file `403` without `overwrite`, and a path below an existing
file `409 path_conflict` naming the ancestor; so such a principal learns that a path holds a file, and, by uploading a guess,
whether the file holds exactly those bytes. Hiding it would make the deploy answer like a new file and lose the status semantics
that clients rely on. What makes it real: a drop box whose readers keep files the depositors must not confirm, such as a shared
inbox for reports. The mitigation is a path pattern of its own for every depositor.
