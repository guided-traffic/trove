# Coverage

> **As of:** 2026-10-29 · **Stage:** specification, every question decided, no code yet · **Yardstick:**
> [feature catalog](../features/README.md) · **Design:** [specification](../spec/README.md)

This page answers two questions: how much of the feature catalog trove implements, and how much
of it the specification already designs. The per-feature list, with each feature's status and the
requirements that cover it, is [checklist.md](checklist.md).

The tables between the `coverage-gen` markers are generated from the catalog and spec headings
(see [How the numbers are produced](#how-the-numbers-are-produced)). The prose around them is
written by hand, carries the date above, and has to be re-read whenever the tables are
regenerated.

## Headline (2026-10-29)

<!-- coverage-gen:begin headline -->
| Number | Value | Kind |
|---|---|---|
| **Implemented** | **8.5%** (190 of 2,239 catalog features) | measured from passing tests that name requirements (Q-ROAD-4) |
| **MVP features covered by spec requirements** | **100%** (377 of 377) | measured from `Covers:` lines |
| P1 features covered by spec requirements | 56.7% (293 of 517) | measured from `Covers:` lines |
| In-scope features covered (MVP, P1, P2, Later) | 32.1% (695 of 2,167) | measured from `Covers:` lines |
| Catalog features | 2,239 on 60 pages | measured from catalog headings |
| Spec requirements | 624 on 15 pages (468 MVP, 151 P1, 5 P2) | measured from spec headings |
| Requirements covering no catalog feature | 101 (`trove-only`) | measured from `Covers:` lines |
<!-- coverage-gen:end headline -->

**What the owner constraints of 2026-10-28 changed.** The tables were regenerated on 2026-10-29,
after the spec pages took in the constraints of that day: HA PostgreSQL, HA Valkey with Sentinel
and S3; a load spread over many replicas with node drains that cause no outage; a configurable
backup job; and no ReadWriteMany volume ([docs/spec/README.md](../spec/README.md#owner-constraints)).
Against the previous generation of 2026-10-28:

- spec requirements: 553 on 12 pages, up from 491 on 11; the new page is
  [backup-and-restore.md](../spec/backup-and-restore.md);
- requirements covering no catalog feature: 81, up from 54;
- catalog features listed in a `Covers:` line: 662, up from 658. The four new ones are P2 backup
  and migration entries (OPS-032, OPS-034, OPS-035 and OPS-039);
- features covered by an MVP requirement: 55 P1 and 10 P2, up from 48 and 6;
- unchanged: MVP 359 of 359, P1 286 of 521, implemented 0.

In short: nothing is built yet, every MVP catalog entry is claimed by a requirement, and little
beyond P1 is designed. All 359 MVP features are covered by at least one requirement. For each of
them, at least one covering requirement is itself MVP, so the spec does not quietly move MVP work
to a later release. Covered means claimed in a `Covers:` line, not reviewed against the feature;
see [Honesty notes](#honesty-notes).

### By priority

<!-- coverage-gen:begin priority -->
| Priority | Features | Specified | Open | Specified share | Covered by an MVP requirement | Implemented |
|---|---:|---:|---:|---:|---:|---:|
| **MVP** | 377 | 377 | 0 | 100% | 377 | 138 |
| **P1** | 517 | 293 | 224 | 56.7% | 61 | 45 |
| **P2** | 895 | 23 | 872 | 2.6% | 12 | 7 |
| **Later** | 378 | 2 | 376 | 0.5% | 2 | 0 |
| **Out of scope** | 72 | 0 | 72 | 0% | 0 | 0 |
| **Total** | 2,239 | 695 | 1,544 | 31.0% | 452 | 190 |
<!-- coverage-gen:end priority -->

The column *Covered by an MVP requirement* shows where the spec pulls work forward: 55 P1 features
and 10 P2 features are covered by at least one MVP requirement (48 and 6 before 2026-10-28). In
those places the first release does more than the priority rubric asks. The new ones come from the owner constraints of 2026-10-28: the backup job and restore
(OPS-032, OPS-034, OPS-035, OPS-039), the monitoring integration that alerts on a Valkey outage
(HA-055), and the repository maintenance operations whose cache invalidation became MVP (API-028,
API-040, API-041, API-043, API-045, API-050). Every table on this page counts them under their
catalog priority.

### By catalog page

Each page name links to that page's section of the checklist.

<!-- coverage-gen:begin pages -->
| Catalog page | Features | MVP | P1 | P2 | Later | Out of scope | Specified | Open |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| [Repositories](checklist.md#repositories-repo) | 68 | 23 | 26 | 16 | 2 | 1 | 41 | 27 |
| [Remote repositories](checklist.md#remote-repositories-rmt) | 49 | 15 | 14 | 20 | 0 | 0 | 28 | 21 |
| [Replication, federation and multi-site topology](checklist.md#replication-federation-and-multi-site-topology-repl) | 72 | 0 | 0 | 17 | 51 | 4 | 0 | 72 |
| [Artifact management](checklist.md#artifact-management-art) | 53 | 10 | 20 | 22 | 0 | 1 | 32 | 21 |
| [Search and query language](checklist.md#search-and-query-language-srch) | 64 | 0 | 24 | 35 | 5 | 0 | 3 | 61 |
| [Builds and CI integration](checklist.md#builds-and-ci-integration-bld) | 49 | 0 | 15 | 22 | 11 | 1 | 0 | 49 |
| [Identity and access](checklist.md#identity-and-access-iam) | 69 | 20 | 11 | 26 | 7 | 5 | 35 | 34 |
| [Authentication providers and human login](checklist.md#authentication-providers-and-human-login-authn) | 44 | 13 | 5 | 25 | 0 | 1 | 18 | 26 |
| [Access tokens](checklist.md#access-tokens-tok) | 48 | 24 | 3 | 12 | 6 | 3 | 27 | 21 |
| [OIDC federation for CI systems](checklist.md#oidc-federation-for-ci-systems-cioidc) | 40 | 20 | 9 | 10 | 1 | 0 | 31 | 9 |
| [Storage and blob providers](checklist.md#storage-and-blob-providers-stor) | 41 | 12 | 7 | 20 | 1 | 1 | 21 | 20 |
| [Retention, cleanup and archiving](checklist.md#retention-cleanup-and-archiving-ret) | 42 | 0 | 26 | 3 | 11 | 2 | 26 | 16 |
| [High availability and deployment](checklist.md#high-availability-and-deployment-ha) | 79 | 49 | 14 | 10 | 3 | 3 | 61 | 18 |
| [Operations, monitoring and configuration](checklist.md#operations-monitoring-and-configuration-ops) | 100 | 17 | 23 | 34 | 1 | 25 | 43 | 57 |
| [Events and extensibility](checklist.md#events-and-extensibility-evt) | 82 | 1 | 24 | 12 | 43 | 2 | 26 | 56 |
| [Distribution and release lifecycle](checklist.md#distribution-and-release-lifecycle-dist) | 70 | 0 | 0 | 0 | 69 | 1 | 0 | 70 |
| [Security scanning and curation](checklist.md#security-scanning-and-curation-sec) | 98 | 18 | 18 | 17 | 43 | 2 | 18 | 80 |
| [UI inventory](checklist.md#ui-inventory-ui) | 61 | 30 | 17 | 8 | 3 | 3 | 47 | 14 |
| [Content API operation catalog](checklist.md#content-api-operation-catalog-api) | 166 | 11 | 56 | 48 | 48 | 3 | 67 | 99 |
| [Operation catalog: administration API](checklist.md#operation-catalog-administration-api-admapi) | 114 | 34 | 23 | 23 | 28 | 6 | 54 | 60 |
| [Package formats: matrix and cross-format behavior](checklist.md#package-formats-matrix-and-cross-format-behavior-fmt) | 14 | 8 | 3 | 1 | 1 | 1 | 9 | 5 |
| [Beyond the baseline: further formats and capabilities](checklist.md#beyond-the-baseline-further-formats-and-capabilities-beyond) | 34 | 0 | 5 | 26 | 3 | 0 | 2 | 32 |

Per-format pages (38 pages, 782 features, 106 specified):

<details>
<summary>38 format pages</summary>

| Catalog page | Features | MVP | P1 | P2 | Later | Out of scope | Specified | Open |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| [npm package format](checklist.md#npm-package-format-pkg-npm) | 62 | 37 | 15 | 8 | 1 | 1 | 52 | 10 |
| [NuGet](checklist.md#nuget-pkg-nuget) | 56 | 35 | 7 | 12 | 2 | 0 | 42 | 14 |
| [Docker image format](checklist.md#docker-image-format-pkg-docker) | 52 | 0 | 34 | 13 | 3 | 2 | 1 | 51 |
| [OCI images and artifacts](checklist.md#oci-images-and-artifacts-pkg-oci) | 20 | 0 | 15 | 4 | 1 | 0 | 0 | 20 |
| [Helm chart format](checklist.md#helm-chart-format-pkg-helm) | 25 | 0 | 18 | 6 | 1 | 0 | 0 | 25 |
| [Maven package format](checklist.md#maven-package-format-pkg-maven) | 39 | 0 | 23 | 15 | 1 | 0 | 0 | 39 |
| [PyPI package format](checklist.md#pypi-package-format-pkg-pypi) | 29 | 0 | 23 | 6 | 0 | 0 | 0 | 29 |
| [Go modules format](checklist.md#go-modules-format-pkg-go) | 16 | 0 | 13 | 3 | 0 | 0 | 0 | 16 |
| [Generic format](checklist.md#generic-format-pkg-generic) | 11 | 0 | 8 | 3 | 0 | 0 | 11 | 0 |
| [Ansible collection format](checklist.md#ansible-collection-format-pkg-ansible) | 13 | 0 | 13 | 0 | 0 | 0 | 0 | 13 |
| [Alpine Linux package format](checklist.md#alpine-linux-package-format-pkg-apk) | 14 | 0 | 0 | 14 | 0 | 0 | 0 | 14 |
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
- since 2026-10-28, by owner constraint: HA PostgreSQL, HA Valkey with Sentinel and S3; `web` and
  `worker` replicas that share a load one replica cannot carry, with any request on any replica
  and node drains without an outage; trove's own backup job with restore into an empty
  installation; and no ReadWriteMany volume;
- the UI and the management API needed to operate all of the above.

P1 is the work directly after the MVP: Docker/OCI, Maven, PyPI, Go, Helm charts and generic
repositories, webhooks, retention and cleanup policies, the search API, build metadata basics,
configuration as code and further CI OIDC providers. One P1 item ships in the first release
anyway, because MVP acceptance tests depend on it: the local class of generic repositories
([roadmap Q-ROAD-1](roadmap.md#q-road-1--generic-local-repositories-in-m1)). Backup and restore
used to be the second such item. Since 2026-10-28 they are MVP, together with the platform backup
layer and the revocation journal
([roadmap Q-ROAD-2](roadmap.md#q-road-2--backup-and-restore-in-the-beta)).

P2, *Later* and *Out of scope* are defined by the same rubric.
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
| **npm-only team** | 200 of 200 specified, 0 open (100%) | 314 of 362 specified, 48 open (86.7%) | 106 | Search and query language 21; Builds and CI integration 15; Repositories 9 |
| **.NET team** | 198 of 198 specified, 0 open (100%) | 304 of 352 specified, 48 open (86.4%) | 97 | Search and query language 21; Builds and CI integration 15; Repositories 9 |
| **platform/ops team** | 258 of 258 specified, 0 open (100%) | 440 of 460 specified, 20 open (95.7%) | 145 | Repositories 9; Operations, monitoring and configuration 4; High availability and deployment 3 |

Pages counted per audience:

| Audience | Who | Catalog pages counted |
|---|---|---|
| **npm-only team** | publishes and installs npm packages from workstations and GitLab CI | [formats/npm.md](../features/formats/npm.md), [formats/README.md](../features/formats/README.md), [repositories.md](../features/repositories.md), [remote-repositories.md](../features/remote-repositories.md), [artifacts.md](../features/artifacts.md), [search-and-query.md](../features/search-and-query.md), [builds-and-ci.md](../features/builds-and-ci.md), [identity-and-access.md](../features/identity-and-access.md), [authentication-providers.md](../features/authentication-providers.md), [access-tokens.md](../features/access-tokens.md), [oidc-ci-integration.md](../features/oidc-ci-integration.md), [ui-inventory.md](../features/ui-inventory.md) |
| **.NET team** | publishes and restores NuGet packages from workstations and GitLab CI | [formats/nuget.md](../features/formats/nuget.md), [formats/README.md](../features/formats/README.md), [repositories.md](../features/repositories.md), [remote-repositories.md](../features/remote-repositories.md), [artifacts.md](../features/artifacts.md), [search-and-query.md](../features/search-and-query.md), [builds-and-ci.md](../features/builds-and-ci.md), [identity-and-access.md](../features/identity-and-access.md), [authentication-providers.md](../features/authentication-providers.md), [access-tokens.md](../features/access-tokens.md), [oidc-ci-integration.md](../features/oidc-ci-integration.md), [ui-inventory.md](../features/ui-inventory.md) |
| **platform/ops team** | runs trove on Kubernetes and administers it | [ha-and-deployment.md](../features/ha-and-deployment.md), [operations-and-monitoring.md](../features/operations-and-monitoring.md), [storage-and-blobs.md](../features/storage-and-blobs.md), [retention-and-cleanup.md](../features/retention-and-cleanup.md), [repositories.md](../features/repositories.md), [remote-repositories.md](../features/remote-repositories.md), [identity-and-access.md](../features/identity-and-access.md), [authentication-providers.md](../features/authentication-providers.md), [access-tokens.md](../features/access-tokens.md), [oidc-ci-integration.md](../features/oidc-ci-integration.md), [api-admin-operations.md](../features/api-admin-operations.md), [events-and-extensibility.md](../features/events-and-extensibility.md), [ui-inventory.md](../features/ui-inventory.md) |

Pages counted for no audience (41, 260 MVP and P1 entries): [Security scanning and curation](checklist.md#security-scanning-and-curation-sec), [Content API operation catalog](checklist.md#content-api-operation-catalog-api), [Docker image format](checklist.md#docker-image-format-pkg-docker), [OCI images and artifacts](checklist.md#oci-images-and-artifacts-pkg-oci), [Helm chart format](checklist.md#helm-chart-format-pkg-helm), [Maven package format](checklist.md#maven-package-format-pkg-maven), [PyPI package format](checklist.md#pypi-package-format-pkg-pypi), [Go modules format](checklist.md#go-modules-format-pkg-go), [Generic format](checklist.md#generic-format-pkg-generic), [Ansible collection format](checklist.md#ansible-collection-format-pkg-ansible), [Bazel modules package format](checklist.md#bazel-modules-package-format-pkg-bazel), [Gradle, Ivy, sbt and Eclipse P2 formats](checklist.md#gradle-ivy-sbt-and-eclipse-p2-formats-pkg-jvm), [Beyond the baseline: further formats and capabilities](checklist.md#beyond-the-baseline-further-formats-and-capabilities-beyond); plus 28 pages without MVP or P1 entries.
<!-- coverage-gen:end audiences -->

What the open entries are, on 2026-10-29:

- **npm-only team.** All MVP entries on its pages are specified. The npm page itself is fully
  specified at MVP and P1. All 48 open entries are P1, and most of them fall into two groups:
  - Search operations: only 3 of the 24 P1 entries on the search page are specified:
    - the global search box;
    - artifact browser search;
    - format-specific metadata search fields, through R-NPM-037.
  - Build metadata: all 15 P1 entries are open, because the page that will own build records is
    not written yet. The development agent decided that build records get their own page,
    `builds.md` with prefix `R-BLD`. The page is written before P1 build work starts, in milestone
    M9 of the [roadmap](roadmap.md). This one decision answers three questions, each with option
    (a): Q-AUTHZ-8 in [authorization.md](../spec/authorization.md), Q-API-5 in
    [api-and-ui.md](../spec/api-and-ui.md) and Q-STOR-3 in [storage.md](../spec/storage.md).

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
  - role and environment operations of the administration API (ADMAPI-019, ADMAPI-066,
    ADMAPI-068). [authorization.md](../spec/authorization.md) and
    [api-and-ui.md](../spec/api-and-ui.md) defer them to P2 together with roles and
    environments. They stay open here because the catalog labels them P1;
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
   headings on spec pages (decided questions `Q-…`, gaps `H-…`, examples) are not requirements.
3. **Status.** A feature is *specified* when its ID appears in the `Covers:` line of at least one
   requirement, and *open* otherwise. *Implemented* follows the rule of
   [roadmap Q-ROAD-4](roadmap.md#q-road-4--how-the-coverage-generator-learns-implementation-status):
   - A requirement is implemented when a test whose name carries the requirement ID passes on
     `main`. The generator reads that from CI's `go test -json` output and the end-to-end
     reports.
   - A feature is implemented only when every requirement that lists it in its `Covers:` line is
     implemented. A feature that is partly built stays *specified*.

   The generator reads the evidence from test reports (`-tests`) and records the result in
   [implemented.txt](implemented.txt), one requirement ID per line. A failing test that names a
   requirement vetoes it. On 2026-10-29 the list is empty, because the repository holds no test
   that names a requirement.
4. **Shares.** Plain counts: each feature counts once, whatever its size. The in-scope share
   leaves out *Out of scope* entries.
5. **Audiences.** For each audience: the MVP and P1 entries of the pages in its row of the
   audience table. The page lists are fixed in the generator, and the table above shows them.
6. **Checks that stop generation** (exit code 1):
   - a duplicate catalog ID, requirement ID, `Q-` ID or `H-` number;
   - a `Covers:` ID that is not in the catalog;
   - a `Covers:` token that is neither an ID nor `trove-only`, or `trove-only` mixed with IDs;
   - an entry or requirement heading without its priority line directly below, or with a
     priority outside the five rubric values;
   - an unclosed code fence;
   - a missing marker pair in this file;
   - an audience page without entries;
   - a test name that carries a requirement ID no spec page defines, or an ID in
     `implemented.txt` that no spec page defines.

   When it runs, the generator prints the counts per priority for the catalog and the spec, the
   number of `Covers:` references, the implemented counts and every heading it skipped inside a
   code fence.
7. **Output.** It writes all of [checklist.md](checklist.md), [implemented.txt](implemented.txt),
   and only the four marked blocks of this file.

**The generator.** It is the Go program `internal/tools/coverage`, run with
`go run ./internal/tools/coverage`. `-check` compares without writing and exits
2 when a committed file is stale, `-tests <report>` learns the implementation status, and `-links`
checks the relative links and anchors of every Markdown file. The modes, the test naming rule
and the report formats are in
[docs/developer/coverage.md](../developer/coverage.md).

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
  only, and every other row keeps its own format's priority. R-AUTH-033 *Client authentication
  matrix* in
  [identity-and-authentication.md](../spec/identity-and-authentication.md#r-auth-033--client-authentication-matrix),
  which covers FMT-013, follows the same split. Its table *Authentication styles across formats*
  maps every client style of FMT-013 to the one authentication stage (R-AUTH-001) and settles the
  npm and NuGet rows at MVP. A client that cannot authenticate is served only as the anonymous
  principal (R-AUTH-037), never through a per-repository bypass. The other rows are left to their
  format pages under the same rules. The other FMT entries were not checked for the same pattern.
  For such an entry, *specified* means at most the npm and NuGet part.
- **The catalog's limits.** The catalog was written from public product documentation, so
  behavior that documentation leaves out is missing from it. Entries differ in granularity by
  page: one per operation on the API pages, one per protocol capability on the format pages. If
  the catalog grows, every share on this page moves without any change to trove. The catalog is
  also a measure of breadth, not of design quality. The 81 `trove-only` requirements
  (54 before 2026-10-28) are design work it does not measure at all, for example
  R-REPO-038 *Egress address rules for remote repositories*, R-STOR-013 *Download integrity
  verification* and R-HA-057 *Valkey failover, outage and degraded mode*. 27 of the 62
  requirements that the owner constraints of 2026-10-28 added are of this kind, among them the
  Valkey client with its degraded mode and each page's rules for its state across replicas and in
  backups, for example R-NPM-046 and R-NPM-047.
- **Priorities are trove's own judgement** and can change. When a priority changes, the MVP
  share changes with it, even if no design or code changes. The owner constraints of 2026-10-28
  are such a case: they raised eight requirements from P1 to MVP, among them the platform backup
  layer, the point-in-time restore and the revocation journal (R-HA-023, R-HA-025, R-HA-054),
  without any change to the catalog. The catalog is not consistent about projects, roles and
  environments:

  | Concept | On the identity page | Elsewhere in the catalog |
  |---|---|---|
  | Projects | P2 (IAM-051) | P1: project settings of repositories (REPO-013, REPO-045) |
  | Roles | P2 (IAM-054 global roles, IAM-055 project roles) | P1: role operations (ADMAPI-019, ADMAPI-066) |
  | Environments | P2 (IAM-063) | P1: environment operations (ADMAPI-068); REPO-013 also assigns environments |

  The spec follows the P1 side for projects (R-AUTHZ-026) and the identity page's P2 for roles and
  environments. [authorization.md](../spec/authorization.md) and
  [api-and-ui.md](../spec/api-and-ui.md) defer ADMAPI-019, ADMAPI-066 and ADMAPI-068 to P2, each
  with a reason, and the fixed system roles of R-AUTHZ-010 cover P1. Five of the open P1 entries
  come from this disagreement:
  - The three deferred ADMAPI entries stay open on purpose until the catalog's priority follows
    the spec.
  - REPO-013 and REPO-045 are assigned to R-AUTHZ-026 by
    [repositories-and-proxy.md](../spec/repositories-and-proxy.md), but no requirement lists them
    in its `Covers:` line yet. The roadmap places that addition in M10.
- **Every design decision is the development agent's until the walkthrough.** The development
  agent decided each question on the spec pages and in the roadmap by selecting one of the
  documented options. The owner reviews the choices at the
  [walkthrough](roadmap.md#owner-walkthrough) after the MVP milestones. A choice the owner
  overturns there can move requirements between priorities, and with them every number on this
  page, without any change to the catalog.
- **Audiences are approximated at page level.** A page counts in full for an audience even when
  some of its entries belong to another team's work. For example, identity-and-access counts for
  the developer teams although most of it is administration. Some pages count for no audience;
  the line under the audience table lists them. Among them, the content API is the only page with
  MVP entries, and all 11 of those are specified. The catalog has no audience tag per feature;
  adding one would make these rows exact.

## Effort

Not estimated. Nothing calibrates one yet: no requirement is implemented and the repository holds
no code, so there is no measured cost per requirement or per feature. Multiplying the MVP
requirement count by an assumed cost per requirement would be a guess presented as a derivation.

A defensible estimate needs two steps:

1. Build a first vertical slice: an npm local, remote and virtual repository behind OIDC login and
   tokens, on PostgreSQL and S3.
2. Record agent-hours per implemented requirement, by spec page. Extrapolate to the remaining
   requirements of each page, and use the observed spread as the range.

The size basis on 2026-10-29, from the tables above:

- 408 MVP, 143 P1 and 2 P2 requirements on 12 spec pages, plus the MVP requirements of the
  generic local class that M1 adds (roadmap Q-ROAD-1);
- 222 P1 catalog entries that no requirement covers yet.

The 66 MVP requirements that the owner constraints of 2026-10-28 added or raised (Valkey, the
role split, statelessness, drains, backup and restore, the revocation journal) are mostly
cross-cutting. Their cost per requirement is likely to differ from that of a protocol
requirement, so the first slice should include one of them, for example R-ARCH-033 (request
collapsing through Valkey), before any extrapolation.

## Related pages

- [checklist.md](checklist.md): every catalog feature with its status and covering requirements.
- [Feature catalog](../features/README.md): entry format and priority rubric.
- [Specification](../spec/README.md): owner constraints, design invariants, requirement format.
