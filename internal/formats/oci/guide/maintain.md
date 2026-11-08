Deleting needs the permission to delete on the repository, and the ownership of the image name. The registry API offers three deletions; the Docker CLI has no command for them, so use a tool that speaks the API (`skopeo delete`, `crane delete`, `curl -X DELETE`) or the package screen of trove.

| Request | Effect |
|---|---|
| `DELETE /v2/<project>/<repository>/<name>/manifests/<tag>` | Removes the tag. The manifest stays and can still be pulled by its digest. |
| `DELETE …/manifests/<digest>` | Removes the manifest, every tag of the image name that points at it, its referrers and, for an index, the platform manifests that no other manifest and no tag of the name names. |
| `DELETE …/blobs/<digest>` | Removes a layer or config that no manifest of the image name references. |

All three answer `202`. A manifest that an index of the image name lists is refused with `409` `DENIED`: delete the index first. A blob that a manifest still references is refused with `409` `DENIED` too.

### What stays

Moving a tag leaves its old manifest in the repository, untagged and pullable by digest, until somebody deletes it or a retention policy removes it. Deleting a manifest does not delete its layers at once: a layer that no manifest of the image name references any more is removed by a periodic job after the grace period (24 hours by default), and the stored object is freed by the next garbage collection.

### Remote and virtual repositories

A remote repository is a cache: its images are removed through the package screen or the management API, and a `DELETE` on the registry API answers `405` `UNSUPPORTED`. A virtual repository routes no deletes.

### After a restore

When trove is restored from a backup, an image that was deleted after the backup is withheld instead of served again, because it may have been removed on purpose: it answers `404` until an administrator reinstates it or confirms the deletion.
