# The NuGet format

Status: the NuGet format is not registered yet. What exists is the foundation the format is built on: the rule libraries, the golden
vectors they are tested against, the reader of a package, the counting test upstream and the contract additions the format needs from the
core. The format itself (routes, handlers, ingestion pipeline, data port and migration) arrives with the skeleton work package. The
requirements are in [spec/formats/nuget.md](../spec/formats/nuget.md); the contract the format implements is in [formats.md](formats.md).

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
