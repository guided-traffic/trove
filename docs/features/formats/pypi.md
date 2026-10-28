# PyPI package format

This page catalogs what a repository manager does to act as a Python package index: hosting
wheels and source distributions in local repositories, proxying and caching an upstream index such
as pypi.org in remote repositories, merging both behind one index URL in virtual repositories,
client authentication, the upload protocol, index and metadata generation, naming enforcement,
search, and the specifics of pip, Poetry, Twine, uv and setuptools. The wire protocol belongs to
the Python packaging community: the simple repository API
([PEP 503](https://peps.python.org/pep-0503/), [PEP 691](https://peps.python.org/pep-0691/),
[PEP 658](https://peps.python.org/pep-0658/), [PEP 714](https://peps.python.org/pep-0714/) and the
consolidated [simple repository API specification](https://packaging.python.org/en/latest/specifications/simple-repository-api/))
and PyPI's [upload API](https://docs.pypi.org/api/upload/). Where an entry relies on those sources
rather than on the described system, it links them; the protocol reference near the end lists what
a compatible index serves. Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/pypi/<repo-key>` stands for the *repository base
URL* of one repository (the path layout is the implementation's choice), `pypi-local`,
`pypi-remote` and `pypi-virtual` are example repository keys, `ci-user` is an example user name and
`${TOKEN}` an access token.

## Repositories and addressing

### PKG-PYPI-001 · PyPI repositories in all three repository classes
Priority: **P1**

PyPI is offered as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)). Capabilities of the format as a whole:

- Python clients install from repositories of every class;
- local repositories hold an organization's own distributions, and the system computes their index
  (PKG-PYPI-013);
- remote repositories proxy and cache an external index such as `https://pypi.org/`
  (PKG-PYPI-017);
- virtual repositories expose several local and remote members under one index URL
  (PKG-PYPI-019);
- uploads produced by setuptools and its predecessor distutils are accepted (PKG-PYPI-024).

Supported clients and their minimum versions:

| Client | Install | Publish | Minimum version |
|---|---|---|---|
| pip | yes | no | 20.2 |
| Poetry | yes | yes | 1.2.0 |
| Twine | no | yes | 3.3.0 |
| uv | yes | yes | 0.8.15 |
| setuptools / distutils (`setup.py … upload`) | — | yes | unspecified |
| Pipenv | yes, specified only through the companion CLI (PKG-PYPI-027) | — | unspecified |

Creating a repository requires the system administrator or a project administrator role; for
other users the create action is not offered. UI location: administration, repositories, create
repository, then the class and the PyPI format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).
A local repository needs a key plus the general local settings; a remote repository needs a key and
a confirmed upstream URL (PKG-PYPI-017); a virtual repository needs a key and one or more members
(UI-032).

### PKG-PYPI-002 · Base URL, index URL and upload URL
Priority: **P1**

Every PyPI repository answers under its repository base URL. Clients use three addresses derived
from it:

| Address | Form | Used by |
|---|---|---|
| Index URL | base URL followed by `/simple` | pip `index-url`, `--index-url` in requirements files, Poetry `source add`, uv `[[index]] url` |
| Upload URL | the base URL itself, without `/simple` | `.pypirc` `repository` (Twine, setuptools), Poetry `repositories.<alias>`, uv `publish-url` |
| Search URL | the base URL, without `/simple` | `pip search --index` (PKG-PYPI-020) |

```text
https://repo.example.com/pypi/pypi-virtual/simple     # index URL (install)
https://repo.example.com/pypi/pypi-local              # upload URL (publish)
```

- The index URL is served by all three classes. Uploads are specified for local repositories only
  (PKG-PYPI-006).
- Index pages live under URLs that end in `/`. A request for the index URL without the trailing
  slash is answered with a redirect built from the system's externally visible base URL: behind a
  reverse proxy, the proxy must pass the original scheme and host to the system (through a header
  that the bundled reverse-proxy configurations add), or
  the client is redirected to an internal address. A request with the trailing slash, or one sent
  to the application port directly, avoids the problem.
- A remote repository configured with a custom index suffix accepts an extra path segment between
  base URL and `/simple` (PKG-PYPI-018).

### PKG-PYPI-003 · Names that differ only in dots and underscores
Priority: **P1**

The system accepts both `.` and `_` in package names, as part of its support of older PyPI
conventions, and does **not** merge names that differ only in these characters: `acme.tools` and
`acme_tools` are stored and listed as two packages. This is meant to match the public index's
behavior.

Edge cases:

- [PEP 503](https://peps.python.org/pep-0503/#normalized-names) defines the normalized name as the
  lower-cased name with every run of `-`, `_` and `.` replaced by one `-`
  (`re.sub(r"[-_.]+", "-", name).lower()`). Under that rule both example names map to
  `acme-tools`, and pip and uv request `simple/acme-tools/`. Which of the two packages the system
  returns for the normalized request, or whether it merges their files there, is unspecified.
- The file-name rules of PKG-PYPI-010 restrict which names can be uploaded once that switch is on.

### PKG-PYPI-004 · Reserved paths inside a repository
Priority: **P1**

The top-level folders `packages/` and `simple/` of a PyPI repository are internal namespaces of the
index. Any deployment below them (for example a generic deploy, ART-002 in
[../artifacts.md](../artifacts.md)) is rejected with HTTP 400. Whether the restriction applies to
all three classes or only to local repositories is unspecified.

## Client authentication

### PKG-PYPI-005 · HTTP Basic credentials for Python clients
Priority: **P1**

Python clients authenticate with HTTP Basic: a user name plus a password or token
(TOK-034 in [../access-tokens.md](../access-tokens.md)). The example snippets use an identity
token (TOK-005) as the password.

| Client | Where the credentials go |
|---|---|
| pip | Embedded in the index URL, `https://ci-user:${TOKEN}@repo.example.com/…/simple`. If the password and its colon are left out, pip prompts for it. This setup assumes that pip cannot read credentials from a file; see the compatibility note below. |
| Poetry | `poetry config http-basic.<name> <user> <token>`, once for each source or upload alias (PKG-PYPI-023). |
| Twine | `username` and `password` in `.pypirc`. Twine does not work with `.netrc` credentials. |
| setuptools / distutils | `.pypirc`; `.netrc` is supported for uploads. |
| uv | `uv auth login`, or `.netrc`; publishing reads separate variables (PKG-PYPI-025). |
| Any client | A keyring provider is an option; its mechanics are unspecified. |

- Reference tokens (TOK-004) carry no user name. pip, Pipenv and Twine need one, so a reference
  token must be paired with the user name of the token's subject; with an empty user name Twine's
  upload fails with 401.
- Anonymous reads work when anonymous access is switched on (IAM-005 in
  [../identity-and-access.md](../identity-and-access.md)).
- Whether PyPI endpoints accept `Authorization: Bearer` is unspecified; only Basic credentials are
  specified for Python clients.
- PyPI's convention for API tokens is the literal user name `__token__`: uv sends it when
  `UV_PUBLISH_TOKEN` is set, and Twine users write it into `.pypirc` by hand. TOK-034 requires the
  token's own subject user name, so whether `__token__` is accepted is unspecified.
- Relevant for compatibility: pip itself also reads `.netrc` and keyring credentials
  ([pip authentication](https://pip.pypa.io/en/stable/topics/authentication/)).

Security: credentials embedded in URLs end up in configuration files, shell history and CI logs;
this is the client's mechanism, not something the server can prevent.

## Publishing

### PKG-PYPI-006 · Upload through the PyPI upload protocol
Priority: **P1**

Clients publish by sending each distribution file to the upload URL (PKG-PYPI-002) with PyPI's
upload protocol (a multipart form POST; fields in PKG-PYPI-029):

| Client | Command |
|---|---|
| Twine | `twine upload --repository <pypirc-section> dist/*` |
| Poetry | `poetry publish --repository <alias> --dist-dir dist` |
| uv | `uv publish dist/* --index <index-name>` |
| setuptools / distutils | `python setup.py sdist bdist_wheel upload -r <pypirc-section>` |

- Target: a local repository. The companion CLI advises against publishing to a remote or virtual
  repository; whether a virtual repository with a default deployment repository
  (REPO-038 in [../repositories.md](../repositories.md)) accepts PyPI uploads is unspecified.
- The separate registration step of the old PyPI workflow (`setup.py register`) is neither needed
  nor supported and must be left out.
- The name, version and summary sent by the client become properties of the stored file
  (PKG-PYPI-007).
- Required permission: deploy on the target repository; replacing an existing file additionally
  needs the delete/overwrite action (IAM-022 in [../identity-and-access.md](../identity-and-access.md)).
- Unspecified: the response codes of a successful and a failed upload, the answer
  to a duplicate file, which form fields besides name, version and summary are stored, the storage
  path of an uploaded file, and whether attestations sent by newer clients are kept.
- File types: package cleanup treats `.tar.gz`, `.tgz`, `.gz`, `.whl`, `.egg` and `.bz2` as PyPI
  lead files (RET-009 in [../retention-and-cleanup.md](../retention-and-cleanup.md)).

### PKG-PYPI-007 · Package properties from distribution metadata
Priority: **P1**

Every stored PyPI distribution carries three standard properties (ART-021 in
[../artifacts.md](../artifacts.md)): package name, package version and summary. They come from the
fields the uploading client sends, or, later, from indexing, which reads the metadata embedded in
the file (`PKG-INFO` in a source distribution, `METADATA` in a wheel).

| Behavior | Value |
|---|---|
| Archive types the indexer can read metadata from | zip, tar, tgz, tar.gz, tar.bz2, egg, whl |
| Indexing quiet period | indexing starts 60 s after the last upload to the same repository |

- Files of other types are stored but only treated as PyPI packages when the properties are
  supplied at deploy time (PKG-PYPI-008).
- Which metadata fields beyond name, version and summary are extracted is unspecified.
- Uses: index generation (PKG-PYPI-013), search (PKG-PYPI-021), the UI info tab (PKG-PYPI-016).

### PKG-PYPI-008 · Deploy a distribution through the UI or generic upload
Priority: **P1**

A distribution can be deployed without a Python client, through the deploy dialog (ART-006 and
UI-013) or the generic deploy operation (ART-002 in [../artifacts.md](../artifacts.md)). For the
system to treat the file as a PyPI package, the deploy must attach the package name and version
properties (deploy-time properties, ART-003); otherwise the indexer tries to extract them from the
file (PKG-PYPI-007), which fails for unsupported archive types.

- Target path: unspecified; the reserved paths of PKG-PYPI-004 and the
  naming checks of PKG-PYPI-009 and PKG-PYPI-010 apply.
- Required permission: deploy on the repository.

### PKG-PYPI-009 · Enforce agreement between file name and metadata
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Require file name to match metadata | Instance-wide. Rejects a PyPI deploy whose file name or path version differs from the name and version in the distribution's own metadata. | off (switched on explicitly) |

- Scope: every PyPI repository of the instance; there is no per-repository switch. The setting is
  an instance-level configuration property, not a UI or API setting.
- Existing files are not touched. Uploading a new version, or overwriting a file, that violates the
  rule is refused.
- The comparison is literal: the metadata name must equal the name in the file path, even where
  the packaging standards allow the metadata name to contain `-` while the wheel file name uses
  `_`.
- Errors, both HTTP 403 with a message naming the policy:
  - the file name or version does not match the name or version in the metadata;
  - no metadata file was found to compare against.
- The exact accepted and rejected examples are unspecified.

Interactions: PKG-PYPI-010 checks the form of the name; this entry checks agreement with the
metadata. Both are recommended together.

### PKG-PYPI-010 · Enforce standard distribution file names
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Require standard distribution file names | Instance-wide. Rejects deploys whose file name does not follow the standard naming of source distributions and wheels. | off (switched on explicitly) |

- Accepted shapes (see the PyPA
  [source distribution file name](https://packaging.python.org/en/latest/specifications/source-distribution-format/#source-distribution-file-name)
  and [wheel file name](https://packaging.python.org/en/latest/specifications/binary-distribution-format/#file-name-convention)
  rules):

  ```text
  {name}-{version}-{optional parts}.tar.gz
  {distribution}-{version}-{optional parts}.whl
  ```

- Main effect: `-` is the separator between the segments, so it must not occur inside the name
  segment. Applying that rule (an illustration, not a normative example list),
  `acme_tools-1.0.tar.gz` passes and `acme-tools-1.0.tar.gz` is refused.
- Error: HTTP 403; the message says the file name breaks the naming rules.
- Scope and backward compatibility as in PKG-PYPI-009: instance-wide, existing files untouched, new
  versions or overwrites of non-conforming names refused.
- The combined behavior of both switches is unspecified beyond the rules above.

## Index and metadata

### PKG-PYPI-011 · Simple index in HTML
Priority: **P1**

Every PyPI repository serves the simple index (PEP 503) under its index URL: a page listing all
projects and, per project, a page listing the distribution files with links to download them.

- Local repositories always maintain the HTML index; they maintain the JSON form as well when it is
  enabled (PKG-PYPI-012).
- Source of the content: computed for local repositories (PKG-PYPI-013), taken from the upstream
  for remote repositories (PKG-PYPI-017), merged for virtual repositories (PKG-PYPI-019).
- Which optional attributes the system emits (hash fragments, `data-requires-python`,
  `data-yanked`, core-metadata attributes, repository-version marker) is unspecified; the standard
  is summarized in PKG-PYPI-028.

### PKG-PYPI-012 · Simple index in JSON
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Simple JSON format | Instance-wide. PyPI repositories answer with JSON index documents ([PEP 691](https://peps.python.org/pep-0691/)). | unspecified (switched on explicitly) |

- Applies to local, remote and virtual repositories; local repositories keep indexing in both HTML
  and JSON.
- Fallback: a request for JSON that cannot be served in JSON gets the HTML form instead. The
  reverse does not hold: when only JSON exists and HTML is asked for, the answer is empty; its
  status code is unspecified.
- A virtual repository answers in JSON only when every member can; if one member is HTML-only, the
  virtual repository answers in HTML.
- Turning the switch off after remote repositories cached their indexes in JSON: while such an
  upstream is unreachable, its packages are unavailable, because the HTML form cannot be fetched;
  they return when the upstream does.
- How the system chooses when a client's `Accept` header admits both forms is unspecified.
- Required role: system administrator or project administrator. UI location: administration,
  package-format settings, PyPI section, checkbox.

### PKG-PYPI-013 · Metadata calculation for local repositories
Priority: **P1**

The system derives the per-project and repository-wide index of a local repository from the stored
distributions' metadata (the properties of PKG-PYPI-007). Calculation follows indexing after
uploads (quiet period 60 s) and can be repeated on demand (PKG-PYPI-014).

- A newly uploaded file therefore appears in the index only after the quiet period plus the
  calculation; the end-to-end latency is unspecified.
- Whether delete, copy and move trigger a recalculation for PyPI is unspecified (contrast conda,
  PKG-CONDA-005 in [conda.md](conda.md), and CRAN, PKG-CRAN-007 in [cran.md](cran.md)).

### PKG-PYPI-014 · Recalculate PyPI metadata
Priority: **P1**

| | |
|---|---|
| Inputs | Repository key; optional flag to run asynchronously. |
| Behavior | Recomputes the package and repository indexes from the stored artifacts' metadata. Synchronous by default. |
| Output | HTTP 200 with a plain-text body. |
| Errors | 401, 403, 500. |
| Repository classes | Local only. |
| Authentication | HTTP Basic or a Bearer token. |
| Required privilege | Unspecified (the operation can answer 403). |

UI: the repository row action "recalculate index" (REPO-009 in
[../repositories.md](../repositories.md)).

### PKG-PYPI-015 · Project metadata in JSON
Priority: **P2**

Two read operations return a package's metadata as JSON, in the manner of PyPI's own
[JSON API](https://docs.pypi.org/api/json/) (`/pypi/<project>/json` and
`/pypi/<project>/<version>/json` on pypi.org):

| Operation | Inputs | Output | Errors |
|---|---|---|---|
| Latest-version metadata | repository key, package name | JSON object describing the latest version | 401, 403, 404, 500 |
| Version metadata | repository key, package name, version | JSON object describing that version | 401, 403, 404, 500 |

Both operations accept HTTP Basic or a Bearer token. Unspecified: the schema of the object, how
"latest" is determined, which repository classes support the operations, and the required
privilege (read is the plausible reading of the 401/403 answers).

### PKG-PYPI-016 · PyPI details in the UI
Priority: **P2**

In the artifact browser (ART-040 in [../artifacts.md](../artifacts.md)), selecting a PyPI
distribution shows a PyPI-specific tab in the item details pane (ART-041) with selected metadata of
the package. Which fields the tab shows is unspecified.

## Remote repositories

### PKG-PYPI-017 · Proxying an upstream Python index
Priority: **P1**

A PyPI remote repository fetches distributions from an upstream index on first request, stores them
and serves later requests from its cache (RMT-001 in
[../remote-repositories.md](../remote-repositories.md)).

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | RMT-004; the address the repository proxies. | unspecified (example: `https://pypi.org/`) |
| Registry URL | A second upstream address, for the index. | `https://pypi.org` |
| Index suffix | Final path segment of the upstream's simple index below the registry URL; changed only for servers that use another one, for example devpi's `+simple` (PKG-PYPI-018). | `simple` |
| Metadata retrieval cache period | RMT-018: how long cached index pages are served before the upstream is asked again. | 7,200 s |

- The registry URL normally stays at its default and is set to the upstream URL only when the
  upstream is another instance of the same kind of system. The configuration also carries two
  older optional path settings, the upstream's index path (default `simple/`)
  and its package-files path (default `packages/`); how they relate to the index suffix is
  unspecified.
- Which requests go to the upstream URL and which to the registry URL is unspecified. The field
  names suggest index pages from the registry URL and distribution files from the upstream URL.
- Index pages are metadata: they expire after the cache period and can be invalidated on demand
  (ART-050 in [../artifacts.md](../artifacts.md), which names PyPI explicitly). Distribution files
  are immutable once cached.
- Upstream indexes such as pypi.org link files on a different host. For clients to download
  through the cache, the served index must point at the remote repository; how the system rewrites
  or resolves those links is unspecified.
- Upstream credentials, proxies and timeouts follow RMT-005 to RMT-016.
- Chained remote repositories (RMT-040): when the upstream is another instance of the same kind of
  system, the registry URL must point at that instance too.
- Search is not available on remote repositories (PKG-PYPI-020).
- JSON versus HTML caching: PKG-PYPI-012.

### PKG-PYPI-018 · Custom index suffix and scoped upstream paths
Priority: **P2**

For upstream servers that do not host one index at a fixed path (for example servers that host
several indexes under `<user>/<index>/`), the remote repository can pass an extra path through to
the upstream:

1. Set both the upstream URL and the registry URL to the same root URL, for example
   `https://pyserver.example.net`.
2. Clients insert the upstream's index path between the base URL and `/simple`:

```shell
pip install acme-tools -i https://repo.example.com/pypi/pypi-remote/myorg/mypypi/simple
pip search acme-tools --index https://repo.example.com/pypi/pypi-remote/myorg/mypypi/
```

- This procedure serves upstreams with a non-default suffix. The index suffix setting of
  PKG-PYPI-017 (default `simple`) covers servers whose index ends in another segment; how the
  suffix setting and the client-supplied scoped path combine, and how either is
  validated, is unspecified.
- Whether a virtual repository can forward such scoped paths to a remote member is unspecified.

## Virtual repositories

### PKG-PYPI-019 · Merged index across members
Priority: **P1**

A PyPI virtual repository serves one index URL over its local and remote members (REPO-033 in
[../repositories.md](../repositories.md)). A typical CI setup resolves through a virtual
repository and publishes to a local one.

- The project list and the per-project file lists are merged from the members; member order and
  priority follow REPO-035 and REPO-018. The PyPI-specific merge rules (for example, the same file
  name in two members, or a project present in a local and a remote member) are unspecified.
- JSON answers only when every member can produce JSON (PKG-PYPI-012).
- The virtual metadata cache period of REPO-037 is not listed for PyPI; whether merged PyPI indexes
  are cached, and for how long, is unspecified.
- `pip search` through a virtual repository covers only its local members (PKG-PYPI-020).
- Uploads through a virtual repository: unspecified (PKG-PYPI-006).

Security: a single merged index URL replaces pip's `--extra-index-url`, with which the client
merges several indexes itself and a public package with an internal package's name can win the
version comparison (dependency confusion). The virtual repository's priority resolution (REPO-018)
is the server-side control for the same risk.

## Search

### PKG-PYPI-020 · `pip search`
Priority: **P2**

`pip search` works against local and virtual PyPI repositories:

```shell
pip search acme --index https://repo.example.com/pypi/pypi-virtual/
```

- The index must be given explicitly as the base URL, without `/simple`; pip does not use the
  `index-url` from its configuration file for this command.
- Remote repositories, including chained remote repositories, are not searched. In a virtual
  repository, remote members are skipped and only local members contribute results.
- The output lists each hit's name followed by a short text; the matching rules (name only,
  summary, prefix or substring) are unspecified.
- Protocol (from pip's implementation): pip calls the XML-RPC method `search` at the given index
  URL, with a name and a summary criterion combined by `or`.
- Remote repositories are no longer searched because pip has deprecated the command. In addition,
  pypi.org has disabled its XML-RPC search, so a pass-through to that upstream would fail anyway.

### PKG-PYPI-021 · Property and package search
Priority: **P1**

The name, version and summary properties (PKG-PYPI-007) make PyPI packages findable through the
property search (SRCH-007 in [../search-and-query.md](../search-and-query.md)), the package search
of the UI (SRCH-050, SRCH-051) and the query language (SRCH-023). Files whose properties were
neither sent nor extracted do not appear in these searches.

## Clients

### PKG-PYPI-022 · pip
Priority: **P1**

pip installs only. Configuration file:

```ini
[global]
index-url = https://ci-user:${TOKEN}@repo.example.com/pypi/pypi-virtual/simple
```

| Aspect | Value |
|---|---|
| Configuration file | `~/.pip/pip.conf`; on Windows `%APPDATA%\pip\pip.ini`, with the legacy `%HOME%\pip\pip.ini` also read |
| Per command | `pip install acme-tools -i https://repo.example.com/pypi/pypi-local/simple` |
| Requirements file | first line `--index-url https://repo.example.com/pypi/pypi-local/simple`, then the pinned requirements |
| Install | `pip3 install acme-tools` |

- pip reuses files from its local cache only when the download URL belongs to a trusted host with a
  valid TLS certificate. Without a valid certificate on the system, every install reaches the
  server even for locally cached files.
- On Windows some Python and pip environment variables must be set; which ones is unspecified.
- A virtualenv is recommended to isolate the installation.

### PKG-PYPI-023 · Poetry
Priority: **P1**

Poetry installs and publishes. Installing and publishing use separate configuration entries, each
with its own credentials:

```shell
# publishing target (upload URL, no /simple)
poetry config repositories.internal-publish https://repo.example.com/pypi/pypi-local
poetry config http-basic.internal-publish ci-user ${TOKEN}

# install source (index URL); "source add" writes a [[tool.poetry.source]] entry into pyproject.toml
poetry config http-basic.internal ci-user ${TOKEN}
poetry source add internal https://repo.example.com/pypi/pypi-virtual/simple

poetry add --source internal acme-tools
poetry publish --repository internal-publish --dist-dir dist --no-interaction
```

- `--no-interaction` suppresses Poetry's confirmation prompt when built files already exist; CI
  without a terminal needs it.
- Minimum version 1.2.0.

### PKG-PYPI-024 · Twine and setuptools
Priority: **P1**

Both read `.pypirc`, whose `repository` is the upload URL (no `/simple`):

```ini
[distutils]
index-servers =
    internal

[internal]
repository = https://repo.example.com/pypi/pypi-local
username = ci-user
password = ${TOKEN}
```

| Aspect | Value |
|---|---|
| File location | `~/.pypirc`; on Windows `%USERPROFILE%\.pypirc` |
| Twine | `twine upload --repository internal dist/*`; `twine check dist/*` validates locally without contacting the server |
| setuptools | `python setup.py sdist upload -r internal`, `… bdist_wheel upload -r internal`, or both in one call |
| `HOME` | setuptools finds `.pypirc` through the `HOME` variable; build servers and Windows must set it |

- Without `-r`, setuptools and distutils upload to `https://pypi.org/pypi`. Security: a forgotten
  `-r` publishes an internal package to the public index.
- `setup.py register` must be omitted (PKG-PYPI-006).
- Twine does not use `.netrc` credentials, while setuptools uploads do.
- Minimum Twine version 3.3.0.

### PKG-PYPI-025 · uv
Priority: **P1**

uv installs and publishes natively.

```toml
# ~/.config/uv/uv.toml   (Windows: %APPDATA%\uv\uv.toml)
[[index]]
name = "internal"
url = "https://repo.example.com/pypi/pypi-virtual/simple"
publish-url = "https://repo.example.com/pypi/pypi-local"
default = true
```

| Aspect | Value |
|---|---|
| Read credentials | `uv auth login https://repo.example.com/pypi/pypi-virtual --username ci-user --password ${TOKEN}`, or a `.netrc` entry `machine repo.example.com` (Windows: `%USERPROFILE%\_netrc`) |
| Publish credentials | `UV_PUBLISH_USERNAME` and `UV_PUBLISH_PASSWORD`, typically a token with deploy permission |
| Install | `uv add acme-tools` (project interface) or `uv pip install acme-tools` (pip-compatible interface) |
| Publish | `uv publish dist/* --index internal` |
| Minimum version | 0.8.15 |

- Read credentials alone are not enough to publish; the publish variables are required as well.
- Open: `url` and `publish-url` either point at the same repository, or, as shown above and as the
  companion CLI recommends, at a virtual repository for the index and a local repository for
  publishing; the spec decides.

### PKG-PYPI-026 · Client setup instructions in the UI
Priority: **P1**

The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) generates, for a
chosen PyPI repository, ready-to-paste snippets with the system's URL, the repository and a token
filled in. It offers two kinds of snippet, the pip configuration and the `setup.py`/`.pypirc`
configuration, and for each uv step (credentials, publish variables, `uv.toml` index) the snippet
can be copied from the dialog as well. Whether it also offers
Poetry and Twine snippets is unspecified. It is reachable by selecting the repository in the
artifact browser (and from the user menu, UI-021).

### PKG-PYPI-027 · Build metadata through a companion CLI wrapper
Priority: **P1**

A companion command-line tool wraps pip, Pipenv, Poetry, uv and Twine. It resolves and publishes
through the system and records a build metadata record (dependencies with checksums, published
files) that is uploaded afterwards (see [../builds-and-ci.md](../builds-and-ci.md)).

<details>
<summary>Wrapper behavior per client</summary>

| Aspect | pip | Pipenv | Poetry | uv | Twine |
|---|---|---|---|---|---|
| Project configuration | server and repository for resolving, server and repository for deploying, optional "global" scope (default off); stored per project; required before the first install | same as pip | server and repository for resolving only; required in wrapped mode | no configuration step; reads the indexes from `pyproject.toml` | none of its own; reads the deploy repository from the pip configuration, else from the Pipenv one |
| Integrated commands | `install` and every other pip command | `install`, `lock`, `sync` and others | `install`, `add`, `update`, `lock`, `publish` and others | `sync`, `install`, `lock`, `add`, `remove`, `run`, `build`, `publish` | `upload`, `check` |
| Publishing | not supported; use Twine | — | `publish --repository <local-repo>` | `publish` to the publish URL of a local repository | `upload` to the configured deploy repository |
| Wrapped versus pass-through | — | — | wrapped (default): injects a source entry into `pyproject.toml` and may run `poetry update`, changing `poetry.lock`; pass-through (switched on by an environment variable): uses Poetry's own configuration, leaves the lock file alone, still collects metadata | always pass-through: uv's own configuration and credential mechanisms | — |
| Credentials | from the CLI's server configuration | same | same | injects uv's per-index credential variables (`UV_INDEX_<NAME>_USERNAME` / `_PASSWORD`) for indexes on the configured host, unless already set, embedded in the URL or present in `.netrc`; then disables uv's keyring provider | from the CLI's server configuration |
| What is recorded | only packages installed during the wrapped run | same | same | dependencies for dependency commands; module ID only for `build`; published files for `publish` | uploaded files |
| Clean recording | use a fresh virtual environment, or reinstall with `--no-cache-dir --force-reinstall` | remove the environment (`pipenv --rm`) and reinstall | — | — | — |

uv specifics:

- Dependency checksums: SHA-256 from `uv.lock`; SHA-1 and MD5 are looked up on the system when the
  first index's host matches the configured server (or the index given by `UV_DEFAULT_INDEX` /
  `UV_INDEX_URL` when `pyproject.toml` names none). A host mismatch yields a warning and only
  SHA-256.
- Recorded identifiers: dependencies as `name:version` with type `whl` or `tar.gz` and no scopes;
  published files with type `wheel` or `sdist`; the default module ID is
  `<project name>:<project version>` from `pyproject.toml`.
- Publish credentials, highest priority first: `UV_PUBLISH_TOKEN`; `UV_PUBLISH_USERNAME` and
  `UV_PUBLISH_PASSWORD`; credentials in the publish URL; `.netrc` for the publish host; credentials
  of an index on the same host; the CLI's own server credentials when the host matches.
- The wrapper reads a publish URL from a key in the `[tool.uv]` table that uv itself ignores, and
  appends it as `--publish-url` unless one is given; a command-line `--publish-url` wins.
- Editable workspace packages are not recorded as dependencies.

Common:

- Build name and number must be given together; module and project are optional.
- pip needs Python 3.6 or later and a `pip` executable on the path (a `pip3`-only system needs a
  link or a virtual environment).

</details>

Server operations the wrapper relies on:

| Operation | Input | Output | Privilege |
|---|---|---|---|
| Basic authentication with user name and token | user name and token | authenticated request | — (reference tokens need the user name, PKG-PYPI-005) |
| Checksum lookup for dependencies | the resolved file, through the query language (SRCH-023) | SHA-1 and MD5 of the stored file | read |
| Build properties on published files | uploaded files | build name, build number and build timestamp properties attached (ART-023) | annotate |
| Build upload | build metadata record | stored build | deploy on the build repository; details in [../builds-and-ci.md](../builds-and-ci.md) |

## Protocol reference

This section summarizes, from the Python packaging specifications, what a PyPI-compatible index
serves. It is the yardstick for client compatibility; which optional parts the described system
implements is unspecified unless an entry above says so. Paths are relative to the repository base
URL (PKG-PYPI-002). Sources: [PEP 503](https://peps.python.org/pep-0503/),
[PEP 592](https://peps.python.org/pep-0592/) (yanked files),
[PEP 629](https://peps.python.org/pep-0629/) (API version marker),
[PEP 658](https://peps.python.org/pep-0658/) (separate core metadata),
[PEP 691](https://peps.python.org/pep-0691/) (JSON form),
[PEP 700](https://peps.python.org/pep-0700/) (versions, size, upload time),
[PEP 714](https://peps.python.org/pep-0714/) (renamed metadata key),
the [name normalization specification](https://packaging.python.org/en/latest/specifications/name-normalization/),
PyPI's [upload API](https://docs.pypi.org/api/upload/), and the
[pip](https://github.com/pypa/pip), [Twine](https://github.com/pypa/twine) and
[uv](https://github.com/astral-sh/uv) sources.

### PKG-PYPI-028 · Simple repository API
Priority: **P1**

<details>
<summary>Endpoints</summary>

| Request | Answer |
|---|---|
| `GET simple/` | Project list: one link per project, link text the project name, target the project page. |
| `GET simple/<normalized-name>/` | Project page: one link per distribution file, link text the file name, target the file URL. |
| `GET <file URL>` | The distribution file. |
| `GET <file URL>.metadata` | The file's core metadata (the wheel's `METADATA`), when the project page announces it (PEP 658). |
| `GET <file URL>.asc` | Detached signature, when announced (`data-gpg-sig`). |
| Any index URL without trailing `/` | Should redirect to the URL with `/` (PEP 503). |

</details>

<details>
<summary>HTML form (PEP 503, 592, 629, 658, 714)</summary>

| Element | Meaning |
|---|---|
| `<meta name="pypi:repository-version" content="1.x">` | API version; clients refuse a newer major version than they know and warn on a newer minor one. |
| `#<hashname>=<hex>` fragment on the file link | File hash, usually `sha256`; pip verifies it after download. |
| `data-requires-python="…"` | The file's Requires-Python, HTML-escaped (`&lt;`, `&gt;`); pip skips files whose constraint the interpreter does not satisfy. |
| `data-yanked` / `data-yanked="reason"` | File is yanked; installers ignore it unless pinned exactly with `==` or `===`. |
| `data-core-metadata="true"` or `="sha256=<hex>"` | A metadata file exists at `<file URL>.metadata`, optionally with its hash (PEP 714 name). |
| `data-dist-info-metadata` | The PEP 658 name of the same attribute; servers may emit it alongside for older clients, newer clients read `data-core-metadata`. |
| `data-gpg-sig="true"/"false"` | Whether a signature exists at `<file URL>.asc`. |

</details>

<details>
<summary>JSON form (PEP 691, 700, 714)</summary>

| Aspect | Rule |
|---|---|
| Content types | `application/vnd.pypi.simple.v1+json`; `application/vnd.pypi.simple.v1+html`; legacy `text/html` (same as the v1 HTML form). |
| Negotiation | By the `Accept` header with quality values (pip sends JSON first, then v1 HTML, then `text/html`). The server should answer with `Vary: Accept`; with no acceptable type it may answer 406 or fall back to `text/html`. An optional `format` URL parameter may select the type too. |
| Project list | `{"meta": {"api-version": "1.x"}, "projects": [{"name": "<name>"}, …]}` |
| Project page | `{"meta": …, "name": "<normalized name>", "versions": [ … ], "files": [ … ]}`; `versions` from API version 1.1. |
| File object | `filename`; `url` (absolute or relative to the page URL); `hashes` (object, may be empty but present); optional `requires-python`; `core-metadata` (`true`, or a hash object; `dist-info-metadata` is the older name); `gpg-sig`; `yanked` (`false`, `true` or a reason string); from API version 1.1 `size` (required) and `upload-time` (optional, UTC). |

</details>

Name handling: clients request the normalized project name (PKG-PYPI-003); the HTML project links
should point at normalized URLs.

### PKG-PYPI-029 · Upload API
Priority: **P1**

Clients upload one file per request with `POST` to the upload URL (PKG-PYPI-002) as
`multipart/form-data`, authenticated with HTTP Basic (PKG-PYPI-005).

<details>
<summary>Form fields (PyPI upload API)</summary>

| Field | Content |
|---|---|
| `:action` | `file_upload` |
| `protocol_version` | `1` |
| `content` | the distribution file |
| `filetype` | `sdist` or `bdist_wheel` (older clients also send other binary types) |
| `pyversion` | `source` for source distributions, the Python tag for wheels |
| `name`, `version`, `metadata_version` | from the core metadata |
| `md5_digest`, `sha256_digest`, `blake2_256_digest` | digests of the file, hex |
| other core-metadata fields | lower-cased, `-` replaced by `_` (for example `summary`, `requires_python`, `requires_dist`, `classifiers`), multi-valued fields repeated |
| `attestations` | optional, sent by newer clients (PEP 740) |

</details>

Client behavior that constrains the server:

- Twine does not follow redirects on upload; it aborts when the upload URL answers with a redirect.
  The upload URL must therefore be accepted exactly as clients write it, with or without a trailing
  slash.
- Twine's `--skip-existing` recognizes "already uploaded" only from specific answers, among them
  any 409 and a 400 whose reason or body contains "already exist"; any other answer makes the run
  fail.
- The server should verify the sent digests against the received file and reject a mismatch.

## Notes for implementers

- Serve the index URL with and without the trailing slash, and build every absolute URL (redirects,
  file links) from the externally visible base URL, honoring reverse-proxy forwarding headers.
- Never redirect the upload URL: Twine aborts on a redirect, and Poetry and uv users commonly
  configure it without a trailing slash.
- Clients request `simple/<normalized-name>/`; a project uploaded as `Acme.Tools` must be found under
  `simple/acme-tools/`, even though the system keeps dot and underscore spellings as separate
  packages (PKG-PYPI-003).
- Emit hash fragments (JSON: `hashes`) on every file link; pip verifies them, and uv and Poetry
  write them into lock files, so the hash for a given file name must never change.
- Answer a duplicate upload with 409 (or 400 with "already exists" in the message) so
  `twine upload --skip-existing` and retried CI jobs behave.
- Serve `data-requires-python` and yanked markers from the stored metadata; without them pip may
  pick a release that cannot be installed on the running interpreter.
- If PEP 658 metadata is announced, the `.metadata` file and its hash must match the wheel exactly;
  pip and uv resolve from it without downloading the wheel.
- Keep `Vary: Accept` on index pages that can be served as HTML or JSON, so caches do not mix the
  two forms.
- A literal comparison of metadata name and file name (PKG-PYPI-009) rejects standard wheels whose
  metadata name contains `-`; compare normalized names if the check is meant to be compatible.
- In a virtual repository, a project page must list files from all eligible members consistently
  with the hashes each member serves; mixing a remote file name with a local file of different
  content breaks lock files.
</content>
</invoke>
