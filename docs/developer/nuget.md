# The NuGet format

Status: the package screens, the setup snippets and the guide of the format are written (phase 4, WP3.1h; see "The package view, the setup snippets and the guide" below). The format is registered with its skeleton (phase 4, WP3.1c): the route table of the whole NuGet v3 protocol, the matching
and the classification of paths, the service index, the error shape, the settings and the configuration section, and the data port interface.
The push, the unlist, the relist and the delete are built ([the write side](#the-write-side)). The symbol push is built ([symbol packages](#symbol-packages)). The operations behind the other routes - package content, registrations, search, the symbol server - are written by
the work packages that own them and answer 501 `nuget_not_implemented` until then ([handlers_stub.go](../../internal/formats/nuget/handlers_stub.go)).
The requirements are in [spec/formats/nuget.md](../spec/formats/nuget.md); the contract the format implements is in [formats.md](formats.md).

## The format

Package [internal/formats/nuget](../../internal/formats/nuget), registered by [internal/formats/all](../../internal/formats/all/all.go). It
imports the format contract and the leaf packages below, and reaches its tables only through the `Store` port.

| File | Responsibility |
|---|---|
| [format.go](../../internal/formats/nuget/format.go) | `Format`, `Descriptor` (the catalog facts: the classes `local`, `remote` and `virtual`, Q-NUGET-28, Q-NUGET-56; the upstream, the 300 s and 1800 s periods, `ChecksumRequireUnsupported`, `NotifyAreas`, `DataPort`), `Inspect` (reads a package for the roads around the push route, Q-NUGET-38) |
| [routes.go](../../internal/formats/nuget/routes.go) | The route table of R-NUGET-001, written once; the route names (`nuget.flatcontainer.nupkg`, Q-NUGET-30); `op`, which records `X-NuGet-Session-Id` |
| [match.go](../../internal/formats/nuget/match.go) | The matchers of the protocol paths, `Match` (the 404 `nuget_v2_unsupported`, the normalization of the ID, the version and the file name), the package-name rule `names` |
| [paths.go](../../internal/formats/nuget/paths.go) | The canonical paths (`NupkgPath`, `NuspecPath`, `PackageDocumentPath`), `Classify`, `CanonicalPath` (route to canonical path), the routing name of a symbol key |
| [index.go](../../internal/formats/nuget/index.go) | `ServiceIndex` (the document), `handleIndex`, the weak ETag and `If-None-Match` |
| [errors.go](../../internal/formats/nuget/errors.go) | `WriteError`: the one-line `text/plain` body `<code>: <message>`, the Basic challenge, `Retry-After` capped at 30 s, the message of the v2 404 |
| [settings.go](../../internal/formats/nuget/settings.go), [settings.schema.json](../../internal/formats/nuget/settings.schema.json) | The `nuget` member of a repository document: `deleteMode`, `forwardSearch`; `settingsOf` |
| [bind.go](../../internal/formats/nuget/bind.go) | `Bind`: the `nuget` section of the configuration per Host; `archiveLimits` maps `nuget.archive` onto `nupkg.Limits` |
| [store.go](../../internal/formats/nuget/store.go) | The data port interface `Store`; the block `>>> M3 nuget-read` holds the reads of the document and search packages |
| [view.go](../../internal/formats/nuget/view.go) | The `ViewAdapter` of the package screens: `Compare`, `Describe`, `Find`, `Withheld`, `Stored`, and `viewPort`, the part of the data port it reads (Q-NUGET-48) |
| [setup.go](../../internal/formats/nuget/setup.go) | `clients` (dotnet, nuget, visual-studio) and `SetupTemplates` (Q-NUGET-49) |
| [guide.go](../../internal/formats/nuget/guide.go), [guide/](../../internal/formats/nuget/guide) | `Guide` and its Markdown templates, one file per section (Q-NUGET-50) |
| [exposure.go](../../internal/formats/nuget/exposure.go) | The exposure adapter: packages and versions, no tags; NuGet's range grammar. `hiddenVersion` and `onRulesEndpoint` are the questions the handlers ask (R-NUGET-034, [NuGet on a customer endpoint](#nuget-on-a-customer-endpoint)) |
| [remote.go](../../internal/formats/nuget/remote.go), [remote_fill.go](../../internal/formats/nuget/remote_fill.go), [handlers_remote.go](../../internal/formats/nuget/handlers_remote.go) | The remote class ([below](#the-remote-class)) |
| [handlers_stub.go](../../internal/formats/nuget/handlers_stub.go) | One method per route that is not built; the package that builds it moves the method into a file of its own |
| [nugettest](../../internal/formats/nuget/nugettest/store.go), [view.go](../../internal/formats/nuget/nugettest/view.go) | The in-memory twin of the port, for the conformance kit and unit tests; it records the versions `InsertVersion` is handed and answers the view reads over them |

### Two kinds of path

A *protocol path* is what a client requests below `/api/nuget/<key>/` (`v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg`); the routes match
it. A *canonical path* is where the item lives (`acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg`, R-NUGET-005); `Classify`, `PackageDocumentPath` and the helpers of
`paths.go` speak it, and `CanonicalPath(route, params)` maps one to the other. The two are never mixed, because the package ID `v3` has the canonical path
`v3/index.json`, which is also the service index (Q-NUGET-29). The kit's fixture maps between them (`Fixture.Canonical`, `Fixture.Protocol`, [formats.md](formats.md)).

### The routes

Names are the handler names of R-NUGET-028; paths are relative to `/api/nuget/<key>/`. A write is a `Local` class route with `Route.APIKey`.

| Route | Method and path | Kind and action |
|---|---|---|
| `nuget.index` | GET `v3`, `v3/`, `v3/index.json` | computed, read; built |
| `nuget.flatcontainer.index` | GET `v3/flatcontainer/{id}/index.json` | metadata, read |
| `nuget.flatcontainer.nupkg` | GET `v3/flatcontainer/{id}/{version}/{id}.{version}.nupkg` | download, read |
| `nuget.flatcontainer.nuspec` | GET `v3/flatcontainer/{id}/{version}/{id}.nuspec` | download, read |
| `nuget.flatcontainer.embedded` | GET `v3/flatcontainer/{id}/{version}/{icon,license,readme}` (P1) | download, read |
| `nuget.registration.index`, `.page`, `.leaf` | GET `v3/{registration,registration-gz,registration-semver2}/{id}/index.json`, `…/page/{lower}/{upper}.json`, `…/{version}.json` | metadata, read |
| `nuget.search`, `nuget.autocomplete` (P1) | GET `v3/search`, `v3/autocomplete` | volatile, read |
| `nuget.push` | PUT `v3/package`, `v3/package/` | upload, deploy, API key |
| `nuget.delete` | DELETE `v3/package/{id}/{version}` | delete, delete, API key |
| `nuget.relist` | POST `v3/package/{id}/{version}` | admin, deploy, API key |
| `nuget.symbolpush` | PUT `v3/symbolpackage`, `v3/symbolpackage/` | upload, deploy, API key |
| `nuget.symbols` (P1) | GET `v3/symbols/{key...}` | download, read |

The two publish routes take one trailing slash (`matchPublish`): the .NET SDK sends `PUT <feed>/v3/package/` for the index entry `<feed>/v3/package` ([Q-NUGET-66](../spec/formats/nuget.md#q-nuget-66--the-publish-resources-accept-a-trailing-slash)).
Every other route keeps the strict match, in which a trailing slash is a refusal.

`Match` normalizes what the path names: the ID is validated with the upstream rule and lowercased, the version resolved to its URL form, and a file name must be the ID and
version of the directories above it (`{id}.{version}.nupkg`, `{id}.nuspec`) in any casing and spelling. An ID, version or file that cannot exist answers 404 `not_found`
(a client that probes a source for an ID it does not hold must not fail its restore); a path no route accepts answers 404 `nuget_v2_unsupported` with the absolute URL of the
service index of the addressed repository, from `formats.RefusalFrom`. The routes of the vulnerability information (remote and virtual repositories) are added with the
classes that serve them.

### Adding a handler

1. Move the method of the route out of `handlers_stub.go` into `handlers_<topic>.go` and write it; the route table is not edited.
2. Read the replica's settings with `f.replicaOf(rq.Host)`; read the repository's member with `settingsOf(rq.Repo.Config)`.
3. Map the route to canonical paths with `CanonicalPath`, never by string surgery on `rq.Path`.
4. Answer errors with `errs` codes; `WriteError` renders them. Add a `ContentCase` with the real status to [conformance_test.go](../../internal/formats/nuget/conformance_test.go).
5. Drop the check from `Skip` in the fixture when its subject exists (ingestion: checks 6 and 9; the remote and virtual adapters: 4 and 5). Checks 8 (clients, templates, guide) and 11 (view) run; the fixture's `View.Seed` stores versions through the `PackageWriter` and the twin's `InsertVersion`, which the ingestion replaces by a push.

### Data port and tables

The port is `nuget.Store`; the implementation is [internal/formatdata/nuget](../../internal/formatdata/nuget/store.go) (the statements are in
[nuget_local.sql](../../internal/meta/postgres/queries/nuget_local.sql)); its `Search` answers `nuget_not_implemented` until the search package replaces
[search.go](../../internal/formatdata/nuget/search.go). The package also registers the four tables of migration 00017 with the
backup ([tables.go](../../internal/formatdata/nuget/tables.go)); [cmd/trove/registry.go](../../cmd/trove/registry.go) links it for `trove migrate`. The reads the document and
search packages need are typed in [rows/read.go](../../internal/formats/nuget/rows/read.go). The reads of the package screens are typed in [rows/view.go](../../internal/formats/nuget/rows/view.go).

### Settings and configuration

The member `nuget` of a repository document holds `deleteMode` (`unlist` default, `delete`; local) and `forwardSearch` (`true` default; remote); the symbol and vulnerability members
arrive with R-NUGET-022 and R-NUGET-023 (Q-NUGET-31). The system file has `nuget.archive`, `nuget.registration` and `nuget.search`
([configuration.md](configuration.md#m2-keys)). The metadata period of Q-NUGET-2 is the generic `cache.metadataTtlSeconds` of a remote repository with the format's default of 300 s.

### The package view, the setup snippets and the guide

**The view** (WP3.1h) is `Format.View()`. It reads through `viewPort`, an interface of its own that the bound data port also implements, as npm's does: `ViewSummaries`, `ViewDetails`, `ViewFind`
and `ViewStored`, written in [formatdata/nuget/view.go](../../internal/formatdata/nuget/view.go) over [nuget_view.sql](../../internal/meta/postgres/queries/nuget_view.sql). The README is not a column:
`ViewDetails` opens the version's `.nupkg` through `Options.Blobs` (a `BlobReader`, ranged reads over the blob; [wire_nuget_blobs.go](../../internal/app/wire_nuget_blobs.go) binds it to the blob store) and
reads the file the `.nuspec` names with the `nupkg` leaf. A missing file, one over 1 MiB and one that is not UTF-8 are no README; a failing object store is an error, so that it is not cached as an absent README.
The members of `metadata`, the ordering of `Stored` and the empty `Withheld` are decided in Q-NUGET-48.

**The setup snippets** are `SetupTemplates()`: configure, install and, for `dotnet` and `nuget`, publish; Visual Studio has the `nuget.config` and the `PackageReference`. A credential part sits inside
`{{if .RequiresAuth}}` and spells the token reference of the language of its template (Q-NUGET-49). To change a command, change the template, the expectation in `setup_test.go`
(`TestR_NUGET_026_TheCommandsAreTheOnesOfTheSpecification`) and the one in [guide_nuget_test.go](../../internal/api/v1/guide_nuget_test.go), which renders it through the API with all three credential modes.

**The guide** is `guideSections()`, one Markdown file per section in [guide/](../../internal/formats/nuget/guide) (the same template language as npm's, over `formats.GuideData`). `Guide()` returns the sections of the classes the
descriptor lists, so the remote and virtual text is tested (`guide_test.go` renders all three classes) and reachable the day the class registers. The troubleshooting table is checked row by row against the problem
catalog and the status each code answers with; a code of the push that the ingestion has not registered yet is accepted with its specified status (`pendingCodes` in `guide_test.go`: delete an entry
when the code exists). To add a section: a file in `guide/`, a line in `guideSections()`, and, if it is a required topic, the kit refuses a class or client without it.

### Tests

`go test ./internal/formats/nuget/` runs the conformance kit (all eleven checks run and none is skipped, `TestR_ARCH_010_NoCheckIsSkipped`; check 6 runs the crafted
packages through the remote adapter's fill hook as well, and the three P1 routes that answer 501, embedded files, autocomplete and the symbol server, are the
fixture's `NoContent` entries, Q-ARCH-281), the route and matching tables, the classification, the error shape and the service index. [test/integration/nuget](../../test/integration/nuget) creates repositories through the management
API, reads the service index over HTTP with every credential form, and asserts the 404, 405, 409 and 501 answers; [view_store_test.go](../../test/integration/nuget/view_store_test.go) runs the view reads against PostgreSQL and [view_api_test.go](../../test/integration/nuget/view_api_test.go) the package screens of the management API over NuGet rows.

## Leaf packages

All four import only [internal/errs](../../internal/errs), the standard library and each other. They are leaves in the layer table
([layers.go](../../internal/archtest/layers.go)), so the format, its data port and its jobs share one parser.

| Package | Responsibility |
|---|---|
| [internal/formats/nuget/id](../../internal/formats/nuget/id) | `ValidateLocal` (ASCII IDs of a local repository), `ValidateUpstream` (the .NET `\w` rule for cached IDs, by Unicode category), `Lower`, `SameID`; code `nuget_invalid_id` |
| [internal/formats/nuget/version](../../internal/formats/nuget/version) | `Parse`, the normalized, full-normalized and URL forms, `Compare` and `SortKey`, `ParseRange` and `Satisfies`; codes `nuget_invalid_version` and `nuget_invalid_version_range` (R-NUGET-003, Q-NUGET-20 to Q-NUGET-22) |
| [internal/formats/nuget/nupkg](../../internal/formats/nuget/nupkg) | `Open` and `Inspect` over an `io.ReaderAt`: the central directory scanned by hand (ZIP64 included), every local header name compared, the name rules, the one `.nuspec` at the root; only the central directory and the `.nuspec` are read (R-NUGET-005, Q-NUGET-23, Q-NUGET-24) |
| [internal/formats/nuget/nuspec](../../internal/formats/nuget/nuspec) | `Parse` of the `.nuspec`: UTF-8 and UTF-16 with BOM, no DOCTYPE, no entity expansion, namespace and repeated-element rules (Q-NUGET-25) |
| [internal/formats/nuget/rows](../../internal/formats/nuget/rows) | the plain values of the data port, a leaf by the rule "`formats/<id>/rows`" |

Every refusal of `nupkg` and `nuspec` is a 400 `*errs.Error` with code `nuget_invalid_package`; the rule that fired is the error's cause,
so `errors.Is` works with the `Err*` values and `nupkg.IsRefusal(err)` identifies them. A cancelled context returns its own error. The
four codes are registered in [codes.go](../../internal/errs/codes.go) and the leaves alias them.

A call site that ingests a package calls `nupkg.Inspect` on the spool with `Limits` from the `nuget.archive` settings, then runs step 4 of
R-NUGET-005 on `Result.Metadata` (a symbol package is refused on the package path with `Metadata.IsSymbols()`), stores `Result.Nuspec` as
the generated `.nuspec` item and maps `nupkg.IsRefusal` to the 400 answer.

## The golden vectors

`version/testdata/vectors.json` holds 2977 versions, 299 ranges and 133 IDs, produced by the reference implementation, NuGet.Versioning, in
the pinned .NET SDK image. The pins are `NUGETVECTORS_SDK_IMAGE` and `NUGET_VERSIONING_VERSION` in [tools/versions.env](../../tools/versions.env).

```bash
make nuget-vectors          # regenerate the file (needs Docker; DOTNET=local uses a local dotnet)
make nuget-vectors-check    # fail when the committed file differs from a regeneration (CI job nuget-vectors)
```

The generator is [tools/nugetvectors](../../tools/nugetvectors). The tests `TestR_NUGET_003_Golden*` compare every accept and reject, every
normalized form, every pairwise order (both `Compare` and `SortKey`) and every range with the file. The documented deviations are the signed
pre-release identifiers (Q-NUGET-22) and the 255-byte bound (Q-NUGET-21). Unicode tables of .NET and Go can drift apart; only the vector cases
catch that, so extend the vectors when a rule depends on a category.

## Fuzzing

```bash
make fuzz-nuget NUGET_FUZZTIME=1m   # nupkg Inspect, nuspec Parse, version Normalize/Compare/Range, id: one minute each
```

`NUGET_FUZZ_TARGETS` holds the `package:Target` pairs. A failing input stays in the package's `testdata/fuzz`; the one under `version/` is a
kept regression input. The closing dispatch of Test and Release runs each of the six targets as its own leg for 30 minutes (job
`fuzz-nuget`, [releasing.md](releasing.md#ci)); a push or a pull request runs none of them.

## The counting test upstream

[test/nugetstub](../../test/nugetstub) is the NuGet counterpart of the npm stub: a generated corpus, both service hosts on one listener,
registration hives, flat container, search, weak ETags, per-host counters and fault modes. The section "The counting NuGet test upstream" in
[e2e.md](e2e.md) describes it; `make nuget-stub-image` builds its image. `deploy/dev/e2e.sh` deploys it when `TROVE_E2E_NUGETSTUB=1` (marker `M3 nuget-stub`; the sets
`nuget`, `nuget-closing` and `nuget-ha` set it) and exports `TROVE_E2E_NUGETSTUB_HOST` and `TROVE_E2E_NUGETSTUB_CONTENT_HOST`; the dev CA is
already in `proxy.outbound.tls.ca_files` of the dev profile, and a remote repository under test names the content host in its
`extraHosts` ([e2e.md](e2e.md#in-the-cluster-1)). The clients that talk to trove are the image of [test/e2e/nugetclients](../../test/e2e/nugetclients)
(`make nuget-clients-image`, `make nuget-clients-smoke`; [e2e.md](e2e.md#the-nuget-clients)).
The tests that drive them against the local, remote and virtual repositories and the customer endpoint (`make e2e SET=nuget`, `SET=nuget-closing`) are described in [e2e.md](e2e.md#the-nuget-tests).

## What the contract gives the format

| Need | Where |
|---|---|
| Inspect a fetched `.nupkg` before it is stored; a service index as discovery document | `formats.FillInspector`, `formats.Discoverer`, [proxy.md](proxy.md#what-a-format-adds-to-the-engine) (Q-ARCH-267, Q-ARCH-268) |
| Forward a search and cache the answer | `RemoteFetcher.Query`, `RemoteAdapter.UpstreamQuery` (Q-ARCH-270) |
| Fan out a search over the members, withhold terms, filter the rows | `Resolver.Plan`, `KeepsAway`, `ReadScope` (Q-ARCH-271) |
| Links in a refusal (the service-index URL of a 404) | `formats.RefusalFrom(r)` (Q-ARCH-263) |
| Spool a multipart part with its own length | `IngestOptions.Length` (Q-ARCH-264) |
| A snippet per shell, `dotnet add package <id> --version <v>` | `SetupTemplate.VersionSpec`, the `powershell` and `cmd` languages (Q-API-187 to Q-API-189) |
| `X-NuGet-ApiKey` accepted as a credential | `APIKey: true` on the push, delete, relist and symbol-push routes; the authentication stage reads the header only there, through the dispatcher's match, and never takes the session cookie there (Q-ARCH-266, Q-AUTH-186, Q-AUTH-187; [identity.md](identity.md#request-path)) |
| `checksumPolicy: require` refused | `catalog.Facts.ChecksumRequireUnsupported` (Q-ARCH-269) |

## The write side

Push, unlist, relist and delete, the tombstones and the serve check (WP3.1d, WP3.1e; R-NUGET-005 to R-NUGET-007, R-NUGET-032, Q-NUGET-36 to Q-NUGET-40).
The order of the checks and what each refusal answers is on [the security page](../security/nuget-publish.md).

| File | Responsibility |
|---|---|
| [publish.go](../../internal/formats/nuget/publish.go) | `readIncoming`: steps 1 to 4 of R-NUGET-005 over a spooled package (`nupkg.Inspect` with the `nuget.archive` limits, the local ID rule, the version, the dependencies, the refusal of a symbol package); `incoming` is the payload to the commit; the metadata JSON (typed by [rows/metadata.go](../../internal/formats/nuget/rows/metadata.go)), the lowercased package types, the ID tokens of the search document |
| [handlers_publish.go](../../internal/formats/nuget/handlers_publish.go) | `handlePush`: the multipart reader (first part is the package, 64 KiB of preamble at most, a bare body is the package), the spool with its own length, the permission on the declared ID, the number (`numberTaken`: replay 201, 409), the shadowing check, the commit with the generated `.nuspec` as a companion, `commitError` |
| [hooks.go](../../internal/formats/nuget/hooks.go) | `OnCommit`: the package row lock first, then the versions and the tombstone, the claim, the version row, the revision, the NuGet row and the published event; the companion's hook links the `.nuspec`; a commit that is not a push is refused (501) |
| [hooks_remove.go](../../internal/formats/nuget/hooks_remove.go) | `OnItemsRemoved`: the removal of a `.nupkg` is the removal of the version (tombstone, version row, revision, `package.deleted`); a generated file alone is refused with 409. `OnItemsRestored`: the tombstone goes with a restored package (P1) |
| [handlers_delete.go](../../internal/formats/nuget/handlers_delete.go) | `handleDelete` (unlist, or the removal through the package writer in `delete` mode), `handleRelist`; repeatable; 404 for a number never held, 204 for a tombstoned one |
| [servecheck.go](../../internal/formats/nuget/servecheck.go) | `ServeCheck`: a stored version is served, listed or not; a tombstoned one and a file without a NuGet row are 404 |
| [metrics.go](../../internal/formats/nuget/metrics.go) | `trove_nuget_push_replayed_total{repo}` |
| [rows/write.go](../../internal/formats/nuget/rows/write.go) | the values of a removal, a listing change, a lookup and the serve state |
| [internal/formatdata/nuget](../../internal/formatdata/nuget) | `store.go` (every write and the audit events in the change's transaction, the journal entries), `read.go` (`Versions`, `Registration`), `search.go` (the search read, replaced by the search package) |
| [nugettest](../../internal/formats/nuget/nugettest/store.go) | the twin with real state: the rows of versions, tombstones and listing, the blob digests a removal reads, a rollback that leaves no trace, the logs `Published`, `Tombstones`, `Listings` and `Journal`; `Revisions` binds the revision and the display name to the host of a test |

How a push goes, in the order of the code:

1. `packagePart` hands the package to the spool; `readIncoming` reads it. A refusal is a 400 with the code of the rule.
2. `Authorize` and `CheckOwnership` decide on the declared ID; `numberTaken` reads `VersionFacts` and `Tombstone` outside a transaction. The identical bytes of a stored version are
   answered 201 here and nothing is written; other bytes and a tombstone are 409.
3. `shadowCheck` asks the resolver for the first version of an ID.
4. `Ingest().Commit` stores the package (lead) and the generated `.nuspec` (companion, `Generated`) in one transaction; `OnCommit` of the lead decides the number again under the package
   row lock and writes the rows and the events.

A hook never takes a second connection: `Store.Tombstone` takes the hook's transaction (Q-NUGET-40). The delete route removes the files through the package writer and the hook of the item store
does the rest, for the protocol, raw and management deletes alike; `formats.RemovedItem.SHA256` carries the digest the hook needs because the item row is gone when it runs (Q-NUGET-39).

The authentication stage reads `Authorization` on a push; `X-NuGet-ApiKey` is read once the API-key stage is in the build (R-NUGET-012). The integration tests send a Bearer token.

Not built here: the upload of the management API, copy and move (they reach `OnCommit` without a push payload and answer 501), the restore replay of the journal entries
(R-NUGET-033; the entries are written and flagged, nothing reads them yet), the trash can (P1), and the remote and virtual classes.

Tests: `go test ./internal/formats/nuget/ ./internal/formats/nuget/nugettest/ ./internal/formatdata/nuget/` (push, unlist, delete, hooks and the kit's checks 6 and 9 with the twin),
and [test/integration/nuget](../../test/integration/nuget) `push_test.go` against PostgreSQL and the object store (the rows, the events, the outbox, the replica counter, two replicas,
concurrent pushes, a push racing a delete).

## Symbol packages

The symbol push `PUT v3/symbolpackage` (WP3.3; R-NUGET-020, Q-NUGET-11, Q-NUGET-60, Q-NUGET-61). The symbol server (R-NUGET-021) is P1 and not built: `GET v3/symbols/{key...}` still answers 501.

| File | Responsibility |
|---|---|
| [handlers_symbols.go](../../internal/formats/nuget/handlers_symbols.go) | `readSymbols` (the archive and `.nuspec` rules, the `SymbolsPackage` type, then `symbols.Validate`), `handleSymbolPush` (the spool, the permission on the declared ID, the lookup of the version, the replay or the 409, the commit), `commitSymbols` (the `OnCommit` of the `.snupkg`: package row lock, the live version, the claim, the link to the version, the symbol row) |
| [symbols](../../internal/formats/nuget/symbols/symbols.go) | A leaf: the allowed entry extensions, the `.pdb` signatures (`Sniff`: `BSJB`, `Microsoft C/C++ MSF 7.00`), `Validate` over an opened archive |
| [nupkg/head.go](../../internal/formats/nuget/nupkg/head.go) | `Archive.Head`: the first bytes of one entry, held to the compression ratio, not counted against the read budget |
| [internal/formatdata/nuget/symbols.go](../../internal/formatdata/nuget/symbols.go) | The job kind `nuget-symbol-index` (registered in `jobs.Default` at initialisation, queue `index`, exclusive), the enqueue in the transaction of `Store.InsertSymbolPackage`, the handler `RunIndexJob`; the read is [nuget_symbols.sql](../../internal/meta/postgres/queries/nuget_symbols.sql) |

How a symbol push goes, in the order of the code:

1. `packagePart` and the spool as for a package; `readSymbols` refuses (400 `nuget_invalid_package`) a package without the type, an entry outside `.pdb .nuspec .xml .psmdcp .rels .p7s`, and a `.pdb` that is neither format.
2. `Authorize` and `CheckOwnership` on the declared ID, then `VersionFacts`: no live version is 404, whatever the reason (never pushed, deleted, tombstoned).
3. `symbolsStored` compares the SHA-256 of the item at the `.snupkg` path with the body: equal is 201 without a write, different is 409 `nuget_symbol_package_exists`. The row and the item are one fact (the row cascades from the item), so the item alone decides.
4. `Ingest().Commit` stores the item; `commitSymbols` links it to the version, so the hard delete of the version removes it, and `InsertSymbolPackage` writes the row and enqueues the job in the same transaction. Linking raises the package revision (the generic rule).

The twin (`nugettest.Store`) logs the rows in `SymbolJobs`. `RunIndexJob` reads one row through `run.DB()` and records `gone`, `indexed` or `stored` as the result; it leaves `indexed_at` unset until R-NUGET-021 extracts the files. How to extend: the extraction goes into `RunIndexJob` (ranged reads of the `.snupkg`, one PDB spooled at a time), together with the migration that enqueues the packages with `indexed_at IS NULL`.

Tests: `go test ./internal/formats/nuget/ ./internal/formats/nuget/symbols/ ./internal/formats/nuget/nupkg/` (`TestR_NUGET_020_*`), and [test/integration/nuget/symbols_test.go](../../test/integration/nuget/symbols_test.go) (the rows, the job on a `worker`, two replicas, the delete).

## Rendered documents and package files

The version list of the flat container, the three registration documents (index, page, leaf) in three hives, and the `.nupkg` and `.nuspec` of a version are
served for local repositories (R-NUGET-008, R-NUGET-009). They read through the `Store` port and the Host's package rows, and write nothing.

| File | Responsibility |
|---|---|
| [documents.go](../../internal/formats/nuget/documents.go) | `serveDocument`: the read permission on the ID, the exposure view, the revision lookup, the weak validator and the 304, the rendition cache, gzip, the reply; `docSpec` describes one document; `viewOf` is the exposure view of the request |
| [handlers_flat.go](../../internal/formats/nuget/handlers_flat.go) | `handleFlatIndex` (the version list), `handleNupkg`, `handleNuspec` through `serveFile` (stat, serve with the protocol's media type) |
| [handlers_registration.go](../../internal/formats/nuget/handlers_registration.go) | `handleRegistrationIndex`, `…Page`, `…Leaf`: one `Store.Registration` read, `registration.Visible` for the hive and the exposure, then `registration.Doc` |
| [document](../../internal/formats/nuget/document/document.go) | The helpers every rendered document shares: `Marshal` (deterministic JSON), `Timestamp`, `VersionList`, `Gzip`, `AcceptsGzip`, `Inputs.ETag`, `IfNoneMatch`, `Reply.Write` (headers, HEAD without a body, the length of the variant sent) |
| [registration](../../internal/formats/nuget/registration) | `Doc.Index`, `Doc.Page`, `Doc.Leaf`, `Visible` (hive and exposure filter), `Hive`, `Paging`, `Feed` (the URLs), `ParseMetadata` (the stored object) |

Both packages are plain edge packages, not layer leaves: the format is their only caller, and `registration` imports `rows`, `id`, `version` and `document`.

The path of a document request:

1. `Authorize(read)` on the ID, then the exposure view: a package the endpoint does not expose is a 404.
2. One `Packages().Package` lookup gives `packages.rev_seq`. The validator is computed from it and the other inputs of `document.Inputs` without rendering, and `If-None-Match`
   answers 304 here, reading no rows.
3. The replica's rendition cache is asked under a key with the same inputs and the coding; a gzip hit is made from a cached identity rendition.
4. On a miss the `Store` is read (`Versions` for the list, `Registration` for the hives), the document is rendered, both codings are cached, and the rows' own revision replaces
   the looked-up one in key and validator when a write raced the lookup.

Things to know when changing a document:

- A member that changes a document's bytes without changing its inputs needs `RenderVersion` (index.go) raised. A new nuget setting that changes the bytes must join `docSpec.settings`.
- Documents are structs, never maps; the order of the members is the order of the sketch in R-NUGET-009, and the golden documents in
  [registration/testdata](../../internal/formats/nuget/registration/testdata) pin it (`go test ./internal/formats/nuget/registration -update` rewrites them; read the diff).
- The registration reads `nuget_version.metadata` with the keys of the table in R-NUGET-005 (Q-NUGET-43). The writer of the row (`rows.VersionInput.Metadata`) must produce
  `title`, `summary`, `description`, `language`, `authors`, `tags` (a list), `projectUrl`, `iconUrl`, `licenseUrl`, `license{type,version,value}`,
  `requireLicenseAcceptance`, `minClientVersion` and `dependencyGroups[{targetFramework, dependencies[{id, version}]}]`; a damaged object makes the document a 500.
- The remote class is served by [handlers_remote.go](../../internal/formats/nuget/handlers_remote.go) and the virtual class is not served here. A handler for them switches on `rq.Repo.Class` before `serveDocument`; `registration.Doc.Extras` carries the pass-through
  `deprecation` and `vulnerabilities` of an upstream entry.
- Tests: the fixture in [documents_fixture_test.go](../../internal/formats/nuget/documents_fixture_test.go) is a Host of the kit over a `docStore` (the twin, with the two document
  reads) and commits versions the way a write does, raising the revision. It is the place to start a test of a document.

## NuGet on a customer endpoint

An endpoint with `exposure: rules` narrows what a NuGet repository serves (R-NUGET-034, R-EP-018). Two pieces cooperate, and the format adds no filter of its own to the second.

| Piece | Where | What it decides |
|---|---|---|
| The ceiling | the decision point, called by `Authorize`, `ReadScope` and the route-level check | The package and version of a request, and every write (403 `endpoint_read_only`, R-EP-013). Its rules are compiled from the rule rows with the adapter in [exposure.go](../../internal/formats/nuget/exposure.go): `ValidateRange`, `InRange`, `Prerelease`, `Dimensions` (packages and versions, no tags) |
| The view | `rq.Endpoint.Exposure`, read by `viewOf` ([documents.go](../../internal/formats/nuget/documents.go)) | What a list or a document contains: the rows are narrowed before the document is rendered and paged (Q-NUGET-63) |

What each route does with it:

| Route | Call | Hidden means |
|---|---|---|
| `flatcontainer/{id}/index.json` | `exposes(viewOf(rq), id)` over `Store.Versions` | the version is not listed; none left is a 404 |
| registration index, page, leaf, three hives | `registration.Visible(versions, hive, keep)` | the leaf is not listed, counts and bounds follow the rest; a leaf of a hidden version is a 404 |
| `.nupkg`, `.nuspec` | `serveFile` asks `hiddenVersion` after `Authorize` | 404, the same body as a missing version |
| search | `Resolver.ReadScope` answers `ReadRows`; `MayRead` is the ceiling | the version is not in `versions`, the package is not counted when none is left |
| service index | `handleIndex`: `takesPushes(rq) && !onRulesEndpoint(rq)` | no publish resource (Q-EP-68) |
| push, delete, relist | the route-level `Authorize` of the write action | 403 `endpoint_read_only` before the body is read |

Rules for a handler that serves a list or a version, in the order they matter:

1. Ask the view with `lower_id` and the URL form of the version (Q-NUGET-62); never with the original spelling. The decision point compares version names letter by letter with what the format passes (Q-EP-69, H-380).
2. A package the view does not expose answers the 404 of a missing package before any lookup (`serveDocument` does this for every document).
3. The rendition key and the validator include `rq.Endpoint.Name` and `rq.Endpoint.ExposureFingerprint`; `serveDocument` and `indexETag` do. A new document that bypasses `serveDocument` must add both, or a rule change leaves the old document valid.
4. Embedded files, the symbol server and autocomplete, when built, call `hiddenVersion` for a version they serve; the symbol server answers 404 on `onRulesEndpoint(rq)` until its filter is specified (R-NUGET-034).
5. A remote or virtual handler that merges versions filters the rows, not the merged bytes (Q-NUGET-63, H-295).

Tests. Adapter, documents, paging, the service index and search over the real adapter's semantics:
[exposure_test.go](../../internal/formats/nuget/exposure_test.go) (`rulesView` stands in for the decision point). The whole stack with real rules, a bound token on a second listener, the writes and
the validators on two replicas: [test/integration/nuget/exposure_test.go](../../test/integration/nuget/exposure_test.go) and
[exposure_versions_test.go](../../test/integration/nuget/exposure_versions_test.go). Run them with `make integration-up` and
`go test -tags integration ./test/integration/nuget -run 'R_NUGET_034|R_EP_018'`.

## Search

The search of a local repository (R-NUGET-010, R-AUTHZ-016; decisions Q-NUGET-45 to Q-NUGET-47) is four pieces.

| Piece | Responsibility |
|---|---|
| [internal/formats/nuget/search](../../internal/formats/nuget/search) | `Parse` (terms, phrases, the field prefixes, the folding), `ParseParams` (`skip`, `take`, `prerelease`, `semVerLevel`, `packageType` with the bounds of `nuget.search`), `IDTokens` (the words of an ID that `search_doc` is built from), `Render` (the result document). It imports the rows leaf, `id`, `version`, `errs` and the standard library; it sits in the edge layer with the format, so the data port (a domain package) does not import it. |
| [handlers_search.go](../../internal/formats/nuget/handlers_search.go) | `handleSearch`, `runSearch`: the read scope of the caller from `Resolver.ReadScope`, then `searchWhole` or `searchRows`. |
| [internal/meta/postgres/queries/nuget_search.sql](../../internal/meta/postgres/queries/nuget_search.sql) | `NuGetSearchCount`, `NuGetSearchPage` (one page, with or without metadata), `NuGetSearchVersions` (the named versions). Generated into `q/` by `make generate`. |
| [internal/formatdata/nuget/search.go](../../internal/formatdata/nuget/search.go) | `Store.Search`, `Store.SearchCount`: the terms to JSON with their tsquery texts, the rows to `rows.SearchHit`. |

**The way a request runs.** `ParseParams` answers 400 for what is out of bounds (before anything is read). `ReadScope.Shape` decides the path: `ReadNone` answers an empty result without asking the store;
`ReadAll` (the caller reads the repository whole) asks `SearchCount` and then `Search` for `[skip, skip+take)`; `ReadRows` scans (Q-NUGET-45). The scan asks `Search` with `KeysOnly` and `Limit` 500 until a batch is short,
asks `MayRead(repo, lower_id, lower_version)` for every listed visible version, keeps the packages with a readable version, applies the `packageType` filter to the latest readable one, counts them
and keeps the entries of the requested page. It reads the page with `Search` and `Only`, the readable versions of the page, and puts the hits back in the order of the scan. More than 200,000 matches answer 400.
The endpoint's exposure rules need no code of their own here: the shape and `MayRead` already contain the endpoint's ceiling.

**What the statements do.** One row per package: the latest visible version (listed, not trashed, under `prerelease` and `semVerLevel`), picked by `DISTINCT ON (lower_id)` over `nuget_version_latest`. The terms are tested on that row:
free text by `strpos` on `lower_id` or by the term's tsquery on `search_doc`; `id` and `packageid` on `lower_id`; `title`, `tags`, `author`, `description` and `summary` on that key of `nuget_version.metadata`; `version` over the package's visible versions; every
other field never. The tsquery text is built in `search.go` from letters and digits only (`'acme':* <-> 'widgets':*`), so no operator of the query language reaches PostgreSQL; the weight-A variant ranks an ID-token match.
Ranking is the best tier over the free-text terms (exact ID 0, ID prefix 1, ID substring or token 2, other fields 3), then `lower_id COLLATE "C"`.

**What `search_doc` must hold.** The statement `NuGetInsertVersion` builds the vector from the parts of `rows.VersionInput`. `IDTokens` of the declared ID is what the ingestion hands over as `IDTokens`, and the reindex job (R-NUGET-025) must use the same function; a
different split changes which packages `json` finds. The metadata object must carry the keys `title`, `summary`, `description`, `authors`, `tags` (a list or a text), `iconUrl`, `licenseUrl`, `projectUrl` and `packageTypes` (a list of objects with `name`): the result and the field terms read them
(the decoding ignores the case of a key).

**Tests.** Parser, bounds, tokens and the document: `go test ./internal/formats/nuget/search/`. The handler over a fake port with a fake read scope (paths, paging, the scan, the bound): [handlers_search_test.go](../../internal/formats/nuget/handlers_search_test.go).
The statements against PostgreSQL, with rows inserted in the shape of `NuGetInsertVersion` and real grants (package include and exclude patterns): [test/integration/nuget/search_test.go](../../test/integration/nuget/search_test.go). When the push road exists, a test of push-then-search joins them.

**Adding a field or a tier.** Parse it in [query.go](../../internal/formats/nuget/search/query.go), add the branch to the `CASE` of both statements (`NuGetSearchCount` and `NuGetSearchPage` carry the same predicate and must stay equal), extend the field table of R-NUGET-010, and add the case to `TestR_NUGET_010_ParseSplitsTheQueryIntoTerms` and `TestR_NUGET_010_FieldTermsLimitTheMatch`.

## The remote class

A remote repository of NuGet proxies and caches a NuGet v3 feed (R-NUGET-014, R-NUGET-015, R-NUGET-031; Q-NUGET-51 to Q-NUGET-55). The engine owns the request path, freshness, collapsing,
the negative cache and the circuit ([proxy.md](proxy.md)); the format adds the adapter below.

| File | Responsibility |
|---|---|
| [remote.go](../../internal/formats/nuget/remote.go) | The adapter (`Remote()` returns the stable value `remoteAdapter`, which embeds `formats.RemoteBase`): `parseTarget` (the canonical paths of a remote: discovery `.nuget/index.json`, `{id}/index.json`, `{id}/.registration/index.json`, `{id}/.registration/page/{lower}/{upper}.json`, the `.nupkg` and `.nuspec`), `DiscoveryDocument`/`ParseDiscovery`, `UpstreamRequest`, `PrepareFill` with the URL-map rows, `OnFill` (the `metadata_changed` signal), `Rewrite`, `UpstreamQuery` |
| [remote_fill.go](../../internal/formats/nuget/remote_fill.go) | `InspectsFill` (the `.nupkg` only) and `InspectFill` (the identity check, 502 `upstream_package_mismatch`; an unreadable package gives no inspection), `commitFill` (version, package row and the NuGet row of origin `cache`), `onCacheRemoved`, the counter `trove_nuget_remote_unparseable_packages_total{repo}` |
| [handlers_remote.go](../../internal/formats/nuget/handlers_remote.go) | The documents of a remote: `serveRemote` (permission, exposure, the stored documents, the validator, the rendition cache, the rendering), `remoteFlatIndex`, `remoteRegistration`, `remoteFile`, `renderRegistration` |
| [remote/](../../internal/formats/nuget/remote) | The leaf: `ParseDiscovery` (the resource types in the order of R-NUGET-014, the shape of every address), `Join` (one escaped segment per segment), `Decode` (bounded gunzip), `VersionList`, `ParseIndex`, `ParsePage`, `Convert`/`Merge` (a leaf to the row the renderer reads, with the pass-through members) |

**Request path.** A handler of a remote route switches on `rq.Repo.Class == formats.Remote` before the local code (`handleFlatIndex`, `serveRegistration`, `serveFile`). The version list is read with
`Remote().Metadata` and rendered in URL form, ascending. A registration document is read as the stored index plus the pages it does not inline (fetched in parallel, at most 8 at once, each its own
cache entry), parsed into `remote.Version`s and rendered by the renderer of the local class (`registration.Doc` with `Extras`, `UpstreamIDs`) with the hive's filter and trove's paging. The validator is a
hash of the inputs of the rendering and the digests of the stored documents, so a 304 costs the engine's reads and no parse; the page list of an index is cached per digest in the rendition cache.
`Rewrite` is the semver2 hive with the default paging, which the kit's check 4 exercises. Cached packages go through the engine's `Artifact`; the first byte leaves after `InspectFill` accepted the package.

**Adding a resource.** Add its canonical path to `parseTarget` and `upstreamURL`, give `PrepareFill` the validation of its document, decide in `InspectsFill` whether it is inspected, and give the handler its
branch. Mind `Classify`: the engine's gone-ID rule and the URL map act on the package `Classify` names for the path.

**What a virtual repository can use.** `storedDocument` reads a stored document of the remote for a request addressed to it (for a member, the request derived through `Request.Member`), `remoteVersions` turns the
stored index and pages into the versions of an ID (`remote.Version`: the row the renderer reads and the pass-through members), and `renderRegistration` renders them; the merge of the rows of several members is the virtual package's.

**Tests.** The leaf: `go test ./internal/formats/nuget/remote/`. The adapter, the fill hooks and the documents over the in-memory upstream of the kit (`formattest.MemRemote`):
[remote_test.go](../../internal/formats/nuget/remote_test.go). End to end against the counting test upstream in the test process, with two replicas and with Valkey on and off:
[test/integration/nuget/remote_test.go](../../test/integration/nuget/remote_test.go) (`make test-integration INTEGRATION_PKGS=./test/integration/nuget`). The upstream is a TLS server with a
self-signed CA the replicas trust through `proxy.outbound.tls.ca_files`, reachable under two names (`127.0.0.1` for the index and the registrations, `localhost` for the flat container) with `localhost`
as the remote's extra host; a test wraps the stub's handler to change what it answers (a gzip-coded hive, a flat container without a version, garbage in place of a package).

**Limits worth knowing.** The archive limits of `InspectFill` are the defaults of `nuget.archive` (H-336, [nuget-remote.md](../security/nuget-remote.md)). `forwardSearch` has no effect yet (Q-NUGET-55). A feed whose flat container
lacks a version its registration lists is served only after a registration of the ID was read (Q-NUGET-54).
## Virtual repositories

The virtual class (R-NUGET-017 to R-NUGET-019; decisions Q-NUGET-56 to Q-NUGET-59). The engine of [virtual.md](virtual.md) plans a request, walks the tiers of an artifact and
caches the merged document of an ID; the format contributes the strategy per path, the document of a local member, the merge, and the rendering of the answer.

| Piece | Responsibility |
|---|---|
| [virtual.go](../../internal/formats/nuget/virtual.go) | `Format.Virtual()`: `Strategy` (the version list and the registration entries are merged, every other path is first match), `LocalDocument` (a member's rows to its document, with the `rev_seq` read before the rows), `Merge` (to package `merge`), `ArtifactPrefix` (`{lower_id}/`). `VariantFlat`, `VariantRegistration`. |
| [merge](../../internal/formats/nuget/merge) | `Do` (the per-version source rule: local, then a remote member whose cache holds the package, then the remote members that would fetch it; ties by position), `DecodeUpstream` (an upstream registration index with its pages inlined to entries), the neutral `Doc`. No host, no request, no clock. |
| [handlers_virtual.go](../../internal/formats/nuget/handlers_virtual.go) | `serveVirtual` (authorize, exposure, `Resolver.Metadata`, validator from the merge fingerprint, the rendition cache unless the merge is partial), `virtualFlat`, `virtualRegistration`, `virtualFile` (`Resolver.Artifact`). The existing handlers branch on `rq.Repo.Class` in one line each. |
| [search_virtual.go](../../internal/formats/nuget/search_virtual.go) | `runVirtualSearch`: `Resolver.Plan`, one `searchRepo` per local member in parallel, `KeepsAway` per result, `mergeHits`/`unionHit`. `searchRemote` is the seam of the forwarded search. |
| [metrics_virtual.go](../../internal/formats/nuget/metrics_virtual.go) | `trove_nuget_virtual_member_skipped_total{repo,reason}`; the one reason is `unavailable` (a partial merge). |

**The way a request runs.** `GET flatcontainer/{id}/index.json` and the three registration documents authorize the read on the ID, apply the endpoint's exposure, and ask `Resolver.Metadata` for the merge of
`{lower_id}/index.json` (variant `flat`) or `{lower_id}/.registration/index.json` (variant `registration`). The engine reads the document of every local member through `LocalDocument` and of every remote
member from the remote engine, merges them with `Merge`, and keeps the result by member set until the revision of a local member changes. The handler computes the weak ETag from the merge fingerprint and the serving inputs
before it decodes anything, answers 304 on a match, and otherwise renders the list or the hive from the merged entries with the registration package. A package file goes to `Resolver.Artifact` at its canonical path; the engine names the member in `Trove-Resolved-From`.

**Writes.** Nothing here: the dispatcher sends a write route (`Classes: local`) addressed to a virtual repository to its deployment target (`redirectClass`), so `handlePush`, `handleDelete` and `handleRelist` run on the target and `rq.Addressed()` is the target.
The service index lists the publish resources when `Resolver.DeployTarget` resolves one (`takesPushes`); the decision point refuses a write on a virtual repository without a target with 403 before the dispatcher is reached (Q-NUGET-59).

**What a remote member must hand over** (the contract with the remote adapter, Q-NUGET-56). For `{lower_id}/index.json` the stored upstream version list; for `{lower_id}/.registration/index.json`, variant `registration`, the upstream's registration index
with every page inlined (`items` present on each page). `merge.DecodeUpstream` fails a member with `upstream_failed` when a page has none.
The remote adapter does not keep that contract yet: since Q-NUGET-67 it hands over the stored upstream bytes, which are gzip-coded for the gzip hives
and list the pages of an ID with 128 or more versions by URL, and `DecodeUpstream` and `decodeFlat` inflate nothing. A registration of a nuget.org ID
through a virtual repository therefore answers 502; the test upstream gzips only when asked and has no ID that large, so the tests pass. Open finding
F-22 of [promt.md](../projektplan/promt.md#8--open-findings).

**Tests.** The merge: `go test ./internal/formats/nuget/merge/`. The handlers over a stand-in for the engine that merges the documents a test names ([virtual_test.go](../../internal/formats/nuget/virtual_test.go), `mergeResolver`), the search over a port per
repository ([search_virtual_test.go](../../internal/formats/nuget/search_virtual_test.go)), and check 5 of the conformance kit ([virtual_fixture_test.go](../../internal/formats/nuget/virtual_fixture_test.go)). Over PostgreSQL, with the real engine, pin rules, claims, grants and two replicas:
[test/integration/nuget/virtual_test.go](../../test/integration/nuget/virtual_test.go). Remote members join those tests with the remote class (they need the upstream stub).

**Adding a document to the merge.** Give it a canonical path that `Classify` knows as metadata of a package, extend `documentOf` and `LocalDocument`, give `merge.Do` its kind, and render it in `handlers_virtual.go`; the validator and the rendition key already carry the fingerprint.
