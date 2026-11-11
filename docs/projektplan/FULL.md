# trove · Full product plan after the beta

**Status:** ⚪ not started · 🟡 in progress or partly done · 🟢 done
**Last updated:** 2026-11-10

Everything that goes beyond the beta release in [BETA.md](BETA.md): the beta phase, the remaining
registry types, and platform features. Releases after the beta are cut by Semantic Release from the
commits on `main`, as before it; no block or milestone has a version number expected beforehand, and
trove stays on major version 0 until the owner decides otherwise
([ADR-124](../decisions.md#adr-124--releases-stay-on-major-version-0)). Order and scope follow the
[roadmap](../coverage/roadmap.md); per-feature detail is in the [feature catalog](../features/README.md)
and, once written, in the [spec](../spec/README.md).

Every registry needs **integration tests** (protocol over HTTP against PostgreSQL, Valkey with
Sentinel and S3 in containers, plus the format conformance kit) and **e2e tests** (its real clients
against trove deployed by the Helm chart on several replicas, through local, remote and virtual
repositories) before it counts as done.

## Where we are

| Block | Content | Status |
|---|---|---|
| [A · Beta phase](#a--beta-phase) | Beta feedback, upgrade path, import tooling, user documentation | 🟡 |
| [B · Registries after the beta](#b--registries-after-the-beta-p1) | Ansible Galaxy first, then generic, PyPI, Helm, OCI/Docker, Go, Maven | 🟡 |
| [C · Platform features](#c--platform-features-p1) | npm/NuGet completion, retention, search, webhooks, identity breadth, HA breadth, curation | ⚪ |
| [D · Further registries](#d--further-registries-p2) | 37 more registry types | ⚪ |
| [E · Further platform work](#e--further-platform-work-p2) | Replication, query language, more storage back ends, … | ⚪ |
| [F · Later](#f--later) | Federation, distribution, license scanning and SBOMs | ⚪ |
| [G · Beyond the catalog](#g--beyond-the-catalog) | Signing and provenance, quotas, cache pre-warming, … | ⚪ |

---

## A · Beta phase

- ⚪ Beta feedback collected, triaged and worked off
- ⚪ Upgrade path from the beta to the next releases tested (rolling upgrade, data kept)
- 🟡 Import tooling: copy existing packages from the registries trove replaces into trove ([import.md](../spec/import.md), R-IMP-001 to R-IMP-009). Built: `trove admin import` for npm and NuGet, with integration tests. Open: Ansible Galaxy (waits for that format), and e2e rows with real clients installing imported packages
- 🟢 User documentation: getting started, client setup per registry, administration, backup and restore, operations runbook ([docs/manual](../manual/README.md))
- ⚪ The release that closes the beta phase (cut by Semantic Release)

## B · Registries after the beta (P1)

Each registry: spec page → implementation → integration tests → e2e tests → client setup in the UI.
Ansible Galaxy goes first because it replaces a system the owner runs today. The other orders
follow the [roadmap's format order](../coverage/roadmap.md#p1-format-expansion-order).

### F0 · Ansible Galaxy (replaces the existing Galaxy)
- 🟢 Spec page [formats/ansible.md](../spec/formats/ansible.md) ([catalog](../features/formats/ansible.md)), the cut into work packages in [ansible-work-packages.md](ansible-work-packages.md)
- 🟢 Local: publish (multipart or raw body, import tasks) and install of collections, immutability and tombstones, reindex, the package screens; integration area `ansiblelocal`. Roles only through remote and virtual repositories (Q-ANS-6)
- 🟢 Remote: proxy of the public Galaxy: pull-through of collections (paged version lists, the announced digest verified, the identity inside the archive checked) and of roles by translation from allowed hosts, the mirror of the collections the patterns select (Q-ANS-52); integration areas `ansibleremote` and `mirror`. 🟡 The Automation Hub's token exchange (Q-ANS-51), also for the mirror's listing, is tested against the stub realm only (H-842)
- 🟢 Virtual: one endpoint for internal and public content: merged version lists, first match, publish through the deploy target, versions a path grant hides stay hidden through the virtual repository; integration area `ansiblevirtual`
- 🟢 Authentication with trove tokens: `ansible.cfg` with the token variable, `--token` with `--server`, Basic, and netrc for roles (R-ANS-006, Q-ANS-54); e2e row `Auth` on both clients
- 🟢 Migration of the content of the existing Galaxy instance: `trove admin import ansible` for collections, integration-tested against the stub and run against the owner's galaxy_ng on 2026-11-11: the dry run, then the copy of its 3 collections and 7 versions into `test/galaxy` of the owner's installation (0 conflicts, 0 failures), and `ansible-galaxy` 2.21 installed two of them from there
- 🟢 Integration tests: `ansiblelocal`, `ansibleremote`, `ansiblevirtual`, the mirror rows of `mirror`, the importer, the conformance kit for all three classes, against the counting stub `test/ansiblestub`
- 🟢 E2E tests: the set `ansible` with ansible-core 2.17.14 and 2.21.6 (collection publish and install, `requirements.yml`, `role install`, digest refusal, pasted snippets, Valkey stopped, virtual and mirror rows) passed on kind on 2026-11-10, 67 tests and subtests, and the CI leg `e2e (ansible)` ran green on the push of 47f5df19 (Test and Release 42902356913). One name with versions in both a local and a remote member is not run with the real clients (H-868)
- 🟢 Client setup instructions in the UI: the setup page offers `ansible-galaxy` with eight snippets, the guide per class, the repository screens with import tasks and tombstones; journey `20-ansible`

### F1 · Generic files: remote and virtual
- ⚪ Spec page `formats/generic.md` extended
- ⚪ Remote: proxy any HTTP file source; Virtual: one path over several repositories
- ⚪ Integration tests
- ⚪ E2E tests: large files to many clients with a single upstream request, upload through a virtual repository

### F2 · PyPI (Python)
- ⚪ Spec page `formats/pypi.md`
- ⚪ Local, remote (pypi.org), virtual; simple index (HTML and JSON)
- ⚪ Clients: pip, Twine, Poetry, uv
- ⚪ Integration tests
- ⚪ E2E tests: install and publish with pip, Twine, Poetry and uv through local, remote and virtual repositories

### F3 · Helm charts (index-based repositories)
- ⚪ Spec page `formats/helm.md`
- ⚪ Local, remote, virtual; merged `index.yaml`
- ⚪ Integration tests
- ⚪ E2E tests: `helm repo add`, `helm push`, `helm install` through local, remote and virtual repositories

### F4 · OCI and Docker (container images)
- 🟢 Spec page `formats/oci.md` (one format `oci` for Docker and OCI content, Q-OCI-1 and Q-OCI-2), with the contracts, schema and format of WP0
- 🟢 Registry API under `/v2/` (all four categories of the distribution specification, conformance suite v1.1.1), token service, chunked uploads across replicas
- 🟢 Local, remote (Docker Hub and other registries; `pullThrough` and `mirror`), virtual; multi-architecture images; referrers; deletion and the sweep of unreferenced blobs; the Docker Engine mirror of a bound host
- 🟡 Clients: Docker CLI with Buildx multi-platform push, Podman and Kubernetes pulls built; BuildKit registry caches, ORAS and Cosign beyond referrers stay
- ⚪ WebAssembly modules as OCI artifacts (`wasm-to-oci`)
- 🟢 Image vulnerability scanning: operating-system and language packages inside images (Trivy in client mode in a worker job), with the findings, report and blocking of beta phase 6 ([vulnerability-scanning.md](../spec/vulnerability-scanning.md#images))
- 🟢 Integration tests (protocol over HTTP across three replicas, the counting stub registry, the conformance suite)
- 🟢 E2E tests: push and pull with Docker and Podman through local, remote (both modes) and virtual repositories on several replicas, an upload interrupted by a drain (nightly set `oci-long`), a mirror pass on n replicas, the closing set `oci-closing` (Valkey stopped, Docker Hub read-only)
- ⚪ Tag caps and kept tag revisions (with the repository caps), promotion, foreign layers, tag resolution by push time, labels and layer history, exposure rules and the Bastion for the format

### F5 · Helm charts stored as OCI artifacts
- ⚪ Chart artifact type on top of F4
- ⚪ Integration tests
- ⚪ E2E tests: `helm push` and `helm install oci://…` through a virtual repository

### F6 · Go modules
- ⚪ Spec page `formats/go.md`
- ⚪ Module proxy protocol; local upload path; remote (proxy.golang.org); virtual
- ⚪ Integration tests
- ⚪ E2E tests: `go mod download` and `go get` through local, remote and virtual repositories

### F7 · Maven (with Gradle basics)
- ⚪ Spec page `formats/maven.md`
- ⚪ Releases and snapshots, metadata calculation, checksum files, layouts
- ⚪ Integration tests
- ⚪ E2E tests: `mvn deploy` and dependency resolution with Maven and Gradle through local, remote and virtual repositories

## C · Platform features (P1)

Milestones of the [roadmap](../coverage/roadmap.md#p1-platform-milestones); they can run in
parallel.

### M6 · npm and NuGet completion
- ⚪ `npm login` (web and legacy flows), `npm search`, deprecate, single-version documents
- ⚪ npm audit forwarding to the upstream as an opt-in (trove answers audits from its own advisory data from beta phase 6), external dependency rewrite, metadata recalculation, upload without the npm client
- ⚪ Further upstream registries with their own credentials
- ⚪ NuGet autocomplete, symbol server and symbol proxy
- ⚪ NuGet vulnerability information passed through from the upstream and in registrations (trove's own `VulnerabilityInfo` ships in beta phase 6), embedded icon, license and README, metadata reindex
- ⚪ Revisit pnpm 11 and 12 unpublishing through a virtual repository (Q-NPM-49 point 5): whether a write through a virtual repository may accept the merge's _rev for packages only the deployment target holds
- ⚪ Integration and e2e tests extended for every addition

### M7 · Storage lifecycle and retention
- ⚪ Cleanup and retention policies with dry run
- ⚪ Storage accounting and quotas
- ⚪ Cleanup of unused cached remote content; trash can

### M8 · Content operations and search
- ⚪ Copy, move and properties on files and packages
- ⚪ Checksum files, folder listings, chunked upload sessions
- ⚪ Search API and search in the UI (spec page to write)

### M9 · Automation and integration
- ⚪ Webhooks with signed payloads, retries and delivery log
- ⚪ Configuration as code (declarative repositories, policies, CI mappings)
- ⚪ Build metadata (spec page to write)
- ⚪ Notifications

### M10 · Identity, access and CI breadth
- ⚪ SCIM provisioning of users and groups; auto-join groups; deactivation of dormant users
- ⚪ Further administrator roles: security administrator, repository and webhook administrators (the auditor is in the beta, owner 2026-11-02)
- ⚪ Delegated administration: delegated policy and CI-mapping management (projects moved into the beta as [phase 5a](BETA.md#phase-5a--projects-and-delegated-administration), owner 2026-11-02)
- ⚪ Token rotation and last-used tracking; browser login handoff for other command-line tools
- ⚪ Further CI identity providers: GitHub Actions and generic OIDC issuers; CI setup snippets
- ⚪ npm trusted publishing and provenance from CI
- ⚪ **To be discussed with the owner, not decided:** resolving a GitLab group's ID to the IDs of its subgroups, so a
  mapping can pin a whole group by immutable ID (GitLab's ID tokens carry only the direct namespace's ID, H-27, observed on
  GitLab 19.0.1). Parked by the owner on 2026-11-02, explicitly without confirming that it is built. His first thought:
  each trove user would have to bring their own GitLab token for it, because a lookup with a credential of trove's own
  could show a user groups and projects that the user cannot see in GitLab. Until then the beta pins groups by a list of
  `namespace_id`s or, on a self-managed instance, by a path glob ([gitlab-ci.md](../manual/admin/gitlab-ci.md))

### M11 · HA and deployment breadth
- ⚪ Worker pools: further splits of the worker role by job queue
- ⚪ Valkey primary-endpoint mode (managed Valkey without Sentinel access)
- ⚪ Separate origin for package content in the browser
- ⚪ Restore that reuses an existing blob store
- ⚪ Container installation without Kubernetes (documented, not HA)

### M12 · Upstream curation
- ⚪ Curation policies at the remote-repository boundary: vulnerable and too-new versions, dry run, waivers, compliant-version fallback (spec page to write); blocking of malicious versions, blocks set in triage and the advisory data source ship in beta phase 6, and a policy-based severity threshold, which the beta does not have, may come here ([Q-VULN-20](../spec/vulnerability-scanning.md#q-vuln-20--which-findings-block-a-download-and-who-sets-and-lifts-a-block))
- ⚪ Further risk facts for curation (licenses, end of life, project health); curation audit

### M13 · trove Bastion

Moved into the beta as [phase 5b](BETA.md#phase-5b--trove-bastion-m13) (owner, 2026-11-02).

## D · Further registries (P2)

Order within the families follows the [roadmap](../coverage/roadmap.md#p2-breadth). Every row needs
all four columns green.

| Registry | Spec page | Implementation | Integration tests | E2E tests |
|---|---|---|---|---|
| NuGet v2 (OData; also Chocolatey, PowerShell) | ⚪ | ⚪ | ⚪ | ⚪ |
| Gradle (rest), Ivy, sbt | ⚪ | ⚪ | ⚪ | ⚪ |
| Eclipse P2 (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| Model-serving containers, NVIDIA NIM (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| Cargo (Rust) | ⚪ | ⚪ | ⚪ | ⚪ |
| RubyGems | ⚪ | ⚪ | ⚪ | ⚪ |
| Terraform / OpenTofu registry (modules, providers) | ⚪ | ⚪ | ⚪ | ⚪ |
| PHP Composer | ⚪ | ⚪ | ⚪ | ⚪ |
| Conan (C/C++) | ⚪ | ⚪ | ⚪ | ⚪ |
| Conda | ⚪ | ⚪ | ⚪ | ⚪ |
| Dart / Flutter pub | ⚪ | ⚪ | ⚪ | ⚪ |
| Hex (Elixir) | ⚪ | ⚪ | ⚪ | ⚪ |
| Swift | ⚪ | ⚪ | ⚪ | ⚪ |
| CRAN (R) | ⚪ | ⚪ | ⚪ | ⚪ |
| LuaRocks | ⚪ | ⚪ | ⚪ | ⚪ |
| Puppet | ⚪ | ⚪ | ⚪ | ⚪ |
| Chef | ⚪ | ⚪ | ⚪ | ⚪ |
| CocoaPods | ⚪ | ⚪ | ⚪ | ⚪ |
| Bower | ⚪ | ⚪ | ⚪ | ⚪ |
| Debian / APT repositories (Debian and Ubuntu; apt, apt-get) | 🟢 [formats/debian.md](../spec/formats/debian.md) | 🟢 local, remote (`pullThrough`, `mirror`), virtual ([deb.md](../developer/deb.md)) | 🟢 `deblocal`, `debkeys`, `debremote`, `debvirtual`, the Debian files of `mirror`, `querybudget`, `postgres`, `itempublish`; the conformance kit with no skip | 🟢 set `deb`, CI job `e2e-deb` ([apt-work-packages.md](apt-work-packages.md#24--closing)) |
| RPM | ⚪ | ⚪ | ⚪ | ⚪ |
| Alpine Linux | ⚪ | ⚪ | ⚪ | ⚪ |
| Opkg | ⚪ | ⚪ | ⚪ | ⚪ |
| Hugging Face models and datasets | ⚪ | ⚪ | ⚪ | ⚪ |
| Git LFS | ⚪ | ⚪ | ⚪ | ⚪ |
| Version-control archives (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| VS Code compatible extension galleries: VS Code, Cursor, Kiro, Windsurf / Devin (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| JetBrains IDE plugins (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| AI agent plugins (Claude Code, Cursor, Codex, VS Code) | ⚪ | ⚪ | ⚪ | ⚪ |
| AI agent packages (APM) | ⚪ | ⚪ | ⚪ | ⚪ |
| Agent skills | ⚪ | ⚪ | ⚪ | ⚪ |
| Vagrant | ⚪ | ⚪ | ⚪ | ⚪ |
| Nix | ⚪ | ⚪ | ⚪ | ⚪ |
| Bazel modules | ⚪ | ⚪ | ⚪ | ⚪ |
| Terraform state backend | ⚪ | ⚪ | ⚪ | ⚪ |

- Debian / APT: built — local, remote (`pullThrough` and `mirror`) and virtual repositories; uploads with suites and components;
  indexes built by a job as signed snapshots served by hash; a signing key per repository with co-signing rotation, imported keys and a
  restore that withholds keys instead of reviving or erasing them; remotes that pass upstream releases through, verify them against
  `deb.upstreamKeys` and never store an older one; the mirror of named suites, components and architectures; virtual repositories that
  merge verified member releases and re-sign; the screens, the setup and the guides; clients Debian 13 and 12, Ubuntu 24.04 and 22.04.
  Evidence: [apt-work-packages.md, Closing](apt-work-packages.md#24--closing). Stays: local source packages, snapshots, debug-symbol and
  installer packages, `dput`, translations and `Contents` for local and virtual repositories, mirrors of flat archives, `Valid-Until` on
  trove's own releases, keys in a key service, Debian vulnerability matching, copy and move, exposure rules, the Bastion and nested virtual
  repositories (debian.md, Deferred); the nightly set `deb-long`, the Debian pass beside clients and under a drain in the e2e sets, a build
  trigger on a change of claims (H-725) and the metric `trove_deb_remote_parse_total`.

## E · Further platform work (P2)

- ⚪ Push and pull replication between installations
- ⚪ Query language for files, packages and properties
- ⚪ Presigned download redirects for large artifacts
- ⚪ Client-side encryption of the stored content in the format of s3-encryption-proxy, imported as a module; trove then always
  streams downloads itself ([R-STOR-048](../spec/storage.md#r-stor-048--client-side-encryption-of-stored-content), the ticket with its open decisions)
- ⚪ Further storage back ends (Google Cloud Storage, Azure Blob Storage), sharding over several buckets
- ⚪ Email notice of expiring tokens
- ⚪ TLS from the edge proxy to the pods
- ⚪ Read-only queries on PostgreSQL standbys
- ⚪ Worker autoscaling on queue depth
- ⚪ Repository export and import
- ⚪ Build promotion
- ⚪ Further curation conditions (licenses, waiver workflow)
- ⚪ `CI_JOB_TOKEN` fallback for pipelines that cannot use ID tokens (only on demand)

## F · Later

- ⚪ Federation of repositories across sites, active-active across sites
- ⚪ Release sets, distribution to edge instances, lifecycle stages, evidence
- ⚪ License scanning and compliance, SBOM generation and export, content analyses (secrets, applicability), scan policies and scopes, on top of the advisory matching of beta phase 6
- ⚪ Machine-learning model repositories with a proprietary SDK client
- ⚪ OSTree hosting, TUF-signed metadata, checksum database for hosted Go modules

## G · Beyond the catalog

Selected from [beyond-baseline.md](../features/beyond-baseline.md):

- ⚪ Minimum release age for remote repositories, per format after npm
- ⚪ Referrers (signatures, SBOMs) through remote and virtual OCI repositories
- ⚪ Provider presets for more CI systems
- ⚪ Per-tenant quotas
- ⚪ Kubernetes service-account token trust
- ⚪ Cache pre-warming from lockfiles and SBOMs
- ⚪ SBOM attachment store
- ⚪ Signature and provenance gates (Sigstore, Notary Project, SLSA)
- ⚪ JSR through its npm compatibility layer
