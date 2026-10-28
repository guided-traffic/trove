# CRAN package format

This page catalogs what a repository manager does to act as a CRAN-style repository for the R
language: hosting source and binary R packages in local repositories, proxying and caching an
upstream CRAN mirror in remote repositories, aggregating both behind one repository URL in virtual
repositories, computing the package index, archiving older versions, and configuring the R client.
The repository layout that R expects is defined by R itself (see
[R Installation and Administration, "Setting up a package repository"](https://cran.r-project.org/doc/manuals/r-release/R-admin.html#Setting-up-a-package-repository)
and the [CRAN documentation](https://cran.r-project.org/other-docs.html)); entries that rely on
them link them. Repository classes in general are
described in [../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/cran/<repo-key>` stands for the *repository URL*
that R uses as a repository root (the path layout is the implementation's choice),
`cran-local`, `cran-remote` and `cran-virtual` are example repository keys, `ci-user` is an example
user name and `${TOKEN}` an access token.

## Repositories and addressing

### PKG-CRAN-001 · CRAN repositories in all three repository classes
Priority: **P2**

CRAN is offered as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)):

- local repositories hold private source and binary packages with fine-grained access control; the
  system computes their index (PKG-CRAN-007) and archives older versions (PKG-CRAN-009);
- remote repositories proxy a CRAN mirror or another instance and cache downloaded packages
  (PKG-CRAN-012);
- virtual repositories aggregate local and remote members under one URL for resolving and, with a
  default deployment repository, for deploying (PKG-CRAN-013, PKG-CRAN-006).

Creating a repository requires the system administrator or a project administrator role; for
other users the create action is not offered. UI location: administration, repositories, create
repository, then the class and the CRAN format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-CRAN-002 · Repository URL and directory layout
Priority: **P2**

A CRAN repository is used by R as a repository root (`repos=` in R). Below it, packages sit in R's
standard directories:

| Kind | Path below the repository root |
|---|---|
| Source packages | `src/contrib/<package>_<version>.tar.gz` |
| Binary packages | `bin/<distribution>/contrib/<R version>/<package file>` |

- Only files deployed to these paths are recognized as CRAN packages and indexed; a file deployed
  elsewhere is stored but not indexed (PKG-CRAN-004, PKG-CRAN-005).
- Installs use the repository's plain file address as the R repository root, while deploys go
  through format-specific operations (PKG-CRAN-004).
- The distribution values for binaries are `linux`, `mac` and `windows`. R itself
  requests `bin/windows/contrib/<x.y>/` and `bin/macosx/…/contrib/<x.y>/`
  ([`contrib.url`](https://stat.ethz.ch/R-manual/R-devel/library/utils/html/contrib.url.html)); how
  the system maps `mac` to R's `macosx` directories, and where Linux binaries are served, is
  unspecified.

### PKG-CRAN-003 · Client authentication
Priority: **P2**

Client authentication comes in two forms:

- HTTP Basic credentials embedded in the repository URL, with a password or a reference token
  (TOK-004, TOK-034 in [../access-tokens.md](../access-tokens.md)) as the secret:

  ```r
  options(repos = c(internal = "https://ci-user:TOKEN@repo.example.com/cran/cran-virtual"))
  ```

- A Bearer token passed through the `headers` argument of `install.packages` (TOK-033):

  ```r
  install.packages("ggplot2",
                   repos = "https://repo.example.com/cran/cran-virtual",
                   headers = c(Authorization = "Bearer TOKEN"))
  ```

Open: either the R client supports only the URL-embedded form, or both forms; the system should
accept both. Anonymous reads work when anonymous access is on (IAM-005 in
[../identity-and-access.md](../identity-and-access.md)). The R version from which `headers` is
honored by `install.packages`, and whether it is also passed to `available.packages`, is an
unspecified client detail.

## Deploying packages

### PKG-CRAN-004 · Deploy a source package
Priority: **P2**

| Way | Request |
|---|---|
| Format deploy operation | `POST` of the package file to the source-package deploy operation of the repository; the system places it under `src/contrib` |
| Plain file upload | `PUT` of the file to `src/contrib/<package>_<version>.tar.gz` |
| UI | artifact browser, deploy (ART-006, UI-013 in [../ui-inventory.md](../ui-inventory.md)): the system recognizes a source package and pre-fills the target path; the path must not be changed |

```shell
curl -u ci-user:${TOKEN} -T ./acme_1.0.0.tar.gz \
  "https://repo.example.com/cran/cran-local/src/contrib/acme_1.0.0.tar.gz"
```

- Changing the target path away from `src/contrib` makes the system skip indexing: the file is not
  identified as a CRAN package.
- Required permission: deploy on the repository.
- Output and error codes of the deploy operation: unspecified.

### PKG-CRAN-005 · Deploy a binary package
Priority: **P2**

| Way | Request |
|---|---|
| Format deploy operation | `POST` of the package file with two mandatory parameters: distribution (operating system, for example `linux`, `mac`, `windows`) and R version |
| Plain file upload | `PUT` of the file to `bin/<distribution>/contrib/<R version>/<file>` |
| UI | deploy dialog with a CRAN section in which distribution and R version are mandatory; the system builds the target path from them and shows the final path after the deploy |

```shell
curl -u ci-user:${TOKEN} -T ./acme_1.0.0.tgz \
  "https://repo.example.com/cran/cran-local/bin/mac/contrib/4.4/acme_1.0.0.tgz"
```

- The UI accepts drag and drop and detects whether a file is a source or a binary package.
- A file put anywhere else is not indexed.
- Unspecified: the accepted value set for distribution and R version (for example `4.4` versus
  `4.4.1`), and the file extensions accepted for binaries.

### PKG-CRAN-006 · Deploy through a virtual repository
Priority: **P2**

Deploys to a virtual CRAN repository (by UI or by the deploy operations) land in its default
deployment repository (REPO-038 in [../repositories.md](../repositories.md)), which must be set;
without it the deploy fails (status unspecified).

## Index and metadata

### PKG-CRAN-007 · Automatic index calculation
Priority: **P2**

The system recalculates the CRAN index of a local repository automatically whenever a CRAN package
is added, removed, copied or moved.

- Asynchronous, after the triggering action completes; it can take up to about 30 s, depending on
  system load.
- Only packages at the standard paths (PKG-CRAN-002) are indexed.
- The index files R reads are `PACKAGES`, `PACKAGES.gz` and `PACKAGES.rds` in each `contrib`
  directory (R's convention, PKG-CRAN-016). Which files exactly the system writes for the calculated
  "Packages and Release metadata" is unspecified.

### PKG-CRAN-008 · Recalculate CRAN metadata
Priority: **P2**

| | |
|---|---|
| Inputs | Repository key; optional flag to run asynchronously. |
| Behavior | Recomputes the package index of the repository from the CRAN packages in it. Synchronous by default. |
| Output | HTTP 200 with a plain-text body. |
| Errors | 401, 403, 500. |
| Repository classes | Local only. |
| Required privilege | Unspecified for the operation; the UI action requires the system administrator role. |

UI: artifact browser, context menu of the repository node, "recalculate index" (asynchronous); the
repository row action of REPO-009 in [../repositories.md](../repositories.md).

### PKG-CRAN-009 · Archived versions in the standard location
Priority: **P2**

The system manages versions by archiving older versions of a package. An instance-wide switch makes
local CRAN repositories store these archives in the hierarchy the official CRAN specification
prescribes.

| Setting | Meaning | Default |
|---|---|---|
| Standard archive location | Instance-wide. Stores archived package versions of local CRAN repositories in the official CRAN hierarchy. Takes effect after a restart. | unspecified (a property to set) |

- CRAN's own convention places older source versions under
  `src/contrib/Archive/<package>/<package>_<version>.tar.gz`, which tools such as
  `remotes::install_version` rely on. The paths the switch uses are unspecified, as is where
  archives go without it ("the wrong location").
- An instance-level configuration property, not a UI or API setting.
- Existing archives are moved with PKG-CRAN-010.

### PKG-CRAN-010 · Move archives to the standard location
Priority: **P2**

| | |
|---|---|
| Inputs | Repository key. |
| Behavior | Moves archives of the repository from the old location to the standard one. Works only while the switch of PKG-CRAN-009 is on. |
| HTTP form | A read-style (`GET`) request, although it changes content. |
| Output | HTTP 200 with a JSON object; its fields are unspecified. |
| Errors | 401, 403, 500. |
| Required privilege | Unspecified. |

Whether the move is synchronous, idempotent, and recalculates the index afterwards is unspecified.

### PKG-CRAN-011 · Package information in the UI
Priority: **P2**

In the artifact browser (ART-040 in [../artifacts.md](../artifacts.md)), selecting a CRAN package
shows a CRAN-specific tab in the item details pane (ART-041) with selected package metadata. The
tab exists at least for packages in virtual repositories; the fields shown, and whether they are
read from the package's `DESCRIPTION` file, are unspecified. Package cleanup policies treat
`.tar.gz` as the CRAN lead file (RET-009 in [../retention-and-cleanup.md](../retention-and-cleanup.md)).

## Remote repositories

### PKG-CRAN-012 · Proxying an upstream CRAN mirror
Priority: **P2**

A CRAN remote repository proxies a CRAN mirror or another instance and caches the packages it
downloads (RMT-001 in [../remote-repositories.md](../remote-repositories.md)).

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | RMT-004; the mirror root to proxy. | unspecified |
| Metadata retrieval cache period | RMT-018; the index files are kept and renewed according to it. | 7,200 s |

Unspecified: whether binary directories and the `Archive` directory are proxied, and how upstream
index entries are mapped.

## Virtual repositories

### PKG-CRAN-013 · Aggregated index in virtual repositories
Priority: **P2**

A CRAN virtual repository resolves locally hosted and proxied packages through one URL, with an
index aggregated from its members.

| Setting | Meaning | Default |
|---|---|---|
| Metadata cache period | REPO-037 in [../repositories.md](../repositories.md): how long the merged index is cached before members are checked again. | open: either 7,200 s or 600 s; the spec decides |

Merge rules for the same package version in several members are unspecified. Deploying through the
virtual repository: PKG-CRAN-006.

## Clients

### PKG-CRAN-014 · R client usage
Priority: **P2**

| Task | R call |
|---|---|
| Install by name | `install.packages("acme", repos = "https://repo.example.com/cran/cran-virtual")` |
| Choose a different configured repository | `setRepositories()` |
| List available packages | `available.packages()` |
| Install by selection from a list (interactive clients) | `install.packages()` |

- Writing `repo=` works too: R's partial argument matching resolves it to `repos`.
- The command-line flow begins with the client setup instructions (PKG-CRAN-015); what
  they configure (for example the session's `repos` option) is unspecified.

### PKG-CRAN-015 · Client setup instructions in the UI
Priority: **P2**

The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) offers CRAN
instructions for the chosen repository with the system's URL and credentials filled in; the
snippets themselves are unspecified.

## Protocol reference

This section summarizes, from R's documentation, which paths R requests from a repository root
(R Installation and Administration, "Setting up a package repository";
[`contrib.url`](https://stat.ethz.ch/R-manual/R-devel/library/utils/html/contrib.url.html);
[`write_PACKAGES`](https://stat.ethz.ch/R-manual/R-devel/library/tools/html/writePACKAGES.html)).
Which of these the system serves beyond PKG-CRAN-002 is unspecified.

### PKG-CRAN-016 · Repository paths R requests
Priority: **P2**

<details>
<summary>Paths below the repository root</summary>

| Path | Content |
|---|---|
| `src/contrib/PACKAGES.rds`, `PACKAGES.gz`, `PACKAGES` | index of source packages; R tries the `.rds` form first, then the compressed and plain DCF forms |
| `src/contrib/<package>_<version>.tar.gz` | source package |
| `src/contrib/Archive/<package>/<package>_<version>.tar.gz` | older source versions (CRAN convention) |
| `bin/windows/contrib/<major.minor>/PACKAGES*` and `<package>_<version>.zip` | Windows binaries for one R minor version |
| `bin/macosx/…/contrib/<major.minor>/PACKAGES*` and `<package>_<version>.tgz` | macOS binaries; recent R versions add a platform segment (for example for arm64) |

Each `PACKAGES` entry is a DCF record. `Package` and `Version` identify it; R's own index writer
also emits the dependency fields (`Depends`, `Imports`, `LinkingTo`, `Suggests`), `License` and
`MD5sum`, and `available.packages()` uses them to drop entries whose dependencies or R version
constraints do not fit the running R.

</details>

## Notes for implementers

- Serve `PACKAGES` together with `PACKAGES.gz` and `PACKAGES.rds` and keep them consistent; R uses
  whichever it finds first, so a stale `.rds` hides new uploads.
- Without an index in a `contrib` directory, R only warns that it cannot read the repository index
  and then reports every package there as not available; return an empty index instead of 404 for
  known but empty directories, so the real cause is not hidden.
- Index only files at the standard paths, and say so in the deploy response; silently storing a
  misplaced file is the most common source of "package not found".
- Binaries are bound to an R minor version; a binary indexed under the wrong `<major.minor>`
  installs but fails to load.
- Archive older versions under `src/contrib/Archive/<package>/` and keep only the newest version in
  the main index; version-pinning tools read the archive.
- R sends Basic credentials from the URL and custom headers only when the caller provides them;
  make anonymous read (if enabled) and credentialed read return the same index.
- The archive move operation changes content behind a `GET`; protect it against caching proxies and
  link prefetchers.
</content>
</invoke>
