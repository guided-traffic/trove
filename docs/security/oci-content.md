# OCI content: deletion, the sweep and a restore

This page describes what a caller can remove from a local OCI repository, what the periodic sweep of unreferenced blobs may remove,
and what a restore does about an image that was deleted after its snapshot. The mechanisms are in
[internal/formats/oci](../../internal/formats/oci) (`handlers_delete.go`, `hooks_remove.go`) and
[internal/formatdata/oci](../../internal/formatdata/oci) (`sweep.go`, `journal_replay.go`, `reinstate.go`); the requirements are R-OCI-018,
R-OCI-019 and R-OCI-033 of [formats/oci.md](../spec/formats/oci.md). The token service and the registry tokens are another
perspective.

## Who may remove what

- A `DELETE` of a tag, a manifest or a blob is decided for the image name as the package, for a tag the tag as the version, and for the item
  by its **path** (`delete`, R-OCI-006, R-AUTHZ-005), so a grant that excludes `**/_tags/keep` holds on the registry route as it does
  on `/files/`; it passes the ownership check of the name in the transaction that removes. A delete by digest also decides `delete` for the
  tags that point at the manifest, its referrers and the children it lists, and everything the cascade goes on to remove (a referrer's
  tags, at every depth): the host decides every path a hook cascades to with the listing form of the decision (no audit event) and refuses
  the whole removal (403, rolled back) at the first one the caller may not delete; `/files/` is guarded the same way (Q-OCI-76), and so is
  every batch of the job that finishes a large `/files/` delete, on what the caller holds when the batch begins
  ([file-transfer.md](file-transfer.md#a-delete-and-its-job), Q-STOR-190). The tags list and the referrers answer leave out the entries whose path the caller may not read, decide them in a batch that writes no
  audit event, and never name a hidden entry in a Link. Authorization runs before the
  existence check, so a caller who may not delete learns nothing of what the name holds.
- The routes serve the local and the virtual class. A remote repository is a cache and answers 405 before a handler runs; a virtual repository
  answers 405 from the handler, because the dispatcher would carry a write to its deployment target (ADR-180).
- The rules of a removal are the storage hook's, so every road takes them: the protocol, `/files/`, the items API, a retention run and
  the sweep. A manifest an index of the name lists, and a blob a manifest references, are refused (409 `DENIED`) and the whole removal
  rolls back. A removal that takes the referencing manifest in the same event is allowed.
- **The cascade of a deleted manifest is the hook's, not the caller's decision per item.** A caller who may delete the image name
  removes its tags, its referrers that no index lists and the children no other manifest and no tag names. The cascade runs as the
  actor of the removal, or as the system actor `removal` when no principal asked for it, and every item it removes writes the
  ordinary deletion events as well as `oci.manifest.deleted`.
- The deletion of a whole image name through `/files/` or the items API, even above the 5,000 files of one event or the batches of the
  job, is judged as one removal: the item store tells the hook (`ItemsEvent.InScope`) which referencing manifests belong to it, so a blob
  is not refused for a manifest the same delete takes in another event.
- Before it deletes the rows of the files of a removal the item store lets the format take its locks (`LockRemoval`): the package rows of
  the image names, in name order. Pushes lock the item first (H-608); the transaction runner retries the loser of a cycle.
- Every removed manifest writes `oci.manifest.deleted` in the transaction of the removal, with the journal entry
  `{"repository", "name", "digest", "actorId"}`: IDs and a digest, no secret.

## The sweep of unreferenced blobs

- The job `oci-sweep` runs hourly on a `worker` replica, exclusive, in the queue `maintenance`. It reads the blob items of local OCI
  repositories only; a remote's blobs are cache entries and are never candidates.
- A candidate is older than `oci.unreferenced_blob_grace` (which must exceed the age after which an upload session expires), is
  referenced by no manifest of its own image name, and is not the digest of a session of that name that is open, completing or
  assembled. The same bytes under another image name do not protect it, and its removal does not touch the other name's item.
- It removes one item per transaction. When the image name has a package row the transaction locks it first, as a manifest push does; it
  reads the references and the sessions again and only then removes through the item store as the system actor `removal`. A name that
  holds only blobs has no package row and none is made for it: the rows of the blobs arbitrate, because a push holds them `FOR SHARE`
  until its own rows are written and the hook of the removal reads what the push committed. A manifest that
  commits a reference while the sweep waits for the lock keeps its blob, and the manifest push reads its layers again inside its commit
  transaction with the rows held (`ExistingItems`, `FOR SHARE`), so a layer cannot go between the check and the manifest's rows; the integration test `TestAPushThatCommitsBetweenTheScanAndTheRemovalKeepsItsBlob`
  holds this against a real database.
- The sweep never deletes an object of the blob store: it removes items, and the garbage collection of R-STOR-024 collects the object
  once no item references it.

## After a restore

- A restore returns the repository to its snapshot, so an image deleted after it is stored again, and it may have been deleted
  because it was malicious. The replay of `oci.manifest.deleted` therefore **withholds** it: it writes the mark in `oci_tombstones`
  and raises the package's revision in the one transaction the journal gives a handler, and it deletes nothing, neither rows nor
  items nor bytes (R-HA-054).
- The entry is a claim, not a fact. The replay acts only on a local OCI repository, only on a manifest whose row the restored
  repository holds and whose item is stored with exactly the digest the entry names, and bounds every string of the entry before it
  reaches a statement. An entry about anything else is "subject unknown" and changes nothing, and an entry that is not an entry is
  skipped.
- The format's serve check refuses a withheld manifest item and a tag item that points at it with the 404 of a missing manifest, on
  `/v2/` and on `/files/`, from facts read with the item (no query of its own); the tags list and the referrers answer omit it; a
  push of the same digest is refused until the mark is gone.
- `trove admin oci reinstate <projectKey>/<repoKey> <name>@<digest>` is the way back. It removes the mark only while the manifest
  row and its item are stored with that digest, so it brings back the same bytes and never admits new ones; it writes `oci.reinstate`
  (not journaled, because it restores access rather than removing it). An administrator who confirms the withdrawal deletes the
  manifest through the management item API: the mark stays, so the digest stays refused.

## What this does not cover

- [H-580](../spec/formats/oci.md#h-580--untagged-manifests-accumulate-until-a-retention-policy-removes-them): untagged manifests accumulate until a retention policy removes them.
- [H-608](../spec/formats/oci.md#h-608--pushes-and-removals-take-their-locks-in-different-orders-and-rely-on-the-retry): pushes and removals take their locks in different orders and rely on the retry.
- [H-645](../spec/formats/oci.md#h-645--a-list-with-more-than-10000-consecutive-tags-the-caller-may-not-read-is-refused): a list with more than 10,000 consecutive tags the caller may not read is refused.
- [H-653](../spec/formats/oci.md#h-653--the-cascade-of-a-retention-run-is-not-held-to-a-subjects-grants): the cascade of a retention run is not held to a subject's grants.
- [H-642](../spec/formats/oci.md#h-642--a-pull-by-digest-names-no-version-so-the-exposure-rules-that-narrow-by-tag-would-not-apply-to-it): a pull by digest names no version, so the exposure rules that narrow by tag would not apply to it.

### H-606 · A forged journal entry withholds a manifest until an administrator acts

The journal bucket is outside trove's own trust: whoever can write an object into it can name a manifest of a local OCI repository
that the restored database holds, and the replay then withholds it. This costs availability of that image (404 on every route) until
`trove admin oci reinstate` or the confirming delete; it costs no data, no integrity and no access, because the replay deletes
nothing and a reinstate brings back the same bytes. **Dormant until a restore runs with a journal that somebody else wrote to.** What
makes it real is a credential for the journal bucket that is not trove's own; the replay output lists every withheld manifest so
the effect is visible.

### H-607 · The layers of a withheld image stay downloadable by digest

The mark is on the manifest. A blob is let through by the serve check because a blob is shared by every image of the name, so the layers
of a withheld image can still be pulled by anyone who knows their digests and may read the name, until the sweep removes them after the
confirming delete. **Live after every replay that withholds.** What makes it real: a manifest withheld because it carried a malicious
layer, a reader who recorded the layer's digest before the restore. The layer's digest is not listed by any route once the manifest
is withheld.

### H-643 · A blob reused after the grace can be swept before its manifest arrives

The grace of `oci.unreferenced_blob_grace` is measured from the creation of the blob item. A blob that was created long ago, stayed
unreferenced and is then reused (a client finds it with `HEAD` and skips the upload, or uploads the same bytes again: an identical
commit changes no row) can be swept before the manifest that names it arrives. The push then answers `MANIFEST_BLOB_UNKNOWN` and the
client uploads the layer again; nothing dangles and nothing is disclosed. **Live today**; real for CI that pushes a layer, fails before
the manifest, and pushes it again after more than the grace.

### H-644 · A manifest that was deleted and pushed again after the snapshot is withheld by the replay

The journal records the deletion and not the later push of the same bytes (a push writes no journal entry). After a restore the replay
therefore withholds a manifest that was deleted and then pushed again after the snapshot, although it is live in the installation that was
lost. This is the conservative direction: availability of that image is lost until `trove admin oci reinstate`, never integrity or
access. **Live after every restore that follows a delete and a re-push**; the replay output lists every withheld manifest.

### H-756 · The memory bound of the crafted manifests is measured on the heap of the test process

The criterion of R-OCI-020 says the replica's RSS stays within 512 MiB of its baseline while crafted manifests (nested 100,000 deep, 20,000 layers, a 5 MiB index, a path as a digest) are
refused. The integration test runs the replicas inside the test process, so it samples the Go heap in use every 10 ms while a manifest is pushed and asserts that the peak stays within 512 MiB of the
heap before it; it does not see memory the runtime holds without using (stacks, released spans not yet returned) and it cannot see the RSS of a separate process. The refusal itself is structural
(the parser's bounds run before any blob is looked up, so the work is linear in a document of at most 4 MiB), and the e2e run is the place for an RSS reading of a pod. Live today as a measurement gap,
not as an observed excess.

