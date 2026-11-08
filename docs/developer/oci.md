# OCI and Docker registry

The OCI format (`internal/formats/oci`) serves container images and other OCI content. This page describes the part that is built, the
skeleton of phase T-16 ("WP0, contracts"): the contract additions, the schema, the configuration keys and the format that registers and
answers `501` until the work packages fill it. The specification is [formats/oci.md](../spec/formats/oci.md); the decision for the
contract is [ADR-161](../decisions.md#adr-161--the-format-contract-gains-the-registry-root-the-upstream-handshake-head-revalidation-and-upload-sessions).
The routes, the token service, the uploads, the remote and virtual classes, the mirror and the clients are not built; the section
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
| [routes.go](../../internal/formats/oci/routes.go) | The route table of R-OCI-009 to R-OCI-018 with its matchers (the image name has slashes, so the operation is read from the end of the path) and the stub handlers that answer `501`; a work package moves its handler out of this file and does not edit the table |
| [errors.go](../../internal/formats/oci/errors.go) | `WriteError`: the distribution specification's error list `{"errors":[{"code":…}]}` for every row of R-OCI-008 |
| [inspect.go](../../internal/formats/oci/inspect.go) | `Inspect`: a manifest is scanned within the limits (4 MiB, 32 levels, 100,000 values) before anything decodes it; the manifest parser of R-OCI-013 replaces the generic decode |
| [settings.go](../../internal/formats/oci/settings.go), [settings.schema.json](../../internal/formats/oci/settings.schema.json) | The `oci` member of the repository document: `oci.mirror.tags`, `excludeTags`, `platforms` (remote repositories only) |
| [store.go](../../internal/formats/oci/store.go) | `Store`, the data port: manifest rows, references, referrers, tags, catalog and sweep candidates |
| [rows](../../internal/formats/oci/rows/rows.go) | The values that cross the port; a leaf (`internal/formats/<id>/rows` imports only `errs` and the standard library) |
| [../formatdata/oci](../../internal/formatdata/oci/store.go) | The implementation of `Store` (every method answers `not_implemented`) and the registration of the tables (`tables.go`); linked by the composition root ([wire_formats.go](../../internal/app/wire_formats.go)) and by `cmd/trove/registry.go` |
| [oci.sql](../../internal/meta/postgres/queries/oci.sql) | The reads of the port as sqlc statements |

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
| `Authenticator` (`TokenRequest` gets the answered request), `Revalidator`, `RemoteCaps.Auth`, `RemoteCaps.Revalidate` | [remote.go](../../internal/formats/remote.go), found by `CapabilitiesOf` | the engine does not call them yet |
| `RemoteDocument.MediaType`, `Prepared.Aliases` | `remote_cache_entries.media_type` (migration 00055); [metadata.go](../../internal/proxy/metadata.go) (`enterAliases`, in the fill transaction, through `items.Store.LinkIn`) | yes |
| `Host.Uploads()`, `UploadSessions`, `UploadSession`, `SessionRef` | [host.go](../../internal/formats/host.go) | [uploads.go](../../internal/formathost/uploads.go): every call `not_implemented`; the work package of the sessions decides principal, binding and `deploy` on the target on every call |
| `Ingestor.Link(from SourceRef)` | [host.go](../../internal/formats/host.go) | [ingest.go](../../internal/formathost/ingest.go) (`linkSource`: resolves the repository by name, a virtual one through its members; live, served, not blocked, readable), `items.Store.CommitLinked` and `blob.Store.LinkExisting`; the in-memory host implements it |
| `discover.Upstream.Artifact` | [discover.go](../../internal/mirror/discover/discover.go) | [mirror_host.go](../../internal/formathost/mirror_host.go): `not_implemented` |

A push by tag commits the tag item as the lead and the manifest item as its companion (a companion goes with a lead that changed), or links the
tag with `Ingestor.Link` when the manifest is stored; a push by digest has the manifest as the lead (R-OCI-013).

## Configuration

| Key | Default | Rule |
|---|---|---|
| `oci.token_lifetime` | `300s` | 60 s to 3600 s (R-OCI-005) |
| `oci.unreferenced_blob_grace` | `24h` | at least 1 m and above `storage.uploads.session_max_age` (R-OCI-019); `validateOCICoupling` in [oci.go](../../internal/config/oci.go) |
| `storage.uploads.part_size`, `writer_lease`, `session_ttl`, `session_max_age` | `16MiB` (5MiB to 5GiB), `60s`, `4h`, `20h` | exist since the storage section; `session_max_age` must stay below `storage.orphans.multipart_max_age`, and `storage.upload.max_size` must not exceed 10,000 parts of `part_size` (R-STOR-011) |

## Tests

| Test | What it pins |
|---|---|
| `TestR_ARCH_010_Conformance` ([conformance_test.go](../../internal/formats/oci/conformance_test.go)) | The kit over the skeleton; every skip and every route that is not built names its reason, and the kit refuses a skip the format contradicts |
| [format_test.go](../../internal/formats/oci/format_test.go) | The descriptor, the registry root in the catalog, the route matchers, `501` in the native shape, `WriteError` per row of R-OCI-008, `Inspect` on crafted manifests |
| [names_test.go](../../internal/formats/oci/names_test.go) | The grammars against the distribution specification's expressions over every string of a small alphabet, the layout, the `oci.mirror` schema |
| `TestR_ARCH_008_OciSettingsSchemaEqualsTheOpenAPIMember` | The JSON schema and `OciSettings` in `api/openapi.yaml` say the same |
| [oci_test.go](../../test/integration/postgres/oci_test.go) | The tables, their export classes, the phases of the three migrations and the key grammar at the database (`make test-integration`) |

The tests carry no `R-OCI-…` in their names: a requirement counts as implemented when the work package that builds its behaviour names it in a test
([coverage.md](coverage.md)).

## What is built and what is not

Built: the contract additions, the three migrations, the configuration keys, the key grammar, the settings schema and the `oci` member of the
repository document, the descriptor, the grammars, the layout, the error shape, the route table, the data-port interface and its empty
implementation, the registration of the format and its error codes (`oci_*`, [codes.go](../../internal/errs/codes.go)).

The mark table `oci_tombstones` (manifests the journal replay withheld), the serve-facts kind `oci_local` ([servecheck.go](../../internal/formats/oci/servecheck.go): the item lookup returns the manifest row and the mark in its one statement, `ServeCheck` refuses a withheld manifest) and the audit event `oci.manifest.deleted` ([events_oci.go](../../internal/audit/events_oci.go), journal flag,
required entry) exist; the replay handler that uses them, the sweep job `oci-sweep` (in `internal/formatdata/oci`, through the item store and
the job queue as NuGet's index job does), the hook that cascades, the replay that withholds (never deletes), `trove admin oci reinstate` and the refusal of a push of a withheld digest are the work package of the deletion. A restore skips an
`oci.manifest.deleted` entry and reports it until then; nothing emits the event yet.

Not built (each is a work package of T-16): the mount of `/v2/` and the core routes (R-OCI-001, R-OCI-003, R-OCI-017), the token service
(R-OCI-004 to R-OCI-006), the handlers of the routes (R-OCI-009 to R-OCI-018), the upload sessions (R-STOR-011), the remote and
virtual classes, the mirror, the setup snippets and the guide (R-OCI-030), the package view (R-OCI-031). The edits that belong to the mount stay open
with it: the `/v2/` row and the route-table rule of R-ARCH-005 and INV-6 in [spec/README.md](../spec/README.md).

## What this page does not cover

The token service, the upload sessions, the remote engine's handshake and the mirror have no code to describe yet; each gets its section here when
its work package lands.
