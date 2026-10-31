# trove · Full product plan after the beta

**Status:** ⚪ not started · 🟡 in progress or partly done · 🟢 done
**Last updated:** 2026-10-31

Everything that goes beyond the beta release in [BETA.md](BETA.md): the beta phase, the remaining
registry types, and platform features. Every completed block or milestone ships the next minor
version after the beta (`v0.9.0`, `v0.10.0`, …); trove stays on major version 0 until the owner
decides otherwise. Order and scope follow the
[roadmap](../coverage/roadmap.md); per-feature detail is in the [feature catalog](../features/README.md)
and, once written, in the [spec](../spec/README.md).

Every registry needs **integration tests** (protocol over HTTP against PostgreSQL, Valkey with
Sentinel and S3 in containers, plus the format conformance kit) and **e2e tests** (its real clients
against trove deployed by the Helm chart on several replicas, through local, remote and virtual
repositories) before it counts as done.

## Where we are

| Block | Content | Status |
|---|---|---|
| [A · Beta phase](#a--beta-phase) | Beta feedback, upgrade path, import tooling, user documentation | ⚪ |
| [B · Registries after the beta](#b--registries-after-the-beta-p1) | Ansible Galaxy first, then generic, PyPI, Helm, OCI/Docker, Go, Maven | ⚪ |
| [C · Platform features](#c--platform-features-p1) | npm/NuGet completion, retention, search, webhooks, identity breadth, HA breadth, curation, trove Bastion | ⚪ |
| [D · Further registries](#d--further-registries-p2) | 37 more registry types | ⚪ |
| [E · Further platform work](#e--further-platform-work-p2) | Replication, query language, more storage back ends, … | ⚪ |
| [F · Later](#f--later) | Federation, distribution, license scanning and SBOMs | ⚪ |
| [G · Beyond the catalog](#g--beyond-the-catalog) | Signing and provenance, quotas, cache pre-warming, … | ⚪ |

---

## A · Beta phase

- ⚪ Beta feedback collected, triaged and worked off
- ⚪ Upgrade path from the beta to the next releases tested (rolling upgrade, data kept)
- ⚪ Import tooling: copy existing packages from the registries trove replaces (npm, NuGet, Ansible Galaxy) into trove (spec page to write)
- ⚪ User documentation: getting started, client setup per registry, administration, operations runbook
- ⚪ Next `v0` minor release

## B · Registries after the beta (P1)

Each registry: spec page → implementation → integration tests → e2e tests → client setup in the UI.
Ansible Galaxy goes first because it replaces a system the owner runs today. The other orders
follow the [roadmap's format order](../coverage/roadmap.md#p1-format-expansion-order).

### F0 · Ansible Galaxy (replaces the existing Galaxy)
- ⚪ Spec page `formats/ansible.md` ([catalog](../features/formats/ansible.md))
- ⚪ Local: publish and install collections and roles
- ⚪ Remote: proxy of the public Galaxy
- ⚪ Virtual: one endpoint for internal and public content
- ⚪ Authentication with trove tokens (`ansible.cfg`, `ansible-galaxy --token`)
- ⚪ Migration of the content of the existing Galaxy instance
- ⚪ Integration tests
- ⚪ E2E tests: `ansible-galaxy collection install/publish`, `ansible-galaxy role install`, `requirements.yml` through local, remote and virtual repositories
- ⚪ Client setup instructions in the UI

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
- ⚪ Spec pages `formats/oci.md` and `formats/docker.md`
- ⚪ Registry API under `/v2/`, token challenge, chunked uploads across replicas
- ⚪ Local, remote (Docker Hub and other registries), virtual; multi-architecture images; referrers
- ⚪ Clients: Docker CLI, Podman, BuildX, ORAS, Cosign
- ⚪ Image vulnerability scanning: operating-system and language packages inside images, with the findings, report and blocking of beta phase 6; the analyser is decided on the spec page ([vulnerability-scanning.md](../spec/vulnerability-scanning.md#deferred))
- ⚪ Integration tests
- ⚪ E2E tests: push and pull with Docker and Podman through local, remote and virtual repositories, an upload interrupted by a drain

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
- ⚪ Further administrator roles: security administrator, auditor, repository and webhook administrators
- ⚪ Delegated administration: projects, delegated policy and CI-mapping management
- ⚪ Token rotation and last-used tracking; browser login handoff for other command-line tools
- ⚪ Further CI identity providers: GitHub Actions and generic OIDC issuers; CI setup snippets
- ⚪ npm trusted publishing and provenance from CI

### M11 · HA and deployment breadth
- ⚪ Worker pools: further splits of the worker role by job queue
- ⚪ Valkey primary-endpoint mode (managed Valkey without Sentinel access)
- ⚪ Separate origin for package content in the browser
- ⚪ Restore that reuses an existing blob store
- ⚪ Container installation without Kubernetes (documented, not HA)

### M12 · Upstream curation
- ⚪ Curation policies at the remote-repository boundary: vulnerable and too-new versions, dry run, waivers, compliant-version fallback (spec page to write); blocking of malicious versions and the advisory data source ship in beta phase 6
- ⚪ Further risk facts for curation (licenses, end of life, project health); curation audit

### M13 · trove Bastion

The owner's request of 2026-10-31 ([spec README](../spec/README.md#owner-constraints)): a separately installable cache
instance in front of a main installation.

- ⚪ Spec page `docs/spec/bastion.md` first: deployment profile, trust between Bastion and main installation, the cache, and
  what the Bastion refuses; decisions on how a Bastion checks credentials and
  permissions (asking the main installation, with a bounded cache of the answers, versus holding a copy), and on the
  state it keeps (cache only, no copy of users, tokens or policies). Catalog entries it pulls forward: the chained
  remote repository RMT-040 to RMT-046 (P2) and the edge deployment profile and its trust pairing DIST-024 and DIST-025
  (Later); their priorities are raised with the page
- ⚪ Own Helm chart (`trove-bastion`) with its own Ingress, Service, NetworkPolicy and TLS Secret; horizontal replicas
  without PostgreSQL of its own if the design allows, otherwise the smallest state that keeps the replicas consistent
- ⚪ Same authentication as the main installation: personal, service-account and CI tokens and the same login, with
  revocation reaching the Bastion within a stated bound; permissions and exposure rules of the main installation
  enforced on every request, cached content included
- ⚪ Package cache: immutable content (tarballs, `.nupkg`) kept and served without asking the main installation again,
  metadata revalidated against it with conditional requests; collapsing of concurrent misses; behavior while the main
  installation is unreachable (stale metadata, cached content) as for remote repositories
- ⚪ Read-only for package content (owner, 2026-10-31): publish, unpublish and tag writes answer in each protocol's shape and
  point at the main installation; client setup snippets for a Bastion
- ⚪ Observability: cache hit rate, bytes served from the cache, upstream requests to the main installation
- ⚪ Integration and e2e tests: npm and NuGet clients against a Bastion on kind in front of a main installation, cold and
  warm cache, main installation unreachable, token revoked at the main installation, drain of a Bastion replica

## D · Further registries (P2)

Order within the families follows the [roadmap](../coverage/roadmap.md#p2-breadth). Every row needs
all four columns green.

| Registry | Spec page | Implementation | Integration tests | E2E tests |
|---|---|---|---|---|
| NuGet v2 (OData; also Chocolatey, PowerShell) | ⚪ | ⚪ | ⚪ | ⚪ |
| Gradle (rest), Ivy, sbt | ⚪ | ⚪ | ⚪ | ⚪ |
| Eclipse P2 (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| Model-serving containers (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
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
| Debian | ⚪ | ⚪ | ⚪ | ⚪ |
| RPM | ⚪ | ⚪ | ⚪ | ⚪ |
| Alpine Linux | ⚪ | ⚪ | ⚪ | ⚪ |
| Opkg | ⚪ | ⚪ | ⚪ | ⚪ |
| Hugging Face models and datasets | ⚪ | ⚪ | ⚪ | ⚪ |
| Git LFS | ⚪ | ⚪ | ⚪ | ⚪ |
| Version-control archives (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| Editor extension marketplace (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| JetBrains IDE plugins (remote) | ⚪ | ⚪ | ⚪ | ⚪ |
| AI agent plugins | ⚪ | ⚪ | ⚪ | ⚪ |
| AI agent packages | ⚪ | ⚪ | ⚪ | ⚪ |
| Agent skills | ⚪ | ⚪ | ⚪ | ⚪ |
| Vagrant | ⚪ | ⚪ | ⚪ | ⚪ |
| Nix | ⚪ | ⚪ | ⚪ | ⚪ |
| Bazel modules | ⚪ | ⚪ | ⚪ | ⚪ |
| Terraform state backend | ⚪ | ⚪ | ⚪ | ⚪ |

## E · Further platform work (P2)

- ⚪ Push and pull replication between installations
- ⚪ Query language for files, packages and properties
- ⚪ Presigned download redirects for large artifacts
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
