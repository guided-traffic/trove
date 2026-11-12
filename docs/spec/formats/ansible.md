# Ansible Galaxy repositories

Status: specification, building (block B, unit F0 of [FULL.md](../../projektplan/FULL.md); the work packages are in
[ansible-work-packages.md](../../projektplan/ansible-work-packages.md)). Built: the format skeleton and the local class (publish, import
tasks, the three read operations, tombstones, the reindex, the package screens). Not built yet: the remote class, the virtual class and
the client surface. It is the first registry after the beta because it replaces a Galaxy system the owner runs today.
The documentation of a collection version (the Documentation and Contents tabs, R-ANS-033 to R-ANS-046, owner task T-19) is built in the backend (the generator, the rows, the route
`docs-blob/` for the three classes, the three operations, the fetch job and the counting stub); the screens and the end-to-end rows are the work of builder B2. The plan is
[galaxy-docs-work-packages.md](../../projektplan/galaxy-docs-work-packages.md).

This page specifies how trove serves Ansible content to `ansible-galaxy`: the format `ansible` for local, remote and virtual
repositories; the subset of the Galaxy API that `ansible-galaxy collection install`, `collection publish`, `role install` and a
`requirements.yml` call; how a local repository takes collection archives and validates them; how a remote repository proxies the
public Galaxy (and a second Galaxy-compatible hub) and what a mirror of it keeps; how roles are served and what is left out; how a
virtual repository merges its members and takes publishes; how trove tokens reach the Galaxy clients; what trove can say about the
digest, the signatures and the vulnerabilities of a collection; how the content of an existing Galaxy instance is imported; the
client setup instructions; and the test suites. Mechanisms shared by all formats are owned by other pages and only refined here:

| Topic | Owner |
|---|---|
| Repository model, virtual resolution, remote request path, URL rewriting, mirror engine, the reindex operation | [repositories-and-proxy.md](../repositories-and-proxy.md) (R-REPO-016 to R-REPO-029, R-REPO-041, R-REPO-043, R-REPO-048, R-REPO-050 to R-REPO-059) |
| Items, blobs, the upload pipeline, the trash can, garbage collection | [storage.md](../storage.md) (R-STOR-003 to R-STOR-005, R-STOR-012, R-STOR-022) |
| Credential sources, challenges, tokens, anonymous access | [identity-and-authentication.md](../identity-and-authentication.md) (R-AUTH-001, R-AUTH-002, R-AUTH-033) |
| Actions, patterns, ownership, claims, the virtual gate | [authorization.md](../authorization.md) (R-AUTHZ-005 to R-AUTHZ-009, R-AUTHZ-016, R-AUTHZ-018) |
| Format contract, untrusted input, read budget, jobs | [architecture.md](../architecture.md) (R-ARCH-008 to R-ARCH-012, R-ARCH-016, R-ARCH-027, R-ARCH-028) |
| Export classes, revision columns, revocation journal | [backup-and-restore.md](../backup-and-restore.md) (R-BAK-004, R-BAK-008, R-BAK-024) |
| The copy of packages from an existing registry | [import.md](../import.md) (R-IMP-001 to R-IMP-009) |
| Vulnerability scanning | [vulnerability-scanning.md](../vulnerability-scanning.md) |

Covers: [formats/ansible.md](../../features/formats/ansible.md) (`PKG-ANSIBLE-*`); the section [Catalog coverage](#catalog-coverage) lists
every entry with what is built, what stays and what is not offered.

External references, normative wherever this page does not say otherwise: the Galaxy API as the public Galaxy and the
[galaxy_ng v3 API documentation](https://docs.ansible.com/projects/galaxy-ng/en/latest/community/api_v3.html) describe it, and the
behaviour of `ansible-galaxy` in ansible-core (section "Client and library behaviour this page relies on"); [semantic versioning
2.0.0](https://semver.org/spec/v2.0.0.html); [netrc](https://www.gnu.org/software/inetutils/manual/html_node/The-_002enetrc-file.html).
The Galaxy protocol is a third-party standard; trove implements the subset below and no more.

## Conventions on this page

| Placeholder | Meaning |
|---|---|
| `<base>` | trove's public base URL of the endpoint the request arrived on; `https://trove.example.com` in examples (# example) |
| `<repo URL>` | `<base>/api/ansible/<projectKey>/<repoKey>/` with its trailing slash; this is the Galaxy server URL a client is configured with, and route paths below are relative to it |
| `<ns>`, `<name>` | the namespace and the name of a collection (`community`, `general`); the collection is `<ns>.<name>` |
| `$TROVE_TOKEN` | a trove token (`trv_pat_…`, `trv_sat_…` or `trv_cit_…`, INV-8) |

Project and repository keys used in examples (# example): the project `web` with `ansible-local` (local), `galaxy` (remote of
`https://galaxy.ansible.com/api/`, mode `pullThrough`), `galaxy-mirror` (remote of the same upstream, mode `mirror`) and `ansible`
(virtual over `ansible-local` and `galaxy`, deploy target `ansible-local`). A client is configured with the server URL
`https://trove.example.com/api/ansible/web/ansible/`.

Priorities: every requirement below is **P1**, as the catalog page rates its entries and as the owner set the unit F0.

## Client and library behaviour this page relies on

<details>
<summary>Verified on 2026-11-10 against the public Galaxy and the source of ansible-core, and what is not verified yet</summary>

Versions checked: the **public Galaxy** (`https://galaxy.ansible.com/api/`, answering on 2026-11-10); **ansible-core 2.21.6**
(the newest on PyPI that day; its `ansible/galaxy/api.py`, `token.py`, `role.py`, `collection/concrete_artifact_manager.py`,
`cli/galaxy.py` and `module_utils/urls.py` were read from the wheel); **ansible-core 2.17.14** (the oldest the catalog names; the same
files were compared with 2.21.6 and the differences listed below); **ansible-core 2.15.8** (installed on the development host; a
collection and a role were installed from the public Galaxy with `-vvvv` and the requests read from the log). The wire behaviour of
2.17 to 2.21 against trove is **not** yet observed; the e2e set (R-ANS-032) proves it.

| Fact | How it was verified | Where this page uses it |
|---|---|---|
| The client first requests the configured server URL and expects a JSON object with `available_versions` (2.21.6); when that fails, or a `GalaxyError` is raised, it requests `<url>/api/` once and uses that URL as the API root; 2.17.14 without `available_versions` assumes `v1` and `v2`, 2.21.6 treats it as an error | `g_connect` in `api.py` of 2.21.6 and 2.17.14 | The repository URL answers the discovery document itself (R-ANS-003, Q-ANS-3) |
| The public Galaxy answers `GET /api/` with `{"available_versions":{"v3":"v3/","v1":"v1/"}}` | `curl` | The shape of the discovery document |
| Every later URL is `<api root>` + the value of `available_versions[v3]` + `collections/<ns>/<name>/versions/?limit=100`, then `.../versions/<version>/`, with a trailing slash; a `next` link of the version list is followed after `urljoin(api root, link)`; a link without scheme must start with `/` | `get_collection_versions`, `get_collection_version_metadata` in 2.21.6; the request log of 2.15.8 (`/api/v3/collections/geerlingguy/mac/versions/?limit=100`, then `.../versions/5.0.0/`, then the download) | R-ANS-004 |
| The fields of a version document that the client reads: `namespace.name`, `collection.name`, `version`, `download_url` (must be absolute or start with `/`), `artifact.sha256`, `metadata.dependencies`, `href`, `signatures` (list of objects with `signature`), `requires_ansible`; of the version list: `data[].version`, `data[].requires_ansible` and `links.next`; of the collection document: `updated_at`, `created_at` (cache invalidation) | `get_collection_version_metadata`, `get_collection_versions`, `get_collection_metadata` in 2.21.6 | The documents trove renders (R-ANS-004) |
| The public Galaxy serves `GET /api/v3/collections/<ns>/<name>/...` as a **302** to `/api/v3/plugin/ansible/content/published/collections/index/<ns>/<name>/...`; its version list has `meta.count`, `links.{first,previous,next,last}` and `data[]` ordered newest first, `limit` at most 100 on the collection index (the version list's cap was not probed); a missing collection answers 404 `{"errors":[{"status":"404","code":"not_found","title":"Not found."}]}` | `curl -D -` | The remote adapter follows redirects of the upstream; the error shape (R-ANS-007) |
| The version document of the public Galaxy is large (community.general 10.0.0: 848,148 bytes, because `metadata.contents` lists every plugin) and its `download_url` is `https://galaxy.ansible.com/api/v3/plugin/ansible/content/published/collections/artifacts/<ns>-<name>-<version>.tar.gz`, which answers 302 to a signed `https://galaxy.ansible.com/api/pulp/content/published/<file>?expires=…&validate_token=…` on the same host | `curl` | Bound of a stored document and the trimmed rendering (R-ANS-017, Q-ANS-10) |
| The documentation of a version is **not** in the version document. The public Galaxy and the owner's galaxy_ng serve it at the version's `href` plus `docs-blob/` (`…/collections/index/<ns>/<name>/versions/<version>/docs-blob/`) as `{"docs_blob":{"contents":[…],"collection_readme":{"name","html"},"documentation_files":[{"name","html"}]}}`. The version document carries `metadata.contents` (`name`, `description`, `content_type` per entry) and, outside `metadata`, `manifest`, `files`, `git_url` and `git_commit_sha`; it has no `docs_blob` member (checked on both servers). An item of `docs_blob.contents` is `{content_name, content_type, doc_strings, readme_file, readme_html}`; `doc_strings` is `{doc, return, examples, metadata}` in the form ansible-doc prints it, except that `doc.options` and `return` are lists of named objects (`name` plus the fields) with `suboptions` and `contains` nested the same way; `readme_*` are set for roles. Sizes of the `docs-blob/` answer on 2026-11-11: community.general 10.0.0 3,472,149 bytes (833 entries: 591 module, 77 module_utils, 42 doc_fragments, 34 lookup, 26 callback, 21 filter, 13 inventory, 10 become, 10 connection, 4 cache, 3 test, 2 action; 709 with `doc_strings`, a median entry 3.6 KB, the largest 59 KB), community.general 14.0.1 3.3 MB, fortinet.fortios 2.6.0 6.5 MB, amazon.aws 12.1.0 2.0 MB, cisco.ios 11.6.0 1.2 MB, ansible.netcommon 8.7.1 155 KB. The owner's `hans_fischer.common` 1.5.0: 4,122 bytes, 32 entries (8 module, 8 filter, 16 module_utils), **every `doc_strings` null**, `collection_readme.html` set, `documentation_files` empty | `curl` (GET, 2026-11-11); `galaxy_importer/schema.py` (`DocsBlob`, `DocsBlobContentItem`) and `loaders/doc_string.py` (`_transform_doc_strings`) of galaxy-importer, master | R-ANS-033 to R-ANS-036, Q-ANS-56 |
| The importer produces the document at import, not from the archive as such: it unpacks the collection into a collections tree and runs the program `ansible-doc --json` of its own environment once per plugin type (`become`, `cache`, `callback`, `cliconf`, `connection`, `filter`, `httpapi`, `inventory`, `lookup`, `shell`, `module`, `strategy`, `test`, `vars`); without that program it logs a warning and leaves `doc_strings` empty (the state of the owner's collection). A content item exists for every file of `plugins/<type>/`, for `module_utils` and `doc_fragments` too, which have no `doc_strings`. The README is `README.md`, the documentation files are the `.md` files directly in `docs/`, each at most 512 KiB, rendered by Python-Markdown (`extra`) and sanitized by nh3 against a small allowlist; **reStructuredText is not rendered** (`DOCFILE_EXTENSIONS = [".md"]`). A content name must match `^(?!.*__)[a-z_][0-9a-z_]*$`. A role has a `readme_html` from the `README.md` of the role | `galaxy_importer/loaders/doc_string.py`, `loaders/content.py`, `utils/markup.py`, `constants.py` of galaxy-importer, master | Q-ANS-57, Q-ANS-58, Q-ANS-64 |
| Whether `ansible-doc` executes plugin code was **not verified** here: for modules it reads the `DOCUMENTATION`, `EXAMPLES` and `RETURN` strings from the source without importing it; for some plugin types it loads the plugin. Fragments (`extends_documentation_fragment`) are merged by ansible-doc, from the same collection, from `ansible.builtin` and from other collections | reading the importer; ansible-doc itself was not read | Q-ANS-58, H-891 |
| Publishing is `POST <api root>v3/artifacts/collections/` with `multipart/form-data` of two parts, `sha256` (the hex digest of the archive) and `file`; the answer is JSON with `task` (resolved with `urljoin(api root, task)`); the client then polls that URL with GET, reading `state`, `finished_at`, `messages[].level/message` and, when `state` is `failed`, `error.code` and `error.description`; a 404 while polling is retried | `publish_collection`, `wait_import_task` in 2.21.6 | R-ANS-008, R-ANS-009 |
| The galaxy_ng documentation shows the part order `file`, then `sha256`; the client sends `sha256` first | the documentation page of the v3 API; `prepare_multipart` call in 2.21.6 | A publish handler accepts both orders (R-ANS-008) |
| The client sends a configured `token=` or `--token` as `Authorization: Token <token>` (class `GalaxyToken`), a configured `username` and `password` as Basic, and a `token` with `auth_url` as a Bearer token it obtains from that URL with a refresh-token grant; the download of the archive carries the same header with `unredirected_headers=['Authorization']`; a role archive is downloaded with `open_url` **without** that header, which falls back to `~/.netrc` (a preemptive Basic header for the host) | `token.py`, `cli/galaxy.py` lines 650 to 690, `_download_file`, `role.py` `fetch`, `urls.py` `_configure_auth` in 2.21.6 | R-ANS-006, R-ANS-015 |
| A token can come from the environment: the variable is `ANSIBLE_GALAXY_SERVER_<SERVER>_<KEY>` with the server name and key upper-cased (`ANSIBLE_GALAXY_SERVER_TROVE_TOKEN`); `ansible.cfg` itself has no variable expansion for the token | `load_galaxy_server_defs` in `config/manager.py` of 2.21.6 | The setup snippets (R-ANS-025) |
| `role install <owner>.<role>` requests `GET <api root>v1/roles/?owner__username=<owner>&name=<role>` (owner is everything before the last dot), then `v1/roles/<id>/versions/?page_size=50`, follows `next_link` relative to the host root, and downloads `download_url` of the chosen version (or the `github_user`/`github_repo` archive of the role); `RoleDistributionServer` takes the first server whose `available_versions` has `v1` | `lookup_role_by_name`, `fetch_role_related`, `role.py` in 2.21.6; the request log of 2.15.8 | R-ANS-014, R-ANS-020 |
| On the public Galaxy a role's `download_url` is a GitHub archive (`https://github.com/<user>/<repo>/archive/<tag>.tar.gz`), not a file Galaxy stores; GitHub answers 302 to `codeload.github.com`; two fetches of one tag a few seconds apart had the same SHA-256 | `curl` on `/api/v1/roles/16/versions/` and on the archive | Roles carry no digest (R-ANS-014, H-840) |
| The signatures of a version are `signatures[]` of its document; the client verifies them locally against `--keyring` with `gpg` over the `MANIFEST.json` of the archive; without a keyring it skips verification | `collection/__init__.py` `verify_file_signatures`, `gpg.py` in 2.21.6 | R-ANS-013, Q-ANS-14 |
| The collection name grammar of the client is `[a-zA-Z0-9_]` for namespace and name; trove's stricter rule is its own decision | `validate_collection_name` in 2.21.6 | Q-ANS-5 |
| Galaxy errors on v3 URLs are read as `errors[].detail`, `.title`, `.code`; on v1 URLs as `default` | `GalaxyError` in 2.21.6 | R-ANS-007 |
| OSV lists no Ansible ecosystem (the list of 2026-11-10 has Go, Maven, npm, NuGet, PyPI, … and no Ansible; `Ansible/all.zip` answers 404) | `curl` on `ecosystems.txt` | Q-ANS-15, H-841 |
| ansible-core 2.17.14 and 2.21.6 differ in `api.py` only in the `v2` fallback and in how a retry error is read; `token.py` and the role code are the same for the calls above | `diff` of the two wheels | The supported matrix (R-ANS-001) |

Not verified, and to be proven by the package that builds the mechanism:

- the import task document beyond what the client reads (galaxy_ng's own serializer was not read; the fields `id`, `state`,
  `created_at`, `started_at`, `finished_at`, `error`, `messages` come from the client and from the catalog);
- the Red Hat Automation Hub: its API root URL, whether a client's `url` for it answers `available_versions` directly, and the token
  exchange of an offline token (R-ANS-019, H-842); no Red Hat account was available;
- the grammar Galaxy applies to namespaces and names when publishing, and its maximum upload size;
- the size of a full mirror of the public Galaxy: 4,685 collections were listed on 2026-11-10 (`meta.count` of the collection index); the
  versions and bytes were not counted (H-846);
- the behaviour of ansible-core 2.17 to 2.21 against trove, `ansible-galaxy collection download` and `verify`, and `--ignore-certs`;
- whether the core accepts a package-less metadata document and a package name with a slash for the role documents (R-ANS-020).

</details>

## Requirements

### R-ANS-001 · The format `ansible`, its classes and the repository address
Priority: **P1** · Covers: PKG-ANSIBLE-001

trove MUST offer the format `ansible` for all three repository classes of INV-5 ([Q-ANS-1](#q-ans-1--one-format-ansible-at-apiansible-on-the-page-formatsansiblemd-with-the-prefix-r-ans)).

Format registration (R-REPO-003):

| Fact | Value |
|---|---|
| ID, display name, layout | `ansible`, `Ansible Galaxy`, `ansible` |
| Classes | `local`, `remote`, `virtual` |
| Protocol base path | `/api/ansible/{project}/{key}/` |
| Upstream presets ([R-REPO-062](../repositories-and-proxy.md#r-repo-062--upstream-presets-of-a-format)) | Ansible Galaxy (`galaxy`, `https://galaxy.ansible.com/api/`, an API root, R-ANS-016, the default), with the extra hosts `github.com` and `codeload.github.com`, without credentials (role archives, R-ANS-020); probe path empty (the URL itself answers the discovery document) |
| Cache periods of a remote | metadata 600 s, not-found 300 s (the periods of the other formats for a mutable index) |
| Pattern subject | `name`: the collection `<ns>.<name>` |
| Package names | yes: the normalized `<ns>.<name>` (R-ANS-002); the documents of roles have a package name of their own (R-ANS-020) |
| Priority members, pin rules, namespace claims | yes (R-ANS-022); repository caps no |
| `packageOwnership` default of a local repository | `enforce` |
| Package document | `collections/<ns>/<name>/collection.json`: a remote answers the shadowing check by asking for it (R-REPO-048) |
| `ChecksumAlwaysVerified` | `true` (R-ANS-018: the archive digest is part of the protocol); the policies `ignore` and `pass-through` are refused with 422 `not_applicable` |
| Data port | `internal/formatdata/ansible` (R-ANS-011) |
| Settings | the member `ansible` of the repository document (R-ANS-019 for a remote of a hub); no setting for local and virtual repositories |
| Clients | `ansible-galaxy` of ansible-core 2.17 and later, of the Ansible community package 10 and later (the catalog's matrix); the e2e images pin ansible-core 2.17.14 and 2.21.6 (R-ANS-032) |

- A local repository hosts an organisation's own **collections**. A remote repository proxies collections of a Galaxy-compatible
  server, and roles of the public Galaxy by translation (R-ANS-020). A virtual repository merges local and remote members behind one
  server URL (R-ANS-022).
- **The federated form** of the catalog is not built; no federation exists, and no signing key is offered for the format (Q-ANS-14).
- Creating a repository needs the system administrator or a project administrator role (R-REPO-001); UI location: administration,
  repositories, create repository, class and format (UI-031).

**Address.** A client names the repository URL as its Galaxy server (`url=` in `ansible.cfg`, `-s`/`--server`); the repository URL
itself answers the discovery document (R-ANS-003). The stored items are readable by path at `/files/<project>/<repo>/` as for any
format (R-STOR, R-EP-014); `/files/` deploys nothing into the repository (R-STOR-007, [Q-ANS-34](#q-ans-34--a-collection-is-published-through-the-galaxy-route-only-a-raw-body-is-the-scripted-form)).

Acceptance criteria:

- `GET /api/v1/formats` lists `ansible` with the three classes and the settings schema of R-ANS-019.
- `ansible-galaxy collection install community.general -s https://trove.example.com/api/ansible/web/ansible/` resolves against a
  virtual repository with a local and a remote member.

### R-ANS-002 · Names, versions and canonical paths
Priority: **P1** · Covers: PKG-ANSIBLE-002, PKG-ANSIBLE-006

Collection identity ([Q-ANS-5](#q-ans-5--collection-names-are-lower-case-versions-are-semantic-versions-the-archive-has-one-canonical-path)):

- **Namespace and name**: each `[a-z0-9_]+`, 1 to 64 characters, not starting with `_`, and with at least one letter. `Names.Normalize`
  folds upper case to lower case for a *request* (a remote may serve what the client's own grammar `[a-zA-Z0-9_]` accepts) and
  refuses anything else. The package name is `<ns>.<name>` (no dot occurs inside a part, so it parses back uniquely). A *publish* of a
  name with upper case, or a part that breaks the rule above, is refused 422 `invalid_name` (Galaxy's own rule was not verified; the
  rule is trove's).
- **Version**: a semantic version 2.0.0 string (`1.2.3`, `1.0.0-rc.1`, `2.0.0+build.5`); `ansible-galaxy` requires it. Stored as
  written; precedence is by semver rules with build metadata ignored; two versions that differ only in build metadata are the same
  version for immutability (R-ANS-008). A version that is not semver is refused 422 `invalid_version`. The list order of a version
  list is precedence **descending** (newest first, as the public Galaxy lists).
- **Canonical paths** (the item paths, R-ARCH-005), all lower case:

| Path | What | Class |
|---|---|---|
| `collections/<ns>/<name>/<ns>-<name>-<version>.tar.gz` | the collection archive; media type `application/gzip` | local item; remote item (cached artifact) |
| `collections/<ns>/<name>/collection.json` | the collection document | remote (stored upstream document); local: computed, no item |
| `collections/<ns>/<name>/versions.json` | one page of the version list; the variant is `offset=<n>`, n a multiple of 100 | remote (stored); local: computed |
| `collections/<ns>/<name>/<version>.json` | the document of one version | remote (stored); local: computed |
| `api-root.json` | the upstream's discovery document | remote (stored, R-ANS-016) |
| `roles/…` | role documents and role archives of a remote (R-ANS-020) | remote only |

- `Classify` maps `….tar.gz` below `collections/` to an artifact of package `<ns>.<name>` and the version (the file name minus the
  prefix `<ns>-<name>-` and the suffix; a name or a version that does not parse is `PathOther`) and `….json` to metadata of the package.
  A client cannot write a computed path: an upload route that accepts a `.json` path is refused by the kit (R-ARCH-010).
- `VersionPath(name, version)` gives the archive path (Q-AUTHZ-255): a path pattern that hides the archive hides the version.
- **Why the path carries the namespace and name**: a flat file name `ns-name-1.0.0.tar.gz` (what the client downloads) would put every
  collection in one directory; the directory form keeps listings and path patterns per collection.

Acceptance criteria:

- `Normalize("Community.General")` is `community.general`; `Normalize("a.b.c")`, `Normalize("_a.b")` and `Normalize("1.2")` are refused.
- A publish of `Acme-Tools-1.0.0.tar.gz` whose manifest says `Acme.Tools` is refused 422 `invalid_name`; a manifest version `1.0` is
  refused 422 `invalid_version`.
- `Classify("collections/acme/tools/acme-tools-1.0.0-rc.1.tar.gz")` is package `acme.tools`, version `1.0.0-rc.1`.

### R-ANS-003 · The API root and the discovery document
Priority: **P1** · Covers: PKG-ANSIBLE-002

`GET <repo URL>` (and `<repo URL>` without the trailing slash, which the dispatcher routes the same, R-ARCH-005) answers the document
`ansible-galaxy` reads first ([Q-ANS-3](#q-ans-3--the-protocol-routes-are-below-the-repository-url-itself-the-root-answers-the-discovery-document)):

```json
{
  "available_versions": { "v3": "v3/", "v1": "v1/" },
  "current_version": "v3",
  "description": "trove Ansible Galaxy API"
}
```

- `v1` is listed **only** by a remote and a virtual repository. A local repository lists `v3` alone, so that
  `ansible-galaxy role install` with `server_list = local, virtual` skips it and takes the first server that lists `v1`
  (`RoleDistributionServer`, verified); a role install against a local repository alone fails with the client's own message that the
  v1 API is not available, and the guide says why (R-ANS-014, R-ANS-025).
- The document needs `read` on the repository like every other route, so a repository whose artifacts are not `public` answers 401 here and
  the client reports the authentication failure at its first request, not a missing API.
- A client that falls back to `<repo URL>api/` (when the first request fails for another reason) meets 404 on that path; the fallback
  therefore does not hide a first-request failure.
- The paths are relative to the root and carry the trailing slash the client builds (`v3/collections/<ns>/<name>/`). A request
  without the trailing slash is served the same; the response never redirects (a redirect would drop the `Authorization` header of
  some clients).

Acceptance criteria:

- `curl -u x:$TROVE_TOKEN <repo URL>` returns the document; on a local repository without `v1`, on a remote with both.
- `ansible-galaxy collection install` against a private repository without a token fails with a 401 naming the server.

### R-ANS-004 · Collection documents, version lists and version documents
Priority: **P1** · Covers: PKG-ANSIBLE-002

Three read operations, relative to the repository URL, in every class (trailing slash optional); `limit` and `offset` behave as in
the public Galaxy:

| Operation | Path | Output |
|---|---|---|
| Get collection | `v3/collections/<ns>/<name>/` | `href`, `namespace`, `name`, `deprecated` (false), `versions_url`, `highest_version{href,version}`, `created_at`, `updated_at` |
| List collection versions | `v3/collections/<ns>/<name>/versions/?limit=&offset=` | `meta.count`, `links{first,previous,next,last}`, `data[]{version,href,created_at,updated_at,requires_ansible,marks}` newest first |
| Get collection version | `v3/collections/<ns>/<name>/versions/<version>/` | the version document below |

- **The version document** carries what the client reads (section "Client and library behaviour"): `version`, `href`, `created_at`,
  `updated_at`, `requires_ansible`, `marks: []`, `artifact{filename,sha256,size}`, `collection{id,name,href}`, `download_url`,
  `name`, `namespace{name}`, `signatures` and `metadata{authors,dependencies,description,documentation,homepage,issues,license,
  license_file,readme,repository,tags}`. trove omits `metadata.contents` and the members `manifest`, `files`, `git_url` and `git_commit_sha`
  that the public Galaxy writes beside `metadata`: no client in the matrix reads them, and they make a document of the public Galaxy
  848 KB ([Q-ANS-10](#q-ans-10--the-version-document-leaves-out-the-plugin-listing)). The documentation of the version is a document of its own
  at `docs-blob/` below the version (R-ANS-035), not a member of this one.
  `collection.id` is a stable UUID derived from the repository and the name (the local class), or the upstream's (remote).
- **Absolute addresses.** `download_url`, every `href` and every pagination link are absolute URLs on `<base>` (the response base URL of
  the endpoint, R-EP-003) and point to this repository's own routes, never to an upstream or a member, whatever the class. The next
  link of a list carries the same `limit` and the next `offset`; the last one has `next: null`.
- **Limits.** `limit` defaults to 100 and is clamped to 1..100; an `offset` beyond the count returns an empty `data`. A request for an
  unknown collection or version answers 404 in the Galaxy shape (R-ANS-007).
- **Local class.** The documents are computed from the rows of R-ANS-011 on every request, with no rendition cache ([Q-ANS-35](#q-ans-35--a-document-is-computed-per-request-and-its-validator-is-a-digest-of-its-bytes));
  `created_at` is the publish time on the database clock and `updated_at` the newest version's; `deprecated` is always false.
- **Remote class** and **virtual class**: R-ANS-017 and R-ANS-022.
- `ETag`: a computed or rewritten document carries a weak `ETag`, a digest of its bytes (they hold the serving address and every row it
  shows), and answers `If-None-Match` with 304; the client ignores it, other tools do not (Q-ANS-35).

Acceptance criteria:

- After two publishes (`1.0.0`, `1.1.0`), the list is `1.1.0`, `1.0.0` with `meta.count` 2; `limit=1` returns one entry with a `next`
  link that, followed, returns the other with `next: null`.
- The version document's `download_url` starts with `<base>/api/ansible/web/ansible-local/v3/artifacts/collections/` whatever `Host`
  header the request carried (the base URL rule of R-EP-003).
- A collection document of a local collection has `highest_version.version` equal to the newest by semver, not by publish time.

### R-ANS-005 · Collection download
Priority: **P1** · Covers: PKG-ANSIBLE-005

`GET v3/artifacts/collections/<ns>-<name>-<version>.tar.gz` serves the archive in every class (route kind `Download`, action `read`).

- The route reads the file name as `Classify` does (R-ANS-002); a name that does not parse is 404. The response carries the content
  headers of R-ARCH-007 (`ETag`, `Repr-Digest` with the SHA-256, `Accept-Ranges`, `Content-Disposition: attachment`), supports `Range`
  and `HEAD`, and never redirects.
- A **remote** repository serves a cached archive or fetches it by the rules of R-ANS-018; a **virtual** repository walks its members
  (R-ANS-022); a **local** repository serves the stored item.
- The archive's SHA-256 is the digest in the version document (`artifact.sha256`). The client compares it with what it received
  and fails with `Mismatch artifact hash with downloaded file` otherwise (verified in `_download_file`).
- The same bytes are readable at `/files/<project>/<repo>/collections/<ns>/<name>/<file>` (R-STOR, R-EP-014); both routes decide the
  same path with the same grants, and the serve check of R-ANS-029 applies to both.

Acceptance criteria:

- `ansible-galaxy collection install <ns>.<name>:<version>` from a local, a remote and a virtual repository installs an archive whose
  SHA-256 equals the published one.
- A request with `Range: bytes=0-99` returns 206 and the first 100 bytes.

### R-ANS-006 · Credentials as the clients send them
Priority: **P1** · Covers: PKG-ANSIBLE-012, PKG-ANSIBLE-013

Authentication is [identity-and-authentication.md](../identity-and-authentication.md) R-AUTH-001, R-AUTH-002, R-AUTH-033 and R-AUTH-037.
The Galaxy clients send a trove token in these forms; the stage accepts all four on the routes of this format
([Q-ANS-4](#q-ans-4--the-authorization-scheme-token-is-accepted-on-the-routes-that-declare-it)):

| How the client is configured | What goes on the wire | What trove does |
|---|---|---|
| `token=` in `ansible.cfg`, `ANSIBLE_GALAXY_SERVER_<NAME>_TOKEN`, `--token` | `Authorization: Token <token>` | The scheme `Token` is read as the token on a route whose declaration says so (`Route.TokenScheme`); the checks are those of `Bearer` (syntax, lookup, type, binding, scope), the method label is `galaxy_token` |
| `username=` and `password=` | `Authorization: Basic base64(user:password)` | The password is the token; the user name is ignored; needs `auth.basic` (R-AUTH-033) |
| `~/.netrc` (role archive download; also every API call that carries no header) | preemptive `Authorization: Basic` for the host | As Basic; one `machine` line serves every repository on the host |
| `auth_url` with `token=` | Bearer token obtained from `auth_url` by the client | Not a way to reach trove (its tokens are not obtained from an SSO); a `Bearer <trove token>` from `curl` or another tool is accepted |

- **The scheme `Token` is accepted nowhere else.** On a route without the declaration the header is "not a trove token" and rejected
  without a lookup, as today. The declaration is a field of the route (as `APIKey` is for NuGet), so the stage and the dispatcher agree
  by the dispatcher's own match; the session cookie is not accepted on these routes either when the header is present.
- A credential that is present and invalid is a rejection and never falls through to anonymous (R-AUTH-001). A 401 carries
  `WWW-Authenticate: Basic realm="trove"` (the path is below `/api/`), so a client that retries on a challenge sends Basic.
- Anonymous access follows the endpoint's rule (R-AUTH-037) and the repository's `public` artifacts level
  ([authorization.md](../authorization.md) R-AUTHZ-033); an
  anonymously readable repository serves every `GET` route without a token and needs a token for `POST v3/artifacts/collections/`.
- A token bound to another endpoint, of a type the endpoint does not accept, or scoped to a path or a package that the request is not
  in, is refused as for every format (R-EP-010, R-AUTHZ-005).
- **The token never appears in a URL or a setup snippet** (R-API-030): the snippets use the environment variable (R-ANS-025).

Acceptance criteria:

- `Authorization: Token $TROVE_TOKEN` on `GET <repo URL>v3/collections/…` returns 200; on `GET /api/npm/…` it returns 401.
- `ansible-galaxy collection publish` with `token=` in `ansible.cfg` succeeds against a repository with `deploy` for the token's
  principal; the audit event names that principal and `galaxy_token`.
- `ansible-galaxy role install` through a remote repository authenticated only by `~/.netrc` succeeds.

### R-ANS-007 · Permissions per operation and errors in the Galaxy shape
Priority: **P1** · Covers: PKG-ANSIBLE-002, PKG-ANSIBLE-003

Actions ([authorization.md](../authorization.md) R-AUTHZ-007 gains the Ansible table):

| Operation | Action | Also decided |
|---|---|---|
| Discovery, collection, version list, version, download, import task read | `read` | the package and the version path (R-AUTHZ-005); on a virtual repository the member reads (R-AUTHZ-009) |
| `POST v3/artifacts/collections/` | `deploy` | the package `<ns>.<name>` and the archive path (R-AUTHZ-005), ownership of the name (R-AUTHZ-018), the shadowing check of a first publish (R-REPO-048) |
| Role lookup, role versions, role archive | `read` | the repository; role documents carry no package name that a grant can name (R-ANS-020) |
| Publish of the archive as a raw body (`curl --data-binary`) | `deploy` | as the publish |
| Removal of a version (management API and package screens) | `delete` | R-ANS-012 |

**Errors.** `WriteError` renders in the shape the client reads, chosen by the route (R-ARCH-023, [Q-ANS-29](#q-ans-29--the-galaxy-error-shape-renders-the-registry-code-without-its-prefix)):

- below `v3/` and at the root: `{"errors":[{"status":"404","code":"not_found","title":"Not found.","detail":"…"}]}` with
  `Content-Type: application/json`; `code` is trove's registry code without the format's prefix (`ansible_version_exists` is `version_exists`), and the generic
  codes under the names a Galaxy uses (`not_authenticated`, `permission_denied`, `not_found`,
  `invalid_name`, `invalid_version`, `version_exists`, `version_tombstoned`, `sha256_mismatch`, `invalid_archive`,
  `payload_too_large`, `no_deploy_target`, `upstream_unavailable`); `detail` is a sentence a person can act on;
- below `v1/`: `{"default": "<sentence>"}` (the client reads `default`).

The client prints `detail` first, then `title` (verified in `GalaxyError`), so `detail` always carries the reason.

Status codes: 401 and 403 and 404 by R-AUTHZ-016; 405 with `Allow` for a wrong method (a publish to a remote repository, with the
reason in `detail`); 409 for `version_exists` and `version_tombstoned`; 413 for a body over the limit (`payload_too_large`); 422 for
a name, a version or an archive the format refuses; 502 and 503 for an upstream failure of a remote (`upstream_unavailable`, with the
upstream's status in `detail` when it is safe to show).

Acceptance criteria:

- A publish with a token that has only `read` answers 403 with `errors[0].code` `permission_denied`, and `ansible-galaxy` prints that
  detail.
- `GET <repo URL>v3/collections/nope/nope/` answers 404 and the client's `collection install` reports that the collection was not found.

### R-ANS-008 · Publish a collection archive
Priority: **P1** · Covers: PKG-ANSIBLE-003, PKG-ANSIBLE-004

`POST v3/artifacts/collections/` with `multipart/form-data`, parts `file` (the archive) and `sha256` (the hex digest of the archive);
the same route takes the archive as the raw body of the request (any content type but `multipart/form-data`, the digest optionally in the
query parameter `sha256`) for a scripted publish with `curl --data-binary`, with every check below and the same task
([Q-ANS-34](#q-ans-34--a-collection-is-published-through-the-galaxy-route-only-a-raw-body-is-the-scripted-form)); `/files/` takes no upload.

1. **Framing.** The handler reads the parts in either order (the client sends `sha256` first, the documentation shows `file` first),
   streams the `file` part into the spool without buffering it (R-ARCH-014) and bounds the preamble as the NuGet push does. A missing
   part is 400 `invalid_request`. The part `sha256` is compared with the digest of the spooled bytes **before the archive is
   inspected**: a mismatch is 400 `sha256_mismatch` and nothing is stored. Without the part the upload is accepted (a plain `PUT`
   has none). ansible-core sends the `file` part base64-encoded with `Content-Transfer-Encoding: base64`, in lines of 76 characters
   ([Q-ANS-53](#q-ans-53--a-file-part-with-content-transfer-encoding-base64-is-decoded-while-it-is-spooled)), so the handler decodes such a part while it spools it; the digest, the size limits and the stored archive are those of the decoded
   bytes. A part without the header is the raw archive (curl, the documentation).
2. **Inspection** (R-ANS-010) reads identity and metadata from the archive. An archive that cannot be a collection archive is
   recorded as a **failed import task** (R-ANS-009), not as an HTTP error, so that the client prints Galaxy's own failure text.
3. **Authorization** of the identity the archive declares (`deploy` on `<ns>.<name>` and the archive path), ownership of the name
   (`local.autoClaim`, R-REPO-023, R-AUTHZ-018) and the shadowing check of a first version (R-REPO-048) run after inspection and
   before the commit, with no transaction open; they are rechecked in the commit transaction under the package row's lock.
4. **Immutability** ([Q-ANS-9](#q-ans-9--a-published-version-is-immutable-a-deleted-one-leaves-a-tombstone)): the archive at the path of a version that exists is
   refused 409 `version_exists` unless the bytes are identical (same SHA-256), when the publish succeeds again with a task of its own that
   points to the same version (idempotent retry, R-ANS-009), changing nothing else. Two versions that differ only in build metadata are one
   number. A version whose number is tombstoned (R-ANS-012) is refused 409 `version_tombstoned`, before the stored version is looked at.
5. **Commit.** One transaction writes the blob, the item, the package and version rows, the `ansible_collection_versions` row, the
   import task row and the audit event `package.published` with the principal of the token (the transaction of R-ARCH-014). The
   response is `202` with `{"task": "<repo URL>v3/imports/collections/<task id>/"}`, the task being already `completed`.
6. **Where it goes.** A local repository takes it; a virtual repository forwards to its deploy target (R-ANS-024); a remote repository
   answers 405 `method_not_allowed` with a detail that says remotes are read-only.

Limits: the archive size is bounded by the repository and the system upload limit (R-STOR-004) and by `ansible.max_archive_bytes`
(R-ANS-031); a larger body is 413 before it is read when the length is declared.

Acceptance criteria:

- `ansible-galaxy collection publish <file> -s <repo URL> --token=$TROVE_TOKEN` prints the import messages, ends successfully and
  `ansible-galaxy collection install` of the same version from the same server installs it.
- A second publish of the same file answers 202 and installs identical bytes; a publish of another archive with the same version
  answers 409 `version_exists` with `detail` naming the version.
- A publish whose `sha256` part is wrong answers 400 `sha256_mismatch` and `package_versions` has no row.
- `curl --data-binary @file.tar.gz '<repo URL>v3/artifacts/collections/' -H 'Content-Type: application/octet-stream'` stores the same
  version and answers the task; a PUT of the archive to `/files/…` is refused with 409 and stores nothing.

### R-ANS-009 · Import tasks
Priority: **P1** · Covers: PKG-ANSIBLE-003

`GET v3/imports/collections/<task id>/` answers the task of a publish ([Q-ANS-8](#q-ans-8--a-publish-is-processed-in-the-request-the-import-task-is-a-stored-record)). The publish is processed in the request, so a task is
**final when it is created**; the client's polling finds `finished_at` on its first poll and stops.

```json
{
  "id": "6f1c…",
  "state": "completed",
  "namespace": "acme", "name": "tools", "version": "1.0.0",
  "created_at": "…", "started_at": "…", "updated_at": "…", "finished_at": "…",
  "error": null,
  "messages": [{"level": "INFO", "message": "Collection acme.tools 1.0.0 stored (sha256 d3499c…)", "time": "…"}]
}
```

- `state` is `completed` or `failed`. A failed task has `error{code,description}` (the codes of R-ANS-007 plus `invalid_archive`
  with the reason of the inspection) and the messages of the checks that failed; the client prints them and exits non-zero.
- A task is created for every publish that got as far as inspection, including an idempotent retry (a new task pointing to the same
  version) and an archive that failed inspection (state `failed`, nothing stored). A publish refused before that (401, 403, 409,
  413, a wrong `sha256`) is an HTTP error and has no task.
- A task is readable with `read` on the repository whose URL the publish answered. Through a virtual repository the task is looked up in
  its deploy target at the time of the read; a task whose target has changed since is 404 (R-ANS-024). The id is a random UUID and
  unguessable; the document names the collection and the version and nothing else of the archive.
- Tasks are kept for `ansible.import_task_keep` (default 30 days) and then deleted by a periodic task; the version stays.
- `404` for an unknown id; the client retries a 404 while polling (verified), so an id lost to the retention ends in the client's
  timeout, which is the reason the retention is far longer than any client's wait.

Acceptance criteria:

- A publish of a file that is not a gzip tarball: `ansible-galaxy collection publish` prints `Galaxy import process failed:` with the
  description and exits 1; the repository holds no version; `GET` of the task shows `state: failed`.
- Reading the task of a publish through the virtual repository returns the same document with state `completed`.

### R-ANS-010 · Reading a collection archive
Priority: **P1** · Covers: PKG-ANSIBLE-006

`Inspect` reads the uploaded archive as untrusted content (R-ARCH-028) and decides identity and the stored metadata. The kit's helper
for gzip-compressed tar archives (`formattest.InspectTarGz`) is the crafted-input harness (R-ANS-031).

- **Structure.** A gzip-compressed tar with `MANIFEST.json` and `FILES.json` at its root (a leading `./` is accepted). Entry types
  other than regular files and directories (links, devices, hard links) are refused; names with `..`, an absolute name or a name
  that leaves the archive are refused; the number of entries, the total expanded size, the size of one entry and the compression ratio
  are bounded by `untrusted.Limits` (the format tightens the defaults, R-ANS-031). The archive is never extracted to disk.
- **`MANIFEST.json`** (at most 1 MiB): `collection_info{namespace,name,version,authors,readme,tags,description,license,license_file,
  dependencies,repository,documentation,homepage,issues}`, `file_manifest_file{name,ftype,chksum_type,chksum_sha256,format}` and
  `format`. The identity `<namespace>.<name>` and the version are validated by R-ANS-002 and **must equal** the file name of the
  upload (`<ns>-<name>-<version>.tar.gz`) when the upload names one: a mismatch is `invalid_archive`
  (`identity_mismatch`). `dependencies` is a map of `<ns>.<name>` to a version requirement string; a key that is not a valid
  collection name, or more than 256 entries, is refused. `file_manifest_file.chksum_sha256` must equal the SHA-256 of the `FILES.json`
  in the archive.
- **`FILES.json`** (at most 16 MiB): `files[]` entries with `name`, `ftype` (`file` or `dir`), `chksum_type` and `chksum_sha256`,
  and `format`. The SHA-256 of every `file` entry listed must equal the digest of the archive member of that name, and an archive
  member that FILES.json does not list (other than the two documents) is refused: the archive is internally consistent, so the
  client's `collection verify` has something to verify against.
- **`meta/runtime.yml`** (optional, at most 64 KiB, a file above it is a bound crossed): its `requires_ansible` string becomes the version's
  `requires_ansible`; the value is stored as text and not interpreted (at most 256 characters, no control character); the file is
  decoded into a node tree and walked, never into Go values (Q-ANS-30), and a document with an anchor, an alias, a tag, a merge key or
  more than 1,000 nodes, that is not a mapping or whose `requires_ansible` is not a plain string is ignored for this field (the version
  then has `requires_ansible` empty) and the task carries a warning message.
- **What is stored** in the version row (R-ANS-011): the manifest's `collection_info` without `readme` text beyond 64 KiB, the
  dependencies, `requires_ansible`, the SHA-256, size and file count. The README text of the archive (`readme` names its path) is read
  on demand by the package screens by streaming the archive up to that member, which a gzip stream needs (R-ANS-026, H-853).
- `Inspection.Package` is `<ns>.<name>`, `Inspection.Version` the version, `Properties` below the prefix `ansible.` (R-ANS-011).

Acceptance criteria:

- An archive with a symlink member, a member `../x`, a `FILES.json` whose digest differs from `file_manifest_file`, or an unlisted
  member: publish ends in a failed task, each with its own message; the repository holds nothing.
- An archive built by `ansible-galaxy collection build` of the ansible-core 2.17.14 and 2.21.6 images is accepted.
- The identity inside the archive and the file name differing (`acme-tools-1.0.0.tar.gz` holding `acme.other`) is a failed task.

### R-ANS-011 · Storage model of the local class
Priority: **P1** · Covers: PKG-ANSIBLE-006

The archives are items of the repository (R-STOR-003); the generic package and version rows (R-ARCH-009) carry `<ns>.<name>`, the
version and the publish time. The format's data port (`internal/formatdata/ansible`) owns four tables, created by migrations that are
named by purpose and numbered at build time ([Q-ANS-24](#q-ans-24--the-format-has-four-tables-and-a-configuration-section)):

```sql
-- the metadata of each stored version of a local repository
CREATE TABLE ansible_collection_versions (
  repo_id            bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name               text   NOT NULL,            -- <ns>.<name>
  version            text   NOT NULL,            -- as written
  sort_key           bytea  NOT NULL,            -- semver precedence, memcmp order (build metadata excluded)
  item_id            bigint NOT NULL REFERENCES items (id) ON DELETE CASCADE,
  sha256             bytea  NOT NULL,
  size               bigint NOT NULL,
  requires_ansible   text   NOT NULL DEFAULT '',
  dependencies       jsonb  NOT NULL DEFAULT '{}',
  collection_info    jsonb  NOT NULL,            -- trimmed (R-ANS-010)
  published_by       text   NOT NULL,            -- principal id
  published_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, name, version)
);
CREATE TABLE ansible_tombstones (               -- R-ANS-012
  repo_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name text NOT NULL, version text NOT NULL, sha256 bytea NOT NULL,
  deleted_at timestamptz NOT NULL DEFAULT now(), deleted_by text NOT NULL,
  PRIMARY KEY (repo_id, name, version)
);
CREATE TABLE ansible_import_tasks (             -- R-ANS-009
  id uuid PRIMARY KEY, repo_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name text NOT NULL, version text NOT NULL, state text NOT NULL CHECK (state IN ('completed','failed')),
  error_code text, error_description text, messages jsonb NOT NULL DEFAULT '[]',
  created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ansible_upstream_versions (        -- R-ANS-018, a remote's announced digests
  repo_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name text NOT NULL, version text NOT NULL,
  sha256 bytea NOT NULL, size bigint NOT NULL, requires_ansible text NOT NULL DEFAULT '',
  recorded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, name, version)
);
```

The first table holds local rows only (the digests a remote's version documents announce are the fourth table, so that a restore without
remote caches can drop it, R-BAK-008). Properties (`ansible.namespace`, `ansible.name`, `ansible.requires_ansible`,
`ansible.dependency_count`) are read-only and namespaced (FMT-008). `OnCommit` writes the row in the commit transaction and raises the
package's `rev_seq`; `OnItemsRemoved` deletes it and, for a removal that is a deletion by a person, writes the tombstone (R-ANS-012).
The columns above are the contract; the builder may add indexes and revision columns the export classes need (R-BAK-004).

### R-ANS-012 · Deleting versions, tombstones and recalculation
Priority: **P1** · Covers: PKG-ANSIBLE-006

- **Delete.** A version is removed through the management API and the package screens (R-STOR-018, `delete`), not through the Galaxy
  API (`ansible-galaxy` has no delete command; the galaxy_ng `DELETE` routes are not offered, Q-ANS-2). The removal takes the
  archive item (to the trash, R-STOR-022), the version row, and leaves a **tombstone** `(name, version, sha256)`.
- **A tombstoned version number cannot be published again** (409 `version_tombstoned`): clients and caches that saw the version must not
  meet other bytes under it ([Q-ANS-9](#q-ans-9--a-published-version-is-immutable-a-deleted-one-leaves-a-tombstone)). `DELETE …/ansible/tombstones/{name}/{version}` of the management API (action `manage`) clears one
  and writes an audit event; restoring the item from the trash keeps the version and drops the tombstone.
- **Recalculate** (the reindex of R-REPO-016): on a local repository `POST …/reindex` enqueues the job `ansible-reindex`, which reads
  every stored archive with ranged reads (the two documents only), rewrites the `ansible_collection_versions` rows from them, creates
  the rows of archives that have none (an import by copy, a restore), raises `rev_seq` of the packages it changed, checkpoints per
  package name and never changes stored bytes. On a remote repository the operation is 405 `not_applicable` (its index is the
  upstream's); on a virtual repository it drops the merged-metadata cache (R-REPO-016).

Acceptance criteria:

- After the deletion of `1.0.0` the version list no longer shows it, a publish of `1.0.0` is 409 `version_tombstoned`, and after the
  tombstone is cleared a publish of other bytes succeeds.
- After corrupting `requires_ansible` of a row in the database, a reindex restores it from the archive.

### R-ANS-013 · Digests and signatures
Priority: **P1** · Covers: PKG-ANSIBLE-005

([Q-ANS-14](#q-ans-14--signatures-pass-through-trove-signs-nothing-and-verifies-nothing) and [Q-ANS-13](#q-ans-13--the-announced-sha-256-is-always-verified-the-first-recorded-digest-stays).)

- **The digest of a collection is the SHA-256 of the archive.** A local repository computes it while spooling (R-STOR-002) and states
  it in `artifact.sha256` and `Repr-Digest`; a remote repository compares the digest its version document announces with the bytes it
  fetched (R-ANS-018) and serves the version document's value; the client compares again after the download. This is the only
  integrity path the protocol has, and the existing one (R-REPO-041, `ChecksumAlwaysVerified`).
- **Signatures.** The protocol carries them as `signatures[]` in the version document, each a detached OpenPGP signature of the
  archive's `MANIFEST.json`, verified by the client against its own keyring. A **local** version has `signatures: []`: trove holds no
  signing key for the format and uploads no signature (the catalog specifies none). A **remote** version document is served with
  the upstream's `signatures` unchanged; trove does not verify them, so a client that sets `--keyring` verifies the upstream's
  signature itself against keys it trusts, and a client without a keyring skips the check, exactly as against the upstream
  ([H-839](#h-839--a-remotes-signatures-are-passed-through-and-not-verified-by-trove)). A **virtual** version document carries the
  signatures of the member whose document it serves.
- **Vulnerabilities.** The vulnerability pipeline (vulnerability-scanning.md) matches stored versions to OSV advisories by ecosystem
  and name. OSV has no Ansible ecosystem (verified 2026-11-10), so the format has no `VulnerabilityAdapter` and no `ImageScanned`
  interface, the evaluation state of its repositories is `not_covered`, and no download is blocked by it
  ([Q-ANS-15](#q-ans-15--no-vulnerability-adapter-the-state-is-not_covered), [H-841](#h-841--collections-are-not-evaluated-for-vulnerabilities)). What the pipeline does say about a collection is only that it was not
  evaluated; it does not read the Python or system packages a collection's roles install, nor the collection's own plugin code.

Acceptance criteria:

- A remote version document's `signatures` equal the stub's bytes; a local one has an empty list.
- The package screen of a collection shows the evaluation state `not_covered` and the dashboard counts the repository as not covered.

### R-ANS-014 · What is served for roles, and what is not
Priority: **P1** · Covers: PKG-ANSIBLE-007

Roles are an older packaging than collections. A role is not an archive that Galaxy stores: the public Galaxy lists a role, its
versions (git tags) and for each a `download_url` that is a GitHub archive (verified). A role also has no manifest with a name,
version and digest that a registry could validate. Hence ([Q-ANS-6](#q-ans-6--roles-are-served-by-translation-from-a-remote-only-collections-are-the-stored-artifact)):

| Role source in a client | Through trove |
|---|---|
| `ansible-galaxy role install <owner>.<role>` (Galaxy role) | **Served by a remote repository** of a Galaxy (and by a virtual repository over one), by translation of the role API and pull-through of the archive (R-ANS-020) |
| `requirements.yml` `roles:` entry with `name: <owner>.<role>` or `src: <owner>.<role>` | As above |
| `src: https://…/x.git` or `git+https://…` with or without `scm: git`; `src: <url>.tar.gz` | **Not served and not seen by trove**: the client clones or downloads on its own; trove has no Git format ([H-844](#h-844--git-and-url-roles-bypass-trove)) |
| Publish of a role (`ansible-galaxy role import`, `role init` plus upload) | **Not offered**: a local repository has no `v1` API and no role route; the guide tells the author to wrap the role into a collection (`roles/` directory of a collection) |
| A role in the content of the existing Galaxy instance | **Not imported**: reported as `notKept` (R-ANS-027) |

- A **collection** that contains roles (`roles/` in the archive) installs as a collection; its roles are used by their collection name.
- The local class answers `GET v1/…` with 404 in the `v1` shape and `{"default":"This repository does not serve roles; …"}`.

### R-ANS-015 · Credentials for role archives
Priority: **P1** · Covers: PKG-ANSIBLE-013

The client downloads a role archive without the token header and uses `~/.netrc` (verified, R-ANS-006). Therefore:

- The setup snippet for a repository that is not anonymously readable writes the `~/.netrc` line
  `machine <host> login <user> password <token>` with the key word `password` (the only secret key word netrc defines), where the
  token is the environment variable (R-ANS-025). The user name is any fixed word; trove ignores it.
- The role archive route accepts Basic with a trove token as the password (and `Token` and `Bearer`, as every route of the format),
  and answers 401 with `WWW-Authenticate: Basic realm="trove"` without a credential, so a client that waits for a challenge also
  works.
- An identity token as netrc password (TOK-005) is a trove token like any other; no second kind exists.
- One `machine` line serves every repository on the host, which is exactly the scope of the token; a token scoped to one repository
  is not usable for a role in another, and that is the intended narrowing.

Acceptance criteria:

- `ansible-galaxy role install` through a private remote repository with a `~/.netrc` line and no `token=` in `ansible.cfg` installs
  the role; without the netrc line the API calls succeed (they carry the token) and the archive download fails with the client's
  401 message.

### R-ANS-016 · Remote repository: upstream and discovery
Priority: **P1** · Covers: PKG-ANSIBLE-009

A remote repository proxies a Galaxy-compatible server (RMT-001, R-REPO-024 to R-REPO-029). `remote.url` is the **API root** of the
upstream: a URL that answers `GET` with `available_versions` ([Q-ANS-11](#q-ans-11--the-upstream-url-is-an-api-root-and-the-adapter-reads-it-as-a-discovery-document)); `https://galaxy.ansible.com/api/` is the default. The upstream test
(R-REPO-024) requests it and reads the document; a URL that answers something else is reported with the hint that the API root of a
Galaxy usually ends in `/api/` (the client's own fallback is not repeated here, so a stored URL is exact).

- The adapter implements `Discoverer` (R-NUGET-014's seam): the discovery document is the stored metadata document `api-root.json`
  of the repository, revalidated like any other (600 s), parsed outside every transaction into the path prefixes of `v3` and `v1`.
  A prefix that is absolute, contains `..` or a scheme is refused (502 `upstream_unavailable`).
- Every upstream request is built from `remote.url` + the prefix + the Galaxy path (`collections/<ns>/<name>/versions/?limit=100&offset=<n>`).
  The upstream's redirects (the public Galaxy answers 302 to `/api/v3/plugin/ansible/content/published/…`, verified) are followed by
  the engine under its redirect rules (at most five hops, never from `https` to `http`, R-REPO-039); a redirect to another host needs
  that host in `remote.extraHosts` (R-REPO-043).
- Whether the upstream has `v1` decides whether the remote lists `v1` itself (R-ANS-003); a Galaxy-compatible hub without it serves
  collections only.
- **The cache periods** are the defaults of R-ANS-001; an archive is immutable and never expires (R-REPO-025).
- **Offline.** A remote that may not contact its upstream (R-REPO-030) serves the stored documents and archives and answers 404 or
  503 as the engine decides for the rest.

Acceptance criteria:

- Creating a remote with `https://galaxy.ansible.com/` (no `/api/`) is refused or reported by the upstream test with the hint, and
  with `https://galaxy.ansible.com/api/` it is reachable.
- With the counting stub (R-ANS-032) as upstream, `ansible-galaxy collection install` through the remote makes the stub count one
  request per document and one per archive, however many clients ask.

### R-ANS-017 · Remote repository: documents, paging and rewriting
Priority: **P1** · Covers: PKG-ANSIBLE-008, PKG-ANSIBLE-009

- **What is stored**: the upstream's documents as received, bounded (`MaxDocumentBytes` 16 MiB, R-ARCH-028: the largest version document
  seen is 0.85 MB): the collection document, the version-list pages and the version documents, each at its canonical path (R-ANS-002);
  `Rewrite` renders them for one response and the stored bytes never change (R-REPO-043).
- **Pages.** The upstream's list is stored in pages of 100 (`limit=100&offset=<n>`), one stored document per page with the variant
  `offset=<n>` ([Q-ANS-12](#q-ans-12--a-remote-stores-the-version-list-in-pages-of-100-and-cuts-the-clients-page-when-it-serves)). When a client asks `limit=L&offset=O` the handler reads the stored pages that cover the range (fetching
  the missing ones through the engine, one flight each), cuts the range, computes `meta.count` from the first page and renders the
  links at this repository's address. The default client request (`limit=100`) is one stored page per 100 versions; an upstream that answers fewer entries than it was asked for is
  read at its own page size ([Q-ANS-39](#q-ans-39--a-remote-reads-the-version-list-at-the-upstreams-page-size-starting-with-the-first-page)). Reading is
  bounded per request (512 pages, 51,200 versions); a longer list is 502 `upstream_unavailable` with that reason.
- **Rewriting.** In the served documents `download_url` is the repository's `v3/artifacts/collections/<file>`, every `href` and link
  the repository's address, `metadata.contents` and the members `manifest`, `files`, `git_url`, `git_commit_sha` are dropped (Q-ANS-10); the documentation is fetched
  separately (R-ANS-035); all
  other fields pass unchanged, `signatures` included. The URL a rewritten `download_url` maps back to is recorded as the URL
  map row of the archive path when the version document is parsed (R-REPO-043): the upstream's own `download_url`, host-checked
  against the upstream origin and `extraHosts`; a version document of collection A cannot map the path of collection B.
- **Parse on fill.** `PrepareFill` validates a version document (the identity in the URL equals the document's `namespace.name`,
  `collection.name` and `version`, the `artifact.sha256` is 64 hex characters, `download_url` is an `https` or `http` URL) before
  anything is stored; a document that fails is 502 and keeps the previous entry. `OnFill` records `ansible_upstream_versions` (R-ANS-018).
- **Removal.** A version the upstream drops from its list is dropped from the served list at the next revalidation; its cached archive
  stays and remains downloadable by path. The remote does not track removal marks (`TracksVersionRemoval` false, H-848).
- **No browsing of uncached content** (PKG-ANSIBLE-008). The artifact browser lists what the cache holds, never the upstream's
  catalogue; the Galaxy index (`v3/collections/` without a name) is not served by a remote, so a client cannot list the upstream
  through trove. The mirror (R-ANS-021) is the only thing that reads the index.
- **Errors.** An upstream 404 for a collection or version is a negative entry (300 s) and 404 to the client; an upstream 401 or 403 is
  `upstream_unavailable` with the status in `detail` (a hub credential problem, R-ANS-019); a 429 follows the engine's rules.

Acceptance criteria:

- A collection with 250 versions in the stub: `ansible-galaxy collection install ns.name` makes the stub serve three list pages once,
  the client sees all 250 versions through the `next` links, and a second install makes no stub request.
- The served version document has no `metadata.contents` and `download_url` on the remote's address; the stored one is byte-identical to
  the stub's.

### R-ANS-018 · Remote repository: collection archives, digests and immutability
Priority: **P1** · Covers: PKG-ANSIBLE-009

([Q-ANS-13](#q-ans-13--the-announced-sha-256-is-always-verified-the-first-recorded-digest-stays).)

- **Fetch.** A request for an archive path is mapped to the recorded upstream URL (R-ANS-017) and fetched through the engine: one
  flight across replicas (R-REPO-025), the withheld tail until the digest is verified (R-REPO-041), the item stored under the
  canonical path. A request for an archive whose version document is not stored first revalidates the version document through the
  engine (`BeforeArtifact` through `h.Remote()`, as npm's R-NPM-025), because the announced digest decides the artifact; a version the
  upstream does not have is 404.
- **Digest.** `BeforeArtifact` returns the SHA-256 of `ansible_upstream_versions` for the path (`FactsGate` reads it with the item
  lookup); the engine verifies the bytes against it whatever `integrity.checksumPolicy` says; the policies `ignore` and `pass-through`
  are refused when the repository is created or updated. A mismatch is 502 `upstream_integrity_mismatch`, nothing is stored, the
  security event of R-REPO-041 is written.
- **The first digest recorded for a version stays while its archive is cached.** If a refreshed version document announces a different
  SHA-256 for a version already recorded whose archive is cached, the recorded row and the cached archive are kept,
  `proxy.upstream_digest_changed` is written, and the served version document keeps announcing the recorded digest and size, so the client's own
  digest comparison passes; an operator resolves by deleting the cached item, after which the next fill records the digest the upstream announces
  now ([Q-ANS-38](#q-ans-38--the-first-digest-of-a-version-stays-only-while-its-archive-is-cached)).
- **Inspect on fill.** `FillInspector` reads the fetched archive's `MANIFEST.json` (R-ANS-010, bounded) and refuses an archive whose
  declared identity or version differs from the path (502 `upstream_unavailable`, `identity_mismatch`); nothing of it reaches a client
  before that, and nothing is stored. The `requires_ansible` of the manifest is not compared with the document's ([Q-ANS-37](#q-ans-37--a-fetched-archive-is-held-to-the-identity-of-its-path-requires_ansible-is-not-compared)).
- **Cache.** The archive is an item of the remote repository, counted in its usage; the cache of a remote never writes a row of
  `ansible_collection_versions` (the commit hook does nothing for a fetched file beyond the generic package row).

Acceptance criteria:

- The stub serves an archive that differs from its document's `sha256`: `ansible-galaxy collection install` fails, no item exists,
  `trove_remote_integrity_failures_total` increments.
- The stub changes the `sha256` of a cached version: the cached archive keeps being served and a `proxy.upstream_digest_changed` event exists.

### R-ANS-019 · Remote repository: a hub behind single sign-on
Priority: **P1** · Covers: PKG-ANSIBLE-010

A remote repository can proxy the Red Hat Ansible Automation Hub, whose collections need a short-lived access token that the client
obtains from a single-sign-on realm with a long-lived **offline token** (a refresh token). The mechanism is specified here and built last
([Q-ANS-17](#q-ans-17--the-hub-token-exchange-is-a-separate-optional-interface-built-after-the-core-class), [H-842](#h-842--the-hub-token-exchange-is-specified-from-documentation-and-never-run-against-the-service)):

| Setting | Value for the Hub | Default |
|---|---|---|
| `remote.url` | `https://console.redhat.com/api/automation-hub/` (an API root, R-ANS-016; **not verified** that this answers `available_versions`) | none |
| `remote.auth` | the offline token as the stored secret of the repository's credential (R-REPO-033), mode `bearer` | none |
| `ansible.authUrl` | the token endpoint, `https://sso.redhat.com/auth/realms/redhat-external/protocol/openid-connect/token` (# example) | none (empty: the credential is sent as it is) |
| `ansible.clientId` | the client identifier of the grant, `cloud-services` (# example, not verified) | `cloud-services` |

- With `ansible.authUrl` set, the adapter exchanges the stored credential at that URL with `grant_type=refresh_token`,
  `client_id=<clientId>` and `refresh_token=<offline token>` (form-encoded, `application/x-www-form-urlencoded`), reads `access_token` and
  `expires_in` from the JSON answer, keeps the access token in the replica's cache until 30 seconds before it expires, and sends
  `Authorization: Bearer <access token>` to the upstream; on a 401 of the upstream it exchanges once more and retries the request once.
  The token request goes only to the host of `authUrl` (egress rules, R-REPO-038, the host rule of R-REPO-033: the credential goes to
  the realm and the token to the upstream origin and nowhere else); neither token is logged, audited or returned by any API.
- The existing contract hands an authenticating adapter the challenge, not the credential (`Authenticator.TokenRequest`), and the
  form needs the offline token in its body. This requirement therefore needs an **additive optional interface** of the remote adapter
  that receives the credential in effect for one exchange only: `formats.CredentialExchanger`, recorded in architecture.md R-ARCH-008 and
  in ADR-191 ([Q-ANS-51](#q-ans-51--the-hub-exchange-is-the-optional-interface-credentialexchanger-and-the-engine-sends-the-exchanged-token-and-never-the-credential)). The engine sends the realm request, caches the token and sends the upstream request with the token in place of the
  credential, so the offline token never goes to the upstream, and nothing is sent to the upstream when the exchange fails.
- A failed exchange (a 400 `invalid_grant` of the realm, a 401 or 403) is 502 `upstream_auth_failed`, the answer every credential the
  upstream refuses has (R-REPO-033), never an empty list; an unreachable realm is the engine's `upstream_unreachable`. The security event
  of the failed request names the realm's host and no token.
- `ansible.authUrl` must be an `https` URL (`http` only for a loopback address, the test stub's), without user information, query or fragment;
  the repository needs `remote.authMode: bearer` (the stored secret is the refresh token) and the endpoint's host is the upstream's or an
  `extraHosts` entry with `sendCredentials`, because the refresh token is in the body of the request, where the hop policy cannot withhold it.
  The service checks all four when the document is saved (`validate_ansible.go`) and the engine again at every exchange.
- The `x-trove-classes` of both settings is `remote`.

Acceptance criteria (with the stub's realm; the service itself is not reachable from the tests):

- The stub's upstream accepts only `Bearer` access tokens its realm issued; a remote with a refresh token installs a collection, and
  after the stub expires the access token, the next request exchanges again and succeeds.
- A wrong refresh token: the install fails with the 502 `upstream_auth_failed` and the stub's upstream saw no request at all (nothing is
  sent without a token, and the credential is not sent in its place).
- A realm host that may not receive the credential: the request fails and the realm saw none.

### R-ANS-020 · Remote repository: roles by translation
Priority: **P1** · Covers: PKG-ANSIBLE-007

([Q-ANS-6](#q-ans-6--roles-are-served-by-translation-from-a-remote-only-collections-are-the-stored-artifact) and [Q-ANS-7](#q-ans-7--role-archives-are-pulled-from-the-hosts-of-the-roles-versions-and-cached-without-a-digest).) A remote whose upstream lists `v1`
serves the three role operations of the client, in the `v1` shape:

| Operation | Path | Stored as |
|---|---|---|
| Find a role | `v1/roles/?owner__username=<o>&name=<n>` | `roles/lookup.json`, variant `owner=<o>&name=<n>` (only these two parameters pass; others are dropped); not cached beyond the metadata period |
| Versions of a role | `v1/roles/<id>/versions/?page_size=50` and its `next_link` pages | `roles/<id>/versions.json`, variant `page=<n>` |
| Role archive | `v1/roles/archives/<id>/<ref>.tar.gz` | item `roles/<id>/<ref>.tar.gz` |

- `<id>` is the upstream's numeric id (digits, at most 12), `<ref>` the version name (`[A-Za-z0-9._+-]{1,128}`); the owner and role
  parameters follow the grammar `[A-Za-z0-9._-]{1,64}`; anything else is 400 in the `v1` shape.
- **Translation.** `download_url` of each served version entry is rewritten to the role archive route of this repository and
  `next_link`/`previous_link` to this repository's address. A version entry whose `download_url` is not an `https` URL on a host of
  `remote.extraHosts` (the defaults are the GitHub hosts) is **dropped from the served list** and counted in
  `trove_ansible_role_versions_dropped_total`, so a client can never be sent to download from elsewhere than the operator allowed. A role
  document's other fields pass unchanged (`github_user`, `github_repo`, `github_branch`, `summary_fields`, …).
- **Archive.** The first request for a role archive path fetches the upstream URL that the versions document recorded for exactly that
  path (the URL map of R-REPO-043, same rule: the path is built from the document's own role id and version name, a document cannot
  map another role's path), through the engine, with the redirect from `github.com` to `codeload.github.com` followed under the host
  rule. The archive is stored under the path and served from it ever after; it is bounded by `ansible.role_archive_max_bytes` (default
  256 MiB). The upstream announces **no digest** for it, so nothing is verified except that it is a gzip-compressed tar archive within
  the limits of R-ANS-031 and that the identity of nothing is claimed ([H-840](#h-840--a-role-archive-has-no-announced-digest-and-the-source-is-not-byte-stable)); under `integrity.checksumPolicy: require` the archive is
  refused with 502 `upstream_digest_missing`, as the policy says.
- **Names** ([Q-ANS-36](#q-ans-36--a-role-is-a-package-of-the-cores-name-machinery-named-role-and-its-id)). A role document has no `<ns>.<name>` package. The version pages and the archives of a role are classified as
  metadata and artifacts of the package `role/<id>`, which no collection can have, and the format's name rule takes that name: the URL map of
  the archive, the include and exclude patterns of the repository, the grants and the shadowing check treat a role like any package. The
  lookup document names no role and has no package.
- **Not mirrored.** The mirror (R-ANS-021) never reads the role index.
- A remote whose upstream has no `v1` answers 404 in the `v1` shape for these routes.

Acceptance criteria:

- `ansible-galaxy role install geerlingguy.pip` through a remote with the stub as upstream installs the role from the archive the stub's
  GitHub stand-in serves, the stub counts one request per document and archive for any number of clients, and the stored archive is
  returned for a second install without an upstream request.
- A versions document with a `download_url` on an unlisted host: that version is not in the served list.

### R-ANS-021 · Remote repository: mirror mode
Priority: **P1** · Covers: PKG-ANSIBLE-009

The owner required a full mirror for Docker and APT as an option; the same is offered here ([Q-ANS-16](#q-ans-16--a-mirror-of-the-collection-index-is-offered-it-keeps-what-the-patterns-select)). A remote in mode `mirror`
(R-REPO-050 to R-REPO-059) keeps the collections its patterns select, so that they stay installable without the upstream.

- **The adapter implements `MirrorLister`.** Methods: `listing` (what `auto` takes): the collection index of the upstream
  (`v3/collections/?limit=100&offset=<n>`, verified to list `namespace`, `name`, `versions_url`, `highest_version` and to page by
  `offset`; the public Galaxy offers no change feed and the filter `namespace`), a full pass every time, with the cursor
  `{"offset": n}`; `names`: the include patterns without a wildcard read as `<ns>.<name>`; `feed` is not offered. As for every method,
  the names the repository holds are added to a full pass.
- **A pattern is the unit of choice.** The patterns select by `<ns>.<name>` (`community.*`, `acme.tools`); a mirror with no pattern
  that bounds a name set (R-REPO-051's bounding rule; a literal prefix) is refused as the engine refuses it for npm; `*` is therefore
  possible only by an operator's explicit choice of the unbounded form that the engine allows, and a full mirror of the public Galaxy
  is then a configuration, not a default ([H-846](#h-846--the-size-of-a-full-mirror-of-the-public-galaxy-is-not-measured)).
- **The plan of a name.** Per name: the stored version-list pages and version documents (through `discover.Upstream.Document`), then
  per version the archive with its announced size; `maxVersionsPerPackage` and `includePrereleases` apply (semver pre-release
  rule); each archive passes the digest check of R-ANS-018. Newest versions first.
- **Politeness.** The public Galaxy's rate limits were not probed; the engine's throttling and window halving on a 429 or a
  `Retry-After` (R-REPO-054) apply, and the default concurrency of the mirror is the engine's.
- **Roles and the hub's index** are not read.
- **Documentation.** With `ansible.fetchDocumentation` on, the pass also queues the documentation fetch (R-ANS-040, R-ANS-042) for each version it keeps, after the archive; the
  fetch is the job's, with the engine's throttling. Its size is not measured ([H-890](#h-890--the-size-of-the-documentation-of-a-full-mirror-and-of-many-local-versions-is-not-measured)).

Acceptance criteria:

- A mirror of the stub with the pattern `acme.*` stores every version of every `acme` collection, none of `other`, after one pass; the
  install through the remote then makes no stub request.
- The stub rate-limits the index: the pass slows and completes (the engine's behaviour, shown for this adapter).

### R-ANS-022 · Virtual repository: members and merging
Priority: **P1** · Covers: PKG-ANSIBLE-011

A virtual repository serves local and remote Ansible repositories behind one server URL; it is the recommended client target
([Q-ANS-18](#q-ans-18--the-virtual-merge-unions-the-versions-and-lets-the-first-tier-win-per-version)). Members, resolution order, patterns, pin rules and namespace claims are the core's (R-REPO-018 to R-REPO-023).

- **Strategies.** Collection documents and version lists are **merged**; version documents and archives use the **first match** of the
  tiers (local, cached remote, remote). An artifact is served from the member that the version document came from, because both
  walk the same plan.
- **Version list.** The union of the members' versions; for a version in several members the entry of the lowest (tier, position)
  wins, and a local member's version never loses to a remote's. If the SHA-256 of the same version differs between members, the
  entry stays the winner's and a metric `trove_ansible_virtual_digest_conflicts_total` counts it once per merge that is built
  (the client reads the winner's document and archive, so it is consistent). The digest of a remote version is the one its version
  document announces, so the merge reads the document of every version that a local and a remote member both hold, and of no other
  ([Q-ANS-45](#q-ans-45--the-digest-conflict-is-looked-for-in-the-versions-a-local-and-a-remote-member-share)). Order: semver descending; `meta.count` is the merged count; pagination is cut after
  the merge, so a client's `limit` and `offset` are consistent across members.
- **Collection document.** `highest_version` is the highest of the merged list; `created_at` is the earliest, `updated_at` the latest of
  the members'.
- **Version document.** The first member that holds the version; the document is rendered neutrally (R-ANS-004: absolute addresses
  of the virtual repository, `signatures` of the serving member). A remote member's cached versions are listed first for a caller who
  may not fill its cache (R-AUTHZ-008).
- **Dependency confusion.** Claims and pin rules (R-REPO-023) keep a name away from the members that must not serve it, before any
  merge; the shadowing check of a first publish (R-REPO-048) asks the remote members for `collections/<ns>/<name>/collection.json`
  (`PackageDocumentPath`). The dependencies of a collection (`metadata.dependencies`) are resolved by the client through the same server,
  so each dependency passes the same rules.
- **Fan-out cost.** A merged document is built through the core's merged-metadata cache (R-REPO-020), keyed by the members' revisions;
  a remote member's pages are read through the member (`Request.Member`), as NuGet does (Q-NUGET-82), and one budget bounds the reading
  of a merge.
- **Roles.** `v1/roles/` lookups and `v1/roles/<id>/versions/` are answered by the first remote member whose answer is not empty
  ([Q-ANS-19](#q-ans-19--role-ids-in-a-virtual-repository-name-their-member)); the role `id` of the served document is rewritten to `<member key>-<upstream id>` (the part after the last `-`
  is digits), and the versions and archive routes decode it to the member and its id; an id that names a member not in the
  repository, or a member without `v1`, is 404. Local members take no part.
- A virtual repository lists `v1` in its discovery document when any remote member's upstream has it.

Acceptance criteria:

- A virtual repository over `ansible-local` (acme.tools 1.0.0) and a remote (acme.tools 0.9.0, other.pkg 2.0.0) lists acme.tools
  `1.0.0`, `0.9.0`; `ansible-galaxy collection install other.pkg` installs from the remote; the local `acme.tools:1.0.0` archive is the
  local one.
- Both members hold `acme.tools 1.0.0` with different bytes: the client installs the local ones and the conflict counter is 1.
- With a claim of `acme.*` for the local member, the remote's `acme.other` is not offered.

### R-ANS-023 · Virtual repository: reads, the gate and member paths
Priority: **P1** · Covers: PKG-ANSIBLE-011

- A caller needs `read` on the virtual repository and, per member, on the member (R-AUTHZ-009): a member the caller may not read is
  skipped, not an error; a version list never reveals which member a version came from, and no document names a member.
- The **member paths** (`/api/ansible/<project>/<member>/…`) of the members stay reachable by their own grants; a cached-view request
  (a read that may not fill, R-AUTHZ-008) is served from stored documents and items only.
- A remote member that cannot be asked (offline, circuit open) makes the merge **fail**: partial results are never cached and are
  answered with 503 `upstream_unavailable` and `Cache-Status: detail=partial` when other members answered (R-REPO-020, [Q-ANS-44](#q-ans-44--a-merge-that-could-not-ask-a-member-fails-instead-of-serving-a-partial-list));
  H-348's limitation (a merge cannot mark one member partial) is that of NuGet's merge and carries over.
- The archive route walks the plan of R-REPO-019 step 6 and serves the first hit with the headers of R-ANS-005.
- **Versions a grant hides.** When a grant or a token scope of the caller decides versions by path on a member (`VersionsByPath`,
  R-AUTHZ-005, Q-AUTHZ-255), a version whose archive path it hides is in no document of the virtual repository: not in the version list
  (and not in `meta.count`), not as the `highest_version` or in the times of the collection document, and its version document is 404. The
  version is decided on the member that serves it, the winner of the merge; the merged document stays shared by every caller and the
  view is the caller's. A collection of which the caller may read no version is 404. A caller whose grants decide no version by path costs
  no per-version question.

Acceptance criteria:

- A caller who can read only the local member sees the local versions through the virtual repository and no error for the remote one.
- With the stub down and the remote's version list not cached, the merged list of a collection the local member holds too is 503 `partial`
  (a collection only the remote could hold is the engine's 502 `upstream_unavailable`), and after the stub is back it is complete.

### R-ANS-024 · Publishing through a virtual repository
Priority: **P1** · Covers: PKG-ANSIBLE-003

`POST v3/artifacts/collections/` to a virtual repository forwards to its **deploy target** (R-REPO-021): the target must be a local
repository of the format, else 405 `no_deploy_target` in the Galaxy shape (a target that is not a local repository of the format). The target's checks (R-ANS-008) apply to the caller's
principal, with the virtual repository's patterns and claims in front of the shadowing check (R-REPO-048: a first publish of a name
that a remote member of the virtual repository serves is refused 403 `shadows_remote_package` unless the caller holds `manage`).

- The answer's `task` is the address of `v3/imports/collections/<id>/` **on the virtual repository** (the address the client was given),
  which looks the task up in the deploy target (R-ANS-009).
- The handler of the virtual class resolves the target, makes a request for it and runs the handler of the local class on that
  request; the 202 answer's task address is then rewritten to the virtual repository
  ([Q-ANS-46](#q-ans-46--a-publish-through-a-virtual-repository-runs-the-local-handler-on-the-target-and-rewrites-the-task-address)).
- The upload through `/files/` of a virtual repository is routed to the deploy target the same way (an upload needs no code of the format,
  as in R-DEB-028).

Acceptance criteria:

- `ansible-galaxy collection publish -s <virtual URL>` stores the version in the local target, the command's polling succeeds through
  the virtual address, and a subsequent `collection install` through the virtual repository installs it.
- A virtual repository without a deploy target answers 403 `permission_denied`: the decision point refuses the deploy action before the format is asked, as for npm ([Q-NPM-36](npm.md)); 405 `no_deploy_target` is left for a target that is not a local repository of the format.

### R-ANS-025 · Client setup instructions and the guide
Priority: **P1** · Covers: PKG-ANSIBLE-012

The setup templates (R-API-030) and the guide (R-UI-049) of the format ([Q-ANS-22](#q-ans-22--the-setup-writes-the-token-as-an-environment-variable-not-into-ansiblecfg)). The format declares one client, `ansible-galaxy`
(`Descriptor.Clients`, versions `2.17` and `2.21`), listed in the UI's table of clients per format (`FORMAT_CLIENTS`, `web/src/app/features/setup/clients.ts`).

| Template id ([Q-ANS-48](#q-ans-48--the-setup-snippets-ids-by-the-cores-convention-a-hostname-template-function-and-powershell-and-cmd-forms-of-the-token-variable)) | Section | File or language | Content |
|---|---|---|---|
| `ansible-galaxy.ansible-cfg.configure` | configure | `ansible.cfg`, ini | `[galaxy]` `server_list = trove` and `[galaxy_server.trove]` `url=<repo URL>`; the token line is **not** written (below) |
| `ansible-galaxy.token-env.configure`, `ansible-galaxy.token-env-powershell.configure`, `ansible-galaxy.token-env-cmd.configure` | configure | shell, PowerShell, cmd | `export ANSIBLE_GALAXY_SERVER_TROVE_TOKEN="${TROVE_TOKEN}"` and the forms of the other two shells; for a repository that is not anonymously readable, else a comment that none is needed |
| `ansible-galaxy.netrc.configure` | configure | shell | `touch ~/.netrc && chmod 600 ~/.netrc` and a `printf` that appends `machine <host name> login trove password ${TROVE_TOKEN}` (the host name alone, by the template function `hostname`); a command and not a file, because netrc expands no variable (needed for roles, R-ANS-015; Q-ANS-54); for a repository that is not anonymously readable |
| `ansible-galaxy.collection.install` | install | shell | `ansible-galaxy collection install <package>` with the version spec `:<version>`; the package placeholder is the collection |
| `ansible-galaxy.requirements.install` | install | yaml, `requirements.yml` | `collections:` with the package, `source: trove` naming the server and the version as a key of the entry; the `roles:` example is a comment (remote and virtual repositories only) |
| `ansible-galaxy.collection.publish` | publish | shell | `ansible-galaxy collection publish <archive> --server <repo URL> --token=${TROVE_TOKEN}`, shown only to a caller who holds `deploy` on the publish target |

- **Why the token is an environment variable, not a line in `ansible.cfg`.** The setup never receives a credential (R-API-030) and
  `ansible.cfg` has no variable expansion for `token` (verified), so a literal reference there would be sent as the token. The client
  reads `ANSIBLE_GALAXY_SERVER_<NAME>_TOKEN`; the server name in the template is `trove`, so the variable name is fixed. A user who
  prefers the file writes `token=` themselves; the guide says that and warns it ends up on disk.
- A snippet for a **virtual** repository with a deploy target names the virtual repository in `url` and in the publish command.
- A snippet for a **local** repository adds the note that roles are not served (R-ANS-003) and a `server_list` with the virtual repository
  first when both exist.
- The guide covers every class and client with the required topics (kit check 8): what a local, a remote and a virtual repository is;
  configure; authenticate (token, netrc); install; `requirements.yml` for collections and roles, with the Git and URL role limits
  (R-ANS-014); publish; maintain (tombstones, import tasks); troubleshooting (401 at the first request, `Mismatch artifact hash`, a
  role that is not found on a local server, `v1` not available).

Acceptance criteria:

- The kit's setup check renders every template for a local, a remote and a virtual repository, with `RequiresAuth` true and false,
  and none contains a token.
- Executing the configure snippets in the e2e client image and then `ansible-galaxy collection install` succeeds.

### R-ANS-026 · Package screens and the deploy dialog
Priority: **P1** · Covers: PKG-ANSIBLE-004

- A `ViewAdapter` gives the package screens (R-API-029): the versions of a collection ordered by semver, the metadata block
  (`requires_ansible`, dependencies, authors, license, tags, repository and issue links), the README of a version read from the archive
  by streaming up to it (the entry named by `collection_info.readme`, text limit 64 KiB, never executed or rendered as HTML by trove), the
  size of a version, and (local) the publisher and time. The metadata members `describe` (`view.go`) emits are `sha256`, `requiresAnsible`
  (string), `dependencies` (object, collection to version range), `authors` (list) and the links `homepage`, `repositoryUrl`,
  `documentationUrl` and `bugsUrl` (the names the other formats use); the licenses and the tags travel in the generic `licenses` and
  `keywords` fields of the version. A member that is empty is left out and not shown.
- The **Documentation and Contents tabs** of the package page, the operations behind them and the setting `ansible.fetchDocumentation` (remote only, in the
  format's settings schema) are R-ANS-033 to R-ANS-037 and R-ANS-039 to R-ANS-043, and [Q-UI-240](../api-and-ui.md#q-ui-240--an-ansible-package-has-a-documentation-tab-and-a-contents-tab-the-readme-tab-is-its-first-page) to Q-UI-244; for the format the README tab is replaced by the Documentation tab.
- The **deploy dialog** (UI-013) is not offered for the format: `/files/` deploys nothing into a repository with a protocol of its own
  (R-STOR-007), so the screens show `ansible-galaxy collection publish` and the raw-body publish of R-ANS-008 instead (Q-ANS-34).
- The UI shows the import tasks of a local repository on its edit form (a lazy tab, newest first, state and messages) through the
  management operation `listAnsibleImportTasks` (a virtual repository shows the tasks of its deploy target); and, on a local repository,
  a second lazy tab with the tombstones and the action to clear one (`manage`, after a confirmation). A remote repository has neither
  tab. The settings of the format (`authUrl`, `clientId`, remote only) come from the format's settings schema, like every format's.

### R-ANS-027 · Import of the content of an existing Galaxy
Priority: **P1** · Covers: trove-only

The copy of an existing Galaxy's collections into a local repository is one more `Format` of the importer ([import.md](../import.md)
R-IMP-009; the [`Format` interface](../../../internal/importer/types.go): `ID`, `ValidName`, `Preflight`, `Search`, `Plan`, `Held`, `Publish`, `Finish`)
([Q-ANS-21](#q-ans-21--the-galaxy-import-is-one-more-format-of-the-importer-collections-only)). `trove admin import ansible --from <source API root> --to <repo URL> [flags]`; the flags, the state file, the report and the exit
codes are those of R-IMP-001 to R-IMP-008, and nothing in the runner, the report or the state file changes.

| Seam | For Ansible |
|---|---|
| `ID` | `ansible` |
| `ValidName` | `<ns>.<name>` by R-ANS-002 (the source's names are folded to lower case; a name the target refuses is `invalid_name`) |
| `Preflight` | `GET <to>`: the discovery document is a trove one (`description` starts with `trove`) and `v3` is listed; a `GET v3/collections/` of the target is not needed |
| `Search` | the index of the source: `GET v3/collections/?limit=100&offset=…` (or the source's own listing, below), all pages, filtered by the text on `<ns>.<name>`; without `--search` and `--package` the command is refused as R-IMP-003 says, with the hint that `--search ''` lists everything |
| `Plan` | the version list of the source (`v3/collections/<ns>/<name>/versions/` pages) and, per version, the version document: `download_url` (host-checked against `--from` and `--source-extra-host`, R-IMP-002), `artifact.sha256`, `created_at`; the versions in the order of the source's publication times, semver where times tie |
| `Held` | the target's version list (`v3/collections/<ns>/<name>/versions/`) and, per version, the version document's `artifact.sha256` |
| `Publish` | the archive, verified against the source's `artifact.sha256` before a byte is sent (R-IMP-004), is sent by `POST v3/artifacts/collections/` with `Authorization: Bearer <token>` and both parts; the target's `202` task is polled until `finished_at`; a failed task is a `failed target_refused` with the task's `error.code` |
| `Finish` | nothing to set (no dist-tags, no listing state); returns the notes |

- **Digests.** A source version without `artifact.sha256` is `no_digest`; there is no switch around it.
- **Kept**: the archives (hence their manifest and README), the order of publication. **Not kept** (reported under `notKept`): publication
  and update times (trove sets its own, Q-IMP-5), download counts, deprecation flags, the source's namespace metadata and owners,
  signatures of the source (trove stores none for a local version), **and every role** of the source (roles cannot be published to a
  local repository, R-ANS-014; the report names the role count when the source's v1 API answers).
- **The source's API.** The importer speaks the Galaxy v3 documents of R-ANS-004 (the public Galaxy's own shape, also the galaxy_ng
  shape) at the API root it is given; for a source whose `v3` collection routes are served below a prefix (`content/<repo>/`), the
  operator passes that prefix as part of `--from`. The importer does not guess. The owner's instance, galaxy_ng 4.12
  at `https://galaxy.wds18.de/api/galaxy/`, was read with `--dry-run` on 2026-11-11: the API root as `--from`, `--source-auth token`
  (galaxy_ng takes Basic and its own `Token` scheme and answers Bearer as "credentials were not provided"), the 302 of `v3/collections/`
  to the distribution `published` followed with the credential on the same origin; 3 collections, 7 versions would import, no conflict,
  nothing left out.
- The format is [`internal/importer/ansible.go`](../../../internal/importer/ansible.go); the importer's tool page is
  [developer/import.md](../../developer/import.md) and the operator's page [manual/admin/import.md](../../manual/admin/import.md).
- The importer's tests use the counting stub of R-ANS-032 as the source and a local repository of the integration stack as the target.
  The unit tests of the format run against a small Galaxy and trove target of their own in the test file.
- **The roles of the source** are counted once per run, through `GET v1/roles/?page_size=1` (the `count` of the answer), and named under
  `notKept` of the first collection that is planned ([Q-ANS-43](#q-ans-43--the-roles-of-the-source-are-reported-once-per-run-with-the-first-collection)); a source without `v1` reports nothing. A collection the source marks `deprecated` is named under `notKept` too.

Acceptance criteria:

- `trove admin import ansible --package acme.tools` against the stub copies every version, `ansible-galaxy collection install` from the
  target installs bytes with the source's SHA-256, a second run reports every version `skipped`.
- A source archive whose bytes differ from its announced `sha256` is `failed digest_mismatch` and the target holds nothing of it.

### R-ANS-028 · Ansible on n replicas, without Valkey and during a drain
Priority: **P1** · Covers: trove-only

- Every route works on any replica: the documents are computed from PostgreSQL rows or stored in the cache entries, the merged documents
  are cached per replica and invalidated through the change areas (R-ARCH-018), nothing is kept in a replica's memory that another cannot
  rebuild. With Valkey stopped a publish, a read, a remote fill and a virtual merge still work (coordination falls back to the database,
  R-HA-009).
- The import task of a publish is read on any replica (a row, not a replica's memory).
- A publish interrupted by a drain is a client-visible failure of one request (the archive is spooled on the replica); the client
  retries, and the idempotent retry of R-ANS-008 makes that safe. A remote fill is one flight across replicas.
- A mirror pass is a job on the `worker` role and resumes elsewhere from its cursor (R-REPO-055).

Acceptance criteria:

- Publishes through three replicas in turn and reads through each of them list the same versions; the e2e set runs in the `scatter`
  profile.
- With Valkey stopped, publish, install through the remote and install through the virtual repository still succeed.

### R-ANS-029 · Ansible data in backups and restores
Priority: **P1** · Covers: trove-only

- `ansible_collection_versions`, `ansible_tombstones` and `ansible_import_tasks` are exported with the repository (R-BAK-004); a restore
  checks that every version row has its item and the other way round (R-BAK-022's check), and an archive without a row gets one from the
  reindex. `ansible_upstream_versions` goes with the cache of a remote and is dropped by a restore without remote caches (R-BAK-008): the
  next version document refills it, and an archive without a row revalidates its document first (R-ANS-018).
- `ansible_version_docs`, `ansible_doc_entries` and `ansible_doc_files` (R-ANS-033) are derived and **not exported**: a restore enqueues the reindex, which rebuilds
  the generated rows from the archives and the remote's rows from the stored documents; a restore without remote caches drops the stored `docs/` documents of a remote and the
  rows with them, and the next version document queues the fetch again.
- **Tombstones are journaled.** The deletion of a version writes the revocation journal entry `ansible.version.deleted`
  (`{repoId, name, version}`) in the transaction of the deletion; a restore replays it and **withholds** the version number again, so a
  restore from an older backup cannot make a deleted number publishable (R-BAK-024). The clearing of a tombstone is journaled the same
  way (`ansible.tombstone.cleared`).

Acceptance criteria:

- A restore from a backup that predates a deletion keeps the tombstone (the version number is 409 on publish) after the replay.

### R-ANS-030 · Audit events and metrics
Priority: **P1** · Covers: trove-only

Audit events (operations-and-observability.md R-OPS-*): `package.published` and `artifact.deployed` as for every format (actor: the
token's principal; the method label `galaxy_token` where it applies); `ansible.import_task.failed` (the refused archive and its code, a
security-relevant event for repeated failures of one principal); `ansible.version.deleted` and `ansible.tombstone.cleared` (R-ANS-012);
`ansible.hub.exchange_failed` (no token in the event); the generic proxy events for a remote (`proxy.upstream_digest_changed`, the
integrity failure).

Metrics (`trove_ansible_*`): `trove_ansible_publish_total{outcome}` (`completed`, `failed`, `exists`, `tombstoned`),
`trove_ansible_role_versions_dropped_total{repo}`, `trove_ansible_virtual_digest_conflicts_total{repo}`,
`trove_ansible_document_bytes{kind}` (histogram of stored document sizes), `trove_ansible_hub_exchanges_total{outcome}`; the series of a
deleted repository are forgotten as for other formats (the metrics-forget test of NuGet is the pattern).

### R-ANS-031 · Limits for untrusted content
Priority: **P1** · Covers: trove-only

The archive a client uploads, an upstream's archive that a fill inspects and the documents an upstream sends are untrusted
(R-ARCH-028); the format tightens `untrusted.DefaultLimits` and states its own:

| Bound | Default | Where |
|---|---|---|
| Archive size (`ansible.max_archive_bytes`) | 1 GiB | publish |
| Archive entries / total expanded bytes (streamed through a digest) | 10,000 / 2 GiB, the core's defaults: a format may lower a bound and not raise it (Q-ANS-30, [H-851](#h-851--a-collection-archive-beyond-the-cores-reader-bounds-is-refused)) | `Inspect`, `InspectFill` |
| `MANIFEST.json` / `FILES.json` / `meta/runtime.yml` | 1 MiB / 16 MiB / 64 KiB | `Inspect` |
| Dependencies in a manifest | 256 | `Inspect` |
| README text read for a screen | 64 KiB | the view |
| Stored upstream document (`MaxDocumentBytes`) | 16 MiB | remote fill |
| Pages / versions read for one list | 512 / 51,200 | remote list |
| Role archive (`ansible.role_archive_max_bytes`) | 256 MiB | role fetch |
| Discovery document | 64 KiB | `ParseDiscovery` |
| Documentation: a `DOCUMENTATION`/`EXAMPLES`/`RETURN` literal / a Python source read for it | 512 KiB / 2 MiB | `Inspect` (R-ANS-034) |
| Documentation: YAML nodes / nesting / anchors, aliases, tags, merge keys | 20,000 / 24 / refused | `Inspect` (R-ANS-034) |
| Documentation: one stored entry / entries per version / stored bytes per version | 1 MiB / 5,000 / 16 MiB | publish, remote fill (R-ANS-033, R-ANS-035) |
| Documentation: a README or Markdown file / files below `docs/` | 512 KiB / 64 | publish (R-ANS-034) |
| Documentation: the stored `docs-blob/` document of a remote | 16 MiB (`MaxDocumentBytes`) | remote fill (R-ANS-035) |
| Documentation: search terms / characters per term / entries answered | 8 / 64 / 5,000 | the operations (R-ANS-036) |
| Documentation: Python source scanned per archive / input of one markup conversion | 256 MiB / 64 KiB | `Inspect` (R-ANS-034), the operations (R-ANS-039) |

A bound crossed is 422 `invalid_archive` (a failed task for a publish; the exception is the documentation, whose bounds drop the part and warn, R-ANS-034) or 413 `payload_too_large` for sizes declared before reading,
502 for an upstream's. The crafted-input corpus of the kit (R-ARCH-010) includes: a tar bomb, a path traversal member, a link member,
a duplicate member name, a `MANIFEST.json` with a name that differs from the file name, a `FILES.json` that lies, an oversized YAML,
and a version document whose `download_url` points to another collection's path.

### R-ANS-032 · Test suites
Priority: **P1** · Covers: trove-only

The testing rule of the phase run asks for integration over HTTP against PostgreSQL, Valkey with Sentinel and S3, the conformance kit, a
counting stub of the public Galaxy, and e2e with the real client on several replicas ([Q-ANS-23](#q-ans-23--the-test-strategy-a-counting-stub-integration-areas-per-class-and-an-e2e-set-ansible-with-two-client-images)).

- **The kit.** `formattest.Run(t, ansible.New(), fixture)` with the fixture of the format: the routes and their examples (kit check 1), the
  crafted inputs through `formattest.InspectTarGz` (R-ANS-031), the remote fixture (a version document and a role document
  and the `Rewrite` of each), the merge fixture (two members' lists and the first-match rule), the view fixture, the setup check (check 8),
  and the guide coverage. A test whose name carries `R_ANS_nnn` proves that requirement's criterion and only that.
- **The counting stub `test/ansiblestub`** (a program and a package, in the style of `test/npmstub`, `test/debstub`, `test/ocistub`): it
  serves the Galaxy documents of R-ANS-003/004 and the index and the v1 role routes from a corpus generated in code (collections built
  with a tar writer, so no fixture file is binary), counts every request by path, offers a control API (`/_control/…`) to reset
  and read the counts, to switch modes (`gzip`, `redirect` as the public Galaxy, `ratelimit`, `slow`, `reject_token`, `digest_changed`,
  `wrong_digest`), to require a bearer realm (the hub exchange), and a GitHub stand-in for role archives on a second host name. Its
  corpus includes a collection with 250 versions (paging), a role with a `download_url` on an unlisted host, and a signed version
  (opaque signature strings).
- **Integration** (`test/integration/…`, run on the shared stack through the slot wrapper, one area per class as for the other
  formats): `ansiblelocal` (publish over HTTP in both orders of parts, task polling, immutability, tombstones, the three read
  operations, `/files/` upload, error shapes, `Token`/Basic/Bearer, permissions, ownership, reindex), `ansibleremote` (discovery, paging,
  rewriting, digest verification, the changed digest, FillInspector, roles, a mirror pass, negative entries, the hub exchange with the
  stub's realm), `ansiblevirtual` (merge, first match, conflict counter, claims, shadowing check, publish through the target, roles by
  member id), and `importer` gains `ansible_test.go`. The stack's Sentinel areas stay with the integration builder.
- **e2e** with the real `ansible-galaxy`, in a set of its own: **`ansible`** (`deploy/dev/e2e.sh`'s naming; CI job `e2e (ansible)`).
  Client pods of two images pin ansible-core **2.17.14** and **2.21.6** (their Python versions as each requires, checked at build); the
  tests: `collection publish` to a local repository on one replica and `install` through each of three replicas; `install` and
  `requirements.yml` (collections and a role) through the remote (counts at the stub: one upstream request per document however many clients
  and replicas), through a mirror (no stub request after the pass), and through the virtual repository (local and remote versions in
  one list, publish through the target); `role install` by token-less `~/.netrc` on a private remote; a wrong token and a repository
  without a token fail with the client's own messages; `server_list` of two servers; a digest the stub changed fails the client; the
  `scatter` profile; the closing run adds Valkey stopped.
- The documentation (R-ANS-033 to R-ANS-046) adds `docs-blob/` to the stub, the integration cases per class (R-ANS-038) and an e2e row per class with a real collection (R-ANS-046).
- The assertions of the e2e tests name R-ANS IDs; a leg that is red is a finding, not a blocker of the release (CLAUDE.md, CI until the
  first stable release).

### R-ANS-033 · The documentation of a collection version: what trove keeps, per class
Priority: **P1** · Covers: trove-only

The owner's galaxy_ng shows a collection's documentation (README, modules, plugins, roles, each with its page; Q-ANS-56 records why trove
shows it too). In trove it is the **wiki part** of the repository (R-ANS-037). "The documentation of a version" is: the README, the Markdown
files of the collection's `docs/` directory, and one **entry** per content item of the collection: a module, a plugin of any type, a role.
An entry has a type, a name, a short description and, where the source documents it, the documentation of the plugin in the Galaxy form
(`doc`, `return`, `examples`, R-ANS-035). The documentation is **derived data**: nothing in it is a source of truth for the archive, and it
can be rebuilt (below).

| Class | Where the documentation comes from | What is kept | When |
|---|---|---|---|
| **Local** | **Generated by trove** from the archive at publish (R-ANS-034) | Rows of the three tables below, `source = 'generated'` | In the commit transaction of the publish; rebuilt by the `ansible-reindex` job |
| **Remote**, pull-through | The upstream's `docs-blob/` document, taken as it is (R-ANS-035, R-ANS-042) | The document as a stored remote document (R-REPO-043, bounded) **and** the rows derived from it, `source = 'upstream'` | After the version document is filled, by a job (R-ANS-042), or by a client's request for the route; only when the setting `ansible.fetchDocumentation` is on (default `true`) |
| **Remote**, mirror | As pull-through, for every version the pass keeps | As pull-through | The pass queues the same job after the archive (R-ANS-040) |
| **Virtual** | The member that serves the version document (first match, R-ANS-022) | Nothing of its own | A read resolves the winner and reads its rows (R-ANS-041) |

- **The tables** (data port `internal/formatdata/ansible`; migrations **00071** `ansible_doc_tables` (the first three tables) and **00072**
  `ansible_doc_fetches` (the fourth); each file starts with `-- trove:phase expand`
  and comments its tables as 00070 does; `name` and `version` are `text COLLATE "C"` with the length checks of 00070; every column below is the
  contract, the indexes beyond the primary keys are the builder's):

```sql
-- one row per version that has documentation
CREATE TABLE ansible_version_docs (
  repo_id      bigint  NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
  name         text    NOT NULL,                  -- <ns>.<name>
  version      text    NOT NULL,
  source       text    NOT NULL CHECK (source IN ('generated', 'upstream')),
  generator    integer NOT NULL,                  -- the version of the code that derived the rows; a reindex rebuilds older ones
  entry_count  integer NOT NULL,
  total_bytes  bigint  NOT NULL,                  -- of the stored entries and files, against the bound of R-ANS-031
  truncated    boolean NOT NULL DEFAULT false,    -- a bound cut entries or files off
  warnings     jsonb   NOT NULL DEFAULT '[]',     -- [{path, code, message}], at most 100
  built_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, name, version)
);
-- one row per content item
CREATE TABLE ansible_doc_entries (
  repo_id bigint NOT NULL, name text NOT NULL, version text NOT NULL,                    -- the key of the row above
  content_type text NOT NULL, content_name text NOT NULL,                                 -- the Galaxy's content_type and content_name
  short_description text NOT NULL DEFAULT '',
  has_docs    boolean NOT NULL,                   -- doc_strings present
  doc         jsonb,                              -- doc_strings in the Galaxy form of R-ANS-035; null when has_docs is false
  readme_md   text,  readme_html text,            -- roles: the generated source, or the upstream's HTML as received
  search_text text NOT NULL DEFAULT '',           -- built by Go, at most 4 KiB: the name with '_' and '.' as spaces, option and return names, the plain text of the descriptions
  search      tsvector GENERATED ALWAYS AS (to_tsvector('simple', search_text)) STORED,
  PRIMARY KEY (repo_id, name, version, content_type, content_name),
  FOREIGN KEY (repo_id, name, version) REFERENCES ansible_version_docs ON DELETE CASCADE
);
-- the README and the Markdown files of docs/
CREATE TABLE ansible_doc_files (
  repo_id bigint NOT NULL, name text NOT NULL, version text NOT NULL,
  file_name text NOT NULL,                                       -- 'README.md' or the file name below docs/
  format text NOT NULL CHECK (format IN ('markdown', 'html')),   -- generated: the source; upstream: HTML as received
  body   text NOT NULL,
  PRIMARY KEY (repo_id, name, version, file_name),
  FOREIGN KEY (repo_id, name, version) REFERENCES ansible_version_docs ON DELETE CASCADE
);
-- remote only: what the last attempt to fetch the documentation of a version came to (the states pending and unavailable)
CREATE TABLE ansible_doc_fetches (
  repo_id bigint NOT NULL REFERENCES repositories (id) ON DELETE CASCADE, name text NOT NULL, version text NOT NULL,
  state      text NOT NULL CHECK (state IN ('queued', 'unavailable', 'none')),
  reason     text NOT NULL DEFAULT '' CHECK (reason IN ('', 'upstream_error', 'too_large', 'invalid_document')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (repo_id, name, version)
);
```

- **Why the search text is a column of its own.** The plain text of the descriptions is extracted in Go (once, bounded), so the generated
  `tsvector` is a function of one text column and the same on every replica. An identifier such as `check_host_reachable` is one token for the
  `simple` configuration, which is why the search text repeats the name with its separators as spaces and the search also matches the name as a
  substring (R-ANS-036).
- **Deleting the version** deletes its rows (`OnItemsRemoved`; the cascade of the repository). The four tables are **not exported** by a backup
  (R-BAK-004): a restore enqueues the reindex, which rebuilds the generated rows from the archives and the remote's rows from the stored
  documents; a restore without remote caches (R-BAK-008) drops the remote's rows with the documents (R-ANS-029, R-ANS-044).
- **States** the screens and the operations report: `available` (an `ansible_version_docs` row exists), `pending` (a `queued` fetch row and no
  documentation row), `unavailable` (a fetch row with that state; `reason` is `upstream_error`, `too_large` or `invalid_document`), `none` (neither: the
  upstream says 404, `fetchDocumentation` is off, a mirror has not reached the version, or a local version has not been reindexed since the feature exists).
  A `queued` row older than one hour is treated as `none` (the job is lost; the next version document queues it again).
- **A version without documentation is not an error.** The package page shows the tab only when the state is not `none` or the version has a
  README (R-UI-060); the protocol route answers 404 in the Galaxy shape.
- **No migration of old versions.** A local version published before this feature has state `none` until the reindex has read it. The
  reindex job is the way (Q-ANS-26), nothing more is built for it (CLAUDE.md, pre-alpha).

Acceptance criteria:

- After the publish of a collection with 2 modules and 1 filter plugin, `ansible_version_docs` has one row with `source = 'generated'` and
  `entry_count` 3 (plus the entries of `module_utils` and `doc_fragments` files, which have `has_docs` false), in the commit transaction: a publish that
  fails after the generation leaves no row.
- A version deleted through the management API leaves no row in any of the four tables.
- A remote with `fetchDocumentation: false` makes no request for `docs-blob/` against the stub (its counters show it).

### R-ANS-034 · Generating the documentation of a local publish
Priority: **P1** · Covers: trove-only

`Inspect` (R-ANS-010) reads the archive once as a stream (Q-ANS-30) and additionally collects the documentation inputs. The generator is a
leaf package of the format (`internal/formats/ansible/docgen`, no I/O, no database); the archive reader hands it members, it hands back entries
and files. **It executes nothing**: no `ansible-doc`, no Python, no import of a plugin, no template; every input is text that is scanned and
decoded under bounds ([Q-ANS-58](#q-ans-58--the-generator-scans-the-python-source-for-its-documentation-strings-and-decodes-them-as-yaml-nodes-it-runs-no-ansible-doc)).

- **Which members.** The content items are found from the file list: a `.py` file below `plugins/<dir>/` (also in sub-directories), for `<dir>` in
  `modules`, `module_utils`, `doc_fragments`, `action`, `become`, `cache`, `callback`, `cliconf`, `connection`, `filter`, `httpapi`, `inventory`,
  `lookup`, `netconf`, `shell`, `strategy`, `terminal`, `test`, `vars`; the content type is the directory (`modules` becomes `module`); the
  content name is the path below the directory with dots for slashes and without `.py` (`storage/s3.py` is `storage.s3`); a file named
  `__init__.py` is no item, nor is one whose name starts with `_` (private and deprecated modules); a name that does not match
  `^(?!.*__)[a-z_][0-9a-z_]*$` per part makes the entry a warning and no item. Roles: a directory `roles/<role>/` is a role item with the role
  name; its `README.md` (at most 512 KiB) is its `readme_md`. `playbooks/` and `meta/` are no items. The README is the member named by
  `collection_info.readme` or, if empty, `README.md` at the root; the files are the `.md` files directly in `docs/` (at most 64, each at most
  512 KiB; the importer's bound); a file that is not UTF-8 is a warning and no file.
- **How the reader hands members over (one pass).** `collection.ReadArchive` takes an optional tap (`collection.WithTap(Tap)`; `Tap.Wants(path)
  bool` and `Tap.Take(path, io.Reader)`); for a member the tap wants, the reader streams it through a writer that hashes it (as for every member) **and**
  keeps its first N bytes (N is the generator's bound for that member, 2 MiB for a source, 512 KiB for Markdown) and discards the rest, so a
  member over its bound costs time and no memory and never refuses the archive. The tap keeps only what the generator needs of a member (the
  literals, the Markdown) and drops the source at once. The name of the README is known only when MANIFEST.json has been read, which may come
  after the member in the tar: the generator keeps every Markdown candidate (`README.md` at the root and `docs/*.md`) and `Build(readmeName)` picks.
  A tap that fails does not fail the archive: its error is a warning.
- **Which strings.** For a `.py` item whose type is not `module_utils` or `doc_fragments`, up to 2 MiB of source is scanned for module-level
  assignments `DOCUMENTATION`, `EXAMPLES` and `RETURN` whose value is one string literal (single, double or triple quotes; prefixes `r`, `u`, `R`, `U`; a
  parenthesized literal; implicit concatenation of adjacent literals). A sidecar `plugins/<dir>/<name>.yml` (or `.yaml`) next to the module or
  plugin holds the same three keys as YAML and wins over the source. Anything else (an f-string, a value built by a name or a call, a literal
  inside a class or a function) is **not read**: the item stays in the list with `has_docs` false and no warning, except for a literal that
  exceeds 512 KiB or does not close, which is a warning (`docs_literal_too_large`, `docs_literal_unterminated`). `doc_fragments` and
  `module_utils` are listed and have no `doc` ([H-891](#h-891--documentation-fragments-are-not-merged-into-the-documentation-of-a-generated-entry)).
- **Decoding.** The text of `DOCUMENTATION` and `RETURN` is YAML decoded into a node tree and walked, never into Go values, with the walker and the
  refusals of `meta/runtime.yml` (Q-ANS-30): an anchor, an alias, a tag or a merge key, more than 20,000 nodes or a nesting deeper than 24 makes
  the entry `has_docs` false with the warning `docs_yaml_refused`. `EXAMPLES` stays a text. The result is shaped like the Galaxy's
  `doc_strings` (below): `doc.options` and `return` become lists of named objects, with `suboptions` and `contains` nested; keys not in the list of
  R-ANS-035 are dropped; scalar values keep their type (a default `1000` stays a number, `true` a boolean, a string a string).
- **Short description.** `doc.short_description` as text, at most 300 characters, control characters removed; an entry without one has an empty
  string.
- **Bounds.** Those of R-ANS-031 (a literal 512 KiB, a source file 2 MiB, 256 MiB of source scanned per archive, 1 MiB per stored entry, 5,000 entries and 16 MiB per version, 512 KiB
  per README and file). Past the 256 MiB the remaining sources are listed with `has_docs` false and one warning `docs_budget_exhausted`, so a crafted archive cannot spend the request's deadline on scanning. A bound crossed never fails the publish: the entry (or file) is kept with `has_docs` false (or dropped), the warning names
  the member and the code, `truncated` is set when entries were dropped, and the import task of the publish carries the warnings as messages of level
  `WARNING` (R-ANS-009; at most 100 warnings, the rest counted in one last message). The documentation is a convenience; a collection that
  `ansible-galaxy collection build` accepted is never refused for its documentation.
- **Determinism.** The same archive gives the same rows, entry by entry (the order of the entries is by type, then name); the `generator` column is the
  integer the package constant `docgen.Version` holds, bumped by a change of the generator's output. The reindex rebuilds a row whose `generator`
  is below the constant (R-ANS-012's job reads the archive anyway).
- **Where it runs.** In the publish request, in the same pass as the rest of `Inspect` (Q-ANS-57), under the 30-second deadline of the core ([H-843](#h-843--a-publish-inspects-the-archive-inside-the-request)):
  the inputs are kept in memory while the stream is read (at most the bounds above: 16 MiB of entries, 32 MiB of files and sources in flight), and the rows
  are written by `OnCommit` in the commit transaction (R-ANS-011), after the version row. If the deadline is crossed while generating, the publish
  fails as it does for any slow inspection; there is no partial documentation.
- **Untrusted by construction.** The text of an entry is written by whoever published the collection. It is stored as text and structure, never as
  HTML, never evaluated, and it reaches a browser only through the converter and the sanitizer of R-ANS-039. Names are not concatenated into a
  path of the blob store; they are values of a column.

Acceptance criteria:

- Crafted sources through `formattest.InspectTarGz` and the generator's unit tests: a module with a `DOCUMENTATION` in a triple-quoted raw string
  with options and suboptions; one with a sidecar `.yml`; one whose `DOCUMENTATION` is assembled by `"".join(...)` (no docs, no warning); an
  unterminated literal (warning); a 600 KiB literal (warning); YAML with an anchor, with `<<` and with 30,000 nodes (each `docs_yaml_refused`); a
  `DOCUMENTATION` inside a function (not read); an `__init__.py` and `_private.py` (no items); a name with a double underscore (warning); an
  `EXAMPLES` with a `</script>` (kept as text); 6,000 plugin files (5,000 entries and `truncated`); the same archive twice (identical rows).
- The README of `hans_fischer.common`-like collection (`README.md` with one heading) is `ansible_doc_files` row `README.md`, `markdown`.
- A publish whose documentation hits a bound is accepted, and the import task shows the warning.
- No test, no code path of the generator imports `os/exec`: `go list -deps` of the package contains no `os/exec` (a test).

### R-ANS-035 · The documentation as a protocol document, per class
Priority: **P1** · Covers: trove-only

One new route in the route table of the format: `ansible.version.docs`, `GET v3/collections/{ns}/{name}/versions/{version}/docs-blob/`, kind
Metadata, action `read`, classes all, gated by the wiki check of R-AUTHZ-034 (R-ANS-037). It is the document a Galaxy-compatible tool and another
trove (as the upstream of a remote) read; no client of the matrix does. The version document keeps leaving `metadata.contents` out
([Q-ANS-62](#q-ans-62--the-documentation-is-served-at-docs-blob-and-the-version-document-keeps-leaving-the-plugin-listing-out)); a tool that wants the list reads this document.

- **Shape** (the Galaxy's, as observed on both servers, Client and library behaviour above):

```json
{"docs_blob": {
  "collection_readme": {"name": "README.md", "html": "<h1>hans_fischer.common</h1>…"},
  "documentation_files": [{"name": "install.md", "html": "…"}],
  "contents": [
    {"content_name": "check_host_reachable", "content_type": "module",
     "doc_strings": {"doc": {"module": "check_host_reachable", "short_description": "…", "description": ["…"],
                              "options": [{"name": "host", "type": "str", "required": true, "description": ["…"],
                                           "suboptions": [{"name": "…"}]}],
                              "author": ["…"], "version_added": "1.2.0", "notes": ["…"], "requirements": ["…"], "seealso": [{"…": "…"}],
                              "attributes": {"…": {}}, "deprecated": {"…": "…"}},
                     "return": [{"name": "…", "returned": "always", "type": "str", "description": ["…"], "sample": "…", "contains": [{"name": "…"}]}],
                     "examples": "- name: …\n  …\n", "metadata": null},
     "readme_file": null, "readme_html": null}]}}
```

  `doc` keeps these keys: `module` (the plugin's name), `plugin_type`, `short_description`, `description`, `version_added`, `author`, `notes`,
  `requirements`, `options` (each: `name`, `type`, `elements`, `required`, `default`, `choices`, `aliases`, `description`, `version_added`,
  `suboptions`), `seealso`, `attributes`, `deprecated`, `extends_documentation_fragment`; `return` is a list (each: `name`, `type`, `elements`,
  `returned`, `description`, `sample`, `version_added`, `contains`). A key not in this list is dropped when the rows are written. The prose values
  (`description`, `notes`, `requirements`, `short_description`, `seealso[].description`, `options[].description`) are the Ansible markup of
  R-ANS-039 as text, never HTML, **except** in the `html` and `readme_html` members, which are HTML.
- **Local**: rendered from the rows; `collection_readme.html` and `documentation_files[].html` are the HTML the Host's Markdown renderer makes of the
  Markdown rows, and `readme_html` of a role likewise (R-ANS-039; the format imports no renderer). The order of `contents` is by type, then name. Weak `ETag` (a digest of the bytes) and
  `If-None-Match` as the other documents (Q-ANS-35). A version without a row is 404 `not_found`.
- **Remote**: the route is an ordinary **metadata document of the remote** (R-ARCH-013): the engine fetches it from the upstream, stores it as
  received at the canonical path `collections/<ns>/<name>/docs/<version>.json` (`Classify` maps it to metadata of the package, never to an artifact),
  revalidates it like every other document and serves it through `Rewrite`. The format adds three things to the engine's path:
  - `UpstreamRequest` maps the path to the upstream URL: the version document's `href` (read from the stored version document, never from the
    request; it reaches the engine as the URL-map row the fill of the version document records, [Q-ANS-73](#q-ans-73--the-address-of-the-documentation-is-the-url-map-row-of-the-version-documents-fill-and-a-recorded-none-keeps-the-route-from-asking-again)) plus `docs-blob/`; the URL must be on the upstream's origin or an `extraHosts` entry and its path must end in `/versions/<version>/docs-blob/` (the
    host check of the `download_url`, R-ANS-018); a version document without a usable `href` has no documentation (404, state `none`).
  - `PrepareFill` validates the shape before anything is stored, outside every transaction (as for the version document, R-ANS-017): JSON of the shape
    above, `contents` an array of at most 5,000 items with `content_name` matching `^(?!.*__)[a-z_][0-9a-z_.]*$` and a `content_type` among the types of
    R-ANS-034 and `role`, no item above 1 MiB of `doc_strings` kept (a larger one is kept as a name and short description with `has_docs` false and the
    set `truncated`), the document at most `MaxDocumentBytes` (16 MiB, the descriptor's bound; the largest seen is 6.5 MB). An error is 502
    `upstream_unavailable` and keeps the previous entry; the fetch row becomes `unavailable` (`too_large` or `invalid_document`).
  - `OnFill` writes the rows (`source = 'upstream'`, `generator` 0) in the fill transaction, under the entry's row lock, with `PutDocs`; an upstream 404 (a
    negative entry of 300 s) writes the fetch row `none`; a 5xx or a network error writes `unavailable` with `upstream_error`.
  The upstream's `html` and `readme_html` members are **untrusted HTML**: `Rewrite` serves the stored document with every one of them run through the
  Host's sanitizer (R-ANS-039); the rest of the document passes unchanged. The engine's rules apply as they are: the credential and its host rule,
  the egress check, the rate limit, the circuit and the one-flight rule. Who triggers the fetch: a client's request for this route (another
  trove, a Galaxy-compatible tool) and the job of R-ANS-042; **a UI read never queries the upstream** (R-API-029), it reads rows.
- **Virtual**: the member that serves the version document (R-ANS-022, first match over the same plan) serves this document too, through its own
  class (R-ANS-041); a caller who may not fill a remote member's cache (R-AUTHZ-008) gets the documentation of the cached or local members only.
- **Setting.** `ansible.fetchDocumentation` (boolean, remote only, default `true`; the format's settings schema, R-ANS-026) switches the **job**
  and the fetch by the route off: with `false` the route answers 404 for a remote and the rows are not written; a client that wants the document
  from an upstream goes to that upstream.
- **Not served**: the `docs-blob/` of a collection (without a version), a listing of versions' documentation, any write.

Acceptance criteria:

- Against the stub with a `docs_blob` (R-ANS-038, R-ANS-046): `GET …/versions/1.0.0/docs-blob/` on the remote answers the stub's contents; the stub counts one
  request for it however many clients and replicas read; the stored document is byte-identical to the stub's.
- The same on a local repository for a published collection returns `contents` with the generated entries, and the same document for the
  virtual repository over it.
- An upstream whose `readme_html` holds `<script>` is served without it; the stored document still holds it.
- A `href` on another origin is not fetched (the stub counts none) and the state is `none`.
- A document with an item without `content_name` is 502 and the previous document stays; the fetch row says `invalid_document`.
- A remote whose upstream answers 404 for `docs-blob/` has state `none` and its screen shows no documentation, not an error.

### R-ANS-036 · The documentation operations of the screens, their rendering and the search
Priority: **P1** · Covers: trove-only

Three management operations under the package, read by the UI (api-and-ui.md, [Q-UI-240](../api-and-ui.md#q-ui-240--an-ansible-package-has-a-documentation-tab-and-a-contents-tab-the-readme-tab-is-its-first-page), R-API-029's family; each is declared wiki content in the OpenAPI document, Q-API-260, R-ANS-037). They
are bound to the generic package and version (the ids of R-API-029), because the screen already holds them; a `repository` query parameter
(`<projectKey>/<repoKey>`) selects the repository, and without it the first repository that may be read and holds the version with a state other than `none` is
used (local before remote, the order of the repositories list); a virtual repository in `repository` is resolved to its winner member by the
plan of R-ANS-022. The answer names the repository it was read from.

| Operation | Method and path | Answer |
|---|---|---|
| `getPackageVersionDocs` | `GET /api/v1/packages/{packageId}/versions/{version}/docs?repository=&q=` | The tree: state, source, the groups with their entries, the files (below) |
| `getPackageVersionDocEntry` | `GET /api/v1/packages/{packageId}/versions/{version}/docs/entries/{type}/{name}?repository=` | One page: the documentation of one module, plugin or role |
| `getPackageVersionDocFile` | `GET /api/v1/packages/{packageId}/versions/{version}/docs/files/{file}?repository=` | The README or a Markdown file as sanitized HTML |

- **The tree** (`getPackageVersionDocs`):

```json
{"state": "available", "source": "generated", "repository": "acme/galaxy-local", "builtAt": "2026-11-11T09:41:02Z", "truncated": false,
 "files": [{"name": "README.md", "title": "hans_fischer.common"}, {"name": "install.md", "title": "install"}],
 "groups": [
   {"category": "module", "label": "Modules", "count": 8, "types": [
     {"type": "module", "count": 8, "entries": [
       {"type": "module", "name": "check_host_reachable", "shortDescription": "Checks whether a host answers", "hasDocs": true}]}]},
   {"category": "plugin", "label": "Plugins", "count": 24, "types": [
     {"type": "filter", "count": 8, "entries": [{"type": "filter", "name": "is_cidr", "shortDescription": "", "hasDocs": false}]},
     {"type": "module_utils", "count": 16, "entries": []}]},
   {"category": "role", "label": "Roles", "count": 0, "types": []}],
 "warnings": [{"path": "plugins/modules/x.py", "code": "docs_literal_unterminated", "message": "…"}]}
```

  `state` is one of `available`, `pending`, `unavailable`, `none` (R-ANS-033); for the last three only `state`, `reason` where it applies and
  `repository` are present. `files[0]` is the README when there is one (`title` is the first heading of the Markdown as text, at most 120
  characters, the file name otherwise). `category` is `module` for type `module`, `role` for `role`, `plugin` for every other type;
  groups are always in the order modules, plugins, roles; types and entries are in alphabetical order. `count` is the number of entries of
  the group **before** `q` filtered them, so the counts of the tree do not change while one searches (the owner's screen: "Modules 8",
  "Plugins 24"); `matchCount` (only with `q`) is the number of entries left. `warnings` only for a caller who holds `deploy` on the
  repository (they describe the publisher's sources). The answer is at most 5,000 entries and about 1 MB.
- **Search** (`q`): the text is split on white space into at most 8 terms of at most 64 characters; control characters are removed; a text of
  fewer than 2 characters is `422 invalid_query`. An entry matches when **every** term matches its name (case-insensitive substring) or its
  `search` vector (prefix match of a term over the words of short description, description, option names and return names; the terms are
  reduced to letters and digits for the vector, never put into a query text). Matching entries keep the order above; a `files` item
  matches by its title. `q` is evaluated in SQL over the rows of one version (at most 5,000), no index is needed beyond the primary key and the
  vector's own. The search is **inside the documentation of one version**; trove has no search across collections here.
- **The entry** (`getPackageVersionDocEntry`): `{type, name, fullName, shortDescription, hasDocs, source, builtAt, versionAdded, deprecated,
  description[], notes[], requirements[], author[], options[], returnValues[], examples, seealso[], attributes[], extendsFragments[],
  readmeHtml}` where `description`, `notes`, `requirements` and the `description` of an option or return value are **arrays of sanitized HTML
  strings**, one per paragraph; names, types, `returned`, `version_added`, authors, choices, defaults and samples are text and typed JSON
  values (the UI shows them as text, a default of `1000` as `1000`); `options` and `returnValues` are the named lists of R-ANS-035 with
  `suboptions` and `contains` nested to a depth of 12 (deeper is dropped with the warning kept in the tree's `warnings`); `examples` is the text
  (the UI shows it in a code block; trove does not highlight, does not run); `seealso` entries carry `module`, `description` and `link` (a link is kept only
  for `http` and `https`); `extendsFragments` is the list of fragment names that were **not** merged (H-891); `readmeHtml` is set for roles. `fullName` is
  `<ns>.<name>.<content name>`. An entry that does not exist is `404 not_found`; one with `hasDocs` false answers 200 with the header
  fields and no sections.
- **The file** (`getPackageVersionDocFile`): `{name, contentType, html, truncated, externalImagesBlocked}`, as the README answer of R-API-029 (the
  README operation stays as it is; the Documentation tab of an Ansible version uses this one for its first page). The `file` is `README.md` or a name from the tree; any
  other is 404. Markdown rows are rendered by the renderer and sanitizer of R-UI-008 on every request with a strong `ETag` over the source,
  the renderer version and the image policy hash (the README rule of R-API-029); HTML rows (upstream) are sanitized by the same policy.
- **Rendering of prose and Markdown** is R-ANS-039: the three operations convert the Ansible markup of the prose with `docgen.RenderMarkup` and sanitize
  the result with the README renderer's policy (R-UI-008) in `internal/api/v1`, so the answer holds sanitized HTML and no field is HTML that was not.
  **reStructuredText is not rendered** (the importer of the Galaxy does not either, Client and library behaviour): a `README.rst` is no README, a
  `.rst` file below `docs/` is no file.
- **Version details.** The generic version details of R-API-029 get, for the format `ansible`, the member `metadata.docs`
  (`{"state": "available", "source": "generated", "entries": 32}`) so that the page chooses its tabs without a probing request. It is wiki content and is
  **absent** for a caller who may not read the wiki (R-ANS-037). `hasReadme` stays what R-API-029 says.
- **Limits and cost.** The three operations are `read` bucket calls (R-API-016). A page of the documentation is one primary-key read; the tree is one
  query over at most 5,000 rows; the request path never reads the archive or the stored document of a remote.

Acceptance criteria:

- The tree of a published collection with 8 modules, 8 filters and 16 `module_utils` files has groups Modules 8 and Plugins 24, and `q=host` returns the
  entries whose name, short description or option names contain a prefix of `host` while the counts stay 8 and 24 and `matchCount` is the number left.
- An entry whose description holds `C(<img src=x onerror=1>)` renders the text escaped inside `code`; one whose `L(x,javascript:alert(1))` renders the
  text `x` and no `a`.
- An entry of a remote whose upstream `doc_strings` is null is `hasDocs` false and 200.
- `q=a` is `422 invalid_query`.
- A caller who may not read the repository gets 404 with the answer of an id that does not exist (R-API-029).
- A caller who may read the repository and not its wiki gets what R-ANS-037 says for each of the three operations (the wiki check of R-AUTHZ-034 answered no).

### R-ANS-037 · The documentation is wiki content: which routes, documents and fields the wiki check gates
Priority: **P1** · Covers: trove-only

Task T-18 gives a repository a **wiki part** whose visibility is set apart from its artifacts. [R-AUTHZ-034](../authorization.md) is the one
requirement that defines the wiki part, **the wiki check** that format code calls, and what a caller gets who may read a repository's artifacts and
not its wiki. This page does not define the check. It lists **exactly which surfaces of the Ansible format are wiki content**, states that each
is gated by the wiki check of R-AUTHZ-034, and what the Galaxy client and the UI see when the check says no.

| Surface | Wiki content | When the wiki check says no |
|---|---|---|
| The route `ansible.version.docs`, `GET v3/collections/{ns}/{name}/versions/{version}/docs-blob/` (R-ANS-035), all classes | **yes**: it serves only wiki content | The answer R-AUTHZ-034 decides for a route that serves only wiki content; the format passes the check's refusal on **unchanged** and rewrites nothing |
| The operations `getPackageVersionDocs`, `getPackageVersionDocEntry`, `getPackageVersionDocFile` (R-ANS-036) | **yes**: they serve only wiki content | As R-AUTHZ-034 decides for an operation of the management API that serves only wiki content |
| The member `metadata.docs` of the version details (R-ANS-036) | **yes**: it announces documentation | The member is **absent**; the details of the version are otherwise as before |
| The generic `readme` operation and the member `hasReadme` of the version details, for the format `ansible` | **yes**: the README is the first page of the documentation. What a README is per format is R-AUTHZ-034's; for `ansible` the README and the Documentation tab are one thing | As R-AUTHZ-034 decides for the generic README: `hasReadme` is false, the operation answers as for a version without a README |
| The stored rows and documents of R-ANS-033 and R-ANS-035 | **yes** (the data the above read); no other code path reads them | Not reachable except through the surfaces above |
| `metadata.contents` and `metadata.docs_blob` of a version document, were they served | **yes**: both are wiki fields. **Today trove serves neither** (Q-ANS-10, Q-ANS-62), so no field is to be removed; a future change that serves one gates it | The **fields are absent** from the document |
| The version document (`description`, `homepage`, `documentation`, `issues` and `repository` links, `metadata.readme`, which is a **file name**, not the README text), the version list, the collection document, the archive, the import tasks | **no**: artifact and management content | They follow the artifact check as before |
| The `warnings` of the tree | **no**: shown only to a caller who holds `deploy`, as the import task is | `deploy` |

- **One call site.** Every handler of the table asks the wiki check through one function of the format package, `wikiReadable(ctx, rq or caller,
  repository)` in the new file `wikigate.go`, and nowhere else: it is the only file that names the check's Go name (fixed by T-18's WP0, "the wiki
  check of R-AUTHZ-034"). The format imports no authorization package (R-ARCH-008); the check reaches it as a Host method or a field of the request,
  as T-18's WP0 decides, and `wikigate.go` is the adapter. A test proves that **every** surface in the table calls `wikiReadable`, from a registry of the surfaces
  that the handlers register themselves into, so a surface added later cannot skip the gate: `TestR_ANS_037_EverySurfaceAsksTheWikiGate`.
- **Declared in the OpenAPI document.** The three operations are marked as wiki content in `api/openapi.yaml` (Q-API-260), and the route carries the
  wiki part in its descriptor; where T-18 enforces the check before the handler (the dispatcher, the OpenAPI-driven authorization), the marker is
  what it reads, and the call in the handler stays as the second layer.
- **Virtual and remote.** The check is asked for the repository the documentation is **read from**: the remote itself for a remote repository; for a
  virtual repository, for **the virtual repository addressed** (its own wiki level) **and** for the member the first-match plan picks among the
  members the caller may read (R-ANS-022). Both must allow; a member the caller may not read the wiki of is skipped like a member the caller may not
  read, and the member is never named. The documentation never mixes members (Q-ANS-61).
- **What the UI sees** (Q-UI-244): `metadata.docs` absent, so the Documentation and Contents tabs are not shown and not requested; a 404 or the
  refusal of an operation removes the tab without a message.
- **What a Galaxy client sees:** `ansible-galaxy` reads none of these surfaces; installs, listings and `requirements.yml` are unchanged whatever the
  wiki level is. A tool that reads `docs-blob/` gets the answer of the first row of the table.
- **Not decided here:** the levels, the settings, the defaults, the inheritance, the role of an external user, the audit of a wiki read, the caches a change
  must reach. R-AUTHZ-034 decides them and a change of its answers needs no change on this page.

Acceptance criteria:

- `TestR_ANS_037_EverySurfaceAsksTheWikiGate` fails when a handler of the table does not call `wikiReadable`.
- With a fake check that denies, each surface of the table answers as the table says (the route and the operations as the fake's refusal says, the member
  absent) **and** the version document, the version list and the archive of the same version are still served.
- With the fake denying for one of two members of a virtual repository, the documentation of the other member is served and the denied one is not named.
- The `wiki` row of R-ANS-046 (a user who reads the artifacts and not the wiki) runs against T-18's real check.

### R-ANS-038 · Tests of the documentation: unit and integration
Priority: **P1** · Covers: trove-only

- **Unit.** `docgen` (R-ANS-034): the crafted sources of its acceptance criteria and a corpus of module sources written for the tests in the shape of real
  ones (options with suboptions, choices, defaults of several types, return values with `contains`, a sidecar, the markup macros), the markup converter with a table of macros and
  hostile inputs (unbalanced macros, 10,000 nested macros, a macro in a macro, `\)` escapes) and a fuzz target for both; the tree and
  entry builders over rows; `Rewrite` of the stored document with a `<script>`; `ParseUpstreamDocs` over excerpts of the two real documents (below); the wiki gate registry (R-ANS-037). A test whose name carries
  `R_ANS_03n` proves that requirement's criterion and only that.
- **Real documents as test data.** The testdata of `galaxydoc` are cut-down excerpts of `docs-blob/` documents of the public Galaxy (a small collection
  such as `ansible.netcommon` 8.7.1, 155 KB, with its README HTML, and ten entries of `community.general` 10.0.0) and a document in the shape of the owner's
  `hans_fischer.common` 1.5.0 (32 entries, every `doc_strings` null). They hold no token and no address of a private server.
- **The stub** (`test/ansiblestub`) serves `docs-blob/` for the versions of its corpus: a collection with 8 modules (one with suboptions and a
  return value, one with `doc_strings` null), 8 filters, 16 `module_utils`, a role with a `readme_html`, a README with a script tag, and a
  collection of 600 entries for the bounds (about 4 MB, built by the corpus generator, no fixture file is binary); the control API adds the
  modes `docs_404`, `docs_500` and `docs_huge` (17 MiB) and the counters `docs-blob` per path.
- **Integration** (through the slot wrapper, one area per class): `ansiblelocal` publishes a collection built with the tar writer (modules with
  documentation, a sidecar, a role, README and `docs/`) and reads the tree, an entry, a file, the search and `docs-blob/`, the deletion of the version, the
  reindex after the rows were deleted by the test, the warnings in the import task, the wiki gate with a denying fake; `ansibleremote` fills the version document, waits for
  the job and reads the same operations from the stub's document, `fetchDocumentation: false`, the 404, 500 and huge modes, the origin check of `href`, the sanitizing of
  the stub's script tag, one stub request however many reads, and a mirror pass that queues the fetch (R-ANS-040); `ansiblevirtual` reads the winner's documentation and the first-match rule for a version in two members (R-ANS-041). The
  Sentinel areas stay with the integration builder.
- **e2e** is R-ANS-046.

Acceptance criteria: the tests above exist and pass; the coverage generator lists R-ANS-033 to R-ANS-046 as implemented only through test names that carry
the IDs (`docs/coverage/implemented.txt` is not touched by this specification).

### R-ANS-039 · Rendering and sanitizing: Markdown, HTML and the Ansible markup
Priority: **P1** · Covers: trove-only

Three kinds of text reach a browser or a client from the documentation, and each leaves trove sanitized by the policy of the README renderer
(R-UI-008, `internal/packages/readme`: goldmark, bluemonday, the link and image rules, the `ui.readme.external_image_origins` list):

| Text | Arrives as | Rendered by | Sanitized by |
|---|---|---|---|
| A README, a file of `docs/`, a role README of a **generated** version | Markdown rows | The Host's Markdown renderer (goldmark) | The same renderer's allowlist |
| `collection_readme.html`, `documentation_files[].html`, `readme_html` of an **upstream** | HTML rows, untrusted | Not rendered | The Host's HTML sanitizer on every serving (R-ANS-035 `Rewrite`; the file operation of R-ANS-036) |
| The prose of a plugin (`description`, `notes`, option and return descriptions, `short_description`, `seealso`) | Ansible markup in the JSON | `docgen.RenderMarkup`: escape all text, then the macros | The same allowlist, in `internal/api/v1`, before the answer leaves |

- **Where it runs.** A format package may import neither the renderer nor bluemonday (R-ARCH-008, `internal/archtest`). The contract therefore
  gains a narrow Host capability, `formats.Markup` returned by `Host.Markup()`: `Markdown(src string) (html string, truncated bool)` and
  `SanitizeHTML(html string) string`, implemented by the composition root over `internal/packages/readme` (which gains `Renderer.SanitizeHTML`, the
  existing allowlist applied to HTML and nothing else), by a deterministic in-memory implementation in the conformance kit's Host, and by a Host
  that has no renderer returning errors, never the input (`NoMarkup`, as `NoVulnerabilityData`). The Ansible format stores the Host's `Markup` in its replica
  (`bind.go`, `Format.Bind`). The three management operations run in `internal/api/v1`, which holds the renderer already (`ps.Readme`) and imports `docgen`
  for `RenderMarkup`; the pure converter is in the leaf `internal/formats/ansible/docgen` so that both can call it.
- **The Ansible markup.** The converter **escapes all text first**, then recognizes `I(x)` and `E(x)` as emphasis, `B(x)` as strong, `C(x)`, `O(x)`,
  `V(x)`, `RV(x)`, `M(x)`, `P(x)` and `R(text,ref)` as `code` (a reference is shown as its text and made no link), `U(url)` and `L(text,url)` as a
  link when the URL is `http` or `https` and as the text otherwise, `HORIZONTALLINE` as a rule, and the backslash escapes of the markup. A macro that
  does not close, or nests deeper than 4, is text; the input of one conversion is at most 64 KiB (more is cut and the answer is marked truncated). The
  output then runs through the sanitizer so that the converter cannot be the weak point; links get `rel="nofollow noopener noreferrer"`.
- **reStructuredText is not rendered**, and neither does the Galaxy's importer (Client and library behaviour): a `README.rst` is no README, a `.rst`
  file below `docs/` is no file, and the page says so in a muted line when `collection_info.readme` names one (the warning `readme_not_markdown`).
- **No HTML is stored for a generated entry.** A generated row holds Markdown and structured text; HTML exists only in an answer. An upstream's HTML is stored
  as received (so the stored document is byte-identical to the upstream's, R-ANS-035) and sanitized on the way out.
- **The UI** binds the HTML the operations return with `[innerHTML]` and everything else as text (R-UI-008's component rules, Q-UI-242).

Acceptance criteria:

- The table of macros has one case per macro, the hostile inputs of R-ANS-038 produce text that contains no tag the allowlist does not name, and the fuzz target
  has no finding in the runs of the gate: for any input, the output of `RenderMarkup` followed by the sanitizer contains no `script`, no `on*` attribute and no URL
  scheme other than `http`, `https` and `mailto`.
- A stored upstream `readme_html` with `<script>`, `<img onerror>` and `<a href="javascript:">` is served at `docs-blob/` and by the file operation without them.
- A kit Host without a renderer makes the Markdown rows of a local `docs-blob/` fail with 500 `internal`, not serve unrendered text.

### R-ANS-040 · A mirror keeps the documentation of the versions it keeps
Priority: **P1** · Covers: trove-only

A remote in mode `mirror` (R-ANS-021) with `ansible.fetchDocumentation` on queues, after the archive of each version it keeps, the documentation fetch job
of R-ANS-042 for that version; the pass does not wait for it and a job that fails does not fail the pass. The job's fetches go through the pass's
background window (`MirrorHost.Background`, the engine's throttling and window halving on a 429 or `Retry-After`, R-REPO-054), so a mirror of many
collections spaces its documentation requests with its archive requests. A version the pass drops (the retention rule of the mirror) loses its rows with
its item (`ItemsRemoved`). The size of a full mirror's documentation is not measured ([H-890](#h-890--the-size-of-the-documentation-of-a-full-mirror-and-of-many-local-versions-is-not-measured)).

Acceptance criteria:

- A mirror pass over the stub with three versions queues three `ansible-docs-fill` jobs and, once they ran, the three versions have `available` documentation;
  with `fetchDocumentation: false` the stub's `docs-blob` counter is 0.
- A stub in mode `docs_500` leaves the pass completed and the versions `unavailable` with `upstream_error`; the jobs retry with the engine's backoff.

### R-ANS-041 · A virtual repository shows the winner's documentation
Priority: **P1** · Covers: trove-only

The virtual class serves `docs-blob/`, and its package screens read documentation, from the member that serves the version document (R-ANS-022, first
match), through that member's class: a local member renders its rows, a remote member serves its stored document through `Rewrite`. The member is the
same one the version document and the archive come from, so the document and its documentation agree. The operations of R-ANS-036 take the virtual
repository in `repository`, resolve the winner by the same plan and name the **winner** in the answer only when the caller may read that member
(otherwise the virtual repository's key is named). A virtual repository fills no remote member's documentation on behalf of a caller who may not fill its
cache (R-AUTHZ-008): the cached rows are served, and a member with none is skipped for documentation only if the version document would be skipped too.
The wiki check applies to both the virtual repository and the winner (R-ANS-037).

Acceptance criteria:

- A virtual repository over a local and a remote member serves, for a version only the remote has, the remote's documentation, and for one the local has,
  the local's, with the same document the member serves directly.
- For a version in both members the first-match member's documentation is served and no entry of the other appears.
- A caller who may not read the first member's repository gets the second member's documentation, and the answer does not name the first.

### R-ANS-042 · The job that fetches a remote's documentation
Priority: **P1** · Covers: trove-only

- **Kind and keys.** `ansible-docs-fill` (queue `index`, dedup `Coalesce` on the key `ansible-docs:<repoId>:<name>:<version>`, at most 5 attempts, the engine's
  backoff, a timeout of 5 minutes per attempt), registered in `internal/formatdata/ansible/jobs.go` beside `ansible-reindex`. Its handler is built by the
  composition root (`Handlers.DocsFill`) over `MirrorHost.Background(ctx, repoID)`, the form the jobs of a data port use (as the Debian format's), and calls
  `Upstream.Document(ctx, DocsDocumentPath(ns, name, version), "")`, which is the engine's metadata fetch: the fill runs `PrepareFill` and `OnFill`
  of R-ANS-035, so there is one code path for the route and the job. The credential, the egress rules and the throttle are the engine's.
- **Who queues it.** `OnFill` of a **version document** of a remote with `fetchDocumentation` on (through the data port, in the fill transaction: a
  `queued` fetch row and the job, `EnqueueDocsFill(ctx, tx, ...)`, as `Reindex` enqueues in a transaction), when the version has no `available` rows
  or the version document's digest changed; the mirror pass (R-ANS-040). Nothing else: not a UI read, not a request for the route (that one fetches
  inline).
- **Outcome.** `available` rows (the fill wrote them); an upstream 404 or a `href` that fails the origin check: the fetch row `none` and the job
  succeeds; `upstream_error`: the job fails and retries; `too_large`, `invalid_document`: the fetch row says so and the job ends permanently (a retry would
  repeat it). A job for a version that was deleted ends at once.
- **Metrics and audit** are R-ANS-045.

Acceptance criteria:

- Eleven concurrent version-document fills of one version queue one job (`Coalesce`), and the stub counts one `docs-blob` request in total.
- A version document refilled with a changed digest queues a new job; one refilled unchanged does not.
- With the stub in mode `docs_huge` the fetch row is `unavailable` with `too_large`, the job is not retried, and no row of the other tables exists.

### R-ANS-043 · Storage and bounds of the documentation
Priority: **P1** · Covers: trove-only

- **Where it lives.** In the rows of R-ANS-033 (PostgreSQL) and, for a remote, as one stored remote document per version (the object store, as every
  document, bounded by `MaxDocumentBytes`). No blob of the documentation is written for a local version: the archive stays the one source.
- **Bounds** are those of R-ANS-031 (the documentation rows of its table), applied by the generator for a local publish and by `PrepareFill` for a remote:
  512 KiB per literal and per Markdown file, 2 MiB per source read, 1 MiB per entry, 5,000 entries, 16 MiB of rows per version, 64 files; the answer of a tree is
  at most 5,000 entries. A bound crossed drops that part, sets `truncated` and warns; it never fails a publish or a fill.
- **Eviction.** A remote's stored `docs/` document is a cache entry and is evicted like any (R-REPO-032); its rows stay until the version's item goes or the
  next fill replaces them, because the rows are what the screens read. A restore without remote caches drops both (R-ANS-029).
- **Per repository.** There is no total bound per repository, only per version ([H-890](#h-890--the-size-of-the-documentation-of-a-full-mirror-and-of-many-local-versions-is-not-measured)); the measured largest set of
  rows is 3.5 MB of JSON for 833 entries (community.general) and 6.5 MB for fortinet.fortios.

Acceptance criteria:

- A collection of 6,000 plugin files publishes and has 5,000 entries and `truncated`; the tree answer is under 1.5 MB.
- The stored document of a remote is evicted and the screens still show the documentation; the route fetches it again.

### R-ANS-044 · Reindex, restore and the generator version
Priority: **P1** · Covers: trove-only

The `ansible-reindex` job (R-ANS-012) gains a documentation step: for every stored archive after the checkpoint it regenerates the rows of a version that has
none or whose `generator` is below `docgen.Version` (the archive is read anyway); for a remote repository it re-derives the rows from the stored `docs/` documents
(`RebuildRemoteDocs`) and queues the fetch for a version with neither. The enqueue of a restore (R-BAK-008) covers it. A step never changes stored bytes of an
archive, writes rows in one transaction per version, and leaves a version whose archive is unreadable with its old rows and a warning in the job log.

Acceptance criteria:

- Rows deleted by the test are rebuilt by the reindex of a local repository, equal to the first generation.
- A bumped `Version` rebuilds every row once; a second reindex rebuilds none.

### R-ANS-045 · Metrics and audit of the documentation
Priority: **P1** · Covers: trove-only

Metric families (R-ANS-030's rules, no label with a repository or collection name): `trove_ansible_docs_generated_total{outcome}` (`completed`, `truncated`, `refused`),
`trove_ansible_docs_fill_total{outcome}` (`available`, `none`, `upstream_error`, `too_large`, `invalid_document`) and `trove_ansible_docs_read_total{operation,outcome}` for the
three operations and the route (`ok`, `not_found`, `denied`). No audit event: generation and fill are derived data, and a read is not audited unless R-AUTHZ-034 decides
that a wiki read is. The import task of a publish carries the warnings (R-ANS-034).

Acceptance criteria: each series moves in the integration test of its path and no label value is a name of the user's data.

### R-ANS-046 · End-to-end rows per class
Priority: **P1** · Covers: trove-only

Real clients found three defects in F0 that integration missed, so the set `ansible` (kind, `make e2e`) gets a row per class, each with the real client for the
protocol and a browser journey for the screen. The source tree of the collection is checked in under `test/e2e/testdata/ansible_docs/` (a namespace with two modules
with full `DOCUMENTATION`, `EXAMPLES` and `RETURN`, a filter, a role with a README, a README and one `docs/` file); the collection is built with `ansible-galaxy
collection build` in the client pod.

| Row | What the real client does | What the browser journey checks |
|---|---|---|
| **local** | `ansible-galaxy collection publish` to a local repository; `ansible-galaxy collection install` of it; `GET docs-blob/` with the client's own token | On the package page: the Documentation tab with the tree (Modules 2, Plugins, Roles 1), the README first, a module page with its parameter table and example, the search narrowing the tree and keeping the counts, the Contents tab, the Import log tab with the publish |
| **remote** | `ansible-galaxy collection install` through a remote repository over the stub (the real client; the stub serves `docs-blob/`); `GET docs-blob/` of the remote | After the job ran: the same tabs from the upstream's document; the stub's README with a script tag shows without it |
| **virtual** | `ansible-galaxy collection install` through a virtual repository over the two; `GET docs-blob/` of the virtual repository | The same tabs on the virtual repository's package page, from the winner |
| **wiki** | A user who may read the artifacts and not the wiki installs the collection with the real client | The package page has no Documentation or Contents tab; `docs-blob/` answers as R-AUTHZ-034 decides. Built by the package that builds T-18's check, in this row |

The journeys run from the Go test like `TestE1_11Journeys`, the assertions name the R-ANS and R-UI IDs, and each row asserts the first tab's content, not only its
presence. A leg that is red is a finding, not a blocker of the release (CLAUDE.md, CI until the first stable release).

Acceptance criteria: the rows local, remote and virtual exist in `test/e2e/ansible_docs_test.go` and are registered in the set `ansible`; the `wiki` row is
added by whichever of T-19's UI builder and the builder of T-18's check lands second, and no row is committed skipped.

## Mirror discovery

What an Ansible remote in mode `mirror` discovers and stores (repositories-and-proxy.md R-REPO-050 to R-REPO-052; tests name R-REPO-051).
The engine, the bounds and the throttling are the core's; this is what the format contributes through its `MirrorLister`:

- **What the upstream offers.** A paged index of all collections (`namespace`, `name`, `highest_version`, `versions_url`) with
  `limit` at most 100 and `offset` (verified on the public Galaxy, 4,685 collections), a filter `namespace` and `deprecated`; per
  collection a paged version list; per version the document with `artifact.sha256`, `artifact.size` and `download_url`.
- **Methods** (`discover.Method`): `feed` not offered (no change feed was found); `listing` (what `auto` takes) walks the index by offset
  and returns the names the scope admits, in the index's order, in pages of the engine's limit with the cursor `{"offset": n}`; `names`
  takes literal include patterns as `<ns>.<name>`.
- **The plan of a name** ([Q-ANS-52](#q-ans-52--the-mirror-lists-by-offset-and-plans-from-the-version-lists-and-the-version-bounds-cut-before-documents-are-read)): the version documents of the collection through `Upstream.Document`, newest first by semver, each an item
  (`collections/<ns>/<name>/<ns>-<name>-<version>.tar.gz`) with the announced `artifact.size` and the pre-release flag; the digest
  reaches the engine through the gate of R-ANS-018 from the recorded row.
- **Moving content.** Every pass revalidates the index and the lists; new versions are planned; the archives of versions the upstream
  dropped stay (the mirror deletes nothing, R-REPO-051).
- A name filter on `deprecated: true` collections is not offered; a deprecated collection is mirrored like another.

## Test plan

| Requirements | Unit | Integration (`test/integration/…`) | e2e set |
|---|---|---|---|
| R-ANS-001 to R-ANS-005 (format, names, root, documents, download) | grammars, semver order, route table, `Classify`, renderers | `ansiblelocal`: the three read operations, download with ranges, formats API | `ansible` |
| R-ANS-006, R-ANS-007 (credentials, permissions, errors) | the stage's scheme rule, error renderers | `ansiblelocal`: `Token`, Basic, Bearer, anonymous, the action map `TestR_AUTHZ_007_AnsibleActionMap`, error shapes | `ansible` (token in `ansible.cfg`, netrc, a wrong token) |
| R-ANS-008 to R-ANS-013 (publish, tasks, inspection, storage, delete, digests) | multipart reader, manifest parser, crafted archives | `ansiblelocal`: both part orders, task polling, immutability, tombstones, reindex, the raw body, `/files/` refused | `ansible` (publish with the real client) |
| R-ANS-014, R-ANS-015, R-ANS-020 (roles) | role document rewrite, host rule | `ansibleremote`: translation, archive pull, dropped versions | `ansible` (role install with netrc) |
| R-ANS-016 to R-ANS-019 (remote, hub) | discovery parse, paging cut, `Rewrite`, `PrepareFill` | `ansibleremote`: counts, digest, changed digest, FillInspector, the stub's realm | `ansible` (remote, one request per document) |
| R-ANS-021 (mirror) | plan over fixtures | `ansibleremote` and `mirror`: a pass, a bound, a rate limit | `ansible` (mirror pass) |
| R-ANS-022 to R-ANS-024 (virtual) | merge over fixtures | `ansiblevirtual`: merge, conflict, claims, publish through the target, role ids | `ansible` (virtual) |
| R-ANS-025, R-ANS-026 (setup, screens) | templates render, kit check 8 | view API | `ansible` (rendered snippets), browser journey of the package screen |
| R-ANS-027 (import) | plan and held over the stub | `importer`: `ansible_test.go` | — |
| R-ANS-033 to R-ANS-046 (documentation) | `docgen` over crafted sources, the markup converter and its fuzz target, the tree and entry builders, `Rewrite` of the stored document, the wiki gate registry | `ansiblelocal`, `ansibleremote`, `ansiblevirtual`: generation, job, rows, tree, entry, file, search, `docs-blob/`, the denying gate | `ansible`: a built collection published, the Documentation tab on local, remote and virtual |
| R-ANS-028 to R-ANS-032 | crafted inputs (kit) | Valkey stopped, journal replay of deletions, metric series | `ansible` in `scatter`; the closing run |

## Catalog coverage

Every entry of the catalog page, with what is built, what stays and what is not offered.

| Entry | Decision | Where |
|---|---|---|
| PKG-ANSIBLE-001 Three classes | Built; the federated form and its signing-key selector are not (no federation, no signing) | R-ANS-001, Q-ANS-14 |
| PKG-ANSIBLE-002 Galaxy v3 API per repository | Built: the three read operations, the discovery document, the version-list paging, the absolute addresses | R-ANS-003, R-ANS-004, R-ANS-007 |
| PKG-ANSIBLE-003 Publish with `ansible-galaxy` | Built: multipart publish, the stored import task, publish through a virtual repository | R-ANS-008, R-ANS-009, R-ANS-024 |
| PKG-ANSIBLE-004 Deploy through the UI or plain HTTP | Built as the raw-body publish; `/files/` and the deploy dialog take nothing into a repository with a protocol of its own (R-STOR-007) | R-ANS-008, Q-ANS-34 |
| PKG-ANSIBLE-005 Install collections | Built in all three classes | R-ANS-005, R-ANS-013 |
| PKG-ANSIBLE-006 Metadata calculation and recalculation | Built: metadata is read at publish, the reindex rebuilds it | R-ANS-010, R-ANS-012 |
| PKG-ANSIBLE-007 Legacy roles through remote repositories | Built by translation for the public Galaxy's roles; Git and URL roles, role publish and role import are not offered | R-ANS-014, R-ANS-020, Q-ANS-6 |
| PKG-ANSIBLE-008 No browsing of uncached content | Built | R-ANS-017 |
| PKG-ANSIBLE-009 Remote repositories for Galaxy | Built, with `pullThrough` and `mirror` | R-ANS-016 to R-ANS-018, R-ANS-021 |
| PKG-ANSIBLE-010 Proxy the Red Hat Automation Hub | Specified; built last; the service was never reached | R-ANS-019, H-842 |
| PKG-ANSIBLE-011 Virtual repositories | Built | R-ANS-022, R-ANS-023 |
| PKG-ANSIBLE-012 `ansible.cfg` configuration | Built as setup snippets; the token is an environment variable, not an `ansible.cfg` line | R-ANS-025, R-ANS-006 |
| PKG-ANSIBLE-013 Credentials for role installation | Built as the netrc snippet and Basic on the role routes | R-ANS-015 |

## Changes to other pages

Each change is made by the work package that builds its mechanism, in the same change as its code
([ansible-work-packages.md](../../projektplan/ansible-work-packages.md)), because several blocks are held equal to the code by tests.

| Page | Change | Decision |
|---|---|---|
| [README.md](../README.md) | The row of this page (made with this page) and the protocol line for `/api/ansible/…` | Q-ANS-1 |
| [architecture.md](../architecture.md) | R-ARCH-008: `Route.TokenScheme`; R-ARCH-008/009: the additive remote interface of the hub exchange (decided by its package); the leaf packages of the format in the layer table | Q-ANS-4, Q-ANS-17, ADR-191 |
| [identity-and-authentication.md](../identity-and-authentication.md) | R-AUTH-001/R-AUTH-033: the scheme `Token` on a declaring route, the method label `galaxy_token` | Q-ANS-4 |
| [authorization.md](../authorization.md) | R-AUTHZ-007: the Ansible action table of R-ANS-007 and `TestR_AUTHZ_007_AnsibleActionMap` | R-ANS-007 |
| [repositories-and-proxy.md](../repositories-and-proxy.md) | R-REPO-003: the format's facts; R-REPO-016: the reindex of the format; R-REPO-024: the upstream test of an API root; R-REPO-050/052: Ansible can mirror, the default upstream | Q-ANS-11, Q-ANS-16 |
| [backup-and-restore.md](../backup-and-restore.md) | R-BAK-004: the tables; R-BAK-008: `ansible_upstream_versions`; R-BAK-024: `ansible.version.deleted` and `ansible.tombstone.cleared` | R-ANS-029 |
| [ha-and-helm.md](../ha-and-helm.md) | R-HA-054: the journal flags; the CI leg `e2e (ansible)` | R-ANS-029, R-ANS-032 |
| [api-and-ui.md](../api-and-ui.md) | The operations `listAnsibleImportTasks` and `clearAnsibleTombstone`; the `ansible-galaxy` setup templates and guide; the import tasks tab | R-ANS-025, R-ANS-026 |
| [operations-and-observability.md](../operations-and-observability.md) | The events and metrics of R-ANS-030; the job kinds `ansible-reindex`, `ansible-mirror` (the engine's) and the task `ansible-import-task-sweep` | R-ANS-030 |
| [endpoints.md](../endpoints.md) | R-EP-018: the format has no exposure adapter | Q-ANS-25 |
| [import.md](../import.md) | The Scope paragraph: Galaxy is imported (R-ANS-027), the format table | Q-ANS-21 |
| [vulnerability-scanning.md](../vulnerability-scanning.md) | The format's repositories are `not_covered` (no OSV ecosystem) | Q-ANS-15 |
| [api-and-ui.md](../api-and-ui.md) | R-API-029: a pointer to the three documentation operations of R-ANS-036 and the `metadata.docs` member; R-UI-007: the tabs of an Ansible version; R-UI-060 to R-UI-064; Q-UI-240 to Q-UI-247; Q-API-260 to Q-API-262 | R-ANS-036, Q-ANS-63 |
| [backup-and-restore.md](../backup-and-restore.md) | R-BAK-004: the four documentation tables are derived and not exported | R-ANS-029, R-ANS-044, Q-ANS-59 |
| [authorization.md](../authorization.md) | R-AUTHZ-034 (T-18): the list of Ansible surfaces that are wiki content (R-ANS-037) is the format's input to the wiki check | R-ANS-037 |
| [architecture.md](../architecture.md) | R-ARCH-008: the Host method `Markup` (R-ANS-039) is added to the contract's list of Host capabilities | Q-ANS-66 |
| [operations-and-observability.md](../operations-and-observability.md) | The job kind `ansible-docs-fill` and the three series of R-ANS-045 | R-ANS-042, R-ANS-045 |
| [repositories-and-proxy.md](../repositories-and-proxy.md) | R-REPO-043: a remote document that fills derived rows (`docs/<version>.json`) | R-ANS-035 |
| [decisions.md](../../decisions.md) | ADR-191 and the table rows of Q-ANS-1 to Q-ANS-28; ADR-196 and ADR-197 (written with this specification) | Q-ANS-66, Q-ANS-69 |

## Deferred

| Catalog ID or topic | Priority | Note |
|---|---|---|
| Deletion by the Galaxy API (`DELETE` of a collection or a version), namespaces (`v3/namespaces/`), collection deprecation, `move`/`copy` between repositories of the Galaxy API | P2 | `ansible-galaxy` does not call them; deletion is the management API's |
| Signing: trove-held keys, uploaded signatures, server-side signature verification of a remote's signatures | P2 | H-839; the protocol is ready (`signatures[]`) |
| Vulnerability evaluation of collections | P2 | H-841; needs an advisory source |
| Exposure rules and the Bastion for the format | P2 | Q-ANS-25 |
| Git and URL roles through a proxy; role publish | P2 | Not offered by design (R-ANS-014) |
| Automation Hub certified content beyond the token exchange (per-repository `content/<repo>/` paths, approval workflows) | P2 | Only the hub as an upstream is specified |
| Search over collections (`v3/plugin/…/search`, `ansible-galaxy collection list --remote`) | P2 | The client's `search` is for roles and uses `v1/search/roles`, not offered |
| The federated repository form | P2 | No federation exists |

## Security notes

- **A token never reaches a URL, a log or a snippet.** The scheme `Token` is read only on the routes that declare it, with the checks of
  a Bearer token; the setup uses an environment variable; the netrc line is the one place a token is written, by the user, with mode 600
  (R-ANS-006, R-ANS-015, R-ANS-025).
- **An archive is untrusted until it is inspected.** Publish and fill read it as a bounded stream, refuse links, traversal and
  inconsistent manifests, and never extract it to disk (R-ANS-010, R-ANS-031).
- **A published version cannot change, and a deleted one cannot come back with other bytes.** Immutability, tombstones and their
  journaled replay (R-ANS-008, R-ANS-012, R-ANS-029).
- **The digest is always checked.** A remote compares the SHA-256 its document announces with the bytes it fetched whatever the
  policy says, keeps the first digest it recorded for a version and writes an event when the upstream changes it (R-ANS-018).
- **Roles come from GitHub through trove only from hosts the operator named, and without a digest.** A role version whose
  archive is on an unlisted host is dropped from the list (R-ANS-020, H-840).
- **Dependency confusion.** A collection's `dependencies` are resolved by the client through the same server, so claims, pin rules and
  the shadowing check (R-ANS-022, R-ANS-024) keep an internal name away from a public member; a role named in `requirements.yml` by Git
  URL bypasses all of it (H-844).
- **The hub's offline token is a long-lived credential.** It is a stored secret of the repository (sealed with the key ring), sent only
  to the realm, and the access token is kept in a replica's cache for its lifetime and no longer (R-ANS-019).
- **Documentation is text from the publisher or the upstream, shown to other people.** It is scanned and decoded under bounds, never executed, never stored as HTML for a
  local entry, converted by an escaping-first converter and sanitized by the one policy of R-UI-008 before it reaches a browser; an upstream's HTML is sanitized on the way
  out. A screen never makes trove query an upstream (R-ANS-034 to R-ANS-039, H-893; [ansible-documentation.md](../../security/ansible-documentation.md)).
- **Documentation is wiki content.** Every surface that reads it asks one function, which calls the wiki check of R-AUTHZ-034 (R-ANS-037, H-894).
- **Task documents name no secret.** An import task is readable by whoever reads the repository and names the collection and the
  version (R-ANS-009).

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-ANS-1 · One format `ansible` at `/api/ansible/`, on the page `formats/ansible.md` with the prefix `R-ANS`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Format ID `ansible`, display name "Ansible Galaxy" (chosen) | `/api/ansible/<projectKey>/<repoKey>/`; collections and roles are content of one format | The catalog's name; the client is `ansible-galaxy` and the repository URL is its "server"; one format because one client and one server list serve both | The display name says Galaxy, which is also the public service's name |
| (b) `galaxy` | The protocol's name | Matches the client's vocabulary | A format named after one service; the catalog page and the roadmap say Ansible |
| (c) Two formats, `ansible-collection` and `ansible-role` | Separate IDs | Roles and collections are different artifacts | One server URL serves both to one `server_list`; two formats would force two URLs and break the client's `RoleDistributionServer` choice |

**Choice:** (a).

**Rationale:** the client's unit is a server, and a server answers for both artifact kinds. The difference between the kinds is expressed
inside the format: roles exist only on remote and virtual repositories (Q-ANS-6). The development agent would revisit it if roles ever
became publishable (then they would be their own package kind inside the same format).

**Applied in the spec:** R-ANS-001; spec README (row and protocol line).

### Q-ANS-2 · What this work builds and what stays

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The client surface of the catalog's P1 entries; no `DELETE`, namespaces, search, signing, exposure, Bastion (chosen) | Collections local/remote/virtual, roles by translation, mirror, hub as an upstream, import, setup, screens | Everything `ansible-galaxy` calls and the owner's replacement needs | Operators who delete through the Galaxy API use the management API |
| (b) Also the galaxy_ng management routes | Namespaces, delete, move/copy | Closer to the product the owner replaces | `ansible-galaxy` calls none of them; the shapes were not verified |

**Choice:** (a).

**Rationale:** the unit F0 replaces a system for its clients' sake; what a client does not call is added when a user asks for it.

**Applied in the spec:** [Deferred](#deferred), [Catalog coverage](#catalog-coverage).

### Q-ANS-3 · The protocol routes are below the repository URL itself; the root answers the discovery document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The repository URL is the API root: `<repo URL>` answers `available_versions`, `v3/…` and `v1/…` are below it (chosen) | Server `url=<repo URL>`; the client's first request succeeds and `api_server` stays | Shortest path layout (`…/v3/collections/…`); one request fewer; verified against `g_connect` of 2.17.14 and 2.21.6 | A user who writes `<repo URL>api/` meets 404 |
| (b) The routes are below `<repo URL>api/` and the root is 404 | The client's fallback appends `/api/` | Looks like a Galaxy's own `/api/v3/…` | Every client makes a failing first request; `/api/ansible/web/x/api/v3/…` repeats `api` |
| (c) Both | Two route sets | Tolerates either configuration | Ambiguous routes, twice the examples, an unclear canonical address in documents |

**Choice:** (a).

**Rationale:** the client's own rule makes the configured URL the API root when it answers the discovery document, in every version read.
The development agent would revisit it if a client in the matrix is found not to accept a root document served below a path with a
trailing component (the e2e set proves 2.17.14 and 2.21.6).

**Applied in the spec:** R-ANS-003, R-ANS-004; the table of canonical addresses. Tests: `TestR_ANS_003_...` (`ansiblelocal`).

### Q-ANS-4 · The authorization scheme `Token` is accepted on the routes that declare it

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A route flag `Route.TokenScheme`, the stage reads `Authorization: Token <t>` as the token there (chosen) | Wired like `APIKey` (the dispatcher's match answers the stage); a new method label `galaxy_token` | The client sends exactly this for `token=` and `--token` (verified); no effect on any other route; the pattern exists | A contract addition (additive, `ContractVersion` stays, R-ARCH-025) |
| (b) Accept `Token` everywhere as Bearer | One more scheme in `credentialHeader` | Smallest change | Widens R-AUTH-001 for every endpoint, including those that never see a Galaxy client |
| (c) Tell users to use Basic (`username`/`password`) only | No auth change | No contract change | The catalog and the client's `--token` and `token=` forms would not work; every setup snippet would carry a password line |

**Choice:** (a). Details: `Route.TokenScheme bool`; `auth.EndpointRules.TokenSchemeRoute func(*http.Request) bool` wired by the composition
root from the dispatcher's match; `auth.MethodGalaxyToken` with the label `galaxy_token` (the request log and the metric vocabulary of
R-OPS-015); in `credentialHeader`, the scheme `Token` is returned as a Bearer-checked secret only when the route declares it; the session
cookie is not accepted on such a route when an `Authorization` header is present.

**Rationale:** a scheme that exists for one client family is scoped to that family's routes. The development agent would revisit it if
a second format needs a non-standard scheme (then the flag would name the scheme).

**Applied in the spec:** R-ANS-006; Changes to other pages (architecture.md, identity-and-authentication.md).

### Q-ANS-5 · Collection names are lower case, versions are semantic versions, the archive has one canonical path

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `[a-z0-9_]+` parts, no leading `_`, at least one letter, at most 64 characters; fold upper case in requests, refuse it on publish; semver 2.0.0; path `collections/<ns>/<name>/<ns>-<name>-<version>.tar.gz` (chosen) | One name per collection; path patterns per collection | Matches what the public Galaxy holds (names are lower case); no two spellings of one collection (dependency confusion by case) | Galaxy's own publish grammar was not verified; the rule is trove's and may refuse a name Galaxy accepts |
| (b) The client's grammar `[a-zA-Z0-9_]` | Accept what the client accepts | Never refuses what the client builds | Two spellings of one name would need the folding anyway |
| (c) A flat path `<file>` | Fewer directories | Equals the download file name | Path patterns and listings cannot be per collection |

**Choice:** (a).

**Rationale:** names are identities that grants, claims and shadowing checks compare; folding in one place (`Names.Normalize`) makes that
exact. The development agent would revisit it when a real source holds a name outside the grammar (the import reports `invalid_name`).

**Applied in the spec:** R-ANS-002; R-ANS-010 (identity check).

### Q-ANS-6 · Roles are served by translation from a remote only; collections are the stored artifact

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Roles only through remote and virtual repositories, by translation of the `v1` role API and pull-through of the archive; no role publish, no Git roles (chosen) | The catalog's rule, made concrete from the verified role flow | Roles on the public Galaxy are GitHub archives, so nothing is "stored" by Galaxy that trove could publish; the owner's clients keep working for public roles | No digest to verify (H-840); GitHub is a dependency of the role path |
| (b) No roles at all | Collections only | Smallest, strongest | Fails the catalog entry; a `requirements.yml` with a public role would not resolve through trove |
| (c) Local role publish as a tarball | A new stored role kind with a `v1` API of trove's own | Full control | Roles have no manifest to validate or identify; reproduces a deprecated packaging; the catalog says roles cannot be published |
| (d) A Git proxy for `scm: git` roles | The Git protocol | Covers the third role source | trove has no Git format; a large new surface |

**Choice:** (a).

**Rationale:** it serves what a client does with the public Galaxy and stores nothing it cannot account for. The development agent would
revisit it if the owner's instance holds roles that matter: the import would then wrap them into collections or a role format would be
specified.

**Applied in the spec:** R-ANS-003, R-ANS-014, R-ANS-015, R-ANS-020; [H-840](#h-840--a-role-archive-has-no-announced-digest-and-the-source-is-not-byte-stable), [H-844](#h-844--git-and-url-roles-bypass-trove).

### Q-ANS-7 · Role archives are pulled from the hosts of the role's versions and cached without a digest

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The archive URL a versions document names is recorded in the URL map of that document's own role; hosts must be in `remote.extraHosts` (default the GitHub hosts); first fetch is stored (chosen) | The rule of R-REPO-043 reused | No client-chosen host, no template; a client cannot fetch an arbitrary repository through the remote | The versions document must be fetched before the archive (a client always does) |
| (b) Build `https://github.com/<user>/<repo>/archive/<ref>.tar.gz` from the request path | A fixed template | No dependency on a prior document | Turns the remote into an open GitHub archive proxy |
| (c) Redirect the client to the upstream URL | trove stores nothing | No bytes through trove | The client would download from GitHub directly, bypassing egress control and the cache |

**Choice:** (a).

**Rationale:** the same rule that protects every other format's artifact URLs. The development agent would revisit it if GitHub archive
bytes prove unstable for a tag (then a cached copy diverges from the source; H-840).

**Applied in the spec:** R-ANS-020.

### Q-ANS-8 · A publish is processed in the request; the import task is a stored record

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Everything happens in the request; a task row is final when created; a content failure is a failed task, an admission failure an HTTP error (chosen) | `202` with a completed or failed task | The client's polling code works unchanged and finishes at once; no queue to drain, no state in a replica's memory | A 1 GiB archive is inspected inside the request (H-843) |
| (b) A background job inspects and commits; the task is `running` meanwhile | Galaxy's own behaviour | Request returns fast; progress | A job kind, a pending-state item model, and the idempotency of retries to design; more failure modes for a registry whose users publish rarely |
| (c) Return the result directly and no task | `201` with no `task` | Simplest | The client requires `task` in the answer (`resp['task']`, verified) |

**Choice:** (a).

**Rationale:** the client only needs the task URL and its `finished_at`. Inspection is bounded by streaming reads. The development agent
would revisit it when archives routinely take longer than the request timeout.

**Applied in the spec:** R-ANS-008, R-ANS-009; [H-843](#h-843--a-publish-inspects-the-archive-inside-the-request).

### Q-ANS-9 · A published version is immutable; a deleted one leaves a tombstone

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Identical retry succeeds, other bytes are 409, a deleted number is tombstoned until cleared (chosen) | The npm rule (R-NPM-008) | A client's cache and a lockfile never meet other bytes under one version; a retry is safe | An operator who deleted by mistake clears a tombstone |
| (b) Overwrite allowed with `overwrite` | Mutable versions | Convenient for development | Breaks the digest contract of every client that saw the version |
| (c) No tombstone: a deleted number may be reused | Simpler | No extra table | A restored cache elsewhere holds other bytes under the number |

**Choice:** (a).

**Rationale:** the digest in the version document is the integrity contract; reuse breaks it. Revisit if operators routinely need
number reuse (then a policy flag).

**Applied in the spec:** R-ANS-008, R-ANS-012, R-ANS-029.

### Q-ANS-10 · The version document leaves out the plugin listing

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Drop `metadata.contents`, `docs_blob`, `manifest`, `files` when serving (chosen) | Local: never stored; remote: stored as received, dropped by `Rewrite` | Every install reads a document; community.general's is 848 KB; no client in the matrix reads these fields (verified) | A tool that browses a collection's plugins through the API finds nothing |
| (b) Pass everything | Full fidelity | Nothing is lost | 0.85 MB for every version document, for every client, every install of a collection with many plugins |

**Choice:** (a).

**Rationale:** a registry serves what its clients read. Revisit when a client in the matrix reads `contents`.

**Correction of 2026-11-11 ([Q-ANS-62](#q-ans-62--the-documentation-is-served-at-docs-blob-and-the-version-document-keeps-leaving-the-plugin-listing-out)):** `docs_blob` is not a member of `metadata`; the Galaxy serves it as the sub-resource `docs-blob/` of the version, and `manifest` and `files` sit beside `metadata`, not in it. The choice stands; the documentation is served at its own route (R-ANS-035).

**Applied in the spec:** R-ANS-004, R-ANS-017.

### Q-ANS-11 · The upstream URL is an API root, and the adapter reads it as a discovery document

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `remote.url` must answer `available_versions`; the adapter is a `Discoverer` that stores it as `api-root.json` (chosen) | `https://galaxy.ansible.com/api/` | Works with any Galaxy-compatible server by its own advertised prefixes; the redirects of the public Galaxy are followed by the engine; one stored document | A user who gives `https://galaxy.ansible.com/` is told to append `/api/` |
| (b) Implement the client's fallback (try the root, then `/api/`) | Same URLs as `ansible.cfg` | Users copy their existing `url=` | Two requests to discover, an ambiguous stored URL |
| (c) Hard-code `/api/v3/plugin/ansible/content/published/collections/index/…` | The public Galaxy's real paths | No redirect hop | Breaks every other Galaxy-compatible server |

**Choice:** (a).

**Rationale:** the discovery document is the protocol's own mechanism. Revisit if the Hub's root does not answer it directly (H-842).

**Applied in the spec:** R-ANS-001, R-ANS-016.

### Q-ANS-12 · A remote stores the version list in pages of 100 and cuts the client's page when it serves

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Stored unit is the upstream page (`limit=100&offset=n`), variant `offset=n`; the handler assembles the client's range from stored pages (chosen) | Reads like NuGet's registration pages (Q-NUGET-82) | Any client `limit` works; the virtual merge reads the same pages; each page is a normal stored document | A request spanning pages reads several documents |
| (b) Store the client's request as asked (`limit=L&offset=O`) | One document per request | Simple | An unbounded number of variants chosen by clients |
| (c) Assemble the whole list into one stored document | One document per collection | Easy to merge | `PrepareFill` sees one fetched document and cannot fetch further pages (Q-NUGET-82 (b)) |

**Choice:** (a).

**Rationale:** the bounded variant space and one reading path for remote and virtual. Revisit if an upstream's page size cannot be set to 100.

**Applied in the spec:** R-ANS-017, R-ANS-022.

### Q-ANS-13 · The announced SHA-256 is always verified; the first recorded digest stays

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ChecksumAlwaysVerified`; the digest of the version document is recorded in `ansible_upstream_versions` at fill and read by `BeforeArtifact`; a changed digest keeps the first (chosen) | The npm model (R-NPM-025) | The digest is part of the protocol (the client checks it too); a move of an upstream's bytes is loud | A fourth table |
| (b) The generic policy (`verify-if-available`, optional `require`) | Default engine behaviour | No format table | `ignore` and `pass-through` would be allowed for a format whose clients verify anyway; the digest would not be available to a virtual repository or the mirror plan |

**Choice:** (a).

**Rationale:** the digest decides the artifact and is needed in four places (fetch, mirror plan, virtual cached set, immutability event).
Revisit if the engine gains a per-path announced-digest store.

**Applied in the spec:** R-ANS-001, R-ANS-011, R-ANS-013, R-ANS-018.

### Q-ANS-14 · Signatures pass through; trove signs nothing and verifies nothing

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `signatures[]` of a remote version pass through unchanged; a local version has none; no verification, no keys (chosen) | The protocol is ready, trove adds nothing | The client verifies against its own keyring as against the upstream; no key store or trust decision in trove | A remote of an unsigned or tampered signature is not caught by trove (H-839) |
| (b) Verify a remote's signatures against configured keys on fill (as R-DEB-020 does for APT) | `internal/openpgp` exists | Catches a bad signature once, for every client | A key configuration and a trust policy for a feature the owner's clients may not use; the signature covers `MANIFEST.json` inside the archive, so verifying means reading the archive on fill |
| (c) trove signs local collections | A signing service as galaxy_ng has | Signed internal content | A key lifecycle per repository (the Debian format's cost) for no stated demand |

**Choice:** (a).

**Rationale:** nothing in the catalog or the owner's use asks for more; the interface is open for (b). Revisit when a client of the owner
sets `--keyring`.

**Applied in the spec:** R-ANS-013; H-839; Deferred.

### Q-ANS-15 · No vulnerability adapter; the state is `not_covered`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No `VulnerabilityAdapter`; the repositories are `not_covered` (chosen) | The state the pipeline already has for a format without an adapter | OSV has no Ansible ecosystem (verified 2026-11-10); honest | No findings |
| (b) Map a collection to the PyPI ecosystem through its Python requirements | Read `requirements.txt` in the archive | Some signal | A collection is not a Python package; the mapping would report findings for something it is not, and miss everything else |
| (c) Wait for an advisory source of the Ansible community | Same as (a) now | | |

**Choice:** (a).

**Rationale:** a state that says "not evaluated" is true; a mapped one is not. Revisit if OSV adds an ecosystem.

**Applied in the spec:** R-ANS-013; H-841.

### Q-ANS-16 · A mirror of the collection index is offered; it keeps what the patterns select

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `MirrorLister` over the collection index (`listing`) and literal names (`names`), selected by patterns; roles and the hub's index excluded; no default selection (chosen) | The engine's mirror mode for Galaxy | The owner required the option for Docker and APT; the public Galaxy lists all 4,685 collections by offset; an offline copy of what a team uses | A full mirror is a big configuration choice, its size unmeasured (H-846) |
| (b) No mirror | Pull-through only | Less to build | An air-gapped installation cannot be prepared |
| (c) Mirror by default of the whole public Galaxy | Everything | Nothing missing | Unbounded storage; the engine refuses an unbounded include as a default anyway |

**Choice:** (a).

**Rationale:** the mechanism exists (R-REPO-050 to R-REPO-059) and the format's part is small. Revisit when the size of the public Galaxy is
measured.

**Applied in the spec:** R-ANS-021, Mirror discovery; H-846.

### Q-ANS-17 · The hub token exchange is a separate optional interface, built after the core class

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A new additive optional interface of the remote adapter that gets the stored credential for one exchange; built as the last package of the remote class (chosen) | The refresh-token grant with the stub's realm | Keeps the secret out of the existing handshake; the core class does not wait for it | A contract addition, and a service nobody could reach |
| (b) Reuse `Authenticator.TokenRequest` | The OCI handshake | No new interface | It hands the adapter a challenge, not the credential, and the form needs the offline token in its body |
| (c) Do not offer the hub | Catalog entry unmet | Nothing unverifiable | The catalog lists it P1 |

**Choice:** (a).

**Rationale:** the existing handshake cannot carry the secret into a request body. The development agent would revisit the shape when a
Red Hat account allows a run against the service.

**Applied in the spec:** R-ANS-019; H-842; ADR-191.

### Q-ANS-18 · The virtual merge unions the versions and lets the first tier win per version

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Merge lists, first match for documents and archives, local before remote, a counter for differing digests (chosen) | The core's tiers (R-REPO-019) | A version always comes from one member with its own document and archive; local content never loses to a remote | Differing bytes under one version are only counted |
| (b) Refuse a version that two members hold with different digests | Fail closed | Catches a confusion | A virtual repository would break on a legitimate mirror of an internal name |
| (c) The highest-priority member serves the whole name | Per-name first match | Simple | Local 1.0.0 and remote 0.9.0 would not both be installable |

**Choice:** (a).

**Rationale:** claims and pin rules, not the merge, are the confusion defence (R-REPO-023). Revisit if the counter fires in practice.

**Applied in the spec:** R-ANS-022.

### Q-ANS-19 · Role ids in a virtual repository name their member

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `id` becomes `<member key>-<upstream id>`, decoded by the last `-` (chosen) | No stored mapping | Stateless, valid on any replica, validated against the current members | Not numeric; verified only by reading: the client uses it as a path segment (`_urljoin`) and never as a number |
| (b) A table of trove-assigned numeric ids | Numeric ids | Looks like Galaxy's | A table and a sweep for a rarely used feature |
| (c) Roles not through a virtual repository | Simpler | | The recommended client target would not install roles |

**Choice:** (a). Revisit if a client rejects a non-numeric id (the e2e set proves it).

**Applied in the spec:** R-ANS-022.

### Q-ANS-20 · The import task of a publish through a virtual repository is read through the virtual repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The task URL is on the repository the client published to; the read looks the id up in the current deploy target (chosen) | One address for the client | The client polls the address it was given | A changed deploy target makes the task 404 |
| (b) The task URL always names the local target | Fixed | Always resolves | The client must reach the member path; the answer would reveal the member |

**Choice:** (a). **Rationale:** members are not named in answers (R-ANS-023). Revisit if targets change often.

**Applied in the spec:** R-ANS-009, R-ANS-024.

### Q-ANS-21 · The Galaxy import is one more `Format` of the importer, collections only

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `internal/importer/ansible.go` implements `Format`; the road is the target's own publish protocol (chosen) | R-IMP-004 applies unchanged | Permissions, digests, immutability, audit, the quota apply to imported versions; no second door | Needs a running target |
| (b) A direct database copy | Fast | | Bypasses every rule |
| (c) Import roles by wrapping them | Converts roles to collections | Would keep roles | Invents package identities and versions the source never had |

**Choice:** (a).

**Rationale:** the importer's seam exists for this. The first run against the owner's instance (2026-11-11, R-ANS-027) found no difference; revisit when a later source does.

**Applied in the spec:** R-ANS-027; import.md Scope.

### Q-ANS-22 · The setup writes the token as an environment variable, not into `ansible.cfg`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ansible.cfg` without the token, plus `ANSIBLE_GALAXY_SERVER_TROVE_TOKEN=${TROVE_TOKEN}` (chosen) | Verified variable name | The setup never holds a credential (R-API-030); `ansible.cfg` does not expand variables | Two snippets for one configuration |
| (b) `token=${TROVE_TOKEN}` in the file | One snippet | Matches the catalog's example | The literal would be sent as the token |
| (c) The API fills the token into the file | One snippet | | Violates R-API-030 |

**Choice:** (a). **Rationale:** the only form that works. Revisit if ansible-core expands environment variables in ini values.

**Applied in the spec:** R-ANS-025.

### Q-ANS-23 · The test strategy: a counting stub, integration areas per class, and an e2e set `ansible` with two client images

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `test/ansiblestub`, areas `ansiblelocal`/`ansibleremote`/`ansiblevirtual`, an e2e set `ansible` with ansible-core 2.17.14 and 2.21.6 (chosen) | The shape of the Debian and OCI tests | A stub that counts requests proves the one-request-per-document rule; two images bracket the matrix | A stub that must imitate the public Galaxy's redirects and shapes |
| (b) Tests against the public Galaxy | Real | | Not repeatable, not countable |
| (c) One client image | Cheaper | | The 2.17/2.21 `api.py` differences would go unproved |

**Choice:** (a). **Rationale:** repeatable and countable. Revisit when ansible-core 2.22 is released.

**Applied in the spec:** R-ANS-032.

### Q-ANS-24 · The format has four tables and a configuration section

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ansible_collection_versions`, `ansible_tombstones`, `ansible_import_tasks`, `ansible_upstream_versions` and `ansible:` keys `max_archive_bytes`, `role_archive_max_bytes`, `import_task_keep` (chosen) | R-ANS-011, R-ANS-031 | Each table has one reason and one owner | Four tables |
| (b) Everything in generic package properties | No tables | | Dependencies and digests are queried per request; the tombstone and task have no property home |

**Choice:** (a). **Rationale:** each is read on a hot path or must survive item removal. Revisit if the engine gains per-path facts.

**Applied in the spec:** R-ANS-011, R-ANS-031.

### Q-ANS-25 · No exposure rules, no Bastion and no vulnerability matching for the format in this phase

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No exposure adapter; not served on endpoints with exposure rules or through the Bastion (chosen) | As the Debian format | Nothing in F0 asks for them | They wait |
| (b) Build them | Complete | Uniform with npm | A design of its own per feature |

**Choice:** (a). **Rationale:** same as Q-DEB-26. Revisit when an endpoint needs Galaxy content.

**Applied in the spec:** Deferred; endpoints.md.

### Q-ANS-26 · The reindex of a local repository is a job that reads the two documents of each archive

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Job `ansible-reindex`, ranged reads, checkpoint per name (chosen) | The NuGet reindex pattern | Never spools a package | A job kind |
| (b) 405 as npm and NuGet answer today | Nothing to build | | The catalog lists the recalculation as P1 |

**Choice:** (a). **Rationale:** the rows are derived data; a way to rebuild them is a safety net for imports and restores.

**Applied in the spec:** R-ANS-012.

### Q-ANS-27 · Errors are rendered in the Galaxy shape by route

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `errors[]` below `v3/` and the root, `default` below `v1/` (chosen) | The shapes `GalaxyError` reads | The client prints trove's `detail` | Two shapes |
| (b) trove's problem-details everywhere | One shape | | The client would print only the HTTP reason |

**Choice:** (a). **Rationale:** the client's own parser decides. Revisit if the client changes its parser.

**Applied in the spec:** R-ANS-007.

### Q-ANS-28 · A collection archive is validated for consistency, not only for identity

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Identity, `FILES.json` digest against the manifest, every listed file's digest, no unlisted member (chosen) | R-ANS-010 | `collection verify` has something true to verify; an archive cannot hide content from its file list | A pass over the archive's bytes at publish |
| (b) Identity only | Cheap | | Inconsistent archives enter the registry |

**Choice:** (a). **Rationale:** one streaming pass is paid once at publish. Revisit if a real archive from `ansible-galaxy collection build`
is refused (the e2e images build one).

**Applied in the spec:** R-ANS-010.

### Q-ANS-29 · The Galaxy error shape renders the registry code without its prefix

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Registry codes `ansible_*`; the Galaxy shape strips the prefix and names the generic kinds its own way (chosen) | `ansible_version_exists` is `version_exists`; `unauthenticated` is `not_authenticated`, `forbidden` is `permission_denied`, `malformed_request` is `invalid_request`, a 502 or 503 of an upstream is `upstream_unavailable` | The codes of this page are what a client sees; the problem catalog (`/api/v1/problems/<code>`) keeps unique, prefixed codes | A mapping function (`galaxyCode`) |
| (b) Register `version_exists`, `invalid_name`, … unprefixed | One name everywhere | No mapping | The registry's codes are global and prefixed per format (R-ARCH-025); a second format could not use the names |

**Choice:** (a). **Rationale:** the registry cannot hold the unprefixed names without taking them from every format, and the client prints
only `detail` and `title`. A local repository has no `v1` API (R-ANS-014): its route table lacks the `v1` routes, so a `v1` request
answers 404 in the `v1` shape with the sentence that says why, whether the dispatcher found no route or refused the class. Revisit if a
client reads `code`.

**Applied in the spec:** R-ANS-007 (the codes are the registry's without the prefix). Code: `internal/formats/ansible/errors.go`,
`internal/errs/codes.go`.

### Q-ANS-30 · A collection archive is read in one pass under the core's bounds

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One streaming pass with the core's tar reader; the format keeps the core's bounds and reads the three documents with bounds of their own (chosen) | Every member goes through a digest (`TarReader.Stream`, which counts against the inflated cap and not against the bytes read into memory); `MANIFEST.json` (1 MiB), `FILES.json` (16 MiB) and `meta/runtime.yml` (64 KiB) are kept; entries at most 10,000 and inflated bytes at most 2 GiB | The reader of R-ARCH-028 is used as it is; no random access, so a fill can hand it the body it stores; the consistency check of Q-ANS-28 needs one pass | A collection of more than 10,000 files or 2 GiB expanded is refused ([H-851](#h-851--a-collection-archive-beyond-the-cores-reader-bounds-is-refused)) |
| (b) Raise the core's limits for this format | 100,000 entries, 4 GiB | Room for the largest collections | A format may lower the core's defaults and not raise them (R-ARCH-028): the rule would change for every format |
| (c) A tar reader of the format's own | Own bounds | Any bound | A second reader of untrusted archives to keep correct |

**Choice:** (a). **Rationale:** the collections of the public Galaxy are far below both bounds (the largest version document describes
2.6 MB of archive); the one addition to the core is `TarReader.Stream`. `meta/runtime.yml` is read through the YAML module already in
`go.mod`, decoded into a node tree and walked, never into Go values: an anchor, an alias, an explicit tag, a merge key, more than 1,000
nodes, a top level that is not a mapping or a `requires_ansible` that is not a short plain string leave the field empty with a warning in
the task, and a file above 64 KiB is a bound crossed. `ReadArchive` takes an `io.Reader` and not the `io.ReaderAt` of the plan, because
the crafted inputs of the kit are streams. Revisit if a real collection is refused by the entry or byte bound.

**Applied in the spec:** R-ANS-010 (the runtime file), R-ANS-031 (the table).

### Q-ANS-31 · The tables carry no revision column; the ETag follows the generic package row

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No revision column on the format's tables (chosen) | The documents are rendered from the version rows; the generic package row's `rev_seq` is raised by every writer (R-ARCH-009), and the validator of a document follows its bytes (Q-ANS-35) | `rev_seq` is already restored with a gap by the generic restore; one number to keep | A change of a version row that raises no package row would not change the ETag: there is none (every writer raises it) |
| (b) A revision column on `ansible_collection_versions` registered with `meta.RegisterRevisionColumn` | A second counter | | The reindex and the commit would have to raise two |

**Choice:** (a). **Rationale:** the reindex raises `rev_seq` of the packages it changes (R-ANS-012), which is all the ETag needs. Revisit if
a document is rendered from a row that no package change touches.

**Applied in the spec:** R-ANS-011 (the columns are the contract; no revision column).

### Q-ANS-32 · The reindex is an optional interface of the data port, not a hook of the contract

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The data port implements `api/v1.Reindexer`, which the endpoint already finds by format ID; the port's step is `RebuildRows` (chosen) | `POST …/reindex` enqueues `ansible-reindex` (queue `index`, coalesce, key `index:<repository id>`) exactly as Debian's does | No change to the format contract; ADR-191's consequences stay as written | The port has two methods about one job (the enqueue, the step) |
| (b) A new optional interface `formats.Reindexer` | A hook the core calls | The core knows the format's job | A contract addition for a job the endpoint already reaches |

**Choice:** (a). **Rationale:** the endpoint (`internal/api/v1/reindex.go`) takes any data port that implements `Reindex(ctx, tx,
repoID, by)`; the plan's `Store.Reindex(ctx, repo, reader, checkpoint)` would have collided with that name, so the step is `RebuildRows`.
A remote repository answers 405 `not_applicable`; a virtual repository drops its merged-document cache (R-REPO-016). Revisit if a second
format needs the same shape.

**Applied in the spec:** R-ANS-012.

### Q-ANS-33 · The data port speaks repository ids and the values of a rows leaf

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The port's methods take the repository id (`int64`) and the values of `internal/formats/ansible/rows`, a leaf (chosen) | As Debian's | The data port is a domain package and may not import the format (edge); `formats.RepoRef` is the format's | The plan's sketch used `formats.RepoRef` |
| (b) `formats.RepoRef` in the port | As the plan wrote it | | A layer breach (R-ARCH-002): the port could not implement the interface |

**Choice:** (a). **Rationale:** the archtest layer rule. `Task` and `CollectionSummary` live in `rows` and are aliased by `galaxydoc`, whose
signatures the plan fixed; `RecordAnnounced` returns the rows whose offered digest differs from the one that stays, so the remote class
can write its event (R-ANS-018). Revisit never: the rule is architectural.

**Applied in the spec:** none (a code-level contract of the plan, section 2.2).

### Q-ANS-34 · A collection is published through the Galaxy route only; a raw body is the scripted form

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The publish route takes the multipart form and, besides it, the archive as the raw body; `/files/` takes nothing (chosen) | `curl --data-binary` publishes with every check and a task; `PUT /files/…` answers the 409 of R-STOR-007 | The rule of the core stands (R-STOR-007, Q-ARCH-223): a format with a protocol of its own builds its documents from what that protocol receives; the format's `Inspect` and the checks that need a request (shadowing, ownership before the body) are reached by one road | The deploy dialog of the UI has no road into a Galaxy repository |
| (b) `/files/` deploys archives | The plan's reading of R-ANS-008 | The deploy dialog works | The generic upload calls no `Inspect` of the format and no shadowing check: an archive would be stored with no row, no identity check and no task; the core refuses it for every other format |

**Choice:** (a). **Rationale:** (b) needs a change of the core that every format would inherit. The raw body gives scripts what `curl -T` was
for. The screens (R-ANS-026) show the publish command instead of a dialog. Revisit if the core lets a format inspect a raw deploy.

**Applied in the spec:** R-ANS-001, R-ANS-005, R-ANS-007, R-ANS-008, R-ANS-026, R-ANS-031 and the catalog row PKG-ANSIBLE-004.

### Q-ANS-35 · A document is computed per request and its validator is a digest of its bytes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No rendition cache; the weak `ETag` is a digest of the rendered bytes (chosen) | Every request reads the rows of the collection and renders; `If-None-Match` is answered after the render | A validator that cannot be stale (the bytes hold the addresses, the rows and the caller's hidden versions); no cache key to keep in step | A request renders even when the client holds the document |
| (b) The rendition cache of the core, keyed by `rev_seq`, the serving address, the endpoint and the hidden-version fingerprint | As NuGet's documents | No render on a hit | A key with five inputs to keep equal to the validator by hand (Q-ARCH-261), for documents of a few KB |

**Choice:** (a). **Rationale:** a document is rendered from a handful of rows (a version list is one indexed scan), and the client reads each
once per command. When the caller's grants hide versions of a collection (`VersionsByPath`), the page is cut from at most 50,000 versions
read and filtered ([H-854](#h-854--a-caller-whose-grants-hide-versions-makes-the-list-read-all-versions-of-the-collection)). Revisit if a list of a very large collection shows up in a profile.

**Applied in the spec:** R-ANS-004.

### Q-ANS-36 · A role is a package of the core's name machinery, named role/ and its id

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `Classify` gives the paths of a role's version pages and archive the package `role/<id>`, and the format's name rule takes that name (chosen) | The URL map, the include and exclude patterns, the grants and the shadowing check treat a role like a package | The URL map of the archive of a role needs a package the document and the archive share (R-REPO-043); one mechanism for roles and collections | A role is named `role/<id>`, which no operator knows, in a pattern |
| (b) A classification without a package for roles | A role is decided by paths alone | Nothing to name | The URL map records only paths of the document's own package, so no role archive could be fetched (`OwnPath`) |

**Choice:** (a). **Rationale:** (b) cannot work: the engine records a URL-map row only when the document and the archive classify under the
same non-empty package. The name has no slash-free form, so it cannot collide with a collection (`<ns>.<name>`). The lookup document names no
role and has no package. Include and exclude patterns of a remote match `role/<id>` (the format's pattern subject is the name), so an
operator who limits a remote to `acme.*` also hides every role ([H-856](../../security/ansible-remote.md#h-856--the-patterns-of-a-remote-see-a-role-by-its-id-and-not-by-owner-and-name)). Revisit when
an operator needs to select roles by owner and name: the lookup then has to be stored per role.

**Applied in the spec:** R-ANS-020.

### Q-ANS-37 · A fetched archive is held to the identity of its path; `requires_ansible` is not compared

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The fill hook compares the archive's namespace, name and version with the path and nothing else (chosen) | `identity_mismatch` is 502; the digest check of R-ANS-018 ties the bytes to the version document | The hook has no data port and no transaction (`FillInspector`), so it cannot read the recorded row | The `requires_ansible` of the manifest and of the document are not compared |
| (b) Also compare `requires_ansible` with the recorded row | The plan's reading of R-ANS-018 | A fully consistent upstream | Needs a data-port read inside the hook, which the contract does not give |

**Choice:** (a). **Rationale:** an archive whose bytes match the digest its document announced is the archive the upstream published; a
`requires_ansible` that differs between its two statements is the upstream's inconsistency and `ansible-galaxy` reads the document's. Revisit if
the contract hands the fill hook a read port.

**Applied in the spec:** R-ANS-018 (the last bullet of "Inspect on fill").

### Q-ANS-38 · The first digest of a version stays only while its archive is cached

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `RecordAnnounced` keeps the recorded digest while a live item exists at the archive's path, and follows the document when there is none (chosen) | Deleting the cached item (or a restore without caches) resolves a republished version | The operator's remedy of R-ANS-018 needs no second step; the rule is one statement | A version that was evicted and republished upstream in between is believed at its new digest |
| (b) The recorded digest stays forever | Strict immutability | Simple | The operator has to delete the row as well, and no API does |
| (c) The row follows the document always | No state | | The cached bytes become unverifiable and the engine's own check raises the event on every request |

**Choice:** (a). **Rationale:** the digest protects bytes that are in the cache; where there are none, nothing is protected and the fetch is
verified against what the upstream announces now. The served version document shows the recorded digest and size, so the document a client
checks the archive against is the one the cached bytes were verified against. The event `proxy.upstream_digest_changed` is queued by the data
port when the offered digest was not taken ([H-857](../../security/ansible-remote.md#h-857--a-republished-version-whose-archive-was-evicted-is-trusted-at-its-new-digest)).
Revisit if the audit shows an upstream that republishes versions as a habit.

**Applied in the spec:** R-ANS-018.

### Q-ANS-39 · A remote reads the version list at the upstream's page size, starting with the first page

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every request reads page 0 for the count, takes the page size from it when the upstream answers fewer than 100 entries, and reads the stored pages that cover the range (chosen) | An upstream that caps `limit` below 100 is read at its cap; one document per page, variant `offset=<n>` | Correct counts and ranges whatever the cap | A request for a later page also needs page 0 (cached after the first) |
| (b) Assume 100 | The public Galaxy's cap | No special case | A capped upstream would show holes at every page border |

**Choice:** (a). **Rationale:** the version list's cap of the public Galaxy was not probed (the page "Client and library behaviour"). The
variant space stays bounded: offsets below 51,200 (512 pages of 100), a page number of a role at most 1,000.

**Applied in the spec:** R-ANS-017.

### Q-ANS-40 · The role host rule is applied twice: when a version list is stored and when it is served

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `PrepareFill` records an archive address only for a version whose `download_url` is an https URL on a host of `extraHosts` and counts the others, the served list leaves the others out (chosen) | The URL map holds addresses the operator allowed; a client is never offered another | The stored document still holds the dropped entries as received | The same rule in two places |
| (b) Only the engine's host check at fetch time | A version is listed and its download fails | Less code | A client is sent to an address that then answers an error |

**Choice:** (a). **Rationale:** [R-ANS-020](#r-ans-020--remote-repository-roles-by-translation) promises that no client is sent to download from elsewhere than the operator allowed. The
size bound `ansible.role_archive_max_bytes` is applied by `AcceptResponse` (the declared length) and by a reader around the body (the
bytes), from the configuration of the replica that was bound last ([H-858](#h-858--the-role-archive-limit-is-one-value-per-process)).

**Applied in the spec:** R-ANS-020, R-ANS-030 (the label of the metric is `repo`, the one label R-OPS-007 allows for a repository).

### Q-ANS-41 · A restore without remote caches deletes the recorded digests with the cache

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The purger `ansible-remote` of the restore fixups empties the caches of every Ansible remote and deletes `ansible_upstream_versions` of each (chosen) | As npm's `npm_remote_packages` | A restored installation fetches and records afresh | A purger of its own |
| (b) Leave the rows | Less code | | A digest recorded for bytes that are gone would refuse an upstream that republished meanwhile |

**Choice:** (a). **Rationale:** the rows describe documents and archives of the cache and must go with it (R-BAK-008).

**Applied in the spec:** R-ANS-029.

### Q-ANS-42 · The gate of an archive reads the recorded digest through the port, without the facts of the item lookup

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `BeforeArtifact` asks the data port for the row on every request (chosen) | One indexed read per archive request | No change of the frozen `ServeFacts` of the contract | One statement more per download than npm's gate |
| (b) Implement `FactsGate` and let the item lookup read the row | As npm | No second statement | A new member of `ServeFacts` and of the item lookup for one format |

**Choice:** (a). **Rationale:** a change of the contract for a statement that is a primary-key read is not earned yet. Revisit when the query
budget test of the Galaxy downloads shows the statement (the integration area `querybudget`).

**Applied in the spec:** R-ANS-018.

### Q-ANS-43 · The roles of the source are reported once per run, with the first collection

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The format counts the roles once (`v1/roles/?page_size=1`) and names the count under `notKept` of the first planned collection (chosen) | `notKept` is per package in the report, the roles are not a package | No change of the runner, the report or the state file (R-IMP-009); the operator sees the loss | The note sits on an arbitrary collection of the report |
| (b) A report field for the run | A place of its own | Honest placement | Changes the runner and the report for one format |
| (c) Say nothing | Roles are not imported | Nothing to build | The operator learns of the loss when a playbook fails |

**Choice:** (a). **Rationale:** the seam promises that nothing in the runner changes. Revisit when a second format has a loss that belongs to the
source and not to a package: a run-level note then earns the report field of (b).

**Applied in the spec:** R-ANS-027.

### Q-ANS-44 · A merge that could not ask a member fails instead of serving a partial list

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The merge fails: 503 `upstream_unavailable`, `Cache-Status: detail=partial`, `Retry-After`, nothing cached (chosen) | R-ANS-023 | A client never resolves against a list that lacks the versions of a member that was down: `ansible-galaxy` picks the newest version of the list, so an incomplete list silently installs an older one | A remote that is down takes the virtual list of every collection it holds down with it, even when a local member holds the collection |
| (b) Serve the partial list uncached, as the NuGet class does (R-NUGET-017) | The list of the members that answered | The local versions stay available | A dependency resolves to an older version without any sign; the sign (`Cache-Status`) is not read by the client |

**Choice:** (a). **Rationale:** the Galaxy client has no way to tell a short list from a complete one, and what it installs from one is a
decision about the software of the installation. Revisit when a member can be marked optional (the engine's partial result per member,
H-348): a local-first repository would then serve the members that answered.

**Applied in the spec:** R-ANS-023.

### Q-ANS-45 · The digest conflict is looked for in the versions a local and a remote member share

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Read the version document of a remote member for each version that a local member also holds, through the member; count once per merge built (chosen) | The digest of a remote version is known when its document was read; the merge makes it known where a conflict can be | The acceptance criterion holds without a prior read of the remote's document; the cost is the number of shared versions, each read cached by the remote class and paid from the page budget of the merge | A merge makes a few member reads |
| (b) Use the digests the remote class recorded (`ansible_upstream_versions`) only | No reads | Cheap | A conflict is seen only after somebody read the remote version's document: the first install after the publish does not count it |
| (c) Compare in every request that serves a version document | The counter follows the requests | Exact for the version served | The counter then counts requests, not versions |

**Choice:** (a). **Rationale:** a conflict is a property of the two members' versions, so it is counted where the merge sees both. Revisit if
the reads show up in the remote's request counts of a repository with many shared versions: (b) then becomes the rule and the criterion
the one of a version read at least once ([H-861](#h-861--the-digest-conflict-counter-counts-a-merge-that-is-built-not-a-request)).

**Applied in the spec:** R-ANS-022.

### Q-ANS-46 · A publish through a virtual repository runs the local handler on the target and rewrites the task address

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The route serves the classes local and virtual; the virtual handler resolves the target, makes a request for it and runs the local class's handler, then gives the task address of the 202 the virtual repository's (chosen) | The OCI pattern (writes act on `Request.WriteTarget`) | The checks, the ownership and the shadowing check are written once; the client keeps polling the address it was given (R-ANS-024); no redirect, which would drop the `Authorization` header of some clients | The handler holds the 202 answer to rewrite it, and depends on the local handler putting the task address into the body |
| (b) The dispatcher redirects the write to the target (`redirectClass`) | NuGet's pattern | No code | The task address then names the target, and the client polls the repository that is not the one it configured: a deploy target changed later makes polls fail |

**Choice:** (a). **Rationale:** R-ANS-024 asks for the task on the address the client was given, and the polling is the part of the
protocol the client cannot do without. Revisit if the local handler stops writing the task address into the body; the unit test of the
rewrite fails then.

**Applied in the spec:** R-ANS-024.

### Q-ANS-47 · The conflict counter's label is `repo`, and the format has one `Bind`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The label is `repo`, the cardinality-capped label of every format (R-OPS-007); the class registers its families with `registerBinder`, run by the format's one `Bind` (chosen) | The metrics registry refuses any other name for a repository | The series leave with the repository; one rule for all formats | The text of R-ANS-030 said `repository` |
| (b) A label `repository` | As the first text | | Refused by the registry at start (the unit test of the bind proves it) |

**Choice:** (a). **Rationale:** the registry decides. The composition root binds a format through one `Bind(h, cfg, metrics)`, so the classes
add their families to that one function (`virtual_metrics.go` registers the virtual ones; a remote class adds its own there).

**Applied in the spec:** R-ANS-022, R-ANS-030.

### Q-ANS-48 · The setup snippets: ids by the core's convention, a `hostname` template function, and PowerShell and cmd forms of the token variable

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Template ids `ansible-galaxy.<name>.<section>`, a template function `hostname` in the API and the kit, `ini` as the language of the `~/.netrc` snippet, the token variable also in PowerShell and cmd (chosen) | The snippet ids come out as `ansible-galaxy.<section>.<name>` | The core derives the snippet id from `<client>.<name>.<section>`; the netrc line carries the host name alone, which is what the client looks up, also on the dev edge with its port | A new function in the template contract (a few lines in the API, one in the kit) |
| (b) The ids as the table of R-ANS-025 first wrote them (`ansible-galaxy.<section>.<name>`) | | Matches the earlier table | The core reads the middle part as the name and would render the id `ansible-galaxy.configure.configure` for all of them |
| (c) `machine <host>` as a placeholder the reader fills | No new function | | A line that is wrong until edited, and a test that cannot run it unchanged |
| (d) `schemeless` of the registry URL as the machine | No new function | | Carries the port and the path; the client's lookup is by host name, so the line never matches |

**Choice:** (a). **Rationale:** the id convention and the lookup by host name are facts of the core and of the client, not of taste. `hostname`
returns the host of an http or https URL without port, brackets or path, and the value unchanged for anything else. Revisit when a second
format needs a host-bound credential in a form `hostname` cannot give.

**Applied in the spec:** R-ANS-025 (the table), api-and-ui.md (R-API-030, the template functions).

### Q-ANS-49 · The client images: the Python each release supports, every dependency pinned, no credential and no CA baked in

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) One image per release on the official Python image of a Python the release supports, `ansible-core==<exact>` installed under a constraints file that pins every dependency (chosen) | ansible-core 2.17.14 on Python 3.11 (the release supports 3.10 to 3.12), 2.21.6 on Python 3.13 (3.12 to 3.14); the constraints are frozen from the first build | The same bytes of the client on every run; a floating dependency cannot turn a client failure into a trove finding | The constraints files are edited by hand when the release moves |
| (b) The newest of each series, unpinned dependencies | | Nothing to maintain | A run that is red tomorrow for a reason in jinja2 or cryptography |
| (c) The newest ansible-core (2.22.0, which needs Python 3.13) as the second client | | The newest client | The catalog and R-ANS-001 name 2.17 and 2.21; a third release is H-864 |

**Choice:** (a). **Rationale:** the tags of the Python images never move (digest only, renovate.json); `make versions-check` runs
`test/e2e/ansibleclients/check-pins.mjs`, which compares the versions, the tags and the constraints; `TestAnsibleClients` compares
`ansible-galaxy --version` with the pins. Revisit when a release of the matrix reaches its end of support.

**Applied in the spec:** R-ANS-032.

### Q-ANS-50 · The client pods: the dev CA by environment variable, every command in a clean home

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `SSL_CERT_FILE` in the pod's environment points at the mounted dev CA; the tests run each command through `trove-clean`, which makes a new home and removes it (chosen) | The environment of the pod reaches every exec | `ansible-galaxy` has no option for a CA file and uses Python's default verify paths, which OpenSSL reads from this variable; no ansible.cfg, `~/.netrc`, galaxy cache or collection of an earlier scenario can reach a command | The CA file replaces the system store for the pod; only the edge is asked, and trove asks the stub and the public Galaxy itself |
| (b) The CA in the system store of the image | | No variable | The CA is baked into an image, or an entrypoint edits the system store at start |
| (c) `--ignore-certs` | | Nothing to mount | Does not test the certificate path the setup snippets describe |

**Choice:** (a). **Rationale:** the pods stay long-lived like the other clients, and a scenario that fails never poisons the next. Revisit if a
scenario needs the public store (a client that reaches a host outside the cluster).

**Applied in the spec:** R-ANS-032.

### Q-ANS-51 · The hub exchange is the optional interface CredentialExchanger, and the engine sends the exchanged token and never the credential

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `formats.CredentialExchanger`: the format builds the realm request from the credential and reads the answer; the engine sends it under the host rule, caches the token and sends the upstream request with the token in place of the credential (chosen) | The credential is handed to the format for one request only; the engine owns the I/O, the cache and the retry | The offline token never reaches the upstream; the host rule is the engine's, not each format's; reuses the handshake's bounded read and the token cache | An addition to the contract and a branch in the engine's `send` |
| (b) The format exchanges through `RemoteFetcher.Exchange` | No engine change | | The format would need the credential value, which it cannot read, and the first upstream request would still carry the credential |
| (c) Extend `Authenticator.TokenRequest` with the credential | One interface | | The handshake answers a 401 challenge and runs after a first request: the credential would already have gone to the upstream |

**Choice:** (a). **Rationale:** [R-ANS-019](#r-ans-019--remote-repository-a-hub-behind-single-sign-on) needs the refresh token in a form body. `ExchangeRequest` receives it and returns the request; the engine
(`internal/proxy/credential_exchange.go`) refuses a realm on a host the hop policy would not give the credential to, exchanges before the
first request of the repository to the upstream's origin, keys the cached token by the format's key, the realm and the digest of the
credential (a rotated offline token needs a new exchange), and on a 401 of the upstream exchanges once more and sends the request once more.
A realm's 4xx is `upstream_auth_failed`. The request to the realm carries no `Authorization` header of the credential (the hop policy is told to add none): the realm receives the refresh token once, in the body. A mirror's discovery request (`Upstream.Get`) takes the same exchange. The lifetime is the realm's `expires_in` less 30 seconds. Revisit when a second format needs
a credential exchange: the interface is not Galaxy-specific.

**Applied in the spec:** R-ANS-019, architecture.md R-ARCH-008, ADR-191.

### Q-ANS-52 · The mirror lists by offset and plans from the version lists, and the version bounds cut before documents are read

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `listing` walks the collection index 100 entries at a time with the cursor `{"offset": n}`; the plan reads the stored version-list pages and, newest first, the documents of the versions the bounds keep (chosen) | One request per index page and per kept version | No document is read for a version the engine would drop; the digest of every kept version is recorded by its fill | A pass reads the whole index even for a narrow pattern |
| (b) Narrow the index by the `namespace` filter of a literal prefix | Fewer index pages | Cheaper for `acme.*` | A second request shape, and the filter was verified on the public Galaxy only; a collection of a namespace in another case is missed |
| (c) Read every version document, then apply the bounds | Simple | | A document per version of a collection whose versions are dropped |

**Choice:** (a). **Rationale:** the public Galaxy lists 4,685 collections, 47 index pages, which a pass reads in seconds; the cost of a
document per version is the cost that matters. Revisit with (b) when a mirror's index walk is measured as the pass's dominant cost (H-846).
The role index is never read. The hub's index is listed with the exchanged token, as a fetch is (`Get` of the engine takes the same exchange, Q-ANS-51).

**Applied in the spec:** R-ANS-021.

### Q-ANS-53 · A file part with Content-Transfer-Encoding base64 is decoded while it is spooled

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Decode a `file` part that says `Content-Transfer-Encoding: base64` while it streams into the spool (chosen) | `base64.NewDecoder` over the part; the digest, the size limit and the inspection see the decoded bytes | It is what the real client sends: `prepare_multipart` of ansible-core 2.17 and 2.21 encodes the archive as base64 in lines of 76 characters (the first run of the e2e set against the local class answered 400 `sha256_mismatch` for every `ansible-galaxy collection publish`, because the digest was that of the encoded text); streaming, no buffering | A body is a third larger on the wire than the archive; `ansible.max_archive_bytes` bounds the decoded bytes, the system upload limit the encoded ones |
| (b) Accept only raw parts | | Nothing to build | No real client can publish |
| (c) Ask the client to change | | | Not in our hands |

**Choice:** (a). **Rationale:** the integration tests of the local class sent raw parts, the way the specification described the request; the real
client was first seen by the e2e set. Revisit if a client sends another transfer encoding (the multipart reader of the standard library decodes
quoted-printable by itself).

**Applied in the spec:** R-ANS-008.

### Q-ANS-54 · The netrc snippet is a command that appends the line

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A shell command: `printf 'machine %s login trove password %s\n' <host> "${TROVE_TOKEN}" >>~/.netrc` after `touch` and `chmod 600` (chosen) | The shell expands the reference | A netrc file expands no variable; the pasted file of the first version held the reference as the password, the client sent it as Basic in place of its Token header, and every request answered 401 (seen in the e2e set with ansible-core 2.17 and 2.21) | One more way to write a credential into a file, as the Debian snippet does for `auth.conf` |
| (b) A file snippet with the reference | | Matches the table of R-ANS-025 as first written | Never works |
| (c) A placeholder the reader replaces | | | A line that is wrong until edited |

**Choice:** (a). **Rationale:** the only form that works. The finding behind it is also a fact about the client: a netrc line for the host wins over the
`Token` header for every request, so the guide says to keep the token in it valid. Revisit if ansible-core stops preferring netrc.

**Applied in the spec:** R-ANS-025, R-ANS-015.

### Q-ANS-56 · trove stores and shows the documentation of a collection: generated for a local publish, kept as received for a remote

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Context: task T-19 (owner, 2026-11-11). galaxy_ng shows the README, the modules, the plugins and the roles of a collection with their pages. The
documentation is not in the archive as such: galaxy-importer builds `docs_blob` at import, by running `ansible-doc` (Client and library behaviour
above). So the question is what trove, which never ran a collection through an importer, can honestly offer for a **local** publish.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Local: generated by trove at publish from the archive's text; remote: kept as received; virtual: the winner's (chosen) | R-ANS-033 to R-ANS-036 | The owner's own collections are local ones and the screen he asked for needs exactly this; no process is run; the three classes show the same screens | A parser to maintain; the result is weaker than ansible-doc's where fragments matter (H-891) |
| (b) Local: only the README and the contents list (file names), remote: as received | No parsing | Cheap and cannot be wrong | The pages "Modules (8)" would open empty for a local collection: the screen exists to read what a module takes |
| (c) Pass through from an upstream only | Remote and virtual only | Nothing to generate | Nothing for a local repository, the class the owner publishes his collections to |
| (d) Run `ansible-doc` as the importer does | Exact output, fragments merged | Fidelity | A Python and ansible-core installation in the image, a process that loads plugin code of uploaded archives: rejected as a trust boundary (R-ARCH-028) and as a second runtime |

**Choice:** (a). **Rationale:** (b) and (c) leave the repository the owner uses without the feature, (d) puts uploaded code next to the server.
The galaxy_ng of the owner shows `doc_strings` null for every entry of `hans_fischer.common` 1.5.0: even there the pages hold what the
importer's environment could produce, so "the plugin's own DOCUMENTATION string, shown as written" is not below the reference. Revisit if the
owner finds generated pages too thin for his collections (then fragments, H-891) or if a safe way to run `ansible-doc` exists.

**Applied in the spec:** R-ANS-033 to R-ANS-036, R-ANS-038.

### Q-ANS-57 · The documentation of a publish is generated in the inspection pass and written in the commit transaction

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) In `Inspect`'s pass; rows written by `OnCommit`; bounded and non-fatal (chosen) | R-ANS-034 | One read of the archive; version and documentation commit or fail together; the import task can carry the warnings; no state "documentation missing" after a successful publish | The publish request does the work under the 30-second deadline (H-843); inputs sit in memory |
| (b) A job after the commit | Documentation appears seconds later | The request stays as fast as today | A second read of the archive for every publish; a state between publish and documentation; the warnings have no task to go to |
| (c) On first read | Generated when a person opens the tab | Nothing is done for versions nobody reads | The request path reads and inflates an archive (H-853 again), a write in a GET |

**Choice:** (a). **Rationale:** the archive is already being streamed and digested; the documentation inputs are text files in it. Measured on the
public Galaxy: 833 entries and 3.5 MB of output for the largest collection, a median entry of 3.6 KB, so memory and time are small next to the
archive. The reindex job covers the other direction (versions published before, a changed generator). Revisit if a publish of a very large
collection comes near the deadline in practice (then (b) for the bounds above a size).

**Applied in the spec:** R-ANS-034, R-ANS-033.

### Q-ANS-58 · The generator scans the Python source for its documentation strings and decodes them as YAML nodes; it runs no ansible-doc

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A lexical scan for module-level `DOCUMENTATION`, `EXAMPLES`, `RETURN` string literals, plus YAML sidecars; YAML decoded into a node tree under the bounds of `meta/runtime.yml`; fragments not merged (chosen) | R-ANS-034 | No execution; what ansible-doc itself does for modules (it reads these strings from the source); bounded; deterministic; a leaf package with a fuzz target | A value built by an expression is not read; fragments are listed, not merged (H-891, H-892) |
| (b) A real Python parser | Parse the module to an AST | Handles every literal form | A Go Python parser is a large dependency and a new untrusted-input parser for little gain |
| (c) Merge same-collection fragments too | Resolve `extends_documentation_fragment` of `plugins/doc_fragments/` | Closer to ansible-doc for collections that use their own fragments | The merge rules (options override, lists extend, per-key exceptions) are ansible-doc's to define; a wrong merge shows wrong parameters; the builtin and other collections' fragments stay unresolvable anyway |

**Choice:** (a). **Rationale:** a documentation string that is not shown is a visible gap, a parameter table that is wrong is a lie; (a) shows exactly
what the author wrote and names the fragments, (c) can be added later without changing the stored shape (`extendsFragments` already carries the names). Revisit
when an owner collection uses fragments for its parameters.

**Applied in the spec:** R-ANS-034; H-891, H-892.

### Q-ANS-59 · The documentation is derived tables, one row per entry, not a document per version

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `ansible_version_docs`, `ansible_doc_entries`, `ansible_doc_files`, and for a remote the small `ansible_doc_fetches`; derived, not exported (chosen) | R-ANS-033 | A page is a primary-key read; the tree never loads entry bodies; search is SQL; the same rows for generated and upstream entries; rebuilt from the archive or the stored document | Three tables; the remote's data exists twice (document and rows) |
| (b) One `jsonb` per version | The Galaxy's `docs_blob` as is | One write, one read | A page loads up to 6.5 MB to show 4 KB; no search without scanning; the whole document is rewritten on any change |
| (c) Only the stored remote document, parsed per request | No rows for remotes | No duplication | 6.5 MB parsed per page view and per search; two code paths (rows for local, parse for remote) |
| (d) Files in the blob store | An item per entry | Reuses the item store | Thousands of tiny blobs per version, garbage collection and reads for what a table holds |

**Choice:** (a). **Rationale:** the access pattern is "the tree of a version" and "one entry of it", both small, from a table of at most 5,000 rows
per version. The remote's duplication is bounded (a stored document is a cache and is evicted like any, the rows are the index); the rows are derived,
so a backup does not carry them (R-BAK-004's classes are untouched) and a restore rebuilds them with the reindex. Revisit if the rows of a mirror of
the whole public Galaxy are measured to cost too much (H-890).

**Applied in the spec:** R-ANS-033, R-ANS-029 (the tables are not exported).

### Q-ANS-60 · A remote's documentation is an ordinary metadata document of the engine, filled by the route and by a job; a screen never queries the upstream

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Verified in this repository: a format cannot enqueue a job (R-ARCH-008), but its data port can, in the fill transaction (`Reindex` of the Debian port
does); `MirrorHost.Background(ctx, repoID)` returns the upstream a data port's job uses, and its `Document` is the engine's metadata fetch with
`PrepareFill` and `OnFill` (`internal/formathost/mirror_host.go`), throttled by the mirror window. So no change of the remote contract is needed.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The route is a normal engine document (`UpstreamRequest`, `PrepareFill`, `OnFill`, `Rewrite`); `OnFill` of the version document queues `ansible-docs-fill`, whose handler calls `Background(...).Document`; a mirror pass queues the same job; setting `fetchDocumentation` (default true) (chosen) | R-ANS-035, R-ANS-040, R-ANS-042 | One code path for the route, the job and the pass; the credential, egress, rate limit and flight rules are the engine's; R-API-029 holds (a screen reads rows); an operator can switch it off | An upstream request per version, 150 KB to 6.5 MB; the first view after an install may find `pending` |
| (b) On the first screen read | The UI read fills | Only what is read | A GET that fetches 6.5 MB from an upstream on behalf of a viewer, breaking R-API-029 and giving an anonymous reader a lever on the upstream |
| (c) Inside the fill of the version document | One flight | No `pending` state | Every client install waits for up to 6.5 MB it does not use (the reason `metadata.contents` is left out of the version document) |
| (d) Never for remotes | Local only | Nothing to fetch | The owner's virtual repository over a remote Galaxy shows nothing for most collections |

**Choice:** (a). **Rationale:** it adds an upstream request that today does not exist, so it is a setting, and the work is the engine's own kind. The mirror
rule follows "keeps what the patterns select" (Q-ANS-16): a mirror that serves the archive should serve the page. Revisit when the size of a full mirror with
documentation is measured (H-890) or the public Galaxy is found to rate limit `docs-blob/`.

**Applied in the spec:** R-ANS-035, R-ANS-040, R-ANS-042, R-ANS-026 (the setting); H-890.

### Q-ANS-61 · A virtual repository shows the winner's documentation; the screen names the repository

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The member that serves the version document serves the documentation; the operations take a `repository` and answer which one was read (chosen) | R-ANS-035, R-ANS-036 | The document and its documentation always agree (one first-match plan); members are not named to a caller who may not see them; no merge | A version in two members shows one member's text |
| (b) Merge the entries of all members | Union of entries | More entries | Entries of different builds of one version mixed: wrong, since versions are immutable per member and the digests may differ (R-ANS-022) |
| (c) Prefer the member with the richest documentation | Highest entry count | Better pages | A rule nobody can predict |

**Choice:** (a). **Rationale:** same rule as the version document and the archive. Revisit with Q-ANS-18.

**Applied in the spec:** R-ANS-035, R-ANS-036.

### Q-ANS-62 · The documentation is served at `docs-blob/` and the version document keeps leaving the plugin listing out

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A route `…/versions/<v>/docs-blob/` in the Galaxy's shape; `metadata.contents` stays out of the version document (chosen) | R-ANS-035; Q-ANS-10 stands | Another trove or a Galaxy-compatible tool reads the documentation where it expects it; a trove remote over a trove repository works; installs read the small document as before | One more route in the frozen table |
| (b) No protocol route; the UI operations only | Smaller surface | Less to build | A remote over another trove has no documentation; the route is nearly free once the rows exist |
| (c) Bring `metadata.contents` back into the version document | The Galaxy's full document | Same bytes as the public Galaxy | 848 KB for every `ansible-galaxy` install of a large collection; Q-ANS-10's reason is unchanged: no client reads it |

**Choice:** (a). **Rationale:** the correction to Q-ANS-10: the documentation was never a member of `metadata`; it is a sub-resource (verified on both servers), so
serving it separately costs a route and keeps Q-ANS-10's reason intact. Revisit with Q-ANS-10 (a client that reads `contents`).

**Applied in the spec:** R-ANS-004, R-ANS-017, R-ANS-035.

### Q-ANS-63 · The screens read three operations: a tree per version, a page per entry, a file per document; the search is a parameter of the tree

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `getPackageVersionDocs` (tree and search), `getPackageVersionDocEntry`, `getPackageVersionDocFile` under the package and version of R-API-029 (chosen) | R-ANS-036 | The screen already holds package and version ids; tree and entry sizes are small and cacheable; the wiki marker is one per operation | Format-specific operations under the generic package path |
| (b) Under the repository: `/api/v1/ansible/{projectKey}/{repoKey}/…` | Next to `listAnsibleImportTasks` | Fits the management operations of the format | The package screen is cross-repository; it would have to pick the repository first and the virtual winner by hand |
| (c) One operation returning the whole `docs_blob` | The protocol document | One call | Up to 6.5 MB per tab view; no sanitized prose |
| (d) Client-side search over the whole tree | The tree carries all text | No search request | The tree would carry every description (hundreds of KB); a second matching implementation |

**Choice:** (a). **Rationale:** the tree carries only names and short descriptions (about 100 bytes per entry), so the UI can show the tree at once; `q` runs in SQL
over one version's rows, so it can match descriptions and parameter names without shipping them. Revisit if search across collections is asked for (Deferred).

**Applied in the spec:** R-ANS-036; api-and-ui.md R-API-029 (a pointer).

### Q-ANS-64 · Plugin prose is the Ansible markup converted by trove; Markdown goes through the README renderer; reStructuredText is not rendered

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Escape first, convert the macros `I() B() C() U() L() R() M() O() V() E() P() RV()` to `em strong code a`, then the README renderer's sanitizer (R-UI-008); Markdown by goldmark; no reStructuredText (chosen) | R-ANS-039 | The author's text never reaches a browser as markup; one sanitizer policy; the importer renders no reStructuredText either | A README in `.rst` shows no README tab entry |
| (b) Pass the upstream's `readme_html` and own-rendered HTML unsanitized | As received | Faithful | An upstream or a publisher can inject script into the UI origin |
| (c) A reStructuredText renderer | `README.rst`, `docs/*.rst` | More collections show a README | A large parser for a format the reference does not render |
| (d) Render the macros in the browser | The UI converts | No server work | A second sanitizer policy in the client (R-UI-008 has one, server side) |

**Choice:** (a). **Rationale:** R-UI-008 puts rendering on the server so one policy serves every client; the converter is a pure function with a fuzz target and the sanitizer
behind it is the second layer. A `.rst` README is reported as "no README" by the documentation tab, which is what the reference does, and costs the author a Markdown file. Revisit if
the owner's collections use reStructuredText. Where the sanitizer is reachable from is Q-ANS-66.

**Applied in the spec:** R-ANS-039, R-ANS-036, R-ANS-034 (the README choice).

### Q-ANS-65 · Bounds: entries, bytes and files per version, and a bound crossed never fails the publish

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) 512 KiB per literal, 2 MiB per source, 1 MiB per entry, 5,000 entries and 16 MiB per version, 512 KiB and 64 per Markdown files; a crossed bound drops that part and warns (chosen) | R-ANS-031 | About 6 times the largest collection seen (833 entries, 3.5 MB; the largest entry 59 KB; the largest `docs-blob/` 6.5 MB); a hostile archive cannot fill the table; a collection is never lost for its documentation | A giant collection loses its tail (the entries are in alphabetical order by type and name; `truncated` says so) |
| (b) Refuse the publish | `invalid_archive` | Strict | `ansible-galaxy collection build` accepted it; the documentation is a convenience |
| (c) No bounds on the Markdown/JSON of a remote | Take what comes | Fidelity | The document bound (`MaxDocumentBytes`) already applies; the rows need the per-entry bounds |

**Choice:** (a). **Rationale:** the numbers come from the measurements in the verification table; the same bounds apply to the rows of a remote. Revisit when a real
collection hits one.

**Applied in the spec:** R-ANS-031, R-ANS-034, R-ANS-035.

### Q-ANS-66 · The Host gains a `Markup` capability, because a format may import neither the renderer nor bluemonday

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Verified in this repository: `internal/archtest/imports.go` (`formatAllowed`) lets a format import the contract, its catalog, the untrusted-content leaf, the
error kinds, configuration, observability and a few leaves; `internal/packages/readme` and every third-party HTML library are outside it. The README is rendered in
`internal/api/v1` (`ps.Readme`). The `docs-blob/` route is served by the format and holds HTML members.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A Host method `Markup() formats.Markup` (`Markdown`, `SanitizeHTML`), implemented over `internal/packages/readme` by the composition root, in memory by the kit, an error-returning `NoMarkup` by a bare Host (chosen) | R-ANS-039 | One policy for every surface; the format stays inside its import rule; the same pattern as `Vulnerabilities()` | A change of the contract (ADR-196) |
| (b) The format ships its own sanitizer | A second policy | No contract change | Two allowlists that drift; a new HTML parser in the format; the format of untrusted input becomes a security component |
| (c) The route serves Markdown and the client renders | `readme_md` members | No renderer | Not the Galaxy's shape (`html`); a tool that reads `docs-blob/` gets no HTML |
| (d) Add `internal/packages/readme` to `formatAllowed` | The format imports it | Smallest change | Opens the rule for one package and pulls goldmark and bluemonday into every format's dependency set |

**Choice:** (a). **Rationale:** the rule exists so that a format cannot reach what it should not; a capability is how the contract lets it. The sanitizer of
untrusted HTML is exactly the thing that must not exist twice. Revisit if a second format needs more than Markdown and HTML sanitizing.

**Applied in the spec:** R-ANS-039; [ADR-196](../../decisions.md#adr-196--the-host-gains-a-markup-capability-for-the-rendering-and-sanitizing-of-a-formats-documentation).

### Q-ANS-67 · The search runs in SQL: substring on the name, prefix on a `simple` text-search vector, every term must match

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `strpos(lower(name), term) > 0` or `search @@ to_tsquery('simple', 'term:*')`, terms reduced to letters and digits, ANDed, over one version's rows (chosen) | R-ANS-036 | No injection (terms become bound parameters of a built tsquery from alphanumerics only); finds identifiers and words; no stemming surprises | No ranking, no typo tolerance |
| (b) `ILIKE '%term%'` over name, description and options | Simple | Easy | Scans `doc` jsonb text; slower for 5,000 rows; escaping of `%` and `_` |
| (c) Filter in the browser | No request | Instant | Names only; the tree must carry all text |
| (d) A search engine | Ranking | Better | A new component for a table of at most 5,000 rows |

**Choice:** (a). **Rationale:** the corpus is one version (at most 5,000 rows, the measured largest 833). Revisit if search across collections is asked for.

**Applied in the spec:** R-ANS-036, R-ANS-033 (`search_text`).

### Q-ANS-68 · For a virtual repository the wiki check is asked for the repository addressed and for the winning member

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Both must allow; a member whose wiki the caller may not read is skipped like a member it may not read (chosen) | R-ANS-037, R-ANS-041 | A virtual repository cannot widen a member's wiki; its own level also counts; the first-match plan stays one plan | A caller may see a later member's documentation where the first member's wiki is closed to it, which equals what it sees for artifacts |
| (b) Only the virtual repository's | One question | Simple | A virtual repository would publish the documentation of a private member's wiki |
| (c) Only the member's | One question | The member owns its content | The virtual repository's own wiki setting would mean nothing |

**Choice:** (a). **Rationale:** it is the rule R-AUTHZ-008 applies to artifacts, applied to the second part. Revisit with R-AUTHZ-034's treatment of virtual repositories.

**Applied in the spec:** R-ANS-037, R-ANS-041.

### Q-ANS-69 · The wiki of the format `ansible` is the documentation and the README; the version document is not wiki

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Wiki: `docs-blob/`, the three operations, `metadata.docs`, the README (generic operation and `hasReadme`), `metadata.contents` and `docs_blob` if ever served; not wiki: the version document and its description and links, the lists, the archive, the import tasks (chosen) | R-ANS-037 | The version document is what `ansible-galaxy` reads to install; making any field of it wiki would break installs for a caller who may read the artifacts; the list is exact and testable | The one-line `description` is visible with the artifacts |
| (b) Everything the screens show besides the install panel | A wider wiki | More closed | Breaks installs or hides data a client needs |
| (c) Only `docs-blob/` | A narrower wiki | Simple | The UI operations would leak the same text |

**Choice:** (a). **Rationale:** the line follows R-AUTHZ-034's definition: documentation of a version's contents and READMEs. Revisit when R-AUTHZ-034 changes the definition.

**Applied in the spec:** R-ANS-037; [ADR-197](../../decisions.md#adr-197--a-formats-documentation-is-gated-by-one-call-the-wiki-check-and-never-by-the-artifact-check).

### Q-ANS-70 · The end-to-end rows are one per class in the set `ansible`, with a real collection and a browser journey each

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) A row per class (local, remote on the stub, virtual) plus the wiki row, each with the real `ansible-galaxy` and a Playwright journey, in the existing set `ansible` (chosen) | R-ANS-046 | The three defects of F0 that integration missed were client behaviours; a set is cut by what it tests (CLAUDE.md) | Kind time for three journeys |
| (b) One journey against the local repository | Less | Cheap | The remote's job and the virtual winner are the parts integration cannot show with a client |
| (c) A new set `ansible-docs` | A leg of its own | Isolation | Sets are cut by what they test, and this is the format's |

**Choice:** (a). **Rationale:** the rows are cheap beside a kind boot and are the only proof that a real client's install and the screen agree. Revisit if the leg's time matters.

**Applied in the spec:** R-ANS-046.

### Q-ANS-71 · The renderer of the protocol document takes its own two-method Markup interface, which the Host's value satisfies

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `galaxydoc` declares `Markup` with the two methods it calls (`Markdown`, `SanitizeHTML`), and the Host's `formats.Markup` satisfies it structurally (chosen) | `RenderDocsBlob(d, m galaxydoc.Markup)` | `galaxydoc` stays a leaf (the layer rule allows the standard library, `errs`, the YAML module and the rows leaf); the same pattern as the matcher's `Adapter` (ADR-150) | Two interfaces with the same methods; a drift is caught by the compiler at the call, not at the declaration |
| (b) `galaxydoc` imports `formats` for `formats.Markup` | The plan's literal signature | One interface | `formats` is the edge layer; a leaf may not import it (R-ARCH-002), and the architecture test fails |
| (c) Pass two functions | `RenderDocsBlob(d, markdown, sanitize)` | No interface | Every caller builds two closures; the capability is no longer one named thing |

**Choice:** (a). **Rationale:** the plan's signature `RenderDocsBlob(d, m formats.Markup)` cannot compile in a leaf; the structural form keeps the call the plan describes (`RenderDocsBlob(d, rq.Host.Markup())`). Revisit if the Host's capability grows beyond the two methods.

**Applied in the spec:** R-ANS-035, R-ANS-039; [ADR-196](../../decisions.md#adr-196--the-host-gains-a-markup-capability-for-the-rendering-and-sanitizing-of-a-formats-documentation).

### Q-ANS-72 · The tree the port reads carries the counts per type before the search

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `rows.DocTree` carries `Counts` (entries per type before `q`) beside `CountBeforeQuery`, and the stored `Warnings` (chosen) | One query answers the tree and every count of R-ANS-036 | The groups' and types' `count` stay constant while one searches without a second call; the API groups from one value | A map in the neutral form |
| (b) A second port method `DocCounts` | Two reads per tree | The tree stays a list | Two queries that can disagree under a concurrent rebuild |
| (c) The API counts the entries it got | No port change | Nothing to store | With `q` the entries are the matches, so the counts before `q` are lost (R-ANS-036 forbids exactly that) |

**Choice:** (a). **Rationale:** R-ANS-036 requires the type and group counts to be those before `q`, and the plan's `DocTree` has only the total. Revisit if a count per type is not needed once the tabs are built.

**Applied in the spec:** R-ANS-036.

### Q-ANS-73 · The address of the documentation is the URL-map row of the version document's fill, and a recorded none keeps the route from asking again

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The fill of a version document records, as a URL-map row (R-REPO-043) of the path `collections/<ns>/<name>/docs/<version>.json`, the `href` plus `docs-blob/` when its host is the upstream's or an extra host and its path ends in `/versions/<version>`; `UpstreamRequest` derives the default address from the API root, which the row replaces; a version document without a usable `href` writes the fetch row `none`, and a fresh `none` row (younger than an hour) keeps the route and the job from asking the upstream (chosen) | R-ANS-035, R-ANS-042 | The engine already takes the address of a stored document from the URL map; no column, no migration; the href is never read from a request; a foreign host is never fetched | A request for `docs-blob/` before the version document was filled uses the derived address, which is on the upstream's origin |
| (b) `UpstreamRequest` reads the stored version document | The spec's first wording | One place | `UpstreamRequest(ctx, RemoteContext, path, variant)` has no access to a stored document or to the data port |
| (c) A column `docs_url` in `ansible_upstream_versions` (a migration) | A recorded address | Explicit | `UpstreamRequest` cannot read it either, and the migration numbers are owned by several tasks |

**Choice:** (a). **Rationale:** the stub's foreign-host case is the acceptance criterion; with (a) the stub counts no request. Revisit if a format-side read of stored documents is added to the remote adapter.

**Applied in the spec:** R-ANS-035, R-ANS-042.

### Q-ANS-74 · The virtual route walks the plan itself, local members first, and a member without documentation falls through to the next

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `virtualDocs` takes the plan of the resolver (`Resolver.Plan`), orders the members by tier and position, asks the wiki check for the addressed repository and for each member, and serves the first member that has documentation: the rows of a local one, the stored document of a remote one through the engine (chosen) | R-ANS-041, R-ANS-037 | The check depends on the caller, so no document is shared between callers through the merged-metadata cache; a remote member the caller may not fill is served from the cache only by the engine | A member that holds the version and has no documentation does not stop the walk: a later member's documentation of the same number is served |
| (b) The merged-metadata cache with a `LocalDocument` for the docs path | First match like the version document | One code path | A document cached for one caller reaches another whose wiki check differs; the winner is not known to the check |
| (c) Stop at the first member that holds the version | Exact winner | Never mixes members | Needs the version document of every remote member first (a request each) |

**Choice:** (a). **Rationale:** correctness of the wiki gate outweighs the exactness of the winner; the fall-through is listed as H-901. Revisit when T-18 gives the check a cacheable key.

**Applied in the spec:** R-ANS-041.

### Q-ANS-75 · The README of a generated set is named README.md and the generator reads the README at the root or in docs/

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The README is stored as the file `README.md` whatever the manifest called it (`collection_info.readme` names a Markdown file at the root or directly in `docs/`); a name that is not `.md` is the warning `readme_not_markdown`; a file of `docs/` with the name of the README is left out with a warning (chosen) | R-ANS-033, R-ANS-034 | The README is found by one name in the rows, the file operation and the protocol document (`collection_readme.name`) | A README elsewhere in the archive is not read |
| (b) A column that marks the README | A migration | Any name | The tables are the contract of R-ANS-033 |
| (c) Keep the manifest's name | `readme.md`, `docs/intro.md` | Faithful | The reader must know which file is the README |

**Choice:** (a). **Rationale:** the tap wants members before the manifest is read, so it keeps every Markdown candidate and `Build(readmeName)` picks. Revisit if a real collection keeps its README elsewhere.

**Applied in the spec:** R-ANS-033, R-ANS-034.

### Q-API-264 · The documentation operations resolve the repository from the version details, and `metadata.docs` is added by the details handler

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The operations read the version details of the caller's scope (`view.Service.Version`) and take its repositories, local before remote, a virtual repository from its members in the order of their tiers; the details handler of the format `ansible` adds `metadata.docs` with the same resolution (chosen) | R-ANS-036, Q-API-263 | No new optional interface of the format contract and no new method of the view service; the same code resolves the tabs and the pages | The resolution ignores the pin rules and claims of a virtual repository (H-902); `metadata.docs` costs the resolution per details request |
| (b) `DocsAnnouncer` on the view adapter and `Service.VersionRepositories` | The plan's contract | The core builds the member | A format-side optional interface (ADR-141 addition) and a second path for the same answer |

**Choice:** (a). **Rationale:** the plan asks to stop if the plan order cannot be had without a change to the core; it can be had from the details. Revisit when T-18 moves `metadata` members behind the wiki check (`ViewData.WikiMetadata`).

**Applied in the spec:** R-ANS-036, Q-API-263.

### Q-ANS-76 · A remote repository's cache is listed by the generic rows and described by a row of the manifest that the commit of a fill writes

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Generic rows only | `CommitFill` creates the package and the version and links the item | No table of the format | The package has no description, licence, dependencies or links, and the owner's task asks for them for every class |
| (b) `CommitFill` also writes the row of `ansible_collection_versions` from the manifest the inspection read (`PutCachedVersion`: no event, no tombstone, no claim) (chosen) | R-UI-020, R-UI-021, R-ANS-026 | The screens read one row for every class (`AnsibleViewRows`), so the Install tab, the links, Requires Ansible and the Dependencies of a remote's collection are what a local one's are; the manifest was read under the bounds of R-ANS-031 by `InspectFill`, nothing is read again; the row is deleted with the cached item | A cache entry is in the table of the local class; the code that reads it by class stays scoped by the repository (the reindex and the version documents run for local repositories only) |
| (c) Read the manifest of the upstream's version document | The version document has the same facts | No archive needed | The listing is by cached archive (Q-ANS-78), and the version document is not stored per version in a row |

**Choice:** (b). **Rationale:** the gap found by the real clients was that a remote's collection was not listed at all; the owner's task shows the same page for local, remote and virtual. Revisit if the table is split by class.

**Applied in the spec:** R-ANS-018, R-ANS-026.

### Q-ANS-77 · A virtual repository lists the collections of its remote members through the rows of the members

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The view lists the holders of a version through the topology: a virtual repository holds what its members hold, so the rows of Q-ANS-76 are listed under the virtual repository without a row of its own (chosen) | R-UI-020, R-ANS-022 | One mechanism; the integration test of `ansiblevirtual` proves it | A collection the remote member has not cached is not listed |
| (b) A row per virtual repository | Rows in the virtual repository | Explicit | A virtual repository stores nothing (R-REPO-018) |

**Choice:** (a). **Applied in the spec:** R-ANS-022, R-ANS-026.

### Q-ANS-78 · A remote version is listed when its archive is cached, not when its version document is filled

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The row is written by the commit of the archive's fill (chosen) | R-UI-020 | A listed version has a file, a size and a digest; the same moment as the other formats' caches | The documentation of a version whose archive is not cached is reachable by the protocol route only |
| (b) At the fill of the version document | Earlier | The documentation tab appears before an install | A version without a file |

**Choice:** (a). **Applied in the spec:** R-ANS-026, R-ANS-036.

### Q-ANS-79 · The eviction of a cached archive removes the generic version as the core does and leaves the format's documentation rows

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The core's removal of the item and its version stands; the documentation rows stay until the repository or the cache is purged (H-900) (chosen) | R-ANS-040 | No removal hook for a cache | A viewer can reach rows of a version that is no longer listed (they need the package id, which is gone) |
| (b) A `RemovalHook` for the remote class | Rows follow items | Clean | A hook for every eviction of a cache |

**Choice:** (a). **Applied in the spec:** H-900.

## What this does not cover

### H-839 · A remote's signatures are passed through and not verified by trove

A remote version document's `signatures[]` reach the client unchanged (R-ANS-013, Q-ANS-14). trove neither verifies them nor holds keys,
so a bad or missing signature at the upstream is not caught by trove; only a client with `--keyring` catches it. Live today for every
remote that serves signed versions; real for a fleet whose clients do not set a keyring (the default of `ansible-galaxy`).

### H-840 · A role archive has no announced digest and the source is not byte-stable

A role archive is a GitHub archive of a tag (R-ANS-020); the upstream announces no digest, and a tag can move or GitHub's archive bytes can
change. trove stores the first fetch and serves it ever after, so a cached copy can differ from what GitHub serves later, and a never
cached role is fetched without verification beyond its form. Live whenever a remote serves roles; two fetches seconds apart were equal
(2026-11-10), longer spans were not observed. Under `integrity.checksumPolicy: require` roles are refused.

### H-841 · Collections are not evaluated for vulnerabilities

The format has no vulnerability adapter (R-ANS-013, Q-ANS-15): OSV has no Ansible ecosystem. Every repository of the format is
`not_covered`, and nothing about a collection's plugin code or the packages its roles install is checked. Live today; real for any
installation that relies on the dashboard for Ansible content.

### H-842 · The hub token exchange is specified from documentation and never run against the service

The Automation Hub's API root, its realm and client identifier, and the response shape are taken from the catalog page and the usual OAuth
refresh-token grant (R-ANS-019); no Red Hat account was available to the development agent. The mechanism is proven against a stub
realm only. Dormant until a remote is pointed at the hub; real the first time it is, when the URL, the client id or the shape may differ.

### H-843 · A publish inspects the archive inside the request

The inspection of a collection archive (a streaming pass over up to 1 GiB, R-ANS-010) runs in the publish request (Q-ANS-8). A slow
storage or a large archive can run into the request or ingress timeout, and the client sees a failed request, not a failed task; the
idempotent retry makes the retry safe but pays the upload again. Live for archives that take longer than the timeout to upload and
inspect; none is expected for the sizes collections have (the public Galaxy's largest version document describes 2.6 MB of archive).

### H-844 · Git and URL roles bypass trove

A `requirements.yml` entry with `scm: git`, a `git+https` source or a tarball URL is fetched by the client from where it names, never
through trove (R-ANS-014). Egress control, the cache and the dependency-confusion rules do not see it. Live for every project whose
requirements file has such entries; trove can only document it.

### H-845 · Galaxy namespaces are not entities of trove

The public Galaxy requires a namespace to exist before a publish; trove has no namespace object: the first publisher of `<ns>.<name>`
claims the name by the repository's ownership rules (`autoClaim`, R-AUTHZ-018) and namespace claims (R-REPO-023) restrict who may. An
operator who expects Galaxy's namespace-first workflow sets claims for the namespaces. Live today; a difference in workflow rather than a
hole, recorded because the owner's current system may rely on it.

### H-846 · The size of a full mirror of the public Galaxy is not measured

The public Galaxy listed 4,685 collections (2026-11-10); the number of versions and the bytes were not counted, and the hosting
requirements of a full mirror (storage, the time of a pass, the Galaxy's rate limits) are unknown (R-ANS-021). Dormant until an operator
configures an unbounded mirror; real when they do.

### H-848 · A remote does not track versions that an upstream removes

`TracksVersionRemoval` is false (R-ANS-017): a version that disappears from the upstream's list disappears from the served list at the next
revalidation, and its cached archive stays downloadable by path. Live for any upstream that removes versions; the cached copy can outlive
a withdrawal the upstream made for a security reason.

### H-850 · Each replica exchanges the refresh token for itself

The access token is kept in the replica's own cache (R-REPO-033), so every replica of an installation asks the realm once per token lifetime
and the realm sees one session per replica for one offline token. A realm that limits the sessions of an offline token, or rotates it on
use, would refuse the second replica; neither was observed, because the service was never reached ([H-842](#h-842--the-hub-token-exchange-is-specified-from-documentation-and-never-run-against-the-service)). Live for a hub on several replicas.

### H-851 · A collection archive beyond the core's reader bounds is refused

The archive reader runs under the core's bounds, 10,000 entries and 2 GiB inflated (R-ANS-031, Q-ANS-30), and a format cannot raise them. A
collection with more files or more expanded bytes is refused at publish, in a fill and by the reindex, with a failed task that names the bound.
Dormant: the collections of the public Galaxy are far below both; real for an internal collection that vendors a large tree.

### H-852 · The restore of a version from the trash is not served

The item store's trash can and its restore do not exist yet (R-STOR-022); the format has no `OnItemsRestored`. A version removed with the
reason "trash" is tombstoned and its row is dropped like a deletion, and nothing brings it back until the restore and its hook are built.
Dormant today; real when the trash can is built.

### H-853 · The README of a version is read by streaming the archive up to it

An archive is a gzip stream, which has no offsets: the README of a version for a screen is found by inflating the archive up to that
member, within the bounds of R-ANS-031 and 20 seconds. A collection whose README comes late in a large archive costs a screen that much
inflation, per request, and the member is cut at 64 KiB. Live for every screen that shows a README; the cost is the size of what precedes it.

### H-854 · A caller whose grants hide versions makes the list read all versions of the collection

When a grant decides versions by path (`VersionsByPath`), the version list of a collection is read whole (at most 50,000 versions) and
filtered before it is paged, so a hidden version is neither listed nor counted (R-AUTHZ-005). Dormant for every repository whose grants do
not narrow by path; real for one that does and whose collection has thousands of versions.

### H-858 · The role archive limit is one value per process

`ansible.role_archive_max_bytes` reaches the response check of a fetch through the configuration of the replica that was bound last in the
process (`internal/formats/ansible/remote_bind.go`): the check has no Host to ask. Dormant: the replicas of one process share one configuration
in production. Real only for a test that starts replicas of different limits in one process.

### H-860 · The upstream test does not give the hint for an upstream URL that is no API root

[R-ANS-016](#r-ans-016--remote-repository-upstream-and-discovery) asks that a URL such as `https://galaxy.ansible.com/` is reported "with the hint that the API root of a Galaxy usually ends in
`/api/`". The 502 of every request through such a repository carries the hint (`parseDiscovery`); the generic upstream test of R-REPO-024 requests
the URL and judges the status alone, so it reports a URL that answers 200 with something else as reachable. Live today for every remote repository
of the format; the first request names the reason.

### H-861 · The digest conflict counter counts a merge that is built, not a request

`trove_ansible_virtual_digest_conflicts_total` is raised when a merge is built (R-ANS-022, Q-ANS-45). A merge the engine serves from its
merged-metadata cache is not built again, so a conflict that stays for a day is counted once per cache period and per replica, not per
install; and a remote member the caller may not fill takes part with its cached versions only and is read only for the versions it has
cached, so a conflict in an uncached version is not counted for that caller. Live today for any virtual repository with a local and a
remote member that hold one version with two digests: the number says that a conflict exists, not how often it is met.

### H-863 · A role lookup is answered by one member, and the ids inside a role document are the upstream's

The lookup of a role through a virtual repository is answered by the first remote member whose answer is not empty (Q-ANS-19); two
members that hold different roles under one owner and name are not merged, and the second is never offered. The ids of the versions inside
a role document (`summary_fields.versions[].id`) are the upstream's own and are not rewritten: the client does not use them as routes.
Live for a virtual repository over two remotes of a Galaxy each; dormant with one.

### H-864 · Only two ansible-core releases are tested

The e2e set runs ansible-core 2.17.14 and 2.21.6 (R-ANS-032, Q-ANS-49). The releases between them, and 2.22, which PyPI offers and which needs
Python 3.13, are not run. Live: a change of the client between 2.17 and 2.21, in how it reads a document or retries, would meet trove for the
first time in a user's pipeline. The differences in the source (`api.py`, `token.py`, the role code) were compared for 2.17.14 and 2.21.6 only.

### H-865 · The dependencies of the client images are pinned by version, not by hash

`constraints-2.17.txt` and `constraints-2.21.txt` pin every package by exact version. A file that PyPI replaced under the same version would
not be noticed by the build. Dormant: PyPI refuses a replacement of a file; real when a project is yanked and re-uploaded by another party.

### H-866 · The guide's statements about the client are not run

The guide says what `ansible-galaxy` caches (`~/.ansible/galaxy_cache`, `--clear-response-cache`), which server a role install takes and that a
role with a Git source bypasses trove. They are read from the documentation and the source (the verification table above); the tests prove the
install, publish and credential paths, not these sentences. Live for the cache statement: a client release that renames the flag makes the
guide wrong without a test failing.

### H-867 · A ~/.netrc line for the host wins over the token for every request

ansible-core 2.17 and 2.21 use a netrc line of the host for every request to it and send Basic in place of their `Token` header (seen in the
e2e set: a line that held a wrong password turned every install into a 401 although the token in the environment was right). The setup writes
the line because roles need it (R-ANS-015), and trove cannot change the order of the client. Live for a user who keeps an old or wrong line
for the host; the guide and the manual say so, and no check of trove can see it, because the request arrives with the wrong credential.

### H-868 · The versions of one name in two members are not run with the real clients

`TestAnsibleVirtual` and `TestAnsibleMirror` run (R-ANS-032), but a name that a local member published first is claimed by it (R-REPO-023,
the default `local.autoClaim`), so the end-to-end set never has one name with versions in a local and in a remote member at once, and nothing
runs with the real clients that the merged list of such a name is the union, first tier winning per version (R-ANS-022, Q-ANS-18). The kit and
the integration area `ansiblevirtual` prove the merge; the clients' view of it, and of a conflict of digests, is not run. Live for a virtual
repository whose local member has the claim switched off.

### H-890 · The size of the documentation of a full mirror and of many local versions is not measured

The rows of R-ANS-033 cost, for the largest collections of the public Galaxy, 1 to 7 MB per version (community.general 14.0.1 3.3 MB, fortinet.fortios
2.6.0 6.5 MB of JSON; the stored `jsonb` is compressed, by how much was not measured), and a remote keeps the document as well. A mirror (R-ANS-021) with
`fetchDocumentation` on keeps both for every version it keeps, and each version is one upstream request. Nothing bounds the total per repository, only
the bounds per version (R-ANS-031). Live for a mirror whose patterns select large collections with many versions, and for a local repository that
publishes a documented version often; dormant for a pull-through remote that only fills what clients ask for. The setting switches the fetch off per remote.

### H-891 · Documentation fragments are not merged into the documentation of a generated entry

ansible-doc merges `extends_documentation_fragment` (fragments of the same collection, of `ansible.builtin` and of other collections) into a plugin's
options, notes and requirements. trove's generator shows the plugin's own `DOCUMENTATION` and lists the fragment names (`extendsFragments`), so a
parameter that a plugin takes through a fragment is missing from its parameter table, and `module_utils` and `doc_fragments` files are listed without a
page. Live for every generated entry that extends a fragment (collections with a shared connection or authentication fragment); an entry of a **remote** is not affected, its upstream merged them. The owner's `hans_fischer.common` 1.5.0 has none to show:
its `doc_strings` are null on his galaxy_ng.

### H-892 · The generator reads literals, not Python, and does not read role argument specs

A `DOCUMENTATION`, `EXAMPLES` or `RETURN` that is not a module-level string literal (built by concatenation with a name, a call, an f-string, assigned inside
a class or function, or imported from another file) is not read: the entry is listed with `has_docs` false and **no warning**, because a lexical scan cannot tell "no
documentation" from "documentation in a form it does not understand". Roles are listed with their README only; `meta/argument_specs.yml` and `meta/main.yml` are
not read. Live for collections that generate their documentation at build time or document roles by argument specs; the page then says the plugin has no
documentation although the author wrote some.

### H-893 · An upstream's HTML and the sanitizer policy can differ in what they keep

A remote's `collection_readme.html`, `documentation_files[].html` and `readme_html` are HTML rendered by the upstream (galaxy-importer: Python-Markdown
`extra` and a small allowlist) and are sanitized again by trove's policy (R-UI-008) before they are shown or served. Constructs the upstream kept and the policy removes
(images from origins not on `ui.readme.external_image_origins`, which become links; `style` and `class` attributes other than `language-*`) are lost, and
the document served at `docs-blob/` by a remote is therefore not byte-identical to the stored one. Live for any upstream README with an external image
(the usual badge); the stored document keeps the original. reStructuredText READMEs are not rendered by anyone in this chain (Q-ANS-64).

### H-894 · The wiki gate registry proves the surfaces it lists, not that it lists every surface

Stage: R-ANS-037. Every surface of the format that serves documentation calls `wikiReadable`, and `TestR_ANS_037_EverySurfaceAsksTheWikiGate` walks a registry of the
surfaces that the handlers register themselves into. A handler added later that serves documentation text (a new route, a new field of the version document, a
search that returns a description from the documentation) and is not registered is not gated, and nothing fails. Live today for none of the routes and operations of
the format (the list is complete as of this specification); what makes it real is a later change to the format or to the generic package operations. Check 15 of the format kit (R-ARCH-010) narrows it for the routes: it serves every content case again for a caller who reads the artifacts and not the wiki and fails when a README text of the fixture is in an answer, so a new route that leaks the README text is found; it proves nothing for documentation text that is no sample of the fixture, and not for a management operation. The gate itself,
its levels and their enforcement are R-AUTHZ-034's and appear on [permissions.md](../../security/permissions.md).
