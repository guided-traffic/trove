# The NuGet format

Status: the NuGet format is registered with its skeleton (phase 4, WP3.1c): the route table of the whole NuGet v3 protocol, the matching
and the classification of paths, the service index, the error shape, the settings and the configuration section, and the data port interface.
The operations behind the other routes - package content, registrations, search, push, unlist, relist, the symbol resources - are written by
the work packages that own them and answer 501 `nuget_not_implemented` until then ([handlers_stub.go](../../internal/formats/nuget/handlers_stub.go)).
The requirements are in [spec/formats/nuget.md](../spec/formats/nuget.md); the contract the format implements is in [formats.md](formats.md).

## The format

Package [internal/formats/nuget](../../internal/formats/nuget), registered by [internal/formats/all](../../internal/formats/all/all.go). It
imports the format contract and the leaf packages below, and reaches its tables only through the `Store` port.

| File | Responsibility |
|---|---|
| [format.go](../../internal/formats/nuget/format.go) | `Format`, `Descriptor` (the catalog facts: class `local` for now, Q-NUGET-28; the upstream, the 300 s and 1800 s periods, `ChecksumRequireUnsupported`, `NotifyAreas`, `DataPort`), `Inspect` (refuses every upload until the ingestion exists, Q-NUGET-32) |
| [routes.go](../../internal/formats/nuget/routes.go) | The route table of R-NUGET-001, written once; the route names (`nuget.flatcontainer.nupkg`, Q-NUGET-30); `op`, which records `X-NuGet-Session-Id` |
| [match.go](../../internal/formats/nuget/match.go) | The matchers of the protocol paths, `Match` (the 404 `nuget_v2_unsupported`, the normalization of the ID, the version and the file name), the package-name rule `names` |
| [paths.go](../../internal/formats/nuget/paths.go) | The canonical paths (`NupkgPath`, `NuspecPath`, `PackageDocumentPath`), `Classify`, `CanonicalPath` (route to canonical path), the routing name of a symbol key |
| [index.go](../../internal/formats/nuget/index.go) | `ServiceIndex` (the document), `handleIndex`, the weak ETag and `If-None-Match` |
| [errors.go](../../internal/formats/nuget/errors.go) | `WriteError`: the one-line `text/plain` body `<code>: <message>`, the Basic challenge, `Retry-After` capped at 30 s, the message of the v2 404 |
| [settings.go](../../internal/formats/nuget/settings.go), [settings.schema.json](../../internal/formats/nuget/settings.schema.json) | The `nuget` member of a repository document: `deleteMode`, `forwardSearch`; `settingsOf` |
| [bind.go](../../internal/formats/nuget/bind.go) | `Bind`: the `nuget` section of the configuration per Host; `archiveLimits` maps `nuget.archive` onto `nupkg.Limits` |
| [store.go](../../internal/formats/nuget/store.go) | The data port interface `Store`; the block `>>> M3 nuget-read` holds the reads of the document and search packages |
| [exposure.go](../../internal/formats/nuget/exposure.go) | The exposure adapter: packages and versions, no tags; NuGet's range grammar. The filter of the documents is written with them (R-NUGET-034, H-295) |
| [handlers_stub.go](../../internal/formats/nuget/handlers_stub.go) | One method per route that is not built; the package that builds it moves the method into a file of its own |
| [nugettest](../../internal/formats/nuget/nugettest/store.go) | The in-memory twin of the port, for the conformance kit and unit tests |

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
| `nuget.push` | PUT `v3/package` | upload, deploy, API key |
| `nuget.delete` | DELETE `v3/package/{id}/{version}` | delete, delete, API key |
| `nuget.relist` | POST `v3/package/{id}/{version}` | admin, deploy, API key |
| `nuget.symbolpush` | PUT `v3/symbolpackage` | upload, deploy, API key |
| `nuget.symbols` (P1) | GET `v3/symbols/{key...}` | download, read |

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
5. Drop the check from `Skip` in the fixture when its subject exists (ingestion: checks 6 and 9; the remote and virtual adapters: 4 and 5; clients and templates: 8; the view: 11).

### Data port and tables

The port is `nuget.Store`; the implementation is [internal/formatdata/nuget](../../internal/formatdata/nuget/store.go), whose methods answer `nuget_not_implemented` until WP3.1d
writes them (the statements are in [nuget_local.sql](../../internal/meta/postgres/queries/nuget_local.sql)). The package also registers the four tables of migration 00017 with the
backup ([tables.go](../../internal/formatdata/nuget/tables.go)); [cmd/trove/registry.go](../../cmd/trove/registry.go) links it for `trove migrate`. The reads the document and
search packages need are typed in [rows/read.go](../../internal/formats/nuget/rows/read.go).

### Settings and configuration

The member `nuget` of a repository document holds `deleteMode` (`unlist` default, `delete`; local) and `forwardSearch` (`true` default; remote); the symbol and vulnerability members
arrive with R-NUGET-022 and R-NUGET-023 (Q-NUGET-31). The system file has `nuget.archive`, `nuget.registration` and `nuget.search`
([configuration.md](configuration.md#m2-keys)). The metadata period of Q-NUGET-2 is the generic `cache.metadataTtlSeconds` of a remote repository with the format's default of 300 s.

### Tests

`go test ./internal/formats/nuget/` runs the conformance kit (checks 1, 2, 3, 7 and 10 run; the others are skipped with their reason, and the kit refuses a skip once the format has what a check
is about), the route and matching tables, the classification, the error shape and the service index. [test/integration/nuget](../../test/integration/nuget) creates repositories through the management
API, reads the service index over HTTP with every credential form, and asserts the 404, 405, 409 and 501 answers.

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
kept regression input. No CI leg runs these yet (the npm legs run on the closing dispatch only, see [releasing.md](releasing.md)).

## The counting test upstream

[test/nugetstub](../../test/nugetstub) is the NuGet counterpart of the npm stub: a generated corpus, both service hosts on one listener,
registration hives, flat container, search, weak ETags, per-host counters and fault modes. The section "The counting NuGet test upstream" in
[e2e.md](e2e.md) describes it; `make nuget-stub-image` builds its image. The deploy wiring (a `TROVE_E2E_NUGETSTUB` block in
`deploy/dev/e2e.sh` next to the npm one, `extraHosts` for the content service, the dev CA in `proxy.outbound.tls.ca_files`) is not done.

## What the contract gives the format

| Need | Where |
|---|---|
| Inspect a fetched `.nupkg` before it is stored; a service index as discovery document | `formats.FillInspector`, `formats.Discoverer`, [proxy.md](proxy.md#what-a-format-adds-to-the-engine) (Q-ARCH-267, Q-ARCH-268) |
| Forward a search and cache the answer | `RemoteFetcher.Query`, `RemoteAdapter.UpstreamQuery` (Q-ARCH-270) |
| Fan out a search over the members, withhold terms, filter the rows | `Resolver.Plan`, `KeepsAway`, `ReadScope` (Q-ARCH-271) |
| Links in a refusal (the service-index URL of a 404) | `formats.RefusalFrom(r)` (Q-ARCH-263) |
| Spool a multipart part with its own length | `IngestOptions.Length` (Q-ARCH-264) |
| A snippet per shell, `dotnet add package <id> --version <v>` | `SetupTemplate.VersionSpec`, the `powershell` and `cmd` languages (Q-API-187 to Q-API-189) |
| `X-NuGet-ApiKey` accepted as a credential | `Route.APIKey` (declared; the authentication stage does not read it yet, Q-ARCH-266) |
| `checksumPolicy: require` refused | `catalog.Facts.ChecksumRequireUnsupported` (Q-ARCH-269) |
