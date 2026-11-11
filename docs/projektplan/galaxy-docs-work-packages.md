# T-19 · The documentation of Ansible Galaxy collections — the work-package cut

The plan of owner task T-19 ([promt.md](promt.md) section 10), written by the planning agent on 2026-11-11. Spec:
[formats/ansible.md](../spec/formats/ansible.md), requirements **R-ANS-033 to R-ANS-046**, decisions **Q-ANS-56 to Q-ANS-70**;
[api-and-ui.md](../spec/api-and-ui.md), requirements **R-UI-060 to R-UI-064**, decisions **Q-UI-240 to Q-UI-247** and **Q-API-260 to Q-API-263**;
[ansible-documentation.md](../security/ansible-documentation.md), gaps **H-890 to H-899**; [ADR-196](../decisions.md) and [ADR-197](../decisions.md);
migrations **00071** and **00072**.

What the owner asked: galaxy_ng shows a collection's documentation (Install, Documentation with a search field and a tree, Contents, Import log,
Dependencies, Distributions, the links); trove shows it for local, remote and virtual repositories, as the **wiki** part of T-18. The spec decides
everything below; a builder takes no design decision of its own, and a question the spec leaves open is reported, not decided.

## 0 · What was found, and what it decides

Verified in this repository and against the public Galaxy (2026-11-11):

- The documentation is the sub-resource `…/versions/<v>/docs-blob/` on the public Galaxy and on the owner's galaxy_ng, not a member of the version document.
  `ansible.netcommon` 8.7.1: 154,868 bytes, 65 entries, keys `collection_readme`, `contents`, `documentation_files` (read again for this plan);
  community.general 10.0.0: 3.5 MB, 833 entries; fortinet.fortios 2.6.0: 6.5 MB; the owner's `hans_fischer.common` 1.5.0: 4,122 bytes, 32 entries, every `doc_strings` null.
- galaxy-importer builds the document at import with `ansible-doc` and renders only Markdown (Q-ANS-56, Q-ANS-64). trove generates the entries for a local publish
  from the archive's text and passes an upstream's document through (Q-ANS-56, Q-ANS-58).
- **A format cannot import the README renderer** (`internal/archtest/imports.go`, `formatAllowed`): the Host gains `Markup()` (Q-ANS-66, ADR-196). The
  first draft of this plan put a renderer glue package into the format; that would have failed the architecture test.
- **A format cannot enqueue a job**, but its data port can in the fill transaction, and `MirrorHost.Background(ctx, repoID)` is the upstream a data port's job uses; its `Document`
  is the engine's metadata fetch with `PrepareFill` and `OnFill` (`internal/formathost/mirror_host.go`). So the remote contract needs no change (Q-ANS-60). The first draft's
  "first check" is settled.
- The API layer already reaches the Ansible store (`Options.Ansible`, `internal/api/v1/ansible_api.go`) and the README renderer (`ps.Readme`); the three operations live there.
- The package page already shows Requires Ansible, licences, tags, links and dependencies for the format (`web/src/app/features/packages/package-detail.ts`, `ansibleFacts`, `links`).

Not verified, and this is the gap: that `ansible-doc` never loads plugin code for some plugin types (not needed: trove runs none); the compression ratio of the rows (H-890); T-18's Go
name and marker for the wiki check (the contract below names it "the wiki check of R-AUTHZ-034 (Go name fixed by T-18's WP0)" and the development agent aligns the names when both
plans are merged); that the view service can list a version's repositories in a virtual repository's plan order without a change (B1's first task of step 6 reads it, section 3).

## 1 · The cut: two builders, at most two at a time

| Builder | Feature end to end | Requirements | Owns (directories and files) | Stack | Kind |
|---|---|---|---|---|---|
| **B1 · Backend** (does WP0 first, alone) | The contracts (section 2); the generator and the markup converter; the archive reader's tap; the four tables, their port, the migrations; the commit-transaction write; the reindex step; the route `docs-blob/` for local, remote, virtual; the remote fill (`PrepareFill`, `OnFill`, `Rewrite`, the job, the mirror queueing, the setting); `Host.Markup`; the three operations and the version-details member; the filters of the import tasks; the wiki call site; the stub; metrics | R-ANS-033 to R-ANS-036, R-ANS-037 (call site), R-ANS-038 to R-ANS-045, R-API (Q-API-260 to Q-API-263) | `internal/formats/ansible/docgen/` (new leaf), `internal/formats/ansible/collection/` (the tap), `internal/formats/ansible/{inspect,publish,hooks,store,routes,match,paths,handlers_local,handlers_remote,handlers_virtual,remote,remote_docs,mirror,settings,view,bind,wikigate}.go`, `settings.schema.json`, `rows/docs.go`, `ansibletest/`, `galaxydoc/`; `internal/formats/{host.go,markup.go}` and `internal/formats/formattest/` (the kit's Markup and the route count); `internal/packages/readme/` (`SanitizeHTML`); the composition root's wiring of `Host.Markup` (`internal/formathost/`, `cmd/`); `internal/formatdata/ansible/` (port, jobs, reindex, purge); `internal/meta/postgres/migrations/00071_*`, `00072_*` and `queries/` with the sqlc output; `internal/packages/view/` (the optional docs interface and the repository list in plan order); `internal/api/v1/{ansible_docs*.go,ansible_tasks.go,packages_api.go (only the details member)}`; `api/openapi.yaml` (WP0 writes every operation and schema; later edits only by B1) and the generated code; `test/ansiblestub/`; `test/integration/{ansiblelocal,ansibleremote,ansiblevirtual}/docs_test.go`; `docs/developer/{ansible.md,ansible-remote.md,ansible-virtual.md,ansible-docs.md (new)}`; `docs/security/ansible-documentation.md` (corrections as the code lands); `docs/spec/{backup-and-restore.md (R-BAK-004 line),architecture.md (R-ARCH-008 line)}` | yes (`ansiblelocal`, `ansibleremote`, `ansiblevirtual`) | no |
| **B2 · UI and e2e** (starts when WP0 has landed) | The Documentation, Contents and Import log tabs, the Dependencies section, the link label, the states; the web unit tests; the e2e rows local, remote, virtual (and wiki) with the real client and the browser journeys; the manual page | R-UI-060 to R-UI-064, R-ANS-046, Q-UI-240 to Q-UI-247 | `web/src/app/features/packages/ansible-docs/` (new: tree, entry page, file page, contents, import log, their specs), `package-detail.{ts,html,scss}` (the tab list and the Metadata sections, for the format only), `web/src/app/shared/` only if a control is missing, the i18n units, `web/src/app/api/testing/fakes-packages.ts` (the fake of the operations), `web/e2e/tests/…-ansible-docs.spec.ts`; `test/e2e/ansible_docs_test.go`, `test/e2e/testdata/ansible_docs/` (the source tree of the collection), `deploy/dev/e2e.sh` (the cases in the set `ansible`); `docs/manual/` (the Documentation tab); `README.md` (Key Features line, documentation table); `docs/developer/{web-ui.md,e2e.md}` rows | no (browser journeys only) | **yes** (the only kind user of T-19) |

Shared files with a rule instead of an owner: `docs/projektplan/{FULL.md,BETA.md,promt.md}` (the development agent); `docs/coverage/*` (regenerated by `make coverage` in each builder's last commit;
`implemented.txt` is written by CI only); `docs/spec/authorization.md` and the wiki check (T-18's package, which T-19 only calls).

The two builders write **disjoint directories**: `web/`, `test/e2e/`, `deploy/dev/` and `docs/manual/` are B2's; everything else listed is B1's. The only file both read is `api/openapi.yaml`
(B2 reads the generated TypeScript client of it, B1 owns it). T-18's builders touch `internal/authz`, the settings pages and the repository model; the points where T-19 meets them are
the wiki check's Go name (WP0, section 2.7), the OpenAPI marker (Q-API-260) and the e2e wiki row (R-ANS-046).

## 2 · Contracts first: WP0 (B1, one commit, landed before B2 starts)

WP0 contains no behaviour: types, the port, signatures, the OpenAPI document, the migrations, the route with a 501 behind it. After it a change to a contract goes through the development agent.

### 2.1 The neutral form (`internal/formats/ansible/rows/docs.go`; `rows` is a leaf that imports only `errs` and the standard library, R-ARCH-002)

```go
// DocSet is the documentation of one version in the one form every source is turned into (generated from an archive, parsed from an upstream's docs-blob,
// read from the rows) and every output is rendered from. It is bounded by R-ANS-031 before it is built.
type DocSet struct {
	Source    string // "generated" | "upstream"
	Generator int    // docgen.Version; 0 for upstream
	Readme    *DocFile
	Files     []DocFile // docs/*.md, at most 64, in name order
	Entries   []DocEntry
	Truncated bool
	Warnings  []DocWarning // at most 100
}
type DocFile struct{ Name, Format, Body string } // Format "markdown" (generated) | "html" (upstream)
type DocEntry struct {
	Type, Name, ShortDescription string          // Type is the Galaxy's content_type; Name its content_name
	HasDocs                      bool
	Doc                          json.RawMessage // doc_strings in the Galaxy form of R-ANS-035; nil when !HasDocs
	ReadmeMD, ReadmeHTML         string          // roles: generated source, or the upstream's HTML
	SearchText                   string          // at most 4 KiB, built by the producer (R-ANS-033)
}
type DocWarning struct{ Path, Code, Message string }
type DocState struct {
	State, Reason, Source string // R-ANS-033 states and reasons
	Entries               int
	Truncated             bool
	BuiltAt               time.Time
}
type DocQuery struct{ Terms []string } // validated by the API (R-ANS-036): 1 to 8 terms of 1 to 64 letters or digits, never raw text
type DocTree struct { // the tree before grouping: entries and file titles, with the counts before q
	State DocState
	Files []DocFileHead   // Name, Title
	Entries []DocEntryHead // Type, Name, ShortDescription, HasDocs
	CountBeforeQuery int
}
```

### 2.2 The port (`internal/formats/ansible/store.go`: a `DocsStore` group embedded in `Store`, implemented by B1 in `internal/formatdata/ansible`)

```go
type DocsStore interface {
	// PutDocs replaces the documentation of a version in the transaction of the publish (OnCommit) or of the fill (OnFill).
	PutDocs(ctx context.Context, tx formats.Tx, repoID int64, name, version string, d rows.DocSet) error
	// RemoveDocs deletes the rows of a version (OnItemsRemoved); a missing one is not an error.
	RemoveDocs(ctx context.Context, tx formats.Tx, repoID int64, name, version string) error
	// SetDocFetch records, in tx, the fetch row of a remote version: state queued, unavailable or none, with the reason.
	SetDocFetch(ctx context.Context, tx formats.Tx, repoID int64, name, version, state, reason string) error
	// EnqueueDocsFill queues the job ansible-docs-fill in tx and writes the queued fetch row; it queues nothing when a job for the version exists.
	EnqueueDocsFill(ctx context.Context, tx formats.Tx, repoID int64, name, version string) error
	DocState(ctx context.Context, repoID int64, name, version string) (rows.DocState, error) // state none when there is nothing
	DocTree(ctx context.Context, repoID int64, name, version string, q *rows.DocQuery) (rows.DocTree, error)
	DocEntry(ctx context.Context, repoID int64, name, version, typ, entry string) (rows.DocEntry, bool, error)
	DocFile(ctx context.Context, repoID int64, name, version, file string) (rows.DocFile, bool, error)
	DocSet(ctx context.Context, repoID int64, name, version string) (rows.DocSet, bool, error) // the whole set, for the protocol document
	// RebuildDocs is the docs step of ansible-reindex for a local repository; RebuildRemoteDocs re-derives a remote's rows from its stored documents.
	RebuildDocs(ctx context.Context, repoID int64, reader rows.ArchiveReader, checkpoint string) (next string, done bool, err error)
	RebuildRemoteDocs(ctx context.Context, repoID int64, checkpoint string) (next string, done bool, err error)
}
```

`ansibletest` implements it in memory in WP0, so B1's unit work and the conformance kit run before the SQL exists. `internal/api/v1.AnsibleStore` gains the read methods (`DocState`, `DocTree`, `DocEntry`, `DocFile`).

### 2.3 The generator, the tap and the converter

```go
// internal/formats/ansible/docgen (B1; leaf: the standard library and the YAML node walker of the collection package only; no os/exec)
const Version = 1
type Limits struct{ Literal, Source, SourceBudget, Entry, EntriesPerVersion, BytesPerVersion, File, Files int64 } // the defaults are R-ANS-031's
type Member struct{ Path string; Body []byte } // the capped bytes of a wanted member (the tap has read them already)
func NewGenerator(lim Limits) *Generator
func (g *Generator) Wants(path string) (maxBytes int64, ok bool) // which members, and how many bytes of each (R-ANS-034)
func (g *Generator) Add(m Member)                                // in archive order; never fails
func (g *Generator) Build(readmeName string) rows.DocSet         // deterministic order, bounds applied, warnings collected
func RenderMarkup(text string) string                            // the Ansible markup, escape first (R-ANS-039); the caller sanitizes the result

// internal/formats/ansible/collection
type Tap interface{ Wants(path string) (maxBytes int64, ok bool); Take(path string, body []byte) }
func WithTap(t Tap) Option                                       // ReadArchive(ctx, r, lim, opts ...Option); a nil tap is today's behaviour

// internal/formats/ansible/galaxydoc (B1)
func ParseUpstreamDocs(doc []byte) (rows.DocSet, error)          // the shape of R-ANS-035, bounded; an item without content_name/content_type is an error
func RenderDocsBlob(d rows.DocSet, m formats.Markup) ([]byte, error) // the protocol document from any class; Markdown rows through m.Markdown, HTML rows through m.SanitizeHTML
```

`Inspection.Payload` carries the `Archive` and now `*rows.DocSet`; the three `collection` tests that pin `ReadArchive`'s signature change in WP0.

### 2.4 The Host capability (`internal/formats/markup.go`, `host.go`)

```go
// Markup is what a format may use to render and sanitize the text of its documentation (ADR-196). It runs the policy of R-UI-008.
type Markup interface {
	Markdown(src string) (html string, truncated bool)
	SanitizeHTML(html string) string
}
// Host gains: Markup() Markup
```

`NoMarkup` (errors, never the input), the kit's deterministic stand-in (escapes and wraps), the composition root's implementation over `internal/packages/readme` (which gains
`(*Renderer).SanitizeHTML(html string) string`, the existing allowlist applied to HTML only), and `Format.Bind` keeping `h.Markup()` in the replica. The architecture test needs no change.

### 2.5 The view and the API

- `internal/packages/view`: the optional interface `DocsAnnouncer` of an `Adapter` (`DocsState(ctx, []Version) (map[int64]DocsAnnouncement, error)`), asked only for adapters that have it, so
  `metadata.docs` is built with the details; and `Service.VersionRepositories(ctx, sc, format, name, version) ([]RepositoryEntry, error)` listing the repositories that hold the version in
  the plan order of a virtual repository, so the operations pick the winner (R-ANS-041).
- `internal/api/v1/ansible_docs.go`: the three handlers of R-ANS-036, `repository` resolution, the `q` validation, the entry builder (`RenderMarkup` then `ps.Readme.SanitizeHTML`, depth 12), the
  file operation with the README `ETag` rule; all call the wiki check once per request (section 2.7).

### 2.6 OpenAPI (`api/openapi.yaml`, written complete in WP0; every operation answers `501 ansible_not_implemented` until B1 builds it)

All three operations: `tags: [packages]`, `x-trove-permission: {public: true}` (the handler decides visibility as the README operation does, R-API-029), `x-trove-part: wiki` (Q-API-260),
parameters `PackageId`, `PackageVersionPath`, the query parameter `repository` (`<projectKey>/<repoKey>`, optional), `IfNoneMatch`; responses 200, 304, 400, 401, 404, 422 (tree only), 429, 500, 503.

| operationId | Path | Query | 200 schema |
|---|---|---|---|
| `getPackageVersionDocs` | `/api/v1/packages/{packageId}/versions/{version}/docs` | `repository`, `q` (string, 2 to 512 characters) | `PackageDocTree` |
| `getPackageVersionDocEntry` | `/api/v1/packages/{packageId}/versions/{version}/docs/entries/{type}/{name}` | `repository` | `PackageDocEntry` |
| `getPackageVersionDocFile` | `/api/v1/packages/{packageId}/versions/{version}/docs/files/{file}` | `repository` | `PackageDocFile` |

```yaml
PackageDocTree:
  type: object
  required: [state, repository]
  properties:
    state: {type: string, enum: [available, pending, unavailable, none]}
    reason: {type: string, enum: [upstream_error, too_large, invalid_document]}
    source: {type: string, enum: [generated, upstream]}
    repository: {type: string, description: "<projectKey>/<repoKey> the documentation was read from"}
    builtAt: {type: string, format: date-time}
    truncated: {type: boolean}
    matchCount: {type: integer, format: int32, description: "Only with q: the entries left."}
    files: {type: array, items: {$ref: "#/components/schemas/PackageDocFileHead"}}      # name, title
    groups: {type: array, items: {$ref: "#/components/schemas/PackageDocGroup"}}        # category (module|plugin|role), label, count (before q), types[] {type, count, entries[] {type, name, shortDescription, hasDocs}}
    warnings: {type: array, items: {$ref: "#/components/schemas/PackageDocWarning"}}    # path, code, message; only for a caller who holds deploy
PackageDocEntry:
  type: object
  required: [type, name, fullName, hasDocs, source]
  properties:   # as R-ANS-036: shortDescription, builtAt, versionAdded, deprecated, description[], notes[], requirements[], author[], options[] (PackageDocOption, recursive via suboptions),
                # returnValues[] (recursive via contains), examples, seealso[], attributes[], extendsFragments[], readmeHtml
PackageDocFile:
  type: object
  required: [name, contentType, html, truncated, externalImagesBlocked]
  properties: {name: {type: string}, contentType: {type: string, enum: [text/markdown]}, html: {type: string}, truncated: {type: boolean}, externalImagesBlocked: {type: integer, format: int32}}
```

`listAnsibleImportTasks` gains the optional query parameters `package` and `version` (Q-API-262). The version details schema gains the optional `metadata.docs` (`state`, `source`, `entries`; Q-API-263).
The repository document's `ansible` member gains `fetchDocumentation` (boolean, default `true`, remote only; also in `settings.schema.json`). The document validator reads `x-trove-part` (value `wiki`; any other value refused)
into `Operation.Part` in `internal/api/v1/document.go`. The generated code (`make generate`) is committed with WP0.

### 2.7 The route, the call site of the wiki check and the job

- `routes.go`: the route `ansible.version.docs` (`GET v3/collections/{ns}/{name}/versions/{version}/docs-blob/`, kind Metadata, action `read`, classes local, remote, virtual, `TokenScheme`) with its matcher in
  `match.go`, example paths, and a 501 handler; `paths.go`: `Classify` of `collections/<ns>/<name>/docs/<version>.json` as metadata of the package, and `DocsDocumentPath`. The counts in `routes_test.go` and
  in `formattest` change with it.
- `wikigate.go`: `func wikiReadable(ctx context.Context, rq *formats.Request, repo formats.RepoRef) error` calling **the wiki check of R-AUTHZ-034 (Go name fixed by T-18's WP0)** and returning its
  refusal unchanged; a registry `registerWikiSurface(name string)` and `TestR_ANS_037_EverySurfaceAsksTheWikiGate` that walks it. Until T-18's WP0 has landed the file holds a local stub with the same
  signature that answers the artifact `read`, marked `// replaced by the wiki check of R-AUTHZ-034`; **it is the only file that changes when the names are aligned**. The API handlers ask through the
  API-side form of the same check (T-18 names it); the call is one function of `ansible_docs.go`.
- `internal/formatdata/ansible/jobs.go`: the kind `ansible-docs-fill` registered with `Handlers.DocsFill` (nil answers the permanent "not built" like `Reindex`), `DocsFillJobKind`, the key
  `ansible-docs:<repoId>:<name>:<version>` (R-ANS-042).
- Migrations: **00071_ansible_doc_tables.sql** (`ansible_version_docs`, `ansible_doc_entries`, `ansible_doc_files`, the GIN index on `search`) and **00072_ansible_doc_fetches.sql**, `-- trove:phase expand`, columns as
  R-ANS-033. The backup table registry marks all four derived (not exported, R-BAK-004).

## 3 · What each builder does

### 3.1 B1 · Backend (after WP0)

1. **`docgen`** (R-ANS-034): the content-item finder from member paths; the hand-written literal scanner over bytes (Python string forms, prefixes, parentheses, adjacent literals, the 2 MiB and 512 KiB bounds, the 256 MiB budget;
   no regular expression over a whole source); the sidecar `.yml`; the YAML node decoding with the refusals of `meta/runtime.yml`; the shaping of `options`/`return` into named lists with `suboptions`/`contains`; the key
   whitelist of R-ANS-035; the README and `docs/` files (`Build(readmeName)` picks the README); roles' README; the bounds and warnings; `Version`; `RenderMarkup`. Fuzz targets for the scanner, the YAML decoding and
   `RenderMarkup`. `go list -deps` has no `os/exec` (a test).
2. **The tap and the commit**: `collection.WithTap` (capped writer beside the digest; a tap error is a warning); `Inspect` builds the generator, returns the `DocSet`; `publish.go` adds the warnings to the import task (level
   `WARNING`, at most 100 and one summary line); `OnCommit` calls `PutDocs` after `PutVersion`; `OnItemsRemoved` calls `RemoveDocs`.
3. **Migrations, queries, port** (`docs_store.go`, `docs_local.go`): `PutDocs` in one statement group (delete the version's rows, insert), the reads, `DocTree` with the `q` rule of R-ANS-036 and Q-ANS-67 (terms to a
   `to_tsquery('simple', 'term:*')` built from alphanumeric-only terms and passed as a parameter, plus `strpos(lower(name), lower($n)) > 0`), `RebuildDocs`; the reindex step (R-ANS-044).
4. **The route, local and virtual** (R-ANS-035, R-ANS-041): `handlers_docs.go` renders `RenderDocsBlob` from `DocSet`, weak `ETag`, 404 without rows; `Host.Markup` and its three implementations; `readme.SanitizeHTML`; the virtual
   winner by the version document's plan; the call of `wikiReadable` on every surface.
5. **The remote** (R-ANS-035, R-ANS-040, R-ANS-042, R-ANS-043): `UpstreamRequest` for the docs path (the version document's `href`, origin and path check), `AcceptResponse`, `PrepareFill` (shape, bounds, `ParseUpstreamDocs`), `OnFill`
   (`PutDocs` and `SetDocFetch`, then `EnqueueDocsFill` from the version document's `OnFill`), `Rewrite` (every `html` through `Markup().SanitizeHTML`), the job handler over `MirrorHost.Background`, the mirror's queueing in
   `mirror.go`, the setting, `remote_purge.go` for the stored documents, `RebuildRemoteDocs`.
6. **The operations** (R-ANS-036, R-ANS-039, Q-API-260 to Q-API-263): `ansible_docs.go` (resolution of `repository` including the virtual winner, `q` validation, the tree grouping with counts before `q`, the entry builder, the file operation),
   `metadata.docs` through the view adapter, the two filters of `listAnsibleImportTasks`. First read `Service.VersionRepositories` against `internal/packages/view/service.go`; if the virtual plan order cannot be had without a
   change to the core's grouping, stop and report (do not copy the plan into the API).
7. **Metrics** (R-ANS-045) and the **stub**: `test/ansiblestub` serves `docs-blob/` from its corpus (the entries of R-ANS-038 including the `<script>` in a README), the modes `docs_404`, `docs_500`, `docs_huge`, the counter per path.
8. **Integration** `ansiblelocal/docs_test.go`, `ansibleremote/docs_test.go`, `ansiblevirtual/docs_test.go` through the slot wrapper (one run per area); developer and security pages; `make cyclo`, lint, `generate-check` (sqlc, oapi-codegen).

### 3.2 B2 · UI and e2e (after WP0; the e2e rows after B1's stub work has landed)

1. **The tab list** (R-UI-060, Q-UI-240): in `package-detail`, the tabs for `ansible` are chosen from `metadata.docs` and `hasReadme`; README is not shown for the format; no request for a tab that is absent; the Metadata tab gains the
   Dependencies section and the "Docs site" label (Q-UI-245); the Import log tab for a local repository's version (R-UI-063).
2. **The Documentation tab** (Q-UI-241, Q-UI-243, Q-UI-246): the two panes of the file browser (`_browse.scss`), the tree from `getPackageVersionDocs` with groups, counts and type sub-groups, the URL parameters `doc`/`q`, `role="tree"`,
   the narrow layout, the search field with its debounce, minimum length, live region and empty state.
3. **The entry and file pages** (R-UI-061, Q-UI-242): head, sections, the parameter table with nested rows, examples and return values in the code block control, the states and the source line; HTML via `[innerHTML]` as the README
   component does, everything else as text.
4. **The Contents tab** (R-UI-062, Q-UI-247): a data table with a type filter over the tree's answer, pages of 100.
5. **Unit tests** (`make web-test`), the lint rules of R-UI-046, the accessibility checks of the other tabs, the viewport cases of R-UI-007 (390 and 1440 px); the fake of the three operations in `fakes-packages.ts` built from the schemas of WP0.
6. **e2e on kind** (R-ANS-046; the set `ansible`; take the kind lock): the collection of `test/e2e/testdata/ansible_docs/` built with `ansible-galaxy collection build` in the client pod; rows **local**, **remote** (on the stub), **virtual**
   and, once T-18's check exists, **wiki**; each with the real client's publish or install and the Playwright journey run from the Go test (like `TestE1_11Journeys`); the cases registered in `deploy/dev/e2e.sh`.
7. The manual page and the README lines.

## 4 · Order and dependencies

```
WP0 (B1 alone) ──► B1 steps 1-3 ──► B1 steps 4-6 ──► B1 step 7 (stub) ──► B1 step 8 (integration)
        └──► B2 steps 1-5 (against the OpenAPI document and the fake) ───────────────────────► B2 step 6 (e2e; needs B1 complete, takes the kind lock) ──► step 7
T-18 WP0 (the wiki check's Go name) ──► alignment of wikigate.go and of the marker (development agent, at merge)
```

Two builders at most. B2's component work needs only the OpenAPI document and the fake; its e2e needs B1 on the integration branch. B1 and B2 share no file. If T-18's WP0 has not landed when B1 starts, `wikigate.go` carries its stub
and the development agent changes that one file at the merge.

## 5 · Tests, by tier

| Tier | What | Proves | Owner |
|---|---|---|---|
| Unit | `docgen` over crafted sources (R-ANS-034's list), the markup table and hostile inputs, fuzz targets for scanner, YAML and converter; `go list -deps` without `os/exec` | R-ANS-034, R-ANS-039 | B1 |
| Unit | `galaxydoc` golden documents (excerpts of the two real documents), `Rewrite` of a stored document with `<script>`, the tree/entry builders, `q` validation, the wiki gate registry and the denying fake, route table counts, the kit's Markup | R-ANS-035 to R-ANS-037, R-ANS-039 | B1 |
| Unit (web) | tab choice by `metadata.docs`; tree grouping and counts; search debounce and states; entry sections; text-only binding; keyboard; narrow layout | R-UI-060 to R-UI-064 | B2 |
| Integration | `ansiblelocal`: publish → tree, entry, file, search, `docs-blob/`, delete, reindex, warnings in the task, denying gate; `bound` collection of 6,000 files | R-ANS-033, R-ANS-034, R-ANS-036, R-ANS-037, R-ANS-043, R-ANS-044 | B1 |
| Integration | `ansibleremote` against the stub: the fetch after the version document, one stub request however many reads and replicas, byte-identical stored document, sanitized serving, `fetchDocumentation: false`, 404, 500, huge, `href` on another origin, a mirror pass that queues, a bad document keeps the previous | R-ANS-035, R-ANS-040, R-ANS-042 | B1 |
| Integration | `ansiblevirtual`: the winner's documentation, first match for a version in two members, a member whose wiki the denying fake closes | R-ANS-041, R-ANS-037 | B1 |
| e2e (kind) | **local**: `ansible-galaxy collection publish` and `install` of the built collection, `docs-blob/` with the token, the Documentation, Contents and Import log tabs; **remote**: install through the remote over the stub, the tabs after the job, the script tag gone; **virtual**: install through the virtual repository, the tabs from the winner; **wiki**: a user who may read the artifacts and not the wiki installs, sees no tab, `docs-blob/` as R-AUTHZ-034 decides | R-ANS-046, R-UI-060 to R-UI-064 | B2 |

The e2e rows are the proof against the defect class that integration missed in F0 (client behaviour); each asserts the first page's content, not the tab's presence.

## 6 · Gates (each builder, once, before its commit)

`go test` of the touched packages, `go test ./internal/archtest/...`, `go test ./internal/tools/coverage/`; the touched integration areas through the slot wrapper; lint with the pinned binary; `make cyclo`; `make generate-check`
(sqlc, oapi-codegen); `go run ./internal/tools/coverage -links`; the white-label scan; for B2 `make web-test` and `make web-lint`. B2's e2e leg runs on kind; a red leg is a finding. Test names carry R- IDs only.

## 7 · Lines the development agent applies (no builder edits these files)

- `promt.md` section 10, T-19: "specified 2026-11-11 (R-ANS-033 to R-ANS-046, R-UI-060 to R-UI-064, Q-ANS-56 to Q-ANS-70, Q-UI-240 to Q-UI-247, Q-API-260 to Q-API-263, ADR-196, ADR-197, H-890 to H-899); plan:
  galaxy-docs-work-packages.md; two builders; align `wikigate.go` and the `x-trove-part` marker with T-18's WP0 at the merge".
- `FULL.md` block F0 gains: "🟡 Documentation of collections (Documentation, Contents and Import log tabs, `docs-blob/`, generated for local, kept for remote, wiki gated)".
- The "Changes to other pages" table of ansible.md is applied by the builder named in it (B1 for backup, architecture, operations; the development agent for authorization).
