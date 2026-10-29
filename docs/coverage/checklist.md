# Coverage checklist

<!-- Generated file: do not edit by hand. -->

Generated from the headings of the [feature catalog](../features/README.md) (`### <ID> · <Title>`
and the `Priority:` line below it) and of the [specification](../spec/README.md)
(`### R-… · …` and the `Priority: … · Covers: …` line below it). Do not edit it by hand: change
the catalog or the spec, then regenerate this file whenever an ID, a title, a priority or a
`Covers:` line changes. The rules and the headline numbers are in
[README.md](README.md#how-the-numbers-are-produced).

**Status.** 98 of 577 spec requirements are implemented: a passing test names them and no
failing test does (the list is [implemented.txt](implemented.txt)). *implemented*: every requirement
that lists the feature in its `Covers:` line is implemented. *specified*: at least one spec
requirement lists the feature in its `Covers:` line and not all of them are implemented. *open*: no
requirement does. For entries with priority *Out of scope*, open is the intended end state.

**Totals.** 2,239 catalog features on 60 pages: 665 specified (58 of them implemented), 1,574 open.
577 spec requirements: 483 cover catalog features, 94 cover none (`trove-only`, [listed at the
end](#requirements-that-cover-no-catalog-feature)).

## Pages

<details>
<summary>60 catalog pages</summary>

| Catalog page | Prefix | Features | MVP specified | Specified | Open | Implemented |
|---|---|---:|---:|---:|---:|---:|
| [Repositories](#repositories-repo) | `REPO` | 68 | 23 of 23 | 40 | 28 | 0 |
| [Remote repositories](#remote-repositories-rmt) | `RMT` | 49 | 15 of 15 | 28 | 21 | 0 |
| [Replication, federation and multi-site topology](#replication-federation-and-multi-site-topology-repl) | `REPL` | 72 | – | 0 | 72 | 0 |
| [Artifact management](#artifact-management-art) | `ART` | 53 | 10 of 10 | 32 | 21 | 1 |
| [Search and query language](#search-and-query-language-srch) | `SRCH` | 64 | – | 3 | 61 | 0 |
| [Builds and CI integration](#builds-and-ci-integration-bld) | `BLD` | 49 | – | 0 | 49 | 0 |
| [Identity and access](#identity-and-access-iam) | `IAM` | 69 | 20 of 20 | 34 | 35 | 0 |
| [Authentication providers and human login](#authentication-providers-and-human-login-authn) | `AUTHN` | 44 | 13 of 13 | 18 | 26 | 0 |
| [Access tokens](#access-tokens-tok) | `TOK` | 48 | 24 of 24 | 27 | 21 | 0 |
| [OIDC federation for CI systems](#oidc-federation-for-ci-systems-cioidc) | `CIOIDC` | 40 | 20 of 20 | 31 | 9 | 0 |
| [Storage and blob providers](#storage-and-blob-providers-stor) | `STOR` | 41 | 12 of 12 | 21 | 20 | 11 |
| [Retention, cleanup and archiving](#retention-cleanup-and-archiving-ret) | `RET` | 42 | – | 26 | 16 | 0 |
| [High availability and deployment](#high-availability-and-deployment-ha) | `HA` | 79 | 49 of 49 | 61 | 18 | 26 |
| [Operations, monitoring and configuration](#operations-monitoring-and-configuration-ops) | `OPS` | 100 | 17 of 17 | 44 | 56 | 15 |
| [Events and extensibility](#events-and-extensibility-evt) | `EVT` | 82 | 1 of 1 | 26 | 56 | 1 |
| [Distribution and release lifecycle](#distribution-and-release-lifecycle-dist) | `DIST` | 70 | – | 0 | 70 | 0 |
| [Security scanning and curation](#security-scanning-and-curation-sec) | `SEC` | 98 | – | 0 | 98 | 0 |
| [UI inventory](#ui-inventory-ui) | `UI` | 61 | 30 of 30 | 47 | 14 | 1 |
| [Content API operation catalog](#content-api-operation-catalog-api) | `API` | 166 | 11 of 11 | 67 | 99 | 1 |
| [Operation catalog: administration API](#operation-catalog-administration-api-admapi) | `ADMAPI` | 114 | 34 of 34 | 54 | 60 | 2 |
| [Package formats: matrix and cross-format behavior](#package-formats-matrix-and-cross-format-behavior-fmt) | `FMT` | 14 | 8 of 8 | 9 | 5 | 0 |
| [npm package format](#npm-package-format-pkg-npm) | `PKG-NPM` | 62 | 37 of 37 | 52 | 10 | 0 |
| [NuGet](#nuget-pkg-nuget) | `PKG-NUGET` | 56 | 35 of 35 | 42 | 14 | 0 |
| [Docker image format](#docker-image-format-pkg-docker) | `PKG-DOCKER` | 52 | – | 1 | 51 | 0 |
| [OCI images and artifacts](#oci-images-and-artifacts-pkg-oci) | `PKG-OCI` | 20 | – | 0 | 20 | 0 |
| [Helm chart format](#helm-chart-format-pkg-helm) | `PKG-HELM` | 25 | – | 0 | 25 | 0 |
| [Maven package format](#maven-package-format-pkg-maven) | `PKG-MAVEN` | 39 | – | 0 | 39 | 0 |
| [PyPI package format](#pypi-package-format-pkg-pypi) | `PKG-PYPI` | 29 | – | 0 | 29 | 0 |
| [Go modules format](#go-modules-format-pkg-go) | `PKG-GO` | 16 | – | 0 | 16 | 0 |
| [Generic format](#generic-format-pkg-generic) | `PKG-GENERIC` | 11 | – | 0 | 11 | 0 |
| [Ansible collection format](#ansible-collection-format-pkg-ansible) | `PKG-ANSIBLE` | 13 | – | 0 | 13 | 0 |
| [Alpine Linux package format](#alpine-linux-package-format-pkg-apk) | `PKG-APK` | 14 | – | 0 | 14 | 0 |
| [Bazel modules package format](#bazel-modules-package-format-pkg-bazel) | `PKG-BAZEL` | 12 | – | 0 | 12 | 0 |
| [Bower package format](#bower-package-format-pkg-bower) | `PKG-BOWER` | 14 | – | 0 | 14 | 0 |
| [Cargo (Rust crates) format](#cargo-rust-crates-format-pkg-cargo) | `PKG-CARGO` | 14 | – | 0 | 14 | 0 |
| [Chef cookbook format](#chef-cookbook-format-pkg-chef) | `PKG-CHEF` | 10 | – | 0 | 10 | 0 |
| [CocoaPods package format](#cocoapods-package-format-pkg-pods) | `PKG-PODS` | 15 | – | 0 | 15 | 0 |
| [PHP Composer format](#php-composer-format-pkg-composer) | `PKG-COMPOSER` | 11 | – | 0 | 11 | 0 |
| [Conan (C/C++) format](#conan-cc-format-pkg-conan) | `PKG-CONAN` | 14 | – | 0 | 14 | 0 |
| [Conda package format](#conda-package-format-pkg-conda) | `PKG-CONDA` | 15 | – | 0 | 15 | 0 |
| [CRAN package format](#cran-package-format-pkg-cran) | `PKG-CRAN` | 16 | – | 0 | 16 | 0 |
| [Debian package format](#debian-package-format-pkg-deb) | `PKG-DEB` | 28 | – | 0 | 28 | 0 |
| [Git LFS format](#git-lfs-format-pkg-lfs) | `PKG-LFS` | 9 | – | 0 | 9 | 0 |
| [Gradle, Ivy, sbt and Eclipse P2 formats](#gradle-ivy-sbt-and-eclipse-p2-formats-pkg-jvm) | `PKG-JVM` | 24 | – | 0 | 24 | 0 |
| [Hex package format (Elixir and Erlang)](#hex-package-format-elixir-and-erlang-pkg-hex) | `PKG-HEX` | 18 | – | 0 | 18 | 0 |
| [Hugging Face models and datasets](#hugging-face-models-and-datasets-pkg-hf) | `PKG-HF` | 18 | – | 0 | 18 | 0 |
| [LuaRocks package format](#luarocks-package-format-pkg-lua) | `PKG-LUA` | 18 | – | 0 | 18 | 0 |
| [Nix package format](#nix-package-format-pkg-nix) | `PKG-NIX` | 20 | – | 0 | 20 | 0 |
| [Opkg package format](#opkg-package-format-pkg-opkg) | `PKG-OPKG` | 8 | – | 0 | 8 | 0 |
| [pub package format (Dart and Flutter)](#pub-package-format-dart-and-flutter-pkg-pub) | `PKG-PUB` | 19 | – | 0 | 19 | 0 |
| [Puppet module format](#puppet-module-format-pkg-puppet) | `PKG-PUPPET` | 12 | – | 0 | 12 | 0 |
| [RPM package format](#rpm-package-format-pkg-rpm) | `PKG-RPM` | 18 | – | 0 | 18 | 0 |
| [RubyGems format](#rubygems-format-pkg-gems) | `PKG-GEMS` | 18 | – | 0 | 18 | 0 |
| [Swift package format](#swift-package-format-pkg-swift) | `PKG-SWIFT` | 18 | – | 0 | 18 | 0 |
| [Terraform and OpenTofu formats](#terraform-and-opentofu-formats-pkg-tf) | `PKG-TF` | 23 | – | 0 | 23 | 0 |
| [Vagrant box format](#vagrant-box-format-pkg-vagrant) | `PKG-VAGRANT` | 4 | – | 0 | 4 | 0 |
| [VCS archive format](#vcs-archive-format-pkg-vcs) | `PKG-VCS` | 13 | – | 0 | 13 | 0 |
| [Machine-learning models and model-serving containers](#machine-learning-models-and-model-serving-containers-pkg-ml) | `PKG-ML` | 19 | – | 0 | 19 | 0 |
| [AI agent assets and IDE extension formats](#ai-agent-assets-and-ide-extension-formats-pkg-ai) | `PKG-AI` | 37 | – | 0 | 37 | 0 |
| [Beyond the baseline: further formats and capabilities](#beyond-the-baseline-further-formats-and-capabilities-beyond) | `BEYOND` | 34 | – | 2 | 32 | 0 |

</details>

## Repositories (REPO)

Catalog page: [repositories.md](../features/repositories.md) · specified by priority: MVP 23 of 23 · P1 17 of 26 · P2 0 of 16 · Later 0 of 2 · Out of scope 0 of 1

<details>
<summary>68 features: 40 specified, 28 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| REPO-001 | Repository classes | MVP | specified | [R-REPO-001](../spec/repositories-and-proxy.md) |
| REPO-002 | One package format per repository | MVP | specified | [R-ARCH-008](../spec/architecture.md) (implemented), [R-REPO-001](../spec/repositories-and-proxy.md) |
| REPO-003 | Generic repositories | P1 | open | – |
| REPO-004 | Repository key naming rules | MVP | specified | [R-REPO-002](../spec/repositories-and-proxy.md) |
| REPO-005 | Repository addressing | MVP | specified | [R-ARCH-005](../spec/architecture.md) (implemented), [R-NPM-001](../spec/formats/npm.md), [R-NUGET-001](../spec/formats/nuget.md), [R-REPO-002](../spec/repositories-and-proxy.md) |
| REPO-006 | Format support per repository class | MVP | specified | [R-ARCH-008](../spec/architecture.md) (implemented), [R-REPO-003](../spec/repositories-and-proxy.md) |
| REPO-007 | Repository administration list | MVP | specified | [R-UI-020](../spec/api-and-ui.md), [R-REPO-012](../spec/repositories-and-proxy.md) |
| REPO-008 | Quick repository setup wizard | P1 | specified | [R-UI-023](../spec/api-and-ui.md), [R-REPO-015](../spec/repositories-and-proxy.md) |
| REPO-009 | Recalculate repository index | P1 | specified | [R-NPM-035](../spec/formats/npm.md), [R-REPO-016](../spec/repositories-and-proxy.md) |
| REPO-010 | Public description and internal notes | MVP | specified | [R-REPO-004](../spec/repositories-and-proxy.md) |
| REPO-011 | Include and exclude patterns | MVP | specified | [R-NPM-033](../spec/formats/npm.md), [R-REPO-005](../spec/repositories-and-proxy.md) |
| REPO-012 | Repository layout reference | P1 | specified | [R-REPO-006](../spec/repositories-and-proxy.md) |
| REPO-013 | Project assignment and environments | P1 | open | – |
| REPO-014 | Sharing a repository with other projects | P2 | open | – |
| REPO-015 | Project-scoped connection to a shared repository | P2 | open | – |
| REPO-016 | Local repository | MVP | specified | [R-ARCH-011](../spec/architecture.md), [R-REPO-001](../spec/repositories-and-proxy.md), [R-REPO-004](../spec/repositories-and-proxy.md) |
| REPO-017 | Disable resolution | P1 | specified | [R-REPO-004](../spec/repositories-and-proxy.md) |
| REPO-018 | Priority resolution | P1 | specified | [R-NPM-028](../spec/formats/npm.md), [R-REPO-022](../spec/repositories-and-proxy.md) |
| REPO-019 | Download redirect | P2 | open | – |
| REPO-020 | CDN download redirect | Out of scope | open | – |
| REPO-021 | Scanner indexing | Later | open | – |
| REPO-022 | Deployment checksum policy | P1 | open | – |
| REPO-023 | Release and snapshot handling | P1 | open | – |
| REPO-024 | Snapshot naming behavior | P1 | open | – |
| REPO-025 | Maximum unique snapshots | P1 | specified | [R-REPO-007](../spec/repositories-and-proxy.md) |
| REPO-026 | Descriptor consistency check | P1 | open | – |
| REPO-027 | Container tag limits and retention | P1 | specified | [R-REPO-007](../spec/repositories-and-proxy.md) |
| REPO-028 | Force authentication per repository | MVP | specified | [R-NUGET-012](../spec/formats/nuget.md), [R-REPO-004](../spec/repositories-and-proxy.md) |
| REPO-029 | npm local repository settings | MVP | specified | [R-NPM-006](../spec/formats/npm.md) |
| REPO-030 | NuGet local and virtual repository settings | MVP | specified | [R-NUGET-003](../spec/formats/nuget.md) |
| REPO-031 | NuGet version normalization rules | MVP | specified | [R-NUGET-003](../spec/formats/nuget.md) |
| REPO-032 | Other format-specific local repository settings | P2 | open | – |
| REPO-033 | Virtual repository | MVP | specified | [R-ARCH-015](../spec/architecture.md), [R-NPM-030](../spec/formats/npm.md), [R-REPO-017](../spec/repositories-and-proxy.md) |
| REPO-034 | Nested virtual repositories | P1 | specified | [R-REPO-018](../spec/repositories-and-proxy.md) |
| REPO-035 | Virtual resolution order | MVP | specified | [R-ARCH-015](../spec/architecture.md), [R-NPM-030](../spec/formats/npm.md), [R-NUGET-017](../spec/formats/nuget.md), [R-REPO-019](../spec/repositories-and-proxy.md) |
| REPO-036 | Latest-version resolution across members | MVP | specified | [R-ARCH-015](../spec/architecture.md), [R-NPM-030](../spec/formats/npm.md), [R-NUGET-017](../spec/formats/nuget.md), [R-REPO-019](../spec/repositories-and-proxy.md) |
| REPO-037 | Merged metadata and virtual metadata cache period | MVP | specified | [R-ARCH-015](../spec/architecture.md), [R-NPM-030](../spec/formats/npm.md), [R-NPM-032](../spec/formats/npm.md), [R-NUGET-017](../spec/formats/nuget.md), [R-REPO-020](../spec/repositories-and-proxy.md) |
| REPO-038 | Default deployment repository | MVP | specified | [R-ARCH-014](../spec/architecture.md), [R-ARCH-015](../spec/architecture.md), [R-NPM-029](../spec/formats/npm.md), [R-NUGET-019](../spec/formats/nuget.md), [R-REPO-021](../spec/repositories-and-proxy.md) |
| REPO-039 | Remote retrieval for requests from other instances | P2 | open | – |
| REPO-040 | External dependency rewrite | P1 | specified | [R-NPM-034](../spec/formats/npm.md) |
| REPO-041 | Descriptor repository-reference cleanup | P1 | open | – |
| REPO-042 | Signing keys on virtual repositories | P2 | open | – |
| REPO-043 | Container tag resolution by latest timestamp | P1 | open | – |
| REPO-044 | Other format-specific virtual repository settings | P2 | open | – |
| REPO-045 | Virtual repositories in projects | P1 | open | – |
| REPO-046 | Module identification from paths | P1 | specified | [R-REPO-006](../spec/repositories-and-proxy.md) |
| REPO-047 | Bundled layouts | P1 | specified | [R-REPO-006](../spec/repositories-and-proxy.md) |
| REPO-048 | Path pattern syntax | P2 | open | – |
| REPO-049 | Integration revision expressions | P2 | open | – |
| REPO-050 | Layout management | P2 | open | – |
| REPO-051 | Layouts per repository class and path translation | P2 | open | – |
| REPO-052 | Layout-based maintenance functions | P2 | open | – |
| REPO-053 | Create repository | MVP | specified | [R-REPO-008](../spec/repositories-and-proxy.md), [R-REPO-009](../spec/repositories-and-proxy.md) |
| REPO-054 | Update repository configuration | MVP | specified | [R-REPO-008](../spec/repositories-and-proxy.md), [R-REPO-011](../spec/repositories-and-proxy.md) |
| REPO-055 | Get repository configuration | MVP | specified | [R-REPO-010](../spec/repositories-and-proxy.md) |
| REPO-056 | Get format-filtered repository configuration | P1 | specified | [R-REPO-010](../spec/repositories-and-proxy.md) |
| REPO-057 | List repositories | MVP | specified | [R-REPO-012](../spec/repositories-and-proxy.md) |
| REPO-058 | Get all repository configurations | P1 | specified | [R-REPO-012](../spec/repositories-and-proxy.md) |
| REPO-059 | Get repository configurations by key | P1 | specified | [R-REPO-014](../spec/repositories-and-proxy.md) |
| REPO-060 | Check repository existence | P2 | open | – |
| REPO-061 | Create repositories in a batch | P1 | specified | [R-REPO-014](../spec/repositories-and-proxy.md) |
| REPO-062 | Update repositories in a batch | P1 | specified | [R-REPO-014](../spec/repositories-and-proxy.md) |
| REPO-063 | Delete repository | MVP | specified | [R-REPO-013](../spec/repositories-and-proxy.md) |
| REPO-064 | Delete repositories in a batch | P1 | specified | [R-REPO-014](../spec/repositories-and-proxy.md) |
| REPO-065 | Shared remote configuration (deprecated) | P2 | open | – |
| REPO-066 | Release-set storage repository | Later | open | – |
| REPO-067 | Format normalization migration | P2 | open | – |
| REPO-068 | System-wide repository search order | P2 | open | – |

</details>

## Remote repositories (RMT)

Catalog page: [remote-repositories.md](../features/remote-repositories.md) · specified by priority: MVP 15 of 15 · P1 13 of 14 · P2 0 of 20

<details>
<summary>49 features: 28 specified, 21 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| RMT-001 | Remote repository (pull-through proxy and cache) | MVP | specified | [R-ARCH-013](../spec/architecture.md), [R-NPM-024](../spec/formats/npm.md), [R-REPO-024](../spec/repositories-and-proxy.md), [R-REPO-025](../spec/repositories-and-proxy.md) |
| RMT-002 | Remote repository configuration operations | MVP | specified | [R-REPO-008](../spec/repositories-and-proxy.md), [R-REPO-009](../spec/repositories-and-proxy.md), [R-REPO-011](../spec/repositories-and-proxy.md), [R-REPO-013](../spec/repositories-and-proxy.md), [R-REPO-024](../spec/repositories-and-proxy.md) |
| RMT-003 | Cache view and cached-content management | MVP | specified | [R-REPO-032](../spec/repositories-and-proxy.md) |
| RMT-004 | Upstream URL | MVP | specified | [R-NUGET-014](../spec/formats/nuget.md), [R-REPO-024](../spec/repositories-and-proxy.md) |
| RMT-005 | Upstream credentials | MVP | specified | [R-REPO-033](../spec/repositories-and-proxy.md) |
| RMT-006 | Bearer token authentication to the upstream | P1 | specified | [R-NPM-023](../spec/formats/npm.md), [R-REPO-033](../spec/repositories-and-proxy.md) |
| RMT-007 | Client TLS certificate for the upstream | P1 | specified | [R-REPO-034](../spec/repositories-and-proxy.md) |
| RMT-008 | Trust for upstream server certificates | P1 | specified | [R-REPO-035](../spec/repositories-and-proxy.md) |
| RMT-009 | Custom HTTP headers to the upstream | P1 | specified | [R-REPO-036](../spec/repositories-and-proxy.md) |
| RMT-010 | Static query parameters | P2 | open | – |
| RMT-011 | Client query parameter propagation | P2 | open | – |
| RMT-012 | Credential forwarding on cross-host redirects | P1 | specified | [R-REPO-039](../spec/repositories-and-proxy.md) |
| RMT-013 | Cookie management | P2 | open | – |
| RMT-014 | URL normalization control | P2 | open | – |
| RMT-015 | Network timeouts and source interface | MVP | specified | [R-ARCH-022](../spec/architecture.md) (implemented), [R-REPO-024](../spec/repositories-and-proxy.md) |
| RMT-016 | Outbound proxy selection per repository | P1 | specified | [R-REPO-037](../spec/repositories-and-proxy.md) |
| RMT-017 | Store artifacts locally or pass through | P1 | specified | [R-REPO-044](../spec/repositories-and-proxy.md) |
| RMT-018 | Metadata retrieval cache period | MVP | specified | [R-ARCH-013](../spec/architecture.md), [R-NPM-024](../spec/formats/npm.md), [R-NUGET-015](../spec/formats/nuget.md), [R-REPO-027](../spec/repositories-and-proxy.md) |
| RMT-019 | Metadata retrieval timeout with stale fallback | MVP | specified | [R-ARCH-013](../spec/architecture.md), [R-NPM-024](../spec/formats/npm.md), [R-REPO-027](../spec/repositories-and-proxy.md) |
| RMT-020 | Negative cache period | MVP | specified | [R-ARCH-013](../spec/architecture.md), [R-NPM-024](../spec/formats/npm.md), [R-NUGET-015](../spec/formats/nuget.md), [R-REPO-028](../spec/repositories-and-proxy.md) |
| RMT-021 | Unused cached artifact cleanup | P1 | specified | [R-REPO-046](../spec/repositories-and-proxy.md) |
| RMT-022 | Remote checksum policy | P1 | specified | [R-NPM-025](../spec/formats/npm.md), [R-NUGET-029](../spec/formats/nuget.md), [R-REPO-041](../spec/repositories-and-proxy.md) |
| RMT-023 | MIME-type mismatch blocking | MVP | specified | [R-REPO-042](../spec/repositories-and-proxy.md) |
| RMT-024 | HEAD request bypass | P2 | open | – |
| RMT-025 | Eager fetching of companion artifacts | P2 | open | – |
| RMT-026 | Cache invalidation | MVP | specified | [R-ARCH-018](../spec/architecture.md) (implemented), [R-NPM-024](../spec/formats/npm.md), [R-REPO-045](../spec/repositories-and-proxy.md) |
| RMT-027 | Cache pre-population without client transfer | P2 | open | – |
| RMT-028 | Download redirect for cached artifacts | P2 | open | – |
| RMT-029 | Assumed-offline period | MVP | specified | [R-ARCH-013](../spec/architecture.md), [R-REPO-029](../spec/repositories-and-proxy.md) |
| RMT-030 | Repository offline mode | MVP | specified | [R-REPO-004](../spec/repositories-and-proxy.md), [R-REPO-030](../spec/repositories-and-proxy.md) |
| RMT-031 | Global offline mode | P1 | specified | [R-REPO-031](../spec/repositories-and-proxy.md) |
| RMT-032 | Hard fail | P2 | open | – |
| RMT-033 | Behavior when the upstream fails | MVP | specified | [R-ARCH-013](../spec/architecture.md), [R-NUGET-015](../spec/formats/nuget.md), [R-REPO-025](../spec/repositories-and-proxy.md), [R-REPO-029](../spec/repositories-and-proxy.md) |
| RMT-034 | Include and exclude patterns | MVP | specified | [R-NPM-033](../spec/formats/npm.md), [R-NUGET-015](../spec/formats/nuget.md), [R-REPO-005](../spec/repositories-and-proxy.md) |
| RMT-035 | Cache layout and remote layout mapping | P2 | open | – |
| RMT-036 | Release and snapshot handling | P1 | open | – |
| RMT-037 | Priority resolution | P1 | specified | [R-NPM-028](../spec/formats/npm.md), [R-REPO-022](../spec/repositories-and-proxy.md) |
| RMT-038 | Remote folder listing | P2 | open | – |
| RMT-039 | Configuration sharing | P2 | open | – |
| RMT-040 | Chained remote repository | P2 | open | – |
| RMT-041 | Download statistics reporting | P2 | open | – |
| RMT-042 | Property synchronization | P2 | open | – |
| RMT-043 | Source absence detection | P2 | open | – |
| RMT-044 | Uncached retrieval through a virtual repository for chained requests | P2 | open | – |
| RMT-045 | Search through a chained remote | P2 | open | – |
| RMT-046 | Parallel chunked download of large files | P2 | open | – |
| RMT-047 | Project-scoped remote repository | P2 | open | – |
| RMT-048 | Outbound HTTP proxy definitions | P1 | specified | [R-REPO-037](../spec/repositories-and-proxy.md) |
| RMT-049 | Default proxy | P1 | specified | [R-REPO-037](../spec/repositories-and-proxy.md) |

</details>

## Replication, federation and multi-site topology (REPL)

Catalog page: [replication-and-federation.md](../features/replication-and-federation.md) · specified by priority: P2 0 of 17 · Later 0 of 51 · Out of scope 0 of 4

<details>
<summary>72 features: 0 specified, 72 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| REPL-001 | Repository replication between instances | P2 | open | – |
| REPL-002 | Scheduled replication | P2 | open | – |
| REPL-003 | Event-based push replication | P2 | open | – |
| REPL-004 | Multi-target push replication | P2 | open | – |
| REPL-005 | Push replication target settings | P2 | open | – |
| REPL-006 | Pull replication | P2 | open | – |
| REPL-007 | Event channel for event-based pull replication | P2 | open | – |
| REPL-008 | Delete synchronization | P2 | open | – |
| REPL-009 | Property and download-statistics synchronization | P2 | open | – |
| REPL-010 | Path filters | P2 | open | – |
| REPL-011 | Replication configuration operations | P2 | open | – |
| REPL-012 | Run replication now | P2 | open | – |
| REPL-013 | Replication status | P2 | open | – |
| REPL-014 | Enable or disable replications in bulk | P2 | open | – |
| REPL-015 | Global replication block | P2 | open | – |
| REPL-016 | Metadata-only replication over storage-level copy | P2 | open | – |
| REPL-017 | Replication monitoring across sites | P2 | open | – |
| REPL-018 | Federated repository | Later | open | – |
| REPL-019 | Members and mesh topology | Later | open | – |
| REPL-020 | Mirror modes: bidirectional, receive-only, send-only | Later | open | – |
| REPL-021 | Receive-only trust enforced at site level | Later | open | – |
| REPL-022 | Pause and resume a member | Later | open | – |
| REPL-023 | Federation priority | Later | open | – |
| REPL-024 | Repository class conversions | Later | open | – |
| REPL-025 | Delete a federated repository | Later | open | – |
| REPL-026 | Format support and exclusions | Later | open | – |
| REPL-027 | Members on different versions | Later | open | – |
| REPL-028 | Event-driven synchronization | Later | open | – |
| REPL-029 | Event queue processing, deduplication and retention | Later | open | – |
| REPL-030 | Binary transfer tasks | Later | open | – |
| REPL-031 | Failed binary task management | Later | open | – |
| REPL-032 | Conflict resolution | Later | open | – |
| REPL-033 | Synchronized metadata and site-local settings | Later | open | – |
| REPL-034 | Configuration synchronization and drift detection | Later | open | – |
| REPL-035 | Full synchronization | Later | open | – |
| REPL-036 | Full synchronization from a pre-generated file list | Later | open | – |
| REPL-037 | Auto-healing and manual recovery | Later | open | – |
| REPL-038 | Bulk mirroring and ordered parallel apply | Later | open | – |
| REPL-039 | Member states and health | Later | open | – |
| REPL-040 | Federation status dashboard | Later | open | – |
| REPL-041 | Federation monitoring operations | Later | open | – |
| REPL-042 | Detailed per-repository and per-site status | Later | open | – |
| REPL-043 | Federation metrics | Later | open | – |
| REPL-044 | Permissions and service-to-service authorization | Later | open | – |
| REPL-045 | Synchronization service | Later | open | – |
| REPL-046 | Network paths and outbound proxy | Later | open | – |
| REPL-047 | Base URL and dedicated federation URL | Later | open | – |
| REPL-048 | Geo-synchronized topology behind a geographic load balancer | Later | open | – |
| REPL-049 | Interaction of federation with other features | Later | open | – |
| REPL-050 | Converting push replication into receive-only federation | Later | open | – |
| REPL-051 | Site registry | Later | open | – |
| REPL-052 | Site topology and health map | Later | open | – |
| REPL-053 | Identity federation | Later | open | – |
| REPL-054 | Identity federation topologies | Later | open | – |
| REPL-055 | Identity federation synchronization settings | Later | open | – |
| REPL-056 | Identity federation conflict handling | Later | open | – |
| REPL-057 | Full broadcast and stale-target recovery | Later | open | – |
| REPL-058 | Central identity federation configuration | Later | open | – |
| REPL-071 | Identity federation target operations on a single site | Later | open | – |
| REPL-059 | Workspace federation of projects and access settings | Later | open | – |
| REPL-060 | Workspace federation monitoring | Later | open | – |
| REPL-061 | Upstream-curation policy federation | Later | open | – |
| REPL-062 | Outbound tunnel for sites behind a firewall | Later | open | – |
| REPL-063 | Tunnel creation and pairing | Later | open | – |
| REPL-064 | Tunnel lifecycle controls | Later | open | – |
| REPL-065 | Tunnel scaling and advanced parameters | Later | open | – |
| REPL-066 | Tunnel diagnostics | Later | open | – |
| REPL-067 | Repository federation over the tunnel | Later | open | – |
| REPL-068 | License gating of multi-site features | Out of scope | open | – |
| REPL-069 | Hosted tenant relocation with federation state | Out of scope | open | – |
| REPL-070 | Usage telemetry from the synchronization service | Out of scope | open | – |
| REPL-072 | Migration from the embedded synchronization mode to the separate service | Out of scope | open | – |

</details>

## Artifact management (ART)

Catalog page: [artifacts.md](../features/artifacts.md) · specified by priority: MVP 10 of 10 · P1 20 of 20 · P2 2 of 22 · Out of scope 0 of 1

<details>
<summary>53 features: 32 specified, 21 open, 1 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| ART-001 | Items, artifacts and packages | MVP | specified | [R-STOR-003](../spec/storage.md) (implemented), [R-STOR-016](../spec/storage.md) |
| ART-002 | Deploy a single artifact or create a folder | MVP | specified | [R-ARCH-014](../spec/architecture.md), [R-STOR-007](../spec/storage.md) |
| ART-003 | Properties attached at deploy time (matrix parameters) | P1 | specified | [R-STOR-009](../spec/storage.md) |
| ART-004 | Deploy by checksum | P1 | specified | [R-STOR-008](../spec/storage.md) |
| ART-005 | Deploy an archive and extract it on arrival | P2 | open | – |
| ART-006 | Deploy dialog in the UI | P1 | specified | [R-NPM-036](../spec/formats/npm.md) |
| ART-007 | Multi-part upload of large files through object storage | P2 | open | – |
| ART-008 | Checksum computation and exposure | MVP | specified | [R-ARCH-014](../spec/architecture.md), [R-STOR-002](../spec/storage.md) (implemented) |
| ART-009 | Client checksum verification policy | P1 | specified | [R-STOR-010](../spec/storage.md) |
| ART-010 | SHA-256 backfill operation | P2 | open | – |
| ART-011 | Retrieve an artifact | MVP | specified | [R-ARCH-011](../spec/architecture.md), [R-STOR-012](../spec/storage.md) |
| ART-012 | Property-constrained resolution (matrix parameters on download) | P2 | open | – |
| ART-013 | Latest-version tokens in download paths | P2 | open | – |
| ART-014 | Download an entry from inside an archive | P2 | open | – |
| ART-015 | Download a folder or a whole repository as an archive | P2 | open | – |
| ART-016 | Download the artifacts of a build as an archive | P2 | open | – |
| ART-017 | Server-side download without transferring content | P2 | open | – |
| ART-018 | Time-limited signed download URLs | P2 | open | – |
| ART-019 | Rotate the signed-URL key | P2 | open | – |
| ART-020 | Filtered (templated) resources | P2 | open | – |
| ART-021 | Item property model | P1 | specified | [R-STOR-020](../spec/storage.md) |
| ART-022 | Read item properties | P1 | specified | [R-STOR-020](../spec/storage.md) |
| ART-023 | Set item properties | P1 | specified | [R-STOR-020](../spec/storage.md) |
| ART-024 | Update item properties (partial update) | P1 | specified | [R-STOR-020](../spec/storage.md) |
| ART-025 | Delete item properties | P1 | specified | [R-STOR-020](../spec/storage.md) |
| ART-026 | Property sets | P1 | specified | [R-STOR-021](../spec/storage.md) |
| ART-027 | Item information | MVP | specified | [R-API-025](../spec/api-and-ui.md), [R-STOR-016](../spec/storage.md) |
| ART-028 | File list | P1 | specified | [R-API-025](../spec/api-and-ui.md), [R-STOR-017](../spec/storage.md) |
| ART-029 | Download statistics | P1 | specified | [R-STOR-015](../spec/storage.md) |
| ART-030 | Last-modified lookup | P2 | open | – |
| ART-031 | Effective permissions of an item | P1 | specified | [R-API-034](../spec/api-and-ui.md), [R-AUTHZ-024](../spec/authorization.md) |
| ART-032 | Copy items | P1 | specified | [R-STOR-019](../spec/storage.md) |
| ART-033 | Move items | P1 | specified | [R-STOR-019](../spec/storage.md) |
| ART-034 | Delete items | MVP | specified | [R-STOR-018](../spec/storage.md) |
| ART-035 | Delete versions (bulk UI) | P2 | open | – |
| ART-036 | Trash can with retention period | P1 | specified | [R-STOR-022](../spec/storage.md) |
| ART-037 | Restore from the trash can | P1 | specified | [R-STOR-023](../spec/storage.md) |
| ART-038 | Permanently delete from the trash can | P1 | specified | [R-STOR-023](../spec/storage.md) |
| ART-039 | Purge redundant trash-can folder records | P2 | open | – |
| ART-040 | File tree browser | MVP | specified | [R-UI-010](../spec/api-and-ui.md) |
| ART-041 | Item details pane | MVP | specified | [R-UI-010](../spec/api-and-ui.md) |
| ART-042 | HTML directory-listing browser | P1 | specified | [R-STOR-017](../spec/storage.md) |
| ART-043 | Public repository view under a path prefix | P2 | implemented | [R-EP-005](../spec/endpoints.md) (implemented) |
| ART-044 | Browsing uncached remote content | P2 | open | – |
| ART-045 | Content browsing of files and archives | P2 | open | – |
| ART-046 | Followers (change notifications) | P2 | open | – |
| ART-047 | WebDAV access | P2 | open | – |
| ART-048 | Package list | MVP | specified | [R-API-029](../spec/api-and-ui.md), [R-UI-006](../spec/api-and-ui.md) |
| ART-049 | Package details and install commands | MVP | specified | [R-API-029](../spec/api-and-ui.md), [R-UI-007](../spec/api-and-ui.md) |
| ART-050 | Invalidate cached metadata in a remote repository | P1 | specified | [R-REPO-045](../spec/repositories-and-proxy.md) |
| ART-051 | List background tasks | P2 | specified | [R-OPS-019](../spec/operations-and-observability.md) |
| ART-052 | RubyGems version list | P2 | open | – |
| ART-053 | Push to a discontinued external distribution service | Out of scope | open | – |

</details>

## Search and query language (SRCH)

Catalog page: [search-and-query.md](../features/search-and-query.md) · specified by priority: P1 3 of 24 · P2 0 of 35 · Later 0 of 5

<details>
<summary>64 features: 3 specified, 61 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| SRCH-001 | Two search sources: stored items and extracted package metadata | P1 | open | – |
| SRCH-002 | Common conventions of the search operations | P1 | open | – |
| SRCH-003 | Result cap on search operations | P1 | open | – |
| SRCH-004 | Name search | P1 | open | – |
| SRCH-005 | Archive entry search | Later | open | – |
| SRCH-006 | Maven coordinate search | P1 | open | – |
| SRCH-007 | Property search | P1 | open | – |
| SRCH-008 | Checksum search | P1 | open | – |
| SRCH-009 | Bad or missing client checksum search | P2 | open | – |
| SRCH-010 | Not-downloaded-since search | P1 | open | – |
| SRCH-011 | Date-range search on selectable date fields | P1 | open | – |
| SRCH-012 | Created-in-date-range search | P1 | open | – |
| SRCH-013 | Path pattern search | P1 | open | – |
| SRCH-014 | Builds for a dependency | P2 | open | – |
| SRCH-015 | License search | Later | open | – |
| SRCH-016 | Version list search | P2 | open | – |
| SRCH-017 | Latest version by layout | P2 | open | – |
| SRCH-018 | Latest version by properties | P2 | open | – |
| SRCH-019 | Build artifacts search | P1 | open | – |
| SRCH-020 | Container image catalog listing | P1 | open | – |
| SRCH-021 | Container tag listing | P1 | open | – |
| SRCH-022 | Parent manifest list lookup | P2 | open | – |
| SRCH-023 | Query execution operation | P2 | open | – |
| SRCH-024 | Query structure and primary domains | P2 | open | – |
| SRCH-025 | Domain fields | P2 | open | – |
| SRCH-026 | Cross-domain relation paths | P2 | open | – |
| SRCH-027 | Field criteria and property criteria | P2 | open | – |
| SRCH-028 | Logical composition of criteria | P2 | open | – |
| SRCH-029 | Single-property match group | P2 | open | – |
| SRCH-030 | Comparison operators and wildcards | P2 | open | – |
| SRCH-031 | Absolute and relative time criteria | P2 | open | – |
| SRCH-032 | Output field selection | P2 | open | – |
| SRCH-033 | Sorting, offset and limit | P2 | open | – |
| SRCH-034 | Distinct results toggle | P2 | open | – |
| SRCH-035 | Virtual repository targeting | P2 | open | – |
| SRCH-036 | Query permission model | P2 | open | – |
| SRCH-037 | Limit before permission filter and scan multiplier | P2 | open | – |
| SRCH-038 | Include/exclude patterns filter query results | P2 | open | – |
| SRCH-039 | Transitive search into chained remote instances | Later | open | – |
| SRCH-040 | Bulk delete action with dry run | P2 | open | – |
| SRCH-041 | Bulk property update action with dry run | P2 | open | – |
| SRCH-042 | Result-set hard cap | P2 | open | – |
| SRCH-043 | Query text size limit | P2 | open | – |
| SRCH-044 | Query timeouts | P2 | open | – |
| SRCH-045 | Concurrency limiting for queries and searches | P2 | open | – |
| SRCH-046 | Streaming mode for large result sets | P2 | open | – |
| SRCH-047 | Compact output | P2 | open | – |
| SRCH-064 | Index-friendly criteria and query cost guidance | P2 | open | – |
| SRCH-048 | Global search box | P1 | specified | [R-UI-015](../spec/api-and-ui.md) |
| SRCH-049 | Search keywords | P1 | open | – |
| SRCH-050 | Format-specific package metadata search fields | P1 | specified | [R-NPM-037](../spec/formats/npm.md) |
| SRCH-051 | Package search | P1 | open | – |
| SRCH-052 | Artifact advanced search types | P1 | open | – |
| SRCH-053 | Trash search | P2 | open | – |
| SRCH-054 | Show the generated query | P2 | open | – |
| SRCH-055 | Build search | P1 | open | – |
| SRCH-056 | Release set search | Later | open | – |
| SRCH-057 | Scanned resource search | Later | open | – |
| SRCH-058 | Artifact browser search | P1 | specified | [R-UI-015](../spec/api-and-ui.md) |
| SRCH-059 | Artifact browser scope keywords | P1 | open | – |
| SRCH-060 | Search suggestions and recent searches | P2 | open | – |
| SRCH-061 | Artifact search results table | P1 | open | – |
| SRCH-062 | Repository tree filters, sort order and favorites | P1 | open | – |
| SRCH-063 | Search keyboard shortcuts | P2 | open | – |

</details>

## Builds and CI integration (BLD)

Catalog page: [builds-and-ci.md](../features/builds-and-ci.md) · specified by priority: P1 0 of 15 · P2 0 of 22 · Later 0 of 11 · Out of scope 0 of 1

<details>
<summary>49 features: 0 specified, 49 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| BLD-001 | Build record and run identity | P1 | open | – |
| BLD-002 | Run-level information | P1 | open | – |
| BLD-003 | Modules | P1 | open | – |
| BLD-004 | Module artifacts and artifact linking | P1 | open | – |
| BLD-005 | Module dependencies | P1 | open | – |
| BLD-006 | Environment and system variables | P1 | open | – |
| BLD-007 | Version-control information | P1 | open | – |
| BLD-008 | Issue-tracker references | P2 | open | – |
| BLD-009 | License-check request | Later | open | – |
| BLD-010 | Build metadata repository | P1 | open | – |
| BLD-011 | Project scoping of builds | P2 | open | – |
| BLD-012 | Privileges per build operation | P1 | open | – |
| BLD-013 | Publish a build run | P1 | open | – |
| BLD-014 | List builds | P1 | open | – |
| BLD-015 | List runs of a build | P1 | open | – |
| BLD-016 | Retrieve a build run | P1 | open | – |
| BLD-017 | Compare two runs | P2 | open | – |
| BLD-018 | Append modules to a stored run | P2 | open | – |
| BLD-019 | Aggregate published runs by reference | P2 | open | – |
| BLD-020 | Promote a build run | P2 | open | – |
| BLD-021 | Promotion status and history | P2 | open | – |
| BLD-022 | Rename a build | P2 | open | – |
| BLD-023 | Delete build runs | P1 | open | – |
| BLD-024 | Retention criteria carried in a published run | P1 | open | – |
| BLD-025 | Promote a container image | P2 | open | – |
| BLD-026 | Promotion and staging extensions | Later | open | – |
| BLD-027 | One build identity per CI job | P2 | open | – |
| BLD-028 | Build identity defaults derived from CI variables | P2 | open | – |
| BLD-029 | GitLab CI include template | P2 | open | – |
| BLD-030 | GitHub Actions setup action | P2 | open | – |
| BLD-031 | CI job summary | Later | open | – |
| BLD-047 | Package links between the CI platform and the package view | Later | open | – |
| BLD-032 | Other CI system integrations | Later | open | – |
| BLD-033 | Build-tool plugins for Maven and Gradle | Later | open | – |
| BLD-034 | Legacy CI plugins | Out of scope | open | – |
| BLD-035 | Connection profiles and CLI authentication | P2 | open | – |
| BLD-036 | Generic file operations | P2 | open | – |
| BLD-037 | Spec files | P2 | open | – |
| BLD-038 | Path placeholders | P2 | open | – |
| BLD-039 | Local build metadata collection and publication | P2 | open | – |
| BLD-040 | Package-manager wrapping | P2 | open | – |
| BLD-041 | Native execution mode | P2 | open | – |
| BLD-042 | Transparent command interception | Later | open | – |
| BLD-043 | Command summaries | Later | open | – |
| BLD-044 | Instance-to-instance transfer | Later | open | – |
| BLD-045 | Attach externally pushed container images to a run | P2 | open | – |
| BLD-046 | Configuration commands for repositories, replication and access | P2 | open | – |
| BLD-048 | Extension plugins for the companion client | Later | open | – |
| BLD-049 | Instance statistics and shell conveniences | Later | open | – |

</details>

## Identity and access (IAM)

Catalog page: [identity-and-access.md](../features/identity-and-access.md) · specified by priority: MVP 20 of 20 · P1 11 of 11 · P2 3 of 26 · Later 0 of 7 · Out of scope 0 of 5

<details>
<summary>69 features: 34 specified, 35 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| IAM-001 | User accounts | MVP | specified | [R-AUTH-007](../spec/identity-and-authentication.md), [R-AUTH-016](../spec/identity-and-authentication.md) |
| IAM-002 | System administrator | MVP | specified | [R-AUTHZ-010](../spec/authorization.md), [R-AUTHZ-022](../spec/authorization.md), [R-AUTH-014](../spec/identity-and-authentication.md) |
| IAM-003 | Per-user options | MVP | specified | [R-AUTH-016](../spec/identity-and-authentication.md), [R-AUTH-020](../spec/identity-and-authentication.md) |
| IAM-004 | User status and disabling | MVP | specified | [R-AUTH-016](../spec/identity-and-authentication.md) |
| IAM-005 | Anonymous user | MVP | specified | [R-AUTHZ-003](../spec/authorization.md), [R-NPM-004](../spec/formats/npm.md), [R-AUTH-037](../spec/identity-and-authentication.md) |
| IAM-006 | Auditor role | P1 | specified | [R-AUTHZ-011](../spec/authorization.md) |
| IAM-007 | Delegated system roles: resource manager and webhook manager | P1 | specified | [R-AUTHZ-012](../spec/authorization.md) |
| IAM-008 | Scan-related roles | Later | open | – |
| IAM-009 | User management operations | MVP | specified | [R-AUTH-016](../spec/identity-and-authentication.md) |
| IAM-010 | Administrator recovery by bootstrap credentials | MVP | specified | [R-AUTH-014](../spec/identity-and-authentication.md), [R-AUTH-015](../spec/identity-and-authentication.md) |
| IAM-011 | User invitation by email | Out of scope | open | – |
| IAM-012 | Persistent login ("remember me") | P2 | open | – |
| IAM-013 | Groups | MVP | specified | [R-AUTHZ-002](../spec/authorization.md), [R-AUTH-008](../spec/identity-and-authentication.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| IAM-014 | Group membership management | MVP | specified | [R-AUTH-008](../spec/identity-and-authentication.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| IAM-015 | Auto-join groups | P1 | specified | [R-AUTH-019](../spec/identity-and-authentication.md) |
| IAM-016 | Administrator groups | MVP | specified | [R-AUTHZ-010](../spec/authorization.md), [R-AUTHZ-022](../spec/authorization.md), [R-AUTH-008](../spec/identity-and-authentication.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| IAM-017 | Groups from external realms | P2 | open | – |
| IAM-018 | Group management operations | MVP | specified | [R-AUTH-018](../spec/identity-and-authentication.md) |
| IAM-019 | Permission policy model | MVP | specified | [R-AUTHZ-001](../spec/authorization.md), [R-AUTHZ-015](../spec/authorization.md) |
| IAM-020 | Repository resources and repository wildcards | MVP | specified | [R-AUTHZ-004](../spec/authorization.md) |
| IAM-021 | Include and exclude patterns | MVP | specified | [R-AUTHZ-005](../spec/authorization.md), [R-NPM-004](../spec/formats/npm.md) |
| IAM-022 | Repository actions | MVP | specified | [R-AUTHZ-006](../spec/authorization.md), [R-NPM-004](../spec/formats/npm.md) |
| IAM-023 | Permissions on remote repositories | MVP | specified | [R-AUTHZ-008](../spec/authorization.md), [R-NPM-004](../spec/formats/npm.md) |
| IAM-024 | Permissions through virtual repositories | MVP | specified | [R-AUTHZ-009](../spec/authorization.md), [R-AUTHZ-015](../spec/authorization.md), [R-NPM-004](../spec/formats/npm.md) |
| IAM-025 | Build resources and actions | P1 | specified | [R-AUTHZ-027](../spec/authorization.md) |
| IAM-026 | Release-set resources and actions | Later | open | – |
| IAM-027 | Distribution-target resources and actions | Later | open | – |
| IAM-028 | CI pipeline-source resources and actions | Out of scope | open | – |
| IAM-029 | Delegated policy management (manage action) | P1 | specified | [R-AUTHZ-025](../spec/authorization.md) |
| IAM-030 | Effective permissions view | P1 | specified | [R-API-034](../spec/api-and-ui.md), [R-UI-028](../spec/api-and-ui.md), [R-AUTHZ-024](../spec/authorization.md) |
| IAM-031 | Permission policy operations | MVP | specified | [R-API-033](../spec/api-and-ui.md), [R-AUTHZ-021](../spec/authorization.md) |
| IAM-032 | Hide existence of unauthorized resources | P1 | specified | [R-API-008](../spec/api-and-ui.md) (implemented), [R-AUTHZ-016](../spec/authorization.md), [R-AUTHZ-017](../spec/authorization.md), [R-EP-013](../spec/endpoints.md) |
| IAM-033 | Older permission interfaces | P2 | open | – |
| IAM-034 | Control over anonymous grants by non-administrators | P2 | open | – |
| IAM-035 | Encrypted client passwords | P2 | open | – |
| IAM-036 | Basic-authentication controls | P1 | specified | [R-AUTH-014](../spec/identity-and-authentication.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| IAM-037 | Token generation by basic-authenticated users | P1 | specified | [R-AUTH-027](../spec/identity-and-authentication.md) |
| IAM-038 | Token lifetime limits and expiry notifications | MVP | specified | [R-AUTH-026](../spec/identity-and-authentication.md), [R-AUTH-032](../spec/identity-and-authentication.md), [R-AUTH-040](../spec/identity-and-authentication.md) |
| IAM-039 | Multi-factor authentication (TOTP) | P2 | open | – |
| IAM-040 | Hide global identity mappings from project administrators | P2 | open | – |
| IAM-041 | Password expiration policy | P2 | open | – |
| IAM-069 | Password strength policy | P1 | specified | [R-AUTH-014](../spec/identity-and-authentication.md) |
| IAM-042 | Temporary login suspension and account lockout | P1 | specified | [R-AUTH-038](../spec/identity-and-authentication.md) |
| IAM-043 | Inter-service join key | Out of scope | open | – |
| IAM-044 | Legacy API keys | Out of scope | open | – |
| IAM-045 | Encryption of stored configuration secrets | MVP | specified | [R-ARCH-021](../spec/architecture.md) (implemented), [R-REPO-033](../spec/repositories-and-proxy.md) |
| IAM-046 | Signing key pairs | P2 | open | – |
| IAM-047 | External secret-manager connectors (HashiCorp Vault) | P2 | open | – |
| IAM-048 | JAR signing keystore for virtual repositories | P2 | open | – |
| IAM-049 | Trusted public keys | Later | open | – |
| IAM-050 | SSH authentication | P2 | open | – |
| IAM-051 | Projects | P2 | specified | [R-AUTHZ-026](../spec/authorization.md) |
| IAM-052 | Project administrators and delegated privileges | P2 | specified | [R-AUTHZ-026](../spec/authorization.md) |
| IAM-053 | Project members | P2 | open | – |
| IAM-054 | Global roles | P2 | open | – |
| IAM-055 | Project roles | P2 | open | – |
| IAM-056 | Role action catalog | P2 | open | – |
| IAM-057 | Repository ownership by projects | P2 | specified | [R-AUTHZ-026](../spec/authorization.md) |
| IAM-058 | Sharing repositories across projects | P2 | open | – |
| IAM-059 | Project-aware sharing through internal remote repositories | Later | open | – |
| IAM-060 | Builds in projects | P2 | open | – |
| IAM-061 | Project storage quotas | P2 | open | – |
| IAM-062 | Anonymous access in projects | P2 | open | – |
| IAM-063 | Environments | P2 | open | – |
| IAM-064 | Environments on roles and repositories | P2 | open | – |
| IAM-065 | Project-scoped scan policies, scopes and reports | Later | open | – |
| IAM-066 | Migration from permission policies to projects | Later | open | – |
| IAM-067 | Project count limits per subscription | Out of scope | open | – |
| IAM-068 | Project and role management operations | P2 | open | – |

</details>

## Authentication providers and human login (AUTHN)

Catalog page: [authentication-providers.md](../features/authentication-providers.md) · specified by priority: MVP 13 of 13 · P1 5 of 5 · P2 0 of 25 · Out of scope 0 of 1

<details>
<summary>44 features: 18 specified, 26 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| AUTHN-001 | Delegated authentication with one internal credential | MVP | specified | [R-AUTH-001](../spec/identity-and-authentication.md) |
| AUTHN-002 | External base URL as a prerequisite for single sign-on | MVP | specified | [R-AUTH-003](../spec/identity-and-authentication.md) |
| AUTHN-003 | Internal password login and the basic-authentication switch | MVP | specified | [R-AUTH-014](../spec/identity-and-authentication.md) |
| AUTHN-004 | Fallback from an external directory to the internal password | P2 | open | – |
| AUTHN-005 | Just-in-time user creation and transient users | MVP | specified | [R-AUTH-007](../spec/identity-and-authentication.md) |
| AUTHN-006 | Profile access for externally created users | MVP | specified | [R-AUTH-007](../spec/identity-and-authentication.md), [R-AUTH-017](../spec/identity-and-authentication.md) |
| AUTHN-007 | OAuth single sign-on general settings | MVP | specified | [R-AUTH-004](../spec/identity-and-authentication.md), [R-AUTH-009](../spec/identity-and-authentication.md) |
| AUTHN-008 | OpenID Connect provider definition | MVP | specified | [R-AUTH-004](../spec/identity-and-authentication.md) |
| AUTHN-009 | Authorization code flow with PKCE | MVP | specified | [R-AUTH-005](../spec/identity-and-authentication.md), [R-AUTH-006](../spec/identity-and-authentication.md) |
| AUTHN-010 | Preset provider types: Google, GitHub, Cloud Foundry UAA | P2 | open | – |
| AUTHN-011 | Login-flow state across cluster nodes | MVP | specified | [R-AUTH-005](../spec/identity-and-authentication.md), [R-AUTH-010](../spec/identity-and-authentication.md), [R-AUTH-042](../spec/identity-and-authentication.md) |
| AUTHN-012 | Binding an internal account to an OAuth identity | P2 | open | – |
| AUTHN-013 | Automatic pairing of SSO users by e-mail address | P2 | open | – |
| AUTHN-014 | Personal sign-up through public OAuth providers and invitations | Out of scope | open | – |
| AUTHN-015 | SAML service-provider login | P2 | open | – |
| AUTHN-016 | SAML configurations | P2 | open | – |
| AUTHN-017 | Encrypted SAML assertions | P2 | open | – |
| AUTHN-018 | Group association from SAML attributes | P2 | open | – |
| AUTHN-019 | Automatic redirect to the SAML IdP | P2 | open | – |
| AUTHN-020 | SAML single logout | P2 | open | – |
| AUTHN-021 | LDAP group lookup for SAML users | P2 | open | – |
| AUTHN-022 | LDAP connection settings | P2 | open | – |
| AUTHN-023 | LDAP login flow | P2 | open | – |
| AUTHN-024 | LDAPS, timeouts and directory tuning | P2 | open | – |
| AUTHN-025 | LDAP group settings and mapping strategies | P2 | open | – |
| AUTHN-026 | Importing and synchronizing directory groups | P2 | open | – |
| AUTHN-027 | Authentication caching and directory revalidation | P2 | open | – |
| AUTHN-028 | SCIM 2.0 user and group provisioning | P1 | specified | [R-AUTH-039](../spec/identity-and-authentication.md) |
| AUTHN-029 | SCIM connector token | P1 | specified | [R-AUTH-039](../spec/identity-and-authentication.md) |
| AUTHN-030 | Effects of de-provisioning on credentials | P1 | specified | [R-AUTH-039](../spec/identity-and-authentication.md) |
| AUTHN-031 | Trusted reverse-proxy authentication | P2 | open | – |
| AUTHN-032 | Crowd / Jira user-management connector | P2 | open | – |
| AUTHN-033 | Crowd group import | P2 | open | – |
| AUTHN-034 | Mutual TLS client authentication | P2 | open | – |
| AUTHN-035 | One-time-password second factor | P2 | open | – |
| AUTHN-036 | Anonymous access | P1 | specified | [R-AUTHZ-003](../spec/authorization.md), [R-AUTH-037](../spec/identity-and-authentication.md), [R-AUTH-040](../spec/identity-and-authentication.md) |
| AUTHN-037 | Login page presentation | MVP | specified | [R-UI-004](../spec/api-and-ui.md), [R-AUTH-009](../spec/identity-and-authentication.md) |
| AUTHN-038 | UI session behavior | MVP | specified | [R-UI-004](../spec/api-and-ui.md), [R-AUTH-010](../spec/identity-and-authentication.md), [R-AUTH-011](../spec/identity-and-authentication.md), [R-AUTH-013](../spec/identity-and-authentication.md) |
| AUTHN-039 | User profile page | MVP | specified | [R-UI-029](../spec/api-and-ui.md), [R-AUTH-017](../spec/identity-and-authentication.md) |
| AUTHN-040 | Personal identity tokens | MVP | specified | [R-UI-029](../spec/api-and-ui.md), [R-AUTH-017](../spec/identity-and-authentication.md), [R-AUTH-027](../spec/identity-and-authentication.md) |
| AUTHN-041 | SSH public keys | P2 | open | – |
| AUTHN-042 | Password self-service and reset | P2 | open | – |
| AUTHN-043 | Legacy API key | P2 | open | – |
| AUTHN-044 | Browser login handoff for command-line tools | P1 | specified | [R-AUTH-034](../spec/identity-and-authentication.md), [R-AUTH-036](../spec/identity-and-authentication.md) |

</details>

## Access tokens (TOK)

Catalog page: [access-tokens.md](../features/access-tokens.md) · specified by priority: MVP 24 of 24 · P1 3 of 3 · P2 0 of 12 · Later 0 of 6 · Out of scope 0 of 3

<details>
<summary>48 features: 27 specified, 21 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| TOK-001 | Access token as the system's own credential | MVP | specified | [R-AUTH-001](../spec/identity-and-authentication.md) |
| TOK-002 | Token attributes and claims | MVP | specified | [R-AUTH-022](../spec/identity-and-authentication.md) |
| TOK-003 | Self-contained signed token format | P2 | open | – |
| TOK-004 | Reference tokens | MVP | specified | [R-AUTH-022](../spec/identity-and-authentication.md), [R-AUTH-023](../spec/identity-and-authentication.md) |
| TOK-005 | Identity tokens | MVP | specified | [R-AUTH-024](../spec/identity-and-authentication.md) |
| TOK-006 | One-time display of token secrets | MVP | specified | [R-AUTH-022](../spec/identity-and-authentication.md), [R-AUTH-027](../spec/identity-and-authentication.md) |
| TOK-007 | Scope expression | MVP | specified | [R-AUTHZ-013](../spec/authorization.md), [R-EP-011](../spec/endpoints.md), [R-AUTH-024](../spec/identity-and-authentication.md) |
| TOK-008 | User-permissions scope | MVP | specified | [R-AUTHZ-002](../spec/authorization.md), [R-AUTHZ-013](../spec/authorization.md), [R-AUTH-024](../spec/identity-and-authentication.md) |
| TOK-009 | Administrator scope | MVP | specified | [R-AUTHZ-013](../spec/authorization.md), [R-AUTH-024](../spec/identity-and-authentication.md) |
| TOK-010 | Group scope | MVP | specified | [R-AUTHZ-013](../spec/authorization.md), [R-AUTH-020](../spec/identity-and-authentication.md) |
| TOK-011 | Project-role scope | P2 | open | – |
| TOK-012 | Resource permission scope | MVP | specified | [R-AUTHZ-013](../spec/authorization.md), [R-AUTH-024](../spec/identity-and-authentication.md) |
| TOK-013 | Project membership read scopes | P2 | open | – |
| TOK-014 | System scopes | P1 | specified | [R-AUTHZ-011](../spec/authorization.md), [R-AUTH-025](../spec/identity-and-authentication.md) |
| TOK-015 | Transient subjects and automatic group membership | MVP | specified | [R-AUTHZ-002](../spec/authorization.md), [R-AUTHZ-014](../spec/authorization.md), [R-AUTH-020](../spec/identity-and-authentication.md) |
| TOK-016 | Audience restriction | P2 | open | – |
| TOK-017 | Token lifetime and non-expiring tokens | MVP | specified | [R-AUTH-026](../spec/identity-and-authentication.md) |
| TOK-018 | Default token expiry | MVP | specified | [R-AUTH-026](../spec/identity-and-authentication.md) |
| TOK-019 | Maximum expiry for non-administrators | MVP | specified | [R-AUTH-026](../spec/identity-and-authentication.md) |
| TOK-020 | Revocability threshold | P2 | open | – |
| TOK-021 | Persistence threshold | P2 | open | – |
| TOK-022 | Force-revocable flag | P2 | open | – |
| TOK-023 | Token expiry notifications | P2 | open | – |
| TOK-024 | Refreshable tokens and the refresh grant | P1 | specified | [R-AUTH-030](../spec/identity-and-authentication.md) |
| TOK-025 | Create a token | MVP | specified | [R-AUTH-027](../spec/identity-and-authentication.md) |
| TOK-026 | Token management screen | MVP | specified | [R-UI-030](../spec/api-and-ui.md), [R-AUTH-028](../spec/identity-and-authentication.md) |
| TOK-027 | List tokens | MVP | specified | [R-AUTH-028](../spec/identity-and-authentication.md) |
| TOK-028 | Get token details | MVP | specified | [R-AUTH-028](../spec/identity-and-authentication.md) |
| TOK-029 | Last-used tracking | P1 | specified | [R-AUTH-031](../spec/identity-and-authentication.md) |
| TOK-030 | Revoke a token by ID | MVP | specified | [R-AUTH-029](../spec/identity-and-authentication.md) |
| TOK-031 | Revoke a token by value | MVP | specified | [R-AUTH-029](../spec/identity-and-authentication.md) |
| TOK-032 | Revocation semantics | MVP | specified | [R-AUTH-023](../spec/identity-and-authentication.md), [R-AUTH-029](../spec/identity-and-authentication.md) |
| TOK-033 | Bearer authentication | MVP | specified | [R-NPM-004](../spec/formats/npm.md), [R-AUTH-001](../spec/identity-and-authentication.md), [R-AUTH-002](../spec/identity-and-authentication.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| TOK-034 | Token as the password in Basic authentication | MVP | specified | [R-NPM-004](../spec/formats/npm.md), [R-AUTH-001](../spec/identity-and-authentication.md), [R-AUTH-002](../spec/identity-and-authentication.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| TOK-035 | Command-line client token handling | P2 | open | – |
| TOK-036 | Token-signing key pair | P2 | open | – |
| TOK-037 | Custom signing certificates | P2 | open | – |
| TOK-038 | Signing key reset and rotation | P2 | open | – |
| TOK-039 | Cross-instance token trust | Later | open | – |
| TOK-040 | Centrally managed trust links between instances | Later | open | – |
| TOK-041 | Bootstrap administrator token | MVP | specified | [R-AUTH-015](../spec/identity-and-authentication.md) |
| TOK-042 | Pairing tokens | Later | open | – |
| TOK-043 | Shared inter-service secret | Out of scope | open | – |
| TOK-048 | Lifetimes and cleanup of internal service credentials | Out of scope | open | – |
| TOK-044 | Legacy API keys | Later | open | – |
| TOK-045 | Hosted account-portal token | Out of scope | open | – |
| TOK-046 | Deprecated token operations of the repository service | Later | open | – |
| TOK-047 | Token operations of the distribution service | Later | open | – |

</details>

## OIDC federation for CI systems (CIOIDC)

Catalog page: [oidc-ci-integration.md](../features/oidc-ci-integration.md) · specified by priority: MVP 20 of 20 · P1 9 of 9 · P2 2 of 10 · Later 0 of 1

<details>
<summary>40 features: 31 specified, 9 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| CIOIDC-001 | OIDC provider configuration | MVP | specified | [R-CI-001](../spec/gitlab-ci-oidc.md), [R-CI-005](../spec/gitlab-ci-oidc.md) |
| CIOIDC-002 | Generic OpenID Connect provider type | MVP | specified | [R-CI-001](../spec/gitlab-ci-oidc.md), [R-CI-005](../spec/gitlab-ci-oidc.md), [R-CI-026](../spec/gitlab-ci-oidc.md) |
| CIOIDC-003 | GitHub Actions provider type with organization restriction | P1 | specified | [R-CI-025](../spec/gitlab-ci-oidc.md) |
| CIOIDC-004 | Azure DevOps provider type | P1 | specified | [R-CI-026](../spec/gitlab-ci-oidc.md) |
| CIOIDC-005 | Provider configuration management operations | MVP | specified | [R-CI-002](../spec/gitlab-ci-oidc.md) |
| CIOIDC-006 | Project association of provider configurations | P1 | specified | [R-CI-023](../spec/gitlab-ci-oidc.md) |
| CIOIDC-007 | Identity mapping | MVP | specified | [R-CI-009](../spec/gitlab-ci-oidc.md), [R-CI-016](../spec/gitlab-ci-oidc.md) |
| CIOIDC-008 | Claims filter matching | MVP | specified | [R-CI-010](../spec/gitlab-ci-oidc.md) |
| CIOIDC-009 | Mapping evaluation order | MVP | specified | [R-CI-009](../spec/gitlab-ci-oidc.md) |
| CIOIDC-010 | Mapped token subject kinds and their combinations | MVP | specified | [R-CI-013](../spec/gitlab-ci-oidc.md), [R-CI-017](../spec/gitlab-ci-oidc.md), [R-CI-024](../spec/gitlab-ci-oidc.md) |
| CIOIDC-011 | Dynamic mapping pattern language | MVP | specified | [R-CI-014](../spec/gitlab-ci-oidc.md) |
| CIOIDC-012 | Dynamic user and group subjects | P1 | specified | [R-CI-024](../spec/gitlab-ci-oidc.md) |
| CIOIDC-013 | Dynamic resource scope from a claim | MVP | specified | [R-AUTHZ-014](../spec/authorization.md), [R-CI-013](../spec/gitlab-ci-oidc.md), [R-CI-014](../spec/gitlab-ci-oidc.md), [R-CI-015](../spec/gitlab-ci-oidc.md) |
| CIOIDC-014 | Transient users from user-subject mappings | P1 | specified | [R-AUTHZ-014](../spec/authorization.md), [R-CI-013](../spec/gitlab-ci-oidc.md), [R-CI-017](../spec/gitlab-ci-oidc.md) |
| CIOIDC-015 | Project-scoped identity mappings | P1 | specified | [R-CI-023](../spec/gitlab-ci-oidc.md) |
| CIOIDC-016 | Hiding global identity mappings from project administrators | P2 | open | – |
| CIOIDC-017 | Identity mapping management operations | MVP | specified | [R-CI-019](../spec/gitlab-ci-oidc.md) |
| CIOIDC-018 | OIDC token exchange operation | MVP | specified | [R-NPM-022](../spec/formats/npm.md), [R-CI-007](../spec/gitlab-ci-oidc.md), [R-CI-008](../spec/gitlab-ci-oidc.md), [R-CI-017](../spec/gitlab-ci-oidc.md), [R-CI-018](../spec/gitlab-ci-oidc.md), [R-CI-020](../spec/gitlab-ci-oidc.md), [R-CI-021](../spec/gitlab-ci-oidc.md) |
| CIOIDC-019 | ID-token verification during exchange | MVP | specified | [R-CI-003](../spec/gitlab-ci-oidc.md), [R-CI-004](../spec/gitlab-ci-oidc.md), [R-CI-018](../spec/gitlab-ci-oidc.md) |
| CIOIDC-020 | Reference token in the exchange response | P2 | open | – |
| CIOIDC-021 | Explicit identity-mapping selection on exchange | P2 | open | – |
| CIOIDC-022 | Application-scoped exchange | Later | open | – |
| CIOIDC-023 | CI run metadata on exchange | P2 | specified | [R-CI-020](../spec/gitlab-ci-oidc.md) |
| CIOIDC-024 | Workflow snippet generator | P2 | specified | [R-UI-035](../spec/api-and-ui.md), [R-CI-027](../spec/gitlab-ci-oidc.md) |
| CIOIDC-025 | Companion CLI token exchange | P2 | open | – |
| CIOIDC-026 | GitHub App onboarding mode | P2 | open | – |
| CIOIDC-027 | Cloud IAM role binding to a user | P2 | open | – |
| CIOIDC-028 | Signed cloud caller-identity token exchange | P2 | open | – |
| CIOIDC-029 | Passwordless image pulls for Kubernetes workloads | P2 | open | – |
| CIOIDC-030 | `id_tokens` keyword | MVP | specified | [R-NPM-022](../spec/formats/npm.md), [R-CI-006](../spec/gitlab-ci-oidc.md), [R-CI-028](../spec/gitlab-ci-oidc.md) |
| CIOIDC-031 | ID-token signing, issuer, keys and lifetime | MVP | specified | [R-CI-003](../spec/gitlab-ci-oidc.md), [R-CI-004](../spec/gitlab-ci-oidc.md), [R-CI-006](../spec/gitlab-ci-oidc.md), [R-CI-016](../spec/gitlab-ci-oidc.md) |
| CIOIDC-032 | Standard JWT claims in GitLab ID tokens | MVP | specified | [R-CI-004](../spec/gitlab-ci-oidc.md), [R-CI-006](../spec/gitlab-ci-oidc.md) |
| CIOIDC-033 | GitLab-specific ID-token claims | MVP | specified | [R-CI-005](../spec/gitlab-ci-oidc.md), [R-CI-006](../spec/gitlab-ci-oidc.md) |
| CIOIDC-034 | `sub` claim format and customization | MVP | specified | [R-CI-006](../spec/gitlab-ci-oidc.md), [R-CI-012](../spec/gitlab-ci-oidc.md) |
| CIOIDC-035 | Pipeline source values | MVP | specified | [R-CI-011](../spec/gitlab-ci-oidc.md) |
| CIOIDC-036 | Fork and merge request pipelines | MVP | specified | [R-CI-012](../spec/gitlab-ci-oidc.md) |
| CIOIDC-037 | Protected refs and protected environments | MVP | specified | [R-CI-011](../spec/gitlab-ci-oidc.md) |
| CIOIDC-038 | `CI_JOB_TOKEN` properties | P1 | specified | [R-CI-022](../spec/gitlab-ci-oidc.md) |
| CIOIDC-039 | API endpoints that accept `CI_JOB_TOKEN` | P1 | specified | [R-CI-022](../spec/gitlab-ci-oidc.md) |
| CIOIDC-040 | Job-token allowlist and scope | P1 | specified | [R-CI-022](../spec/gitlab-ci-oidc.md) |

</details>

## Storage and blob providers (STOR)

Catalog page: [storage-and-blobs.md](../features/storage-and-blobs.md) · specified by priority: MVP 12 of 12 · P1 7 of 7 · P2 2 of 20 · Later 0 of 1 · Out of scope 0 of 1

<details>
<summary>41 features: 21 specified, 20 open, 11 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| STOR-001 | Content-addressed binary storage with deduplication | MVP | specified | [R-ARCH-009](../spec/architecture.md), [R-ARCH-011](../spec/architecture.md), [R-STOR-001](../spec/storage.md) (implemented), [R-STOR-003](../spec/storage.md) (implemented), [R-STOR-004](../spec/storage.md) (implemented), [R-STOR-005](../spec/storage.md) (implemented) |
| STOR-002 | Reference-only copy, move and delete | MVP | specified | [R-ARCH-009](../spec/architecture.md), [R-STOR-003](../spec/storage.md) (implemented), [R-STOR-005](../spec/storage.md) (implemented), [R-STOR-018](../spec/storage.md), [R-STOR-019](../spec/storage.md) |
| STOR-003 | Checksum-first transfers | P1 | specified | [R-STOR-008](../spec/storage.md) |
| STOR-004 | SHA-256 kept as a first-class checksum | MVP | specified | [R-ARCH-014](../spec/architecture.md), [R-STOR-002](../spec/storage.md) (implemented) |
| STOR-005 | SHA-256 backfill job | P2 | open | – |
| STOR-006 | Binary store configuration | MVP | implemented | [R-STOR-030](../spec/storage.md) (implemented), [R-STOR-035](../spec/storage.md) (implemented) |
| STOR-007 | Composable provider chain and built-in templates | P2 | open | – |
| STOR-008 | Local file-system store | P2 | open | – |
| STOR-009 | Node-local read cache | P2 | open | – |
| STOR-010 | Database blob store | P2 | open | – |
| STOR-011 | Sharded store with redundancy | P2 | open | – |
| STOR-012 | Health-aware shards and read-only shards | P2 | open | – |
| STOR-013 | Cross-zone sharding | P2 | open | – |
| STOR-014 | Cluster-shared node-local stores | P2 | open | – |
| STOR-015 | Rebalancing and storage optimization | P2 | open | – |
| STOR-016 | Asynchronous upload to the persistent store | P2 | open | – |
| STOR-017 | Retry of store operations | P1 | implemented | [R-STOR-033](../spec/storage.md) (implemented) |
| STOR-018 | Binary fetch workers for federated repositories | Later | open | – |
| STOR-019 | Direct (synchronous) upload to object storage | MVP | specified | [R-ARCH-014](../spec/architecture.md), [R-STOR-004](../spec/storage.md) (implemented), [R-STOR-005](../spec/storage.md) (implemented) |
| STOR-020 | Object-store connection and bucket settings | MVP | implemented | [R-STOR-030](../spec/storage.md) (implemented) |
| STOR-021 | Object-store authentication | MVP | implemented | [R-STOR-031](../spec/storage.md) (implemented) |
| STOR-022 | Object-store encryption | P1 | specified | [R-STOR-032](../spec/storage.md) |
| STOR-023 | Multipart upload and transfer thresholds | MVP | implemented | [R-STOR-006](../spec/storage.md) (implemented) |
| STOR-024 | Object-store connection pool, timeouts and transport health | MVP | implemented | [R-STOR-033](../spec/storage.md) (implemented) |
| STOR-025 | Outbound proxy for object-store traffic | P1 | specified | [R-STOR-034](../spec/storage.md) |
| STOR-026 | Multi-bucket sharding on object storage | P2 | open | – |
| STOR-027 | Migration of an existing store into object storage | P2 | open | – |
| STOR-028 | Storage-class compatibility | P2 | open | – |
| STOR-029 | Compatibility with S3-compatible object stores | MVP | implemented | [R-STOR-030](../spec/storage.md) (implemented), [R-STOR-035](../spec/storage.md) (implemented) |
| STOR-030 | Google Cloud Storage | P2 | open | – |
| STOR-031 | Azure Blob Storage | P2 | open | – |
| STOR-032 | Download redirect to signed object-storage URLs | P2 | implemented | [R-STOR-014](../spec/storage.md) (implemented) |
| STOR-033 | Minimum size for redirected downloads | P2 | implemented | [R-STOR-014](../spec/storage.md) (implemented) |
| STOR-034 | CDN-signed download URLs | P2 | open | – |
| STOR-035 | Client choice of the redirect target | P2 | open | – |
| STOR-036 | Garbage collection of unreferenced binaries | MVP | implemented | [R-STOR-024](../spec/storage.md) (implemented) |
| STOR-037 | Garbage-collection schedule and manual run | MVP | implemented | [R-ARCH-016](../spec/architecture.md) (implemented), [R-STOR-025](../spec/storage.md) (implemented) |
| STOR-038 | Pruning unreferenced data in the store | P1 | specified | [R-STOR-026](../spec/storage.md) |
| STOR-039 | Storage summary | P1 | specified | [R-OPS-023](../spec/operations-and-observability.md), [R-STOR-028](../spec/storage.md) |
| STOR-040 | Storage quota and disk-space thresholds | P1 | specified | [R-STOR-029](../spec/storage.md) |
| STOR-041 | Compact the embedded database | Out of scope | open | – |

</details>

## Retention, cleanup and archiving (RET)

Catalog page: [retention-and-cleanup.md](../features/retention-and-cleanup.md) · specified by priority: P1 26 of 26 · P2 0 of 3 · Later 0 of 11 · Out of scope 0 of 2

<details>
<summary>42 features: 26 specified, 16 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| RET-001 | Retention policy model | P1 | specified | [R-STOR-037](../spec/storage.md) |
| RET-002 | Global and project-scoped policies | P2 | open | – |
| RET-003 | Repository and path scope | P1 | specified | [R-STOR-038](../spec/storage.md) |
| RET-004 | Package name and version selectors | P1 | specified | [R-STOR-038](../spec/storage.md) |
| RET-005 | Age and usage conditions | P1 | specified | [R-STOR-038](../spec/storage.md) |
| RET-006 | Property conditions | P1 | specified | [R-STOR-038](../spec/storage.md) |
| RET-007 | Keep last N versions | P1 | specified | [R-STOR-038](../spec/storage.md) |
| RET-008 | Combining conditions | P1 | specified | [R-STOR-038](../spec/storage.md) |
| RET-009 | Lead file and format support | P1 | specified | [R-STOR-039](../spec/storage.md) |
| RET-010 | Container images under package policies | P1 | specified | [R-STOR-039](../spec/storage.md) |
| RET-011 | Cron schedule | P1 | specified | [R-STOR-040](../spec/storage.md) |
| RET-012 | Maximum run duration and incremental processing | P1 | specified | [R-STOR-040](../spec/storage.md) |
| RET-013 | Enable and disable a policy | P1 | specified | [R-STOR-037](../spec/storage.md) |
| RET-014 | Manual run | P1 | specified | [R-STOR-041](../spec/storage.md) |
| RET-015 | Dry run | P1 | specified | [R-STOR-041](../spec/storage.md) |
| RET-016 | Stop a run and stop all runs | P1 | specified | [R-STOR-041](../spec/storage.md) |
| RET-017 | Disable all policies | P1 | specified | [R-STOR-041](../spec/storage.md) |
| RET-018 | Run concurrency, queuing and limits | P1 | specified | [R-STOR-040](../spec/storage.md) |
| RET-019 | Edit and delete a policy | P1 | specified | [R-STOR-037](../spec/storage.md) |
| RET-020 | Policy list and run history | P1 | specified | [R-STOR-042](../spec/storage.md) |
| RET-021 | Run reports | P1 | specified | [R-STOR-042](../spec/storage.md) |
| RET-022 | Package cleanup policy | P1 | specified | [R-STOR-038](../spec/storage.md) |
| RET-023 | Build cleanup policy | P1 | specified | [R-STOR-043](../spec/storage.md) |
| RET-024 | Release-set cleanup policy | Later | open | – |
| RET-025 | Cleanup and archiving on federated repositories | Later | open | – |
| RET-026 | Retention and push replication | P2 | open | – |
| RET-027 | Package archive policy | Later | open | – |
| RET-028 | Archive instance pairing | Later | open | – |
| RET-029 | Archive search | Later | open | – |
| RET-030 | Restore from the archive | Later | open | – |
| RET-031 | Restore runs and reports | Later | open | – |
| RET-032 | Archive storage overview | Later | open | – |
| RET-033 | Archive and restore logging | Later | open | – |
| RET-034 | Artifact-level archive policy (older archive) | Later | open | – |
| RET-035 | Maximum unique snapshots, unique tags and tag revisions | P1 | specified | [R-STOR-044](../spec/storage.md) |
| RET-036 | Unused-cache expiry on remote repositories | P1 | specified | [R-STOR-045](../spec/storage.md) |
| RET-037 | Retention parameters per build name | P1 | specified | [R-STOR-043](../spec/storage.md) |
| RET-038 | Trash can interaction | P1 | specified | [R-STOR-022](../spec/storage.md) |
| RET-039 | Release-set retention policies | Later | open | – |
| RET-042 | Search-driven and scripted custom cleanup | P2 | open | – |
| RET-040 | License-tier gating of retention features | Out of scope | open | – |
| RET-041 | Portal provisioning of hosted archive instances | Out of scope | open | – |

</details>

## High availability and deployment (HA)

Catalog page: [ha-and-deployment.md](../features/ha-and-deployment.md) · specified by priority: MVP 49 of 49 · P1 11 of 14 · P2 1 of 10 · Later 0 of 3 · Out of scope 0 of 3

<details>
<summary>79 features: 61 specified, 18 open, 26 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| HA-001 | Active-active cluster of identical nodes | MVP | specified | [R-ARCH-001](../spec/architecture.md) (implemented), [R-ARCH-032](../spec/architecture.md) (implemented), [R-HA-001](../spec/ha-and-helm.md), [R-AUTH-042](../spec/identity-and-authentication.md) |
| HA-002 | Cluster-wide task execution without a primary node | MVP | specified | [R-ARCH-001](../spec/architecture.md) (implemented), [R-ARCH-016](../spec/architecture.md) (implemented), [R-ARCH-017](../spec/architecture.md) (implemented), [R-HA-001](../spec/ha-and-helm.md), [R-HA-005](../spec/ha-and-helm.md) (implemented) |
| HA-003 | Shared metadata database as the cluster backbone | MVP | specified | [R-ARCH-002](../spec/architecture.md) (implemented), [R-HA-007](../spec/ha-and-helm.md), [R-HA-013](../spec/ha-and-helm.md) (implemented) |
| HA-004 | Shared binary storage for all nodes | MVP | specified | [R-ARCH-002](../spec/architecture.md) (implemented), [R-HA-007](../spec/ha-and-helm.md), [R-HA-010](../spec/ha-and-helm.md) |
| HA-005 | Node-local persistent volume for transient data | MVP | specified | [R-ARCH-014](../spec/architecture.md), [R-HA-012](../spec/ha-and-helm.md), [R-HA-035](../spec/ha-and-helm.md) (implemented), [R-HA-061](../spec/ha-and-helm.md) (implemented) |
| HA-006 | Cluster-wide synchronization of configuration and state | MVP | specified | [R-ARCH-002](../spec/architecture.md) (implemented), [R-ARCH-017](../spec/architecture.md) (implemented), [R-ARCH-018](../spec/architecture.md) (implemented), [R-ARCH-030](../spec/architecture.md) (implemented), [R-HA-003](../spec/ha-and-helm.md), [R-HA-004](../spec/ha-and-helm.md) (implemented) |
| HA-007 | Distributed locking through the database | MVP | specified | [R-ARCH-017](../spec/architecture.md) (implemented), [R-ARCH-030](../spec/architecture.md) (implemented), [R-ARCH-033](../spec/architecture.md), [R-HA-005](../spec/ha-and-helm.md) (implemented), [R-HA-006](../spec/ha-and-helm.md) (implemented) |
| HA-008 | Network locality of cluster components | MVP | specified | [R-HA-049](../spec/ha-and-helm.md) |
| HA-009 | Horizontal scaling: adding and removing nodes | MVP | specified | [R-ARCH-001](../spec/architecture.md) (implemented), [R-ARCH-032](../spec/architecture.md) (implemented), [R-HA-003](../spec/ha-and-helm.md), [R-HA-059](../spec/ha-and-helm.md), [R-HA-032](../spec/ha-and-helm.md) (implemented), [R-AUTH-042](../spec/identity-and-authentication.md) |
| HA-010 | Deployment topology: single node or cluster | MVP | specified | [R-ARCH-001](../spec/architecture.md) (implemented), [R-HA-001](../spec/ha-and-helm.md) |
| HA-011 | Active-passive warm standby | P2 | open | – |
| HA-012 | Single client entry point with path-based routing | MVP | implemented | [R-ARCH-005](../spec/architecture.md) (implemented), [R-HA-037](../spec/ha-and-helm.md) (implemented) |
| HA-013 | Internal service registry and health-gated routing | Later | open | – |
| HA-014 | Availability-zone affinity for internal routing | P2 | open | – |
| HA-015 | Components deployed as separately scaled workloads | P1 | specified | [R-ARCH-001](../spec/architecture.md) (implemented), [R-HA-002](../spec/ha-and-helm.md) (implemented), [R-HA-062](../spec/ha-and-helm.md) |
| HA-016 | Cluster encryption key | MVP | specified | [R-API-049](../spec/api-and-ui.md), [R-ARCH-021](../spec/architecture.md) (implemented), [R-HA-027](../spec/ha-and-helm.md) (implemented) |
| HA-017 | Service trust key provisioning and rotation | Out of scope | open | – |
| HA-018 | Internal TLS with a built-in certificate authority | P2 | implemented | [R-ARCH-035](../spec/architecture.md) (implemented), [R-HA-063](../spec/ha-and-helm.md) (implemented) |
| HA-019 | Trust store for outbound TLS connections | MVP | specified | [R-ARCH-022](../spec/architecture.md) (implemented), [R-HA-043](../spec/ha-and-helm.md) |
| HA-020 | IPv6 and dual-stack networking | P1 | specified | [R-ARCH-005](../spec/architecture.md) (implemented), [R-HA-044](../spec/ha-and-helm.md) |
| HA-021 | Network ports and listener layout | MVP | implemented | [R-ARCH-005](../spec/architecture.md) (implemented), [R-EP-001](../spec/endpoints.md) (implemented), [R-HA-028](../spec/ha-and-helm.md) (implemented) |
| HA-022 | Load balancer in front of the cluster | MVP | implemented | [R-HA-037](../spec/ha-and-helm.md) (implemented) |
| HA-023 | Health endpoints for load balancers and probes | MVP | specified | [R-HA-008](../spec/ha-and-helm.md), [R-OPS-001](../spec/operations-and-observability.md) (implemented) |
| HA-024 | Proxy requirements for large and long transfers | MVP | specified | [R-ARCH-005](../spec/architecture.md) (implemented), [R-HA-039](../spec/ha-and-helm.md) |
| HA-025 | Forwarded headers and the public base URL | MVP | specified | [R-ARCH-006](../spec/architecture.md) (implemented), [R-EP-003](../spec/endpoints.md) (implemented), [R-HA-040](../spec/ha-and-helm.md) |
| HA-026 | Reverse-proxy configuration generator | P2 | open | – |
| HA-027 | Container-registry routing at the edge | P1 | specified | [R-EP-007](../spec/endpoints.md), [R-HA-041](../spec/ha-and-helm.md) (implemented) |
| HA-028 | Client-certificate forwarding from the reverse proxy | P2 | open | – |
| HA-029 | Helm chart as the Kubernetes deployment | MVP | specified | [R-HA-028](../spec/ha-and-helm.md) (implemented), [R-HA-031](../spec/ha-and-helm.md) |
| HA-030 | Fail-fast chart validation | MVP | implemented | [R-HA-029](../spec/ha-and-helm.md) (implemented) |
| HA-031 | Cluster key provisioning in the chart | MVP | implemented | [R-HA-027](../spec/ha-and-helm.md) (implemented) |
| HA-032 | Replica count and clustered mode in the chart | MVP | implemented | [R-HA-028](../spec/ha-and-helm.md) (implemented), [R-HA-032](../spec/ha-and-helm.md) (implemented) |
| HA-033 | External database configuration in the chart | MVP | implemented | [R-HA-030](../spec/ha-and-helm.md) (implemented) |
| HA-034 | Bundled evaluation database | P2 | open | – |
| HA-035 | Secrets handling in the chart | MVP | implemented | [R-HA-030](../spec/ha-and-helm.md) (implemented) |
| HA-036 | Configuration rendering and override precedence | MVP | implemented | [R-HA-030](../spec/ha-and-helm.md) (implemented), [R-OPS-028](../spec/operations-and-observability.md) (implemented) |
| HA-037 | First-start bootstrap | MVP | specified | [R-HA-030](../spec/ha-and-helm.md) (implemented), [R-OPS-031](../spec/operations-and-observability.md) |
| HA-038 | Kubernetes Service exposure | MVP | implemented | [R-EP-008](../spec/endpoints.md) (implemented), [R-HA-028](../spec/ha-and-helm.md) (implemented), [R-HA-037](../spec/ha-and-helm.md) (implemented) |
| HA-039 | Ingress exposure | MVP | implemented | [R-EP-008](../spec/endpoints.md) (implemented), [R-HA-037](../spec/ha-and-helm.md) (implemented) |
| HA-040 | Gateway API exposure | P1 | specified | [R-EP-008](../spec/endpoints.md) (implemented), [R-HA-038](../spec/ha-and-helm.md), [R-HA-053](../spec/ha-and-helm.md) |
| HA-041 | Bundled edge reverse proxy | P2 | open | – |
| HA-042 | TLS termination topology in Kubernetes | MVP | implemented | [R-HA-037](../spec/ha-and-helm.md) (implemented) |
| HA-043 | Horizontal pod autoscaling | MVP | implemented | [R-HA-032](../spec/ha-and-helm.md) (implemented) |
| HA-044 | Scheduling and disruption controls | MVP | implemented | [R-HA-032](../spec/ha-and-helm.md) (implemented) |
| HA-045 | Container security hardening | MVP | implemented | [R-HA-033](../spec/ha-and-helm.md) (implemented) |
| HA-046 | Health probes in the chart | MVP | implemented | [R-HA-034](../spec/ha-and-helm.md) (implemented) |
| HA-047 | Graceful termination and connection draining | MVP | specified | [R-ARCH-024](../spec/architecture.md) (implemented), [R-HA-011](../spec/ha-and-helm.md) (implemented), [R-HA-059](../spec/ha-and-helm.md) |
| HA-048 | Custom volumes and volume mounts | P1 | specified | [R-HA-046](../spec/ha-and-helm.md) |
| HA-049 | Custom init containers | P1 | specified | [R-HA-046](../spec/ha-and-helm.md) |
| HA-050 | Custom sidecar containers | P1 | specified | [R-HA-046](../spec/ha-and-helm.md) |
| HA-051 | Environment variables, lifecycle hooks and ConfigMaps | P1 | specified | [R-HA-046](../spec/ha-and-helm.md) |
| HA-052 | Resource requests, limits and sizing profiles | MVP | specified | [R-HA-036](../spec/ha-and-helm.md) (implemented), [R-HA-050](../spec/ha-and-helm.md) |
| HA-053 | Persistence and storage back-end selection in the chart | MVP | implemented | [R-HA-030](../spec/ha-and-helm.md) (implemented) |
| HA-054 | Network policies | MVP | implemented | [R-HA-042](../spec/ha-and-helm.md) (implemented) |
| HA-055 | Log shipping and metrics integration in the chart | P1 | specified | [R-HA-045](../spec/ha-and-helm.md) |
| HA-056 | Private image registry and air-gapped installation | P1 | specified | [R-HA-047](../spec/ha-and-helm.md) |
| HA-057 | Server-side extension scripts delivered through the chart | Later | open | – |
| HA-073 | Message-broker TLS for the scanning component | Later | open | – |
| HA-058 | Supported database engine and versions | MVP | implemented | [R-HA-013](../spec/ha-and-helm.md) (implemented) |
| HA-059 | Non-PostgreSQL database engines | Out of scope | open | – |
| HA-060 | Database provisioning | MVP | implemented | [R-HA-014](../spec/ha-and-helm.md) (implemented) |
| HA-061 | Database connection settings | MVP | implemented | [R-HA-015](../spec/ha-and-helm.md) (implemented) |
| HA-062 | Connection pool sizing and the cluster-wide connection budget | MVP | implemented | [R-ARCH-019](../spec/architecture.md) (implemented), [R-HA-016](../spec/ha-and-helm.md) (implemented), [R-HA-058](../spec/ha-and-helm.md) (implemented) |
| HA-063 | TLS to the database | MVP | implemented | [R-HA-015](../spec/ha-and-helm.md) (implemented) |
| HA-064 | PostgreSQL high availability and failover | MVP | specified | [R-HA-009](../spec/ha-and-helm.md) |
| HA-065 | Database password rotation without downtime | P1 | implemented | [R-HA-017](../spec/ha-and-helm.md) (implemented) |
| HA-066 | Schema migration on upgrade | MVP | implemented | [R-ARCH-026](../spec/architecture.md) (implemented), [R-HA-018](../spec/ha-and-helm.md) (implemented), [R-HA-019](../spec/ha-and-helm.md) (implemented) |
| HA-067 | Hardware sizing tiers | MVP | specified | [R-HA-050](../spec/ha-and-helm.md) |
| HA-068 | Platform prerequisites | MVP | specified | [R-HA-049](../spec/ha-and-helm.md) |
| HA-074 | Preflight environment check | P2 | open | – |
| HA-075 | Deployment-wide outbound proxy for service traffic | P1 | open | – |
| HA-076 | Outbound destinations and air-gapped operation | P1 | open | – |
| HA-077 | Container installation without Kubernetes | P1 | open | – |
| HA-078 | Host installation from OS packages, archives and playbooks | P2 | open | – |
| HA-079 | Single-host evaluation installer | P2 | open | – |
| HA-069 | Rolling upgrade without downtime | MVP | specified | [R-ARCH-026](../spec/architecture.md) (implemented), [R-HA-019](../spec/ha-and-helm.md) (implemented), [R-HA-020](../spec/ha-and-helm.md) |
| HA-070 | Upgrade preparation | MVP | specified | [R-HA-021](../spec/ha-and-helm.md) |
| HA-071 | Uninstall and data cleanup | MVP | specified | [R-HA-048](../spec/ha-and-helm.md) |
| HA-072 | License installation and license gating of clustering | Out of scope | open | – |

</details>

## Operations, monitoring and configuration (OPS)

Catalog page: [operations-and-monitoring.md](../features/operations-and-monitoring.md) · specified by priority: MVP 17 of 17 · P1 20 of 23 · P2 7 of 34 · Later 0 of 1 · Out of scope 0 of 25

<details>
<summary>100 features: 44 specified, 56 open, 15 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| OPS-001 | Ping | MVP | implemented | [R-OPS-002](../spec/operations-and-observability.md) (implemented) |
| OPS-002 | Readiness probe | MVP | implemented | [R-ARCH-024](../spec/architecture.md) (implemented), [R-OPS-001](../spec/operations-and-observability.md) (implemented) |
| OPS-003 | Liveness probe | MVP | implemented | [R-ARCH-024](../spec/architecture.md) (implemented), [R-OPS-001](../spec/operations-and-observability.md) (implemented) |
| OPS-004 | Health-check tuning | P1 | implemented | [R-OPS-001](../spec/operations-and-observability.md) (implemented) |
| OPS-005 | Version information | MVP | implemented | [R-OPS-002](../spec/operations-and-observability.md) (implemented) |
| OPS-006 | Application headers for anonymous callers | P1 | implemented | [R-OPS-002](../spec/operations-and-observability.md) (implemented) |
| OPS-007 | System information report | P2 | implemented | [R-OPS-004](../spec/operations-and-observability.md) (implemented) |
| OPS-008 | Service status overview | P1 | specified | [R-API-038](../spec/api-and-ui.md) (implemented), [R-UI-038](../spec/api-and-ui.md), [R-OPS-003](../spec/operations-and-observability.md) (implemented) |
| OPS-009 | Cluster lock diagnostics | P2 | specified | [R-OPS-026](../spec/operations-and-observability.md) |
| OPS-010 | Outbound connection test | P2 | open | – |
| OPS-011 | Metrics endpoint | MVP | implemented | [R-OPS-006](../spec/operations-and-observability.md) (implemented) |
| OPS-012 | Core metric families | MVP | specified | [R-BAK-014](../spec/backup-and-restore.md), [R-EP-009](../spec/endpoints.md) (implemented), [R-OPS-007](../spec/operations-and-observability.md) (implemented), [R-OPS-008](../spec/operations-and-observability.md) |
| OPS-013 | Auxiliary service metric families | P1 | specified | [R-OPS-008](../spec/operations-and-observability.md) |
| OPS-014 | Federation metric families | Later | open | – |
| OPS-015 | Metrics log output | P2 | open | – |
| OPS-016 | Runtime management beans | Out of scope | open | – |
| OPS-017 | Log set and file naming | MVP | implemented | [R-OPS-009](../spec/operations-and-observability.md) (implemented) |
| OPS-018 | Service log record format | MVP | implemented | [R-OPS-009](../spec/operations-and-observability.md) (implemented) |
| OPS-019 | Request log | MVP | implemented | [R-EP-009](../spec/endpoints.md) (implemented), [R-OPS-010](../spec/operations-and-observability.md) (implemented) |
| OPS-020 | Outbound request log | MVP | specified | [R-OPS-011](../spec/operations-and-observability.md) |
| OPS-021 | Front router access log | P2 | open | – |
| OPS-022 | Security access log | MVP | specified | [R-AUTH-041](../spec/identity-and-authentication.md), [R-OPS-015](../spec/operations-and-observability.md), [R-OPS-016](../spec/operations-and-observability.md) |
| OPS-023 | Audit trail of security changes | MVP | specified | [R-API-019](../spec/api-and-ui.md), [R-API-039](../spec/api-and-ui.md), [R-UI-037](../spec/api-and-ui.md), [R-AUTH-041](../spec/identity-and-authentication.md), [R-OPS-015](../spec/operations-and-observability.md), [R-OPS-016](../spec/operations-and-observability.md), [R-OPS-017](../spec/operations-and-observability.md) |
| OPS-024 | Log rotation and archiving | P2 | open | – |
| OPS-025 | Log level configuration | P1 | implemented | [R-OPS-012](../spec/operations-and-observability.md) (implemented) |
| OPS-026 | Temporary logger level override | P1 | implemented | [R-OPS-012](../spec/operations-and-observability.md) (implemented) |
| OPS-027 | System log viewer | P2 | open | – |
| OPS-028 | Live log API | P2 | open | – |
| OPS-029 | Syslog forwarding | P2 | open | – |
| OPS-030 | Log analytics integrations | P2 | open | – |
| OPS-031 | Support bundle | P1 | specified | [R-OPS-027](../spec/operations-and-observability.md) |
| OPS-032 | Scheduled system backups | P2 | specified | [R-API-050](../spec/api-and-ui.md), [R-UI-048](../spec/api-and-ui.md), [R-BAK-001](../spec/backup-and-restore.md), [R-BAK-002](../spec/backup-and-restore.md), [R-BAK-003](../spec/backup-and-restore.md), [R-BAK-006](../spec/backup-and-restore.md), [R-BAK-008](../spec/backup-and-restore.md), [R-BAK-009](../spec/backup-and-restore.md), [R-BAK-010](../spec/backup-and-restore.md), [R-BAK-011](../spec/backup-and-restore.md), [R-BAK-012](../spec/backup-and-restore.md), [R-BAK-013](../spec/backup-and-restore.md), [R-BAK-014](../spec/backup-and-restore.md), [R-BAK-016](../spec/backup-and-restore.md), [R-BAK-017](../spec/backup-and-restore.md), [R-BAK-018](../spec/backup-and-restore.md), [R-BAK-029](../spec/backup-and-restore.md), [R-HA-060](../spec/ha-and-helm.md) |
| OPS-033 | Component backup and disaster recovery | P1 | specified | [R-BAK-001](../spec/backup-and-restore.md), [R-BAK-005](../spec/backup-and-restore.md), [R-BAK-006](../spec/backup-and-restore.md), [R-BAK-015](../spec/backup-and-restore.md), [R-BAK-019](../spec/backup-and-restore.md), [R-BAK-026](../spec/backup-and-restore.md), [R-BAK-028](../spec/backup-and-restore.md), [R-HA-060](../spec/ha-and-helm.md), [R-HA-023](../spec/ha-and-helm.md), [R-HA-024](../spec/ha-and-helm.md), [R-HA-025](../spec/ha-and-helm.md), [R-HA-026](../spec/ha-and-helm.md) |
| OPS-034 | System export | P2 | specified | [R-BAK-001](../spec/backup-and-restore.md), [R-BAK-003](../spec/backup-and-restore.md), [R-BAK-005](../spec/backup-and-restore.md) |
| OPS-035 | System import | P2 | specified | [R-BAK-012](../spec/backup-and-restore.md), [R-BAK-020](../spec/backup-and-restore.md), [R-BAK-021](../spec/backup-and-restore.md), [R-BAK-023](../spec/backup-and-restore.md), [R-BAK-029](../spec/backup-and-restore.md) |
| OPS-036 | Repository export | P2 | open | – |
| OPS-037 | Repository import | P2 | open | – |
| OPS-038 | Identity-service configuration export and import | P2 | open | – |
| OPS-039 | Instance migration by export and import | P2 | specified | [R-BAK-021](../spec/backup-and-restore.md), [R-BAK-023](../spec/backup-and-restore.md), [R-BAK-025](../spec/backup-and-restore.md) |
| OPS-040 | Node configuration file | MVP | implemented | [R-ARCH-020](../spec/architecture.md) (implemented), [R-OPS-028](../spec/operations-and-observability.md) (implemented) |
| OPS-041 | Advanced runtime properties | P2 | open | – |
| OPS-042 | Central configuration descriptor | P2 | open | – |
| OPS-043 | Declarative configuration patch | P1 | specified | [R-OPS-029](../spec/operations-and-observability.md), [R-OPS-030](../spec/operations-and-observability.md) |
| OPS-044 | Configuration patch sections | P1 | specified | [R-OPS-029](../spec/operations-and-observability.md) |
| OPS-045 | Deleting elements through a patch | P1 | specified | [R-OPS-029](../spec/operations-and-observability.md), [R-OPS-030](../spec/operations-and-observability.md) |
| OPS-046 | First-start bootstrap file | P1 | specified | [R-OPS-031](../spec/operations-and-observability.md) |
| OPS-047 | Authentication-provider bootstrap file | P2 | open | – |
| OPS-048 | Identity-service configuration documents | P2 | open | – |
| OPS-049 | Application integrations | P2 | open | – |
| OPS-050 | MIME type mapping | P1 | specified | [R-OPS-034](../spec/operations-and-observability.md) |
| OPS-051 | External base URL | MVP | specified | [R-EP-003](../spec/endpoints.md) (implemented), [R-AUTH-003](../spec/identity-and-authentication.md), [R-OPS-005](../spec/operations-and-observability.md) (implemented) |
| OPS-052 | Per-request base URL override | P1 | implemented | [R-EP-003](../spec/endpoints.md) (implemented), [R-OPS-005](../spec/operations-and-observability.md) (implemented) |
| OPS-053 | Server name, date format and help | P2 | open | – |
| OPS-054 | Logo | P2 | open | – |
| OPS-055 | System banner message | P1 | specified | [R-OPS-025](../spec/operations-and-observability.md) |
| OPS-056 | Login notice | P2 | open | – |
| OPS-057 | Repository-manager general settings | P1 | open | – |
| OPS-058 | Folder download limits | P2 | open | – |
| OPS-059 | Global replication blocking | P2 | open | – |
| OPS-060 | Mail server | P1 | specified | [R-OPS-033](../spec/operations-and-observability.md) |
| OPS-061 | Reverse proxy configuration generator | P2 | open | – |
| OPS-062 | Container registry addressing method | P1 | open | – |
| OPS-063 | Reverse proxy deployment rules | P1 | open | – |
| OPS-064 | Load protection | P2 | open | – |
| OPS-065 | Maintenance jobs and schedules | P1 | specified | [R-OPS-019](../spec/operations-and-observability.md) |
| OPS-066 | Storage quota | P1 | specified | [R-STOR-029](../spec/storage.md) |
| OPS-067 | Prune unreferenced data | P1 | specified | [R-STOR-026](../spec/storage.md) |
| OPS-068 | Compact the embedded database | Out of scope | open | – |
| OPS-069 | SHA-256 backfill job | P2 | open | – |
| OPS-070 | Package metadata re-index | P2 | specified | [R-OPS-022](../spec/operations-and-observability.md) |
| OPS-071 | Maven index calculation | P2 | open | – |
| OPS-072 | Upgrade compatibility policy | MVP | specified | [R-ARCH-025](../spec/architecture.md), [R-HA-021](../spec/ha-and-helm.md) |
| OPS-073 | Rolling upgrade of a cluster | MVP | specified | [R-ARCH-026](../spec/architecture.md) (implemented), [R-HA-020](../spec/ha-and-helm.md) |
| OPS-074 | Schema and data migrations on upgrade | MVP | specified | [R-ARCH-016](../spec/architecture.md) (implemented), [R-ARCH-026](../spec/architecture.md) (implemented), [R-HA-018](../spec/ha-and-helm.md) (implemented), [R-HA-022](../spec/ha-and-helm.md) |
| OPS-075 | Migration from another repository manager | P2 | open | – |
| OPS-076 | Signed release artifacts | P1 | specified | [R-HA-047](../spec/ha-and-helm.md) |
| OPS-077 | License-tier gating | Out of scope | open | – |
| OPS-078 | License management | Out of scope | open | – |
| OPS-079 | Portal accounts and sign-in | Out of scope | open | – |
| OPS-080 | Portal single sign-on | Out of scope | open | – |
| OPS-081 | DNS domain validation | Out of scope | open | – |
| OPS-082 | Usage explorer | Out of scope | open | – |
| OPS-083 | Detailed data-transfer logs | Out of scope | open | – |
| OPS-084 | Billing | Out of scope | open | – |
| OPS-085 | Hosted instance provisioning | Out of scope | open | – |
| OPS-086 | Portal users and roles | Out of scope | open | – |
| OPS-087 | Usage alert notifications | Out of scope | open | – |
| OPS-088 | Portal audit log | Out of scope | open | – |
| OPS-089 | Support contacts and support portal | Out of scope | open | – |
| OPS-090 | Subscription management | Out of scope | open | – |
| OPS-091 | Custom domain names for hosted instances | Out of scope | open | – |
| OPS-092 | Hosted log collection | Out of scope | open | – |
| OPS-093 | Hosted log streaming | Out of scope | open | – |
| OPS-094 | Hosted network access controls | Out of scope | open | – |
| OPS-095 | DNS routing policies | Out of scope | open | – |
| OPS-096 | Preview feature program | Out of scope | open | – |
| OPS-097 | Deployment registry and usage reporting | Out of scope | open | – |
| OPS-099 | Entitlement connector and offline entitlement registration | Out of scope | open | – |
| OPS-100 | Contributing-developer count | Out of scope | open | – |
| OPS-098 | Global read access to build information | P2 | open | – |

</details>

## Events and extensibility (EVT)

Catalog page: [events-and-extensibility.md](../features/events-and-extensibility.md) · specified by priority: MVP 1 of 1 · P1 24 of 24 · P2 1 of 12 · Later 0 of 43 · Out of scope 0 of 2

<details>
<summary>82 features: 26 specified, 56 open, 1 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| EVT-001 | Asynchronous event dispatch to webhooks | P1 | specified | [R-OPS-035](../spec/operations-and-observability.md), [R-OPS-042](../spec/operations-and-observability.md) |
| EVT-002 | Common event envelope | P1 | specified | [R-OPS-036](../spec/operations-and-observability.md) |
| EVT-003 | Actor context in events | P1 | specified | [R-OPS-036](../spec/operations-and-observability.md) |
| EVT-004 | Artifact events | P1 | specified | [R-OPS-037](../spec/operations-and-observability.md) |
| EVT-005 | Artifact property events | P1 | specified | [R-OPS-037](../spec/operations-and-observability.md) |
| EVT-006 | Container image tag events | P1 | specified | [R-OPS-037](../spec/operations-and-observability.md) |
| EVT-007 | Build events | P1 | specified | [R-OPS-037](../spec/operations-and-observability.md) |
| EVT-008 | User lockout event | P2 | open | – |
| EVT-009 | First-generation release set events | Later | open | – |
| EVT-010 | Release set creation events | Later | open | – |
| EVT-011 | Release set promotion events | Later | open | – |
| EVT-012 | Distribution events | Later | open | – |
| EVT-013 | Distribution destination events | Later | open | – |
| EVT-014 | Upstream curation events | Later | open | – |
| EVT-015 | Scan status events | Later | open | – |
| EVT-016 | Application lifecycle events | Later | open | – |
| EVT-017 | Event catalog discovery | P1 | specified | [R-OPS-037](../spec/operations-and-observability.md) |
| EVT-018 | Webhook subscription | P1 | specified | [R-OPS-038](../spec/operations-and-observability.md) |
| EVT-019 | Repository and path filter | P1 | specified | [R-OPS-038](../spec/operations-and-observability.md) |
| EVT-020 | Build filter | P1 | specified | [R-OPS-038](../spec/operations-and-observability.md) |
| EVT-021 | Release, environment and application filters | Later | open | – |
| EVT-022 | Project-scoped subscriptions | P2 | open | – |
| EVT-023 | Standard webhook handler | P1 | specified | [R-OPS-040](../spec/operations-and-observability.md) |
| EVT-024 | Payload signing and shared-secret header | P1 | specified | [R-OPS-040](../spec/operations-and-observability.md) |
| EVT-025 | Templated webhook handler | P1 | specified | [R-OPS-041](../spec/operations-and-observability.md) |
| EVT-026 | Payload template language | P1 | specified | [R-OPS-041](../spec/operations-and-observability.md) |
| EVT-027 | Handler secrets | P1 | specified | [R-OPS-041](../spec/operations-and-observability.md) |
| EVT-028 | Outbound proxy for delivery | P1 | specified | [R-OPS-042](../spec/operations-and-observability.md) |
| EVT-029 | Private-network target protection | P1 | specified | [R-OPS-042](../spec/operations-and-observability.md) |
| EVT-030 | Webhook management privileges | P1 | specified | [R-OPS-039](../spec/operations-and-observability.md) |
| EVT-031 | Test delivery | P1 | specified | [R-OPS-043](../spec/operations-and-observability.md) |
| EVT-032 | Delivery log | P1 | specified | [R-OPS-044](../spec/operations-and-observability.md) |
| EVT-033 | Delivery log storage and streaming | P1 | specified | [R-OPS-044](../spec/operations-and-observability.md) |
| EVT-034 | Webhook management UI | P1 | specified | [R-UI-039](../spec/api-and-ui.md) |
| EVT-035 | Recipes: triggering CI pipelines | P1 | specified | [R-OPS-041](../spec/operations-and-observability.md) |
| EVT-036 | Recipes: chat and incident tools | P2 | open | – |
| EVT-037 | Interactive chat application | Out of scope | open | – |
| EVT-081 | Retired issue-tracker applications | Out of scope | open | – |
| EVT-038 | Scripting hook service | Later | open | – |
| EVT-039 | Hook kinds | Later | open | – |
| EVT-040 | Hook definition | Later | open | – |
| EVT-041 | Hook trigger filter | Later | open | – |
| EVT-042 | Hook decision protocol | Later | open | – |
| EVT-043 | Upload and storage pre-operation hooks | Later | open | – |
| EVT-044 | Download-flow hooks | Later | open | – |
| EVT-045 | Post-operation notification hooks | Later | open | – |
| EVT-046 | Build-information hooks | Later | open | – |
| EVT-047 | Replication hooks | Later | open | – |
| EVT-048 | Repository configuration hooks | Later | open | – |
| EVT-049 | Token lifecycle hooks | Later | open | – |
| EVT-050 | Runtime workload hook | Later | open | – |
| EVT-051 | HTTP-triggered hooks | Later | open | – |
| EVT-052 | Scheduled hooks | Later | open | – |
| EVT-053 | Script runtime API | Later | open | – |
| EVT-054 | Per-hook state store | Later | open | – |
| EVT-055 | Execution limits and failure backoff | Later | open | – |
| EVT-056 | Hook testing | Later | open | – |
| EVT-057 | Execution history and rerun | Later | open | – |
| EVT-058 | Action discovery metadata | Later | open | – |
| EVT-059 | Hook versions | Later | open | – |
| EVT-060 | Hook service settings | Later | open | – |
| EVT-061 | Hook management UI | Later | open | – |
| EVT-062 | Local development tooling and declarative management | Later | open | – |
| EVT-063 | Extension scripts: deployment and reload | Later | open | – |
| EVT-064 | Extension point catalog | Later | open | – |
| EVT-065 | Execution identity and elevation | Later | open | – |
| EVT-066 | REST-invoked executions | Later | open | – |
| EVT-067 | Build promotion and staging strategies | Later | open | – |
| EVT-068 | Extension authentication realms | Later | open | – |
| EVT-069 | Extension inventory, source retrieval, logging and UI | Later | open | – |
| EVT-070 | Machine-readable API description | MVP | implemented | [R-API-002](../spec/api-and-ui.md) (implemented) |
| EVT-071 | Typed JSON configuration documents | P2 | open | – |
| EVT-072 | API deprecation lifecycle | P2 | specified | [R-API-001](../spec/api-and-ui.md) (implemented), [R-ARCH-025](../spec/architecture.md) |
| EVT-073 | GraphQL query gateway | P2 | open | – |
| EVT-074 | Schema introspection and interactive console | P2 | open | – |
| EVT-075 | Package metadata GraphQL domain | P2 | open | – |
| EVT-076 | Legacy package metadata GraphQL | Later | open | – |
| EVT-077 | Remote tool server for AI assistants | P2 | open | – |
| EVT-078 | Experimental tool gating and approval | P2 | open | – |
| EVT-079 | Tool catalog | P2 | open | – |
| EVT-082 | In-product AI assistant | Later | open | – |
| EVT-080 | Client SDKs | P2 | open | – |

</details>

## Distribution and release lifecycle (DIST)

Catalog page: [distribution-and-lifecycle.md](../features/distribution-and-lifecycle.md) · specified by priority: Later 0 of 69 · Out of scope 0 of 1

<details>
<summary>70 features: 0 specified, 70 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| DIST-001 | Release set model | Later | open | – |
| DIST-002 | Release set storage repositories | Later | open | – |
| DIST-003 | Create a release set version from sources | Later | open | – |
| DIST-004 | Release set signing keys | Later | open | – |
| DIST-005 | Creation validation, conflict resolution and dry run | Later | open | – |
| DIST-006 | Draft release set versions | Later | open | – |
| DIST-007 | Derive a version with include and exclude filters | Later | open | – |
| DIST-008 | Tags on release set versions | Later | open | – |
| DIST-009 | Properties on release set versions | Later | open | – |
| DIST-010 | Release set lists, version details and timeline | Later | open | – |
| DIST-011 | Find release set versions containing an artifact | Later | open | – |
| DIST-012 | Independent verification of a release set version | Later | open | – |
| DIST-013 | Delete and restore release set versions | Later | open | – |
| DIST-014 | Release set permissions | Later | open | – |
| DIST-015 | Release set background settings and metrics | Later | open | – |
| DIST-016 | Release sets across federated sites | Later | open | – |
| DIST-017 | Lifecycle stages | Later | open | – |
| DIST-018 | Assign repositories to stages | Later | open | – |
| DIST-019 | Lifecycle definition | Later | open | – |
| DIST-020 | Promote a release set version | Later | open | – |
| DIST-021 | Promotion rollback | Later | open | – |
| DIST-022 | Promotion records and queries | Later | open | – |
| DIST-023 | Security scan gate for release sets | Later | open | – |
| DIST-024 | Distribution topology and edge instances | Later | open | – |
| DIST-025 | Trust between source and edge instances | Later | open | – |
| DIST-026 | Signing key propagation to targets | Later | open | – |
| DIST-027 | Distribute a release set version | Later | open | – |
| DIST-028 | Target path mapping and repository auto-creation | Later | open | – |
| DIST-029 | Distribution tracking and monitoring | Later | open | – |
| DIST-030 | Abort a distribution | Later | open | – |
| DIST-031 | Distribution priority queue | Later | open | – |
| DIST-032 | Remote deletion from targets | Later | open | – |
| DIST-033 | Reporting of deletions made on targets | Later | open | – |
| DIST-034 | Received release sets on targets | Later | open | – |
| DIST-035 | Destination permissions | Later | open | – |
| DIST-036 | Distribution task maintenance | Later | open | – |
| DIST-037 | Distribution service operations | Later | open | – |
| DIST-038 | Legacy release sets assembled by the distribution service | Later | open | – |
| DIST-039 | One-step create, sign and distribute | Later | open | – |
| DIST-040 | Retention of received release sets on targets | Later | open | – |
| DIST-041 | Hosted-service edges and pooled edge licenses | Out of scope | open | – |
| DIST-068 | Edge instance capability profile and setup | Later | open | – |
| DIST-042 | Export a release set version as an archive | Later | open | – |
| DIST-043 | Import a release set archive | Later | open | – |
| DIST-044 | Evidence model | Later | open | – |
| DIST-045 | Evidence file format | Later | open | – |
| DIST-046 | Evidence signing keys and server-side verification | Later | open | – |
| DIST-047 | Deploy evidence to a subject | Later | open | – |
| DIST-048 | Server-assisted evidence preparation | Later | open | – |
| DIST-049 | Evidence on entities | Later | open | – |
| DIST-050 | Evidence attachments | Later | open | – |
| DIST-051 | Internally generated evidence | Later | open | – |
| DIST-052 | View, search and download evidence | Later | open | – |
| DIST-053 | Evidence lifecycle | Later | open | – |
| DIST-066 | Client-side evidence verification | Later | open | – |
| DIST-069 | Pull-request merge evidence collected at promotion | Later | open | – |
| DIST-070 | Evidence from CI attestations and third-party tools | Later | open | – |
| DIST-054 | Applications | Later | open | – |
| DIST-055 | Package binding | Later | open | – |
| DIST-056 | Application versions | Later | open | – |
| DIST-057 | Application promotion, release and rollback | Later | open | – |
| DIST-058 | Lifecycle policies, rules and templates | Later | open | – |
| DIST-059 | Gate evaluations | Later | open | – |
| DIST-060 | Waivers | Later | open | – |
| DIST-061 | Compliance frameworks | Later | open | – |
| DIST-062 | Governance activity log | Later | open | – |
| DIST-063 | Governance insights | Later | open | – |
| DIST-064 | External governance integrations | Later | open | – |
| DIST-065 | Distribution and air-gapped transfer of application versions | Later | open | – |
| DIST-067 | Runtime status of released application versions | Later | open | – |

</details>

## Security scanning and curation (SEC)

Catalog page: [security-scanning-and-curation.md](../features/security-scanning-and-curation.md) · specified by priority: P1 0 of 22 · P2 0 of 18 · Later 0 of 56 · Out of scope 0 of 2

<details>
<summary>98 features: 0 specified, 98 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| SEC-001 | Package risk data source | P1 | open | – |
| SEC-002 | Online synchronization of the vulnerability database | P1 | open | – |
| SEC-003 | Offline synchronization of the vulnerability database | P2 | open | – |
| SEC-004 | Malicious package intelligence | P1 | open | – |
| SEC-005 | Package labels | P2 | open | – |
| SEC-006 | Custom licenses | P2 | open | – |
| SEC-090 | Package intelligence browser | P2 | open | – |
| SEC-007 | Upstream curation service | P1 | open | – |
| SEC-008 | Curated remote repositories | P1 | open | – |
| SEC-009 | Curation policy | P1 | open | – |
| SEC-010 | Curation policy scope | P1 | open | – |
| SEC-011 | Curation condition model | P1 | open | – |
| SEC-012 | Malicious package condition | P1 | open | – |
| SEC-013 | Vulnerability conditions | P1 | open | – |
| SEC-014 | Package age conditions | P1 | open | – |
| SEC-015 | Single-version and end-of-life conditions | P2 | open | – |
| SEC-016 | License conditions | P2 | open | – |
| SEC-017 | Project health and image provenance conditions | P2 | open | – |
| SEC-018 | Label list conditions | P2 | open | – |
| SEC-019 | Package integrity condition | P2 | open | – |
| SEC-020 | Custom conditions | P2 | open | – |
| SEC-021 | Block and dry-run actions | P1 | open | – |
| SEC-022 | Pending-data handling | P1 | open | – |
| SEC-023 | Enforcement on cached packages | P1 | open | – |
| SEC-024 | Pass-through evaluation mode | P2 | open | – |
| SEC-025 | Compliant version fallback | P1 | open | – |
| SEC-026 | Compliant version fallback per client | P1 | open | – |
| SEC-027 | Curated remote repositories inside virtual repositories | P1 | open | – |
| SEC-028 | Policy waivers | P1 | open | – |
| SEC-029 | Waiver requests and approval | P2 | open | – |
| SEC-030 | Time-limited waivers | P2 | open | – |
| SEC-031 | Waivers for pending and fully blocked packages | P2 | open | – |
| SEC-032 | Curation audit log | P1 | open | – |
| SEC-033 | Compliant version fallback audit | P2 | open | – |
| SEC-034 | Curation notifications | P2 | open | – |
| SEC-035 | Curation roles | P1 | open | – |
| SEC-036 | Effective policies of a repository | P1 | open | – |
| SEC-037 | Curation management operations | P1 | open | – |
| SEC-038 | Dependency pre-check | P2 | open | – |
| SEC-039 | On-demand evaluation of uncovered packages | Later | open | – |
| SEC-040 | Curation policy sharing across sites | Later | open | – |
| SEC-041 | Redirect endpoint for intercepted public registry traffic | P2 | open | – |
| SEC-042 | Indexing scope | Later | open | – |
| SEC-043 | Scanning on upload and on cache | Later | open | – |
| SEC-044 | On-demand indexing and reindexing | Later | open | – |
| SEC-045 | Scan data retention | Later | open | – |
| SEC-046 | File-name exclusion from indexing | Later | open | – |
| SEC-047 | Scan status | Later | open | – |
| SEC-048 | Continuous re-evaluation | Later | open | – |
| SEC-049 | Scan results view | Later | open | – |
| SEC-050 | Scan policy model | Later | open | – |
| SEC-051 | Security rule criteria | Later | open | – |
| SEC-052 | License rule criteria | Later | open | – |
| SEC-053 | Operational risk rules and scoring | Later | open | – |
| SEC-054 | Scan policy actions | Later | open | – |
| SEC-055 | Scan scopes | Later | open | – |
| SEC-056 | Apply a scan scope to existing content | Later | open | – |
| SEC-057 | Violations | Later | open | – |
| SEC-058 | Ignore rules | Later | open | – |
| SEC-059 | Violation notifications and webhooks | Later | open | – |
| SEC-060 | Issue tracker integration | Later | open | – |
| SEC-097 | Export of findings to external security dashboards | Later | open | – |
| SEC-061 | Block downloads on violation | Later | open | – |
| SEC-062 | Block downloads of not-yet-scanned artifacts | Later | open | – |
| SEC-063 | Download status check | Later | open | – |
| SEC-064 | Global blocking overrides | Later | open | – |
| SEC-065 | SBOM generation | Later | open | – |
| SEC-066 | SBOM export | Later | open | – |
| SEC-067 | SBOM import and aggregation | Later | open | – |
| SEC-068 | Dependency graph and transitive SBOM | Later | open | – |
| SEC-091 | Base image detection | Later | open | – |
| SEC-069 | License detection and management | Later | open | – |
| SEC-070 | License conclusion by priority | Later | open | – |
| SEC-071 | License attribution report | Later | open | – |
| SEC-072 | Scan result export per resource | Later | open | – |
| SEC-073 | Aggregated security reports | Later | open | – |
| SEC-074 | Affected-resource search | Later | open | – |
| SEC-075 | Scanned-resource search | Later | open | – |
| SEC-076 | Summaries, comparison and differences | Later | open | – |
| SEC-077 | Remediation suggestions | Later | open | – |
| SEC-078 | Custom issues | Later | open | – |
| SEC-079 | Scanner audit log | Later | open | – |
| SEC-080 | Scanner operational settings | Later | open | – |
| SEC-092 | Security overview dashboard | Later | open | – |
| SEC-094 | Threat intelligence feed with impact check | Later | open | – |
| SEC-095 | Scanner processing failures and retries | Later | open | – |
| SEC-096 | Scanner system status | Later | open | – |
| SEC-081 | Vulnerability applicability analysis | Later | open | – |
| SEC-082 | Secret detection in stored artifacts | Later | open | – |
| SEC-083 | Misconfiguration and first-party code analysis | Later | open | – |
| SEC-084 | Runtime-aware filtering and runtime monitoring | Later | open | – |
| SEC-085 | Source-code scanning outside the registry | Later | open | – |
| SEC-086 | Patched-package substitution at resolution time | Later | open | – |
| SEC-087 | Malicious model and agent-skill detection | Later | open | – |
| SEC-093 | Snippet detection | Later | open | – |
| SEC-098 | CI workflow injection detection | Later | open | – |
| SEC-088 | Entitlement checks | Out of scope | open | – |
| SEC-089 | Data-provider malicious-package digest | Out of scope | open | – |

</details>

## UI inventory (UI)

Catalog page: [ui-inventory.md](../features/ui-inventory.md) · specified by priority: MVP 30 of 30 · P1 17 of 17 · P2 0 of 8 · Later 0 of 3 · Out of scope 0 of 3

<details>
<summary>61 features: 47 specified, 14 open, 1 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| UI-001 | Application shell and header | MVP | specified | [R-UI-002](../spec/api-and-ui.md) |
| UI-002 | Areas and navigation map | MVP | specified | [R-UI-003](../spec/api-and-ui.md) |
| UI-003 | Project scope selector | P1 | specified | [R-UI-041](../spec/api-and-ui.md) |
| UI-004 | User menu | MVP | specified | [R-UI-002](../spec/api-and-ui.md) |
| UI-005 | Permission-driven rendering | MVP | specified | [R-UI-005](../spec/api-and-ui.md), [R-AUTHZ-023](../spec/authorization.md) |
| UI-006 | Login screen | MVP | specified | [R-UI-004](../spec/api-and-ui.md) |
| UI-007 | First-run onboarding wizard | P2 | open | – |
| UI-058 | Supported browsers | MVP | implemented | [R-UI-001](../spec/api-and-ui.md) (implemented) |
| UI-008 | Packages list screen | MVP | specified | [R-UI-006](../spec/api-and-ui.md) |
| UI-009 | Package details screen | MVP | specified | [R-UI-007](../spec/api-and-ui.md), [R-UI-008](../spec/api-and-ui.md) |
| UI-010 | Install command panel | MVP | specified | [R-UI-009](../spec/api-and-ui.md) |
| UI-011 | Artifact browser screen | MVP | specified | [R-UI-010](../spec/api-and-ui.md) |
| UI-012 | Item actions and their required permissions | MVP | specified | [R-UI-011](../spec/api-and-ui.md) |
| UI-013 | Deploy dialog | P1 | specified | [R-UI-012](../spec/api-and-ui.md) |
| UI-014 | Copy and move dialog | P1 | specified | [R-UI-013](../spec/api-and-ui.md) |
| UI-015 | Folder download dialog | P2 | open | – |
| UI-016 | Delete item action | MVP | specified | [R-UI-011](../spec/api-and-ui.md) |
| UI-017 | Delete-versions dialog | P2 | open | – |
| UI-018 | Trash can in the tree | P1 | specified | [R-UI-014](../spec/api-and-ui.md) |
| UI-019 | Global search box | P1 | specified | [R-UI-015](../spec/api-and-ui.md) |
| UI-020 | Artifact search on the browser page | P1 | specified | [R-UI-015](../spec/api-and-ui.md) |
| UI-021 | Client setup dialog | MVP | specified | [R-UI-016](../spec/api-and-ui.md) |
| UI-022 | Credential step and token embedding | MVP | specified | [R-UI-016](../spec/api-and-ui.md) |
| UI-023 | Snippet templating | MVP | specified | [R-API-030](../spec/api-and-ui.md), [R-UI-016](../spec/api-and-ui.md) |
| UI-024 | npm client instructions | MVP | specified | [R-UI-017](../spec/api-and-ui.md), [R-NPM-041](../spec/formats/npm.md) |
| UI-025 | NuGet client instructions | MVP | specified | [R-UI-018](../spec/api-and-ui.md), [R-NUGET-026](../spec/formats/nuget.md) |
| UI-026 | Instructions for other formats | P1 | specified | [R-UI-019](../spec/api-and-ui.md) |
| UI-027 | Federated repository instructions | Later | open | – |
| UI-028 | Builds list | P1 | specified | [R-UI-040](../spec/api-and-ui.md) |
| UI-029 | Build run details | P1 | specified | [R-UI-040](../spec/api-and-ui.md) |
| UI-030 | Repositories screen | MVP | specified | [R-UI-020](../spec/api-and-ui.md) |
| UI-031 | Create and edit repository form | MVP | specified | [R-UI-021](../spec/api-and-ui.md) |
| UI-032 | Virtual repository member selector | MVP | specified | [R-UI-022](../spec/api-and-ui.md) |
| UI-033 | Quick repository setup wizard | P1 | specified | [R-UI-023](../spec/api-and-ui.md) |
| UI-034 | Property sets screen | P2 | open | – |
| UI-035 | Users screen | MVP | specified | [R-UI-024](../spec/api-and-ui.md) |
| UI-036 | User editor | MVP | specified | [R-UI-024](../spec/api-and-ui.md) |
| UI-037 | Groups screen and group editor | MVP | specified | [R-UI-025](../spec/api-and-ui.md) |
| UI-038 | Anonymous user page | P1 | specified | [R-UI-032](../spec/api-and-ui.md) |
| UI-039 | Permissions screen | MVP | specified | [R-UI-027](../spec/api-and-ui.md) |
| UI-040 | Permission policy editor | MVP | specified | [R-UI-027](../spec/api-and-ui.md) |
| UI-041 | Access tokens screen | MVP | specified | [R-UI-030](../spec/api-and-ui.md) |
| UI-042 | User management settings screen | P1 | specified | [R-UI-032](../spec/api-and-ui.md) |
| UI-043 | General security settings screen | MVP | specified | [R-UI-031](../spec/api-and-ui.md) |
| UI-044 | General settings screen | MVP | specified | [R-UI-033](../spec/api-and-ui.md) |
| UI-045 | General repository settings screen | P1 | specified | [R-UI-034](../spec/api-and-ui.md) |
| UI-046 | Webhooks screen | P1 | specified | [R-UI-039](../spec/api-and-ui.md) |
| UI-047 | Webhook editor | P1 | specified | [R-UI-039](../spec/api-and-ui.md) |
| UI-048 | Webhook troubleshooting tab | P2 | open | – |
| UI-049 | CI identity integration screen | MVP | specified | [R-UI-035](../spec/api-and-ui.md), [R-CI-027](../spec/gitlab-ci-oidc.md) |
| UI-050 | Projects overview | P1 | specified | [R-UI-041](../spec/api-and-ui.md) |
| UI-051 | Create and edit project dialog | P1 | specified | [R-UI-041](../spec/api-and-ui.md) |
| UI-052 | Project storage quota indicators | P2 | open | – |
| UI-056 | Project members screen | P2 | open | – |
| UI-057 | Global roles and project roles screens | P2 | open | – |
| UI-053 | Profile page | MVP | specified | [R-UI-029](../spec/api-and-ui.md) |
| UI-054 | Federation status dashboard | Later | open | – |
| UI-061 | Federation monitoring screens of the older embedded synchronization mode | Later | open | – |
| UI-055 | Hosted account portal dashboard | Out of scope | open | – |
| UI-059 | Site launcher across hosted sites and subscriptions | Out of scope | open | – |
| UI-060 | In-product AI assistant panel | Out of scope | open | – |

</details>

## Content API operation catalog (API)

Catalog page: [api-content-operations.md](../features/api-content-operations.md) · specified by priority: MVP 11 of 11 · P1 56 of 56 · P2 0 of 48 · Later 0 of 48 · Out of scope 0 of 3

<details>
<summary>166 features: 67 specified, 99 open, 1 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| API-001 | Common operation conventions | MVP | specified | [R-API-001](../spec/api-and-ui.md) (implemented), [R-API-003](../spec/api-and-ui.md), [R-API-004](../spec/api-and-ui.md) (implemented), [R-API-005](../spec/api-and-ui.md), [R-API-009](../spec/api-and-ui.md) (implemented), [R-API-011](../spec/api-and-ui.md) |
| API-002 | Start, monitor and stop unreferenced-binary pruning | P1 | specified | [R-API-040](../spec/api-and-ui.md) |
| API-003 | Item information (file or folder) | MVP | specified | [R-API-025](../spec/api-and-ui.md) |
| API-004 | File list | P1 | specified | [R-API-025](../spec/api-and-ui.md) |
| API-005 | Download statistics of a file | P1 | specified | [R-API-025](../spec/api-and-ui.md) |
| API-006 | Last-modified item under a path | P2 | open | – |
| API-007 | Read item properties | P1 | specified | [R-API-026](../spec/api-and-ui.md) |
| API-008 | Effective permissions on an item | P1 | specified | [R-API-034](../spec/api-and-ui.md) |
| API-009 | Set item properties | P1 | specified | [R-API-026](../spec/api-and-ui.md) |
| API-010 | Delete item properties | P1 | specified | [R-API-026](../spec/api-and-ui.md) |
| API-011 | Update item properties (partial) | P1 | specified | [R-API-026](../spec/api-and-ui.md) |
| API-012 | Compute SHA-256 for legacy items | P2 | open | – |
| API-013 | Deploy a file or create a folder | MVP | specified | [R-API-025](../spec/api-and-ui.md) |
| API-014 | Deploy by checksum | P1 | specified | [R-API-026](../spec/api-and-ui.md) |
| API-015 | Deploy and extract an archive | P2 | open | – |
| API-016 | Download a file | MVP | specified | [R-API-025](../spec/api-and-ui.md) |
| API-017 | Download a single entry from an archive | P2 | open | – |
| API-018 | Download all artifacts of a build as an archive | P2 | open | – |
| API-019 | Download a folder or repository as an archive | P2 | open | – |
| API-020 | Server-side download (cache warm-up) | P2 | open | – |
| API-021 | RubyGems version list | P2 | open | – |
| API-022 | Delete a file or folder | MVP | specified | [R-API-025](../spec/api-and-ui.md) |
| API-023 | Copy a file or folder | P1 | specified | [R-API-027](../spec/api-and-ui.md) |
| API-024 | Move a file or folder | P1 | specified | [R-API-027](../spec/api-and-ui.md) |
| API-025 | Create a signed download URL | P2 | open | – |
| API-026 | Rotate the signed-URL key | P2 | open | – |
| API-027 | Push artifacts to a discontinued external distribution service | Out of scope | open | – |
| API-028 | Invalidate cached remote metadata | P1 | specified | [R-API-024](../spec/api-and-ui.md), [R-OPS-021](../spec/operations-and-observability.md) |
| API-029 | List background tasks | P2 | open | – |
| API-030 | Rebalance redundant storage | P2 | open | – |
| API-031 | Storage summary | P1 | specified | [R-API-040](../spec/api-and-ui.md), [R-OPS-023](../spec/operations-and-observability.md) |
| API-032 | Recalculate the storage summary | P1 | specified | [R-API-040](../spec/api-and-ui.md), [R-OPS-023](../spec/operations-and-observability.md) |
| API-033 | Application metrics in OpenMetrics format | P1 | implemented | [R-API-038](../spec/api-and-ui.md) (implemented), [R-OPS-006](../spec/operations-and-observability.md) (implemented) |
| API-034 | Puppet module and release listing | P2 | open | – |
| API-035 | Source-control repository listing (tags, branches, refs) | P2 | open | – |
| API-036 | Source-control snapshot archives | P2 | open | – |
| API-037 | Single file from a source-control tag or branch | P2 | open | – |
| API-038 | Recalculate the index of other formats | P2 | open | – |
| API-039 | Recalculate RPM (YUM) metadata | P2 | open | – |
| API-040 | Recalculate NuGet metadata | P1 | specified | [R-API-024](../spec/api-and-ui.md), [R-NUGET-025](../spec/formats/nuget.md) |
| API-041 | Recalculate npm indexes | P1 | specified | [R-API-024](../spec/api-and-ui.md), [R-OPS-022](../spec/operations-and-observability.md) |
| API-042 | Calculate a Maven index | P2 | open | – |
| API-043 | Calculate Maven metadata | P1 | specified | [R-API-024](../spec/api-and-ui.md) |
| API-044 | Generate a Maven POM | P2 | open | – |
| API-045 | Recalculate PyPI indexes | P1 | specified | [R-API-024](../spec/api-and-ui.md) |
| API-046 | PyPI JSON project metadata | P1 | specified | [R-API-036](../spec/api-and-ui.md) |
| API-047 | Recalculate Debian metadata | P2 | open | – |
| API-048 | Recalculate coordinates of cached Debian packages | P2 | open | – |
| API-049 | Create a Debian snapshot | P2 | open | – |
| API-050 | Recalculate a Helm chart index | P1 | specified | [R-API-024](../spec/api-and-ui.md) |
| API-051 | Relocate misplaced CRAN archives | P2 | open | – |
| API-052 | Create a repository | MVP | specified | [R-API-022](../spec/api-and-ui.md), [R-REPO-009](../spec/repositories-and-proxy.md) |
| API-053 | Create repositories in a batch | P1 | specified | [R-API-023](../spec/api-and-ui.md), [R-REPO-014](../spec/repositories-and-proxy.md) |
| API-054 | Update a repository configuration | MVP | specified | [R-API-022](../spec/api-and-ui.md), [R-REPO-011](../spec/repositories-and-proxy.md) |
| API-055 | Update repositories in a batch | P1 | specified | [R-API-023](../spec/api-and-ui.md), [R-REPO-014](../spec/repositories-and-proxy.md) |
| API-056 | Check whether repositories exist | P2 | open | – |
| API-057 | Get a repository configuration | MVP | specified | [R-API-022](../spec/api-and-ui.md), [R-REPO-010](../spec/repositories-and-proxy.md) |
| API-058 | Get a format-filtered repository configuration | P1 | specified | [R-API-022](../spec/api-and-ui.md), [R-REPO-010](../spec/repositories-and-proxy.md) |
| API-059 | Get repository configurations by key | P1 | specified | [R-API-022](../spec/api-and-ui.md), [R-REPO-014](../spec/repositories-and-proxy.md) |
| API-060 | Get all repository configurations | P1 | specified | [R-API-022](../spec/api-and-ui.md), [R-REPO-012](../spec/repositories-and-proxy.md) |
| API-061 | List repositories | MVP | specified | [R-API-022](../spec/api-and-ui.md), [R-REPO-012](../spec/repositories-and-proxy.md) |
| API-062 | Shared remote repository configuration (deprecated) | P2 | open | – |
| API-063 | Delete a repository | MVP | specified | [R-API-015](../spec/api-and-ui.md), [R-API-022](../spec/api-and-ui.md), [R-REPO-013](../spec/repositories-and-proxy.md) |
| API-064 | Delete repositories in a batch | P1 | specified | [R-API-023](../spec/api-and-ui.md), [R-REPO-014](../spec/repositories-and-proxy.md) |
| API-065 | List repositories connectable from a project | P2 | open | – |
| API-066 | List project-scoped consumers of a repository | P2 | open | – |
| API-067 | Convert a local repository to federated | Later | open | – |
| API-068 | Convert federated repositories back to local | Later | open | – |
| API-069 | List federated repositories with configuration drift | Later | open | – |
| API-070 | Recover exhausted federation queues | Later | open | – |
| API-071 | Reconnect to the federation service | Out of scope | open | – |
| API-072 | Full synchronization of a federated repository | Later | open | – |
| API-073 | Propagate creation time during full sync | Later | open | – |
| API-074 | Resynchronize a member's configuration | Later | open | – |
| API-075 | Remove a member from all federations | Later | open | – |
| API-076 | Federation connection details of a repository | Later | open | – |
| API-077 | Federation state list and summary | Later | open | – |
| API-078 | Prioritize a federated repository | Later | open | – |
| API-079 | Legacy federation status (built-in synchronization) | Later | open | – |
| API-080 | Failed binary transfer tasks | Later | open | – |
| API-081 | Instance trust pairing | Later | open | – |
| API-082 | Replication configuration document | P2 | open | – |
| API-083 | Get, set, update and delete a repository's replication | P2 | open | – |
| API-084 | Replication status of a path | P2 | open | – |
| API-085 | Run replication now | P2 | open | – |
| API-086 | Create, replace or update multi-push replication | P2 | open | – |
| API-087 | Enable or disable replications in bulk | P2 | open | – |
| API-088 | Event-based pull replication channels | P2 | open | – |
| API-089 | Block and unblock replication globally | P2 | open | – |
| API-090 | Checksum (metadata-only) replication | P2 | open | – |
| API-091 | Query language execution | P2 | open | – |
| API-092 | Name search | P1 | specified | [R-API-028](../spec/api-and-ui.md) |
| API-093 | Archive entry search (deprecated) | Later | open | – |
| API-094 | Maven coordinate search | P1 | specified | [R-API-029](../spec/api-and-ui.md) |
| API-095 | Property search | P1 | specified | [R-API-028](../spec/api-and-ui.md) |
| API-096 | Checksum search | P1 | specified | [R-API-028](../spec/api-and-ui.md) |
| API-097 | Bad or missing client checksum search | P2 | open | – |
| API-098 | Not-downloaded-since search | P1 | specified | [R-API-028](../spec/api-and-ui.md) |
| API-099 | Date-range search on selected date fields | P1 | specified | [R-API-028](../spec/api-and-ui.md) |
| API-100 | Created-in-date-range search | P1 | specified | [R-API-028](../spec/api-and-ui.md) |
| API-101 | Path pattern search | P1 | specified | [R-API-028](../spec/api-and-ui.md) |
| API-102 | Builds that use an artifact as dependency | P2 | open | – |
| API-103 | License search | Later | open | – |
| API-104 | Version list search | P2 | open | – |
| API-105 | Latest version by layout | P2 | open | – |
| API-106 | Latest version by properties | P2 | open | – |
| API-107 | Build artifacts search | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-108 | Parent manifest lists of a container image | P2 | open | – |
| API-109 | Container image catalog | P1 | specified | [R-API-029](../spec/api-and-ui.md) |
| API-110 | Container tag listing | P1 | specified | [R-API-029](../spec/api-and-ui.md) |
| API-111 | Trash can operations | P1 | specified | [R-API-027](../spec/api-and-ui.md) |
| API-112 | Remove redundant trash directory records | P2 | open | – |
| API-113 | Run garbage collection | MVP | specified | [R-API-040](../spec/api-and-ui.md), [R-OPS-020](../spec/operations-and-observability.md) |
| API-114 | List and read stored release sets | Later | open | – |
| API-115 | Delete a source release-set version | Later | open | – |
| API-116 | Import a release-set version from an archive | Later | open | – |
| API-117 | Retention of first-generation release sets | Later | open | – |
| API-118 | Distribution service metrics | Later | open | – |
| API-119 | Distribution target rules | Later | open | – |
| API-120 | Read release sets in the distribution service | Later | open | – |
| API-121 | Create, update and delete release sets | Later | open | – |
| API-122 | Sign a release-set version | Later | open | – |
| API-123 | Trigger a vulnerability scan of a release-set version | Later | open | – |
| API-124 | First-generation distribution status | Later | open | – |
| API-125 | Distribute a first-generation release-set version | Later | open | – |
| API-126 | Delete a first-generation release-set version from edge instances | Later | open | – |
| API-127 | Abort a first-generation distribution | Later | open | – |
| API-128 | Create, sign and distribute in one request | Later | open | – |
| API-129 | List registered edge instances | Later | open | – |
| API-130 | Distribute a second-generation release-set version | Later | open | – |
| API-131 | Abort, remote-delete and track second-generation distributions | Later | open | – |
| API-132 | Change the priority of a running distribution | Later | open | – |
| API-133 | Export and import release sets for disconnected sites | Later | open | – |
| API-134 | Release-set signing key | Later | open | – |
| API-135 | GPG keys for artifact signatures | Later | open | – |
| API-136 | Distribution service tokens | Out of scope | open | – |
| API-137 | Destination permissions | Later | open | – |
| API-138 | Stuck distributions and emergency actions | Later | open | – |
| API-139 | Redistribute recent release sets to one edge instance | Later | open | – |
| API-140 | Distribution service health and settings | Later | open | – |
| API-141 | Execute a named plugin execution | Later | open | – |
| API-142 | Download plugin source | Later | open | – |
| API-143 | List plugin executions and staging strategies | Later | open | – |
| API-144 | Get a build staging strategy | Later | open | – |
| API-145 | Execute a plugin-defined build promotion | Later | open | – |
| API-146 | Reload plugins | Later | open | – |
| API-147 | Tail server logs over the API | P2 | open | – |
| API-148 | Webhook subscription document | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-038](../spec/operations-and-observability.md) |
| API-149 | List webhook subscriptions | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-038](../spec/operations-and-observability.md) |
| API-150 | Create a webhook subscription | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-038](../spec/operations-and-observability.md) |
| API-151 | Get a webhook subscription | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-038](../spec/operations-and-observability.md) |
| API-152 | Replace a webhook subscription | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-038](../spec/operations-and-observability.md) |
| API-153 | Delete a webhook subscription | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-038](../spec/operations-and-observability.md) |
| API-154 | Test a webhook subscription | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-043](../spec/operations-and-observability.md) |
| API-155 | Stream or fetch webhook delivery records | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-OPS-044](../spec/operations-and-observability.md) |
| API-156 | Build information document | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-157 | List builds | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-158 | Upload build information | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-159 | List the runs of a build | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-160 | Delete build runs | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-161 | Append modules to a build run | P2 | open | – |
| API-162 | Get a build run | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-163 | Promote a build run | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-164 | Rename a build | P2 | open | – |
| API-165 | Set build retention | P1 | specified | [R-API-042](../spec/api-and-ui.md) |
| API-166 | Promote a container image | P1 | specified | [R-API-036](../spec/api-and-ui.md) |

</details>

## Operation catalog: administration API (ADMAPI)

Catalog page: [api-admin-operations.md](../features/api-admin-operations.md) · specified by priority: MVP 34 of 34 · P1 20 of 23 · P2 0 of 23 · Later 0 of 28 · Out of scope 0 of 6

<details>
<summary>114 features: 54 specified, 60 open, 2 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| ADMAPI-001 | Callers and privilege tiers | MVP | specified | [R-API-005](../spec/api-and-ui.md), [R-API-008](../spec/api-and-ui.md) (implemented), [R-AUTHZ-010](../spec/authorization.md), [R-AUTHZ-022](../spec/authorization.md) |
| ADMAPI-002 | Status codes and response bodies | MVP | specified | [R-API-001](../spec/api-and-ui.md) (implemented), [R-API-004](../spec/api-and-ui.md) (implemented), [R-API-009](../spec/api-and-ui.md) (implemented), [R-API-015](../spec/api-and-ui.md), [R-AUTHZ-016](../spec/authorization.md) |
| ADMAPI-003 | Paging styles | MVP | specified | [R-API-010](../spec/api-and-ui.md) |
| ADMAPI-004 | Create user | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-016](../spec/identity-and-authentication.md) |
| ADMAPI-005 | Get user | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-016](../spec/identity-and-authentication.md) |
| ADMAPI-006 | List users | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-016](../spec/identity-and-authentication.md) |
| ADMAPI-007 | Update user (partial) | MVP | specified | [R-API-013](../spec/api-and-ui.md), [R-API-031](../spec/api-and-ui.md), [R-AUTH-016](../spec/identity-and-authentication.md) |
| ADMAPI-008 | Delete user | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-016](../spec/identity-and-authentication.md) |
| ADMAPI-009 | Add or remove groups of a user | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-016](../spec/identity-and-authentication.md) |
| ADMAPI-010 | Change a user's password | P2 | open | – |
| ADMAPI-011 | Password expiry operations | P2 | open | – |
| ADMAPI-012 | Unlock a user | P1 | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-038](../spec/identity-and-authentication.md) |
| ADMAPI-114 | Deprecated identity operations of the repository service | Later | open | – |
| ADMAPI-013 | Create group | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| ADMAPI-014 | Get group | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| ADMAPI-015 | List groups | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| ADMAPI-016 | Update group | MVP | specified | [R-API-013](../spec/api-and-ui.md), [R-API-031](../spec/api-and-ui.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| ADMAPI-017 | Delete group | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| ADMAPI-018 | Add or remove group members | MVP | specified | [R-API-031](../spec/api-and-ui.md), [R-AUTH-018](../spec/identity-and-authentication.md) |
| ADMAPI-019 | Global role operations | P1 | open | – |
| ADMAPI-020 | Create permission policy | MVP | specified | [R-API-033](../spec/api-and-ui.md), [R-AUTHZ-021](../spec/authorization.md) |
| ADMAPI-021 | List permission policies | MVP | specified | [R-API-033](../spec/api-and-ui.md), [R-AUTHZ-021](../spec/authorization.md) |
| ADMAPI-022 | Get permission policy | MVP | specified | [R-API-033](../spec/api-and-ui.md), [R-AUTHZ-021](../spec/authorization.md) |
| ADMAPI-023 | Delete permission policy | MVP | specified | [R-API-033](../spec/api-and-ui.md), [R-AUTHZ-021](../spec/authorization.md) |
| ADMAPI-024 | Operate on one resource section of a policy | MVP | specified | [R-API-013](../spec/api-and-ui.md), [R-API-033](../spec/api-and-ui.md), [R-AUTHZ-021](../spec/authorization.md) |
| ADMAPI-025 | Older permission interface with resource sections | P2 | open | – |
| ADMAPI-026 | Oldest repository-only permission interface | P2 | open | – |
| ADMAPI-027 | Webhook management rights and webhook token scope | P1 | specified | [R-API-041](../spec/api-and-ui.md), [R-AUTHZ-012](../spec/authorization.md), [R-OPS-039](../spec/operations-and-observability.md) |
| ADMAPI-028 | OIDC provider configuration operations | MVP | specified | [R-API-035](../spec/api-and-ui.md), [R-CI-002](../spec/gitlab-ci-oidc.md) |
| ADMAPI-029 | Identity mapping operations | MVP | specified | [R-API-035](../spec/api-and-ui.md), [R-CI-019](../spec/gitlab-ci-oidc.md) |
| ADMAPI-030 | Project-scoped identity mappings | P1 | specified | [R-API-035](../spec/api-and-ui.md), [R-CI-023](../spec/gitlab-ci-oidc.md) |
| ADMAPI-031 | OIDC token exchange | MVP | specified | [R-API-035](../spec/api-and-ui.md), [R-CI-007](../spec/gitlab-ci-oidc.md) |
| ADMAPI-032 | SAML configuration operations | P2 | open | – |
| ADMAPI-033 | Directory (LDAP) connection operations | P2 | open | – |
| ADMAPI-034 | Directory group settings, search and import operations | P2 | open | – |
| ADMAPI-035 | SCIM 2.0 provisioning endpoint | P1 | specified | [R-API-046](../spec/api-and-ui.md), [R-AUTH-039](../spec/identity-and-authentication.md) |
| ADMAPI-036 | Trusted reverse-proxy login configuration | P2 | open | – |
| ADMAPI-037 | Crowd connector configuration | Later | open | – |
| ADMAPI-038 | Cloud IAM role binding operations | P2 | open | – |
| ADMAPI-039 | Create or refresh an access token | MVP | specified | [R-API-032](../spec/api-and-ui.md), [R-AUTH-027](../spec/identity-and-authentication.md) |
| ADMAPI-040 | Scope expressions accepted by token creation | MVP | specified | [R-API-032](../spec/api-and-ui.md), [R-AUTHZ-013](../spec/authorization.md), [R-AUTH-024](../spec/identity-and-authentication.md) |
| ADMAPI-041 | Read operations open to non-administrators through system scopes | P1 | specified | [R-API-008](../spec/api-and-ui.md) (implemented), [R-AUTHZ-011](../spec/authorization.md), [R-AUTH-025](../spec/identity-and-authentication.md) |
| ADMAPI-042 | Token lifetime policy settings | MVP | specified | [R-API-037](../spec/api-and-ui.md), [R-AUTH-026](../spec/identity-and-authentication.md) |
| ADMAPI-043 | List tokens | MVP | specified | [R-API-032](../spec/api-and-ui.md), [R-AUTH-028](../spec/identity-and-authentication.md) |
| ADMAPI-044 | Get token details | MVP | specified | [R-API-032](../spec/api-and-ui.md), [R-AUTH-028](../spec/identity-and-authentication.md) |
| ADMAPI-045 | Revoke a token by ID | MVP | specified | [R-API-032](../spec/api-and-ui.md), [R-AUTH-029](../spec/identity-and-authentication.md) |
| ADMAPI-046 | Revoke a token by value | MVP | specified | [R-API-032](../spec/api-and-ui.md), [R-AUTH-029](../spec/identity-and-authentication.md) |
| ADMAPI-047 | Read and set the default token expiry | MVP | specified | [R-API-037](../spec/api-and-ui.md), [R-AUTH-040](../spec/identity-and-authentication.md) |
| ADMAPI-048 | Create a pairing token | Later | open | – |
| ADMAPI-049 | Legacy API key operations | Later | open | – |
| ADMAPI-050 | Account lockout policy and locked users | P1 | specified | [R-API-037](../spec/api-and-ui.md), [R-AUTH-038](../spec/identity-and-authentication.md) |
| ADMAPI-051 | Password expiration policy and bulk expiry | P2 | open | – |
| ADMAPI-052 | Encrypted client password | P2 | open | – |
| ADMAPI-053 | Activate and deactivate secret encryption | P2 | open | – |
| ADMAPI-054 | Password complexity policy | P2 | open | – |
| ADMAPI-055 | Basic-authentication switch | P1 | specified | [R-API-037](../spec/api-and-ui.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| ADMAPI-056 | Trust-root certificates of the token issuer | P2 | open | – |
| ADMAPI-057 | External secret-manager connections | P2 | open | – |
| ADMAPI-058 | Default package-signing key (older interface) | P2 | open | – |
| ADMAPI-059 | Signing key pairs | P2 | open | – |
| ADMAPI-060 | Key-pair assignment to repositories | P2 | open | – |
| ADMAPI-061 | Trusted public keys of other services | Later | open | – |
| ADMAPI-062 | Installed TLS certificates | P2 | open | – |
| ADMAPI-063 | Project operations | P1 | specified | [R-API-047](../spec/api-and-ui.md) |
| ADMAPI-064 | Project user membership operations | P1 | specified | [R-API-047](../spec/api-and-ui.md) |
| ADMAPI-065 | Project group membership operations | P1 | specified | [R-API-047](../spec/api-and-ui.md) |
| ADMAPI-066 | Project role operations | P1 | open | – |
| ADMAPI-067 | Repository assignment and sharing operations | P1 | specified | [R-API-047](../spec/api-and-ui.md) |
| ADMAPI-068 | Environment operations | P1 | open | – |
| ADMAPI-069 | Health probes | MVP | implemented | [R-API-038](../spec/api-and-ui.md) (implemented), [R-OPS-001](../spec/operations-and-observability.md) (implemented) |
| ADMAPI-070 | Version and system information | MVP | implemented | [R-API-038](../spec/api-and-ui.md) (implemented), [R-OPS-002](../spec/operations-and-observability.md) (implemented), [R-OPS-004](../spec/operations-and-observability.md) (implemented) |
| ADMAPI-071 | External base URL | MVP | specified | [R-API-037](../spec/api-and-ui.md), [R-OPS-005](../spec/operations-and-observability.md) (implemented) |
| ADMAPI-072 | Read and replace the whole configuration document | P1 | specified | [R-API-044](../spec/api-and-ui.md), [R-OPS-032](../spec/operations-and-observability.md) |
| ADMAPI-073 | Apply partial configuration changes from YAML | P1 | specified | [R-API-044](../spec/api-and-ui.md), [R-OPS-030](../spec/operations-and-observability.md) |
| ADMAPI-074 | Verify an outbound connection | P2 | open | – |
| ADMAPI-075 | Reverse-proxy configuration generator | P2 | open | – |
| ADMAPI-076 | SHA-256 migration task | Later | open | – |
| ADMAPI-077 | Re-index paths in the package metadata service | P2 | open | – |
| ADMAPI-078 | Cluster lock diagnostics | P1 | specified | [R-API-045](../spec/api-and-ui.md), [R-OPS-026](../spec/operations-and-observability.md) |
| ADMAPI-079 | Upstream curation availability setting | Later | open | – |
| ADMAPI-080 | Temporary logger levels | P1 | specified | [R-API-045](../spec/api-and-ui.md), [R-OPS-012](../spec/operations-and-observability.md) (implemented) |
| ADMAPI-081 | Support bundles | P1 | specified | [R-API-045](../spec/api-and-ui.md), [R-OPS-027](../spec/operations-and-observability.md) |
| ADMAPI-082 | Support bundles of the distribution service | Later | open | – |
| ADMAPI-083 | System and repository import and export | P2 | open | – |
| ADMAPI-084 | Identity configuration export and import | P1 | specified | [R-API-044](../spec/api-and-ui.md), [R-OPS-032](../spec/operations-and-observability.md) |
| ADMAPI-085 | Package cleanup policy operations | P1 | specified | [R-API-043](../spec/api-and-ui.md) |
| ADMAPI-086 | Build cleanup policy operations | P1 | specified | [R-API-043](../spec/api-and-ui.md) |
| ADMAPI-087 | Release-set cleanup policy operations | Later | open | – |
| ADMAPI-088 | Cleanup run control, views and reports | P1 | specified | [R-API-043](../spec/api-and-ui.md) |
| ADMAPI-089 | Package archive policy operations | Later | open | – |
| ADMAPI-090 | Older artifact-level archive operations | Later | open | – |
| ADMAPI-091 | Hook definition operations | Later | open | – |
| ADMAPI-092 | Hook event kinds | Later | open | – |
| ADMAPI-093 | Execute, test and rerun hooks | Later | open | – |
| ADMAPI-094 | Hook discovery metadata | Later | open | – |
| ADMAPI-095 | Hook service health probes | Later | open | – |
| ADMAPI-096 | Central instance registry | Later | open | – |
| ADMAPI-097 | Topology operations | Later | open | – |
| ADMAPI-098 | Entity synchronization monitoring | Later | open | – |
| ADMAPI-099 | Identity federation targets on one instance | Later | open | – |
| ADMAPI-100 | Central identity federation configuration | Later | open | – |
| ADMAPI-101 | Curation policy federation | Later | open | – |
| ADMAPI-102 | Central console system operations | Later | open | – |
| ADMAPI-103 | License installation and inspection | Out of scope | open | – |
| ADMAPI-104 | License pools in the central console | Out of scope | open | – |
| ADMAPI-105 | Network tunnel between a hosted and a self-hosted instance | Out of scope | open | – |
| ADMAPI-106 | Entitlement connector and offline registration | Out of scope | open | – |
| ADMAPI-107 | Hosted account portal operations | Out of scope | open | – |
| ADMAPI-108 | Hosted log collection | Out of scope | open | – |
| ADMAPI-109 | Application lifecycle service operations | Later | open | – |
| ADMAPI-110 | Policy, rule, template and evaluation operations | Later | open | – |
| ADMAPI-111 | Lifecycle stage operations | Later | open | – |
| ADMAPI-112 | Evidence operations | Later | open | – |
| ADMAPI-113 | Release set operations | Later | open | – |

</details>

## Package formats: matrix and cross-format behavior (FMT)

Catalog page: [formats/README.md](../features/formats/README.md) · specified by priority: MVP 8 of 8 · P1 1 of 3 · P2 0 of 1 · Later 0 of 1 · Out of scope 0 of 1

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| FMT-001 | Format catalog and repository-class matrix | MVP | specified | [R-ARCH-008](../spec/architecture.md) (implemented), [R-ARCH-010](../spec/architecture.md) |
| FMT-002 | Several native clients per format | MVP | specified | [R-ARCH-010](../spec/architecture.md), [R-NPM-045](../spec/formats/npm.md) |
| FMT-003 | Client version floors and validated client versions | MVP | specified | [R-ARCH-010](../spec/architecture.md), [R-NPM-045](../spec/formats/npm.md) |
| FMT-004 | Format eligibility for federation | Later | open | – |
| FMT-005 | Container-only deployment profile | Out of scope | open | – |
| FMT-006 | Client setup instructions across formats | MVP | specified | [R-ARCH-008](../spec/architecture.md) (implemented), [R-NPM-041](../spec/formats/npm.md) |
| FMT-007 | Package view and file view | MVP | specified | [R-ARCH-009](../spec/architecture.md) |
| FMT-008 | Format property namespaces | MVP | specified | [R-ARCH-008](../spec/architecture.md) (implemented), [R-NPM-006](../spec/formats/npm.md) |
| FMT-009 | Format-aware deploy coordinates | P2 | open | – |
| FMT-010 | Default layout per format and layout mapping | P1 | open | – |
| FMT-011 | Automatic metadata calculation | MVP | specified | [R-ARCH-008](../spec/architecture.md) (implemented), [R-ARCH-012](../spec/architecture.md), [R-ARCH-016](../spec/architecture.md) (implemented), [R-NPM-010](../spec/formats/npm.md), [R-NUGET-005](../spec/formats/nuget.md) |
| FMT-012 | Manual metadata recalculation | P1 | specified | [R-ARCH-016](../spec/architecture.md) (implemented), [R-NPM-035](../spec/formats/npm.md), [R-NUGET-025](../spec/formats/nuget.md) |
| FMT-013 | Client authentication styles and anonymous fallbacks | MVP | specified | [R-AUTH-033](../spec/identity-and-authentication.md) |
| FMT-014 | Package settings screen | P1 | open | – |

## npm package format (PKG-NPM)

Catalog page: [formats/npm.md](../features/formats/npm.md) · specified by priority: MVP 37 of 37 · P1 15 of 15 · P2 0 of 8 · Later 0 of 1 · Out of scope 0 of 1

<details>
<summary>62 features: 52 specified, 10 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-NPM-001 | npm repositories in all three repository classes | MVP | specified | [R-NPM-001](../spec/formats/npm.md) |
| PKG-NPM-002 | Registry URL of a repository | MVP | specified | [R-NPM-001](../spec/formats/npm.md) |
| PKG-NPM-003 | Scoped packages and encoded slashes | MVP | specified | [R-AUTHZ-005](../spec/authorization.md), [R-NPM-002](../spec/formats/npm.md) |
| PKG-NPM-004 | Package name rules | MVP | specified | [R-NPM-002](../spec/formats/npm.md) |
| PKG-NPM-005 | Bearer token in client configuration | MVP | specified | [R-NPM-004](../spec/formats/npm.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| PKG-NPM-006 | Basic credentials in client configuration | MVP | specified | [R-NPM-004](../spec/formats/npm.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| PKG-NPM-007 | When clients send credentials | MVP | specified | [R-AUTHZ-016](../spec/authorization.md), [R-NPM-004](../spec/formats/npm.md), [R-AUTH-002](../spec/identity-and-authentication.md) |
| PKG-NPM-008 | Browser (web) login | P1 | specified | [R-NPM-020](../spec/formats/npm.md), [R-AUTH-034](../spec/identity-and-authentication.md) |
| PKG-NPM-009 | Legacy username and password login | P1 | specified | [R-NPM-021](../spec/formats/npm.md), [R-AUTH-035](../spec/identity-and-authentication.md) |
| PKG-NPM-010 | Logout | P1 | specified | [R-NPM-021](../spec/formats/npm.md), [R-AUTH-035](../spec/identity-and-authentication.md) |
| PKG-NPM-011 | Identity and connectivity checks | MVP | specified | [R-NPM-019](../spec/formats/npm.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| PKG-NPM-012 | One-time-password challenge on writes | P2 | open | – |
| PKG-NPM-013 | Trusted publishing through CI OIDC token exchange | P1 | specified | [R-NPM-022](../spec/formats/npm.md) |
| PKG-NPM-014 | Client-side token and profile management | P2 | open | – |
| PKG-NPM-015 | Publish through the npm protocol | MVP | specified | [R-AUTHZ-007](../spec/authorization.md), [R-NPM-007](../spec/formats/npm.md) |
| PKG-NPM-016 | Immutable versions | MVP | specified | [R-NPM-008](../spec/formats/npm.md) |
| PKG-NPM-017 | Deploy a package tarball through the UI or generic upload | P1 | specified | [R-NPM-036](../spec/formats/npm.md) |
| PKG-NPM-018 | Reject uploads whose path does not match the package | P1 | specified | [R-NPM-036](../spec/formats/npm.md) |
| PKG-NPM-019 | Unpublish | MVP | specified | [R-AUTHZ-007](../spec/authorization.md), [R-NPM-017](../spec/formats/npm.md) |
| PKG-NPM-020 | Deprecate and undeprecate | P1 | specified | [R-NPM-018](../spec/formats/npm.md) |
| PKG-NPM-021 | Dist-tag management | MVP | specified | [R-AUTHZ-007](../spec/authorization.md), [R-NPM-016](../spec/formats/npm.md) |
| PKG-NPM-022 | Maintainer list changes | P2 | open | – |
| PKG-NPM-023 | Staged publishing | Later | open | – |
| PKG-NPM-024 | Provenance bundles and attestation links | P2 | open | – |
| PKG-NPM-025 | Full package document | MVP | specified | [R-NPM-010](../spec/formats/npm.md) |
| PKG-NPM-026 | Abbreviated package document | MVP | specified | [R-NPM-011](../spec/formats/npm.md) |
| PKG-NPM-027 | Publish times for client-side release-age policies | MVP | specified | [R-NPM-012](../spec/formats/npm.md) |
| PKG-NPM-028 | Single-version document | P1 | specified | [R-NPM-014](../spec/formats/npm.md) |
| PKG-NPM-062 | Tarball download | MVP | specified | [R-NPM-015](../spec/formats/npm.md), [R-NPM-025](../spec/formats/npm.md) |
| PKG-NPM-029 | Metadata calculation for local repositories | MVP | specified | [R-NPM-010](../spec/formats/npm.md) |
| PKG-NPM-030 | Recalculate npm metadata | P1 | specified | [R-NPM-035](../spec/formats/npm.md) |
| PKG-NPM-031 | Selection of the `latest` dist-tag | MVP | specified | [R-NPM-016](../spec/formats/npm.md), [R-NPM-031](../spec/formats/npm.md) |
| PKG-NPM-032 | Conditional requests and cache headers | MVP | specified | [R-NPM-013](../spec/formats/npm.md) |
| PKG-NPM-033 | Stored package properties | MVP | specified | [R-NPM-006](../spec/formats/npm.md) |
| PKG-NPM-034 | Storage layout | MVP | specified | [R-NPM-003](../spec/formats/npm.md), [R-NPM-006](../spec/formats/npm.md) |
| PKG-NPM-035 | Strict layout enforcement per local repository | P2 | open | – |
| PKG-NPM-036 | Strict-layout conversion of existing repositories | P2 | open | – |
| PKG-NPM-037 | Registry signatures for `npm audit signatures` | P2 | open | – |
| PKG-NPM-038 | Proxying an upstream npm registry | MVP | specified | [R-AUTHZ-008](../spec/authorization.md), [R-NPM-024](../spec/formats/npm.md), [R-NPM-025](../spec/formats/npm.md), [R-NPM-027](../spec/formats/npm.md) |
| PKG-NPM-039 | Remote metadata freshness and invalidation | MVP | specified | [R-NPM-024](../spec/formats/npm.md) |
| PKG-NPM-040 | Credentials for a private upstream registry | P1 | specified | [R-NPM-023](../spec/formats/npm.md) |
| PKG-NPM-041 | FHIR package registry as upstream | P2 | open | – |
| PKG-NPM-042 | Merged package documents | MVP | specified | [R-AUTHZ-009](../spec/authorization.md), [R-NPM-030](../spec/formats/npm.md), [R-NPM-033](../spec/formats/npm.md) |
| PKG-NPM-043 | Dist-tag resolution across members | MVP | specified | [R-NPM-031](../spec/formats/npm.md) |
| PKG-NPM-044 | Virtual metadata cache period | MVP | specified | [R-NPM-032](../spec/formats/npm.md) |
| PKG-NPM-045 | Publishing through a virtual repository | MVP | specified | [R-AUTHZ-009](../spec/authorization.md), [R-NPM-029](../spec/formats/npm.md) |
| PKG-NPM-046 | External dependency rewrite | P1 | specified | [R-NPM-034](../spec/formats/npm.md) |
| PKG-NPM-047 | Permissions covering metadata as well as tarballs | MVP | specified | [R-AUTHZ-005](../spec/authorization.md), [R-NPM-003](../spec/formats/npm.md) |
| PKG-NPM-048 | `npm search` | P1 | specified | [R-NPM-037](../spec/formats/npm.md) |
| PKG-NPM-049 | `npm audit` forwarding | P1 | specified | [R-NPM-038](../spec/formats/npm.md) |
| PKG-NPM-050 | npm client configuration | MVP | specified | [R-NPM-041](../spec/formats/npm.md) |
| PKG-NPM-051 | pnpm | MVP | specified | [R-NPM-040](../spec/formats/npm.md) |
| PKG-NPM-052 | Yarn Classic (1.x) | MVP | specified | [R-NPM-040](../spec/formats/npm.md) |
| PKG-NPM-053 | Yarn Modern (2 and later) | MVP | specified | [R-NPM-042](../spec/formats/npm.md) |
| PKG-NPM-054 | Client setup instructions in the UI | MVP | specified | [R-NPM-041](../spec/formats/npm.md) |
| PKG-NPM-055 | Build metadata through a companion CLI wrapper | P1 | specified | [R-NPM-039](../spec/formats/npm.md) |
| PKG-NPM-056 | Read endpoints | MVP | specified | [R-NPM-001](../spec/formats/npm.md) |
| PKG-NPM-057 | Write endpoints | MVP | specified | [R-NPM-007](../spec/formats/npm.md), [R-NPM-009](../spec/formats/npm.md) |
| PKG-NPM-058 | Authentication endpoints | MVP | specified | [R-NPM-019](../spec/formats/npm.md), [R-NPM-020](../spec/formats/npm.md), [R-NPM-043](../spec/formats/npm.md) |
| PKG-NPM-059 | Security audit endpoints | P1 | specified | [R-NPM-038](../spec/formats/npm.md) |
| PKG-NPM-060 | Headers and error bodies | MVP | specified | [R-NPM-005](../spec/formats/npm.md), [R-NPM-043](../spec/formats/npm.md) |
| PKG-NPM-061 | Public-registry-only endpoints | Out of scope | open | – |

</details>

## NuGet (PKG-NUGET)

Catalog page: [formats/nuget.md](../features/formats/nuget.md) · specified by priority: MVP 35 of 35 · P1 7 of 7 · P2 0 of 12 · Later 0 of 2

<details>
<summary>56 features: 42 specified, 14 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-NUGET-001 | NuGet repositories in all three classes | MVP | specified | [R-NUGET-001](../spec/formats/nuget.md) |
| PKG-NUGET-002 | Feed addresses per protocol | MVP | specified | [R-NUGET-001](../spec/formats/nuget.md) |
| PKG-NUGET-003 | NuGet v3 protocol | MVP | specified | [R-NUGET-001](../spec/formats/nuget.md) |
| PKG-NUGET-004 | NuGet v2 (OData) and v1 feed | P2 | open | – |
| PKG-NUGET-005 | Package recognition and stored properties | MVP | specified | [R-NUGET-005](../spec/formats/nuget.md) |
| PKG-NUGET-006 | Automatic metadata calculation | MVP | specified | [R-NUGET-005](../spec/formats/nuget.md) |
| PKG-NUGET-007 | Recalculate NuGet metadata (reindex) | P1 | specified | [R-NUGET-025](../spec/formats/nuget.md) |
| PKG-NUGET-008 | Enforced layout (normalized repositories) | MVP | specified | [R-NUGET-003](../spec/formats/nuget.md) |
| PKG-NUGET-009 | Version normalization and duplicate detection | MVP | specified | [R-NUGET-003](../spec/formats/nuget.md) |
| PKG-NUGET-010 | Global normalization for new repositories | P2 | open | – |
| PKG-NUGET-011 | Normalization compatibility between repositories | MVP | specified | [R-NUGET-003](../spec/formats/nuget.md) |
| PKG-NUGET-012 | Custom layouts and folder paths | P2 | open | – |
| PKG-NUGET-013 | Normalization migration of existing repositories | P2 | open | – |
| PKG-NUGET-014 | SemVer 2.0.0 packages | MVP | specified | [R-NUGET-004](../spec/formats/nuget.md) |
| PKG-NUGET-015 | Maximum unique snapshots on NuGet repositories | P2 | open | – |
| PKG-NUGET-016 | Anonymous access and forced authentication | MVP | specified | [R-AUTHZ-016](../spec/authorization.md), [R-NUGET-012](../spec/formats/nuget.md), [R-AUTH-002](../spec/identity-and-authentication.md), [R-AUTH-037](../spec/identity-and-authentication.md) |
| PKG-NUGET-017 | Client credentials over HTTP Basic | MVP | specified | [R-NUGET-012](../spec/formats/nuget.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| PKG-NUGET-018 | API key for push and delete | MVP | specified | [R-NUGET-006](../spec/formats/nuget.md), [R-NUGET-012](../spec/formats/nuget.md), [R-AUTH-033](../spec/identity-and-authentication.md) |
| PKG-NUGET-019 | Permissions per NuGet operation | MVP | specified | [R-AUTHZ-007](../spec/authorization.md), [R-AUTHZ-008](../spec/authorization.md), [R-NUGET-013](../spec/formats/nuget.md) |
| PKG-NUGET-020 | Push through the NuGet protocol | MVP | specified | [R-NUGET-006](../spec/formats/nuget.md) |
| PKG-NUGET-021 | Deploy through the UI and generic upload | MVP | specified | [R-NUGET-005](../spec/formats/nuget.md) |
| PKG-NUGET-022 | Delete, unlist and relist | MVP | specified | [R-AUTHZ-007](../spec/authorization.md), [R-NUGET-007](../spec/formats/nuget.md) |
| PKG-NUGET-023 | NuGet remote repository settings | MVP | specified | [R-NUGET-014](../spec/formats/nuget.md) |
| PKG-NUGET-024 | Proxying the public NuGet gallery | MVP | specified | [R-NUGET-014](../spec/formats/nuget.md), [R-NUGET-015](../spec/formats/nuget.md), [R-NUGET-016](../spec/formats/nuget.md) |
| PKG-NUGET-025 | Chained remote NuGet repository | P2 | open | – |
| PKG-NUGET-026 | NuGet virtual repository | MVP | specified | [R-NUGET-017](../spec/formats/nuget.md), [R-NUGET-018](../spec/formats/nuget.md) |
| PKG-NUGET-027 | Push through a virtual repository | MVP | specified | [R-AUTHZ-009](../spec/authorization.md), [R-NUGET-019](../spec/formats/nuget.md) |
| PKG-NUGET-056 | Federated NuGet repositories | Later | open | – |
| PKG-NUGET-028 | Client search and version listing | MVP | specified | [R-NUGET-010](../spec/formats/nuget.md), [R-NUGET-016](../spec/formats/nuget.md), [R-NUGET-018](../spec/formats/nuget.md) |
| PKG-NUGET-029 | Symbol package upload | MVP | specified | [R-NUGET-020](../spec/formats/nuget.md) |
| PKG-NUGET-030 | Symbol server for debuggers | P1 | specified | [R-NUGET-021](../spec/formats/nuget.md) |
| PKG-NUGET-031 | Proxying an upstream symbol server | P1 | specified | [R-NUGET-022](../spec/formats/nuget.md) |
| PKG-NUGET-032 | Symbol resolution through virtual repositories | P1 | specified | [R-NUGET-022](../spec/formats/nuget.md) |
| PKG-NUGET-033 | nuget.exe (NuGet CLI) | MVP | specified | [R-NUGET-026](../spec/formats/nuget.md) |
| PKG-NUGET-034 | .NET CLI | MVP | specified | [R-NUGET-026](../spec/formats/nuget.md) |
| PKG-NUGET-035 | Visual Studio | MVP | specified | [R-NUGET-026](../spec/formats/nuget.md) |
| PKG-NUGET-036 | nuget.config elements the server relies on | MVP | specified | [R-NUGET-026](../spec/formats/nuget.md) |
| PKG-NUGET-037 | PowerShell package management | P2 | open | – |
| PKG-NUGET-038 | Chocolatey | P2 | open | – |
| PKG-NUGET-039 | Companion client wrapping nuget and dotnet | P2 | open | – |
| PKG-NUGET-040 | Newest-compliant-version selection for NuGet clients | Later | open | – |
| PKG-NUGET-041 | Protocol conventions | MVP | specified | [R-NUGET-002](../spec/formats/nuget.md) |
| PKG-NUGET-042 | Package IDs and versions on the wire | MVP | specified | [R-NUGET-003](../spec/formats/nuget.md), [R-NUGET-004](../spec/formats/nuget.md), [R-NUGET-009](../spec/formats/nuget.md) |
| PKG-NUGET-043 | Service index | MVP | specified | [R-NUGET-002](../spec/formats/nuget.md) |
| PKG-NUGET-044 | Resource URL placement for authenticated feeds | MVP | specified | [R-NUGET-002](../spec/formats/nuget.md) |
| PKG-NUGET-045 | Package content (flat container) | MVP | specified | [R-NUGET-008](../spec/formats/nuget.md) |
| PKG-NUGET-046 | Package metadata (registrations) | MVP | specified | [R-NUGET-009](../spec/formats/nuget.md) |
| PKG-NUGET-047 | Search query service | MVP | specified | [R-NUGET-010](../spec/formats/nuget.md) |
| PKG-NUGET-048 | Search autocomplete service | P1 | specified | [R-NUGET-011](../spec/formats/nuget.md) |
| PKG-NUGET-049 | Push, delete and relist (package publish) | MVP | specified | [R-NUGET-006](../spec/formats/nuget.md), [R-NUGET-007](../spec/formats/nuget.md) |
| PKG-NUGET-050 | Symbol package publish | MVP | specified | [R-NUGET-020](../spec/formats/nuget.md) |
| PKG-NUGET-051 | Vulnerability information | P1 | specified | [R-NUGET-023](../spec/formats/nuget.md) |
| PKG-NUGET-052 | Repository signatures | P2 | open | – |
| PKG-NUGET-053 | Catalog | P2 | open | – |
| PKG-NUGET-054 | URI template resources | P2 | open | – |
| PKG-NUGET-055 | Embedded icon, license and README files | P1 | specified | [R-NUGET-024](../spec/formats/nuget.md) |

</details>

## Docker image format (PKG-DOCKER)

Catalog page: [formats/docker.md](../features/formats/docker.md) · specified by priority: P1 0 of 34 · P2 1 of 13 · Later 0 of 3 · Out of scope 0 of 2

<details>
<summary>52 features: 1 specified, 51 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-DOCKER-001 | Docker registries in all three repository classes | P1 | open | – |
| PKG-DOCKER-002 | Repository keys usable as registry names | P1 | open | – |
| PKG-DOCKER-003 | Image-name visibility for readers | P1 | open | – |
| PKG-DOCKER-004 | Registry API version per repository | P1 | open | – |
| PKG-DOCKER-005 | How clients address a repository | P1 | open | – |
| PKG-DOCKER-006 | Repository path method | P1 | open | – |
| PKG-DOCKER-007 | Subdomain method | P2 | specified | [R-EP-007](../spec/endpoints.md) |
| PKG-DOCKER-008 | Port method | P2 | open | – |
| PKG-DOCKER-009 | Reverse-proxy configuration for registry access | P2 | open | – |
| PKG-DOCKER-010 | Hosted-service address formats | Out of scope | open | – |
| PKG-DOCKER-011 | Login and stored client credentials | P1 | open | – |
| PKG-DOCKER-012 | Authentication challenge on the registry API | P1 | open | – |
| PKG-DOCKER-013 | Credential snippet operation | P2 | open | – |
| PKG-DOCKER-014 | Docker client login through an OAuth session | P2 | open | – |
| PKG-DOCKER-015 | Image pull credentials for Kubernetes | P1 | open | – |
| PKG-DOCKER-016 | Registry HTTP API v2 | P1 | open | – |
| PKG-DOCKER-017 | Pulling images | P1 | open | – |
| PKG-DOCKER-018 | Pushing image layers | P1 | open | – |
| PKG-DOCKER-019 | Layer deduplication | P1 | open | – |
| PKG-DOCKER-020 | Pushing manifests and tags | P1 | open | – |
| PKG-DOCKER-021 | Blocking schema-1 manifests | P1 | open | – |
| PKG-DOCKER-022 | Catalog and tag listing | P1 | open | – |
| PKG-DOCKER-023 | Manifest lists and image indexes | P1 | open | – |
| PKG-DOCKER-024 | Operating-system-qualified tag on platform-specific push | P2 | open | – |
| PKG-DOCKER-025 | Delete a multi-architecture tag | P1 | open | – |
| PKG-DOCKER-026 | Tag syntax validation | P1 | open | – |
| PKG-DOCKER-027 | Overwrite permission for existing tags | P1 | open | – |
| PKG-DOCKER-028 | Maximum unique tags per image | P1 | open | – |
| PKG-DOCKER-029 | Retention of overwritten tag revisions | P1 | open | – |
| PKG-DOCKER-030 | Download statistics shared across tags of one digest | P2 | open | – |
| PKG-DOCKER-031 | Deleting images and tags | P1 | open | – |
| PKG-DOCKER-032 | Promote an image between repositories | P1 | open | – |
| PKG-DOCKER-033 | Proxying Docker Hub and other registries | P1 | open | – |
| PKG-DOCKER-034 | Docker Hub rate limits | P1 | open | – |
| PKG-DOCKER-035 | Official-image namespace on Docker Hub | P1 | open | – |
| PKG-DOCKER-036 | Foreign layers | P2 | open | – |
| PKG-DOCKER-037 | Project scope for Google registries | P2 | open | – |
| PKG-DOCKER-038 | Chaining to another instance | P2 | open | – |
| PKG-DOCKER-039 | Virtual Docker registry | P1 | open | – |
| PKG-DOCKER-040 | Resolve tags by latest timestamp | P1 | open | – |
| PKG-DOCKER-052 | Upstream for a cloud registry's pass-through repository | P2 | open | – |
| PKG-DOCKER-041 | Image tag details | P1 | open | – |
| PKG-DOCKER-042 | Image labels as searchable properties | P1 | open | – |
| PKG-DOCKER-043 | Storage layout of images | P1 | open | – |
| PKG-DOCKER-044 | Docker Content Trust | P2 | open | – |
| PKG-DOCKER-045 | Supported container clients | P1 | open | – |
| PKG-DOCKER-046 | Client setup instructions | P1 | open | – |
| PKG-DOCKER-047 | Build metadata for images through a companion CLI | P2 | open | – |
| PKG-DOCKER-048 | Local image scanning through the companion CLI | Later | open | – |
| PKG-DOCKER-049 | Docker registry API v1 repositories | Later | open | – |
| PKG-DOCKER-050 | Migrate a v1 repository to v2 | Later | open | – |
| PKG-DOCKER-051 | Container-only deployment profile | Out of scope | open | – |

</details>

## OCI images and artifacts (PKG-OCI)

Catalog page: [formats/oci.md](../features/formats/oci.md) · specified by priority: P1 0 of 15 · P2 0 of 4 · Later 0 of 1

<details>
<summary>20 features: 0 specified, 20 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-OCI-001 | OCI repositories in all three repository classes | P1 | open | – |
| PKG-OCI-002 | Local OCI repository settings | P1 | open | – |
| PKG-OCI-003 | Proxying OCI-compliant registries | P1 | open | – |
| PKG-OCI-004 | Virtual OCI repositories | P1 | open | – |
| PKG-OCI-005 | How OCI clients address a repository | P1 | open | – |
| PKG-OCI-006 | Distribution specification endpoints | P1 | open | – |
| PKG-OCI-007 | Manifest kinds and content negotiation | P1 | open | – |
| PKG-OCI-008 | Non-image artifacts | P1 | open | – |
| PKG-OCI-009 | Referrers API | P1 | open | – |
| PKG-OCI-010 | Referrer storage and the referrers index | P1 | open | – |
| PKG-OCI-011 | Referrers tag schema | P2 | open | – |
| PKG-OCI-012 | Moving referrers with their subject | P2 | open | – |
| PKG-OCI-013 | Podman | P1 | open | – |
| PKG-OCI-014 | Docker Buildx | P1 | open | – |
| PKG-OCI-015 | BuildKit and registry build caches | P2 | open | – |
| PKG-OCI-016 | WebAssembly modules | P2 | open | – |
| PKG-OCI-017 | ORAS | P1 | open | – |
| PKG-OCI-018 | Helm charts in OCI repositories | P1 | open | – |
| PKG-OCI-019 | Client setup instructions | P1 | open | – |
| PKG-OCI-020 | Scanning limited to container images | Later | open | – |

</details>

## Helm chart format (PKG-HELM)

Catalog page: [formats/helm.md](../features/formats/helm.md) · specified by priority: P1 0 of 18 · P2 0 of 6 · Later 0 of 1

<details>
<summary>25 features: 0 specified, 25 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-HELM-001 | Two flavors of Helm repositories | P1 | open | – |
| PKG-HELM-002 | Supported Helm client versions | P1 | open | – |
| PKG-HELM-003 | Chart repository protocol | P1 | open | – |
| PKG-HELM-004 | Deploying charts to an index-based repository | P1 | open | – |
| PKG-HELM-005 | Automatic index calculation for local repositories | P1 | open | – |
| PKG-HELM-006 | Recalculate the chart index | P1 | open | – |
| PKG-HELM-007 | Partial re-indexing | P2 | open | – |
| PKG-HELM-008 | Relative chart URLs in the index | P1 | open | – |
| PKG-HELM-009 | Prevent duplicate chart paths | P1 | open | – |
| PKG-HELM-010 | Enforce chart name, version and SemVer | P1 | open | – |
| PKG-HELM-011 | Proxying an index-based chart repository | P1 | open | – |
| PKG-HELM-012 | Charts base URL | P1 | open | – |
| PKG-HELM-013 | External dependency rewrite | P2 | open | – |
| PKG-HELM-014 | Merged index in virtual repositories | P1 | open | – |
| PKG-HELM-015 | Member selection by namespace in virtual repositories | P2 | open | – |
| PKG-HELM-016 | Chart information views | P2 | open | – |
| PKG-HELM-017 | Helm-via-OCI repositories | P1 | open | – |
| PKG-HELM-018 | Pushing and pulling charts over OCI | P1 | open | – |
| PKG-HELM-019 | Helm-via-OCI repository settings | P1 | open | – |
| PKG-HELM-020 | Container access method applies to chart registries | P1 | open | – |
| PKG-HELM-021 | Generic content and list handling in Helm-via-OCI repositories | P2 | open | – |
| PKG-HELM-022 | Promoting charts stored as OCI | P1 | open | – |
| PKG-HELM-023 | Build metadata for charts through a companion CLI | P2 | open | – |
| PKG-HELM-024 | Client setup instructions | P1 | open | – |
| PKG-HELM-025 | Chart scanning | Later | open | – |

</details>

## Maven package format (PKG-MAVEN)

Catalog page: [formats/maven.md](../features/formats/maven.md) · specified by priority: P1 0 of 23 · P2 0 of 15 · Later 0 of 1

<details>
<summary>39 features: 0 specified, 39 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-MAVEN-001 | Maven repositories in all repository classes | P1 | open | – |
| PKG-MAVEN-002 | Repository URL and Maven 2 path layout | P1 | open | – |
| PKG-MAVEN-003 | Release and snapshot handling | P1 | open | – |
| PKG-MAVEN-004 | Deploy through the Maven protocol | P1 | open | – |
| PKG-MAVEN-005 | POM consistency check | P1 | open | – |
| PKG-MAVEN-006 | Deployment checksum policy | P1 | open | – |
| PKG-MAVEN-007 | Checksum files next to artifacts | P1 | open | – |
| PKG-MAVEN-008 | Deploy dialog for Maven artifacts | P2 | open | – |
| PKG-MAVEN-009 | Generate a POM for a stored JAR | P2 | open | – |
| PKG-MAVEN-010 | Snapshot naming behavior | P1 | open | – |
| PKG-MAVEN-011 | Maximum unique snapshots | P1 | open | – |
| PKG-MAVEN-012 | Overwrite rules for releases and snapshots | P1 | open | – |
| PKG-MAVEN-013 | Newest snapshot and release by symbolic version | P2 | open | – |
| PKG-MAVEN-014 | Metadata calculation for local repositories | P1 | open | – |
| PKG-MAVEN-015 | Recalculate Maven metadata | P1 | open | – |
| PKG-MAVEN-016 | Metadata kept consistent on deletion | P1 | open | – |
| PKG-MAVEN-017 | Proxying a Maven upstream | P1 | open | – |
| PKG-MAVEN-018 | Upstream checksum policy | P1 | open | – |
| PKG-MAVEN-019 | Eager fetching of jars and sources | P2 | open | – |
| PKG-MAVEN-020 | Browsing upstream content that is not cached | P2 | open | – |
| PKG-MAVEN-021 | Upstreams that need cookies and cross-host credentials | P2 | open | – |
| PKG-MAVEN-022 | Aggregated resolution through a virtual repository | P1 | open | – |
| PKG-MAVEN-023 | Repository references removed from served POMs | P1 | open | – |
| PKG-MAVEN-024 | Cleanup of cached POMs in virtual repositories | P2 | open | – |
| PKG-MAVEN-025 | Force authentication on virtual Maven repositories | P1 | open | – |
| PKG-MAVEN-026 | JAR signing on download through a virtual repository | P2 | open | – |
| PKG-MAVEN-027 | Credentials in Maven client configuration | P1 | open | – |
| PKG-MAVEN-028 | Existence check before authentication | P2 | open | – |
| PKG-MAVEN-029 | Maven settings in the client setup instructions | P1 | open | – |
| PKG-MAVEN-030 | Personalised settings files | P2 | open | – |
| PKG-MAVEN-031 | Maven index for IDE integrations | P2 | open | – |
| PKG-MAVEN-032 | Scheduled index calculation | P2 | open | – |
| PKG-MAVEN-033 | Calculate the Maven index on demand | P2 | open | – |
| PKG-MAVEN-034 | Wrapping Maven with the companion CLI | P2 | open | – |
| PKG-MAVEN-035 | Build-tool plugin for Maven | Later | open | – |
| PKG-MAVEN-036 | Repository paths and HTTP semantics | P1 | open | – |
| PKG-MAVEN-037 | `maven-metadata.xml` documents | P1 | open | – |
| PKG-MAVEN-038 | Snapshot file naming | P1 | open | – |
| PKG-MAVEN-039 | Repository-root auxiliary files | P2 | open | – |

</details>

## PyPI package format (PKG-PYPI)

Catalog page: [formats/pypi.md](../features/formats/pypi.md) · specified by priority: P1 0 of 23 · P2 0 of 6

<details>
<summary>29 features: 0 specified, 29 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-PYPI-001 | PyPI repositories in all three repository classes | P1 | open | – |
| PKG-PYPI-002 | Base URL, index URL and upload URL | P1 | open | – |
| PKG-PYPI-003 | Names that differ only in dots and underscores | P1 | open | – |
| PKG-PYPI-004 | Reserved paths inside a repository | P1 | open | – |
| PKG-PYPI-005 | HTTP Basic credentials for Python clients | P1 | open | – |
| PKG-PYPI-006 | Upload through the PyPI upload protocol | P1 | open | – |
| PKG-PYPI-007 | Package properties from distribution metadata | P1 | open | – |
| PKG-PYPI-008 | Deploy a distribution through the UI or generic upload | P1 | open | – |
| PKG-PYPI-009 | Enforce agreement between file name and metadata | P2 | open | – |
| PKG-PYPI-010 | Enforce standard distribution file names | P2 | open | – |
| PKG-PYPI-011 | Simple index in HTML | P1 | open | – |
| PKG-PYPI-012 | Simple index in JSON | P1 | open | – |
| PKG-PYPI-013 | Metadata calculation for local repositories | P1 | open | – |
| PKG-PYPI-014 | Recalculate PyPI metadata | P1 | open | – |
| PKG-PYPI-015 | Project metadata in JSON | P2 | open | – |
| PKG-PYPI-016 | PyPI details in the UI | P2 | open | – |
| PKG-PYPI-017 | Proxying an upstream Python index | P1 | open | – |
| PKG-PYPI-018 | Custom index suffix and scoped upstream paths | P2 | open | – |
| PKG-PYPI-019 | Merged index across members | P1 | open | – |
| PKG-PYPI-020 | `pip search` | P2 | open | – |
| PKG-PYPI-021 | Property and package search | P1 | open | – |
| PKG-PYPI-022 | pip | P1 | open | – |
| PKG-PYPI-023 | Poetry | P1 | open | – |
| PKG-PYPI-024 | Twine and setuptools | P1 | open | – |
| PKG-PYPI-025 | uv | P1 | open | – |
| PKG-PYPI-026 | Client setup instructions in the UI | P1 | open | – |
| PKG-PYPI-027 | Build metadata through a companion CLI wrapper | P1 | open | – |
| PKG-PYPI-028 | Simple repository API | P1 | open | – |
| PKG-PYPI-029 | Upload API | P1 | open | – |

</details>

## Go modules format (PKG-GO)

Catalog page: [formats/go.md](../features/formats/go.md) · specified by priority: P1 0 of 13 · P2 0 of 3

<details>
<summary>16 features: 0 specified, 16 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-GO-001 | Go repositories in all three repository classes | P1 | open | – |
| PKG-GO-002 | Module proxy endpoint of a repository | P1 | open | – |
| PKG-GO-003 | Resolution only through local and virtual repositories | P1 | open | – |
| PKG-GO-004 | Native `go` client configuration | P1 | open | – |
| PKG-GO-005 | Companion client integration for Go | P2 | open | – |
| PKG-GO-006 | Publish a module version | P1 | open | – |
| PKG-GO-007 | Go remote repository and upstream kind | P1 | open | – |
| PKG-GO-008 | Module-mirror remote | P1 | open | – |
| PKG-GO-009 | VCS-backed remote for private modules | P1 | open | – |
| PKG-GO-010 | GitHub and GitHub Enterprise upstreams | P2 | open | – |
| PKG-GO-011 | GitLab upstream | P1 | open | – |
| PKG-GO-012 | GitLab subgroup resolution | P1 | open | – |
| PKG-GO-013 | Bitbucket Cloud and Bitbucket Server upstreams | P2 | open | – |
| PKG-GO-014 | Checksum database proxy | P1 | open | – |
| PKG-GO-015 | Following `go-import` meta tags | P1 | open | – |
| PKG-GO-016 | Routing module paths to members of a virtual repository | P1 | open | – |

</details>

## Generic format (PKG-GENERIC)

Catalog page: [formats/generic.md](../features/formats/generic.md) · specified by priority: P1 0 of 8 · P2 0 of 3

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-GENERIC-001 | Generic repositories in all three repository classes | P1 | open | – |
| PKG-GENERIC-002 | Non-conformant content belongs in a generic repository | P1 | open | – |
| PKG-GENERIC-003 | Free-form paths and the repository layout reference | P1 | open | – |
| PKG-GENERIC-004 | Deploy any file | P1 | open | – |
| PKG-GENERIC-005 | Download any file | P1 | open | – |
| PKG-GENERIC-006 | Generic remote repository for any HTTP source | P1 | open | – |
| PKG-GENERIC-007 | SHA-256 lookup for uncached items | P2 | open | – |
| PKG-GENERIC-008 | Generic virtual repository | P1 | open | – |
| PKG-GENERIC-009 | HTTP build cache on a generic repository | P2 | open | – |
| PKG-GENERIC-010 | Bulk file operations on generic content | P2 | open | – |
| PKG-GENERIC-011 | Generic repository as cross-format copy and move target | P1 | open | – |

## Ansible collection format (PKG-ANSIBLE)

Catalog page: [formats/ansible.md](../features/formats/ansible.md) · specified by priority: P1 0 of 13

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-ANSIBLE-001 | Ansible repositories in all three repository classes | P1 | open | – |
| PKG-ANSIBLE-002 | Galaxy v3 API per repository | P1 | open | – |
| PKG-ANSIBLE-003 | Publish collections with ansible-galaxy | P1 | open | – |
| PKG-ANSIBLE-004 | Deploy collection archives through the UI or plain HTTP | P1 | open | – |
| PKG-ANSIBLE-005 | Install collections | P1 | open | – |
| PKG-ANSIBLE-006 | Collection metadata calculation and recalculation | P1 | open | – |
| PKG-ANSIBLE-007 | Install legacy roles through remote repositories | P1 | open | – |
| PKG-ANSIBLE-008 | No browsing of uncached remote content | P1 | open | – |
| PKG-ANSIBLE-009 | Remote repositories for Ansible Galaxy | P1 | open | – |
| PKG-ANSIBLE-010 | Proxy Red Hat Ansible Automation Hub | P1 | open | – |
| PKG-ANSIBLE-011 | Virtual Ansible repositories | P1 | open | – |
| PKG-ANSIBLE-012 | ansible.cfg configuration for collections | P1 | open | – |
| PKG-ANSIBLE-013 | Credentials for role installation | P1 | open | – |

## Alpine Linux package format (PKG-APK)

Catalog page: [formats/alpine.md](../features/formats/alpine.md) · specified by priority: P2 0 of 14

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-APK-001 | Alpine repositories in all three repository classes | P2 | open | – |
| PKG-APK-002 | Branch, repository and architecture structure | P2 | open | – |
| PKG-APK-003 | Automatic index calculation for local repositories | P2 | open | – |
| PKG-APK-004 | Recalculate Alpine index | P2 | open | – |
| PKG-APK-005 | Package metadata as searchable properties | P2 | open | – |
| PKG-APK-006 | Index signing with an RSA key pair | P2 | open | – |
| PKG-APK-007 | Index re-signing in virtual repositories | P2 | open | – |
| PKG-APK-008 | RSA key pairs in the key store | P2 | open | – |
| PKG-APK-009 | Public key for clients | P2 | open | – |
| PKG-APK-010 | Unsigned operation with untrusted mode | P2 | open | – |
| PKG-APK-011 | Remote Alpine repositories | P2 | open | – |
| PKG-APK-012 | Deploy Alpine packages | P2 | open | – |
| PKG-APK-013 | apk client configuration | P2 | open | – |
| PKG-APK-014 | Companion CLI integration for apk | P2 | open | – |

## Bazel modules package format (PKG-BAZEL)

Catalog page: [formats/bazel.md](../features/formats/bazel.md) · specified by priority: P1 0 of 1 · P2 0 of 11

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-BAZEL-001 | Bazel module repositories | P2 | open | – |
| PKG-BAZEL-002 | Registry URL | P2 | open | – |
| PKG-BAZEL-003 | Index registry layout | P2 | open | – |
| PKG-BAZEL-004 | Source description and source archives | P2 | open | – |
| PKG-BAZEL-005 | Client authentication through netrc | P2 | open | – |
| PKG-BAZEL-006 | Registry configuration | P2 | open | – |
| PKG-BAZEL-007 | Publish a module version | P2 | open | – |
| PKG-BAZEL-008 | Proxying the Bazel Central Registry | P2 | open | – |
| PKG-BAZEL-009 | Virtual Bazel module repositories | P2 | open | – |
| PKG-BAZEL-010 | Generic repository as Bazel HTTP remote cache | P2 | open | – |
| PKG-BAZEL-011 | Maven repositories for Bazel JVM dependencies | P1 | open | – |
| PKG-BAZEL-012 | Limitations | P2 | open | – |

## Bower package format (PKG-BOWER)

Catalog page: [formats/bower.md](../features/formats/bower.md) · specified by priority: P2 0 of 13 · Later 0 of 1

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-BOWER-001 | Bower repositories in all three repository classes | P2 | open | – |
| PKG-BOWER-002 | Registry URL of a repository | P2 | open | – |
| PKG-BOWER-003 | Package lookup | P2 | open | – |
| PKG-BOWER-004 | Package list and search | P2 | open | – |
| PKG-BOWER-005 | Register and unregister packages | P2 | open | – |
| PKG-BOWER-006 | Resolver plugin and client configuration | P2 | open | – |
| PKG-BOWER-007 | Shorthand resolver for GitHub dependencies | P2 | open | – |
| PKG-BOWER-008 | Clients below Bower 1.5 | Later | open | – |
| PKG-BOWER-009 | Deploy packages | P2 | open | – |
| PKG-BOWER-010 | Recalculate the Bower index | P2 | open | – |
| PKG-BOWER-011 | Remote Bower repository settings | P2 | open | – |
| PKG-BOWER-012 | Registering private Git repositories | P2 | open | – |
| PKG-BOWER-013 | Virtual Bower repositories | P2 | open | – |
| PKG-BOWER-014 | External dependency rewrite | P2 | open | – |

## Cargo (Rust crates) format (PKG-CARGO)

Catalog page: [formats/cargo.md](../features/formats/cargo.md) · specified by priority: P2 0 of 13 · Later 0 of 1

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-CARGO-001 | Cargo repositories: local and remote only | P2 | open | – |
| PKG-CARGO-002 | Sparse registry index of a repository | P2 | open | – |
| PKG-CARGO-003 | Sparse index setting and the retired Git index | P2 | open | – |
| PKG-CARGO-004 | Cargo client configuration | P2 | open | – |
| PKG-CARGO-005 | Anonymous download and search for older clients | P2 | open | – |
| PKG-CARGO-006 | Publish through the Cargo web API | P2 | open | – |
| PKG-CARGO-007 | Deploy a crate file by path | P2 | open | – |
| PKG-CARGO-008 | Yank and unyank | P2 | open | – |
| PKG-CARGO-009 | Cargo web API surface | P2 | open | – |
| PKG-CARGO-010 | Remote repository for crates.io | P2 | open | – |
| PKG-CARGO-011 | Chained Cargo remote | P2 | open | – |
| PKG-CARGO-012 | Recalculate the Cargo index | P2 | open | – |
| PKG-CARGO-013 | Source replacement across federated members | Later | open | – |
| PKG-CARGO-014 | Companion client setup and build metadata for Cargo | P2 | open | – |

## Chef cookbook format (PKG-CHEF)

Catalog page: [formats/chef.md](../features/formats/chef.md) · specified by priority: P2 0 of 10

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-CHEF-001 | Chef repositories in all three repository classes | P2 | open | – |
| PKG-CHEF-002 | Supermarket endpoint per repository | P2 | open | – |
| PKG-CHEF-003 | Cookbook metadata calculation | P2 | open | – |
| PKG-CHEF-004 | Remote Chef repositories | P2 | open | – |
| PKG-CHEF-005 | Virtual Chef repositories | P2 | open | – |
| PKG-CHEF-006 | Publish cookbooks | P2 | open | – |
| PKG-CHEF-007 | Install, download and inspect cookbooks | P2 | open | – |
| PKG-CHEF-008 | Unshare a cookbook version | P2 | open | – |
| PKG-CHEF-009 | Knife configuration and authentication | P2 | open | – |
| PKG-CHEF-010 | Berkshelf configuration | P2 | open | – |

## CocoaPods package format (PKG-PODS)

Catalog page: [formats/cocoapods.md](../features/formats/cocoapods.md) · specified by priority: P2 0 of 14 · Later 0 of 1

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-PODS-001 | CocoaPods repositories in all three repository classes | P2 | open | – |
| PKG-PODS-002 | Specs-source URL of a repository | P2 | open | – |
| PKG-PODS-003 | CDN specs index | P2 | open | – |
| PKG-PODS-004 | Pod archives and served podspecs | P2 | open | – |
| PKG-PODS-005 | Client setup with a CDN source | P2 | open | – |
| PKG-PODS-006 | Authentication and anonymous access | P2 | open | – |
| PKG-PODS-007 | Legacy Git-based specs index with a client plugin | Later | open | – |
| PKG-PODS-008 | Deploy pods | P2 | open | – |
| PKG-PODS-009 | Recalculate the CocoaPods index | P2 | open | – |
| PKG-PODS-010 | CDN readiness check | P2 | open | – |
| PKG-PODS-011 | URL base for generated URLs | P2 | open | – |
| PKG-PODS-012 | Remote CocoaPods repository settings | P2 | open | – |
| PKG-PODS-013 | Source archives from Git providers | P2 | open | – |
| PKG-PODS-014 | Virtual CocoaPods repositories | P2 | open | – |
| PKG-PODS-015 | Unsupported commands and content | P2 | open | – |

## PHP Composer format (PKG-COMPOSER)

Catalog page: [formats/composer.md](../features/formats/composer.md) · specified by priority: P2 0 of 11

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-COMPOSER-001 | Composer repositories in all three repository classes | P2 | open | – |
| PKG-COMPOSER-002 | Composer protocol endpoint of a repository | P2 | open | – |
| PKG-COMPOSER-003 | Composer client configuration | P2 | open | – |
| PKG-COMPOSER-004 | Install packages | P2 | open | – |
| PKG-COMPOSER-005 | Deploy a package archive | P2 | open | – |
| PKG-COMPOSER-006 | Metadata calculation for local repositories | P2 | open | – |
| PKG-COMPOSER-007 | Remote Composer repository: metadata and download addresses | P2 | open | – |
| PKG-COMPOSER-008 | Download providers and custom download URL template | P2 | open | – |
| PKG-COMPOSER-009 | Drupal upstreams | P2 | open | – |
| PKG-COMPOSER-010 | Virtual Composer repositories | P2 | open | – |
| PKG-COMPOSER-011 | Source installs under upstream curation | P2 | open | – |

## Conan (C/C++) format (PKG-CONAN)

Catalog page: [formats/conan.md](../features/formats/conan.md) · specified by priority: P2 0 of 13 · Later 0 of 1

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-CONAN-001 | Conan repositories in all three repository classes | P2 | open | – |
| PKG-CONAN-002 | Conan server protocol of a repository | P2 | open | – |
| PKG-CONAN-003 | Conan 2 client configuration and login | P2 | open | – |
| PKG-CONAN-004 | Conan 1 legacy client | P2 | open | – |
| PKG-CONAN-005 | Anonymous access and forced authentication | P2 | open | – |
| PKG-CONAN-006 | Upload recipes and binary packages | P2 | open | – |
| PKG-CONAN-007 | Install packages | P2 | open | – |
| PKG-CONAN-008 | Revisions and storage layout | P2 | open | – |
| PKG-CONAN-009 | Migration of revisionless packages | P2 | open | – |
| PKG-CONAN-010 | Recalculate Conan metadata | P2 | open | – |
| PKG-CONAN-011 | Remote Conan repositories | P2 | open | – |
| PKG-CONAN-012 | Virtual Conan repositories | P2 | open | – |
| PKG-CONAN-013 | Scanning Conan packages and C/C++ builds | Later | open | – |
| PKG-CONAN-014 | Companion client for Conan | P2 | open | – |

## Conda package format (PKG-CONDA)

Catalog page: [formats/conda.md](../features/formats/conda.md) · specified by priority: P2 0 of 15

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-CONDA-001 | Conda repositories in all three repository classes | P2 | open | – |
| PKG-CONDA-002 | Channel URL, platform subdirectories and sub-channels | P2 | open | – |
| PKG-CONDA-003 | Client authentication | P2 | open | – |
| PKG-CONDA-004 | Deploy a package to its subdirectory path | P2 | open | – |
| PKG-CONDA-005 | Automatic index calculation | P2 | open | – |
| PKG-CONDA-006 | Recalculate conda metadata | P2 | open | – |
| PKG-CONDA-007 | Index calculation worker pool | P2 | open | – |
| PKG-CONDA-008 | Channel index documents | P2 | open | – |
| PKG-CONDA-009 | Sharded channel index (CEP 16) | P2 | open | – |
| PKG-CONDA-010 | Package information and metadata search | P2 | open | – |
| PKG-CONDA-011 | Proxying an upstream channel | P2 | open | – |
| PKG-CONDA-012 | Aggregated channel index | P2 | open | – |
| PKG-CONDA-013 | conda client configuration | P2 | open | – |
| PKG-CONDA-014 | Client setup instructions in the UI | P2 | open | – |
| PKG-CONDA-015 | Channel endpoints | P2 | open | – |

## CRAN package format (PKG-CRAN)

Catalog page: [formats/cran.md](../features/formats/cran.md) · specified by priority: P2 0 of 16

<details>
<summary>16 features: 0 specified, 16 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-CRAN-001 | CRAN repositories in all three repository classes | P2 | open | – |
| PKG-CRAN-002 | Repository URL and directory layout | P2 | open | – |
| PKG-CRAN-003 | Client authentication | P2 | open | – |
| PKG-CRAN-004 | Deploy a source package | P2 | open | – |
| PKG-CRAN-005 | Deploy a binary package | P2 | open | – |
| PKG-CRAN-006 | Deploy through a virtual repository | P2 | open | – |
| PKG-CRAN-007 | Automatic index calculation | P2 | open | – |
| PKG-CRAN-008 | Recalculate CRAN metadata | P2 | open | – |
| PKG-CRAN-009 | Archived versions in the standard location | P2 | open | – |
| PKG-CRAN-010 | Move archives to the standard location | P2 | open | – |
| PKG-CRAN-011 | Package information in the UI | P2 | open | – |
| PKG-CRAN-012 | Proxying an upstream CRAN mirror | P2 | open | – |
| PKG-CRAN-013 | Aggregated index in virtual repositories | P2 | open | – |
| PKG-CRAN-014 | R client usage | P2 | open | – |
| PKG-CRAN-015 | Client setup instructions in the UI | P2 | open | – |
| PKG-CRAN-016 | Repository paths R requests | P2 | open | – |

</details>

## Debian package format (PKG-DEB)

Catalog page: [formats/debian.md](../features/formats/debian.md) · specified by priority: P2 0 of 27 · Later 0 of 1

<details>
<summary>28 features: 0 specified, 28 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-DEB-001 | Debian repositories in all three repository classes | P2 | open | – |
| PKG-DEB-002 | Automatic layout and package coordinates | P2 | open | – |
| PKG-DEB-003 | Legacy trivial (flat) layout | Later | open | – |
| PKG-DEB-004 | Deploy a binary package with its coordinates | P2 | open | – |
| PKG-DEB-005 | Several coordinate sets for one package | P2 | open | – |
| PKG-DEB-006 | Architecture-independent packages | P2 | open | – |
| PKG-DEB-007 | Source packages | P2 | open | – |
| PKG-DEB-008 | Automatic index calculation for local repositories | P2 | open | – |
| PKG-DEB-009 | Package metadata validation and index status | P2 | open | – |
| PKG-DEB-010 | Package metadata as searchable properties | P2 | open | – |
| PKG-DEB-011 | Index compression formats | P2 | open | – |
| PKG-DEB-012 | Debug-symbol package indexing | P2 | open | – |
| PKG-DEB-013 | Acquire-by-hash | P2 | open | – |
| PKG-DEB-014 | MD5 checksums in the Packages index | P2 | open | – |
| PKG-DEB-015 | Origin and Label of the Release file | P2 | open | – |
| PKG-DEB-016 | Recalculate Debian metadata | P2 | open | – |
| PKG-DEB-017 | Release signing with GPG | P2 | open | – |
| PKG-DEB-018 | Signing keys per repository and key rotation | P2 | open | – |
| PKG-DEB-019 | Instance-wide default GPG key | P2 | open | – |
| PKG-DEB-020 | Public key for client trust | P2 | open | – |
| PKG-DEB-021 | Remote Debian repositories | P2 | open | – |
| PKG-DEB-022 | Coordinate calculation for cached remote packages | P2 | open | – |
| PKG-DEB-023 | Virtual Debian repositories | P2 | open | – |
| PKG-DEB-024 | Create a Debian snapshot | P2 | open | – |
| PKG-DEB-025 | Resolve and delete snapshots | P2 | open | – |
| PKG-DEB-026 | APT source configuration | P2 | open | – |
| PKG-DEB-027 | Authenticated APT access | P2 | open | – |
| PKG-DEB-028 | Companion CLI integration for APT | P2 | open | – |

</details>

## Git LFS format (PKG-LFS)

Catalog page: [formats/git-lfs.md](../features/formats/git-lfs.md) · specified by priority: P2 0 of 9

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-LFS-001 | Git LFS repositories in all three repository classes | P2 | open | – |
| PKG-LFS-002 | LFS endpoint of a repository | P2 | open | – |
| PKG-LFS-003 | Batch API | P2 | open | – |
| PKG-LFS-004 | Object upload, download and verification (basic transfer adapter) | P2 | open | – |
| PKG-LFS-005 | Object storage layout and metadata | P2 | open | – |
| PKG-LFS-006 | Deploy an LFS object through the UI | P2 | open | – |
| PKG-LFS-007 | File locking | P2 | open | – |
| PKG-LFS-008 | Client authentication | P2 | open | – |
| PKG-LFS-009 | Cleanup of unreferenced LFS objects | P2 | open | – |

## Gradle, Ivy, sbt and Eclipse P2 formats (PKG-JVM)

Catalog page: [formats/jvm-other.md](../features/formats/jvm-other.md) · specified by priority: P1 0 of 4 · P2 0 of 19 · Later 0 of 1

<details>
<summary>24 features: 0 specified, 24 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-JVM-001 | Maven-family formats and their shared settings | P1 | open | – |
| PKG-JVM-002 | Gradle repositories | P1 | open | – |
| PKG-JVM-003 | Gradle client setup instructions | P1 | open | – |
| PKG-JVM-004 | Gradle cache layout | P2 | open | – |
| PKG-JVM-005 | Gradle module metadata files | P1 | open | – |
| PKG-JVM-006 | Wrapping Gradle with the companion CLI | P2 | open | – |
| PKG-JVM-007 | Build-tool plugin for Gradle | Later | open | – |
| PKG-JVM-008 | Ivy repositories | P2 | open | – |
| PKG-JVM-009 | Ivy layout and integration revisions | P2 | open | – |
| PKG-JVM-010 | Ivy dynamic revisions against remote repositories | P2 | open | – |
| PKG-JVM-011 | Ivy settings in the client setup instructions | P2 | open | – |
| PKG-JVM-012 | sbt repositories | P2 | open | – |
| PKG-JVM-013 | sbt layouts | P2 | open | – |
| PKG-JVM-014 | sbt client configuration | P2 | open | – |
| PKG-JVM-015 | Separate Maven and Ivy proxies for sbt | P2 | open | – |
| PKG-JVM-016 | Publishing sbt snapshots | P2 | open | – |
| PKG-JVM-017 | P2 repositories: remote and virtual only | P2 | open | – |
| PKG-JVM-018 | Remote P2 repository URL | P2 | open | – |
| PKG-JVM-019 | P2 virtual repository composition | P2 | open | – |
| PKG-JVM-020 | One remote repository per upstream base URL | P2 | open | – |
| PKG-JVM-021 | P2 metadata aggregation and caching | P2 | open | – |
| PKG-JVM-022 | Force authentication for P2 | P2 | open | – |
| PKG-JVM-023 | Eclipse and Tycho client configuration | P2 | open | – |
| PKG-JVM-024 | P2 in the configuration interfaces | P2 | open | – |

</details>

## Hex package format (Elixir and Erlang) (PKG-HEX)

Catalog page: [formats/hex.md](../features/formats/hex.md) · specified by priority: P2 0 of 18

<details>
<summary>18 features: 0 specified, 18 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-HEX-001 | Hex repositories in all three repository classes | P2 | open | – |
| PKG-HEX-002 | Repository URL and client-side repository name | P2 | open | – |
| PKG-HEX-003 | Registry resources | P2 | open | – |
| PKG-HEX-004 | Package tarballs | P2 | open | – |
| PKG-HEX-005 | Installs metadata file | P2 | open | – |
| PKG-HEX-006 | Organization (private) packages | P2 | open | – |
| PKG-HEX-007 | Repository signing key pair | P2 | open | – |
| PKG-HEX-008 | Upstream public key on remote repositories | P2 | open | – |
| PKG-HEX-009 | Public key download for clients | P2 | open | – |
| PKG-HEX-010 | Mix client setup per repository class | P2 | open | – |
| PKG-HEX-011 | Declaring dependencies | P2 | open | – |
| PKG-HEX-012 | Supported clients, versions and commands | P2 | open | – |
| PKG-HEX-013 | OAuth token exchange of newer Hex clients | P2 | open | – |
| PKG-HEX-014 | Deploy package tarballs | P2 | open | – |
| PKG-HEX-015 | Local repository layout and metadata generation | P2 | open | – |
| PKG-HEX-016 | Remote Hex repositories | P2 | open | – |
| PKG-HEX-017 | Virtual Hex repositories | P2 | open | – |
| PKG-HEX-018 | Per-package permissions on layout folders | P2 | open | – |

</details>

## Hugging Face models and datasets (PKG-HF)

Catalog page: [formats/huggingface.md](../features/formats/huggingface.md) · specified by priority: P2 0 of 15 · Later 0 of 3

<details>
<summary>18 features: 0 specified, 18 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-HF-001 | Hugging Face repositories in all three repository classes | P2 | open | – |
| PKG-HF-002 | Hub endpoint of a repository | P2 | open | – |
| PKG-HF-003 | Client authentication | P2 | open | – |
| PKG-HF-004 | Naming rules for organizations, names and revisions | P2 | open | – |
| PKG-HF-005 | Upload models and datasets | P2 | open | – |
| PKG-HF-006 | Download whole repositories (snapshot) | P2 | open | – |
| PKG-HF-007 | Download individual files through ML libraries | P2 | open | – |
| PKG-HF-008 | Client configuration and timeouts | P2 | open | – |
| PKG-HF-009 | Revision-based storage layout | P2 | open | – |
| PKG-HF-010 | Legacy revision layout | Later | open | – |
| PKG-HF-011 | Layout upgrade and restore | Later | open | – |
| PKG-HF-012 | Proxying the public Hub | P2 | open | – |
| PKG-HF-013 | Private upstream repositories | P2 | open | – |
| PKG-HF-014 | Virtual repositories | P2 | open | – |
| PKG-HF-015 | Xet large-file protocol | P2 | open | – |
| PKG-HF-016 | Companion CLI download and upload with build metadata | P2 | open | – |
| PKG-HF-017 | Model scanning and upstream curation | Later | open | – |
| PKG-HF-018 | Hub API endpoints used by the supported clients | P2 | open | – |

</details>

## LuaRocks package format (PKG-LUA)

Catalog page: [formats/luarocks.md](../features/formats/luarocks.md) · specified by priority: P2 0 of 17 · Later 0 of 1

<details>
<summary>18 features: 0 specified, 18 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-LUA-001 | LuaRocks repositories in all repository classes | P2 | open | – |
| PKG-LUA-002 | Rocks-server URL | P2 | open | – |
| PKG-LUA-003 | Manifest files | P2 | open | – |
| PKG-LUA-004 | Rocks, rockspecs and package identity | P2 | open | – |
| PKG-LUA-005 | Upload protocol | P2 | open | – |
| PKG-LUA-006 | Client configuration for installs | P2 | open | – |
| PKG-LUA-007 | Install authentication | P2 | open | – |
| PKG-LUA-008 | Upload configuration and authentication | P2 | open | – |
| PKG-LUA-009 | Install commands | P2 | open | – |
| PKG-LUA-010 | Automatic manifest maintenance | P2 | open | – |
| PKG-LUA-011 | Recalculate the LuaRocks index | P2 | open | – |
| PKG-LUA-012 | Package views for LuaRocks | P2 | open | – |
| PKG-LUA-013 | Remote LuaRocks repositories | P2 | open | – |
| PKG-LUA-014 | Chained remote LuaRocks repositories | P2 | open | – |
| PKG-LUA-015 | Federated LuaRocks repositories | Later | open | – |
| PKG-LUA-016 | Virtual LuaRocks repositories | P2 | open | – |
| PKG-LUA-017 | Cleanup and archive policies for LuaRocks | P2 | open | – |
| PKG-LUA-018 | Limitations | P2 | open | – |

</details>

## Nix package format (PKG-NIX)

Catalog page: [formats/nix.md](../features/formats/nix.md) · specified by priority: P2 0 of 19 · Later 0 of 1

<details>
<summary>20 features: 0 specified, 20 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-NIX-001 | Nix repositories in all repository classes | P2 | open | – |
| PKG-NIX-002 | Binary-cache URL of a repository | P2 | open | – |
| PKG-NIX-003 | Cache information document | P2 | open | – |
| PKG-NIX-004 | Store-object metadata | P2 | open | – |
| PKG-NIX-005 | NAR archives | P2 | open | – |
| PKG-NIX-006 | Upload build outputs with `nix copy` | P2 | open | – |
| PKG-NIX-007 | Channel resolution | P2 | open | – |
| PKG-NIX-008 | Publish a channel expression snapshot | P2 | open | – |
| PKG-NIX-009 | Ed25519 signing of cache entries | P2 | open | – |
| PKG-NIX-010 | Client trust configuration | P2 | open | – |
| PKG-NIX-011 | Substituter configuration and priority | P2 | open | – |
| PKG-NIX-012 | Authentication methods | P2 | open | – |
| PKG-NIX-013 | Installing packages | P2 | open | – |
| PKG-NIX-014 | Package identification from store paths | P2 | open | – |
| PKG-NIX-015 | Naming and versioning guidance | P2 | open | – |
| PKG-NIX-016 | Remote Nix repositories | P2 | open | – |
| PKG-NIX-017 | Virtual Nix repositories | P2 | open | – |
| PKG-NIX-018 | Federated Nix repositories | Later | open | – |
| PKG-NIX-019 | Build metadata for Nix through the companion client | P2 | open | – |
| PKG-NIX-020 | Limitations | P2 | open | – |

</details>

## Opkg package format (PKG-OPKG)

Catalog page: [formats/opkg.md](../features/formats/opkg.md) · specified by priority: P2 0 of 8

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-OPKG-001 | Opkg repositories | P2 | open | – |
| PKG-OPKG-002 | Feeds at arbitrary paths | P2 | open | – |
| PKG-OPKG-003 | Automatic index calculation per feed | P2 | open | – |
| PKG-OPKG-004 | Recalculate Opkg metadata | P2 | open | – |
| PKG-OPKG-005 | Index signing with GPG | P2 | open | – |
| PKG-OPKG-006 | Remote Opkg repositories | P2 | open | – |
| PKG-OPKG-007 | Deploy Opkg packages | P2 | open | – |
| PKG-OPKG-008 | opkg client configuration | P2 | open | – |

## pub package format (Dart and Flutter) (PKG-PUB)

Catalog page: [formats/pub.md](../features/formats/pub.md) · specified by priority: P2 0 of 18 · Later 0 of 1

<details>
<summary>19 features: 0 specified, 19 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-PUB-001 | pub repositories in all three repository classes | P2 | open | – |
| PKG-PUB-002 | Hosted URL of a repository | P2 | open | – |
| PKG-PUB-003 | API version header, errors and retries | P2 | open | – |
| PKG-PUB-004 | List all versions of a package | P2 | open | – |
| PKG-PUB-005 | Archive download | P2 | open | – |
| PKG-PUB-006 | Publish a package version | P2 | open | – |
| PKG-PUB-007 | Publishing with an attestation | P2 | open | – |
| PKG-PUB-008 | Security advisories for a package | Later | open | – |
| PKG-PUB-009 | Deprecated per-version endpoints | P2 | open | – |
| PKG-PUB-010 | Discontinued packages and retracted versions | P2 | open | – |
| PKG-PUB-011 | Token authentication | P2 | open | – |
| PKG-PUB-012 | Client setup for Dart and Flutter | P2 | open | – |
| PKG-PUB-013 | Storage layout and per-package index | P2 | open | – |
| PKG-PUB-014 | Deploy through the UI or file upload | P2 | open | – |
| PKG-PUB-015 | SemVer 2.0 versions | P2 | open | – |
| PKG-PUB-016 | Recalculate pub repository metadata | P2 | open | – |
| PKG-PUB-017 | Remote pub repositories | P2 | open | – |
| PKG-PUB-018 | Virtual pub repositories | P2 | open | – |
| PKG-PUB-019 | Supported client versions | P2 | open | – |

</details>

## Puppet module format (PKG-PUPPET)

Catalog page: [formats/puppet.md](../features/formats/puppet.md) · specified by priority: P2 0 of 11 · Later 0 of 1

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-PUPPET-001 | Puppet repositories in all three repository classes | P2 | open | – |
| PKG-PUPPET-002 | Forge v3 API per repository | P2 | open | – |
| PKG-PUPPET-003 | Free layout and module archive deploy | P2 | open | – |
| PKG-PUPPET-004 | Module metadata calculation and properties | P2 | open | – |
| PKG-PUPPET-005 | Asynchronous search availability | P2 | open | – |
| PKG-PUPPET-006 | Remote Puppet repositories | P2 | open | – |
| PKG-PUPPET-007 | Virtual Puppet repositories | P2 | open | – |
| PKG-PUPPET-008 | Puppet CLI configuration | P2 | open | – |
| PKG-PUPPET-009 | r10k configuration | P2 | open | – |
| PKG-PUPPET-010 | librarian-puppet configuration | P2 | open | – |
| PKG-PUPPET-011 | Reverse-proxy workaround for old Puppet clients | Later | open | – |
| PKG-PUPPET-012 | Anonymous and authenticated access | P2 | open | – |

## RPM package format (PKG-RPM)

Catalog page: [formats/rpm.md](../features/formats/rpm.md) · specified by priority: P2 0 of 18

<details>
<summary>18 features: 0 specified, 18 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-RPM-001 | RPM repositories in all three repository classes | P2 | open | – |
| PKG-RPM-002 | Metadata folder depth | P2 | open | – |
| PKG-RPM-003 | Automatic metadata calculation | P2 | open | – |
| PKG-RPM-004 | Generated repodata files | P2 | open | – |
| PKG-RPM-005 | File-list indexing | P2 | open | – |
| PKG-RPM-006 | Recalculate YUM metadata | P2 | open | – |
| PKG-RPM-007 | YUM group files | P2 | open | – |
| PKG-RPM-008 | Module metadata | P2 | open | – |
| PKG-RPM-009 | Weak-dependency tags | P2 | open | – |
| PKG-RPM-010 | SHA-256 checksums in repomd.xml | P2 | open | – |
| PKG-RPM-011 | RPM package settings screen | P2 | open | – |
| PKG-RPM-012 | Metadata signing with GPG | P2 | open | – |
| PKG-RPM-013 | Signing keys for RPM repositories | P2 | open | – |
| PKG-RPM-014 | Remote RPM repositories | P2 | open | – |
| PKG-RPM-015 | Virtual RPM repositories | P2 | open | – |
| PKG-RPM-016 | Deploy RPM packages | P2 | open | – |
| PKG-RPM-017 | YUM and DNF client configuration | P2 | open | – |
| PKG-RPM-018 | Client authentication | P2 | open | – |

</details>

## RubyGems format (PKG-GEMS)

Catalog page: [formats/rubygems.md](../features/formats/rubygems.md) · specified by priority: P2 0 of 18

<details>
<summary>18 features: 0 specified, 18 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-GEMS-001 | RubyGems repositories in all three repository classes | P2 | open | – |
| PKG-GEMS-002 | RubyGems endpoint of a repository | P2 | open | – |
| PKG-GEMS-003 | Gem source configuration | P2 | open | – |
| PKG-GEMS-004 | Push credentials | P2 | open | – |
| PKG-GEMS-005 | Bundler configuration | P2 | open | – |
| PKG-GEMS-006 | Push a gem | P2 | open | – |
| PKG-GEMS-007 | Deploy a gem file by path | P2 | open | – |
| PKG-GEMS-008 | Yank a gem version | P2 | open | – |
| PKG-GEMS-009 | Gem information | P2 | open | – |
| PKG-GEMS-010 | Search | P2 | open | – |
| PKG-GEMS-011 | Dependency API | P2 | open | – |
| PKG-GEMS-012 | Version list of a gem | P2 | open | – |
| PKG-GEMS-013 | Automatic spec index maintenance | P2 | open | – |
| PKG-GEMS-014 | Reindex and update-index operations | P2 | open | – |
| PKG-GEMS-015 | Compact index for Bundler | P2 | open | – |
| PKG-GEMS-016 | Virtual RubyGems repositories | P2 | open | – |
| PKG-GEMS-017 | Remote RubyGems repository | P2 | open | – |
| PKG-GEMS-018 | Companion client for RubyGems and Bundler | P2 | open | – |

</details>

## Swift package format (PKG-SWIFT)

Catalog page: [formats/swift.md](../features/formats/swift.md) · specified by priority: P2 0 of 18

<details>
<summary>18 features: 0 specified, 18 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-SWIFT-001 | Swift repositories in all three repository classes | P2 | open | – |
| PKG-SWIFT-002 | Registry URL and package identity | P2 | open | – |
| PKG-SWIFT-003 | API version negotiation, media types and errors | P2 | open | – |
| PKG-SWIFT-004 | List package releases | P2 | open | – |
| PKG-SWIFT-005 | Fetch release metadata | P2 | open | – |
| PKG-SWIFT-006 | Fetch the package manifest | P2 | open | – |
| PKG-SWIFT-007 | Download a source archive | P2 | open | – |
| PKG-SWIFT-008 | Look up package identifiers by source URL | P2 | open | – |
| PKG-SWIFT-009 | Publish a release | P2 | open | – |
| PKG-SWIFT-010 | Client authentication | P2 | open | – |
| PKG-SWIFT-011 | Client setup | P2 | open | – |
| PKG-SWIFT-012 | Storage layout and index calculation | P2 | open | – |
| PKG-SWIFT-013 | Recalculate the Swift index | P2 | open | – |
| PKG-SWIFT-014 | Proxying a Swift registry | P2 | open | – |
| PKG-SWIFT-015 | External dependencies on remote repositories | P2 | open | – |
| PKG-SWIFT-016 | Virtual Swift repositories | P2 | open | – |
| PKG-SWIFT-017 | Replacing source-control dependencies with registry identities | P2 | open | – |
| PKG-SWIFT-018 | Supported client versions | P2 | open | – |

</details>

## Terraform and OpenTofu formats (PKG-TF)

Catalog page: [formats/terraform.md](../features/formats/terraform.md) · specified by priority: P2 0 of 23

<details>
<summary>23 features: 0 specified, 23 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-TF-001 | Registry repositories for modules and providers | P2 | open | – |
| PKG-TF-002 | OpenTofu clients | P2 | open | – |
| PKG-TF-003 | State backend repository type | P2 | open | – |
| PKG-TF-004 | Module layout and deploy | P2 | open | – |
| PKG-TF-005 | Module source addresses and module registry protocol | P2 | open | – |
| PKG-TF-006 | Module source host override | P2 | open | – |
| PKG-TF-007 | Provider registry protocol (origin registry) | P2 | open | – |
| PKG-TF-008 | Provider network mirror | P2 | open | – |
| PKG-TF-009 | Provider package layout and deploy | P2 | open | – |
| PKG-TF-010 | Provider signing key | P2 | open | – |
| PKG-TF-011 | Remote Terraform repositories | P2 | open | – |
| PKG-TF-012 | Token-authenticated upstream registries | P2 | open | – |
| PKG-TF-013 | Include and exclude patterns for providers | P2 | open | – |
| PKG-TF-014 | Virtual Terraform repositories | P2 | open | – |
| PKG-TF-015 | Recalculate Terraform index | P2 | open | – |
| PKG-TF-016 | Interactive client login | P2 | open | – |
| PKG-TF-017 | Manually configured client token | P2 | open | – |
| PKG-TF-018 | Remote state storage with workspaces | P2 | open | – |
| PKG-TF-019 | State locking | P2 | open | – |
| PKG-TF-020 | State encryption at rest | P2 | open | – |
| PKG-TF-021 | State history and state viewer | P2 | open | – |
| PKG-TF-022 | Backend client configuration | P2 | open | – |
| PKG-TF-023 | Module publishing with the companion CLI | P2 | open | – |

</details>

## Vagrant box format (PKG-VAGRANT)

Catalog page: [formats/vagrant.md](../features/formats/vagrant.md) · specified by priority: P2 0 of 4

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-VAGRANT-001 | Local Vagrant repositories | P2 | open | – |
| PKG-VAGRANT-002 | Deploy a box with name, provider and version | P2 | open | – |
| PKG-VAGRANT-003 | Box endpoint for `vagrant box add` | P2 | open | – |
| PKG-VAGRANT-004 | Client authentication and server URL | P2 | open | – |

## VCS archive format (PKG-VCS)

Catalog page: [formats/vcs.md](../features/formats/vcs.md) · specified by priority: P2 0 of 13

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-VCS-001 | VCS remote repositories | P2 | open | – |
| PKG-VCS-002 | Git provider selection | P2 | open | – |
| PKG-VCS-003 | Download path template for custom providers | P2 | open | – |
| PKG-VCS-004 | Cache layout of VCS archives | P2 | open | – |
| PKG-VCS-005 | List tags and list branches | P2 | open | – |
| PKG-VCS-006 | Download the Git references file | P2 | open | – |
| PKG-VCS-007 | Download a tag archive | P2 | open | – |
| PKG-VCS-008 | Download a branch archive | P2 | open | – |
| PKG-VCS-009 | Download a commit archive | P2 | open | – |
| PKG-VCS-010 | Download a release archive | P2 | open | – |
| PKG-VCS-011 | Download a single file from a tag or branch | P2 | open | – |
| PKG-VCS-012 | Subgroup repository variants | P2 | open | – |
| PKG-VCS-013 | Private and redirecting upstreams | P2 | open | – |

## Machine-learning models and model-serving containers (PKG-ML)

Catalog page: [formats/ml-models.md](../features/formats/ml-models.md) · specified by priority: P2 0 of 15 · Later 0 of 3 · Out of scope 0 of 1

<details>
<summary>19 features: 0 specified, 19 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-ML-001 | Machine-learning model repositories | P2 | open | – |
| PKG-ML-002 | Model storage layout | P2 | open | – |
| PKG-ML-003 | Log a single-file model | P2 | open | – |
| PKG-ML-004 | Load a single-file model | P2 | open | – |
| PKG-ML-005 | Framework-aware models | P2 | open | – |
| PKG-ML-006 | Model information lookup | P2 | open | – |
| PKG-ML-007 | Inference code packaging rules | P2 | open | – |
| PKG-ML-008 | Model metadata and search | P2 | open | – |
| PKG-ML-009 | SDK configuration and client setup | P2 | open | – |
| PKG-ML-010 | Model-serving containers: two-repository setup | P2 | open | – |
| PKG-ML-011 | NIM model remote repository | P2 | open | – |
| PKG-ML-012 | Running a NIM container through the system | P2 | open | – |
| PKG-ML-013 | NIM cache layout | P2 | open | – |
| PKG-ML-014 | Cached model properties and search | P2 | open | – |
| PKG-ML-015 | Model versions and tags | P2 | open | – |
| PKG-ML-016 | Model catalog with per-project allow-listing | Later | open | – |
| PKG-ML-017 | Gateway for external model APIs | Later | open | – |
| PKG-ML-018 | Managed model build, deployment and monitoring | Out of scope | open | – |
| PKG-ML-019 | Model bill of materials | Later | open | – |

</details>

## AI agent assets and IDE extension formats (PKG-AI)

Catalog page: [formats/ai-and-ide.md](../features/formats/ai-and-ide.md) · specified by priority: P2 0 of 18 · Later 0 of 16 · Out of scope 0 of 3

<details>
<summary>37 features: 0 specified, 37 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| PKG-AI-001 | Skills repositories | P2 | open | – |
| PKG-AI-002 | Skill package identity and metadata extraction | P2 | open | – |
| PKG-AI-003 | Skill registry base URL and discovery document | P2 | open | – |
| PKG-AI-004 | Publish a skill through the registry protocol | P2 | open | – |
| PKG-AI-005 | Download, resolve and single-file operations for skills | P2 | open | – |
| PKG-AI-006 | List, detail, version and search operations for skills | P2 | open | – |
| PKG-AI-007 | Scan gate for skills | Later | open | – |
| PKG-AI-008 | Signed evidence for skills and plugins | Later | open | – |
| PKG-AI-009 | Skill installation targets and client workflow | P2 | open | – |
| PKG-AI-010 | Agent plugin repositories | P2 | open | – |
| PKG-AI-011 | Plugin bundle layouts | P2 | open | – |
| PKG-AI-012 | Marketplace index documents | P2 | open | – |
| PKG-AI-013 | Native marketplace connection for Claude Code | P2 | open | – |
| PKG-AI-014 | Plugin publish and install workflow | P2 | open | – |
| PKG-AI-015 | Agent package repositories for APM | P2 | open | – |
| PKG-AI-016 | APM v1 registry protocol | P2 | open | – |
| PKG-AI-017 | Build metadata for agent assets | P2 | open | – |
| PKG-AI-018 | VS Code compatible extension gallery proxy | P2 | open | – |
| PKG-AI-019 | Read-only reference token in the gallery URL | P2 | open | – |
| PKG-AI-020 | JetBrains plugin marketplace proxy | P2 | open | – |
| PKG-AI-021 | Curation of IDE extensions and plugins | Later | open | – |
| PKG-AI-022 | MCP server registry with project-scoped approval | Later | open | – |
| PKG-AI-023 | Register a local custom MCP server | Later | open | – |
| PKG-AI-024 | Register a remote custom MCP server | Later | open | – |
| PKG-AI-025 | MCP tool policies | Later | open | – |
| PKG-AI-026 | Client-side policy proxy for MCP servers | Later | open | – |
| PKG-AI-027 | Organization-wide lockdown of agent clients | Later | open | – |
| PKG-AI-028 | AI asset catalog | Later | open | – |
| PKG-AI-029 | Project-scoped registries with automatic repository provisioning | Later | open | – |
| PKG-AI-030 | Skill policies | Later | open | – |
| PKG-AI-031 | Skill waivers | Later | open | – |
| PKG-AI-032 | Roles for unscanned skills | Later | open | – |
| PKG-AI-033 | AI asset scanner with operator-supplied model credentials | Later | open | – |
| PKG-AI-036 | Plugin scanning and allowed-plugin status | Later | open | – |
| PKG-AI-034 | Hosted-service activation of AI features | Out of scope | open | – |
| PKG-AI-035 | Separately distributed agent integrations | Out of scope | open | – |
| PKG-AI-037 | Command-output compression for coding agents | Out of scope | open | – |

</details>

## Beyond the baseline: further formats and capabilities (BEYOND)

Catalog page: [beyond-baseline.md](../features/beyond-baseline.md) · specified by priority: P1 1 of 5 · P2 1 of 26 · Later 0 of 3

<details>
<summary>34 features: 2 specified, 32 open, 0 implemented</summary>

| ID | Feature | Priority | Status | Covered by |
|---|---|---|---|---|
| BEYOND-001 | Arch Linux pacman repositories | P2 | open | – |
| BEYOND-002 | Homebrew bottle, API and download mirrors | P2 | open | – |
| BEYOND-003 | winget REST sources | P2 | open | – |
| BEYOND-004 | Flatpak applications from OCI repositories | P2 | open | – |
| BEYOND-005 | OSTree archive repositories for Flatpak | Later | open | – |
| BEYOND-006 | Julia package server protocol | P2 | open | – |
| BEYOND-007 | Hackage repositories for Haskell | P2 | open | – |
| BEYOND-008 | opam repositories for OCaml | P2 | open | – |
| BEYOND-009 | CPAN repositories for Perl | P2 | open | – |
| BEYOND-010 | JSR (JavaScript Registry) | P2 | open | – |
| BEYOND-011 | Hosted editor extensions with the Open VSX API | P2 | open | – |
| BEYOND-012 | WebAssembly components on OCI | P2 | open | – |
| BEYOND-013 | Gentoo binary package hosts | P2 | open | – |
| BEYOND-014 | FreeBSD pkg repositories | P2 | open | – |
| BEYOND-015 | Referrers through remote and virtual repositories | P1 | open | – |
| BEYOND-016 | Referrer-aware lifecycle operations | P2 | open | – |
| BEYOND-017 | Artifact-type recognition | P2 | open | – |
| BEYOND-018 | Upstream selection by source namespace | P2 | open | – |
| BEYOND-019 | Signature verification gate for Sigstore-signed content | P2 | open | – |
| BEYOND-020 | SLSA provenance verification policy | P2 | open | – |
| BEYOND-021 | npm provenance for locally published packages | P1 | open | – |
| BEYOND-022 | PyPI digital attestations (PEP 740) | P2 | open | – |
| BEYOND-023 | Project status markers in the Python index (PEP 792) | P2 | open | – |
| BEYOND-024 | SBOM attachment store | P2 | open | – |
| BEYOND-025 | TUF-signed repository metadata | Later | open | – |
| BEYOND-026 | Checksum database for hosted Go modules | Later | open | – |
| BEYOND-034 | Notary Project signature verification for OCI artifacts | P2 | open | – |
| BEYOND-027 | Minimum release age for remote repositories | P1 | open | – |
| BEYOND-028 | Namespace claims against dependency confusion | P1 | specified | [R-REPO-023](../spec/repositories-and-proxy.md), [R-REPO-048](../spec/repositories-and-proxy.md) |
| BEYOND-029 | Cache pre-warming from lockfiles and SBOMs | P2 | open | – |
| BEYOND-030 | Per-tenant quotas and rate limits | P2 | specified | [R-API-016](../spec/api-and-ui.md), [R-EP-015](../spec/endpoints.md) |
| BEYOND-031 | Provider presets for further CI systems | P1 | open | – |
| BEYOND-032 | Kubernetes service-account token trust | P2 | open | – |
| BEYOND-033 | SPIFFE JWT-SVID trust | P2 | open | – |

</details>

## Requirements that cover no catalog feature

Requirements whose `Covers:` line says `trove-only`: trove's own design work that no
catalog feature measures. They count toward the requirement total, not toward any share.

<details>
<summary>94 requirements</summary>

| Requirement | Title | Priority | Spec page |
|---|---|---|---|
| R-API-006 | CSRF protection on the management API | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-007 | Recent authentication for sensitive actions | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-012 | Optimistic concurrency with ETag and If-Match | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-014 | Idempotency keys for creates | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-017 | Request limits, timeouts and response headers | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-018 | Cross-origin access | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-051 | Requests that span replicas | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-052 | Visibility of a management write on other replicas | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-020 | Resource map and routing | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-021 | Current caller and UI preferences | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-API-048 | Package ownership surface | P1 | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-026 | Service accounts | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-036 | Claim tester | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-042 | Errors, conflicts and feedback | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-043 | Secrets in the browser | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-044 | Accessibility | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-045 (implemented) | Internationalization | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-046 | State management with signals | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-UI-047 | Testing | MVP | [api-and-ui.md](../spec/api-and-ui.md) |
| R-ARCH-003 (implemented) | Go module and package layout | MVP | [architecture.md](../spec/architecture.md) |
| R-ARCH-004 (implemented) | Angular workspace layout | MVP | [architecture.md](../spec/architecture.md) |
| R-ARCH-007 | One origin for UI and package content | MVP | [architecture.md](../spec/architecture.md) |
| R-ARCH-029 | Optional separate content origin | P1 | [architecture.md](../spec/architecture.md) |
| R-ARCH-031 (implemented) | Valkey client, Sentinel discovery, TLS, ACL and degradation | MVP | [architecture.md](../spec/architecture.md) |
| R-ARCH-034 | Cluster-wide rate-limit counters | MVP | [architecture.md](../spec/architecture.md) |
| R-ARCH-023 (implemented) | Error model | MVP | [architecture.md](../spec/architecture.md) |
| R-ARCH-027 (implemented) | Performance targets | MVP | [architecture.md](../spec/architecture.md) |
| R-ARCH-028 | Resource limits for untrusted package content | MVP | [architecture.md](../spec/architecture.md) |
| R-AUTHZ-030 | The `authenticated` principal | MVP | [authorization.md](../spec/authorization.md) |
| R-AUTHZ-018 | Package ownership | MVP | [authorization.md](../spec/authorization.md) |
| R-AUTHZ-019 | Package ownership API | MVP | [authorization.md](../spec/authorization.md) |
| R-AUTHZ-020 | Shared ownership domains | P1 | [authorization.md](../spec/authorization.md) |
| R-AUTHZ-028 | Compiled grant index and invalidation across replicas | MVP | [authorization.md](../spec/authorization.md) |
| R-AUTHZ-032 | Seeing one's own permission change on any replica | MVP | [authorization.md](../spec/authorization.md) |
| R-AUTHZ-029 | Audit of authorization decisions | MVP | [authorization.md](../spec/authorization.md) |
| R-AUTHZ-031 | Revocation journal entries and the narrowing comparator | MVP | [authorization.md](../spec/authorization.md) |
| R-BAK-004 (implemented) | Table registry and export classes | MVP | [backup-and-restore.md](../spec/backup-and-restore.md) |
| R-BAK-007 | Garbage-collection hold | MVP | [backup-and-restore.md](../spec/backup-and-restore.md) |
| R-BAK-022 | State a restore rebuilds or invalidates | MVP | [backup-and-restore.md](../spec/backup-and-restore.md) |
| R-BAK-024 | Revocation journal replay after a restore | MVP | [backup-and-restore.md](../spec/backup-and-restore.md) |
| R-BAK-027 | Client-side encryption of the database files | P1 | [backup-and-restore.md](../spec/backup-and-restore.md) |
| R-EP-002 (implemented) | Endpoint identity comes from the listener | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-004 (implemented) | TLS on endpoint listeners: certificates by SNI, wildcard certificates, reload | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-006 (implemented) | Host-root binding: the reserved mechanism | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-010 (implemented) | Accepted credentials per endpoint | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-012 | Exposure rules: model, validation and storage | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-014 | Exposure on raw files, listings and the management API | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-016 | Endpoint and exposure management API | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-017 | Endpoint administration screen | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-018 | Package metadata on an endpoint: filtering, URLs and cache keys | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-019 | Cache-fill policy per endpoint | MVP | [endpoints.md](../spec/endpoints.md) |
| R-EP-020 | Exposure changes in the revocation journal | MVP | [endpoints.md](../spec/endpoints.md) |
| R-NPM-026 | Versions removed upstream | MVP | [formats/npm.md](../spec/formats/npm.md) |
| R-NPM-044 | npm audit events and metrics | MVP | [formats/npm.md](../spec/formats/npm.md) |
| R-NPM-046 | npm across replicas: no state between requests, Valkey degradation, drain | MVP | [formats/npm.md](../spec/formats/npm.md) |
| R-NPM-047 | npm data in backups and restores | MVP | [formats/npm.md](../spec/formats/npm.md) |
| R-NPM-048 | npm documents on an endpoint with exposure rules | MVP | [formats/npm.md](../spec/formats/npm.md) |
| R-NUGET-027 | Acceptance test suite | MVP | [formats/nuget.md](../spec/formats/nuget.md) |
| R-NUGET-028 | Audit events and metrics for NuGet | MVP | [formats/nuget.md](../spec/formats/nuget.md) |
| R-NUGET-030 | NuGet on interchangeable replicas (INV-18) | MVP | [formats/nuget.md](../spec/formats/nuget.md) |
| R-NUGET-031 | Valkey unavailable or emptied | MVP | [formats/nuget.md](../spec/formats/nuget.md) |
| R-NUGET-032 | Pushes and deletes across drains, restarts and client retries | MVP | [formats/nuget.md](../spec/formats/nuget.md) |
| R-NUGET-033 | NuGet data in backups and restores | MVP | [formats/nuget.md](../spec/formats/nuget.md) |
| R-NUGET-034 | NuGet documents on an endpoint with exposure rules | MVP | [formats/nuget.md](../spec/formats/nuget.md) |
| R-CI-029 | CI state across replicas, Valkey outages and restores | MVP | [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md) |
| R-CI-030 | Background tasks of the CI exchange | MVP | [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md) |
| R-HA-056 (implemented) | Valkey with Sentinel: connection and supported topologies | MVP | [ha-and-helm.md](../spec/ha-and-helm.md) |
| R-HA-057 (implemented) | Valkey failover, outage and degraded mode | MVP | [ha-and-helm.md](../spec/ha-and-helm.md) |
| R-HA-054 | Revocation journal in object storage | MVP | [ha-and-helm.md](../spec/ha-and-helm.md) |
| R-HA-055 | Optional content host at the edge | P1 | [ha-and-helm.md](../spec/ha-and-helm.md) |
| R-HA-051 | Local development profile | MVP | [ha-and-helm.md](../spec/ha-and-helm.md) |
| R-HA-052 | Failure-injection test suite | MVP | [ha-and-helm.md](../spec/ha-and-helm.md) |
| R-AUTH-012 | CSRF protection for cookie-authenticated requests | MVP | [identity-and-authentication.md](../spec/identity-and-authentication.md) |
| R-AUTH-021 | Dormant-user deactivation | P1 | [identity-and-authentication.md](../spec/identity-and-authentication.md) |
| R-AUTH-043 | Scheduled authentication tasks | MVP | [identity-and-authentication.md](../spec/identity-and-authentication.md) |
| R-OPS-045 | Valkey and coordination observability | MVP | [operations-and-observability.md](../spec/operations-and-observability.md) |
| R-OPS-046 | Operational signals for availability, horizontal scale and backup | MVP | [operations-and-observability.md](../spec/operations-and-observability.md) |
| R-OPS-013 (implemented) | Secret redaction | MVP | [operations-and-observability.md](../spec/operations-and-observability.md) |
| R-OPS-014 | Event registry | MVP | [operations-and-observability.md](../spec/operations-and-observability.md) |
| R-OPS-018 (implemented) | Trace context and OpenTelemetry export | MVP | [operations-and-observability.md](../spec/operations-and-observability.md) |
| R-OPS-047 | Installation-wide cache flush | P1 | [operations-and-observability.md](../spec/operations-and-observability.md) |
| R-OPS-024 | Read-only maintenance mode | P1 | [operations-and-observability.md](../spec/operations-and-observability.md) |
| R-REPO-026 | Collapsing concurrent misses (single-flight and Valkey flight lock) | MVP | [repositories-and-proxy.md](../spec/repositories-and-proxy.md) |
| R-REPO-038 | Egress address rules for remote repositories | MVP | [repositories-and-proxy.md](../spec/repositories-and-proxy.md) |
| R-REPO-040 | Size and concurrency limits | MVP | [repositories-and-proxy.md](../spec/repositories-and-proxy.md) |
| R-REPO-043 | Metadata URL rewriting | MVP | [repositories-and-proxy.md](../spec/repositories-and-proxy.md) |
| R-REPO-049 | Repository and proxy state on n replicas, without Valkey and during a drain | MVP | [repositories-and-proxy.md](../spec/repositories-and-proxy.md) |
| R-REPO-047 | Proxy and resolution observability | MVP | [repositories-and-proxy.md](../spec/repositories-and-proxy.md) |
| R-STOR-011 | Multi-request upload sessions | P1 | [storage.md](../spec/storage.md) |
| R-STOR-013 (implemented) | Download integrity verification | MVP | [storage.md](../spec/storage.md) |
| R-STOR-027 | Integrity scrub | P1 | [storage.md](../spec/storage.md) |
| R-STOR-036 (implemented) | Behavior during an object-store outage | MVP | [storage.md](../spec/storage.md) |
| R-STOR-046 (implemented) | Storage work per role and across replicas | MVP | [storage.md](../spec/storage.md) |
| R-STOR-047 | Storage state in backups and restores | MVP | [storage.md](../spec/storage.md) |

</details>
