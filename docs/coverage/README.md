# Coverage

> **As of:** 2026-10-28 · **Stage:** specification, no code yet · **Yardstick:**
> [feature catalog](../features/README.md) · **Design:** [specification](../spec/README.md)

This page answers two questions: how much of the feature catalog trove implements, and how much
of it the specification already designs. The per-feature list, with each feature's status and the
requirements that cover it, is [checklist.md](checklist.md).

The tables between the `coverage-gen` markers are generated from the catalog and spec headings
(see [How the numbers are produced](#how-the-numbers-are-produced)). The prose around them is
written by hand, carries the date above, and has to be re-read whenever the tables are
regenerated.

## Headline (2026-10-28)

<!-- coverage-gen:begin headline -->
| Number | Value | Kind |
|---|---|---|
| **Implemented** | **0%** (0 of 2,239 catalog features) | fact, not computed: the repository holds no code yet |
| **MVP features covered by spec requirements** | **99.7%** (358 of 359) | measured from `Covers:` lines |
| P1 features covered by spec requirements | 56.1% (285 of 508) | measured from `Covers:` lines |
| In-scope features covered (MVP, P1, P2, Later) | 30.3% (656 of 2,167) | measured from `Covers:` lines |
| Catalog features | 2,239 on 60 pages | measured from catalog headings |
| Spec requirements | 483 on 11 pages (336 MVP, 146 P1, 1 P2) | measured from spec headings |
| Requirements covering no catalog feature | 49 (`trove-only`) | measured from `Covers:` lines |
<!-- coverage-gen:end headline -->

In short: nothing is built yet, the first release is almost completely designed, and little
beyond P1 is designed. 358 of the 359 MVP features are covered by at least one requirement. For
each of those 358, at least one covering requirement is itself MVP, so the spec does not quietly
move MVP work to a later release. The one open MVP entry is FMT-013; see
[Honesty notes](#honesty-notes).

### By priority

<!-- coverage-gen:begin priority -->
| Priority | Features | Specified | Open | Specified share | Covered by an MVP requirement | Implemented |
|---|---:|---:|---:|---:|---:|---:|
| **MVP** | 359 | 358 | 1 | 99.7% | 358 | 0 |
| **P1** | 508 | 285 | 223 | 56.1% | 46 | 0 |
| **P2** | 909 | 13 | 896 | 1.4% | 5 | 0 |
| **Later** | 391 | 0 | 391 | 0% | 0 | 0 |
| **Out of scope** | 72 | 0 | 72 | 0% | 0 | 0 |
| **Total** | 2,239 | 656 | 1,583 | 29.3% | 409 | 0 |
<!-- coverage-gen:end priority -->

The column *Covered by an MVP requirement* shows where the spec pulls work forward. On 2026-10-28,
46 P1 features and 5 P2 features are covered by at least one MVP requirement, so in those places
the first release does more than the priority rubric asks. Every table on this page counts them
under their catalog priority.

### By catalog page

Each page name links to that page's section of the checklist.

<!-- coverage-gen:begin pages -->
| Catalog page | Features | MVP | P1 | P2 | Later | Out of scope | Specified | Open |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| [Repositories](checklist.md#repositories-repo) | 68 | 23 | 26 | 16 | 2 | 1 | 40 | 28 |
| [Remote repositories](checklist.md#remote-repositories-rmt) | 49 | 15 | 14 | 20 | 0 | 0 | 28 | 21 |
| [Replication, federation and multi-site topology](checklist.md#replication-federation-and-multi-site-topology-repl) | 72 | 0 | 0 | 17 | 51 | 4 | 0 | 72 |
| [Artifact management](checklist.md#artifact-management-art) | 53 | 10 | 20 | 22 | 0 | 1 | 31 | 22 |
| [Search and query language](checklist.md#search-and-query-language-srch) | 64 | 0 | 24 | 35 | 5 | 0 | 3 | 61 |
| [Builds and CI integration](checklist.md#builds-and-ci-integration-bld) | 49 | 0 | 15 | 22 | 11 | 1 | 0 | 49 |
| [Identity and access](checklist.md#identity-and-access-iam) | 69 | 20 | 11 | 26 | 7 | 5 | 34 | 35 |
| [Authentication providers and human login](checklist.md#authentication-providers-and-human-login-authn) | 44 | 13 | 5 | 25 | 0 | 1 | 18 | 26 |
| [Access tokens](checklist.md#access-tokens-tok) | 48 | 24 | 3 | 12 | 6 | 3 | 27 | 21 |
| [OIDC federation for CI systems](checklist.md#oidc-federation-for-ci-systems-cioidc) | 40 | 20 | 9 | 10 | 1 | 0 | 31 | 9 |
| [Storage and blob providers](checklist.md#storage-and-blob-providers-stor) | 41 | 12 | 7 | 20 | 1 | 1 | 21 | 20 |
| [Retention, cleanup and archiving](checklist.md#retention-cleanup-and-archiving-ret) | 42 | 0 | 26 | 3 | 11 | 2 | 26 | 16 |
| [High availability and deployment](checklist.md#high-availability-and-deployment-ha) | 79 | 49 | 14 | 10 | 3 | 3 | 60 | 19 |
| [Operations, monitoring and configuration](checklist.md#operations-monitoring-and-configuration-ops) | 100 | 17 | 23 | 34 | 1 | 25 | 40 | 60 |
| [Events and extensibility](checklist.md#events-and-extensibility-evt) | 82 | 1 | 24 | 12 | 43 | 2 | 25 | 57 |
| [Distribution and release lifecycle](checklist.md#distribution-and-release-lifecycle-dist) | 70 | 0 | 0 | 0 | 69 | 1 | 0 | 70 |
| [Security scanning and curation](checklist.md#security-scanning-and-curation-sec) | 98 | 0 | 22 | 18 | 56 | 2 | 0 | 98 |
| [UI inventory](checklist.md#ui-inventory-ui) | 61 | 30 | 17 | 8 | 3 | 3 | 47 | 14 |
| [Content API operation catalog](checklist.md#content-api-operation-catalog-api) | 166 | 11 | 56 | 48 | 48 | 3 | 67 | 99 |
| [Operation catalog: administration API](checklist.md#operation-catalog-administration-api-admapi) | 114 | 34 | 23 | 23 | 28 | 6 | 54 | 60 |
| [Package formats: matrix and cross-format behavior](checklist.md#package-formats-matrix-and-cross-format-behavior-fmt) | 14 | 8 | 3 | 1 | 1 | 1 | 8 | 6 |
| [Beyond the baseline: further formats and capabilities](checklist.md#beyond-the-baseline-further-formats-and-capabilities-beyond) | 34 | 0 | 5 | 26 | 3 | 0 | 2 | 32 |

Per-format pages (38 pages, 782 features, 94 specified):

<details>
<summary>38 format pages</summary>

| Catalog page | Features | MVP | P1 | P2 | Later | Out of scope | Specified | Open |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| [npm package format](checklist.md#npm-package-format-pkg-npm) | 62 | 37 | 15 | 8 | 1 | 1 | 52 | 10 |
| [NuGet](checklist.md#nuget-pkg-nuget) | 56 | 35 | 7 | 12 | 2 | 0 | 42 | 14 |
| [Docker image format](checklist.md#docker-image-format-pkg-docker) | 52 | 0 | 34 | 13 | 3 | 2 | 0 | 52 |
| [OCI images and artifacts](checklist.md#oci-images-and-artifacts-pkg-oci) | 20 | 0 | 15 | 4 | 1 | 0 | 0 | 20 |
| [Helm chart format](checklist.md#helm-chart-format-pkg-helm) | 25 | 0 | 18 | 6 | 1 | 0 | 0 | 25 |
| [Maven package format](checklist.md#maven-package-format-pkg-maven) | 39 | 0 | 23 | 15 | 1 | 0 | 0 | 39 |
| [PyPI package format](checklist.md#pypi-package-format-pkg-pypi) | 29 | 0 | 23 | 6 | 0 | 0 | 0 | 29 |
| [Go modules format](checklist.md#go-modules-format-pkg-go) | 16 | 0 | 13 | 3 | 0 | 0 | 0 | 16 |
| [Generic format](checklist.md#generic-format-pkg-generic) | 11 | 0 | 8 | 3 | 0 | 0 | 0 | 11 |
| [Alpine Linux package format](checklist.md#alpine-linux-package-format-pkg-apk) | 14 | 0 | 0 | 14 | 0 | 0 | 0 | 14 |
| [Ansible collection format](checklist.md#ansible-collection-format-pkg-ansible) | 13 | 0 | 0 | 13 | 0 | 0 | 0 | 13 |
| [Bazel modules package format](checklist.md#bazel-modules-package-format-pkg-bazel) | 12 | 0 | 1 | 11 | 0 | 0 | 0 | 12 |
| [Bower package format](checklist.md#bower-package-format-pkg-bower) | 14 | 0 | 0 | 13 | 1 | 0 | 0 | 14 |
| [Cargo (Rust crates) format](checklist.md#cargo-rust-crates-format-pkg-cargo) | 14 | 0 | 0 | 13 | 1 | 0 | 0 | 14 |
| [Chef cookbook format](checklist.md#chef-cookbook-format-pkg-chef) | 10 | 0 | 0 | 10 | 0 | 0 | 0 | 10 |
| [CocoaPods package format](checklist.md#cocoapods-package-format-pkg-pods) | 15 | 0 | 0 | 14 | 1 | 0 | 0 | 15 |
| [PHP Composer format](checklist.md#php-composer-format-pkg-composer) | 11 | 0 | 0 | 11 | 0 | 0 | 0 | 11 |
| [Conan (C/C++) format](checklist.md#conan-cc-format-pkg-conan) | 14 | 0 | 0 | 13 | 1 | 0 | 0 | 14 |
| [Conda package format](checklist.md#conda-package-format-pkg-conda) | 15 | 0 | 0 | 15 | 0 | 0 | 0 | 15 |
| [CRAN package format](checklist.md#cran-package-format-pkg-cran) | 16 | 0 | 0 | 16 | 0 | 0 | 0 | 16 |
| [Debian package format](checklist.md#debian-package-format-pkg-deb) | 28 | 0 | 0 | 27 | 1 | 0 | 0 | 28 |
| [Git LFS format](checklist.md#git-lfs-format-pkg-lfs) | 9 | 0 | 0 | 9 | 0 | 0 | 0 | 9 |
| [Gradle, Ivy, sbt and Eclipse P2 formats](checklist.md#gradle-ivy-sbt-and-eclipse-p2-formats-pkg-jvm) | 24 | 0 | 4 | 19 | 1 | 0 | 0 | 24 |
| [Hex package format (Elixir and Erlang)](checklist.md#hex-package-format-elixir-and-erlang-pkg-hex) | 18 | 0 | 0 | 18 | 0 | 0 | 0 | 18 |
| [Hugging Face models and datasets](checklist.md#hugging-face-models-and-datasets-pkg-hf) | 18 | 0 | 0 | 15 | 3 | 0 | 0 | 18 |
| [LuaRocks package format](checklist.md#luarocks-package-format-pkg-lua) | 18 | 0 | 0 | 17 | 1 | 0 | 0 | 18 |
| [Nix package format](checklist.md#nix-package-format-pkg-nix) | 20 | 0 | 0 | 19 | 1 | 0 | 0 | 20 |
| [Opkg package format](checklist.md#opkg-package-format-pkg-opkg) | 8 | 0 | 0 | 8 | 0 | 0 | 0 | 8 |
| [pub package format (Dart and Flutter)](checklist.md#pub-package-format-dart-and-flutter-pkg-pub) | 19 | 0 | 0 | 18 | 1 | 0 | 0 | 19 |
| [Puppet module format](checklist.md#puppet-module-format-pkg-puppet) | 12 | 0 | 0 | 11 | 1 | 0 | 0 | 12 |
| [RPM package format](checklist.md#rpm-package-format-pkg-rpm) | 18 | 0 | 0 | 18 | 0 | 0 | 0 | 18 |
| [RubyGems format](checklist.md#rubygems-format-pkg-gems) | 18 | 0 | 0 | 18 | 0 | 0 | 0 | 18 |
| [Swift package format](checklist.md#swift-package-format-pkg-swift) | 18 | 0 | 0 | 18 | 0 | 0 | 0 | 18 |
| [Terraform and OpenTofu formats](checklist.md#terraform-and-opentofu-formats-pkg-tf) | 23 | 0 | 0 | 23 | 0 | 0 | 0 | 23 |
| [Vagrant box format](checklist.md#vagrant-box-format-pkg-vagrant) | 4 | 0 | 0 | 4 | 0 | 0 | 0 | 4 |
| [VCS archive format](checklist.md#vcs-archive-format-pkg-vcs) | 13 | 0 | 0 | 13 | 0 | 0 | 0 | 13 |
| [Machine-learning models and model-serving containers](checklist.md#machine-learning-models-and-model-serving-containers-pkg-ml) | 19 | 0 | 0 | 15 | 3 | 1 | 0 | 19 |
| [AI agent assets and IDE extension formats](checklist.md#ai-agent-assets-and-ide-extension-formats-pkg-ai) | 37 | 0 | 0 | 18 | 16 | 3 | 0 | 37 |

</details>
<!-- coverage-gen:end pages -->

## What MVP means

MVP is the scope of the first release. The [priority rubric](../features/README.md#priority-rubric)
in the catalog defines it, and the summary here is no substitute:

- npm and NuGet, each with local, remote (pull-through) and virtual repositories;
- human login through an external OpenID Connect provider, trove's own access tokens, and
  fine-grained permissions;
- the GitLab CI token exchange over OIDC, through which pipeline jobs obtain short-lived, scoped
  trove tokens;
- PostgreSQL for metadata, S3-compatible object storage for binaries, high availability, and
  deployment through a Helm chart;
- the UI and the management API needed to operate all of the above.

P1 is the work directly after the MVP: Docker/OCI, Maven, PyPI, Go, Helm charts and generic
repositories, webhooks, retention and cleanup policies, the search API, build metadata basics,
configuration as code and further CI OIDC providers. P2, *Later* and *Out of scope* are defined by the same rubric.
*Out of scope* entries stay in the catalog only so its inventory is complete. Nothing is planned
for them.

## Readiness by audience

Readiness here means *specified versus open*, not *usable*. Nothing is implemented, so no audience
can use trove today. The rows show how far the design gets each audience once it is built. Each
audience counts the MVP and P1 entries of a fixed list of catalog pages, shown below the first
table.

<!-- coverage-gen:begin audiences -->
| Audience | MVP entries | MVP and P1 entries | Implemented | Most open entries (MVP and P1) |
|---|---|---|---:|---|
| **npm-only team** | 199 of 200 specified, 1 open (99.5%) | 313 of 362 specified, 49 open (86.5%) | 0 | Search and query language 21; Builds and CI integration 15; Repositories 9 |
| **.NET team** | 197 of 198 specified, 1 open (99.5%) | 303 of 352 specified, 49 open (86.1%) | 0 | Search and query language 21; Builds and CI integration 15; Repositories 9 |
| **platform/ops team** | 258 of 258 specified, 0 open (100%) | 440 of 460 specified, 20 open (95.7%) | 0 | Repositories 9; High availability and deployment 3; Operation catalog: administration API 3 |

Pages counted per audience:

| Audience | Who | Catalog pages counted |
|---|---|---|
| **npm-only team** | publishes and installs npm packages from workstations and GitLab CI | [formats/npm.md](../features/formats/npm.md), [formats/README.md](../features/formats/README.md), [repositories.md](../features/repositories.md), [remote-repositories.md](../features/remote-repositories.md), [artifacts.md](../features/artifacts.md), [search-and-query.md](../features/search-and-query.md), [builds-and-ci.md](../features/builds-and-ci.md), [identity-and-access.md](../features/identity-and-access.md), [authentication-providers.md](../features/authentication-providers.md), [access-tokens.md](../features/access-tokens.md), [oidc-ci-integration.md](../features/oidc-ci-integration.md), [ui-inventory.md](../features/ui-inventory.md) |
| **.NET team** | publishes and restores NuGet packages from workstations and GitLab CI | [formats/nuget.md](../features/formats/nuget.md), [formats/README.md](../features/formats/README.md), [repositories.md](../features/repositories.md), [remote-repositories.md](../features/remote-repositories.md), [artifacts.md](../features/artifacts.md), [search-and-query.md](../features/search-and-query.md), [builds-and-ci.md](../features/builds-and-ci.md), [identity-and-access.md](../features/identity-and-access.md), [authentication-providers.md](../features/authentication-providers.md), [access-tokens.md](../features/access-tokens.md), [oidc-ci-integration.md](../features/oidc-ci-integration.md), [ui-inventory.md](../features/ui-inventory.md) |
| **platform/ops team** | runs trove on Kubernetes and administers it | [ha-and-deployment.md](../features/ha-and-deployment.md), [operations-and-monitoring.md](../features/operations-and-monitoring.md), [storage-and-blobs.md](../features/storage-and-blobs.md), [retention-and-cleanup.md](../features/retention-and-cleanup.md), [repositories.md](../features/repositories.md), [remote-repositories.md](../features/remote-repositories.md), [identity-and-access.md](../features/identity-and-access.md), [authentication-providers.md](../features/authentication-providers.md), [access-tokens.md](../features/access-tokens.md), [oidc-ci-integration.md](../features/oidc-ci-integration.md), [api-admin-operations.md](../features/api-admin-operations.md), [events-and-extensibility.md](../features/events-and-extensibility.md), [ui-inventory.md](../features/ui-inventory.md) |

Pages counted for no audience (41, 233 MVP and P1 entries): [Security scanning and curation](checklist.md#security-scanning-and-curation-sec), [Content API operation catalog](checklist.md#content-api-operation-catalog-api), [Docker image format](checklist.md#docker-image-format-pkg-docker), [OCI images and artifacts](checklist.md#oci-images-and-artifacts-pkg-oci), [Helm chart format](checklist.md#helm-chart-format-pkg-helm), [Maven package format](checklist.md#maven-package-format-pkg-maven), [PyPI package format](checklist.md#pypi-package-format-pkg-pypi), [Go modules format](checklist.md#go-modules-format-pkg-go), [Generic format](checklist.md#generic-format-pkg-generic), [Bazel modules package format](checklist.md#bazel-modules-package-format-pkg-bazel), [Gradle, Ivy, sbt and Eclipse P2 formats](checklist.md#gradle-ivy-sbt-and-eclipse-p2-formats-pkg-jvm), [Beyond the baseline: further formats and capabilities](checklist.md#beyond-the-baseline-further-formats-and-capabilities-beyond); plus 29 pages without MVP or P1 entries.
<!-- coverage-gen:end audiences -->

What the open entries are, on 2026-10-28:

- **npm-only team.** The only open MVP entry is FMT-013. The npm page itself is fully specified at
  MVP and P1. Most of the open P1 entries fall into two groups:
  - Search operations: only 3 of the 24 P1 entries on the search page are specified:
    - the global search box;
    - artifact browser search;
    - format-specific metadata search fields, through R-NPM-037.
  - Build metadata: all 15 P1 entries are open, and no spec page owns build records yet. The open
    questions Q-AUTHZ-8 in [authorization.md](../spec/authorization.md) and Q-API-5 in
    [api-and-ui.md](../spec/api-and-ui.md) both recommend a new spec page for them.

  The remaining 12 open entries fall into three groups:
  - Eight repository settings that matter only once other formats arrive:
    - Maven-family release and snapshot handling, snapshot naming, the deployment checksum policy
      and descriptor checks (REPO-022, REPO-023, REPO-024, REPO-026, REPO-041, RMT-036);
    - Docker tag resolution (REPO-043);
    - generic repositories (REPO-003).
  - Two cross-format entries that do affect npm and NuGet:
    - layout mapping (FMT-010), for which the catalog lists bundled npm and NuGet layouts;
    - the instance-wide package settings screen (FMT-014), which carries a NuGet
      version-normalization switch.
  - Two project entries: REPO-013 *Project assignment and environments* and REPO-045 *Virtual
    repositories in projects*. Projects themselves are specified at P1 (R-AUTHZ-026, R-API-047),
    but no requirement lists these two (see the priority note under
    [Honesty notes](#honesty-notes)).
- **.NET team.** The open entries are the same, because the shared pages are the same. The NuGet
  page is fully specified at MVP and P1; its open entries are P2 and *Later*.
- **platform/ops team.** All MVP entries on its pages are specified. The open P1 entries are:
  - outbound connectivity: a deployment-wide outbound proxy (HA-075) and air-gapped operation
    (HA-076);
  - installing without Kubernetes (HA-077);
  - general settings, the container registry addressing method and reverse-proxy rules
    (OPS-057, OPS-062, OPS-063);
  - the webhook management screen (EVT-034);
  - role and environment operations of the administration API (ADMAPI-019, ADMAPI-066,
    ADMAPI-068);
  - 10 of the repository entries listed for the npm team: the eight format-specific settings
    and the two project entries. FMT-010 and FMT-014 are not counted here, because this audience
    does not count the formats page.

## How the numbers are produced

1. **Catalog entries.** Every heading `### <ID> · <Title>` under [docs/features/](../features/)
   whose next line is `Priority: **<P>**`, where P is MVP, P1, P2, Later or Out of scope. Lines
   inside fenced code blocks are ignored, so the format examples in the catalog README and the
   spec README are not entries.
2. **Requirements.** Every heading `### R-<…>-<n> · <Title>` under [docs/spec/](../spec/) whose
   next line is `Priority: **<P>** · Covers: <ID>, <ID>, …` or `Covers: trove-only`. Other
   headings on spec pages (open questions `Q-…`, gaps `H-…`, examples) are not requirements.
3. **Status.** A feature is *specified* when its ID appears in the `Covers:` line of at least one
   requirement, and *open* otherwise. The generator does not measure *implemented*. It reports 0
   because no code exists (on 2026-10-28 the repository's only commit holds LICENSE and
   README.md). Once code exists, *implemented* needs its own evidence for each requirement, such
   as a passing acceptance test that names the requirement ID. That rule is not decided yet.
4. **Shares.** Plain counts: each feature counts once, whatever its size. The in-scope share
   leaves out *Out of scope* entries.
5. **Audiences.** For each audience: the MVP and P1 entries of the pages in its row of the
   audience table. The page lists are fixed in the generator, and the table above shows them.
6. **Checks that stop generation** (exit code 1):
   - a duplicate catalog ID or requirement ID;
   - a `Covers:` ID that is not in the catalog;
   - a `Covers:` token that is neither an ID nor `trove-only`, or `trove-only` mixed with IDs;
   - an entry or requirement heading without its priority line directly below;
   - an unclosed code fence;
   - a missing marker pair in this file;
   - an audience page without entries.

   When it runs, the generator prints the counts per priority for the catalog and the spec, the
   number of `Covers:` references, and every heading it skipped inside a code fence.
7. **Output.** It writes all of [checklist.md](checklist.md), and only the four marked blocks of
   this file.

**Gap: the generator is not in the repository.** For this version it ran outside the repository.
Until it is committed and its compare-only mode runs in CI, nothing keeps
[checklist.md](checklist.md) and the tables above current when a catalog or spec heading
changes. Until then, the rules above are its specification.

## Honesty notes

- **Every feature counts the same.** RMT-004 *Upstream URL* counts as much as HA-001
  *Active-active cluster of identical nodes*. The two operation catalogs list about one entry per
  API operation, or per small group of operations, which makes them 280 of the 2,239 entries. The shares therefore measure catalog
  breadth, not effort and not value to a user.
- **Specified means a requirement claims the feature, not that it was reviewed.** The generator
  checks that a `Covers:` ID exists. It does not check that the requirement designs everything the
  feature describes. A feature that a requirement covers only in part still counts as specified.
  Nobody has spot-checked coverage claims for this page.
- **Cross-format entries.** Entries on the [formats page](../features/formats/README.md) apply to
  every format. FMT-013 says explicitly that its MVP priority applies to the npm and NuGet rows
  only, and every other row keeps its own format's priority. The other FMT entries were not
  checked for the same pattern. For such an entry, *specified* means at most the npm and NuGet
  part.
- **The one open MVP entry, FMT-013** (*Client authentication styles and anonymous fallbacks*).
  The design exists in parts, but no requirement names FMT-013:
  - R-AUTH-033 *Client authentication matrix* in
    [identity-and-authentication.md](../spec/identity-and-authentication.md) defines how npm and
    NuGet clients present tokens.
  - R-AUTH-037 and R-NUGET-012 cover anonymous access and NuGet endpoint authentication.

  This looks like a missing ID in a `Covers:` line rather than missing design. It is not verified
  that these requirements cover the per-format anonymous fallbacks FMT-013 asks for. Either
  outcome is resolved in the spec: add FMT-013 to the covering requirement, or write the missing
  requirement.
- **The catalog's limits.** The catalog was written from public product documentation, so
  behavior that documentation leaves out is missing from it. Entries differ in granularity by
  page: one per operation on the API pages, one per protocol capability on the format pages. If
  the catalog grows, every share on this page moves without any change to trove. The catalog is
  also a measure of breadth, not of design quality. The 49 `trove-only` requirements are design
  work it does not measure at all, for example R-REPO-038 *Egress address rules for remote
  repositories* and R-STOR-013 *Download integrity verification*.
- **Priorities are trove's own judgement** and can change. When a priority changes, the MVP
  share changes with it, even if no design or code changes. The catalog is not consistent about
  projects, roles and environments:

  | Concept | On the identity page | Elsewhere in the catalog |
  |---|---|---|
  | Projects | P2 (IAM-051) | P1: project settings of repositories (REPO-013, REPO-045) |
  | Roles | P2 (IAM-054 global roles, IAM-055 project roles) | P1: role operations (ADMAPI-019, ADMAPI-066) |
  | Environments | P2 (IAM-063) | P1: environment operations (ADMAPI-068); REPO-013 also assigns environments |

  The spec follows the P1 side for projects (R-AUTHZ-026), but not yet for roles and
  environments. Five of the open P1 entries come from this disagreement. Settling the catalog
  priority first is cheaper than designing those five.
- **Audiences are approximated at page level.** A page counts in full for an audience even when
  some of its entries belong to another team's work. For example, identity-and-access counts for
  the developer teams although most of it is administration. Some pages count for no audience;
  the line under the audience table lists them. Among them, the content API is the only page with
  MVP entries, and all 11 of those are specified. The catalog has no audience tag per feature;
  adding one would make these rows exact.

## Effort

Not estimated. Nothing calibrates one yet: no requirement is implemented and the repository has a
single commit, so there is no measured cost per requirement or per feature. Multiplying the MVP
requirement count by an assumed cost per requirement would be a guess presented as a derivation.

A defensible estimate needs two steps:

1. Build a first vertical slice: an npm local, remote and virtual repository behind OIDC login and
   tokens, on PostgreSQL and S3.
2. Record agent-hours per implemented requirement, by spec page. Extrapolate to the remaining
   requirements of each page, and use the observed spread as the range.

The size basis on 2026-10-28, from the tables above:

- 336 MVP and 146 P1 requirements on 11 spec pages;
- 223 P1 catalog entries that no requirement covers yet.

## Related pages

- [checklist.md](checklist.md): every catalog feature with its status and covering requirements.
- [Feature catalog](../features/README.md): entry format and priority rubric.
- [Specification](../spec/README.md): owner constraints, design invariants, requirement format.
