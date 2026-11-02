# trove · Project plan to the beta release

**Status:** ⚪ not started · 🟡 in progress or partly done · 🟢 done
**Last updated:** 2026-11-02

The way from zero to the beta release. The beta is trove with two package registries, **npm** and
**NuGet (.NET)**, each with local, remote (pull-through) and virtual repositories, running on the
production topology the owner requires: HA PostgreSQL, HA Valkey with Sentinel, S3, separate `web`
and `worker` roles, node drains without outage, and a backup job. Everything after the beta
(further registries such as Ansible Galaxy, PyPI, Docker, and platform breadth) is in
[FULL.md](FULL.md).

The beta release is what the [roadmap](../coverage/roadmap.md) builds in M0 to M5, M4b, the projects of M10 and the Bastion of M13 included, after
the owner walkthrough. Details, exit criteria and requirement IDs of every phase are in the roadmap
section linked under each heading.

**Releases:** Semantic Release cuts the releases from the conventional commits on `main`, on major version 0
([ADR-124](../decisions.md#adr-124--releases-stay-on-major-version-0)). This plan tracks what is built, not release
numbers (owner, 2026-11-02).

## Where we are

| Phase | Result | Status |
|---|---|---|
| [0 · Specification](#phase-0--specification) | Feature catalog, specification, decisions | 🟢 |
| [1 · Foundation](#phase-1--foundation-m0) | Skeleton, HA-shaped local stack, roles, drain-safe shutdown, TLS everywhere, endpoint foundation | 🟢 |
| [2 · Access core](#phase-2--access-core-m1) | Login, tokens, permissions, repositories, admin UI | 🟢 |
| [3 · npm registry](#phase-3--npm-registry-m2) | npm local, remote, virtual | 🟢 |
| [4 · NuGet registry](#phase-4--nuget-net-registry-m3) | NuGet v3 local, remote, virtual | 🟡 |
| [5a · Projects](#phase-5a--projects-and-delegated-administration) | Projects with project administrators who manage their repositories, policies and the technical accounts of external parties | ⚪ |
| [5 · GitLab CI OIDC](#phase-5--gitlab-ci-oidc-m4) | CI jobs get scoped tokens without stored secrets | ⚪ |
| [5b · trove Bastion](#phase-5b--trove-bastion-m13) | An exposed, read-only cache instance in front of the main installation that serves released content to external parties and takes load off it | ⚪ |
| [6 · Vulnerability scanning](#phase-6--vulnerability-scanning-m4b) | Own advisory mirror, findings and vulnerability report, malicious versions blocked, npm and NuGet audits from trove's data | ⚪ |
| [7 · HA, backup, Helm](#phase-7--ha-hardening-backup-and-helm-m5) | Drains and failovers without errors, backup and restore; beta candidate | ⚪ |
| [8 · Walkthrough and beta](#phase-8--walkthrough-and-beta-release) | Owner review, objections fixed; **beta release** | ⚪ |

## Testing rule for every registry

Every registry ships with both test levels before its phase counts as done:

- **Integration tests:** Go tests against real PostgreSQL, Valkey with Sentinel and an S3 store in
  containers. They drive the registry protocol over HTTP (publish, fetch, metadata, errors,
  permissions) and run the format conformance kit.
- **E2E tests:** the real package-manager clients against trove deployed with the Helm chart in a
  local Kubernetes cluster, with several `web` replicas and random request distribution, through
  local, remote and virtual repositories; plus browser tests of the registry's UI screens.

The [test matrix](#test-matrix) at the end tracks both levels per registry.

---

## Phase 0 · Specification

- 🟢 Feature catalog: 2,239 features of a full repository manager, prioritized ([docs/features](../features/README.md))
- 🟢 Specification: 553 requirements on 12 pages ([docs/spec](../spec/README.md))
- 🟢 Every open design question decided with options and rationale ([decisions](../decisions.md#decisions-by-the-development-agent))
- 🟢 Revision for Valkey with Sentinel, horizontal scale, backup job and no ReadWriteMany storage
- 🟢 Roadmap with milestones and testable exit criteria ([roadmap](../coverage/roadmap.md))
- 🟢 Coverage checklist: every MVP feature covered by the spec ([coverage](../coverage/README.md))

## Phase 1 · Foundation (M0)

Details: [roadmap M0](../coverage/roadmap.md#m0--foundation)

**Repository and toolchain**
- 🟢 Go module (Go 1.27.3) with pinned tools, task runner, architecture test
- 🟢 Angular 22 workspace; UI embedded in the Go binary
- 🟢 CI pipeline: lint, unit tests, integration tier, image build, chart lint, one version per tool
- 🟢 Coverage generator as a Go tool in the repository

**Data and storage**
- 🟢 PostgreSQL connection classes, transaction-mode pooler support, migration runner and migration Job
- 🟢 S3 blob store: spooling, hashing, content-addressed commit, multipart transfer, garbage collection
- 🟢 S3 conformance check against the local S3 store

**Coordination across replicas**
- 🟢 Job queue with leases, claimed only by `worker` replicas
- 🟢 Change log and epochs in PostgreSQL for cache invalidation
- 🟢 Valkey client with Sentinel discovery, TLS, ACL and degraded mode

**Server and deployment**
- 🟢 Server shell: roles `web`, `worker`, `all`; configuration; middleware; error model
- 🟢 Drain-safe shutdown: readiness off, endpoint delay, in-flight requests finish, jobs released
- 🟢 Helm chart skeleton: one Deployment per role, Services, PodDisruptionBudgets, topology spread
- 🟢 Local HA-shaped stack on the owner's toolstack: CloudNativePG with standby, valkey-operator with three Sentinels, SeaweedFS, HAProxy Ingress, cert-manager, internal-secrets-operator (dev-only deviation: the operator's default Valkey user, see [local-profile](../developer/local-profile.md))
- 🟢 Metrics, JSON request log, health and readiness endpoints
- 🟢 TLS on every connection, trove's listeners included; certificate rotation without restart
- 🟢 Endpoints: further client listeners with their own port, hosts (one wildcard allowed), base URL and certificates chosen by SNI; the endpoint is the listener's, a foreign host gets `421`; route families per endpoint; host-root binding reserved for later registries ([spec](../spec/endpoints.md))
- 🟢 Helm chart per endpoint: Service or own pool, Ingress with its own class, hosts and TLS Secret, NetworkPolicy per port (the Gateway route per endpoint arrives with the chart's Gateway support, R-HA-038, in phase 7)

**Exit checks**
- 🟢 PostgreSQL and Valkey failover under load without a failed request
- 🟢 Node drain under load without a failed request
- 🟢 No state between requests: test profile with random distribution across replicas
- 🟢 Listener certificate rotated under load without a failed request
- 🟢 A second endpoint with its own host and certificate answers `421` for a foreign host and serves only its route families

## Phase 2 · Access core (M1)

Details: [roadmap M1](../coverage/roadmap.md#m1--access-core)

**Identity and tokens**
- 🟢 OIDC login with a test identity provider (Keycloak in the dev profile, Q-AUTH-35), sessions, CSRF protection, logout
- 🟢 Break-glass administrator and bootstrap commands; the chart mounts its password from a Secret that internal-secrets-operator can generate and rotate (`secrets.breakGlass`)
- 🟢 Users, groups (from the login provider's claims) and service accounts
- 🟢 Personal and service-account tokens (`trv_pat_`, `trv_sat_`): scopes, lifetime, revocation
- 🟢 Brute-force protection with cluster-wide counters

**Permissions**
- 🟢 Permission policies: principals, repositories, name patterns, actions
- 🟢 Evaluation engine with caches that stay correct across replicas
- 🟢 Audit of logins, token changes, policy changes and denied requests

**Endpoints and exposure**
- 🟢 Accepted credentials per endpoint; tokens bound to endpoints
- 🟢 Exposure rules per endpoint (repository, package and version patterns, version ranges, pre-releases, tags) as a ceiling for every caller, administrators included; hidden content answers 404
- 🟢 Management API and admin screen for endpoints and exposure rules, with an exposure check
- 🟢 Raw files on an endpoint filtered by exposure; rate limit per endpoint

**Repositories and files**
- 🟢 Repository model and management API (local, remote, virtual); the generic format implements the local class, remote and virtual repositories take shape with npm in phase 3
- 🟢 Generic local repositories and raw file transfer under `/files/`
- 🟢 API conventions: errors, cursor pagination, ETags, idempotency keys, rate limits

**Admin UI**
- 🟢 Sign-in, repository list and creation wizard, file browser
- 🟢 Users, groups, service accounts, personal tokens
- 🟢 Permission policy editor with a "why can X do Y" preview from the draft (Q-UI-21); the explain operation of R-AUTHZ-024 is P1 (Q-AUTHZ-27)
- 🟢 Endpoint screen with the exposure rule editor and its preview
- 🟢 Audit log and system status

**Tests**
- 🟢 Integration tests: login, tokens, permission evaluation, raw transfer
- 🟢 E2E tests: browser journeys across replicas with random distribution (Playwright with axe), and the exit checks E1.1, E1.8, E1.11, E1.13 and E1.14 on the kind cluster

## Phase 3 · npm registry (M2)

Details: [roadmap M2](../coverage/roadmap.md#m2--npm) · spec: [formats/npm.md](../spec/formats/npm.md)

**Engines shared by all registries**
- 🟢 Contract hardening from the M1 architecture review, before the format contract freezes ([roadmap WP2.0](../coverage/roadmap.md#m2--npm))
- 🟢 Format contract and conformance kit
- 🟢 Proxy engine: upstream settings, request collapsing through Valkey, metadata and negative caches, stale metadata while the upstream is down
- 🟢 Virtual engine: member order, merged metadata, deployment target, namespace claims, protection against dependency confusion

**npm**
- 🟢 Local: publish, package documents, dist-tags, unpublish, whoami, ping
- 🟢 Remote: proxy of the public npm registry, tarball URL rewriting, versions removed upstream
- 🟢 Virtual: merged package documents and dist-tags, writes through the deployment target
- 🟢 Clients: npm, pnpm, Yarn Classic, Yarn Berry
- 🟢 Package UI: package list and detail, README, install panel, client setup snippets
- 🟢 Repository settings form rendered from the format's settings schema (R-ARCH-004, R-UI-021, Q-ARCH-229, Q-UI-78 to Q-UI-82)
- 🟢 npm on a customer endpoint: package documents and dist-tags list only exposed versions, URLs on the endpoint's host, no upstream fetch from that endpoint by default
- 🟢 Security audit: `npm audit`, `pnpm audit` and `yarn npm audit` through remote and virtual repositories (R-NPM-038, owner 2026-10-31)
- 🟢 In-app guide for every npm variant: local, remote and virtual repositories with npm, pnpm, Yarn Berry and Yarn Classic (owner 2026-10-31)

**Tests**
- 🟢 npm integration tests: protocol over HTTP, conformance kit, nightly fuzzing of package documents (the three 30-minute fuzz legs passed in the closing run 41235811145)
- 🟢 npm e2e tests: install and publish with npm, pnpm, Yarn Classic and Yarn Berry through local, remote and virtual repositories
- 🟢 npm e2e: cold cache with three clients at once, upstream down, Valkey stopped, replica killed mid-install (the Valkey-stopped leg passed in the closing run 41235811145)
- 🟢 npm load in the drain and failover tests built (`TestNPMHA`, `TestNPMHACrash`, `make e2e SET=npm-ha`); since
  2026-11-01 they run in `HA and Performance (nightly)` and on dispatch, not per change (owner). Making them pass reliably,
  and the drain criterion of the security audit (R-NPM-038), moved to phase 7
- 🟢 npm e2e: a customer endpoint through its own Ingress lists only exposed versions; hidden versions answer 404

**Open findings of 2026-11-01, fixed in the closing run**
- 🟢 F-1 to F-16 and F-18 of [promt.md section 8](promt.md#8--open-findings): npm publish snippets, setup tokens through a
  virtual repository, the browse curl commands, the policy preview for service accounts, the `manage` help, revoke by value,
  service-account token actions, endpoint binding in the token forms, last use of tokens, the maintenance-mode row, the
  break-glass password status, the saved UI language, stored versions in the exposure check, underscore IDs in the coverage
  evidence, allocation-free pattern matching in `Decide`, the help journey with axe in both schemes, the status texts. The
  code and security reviews of the run found a readable-package leak in the exposure check and the reach of a virtual
  repository's token scope, and Test and Release found a light accent below 4.5:1; all fixed before release
- 🟢 Long tests out of the per-change pipeline (owner, 2026-11-01): `failover` and `npm-ha` run in
  `HA and Performance (nightly)`

## Phase 4 · NuGet (.NET) registry (M3)

Details: [roadmap M3](../coverage/roadmap.md#m3--nuget) · spec: [formats/nuget.md](../spec/formats/nuget.md)

**Carried over**
- 🟢 Renovate pull request #42 (TypeScript 7, a major update): it cannot merge while `@angular/compiler-cli` 22.2 and typescript-eslint accept only `typescript <6.1`; a Renovate rule holds TypeScript below 6.1 until Angular's peer range admits 7 ([ADR-140](../decisions.md#adr-140--typescript-is-held-below-61-until-angulars-peer-range-admits-7x))
- 🟢 CI checks against the latest release: `api/openapi.yaml` with a breaking-change checker (R-ARCH-025) and the embedded migration files (R-ARCH-026), job `compat` and `make compat-check`

**NuGet v3**
- 🟡 Local: service index, package content, registrations, search, push, unlist, relist, delete; search terms meet only the
  versions the caller may read and the repository's patterns apply (Q-NUGET-74, Q-NUGET-77). Open: rows stored before the
  SemVer 2 classification of dependency ranges keep `is_semver2 = false` (F-30 of [promt.md section 8](promt.md#8--open-findings))
- 🟢 Symbol package push (`.snupkg`): validated and stored, one indexing job per symbol package (Q-NUGET-11)
- 🟢 Remote: proxy of nuget.org with resource URL rewriting, discovery, re-paging, the identity check of a cached package
  (only content that is no package passes without it, Q-NUGET-84), pages and leaves bounded at the fill and the rendering
  (Q-NUGET-83), deprecations and vulnerabilities without the upstream's addresses (Q-NUGET-85), and the forwarded search
  with the withheld-query rule (R-NUGET-016); a claim filters the cached forwarded answers of every replica within the bound
  of R-NUGET-016, with and without Valkey (`TestNuGetSearchClaimTiming`, `TestNuGetClosing`)
- 🟢 Virtual: merged package content and metadata, a nuget.org member's registration merged whole (gzip-coded, pages by
  address, Q-NUGET-82), merged search over local and remote members decided on the addressed repository, four members at
  a time on one scan budget (Q-NUGET-75, Q-NUGET-76, Q-NUGET-81), publishing through the deployment target
- 🟢 Authentication: token as password in `nuget.config`, token as API key for push (`X-NuGet-ApiKey`, Q-AUTH-186)
- 🟡 Clients: the .NET 10 SDK on Linux and nuget.exe on Mono run against trove on every change (e2e `nuget`); setup
  instructions for Visual Studio in the guide. The Windows row and the manual Visual Studio checklist run in the owner
  walkthrough of phase 8 ([Q-NUGET-72](../spec/formats/nuget.md#q-nuget-72--the-windows-row-of-e31-and-the-visual-studio-checklist-run-in-the-owner-walkthrough-not-on-a-ci-runner))
- 🟢 Package UI and client setup snippets for .NET (`dotnet`, nuget.exe, `nuget.config`, the token in each shell's spelling)
- 🟢 In-app guide for every NuGet variant: local, remote and virtual repositories with the .NET CLI, nuget.exe and Visual Studio, and `nuget.config` (R-UI-049, owner 2026-10-31); browser journeys `10-help` and `11-nuget`
- 🟢 NuGet on a customer endpoint: version lists, registrations and search list only exposed versions, a search term meets
  only those, through a virtual repository its own rules decide, the validators follow only what the endpoint shows, and
  there is no push there (`TestNuGetEndpoint`; the search through a virtual `nuget-customers`:
  `TestR_NUGET_034_SearchThroughAVirtualRepositoryOnAnEndpointWithRules`;
  [nuget-virtual.md](../security/nuget-virtual.md), [endpoints-and-exposure.md](../security/endpoints-and-exposure.md))
- 🟢 Format contract additions found by the M2 architecture review (roadmap M3, WP3.0): a hook on a remote fill, discovery documents and the URL map for metadata, pass-through and fan-out for search, content on `Volatile` routes, a format that can name only its own repository, rendered-document mechanics in the contract, validation of remote configuration, the format's rules on the raw routes, a typed environment for a format, the dormant methods, the writer-owned eviction signal, no format knowledge in the virtual bridge, one engine value type each, links in refusals, the declared length of a body, a minimal contract, the per-format data-port leaf rule, the real populate decision in the audit event; and from the review of 2026-11-01 (WP3.0u to WP3.0w): the addressed repository on member requests through one derivation, stored versions as a method of the view adapter, and a test that the written contract equals the code
- 🟢 Format contract frozen for all later registries: `ContractVersion` 1, its surface recorded in
  `internal/formats/testdata/contract.txt`, npm and NuGet pass all eleven kit checks
  ([ADR-141](../decisions.md#adr-141--the-format-contract-is-frozen-at-version-1)); check 6 also holds a discovery parser to
  its bounds on crafted service indexes

**Tests**
- 🟢 NuGet integration tests: protocol over HTTP, conformance kit, SemVer 2 and ID normalization, golden vectors of the
  .NET version rules, the action map of R-AUTHZ-007 with and without each action (E3.4)
- 🟢 NuGet e2e tests: `dotnet restore`, `dotnet nuget push` and nuget.exe through local, remote and virtual repositories
  against the counting test upstream (set `nuget`, per change), and the run against nuget.org of E3.1 (`TestNuGetClosingPublic`,
  set `nuget-closing`), green on kind after the fix wave; its run in Test and Release on the commit that closes the phase is
  part of that closing run
- 🟡 NuGet e2e: cold cache with three clients at once (E3.2, one upstream request per `.nupkg`), a push whose answer the edge
  drops, documents and restores with Valkey stopped (set `nuget-closing`); the drain part moved to phase 7
  ([Q-NUGET-73](../spec/formats/nuget.md#q-nuget-73--the-nuget-part-of-the-drain-test-moves-to-phase-7-with-the-other-long-legs))
- ⚪ NuGet load joins the drain and failover tests: moved to phase 7 (Q-NUGET-73); the nightly leg `nuget-ha` runs its set
- 🟢 NuGet e2e: `dotnet restore` through a customer endpoint gets only exposed versions; a hidden version fails to resolve
  with `NU1102`, and a push answers 403 (`TestNuGetEndpoint`, Q-NUGET-71)

**Findings of 2026-11-02**
- 🟢 F-20 to F-26, F-28 and F-29 of [promt.md section 8](promt.md#8--open-findings), from the code review and the security
  audit of the phase: the forwarded search; the search decided on the addressed repository and on the versions the caller
  may read, with a bounded fan-out and the repositories' patterns; a nuget.org member's registration through a virtual
  repository; the local documents (SemVer 2 dependency ranges, validators on endpoints with rules, the lock order of a
  removal, the revision of the version list); upstream addresses in deprecations and vulnerabilities; the identity check
  and the bounds of a remote's cache fill; the package reader's central directory; the write budget of `access-long`; a kit
  check for discovery documents
- 🟢 The code review and the security audit of the fix wave: a `version:` term that carried a protected name upstream,
  upstream addresses in forwarded results, a forwarded result matched on a hidden version, an unbounded forwarded answer,
  a Unicode path ID past patterns and claims, unbounded leaves at a page fill, the search scan paging by offset, explicit
  folders left after a subtree delete, the validator of a remote document on an endpoint with rules; the remaining gaps are
  H-345, H-347, H-348 and H-351 to H-353 on the [security pages](../security/README.md)
- 🟢 R-NUGET-016's claim timing criterion, which the closing of F-20 left without a test, runs in e2e: no replica lists the
  claimed package 100 ms after the claim with Valkey and 860 ms after it with Valkey stopped (kind)
- 🟢 F-27: the run against nuget.org is green on kind, all seven cases (the forwarded search, nuget.exe, the restore and the
  registrations through the remote and the virtual repository)
- 🟡 Open: F-30 (`is_semver2` of rows stored before the fix)

## Phase 5a · Projects and delegated administration

Details: spec [authorization.md](../spec/authorization.md) R-AUTHZ-026 · decisions
[Q-AUTHZ-169](../spec/authorization.md#q-authz-169--a-technical-account-for-a-project-belongs-to-the-project) (owner)

The owner moved projects into the beta on 2026-11-02, before GitLab CI OIDC: project administrators create technical
accounts for external parties and give them fine or coarse access to chosen repositories of their project, and the
Bastion serves those parties. Phase 5 then maps GitLab pipelines into projects.

- ⚪ Spec first: R-AUTHZ-026 completed with project service accounts (Q-AUTHZ-169), the project screens and the open
  decision whether a project administrator may create remote repositories (a new egress path)
- ⚪ Projects: an immutable key, display name and description, the switches `managePolicies` and `manageRepositories`
  set by an administrator; a repository belongs to at most one project, repositories a project administrator creates
  carry the project key as prefix
- ⚪ Project administrators: users and groups named on the project; they hold `manage` on the project's repositories and,
  with the switches, manage its policies and repositories; they never bind system roles and see no global policy
- ⚪ Project policies: fine (one repository by key) or coarse (pattern or class) and only over the project's own
  repositories; the effect is the union with global policies
- ⚪ Project service accounts (owner, Q-AUTHZ-169): created, disabled and deleted by project administrators, named
  `<projectKey>-…`, their tokens issued and revoked by them, granted only through the project's policies; a project is
  empty, and can be deleted, only without repositories and service accounts
- ⚪ Admin UI: projects for administrators; for a project administrator the project's repositories, policies, service
  accounts and their tokens; the auditor sees every project read-only
- ⚪ Audit of every project change; security page for delegated administration
- ⚪ Integration tests: a project administrator cannot reach a repository, policy or principal outside the project, cannot
  bind a system role, cannot create a token that outlives or outranks the project's grants; e2e: a partner's project
  service account installs with npm and restores with dotnet through the main installation

## Phase 5 · GitLab CI OIDC (M4)

Details: [roadmap M4](../coverage/roadmap.md#m4--gitlab-ci-oidc) · spec: [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md)

- ⚪ GitLab as trusted issuer: discovery, signing keys, key rotation; several GitLab instances side by side (R-CI-005)
- ⚪ GitLab issuers declared in the Helm values (`config.ci.issuers`), applied by `trove migrate`, shown in trove and
  read-only there ([Q-CI-16](../spec/gitlab-ci-oidc.md#q-ci-16--ci-issuers-provisioned-through-the-helm-values-are-read-only-in-trove), owner 2026-11-02)
- ⚪ Token exchange: GitLab ID token (`id_tokens:` with trove as audience) in, short-lived CI token (`trv_cit_`) out;
  `CI_JOB_TOKEN` is not accepted (owner 2026-11-02; its fallback stays P2, R-CI-022)
- ⚪ Audit of the exchange: which pipeline (instance, group, project, branch, pipeline, job) signed in when, as issuance
  records with a tab per issuer in the admin UI (R-CI-020)
- ⚪ Admin manual: a runbook page for GitLab CI (register an instance in the Helm values, the `id_tokens:` block, a
  mapping, verify with the dry run, read the issuances, troubleshoot refusals) (owner 2026-11-02)
- ⚪ Identity mappings: conditions on project, branch, protected ref, environment; templated grants
- ⚪ Package ownership: a CI project writes only the packages it owns, reads what it is granted
- ⚪ Admin UI: CI issuers, mappings, dry run and claim tester
- ⚪ Reference pipeline: `.gitlab-ci.yml` publishing an npm package and pushing a NuGet package
- ⚪ Integration tests: invalid signatures, wrong audience, expired tokens, fork merge requests
- ⚪ E2E tests against a GitLab test instance for npm and NuGet, with a `web` node drained during the run

## Phase 5b · trove Bastion (M13)

Details: [roadmap M13](../coverage/roadmap.md#p1-platform-milestones) · spec: [bastion.md](../spec/bastion.md)

The owner moved the Bastion into the beta on 2026-11-02, before vulnerability scanning, so that phase 7 hardens it with
everything else. Its main purpose (owner): an exposed artifact instance that gives external parties read access to released
content, caches artifacts and takes as much load as possible off the main installation. It is exposed to the internet and
to denial-of-service attempts.

- 🟢 Spec page [bastion.md](../spec/bastion.md) first: deployment profile, trust between Bastion and main installation, the cache, and
  what the Bastion refuses. Decided by the owner on 2026-10-31: the Bastion asks the main installation whether a
  credential is valid and what it may read, and keeps the answers for a short, bounded time; it holds no copy of
  users, tokens or policies. Decided by the owner on 2026-11-02: the Bastion's own credential towards the main
  installation is a token that a trove administrator of the main installation issues for the Bastion (read only, nothing
  else); a client certificate is not its identity, because a TLS-terminating reverse proxy in front of the main
  installation would not pass it on. The Bastion connects to the main endpoint of the main installation (owner,
  2026-11-02): the Bastion replaces the customer endpoint of the earlier plans. Settled on the page: the bound (R-BAS-020,
  R-BAS-026), the interface on the main installation (R-BAS-008 to R-BAS-017), the renewal of the Bastion's token
  (R-BAS-006), and the state it keeps beyond its cache (answers in memory and, optionally, signed in Valkey; R-BAS-020,
  R-BAS-034). Catalog entries it pulls forward: the chained remote repository (RMT-040, RMT-041, RMT-044) and the read-only
  deployment profile with its trust between source and instance (DIST-024, DIST-025), raised to MVP with the page; RMT-042,
  RMT-043, RMT-045 and RMT-046 stay P2 (Q-BAS-47)
- ⚪ A second Helm chart (`trove-bastion`) for this instance type (owner, 2026-11-02) with its own Ingress, Service, NetworkPolicy and TLS Secret; horizontal replicas
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
- ⚪ What a caller reads (owner, 2026-11-02): there is no release step of its own. The right is the token's: a caller reads
  through the Bastion exactly what its token may read on the main installation, and read access behaves the same on both
- ⚪ How the Bastion fetches (owner, 2026-11-02): with the caller's token. On a cache miss it forwards the caller's token,
  the main installation decides and serves, and the Bastion caches the bytes; a cached artifact is served to another
  caller only after the main installation said that this caller may read it (an answer kept for the bound). The
  Bastion's own token identifies the Bastion and may only have tokens checked; it reads no package
- ⚪ How fast a revocation reaches the Bastion (owner, 2026-11-02): the Bastion keeps the main installation's answers for a
  configurable period (some minutes) and follows a revocation feed of the main installation every few seconds, so a
  revoked token or a narrowed right is dropped within seconds; widening a right may take the period
- ⚪ The main installation unreachable (owner, 2026-11-02): answers the Bastion already holds stay valid for a configurable
  grace period (proposed 15 minutes) beyond their normal period; callers with a valid answer get cached artifacts and
  possibly stale metadata, a token the Bastion has not seen gets `503`, and after the grace period it serves nothing
- ⚪ The cache (owner, 2026-11-02): a small part of the artifacts, never the main installation's object store, which the
  Bastion cannot read; its backend is chosen in the chart, a bucket of the Bastion's own shared by its replicas or a local
  volume per replica, with size limit, eviction by last access and the metadata revalidation period configurable
- ⚪ Shared state (owner, 2026-11-02): Valkey is optional. With it, the replicas share blocked addresses, rate-limit and
  failed-sign-in counters, the coordination of fetches and the main installation's answers; without it, each replica works
  alone with the budgets divided by the number of replicas; losing Valkey costs performance, never correctness (INV-3).
  With a local volume per replica, the chart can route by path so that each artifact lives on one replica
- ⚪ Anonymous reads (owner, 2026-11-02): as on the main installation; the Bastion admits anonymous requests exactly when
  the main installation does. Because an anonymous request costs an attacker nothing, the Bastion absorbs misses itself:
  a negative cache for names the main installation does not have, a budget of concurrent requests to the main
  installation that misses cannot exceed, and stricter per-address limits for requests without a credential
- ⚪ Audit of downloads (owner, 2026-11-02): the Bastion reports the downloads it serves from its cache to the main
  installation in batches (who, what, when, address; origin Bastion), under the same `audit.record_downloads` switch; it
  buffers them, bounded, while the main installation is unreachable and sends them later; events lost to a full buffer are
  counted and reported
- ⚪ No web interface (owner, 2026-11-02): the Bastion serves the package clients and raw downloads with tokens only; no
  login, no session, no UI
- ⚪ Configured only through its Helm chart (owner, 2026-11-02): the Bastion has no settings held anywhere else; every value
  (main installation, its token, cache, periods, limits, blocking, trusted proxies) is a chart value, and a change is a
  rollout
- ⚪ Blocking in two stages (owner, 2026-11-02): the Bastion itself enforces the rules that need trove's knowledge (failed
  authentication, misses, block duration, a block list shared through Valkey); its chart can put a coarser per-address
  limit in front at HAProxy Ingress, which can be switched off and is set wider than the Bastion's own. Volumetric attacks
  on bandwidth are the network provider's, not the Bastion's
- ⚪ The URL space of the main installation (2026-11-02): the Bastion holds no repository list; every repository of the main
  installation is reachable under the same paths, and the main installation renders documents for the Bastion's host
  (the Bastion sends its base URL as the forwarded host; the main installation lists the Bastion in `trusted_proxies` and
  `additional_base_urls`), so the Bastion carries no format knowledge
- ⚪ Metadata (owner, 2026-11-02): the main installation marks a document that is the same for every caller who reads the
  whole repository; the Bastion serves that copy for the metadata period to each caller for whom the main installation
  confirmed a full read of the repository (an answer kept like the others, under the revocation feed). A filtered
  document is never shared: it goes to the main installation per caller with a conditional request. Artifacts are shared
  after the main installation said the caller may read them
- ⚪ The Bastion's token (owner, 2026-11-02): renewed by hand with an overlap: an administrator issues a second token before
  the first expires, it reaches the Bastion's Secret through GitOps, the old one is revoked; the Bastion reloads a changed
  Secret without a restart, exports the remaining lifetime as a metric and warns in its log from a chart-set period before
  expiry (proposed 30 days). The Bastion holds no right to write Secrets or to issue tokens
- ⚪ Interface version (owner, 2026-11-02): the Bastion interface of the main installation (token checks, revocation feed,
  audit intake) has a version of its own, separate from the release number, raised only when that interface changes,
  which after the stable release is rare. Main installation and Bastion talk only when they speak the same interface
  version; on a mismatch the Bastion treats the main installation as unreachable (the grace period, then nothing) and
  raises a Prometheus alert (a metric plus a rule its chart ships)
- ⚪ The main installation counts the real client: the Bastion stops abuse before a request reaches the main installation,
  and the main installation lists the Bastion's addresses in `server.trusted_proxies`, so its per-address limits apply to
  the client and not to the Bastion
- ⚪ Internet exposure (owner, 2026-11-02): rate limits per client address, automatic blocking of addresses that flood it,
  a timeout for an address after too many failed authentication attempts, and a configurable cache (size, retention,
  metadata revalidation)

## Phase 6 · Vulnerability scanning (M4b)

Details: [roadmap M4b](../coverage/roadmap.md#m4b--vulnerability-scanning) · spec: [vulnerability-scanning.md](../spec/vulnerability-scanning.md)

The owner decided on 2026-10-31 that trove scans its stored packages itself and shows a vulnerability dashboard in the
beta. The phase comes before the HA phase, so that phase 7 hardens the scanner with everything else. The testing rule above
applies to the npm and NuGet parts of this phase.

**Engines**
- ⚪ Advisory mirror on `worker`: the OSV archives of npm and NuGet (GitHub Advisory Database, OpenSSF malicious packages), full and incremental sync with conditional and ranged requests, integrity and shrink guards, egress rules, sources status
- ⚪ Offline import for air-gapped installations from a generic repository
- ⚪ Known-exploited marking from the CISA catalog
- ⚪ Matching by package identity with each format's version order, as an optional interface of the frozen format contract; public advisories never applied to claimed names, claimed-name matches as a security alert
- ⚪ Findings: severity from CVSS, triage statuses and history, re-evaluation when the advisory data changes, rescan on demand, retention
- ⚪ Triage instance-wide by the `auditor` role, which sees every repository's metadata and findings but downloads, uploads
  and changes no artifact ([Q-AUTHZ-172](../spec/authorization.md#q-authz-172--the-auditor-reads-every-repositorys-metadata-and-triages-but-downloads-nothing), owner 2026-11-02)
- ⚪ Download blocking: malicious versions by default, every other block set and lifted in triage on a confirmed finding, no severity threshold (owner, 2026-10-31); on every route that serves a version, in the one item-lookup query; report mode for the whole installation

**Formats**
- ⚪ npm: `npm audit`, `pnpm audit` and `yarn npm audit` answered from trove's data on local, remote and virtual repositories; no package name leaves the installation
- ⚪ NuGet: `VulnerabilityInfo` from trove's data on every repository class, so `dotnet restore` reports NU1901 to NU1904
- ⚪ Blocked downloads in npm's and NuGet's error shapes

**API and UI**
- ⚪ Vulnerability report: counts by severity, trend, filterable and sortable list, finding detail, bulk triage, CSV export; per repository through its filter
- ⚪ Vulnerabilities tab on the package page; sources screen with the offline commands
- ⚪ Permissions: findings only where the caller can read the package; triage with the capability `vulnerabilities:triage`

**Tests**
- ⚪ Integration tests against PostgreSQL, Valkey with Sentinel and S3 with a test advisory source: sync paths and faults, the matching corpus per format, the finding lifecycle, blocking on every route, the API with permission-narrowed callers
- ⚪ E2E tests: npm, pnpm and Yarn audits and a blocked install, `dotnet restore` audit warnings and a blocked restore, through local, remote and virtual repositories on several `web` replicas with random distribution; browser journeys of the report, triage, blocking and export
- ⚪ E2E: a full sync drained mid-run finishes on another `worker`; the set once with Valkey stopped
- ⚪ Performance bounds V-1 to V-9 with 100,000 stored versions

## Phase 7 · HA hardening, backup and Helm (M5)

Details: [roadmap M5](../coverage/roadmap.md#m5--ha-hardening-and-helm-ga) · spec: [ha-and-helm.md](../spec/ha-and-helm.md), [backup-and-restore.md](../spec/backup-and-restore.md)

**High availability**
- ⚪ Failure suite: pod crash, PostgreSQL switchover and failover, Valkey failover and outage, S3 outage, node loss
- ⚪ Node drain under npm and NuGet load with zero failed requests; the NuGet load (`TestNuGetHA`) and R-ARCH-024's drain
  criterion move here from phase 4 ([Q-NUGET-73](../spec/formats/nuget.md#q-nuget-73--the-nuget-part-of-the-drain-test-moves-to-phase-7-with-the-other-long-legs))
- 🟡 npm load in the drain and failover tests reliably green (moved from phase 3, owner 2026-11-01). Diagnosis of
  2026-11-01: `make e2e SET=npm-ha` passes on a 17-CPU kind cluster (drain: 40,894 k6 requests, 0 failures, every job at
  attempt 1); with the kind nodes held to 6 CPUs the drain gets 1,062 `503`s, all `memory_slots` shedding of Q-ARCH-240 (no
  OOM, heap 111 MB), and the PostgreSQL switchover fails too. The scenario needs about 12 cores. In CI run 41322837920 the
  whole runner stalled for 40 s (worker loop silent, every backend timing out at once), the edge had no ready backend, leases
  expired and the reaper counted the attempts (no counting defect). CPU profile of a `web` pod: 57 % in the virtual tarball
  path, 35 % TLS writes, 30 % per-tarball PostgreSQL reads (`TarballFacts`, `items.read`, `proxy.getEntry`, `Tombstoned`).
  Open: a per-replica cache of the immutable tarball facts, shedding by memory in use instead of a worst case per request
  (after measuring a held download), the full settle deadline for the release writes at shutdown, and a load the runner
  can carry
- ⚪ The drain criterion of the security audit (R-NPM-038, moved from phase 3): the `web` pod that forwards an `npm audit`
  to a provider answering after 20 s is deleted 1 s in; `npm audit` succeeds and no pod gets `SIGKILL`
- ⚪ The default of `health.shutdown_drain_delay` (5 s, proposed) set from the endpoint-removal time of the target Ingress measured under load; the dev profile needs 15 s on the CI runners (Actions run 41241903588)
- ⚪ The Bastion under the failure suite: a Bastion replica drained and lost, the main installation unreachable and
  failing over, a rolling upgrade of either side
- ⚪ Vulnerability scanning under the failure suite: sync and evaluation jobs across drains and failovers, blocks and triage after a restore (E5.13)
- ⚪ Rolling upgrade from the previous pre-release with mixed versions
- ⚪ Performance targets met in two nightly runs in a row

**Backup and restore**
- ⚪ Backup job (CronJob): consistent database snapshot, blobs copied incrementally to the backup S3 target
- ⚪ Restore job into an empty installation
- ⚪ Revocation journal: a restore never brings back revoked tokens, removed permissions or narrowed exposure
- ⚪ E2E: backup taken during publishing restores with every npm and NuGet file digest-correct

**Helm chart and release**
- ⚪ Chart complete: values reference and schema, autoscaling, network policies, monitoring integration
- ⚪ Security documentation in [docs/security](../security/README.md) and security review
- 🟡 Release pipeline: `Release Docker & Helm` publishes the image `guidedtraffic/trove` (Docker Hub) with provenance and SBOM attestations and the chart (GitHub Pages) with each release ([releasing.md](../developer/releasing.md#the-release-pipeline)); it publishes every release since the switch to the organisation's CI standard. Open: E5.8, the beta candidate installed by digest from there passing E2.1's smoke scenarios
- ⚪ Beta candidate published

## Phase 8 · Walkthrough and beta release

Details: [roadmap: owner walkthrough](../coverage/roadmap.md#owner-walkthrough)

- ⚪ Guided walkthrough of the running beta candidate with the owner:
  - ⚪ OIDC sign-in, users, groups, tokens, permission policies
  - ⚪ npm and .NET clients against local, remote and virtual repositories, with nuget.exe and the .NET SDK on Windows and
    the Visual Studio 2022 checklist of R-NUGET-027 (E3.1, moved from phase 4,
    [Q-NUGET-72](../spec/formats/nuget.md#q-nuget-72--the-windows-row-of-e31-and-the-visual-studio-checklist-run-in-the-owner-walkthrough-not-on-a-ci-runner))
  - ⚪ GitLab reference pipeline
  - ⚪ Vulnerability report, a blocked malicious version, and the npm and .NET audits from trove's data
  - ⚪ Node drain and PostgreSQL and Valkey failover under load
  - ⚪ Backup by the CronJob and restore into an empty namespace
- ⚪ Objections from the walkthrough worked off
- ⚪ All exit checks green again
- 🟡 Beta documentation: installation guide, configuration reference, known limitations. The first edition of the
  [manual](../manual/README.md) (7 user and 12 administrator pages, among them installation, configuration and known
  limitations) is written against the code of 2026-11-01, embedded into the binary and shown behind the "?" of the UI
  (R-API-054, R-UI-050, owner 2026-11-01). Open: the pages follow every later phase, and the walkthrough reviews them
- 🟡 Design language of the owner's example screens in both colour schemes (Q-UI-88, owner 2026-11-01): palette, shell and
  theme button built and in the component tests; open: the browser tier's colour-contrast check (light and dark) in CI and the
  owner's review at the walkthrough
- ⚪ **Beta release** published

---

## Test matrix

| Area | Integration tests | E2E tests | Drain and failover load |
|---|---|---|---|
| Foundation (storage, coordination) | 🟢 | — | 🟢 |
| Access core (login, tokens, permissions) | 🟢 | 🟢 | 🟢 |
| **npm** | 🟢 | 🟢 | 🟡 |
| **NuGet (.NET)** | 🟢 | 🟡 | ⚪ |
| GitLab CI OIDC | ⚪ | ⚪ | ⚪ |
| Vulnerability scanning | ⚪ | ⚪ | ⚪ |
| Backup and restore | ⚪ | ⚪ | — |
