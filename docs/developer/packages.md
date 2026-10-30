# The package screens' API: read model, README, client setup

R-API-029 (packages, versions, README) and R-API-030 (client setup snippets) are the API behind the package list, the package page, the install panel and
the client setup page of the admin UI ([web-ui.md](web-ui.md#package-screens-and-client-setup)). The contract is the block `M2 packages` of
[api/openapi.yaml](../../api/openapi.yaml); the handlers are [packages_api.go](../../internal/api/v1/packages_api.go) and
[setup_snippets.go](../../internal/api/v1/setup_snippets.go). The decisions are Q-API-164 to Q-API-168 and Q-UI-75 to Q-UI-77 in
[api-and-ui.md](../spec/api-and-ui.md).

## What has to be in your head first

- **A package of the API is a format and a normalized name, across repositories.** The generic rows are per repository (`packages (repo_id, name)`); the
  view aggregates them. Its id is derived, not stored (`view.ID`, Q-API-164).
- **Every aggregate counts only what the caller may read, and the filter is in the statement that counts** (Q-API-165). The decision point classifies each
  repository for the request; the statements take the result as arrays.
- **The core knows no format.** What the generic rows do not hold (description, keywords, licenses, metadata, README) and the order of versions come from the
  format's `ViewAdapter` over its data port (Q-API-166).
- **The server never builds an address from the `Host` of a request**, and never puts a token into a snippet (Q-API-168).
- **A hidden thing and a missing thing look the same**: `404`, the same problem body.

## Files

| Path | Responsibility |
|---|---|
| [internal/api/v1/packages_api.go](../../internal/api/v1/packages_api.go) | The seven handlers; `PackageServices`; the request's scope (`packageScope`); `writeTagged`; the download URL; `GetClientSetup` and `publishTarget` |
| [internal/api/v1/setup_snippets.go](../../internal/api/v1/setup_snippets.go) | Runs a format's `SetupTemplate`s: the credential modes, the package substitution, `shellQuote`, the snippet ids, the token-shape check |
| [internal/packages/view/](../../internal/packages/view) | The read model (domain layer): `Service` (`Scope`, `List`, `Package`, `Versions`, `Version`, `ReadmeOf`), `Topology` (which virtual repository contains which), `ID`/`ParseID`, the `Adapter` interface and the value types |
| [internal/packages/readme/](../../internal/packages/readme) | The README renderer: goldmark, bluemonday, the finishing pass, `HeadingSlug`, `Version`, `PolicyHash` |
| [internal/authz/visible.go](../../internal/authz/visible.go) | `ReadScoper`, `ReadScope`, `ReadShape` (`ReadAll`, `ReadNone`, `ReadRows`): the classification of a repository for a listing |
| [internal/formats/view.go](../../internal/formats/view.go) | The contract: `ViewAdapter`, `ViewVersion`, `ViewFields`, `ViewData`, `ViewFind`, `DataPorts`, `Base.View()` |
| [internal/formathost/view.go](../../internal/formathost/view.go) | `NewViewAdapters`: bridges the registered formats' adapters to `view.Adapters` (the domain package imports no format) |
| [internal/formats/npm/view.go](../../internal/formats/npm/view.go), [internal/formatdata/npm/view.go](../../internal/formatdata/npm/view.go) | npm's adapter and its reads ([npm.md](npm.md#the-package-view)) |
| [internal/meta/postgres/queries/package_view.sql](../../internal/meta/postgres/queries/package_view.sql), [npm_view.sql](../../internal/meta/postgres/queries/npm_view.sql) | The statements |
| [internal/config/ui.go](../../internal/config/ui.go) | `ui.readme.*` and `ui.client_setup.*` ([configuration.md](configuration.md)) |
| [internal/app/wire_packages.go](../../internal/app/wire_packages.go) | The step `packages-api` (`wirePackagesAPI`, a role with a listener): the view over the registered adapters, the engine as classifier, the renderer with the operator's origins. A part that cannot be built (the renderer under `ui.readme.*`) stops the start-up; the operations are never served as `503` by a replica that is ready (Q-ARCH-230). `requireServing` names the package services |

## The request path of a list

1. `ParseList` reads the filters, the sort and the cursor; an unknown parameter or sort is `400`, a cursor of other filters `400 invalid_cursor`.
2. `packageScope`: the anonymous gate (401 while anonymous access is off), the snapshot of the repositories (`Reader.List`) for the `Topology`, the request's
   index (`requestHolder`, so every question of the request is answered on one index), `ReadScopeOn` for the physical repositories, `Readable` for the
   virtual ones, then `view.Service.Scope`.
3. `Scope` takes the repositories that are `ReadAll` and have no include or exclude patterns (`Repository.Restricts`, R-REPO-005) as a list of keys. For every other repository that is not `ReadNone` it reads their version triples (`PackageViewCandidates`,
   at most 200 000, narrowed by format and name prefix), keeps the ones the repository's patterns admit (`Topology.Admits`) and the caller may read (`ReadScope.MayRead`; skipped for a `ReadAll` repository) as `repo US name US version` strings.
4. `Service.List` asks the adapters for the versions that match `q` and `keyword` (`Find`) and for the withheld versions (`Withheld`), then runs
   `PackageViewList`: the CTE `vis` holds the versions that are live, not withheld, in the scope and inside the filters; `grouped` aggregates per format and
   name; the keyset condition and the order depend on the sort. Names are compared with collation `C`.
5. For the packages of the page it reads their visible version rows (`PackageViewVersions`), takes the latest version by the adapter's order, and asks the
   adapter for the description and keywords of that version.

The details (`Service.Version`) read the visible rows of the package, take the oldest copy as the primary one, ask the adapter for summary and details with the
visible set of versions, and read the files of the primary version. `ReadmeOf` asks for details and README. The repositories of a version are the physical
repositories that hold it plus the virtual repositories that contain one of them and that the caller may read, local first.

Two facts that are easy to get wrong: a Go `nil` slice reaches the driver as `NULL`, and `x = ANY(NULL)` under a `NOT` drops every row, so `keys` and `ids` in
[service.go](../../internal/packages/view/service.go) turn them into empty arrays; and `ReadAll` is only returned when nothing narrows (no pattern in the
grant, no scope element, an endpoint in mode `all`), because a repository wrongly classified `ReadAll` is a leak, while one classified `ReadRows` costs time.

## The README

`readme.Renderer.Render` runs goldmark (CommonMark, tables, strikethrough, autolinks, task lists, raw HTML), bluemonday with the allowlist of R-UI-008, and
`finish`, a pass over the parsed fragment that applies what an allowlist cannot say: link targets and `rel`, the image policy, disabled checkboxes and
the `user-content-<slug>` heading anchors. The limits are the source (cut), the HTML (re-rendered from a proportionally shorter source) and two seconds
(abandoned for the escaped text); each sets `truncated`. The ETag is computed from the source, `readme.Version` and `PolicyHash()` before rendering. Raise
`Version` whenever a change makes a source render differently. `HeadingSlug` implements JavaScript's `trim`, `\s` and `toLowerCase` on purpose (Q-UI-76).

## Client setup

`renderSnippets` takes the format's templates of one client and runs each with `text/template` over `formats.SetupData` and the functions `shell` and
`xml`; then `applyCredentials` rewrites `formats.SetupTokenRef` by the mode and `substitutePackage` fills `formats.SetupPackageRef` quoted for the language of the
snippet. Publish templates are rendered only for a caller who may deploy to the target. `GetClientSetup` refuses an unknown client (the error lists the clients of
the format), a value above 256 bytes, with a control character or in the shape of a token, a `package` or `version` that starts with `-`
(a client reads it as an option, Q-NPM-50) and a `package` the format's name rule (`Descriptor().Names`) does not take; the snippet holds the
normalized name, and `shellQuote` quotes any value that starts with `-`. It answers `503` without a configured base URL. The registry URL is
the base URL plus the format's `protocolBasePath`; `endpointPath` is the same without its scheme.

## Tests

| Tier | Where | What |
|---|---|---|
| Unit | [render_test.go](../../internal/packages/readme/render_test.go) | The XSS corpus (26 vectors), raw HTML, GitHub-style Markdown, links, images by policy, origin validation, limits, the fallback, the heading corpus |
| Unit | [view_test.go](../../internal/packages/view/view_test.go), [visible_test.go](../../internal/authz/visible_test.go) | Ids, topology, latest version, cursors; the classification and its agreement with `DecideOn` over a matrix |
| Unit | [setup_snippets_test.go](../../internal/api/v1/setup_snippets_test.go), [packages_api_test.go](../../internal/api/v1/packages_api_test.go) | Modes, quoting, ids, publish gate, token refusal, `503` without the read model or a base URL, tags |
| Unit | [view_test.go](../../internal/formats/npm/view_test.go), [view_test.go](../../internal/formathost/view_test.go), [ui_test.go](../../internal/config/ui_test.go) | npm's metadata members, the bridge, the configuration |
| Integration | [test/integration/packages](../../test/integration/packages) | The in-process harness with npm publishes as data: list, filters, every sort walked by cursor, visibility for six callers, a repository with exclude patterns, versions, details, tags, README, setup, an endpoint with rules |
| Web | `server-address.spec.ts`, `package-readme.spec.ts` | The base URL from the formats answer; the heading corpus |

Run the area with `make test-integration` or, for this one area, the invocation of [build-and-test.md](build-and-test.md) with `./test/integration/packages/...`.

## Extension checklists

**A format gets package screens.** (1) `View()` returning an adapter, in `internal/formats/<id>/view.go` ([formats.md](formats.md#the-package-view)); (2) the reads in
`internal/formatdata/<id>/view.go` and the statements in `queries/<id>_view.sql`, behind the format's port; (3) `Descriptor.Names` (the view refuses a format without
package names) and, for the lookup, the normalization the format's name rule gives; (4) setup templates (`SetupTemplates`) with `SetupTokenRef` and `SetupPackageRef`, and
the clients in the UI table (`web/src/app/features/setup/clients.ts`) under the format's own ids; (5) tests in the format's package and a publish-based case in
`test/integration/packages`.

**A field is added to a package or version answer.** Edit the schema in `api/openapi.yaml` (both copies: `make generate` refreshes `internal/api/v1/openapi.yaml`
and the generated server), then `view.Package`/`VersionDetail`, the statement, the handler mapping, `npm run generate:api`, the UI fake and the specs.

## What is not built

- The README cache table of R-API-029 (Q-API-167, [H-298](../security/api-edge.md#h-298--a-readme-is-rendered-on-every-request-without-a-tag-and-a-rendering-that-runs-over-its-time-limit-is-not-stopped)).
- Images of files embedded in a package (P1, R-ARCH-029).
- Publish and CI snippets for npm: the npm templates are configure and install only; the API renders a publish template where a format supplies one.
- A decision per row inside the statement: the rows of a narrowed repository are decided in Go ([H-299](../security/permissions.md#h-299--the-cost-of-a-package-list-for-a-caller-with-row-level-grants-depends-on-versions-the-caller-cannot-read)).
- Verified in the integration area: the visibility of six callers, the sorts, filters, cursors, tags and setup answers of npm. Not verified: another format, a virtual
  repository with remote members holding cached versions, and a list above the bound of 200 000 rows decided in Go (a unit test checks the refusal's building blocks only).
