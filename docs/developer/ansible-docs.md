# Ansible Galaxy: the documentation of collections

The documentation of a collection version (the README, the Markdown files of `docs/`, and one entry per module, plugin and role) is derived
data that trove generates for a local repository, keeps for a remote one and reads from the winning member for a virtual one. The
requirements are [R-ANS-033 to R-ANS-046](../spec/formats/ansible.md), the screens are R-UI-060 to R-UI-064
([api-and-ui.md](../spec/api-and-ui.md)), the decisions [ADR-196](../decisions.md) and [ADR-197](../decisions.md), and the threats
[docs/security/ansible-documentation.md](../security/ansible-documentation.md). The work-package cut is
[galaxy-docs-work-packages.md](../projektplan/galaxy-docs-work-packages.md).

## What is built and what is not

The backend and the screens are built; the end-to-end rows and the browser journey of the set `ansible` are not written yet. Where a part is a stand-in it says so.

| Part | Where | State |
|---|---|---|
| The neutral form, the file title rule | [rows/docs.go](../../internal/formats/ansible/rows/docs.go), [rows/doctitle.go](../../internal/formats/ansible/rows/doctitle.go) | built |
| The generator: the literal scanner, the YAML node walker, the shaping of options and return values, the bounds | [docgen](../../internal/formats/ansible/docgen) (`scan.go`, `shape.go`, `docgen.go`) | built; executes nothing (a test lists its dependencies) |
| The converter of the Ansible markup | [docgen/markup.go](../../internal/formats/ansible/docgen/markup.go) | built; escapes first, a fuzz target holds the tag set |
| The tap of the archive reader | [collection/tap.go](../../internal/formats/ansible/collection/tap.go); `Generator` implements it | built |
| The port group `DocsStore` and its twin | [store.go](../../internal/formats/ansible/store.go), [ansibletest/docs.go](../../internal/formats/ansible/ansibletest/docs.go) | built |
| The PostgreSQL implementation, the statements, the reindex step | [docs_store.go](../../internal/formatdata/ansible/docs_store.go), [queries/ansible_docs.sql](../../internal/meta/postgres/queries/ansible_docs.sql), [reindex.go](../../internal/formatdata/ansible/reindex.go) | built |
| The four tables | migrations `ansible_doc_tables` and `ansible_doc_fetches`; registered `transient` in [doc_tables.go](../../internal/formatdata/ansible/doc_tables.go) | built |
| The publish | `Inspect` runs the generator in the one pass; `OnCommit` puts the rows with the version; `OnItemsRemoved` removes them; the warnings are messages of the import task | built |
| The upstream parser and the document renderer | [galaxydoc/docs.go](../../internal/formats/ansible/galaxydoc/docs.go) | built |
| `Host.Markup`, `NoMarkup`, the kit's stand-in, the composition root's value | [markup.go](../../internal/formats/markup.go), [formattest/markup.go](../../internal/formats/formattest/markup.go), [app/wire_markup.go](../../internal/app/wire_markup.go) | built |
| The route `ansible.version.docs` | [handlers_docs.go](../../internal/formats/ansible/handlers_docs.go) (local, virtual), [handlers_remote_docs.go](../../internal/formats/ansible/handlers_remote_docs.go) (remote) | built |
| The remote fill | `PrepareFill`, `OnFill`, `queueDocs` and `docsAddress` in [remote.go](../../internal/formats/ansible/remote.go) | built |
| The job `ansible-docs-fill` | [jobs.go](../../internal/formatdata/ansible/jobs.go), [docs_job.go](../../internal/formatdata/ansible/docs_job.go), wired in [wire_ansible.go](../../internal/app/wire_ansible.go) | built |
| The setting `ansible.fetchDocumentation` | [settings.schema.json](../../internal/formats/ansible/settings.schema.json), `AnsibleSettings` in [api/openapi.yaml](../../api/openapi.yaml) | built |
| The three operations, `metadata.docs`, the filters of `listAnsibleImportTasks` | [api/v1/ansible_docs.go](../../internal/api/v1/ansible_docs.go), [ansible_tasks.go](../../internal/api/v1/ansible_tasks.go) | built |
| The wiki seam | [wikigate.go](../../internal/formats/ansible/wikigate.go) (`Host.WikiReadable`) and `Scope.MayReadWiki` in [ansible_docs.go](../../internal/api/v1/ansible_docs.go) | built: the wiki check of R-AUTHZ-034, since task T-18 |
| Metrics | [docs_metrics.go](../../internal/formats/ansible/docs_metrics.go) | the route and the fills of the request path; see H-903 |
| The counting stub | [test/ansiblestub](../../test/ansiblestub) (`docs-blob/`, the modes `docs`, `docs_foreign_href`) | built |
| The Documentation, Contents and Import log tabs, the Dependencies links | [web-ui.md](web-ui.md#the-ansible-documentation-screens) | built against the generated types and the fakes; not yet seen in a browser, the browser journey of the set `ansible` is not written yet |

## The paths

**Publish.** `Inspect` gives the archive reader a `docgen.Generator` as its tap: the reader streams every member through the digest and hands the generator the first bytes of the members it wants; a Python source is scanned for its three literals at once and dropped. `Build(readmeName)` makes the `rows.DocSet`, which travels in the `Inspection.Payload` to `OnCommit`, where `PutDocs` writes it in the commit transaction.

**Remote.** The fill of a version document records the address of the documentation as a URL-map row and, with `fetchDocumentation` on, queues the job (`queueDocs`); the job asks the engine for the stored document, whose `PrepareFill` parses it and whose `OnFill` writes the rows. A client's request for the route takes the same path inline. The route renders from the parsed stored document through the Host's sanitizer; the stored bytes stay as received.

**Virtual.** `virtualDocs` takes the resolver's plan and tries the members by tier and position (Q-ANS-74).

**Screens.** `resolveDocs` takes the repositories of the version details, local before remote, and the first whose state is not none (Q-API-264).

## The wiki seam

Who may read a repository's artifacts may not be who may read its documentation (R-ANS-037). The format asks one function,
`wikiReadable(ctx, rq, target) bool` in [wikigate.go](../../internal/formats/ansible/wikigate.go), which calls `rq.Host.WikiReadable`, the
question form of the wiki check of R-AUTHZ-034; a handler that gets false answers as an absent resource. The route `ansible.version.docs`
is declared `Wiki` in [routes.go](../../internal/formats/ansible/routes.go), so the dispatcher refuses a caller who may not read the wiki
before the handler runs; the handlers ask again for the repository they read from, which for a virtual repository is a member. The three
management operations carry the marker `x-trove-part: wiki` in [api/openapi.yaml](../../api/openapi.yaml) (read into `Operation.Part` by
[document.go](../../internal/api/v1/document.go)) and decide each candidate repository with `Scope.MayReadWiki` in
[ansible_docs.go](../../internal/api/v1/ansible_docs.go), the caller's `authz.ReadScope.MayReadWiki`; the announcement `metadata.docs` of
the version details goes through the same function, so it is absent for a caller who may not read the wiki. `registerWikiSurface` lists
the surfaces that serve documentation, `TestR_ANS_037_EverySurfaceAsksTheWikiGate` walks the list, and check 15 of the format kit serves
every content case again for a caller who reads the artifacts and not the wiki.

## The Markup capability

A format may import neither the README renderer nor an HTML library (R-ARCH-002), yet the document it serves holds HTML members. The Host
hands it `Markup()` (ADR-196): `Markdown(src)` renders under the policy of R-UI-008 and `SanitizeHTML(html)` applies the allowlist to HTML
somebody else wrote. The composition root builds the value over `internal/packages/readme` with the operator's `ui.readme` keys
(`App.hostMarkup`). A Host without a renderer answers `formats.NoMarkup`, whose calls return the empty string. The conformance kit's Host
answers `formattest.StandInMarkup`, which escapes: a unit test that uses it proves the wiring, not the policy (H-898).

## Tests

| Test | Proves |
|---|---|
| `TestR_ANS_034_*` in [docgen](../../internal/formats/ansible/docgen) | the generator over crafted sources, the bounds, the determinism, `FuzzR_ANS_034_Generator` |
| `TestR_ANS_039_TheMarkupTableHasACasePerMacro`, `FuzzR_ANS_039_RenderMarkup` | the converter |
| `test/integration/ansiblelocal/docs_test.go` | publish, the operations, the route, the reindex, the warnings |
| `test/integration/ansibleremote/docs_test.go` | the fetch once, the sanitized serving, the states, the setting, a foreign `href` |
| `test/integration/ansiblevirtual/docs_test.go` | the first member that has documentation |
| `TestR_ANS_033_TheTwinReportsTheStatesOfTheDocumentation`, `TestR_ANS_036_TheTwinsTreeKeepsTheCountsWhileASearchFilters` | the rules the twin shares with the SQL store |
| `TestR_ANS_034_TheTapSeesTheFirstBytesOfAWantedMember` | the tap sees the capped bytes of the members it wants and changes nothing else |
| `TestR_ANS_035_TheDocsBlobDocumentIsMetadataOfItsVersion`, `TestR_ANS_003_TheRouteTable` | the route and the canonical path of the stored document |
| `TestR_ANS_037_EverySurfaceAsksTheWikiGate`, `TestR_ANS_037_TheDocumentListsTheWikiOperations` | the seam and the marker |
| `TestR_ANS_039_SanitizeHTMLAppliesTheREADMEPolicyToForeignHTML`, `TestR_ANS_039_TheHostMarkupRunsThePolicyOfTheREADMERenderer` | the one policy behind `Markup` |
