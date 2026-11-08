# OCI and Docker registry

The OCI format (`internal/formats/oci`) serves container images and other OCI content. This page describes the part that is built, the
skeleton of phase T-16 ("WP0, contracts"): the contract additions, the schema, the configuration keys and the format that registers and
answers `501` until the work packages fill it, and the operations built on it: the manifest parser, the pulls of manifests and blobs and the push of
manifests (WP4a, [ADR-164](../decisions.md#adr-164--the-oci-push-path-is-one-transaction-with-its-reference-check-under-row-locks-and-the-formats-data-port-reads-items)),
the tags list, referrers, deletion, sweep and replay (WP4b, ADR-168). The specification is [formats/oci.md](../spec/formats/oci.md); the decision for the contract is
[ADR-161](../decisions.md#adr-161--the-format-contract-gains-the-registry-root-the-upstream-handshake-head-revalidation-and-upload-sessions).
The registry tokens and the token service ([Registry tokens and the token service](#registry-tokens-and-the-token-service)) and the port of the
[Upload sessions](#upload-sessions-r-stor-011) are built; the mount of `/v2/`, the upload routes, the remote and virtual classes, the mirror and the clients are not; the section
[What is built and what is not](#what-is-built-and-what-is-not) lists them.

## What has to be in your head first

- **One format, `oci`, for Docker and OCI content** (Q-OCI-1). The address is the root of a host: `/v2/<projectKey>/<repoKey>/<name>/…`.
  The format declares `catalog.Facts.RegistryRoot`, so its `ProtocolBasePath` is `catalog.RegistryRootBasePath`
  (`/v2/{project}/{key}/`), and at most one format may declare it. The three repository-less routes below `/v2/` (the version check, the
  catalog, the token endpoint) belong to the core, not to the format (Q-OCI-4).
- **The dispatcher does not mount a registry-root format.** `dispatch.New` skips it, so `/api/oci/…` answers nothing; the mount of
  `/v2/` is a work package of its own, and until it exists the format's routes are reached by the conformance kit only.
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
| [paths.go](../../internal/formats/oci/paths.go) | The storage layout (`ManifestPath`, `TagPath`, `TaglistPath`, `BlobPath`), `Classify` and `PackageDocumentPath` |
| [routes.go](../../internal/formats/oci/routes.go) | The route table of R-OCI-009 to R-OCI-018 with its matchers (the image name has slashes, so the operation is read from the end of the path) and `stub`, the handler of an operation not built (the stub files `handlers_wp5_stub.go` hold them); a work package replaces its stub file and does not edit the table |
| [errors.go](../../internal/formats/oci/errors.go) | `WriteError`: the distribution specification's error list `{"errors":[{"code":…}]}` for every row of R-OCI-008; a conflict is `409 DENIED` |
| [handlers_manifest.go](../../internal/formats/oci/handlers_manifest.go) | The pull of a manifest and the push by tag or by digest: the order of the checks, the tag decision (`commitTag`), the reference check (`checkReferences`), the validator translation |
| [handlers_blob.go](../../internal/formats/oci/handlers_blob.go) | The pull of a blob of a name (local class) |
| [hooks.go](../../internal/formats/oci/hooks.go) | `OnCommit`: the package row, the claim, the version of a tag and the manifest rows, in the commit transaction |
| [view.go](../../internal/formats/oci/view.go) | The package view adapter: tags newest push first, the platform table of an index, the withheld tags |
| [handlers_wp5_stub.go](../../internal/formats/oci/handlers_wp5_stub.go) | The `501` handlers of the operations not built yet (uploads and mount); the work package that builds one deletes its file |
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
virtual classes answer `501` through the stub files until their work packages replace them.

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
| [handlers_tags.go](../../internal/formats/oci/handlers_tags.go), [handlers_referrers.go](../../internal/formats/oci/handlers_referrers.go) | The tags list (`n`, `last`, the `Link`, 404 for a name that holds nothing) and the referrers API (the index answer, the `artifactType` filter, paging by the last digest, 4 MiB per page) | The tags list is in byte order (R-OCI-015) and does not use `tagorder`, whose rank order is the package view's and the mirror's (Q-OCI-64).
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
| [api/registry/enforce.go](../../internal/api/registry/enforce.go) | `Enforcer` (`NewEnforcer`, `Mount`, `Routes`, `Patterns`, `Target`, `Check`, `Also`), `Route`, `RouteKind`, `RepositoryRoutes`: the enforcement point of Q-OCI-56 |
| [api/registry/route/route.go](../../internal/api/registry/route/route.go) | The leaf the dispatcher and the registry root share: `Target`, `Matcher`, the core route names `NameVersion`, `NameCatalog`, `NameToken`; standard library and `qname` only, so the dispatcher depends on neither the registry root nor the authentication stage (archtest `moduleBans`, transitive) |
| [api/registry/registrytest/walk.go](../../internal/api/registry/registrytest/walk.go) | `Walk.Run`: the proof through the endpoint's whole handler, with minted registry tokens |
| [api/dispatch/registry_target.go](../../internal/api/dispatch/registry_target.go) | `Dispatcher.RegistryTarget`: the dispatcher's one match below `/v2/` (core routes, the format's `Match`, the cache view, the bound host) |
| [app.go](../../internal/app/app.go), [wire_endpoints.go](../../internal/app/wire_endpoints.go) | `endpointRuntime.registry`; the route-table check admits exactly its patterns below `/v2/` (the second list of `CheckRouteTable`; the third, the formats' host-root patterns below `/.well-known/`, is empty until a format registers a discovery document) |
| [events_registry_token.go](../../internal/audit/events_registry_token.go), [dedup.go](../../internal/audit/dedup.go) | The event and its deduplication per client address, endpoint, reason and credential |
| [m1_convert.go](../../internal/app/m1_convert.go) | `registryGrant`: `auth.Credential.Registry` to `httpserver.Credential.Registry` (`RegistryGrant.Allows`), for anonymous registry tokens too |
| [wire_identity.go](../../internal/app/wire_identity.go) | `wireAuth` builds the `Signer` from the key ring and `oci.token_lifetime` and gives it and `registryRealm` to the stage; `authArea.registry` holds the signer for the token service; `credentialRules(rt)` are the endpoint's rules for the chain's stage and the token service |

**For the mount of `/v2/` (the registry root's work package).** The token service and the enforcement point are built without a route.
Mounting them needs:

1. **The enforcement point.** One `registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: ...})` per endpoint, with
   the format's `WriteError` and the dispatcher itself as the `Matcher`: `dispatch.Dispatcher.RegistryTarget` is the dispatcher's own
   match, which its `/v2/` handler must route by too (one match, as `APIKeyRoute` shares the dispatcher's match with the stage). Set
   `rt.registry` to it: the start-up route-table check of `startClient` admits exactly `rt.registry.Patterns()` below `/v2/` and none
   while it is nil. Mount every handler below `/v2/` with `enforcer.Mount(router, pattern, handler, routes...)` - never with
   `router.Handle` - in one call per pattern: the repository routes with `registry.RepositoryRoutes(format.Routes(),
   "/v2/web/docker-local/")` (every route of the format, its action and its first example), the version check as
   `Route{Name: route.NameVersion, Kind: registry.RouteVersion, Method: "GET", Example: "/v2/"}` (exactly one, `GET` or `HEAD`
   of `/v2/`), the catalog as `route.NameCatalog`/`RouteCatalog`, the token service as `route.NameToken`/`RouteToken`. The mount's handler
   calls `enforcer.Also(r, source, registrytoken.Pull)` for the source of a cross-repository mount (each action is checked on its own;
   a source key with `~cache` is its base repository).
2. **The walk.** The mount's test calls `registrytest.Walk{Enforcer, Handler, Patterns, FormatRoutes, Mint, Prepare}.Run(t)` with the
   endpoint's whole handler (the chain, so the stage runs), `router.Patterns()`, the names of every route of the format, and a `Mint`
   that signs a registry token with the auth area's signer for a live trove token of the test (its ID, type and the SHA-256 of its
   secret). It fails for a route of the format that is not mounted, a `/v2/` pattern around the enforcer, an example the dispatcher matches
   to another route, a route an empty grant passes, and a route that a grant of every other action passes or a grant of exactly its
   action does not.
3. **The token service.** `registry.NewToken(registry.TokenOptions{...})` per endpoint, with `Signer: part[authArea](a.parts).registry`
   (the one source of the lifetime, `oci.token_lifetime`), the authz engine as `Decider`, the repository reader, `Format: "oci"`,
   `ValidName: oci.ValidName`, one `registrytoken.NewMetrics(a.tele.metrics.Registry())` shared by every endpoint, the audit recorder, the
   format's `WriteError`, and `Authenticate`. `authArea.stage` is the `auth.Authenticator` interface, so `Authenticate` needs the concrete
   `*auth.Stage` (a type assertion on `a.authStage().stage`, or a narrow interface with `AuthenticateTokenRequest`). Build it like
   `credentialStage`: `a.credentialRules(rt)`, `principalFromResult`, wrapped in `withMinEpoch`, and `recordPrincipal`, so the request log
   names the caller the token service authenticated.
4. **`Bound`** of the token service and the bound host in `RegistryTarget`: the bound host's repository only when it is of the format
   (R-EP-006: a binding to another repository behaves as no binding); both read `RequestInfo.RootRepository` as it is today.
5. **`Options.RegistryScope`** of the stage: the scope entry of the request's route from the dispatcher's match, as `APIKeyRoute` is.
6. **The version check** answers 401 with the challenge when `p.Anonymous && p.Credential.Registry == nil` (R-OCI-003, Q-OCI-20): the
   stage passes a request without a credential as anonymous where anonymous requests are allowed, so the handler builds that challenge
   itself (`registrytoken.Challenge(realm, service, "", "")` with `registrytoken.Realm` of the base URL).
7. **Rate limits.** The chain sees every token request as anonymous, so a rate-limit class keyed by principal counts token requests in
   the bucket of the client address (Q-OCI-51).

**The upload-session contract (WP1, WP3, WP5).** The enforcement point checks a registry token's grant against the image name of the
path. An upload session is continued (`PATCH`), finished (`PUT`), queried (`GET`) and cancelled (`DELETE`) by its ID under that path, so
the grant of `push` on image A must never reach a session of image B: every call of `formats.UploadSessions` (`Get`, `Append`,
`Finalize`, `Abort`) passes the `SessionRef` with the binding the format names (the image name of the path) and the addressed repository,
and the host answers 404 `BLOB_UPLOAD_UNKNOWN` unless the session belongs to that repository, that image name and the request's principal
(R-STOR-011, Q-OCI-26). A session handler that reads the session by its ID alone, or takes the image name from the session instead of the
path, lets a registry token push into an image its grant does not name.

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

How a format's handler uses the port (the handlers are the work package of the routes, WP5):

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
| [oci_host_test.go](../../test/integration/files/oci_host_test.go) | The routes on the production host (real dispatcher, item store, hooks, S3), mounted at `/api/oci/` through a wrapper because `/v2/` is not mounted yet |
| `TestR_OCI_015_…`, `TestR_OCI_016_…`, `TestR_OCI_018_…`, `TestR_OCI_030_…` ([handlers_lists_test.go](../../internal/formats/oci/handlers_lists_test.go), [handlers_delete_test.go](../../internal/formats/oci/handlers_delete_test.go), [hooks_remove_test.go](../../internal/formats/oci/hooks_remove_test.go), [setup_test.go](../../internal/formats/oci/setup_test.go)) | The lists, the deletion routes, the cascade and the refusals of the hook against an in-memory twin of the data port ([store_twin_test.go](../../internal/formats/oci/store_twin_test.go)), and the setup snippets |
| [test/integration/ocilocal](../../test/integration/ocilocal) | Against the stack: 2,500 tags in pages, the referrers, a raw delete of an index through a running replica (cascade, events, journal entries), the sweep (grace, sessions, the lock against a push, the job on a worker), the replay and the reinstate. The registry routes are not mounted yet, so the state is what a push leaves: items committed through the item store and the rows by SQL |
| [oci_test.go](../../test/integration/postgres/oci_test.go) | The tables, their export classes, the phases of the three migrations and the key grammar at the database (`make test-integration`) |
| [registrytoken/*_test.go](../../internal/auth/registrytoken) | `TestRegistryTokenForm` (round trip, every character flipped, expiry, another endpoint, another key, unknown key, malformed values, the scanner pattern), `TestRegistryTokenClaimsRules` (with the configured lifetime and the issue time ahead), `TestR_ARCH_021_RegistryTokenKeyRingRotation`, `TestScopeGrammar` (with the cache view), `TestChallenge` (with no base URL), `TestGrantAllows`, `TestTokenServiceMetrics`, `TestTheLogScrubberHidesARegistryToken` |
| [stage_registry_test.go](../../internal/auth/stage_registry_test.go) | The stage: a registry token below `/v2/` only, as `Bearer` only and not on an escaped path, refusals without a token lookup, a revoked trove token, a claims mismatch (ID, owner, type), the endpoint's token types, bindings and `principal_disabled` (`TestR_EP_011_TheTroveTokensRulesReachItsRegistryToken`), a forged token counted by the throttle (`TestR_AUTH_038_…`), anonymous registry tokens, no session cookie below `/v2/`, the challenge, `AuthenticateTokenRequest`, `Presented` never printing its secret (`TestR_AUTH_001_…`) |
| [api/registry/*_test.go](../../internal/api/registry) | The token service (`TestTokenService`, `TestTokenServiceForm`: `GET` and `POST` with containerd's content type, the grant of read and deploy, the lifetime cap, the trove token that expires within the second, 400 in both shapes, the grant beyond 4 KiB, 405, the catalog, the cache view, a bound host; the `service` check; anonymous with and without anonymous requests; the event with its endpoint column and the metric; the request never printing its credential), `TestGrantIsTheCoarseCheck`, `TestActionOf`, the enforcement point through a whole handler with the real stage, signer, dispatcher and OCI routes (`TestTheEnforcementPointThroughTheWholeHandler` with `Walk`, the route table, the cache view, the own error code; `TestTheWalkCatchesAWrongMount`: a route left out, a matcher that resolves a push to the pull route, the version check off `/v2/`; `TestMountRefusesWhatItCannotEnforce`; `TestAlsoChecksTheMountsSourceActionByAction`); `TestTheRegistryMatchIsTheDispatchers` in [dispatch](../../internal/api/dispatch/registry_target_test.go) |
| [registrytoken_test.go](../../test/integration/identity/registrytoken_test.go) | Against the stack, two replicas with Valkey running and stopped (`TestRegistryTokensAgainstTheRealStores`): a token issued on one works on the other, a forged, tampered, expired or foreign token runs no statement (a tracer on the replica's pools counts the statements of the marked request, with a positive control), a revoked PAT stops its registry token within the bound; `TestAnonymousRegistryTokenAgainstTheRealStores` with and without `auth.anonymous` and after the switch is turned off; `TestR_ARCH_021_RegistryTokenKeyRingRotationAcrossReplicas` |
| [dedup_registry_test.go](../../internal/audit/dedup_registry_test.go) | `auth.registry_token.denied` collapses per address, endpoint, reason and credential |

The skeleton tests of WP0 carry no `R-OCI-…` in their names; the tests of the work packages that build behaviour do ([coverage.md](coverage.md)).
The tests of the token service name their R-OCI requirement in a comment until the registry root names it in the test name once `/v2/` is mounted
and the grant check runs; the R-ARCH, R-AUTH and R-EP requirements they prove are in their names. The upload sessions name R-STOR-011 and R-OCI-032.

## What is built and what is not

Built: the contract additions, the three migrations, the configuration keys, the key grammar, the settings schema and the `oci` member of the
repository document, the descriptor, the grammars, the layout, the error shape, the route table, the data-port interface and its empty
implementation, the registration of the format and its error codes (`oci_*`, [codes.go](../../internal/errs/codes.go)).

The mark table `oci_tombstones` (manifests the journal replay withheld), the serve-facts kind `oci_local` ([servecheck.go](../../internal/formats/oci/servecheck.go): the item lookup returns the manifest row and the mark in its one statement, `ServeCheck` refuses a withheld manifest) and the audit events `oci.manifest.deleted` and `oci.reinstate` ([events_oci.go](../../internal/audit/events_oci.go)) exist, and so do their users: the tags list, the referrers API, the deletion of tags, manifests and blobs with the hook that cascades, the sweep job `oci-sweep`, the replay that withholds (never deletes), `trove admin oci reinstate`, the setup snippets and the guide, and the tag order. The refusal of a push of a withheld digest belongs to the manifest push.

Built since (WP4a): the manifest parser, the pulls of manifests and blobs, the push of manifests by tag and by digest with the hook and the rows,
the withheld-digest refusal, the package view and the tag order.

Built: the manifest parser, the pulls and pushes and the package view (WP4a); the tags list, referrers, deletes, the sweep, the replay, reinstate,
the setup snippets and the guide (WP4b); the registry token and its token service (R-OCI-004 to R-OCI-006, R-OCI-034 for the token service); the port of
the upload sessions (R-STOR-011), which the upload handlers will call.

Not built here (each is a work package of T-16): the mount of `/v2/` and the core routes (R-OCI-001, R-OCI-003, R-OCI-017; the catalog's page per repository
is `Store.CatalogNames`) with the route of the token service and the grant check before the decision point, the upload routes (R-OCI-011, R-OCI-012), the remote
and virtual classes and the mirror. The edits that belong to the mount stay open
with it: the `/v2/` row and the route-table rule of R-ARCH-005 and INV-6 in [spec/README.md](../spec/README.md).

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

The mirror has no code to describe yet; it gets its section here when its work package lands.
