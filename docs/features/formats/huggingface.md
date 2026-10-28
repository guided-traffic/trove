# Hugging Face models and datasets

This page catalogs what a repository manager does to stand in for the
[Hugging Face Hub](https://huggingface.co/docs/hub/index) towards the `huggingface_hub` Python
library and the libraries built on it (Transformers, Diffusers, Datasets): hosting an
organization's own models and datasets in local repositories, proxying and caching the public Hub
in remote repositories, aggregating both behind one endpoint in virtual repositories, the storage
layout by revision, the Xet large-file protocol, client configuration, and build metadata
collection. The Hub API is defined by Hugging Face, not by any repository manager; entries that
rely on the [`huggingface_hub` documentation](https://huggingface.co/docs/huggingface_hub/index)
or source code link them. Hugging Face Spaces are not
supported. Generic machine-learning model repositories and model-serving container registries are
in [ml-models.md](ml-models.md). Repository classes in general are described in
[../repositories.md](../repositories.md), pull-through caching in
[../remote-repositories.md](../remote-repositories.md).

Throughout this page, `https://repo.example.com/hf/<repo-key>` stands for the *Hub endpoint* of one
repository (the path layout is the implementation's choice), `hf-local`, `hf-remote` and
`hf-virtual` are example repository keys, `acme/sentiment-model` is an example repository ID and
`${TOKEN}` an access token.

## Repositories and addressing

### PKG-HF-001 · Hugging Face repositories in all three repository classes
Priority: **P2**

Hugging Face is offered as a local, a remote and a virtual repository format (REPO-006 in
[../repositories.md](../repositories.md)), for two content kinds, models and datasets:

- local repositories hold private models, datasets and related files, for example models fine-tuned
  on proprietary data that must not be published;
- remote repositories proxy the public Hub and keep a cached copy, so work continues when a model
  disappears upstream or the Hub is slow (PKG-HF-012);
- virtual repositories expose local and remote members under one endpoint (PKG-HF-014).

Settings of the general repository forms that do not apply to this format: disable resolution
(REPO-017), priority resolution (REPO-018), and store-locally versus pass-through on remote
repositories (RMT-017 in [../remote-repositories.md](../remote-repositories.md)).

Creating a repository requires the system administrator or a project administrator role; for other
users the create action is not offered. UI location: administration, repositories, create
repository, then the class and the Hugging Face format (UI-031 in
[../ui-inventory.md](../ui-inventory.md)). New local and remote repositories use the
revision-based layout of PKG-HF-009.

Interactions: content-addressed storage keeps each large model file once however many repositories
reference it ([../storage-and-blobs.md](../storage-and-blobs.md)); models can be replicated or
federated between sites ([../replication-and-federation.md](../replication-and-federation.md));
scanning and upstream curation are PKG-HF-017.

### PKG-HF-002 · Hub endpoint of a repository
Priority: **P2**

Every Hugging Face repository answers a Hub-compatible HTTP API under its own Hub endpoint. Clients
are pointed at it with the `HF_ENDPOINT` variable; from then on, `huggingface_hub` builds every Hub
URL (model information, file resolution, uploads) below that endpoint instead of
`https://huggingface.co`.

```shell
export HF_ENDPOINT=https://repo.example.com/hf/hf-virtual
```

- Supported client operations: uploading a folder (PKG-HF-005), downloading a whole repository
  snapshot (PKG-HF-006), and downloading individual files through ML libraries (PKG-HF-007).
- The full Hugging Face CLI command set (authentication, cache, repository management,
  collections, jobs and so on) is not supported.
- Which Hub endpoints the system implements is unspecified; PKG-HF-018 summarizes the endpoints the
  supported client operations use.

### PKG-HF-003 · Client authentication
Priority: **P2**

Clients pass a token of the system in `HF_TOKEN`; `huggingface_hub` sends it as
`Authorization: Bearer <token>` (TOK-033 in [../access-tokens.md](../access-tokens.md)).

```shell
export HF_TOKEN=${TOKEN}
```

- Token kinds: identity token (TOK-005) and access token.
- Anonymous use works when anonymous access is switched on (IAM-005 in
  [../identity-and-access.md](../identity-and-access.md)).
- Downloading needs read; uploading needs deploy; replacing an existing revision needs
  delete/overwrite (IAM-022, PKG-HF-005).

Security: the Hub client also sends `HF_TOKEN` to whatever `HF_ENDPOINT` names, so a token of the
system leaks to huggingface.co if a job runs without `HF_ENDPOINT`; separate tokens per endpoint
avoid that.

### PKG-HF-004 · Naming rules for organizations, names and revisions
Priority: **P2**

Content deployed to local repositories must follow the Hub's naming rules:

| Element | Allowed characters | Further rules |
|---|---|---|
| Organization | letters, digits, `-` | must not start or end with `-`, no `--`, not digits only |
| Model or dataset name | letters, digits, `-`, `_`, `.` | — |
| Revision identifier | letters, digits, `-`, `.` | no spaces, no control characters such as `^` or `&` |

- Repository IDs have the form `organization/name`; the legacy layout (PKG-HF-010) treats the
  organization as optional.
- The error returned for a violation is unspecified.

## Uploading and downloading

### PKG-HF-005 · Upload models and datasets
Priority: **P2**

Clients upload a local folder as one revision of a model or dataset:

```python
from huggingface_hub import HfApi

HfApi().upload_folder(
    folder_path="./sentiment-model",
    repo_id="acme/sentiment-model",
    revision="9c41d07e5b2a8f63e1d4c7b0a95f2e8d36b1c470",   # any revision identifier, often a commit hash
    repo_type="model",                                      # or "dataset"
)
```

| Input | Meaning |
|---|---|
| Folder | local folder whose files form the revision |
| Repository ID | `organization/name` (PKG-HF-004) |
| Revision | the revision to store the files under; typically a commit-style hash, used as a unique ID |
| Type | `model` or `dataset` |

- Target: a local repository. Whether a virtual repository with a default deployment repository
  (REPO-038 in [../repositories.md](../repositories.md)) accepts uploads is unspecified.
- Each upload is stored in its own folder named from the revision and a timestamp the system
  generates (PKG-HF-009); clients cannot predict the folder name.
- Uploading the same revision again: in the legacy layout (PKG-HF-010) it replaces the earlier
  upload, which needs the delete/overwrite action; without it the system answers HTTP 409. For the
  current layout the behavior is unspecified.
- Large files go through the Xet protocol when enabled (PKG-HF-015), otherwise through Git LFS.
- Unspecified: whether the model or dataset has to exist before the first upload (on the public Hub
  it does), and whether commit messages are kept.

### PKG-HF-006 · Download whole repositories (snapshot)
Priority: **P2**

Clients download every file of one revision with `snapshot_download`:

```python
from huggingface_hub import snapshot_download

snapshot_download(
    repo_id="acme/sentiment-model",
    revision="9c41d07e5b2a8f63e1d4c7b0a95f2e8d36b1c470",
    repo_type="model",          # or "dataset"
    etag_timeout=86400,
)
```

- Supported by local, remote and virtual repositories; for virtual repositories it is the only
  supported download method.
- The revision can be a branch, a tag or a commit hash; the recommended form is the full commit
  hash, copied from the model's history on the Hub.
- Large models take long to serve, especially on first fetch through a remote repository; hence the
  raised timeout (PKG-HF-008).
- The client stores the files in its local cache (by default under `~/.cache/huggingface/hub/`, in
  `models--<org>--<name>/snapshots/<commit>/`). This is client-side layout, not the server's.

### PKG-HF-007 · Download individual files through ML libraries
Priority: **P2**

Libraries such as Transformers and Diffusers fetch individual files of a model, and the Datasets
library loads datasets by name:

```python
from transformers import AutoModelForCausalLM, AutoTokenizer
tokenizer = AutoTokenizer.from_pretrained("acme/sentiment-model")
model = AutoModelForCausalLM.from_pretrained("acme/sentiment-model")

from datasets import load_dataset
dataset = load_dataset("acme/support-tickets")
```

- Requires `huggingface_hub` 0.19.0 or later and a raised file-metadata timeout
  (`HF_HUB_ETAG_TIMEOUT`, PKG-HF-008); otherwise clients must fall back to snapshot downloads.
- Not available through virtual repositories (PKG-HF-014).
- The snippets can be copied from the Hub's "use this model" panel and work unchanged once
  `HF_ENDPOINT` and `HF_TOKEN` are set.

### PKG-HF-008 · Client configuration and timeouts
Priority: **P2**

| Variable | Value | Purpose |
|---|---|---|
| `HF_ENDPOINT` | the repository's Hub endpoint | routes all Hub calls to the system (PKG-HF-002) |
| `HF_TOKEN` | a token of the system | authentication (PKG-HF-003) |
| `HF_HUB_ETAG_TIMEOUT` | `86400` | timeout in seconds for the file-metadata request that precedes each download |
| `HF_HUB_DOWNLOAD_TIMEOUT` | `86400` | timeout in seconds for download requests |

- Every client setup snippet sets both timeouts to 24 hours. The client's own defaults are
  10 seconds. The reason is unspecified; a likely one is that a remote repository answers the
  metadata request only after it has fetched a large file from the upstream.
- The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) generates these
  exports with a token: the user enters their password, the dialog creates a token and the
  instructions. The same dialog is reachable from a model page of the model catalog (PKG-ML-016 in
  [ml-models.md](ml-models.md)).

## Storage layout

### PKG-HF-009 · Revision-based storage layout
Priority: **P2**

Local and remote Hugging Face repositories store content per model or dataset and per revision.
New repositories use this layout by default. It is named after the layout of generic
machine-learning repositories (PKG-ML-002 in [ml-models.md](ml-models.md)); only the shape an
upload through the companion CLI produces (PKG-HF-016) is specified:

```text
<repository>/
└── models/<organization>/<name>/<revision>_<timestamp>/…files
```

- An upload creates a folder named from the revision and a timestamp generated by the system, for
  example `v2_2026-05-03T09:14:07.120Z`.
- Unspecified: how this folder relates to the version level of PKG-ML-002 (manifest, `model/`,
  `code/`), what else it contains (generated descriptor files, separation of model files and
  metadata), and where datasets are stored (the legacy layout of PKG-HF-010 uses a separate
  `datasets/` top folder).
- Required for virtual members (PKG-HF-014) and for Xet (PKG-HF-015).
- Package cleanup policies treat a generated model-info or dataset-info file as the lead file of a
  version (RET-009 in [../retention-and-cleanup.md](../retention-and-cleanup.md)).

### PKG-HF-010 · Legacy revision layout
Priority: **Later**

The layout of older repositories, deprecated and kept only until repositories are migrated
(PKG-HF-011). A new implementation needs it only to import content from that layout.

- Two kinds of versions: integration versions, which behave like Git branches (in local repositories
  only `main`; in remote repositories one per requested branch), and release versions (tags,
  commits, revisions).
- Below `models/<org>/<name>/` and `datasets/<org>/<name>/` (organization optional), one folder per
  version name, holding one timestamped folder per stored copy, each with a generated metadata
  descriptor file and the content files. Remote repositories also keep folders named after the
  original commit hash, and older variants of the layout kept a pointer file to the latest copy
  per version folder.
- Downloading without a revision stores the latest copy under `main` with a timestamp; earlier copies
  stay reachable through a generated SHA-1 revision value stored as a property of the descriptor.
- Downloading a specific revision (commit hash or tag) stores it under that revision's folder.
- Uploading a version that exists replaces it; the replaced copy is no longer reachable by its
  generated SHA-1 value. Without delete/overwrite permission the system answers HTTP 409.

### PKG-HF-011 · Layout upgrade and restore
Priority: **Later**

| Operation | Inputs | Behavior | Privilege |
|---|---|---|---|
| Upgrade layout (UI) | a legacy Hugging Face repository | Converts the repository to the layout of PKG-HF-009 after a confirmation; a notice reports completion. A snapshot of the old content is kept. | system administrator |
| Restore legacy layout (API) | repository key; a package-type parameter with the format's fixed value | Restores the snapshot taken at upgrade time. Returns HTTP 200 with a completion message. | unspecified |

- UI location: administration, repositories, row action menu, "upgrade layout".
- Restore is a remediation for failed upgrades only. Content added after the upgrade is deleted by
  a restore (data loss).
- Replicated or federated repositories: layouts are incompatible across members. Before upgrading,
  all members must run a version that knows the new layout, replication must be paused with an
  empty queue, and every member must be upgraded before replication resumes
  ([../replication-and-federation.md](../replication-and-federation.md)).

## Remote and virtual repositories

### PKG-HF-012 · Proxying the public Hub
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | RMT-004; only `https://huggingface.co` is supported. | `https://huggingface.co` is the only valid value; whether the form pre-fills it is unspecified |
| Upstream password or access token | RMT-005; a Hub token for private upstream repositories (PKG-HF-013). | empty |
| Metadata retrieval cache period | RMT-018. | 7,200 s |

- Downloads are cached per requested revision (PKG-HF-009); the first download of a large model can
  take long.
- Datasets whose Hub repository only points to data hosted elsewhere are not cached; only data stored
  on the Hub itself is.
- Unspecified: how a branch name such as `main` is mapped to a cached copy once the upstream branch
  moves, and whether the cache period applies to that mapping.

### PKG-HF-013 · Private upstream repositories
Priority: **P2**

To proxy private models of a Hub account, the remote repository authenticates to the Hub with a
Hub access token of type "read", entered as the password or access token of the remote repository
(RMT-005); the user name stays empty. All users with read access to the remote repository can then
download those private models through it. Whether gated public models (which need accepted terms
on the Hub) work the same way is unspecified.

Security: the remote repository's own permissions become the only gate for the private upstream
content.

### PKG-HF-014 · Virtual repositories
Priority: **P2**

- A virtual repository aggregates local and remote Hugging Face members under one endpoint.
- Members must use the revision-based layout (PKG-HF-009).
- Downloads through a virtual repository work with `snapshot_download` only (PKG-HF-006), not with
  per-file library downloads (PKG-HF-007).
- Resolution order across members follows REPO-035 in [../repositories.md](../repositories.md); the
  merge of model information for the same repository ID in two members is unspecified.

## Large files

### PKG-HF-015 · Xet large-file protocol
Priority: **P2**

The Hub's [Xet](https://huggingface.co/docs/hub/en/xet/index) storage protocol replaces Git LFS for
large files with chunk-level deduplication; it speeds up transfers and handles files larger than
50 GB.

| Setting | Meaning | Default |
|---|---|---|
| Enable Xet protocol | Instance-wide. Local, remote and virtual Hugging Face repositories transfer large files through Xet. | unspecified |

- Required role: system administrator or project administrator. UI location: administration,
  package-format settings, Hugging Face section, checkbox.
- Prerequisites: `huggingface_hub` 0.30 or later; repositories in the revision-based layout
  (PKG-HF-009); for self-hosted installations, outbound access to the domains the Hub lists at
  `https://huggingface.co/.well-known/meta.json`.
- Switching Xet on for remote repositories switches it on for the compatible local and virtual
  repositories too.
- Turning it off sends large files through Git LFS again.

Limits and edge cases:

- Not supported on repositories that answer downloads with a redirect (REPO-019, RMT-028).
- Bounded client range: `huggingface_hub` 1.19.0 is the newest supported client and every later
  version is unsupported; `hf_xet` was once capped at 1.4.3, a cap since lifted. The bounds move
  with releases.
- A client below `huggingface_hub` 0.30.0 cannot fetch files that another client stored through
  Xet, so a repository shared by old and new clients breaks for the old ones.
- From `huggingface_hub` 1.20.0 the client sends large files through Xet regardless of the
  server's setting; if Xet is off on the server, those files can no longer be downloaded.
- A chained remote repository (RMT-040) pulling from a local repository that holds Xet files needs
  Xet enabled on the pulling side, or downloads fail.
- Rare client-side race conditions at high download speed can leave a file incomplete; this is a
  client defect.

## Build metadata and security

### PKG-HF-016 · Companion CLI download and upload with build metadata
Priority: **P2**

A companion command-line tool offers exactly two Hugging Face commands, download and upload, as
wrappers around `snapshot_download` and `upload_folder`. It runs them through a local Python 3
interpreter with `huggingface_hub` installed, and records build metadata on request (see
[../builds-and-ci.md](../builds-and-ci.md)).

<details>
<summary>Options</summary>

| Option | Applies to | Meaning | Default |
|---|---|---|---|
| Repository ID | both | `organization/name` | required |
| Folder | upload | local folder to upload | required |
| Revision | both | branch, tag or commit to download; branch or tag to upload to | `main` |
| Type | both | `model` or `dataset` | `model` |
| Metadata timeout | download | seconds for the file-metadata request | 86,400 |
| Download timeout | download | seconds for download requests | 86,400 |
| Repository key | both | sets `HF_ENDPOINT` from the configured server and this key; required when `HF_ENDPOINT` is not set | — |
| Server | both | which configured server to use; sets `HF_TOKEN` from its credentials | default server |
| Build name, build number | both | enable build metadata collection; both required together | — |
| Module, project | both | module ID and project of the build metadata | see below |

</details>

- The tool reads the repository key from the last path segment of `HF_ENDPOINT`.
- Download with build metadata: after the download, the tool queries the system (query language,
  SRCH-023 in [../search-and-query.md](../search-and-query.md)) for all files of the downloaded path
  and records them as dependencies with name, type, SHA-1, MD5 and SHA-256.
- Upload with build metadata: the tool queries the system for the uploaded files, records them as
  artifacts with the same checksums, and sets build name, build number, build timestamp and project
  properties on the uploaded folder (ART-023 in [../artifacts.md](../artifacts.md)). The query is
  needed because the storage folder name contains a timestamp the system generates (PKG-HF-009).
- Default module ID: `<build name>-<build number>:<type>`; the module type distinguishes models
  from datasets.

Server operations the tool relies on: the Hub API (PKG-HF-018), query execution with read
permission, property setting with annotate permission, and build upload.

### PKG-HF-017 · Model scanning and upstream curation
Priority: **Later**

The format sits inside a governed model supply chain; the mechanisms belong to
[../security-scanning-and-curation.md](../security-scanning-and-curation.md):

- curation checks a requested Hub model against a catalog of scan results before the remote
  repository downloads it, and blocks unsafe models;
- cached and stored models are scanned continuously for license issues and malicious content, and
  blocked when an issue is found;
- scanning of datasets is not covered yet;
- the model catalog's allow-listing of models per project builds on this (PKG-ML-016 in
  [ml-models.md](ml-models.md)).

## Protocol reference

This section summarizes, from the `huggingface_hub` sources and the
[Hub API documentation](https://huggingface.co/docs/hub/api), the Hub endpoints that the supported
client operations call. It is the yardstick for client compatibility; the set of endpoints the
system implements is unspecified. Paths are relative to the Hub endpoint
(PKG-HF-002); `{type}` is `models` or `datasets`, and `{prefix}` is empty for models and
`datasets/` for datasets.

### PKG-HF-018 · Hub API endpoints used by the supported clients
Priority: **P2**

<details>
<summary>Endpoints</summary>

| Request | Used by | Answer the client relies on |
|---|---|---|
| `GET api/{type}/{repo_id}/revision/{revision}` | snapshot download, libraries | JSON with the resolved commit (`sha`) and the file list (`siblings[].rfilename`); newer clients may list files through `GET api/{type}/{repo_id}/tree/{revision}` instead |
| `HEAD {prefix}{repo_id}/resolve/{revision}/{path}` | every file download | headers `X-Repo-Commit` (commit hash), `ETag` or `X-Linked-Etag` (content identity; for large files the SHA-256), `X-Linked-Size` or `Content-Length`; may answer with a redirect to the download location |
| `GET {prefix}{repo_id}/resolve/{revision}/{path}` | every file download | the file, possibly after a redirect; range requests let the client resume |
| `POST api/{type}/{repo_id}/preupload/{revision}` | upload | for each announced file (path, size, sample), whether to upload it as a regular or a large (LFS) file, and whether to ignore it |
| `POST {prefix}{repo_id}.git/info/lfs/objects/batch` | upload of large files | Git LFS batch answer with upload actions per object (SHA-256 and size) |
| `POST api/{type}/{repo_id}/commit/{revision}` | upload | accepts an NDJSON body (header, regular files base64-encoded, references to uploaded large files, deletions) and answers with the new commit ID |
| Xet token and storage endpoints | Xet transfers (PKG-HF-015) | see the [Xet documentation](https://huggingface.co/docs/hub/en/xet/index) |

</details>

## Notes for implementers

- Every file-metadata answer must carry `X-Repo-Commit` and an `ETag`; without them
  `huggingface_hub` refuses the file as "not on the Hub" and libraries fall back to errors.
- The `ETag` decides the client's blob cache key: it must stay identical for identical content and
  change when content changes, across local, remote and virtual repositories.
- A branch name such as `main` resolves to a commit; return the same commit hash from the model
  information and from `X-Repo-Commit`, or the client builds inconsistent snapshot folders.
- The system accepts arbitrary revision identifiers as upload targets, including hash-like ones,
  while the public Hub only commits to branches; keep both forms resolvable for downloads.
- Fetching a large upstream file before answering the metadata request needs the client's raised
  timeouts; streaming through while caching, or answering the metadata from upstream headers, keeps
  clients with default timeouts working.
- The client's HTTP library drops the `Authorization` header when a redirect changes the host; a
  download redirect to another host (object storage, for example) must therefore carry its own
  authorization, such as a pre-signed URL.
- Return 401 for a missing or invalid token and 404 for an unknown repository, revision or file.
  `huggingface_hub` turns these into distinct exceptions, refined by the Hub's `X-Error-Code`
  response header (`RepoNotFound`, `RevisionNotFound`, `EntryNotFound`, `GatedRepo`); sending that
  header gives users an actionable message instead of a generic HTTP error.
- Requests carry the repository type through `models`/`datasets` paths and the `datasets/` prefix;
  a dataset and a model with the same ID are different objects.
</content>
</invoke>
