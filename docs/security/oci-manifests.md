# OCI manifests: the push and the pull of a local repository

This page describes what a push of a manifest or a tag can put into a local OCI repository, in which order a push is checked and what
commits together, and what a pull of a manifest or a blob reads and serves. The code is
[handlers_manifest.go](../../internal/formats/oci/handlers_manifest.go) (`handleManifestPut`, `commitTag`, `checkReferences`,
`shadowCheck`, `handleManifestGet`), [handlers_blob.go](../../internal/formats/oci/handlers_blob.go) (`handleBlobGet`), the commit hook
[hooks.go](../../internal/formats/oci/hooks.go) (`OnCommit`) and the parser [manifest](../../internal/formats/oci/manifest); the
requirements are R-OCI-006, R-OCI-009, R-OCI-010, R-OCI-013, R-OCI-014 and R-OCI-020 of [formats/oci.md](../spec/formats/oci.md). The
registry token that carries the grant is [oci-registry.md](oci-registry.md), the blob uploads and the mount are
[oci-uploads.md](oci-uploads.md), the removal of what a push stored is [oci-content.md](oci-content.md), and what a remote or a virtual
repository adds are [oci-remote.md](oci-remote.md) and [oci-virtual.md](oci-virtual.md).

## What a request is decided on

- Every request names an item path and is decided on it, with the image name as the package and, for a tag, the tag as the version
  (R-OCI-006, Q-OCI-140): a manifest `<name>/_manifests/sha256:<hex>`, a tag `<name>/_tags/<tag>`, a blob `<name>/_blobs/sha256:<hex>`. A
  grant that is scoped by path or by package therefore holds on the registry routes as it does on `/files/`.
- A pull of a manifest or a blob needs `read`. A push needs `deploy` on the tag path and on the manifest path; a tag that points at
  another digest also needs `overwrite`, decided from what the tag points at when the commit begins (`commitTag`). A concurrent push
  that moves the tag between the read and the commit makes the item store refuse the overwrite the decision did not cover, and the tag
  is read and decided again, at most three times.
- A registry token is a ceiling only: each request is decided again by the decision point, which audits the write decisions
  (`authz.write.allowed` with `deploy` or `overwrite`, R-OCI-034).
- A pull by digest names no tag, so a rule that narrows by version does not see it; the format has no exposure adapter, so an endpoint
  with exposure rules does not serve it ([H-642](../spec/formats/oci.md#h-642--a-pull-by-digest-names-no-version-so-the-exposure-rules-that-narrow-by-tag-would-not-apply-to-it),
  linked from [oci-content.md](oci-content.md)).

## The order of the checks of a push

`PUT /v2/<project>/<repository>/<name>/manifests/<ref>` runs these steps, and nothing is stored before the last one:

1. The coarse `deploy` check on the repository runs before the handler, before the body is read (R-ARCH-005).
2. The reference is a digest of the form `sha256:<64 lower-case hex>` or a tag of the tag grammar; `?tag=` and a digest algorithm other
   than `sha256` answer `UNSUPPORTED`. The `Content-Type` is one of the four accepted manifest types.
3. The decision for the name and the tag, and the ownership pre-check of R-AUTHZ-018 with the image name as the package (a new tag or a
   push by digest is a publish, a tag that exists is a change), both before the body is read.
4. The body is spooled within 4 MiB (`MaxManifestBytes`, 413 `SIZE_INVALID` above it) and parsed by the manifest parser within the bounds of
   R-OCI-020: nesting of 32 levels, 10,000 descriptors, 100,000 JSON values, after `untrusted.CheckJSON`. The parser is strict where a
   second reader could read the document differently (a key twice, a key that differs only in case, a document that is not UTF-8, data
   after the value, a size that is no plain integer). A crafted document is refused here, before any referenced item is looked up.
5. The digest is the SHA-256 of the received bytes; a push by digest whose digest differs answers `DIGEST_INVALID`. A digest the journal
   replay withheld under the name answers 409 until an administrator reinstates it ([oci-content.md](oci-content.md#after-a-restore)).
6. Every config, layer and index entry must be stored under the same name (`MANIFEST_BLOB_UNKNOWN` otherwise): bytes that another name or
   repository holds do not count. A foreign layer (one with `urls` or a non-distributable media type) and the `subject` are not required.
   An index entry the replay withheld is refused the same way.
7. The shadowing check of R-REPO-048 runs while the name has no live version.
8. The commit (below).

## What commits together

- A push by tag commits the tag item as the lead and the manifest item as its companion, or links the tag to the manifest when that is
  stored already; a push by digest commits the manifest item alone. Both run in one transaction with the format's rows and the package
  version (R-STOR-005), so a tag item and a version row that disagree are never visible.
- The commit hook takes the package row lock first, then reads the versions, makes the ownership claim and checks the references again
  on rows it holds `FOR SHARE` until the transaction ends (`ExistingItems`, Q-OCI-61): a blob or a child manifest cannot be removed
  between the check and the manifest's rows. A tag linked to a stored manifest checks, on a held row, that the manifest is still there.
- A push that the shadowing check skipped because the name had a live version, and that finds at commit time that it is the first
  version after all, is answered 503 with `Retry-After`, so the check runs on the next attempt.
- A manifest or a tag that reaches the repository by any road other than the protocol (a raw deploy, the items API) is refused by the
  hook in the transaction (`OnCommit`); a blob item whose path digest is not the SHA-256 of its content is refused the same way.
- Pushes and removals take the package row and the item rows in different orders; the database ends the cycle and the runner retries
  ([H-608](../spec/formats/oci.md#h-608--pushes-and-removals-take-their-locks-in-different-orders-and-rely-on-the-retry), linked from
  [oci-content.md](oci-content.md)).

## What a pull serves

- A manifest is served as it is stored, whatever `Accept` says: no conversion and no rewriting, so its digest stays the one the client
  verifies (Q-OCI-8). A tag is resolved at request time; a manifest stays readable by digest while it is stored, tagged or not.
- A blob is readable through a name only when an item of that name holds it (R-OCI-010), so a caller who may read one name cannot read the
  layers of another name of the same repository by digest, even when the blob store holds the same bytes once.
- Ranges, the validators and download integrity are the item store's (R-STOR-012, R-STOR-013): a blob whose stored bytes do not match
  their SHA-256 ends the response.

## Tests

- [handlers_manifest_test.go](../../internal/formats/oci/handlers_manifest_test.go): the decision per name and tag before the body is
  read (`TestAuthorizationIsDecidedPerNameAndTagBeforeTheBodyIsRead`), the ownership pre-check before the body, `overwrite` for a moved
  tag, the refusals of R-OCI-013 and the crafted inputs of R-OCI-020, a withheld digest, a push that fails in the transaction and leaves
  nothing, concurrent pushes of one tag, and a manifest or tag that reaches the repository another way.
- [manifest](../../internal/formats/oci/manifest): the parser's refusals and bounds (`TestParseRefusals`, `TestR_OCI_020_CraftedBounds`).
- [test/integration/ocilocal](../../test/integration/ocilocal): the push against the sweep on a real database
  (`TestAPushThatCommitsBetweenTheScanAndTheRemovalKeepsItsBlob`).

## What this does not cover

- [H-600](../spec/formats/oci.md#h-600--the-sizes-a-manifest-declares-are-not-compared-with-the-stored-blobs): the sizes a manifest
  declares are the pusher's claims, not compared with the stored blobs, and the package screen sums them.
- [H-601](../spec/formats/oci.md#h-601--blob-downloads-are-streamed-and-never-redirected-to-the-object-store): every blob download goes
  through a replica; the presigned redirect is not built.
- [H-602](../spec/formats/oci.md#h-602--the-first-push-of-a-name-by-digest-can-claim-the-name-for-a-caller-that-never-pushes-a-tag): a
  push by digest claims an unclaimed name for a holder of `deploy` that never pushes a tag.
- [H-604](../spec/formats/oci.md#h-604--the-shadowing-check-is-asked-once-per-push-until-the-name-has-a-tag): each child push of a fresh
  multi-platform image asks the shadowing check again until the first tag exists.
- [H-609](../spec/formats/oci.md#h-609--the-cost-of-a-push-is-bounded-by-the-size-of-the-manifest-not-by-admission-control): the memory
  of a push is bounded by the manifest limits, and whether admission control bounds the pushes in flight is not verified.
- [H-672](../spec/formats/oci.md#h-672--the-size-of-an-index-leaves-out-a-foreign-child-that-is-pushed-after-it): the stored size of an
  index leaves out a foreign child pushed after it.

This page does not cover the uploads that put the blobs a manifest names ([oci-uploads.md](oci-uploads.md)), the removal and the sweep
([oci-content.md](oci-content.md)), or the fill of a remote and the resolution of a virtual repository, which serve manifests from other
sources ([oci-remote.md](oci-remote.md), [oci-virtual.md](oci-virtual.md)).
