# User avatars

Status: implemented on 2026-11-07 (R-API-055, [Q-API-207](../spec/api-and-ui.md#q-api-207--an-avatar-is-read-at-a-path-named-by-its-digest)
to [Q-API-210](../spec/api-and-ui.md#q-api-210--the-user-record-carries-avatarurl-and-its-etag-follows-the-avatar),
[Q-AUTH-195](../spec/identity-and-authentication.md#q-auth-195--any-authenticated-caller-reads-an-avatar) to
[Q-AUTH-197](../spec/identity-and-authentication.md#q-auth-197--setting-and-removing-an-avatar-are-two-identity-events-without-image-bytes),
[Q-STOR-166](../spec/storage.md#q-stor-166--the-avatar-image-is-a-blob-of-the-object-store-not-a-column) and
[Q-STOR-167](../spec/storage.md#q-stor-167--a-row-names-the-avatar-the-blob-reference-rules-keep-and-collect-it)). A signed-in user
can upload a picture for their own record. The body is the one piece of untrusted binary input that the management API decodes
itself, so this page says what the server does with it, what it refuses, what it stores and what it serves.

It describes [internal/avatar](../../internal/avatar/) ([process.go](../../internal/avatar/process.go),
[orient.go](../../internal/avatar/orient.go), [store.go](../../internal/avatar/store.go)),
[internal/api/v1/avatar_api.go](../../internal/api/v1/avatar_api.go) and the table `user_avatars`.

## What comes in, and in which order it is judged

The checks run in this order, each before the cost of the next ([Process](../../internal/avatar/process.go)); the first that fails
answers.

1. **The declared type** (`Content-Type`) must be `image/png`, `image/jpeg` or `image/webp`, else `415`. It is checked before a byte
   of the body is read.
2. **The length**, while it is read: the body is read through a reader that stops at 1 MiB + 1 byte, so a longer body is `413` and
   is never held, whatever `Content-Length` claims.
3. **The content decides the type.** The first 512 bytes are sniffed (`http.DetectContentType`). They must be a PNG, a JPEG or a
   WebP, else `415`, and the same one as declared, else `415`. An SVG, an HTML page, a GIF, a script with a GIF header and a file
   with a fake `Content-Type` are all refused here. SVG is never decoded, at any stage.
4. **Animation** is refused with `422`: a PNG with an `acTL` chunk before its image data, a WebP with the animation flag of its
   extended header. The decoders would otherwise read the default image or fail in ways that differ per format.
5. **The size comes from the header, before any pixel is allocated** (`DecodeConfig` of the format): at least 32 x 32 and at most
   4,194,304 pixels in all, the product taken in 64 bits, else `422`. A PNG that declares 100,000 x 100,000 pixels in a few dozen
   bytes, a JPEG frame header of 65,535 x 65,535 and a WebP canvas of 16,000 x 16,000 are refused here, and the tests assert the
   refusal is the size and not a failed decode.
6. **The work and the memory, from the header.** A JPEG is walked marker by marker, stepping over the entropy-coded data, and one
   with more than 64 scans is `422` `too_complex`: the decoder walks every block of the image once per scan, so a progressive
   file of 2048 x 2048 pixels with tens of thousands of tiny scans would cost minutes of CPU for under 1 MiB of upload. The number
   of scans is not the whole bound: a scan of a four-component image costs four times one of a gray image, and a scan can be a few
   bytes, so the scans are also summed in block visits (the components a scan carries times the blocks of a component, twice for
   an AC refinement scan) and more than 3,145,728 visits, 48 scans of a 2048 x 2048 component, is the same `422`
   ([Q-UI-174](../spec/api-and-ui.md#q-ui-174--the-scans-of-a-jpeg-are-bounded-by-block-visits-and-the-decoders-input-is-read-in-short-pieces)). Then the
   memory of the decode is estimated from the header (`decodeCost`): the decoded image at 4 bytes a pixel for an 8-bit PNG, 8 for
   a 16-bit PNG or a WebP, the components of a baseline JPEG (3, or 4 for CMYK), 5 per component for a progressive JPEG (which
   keeps the coefficients of every block), plus the scaler's buffer of 256 x short side x 32 bytes. Above 48 MiB the answer is
   `422` `invalid_dimensions`; a progressive three-component JPEG is therefore accepted up to about 1,600 x 1,600 pixels, a
   progressive four-component one up to about 1,400 x 1,400, an 8-bit PNG or a baseline JPEG up to 2048 x 2048.
7. **A decoding slot.** A replica decodes two images at a time; a request waits up to 2 s for a slot and then answers `503
   server_busy` with `Retry-After: 1`. The admission memory guard of the chain charges a request what a download holds
   (Q-ARCH-328), not a decode, so the processor bounds its own memory: two slots times at most 48 MiB is 96 MiB (H-563).
8. **The decode, with a deadline.** `image/png` and `image/jpeg` of the standard library, and the decoder of
   `golang.org/x/image/webp` (the encoder is not imported). The decoders read their input as they go, through a reader that fails
   once the decode's context is done: 5 s after it started, or when the client leaves. The reader returns at most 16 bytes a read,
   because the decoders fill 4 KiB buffers and a file of few bytes and many scans would otherwise run all its scans after its last
   read, without the deadline being looked at. A decode that exceeds the deadline is `422`
   `too_complex`; a client that left gets no answer. A decoder that panics on a crafted file is recovered and counts as an image
   that does not decode. A truncated or damaged image is `422` `undecodable_image`.

## What is stored: the pixels and nothing else

After the decode the server does not keep a byte of the upload. It takes the centre square of the decoded pixels, scales it to
256 x 256 (Catmull-Rom of `golang.org/x/image/draw`) and encodes a new PNG with the standard encoder from the pixels alone.

- **No metadata survives.** EXIF (the GPS position, the camera, the time), XMP, ICC profiles, comments and text chunks are not
  copied: the encoder writes `IHDR`, `IDAT` and `IEND` only. The test builds a JPEG with an Exif block and a comment and a PNG
  with a text chunk and asserts none of their strings is in the result.
- **No appended data survives.** A PNG with an HTML page after `IEND`, or a file that is a valid image and a script at once, comes
  out as a clean PNG: what a decoder ignores after the image is not read into the result.
- **The orientation of a photo is applied, not copied.** The reader of the EXIF orientation is a bounds-checked parser of the first
  Exif segment, limited to the segments before the image data; anything it cannot read is orientation 1. It is fuzzed
  (`FuzzR_API_055_ExifOrientation`) and its result is applied to the 256 x 256 square, never to the source.
- **The result is deterministic**, so a user who uploads the same picture twice gets the same digest, and the second upload
  changes nothing and records nothing.

The PNG is a blob of the blob store ([blob-store](../developer/blob-store.md)): hashed while it is spooled, committed by the
protocol of R-STOR-005, deduplicated by SHA-256. `user_avatars` holds the user, the digest and the size. The blob reference
triggers of the table keep the blob from the collector while a row names it and queue it when the row is replaced, removed or
deleted with its user.

## What a request can read

- `GET /api/v1/avatars/{digest}` is open to every authenticated caller, a session, a personal access token, a service-account
  token and a CI token alike, and to no anonymous one, also where a `public` level opens repositories (Q-AUTH-195).
- It answers only for a digest that some user's avatar currently names. The lookup is the first thing it does
  (`AvatarByDigest`), so the route is not a way to read any other blob of the store by its digest, and a digest of a replaced or
  removed avatar is `404`.
- The bytes are read through the blob store with `OpenOptions.Verify` and compared with the digest and the row's size once more
  ([store.go](../../internal/avatar/store.go) `Open`); an object that was changed in the bucket is never sent. The request
  answers `503` and the blob is marked corrupt by the read, as for any download.
- **The response cannot be anything but a picture.** `Content-Type: image/png`, `X-Content-Type-Options: nosniff`,
  `Content-Security-Policy: default-src 'none'; sandbox`, `Content-Disposition: inline; filename="avatar.png"`,
  `Cross-Origin-Resource-Policy: same-origin`. The page that shows it admits it by `img-src 'self'` (R-UI-001); the URL is on
  trove's origin and the route is served by the roles that serve `/api/v1/`.
- `Cache-Control: private, max-age=31536000, immutable` and the digest as the strong ETag: the URL names the content. A replaced
  avatar has a new URL.

## Who can change what

| Operation | Who | Why |
|---|---|---|
| `PUT /api/v1/users/me/avatar` | the user, or the break-glass account, by a session or by a token that inherits the owner's rights | the caller's own record; a service account or a CI token has no user record and answers `403`, and so does a token whose scope is narrower than its owner's rights: the two operations declare `x-trove-owner-rights` and the shared authorization step refuses such a token before the handler runs (Q-API-213, Q-API-214, [authorize.go](../../internal/api/v1/authorize.go)) |
| `DELETE /api/v1/users/me/avatar` | the same | the same |
| `DELETE /api/v1/users/{username}/avatar` | the holder of `identities:write` (the role `admin`) | moderation: an unwelcome picture can be taken down by someone other than its owner |
| setting another user's avatar | nobody | an administrator cannot put a picture into another person's profile |

A write by a session needs the CSRF header like every unsafe request under `/api/v1/` ([login-and-sessions](login-and-sessions.md));
a write by a token does not (R-API-006). Both are `user.avatar.set` or `user.avatar.removed` events in the same transaction as the
change, with the digest and who did it (`source` `self` or `admin`); the event holds no image bytes ([audit](audit.md)). A refused
upload records nothing.

## What it costs a replica

- One upload holds the body (1 MiB at most), the decoded image and the scaler's buffer (48 MiB at most by the estimate of step 6)
  and the 256 x 256 result; at most two uploads decode at once on a replica. The slowest image the checks accept costs about
  0.5 s of CPU for the whole of `Process`, decode, scale and encode, measured without the race detector
  ([Q-UI-173](../spec/api-and-ui.md#q-ui-173--the-slowest-accepted-avatar-is-measured-against-the-decode-deadline-and-pinned-in-the-performance-run), [Q-UI-174](../spec/api-and-ui.md#q-ui-174--the-scans-of-a-jpeg-are-bounded-by-block-visits-and-the-decoders-input-is-read-in-short-pieces)).
  For a PNG, a baseline JPEG and a WebP the 1 MiB cap decides which image that is; for a progressive JPEG it is the block-visit
  allowance. A decode is cut off after 5 s, so one decode needs 0.1 CPU on average to finish; two at once, which an attacker can
  arrange, need 0.2 CPU of the pod. A JPEG is refused above 64 scans or above the visit allowance; the rate limits of the management API
  (R-API-016) apply per caller as to any write.
- A read is one database lookup and, for the first request of each browser, one verified object-store read. A conditional request
  that matches is answered `304` from the lookup alone, with the cache policy of the image (not `no-store`, which would replace the
  immutable policy of the entry the browser holds, RFC 9111 section 4.3.4), and the rest are cache hits.

## What the browser does with it

The UI ([R-UI-051](../spec/api-and-ui.md#r-ui-051--avatar-upload)) checks a chosen file for its type, its size and its pixel size
before it sends it. That is a courtesy that saves a request for a common mistake; none of the server's checks above depends on
it, and a client that is not the UI skips it. The UI sends the file as the request body with the session's CSRF header and shows
the image only as the `src` of an `<img>` with an empty alternative text, from a same-origin URL that the page's CSP admits
(`img-src 'self' data:`); it puts nothing of the uploaded bytes into the document. The generated robot of a user without an image
is drawn in the browser from the bundled style and shown as a data URI, with no request to another host (Q-UI-167).

## What this does not cover

### H-559 · The picture itself is not inspected

Component: `PUT /api/v1/users/me/avatar`, [Process](../../internal/avatar/process.go). Subject: every user who can upload.
Live today.

The server makes sure that what it stores is a harmless PNG; it does not look at what the picture shows. A user can upload an
offensive or misleading picture (for instance one that imitates another user's), and every authenticated caller who sees that
user's record sees it. There is no automatic check and no approval step; the remedy is the removal by an administrator
(`DELETE /api/v1/users/{username}/avatar`), which is reactive. A user also cannot hide their own avatar from other callers: the
read is open to every authenticated caller (Q-AUTH-195). What would make it matter is an installation with many users who do not
know each other, or a public deployment with open sign-up through the identity provider.

### H-560 · The image is decoded inside the API process

Component: [Process](../../internal/avatar/process.go) in the `api` and `all` roles. Subject: the replica that decodes. Live
today, bounded.

The decoders are the standard library's `image/png` and `image/jpeg` and `golang.org/x/image/webp`, written in a memory-safe
language, and a panic is recovered. They still run in the process that serves the management API, with its credentials and its
memory. The bounds (1 MiB, 4 Mpixel, 64 scans, 48 MiB, two at a time, 5 s a decode) limit what a hostile file can cost, but not what a
defect in a decoder could do that the Go runtime does not prevent. A request holds one of the two slots for at most the 5 s of
the decode (the decoders stop at their next read, so a long stretch of work between two reads can run past it), and a burst of
uploads from many accounts is bounded only by the per-caller rate limits (R-API-016, H-43) and the slot wait of 2 s. Moving the decode to a separate, resource
limited process was considered and not done (Q-API-208, option (e)). What would make it matter is a published defect in one of
the decoders; the dependency is pinned in `go.mod` and covered by `govulncheck`.

### H-562 · A user who alternates avatars makes an administrator's conditional edit fail

Component: the trigger `user_avatars_revision_change` and `user_avatars_revision_replace` on `principal.revision` (Q-API-210).
Subject: `PATCH /api/v1/users/{username}` by an administrator, with `If-Match`. Live today.

Every change of a user's avatar bumps the revision that is the ETag of the user record, so an administrator who loaded the record
and then sends a `PATCH` with that tag gets `412` when the user changed the avatar in between. A user who uploads two different
pictures in turn, as often as the rate limits allow, can keep such edits failing; the administrator's remedy is `If-Match: *`,
which is a deliberate blind write, or a read and a retry. Nothing is lost and nothing is changed by the failed edit. It would matter
for an administrator who edits profile data of users who are hostile to it, or in a script that cannot retry. The avatar is part of
the record's representation, so keeping its change out of the revision would leave the ETag of `GET` stale (Q-API-210, option (b)
needs a second tagging scheme).

### H-563 · The memory of a decode is bounded but not charged to the admission guard

Component: [Process](../../internal/avatar/process.go) and the memory guard of the chain (Q-ARCH-328). Subject: the replica's
memory limit. Live today, bounded.

The guard charges a request the bytes of a download (640 KiB with the defaults) and, while a request is in flight, cannot see
what a decode holds. The processor bounds it itself, to 96 MiB at the worst (two decodes of 48 MiB by the estimate of the header),
beside the 1 MiB body of each upload. On a replica whose limit is 256 MiB that is a large part of the limit that the guard counts
as free, so the heap gate (H-539) is what closes the replica when other work arrives during two large decodes. The estimate is
made from the header and is conservative for the formats it knows (progressive JPEG, CMYK, 16-bit PNG); a decoder that allocates
more than the estimate for a file that follows its header would exceed it. What would make it matter is a small memory limit with
traffic that also runs large decodes; the guard does not take a charge larger than the request bytes for one reader
(`Ticket.Settle`), so the fix is a charge of the estimate at the door, which is not built.

### H-561 · A removed or replaced picture outlives its removal in the bucket and in browsers

Component: the blob store's collector ([blob-store](../developer/blob-store.md)) and the browser cache. Subject: the removed image.
Live today.

When an avatar is replaced, removed or its user deleted, the route stops serving the old digest at once, but the object stays in
the bucket until the collector has checked its references after `storage.gc.delete_settle` (R-STOR-024); someone with read access
to the bucket can read it until then (the bucket is outside trove's authorization, H-11). A browser that loaded the picture keeps
it until its cache entry ends, up to a year (`immutable`, Q-API-209), and shows it for the same URL without asking. Two users who
uploaded the same picture share one object, which stays until both have dropped it. What would make it matter is a picture that
must be gone everywhere at once; nothing in trove can recall a copy a browser holds.

### H-640 · Only the decode has a deadline, and it is looked at only when the decoder reads

Component: [Process](../../internal/avatar/process.go), `decode` and what follows it. Subject: the replica's CPU and the decoding
slot. Live today, bounded by measurement.

The 5 s deadline is the context of the decoder's reader, looked at on each read of at most 16 bytes. Two stretches of work run
without a read. The first is the reconstruction a JPEG decoder does after its last read (the inverse transform of every block):
a deadline that falls after it does not stop the decode; measured on the development machine, with a deadline of 100 ms eight of
the ten JPEG inputs finished with no error in 89 to 194 ms, the slowest 94 ms past the deadline, while a deadline of 20 ms stopped
all of them within 2.6 ms of it. At the visit limit the reconstruction is part of the 0.47 s. The second is the scale and the encode that follow every decode: the request checks its own
context once and then scales the centre square to 256 x 256 (Catmull-Rom, which has fast paths for `image.NRGBA`, `RGBA`, `Gray`
and `YCbCr` and reads a 16-bit PNG through its `RGBA64Image` interface path) and encodes the result; neither reads from the upload,
and both keep the slot until they finish. All of it is bounded by the area limit, the visit allowance and the fixed output, and it
is part of the figures of Q-UI-173; nothing enforces it at run time. The nightly test
(`TestR_API_055_TheSlowestAcceptedImagesDecodeInsideTheDeadline` with `TROVE_PERF_GATE=1`) fails when the slowest input costs more
than one second of CPU in the least of seven decodes (the wall time is logged, not judged). What would make it matter is a change to the scaler, the
decoders or the bounds that the nightly run has not yet judged, or a pod whose CPU limit is far below the 0.2 CPU the two slots
need, where reconstruction, scale and encode alone can run for seconds after a decode that finished inside its 5 s.

### H-641 · The deadline is wall time, so a pod that is starved of CPU refuses large images

Component: `DecodeTimeout` in [Process](../../internal/avatar/process.go), the CPU limit of the `api` pods
(`api.resources.limits.cpu`, empty by default) and the node's CPU. Subject: a user who uploads a large image. Live today for a
pod with a low CPU limit or a saturated node; dormant with the chart's defaults on a node with spare CPU.

The deadline bounds what an upload costs the replica, which is why it is wall time: a hostile upload must not hold a CPU and a
slot for longer. The same bound refuses a legitimate image when the process gets too little CPU. An ordinary photo (a baseline
JPEG or a PNG of 2048 x 2048 pixels) costs 0.15 to 0.3 s of CPU, the slowest image the checks accept 0.47 s; a decode needs
0.1 CPU on average to finish inside 5 s for the slowest, and the pod 0.2 CPU while two decode at once. Below that the user sees
`422` `too_complex` with no hint at the cause, and a retry on a calmer node succeeds. In the measurement of Q-UI-173, hours with a
host load of 33 to 128 on 20 cores made decodes of the same images more than 25 times slower in the worst sample (8.4 s for a PNG that
costs 0.3 s), which is the case. The measured margin is one machine's (linux/arm64, Go 1.27.3), and the lossy WebP is not measured
because the repository has no encoder to build one. What would make it matter is an operator who sets `api.resources.limits.cpu`
below about 250m (two slots of the slowest image need 0.2 CPU for the deadline to hold), or CPU-starved nodes, together with
users who upload large images; the remedy for the first is the limit, not a longer deadline, because the deadline is the bound on
a hostile upload.

The nightly test judges the least CPU time of seven decodes and not the wall time, for the same reason this gap exists: the wall time
of a decode measures how busy the runner is (Q-ARCH-220), so a loaded runner would fail the gate for a regression that is not in the code.
