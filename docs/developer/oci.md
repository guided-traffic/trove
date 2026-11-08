# OCI and Docker registry

The OCI format (`internal/formats/oci`) serves container images and other OCI content. This page describes the part that is built, the
skeleton of phase T-16 ("WP0, contracts"): the contract additions, the schema, the configuration keys and the format that registers and
answers `501` until the work packages fill it, and the operations built on it: the manifest parser, the pulls of manifests and blobs and the push of
manifests (WP4a, [ADR-164](../decisions.md#adr-164--the-oci-push-path-is-one-transaction-with-its-reference-check-under-row-locks-and-the-formats-data-port-reads-items)),
the tags list, referrers, deletion, sweep and replay (WP4b, ADR-168). The specification is [formats/oci.md](../spec/formats/oci.md); the decision for the contract is
[ADR-161](../decisions.md#adr-161--the-format-contract-gains-the-registry-root-the-upstream-handshake-head-revalidation-and-upload-sessions).
The registry tokens and the token service ([Registry tokens and the token service](#registry-tokens-and-the-token-service)) and the port of the
[Upload sessions](#upload-sessions-r-stor-011) and the [blob uploads and the mount](#blob-uploads-and-the-mount-r-oci-011-r-oci-012) are built; and [the remote class](#the-remote-class-r-oci-021-to-r-oci-025) are built, and so is [the virtual class](#the-virtual-class-r-oci-027-r-oci-028); the mirror and the clients are not; the section
[What is built and what is not](#what-is-built-and-what-is-not) lists them.

## What has to be in your head first

- **One format, `oci`, for Docker and OCI content** (Q-OCI-1). The address is the root of a host: `/v2/<projectKey>/<repoKey>/<name>/…`.
  The format declares `catalog.Facts.RegistryRoot`, so its `ProtocolBasePath` is `catalog.RegistryRootBasePath`
  (`/v2/{project}/{key}/`), and at most one format may declare it. The three repository-less routes below `/v2/` (the version check, the
  catalog, the token endpoint) belong to the core, not to the format (Q-OCI-4).
- **The dispatcher serves a registry-root format at `/v2/`, not at `/api/oci/`.** `dispatch.New` skips it for the `/api/<format>/`
  prefixes, so `/api/oci/…` answers nothing; `Dispatcher.RegistryHandler` serves it below the one pattern `/v2/{rest...}` that
  `registry.Register` mounts through the enforcement point (Q-OCI-100). Every read of a `/v2/` path goes through the dispatcher's
  `registryMatch`.
- **Keys are OCI name components.** Every repository key and project key, for every format, follows the component grammar of the
  distribution specification (Q-OCI-3): [qname.go](../../internal/qname/qname.go) `ValidKey` and `ValidProjectKey`, the database
  checks of migration 00056, the OpenAPI patterns and the UI patterns. `TestR_REPO_002_TheKeyGrammarsEqualTheirRegularExpressions` compares the
  hand-written functions with the expressions over every string of a small alphabet.
- **Nothing is stored twice.** A manifest and a blob are items, a tag is an item whose bytes are the manifest's (R-OCI-007); the two tables
  hold the graph and carry no blob column.

## Files

| File | Responsibility |
|---|---|
| [format.go](../../internal/formats/oci/format.go) | `Format`, `Descriptor` (the facts: registry root, upstream, extra and credential hosts, periods, limits, data port), `New`, `Match` |
| [names.go](../../internal/formats/oci/names.go) | The grammars of image names, tags and digests (`ValidName`, `ValidFullName`, `ValidTag`, `ValidDigest`) and the package-name rule `names` (a name is never folded; a pattern is left alone) |
| [paths.go](../../internal/formats/oci/paths.go) | The storage layout (`ManifestPath`, `TagPath`, `TaglistPath`, `BlobPath`), `Classify`, `PackageDocumentPath` and `VersionPath` (a tag is a version, decided on its tag path as well, Q-AUTHZ-258) |
| [routes.go](../../internal/formats/oci/routes.go) | The route table of R-OCI-009 to R-OCI-018 with its matchers (the image name has slashes, so the operation is read from the end of the path); every operation has a handler |
| [errors.go](../../internal/formats/oci/errors.go) | `WriteError`: the distribution specification's error list `{"errors":[{"code":…}]}` for every row of R-OCI-008; a conflict is `409 DENIED` |
| [handlers_manifest.go](../../internal/formats/oci/handlers_manifest.go) | The pull of a manifest and the push by tag or by digest: the order of the checks, the tag decision (`commitTag`), the reference check (`checkReferences`), the validator translation |
| [handlers_blob.go](../../internal/formats/oci/handlers_blob.go) | The pull of a blob of a name (local class) |
| [remote.go](../../internal/formats/oci/remote.go) | The remote adapter: `UpstreamRequest` (namespace prefix, `Accept`), `UpstreamQuery`, `AcceptResponse`, `PrepareFill`, `OnFill`, `InspectFill`, `BeforeArtifact`, `Rewrite`; see [the remote class](#the-remote-class-r-oci-021-to-r-oci-025) |
| [remote_auth.go](../../internal/formats/oci/remote_auth.go), [remote_revalidate.go](../../internal/formats/oci/remote_revalidate.go) | The `Authenticator` (challenge parser, scope from the request, token document) and the `Revalidator` (the `HEAD` and its digest) |
| [remote_handlers.go](../../internal/formats/oci/remote_handlers.go) | What a client sees of a remote repository: manifests, blobs, the tags list window, referrers; the `library/` mapping |
| [mirror.go](../../internal/formats/oci/mirror.go), [mirror_listing.go](../../internal/formats/oci/mirror_listing.go), [mirror_tags.go](../../internal/formats/oci/mirror_tags.go), [mirror_plan.go](../../internal/formats/oci/mirror_plan.go) | The mirror lister (`discover.Lister`) on the remote adapter: the methods and the exact names, the catalog, the tags list and the tag selection, the plan of an image; see [the mirror](#the-mirror-r-oci-026) |
| [hooks.go](../../internal/formats/oci/hooks.go) | `OnCommit`: the package row, the claim, the version of a tag and the manifest rows, in the commit transaction |
| [view.go](../../internal/formats/oci/view.go) | The package view adapter: tags newest push first, the platform table of an index, the withheld tags |
| [handlers_upload.go](../../internal/formats/oci/handlers_upload.go) | The blob upload protocol over the host's session port: start, monolithic `POST`, `PATCH`, `PUT`, status, abort; `chunkStart` (the strict `Content-Range`), `outOfOrder` (the headers of a 416) |
| [virtual.go](../../internal/formats/oci/virtual.go) | The virtual adapter: `Strategy` (the tags list is the one merged document), `Merge` (the union of the members' tags lists, each tag attributed to the member the tier resolution picks) |
| [virtual_read.go](../../internal/formats/oci/virtual_read.go) | What a client reads through a virtual repository: the plan (`members`), the `walk` over the members, a manifest by tag, a manifest and a blob by digest in three tiers, the tags list and the referrers as unions |
| [virtual_write.go](../../internal/formats/oci/virtual_write.go) | `writeRequest` (the request a write is decided and committed on), the link step of a manifest push (`linkFromMembers`), the refusal of a delete |
| [handlers_mount.go](../../internal/formats/oci/handlers_mount.go) | The cross-repository mount (`handleMount`) and `MountSource`, the one parser of `from` |
| [manifest/](../../internal/formats/oci/manifest/manifest.go) | The manifest parser: the four media types, the strict JSON reader (`tree.go`), descriptors, references, the row annotations |
| [tagorder/](../../internal/formats/oci/tagorder/tagorder.go) | The order of tags (Q-OCI-17, Q-OCI-64) |
| [inspect.go](../../internal/formats/oci/inspect.go) | `Inspect`: a manifest at the path of a manifest or a tag item is read by the manifest parser within the limits (4 MiB, 32 levels, 100,000 values, 10,000 descriptors) |
| [settings.go](../../internal/formats/oci/settings.go), [settings.schema.json](../../internal/formats/oci/settings.schema.json) | The `oci` member of the repository document: `oci.mirror.tags`, `excludeTags`, `platforms` (remote repositories only) |
| [store.go](../../internal/formats/oci/store.go) | `Store`, the data port: manifest rows, references, referrers, tags, catalog and sweep candidates |
| [rows](../../internal/formats/oci/rows/rows.go) | The values that cross the port; a leaf (`internal/formats/<id>/rows` imports only `errs` and the standard library) |
| [../formatdata/oci](../../internal/formatdata/oci/store.go) | The implementation of `Store` and the registration of the tables (`tables.go`); linked by the composition root ([wire_formats.go](../../internal/app/wire_formats.go)) and by `cmd/trove/registry.go` |
| [oci.sql](../../internal/meta/postgres/queries/oci.sql), [oci_manifests.sql](../../internal/meta/postgres/queries/oci_manifests.sql) | The statements of the port as sqlc; `oci_manifests.sql` is the push path and the view reads ([store_manifests.go](../../internal/formatdata/oci/store_manifests.go)) |

## Manifests, pulls and the push path

The local class serves `GET`/`HEAD` of manifests and blobs and `PUT` of manifests; the list and delete side, the uploads and the remote and
virtual classes are the sections below.

**Pull** ([handlers_manifest.go](../../internal/formats/oci/handlers_manifest.go) `handleManifestGet`, [handlers_blob.go](../../internal/formats/oci/handlers_blob.go)).
The tag or digest is validated, the read is decided for the name (and the tag), the item is looked up and served by the core
(`Items().Serve`: serve check, conditionals, ranges, verifying reader, statistics). The response is the stored bytes whatever `Accept` says.
`validatorWriter` sets `Docker-Content-Digest` and the `ETag` `"sha256:<hex>"` on a status line that carries the item, and
`storedValidators` turns the request's `If-None-Match`, `If-Match` and `If-Range` into the hex form the item store compares (Q-OCI-62).
A blob is `application/octet-stream` and is found only under the name that holds its item.

**Push** (`handleManifestPut`). The order is fixed because each step is cheaper than the next and refuses more:

1. names, tag or digest, the query (`?tag=` and a `digest-algorithm` other than sha256 are `UNSUPPORTED`), the `Content-Type` (`manifest.MediaTypeOf`);
2. `Authorize` deploy for the name and tag and the ownership pre-check, before the body is read;
3. `Spool` (4 MiB), `manifest.Parse`, the digest against the URL, the withheld mark (`409 DENIED`), `checkReferences` (all outside a transaction);
4. the shadowing check while the name has no live version;
5. the commit: by digest the manifest item is the lead; by tag `commitTag` reads the tag, authorizes `deploy` (and `overwrite` when the tag
   points at another digest), then either links the tag to a stored manifest or commits the tag item (lead) with the manifest item (companion).
   A concurrent push of the same tag makes the item store refuse the overwrite the decision did not cover; the tag is read and decided again (at most three rounds).

**The hook** ([hooks.go](../../internal/formats/oci/hooks.go)). `OnCommit` runs in the commit transaction. The tag item's hook locks the
package row (`EnsureLocked`), claims the name, creates the version of a new tag (a version whose tag item is gone is replaced; a moved tag keeps
its version) and raises the revision. The manifest item's hook, after it, checks the references again with `ExistingItems` in the transaction
(which holds the rows `FOR SHARE`), writes the manifest row and its references (`PutManifest`) and raises the revision. Where the manifest is the lead
(a push by digest) it also claims the name. A blob item passes; a manifest or tag item without a push payload is refused.

**The parser** ([manifest/](../../internal/formats/oci/manifest/manifest.go)). `Parse(data, contentType, limits)` bounds the document, scans it
(`untrusted.CheckJSON`), reads it with its own tree reader (`tree.go`: duplicate keys, case-folded lookalikes, non-UTF-8 and trailing data are refused),
checks the shape of the type, every descriptor (digest, size as a plain integer, media type, `urls`, `data`, `platform`) and computes the digest over the exact
bytes. `References()` lists each target once with its kind, first platform and whether the repository must hold it; `RowAnnotations` cuts the annotations to
the bound of the column (sums are saturated; the text is counted as jsonb prints it, to the byte, so the database's `CHECK` holds at exactly 65,536 bytes: the integration test stores the boundary).
Fuzz targets: `FuzzR_OCI_020_Parse`, `FuzzR_ARCH_028_Inspect`, `FuzzR_OCI_031_Compare`.

**The view** ([view.go](../../internal/formats/oci/view.go)): `Stored` lists the tags of a local repository newest push first, `Withheld` the tags of
withheld manifests, `Describe` the digest, media type, size and (for an index) the platform table, `Compare` is `tagorder.Compare`.
The format also implements the optional `formats.ViewSizer` (`Format.ViewSizes`, ADR-177): the package view asks it, once for a page of the version
list and once for the details, for the `sizeBytes` of a tag, the stored size of the manifest, read by `Store.TagSizes` (the config and layers of an image, the sum over the required children of an
index, the same number as `metadata.size`). The core would otherwise show the sum of the version's linked files, which for a tag is the manifest
document (Q-OCI-130). The blobs are not linked to a version, on purpose. The size is stored: `PutManifest` sets an index's `content_size` to the sum
of its children's (`OCISetIndexContentSize`, after the edges are written), so an index of indexes is right at any depth; `ViewSizes` reads it with
`Store.TagSizes`, one statement for a page, and the platform table of the details is the only reader of the children.

| File | Responsibility | Notes |
|---|---|---|
| [handlers_tags.go](../../internal/formats/oci/handlers_tags.go), [handlers_referrers.go](../../internal/formats/oci/handlers_referrers.go) | The tags list (`n`, `last`, the `Link`, 404 for a name that holds nothing) and the referrers API (the index answer, the `artifactType` filter, paging by the last digest, 4 MiB per page) | The tags list is decided on `<name>/_taglist`, referrers on the subject's manifest path, each tag and referrer on its own (Q-OCI-140). The tags list is in byte order (R-OCI-015) and does not use `tagorder`, whose rank order is the package view's and the mirror's (Q-OCI-64).
| [handlers_delete.go](../../internal/formats/oci/handlers_delete.go), [hooks_remove.go](../../internal/formats/oci/hooks_remove.go) | `DELETE` of a tag, a manifest and a blob (authorization, existence, ownership, then the item store), and `OnItemsRemoved`: the refusals, the cascade and the deletion event of every road of removal |
| [setup.go](../../internal/formats/oci/setup.go), [guide.go](../../internal/formats/oci/guide.go), [guide/](../../internal/formats/oci/guide) | The clients `docker` and `podman`, their setup snippets and the guide (R-OCI-030) |
| [errors.go](../../internal/formats/oci/errors.go) | `WriteError`: the distribution specification's error list `{"errors":[{"code":…}]}` for every row of R-OCI-008 |
| [inspect.go](../../internal/formats/oci/inspect.go) | `Inspect`: a manifest is scanned within the limits (4 MiB, 32 levels, 100,000 values) before anything decodes it; the manifest parser of R-OCI-013 replaces the generic decode |
| [settings.go](../../internal/formats/oci/settings.go), [settings.schema.json](../../internal/formats/oci/settings.schema.json) | The `oci` member of the repository document: `oci.mirror.tags`, `excludeTags`, `platforms` (remote repositories only) |
| [store.go](../../internal/formats/oci/store.go) | `Store`, the data port: manifest rows, references, referrers, tags, catalog and sweep candidates |
| [rows](../../internal/formats/oci/rows/rows.go) | The values that cross the port; a leaf (`internal/formats/<id>/rows` imports only `errs` and the standard library) |
| [../formatdata/oci](../../internal/formatdata/oci/store.go) | The implementation of `Store` and the registration of the tables (`tables.go`); linked by the composition root ([wire_formats.go](../../internal/app/wire_formats.go)) and by `cmd/trove/registry.go`. `store_lists.go`: tags, referrers, holds, catalog names, the reads of the removal hook and the deletion event; `store_sweep.go`: the sweep's statements and the marks; `sweep.go`: the job `oci-sweep`; `journal_replay.go`: the replay handler; `reinstate.go`: the administrator's command.  `store_removal.go`: `RemoveManifest` and `ReferencedBy` |
| [oci.sql](../../internal/meta/postgres/queries/oci.sql), [oci_lists.sql](../../internal/meta/postgres/queries/oci_lists.sql) | The statements of the port (sqlc): the manifest reads, and the lists, the cascade, the sweep and the replay |
| [admin_oci.go](../../cmd/trove/admin_oci.go) | `trove admin oci reinstate <projectKey>/<repoKey> <name>@<digest>` |

## The contract additions

[ADR-161](../decisions.md#adr-161--the-format-contract-gains-the-registry-root-the-upstream-handshake-head-revalidation-and-upload-sessions)
lists them; the blocks of R-ARCH-008 and R-ARCH-009 in [architecture.md](../spec/architecture.md) and
[testdata/contract.txt](../../internal/formats/testdata/contract.txt) hold them. Where they live in code:

| Addition | Code | Production host |
|---|---|---|
| `Facts.RegistryRoot`, `Facts.DefaultCredentialHosts` | [catalog.go](../../internal/formats/catalog/catalog.go) (checked at `Register`) | — |
| `Refusal.Registry()` reads the base path from the catalog by `Format` (no field) | [route.go](../../internal/formats/route.go) | — |
| `LinkBuilder` renders the base path of the format; `ClientName`; `EndpointFacts.Bound` | [links.go](../../internal/formathost/links.go); the dispatcher sets `Bound` with the mount of bound hosts | yes |
| `Route.PackageParam` | [route.go](../../internal/formats/route.go); set to `name` by every OCI route | — |
| `ItemsEvent.Cascade` | [hooks.go](../../internal/formathost/hooks.go) (`Hooks.cascade`), wired through `Deps.Remover`; the system actor `removal` when the removal had no request | yes |
| `SetupData.ImagePrefix` | [setup.go](../../internal/formats/setup.go); filled by `imagePrefix` in [packages_api.go](../../internal/api/v1/packages_api.go) | yes |
| `Authenticator` (`TokenRequest` gets the answered request), `Revalidator`, `RemoteCaps.Auth`, `RemoteCaps.Revalidate` | [remote.go](../../internal/formats/remote.go), found by `CapabilitiesOf` | yes: [formathost/remote.go](../../internal/formathost/remote.go) adapts them to `proxy.Authenticator` and `proxy.Revalidator`, the engine runs them ([proxy.md](proxy.md) "The token handshake and the revalidation request", ADR-165); the OCI adapter that implements them is its own work package |
| `RemoteDocument.MediaType`, `Prepared.Aliases` | `remote_cache_entries.media_type` (migration 00055); [metadata.go](../../internal/proxy/metadata.go) (`enterAliases`, in the fill transaction, through `items.Store.LinkIn`) | yes |
| `Host.Uploads()`, `UploadSessions`, `UploadSession`, `SessionRef` | [host.go](../../internal/formats/host.go) | [uploads.go](../../internal/formathost/uploads.go) over [internal/uploads](../../internal/uploads/): decides principal, binding and `deploy` on the target on every call ([Upload sessions](#upload-sessions-r-stor-011)); a host built without the service answers `not_implemented` |
| `Ingestor.Link(from SourceRef)` | [host.go](../../internal/formats/host.go) | [ingest.go](../../internal/formathost/ingest.go) (`linkSource`: resolves the repository by name, a virtual one through its members; live, served, not blocked, readable), `items.Store.CommitLinked` and `blob.Store.LinkExisting`; the in-memory host implements it |
| `discover.Upstream.Artifact` | [discover.go](../../internal/mirror/discover/discover.go) | [mirror_host.go](../../internal/formathost/mirror_host.go): `not_implemented` |

A push by tag commits the tag item as the lead and the manifest item as its companion (a companion goes with a lead that changed), or links the
tag with `Ingestor.Link` when the manifest is stored; a push by digest has the manifest as the lead (R-OCI-013).

## Removal, the sweep and the replay

One hook holds the rules of every removal: [hooks_remove.go](../../internal/formats/oci/hooks_remove.go) `OnItemsRemoved`. The `DELETE`
routes do not remove anything themselves; they decide authorization (`delete` on the image name, with the tag as the version), look the
item up for the 404, take the lock of the name's package and the ownership claim, and call `PackageWriter.DeleteItems`, so the item store removes and the hook
runs in the same transaction. A raw delete through `/files/`, the items API, a retention run and the sweep take the same road.

| A removed item | What the hook does |
|---|---|
| `<name>/_tags/<tag>` | Deletes the version row the tag led (`TrashOrDeleteVersion`); the manifest stays |
| `<name>/_manifests/<digest>` | Refuses (conflict, 409 `DENIED`) when an index of the name lists it and is not removed by the same event; else reads its tags, referrers and children, removes its row (the references go with it), cascades (`ItemsEvent.Cascade`) to the tags, to the referrers no index lists and to the children no other manifest and no tag names, and records `oci.manifest.deleted` with the journal entry; the hook runs again for what was cascaded |
| `<name>/_blobs/<digest>` | Refuses while a manifest of the name references it and is not removed by the same event |
| any item of a remote repository's `_manifests/` | Removes the manifest rows (Q-OCI-34); no event, no cascade |

An event with the reason `trash` keeps the rows and cascades nothing (the trash can does not exist yet) but still refuses. A
blob removal changes no row of the format and raises no revision; everything else raises the package's `rev_seq`.

The sweep ([sweep.go](../../internal/formatdata/oci/sweep.go)) is built by `startOCISweep` in [wire_formats.go](../../internal/app/wire_formats.go) on a
replica that claims jobs and handed the item store late (`a.itemStore`), like the job that deletes a subtree; the kind `oci-sweep` and
its hourly task are registered in `registerFormatJobs`. Each blob item is removed in a transaction of its own that locks the package row
of the name when it has one (a name that holds only blobs has none and gets none: the blob rows arbitrate, a push holds them `FOR SHARE`)
and asks `OCIBlobStillUnreferenced` again (references and unfinished upload sessions), then calls the item store's `RemoveIn` as the
system actor `removal`; a blob the hook refuses because a push committed a reference meanwhile (`oci_referenced`) is counted as kept. The scan (`OCIUnreferencedBlobs`) walks the blob items of a repository in path order and has no index of its
own (Q-OCI-71).

The replay ([journal_replay.go](../../internal/formatdata/oci/journal_replay.go)) is registered with `journal.Register` in `init`; `cmd/trove`
links the package, and `TestR_HA_054_EveryJournaledEventHasAReplayHandler` fails when it is not there. It withholds (writes the mark and
raises the revision) and deletes nothing. `Reinstate` ([reinstate.go](../../internal/formatdata/oci/reinstate.go)) is the command's transaction.
The security view is [docs/security/oci-content.md](../security/oci-content.md).

## Registry tokens and the token service

R-OCI-004 to R-OCI-006 and R-OCI-034; decisions Q-OCI-50 to Q-OCI-57 and
[ADR-163](../decisions.md#adr-163--registry-tokens-are-bound-to-their-trove-token-by-the-hash-of-its-secret-and-verified-through-the-token-cache);
the security view is [security/oci-registry.md](../security/oci-registry.md).

What has to be in your head:

- **A registry token is its trove token narrowed.** The claims name the trove token by its ID, its type and the SHA-256 of its
  secret; the stage verifies the MAC without a lookup and then asks the token cache with the hash
  (`token.Service.CheckSecretHash`), and both paths share `Stage.accept`, so revocation, the freshness gate, the throttle and the
  endpoint's rules are the trove token's. Claims that span more than `oci.token_lifetime` or are issued more than a minute ahead are
  refused.
- **The chain does not authenticate `/v2/_token`.** The stage returns the anonymous result for that path whatever the request
  carries; the token service takes the one credential (`auth.PresentedFrom` for the header of a `GET`, `auth.PresentedForm` for the
  form of a `POST`) and hands it to `Stage.AuthenticateTokenRequest`. A registry token is no credential there.
- **The grant is a ceiling, enforced in one place.** The token service grants per scope entry by the coarse check, asked with
  `PathsAllowed` (no audit event) and the image name as the package. `registry.Enforcer` wraps the whole mount of `/v2/` and refuses a
  registry token on every route that is not registered with it or whose grant lacks the route's action (Q-OCI-56); then the decision
  point decides every request again.
- **`internal/auth` does not depend on the decision point.** `registrytoken` holds the form, the keys, the scope grammar, the grant and
  the challenge; the grant decision and the action mapping (`ActionOf`) are the registry root's (`internal/api/registry`).
  `go list -deps ./internal/auth | grep authz` prints nothing.

| File | Responsibility |
|---|---|
| [registrytoken/token.go](../../internal/auth/registrytoken/token.go) | `Signer` (`NewSigner(ring, lifetime)` derives the MAC keys from the ring with info `trove registry token v1`; `Issue`, `Verify`), `Claims`, `Binding`, the form `trv_rgt_` + base64url(`1:<key id>:<JSON>`) + `.` + MAC, the limits (`MaxLength` 4 KiB, `MaxEntries` 16, `MaxLifetime` 1 h, `MaxClockSkew` 1 min) |
| [registrytoken/grant.go](../../internal/auth/registrytoken/grant.go) | `Actions` (`Pull`, `Push`, `Delete`), `Entry`, `Grant.Allows`, `Grant.Add`, `FullName` |
| [registrytoken/scope.go](../../internal/auth/registrytoken/scope.go) | `ParseScopes`: the scope grammar of the distribution token protocol, 16 entries at most, unknown entries skipped, the bound host's short names, a cache view under its base repository; `RepositoryScope` for a challenge |
| [registrytoken/challenge.go](../../internal/auth/registrytoken/challenge.go) | `TokenPath`, `Realm` (realm and service of a base URL; `("", "")` without one), `Challenge` (the `WWW-Authenticate` of R-OCI-004 step 1), `ErrorInvalidToken`, `ErrorInsufficientScope` |
| [registrytoken/metrics.go](../../internal/auth/registrytoken/metrics.go) | `trove_oci_registry_tokens_total{outcome}` |
| [stage.go](../../internal/auth/stage.go) | `byRegistryToken`, `anonymousRegistry`, `accept` (shared with `byToken`), `AuthenticateTokenRequest`, `Presented` (renders its kind only); the registry challenge below `/v2/` (`Options.RegistryRealm`, `Options.RegistryScope`); no session cookie below `/v2/` |
| [api/registry/token.go](../../internal/api/registry/token.go) | `Token` (`NewToken`, `TokenOptions`): reads the `GET` and the `POST` form, checks `service`, authenticates through the hook, grants, signs, answers; the 400 body of Q-OCI-52; `auth.registry_token.denied` and the metric |
| [api/registry/grantor.go](../../internal/api/registry/grantor.go) | `Grantor.Grant` over an `Asker` (`*edge.Access`) and a repository reader: the coarse check per entry and action, then only repositories of the format that are served; `ActionOf` |
| [api/registry/enforce.go](../../internal/api/registry/enforce.go) | `Enforcer` (`NewEnforcer`, `Mount`, `Routes`, `Patterns`, `UncleanedPrefixes`, `Scope`, `Check`, `Also`), `Route`, `RouteKind`, `RepositoryRoutes`: the enforcement point of Q-OCI-56; `Mount` also registers the uncleaned prefix of a subtree pattern; `Scope` is the stage's `RegistryScope` |
| [api/registry/route/route.go](../../internal/api/registry/route/route.go) | The leaf the dispatcher and the registry root share: `Target`, `Matcher`, the core route names `NameVersion`, `NameCatalog`, `NameToken`; standard library and `qname` only, so the dispatcher depends on neither the registry root nor the authentication stage (archtest `moduleBans`, transitive) |
| [api/registry/register.go](../../internal/api/registry/register.go) | `Register`, `RegisterOptions`, `Pattern` (`/v2/{rest...}`), `BarePattern` (`/v2`), `ExampleRepo`: mounts the dispatcher's handler for everything below `/v2/` through the enforcer with every route of the format and the three core routes, and the format's error shape for `/v2/` |
| [api/registry/ping.go](../../internal/api/registry/ping.go), [challenge.go](../../internal/api/registry/challenge.go) | `NewVersion`: `GET` and `HEAD` of `/v2/` (200 `{}`; 401 with its own challenge for an anonymous caller without a registry token, Q-OCI-20); `Challenge`: that refusal |
| [api/registry/catalog.go](../../internal/api/registry/catalog.go) | `Catalog` (`NewCatalog`, `CatalogOptions`, `CatalogStore`): the readable repositories in the order of their name prefix, names from the data port's `CatalogNames` in pages of 500, each decided with the listing form of the decision point, `n`/`last`/`Link` (Q-OCI-101) |
| [api/registry/registrytest/walk.go](../../internal/api/registry/registrytest/walk.go) | `Walk.Run`: the proof through the endpoint's whole handler, with minted registry tokens; its own expectation from the format's `Match`, the served route from the `Served` probe, `Bound` for a bound-host endpoint |
| [api/dispatch/registry_target.go](../../internal/api/dispatch/registry_target.go) | `registryMatch`: the dispatcher's one match below `/v2/` (core routes, the format's `Match`, the cache view, the bound host, the clean path) returning the target, the format, the route and its parameters; `RegistryTarget` is it reduced to the target; `BoundRepository`: the binding of the host root, for a repository of the format only (Q-EP-80); `RegistryFormat` |
| [api/dispatch/registry.go](../../internal/api/dispatch/registry.go) | `RegistryHandler(CoreRoutes)`: the one handler of everything below `/v2/`; matches with `registryMatch`, calls the core handler the match names or `serveRoute` (the tail of the `/api/<format>/` path: Bastion marking, coarse check, repository lifecycle and class, handler) |
| [httpserver/routetable.go](../../internal/httpserver/routetable.go), [router.go](../../internal/httpserver/router.go) | `CheckRouter` (patterns, probe of each pattern in a scratch mux, uncleaned prefixes, fallback), `HandleUncleaned` (panics at or below `/v2`), `HandleRegistryUncleaned`, `UncleanedPrefixes`, `HasFallback` |
| [app/wire_registry.go](../../internal/app/wire_registry.go) | `registerRegistry` (per endpoint: enforcer, token service, version check, catalog, `Register`; sets `rt.registry`), `registryTokenAuth` (the stage for the token service with `withMinEpoch` and `recordPrincipal`), `registryScope` (the stage's `RegistryScope`), `registryTokenMetrics` (once for all endpoints) |
| [app.go](../../internal/app/app.go), [wire_endpoints.go](../../internal/app/wire_endpoints.go) | `endpointRuntime.registry`; the route-table check admits exactly its patterns below `/v2/` (the second list of `CheckRouteTable`; the third, the formats' host-root patterns below `/.well-known/`, is empty until a format registers a discovery document) |
| [events_registry_token.go](../../internal/audit/events_registry_token.go), [dedup.go](../../internal/audit/dedup.go) | The event and its deduplication per client address, endpoint, reason and credential |
| [m1_convert.go](../../internal/app/m1_convert.go) | `registryGrant`: `auth.Credential.Registry` to `httpserver.Credential.Registry` (`RegistryGrant.Allows`), for anonymous registry tokens too |
| [wire_identity.go](../../internal/app/wire_identity.go) | `wireAuth` builds the `Signer` from the key ring and `oci.token_lifetime` and gives it and `registryRealm` to the stage; `authArea.registry` holds the signer for the token service; `credentialRules(rt)` are the endpoint's rules for the chain's stage and the token service |

**The mount of `/v2/` (built by the registry root's work package).** [wire_registry.go](../../internal/app/wire_registry.go) does, per
endpoint whose surface holds `packages` and on a replica with a format host:

1. **One enforcer** (`registry.NewEnforcer` with the dispatcher as the `Matcher` and the format's `WriteError`), set as `rt.registry`; the
   start-up check `httpserver.CheckRouter` admits exactly `rt.registry.Patterns()` and `UncleanedPrefixes()` below `/v2/` and none
   while it is nil (an endpoint without `packages` has none).
2. **Two patterns, one handler** (`registry.Register`): `/v2` and `/v2/{rest...}`, every method, mounted with `enforcer.Mount(router, patterns, h, routes...)` together with every route of the
   format (`RepositoryRoutes(format.Routes(), "/v2/web/docker-local/")`: its name, action and first example) and the three core routes
   (`route.NameVersion` as `GET /v2/`, `NameCatalog`, `NameToken`). The handler is `Dispatcher.RegistryHandler`: it calls `registryMatch`
   once and serves what that names. Do not add a further pattern below `/v2/` (the route table refuses it) and do not parse a
   `/v2/` path anywhere else: a new reader of the path calls `registryMatch` (or `RegistryTarget`), never `Format.Match` on its own.
3. **The token service** (`registry.NewToken`): `Signer` from the auth area, the decision point, the repository reader, `Format: "oci"`,
   `ValidName`, the shared metrics (`registryTokenMetrics`), the audit recorder, the format's `WriteError`, `Bound: disp.BoundRepository`
   and `Authenticate: registryTokenAuth` (`AuthenticateTokenRequest` with `credentialRules(rt)` inside `withMinEpoch`, then
   `recordPrincipal`, so the request log names the caller). `Bound` is mandatory: the token service has no default that reads the
   context.
4. **The stage's scope**: `auth.Options.RegistryScope = a.registryScope`, which asks the enforcer of the request's endpoint
   (`Enforcer.Scope`): `repository:<name>:<action>` for a repository route, `registry:catalog:*` for the catalog, none for the rest.
5. **The version check** is the handler's own 401 for an anonymous request without a registry token (the stage lets it through where
   anonymous access is on); **the catalog** takes the data port's page by a type assertion to `registry.CatalogStore` (`*ocidata.Store` satisfies it, checked at
   compile time in `wire_registry.go`; start-up fails if the port does not).
6. **The route table**: the check runs per endpoint in `startClient` after the registry is mounted.

The source of a cross-repository mount is checked by the enforcer, not by the handler: `EnforcerOptions.MountRoute`/`MountSource` make it
drop `mount` and `from` from a registry token's `oci.upload.start` request that lacks pull on the source, so the handler's own fallback
(202 and a session) answers (Q-OCI-102). What is left: whichever of the registry root and the upload handlers merges second replaces the
copy `ociMountSource` in `wire_registry.go` with `ociformat.MountSource` and adds the integration mount test with a positive control
(pull on the source: 201) and the bound-host case (pull only on `(bound, p/k/img)`, `from=p/k/img`: no link from `p/k`).
`Format.Match` and the OCI route handlers are WP4's; the catalog's page is `Store.CatalogNames` of the data port. A registry request on
a bound host reaches the handler with `Endpoint.Bound` set from the match (`routed.bound`), never from the raw binding.

Rate limits: the chain sees every token request as anonymous, so a rate-limit class keyed by principal counts token requests in the
bucket of the client address (Q-OCI-51, H-650).

**The walk (the end-to-end proof).** `registrytest.Walk{Enforcer, Handler, Patterns, Format, Repo, Mint, Prepare, Served, Bound}.Run(t)`
with the endpoint's whole handler. The integration test ([registry_test.go](../../test/integration/registry/registry_test.go)) uses
`App.EndpointHandler`, `EndpointRouter` and `RegistryEnforcer`, mints tokens at the token service with a real trove token, and reads
which route served a request from the `trove_http_requests_total{handler=…}` series the replica counts (the dispatcher's label).
`Bound` makes the walk one of a bound-host endpoint. It fails for a route of the format that is not mounted, a `/v2/` pattern around
the enforcer, an example whose route the format's `Match` names differently or another route serves, a route an empty grant passes, and
a route that a grant of every other action passes or a grant of exactly its action does not.

**The upload-session contract (WP1, WP3, WP5).** The enforcement point checks a registry token's grant against the image name of the
path. An upload session is continued (`PATCH`), finished (`PUT`), queried (`GET`) and cancelled (`DELETE`) by its ID under that path, so
the grant of `push` on image A must never reach a session of image B: every call of `formats.UploadSessions` (`Get`, `Append`,
`Finalize`, `Abort`) passes the `SessionRef` with the binding the format names (the image name of the path) and the addressed repository,
and the host answers 404 `BLOB_UPLOAD_UNKNOWN` unless the session belongs to that repository, that image name and the request's principal
(R-STOR-011, Q-OCI-26). A session handler that reads the session by its ID alone, or takes the image name from the session instead of the
path, lets a registry token push into an image its grant does not name.

## Blob uploads and the mount (R-OCI-011, R-OCI-012)

The handlers hold the protocol and nothing of a session: the state is the host's session port ([Upload sessions](#upload-sessions-r-stor-011)),
so any replica continues a session and none keeps anything of it. The routes serve the classes local and virtual (ADR-180): the handlers act on the request's
repository, or through `writeRequest` on the deployment target of the virtual repository the client addressed, and the session records both.
Decisions: [ADR-175](../decisions.md#adr-175--the-upload-and-mount-handlers-hold-the-protocol-and-the-host-holds-the-session-and-the-link),
Q-OCI-110 to Q-OCI-114. The security view is [oci-uploads.md](../security/oci-uploads.md).

| Request | Handler | What it does |
|---|---|---|
| `POST …/blobs/uploads/` | `handleUploadStart` | `?mount=` is the mount, `?digest=` a monolithic upload, else `Uploads().Start` with the image name as the binding: 202, `Location`, `Range: 0-0`, `OCI-Chunk-Min-Length` (the session's part size), `Docker-Upload-UUID` |
| `POST …?digest=<d>` | `handleMonolithic` | `Authorize` deploy on the blob path, `Ingest().Spool`, the computed SHA-256 against the named one (400 `DIGEST_INVALID`, nothing stored), `Ingest().Commit` of `<name>/_blobs/<d>`: 201, no session |
| `PATCH <location>` | `handleUploadPatch` | `chunkStart` reads `Content-Range` (`-1` without it), `Append`; 202 with `Range: 0-<received-1>`; the 416 of an offset mismatch gets `Range`, `Location` and `Docker-Upload-UUID` from the typed `received` (`outOfOrder`) |
| `PUT <location>?digest=<d>` | `handleUploadPut` | a `Content-Range` on the last bytes is checked against `Get().Received` (416), then `Finalize` with the blob path; 201, `Location` of the blob, `Docker-Content-Digest`; a wrong digest keeps the session |
| `GET`/`DELETE <location>` | `handleUploadStatus`, `handleUploadCancel` | `Get` (204 with `Range`, `Location`, `OCI-Chunk-Min-Length`), `Abort` (204) |
| `POST …?mount=<d>&from=<src>` | `handleMount` | below |

Every session call passes `SessionRef{ID, Binding: <image name of the path>}`; an id that is no UUID is 404 `BLOB_UPLOAD_UNKNOWN` without a call. The
handlers never read a session by its id alone, and never take the image name from the session (the contract above).

**The mount.** `handleMount` validates the digest (400 `DIGEST_INVALID`), parses `from` with `MountSource` (the shared host:
`<projectKey>/<repoKey>/<name>`; a bound host, `rq.Endpoint.Bound`: an image name of the addressed repository; a key with `~cache` is the base
repository of a remote's cache view and `cache` is true), decides `deploy` on the target blob path, asks the ownership rule without a publish (`checkOwnership`, Q-OCI-115), and calls
`Ingest().Link(rq, SourceRef{repo, BlobPath(srcName, digest)}, Commit{...})`. The host ([linkSource](../../internal/formathost/ingest.go)) resolves the repository by name and
refuses one of another format than the target's; decides the read as a read is decided (`Host.Authorize` on a request derived for the source: the decision, the repository's include and exclude
patterns, the refusal of a claimed name for a remote; for a virtual source the routing, `SourceRouter`, and `MemberUsableOn` per routed member); and checks that the item is a live file whose blob is served
and that no finding blocks it. It does not run the format's serve check. Every failure of that is `errs.NotFound`, and `handleMount` answers a `NotFound`, a `from` that names no image, and a `mount` without `from` the same way:
`startSession`, 202. Anything else (the target's deploy gone, a store failure) is the error it is. The commit hook refuses, as defence in depth,
a blob item whose content is not the digest of its path, whose name is no digest or whose commit carries no SHA-256 (`OnCommit`, 409 `DENIED`, Q-OCI-113).

`oci.MountSource` is exported for the registry root: the enforcement point (`Enforcer.withoutUnreadableMount`, `internal/app/wire_registry.go` passes `ociformat.MountSource`) checks the grant of a registry token on the source
(`pull` there) before the handler runs, with the repository and the name this function returns, and drops `mount` and `from` of a request whose grant lacks it, so the handler opens a plain session (Q-OCI-102). Both read the
first value of a repeated parameter, and `from` against the bound repository the match read: `EndpointFacts.BoundRepo`, set by the dispatcher's `serveRoute` from the match (for a host bound to a virtual repository it is the
virtual one, while the request `writeRequest` makes for a write acts on the deployment target). See [The grant on the source](../security/oci-uploads.md#the-grant-on-the-source); the end-to-end test is
[mount_test.go](../../test/integration/registry/mount_test.go).

The `Finalize` of the port takes the offset of the last bytes as `Append` does (-1 appends at the end), decided under the session's claim, and returns the received count with a 416 in `Committed.Received` (ADR-178, contract version 3);
`handleUploadPut` passes the `Content-Range` start (-1 for none or an empty body). It asks `deploy` and then the ownership rule before the body is read, as the start of a session, a monolithic upload and a mount do
(`checkOwnership`: no publish, `FirstVersion` from the live versions of the name, Q-OCI-115); a session is decided on `<name>/_taglist` (Q-OCI-116).

**Tests.** Unit: [handlers_upload_test.go](../../internal/formats/oci/handlers_upload_test.go) (`TestR_OCI_011_…`, `TestR_OCI_012_…`) over the kit's in-memory host
and a session port that enforces the binding: the headers, chunks with and without `Content-Range`, the 416 and its `Range`, the strict header, the wrong
digest, the binding and non-UUID ids on every method, abort, the monolithic upload, the mount (201, the fallback shapes compared field by field, deploy on the
target, a source of the wrong content, a withheld manifest digest, a bound host, `MountSource` table). Integration, on the production host with three replicas
(each its own blob store, spool and session service, requests pinned per replica) against PostgreSQL and S3:
[oci_upload_test.go](../../test/integration/files/oci_upload_test.go) (`make test-integration`; the format is mounted at `/api/oci/` as in
[oci_fixture_test.go](../../test/integration/files/oci_fixture_test.go); the protocol criteria of the push run over `/v2/` in [test/integration/oci](../../test/integration/oci)).
## The remote class (R-OCI-021 to R-OCI-025)

The engine owns the request path, the freshness, the flights, the negative cache, the circuit, the handshake's I/O and the redirects; the format
([remote.go](../../internal/formats/oci/remote.go) and its neighbours) answers four questions of it. The routes are the local class's: the four
read handlers of WP4 hand a request addressed to a remote repository to `remoteManifestGet`, `remoteBlobGet`, `remoteTagsList` and `remoteReferrers`
([remote_handlers.go](../../internal/formats/oci/remote_handlers.go)); a write answers 405 because the route is `Classes: local`.

| Resource | Kind | Canonical path | Upstream request | Stored as |
|---|---|---|---|---|
| Manifest by tag | metadata (`Metadata`) | `<name>/_tags/<tag>` | `GET /v2/<prefix/name>/manifests/<tag>`, `Accept` the four manifest types | the cache entry, with the upstream's `Content-Type` in `media_type`, and the alias item `<name>/_manifests/<digest>` for the same blob |
| Manifest by digest | content (`Artifact`) | `<name>/_manifests/<digest>` | the same URL with the digest | an item; verified against the digest of the path (`BeforeArtifact`) |
| Blob | content (`Artifact`) | `<name>/_blobs/<digest>` | `GET /v2/<prefix/name>/blobs/<digest>`, redirects followed by the engine | an item; verified against the digest of the path |
| Tags list | metadata | `<name>/_taglist` | `GET …/tags/list?n=10000` | the cache entry |
| A window of the tags list beyond the stored one, referrers | pass-through (`Query`) | `<name>/tags/list`, `<name>/referrers/<digest>` | `UpstreamQuery` | the engine's per-replica answer cache, for the metadata period |

- **The namespace prefix** is the path of `remote.url`: `registryURL` puts it between `/v2/` and the image name, and `scopeOf` names the prefixed
  name in the token scope. The `library/` mapping is the handler's (`remoteName`): the name, the canonical path, the cache entry and the package are
  `library/<n>`, so the adapter never sees the unmapped name.
- **What a response is checked for.** `AcceptResponse` sees the headers and not the body, `PrepareFill` and `InspectFill` the body and not the
  headers. Two checks need both: the digest a tag announces in `Docker-Content-Digest` is checked by a reader `AcceptResponse` puts around the
  body (`digestBody`: the SHA-256 of what passed against the header, failing at the end of the body with `upstream_integrity_mismatch`, so nothing
  is stored; the engine raises the integrity event and counter for it in `bodyFailure`; the verdict is given when the body ends, which the spool's reads reach,
  `TestTheDigestOfATagIsCheckedWhicheverWayTheBodyIsRead`), and the cut of a tags list is recorded by a reader that adds the member
  `x-trove-truncated` (`truncatedBody`), which makes the body longer than the length the upstream announced, so `AcceptResponse` sets `ContentLength` to -1 and
  deletes the header (Q-OCI-126). A manifest is bounded before it is spooled (`boundedBody`, and the announced length). The media type of a
  fetched manifest is the document's own `mediaType` member, and for a document without one an index when it has `manifests` and an image manifest
  otherwise (`fetchedMediaType`). Schema 1 is refused by type in `AcceptResponse` and by `schemaVersion` in `readFetched`. Everything the parser
  refuses is 502 with `MANIFEST_INVALID` in the detail.
- **Where the rows come from.** A tag's manifest is parsed in `PrepareFill` (outside any transaction) and its rows are written by `OnFill`, unless the
  stored entry was fetched later (R-REPO-026); the engine then enters the alias item and its artifact entry. A manifest fetched by digest is parsed
  by `InspectFill` before it is stored and its rows are written by `OnCommit` (`remoteCommit` in [hooks.go](../../internal/formats/oci/hooks.go)),
  from the inspection. Both raise the revision of the image name's package. The alias item commit carries no inspection and writes no row; removing it
  removes the rows (`onCacheRemoved`, Q-OCI-34).
- **The handshake.** `TokenRequest` reads the Bearer challenge's realm and service and builds `GET <realm>?service=…&scope=repository:<prefixed name>:pull`
  from `orig` and the path (`registry:catalog:*` for the catalog); the key is `scope=<scope>`, to which the engine adds the repository, the
  configuration version, the token service and the credential. A Basic challenge or none is `ok` false. `ReadToken` accepts `token` or
  `access_token`, refuses a token above 16 KiB or with a character that is no header value, and returns `expires_in`.
- **Revalidation.** `RevalidationRequest` clones the engine's `GET` into a `HEAD` (same URL and `Accept`) for a tag; `Unchanged` compares
  `Docker-Content-Digest` with the stored digest (and the type, when the `HEAD` names one). The tags list is revalidated by the engine's conditional `GET`.
- **The tags list.** `remoteTagsList` fills the page from a `tagSource`: the stored document's window after `last` (byte order, deduplicated, tags outside the
  grammar dropped), decided in chunks with the listing-form check until `n` readable tags are found (the `Link` names the last tag listed, never a hidden one;
  the scan budget is the local class's), and, when the stored list was cut, the upstream's pages after the last tag taken (`pass`, `n` 1,000, passed through as
  queries). The upstream's `Link` does not reach the format, so the `Link` is trove's: set whenever the stored list was cut or a pass-through page was used,
  and a page that is empty ends the list. When the upstream cannot be asked (`unreachable`: offline, the circuit open, a caller who may not fill) the stored
  window answers; referrers answer 404 (Q-OCI-128).
- **The upstream test** requests `/v2/` at the root of the host (Q-REPO-281); a `401` with a Bearer challenge reads as reachable with `authenticated: null`
  (`catalog.Facts.ProbeChallengeReachable`, Q-REPO-280), unless `authMode` is `bearer`. `Register` refuses the fact without the class remote, a probe path and a
  remote adapter that implements `Authenticator`.
- **A stood `401`.** `StoodMissing` (the optional `formats.StoodChallengeReader` of the `Authenticator`) reads a Bearer challenge with
  `error="insufficient_scope"` as a missing image: the engine answers it as a 404 with a negative entry and keeps the remembered challenge (Q-OCI-127).
- **Docker Hub.** The default extra host is `auth.docker.io` (a credential host). The blob host Docker Hub answered with on 2026-11-08
  (`production.cloudfront.docker.com`) is not listed: `extraHosts` do not gate redirects (Q-OCI-125). `libraryName` maps only a URL without a namespace prefix, and
  `gateRemote` allows every read on both spellings (`Authorize` on the typed one, `PathsAllowed` on the other, `bothAllow` for the entries of a list) and `SourceSpellings` gives a mount's source the other one (Q-OCI-129).

Tests: [remote_test.go](../../internal/formats/oci/remote_test.go) (the adapter and the handlers over the kit's in-memory engine) and
[test/integration/ociremote](../../test/integration/ociremote) (the whole application against the stub registry, below).

### Testing the remote class through the application

[env_test.go](../../test/integration/ociremote/env_test.go) sends its requests to `/v2/<project>/<key>/` of a running replica with the registry token the token
service issues to the trove token for the scope of the image (`registryToken`, cached for a minute), as a container client does, so the enforcement point and the grant
check are in the path. Everything is the application's: the engine, the egress rules, the storage hooks, the data port,
the repository validation. The stub runs in the test process on TLS; `Running.DialOverride()` reaches its three names (`registry.localhost`,
`auth.localhost`, `cdn.localhost`) and a second stub answers as Docker Hub's three (`registry-1.docker.io`, `auth.docker.io`,
`production.cloudfront.docker.com`), so no name is resolved.

## The mirror (R-OCI-026)

A remote repository in mode `mirror` keeps the images its settings select. The engine is the mirror's ([mirror.md](mirror.md)); the format contributes the lister, in four
files, and sends no request of its own: the catalog and the tags lists go through `Upstream.Get` (egress rules, token handshake, background slot, throttling), the tag
documents through `Upstream.Document`, the manifests an index names through `Upstream.Artifact`. Every call is idempotent; nothing is kept between calls.

| Step | Where | What |
|---|---|---|
| Methods | `MirrorMethods` | `listing` when `GET /v2/_catalog?n=1` answers 200 with a `repositories` array; `names` always; no feed |
| Names | `mirrorListing`, `mirrorExactNames` | The catalog in pages of 1,000 by `last` (the `Link` is read for its `last` only; the next request is built here, on the upstream origin), each name below the namespace prefix, checked by `Scope.Admits`; or the include patterns without a wildcard, as written (`nginx` stays `nginx`, Q-OCI-183) |
| Tags | `readTags`, `selectTags` | The tags list in pages of 1,000, at most 100,000; the tags `oci.mirror.tags` matches and `excludeTags` does not; wildcard matches newest first by [tagorder](../../internal/formats/oci/tagorder/tagorder.go), then the exact tags, `Pinned` (Q-OCI-180) |
| Bounds | `MirrorPlan`, `discover.BoundsOf` | A tag the engine would drop is not read (Q-OCI-182) |
| Items | `tagItems`, `itemBuilder` | The tag document (`Document`, a `HEAD` when stored); a single image gives its config and layers; an index gives the selected child manifests (`selectChildren`, `platformSelected`, attestations by `vnd.docker.reference.digest`), read through `Artifact`, and their config and layers; foreign layers (`urls`) are left out; every item carries the tag as its `Version` and the size its descriptor announces |

The host side is format-neutral: [mirror_host.go](../../internal/formathost/mirror_host.go) `fillPath` is `Fill` and `Artifact` (the gate, the engine's fill, the populate event of the system);
`Artifact` reads the stored bytes back through the item store and reports a fill it made to the package's tally with `mirror.ReportStored` (the engine counts it as an item and in `maxBytes`,
[fill.go](../../internal/mirror/fill.go) `plan`); `passUpstream.VersionBounds` implements `discover.Bounded`. The engine counts versions, not items
([policy.go](../../internal/mirror/policy.go) `SelectItems`), and fills a path once per attempt (`fillPackage`). The APT builders reuse `Artifact` and `Bounded` for their indexes.
The Owner rule for `oci.mirror` is [validate_mirror.go](../../internal/repository/validate_mirror.go) `checkDelegatedFormatMirror`.

What a pass costs on Docker Hub: a first pass of n kept tags is about n manifest `GET` requests plus one per kept child manifest (the only requests Docker Hub counts); a later pass is
one `HEAD` per kept tag. The platform filter matches `os/arch` against every variant and the stub's `acme/b` carries `linux/arm64/v8`.

Tests: [mirror_test.go](../../internal/formats/oci/mirror_test.go) (the lister over a fake `Upstream`: tag choice and order, the plan, platforms and attestations, foreign layers, a schema 1 tag, an unknown name,
the tags list in pages and its bound, the catalog and the exact names, the bounds), [versions_test.go](../../internal/mirror/versions_test.go) (versions, the shared path, the tally),
[mirror_format_test.go](../../internal/repository/mirror_format_test.go) (the Owner rule), and [test/integration/mirror/oci_test.go](../../test/integration/mirror/oci_test.go): the whole stack, two worker processes that
claim the queue `mirror`, a client-serving process and the counting stub (a catalog pass with the tag bound and a pull with the stub stopped, the exact-name pass, the platform filter, a second pass and a moved tag,
the byte bound, a Docker Hub mirror with the quota answer). The e2e rows (a pass while real clients pull, the drain) are in [e2e.md](e2e.md#the-oci-tests) (`TestMirror_R_REPO_055_AnOCIPassRunsWhileClientsPull`, `TestMirrorLong_R_REPO_058_DrainOfTheNodeOfAnOCIPass`).

## The virtual class (R-OCI-027, R-OCI-028)

The virtual engine ([internal/virtual](../../internal/virtual)) plans a request: the repository's patterns, the pin rules and claims for the image name, the members the caller
may read, and for each whether it may fill the member's cache. The OCI format does not use the engine's tiers or its merged-metadata cache (Q-OCI-160): it walks the plan itself and
calls the handler of a direct request on a request made for each member (`Request.Member`), so a member answers as it does when addressed and every decision it makes still names the
virtual repository as the addressed one.

| Request | Where | How |
|---|---|---|
| Manifest by tag | `virtualManifestGet` | `walk` over the local members, then the remote members, in member order; each through `handleManifestGet` (a remote through the remote request path) |
| Manifest or blob by digest | `virtualByDigest`, `digestSteps` | three passes: the local members; the caches of the remote members (`serveCached`, `Remote().Cached`, no upstream); the remote members the caller may fill |
| Tags list | `virtualTagsList` | each member's readable tags (`localMemberTags`, `remoteMemberTags`), merged by `virtualAdapter.Merge`, read again on the virtual repository (`pageVisible`), paged |
| Referrers | `virtualReferrers` | each local member's page after `last`, each remote member's list (`remoteReferrerList`), deduplicated by digest, read again on the virtual repository (`pageReferrers`) |
| Catalog | the registry root | not answered for a virtual repository (R-OCI-017) |

`walk` ([virtual_read.go](../../internal/formats/oci/virtual_read.go)) is the one rule for a miss and a failure (Q-OCI-162): a member that has not got the item, refuses the caller or may not be
filled is a miss; a member that cannot be asked, or whose object is lost, is remembered and is the answer when nothing serves; an error after the first byte ends the request. It puts
`Trove-Resolved-From` on the answer of the member that serves, when the status line goes out.

**Writes.** The write routes serve the class virtual (ADR-180). A handler that writes starts with `writeRequest`: the request itself for a local repository, a request made for the deployment
target (`Request.WriteTarget`) for a virtual one. Decisions, the ownership rule, the package rows and the commit act on that request; the upload sessions are called with the addressed
request (`rq.Host.Uploads().Start(ctx, addr, target, name)`), and every `Location` is built for the addressed one. A new write route must do the same. The DELETE handlers answer 405 for the class.

**The link step** ([virtual_write.go](../../internal/formats/oci/virtual_write.go) `linkFromMembers`) runs in `handleManifestPut` before the reference check when the addressed repository is
virtual: `Ingestor.Link` with the virtual repository as the source, by name, for each blob and manifest the target lacks; an index's child is read with `memberManifest` (a `GET` of the manifest
through the first two tiers into memory), parsed, and linked after its own blobs with the payload of a push by digest.

**Tests.** Unit: [virtual_test.go](../../internal/formats/oci/virtual_test.go) (the walk, the merge, the refusals of a write and a delete) and the merge cases of the conformance kit
([conformance_test.go](../../internal/formats/oci/conformance_test.go)). Application: [test/integration/ocivirtual](../../test/integration/ocivirtual) (the area is in shard b of
`make test-integration`): a virtual repository over `docker-local` and a remote repository on the stub, with registry tokens from the token service; the push is a chunked upload of every blob
and the manifest, as a client sends it. [env_test.go](../../test/integration/ocivirtual/env_test.go) builds the installation (two stubs whose `app:1` differ, claims, a virtual repository without
a deployment target); a writer holds `manage` unless the test says `plain`, because a first push of `app` is a shadowing decision (R-REPO-048), and `docker-local` does not auto-claim unless
the test asks (`autoClaim`). The security view is [oci-virtual.md](../security/oci-virtual.md).

## Configuration

| Key | Default | Rule |
|---|---|---|
| `oci.token_lifetime` | `300s` | 60 s to 3600 s (R-OCI-005); the token service never issues a token beyond the expiry of the trove token that requested it |
| `oci.unreferenced_blob_grace` | `24h` | at least 1 m and above `storage.uploads.session_max_age` (R-OCI-019); `validateOCICoupling` in [oci.go](../../internal/config/oci.go) |
| `storage.uploads.part_size`, `writer_lease`, `session_ttl`, `session_max_age`, `max_open_sessions` | `16MiB` (5MiB to 5GiB), `60s`, `4h`, `20h`, `100` | `max_open_sessions` is the cap per principal and repository (429 at `Start`, Q-STOR-176); `writer_lease` stays below `session_ttl` and above the time a part takes (a part must be sendable at 32 MiB/s within three quarters of it); the others exist since the storage section; `session_max_age` must stay below `storage.orphans.multipart_max_age`, and `storage.upload.max_size` must not exceed 10,000 parts of `part_size` (R-STOR-011) |

## Upload sessions (R-STOR-011)

A blob that a client sends over several requests is a session of [internal/uploads](../../internal/uploads/): a row of `upload_sessions`, the
rows of its parts and tail segments and, from its first full part, one S3 multipart upload of `<prefix>/_uploads/<session-id>/data`. Any replica continues it, none keeps anything of it between
requests, and Valkey is not involved (R-OCI-032: with Valkey stopped an upload goes on, because no step reads or writes it;
`TestR_OCI_032_TheSessionServiceReachesNoValkeyClient` pins the imports). The security view is
[upload-sessions.md](../security/upload-sessions.md); the decisions are [ADR-162](../decisions.md#adr-162--the-upload-session-service-is-a-domain-package-under-the-host-and-a-staged-blob-commits-through-the-commit-protocol-with-a-server-side-copy)
and Q-STOR-170 to Q-STOR-172.

| File | Responsibility |
|---|---|
| [service.go](../../internal/uploads/service.go) | `Service`, `Scope`, `Session`; `Start`, `Lookup`, `Get`, `Abort`; the fault hook; `unknown()` (404 `BLOB_UPLOAD_UNKNOWN`) and `outOfOrder()` (416 `BLOB_UPLOAD_INVALID`) |
| [append.go](../../internal/uploads/append.go) | `Append`, the claim, and `stream`: the body, read first through the hash states into segments; the segment that completes a part is joined with the recorded tail segments (`joinTails`, the only place they are read), sent under a renewed claim (`sendPart`) and recorded; what is left is written as one more tail segment (`endOfBody`, merged when there are 32) |
| [finalize.go](../../internal/uploads/finalize.go) | `Finalize`: the digest check and the move to `completing` (`closeOpen`), then `assemble` (`tailAsPart` under a short claim, `completeSmall` for a blob of less than a part, `CompleteMultipartUpload`) and the commit (`complete`); it returns a `Result` whose `Replayed` says that the session was done and the item is the recorded one |
| [rows.go](../../internal/uploads/rows.go) | the `rows` seam and its sqlc implementation (one transaction per method), the states |
| [hashstate.go](../../internal/uploads/hashstate.go) | the four hash functions of the digest set, marshalled into `hash_state` and restored by the next request |
| [uploads.go](../../internal/formathost/uploads.go) | the host's `formats.UploadSessions` over the service: the scope from the request, the target (the repository, or a virtual repository's deployment target), the `deploy` decision, the commit callback |

How a format's handler uses the port (the handlers are [handlers_upload.go](../../internal/formats/oci/handlers_upload.go)):

1. `POST …/blobs/uploads/`: `Uploads().Start(ctx, rq, target, imageName)` returns the session. `UploadSession.PartSize` is the value of the
   header `OCI-Chunk-Min-Length`; the id goes into the `Location` and `Docker-Upload-UUID`. For a virtual repository `target` is the deployment
   target (`Resolver().DeployTarget`); the host refuses any other repository.
2. `PATCH <location>`: `Append(ctx, rq, SessionRef{ID, imageName}, offset, body)` with `offset < 0` for a request without `Content-Range`. The returned
   `received` is the `Range: 0-<received-1>` of the 202 and of a 416, the error of which is an `*errs.Error` of kind `RangeNotSatisfiable`
   with code `oci_blob_upload_invalid`.
3. `PUT <location>?digest=<d>`: `Finalize(ctx, rq, ref, sha256, lastBytes, commit)` with the item `<name>/_blobs/<digest>` in `commit`; the host decides `deploy` on that path as well. A digest that
   differs is `oci_digest_invalid` and keeps the session open; a repeated request after a cut continues at the recorded state and does not read its body; one on a session that is done answers from the record (`Committed.Identical`, the recorded item id) and commits nothing.
4. `GET <location>` is `Get`, `DELETE <location>` is `Abort`. Every refusal of a session is `errs.NotFound` with the code `oci_blob_upload_unknown`:
   a session of another principal, repository or image name, a finished or expired one, and a `deploy` that is no longer allowed.

The fault points of `Service.Fault` (a test sets the field; a returned error ends the request as a killed process would, with no release of the
claim and no clean-up, and `errors.Is(err, blob.ErrFault)`; a hook may block to hold a request at the point):

| Point | Where |
|---|---|
| `append.claimed` | right after the claim of an append |
| `append.part_uploaded` | after `UploadPart`, before the part is recorded |
| `append.body_ended` | after the body ended, before the tail object is written (a request held here until its lease expired is the zombie writer of the acceptance criteria) |
| `append.tail_written` | after the tail object, before the record |
| `append.before_record` | after the stream, before the end of the body is recorded |
| `finalize.claimed`, `finalize.completing_recorded` | after the claim of a closing request; after the digest and the state `completing` were recorded |
| `finalize.completing`, `finalize.after_complete` | start of the step `completing`; after `CompleteMultipartUpload`, before the state `assembled` is recorded |
| `finalize.committed` | after the commit, before the staging objects are removed |

## Tests

| Test | What it pins |
|---|---|
| [service_test.go](../../internal/uploads/service_test.go) (`TestR_STOR_011_…`) | The state machine against a simulation of the tables ([memrows_test.go](../../internal/uploads/memrows_test.go)) and the in-memory store: three replicas, whole parts, every blob shape, 416, concurrent requests, the zombie writer, a cut finalize at each point, overlapping finalizes, the scope, the age, the size limit, a damaged tail, a drain |
| [uploads_test.go](../../internal/formathost/uploads_test.go) | The host's port: the decision before every call, the scope, the target of a virtual repository, the commit in the end-of-session transaction |
| [uploads_test.go](../../test/integration/uploads/uploads_test.go) (`make test-integration`) | The same acceptance criteria against PostgreSQL and the S3 store, with three replicas of their own: chunks through three replicas, 100 chunks making whole parts, concurrent requests, the zombie writer, the cut finalize, expiry by the collection run, the sweep of `_uploads/` |
| `TestR_ARCH_010_Conformance` ([conformance_test.go](../../internal/formats/oci/conformance_test.go)) | The kit over the skeleton; every skip and every route that is not built names its reason, and the kit refuses a skip the format contradicts |
| [format_test.go](../../internal/formats/oci/format_test.go) | The descriptor, the registry root in the catalog, the route matchers, `501` in the native shape, `WriteError` per row of R-OCI-008, `Inspect` on crafted manifests |
| [names_test.go](../../internal/formats/oci/names_test.go) | The grammars against the distribution specification's expressions over every string of a small alphabet, the layout, the `oci.mirror` schema |
| `TestR_ARCH_008_OciSettingsSchemaEqualsTheOpenAPIMember` | The JSON schema and `OciSettings` in `api/openapi.yaml` say the same |
| [authenticator_test.go](../../internal/proxy/authenticator_test.go), [handshake_test.go](../../test/integration/proxy/handshake_test.go) | The engine's side of R-OCI-021 and R-OCI-022 against the generic test upstream (no OCI knowledge): the handshake, the credential hosts, the token cache, the `HEAD` revalidation. Their names carry `R-REPO-033`, `R-REPO-027`, `R-REPO-054` and `R-ARCH-032`, the requirements the engine side builds; the `R-OCI-…` IDs are for the adapter that implements the handshake |
| [manifest_test.go](../../internal/formats/oci/manifest/manifest_test.go) | Valid documents written after the image specification's examples, ~70 refusals (duplicate and lookalike keys, digests, sizes, `data`, platforms), crafted bounds, the grammars against their expressions, the fuzz target |
| [handlers_manifest_test.go](../../internal/formats/oci/handlers_manifest_test.go) with the in-memory twin [twin_test.go](../../internal/formats/oci/twin_test.go) | Pulls, pushes, refusals, tag moves, concurrent pushes, the failed transaction, the first-version check, the view, through the kit's in-memory host |
| [store_test.go](../../test/integration/oci/store_test.go) | The data port's statements against PostgreSQL: rows, references, `FOR SHARE`, the annotation bound against the column, the view reads |
| [oci_upload_test.go](../../test/integration/files/oci_upload_test.go), [handlers_upload_test.go](../../internal/formats/oci/handlers_upload_test.go) | The upload protocol and the mount: see [Blob uploads and the mount](#blob-uploads-and-the-mount-r-oci-011-r-oci-012) |
| `TestR_OCI_02[1-5]_…` ([remote_test.go](../../internal/formats/oci/remote_test.go)) | The upstream request per kind and prefix, the four `Accept` types, the `HEAD` derived from the `GET`, schema 1 by type and by content, the announced digest against the bytes, the alias of a tag, the inspection of a manifest by digest, the token request (scope from the request, realm and service from the challenge, Basic is no handshake, a bad realm), the token document, the cut tags list, the tags list windows and the pass-through, referrers with the filter and 404 |
| `TestR_OCI_02[1-5]_…` ([test/integration/ociremote](../../test/integration/ociremote)) | Against the stack and the stub: the credential only to the realm of a credential host and the token only to the registry, a foreign realm refused, one challenged request per replica, tokens reused and not shared, one manifest `GET` per tag, `HEAD` revalidation and a moved tag, the digest of a fetched tag without a request, a wrong digest, schema 1, a missing tag, a multi-platform pull with each layer once and nothing sent to the CDN, tags list windows, referrers, `library/`, the quota answer, the upstream test |
| [test/integration/files/oci_fixture_test.go](../../test/integration/files/oci_fixture_test.go) | The seams for the OCI tests of the area `files` that stage what a replica cannot (a removal holding the package lock, the delete job's guard, a standing session): the real dispatcher, item store, hooks and S3 with the format mounted at `/api/oci/` through a wrapper. The protocol criteria that used to run here run over `/v2/` in `test/integration/oci` ([Q-OCI-214](../spec/formats/oci.md#q-oci-214--the-protocol-criteria-run-in-testintegrationoci-against-replicas-of-the-whole-application)) |
| [test/integration/oci](../../test/integration/oci) | The protocol criteria and the conformance suite: see [The protocol tests and the conformance suite](#the-protocol-tests-and-the-conformance-suite) |
| `TestR_OCI_015_…`, `TestR_OCI_016_…`, `TestR_OCI_018_…` and the setup tests ([handlers_lists_test.go](../../internal/formats/oci/handlers_lists_test.go), [handlers_delete_test.go](../../internal/formats/oci/handlers_delete_test.go), [hooks_remove_test.go](../../internal/formats/oci/hooks_remove_test.go), [setup_test.go](../../internal/formats/oci/setup_test.go)) | The lists, the deletion routes, the cascade and the refusals of the hook against an in-memory twin of the data port ([store_twin_test.go](../../internal/formats/oci/store_twin_test.go)), and the setup snippets |
| [test/integration/ocilocal](../../test/integration/ocilocal) | Against the stack: 2,500 tags in pages, the referrers, a raw delete of an index through a running replica (cascade, events, journal entries), the sweep (grace, sessions, the lock against a push, the job on a worker), the replay and the reinstate. The state is what a push leaves (items committed through the item store, the rows by SQL), so the sweep, the replay and a raw delete can be staged exactly |
| [oci_test.go](../../test/integration/postgres/oci_test.go) | The tables, their export classes, the phases of the three migrations and the key grammar at the database (`make test-integration`) |
| [registrytoken/*_test.go](../../internal/auth/registrytoken) | `TestRegistryTokenForm` (round trip, every character flipped, expiry, another endpoint, another key, unknown key, malformed values, the scanner pattern), `TestRegistryTokenClaimsRules` (with the configured lifetime and the issue time ahead), `TestR_ARCH_021_RegistryTokenKeyRingRotation`, `TestScopeGrammar` (with the cache view), `TestChallenge` (with no base URL), `TestGrantAllows`, `TestTokenServiceMetrics`, `TestTheLogScrubberHidesARegistryToken` |
| [stage_registry_test.go](../../internal/auth/stage_registry_test.go) | The stage: a registry token below `/v2/` only, as `Bearer` only and not on an escaped path, refusals without a token lookup, a revoked trove token, a claims mismatch (ID, owner, type), the endpoint's token types, bindings and `principal_disabled` (`TestR_EP_011_TheTroveTokensRulesReachItsRegistryToken`), a forged token counted by the throttle (`TestR_AUTH_038_…`), anonymous registry tokens, no session cookie below `/v2/`, the challenge, `AuthenticateTokenRequest`, `Presented` never printing its secret (`TestR_AUTH_001_…`) |
| [api/registry/*_test.go](../../internal/api/registry) | The token service (`TestTokenService`, `TestTokenServiceForm`: `GET` and `POST` with containerd's content type, the grant of read and deploy, the lifetime cap, the trove token that expires within the second, 400 in both shapes, the grant beyond 4 KiB, 405, the catalog, the cache view, a bound host; the `service` check; anonymous with and without anonymous requests; the event with its endpoint column and the metric; the request never printing its credential), `TestGrantIsTheCoarseCheck`, `TestActionOf`, the enforcement point through a whole handler with the real stage, signer, dispatcher and OCI routes (`TestTheEnforcementPointThroughTheWholeHandler` with `Walk`, the route table, the cache view, the own error code; `TestTheWalkCatchesAWrongMount`: a route left out, a matcher that resolves a push to the pull route, the version check off `/v2/`; `TestMountRefusesWhatItCannotEnforce`; `TestAlsoChecksTheMountsSourceActionByAction`); `TestTheRegistryMatchIsTheDispatchers` in [dispatch](../../internal/api/dispatch/registry_target_test.go) |
| [api/dispatch/registry_test.go](../../internal/api/dispatch/registry_test.go) | The registry handler with the real OCI format and fakes: the 404 `NAME_UNKNOWN` shapes (`TestAPathThatNamesNoReadableRepositoryIsNameUnknown`), the unclean path before the decision point, a core route served by its match only (the escaped spellings reach none), `TestTheRouteServedIsTheRouteMatched` (the label equals `RegistryTarget` for a list of requests), the bound host (`TestEndpointFactsBoundIsTheMatchsReading`: `Endpoint.Bound` comes from the match; `TestABoundHostIsTheDistributionAPIOfItsRepository`: a repository of another format or a missing one is no binding, a failed lookup), 405 for a class |
| [api/registry/root_test.go](../../internal/api/registry/root_test.go) | The version check per credential, the catalog's paging over repositories in byte order with fakes (`n`, `last`, bound, virtual, 501), `Enforcer.Scope`, `Register` |
| [httpserver/routecheck_test.go](../../internal/httpserver/routecheck_test.go) | `CheckRouteTable`'s probe (`/v%32/{rest...}`, host-only patterns), `HandleUncleaned` and `HandleRegistryUncleaned`, `CheckRouter`'s uncleaned prefixes and fallback |
| [test/integration/registry](../../test/integration/registry) | Against the stack with a full replica and a bound-host endpoint: the walk through the real handler on both endpoints, the version check, the 404 shapes (hidden and missing give the same bytes), the bound host, the catalog on the real decision point with a package-scoped token and a stand-in for the data port |
| [registrytoken_test.go](../../test/integration/identity/registrytoken_test.go) | Against the stack, two replicas with Valkey running and stopped (`TestRegistryTokensAgainstTheRealStores`): a token issued on one works on the other, a forged, tampered, expired or foreign token runs no statement (a tracer on the replica's pools counts the statements of the marked request, with a positive control), a revoked PAT stops its registry token within the bound; `TestAnonymousRegistryTokenAgainstTheRealStores` with and without `auth.anonymous` and after the switch is turned off; `TestR_ARCH_021_RegistryTokenKeyRingRotationAcrossReplicas` |
| [dedup_registry_test.go](../../internal/audit/dedup_registry_test.go) | `auth.registry_token.denied` collapses per address, endpoint, reason and credential |

The skeleton tests of WP0 carry no `R-OCI-…` in their names. A test name carries a requirement ID only when the test proves a whole acceptance criterion of it
([Q-OCI-213](../spec/formats/oci.md#q-oci-213--a-test-name-carries-the-id-of-a-requirement-when-it-proves-a-whole-acceptance-criterion-of-it), finding F-88); a test of a mechanism names the
requirement in a comment that says which criteria the requirement has and where they are proven ("Mechanism of R-OCI-0nn, not a proof of its criteria: …"). R-OCI-014 and R-OCI-030
have criteria that run real clients, so no test of the integration tier carries those IDs; they come with the e2e rows. `go run ./internal/tools/coverage -check` reads the names.

## The protocol tests and the conformance suite

[test/integration/oci](../../test/integration/oci) holds the data port's tests ([store_test.go](../../test/integration/oci/store_test.go)) and, built on real listeners, the protocol criteria of
R-OCI-035. Why this area and not `ocilocal` or a new one: [Q-OCI-214](../spec/formats/oci.md#q-oci-214--the-protocol-criteria-run-in-testintegrationoci-against-replicas-of-the-whole-application).

| File | What it pins |
|---|---|
| [proto_env_test.go](../../test/integration/oci/proto_env_test.go) | `newProto`: one installation (PostgreSQL, Valkey with Sentinel, S3), the repositories `oci-local`, `oci-other`, `oci-secret`, `oci-owned` (ownership enforced) and the virtual `oci-virtual` (member and deployment target `oci-local`), a person per grant (`reader` read; `pusher` read and deploy; `deployonly`; `mover` with overwrite; `deleter`; `nobody`), N replicas started with `app.New`; `proto.do` sends a request to the replica the test names, `proto.next` is the scatter dispatcher; `registryToken` and `grantOf` read the token service and the grant a token carries. People a test needs are made in `protoOptions.setup`, before the replicas start (a policy created later is not in their caches) |
| [protocol_test.go](../../test/integration/oci/protocol_test.go), [paths_test.go](../../test/integration/oci/paths_test.go) | `TestProtocol`: one subtest per criterion, named with the requirement it proves (`R-OCI-011_a_chunked_upload_over_three_replicas_and_the_416_form`): names, tags and digests that are none; the version check and the paths that reach no core route; the token service, the direct credential, the pull-only token on an upload; overwrite and delete; one object for a tag and its manifest; pull headers and the Docker media type; ranges; the three-chunk upload over three replicas; the mount by the source's grant; push refusals, artifacts and concurrent tags; an index and its platforms; a push by digest; a tag push against a digest push; tags paging; concurrent referrers with the filter; the catalog; the delete of an index; crafted manifests with the heap sampled; a withheld manifest; the path patterns on the protocol routes |
| [actionmap_test.go](../../test/integration/oci/actionmap_test.go) | `TestR_AUTHZ_007_OCIActionMap`: a subtest per row of the OCI table of R-AUTHZ-007 - the caller with the action is served, the caller without it is refused, the token service grants the row's token action exactly to the first, and a name owned by another owner is refused for the rows that mark ownership |
| [valkey_test.go](../../test/integration/oci/valkey_test.go) | `TestRegistryWorksWithValkeyStopped`: token service, chunked and monolithic push, pulls, tags list and catalog through a local and a virtual repository on three replicas whose Sentinel answers nothing. "Stopped" is configuration (`testenv.StackOptions.Valkey` false), so the stack's Valkey is not touched and no failover lock is needed |
| [conformance_run_test.go](../../test/integration/oci/conformance_run_test.go) | `TestOCIConformance` (build tag `ociconformance`): runs the compiled suite twice, reads the JUnit, fails on a failed or an undocumented skipped specification |

The conformance suite is run by `make oci-conformance` ([build-and-test.md](build-and-test.md#targets)):

1. `oci-conformance-build` resolves `OCI_CONFORMANCE_TAG` (the pin, `v1.1.1`) to its commit with `git ls-remote`, downloads
   `github.com/opencontainers/distribution-spec/conformance` at that commit into `.out/oci-conformance/mod` (a module cache of its own) and compiles its test package with the repository's Go
   (`GOTOOLCHAIN=local`) to `.out/oci-conformance/conformance.test` ([Q-OCI-210](../spec/formats/oci.md#q-oci-210--the-conformance-suite-is-fetched-at-the-commit-its-tag-points-at)).
2. `TestOCIConformance` starts one replica whose base URL and listen address are one loopback address (the suite follows the token realm), creates the person `conformance` (read, deploy,
   overwrite and delete on the three repositories) and runs the binary with the variables of R-OCI-035 in `.out/oci-conformance/local` (namespace `it/oci-local/conformance`) and `.out/oci-conformance/virtual`
   (`it/oci-virtual/conformance`). Each directory holds `junit.xml`, `report.html` and `output.txt`.
3. The virtual run switches content management off: a delete through a virtual repository is refused by design ([Q-OCI-211](../spec/formats/oci.md#q-oci-211--the-conformance-suite-runs-all-four-categories-through-the-local-address-and-pull-push-and-discovery-through-the-virtual-address),
   gap [H-755](../security/oci-virtual.md#what-this-does-not-cover)). The skips the runner accepts are the ones that decision lists, nothing else.

CI runs it in shard b of the `integration` job after `make test-integration` and uploads the reports as the artifact `oci-conformance-junit`. The first run found that the challenge of a mount did not
name the source ([Q-OCI-212](../spec/formats/oci.md#q-oci-212--the-challenge-of-a-mount-names-the-sources-pull-as-well-as-the-targets-push)); to run one specification or a category while working, set the
`OCI_TEST_*` variables by hand and run `.out/oci-conformance/conformance.test` against a replica of your own.

## What is built and what is not

Built: the contract additions, the three migrations, the configuration keys, the key grammar, the settings schema and the `oci` member of the
repository document, the descriptor, the grammars, the layout, the error shape, the route table, the data-port interface and its empty
implementation, the registration of the format and its error codes (`oci_*`, [codes.go](../../internal/errs/codes.go)).

The mark table `oci_tombstones` (manifests the journal replay withheld), the serve-facts kind `oci_local` ([servecheck.go](../../internal/formats/oci/servecheck.go): the item lookup returns the manifest row and the mark in its one statement, `ServeCheck` refuses a withheld manifest) and the audit events `oci.manifest.deleted` and `oci.reinstate` ([events_oci.go](../../internal/audit/events_oci.go)) exist, and so do their users: the tags list, the referrers API, the deletion of tags, manifests and blobs with the hook that cascades, the sweep job `oci-sweep`, the replay that withholds (never deletes), `trove admin oci reinstate`, the setup snippets and the guide, and the tag order. The refusal of a push of a withheld digest belongs to the manifest push.

Built since (WP4a): the manifest parser, the pulls of manifests and blobs, the push of manifests by tag and by digest with the hook and the rows,
the withheld-digest refusal, the package view and the tag order.

Built: the manifest parser, the pulls and pushes and the package view (WP4a); the tags list, referrers, deletes, the sweep, the replay, reinstate,
the setup snippets and the guide (WP4b); the registry token and its token service (R-OCI-004 to R-OCI-006, R-OCI-034 for the token service); the port of
the upload sessions (R-STOR-011) and the upload handlers and the mount that call it (WP5, [ADR-175](../decisions.md#adr-175--the-upload-and-mount-handlers-hold-the-protocol-and-the-host-holds-the-session-and-the-link)).

Built by the registry root's work package: the mount of `/v2/` (one pattern, the dispatcher's handler, the enforcement point, the route
table's checks), the version check, the catalog over the data port's `CatalogNames`, the bound host and the 404 shapes (R-OCI-001 mount,
R-OCI-003, R-OCI-017, R-OCI-029).

Built (WP8): the remote class (R-OCI-021 to R-OCI-025): the adapter, the handshake, the revalidation, the pass-through of the tags list window and of referrers.

Built (WP10): the mirror ([the mirror](#the-mirror-r-oci-026)).

Built (WP9): the virtual class ([the virtual class](#the-virtual-class-r-oci-027-r-oci-028)).

Built (WP13): the conformance suite and the protocol tests of [The protocol tests and the conformance suite](#the-protocol-tests-and-the-conformance-suite).

Built (WP11): the e2e rows with Docker and Podman on kind ([e2e.md](e2e.md#the-oci-clients)).

Not built here (each is a work package of T-16): the package screens' remaining parts.

## The counting stub registry

[test/ocistub](../../test/ocistub) is the upstream of the OCI tests (R-OCI-035): the remote and virtual criteria of R-OCI-021 to R-OCI-025, the
mirror criteria of [Mirror discovery](../spec/formats/oci.md#mirror-discovery) and the e2e sets read what it counted. Like the npm and NuGet stubs it
is a Go package (used in-process), a container and a pod of the dev cluster, all the same code; it reaches no network and answers TLS only. The
image set is generated in code (no recorded response of a public registry), with no clock and no random source, so two starts, and two builds of the
set, produce the same digests (`TestCorpusIsDeterministic`; `TestCorpusPinnedDigests` pins three of them, and the change that
moves the generator on purpose updates the pins). It serves reads only: a write answers 405 `UNSUPPORTED`.

| File | Responsibility |
|---|---|
| [stub.go](../../test/ocistub/stub.go) | `Config`, `Server`: state under one mutex (counters, mode, a copy of the corpus), the three hosts (`derive`), `MoveTag`, `SetTag`, `DeleteTag`, `RevokeTokens`, `Image`, `SetMode`, `Reset`, `ResetAll` |
| [serve.go](../../test/ocistub/serve.go) | the request path: count, host check, gate, authorize, dispatch; tags and catalog pages, manifests (`negotiate` applies the `Accept` rules of distribution), blobs and their redirect, the CDN, referrers |
| [auth.go](../../test/ocistub/auth.go) | the challenge, the realm (`/token`), the signed token (scopes, expiry, revocation epoch), the check of the registry |
| [corpus.go](../../test/ocistub/corpus.go) | the image set (`Default`): layers, configurations, manifests, indexes, attestation manifests, referrers; `recipe`, which a moved tag regenerates |
| [info.go](../../test/ocistub/info.go) | `ImageInfo`: a manifest with the manifests and blobs below it, for a test that needs the digests |
| [route.go](../../test/ocistub/route.go) | path to kind, repository name and reference (the name has slashes: the operation is read from the end) |
| [mode.go](../../test/ocistub/mode.go) | the switches (`Mode`) and their classes |
| [throttle.go](../../test/ocistub/throttle.go), [quota.go](../../test/ocistub/quota.go) | the throttling modes of R-REPO-059 with Docker Hub's answer, and the pull quota |
| [counts.go](../../test/ocistub/counts.go) | the counters and their readers |
| [control.go](../../test/ocistub/control.go) | the control API below `/-/stub/` |
| [inflight.go](../../test/ocistub/inflight.go) | the requests being served at once and their maximum since the last reset (R-REPO-055) |
| [tls.go](../../test/ocistub/tls.go) | `Start` (TLS or nothing), a throwaway CA, a certificate reloaded when its files change |
| [cmd/ocistub](../../test/ocistub/cmd/ocistub), [Containerfile](../../test/ocistub/Containerfile) | the binary (`-listen`, `-tls-cert`, `-tls-key` or `-self-signed`, `-public-url`, `-realm-url`, `-cdn-url`, `-control-token-file`, `-service`, `-page-size`) and the image (`make oci-stub-image`) |

### The three hosts

A large public registry spreads one pull over three hosts, and the rules of R-OCI-021 and R-OCI-023 (the realm host, `extraHosts`, credentials that
never go to a redirect target) are about the differences between them. The stub keeps the three apart by the `Host` header and answers 404 to a path on
the wrong one:

| Host | Serves | Reached as |
|---|---|---|
| registry | `/v2/…` | `Config.PublicURL` / `-public-url`, in the cluster `ocistub.<ns>.svc` |
| realm | `/token` | `Config.RealmURL` / `-realm-url`, in the cluster `ocistub-auth.<ns>.svc` |
| blob CDN | `/cdn/<digest>?repo=&sig=` | `Config.CDNURL` / `-cdn-url`, in the cluster `ocistub-cdn.<ns>.svc` |

Without the URLs a request under a loopback name (`127.0.0.1`, `localhost`, `::1` or `*.localhost`) gets the registry `registry.localhost`, the realm
`auth.localhost` and the CDN `cdn.localhost` on the same port (`LoopbackRegistryHost`, `LoopbackRealmHost`, `LoopbackCDNHost`): three names for one
listener, which is how a Go test runs three hosts. The choice follows what trove can name and reach: the `extraHosts` grammar of a remote
(`^[a-z0-9*?.\-_]{1,253}$`) cannot name an IPv6 literal such as `[::1]`, `127.0.0.2` needs an alias on macOS, and `*.localhost` names are not resolved by Go itself (it hands them to the system resolver only when `nsswitch.conf` lists `myhostname`;
otherwise it sends a DNS query, which a public resolver answers `no such host`). So the names are never resolved: `Running.DialOverride()` maps
`registry.localhost:<port>`, `auth.localhost:<port>` and `cdn.localhost:<port>` (port 443 when a configured URL has none) to the listener, for
trove's seam `app.Options.DialOverride` (`outbound.WithDialOverride`), and `Running.DialContext` is the `DialContext` of a test's `http.Transport`.
The TLS server name, the `Host` header and the host rules stay the names. A client that dials the listener
under `127.0.0.1` has the wrong `Host` for the API and gets 404. The test certificate is `NewSelfSigned(LoopbackHosts...)`. Any other `Host` is
used for all three. A blob `GET` or `HEAD` on the registry answers `307` with a signed CDN URL (HMAC over repository and digest), as distribution
does with a storage driver that redirects (the S3 driver presigns `HEAD` too); `Mode.HeadDirect` lets the registry answer the `HEAD` itself and
`Mode.BlobsDirect` serves both from the registry. The CDN never asks for a token, as a signed URL does not, and the counters record the
credential scheme every request carried, so "the CDN host recorded no `Authorization`" is `AuthSchemes(host)` of the CDN host equal to `["none"]`;
the CDN also refuses a request that has one with 400, as an object store does for a signed URL (`Mode.CDNAcceptAuth` lets it through).

### What it serves

| Resource | Notes |
|---|---|
| `GET /v2/` | `{}` with `Docker-Distribution-API-Version`, behind the challenge |
| manifests by tag and by digest, `HEAD` too | `Content-Type` is the stored media type, `Docker-Content-Digest` and a strong `ETag` carry the digest, `If-None-Match` gives 304. The `Accept` rules are those of distribution v3.1.1 (`registry/handlers/manifests.go`): all `Accept` lines are read, split at commas and parsed with `mime.ParseMediaType`; there is no wildcard. An OCI manifest or index is served only to a client that lists its type, else 404 `MANIFEST_UNKNOWN` (so a request with no `Accept` header gets neither); a Docker manifest list requested **by tag** and not accepted is replaced by its `linux/amd64` manifest, and that needs the schema 2 type, else 400 `MANIFEST_INVALID`; nothing is rewritten by digest; a Docker schema 2 manifest is served to everyone |
| blobs | only those the repository holds (a layer shared by two repositories is held by both); `Range` works |
| `GET /v2/<name>/tags/list` | sorted by bytes, `n` and `last`, `Link: <…>; rel="next"` (relative, as distribution writes it). Every page is capped to `Config.PageSize` (default 700) whatever `n` a client asks, so `bulk/tags` comes in four pages: 700, 700, 700, 400; `n=0` is an empty list; a bad `n` is 400 `PAGINATION_NUMBER_INVALID` |
| `GET /v2/_catalog` | only with `Mode.Catalog` (404 otherwise, as distribution does when its catalog is disabled); the same paging; its scope is `registry:catalog:*` |
| `GET /v2/<name>/referrers/<digest>` | only with `Mode.Referrers` (404 otherwise); an image index of the referrers with `artifactType` and annotations, `?artifactType=` filters and sets `OCI-Filters-Applied`; a digest with no referrers is an empty index. The tag schema fallback (`sha256-<hex>`) is always there for `acme/signed:1.0` and answers the same bytes |

The set (`Default`; see the comment there for the exact shapes):

| Repository | Tags | Shape |
|---|---|---|
| `library/busybox` | `latest`, `1.36`; `1.35` | Docker manifest list (`linux/amd64`, `linux/arm64/v8`); Docker manifest, `linux/amd64` |
| `library/alpine` | `3`, `3.20`, `latest`; `3.19` | OCI index of both platforms and an attestation manifest of each (BuildKit's shape: platform `unknown/unknown`, `vnd.docker.reference.type: attestation-manifest`, `vnd.docker.reference.digest` naming the child, one `application/vnd.in-toto+json` layer); OCI manifest |
| `library/nginx` | `1`, `1.27`, `latest` | Docker manifest list |
| `acme/a` | `1.0`, `1.1`, `1.2`, `2.0`, `latest` | OCI manifests, five distinct images (the tags of the mirror criteria) |
| `acme/b` | `1.0`, `latest` | OCI indexes with both platforms and two attestation manifests (the `platforms` criterion) |
| `other/c` | `1.0` | OCI manifest (the repository a mirror with `acme/*` must never ask for) |
| `app`, `library/app` | `1` | one OCI image under both names: `app:1` as a local repository holds it and `library/app:1` as a Docker Hub mode remote asks for it (R-OCI-025, R-OCI-027) |
| `acme/tool` | `1` | OCI manifest that only the stub has (the claim criterion of R-OCI-027) |

The arm64 platform of every index is `linux/arm64/v8`, as Docker Hub writes it; a criterion that filters on `linux/arm64` meets the variant.
| `acme/signed` | `1.0`, `sha256-<hex of 1.0>` | OCI manifest with a signature and an SBOM as referrers |
| `bulk/tags` | `latest`, `t0001` to `t2499` | 2,500 tags on ten images |

Layers are real gzip-compressed tar archives (stored, not deflated, so the bytes do not depend on the compressor of a Go release) and each
configuration's `diff_ids` are the digests of the uncompressed tars, so `docker`, `podman` and `containerd` can pull and unpack them. They do not run: a layer holds one data file and no `/bin/sh`. Every image of a platform
shares one base layer (8 KiB of content, 9,753 bytes as a layer) and has a layer of its own (4 KiB of content, 5,657 bytes); a single-platform image is about 16 KiB with its configuration and manifest, so a `maxBytes` of a few KiB is below it. A moved tag (`MoveTag`) is a new revision of the same image
(other layers, so other digests); the earlier manifest stays and is served by its digest, as in a registry.

### The handshake

`Mode.Auth` is `anonymous` (the default), `basic` or `none`. In the first two every `/v2/` request without a valid token is answered 401 with
`WWW-Authenticate: Bearer realm="<realm>/token",service="<service>",scope="repository:<name>:pull"` (no scope for `/v2/`, `registry:catalog:*` for
the catalog; `error="invalid_token"` or `"insufficient_scope"` when a token was sent). The realm issues a signed token to a client with no
credentials in `anonymous`, requires `Config.Username`/`Config.Password` (defaults `hub-user`/`hub-pass`) as Basic in `basic`, and in both refuses wrong
credentials with 401. It grants only the `pull` action whatever is asked, drops the scopes of `Mode.PrivateRepos` for a client with no credentials (a
token without access, as Docker Hub issues), and answers a request for another `service` 400. The token carries the scopes, the expiry (`expires_in`,
`Mode.TokenTTLSeconds`, default 300; `Mode.TokenOmitExpiry` leaves it out) and the revocation epoch; `RevokeTokens` makes every token issued so far
invalid and `Mode.StaticToken` is a bearer token accepted for everything with no handshake (`authMode: bearer`).

### Counting

Every request except the control API is counted by method, `Host`, path as sent, query, conditionality and credential scheme (never the credential),
with the repository and reference it addressed (for the CDN, the repository the signed URL names) and the number of answers by status. A request a
switch refuses is counted too; one the stub cut counts under status `0`. The readers on `Counts`:

| Reader | Use |
|---|---|
| `ManifestGets(name, ref)`, `ManifestHeads(name, ref)` | "one `GET` of the tag manifest", "one `HEAD` and no `GET`" (R-OCI-022); an empty `ref` sums the references. A request answered with a 401 challenge is not counted (the handshake sends a request twice), so a correct client has one; `OfKind` and `WithStatus` give the raw numbers |
| `ManifestEntries(name, ref, method)` | the counter entries with the `Accept` media types (`Entry.Accept`, sorted, no `q`) each request carried: the proof of which types a client asked for |
| `BlobFetches(digest)` | `GET` requests for a blob that delivered bytes (200 or 206), on the registry or the CDN host; the redirect is no fetch ("each layer once", R-OCI-023) |
| `BlobRequests(name, digest, method)`, `CDNRequests(digest)` | the requests of one host kind |
| `TokenRequests(scheme)` | requests the realm saw, by credential scheme ("the realm records `Basic`", "a second pull sends no token request") |
| `AuthSchemes(host)`, `OnHost(host)`, `Hosts()` | which credential schemes reached which host |
| `TagLists(name)`, `Catalogs()`, `ReferrersRequests(name)`, `RepositoryRequests(name)`, `OfKind(kind, method)`, `Requests(method, path)`, `WithStatus(code)` | the rest |
| `Violations(maxManifests, maxBlobs)` | no manifest `GET` more than `maxManifests` times per repository and reference, no blob fetched more than `maxBlobs` times |

### Switches

`Mode` replaces the previous mode as a whole (`{}` clears it) and starts the pull quota's count afresh. A "class" is one of `base`, `catalog`, `tags`,
`manifest`, `blob` (registry host), `referrers`, `token`, `cdn`, `other` and, in the delay and failure maps, `all` (added to the class's own). Entries
of `schema1`, `wrong_digest` and `wrong_blob` are `name` (all of the repository), `name:tag` or `name@sha256:…`.

| Field | Effect |
|---|---|
| `down` | `503`, `reset` (connection cut) or `hang` (held until the client gives up, at most two minutes), on all three hosts. Stopping the listener itself is `Running.Close` |
| `delay_ms` | by class: `{"manifest": 2000}` |
| `fail_status` | by class, a 5xx status instead of the content: `{"blob": 502}` |
| `auth`, `private_repos`, `static_token`, `token_ttl_seconds`, `token_omit_expiry` | the handshake, above |
| `refuse_token` | the realm answers this status (400 to 599) instead of a token |
| `blobs_direct`, `head_direct`, `cdn_accept_auth` | blobs from the registry host; only the `HEAD` from the registry host; the CDN serves a request that has credentials (it refuses one by default) |
| `catalog`, `referrers` | the two optional endpoints (off by default) |
| `not_found` | repositories that answer 404 `NAME_UNKNOWN` |
| `schema1` | tags that answer a schema 1 manifest (`application/vnd.docker.distribution.manifest.v1+prettyjws`) |
| `wrong_digest` | manifests whose bytes are not the bytes of their `Docker-Content-Digest` (the stub appends a newline) |
| `wrong_blob` | blobs whose bytes are not the bytes of their digest, on the registry and the CDN |
| `pull_quota` | Docker Hub's pull limit: only manifest `GET` requests count, every manifest answer carries `RateLimit-Limit: <n>;w=<s>` and `RateLimit-Remaining: <n>;w=<s>`, a `GET` over the limit is 429 `TOOMANYREQUESTS` with `0;w=<s>` and no `Retry-After`; defaults 100 per 21,600 s. The window starts with the first counted request |

The throttle is a switch of its own (`SetThrottle`, `/-/stub/throttle`), with the shape of the other stubs: the modes `429` and `429-date` (with
`Retry-After`), `503`, `503-slowdown` (the S3 body, no `Retry-After`), `403-quota` and `429-quota` (`X-RateLimit-Remaining: 0`), each for the n-th request
(`nth`, with `count` in a row) or for a period (`for_ms`) counted from the moment it is set, and a delay. Two additions: `429-hub` answers as Docker Hub
does (R-OCI-025: `RateLimit-Limit: 100;w=21600`, `RateLimit-Remaining: 0;w=21600`, `TOOMANYREQUESTS`, no `Retry-After`), (it never throttles or counts a `HEAD`, as Docker Hub counts `GET`) and `class` narrows the throttle
to one class, so that `{"mode": "429-hub", "class": "manifest"}` throttles manifests only and counts only them.

### Control API

Below `/-/stub/` on any host, with `Authorization: Bearer <token>` (`Config.ControlToken`; without it the API does not exist). It is not counted.

| Request | Effect |
|---|---|
| `GET counts`, `GET inflight` | the counters (`Counts` as JSON); the requests open now and the most open at once since the last reset |
| `POST reset[?all=true]` | zero the counters; with `all` also the mode, the throttle, the quota, the revocation and the changes to the set |
| `GET`/`POST mode`, `GET`/`POST throttle`, `GET quota` | the switches (a body that names an unknown field is 400); the quota's use |
| `GET repositories`, `GET tags?name=`, `GET image?name=&reference=` | the set as it is now: names, tags with digests, and a manifest with the manifests and blobs below it (`ImageInfo`), so that an e2e test can name the digests a pull must have fetched |
| `POST move-tag` `{"name","tag"}` | the tag points at a new revision of its image; answers `{"digest"}` |
| `POST set-tag` `{"name","tag","digest"}`, `POST delete-tag` `{"name","tag"}` | point a tag at a manifest the repository holds (to move it back); remove a tag, the manifest stays |
| `POST revoke-tokens` | every token issued so far is invalid; the next request is challenged again |

The same operations are Go methods for in-process tests (`Counts`, `SetMode`, `SetThrottle`, `MoveTag`, `SetTag`, `DeleteTag`, `RevokeTokens`, `Image`,
`Digest`, `Tags`, `Repositories`, `Quota`, `Inflight`). An in-process test starts it like this:

```go
stub, _ := ocistub.New(ocistub.Config{})
ss, _ := ocistub.NewSelfSigned(ocistub.LoopbackHosts...)
run, _ := stub.Start("127.0.0.1:0", ss.ServerConfig())
defer run.Close()
// run.URL (https://registry.localhost:<port>), run.RealmURL (auth.localhost), run.CDNURL (cdn.localhost); ss.CAPEM is the CA to trust
```

### In the cluster

[deploy/dev/ocistub.yaml](../../deploy/dev/ocistub.yaml): the Services `ocistub` (registry), `ocistub-auth` (realm) and `ocistub-cdn` (blob CDN) on 443
in front of one pod (the counters are in memory), a certificate of the dev CA for the three names, the control token in Secret `trove-ocistub` (key
`control-token`), TCP probes (an HTTP probe would be counted). The remote repository under test names `ocistub-auth.<ns>.svc` in `extraHosts` (with
`sendCredentials` when it pulls with credentials) and `ocistub-cdn.<ns>.svc` without. `make oci-stub-image` builds the image; loading it into kind and applying the manifest is
not wired into `deploy/dev/e2e.sh` yet (the e2e work package adds the marker and the `TROVE_E2E_OCISTUB` switch, as the other stubs have).

### Not covered by the stub

It serves no writes (no push, no `DELETE`), no foreign layers (`urls`), and keeps no per-client accounting: the pull quota is one counter for the
whole stub, not one per address or account, and a quota answer carries no `Retry-After`, which is the point of the case. The signed CDN URL has no
expiry. Whether a client asks the realm for the scope of the repository it is about to read is checked only through the result: a token that does not
cover the repository is answered 401 `insufficient_scope`. The stub is not a conformance reference: the conformance suite of R-OCI-035 runs against
trove, not against the stub.

## What this page does not cover

Referrers (signatures, SBOMs) are not mirrored; they stay pull-through (R-OCI-024). The children of an index inside an index are not read. The e2e rows with real clients on several replicas
are the e2e work package's.
