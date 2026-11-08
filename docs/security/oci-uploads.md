# OCI blob uploads and the cross-repository mount

Status: built in T-16 work package 5 (R-OCI-011, R-OCI-012,
[ADR-175](../decisions.md#adr-175--the-upload-and-mount-handlers-hold-the-protocol-and-the-host-holds-the-session-and-the-link),
[Q-OCI-110](../spec/formats/oci.md#q-oci-110--a-mount-that-cannot-be-served-opens-a-session-and-says-nothing-more-and-one-function-parses-its-source) to
[Q-OCI-114](../spec/formats/oci.md#q-oci-114--the-grant-on-the-source-of-a-mount-is-the-registry-roots-check-the-read-right-is-the-hosts-link)).
This page says what a request to the upload routes can reach, what the handlers decide and what they leave to the host, and what a mount tells a caller. The
session itself (who may continue it, the claim, the staging) is [upload-sessions.md](upload-sessions.md); the registry token that carries the grant is
[oci-registry.md](oci-registry.md). The code is [handlers_upload.go](../../internal/formats/oci/handlers_upload.go) and
[handlers_mount.go](../../internal/formats/oci/handlers_mount.go), the link is
[`linkSource`](../../internal/formathost/ingest.go), the hook check is [`OnCommit`](../../internal/formats/oci/hooks.go).

## What decides what

```
 POST/PATCH/PUT/GET/DELETE …/<name>/blobs/uploads/…
     |
     |  dispatcher: the coarse check, deploy on the repository (before the body is read)
     v
 handler (the format)            host
 --------------------            ----
 session id is a UUID?   --no--> 404 BLOB_UPLOAD_UNKNOWN
 Content-Range well formed? --no-> 400 BLOB_UPLOAD_INVALID (no session call)
 Uploads().X(ref{id, name}) ---> principal, addressed repository, image name match? deploy on the target still allowed?
                                   no -> 404 BLOB_UPLOAD_UNKNOWN (the answer for an id never issued)
```

| Question | Decided by | Where |
|---|---|---|
| May the caller write to the repository at all | the dispatcher's coarse check, before the handler | R-ARCH-005 step 4 |
| Does the session belong to this caller, repository and image name | the host, on every session call | [uploads.go](../../internal/formathost/uploads.go) `sessions.session`; the handler passes the image name of the path as the binding and never takes it from the session |
| May the caller write this blob path | `Authorize(deploy)` on `<name>/_blobs/<digest>` in the monolithic upload and the mount; the host's `Finalize` for a session, and the host's decision on every session call on the package's document path (`<name>/_taglist`) | patterns and grants by path see a path of the package |
| May the caller write this name at all (R-AUTHZ-018) | `CheckOwnership` without a publish (the name's first version while it has no live version) at the start of a session, a monolithic upload, a mount and the finishing `PUT`, after `deploy` is decided and before a body is read | [Q-OCI-115](../spec/formats/oci.md#q-oci-115--blob-uploads-and-mounts-ask-the-ownership-rule-without-a-publish-before-a-body-is-read) |
| Is the content the digest's | the upload pipeline and the session's finalize compute it; the handler compares a monolithic upload's; the commit hook checks every blob item | [hooks.go](../../internal/formats/oci/hooks.go) |
| May the caller read a mount's source | the host's `Link`: a repository of the target's format; the read as `Host.Authorize` decides it on a request derived for the source (decision, include and exclude patterns, the refusal of a claimed name for a remote); for a virtual source the routing and the per-member read check | [`linkSource`](../../internal/formathost/ingest.go); not the format's serve check |
| Does the registry token carry `pull` on the source | the registry root's enforcement point, with the repository and name `oci.MountSource` returns for the bound repository the match read (`EndpointFacts.BoundRepo`) | [Q-OCI-102](../spec/formats/oci.md#q-oci-102--the-pull-check-on-a-mounts-source-is-made-by-the-enforcement-point-and-answers-as-an-unreadable-source): the enforcement point drops `mount` and `from` of a request whose grant lacks `pull` there |

A handler never reads a session by its id alone: the integration tests send the session of one image name under another name, another repository and
another principal on all four methods and get the same 404 each time, and the owner's session is untouched
(`TestR_OCI_011_ASessionOfAnotherPrincipalOrNameIs404`).

## What a mount says

A mount is the one request in which a caller names a repository it may not read. The handler gives that name no effect it can observe:

| What is wrong with the source | Answer |
|---|---|
| the repository does not exist, the name holds nothing, the blob is not there, a finding blocks the blob, the digest names a manifest | 202 with a new session |
| the repository exists and the caller may not read it | 202 with a new session, the same headers |
| `from` is not an image name, or is missing | 202 with a new session |
| the digest is not a sha256 digest | 400 `DIGEST_INVALID`, whatever the source |
| the caller may not deploy to the target name | 403, as for any push |

`TestR_OCI_012_AMountFromAnUnreadableRepositoryOpensASessionAndSaysNothingMore` compares the whole answer (status, headers other than the session's own
id, body) of six such mounts with that of an unreadable repository, on a running stack with three replicas; the unit test does the same for ten more
cases. The answers differ in time ([H-661](#h-661--a-mount-answers-differently-in-time-for-a-repository-that-does-not-exist-one-of-another-format-and-one-the-caller-may-not-read)).

The source is always a path of the form `<source name>/_blobs/<digest>` of a repository the host resolves by name, and the host refuses a repository of
another format than the target's (the answer is the same `NotFound`). `Link` takes size and digests from the source's blob row, so as defence in depth the
hook of the target refuses a blob item whose SHA-256 is not the digest in its path, a name that is no digest and a commit without a SHA-256 (409 `DENIED`):
a blob item cannot hold content its digest does not name, whatever commits it (`TestR_OCI_012_TheBlobHookRefusesWhatItCannotVerify`,
`TestR_OCI_012_TheHookRefusesABlobItemThatIsNotItsDigestsContent`). The read of the source is the read: an exclude pattern the source repository gained after the blob was pushed hides it from a
mount as from a pull (`TestR_OCI_012_AnExcludePatternAddedAfterThePushHidesTheSourceFromAMount`), and a member of a virtual source that the caller may not
read, or that the routing drops, is not consulted (`TestR_STOR_008_LinkResolvesTheSourceByNameAndChecksIt`). The digest of a manifest is no blob of the
source: the layout keeps `_manifests/` and `_blobs/` apart, so such a mount is the 202 above (`TestR_OCI_012_AManifestDigestIsNoBlobSource`).

A mount copies no bytes (the integration test counts the object store's puts on the replica that served it) and links the item the caller proved it can read; the
new item is a normal item of the target name, so the audit event of an upload and the recorded write decisions are written with it.

## The grant on the source

A registry token needs `pull` on the source of a mount besides `push` on the target (R-OCI-006). The format cannot see the token, so the enforcement point of
the registry root does: for a request of the route `oci.upload.start` with `mount` and `from` whose grant lacks `pull` on the source, it drops both parameters, and the handler opens an
ordinary session (202), the answer for a source that does not exist or that the caller may not read (Q-OCI-102). It reads `from` with the function the handler reads it with
(`oci.MountSource`) and against the same bound repository: the match sets `EndpointFacts.BoundRepo`, which for a host bound to a virtual repository is the virtual one and not the
deployment target a write is redirected to, so the enforcement point and `handleMount` name the same repository, image and cache view. Both read the first value of a
repeated `from` or `mount`, and the strip removes every value. `TestR_OCI_012_AMountThroughV2IsLimitedByTheGrantOnItsSource` proves it through `/v2/` on the shared host and on a bound host: a token with
`pull` on the source links (201), one without it gets a session and links nothing (202), a principal who may not read the source gets the same 202 whatever its token carries, and repeated parameters change
neither the check nor the link.

## What this does not cover

### H-661 · A mount answers differently in time for a repository that does not exist, one of another format, and one the caller may not read

Component: `linkSource`. Subject: a holder of `deploy` on any repository of the format. Every such mount is the same 202, but the work differs: a repository that does not exist ends at the
repository reader; one that exists but is of another format, disabled or being deleted ends after it, before any decision; one of the format continues to a decision on a fresh index, the
read of the source and the lookup of the item. A caller who measures many mounts can tell the three apart and learn which repository keys exist, and which are of another format or disabled. Not measured; live
today for every installation with more than one repository. A replica whose authorization state has not learned a repository yet, or cannot confirm itself, answers the 202 as well (it is a refusal like the others).

### H-662 · The ownership of an image name is asked when an upload starts and when it finishes, not on the chunks between, and not in the commit

Component: `checkOwnership` ([handlers_upload.go](../../internal/formats/oci/handlers_upload.go)) and the session port. Subject: the owner of an image name. The rule of R-AUTHZ-018 is asked
without a publish at the start of a session, at a monolithic upload, at a mount and at the `PUT` that finishes a session, before the body of each is read
([Q-OCI-115](../spec/formats/oci.md#q-oci-115--blob-uploads-and-mounts-ask-the-ownership-rule-without-a-publish-before-a-body-is-read)); a `PATCH` is decided for `deploy` only, and the commit of a blob
makes no ownership claim and asks the rule no more (it creates no package, and the blob branch of `OnCommit` holds no ownership state). A `PUT` or a monolithic upload whose check passed
before the name gained an owner therefore still commits; its window is the time its body takes. A name that gains an owner, or a principal who is removed from the name's owners, between a `PATCH` and
the `PUT` of one upload lets that `PATCH` stage its bytes (at most what the session cap and the upload limit allow, up to `storage.uploads.session_max_age`); the `PUT` that would commit them is
refused, as a session the caller may not continue (404 `BLOB_UPLOAD_UNKNOWN`, as the host answers a `PATCH` whose `deploy` is gone). The staged bytes are removed with the expiry of the session. Live today.

### H-663 · An upload through a virtual repository records and names the deployment target, not the repository the client addressed

Component: the dispatcher's redirect of a write to the deployment target ([dispatcher.go](../../internal/api/dispatch/dispatcher.go) `redirectClass`) and `sessionLocation`. Subject: a client that
pushes to a virtual repository. R-OCI-011 has the session record the repository the client addressed and R-OCI-028 has the push executed on the target; the dispatcher replaces the repository of
the request with the target and keeps no addressed repository, so the session records the target as both and the `Location` of a session and of a blob names the target's address. A client that
follows the `Location` talks to the target (it may lack the right there, or fall outside the virtual repository's exposure). The upload routes serve the local class, and there is no OCI virtual class yet:
dormant today; it becomes real with the class, which builds the path (the dispatcher keeps the addressed repository on a write redirect, the host accepts an addressed virtual as the session's, the `Location` comes from the link builder for the addressed repository).
