# VCS archive format

This page catalogs the VCS format: remote repositories that proxy and cache the source archives a
Git hosting service generates for tags, branches, commits and releases (tarballs and zip files),
list a hosted repository's tags and branches, and serve single files from a tag or branch. It lets
builds consume source snapshots from GitHub, Bitbucket and similar services through the repository
manager, with caching for offline resilience and the system's own access control. There is no
client protocol of a package manager behind it; consumers are scripts and build tools that fetch
archives by URL. Remote repositories in general are described in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `vcs-github` is an example repository key, `<owner>` is the user or
organization on the Git host, `<repo>` the hosted repository's name and `<ref>` a tag, branch or
commit. The URL shape of the operations is the implementation's choice.

## Repository

### PKG-VCS-001 · VCS remote repositories
Priority: **P2**

The VCS format exists for remote repositories only; local, virtual and federated VCS repositories
are not supported (REPO-006 in [../repositories.md](../repositories.md)).

Capabilities:

- list all tags and all branches of a hosted repository (PKG-VCS-005);
- download a tag, branch, commit or release as `tar.gz` or `zip` (PKG-VCS-007 to PKG-VCS-010) and a
  single file from a tag or branch (PKG-VCS-011);
- cache downloaded archives on demand, so they stay available when the network or the Git host is
  down;
- control access per repository through the permission model, for example per project or team
  (IAM-019 and IAM-023 in [../identity-and-access.md](../identity-and-access.md)).

Behavior:

- Archives are cached on first request; cached archives can be removed from the cache (RMT-003 in
  [../remote-repositories.md](../remote-repositories.md)).
- Files cannot be deployed into a VCS repository manually.
- One VCS remote repository typically serves one Git host (for example one for `github.com`, one
  for `bitbucket.org`).
- The general remote setting that skips the HEAD request before caching (RMT-024 in
  [../remote-repositories.md](../remote-repositories.md)) is not supported for VCS remotes, and
  priority resolution (RMT-037) is not offered for them.

Creation: system administrator or project administrator. UI location: administration,
repositories, create repository, remote, VCS format; the form asks for the repository key, the
upstream URL (for GitHub `https://github.com` or the GitHub Enterprise Server URL) and, in a VCS
settings section, the Git provider (PKG-VCS-002).

### PKG-VCS-002 · Git provider selection
Priority: **P2**

The provider tells the repository how to build archive and listing requests for the upstream.

| Setting | Meaning | Default |
|---|---|---|
| Git provider | Upstream type: see the list below | unspecified |
| Upstream URL | Base URL of the Git host, for example `https://github.com` | unspecified |

| Provider | For | Example repository URL on the host |
|---|---|---|
| GitHub (cloud) | `https://github.com` | `https://github.com/myorg/myrepo.git` |
| GitHub Enterprise Server | Self-hosted GitHub | `https://git.example.com/myorg/myrepo.git` |
| Bitbucket Cloud | `https://bitbucket.org` | `https://bitbucket.org/myorg/myrepo.git` |
| Bitbucket Server / Data Center | Self-managed Bitbucket Server or Data Center | `https://bitbucket.example.com/scm/myproj/myrepo.git` |
| Bitbucket Server before 5.1 (Stash) | Older Atlassian Stash installations | `https://stash.example.com/scm/myproj/myrepo.git` |
| Another instance of this system | A VCS repository on another instance, as a chained remote (RMT-040 in [../remote-repositories.md](../remote-repositories.md)) | `https://other.example.com/<repo-key>` |
| Custom | Any other Git host; upstream URL and credentials are entered by hand and download paths are configured by template (PKG-VCS-003) | — |

The remote-repository settings reference describes the same choice with older labels: GitHub,
Bitbucket, private Bitbucket (Stash), private Bitbucket before 5.1.0, another instance of this
system (for a chained remote) and custom. Whether both lists name the same stored values is
unspecified.

When the upstream is another instance of this system, its URL must point at that instance's VCS
endpoint, which carries a format-specific path prefix, not at the plain repository URL (the same
rule as for chained remotes of other API-style formats).

Limits: only two-level repository paths (`<owner>/<repo>`) are supported, for every provider
including custom; nested group paths such as GitLab's `group/subgroup/repo` are not (but see
PKG-VCS-012). The provider list has no GitLab entry.

### PKG-VCS-003 · Download path template for custom providers
Priority: **P2**

For a Git host that is not in the provider list, the administrator supplies the path at which the
host serves archive downloads, relative to the upstream URL, as a template with four placeholders:

| Placeholder | Replaced by |
|---|---|
| `{0}` | User or organization name (`<owner>`) |
| `{1}` | Repository name (`<repo>`) |
| `{2}` | Branch or tag name (`<ref>`) |
| `{3}` | File extension of the archive to download (`tar.gz` or `zip`) |

Example: GitHub serves archives at `https://github.com/<owner>/<repo>/archive/<ref>.<ext>`, so the
template for an equivalent custom host is `{0}/{1}/archive/{2}.{3}`.

| Setting | Meaning | Default |
|---|---|---|
| Download path template | Template above; shown for the custom provider | unspecified |

Limits: the template has no placeholder for additional path segments, so nested repository paths
cannot be expressed. How tag and branch listings are obtained from a custom host is unspecified.

### PKG-VCS-004 · Cache layout of VCS archives
Priority: **P2**

A VCS repository needs a hierarchical layout of the cached archives so that layout-based functions
work: version search (SRCH-016 in [../search-and-query.md](../search-and-query.md)) and version
cleanup (REPO-052 in [../repositories.md](../repositories.md)). The system ships a built-in VCS
layout that works for both GitHub and Bitbucket; a custom layout may be defined instead (REPO-050).

Built-in VCS layout, in the descriptive notation of REPO-048:

| Part | Value |
|---|---|
| Artifact path pattern | `<org-path>/<module>/<refs>/<base-rev>/<module>-<base-rev>(-<file-integ-rev>)(-<classifier>).<ext>`, where `<refs>` is a custom token matching `tags` or `branches` |
| Distinct descriptor path | none |
| Folder integration revision expression | `.*` |
| File integration revision expression | `[a-zA-Z0-9]{40}` (a full commit hash) |

Consequences:

- An archive of a tag is a release version (`<base-rev>` is the tag name).
- An archive of a branch is an integration version: the base revision is the branch name and the
  file integration revision is the commit hash, so a version search reports it as, for example,
  `master-062b5267d0a3538f1f6dee3df16da536b73061ea` flagged as integration, next to `2.0.3` flagged
  as release.
- Version cleanup detects previously cached versions of a module by this layout.

| Setting | Meaning | Default |
|---|---|---|
| Repository layout | Layout of the cache | the built-in VCS layout (offered for VCS repositories; whether it is preselected is unspecified) |
| Maximum unique snapshots | Upper bound on stored unique snapshots of one artifact; when it is exceeded, older ones are removed (RMT-036 in [../remote-repositories.md](../remote-repositories.md)). Listed among the VCS-specific remote settings | 0 (no limit, no cleanup) |

How "snapshot" maps onto branch archives (one per commit hash under the built-in layout) is
unspecified; the natural reading is that each cached commit archive of a branch counts as one.

Limits: the layout of a remote repository cannot be changed after creation; define it first.

## Operations

All operations below act on a VCS remote repository, take the repository key, the owner and the
repository name as inputs, and answer `401` for invalid credentials, `403` without permission and
`500` on internal errors; downloads also answer `404` when the ref or file does not exist. The
operation reference accepts HTTP Basic, bearer token and API-key authentication.

Required privilege: unspecified in the operation reference; read on the repository is the natural
mapping. Whether filling the cache needs more than read is governed by IAM-023 in
[../identity-and-access.md](../identity-and-access.md).

### PKG-VCS-005 · List tags and list branches
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| List tags | Repository key, owner, repository name | JSON array; each entry has the ref name, the commit identifier, and a flag telling a branch from a tag |
| List branches | Same | Same entry shape |

Both work in a repository or a subgroup repository; how a subgroup path is passed is unspecified
(see PKG-VCS-012).

### PKG-VCS-006 · Download the Git references file
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Get references file | Repository key, owner, repository name | The hosted repository's Git references file, returned as a string |

The format of the file (for example a smart-HTTP `info/refs` advertisement or a plain ref list) is
unspecified.

### PKG-VCS-007 · Download a tag archive
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Download tag | Repository key, owner, repository name, tag name; optional archive format `tar.gz` or `zip` | The complete tree of the tag as an archive |
| Download tag, short form | Repository key, owner, tag name; the owner is used as the repository name | Same |

| Setting / parameter | Meaning | Default |
|---|---|---|
| Archive format | `tar.gz` or `zip` | `tar.gz` |
| Property filter | Download conditionally on item properties (for example `qa=approved`) | none |

Edge cases: tag downloads can be made conditional on properties, but the parameter list of the
plain tag download lacks the property filter, while the subgroup variant (PKG-VCS-012) has it.
Open: whether the plain tag download takes the filter; the spec decides.

### PKG-VCS-008 · Download a branch archive
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Download branch | Repository key, owner, repository name, branch name; optional archive format; optional property filter | The complete tree of the branch head as an archive |
| Download branch, short form | Repository key, owner, branch name; the owner is used as the repository name | Same |

| Parameter | Meaning | Default |
|---|---|---|
| Archive format | `tar.gz` or `zip` | `tar.gz` |
| Property filter | `key=value` condition on the cached item (ART-012 in [../artifacts.md](../artifacts.md)) | none |

Behavior: with a property filter, only already cached archives are searched; the upstream is not
contacted. Without one, a branch download reflects the branch head; how long a cached branch
archive is reused before the head is checked again is unspecified (the general metadata and cache
periods are on [../remote-repositories.md](../remote-repositories.md)).

### PKG-VCS-009 · Download a commit archive
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Download commit | Repository key, owner, repository name, commit identifier; optional archive format (`tar.gz` default, `zip`) | The complete tree at that commit as an archive |

### PKG-VCS-010 · Download a release archive
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Download release | Repository key, owner, repository name, release name; optional archive format (`tar.gz` default, `zip`) | The complete release archive of a tag |

Limits: specified for GitHub releases only; behavior with other providers is unspecified.

### PKG-VCS-011 · Download a single file from a tag or branch
Priority: **P2**

| Operation | Inputs | Output |
|---|---|---|
| Download file in tag | Repository key, owner, repository name, and `<tag>!<path-in-tree>` | The file |
| Download file in branch | Repository key, owner, repository name, and `<branch>!<path-in-tree>` | The file |
| Short forms of both | As above without the repository name; the owner is used as the repository name | The file |

Example of the combined ref-and-path argument: `v3.0.0!README.md`.

Edge cases: open whether these downloads return the file raw (as binary, with no archive format
parameter) or wrapped in a zip archive (archive format `zip`); the spec decides.

### PKG-VCS-012 · Subgroup repository variants
Priority: **P2**

Four further operations are meant for hosted repositories that live in a subgroup.
They carry the ref after an extra `archive/refs/` path segment following the repository name:

| Operation | Inputs | Output |
|---|---|---|
| Download branch from a subgroup repository | Repository key, owner, repository name, branch name; optional property filter | Branch archive (`tar.gz` default, `zip`) |
| Download tag from a subgroup repository | Repository key, owner, repository name, tag name; optional property filter | Tag archive |
| Download file in a branch of a subgroup repository | Repository key, owner, repository name, `<branch>!<path>` | The file |
| Download file in a tag of a subgroup repository | Repository key, owner, repository name, `<tag>!<path>` | The file |

Open: these operations imply nested group paths, while PKG-VCS-002 excludes nested paths such as
GitLab subgroups for every provider; the spec decides which holds. How a subgroup path maps onto
owner and repository name is unspecified.

Edge cases: the subgroup branch download returns `tar.gz` by default or `zip`, but neither
subgroup archive download lists an archive-format parameter; how `zip` is requested there is
unspecified.

## Upstream access

### PKG-VCS-013 · Private and redirecting upstreams
Priority: **P2**

- Private hosted repositories (private GitHub repositories, self-hosted servers with
  authentication) are reached with upstream credentials on the remote repository's basic tab
  (RMT-005 in [../remote-repositories.md](../remote-repositories.md)).
- Some Git hosts, GitHub among them, redirect archive downloads to a CDN host. Forwarding the
  credentials on such cross-host redirects must then be enabled (advanced tab; RMT-012).

| Setting | Meaning | Default |
|---|---|---|
| Upstream user name and password or token | Credentials for the Git host | none |
| Forward credentials on cross-host redirects | Send the upstream credentials to the redirect target as well; needed for hosts that redirect to a CDN | unspecified (RMT-012) |

Security note: forwarding credentials to a redirect target hands them to whatever host the
upstream redirects to; enable it only for upstreams whose redirect targets are trusted.

## Notes for implementers

- Consumers that pin an archive's checksum depend on the cached copy of a tag archive staying
  unchanged; whether the system ever re-fetches a cached tag archive from the host is unspecified.
- A branch archive changes with every push. The built-in layout names it with the commit hash as
  file integration revision, so cached archives of different commits of one branch do not
  overwrite each other.
- `!` separates the ref from the path inside the tree in single-file downloads, because ref names
  may contain `/`; how a `!` inside a ref name is handled is unspecified.
- Default the archive format to `tar.gz` everywhere; `zip` must be requested explicitly.
- A property-filtered download must never reach the upstream; it is a query over the cache.
- Commit identifiers in the layout are 40 hex characters; abbreviated hashes do not match the file
  integration revision expression and are not recognized as integration versions.
- GitHub and other hosts redirect archive requests to another host; without credential forwarding,
  private repositories fail on the redirect, not on the first request.
- The short operation forms use the owner as repository name; route them unambiguously next to the
  full forms, which have one more path segment.
