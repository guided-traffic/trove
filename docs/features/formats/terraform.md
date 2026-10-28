# Terraform and OpenTofu formats

This page catalogs what a repository manager does for Terraform and OpenTofu: a private registry
for modules and providers (local, remote and virtual repositories), with providers served either
through the provider registry protocol (the system acts as origin registry, with GPG signing keys)
or as a provider network mirror; a separate state-backend repository type that stores Terraform
state per workspace with locking, encryption, history and a state viewer; client login; and
module publishing through the companion CLI. The client protocols are HashiCorp's
([module registry protocol](https://developer.hashicorp.com/terraform/internals/module-registry-protocol),
[provider registry protocol](https://developer.hashicorp.com/terraform/internals/provider-registry-protocol),
[provider network mirror protocol](https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol),
[remote service discovery](https://developer.hashicorp.com/terraform/internals/remote-service-discovery));
OpenTofu implements the same protocols. Entries that rely on those protocols say so. Throughout
this page, `repo.example.com` is the system's host name, `<repo-url>` stands for the URL under
which a repository's files are served by path and `<format-url>` for the repository's Terraform
endpoint URL; the concrete path layout is the implementation's choice. `tf-local`, `tf-remote`,
`tf-virtual` and `tf-state` are example repository keys.

## Repository types

### PKG-TF-001 · Registry repositories for modules and providers
Priority: **P2**

The Terraform registry format exists for local, remote and virtual repositories (REPO-006 in
[../repositories.md](../repositories.md)).

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Registry type | local | Module or provider. Selects what the repository hosts and the matching default layout (a module layout or a provider layout, REPO-047). | unspecified (must be chosen) |
| Signing key (provider repositories) | local | GPG public key for the provider registry protocol (PKG-TF-010). | unspecified (required for the origin-registry mode) |

- Remote and virtual repositories have no registry type: their settings are the same for modules
  and providers, and one remote or virtual repository serves both.
- Whether the registry type of a local repository can be changed after creation is unspecified.
- Supported clients: Terraform CLI 1.0.0 and later; the supported OpenTofu versions are unspecified
  (PKG-TF-002).
- Federated repositories accept the registry format (outside this page's scope).

Creating a repository requires the system administrator or a project administrator role. UI
location: administration, repositories, create repository, choose the class and the
Terraform/OpenTofu format (UI-031 in [../ui-inventory.md](../ui-inventory.md)).

### PKG-TF-002 · OpenTofu clients
Priority: **P2**

The same repositories serve OpenTofu. After the initial client configuration, everything on this
page applies to OpenTofu as well. Differences:

- login with `tofu login <host>` (PKG-TF-016);
- the network-mirror configuration excludes the OpenTofu public registry from direct installation
  instead of HashiCorp's (PKG-TF-008);
- a remote repository proxying the OpenTofu registry uses OpenTofu-specific upstream URLs
  (PKG-TF-011).

Open: the client snippet excludes `registry.opentofu.io/*/*`, while the remote-repository
settings use `https://registry.opentofu.org/`; which host the snippet excludes is for the spec to
decide. OpenTofu's public registry host is `registry.opentofu.org`.

### PKG-TF-003 · State backend repository type
Priority: **P2**

A separate repository format that stores Terraform state, working in parallel to the registry
repositories (PKG-TF-018 to PKG-TF-022).

- Local repositories only; remote and virtual state-backend repositories do not exist.
- Not available as a federated repository (excluded by default, configurable instance-wide);
  replication between two instances works only with state encryption turned off (PKG-TF-020).

## Modules

### PKG-TF-004 · Module layout and deploy
Priority: **P2**

A module is the set of `.tf` files of one directory, packed into a zip archive. Modules are
deployed to a local module repository with a plain HTTP `PUT` (deploy by checksum and archive
deploy also work, ART-004 and ART-005 in [../artifacts.md](../artifacts.md)):

```text
PUT <repo-url>/<namespace>/<module-name>/<provider>/<version>.zip
```

| Path segment | Meaning |
|---|---|
| Namespace | Owner or organization of the module. |
| Module name | Name of the module. |
| Provider | The provider ("system") the module targets, for example `aws`. |
| Version | Module version; the file name is `<version>.zip`. |

- Required permission: deploy on the target path (IAM-022 in
  [../identity-and-access.md](../identity-and-access.md)).
- UI: the deploy dialog (UI-013 in [../ui-inventory.md](../ui-inventory.md)).
- Publishing with the companion CLI: PKG-TF-023.

### PKG-TF-005 · Module source addresses and module registry protocol
Priority: **P2**

Clients reference a module through the system's host name. Because a registry module address has
exactly three segments (namespace, name, provider), the repository key is folded into the
namespace segment with a double underscore:

```hcl
module "vpc" {
  source  = "repo.example.com/tf-virtual__terraform-aws-modules/vpc/aws"
  version = "5.1.0"
}
```

- The system splits the namespace segment at `__` into repository key and namespace. Behavior for
  repository keys that themselves contain `__` is unspecified.
- Implicit protocol requirements (from the module registry protocol): Terraform first
  reads `https://<host>/.well-known/terraform.json` for the `modules.v1` base URL, then lists
  versions and requests a download location for the chosen version; the system must answer at the
  host root, independent of any context path.
- Modules resolve from local, remote and virtual repositories.

### PKG-TF-006 · Module source host override
Priority: **P2**

When a client installs a module, the system replaces the host name in the module's source location
with its own, so that the module archive is fetched through the system. The exact rewriting rules
are unspecified.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Override module source host name | instance-wide, startup configuration | Turns the replacement off; disabling it is not recommended. | on |

## Providers

### PKG-TF-007 · Provider registry protocol (origin registry)
Priority: **P2**

In this mode the system is the authoritative origin registry for providers under its host name.
Terraform discovers and trusts it through the standard provider registry protocol, so users need
no CLI configuration beyond a one-time login for private registries (PKG-TF-016).

```hcl
terraform {
  required_providers {
    docker = {
      source  = "repo.example.com/<repo-key><separator><namespace>/docker"
      version = "1.0.0"
    }
  }
}
```

- The provider address also has only two segments below the host (namespace and type), so the
  repository key and the namespace share the namespace segment, joined by a fixed infix token that
  differs from the module separator. The token is the implementation's choice.
- The local repository must have a GPG public key associated (PKG-TF-010); without it the protocol
  cannot be served.
- Implicit protocol requirements (from the provider registry protocol): service
  discovery announces `providers.v1`; the version list names the supported protocol versions and
  platforms; the download response for one platform carries the archive URL, the URLs of the
  `SHA256SUMS` file and its signature, the archive's SHA-256, and the GPG public key(s) that verify
  the signature. Terraform refuses a provider whose signature does not verify against the returned
  key.

### PKG-TF-008 · Provider network mirror
Priority: **P2**

In this mode a repository acts as a network mirror (a local cache) for providers whose origin is
elsewhere. Clients configure it explicitly in the CLI configuration (`~/.terraformrc`,
`%APPDATA%/terraform.rc` on Windows):

```hcl
provider_installation {
  direct {
    exclude = ["registry.terraform.io/*/*"]
  }
  network_mirror {
    url = "<format-url>/providers/"
  }
}
```

- Provider addresses in configurations stay unchanged (`kreuzwerker/docker`).
- Terraform verifies downloaded archives against checksums only; signature trust stays with the
  origin registry.
- For OpenTofu, the excluded registry is OpenTofu's (PKG-TF-002).
- Implicit protocol requirements (from the network mirror protocol): the mirror answers
  a version index per provider and, per version, the archive list per platform with download URLs
  and hashes.

### PKG-TF-009 · Provider package layout and deploy
Priority: **P2**

Provider files are deployed to a local provider repository with plain HTTP `PUT` requests below
`<namespace>/<type>/<version>/`:

| File | Needed for | Example |
|---|---|---|
| `terraform-provider-<type>_<version>_<os>_<arch>.zip` | both modes; one per platform | `terraform-provider-null_3.1.0_linux_amd64.zip` |
| `terraform-provider-<type>_<version>_SHA256SUMS` | registry protocol | `terraform-provider-null_3.1.0_SHA256SUMS` |
| `terraform-provider-<type>_<version>_SHA256SUMS.sig` | registry protocol; detached GPG signature of the checksum file | `terraform-provider-null_3.1.0_SHA256SUMS.sig` |

- The publisher signs the checksum file; the repository's associated public key (PKG-TF-010) must
  match the publisher's signing key. Whether the system can sign the checksum file itself is
  unspecified.
- Open: deploy paths either follow the path template above or put an extra folder level before
  the namespace; the template above is the working assumption, and the spec decides.
- Required permission: deploy on the target path.

### PKG-TF-010 · Provider signing key
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Provider signing key | local provider repositories | GPG key pair from the central key store (IAM-046 in [../identity-and-access.md](../identity-and-access.md)) whose public key Terraform uses to verify provider packages. | unspecified (optional at the format level, required for PKG-TF-007) |

- Must be set when the repository is created for the origin-registry mode (PKG-TF-007).
- Prerequisite: the key pair has been generated and uploaded to the key store.
- Key rotation for provider repositories is unspecified; Terraform verifies each download against
  the keys returned with it, so already-published versions need signatures that match the key the
  system returns.

## Remote and virtual repositories

### PKG-TF-011 · Remote Terraform repositories
Priority: **P2**

A remote repository proxies and caches modules and providers from a public or private registry
(RMT-001 in [../remote-repositories.md](../remote-repositories.md)). Three upstream URLs describe
the upstream:

| Setting | Meaning | Default |
|---|---|---|
| URL | Base URL of the module storage (where module archives are downloaded from). | unspecified (pre-filled) |
| Registry URL | Base URL of the upstream registry API. OpenTofu: `https://registry.opentofu.org/`; GitLab: `https://gitlab.com`; HCP Terraform: `https://app.terraform.io`. | unspecified (pre-filled) |
| Providers URL | Base URL of the provider storage. OpenTofu: `https://github.com/`. | `https://releases.hashicorp.com` |
| Git provider | Version-control host type of the module sources; GitHub is the recommended value when the upstream is HashiCorp's public registry; every other Git provider is ruled out. | unspecified for Terraform (the general remote configuration schema defaults the Git provider to GitHub) |

- Upstream is another instance of the system (chained remote, RMT-040): URL = the upstream
  repository's file URL, Registry URL = its Terraform endpoint URL, Providers URL = its Terraform
  endpoint URL plus `/providers`.
- When the repository is created for one of the well-known public registries
  (`registry.terraform.io`, `registry.opentofu.org`, or one further public registry host), the
  system switches on HEAD request bypass automatically (RMT-024 in
  [../remote-repositories.md](../remote-repositories.md)).
- All other remote settings (caching, offline, proxy, credentials) are the general ones
  ([../remote-repositories.md](../remote-repositories.md)).

### PKG-TF-012 · Token-authenticated upstream registries
Priority: **P2**

A remote repository can proxy a private GitLab Terraform registry or HCP Terraform.

| Setting | Meaning | Default |
|---|---|---|
| Password / access token | Bearer token of the upstream: a GitLab access token, or an HCP Terraform organization API token. | unspecified (optional) |
| Token authentication | Sends the credential as a bearer token (RMT-006). Required for GitLab and HCP Terraform upstreams. | off |
| External dependency rewrite and allow list | On the remote repository's advanced tab; lists the URL patterns the client may resolve module sources from (REPO-040 in [../repositories.md](../repositories.md)). Examples: `https://*terraform*.io/**` for HCP Terraform, `https://gitlab.com/**` for GitLab. | off (general default of REPO-040) |

### PKG-TF-013 · Include and exclude patterns for providers
Priority: **P2**

Include and exclude patterns (RMT-034 in [../remote-repositories.md](../remote-repositories.md))
restrict which upstream providers a remote repository fetches. Patterns are Ant-like, match paths of
the remote's cache layout, include everything by default and exclude nothing by default; an exclude
match wins over an include match. Example patterns address one provider by a path segment
of the form `terraform-provider-<name>` (for example `**/terraform-provider-kubernetes/**`) and
distinguish providers of HashiCorp from external ones hosted on GitHub by owner. The cache layout
the patterns match is the implementation's choice.

### PKG-TF-014 · Virtual Terraform repositories
Priority: **P2**

A virtual repository aggregates local and remote Terraform repositories behind one URL for
resolving and deploying modules and providers (REPO-033, REPO-038 in
[../repositories.md](../repositories.md)). Its settings are the same for modules and providers.
Merge rules for the same module or provider version in several members are unspecified (general
rules: REPO-035).

## Index

### PKG-TF-015 · Recalculate Terraform index
Priority: **P2**

| Operation | Inputs | Output | Required privilege |
|---|---|---|---|
| Recalculate Terraform index | repository key | plain-text confirmation | administrator (specified for the UI action) |

- Recomputes the module or provider index of a local repository, asynchronously.
- It also re-indexes a Git index and thereby the remote repositories; what that index is and which
  remote repositories are affected is unspecified.
- UI: artifact browser, context menu of the repository, "recalculate index".

## Client authentication

### PKG-TF-016 · Interactive client login
Priority: **P2**

`terraform login repo.example.com` (or `tofu login`) opens the system's login page in a browser;
after the user approves, the client receives an access token and stores it in
`~/.terraform.d/credentials.tfrc.json`. The token serves the registry and the state backend.

- Implicit protocol requirements (from the Terraform login protocol): service discovery
  announces `login.v1` with an OAuth 2.0 authorization-code flow (authorization and token endpoints,
  loopback redirect ports); the system must implement that flow and issue its own token
  (TOK-001 in [../access-tokens.md](../access-tokens.md)).
- Token lifetime and scope of tokens issued this way are unspecified.
- Anonymous access to registry and backend works when it is enabled (AUTHN-036 in
  [../authentication-providers.md](../authentication-providers.md)).

### PKG-TF-017 · Manually configured client token
Priority: **P2**

Instead of the login flow, a user creates a token (identity token, TOK-005 in
[../access-tokens.md](../access-tokens.md)) and writes it into the credentials file:

```json
{
  "credentials": {
    "repo.example.com": { "token": "<token>" }
  }
}
```

- Terraform keys credentials by host name; the host key must not be left empty.
- With a token in the file, no login is needed.
- Terraform also reads host tokens from `TF_TOKEN_<host>` environment variables (Terraform
  documentation), which suits CI jobs. Environment variables that configure a separate
  infrastructure-as-code provider are unrelated to registry authentication.

## State backend

### PKG-TF-018 · Remote state storage with workspaces
Priority: **P2**

A state-backend repository (PKG-TF-003) stores the state that Terraform keeps about the resources it
manages, so that everyone working on the same infrastructure uses the same state.

- One configuration has one backend but can have several named workspaces; each workspace has its
  own state in the same repository, without new backend configuration or credentials.
- The backend is compatible with HashiCorp's enterprise backend for Terraform, with locking, data
  encoding and smart comparisons (meaning unspecified). The protocol details (which
  service-discovery entry, which endpoints) are unspecified.

### PKG-TF-019 · State locking
Priority: **P2**

Terraform locks the state for every operation that can change it, so that no second run acquires
the lock and corrupts the state. The backend implements the lock. Lock timeouts, force-unlock and
lock information shown to users are unspecified.

### PKG-TF-020 · State encryption at rest
Priority: **P2**

| Setting | Classes | Meaning | Default |
|---|---|---|---|
| Encrypt states | local state-backend repositories | Encrypts stored state files with the instance's master encryption key (IAM-045 in [../identity-and-access.md](../identity-and-access.md)). | on |

- Losing the master encryption key makes every encrypted state unrecoverable; the key must be
  backed up separately.
- Replication of state-backend repositories between two different instances requires this setting
  to be off, because the target cannot decrypt states encrypted with the source's key.

### PKG-TF-021 · State history and state viewer
Priority: **P2**

- Every stored state version is kept as a snapshot, giving a full history of the workspace's state.
- A viewer shows the content of a state with search inside it.

Retention of old state versions, diffing between versions and the UI location are unspecified.

### PKG-TF-022 · Backend client configuration
Priority: **P2**

A client configuration block for the backend is unspecified. From the compatibility with
HashiCorp's enterprise backend (PKG-TF-018), clients configure Terraform's remote backend with the
system's host name, an organization and workspace names, authenticated by the token of PKG-TF-016
or PKG-TF-017. How organization and workspace names map onto state-backend repositories is
unspecified and must be designed.

## Companion CLI

### PKG-TF-023 · Module publishing with the companion CLI
Priority: **P2**

The companion CLI (BLD-040 in [../builds-and-ci.md](../builds-and-ci.md)) packs and publishes modules
without the `terraform` binary. It supports deployment only; resolution stays with Terraform's own
registry mechanism.

Configuration: a per-project (or global) step stores the deployment repository and the connection
profile. In non-interactive environments the existing configuration file must be removed first,
otherwise the step prompts for overwrite confirmation and blocks.

Publish behavior:

- scans the file system from the working directory; every directory with at least one `.tf` file is
  a module, packed with all its content (including submodule directories) into one zip;
- does not descend into a directory once it has been packed, so submodules are not published
  separately;
- uploads each module to `<namespace>/<module-directory-name>/<provider>/<tag>.zip`.

| Option | Meaning | Default |
|---|---|---|
| Namespace | Namespace segment of the target path. | required |
| Provider | Provider segment of the target path. | required |
| Tag | Version; becomes the file name `<tag>.zip`. | required |
| Exclusions | Semicolon-separated patterns with `*` and `?`; matching directories are not scanned and matching files are not packed. | unspecified (optional) |
| Build name, build number, module | Records the published modules as artifacts of a build run (BLD-039). | unspecified (optional) |
| Project | Project key for the build metadata. | unspecified (optional) |

## Notes for implementers

- Terraform discovers every service through `https://<host>/.well-known/terraform.json`; the
  document must be served at the host root even when the system runs under a path prefix
  (PKG-TF-005).
- Module and provider addresses carry the repository key inside the namespace segment; parsing must
  be unambiguous, so repository keys and namespaces must not contain the separator (PKG-TF-005,
  PKG-TF-007).
- The origin-registry mode fails at `terraform init` when the repository has no signing key or the
  uploaded `SHA256SUMS.sig` was made with a different key (PKG-TF-009, PKG-TF-010).
- Network-mirror mode needs only the platform zips; registry mode also needs the checksum file and
  its signature for every version.
- The companion CLI writes the tag verbatim into the file name (for example `v0.1.2.zip`);
  module versions in the registry protocol are semantic versions without a `v`, so the index must
  normalize or reject such names (behavior unspecified).
- The state backend cannot be proxied, aggregated or federated; disaster recovery relies on
  replication with encryption off or on backups that include the master encryption key
  (PKG-TF-020).
- `terraform login` needs an OAuth authorization-code flow with loopback redirects; a pure
  token-based system cannot support it (PKG-TF-016).
- Remote repositories need three upstream URLs, and HCP Terraform and GitLab upstreams need bearer
  authentication plus an allow list for module sources (PKG-TF-011, PKG-TF-012).
