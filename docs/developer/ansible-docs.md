# Ansible Galaxy: the documentation of collections

The documentation of a collection version (the README, the Markdown files of `docs/`, and one entry per module, plugin and role) is derived
data that trove generates for a local repository, keeps for a remote one and reads from the winning member for a virtual one. The
requirements are [R-ANS-033 to R-ANS-046](../spec/formats/ansible.md), the screens are R-UI-060 to R-UI-064
([api-and-ui.md](../spec/api-and-ui.md)), the decisions [ADR-196](../decisions.md) and [ADR-197](../decisions.md), and the threats
[docs/security/ansible-documentation.md](../security/ansible-documentation.md). The work-package cut is
[galaxy-docs-work-packages.md](../projektplan/galaxy-docs-work-packages.md).

## What is built and what is not

Work package WP0 (the contracts) is built. The generator, the converter, the upstream parser, the SQL of the port, the handlers, the remote
fill and the screens are not: each of them answers or does nothing as the table says, so nothing is silently empty.

| Part | Where | State |
|---|---|---|
| The neutral form (`DocSet`, `DocEntry`, `DocFile`, `DocState`, `DocTree`, `DocQuery`), the file title rule | [rows/docs.go](../../internal/formats/ansible/rows/docs.go), [rows/doctitle.go](../../internal/formats/ansible/rows/doctitle.go) | built |
| The port group `DocsStore` of `ansible.Store` | [store.go](../../internal/formats/ansible/store.go) | contract |
| Its in-memory twin | [ansibletest/docs.go](../../internal/formats/ansible/ansibletest/docs.go) | built (states, tree with the search rule, entries, files) |
| Its PostgreSQL implementation | [docs_store.go](../../internal/formatdata/ansible/docs_store.go) | every method answers `not_implemented` |
| The four tables | migrations `ansible_doc_tables` and `ansible_doc_fetches` in [migrations](../../internal/meta/postgres/migrations), registered `Transient` (not exported) in [doc_tables.go](../../internal/formatdata/ansible/doc_tables.go) | built |
| The generator and the markup converter | [docgen](../../internal/formats/ansible/docgen/docgen.go) | signatures; `Wants` wants nothing, `Build` builds an empty set, `RenderMarkup` escapes |
| The archive reader's tap | [collection/tap.go](../../internal/formats/ansible/collection/tap.go) | built |
| The upstream parser and the document renderer | [galaxydoc/docs.go](../../internal/formats/ansible/galaxydoc/docs.go) | signatures; both answer `ErrDocsNotBuilt` |
| `Host.Markup`, `NoMarkup`, the kit's stand-in, the composition root's value | [markup.go](../../internal/formats/markup.go), [formattest/markup.go](../../internal/formats/formattest/markup.go), [app/wire_markup.go](../../internal/app/wire_markup.go), `readme.Renderer.SanitizeHTML` | built |
| The route `ansible.version.docs` | [routes.go](../../internal/formats/ansible/routes.go), [match.go](../../internal/formats/ansible/match.go), [paths.go](../../internal/formats/ansible/paths.go), [handlers_docs.go](../../internal/formats/ansible/handlers_docs.go) | declared for the three classes; the handlers answer 501 |
| The wiki seam | [wikigate.go](../../internal/formats/ansible/wikigate.go) | built; the check is the repository read decision until task T-18 replaces it |
| The job `ansible-docs-fill` | [jobs.go](../../internal/formatdata/ansible/jobs.go) | registered; the handler is nil and answers a permanent `not_implemented` |
| The setting `ansible.fetchDocumentation` | [settings.schema.json](../../internal/formats/ansible/settings.schema.json), `AnsibleSettings` in [api/openapi.yaml](../../api/openapi.yaml), `Settings.FetchDocumentation` | built (read, not yet acted on) |
| The three operations and their schemas, `metadata.docs`, the filters `package` and `version` of `listAnsibleImportTasks` | [api/openapi.yaml](../../api/openapi.yaml), [ansible_docs.go](../../internal/api/v1/ansible_docs.go) | documented; the handlers answer 503 `unavailable`, the filters are refused the same way |

## The wiki seam

Who may read a repository's artifacts may not be who may read its documentation (R-ANS-037). The format asks one function,
`wikiReadable(ctx, rq, target) bool` in [wikigate.go](../../internal/formats/ansible/wikigate.go), which has the signature of task T-18's
`Host.WikiReadable`; a handler that gets false answers as an absent resource. Today the function calls the package variable `wikiCheck`,
the repository read decision of the request's subject; when T-18 lands the wiki check of R-AUTHZ-034, only that variable changes. The
other sites of the switch: `Route.Wiki` on `routeDocs` in routes.go, the marker `x-trove-part: wiki` of the three operations in
[api/openapi.yaml](../../api/openapi.yaml) (set, read into `Operation.Part` by [document.go](../../internal/api/v1/document.go)), and
`ViewData.WikiMetadata = []string{"docs"}` when the version details announce the documentation. `registerWikiSurface` lists the surfaces
that serve documentation, and `TestR_ANS_037_EverySurfaceAsksTheWikiGate` walks the list.

## The Markup capability

A format may import neither the README renderer nor an HTML library (R-ARCH-002), yet the document it serves holds HTML members. The Host
hands it `Markup()` (ADR-196): `Markdown(src)` renders under the policy of R-UI-008 and `SanitizeHTML(html)` applies the allowlist to HTML
somebody else wrote. The composition root builds the value over `internal/packages/readme` with the operator's `ui.readme` keys
(`App.hostMarkup`). A Host without a renderer answers `formats.NoMarkup`, whose calls return the empty string. The conformance kit's Host
answers `formattest.StandInMarkup`, which escapes: a unit test that uses it proves the wiring, not the policy (H-898).

## Tests

| Test | Proves |
|---|---|
| `TestR_ANS_033_TheTwinReportsTheStatesOfTheDocumentation`, `TestR_ANS_036_TheTwinsTreeKeepsTheCountsWhileASearchFilters` | the rules the twin shares with the SQL store |
| `TestR_ANS_034_TheTapSeesTheFirstBytesOfAWantedMember` | the tap sees the capped bytes of the members it wants and changes nothing else |
| `TestR_ANS_035_TheDocsBlobDocumentIsMetadataOfItsVersion`, `TestR_ANS_003_TheRouteTable` | the route and the canonical path of the stored document |
| `TestR_ANS_037_EverySurfaceAsksTheWikiGate`, `TestR_ANS_037_TheDocumentListsTheWikiOperations` | the seam and the marker |
| `TestR_ANS_039_SanitizeHTMLAppliesTheREADMEPolicyToForeignHTML`, `TestR_ANS_039_TheHostMarkupRunsThePolicyOfTheREADMERenderer` | the one policy behind `Markup` |
