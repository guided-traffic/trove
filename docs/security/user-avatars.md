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
6. **A decoding slot.** A replica decodes two images at a time; a request waits up to 2 s for a slot and then answers `503
   server_busy` with `Retry-After: 1`. The admission memory guard of the chain charges a request what a download holds
   (Q-ARCH-328), not a decode, so the processor bounds its own memory: two slots times 4,194,304 pixels times at most 8 bytes per
   pixel is 64 MiB.
7. **The decode.** `image/png` and `image/jpeg` of the standard library, and the decoder of `golang.org/x/image/webp` (the encoder
   is not imported). A decoder that panics on a crafted file is recovered and counts as an image that does not decode. A truncated
   or damaged image is `422` `undecodable_image`.

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
  token and a CI token alike, and to no anonymous one, also with anonymous access on (Q-AUTH-195).
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
| `PUT /api/v1/users/me/avatar` | the user, or the break-glass account | the caller's own record; a service account or a CI token has no user record and answers `403` |
| `DELETE /api/v1/users/me/avatar` | the same | the same |
| `DELETE /api/v1/users/{username}/avatar` | the holder of `identities:write` (the role `admin`) | moderation: an unwelcome picture can be taken down by someone other than its owner |
| setting another user's avatar | nobody | an administrator cannot put a picture into another person's profile |

A write by a session needs the CSRF header like every unsafe request under `/api/v1/` ([login-and-sessions](login-and-sessions.md));
a write by a token does not (R-API-006). Both are `user.avatar.set` or `user.avatar.removed` events in the same transaction as the
change, with the digest and who did it (`source` `self` or `admin`); the event holds no image bytes ([audit](audit.md)). A refused
upload records nothing.

## What it costs a replica

- One upload holds the body (1 MiB at most), the decoded image (4 Mpixel at most, 32 MiB at the widest pixel) and the 256 x 256
  result; at most two uploads decode at once on a replica. The CPU of a decode of the largest accepted image is a few hundred
  milliseconds; the rate limits of the management API (R-API-016) apply per caller as to any write.
- A read is one database lookup and one verified object-store read, for the first request of each browser: the rest are cache hits.

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
memory. The bounds (1 MiB, 4 Mpixel, two at a time) limit what a hostile file can cost, but not what a defect in a decoder could do
that the Go runtime does not prevent, such as a long loop on a crafted stream: a decode in progress cannot be interrupted, so a
request that holds one of the two slots can hold it for the length of that decode, and a burst of uploads from many accounts is
bounded only by the per-caller rate limits (R-API-016, H-43) and the slot wait of 2 s. Moving the decode to a separate, resource
limited process was considered and not done (Q-API-208, option (e)). What would make it matter is a published defect in one of
the decoders; the dependency is pinned in `go.mod` and covered by `govulncheck`.

### H-561 · A removed or replaced picture outlives its removal in the bucket and in browsers

Component: the blob store's collector ([blob-store](../developer/blob-store.md)) and the browser cache. Subject: the removed image.
Live today.

When an avatar is replaced, removed or its user deleted, the route stops serving the old digest at once, but the object stays in
the bucket until the collector has checked its references after `storage.gc.delete_settle` (R-STOR-024); someone with read access
to the bucket can read it until then (the bucket is outside trove's authorization, H-11). A browser that loaded the picture keeps
it until its cache entry ends, up to a year (`immutable`, Q-API-209), and shows it for the same URL without asking. Two users who
uploaded the same picture share one object, which stays until both have dropped it. What would make it matter is a picture that
must be gone everywhere at once; nothing in trove can recall a copy a browser holds.
