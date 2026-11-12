# Ansible Galaxy collection documentation

Status: built in the backend (owner task T-19; requirements [R-ANS-033 to R-ANS-046](../spec/formats/ansible.md), work packages in
[galaxy-docs-work-packages.md](../projektplan/galaxy-docs-work-packages.md)). The screens are not part of this page's mechanism yet; the wiki check is
the one of R-AUTHZ-034 (`Host.WikiReadable`, `authz.ReadScope.MayReadWiki`; see [visibility.md](visibility.md)).

This page describes what the text of a collection's documentation can make trove do and show, and who may read it. Two parties write that text and
neither is trusted: a **publisher** (the plugin sources, the README and the `docs/` files of an archive, read at publish) and an **upstream Galaxy** (the
`docs-blob/` document of a remote repository). The reader is any caller who may read the repository, and a browser on trove's own origin. The archive
reader's bounds are on [ansible.md](ansible.md); the remote engine's checks on [remote-repositories.md](remote-repositories.md) and
[ansible-remote.md](ansible-remote.md); the README renderer's policy is the one of [R-UI-008](../spec/api-and-ui.md); who may read the wiki part is
decided by [permissions.md](permissions.md) (R-AUTHZ-034). The code is `internal/formats/ansible/docgen` (the generator and the
markup converter), [internal/formats/ansible](../../internal/formats/ansible) (`wikigate.go`, the route and the fill), the data port
[internal/formatdata/ansible](../../internal/formatdata/ansible) and the operations in [internal/api/v1](../../internal/api/v1).

## What reads the publisher's text, and what it cannot do

| Step | What is decided | Where | Result |
|---|---|---|---|
| 1 | Only text is read: members under `plugins/<type>/`, `roles/<role>/README.md`, `README.md`, `docs/*.md`, sidecar `.yml` files. **Nothing is executed**: no `ansible-doc`, no Python, no import of a plugin, no template. `go list -deps` of the generator names no `os/exec` (a test) | `docgen` | A crafted source is data |
| 2 | A source is read up to 2 MiB, a literal up to 512 KiB, 256 MiB of source per archive, 64 Markdown files; the rest of a member is discarded while it streams through its digest | the archive reader's tap | A bound crossed drops that part with a warning and never fails the publish |
| 3 | The three strings are found by a lexical scan of module-level assignments of one string literal; an f-string, a call, a name or a literal inside a function is not read | `docgen` | No expression is evaluated; the entry is listed without documentation |
| 4 | `DOCUMENTATION` and `RETURN` are decoded as a YAML node tree and walked, never into Go values; an anchor, alias, tag, merge key, more than 20,000 nodes or nesting beyond 24 refuses that entry | the walker of `meta/runtime.yml` | The entry has no documentation, a warning says why |
| 5 | The result is stored as structured text and Markdown, never as HTML | `ansible_doc_entries`, `ansible_doc_files` | Nothing stored can run |
| 6 | Names are values of columns; no name builds a path, a statement or an object-store key | the data port | A hostile name is a string |

## What reaches a browser

| Text | Path | Control |
|---|---|---|
| A generated README or `docs/` file | Markdown row, rendered per request | goldmark and the README allowlist, link and image rules of R-UI-008 |
| The prose of a plugin | Ansible markup in the row | `docgen.RenderMarkup` escapes all text first and recognizes a fixed macro set, links only for `http` and `https`; then the same allowlist |
| An upstream's `html`, `readme_html` | Stored as received, sanitized **on every serving**, by `docs-blob/` and by the file operation | `Host.Markup().SanitizeHTML`, the allowlist of R-UI-008 |
| An example, a sample, a default | JSON text | The UI binds it as text in a code block; it is never highlighted or evaluated |

The format cannot sanitize on its own (R-ARCH-008 refuses it the renderer): the Host hands it the one implementation ([Q-ANS-66](../spec/formats/ansible.md#q-ans-66--the-host-gains-a-markup-capability-because-a-format-may-import-neither-the-renderer-nor-bluemonday)),
so there is one policy, and a policy change applies to stored upstream documents at once because nothing sanitized is stored.

## What an upstream can make trove do

| Step | What is decided | Where | Answer |
|---|---|---|---|
| 1 | The address of the document is the version document's `href` plus `docs-blob/`, recorded as a URL-map row when the version document is filled and never taken from a request; its host is the upstream's or an `extraHosts` entry and its path ends in `/versions/<version>/docs-blob/` ([Q-ANS-73](../spec/formats/ansible.md)) | `PrepareFill` of the version document | Otherwise no row is recorded, the fetch row says `none` and the route and the job send no request |
| 2 | The document is at most `MaxDocumentBytes` (16 MiB) and its shape is checked before anything is stored: at most 5,000 items, valid names and types, 1 MiB kept per entry | `PrepareFill` | 502, the previous entry stays |
| 3 | The credential, egress rules, the rate limit, the circuit and the one-flight rule are the engine's | the engine | As for every document |
| 4 | A screen never contacts the upstream; only a client's request for the route and the job `ansible-docs-fill` do | the operations read rows | A viewer cannot make trove fetch |

## Who may read it

The documentation is the wiki part of the repository. Every surface that serves it asks **the wiki check of R-AUTHZ-034** through `wikiReadable` in
`wikigate.go`, and a registry test proves that each listed surface does ([R-ANS-037](../spec/formats/ansible.md#r-ans-037--the-documentation-is-wiki-content-which-routes-documents-and-fields-the-wiki-check-gates)).
The version document, the lists and the archive are not wiki and follow the artifact check. For a virtual repository the check is asked for the repository
addressed and for the winning member.

## What this does not cover

### H-900 · A remote version's documentation outlives the eviction of its archive

The rows of a remote version are deleted with the repository and by the purge of the remote caches (R-BAK-008), and not when the cached archive of the version is evicted or a mirror drops the version: the remote class has no removal hook for them. Live today for every remote repository that evicts archives; real for a viewer who sees the documentation of a version whose archive is gone, and for the storage of the rows (they stay small next to an archive). The stored document of the engine follows its own retention.

### H-901 · A virtual repository falls through to a later member's documentation

The route walks the members by tier and position and serves the first that has documentation ([Q-ANS-74](../spec/formats/ansible.md)). A member that holds the version and has none does not stop the walk, so a later member that holds the same number with other bytes can supply the documentation. Live today where two members hold one version number with different archives (the digest conflict counter of R-ANS-022 counts those); the documentation never mixes members within one answer.

### H-902 · The screens' choice of a repository ignores the pin rules and claims of a virtual repository

`resolveDocs` takes the members of a virtual repository in the order of their tiers and the repositories of the caller's read scope; it does not apply the claims and pin rules (R-REPO-023) that keep a name away from a member. Live today for a virtual repository with a claim for a name that a remote member also serves: the Documentation tab may show that member's documentation of the number while an install through the virtual repository takes the claiming member's archive. The protocol route is not affected (it asks the resolver's plan).

### H-903 · The operations' metrics and the job's failures are not counted

`trove_ansible_docs_read_total` counts the route and `trove_ansible_docs_fill_total` the fills of the request path; the three management operations and the failures of the job `ansible-docs-fill` are not counted (R-ANS-045 names them). Live today for every installation; it hides a failing fetch job from the dashboards until the fetch row is read.

- [H-890](../spec/formats/ansible.md#h-890--the-size-of-the-documentation-of-a-full-mirror-and-of-many-local-versions-is-not-measured): the size of a full mirror's documentation is not measured.
- [H-891](../spec/formats/ansible.md#h-891--documentation-fragments-are-not-merged-into-the-documentation-of-a-generated-entry): fragments are not merged into a generated entry.
- [H-892](../spec/formats/ansible.md#h-892--the-generator-reads-literals-not-python-and-does-not-read-role-argument-specs): the generator reads literals, not Python.
- [H-893](../spec/formats/ansible.md#h-893--an-upstreams-html-and-the-sanitizer-policy-can-differ-in-what-they-keep): an upstream's HTML and the policy differ in what they keep.
- [H-894](../spec/formats/ansible.md#h-894--the-wiki-gate-registry-proves-the-surfaces-it-lists-not-that-it-lists-every-surface): the registry proves the surfaces it lists.

### H-895 · A request for `docs-blob/` of a public remote makes trove fetch up to 6.5 MB per version

Stage: the route on a remote repository (R-ANS-035). A caller who may read the repository, and an anonymous caller where the repository is public, can
request the document of any version the remote's version document names; the engine fetches it from the upstream and stores it. The engine's
one-flight rule, the negative entry, the circuit and the rate limit apply, and `fetchDocumentation: false` closes the route, but there is no per-caller
budget for large documents. Live for a public remote of an upstream that serves large documents (fortinet.fortios 2.6.0: 6.5 MB); dormant for a repository
that requires authentication. What makes it real: an anonymous reader walking the versions of a large collection.

### H-896 · The jobs that fetch documentation are not capped per repository

Stage: `OnFill` of a version document queues `ansible-docs-fill` (R-ANS-042). A client that asks for the version documents of many versions queues a job for each
(coalesced per version, not per repository), and a mirror queues one per kept version. The window and the engine's throttle space the requests; nothing caps the
number of queued jobs or the bytes a repository's documentation may take. Live for a remote that proxies a client resolving many versions of a large collection;
bounded by the mirror's own retention rule for a mirror.

### H-897 · The displayed documentation is what the publisher's literal says, not what Ansible would load

Stage: the generator (R-ANS-034). The scan reads module-level string literals and, where there are several assignments, the last one, as Python does for a
straight-line module. A module that assigns `DOCUMENTATION` under a condition, or replaces it at import, shows trove's reading and not ansible-doc's. The
publisher can already ship code that runs on the controller, so the text carries no new capability, but a reader who trusts the page more than the source is misled.
Live for every generated entry whose source is not straight-line; the page states the source of the documentation (generated by trove, not by ansible-doc).

### H-898 · The kit's Markup is a stand-in and proves wiring, not the policy

Stage: the conformance kit's Host (R-ANS-039). Unit tests of the format run against a deterministic in-memory `Markup`; the real allowlist is exercised only by the integration
areas and the e2e rows that go through the composition root. Live for every test that does not; a change that weakens the real policy is caught by the README renderer's own tests
and the e2e row with the stub's `<script>`, not by the format's unit tests.

### H-899 · An upstream's search text is stored from its prose

Stage: `ansible_doc_entries.search_text` (R-ANS-033). The text is the upstream's or the publisher's, cut to 4 KiB and never rendered, but it is stored per entry and
indexed, so a hostile document of 5,000 entries holds 20 MiB of it besides the entries themselves. The per-version bound of 16 MiB counts `doc`, Markdown and `search_text`
together, so the figure is bounded; the cost of the index on a large mirror is part of H-890.
