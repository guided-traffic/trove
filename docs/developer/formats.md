# The format contract

How a package format plugs into trove: the contract in [internal/formats](../../internal/formats/), the registry, the `Host` a format
reaches the core through, the storage hooks, the dispatcher that serves `/api/<format>/<repoKey>/…`, and how to add a format.
Requirements: [R-ARCH-005, R-ARCH-007 to R-ARCH-010, R-ARCH-014](../spec/architecture.md), [R-REPO-003](../spec/repositories-and-proxy.md),
[R-EP-018](../spec/endpoints.md), the hooks table of [storage.md](../spec/storage.md). Decisions:
[Q-ARCH-188, Q-ARCH-189, Q-ARCH-193, Q-ARCH-211 to Q-ARCH-217](../spec/architecture.md#decisions), [Q-GENERIC-7](../spec/formats/generic.md).

The conformance kit (`internal/formats/formattest`) and the resource limits and readers for untrusted content (`internal/formats/untrusted`) have
their own sections below: [Conformance kit](#conformance-kit) and [Untrusted content](#untrusted-content).

## What has to be in your head first

- **A format is a Go package that implements `formats.Format` and imports almost nothing.** The archtest allowlist
  ([imports.go](../../internal/archtest/imports.go)) lets a format import the contract, `formats/catalog`, `formats/untrusted`, `errs`,
  `config` and `observability`. Core types reach it as aliases of the contract (`formats.Action`, `formats.Path`, `formats.Tx`) and
  as fields of the request (`rq.Subject`, `rq.Index`, `rq.Repo`), so it never names `authz`, `repository` or `meta`.
- **A format runs no SQL.** Its generic package rows go through the `PackageWriter` the event or `Host.Write` hands it; its own side
  tables go through a data port it declares (`Descriptor.DataPort`), implemented by a domain package
  (`internal/formatdata/<id>`) and bound by the composition root ([Q-ARCH-193](../spec/architecture.md#decisions)). The `Tx` it
  receives is opaque: it cannot unwrap it.
- **`packages.rev_seq` is the writer's job, not the format's.** Every `PackageWriter` call that writes generic rows raises the revision of
  that package, once per transaction ([Q-ARCH-212](../spec/architecture.md#decisions)). A change that writes only side rows calls
  `BumpRevision`.
- **The core owns the headers, the checks and the transaction.** `formats.ServeRoute` wraps every `Download`, `Metadata` and `Computed`
  route in the content headers of R-ARCH-007; the dispatcher runs the key shape, the clean path, the match, the index and the coarse
  check before a handler sees the request; `Host.Ingest().Commit` runs the hook in the item's transaction.
- **A format with no routes has no prefix.** generic's protocol is `/files/` ([Q-GENERIC-7](../spec/formats/generic.md)); `/files/` is the
  raw view of every repository and is served by the generic layer, not by the dispatcher.
- **`ContractVersion` is 1 until the freeze at the end of M3** ([Q-ARCH-213](../spec/architecture.md#decisions)): a change to the
  contract is applied to every format in the same change.
- **Unbound seams fail closed.** `Claim` and `AutoClaim` refuse until the composition root binds the ownership rule and the namespace auto
  claim ([Q-ARCH-215](../spec/architecture.md#decisions)); it binds both ([wire_formats.go](../../internal/app/wire_formats.go)), so a format with package
  names publishes under the rule. `AutoClaim` returns the `AutoClaimResult` that the publish's audit event records.
- **Per-replica state of a format hangs off the Host.** A format is registered once per process; a process may hold several replicas (the integration tests do).
  A format that reads `trove.yaml` or exports metrics implements `Bind(host, cfg, metrics)`, which the composition root calls with the Host it built
  (`hostBinder` in [wire_formats.go](../../internal/app/wire_formats.go)); the format keeps the result per Host, never in a package variable (npm: [bind.go](../../internal/formats/npm/bind.go)).

## Files

### `internal/formats` (the contract, edge layer)

| File | Responsibility |
|---|---|
| [formats.go](../../internal/formats/formats.go) | `ContractVersion`, the aliases and constants, `Format`, `Descriptor`, `Register` / `Validate` / `CatalogEntry`, `Lookup`, `List`, `Descriptors` |
| [route.go](../../internal/formats/route.go) | `Route`, `RouteKind`, `Params`, `Template`, `MatchRoutes`, `Request`, `EndpointFacts`, `Target`, `PathKind`, `PathInfo` |
| [host.go](../../internal/formats/host.go) | `Host` and its parts: `Ingestor`, `ItemStore`, `PackageStore`, `LinkBuilder`, `RenditionCache` |
| [hooks.go](../../internal/formats/hooks.go) | The storage-hook types: `RepoRef`, `ItemRef`, `CommitEvent`, `ItemsEvent`, `Inspection`, `PackageWriter`, `OwnershipWrite` |
| [remote.go](../../internal/formats/remote.go) | `RemoteAdapter`, `RemoteFetcher` and their types (the engine arrives with the first remote format) |
| [virtual.go](../../internal/formats/virtual.go) | `VirtualAdapter`, `MemberDocument`, `Resolver` and their types |
| [exposure.go](../../internal/formats/exposure.go) | `ExposureAdapter`, `IndexBuilder` |
| [setup.go](../../internal/formats/setup.go) | `Client`, `SetupTemplate`, `SetupData` |
| [serve.go](../../internal/formats/serve.go) | `ServeMode`, `ServeRoute`, `Base` |

### The packages around it

| Package | Responsibility |
|---|---|
| [internal/formats/catalog](../../internal/formats/catalog/) | The facts of every format, the name rule, the exposure facts (a leaf layer); `Register` fills it |
| [internal/formats/untrusted](../../internal/formats/untrusted/) | `Limits`, `DefaultLimits`, `Tighten`, and the bounded readers of R-ARCH-028 (see [Untrusted content](#untrusted-content)) |
| [internal/formats/formattest](../../internal/formats/formattest/) | The conformance kit: the in-memory `Host`, the ten checks, the fuzz helpers (see [Conformance kit](#conformance-kit)); `crafted/` generates the hostile inputs, `testformat/` is the kit's own format with one named defect per check |
| [internal/formats/generic](../../internal/formats/generic/) | The generic format: no routes, `Classify`, a paths-only exposure adapter |
| [internal/formats/all](../../internal/formats/all/all.go) | Registers every format at compile time (R-ARCH-010) |
| [internal/httpserver/content](../../internal/httpserver/content/content.go) | The content-header wrapper (`File`, `Index`, `Document`); `httpserver.ContentWriterFor` is the request-aware call |
| [internal/packages](../../internal/packages/) | The generic package rows: `Store` (reads), `Writer` (writes, raises `rev_seq` once per package per transaction), `RestoredAt`; statements in [packages.sql](../../internal/meta/postgres/queries/packages.sql) |
| [internal/formathost](../../internal/formathost/) | The `Host` ([host.go](../../internal/formathost/host.go)), the item-store adapters ([ingest.go](../../internal/formathost/ingest.go)), the `PackageWriter` adapter and the ownership seams ([writer.go](../../internal/formathost/writer.go)), the storage hooks and the files seam ([hooks.go](../../internal/formathost/hooks.go)), the data-port bindings ([bind.go](../../internal/formathost/bind.go)), links, renditions, per-request state, the stubs of the engines that do not exist yet, and the bridge from the virtual engine to `formats.Resolver` ([virtual.go](../../internal/formathost/virtual.go), [virtual.md](virtual.md)) |
| [internal/api/dispatch](../../internal/api/dispatch/) | The dispatcher: [dispatcher.go](../../internal/api/dispatch/dispatcher.go) (routing and the request path), [errors.go](../../internal/api/dispatch/errors.go) (the error shape of a prefix) |
| [internal/app/wire_formats.go](../../internal/app/wire_formats.go) | The wiring: hooks and files seam at step 8, Host and dispatcher at step 9, the data-port bindings, the catalog freeze |
| [internal/files/formats.go](../../internal/files/formats.go) | `files.Formats`, the seam the generic layer asks a format through (`ServeCheck` is called in `Stat`; `Classify` is declared for the exposure coordinate of R-EP-014) |

## The registry

`formats.Register(f)` (called from `internal/formats/all`) validates the format, puts its facts into `catalog.Default()` and keeps the format
for `Lookup` and `List`. It panics on:

- an invalid or reserved ID, a duplicate ID;
- a `ContractVersion` that is not the contract's;
- a class without its adapter (`Remote` listed and `Remote()` nil) or an adapter without its class;
- a `DataPort` that is no interface type;
- a route set that is not servable: a route without name, method, template, handler, kind, action or example; a name used twice; a route
  that does not match its own example; an example that another route of the same method (and an overlapping class set) also matches; routes under a protocol base
  path other than `/api/<id>/{key}/`; a route of a class the format does not support;
- every rule of the catalog (class set, layout, pattern subject, name rule, exposure).

`formats.Validate(f)` runs the contract checks without registering. The exposure facts of the catalog come from `Format.Exposure()`: no adapter
means not exposable, and the adapter's `Dimensions` and `catalog.Versioning` are what the decision point reads. The composition root
freezes the catalog ([wire_formats.go](../../internal/app/wire_formats.go) `wireFormats`) before the replica serves.

## The request path of a format route

```text
chain 1-7 (recovery, trace, log, in-flight, address and host, authentication, rate limit)
  -> dispatch.Dispatcher.ServeHTTP
     1  split /api/<id>/<key>/<rest>; key shape; rest stays escaped
     2  a family of its own below the prefix (npm's exchange) only where the endpoint's surface holds it
     3  repository.CleanPath(rest) -> 400 invalid_path            (before authorization)
     4  Format.Match(method, rest) -> route and params | 404 | 405 with Allow;  the route name labels the request
     5  edge.Resolve -> the request's holder: subject (with the endpoint), index, key made known
     6  coarse DecideOn: the route's action on the repository, kind from the route; the exposure ceiling first
     7  Repos.ByKey; deleting or another format -> 404 repository_not_found; disabled -> 404 repository_disabled
     8  a class the route does not serve -> a virtual write goes to Resolver().DeployTarget, else 405 with Allow
     9  formats.Request built, formathost.Prepare; formats.ServeRoute (content headers, recovery, WriteError); formathost.Finish closes the spools
```

Every error of steps 1 to 8 is rendered by the format's `WriteError`. The chain's own refusals (401, 421, 429, 503) on the prefix are rendered by
it too: [errors.go](../../internal/api/dispatch/errors.go) registers it with `Router.HandleErrorShape`, and `httpserver.Problem` consults the table by path
prefix ([router.go](../../internal/httpserver/router.go), [context.go](../../internal/httpserver/context.go)). A format's `WriteError` therefore must not call
`httpserver.Problem`.

The handler decides on the concrete package or path with `Host.Authorize(ctx, rq, Target{...})`: the host completes the coordinate (repository, class, format, the
request kind of the route), decides on `rq.Index`, then applies the repository's include and exclude patterns to the package name or the path (404 for a read,
403 `not_admitted` for a write). An allowed write decision is remembered with the request and recorded in the transaction of the write
(`Ingest().Commit` and `Host.Write`), where the generic layer records its own (R-AUTHZ-029).

## The storage hooks

The item store ([items.md](items.md)) runs `items.Hooks` inside the transaction of a commit and of a removal. `formathost.Hooks` is the implementation:

1. It finds the format of the event's repository (`RepoRef.Format`); a repository of an unregistered format has no hook.
2. It binds a `PackageWriter` to the transaction (property writes only below the format's `PropertyPrefix`) and fetches the format's data port.
3. It calls `OnCommit` (with `Origin` upload or fill, the inspection and payload of an upload, `Replaced`) or `OnItemsRemoved` (each removed item with the
   version it belonged to).

A route that removes the files of a version calls `PackageWriter.DeleteItems(repo, paths...)` from inside `Host.Write`: the host's writer hands the paths to
`items.Store.RemoveIn` in the same transaction, which tells `OnItemsRemoved` there, so the route's checks and the format's side rows commit with the removal (npm: [npm.md](npm.md), Q-NPM-27).
Only the writer of a request removes (`formathost.ItemRemover` on the host's item store); the writer of a hook answers `errs.Internal`.

The writer is bound to the request when the commit came through `Host.Ingest().Commit`; a fill or the `items-delete` job has no request, so `Claim` and `AutoClaim`
refuse there. The sync delete and every batch of the delete job run the removal hook, which is how a delete of an npm folder through `/files/` raises `rev_seq`.

`files.Formats` ([formats.go](../../internal/files/formats.go)) is the generic layer's seam: `FilesSeam` in [hooks.go](../../internal/formathost/hooks.go) implements it over the
registry. For generic both calls are no-ops, so `/files/` behaves as before; a format that tombstones a version makes the `/files/` GET of its tarball a 404.

## Content headers and error shapes

`formats.ServeRoute(f, w, rq, sameOrigin)` is the one function that runs a handler. It wraps the writer for a route of kind `Download` (file headers with
`Content-Disposition`), `Metadata` and `Computed` (the same set without it); a route of any other kind is not wrapped and must return no package-derived bytes
(kit check 7). It recovers a panic (log with format and route, 500 through `WriteError`, `http.ErrAbortHandler` passes), renders an error returned before the status
line through `WriteError` without `Content-Disposition`, and aborts the connection for an error after it. `sameOrigin` is true when a session cookie authenticated the request.

## Wiring

[wire_formats.go](../../internal/app/wire_formats.go) is the only wiring:

| Function | Moment | Does |
|---|---|---|
| `wireFormats` | step `formats` | freezes the catalog, binds and checks the data ports (`formatPorts` is where a format with side tables adds its constructor), builds the hooks and the files seam; also on a worker, whose delete job runs the hooks |
| `formatHost`, `buildFormatHost` | first use | builds the package store, the rendition cache (`cache.rendition_bytes`), the `Host` and the dispatcher, once |
| `registerFormats` | step 9, from `startClient` | mounts the prefix of every format with routes on each endpoint's router, with its error shape |

The composition root binds `formathost.Ownership` (over `authz.Ownership.EnforceTx`) and `formathost.AutoClaimer` (over `repository.Routing.AutoClaim`, which gets the caller of the
request and returns its `AutoClaimResult`) with two small adapters. It does **not** bind the remote engine and the resolver yet (`Host.Remote()` and `Host.Resolver()` answer an internal
error that says so; a virtual `DeployTarget` answers `405 no_deploy_target`; the stub's `ShadowCheck` lets a first publish proceed, because without the engine no remote member serves a name).
Each is a field of `formathost.Options` or `Deps`.

## Tests

| Where | What |
|---|---|
| [formats_test.go](../../internal/formats/formats_test.go), [serve_test.go](../../internal/formats/serve_test.go) | `Register`'s start-up checks (R-ARCH-010), `MatchRoutes`, `Template`, `ServeRoute` (R-ARCH-007, R-ARCH-023, R-ARCH-028) |
| [internal/httpserver/content](../../internal/httpserver/content/content_test.go) | The header sets of the three kinds and that the handler cannot weaken them |
| [dispatcher_test.go](../../internal/api/dispatch/dispatcher_test.go) | Routing, the order of the checks, the class rules, the chain's refusals in the format's shape (fakes, no database) |
| [formathost_test.go](../../internal/formathost/formathost_test.go) | Data-port bindings (Q-ARCH-193), `Authorize`, the fail-closed `Claim`, links, renditions |
| [format_enums_test.go](../../internal/api/v1/format_enums_test.go) | `TestR_ARCH_010_PerClassFormatEnums`: the OpenAPI enums equal the registry |
| [formattest](../../internal/formats/formattest/) (`host_test.go`, `kit_test.go`), [testformat](../../internal/formats/formattest/testformat/format_test.go), [crafted](../../internal/formats/formattest/crafted/crafted_test.go) | The in-memory `Host` against the rules of the production writer, the kit's skip and fixture rules, every check failing on its named defect, the generators producing what they claim |
| [internal/formats/untrusted](../../internal/formats/untrusted/) (`readers_test.go`, `internal_test.go`) | The bounded readers against the crafted inputs: 2 GiB manifest, 10 GiB zip, JSON 100,000 deep, a document type declaration |
| [generic/conformance_test.go](../../internal/formats/generic/conformance_test.go) | Generic runs the kit with reasoned skips; checks 2 and 3 run |
| [test/integration/files/formats_test.go](../../test/integration/files/formats_test.go) | Against PostgreSQL and the object store: `rev_seq` once per transaction, the hooks in the item's transaction, the dispatcher end to end, `ServeCheck` on the route and on `/files/`, the removal hook |

[probeformat_test.go](../../test/integration/files/probeformat_test.go) is a complete small format (routes, hooks, data port, exposure adapter, error shape) and the
best example to copy from.

## Conformance kit

[internal/formats/formattest](../../internal/formats/formattest/) is the table-driven test every format runs in its own package (R-ARCH-010). It needs no
PostgreSQL, no object store and no Valkey: `go test ./internal/formats/...` runs it. Decision: [Q-ARCH-216](../spec/architecture.md#decisions) (the kit
imports what the contract imports, so check 7 runs the real `formats.ServeRoute`).

### How a format runs it

```go
// internal/formats/<id>/conformance_test.go
func TestR_ARCH_010_Conformance(t *testing.T) { formattest.Run(t, Format{}, fixture()) }
func FuzzR_ARCH_028_Match(f *testing.F)       { formattest.FuzzMatch(f, Format{}) }
func FuzzR_ARCH_028_Inspect(f *testing.F)     { formattest.FuzzInspect(f, Format{}, "name/-/name-1.0.0.pkg", validUpload) }
```

`Run` makes one subtest per check, named `R-ARCH-010 check 7 content headers`. `Evaluate(f, fx)` runs the same checks and returns a `Report` without a
`testing.T`; the kit's own tests use it to assert that a defective format fails the check the spec names.
The fixture ([kit.go](../../internal/formats/formattest/kit.go)) holds what only the format can state. Everything else is derived from the format: its routes, their
`Examples`, its descriptor, its adapters, its setup templates.

| Fixture field | Used by | What goes in it |
|---|---|---|
| `Host` | all | `func() HostOptions`, called once per case: the class, the base URL, `Data` (the in-memory twin of the format's data port, bound and checked like the composition root does), `Allow`, `Claim`, `Remote` |
| `Rejects` | 2 | Further escaped paths `Match` must refuse |
| `Paths` | 3 | Escaped path and the `PathInfo` `Classify` must say; root, folder and a scoped or odd name belong here |
| `Remote` | 4 | The upstream origin, upstream documents (`RemoteDoc`), and `DownloadLinks(doc)`; at least one document must link to the upstream |
| `Merge` | 5 | Member documents in the core's order, one source marker per part, `Versions(doc)` and `VersionSource(merged, v)` to read the marker back |
| `Inspect` | 6 | Crafted uploads: `InspectTarGz(path, manifestEntry)`, `InspectZip`, `InspectJSON`, `InspectXML` give the standard cases; add your own `InspectCase` with a `crafted.Input` |
| `Content` | 7 | A request per `Download`, `Metadata`, `Computed` and `Volatile` route, with a `Seed` that stores what it reads |
| `Changes` | 9 | A `ChangeCase` per storage hook and per `Upload`, `Delete` or `Admin` route; `Do` drives the host (`h.Serve`, `h.Ingest().Commit`, `h.Remove`, `h.Restore`) |
| `NoRevision` | 9 | A route of those kinds that changes no package row, with the reason |
| `Names` | 10 | Spellings of one package name: they must normalize to one name and lead to one document path |
| `OptionNames` | 8 | Literal client option names a template may spell although they contain `token`, `password` or `_auth` |
| `Skip` | all | The checks that do not apply, each with the reason |

### The ten checks

| # | The kit does | Fails when |
|---|---|---|
| 1 | `formats.Validate` and the catalog's rules (what `Register` runs), then `Match(method, example)` for every example of every route | An example is unmatched, matched by two routes (`/{package}` and `/{scope}/{name}` without a disambiguating rule), or resolves to another route |
| 2 | The traversal inputs (`..`, `%2e%2e`, `%2E%2e%2f`, NUL, `%00`, `\x01`, backslash, malformed escapes, 4,097 bytes, a 256-byte segment), `Rejects`, and every route example mutated with them, through `Match`; runs for a format without routes too | Any of them resolves to a route, or `Match` panics |
| 3 | `Classify` over every example (a `Download` route is `Content`, a `Metadata` or `Computed` route is never `Content`, a `Volatile` route is `PathVolatile`) and over `Paths` | A missing kind, a wrong kind, a path case that differs |
| 4 | `Rewrite` twice on a copy of each document, then on its own output | A different output, a changed input, an output that rewriting changes again, an upstream host left in a download link; a fixture without an upstream link |
| 5 | `Merge` twice; for each version, the source must be the lowest (tier, position) part that holds it | Another member wins a version, or the output differs |
| 6 | Every crafted input through `Inspect` with the effective limits (`untrusted.DefaultLimits` tightened by the descriptor): `413` or `422` within 5 s, heap sampled with `runtime/metrics` within 512 MiB of the baseline | Success, another kind, a timeout (the kit cancels the context), the heap bound, a panic |
| 7 | Every content case through `formats.ServeRoute`, once with and once without a session cookie, with the handler wrapped so that it deletes the content headers before and while it writes; the headers are read at the status line and compared with `content.Headers` | A header missing or weakened; `Content-Disposition` on a document; a route of another kind that served an item or whose body holds package bytes it read; a route without a case |
| 8 | Every client has a `configure` and an `install` template; every template parses and executes over `SetupData`; no template text matches `(?i)token\|password\|_auth` outside the whitelisted option names | A missing section, an unknown client, a duplicate ID, a bad language or section, a credential |
| 9 | Each `ChangeCase` against the in-memory host: `rev_seq` of its package before and after | The revision did not rise; a route of kind `Upload`, `Delete` or `Admin` without a case or a `NoRevision` entry |
| 10 | For each spelling: normalize, `PackageDocumentPath`, escape, `Match("GET", …)`; all spellings of one name | A name the format rejects, spellings that normalize differently or lead to two paths, a path that matches no `GET` route |

A skip without a reason fails the check. So does a skip the format contradicts: checks 1, 3 and 7 with routes, 4 with a remote adapter, 5 with a virtual
adapter, 8 with clients, 10 with package names, and check 2 always. A check with nothing to run over and no skip fails: that is how a later generic remote
class cannot pass vacuously ([generic's test](../../internal/formats/generic/conformance_test.go) shows the skips of a format with no routes).

### The in-memory host

[`formattest.NewHost(f, HostOptions)`](../../internal/formats/formattest/host.go) implements `formats.Host`: items ([store.go](../../internal/formats/formattest/store.go)), package rows
with `rev_seq`, links on a fixed base URL (`DefaultBaseURL`), renditions, notifications and jobs as logs, an authorization table (`Allow`, with every call recorded),
the format's data port, and `Remote` and `Resolver` from the options (`MemRemote` is a ready remote over fixed documents; unset, they answer "not supported" like the
production host before an engine is bound). It mirrors production where a format can tell the difference:

- the package writer raises `rev_seq` once per package per transaction, however many calls touch it; `EnsureLocked` of a new package starts at 1 and does not bump an
  existing one; `BumpRevision` is idempotent within the transaction (Q-ARCH-212);
- a transaction (`Write`, `Ingest().Commit`, `Remove`, `Restore`, `Seed`) is undone as a whole when its function or a `Before` hook fails: rows, notifications and jobs vanish;
  a commit sends the notifications and runs the `After` hooks;
- `Claim` on a writer bound to no request (`Seed`) answers an internal error; `SetProperties` refuses a key outside the descriptor's `PropertyPrefix`;
- `Ingest().Commit` of the same content at the same path is an identical commit that runs no hook; other content without `MayOverwrite` is refused;
- `Serve` runs the format's `ServeCheck`, and `Host.Serve(ctx, method, path, body, sameOrigin)` runs a request the way the dispatcher does (`Match`, the request, `ServeRoute`).

A seed that stores state without a hook: `h.PutItem(path, mediaType, data)`. A seed that goes through the hooks: `h.Ingest().Commit` or the format's own upload route.
Reads see the effects of a transaction as it runs (there is no isolation between a running transaction and a reader); production reads the committed state.

### Crafted inputs

[crafted](../../internal/formats/formattest/crafted/crafted.go) generates each input from a few bytes on demand, so a test holds none of the gigabytes it pretends to carry:
`TarGzEntry(name, size)` (a regular file that declares and holds `size` zeros), `TarGzEntries(n)`, `TarGzDuplicates(name)`, `TarGzSymlink(name, target)`, `ZipBomb(name, size)`
(a zip64 archive with a correct CRC, addressed by offset and never held in memory; it is 10.6 MB long for 10 GiB), `ZipEntries(n)`, `DeepJSON(depth)`, `WideJSON(n)`, `XMLDoctype()` (entities that expand
into each other), `DeepXML(depth)`. An `Input` is a stream (`Open`) and, for an archive, a `ReaderAt`; `formattest.SpoolOf(in)` serves it as the spool of an upload.

To add a crafted input: write the generator in `crafted.go` (a `pull` reader or a `segments` reader keeps it streaming), test in `crafted_test.go` that it is what it claims (the
standard library reads it, the size and CRC are right), then add an `InspectCase` to the standard set in [kit.go](../../internal/formats/formattest/kit.go) if every format of that archive type
should meet it, or to one format's fixture if it is specific. A format-specific ambiguity (`TarGzDuplicates`, `TarGzSymlink`) is not in the standard set: the spec asks 413 or 422 of the
kit's cases, and a format may answer such an input with its own 400.

### The test format and its defects

[testformat](../../internal/formats/formattest/testformat/format.go) is a complete small format that passes all ten checks (`testformat.New()` with `testformat.Fixture()`), and a named
`Defect` per check that breaks the rule the check exists for (`AmbiguousRoutes`, `CleaningMatch`, `ContentMetadata`, `NondeterministicRewrite`, `UpstreamLeak`, `MergeLastWins`,
`InspectAccepts`, `InspectWrongKind`, `InspectHeap`, `TarballVolatile`, `TemplateCredential`, `NoInstallTemplate`, `TagWithoutRevision`, `NamesCaseSensitive`,
`WrongDocumentPath`). [format_test.go](../../internal/formats/formattest/testformat/format_test.go) asserts that each defect fails the check the spec names, plus the checks that cannot
stand without it (routes that are not told apart cannot be served), and no other: these are the acceptance criteria of R-ARCH-010 and R-ARCH-007. To add a rule to the kit, add the defect first
and watch the test fail.

### Fuzzing

`FuzzMatch(f, format)` seeds from the route examples, their traversal mutations and the kit's inputs; for any method and path `Match` must not panic, answer only not found, method not
allowed (with `Allow`) or invalid path, never resolve a path that `repository.CleanPath` refuses, and answer the same twice. `FuzzInspect(f, format, path, seeds...)` runs `Inspect` with the
kit's tightened limits: it must not panic, finish within its deadline, fail only with a client error (never `Internal`, never unclassified) and return properties below the descriptor's
`PropertyPrefix` only. Run one with `go test -run '^$' -fuzz FuzzR_ARCH_028_Match -fuzztime 2m ./internal/formats/formattest/testformat/`.

### What the kit does not see

- Which hooks a format implements: `formats.Base` supplies no-op hooks, so the kit cannot tell an overridden `OnCommit` from the default. The fixture must hold a change case for each hook the
  format overrides; a hook with no case is a gap nobody reports.
- That a route handler decides authorization on the right target: the host records every `Authorize` call (`h.Authorizations()`), and a format's own test asserts on them.
- The dispatcher's order of checks and the error shapes of the chain: [dispatcher_test.go](../../internal/api/dispatch/dispatcher_test.go).
- Retried transactions: `Write` runs the function once; a format with a non-transactional twin of its data port must not depend on a rollback.
- A leak of package bytes through a `Volatile`, `Upload`, `Delete` or `Admin` route that the kit detects only when the bytes appear unchanged in the body or the route served an item.

## Untrusted content

[internal/formats/untrusted](../../internal/formats/untrusted/) is the one place that enforces R-ARCH-028 for every format. It imports the standard library and `errs`; a format may import it.
Every reader takes a `Limits` and treats an unset field as the default (`DefaultLimits().Tighten(l)`), so a zero `Limits` is never unbounded; a format can only tighten
(`Descriptor.Limits`, `Tighten`). A `Limits` field that is crossed is `untrusted.TooLarge` (413); a structure the readers refuse is `untrusted.Refused` (422, code `content_refused`).
A format maps both to the codes of its protocol; `IsTooLarge` and `IsRefused` classify. Decision: [Q-ARCH-217](../spec/architecture.md#decisions).

| File | Responsibility |
|---|---|
| [limits.go](../../internal/formats/untrusted/limits.go) | `Limits`, `DefaultLimits`, `Tighten` (never loosens), `Bound` (the document cap: `DocumentBytes`, else `ManifestEntryBytes`) |
| [bounded.go](../../internal/formats/untrusted/bounded.go) | `Limit` and `ReadAll` (fail with `TooLarge` instead of cutting), `WithContext`, `Limits.Context` (the deadline) |
| [json.go](../../internal/formats/untrusted/json.go) | `CheckJSON` (one pass over the bytes for nesting depth and number of values, strings skipped), `DecodeJSON`, `UnmarshalJSON` |
| [xml.go](../../internal/formats/untrusted/xml.go) | `CheckXML` (any declaration, so any document type declaration, is refused; nesting depth; well-formedness; UTF-8 only), `DecodeXML`, `UnmarshalXML` |
| [tar.go](../../internal/formats/untrusted/tar.go) | `NewTarGz`, `TarReader` (`Next`, `Read`, `ReadEntry`), `CountTarGz` |
| [zip.go](../../internal/formats/untrusted/zip.go) | `NewZip`, `ZipReader` (`Entries`, `ReadEntry`) |

What the archive readers enforce, and what stays with the format:

| Rule | tar.gz | zip |
|---|---|---|
| Entry count | `Entries`, at the header | `Entries`, from the end record, before the standard library parses the directory |
| Declared bytes of all files | `StreamBytes`, at the header | `StreamBytes`, at open |
| Bytes read into memory | `UncompressedBytes` in total; `ReadEntry(max)` refuses a declared size above `max` (default `ManifestEntryBytes`) before inflating | the same, per `ReadEntry` |
| Inflated bytes | capped at `StreamBytes` plus a per-entry overhead, skipped content included | cut at the declared size; a stream that disagrees is refused |
| Ratio | `Ratio` per entry read, measured on the compressed bytes the inflater consumed, with 128 KiB of read-ahead allowance | `Ratio` on the declared sizes, before inflating |
| Names | A name used twice (a directory's trailing slash is no second name) is refused | The same, and the central directory's name must equal the local header's |
| Entry types | Links, devices, sparse files refused | Links, encrypted entries and methods other than store and deflate refused |
| After the archive | Only zero padding, a clean gzip trailer and no second member | No bytes in the central directory its entries do not account for |

What a format still does itself: the rules of its own protocol (npm's single first path component, case-folded `package.json`, link targets: [tarball](../../internal/formats/npm/tarball/tarball.go) implements them with
its own finite limits, as R-ARCH-028 allows a streaming pass), which entry is the manifest, and the mapping of the two error kinds to its codes. `CountTarGz` is the generic form of the counting pass
(`dist.unpackedSize`); npm's own pass stays in `tarball`. Names are never file-system paths: nothing in the package writes to disk.

What is not covered: an archive in several parts is refused, not read; the zip reader takes sizes from the central directory and does not look at a local header's own sizes or data descriptor;
an entry whose compressed stream is longer than the central directory says is read only up to the declared compressed size by the standard library and refused as damaged (this path is not covered by a
test of its own).

## How to add a format

1. Write the spec page `docs/spec/formats/<id>.md` (routes, grammar, error shape, hooks, tables) and its decisions, in the numbers the owning plan assigns.
2. Create `internal/formats/<id>/` with a type that embeds `formats.Base` and implements `Descriptor` (facts, `ContractVersion: formats.ContractVersion`,
   `Names` if it has package names, `PropertyPrefix`, `DataPort` if it has side tables), `Routes`, `Match` (one call of `formats.MatchRoutes`), `Classify` and
   `PackageDocumentPath`, then what it needs of the hooks, the adapters (`Remote()` exactly when `Classes` lists `Remote`, `Virtual()` likewise), `Exposure()` and `WriteError`.
   Give every route at least one example; a name that two routes could both match needs a `Route.Match` that tells them apart.
3. If it has side tables: add the migration ([database.md](database.md)), the port interface in the format package, the implementation in
   `internal/formatdata/<id>` (domain layer, sqlc statements in `queries/`), and its constructor in `formatPorts` in
   [wire_formats.go](../../internal/app/wire_formats.go).
4. Register it in [internal/formats/all/all.go](../../internal/formats/all/all.go).
5. Add the format's enum entries to `api/openapi.yaml` (`FormatName` and the schemas of its classes, the block `# >>> WP2.1 formats`);
   `TestR_ARCH_010_PerClassFormatEnums` fails until they match, then `make generate`.
6. Write the conformance test with the kit (`formattest.Run`), the fixture of the format and its fuzz targets ([Conformance kit](#conformance-kit)), and the integration tests against the real stack.
7. Update this page, [repositories.md](repositories.md) (the format registry section) and the README's pages table.

## What this page does not cover

- The kit does not run the dispatcher, the authorization decision or the real item store: it runs the format against an in-memory `Host`. That the production host
  behaves like the in-memory one is tested by the integration tests of `test/integration/files`, not by the kit.
- The remote engine, package ownership and the namespace auto claim: the contract has their types and the host their seams; the engines
  and rules are built with the first format that needs them. The virtual engine exists ([virtual.md](virtual.md)) and the Host takes it as `Options.Resolver`
  through `formathost.NewResolver`; `MemberDocument.Restricted` and `ShadowOutcome.Refusal`/`Shadowed` are the two fields it added to the contract.
