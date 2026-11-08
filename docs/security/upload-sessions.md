# Upload sessions

Status: built in T-16 work package 1 (R-STOR-011, [Q-OCI-12](../spec/formats/oci.md#q-oci-12--uploads-use-the-sessions-of-r-stor-011-mounts-name-their-source-no-automatic-mount),
[ADR-162](../decisions.md#adr-162--the-upload-session-service-is-a-domain-package-under-the-host-and-a-staged-blob-commits-through-the-commit-protocol-with-a-server-side-copy),
[Q-STOR-170](../spec/storage.md#q-stor-170--the-collection-run-aborts-a-live-session-for-its-idle-time-and-an-open-one-for-its-age) to
[Q-STOR-175](../spec/storage.md#q-stor-175--a-part-upload-is-sent-under-a-freshly-renewed-claim-and-ends-before-its-lease-does)).
One blob that a client sends over several requests is a session: a row in PostgreSQL and, from its first full part, one S3 multipart
upload, continued by any replica. This page says who can continue a session, what stands between a session and the blob, what is staged
in the bucket and what checks it, and what the collection run removes. It describes [internal/uploads](../../internal/uploads/)
([service.go](../../internal/uploads/service.go), [append.go](../../internal/uploads/append.go),
[finalize.go](../../internal/uploads/finalize.go), [rows.go](../../internal/uploads/rows.go)), the host's port
([internal/formathost/uploads.go](../../internal/formathost/uploads.go)), the session step of the collection run
([internal/blob/gc_uploads.go](../../internal/blob/gc_uploads.go)), the tables
[`upload_sessions`](../../internal/meta/postgres/migrations/00054_upload_sessions.sql) and
[`upload_session_tails`](../../internal/meta/postgres/migrations/00057_upload_session_tails.sql) and their statements in
[uploads.sql](../../internal/meta/postgres/queries/uploads.sql).

## Who can continue a session

A session id is a random version 4 UUID and appears in the upload URL, so it is not a secret and is not treated as one
([service.go](../../internal/uploads/service.go) `Start`). What lets a request continue a session is a match of three recorded values,
checked on every call, and a decision:

| Check | Where | Failing answer |
|---|---|---|
| The repository the client addressed is the one the session was started on (`addressed_repo_id`) | `row.matches` | 404 `BLOB_UPLOAD_UNKNOWN` |
| The binding (the OCI image name) is the one the session was started with, up to 512 bytes | `row.matches` | 404 |
| The principal is the one that started it (`principal_id`; none for an anonymous start; the id of its token for a CI job, so that two jobs of one identity mapping are two principals) | `row.matches` | 404 |
| The repository the session writes to is still where the addressed repository sends writes (itself, or the current deployment target of a virtual repository) | [`sessions.session`](../../internal/formathost/uploads.go) | 404 |
| `deploy` on that target, decided again on the request's index, with the image name as the package and the repository's include and exclude patterns; a finalize decides it again on the path it commits | `sessions.decide`, `Finalize`, through `Host.Authorize` | 404 for a refusal of `Forbidden`, `NotFound` or `Unauthenticated` kind (a permission revoked during an upload ends it, and a caller that is no longer signed in learns nothing of the session); other errors (an index that is not confirmed) pass |

The first three are one function, so a session of another principal, another repository or another name is the same error as an id that
was never issued, and so is a session that is aborted, past its maximum age or does not exist. The service never tells the cases apart
([unknown](../../internal/uploads/service.go)); the cases for which the answer is a 416 are the ones where the caller has already
proven it is the session's: an offset other than the received count, and a claim another request holds. A request whose session ended
under it (aborted while it wrote) is told 404, not 416.

The service itself decides nothing about permissions. It takes a scope the host built from the request and refuses a session that does
not match it; the host decides `deploy` before it asks. The architecture test places the package in the domain layer and so limits what
it may import; it does not limit who imports it, and today the host (`internal/formathost`) and the composition root are the only
importers. A caller that reached the service another way would skip the decision.

## One writer at a time

A request that carries bytes takes the writer claim in one short transaction: `writer` is set to a fresh random token and
`writer_until` to the database's `now()` plus `storage.uploads.writer_lease`, only if the session is open, younger than
`storage.uploads.session_max_age`, no other claim is unexpired and the request's first byte is the received count
([UploadClaim](../../internal/meta/postgres/queries/uploads.sql)). The lease and the age are judged by the database's clock, never by the
replica's, so two replicas with different clocks cannot both hold a claim. Every write the request makes afterwards (a part, the end of
the body) is conditional on its token, so a request whose lease ran out and whose session another request took records nothing: its
statement affects no row, and it answers 416. No transaction stays open between the claim and the end of the request (R-ARCH-019).

Sending a part and reading the tail are conditional as well. The claim is extended before the tail segments are read (the request that took
the session may have removed them, and a request that finds them gone with its claim lost stops with 416 and judges nothing) and right
before each `UploadPart` (a request that lost the claim creates no multipart upload and sends nothing), and the client side of the upload
has a deadline of three quarters of the lease ([Q-STOR-175](../spec/storage.md#q-stor-175--a-part-upload-is-sent-under-a-freshly-renewed-claim-and-ends-before-its-lease-does)). A store that had received the whole body when the client cut it can still store it, so
the second guard is the one that holds: a store either refuses a part whose ETag is not the one the session recorded or assembles the part
that is named, which the conformance check of R-STOR-035 tests at every start of a replica and the integration tier pins against SeaweedFS.

A request that ends without recording releases its claim at once with a context of its own, so a drain does not make the next request wait
for the lease; only a process that was killed leaves its claim until `writer_until`. The record transactions run under a context of
their own as well: a drain does not cut a COMMIT in flight.

## What is staged, and what checks it

Everything a session stages is below `<prefix>/_uploads/<session-id>/`, apart from the blobs under `<prefix>/sha256/`:

| Object | Content | Checked when it is used |
|---|---|---|
| `data` | the multipart upload of the parts (created with the first part) | the parts carry the payload checksum (`storage.s3.payload_checksum`), the store verifies each on arrival; `CompleteMultipartUpload` names the recorded ETags; the completed object's size must equal the received count |
| `tail-<writer>` | one segment of the bytes after the last part, fewer than one part in all; a blob of less than one part is its single segment or the merge of its segments | each segment is read back once, by the request that completes the part, through a reader that checks the recorded size (a longer object fails at once) and SHA-256; a mismatch, a missing object or a longer one aborts the session and answers an internal error |

A request that lost its claim and still writes its segment writes it under its own token's key, so it cannot replace a segment the session
records. It deletes that segment only when its record failed for certain (the claim was lost). An error that leaves it unknown whether the
transaction committed (a connection that broke after COMMIT, a switchover) deletes nothing: the segment may be the recorded one, and a
segment that was not is removed with the rest of the session.

The four hash functions of the digest set run over the whole content across requests. Their states are stored in `hash_state` and
restored by the next request, so no byte is hashed twice and no request needs the earlier bytes. Go documents that a marshalled hash state
may contain input that was not hashed yet: here up to 127 bytes of the blob (SHA-1, SHA-256, MD5) or 255 (SHA-512) sit in PostgreSQL next
to the digest, in a table of the export class `transient`. The SHA-256 of the whole content is compared with the digest the client names
before the session leaves `open`; a mismatch is a 400 `DIGEST_INVALID` that keeps the session open, with the bytes of the closing request
recorded.

## From the last byte to the blob

A finalize passes through states that are recorded before the next one starts, each idempotent: `completing` (the digest is recorded in
the same statement that records the end of the body), `assembled` (the staging object is whole), `done`. A request that finds the session
in one of the later states continues at it without a claim and without reading its body; its digest must equal the recorded one. The step
that sends the tail as the last part takes a short claim of its own, so that two continuations do not upload it twice; the one that does
not get it is told to retry.

The commit is the commit protocol of R-STOR-005 with a server-side copy of the staging object to the content address as its object write
([blob.Staged](../../internal/blob/staged.go)). No byte passes through the replica, and a blob that is stored already copies nothing.
The item and the end of the session (`done`, with the item's id) are written in the one transaction that links the item, so a finalize cut
after the copy and before that transaction is repeated, and a finalize cut after it is not a second item. A repeated closing request on a
session that is done is answered from the record: nothing is committed again, no second decision is recorded and an item that was
deleted meanwhile is not re-created. The decisions that allowed the write are recorded in that transaction with the item, as for every
other upload ([ingest.go](../../internal/formathost/ingest.go) `commit`).

The staged blob is the digest the session computed, and the commit stores it under that digest. The copy does not read the staging
object ([H-591](#h-591--the-staging-object-is-copied-without-being-read-back)).

## What the collection run removes

The run (R-STOR-025) aborts the sessions without a recorded request for `storage.uploads.session_ttl` and the open ones older than
`storage.uploads.session_max_age`, removes the multipart upload and the objects of the aborted sessions that the request which ended them did not already clean and, once the replay window of a repeated closing request (15 minutes) has passed, of the finished ones, and then their rows; a session that staged nothing, or whose request removed the objects, is deleted in bulk without a call to the store
([gc_uploads.go](../../internal/blob/gc_uploads.go), [Q-STOR-170](../spec/storage.md#q-stor-170--the-collection-run-aborts-a-live-session-for-its-idle-time-and-an-open-one-for-its-age)).
A full sweep also removes what no session row stands for, if it is older than `session_max_age`
([Q-STOR-172](../spec/storage.md#q-stor-172--the-full-collection-sweep-removes-what-no-session-stands-for-until-the-orphan-sweep-is-a-task-of-its-own)):
the objects and multipart uploads of an id without a row, and the multipart upload of a session that is over or whose upload is another.
A directory below `_uploads/` that is not a UUID in its canonical form (lower case, hyphenated) is logged and never deleted. The expiry and the cleaning each have a time budget of two minutes per run; the sweep takes at most 25 pages per run, lists the staging area in pages, saves where it stopped so that the next run goes on from there, and re-reads the deletion pause before every batch: while it is set nothing
is aborted or removed. Every request checks the age at its claim, and the renewals before the tail is read and before each part refuse a session past it, so a session cannot be continued past `session_max_age` (by more than the request that is running) however long the runs are delayed or paused.

`session_max_age` is below `storage.orphans.multipart_max_age`, and startup refuses a configuration where it is not
([config/storage.go](../../internal/config/storage.go)); it refuses `writer_lease` at or above `session_ttl` as well, which would let the
run abort a session whose claim is valid. Keeping the bucket's lifecycle rule for incomplete multipart uploads at one day or longer is the
operator's part: the permissions of R-STOR-031 do not let trove read the rule.

## What it costs a replica

A request that sends fewer bytes than are missing for a part writes them as one tail segment and reads nothing back: its cost is its own
bytes, one write, three short transactions and one claim ([Q-STOR-174](../spec/storage.md#q-stor-174--the-tail-is-a-list-of-segments-that-are-read-once-and-the-multipart-upload-is-created-with-the-first-part)).
The request whose bytes complete a part reads the segments once and joins them with its bytes; its spool holds the part and the segment its
own bytes made ([IngestSegment](../../internal/blob/spool_segment.go) reserves the budget as the bytes arrive and fails with the 503 of a
full spool). A tail with 32 segments is merged by the request that would add one more, which reads it once. What a client can make a
replica spend by sending small chunks is therefore bounded by its own bytes, except for one read of at most a part per 33 requests
([H-594](#h-594--a-merge-of-the-tail-segments-reads-up-to-a-part-for-the-bytes-of-one-request)). A session that is started and not
continued costs a row and no object.

## What this does not cover

- [H-11](../spec/storage.md#h-11--object-store-principals-bypass-troves-authorization-and-integrity-checks): principals of the object store bypass trove's checks; the staging area is part of what they can read and replace.
- [H-13](../spec/storage.md#h-13--deduplication-reveals-the-existence-of-known-content-through-upload-timing): a finalize whose blob is stored already copies nothing and answers sooner.
- [H-14](../spec/storage.md#h-14--no-per-principal-limit-on-concurrent-uploads-or-spool-use): no per-principal limit on bodies in flight or the spool they use; the requests of a session are bodies like any other, and a client that holds many slow bodies (a second byte trickled under the stall timeout) holds the spool of each; the bound of H-14 and not the session design is what limits that.
- [H-552](../spec/storage.md#h-552--a-deduplication-hit-across-projects-can-be-told-apart-by-its-timing): the same timing across projects.

### H-590 · The bytes that open sessions stage are not limited, and the cap on their number is per principal and repository

Component: the session service, the table `upload_sessions` and the staging area of the bucket. Subject: every principal that holds `deploy`
on a repository whose format calls the host's upload port. A `Start` costs a row and nothing in the bucket, and one principal may hold
`storage.uploads.max_open_sessions` (100 by default) open sessions on one repository: a start above it answers 429
([Q-STOR-176](../spec/storage.md#q-stor-176--the-open-sessions-of-a-principal-on-a-repository-are-capped-an-ended-session-leaves-no-row-and-the-session-step-has-a-time-budget)).
The starts of a repository are serialized by the lock of its row, so the cap holds exactly. A session that ends (`DELETE`, a failure, a
finalize) leaves no row once its request removed its objects, so a caller that starts and cancels in a loop does not grow the table; a
finished session stays for 15 minutes. What the cap does not bound: the sessions a caller starts and leaves open, each of which holds a
place for `session_ttl` (4 h) and, once continued, its staged bytes for up to `session_max_age`; the number of principals, tokens and
repositories, each with a cap of its own (so principals times repositories times 100 sessions can be open, and a CI identity gets a new principal per token, several per job); and the bytes, which only
the size limits bound, since a repository has no quota
([H-156](file-transfer.md#h-156--a-repository-has-no-quota-so-a-holder-of-deploy-is-bounded-only-by-the-size-limits)). The collection run
removes expired sessions for a time budget of two minutes per step and run, 200 rows at a time; where the sessions that expire exceed what
the runs remove between them, the table and the bucket grow until the rate falls. Dormant until a format's handlers call the port (the OCI
handlers land with T-16 work package 5); live from then on for every installation with an OCI repository, and real where the holders of
`deploy` are not all trusted. Mitigation outside trove: a bucket quota, a short `session_ttl`, and the rate limit of the endpoint.

### H-591 · The staging object is copied without being read back

Component: the commit of a finalize ([staged.go](../../internal/blob/staged.go) `putStaged`). Subject: every blob stored through a session.
The digest under which the blob is stored is the one the session computed over the bytes that arrived, and the parts were verified by the
store as they arrived; the copy to the content address moves the staging object, the multipart object `data` or, for a blob of less than one
part, its single tail segment or the merge of its segments, by a request to the store and checks only its size. A principal that can write
the bucket (the case of [H-11](../spec/storage.md#h-11--object-store-principals-bypass-troves-authorization-and-integrity-checks))
and replaces that object between its completion and the copy makes trove store other bytes under the digest of the original; the scrub
(R-STOR-027) finds the mismatch later, and a download verifies the digest before it releases the last bytes (R-STOR-013). The window is the
time between the two steps, normally seconds and as long as a finalize that was cut waits for its retry. Live today for every session; it
needs write access to the bucket, which already breaks every other integrity guarantee of the blob store.

### H-592 · The sessions of anonymous callers are one principal's: they share each other's sessions and one cap

Component: the scope check (`row.matches`) and the cap on open sessions. Subject: an endpoint that allows anonymous `deploy`. The principal
of an anonymous session is recorded as none, so every anonymous caller who knows the id, the repository and the image name satisfies the
check, and the only protection of an upload against another anonymous client is that the id is a random 122-bit value that appears in the
upload URL: a caller who reads it (a log, a proxy) can continue or abort that upload, although it cannot swap the content of a blob that
is committed under its digest. The cap is shared too: all anonymous callers are one principal on a repository, so one anonymous client that
starts `max_open_sessions` (100) sessions and leaves them open blocks every anonymous push to that repository for `session_ttl` (4 h). A CI
job is not in this gap: its session and its cap are keyed on its token, which each exchange issues (a job can hold several, and an issuer that does not make an id_token single-use lets one be exchanged several times, so this is a finer key than the identity mapping and no guarantee of one cap per job). Dormant unless an
endpoint allows anonymous writes; live where it does. The same anonymous caller could push the same bytes under its own session anyway,
and could fill the repository with them.

### H-593 · The staged bytes of a deleted repository's sessions outlive it until the next full sweep

Component: `upload_sessions.repo_id` and `addressed_repo_id` with `ON DELETE CASCADE`, and the full sweep of the collection run. Subject: the
operator. Deleting a repository deletes the rows of its sessions with it; the multipart upload and the objects they staged have no row any
more and are removed by the next full sweep (`gc-full`, weekly by default) once they are older than `session_max_age`. Until then the
bucket holds up to the staged bytes of the sessions that were open. Live for every installation that deletes a repository with open
sessions.

### H-594 · A merge of the tail segments reads up to a part for the bytes of one request

Component: the request that completes a part, and the request that would add the 33rd tail segment ([append.go](../../internal/uploads/append.go)
`endOfBody`). Subject: every replica that takes uploads, and its spool. The first reads the segments the earlier requests wrote, which their
bytes paid for; the second reads the whole tail (up to one part, 16 MiB by default) for the bytes of one request, once per 33 requests of a
client that sends very small chunks, and holds it in the spool while it merges. A holder of `deploy` can therefore make a replica read and
write a part per 33 of its tiny requests, not per request; a spool budget that is small against the part size is used up by a few such
merges at once ([H-14](../spec/storage.md#h-14--no-per-principal-limit-on-concurrent-uploads-or-spool-use)). Live today for every session.
`writer_lease` also has to exceed the time one part takes to send, or a part never completes (the upload is cut at three quarters of the
lease): with parts of 16 MiB the default of 60 s needs 0.4 MB/s, a part of 5 GiB needs about 119 MB/s; startup refuses a configuration whose part cannot be sent at 32 MiB/s within three quarters of the lease, and nothing checks the real bandwidth.
