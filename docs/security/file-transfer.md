# File transfer: content isolation, hidden existence and the write path

Status: the raw transfer of the generic local class and the items operations of milestone M1 are implemented
([internal/files](../../internal/files/), [internal/api/rawfiles](../../internal/api/rawfiles/); the developer view is
[files.md](../developer/files.md)). This page describes the mechanisms of `/files/` and of the items operations that have a
security relation. The decision itself is [permissions.md](permissions.md); the model, the path rules and the patterns are
[repository-model.md](repository-model.md).

## What is stored is served as stored, to whoever may read it

A generic repository neither scans nor validates what it stores (H-131 in
[formats/generic.md](../spec/formats/generic.md)). Everything below is about keeping stored content from becoming a weapon
against trove's own origin, and about who may write and read it.

## Content isolation (R-ARCH-007)

The UI, the management API and `/files/` share one origin. A file such as `evil.svg` is therefore served so that a browser can
neither render nor script it as part of that origin. [httpserver.ContentWriter](../../internal/httpserver/content.go) wraps the
writer of a response that carries a file:

| Header | Value | Purpose |
|---|---|---|
| `X-Content-Type-Options` | `nosniff` | the declared type is the type; also set on every answer by the chain |
| `Content-Security-Policy` | `sandbox; default-src 'none'` | a document opened from the URL runs without an origin and without scripts |
| `Cross-Origin-Resource-Policy` | `same-site` | another site cannot load the content as a subresource |
| `Content-Disposition` | `attachment; filename*=UTF-8''<name>` | a browser downloads instead of rendering; the name is percent-encoded per RFC 8187 |

The wrapper sets them when it is created, again at `WriteHeader` and again at the first `Write`, so code between those points that
deletes or overwrites one of them does not change what is sent (unit test `TestContentWriter`). The same set, with the same checks,
is on `200`, `206`, `304` and `HEAD`. The directory index of a folder is a page trove renders itself: it has `nosniff`,
`same-site` and the policy without `sandbox`, and no disposition, and every name in it is HTML-escaped and linked as `./` plus the
percent-encoded name, so `javascript:x` stays a relative path. Problem answers carry none of this: their body is not content.
The media type is the one the uploader sent; it is never derived from the content or the name.

## Paths and patterns

The path is canonicalised by `repository.CleanPath` before any authorization (see
[repository-model.md](repository-model.md)), and the paths that `net/http.ServeMux` would silently rewrite (`a//b`, `a/../b`)
reach the handler through `Router.HandleUncleaned` and are refused with `400 invalid_path`, not redirected. The patterns of a
repository are routing rules for every caller: a deploy outside them is `403 not_admitted` for an administrator too, a read or a
delete of such a path is a `404` through `/files/` while the management items operations still show it.

## The order of the checks on a write

A `PUT` is checked in this order, and each step refuses before the next one costs anything:

1. the path (400), then `deploy` on it, before the body is read: a refused request sends no `100 Continue` and reads nothing;
2. class and patterns; the declared length and `storage.upload.max_size` (413) before the spool takes a byte;
3. after the body: length (400) and the digests the client announced (409; nothing is stored);
4. `overwrite`, when an existing item has other bytes, **before** the object is written, so a refused replacement never reaches the
   object store (integration test: the put counter does not move);
5. in the commit transaction the item is locked and the decision is made again for a path that appeared meanwhile.

An allowed write is recorded with the change (`authz.write.allowed`, `artifact.deployed`) in one transaction: a failing audit
insert rolls the write back.

## Hidden existence

A caller without `read` gets the same `404` for a file that exists and one that does not, and for a repository that exists and one
that does not: the answer is made by the decision before any lookup (R-AUTHZ-016), and every "missing" answer on the same route is
built from `authz.NotFound()`. The integration test compares the bodies byte for byte, apart from the request path and the request
ID that the problem echoes. A refused write is `403` whether or not the target exists. An anonymous caller is `401` with a
challenge while anonymous access is off. The listing operations drop what the caller may not read, with one `PathsAllowed` call per
page instead of a refusal per entry, which would write a denial event for every omitted name.

One deliberate exception to "authorization before lookup": the request path looks up a key that the replica's snapshot does not hold
before it decides, so that a repository created through another replica is usable at once (Q-ARCH-40, H-155).

## Integrity of a download

A whole body is verified against the blob's SHA-256 while it is streamed; the last `storage.download.hold_back` bytes are not
released before the digest matched, and a mismatch cuts the connection (R-STOR-013). A range is served as stored and cannot be
verified (H-11 in [storage.md](../spec/storage.md)). Only evidence about the object marks a blob `corrupt`; a transport error aborts
the response and changes nothing. `Content-Length` is always declared, so a response that a shutdown deadline cut is detectable.

## Buffers and restarts

The download statistics and the repository activity are write-only buffers of a replica, flushed in batches and at shutdown; a crash
loses at most one interval of one replica. No request reads them. A request that is cut at the shutdown deadline while its body is
still arriving leaves nothing behind (the spool file is removed); one whose body has arrived runs on for the commit grace
(R-ARCH-024).

## What this does not cover

### H-154 · An item that appears below a folder while its deletion job runs is removed without a check

Component: the job `items-delete` (R-STOR-018). Subject: `delete` on a subtree larger than `storage.delete.max_sync_items`. Live with
milestone M1. The request decides `delete` for every item of the subtree and then enqueues the job, which lists and removes what it finds;
it does not decide again, because a job has no caller. A file that a holder of `deploy` adds below the folder between the check and the
batch that reaches it is removed although the caller of the delete had no `delete` grant on it. A subtree up to the synchronous limit is
checked and removed in one transaction and has no such window. What makes it real: a folder with more than `max_sync_items` entries that
another principal may still write into while it is deleted. The trash can (R-STOR-022, P1) makes it recoverable.

### H-155 · The first request for an unknown repository key costs a query that a known key does not

Component: the lookup of R-ARCH-011 step 2 before the decision (Q-ARCH-40). Subject: the response time of a request for a key that this
replica has never seen. Live with milestone M1. A key that the snapshot does not hold is looked up in PostgreSQL once per entry
lifetime (`cache.unknown_repo_keys`, 60 s) and then answered from the negative cache; a key it holds costs a map lookup. A caller who
measures the first answer for a fresh key can tell a key that exists but is hidden from it (a lookup that finds the row and polls the
change log) from a key that does not exist (a lookup that finds nothing) only for that first request, and only by the extra poll. What
makes it real: probing for the names of private repositories with timing measurements against a replica that has not seen them.
The answer bytes are the same (R-AUTHZ-016); the time is not.

### H-156 · A repository has no quota, so a holder of deploy is bounded only by the size limits

Component: admission of a deploy (R-STOR-004, R-STOR-029). Subject: the storage a principal with `deploy` can consume. Live with
milestone M1. The quota of R-STOR-029 and the `507` it answers are not built; an upload is bounded by `storage.upload.max_size` (and
`config.storage.maxUploadSize` of the repository), by the spool budget of the replica and by nothing else. Distinct content is stored
once per digest, so repeating one file costs nothing, but a stream of different files fills the bucket. What makes it real: a service
account with `deploy` on a repository whose token leaks; grant `deploy` only to the accounts that publish and watch the storage usage of the
repository.
