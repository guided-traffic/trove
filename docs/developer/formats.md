# The format contract

How a package format plugs into trove: the contract in [internal/formats](../../internal/formats/), the registry, the `Host` a format
reaches the core through, the storage hooks, the dispatcher that serves `/api/<format>/<repoKey>/…`, and how to add a format.
Requirements: [R-ARCH-005, R-ARCH-007 to R-ARCH-010, R-ARCH-014](../spec/architecture.md), [R-REPO-003](../spec/repositories-and-proxy.md),
[R-EP-018](../spec/endpoints.md), the hooks table of [storage.md](../spec/storage.md). Decisions:
[Q-ARCH-188, Q-ARCH-189, Q-ARCH-193, Q-ARCH-211 to Q-ARCH-215](../spec/architecture.md#decisions), [Q-GENERIC-7](../spec/formats/generic.md).

The conformance kit (`internal/formats/formattest`) and the resource limits (`internal/formats/untrusted`) are built by their own work
package and are described here only where the contract meets them.

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
- **Unbound seams fail closed.** Package ownership and the namespace auto claim are not wired yet; `Claim` and `AutoClaim` refuse until the
  composition root binds them ([Q-ARCH-215](../spec/architecture.md#decisions)).

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
| [internal/formats/untrusted](../../internal/formats/untrusted/) | `Limits`, `DefaultLimits`, `Tighten` (R-ARCH-028); the bounded readers come with the kit's work package |
| [internal/formats/generic](../../internal/formats/generic/) | The generic format: no routes, `Classify`, a paths-only exposure adapter |
| [internal/formats/all](../../internal/formats/all/all.go) | Registers every format at compile time (R-ARCH-010) |
| [internal/httpserver/content](../../internal/httpserver/content/content.go) | The content-header wrapper (`File`, `Index`, `Document`); `httpserver.ContentWriterFor` is the request-aware call |
| [internal/packages](../../internal/packages/) | The generic package rows: `Store` (reads), `Writer` (writes, raises `rev_seq` once per package per transaction), `RestoredAt`; statements in [packages.sql](../../internal/meta/postgres/queries/packages.sql) |
| [internal/formathost](../../internal/formathost/) | The `Host` ([host.go](../../internal/formathost/host.go)), the item-store adapters ([ingest.go](../../internal/formathost/ingest.go)), the `PackageWriter` adapter and the ownership seams ([writer.go](../../internal/formathost/writer.go)), the storage hooks and the files seam ([hooks.go](../../internal/formathost/hooks.go)), the data-port bindings ([bind.go](../../internal/formathost/bind.go)), links, renditions, per-request state, the stubs of the engines that do not exist yet |
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
freezes the catalog ([wire_formats.go](../../internal/app/wire_formats.go) `formatsDeps`) before the replica serves.

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
| `formatsDeps`, `formatHooks` | step 8, from `wireFiles` | freezes the catalog, binds and checks the data ports (`formatPorts` is where a format with side tables adds its constructor), builds the hooks and the files seam; also on a worker, whose delete job runs the hooks |
| `formatHost`, `buildFormatHost` | first use | builds the package store, the rendition cache (`cache.rendition_bytes`), the `Host` and the dispatcher, once |
| `registerFormats` | step 9, from `startClient` | mounts the prefix of every format with routes on each endpoint's router, with its error shape |

The seams the composition root does **not** bind yet: `formathost.Ownership` (the package-ownership rule, `EnforceTx`), `formathost.AutoClaimer` (the namespace auto claim), the
remote engine and the resolver (`Host.Remote()` and `Host.Resolver()` answer an internal error that says so; a virtual `DeployTarget` answers `405 no_deploy_target`).
Each is a field of `formathost.Options` or `Deps`.

## Tests

| Where | What |
|---|---|
| [formats_test.go](../../internal/formats/formats_test.go), [serve_test.go](../../internal/formats/serve_test.go) | `Register`'s start-up checks (R-ARCH-010), `MatchRoutes`, `Template`, `ServeRoute` (R-ARCH-007, R-ARCH-023, R-ARCH-028) |
| [internal/httpserver/content](../../internal/httpserver/content/content_test.go) | The header sets of the three kinds and that the handler cannot weaken them |
| [dispatcher_test.go](../../internal/api/dispatch/dispatcher_test.go) | Routing, the order of the checks, the class rules, the chain's refusals in the format's shape (fakes, no database) |
| [formathost_test.go](../../internal/formathost/formathost_test.go) | Data-port bindings (Q-ARCH-193), `Authorize`, the fail-closed `Claim`, links, renditions |
| [format_enums_test.go](../../internal/api/v1/format_enums_test.go) | `TestR_ARCH_010_PerClassFormatEnums`: the OpenAPI enums equal the registry |
| [test/integration/files/formats_test.go](../../test/integration/files/formats_test.go) | Against PostgreSQL and the object store: `rev_seq` once per transaction, the hooks in the item's transaction, the dispatcher end to end, `ServeCheck` on the route and on `/files/`, the removal hook |

[probeformat_test.go](../../test/integration/files/probeformat_test.go) is a complete small format (routes, hooks, data port, exposure adapter, error shape) and the
best example to copy from.

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
6. Write the conformance test with the kit (`formattest.Run`), the fixture of the format and its fuzz targets, and the integration tests against the real stack.
7. Update this page, [repositories.md](repositories.md) (the format registry section) and the README's pages table.

## What this page does not cover

- The conformance kit, the in-memory host and the crafted inputs: `internal/formats/formattest` and `internal/formats/untrusted` (their own work package).
- The remote engine, the virtual engine, package ownership and the namespace auto claim: the contract has their types and the host their seams; the engines
  and rules are built with the first format that needs them.
