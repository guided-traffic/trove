# Ansible Galaxy repositories

Status: specification, planned (block B, unit F0 of [FULL.md](../../projektplan/FULL.md); the work packages are in
[ansible-work-packages.md](../../projektplan/ansible-work-packages.md)). Nothing on this page is built yet. It is the first registry after the
beta because it replaces a Galaxy system the owner runs today.

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
- whether the Galaxy of the owner (the import source, R-ANS-027) speaks the same v3 documents; it is not named on this page and the
  development agent has not seen it (H-847);
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
| Default upstream | `https://galaxy.ansible.com/api/` (an API root, R-ANS-016); default extra hosts `github.com` and `codeload.github.com`, without credentials (role archives, R-ANS-020); probe path empty (the URL itself answers the discovery document) |
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
itself answers the discovery document (R-ANS-003). The same repository is reachable by path at `/files/<project>/<repo>/` as for any
format (R-STOR, R-EP-014).

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
- The document needs `read` on the repository like every other route, so a repository without anonymous access answers 401 here and
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
  license_file,readme,repository,tags}`. trove omits `metadata.contents`, `metadata.docs_blob`, `metadata.manifest` and `metadata.files`:
  no client in the matrix reads them, and they make a document of the public Galaxy 848 KB ([Q-ANS-10](#q-ans-10--the-version-document-leaves-out-the-plugin-listing)).
  `collection.id` is a stable UUID derived from the repository and the name (the local class), or the upstream's (remote).
- **Absolute addresses.** `download_url`, every `href` and every pagination link are absolute URLs on `<base>` (the response base URL of
  the endpoint, R-EP-003) and point to this repository's own routes, never to an upstream or a member, whatever the class. The next
  link of a list carries the same `limit` and the next `offset`; the last one has `next: null`.
- **Limits.** `limit` defaults to 100 and is clamped to 1..100; an `offset` beyond the count returns an empty `data`. A request for an
  unknown collection or version answers 404 in the Galaxy shape (R-ANS-007).
- **Local class.** The documents are computed from the rows of R-ANS-011 on every request (`rev_seq`-keyed renditions, R-ARCH-012);
  `created_at` is the publish time on the database clock and `updated_at` the newest version's; `deprecated` is always false.
- **Remote class** and **virtual class**: R-ANS-017 and R-ANS-022.
- `Cache-Control` and `ETag`: a computed or rewritten document carries a weak `ETag` derived from the stored digest or `rev_seq` and
  the serving address, and answers `If-None-Match` with 304 (R-NPM-013's pattern); the client ignores them, other tools do not.

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
- The same bytes are reachable at `/files/<project>/<repo>/collections/<ns>/<name>/<file>` (R-STOR, R-EP-014); both routes decide the
  same path with the same grants.

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
- Anonymous access follows the endpoint's rule (R-AUTH-037) and the repository's read grant for the anonymous principal; an
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
| Upload through `/files/` (the deploy dialog) | `deploy` | as the publish |
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
the same publish is reachable as an upload of the archive through `/files/<project>/<repo>/collections/<ns>/<name>/<file>` (the deploy
dialog and `curl -T`), which skips the multipart framing and the task but not a single check below.

1. **Framing.** The handler reads the parts in either order (the client sends `sha256` first, the documentation shows `file` first),
   streams the `file` part into the spool without buffering it (R-ARCH-014) and bounds the preamble as the NuGet push does. A missing
   part is 400 `invalid_request`. The part `sha256` is compared with the digest of the spooled bytes **before the archive is
   inspected**: a mismatch is 400 `sha256_mismatch` and nothing is stored. Without the part the upload is accepted (a plain `PUT`
   has none).
2. **Inspection** (R-ANS-010) reads identity and metadata from the archive. An archive that cannot be a collection archive is
   recorded as a **failed import task** (R-ANS-009), not as an HTTP error, so that the client prints Galaxy's own failure text.
3. **Authorization** of the identity the archive declares (`deploy` on `<ns>.<name>` and the archive path), ownership of the name
   (`local.autoClaim`, R-REPO-023, R-AUTHZ-018) and the shadowing check of a first version (R-REPO-048) run after inspection and
   before the commit, with no transaction open; they are rechecked in the commit transaction under the package row's lock.
4. **Immutability** ([Q-ANS-9](#q-ans-9--a-published-version-is-immutable-a-deleted-one-leaves-a-tombstone)): the archive at the path of a version that exists is
   refused 409 `version_exists` unless the bytes are identical (same SHA-256), when the publish succeeds again with the task of the
   first publish (idempotent retry). A version whose number is tombstoned (R-ANS-012) is refused 409 `version_tombstoned`.
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
- `curl -T file.tar.gz <base>/files/web/ansible-local/collections/acme/tools/acme-tools-1.0.0.tar.gz` stores the same version; the same
  `curl` to a path whose name differs from the manifest's is 422 `path_mismatch`.

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
  on demand by the package screens through ranged reads, as NuGet does (R-ANS-026).
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
  links at this repository's address. The default client request (`limit=100`) is one stored page per 100 versions. Reading is
  bounded per request (512 pages, 51,200 versions); a longer list is 502 `upstream_unavailable` with that reason.
- **Rewriting.** In the served documents `download_url` is the repository's `v3/artifacts/collections/<file>`, every `href` and link
  the repository's address, `metadata.contents`, `metadata.docs_blob`, `metadata.manifest` and `metadata.files` are dropped (Q-ANS-10); all
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
- **The first digest recorded for a version stays.** If a refreshed version document announces a different SHA-256 for a version already
  recorded, the recorded row and the cached archive are kept, `proxy.upstream_digest_changed` is written, and the client's own digest
  comparison against the (unchanged) served document passes; an operator resolves by deleting the cached item.
- **Inspect on fill.** `FillInspector` reads the fetched archive's `MANIFEST.json` (R-ANS-010, bounded) and refuses an archive whose
  declared identity or version differs from the path (502 `upstream_unavailable`, `identity_mismatch`); nothing of it reaches a client
  before that, and nothing is stored. The `requires_ansible` of the row is checked against the document's.
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
  that receives the credential in effect for one exchange only; its shape is decided by the package that builds it and recorded as a
  decision and in architecture.md R-ARCH-008 (ADR-191 reserves the number).
- A failed exchange (a 400 `invalid_grant` of the realm, an unreachable realm) is `upstream_unavailable` with the reason
  `upstream_authentication_failed`, never an empty list; the repository's status shows it.
- `ansible.authUrl` must be an `https` URL (`http` only for a loopback test stub) and is validated like `remote.url`.
- The `x-trove-classes` of both settings is `remote`.

Acceptance criteria (with the stub's realm; the service itself is not reachable from the tests):

- The stub's upstream accepts only `Bearer` access tokens its realm issued; a remote with a refresh token installs a collection, and
  after the stub expires the access token, the next request exchanges again and succeeds.
- A wrong refresh token: the install fails with the 503 of `upstream_authentication_failed` and the stub's realm saw no request with
  the old access token.

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
- **Names.** A role document has no `<ns>.<name>` package. The documents and archives of roles are classified as metadata and artifacts
  of the package name `role/<id>`, which no collection can have; they are decided by the repository's grants and path patterns
  (`roles/**`), not by a package name an operator would know. (Whether the core's name machinery takes that package name is open and
  proven by WP0; the fallback is a classification without a package, in which case claims and ownership do not apply to roles, which
  nothing in the design needs.)
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
  entry stays the winner's and a metric `trove_ansible_virtual_digest_conflicts_total` counts it (the client reads the winner's
  document and archive, so it is consistent). Order: semver descending; `meta.count` is the merged count; pagination is cut after
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
  answered with 503 `upstream_unavailable` and `Cache-Status: detail=partial` (R-REPO-020); H-348's limitation (a merge cannot mark one
  member partial) is that of NuGet's merge and carries over.
- The archive route walks the plan of R-REPO-019 step 6 and serves the first hit with the headers of R-ANS-005.

Acceptance criteria:

- A caller who can read only the local member sees the local versions through the virtual repository and no error for the remote one.
- With the stub down and the remote's version list not cached, the merged list is 503 `partial`, and after the stub is back it is complete.

### R-ANS-024 · Publishing through a virtual repository
Priority: **P1** · Covers: PKG-ANSIBLE-003

`POST v3/artifacts/collections/` to a virtual repository forwards to its **deploy target** (R-REPO-021): the target must be a local
repository of the format, else 405 `no_deploy_target` in the Galaxy shape. The target's checks (R-ANS-008) apply to the caller's
principal, with the virtual repository's patterns and claims in front of the shadowing check (R-REPO-048: a first publish of a name
that a remote member of the virtual repository serves is refused 403 `shadows_remote_package` unless the caller holds `manage`).

- The answer's `task` is the address of `v3/imports/collections/<id>/` **on the virtual repository** (the address the client was given),
  which looks the task up in the deploy target (R-ANS-009).
- The upload through `/files/` of a virtual repository is routed to the deploy target the same way (an upload needs no code of the format,
  as in R-DEB-028).

Acceptance criteria:

- `ansible-galaxy collection publish -s <virtual URL>` stores the version in the local target, the command's polling succeeds through
  the virtual address, and a subsequent `collection install` through the virtual repository installs it.
- A virtual repository without a deploy target answers 405 `no_deploy_target`.

### R-ANS-025 · Client setup instructions and the guide
Priority: **P1** · Covers: PKG-ANSIBLE-012

The setup templates (R-API-030) and the guide (R-UI-049) of the format ([Q-ANS-22](#q-ans-22--the-setup-writes-the-token-as-an-environment-variable-not-into-ansiblecfg)). The format declares one client, `ansible-galaxy`
(`Descriptor.Clients`, versions `2.17` and `2.21`), listed in the UI's table of clients per format (`FORMAT_CLIENTS`, `web/src/app/features/setup/clients.ts`).

| Template id | Section | File or language | Content |
|---|---|---|---|
| `ansible-galaxy.configure.ansible-cfg` | configure | `ansible.cfg`, ini | `[galaxy]` `server_list = trove` and `[galaxy_server.trove]` `url=<repo URL>`; the token line is **not** written (below) |
| `ansible-galaxy.configure.token-env` | configure | shell | `export ANSIBLE_GALAXY_SERVER_TROVE_TOKEN=${TROVE_TOKEN}` (for a repository that is not anonymously readable); PowerShell and cmd forms |
| `ansible-galaxy.configure.netrc` | configure | `~/.netrc`, text | `machine <host> login trove password ${TROVE_TOKEN}` with a hint to `chmod 600` (needed for roles, R-ANS-015) |
| `ansible-galaxy.install.collection` | install | shell | `ansible-galaxy collection install <package>` with the version spec `:<version>`; the package placeholder is the collection |
| `ansible-galaxy.install.requirements` | install | yaml | a `requirements.yml` with `collections:` and (remote and virtual) `roles:` examples, `source:` naming the server |
| `ansible-galaxy.publish.collection` | publish | shell | `ansible-galaxy collection publish <archive> -s <repo URL> --token=${TROVE_TOKEN}`, shown only to a caller who holds `deploy` on the publish target |

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
  by ranged reads (the entry named by `collection_info.readme`, text limit 64 KiB, never executed or rendered as HTML by trove), the
  size of a version, and (local) the publisher and time.
- The **deploy dialog** (UI-013) uploads an archive through `/files/` (R-ANS-008); the dialog needs no Ansible option because the
  archive names its own coordinates. The dialog's error for a refused archive shows the `detail` of the refusal.
- The UI shows the import tasks of a local repository on its edit form (a lazy tab, newest first, state and messages) through the
  management operation `listAnsibleImportTasks`; and the tombstones with the action to clear one.

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
  operator passes that prefix as part of `--from`. The importer does not guess. **The owner's instance has not been seen** by the
  development agent ([H-847](#h-847--the-import-is-written-against-the-public-galaxy-and-not-against-the-owners-instance)): the package that builds this requirement runs the importer against the first real source
  through `--dry-run` before any copy and records what differs.
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
`trove_ansible_role_versions_dropped_total{repository}`, `trove_ansible_virtual_digest_conflicts_total{repository}`,
`trove_ansible_document_bytes{kind}` (histogram of stored document sizes), `trove_ansible_hub_exchanges_total{outcome}`; the series of a
deleted repository are forgotten as for other formats (the metrics-forget test of NuGet is the pattern).

### R-ANS-031 · Limits for untrusted content
Priority: **P1** · Covers: trove-only

The archive a client uploads, an upstream's archive that a fill inspects and the documents an upstream sends are untrusted
(R-ARCH-028); the format tightens `untrusted.DefaultLimits` and states its own:

| Bound | Default | Where |
|---|---|---|
| Archive size (`ansible.max_archive_bytes`) | 1 GiB | publish, `/files/` |
| Archive entries / total expanded bytes (streamed through a digest) | 10,000 / 2 GiB, the core's defaults: a format may lower a bound and not raise it (Q-ANS-30, [H-851](#h-851--a-collection-archive-beyond-the-cores-reader-bounds-is-refused)) | `Inspect`, `InspectFill` |
| `MANIFEST.json` / `FILES.json` / `meta/runtime.yml` | 1 MiB / 16 MiB / 64 KiB | `Inspect` |
| Dependencies in a manifest | 256 | `Inspect` |
| README text read for a screen | 64 KiB | the view |
| Stored upstream document (`MaxDocumentBytes`) | 16 MiB | remote fill |
| Pages / versions read for one list | 512 / 51,200 | remote list |
| Role archive (`ansible.role_archive_max_bytes`) | 256 MiB | role fetch |
| Discovery document | 64 KiB | `ParseDiscovery` |

A bound crossed is 422 `invalid_archive` (a failed task for a publish) or 413 `payload_too_large` for sizes declared before reading,
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
- The assertions of the e2e tests name R-ANS IDs; a leg that is red is a finding, not a blocker of the release (CLAUDE.md, CI until the
  first stable release).

## Mirror discovery

What an Ansible remote in mode `mirror` discovers and stores (repositories-and-proxy.md R-REPO-050 to R-REPO-052; tests name R-REPO-051).
The engine, the bounds and the throttling are the core's; this is what the format contributes through its `MirrorLister`:

- **What the upstream offers.** A paged index of all collections (`namespace`, `name`, `highest_version`, `versions_url`) with
  `limit` at most 100 and `offset` (verified on the public Galaxy, 4,685 collections), a filter `namespace` and `deprecated`; per
  collection a paged version list; per version the document with `artifact.sha256`, `artifact.size` and `download_url`.
- **Methods** (`discover.Method`): `feed` not offered (no change feed was found); `listing` (what `auto` takes) walks the index by offset
  and returns the names the scope admits, in the index's order, in pages of the engine's limit with the cursor `{"offset": n}`; `names`
  takes literal include patterns as `<ns>.<name>`.
- **The plan of a name**: the version documents of the collection through `Upstream.Document`, newest first by semver, each an item
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
| R-ANS-008 to R-ANS-013 (publish, tasks, inspection, storage, delete, digests) | multipart reader, manifest parser, crafted archives | `ansiblelocal`: both part orders, task polling, immutability, tombstones, reindex, `/files/` | `ansible` (publish with the real client) |
| R-ANS-014, R-ANS-015, R-ANS-020 (roles) | role document rewrite, host rule | `ansibleremote`: translation, archive pull, dropped versions | `ansible` (role install with netrc) |
| R-ANS-016 to R-ANS-019 (remote, hub) | discovery parse, paging cut, `Rewrite`, `PrepareFill` | `ansibleremote`: counts, digest, changed digest, FillInspector, the stub's realm | `ansible` (remote, one request per document) |
| R-ANS-021 (mirror) | plan over fixtures | `ansibleremote` and `mirror`: a pass, a bound, a rate limit | `ansible` (mirror pass) |
| R-ANS-022 to R-ANS-024 (virtual) | merge over fixtures | `ansiblevirtual`: merge, conflict, claims, publish through the target, role ids | `ansible` (virtual) |
| R-ANS-025, R-ANS-026 (setup, screens) | templates render, kit check 8 | view API | `ansible` (rendered snippets), browser journey of the package screen |
| R-ANS-027 (import) | plan and held over the stub | `importer`: `ansible_test.go` | — |
| R-ANS-028 to R-ANS-032 | crafted inputs (kit) | Valkey stopped, journal replay of deletions, metric series | `ansible` in `scatter`; the closing run |

## Catalog coverage

Every entry of the catalog page, with what is built, what stays and what is not offered.

| Entry | Decision | Where |
|---|---|---|
| PKG-ANSIBLE-001 Three classes | Built; the federated form and its signing-key selector are not (no federation, no signing) | R-ANS-001, Q-ANS-14 |
| PKG-ANSIBLE-002 Galaxy v3 API per repository | Built: the three read operations, the discovery document, the version-list paging, the absolute addresses | R-ANS-003, R-ANS-004, R-ANS-007 |
| PKG-ANSIBLE-003 Publish with `ansible-galaxy` | Built: multipart publish, the stored import task, publish through a virtual repository | R-ANS-008, R-ANS-009, R-ANS-024 |
| PKG-ANSIBLE-004 Deploy through the UI or plain HTTP | Built through `/files/` and the deploy dialog; deploy by checksum and archive deploy are the generic ones | R-ANS-008, R-ANS-026 |
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
| [decisions.md](../../decisions.md) | ADR-191 and the table rows of Q-ANS-1 to Q-ANS-28 | — |

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

**Rationale:** the importer's seam exists for this. Revisit after the first run against the owner's instance (H-847).

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
| (a) No revision column on the format's tables (chosen) | The documents are rendered from the version rows; their ETag is derived from the generic package row's `rev_seq`, which the commit hook raises (R-ARCH-009) | `rev_seq` is already restored with a gap by the generic restore; one number to keep | A change of a version row that raises no package row would not change the ETag: there is none (every writer raises it) |
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

### H-847 · The import is written against the public Galaxy and not against the owner's instance

The importer speaks the v3 documents of the public Galaxy (R-ANS-027). The owner's instance has not been seen by the development agent
(which software, which version, whether its collection routes sit below a prefix, whether it holds roles, whether it enforces a
different name grammar). Dormant until the first run; real then, when a dry run may show differences. The package that builds the importer
runs a `--dry-run` against the first real source and records them.

### H-848 · A remote does not track versions that an upstream removes

`TracksVersionRemoval` is false (R-ANS-017): a version that disappears from the upstream's list disappears from the served list at the next
revalidation, and its cached archive stays downloadable by path. Live for any upstream that removes versions; the cached copy can outlive
a withdrawal the upstream made for a security reason.

### H-851 · A collection archive beyond the core's reader bounds is refused

The archive reader runs under the core's bounds, 10,000 entries and 2 GiB inflated (R-ANS-031, Q-ANS-30), and a format cannot raise them. A
collection with more files or more expanded bytes is refused at publish, in a fill and by the reindex, with a failed task that names the bound.
Dormant: the collections of the public Galaxy are far below both; real for an internal collection that vendors a large tree.
