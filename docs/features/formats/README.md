# Package formats: matrix and cross-format behavior

This page is the entry point for package formats. It states which formats a full-featured
repository manager supports, which repository classes (local, remote, virtual, federated) each
format offers, which native clients speak it and from which client version, what priority trove
gives it, and which page in this directory describes it in depth. It then catalogs the behavior
that all formats share, or that differs between formats only in its parameters: client families,
client version floors, client setup instructions, the package view versus the file view,
format-specific property namespaces, deploy coordinates, layouts, metadata calculation, client
authentication styles, and the instance-wide settings per format. Wire
protocols and format-specific settings live on the per-format pages (IDs `PKG-<FORMAT>-nnn`);
repository classes in general on [../repositories.md](../repositories.md), pull-through caching on
[../remote-repositories.md](../remote-repositories.md).

## Formats and repository classes

### FMT-001 · Format catalog and repository-class matrix
Priority: **MVP**

The system offers a fixed catalog of package formats, extended over time. Every repository has
exactly one format, chosen at creation and immutable afterwards (REPO-002 in
[../repositories.md](../repositories.md)). Each format has a stable lowercase identifier used in
repository documents; the UI shows a display name. Not every format exists in every repository
class. The matrix below gives, per format, the classes it supports, its native clients, the
trove priority and the format page. This entry is MVP for npm and NuGet; every other row carries
its own priority.

How the priorities were set:

- **MVP**: npm and NuGet, by the owner's constraints (INV-14 in
  [../../spec/README.md](../../spec/README.md)).
- **P1**: Docker, OCI, both Helm variants, Maven, PyPI, Go and generic, by the priority rubric in
  [../README.md](../README.md).
- **P2**: every other format. Gradle, Ivy and sbt are separate formats in the catalog with their
  own layouts and settings (REPO-047); the rubric names only Maven as P1, so the dedicated types
  are P2.
- **Later**: machine-learning model repositories whose only client is a companion Python SDK.
  There is no independent client or open protocol to be compatible with; revisit if an open
  model-registry protocol becomes a target.
- The federated column is informational: federation across sites is **Later** in the rubric
  (see [../replication-and-federation.md](../replication-and-federation.md)).

<details open>
<summary>Format matrix</summary>

Legend: **yes** = supported; **no** = not supported; **rule** = allowed by the general federation
rule (every format except an exclusion list, FMT-004) but absent from the API's federated format
set; **n/a** = the format has no local repositories, and federation is enabled on local
repositories.

| Format | Local | Remote | Virtual | Federated | Native clients | Priority | Page |
|---|---|---|---|---|---|---|---|
| npm | yes | yes | yes | yes | npm CLI, pnpm, Yarn Classic, Yarn Modern (Berry) | **MVP** | [npm.md](npm.md) |
| NuGet | yes | yes | yes | yes | NuGet CLI, .NET CLI, Visual Studio, PowerShell package management, Chocolatey | **MVP** | [nuget.md](nuget.md) |
| Docker | yes | yes | yes | yes | Docker CLI, Podman, BuildX, Kaniko, OpenShift CLI | **P1** | [docker.md](docker.md) |
| OCI | yes | yes | yes | yes | Podman, BuildX, BuildKit (buildctl), wasm-to-oci, ORAS, Helm, Cosign | **P1** | [oci.md](oci.md) |
| Helm charts (index-based chart repository) | yes | yes | yes | yes | Helm | **P1** | [helm.md](helm.md) |
| Helm charts stored as OCI artifacts | yes | yes | yes | yes | Helm | **P1** | [helm.md](helm.md) |
| Maven | yes | yes | yes | yes | Maven (`mvn`) | **P1** | [maven.md](maven.md) |
| PyPI | yes | yes | yes | yes | pip, Twine, Poetry, uv; Pipenv appears only in the companion CLI's compatibility list | **P1** | [pypi.md](pypi.md) |
| Go modules | yes | yes | yes | yes | `go` | **P1** | [go.md](go.md) |
| Generic | yes | yes | yes | yes | any HTTP client | **P1** | [generic.md](generic.md) |
| Ansible | yes | yes | yes | yes | `ansible-galaxy` (collections and roles) | **P1** | [ansible.md](ansible.md) |
| Alpine Linux | yes | yes | yes | yes | `apk` (apk-tools) | P2 | [alpine.md](alpine.md) |
| Bazel modules | yes (format page) | yes | yes (format page) | rule | Bazel (Bzlmod); own modules are uploaded as files | P2 | [bazel.md](bazel.md) |
| Bower | yes | yes | yes | yes | `bower` | P2 | [bower.md](bower.md) |
| Cargo | yes | yes | no | yes | `cargo` | P2 | [cargo.md](cargo.md) |
| Chef | yes | yes | yes | yes | `knife`, Berkshelf | P2 | [chef.md](chef.md) |
| CocoaPods | yes | yes (only through the CocoaPods CDN) | yes (only with CDN-compatible members) | yes | `pod` through the CocoaPods CDN; a deprecated plugin-based mode | P2 | [cocoapods.md](cocoapods.md) |
| PHP Composer | yes | yes | yes | yes | `composer` | P2 | [composer.md](composer.md) |
| Conan | yes | yes | yes | yes | Conan 2 (Conan 1 as legacy) | P2 | [conan.md](conan.md) |
| Conda | yes | yes | yes | yes | `conda` | P2 | [conda.md](conda.md) |
| CRAN | yes | yes | yes | yes | R (`install.packages`) | P2 | [cran.md](cran.md) |
| Debian | yes | yes | yes | yes | APT | P2 | [debian.md](debian.md) |
| Git LFS | yes | yes | yes | yes | Git LFS client | P2 | [git-lfs.md](git-lfs.md) |
| Gradle | yes | yes | yes | yes | Gradle | P2 | [jvm-other.md](jvm-other.md) |
| Ivy | yes | yes | yes | yes | Apache Ivy | P2 | [jvm-other.md](jvm-other.md) |
| sbt | yes | yes | yes | yes | sbt | P2 | [jvm-other.md](jvm-other.md) |
| Eclipse P2 | no (own P2 content is hosted in Maven or generic local repositories) | yes | yes | n/a | Eclipse | P2 | [jvm-other.md](jvm-other.md) |
| Hex | yes | yes | yes | rule | `mix` with Hex (`rebar3` is not supported) | P2 | [hex.md](hex.md) |
| Hugging Face models and datasets | yes | yes | yes | yes | `huggingface_hub` library and CLI | P2 | [huggingface.md](huggingface.md) |
| LuaRocks | yes | yes | yes | rule | `luarocks` | P2 | [luarocks.md](luarocks.md) |
| Nix | yes | yes | yes | yes (stated on the format's own page) | `nix`, `nix-env`, `nix-shell`, NixOS configuration | P2 | [nix.md](nix.md) |
| Opkg | yes | yes | no | yes | `opkg` | P2 | [opkg.md](opkg.md) |
| Dart/Flutter pub | yes | yes | yes | rule | Dart CLI, Flutter CLI | P2 | [pub.md](pub.md) |
| Puppet | yes | yes | yes | yes | `puppet`, r10k, librarian-puppet | P2 | [puppet.md](puppet.md) |
| RPM | yes | yes | yes | yes | DNF, YUM | P2 | [rpm.md](rpm.md) |
| RubyGems | yes | yes | yes | yes | `gem`, Bundler | P2 | [rubygems.md](rubygems.md) |
| Swift | yes | yes | yes | rule | Swift Package Manager | P2 | [swift.md](swift.md) |
| Terraform/OpenTofu registry (modules, providers) | yes | yes | yes | rule | Terraform CLI, OpenTofu CLI | P2 | [terraform.md](terraform.md) |
| Terraform state backend | yes | no | no | no (excluded by default, FMT-004) | Terraform CLI, OpenTofu CLI | P2 | [terraform.md](terraform.md) |
| Vagrant | yes | no | no | yes | `vagrant` | P2 | [vagrant.md](vagrant.md) |
| Version-control archives | no | yes | no | n/a | any HTTP client | P2 | [vcs.md](vcs.md) |
| Model-serving containers (NVIDIA NIM) | no | yes | no | n/a | Docker | P2 | [ml-models.md](ml-models.md) |
| AI editor extensions (VS Code marketplace proxy) | no | yes | no | n/a | VS Code, Cursor, Kiro, Devin (formerly Windsurf) | P2 | [ai-and-ide.md](ai-and-ide.md) |
| JetBrains IDE plugins | no | yes | no | n/a | JetBrains IDEs | P2 | [ai-and-ide.md](ai-and-ide.md) |
| AI agent plugins | yes | no | no | rule | Claude Code natively; Cursor, Codex and VS Code only through the companion CLI | P2 | [ai-and-ide.md](ai-and-ide.md) |
| AI agent packages | yes | no | no | yes (stated on the format's own page) | Agent Package Manager (`apm`) | P2 | [ai-and-ide.md](ai-and-ide.md) |
| Agent skills | yes | no | no | rule | ClawHub-compatible clients, the companion CLI | P2 | [ai-and-ide.md](ai-and-ide.md) |
| Machine-learning models (companion SDK) | yes | no | no | rule | only a companion Python SDK | Later | [ml-models.md](ml-models.md) |

</details>

Basis of the class columns and their open questions:

- Local, remote and virtual follow the client-support table of REPO-006 in
  [../repositories.md](../repositories.md). Hex, Nix, the machine-learning models, the
  model-serving containers and agent skills are missing from that table; their rows come from the
  formats' own pages (Hex and Nix: all three classes; machine-learning models and skills: local
  only; model-serving containers: remote only, and local and virtual are explicitly unsupported).
- Bazel modules is the one row where the table and the format's own page disagree: the table says
  remote only, while the format page describes local repositories for an organization's own
  modules, remote repositories for the central registry and virtual repositories over both. The
  matrix follows the format page (as [bazel.md](bazel.md) does). The reference configuration
  lists Bazel modules for local, virtual and federated but not remote.
- The API's per-class format sets disagree on some rows (REPO-006 lists them): the virtual set omits
  CocoaPods; no set lists Bazel modules, Eclipse P2, LuaRocks, Hex, Nix or the AI formats.
  Open: the spec decides which of the two governs.
- Federated: "yes" means the format is in the API's federated set, or, where marked, that the
  format's own page states federated support (Nix; AI agent packages, whose page names local and
  federated repositories as the only supported classes). The federation pages state the broader
  rule of FMT-004; the gap (pub, Swift, Terraform registry, Bazel modules, LuaRocks, Hex, the other
  AI formats, machine-learning models) is unresolved.
- The client columns combine the catalog's client aliases (for example Yarn under npm, Chocolatey
  and PowerShell under NuGet, ORAS and WebAssembly modules under OCI, Flutter under pub, OpenTofu
  under Terraform) with the "supported clients" statements of the format pages. Version floors
  are in FMT-003.

Operations: no operation lists the format catalog itself; the per-class format sets are part of
the repository document schema (REPO-006, REPO-053 in [../repositories.md](../repositories.md)).

UI location: administration area, repositories, "create repository": first the class, then the
format. Whether the format picker hides formats the chosen class does not support is unspecified.
Creating a "Helm" repository yields the OCI-based chart variant by default; the form offers a
button to create the index-based variant instead, and a system-wide switch (FMT-014) flips the
default back to the index-based variant.

Edge cases: some format lists also name repository types that are not package formats (a build
metadata repository, release-set storage). Those are covered in
[../builds-and-ci.md](../builds-and-ci.md) and REPO-066.

Interactions: repository classes REPO-001, quick setup REPO-008, federation FMT-004, the
container-only profile FMT-005, system-wide package settings FMT-014.

## Native clients

### FMT-002 · Several native clients per format
Priority: **MVP**

A format is a wire protocol plus a storage and metadata model, not a client. Several client
programs can speak the same format, and the system serves all of them from the same repository and
the same endpoint; no repository setting selects a client. The client matters in three places:

- client setup instructions offer a client picker for formats with several clients (FMT-006,
  UI-021 in [../ui-inventory.md](../ui-inventory.md));
- package details render one install command per client (UI-010), for example both Docker and
  Podman for a container image;
- clients differ in how they authenticate (FMT-013): some need a token in their configuration
  file, some read credentials only from a `.netrc` file, a few accept credentials only inside the
  URL, and one (JetBrains IDEs) cannot authenticate at all.

<details>
<summary>Client families per format</summary>

| Format | Clients served by the same repository | Remarks |
|---|---|---|
| npm | npm CLI, pnpm, Yarn Classic, Yarn Modern | All four are MVP (INV-14). Login differs for scoped and unscoped registries. |
| NuGet | NuGet CLI, .NET CLI, Visual Studio, PowerShell package management, Chocolatey | Setup covers both feed protocols, v3 and v2, for the NuGet CLI, the .NET CLI and Chocolatey. Client setups that depend on v2 are P2 (INV-14). |
| PyPI | pip, Twine, Poetry, uv | pip installs only; Twine publishes only; Poetry and uv do both. Pipenv appears only in the companion CLI's compatibility list. |
| Docker | Docker CLI, Podman, BuildX, Kaniko, OpenShift CLI | Docker CLI and Podman are the native clients. Kaniko and the OpenShift CLI are specified only for pushes driven by the companion CLI, which also records build metadata. |
| OCI | Podman, BuildX, BuildKit (buildctl), wasm-to-oci, ORAS, Helm, Cosign | WebAssembly modules, arbitrary OCI artifacts, charts and signatures share one registry surface. |
| Helm (index-based) | Helm | Separate format from charts on OCI. |
| Helm charts on OCI | Helm | Same registry surface as Docker and OCI. |
| Dart/Flutter pub | Dart CLI, Flutter CLI | |
| Terraform/OpenTofu | Terraform CLI, OpenTofu CLI | Registry (modules, providers) and state backend are two formats. |
| Puppet | `puppet`, r10k, librarian-puppet | |
| Chef | `knife`, Berkshelf | |
| RubyGems | `gem`, Bundler | Bundler's compact index is supported once it is switched on system-wide (see [rubygems.md](rubygems.md)). |
| Conan | Conan 2, Conan 1 | Conan 1 is legacy. |
| RPM | DNF, YUM | |
| Nix | `nix`, `nix-env`, `nix-shell`, NixOS configuration | Binary cache and channels. |
| AI editor extensions | VS Code, Cursor, Kiro, Devin (formerly Windsurf) | Editors built on VS Code, through their extension marketplace setting. |
| AI agent plugins | Claude Code, Cursor, Codex, VS Code | Only Claude Code installs natively from the repository's marketplace index; the others, and every publish, go through the companion CLI. |
| JetBrains IDE plugins | JetBrains IDEs | The IDEs cannot authenticate; every access is anonymous and read-only. |
| Hex | `mix` | `rebar3` is not supported. Publishing to a local repository with the client's own publish command is not supported; packages are deployed over HTTP instead. |
| Model-serving containers | Docker | The model vendor's own CLI and SDK are not supported. |

</details>

Format families that share infrastructure:

- Docker, OCI and Helm-on-OCI share one registry API surface (FMT-005 names this as the reason the
  container-only profile can serve all three).
- Maven, Gradle, Ivy and sbt are separate formats with separate default layouts (REPO-047).
- Model-serving containers need a remote Docker repository that points at the model vendor's
  container registry as a prerequisite; the format's own remote repository proxies the model
  catalog API.

Edge cases: whether a client that is not listed works is unspecified. The Hex client accepts only
one repository that stands in for the public registry, and one Hex virtual repository per client
(see [hex.md](hex.md)).

Interactions: FMT-003 (versions), FMT-006 (setup), FMT-013 (authentication styles), UI-010 and
UI-021 in [../ui-inventory.md](../ui-inventory.md).

### FMT-003 · Client version floors and validated client versions
Priority: **MVP**

For many clients the system states a minimum client version; older clients may fail against it.
These floors are documentation only. Whether the server detects or rejects older clients is
unspecified. Only some floors have a stated reason (the sparse index for Cargo, OCI support in
Helm, registry publishing in Swift, the Xet storage protocol for Hugging Face, context paths for
Puppet); for the rest the reason is unspecified. Separately, npm 9 and later dropped a legacy
Basic-credential property on the client side, which changes the setup, not the floor.

<details>
<summary>Stated minimum client versions</summary>

| Format | Client | Stated minimum | Remarks |
|---|---|---|---|
| npm | npm CLI | 1.4.3 | From npm 9 the client no longer accepts the legacy Basic-credential property in its configuration file; use `npm login` or a token. |
| npm | pnpm, Yarn | unspecified | |
| NuGet | NuGet CLI | 3.4.4 | From 3.5, symbol packages publish without a separately configured symbol source. |
| NuGet | PowerShell package management | 1.0.5 | |
| NuGet | Chocolatey | 1.2.0 | |
| NuGet | .NET CLI, Visual Studio | unspecified | |
| PyPI | pip | 20.2 | install only |
| PyPI | Twine | 3.3.0 | publish only |
| PyPI | Poetry | 1.2.0 | |
| PyPI | uv | 0.8.15 | |
| Docker | OpenShift CLI | 3.0.0 | Stated for the companion CLI's build wrapper around the OpenShift CLI, not for direct pushes. |
| Docker | Docker CLI | unspecified | A legacy Docker V1 protocol mode is mentioned. |
| OCI | Cosign | 2.0.0 | Older Cosign needed an extra server switch to work with OCI 1.1. |
| Helm (index-based) | Helm | 2.9.0 | Support for Helm below 3.0.0 is being discontinued. Relative chart URLs in the index work only with Helm 3 and later. |
| Helm on OCI | Helm | 3.8.0 | Earlier versions work only with Helm's experimental OCI mode enabled. |
| Go | `go` | 1.11.0 | |
| Alpine Linux | apk-tools | 2.10.3 | Alpine Linux 3.9.6 or later. |
| Ansible | Ansible Core | 2.17 | Ansible community package 10.x or later. |
| Bower | `bower` | 1.5 | Clients below 1.5 need two extra npm helper packages and are supported only by old releases of the system (see [bower.md](bower.md)). |
| Cargo | `cargo` | 1.49.0 | Sparse index: 1.60 to 1.67 with the unstable sparse-registry flag, stable from 1.68. |
| PHP Composer | `composer` | 2.x | The public registry stopped serving Composer 1 on 2025-09-01. Composer 1 keeps working against local repositories until December 2026. |
| Conan | Conan | 2 recommended | Conan 1 supported as legacy. |
| Hex | Hex / Elixir / OTP | 2.1.1 / 1.16 / 26.2.5.6 | |
| Hugging Face | `huggingface_hub` | 0.19.0 | 0.30.0 for the Xet storage protocol; older clients cannot download files uploaded through Xet. Xet also has a ceiling: it is stated for `huggingface_hub` up to 1.18.0 with `hf_xet` up to 1.4.3; later versions work only with newer server releases. Starting with 1.20.0 the client sends big files over Xet whatever the server switch says, so downloads of those files fail while Xet is off on the server. |
| AI agent packages | `apm` | 0.23.0 | |
| Dart/Flutter pub | pub | 2.15.0-268.8.beta | |
| Puppet | `puppet` | 4.9.2 (or 5.x) | Up to 4.9.1 the client cannot use a repository URL that contains a context path. |
| Puppet | r10k | 5.4.5 | |
| RPM | YUM / DNF | 4.18.2 / 4.19.2 | |
| Swift | Swift | 5.9 for registry publishing | 5.8 and earlier publish with a raw HTTP upload. From 5.9, Swift Package Manager resolves from Git by default; resolving through a remote or virtual repository needs a flag that replaces source-control dependencies with registry ones. Xcode does not apply that replacement. |
| Terraform | Terraform CLI | 1.0.0 | OpenTofu: unspecified. |
| Model-serving containers | model container | 1.3 | The model containers themselves, not a client. |
| every other format | — | unspecified | |

</details>

The companion CLI also publishes a compatibility list: an officially supported range per
client and, for others, the latest version verified to work. These versions validate the wrapper
CLI driving the client, not the server alone. They are useful as the set of current client
versions to test against, as of late October 2026:

<details>
<summary>Validated client versions (companion CLI list)</summary>

| Client | Officially supported range | Latest version verified |
|---|---|---|
| Conan | 2.10.2 – 2.28.1 | 2.33.0 |
| Gradle | 5.6.4 – 8.3 | 9.6.0-RC3 |
| Hugging Face Hub CLI | 1.19.0 – 1.29.0 | 2.3.0 |
| Maven | 3.8.8 | 3.8.8 |
| NuGet / .NET SDK | 8.0.417 | 10.0.401 |
| Pipenv | 2023.7.4 – 2026.6.1 | 2026.10.3 |
| Go | — | 1.27.3 |
| Helm | — | 4.3.1 |
| Nix | — | 2.35.4 |
| npm | — | 12.2.1 |
| pip | — | 26.2.2 |
| pnpm | — | 12.16.0 |
| Poetry | — | 2.5.1 |
| uv | — | 0.13.6 |
| Yarn Modern (Berry) | — | 4.18.1 |

</details>

Edge cases: the same list names client versions that failed the wrapper's tests (several npm 11
and 12 releases, pnpm 10 and 11, Gradle 9, Maven 3.9 and 4.0 release candidates, among others).
Those are failures of the wrapper's integration. Nothing ties them to the server, so they are not
server constraints.

Interactions: FMT-002; per-format client sections on the format pages.

## Format availability by deployment

### FMT-004 · Format eligibility for federation
Priority: **Later**

Federation is enabled on local repositories. It is available for every format except those on a
system-level exclusion list. Supported examples: generic, Maven, Gradle,
npm, Docker, OCI, Helm, PyPI, NuGet, Go, Conan, RPM, Debian, Terraform, and the build metadata
repository type.

| Setting | Meaning | Default |
|---|---|---|
| Formats excluded from federation | System-level, comma-separated list of format identifiers for which federated repositories cannot be created | the Terraform state backend format |

- UI location: the format is chosen on the federated repository form. Whether excluded formats are
  hidden there or rejected on save is unspecified.
- Operations: none specific to this setting. It is a system-level setting outside
  the repository API.

Edge cases: the API's federated format set is narrower than the rule. It omits pub, Swift, the
Terraform registry, Bazel modules, LuaRocks, Hex, Nix, the AI formats and machine-learning models,
while the federation page names Terraform as supported, the reference configuration lists Bazel
modules, and the Nix and AI agent package pages state federated support (FMT-001). Cargo packages
embed the URL of the registry they were published to, so resolving across federation members
needs client-side source replacement (see [cargo.md](cargo.md)).

Interactions: federation in [../replication-and-federation.md](../replication-and-federation.md);
the container-only profile has no federation (FMT-005); client setup for federated repositories
(UI-027 in [../ui-inventory.md](../ui-inventory.md)).

### FMT-005 · Container-only deployment profile
Priority: **Out of scope**

A self-hosted deployment profile limited to container and chart workflows. The profile is
packaging, not a capability, so trove does not need it. Its observable behavior is recorded so
the inventory stays complete:

- Formats: Docker, OCI, Helm (index-based), Helm on OCI and generic only. The setup wizard and the
  repository screens offer only these, and creating a repository of any other format is blocked.
  Docker, OCI and Helm on OCI share one registry API surface.
- Classes: local, remote (including a remote whose upstream is another instance of the same
  repository manager) and virtual. Several registries of one format can run side by side, for
  example per team or per promotion stage. No replication, no federation.
- First run: the setup wizard makes the administrator replace the initial password (at least 8
  characters with upper case, lower case and a digit), then asks for the base URL and an optional
  default outbound proxy, and finally which of the five formats to use. It creates one default
  local, remote and virtual repository for each selected format (compare REPO-008 in
  [../repositories.md](../repositories.md)).
- Clients: the same as the full profile: Docker, Podman, Helm 3, ORAS and the companion CLI's
  container commands.
- Included beyond formats: content-addressed storage with deduplication; include and exclude
  patterns; copy, move and delete in the UI; the full REST API for the supported formats; search
  by name, archive content, property and checksum; properties; the query language; local disks and
  cloud object storage (S3, Google Cloud Storage, Azure Blob); LDAP login with role-based users,
  groups and permissions; garbage collection and backups; publishing build metadata from CI.
- Excluded: high availability and multi-node clusters, storage sharding, vulnerability scanning,
  build promotion, demotion and cleanup, release sets, distribution to edge instances, identity
  federation, replication, server-side plugins, SAML, Crowd, LDAP groups, several LDAP
  directories and further identity providers.
- It upgrades in place to a newer version of itself or to the full profile.
- A second reduced profile is limited to Maven, Gradle, Ivy, sbt and generic. Compared with the
  container-only profile it has no container formats, no query language, no property
  search and no cloud object storage, and it can use only the bundled layouts without layout
  conversion.

Interactions: FMT-001; quick setup REPO-008 and UI-033.

## Client setup

### FMT-006 · Client setup instructions across formats
Priority: **MVP**

For every format, and for every client of a multi-client format, the system generates
ready-to-use setup text for a chosen repository. The dialog itself (entry points, flow, credential
step, templating) is UI-021 to UI-027 in [../ui-inventory.md](../ui-inventory.md). This entry
states the contract each format has to fulfil:

| Part | Content | Condition |
|---|---|---|
| Configure | Register the repository endpoint and credentials in the client's configuration | always; the credential step (UI-022) sits on this tab |
| Further tabs, for example publish and install | Commands or configuration to upload or fetch packages | vary per format and client; when a publish tab is offered (for example for a remote repository) is unspecified |
| Per-version install command | On the package details, one command per client and per repository holding the version | UI-010 |

- The set of tabs, their names and their content vary per format and client.
- A client picker appears only for formats with several clients (FMT-002).
- Prerequisite: a repository of the format exists. Without one there is nothing to configure.
- Credentials: a newly generated identity token or a token the user supplies is embedded in the
  snippets (UI-022). Formats whose clients need an explicit credential in the install command
  prompt for token generation first (UI-010).
- Federated repositories: the generated address always points at the local member. Resolve
  snippets require read permission, deploy snippets require deploy permission (UI-027).
- Besides the dialog, every format has full manual connection instructions. In this catalog those
  are the client sections of the format pages.

<details>
<summary>Setup steps beyond "address plus credential" that the instructions must cover</summary>

| Format | Extra step |
|---|---|
| npm | Separate login for scoped and unscoped registries; npm 9 and later need a token or `npm login`, not the legacy Basic property (FMT-003). |
| Docker, OCI, Helm on OCI | Registry login before push or pull. |
| Hex | Download the repository's public key into the project. A self-hosted Hex repository's name in the client must equal the remote repository's full URL. Only one repository may stand in for the public registry, and one client can use only one Hex virtual repository. |
| CRAN | The R client accepts credentials only inside the repository URL (password or reference token). |
| LuaRocks | No interactive prompt: install credentials go into the client configuration or `.netrc`; upload credentials only inside the server URL of the upload configuration. |
| Nix | The client never prompts for credentials, so they must be configured beforehand (`.netrc`). |
| Cargo | Sparse HTTP index only: creating or updating a repository on the legacy Git index is blocked. Clients up to 1.67 that do not use the sparse index send no credentials for install and search (FMT-013). |
| CocoaPods | Only the CocoaPods CDN mode is configurable; the plugin-based mode is deprecated. An existing repository must be reindexed once to create the CDN index structure before clients connect (FMT-012). Credentials go into `.netrc`. Virtual repositories accept only CDN-compatible members. |
| Swift | From Swift 5.9, resolving through a remote or virtual repository needs a flag that replaces Git dependencies with registry ones; Xcode does not apply it, so `Package.swift` must declare registry identities. |
| Puppet | Clients up to 4.9.1 need a reverse-proxy rewrite workaround for the context path. |
| JetBrains IDE plugins | No credentials at all: the IDEs connect anonymously (FMT-013). |

</details>

Security, per case: every snippet carries a live credential (UI-022). For CRAN the credential sits
inside the repository URL, so it also ends up wherever the R configuration or command line is
logged or shared. That is live whenever a CRAN repository requires authentication. The same holds
for LuaRocks uploads, whose credential sits in the server URL of the upload configuration.

Operations: no API that renders setup text is specified. The UI uses the token
operations of [../access-tokens.md](../access-tokens.md).

Interactions: FMT-002, FMT-013, UI-021 to UI-027.

## Packages, files and properties

### FMT-007 · Package view and file view
Priority: **MVP**

The system shows the same stored content in two ways:

- **File view**: the repository tree by path, with one node per folder and file (ART-040 in
  [../artifacts.md](../artifacts.md)).
- **Package view**: a list of packages and their versions, built from the format's own metadata.
  It spans all repositories (ART-048, ART-049; UI-008, UI-009 in
  [../ui-inventory.md](../ui-inventory.md)).

What makes a package (ART-001):

| Upload | Result |
|---|---|
| Any file in a generic repository | A file only; no package entry |
| A package file in a repository of its format | A file plus a package entry, named from the package's metadata (for example the manifest of an npm tarball) |
| A multi-part package pushed with the native client (for example a container image: manifest plus layers) | The system assembles one package from the parts and tracks which files belong to it |

Package identity per format comes from different sources. Most formats read a manifest inside the
package. LuaRocks reads file names plus the rockspec. Nix derives name and version from store
paths. Skills read a metadata file in the package, which is parsed only up to 5 MB. AI agent
plugins take name and version from the folder path, not from the manifest inside the archive.

Behavior and edge cases:

- When one package version is stored in several repositories, its details list every repository
  holding it, and the install command lets the user pick one (UI-010).
- Moving one file of a multi-part package by hand strips it of its package context and can break
  the package.
- The package record is extracted at upload time and not refreshed when files are later changed by
  hand. A file that is renamed by hand and uploaded again carries its new name in the file view,
  while the package view keeps the name from the metadata recorded at upload (SRCH-001 in
  [../search-and-query.md](../search-and-query.md)).
- The package view is for browsing, not resolution. Stated explicitly for LuaRocks and skills: it
  does not replace the client's own resolution rules.
- Version ordering is only as good as the format's versioning. For Nix, versions are free-form, the
  "latest version" is best effort, versions that extract to the same string are aggregated into one
  entry, and the displayed name can differ from the install name.
- A "release versions only" filter exists where a format distinguishes pre-releases. Download
  counts for container images are per tag (ART-029).
- For remote repositories the file view shows only what has been cached. Hex and Nix state that the
  upstream registry cannot be browsed (ART-044).
- For recognized package files, the file view adds a format-specific information tab: stated for
  Docker, Debian, CRAN, Conda, PyPI, Helm and Helm on OCI. The Debian tab also shows why a package
  was not indexed (FMT-008).

Operations: the operations behind package listing and details are unspecified (UI-008).
File-level operations are in [../artifacts.md](../artifacts.md).

Interactions: ART-001, ART-040, ART-048, ART-049, SRCH-001, SRCH-051, FMT-008.

### FMT-008 · Format property namespaces
Priority: **MVP**

When a package is deployed, the system extracts identifying facts from its metadata and writes
them as properties on its files. The npm and Puppet pages state the same for packages cached by a
remote repository. Most of these properties use keys that share a prefix reserved for the format:
a short form of the format's name, then a dot. Vagrant's coordinate keys carry no such prefix.
They feed property search (SRCH-007), the package search fields per format (SRCH-050), the query
language, and anything else that filters on properties, such as cleanup policies.

<details>
<summary>Facts recorded as properties, per format</summary>

| Format | Facts | Remarks |
|---|---|---|
| npm | name, version | Deployed and cached packages. |
| NuGet | package ID, version | Re-annotated from the package manifest by the NuGet recalculation (FMT-012). |
| PyPI | name, version, summary | Set from the uploading client's data, or later during indexing for the archive types that can be read (zip, tar, tgz, tar.gz, tar.bz2, egg, wheel). A file uploaded through the UI or the plain HTTP deploy needs name and version supplied as properties (FMT-009). |
| Puppet | name, version | Deployed and cached modules. |
| Alpine Linux | name, version, branch, repository, architecture | Written on every file of the package. |
| Debian | entries of the control file, including distribution, component and architecture | Written on new deploys. Existing files get them only through the Debian recalculation, which writes them by default (FMT-012). A package that fails metadata validation is not indexed and receives a status property with the reason. A remote repository can also derive distribution, component and architecture for its cached packages (FMT-012). |
| Docker | the image's repository name (for example `library/ubuntu`), among other properties | |
| Vagrant | box name, provider, version | Supplied by the deployer (FMT-009); keys without a format prefix. |
| Agent skills | name, version, description, author, tags, display name, summary, change log, fingerprint, and more | |
| other formats | fields listed per format in SRCH-050 (for example Maven group and artifact IDs, Conan user and channel, Helm app version) | |

</details>

Behavior:

- Properties exist on files in local repositories and in the caches of remote repositories.
  Virtual repositories show them read-only (ART-021 in [../artifacts.md](../artifacts.md)).
- Recalculation can back-fill properties for content deployed before a format began writing
  them. Debian and Opkg recalculation take a "write properties" switch (FMT-012).
- General property limits apply: key up to 255 characters, value up to 2400 characters (ART-021).

Edge cases: whether users can overwrite or delete these system-written properties is unspecified.
Debian, Vagrant and manual PyPI uploads even expect deployers to supply some of them (FMT-009).
As a general rule, property values are guidance and are not strictly enforced.

Security, per case: package search, cleanup policies and query-language filters trust these
properties. If a principal with annotate permission can rewrite them, it can change how a package
is found and selected (for example its name or version as seen by search and cleanup) without
changing the binary. This is live wherever the namespace is not write-protected; whether it
is write-protected is unspecified.

Interactions: ART-003 (properties at deploy time), ART-021 to ART-026, SRCH-007, SRCH-050,
FMT-009, FMT-012.

### FMT-009 · Format-aware deploy coordinates
Priority: **P2**

Some formats need coordinates at deploy time that the file itself does not carry, or carries in a
form the indexer does not read. The system takes them either as properties attached to the deploy
request (matrix parameters, ART-003), or as extra fields in the UI deploy dialog that rewrite the
target path:

| Format | Coordinates | Rules |
|---|---|---|
| Debian | distribution, component, architecture | Given as properties on the deploy path or as fields of the UI's "deploy as Debian artifact" option. A key may repeat to assign several values (for example several architectures). An architecture value meaning "all" places the package in an architecture-independent layout. |
| Vagrant | box name, box provider, box version | All three are mandatory properties on the deploy path. Further properties are optional. The file may sit anywhere in the repository. |
| PyPI | name, version | Required as properties when a package is uploaded through the UI or a plain HTTP deploy instead of a PyPI client. Indexing tries to extract them from the file, which works only for the readable archive types (FMT-008). |
| Git LFS | — | The UI option "deploy as Git LFS artifact" computes the target path. |
| CRAN | — | The UI computes the target path from the uploaded file. Changing it by hand can prevent metadata calculation, leaving the package unindexed. |
| Alpine Linux | — | Packages must be deployed under a branch/repository/architecture folder structure, or they are not indexed. |
| Hex | — | Local repositories accept packages only under their tarballs folder. |
| LuaRocks | — | Rocks and rockspecs go to the repository root. |
| RubyGems | — | Gems must be deployed under a `gems` folder, or indexing ignores them. |
| AI agent packages | — | Archives must follow an owner/package/package-version layout without subfolders; uploads that do not are rejected. |

Edge cases: a file deployed outside the structure its format's indexer expects is usually stored
but not indexed. It appears in the file view but not in the package view or the client's index.
A few formats reject such uploads instead: AI agent packages always, Helm when its enforced layout
is on (FMT-010).

Interactions: FMT-008, FMT-010, FMT-011; deploy operations in [../artifacts.md](../artifacts.md).

## Layouts

### FMT-010 · Default layout per format and layout mapping
Priority: **P1**

Every repository references a layout that tells the system how module information (organization,
module, revision, classifier, extension) is encoded in paths (REPO-012, REPO-046 in
[../repositories.md](../repositories.md)). Formats relate to layouts as follows:

- **Default layout per format**: when a repository is created in the UI, the system preselects the
  layout recommended for the chosen format. Bundled layouts exist for Ansible, Bower, Cargo,
  Composer, Conan, Go, Gradle, Ivy, Maven 1, Maven 2/3, npm, NuGet and Puppet, plus a "build"
  layout and an "npm test" layout whose purpose is unspecified (REPO-047). For the remaining
  formats, which layout is preselected is unspecified on this page; see each format page.
- **Repositories without a chosen layout**: repositories that predate custom layouts receive the
  Maven 2 layout, and the API applies it by default "where applicable" (REPO-012). A layout is
  mandatory for local repositories and for the cache of a remote repository; the upstream layout
  of a remote and the layout of a virtual repository are optional.
- **Mapping between layouts**: a remote repository can declare a different layout for its upstream
  than for its cache. Requests are then translated from the cache layout to the upstream layout. A
  virtual repository with a layout translates requests into each member's layout. Copy and move
  between local repositories with different layouts translate paths unless suppressed. The request
  passes through untranslated when module information cannot be built, when it cannot be mapped,
  or when either side has no layout (REPO-051, REPO-052).
- **Format-enforced structures**: several formats enforce their own path rules on top of, or
  instead of, a configurable layout. Examples: Helm's enforced layout rejects a second path for the
  same chart name and version, and rejects charts whose file name disagrees with the chart
  metadata or whose version is not semantic versioning 2; it is set when a repository is created
  (per repository, or for all Helm repositories through a system-wide switch that cannot be undone
  per repository) and does not apply to repositories created before the feature existed. Others are
  PyPI's enforced layout and file-name normalization, NuGet's enforced layout (REPO-030, REPO-031),
  Debian's deprecated "trivial" layout (no source packages, no acquire-by-hash, and not allowed as
  a virtual repository member), and the Terraform registry type that selects the layout
  (REPO-032). Details on the format pages.

Settings: the layout reference per repository (REPO-012); layout definitions (REPO-048 to REPO-050).
No setting maps formats to default layouts. The mapping is built into the UI's
recommendation.

Edge cases: a layout with custom tokens but without a module token breaks some API operations
(REPO-048). For a package version deployed twice under different paths, Helm keeps only the latest
path in its index; the earlier file stays in the repository but is invisible to clients.

Interactions: REPO-012, REPO-046 to REPO-052, FMT-009, latest-version search by layout (SRCH-017 in
[../search-and-query.md](../search-and-query.md)).

## Metadata calculation

### FMT-011 · Automatic metadata calculation
Priority: **MVP**

Formats whose clients read a server-side index (a package list, a manifest, a repository metadata
file) have that index computed by the system from the packages it holds. Clients never upload the
index. For local repositories the calculation runs automatically, except that RPM has a
per-repository switch for it.

- **Triggers**: deploying, deleting, copying or moving a package of the format (stated for NuGet,
  Conda, CRAN, LuaRocks and RPM), and content import at system or repository level (stated for
  RPM). Assigning a signing key to a repository triggers a recalculation that also signs every
  package (npm).
- **Timing**: the calculation starts only after the triggering action has completed and runs
  asynchronously; how long it takes depends on system load. Stated figures: up to about 30 seconds
  (Conda, CRAN), about one minute until a deploy appears in the index (Debian, Opkg), and for
  PyPI indexing starts only after a 60-second quiet period counted from the last upload to the
  repository. npm and Puppet build their search index asynchronously after publishing to a local
  repository. RubyGems indexes in the background.
- **Scope per class**: local repositories (RPM only with its switch on). Remote repositories
  annotate cached packages (npm, Puppet) and, for Swift and Composer, validate upstream metadata.
  Virtual repositories
  aggregate their members' metadata (REPO-037): stated for Conda, changes in local members show up
  at once, while metadata from remote members is renewed on demand, no more often than every 10
  minutes by default, governed by the virtual repository's metadata cache period. REPO-037 gives a
  different default for that period. Open: which of the two defaults applies; the spec decides.
- **Formats with server-side calculation stated**: Alpine Linux, Ansible, Bower, Cargo, Chef,
  CocoaPods, Composer, Conda, CRAN, Debian, LuaRocks, NuGet (including symbol packages), npm,
  Opkg, pub, Puppet, RPM, RubyGems, Swift, Vagrant, AI agent packages (from their manifest files),
  Nix. Automatic triggering is stated explicitly only for Alpine Linux, Conda, CRAN, Debian,
  LuaRocks, NuGet, Opkg, RPM (when its switch is on) and RubyGems; for the others only the
  calculation itself is specified. For formats not listed it is unspecified here; see each
  format page.

| Setting | Meaning | Default |
|---|---|---|
| Metadata worker threads (Conda) | System-level number of workers that calculate Conda metadata in parallel | 5 |
| Automatic metadata calculation (RPM, per local repository) | Whether deploy, delete, copy, move and import trigger the RPM calculation | unspecified |
| Metadata folder depth (RPM, per local repository) | Directory level below which the system looks for packages and writes the repository metadata folder; 0 means the repository root, a higher value gives one metadata set per sub-tree (for example per package name) | 0 |
| File-list indexing (RPM, per repository) | Whether the per-file list index of all packages is calculated; costly on large repositories. Without it, clients cannot find which package provides a file. A virtual repository's file list is incomplete if any member has it off. | off for new repositories |
| Metadata validation (Debian, system-level) | Rejects packages with empty required metadata fields from the index (FMT-008) | on |

Edge cases:

- RPM calculation replaces repository metadata that was deployed by hand or imported, including
  database-format metadata files.
- The asynchronous delay is visible to clients: a CI job that publishes and immediately installs
  can miss the new version for seconds up to a minute or more (PyPI waits a 60-second quiet
  period before it even starts). The npm and Puppet pages say so explicitly: a package just
  published can stay invisible to search until indexing completes, also through a virtual
  repository that contains the local one; through a remote repository it appears only after the
  next upstream check allowed by the metadata retrieval cache period (RMT-018 in
  [../remote-repositories.md](../remote-repositories.md)). Whether any format holds the publish
  response until its index is updated is unspecified.

Operations: none; the calculation is internal. Manual recalculation is FMT-012.

Interactions: FMT-008 (properties written during calculation), FMT-012, REPO-037, per-format
metadata entries (for example PKG-NPM-029 in [npm.md](npm.md), PKG-NUGET-006 in
[nuget.md](nuget.md)).

### FMT-012 · Manual metadata recalculation
Priority: **P1**

An operator can recompute a repository's index and format metadata from the packages it holds, for
example after a bulk import, a layout change, a corrupted index or a configuration change.
Availability, scope and options differ per format. The operation is expensive on large
repositories (REPO-009): a Conda reindex can take up to a few hours.

UI locations (vary per format):

- administration area, repositories list, row action "recalculate index" (Ansible, RPM; REPO-009);
- artifact browser, right-click on the repository, "recalculate index" (Cargo, CocoaPods, Conda,
  CRAN, LuaRocks, pub, Swift, Terraform);
- a "reindex packages" action whose location is unspecified (NuGet, CRAN).

Privilege: administrator (stated for Ansible, Cargo, CRAN, pub, Terraform); administrator or
project administrator (Swift); otherwise unspecified. The API operations answer 401 for bad
credentials and 403 for missing permission.

<details>
<summary>Recalculation operations per format</summary>

Every operation except the Maven index calculation takes one repository key in its path. Each
answers 200 with a plain-text body on success, 401 for bad credentials, 403 for missing permission
and 500 on failure.

| Format | Scope and inputs | Classes | Mode | Extra options |
|---|---|---|---|---|
| Alpine Linux | whole repository | local | asynchronous | — |
| Bower | whole repository | unspecified | unspecified | — |
| Cargo | whole repository; also indexes remote repositories | local, remote | synchronous by default | asynchronous flag |
| CocoaPods | whole repository; also creates the structure CDN clients need | unspecified | unspecified | — |
| Conan | whole repository | local | synchronous by default | asynchronous flag |
| Conda | whole repository | local | synchronous by default | asynchronous flag |
| CRAN | whole repository | local | synchronous by default | asynchronous flag |
| Debian | whole repository: package lists and release files | local | synchronous by default | asynchronous flag; write-properties flag (0 or 1) |
| Debian, remote caches | package coordinates of cached packages | remote cache only | unspecified | — |
| Helm (index-based) | whole repository | local, federated | unspecified | — |
| Helm, partial | a path that holds several charts, or a single chart archive | unspecified | unspecified | path |
| Maven index | a list of repositories; for a virtual repository, the members to include in the aggregated index | unspecified | unspecified | repository list; force flag (0 or 1) |
| Maven metadata | a folder path; the calculation fails unless a POM exists in the repository | local | unspecified | a non-recursive flag (its default is open, see PKG-MAVEN-015 in [maven.md](maven.md)) |
| npm | whole repository | local | unspecified | — |
| NuGet | whole repository; re-annotates each package's properties from its manifest | local, remote and its cache, virtual | unspecified | — |
| Opkg | whole repository, each feed location | local | synchronous by default | asynchronous flag; write-properties flag (0 or 1) |
| pub | whole repository | local, remote | asynchronous (an asynchronous flag is nonetheless listed) | asynchronous flag |
| PyPI | whole repository: package and repository indexes | local | synchronous by default | asynchronous flag |
| RubyGems | whole repository: re-creates all spec index files | unspecified | unspecified | — |
| RubyGems, update | whole repository: updates the spec index files only where needed | unspecified | unspecified | — |
| Swift | whole repository; also indexes remote repositories | local | asynchronous | — |
| Terraform | module or provider index; also indexes remote repositories | local | asynchronous | — |
| RPM | whole repository; for virtual repositories, a repository-metadata folder path | local, virtual, federated | synchronous by default | asynchronous flag; metadata path |
| Ansible, LuaRocks | whole repository | local | asynchronous (LuaRocks) | UI action; API operation unspecified |

Open, mode: for Cargo, Conda and CRAN the recalculation either runs synchronously by default with
an opt-in asynchronous mode, as the table lists, or is always asynchronous; the spec decides. For
pub the recalculation is asynchronous, yet the operation still takes an asynchronous flag.

No recalculation operation is specified for the other formats (among them Docker, OCI, Go,
Composer, Chef, Puppet, Vagrant, Hex, Hugging Face, Nix, Git LFS, generic).

</details>

When a recalculation is required, not optional:

- Debian properties for files deployed before properties were written (FMT-008).
- Switching Helm to relative chart URLs: the switch affects only charts deployed afterwards until a
  reindex aligns the older ones.
- Migrating a Cargo repository from the Git index to the sparse index: enable sparse indexing,
  then reindex.
- Creating the CDN index structure for an existing CocoaPods repository.
- Turning on sharded Conda metadata (FMT-014): local repositories must be reindexed once.
- Changing the RPM folder depth: packages indexed at the old depth may need to be reindexed or
  moved, and clients may need to point at the new depth.

Interactions: REPO-009 (UI row action), FMT-011, FMT-008, per-format entries (PKG-NPM-030 in
[npm.md](npm.md), PKG-NUGET-007 in [nuget.md](nuget.md)), operation catalog in
[../api-content-operations.md](../api-content-operations.md).

## Client authentication

### FMT-013 · Client authentication styles and anonymous fallbacks
Priority: **MVP**

Native clients present credentials in different ways, and a few cannot present them at all. The
system has to accept each client's style on that format's endpoint, and some formats need a
per-repository fallback to anonymous access. This entry is MVP for the npm and NuGet rows; every
other row carries its format's priority (FMT-001).

<details>
<summary>How each client presents credentials</summary>

| Format / client | How credentials reach the server | Server-side consequence |
|---|---|---|
| npm | A token in the client configuration file, per registry or per scope; `npm login` writes it. npm 9 and later ignore the legacy Basic-credential property (FMT-003). | Token authentication on every npm endpoint; the legacy Basic property is not a usable path for current clients. |
| NuGet | User name and password (or a token in the password field) stored per package source; pushes to a v2 feed use an API key built from user name and key. | With anonymous access on, NuGet clients are not challenged and so never send credentials; a per-repository force-authentication switch makes the server challenge first (REPO-028 in [../repositories.md](../repositories.md), PKG-NUGET-016 in [nuget.md](nuget.md)). |
| CRAN | Basic credentials inside the repository URL only (password or reference token). | Credentials in URLs (FMT-006, security note). |
| LuaRocks | No interactive prompt. Installs read credentials from the client configuration or `.netrc`; uploads accept them only inside the server URL; no bearer token, no user and password options. | Basic authentication must work for every LuaRocks endpoint. |
| Nix | No interactive prompt; credentials come from `.netrc`. | Basic authentication on the binary-cache endpoints. |
| CocoaPods | `pod` cannot authenticate against HTTP endpoints on its own; it fetches through curl, which reads `.netrc`. | Basic authentication; anonymous access is the documented default for these repositories (PKG-PODS-006 in [cocoapods.md](cocoapods.md)). |
| Cargo up to 1.67 without the sparse index | Sends no credentials for install and search. | A per-repository "allow anonymous download and search" switch (PKG-CARGO-005 in [cargo.md](cargo.md)). |
| JetBrains IDEs | Cannot authenticate at all. | Every request is anonymous and every operation read-only. |
| Agent skills | The discovery document is the only anonymous endpoint; listing, download, publish and resolve need credentials (in the URL, a token, or `.netrc`). | Mixed anonymous and authenticated endpoints in one format. |
| AI agent plugins through Claude Code | Authentication fails when the user name is an email address. | User names used by this client must not be email addresses. |

</details>

Security, per case:

- Cargo: the anonymous download-and-search switch exposes every crate in that repository to
  unauthenticated callers, regardless of the system-wide anonymous setting (PKG-CARGO-005). Live
  wherever the switch is on, typically to support clients up to 1.67.
- JetBrains IDE plugins: reads have no access control, so anyone who can reach the endpoint can
  pull the cached plugins. Live for every repository of this format.
- NuGet: with anonymous access on and force authentication off, users never authenticate, so they
  see only what the anonymous user may see and their downloads are not attributed to them. Live
  whenever the system allows anonymous access (IAM-005 in
  [../identity-and-access.md](../identity-and-access.md)).
- CRAN and LuaRocks uploads: credentials embedded in URLs (FMT-006).

Interactions: FMT-002, FMT-003, FMT-006, REPO-028, IAM-005, tokens in
[../access-tokens.md](../access-tokens.md).

## System-wide format settings

### FMT-014 · Package settings screen
Priority: **P1**

Besides per-repository settings, the system keeps instance-wide switches per format on one
administration screen (administration area, system settings, package settings), with one section
per format or format family and a save button. Changing it needs administrator or project
administrator rights. Sections and switches:

<details>
<summary>Sections of the package settings screen</summary>

| Section | Switch | Effect | Default |
|---|---|---|---|
| Docker, OCI, Helm on OCI | Synchronize download statistics for shared digests | A pull by any tag or digest updates the statistics of every readable tag in the same repository and image that shares the manifest digest; asynchronous, not retroactive ([docker.md](docker.md), PKG-DOCKER-030) | unspecified |
| Docker, OCI, Helm on OCI | Enforce strict tag overwrite | Pushing to an existing image name and tag needs delete/overwrite permission, so different manifest types cannot share a tag (PKG-DOCKER-027); the switch is announced to disappear, leaving the enforcement permanently on | on |
| Helm | Create Helm repositories as OCI first | Repository creation for "Helm" yields the OCI-based variant (FMT-001) | on |
| Helm | Prevent duplicate chart paths; enforce chart name and version | Enforced layout for all Helm repositories; cannot be switched off per repository afterwards (FMT-010) | unspecified |
| PyPI | Simple index in JSON | Serve the JSON form of the simple index ([pypi.md](pypi.md), PKG-PYPI-012) | unspecified |
| Conda | Sharded metadata | Serve the sharded channel index; local repositories must be reindexed afterwards (FMT-012, [conda.md](conda.md)) | unspecified |
| Hugging Face | Xet protocol | Large-file transfer through Xet instead of Git LFS (FMT-003, [huggingface.md](huggingface.md)) | unspecified |
| NuGet | Normalize package versions | Every NuGet repository created afterwards is normalized ([nuget.md](nuget.md), PKG-NUGET-010) | unspecified |
| RPM | Recommends tags and further metadata switches | See PKG-RPM-011 in [rpm.md](rpm.md) | see there |

</details>

Behavior and edge cases:

- When the same switch is also set in the system configuration file, the file wins: the checkbox is
  shown disabled with the file's value (stated for RPM). Whether this holds for every section is
  unspecified.
- Several switches affect only content or repositories created afterwards (NuGet normalization,
  Helm enforced layout, Docker statistics synchronization) or need a reindex (Conda sharding).
- Operations: no API for this screen is specified; whether the switches are part of the
  system configuration document is unspecified.

Interactions: FMT-001, FMT-010, FMT-012, the per-format pages linked above.

## Notes for implementers

- A format is a protocol, not a client. Test every listed client of a format against the same
  repository (npm, pnpm, Yarn Classic and Modern; NuGet CLI, .NET CLI, Visual Studio). Clients
  differ in authentication style (token in config file, `.netrc`, credentials in URL, none at
  all; FMT-013), and NuGet clients never send credentials unless the server challenges them.
- Metadata calculation is asynchronous and starts only after the action completes, so a publish
  followed at once by an install can see the old index. CI pipelines do exactly that; decide per
  format whether the publish response waits for the index.
- The package record is a snapshot from upload time. Manual file renames or moves desynchronize it
  from the file view, and moving one part of a multi-part package breaks the package.
- Format property namespaces are read by search, cleanup and the query language. Decide whether
  that namespace is writable by users. Debian, Vagrant and manual PyPI uploads need deployers to
  write some of it, so a blanket write-protection breaks those formats.
- The class-by-format support differs between the client-support table and the API's per-class
  sets (FMT-001, REPO-006). Pick one authoritative set and reject other combinations at creation.
- Federation follows "all formats except an exclusion list" in one place and an explicit set in
  another. The Terraform state backend is the only default exclusion.
- Client version floors are protocol cliffs, not cosmetic. Examples: npm 9 drops the legacy Basic
  property, Cargo needs 1.68 for a stable sparse index, Helm needs 3.8 for OCI, Swift needs 5.9 for
  registry publishing, and Hugging Face clients below 0.30 cannot read files uploaded through Xet.
- Recalculation operations answer plain text, not JSON. Several default to synchronous execution
  and can take hours on large repositories; expose the asynchronous mode and do not run the
  synchronous one inside a request timeout.
- Some configuration changes (Helm relative URLs, Cargo sparse index, RPM folder depth) affect
  only new content until a recalculation runs. Treat "reindex required" as part of the change.
- A file deployed outside its format's expected path structure is usually stored but not indexed.
  It shows in the file view and is invisible to clients and to the package view. Decide per format
  whether to reject such uploads instead, as AI agent packages and Helm's enforced layout do.
- The class matrix and the format pages disagree for Bazel modules (FMT-001). The format page,
  with local, remote and virtual repositories, is the one a registry client needs.
- Instance-wide format switches (FMT-014) and the system configuration file can both set the same
  value. Define one precedence rule for all sections; one is stated only for RPM.
