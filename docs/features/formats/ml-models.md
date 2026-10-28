# Machine-learning models and model-serving containers

This page catalogs three related capabilities around machine-learning models that go beyond the
Hugging Face format ([huggingface.md](huggingface.md)): generic machine-learning model repositories
that store models of several frameworks with their code, dependencies and metrics; remote
repositories for model-serving containers, using NVIDIA NIM as the example; and, at capability level
only, a model catalog that governs which models projects may use. The model file formats (ONNX,
PyTorch, safetensors and others) and the container runtime belong to their own projects; where an
entry relies on those sources rather than on the described system, it links them. Repository
classes in general are described in [../repositories.md](../repositories.md), pull-through caching
in [../remote-repositories.md](../remote-repositories.md).

Throughout this page, `ml-local`, `nim-remote` and `nim-docker-remote` are example repository keys,
`repo.example.com` is the system's host, and `${TOKEN}` an access token.

## Machine-learning model repositories

### PKG-ML-001 · Machine-learning model repositories
Priority: **P2**

A machine-learning repository is a local repository whose layout holds models of several kinds
(framework-aware models and opaque model files) in one place, together with their code,
dependencies, parameters and metrics. It is a repository layout rather than a package protocol:
clients do not speak a standard package-manager protocol to it, but a companion Python SDK that
"logs" (uploads) and "loads" (downloads) models (PKG-ML-003 to PKG-ML-006).

- Repository classes: local only.
- Benefits: access control per repository, searchable metadata
  (PKG-ML-008), and models managed next to the application's other dependencies.
- Not supported: vulnerability and malicious-model scanning of these repositories.
- Creating a repository requires the system administrator or a project administrator role. UI
  location: administration, repositories, create repository, class local, machine-learning format
  (UI-031 in [../ui-inventory.md](../ui-inventory.md)); only a key is required.
- The wire protocol between SDK and server is unspecified. The SDK relies on
  content-addressed storage and uploads a file only when the repository does not already hold a
  file with the same checksum, which matches deploy by checksum (ART-004 in
  [../artifacts.md](../artifacts.md)); properties (ART-023) and the query language (SRCH-023 in
  [../search-and-query.md](../search-and-query.md)) are the other server capabilities it uses.

### PKG-ML-002 · Model storage layout
Priority: **P2**

```text
<repository>/
└── models/
    └── <model name>/
        └── <version>/
            ├── <model manifest>        # model name, its files and dependencies
            ├── model/                  # the model files
            │   ├── file1
            │   └── file2
            └── code/                   # optional inference code
                ├── <code archive>
                └── requirements.txt
```

- The SDK generates the model manifest after the upload.
- A version defaults to the current timestamp when the client gives none.
- The same layout is the default for new Hugging Face repositories (PKG-HF-009 in
  [huggingface.md](huggingface.md)).
- Package cleanup policies treat the model manifest as the lead file of a version (RET-009 in
  [../retention-and-cleanup.md](../retention-and-cleanup.md)).

### PKG-ML-003 · Log a single-file model
Priority: **P2**

The format-agnostic way to store a model: the model is one file, treated as an opaque blob.

<details>
<summary>Inputs</summary>

| Input | Meaning | Default |
|---|---|---|
| Source path | local path of the model file; a single file, not a directory | required |
| Repository | target machine-learning repository | required |
| Model name | unique name of the model | required |
| Version | model version | current timestamp |
| Properties | string key/value pairs, searchable | none |
| Dependencies | one of: a list of requirement specifiers (`catboost==1.2.5`); the path of one `requirements.txt`, `pyproject.toml` or `conda.yml`; a two-item list with `pyproject.toml` and `poetry.lock` | none |
| Code directory | directory with the inference source code | none |
| Parameters | training hyperparameters as key/value pairs | none |
| Metrics | numeric key/value pairs, searchable | none |
| Prediction file | script that defines prediction (PKG-ML-007) | none |

</details>

- Code directory, dependencies and prediction file must be given together or not at all.
- File types listed for opaque models: `.cbm`, `.pretrained_model`, `.onnx`, `.pkl`, `.pth`,
  `.joblib`. Open: the list is either exhaustive, so other extensions are rejected, or
  illustrative, so an `.rds` file, which is not in it, logs as well; the spec decides.
- A file whose checksum already exists in the repository is not uploaded again (PKG-ML-001).
- Required permission: deploy on the repository (and annotate for properties, by the general rules
  of IAM-022 in [../identity-and-access.md](../identity-and-access.md)).
- Because the file is opaque, the user has to determine and manage the dependencies and code needed
  to load and run it; the system does not inspect it.

### PKG-ML-004 · Load a single-file model
Priority: **P2**

| Input | Meaning | Default |
|---|---|---|
| Repository | repository that holds the model | required |
| Model name | name of the model | required |
| Version | version to load | required |
| Target path | local folder for the files | unspecified |

Output: the local path of the downloaded model. Required permission: read.

### PKG-ML-005 · Framework-aware models
Priority: **P2**

For five frameworks the SDK serializes a live model object on upload and returns a ready-to-use
object on download, with dependencies and code included:

| Framework | Logged object |
|---|---|
| [CatBoost](https://catboost.ai/) | CatBoost model |
| Hugging Face Transformers | model plus tokenizer |
| [ONNX](https://onnx.ai/) | ONNX model |
| [PyTorch](https://pytorch.org/) | `torch.nn.Module` |
| [scikit-learn](https://scikit-learn.org/) | estimator (serialized with joblib) |

- Logging takes the same optional inputs as PKG-ML-003 (version, properties, dependencies, code
  directory, parameters, metrics, prediction file, with the same all-or-none rule).
- Loading takes repository, model name and version and returns the deserialized object.
- Deserializing pickled models requires the same libraries, in compatible versions, in the loading
  environment.
- Models of other frameworks use the single-file way (PKG-ML-003).

### PKG-ML-006 · Model information lookup
Priority: **P2**

| | |
|---|---|
| Inputs | repository, model name, version, and the model's kind: one of the five frameworks of PKG-ML-005 or opaque files |
| Output | information about the stored model version; the content is unspecified |
| Privilege | read (by the general rules) |

### PKG-ML-007 · Inference code packaging rules
Priority: **P2**

A model can carry custom inference logic (input validation, transformation and similar) in its code
directory. The prediction file must satisfy:

| Rule | Requirement |
|---|---|
| File name | `predict.py` |
| Location | inside the code directory, at any depth |
| Function | a function `predict` with exactly two parameters followed by `**kwargs`, for example `def predict(model, data, **kwargs)` |
| Parameter names | at least one non-whitespace character each, no commas; whitespace around keywords, parentheses and commas is free |

When such a model is later built for serving, the contents of the code directory become the root of
the Python import path. A pickled model whose class lives in a package therefore needs the package's
parent directory as code directory, or unpickling fails with a missing-module error.

### PKG-ML-008 · Model metadata and search
Priority: **P2**

Logged models carry user-supplied properties (strings), metrics (numbers), parameters and
automatically extracted attributes. They are searchable with the query language (SRCH-023 in
[../search-and-query.md](../search-and-query.md)) and serve to manage models by metadata. Which
values are stored as item properties (ART-021 in [../artifacts.md](../artifacts.md)) and which
only inside the model manifest, and which attributes are extracted automatically, is unspecified.

### PKG-ML-009 · SDK configuration and client setup
Priority: **P2**

- The SDK is a Python package installed with pip.
- It reads two environment variables: the system's base URL and an access token
  (TOK-001 in [../access-tokens.md](../access-tokens.md)).
- The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) generates the
  snippet with URL and token filled in.

## Model-serving containers (NVIDIA NIM)

### PKG-ML-010 · Model-serving containers: two-repository setup
Priority: **P2**

An [NVIDIA NIM](https://docs.nvidia.com/nim/) is a GPU-optimized inference microservice made of a
dedicated container image, an optimization profile and a model. The container image and the model
files come from two different NVIDIA services, so the system needs two remote repositories:

| Repository | Format | Upstream | Upstream credentials |
|---|---|---|---|
| Container images | Docker remote | `https://nvcr.io` | user name `$oauthtoken`, password an NVIDIA personal key |
| Model files | NIM remote (PKG-ML-011) | `https://api.ngc.nvidia.com` | user name empty, password an NVIDIA personal key |

- Repository classes for the NIM format: remote only; local and virtual repositories are not
  supported.
- Supported client: Docker only. NVIDIA's own CLI and SDK for its catalog are not supported.
- Supported NIM versions: 1.3 and later.
- Not supported: vulnerability scanning, upstream curation, anonymous access, and direct download
  from object storage.
- Client machines need Docker, supported NVIDIA GPUs and NVIDIA's container toolkit.
- Upstream authentication fails not only for a missing or wrong personal key: a gated model whose
  license terms the key's owner has not accepted in NVIDIA's catalog is refused as well, and the
  error surfaces as an upstream credential failure of the remote repository.

### PKG-ML-011 · NIM model remote repository
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Upstream URL | NVIDIA's catalog API. | pre-filled `https://api.ngc.nvidia.com` |
| User name | — | empty |
| Password or access token | NVIDIA personal key (RMT-005 in [../remote-repositories.md](../remote-repositories.md)). | none |
| Metadata retrieval cache period | RMT-018; a model published upstream becomes visible in the system only after the next check. | 7,200 s |

- Creating it requires the system administrator or a project administrator role. UI location:
  administration, repositories, create repository, class remote, NIM format.
- The repository acts as the model-download and authentication service for the NIM container
  (PKG-ML-012). Which NVIDIA API endpoints it emulates is unspecified.

### PKG-ML-012 · Running a NIM container through the system
Priority: **P2**

The container is pulled through the Docker remote repository and downloads its model through the
NIM remote repository. Configuration is passed as environment variables:

| Variable | Value |
|---|---|
| `NGC_API_KEY` | a token of the system, generated by the client setup dialog |
| `NGC_API_ENDPOINT` | host and model-API path of the NIM remote repository, without scheme |
| `NGC_AUTH_ENDPOINT` | the same address as `NGC_API_ENDPOINT` |
| `NGC_API_SCHEME` | `http` or `https` |
| `CACHE_DIR` | host folder for downloaded model weights |

```shell
docker login repo.example.com
docker run -it --rm --gpus all \
  -e NGC_API_KEY="$NGC_API_KEY" \
  -e NGC_API_ENDPOINT="$NGC_API_ENDPOINT" \
  -e NGC_API_SCHEME="$NGC_API_SCHEME" \
  -e NGC_AUTH_ENDPOINT="$NGC_AUTH_ENDPOINT" \
  -v "$CACHE_DIR:/opt/nim/.cache" \
  repo.example.com/nim-docker-remote/nim/meta/llama-3.1-8b-instruct:1.1.0
```

- The image path is the part of NVIDIA's image reference between `nvcr.io/` and the tag; tags are
  listed in NVIDIA's catalog.
- The registry part of the image reference follows the Docker addressing of the system (see the
  Docker format page under [README.md](README.md)).
- The client setup dialog (UI-021 in [../ui-inventory.md](../ui-inventory.md)) fills in host,
  repository key and token.

Security: the token of the system is passed into the container's environment and visible to anyone
who can inspect the container; a read-only token scoped to the two repositories limits the damage.

### PKG-ML-013 · NIM cache layout
Priority: **P2**

```text
<repository cache>/
├── <internal metadata area>/            # caller information, model-to-path mapping,
│                                        # per model version a mapping of upstream file URLs
└── models/<organization>/<model>/<version>/
    ├── <generated model-info file>      # name, version, description, authorship, requirements, origin
    └── model/
        ├── checksums.blake3
        ├── config.json
        ├── generation_config.json
        ├── LICENSE.txt
        ├── NOTICE.txt
        ├── model-<n>-of-<total>.safetensors
        ├── model.safetensors.index.json
        ├── special_tokens_map.json
        ├── tokenizer.json
        ├── tokenizer_config.json
        └── tool_use_config.json
```

- The URL mapping lets the system answer later requests for the same upstream file from the cache.
- `checksums.blake3` holds BLAKE3 hashes of the other model files for integrity checks; the model
  weights are split into [safetensors](https://huggingface.co/docs/safetensors/index) shards with an
  index file.
- A second layout exists, assigned to the Docker remote repository: an internal area with caller
  information and a model mapping at the root, and below `models/` an organization, team and
  publisher hierarchy per model and version, whose own internal folder holds the checksum and
  configuration files. Whether that layout really belongs to the Docker remote repository or is an
  older form of the NIM remote cache is unspecified.

### PKG-ML-014 · Cached model properties and search
Priority: **P2**

Every cached NIM item carries descriptive properties (ART-021 in [../artifacts.md](../artifacts.md)):
creation date, display name, framework, labels, model format, latest version ID, name, precision,
publisher, short description, update date and version ID. Where the values come from is
unspecified; the names match the fields of NVIDIA's catalog.

- Property search (SRCH-007 in [../search-and-query.md](../search-and-query.md)) finds models by
  name or version; the artifact browser shows them like other cached items.
- A model newly published upstream is found only after the remote repository has checked the
  upstream again (PKG-ML-011).

### PKG-ML-015 · Model versions and tags
Priority: **P2**

The package views (ART-048 in [../artifacts.md](../artifacts.md), UI-008 in
[../ui-inventory.md](../ui-inventory.md)) list the versions and tags of a cached NIM model; selecting
one shows its details.

## Model catalog (capability level)

### PKG-ML-016 · Model catalog with per-project allow-listing
Priority: **Later**

A governance layer over models:

- a marketplace view lists models available from approved providers (externally hosted model APIs
  and model packages such as Hugging Face models and NIMs) with license information and scan
  results;
- a registry view lists the models allowed per project; by default nothing is allowed, and a model
  not on a project's allow-list cannot be used there;
- a detection view lists models that the scanner found in the organization's repositories and
  that nobody approved (currently model packages only), with the projects, repositories and
  artifact paths where each occurs and an inline mark for models flagged as malicious. Actions:
  approve, per project only and never for a model flagged as malicious; block, per project, per
  repository or everywhere at once, enforced by upstream curation on later downloads, with partial
  failures reported per resource and no unblock from this view; dismiss, at the same three scopes,
  which hides the finding until the model is detected again;
- only administrators allow models; allowing a model package requires upstream curation to be on
  for the format's remote repositories ([../security-scanning-and-curation.md](../security-scanning-and-curation.md));
- each model page shows a status badge (blocked when at least one policy blocks the model's latest
  version, otherwise allowed) and a policy tab listing the blocking policies with their matched
  condition; while a model is blocked, its use and deploy actions are disabled;
- "use model" for an allowed Hugging Face package generates a token and the client configuration
  for a remote repository (PKG-HF-008 in [huggingface.md](huggingface.md)) plus a library snippet;
  for a NIM it shows the container command of PKG-ML-012;
- custom models can be uploaded into the catalog and are allowed automatically; a local
  malicious-model scan before upload is recommended;
- search and filters: free text, model type, provider, package type;
- the same catalog also governs MCP servers, agent skills and plug-ins, which are outside this page.

### PKG-ML-017 · Gateway for external model APIs
Priority: **Later**

A proxy in front of hosted model APIs (OpenAI-compatible chat completions; providers named include
OpenAI, Google Gemini, Anthropic and AWS):

| Element | Behavior |
|---|---|
| Connection | stores the provider credential for one provider and one project; one connection per provider-project pair; administrators only |
| Connection name | letters, digits, `-`, `_`; unique |
| Secret | the provider API key, stored as a named secret and selectable per connection |
| Client access | a token of the system and a model name that includes the connection name, used against the gateway's OpenAI-compatible endpoint |
| Audit | all traffic through an allowed connection is tracked and audited; a runtime dashboard shows usage when there is traffic |
| Errors | a bad or expired token, or a model name the project does not know, yields 401; a request for a model that has since been blocked yields 404 |
| Lifecycle rules | a connection cannot be deleted while a model still uses it (block the model first); a deployed model cannot be blocked until it is undeployed |
| UI | administration, model settings, connections: sort, filter by connection, provider, project or environment, group by project, edit the secret |

### PKG-ML-018 · Managed model build, deployment and monitoring
Priority: **Out of scope**

An operator-run ML platform would cover building and training models on managed compute,
promoting model versions to container builds, deploying them as real-time, batch or streaming
endpoints on managed GPU clusters or cloud ML services, scaling, automatic undeployment of failed
deployments, inference analytics, monitoring, experiment tracking and a feature store. These are
hosted compute services, not repository functions.

### PKG-ML-019 · Model bill of materials
Priority: **Later**

Every model governed by the catalog of PKG-ML-016 carries a machine-learning bill of materials: a
record with a fixed set of fields that says where the model comes from, under which license, how
it is built, what it consumes and produces, how it was trained and which risks are known. The
system fills it automatically, and it can be exported for audits.

| Aspect | Behavior |
|---|---|
| Scope | one record per model for externally hosted model APIs and for model packages; one per model version for custom models, and a build made from a version shows that version's record |
| Fields | 13: model name, model version, supplier or provider, author, unique ID as a [package URL](https://github.com/package-url/purl-spec), release date (ISO 8601), license, architecture, parameters, inputs and outputs, compute resources, training dataset, risk assessment |
| Population | harvested when a model is registered or a custom version is created: from the provider and gateway details for model APIs, from catalog metadata (including Hugging Face model card fields) for model packages, from training jobs and SDK-logged context for custom models; hosted model APIs show placeholders where providers disclose nothing |
| Editing | risk assessment is editable for every model kind; for custom models also supplier, author, license, dataset and (when not harvested) compute resources; everything else is read-only |
| Export | JSON in [CycloneDX](https://cyclonedx.org/) 1.7 or [SPDX](https://spdx.dev/) 3.0 |
| Permissions | a model read permission to view, a model manage permission to edit |
| UI | a tab on the model page, the version page or the build page, with a field filter and a last-modified time |

Interactions: the SBOM capabilities of
[../security-scanning-and-curation.md](../security-scanning-and-curation.md) cover packages; this
record is the model counterpart and depends on the catalog of PKG-ML-016.

## Notes for implementers

- Machine-learning repositories have no standard client protocol; compatibility means providing the
  generic primitives the SDK needs (deploy by checksum, properties, query, download by path) with
  the layout of PKG-ML-002.
- Model files are large and frequently identical across versions; checksum-first upload and
  content-addressed storage avoid re-uploading unchanged weights.
- Treat a model version as immutable once its manifest exists; loaders resolve name and version to
  files and break if files change underneath.
- Pickled models (`.pkl`, `.joblib`, many `.pth`) execute code when loaded; the server cannot make
  them safe, but it can record provenance and keep scanning hooks available.
- For NIM, the system stands in for two NVIDIA services at once (container registry through the
  Docker remote, model API through the NIM remote); both need the NVIDIA personal key, and the NIM
  container needs the system's token for the model API.
- The NIM container sends the same address as API and authentication endpoint; the NIM remote
  repository must answer the container's token exchange as well as its model downloads.
- New upstream NIM models appear only after the metadata cache period; a manual cache invalidation
  (ART-050) helps when a model must be available immediately.
</content>
</invoke>
